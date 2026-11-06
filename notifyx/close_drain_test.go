package notifyx

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingSink 首次调用阻塞到 release；计数送达次数。
type blockingSink struct {
	entered  chan struct{}
	release  chan struct{}
	calls    atomic.Int32
	accepted atomic.Int32
}

func (s *blockingSink) Notify(_ context.Context, _ Payload) error {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.release // 模拟「sink 卡在一次不理 ctx 的调用里」
	}
	s.accepted.Add(1)
	return nil
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("超时等待：%s", what)
}

// TestCloseTimeoutDropsRemaining Close 的限期到了之后，队列里剩下的件必须
// **不再投递**并计入 Dropped（notifyx#37）。
//
// 此前 Close 超时只是自己返回 ctx.Err()，后台照常把剩下的件投递成功——
// 「超时未排完的部分按丢弃计」两头都不成立：既没丢弃，也没计数。调用方据此
// 判定「未送达」而实际送达了；与它据此判定「已送达」而实际丢弃的一样有害。
func TestCloseTimeoutDropsRemaining(t *testing.T) {
	sink := &blockingSink{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	defer release() // 用例中途失败也不留下卡住的 goroutine

	var onDrop atomic.Int32
	n := NewNotifier(sink,
		WithNotifyRetry(1, time.Millisecond, time.Millisecond), // 无退避，聚焦「是否还投递」
		WithOnDrop(func(Payload, error) { onDrop.Add(1) }),
	)

	for i := 0; i < 5; i++ {
		n.Emit(Payload{Event: EventTaskComplete, Title: "t"})
	}
	select {
	case <-sink.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("超时：sink 未收到首件")
	}

	// 限期已到的 Close。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close 应返回 ctx 错误，得到 %v", err)
	}

	// 剩下的 4 件：不投递、计入丢弃，且 OnDrop 同步收到。
	waitUntil(t, "剩余 4 件计入 Dropped", func() bool { return n.Dropped() == 4 })
	if got := onDrop.Load(); got != 4 {
		t.Fatalf("OnDrop 应收到 4 次丢弃，得到 %d", got)
	}
	if got := n.Delivered(); got != 0 {
		t.Fatalf("限期到时不该有任何件被计为送达，得到 %d", got)
	}

	// 放行在飞件：它属于「已排空的部分」，照常送达。
	release()
	waitUntil(t, "在飞件送达", func() bool { return n.Delivered() == 1 })

	if got := sink.accepted.Load(); got != 1 {
		t.Fatalf("sink 只该收到 1 件（其余按丢弃计），实际 %d 件", got)
	}
	if got := n.Delivered() + n.Dropped(); got != 5 {
		t.Fatalf("账不平：Delivered+Dropped=%d，应为 5", got)
	}
}

// TestCloseWithinDeadlineDrainsEverything 成对对照：限期充裕时不得丢任何件——
// 否则「一律放弃」也能让上一条绿，而正常停机开始丢通知。
func TestCloseWithinDeadlineDrainsEverything(t *testing.T) {
	sink := &blockingSink{entered: make(chan struct{}), release: make(chan struct{})}
	close(sink.release) // 本用例不让 sink 阻塞

	n := NewNotifier(sink, WithNotifyRetry(1, time.Millisecond, time.Millisecond))
	for i := 0; i < 5; i++ {
		n.Emit(Payload{Event: EventTaskComplete, Title: "t"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := n.Close(ctx); err != nil {
		t.Fatalf("限期内完成排空应返回 nil，得到 %v", err)
	}
	if got := n.Delivered(); got != 5 {
		t.Fatalf("送达 %d 件，应为 5", got)
	}
	if got := n.Dropped(); got != 0 {
		t.Fatalf("限期内排空不该丢任何件，得到 %d", got)
	}
}
