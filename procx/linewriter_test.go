package procx

import (
	"strings"
	"testing"
)

func newLineWriter() (*lineWriter, *[]string) {
	lines := &[]string{}
	return &lineWriter{buf: &cappedBuffer{}, onLine: func(l string) { *lines = append(*lines, l) }}, lines
}

// TestLineWriterResyncKeepsTrailingLines 超限行结束后，同一 chunk 里跟着的完整行
// 必须照常回调（agent#30）。
//
// 失效形态：重同步取 `bytes.LastIndexByte(p, '\n')` 跳到**最后一个**换行，于是
// 超限行行尾与本 chunk 末尾之间的所有行被整段丢弃——正是「单行 >1MB（含超大
// diff 的 stream-json result 事件）后同一 read chunk 内的后续事件行全部丢失，
// 消费方只看到事件流断片」。
func TestLineWriterResyncKeepsTrailingLines(t *testing.T) {
	lw, lines := newLineWriter()

	if _, err := lw.Write([]byte(strings.Repeat("A", 3<<20))); err != nil { // 3MB 无换行
		t.Fatal(err)
	}
	if !lw.overflow {
		t.Fatal("超限后 overflow 应置位")
	}

	// 超限行的行尾 + 同一 chunk 内两条完整事件行。
	chunk := "tail\n" + `{"type":"step","n":1}` + "\n" + `{"type":"result","result":"ok"}` + "\n"
	if _, err := lw.Write([]byte(chunk)); err != nil {
		t.Fatal(err)
	}
	if lw.overflow {
		t.Fatal("行尾后应解除 overflow")
	}
	want := []string{`{"type":"step","n":1}`, `{"type":"result","result":"ok"}`}
	if len(*lines) != len(want) {
		t.Fatalf("超限行行尾之后的完整行被吞了: got %v want %v", *lines, want)
	}
	for i := range want {
		if (*lines)[i] != want[i] {
			t.Fatalf("行 %d = %q, want %q", i, (*lines)[i], want[i])
		}
	}
}

// TestLineWriterMultiLineChunkBeyondCap 一个 chunk 含多行、总字节超上限时，
// 这些行本身都没超限，必须全部回调（成对保护：不能用「整 chunk 超限即丢弃」
// 换取上面的修复）。
func TestLineWriterMultiLineChunkBeyondCap(t *testing.T) {
	lw, lines := newLineWriter()

	var sb strings.Builder
	const per = 4096
	for i := 0; i < 400; i++ { // 400 × 4KB ≈ 1.6MB，单行远小于上限
		sb.WriteString(strings.Repeat("x", per))
		sb.WriteString("\n")
	}
	if _, err := lw.Write([]byte(sb.String())); err != nil {
		t.Fatal(err)
	}
	if lw.overflow {
		t.Fatal("逐行都在上限内，不应进 overflow")
	}
	if len(*lines) != 400 {
		t.Fatalf("应回调 400 行，实得 %d", len(*lines))
	}
}

// TestLineWriterChunkInternalOverflow 超限行的行尾恰好落在本 chunk 内
// （跨 chunk 累积超限后一次收尾）也必须丢该行、留后续行。
func TestLineWriterChunkInternalOverflow(t *testing.T) {
	lw, lines := newLineWriter()

	if _, err := lw.Write([]byte(strings.Repeat("A", 3<<20))); err != nil {
		t.Fatal(err)
	}
	// 同一次写入里：洪泛行收尾 + 完整行，随后再来一次写入验证状态已复原。
	if _, err := lw.Write([]byte("tail\nok-1\nok-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := lw.Write([]byte("\nok-3\n")); err != nil {
		t.Fatal(err)
	}
	want := []string{"ok-1", "ok-2", "ok-3"}
	if len(*lines) != len(want) {
		t.Fatalf("got %v want %v", *lines, want)
	}
	for i := range want {
		if (*lines)[i] != want[i] {
			t.Fatalf("行 %d = %q, want %q", i, (*lines)[i], want[i])
		}
	}
}

// TestLineWriterSingleOversizedLineInChunk 单行自身超限（行尾在本 chunk 内）
// 也丢该行而不是把整 chunk 之后的处理搞乱。
func TestLineWriterSingleOversizedLineInChunk(t *testing.T) {
	lw, lines := newLineWriter()

	chunk := strings.Repeat("A", maxLineLen+1024) + "\n" + "after\n"
	if _, err := lw.Write([]byte(chunk)); err != nil {
		t.Fatal(err)
	}
	if lw.overflow {
		t.Fatal("本 chunk 内已扫到行尾，不应保持 overflow")
	}
	if len(*lines) != 1 || (*lines)[0] != "after" {
		t.Fatalf("超限行应丢弃、其后行应保留: %v", *lines)
	}
}

// TestLineWriterPrefixSplitAcrossChunks 行被 chunk 边界切开仍拼回一行
// （回归：循环重写不得把 partial 弄丢）。
func TestLineWriterPrefixSplitAcrossChunks(t *testing.T) {
	lw, lines := newLineWriter()

	for _, part := range []string{`{"type":"res`, `ult","result":`, `"ok"}` + "\n"} {
		if _, err := lw.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if len(*lines) != 1 || (*lines)[0] != `{"type":"result","result":"ok"}` {
		t.Fatalf("跨 chunk 行未拼回: %v", *lines)
	}
}

// TestLineWriterEmptyLinesSkipped 空行不回调（既有语义，保持）。
func TestLineWriterEmptyLinesSkipped(t *testing.T) {
	lw, lines := newLineWriter()
	if _, err := lw.Write([]byte("\n\n a \n\n")); err != nil {
		t.Fatal(err)
	}
	if len(*lines) != 1 || (*lines)[0] != " a " {
		t.Fatalf("空行应跳过: %v", *lines)
	}
}
