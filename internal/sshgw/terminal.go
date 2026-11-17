package sshgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// TerminalWSPath 是主机网页终端的 chi 路由模式（挂在 Deps.Mounts，不经 /api/v1 鉴权组，由一次性票据鉴权）。
const TerminalWSPath = "/api/v1/ssh/hosts/{id}/terminal"

const (
	maxTerminals      = 8
	termReadLimit     = 1 << 20
	termWriteTimeout  = 30 * time.Second
	termOutBuf        = 32 << 10
	maxCloseReasonLen = 120

	termDefaultCols, termMinCols, termMaxCols = 120, 20, 500
	termDefaultRows, termMinRows, termMaxRows = 30, 5, 200
)

// 终端 WS 的关闭码（规格 sshHostTerminal）。
const (
	termCloseRevoked     websocket.StatusCode = 4401
	termCloseFull        websocket.StatusCode = 4429
	termCloseUnreachable websocket.StatusCode = 4502
)

// TicketRedeemer 兑换一次性 WS 票据（*auth.Tickets 实现）。
type TicketRedeemer interface {
	Redeem(ticket string) (auth.Principal, bool)
}

// PrincipalChecker 复核主体当前是否仍有效（*auth.Service 实现）。
type PrincipalChecker interface {
	PrincipalValid(ctx context.Context, p auth.Principal) (bool, error)
}

// termConn 是一条网页终端连接。
type termConn struct {
	principal auth.Principal
	ws        *websocket.Conn
	// ctx 管拨号与上游；WS 自身的读写不挂在它上面（取消读写的 ctx 会不发关闭帧就断开）。
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	mu     sync.Mutex
	up     *upstream
}

// closeWith 以 code 关闭连接（只第一次生效）：后台发关闭帧（不阻塞调用方，Revoke 与 invalidate 在锁外同步调用），
// 并关掉上游整条链。
func (c *termConn) closeWith(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		go func() { _ = c.ws.Close(code, reason) }()
		c.cancel()
		c.mu.Lock()
		up := c.up
		c.mu.Unlock()
		if up != nil {
			up.Close()
		}
	})
}

// setUpstream 记下拨通的上游；连接已关闭时返回 false（调用方负责关掉 up）。
func (c *termConn) setUpstream(up *upstream) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return false
	}
	c.up = up
	return true
}

// TerminalHandler 返回 `GET /api/v1/ssh/hosts/{id}/terminal?ticket=…&cols=…&rows=…` 的处理器：票据无效 → 401（升级前）；
// 主机不存在 → 404；之后：名额 → 复核主体 → 拨号 → PTY + shell → 双向拷贝。
func (s *Service) TerminalHandler(t TicketRedeemer, v PrincipalChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serveTerminal(w, r, t, v) })
}

func (s *Service) serveTerminal(w http.ResponseWriter, r *http.Request, t TicketRedeemer, v PrincipalChecker) {
	p, ok := t.Redeem(r.URL.Query().Get("ticket"))
	if !ok {
		http.Error(w, `{"code":"unauthorized","message":"invalid or expired ticket"}`, http.StatusUnauthorized)
		return
	}
	h, err := s.host(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		var se *Error
		if errors.As(err, &se) {
			http.Error(w, `{"code":"not_found","message":"主机不存在"}`, http.StatusNotFound)
		} else {
			http.Error(w, `{"code":"internal","message":"internal error"}`, http.StatusInternalServerError)
		}
		return
	}
	q := r.URL.Query()
	cols := clampQuery(q.Get("cols"), termDefaultCols, termMinCols, termMaxCols)
	rows := clampQuery(q.Get("rows"), termDefaultRows, termMinRows, termMaxRows)
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// 来源由一次性票据保证（票据只能经已鉴权的 REST 取得），与 /api/v1/ws 相同
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(termReadLimit)
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	c := &termConn{principal: p, ws: ws, ctx: ctx, cancel: cancel}
	defer c.closeWith(websocket.StatusNormalClosure, "")
	if !s.registerTerm(c) {
		c.closeWith(termCloseFull, "too many terminals")
		return
	}
	defer s.unregisterTerm(c)
	// 登记之后再复核主体：此后的吊销经 Revoke 关闭本连接，此前的吊销在这里被发现
	if v != nil {
		if ok, err := v.PrincipalValid(ctx, p); err != nil || !ok {
			c.closeWith(termCloseRevoked, "revoked")
			return
		}
	}
	s.runTerminal(c, h, cols, rows)
}

