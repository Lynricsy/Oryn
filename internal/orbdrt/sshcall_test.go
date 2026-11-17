package orbdrt

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// M18 8.2：orbd.sock 的 ssh.call 原样转成 ctl ssh.call；无效线程 id 与缺工具名不转发；控制面的错误码原样回到 sock。
func TestSSHCallForward(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)

	calls := make(chan apigen.LinkCtlSSHCallParams, 4)
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		if method != string(apigen.MethodSSHCall) {
			return nil, false, nil
		}
		var p apigen.LinkCtlSSHCallParams
		_ = json.Unmarshal(params, &p)
		calls <- p
		if p.Tool != "ssh_exec_many" {
			return nil, true, &link.CtlError{Code: "invalid_params", Message: "未知的 SSH 工具"}
		}
		return apigen.LinkCtlSSHCallResult{Text: "## web1 — exit 0（0.1s）\nok\n", Details: map[string]any{"results": []any{}}}, true, nil
	}

	params := map[string]any{"hosts": []any{"web1"}, "command": "echo ok"}
	raw, err := sockCall(t, rt, orbsock.MethodSshCall, apigen.LinkCtlSSHCallParams{
		ThreadID: libThread, Tool: "ssh_exec_many", Params: params,
	}, nil)
	require.NoError(t, err)
	var res apigen.LinkCtlSSHCallResult
	require.NoError(t, json.Unmarshal(raw, &res))
	assert.Contains(t, res.Text, "## web1 — exit 0")
	assert.Contains(t, res.Details, "results")
	p := <-calls
	assert.Equal(t, libThread, p.ThreadID)
	assert.Equal(t, "ssh_exec_many", p.Tool)
	assert.Equal(t, params, p.Params)

	// 缺 params 时补成空对象（控制面按工具严格解码）
	_, err = sockCall(t, rt, orbsock.MethodSshCall, map[string]any{"threadId": libThread, "tool": "ssh_exec_many"}, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{}, (<-calls).Params)

	for _, bad := range []map[string]any{
		{"threadId": "../x", "tool": "ssh_exec_many"},
		{"threadId": libThread, "tool": " "},
	} {
		_, err := sockCall(t, rt, orbsock.MethodSshCall, bad, nil)
		assert.Equal(t, orbsock.CodeInvalidParams, sockErrCode(err), "%v", bad)
	}
	assert.Empty(t, calls, "无效参数不转发")

	_, err = sockCall(t, rt, orbsock.MethodSshCall, apigen.LinkCtlSSHCallParams{ThreadID: libThread, Tool: "ssh_nope", Params: map[string]any{}}, nil)
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "invalid_params", se.Code)
	assert.Equal(t, "未知的 SSH 工具", se.Message)
	<-calls
}
