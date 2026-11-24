package orbdrt

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/Lynricsy/Oryn/internal/link"
)

const (
	// browserRelayStartTimeout 是等待 orb 内 omp browser-relay 打印就绪日志的上限。
	browserRelayStartTimeout = 15 * time.Second
	// browserRelayDefaultIdleGrace 是流断开后回收本线程 omp browser-relay 子进程的宽限期。
	browserRelayDefaultIdleGrace = 30 * time.Second
	// browserRelayStopGrace 是 SIGTERM 到 SIGKILL 的等待。
	browserRelayStopGrace = 5 * time.Second
	// browserRelayMaxLineBytes 是 NDJSON 单行上限（兼容 base64 截图）。
	browserRelayMaxLineBytes = 64 << 20
	// envBrowserRelayState 是写给 omp 扩展的状态文件路径。
	envBrowserRelayState = "ORYN_BROWSER_RELAY_STATE"
)

// browserRelayStateFile 落盘给 oryn-orb.ts 读取，判断本线程当前是否在线绑定了主人的本机 Chrome。
type browserRelayStateFile struct {
	Online bool `json:"online"`
}

// browserRelayManager 管理本 orb 上全部线程的本机浏览器中继前门与按需拉起的 omp browser-relay 子进程（M27）。
type browserRelayManager struct {
	r         *Runtime
	idleGrace time.Duration

	mu     sync.Mutex
	closed bool
	fronts map[string]*threadRelayFront
}

func newBrowserRelayManager(r *Runtime) *browserRelayManager {
	grace := r.cfg.BrowserRelayIdleGrace
	if grace <= 0 {
		grace = browserRelayDefaultIdleGrace
	}
	return &browserRelayManager{
		r:         r,
		idleGrace: grace,
		fronts:    map[string]*threadRelayFront{},
	}
}

type threadRelayFront struct {
	mgr       *browserRelayManager
	threadID  string
	ln        net.Listener
	frontPort int
	statePath string
	overlay   string

	mu           sync.Mutex
	closed       bool
	innerPort    int
	innerToken   string
	stopInner    func()
	idleTimer    *time.Timer
	activeStream context.CancelFunc
	streamGen    uint64
}

func (r *Runtime) browserRelayOverlayPath(threadID string) string {
	return filepath.Join(r.home, ".oryn", "threads", threadID, "browser-relay.yml")
}

func (m *browserRelayManager) threadDir(threadID string) string {
	return filepath.Join(m.r.home, ".oryn", "threads", threadID)
}

// ensureThreadFront 为线程准备独立的 loopback relayFront、browser-relay.yml 与状态文件。
func (m *browserRelayManager) ensureThreadFront(threadID string) (frontAddr, statePath, overlayPath string, err error) {
	if !validThreadID(threadID) {
		return "", "", "", fmt.Errorf("browserrelay: invalid thread id %q", threadID)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", "", "", errors.New("browserrelay: manager closed")
	}
	if f := m.fronts[threadID]; f != nil {
		m.mu.Unlock()
		return f.ln.Addr().String(), f.statePath, f.overlay, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		m.mu.Unlock()
		return "", "", "", fmt.Errorf("browserrelay: listen front: %w", err)
	}
	frontPort := 0
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		frontPort = tcp.Port
	}
	dir := m.threadDir(threadID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		_ = ln.Close()
		m.mu.Unlock()
		return "", "", "", fmt.Errorf("browserrelay: mkdir: %w", err)
	}
	overlayPath = filepath.Join(dir, "browser-relay.yml")
	statePath = filepath.Join(dir, "browser-relay-state.json")
	overlayBody := fmt.Appendf(nil, "# 由 orbd 生成（M27）：为本线程配置独立的本机浏览器中继前门（不设 browser.relay: true，保持云桌面默认）。\nbrowser:\n  relayUrl: \"http://%s\"\n", ln.Addr().String())
	if err := writeFileAtomic(overlayPath, overlayBody, 0o600); err != nil {
		_ = ln.Close()
		m.mu.Unlock()
		return "", "", "", fmt.Errorf("browserrelay: write overlay: %w", err)
	}
	f := &threadRelayFront{
		mgr:       m,
		threadID:  threadID,
		ln:        ln,
		frontPort: frontPort,
		statePath: statePath,
		overlay:   overlayPath,
	}
	f.writeState(false)
	m.fronts[threadID] = f
	m.mu.Unlock()

	go f.serve()
	return ln.Addr().String(), statePath, overlayPath, nil
}

func (f *threadRelayFront) writeState(online bool) {
	b, _ := json.Marshal(browserRelayStateFile{Online: online})
	_ = writeFileAtomic(f.statePath, b, 0o600)
}

func (f *threadRelayFront) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.serveConn(c)
	}
}

