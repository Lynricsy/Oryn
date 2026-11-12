package link

import (
	"net"
	"sync"
)

// StreamListener 是由 link 流喂入的虚拟 net.Listener：调用方把收到的流 Push 进来，
// http.Server 等经 Accept 取走。控制面的 egress 服务与 orbd 的本机 API（fs 流）共用。
type StreamListener struct {
	addr streamAddr
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

// NewStreamListener 返回一个空的虚拟监听；network 作为 Addr 的 Network / String（如 "oryn-egress"）。
func NewStreamListener(network string) *StreamListener {
	return &StreamListener{addr: streamAddr(network), ch: make(chan net.Conn), done: make(chan struct{})}
}

// Push 把一条连接交给 Accept 方；监听已关闭则直接关闭 c。
// 阻塞到被 Accept 取走或监听关闭。
func (l *StreamListener) Push(c net.Conn) {
	select {
	case <-l.done:
		_ = c.Close()
		return
	default:
	}
	select {
	case l.ch <- c:
	case <-l.done:
		_ = c.Close()
	}
}

// Accept 等待下一条被 Push 的连接；监听关闭后返回 net.ErrClosed。
func (l *StreamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// Close 关闭监听（幂等）；之后的 Push 会关闭传入的连接。
func (l *StreamListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

// Addr 返回虚拟地址。
func (l *StreamListener) Addr() net.Addr { return l.addr }

type streamAddr string

func (a streamAddr) Network() string { return string(a) }
func (a streamAddr) String() string  { return string(a) }
