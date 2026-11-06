package hookx

import (
	"context"
	"testing"
)

// TestPreventContinuationNeverOnStop Stop 事件上 PreventContinuation 必须恒为
// false，block 的含义由 StopShouldContinue 表达（runtime#33）。
//
// 失效形态是**语义反转**：两个字段同时为 true 时，通用处理按
// 「PreventContinuation → 不继续」分派，会把「要求继续」读成「禁止继续」。
func TestPreventContinuationNeverOnStop(t *testing.T) {
	// wantBlock 区分两条入口：continue:false 在 Stop 上只表达「否决停止」，
	// 既有语义不置 BlockRequested（本次不改动它，只钉住 PreventContinuation）。
	cases := []struct {
		name      string
		events    string
		output    string
		wantBlock bool
	}{
		{"decision:block", "stop", `{"decision":"block","reason":"还有活"}`, true},
		{"continue:false", "stop", `{"continue":false,"stopReason":"还有活"}`, false},
		{"decision:block + systemMessage", "stop", `{"decision":"block","systemMessage":"别停","reason":"还有活"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{Hooks: []Hook{shHook(t, tc.events, tc.output)}}
			res := r.Run(context.Background(), EventStop, Input{Event: EventStop})
			if res.PreventContinuation {
				t.Fatalf("Stop 事件不得置 PreventContinuation（它与「否决停止」语义相反）: %+v", res)
			}
			if !res.StopShouldContinue {
				t.Fatalf("Stop 事件上否决停止应置 StopShouldContinue: %+v", res)
			}
			if res.BlockRequested != tc.wantBlock {
				t.Fatalf("BlockRequested = %v, want %v: %+v", res.BlockRequested, tc.wantBlock, res)
			}
		})
	}
}

// TestPreventContinuationStillSetForToolEvents 负对照：非 Stop 事件的行为不变。
//
// 只有「Stop 不再置位」的用例等于没钉住——若把 PreventContinuation 整个删掉，
// 上面那组照样全绿，而安全阻断面会静默失效。
func TestPreventContinuationStillSetForToolEvents(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event Event
	}{
		{"pre_tool_use", EventPreToolUse},
		{"permission_request", EventPermissionRequest},
		{"post_tool_use", EventPostToolUse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{Hooks: []Hook{
				shHook(t, string(tc.event), `{"decision":"block","reason":"危险"}`),
			}}
			res := r.Run(context.Background(), tc.event, Input{Event: tc.event})
			if !res.PreventContinuation {
				t.Fatalf("%s 上的 block 必须置 PreventContinuation: %+v", tc.event, res)
			}
			if res.StopShouldContinue {
				t.Fatalf("%s 不是 Stop 事件，不得置 StopShouldContinue: %+v", tc.event, res)
			}
		})
	}
}

// TestStopBlockKeepsAdditionalContexts 回归：Stop 上 block 的说明仍要进上下文。
func TestStopBlockKeepsAdditionalContexts(t *testing.T) {
	r := &Runner{Hooks: []Hook{
		shHook(t, "stop", `{"decision":"block","systemMessage":"别停","reason":"还有活"}`),
	}}
	res := r.Run(context.Background(), EventStop, Input{Event: EventStop})
	if res.StopReason != "还有活" {
		t.Fatalf("stopReason 取首个非空（stopReason→reason→systemMessage）: %q", res.StopReason)
	}
	if len(res.AdditionalContexts) != 2 ||
		res.AdditionalContexts[0] != "别停" || res.AdditionalContexts[1] != "还有活" {
		t.Fatalf("systemMessage 与 reason 都应进附加上下文: %v", res.AdditionalContexts)
	}
}
