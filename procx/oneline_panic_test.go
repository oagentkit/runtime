package procx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestRunOnLinePanicIsolated 回归（runtime#70）：OnLine 回调里的消费方 bug
// （panic）不得炸宿主进程。回调运行在 os/exec 的**内部拷贝 goroutine** 上
// （writerDescriptor 的裸 io.Copy，无 recover），panic 顺 Write → io.Copy
// 打成进程级 fatal——调用方在自己 goroutine 上 defer recover 拦不住另一条
// goroutine，旧实现下整个进程 exit status 2。
//
// probe 以子进程形态跑（re-exec 测试二进制 + 环境变量开关）：否则红测阶段
// 的 panic 会炸掉整个 go test 进程，而不是留下一条失败用例。
func TestRunOnLinePanicIsolated(t *testing.T) {
	if os.Getenv("AKX_PROCX_ONLINE_PANIC_PROBE") == "1" {
		runOnLinePanicProbe()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunOnLinePanicIsolated$", "-test.v")
	cmd.Env = append(os.Environ(), "AKX_PROCX_ONLINE_PANIC_PROBE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe 进程非零退出——OnLine panic 未被收编（runtime#70）: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PROBE-SURVIVED") {
		t.Fatalf("probe 未走到存活标记:\n%s", out)
	}
	if strings.Contains(string(out), "PROBE-RECOVERED-IN-CALLER") {
		t.Fatalf("收编必须发生在 procx 库层——调用方 recover 拦不住 exec 拷贝 goroutine:\n%s", out)
	}
}

// runOnLinePanicProbe 在子进程里复现 issue 的场景：OnLine 在 marker 行 panic
// （消费方 bug），调用方 goroutine 上挂 defer recover（旧实现下它拦不住 exec
// 内部 goroutine 的 panic，进程仍死）。
func runOnLinePanicProbe() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("PROBE-RECOVERED-IN-CALLER:", r)
		}
	}()
	var lines []string
	stdout, _, code, err := Run(context.Background(), RunRequest{
		Argv: []string{"sh", "-c", "echo marker-line; echo after-line"},
		OnLine: func(line string) {
			lines = append(lines, line)
			if strings.Contains(line, "marker-line") {
				panic("consumer bug: assignment to entry in nil map")
			}
		},
	})
	if err != nil {
		fmt.Println("PROBE-RUN-ERR:", err)
		return
	}
	if code != 0 {
		fmt.Println("PROBE-RUN-CODE:", code)
		return
	}
	// 收编后的期望形态：panic 后本次执行不再回调后续行（缺陷已暴露，重复回调
	// 只会重复炸同一处），但 stdout 全文仍照常采集兜底——「丢行回调」不等于
	// 「丢命令结果」。
	if !strings.Contains(stdout, "after-line") {
		fmt.Println("PROBE-STDOUT-LOST:", stdout)
		return
	}
	if len(lines) != 1 {
		fmt.Println("PROBE-LINE-COUNT:", len(lines))
		return
	}
	fmt.Println("PROBE-SURVIVED")
}
