package link

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testToken = "oot_test"

// startServer 起一个 httptest 服务端，每个 link 会话交给 onSession。
func startServer(t *testing.T, onSession func(*Session)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(Path, func(w http.ResponseWriter, r *http.Request) {
		s, err := Accept(w, r, func(_ *http.Request, token string) error {
			if token != testToken {
				return errors.New("bad token")
			}
			return nil
		})
		if err != nil {
			return
		}
		onSession(s)
		<-s.CloseChan()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDialUnauthorized(t *testing.T) {
	srv := startServer(t, func(*Session) {})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, URL(srv.URL), "oot_wrong")
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestHeaderCodec(t *testing.T) {
	line, err := EncodeHeader(StreamHeader{T: StreamAgent, ThreadID: "T-1"})
	require.NoError(t, err)
	assert.Equal(t, `{"t":"agent","threadId":"T-1"}`+"\n", string(line))

	h, err := DecodeHeader([]byte(`{"t":"portal","port":3000}` + "\n"))
	require.NoError(t, err)
	assert.Equal(t, StreamPortal, h.T)
	assert.JSONEq(t, `{"t":"portal","port":3000}`, string(h.Raw))

	// Raw 原样往返。
	line, err = EncodeHeader(h)
	require.NoError(t, err)
	assert.Equal(t, `{"t":"portal","port":3000}`+"\n", string(line))

	_, err = EncodeHeader(StreamHeader{})
	assert.Error(t, err)
	_, err = DecodeHeader([]byte(`{"x":1}`))
	assert.Error(t, err)
}

func TestLinkCtlAndAgentStream(t *testing.T) {
	const size = 1 << 20
	clientData := make([]byte, size)
	serverData := make([]byte, size)
	_, _ = rand.Read(clientData)
	_, _ = rand.Read(serverData)

	type notif struct {
		method string
		params json.RawMessage
	}
	notifs := make(chan notif, 4)
	serverGot := make(chan [32]byte, 1)
	serverErr := make(chan error, 4)

	srv := startServer(t, func(s *Session) {
		go func() {
			ctx := context.Background()
			// 第一条流必须是 ctl。
			st, err := s.AcceptStream(ctx)
			if err != nil {
				serverErr <- err
				return
			}
			if st.Header.T != StreamCtl {
				serverErr <- errors.New("first stream is not ctl: " + st.Header.T)
				return
			}
			NewCtl(st, CtlHandlers{
				OnRequest: func(_ context.Context, method string, params json.RawMessage) (any, error) {
					switch method {
					case "entries.cursors":
						var p struct {
							ThreadID string `json:"threadId"`
						}
						if err := json.Unmarshal(params, &p); err != nil {
							return nil, &CtlError{Code: CodeInvalidParams, Message: err.Error()}
						}
						return map[string]any{"cursors": []map[string]any{{"file": p.ThreadID + ".jsonl", "offset": 42}}}, nil
					default:
						return nil, ErrMethodNotFound
					}
				},
				OnNotify: func(_ context.Context, method string, params json.RawMessage) {
					notifs <- notif{method, params}
				},
			})

			ag, err := s.AcceptStream(ctx)
			if err != nil {
				serverErr <- err
				return
			}
			if ag.Header.T != StreamAgent || ag.Header.ThreadID != "T-abc" {
				serverErr <- errors.New("unexpected agent header")
				return
			}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				if _, err := ag.Write(serverData); err != nil {
					serverErr <- err
				}
				_ = ag.Close()
			}()
			go func() {
				defer wg.Done()
				h := sha256.New()
				if _, err := io.Copy(h, ag); err != nil {
					serverErr <- err
				}
				var sum [32]byte
				copy(sum[:], h.Sum(nil))
				serverGot <- sum
			}()
			wg.Wait()
		}()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := Dial(ctx, URL(srv.URL), testToken)
	require.NoError(t, err)
	defer sess.Close()

	ctlStream, err := sess.Open(ctx, StreamHeader{T: StreamCtl})
	require.NoError(t, err)
	ctl := NewCtl(ctlStream, CtlHandlers{})

	// 通知先于请求发出，服务端按序收到。
	require.NoError(t, ctl.Notify("hello", map[string]any{"orbdVersion": "test", "threads": []any{}}))

	var res struct {
		Cursors []struct {
			File   string `json:"file"`
			Offset int64  `json:"offset"`
		} `json:"cursors"`
	}
	require.NoError(t, ctl.Call(ctx, "entries.cursors", map[string]string{"threadId": "T-abc"}, &res))
	require.Len(t, res.Cursors, 1)
	assert.Equal(t, "T-abc.jsonl", res.Cursors[0].File)
	assert.EqualValues(t, 42, res.Cursors[0].Offset)

	err = ctl.Call(ctx, "nope", nil, nil)
	var ce *CtlError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, CodeMethodNotFound, ce.Code)

	select {
	case n := <-notifs:
		assert.Equal(t, "hello", n.method)
		assert.JSONEq(t, `{"orbdVersion":"test","threads":[]}`, string(n.params))
	case <-ctx.Done():
		t.Fatal("notification not received")
	}

	// agent 流双向各 1MiB 并发传输。
	ag, err := sess.Open(ctx, StreamHeader{T: StreamAgent, ThreadID: "T-abc"})
	require.NoError(t, err)
	var wg sync.WaitGroup
	var clientSum [32]byte
	var writeErr, readErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, writeErr = ag.Write(clientData)
		_ = ag.Close()
	}()
	go func() {
		defer wg.Done()
		h := sha256.New()
		_, readErr = io.Copy(h, ag)
		copy(clientSum[:], h.Sum(nil))
	}()
	wg.Wait()
	require.NoError(t, writeErr)
	require.NoError(t, readErr)
	assert.Equal(t, sha256.Sum256(serverData), clientSum, "client received server data")

	select {
	case got := <-serverGot:
		assert.Equal(t, sha256.Sum256(clientData), got, "server received client data")
	case err := <-serverErr:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("server did not finish")
	}
	select {
	case err := <-serverErr:
		t.Fatal(err)
	default:
	}
}

func TestCtlCallCancelAndClose(t *testing.T) {
	release := make(chan struct{})
	srv := startServer(t, func(s *Session) {
		go func() {
			st, err := s.AcceptStream(context.Background())
			if err != nil {
				return
			}
			NewCtl(st, CtlHandlers{OnRequest: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
				select {
				case <-release:
				case <-ctx.Done():
				}
				return nil, nil
			}})
		}()
	})
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := Dial(ctx, URL(srv.URL), testToken)
	require.NoError(t, err)
	st, err := sess.Open(ctx, StreamHeader{T: StreamCtl})
	require.NoError(t, err)
	ctl := NewCtl(st, CtlHandlers{})

	callCtx, callCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer callCancel()
	err = ctl.Call(callCtx, "slow", nil, nil)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	done := make(chan error, 1)
	go func() { done <- ctl.Call(ctx, "slow", nil, nil) }()
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, sess.Close())
	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrCtlClosed)
	case <-ctx.Done():
		t.Fatal("pending call not failed after session close")
	}
	<-ctl.Done()
	assert.Error(t, ctl.Notify("x", nil))
}

