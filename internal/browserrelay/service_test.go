package browserrelay

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

type fakeTickets struct {
	mu sync.Mutex
	m  map[string]auth.Principal
}

func (f *fakeTickets) issue(tok string, p auth.Principal) {
	f.mu.Lock()
	if f.m == nil {
		f.m = map[string]auth.Principal{}
	}
	f.m[tok] = p
	f.mu.Unlock()
}

func (f *fakeTickets) Redeem(tok string) (auth.Principal, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.m[tok]
	delete(f.m, tok)
	return p, ok
}

type fakeOrbDialer struct {
	mu        sync.Mutex
	connected map[string]bool
	features  map[string][]string
	streams   chan net.Conn
}

func (f *fakeOrbDialer) Connected(orbID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected[orbID]
}

func (f *fakeOrbDialer) HasFeature(orbID, feature string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.features[orbID] {
		if x == feature {
			return true
		}
	}
	return false
}

func (f *fakeOrbDialer) DialBrowserRelay(_ context.Context, _, _ string) (net.Conn, error) {
	a, b := net.Pipe()
	f.streams <- b
	return a, nil
}

type deliveredMsg struct {
	threadID string
	msg      threads.AutoMessage
}

type fakeDeliverer struct {
	ch chan deliveredMsg
}

func (f *fakeDeliverer) Deliver(_ context.Context, threadID string, m threads.AutoMessage, _ func(error)) (func() bool, error) {
	f.ch <- deliveredMsg{threadID: threadID, msg: m}
	return func() bool { return false }, nil
}

type fakeBroadcaster struct {
	mu      sync.Mutex
	states  []apigen.ThreadBrowserRelay
	clients [][]apigen.BrowserRelayClient
}

func (f *fakeBroadcaster) BrowserRelayState(st apigen.ThreadBrowserRelay) {
	f.mu.Lock()
	f.states = append(f.states, st)
	f.mu.Unlock()
}

func (f *fakeBroadcaster) BrowserRelayClients(cs []apigen.BrowserRelayClient) {
	f.mu.Lock()
	f.clients = append(f.clients, cs)
	f.mu.Unlock()
}

func newTestEnv(t *testing.T) (*Service, *store.Store, *fakeTickets, *fakeOrbDialer, *fakeDeliverer, *fakeBroadcaster, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	tk := &fakeTickets{}
	orb := &fakeOrbDialer{
		connected: map[string]bool{"orb_1": true},
		features:  map[string][]string{"orb_1": {orbs.FeatureBrowserRelay}},
		streams:   make(chan net.Conn, 8),
	}
	deliv := &fakeDeliverer{ch: make(chan deliveredMsg, 8)}
	hub := &fakeBroadcaster{}

	svc := New(Config{Hub: hub, Deliver: deliv}, st, orb, tk)
	t.Cleanup(svc.Close)

	ts := httptest.NewServer(svc.WSHandler())
	t.Cleanup(ts.Close)

	// 预置一个 browser_extension 设备、一个普通 cli 设备与一个 work 线程
	require.NoError(t, st.Q.CreateDevice(ctx, db.CreateDeviceParams{
		ID: "dev_ext", Name: "Chrome Work", Kind: "browser_extension", Platform: "chrome", TokenHash: "h_ext", CreatedAt: 1,
	}))
	require.NoError(t, st.Q.CreateDevice(ctx, db.CreateDeviceParams{
		ID: "dev_cli", Name: "CLI", Kind: "cli", Platform: "linux", TokenHash: "h_cli", CreatedAt: 1,
	}))
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{
		ID: "prv_1", Name: "p", TokenHash: "hp", CreatedAt: 1,
	}))
	require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{
		ID: "orb_1", ProviderID: "prv_1", Type: "default", Cpus: 2, MemoryMb: 1024, Image: "img",
		State: "ready", StateChangedAt: 1, TokenHash: "ho", CreatedAt: 1,
	}))
	orbID := "orb_1"
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{
		Kind: "work", ID: "thr_1", OrbID: &orbID, Title: "测试线程", TitleSource: "auto", State: "idle", SessionDir: "/s", CreatedAt: 1, UpdatedAt: 1,
	}))
	return svc, st, tk, orb, deliv, hub, ts
}

func dialTestWS(t *testing.T, ts *httptest.Server, ticket string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "?ticket=" + ticket
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, u, nil)
}

func readWSJSON(t *testing.T, ws *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := ws.Read(ctx)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func writeWSJSON(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, ws.Write(ctx, websocket.MessageText, b))
}

