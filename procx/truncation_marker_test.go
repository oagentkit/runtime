package procx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeScript 落一个可执行 shell 脚本（procx 用例的既有手法）。
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// truncMarker 判 stdout 是否带截断尾巴（noteTruncated 的固定文案片段）。
func truncMarker(s string) bool { return strings.Contains(s, "已截断") }

// TestTruncationMarkedOnFailurePath 失败路径同样必须留痕（runtime#38）。
//
// 修复前的形态：成功路径 `noteTruncated` 盖尾巴、失败路径裸返 out ⇒ 调用方
// 拿到的是被裁剪到 maxStdout 的失败现场，却当成全量，把「截断导致看不到
// 结论」误读成「命令没输出结论」。
func TestTruncationMarkedOnFailurePath(t *testing.T) {
	dir := t.TempDir()
	const limit = 4096
	// 刷满 stdout（超 limit）后以非零码退出
	fail := writeScript(t, dir, "flood-fail",
		"#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' 'a'\necho fatal >&2\nexit 3\n")

	stdout, stderr, code, err := Run(context.Background(), RunRequest{
		Argv: []string{fail}, Dir: dir, Timeout: 30 * time.Second, MaxStdout: limit,
	})
	if err == nil {
		t.Fatal("非零退出应返回 error")
	}
	if code != 3 {
		t.Fatalf("exitCode = %d, want 3", code)
	}
	if !strings.Contains(stderr, "fatal") {
		t.Fatalf("stderr 尾巴应保留: %q", stderr)
	}
	if len(stdout) >= 200000 {
		t.Fatalf("stdout 应被限容裁剪，实得 %d 字节", len(stdout))
	}
	if !truncMarker(stdout) {
		t.Fatalf("失败路径的截断 stdout 必须带标记，否则调用方会把它当全量（runtime#38）: 末尾 %q",
			stdout[max(0, len(stdout)-80):])
	}
}

// TestTruncationUnmarkedWhenUnderLimit 负对照：未超限的失败路径**不得**出现标记。
//
// 只有「开着绿」的用例等于没钉住——若标记无条件加，上一条照样过。
func TestTruncationUnmarkedWhenUnderLimit(t *testing.T) {
	dir := t.TempDir()
	fail := writeScript(t, dir, "small-fail", "#!/bin/sh\necho boom\nexit 2\n")

	stdout, _, code, err := Run(context.Background(), RunRequest{
		Argv: []string{fail}, Dir: dir, Timeout: 30 * time.Second, MaxStdout: 4096,
	})
	if err == nil || code != 2 {
		t.Fatalf("应非零退出: code=%d err=%v", code, err)
	}
	if truncMarker(stdout) {
		t.Fatalf("未超限不得加截断标记: %q", stdout)
	}
	if !strings.Contains(stdout, "boom") {
		t.Fatalf("stdout 应完整: %q", stdout)
	}
}

// TestTruncationMarkedOnTimeoutPath 超时/取消路径同样要留痕（第三条返回路径）。
func TestTruncationMarkedOnTimeoutPath(t *testing.T) {
	dir := t.TempDir()
	// 先刷满 stdout 再长睡，让本地超时在「已截断」之后发生
	slow := writeScript(t, dir, "flood-sleep",
		"#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' 'b'\nsleep 30\n")

	stdout, _, _, err := Run(context.Background(), RunRequest{
		Argv: []string{slow}, Dir: dir, Timeout: 400 * time.Millisecond, MaxStdout: 4096,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("应 errors.Is ErrTimeout: %v", err)
	}
	if !truncMarker(stdout) {
		t.Fatalf("超时路径的截断 stdout 必须带标记: 末尾 %q", stdout[max(0, len(stdout)-80):])
	}
}

// TestStderrTruncationMarked stderr 同病同治（上限 maxChildStdout）。
func TestStderrTruncationMarked(t *testing.T) {
	if testing.Short() {
		t.Skip("要往 stderr 写 >8MB，short 模式跳过")
	}
	dir := t.TempDir()
	// >8MB 写 stderr（maxChildStdout），stdout 保持干净
	loud := writeScript(t, dir, "loud-fail",
		"#!/bin/sh\nhead -c 9000000 /dev/zero | tr '\\0' 'e' >&2\nexit 4\n")

	stdout, stderr, code, _ := Run(context.Background(), RunRequest{
		Argv: []string{loud}, Dir: dir, Timeout: 60 * time.Second,
	})
	if code != 4 {
		t.Fatalf("exitCode = %d, want 4", code)
	}
	if truncMarker(stdout) {
		t.Fatalf("stdout 干净，不得有截断标记: %q", stdout)
	}
	if !truncMarker(stderr) {
		t.Fatalf("stderr 超 8MB 上限必须带标记，否则被裁剪的 stderr 会被当成全量")
	}
}

// TestSuccessPathStillMarked 回归：成功路径的既有行为不改。
func TestSuccessPathStillMarked(t *testing.T) {
	dir := t.TempDir()
	ok := writeScript(t, dir, "flood-ok",
		"#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' 'c'\n")

	stdout, _, code, err := Run(context.Background(), RunRequest{
		Argv: []string{ok}, Dir: dir, Timeout: 30 * time.Second, MaxStdout: 4096,
	})
	if err != nil || code != 0 {
		t.Fatalf("应成功: code=%d err=%v", code, err)
	}
	if !truncMarker(stdout) {
		t.Fatalf("成功路径的截断 stdout 必须带标记（既有行为）: %q", stdout[max(0, len(stdout)-80):])
	}
}
