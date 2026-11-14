package realtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/realtime"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// publicFilter 同 share 服务：子 agent 文件与合成行（标题槽、会话头）不发，隐藏类型清空内容。
func publicFilter(e threads.Entry) (threads.Entry, bool) {
	if strings.HasPrefix(e.EntryID, "__") {
		return threads.Entry{}, false
	}
	pe, ok := transcript.Public(transcript.Entry(e))
	return threads.Entry(pe), ok
}

// pubServer 起一个把 `?share=&thread=&after=&rev=` 交给 hub.ServePublic 的服务（share 服务的 WSHandler 的替身）。
func (e *env) pubServer() *httptest.Server {
	e.t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		tid := q.Get("thread")
		after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
		var rev *int64
		if v := q.Get("rev"); v != "" {
			n, _ := strconv.ParseInt(v, 10, 64)
			rev = &n
		}
		e.hub.ServePublic(w, r, realtime.PublicSub{
			ShareID: q.Get("share"), ThreadID: tid, Filter: publicFilter,
			Backfill: func(ctx context.Context, after, upTo int64) ([]threads.Entry, error) {
				var out []threads.Entry
				for {
					es, err := e.svc.EntriesRange(ctx, tid, after, upTo, 500)
					if err != nil {
						return nil, err
					}
					for _, en := range es {
						if pe, ok := publicFilter(en); ok {
							out = append(out, pe)
						}
					}
					if len(es) < 500 {
						return out, nil
					}
					after = es[len(es)-1].Seq
				}
			},
		}, after, rev)
	}))
	e.t.Cleanup(srv.Close)
	return srv
}

type pubClient struct {
	t      *testing.T
	ws     *websocket.Conn
	ch     chan msg
	closed chan error // 读循环结束时的错误
	all    []msg      // 已被 next 消费过的全部消息（只在测试 goroutine 中访问）
}

func dialPublic(t *testing.T, srv *httptest.Server, query string) (*pubClient, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/?"+query, nil)
	if err != nil {
		return nil, resp, err
	}
	ws.SetReadLimit(64 << 20)
	c := &pubClient{t: t, ws: ws, ch: make(chan msg, 10000), closed: make(chan error, 1)}
	go func() {
		defer close(c.ch)
		for {
			_, b, err := ws.Read(context.Background())
			if err != nil {
				c.closed <- err
				return
			}
			var m msg
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			m.raw = b
			c.ch <- m
		}
	}()
	t.Cleanup(func() { _ = ws.CloseNow() })
	return c, resp, nil
}

func (c *pubClient) next(pred func(msg) bool) msg {
	c.t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case m, ok := <-c.ch:
			if !ok {
				c.t.Fatal("public websocket closed")
			}
			c.all = append(c.all, m)
			if pred(m) {
				return m
			}
		case <-timeout:
			c.t.Fatal("timed out waiting for public message")
		}
	}
}

// drainClosed 读完剩余消息并返回关闭错误。
func (c *pubClient) drainClosed() error {
	c.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-c.ch:
			if !ok {
				return <-c.closed
			}
			c.all = append(c.all, m)
		case <-timeout:
			c.t.Fatal("public websocket not closed")
		}
	}
}

func (c *pubClient) send(v any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	require.NoError(c.t, c.ws.Write(context.Background(), websocket.MessageText, b))
}

// allowedPublic 是匿名连接允许出现的消息类型（N2 的验收集合）。
var allowedPublic = map[string]bool{
	"share.snapshot": true, "entries": true, "frame": true, "live.reset": true,
	"transcript.reset": true, "share.state": true, "share.revoked": true, "pong": true,
}

func (c *pubClient) assertClean() {
	c.t.Helper()
	for _, m := range c.all {
		assert.True(c.t, allowedPublic[m.Type], "匿名连接不应收到 %s", m.Type)
		assert.NotContains(c.t, string(m.raw), `"threadId"`, "匿名消息不带 threadId：%s", m.raw)
		if m.Type == "frame" {
			assert.NotEqual(c.t, "extension_ui_request", m.frameType())
		}
	}
}

