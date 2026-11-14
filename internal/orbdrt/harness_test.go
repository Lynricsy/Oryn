package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/omprpc"
)

var fakeompBin string

func TestMain(m *testing.M) {
	// 受管服务测试把测试二进制自身当作服务进程（见 services_test.go 的 runServiceHelper）。
	if mode := os.Getenv("ORBD_TEST_HELPER"); mode != "" {
		runServiceHelper(mode)
		return
	}
	if exe, err := os.Executable(); err == nil {
		testBin = exe
	}
	dir, err := os.MkdirTemp("", "orbdrt-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeompBin = filepath.Join(dir, "fakeomp")
	cmd := exec.Command("go", "build", "-o", fakeompBin, "github.com/Lynricsy/Oryn/cmd/fakeomp")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build fakeomp:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

const testToken = "oot_test"

// ---- 假控制面 ----

type notif struct {
	method string
	params json.RawMessage
}

type cpFile struct {
	offset int64
	lines  []string
}

type fakeCP struct {
	t       *testing.T
	srv     *httptest.Server
	blocked atomic.Bool
	// token 是接受的 orb 令牌（缺省 testToken）。
	token atomic.Value

	mu     sync.Mutex
	sess   *link.Session
	ctl    *link.Ctl
	notifs []notif
	files  map[string]map[string]*cpFile // threadId → file → state
	hellos int
	// foreign 中的线程对 entries.* 回 foreign_thread。
	foreign map[string]bool
	// project 是本 orb 所属项目的项目脚本；nil 表示 orb 不属于任何项目（no_project）。
	project *apigen.LinkCtlProjectScripts

	agents chan *link.Stream
	blobs  chan *link.Stream
	egress *streamListener
}

func newFakeCP(t *testing.T) *fakeCP {
	cp := &fakeCP{
		t:       t,
		files:   map[string]map[string]*cpFile{},
		foreign: map[string]bool{},
		agents:  make(chan *link.Stream, 16),
		blobs:   make(chan *link.Stream, 4),
		egress:  &streamListener{ch: make(chan net.Conn, 16), done: make(chan struct{})},
	}
	cp.token.Store(testToken)
	mux := http.NewServeMux()
	mux.HandleFunc(link.Path, cp.handle)
	cp.srv = httptest.NewServer(mux)
	egressSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "egress %s %s %s", r.Method, r.URL.Path, r.Header.Get("X-Test"))
	})}
	go func() { _ = egressSrv.Serve(cp.egress) }()
	t.Cleanup(func() {
		_ = egressSrv.Close()
		cp.mu.Lock()
		if cp.sess != nil {
			_ = cp.sess.Close()
		}
		cp.mu.Unlock()
		cp.srv.CloseClientConnections()
		cp.srv.Close()
	})
	return cp
}

func (cp *fakeCP) handle(w http.ResponseWriter, r *http.Request) {
	if cp.blocked.Load() {
		http.Error(w, "blocked", http.StatusServiceUnavailable)
		return
	}
	sess, err := link.Accept(w, r, func(_ *http.Request, tok string) error {
		if want, _ := cp.token.Load().(string); tok != want {
			return errors.New("bad token")
		}
		return nil
	})
	if err != nil {
		return
	}
	defer sess.Close()
	ctx := context.Background()
	st, err := sess.AcceptStream(ctx)
	if err != nil || st.Header.T != link.StreamCtl {
		return
	}
	ctl := link.NewCtl(st, link.CtlHandlers{OnRequest: cp.onRequest, OnNotify: cp.onNotify})
	cp.mu.Lock()
	cp.sess, cp.ctl = sess, ctl
	cp.mu.Unlock()
	for {
		s, err := sess.AcceptStream(ctx)
		if err != nil {
			var he *link.HeaderError
			if errors.As(err, &he) {
				continue
			}
			return
		}
		switch s.Header.T {
		case link.StreamAgent:
			cp.agents <- s
		case link.StreamBlob:
			cp.blobs <- s
		case link.StreamEgress:
			cp.egress.ch <- s
		default:
			_ = s.Close()
		}
	}
}

func (cp *fakeCP) file(thread, file string) *cpFile {
	m := cp.files[thread]
	if m == nil {
		m = map[string]*cpFile{}
		cp.files[thread] = m
	}
	f := m[file]
	if f == nil {
		f = &cpFile{}
		m[file] = f
	}
	return f
}

