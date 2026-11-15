package pacer

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestStream(interval time.Duration, chars, capBytes int) (*Stream, *fakeClock) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	return NewStream(interval, chars, capBytes, WithClock(clk.Now)), clk
}

func TestOfferHoldsUntilWindow(t *testing.T) {
	s, clk := newTestStream(400*time.Millisecond, 200, 1<<20)
	if _, ok := s.Offer("片段一"); ok {
		t.Fatal("时间窗与字符窗均未达到，不应发布")
	}
	clk.advance(401 * time.Millisecond)
	d, ok := s.Offer("片段二")
	if !ok || d.Seq != 1 {
		t.Fatalf("越过时间窗应发布，得到 ok=%v delta=%+v", ok, d)
	}
}

func TestOfferFlushesAtCharWindow(t *testing.T) {
	s, clk := newTestStream(400*time.Millisecond, 10, 1<<20)
	// 缓冲窗按字节计：10 字节恰好达窗
	d, ok := s.Offer("aaaaaaaaaa")
	if !ok || d.Seq != 1 || len(d.Text) != 10 {
		t.Fatalf("达到字符窗应立即发布，得到 ok=%v delta=%+v", ok, d)
	}
	clk.advance(50 * time.Millisecond)
	if _, ok := s.Offer("短"); ok {
		t.Fatal("发布后时间窗重置，短片不应发布")
	}
}

func TestSeqMonotonicAndTextMerged(t *testing.T) {
	s, clk := newTestStream(400*time.Millisecond, 200, 1<<20)
	s.Offer("a")
	s.Offer("b")
	clk.advance(401 * time.Millisecond)
	d1, ok := s.Offer("c")
	if !ok || d1.Text != "abc" || d1.Seq != 1 {
		t.Fatalf("窗口内分片应合并，得到 %+v ok=%v", d1, ok)
	}
	clk.advance(401 * time.Millisecond)
	d2, ok := s.Offer("d")
	if !ok || d2.Seq != 2 {
		t.Fatalf("seq 应单调递增，得到 %+v", d2)
	}
}

func TestAbsorbDropsLateChunks(t *testing.T) {
	s, _ := newTestStream(400*time.Millisecond, 200, 1<<20)
	s.Offer("x")
	s.Absorb()
	if !s.Sealed() {
		t.Fatal("Absorb 后应 Sealed")
	}
	if _, ok := s.Offer("迟到分片"); ok {
		t.Fatal("收口后迟到分片应丢弃")
	}
	if _, ok := s.Flush(); ok {
		t.Fatal("收口后 Flush 不应产出")
	}
}

func TestCapBytesSilences(t *testing.T) {
	s, _ := newTestStream(0, 5, 12) // capBytes=12
	if _, ok := s.Offer("aaaaaaaaaa"); !ok {
		t.Fatal("第一片达到字符窗应发布")
	}
	// published=10 < 12，第二片发布时按剩余额度 2 字节钳制（runtime#124：
	// 不得整片发布把 published 顶到 20 越过 12 的总量帽）。
	d, ok := s.Offer("bbbbbbbbbb")
	if !ok {
		t.Fatal("剩余额度内应发布（钳制后的残片）")
	}
	if d.Text != "bb" || s.published != 12 {
		t.Fatalf("第二片应被钳制到剩余 2 字节，得到 delta=%+v published=%d", d, s.published)
	}
	if _, ok := s.Offer("cccc"); ok {
		t.Fatal("超帽后应静默停发")
	}
	if _, ok := s.Flush(); ok {
		t.Fatal("超帽后 Flush 不应产出")
	}
}

func TestDefaultsApplied(t *testing.T) {
	s := NewStream(0, 0, 0)
	if s.interval != DefaultInterval || s.bytes != DefaultBytes || s.capBytes != DefaultCapBytes {
		t.Fatalf("缺省值未生效：%v %d %d", s.interval, s.bytes, s.capBytes)
	}
}

// TestFlushDrainsResidue 回归：普通残尾（远小于剩余额度）照常全量落地。
func TestFlushDrainsResidue(t *testing.T) {
	s, _ := newTestStream(time.Hour, 1000, 1<<20) // 长窗：永不自动发布
	if _, ok := s.Offer("残尾"); ok {
		t.Fatal("长窗下不应自动发布")
	}
	d, ok := s.Flush()
	if !ok || d.Text != "残尾" || d.Seq != 1 {
		t.Fatalf("Flush 应落地残尾，得到 %+v ok=%v", d, ok)
	}
	if _, ok := s.Flush(); ok {
		t.Fatal("空缓冲 Flush 不应产出")
	}
}

// TestFlushClampsResidueToRemainingCap 回归（runtime#124）：Flush 注释承诺
// 「帽内」，但旧实现只看发布**前** published——published=8、cap=10 时缓冲
// 8 字节会整片发布到 16。修复后第二次 Flush 只能拿走剩余 2 字节。
func TestFlushClampsResidueToRemainingCap(t *testing.T) {
	s, _ := newTestStream(time.Hour, 1000, 10)
	if _, ok := s.Offer("12345678"); ok {
		t.Fatal("长窗下不应自动发布")
	}
	d, ok := s.Flush()
	if !ok || d.Text != "12345678" || s.published != 8 {
		t.Fatalf("首批应全量发布，得到 delta=%+v ok=%v published=%d", d, ok, s.published)
	}
	if _, ok := s.Offer("abcdefgh"); ok {
		t.Fatal("长窗下不应自动发布")
	}
	d, ok = s.Flush()
	if !ok || d.Text != "ab" {
		t.Fatalf("残尾应被钳制到剩余 2 字节，得到 delta=%+v ok=%v", d, ok)
	}
	if s.published != 10 {
		t.Fatalf("published 必须恰好停在 capBytes，实得 %d", s.published)
	}
	if _, ok := s.Offer("zz"); ok {
		t.Fatal("帽满后 Offer 应静默停发")
	}
	if _, ok := s.Flush(); ok {
		t.Fatal("帽满后 Flush 不应产出")
	}
}

