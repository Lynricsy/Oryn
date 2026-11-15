package fcguest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	req := Request{Op: OpStart, URL: "http://172.31.255.1:7600", OrbID: "orb-1", Token: "oot_x", DNS: []string{"1.1.1.1"}, NowMs: 42}
	require.NoError(t, WriteRequest(&buf, req))
	require.NoError(t, WriteRequest(&buf, Request{Op: OpShutdown, GraceSeconds: 30}))
	assert.Equal(t, `{"op":"start","url":"http://172.31.255.1:7600","orbId":"orb-1","token":"oot_x","dns":["1.1.1.1"],"nowMs":42}`+"\n"+
		`{"op":"shutdown","graceSeconds":30}`+"\n", buf.String(), "一行一条、零值字段省略")

	r := bufio.NewReader(&buf)
	got, err := ReadRequest(r)
	require.NoError(t, err)
	assert.Equal(t, req, got)
	got, err = ReadRequest(r)
	require.NoError(t, err)
	assert.Equal(t, Request{Op: OpShutdown, GraceSeconds: 30}, got)
	_, err = ReadRequest(r)
	assert.ErrorIs(t, err, io.EOF)

	buf.Reset()
	require.NoError(t, WriteResponse(&buf, Response{OK: true}))
	require.NoError(t, WriteResponse(&buf, Response{Error: "boom"}))
	assert.Equal(t, `{"ok":true}`+"\n"+`{"ok":false,"error":"boom"}`+"\n", buf.String())
	r = bufio.NewReader(&buf)
	resp, err := ReadResponse(r)
	require.NoError(t, err)
	assert.Equal(t, Response{OK: true}, resp)
	resp, err = ReadResponse(r)
	require.NoError(t, err)
	assert.Equal(t, Response{Error: "boom"}, resp)
}

func TestLineLimits(t *testing.T) {
	// 恰好 MaxLine 字节的一行可读（跨越 bufio 的 4KiB 缓冲）。
	pad := MaxLine - len(`{"op":"ping","token":""}`)
	exact := `{"op":"ping","token":"` + strings.Repeat("a", pad) + `"}`
	require.Len(t, exact, MaxLine)
	got, err := ReadRequest(bufio.NewReader(strings.NewReader(exact + "\n")))
	require.NoError(t, err)
	assert.Len(t, got.Token, pad)

	_, err = ReadRequest(bufio.NewReader(strings.NewReader(`{"op":"ping","token":"` + strings.Repeat("a", pad+1) + "\"}\n")))
	assert.ErrorIs(t, err, ErrLineTooLong, "超出 MaxLine 的读报错")
	assert.ErrorIs(t, WriteRequest(io.Discard, Request{Op: OpToken, Token: strings.Repeat("a", MaxLine)}), ErrLineTooLong, "超出 MaxLine 的写报错")

	_, err = ReadRequest(bufio.NewReader(strings.NewReader(`{"op":"ping"}`)))
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "没有结尾换行")
	_, err = ReadRequest(bufio.NewReader(strings.NewReader("not json\n")))
	assert.ErrorContains(t, err, "无效的 JSON 行")
}

func TestHandshake(t *testing.T) {
	for name, tc := range map[string]struct {
		reply string
		err   string
	}{
		"成功":      {reply: "OK 1073741824\n"},
		"关闭连接":    {err: "未监听"},
		"应答不是 OK": {reply: "FAILURE\n", err: "无效"},
		"端口不是数字":  {reply: "OK x\n", err: "无效"},
		"应答过长":    {reply: "OK " + strings.Repeat("1", 100) + "\n", err: "超过"},
	} {
		t.Run(name, func(t *testing.T) {
			host, guest := net.Pipe()
			defer host.Close()
			go func() {
				defer guest.Close()
				line, err := bufio.NewReader(guest).ReadString('\n')
				if err != nil || line != "CONNECT 5005\n" {
					return
				}
				_, _ = io.WriteString(guest, tc.reply)
			}()
			r, err := Handshake(host, Port)
			if tc.err != "" {
				assert.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, r)
		})
	}
}

// fakeVsock 模拟 Firecracker 的 vsock unix socket：校验 CONNECT 行、回 OK，再把请求交给 handle 应答。
func fakeVsock(t *testing.T, handle func(Request, net.Conn)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v.sock")
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				line, err := r.ReadString('\n')
				if err != nil || line != "CONNECT 5005\n" {
					return
				}
				// 握手应答与 guest 的应答之间不留间隔也必须正确解析（共用同一个读缓冲）。
				req, err := func() (Request, error) {
					_, _ = io.WriteString(c, "OK 1073741825\n")
					return ReadRequest(r)
				}()
				if err != nil {
					return
				}
				handle(req, c)
			}()
		}
	}()
	return path
}

func TestCall(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []Request
	)
	path := fakeVsock(t, func(req Request, c net.Conn) {
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		switch req.Op {
		case OpPing:
			_ = WriteResponse(c, Response{OK: true})
		case OpFSFreeze:
			_ = WriteResponse(c, Response{Error: "FIFREEZE: device busy"})
		case OpSync:
			time.Sleep(time.Second) // 不应答，等调用方超时
		}
	})
	ctx := context.Background()

	require.NoError(t, Call(ctx, path, Request{Op: OpPing}))

	err := Call(ctx, path, Request{Op: OpFSFreeze})
	var re *RemoteError
	require.True(t, errors.As(err, &re), "ok=false → *RemoteError")
	assert.Equal(t, &RemoteError{Op: OpFSFreeze, Msg: "FIFREEZE: device busy"}, re)
	assert.Equal(t, "fcguest fsfreeze: FIFREEZE: device busy", err.Error())

	tctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = Call(tctx, path, Request{Op: OpSync})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 900*time.Millisecond, "超时即返回，不等 guest")

	err = Call(ctx, filepath.Join(t.TempDir(), "missing.sock"), Request{Op: OpPing})
	assert.ErrorContains(t, err, "missing.sock")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []Request{{Op: OpPing}, {Op: OpFSFreeze}, {Op: OpSync}}, seen)
}
