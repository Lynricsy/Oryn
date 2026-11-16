package orbdrt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// CDP 前门（M8 计划 2.4）：orbd 监听 127.0.0.1:7822，每个连接先读出客户端的 HTTP 请求头、确保浏览器在运行
// （至多 30s），再与 Chromium 自己的 DevTools 端口对接。请求头原样转发（只去掉 Sec-WebSocket-Extensions，
// 不协商压缩，帧因此可读）：Chromium 用请求的 Host 头拼 webSocketDebuggerUrl，omp 以前门地址访问，
// 后续的 WebSocket 自然也走前门。
//
// 用户持有桌面控制时（deskcontrol.go），前门挂起 agent 的命令：普通 HTTP 请求在对接前等待，WebSocket 上
// 客户端发出的每条数据消息（CDP 命令）在转发前等待，至多 CDPHold；超时后不转发，回一条 CDP 错误（HTTP 回 503）。
// 刚因超时回过错误的连接上紧接着到来的命令直接回错误，不再逐条等待（一次突发至多挂起一个 CDPHold）。

// maxCDPConns 是前门的并发连接上限，超出直接关闭。
const maxCDPConns = 64

const (
	// cdpHeadTimeout 是读客户端 / Chromium HTTP 头的上限；cdpMaxHead 是头的字节上限。
	cdpHeadTimeout = 30 * time.Second
	cdpMaxHead     = 64 << 10
	// cdpMaxHeldFrame 是挂起时缓存的单帧上限：更大的帧只能等，超时即断开连接（CDP 命令远小于此）。
	cdpMaxHeldFrame = 16 << 20
	// cdpFailFastWindow：超时回错误后这段时间内到来的命令直接回错误。
	cdpFailFastWindow = time.Second
	// cdpHeldErrorCode 是挂起超时时回给 agent 的 CDP 错误码（JSON-RPC 服务端错误区间）。
	cdpHeldErrorCode = -32000
)

const cdpHeldMessage = "Oryn: 用户已接管桌面，浏览器命令暂停执行。请稍后重试（用户交还后会收到通知），或先做与桌面无关的工作。"

func (d *desktop) serveCDP(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = d.cdpLn.Close() })
	defer stop()
	for {
		c, err := d.cdpLn.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				d.log.Warn("CDP front door accept failed", "err", err)
			}
			return
		}
		select {
		case d.cdpSem <- struct{}{}:
		default:
			_ = c.Close()
			continue
		}
		go func() {
			defer func() { <-d.cdpSem }()
			d.serveCDPConn(ctx, c)
		}()
	}
}

func (d *desktop) serveCDPConn(ctx context.Context, c net.Conn) {
	cr := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(cdpHeadTimeout))
	head, err := readHTTPHead(cr)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return
	}
	ws := headerHas(head, "Upgrade", "websocket")
	if ws {
		head = dropHeader(head, "Sec-WebSocket-Extensions")
	} else if !d.waitUserRelease(ctx, d.ctlT.CDPHold) {
		body, _ := json.Marshal(map[string]string{"error": cdpHeldMessage})
		_, _ = fmt.Fprintf(c, "HTTP/1.1 503 Service Unavailable\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		_ = c.Close()
		return
	}
	sctx, cancel := context.WithTimeout(ctx, browserStartWait)
	err = d.startBrowser(sctx)
	cancel()
	if err != nil {
		d.log.Info("CDP connection refused: browser not available", "err", err)
		_ = c.Close()
		return
	}
	port := d.devtoolsPort()
	if port == 0 {
		_ = c.Close()
		return
	}
	up, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		d.log.Info("dial DevTools failed", "port", port, "err", err)
		_ = c.Close()
		return
	}
	client := &bufConn{Conn: c, r: cr}
	if _, err := up.Write(head); err != nil {
		_ = c.Close()
		_ = up.Close()
		return
	}
	if !ws {
		pipeConns(client, up)
		return
	}
	ur := bufio.NewReader(up)
	_ = up.SetReadDeadline(time.Now().Add(cdpHeadTimeout))
	resp, err := readHTTPHead(ur)
	_ = up.SetReadDeadline(time.Time{})
	if err == nil {
		_, err = c.Write(resp)
	}
	if err != nil {
		_ = c.Close()
		_ = up.Close()
		return
	}
	upstream := &bufConn{Conn: up, r: ur}
	if !bytes.HasPrefix(resp, []byte("HTTP/1.1 101")) {
		pipeConns(client, upstream)
		return
	}
	d.proxyCDPSocket(ctx, client, upstream)
}

