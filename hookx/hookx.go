// Package hookx 外部 hook 拦截协议：在关键拦截点派生子进程，stdin 喂 JSON
// 事件载荷、stdout 收 JSON 决策，归并多 hook 结果。对标 ZCode hooks/
// runner.ts + output.ts 的「跨语言 hook 事实标准」形态。
//
// 决策协议（HookOutput）：
//
//	continue:false 或 decision:"block" → 阻断（PreToolUse/PermissionRequest
//	  事件上同时意味着 permission deny；Stop 事件上意味着「继续跑」）
//	decision:"approve" → 权限事件上升格放行（消费方按 permgate 语义消费——
//	  alwaysAsk 不可被 hook 升格，这是调用方纪律）
//	additionalContext / additional_context → 附加上下文累积
//	hookSpecificOutput.updatedInput → 输入改写（消费方必须用改写后的输入
//	  重走权限/许可链——hook 注入的输入不能绕过规则）
//
// 失败语义：hook 进程失败（超时/非零退出/stdout 非 JSON/事件名不符）默认按
// 「无意见」处理不炸主流程，经 OnError 可观测——扩展点故障不该阻断业务。
//
// 「需要强一致的场景由调用方在 OnError 里自行收紧」曾是一句做不到的承诺：
// OnError 是纯观察者，Run 里失败即 continue，调用方没有任何办法把失败升级成
// 阻断。现在由 Runner.FailClosedEvents 表达：列出的失事件类型上，hook 执行失败
// 不再等于「无意见」，而是落一条**保守判定**（阻断 + 权限 deny）（hookx#34）。
// Stop 是例外，见 failClosedResult 的说明。
//
// 子进程环境走 procx.ChildEnv 白名单（绝不继承全量密钥）。
package hookx

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/oagentkit/runtime/procx"
	"github.com/oekit/pkg/cache"
)

// DefaultTimeout 单 hook 执行预算（对标 ZCode 60s）。
const DefaultTimeout = 60 * time.Second

// maxStdout hook stdout 采集上限（限容纪律：决策 JSON 不该超过这个量级）。
const maxStdout = 1 << 20

// Event 拦截点。
type Event string

const (
	EventSessionStart       Event = "session_start"
	EventUserPromptSubmit   Event = "user_prompt_submit"
	EventPreToolUse         Event = "pre_tool_use"
	EventPermissionRequest  Event = "permission_request"
	EventPostToolUse        Event = "post_tool_use"
	EventPostToolUseFailure Event = "post_tool_use_failure"
	EventStop               Event = "stop"
)

// Hook 单个拦截器声明。
type Hook struct {
	// Command 子进程 argv（直传无 shell）。
	Command []string
	// Events 事件匹配："*"（全部）、"|"分隔精确名（"pre_tool_use|post_tool_use"）、
	// "/正则/" 形态（"/.*tool.*/"）。空 = "*"。
	Events string
	// Timeout ≤0 → DefaultTimeout。
	Timeout time.Duration
	// Env 追加透传的环境变量白名单（叠加 procx 基础集）。
	Env []string
}

// Input 派发载荷（stdin JSON）。
type Input struct {
	Event     Event          `json:"event"`
	Tool      string         `json:"tool,omitempty"`
	ToolInput any            `json:"input,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"` // 调用方附带上下文（会话 id 等）
}

// Specific hook 事件专属输出。
type Specific struct {
	HookEventName Event `json:"hookEventName"`
	// UpdatedInput 改写后的工具输入（消费方须以改写输入重走许可链）。
	UpdatedInput any `json:"updatedInput,omitempty"`
}

