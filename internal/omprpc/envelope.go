package omprpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Envelope 是 Go 侧唯一需要解析的帧字段；其余内容保持在 Raw 中原样转发。
//
// 字段来自 omp rpc-types.ts：命令 `{id?, type, ...}`；应答
// `{id?, type:"response", command, success, data?, error?, code?}`。
// 注意 `error` 在应答里是字符串，但在 `prompt_result` 等帧里是对象，因此保留原始 JSON。
type Envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Command string          `json:"command,omitempty"`
	Success *bool           `json:"success,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
	Code    string          `json:"code,omitempty"`
	// Raw 是完整的原始帧。
	Raw json.RawMessage `json:"-"`
}

// 常用帧类型。
const (
	TypeReady    = "ready"
	TypeResponse = "response"
)

// ParseEnvelope 解析帧的信封字段；frame 必须是 JSON 对象。
func ParseEnvelope(frame []byte) (Envelope, error) {
	var e Envelope
	if !isJSONObject(frame) {
		return e, errors.New("omprpc: frame must be a json object")
	}
	if err := json.Unmarshal(frame, &e); err != nil {
		return e, fmt.Errorf("omprpc: parse envelope: %w", err)
	}
	e.Raw = frame
	return e, nil
}

// IsResponse 报告是否为命令应答帧。
func (e *Envelope) IsResponse() bool { return e.Type == TypeResponse }

// OK 报告应答是否成功。
func (e *Envelope) OK() bool { return e.Success != nil && *e.Success }

// ErrorText 返回 `error` 字段的可读文本：字符串原样返回，对象取其 `message`。
func (e *Envelope) ErrorText() string {
	if len(e.Error) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(e.Error, &s) == nil {
		return s
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(e.Error, &obj) == nil && obj.Message != "" {
		return obj.Message
	}
	return string(e.Error)
}

// SetID 返回把顶层 `id` 字段设为 id 后的帧。没有 id 时在对象开头插入、其余字节不变；
// 已有 id 时覆盖并重新编码（键顺序可能变化）。
func SetID(frame []byte, id string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(frame, &m); err != nil {
		return nil, fmt.Errorf("omprpc: set id: %w", err)
	}
	idJSON, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	if _, ok := m["id"]; ok {
		m["id"] = idJSON
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(m); err != nil {
			return nil, err
		}
		return bytes.TrimRight(buf.Bytes(), "\n"), nil
	}
	// 没有 id 时直接在对象开头插入，避免重新编码整个帧。
	trimmed := bytes.TrimLeft(frame, " \t\r\n")
	rest := bytes.TrimLeft(trimmed[1:], " \t\r\n")
	out := make([]byte, 0, len(frame)+len(idJSON)+8)
	out = append(out, `{"id":`...)
	out = append(out, idJSON...)
	if len(rest) > 0 && rest[0] != '}' {
		out = append(out, ',')
	}
	out = append(out, rest...)
	return out, nil
}