// bufConn 是读方向带缓冲的连接（HTTP 头之后已读入缓冲的字节不能丢）。
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *bufConn) CloseWrite() error          { closeWrite(b.Conn); return nil }

// readHTTPHead 读到空行为止（含），至多 cdpMaxHead 字节。
func readHTTPHead(r *bufio.Reader) ([]byte, error) {
	var head []byte
	for {
		line, err := r.ReadSlice('\n')
		head = append(head, line...)
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
		if len(head) > cdpMaxHead {
			return nil, errors.New("cdp: HTTP head too large")
		}
		if err == nil && (bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n"))) {
			return head, nil
		}
	}
}

// headerLines 遍历请求 / 响应头的各行（不含起始行与结尾空行），fn 返回 false 时停止。
func headerLines(head []byte, fn func(name, value []byte, line []byte) bool) {
	lines := bytes.SplitAfter(head, []byte("\n"))
	for _, line := range lines[1:] {
		k, v, ok := bytes.Cut(bytes.TrimRight(line, "\r\n"), []byte(":"))
		if !ok {
			continue
		}
		if !fn(bytes.TrimSpace(k), bytes.TrimSpace(v), line) {
			return
		}
	}
}

// headerHas 报告 head 里名为 name 的头是否包含 token（均不区分大小写）。
func headerHas(head []byte, name, token string) bool {
	found := false
	headerLines(head, func(k, v, _ []byte) bool {
		if bytes.EqualFold(k, []byte(name)) {
			for _, t := range bytes.Split(v, []byte(",")) {
				if bytes.EqualFold(bytes.TrimSpace(t), []byte(token)) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// dropHeader 返回去掉名为 name 的头之后的 head。
func dropHeader(head []byte, name string) []byte {
	lines := bytes.SplitAfter(head, []byte("\n"))
	out := append([]byte(nil), lines[0]...)
	for _, line := range lines[1:] {
		k, _, ok := bytes.Cut(line, []byte(":"))
		if ok && bytes.EqualFold(bytes.TrimSpace(k), []byte(name)) {
			continue
		}
		out = append(out, line...)
	}
	return out
}

// ---- WebSocket 帧 ----

// wsHeader 是一个 WebSocket 帧头（RFC 6455 §5.2）；raw 是原始字节。
type wsHeader struct {
	raw    []byte
	fin    bool
	op     byte
	masked bool
	mask   [4]byte
	n      uint64
}

func readWSHeader(r io.Reader) (wsHeader, error) {
	var h wsHeader
	var b [14]byte
	if _, err := io.ReadFull(r, b[:2]); err != nil {
		return h, err
	}
	h.fin, h.op, h.masked = b[0]&0x80 != 0, b[0]&0x0f, b[1]&0x80 != 0
	size := 2
	switch n := b[1] & 0x7f; n {
	case 126:
		if _, err := io.ReadFull(r, b[2:4]); err != nil {
			return h, err
		}
		h.n, size = uint64(binary.BigEndian.Uint16(b[2:4])), 4
	case 127:
		if _, err := io.ReadFull(r, b[2:10]); err != nil {
			return h, err
		}
		h.n, size = binary.BigEndian.Uint64(b[2:10]), 10
	default:
		h.n = uint64(n)
	}
	if h.masked {
		if _, err := io.ReadFull(r, b[size:size+4]); err != nil {
			return h, err
		}
		copy(h.mask[:], b[size:size+4])
		size += 4
	}
	h.raw = append([]byte(nil), b[:size]...)
	return h, nil
}

// wsTextFrame 编码一个服务端（不加掩码）的完整文本帧。
func wsTextFrame(payload []byte) []byte {
	out := []byte{0x81}
	switch n := len(payload); {
	case n < 126:
		out = append(out, byte(n))
	case n <= 0xffff:
		out = append(out, 126, byte(n>>8), byte(n))
	default:
		out = append(out, 127)
		out = binary.BigEndian.AppendUint64(out, uint64(n))
	}
	return append(out, payload...)
}

// proxyCDPSocket 在 101 之后逐帧转发：客户端 → Chromium 方向按控制状态挂起数据消息；Chromium → 客户端
// 方向逐帧整帧写出（wmu 保证注入的错误帧不会插进别的帧中间）。
func (d *desktop) proxyCDPSocket(ctx context.Context, client, up *bufConn) {
	var wmu sync.Mutex
	done := make(chan struct{}, 2)
	go func() {
		_ = d.forwardCDPCommands(ctx, client, up, &wmu)
		closeWrite(up)
		done <- struct{}{}
	}()
	go func() {
		_ = copyWSFrames(client, up, &wmu)
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	deadline := time.Now().Add(portalDrainTimeout)
	_ = client.SetDeadline(deadline)
	_ = up.SetDeadline(deadline)
	<-done
	_ = client.Close()
	_ = up.Close()
}

// copyWSFrames 把 src 的帧逐个整帧写到 dst，每帧持有 wmu。
func copyWSFrames(dst io.Writer, src io.Reader, wmu *sync.Mutex) error {
	for {
		h, err := readWSHeader(src)
		if err != nil {
			return err
		}
		wmu.Lock()
		_, err = dst.Write(h.raw)
		if err == nil {
			_, err = io.CopyN(dst, src, int64(h.n))
		}
		wmu.Unlock()
		if err != nil {
			return err
		}
	}
}

// forwardCDPCommands 转发客户端的帧。控制帧与续帧直接转发；数据消息的首帧在用户持有控制时先挂起。
func (d *desktop) forwardCDPCommands(ctx context.Context, client, up io.ReadWriter, wmu *sync.Mutex) error {
	var failFastUntil time.Time
	for {
		h, err := readWSHeader(client)
		if err != nil {
			return err
		}
		gated := (h.op == 1 || h.op == 2) && d.userHeld()
		if !gated {
			if _, err := up.Write(h.raw); err != nil {
				return err
			}
			if _, err := io.CopyN(up, client, int64(h.n)); err != nil {
				return err
			}
			continue
		}
		if h.n > cdpMaxHeldFrame || !h.fin {
			// 无法缓存整条消息：只能等；超时则断开（agent 侧按连接断开处理并重连）
			if !d.waitUserRelease(ctx, d.ctlT.CDPHold) {
				return errors.New("cdp: command held too long")
			}
			if _, err := up.Write(h.raw); err != nil {
				return err
			}
			if _, err := io.CopyN(up, client, int64(h.n)); err != nil {
				return err
			}
			continue
		}
		payload := make([]byte, h.n)
		if _, err := io.ReadFull(client, payload); err != nil {
			return err
		}
		if time.Now().Before(failFastUntil) || !d.waitUserRelease(ctx, d.ctlT.CDPHold) {
			if !d.userHeld() {
				// 恰好在超时的同时交还：照常转发
				if err := writeFrame(up, h, payload); err != nil {
					return err
				}
				continue
			}
			if err := replyHeld(client, h, payload, wmu); err != nil {
				return err
			}
			failFastUntil = time.Now().Add(cdpFailFastWindow)
			continue
		}
		if err := writeFrame(up, h, payload); err != nil {
			return err
		}
	}
}

func writeFrame(w io.Writer, h wsHeader, payload []byte) error {
	if _, err := w.Write(h.raw); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// replyHeld 对一条被挂起超时的 CDP 命令回错误（同一 id / sessionId）；解析不出 id 时报错断开。
func replyHeld(client io.Writer, h wsHeader, payload []byte, wmu *sync.Mutex) error {
	plain := payload
	if h.masked {
		plain = make([]byte, len(payload))
		for i, b := range payload {
			plain[i] = b ^ h.mask[i%4]
		}
	}
	var cmd struct {
		ID        *json.RawMessage `json:"id"`
		SessionID string           `json:"sessionId,omitempty"`
	}
	if json.Unmarshal(plain, &cmd) != nil || cmd.ID == nil {
		return errors.New("cdp: held message is not a CDP command")
	}
	resp := map[string]any{"id": cmd.ID, "error": map[string]any{"code": cdpHeldErrorCode, "message": cdpHeldMessage}}
	if cmd.SessionID != "" {
		resp["sessionId"] = cmd.SessionID
	}
	b, _ := json.Marshal(resp)
	wmu.Lock()
	defer wmu.Unlock()
	_, err := client.Write(wsTextFrame(b))
	return err
}
