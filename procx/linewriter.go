package procx

import "bytes"

// maxLineLen lineWriter 单行缓冲上限：超限丢弃该行不再回调。partial 若无上限，
// 单行无换行洪泛会绕过 maxChildStdout 限容（claude stream-json 每个事件就是一行，
// 一个含超大 diff 的 result 事件即可冲出数百 MB 峰值）。
const maxLineLen = 1 << 20 // 1MB

// lineWriter 按行拆分 stdout，回调后仍写入 buf 保留全文。
// 单行超过 maxLineLen 时丢弃该行（overflow 置位后不回调），扫到该行行尾后
// **从行尾之后继续按行处理**，buf 照常限容写入保留全文供兜底。
type lineWriter struct {
	buf      *cappedBuffer
	onLine   func(string)
	partial  []byte
	overflow bool
}

func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	_, _ = w.buf.Write(p)
	for len(p) > 0 {
		if w.overflow {
			// 仍在超限行内：不累积，只找它的行尾。行尾之后同一 chunk 里
			// 的字节必须继续按行处理——用 LastIndexByte 会一路跳到**最后一个**
			// 换行，把超限行与本 chunk 末尾之间的完整行整段吞掉且不回调
			// （agent#30：单行 >1MB 后同 chunk 后续事件行全部丢失，消费方
			// 只看到事件流断片，无任何信号）。
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				return n, nil
			}
			w.overflow = false
			w.partial = w.partial[:0]
			p = p[i+1:]
			continue
		}
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(w.partial)+len(p) > maxLineLen {
				w.overflow = true // 整行丢弃（含已累积部分），防 partial 无限放大
				w.partial = w.partial[:0]
				return n, nil
			}
			w.partial = append(w.partial, p...)
			return n, nil
		}
		if len(w.partial)+i > maxLineLen {
			// 该行自身就是超限行（行尾落在本 chunk 内）：与跨 chunk 的超限行
			// 同等处置——整行丢弃，跳过它的行尾。
			w.partial = w.partial[:0]
			p = p[i+1:]
			continue
		}
		line := string(append(w.partial, p[:i]...))
		w.partial = w.partial[:0]
		p = p[i+1:]
		if line != "" {
			w.onLine(line)
		}
	}
	return n, nil
}
