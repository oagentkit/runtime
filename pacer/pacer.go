// Package pacer 流式增量节流器：LLM token 级分片若逐条发布会洪泛事件通道与
// 前端（每秒数次×千字级文本），按「距上次发布 ≥interval 或缓冲 ≥bytes」合并
// 发布；总量帽内单流 seq 自 1 单调递增；权威快照到达即 Absorb 收口（此后迟到
// 分片丢弃）；收尾残尾用 Flush 落地。
//
// 单 goroutine 契约：与消费方事件回调同一约束面（回调在 drain 循环内同步执行，
// 不阻塞不并发），故不加锁；跨 goroutine 使用由调用方自行串行化。
//
// 生命周期: Experimental——未过一个 minor 无 breaking 窗口（api-compat.md 晋级条件一），初值不承诺兼容
package pacer

import (
	"time"
	"unicode/utf8"
)

const (
	// DefaultInterval 缺省时间窗。
	DefaultInterval = 400 * time.Millisecond
	// DefaultBytes 缺省缓冲窗（字节口径：缓冲达到即提前发布；CJK 场景
	// 200 字节 ≈ 66 汉字）。v0.12.0 改名 DefaultChars→DefaultBytes——
	// 实现一直是 len(s.text) 字节比较，命名与口径对齐（第八轮审计）。
	DefaultBytes = 200
	// DefaultCapBytes 单流发布总量帽（深度思考可极长，超帽静默停发）。
	DefaultCapBytes = 64 << 10
)

// Delta 一次待发布增量：Seq 单流内自 1 递增；Text 为合并分片（非累积快照——
// 前端按序追加，避免长文本 O(n²) 重发）。
type Delta struct {
	Seq  int
	Text string
}

// Stream 单条流的增量缓冲与节拍判定。
type Stream struct {
	interval  time.Duration
	bytes     int // 缓冲窗，字节口径（len(text) 比较）
	capBytes  int
	text      string
	seq       int
	flushedAt time.Time
	published int
	done      bool
	now       func() time.Time
}

// StreamOption Stream 可选项（runtime#110：AGENTS.md 明禁裸 Option——
// 多包 co-import 时与其它包的 Option/WithClock 冲突；仓内其余三包均为 XxxOption）。
type StreamOption func(*Stream)

// WithClock 注入时钟（测试用；缺省 time.Now）。nil 按「nil 参数=显式回退默认值」
// 口径处理（对齐 oekit/pkg cache.WithClock 的 pkg#356 修法）：不把 nil 存进实例
// ——nil 一路存到 Offer/Flush 的 s.now() 调用点，就成了构造期之外的远端
// SIGSEGV（runtime#113）。
func WithClock(now func() time.Time) StreamOption {
	return func(s *Stream) {
		if now == nil {
			return
		}
		s.now = now
	}
}

// NewStream 构造；interval<=0 / bytes<=0 / capBytes<=0 取对应缺省值。
func NewStream(interval time.Duration, bytes, capBytes int, opts ...StreamOption) *Stream {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if bytes <= 0 {
		bytes = DefaultBytes
	}
	if capBytes <= 0 {
		capBytes = DefaultCapBytes
	}
	s := &Stream{
		interval: interval,
		bytes:    bytes,
		capBytes: capBytes,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.now == nil {
		s.now = time.Now // 构造后兜底：任何 option 把时钟覆成 nil 都回退缺省（runtime#113）
	}
	return s
}

// Offer 接收一个增量分片：按节流窗口判定是否发布。done（权威已到）后的
// 迟到分片丢弃。
func (s *Stream) Offer(chunk string) (Delta, bool) {
	// 超帽与收口同为静默态：检测前置于累加——此前超帽后每个迟到分片仍无限
	// 滞留 s.text（深度思考尾部可达数百 KB 内存 + O(n²) 拷贝拖停 drain 循环；
	// 第八轮审计 C 级面）。Absorb 的权威快照会补全被帽截断的内容。
	if s.done || s.published >= s.capBytes {
		return Delta{}, false
	}
	s.text += chunk
	now := s.now()
	if s.flushedAt.IsZero() {
		s.flushedAt = now
	}
	if now.Sub(s.flushedAt) < s.interval && len(s.text) < s.bytes {
		return Delta{}, false
	}
	return s.Flush()
}

// Flush 立即发布缓冲残尾（钳制在剩余额度内，单次发布不越 capBytes 总量帽）；
// 空缓冲/已收口/超帽不动序号。
//
// runtime#124：旧实现只以「发布前 published >= capBytes」判帽，残尾按全量发布
// ——published=capBytes-100 时缓冲 200 字节会一次把总量顶到 capBytes+100，
// 单个巨大 chunk 更能一举越过整条帽。「超帽静默停发」只拦得出**下一次** Offer，
// 与包头/DefaultCapBytes/本注释三处「总量帽内」的承诺相悖。
// 截断按字节额度（与 capBytes/DefaultBytes 口径一致），但起点回退到 rune 边界：
// 帽不是非法 UTF-8 的许可证。
func (s *Stream) Flush() (Delta, bool) {
	if s.done || s.text == "" || s.published >= s.capBytes {
		return Delta{}, false
	}
	text := s.text
	if remaining := s.capBytes - s.published; len(text) > remaining {
		text = clampRunes(text, remaining)
		if text == "" {
			// 剩余额度容不下首个 rune：帽已实质到顶，清缓冲并按超帽停发，
			// 避免迟到的分片在 Flush 里滞留（同 Offer 前置判定的内存纪律）。
			s.text = ""
			s.published = s.capBytes
			return Delta{}, false
		}
	}
	pub := Delta{Seq: s.seq + 1, Text: text}
	s.seq, s.text, s.flushedAt = pub.Seq, "", s.now()
	s.published += len(pub.Text)
	return pub, true
}

// clampRunes 把 s 截到至多 maxBytes 字节，切口回退到 rune 起点（不劈开
// 多字节字符）。maxBytes<=0 或首 rune 本身就超额度时返回空串。
func clampRunes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Absorb 权威快照到达：增量缓冲作废并收口（消费方以权威整段替换增量累积）。
func (s *Stream) Absorb() {
	s.done = true
	s.text = ""
}

// Buffered 当前滞留缓冲的字节数（观测面：超帽/收口后应恒 0——超帽静默停发
// 的感知锚点，消费方据此区分「流进行中」与「已被帽截断、等权威快照」）。
func (s *Stream) Buffered() int { return len(s.text) }

// Sealed 是否已收口（Absorb 之后）。
func (s *Stream) Sealed() bool {
	return s.done
}
