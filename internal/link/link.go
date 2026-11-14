// Package link 实现 orb link（计划 C2）：orbd 出站连接控制面的
// WebSocket（子协议 oryn.orb.v1），其上承载 yamux 多路复用；
// 每条 yamux 流以一行 JSON 流头开始。orbd 与 orynd 共用本包。
package link

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

const (
	// Subprotocol 是 WebSocket 子协议名。
	Subprotocol = "oryn.orb.v1"
	// Path 是控制面上 link 端点的路径。
	Path = "/orb/v1/link"
	// KeepAliveInterval 是 yamux 心跳间隔。
	KeepAliveInterval = 10 * time.Second
)

// ErrUnauthorized 表示握手时令牌被拒绝（服务端返回 401）。
var ErrUnauthorized = errors.New("link: unauthorized")

// URL 把控制面基础地址（如 ORYN_URL=http://172.18.0.1:7600）转换为 link 端点地址。
func URL(base string) string {
	return strings.TrimRight(base, "/") + Path
}

// Session 是一条已建立的 link：底层传输（WebSocket 或任意字节流）+ yamux 会话。
type Session struct {
	closeConn func()
	mux       *yamux.Session
	cancel    context.CancelFunc
	once      sync.Once
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = KeepAliveInterval
	cfg.LogOutput = nil
	cfg.Logger = slogLogger{}
	return cfg
}

// slogLogger 把 yamux 的日志转到 slog（debug 级别）。
type slogLogger struct{}

func (slogLogger) Print(v ...any) { slog.Debug(fmt.Sprint(v...), "component", "yamux") }
func (slogLogger) Printf(format string, v ...any) {
	slog.Debug(fmt.Sprintf(format, v...), "component", "yamux")
}
func (slogLogger) Println(v ...any) { slog.Debug(fmt.Sprint(v...), "component", "yamux") }

func newSession(ws *websocket.Conn, client bool) (*Session, error) {
	// NetConn 的 ctx 决定整条连接的寿命；与 HTTP 请求的 ctx 解耦，由 Close 取消。
	ctx, cancel := context.WithCancel(context.Background())
	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	var (
		mux *yamux.Session
		err error
	)
	if client {
		mux, err = yamux.Client(nc, yamuxConfig())
	} else {
		mux, err = yamux.Server(nc, yamuxConfig())
	}
	if err != nil {
		cancel()
		_ = ws.CloseNow()
		return nil, fmt.Errorf("link: yamux: %w", err)
	}
	s := &Session{
		closeConn: func() { _ = ws.Close(websocket.StatusNormalClosure, "") },
		mux:       mux,
		cancel:    cancel,
	}
	go func() {
		<-mux.CloseChan()
		s.Close()
	}()
	return s, nil
}

// NewConnSession 在任意字节流（如继承来的 socketpair）上建立 link 会话：
// client 为真时作 yamux 客户端（orbd 一侧），否则作服务端（orynd 一侧）。
// 不做任何鉴权，调用方须自行保证对端身份。
func NewConnSession(conn net.Conn, client bool) (*Session, error) {
	var (
		mux *yamux.Session
		err error
	)
	if client {
		mux, err = yamux.Client(conn, yamuxConfig())
	} else {
		mux, err = yamux.Server(conn, yamuxConfig())
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("link: yamux: %w", err)
	}
	s := &Session{
		closeConn: func() { _ = conn.Close() },
		mux:       mux,
		cancel:    func() {},
	}
	go func() {
		<-mux.CloseChan()
		s.Close()
	}()
	return s, nil
}

// Dial 由 orbd 调用：连接 url（http/https/ws/wss 均可，须含 Path），
// 携带 `Authorization: Bearer <token>` 与子协议，成功后作为 yamux 客户端。
// ctx 只约束握手过程。
func Dial(ctx context.Context, url, token string) (*Session, error) {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	ws, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader:   h,
		Subprotocols: []string{Subprotocol},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
		}
		return nil, fmt.Errorf("link: dial: %w", err)
	}
	if ws.Subprotocol() != Subprotocol {
		_ = ws.Close(websocket.StatusPolicyViolation, "subprotocol required")
		return nil, fmt.Errorf("link: server did not accept subprotocol %q", Subprotocol)
	}
	return newSession(ws, true)
}

// AuthFunc 校验 Bearer 令牌；返回错误即拒绝（401）。
type AuthFunc func(r *http.Request, token string) error