func (f *threadRelayFront) serveConn(c net.Conn) {
	cr := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(cdpHeadTimeout))
	head, err := readHTTPHead(cr)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return
	}

	f.mu.Lock()
	innerPort := f.innerPort
	f.mu.Unlock()

	if innerPort == 0 {
		// 未绑定在线扩展时快速返回符合 omp probe.ts 契约的 503：
		// 1. status 503 让 ensureRelayDaemon 认为外部 relay 已占端口，不会自启本地 daemon；
		// 2. extensionSeen=false 且 uptimeMs>=60000（>35000）让 waitForRelayExtension 0ms 快速失败；
		// 3. ompRelayVersion 去掉 omp/ 前缀，避免被误判 outdated-relay。
		body, _ := json.Marshal(map[string]any{
			"ompRelayVersion":    stripOmpVersionPrefix(f.mgr.r.agentVersion),
			"error":              "No local Chrome extension bound to this thread. Bind one in Oryn thread header or the Chrome extension popup.",
			"extensionSeen":      false,
			"uptimeMs":           60_000,
			"ompRelayCdpClients": 0,
		})
		_, _ = fmt.Fprintf(c, "HTTP/1.1 503 Service Unavailable\r\nContent-Type: application/json;charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		_ = c.Close()
		return
	}

	up, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(innerPort)), 5*time.Second)
	if err != nil {
		body := []byte(`{"error":"local browser relay process is unreachable"}`)
		_, _ = fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		_ = c.Close()
		return
	}
	client := &bufConn{Conn: c, r: cr}
	if _, err := up.Write(head); err != nil {
		_ = c.Close()
		_ = up.Close()
		return
	}
	pipeConns(client, up)
}

// handleStream 处理控制面主动打开的 StreamBrowserRelay 流。
func (m *browserRelayManager) handleStream(st *link.Stream) {
	defer st.Close()
	tid := st.Header.ThreadID
	if !validThreadID(tid) {
		m.r.log.Warn("browser_relay stream rejected: invalid threadId", "threadId", tid)
		return
	}
	if _, _, _, err := m.ensureThreadFront(tid); err != nil {
		m.r.log.Warn("browser_relay ensure front failed", "thread", tid, "err", err)
		return
	}
	m.mu.Lock()
	f := m.fronts[tid]
	m.mu.Unlock()
	if f == nil {
		return
	}
	f.bridgeStream(st)
}

func (f *threadRelayFront) bridgeStream(st net.Conn) {
	ctx, cancel := context.WithCancel(f.mgr.r.ctx)
	defer cancel()

	// 若同一线程已有旧流，先取消旧流并取消待触发的空闲回收定时器
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	if f.activeStream != nil {
		f.activeStream()
	}
	f.streamGen++
	myGen := f.streamGen
	f.activeStream = cancel
	if f.idleTimer != nil {
		f.idleTimer.Stop()
		f.idleTimer = nil
	}
	f.mu.Unlock()

	port, token, err := f.ensureInnerRelay(ctx)
	if err != nil {
		f.mgr.r.log.Warn("start inner omp browser-relay failed", "thread", f.threadID, "err", err)
		f.clearActiveStream(myGen)
		return
	}

	// 不带 Origin 头拨号 /ext，避免触发 server.ts 的本地扩展自更新检查
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/ext?token=%s", port, token)
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	ws, _, err := websocket.Dial(dctx, wsURL, nil)
	dcancel()
	if err != nil {
		f.mgr.r.log.Warn("dial inner omp browser-relay /ext failed", "thread", f.threadID, "err", err)
		f.clearActiveStream(myGen)
		return
	}
	ws.SetReadLimit(browserRelayMaxLineBytes)
	defer ws.CloseNow()

	f.writeState(true)
	defer func() {
		f.clearActiveStream(myGen)
	}()
	// 任一方向退出时同时取消另一侧
	go func() {
		<-ctx.Done()
		_ = st.Close()
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	// 方向 1：inner omp browser-relay (/ext WS) → orynd (NDJSON 流)
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, data); err != nil {
				continue
			}
			compact.WriteByte('\n')
			if _, err := st.Write(compact.Bytes()); err != nil {
				return
			}
		}
	}()

	// 方向 2：orynd (NDJSON 流) → inner omp browser-relay (/ext WS)
	r := bufio.NewReaderSize(st, 64<<10)
	for {
		line, err := readBrowserRelayLine(r, browserRelayMaxLineBytes)
		if err != nil {
			break
		}
		if len(line) == 0 {
			continue
		}
		wctx, wcancel := context.WithTimeout(ctx, 30*time.Second)
		err = ws.Write(wctx, websocket.MessageText, line)
		wcancel()
		if err != nil {
			break
		}
	}
	cancel()
	wg.Wait()
}

func (f *threadRelayFront) clearActiveStream(gen uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 只有仍是当前活动世代时才刷离线并启动空闲子进程回收（防止新流已顶替后误关）
	if f.streamGen != gen || f.activeStream == nil {
		return
	}
	f.activeStream = nil
	f.writeState(false)
	if f.innerPort != 0 && !f.closed {
		if f.idleTimer != nil {
			f.idleTimer.Stop()
		}
		f.idleTimer = time.AfterFunc(f.mgr.idleGrace, f.reapInnerIfIdle)
	}
}

func (f *threadRelayFront) reapInnerIfIdle() {
	f.mu.Lock()
	if f.activeStream != nil || f.stopInner == nil {
		f.mu.Unlock()
		return
	}
	stop := f.stopInner
	f.stopInner = nil
	f.innerPort = 0
	f.innerToken = ""
	f.idleTimer = nil
	f.mu.Unlock()
	stop()
}

