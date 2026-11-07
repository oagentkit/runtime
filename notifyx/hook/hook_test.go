package hook

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/oagentkit/runtime/notifyx"
)

// mappingCases 归一化映射表用例——与主包 notifyx/doc.go 的映射表**逐条对应**：
// 表里每一条「平台事件 → 归一事件」在这里都有一个 case 钉住。两边任何一边
// 改了、另一边没跟上的漂移会直接红（runtime#36 的「同测」契约点）。
var mappingCases = []struct {
	agent Agent
	raw   string        // 平台原始事件名（原样拼写，含分隔符/大小写差异）
	want  notifyx.Event // 期望归一事件
}{
	// task_complete ← zcode Stop / cc Stop / kimi Stop / codex task_complete / opencode·mimo session.idle
	{"zcode", "Stop", notifyx.EventTaskComplete},
	{"cc", "Stop", notifyx.EventTaskComplete},
	{"kimi", "Stop", notifyx.EventTaskComplete},
	{"codex", "task_complete", notifyx.EventTaskComplete},
	{"opencode", "session.idle", notifyx.EventTaskComplete},
	{"mimo", "session.idle", notifyx.EventTaskComplete}, // mimo 与 opencode 同构共用表
	// permission_request ← zcode/cc/kimi PermissionRequest / codex PermissionRequest / opencode·mimo permission.updated
	{"zcode", "PermissionRequest", notifyx.EventPermissionRequest},
	{"cc", "PermissionRequest", notifyx.EventPermissionRequest},
	{"kimi", "PermissionRequest", notifyx.EventPermissionRequest},
	{"codex", "agent_permission_request", notifyx.EventPermissionRequest},
	{"opencode", "permission.updated", notifyx.EventPermissionRequest},
	{"mimo", "permission.updated", notifyx.EventPermissionRequest},
	// input_required ← opencode·mimo question.asked（cc 提问形态另有专项用例）
	{"opencode", "question.asked", notifyx.EventInputRequired},
	{"mimo", "question.asked", notifyx.EventInputRequired},
	// error ← kimi StopFailure（其余 failure 类走兜底规则用例）
	{"kimi", "StopFailure", notifyx.EventError},
	// session_end ← 平台显式会话结束信号（通用词，非逐平台枚举）
	{"zcode", "SessionEnd", notifyx.EventSessionEnd},
	{"cc", "session_end", notifyx.EventSessionEnd},
	// 「类」措辞的兜底规则：同族事件名无需进表也能归对
	{"cc", "PermissionRequested", notifyx.EventPermissionRequest},
	{"codex", "task_failure", notifyx.EventError},
	{"zcode", "StopWithError", notifyx.EventError},
}

// 同测锚点：上面每个 case 独立断言（不用子测试套表——失败信息直接给出
// 漂移的那一条）。
func TestNormalizeMappingTable(t *testing.T) {
	for _, tc := range mappingCases {
		// 事件名字段轮换挂在不同的候选字段上（event/hook_event_name/type/...），
		// 保证提取链路与映射链路一起被钉住。
		for _, field := range []string{"event", "hook_event_name", "type", "notification_type"} {
			data, err := json.Marshal(map[string]any{field: tc.raw})
			if err != nil {
				t.Fatal(err)
			}
			p, err := Normalize(tc.agent, data)
			if err != nil {
				t.Errorf("%s/%s(%s): %v", tc.agent, tc.raw, field, err)
				continue
			}
			if p.Event != tc.want {
				t.Errorf("%s/%s(%s) = %q, want %q", tc.agent, tc.raw, field, p.Event, tc.want)
			}
		}
	}
}

// 命名风格归一：大小写与分隔符差异不进映射语义。
func TestNormalizeNameStyleInsensitive(t *testing.T) {
	for _, name := range []string{"PermissionRequest", "permission_request", "permission-request", "PERMISSIONREQUEST"} {
		if ev, ok := lookupEvent(AgentZCode, name, nil); !ok || ev != notifyx.EventPermissionRequest {
			t.Errorf("lookupEvent(%q) = %v,%v; want permission_request", name, ev, ok)
		}
	}
}

// cc notification 不带类型字段：按形态分派——提问形态 → input_required，
// 其余（权限类为主形态）→ permission_request。
func TestNormalizeCCNotificationShapes(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    notifyx.Event
	}{
		{"提问形态-英文", `{"event":"notification","message":"Claude is waiting for your input"}`, notifyx.EventInputRequired},
		{"提问形态-中文", `{"event":"notification","message":"等待输入"}`, notifyx.EventInputRequired},
		{"权限主形态", `{"event":"notification","message":"Claude needs your permission to use Bash"}`, notifyx.EventPermissionRequest},
		{"notification_type 显式给出时优先", `{"event":"notification","notification_type":"Stop"}`, notifyx.EventTaskComplete},
	}
	for _, tc := range cases {
		p, err := Normalize(AgentCC, []byte(tc.payload))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if p.Event != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, p.Event, tc.want)
		}
	}
}

// 标准字段提取 + Extras 透传原始平台与事件名（审计维度）。
func TestNormalizeExtractsFields(t *testing.T) {
	p, err := Normalize(AgentZCode, []byte(`{"hook_event_name":"Stop","session_id":"s-42","title":"评审完成","message":"done","project":"ignored"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "s-42" || p.Title != "评审完成" || p.Message != "done" {
		t.Fatalf("字段提取: session=%q title=%q message=%q", p.SessionID, p.Title, p.Message)
	}
	if p.Extras["agent"] != "zcode" || p.Extras["raw_event"] != "Stop" {
		t.Fatalf("Extras 应透传 agent/raw_event: %v", p.Extras)
	}
	if !p.Valid() {
		t.Fatal("归一化结果必须是 notifyx 契约内的有效载荷")
	}
	// sessionId 驼峰候选也要认。
	p2, err := Normalize(AgentOpenCode, []byte(`{"type":"session.idle","sessionId":"s2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p2.SessionID != "s2" {
		t.Fatalf("sessionId 驼峰候选未生效: %q", p2.SessionID)
	}
}

// 不在契约内的事件与非法输入必须显式报错，不猜测归一。
func TestNormalizeErrors(t *testing.T) {
	cases := []struct {
		name    string
		agent   Agent
		payload string
		wantSub string
	}{
		{"事件不在映射表", AgentZCode, `{"event":"Whatever"}`, "不在映射表"},
		{"未知平台", Agent("minimax"), `{"event":"Stop"}`, "不在映射表"},
		{"缺事件名字段", AgentCC, `{"message":"x"}`, "事件名字段"},
		{"非法 JSON", AgentKimi, `{`, "不是合法 JSON"},
	}
	for _, tc := range cases {
		_, err := Normalize(tc.agent, []byte(tc.payload))
		if err == nil {
			t.Errorf("%s: 应报错", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("%s: 错误 %q 应包含 %q", tc.name, err, tc.wantSub)
		}
	}
}
