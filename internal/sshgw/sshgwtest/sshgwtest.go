// Package sshgwtest 提供测试用的假 SSH 目标机：真实执行命令、提供 sftp 子系统与 direct-tcpip 转发，
// 供 sshgw 的拨号、网关、工具与 SFTP 测试使用。只用于测试。
package sshgwtest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Options 描述假目标机接受的认证方式与行为。
type Options struct {
	AuthorizedKeys      []ssh.PublicKey // 非空时接受这些公钥
	Password            string          // 非空时接受 password 认证
	KeyboardInteractive bool            // 为真且 Password 非空：只提供 keyboard-interactive（单个不回显提示，答案须等于 Password），不提供 password 方法
	HostKey             ssh.Signer      // nil = 随机 ed25519
	AllowDirectTCPIP    bool            // 是否接受 direct-tcpip 通道
	Silent              bool            // 接受 TCP 后不说话（用来测握手超时）
	User                string          // 非空时只接受这个登录名
}

// FakeSSHD 是一台跑在 127.0.0.1 随机端口上的假 SSH 目标机。方法均并发安全。
type FakeSSHD struct {
	opts   Options
	ln     net.Listener
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once

	mu      sync.Mutex
	hostKey ssh.Signer
	raw     map[net.Conn]struct{}
	last    *ssh.ServerConn // 最近一个已登录的连接；断开后清空
	direct  int
	closed  bool
}

// NewFakeSSHD 起一台假目标机，测试结束时自动关闭。
func NewFakeSSHD(t testing.TB, opts Options) *FakeSSHD {
	t.Helper()
	hostKey := opts.HostKey
	if hostKey == nil {
		hostKey = RandomSigner(t)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("sshgwtest: 监听失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &FakeSSHD{
		opts:    opts,
		ln:      ln,
		ctx:     ctx,
		cancel:  cancel,
		hostKey: hostKey,
		raw:     map[net.Conn]struct{}{},
	}
	f.wg.Go(f.acceptLoop)
	t.Cleanup(f.Close)
	return f
}

// RandomSigner 生成一把随机 ed25519 签名密钥，可用作主机密钥或客户端钥匙。
func RandomSigner(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("sshgwtest: 生成密钥失败: %v", err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("sshgwtest: 生成签名器失败: %v", err)
	}
	return s
}

// Addr 返回监听的主机与端口。
func (f *FakeSSHD) Addr() (host string, port int) {
	a := f.ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

// HostPublicKey 返回当前主机公钥（新连接使用的那把）。
func (f *FakeSSHD) HostPublicKey() ssh.PublicKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hostKey.PublicKey()
}

// HostKeyFingerprint 返回当前主机公钥的 SHA256 指纹。
func (f *FakeSSHD) HostKeyFingerprint() string {
	return ssh.FingerprintSHA256(f.HostPublicKey())
}

// SetHostKey 更换主机密钥；之后的新连接使用新密钥，已有连接不受影响。
func (f *FakeSSHD) SetHostKey(s ssh.Signer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hostKey = s
}

// DirectTCPIPCount 返回成功建立的 direct-tcpip 转发次数。
func (f *FakeSSHD) DirectTCPIPCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.direct
}

// SendForwardedTCPIP 对最近一个已登录的客户端连接主动打开一条 forwarded-tcpip 通道。
// 对方拒绝时返回 *ssh.OpenChannelError；对方接受时立即关掉通道并返回 nil。
// 客户端的握手比服务端登记连接（f.last）先返回，所以最多等 5 秒让连接登记上。
func (f *FakeSSHD) SendForwardedTCPIP() error {
	var conn *ssh.ServerConn
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		f.mu.Lock()
		conn = f.last
		f.mu.Unlock()
		if conn != nil || time.Now().After(deadline) {
			break
		}
	}
	if conn == nil {
		return errors.New("sshgwtest: 没有已登录的客户端连接")
	}
	payload := ssh.Marshal(struct {
		Addr       string
		Port       uint32
		OriginAddr string
		OriginPort uint32
	}{"127.0.0.1", 8080, "127.0.0.1", 40000})
	ch, reqs, err := conn.OpenChannel("forwarded-tcpip", payload)
	if err != nil {
		return err
	}
	go ssh.DiscardRequests(reqs)
	// 对方可能已先关掉通道，此时 Close 报 EOF；接受本身已成功。
	_ = ch.Close()
	return nil
}

