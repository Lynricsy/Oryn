// Package fcguest 是 Firecracker 宿主 ↔ guest 的控制协议（M11b）：guest 内作 PID 1 的 orbd（guest-init）
// 在 vsock 端口 Port 上监听；宿主经 Firecracker 的 vsock unix socket 连入，一条连接一问一答，
// 每个方向各一行 JSON（以 '\n' 结尾，不含换行至多 MaxLine 字节）。
package fcguest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Port 是 guest 内 orbd guest-init 监听的 vsock 端口。
const Port uint32 = 5005

// MaxLine 是每个方向一行 JSON 的上限（不含结尾换行）。
const MaxLine = 64 << 10

// Request.Op 的取值。
const (
	OpStart    = "start"    // 首次启动：对时、写 hostname / hosts / resolv.conf / 令牌，拉起 orbd run（已在运行时等同 token）
	OpToken    = "token"    // 对时、重写令牌文件、SIGHUP orbd
	OpSync     = "sync"     // sync(2)
	OpFSFreeze = "fsfreeze" // 冻结可写盘的文件系统（120 秒内没等到 fsthaw 自行解冻）
	OpFSThaw   = "fsthaw"   // 解冻
	OpShutdown = "shutdown" // SIGTERM orbd、至多等 GraceSeconds、杀掉其余进程、sync，应答后重启（Firecracker 随之退出）
	OpPing     = "ping"     // 存活探测
)

// Request 是宿主发给 guest 的一行请求。
type Request struct {
	Op           string   `json:"op"`                     // start | token | sync | fsfreeze | fsthaw | shutdown | ping
	URL          string   `json:"url,omitempty"`          // start：ORYN_URL
	OrbID        string   `json:"orbId,omitempty"`        // start
	Token        string   `json:"token,omitempty"`        // start / token：本代 orb 令牌
	DNS          []string `json:"dns,omitempty"`          // start
	NowMs        int64    `json:"nowMs,omitempty"`        // start / token：宿主时间，guest 据此对时
	CAPEM        string   `json:"caPem,omitempty"`        // start：orb link 的 TLS CA（PEM，可空）；guest 原样作为 ORYN_ORB_CA_PEM 交给 orbd
	GraceSeconds int      `json:"graceSeconds,omitempty"` // shutdown
}

// Response 是 guest 的一行应答。
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ErrLineTooLong 表示一行超过 MaxLine。
var ErrLineTooLong = fmt.Errorf("fcguest: 一行超过 %d 字节", MaxLine)

// RemoteError 是 guest 应答 ok=false 时 Call 返回的错误。
type RemoteError struct {
	Op  string
	Msg string
}

func (e *RemoteError) Error() string { return fmt.Sprintf("fcguest %s: %s", e.Op, e.Msg) }

// ReadRequest 读一行请求（guest 端）。
func ReadRequest(r *bufio.Reader) (Request, error) {
	var req Request
	err := readJSON(r, &req)
	return req, err
}

// WriteRequest 写一行请求（宿主端）。
func WriteRequest(w io.Writer, req Request) error { return writeJSON(w, req) }

// ReadResponse 读一行应答（宿主端）。
func ReadResponse(r *bufio.Reader) (Response, error) {
	var resp Response
	err := readJSON(r, &resp)
	return resp, err
}

// WriteResponse 写一行应答（guest 端）。
func WriteResponse(w io.Writer, resp Response) error { return writeJSON(w, resp) }

// readLine 读到 '\n' 为止并去掉它；超过 MaxLine 返回 ErrLineTooLong，没读到换行就结束返回 io.ErrUnexpectedEOF（一个字节都没有时为 io.EOF）。
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > limit+1 {
			return nil, ErrLineTooLong
		}
		line = append(line, chunk...)
		switch {
		case err == nil:
			return line[:len(line)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}

func readJSON(r *bufio.Reader, v any) error {
	line, err := readLine(r, MaxLine)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("fcguest: 无效的 JSON 行: %w", err)
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxLine {
		return ErrLineTooLong
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// maxHandshakeLine 是 Firecracker 握手应答（"OK <宿主端口>"）的长度上限。
const maxHandshakeLine = 64

// Handshake 在 Firecracker 的 vsock unix socket 连接上做宿主发起的握手：写 "CONNECT <port>\n"，
// 读到 "OK <n>\n" 后返回同一连接上的读缓冲（之后的应答必须从它读）。Firecracker 连不上 guest 端口时会直接关闭连接。
func Handshake(rw io.ReadWriter, port uint32) (*bufio.Reader, error) {
	if _, err := fmt.Fprintf(rw, "CONNECT %d\n", port); err != nil {
		return nil, fmt.Errorf("fcguest: vsock 握手: %w", err)
	}
	r := bufio.NewReader(rw)
	line, err := readLine(r, maxHandshakeLine)
	if err != nil {
		return nil, fmt.Errorf("fcguest: vsock 握手（guest 端口 %d 未监听？）: %w", port, err)
	}
	rest, ok := bytes.CutPrefix(line, []byte("OK "))
	if !ok {
		return nil, fmt.Errorf("fcguest: vsock 握手应答 %q 无效", line)
	}
	if _, err := strconv.ParseUint(strings.TrimSpace(string(rest)), 10, 32); err != nil {
		return nil, fmt.Errorf("fcguest: vsock 握手应答 %q 无效", line)
	}
	return r, nil
}

// Call 经 Firecracker 的 vsock unix socket（jail 里的 v.sock）向 guest 的 Port 发一个请求并等应答：
// 拨号、握手、写请求、读应答，整个过程受 ctx 约束。传输错误原样返回；guest 应答 ok=false 时返回 *RemoteError。
func Call(ctx context.Context, udsPath string, req Request) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", udsPath)
	if err != nil {
		return fmt.Errorf("fcguest: 连接 %s: %w", udsPath, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	r, err := Handshake(conn, Port)
	if err == nil {
		err = WriteRequest(conn, req)
	}
	var resp Response
	if err == nil {
		resp, err = ReadResponse(r)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("fcguest %s: %w", req.Op, ctxErr)
		}
		return fmt.Errorf("fcguest %s: %w", req.Op, err)
	}
	if !resp.OK {
		return &RemoteError{Op: req.Op, Msg: resp.Error}
	}
	return nil
}
