package notifyx

// 2026-11-11 第二轮清零回归（#99）：Close 超时分支自排干后，等 space 的
// EmitWait 必须收口而不是永久阻塞。

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// blockingSink 首件卡住（触发 Close 的 ctx 超时分支），其余件堆队列。
type r2BlockingSink struct {
	entered chan struct{}
	release chan struct{}
}

func (s *r2BlockingSink) Notify(_ context.Context, _ Payload) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return nil
}

// TestEmitWaitUnblocksAfterCloseTimeout_Issue99 队列满时 EmitWait 等 space；
// Close 超时自排干（deliverLoop 退出、space 无投递者）后，EmitWait 须经
// space 信号 / done 兜底醒来撞 errClosed 返回——不再永久阻塞。
func TestEmitWaitUnblocksAfterCloseTimeout_Issue99(t *testing.T) {
	sink := &r2BlockingSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(sink.release)
	var dropped atomic.Int32
	n := NewNotifier(sink,
		WithNotifyQueueSize(1),
		WithNotifyRetry(1, time.Millisecond, time.Millisecond),
		WithOnDrop(func(Payload, error) { dropped.Add(1) }),
	)

	// 首件卡 sink（占住 deliverLoop），第二件填满队列，第三件让 EmitWait 等 space。
	n.Emit(Payload{Event: EventTaskComplete, Title: "stuck"})
	<-sink.entered
	n.Emit(Payload{Event: EventTaskComplete, Title: "queued"})
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- n.EmitWait(context.Background(), Payload{Event: EventTaskComplete, Title: "waiting"})
	}()
	time.Sleep(50 * time.Millisecond) // 让 EmitWait 进入等 space 态

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := n.Close(ctx); err == nil {
		t.Fatal("超时 Close 应返回 ctx.Err()")
	}
	select {
	case err := <-waitDone:
		if err == nil {
			t.Fatal("Close 后 EmitWait 应如实报错（errClosed），不得静默成功")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EmitWait 在 Close 超时排干后永久阻塞（runtime#99）")
	}
}