// Close 关闭监听与全部连接、结束全部子进程。可重复调用。
func (f *FakeSSHD) Close() {
	f.once.Do(func() {
		f.cancel()
		_ = f.ln.Close()
		f.mu.Lock()
		f.closed = true
		for c := range f.raw {
			_ = c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
}

func (f *FakeSSHD) acceptLoop() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			_ = c.Close()
			return
		}
		f.raw[c] = struct{}{}
		f.mu.Unlock()
		f.wg.Go(func() {
			defer func() {
				_ = c.Close()
				f.mu.Lock()
				delete(f.raw, c)
				f.mu.Unlock()
			}()
			f.serveConn(c)
		})
	}
}

// serverConfig 按当前主机密钥为每条连接新建配置，使 SetHostKey 只影响之后的连接。
func (f *FakeSSHD) serverConfig() *ssh.ServerConfig {
	userOK := func(meta ssh.ConnMetadata) error {
		if f.opts.User != "" && meta.User() != f.opts.User {
			return fmt.Errorf("用户 %q 不被接受", meta.User())
		}
		return nil
	}
	cfg := &ssh.ServerConfig{}
	if len(f.opts.AuthorizedKeys) > 0 {
		cfg.PublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if err := userOK(meta); err != nil {
				return nil, err
			}
			for _, k := range f.opts.AuthorizedKeys {
				if string(k.Marshal()) == string(key.Marshal()) {
					return &ssh.Permissions{}, nil
				}
			}
			return nil, errors.New("公钥未授权")
		}
	}
	if f.opts.Password != "" {
		if f.opts.KeyboardInteractive {
			cfg.KeyboardInteractiveCallback = func(meta ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
				if err := userOK(meta); err != nil {
					return nil, err
				}
				answers, err := challenge("", "", []string{"Password: "}, []bool{false})
				if err != nil {
					return nil, err
				}
				if len(answers) != 1 || answers[0] != f.opts.Password {
					return nil, errors.New("密码不对")
				}
				return &ssh.Permissions{}, nil
			}
		} else {
			cfg.PasswordCallback = func(meta ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
				if err := userOK(meta); err != nil {
					return nil, err
				}
				if string(pw) != f.opts.Password {
					return nil, errors.New("密码不对")
				}
				return &ssh.Permissions{}, nil
			}
		}
	}
	f.mu.Lock()
	cfg.AddHostKey(f.hostKey)
	f.mu.Unlock()
	return cfg
}

func (f *FakeSSHD) serveConn(c net.Conn) {
	if f.opts.Silent {
		_, _ = io.Copy(io.Discard, c)
		return
	}
	conn, chans, reqs, err := ssh.NewServerConn(c, f.serverConfig())
	if err != nil {
		return
	}
	f.mu.Lock()
	f.last = conn
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		if f.last == conn {
			f.last = nil
		}
		f.mu.Unlock()
	}()

	// 全局请求（含 keepalive@openssh.com）一律回 false；客户端保活靠收到回复判定在线。
	f.wg.Go(func() {
		for req := range reqs {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	})
	var chWG sync.WaitGroup
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			chWG.Go(func() { f.handleSession(nc) })
		case "direct-tcpip":
			chWG.Go(func() { f.handleDirect(nc) })
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
	_ = conn.Close()
	chWG.Wait()
}

