package progress

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestUnsubscribeClosesChannelForRange 退订后 `for range ch` 必须**退出**
// （runtime#32）：此前 channel 永不关闭，range 永久阻塞 ⇒ goroutine 泄漏，
// 且已入缓冲的事件被钉在无人消费的 channel 上。
func TestUnsubscribeClosesChannelForRange(t *testing.T) {
	b := NewBus[int]()
	ch, cancel := b.Subscribe(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		n := 0
		for range ch { // 唯一退出路径就是 channel 被关闭
			n++
			if n > 1000 {
				return
			}
		}
	}()

	b.Publish(1)
	b.Publish(2)
	cancel() // 退订 → close(ch) → range 退出
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("退订后 for range ch 未退出——channel 仍未关闭（goroutine 泄漏）")
	}
}

// TestUnsubscribeIdempotentNoDoubleClose 重复退订不得 panic（runtime#32 的
// 连带缺陷）：此前 `delete(b.subs, ch)` 在 once **之外**，重复调用是幂等的；
// 加了 close 之后若不同步收进 once，第二次调用会 close 已关闭的 channel 而
// panic——这是「修一个泄漏、引一个崩溃」的经典形态，故单独钉住。
func TestUnsubscribeIdempotentNoDoubleClose(t *testing.T) {
	b := NewBus[int]()
	_, cancel := b.Subscribe(context.Background())
	cancel()
	cancel() // 显式二次退订
	func() { // 再多来几次，覆盖 once 之外的路径
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("重复退订 panic: %v", r)
			}
		}()
		cancel()
		cancel()
	}()

	// ctx 取消与显式 cancel 双向收口时同样只允许 close 一次
	ctx, ccancel := context.WithCancel(context.Background())
	_, cancel2 := b.Subscribe(ctx)
	cancel2()
	ccancel()
	time.Sleep(20 * time.Millisecond) // 等监听 goroutine 的 unsubscribe 跑完
	cancel2()
}

// TestPublishDuringUnsubscribeNoSendOnClosed 并发 Publish 与退订不得出现
// send on closed channel（runtime#32 的核心安全性论证）：
// Publish 持 b.mu 遍历并发送，unsubscribe 持同一把锁摘注册 + close(ch)
// ⇒ 两者互斥，close 之后不会再有 send。旧注释称该竞窗「不可消除」，
// 本用例是那个判断的反证。
func TestPublishDuringUnsubscribeNoSendOnClosed(t *testing.T) {
	b := NewBus[int]()
	var wg sync.WaitGroup

	// 8 个发布者持续打事件
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 2000; j++ {
				b.Publish(j)
			}
		}()
	}
	// 同时反复订阅/退订，退订即 close
	for i := 0; i < 200; i++ {
		ch, cancel := b.Subscribe(context.Background())
		go func() {
			for range ch { // 关闭即退出
			}
		}()
		cancel()
	}
	wg.Wait()
	// panic 会在上面的 goroutine 里直接炸掉测试进程，走到这里即为安全
}

// TestUnsubscribeDrainsBufferedBeforeClose 关闭前已入缓冲的事件仍可被取完
// （range 语义：先取缓冲、再退出）——不得因为 close 而丢已投递的事件。
func TestUnsubscribeDrainsBufferedBeforeClose(t *testing.T) {
	b := NewBus[int]()
	ch, cancel := b.Subscribe(context.Background())
	b.Publish(7)
	b.Publish(8)
	cancel()

	var got []int
	for v := range ch {
		got = append(got, v)
	}
	if len(got) != 2 || got[0] != 7 || got[1] != 8 {
		t.Fatalf("关闭前已入缓冲的事件应可完整取出，得到 %v", got)
	}
}
