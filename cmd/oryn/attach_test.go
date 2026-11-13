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
	assert.Equal(t, []string{"thread.snapshot", "entries", "frame", "ui.resolved"}, types)
}
