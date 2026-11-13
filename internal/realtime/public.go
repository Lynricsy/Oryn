package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 匿名（公开分享）连接的限额。
const (
	// MaxPublicPerShare / MaxPublicTotal 是每个分享与全局的匿名连接上限。
	MaxPublicPerShare = 64
	MaxPublicTotal    = 512
	// publicReadLimit 是匿名客户端单条消息上限（只接受 ping）。
	publicReadLimit = 4 << 10
	// closeRevoked 是分享撤销 / 线程删除时的关闭码。
	closeRevoked websocket.StatusCode = 4001
)

// publicFrameTypes 是匿名连接能收到的 omp 帧类型（其余帧——待答请求、终端、队列、状态、子 agent 进度等——一律不发）；
// 白名单内的帧还要过 publicFrame 的内容过滤。
var publicFrameTypes = map[string]bool{
	"message_start": true, "message_update": true, "message_end": true,
	"tool_execution_start": true, "tool_execution_update": true, "tool_stream_update": true, "tool_execution_end": true,
	"agent_start": true, "agent_end": true,
}

// PublicSub 描述一条匿名连接要看的分享。Backfill 返回公开范围内 after < seq ≤ upTo 的条目（升序），
// Filter 是对实时到达条目的同一过滤（返回 false 即不发）。
type PublicSub struct {
	ShareID, ThreadID string
	Backfill          func(ctx context.Context, after, upTo int64) ([]threads.Entry, error)
	Filter            func(threads.Entry) (threads.Entry, bool)
}

// pubSub 是匿名连接对线程的订阅；由 Hub.mu 保护。补发期间实时事件先进 buf，分支事件先进 pending。
type pubSub struct {
	shareID   string
	filter    func(threads.Entry) (threads.Entry, bool)
	buffering bool
	buf       [][]byte
	lastState string
	lastTitle string
	// branch 跟踪客户端手里的活动分支；叶子跳到它之外时发 transcript.reset 让客户端整段重拉（B7）。
	branch branchView
	// pending 是补发期间到达的分支事件：补发完成、branch 以补发内容初始化之后按序重放。
	pending []branchEvent
	// rev 是客户端当前的转录版本（transcript.reset 要带上它）。
	rev int64
	// resetSent 为真表示已让客户端重连（之后的分支事件不再处理）。
	resetSent bool
}

type publicSnapshot struct {
	Type          string            `json:"type"`
	Title         string            `json:"title"`
	State         string            `json:"state"`
	LeafEntryID   *string           `json:"leafEntryId,omitempty"`
	LastSeq       int64             `json:"lastSeq"`
	TranscriptRev int64             `json:"transcriptRev"`
	FullReplay    bool              `json:"fullReplay"`
	Live          []json.RawMessage `json:"live"`
	Subagents     []json.RawMessage `json:"subagents"`
}

type publicEntries struct {
	Type        string          `json:"type"`
	Entries     []threads.Entry `json:"entries"`
	LeafEntryID *string         `json:"leafEntryId,omitempty"`
	LastSeq     int64           `json:"lastSeq"`
}

func publicTitle(t string) string {
	if t == "" {
		return "Untitled"
	}
	return t
}

func marshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("realtime: 编码消息失败", "err", err)
		return nil
	}
	return b
}

var revokedMsg = marshal(apigen.WsPublicRevoked{Type: apigen.ShareRevoked})

// reservePublic 在升级前占一个匿名名额；分享已撤销返回 404，名额已满返回 503。
func (h *Hub) reservePublic(shareID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.revokedShares[shareID]:
		return http.StatusNotFound
	case h.pubTotal >= h.MaxPublicTotal || h.pubPerShare[shareID] >= h.MaxPublicPerShare:
		return http.StatusServiceUnavailable
	}
	h.pubTotal++
	h.pubPerShare[shareID]++
	return 0
}

func (h *Hub) releasePublicLocked(shareID string) {
	h.pubTotal--
	if h.pubPerShare[shareID]--; h.pubPerShare[shareID] <= 0 {
		delete(h.pubPerShare, shareID)
	}
}

