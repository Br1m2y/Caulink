package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/accel"
	"github.com/Br1m2y/caulink/internal/ippool"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// stubAccel 是最小的 AccelController，只用来验路由与鉴权。
type stubAccel struct {
	started  []accel.Request
	canceled []string
	dirSet   string
	startErr error
}

func (s *stubAccel) Start(req accel.Request) (accel.TaskView, error) {
	if s.startErr != nil {
		return accel.TaskView{}, s.startErr
	}
	s.started = append(s.started, req)
	return accel.TaskView{ID: "t1", URL: req.URL, Status: accel.StatusQueued}, nil
}

func (s *stubAccel) Snapshot() []accel.TaskView {
	return []accel.TaskView{{ID: "t1", Status: accel.StatusDone, DoneBytes: 10, TotalBytes: 10}}
}

func (s *stubAccel) Cancel(id string) error {
	if id != "t1" {
		return accel.ErrNotFound
	}
	s.canceled = append(s.canceled, id)
	return nil
}

func (s *stubAccel) DefaultDir() string  { return `D:\downloads` }
func (s *stubAccel) MaxConnections() int { return accel.MaxConnections }

func (s *stubAccel) SetDefaultDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("保存目录不能为空")
	}
	s.dirSet = dir
	return dir, nil
}

func newAccelTestServer(t *testing.T, accelCtl AccelController) (string, func()) {
	t.Helper()
	pool := ippool.New()
	if err := pool.Upsert(contract.IPPoolEntry{
		ID: "lo@127.0.0.1", Interface: "lo", SourceIP: "127.0.0.1",
		AuthStatus: contract.AuthOnline, HealthScore: 1,
	}); err != nil {
		t.Fatal(err)
	}
	s := &StatusServer{Pool: pool, Accel: accelCtl}
	return startStatus(t, s)
}

func TestAccelTasksWithoutController(t *testing.T) {
	addr, stop := newAccelTestServer(t, nil)
	defer stop()

	resp, err := http.Get("http://" + addr + "/api/v1/accel/tasks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("未接线时应返回 501，实际 %d", resp.StatusCode)
	}
}

func TestAccelTasksGET(t *testing.T) {
	addr, stop := newAccelTestServer(t, &stubAccel{})
	defer stop()

	resp, err := http.Get("http://" + addr + "/api/v1/accel/tasks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body struct {
		Defaults AccelDefaults    `json:"defaults"`
		Tasks    []accel.TaskView `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Defaults.Dir != `D:\downloads` || body.Defaults.Connections != accel.DefaultConnections {
		t.Fatalf("defaults 不对: %+v", body.Defaults)
	}
	if len(body.Tasks) != 1 || body.Tasks[0].Status != accel.StatusDone {
		t.Fatalf("tasks 不对: %+v", body.Tasks)
	}
}

// 写操作必须要求 X-Caulink-Action：否则本机任意网页都能往磁盘里塞文件。
func TestAccelStartRequiresActionHeader(t *testing.T) {
	stub := &stubAccel{}
	addr, stop := newAccelTestServer(t, stub)
	defer stop()

	body := `{"url":"https://example.com/a.iso"}`
	resp, err := http.Post("http://"+addr+"/api/v1/accel/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("缺头应返回 403，实际 %d", resp.StatusCode)
	}
	if len(stub.started) != 0 {
		t.Fatal("缺头时不该真的开始任务")
	}
}

func TestAccelStartAndCancel(t *testing.T) {
	stub := &stubAccel{}
	addr, stop := newAccelTestServer(t, stub)
	defer stop()

	body := bytes.NewBufferString(`{"url":"https://example.com/a.iso","connections":4}`)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/v1/accel/tasks", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Caulink-Action", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("启动任务应 200，实际 %d", resp.StatusCode)
	}
	if len(stub.started) != 1 || stub.started[0].Connections != 4 {
		t.Fatalf("请求没有正确透传: %+v", stub.started)
	}

	creq, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/v1/accel/tasks/cancel",
		bytes.NewBufferString(`{"id":"t1"}`))
	creq.Header.Set("X-Caulink-Action", "1")
	cresp, err := http.DefaultClient.Do(creq)
	if err != nil {
		t.Fatal(err)
	}
	defer cresp.Body.Close()
	if cresp.StatusCode != http.StatusOK || len(stub.canceled) != 1 {
		t.Fatalf("取消失败: %d / %v", cresp.StatusCode, stub.canceled)
	}

	// 不存在的任务返回 404，而不是 200 假装成功。
	bad, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/v1/accel/tasks/cancel",
		bytes.NewBufferString(`{"id":"nope"}`))
	bad.Header.Set("X-Caulink-Action", "1")
	bresp, err := http.DefaultClient.Do(bad)
	if err != nil {
		t.Fatal(err)
	}
	defer bresp.Body.Close()
	if bresp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知任务应 404，实际 %d", bresp.StatusCode)
	}
}

func TestAccelStartPropagatesError(t *testing.T) {
	stub := &stubAccel{startErr: errors.New("只支持 http / https 直链")}
	addr, stop := newAccelTestServer(t, stub)
	defer stop()

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/v1/accel/tasks",
		strings.NewReader(`{"url":"ftp://x/y"}`))
	req.Header.Set("X-Caulink-Action", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 URL 应 400，实际 %d", resp.StatusCode)
	}
}

// 保证测试不依赖真实网络。
func TestAccelStubIsFast(t *testing.T) {
	start := time.Now()
	stub := &stubAccel{}
	if _, err := stub.Start(accel.Request{URL: "http://x"}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("stub 不应有耗时")
	}
}

func TestAccelSettingsDir(t *testing.T) {
	stub := &stubAccel{}
	addr, stop := newAccelTestServer(t, stub)
	defer stop()

	post := func(body string, withHeader bool) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/v1/accel/settings",
			strings.NewReader(body))
		if withHeader {
			req.Header.Set("X-Caulink-Action", "1")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// 和登录、取消一样：写操作必须带这个头
	resp := post(`{"dir":"D:/Downloads/caulink"}`, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("缺头应 403，实际 %d", resp.StatusCode)
	}

	resp = post(`{"dir":"D:/Downloads/caulink"}`, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("改目录应 200，实际 %d", resp.StatusCode)
	}
	if stub.dirSet != "D:/Downloads/caulink" {
		t.Fatalf("目录没有透传到控制器: %q", stub.dirSet)
	}

	var body struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Dir == "" {
		t.Fatal("响应里应带上规范化后的目录")
	}

	// 空目录由控制器报错，端点转成 400
	bad := post(`{"dir":""}`, true)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("空目录应 400，实际 %d", bad.StatusCode)
	}
}

func TestAccelSettingsRequiresController(t *testing.T) {
	addr, stop := newAccelTestServer(t, nil)
	defer stop()

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/v1/accel/settings",
		strings.NewReader(`{"dir":"D:/x"}`))
	req.Header.Set("X-Caulink-Action", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("未接线应 501，实际 %d", resp.StatusCode)
	}
}
