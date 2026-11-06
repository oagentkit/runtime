// Package progress 泛型事件总线。
// 多订阅者广播，每个订阅者 64 缓冲，满了就丢（观测数据允许有损）。
// 发布永不阻塞（订阅者慢不拖慢主链路）。
package progress

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/oekit/concurrency/async"
)

// Bus 泛型多订阅者广播总线。
type Bus[T any] struct {
	mu      sync.Mutex
	subs    map[chan T]struct{}
	dropped atomic.Int64 // 订阅者积压导致的丢弃事件计数（Dropped 观测）
}

// Dropped 累计丢弃的事件数（订阅者积压时）。有损是声明的设计，
// 计数让"进度条停在中间态"可归因。
func (b *Bus[T]) Dropped() int64 {
	if b == nil {
		return 0
	}
	return b.dropped.Load()
}

// NewBus 创建事件总线。
func NewBus[T any]() *Bus[T] {
	return &Bus[T]{subs: map[chan T]struct{}{}}
}

// Publish 非阻塞广播。nil Bus 安全（no-op）。
func (b *Bus[T]) Publish(e T) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // 订阅者积压：丢弃（计数可观测）
			b.dropped.Add(1)
		}
	}
}

// Subscribe 注册订阅者；返回取消函数（ctx 结束也会自动取消）。
// nil Bus 安全：返回永不产出的 nil channel 与 no-op cancel（与 Publish/Dropped
// 同一 nil 契约）。
// cancel 与 ctx 结束双向收口：任一发生，监听 goroutine 都会退出——
// 此前手动 cancel 只删 channel 不唤醒监听 goroutine，ctx 为 Background 时
// 每个订阅者永久泄漏一个 goroutine + 一条残留 channel。
//
// channel 在**退订/ctx 结束时关闭**（runtime#32，此前是 #20 的「永不关闭」）：
// `for range ch` 可正常退出，也不会再把已缓冲事件挂在无人消费的 channel 上。
//
// 旧实现不 close 的理由是「close 与并发 send 的竞窗不可消除」——这个前提是错的：
// Publish 遍历并发送时持 b.mu，unsubscribe 摘注册与 close 也在 b.mu 内，
// 两者**同锁互斥** ⇒ 不存在「send 到已关闭 channel」的窗口，close 是安全的。
//
// ⚠️ 消费方契约随之改变：select 消费必须检查 ok（`v, ok := <-ch`）。
// 关闭后该 case 恒可就绪并反复给出零值，写 `case v := <-ch:` 而不查 ok 的
// 循环会空转——这是 Go 里所有可关闭 channel 的通用前提，不是本包的额外要求。
func (b *Bus[T]) Subscribe(ctx context.Context) (<-chan T, func()) {
	if b == nil {
		return nil, func() {}
	}
	ch := make(chan T, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	stop := make(chan struct{})
	// 全部收进 once：delete 与 close 必须**只发生一次**。此前只有 close(stop)
	// 在 once 里，摘注册在外面 ⇒ 重复调 cancel 会二次 close(ch) panic
	// （关闭 channel 之前这个重复 delete 是幂等的，close 之后就不是了）。
	unsubscribe := func() {
		once.Do(func() {
			close(stop)
			b.mu.Lock()
			delete(b.subs, ch)
			// 持锁 close：与 Publish 的持锁 send 互斥（见函数上方契约）。
			// 已入缓冲的事件不受影响——range 会先把它们取完再退出。
			close(ch)
			b.mu.Unlock()
		})
	}
	async.GoWithContext(ctx, func(ctx context.Context) {
		select {
		case <-ctx.Done():
			unsubscribe()
		case <-stop:
		}
	})
	return ch, unsubscribe
}
