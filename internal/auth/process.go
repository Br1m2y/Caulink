package auth

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// process 包装一个 BitSrunLoginGo 子进程。
//
// Wait 只能调用一次，所以启动后立刻在后台等，用 done channel 广播退出。
// 否则 "判断是否已退出" 和 "等待退出" 会互相抢 Wait，其中一个永远拿不到结果。
type process struct {
	cmd       *exec.Cmd
	logFile   *os.File
	startedAt time.Time

	done     chan struct{}
	exitCode int
	waitErr  error
}

func startProcess(binary, cfgPath, iface, dir string) (*process, error) {
	logPath := filepath.Join(dir, "bitsrun.stdout.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开子进程日志 %s: %w", logPath, err)
	}

	cmd := exec.Command(binary, "--config", cfgPath, "--interface", iface)
	// 直接接文件而不是管道：管道写满（64KB）而没人读时，子进程会阻塞在写日志上，
	// 表现为"认证进程活着但不工作"，极难排查。
	cmd.Stdout = f
	cmd.Stderr = f

	setProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, fmt.Errorf("启动 %s: %w", binary, err)
	}

	p := &process{
		cmd:       cmd,
		logFile:   f,
		startedAt: time.Now(),
		done:      make(chan struct{}),
	}
	go func() {
		defer close(p.done)
		p.waitErr = cmd.Wait()
		p.exitCode = describeExitCode(p.waitErr)
	}()
	return p, nil
}

// waitExit 阻塞到子进程退出，返回退出码。
func (p *process) waitExit() (int, error) {
	<-p.done
	return p.exitCode, p.waitErr
}

// alive 非阻塞地判断进程是否还在运行。
func (p *process) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// stop 先温和终止，超时再强杀。
func (p *process) stop(timeout time.Duration) error {
	if !p.alive() {
		return nil
	}

	if err := terminateProcess(p.cmd); err != nil {
		// 正常终止失败就直接强杀，不让错误冒泡打断上层流程。
		_ = killProcess(p.cmd)
	}

	select {
	case <-p.done:
		return nil
	case <-time.After(timeout):
	}

	_ = killProcess(p.cmd)
	select {
	case <-p.done:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("子进程 pid=%d 在强杀后仍无响应", p.cmd.Process.Pid)
	}
}

func (p *process) closeLog() {
	if p.logFile != nil {
		_ = p.logFile.Close()
		p.logFile = nil
	}
}

func (p *process) pid() int {
	if p.cmd.Process == nil {
		return -1
	}
	return p.cmd.Process.Pid
}

// describeExitCode 把 Wait 的错误翻译成可读的退出码。
// 被信号杀死时 ExitCode() 返回 -1，此时用信号值更有信息量。
func describeExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
