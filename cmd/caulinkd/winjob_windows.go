package main

import (
	"syscall"
	"unsafe"
)

// Windows 上"父进程死亡"默认**不会**带走子进程：桌面端用 TerminateProcess
// 结束 caulinkd 时，caulinkd 拉起的 caulink-auth 会变成孤儿，继续每 30 秒
// 探测并登录门户（用着启动那一刻的旧密码、旧门户地址），既污染会话又可能
// 触发门户风控。
//
// 对策是把守护进程放进一个带 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE 的作业对象：
// 作业句柄的最后一个引用关闭时（也就是守护进程进程对象销毁时，哪怕是强杀），
// 内核会把作业内的所有进程一起结束。子进程由 exec 创建时会自动继承作业成员身份，
// 不需要逐个 assign。
//
// 这是 Windows 上唯一可靠的"父死子亡"机制；进程内的 defer 在强杀下不会执行。

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x00002000
)

type joBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type joIOCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type joExtendedLimitInformation struct {
	BasicLimitInformation joBasicLimitInformation
	IoInfo                joIOCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var (
	kernel32Job                  = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32Job.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32Job.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32Job.NewProc("AssignProcessToJobObject")
	procGetCurrentProcessJob     = kernel32Job.NewProc("GetCurrentProcess")
)

// ensureProcessJob 把当前进程加入一个"关闭即杀"的作业对象。
//
// 返回的 cleanup 用于正常退出时显式关闭句柄（此时也会触发一次清场，
// 但那时子进程通常已经由认证层停掉了，属于幂等操作）。
//
// 失败不致命：拿不到作业对象只是失去这层兜底，启动时的孤儿清扫仍然有效，
// 所以这里只返回 error 交给调用方记日志，不阻断启动。
func ensureProcessJob() (cleanup func(), err error) {
	h, _, callErr := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return func() {}, callErr
	}

	info := joExtendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	if r, _, e := procSetInformationJobObject.Call(
		h,
		uintptr(jobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	); r == 0 {
		_ = syscall.CloseHandle(syscall.Handle(h))
		return func() {}, e
	}

	self, _, _ := procGetCurrentProcessJob.Call()
	// 注意：GetCurrentProcess 返回的是伪句柄 (-1)，不需要也不能关闭；
	// AssignProcessToJobObject 接受它。
	if r, _, e := procAssignProcessToJobObject.Call(h, self); r == 0 {
		_ = syscall.CloseHandle(syscall.Handle(h))
		return func() {}, e
	}

	return func() { _ = syscall.CloseHandle(syscall.Handle(h)) }, nil
}