// ServePublic 处理公开分享的匿名 WebSocket：share.snapshot → 分批补发公开条目 → 放出缓冲的实时事件 → 只认 ping。
// 匿名连接不进 conns / subs，只收到显式写给它的消息；不算 focus、不记用户交互、不唤醒 orb。
func (h *Hub) ServePublic(w http.ResponseWriter, r *http.Request, sub PublicSub, after int64, rev *int64) {
	switch h.reservePublic(sub.ShareID) {
	case http.StatusNotFound:
		writeJSONError(w, http.StatusNotFound, `{"code":"not_found","message":"not found"}`)
		return
	case http.StatusServiceUnavailable:
		writeJSONError(w, http.StatusServiceUnavailable, `{"code":"share_busy","message":"too many viewers"}`)
		return
	}
	// 默认的同源 Origin 校验保留：匿名连接没有票据，只允许本站页面发起
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.mu.Lock()
		h.releasePublicLocked(sub.ShareID)
		h.mu.Unlock()
		return
	}
	ws.SetReadLimit(publicReadLimit)
	ctx, cancel := context.WithCancel(context.Background())
	c := &conn{h: h, id: fmt.Sprintf("pub-%d", h.nextConn.Add(1)), ws: ws, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), public: true}
	go c.writeLoop()
	var mine *pubSub
	defer h.dropPublic(c, sub, &mine)

	var snap threads.Snapshot
	err = h.svc.Subscribe(ctx, sub.ThreadID, func(s threads.Snapshot) {
		full := (rev != nil && *rev != s.TranscriptRev) || (rev == nil && after > 0 && s.TranscriptRev > 0)
		if full {
			after = 0
		}
		snap = s
		title := publicTitle(s.Thread.Title)
		state := string(s.Thread.State)
		mine = &pubSub{shareID: sub.ShareID, filter: sub.Filter, buffering: true, lastState: state, lastTitle: title, rev: s.TranscriptRev}
		live := publicLive(s.Agent.Live)
		h.mu.Lock()
		if h.revokedShares[sub.ShareID] {
			// 撤销发生在占名额之后、登记之前：不登记，直接以撤销收尾
			h.mu.Unlock()
			mine = nil
			c.finish(revokedMsg, closeRevoked, "revoked")
			return
		}
		m := h.pubs[sub.ThreadID]
		if m == nil {
			m = map[*conn]*pubSub{}
			h.pubs[sub.ThreadID] = m
		}
		m[c] = mine
		h.mu.Unlock()
		// 子 agent 的进度不公开（F3）：subagents 恒为空
		c.send(publicSnapshot{
			Type: string(apigen.ShareSnapshot), Title: title, State: state, LeafEntryID: s.LeafEntryID, LastSeq: s.LastSeq,
			TranscriptRev: s.TranscriptRev, FullReplay: full, Live: live, Subagents: []json.RawMessage{},
		})
	})
	if errors.Is(err, threads.ErrNotFound) {
		// 线程已删除：分享随之失效
		c.finish(revokedMsg, closeRevoked, "revoked")
		c.waitClosed()
		return
	}
	if err != nil {
		slog.Warn("realtime: 公开分享订阅失败", "share", sub.ShareID, "err", err)
		c.close(websocket.StatusInternalError, "subscribe failed")
		return
	}
	if mine == nil {
		c.waitClosed()
		return
	}
	var backfilled []threads.Entry
	if after < snap.LastSeq {
		es, err := sub.Backfill(ctx, after, snap.LastSeq)
		if err != nil {
			slog.Warn("realtime: 公开分享补发失败", "share", sub.ShareID, "err", err)
			c.close(websocket.StatusInternalError, "backfill failed")
			return
		}
		backfilled = es
		for i := 0; i < len(es); i += entriesBatch {
			batch := es[i:min(i+entriesBatch, len(es))]
			last := batch[len(batch)-1].Seq
			if i+entriesBatch >= len(es) {
				last = snap.LastSeq
			}
			c.send(publicEntries{Type: string(apigen.WsPublicEntriesTypeEntries), Entries: batch, LeafEntryID: snap.LeafEntryID, LastSeq: last})
		}
	}
	// 客户端手里的分支 = 补发内容（全量补发）或此前已持有的、以快照叶子为末端的分支（续传）
	fallback := ""
	if after > 0 {
		fallback = deref(snap.LeafEntryID)
	}
	h.mu.Lock()
	if h.pubs[sub.ThreadID][c] == mine {
		mine.branch.init(linksOf(backfilled), fallback, deref(snap.LeafEntryID))
		reset := false
		for _, ev := range mine.pending {
			if reset = mine.branch.apply(ev); reset {
				break
			}
		}
		mine.pending = nil
		c.release(mine.buf)
		mine.buf, mine.buffering = nil, false
		if reset && !mine.resetSent {
			h.resetPublicLocked(c, mine)
		}
	}
	h.mu.Unlock()
	c.publicReadLoop()
}

func writeJSONError(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}

// publicReadLoop 只接受 ping；任何其它消息即以 1008 关闭。
func (c *conn) publicReadLoop() {
	for {
		_, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		var m struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &m) != nil || m.Type != "ping" {
			c.close(websocket.StatusPolicyViolation, "only ping is accepted")
			return
		}
		c.send(map[string]string{"type": "pong"})
	}
}

