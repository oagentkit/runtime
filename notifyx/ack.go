package notifyx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var (
	errQueueFull      = errors.New("notifyx: 出站队列已满")
	errClosed         = errors.New("notifyx: 外发器已关闭")
	errInvalidPayload = errors.New("notifyx: 载荷无效（event 不在契约枚举）")
	// errAborted Close 限期已到，本件按丢弃计（notifyx#37）。
	errAborted         = errors.New("notifyx: Close 限期已到，未排空的部分按丢弃计")
	errAckUnauthorized = errors.New("notifyx: ack 端点拒绝（检查凭证）")
)

// Ack 「等人工」事件的人工确认回执（octopus 侧契约同源）：
// Decision 取值 approve / deny / done；Operator 与 Reason 为人工操作的
// 操作人与原因（审计链路）。
type Ack struct {
	Decision string `json:"decision"`
	Operator string `json:"operator,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// AckEndpoint 拉一次确认回执：返回 (ack, nil) = 已取到；(nil, nil) = 暂无
// 回执（继续等）；(_, err) = 拉取失败（PollAck 计数后继续轮询，除非连续
// 失败超限）。
type AckEndpoint func(ctx context.Context, sessionID string) (*Ack, error)

// DefaultAckFetchTimeout HTTPAckEndpoint 单次拉取超时。
const DefaultAckFetchTimeout = 10 * time.Second

// HTTPAckEndpoint 轮询端点的 HTTP 形态：GET {baseURL}?session_id=...，
// 响应体兼容两种形态——顶层 JSON null、或 {"ack": null|{...}}（octopus
// 的 ack != null 取回结果 / ack = null 继续等）。4xx/5xx 返回错误。
func HTTPAckEndpoint(baseURL string) AckEndpoint {
	return func(ctx context.Context, sessionID string) (*Ack, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
		if err != nil {
			return nil, fmt.Errorf("notifyx: 构造 ack 请求失败: %w", err)
		}
		q := req.URL.Query()
		q.Set("session_id", sessionID)
		req.URL.RawQuery = q.Encode()
		client := &http.Client{Timeout: DefaultAckFetchTimeout}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("notifyx: ack 拉取失败: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			_, _ = io.Copy(io.Discard, resp.Body)
			return nil, errAckUnauthorized
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			return nil, fmt.Errorf("notifyx: ack 端点返回 %s，body 尾部: %.256s", resp.Status, snippet)
		}
		// {"ack": null|{...}} 形态；顶层 null 解码为全零结构体 → 同样得 nil ack
		var envelope struct {
			Ack *Ack `json:"ack"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return nil, fmt.Errorf("notifyx: ack 响应非 JSON: %w", err)
		}
		return envelope.Ack, nil
	}
}

// PollAckOption PollAck 可选项。
type PollAckOption func(*pollConfig)

type pollConfig struct {
	interval            time.Duration
	maxConsecutiveFails int
}

// WithPollInterval 轮询间隔（缺省 2s）。
func WithPollInterval(d time.Duration) PollAckOption {
	return func(c *pollConfig) {
		if d > 0 {
			c.interval = d
		}
	}
}

// WithPollMaxConsecutiveFails 连续拉取失败上限（超过即放弃返回最后错误；
// 缺省 5——瞬时网络抖动不放弃，端点持续不可达不再空转）。
func WithPollMaxConsecutiveFails(n int) PollAckOption {
	return func(c *pollConfig) {
		if n > 0 {
			c.maxConsecutiveFails = n
		}
	}
}

// PollAck 轮询等待人工确认回执：ack 非空即返回；暂无回执按 interval 继续；
// ctx 结束（超时/取消）返回 (nil, ctx.Err())。连续失败超限返回最后错误。
func PollAck(ctx context.Context, ep AckEndpoint, sessionID string, opts ...PollAckOption) (*Ack, error) {
	cfg := pollConfig{interval: 2 * time.Second, maxConsecutiveFails: 5}
	for _, o := range opts {
		o(&cfg)
	}
	var lastErr error
	consecutiveFails := 0
	for {
		ack, err := ep(ctx, sessionID)
		if err == nil {
			if ack != nil {
				return ack, nil
			}
			consecutiveFails = 0 // null 回执是健康响应，重置失败计数
		} else {
			lastErr = err
			consecutiveFails++
			if consecutiveFails >= cfg.maxConsecutiveFails {
				return nil, lastErr
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(cfg.interval):
		}
	}
}
