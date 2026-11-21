package orbdrt

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// orb 内 `oryn boxes …`（M23）在 orbd 的落点：box.call 原样转成 ctl box.call，由控制面执行。
// 只在 orbd.sock 上提供（runner.sock 不提供，runner 的 link 也拒绝这个 ctl 方法）。

// boxCallTimeout 是 box.call 的转发上限：盖住部署的传输（30 分钟）与激活（10 分钟）。
const boxCallTimeout = 45 * time.Minute

// boxCall 处理 orbd.sock 的 box.call：校验最基本的形状后原样转成 ctl box.call。
func (r *Runtime) boxCall(ctx context.Context, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlBoxCallParams
	if err := decodeSock(params, &p); err != nil {
		return nil, err
	}
	if !validThreadID(p.ThreadID) {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "invalid threadId %q", p.ThreadID)
	}
	if strings.TrimSpace(p.Op) == "" {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "missing op")
	}
	if p.Params == nil {
		p.Params = map[string]any{}
	}
	return r.ctlForwardFor(ctx, apigen.MethodBoxCall, p, boxCallTimeout)
}
