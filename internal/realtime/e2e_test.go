package realtime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/realtime"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/threads/threadstest"
)

var fakeompBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oryn-realtime-*")
	if err != nil {
		panic(err)
	}
	fakeompBin, err = threadstest.BuildFakeOmp(dir)
	if err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type tickets struct{}

func (tickets) Redeem(t string) (auth.Principal, bool) {
	if t == "bad" {
		return auth.Principal{}, false
	}
	return auth.Principal{Kind: "device", ID: t}, true
}

type revocations struct{ fn func(kind, id string) }

func (r *revocations) Subscribe(fn func(kind, id string)) { r.fn = fn }

type env struct {
	t    *testing.T
	st   *store.Store
	orbs *threadstest.Orbs
	svc  *threads.Service
	hub  *realtime.Hub
	srv  *httptest.Server
	rev  *revocations
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir+"/data")
	require.NoError(t, err)
	orbs, err := threadstest.NewOrbs(st, fakeompBin, dir+"/orbs", "FAKEOMP_DELAY_MS=40")
	require.NoError(t, err)
	svc := threads.New(threads.Config{DataDir: dir + "/data", WakeOnFocus: true}, st, orbs)
	rev := &revocations{}
	hub := realtime.New(svc, tickets{}, rev, nil)
	srv := httptest.NewServer(hub)
	t.Cleanup(func() {
		srv.Close()
		orbs.Close()
		svc.Close()
		_ = st.Close()
	})
	return &env{t: t, st: st, orbs: orbs, svc: svc, hub: hub, srv: srv, rev: rev}
}

