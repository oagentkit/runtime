package notifyx

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oekit/concurrency/async"
)

// Notifier 可靠外发器：出站队列 + 指数退避重试 + 单次尝试超时。
// Emit 永不阻塞主流程（队列满丢弃计数）；Close 排空后退出。
type Notifier struct {
	sink Sink

	queue chan Payload
	// closed 置位后 Emit 一律丢弃（防向已关闭队列发送 panic）。
	closed    atomic.Bool
	startOnce sync.Once
	closeOnce sync.Once
	done      chan struct{}

	timeout     time.Duration // 单次尝试超时（0 = Sink 自理）
	maxAttempts int           // 总尝试次数（含首次；<1 = 1）
	baseDelay   time.Duration // 退避基准
	maxDelay    time.Duration // 退避上限
	queueSize   int

	delivered atomic.Int64
	dropped   atomic.Int64
	onDrop    func(p Payload, err error)
}

// NotifierOption 外发器可选项。
type NotifierOption func(*Notifier)

// WithNotifyTimeout 单次尝试超时（0 = 交给 Sink 自理；HTTPSink 自带 10s）。
func WithNotifyTimeout(d time.Duration) NotifierOption {
	return func(n *Notifier) { n.timeout = d }
}

// WithNotifyRetry 重试参数：maxAttempts 总尝试次数（含首次，<1 视作 1）、
// baseDelay/maxDelay 指数退避边界（缺省 500ms/30s）。
func WithNotifyRetry(maxAttempts int, baseDelay, maxDelay time.Duration) NotifierOption {
	return func(n *Notifier) {
		if maxAttempts < 1 {
			maxAttempts = 1
		}
		n.maxAttempts = maxAttempts
		n.baseDelay = baseDelay
		n.maxDelay = maxDelay
	}
}

// WithNotifyQueueSize 出站队列容量（缺省 256）。满后 Emit 丢弃计数。
func WithNotifyQueueSize(n int) NotifierOption {
	return func(nr *Notifier) {
		if n > 0 {
			nr.queueSize = n
		}
	}
}

// WithOnDrop 最终丢弃回调（重试耗尽 / 队列满 / 已关闭后 Emit）：通知平台
// 侧需要强一致的消费方在此收紧（告警、落库重放）。
func WithOnDrop(f func(p Payload, err error)) NotifierOption {
	return func(n *Notifier) { n.onDrop = f }
}

// NewNotifier 构造外发器。首次 Emit 惰性启动投递 goroutine（GoSafe，panic
// 被 recover——通知面故障不炸业务）。
func NewNotifier(sink Sink, opts ...NotifierOption) *Notifier {
	n := &Notifier{
		sink:        sink,
		queue:       make(chan Payload, 256),
		done:        make(chan struct{}),
		maxAttempts: 3,
		baseDelay:   500 * time.Millisecond,
		maxDelay:    30 * time.Second,
		queueSize:   256,
	}
	for _, o := range opts {
		o(n)
	}
	if cap(n.queue) != n.queueSize {
		n.queue = make(chan Payload, n.queueSize)
	}
	return n
}

// stamp 零值 Timestamp 补 now（UTC）：Payload.Timestamp 的契约是「Emit 时零值
// 自动补 now」——调用方不填才是常规形态。
//
// 两个入口共用这一份实现是刻意的：此前两处各写一遍、条件一起写反（
// `if !p.Timestamp.IsZero()` 才补 ⇒ 零值永远补不上、`omitempty` 把字段整个
// 序列化掉，下游按时间排序/过滤/窗口全哑），单写一处不会再漂（agent#27）。
// 非零值一律保留——调用方自带的事件时刻优先于本地时钟。
func stamp(p *Payload) {
	if p.Timestamp.IsZero() {
		p.Timestamp = time.Now().UTC()
	}
}

// Emit 异步外发（非阻塞）：入出站队列即返回。队列满或已关闭时丢弃计数并
// 触发 OnDrop——通知不能阻塞主流程，丢弃是显式计量的代价。零值 Timestamp
// 自动补齐。
func (n *Notifier) Emit(p Payload) {
	stamp(&p)
	if !p.Valid() {
		n.drop(p, errInvalidPayload)
		return
	}
	n.startOnce.Do(func() {
		async.GoSafe(func() { n.deliverLoop() })
	})
	if n.closed.Load() {
		n.drop(p, errClosed)
		return
	}
	select {
	case n.queue <- p:
	default:
		n.drop(p, errQueueFull)
	}
}

// EmitWait 背压形态：阻塞直到入队或 ctx 结束——「等人工」类必须送达的通知
// 用这个（代价是主流程让出一段并发度）。其余语义同 Emit。
func (n *Notifier) EmitWait(ctx context.Context, p Payload) error {
	stamp(&p)
	if !p.Valid() {
		n.drop(p, errInvalidPayload)
		return errInvalidPayload
	}
	n.startOnce.Do(func() {
		async.GoSafe(func() { n.deliverLoop() })
	})
	if n.closed.Load() {
		n.drop(p, errClosed)
		return errClosed
	}
	select {
	case n.queue <- p:
		return nil
	case <-ctx.Done():
		n.drop(p, ctx.Err())
		return ctx.Err()
	}
}

// deliverLoop 投递循环：逐件重试（指数退避），Close 关队列后排空退出。
func (n *Notifier) deliverLoop() {
	defer close(n.done)
	for p := range n.queue {
		n.deliver(p)
	}
}

func (n *Notifier) deliver(p Payload) {
	var lastErr error
	for attempt := 0; attempt < n.maxAttempts; attempt++ {
		if attempt > 0 {
			n.sleep(backoff(n.baseDelay, n.maxDelay, attempt))
		}
		var err error
		if n.timeout > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
			err = n.sink.Notify(ctx, p)
			cancel()
		} else {
			err = n.sink.Notify(context.Background(), p)
		}
		if err == nil {
			n.delivered.Add(1)
			return
		}
		lastErr = err
	}
	n.drop(p, lastErr)
}

// sleep 退避等待（固定等待：关闭态不打断重试——Close 前已入队的件按
// 声明的重试契约走完；Close 的限期语义只约束「等它多久」，不强杀在途件）。
func (n *Notifier) sleep(d time.Duration) {
	time.Sleep(d)
}

func backoff(base, max time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	d := base << (attempt - 1)
	if d > max || d <= 0 {
		return max
	}
	return d
}

func (n *Notifier) drop(p Payload, err error) {
	n.dropped.Add(1)
	if n.onDrop != nil {
		n.onDrop(p, err)
	}
}

// Delivered 累计送达件数。
func (n *Notifier) Delivered() int64 { return n.delivered.Load() }

// Dropped 累计丢弃件数（重试耗尽 / 队列满 / 无效载荷 / 关闭后 Emit）。
func (n *Notifier) Dropped() int64 { return n.dropped.Load() }

// Close 停收新件并排空队列（限期由 ctx 控制；超时未排完的部分按丢弃计）。
func (n *Notifier) Close(ctx context.Context) error {
	n.closeOnce.Do(func() {
		n.closed.Store(true)
		close(n.queue)
	})
	select {
	case <-n.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
