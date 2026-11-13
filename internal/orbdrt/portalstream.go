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
// 端口是否可暴露由这里最终裁决：只放行属于有 portal 的服务的端口，egress 端口永不暴露。

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
		r.writePortalReply(st, apigen.LinkPortalReply{Ok: false, Code: new(code), Message: new(msg)})
		_ = st.Close()
	}
	switch {
	case port <= 0 || port > 65535:
		reject(portalNotExposed, "invalid port")
		return
	case port == r.svc.egressPort:
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
	if !r.writePortalReply(st, apigen.LinkPortalReply{Ok: true}) {
		_ = conn.Close()
		_ = st.Close()
		return
	}
	pipePortal(st, conn)
}

// writePortalReply 写应答行（10s 写截止）；失败返回 false。
func (r *Runtime) writePortalReply(st *link.Stream, rep apigen.LinkPortalReply) bool {
	b, _ := json.Marshal(rep)
	_ = st.SetWriteDeadline(time.Now().Add(portalReplyTimeout))
	_, err := st.Write(append(b, '\n'))
	_ = st.SetWriteDeadline(time.Time{})
	if err != nil {
		r.log.Debug("portal reply failed", "port", st.Header.Port, "err", err)
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

// pipePortal 双向拷贝：一个方向结束后对另一端半关闭写方向，并给剩下的方向 portalDrainTimeout，随后两边都关。
func pipePortal(st *link.Stream, conn net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(conn, st) // 控制面 → 服务
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(st, conn) // 服务 → 控制面
		_ = st.Close()           // yamux FIN：半关闭写方向
		done <- struct{}{}
	}()
	<-done
	deadline := time.Now().Add(portalDrainTimeout)
	_ = conn.SetDeadline(deadline)
	_ = st.SetDeadline(deadline)
	<-done
	_ = conn.Close()
	_ = st.Close()
}
