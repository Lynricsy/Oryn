package omprpc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ChunkFrame 对应 omp 的 RpcChunkFrame（rpc-types.ts）：
// `{type:"rpc_chunk", chunkId, index, count, byteLength, data}`，data 为标准 base64（带填充）。
type ChunkFrame struct {
	Type       string `json:"type"`
	ChunkID    string `json:"chunkId"`
	Index      int    `json:"index"`
	Count      int    `json:"count"`
	ByteLength int    `json:"byteLength"`
	Data       string `json:"data"`
}

// TypeChunk 是协议 v2 分块帧的 type。
const TypeChunk = "rpc_chunk"

// ChunkError 表示一个分块序列被丢弃（乱序、缺块、超限、数据损坏）。可恢复：读取可以继续。
type ChunkError struct {
	ChunkID string
	Reason  string
}

func (e *ChunkError) Error() string {
	return fmt.Sprintf("omprpc: rpc_chunk %q dropped: %s", e.ChunkID, e.Reason)
}

// Reassembler 重组 rpc_chunk 序列。omp 总是连续写出一个逻辑帧的全部分块，
// 因此任意时刻至多一个在途序列；不是线程安全的。
type Reassembler struct {
	pending *pendingChunks
	// dropped 记录最近一次被丢弃的 chunkId，其后续分块静默忽略。
	dropped string
}

type pendingChunks struct {
	chunkID    string
	count      int
	byteLength int
	nextIndex  int
	buf        []byte
}

// Push 处理一条已读出的物理行。
//
//   - 普通帧：原样返回。
//   - 分块帧：序列未完成时返回 (nil, nil)；完成时返回重组出的逻辑帧。
//   - 序列出错：丢弃该 chunkId 并返回 *ChunkError；若是被一条普通帧打断（即缺块），
//     返回值同时包含该普通帧与 *ChunkError，调用方应记录错误后照常处理该帧。
func (r *Reassembler) Push(line []byte) ([]byte, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return nil, fmt.Errorf("omprpc: invalid frame json: %w", err)
	}
	if probe.Type != TypeChunk {
		if !isJSONObject(line) {
			return nil, errors.New("omprpc: rpc frame must be an object")
		}
		if p := r.pending; p != nil {
			r.drop(p.chunkID)
			return line, &ChunkError{ChunkID: p.chunkID, Reason: fmt.Sprintf("sequence interrupted after %d/%d chunks", p.nextIndex, p.count)}
		}
		return line, nil
	}

	var c ChunkFrame
	if err := json.Unmarshal(line, &c); err != nil {
		return nil, r.fail(c.ChunkID, "invalid chunk metadata: "+err.Error())
	}
	if c.ChunkID == "" || len(c.ChunkID) > 128 || c.Index < 0 || c.Count < 1 || c.Index >= c.Count ||
		c.ByteLength < 0 || c.Count > MaxReassembledBytes/ChunkPayloadBytes+1 {
		return nil, r.fail(c.ChunkID, "invalid chunk metadata")
	}
	if c.ByteLength > MaxReassembledBytes {
		return nil, r.fail(c.ChunkID, fmt.Sprintf("declared byteLength %d exceeds %d", c.ByteLength, MaxReassembledBytes))
	}
	if r.pending == nil && c.ChunkID == r.dropped {
		return nil, nil
	}

	var interrupted error
	if p := r.pending; p != nil && p.chunkID != c.ChunkID {
		// 旧序列未完成就开始了新序列：旧序列缺块，丢弃后继续处理新分块。
		interrupted = &ChunkError{ChunkID: p.chunkID, Reason: fmt.Sprintf("sequence interrupted by chunk %q after %d/%d chunks", c.ChunkID, p.nextIndex, p.count)}
		r.drop(p.chunkID)
	}
	frame, err := r.pushChunk(&c)
	if interrupted != nil {
		return frame, errors.Join(interrupted, err)
	}
	return frame, err
}

