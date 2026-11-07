//go:build windows

// group_windows_test.go —— Windows 侧的进程树托管用例。
//
// 这两个用例**没有**在编写时于 Windows 上跑过：本仓 CI 只用 ubuntu runner，
// 作者手上也没有 Windows 机器。它们的角色是为「将来接上 Windows runner」
// 预先钉住两条判据——编译期由 `GOOS=windows go vet ./procx` 保证（vet 会
// 分析测试文件），运行期留给第一个在 Windows 上跑本仓的人。
//
// 如实标注这一点，比写成「已通过」更有用：一条没跑过的用例不该被当成证据。
package procx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRunWindows 基本路径：Job 托管之后子进程仍能正常执行并回传 stdout。
// 这条抓的是「attach 把进程放进 Job 之后它就跑不起来」这类装配错误——
// 例如 OpenProcess 的访问权不足或 AssignProcessToJobObject 用错句柄。
func TestRunWindows(t *testing.T) {
	stdout, _, _, err := Run(context.Background(), RunRequest{
		Argv: []string{"cmd", "/c", "echo", "procx-windows"},
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if !strings.Contains(stdout, "procx-windows") {
		t.Fatalf("stdout 未包含预期输出: %q", stdout)
	}
}

// TestRunWindowsKillsTreeOnTimeout 超时路径：Timeouts 到点后必须整树终止
// 并按 ErrTimeout 分类。ping 在 Windows 上会持续 ~59 秒，足以证明终止发生在
// 超时而非自然退出。
//
// 它验的是「TerminateJobObject 被真的调用且成功」这一条；**孙进程是否
// 真的死掉**需要按 pid 观察，本用例不做（无 tasklist 解析的可靠口径）——
// 那一层的判据留给有 Windows 环境时的专门用例。
func TestRunWindowsKillsTreeOnTimeout(t *testing.T) {
	start := time.Now()
	_, _, _, err := Run(context.Background(), RunRequest{
		Argv:    []string{"cmd", "/c", "ping", "-n", "60", "127.0.0.1"},
		Timeout: 300 * time.Millisecond,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("应判为 ErrTimeout，实际: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("超时终止耗时过长（%s）：整树终止可能没有生效", elapsed)
	}
}
