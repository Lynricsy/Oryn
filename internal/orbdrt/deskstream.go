package orbdrt

import (
	"net"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// desk 流（M8 计划 2.5）：控制面为每个桌面观看者打开一条 `desk` 流，orbd 先回一行 LinkDeskReply，
// ok=true 之后把流与 Xvnc 的 unix socket（$HOME/.oryn/vnc.sock）对接，流上是原始 RFB 字节。

// desk 应答的错误码（docs/protocol.md「M8」）。
const (
	deskDisplayUnavailable = "display_unavailable"
	deskFailed             = "failed"
)

func (r *Runtime) serveDesk(st *link.Stream) {
	d := r.desk
	reject := func(code, msg string) {
		r.writeReplyLine(st, apigen.LinkDeskReply{Ok: false, Code: new(code), Message: new(msg)})
		_ = st.Close()
	}
	if !d.isDisplayReady() {
		reject(deskDisplayUnavailable, "the desktop display is not ready")
		return
	}
	conn, err := net.DialTimeout("unix", d.sockPath(), portalDialTimeout)
	if err != nil {
		reject(deskFailed, err.Error())
		return
	}
	if !r.writeReplyLine(st, apigen.LinkDeskReply{Ok: true}) {
		_ = conn.Close()
		_ = st.Close()
		return
	}
	pipePortal(st, conn)
}
