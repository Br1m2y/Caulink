// Package portal 记录"每个源 IP 该用哪个门户节点登录"。
//
// 背景：校园网 ePortal 不是单点，而是按网段/AC 分流的一组节点。实测同一台机器上
// 两块网卡分属不同网段时，HTTP 被不同节点劫持（例如一个被 10.3.191.9 管，
// 另一个被 10.3.38.7 管）。把登录提交给错误的节点时，门户照样返回 3.htm
// "登录成功"，但流量依旧被劫持——表现为"认证一直成功、网却一直不通"。
//
// 这里把"探测/登录时发现的正确节点"按源 IP 记下来，持久化到 JSON，供守护进程
// 与认证子进程共用。
package portal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Cache 是"源 IP → 门户 host:port"的持久化映射。
//
// 并发安全；写盘失败只忽略（缓存丢了最多多试一个候选节点，不影响正确性）。
type Cache struct {
	mu   sync.Mutex
	path string
	m    map[string]string
}

// Load 从 path 读取缓存。文件不存在或损坏时返回空缓存。
func Load(path string) *Cache {
	c := &Cache{path: path, m: make(map[string]string)}
	if path == "" {
		return c
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return c
	}
	for k, v := range m {
		if k != "" && v != "" {
			c.m[k] = v
		}
	}
	return c
}

// Get 返回某源 IP 缓存的门户地址，没有则返回空串。
func (c *Cache) Get(sourceIP string) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[sourceIP]
}

// Set 记住某源 IP 的门户地址并落盘。
func (c *Cache) Set(sourceIP, hostport string) {
	if c == nil || sourceIP == "" || hostport == "" {
		return
	}
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = make(map[string]string)
	}
	if c.m[sourceIP] == hostport {
		c.mu.Unlock()
		return
	}
	c.m[sourceIP] = hostport
	data, err := json.MarshalIndent(c.m, "", "  ")
	path := c.path
	c.mu.Unlock()
	if err != nil || path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// SplitHostPort 把 "host:port" 拆开；缺端口时补默认值。
func SplitHostPort(hostport string, defPort int) (string, int, error) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return "", 0, fmt.Errorf("portal: 地址为空")
	}
	if strings.Contains(hostport, ":") {
		var port int
		if _, err := fmt.Sscanf(hostport[strings.LastIndex(hostport, ":")+1:], "%d", &port); err == nil && port > 0 {
			host := hostport[:strings.LastIndex(hostport, ":")]
			if host != "" {
				return host, port, nil
			}
		}
	}
	return hostport, defPort, nil
}
