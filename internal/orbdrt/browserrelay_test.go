package orbdrt

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/link"
)

// fakeInnerRelay 模拟 orb 内的 `omp browser-relay`：提供 /ext、/json/version 与 /cdp。
type fakeInnerRelay struct {
	srv         *http.Server
	ln          net.Listener
	token       string
	extSeen     atomic.Bool
	extConnected chan struct{}
	extIncoming chan string
	extSend     chan string
	stopped     atomic.Bool
}

func startFakeInnerRelay(t *testing.T, port int, token string) (*fakeInnerRelay, func(), error) {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, nil, err
	}
	fr := &fakeInnerRelay{
		ln:           ln,
		token:        token,
		extConnected: make(chan struct{}, 1),
		extIncoming:  make(chan string, 16),
		extSend:      make(chan string, 16),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ext", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != fr.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Origin") != "" {
			http.Error(w, "unexpected origin", http.StatusForbidden)
			return
		}
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		fr.extSeen.Store(true)
		select {
		case fr.extConnected <- struct{}{}:
		default:
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			for {
				select {
				case msg := <-fr.extSend:
					_ = ws.Write(ctx, websocket.MessageText, []byte(msg))
				case <-ctx.Done():
					return
				}
			}
		}()
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			fr.extIncoming <- string(b)
		}
	})
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		host := r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Browser":              "Chrome/136.0.0.0",
			"ompRelayVersion":      "19.0.1",
			"webSocketDebuggerUrl": "ws://" + host + "/cdp",
		})
	})
	mux.HandleFunc("/cdp", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			typ, b, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			_ = ws.Write(r.Context(), typ, b)
		}
	})
	fr.srv = &http.Server{Handler: mux}
	go func() { _ = fr.srv.Serve(ln) }()
	stop := func() {
		fr.stopped.Store(true)
		_ = fr.srv.Close()
		_ = ln.Close()
	}
	return fr, stop, nil
}

func TestBrowserRelayFrontUnboundAndBoundLifecycle(t *testing.T) {
	cp := newFakeCP(t)
	var mu sync.Mutex
	relays := map[int]*fakeInnerRelay{}

	rt := startRuntime(t, cp, func(c *Config) {
		c.BrowserRelayIdleGrace = 120 * time.Millisecond
		c.BrowserRelayStart = func(_ context.Context, port int, token string) (func(), error) {
			fr, stop, err := startFakeInnerRelay(t, port, token)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			relays[port] = fr
			mu.Unlock()
			return stop, nil
		}
	})
	rt.agentVersion = "omp/19.0.1"

	// 1. 线程前门初始化：写出只含 browser.relayUrl 的覆盖层（不写 browser.relay: true）
	frontAddr, statePath, overlayPath, err := rt.browserRelay.ensureThreadFront("T-test1")
	require.NoError(t, err)
	ov, err := os.ReadFile(overlayPath)
	require.NoError(t, err)
	assert.Contains(t, string(ov), `relayUrl: "http://`+frontAddr+`"`)
	assert.NotContains(t, string(ov), "\n  relay:")

	// 2. 未绑定时 GET /json/version 0ms 返回符合 omp probe.ts 契约的 503
	resp, err := http.Get("http://" + frontAddr + "/json/version")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	var info map[string]any
	require.NoError(t, json.Unmarshal(body, &info))
	assert.Equal(t, "19.0.1", info["ompRelayVersion"])
	assert.Equal(t, false, info["extensionSeen"])
	assert.GreaterOrEqual(t, info["uptimeMs"].(float64), float64(60000))

	// 3. 控制面打开 StreamBrowserRelay → 启动内部 relay、建立 /ext WS、双向转发 NDJSON 与前门反代
	cp.ctlNow(t)
	cp.mu.Lock()
	sess := cp.sess
	cp.mu.Unlock()
	require.NotNil(t, sess)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamBrowserRelay, ThreadID: "T-test1"})
	require.NoError(t, err)

	var fr *fakeInnerRelay
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, v := range relays {
			if v.extSeen.Load() {
				fr = v
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond)

	// 状态文件切换为 online=true
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(statePath)
		return err == nil && strings.Contains(string(b), `"online":true`)
	}, 2*time.Second, 20*time.Millisecond)

	// NDJSON 方向 1：orynd → inner /ext
	_, err = st.Write([]byte(`{"t":"hello","tabs":[]}` + "\n"))
	require.NoError(t, err)
	select {
	case got := <-fr.extIncoming:
		assert.Contains(t, got, `"t":"hello"`)
	case <-time.After(2 * time.Second):
		t.Fatal("expected hello frame at inner /ext")
	}

	// NDJSON 方向 2：inner /ext → orynd
	fr.extSend <- `{"t":"rpc","id":1,"op":"createTab","url":"https://example.com"}`
	line, err := bufio.NewReader(st).ReadString('\n')
	require.NoError(t, err)
	assert.Contains(t, line, `"op":"createTab"`)

	// HTTP 反代：GET /json/version 走内部 relay 返回 200 且保留 frontAddr Host
	resp2, err := http.Get("http://" + frontAddr + "/json/version")
	require.NoError(t, err)
	body2, err := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
	assert.Contains(t, string(body2), "ws://"+frontAddr+"/cdp")

	// WS 反代：WS /cdp 双向透传
	cdpWS, _, err := websocket.Dial(ctx, "ws://"+frontAddr+"/cdp", nil)
	require.NoError(t, err)
	require.NoError(t, cdpWS.Write(ctx, websocket.MessageText, []byte(`{"id":9,"method":"Browser.getVersion"}`)))
	_, echo, err := cdpWS.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, `{"id":9,"method":"Browser.getVersion"}`, string(echo))
	_ = cdpWS.CloseNow()

	// 4. 多线程隔离：第二个线程拿到独立前门端口且互不干扰
	frontAddr2, _, _, err := rt.browserRelay.ensureThreadFront("T-test2")
	require.NoError(t, err)
	assert.NotEqual(t, frontAddr, frontAddr2)
	respOther, err := http.Get("http://" + frontAddr2 + "/json/version")
	require.NoError(t, err)
	_ = respOther.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, respOther.StatusCode)

	// 5. 流关闭 → 状态文件立即回 false，空闲宽限期后回收内部 relay
	_ = st.Close()
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(statePath)
		return err == nil && strings.Contains(string(b), `"online":false`)
	}, 2*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		return fr.stopped.Load()
	}, 2*time.Second, 20*time.Millisecond)
}
