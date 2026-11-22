package procx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// shell_test.go —— 壳自身的委托一致性测试。行为测试已随实现迁入
// oekit/pkg/procx（classifywait/truncation/oneline_panic 等逐条钉死）；
// 这里只钉两件壳层自己的事：
//
//  1. Run(4 值签名) 与 RunDetailed(结构化) 对同一请求零漂移（解构薄壳契约）；
//  2. ChildEnv 壳层补回 compatAgentEnvKeys 的行为兼容（外部消费方升级不漂移）。

// TestRunShellParityWithRunDetailed 壳内两入口零漂移。
func TestRunShellParityWithRunDetailed(t *testing.T) {
	req := RunRequest{
		Argv:    []string{"sh", "-c", "echo out-payload; echo err-payload >&2; exit 5"},
		Timeout: 30 * time.Second,
	}

	out, errStr, code, err := Run(context.Background(), req)
	res, derr := RunDetailed(context.Background(), req)
	if (err == nil) != (derr == nil) || (err != nil && err.Error() != derr.Error()) {
		t.Fatalf("Run/RunDetailed err 漂移: %v vs %v", err, derr)
	}
	if out != res.Stdout || errStr != res.Stderr || code != res.ExitCode {
		t.Fatalf("Run/RunDetailed 返回漂移: (%q,%q,%d) vs %+v", out, errStr, code, res)
	}
	if !strings.Contains(res.Stdout, "out-payload") || !strings.Contains(res.Stderr, "err-payload") || res.ExitCode != 5 {
		t.Fatalf("样本自检: %+v", res)
	}
}

// TestRunShellTimeoutSentinel 超时经壳透传后仍是 pkg 的 ErrTimeout（alias 不换型）。
func TestRunShellTimeoutSentinel(t *testing.T) {
	_, err := RunDetailed(context.Background(), RunRequest{
		Argv:    []string{"sh", "-c", "sleep 30"},
		Timeout: 150 * time.Millisecond,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("超时应 errors.Is ErrTimeout: %v", err)
	}
}

// TestShellChildEnvKeepsAgentConfigKeys 行为兼容：壳层 ChildEnv 必须补回
// agent 配置目录键（旧 runtime 白名单成员），且 pkg 基础组（TZ/windows 组
// 在非 windows 不显）与尾部纪律照常生效。
func TestShellChildEnvKeepsAgentConfigKeys(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/u/.claude")
	t.Setenv("TZ", "Asia/Shanghai")
	t.Setenv("RUNPROC_SHELL_SECRET", "leak-me")

	env := ChildEnv(nil)
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"CLAUDE_CONFIG_DIR=/home/u/.claude", // agent 兼容键：按名透传
		"TZ=Asia/Shanghai",                  // pkg 基础组新增（旧 runtime 缺）
		"GIT_TERMINAL_PROMPT=0",             // 尾部纪律
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("壳层 ChildEnv 缺 %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "RUNPROC_SHELL_SECRET") {
		t.Errorf("默认拒绝被破坏（秘密变量泄漏进子进程环境）:\n%s", joined)
	}
	if !hasUniqueKeys(env) {
		t.Errorf("环境存在同名重复项:\n%s", joined)
	}
}

// TestShellInheritAllExceptDelegates 继承式入口委托 pkg：白名单之外的变量
// 必须在（继承语义），except 剔除生效。
func TestShellInheritAllExceptDelegates(t *testing.T) {
	t.Setenv("RUNPROC_SHELL_SECRET", "drop-me")
	t.Setenv("GIT_AUTHOR_NAME", "kept")

	env := InheritAllExcept([]string{"RUNPROC_SHELL_SECRET"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "drop-me") {
		t.Errorf("except 名单未剔除:\n%s", joined)
	}
	if !strings.Contains(joined, "GIT_AUTHOR_NAME=kept") {
		t.Errorf("开放式 GIT_* 命名空间必须整体继承:\n%s", joined)
	}
	if !strings.Contains(joined, "CI=1") && !hasEntry(env, "CI") {
		// CI 缺省追加（父进程未设时）；若父进程已设 CI=0 则按父值。
		t.Logf("CI 缺省项形态: %v", env)
	}
}

// hasUniqueKeys 断言环境切片无同名键（继承父环境自身可能带同名项，尾部
// 纪律去重保证）。
func hasUniqueKeys(env []string) bool {
	seen := map[string]bool{}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func hasEntry(env []string, name string) bool {
	for _, kv := range env {
		if n, _, _ := strings.Cut(kv, "="); n == name {
			return true
		}
	}
	return false
}
