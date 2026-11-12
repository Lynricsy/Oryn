package link

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamListenerPushAcceptClose(t *testing.T) {
	l := NewStreamListener("oryn-test")
	assert.Equal(t, "oryn-test", l.Addr().Network())
	assert.Equal(t, "oryn-test", l.Addr().String())

	a, b := net.Pipe()
	defer b.Close()
	go l.Push(a)
	got, err := l.Accept()
	require.NoError(t, err)
	assert.Same(t, a, got)

	require.NoError(t, l.Close())
	require.NoError(t, l.Close()) // 幂等
	_, err = l.Accept()
	assert.True(t, errors.Is(err, net.ErrClosed))

	// 关闭后 Push 的连接被直接关闭。
	c, d := net.Pipe()
	l.Push(c)
	_ = d.SetReadDeadline(time.Now().Add(time.Second))
	_, err = d.Read(make([]byte, 1))
	assert.Error(t, err)
	assert.False(t, errors.Is(err, os.ErrDeadlineExceeded), "应读到对端关闭而非超时")
}
