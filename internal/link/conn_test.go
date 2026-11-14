package link

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H1：net.Pipe 两端各建一个会话，ctl 往返一次；关闭一端后另一端的 CloseChan 关闭。
func TestNewConnSessionCtlRoundTripAndClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a, b := net.Pipe()
	srvCh := make(chan *Session, 1)
	errCh := make(chan error, 1)
	go func() {
		s, err := NewConnSession(b, false)
		if err != nil {
			errCh <- err
			return
		}
		srvCh <- s
	}()
	cli, err := NewConnSession(a, true)
	require.NoError(t, err)
	var srv *Session
	select {
	case srv = <-srvCh:
	case err := <-errCh:
		t.Fatal(err)
	}

	go func() {
		st, err := srv.AcceptStream(ctx)
		if err != nil {
			return
		}
		NewCtl(st, CtlHandlers{OnRequest: func(_ context.Context, method string, params json.RawMessage) (any, error) {
			return map[string]any{"method": method, "params": params}, nil
		}})
	}()

	st, err := cli.Open(ctx, StreamHeader{T: StreamCtl})
	require.NoError(t, err)
	ctl := NewCtl(st, CtlHandlers{})
	var res struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	require.NoError(t, ctl.Call(ctx, "ping", map[string]int{"n": 1}, &res))
	assert.Equal(t, "ping", res.Method)
	assert.JSONEq(t, `{"n":1}`, string(res.Params))

	require.NoError(t, cli.Close())
	select {
	case <-srv.CloseChan():
	case <-ctx.Done():
		t.Fatal("server session not closed after client close")
	}
	assert.True(t, srv.IsClosed())
}
