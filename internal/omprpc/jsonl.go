// Package omprpc 实现 omp `--mode rpc` / `rpc-ui` 的传输层：JSONL 分帧、
// 协议 v2 的 `rpc_chunk` 重组以及最小的信封解析。
//
// 权威实现见 oryn-agent 仓库 `packages/coding-agent/src/modes/rpc/rpc-frame.ts`
// （MAX_RPC_FRAME_BYTES / MAX_RPC_REASSEMBLED_BYTES / RpcFrameDecoder）。
package omprpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	// MaxFrameBytes 是单条物理 JSONL 行的上限（含换行符），与 omp 的 MAX_RPC_FRAME_BYTES 一致。
	MaxFrameBytes = 1 << 20
	// MaxReassembledBytes 是协议 v2 重组后单个逻辑帧的上限，与 omp 的 MAX_RPC_REASSEMBLED_BYTES 一致。
	MaxReassembledBytes = 64 << 20
	// ChunkPayloadBytes 是 omp 每个 rpc_chunk 携带的原始字节数上限（RPC_CHUNK_PAYLOAD_BYTES）。
	ChunkPayloadBytes = 256 << 10
)

// ErrLineTooLong 表示某一物理行超过了上限；该行剩余部分已被丢弃，读取可以继续。
var ErrLineTooLong = errors.New("omprpc: jsonl line exceeds limit")

// Reader 按行读取 JSONL。空行（含只有空白的行）被跳过，行尾的 `\r` 被去掉。
type Reader struct {
	br  *bufio.Reader
	max int
}

// NewReader 创建行读取器；maxLine 为单行上限（含换行），<=0 时使用 MaxFrameBytes。
func NewReader(r io.Reader, maxLine int) *Reader {
	if maxLine <= 0 {
		maxLine = MaxFrameBytes
	}
	return &Reader{br: bufio.NewReaderSize(r, 64<<10), max: maxLine}
}

// ReadLine 返回下一条非空物理行（不含换行符，调用方拥有返回的切片）。
//
// 超过上限时丢弃该行剩余部分并返回 ErrLineTooLong，之后可继续调用。
// 流在行中途结束时返回 io.ErrUnexpectedEOF，正常结束返回 io.EOF。
func (r *Reader) ReadLine() ([]byte, error) {
	for {
		line, err := r.readPhysical()
		if err != nil {
			return nil, err
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		return line, nil
	}
}

func (r *Reader) readPhysical() ([]byte, error) {
	var buf []byte
	overflow := false
	for {
		chunk, err := r.br.ReadSlice('\n')
		if !overflow {
			// 上限包含换行符：内容长度 + 1 不得超过 max。
			n := len(buf) + len(chunk)
			if err == nil {
				n-- // chunk 以 '\n' 结尾，内容部分少 1
			}
			if n+1 > r.max {
				overflow = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		switch {
		case err == nil:
			if overflow {
				return nil, ErrLineTooLong
			}
			return buf[:len(buf)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if overflow || len(buf) > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, io.EOF
		default:
			return nil, err
		}
	}
}

// Writer 把帧编码为一行 JSON 写出；并发安全，每帧一次 Write。
type Writer struct {
	mu  sync.Mutex
	w   io.Writer
	max int
}

// NewWriter 创建帧写入器；maxLine 为单行上限（含换行），<=0 表示不限制
// （omp 的 stdin 没有行长上限，只有 stdout 有）。
func NewWriter(w io.Writer, maxLine int) *Writer {
	return &Writer{w: w, max: maxLine}
}

// WriteFrame 以 JSON 编码 v（不转义 HTML 字符）并写出一行。
func (w *Writer) WriteFrame(v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("omprpc: encode frame: %w", err)
	}
	return w.writeLine(buf.Bytes())
}

// WriteRaw 写出一条已编码的 JSON 行（不含换行符）；行内不得包含换行。
func (w *Writer) WriteRaw(line []byte) error {
	if bytes.IndexByte(line, '\n') >= 0 {
		return errors.New("omprpc: raw frame contains newline")
	}
	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	return w.writeLine(buf)
}

func (w *Writer) writeLine(line []byte) error {
	if w.max > 0 && len(line) > w.max {
		return ErrLineTooLong
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.w.Write(line)
	return err
}