// M5：一个 ctl 端点同时至多处理 MaxConcurrentCtlRequests 个对端请求，超出的立即以 busy 应答且不执行。
func TestCtlConcurrentRequestCap(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, MaxConcurrentCtlRequests+1)
	srv := startServer(t, func(s *Session) {
		go func() {
			st, err := s.AcceptStream(context.Background())
			if err != nil {
				return
			}
			NewCtl(st, CtlHandlers{OnRequest: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
				return map[string]bool{"ok": true}, nil
			}})
		}()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := Dial(ctx, URL(srv.URL), testToken)
	require.NoError(t, err)
	defer sess.Close()
	st, err := sess.Open(ctx, StreamHeader{T: StreamCtl})
	require.NoError(t, err)
	ctl := NewCtl(st, CtlHandlers{})

	errs := make(chan error, MaxConcurrentCtlRequests)
	for range MaxConcurrentCtlRequests {
		go func() { errs <- ctl.Call(ctx, "slow", nil, nil) }()
	}
	for range MaxConcurrentCtlRequests {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("handlers did not start")
		}
	}
	// 名额已满：下一个请求得到 busy，处理器没有被调用。
	err = ctl.Call(ctx, "slow", nil, nil)
	var ce *CtlError
	require.True(t, errors.As(err, &ce), "%v", err)
	assert.Equal(t, CodeBusy, ce.Code)
	assert.Len(t, entered, 0)

	close(release)
	for range MaxConcurrentCtlRequests {
		assert.NoError(t, <-errs)
	}
	// 名额释放后恢复正常。
	assert.NoError(t, ctl.Call(ctx, "slow", nil, nil))
}

