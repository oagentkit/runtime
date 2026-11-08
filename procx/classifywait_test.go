package procx

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestClassifyWaitLocalDeadlineBeatsLateCancel 回归（runtime#63）：本地 Timeout
// 到点、调用方恰在超时之后才 cancel——真实超时必须报 ErrTimeout，不得被
// 「ctx.Err() 优先」的旧判据吞成 ErrCanceled（调用方按 ErrTimeout 走重试、按
// ErrCanceled 走放弃，误报直接把调用方引到错误的处置分支）。
//
// 这个竞态窗口（调用方的 cancel 落在子进程退出与 classifyWait 取值之间）无法
// 用真实进程稳定复现，按 waitDelayIsBenign 的先例抽判据、用构造的 ctx 逐条钉死。
func TestClassifyWaitLocalDeadlineBeatsLateCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cctx, cancel2 := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel2()
	<-cctx.Done() // 本地超时落定（真实超时）
	cancel()      // 调用方恰在超时后取消——issue 的边界场景

	err := classifyWait(ctx, cctx, "cmd", errors.New("exit status 1"), newCappedBuffer(0), time.Minute)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("本地超时落定后调用方才取消，应报 ErrTimeout: %v", err)
	}
	if errors.Is(err, ErrCanceled) {
		t.Fatalf("真实超时不得误报 ErrCanceled: %v", err)
	}
}

// TestClassifyWaitCallerDeadlineStaysCanceled 成对回归守卫：调用方**自己的
// deadline 更早到期**（本地 Timeout 更长）仍是 ErrCanceled——「本地超时优先」
// 不得把这条既有语义一起改掉（cctx.Err() 同样是 DeadlineExceeded，但那出自
// 调用方 deadline 经 parent 传播，不是本地预算耗尽）。
func TestClassifyWaitCallerDeadlineStaysCanceled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done() // 调用方自己的 deadline 先到期
	cctx, cancel2 := context.WithTimeout(ctx, time.Minute)
	defer cancel2()

	err := classifyWait(ctx, cctx, "cmd", errors.New("exit status 1"), newCappedBuffer(0), time.Minute)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("调用方自己的 deadline 到期应报 ErrCanceled（不得误报本地超时）: %v", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatalf("调用方 deadline 不得被误报成 ErrTimeout: %v", err)
	}
}

// TestClassifyWaitPlainCancelUnchanged 再一条守卫：调用方无 deadline 纯 cancel
// （现有 e2e 覆盖的形态）保持 ErrCanceled。
func TestClassifyWaitPlainCancelUnchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cctx, cancel2 := context.WithTimeout(ctx, time.Minute)
	defer cancel2()

	err := classifyWait(ctx, cctx, "cmd", errors.New("exit status 1"), newCappedBuffer(0), time.Minute)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("调用方取消应报 ErrCanceled: %v", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatalf("纯取消不得命中 ErrTimeout: %v", err)
	}
}

// TestClassifyWaitPlainFailureUnchanged 两个 ctx 都干净的非零退出：不带任何
// sentinel（普通失败语义不受本次改动影响）。
func TestClassifyWaitPlainFailureUnchanged(t *testing.T) {
	ctx := context.Background()
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	err := classifyWait(ctx, cctx, "cmd", errors.New("exit status 1"), newCappedBuffer(0), time.Minute)
	if errors.Is(err, ErrTimeout) || errors.Is(err, ErrCanceled) {
		t.Fatalf("普通失败不得命中 sentinel: %v", err)
	}
}
