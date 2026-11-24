package browserrelay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	// wsReadLimit 是单条扩展消息上限（需承载 base64 截图）。
	wsReadLimit = 64 << 20
	// wsMaxPendingBytes 是发往扩展的队列字节上限，超出即断开防慢客户端拖垮内存。
	wsMaxPendingBytes = 16 << 20
	wsWriteTimeout    = 30 * time.Second
)

// extClient 表示一条在线的 Chrome 扩展 WebSocket 连接。
type extClient struct {
	deviceID    string
	deviceName  string
	connectedAt int64
	ws          *websocket.Conn
	ctx         context.Context
	cancel      context.CancelFunc

	metaMu         sync.RWMutex
	instanceID     string
	browserSession string
	browserVersion string
	extVersion     string

	sendMu      sync.Mutex
	sendCond    *sync.Cond
	sendQueue   [][]byte
	sendPending int
	sendClosed  bool

	closeOnce sync.Once
}

func newExtClient(parent context.Context, deviceID, deviceName string, ws *websocket.Conn) *extClient {
	ctx, cancel := context.WithCancel(parent)
	c := &extClient{
		deviceID:    deviceID,
		deviceName:  deviceName,
		connectedAt: time.Now().UnixMilli(),
		ws:          ws,
		ctx:         ctx,
		cancel:      cancel,
	}
	c.sendCond = sync.NewCond(&c.sendMu)
	return c
}

func (c *extClient) send(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return c.enqueue(b)
}

func (c *extClient) enqueue(b []byte) bool {
	c.sendMu.Lock()
	if c.sendClosed {
		c.sendMu.Unlock()
		return false
	}
	if c.sendPending+len(b) > wsMaxPendingBytes {
		c.sendClosed = true
		c.sendQueue = nil
		c.sendPending = 0
		c.sendCond.Broadcast()
		c.sendMu.Unlock()
		c.closeWith(closeFull, "send buffer overflow")
		return false
	}
	c.sendQueue = append(c.sendQueue, b)
	c.sendPending += len(b)
	c.sendCond.Signal()
	c.sendMu.Unlock()
	return true
}

func (c *extClient) writeLoop() {
	for {
		c.sendMu.Lock()
		for len(c.sendQueue) == 0 && !c.sendClosed {
			c.sendCond.Wait()
		}
		if c.sendClosed {
			c.sendMu.Unlock()
			return
		}
		msg := c.sendQueue[0]
		c.sendQueue[0] = nil
		c.sendQueue = c.sendQueue[1:]
		c.sendPending -= len(msg)
		c.sendMu.Unlock()

		wctx, cancel := context.WithTimeout(c.ctx, wsWriteTimeout)
		err := c.ws.Write(wctx, websocket.MessageText, msg)
		cancel()
		if err != nil {
			c.closeWith(websocket.StatusGoingAway, "write failed")
			return
		}
	}
}

func (c *extClient) closeWith(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.sendMu.Lock()
		c.sendClosed = true
		c.sendQueue = nil
		c.sendPending = 0
		c.sendCond.Broadcast()
		c.sendMu.Unlock()
		_ = c.ws.Close(code, reason)
		c.cancel()
	})
}

// extMessage 是扩展发往控制面的统一信封。
type extMessage struct {
	T                    string          `json:"t"`
	InstanceID           string          `json:"instanceId,omitempty"`
	BrowserSession       string          `json:"browserSession,omitempty"`
	BrowserVersion       string          `json:"browserVersion,omitempty"`
	UserAgent            string          `json:"userAgent,omitempty"`
	ExtVersion           string          `json:"extVersion,omitempty"`
	ThreadID             string          `json:"threadId,omitempty"`
	SharedTabCount       int             `json:"sharedTabCount,omitempty"`
	PendingApprovals     int             `json:"pendingApprovals,omitempty"`
	AutoApproveCreateTab *bool           `json:"autoApproveCreateTab,omitempty"`
	ApprovalID           string          `json:"approvalId,omitempty"`
	Decision             string          `json:"decision,omitempty"`
	Summary              string          `json:"summary,omitempty"`
	Payload              json.RawMessage `json:"payload,omitempty"`
}

// WSHandler 返回 `GET /api/v1/browser-relay/ws?ticket=…` 的处理器。
func (s *Service) WSHandler() http.Handler {
	return http.HandlerFunc(s.serveWS)
}