func TestHeaderCodecPort(t *testing.T) {
	line, err := EncodeHeader(StreamHeader{T: StreamPortal, Port: 8080})
	require.NoError(t, err)
	assert.Equal(t, `{"t":"portal","port":8080}`+"\n", string(line))

	h, err := DecodeHeader(line)
	require.NoError(t, err)
	assert.Equal(t, StreamPortal, h.T)
	assert.Equal(t, 8080, h.Port)

	// 非 portal 流不输出 port。
	line, err = EncodeHeader(StreamHeader{T: StreamFS})
	require.NoError(t, err)
	assert.Equal(t, `{"t":"fs"}`+"\n", string(line))
}

// TestPortalStreamReplyThenRaw：orynd（服务端）打开带端口的 portal 流，orbd（客户端）回一行应答后
// 紧跟原始字节写出；服务端 ReadLine 读出应答行后，Read 从其后继续得到完整原始字节。
func TestPortalStreamReplyThenRaw(t *testing.T) {
	raw := make([]byte, 256<<10)
	_, _ = rand.Read(raw)

	type got struct {
		reply []byte
		sum   [32]byte
		err   error
	}
	res := make(chan got, 1)
	srv := startServer(t, func(s *Session) {
		go func() {
			ctx := context.Background()
			st, err := s.Open(ctx, StreamHeader{T: StreamPortal, Port: 3000})
			if err != nil {
				res <- got{err: err}
				return
			}
			defer st.Close()
			reply, err := st.ReadLine(4096)
			if err != nil {
				res <- got{err: err}
				return
			}
			h := sha256.New()
			if _, err := io.Copy(h, st); err != nil {
				res <- got{err: err}
				return
			}
			var sum [32]byte
			copy(sum[:], h.Sum(nil))
			res <- got{reply: reply, sum: sum}
		}()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := Dial(ctx, URL(srv.URL), testToken)
	require.NoError(t, err)
	defer sess.Close()

	st, err := sess.AcceptStream(ctx)
	require.NoError(t, err)
	assert.Equal(t, StreamPortal, st.Header.T)
	assert.Equal(t, 3000, st.Header.Port)
	// 应答行与原始字节一次写出，确保服务端的缓冲里同时有两者。
	_, err = st.Write(append([]byte(`{"ok":true}`+"\n"), raw...))
	require.NoError(t, err)
	require.NoError(t, st.Close())

	select {
	case g := <-res:
		require.NoError(t, g.err)
		assert.Equal(t, `{"ok":true}`+"\n", string(g.reply))
		assert.Equal(t, sha256.Sum256(raw), g.sum)
	case <-ctx.Done():
		t.Fatal("timeout waiting for portal stream")
	}
}

func TestStreamReadLineLimits(t *testing.T) {
	type got struct {
		first, second []byte
		err1, err2    error
	}
	res := make(chan got, 1)
	srv := startServer(t, func(s *Session) {
		go func() {
			st, err := s.AcceptStream(context.Background())
			if err != nil {
				res <- got{err1: err}
				return
			}
			defer st.Close()
			var g got
			g.first, g.err1 = st.ReadLine(16)
			if g.err1 == nil {
				g.second, g.err2 = st.ReadLine(16)
			}
			res <- g
		}()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := Dial(ctx, URL(srv.URL), testToken)
	require.NoError(t, err)
	defer sess.Close()

	st, err := sess.Open(ctx, StreamHeader{T: StreamPortal, Port: 1})
	require.NoError(t, err)
	_, err = st.Write([]byte("short\n" + "this line is far too long\n"))
	require.NoError(t, err)
	require.NoError(t, st.Close())

	select {
	case g := <-res:
		require.NoError(t, g.err1)
		assert.Equal(t, "short\n", string(g.first))
		assert.ErrorIs(t, g.err2, ErrLineTooLong)
	case <-ctx.Done():
		t.Fatal("timeout")
	}
}