func (r *Reassembler) pushChunk(c *ChunkFrame) ([]byte, error) {
	p := r.pending
	if p == nil {
		if c.Index != 0 {
			return nil, r.fail(c.ChunkID, fmt.Sprintf("sequence must start at index 0, got %d", c.Index))
		}
		p = &pendingChunks{chunkID: c.ChunkID, count: c.Count, byteLength: c.ByteLength}
		r.pending = p
	}
	if p.count != c.Count || p.byteLength != c.ByteLength {
		return nil, r.fail(c.ChunkID, "chunk metadata changed mid-sequence")
	}
	if c.Index != p.nextIndex {
		return nil, r.fail(c.ChunkID, fmt.Sprintf("out of order: expected index %d, got %d", p.nextIndex, c.Index))
	}
	data, err := base64.StdEncoding.Strict().DecodeString(c.Data)
	if err != nil || len(data) == 0 {
		return nil, r.fail(c.ChunkID, "invalid chunk data")
	}
	if len(p.buf)+len(data) > p.byteLength {
		return nil, r.fail(c.ChunkID, "sequence exceeds declared byteLength")
	}
	p.buf = append(p.buf, data...)
	p.nextIndex++
	if p.nextIndex < p.count {
		return nil, nil
	}
	r.pending = nil
	if len(p.buf) != p.byteLength {
		r.dropped = p.chunkID
		return nil, &ChunkError{ChunkID: p.chunkID, Reason: fmt.Sprintf("length mismatch: got %d, declared %d", len(p.buf), p.byteLength)}
	}
	if !json.Valid(p.buf) || !isJSONObject(p.buf) {
		r.dropped = p.chunkID
		return nil, &ChunkError{ChunkID: p.chunkID, Reason: "reassembled frame is not a json object"}
	}
	return p.buf, nil
}

// EncodeChunks 按 omp encodeChunkedRpcFrames 的方式把一个逻辑帧（不含换行的 JSON）
// 切成 rpc_chunk 物理行（每行不含换行）。仅在协议 v2 且帧超过 MaxFrameBytes 时使用。
func EncodeChunks(frame []byte, chunkID string) ([][]byte, error) {
	if len(frame) > MaxReassembledBytes {
		return nil, fmt.Errorf("omprpc: frame of %d bytes exceeds reassembly limit", len(frame))
	}
	count := (len(frame) + ChunkPayloadBytes - 1) / ChunkPayloadBytes
	lines := make([][]byte, 0, count)
	for i := range count {
		part := frame[i*ChunkPayloadBytes : min((i+1)*ChunkPayloadBytes, len(frame))]
		line, err := json.Marshal(ChunkFrame{
			Type: TypeChunk, ChunkID: chunkID, Index: i, Count: count, ByteLength: len(frame),
			Data: base64.StdEncoding.EncodeToString(part),
		})
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func (r *Reassembler) fail(chunkID, reason string) error {
	r.drop(chunkID)
	return &ChunkError{ChunkID: chunkID, Reason: reason}
}

func (r *Reassembler) drop(chunkID string) {
	r.pending = nil
	r.dropped = chunkID
}

func isJSONObject(b []byte) bool {
	b = bytes.TrimLeft(b, " \t\r\n")
	return len(b) > 0 && b[0] == '{'
}

// Decoder 组合 Reader 与 Reassembler，逐个产出逻辑帧。
type Decoder struct {
	r       *Reader
	ra      Reassembler
	carried []byte
}

// NewDecoder 创建 omp stdout 解码器（物理行上限 MaxFrameBytes）。
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: NewReader(r, MaxFrameBytes)}
}

// Next 返回下一个逻辑帧（一个 JSON 对象）。
//
// 可恢复错误（ErrLineTooLong、非法 JSON 行、*ChunkError）返回后可继续调用 Next；
// 用 IsRecoverable 判断。其它错误（含 io.EOF）表示流已结束。
func (d *Decoder) Next() ([]byte, error) {
	if f := d.carried; f != nil {
		d.carried = nil
		return f, nil
	}
	for {
		line, err := d.r.ReadLine()
		if err != nil {
			if errors.Is(err, ErrLineTooLong) {
				return nil, &FrameError{Err: err}
			}
			return nil, err
		}
		frame, err := d.ra.Push(line)
		if err != nil {
			if frame != nil {
				d.carried = frame
			}
			return nil, &FrameError{Err: err}
		}
		if frame != nil {
			return frame, nil
		}
	}
}

// FrameError 包装可恢复的单帧错误。
type FrameError struct{ Err error }

func (e *FrameError) Error() string { return e.Err.Error() }
func (e *FrameError) Unwrap() error { return e.Err }

// IsRecoverable 报告 err 是否只影响单个帧、流仍可继续读取。
func IsRecoverable(err error) bool {
	var fe *FrameError
	return errors.As(err, &fe)
}
