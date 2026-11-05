package notifyx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Event 归一化生命周期事件（契约枚举，映射表见包文档）。
type Event string

const (
	// EventTaskComplete 任务完成（zcode Stop / kimi Stop / opencode session.idle …）。
	EventTaskComplete Event = "task_complete"
	// EventPermissionRequest 等人工授权（zcode PermissionRequest / kimi PermissionRequest …）。
	EventPermissionRequest Event = "permission_request"
	// EventInputRequired 等人工输入（opencode question.asked / cc 提问形态 notification）。
	EventInputRequired Event = "input_required"
	// EventError 任务失败（kimi StopFailure / 各平台 failure 类）。
	EventError Event = "error"
	// EventSessionEnd 会话结束（平台显式信号）。
	EventSessionEnd Event = "session_end"
)

// Priority 载荷优先级词表（提示性字段，通知平台自行取舍）。
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
)

// Payload 归一化通知载荷（标准字段 + Extras 透传平台特有维度）。
type Payload struct {
	// Event 事件类型（必填；空值经 Valid 判定无效）。
	Event Event `json:"event"`
	// Timestamp 事件时刻（Emit 时零值自动补 now）。
	Timestamp time.Time `json:"timestamp,omitempty"`
	// SessionID 会话标识（ack 轮询按它对账）。
	SessionID string `json:"session_id,omitempty"`
	// Title 通知标题（如「argus 评审完成」）。
	Title string `json:"title,omitempty"`
	// Message 正文。
	Message string `json:"message,omitempty"`
	// Project 项目标识（仓路径/名）。
	Project string `json:"project,omitempty"`
	// Branch 分支。
	Branch string `json:"branch,omitempty"`
	// Priority 优先级（low/normal/high；空 = normal）。
	Priority string `json:"priority,omitempty"`
	// Extras 平台特有维度透传（工具名、耗时、错误分类…）。
	Extras map[string]any `json:"extras,omitempty"`
}

// Valid 载荷是否可外发（Event 必须是契约枚举之一）。
func (p Payload) Valid() bool {
	switch p.Event {
	case EventTaskComplete, EventPermissionRequest, EventInputRequired, EventError, EventSessionEnd:
		return true
	}
	return false
}

// Sink 通知出口：把归一化载荷外发到通知平台。实现要求幂等友好（重试由
// Notifier 驱动，同一载荷可能到达多次——通知平台按 timestamp+session_id
// 去重或接受重复通知）。
type Sink interface {
	// Notify 外发一条通知。返回错误 = 本次尝试失败（Notifier 决定重试/丢弃）。
	Notify(ctx context.Context, p Payload) error
}

// DefaultHTTPTimeout HTTPSink 单次请求缺省超时。
const DefaultHTTPTimeout = 10 * time.Second

// HTTPSink webhook 通知出口：POST JSON 到固定 URL。
type HTTPSink struct {
	// URL 通知端点。
	URL string
	// Client HTTP 客户端（nil = 缺省 10s 超时）。
	Client *http.Client
	// Headers 附加请求头（认证凭证从这里进：Authorization / X-Webhook-Token）。
	Headers map[string]string
}

// NewHTTPSink 构造 webhook 出口。
func NewHTTPSink(url string) *HTTPSink {
	return &HTTPSink{URL: url}
}

// WithHeader 附加请求头（链式，凭证注入点）。
func (s *HTTPSink) WithHeader(k, v string) *HTTPSink {
	if s.Headers == nil {
		s.Headers = make(map[string]string)
	}
	s.Headers[k] = v
	return s
}

func (s *HTTPSink) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: DefaultHTTPTimeout}
}

// Notify POST JSON（Sink 实现）。2xx 视为送达；其余按错误返回（含响应
// 尾部片段，限长防爆）。
func (s *HTTPSink) Notify(ctx context.Context, p Payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("notifyx: 载荷序列化失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notifyx: 构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("notifyx: 外发失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return fmt.Errorf("notifyx: 端点返回 %s，body 尾部: %.256s", resp.Status, snippet)
}
