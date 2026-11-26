package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// sandbox.call（M30 步骤 3.3，docs/plan-m30.md C5）：orb 内 `oryn sandbox …` 与 sandbox_* 工具经 orbd.sock 发来一次
// 沙箱调用，orbd 在当前 link 上打开一条 `sandbox` 流转给控制面：写一行请求 `{"op","params"}`，读回零或多行输出
// `{"s":"o"|"e","d"}` 与最后一行 `{"result"}` / `{"error"}`。调用方断开时关流，控制面据此取消这次操作。

// sandboxLineMax 是 sandbox 流上一行（请求或应答）的上限。
const sandboxLineMax = 1 << 20

type sandboxStreamLine struct {
	S      string          `json:"s"`
	D      string          `json:"d"`
	Result json.RawMessage `json:"result"`
	Error  *orbsock.Error  `json:"error"`
}

func (r *Runtime) sandboxCall(ctx context.Context, params json.RawMessage, out *orbsock.Responder) (any, error) {
	var p orbsock.SandboxCallParams
	if err := decodeSock(params, &p); err != nil {
		return nil, err
	}
	if !validThreadID(p.ThreadID) || strings.TrimSpace(p.Op) == "" {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "threadId and op are required")
	}
	if len(p.Params) == 0 || string(p.Params) == "null" {
		p.Params = json.RawMessage("{}")
	}
	req, err := json.Marshal(struct {
		Op     string          `json:"op"`
		Params json.RawMessage `json:"params"`
	}{p.Op, p.Params})
	if err != nil {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "%v", err)
	}
	if len(req)+1 > sandboxLineMax {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "request exceeds %d bytes", sandboxLineMax)
	}
	c := r.current()
	if c == nil {
		return nil, orbsock.Errorf(orbsock.CodeOffline, "orbd is not connected to the control plane")
	}
	st, err := c.sess.Open(ctx, link.StreamHeader{T: link.StreamSandbox, ThreadID: p.ThreadID})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, orbsock.Errorf(orbsock.CodeOffline, "open sandbox stream: %v", err)
	}
	defer st.Close()
	// 调用方断开：关流（控制面读到 EOF 即放弃这次操作）并解除阻塞的读。
	stop := context.AfterFunc(ctx, func() {
		_ = st.Close()
		_ = st.SetReadDeadline(time.Now())
	})
	defer stop()
	if _, err := st.Write(append(req, '\n')); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, orbsock.Errorf(orbsock.CodeOffline, "write sandbox request: %v", err)
	}
	for {
		line, err := st.ReadLine(sandboxLineMax)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, link.ErrLineTooLong) {
				return nil, orbsock.Errorf(orbsock.CodeInternal, "sandbox reply line exceeds %d bytes", sandboxLineMax)
			}
			return nil, orbsock.Errorf(orbsock.CodeOffline, "the link closed before the sandbox call finished: %v", err)
		}
		var m sandboxStreamLine
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, orbsock.Errorf(orbsock.CodeInternal, "bad sandbox reply line: %v", err)
		}
		switch {
		case m.Error != nil:
			return nil, orbsock.Errorf(m.Error.Code, "%s", m.Error.Message)
		case m.Result != nil:
			return m.Result, nil
		case m.S == "o":
			out.Log(orbsock.StreamStdout, m.D)
		case m.S == "e":
			out.Log(orbsock.StreamStderr, m.D)
		}
	}
}
