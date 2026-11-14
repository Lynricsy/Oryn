package desktop

import (
	"net"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 审查 6：已连接后 desk 流由 orb 一侧结束（Xvnc 重启 / link 断开），客户端此时静默不发消息：
// 服务端以 4502 关闭 WS、处理器退出、观看者名额回收，随后能再开满 maxViewers 个。
func TestDesktopWSUpstreamEOFReleasesViewer(t *testing.T) {
	w := newWSEnv(t)
	o := w.orb("")
	peers := make(chan net.Conn, 1)
	w.fo.mu.Lock()
	w.fo.deskPeer = peers
	w.fo.mu.Unlock()
	done := w.wsDone

	c := w.dial(o, web("s1"))
	defer c.CloseNow()
	peer := <-peers
	// orb 一侧先发一点 RFB 字节，确认双向已接通
	go func() { _, _ = peer.Write([]byte("RFB 003.008\n")) }()
	_, b, err := c.Read(w.ctx)
	require.NoError(t, err)
	assert.Equal(t, "RFB 003.008\n", string(b))

	require.NoError(t, peer.Close())
	assert.Equal(t, websocket.StatusCode(4502), closeCode(t, c))
	require.Equal(t, o, <-done, "处理器退出")
	w.svc.wmu.Lock()
	assert.Zero(t, w.svc.viewers[o], "名额已回收")
	assert.Empty(t, w.svc.conns)
	w.svc.wmu.Unlock()

	// 名额完整可用：再开 maxViewers 个都不会 4429
	w.fo.mu.Lock()
	w.fo.deskPeer = nil
	w.fo.mu.Unlock()
	for range maxViewers {
		cc := w.dial(o, web("s1"))
		require.NoError(t, cc.Write(w.ctx, websocket.MessageBinary, []byte("x")))
		_, _, err := cc.Read(w.ctx)
		require.NoError(t, err)
		defer cc.CloseNow()
	}
}
