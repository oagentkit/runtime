package notifyx

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// captureSink 把送达的载荷转发到 channel——事件驱动，不靠 sleep 猜时序。
func captureSink() (Sink, <-chan Payload) {
	ch := make(chan Payload, 8)
	return SinkFunc(func(_ context.Context, p Payload) error {
		ch <- p
		return nil
	}), ch
}

func recvPayload(t *testing.T, ch <-chan Payload) Payload {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("2s 内未收到载荷")
		return Payload{}
	}
}

// TestEmitStampsZeroTimestamp 零值 Timestamp 必须补 now（agent#27）。
//
// 失效形态：条件写成 `if !p.Timestamp.IsZero()` ⇒ 调用方不填（Emit 的常规
// 形态）时时间戳永远是零值，`omitempty` 把 `timestamp` 字段整个序列化掉，
// 下游按时间排序/过滤/窗口全哑且**无任何信号**。
func TestEmitStampsZeroTimestamp(t *testing.T) {
	sink, got := captureSink()
	n := NewNotifier(sink)
	before := time.Now().UTC()
	n.Emit(Payload{Event: EventTaskComplete, SessionID: "s1"})
	after := time.Now().UTC()
	if err := n.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	p := recvPayload(t, got)
	if p.Timestamp.IsZero() {
		t.Fatal("零值时间戳未补 now——下游按时间排序/过滤全哑")
	}
	if p.Timestamp.Location() != time.UTC {
		t.Fatalf("应补 UTC 时刻，实得 %v", p.Timestamp.Location())
	}
	if p.Timestamp.Before(before) || p.Timestamp.After(after) {
		t.Fatalf("补的时刻 %v 不在 emit 窗口 [%v, %v] 内", p.Timestamp, before, after)
	}
}

// TestEmitPreservesExplicitTimestamp 成对对照：显式时刻必须原样保留——
// 否则「一律覆盖成本地时钟」也能让上一条全绿，而调用方自带的事件时刻被吞掉。
func TestEmitPreservesExplicitTimestamp(t *testing.T) {
	sink, got := captureSink()
	n := NewNotifier(sink)
	explicit := time.Date(2020, 3, 4, 5, 6, 7, 0, time.FixedZone("CST", 8*3600))
	n.Emit(Payload{Event: EventTaskComplete, Timestamp: explicit})
	if err := n.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	if p := recvPayload(t, got); !p.Timestamp.Equal(explicit) {
		t.Fatalf("显式时刻被改写: got %v want %v", p.Timestamp, explicit)
	}
}

// TestEmitWaitStampsZeroTimestamp EmitWait 同病同治（issue 点名 :120-121）。
func TestEmitWaitStampsZeroTimestamp(t *testing.T) {
	sink, got := captureSink()
	n := NewNotifier(sink)
	if err := n.EmitWait(context.Background(), Payload{Event: EventInputRequired}); err != nil {
		t.Fatal(err)
	}
	if err := n.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	if p := recvPayload(t, got); p.Timestamp.IsZero() {
		t.Fatal("EmitWait 零值时间戳未补 now")
	}
}

// TestPayloadJSONKeepsTimestamp 线格式钉子：补过的时间戳必须真的出现在序列化
// 结果里（`omitempty` 下零值会被整字段丢掉——这正是缺陷「静默丢字段」的机理）。
func TestPayloadJSONKeepsTimestamp(t *testing.T) {
	sink, got := captureSink()
	n := NewNotifier(sink)
	n.Emit(Payload{Event: EventError})
	if err := n.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(recvPayload(t, got))
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["timestamp"]; !ok {
		t.Fatalf("序列化结果缺 timestamp 字段（omitempty 吃掉了零值）: %s", raw)
	}
}