func TestWSRejectsNonExtensionAndInvalidTicket(t *testing.T) {
	_, _, tk, _, _, _, ts := newTestEnv(t)

	_, resp, err := dialTestWS(t, ts, "bad")
	require.Error(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// 普通 CLI 设备令牌不能连 browser-relay WS
	tk.issue("t_cli", auth.Principal{Kind: auth.KindDevice, ID: "dev_cli"})
	_, resp, err = dialTestWS(t, ts, "t_cli")
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// 网页会话不能连 browser-relay WS
	tk.issue("t_web", auth.Principal{Kind: auth.KindWeb, ID: "web_1"})
	_, resp, err = dialTestWS(t, ts, "t_web")
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestBindBridgeBidirectionalAndApprovalDelivery(t *testing.T) {
	ctx := context.Background()
	svc, _, tk, orb, deliv, _, ts := newTestEnv(t)

	tk.issue("t_ext", auth.Principal{Kind: auth.KindDevice, ID: "dev_ext"})
	ws, _, err := dialTestWS(t, ts, "t_ext")
	require.NoError(t, err)
	defer ws.CloseNow()

	writeWSJSON(t, ws, map[string]any{
		"t":              "register",
		"instanceId":     "inst_1",
		"browserSession": "sess_1",
		"browserVersion": "Chrome/136.0.0.0",
		"extVersion":     "0.2.0",
	})
	reg := readWSJSON(t, ws)
	assert.Equal(t, "registered", reg["t"])
	assert.Equal(t, "dev_ext", reg["deviceId"])

	// 绑定线程 → 控制面打开 orb 流并给扩展发 session.open
	st, err := svc.BindThread(ctx, "thr_1", "dev_ext")
	require.NoError(t, err)
	assert.True(t, st.Bound)
	assert.True(t, st.Online)

	openMsg := readWSJSON(t, ws)
	assert.Equal(t, "session.open", openMsg["t"])
	assert.Equal(t, "thr_1", openMsg["threadId"])

	orbConn := <-orb.streams
	defer orbConn.Close()

	// 扩展 → orb 方向帧转发
	writeWSJSON(t, ws, map[string]any{
		"t":        "session.frame",
		"threadId": "thr_1",
		"payload":  map[string]any{"t": "hello", "tabs": []any{}},
	})
	line, err := bufio.NewReader(orbConn).ReadString('\n')
	require.NoError(t, err)
	assert.Contains(t, line, `"t":"hello"`)

	// orb → 扩展 方向帧转发
	_, err = orbConn.Write([]byte(`{"t":"rpc","id":7,"op":"createTab","url":"https://example.com"}` + "\n"))
	require.NoError(t, err)
	down := readWSJSON(t, ws)
	assert.Equal(t, "session.frame", down["t"])
	assert.Equal(t, "thr_1", down["threadId"])
	payload, ok := down["payload"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "createTab", payload["op"])

	// 扩展上报待审批与后续批准决议 → 校验 state 更新与向线程投递重试通知
	writeWSJSON(t, ws, map[string]any{
		"t":                "session.state",
		"threadId":         "thr_1",
		"sharedTabCount":   1,
		"pendingApprovals": 1,
	})
	require.Eventually(t, func() bool {
		cur, err := svc.GetThreadState(ctx, "thr_1")
		return err == nil && cur.SharedTabCount == 1 && cur.PendingApprovals == 1
	}, 2*time.Second, 20*time.Millisecond)

	writeWSJSON(t, ws, map[string]any{
		"t":          "session.approval_resolved",
		"threadId":   "thr_1",
		"approvalId": "apr_1",
		"decision":   "approved_once",
		"summary":    "新建标签页 https://example.com",
	})
	select {
	case d := <-deliv.ch:
		assert.Equal(t, "thr_1", d.threadID)
		assert.True(t, d.msg.Steer)
		assert.Contains(t, d.msg.Text, "批准")
		assert.Contains(t, d.msg.Text, "重试")
	case <-time.After(2 * time.Second):
		t.Fatal("expected approval resolution message delivered to thread")
	}

	// 吊销设备 → 扩展 WS 以 4401 断开、绑定被清除
	svc.Revoke(auth.KindDevice, "dev_ext")
	rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
	defer rcancel()
	_, _, err = ws.Read(rctx)
	require.Error(t, err)
	assert.Equal(t, closeRevoked, websocket.CloseStatus(err))

	after, err := svc.GetThreadState(ctx, "thr_1")
	require.NoError(t, err)
	assert.False(t, after.Bound)
	assert.False(t, after.Online)
}

func TestOrbLinkUpRestoresBridge(t *testing.T) {
	ctx := context.Background()
	svc, _, tk, orb, _, _, ts := newTestEnv(t)

	// 初始 orb 离线
	orb.mu.Lock()
	orb.connected["orb_1"] = false
	orb.mu.Unlock()

	tk.issue("t_ext", auth.Principal{Kind: auth.KindDevice, ID: "dev_ext"})
	ws, _, err := dialTestWS(t, ts, "t_ext")
	require.NoError(t, err)
	defer ws.CloseNow()

	writeWSJSON(t, ws, map[string]any{"t": "register"})
	_ = readWSJSON(t, ws)

	_, err = svc.BindThread(ctx, "thr_1", "dev_ext")
	require.NoError(t, err)
	assert.Empty(t, orb.streams)

	// orb 重连并触发 EventLinkUp → 自动建桥接并向扩展发 session.open
	orb.mu.Lock()
	orb.connected["orb_1"] = true
	orb.mu.Unlock()
	svc.OnOrbEvent(orbs.Event{Kind: orbs.EventLinkUp, OrbID: "orb_1"})

	openMsg := readWSJSON(t, ws)
	assert.Equal(t, "session.open", openMsg["t"])
	select {
	case c := <-orb.streams:
		_ = c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("expected bridge stream opened after EventLinkUp")
	}
}