func (cp *fakeCP) onRequest(_ context.Context, method string, params json.RawMessage) (any, error) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	var probe struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(params, &probe)
	if cp.foreign[probe.ThreadID] {
		return nil, &link.CtlError{Code: CodeForeignThread, Message: "thread belongs to another orb"}
	}
	switch method {
	case "entries.cursors":
		var p apigen.LinkCtlEntriesCursorsParams
		_ = json.Unmarshal(params, &p)
		res := apigen.LinkCtlEntriesCursorsResult{Cursors: []apigen.LinkCtlEntryCursor{}}
		for name, f := range cp.files[p.ThreadID] {
			res.Cursors = append(res.Cursors, apigen.LinkCtlEntryCursor{File: name, Offset: f.offset})
		}
		return res, nil
	case "entries.append":
		var p apigen.LinkCtlEntriesAppendParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		f := cp.file(p.ThreadID, p.File)
		if p.BaseOffset != f.offset {
			return apigen.LinkCtlEntriesAppendResult{Accepted: false, Offset: f.offset}, nil
		}
		f.lines = append(f.lines, p.Lines...)
		f.offset = p.EndOffset
		return apigen.LinkCtlEntriesAppendResult{Accepted: true, Offset: f.offset}, nil
	case "project.scripts.get", "project.scripts.set":
		if cp.project == nil {
			return nil, &link.CtlError{Code: "no_project", Message: "orb has no project"}
		}
		if method == "project.scripts.set" {
			var p apigen.LinkCtlProjectScriptsSetParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			if p.Setup != nil {
				cp.project.Setup = p.Setup
			}
			if p.Resume != nil {
				cp.project.Resume = p.Resume
			}
		}
		return *cp.project, nil
	}
	return nil, link.ErrMethodNotFound
}

func (cp *fakeCP) onNotify(_ context.Context, method string, params json.RawMessage) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.notifs = append(cp.notifs, notif{method, append(json.RawMessage(nil), params...)})
	switch method {
	case "hello":
		cp.hellos++
	case "entries.reset":
		var p apigen.LinkCtlEntriesResetParams
		_ = json.Unmarshal(params, &p)
		f := cp.file(p.ThreadID, p.File)
		f.offset, f.lines = 0, nil
	}
}

func (cp *fakeCP) ctlNow(t *testing.T) *link.Ctl {
	var ctl *link.Ctl
	require.Eventually(t, func() bool {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		ctl = cp.ctl
		return ctl != nil && ctl.Err() == nil
	}, 10*time.Second, 10*time.Millisecond, "no ctl connection")
	return ctl
}

func (cp *fakeCP) call(t *testing.T, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return cp.ctlNow(t).Call(ctx, method, params, result)
}

// waitNotif 等待下标 ≥ from 且满足 pred 的 method 通知，返回其下标与参数。
func (cp *fakeCP) waitNotif(t *testing.T, from int, method string, pred func(json.RawMessage) bool) (int, json.RawMessage) {
	t.Helper()
	var idx int
	var params json.RawMessage
	require.Eventually(t, func() bool {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		for i := from; i < len(cp.notifs); i++ {
			n := cp.notifs[i]
			if n.method == method && (pred == nil || pred(n.params)) {
				idx, params = i, n.params
				return true
			}
		}
		return false
	}, 20*time.Second, 10*time.Millisecond, "notification %s not received", method)
	return idx, params
}

func (cp *fakeCP) notifCount() int {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return len(cp.notifs)
}

func (cp *fakeCP) killLink() {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if cp.sess != nil {
		_ = cp.sess.Close()
	}
	cp.ctl = nil
}

// lines 返回某线程主会话文件（唯一的顶层 *.jsonl）已入库的行。
func (cp *fakeCP) mainLines(thread string) []string {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	for name, f := range cp.files[thread] {
		if !strings.Contains(name, "/") {
			return append([]string(nil), f.lines...)
		}
	}
	return nil
}

type streamListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func (l *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *streamListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *streamListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

// ---- orbd 运行时 ----

func startRuntime(t *testing.T, cp *fakeCP, mutate func(*Config)) *Runtime {
	t.Helper()
	cfg := Config{
		URL:             cp.srv.URL,
		OrbID:           "orb-test",
		Token:           testToken,
		Home:            t.TempDir(),
		AgentBin:        fakeompBin,
		EgressAddr:      "127.0.0.1:0",
		Version:         "test-orbd",
		PollInterval:    200 * time.Millisecond,
		MetricsInterval: time.Hour,
		RestartDelays:   []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond},
		InitTimeout:     20 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	rt, err := New(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rt.Run(ctx)
	}()
	t.Cleanup(func() {
		rt.stopAllAgents(5 * time.Second)
		cancel()
		<-done
	})
	return rt
}

// pushAll 在一次加锁内排入全部帧。转发器在测试推帧时已在运行：逐条 push 时它可能先写完已排的帧、
// 见队列为空且没有进程便关流（设计如此），其后的帧落到另一条流上，与「全部帧走同一条流」的断言冲突。
func (o *outbox) pushAll(items []outItem) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, it := range items {
		o.pushLocked(it)
	}
}

