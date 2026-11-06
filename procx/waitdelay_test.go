package procx

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

// TestWaitDelayIsBenign 成功特判的三个条件（runtime#35）。
//
// 关键一行是「本地 Timeout 已到但 waitErr 仍是 ErrWaitDelay」：`ctx`（调用方 ctx）
// 干净、`cctx`（WithTimeout 派生）已 DeadlineExceeded——只查 ctx 的旧判据在这里
// 返回 true，把**被超时终止**的执行上报成 exit 0 成功，ErrTimeout 语义丢失。
//
// 这个竞态无法用真实进程稳定复现（要求 leader 的退出点与本地超时点重合），
// 故判据单独成函数、在此逐条钉死；良性路径由
// TestRunLeaderExitsWithDaemonGrandchild 端到端覆盖。
func TestWaitDelayIsBenign(t *testing.T) {
	// 良性形态：两个 ctx 都干净
	t.Run("ErrWaitDelay + 双 ctx 干净 = 成功", func(t *testing.T) {
		ctx := context.Background()
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if !waitDelayIsBenign(ctx, cctx, fmt.Errorf("wrap: %w", exec.ErrWaitDelay)) {
			t.Fatal("良性 ErrWaitDelay 应判成功（npm 类守护化孙进程不能把成功跑判成失败）")
		}
	})

	// 本条即 #35：本地超时已到，调用方 ctx 干净
	t.Run("ErrWaitDelay + 本地超时已到 = 失败", func(t *testing.T) {
		ctx := context.Background()
		cctx, cancel := context.WithTimeout(ctx, time.Nanosecond)
		defer cancel()
		<-cctx.Done() // 确保 DeadlineExceeded 已落定，不靠 sleep 猜时序
		if waitDelayIsBenign(ctx, cctx, exec.ErrWaitDelay) {
			t.Fatal("本地超时已到时必须走 classifyWait 报 ErrTimeout——判成功等于把被超时终止的执行说成正常完成")
		}
	})

	// 调用方取消：即使 waitErr 是 ErrWaitDelay 也不得判成功（ErrCanceled 语义）
	t.Run("ErrWaitDelay + 调用方取消 = 失败", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		<-ctx.Done()
		cctx, cancel2 := context.WithTimeout(context.Background(), time.Minute)
		defer cancel2()
		if waitDelayIsBenign(ctx, cctx, exec.ErrWaitDelay) {
			t.Fatal("调用方取消不得判成功")
		}
	})

	// 非 ErrWaitDelay：与 WaitDelay 无关的失败，任何 ctx 状态下都不是成功
	t.Run("非 ErrWaitDelay + 双 ctx 干净 = 失败", func(t *testing.T) {
		ctx := context.Background()
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if waitDelayIsBenign(ctx, cctx, errors.New("exit status 1")) {
			t.Fatal("普通失败不得判成功")
		}
	})

	// 单靠 errors.Is 是不够的：包一层的 ErrWaitDelay 仍要认（os/exec 会用
	// %w 包它），但包一层的「别的错」不能被误认成 ErrWaitDelay
	t.Run("嵌套判别", func(t *testing.T) {
		ctx := context.Background()
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		wrapped := fmt.Errorf("a: %w", fmt.Errorf("b: %w", exec.ErrWaitDelay))
		if !waitDelayIsBenign(ctx, cctx, wrapped) {
			t.Fatal("多层 %w 包裹的 ErrWaitDelay 应被认出")
		}
		if waitDelayIsBenign(ctx, cctx, fmt.Errorf("a: %w", errors.New("boom"))) {
			t.Fatal("非 ErrWaitDelay 不得判成功")
		}
	})
}
