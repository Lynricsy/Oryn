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

// M23 3.3：orbd.sock 的 box.call 原样转成 ctl box.call；无效线程 id 与缺 op 不转发；控制面的错误码原样回到 sock。
func TestBoxCallForward(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)

	calls := make(chan apigen.LinkCtlBoxCallParams, 4)
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		if method != string(apigen.MethodBoxCall) {
			return nil, false, nil
		}
		var p apigen.LinkCtlBoxCallParams
		_ = json.Unmarshal(params, &p)
		calls <- p
		if p.Op == "delete" {
			return nil, true, &link.CtlError{Code: "forbidden", Message: "deleting a box needs the owner"}
		}
		return apigen.LinkCtlBoxCallResult{Result: map[string]any{"boxes": []any{}}}, true, nil
	}

	raw, err := sockCall(t, rt, orbsock.MethodBoxCall, apigen.LinkCtlBoxCallParams{
		ThreadID: libThread, Op: "show", Params: map[string]any{"box": "b1"},
	}, nil)
	require.NoError(t, err)
	var res apigen.LinkCtlBoxCallResult
	require.NoError(t, json.Unmarshal(raw, &res))
	assert.Contains(t, res.Result, "boxes")
	p := <-calls
	assert.Equal(t, libThread, p.ThreadID)
	assert.Equal(t, "show", p.Op)
	assert.Equal(t, map[string]any{"box": "b1"}, p.Params)

	// 缺 params 时补成空对象
	_, err = sockCall(t, rt, orbsock.MethodBoxCall, map[string]any{"threadId": libThread, "op": "list"}, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{}, (<-calls).Params)

	for _, bad := range []map[string]any{
		{"threadId": "../x", "op": "list"},
		{"threadId": libThread, "op": " "},
	} {
		_, err := sockCall(t, rt, orbsock.MethodBoxCall, bad, nil)
		assert.Equal(t, orbsock.CodeInvalidParams, sockErrCode(err), "%v", bad)
	}
	assert.Empty(t, calls, "无效参数不转发")

	_, err = sockCall(t, rt, orbsock.MethodBoxCall, apigen.LinkCtlBoxCallParams{ThreadID: libThread, Op: "delete", Params: map[string]any{}}, nil)
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "forbidden", se.Code)
	<-calls
}
