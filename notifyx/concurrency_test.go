package notifyx

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentEmitDuringCloseDoesNotPanic Emit 与 Close 并发时不得 panic，
// 且每件载荷都必须被记账（送达或丢弃），没有第三种去向（notifyx#28）。
//
// 失效形态：Emit 先读 `closed.Load()` 再 send，Close 在两者之间
// `close(n.queue)` ⇒ `select` 的 send case 对已关闭 channel 照样 panic ⇒
// `send on closed channel` **进程级 crash**。而注释宣称「closed 置位后 Emit
// 一律丢弃（防 panic）」——那条保证当时并不成立。
//
// 断言三条：① 不 panic（运行时天然判）；② delivered + dropped == 调用次数
// （不漏账——漏账意味着某件既没送达也没计入丢弃）；③ 全部送达件载荷合法。
func TestConcurrentEmitDuringCloseDoesNotPanic(t *testing.T) {
	const emitters, each = 8, 300

	var deliveredCount atomic.Int64
	sink := SinkFunc(func(_ context.Context, p Payload) error {
		if p.Event == "" {
			t.Errorf("送达了空载荷")
		}
		deliveredCount.Add(1)
		return nil
	})
	n := NewNotifier(sink, WithNotifyQueueSize(8))

	var emitted atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < emitters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < each; j++ {
				emitted.Add(1)
				n.Emit(Payload{Event: EventTaskComplete, SessionID: fmt.Sprintf("s%d-%d", i, j)})
			}
		}(i)
	}

	// 关闭与 Emit 并发进行：竞窗落在「Emit 已过 closed 检查、尚未 send」。
	closeDone := make(chan error, 1)
	close(start) // 同时放行全部 emitter 与 Close
	go func() { closeDone <- n.Close(context.Background()) }()

	wg.Wait()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close 未在 10s 内返回")
	}

	// Close 之后 Emit 一律丢弃（这是注释承诺的那条不变量）。
	n.Emit(Payload{Event: EventTaskComplete, SessionID: "after-close"})
	emitted.Add(1)

	if got := n.Delivered() + n.Dropped(); got != emitted.Load() {
		t.Fatalf("账不平: delivered=%d dropped=%d 合计=%d, 调用次数=%d",
			n.Delivered(), n.Dropped(), got, emitted.Load())
	}
	if n.Delivered() != deliveredCount.Load() {
		t.Fatalf("Delivered 计数与实际送达不一致: %d vs %d", n.Delivered(), deliveredCount.Load())
	}
	if n.Dropped() == 0 {
		t.Fatal("并发关闭下必然有丢弃（队列只有 8 格），dropped 不该是 0")
	}
}

// TestCloseWithoutEmitReturns 从没 Emit 过就 Close 必须返回，不能挂死。
//
// 失效形态：投递循环惰性启动，`done` 由它关闭；没有 Emit 就没有循环，
// `Close` 等一个永不关闭的 `done`。
func TestCloseWithoutEmitReturns(t *testing.T) {
	n := NewNotifier(SinkFunc(func(context.Context, Payload) error { return nil }))
	done := make(chan error, 1)
	go func() { done <- n.Close(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("无 Emit 时 Close 挂死")
	}
	// 再 Close 一次（幂等）仍要立刻返回。
	if err := n.Close(context.Background()); err != nil {
		t.Fatalf("重复 Close: %v", err)
	}
}

// TestEmitWaitUnblocksOnClose EmitWait 在队列满时被 Close 解开：不得向已关闭
// 队列发送，也不得把调用方永久挂住。
func TestEmitWaitUnblocksOnClose(t *testing.T) {
	block := make(chan struct{})
	release := make(chan struct{})
	sink := SinkFunc(func(_ context.Context, _ Payload) error {
		select {
		case <-block:
			return nil
		case <-release:
			return nil
		}
	})
	n := NewNotifier(sink, WithNotifyQueueSize(1))
	n.Emit(Payload{Event: EventTaskComplete}) // 占住投递 goroutine
	n.Emit(Payload{Event: EventTaskComplete}) // 占满队列

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- n.EmitWait(ctx, Payload{Event: EventInputRequired}) }()

	close(release) // 让 sink 放行，队列腾空
	select {
	case <-errc:
	case <-time.After(3 * time.Second):
		t.Fatal("EmitWait 未返回")
	}
	_ = n.Close(context.Background())
	close(block)
}

// TestEmitWaitHonorsCtxWhenQueueStuck 队列永远腾不出空间时 EmitWait 按 ctx 收口
// （不能死等，也不能 panic）。
//
// 构造是**确定性**的：等 sink 真正开始处理第一件（此时队列必空、投递 goroutine
// 必堵住），再把队列填满一格，EmitWait 就必然进「等 space」分支。
func TestEmitWaitHonorsCtxWhenQueueStuck(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	sink := SinkFunc(func(_ context.Context, _ Payload) error {
		once.Do(func() { close(started) })
		<-release // 永久堵住投递 goroutine
		return nil
	})
	n := NewNotifier(sink, WithNotifyQueueSize(1))

	n.Emit(Payload{Event: EventTaskComplete}) // 第一件进 sink
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("投递 goroutine 未启动")
	}
	n.Emit(Payload{Event: EventTaskComplete}) // 填满队列（此时队列必空）
	if n.Dropped() != 0 {
		t.Fatalf("填满队列这一步不该丢弃: %d", n.Dropped())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := n.EmitWait(ctx, Payload{Event: EventInputRequired})
	close(release)
	if err == nil {
		t.Fatal("队列腾不出空间时应按 ctx 收口")
	}
	if n.Dropped() == 0 {
		t.Fatal("ctx 收口应计入丢弃")
	}
}
