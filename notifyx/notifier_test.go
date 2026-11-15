package notifyx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func slowConfig() []NotifierOption {
	return []NotifierOption{
		WithNotifyRetry(3, 5*time.Millisecond, 15*time.Millisecond),
		WithNotifyQueueSize(16),
	}
}

// 送达 + 重试恢复：前两次 500、第三次 200 → Delivered=1。
func TestNotifierRetryRecovers(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(NewHTTPSink(srv.URL), slowConfig()...)
	n.Emit(Payload{Event: EventTaskComplete, SessionID: "s1"})
	if err := n.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n.Delivered() != 1 || n.Dropped() != 0 {
		t.Fatalf("delivered=%d dropped=%d, want 1/0", n.Delivered(), n.Dropped())
	}
	if calls.Load() != 3 {
		t.Fatalf("应重试到第 3 次: %d", calls.Load())
	}
}

// 重试耗尽 → 丢弃计数 + OnDrop 回调。
func TestNotifierDropsAfterExhaustion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var dropped Payload
	n := NewNotifier(NewHTTPSink(srv.URL), append(slowConfig(),
		WithOnDrop(func(p Payload, _ error) { dropped = p }))...)
	n.Emit(Payload{Event: EventError, SessionID: "s2", Message: "boom"})
	if err := n.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n.Dropped() != 1 || n.Delivered() != 0 {
		t.Fatalf("dropped=%d delivered=%d, want 1/0", n.Dropped(), n.Delivered())
	}
	if dropped.SessionID != "s2" {
		t.Fatalf("OnDrop 应收到载荷: %+v", dropped)
	}
}

// 队列满：Emit 非阻塞丢弃（主流程永不卡住）。
func TestEmitNeverBlocksWhenQueueFull(t *testing.T) {
	release := make(chan struct{})
	sink := SinkFunc(func(_ context.Context, _ Payload) error {
		<-release // 第一件挂住投递 goroutine，队列堆积
		return nil
	})
	n := NewNotifier(sink, WithNotifyQueueSize(1))
	n.Emit(Payload{Event: EventTaskComplete}) // 占住投递
	deadline := time.After(2 * time.Second)
	for i := 0; i < 8; i++ {
		select {
		case <-deadline:
			close(release)
			t.Fatal("Emit 阻塞了主流程")
		default:
		}
		n.Emit(Payload{Event: EventTaskComplete})
	}
	close(release)
	_ = n.Close(context.Background())
	if n.Dropped() == 0 {
		t.Fatal("队列满应产生丢弃计数")
	}
}

// 无效载荷丢弃 + Close 排空：Close 前入队的件全部送达。
func TestCloseDrainsQueue(t *testing.T) {
	var delivered atomic.Int32
	sink := SinkFunc(func(_ context.Context, _ Payload) error {
		delivered.Add(1)
		return nil
	})
	n := NewNotifier(sink, slowConfig()...)
	for i := 0; i < 5; i++ {
		n.Emit(Payload{Event: EventSessionEnd, SessionID: "s3"})
	}
	n.Emit(Payload{Event: "bogus"}) // 无效：不在契约枚举
	if err := n.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if delivered.Load() != 5 {
		t.Fatalf("Close 应排空 5 件: %d", delivered.Load())
	}
	if n.Dropped() != 1 {
		t.Fatalf("无效载荷应丢弃 1 件: %d", n.Dropped())
	}
	// 关闭后 Emit 一律丢弃
	n.Emit(Payload{Event: EventTaskComplete})
	if n.Dropped() != 2 {
		t.Fatalf("关闭后 Emit 应丢弃: %d", n.Dropped())
	}
}

// EmitWait 背压形态：入队成功返回 nil。
func TestEmitWaitEnqueues(t *testing.T) {
	var delivered atomic.Int32
	sink := SinkFunc(func(_ context.Context, _ Payload) error { delivered.Add(1); return nil })
	n := NewNotifier(sink, slowConfig()...)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := n.EmitWait(ctx, Payload{Event: EventPermissionRequest, SessionID: "s4"}); err != nil {
		t.Fatalf("EmitWait: %v", err)
	}
	if err := n.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if delivered.Load() != 1 {
		t.Fatalf("delivered=%d", delivered.Load())
	}
}

// Ack 轮询：null → null → ack 三拍收敛；HTTP 两种响应形态兼容。
func TestPollAckConverges(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/ack", func(w http.ResponseWriter, _ *http.Request) {
		switch hits.Add(1) {
		case 1, 2:
			_, _ = w.Write([]byte(`{"ack": null}`))
		default:
			_, _ = w.Write([]byte(`{"ack":{"decision":"approve","operator":"zhang","reason":"ok"}}`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ack, err := PollAck(ctx, HTTPAckEndpoint(srv.URL+"/ack"), "sess-1", WithPollInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if ack.Decision != "approve" || ack.Operator != "zhang" {
		t.Fatalf("ack 不符: %+v", ack)
	}
}

// 顶层 null 形态（部分平台直接回 JSON null）→ 暂无回执。
func TestAckTopLevelNull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`null`))
	}))
	defer srv.Close()
	ack, err := HTTPAckEndpoint(srv.URL)(context.Background(), "s")
	if err != nil || ack != nil {
		t.Fatalf("顶层 null 应解析为暂无回执: ack=%v err=%v", ack, err)
	}
}

