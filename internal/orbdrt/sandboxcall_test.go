package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// sbxReq 是假控制面在 sandbox 流上收到的一次调用。
type sbxReq struct {
	header link.StreamHeader
	line   string
	st     *link.Stream
}

// serveSandbox 接下一条 sandbox 流，读出请求行后按 reply 回写并关流。
func serveSandbox(t *testing.T, cp *fakeCP, reply string) <-chan sbxReq {
	t.Helper()
	ch := make(chan sbxReq, 1)
	go func() {
		select {
		case st := <-cp.sbx:
			line, err := st.ReadLine(1 << 20)
			if err != nil {
				close(ch)
				return
			}
			if reply != "" {
				_, _ = st.Write([]byte(reply))
				_ = st.Close()
			}
			ch <- sbxReq{header: st.Header, line: string(line), st: st}
		case <-time.After(20 * time.Second):
			close(ch)
		}
	}()
	return ch
}

// M30 3.3：orbd.sock 的 sandbox.call 在 link 上开一条 sandbox 流：输出行变成 stdout / stderr 日志，result 原样返回，
// error 的错误码原样回到 sock；流在最后一行之前断开 → offline；无效参数不开流。
func TestSandboxCallForward(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)

	got := serveSandbox(t, cp, `{"s":"o","d":"a\n"}`+"\n"+`{"s":"e","d":"b\n"}`+"\n"+`{"result":{"exit":2,"durationMs":5}}`+"\n")
	var log sockLog
	raw, err := sockCall(t, rt, orbsock.MethodSandboxCall, orbsock.SandboxCallParams{
		ThreadID: libThread, Op: "exec", Params: json.RawMessage(`{"sandbox":"sbx_1","command":"x"}`),
	}, &log)
	require.NoError(t, err)
	assert.JSONEq(t, `{"exit":2,"durationMs":5}`, string(raw))
	assert.Equal(t, "a\n", log.get(orbsock.StreamStdout))
	assert.Equal(t, "b\n", log.get(orbsock.StreamStderr))
	req, ok := <-got
	require.True(t, ok)
	assert.Equal(t, link.StreamSandbox, req.header.T)
	assert.Equal(t, libThread, req.header.ThreadID)
	assert.JSONEq(t, `{"op":"exec","params":{"sandbox":"sbx_1","command":"x"}}`, req.line)

	// 缺 params 时补成空对象；控制面的错误码原样透传。
	got = serveSandbox(t, cp, `{"error":{"code":"quota_per_orb","message":"at most 2 sandboxes"}}`+"\n")
	_, err = sockCall(t, rt, orbsock.MethodSandboxCall, map[string]any{"threadId": libThread, "op": "create"}, nil)
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "quota_per_orb", se.Code)
	assert.Equal(t, "at most 2 sandboxes", se.Message)
	req = <-got
	assert.JSONEq(t, `{"op":"create","params":{}}`, req.line)

	// 最后一行之前断开
	got = serveSandbox(t, cp, `{"s":"o","d":"partial"}`+"\n")
	_, err = sockCall(t, rt, orbsock.MethodSandboxCall, orbsock.SandboxCallParams{ThreadID: libThread, Op: "list"}, nil)
	assert.Equal(t, orbsock.CodeOffline, sockErrCode(err))
	<-got

	for _, bad := range []map[string]any{
		{"threadId": "../x", "op": "list"},
		{"threadId": libThread, "op": " "},
	} {
		_, err := sockCall(t, rt, orbsock.MethodSandboxCall, bad, nil)
		assert.Equal(t, orbsock.CodeInvalidParams, sockErrCode(err), "%v", bad)
	}
	assert.Empty(t, cp.sbx, "无效参数不开流")
}

// 调用方中途断开 orbd.sock：orbd 关掉 sandbox 流，控制面读到 EOF。
func TestSandboxCallCallerGone(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	require.Eventually(t, func() bool {
		_, err := os.Stat(orbsock.Path(rt.home))
		return rt.current() != nil && err == nil
	}, 10*time.Second, 10*time.Millisecond)

	got := serveSandbox(t, cp, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := orbsock.Call(ctx, orbsock.Path(rt.home), orbsock.MethodSandboxCall, orbsock.SandboxCallParams{ThreadID: libThread, Op: "exec"}, nil)
		done <- err
	}()
	req, ok := <-got
	require.True(t, ok)
	cancel()
	_ = req.st.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err := req.st.Read(make([]byte, 1))
	assert.True(t, errors.Is(err, io.EOF), "%v", err)
	assert.Error(t, <-done)
}
