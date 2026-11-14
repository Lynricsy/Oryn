package orbdrt

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// portal 流（M4 计划 2.9）：控制面在 link 上打开 `portal` 流（流头带端口），orbd 回一行 LinkPortalReply，
// ok=true 之后把流与 127.0.0.1:<port>（拒绝时退到 [::1]:<port>）做裸 TCP 对接，不解析 HTTP。
// 端口是否可暴露由这里最终裁决：只放行属于有 portal 的服务的端口，orbd 的保留端口（egress、CDP 前门、
// Chromium 当前的 DevTools 端口）永不暴露。

const (
	portalDialTimeout  = 5 * time.Second
	portalReplyTimeout = 10 * time.Second
	// portalDrainTimeout 是一个方向结束后留给另一方向的时间。
	portalDrainTimeout = 30 * time.Second
)

// Portal 应答的错误码（docs/protocol.md「M4」）。
const (
	portalNotExposed        = "not_exposed"
	portalForbiddenPort     = "forbidden_port"
	portalConnectionRefused = "connection_refused"
	portalTimeout           = "timeout"
	portalFailed            = "failed"
)

func (r *Runtime) servePortal(st *link.Stream) {
	port := st.Header.Port
	reject := func(code, msg string) {
		r.writeReplyLine(st, apigen.LinkPortalReply{Ok: false, Code: new(code), Message: new(msg)})
		_ = st.Close()
	}
	switch {
	case port <= 0 || port > 65535:
		reject(portalNotExposed, "invalid port")
		return
	case r.reservedPort(port):
		reject(portalForbiddenPort, "port "+strconv.Itoa(port)+" is reserved by orbd")
		return
	case !r.svc.Exposed(port):
		reject(portalNotExposed, "port "+strconv.Itoa(port)+" does not belong to a service with a portal")
		return
	}
	conn, err := dialLoopback(port)
	if err != nil {
		code := portalFailed
		var ne net.Error
		switch {
		case errors.Is(err, syscall.ECONNREFUSED):
			code = portalConnectionRefused
		case errors.As(err, &ne) && ne.Timeout():
			code = portalTimeout
		}
		reject(code, err.Error())
		return
	}
	if !r.writeReplyLine(st, apigen.LinkPortalReply{Ok: true}) {
		_ = conn.Close()
		_ = st.Close()
		return
	}
	pipePortal(st, conn)
}

// writeReplyLine 写 portal / desk 流的应答行（JSON + `\n`，10s 写截止）；失败返回 false。
func (r *Runtime) writeReplyLine(st *link.Stream, rep any) bool {
	b, _ := json.Marshal(rep)
	_ = st.SetWriteDeadline(time.Now().Add(portalReplyTimeout))
	_, err := st.Write(append(b, '\n'))
	_ = st.SetWriteDeadline(time.Time{})
	if err != nil {
		r.log.Debug("stream reply failed", "t", st.Header.T, "port", st.Header.Port, "err", err)
		return false
	}
	return true
}

// dialLoopback 连本机端口：先 127.0.0.1，被拒绝时再试 [::1]（Node 系 dev server 常只听 IPv6 回环）。
func dialLoopback(port int) (net.Conn, error) {
	p := strconv.Itoa(port)
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", p), portalDialTimeout)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return nil, err
	}
	if c6, err6 := net.DialTimeout("tcp", net.JoinHostPort("::1", p), portalDialTimeout); err6 == nil {
		return c6, nil
	}
	return nil, err
}

// pipePortal 双向拷贝 portal / desk 流与本机连接（见 pipeConns）。
func pipePortal(st *link.Stream, conn net.Conn) { pipeConns(st, conn) }

// pipeConns 双向拷贝：一个方向结束后对另一端半关闭写方向（有 CloseWrite 的用它，link 流的 Close 本身就是
// 半关闭），并给剩下的方向 portalDrainTimeout，随后两边都关。
func pipeConns(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(b, a)
		closeWrite(b)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(a, b)
		closeWrite(a)
		done <- struct{}{}
	}()
	<-done
	deadline := time.Now().Add(portalDrainTimeout)
	_ = a.SetDeadline(deadline)
	_ = b.SetDeadline(deadline)
	<-done
	_ = a.Close()
	_ = b.Close()
}

// closeWrite 半关闭 c 的写方向：TCP / unix 连接用 CloseWrite；link 流的 Close 发 FIN，同样是半关闭。
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