// {"ack": null} 同样是「暂无回执」的文档形态（不得被当形态错误）。
func TestAckEnvelopeNullIsNoAck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ack": null}`))
	}))
	defer srv.Close()
	ack, err := HTTPAckEndpoint(srv.URL)(context.Background(), "s")
	if err != nil || ack != nil {
		t.Fatalf("{\"ack\": null} 应解析为暂无回执: ack=%v err=%v", ack, err)
	}
}

// TestAckBareAckShapeFailsLoud 回归（runtime#127）：裸 Ack（无 ack 包裹）
// 曾解码成功但 envelope.Ack == nil → (nil, nil)，被 PollAck 当「健康但暂无
// 回执」，连续失败计数被重置，一直空转到 ackTimeout。契约外的对象必须
// fail-loud，走 error 路径。
func TestAckBareAckShapeFailsLoud(t *testing.T) {
	for _, body := range []string{
		`{"decision":"approve","operator":"z"}`, // 裸 Ack
		`{}`,                                    // 空对象同样无 ack 键
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		ack, err := HTTPAckEndpoint(srv.URL)(context.Background(), "s")
		srv.Close()
		if err == nil {
			t.Fatalf("契约外响应 %s 应返回错误，实得 ack=%+v", body, ack)
		}
		if ack != nil {
			t.Fatalf("形态错误时不得给出 ack: %+v", ack)
		}
		if !strings.Contains(err.Error(), "形态不在契约内") {
			t.Fatalf("错误应说明形态不在契约内，实得: %v", err)
		}
	}
}

// TestPollAckBareAckShapeAborts 回归（runtime#127 的失效面）：裸 Ack 必须
// 计入连续失败并快速放弃，而不是当「暂无回执」轮询到 ctx 超时。
func TestPollAckBareAckShapeAborts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"decision":"approve"}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	ack, err := PollAck(ctx, HTTPAckEndpoint(srv.URL), "s",
		WithPollInterval(time.Millisecond), WithPollMaxConsecutiveFails(3))
	if err == nil {
		t.Fatalf("裸 Ack 持续响应应最终返回错误，实得 ack=%+v", ack)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("裸 Ack 应计入连续失败快速放弃，不得空转到 ctx 超时（耗时 %v）", time.Since(start))
	}
	if hits.Load() != 3 {
		t.Fatalf("连续失败上限 3 应恰拉 3 次，实际 %d", hits.Load())
	}
}

// 非对象/非法 ack 字段同样是形态错误，不能静默当暂无回执。
func TestAckInvalidShapesFailLoud(t *testing.T) {
	for _, body := range []string{`[]`, `"ok"`, `{"ack": 123}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := HTTPAckEndpoint(srv.URL)(context.Background(), "s")
		srv.Close()
		if err == nil {
			t.Fatalf("响应 %s 应返回错误", body)
		}
	}
}

// 端点持续失败：连续失败超限放弃（不再空转），返回最后错误。
func TestPollAckAbortsOnPersistentFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := PollAck(ctx, HTTPAckEndpoint(srv.URL), "s",
		WithPollInterval(5*time.Millisecond), WithPollMaxConsecutiveFails(3))
	if err == nil {
		t.Fatal("持续失败应返回错误")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("连续失败超限应及时放弃，耗时 %v", time.Since(start))
	}
}

// 载荷经 HTTPSink 外发的 JSON 形态钉子（snake_case 契约）。
func TestPayloadWireShape(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		got = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := Payload{Event: EventPermissionRequest, SessionID: "s9", Project: "argus", Branch: "main", Priority: PriorityHigh}
	if err := NewHTTPSink(srv.URL).Notify(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("载荷非 JSON: %s", got)
	}
	if m["event"] != "permission_request" || m["session_id"] != "s9" {
		t.Fatalf("契约键名不符: %s", got)
	}
	if _, ok := m["timestamp"]; !ok {
		t.Fatalf("HTTPSink 直调不补时间戳是 Sink 语义（Emit 才补），载荷自带时间戳字段: %s", got)
	}
}

// SinkFunc 适配器：闭包变 Sink（测试与轻量集成用）。
type SinkFunc func(ctx context.Context, p Payload) error

func (f SinkFunc) Notify(ctx context.Context, p Payload) error { return f(ctx, p) }