// Output hook stdout 决策协议（未知字段忽略）。
type Output struct {
	Continue   *bool  `json:"continue,omitempty"`
	StopReason string `json:"stopReason,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Decision "approve" | "block"（大小写不敏感，两侧空白忽略）。契约外的取值
	// 不予采纳并经 Runner.OnError 上报——静默忽略会让「hook 想拦、库当没看见」
	// 变成无声的放行（hookx#29）。
	Decision      string `json:"decision,omitempty"`
	SystemMessage string `json:"systemMessage,omitempty"`
	// 附加上下文（两种拼写都收——事实标准里两代拼写并存）。
	AdditionalContext  string    `json:"additionalContext,omitempty"`
	AdditionalContext2 string    `json:"additional_context,omitempty"`
	HookSpecificOutput *Specific `json:"hookSpecificOutput,omitempty"`
}

// RunResult 事件级归并结果（多个 hook 单调合并）。
type RunResult struct {
	AdditionalContexts []string
	// BlockRequested 任一 hook 请求阻断。
	BlockRequested bool
	// PreventContinuation 阻断且该事件不允许继续（PreToolUse/PermissionRequest）。
	// **Stop 事件上恒为 false**：Stop 的 block 语义是「否决停止、继续跑」，
	// 由 StopShouldContinue 表达。两者不可能同时为真——通用处理若按
	// 「PreventContinuation → 不继续」分派，同时置位会把语义读反（runtime#33）。
	PreventContinuation bool
	StopReason          string
	// StopShouldContinue Stop 事件上 hook 否决停止（continue:false 或
	// decision:block——block 分支沿用阻断语义）。
	StopShouldContinue bool
	// PermissionBehavior "" | "allow" | "deny"。deny 单调，且**在两个层级上都单调**：
	// 跨 hook 归并（merge）与单个 hook 输出内部（如 continue:false 与
	// decision:approve 并存）都只向保守方向收敛，allow 不能覆盖已置的 deny（hookx#29）。
	PermissionBehavior string
	// UpdatedInput 最后一个改写输入（nil=未改写）。
	UpdatedInput any
}

// Runner hook 执行器。零值可用（无 hook 时 Run 返回空结果）。
type Runner struct {
	Hooks []Hook
	// OnError hook 执行失败回调（nil=静默容错）。
	OnError func(h Hook, err error)
	// FailClosedEvents 这些事件类型上「hook 执行失败」不再按「无意见」处理：
	// 失败本身落一条保守判定（阻断 + 权限事件上 deny），把「二进制缺失/超时/
	// 坏 JSON 静默放行」变成拒绝（hookx#34）。
	//
	// 只列**安全面**的事件（如 EventPreToolUse / EventPermissionRequest）。
	// Stop 的 block 语义是反的（否决停止、继续跑），失败不能被读成「必须
	// 继续」，故列在 FailClosedEvents 里对 Stop 不产生额外判定——失败仍经
	// OnError 可见。
	FailClosedEvents []Event
}

// failClosed 报告该事件类型是否要求 fail-closed。
func (r *Runner) failClosed(event Event) bool {
	for _, e := range r.FailClosedEvents {
		if e == event {
			return true
		}
	}
	return false
}

// failClosedResult 把「hook 执行失败」翻译成保守判定。
//
// Stop 上刻意不产生判定：该事件 block 的含义是「否决停止、继续跑」，一次
// 执行失败无法支撑「必须继续」这种强结论——把失败强扭成继续跑，与「保守」
// 正好相反。失败在该事件上仍经 OnError 可见。
func failClosedResult(event Event, err error) RunResult {
	var res RunResult
	if event == EventStop {
		return res
	}
	res.BlockRequested = true
	res.PreventContinuation = true
	res.StopReason = fmt.Sprintf("hook 执行失败（fail-closed）: %v", err)
	if event == EventPermissionRequest || event == EventPreToolUse {
		res.PermissionBehavior = "deny"
	}
	return res
}

// Run 派发事件：顺序执行全部命中 hook 并归并（顺序确定性——先注册先执行）。
func (r *Runner) Run(ctx context.Context, event Event, payload Input) RunResult {
	var merged RunResult
	for _, h := range r.Hooks {
		if !h.matches(event) {
			continue
		}
		out, err := runHook(ctx, h, payload)
		if err != nil {
			if r.OnError != nil {
				r.OnError(h, err)
			}
			if r.failClosed(event) {
				merged.merge(failClosedResult(event, err))
			}
			continue
		}
		res, perr := processOutput(event, out)
		merged.merge(res)
		if perr != nil && r.OnError != nil {
			r.OnError(h, perr) // 输出越界：结果照常参与归并，但要让调用方看见
		}
	}
	return merged
}

// eventsReCache 正则模式编译缓存（/…/ 形态）。matches 在每次事件派发的热
// 路径上（pre_tool_use 每次工具调用都跑全部 hook），坏模式不缓存不匹配
// （与原"编译失败即不匹配"行为一致）。
//
// 单飞去重：并发首次命中同一模式时只编译一次，其余请求共享结果。
// TTL=0 ⇒ 永不过期，且不引入后台清扫 goroutine（与原 sync.Map 无常驻协程一致）。
var eventsReCache = cache.NewSingleFlightCache(cache.NewLocalCache[regexp.Regexp](cache.LocalConfig[regexp.Regexp]{
	NullTTL: -1, // 编译失败走 err 分支本就不写 null marker；显式禁用空值缓存，杜绝「查不到」被误当缓存事实
}))

func (h Hook) matches(event Event) bool {
	pattern := strings.TrimSpace(h.Events)
	if pattern == "" || pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") && len(pattern) > 2 {
		re, err := eventsReCache.GetOrLoadWithSource(context.Background(), pattern, func(_ context.Context, p string) (*regexp.Regexp, error) {
			return regexp.Compile(p[1 : len(p)-1])
		})
		if err != nil {
			return false
		}
		return re.MatchString(string(event))
	}
	for _, name := range strings.Split(pattern, "|") {
		if strings.TrimSpace(name) == string(event) {
			return true
		}
	}
	return false
}

// runHook 派生子进程：stdin 喂载荷、限容收 stdout、超时整组终止——执行纪律
// 经 procx 单源（Setpgid 建组；sh -c 派生的孙进程被杀后仍握管道写端会让裸
// exec 的 Wait 挂到孙进程退出，进程组终止是唯一正确形态）。
func runHook(ctx context.Context, h Hook, payload Input) (*Output, error) {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	stdout, _, _, err := procx.Run(ctx, procx.RunRequest{
		Argv:      h.Command,
		Env:       h.Env,
		Timeout:   timeout,
		MaxStdout: maxStdout,
		Stdin:     payloadJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("hookx: %w", err)
	}
	raw := strings.TrimSpace(stdout)
	if raw == "" {
		return nil, nil // 无输出=无意见
	}
	var out Output
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("hookx: stdout 非 JSON: %v（%s）", err, truncate(raw))
	}
	if out.HookSpecificOutput != nil && out.HookSpecificOutput.HookEventName != payload.Event {
		return nil, fmt.Errorf("hookx: hook 返回了错误的事件名 %q（期望 %q）",
			out.HookSpecificOutput.HookEventName, payload.Event)
	}
	return &out, nil
}

// processOutput 单 hook 输出 → 结果（对标 processHookOutput）。
//
// 返回的 error 不是「执行失败」（执行失败在 runHook 那层），而是**输出越界**：
// 目前只有 decision 取值不在契约内一种。走 OnError 可观测而不是静默忽略——
// 静默忽略的失效形态是「hook 想拦、库当没看见」（hookx#29）。
func processOutput(event Event, out *Output) (RunResult, error) {
	var res RunResult
	if out == nil {
		return res, nil
	}
	applyContinueFlag(event, out, &res)
	err := applyDecision(event, out, &res)
	applyContexts(out, &res)
	return res, err
}

// applyContinueFlag continue:false 的事件语义：非 Stop = 阻断续跑；
// Stop 上 = 否决停止（继续跑）——协议语义反着实现会造成无限续跑。
func applyContinueFlag(event Event, out *Output, res *RunResult) {
	if out.Continue == nil || *out.Continue {
		return
	}
	isPermission := event == EventPermissionRequest || event == EventPreToolUse
	if event != EventStop {
		res.BlockRequested = true
		res.StopReason = firstNonEmpty(out.StopReason, out.Reason)
		if isPermission {
			setPermission(res, "deny")
		}
		res.PreventContinuation = true
		return
	}
	// continue:true 等价缺省「无异议、允许停止」——不是否决
	res.StopShouldContinue = true
	res.StopReason = firstNonEmpty(out.StopReason, out.Reason)
}

// applyDecision decision 字段：approve 放行权限段；block 阻断（Stop 上转为
// 否决停止并附加说明）。
//
// decision 取值按契约大小写不敏感识别（"BLOCK"/"Approve" 是常见手写形态，
// 原实现把它们当未知值静默丢弃——hook 想拦、库当没看见，方向不安全）。
// 契约外的取值一律不猜语义，经返回值上报 OnError。
func applyDecision(event Event, out *Output, res *RunResult) error {
	isPermission := event == EventPermissionRequest || event == EventPreToolUse
	decision := strings.ToLower(strings.TrimSpace(out.Decision))
	switch decision {
	case "":
		return nil
	case "approve":
		if isPermission {
			setPermission(res, "allow") // deny 单调：不覆盖 continue:false 已置的 deny
		}
	case "block":
		res.BlockRequested = true
		res.StopReason = firstNonEmpty(out.StopReason, out.Reason, out.SystemMessage)
		if isPermission {
			setPermission(res, "deny")
		}
		// PreventContinuation 的语义是「该事件不允许继续」，只对
		// PreToolUse/PermissionRequest 成立（见 RunResult 字段文档）。Stop 事件上
		// block 的含义**相反**——是「否决停止、继续跑」（StopShouldContinue）。
		// 两者同时为 true 会让通用处理「PreventContinuation → 不继续」把
		// 「要求继续」读成「禁止继续」，语义 180 度反转（runtime#33）。
		//
		// 这里与上面 continue:false 的处理保持逐字同构：非 Stop 才置
		// PreventContinuation 并收口，Stop 落到否决停止分支。
		if event != EventStop {
			res.PreventContinuation = true
			return nil
		}
		res.StopShouldContinue = true
		if out.SystemMessage != "" {
			res.AdditionalContexts = append(res.AdditionalContexts, out.SystemMessage)
		}
		if out.Reason != "" {
			res.AdditionalContexts = append(res.AdditionalContexts, out.Reason)
		}
	default:
		return fmt.Errorf("hookx: hook 返回的 decision=%q 不在契约内（只支持 approve/block，大小写不敏感）；"+
			"该值不予采纳——需要拦截请用 block 或 continue:false", out.Decision)
	}
	return nil
}

// setPermission PermissionBehavior 的唯一写入口，deny 单调：allow 不得覆盖
// 已置的 deny。
//
// 单个 hook 的输出可以自相矛盾（`continue:false` + `decision:"approve"`），
// 两个字段写的是同一个 res；原先后置的 approve 直接顶掉先置的 deny，于是
// **同一个输出内部是「放行优先」，而跨 hook 归并（merge）是「deny 单调」**
// ——同一份语义在两个层级上方向相反，且相反的那个方向不安全（hookx#29）。
// 现在两层同口径：权限面只向保守方向收敛。
func setPermission(res *RunResult, behavior string) {
	if behavior == "allow" && res.PermissionBehavior == "deny" {
		return
	}
	res.PermissionBehavior = behavior
}

// applyContexts 附加上下文与 UpdatedInput 透传。
func applyContexts(out *Output, res *RunResult) {
	if out.AdditionalContext != "" {
		res.AdditionalContexts = append(res.AdditionalContexts, out.AdditionalContext)
	}
	if out.AdditionalContext2 != "" {
		res.AdditionalContexts = append(res.AdditionalContexts, out.AdditionalContext2)
	}
	if out.HookSpecificOutput != nil {
		res.UpdatedInput = out.HookSpecificOutput.UpdatedInput
	}
}

// merge 单调归并：阻断/deny/continue-flags 只能置位不能清除；stopReason 与
// updatedInput 后者胜；上下文累积。
func (t *RunResult) merge(next RunResult) {
	t.AdditionalContexts = append(t.AdditionalContexts, next.AdditionalContexts...)
	if next.BlockRequested {
		t.BlockRequested = true
		if next.StopReason != "" {
			t.StopReason = next.StopReason
		}
	}
	if next.PreventContinuation {
		t.PreventContinuation = true
		if next.StopReason != "" {
			t.StopReason = next.StopReason
		}
	}
	if next.StopShouldContinue {
		t.StopShouldContinue = true
		if next.StopReason != "" {
			t.StopReason = next.StopReason
		}
	}
	if next.PermissionBehavior == "deny" {
		t.PermissionBehavior = "deny" // deny 单调（保守合并）
	} else if t.PermissionBehavior != "deny" && next.PermissionBehavior != "" {
		t.PermissionBehavior = next.PermissionBehavior
	}
	if next.UpdatedInput != nil {
		t.UpdatedInput = next.UpdatedInput
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string) string {
	const cap = 200
	r := []rune(s)
	if len(r) <= cap {
		return s
	}
	return string(r[:cap]) + "…"
}
