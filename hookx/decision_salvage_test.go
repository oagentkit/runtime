package hookx

import (
	"context"
	"strings"
	"testing"
	"time"
)

// decisionThenExitHook 构造 issue#69 的真实 hook 形态：先打印决策 JSON，再以
// 非零退出码退出（决策之后才发生的内部错误 / `set -e` 后置检查 / 包装器诊断）。
func decisionThenExitHook(t *testing.T, events, out, exit string) Hook {
	t.Helper()
	return Hook{Command: []string{"sh", "-c", "cat >/dev/null; printf '%s' '" + out + "'; exit " + exit}, Events: events}
}

// TestNonZeroExitKeepsBlockDecision 回归（runtime#69）：hook 打印了
// `decision:"block"` 之后才非零退出——决策必须照常采纳。旧实现 runHook 在
// `err != nil` 时整段丢弃 stdout，打出来的 block 静默蒸发，缺省 fail-open
// 把动作放过去（「hook 想拦、库当没看见」的深层复刻）。
func TestNonZeroExitKeepsBlockDecision(t *testing.T) {
	var errs []error
	r := &Runner{
		Hooks: []Hook{decisionThenExitHook(t, "pre_tool_use",
			`{"decision":"block","reason":"deny this tool call"}`, "3")},
		OnError: func(_ Hook, err error) { errs = append(errs, err) },
	}
	res := r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse, Tool: "bash"})
	if !res.BlockRequested || !res.PreventContinuation {
		t.Fatalf("非零退出不得吞掉已打印的 block 决策: %+v", res)
	}
	if res.PermissionBehavior != "deny" {
		t.Fatalf("block 应落权限 deny——fail-open 放行是本次要消灭的形态: %+v", res)
	}
	if res.StopReason != "deny this tool call" {
		t.Fatalf("决策里的 reason 应照常透传: %q", res.StopReason)
	}
	// 决策采纳不等于失败静默：执行失败仍须经 OnError 可见
	if len(errs) != 1 {
		t.Fatalf("执行失败应上报一次 OnError，实得 %d 次: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "3") {
		t.Fatalf("OnError 应携带执行失败的成因（退出码）: %v", errs[0])
	}
}

// TestNonZeroExitKeepsContinueFalse continue:false 同样是阻断信号，非零退出
// 不得把它丢掉。
func TestNonZeroExitKeepsContinueFalse(t *testing.T) {
	r := &Runner{Hooks: []Hook{decisionThenExitHook(t, "user_prompt_submit",
		`{"continue":false,"stopReason":"拦截"}`, "2")}}
	res := r.Run(context.Background(), EventUserPromptSubmit, Input{Event: EventUserPromptSubmit})
	if !res.BlockRequested || !res.PreventContinuation || res.StopReason != "拦截" {
		t.Fatalf("continue:false 应在非零退出后照常采纳: %+v", res)
	}
}

// TestNonZeroExitKeepsApprove 契约内的 approve 同口径救回——决策采纳与取值
// 方向无关，hook 明确说过的话优先于失败缺省。
func TestNonZeroExitKeepsApprove(t *testing.T) {
	r := &Runner{Hooks: []Hook{decisionThenExitHook(t, "permission_request",
		`{"decision":"approve","additionalContext":"已核对"}`, "3")}}
	res := r.Run(context.Background(), EventPermissionRequest, Input{Event: EventPermissionRequest})
	if res.PermissionBehavior != "allow" || len(res.AdditionalContexts) != 1 {
		t.Fatalf("approve 应在非零退出后照常采纳: %+v", res)
	}
}

// TestTimeoutKeepsBlockDecision 超时是同一形态的另一条路径：决策 JSON 打完
// 之后 hook 才挂死——超时终止不得把已说的决策一起抹掉。
func TestTimeoutKeepsBlockDecision(t *testing.T) {
	r := &Runner{Hooks: []Hook{Hook{
		Command: []string{"sh", "-c", `cat >/dev/null; printf '%s' '{"decision":"block","reason":"慢决策"}'; sleep 30`},
		Timeout: 300 * time.Millisecond,
	}}}
	res := r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse, Tool: "bash"})
	if !res.BlockRequested || res.PermissionBehavior != "deny" {
		t.Fatalf("超时不得吞掉已打印的 block 决策: %+v", res)
	}
}

