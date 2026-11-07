package hook

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oagentkit/runtime/notifyx"
)

// Agent 平台标识（挂点协议族）。未知取值在 Normalize 里按「事件不在映射表」
// 报错，不做静默兜底。
type Agent string

const (
	// AgentZCode ZCode hooks（stdin JSON，事件名如 Stop / PermissionRequest）。
	AgentZCode Agent = "zcode"
	// AgentCC Claude Code hooks（stdin JSON，Stop / Notification 提问形态）。
	AgentCC Agent = "cc"
	// AgentCodex codex notify（argv JSON，type 字段）。
	AgentCodex Agent = "codex"
	// AgentKimi kimi TOML hooks（Stop / StopFailure / PermissionRequest）。
	AgentKimi Agent = "kimi"
	// AgentOpenCode opencode JS 插件事件（session.idle / permission.updated / question.asked）。
	AgentOpenCode Agent = "opencode"
	// AgentMimo mimo 与 opencode 挂点同构（doc.go「opencode·mimo JS 插件」），
	// 映射表共用，不单独维护一份。
	AgentMimo Agent = "mimo"
)

// rawEventTable 平台原始事件名（normalizeName 归形后）→ notifyx.Event 的
// 精确映射表。这是主包 doc.go 映射表的可执行单源：表里的每一行在
// hook_test.go 都有对应用例钉住，改表必须同步改文档（反之亦然）。
var rawEventTable = map[Agent]map[string]notifyx.Event{
	AgentZCode: {
		"stop":              notifyx.EventTaskComplete,
		"permissionrequest": notifyx.EventPermissionRequest,
	},
	AgentCC: {
		"stop":              notifyx.EventTaskComplete,
		"permissionrequest": notifyx.EventPermissionRequest,
	},
	AgentCodex: {
		"taskcomplete":           notifyx.EventTaskComplete,
		"permissionrequest":      notifyx.EventPermissionRequest,
		"agentpermissionrequest": notifyx.EventPermissionRequest,
	},
	AgentKimi: {
		"stop":              notifyx.EventTaskComplete,
		"stopfailure":       notifyx.EventError,
		"permissionrequest": notifyx.EventPermissionRequest,
	},
	AgentOpenCode: {
		"sessionidle":       notifyx.EventTaskComplete,
		"permissionupdated": notifyx.EventPermissionRequest,
		"questionasked":     notifyx.EventInputRequired,
	},
}

// tableFor 取平台的映射表；mimo 与 opencode 同构共用一份；未知平台返回 nil。
func tableFor(agent Agent) map[string]notifyx.Event {
	if agent == AgentMimo {
		return rawEventTable[AgentOpenCode]
	}
	return rawEventTable[agent]
}

// normalizeName 事件名归形：小写并去掉字母数字以外的字符——
// `PermissionRequest` / `permission_request` / `session.idle` 归为同名，
// 平台命名风格差异不进映射语义。
func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// eventNameCandidates 平台载荷里可能携带事件名的字段，按优先级排列。
var eventNameCandidates = []string{"event", "hook_event_name", "type", "notification_type", "name"}

// Normalize 把平台挂点载荷归一化为 notifyx.Payload：事件名查映射表，
// session_id / title / message 提取标准字段，原始平台与事件名进 Extras
// 透传（审计可见「谁发的、原名叫什么」）。错误三态：payload 非 JSON、
// 找不到事件名字段、事件不在映射表（含未知 agent）。
func Normalize(agent Agent, data []byte) (notifyx.Payload, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return notifyx.Payload{}, fmt.Errorf("notifyx/hook: payload 不是合法 JSON: %w", err)
	}
	name := firstString(raw, eventNameCandidates...)
	if name == "" {
		return notifyx.Payload{}, fmt.Errorf("notifyx/hook: payload 里找不到事件名字段（候选: %s）", strings.Join(eventNameCandidates, "/"))
	}
	ev, ok := lookupEvent(agent, name, raw)
	if !ok {
		return notifyx.Payload{}, fmt.Errorf("notifyx/hook: %s 事件 %q 不在映射表（契约见 notifyx doc.go）", agent, name)
	}
	return notifyx.Payload{
		Event:     ev,
		SessionID: firstString(raw, "session_id", "sessionId", "session"),
		Title:     firstString(raw, "title"),
		Message:   firstString(raw, "message", "text"),
		Extras:    map[string]any{"agent": string(agent), "raw_event": name},
	}, nil
}

// lookupEvent 事件名 → 归一事件：精确表 → cc notification 形态分派 →
// 「类」措辞兜底规则 → session_end 通用信号。第二返回值 false = 不在契约内。
func lookupEvent(agent Agent, name string, raw map[string]any) (notifyx.Event, bool) {
	table := tableFor(agent)
	if table == nil {
		return "", false
	}
	norm := normalizeName(name)
	if ev, ok := table[norm]; ok {
		return ev, true
	}
	// 平台显式会话结束信号是跨平台通用词（doc.go「无挂点的平台不映射」
	// 指的是 minimax 这类没有 hooks 机制的平台，不是事件名要逐平台枚举）。
	if norm == "sessionend" {
		return notifyx.EventSessionEnd, true
	}
	if agent == AgentCC && norm == "notification" {
		return ccNotificationEvent(raw), true
	}
	return fuzzyEvent(norm)
}

// ccNotificationEvent cc notification 事件不带类型字段，按 doc.go 表的
// 措辞分派：「提问形态」（message 提到等待输入）→ input_required，
// 其余按主形态 permission_request 归一。
func ccNotificationEvent(raw map[string]any) notifyx.Event {
	if t := firstString(raw, "notification_type"); t != "" {
		if ev, ok := lookupEvent(AgentCC, t, nil); ok {
			return ev
		}
	}
	msg := strings.ToLower(firstString(raw, "message", "title"))
	for _, marker := range []string{"waiting for your input", "等待输入", "需要输入"} {
		if strings.Contains(msg, marker) {
			return notifyx.EventInputRequired
		}
	}
	return notifyx.EventPermissionRequest
}

// fuzzyEvent 精确表未命中的兜底规则，对应 doc.go 表内「PermissionRequest
// 类 / failure 类事件」的模糊措辞——平台新增同族事件名时无需改表也能归对。
func fuzzyEvent(norm string) (notifyx.Event, bool) {
	switch {
	case strings.Contains(norm, "permissionrequest"):
		return notifyx.EventPermissionRequest, true
	case strings.Contains(norm, "failure"), strings.Contains(norm, "error"):
		return notifyx.EventError, true
	}
	return "", false
}

// firstString 依次取第一个存在且为字符串的非空字段值（平台字段命名风格
// 不一，候选按优先级排列）。
func firstString(raw map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := raw[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
