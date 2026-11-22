// Package headtail 是有上限的输出收集器：只留开头与结尾各一段，中间记省略的字节数。
// SSH 工具、运行前闸门（SSH 与本机沙箱）共用。
package headtail

import (
	"fmt"
	"strings"
	"sync"
)

// Buffer 并发安全地收集输出：只留头 Half 字节与尾 Half 字节，记下总长。零值不可用，用 New。
type Buffer struct {
	mu    sync.Mutex
	half  int
	head  []byte
	tail  []byte // 滑动窗口，至多 half 字节
	total int
}

// New 返回头尾各保留 half 字节的收集器。
func New(half int) *Buffer { return &Buffer{half: half} }

// Write 实现 io.Writer；从不返回错误。
func (w *Buffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += len(p)
	rest := p
	if room := w.half - len(w.head); room > 0 {
		n := min(room, len(rest))
		w.head = append(w.head, rest[:n]...)
		rest = rest[n:]
	}
	if len(rest) > 0 {
		w.tail = append(w.tail, rest...)
		if over := len(w.tail) - w.half; over > 0 {
			w.tail = append(w.tail[:0], w.tail[over:]...)
		}
	}
	return len(p), nil
}

// String 返回输出（非法 UTF-8 去掉）；超过两倍上限时在头尾之间插省略说明。
func (w *Buffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if omitted := w.total - len(w.head) - len(w.tail); omitted > 0 {
		return strings.ToValidUTF8(string(w.head), "") + fmt.Sprintf("\n…（省略 %d 字节）…\n", omitted) + strings.ToValidUTF8(string(w.tail), "")
	}
	return strings.ToValidUTF8(string(w.head)+string(w.tail), "")
}
