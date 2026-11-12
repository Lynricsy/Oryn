// Package orbsock 是 orbd 本地控制口（M2.5）：`$HOME/.oryn/orbd.sock` 上的 JSON 行协议，
// orbd（服务端）与 orb 内的 `oryn orb setup …`（客户端）共用。
//
// 每个连接一个请求：客户端写一行 `{"method": "...", "params": {...}}`，服务端回若干行：
// 零或多条 `{"type":"log","stream":"stdout|stderr|system","data":"..."}`，
// 最后恰好一条 `{"type":"result","result":...}` 或 `{"type":"error","code":"...","message":"..."}`。
// 客户端在收到最后一条之前不得关闭连接：服务端把连接关闭视为调用方放弃（会取消正在运行的重跑）。
package orbsock

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 方法名。
const (
	MethodSetupRerun        = "setup.rerun"
	MethodSetupStatus       = "setup.status"
	MethodProjectScriptsGet = "project.scripts.get"
	MethodProjectScriptsSet = "project.scripts.set"
)

// 消息类型。
const (
	TypeLog    = "log"
	TypeResult = "result"
	TypeError  = "error"
)

// 日志流。
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
	StreamSystem = "system"
)

// 错误码（控制面经 ctl 返回的错误码如 `no_project` 原样透传）。
const (
	CodeBusy           = "busy"      // 已有 setup / resume 在运行
	CodeNotReady       = "not_ready" // orbd 还没收到过 setup.run / resume.run
	CodeOffline        = "offline"   // orbd 当前未连上控制面
	CodeNoProject      = "no_project"
	CodeInvalidParams  = "invalid_params"
	CodeMethodNotFound = "method_not_found"
	CodeInternal       = "internal"
)

// maxRequestBytes 是请求行上限（项目脚本随 project.scripts.set 传入）。
const maxRequestBytes = 4 << 20

// Path 返回 $HOME 下的控制口路径。
func Path(home string) string { return filepath.Join(home, ".oryn", "orbd.sock") }

// Request 是客户端发出的唯一一行。
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Message 是服务端回送的一行。
type Message struct {
	Type    string          `json:"type"`
	Stream  string          `json:"stream,omitempty"`
	Data    string          `json:"data,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
}

// Error 是控制口错误，同时实现 error。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf 构造一个控制口错误。
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// RerunParams 是 setup.rerun 的参数。
type RerunParams struct {
	Resume bool `json:"resume,omitempty"`
}

// Status 是 setup.status 的结果，也是 `$HOME/.oryn/setup-status.json` 的内容。
type Status struct {
	Setup  *apigen.LinkCtlSetupResultParams `json:"setup"`
	Resume *apigen.LinkCtlSetupResultParams `json:"resume"`
}

// ---- 服务端 ----

// Responder 向调用方流式写日志行；并发安全，写失败（调用方已断开）后静默丢弃。
type Responder struct {
	mu     sync.Mutex
	enc    *json.Encoder
	broken bool
}

// Log 写一条日志消息。
func (r *Responder) Log(stream, data string) {
	r.write(Message{Type: TypeLog, Stream: stream, Data: data})
}

func (r *Responder) write(m Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.broken {
		return
	}
	if err := r.enc.Encode(m); err != nil {
		r.broken = true
	}
}

// Handler 处理一个请求：返回 (result, nil) 时写 result 消息；返回 *Error 时原样写错误，
// 其它错误映射为 code=internal。ctx 在调用方断开连接时取消。
type Handler func(ctx context.Context, method string, params json.RawMessage, out *Responder) (any, error)

// Listen 在 path 上监听 unix socket（权限 0600）。残留的 socket 文件若无人监听则删除；
// 已有活的监听者时报错。
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("orbsock: %s is already being served", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve 接受连接直到 ln 关闭或 ctx 结束；每个连接一个 goroutine。
func Serve(ctx context.Context, ln net.Listener, h Handler) {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				slog.Warn("orbsock: accept failed", "err", err)
			}
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveConn(ctx, c, h)
		}()
	}
}

func serveConn(ctx context.Context, c net.Conn, h Handler) {
	defer c.Close()
	out := &Responder{enc: json.NewEncoder(c)}
	br := bufio.NewReaderSize(c, 64<<10)
	line, err := readLine(br, maxRequestBytes)
	if err != nil {
		out.write(Message{Type: TypeError, Code: CodeInvalidParams, Message: err.Error()})
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil || req.Method == "" {
		out.write(Message{Type: TypeError, Code: CodeInvalidParams, Message: "request must be one JSON line with a method"})
		return
	}
	// 调用方断开（读到 EOF）即取消请求。
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, br)
		cancel()
	}()
	res, err := h(cctx, req.Method, req.Params, out)
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			e = &Error{Code: CodeInternal, Message: err.Error()}
		}
		out.write(Message{Type: TypeError, Code: e.Code, Message: e.Message})
		return
	}
	b, err := json.Marshal(res)
	if err != nil {
		out.write(Message{Type: TypeError, Code: CodeInternal, Message: err.Error()})
		return
	}
	out.write(Message{Type: TypeResult, Result: b})
}

func readLine(br *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > limit {
			return nil, fmt.Errorf("request exceeds %d bytes", limit)
		}
		switch {
		case err == nil:
			return buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

// ---- 客户端 ----

// Call 发出一个请求，把日志消息交给 onLog（可为 nil），返回 result 的原始 JSON。
// 服务端错误以 *Error 返回。
func Call(ctx context.Context, path, method string, params any, onLog func(stream, data string)) (json.RawMessage, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	req := Request{Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		req.Params = b
	}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(c)
	for {
		var m Message
		if err := dec.Decode(&m); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return nil, errors.New("orbsock: connection closed before result")
			}
			return nil, err
		}
		switch m.Type {
		case TypeLog:
			if onLog != nil {
				onLog(m.Stream, m.Data)
			}
		case TypeResult:
			return m.Result, nil
		case TypeError:
			return nil, &Error{Code: m.Code, Message: m.Message}
		default:
			return nil, fmt.Errorf("orbsock: unexpected message type %q", m.Type)
		}
	}
}
