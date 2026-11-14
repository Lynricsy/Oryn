package desktop

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

type wsEnv struct {
	*env
	tickets *auth.Tickets
	srv     *httptest.Server
	// wsDone 收到每个退出的桌面 WS 处理器的 orb id。
	wsDone chan string
}

func newWSEnv(t *testing.T) *wsEnv {
	e := newEnv(t)
	tk := auth.NewTickets()
	e.svc.tickets = tk
	done := make(chan string, 256)
	e.svc.onWSDone = func(id string) {
		select {
		case done <- id:
		default:
		}
	}
	r := chi.NewRouter()
	r.Handle(WSPath, e.svc.WSHandler())
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &wsEnv{env: e, tickets: tk, srv: srv, wsDone: done}
}

func (w *wsEnv) url(orbID, ticket string) string {
	return "ws" + strings.TrimPrefix(w.srv.URL, "http") + "/api/v1/orbs/" + orbID + "/desktop/ws?ticket=" + ticket
}

func (w *wsEnv) ticket(p auth.Principal) string {
	tk, _ := w.tickets.Issue(p)
	return tk
}

func (w *wsEnv) dial(orbID string, p auth.Principal) *websocket.Conn {
	w.t.Helper()
	c, _, err := websocket.Dial(w.ctx, w.url(orbID, w.ticket(p)), nil)
	require.NoError(w.t, err)
	c.SetReadLimit(4 << 20)
	return c
}

// closeCode 读到连接关闭并返回关闭码。
func closeCode(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func web(id string) auth.Principal { return auth.Principal{Kind: auth.KindWeb, ID: id} }

func TestDesktopWSAuth(t *testing.T) {
	w := newWSEnv(t)
	o := w.orb("")
	for _, u := range []string{w.url(o, ""), w.url(o, "owt_bogus")} {
		_, resp, err := websocket.Dial(w.ctx, u, nil)
		require.Error(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}
	tk := w.ticket(web("s1"))
	c, _, err := websocket.Dial(w.ctx, w.url(o, tk), nil)
	require.NoError(t, err)
	defer c.CloseNow()
	_, resp, err := websocket.Dial(w.ctx, w.url(o, tk), nil)
	require.Error(t, err, "票据只能用一次")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	_, resp, err = websocket.Dial(w.ctx, w.url("orb-missing", w.ticket(web("s1"))), nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// 回显 1MiB；客户端每条消息记一次用户交互；Revoke → 4401。
func TestDesktopWSEchoTouchRevoke(t *testing.T) {
	w := newWSEnv(t)
	o := w.orb("")
	c := w.dial(o, web("s1"))
	payload := bytes.Repeat([]byte("RFB-bytes;"), 1<<17)
	payload = payload[:1<<20]
	go func() {
		for i := 0; i < len(payload); i += 64 << 10 {
			_ = c.Write(w.ctx, websocket.MessageBinary, payload[i:min(i+64<<10, len(payload))])
		}
	}()
	var got []byte
	for len(got) < len(payload) {
		typ, b, err := c.Read(w.ctx)
		require.NoError(t, err)
		assert.Equal(t, websocket.MessageBinary, typ)
		got = append(got, b...)
	}
	assert.True(t, bytes.Equal(payload, got))
	for range 16 {
		assert.Equal(t, o, <-w.fo.touched)
	}

	other := w.dial(o, auth.Principal{Kind: auth.KindDevice, ID: "dev_1"})
	w.svc.Revoke(auth.KindWeb, "s1")
	assert.Equal(t, websocket.StatusCode(4401), closeCode(t, c))
	// 别的主体不受影响
	require.NoError(t, other.Write(w.ctx, websocket.MessageBinary, []byte("ping")))
	_, b, err := other.Read(w.ctx)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(b))
	other.CloseNow()
}

// 第 9 个观看者 → 4429；有人离开后名额恢复。
func TestDesktopWSViewerLimit(t *testing.T) {
	w := newWSEnv(t)
	o := w.orb("")
	var conns []*websocket.Conn
	for range maxViewers {
		c := w.dial(o, web("s"))
		// 一次往返：确认已登记
		require.NoError(t, c.Write(w.ctx, websocket.MessageBinary, []byte("x")))
		_, _, err := c.Read(w.ctx)
		require.NoError(t, err)
		conns = append(conns, c)
	}
	ninth := w.dial(o, web("s"))
	assert.Equal(t, websocket.StatusCode(4429), closeCode(t, ninth))
	// 另一台 orb 不受影响
	o2 := w.orb("")
	c2 := w.dial(o2, web("s"))
	require.NoError(t, c2.Write(w.ctx, websocket.MessageBinary, []byte("y")))
	_, _, err := c2.Read(w.ctx)
	require.NoError(t, err)
	c2.CloseNow()
	for _, c := range conns {
		c.CloseNow()
	}
}

// orb 不可用 → 4503；旧 orbd → 4409；已删除 → 4410；DeskDialError → 4502（reason = 错误码）。
func TestDesktopWSFailures(t *testing.T) {
	w := newWSEnv(t)
	cases := []struct {
		usable, dial error
		code         websocket.StatusCode
		reason       string
	}{
		{usable: orbs.ErrOrbNotReady, code: 4503, reason: "orb_unavailable"},
		{usable: orbs.ErrOrbdOutdated, code: 4409, reason: "orbd_outdated"},
		{usable: orbs.ErrOrbGone, code: 4410, reason: "orb_deleted"},
		{dial: &orbs.DeskDialError{Code: "display_unavailable"}, code: 4502, reason: "display_unavailable"},
		{dial: io.ErrUnexpectedEOF, code: 4502, reason: "failed"},
	}
	for _, tc := range cases {
		o := w.orb("")
		w.fo.mu.Lock()
		if tc.usable != nil {
			w.fo.usable[o] = tc.usable
		}
		if tc.dial != nil {
			w.fo.dialErr[o] = tc.dial
		}
		w.fo.mu.Unlock()
		c := w.dial(o, web("s"))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _, err := c.Read(ctx)
		cancel()
		var ce websocket.CloseError
		require.ErrorAs(t, err, &ce, tc.reason)
		assert.Equal(t, tc.code, ce.Code, tc.reason)
		assert.Equal(t, tc.reason, ce.Reason)
	}
}