type msg struct {
	Type      string          `json:"type"`
	ThreadID  string          `json:"threadId"`
	ID        string          `json:"id"`
	Code      string          `json:"code"`
	RequestID string          `json:"requestId"`
	Frame     json.RawMessage `json:"frame"`
	Response  json.RawMessage `json:"response"`
	Entries   []threads.Entry `json:"entries"`
	LastSeq   int64           `json:"lastSeq"`
	// term.resized
	TerminalID string `json:"terminalId"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
	// thread.snapshot / transcript.reset
	TranscriptRev int64 `json:"transcriptRev"`
	FullReplay    bool  `json:"fullReplay"`
	Upsert        []struct {
		ID    string `json:"id"`
		State string `json:"state"`
	} `json:"upsert"`
	Agent struct {
		Status    string            `json:"status"`
		Live      []json.RawMessage `json:"live"`
		PendingUI []json.RawMessage `json:"pendingUi"`
	} `json:"agent"`
	raw []byte
}

func (m msg) frameType() string {
	var f struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(m.Frame, &f)
	return f.Type
}

type client struct {
	t  *testing.T
	ws *websocket.Conn
	ch chan msg
}

func (e *env) dial(ticket string) *client {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/?ticket=" + ticket
	ws, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(e.t, err)
	ws.SetReadLimit(64 << 20)
	c := &client{t: e.t, ws: ws, ch: make(chan msg, 10000)}
	go func() {
		defer close(c.ch)
		for {
			_, b, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			var m msg
			require.NoError(e.t, json.Unmarshal(b, &m))
			m.raw = b
			c.ch <- m
		}
	}()
	e.t.Cleanup(func() { _ = ws.CloseNow() })
	first := c.next(func(m msg) bool { return true })
	require.Equal(e.t, "index", first.Type)
	return c
}

func (c *client) send(v any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	require.NoError(c.t, c.ws.Write(context.Background(), websocket.MessageText, b))
}

// next 返回第一条满足 pred 的消息（其余消息被丢弃）。
func (c *client) next(pred func(msg) bool) msg {
	c.t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case m, ok := <-c.ch:
			if !ok {
				c.t.Fatal("websocket closed")
			}
			if pred(m) {
				return m
			}
		case <-timeout:
			c.t.Fatal("timed out waiting for message")
		}
	}
}

// collect 收集消息直到 stop 返回真（含该条）。
func (c *client) collect(stop func(msg) bool) []msg {
	c.t.Helper()
	var out []msg
	c.next(func(m msg) bool {
		out = append(out, m)
		return stop(m)
	})
	return out
}

func isFrame(typ string) func(msg) bool {
	return func(m msg) bool { return m.Type == "frame" && m.frameType() == typ }
}

func (e *env) waitState(id, want string) {
	e.t.Helper()
	require.Eventually(e.t, func() bool {
		th, err := e.svc.Get(context.Background(), id)
		return err == nil && string(th.State) == want
	}, 20*time.Second, 20*time.Millisecond, "thread %s never reached %s", id, want)
}

func entryRoles(t *testing.T, es []threads.Entry) []string {
	var roles []string
	for _, e := range es {
		if e.Type != "message" {
			continue
		}
		var p struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		require.NoError(t, json.Unmarshal(e.Payload, &p))
		roles = append(roles, p.Message.Role)
	}
	return roles
}

func TestThreadEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.dial("dev_a")

	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "hello there"})
	require.NoError(t, err)
	assert.Equal(t, threads.StateProvisioning, string(th.State))
	require.NotNil(t, th.OrbID)
	a.send(map[string]any{"type": "sub", "threadId": th.ID, "afterSeq": 0})

	// A：快照 → 帧流，记录 index.patch 中的状态序列
	var states []string
	var frames []string
	var b *client
	var bSnap msg
	a.next(func(m msg) bool {
		switch m.Type {
		case "index.patch":
			for _, u := range m.Upsert {
				if u.ID == th.ID && (len(states) == 0 || states[len(states)-1] != u.State) {
					states = append(states, u.State)
				}
			}
		case "frame":
			frames = append(frames, m.frameType())
			// 流式中途加入第二个订阅者
			if m.frameType() == "message_update" && b == nil {
				b = e.dial("dev_b")
				b.send(map[string]any{"type": "sub", "threadId": th.ID})
				bSnap = b.next(func(m msg) bool { return m.Type == "thread.snapshot" })
			}
		}
		return len(states) > 0 && states[len(states)-1] == threads.StateIdle
	})
	assert.Equal(t, []string{threads.StateProvisioning, threads.StateWorking, threads.StateIdle}, states)
	assert.Equal(t, []string{"message_start", "message_update", "message_end", "tool_execution_start", "tool_execution_end", "agent_end"},
		subsequence(frames, "message_start", "message_update", "message_end", "tool_execution_start", "tool_execution_end", "agent_end"))

	require.NotNil(t, b, "second subscriber joined mid-stream")
	require.NotEmpty(t, bSnap.Agent.Live, "late subscriber gets in-flight frames")
	var first struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(bSnap.Agent.Live[0], &first))
	assert.Equal(t, "message_start", first.Type)

	e.waitState(th.ID, threads.StateIdle)
	require.Eventually(t, func() bool {
		page, err := e.svc.Entries(ctx, th.ID, 0, 500)
		require.NoError(t, err)
		roles := entryRoles(t, page.Entries)
		return len(roles) >= 4 && roles[0] == "user" && roles[len(roles)-1] == "assistant"
	}, 10*time.Second, 20*time.Millisecond)
	got, err := e.svc.Get(ctx, th.ID)
	require.NoError(t, err)
	assert.True(t, got.Unread, "转入 idle 时无连接 focus → 未读")
	a.send(map[string]any{"type": "focus", "threadId": th.ID})
	require.Eventually(t, func() bool { g, _ := e.svc.Get(ctx, th.ID); return !g.Unread }, 5*time.Second, 10*time.Millisecond)

	// 派生记录
	var usage, tools int
	require.NoError(t, e.st.DB.QueryRow(`SELECT COUNT(*) FROM usage_records WHERE thread_id = ?`, th.ID).Scan(&usage))
	require.NoError(t, e.st.DB.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE thread_id = ?`, th.ID).Scan(&tools))
	assert.Equal(t, 2, usage)
	assert.Equal(t, 1, tools)

	// 托管命令被拒
	a.send(map[string]any{"type": "cmd", "threadId": th.ID, "id": "x1", "frame": map[string]any{"type": "open_session"}})
	assert.Equal(t, "managed_by_oryn", a.next(func(m msg) bool { return m.Type == "error" && m.ID == "x1" }).Code)

	// ask：两个客户端都看到请求，先答者生效，另一端收到 ui.resolved
	a.send(map[string]any{"type": "cmd", "threadId": th.ID, "id": "c1", "frame": map[string]any{"type": "prompt", "message": "please ask me"}})
	res := a.next(func(m msg) bool { return m.Type == "res" && m.ID == "c1" })
	var resp struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
	}
	require.NoError(t, json.Unmarshal(res.Response, &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, "c1", resp.ID, "应答 id 换回客户端 id")
	reqA := a.next(isFrame("extension_ui_request"))
	reqB := b.next(isFrame("extension_ui_request"))
	var ui struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(reqA.Frame, &ui))
	assert.JSONEq(t, string(reqA.Frame), string(reqB.Frame))
	e.waitState(th.ID, threads.StateAwaitingInput)

	a.send(map[string]any{"type": "ui.response", "threadId": th.ID, "frame": map[string]any{"type": "extension_ui_response", "id": ui.ID, "value": "beta"}})
	assert.Equal(t, ui.ID, a.next(func(m msg) bool { return m.Type == "ui.resolved" }).RequestID)
	assert.Equal(t, ui.ID, b.next(func(m msg) bool { return m.Type == "ui.resolved" }).RequestID)
	b.send(map[string]any{"type": "ui.response", "threadId": th.ID, "frame": map[string]any{"type": "extension_ui_response", "id": ui.ID, "value": "gamma"}})
	assert.Equal(t, "ui_resolved", b.next(func(m msg) bool { return m.Type == "error" }).Code)

	// 等回合结束且条目里出现所选值
	var lastSeq int64
	a.next(func(m msg) bool {
		if m.Type == "entries" {
			lastSeq = m.LastSeq
			for _, en := range m.Entries {
				if strings.Contains(string(en.Payload), "You picked beta") && strings.Contains(string(en.Payload), `"stopReason":"toolUse"`) {
					return true
				}
			}
		}
		return false
	})
	a.next(isFrame("session_settled"))
	e.waitState(th.ID, threads.StateIdle)
	time.Sleep(200 * time.Millisecond) // 让最后几行入库
	page, err := e.svc.Entries(ctx, th.ID, 0, 500)
	require.NoError(t, err)
	lastSeq = page.LastSeq

	// 重连：afterSeq 之后无重复
	c := e.dial("dev_c")
	c.send(map[string]any{"type": "sub", "threadId": th.ID, "afterSeq": lastSeq - 2})
	snap := c.next(func(m msg) bool { return m.Type == "thread.snapshot" })
	assert.Equal(t, lastSeq, snap.LastSeq)
	ent := c.next(func(m msg) bool { return m.Type == "entries" })
	require.Len(t, ent.Entries, 2)
	assert.Equal(t, lastSeq-1, ent.Entries[0].Seq)
	assert.Equal(t, lastSeq, ent.Entries[1].Seq)

	d := e.dial("dev_d")
	d.send(map[string]any{"type": "sub", "threadId": th.ID, "afterSeq": 0})
	seen := map[int64]bool{}
	d.next(func(m msg) bool {
		if m.Type == "entries" {
			for _, en := range m.Entries {
				assert.False(t, seen[en.Seq], "duplicate seq %d", en.Seq)
				seen[en.Seq] = true
			}
		}
		return int64(len(seen)) == lastSeq
	})
	var n, distinct int
	require.NoError(t, e.st.DB.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT file || entry_id) FROM entries WHERE thread_id = ?`, th.ID).Scan(&n, &distinct))
	assert.Equal(t, n, distinct)
	assert.EqualValues(t, lastSeq, n)

	// 吊销：关闭对应主体的连接
	e.rev.fn("device", "dev_d")
	for range d.ch {
	}
}

func TestBadTicket(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/?ticket=bad", nil)
	require.Error(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

func TestForkCopy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	e.waitState(th.ID, threads.StateIdle)
	require.Eventually(t, func() bool { g, _ := e.svc.Get(ctx, th.ID); return g.LeafEntryID != nil }, 5*time.Second, 10*time.Millisecond)

	child, err := e.svc.Fork(ctx, th.ID, nil, "copy")
	require.NoError(t, err)
	require.NotNil(t, child.ParentThreadID)
	assert.Equal(t, th.ID, *child.ParentThreadID)
	require.Eventually(t, func() bool {
		g, _ := e.svc.Get(ctx, child.ID)
		return g.OrbID != nil && *g.OrbID != *th.OrbID && g.State == threads.StateIdle
	}, 20*time.Second, 20*time.Millisecond)
	assert.Equal(t, []string{*th.OrbID}, e.orbs.Snapshots())
	// 副本会话目录里的源条目被复制到新线程
	require.Eventually(t, func() bool {
		page, _ := e.svc.Entries(ctx, child.ID, 0, 500)
		return len(entryRoles(t, page.Entries)) >= 4
	}, 10*time.Second, 20*time.Millisecond)
	assert.Contains(t, e.orbs.Calls(), "fs.copyTree")
}

func TestForkShareRequiresAgentSupport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	e.waitState(th.ID, threads.StateIdle)
	require.Eventually(t, func() bool { g, _ := e.svc.Get(ctx, th.ID); return g.LeafEntryID != nil }, 5*time.Second, 10*time.Millisecond)
	// fakeomp 不实现 fork_from：错误原样返回，不建新线程
	_, err = e.svc.Fork(ctx, th.ID, nil, "share")
	var te *threads.Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "agent_error", te.Code)
	list, err := e.svc.List(ctx, threads.ListFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestBlobFetchedFromOrb(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	data := []byte("\x89PNG\r\n\x1a\nblob body")
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	e.orbs.AddBlob(sha, data)
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "look at blob:sha256:" + sha})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, path, err := e.svc.Blob(ctx, sha)
		if err != nil {
			return false
		}
		b, err := os.ReadFile(path)
		return err == nil && string(b) == string(data)
	}, 20*time.Second, 20*time.Millisecond)
	b, _, err := e.svc.Blob(ctx, sha)
	require.NoError(t, err)
	assert.Equal(t, "image/png", b.Mime)
	e.waitState(th.ID, threads.StateIdle)
}

func TestForeignThreadKicksLink(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	t1, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "one"})
	require.NoError(t, err)
	t2, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "two"})
	require.NoError(t, err)
	e.waitState(t1.ID, threads.StateIdle)
	e.waitState(t2.ID, threads.StateIdle)
	ctl := e.orbs.OrbdCtl(*t1.OrbID)
	require.NotNil(t, ctl)
	err = ctl.Call(ctx, "entries.cursors", map[string]string{"threadId": t2.ID}, nil)
	require.Error(t, err)
	select {
	case <-ctl.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("link not closed")
	}
}

func TestSlowConsumerDisconnected(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	defer st.Close()
	orbs, err := threadstest.NewOrbs(st, fakeompBin, dir+"/orbs")
	require.NoError(t, err)
	defer orbs.Close()
	svc := threads.New(threads.Config{DataDir: dir}, st, orbs)
	defer svc.Close()
	hub := realtime.New(svc, tickets{}, nil, nil)
	hub.MaxPending = 8
	srv := httptest.NewServer(hub)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/?ticket=x", nil)
	require.NoError(t, err)
	_, _, err = ws.Read(ctx)
	require.Error(t, err)
	assert.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err))
}

func TestQueueFullWhileOrbAway(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	e.waitState(th.ID, threads.StateIdle)
	// 断开 link：命令进入排队
	e.orbs.Kick(*th.OrbID, "test")
	require.Eventually(t, func() bool { _, ok := e.orbs.Ctl(*th.OrbID); return !ok }, 5*time.Second, 10*time.Millisecond)
	frame := json.RawMessage(`{"type":"get_state"}`)
	var lastErr error
	for i := 0; i < 200 && lastErr == nil; i++ {
		lastErr = e.svc.Command(ctx, th.ID, frame, func(json.RawMessage) {})
	}
	var te *threads.Error
	require.ErrorAs(t, lastErr, &te)
	assert.Equal(t, "queue_full", te.Code)
}

func subsequence(xs []string, want ...string) []string {
	var out []string
	i := 0
	for _, x := range xs {
		if i < len(want) && x == want[i] {
			out = append(out, x)
			i++
		}
	}
	return out
}

// ---- M2.6 审查修复 ----

// extraLines 构造 n 条主会话消息行。
func extraLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf(`{"type":"message","id":"x%04d","parentId":null,"message":{"role":"user","content":"x","timestamp":%d}}`, i, i+1)
	}
	return lines
}

func appendViaOrbd(t *testing.T, ctl interface {
	Call(ctx context.Context, method string, params, result any) error
}, threadID, file string, base int64, lines []string) int64 {
	t.Helper()
	end := base
	for _, l := range lines {
		end += int64(len(l) + 1)
	}
	var res struct {
		Accepted bool `json:"accepted"`
	}
	require.NoError(t, ctl.Call(context.Background(), "entries.append", map[string]any{
		"threadId": threadID, "file": file, "baseOffset": base, "endOffset": end, "lines": lines,
	}, &res))
	require.True(t, res.Accepted)
	return end
}

// CP2：补发分批时每批的 lastSeq 只到该批覆盖的 seq；在两批之间断线后以它续传，拿到其余全部条目，无缺口。
// CP11：客户端的 transcriptRev 与当前不同即从头补发（fullReplay）；reset_done 广播 transcript.reset 并递增版本。
func TestCatchUpBatchCursorAndTranscriptRev(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	e.waitState(th.ID, threads.StateIdle)
	ctl := e.orbs.OrbdCtl(*th.OrbID)
	require.NotNil(t, ctl)
	lines := extraLines(450)
	appendViaOrbd(t, ctl, th.ID, "extra.jsonl", 0, lines)
	page, err := e.svc.Entries(ctx, th.ID, 0, 1)
	require.NoError(t, err)
	total := page.LastSeq
	require.Greater(t, total, int64(400))

	a := e.dial("dev_a")
	a.send(map[string]any{"type": "sub", "threadId": th.ID, "afterSeq": 0})
	snap := a.next(func(m msg) bool { return m.Type == "thread.snapshot" })
	assert.Equal(t, total, snap.LastSeq)
	assert.False(t, snap.FullReplay)
	first := a.next(func(m msg) bool { return m.Type == "entries" })
	require.Len(t, first.Entries, 200)
	assert.Equal(t, first.Entries[199].Seq, first.LastSeq, "首批只覆盖到自己的最后一条")
	require.Less(t, first.LastSeq, total)
	_ = a.ws.CloseNow() // 两批之间断线

	b := e.dial("dev_b")
	b.send(map[string]any{"type": "sub", "threadId": th.ID, "afterSeq": first.LastSeq, "transcriptRev": snap.TranscriptRev})
	bSnap := b.next(func(m msg) bool { return m.Type == "thread.snapshot" })
	assert.False(t, bSnap.FullReplay)
	next := first.LastSeq + 1
	var last int64
	b.next(func(m msg) bool {
		if m.Type != "entries" {
			return false
		}
		for _, en := range m.Entries {
			require.Equal(t, next, en.Seq, "续传无缺口无重复")
			next++
		}
		last = m.LastSeq
		return last == total
	})
	assert.Equal(t, total+1, next)

	// 版本不符：从头补发
	c := e.dial("dev_c")
	c.send(map[string]any{"type": "sub", "threadId": th.ID, "afterSeq": total, "transcriptRev": 7})
	cSnap := c.next(func(m msg) bool { return m.Type == "thread.snapshot" })
	assert.True(t, cSnap.FullReplay)
	assert.Equal(t, int64(1), c.next(func(m msg) bool { return m.Type == "entries" }).Entries[0].Seq)

	// 会话文件重写：只重发前 10 行，其余被删除 → transcript.reset{rev 1}
	require.NoError(t, ctl.Notify("entries.reset", map[string]string{"threadId": th.ID, "file": "extra.jsonl"}))
	appendViaOrbd(t, ctl, th.ID, "extra.jsonl", 0, lines[:10])
	require.NoError(t, ctl.Notify("entries.reset_done", map[string]string{"threadId": th.ID, "file": "extra.jsonl"}))
	reset := c.next(func(m msg) bool { return m.Type == "transcript.reset" })
	assert.Equal(t, int64(1), reset.TranscriptRev)
	// 缺省版本但 afterSeq > 0 且线程已被重写过：同样从头补发
	d := e.dial("dev_d")
	d.send(map[string]any{"type": "sub", "threadId": th.ID, "afterSeq": total})
	dSnap := d.next(func(m msg) bool { return m.Type == "thread.snapshot" })
	assert.True(t, dSnap.FullReplay)
	assert.Equal(t, int64(1), dSnap.TranscriptRev)
}

// M5：orb 主动打开、没有对应 blob.fetch 的 blob 流被拒，内容不落盘。
func TestUnsolicitedBlobRejected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	e.waitState(th.ID, threads.StateIdle)
	data := []byte("unsolicited payload")
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	require.NoError(t, e.orbs.OpenBlob(ctx, *th.OrbID, sha, data))
	_, _, err = e.svc.Blob(ctx, sha)
	var te *threads.Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "not_found", te.Code)
}

type denyAll struct{}

func (denyAll) PrincipalValid(context.Context, auth.Principal) (bool, error) { return false, nil }

// H3：票据兑换后、连接登记后复核主体；已吊销的主体拿不到连接。
func TestRevokedPrincipalTicketRefused(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	defer st.Close()
	orbs, err := threadstest.NewOrbs(st, fakeompBin, dir+"/orbs")
	require.NoError(t, err)
	defer orbs.Close()
	svc := threads.New(threads.Config{DataDir: dir}, st, orbs)
	defer svc.Close()
	srv := httptest.NewServer(realtime.New(svc, tickets{}, nil, denyAll{}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/?ticket=dev_gone", nil)
	require.NoError(t, err)
	_, _, err = ws.Read(ctx)
	require.Error(t, err)
	assert.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err))
}

// CP5：fork copy 的阶段持久化。orb 已绑定、会话目录尚未复制时重启 → 新服务复制目录后再启动 agent。
func TestForkCopyResumesAfterRestartBeforeCopy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	e.waitState(th.ID, threads.StateIdle)
	require.Eventually(t, func() bool { g, _ := e.svc.Get(ctx, th.ID); return g.LeafEntryID != nil }, 5*time.Second, 10*time.Millisecond)

	e.orbs.Hold(true)
	child, err := e.svc.Fork(ctx, th.ID, nil, "copy")
	require.NoError(t, err)
	var childOrb string
	require.Eventually(t, func() bool {
		g, _ := e.svc.Get(ctx, child.ID)
		if g.OrbID == nil {
			return false
		}
		childOrb = *g.OrbID
		return true
	}, 10*time.Second, 10*time.Millisecond, "副本 orb 已绑定")

	svc2 := restartThreads(t, e)
	e.orbs.Hold(false)
	e.orbs.Release(childOrb)
	require.Eventually(t, func() bool {
		g, _ := svc2.Get(ctx, child.ID)
		page, _ := svc2.Entries(ctx, child.ID, 0, 500)
		return g.State == threads.StateIdle && len(entryRoles(t, page.Entries)) >= 4
	}, 20*time.Second, 20*time.Millisecond, "重启后复制了源会话目录")
}

// CP5：快照进行中重启 → 新服务从快照阶段续作（建 orb、复制、启动）。
func TestForkCopyResumesAfterRestartDuringSnapshot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	e.waitState(th.ID, threads.StateIdle)
	require.Eventually(t, func() bool { g, _ := e.svc.Get(ctx, th.ID); return g.LeafEntryID != nil }, 5*time.Second, 10*time.Millisecond)

	e.orbs.GateSnapshots(true)
	child, err := e.svc.Fork(ctx, th.ID, nil, "copy")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(e.orbs.Snapshots()) == 1 }, 5*time.Second, 10*time.Millisecond)
	e.orbs.GateSnapshots(false)
	svc2 := restartThreads(t, e)
	require.Eventually(t, func() bool {
		g, _ := svc2.Get(ctx, child.ID)
		page, _ := svc2.Entries(ctx, child.ID, 0, 500)
		return g.OrbID != nil && *g.OrbID != *th.OrbID && g.State == threads.StateIdle && len(entryRoles(t, page.Entries)) >= 4
	}, 20*time.Second, 20*time.Millisecond, "重启后从快照阶段续作")
	assert.Len(t, e.orbs.Snapshots(), 2, "快照重做一次")
}

// CP5：orb 尚未就绪时重启，已受理的首条提示词照样执行。
func TestStartupPromptRunsAfterRestart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.orbs.Hold(true)
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "durable hello"})
	require.NoError(t, err)
	svc2 := restartThreads(t, e)
	e.orbs.Hold(false)
	e.orbs.Release(*th.OrbID)
	require.Eventually(t, func() bool {
		page, _ := svc2.Entries(ctx, th.ID, 0, 500)
		for _, en := range page.Entries {
			if strings.Contains(string(en.Payload), "durable hello") {
				return true
			}
		}
		return false
	}, 20*time.Second, 20*time.Millisecond, "重启后首条提示词被执行")
}

// restartThreads 关闭旧线程服务，在同一数据库与同一组 orb 上构造新服务（模拟控制面重启）。
func restartThreads(t *testing.T, e *env) *threads.Service {
	t.Helper()
	e.svc.Close()
	e.orbs.Rebind()
	svc := threads.New(threads.Config{DataDir: t.TempDir(), WakeOnFocus: true}, e.st, e.orbs)
	t.Cleanup(svc.Close)
	return svc
}
