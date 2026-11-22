package download_test

import (
	"fmt"
	"os"
	"testing"
)

// 这组用例要真实校园网环境：两个已认证的源 IP、能访问公网的镜像站。
//
// 默认整包跳过，不然 `go test ./...` 在 CI 上会去下 20 MiB、再去绑一个
// 10.6.x 的地址，既跑不过也没意义。要看真实数据就自己带上环境变量：
//
//	Linux/macOS:  CAULINK_LIVE_TEST=1 go test ./test/download/ -v -timeout 600s
//	Windows CMD:  set CAULINK_LIVE_TEST=1 && go test ./test/download/ -v -timeout 600s
//	PowerShell:   $env:CAULINK_LIVE_TEST=1; go test ./test/download/ -v -timeout 600s
func TestMain(m *testing.M) {
	if os.Getenv("CAULINK_LIVE_TEST") == "" {
		fmt.Fprintln(os.Stderr, "跳过 test/download：这些用例需要真实校园网环境，设置 CAULINK_LIVE_TEST=1 再跑")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
