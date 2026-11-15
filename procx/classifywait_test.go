package procx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// TestClassifyWaitTailStartsAtRuneBoundary 回归（runtime#123）：stderr 尾巴
// 按字节切 `tail[len(tail)-400:]` 会腰斩 UTF-8——600 字节中文的切点落在
// 汉字中间，错误文案开头是半个字符。按 rune 回退后整条消息必须是合法 UTF-8，
// 且尾部只剩完整汉字。
func TestClassifyWaitTailStartsAtRuneBoundary(t *testing.T) {
	errBuf := newCappedBuffer(0)
	if _, err := errBuf.Write([]byte(strings.Repeat("中", 200))); err != nil { // 600 字节
		t.Fatal(err)
	}
	ctx := context.Background()
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	err := classifyWait(ctx, cctx, "cmd", errors.New("exit status 1"), errBuf, time.Minute)
	msg := err.Error()
	if !utf8.ValidString(msg) {
		t.Fatalf("错误文案含非法 UTF-8（按字节腰斩）: %q", msg)
	}
	const sep = ": exit status 1: "
	i := strings.Index(msg, sep)
	if i < 0 {
		t.Fatalf("错误文案缺少 stderr 尾巴分隔符: %q", msg)
	}
	tail := msg[i+len(sep):]
	if len(tail) == 0 || len(tail) > 400 {
		t.Fatalf("尾巴长度应在 (0,400] 字节内，实得 %d", len(tail))
	}
	if strings.Trim(tail, "中") != "" {
		t.Fatalf("rune 边界回退后应只剩完整汉字，实得 %q", tail)
	}
}

// TestTailSnippetShortUnchanged 负对照：不超过上限的尾巴原样返回。
func TestTailSnippetShortUnchanged(t *testing.T) {
	const s = "短错误"
	if got := tailSnippet(s, 400); got != s {
		t.Fatalf("未超上限应原样返回，实得 %q", got)
	}
	if got := tailSnippet(s, 0); got != "" {
		t.Fatalf("limit<=0 应返回空串，实得 %q", got)
	}
	// ASCII 恰好落在 400 字节边界时也必须完整。
	ascii := strings.Repeat("a", 401)
	if got := tailSnippet(ascii, 400); got != ascii[1:] {
		t.Fatalf("ASCII 尾巴应为最后 400 字节，实得长度 %d", len(got))
	}
}