// dropPublic 移除匿名连接并归还名额（不调用 Focus：匿名连接从不 focus）。
func (h *Hub) dropPublic(c *conn, sub PublicSub, mine **pubSub) {
	c.close(websocket.StatusNormalClosure, "")
	h.mu.Lock()
	if m := h.pubs[sub.ThreadID]; m != nil && *mine != nil && m[c] == *mine {
		c.discard((*mine).buf)
		delete(m, c)
		if len(m) == 0 {
			delete(h.pubs, sub.ThreadID)
		}
	}
	h.releasePublicLocked(sub.ShareID)
	h.mu.Unlock()
}

// ClosePublic 给该分享的全部匿名连接发 share.revoked 并以 4001 关闭；此后该分享不再接受新连接。
func (h *Hub) ClosePublic(shareID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.revokedShares[shareID] = true
	for tid, m := range h.pubs {
		for c, ps := range m {
			if ps.shareID != shareID {
				continue
			}
			c.discard(ps.buf)
			delete(m, c)
			c.finish(revokedMsg, closeRevoked, "revoked")
		}
		if len(m) == 0 {
			delete(h.pubs, tid)
		}
	}
}

// closeThreadPublicLocked 关闭线程的全部匿名连接（线程删除）。调用方持有 h.mu。
func (h *Hub) closeThreadPublicLocked(threadID string) {
	for c, ps := range h.pubs[threadID] {
		c.discard(ps.buf)
		c.finish(revokedMsg, closeRevoked, "revoked")
	}
	delete(h.pubs, threadID)
}

// toPublicLocked 把 b 投递给线程的全部匿名连接（补发中的先缓冲）。调用方持有 h.mu。
func (h *Hub) toPublicLocked(threadID string, b []byte) {
	if b == nil {
		return
	}
	for c, ps := range h.pubs[threadID] {
		if ps.buffering {
			c.stash(&ps.buf, b)
			continue
		}
		c.enqueue(b)
	}
}

// publicEntriesLocked 把新条目逐条过各连接的过滤后发 entries（不带 threadId），再把这批条目交给分支跟踪。
// 调用方持有 h.mu。
func (h *Hub) publicEntriesLocked(threadID string, es []threads.Entry, leaf *string, lastSeq int64) {
	ev := branchEvent{entries: true, links: linksOf(es), leaf: deref(leaf)}
	for c, ps := range h.pubs[threadID] {
		out := make([]threads.Entry, 0, len(es))
		for _, e := range es {
			if ps.filter == nil {
				continue
			}
			if pe, ok := ps.filter(e); ok {
				out = append(out, pe)
			}
		}
		if len(out) > 0 {
			if b := marshal(publicEntries{Type: string(apigen.WsPublicEntriesTypeEntries), Entries: out, LeafEntryID: leaf, LastSeq: lastSeq}); b != nil {
				if ps.buffering {
					c.stash(&ps.buf, b)
				} else {
					c.enqueue(b)
				}
			}
		}
		h.branchLocked(c, ps, ev)
	}
}

// publicStateLocked 在线程状态或标题变化时发 share.state；叶子变化交给分支跟踪（切分支不产生新条目，
// 只经 index.patch 体现，B7）。调用方持有 h.mu。
func (h *Hub) publicStateLocked(t apigen.Thread) {
	m := h.pubs[t.ID]
	if len(m) == 0 {
		return
	}
	state, title := string(t.State), publicTitle(t.Title)
	ev := branchEvent{leaf: deref(t.LeafEntryID)}
	var b []byte
	for c, ps := range m {
		if ps.lastState != state || ps.lastTitle != title {
			ps.lastState, ps.lastTitle = state, title
			if b == nil {
				b = marshal(apigen.WsPublicState{Type: apigen.ShareState, State: t.State, Title: title})
			}
			if ps.buffering {
				c.stash(&ps.buf, b)
			} else {
				c.enqueue(b)
			}
		}
		h.branchLocked(c, ps, ev)
	}
}

// Notification 实现 notify.Broadcaster：新通知广播给全部私有连接（匿名连接收不到）。
func (h *Hub) Notification(n apigen.Notification) {
	h.toAll(apigen.WsServerNotification{Type: apigen.WsServerNotificationTypeNotification, Notification: n})
}

// NotificationsRead 实现 notify.Broadcaster：threadID 为空表示全部已读。
func (h *Hub) NotificationsRead(threadID string) {
	m := apigen.WsServerNotificationsRead{Type: apigen.NotificationsRead}
	if threadID != "" {
		m.ThreadID = &threadID
	}
	h.toAll(m)
}
