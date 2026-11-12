package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/Lynricsy/Oryn/internal/omprpc"
)

// MaxCtlLineBytes 是 ctl 流单行 JSON 的上限（entries.append 可能携带较大的批次）。
const MaxCtlLineBytes = 96 << 20

// CtlMessage 是 ctl 流上的一行：
// 请求 `{id, method, params}`、应答 `{id, result}` / `{id, error}`、通知 `{method, params}`。
type CtlMessage struct {
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *CtlError       `json:"error,omitempty"`
}

// CtlError 是 ctl 应答中的错误，同时实现 error。
type CtlError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *CtlError) Error() string { return e.Code + ": " + e.Message }

// 标准错误码。
const (
	CodeMethodNotFound = "method_not_found"
	CodeInvalidParams  = "invalid_params"
	CodeInternal       = "internal"
	// CodeBusy 表示对端同时在途的请求已达 MaxConcurrentCtlRequests，本请求未执行（可稍后重试）。
	CodeBusy = "busy"
)

// MaxConcurrentCtlRequests 是一个 ctl 端点同时处理的对端请求上限（每个请求一个 goroutine、各自持有至多
// MaxCtlLineBytes 的参数）；超出的请求立即以 busy 应答，不执行。
const MaxConcurrentCtlRequests = 32

// ErrMethodNotFound 可由请求处理器返回，应答错误码为 method_not_found。
var ErrMethodNotFound = &CtlError{Code: CodeMethodNotFound, Message: "method not found"}

// ErrCtlClosed 表示 ctl 连接已关闭，未完成的调用全部以此失败。
var ErrCtlClosed = errors.New("link: ctl closed")

// CtlHandlers 处理对端发来的请求与通知。
//
//   - OnRequest 在独立 goroutine 中执行（长请求不阻塞读循环；同时至多 MaxConcurrentCtlRequests 个，超出回 busy）；返回值编码为 result，
//     返回 *CtlError 时原样作为错误应答，其它错误映射为 code=internal。nil 时一律回 method_not_found。
//   - OnNotify 在读循环中按到达顺序同步执行（保证 entries.append 等通知有序）；
//     处理器不应长时间阻塞。
type CtlHandlers struct {
	OnRequest func(ctx context.Context, method string, params json.RawMessage) (any, error)
	OnNotify  func(ctx context.Context, method string, params json.RawMessage)
}