// TestFlushClampKeepsRuneBoundary 回归（runtime#124 与 #123 同族纪律）：
// 字节额度切点落在多字节字符中间时回退到 rune 起点——帽不是非法 UTF-8 的
// 许可证；剩余额度容不下首个完整 rune 时宁可不发，也不发半个字符。
func TestFlushClampKeepsRuneBoundary(t *testing.T) {
	s, _ := newTestStream(time.Hour, 1000, 8)
	if _, ok := s.Offer("abcdef中"); ok { // 9 字节，其中「中」占 6..8
		t.Fatal("长窗下不应自动发布")
	}
	d, ok := s.Flush()
	if !ok {
		t.Fatal("剩余额度内应有产出")
	}
	if d.Text != "abcdef" {
		t.Fatalf("8 字节切点落在「中」中间，应回退到 6 字节，得到 %q", d.Text)
	}
	if !utf8.ValidString(d.Text) {
		t.Fatalf("发布文本不得含非法 UTF-8: %q", d.Text)
	}
	if s.published != 6 {
		t.Fatalf("published 应计实际发布字节 6，实得 %d", s.published)
	}
	// 剩余 2 字节容不下 3 字节的「中」：停发且清缓冲，不留半个字符。
	if _, ok := s.Offer("中"); ok {
		t.Fatal("长窗下不应自动发布")
	}
	if _, ok := s.Flush(); ok {
		t.Fatal("剩余额度容不下完整 rune 时不得发布")
	}
	if s.published != 8 || s.Buffered() != 0 {
		t.Fatalf("应记满帽并清空缓冲，得到 published=%d buffered=%d", s.published, s.Buffered())
	}
}

// TestOfferSingleHugeChunkCannotExceedCap 单个巨大分片直通 Offer→Flush 的
// 形态（深度思考单分片 > 缺省 64KB）：必须被钳制，而不是一次顶穿总量帽。
func TestOfferSingleHugeChunkCannotExceedCap(t *testing.T) {
	s, _ := newTestStream(0, 5, 16)
	d, ok := s.Offer(strings.Repeat("x", 100))
	if !ok {
		t.Fatal("达到缓冲窗应发布")
	}
	if len(d.Text) != 16 || s.published != 16 {
		t.Fatalf("单次发布应钳制到 capBytes=16，得到 len=%d published=%d", len(d.Text), s.published)
	}
	if _, ok := s.Offer("more"); ok {
		t.Fatal("帽满后应静默停发")
	}
}

// TestCapBytesNoBufferRetention 回归（第八轮审计）：超帽后迟到分片不得滞留
// 缓冲——此前每个分片仍 s.text += chunk（深度思考尾部可达数百 KB 内存 +
// O(n²) 拷贝），Flush 因超帽恒不发布也不清理。
func TestCapBytesNoBufferRetention(t *testing.T) {
	s := NewStream(time.Hour, 1, 16) // 极小帽：首个发布即触顶
	s.Offer("0123456789abcdefgh")    // 18 字节 > 16 帽
	if _, ok := s.Offer("尾部滞留检查"); ok {
		t.Fatal("超帽后不应再发布")
	}
	if s.Buffered() != 0 {
		t.Fatalf("超帽后缓冲应零滞留, got %d", s.Buffered())
	}
}

// TestWithClockNilFallsBackToDefault 回归（runtime#113）：WithClock(nil) 不得把
// 构造期已就位的缺省时钟覆盖成 nil——此前 nil 一路存进实例，首次 Offer 即以
// nil 函数调用 SIGSEGV，而 godoc 承诺的是「缺省 time.Now」。口径对齐
// oekit/pkg#356：nil 参数=显式回退默认值。
func TestWithClockNilFallsBackToDefault(t *testing.T) {
	// 构造期守卫：nil 不得落进实例（守卫失效时这里直接查出，而非等 Offer 炸）
	s := NewStream(time.Hour, 1000, 1<<20, WithClock(nil))
	if s.now == nil {
		t.Fatal("WithClock(nil) 后实例时钟不得为 nil（应回退缺省 time.Now）")
	}
	// 行为护栏：WithClock(nil) 的 Offer/Flush 必须与不传 option 完全一致。
	// 长窗下 Offer 只缓冲、Flush 落残尾，两路判定与时钟取值无关，可逐字比对。
	def := NewStream(time.Hour, 1000, 1<<20)
	dDef, okDef := def.Offer("残尾")
	dNil, okNil := s.Offer("残尾")
	if okDef != okNil || dDef != dNil {
		t.Fatalf("WithClock(nil) 与缺省构造行为不一致：默认 ok=%v delta=%+v，nil ok=%v delta=%+v",
			okDef, dDef, okNil, dNil)
	}
	if d, ok := s.Flush(); !ok || d.Text != "残尾" || d.Seq != 1 {
		t.Fatalf("nil 回退缺省后 Flush 应正常落地，得到 ok=%v delta=%+v", ok, d)
	}
}
