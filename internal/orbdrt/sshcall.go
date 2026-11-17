package orbdrt

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// orb 内 SSH 工具（M18，ssh_exec_many）在 orbd 的落点：ssh.call 原样转成 ctl ssh.call，由控制面经 SSH 网关执行。
// 只在 orbd.sock 上提供（runner.sock 只认 artifact.call，runner 的 link 也拒绝这个 ctl 方法）。

// sshCallTimeout 是 ssh.call 的转发上限：控制面单台主机最多执行 600 秒，再留出拨号与排队的余量。
const sshCallTimeout = 11 * time.Minute

// sshCall 处理 orbd.sock 的 ssh.call：校验最基本的形状后原样转成 ctl ssh.call。
func (r *Runtime) sshCall(ctx context.Context, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlSSHCallParams
	if err := decodeSock(params, &p); err != nil {
		return nil, err
	}
	if !validThreadID(p.ThreadID) {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "invalid threadId %q", p.ThreadID)
	}
	if strings.TrimSpace(p.Tool) == "" {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "missing tool name")
	}
	if p.Params == nil {
		p.Params = map[string]any{}
	}
	return r.ctlForwardFor(ctx, apigen.MethodSSHCall, p, sshCallTimeout)
}
