// Package realtime 实现客户端 WebSocket hub（计划 C3 `GET /api/v1/ws?ticket=`）：
// 连接建立后推 `index`，之后 `index.patch`；`sub` 时先发 `thread.snapshot`，再分批补发条目，
// 然后转实时；`cmd` 的应答只回发起连接；`ui.response` 先到先得；待发超过 8MiB 的慢连接被断开。
// 终端输出（M3）只发给 `term.watch` 开启的订阅，单连接待发超过 TermLag 时丢弃并改发 `term.lagged`，不断开。
// 公开分享（M5）的匿名连接在独立的 pubs 表里（public.go），只收到显式写给它的消息。
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// TicketRedeemer 兑换一次性 WS 票据（auth.Tickets 实现）。
type TicketRedeemer interface {
	Redeem(ticket string) (auth.Principal, bool)
}

// PrincipalChecker 复核主体当前是否仍有效（设备未吊销、网页会话存在且未过期）；auth.Service 实现。
type PrincipalChecker interface {
	PrincipalValid(ctx context.Context, p auth.Principal) (bool, error)
}

// Revocations 在设备吊销 / 登出时回调（kind、id 与 auth.Principal 对应）；hub 据此关闭相关连接。
type Revocations interface {
	Subscribe(fn func(kind, id string))
}

// 默认参数。
const (
	// MaxPendingBytes 是单连接待发字节上限，超过即断开让其重连补拉。
	MaxPendingBytes = 8 << 20
	// TermLagBytes 是终端输出的软上限：单连接待发超过它时丢弃 terminal_output 并发 term.lagged。
	TermLagBytes = 1 << 20
	// MaxMessageBytes 是客户端单条消息上限（带内联图片的 prompt）。
	MaxMessageBytes = 16 << 20
	writeTimeout    = 30 * time.Second
	entriesBatch    = 200
)

// Hub 是客户端 WS hub，同时实现 threads.Sink。
type Hub struct {
	svc     *threads.Service
	tickets TicketRedeemer
	valid   PrincipalChecker
	// MaxPending / TermLag 可在测试中调小。
	MaxPending int
	TermLag    int
	// MaxPublicPerShare / MaxPublicTotal 是匿名连接名额（默认 64 / 512），可在测试中调小。
	MaxPublicPerShare int
	MaxPublicTotal    int

	nextConn atomic.Uint64

	mu    sync.Mutex
	rev   int64
	conns map[*conn]struct{}
	subs  map[string]map[*conn]*sub
	// pubs 是公开分享的匿名连接（线程 id → 连接），与 conns / subs 互不相交：
	// 只有显式写给它的消息（public.go）才到得了匿名连接。
	pubs        map[string]map[*conn]*pubSub
	pubTotal    int
	pubPerShare map[string]int
	// revokedShares 是已撤销的分享 id：撤销不可逆，晚到的连接据此在登记前被拒（条目数 = 撤销次数）。
	revokedShares map[string]bool
}

// New 创建 hub 并设为 svc 的 Sink；rev 可为 nil。valid 在连接登记后复核票据主体（H3：票据签发后、
// 兑换前主体被吊销时，吊销事件发生在连接登记之前、关不掉它）。
func New(svc *threads.Service, tickets TicketRedeemer, rev Revocations, valid PrincipalChecker) *Hub {
	h := &Hub{
		svc: svc, tickets: tickets, valid: valid, MaxPending: MaxPendingBytes, TermLag: TermLagBytes,
		MaxPublicPerShare: MaxPublicPerShare, MaxPublicTotal: MaxPublicTotal,
		conns: map[*conn]struct{}{}, subs: map[string]map[*conn]*sub{},
		pubs: map[string]map[*conn]*pubSub{}, pubPerShare: map[string]int{}, revokedShares: map[string]bool{},
	}
	svc.SetSink(h)
	if rev != nil {
		rev.Subscribe(h.revoke)
	}
	return h
}