// Accept 由 orynd 的 HTTP 处理器调用：校验令牌与子协议后升级连接，
// 作为 yamux 服务端。失败时已写出 HTTP 错误响应。
// 返回后调用方可以阻塞在 <-s.CloseChan() 上，也可以直接返回处理器。
func Accept(w http.ResponseWriter, r *http.Request, auth AuthFunc) (*Session, error) {
	token, ok := bearerToken(r)
	if !ok {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return nil, ErrUnauthorized
	}
	if err := auth(r, token); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}})
	if err != nil {
		return nil, fmt.Errorf("link: accept: %w", err)
	}
	if ws.Subprotocol() != Subprotocol {
		_ = ws.Close(websocket.StatusPolicyViolation, "subprotocol "+Subprotocol+" required")
		return nil, fmt.Errorf("link: client did not request subprotocol %q", Subprotocol)
	}
	return newSession(ws, false)
}

func bearerToken(r *http.Request) (string, bool) {
	v := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(v) <= len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(v[len(prefix):]), true
}

// Open 打开一条新流并写出流头。
func (s *Session) Open(ctx context.Context, h StreamHeader) (*Stream, error) {
	line, err := h.encode()
	if err != nil {
		return nil, err
	}
	st, err := s.mux.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("link: open stream: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = st.SetWriteDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = st.Close() })
	_, err = st.Write(line)
	if !stop() || err != nil {
		_ = st.Close()
		if err == nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("link: write stream header: %w", err)
	}
	_ = st.SetWriteDeadline(time.Time{})
	return &Stream{Stream: st, Header: h, br: bufio.NewReader(st)}, nil
}

// AcceptStream 等待对端打开的下一条流并读出流头。
// 流头非法的流会被关闭并返回错误；调用方可以继续 AcceptStream。
func (s *Session) AcceptStream(ctx context.Context) (*Stream, error) {
	type result struct {
		st  *yamux.Stream
		err error
	}
	ch := make(chan result, 1)
	go func() {
		st, err := s.mux.AcceptStream()
		ch <- result{st, err}
	}()
	var res result
	select {
	case res = <-ch:
	case <-ctx.Done():
		// 让后台 goroutine 拿到的流不泄漏。
		go func() {
			if r := <-ch; r.st != nil {
				_ = r.st.Close()
			}
		}()
		return nil, ctx.Err()
	}
	if res.err != nil {
		return nil, fmt.Errorf("link: accept stream: %w", res.err)
	}
	st := res.st
	br := bufio.NewReader(st)
	stop := context.AfterFunc(ctx, func() { _ = st.Close() })
	_ = st.SetReadDeadline(time.Now().Add(30 * time.Second))
	h, err := readHeader(br)
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = st.Close()
		return nil, &HeaderError{Err: err}
	}
	_ = st.SetReadDeadline(time.Time{})
	return &Stream{Stream: st, Header: h, br: br}, nil
}

// HeaderError 表示一条流的流头读取失败；会话本身仍然可用。
type HeaderError struct{ Err error }

func (e *HeaderError) Error() string { return "link: stream header: " + e.Err.Error() }
func (e *HeaderError) Unwrap() error { return e.Err }

// Close 关闭 yamux 会话与底层传输；可重复调用。
func (s *Session) Close() error {
	s.once.Do(func() {
		_ = s.mux.Close()
		s.closeConn()
		s.cancel()
	})
	return nil
}

// CloseChan 在会话关闭（任一方关闭、心跳超时、网络断开）后关闭。
func (s *Session) CloseChan() <-chan struct{} { return s.mux.CloseChan() }

// IsClosed 报告会话是否已关闭。
func (s *Session) IsClosed() bool { return s.mux.IsClosed() }

// RemoteAddr 返回对端地址（客户端一侧为占位地址）。
func (s *Session) RemoteAddr() net.Addr { return s.mux.RemoteAddr() }

// Stream 是一条带已解析流头的 yamux 流。Read 先返回读流头时缓冲的剩余字节。
// Close 发送 FIN 半关闭写方向：对端读到 EOF，本端仍可继续读取对端剩余数据。
type Stream struct {
	*yamux.Stream
	Header StreamHeader
	br     *bufio.Reader
}

// Read 实现 io.Reader。
func (s *Stream) Read(p []byte) (int, error) { return s.br.Read(p) }

// ErrLineTooLong 表示 ReadLine 读到的行超过了上限。
var ErrLineTooLong = errors.New("link: line too long")

// ReadLine 读出一行（含结尾 `\n`，不含则表示流在换行前结束并返回错误），
// 行长超过 max 字节即返回 ErrLineTooLong。经内部缓冲读取，之后的 Read 从该行之后继续，
// 供读 portal 流的应答行（LinkPortalReply）后再读原始字节。
func (s *Stream) ReadLine(max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := s.br.ReadSlice('\n')
		if len(buf)+len(chunk) > max {
			return nil, ErrLineTooLong
		}
		buf = append(buf, chunk...)
		if err == nil {
			return buf, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

var _ io.ReadWriteCloser = (*Stream)(nil)
