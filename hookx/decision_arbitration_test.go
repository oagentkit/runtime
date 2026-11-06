package hookx

import (
	"context"
	"strings"
	"testing"
)

// TestContinueFalseBeatsApprove 单个 hook 输出内部矛盾时，权限面必须向保守方向
// 收敛：`continue:false` 已置 deny，后置的 `decision:"approve"` 不得把它顶掉
// （hookx#29）。
//
// 失效形态：两个字段写同一个 res，`processOutput` 先 applyContinueFlag 再
// applyDecision ⇒ approve 覆写 deny ⇒「既说别继续、又说放行」被静默解析为放行。
// 而跨 hook 的 merge 是 deny 单调的——同一语义在两层方向相反，且相反的那个方向
// 不安全。
func TestContinueFalseBeatsApprove(t *testing.T) {
	r := &Runner{Hooks: []Hook{
		shHook(t, "pre_tool_use", `{"continue":false,"decision":"approve","reason":"矛盾输出"}`),
	}}
	res := r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse, Tool: "bash"})
	if res.PermissionBehavior != "deny" {
		t.Fatalf("continue:false 的 deny 被 approve 顶掉了: %+v", res)
	}
	if !res.BlockRequested || !res.PreventContinuation {
		t.Fatalf("阻断标记应保持: %+v", res)
	}
}

// TestApproveStillAllows 成对对照：没有矛盾时 approve 照常生效——否则「一律算
// deny」也能让上一条绿，而放行能力整体消失。
func TestApproveStillAllows(t *testing.T) {
	r := &Runner{Hooks: []Hook{
		shHook(t, "permission_request", `{"decision":"approve"}`),
	}}
	res := r.Run(context.Background(), EventPermissionRequest, Input{Event: EventPermissionRequest})
	if res.PermissionBehavior != "allow" {
		t.Fatalf("单独 approve 应放行: %+v", res)
	}
}

// TestDecisionCaseInsensitive 大小写变体是常见手写形态，必须按契约识别。
//
// 原实现精确匹配 "approve"/"block"，`"BLOCK"` 落空 → 静默忽略 → hook 想拦、
// 库当没看见（安全方向不安全）。
func TestDecisionCaseInsensitive(t *testing.T) {
	cases := []struct {
		decision string
		event    Event
		check    func(RunResult) bool
		want     string
	}{
		{"BLOCK", EventPreToolUse, func(r RunResult) bool { return r.PermissionBehavior == "deny" && r.BlockRequested }, "deny+block"},
		{"Block", EventPreToolUse, func(r RunResult) bool { return r.PermissionBehavior == "deny" }, "deny"},
		{"  approve  ", EventPermissionRequest, func(r RunResult) bool { return r.PermissionBehavior == "allow" }, "allow"},
		{"Approve", EventPermissionRequest, func(r RunResult) bool { return r.PermissionBehavior == "allow" }, "allow"},
	}
	for _, tc := range cases {
		t.Run(tc.decision, func(t *testing.T) {
			r := &Runner{Hooks: []Hook{
				shHook(t, string(tc.event), `{"decision":"`+strings.TrimSpace(tc.decision)+`"}`),
			}}
			res := r.Run(context.Background(), tc.event, Input{Event: tc.event})
			if !tc.check(res) {
				t.Fatalf("decision=%q 应按契约识别为 %s: %+v", tc.decision, tc.want, res)
			}
		})
	}
}

// TestUnknownDecisionReported 契约外的取值不猜语义，但**必须经 OnError 可见**
// ——静默忽略是这次要消灭的形态。
func TestUnknownDecisionReported(t *testing.T) {
	var reported []error
	r := &Runner{
		Hooks: []Hook{shHook(t, "pre_tool_use", `{"decision":"reject","reason":"随便写的"}`)},
		OnError: func(_ Hook, err error) {
			reported = append(reported, err)
		},
	}
	res := r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse})
	if len(reported) != 1 {
		t.Fatalf("越界 decision 应上报一次 OnError，实得 %d 次", len(reported))
	}
	msg := reported[0].Error()
	if !strings.Contains(msg, "reject") || !strings.Contains(msg, "decision") {
		t.Fatalf("错误信息应点名取值与字段: %q", msg)
	}
	// 不猜语义：不置任何权限/阻断位。
	if res.PermissionBehavior != "" || res.BlockRequested || res.PreventContinuation {
		t.Fatalf("未知取值不应被猜成任何判定: %+v", res)
	}
}

// TestContractDecisionNoError 契约内取值（含空）不得触发 OnError——否则上面
// 那条会退化成「每次调用都报错」的噪声。
func TestContractDecisionNoError(t *testing.T) {
	for _, decision := range []string{"", "approve", "block"} {
		var reported int
		r := &Runner{
			Hooks:   []Hook{shHook(t, "pre_tool_use", `{"decision":"`+decision+`"}`)},
			OnError: func(_ Hook, _ error) { reported++ },
		}
		r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse})
		if reported != 0 {
			t.Fatalf("decision=%q 在契约内，不该上报错误（%d 次）", decision, reported)
		}
	}
}

// TestCrossHookDenyMonotonic 回归：跨 hook 归并的 deny 单调不受本次改动影响。
func TestCrossHookDenyMonotonic(t *testing.T) {
	r := &Runner{Hooks: []Hook{
		shHook(t, "pre_tool_use", `{"decision":"block"}`),
		shHook(t, "pre_tool_use", `{"decision":"approve"}`),
	}}
	res := r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse})
	if res.PermissionBehavior != "deny" {
		t.Fatalf("跨 hook 归并应 deny 单调: %+v", res)
	}
}
