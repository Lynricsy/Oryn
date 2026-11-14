package desktop

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// WSPath 是桌面 WebSocket 的 chi 路由模式（挂在 Deps.Mounts，不经 /api/v1 鉴权组）。
const WSPath = "/api/v1/orbs/{id}/desktop/ws"

// 桌面 WS 的关闭码（protocol.md「M8 → REST」）。
const (
	closeRevoked     websocket.StatusCode = 4401
	closeOutdated    websocket.StatusCode = 4409
	closeDeleted     websocket.StatusCode = 4410
	closeFull        websocket.StatusCode = 4429
	closeUnreachable websocket.StatusCode = 4502
	closeUnavailable websocket.StatusCode = 4503
)

// deskConn 是一个桌面观看者。
type deskConn struct {
	orbID     string
	principal auth.Principal
	ws        *websocket.Conn
	// ctx 管唤醒、拨号与 desk 流；WS 自身的读写不挂在它上面（取消读写的 ctx 会不发关闭帧就断开）。
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

// closeWith 以 code 关闭连接（只第一次生效）：后台发关闭帧（不阻塞调用方，Revoke 在吊销回调里调用），并结束 desk 流。
func (c *deskConn) closeWith(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		go func() { _ = c.ws.Close(code, reason) }()
		c.cancel()
	})
}

// WSHandler 返回 `GET /api/v1/orbs/{id}/desktop/ws?ticket=…` 的处理器：票据无效 → 401（升级前）；orb 不存在 → 404；
// 之后：观看者名额 → 唤醒 → DialDesk → 双向拷贝。客户端发来的每条消息记一次（合并后的）用户交互。
func (s *Service) WSHandler() http.Handler {
	return http.HandlerFunc(s.serveWS)
}

func (s *Service) serveWS(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tickets.Redeem(r.URL.Query().Get("ticket"))
	if !ok {
		http.Error(w, `{"code":"unauthorized","message":"invalid or expired ticket"}`, http.StatusUnauthorized)
		return
	}
	orbID := chi.URLParam(r, "id")
	if _, err := s.o.Get(r.Context(), orbID); err != nil {
		if errors.Is(err, orbs.ErrNotFound) {
			http.Error(w, `{"code":"not_found","message":"orb not found"}`, http.StatusNotFound)
		} else {
			http.Error(w, `{"code":"internal","message":"internal error"}`, http.StatusInternalServerError)
		}
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// 来源由一次性票据保证（票据只能经已鉴权的 REST 取得），与 /api/v1/ws 相同
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(wsReadLimit)
	ctx, cancel := context.WithCancel(s.ctx)
	c := &deskConn{orbID: orbID, principal: p, ws: ws, ctx: ctx, cancel: cancel}
	defer c.closeWith(websocket.StatusNormalClosure, "")
	if !s.register(c) {
		c.closeWith(closeFull, "too many viewers")
		return
	}
	defer s.unregister(c)
	// 登记之后再复核主体：此后的吊销经 Revoke 关闭本连接，此前的吊销在这里被发现
	if s.cfg.Valid != nil {
		if ok, err := s.cfg.Valid.PrincipalValid(ctx, p); err != nil || !ok {
			c.closeWith(closeRevoked, "revoked")
			return
		}
	}
	if err := s.o.EnsureUsable(ctx, orbID, orbs.FeatureDesktop, true, wakeWait); err != nil {
		switch {
		case errors.Is(err, orbs.ErrOrbdOutdated):
			c.closeWith(closeOutdated, "orbd_outdated")
		case errors.Is(err, orbs.ErrOrbGone):
			c.closeWith(closeDeleted, "orb_deleted")
		default:
			c.closeWith(closeUnavailable, "orb_unavailable")
		}
		return
	}
	desk, err := s.o.DialDesk(ctx, orbID)
	if err != nil {
		reason := "failed"
		var de *orbs.DeskDialError
		switch {
		case errors.As(err, &de):
			reason = de.Code
		case errors.Is(err, orbs.ErrOrbdOutdated):
			c.closeWith(closeOutdated, "orbd_outdated")
			return
		}
		slog.Info("桌面不可达", "orb", orbID, "err", err)
		c.closeWith(closeUnreachable, reason)
		return
	}
	defer desk.Close()
	stop := context.AfterFunc(ctx, func() { _ = desk.Close() })
	defer stop()
	go s.pumpToClient(c, desk)
	s.pumpToOrb(c, desk)
}

// pumpToOrb 把客户端消息写进 desk 流；每条消息记一次合并后的用户交互。
func (s *Service) pumpToOrb(c *deskConn, desk net.Conn) {
	defer c.cancel()
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		s.o.TouchUserCoalesced(c.ctx, c.orbID)
		if _, err := desk.Write(data); err != nil {
			return
		}
	}
}

// pumpToClient 把 desk 流上的字节以二进制消息写给客户端；每次写的截止时间为 wsWriteTimeout。
func (s *Service) pumpToClient(c *deskConn, desk net.Conn) {
	defer c.cancel()
	buf := make([]byte, 32<<10)
	for {
		n, err := desk.Read(buf)
		if n > 0 {
			wctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
			werr := c.ws.Write(wctx, websocket.MessageBinary, buf[:n])
			cancel()
			if werr != nil {
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && c.ctx.Err() == nil {
				slog.Debug("desk 流读取结束", "orb", c.orbID, "err", err)
			}
			return
		}
	}
}

// register 登记观看者；超过每 orb 上限返回 false。
func (s *Service) register(c *deskConn) bool {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.viewers[c.orbID] >= maxViewers {
		return false
	}
	s.viewers[c.orbID]++
	s.conns[c] = struct{}{}
	return true
}

func (s *Service) unregister(c *deskConn) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, ok := s.conns[c]; !ok {
		return
	}
	delete(s.conns, c)
	if s.viewers[c.orbID]--; s.viewers[c.orbID] <= 0 {
		delete(s.viewers, c.orbID)
	}
}

// Revoke 以 4401 关闭该主体的全部桌面连接（设备吊销 / 登出）。
func (s *Service) Revoke(kind, id string) {
	s.wmu.Lock()
	var victims []*deskConn
	for c := range s.conns {
		if c.principal.Kind == kind && c.principal.ID == id {
			victims = append(victims, c)
		}
	}
	s.wmu.Unlock()
	for _, c := range victims {
		c.closeWith(closeRevoked, "revoked")
	}
}