type conn struct {
	h         *Hub
	id        string
	ws        *websocket.Conn
	principal auth.Principal
	ctx       context.Context
	cancel    context.CancelFunc

	// public 为真表示公开分享的匿名连接（不在 conns / subs 里，无主体）。
	public bool

	mu    sync.Mutex
	queue [][]byte
	// pending 是已排队与暂存（补发期间的 sub.buf、index 发出前的 indexBuf）消息的总字节数，统一受 MaxPending 约束。
	pending int
	closed  bool
	// closing 为真时不再接受新消息，队列写完后以 closeCode 关闭（finish）。
	closing     bool
	closeCode   websocket.StatusCode
	closeReason string
	wake        chan struct{}

	// 以下由 Hub.mu 保护
	indexReady bool
	indexBuf   [][]byte
}

// sub 是连接对一个线程的订阅；补发条目期间实时事件先进 buf。由 Hub.mu 保护。
// term 为真时接收该线程的 terminal_output（term.watch）。
type sub struct {
	buffering bool
	buf       [][]byte
	term      bool
}

// ServeHTTP 处理 `GET /api/v1/ws?ticket=…`。
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := h.tickets.Redeem(r.URL.Query().Get("ticket"))
	if !ok {
		http.Error(w, `{"code":"unauthorized","message":"invalid or expired ticket"}`, http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// 来源由一次性票据保证（票据只能经已鉴权的 REST 取得）；原生客户端的 Origin 各不相同
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(MaxMessageBytes)
	ctx, cancel := context.WithCancel(context.Background())
	c := &conn{h: h, id: fmt.Sprintf("ws-%d", h.nextConn.Add(1)), ws: ws, principal: p, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	h.mu.Lock()
	h.conns[c] = struct{}{}
	rev := h.rev
	h.mu.Unlock()
	go c.writeLoop()
	defer h.drop(c)
	// 登记之后再复核主体：此后的吊销会经 revoke 关闭本连接，此前的吊销在这里被发现
	if h.valid != nil {
		if ok, err := h.valid.PrincipalValid(ctx, p); err != nil || !ok {
			c.close(websocket.StatusPolicyViolation, "revoked")
			return
		}
	}

	index, err := h.svc.Index(ctx)
	if err != nil {
		c.close(websocket.StatusInternalError, "index failed")
		return
	}
	c.send(map[string]any{"type": "index", "rev": rev, "threads": index})
	h.mu.Lock()
	c.release(c.indexBuf)
	c.indexBuf, c.indexReady = nil, true
	h.mu.Unlock()
	c.readLoop()
}

func (h *Hub) drop(c *conn) {
	c.close(websocket.StatusNormalClosure, "")
	h.mu.Lock()
	delete(h.conns, c)
	for id, m := range h.subs {
		delete(m, c)
		if len(m) == 0 {
			delete(h.subs, id)
		}
	}
	h.mu.Unlock()
	if !c.public {
		h.svc.Focus(context.Background(), c.id, "")
	}
}

func (h *Hub) revoke(kind, id string) {
	h.mu.Lock()
	var victims []*conn
	for c := range h.conns {
		if c.principal.Kind == kind && c.principal.ID == id {
			victims = append(victims, c)
		}
	}
	h.mu.Unlock()
	for _, c := range victims {
		c.close(websocket.StatusPolicyViolation, "revoked")
	}
}

// ---- 写方向 ----

func (c *conn) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("realtime: 编码消息失败", "err", err)
		return
	}
	c.enqueue(b)
}

// admit 把 n 字节计入待发预算；超过上限即断开并返回 false。
func (c *conn) admit(n int) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.pending += n
	over := c.pending > c.h.MaxPending
	c.mu.Unlock()
	if over {
		slog.Info("realtime: 慢消费者，断开", "conn", c.id)
		c.close(websocket.StatusPolicyViolation, "slow consumer")
		return false
	}
	return true
}

// enqueue 排入一条待发消息；超过待发上限即断开。
func (c *conn) enqueue(b []byte) {
	if c.admit(len(b)) {
		c.release([][]byte{b})
	}
}

// admitSoft 在待发不超过 limit（及 MaxPending）时计入预算并返回 true；否则不计入、不断开，返回 false。
func (c *conn) admitSoft(n, limit int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.pending+n > limit || c.pending+n > c.h.MaxPending {
		return false
	}
	c.pending += n
	return true
}

