package providerrt

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/unixsock"
)

// Agent 沙箱（M30）的 link 中继：沙箱里的 orbd 不经网络拨控制面，而是连 provider 建的一个本机监听
// （docker：挂进容器的 unix socket；Firecracker：jail 里的 v.sock_<LinkPort>），由 provider 原样转发到 ORYN_URL 的 host:port。
// TLS、WebSocket 与 Bearer 令牌仍是 orbd ↔ orynd 端到端，中继只搬字节。

const (
	// relayMaxConns 是每台沙箱同时在途的中继连接上限，超出的新连接立即关闭。
	relayMaxConns = 8
	// relayDialTimeout 是中继拨目标的时限。
	relayDialTimeout = 10 * time.Second
)

// relayTarget 取 ORYN_URL 的 host:port（http / ws 缺省 80，https / wss 缺省 443）。
func relayTarget(orynURL string) (string, error) {
	u, err := url.Parse(orynURL)
	if err != nil {
		return "", fmt.Errorf("sandbox relay: 解析 ORYN_URL %q: %w", orynURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("sandbox relay: ORYN_URL %q 没有主机名", orynURL)
	}
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "http", "ws":
			port = "80"
		case "https", "wss":
			port = "443"
		default:
			return "", fmt.Errorf("sandbox relay: ORYN_URL %q 的 scheme 不受支持", orynURL)
		}
	}
	return net.JoinHostPort(host, port), nil
}

// linkRelay 把 ln 上的每条连接转发到 target 的 TCP 连接。
type linkRelay struct {
	ln     net.Listener
	target string

	mu     sync.Mutex
	closed bool
	// conns 是在途连接：沙箱侧连接 → 目标侧连接（拨通之前为 nil）。
	conns map[net.Conn]net.Conn
}

// newLinkRelay 在 ln 上开始转发（accept 循环在后台运行，直到 Close）。
func newLinkRelay(ln net.Listener, target string) *linkRelay {
	r := &linkRelay{ln: ln, target: target, conns: map[net.Conn]net.Conn{}}
	go r.serve()
	return r
}

func (r *linkRelay) serve() {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || r.isClosed() {
				return
			}
			slog.Warn("沙箱中继 accept 失败", "target", r.target, "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if !r.track(c) {
			_ = c.Close()
			continue
		}
		go r.pipe(c)
	}
}

func (r *linkRelay) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// track 登记一条新连接；已关闭或在途已满时返回 false。
func (r *linkRelay) track(c net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(r.conns) >= relayMaxConns {
		return false
	}
	r.conns[c] = nil
	return true
}

// pipe 拨目标并双向复制；任一方向结束即关闭两端。
func (r *linkRelay) pipe(c net.Conn) {
	defer func() {
		r.mu.Lock()
		delete(r.conns, c)
		r.mu.Unlock()
	}()
	up, err := net.DialTimeout("tcp", r.target, relayDialTimeout)
	if err != nil {
		slog.Warn("沙箱中继拨目标失败", "target", r.target, "err", err)
		_ = c.Close()
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = up.Close()
		_ = c.Close()
		return
	}
	r.conns[c] = up
	r.mu.Unlock()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
	_ = c.Close()
	_ = up.Close()
	<-done
}

// Close 关监听与全部在途连接。监听的 socket 文件不删（unixsock.Listen 的约定：同一路径可能已被新的中继接管）。
func (r *linkRelay) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	conns := r.conns
	r.conns = map[net.Conn]net.Conn{}
	r.mu.Unlock()
	_ = r.ln.Close()
	for c, up := range conns {
		_ = c.Close()
		if up != nil {
			_ = up.Close()
		}
	}
}

// relaySet 是驱动持有的各沙箱中继（orb id → 中继）。零值可用。
type relaySet struct {
	mu sync.Mutex
	m  map[string]*linkRelay
}

// put 登记 orbID 的中继；已有的旧中继被替换并关闭。
func (s *relaySet) put(orbID string, r *linkRelay) {
	s.mu.Lock()
	if s.m == nil {
		s.m = map[string]*linkRelay{}
	}
	old := s.m[orbID]
	s.m[orbID] = r
	s.mu.Unlock()
	if old != nil && old != r {
		old.Close()
	}
}

// has 报告 orbID 是否已有转发到 target 的中继。
func (s *relaySet) has(orbID, target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.m[orbID]
	return r != nil && r.target == target
}

// drop 关闭并移除 orbID 的中继（没有则什么也不做）。
func (s *relaySet) drop(orbID string) {
	s.mu.Lock()
	r := s.m[orbID]
	delete(s.m, orbID)
	s.mu.Unlock()
	if r != nil {
		r.Close()
	}
}

// closeAll 关闭并移除全部中继。
func (s *relaySet) closeAll() {
	s.mu.Lock()
	m := s.m
	s.m = nil
	s.mu.Unlock()
	for _, r := range m {
		r.Close()
	}
}

// listen 在 path 上建 unix 监听（mode、uid 同 unixsock.Listen）并登记为 orbID 转发到 target 的中继，替换旧的。
func (s *relaySet) listen(orbID, path string, mode os.FileMode, uid int, target string) error {
	ln, err := unixsock.Listen(path, mode, uid)
	if err != nil {
		return fmt.Errorf("sandbox relay: %w", err)
	}
	s.put(orbID, newLinkRelay(ln, target))
	return nil
}
