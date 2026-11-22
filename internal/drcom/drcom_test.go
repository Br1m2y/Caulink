package drcom

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newTestClient 把 Client 指向本地 httptest 服务器。
//
// 注意：这里不测"绑定源 IP"——那个只能在真机上验证（见 ADR 0004）。
func newTestClient(t *testing.T, handler http.HandlerFunc) (Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("解析测试服务器地址: %v", err)
	}
	port := 0
	fmt.Sscanf(u.Port(), "%d", &port)
	return Client{
		Portal:     u.Hostname(),
		Port:       port,
		Username:   "2023001",
		Realm:      "cau",
		Password:   "secret",
		SourceIP:   "127.0.0.1",
		ProbeURL:   srv.URL + "/connecttest.txt",
		HTTPClient: srv.Client(),
	}, srv
}

func TestLoginSubmitsRealmAccount(t *testing.T) {
	var gotUser, gotPass string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("登录应为 POST，实际 %s", r.Method)
		}
		_ = r.ParseForm()
		gotUser = r.Form.Get("DDDDD")
		gotPass = r.Form.Get("upass")
		http.Redirect(w, r, "/3.htm?wlanuserip=10.6.18.3", http.StatusFound)
	})

	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("登录应成功: %v", err)
	}
	// 账号必须带 realm 后缀，否则 RADIUS 报 no AT symbol in realm。
	if gotUser != "2023001@cau" {
		t.Errorf("提交的账号应为 2023001@cau，实际 %q", gotUser)
	}
	if gotPass != "secret" {
		t.Errorf("密码未正确提交: %q", gotPass)
	}
}

func TestLoginRejectedCarriesRetCode(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r,
			"/2.htm?RetCode=Rad:Misconfigured+client:+no+AT+symbol+in+realm",
			http.StatusFound)
	})

	err := client.Login(context.Background())
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !errors.Is(err, ErrRejected) {
		t.Errorf("应包装 ErrRejected，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "no AT symbol in realm") {
		t.Errorf("错误信息应含门户返回的原因，实际 %v", err)
	}
}

func TestLoginUnrecognizedResponseIsError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // 既不是 3.htm 也不是 2.htm
	})
	if err := client.Login(context.Background()); err == nil {
		t.Fatal("未识别的响应应报错，不能当成成功")
	}
}

func TestLogoutRequiresSuccessPage(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/2.htm?wlanuserip=127.0.0.1&ACLogOut=1", http.StatusFound)
	})
	if err := client.Logout(context.Background()); err != nil {
		t.Fatalf("注销应成功: %v", err)
	}
}

// 这是最关键的一条回归测试：未认证时 portal 会 302 到登录页，
// 若客户端跟随重定向，就会拿到登录页的 200 而误判"在线"，
// 事件驱动重认证将永远不会触发。
func TestProbeTreatsRedirectAsOffline(t *testing.T) {
	var hitLoginPage bool
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connecttest.txt":
			http.Redirect(w, r, "/login", http.StatusFound)
		case "/login":
			hitLoginPage = true
			fmt.Fprint(w, "Microsoft Connect Test") // 登录页也含同样文字
		default:
			http.NotFound(w, r)
		}
	})

	online, err := client.Probe(context.Background())
	if err != nil {
		t.Fatalf("探测不应报错: %v", err)
	}
	if online {
		t.Fatalf("302 必须判为离线（客户端跟随重定向会误判，见 %s）", srv.URL)
	}
	if hitLoginPage {
		t.Error("不应跟随重定向到登录页")
	}
}

func TestProbeDetectsFakeOKPage(t *testing.T) {
	// 有些劫持页返回 200 但内容是登录页：不能当成在线。
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>请先登录</html>")
	})
	online, err := client.Probe(context.Background())
	if err != nil {
		t.Fatalf("探测不应报错: %v", err)
	}
	if online {
		t.Fatal("内容不匹配的 200 必须判为离线")
	}
}

func TestProbeOnline(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Microsoft Connect Test")
	})
	online, err := client.Probe(context.Background())
	if err != nil {
		t.Fatalf("探测不应报错: %v", err)
	}
	if !online {
		t.Fatal("正常内容应判为在线")
	}
}

func TestAccountFallsBackWithoutRealm(t *testing.T) {
	c := Client{Username: "2023001"}
	if got := c.Account(); got != "2023001" {
		t.Errorf("无 realm 时应原样返回账号，实际 %q", got)
	}
}
