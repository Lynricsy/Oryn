package link

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// 流类型（流头的 `t` 字段）。
const (
	StreamCtl    = "ctl"
	StreamAgent  = "agent"
	StreamEgress = "egress"
	StreamBlob   = "blob"
	StreamPortal = "portal"
	StreamFS     = "fs"
	StreamDesk   = "desk"
)

// MaxHeaderBytes 是流头行（含换行）的上限。
const MaxHeaderBytes = 64 << 10

// StreamHeader 是每条 yamux 流的第一行 `{"t":"<type>",…}\n`。
// 已知字段显式列出；后续里程碑新增的字段保留在 Raw 中。
type StreamHeader struct {
	T string `json:"t"`
	// ThreadID：agent / egress 流所属线程。
	ThreadID string `json:"threadId,omitempty"`
	// SHA256 / Size：blob 流的内容摘要与字节数。
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	// Port：portal 流要对接的 orb 内本机端口（M4）。
	Port int `json:"port,omitempty"`
	// Raw 是解码时读到的完整流头 JSON（编码时若非空则原样发送，忽略上面的字段）。
	Raw json.RawMessage `json:"-"`
}

func (h StreamHeader) encode() ([]byte, error) {
	var line []byte
	if len(h.Raw) > 0 {
		var probe struct {
			T string `json:"t"`
		}
		if err := json.Unmarshal(h.Raw, &probe); err != nil || probe.T == "" {
			return nil, errors.New("link: raw stream header must be an object with non-empty t")
		}
		line = bytes.TrimSpace(h.Raw)
	} else {
		if h.T == "" {
			return nil, errors.New("link: stream header requires t")
		}
		b, err := json.Marshal(h)
		if err != nil {
			return nil, err
		}
		line = b
	}
	if bytes.IndexByte(line, '\n') >= 0 || len(line)+1 > MaxHeaderBytes {
		return nil, errors.New("link: invalid stream header line")
	}
	return append(line, '\n'), nil
}

// EncodeHeader 返回流头行（含换行）。
func EncodeHeader(h StreamHeader) ([]byte, error) { return h.encode() }

// readHeader 从 br 读出一行流头；超过 MaxHeaderBytes 即失败。
func readHeader(br *bufio.Reader) (StreamHeader, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > MaxHeaderBytes {
			return StreamHeader{}, errors.New("header too long")
		}
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return StreamHeader{}, err
	}
	return DecodeHeader(buf)
}

// DecodeHeader 解析一行流头（可带结尾换行）。
func DecodeHeader(line []byte) (StreamHeader, error) {
	line = bytes.TrimSpace(line)
	var h StreamHeader
	if err := json.Unmarshal(line, &h); err != nil {
		return h, fmt.Errorf("decode: %w", err)
	}
	if h.T == "" {
		return h, errors.New("missing t")
	}
	h.Raw = append(json.RawMessage(nil), line...)
	return h, nil
}
