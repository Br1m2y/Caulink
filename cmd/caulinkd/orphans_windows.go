package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// 启动时清扫上一轮遗留的认证子进程。
//
// 场景：用户关掉程序（桌面端强杀守护进程）→ caulink-auth 成为孤儿 →
// 它们仍在每 30 秒探测并登录门户，用的是旧密码、旧门户地址。下次启动若再
// 拉起一批，就会多份进程同时登录同一账号，既污染会话也可能触发门户风控。
//
// 作业对象（winjob_windows.go）能防住"以后"的孤儿，但防不住"升级前已经
// 留在机器上"的那些。所以启动时先扫一遍。

const (
	th32csSnapProcess          = 0x00000002
	processTerminate           = 0x0001
	processQueryLimitedInfoWin = 0x1000
	maxPathWin                 = 260
)

type processEntry32W struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [maxPathWin]uint16
}

var (
	kernel32Proc                  = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32Snapshot  = kernel32Proc.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW           = kernel32Proc.NewProc("Process32FirstW")
	procProcess32NextW            = kernel32Proc.NewProc("Process32NextW")
	procOpenProcess               = kernel32Proc.NewProc("OpenProcess")
	procQueryFullProcessImageName = kernel32Proc.NewProc("QueryFullProcessImageNameW")
	procTerminateProcess          = kernel32Proc.NewProc("TerminateProcess")
)

// killOrphanAuthProcesses 结束"不是本进程子进程"的 caulink-auth。
//
// 判定很保守：可执行文件路径必须与当前安装目录下的 caulink-auth.exe 完全一致。
// 这样不会误杀用户从别的目录启动的实例，也不会碰系统里同名的其它程序。
func killOrphanAuthProcesses(logger *slog.Logger) {
	self := uint32(os.Getpid())

	exeDir, err := os.Executable()
	if err != nil {
		return
	}
	wantPath, err := filepath.Abs(filepath.Join(filepath.Dir(exeDir), "caulink-auth.exe"))
	if err != nil {
		return
	}

	snap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snap == uintptr(syscall.InvalidHandle) || snap == 0 {
		return
	}
	defer syscall.CloseHandle(syscall.Handle(snap))

	var entry processEntry32W
	entry.Size = uint32(unsafe.Sizeof(entry))

	r, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	for r != 0 {
		name := syscall.UTF16ToString(entry.ExeFile[:])
		if strings.EqualFold(name, "caulink-auth.exe") &&
			entry.ProcessID != self && entry.ParentProcessID != self {

			if path, ok := processImagePath(uint32(entry.ProcessID)); ok &&
				strings.EqualFold(path, wantPath) {

				if killed := terminatePID(uint32(entry.ProcessID)); killed && logger != nil {
					logger.Info("已清理上一轮遗留的认证子进程",
						"pid", entry.ProcessID, "parent", entry.ParentProcessID)
				}
			}
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		r, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	}
}

// processImagePath 返回进程的可执行文件完整路径。
func processImagePath(pid uint32) (string, bool) {
	h, _, _ := procOpenProcess.Call(processQueryLimitedInfoWin, 0, uintptr(pid))
	if h == 0 {
		return "", false
	}
	defer syscall.CloseHandle(syscall.Handle(h))

	buf := make([]uint16, syscall.MAX_PATH)
	size := uint32(len(buf))
	r, _, _ := procQueryFullProcessImageName.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return "", false
	}
	return syscall.UTF16ToString(buf[:size]), true
}

// terminatePID 结束指定进程。
func terminatePID(pid uint32) bool {
	h, _, _ := procOpenProcess.Call(processTerminate, 0, uintptr(pid))
	if h == 0 {
		return false
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	r, _, _ := procTerminateProcess.Call(h, 1)
	return r != 0
}