// Ctl 是 ctl 流上的双向 JSON-lines RPC 端点。
type Ctl struct {
	rwc      io.ReadWriteCloser
	w        *omprpc.Writer
	handlers CtlHandlers

	nextID atomic.Uint64
	// slots 是在途对端请求的信号量（容量 MaxConcurrentCtlRequests）。
	slots   chan struct{}
	mu      sync.Mutex
	pending map[string]chan CtlMessage
	closed  bool

	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// NewCtl 在 rwc（通常是 t=ctl 的 Stream）上启动读循环。
func NewCtl(rwc io.ReadWriteCloser, h CtlHandlers) *Ctl {
	ctx, cancel := context.WithCancelCause(context.Background())
	c := &Ctl{
		rwc:      rwc,
		w:        omprpc.NewWriter(rwc, MaxCtlLineBytes),
		handlers: h,
		pending:  make(map[string]chan CtlMessage),
		slots:    make(chan struct{}, MaxConcurrentCtlRequests),
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Done 在读循环结束（连接关闭或出错）后关闭。
func (c *Ctl) Done() <-chan struct{} { return c.done }

// Err 返回读循环结束的原因（Done 之前为 nil）。
func (c *Ctl) Err() error {
	select {
	case <-c.done:
		return context.Cause(c.ctx)
	default:
		return nil
	}
}

// Close 关闭底层流并让所有未完成调用失败。
func (c *Ctl) Close() error {
	err := c.rwc.Close()
	c.shutdown(ErrCtlClosed)
	return err
}

func (c *Ctl) shutdown(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()
	c.cancel(cause)
	for _, ch := range pending {
		close(ch)
	}
}

// Call 发送请求并等待应答；result 为 nil 时丢弃结果。对端返回错误时得到 *CtlError。
func (c *Ctl) Call(ctx context.Context, method string, params, result any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	id := strconv.FormatUint(c.nextID.Add(1), 10)
	ch := make(chan CtlMessage, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrCtlClosed
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.pending != nil {
			delete(c.pending, id)
		}
		c.mu.Unlock()
	}()

	if err := c.w.WriteFrame(CtlMessage{ID: id, Method: method, Params: raw}); err != nil {
		return fmt.Errorf("link: ctl write: %w", err)
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			return ErrCtlClosed
		}
		if msg.Error != nil {
			return msg.Error
		}
		if result != nil && len(msg.Result) > 0 {
			if err := json.Unmarshal(msg.Result, result); err != nil {
				return fmt.Errorf("link: ctl decode result of %s: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Notify 发送通知（无应答）。
func (c *Ctl) Notify(method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	if c.ctx.Err() != nil {
		return ErrCtlClosed
	}
	if err := c.w.WriteFrame(CtlMessage{Method: method, Params: raw}); err != nil {
		return fmt.Errorf("link: ctl write: %w", err)
	}
	return nil
}

func marshalParams(params any) (json.RawMessage, error) {
	switch p := params.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return p, nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("link: ctl encode params: %w", err)
	}
	return b, nil
}

func (c *Ctl) readLoop() {
	defer close(c.done)
	r := omprpc.NewReader(c.rwc, MaxCtlLineBytes)
	for {
		line, err := r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = ErrCtlClosed
			}
			c.shutdown(err)
			_ = c.rwc.Close()
			return
		}
		var msg CtlMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			c.shutdown(fmt.Errorf("link: ctl malformed line: %w", err))
			_ = c.rwc.Close()
			return
		}
		switch {
		case msg.Method != "" && msg.ID != "":
			select {
			case c.slots <- struct{}{}:
				go func() {
					defer func() { <-c.slots }()
					c.serve(msg)
				}()
			default:
				reply := CtlMessage{ID: msg.ID, Error: &CtlError{Code: CodeBusy, Message: "too many concurrent ctl requests"}}
				if err := c.w.WriteFrame(reply); err != nil && c.ctx.Err() == nil {
					slog.Warn("link: ctl busy reply write failed", "method", msg.Method, "err", err)
				}
			}
		case msg.Method != "":
			if c.handlers.OnNotify != nil {
				c.handlers.OnNotify(c.ctx, msg.Method, msg.Params)
			}
		case msg.ID != "":
			c.mu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		default:
			slog.Warn("link: ctl message without id or method ignored", "line", truncate(line, 200))
		}
	}
}

func (c *Ctl) serve(req CtlMessage) {
	reply := CtlMessage{ID: req.ID}
	if c.handlers.OnRequest == nil {
		reply.Error = ErrMethodNotFound
	} else {
		res, err := c.handlers.OnRequest(c.ctx, req.Method, req.Params)
		if err != nil {
			var ce *CtlError
			if errors.As(err, &ce) {
				reply.Error = ce
			} else {
				reply.Error = &CtlError{Code: CodeInternal, Message: err.Error()}
			}
		} else {
			raw, err := marshalParams(res)
			if err != nil {
				reply.Error = &CtlError{Code: CodeInternal, Message: err.Error()}
			} else {
				if raw == nil {
					raw = json.RawMessage("{}")
				}
				reply.Result = raw
			}
		}
	}
	if err := c.w.WriteFrame(reply); err != nil && c.ctx.Err() == nil {
		slog.Warn("link: ctl reply write failed", "method", req.Method, "err", err)
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(bytes.ToValidUTF8(b[:n], nil)) + "…"
}
