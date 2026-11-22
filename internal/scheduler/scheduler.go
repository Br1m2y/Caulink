// Package scheduler 决定每一条新连接从哪个源 IP 发起。
//
// 关键前提（《要求.md》§2）：单条 TCP 连接的源/目的地址在连接期内固定，
// 所以这里做的是**连接级**负载均衡，不是单连接多路径聚合。
// 任何声称"一个连接跑两条线"的方案都是错的。
package scheduler

import (
	"errors"
	"math"
	"sort"
	"sync"

	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/netsrc"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// ErrNoCandidate 表示当前没有任何可调度的源 IP。
var ErrNoCandidate = errors.New("scheduler: 没有可调度的源 IP（全部离线或健康分为 0）")

// errNilPool 由 New 在参数缺失时返回。
var errNilPool = errors.New("scheduler: pool 不能为空")

// stickyHysteresis 是敏感流量"粘住"的容忍阈值。
//
// 当前绑定的 IP 只要健康分不低于最优者的 80%，就继续用它。
// 不放这个滞回，两条链路分数小幅交替时会让游戏/会议每次重连都换 IP，
// 而换 IP 意味着服务端看到一个新会话 —— 掉线、重连、语音重连全来了。
// 对这种流量，"稳"比"最优"重要一个数量级。
const stickyHysteresis = 0.8

// stickyKey 是敏感流量的粘性键。MVP 把所有敏感流量固定到同一个 IP
// （《要求.md》§3 的调度规则就是这么定的）。将来要按流细分时改这里。
const stickyKey = "interactive"

// Scheduler 按 IP 池的实时状态分配连接。
type Scheduler struct {
	pool       *ippool.Pool
	classifier Classifier

	mu sync.Mutex
	// current 是平滑加权轮询的中间量，见 pickSWRRLocked。
	current map[string]int
	// sticky 记录敏感流量当前绑定的 IP。
	sticky map[string]string
}

// New 创建调度器。classifier 为 nil 时所有流量按 bulk 处理。
func New(pool *ippool.Pool, classifier Classifier) (*Scheduler, error) {
	if pool == nil {
		return nil, errNilPool
	}
	return &Scheduler{
		pool:       pool,
		classifier: classifier,
		current:    make(map[string]int),
		sticky:     make(map[string]string),
	}, nil
}

// Pick 为一条新连接选择源 IP。
//
// network 形如 "tcp"/"udp"，addr 形如 "host:port"。两者都只用于流量分类，
// 不参与选路计算。
func (s *Scheduler) Pick(network, addr string) (contract.IPPoolEntry, error) {
	entries := s.pool.Schedulable()

	// 目标是 IPv6 字面量时，只能用有 IPv6 源地址的条目。
	// 否则内核会以 WSAEADDRNOTAVAIL 拒绝建连（源地址族不匹配），
	// 表现为"选了一个 IP 但一条连接都建不起来"。
	if wantV6, known := netsrc.WantsIPv6(addr); known && wantV6 {
		filtered := entries[:0:0]
		for _, e := range entries {
			if netsrc.HasFamily(e, true) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	if len(entries) == 0 {
		return contract.IPPoolEntry{}, ErrNoCandidate
	}

	class := ClassBulk
	if s.classifier != nil {
		class = s.classifier.Classify(network, addr)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 池会增删 IP，长期运行必须清掉已消失条目的中间状态，否则 map 只增不减。
	s.gcLocked(entries)

	if class == ClassInteractive {
		return s.pickStickyLocked(entries), nil
	}
	return s.pickSWRRLocked(entries), nil
}

// StickyID 返回敏感流量当前绑定的 IP，供 UI 展示"游戏/会议走的哪条线"。
func (s *Scheduler) StickyID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sticky[stickyKey]
}

// pickSWRRLocked 是平滑加权轮询（smooth weighted round-robin）。
//
// 每轮给每个候选加上自己的权重，选当前值最大的，再把它的值减去总权重。
// 相比"按权重随机"，它严格保证在 N 次分配内各 IP 拿到的比例等于权重比，
// 且不会出现某个 IP 连续被选中多次导致瞬时打满的情况。
//
// 相比普通加权轮询（按权重展开成序列），它不需要预先把权重变成固定长度的列表，
// 权重随健康分实时变化时不需要重建序列。
func (s *Scheduler) pickSWRRLocked(entries []contract.IPPoolEntry) contract.IPPoolEntry {
	total := 0
	bestIdx := 0
	bestCur := math.MinInt

	for i, e := range entries {
		w := weightOf(e)
		s.current[e.ID] += w
		total += w
		if s.current[e.ID] > bestCur {
			bestCur = s.current[e.ID]
			bestIdx = i
		}
	}

	best := entries[bestIdx]
	s.current[best.ID] -= total
	return best
}

// pickStickyLocked 把敏感流量固定到当前最优 IP，但带滞回。
func (s *Scheduler) pickStickyLocked(entries []contract.IPPoolEntry) contract.IPPoolEntry {
	best := bestOf(entries)

	if id, ok := s.sticky[stickyKey]; ok {
		if cur, found := find(entries, id); found {
			if cur.HealthScore >= best.HealthScore*stickyHysteresis {
				return cur
			}
		}
	}

	s.sticky[stickyKey] = best.ID
	return best
}

func (s *Scheduler) gcLocked(entries []contract.IPPoolEntry) {
	alive := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		alive[e.ID] = struct{}{}
	}
	for id := range s.current {
		if _, ok := alive[id]; !ok {
			delete(s.current, id)
		}
	}
	for k, id := range s.sticky {
		if _, ok := alive[id]; !ok {
			delete(s.sticky, k)
		}
	}
}

// weightOf 把健康分转成整数权重。
//
// 乘 100 是为了让 SWRR 的整数运算保留两位精度。下限 1 很关键：
// 健康分 0.004 会取整成 0，权重 0 意味着这个 IP 永远不会被选中，
// 但它的 HealthScore > 0 又让它留在可调度集合里，形成"看得见用不上"的僵尸条目。
func weightOf(e contract.IPPoolEntry) int {
	w := int(math.Round(e.HealthScore * 100))
	if w < 1 {
		w = 1
	}
	return w
}

// WeightOf 暴露单条目的调度权重，供状态接口与 UI 展示。
//
// 只读用途。不要用它绕开 Pick 自己做选路 —— 那会丢掉 SWRR 的中间状态和敏感流量的粘性，
// 结果是"看起来在按权重分配，实际把游戏流量甩来甩去"。
func WeightOf(e contract.IPPoolEntry) int {
	return weightOf(e)
}

// bestOf 选健康分最高者；并列时取 RTT 更低者；再并列按 ID 保证结果稳定。
func bestOf(entries []contract.IPPoolEntry) contract.IPPoolEntry {
	sorted := make([]contract.IPPoolEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].HealthScore != sorted[j].HealthScore {
			return sorted[i].HealthScore > sorted[j].HealthScore
		}
		if sorted[i].RTTMs != sorted[j].RTTMs {
			return sorted[i].RTTMs < sorted[j].RTTMs
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted[0]
}

func find(entries []contract.IPPoolEntry, id string) (contract.IPPoolEntry, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return contract.IPPoolEntry{}, false
}