func (s *Service) serveWS(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tickets.Redeem(r.URL.Query().Get("ticket"))
	if !ok {
		http.Error(w, `{"code":"unauthorized","message":"invalid or expired ticket"}`, http.StatusUnauthorized)
		return
	}
	if p.Kind != auth.KindDevice {
		http.Error(w, `{"code":"forbidden","message":"browser_extension device token required"}`, http.StatusForbidden)
		return
	}
	dev, err := s.st.Q.GetDevice(r.Context(), p.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, `{"code":"unauthorized","message":"device not found"}`, http.StatusUnauthorized)
		} else {
			http.Error(w, `{"code":"internal","message":"internal error"}`, http.StatusInternalServerError)
		}
		return
	}
	if dev.Kind != string(apigen.BrowserExtension) || dev.RevokedAt != nil {
		http.Error(w, `{"code":"forbidden","message":"device is not an active browser extension"}`, http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// 来源由一次性票据保证（票据只能由已鉴权的 browser_extension 设备令牌取得）
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(wsReadLimit)

	c := newExtClient(s.ctx, dev.ID, dev.Name, ws)
	defer c.closeWith(websocket.StatusNormalClosure, "")

	// 登记连接：同一 deviceID 的旧连接被 4409 顶替
	s.mu.Lock()
	prev := s.clients[dev.ID]
	s.clients[dev.ID] = c
	s.mu.Unlock()
	if prev != nil {
		prev.closeWith(closeReplaced, "replaced by new connection")
	}

	// 登记之后复核主体，防票据签发到登记之间的吊销竞态
	if s.cfg.Valid != nil {
		if valid, err := s.cfg.Valid.PrincipalValid(c.ctx, p); err != nil || !valid {
			s.unregisterClient(c)
			c.closeWith(closeRevoked, "revoked")
			return
		}
	}

	defer s.unregisterClient(c)
	go c.writeLoop()

	s.readLoop(c)
}

func (s *Service) unregisterClient(c *extClient) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var toClose []*sessionBridge
	var affected []string

	s.mu.Lock()
	isCurrent := s.clients[c.deviceID] == c
	if isCurrent {
		delete(s.clients, c.deviceID)
		for tid, br := range s.bridges {
			if br.client == c {
				delete(s.bridges, tid)
				delete(s.stats, tid)
				toClose = append(toClose, br)
				affected = append(affected, tid)
			}
		}
	}
	s.mu.Unlock()

	for _, br := range toClose {
		br.close()
	}
	if isCurrent {
		if rows, err := s.st.Q.ListThreadBrowserRelayBindingsByDevice(ctx, c.deviceID); err == nil {
			for _, r := range rows {
				affected = append(affected, r.ThreadID)
			}
		}
		for _, tid := range dedupStrings(affected) {
			s.emitThreadState(ctx, tid)
		}
		s.emitClients(ctx)
	}
}

func (s *Service) readLoop(c *extClient) {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var m extMessage
		if err := json.Unmarshal(data, &m); err != nil {
			slog.Debug("忽略格式无效的浏览器扩展消息", "device", c.deviceID, "err", err)
			continue
		}
		switch m.T {
		case "ping":
			c.send(map[string]string{"t": "pong"})
		case "register":
			c.metaMu.Lock()
			c.instanceID = m.InstanceID
			c.browserSession = m.BrowserSession
			c.browserVersion = m.BrowserVersion
			c.extVersion = m.ExtVersion
			c.metaMu.Unlock()
			c.send(map[string]any{
				"t":          "registered",
				"deviceId":   c.deviceID,
				"deviceName": c.deviceName,
				"threads":    s.activeWorkThreads(c.ctx),
			})
			s.restoreDeviceBindings(c.ctx, c.deviceID)
			s.emitClients(c.ctx)
		case "threads.list":
			c.send(map[string]any{
				"t":       "threads.update",
				"threads": s.activeWorkThreads(c.ctx),
			})
		case "session.bind":
			if m.ThreadID != "" {
				if _, err := s.BindThread(c.ctx, m.ThreadID, c.deviceID); err != nil {
					slog.Debug("扩展请求绑定线程失败", "device", c.deviceID, "thread", m.ThreadID, "err", err)
				}
			}
		case "session.unbind":
			if m.ThreadID != "" {
				if _, err := s.UnbindThread(c.ctx, m.ThreadID); err != nil {
					slog.Debug("扩展请求解绑线程失败", "device", c.deviceID, "thread", m.ThreadID, "err", err)
				}
			}
		case "session.state":
			if m.ThreadID == "" {
				continue
			}
			s.mu.Lock()
			s.stats[m.ThreadID] = sessionStats{
				sharedTabCount:       max(0, m.SharedTabCount),
				pendingApprovals:     max(0, m.PendingApprovals),
				autoApproveCreateTab: m.AutoApproveCreateTab,
			}
			s.mu.Unlock()
			s.emitThreadState(c.ctx, m.ThreadID)
			s.emitClients(c.ctx)
		case "session.approval_resolved":
			if m.ThreadID == "" {
				continue
			}
			s.notifyApprovalResolved(m.ThreadID, m.Decision, m.Summary)
			s.emitThreadState(c.ctx, m.ThreadID)
			s.emitClients(c.ctx)
		case "session.frame":
			if m.ThreadID == "" || len(m.Payload) == 0 {
				continue
			}
			s.forwardExtFrame(c, m.ThreadID, m.Payload)
		}
	}
}

func (s *Service) restoreDeviceBindings(ctx context.Context, deviceID string) {
	rows, err := s.st.Q.ListThreadBrowserRelayBindingsByDevice(ctx, deviceID)
	if err != nil {
		return
	}
	for _, r := range rows {
		s.ensureBridge(ctx, r.ThreadID)
		s.emitThreadState(ctx, r.ThreadID)
	}
}

func dbUpsertParams(threadID, deviceID string, now int64) db.UpsertThreadBrowserRelayBindingParams {
	return db.UpsertThreadBrowserRelayBindingParams{
		ThreadID:  threadID,
		DeviceID:  deviceID,
		CreatedAt: now,
		UpdatedAt: now,
	}
}