// seedThread 直接在库里建一个无 orb 的线程与 n 条主会话消息（外加一条子 agent 条目）。
func (e *env) seedThread(id string, n int) {
	e.t.Helper()
	ctx := context.Background()
	require.NoError(e.t, e.st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: id, Title: "seeded", TitleSource: "auto", State: "idle", SessionDir: "/x", CreatedAt: 1, UpdatedAt: 1}))
	parent := ""
	for i := range n {
		eid := fmt.Sprintf("e%04d", i)
		var pp *string
		if parent != "" {
			pp = &parent
		}
		payload := fmt.Sprintf(`{"type":"message","id":%q,"message":{"role":"assistant","content":"line %d"}}`, eid, i)
		_, err := e.st.Q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: id, File: "s.jsonl", EntryID: eid, ParentID: pp, Type: "message", Payload: []byte(payload)})
		require.NoError(e.t, err)
		parent = eid
	}
	_, err := e.st.Q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: id, File: "s/task.jsonl", EntryID: "sub", Type: "message", Payload: []byte(`{"type":"message"}`)})
	require.NoError(e.t, err)
	require.NoError(e.t, e.st.Q.SetThreadLeaf(ctx, db.SetThreadLeafParams{LeafEntryID: &parent, ID: id}))
}

func TestPublicStream(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pub := e.pubServer()
	a := e.dial("dev_a")
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "hello there"})
	require.NoError(t, err)
	a.send(map[string]any{"type": "sub", "threadId": th.ID, "afterSeq": 0})

	// 流式中途加入：从 live 拿到在途消息
	a.next(isFrame("message_update"))
	p, _, err := dialPublic(t, pub, "share=shr_1&thread="+th.ID)
	require.NoError(t, err)
	snap := p.next(func(m msg) bool { return true })
	require.Equal(t, "share.snapshot", snap.Type)
	var s struct {
		Title string            `json:"title"`
		State string            `json:"state"`
		Live  []json.RawMessage `json:"live"`
	}
	require.NoError(t, json.Unmarshal(snap.raw, &s))
	require.NotEmpty(t, s.Live, "流式中途加入的匿名连接从 live 拿到在途帧")
	assert.Equal(t, "message_start", frameType(s.Live[0]))
	for _, f := range s.Live {
		assert.Contains(t, []string{"message_start", "message_update", "message_end", "tool_execution_start", "tool_execution_update", "tool_stream_update", "tool_execution_end"}, frameType(f))
	}
	assert.False(t, e.svc.Focused(th.ID), "匿名连接不算 focus")
	p.next(func(m msg) bool { return m.Type == "frame" && m.frameType() == "agent_end" })
	e.waitState(th.ID, threads.StateIdle)
	got, err := e.svc.Get(ctx, th.ID)
	require.NoError(t, err)
	assert.True(t, got.Unread, "只有匿名连接在看时线程仍记未读")

	// ask：私有连接作答；匿名连接看不到请求与 ui.resolved，但能看到结果条目与状态变化
	a.send(map[string]any{"type": "cmd", "threadId": th.ID, "id": "c1", "frame": map[string]any{"type": "prompt", "message": "please ask me"}})
	req := a.next(isFrame("extension_ui_request"))
	var ui struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(req.Frame, &ui))
	p.next(func(m msg) bool {
		return m.Type == "share.state" && strings.Contains(string(m.raw), `"awaiting_input"`)
	})
	a.send(map[string]any{"type": "ui.response", "threadId": th.ID, "frame": map[string]any{"type": "extension_ui_response", "id": ui.ID, "value": "beta"}})
	a.next(func(m msg) bool { return m.Type == "ui.resolved" })
	p.next(func(m msg) bool {
		if m.Type != "entries" {
			return false
		}
		for _, en := range m.Entries {
			if strings.Contains(string(en.Payload), "You picked beta") {
				return true
			}
		}
		return false
	})
	e.waitState(th.ID, threads.StateIdle)

	// 通知只到私有连接
	e.hub.Notification(apigen.Notification{ID: "ntf_1", Kind: apigen.NotificationKindDone, Title: "t", URL: "/t/x", Deliveries: []apigen.NotificationDelivery{}})
	e.hub.NotificationsRead("")
	a.next(func(m msg) bool { return m.Type == "notification" })
	a.next(func(m msg) bool { return m.Type == "notifications.read" })

	p.send(map[string]string{"type": "ping"})
	p.next(func(m msg) bool { return m.Type == "pong" })

	// 撤销：收到 share.revoked 后以 4001 关闭
	e.hub.ClosePublic("shr_1")
	err = p.drainClosed()
	assert.Equal(t, websocket.StatusCode(4001), websocket.CloseStatus(err))
	require.NotEmpty(t, p.all)
	assert.Equal(t, "share.revoked", p.all[len(p.all)-1].Type)
	p.assertClean()
	var types []string
	for _, m := range p.all {
		types = append(types, m.Type)
	}
	assert.Contains(t, types, "entries")
	assert.Contains(t, types, "frame")

	// 已撤销的分享不再接受连接
	_, resp, err := dialPublic(t, pub, "share=shr_1&thread="+th.ID)
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestPublicBackfillBatches(t *testing.T) {
	e := newEnv(t)
	pub := e.pubServer()
	const tid = "T-01932f6e-8d1a-7c3b-9f2e-000000000001"
	e.seedThread(tid, 450)

	collect := func(query string) (msg, []msg) {
		p, _, err := dialPublic(t, pub, query)
		require.NoError(t, err)
		snap := p.next(func(m msg) bool { return true })
		require.Equal(t, "share.snapshot", snap.Type)
		var batches []msg
		p.next(func(m msg) bool {
			if m.Type == "entries" {
				batches = append(batches, m)
			}
			return m.Type == "entries" && m.LastSeq == snap.LastSeq
		})
		p.assertClean()
		return snap, batches
	}
	snap, batches := collect("share=shr_a&thread=" + tid + "&after=0")
	assert.EqualValues(t, 451, snap.LastSeq)
	require.Len(t, batches, 3)
	var last int64
	var seqs []int64
	for _, b := range batches {
		assert.Greater(t, b.LastSeq, last, "补发批次的 lastSeq 单调")
		last = b.LastSeq
		assert.LessOrEqual(t, len(b.Entries), 200)
		for _, en := range b.Entries {
			assert.NotContains(t, en.File, "/")
			seqs = append(seqs, en.Seq)
		}
	}
	assert.Equal(t, []int64{200, 400, 451}, []int64{batches[0].LastSeq, batches[1].LastSeq, batches[2].LastSeq})
	assert.Len(t, seqs, 450)

	// 续传：只补 after 之后；版本不符时从头补发并置 fullReplay
	_, batches = collect("share=shr_a&thread=" + tid + "&after=440&rev=0")
	require.Len(t, batches, 1)
	assert.EqualValues(t, 441, batches[0].Entries[0].Seq)
	snap, batches = collect("share=shr_a&thread=" + tid + "&after=440&rev=7")
	assert.True(t, snap.FullReplay)
	assert.EqualValues(t, 1, batches[0].Entries[0].Seq)
}

