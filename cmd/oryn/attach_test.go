package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const attachThread = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"

// fakeWS 是控制面 WebSocket 的替身：每条连接交给测试逐条收发。
type fakeWS struct {
	t     *testing.T
	srv   *httptest.Server
	conns chan *fakeConn
}

type fakeConn struct {
	t    *testing.T
	c    *websocket.Conn
	recv chan map[string]any
	done chan struct{}
	once sync.Once
}

func newFakeWS(t *testing.T) *fakeWS {
	f := &fakeWS{t: t, conns: make(chan *fakeConn, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/ws-ticket", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ticket":"tk","expiresIn":30}`))
	})
	mux.HandleFunc("GET /api/v1/ws", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "tk", r.URL.Query().Get("ticket"))
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		fc := &fakeConn{t: t, c: c, recv: make(chan map[string]any, 64), done: make(chan struct{})}
		go func() {
			defer close(fc.recv)
			for {
				_, b, err := c.Read(context.Background())
				if err != nil {
					return
				}
				var m map[string]any
				_ = json.Unmarshal(b, &m)
				fc.recv <- m
			}
		}()
		f.conns <- fc
		<-fc.done
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWS) accept() *fakeConn {
	f.t.Helper()
	select {
	case c := <-f.conns:
		f.t.Cleanup(c.close)
		return c
	case <-time.After(10 * time.Second):
		f.t.Fatal("客户端没有连上")
		return nil
	}
}

func (c *fakeConn) send(v any) {
	c.t.Helper()
	b, err := json.Marshal(v)
	require.NoError(c.t, err)
	require.NoError(c.t, c.c.Write(context.Background(), websocket.MessageText, b))
}

// next 读客户端发来的下一条消息（连接已断开时返回 nil）。
func (c *fakeConn) next() map[string]any {
	c.t.Helper()
	select {
	case m := <-c.recv:
		return m
	case <-time.After(10 * time.Second):
		c.t.Fatal("等不到客户端消息")
		return nil
	}
}

func (c *fakeConn) close() {
	c.once.Do(func() {
		_ = c.c.Close(websocket.StatusNormalClosure, "")
		close(c.done)
	})
}

// expectSub 读掉连接开头的 focus 与 sub，返回 sub。
func (c *fakeConn) expectSub() map[string]any {
	c.t.Helper()
	focus := c.next()
	assert.Equal(c.t, map[string]any{"type": "focus", "threadId": attachThread}, focus)
	sub := c.next()
	assert.Equal(c.t, "sub", sub["type"])
	assert.Equal(c.t, attachThread, sub["threadId"])
	return sub
}

func snapshot(state string, lastSeq int64, extra map[string]any) map[string]any {
	m := map[string]any{
		"type": "thread.snapshot", "threadId": attachThread, "lastSeq": lastSeq, "transcriptRev": 7, "fullReplay": false,
		"thread": map[string]any{"id": attachThread, "state": state},
		"agent":  map[string]any{"status": "ready", "pendingUi": []any{}, "subagents": []any{}, "live": []any{}},
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// msgEntry 构造一条主会话 message 条目；content 为字符串或块数组。
func msgEntry(seq int64, id, parent, role string, content any, ts int64, extra map[string]any) map[string]any {
	msg := map[string]any{"role": role, "content": content, "timestamp": ts}
	for k, v := range extra {
		msg[k] = v
	}
	e := map[string]any{
		"seq": seq, "file": "s.jsonl", "entryId": id, "type": "message",
		"payload": map[string]any{"type": "message", "id": id, "message": msg},
	}
	if parent != "" {
		e["parentId"] = parent
	}
	return e
}

func entries(lastSeq int64, es ...map[string]any) map[string]any {
	return map[string]any{"type": "entries", "threadId": attachThread, "entries": es, "lastSeq": lastSeq, "leafEntryId": es[len(es)-1]["entryId"]}
}

func patch(state string) map[string]any {
	return map[string]any{"type": "index.patch", "rev": 1, "remove": []any{}, "upsert": []any{map[string]any{"id": attachThread, "state": state}}}
}

func frame(f map[string]any) map[string]any {
	return map[string]any{"type": "frame", "threadId": attachThread, "frame": f}
}

// syncBuf 是并发安全的输出缓冲；每次写入后通知 waitFor。
type syncBuf struct {
	mu   sync.Mutex
	b    bytes.Buffer
	note chan struct{}
}

func newSyncBuf() *syncBuf { return &syncBuf{note: make(chan struct{}, 1)} }

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.b.Write(p)
	select {
	case s.note <- struct{}{}:
	default:
	}
	return n, err
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuf) Bytes() []byte { return []byte(s.String()) }

// waitFor 等到输出包含 sub。
func (s *syncBuf) waitFor(t *testing.T, sub string) {
	t.Helper()
	for !strings.Contains(s.String(), sub) {
		select {
		case <-s.note:
		case <-time.After(10 * time.Second):
			t.Fatalf("输出里等不到 %q：%s", sub, s.String())
		}
	}
}

type attachRun struct {
	out, errb *syncBuf
	done      chan [2]any
	inW       *io.PipeWriter
}

func startAttach(t *testing.T, ws *fakeWS, o attachOpts, withInput bool) *attachRun {
	t.Helper()
	c := &apiClient{base: ws.srv.URL, token: "odt_test", http: ws.srv.Client()}
	r := &attachRun{out: newSyncBuf(), errb: newSyncBuf(), done: make(chan [2]any, 1)}
	o.threadID, o.out, o.errw = attachThread, r.out, r.errb
	o.dial = func(ctx context.Context) (*wsConn, error) { return dialWS(ctx, c) }
	o.backoff = func(int) time.Duration { return 0 }
	if withInput {
		pr, pw := io.Pipe()
		o.in, r.inW = pr, pw
		t.Cleanup(func() { _ = pw.Close() })
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		code, err := runAttach(ctx, o)
		r.done <- [2]any{code, err}
	}()
	return r
}

func (r *attachRun) wait(t *testing.T) (int, error) {
	t.Helper()
	select {
	case res := <-r.done:
		err, _ := res[1].(error)
		return res[0].(int), err
	case <-time.After(10 * time.Second):
		t.Fatal("attach 没有退出")
		return 0, nil
	}
}

func (r *attachRun) line(t *testing.T, s string) {
	t.Helper()
	_, err := fmt.Fprintln(r.inW, s)
	require.NoError(t, err)
}

func text(s string) []any { return []any{map[string]any{"type": "text", "text": s}} }

func TestAttachTailThenCommittedEntriesOnly(t *testing.T) {
	ws := newFakeWS(t)
	r := startAttach(t, ws, attachOpts{tail: 2, exitOnIdle: true}, false)
	c := ws.accept()
	sub := c.expectSub()
	assert.EqualValues(t, 0, sub["afterSeq"])

	c.send(map[string]any{"type": "index", "rev": 1, "threads": []any{map[string]any{"id": attachThread, "state": "idle"}}})
	c.send(snapshot("idle", 5, nil))
	c.send(entries(3,
		map[string]any{"seq": 1, "file": "s.jsonl", "entryId": "__title__", "type": "title", "payload": map[string]any{}},
		msgEntry(2, "u1", "", "user", "first question", 10, nil),
		msgEntry(3, "a1", "u1", "assistant", text("first answer"), 11, nil)))
	c.send(entries(5,
		msgEntry(4, "u2", "a1", "user", text("second question"), 12, nil),
		msgEntry(5, "a2", "u2", "assistant", []any{
			map[string]any{"type": "text", "text": "running"},
			map[string]any{"type": "toolCall", "id": "c1", "name": "bash", "arguments": map[string]any{"command": "uname -a"}},
		}, 13, nil)))
	// 已处于 idle：不立即退出，等下一个回合
	c.send(patch("idle"))
	c.send(patch("working"))
	c.send(frame(map[string]any{"type": "agent_start"}))
	// 非终端：流式帧不打印，只打已提交条目
	c.send(frame(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "timestamp": 15}}))
	c.send(frame(map[string]any{"type": "message_update", "message": map[string]any{"role": "assistant", "timestamp": 15},
		"assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "Lin"}}))
	c.send(entries(6, msgEntry(6, "t1", "a2", "toolResult", text("Linux"), 14, map[string]any{"toolName": "bash", "toolCallId": "c1"})))
	c.send(frame(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "timestamp": 15, "content": text("Linux box")}}))
	c.send(patch("idle"))
	// message_end 见过但尚未入库：等条目到达再退出
	c.send(entries(7, msgEntry(7, "a3", "t1", "assistant", text("Linux box"), 15, nil)))

	code, err := r.wait(t)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "[user] second question\n[assistant] running\n[tool bash] uname -a\n[tool bash] ok\n[assistant] Linux box\n", r.out.String())
}

func TestAttachTTYStreamsWithoutDuplicates(t *testing.T) {
	ws := newFakeWS(t)
	r := startAttach(t, ws, attachOpts{tail: 10, tty: true, exitOnIdle: true}, false)
	c := ws.accept()
	c.expectSub()
	c.send(snapshot("working", 0, nil))
	c.send(entries(1, msgEntry(1, "u1", "", "user", "hi", 100, nil)))
	c.send(frame(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "timestamp": 101}}))
	for _, d := range []string{"Hel", "lo"} {
		c.send(frame(map[string]any{"type": "message_update", "message": map[string]any{"role": "assistant", "timestamp": 101},
			"assistantMessageEvent": map[string]any{"type": "text_delta", "delta": d}}))
	}
	c.send(frame(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "timestamp": 101}}))
	c.send(entries(3,
		msgEntry(2, "a1", "u1", "assistant", []any{
			map[string]any{"type": "text", "text": "Hello"},
			map[string]any{"type": "toolCall", "id": "c1", "name": "read", "arguments": map[string]any{"path": "go.mod"}},
		}, 101, nil),
		msgEntry(3, "t1", "a1", "toolResult", text("module x"), 102, map[string]any{"toolName": "read", "isError": true})))
	c.send(patch("idle"))

	code, err := r.wait(t)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "[user] hi\n[assistant] Hello\n[tool read] go.mod\n[tool read] error\n", r.out.String())
}

func TestAttachInput(t *testing.T) {
	ws := newFakeWS(t)
	r := startAttach(t, ws, attachOpts{tail: 0}, true)
	c := ws.accept()
	c.expectSub()
	c.send(snapshot("idle", 0, nil))

	// 没有待答：一行 = steer
	r.line(t, "hello there")
	cmd := c.next()
	assert.Equal(t, "cmd", cmd["type"])
	assert.Equal(t, attachThread, cmd["threadId"])
	assert.NotEmpty(t, cmd["id"])
	assert.Equal(t, map[string]any{"type": "prompt", "message": "hello there", "streamingBehavior": "steer"}, cmd["frame"])

	// 单独 '.' = resume
	r.line(t, ".")
	resCmd := c.next()
	assert.Equal(t, map[string]any{"type": "resume"}, resCmd["frame"])

	// 有待答：一行 = ui.response（编号 → 选项）
	c.send(frame(map[string]any{"type": "extension_ui_request", "id": "q1", "method": "select", "title": "Pick one", "options": []any{"A", "B"}}))
	r.out.waitFor(t, "（输入编号选择）")
	r.line(t, "2")
	resp := c.next()
	assert.Equal(t, "ui.response", resp["type"])
	assert.Equal(t, attachThread, resp["threadId"])
	assert.Equal(t, map[string]any{"type": "extension_ui_response", "id": "q1", "value": "B"}, resp["frame"])

	c.send(map[string]any{"type": "ui.resolved", "threadId": attachThread, "requestId": "q1"})
	r.line(t, "/abort")
	abort := c.next()
	assert.Equal(t, map[string]any{"type": "abort"}, abort["frame"])
	r.line(t, "/quit")
	code, err := r.wait(t)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Contains(t, r.out.String(), "[ask] Pick one\n  1) A\n  2) B\n")
}

func TestAttachInputRejectsUnknownOption(t *testing.T) {
	ws := newFakeWS(t)
	r := startAttach(t, ws, attachOpts{tail: 0}, true)
	c := ws.accept()
	c.expectSub()
	c.send(snapshot("awaiting_input", 0, map[string]any{"agent": map[string]any{
		"status": "ready", "subagents": []any{}, "live": []any{},
		"pendingUi": []any{map[string]any{"type": "extension_ui_request", "id": "q9", "method": "select", "title": "Go?", "options": []any{"yes", "no"}}},
	}}))
	r.out.waitFor(t, "[ask] Go?")
	r.line(t, "maybe")
	r.line(t, "no")
	resp := c.next()
	assert.Equal(t, map[string]any{"type": "extension_ui_response", "id": "q9", "value": "no"}, resp["frame"], "非选项文字不作答，下一行才作答")
	r.line(t, "/quit")
	_, err := r.wait(t)
	require.NoError(t, err)
	assert.Contains(t, r.errb.String(), "请输入选项编号（1-2）")
	assert.Contains(t, r.out.String(), "[ask] Go?")
}

func TestAttachExitCodes(t *testing.T) {
	for state, want := range map[string]int{"idle": 0, "awaiting_input": exitAttachAwaiting, "error": exitAttachError} {
		t.Run(state, func(t *testing.T) {
			ws := newFakeWS(t)
			r := startAttach(t, ws, attachOpts{tail: 0, exitOnIdle: true}, false)
			c := ws.accept()
			c.expectSub()
			c.send(snapshot("working", 0, nil))
			c.send(patch(state))
			code, err := r.wait(t)
			require.NoError(t, err)
			assert.Equal(t, want, code)
		})
	}
}

func TestAttachReconnectResumes(t *testing.T) {
	ws := newFakeWS(t)
	r := startAttach(t, ws, attachOpts{tail: -1, exitOnIdle: true}, false)
	c := ws.accept()
	c.expectSub()
	c.send(snapshot("idle", 2, nil))
	c.send(entries(2, msgEntry(1, "u1", "", "user", "one", 1, nil), msgEntry(2, "a1", "u1", "assistant", text("two"), 2, nil)))
	c.close()

	// 重连：sub 带上最近的 lastSeq 与 transcriptRev；只打印新条目
	c2 := ws.accept()
	sub := c2.expectSub()
	assert.EqualValues(t, 2, sub["afterSeq"])
	assert.EqualValues(t, 7, sub["transcriptRev"])
	c2.send(snapshot("working", 3, nil))
	c2.send(entries(3, msgEntry(3, "u2", "a1", "user", "three", 3, nil)))

	// transcript.reset：清空本地条目，从 0 重订阅；已打印过的条目不重复
	c2.send(map[string]any{"type": "transcript.reset", "threadId": attachThread, "transcriptRev": 8})
	resub := c2.next()
	assert.Equal(t, map[string]any{"type": "sub", "threadId": attachThread, "afterSeq": float64(0), "transcriptRev": float64(8)}, resub)
	c2.send(snapshot("working", 4, map[string]any{"transcriptRev": 8, "fullReplay": true}))
	c2.send(entries(4, msgEntry(1, "u1", "", "user", "one", 1, nil), msgEntry(2, "a1", "u1", "assistant", text("two"), 2, nil),
		msgEntry(3, "u2", "a1", "user", "three", 3, nil), msgEntry(4, "a2", "u2", "assistant", text("four"), 4, nil)))
	c2.send(patch("idle"))

	code, err := r.wait(t)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "[user] one\n[assistant] two\n[user] three\n[assistant] four\n", r.out.String())
	assert.Contains(t, r.errb.String(), "重连中（1/5）")
}

func TestAttachJSON(t *testing.T) {
	ws := newFakeWS(t)
	r := startAttach(t, ws, attachOpts{tail: 10, json: true, exitOnIdle: true}, false)
	c := ws.accept()
	c.expectSub()
	c.send(patch("working"))
	c.send(snapshot("working", 1, nil))
	c.send(entries(1, msgEntry(1, "u1", "", "user", "hi", 1, nil)))
	c.send(frame(map[string]any{"type": "agent_start"}))
	c.send(map[string]any{"type": "ui.resolved", "threadId": attachThread, "requestId": "x"})
	// --json：计划帧与 plan.resolved 原样输出，不打印 [plan]
	c.send(frame(planFrame("P", "execute")))
	c.send(map[string]any{"type": "plan.resolved", "threadId": attachThread})
	c.send(map[string]any{"type": "frame", "threadId": "T-other", "frame": map[string]any{"type": "agent_start"}})
	c.send(patch("idle"))
	code, err := r.wait(t)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	var types []string
	for _, l := range bytes.Split(bytes.TrimSpace(r.out.Bytes()), []byte("\n")) {
		var m map[string]any
		require.NoError(t, json.Unmarshal(l, &m), string(l))
		assert.Equal(t, attachThread, m["threadId"])
		types = append(types, m["type"].(string))
	}
	assert.Equal(t, []string{"thread.snapshot", "entries", "frame", "ui.resolved", "frame", "plan.resolved"}, types)
}

// planFrame 构造 plan_proposal 帧（omp RpcPlanProposalFrame）。
func planFrame(title string, options ...string) map[string]any {
	opts := make([]any, len(options))
	for i, o := range options {
		opts[i] = o
	}
	return map[string]any{
		"type": "plan_proposal", "planFilePath": "/work/.omp/plan.md", "title": title, "suggestedFileName": "plan.md",
		"planContent": "# Plan\n- step one\n", "options": opts,
	}
}

func TestAttachPlanProposal(t *testing.T) {
	ws := newFakeWS(t)
	r := startAttach(t, ws, attachOpts{tail: 0}, true)
	c := ws.accept()
	c.expectSub()
	// 快照同时带未决 UI 请求与计划提案
	c.send(snapshot("awaiting_input", 0, map[string]any{"agent": map[string]any{
		"status": "ready", "subagents": []any{}, "live": []any{},
		"pendingUi":    []any{map[string]any{"type": "extension_ui_request", "id": "q1", "method": "select", "title": "Pick", "options": []any{"A", "B"}}},
		"planProposal": planFrame("Ship it", "execute", "compact", "keep_context", "save", "refine"),
	}}))
	r.out.waitFor(t, "其它文字作为修改意见发回")
	assert.Equal(t, "[ask] Pick\n  1) A\n  2) B\n（输入编号选择）\n"+
		"[plan] Ship it\n# Plan\n- step one\n  1) execute\n  2) compact\n  3) keep_context\n  4) save\n  5) refine\n"+
		"（输入编号；save 需写成 \"save <路径>\"，相对路径按 agent 工作目录解析；其它文字作为修改意见发回）\n", r.out.String())

	// 有未决 UI 请求时先答它
	r.line(t, "2")
	resp := c.next()
	assert.Equal(t, "ui.response", resp["type"])
	assert.Equal(t, map[string]any{"type": "extension_ui_response", "id": "q1", "value": "B"}, resp["frame"])
	c.send(map[string]any{"type": "ui.resolved", "threadId": attachThread, "requestId": "q1"})
	// 同一连接上按序处理：看到这条条目即说明 ui.resolved 已处理
	c.send(entries(1, msgEntry(1, "u1", "", "user", "marker one", 10, nil)))
	r.out.waitFor(t, "[user] marker one")

	planCmd := func(want map[string]any) {
		t.Helper()
		m := c.next()
		assert.Equal(t, "cmd", m["type"])
		assert.Equal(t, attachThread, m["threadId"])
		assert.Equal(t, want, m["frame"])
	}
	// refine 的编号、单独的 save、越界编号：提示且不发送；编号 1 → options[0]
	for _, l := range []string{"5", "refine", "4", "9", "1"} {
		r.line(t, l)
	}
	planCmd(map[string]any{"type": "plan_approval", "approved": true, "option": "execute"})
	errs := r.errb.String()
	assert.Equal(t, 2, strings.Count(errs, "请直接输入修改意见\n"), errs)
	assert.Contains(t, errs, "save 需写成 \"save <路径>\"\n")
	assert.Contains(t, errs, "请输入选项编号（1-5）或修改意见\n")

	r.line(t, "save plans/ship.md")
	planCmd(map[string]any{"type": "plan_approval", "approved": true, "option": "save", "savePath": "plans/ship.md"})
	r.line(t, "keep_context")
	planCmd(map[string]any{"type": "plan_approval", "approved": true, "option": "keep_context"})
	r.line(t, "please split step one")
	planCmd(map[string]any{"type": "plan_approval", "approved": false, "feedback": "please split step one"})

	// plan.resolved 之后输入恢复为普通消息
	c.send(map[string]any{"type": "plan.resolved", "threadId": attachThread})
	c.send(entries(2, msgEntry(2, "u2", "u1", "user", "marker two", 11, nil)))
	r.out.waitFor(t, "[user] marker two")
	r.line(t, "hello")
	planCmd(map[string]any{"type": "prompt", "message": "hello", "streamingBehavior": "steer"})
	r.line(t, "/quit")
	_, err := r.wait(t)
	require.NoError(t, err)
}

func TestAttachPlanProposalFrame(t *testing.T) {
	ws := newFakeWS(t)
	r := startAttach(t, ws, attachOpts{tail: 0, exitOnIdle: true}, false)
	c := ws.accept()
	c.expectSub()
	c.send(snapshot("working", 0, nil))
	c.send(frame(planFrame("Later", "execute", "refine")))
	// 同一提案重复到达不重复打印
	c.send(frame(planFrame("Later", "execute", "refine")))
	c.send(patch("awaiting_input"))
	code, err := r.wait(t)
	require.NoError(t, err)
	assert.Equal(t, exitAttachAwaiting, code)
	assert.Equal(t, "[plan] Later\n# Plan\n- step one\n  1) execute\n  2) refine\n（等待审批）\n", r.out.String())
}

func TestAttachPipeInputEOFKeepsWatching(t *testing.T) {
	ws := newFakeWS(t)
	r := startAttach(t, ws, attachOpts{tail: 0, exitOnIdle: true, pipeIn: true}, true)
	c := ws.accept()
	c.expectSub()
	c.send(snapshot("idle", 0, nil))
	require.NoError(t, r.inW.Close())
	r.errb.waitFor(t, "（输入已结束，继续查看；Ctrl-C 退出）")
	c.send(patch("working"))
	c.send(patch("awaiting_input"))
	code, err := r.wait(t)
	require.NoError(t, err)
	assert.Equal(t, exitAttachAwaiting, code, "管道 EOF 不退出，回合结束后按 --exit-on-idle 退出")
}

// 快照之前读到的输入行要等快照建立待答请求 / 计划状态后再处理：审批输入不能被当成普通消息（steer）发出。
func TestAttachInputWaitsForSnapshot(t *testing.T) {
	agent := func(k string, v any) map[string]any {
		return map[string]any{"status": "ready", "subagents": []any{}, "live": []any{}, "pendingUi": []any{}, k: v}
	}
	cases := map[string]struct {
		agent    map[string]any
		line     string
		wantType string
		want     map[string]any
	}{
		"plan": {agent("planProposal", planFrame("Ship it", "execute", "refine")), "execute",
			"cmd", map[string]any{"type": "plan_approval", "approved": true, "option": "execute"}},
		"ask": {agent("pendingUi", []any{map[string]any{"type": "extension_ui_request", "id": "q1", "method": "select", "title": "Pick", "options": []any{"A", "B"}}}), "2",
			"ui.response", map[string]any{"type": "extension_ui_response", "id": "q1", "value": "B"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ws := newFakeWS(t)
			r := startAttach(t, ws, attachOpts{tail: 0, pipeIn: true}, true)
			c := ws.accept()
			c.expectSub()
			// 第二行写入返回时，扫描 goroutine 已把第一行交给事件循环（交出之后才会再读管道）
			r.line(t, tc.line)
			r.line(t, "")
			c.send(snapshot("awaiting_input", 0, map[string]any{"agent": tc.agent}))
			m := c.next()
			assert.Equal(t, tc.wantType, m["type"])
			assert.Equal(t, tc.want, m["frame"])
			r.line(t, "/quit")
			_, err := r.wait(t)
			require.NoError(t, err)
		})
	}
}

// 重连后快照里恢复的计划提案在补发完成后打印；后续重连带同一提案不重复打印。
func TestAttachReconnectPrintsRestoredPlan(t *testing.T) {
	ws := newFakeWS(t)
	r := startAttach(t, ws, attachOpts{tail: 0, exitOnIdle: true}, false)
	c := ws.accept()
	c.expectSub()
	c.send(snapshot("idle", 0, nil))
	c.close()

	withPlan := map[string]any{"agent": map[string]any{"status": "ready", "subagents": []any{}, "live": []any{}, "pendingUi": []any{},
		"planProposal": planFrame("Offline plan", "execute", "refine")}}
	c2 := ws.accept()
	c2.expectSub()
	c2.send(snapshot("awaiting_input", 0, withPlan))
	c2.close()

	c3 := ws.accept()
	c3.expectSub()
	c3.send(snapshot("awaiting_input", 0, withPlan))
	c3.send(patch("working"))
	c3.send(patch("awaiting_input"))
	code, err := r.wait(t)
	require.NoError(t, err)
	assert.Equal(t, exitAttachAwaiting, code)
	assert.Equal(t, "[plan] Offline plan\n# Plan\n- step one\n  1) execute\n  2) refine\n（等待审批）\n", r.out.String())
}

// CLI attach 输入快捷对齐：忙时有队列空回车、!/!! bash、$/$$ Python eval、? btw、->/=> follow_up（不含单字母 c）。
func TestAttachInputShortcuts(t *testing.T) {
	ws := newFakeWS(t)
	r := startAttach(t, ws, attachOpts{tail: 0}, true)
	c := ws.accept()
	c.expectSub()
	// queue_update 先于快照发出：连接上的消息按序处理，而输入行要等快照处理完才执行（attacher.queued），
	// 所以 sync 执行时 queue_update 一定已被消费。反过来发时 select 可能先取到输入行，空回车看不到队列（CI 偶发）。
	c.send(map[string]any{
		"type":     "frame",
		"threadId": attachThread,
		"frame":    map[string]any{"type": "queue_update", "steering": []string{"queued-1"}, "followUp": []string{}},
	})
	c.send(snapshot("working", 0, nil))
	r.line(t, "sync")
	assert.Equal(t, map[string]any{"type": "prompt", "message": "sync", "streamingBehavior": "steer"}, c.next()["frame"])

	// 忙时有队列空回车 → abort_and_deliver_queue
	r.line(t, "")
	assert.Equal(t, map[string]any{"type": "abort_and_deliver_queue"}, c.next()["frame"])

	// ! / !! bash
	r.line(t, "!git status")
	assert.Equal(t, map[string]any{"type": "bash", "command": "git status"}, c.next()["frame"])
	r.line(t, "!!ls -la")
	assert.Equal(t, map[string]any{"type": "bash", "command": "ls -la", "excludeFromContext": true}, c.next()["frame"])

	// $ / $$ Python eval；$HOME 仍是普通 prompt
	r.line(t, "$ print(1)")
	assert.Equal(t, map[string]any{"type": "eval", "language": "python", "code": "print(1)"}, c.next()["frame"])
	r.line(t, "$$ 1+1")
	assert.Equal(t, map[string]any{"type": "eval", "language": "python", "code": "1+1", "excludeFromContext": true}, c.next()["frame"])
	r.line(t, "$HOME")
	assert.Equal(t, map[string]any{"type": "prompt", "message": "$HOME", "streamingBehavior": "steer"}, c.next()["frame"])

	// ? btw；?? 仍是普通 prompt
	r.line(t, "? 为何失败")
	assert.Equal(t, map[string]any{"type": "btw", "question": "为何失败"}, c.next()["frame"])
	r.line(t, "??")
	assert.Equal(t, map[string]any{"type": "prompt", "message": "??", "streamingBehavior": "steer"}, c.next()["frame"])

	// -> / => follow_up 队列简写
	r.line(t, "-> 下一步")
	assert.Equal(t, map[string]any{"type": "follow_up", "message": "下一步"}, c.next()["frame"])
	r.line(t, "=> 顺手查")
	assert.Equal(t, map[string]any{"type": "follow_up", "message": "顺手查"}, c.next()["frame"])

	// 单字母 c 保持普通消息，不触发 resume
	r.line(t, "c")
	assert.Equal(t, map[string]any{"type": "prompt", "message": "c", "streamingBehavior": "steer"}, c.next()["frame"])

	r.line(t, "/quit")
	code, err := r.wait(t)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
}