// stash 把消息暂存到 dst（已计入预算，之后由 release 放进发送队列或由 discard 退回）；超限即断开。
func (c *conn) stash(dst *[][]byte, b []byte) {
	if c.admit(len(b)) {
		*dst = append(*dst, b)
	}
}

// release 把已计入预算的消息放进发送队列。
func (c *conn) release(bs [][]byte) {
	if len(bs) == 0 {
		return
	}
	c.mu.Lock()
	if c.closed || c.closing {
		c.mu.Unlock()
		return
	}
	c.queue = append(c.queue, bs...)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// discard 退回已暂存但不再发送的消息的预算。
func (c *conn) discard(bs [][]byte) {
	n := 0
	for _, b := range bs {
		n += len(b)
	}
	c.mu.Lock()
	c.pending -= n
	c.mu.Unlock()
}

func (c *conn) writeLoop() {
	for {
		c.mu.Lock()
		q := c.queue
		c.queue = nil
		done := c.closing && len(q) == 0
		c.mu.Unlock()
		if done {
			c.close(c.closeCode, c.closeReason)
			return
		}
		for _, b := range q {
			wctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.close(websocket.StatusGoingAway, "write failed")
				return
			}
			c.mu.Lock()
			c.pending -= len(b)
			c.mu.Unlock()
		}
		if len(q) > 0 {
			continue
		}
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
		}
	}
}

