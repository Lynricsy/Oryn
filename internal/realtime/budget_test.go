package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serverConn 返回一条真实 WebSocket 的服务端 conn（不跑读写循环）。
func serverConn(t *testing.T, h *Hub) *conn {
	t.Helper()
	got := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		got <- ws
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cl, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.CloseNow() })
	ws := <-got
	cctx, ccancel := context.WithCancel(context.Background())
	t.Cleanup(ccancel)
	return &conn{h: h, id: "ws-test", ws: ws, ctx: cctx, cancel: ccancel, wake: make(chan struct{}, 1)}
}

func (c *conn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// CP18：index 发出前暂存的广播与补发期间暂存的线程事件同样计入单连接预算，超限即断开。
func TestBufferedMessagesCountTowardBudget(t *testing.T) {
	h := &Hub{MaxPending: 100, conns: map[*conn]struct{}{}, subs: map[string]map[*conn]*sub{}}
	idx := serverConn(t, h)
	h.conns[idx] = struct{}{}
	big := map[string]string{"type": "orb", "pad": strings.Repeat("x", 60)}
	h.toAll(big)
	assert.False(t, idx.isClosed())
	h.toAll(big)
	assert.True(t, idx.isClosed(), "indexBuf 超出预算")

	h2 := &Hub{MaxPending: 100, conns: map[*conn]struct{}{}, subs: map[string]map[*conn]*sub{}}
	c := serverConn(t, h2)
	s := &sub{buffering: true}
	h2.subs["T-1"] = map[*conn]*sub{c: s}
	h2.toThread("T-1", big)
	assert.False(t, c.isClosed())
	h2.toThread("T-1", big)
	assert.True(t, c.isClosed(), "sub.buf 超出预算")

	// 补发出错时移除订阅并退回暂存预算
	h3 := &Hub{MaxPending: 1000, conns: map[*conn]struct{}{}, subs: map[string]map[*conn]*sub{}}
	c3 := serverConn(t, h3)
	s3 := &sub{buffering: true}
	h3.subs["T-1"] = map[*conn]*sub{c3: s3}
	h3.toThread("T-1", big)
	h3.unsubscribe(c3, "T-1", s3)
	assert.Empty(t, h3.subs)
	c3.mu.Lock()
	assert.Zero(t, c3.pending)
	c3.mu.Unlock()
}
