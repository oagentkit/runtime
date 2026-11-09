package procx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// procx#92：截断事实此前唯一的出口是 noteTruncated 追加在 stdout 尾部的人读
// 文案——tools#94 的 fail-closed 终态守门只能 `strings.Contains(stdout, "（stdout 超过")`
// + `strings.HasSuffix(trimmed, "已截断）")` 双条件尾缀匹配：文案一改即静默
// 失效；CLI 自身输出恰以此短语收尾则误判（拿文案当协议）。
// RunDetailed/RunResult 把「本次执行发生过截断」透出为结构化布尔事实，
// noteTruncated 文案降级为纯人读排障面。

// TestRunDetailedStdoutTruncatedFact stdout 截断事实：超限 ⇒ StdoutTruncated，
// 未超限 ⇒ false（反证）。与既有 noteTruncated 标记互为印证：布尔为真 ⇔
// 尾巴上有截断留痕（文案保留人读面，但不再是机器契约的唯一出口）。
func TestRunDetailedStdoutTruncatedFact(t *testing.T) {
	dir := t.TempDir()
	const limit = 4096

	flood := writeScript(t, dir, "flood",
		"#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' 'a'\n")
	res, err := RunDetailed(context.Background(), RunRequest{
		Argv: []string{flood}, Dir: dir, Timeout: 30 * time.Second, MaxStdout: limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.StdoutTruncated {
		t.Fatal("stdout 超限后 StdoutTruncated 必须 为 true（结构化事实缺失 = 消费方只能尾缀匹配文案，procx#92）")
	}
	if !res.Truncated() {
		t.Fatal("Truncated() 汇总必须为 true")
	}
	if !truncMarker(res.Stdout) {
		t.Fatal("人读截断尾巴必须保留（排障面不回退）")
	}

	quiet := writeScript(t, dir, "quiet", "#!/bin/sh\necho ok\n")
	res, err = RunDetailed(context.Background(), RunRequest{
		Argv: []string{quiet}, Dir: dir, Timeout: 30 * time.Second, MaxStdout: limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StdoutTruncated || res.StderrTruncated || res.Truncated() {
		t.Fatalf("未超限不得报截断: %+v", res)
	}
	if truncMarker(res.Stdout) {
		t.Fatalf("未超限不得有截断尾巴: %q", res.Stdout)
	}
}

// TestRunDetailedStderrTruncatedFact stderr 截断事实（上限 maxChildStdout），
// 且 stdout/stderr 两个事实互不串线。
func TestRunDetailedStderrTruncatedFact(t *testing.T) {
	if testing.Short() {
		t.Skip("要往 stderr 写 >8MB，short 模式跳过")
	}
	dir := t.TempDir()
	loud := writeScript(t, dir, "loud",
		"#!/bin/sh\nhead -c 9000000 /dev/zero | tr '\\0' 'e' >&2\nexit 4\n")
	res, err := RunDetailed(context.Background(), RunRequest{
		Argv: []string{loud}, Dir: dir, Timeout: 60 * time.Second,
	})
	if err == nil {
		t.Fatal("非零退出应返回 error")
	}
	if res.ExitCode != 4 {
		t.Fatalf("exitCode = %d, want 4", res.ExitCode)
	}
	if !res.StderrTruncated {
		t.Fatal("stderr 超 8MB 上限后 StderrTruncated 必须为 true")
	}
	if res.StdoutTruncated {
		t.Fatal("stdout 干净，不得报截断")
	}
	if res.Truncated() != (res.StdoutTruncated || res.StderrTruncated) {
		t.Fatalf("Truncated() 汇总口径不一致: %+v", res)
	}
}

// TestRunDetailedTruncatedOnFailureAndTimeoutPaths 截断事实必须在**所有**返回
// 路径透出（对齐 runtime#38 的结构化版本）：失败路径与超时路径均不得丢事实。
func TestRunDetailedTruncatedOnFailureAndTimeoutPaths(t *testing.T) {
	dir := t.TempDir()
	const limit = 4096

	fail := writeScript(t, dir, "flood-fail",
		"#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' 'a'\necho fatal >&2\nexit 3\n")
	res, err := RunDetailed(context.Background(), RunRequest{
		Argv: []string{fail}, Dir: dir, Timeout: 30 * time.Second, MaxStdout: limit,
	})
	if err == nil || res.ExitCode != 3 {
		t.Fatalf("非零退出: code=%d err=%v", res.ExitCode, err)
	}
	if !res.StdoutTruncated {
		t.Fatal("失败路径 StdoutTruncated 不得丢（runtime#38 的结构化等价）")
	}

	slow := writeScript(t, dir, "flood-sleep",
		"#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' 'b'\nsleep 30\n")
	res, err = RunDetailed(context.Background(), RunRequest{
		Argv: []string{slow}, Dir: dir, Timeout: 2 * time.Second, MaxStdout: limit,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("应 errors.Is ErrTimeout: %v", err)
	}
	if !res.StdoutTruncated {
		t.Fatal("超时路径 StdoutTruncated 不得丢")
	}
}

// TestRunDetailedTruncatedWithOnLine OnLine 回调路径同样透出截断事实
// （回调挂在 lineWriter 上，事实仍出自 cappedBuffer，不得因拆行而丢）。
func TestRunDetailedTruncatedWithOnLine(t *testing.T) {
	dir := t.TempDir()
	const limit = 4096
	flood := writeScript(t, dir, "flood-lines",
		"#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' 'a'\n")
	var lines int
	res, err := RunDetailed(context.Background(), RunRequest{
		Argv: []string{flood}, Dir: dir, Timeout: 30 * time.Second, MaxStdout: limit,
		OnLine: func(string) { lines++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if lines == 0 {
		t.Fatal("OnLine 应有回调（守门样本有效性）")
	}
	if !res.StdoutTruncated {
		t.Fatal("OnLine 路径 StdoutTruncated 不得丢")
	}
}

// TestRunDetailedParityWithRun RunDetailed 与 Run 事实一致（同一纪律两份出口，
// 不允许 RunDetailed 的字段与 Run 的返回值漂移）。
func TestRunDetailedParityWithRun(t *testing.T) {
	dir := t.TempDir()
	fail := writeScript(t, dir, "parity-fail",
		"#!/bin/sh\necho out-payload\necho err-payload >&2\nexit 5\n")
	req := RunRequest{Argv: []string{fail}, Dir: dir, Timeout: 30 * time.Second}

	out, errStr, code, err := Run(context.Background(), req)
	res, derr := RunDetailed(context.Background(), req)
	if (err == nil) != (derr == nil) || (err != nil && err.Error() != derr.Error()) {
		t.Fatalf("Run/RunDetailed err 漂移: %v vs %v", err, derr)
	}
	if out != res.Stdout || errStr != res.Stderr || code != res.ExitCode {
		t.Fatalf("Run/RunDetailed 返回漂移: (%q,%q,%d) vs %+v", out, errStr, code, res)
	}
	if !strings.Contains(res.Stdout, "out-payload") || !strings.Contains(res.Stderr, "err-payload") {
		t.Fatalf("样本自检: %+v", res)
	}
}
