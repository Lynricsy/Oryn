package orbdrt

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"
)

// CDP 前门（M8 计划 2.4）：orbd 监听 127.0.0.1:7822，每个连接先确保浏览器在运行（至多 30s），
// 再与 Chromium 自己的 DevTools 端口做裸 TCP 对接。不解析 HTTP：Chromium 用请求的 Host 头拼
// webSocketDebuggerUrl，omp 以前门地址访问，后续的 WebSocket 自然也走前门。

// maxCDPConns 是前门的并发连接上限，超出直接关闭。
const maxCDPConns = 64

func (d *desktop) serveCDP(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = d.cdpLn.Close() })
	defer stop()
	for {
		c, err := d.cdpLn.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				d.log.Warn("CDP front door accept failed", "err", err)
			}
			return
		}
		select {
		case d.cdpSem <- struct{}{}:
		default:
			_ = c.Close()
			continue
		}
		go func() {
			defer func() { <-d.cdpSem }()
			d.serveCDPConn(ctx, c)
		}()
	}
}

func (d *desktop) serveCDPConn(ctx context.Context, c net.Conn) {
	sctx, cancel := context.WithTimeout(ctx, browserStartWait)
	err := d.startBrowser(sctx)
	cancel()
	if err != nil {
		d.log.Info("CDP connection refused: browser not available", "err", err)
		_ = c.Close()
		return
	}
	port := d.devtoolsPort()
	if port == 0 {
		_ = c.Close()
		return
	}
	up, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		d.log.Info("dial DevTools failed", "port", port, "err", err)
		_ = c.Close()
		return
	}
	pipeConns(c, up)
}
