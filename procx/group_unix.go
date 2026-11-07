//go:build unix

// group_unix.go —— POSIX 进程组落地：Setpgid 建组 + 按 -pid 给整组发信号。
package procx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// unixGroup 用进程组（pgid）表达「一棵树」。
type unixGroup struct{ cmd *exec.Cmd }

// newGroupCmd 建组装配：Setpgid 让子进程成为新组的组长，于是它的所有
// 后代都落在同一个 pgid 下，`kill(-pid, sig)` 一次送达整棵树。
func newGroupCmd(cctx context.Context, dir string, argv, env []string) (*exec.Cmd, processGroup) {
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	g := &unixGroup{cmd: cmd}
	// CommandContext 缺省在 ctx done 时直接 SIGKILL leader，会抢在宽限前面——
	// 用 cmd.Cancel 接管为 TERM 整组，宽限内的 SIGKILL 由 WaitDelay 兜底。
	cmd.Cancel = func() error {
		err := g.kill(killGraceful)
		if errors.Is(err, errGroupGone) {
			// 子进程恰在 deadline 触发瞬间自然退出：组已不在，Kill 返回 ESRCH。
			// 按 os/exec 约定返回 ErrProcessDone——否则成功退出会被误当
			// Cancel 失败，Wait 报错（第六轮审计）。
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = killGrace
	return cmd, g
}

// attach unix 侧无需动作：Setpgid 在 Start 时就已生效。
func (g *unixGroup) attach() error { return nil }

func (g *unixGroup) kill(mode killMode) error {
	if g.cmd.Process == nil {
		return errGroupGone
	}
	sig := syscall.SIGTERM
	if mode == killForceful {
		sig = syscall.SIGKILL
	}
	err := syscall.Kill(-g.cmd.Process.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return errGroupGone
	}
	return err
}

// close unix 侧无平台资源需要释放。
func (g *unixGroup) close() {}
