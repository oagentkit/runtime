//go:build windows

// group_windows.go —— Windows 进程树落地：Job Object。
//
// Windows 没有 POSIX 的进程组与信号：没有 pgid，没有 SIGTERM/SIGKILL，
// `syscall.Kill` 也不存在。把「整棵树当一个单元终止」这件事在 Windows 上
// 只有一条路——**Job Object**：把子进程放进一个 Job，终止时
// `TerminateJobObject` 杀掉 Job 内的全部进程，孙进程一并收场。
//
// 按 pid 逐个 Kill 拿不到孙进程，而孙进程残留正是 procx 要防的那类缺陷
// （包装器类命令常把真实工作进程守护化到后台）。故这里不用「杀 leader」
// 糊过去。
//
// # 已知差异（如实声明，不假装与 unix 等价）
//
//   - **没有优雅终止档**。Windows 无 SIGTERM 等价物，故 killGraceful 直接
//     整树强制终止：「TERM → 宽限 3s → KILL」在 Windows 上退化为「立即终止」。
//     killGrace 仍然生效，但它兜的只剩 WaitDelay 的管道收口。
//   - **不设 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE**。那需要
//     SetInformationJobObject 与按位布局的结构体（在 32/64 位上大小不同），
//     而本仓 CI 不在 Windows 上跑，无法验证布局正确性——写错的布局会让
//     SetInformationJobObject 静默失败（只回一个 bool），比不做更危险。
//     替代做法是正常路径与错误路径都显式 TerminateJobObject（见 kill/close），
//     覆盖「孙进程残留」这一实际失效模式。宿主进程被强杀时 Job 不会自动清树，
//     这是本实现与 unix 侧的已知差别。
package procx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObject = kernel32.NewProc("CreateJobObjectW")
	procAssignJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJob    = kernel32.NewProc("TerminateJobObject")
)

// 进程访问权位（winnt.h）。用常量而不是 syscall.PROCESS_*：
// 后者的可用性随 Go 版本变动，而这几个位是 Windows NT 以来未变 ABI。
const (
	processTerminate        = 0x0001
	processSetQuota         = 0x0100
	processQueryInformation = 0x0400
)

// windowsGroup 用 Job Object 表达「一棵树」。
type windowsGroup struct {
	cmd *exec.Cmd
	job syscall.Handle // 0 = 尚未托管
}

// newGroupCmd 装配命令。
//
// 刻意**不设** SysProcAttr：Windows 上「整组」由 Job 表达，而
// CREATE_NEW_PROCESS_GROUP 只影响控制台 Ctrl+C 的送达面，与本包
// 「超时/取消后整树收场」的承诺无关。少设一个标志，就少一处需要在
// 无法运行 Windows 的机器上靠想象验证的行为。
func newGroupCmd(cctx context.Context, dir string, argv, env []string) (*exec.Cmd, processGroup) {
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = env

	g := &windowsGroup{cmd: cmd}
	cmd.Cancel = func() error {
		err := g.kill(killGraceful)
		if errors.Is(err, errGroupGone) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = killGrace
	return cmd, g
}

// attach 建 Job 并把已启动的子进程放进去。
//
// 必须在 Start 之后调用：AssignProcessToJobObject 要的是进程句柄，而句柄
// 只有 Start 之后才有。attach 失败一律当致命——不托管就兑现不了「整树收场」，
// 此时调用方按「启动失败」处理（process.go 会杀掉并 Wait 回收，不留孤儿）。
func (g *windowsGroup) attach() error {
	if g.cmd.Process == nil {
		return fmt.Errorf("procx: 子进程未启动，无法托管进程树")
	}
	h, _, cerr := procCreateJobObject.Call(0, 0)
	if h == 0 {
		return fmt.Errorf("procx: CreateJobObject 失败: %w", cerr)
	}
	job := syscall.Handle(h)

	proc, oerr := syscall.OpenProcess(processTerminate|processSetQuota|processQueryInformation,
		false, uint32(g.cmd.Process.Pid))
	if oerr != nil {
		_ = syscall.CloseHandle(job)
		return fmt.Errorf("procx: OpenProcess(%d) 失败: %w", g.cmd.Process.Pid, oerr)
	}
	defer func() { _ = syscall.CloseHandle(proc) }()

	if r, _, aerr := procAssignJobObject.Call(uintptr(job), uintptr(proc)); r == 0 {
		_ = syscall.CloseHandle(job)
		return fmt.Errorf("procx: AssignProcessToJobObject 失败: %w", aerr)
	}
	g.job = job
	return nil
}

// kill 终止整棵树。两档在 Windows 上走同一条路（无优雅终止等价物，见文件头）。
func (g *windowsGroup) kill(mode killMode) error {
	if g.job == 0 {
		// 尚未托管（Start 失败 / attach 失败）：没有可终止的树。
		return errGroupGone
	}
	if r, _, err := procTerminateJob.Call(uintptr(g.job), 1); r == 0 {
		return fmt.Errorf("procx: TerminateJobObject 失败: %w", err)
	}
	return nil
}

// close 先终止残留（Wait 已返回、但可能仍有孙进程握资源），再释放句柄。
func (g *windowsGroup) close() {
	if g.job == 0 {
		return
	}
	_, _, _ = procTerminateJob.Call(uintptr(g.job), 1)
	_ = syscall.CloseHandle(g.job)
	g.job = 0
}
