package orbdrt

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/link"
)

// egressWait 是 link 断开时新出站连接等待重连的上限。
const egressWait = 30 * time.Second

// serveEgress 在 127.0.0.1:7811 接受本机出站代理连接（模型 API 与 git 请求），
// 每个 TCP 连接对应一条 `egress` 流，控制面在其上以 HTTP/1.1 服务并注入密钥（H4.6）。
func (r *Runtime) serveEgress(ctx context.Context) {
	go func() {
		<-ctx.Done()
		_ = r.egressLn.Close()
	}()
	for {
		nc, err := r.egressLn.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			r.log.Warn("egress accept failed", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go r.handleEgress(ctx, nc)
	}
}

func (r *Runtime) handleEgress(ctx context.Context, nc net.Conn) {
	defer nc.Close()
	wctx, cancel := context.WithTimeout(ctx, egressWait)
	c, err := r.waitConn(wctx)
	cancel()
	if err != nil {
		r.log.Warn("egress: no link; dropping connection")
		return
	}
	octx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := c.sess.Open(octx, link.StreamHeader{T: link.StreamEgress})
	cancel()
	if err != nil {
		r.log.Warn("egress: open stream failed", "err", err)
		return
	}
	defer st.Close()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-c.done: // link 断开：两端都不会再有数据
			_ = nc.Close()
		case <-finished:
		}
	}()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(st, nc)
		_ = st.Close() // 半关闭：控制面读到 EOF
	}()
	_, _ = io.Copy(nc, st)
	if tc, ok := nc.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	wg.Wait()
}