// runTerminal 拨通主机、开 PTY 与 shell，在 WS 与会话之间双向拷贝，结束时记审计。
func (s *Service) runTerminal(c *termConn, h db.SshHost, cols, rows int) {
	since := s.snapshot()
	up, err := s.dial(c.ctx, h)
	if err == nil {
		untrack, ok := s.track(up.hostIDs, since, func() { c.closeWith(termCloseRevoked, "host changed") })
		defer untrack()
		if !ok {
			up.Close()
			err = errStale
		} else if !c.setUpstream(up) {
			up.Close()
			return
		}
	}
	if err != nil {
		s.audit(c.ctx, auditRow{source: srcOwner, host: h, kind: "connect", detail: err.Error()})
		c.closeWith(termCloseUnreachable, truncateReason(err.Error()))
		return
	}
	sess, err := up.client.NewSession()
	if err != nil {
		c.closeWith(termCloseUnreachable, truncateReason("开会话失败："+err.Error()))
		return
	}
	defer sess.Close()
	out := &wsWriter{ws: c.ws}
	sess.Stdout, sess.Stderr = out, out
	stdin, err := sess.StdinPipe()
	if err != nil {
		c.closeWith(termCloseUnreachable, truncateReason(err.Error()))
		return
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		c.closeWith(termCloseUnreachable, truncateReason("申请 PTY 失败："+err.Error()))
		return
	}
	if err := sess.Shell(); err != nil {
		c.closeWith(termCloseUnreachable, truncateReason("启动 shell 失败："+err.Error()))
		return
	}
	start := time.Now()
	var exitCode *int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := sess.Wait()
		var ee *ssh.ExitError
		switch {
		case err == nil:
			exitCode = new(int64(0))
		case errors.As(err, &ee):
			exitCode = new(int64(ee.ExitStatus()))
		}
		c.closeWith(websocket.StatusNormalClosure, "")
	}()
	s.pumpInput(c, sess, stdin)
	c.closeWith(websocket.StatusNormalClosure, "")
	<-done
	s.audit(c.ctx, auditRow{source: srcOwner, host: h, kind: "shell", exitCode: exitCode,
		durationMs: new(time.Since(start).Milliseconds()), ok: exitCode == nil || *exitCode == 0})
}

// pumpInput 把客户端消息送进会话：二进制帧是输入字节，文本帧是 resize JSON。WS 关闭时返回。
func (s *Service) pumpInput(c *termConn, sess *ssh.Session, stdin io.Writer) {
	for {
		typ, data, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			if _, err := stdin.Write(data); err != nil {
				return
			}
			continue
		}
		var m struct {
			Type       string
			Cols, Rows int
		}
		if json.Unmarshal(data, &m) != nil || m.Type != "resize" {
			continue
		}
		cols := clamp(m.Cols, termMinCols, termMaxCols)
		rows := clamp(m.Rows, termMinRows, termMaxRows)
		if err := sess.WindowChange(rows, cols); err != nil {
			slog.Debug("终端调整尺寸失败", "err", err)
		}
	}
}

// wsWriter 把 PTY 输出写成二进制帧，每次写的截止时间为 termWriteTimeout。ssh.Session 以 32 KiB 缓冲拷贝。
type wsWriter struct{ ws *websocket.Conn }

func (w *wsWriter) Write(p []byte) (int, error) {
	for off := 0; off < len(p); off += termOutBuf {
		ctx, cancel := context.WithTimeout(context.Background(), termWriteTimeout)
		err := w.ws.Write(ctx, websocket.MessageBinary, p[off:min(len(p), off+termOutBuf)])
		cancel()
		if err != nil {
			return off, err
		}
	}
	return len(p), nil
}

func (s *Service) registerTerm(c *termConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.terms) >= maxTerminals {
		return false
	}
	s.terms[c] = struct{}{}
	return true
}

func (s *Service) unregisterTerm(c *termConn) {
	s.mu.Lock()
	delete(s.terms, c)
	s.mu.Unlock()
}

// Revoke 以 4401 关闭该主体的全部网页终端（设备吊销 / 登出）。
func (s *Service) Revoke(kind, id string) {
	s.mu.Lock()
	var victims []*termConn
	for c := range s.terms {
		if c.principal.Kind == kind && c.principal.ID == id {
			victims = append(victims, c)
		}
	}
	s.mu.Unlock()
	for _, c := range victims {
		c.closeWith(termCloseRevoked, "revoked")
	}
}

func clampQuery(v string, def, lo, hi int) int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return clamp(n, lo, hi)
}

func clamp(n, lo, hi int) int { return max(lo, min(hi, n)) }

// truncateReason 把关闭原因截到 maxCloseReasonLen 字节（WS 关闭帧原因上限 123 字节）。
func truncateReason(v string) string {
	if len(v) <= maxCloseReasonLen {
		return v
	}
	cut := maxCloseReasonLen
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut]
}