// TestFailClosedYieldsToSalvagedDecision 决策救回与 FailClosedEvents 并存时，
// hook 自己的判定取代保守判定（StopReason 来自 hook 而非 fail-closed 兜底），
// 失败仍经 OnError 可见。
func TestFailClosedYieldsToSalvagedDecision(t *testing.T) {
	var errs []error
	r := &Runner{
		Hooks:            []Hook{decisionThenExitHook(t, "pre_tool_use", `{"decision":"block","reason":"hook 自己的判定"}`, "3")},
		FailClosedEvents: []Event{EventPreToolUse},
		OnError:          func(_ Hook, err error) { errs = append(errs, err) },
	}
	res := r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse, Tool: "bash"})
	if !res.BlockRequested || res.PermissionBehavior != "deny" {
		t.Fatalf("救回的决策应照常生效: %+v", res)
	}
	if res.StopReason != "hook 自己的判定" {
		t.Fatalf("hook 的判定应取代 fail-closed 兜底: %q", res.StopReason)
	}
	if len(errs) != 1 {
		t.Fatalf("失败仍应上报一次 OnError，实得 %d 次", len(errs))
	}
}

// 成对对照（runtime#69 的向后兼容边界）：没有可抢救的契约决策时，失败语义
// 保持原样——否则「一律当作有决策」会让这几条意外变绿。
func TestNonZeroExitWithoutDecisionUnchanged(t *testing.T) {
	cases := []struct {
		name string
		hook Hook
	}{
		{"非零退出+空输出", failingHook(t, "pre_tool_use")}, // exit 3 无输出
		{"非零退出+非JSON", Hook{Command: []string{"sh", "-c", "cat >/dev/null; printf 'not-json'; exit 3"}}},
		{"非零退出+契约外decision", decisionThenExitHook(t, "pre_tool_use", `{"decision":"reject","reason":"随便写的"}`, "3")},
		{"非零退出+continue:true", decisionThenExitHook(t, "pre_tool_use", `{"continue":true}`, "3")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var errs []error
			r := &Runner{Hooks: []Hook{tc.hook}, OnError: func(_ Hook, err error) { errs = append(errs, err) }}
			res := r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse, Tool: "bash"})
			if res.PermissionBehavior != "" || res.BlockRequested || res.PreventContinuation {
				t.Fatalf("无可抢救决策时应维持 fail-open 缺省（判定义务在 FailClosedEvents）: %+v", res)
			}
			if len(errs) != 1 {
				t.Fatalf("执行失败应上报一次 OnError，实得 %d 次", len(errs))
			}
		})
	}
}

// TestFailClosedCoversUnsalvageableFailure 无可抢救决策 + FailClosedEvents：
// 保守判定照旧生效（救回机制不得削弱 hookx#34 的兜底）。
func TestFailClosedCoversUnsalvageableFailure(t *testing.T) {
	r := &Runner{
		Hooks:            []Hook{decisionThenExitHook(t, "pre_tool_use", `{"decision":"reject"}`, "3")},
		FailClosedEvents: []Event{EventPreToolUse},
	}
	res := r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse, Tool: "bash"})
	if !res.BlockRequested || !strings.Contains(res.StopReason, "fail-closed") {
		t.Fatalf("契约外决策救不回时，FailClosedEvents 的保守判定必须兜底: %+v", res)
	}
}

// TestSalvageRejectsWrongEventName 失败进程声称别的事件名的输出不可信——与
// 成功路径同一道门，不因抢救而豁免。
func TestSalvageRejectsWrongEventName(t *testing.T) {
	r := &Runner{Hooks: []Hook{decisionThenExitHook(t, "pre_tool_use",
		`{"decision":"block","reason":"冒名","hookSpecificOutput":{"hookEventName":"stop"}}`, "3")}}
	res := r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse})
	if res.BlockRequested {
		t.Fatalf("事件名不符的失败输出不得被采纳: %+v", res)
	}
}