// ---- agent 流帧读取 ----

type frameReader struct {
	ch chan json.RawMessage
}

func readFrames(st *link.Stream) *frameReader {
	fr := &frameReader{ch: make(chan json.RawMessage, 4096)}
	go func() {
		defer close(fr.ch)
		rd := omprpc.NewReader(st, omprpc.MaxReassembledBytes+1)
		for {
			line, err := rd.ReadLine()
			if err != nil {
				return
			}
			fr.ch <- line
		}
	}()
	return fr
}

func frameType(f json.RawMessage) string {
	var e struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(f, &e)
	return e.Type
}

// until 读取帧直到出现 typ（含），超时或流结束则失败。
func (fr *frameReader) until(t *testing.T, typ string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	tm := time.NewTimer(30 * time.Second)
	defer tm.Stop()
	for {
		select {
		case f, ok := <-fr.ch:
			if !ok {
				t.Fatalf("agent stream ended before %s; got %v", typ, types(out))
			}
			out = append(out, f)
			if frameType(f) == typ {
				return out
			}
		case <-tm.C:
			t.Fatalf("timed out waiting for %s; got %v", typ, types(out))
		}
	}
}

func types(frames []json.RawMessage) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		out = append(out, frameType(f))
	}
	return out
}

// compress 合并相邻重复的类型（message_update 等流式帧）。
func compress(ts []string) []string {
	var out []string
	for _, s := range ts {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

var turnTypes = []string{
	"agent_start", "message_start", "message_update", "message_end",
	"tool_execution_start", "tool_execution_update", "tool_execution_end",
	"message_start", "message_update", "message_end",
	"agent_end", "prompt_result", "session_settled",
}

func waitAgentStream(t *testing.T, cp *fakeCP) *link.Stream {
	t.Helper()
	select {
	case st := <-cp.agents:
		return st
	case <-time.After(20 * time.Second):
		t.Fatal("no agent stream")
		return nil
	}
}

func statusIs(thread, status string) func(json.RawMessage) bool {
	return func(raw json.RawMessage) bool {
		var p apigen.LinkCtlAgentStatusParams
		_ = json.Unmarshal(raw, &p)
		return p.ThreadID == thread && string(p.Status) == status
	}
}

func initFrames(thread string) []apigen.OmpFrame {
	return []apigen.OmpFrame{
		{"type": "open_session", "sessionDir": "~/.oryn/threads/" + thread},
		{"type": "set_event_filter", "events": nil, "messageUpdates": "delta"},
		{"type": "set_ask_dialog", "enabled": true, "form": true},
		{"type": "set_subagent_subscription", "level": "progress"},
		// M3：控制面在 set_subagent_subscription 之后开启终端（每个 initFrame 都必须成功）。
		{"type": "terminal_configure", "interactiveCommands": true, "agentTools": true},
	}
}

func startAgent(t *testing.T, cp *fakeCP, thread string, env map[string]string) int {
	t.Helper()
	var res apigen.LinkCtlAgentStartResult
	require.NoError(t, cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID: thread, SessionDir: "~/.oryn/threads/" + thread, Env: env, InitFrames: initFrames(thread),
	}, &res))
	require.Positive(t, res.Pid)
	return res.Pid
}

func sendCmd(t *testing.T, st *link.Stream, cmd string) {
	t.Helper()
	_, err := st.Write([]byte(cmd + "\n"))
	require.NoError(t, err)
}

// waitEntries 等待主会话文件入库 n 行，并检查条目 id 不重复。
func waitEntries(t *testing.T, cp *fakeCP, thread string, n int) []string {
	t.Helper()
	var lines []string
	require.Eventually(t, func() bool {
		lines = cp.mainLines(thread)
		return len(lines) >= n
	}, 20*time.Second, 20*time.Millisecond, "entries not replicated")
	time.Sleep(300 * time.Millisecond) // 给可能的重复一个出现的机会
	lines = cp.mainLines(thread)
	require.Len(t, lines, n)
	ids := map[string]bool{}
	for _, l := range lines {
		var e struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(l), &e))
		if e.ID != "" {
			require.False(t, ids[e.ID], "duplicate entry %s", e.ID)
			ids[e.ID] = true
		}
	}
	return lines
}

func messageRoles(lines []string) []string {
	var roles []string
	for _, l := range lines {
		var e struct {
			Type    string `json:"type"`
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		_ = json.Unmarshal([]byte(l), &e)
		if e.Type == "message" {
			roles = append(roles, e.Message.Role)
		}
	}
	return roles
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
