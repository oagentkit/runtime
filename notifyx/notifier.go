package notifyx

import (
	"context"
	"errors"
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
	// sendMu 串行化「closed 检查 + 入队」与「置 closed + close(queue)」。
	//
	// 此前两者之间是 check-then-act 竞窗：Emit 读到 closed=false，Close 随即
	// close(queue)，Emit 再 send ⇒ `send on closed channel` **进程级 panic**
	// （select 的 send case 对已关闭 channel 照样 panic，不提供保护）。注释
	// 宣称的「closed 置位后 Emit 一律丢弃」当时并不成立（notifyx#28）。
	// 现在检查与入队在同一把锁内，close 也在锁内，竞窗不可达。
	sendMu sync.RWMutex
	// closed 置位后 Emit 一律丢弃（防向已关闭队列发送 panic）。
	closed atomic.Bool
	// space 队列腾出容量信号（容量 1）：EmitWait 等它而不是轮询睡眠。
	// deliverLoop 每取走一件非阻塞投一个（满了就丢——信号是「可能有空位」，
	// 不是计数，等待方醒来重试即可）。
	space     chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	done      chan struct{}
	// abort 在 Close 的限期耗尽时关闭：此后**不再开始新的投递**，队列中
	// 剩下的件逐件计入丢弃，在途重试的退避也被打断（notifyx#37）。
	//
	// 此前 Close 超时只是自己返回 ctx.Err()，后台照常把剩下的件投递成功——
	// 文档写的「超时未排完的部分按丢弃计」两头都不成立：既没丢弃，也没计数。
	// 调用方据此判定「未送达」而实际送达了，与它据此判定「已送达」而实际
	// 丢弃的一样糟：契约必须只有一种读法。
	abort     chan struct{}
	abortOnce sync.Once

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
		space:       make(chan struct{}, 1),
		abort:       make(chan struct{}),
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
	n.startLoop()
	if err := n.enqueue(p); err != nil {
		n.drop(p, err)
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
	n.startLoop()
	for {
		err := n.enqueue(p)
		if err == nil {
			return nil
		}
		if errors.Is(err, errClosed) {
			n.drop(p, err)
			return err
		}
		// 队列满：等 deliverLoop 取走一件（事件驱动，不轮询睡眠）。
		// 等待期间**不持锁**——否则 Close 会卡在一个被慢 sink 堵住的
		// EmitWait 后面，连带 Close 的 ctx 限期失效。
		// n.done 兜底（runtime#99）：Close 的 ctx 超时分支**自排干**队列、
		// deliverLoop 已退出（不再有 space 投递者）——只等 space 的 EmitWait
		// 会永久阻塞。done 关闭即 Close 全路径完成，醒来重试 enqueue 撞
		// errClosed 如实返回。
		select {
		case <-n.space:
		case <-n.done:
		case <-ctx.Done():
			n.drop(p, ctx.Err())
			return ctx.Err()
		}
	}
}

// startLoop 惰性启动投递 goroutine（GoSafe：通知面故障不炸业务）。
func (n *Notifier) startLoop() {
	n.startOnce.Do(func() {
		async.GoSafe(func() { n.deliverLoop() })
	})
}

// enqueue 把载荷投入出站队列：**closed 检查与 send 在同一把读锁内**，Close 的
// 「置 closed + close(queue)」在同一把写锁内 ⇒ 不存在「检查后关闭」的竞窗
// （notifyx#28）。返回 errClosed / errQueueFull 供调用方分别计数。
//
// 只做非阻塞投递（select + default）：Emit 的「永不阻塞主流程」由此成为结构性
// 事实，阻塞语义由 EmitWait 在外层用 space 信号自己实现。
func (n *Notifier) enqueue(p Payload) error {
	n.sendMu.RLock()
	defer n.sendMu.RUnlock()
	if n.closed.Load() {
		return errClosed
	}
	select {
	case n.queue <- p:
		return nil
	default:
		return errQueueFull
	}
}

// deliverLoop 投递循环：逐件重试（指数退避），Close 关队列后排空退出。
//
// abort 置位后（Close 限期已到）**不再开始新的投递**：剩下的件逐件计入
// 丢弃并触发 OnDrop，队列仍照常排空以便 done 关闭（notifyx#37）。
func (n *Notifier) deliverLoop() {
	defer close(n.done)
	for p := range n.queue {
		select { // 让出容量信号（非阻塞：满即丢，等待方醒来会重试）
		case n.space <- struct{}{}:
		default:
		}
		if n.aborted() {
			n.drop(p, errAborted)
			continue
		}
		if err := n.deliver(p); errors.Is(err, errAborted) {
			n.drop(p, errAborted)
		}
	}
}

// aborted Close 限期是否已到。
func (n *Notifier) aborted() bool {
	select {
	case <-n.abort:
		return true
	default:
		return false
	}
}

// abortNow 置位中止信号（幂等）。
func (n *Notifier) abortNow() { n.abortOnce.Do(func() { close(n.abort) }) }

// deliver 逐次重试投递。返回 errAborted = Close 限期已到、本次投递中止
// （由调用方计入丢弃）；其余情况自行 drop 并返回 nil。
func (n *Notifier) deliver(p Payload) error {
	var lastErr error
	for attempt := 0; attempt < n.maxAttempts; attempt++ {
		if n.aborted() {
			return errAborted
		}
		if attempt > 0 && !n.sleep(backoff(n.baseDelay, n.maxDelay, attempt)) {
			return errAborted // 退避期间限期到：不再尝试，本件按丢弃计
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
			return nil
		}
		lastErr = err
	}
	n.drop(p, lastErr)
	return nil
}

// sleep 退避等待。返回 false = 期间 Close 限期到了，调用方应中止本次投递。
//
// 「关闭态不打断重试」仍然成立：正常 Close（排空完成前 ctx 未到期）不会置位
// abort，已入队的件照声明的重试契约走完。**只有** Close 的限期已经耗尽时才
// 打断——那一刻契约已宣告这些件按丢弃计，继续重试只会让 Close 返回之后
// 还有成功投递，正是 notifyx#37 的形态。
func (n *Notifier) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-n.abort:
		return false
	}
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
		n.sendMu.Lock() // 与 enqueue 的「检查 + send」互斥：锁内 close 才安全
		defer n.sendMu.Unlock()
		n.closed.Store(true)
		close(n.queue)
	})
	// 从没 Emit 过就 Close 时也必须把投递循环拉起来：drain 靠它跑完，
	// done 靠它关闭——否则 Close 永远等一个不存在的 goroutine（挂死）。
	n.startLoop()
	select {
	case <-n.done:
		return nil
	case <-ctx.Done():
		// 限期已到：剩下的一律按丢弃计（notifyx#37）。
		//
		// 记账**不能交给投递循环**：循环是串行的，正卡在一次不理 ctx 的 sink
		// 调用里时它连下一次取件都做不到，队列里剩下的件就永远不会被计——
		// 「既不丢弃也不计数」正是 issue 报的形态。故由 Close 自己把队列排干。
		// channel 的每次接收只归一个接收者，与循环并发排空不会重复计数：
		// 循环取到的那件走它自己的 abort 分支，Close 取到的计在这里。
		n.abortNow()
		for {
			p, ok := <-n.queue
			if !ok {
				break
			}
			n.drop(p, errAborted)
			// 让出容量信号（runtime#99）：与 deliverLoop 同纪律——等 space
			// 的 EmitWait 醒来重试。done 兜底（EmitWait 侧）保证最终全部
			// 收口，这里投信号让最先等的那个立即走。
			select {
			case n.space <- struct{}{}:
			default:
			}
		}
		return ctx.Err()
	}
}