func TestPublicLimitsAndClose(t *testing.T) {
	e := newEnv(t)
	pub := e.pubServer()
	const tid = "T-01932f6e-8d1a-7c3b-9f2e-000000000002"
	e.seedThread(tid, 3)

	var conns []*pubClient
	for range realtime.MaxPublicPerShare {
		p, _, err := dialPublic(t, pub, "share=shr_full&thread="+tid+"&after=3")
		require.NoError(t, err)
		p.next(func(m msg) bool { return m.Type == "share.snapshot" })
		conns = append(conns, p)
	}
	_, resp, err := dialPublic(t, pub, "share=shr_full&thread="+tid)
	require.Error(t, err, "第 65 条连接被拒")
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.False(t, e.svc.Focused(tid))

	// 其它分享不受该分享名额影响；名额随断开归还
	other, _, err := dialPublic(t, pub, "share=shr_other&thread="+tid+"&after=3")
	require.NoError(t, err)
	other.next(func(m msg) bool { return m.Type == "share.snapshot" })
	require.NoError(t, conns[0].ws.Close(websocket.StatusNormalClosure, ""))
	require.Eventually(t, func() bool {
		p, _, err := dialPublic(t, pub, "share=shr_full&thread="+tid+"&after=3")
		if err != nil {
			return false
		}
		_ = p.ws.CloseNow()
		return true
	}, 5*time.Second, 20*time.Millisecond)

	// 只接受 ping：其它消息即以 1008 关闭
	conns[1].send(map[string]any{"type": "cmd", "threadId": tid, "frame": map[string]any{"type": "abort"}})
	assert.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(conns[1].drainClosed()))

	// 线程删除：全部匿名连接收到 share.revoked 后断开
	require.NoError(t, e.svc.Delete(context.Background(), tid))
	for _, p := range []*pubClient{conns[2], other} {
		assert.Equal(t, websocket.StatusCode(4001), websocket.CloseStatus(p.drainClosed()))
		assert.Equal(t, "share.revoked", p.all[len(p.all)-1].Type)
		p.assertClean()
	}
	// 删除之后才来的连接：升级后立即收到 share.revoked 并断开
	late, _, err := dialPublic(t, pub, "share=shr_new&thread="+tid)
	require.NoError(t, err)
	assert.Equal(t, websocket.StatusCode(4001), websocket.CloseStatus(late.drainClosed()))
	require.Len(t, late.all, 1)
	assert.Equal(t, "share.revoked", late.all[0].Type)
}

func frameType(f json.RawMessage) string {
	var v struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(f, &v)
	return v.Type
}