// finish 排入最后一条消息，写完后以 code 关闭连接；此后的消息一律丢弃。
func (c *conn) finish(b []byte, code websocket.StatusCode, reason string) {
	c.mu.Lock()
	if c.closed || c.closing {
		c.mu.Unlock()
		return
	}
	c.closing, c.closeCode, c.closeReason = true, code, reason
	c.pending += len(b)
	c.queue = append(c.queue, b)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// waitClosed 等到连接关闭（writeLoop 写完 finish 的消息并完成关闭握手）。
func (c *conn) waitClosed() { <-c.ctx.Done() }

func (c *conn) close(code websocket.StatusCode, reason string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.queue = nil
	c.mu.Unlock()
	// 先完成关闭握手再取消 ctx：读写 ctx 被取消时 websocket 会不带状态码直接断开
	go func() {
		_ = c.ws.Close(code, reason)
		c.cancel()
	}()
}

// ---- 读方向 ----

type clientMsg struct {
	Type          string          `json:"type"`
	ThreadID      *string         `json:"threadId"`
	ID            string          `json:"id"`
	AfterSeq      *int64          `json:"afterSeq"`
	TranscriptRev *int64          `json:"transcriptRev"`
	Frame         json.RawMessage `json:"frame"`
	On            *bool           `json:"on"`
}

func (c *conn) readLoop() {
	for {
		_, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		var m clientMsg
		if err := json.Unmarshal(data, &m); err != nil {
			c.sendError("bad_message", err.Error(), nil, "")
			continue
		}
		c.handle(m)
	}
}

func (c *conn) sendError(code, msg string, threadID *string, id string) {
	e := map[string]any{"type": "error", "code": code, "message": msg}
	if threadID != nil && *threadID != "" {
		e["threadId"] = *threadID
	}
	if id != "" {
		e["id"] = id
	}
	c.send(e)
}

func (c *conn) fail(err error, threadID *string, id string) {
	var te *threads.Error
	if errors.As(err, &te) {
		c.sendError(te.Code, te.Message, threadID, id)
		return
	}
	slog.Warn("realtime: 处理消息失败", "conn", c.id, "err", err)
	c.sendError("internal", err.Error(), threadID, id)
}

func (c *conn) handle(m clientMsg) {
	tid := ""
	if m.ThreadID != nil {
		tid = *m.ThreadID
	}
	needThread := func() bool {
		if tid == "" {
			c.sendError("bad_message", m.Type+" requires threadId", nil, m.ID)
			return false
		}
		return true
	}
	switch m.Type {
	case "ping":
		c.send(map[string]string{"type": "pong"})
	case "focus":
		c.h.svc.Focus(c.ctx, c.id, tid)
	case "sub":
		if !needThread() {
			return
		}
		after := int64(0)
		if m.AfterSeq != nil {
			after = *m.AfterSeq
		}
		if err := c.h.subscribe(c, tid, after, m.TranscriptRev); err != nil {
			c.fail(err, m.ThreadID, "")
		}
	case "unsub":
		if !needThread() {
			return
		}
		c.h.unsubscribe(c, tid, nil)
	case "cmd":
		if !needThread() {
			return
		}
		clientID := m.ID
		err := c.h.svc.Command(c.ctx, tid, m.Frame, func(resp json.RawMessage) {
			if clientID != "" {
				if r, err := omprpc.SetID(resp, clientID); err == nil {
					resp = r
				}
			}
			c.send(map[string]any{"type": "res", "threadId": tid, "id": clientID, "response": resp})
		})
		if err != nil {
			c.fail(err, m.ThreadID, clientID)
		}
	case "ui.response":
		if !needThread() {
			return
		}
		if err := c.h.svc.UIResponse(c.ctx, tid, m.Frame); err != nil {
			c.fail(err, m.ThreadID, "")
		}
	case "term.watch":
		if !needThread() {
			return
		}
		if m.On == nil {
			c.sendError("bad_message", "term.watch requires on", m.ThreadID, "")
			return
		}
		if !c.h.setTermWatch(c, tid, *m.On) {
			c.sendError("not_subscribed", "term.watch requires sub first", m.ThreadID, "")
		}
	default:
		c.sendError("bad_message", "unknown message type "+m.Type, m.ThreadID, m.ID)
	}
}

// subscribe：快照（与订阅登记在线程锁内原子完成）→ 补发 (afterSeq, lastSeq] 的条目 → 放出缓冲的实时事件。
// 客户端的转录版本与当前不同（或缺省但 afterSeq > 0 且线程已被重写过）时从头补发并置 fullReplay（CP11）。
// 每批补发的 lastSeq 只到该批实际覆盖的 seq（CP2）：在两批之间断线的客户端以它续传，不会跳过后续条目。
func (h *Hub) subscribe(c *conn, threadID string, afterSeq int64, clientRev *int64) error {
	var snap threads.Snapshot
	var mine *sub
	after := afterSeq
	err := h.svc.Subscribe(c.ctx, threadID, func(s threads.Snapshot) {
		if (clientRev != nil && *clientRev != s.TranscriptRev) || (clientRev == nil && afterSeq > 0 && s.TranscriptRev > 0) {
			s.FullReplay, after = true, 0
		}
		snap = s
		mine = &sub{buffering: true}
		h.mu.Lock()
		m := h.subs[threadID]
		if m == nil {
			m = map[*conn]*sub{}
			h.subs[threadID] = m
		}
		if old := m[c]; old != nil {
			c.discard(old.buf)
			// 同一连接重新订阅（如 transcript.reset 后）不关掉已开启的终端观看
			mine.term = old.term
		}
		m[c] = mine
		h.mu.Unlock()
		c.send(s)
	})
	if err != nil {
		return err
	}
	for after < snap.LastSeq {
		es, err := h.svc.EntriesRange(c.ctx, threadID, after, snap.LastSeq, entriesBatch)
		if err != nil {
			h.unsubscribe(c, threadID, mine)
			return err
		}
		if len(es) == 0 {
			break
		}
		covered := es[len(es)-1].Seq
		if len(es) < entriesBatch {
			// 不满一批：(after, snap.LastSeq] 已全部取完（其余 seq 已被删除或超出快照）
			covered = snap.LastSeq
		}
		c.send(entriesMsg{Type: "entries", ThreadID: threadID, Entries: es, LeafEntryID: snap.LeafEntryID, LastSeq: covered})
		after = covered
	}
	h.mu.Lock()
	if h.subs[threadID][c] == mine {
		c.release(mine.buf)
		mine.buf, mine.buffering = nil, false
	}
	h.mu.Unlock()
	return nil
}

// unsubscribe 移除连接对线程的订阅（仅当仍是 s），并退回其暂存消息的预算。
func (h *Hub) unsubscribe(c *conn, threadID string, s *sub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.subs[threadID]
	cur := m[c]
	if cur == nil || (s != nil && cur != s) {
		return
	}
	c.discard(cur.buf)
	delete(m, c)
	if len(m) == 0 {
		delete(h.subs, threadID)
	}
}

// setTermWatch 开关连接对线程终端输出的接收；未订阅该线程时返回 false。
func (h *Hub) setTermWatch(c *conn, threadID string, on bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.subs[threadID][c]
	if s == nil {
		return false
	}
	s.term = on
	return true
}

type entriesMsg struct {
	Type        string          `json:"type"`
	ThreadID    string          `json:"threadId"`
	Entries     []threads.Entry `json:"entries"`
	LeafEntryID *string         `json:"leafEntryId,omitempty"`
	LastSeq     int64           `json:"lastSeq"`
}

// ---- threads.Sink ----

// toThread 把消息投递给线程的全部订阅者（补发中的订阅先缓冲）。
func (h *Hub) toThread(threadID string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c, s := range h.subs[threadID] {
		if s.buffering {
			c.stash(&s.buf, b)
			continue
		}
		c.enqueue(b)
	}
}

// toAll 广播给全部连接（index 尚未发出的连接先缓冲）。
func (h *Hub) toAll(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcastLocked(b)
}

func (h *Hub) broadcastLocked(b []byte) {
	for c := range h.conns {
		if !c.indexReady {
			c.stash(&c.indexBuf, b)
			continue
		}
		c.enqueue(b)
	}
}

func (h *Hub) patch(upsert []apigen.Thread, remove []string) {
	if upsert == nil {
		upsert = []apigen.Thread{}
	}
	if remove == nil {
		remove = []string{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rev++
	b, err := json.Marshal(map[string]any{"type": "index.patch", "rev": h.rev, "upsert": upsert, "remove": remove})
	if err != nil {
		return
	}
	h.broadcastLocked(b)
}

// IndexUpsert 实现 threads.Sink；状态或标题变化时匿名连接另收 share.state。
func (h *Hub) IndexUpsert(t apigen.Thread) {
	h.patch([]apigen.Thread{t}, nil)
	h.mu.Lock()
	h.publicStateLocked(t)
	h.mu.Unlock()
}

// IndexRemove 实现 threads.Sink；线程删除时它的匿名连接收到 share.revoked 后关闭。
func (h *Hub) IndexRemove(id string) {
	h.patch(nil, []string{id})
	h.mu.Lock()
	delete(h.subs, id)
	h.closeThreadPublicLocked(id)
	h.mu.Unlock()
}

// Entries 实现 threads.Sink。
func (h *Hub) Entries(threadID string, es []threads.Entry, leaf *string, lastSeq int64) {
	h.toThread(threadID, entriesMsg{Type: "entries", ThreadID: threadID, Entries: es, LeafEntryID: leaf, LastSeq: lastSeq})
	h.mu.Lock()
	if len(h.pubs[threadID]) > 0 {
		h.publicEntriesLocked(threadID, es, leaf, lastSeq)
	}
	h.mu.Unlock()
}

// Frame 实现 threads.Sink；白名单内、通过内容过滤（publicFrame）的帧另发给匿名连接（不带 threadId）。
func (h *Hub) Frame(threadID string, frame json.RawMessage) {
	h.toThread(threadID, map[string]any{"type": "frame", "threadId": threadID, "frame": frame})
	h.mu.Lock()
	if len(h.pubs[threadID]) > 0 {
		if pf, ok := publicFrame(frame); ok {
			b := make([]byte, 0, len(pf)+28)
			b = append(b, `{"type":"frame","frame":`...)
			b = append(b, pf...)
			b = append(b, '}')
			h.toPublicLocked(threadID, b)
		}
	}
	h.mu.Unlock()
}

// UIResolved 实现 threads.Sink。
func (h *Hub) UIResolved(threadID, requestID string) {
	h.toThread(threadID, map[string]string{"type": "ui.resolved", "threadId": threadID, "requestId": requestID})
}

// LiveReset 实现 threads.Sink。
func (h *Hub) LiveReset(threadID string) {
	h.toThread(threadID, map[string]string{"type": "live.reset", "threadId": threadID})
	h.mu.Lock()
	if len(h.pubs[threadID]) > 0 {
		h.toPublicLocked(threadID, marshal(apigen.WsPublicLiveReset{Type: apigen.WsPublicLiveResetTypeLiveReset}))
	}
	h.mu.Unlock()
}

// TranscriptReset 实现 threads.Sink。
func (h *Hub) TranscriptReset(threadID string, rev int64) {
	h.toThread(threadID, map[string]any{"type": "transcript.reset", "threadId": threadID, "transcriptRev": rev})
	h.mu.Lock()
	if m := h.pubs[threadID]; len(m) > 0 {
		h.toPublicLocked(threadID, marshal(apigen.WsPublicTranscriptReset{Type: apigen.WsPublicTranscriptResetTypeTranscriptReset, TranscriptRev: rev}))
		// 客户端会以新版本整段重拉：分支跟踪随之作废
		for _, ps := range m {
			ps.rev, ps.resetSent = rev, true
		}
	}
	h.mu.Unlock()
}

// PlanResolved 实现 threads.Sink。
func (h *Hub) PlanResolved(threadID string) {
	h.toThread(threadID, map[string]string{"type": "plan.resolved", "threadId": threadID})
}

// Orb 实现 threads.Sink。
func (h *Hub) Orb(o apigen.Orb) { h.toAll(map[string]any{"type": "orb", "orb": o}) }

// TermOutput 实现 threads.Sink：线上仍是普通的 `frame` 消息，但只发给开启了 term.watch 的订阅。
// 连接待发超过 TermLag 时丢弃本帧、关闭其观看并发一条 term.lagged（客户端经 terminal_snapshot 重同步后重新开启）。
func (h *Hub) TermOutput(threadID string, frame json.RawMessage) {
	// 热路径：frame 已由线程服务解析过，是一行合法 JSON，直接拼接，不再整帧重编码
	tid, err := json.Marshal(threadID)
	if err != nil {
		return
	}
	b := make([]byte, 0, len(frame)+len(tid)+40)
	b = append(b, `{"type":"frame","threadId":`...)
	b = append(b, tid...)
	b = append(b, `,"frame":`...)
	b = append(b, frame...)
	b = append(b, '}')
	var lagged []byte
	h.mu.Lock()
	defer h.mu.Unlock()
	for c, s := range h.subs[threadID] {
		if !s.term {
			continue
		}
		if c.admitSoft(len(b), h.TermLag) {
			if s.buffering {
				s.buf = append(s.buf, b)
			} else {
				c.release([][]byte{b})
			}
			continue
		}
		s.term = false
		if lagged == nil {
			lagged = termLaggedMsg(threadID)
		}
		h.deliverLocked(c, s, lagged)
	}
}

// TermLagged 实现 threads.Sink：orbd 丢弃过终端输出，通知该线程全部观看终端的连接（不改其观看开关）。
func (h *Hub) TermLagged(threadID string) {
	b := termLaggedMsg(threadID)
	h.mu.Lock()
	defer h.mu.Unlock()
	for c, s := range h.subs[threadID] {
		if s.term {
			h.deliverLocked(c, s, b)
		}
	}
}

// TermResized 实现 threads.Sink。
func (h *Hub) TermResized(threadID, terminalID string, cols, rows int) {
	h.toThread(threadID, apigen.WsServerTermResized{Type: apigen.TermResized, ThreadID: threadID, TerminalID: terminalID, Cols: cols, Rows: rows})
}

func termLaggedMsg(threadID string) []byte {
	b, _ := json.Marshal(apigen.WsServerTermLagged{Type: apigen.TermLagged, ThreadID: threadID})
	return b
}

// deliverLocked 按订阅是否在补发中把消息暂存或排入发送队列（受 MaxPending 约束）。调用方持有 h.mu。
func (h *Hub) deliverLocked(c *conn, s *sub, b []byte) {
	if s.buffering {
		c.stash(&s.buf, b)
		return
	}
	c.enqueue(b)
}
