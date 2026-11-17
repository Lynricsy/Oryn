package sshgw

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/sshgw/sshgwtest"
)

type fakeTickets map[string]auth.Principal

func (f fakeTickets) Redeem(t string) (auth.Principal, bool) {
	p, ok := f[t]
	return p, ok
}

type termEnv struct {
	*gwEnv
	url string
}

func newTermEnv(t *testing.T) *termEnv {
	t.Helper()
	e := &termEnv{gwEnv: newGwEnv(t, sshgwtest.Options{})}
	tickets := fakeTickets{"good": {Kind: "device", ID: "dev_1"}, "good2": {Kind: "device", ID: "dev_1"}, "other": {Kind: "web", ID: "w"}}
	r := chi.NewRouter()
	r.Handle(TerminalWSPath, e.svc.TerminalHandler(tickets, nil))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	e.url = "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ssh/hosts/"
	return e
}

func (e *termEnv) dial(t *testing.T, hostID, ticket string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, e.url+hostID+"/terminal?ticket="+ticket+"&cols=80&rows=24", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

// readUntil 读二进制帧直到累计输出含 want；返回读到的全部输出。
func readUntil(t *testing.T, c *websocket.Conn, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out strings.Builder
	for !strings.Contains(out.String(), want) {
		typ, b, err := c.Read(ctx)
		require.NoError(t, err, "已读到：%q", out.String())
		require.Equal(t, websocket.MessageBinary, typ)
		out.Write(b)
	}
	return out.String()
}

// closeStatus 等连接被服务端关闭并返回关闭码。
func closeStatus(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			var ce websocket.CloseError
			require.True(t, errors.As(err, &ce), "want close error, got %v", err)
			return ce.Code
		}
	}
}

func TestTerminalEcho(t *testing.T) {
	e := newTermEnv(t)
	c := e.dial(t, e.host.ID, "good")
	ctx := context.Background()

	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":40}`)))
	require.NoError(t, c.Write(ctx, websocket.MessageBinary, []byte("echo hi\n")))
	readUntil(t, c, "hi")

	// shell 退出 → 1000
	require.NoError(t, c.Write(ctx, websocket.MessageBinary, []byte("exit 0\n")))
	require.Equal(t, websocket.StatusNormalClosure, closeStatus(t, c))

	require.Eventually(t, func() bool {
		rows := e.auditRows(t)
		return len(rows) > 0 && rows[0].Kind == "shell" && rows[0].Source == "owner" && rows[0].DurationMs != nil
	}, 5*time.Second, 20*time.Millisecond)
}

func TestTerminalRejects(t *testing.T) {
	e := newTermEnv(t)
	httpURL := "http" + strings.TrimPrefix(e.url, "ws")

	resp, err := http.Get(httpURL + e.host.ID + "/terminal?ticket=bad")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	resp, err = http.Get(httpURL + "ssh_nope/terminal?ticket=good")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// 连不上目标机 → 4502
	e.target.Close()
	c := e.dial(t, e.host.ID, "good2")
	require.Equal(t, termCloseUnreachable, closeStatus(t, c))
}

func TestTerminalRevokeAndInvalidate(t *testing.T) {
	e := newTermEnv(t)
	ctx := context.Background()

	c := e.dial(t, e.host.ID, "good")
	require.NoError(t, c.Write(ctx, websocket.MessageBinary, []byte("echo ready\n")))
	readUntil(t, c, "ready")
	other := e.dial(t, e.host.ID, "other")
	require.NoError(t, other.Write(ctx, websocket.MessageBinary, []byte("echo ready\n")))
	readUntil(t, other, "ready")

	e.svc.Revoke("device", "dev_1")
	require.Equal(t, termCloseRevoked, closeStatus(t, c))

	// 主机被改 → 4401
	_, err := e.svc.ResetHostKey(ctx, e.host.ID)
	require.NoError(t, err)
	require.Equal(t, termCloseRevoked, closeStatus(t, other))
}
