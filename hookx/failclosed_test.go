package hookx

import (
	"context"
	"strings"
	"testing"
)

// failingHook 执行必然失败的 hook（非零退出）。
func failingHook(t *testing.T, events string) Hook {
	t.Helper()
	return Hook{Command: []string{"sh", "-c", "cat >/dev/null; exit 3"}, Events: events}
}

// badJSONHook 输出非 JSON（runHook 的第二类失败）。
func badJSONHook(t *testing.T, events string) Hook {
	t.Helper()
	return Hook{Command: []string{"sh", "-c", "cat >/dev/null; printf 'not-json'"}, Events: events}
}

// TestFailClosedDeniesSafetyEvent 安全事件上列进 FailClosedEvents 后，hook 执行
// 失败必须落保守判定（hookx#34）。
//
// 失效形态：`Run` 里失败即 `continue`，OnError 是纯观察者 ⇒ 二进制缺失/超时/
// 坏 JSON 全部静默等于「放行」。包文档「由调用方在 OnError 里自行收紧」在
// 机制上不可能。
func TestFailClosedDeniesSafetyEvent(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		hook  func(*testing.T, string) Hook
	}{
		{"pretool-exit3", EventPreToolUse, failingHook},
		{"pretool-badjson", EventPreToolUse, badJSONHook},
		{"permission-exit3", EventPermissionRequest, failingHook},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{
				Hooks:            []Hook{tc.hook(t, string(tc.event))},
				FailClosedEvents: []Event{EventPreToolUse, EventPermissionRequest},
			}
			res := r.Run(context.Background(), tc.event, Input{Event: tc.event, Tool: "bash"})
			if res.PermissionBehavior != "deny" {
				t.Fatalf("失败应判 deny（否则等于静默放行）: %+v", res)
			}
			if !res.BlockRequested || !res.PreventContinuation {
				t.Fatalf("失败应阻断续跑: %+v", res)
			}
			if !strings.Contains(res.StopReason, "fail-closed") {
				t.Fatalf("StopReason 应说明成因: %q", res.StopReason)
			}
		})
	}
}

// TestFailOpenIsDefault 成对对照：**不列** FailClosedEvents 时行为保持原样
// （失败=无意见）——否则「一律 fail-closed」也能让上一条绿，而扩展点故障
// 开始阻断所有业务，正是原设计要避免的事。
func TestFailOpenIsDefault(t *testing.T) {
	r := &Runner{Hooks: []Hook{failingHook(t, string(EventPreToolUse))}}
	res := r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse})
	if res.PermissionBehavior != "" || res.BlockRequested || res.PreventContinuation {
		t.Fatalf("缺省应 fail-open（无意见）: %+v", res)
	}
}

// TestFailClosedOnStopAddsNoVerdict Stop 的 block 语义反转，失败不能被读成
// 「必须继续」——列进 FailClosedEvents 也不产生额外判定，但失败仍经 OnError
// 可见（否则就成了静默）。
func TestFailClosedOnStopAddsNoVerdict(t *testing.T) {
	var reported int
	r := &Runner{
		Hooks:            []Hook{failingHook(t, string(EventStop))},
		FailClosedEvents: []Event{EventStop},
		OnError:          func(_ Hook, _ error) { reported++ },
	}
	res := r.Run(context.Background(), EventStop, Input{Event: EventStop})
	if res.StopShouldContinue || res.BlockRequested || res.PreventContinuation {
		t.Fatalf("Stop 上的失败不该产生任何判定: %+v", res)
	}
	if reported != 1 {
		t.Fatalf("失败仍须经 OnError 可见: %d 次", reported)
	}
}

// TestFailClosedStillReportsOnError fail-closed 不是「替掉 OnError」：两者并存，
// 调用方既要拿到判定，也要拿到成因。
func TestFailClosedStillReportsOnError(t *testing.T) {
	var errs []error
	r := &Runner{
		Hooks:            []Hook{failingHook(t, string(EventPreToolUse))},
		FailClosedEvents: []Event{EventPreToolUse},
		OnError:          func(_ Hook, err error) { errs = append(errs, err) },
	}
	r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse})
	if len(errs) != 1 {
		t.Fatalf("OnError 应被调用一次，实得 %d", len(errs))
	}
}

// TestFailClosedOnlyAffectsListedEvents 只列 PreToolUse 时，PermissionRequest
// 上的失败仍然 fail-open——列表是逐事件生效的，不是全局开关。
func TestFailClosedOnlyAffectsListedEvents(t *testing.T) {
	r := &Runner{
		Hooks:            []Hook{failingHook(t, "*")},
		FailClosedEvents: []Event{EventPreToolUse},
	}
	res := r.Run(context.Background(), EventPermissionRequest, Input{Event: EventPermissionRequest})
	if res.PermissionBehavior != "" || res.BlockRequested {
		t.Fatalf("未列出的事件应保持 fail-open: %+v", res)
	}
}

// TestFailClosedKeepsHealthyHookVerdict 失败 hook 的保守判定不得顶掉其它正常
// hook 已给出的更严判定（deny 单调），也不得让正常 approve 被无视成 allow。
func TestFailClosedKeepsHealthyHookVerdict(t *testing.T) {
	r := &Runner{
		Hooks: []Hook{
			shHook(t, "pre_tool_use", `{"decision":"approve"}`),
			failingHook(t, "pre_tool_use"),
		},
		FailClosedEvents: []Event{EventPreToolUse},
	}
	res := r.Run(context.Background(), EventPreToolUse, Input{Event: EventPreToolUse})
	if res.PermissionBehavior != "deny" {
		t.Fatalf("失败 hook 的 deny 应压过 approve: %+v", res)
	}
}
