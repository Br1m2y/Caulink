package portal

import (
	"path/filepath"
	"testing"
)

func TestCachePersistsAcrossLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portal-map.json")
	c := Load(path)
	if got := c.Get("10.6.89.173"); got != "" {
		t.Fatalf("空缓存不应有值，实际 %q", got)
	}
	c.Set("10.6.89.173", "10.3.38.7:801")

	// 重新加载应读到落盘的值。
	again := Load(path)
	if got := again.Get("10.6.89.173"); got != "10.3.38.7:801" {
		t.Fatalf("重新加载后应命中缓存，实际 %q", got)
	}
}

func TestCacheIgnoresEmptyValues(t *testing.T) {
	c := Load("")
	c.Set("10.0.0.1", "")
	if got := c.Get("10.0.0.1"); got != "" {
		t.Fatalf("空值不应写入，实际 %q", got)
	}
}

func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
	}{
		{"10.3.38.7:801", "10.3.38.7", 801},
		{"10.3.38.7", "10.3.38.7", 801},
		{"portal.cau.edu.cn:8080", "portal.cau.edu.cn", 8080},
	}
	for _, c := range cases {
		host, port, err := SplitHostPort(c.in, 801)
		if err != nil || host != c.host || port != c.port {
			t.Errorf("SplitHostPort(%q) = (%q,%d,%v)，期望 (%q,%d)", c.in, host, port, err, c.host, c.port)
		}
	}
	if _, _, err := SplitHostPort("", 801); err == nil {
		t.Error("空地址应报错")
	}
}