func (f *threadRelayFront) ensureInnerRelay(ctx context.Context) (int, string, error) {
	f.mu.Lock()
	if f.innerPort != 0 && f.stopInner != nil {
		port, tok := f.innerPort, f.innerToken
		f.mu.Unlock()
		return port, tok, nil
	}
	f.mu.Unlock()

	port, err := pickFreeLoopbackPort()
	if err != nil {
		return 0, "", err
	}
	tok, err := randomHex(32)
	if err != nil {
		return 0, "", err
	}

	var stop func()
	if f.mgr.r.cfg.BrowserRelayStart != nil {
		stop, err = f.mgr.r.cfg.BrowserRelayStart(ctx, port, tok)
	} else {
		stop, err = f.startOmpBrowserRelay(ctx, port, tok)
	}
	if err != nil {
		return 0, "", err
	}

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		stop()
		return 0, "", errors.New("browserrelay: front closed")
	}
	// 并发竞态下若另一条流已启动，释放刚启动的这份
	if f.innerPort != 0 && f.stopInner != nil {
		existPort, existTok := f.innerPort, f.innerToken
		f.mu.Unlock()
		stop()
		return existPort, existTok, nil
	}
	f.innerPort = port
	f.innerToken = tok
	f.stopInner = stop
	f.mu.Unlock()
	return port, tok, nil
}

func (f *threadRelayFront) startOmpBrowserRelay(ctx context.Context, port int, token string) (func(), error) {
	bin, err := f.mgr.r.bins.look(f.mgr.r.cfg.AgentBin)
	if err != nil {
		return nil, fmt.Errorf("resolve agent binary %q: %w", f.mgr.r.cfg.AgentBin, err)
	}
	cmd := exec.Command(bin, "browser-relay", "--port", strconv.Itoa(port), "--token", token)
	cmd.Dir = f.mgr.r.home
	cmd.Env = safeRelayEnv(f.mgr.r.home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &limitedBuffer{buf: &stderrBuf, max: 16 << 10}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	stopFn := func() {
		if cmd.Process == nil {
			return
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(browserRelayStopGrace):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}

	readyCh := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "browser relay listening") {
				readyCh <- nil
				// 持续排空 stdout，避免子进程日志写满管道阻塞
				for sc.Scan() {
				}
				return
			}
		}
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg == "" {
			errMsg = "omp browser-relay exited before listening"
		}
		readyCh <- errors.New(errMsg)
	}()

	tctx, cancel := context.WithTimeout(ctx, browserRelayStartTimeout)
	defer cancel()
	select {
	case err := <-readyCh:
		if err != nil {
			stopFn()
			return nil, err
		}
		return stopFn, nil
	case <-tctx.Done():
		stopFn()
		return nil, fmt.Errorf("omp browser-relay ready timeout: %w", tctx.Err())
	}
}

func (f *threadRelayFront) close() {
	f.mu.Lock()
	f.closed = true
	if f.idleTimer != nil {
		f.idleTimer.Stop()
		f.idleTimer = nil
	}
	active := f.activeStream
	f.activeStream = nil
	stop := f.stopInner
	f.stopInner = nil
	f.innerPort = 0
	f.innerToken = ""
	f.mu.Unlock()

	if active != nil {
		active()
	}
	_ = f.ln.Close()
	if stop != nil {
		stop()
	}
	f.writeState(false)
}

func (m *browserRelayManager) reservedPort(port int) bool {
	if m == nil || port <= 0 {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.fronts {
		if f.frontPort == port {
			return true
		}
		f.mu.Lock()
		match := f.innerPort == port
		f.mu.Unlock()
		if match {
			return true
		}
	}
	return false
}

func (m *browserRelayManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	fronts := make([]*threadRelayFront, 0, len(m.fronts))
	for _, f := range m.fronts {
		fronts = append(fronts, f)
	}
	m.fronts = map[string]*threadRelayFront{}
	m.mu.Unlock()

	for _, f := range fronts {
		f.close()
	}
}

func stripOmpVersionPrefix(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "omp/")
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return "0.0.0"
	}
	return v
}

func pickFreeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("browserrelay: unexpected listener addr")
	}
	return tcp.Port, nil
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func safeRelayEnv(home string) []string {
	out := []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
	}
	for _, k := range []string{"LANG", "LC_ALL", "TMPDIR", "XDG_RUNTIME_DIR"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func readBrowserRelayLine(r *bufio.Reader, maxBytes int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxBytes {
			return nil, errors.New("browserrelay: line exceeds limit")
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(buf) > 0 {
				return bytes.TrimSpace(buf), nil
			}
			return nil, err
		}
		return bytes.TrimSpace(buf), nil
	}
}

type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.buf.Len() < l.max {
		rem := l.max - l.buf.Len()
		if len(p) > rem {
			_, _ = l.buf.Write(p[:rem])
		} else {
			_, _ = l.buf.Write(p)
		}
	}
	return len(p), nil
}