func (f *FakeSSHD) handleDirect(nc ssh.NewChannel) {
	if !f.opts.AllowDirectTCPIP {
		_ = nc.Reject(ssh.Prohibited, "direct-tcpip not allowed")
		return
	}
	var p struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "bad direct-tcpip payload")
		return
	}
	var d net.Dialer
	dctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	target, err := d.DialContext(dctx, "tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
	cancel()
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		_ = target.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	f.mu.Lock()
	f.direct++
	f.mu.Unlock()

	stop := context.AfterFunc(f.ctx, func() {
		_ = target.Close()
		_ = ch.Close()
	})
	defer stop()
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = io.Copy(ch, target)
		_ = ch.CloseWrite()
	})
	_, _ = io.Copy(target, ch)
	if tc, ok := target.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	wg.Wait()
	_ = target.Close()
	_ = ch.Close()
}

// session 是一条会话通道上的状态：至多启动一次 exec / shell / subsystem。
type session struct {
	f       *FakeSSHD
	ch      ssh.Channel
	ctx     context.Context
	mu      sync.Mutex
	started bool
	cmd     *exec.Cmd
	done    sync.WaitGroup
}

func (f *FakeSSHD) handleSession(nc ssh.NewChannel) {
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(f.ctx)
	s := &session{f: f, ch: ch, ctx: ctx}
	stop := context.AfterFunc(f.ctx, func() { _ = ch.Close() })
	defer func() {
		stop()
		// 通道已关：结束仍在跑的进程并等它收尾。
		cancel()
		s.done.Wait()
	}()
	for req := range reqs {
		ok := false
		switch req.Type {
		case "pty-req", "window-change", "env":
			ok = true
		case "exec":
			var p struct{ Command string }
			if ssh.Unmarshal(req.Payload, &p) == nil {
				ok = s.start(false, "sh", "-c", p.Command)
			}
		case "shell":
			ok = s.start(true, "sh")
		case "subsystem":
			var p struct{ Name string }
			if ssh.Unmarshal(req.Payload, &p) == nil && p.Name == "sftp" {
				ok = s.startSFTP()
			}
		case "signal":
			var p struct{ Signal string }
			if ssh.Unmarshal(req.Payload, &p) == nil {
				ok = s.signal(p.Signal)
			}
		}
		if req.WantReply {
			_ = req.Reply(ok, nil)
		}
	}
}

func (s *session) claim() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return false
	}
	s.started = true
	return true
}

// start 启动进程并接上通道；mergeStderr 为真时 stderr 并进 stdout（shell 用）。
// 进程跟随会话的 context：通道关闭或假目标机关闭时被杀掉。
func (s *session) start(mergeStderr bool, name string, args ...string) bool {
	if !s.claim() {
		return false
	}
	cmd := exec.CommandContext(s.ctx, name, args...)
	cmd.Stdout = s.ch
	if mergeStderr {
		cmd.Stderr = s.ch
	} else {
		cmd.Stderr = s.ch.Stderr()
	}
	// 进程组：取消时连同子孙一起杀掉，免得残留进程拖住输出管道。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	go func() {
		_, _ = io.Copy(stdin, s.ch)
		_ = stdin.Close()
	}()
	s.done.Go(func() {
		_ = cmd.Wait()
		code := cmd.ProcessState.ExitCode()
		if code < 0 {
			code = 255
		}
		s.exit(code)
	})
	return true
}

func (s *session) startSFTP() bool {
	if !s.claim() {
		return false
	}
	srv, err := sftp.NewServer(s.ch)
	if err != nil {
		return false
	}
	s.done.Go(func() {
		err := srv.Serve()
		_ = srv.Close()
		code := 0
		if err != nil && !errors.Is(err, io.EOF) {
			code = 1
		}
		s.exit(code)
	})
	return true
}

func (s *session) signal(name string) bool {
	sig, ok := map[string]syscall.Signal{
		"KILL": syscall.SIGKILL, "TERM": syscall.SIGTERM, "INT": syscall.SIGINT, "HUP": syscall.SIGHUP,
	}[name]
	if !ok {
		return false
	}
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return false
	}
	return syscall.Kill(-cmd.Process.Pid, sig) == nil
}

// exit 发 exit-status 后关闭通道。
func (s *session) exit(code int) {
	_, _ = s.ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
	_ = s.ch.Close()
}
