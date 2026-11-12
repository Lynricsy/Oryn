package orbsock

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serve(t *testing.T, path string, h Handler) {
	t.Helper()
	ln, err := Listen(path)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); Serve(ctx, ln, h) }()
	t.Cleanup(func() { cancel(); <-done })
}

// orbd 重启后残留的 socket 文件不能挡住新监听；活着的监听者则不能被抢占。权限 0600。
func TestListenStaleAndLive(t *testing.T) {
	path := Path(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	// 残留：一个已关闭监听者留下的 socket 文件。
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, ln.Close())
	require.FileExists(t, path)

	ln2, err := Listen(path)
	require.NoError(t, err, "stale socket must be replaced")
	defer ln2.Close()
	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	_, err = Listen(path)
	assert.Error(t, err, "live listener must not be replaced")
}

func TestCallStreamsLogsThenResultOrError(t *testing.T) {
	path := Path(t.TempDir())
	serve(t, path, func(_ context.Context, method string, params json.RawMessage, out *Responder) (any, error) {
		switch method {
		case "echo":
			out.Log(StreamStdout, "a\n")
			out.Log(StreamStderr, "b\n")
			return json.RawMessage(params), nil
		case "busy":
			out.Log(StreamSystem, "x")
			return nil, Errorf(CodeBusy, "in progress")
		default:
			return nil, errors.New("plain")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var logs []string
	raw, err := Call(ctx, path, "echo", map[string]int{"n": 1}, func(s, d string) { logs = append(logs, s+":"+d) })
	require.NoError(t, err)
	assert.JSONEq(t, `{"n":1}`, string(raw))
	assert.Equal(t, []string{"stdout:a\n", "stderr:b\n"}, logs)

	_, err = Call(ctx, path, "busy", nil, nil)
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodeBusy, e.Code)

	_, err = Call(ctx, path, "other", nil, nil)
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodeInternal, e.Code)
}

// 调用方断开连接时取消处理器的 ctx（手动重跑随 Ctrl-C 停止）。
func TestDisconnectCancelsHandler(t *testing.T) {
	path := Path(t.TempDir())
	canceled := make(chan struct{})
	serve(t, path, func(ctx context.Context, _ string, _ json.RawMessage, _ *Responder) (any, error) {
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := Call(ctx, path, "wait", nil, nil); errc <- err }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler ctx not canceled after client disconnect")
	}
	assert.ErrorIs(t, <-errc, context.Canceled)
}
