// Package ippool 是源 IP 池的状态中心，全系统唯一共享的可变状态。
//
// 字段归属（这条规则不写死，A 和 B 一定会互相覆盖对方的字段）：
//   - AuthStatus、LastSeen：只有认证层（internal/auth）可以写 → UpdateAuth
//   - HealthScore、RTTMs、LossRate：只有健康检查层（internal/health）可以写 → UpdateHealth
//
// 因此本包刻意不提供"整条替换"的方法。Upsert 只在建池时用一次，
// 之后所有修改都走那两个窄接口。
package ippool

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// ErrNotFound 表示指定 ID 的条目不在池中。
var ErrNotFound = errors.New("ippool: 条目不存在")

// Pool 是并发安全的源 IP 池。
type Pool struct {
	mu      sync.RWMutex
	entries map[string]*contract.IPPoolEntry
}

// New 创建空池。
func New() *Pool {
	return &Pool{entries: make(map[string]*contract.IPPoolEntry)}
}

// Upsert 插入或整条替换一个条目。
//
// 只应在建池/新增源 IP 时调用。运行期改状态请用 UpdateAuth / UpdateHealth，
// 否则你会把对方刚写好的字段覆盖回零值。
func (p *Pool) Upsert(e contract.IPPoolEntry) error {
	if e.ID == "" {
		return errors.New("ippool: id 为空")
	}
	if e.Interface == "" {
		return fmt.Errorf("ippool: interface 为空 (id %q)", e.ID)
	}
	if ip := net.ParseIP(e.SourceIP); ip == nil || ip.IsUnspecified() {
		return fmt.Errorf("ippool: source_ip %q 非法 (id %q)", e.SourceIP, e.ID)
	}

	// 新条目可能还没经过任何一次探测，这里给合理的默认值，
	// 免得 LastSeen 零值让下游的 contract.Validate 失败。
	if e.AuthStatus == "" {
		e.AuthStatus = contract.AuthOffline
	}
	if e.LastSeen.IsZero() {
		e.LastSeen = time.Now().UTC()
	}
	if e.HealthScore < 0 {
		e.HealthScore = 0
	}
	if e.HealthScore > 1 {
		e.HealthScore = 1
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	copy := e
	p.entries[e.ID] = &copy
	return nil
}

// Remove 删除条目，返回是否真的删掉了。
func (p *Pool) Remove(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.entries[id]; !ok {
		return false
	}
	delete(p.entries, id)
	return true
}

// Get 取单个条目（返回副本）。
func (p *Pool) Get(id string) (contract.IPPoolEntry, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.entries[id]
	if !ok {
		return contract.IPPoolEntry{}, false
	}
	return *e, true
}

// Len 返回条目总数。
func (p *Pool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.entries)
}

// Snapshot 返回全部条目的一致快照，按 ID 排序保证顺序稳定。
//
// 返回的是值拷贝，调用方随便改，不影响池内状态。
func (p *Pool) Snapshot() []contract.IPPoolEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]contract.IPPoolEntry, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Schedulable 返回当前可分流的条目：认证在线且健康分大于 0。
//
// 已经是快照语义，调度器拿到的结果可以直接用来做决策，不需要再持锁。
func (p *Pool) Schedulable() []contract.IPPoolEntry {
	all := p.Snapshot()
	out := make([]contract.IPPoolEntry, 0, len(all))
	for _, e := range all {
		if e.Schedulable() {
			out = append(out, e)
		}
	}
	return out
}

// UpdateAuth 只改认证相关字段。认证层专用。
func (p *Pool) UpdateAuth(id string, status contract.AuthStatus, at time.Time) error {
	if !status.Valid() {
		return fmt.Errorf("ippool: auth_status %q 非法", status)
	}
	if at.IsZero() {
		at = time.Now()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	e.AuthStatus = status
	e.LastSeen = at.UTC()
	return nil
}

// UpdateHealth 只改链路质量字段。健康检查层专用。
//
// 越界值在这里夹紧而不是报错：探测抖动产生一个 1.2 的分数不该让整条链路被丢弃。
func (p *Pool) UpdateHealth(id string, score, rttMs, lossRate float64) error {
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	if rttMs < 0 {
		rttMs = 0
	}
	if lossRate < 0 {
		lossRate = 0
	}
	if lossRate > 1 {
		lossRate = 1
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	e.HealthScore = score
	e.RTTMs = rttMs
	e.LossRate = lossRate
	return nil
}
