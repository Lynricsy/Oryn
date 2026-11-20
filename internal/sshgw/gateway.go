package sshgw

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const serverVersion = "SSH-2.0-Oryn"

// 会话通道里 orb→上游 原样转发的请求；其余（agent 转发、X11 等）一律拒绝。
var forwardToUpstream = map[string]bool{
	"pty-req": true, "shell": true, "exec": true, "subsystem": true, "env": true,
	"window-change": true, "signal": true, "break": true, "eow@openssh.com": true,
}

// 会话通道里 上游→orb 原样转发的请求。
var forwardToOrb = map[string]bool{
	"exit-status": true, "exit-signal": true, "eow@openssh.com": true, "keepalive@openssh.com": true,
}

// ServeStream 服务 orb 发来的一条 `ssh` 流：它是 orb 内到网关的一条 TCP 连接，其后是裸 SSH 字节流。
// 协调宿主不接入网关。orb 不认证客户端，按 orb 可见的主机放行；runner（M21）只接受登记过的线程公钥，按该线程可见的
// 主机放行（threadkeys.go）。流由调用方关闭。
func (s *Service) ServeStream(ctx context.Context, orbID string, _ link.StreamHeader, st *link.Stream) {
	orb, err := s.orbs.Get(ctx, orbID)
	if err != nil {
		slog.Warn("SSH 网关：读取 orb 失败", "orb", orbID, "err", err)
		return
	}
	if orb.Coordinator {
		slog.Warn("SSH 网关：协调宿主不可用", "orb", orbID)
		return
	}
	s.serveConn(ctx, orb, st)
}

// gwConn 是一条网关连接：orb 侧的 SSH 服务端连接与上游的 SSH 客户端连接。
type gwConn struct {
	s *Service
	// v 是这条连接的可见性主体：orb，或 runner 上经公钥认出的线程（认证成功前只有 orb）。
	v        viewer
	threadID string // 审计记的线程：runner 为认出的线程，orb 为其上恰好一条线程时的那条
	c        net.Conn
	host     db.SshHost
	up       *upstream
	untrack  func()
	// untrackKey 把 runner 连接从其公钥下摘掉（trackKeyConn）；orb 连接为 nil。
	untrackKey func()
	once       sync.Once
}

// close 关掉 orb 侧的底层连接与上游整条链（幂等）。
func (g *gwConn) close() {
	g.once.Do(func() {
		_ = g.c.Close()
		if g.up != nil {
			g.up.Close()
		}
	})
}

// permThread 是 runner 连接的 ssh.Permissions 里记认出的线程的扩展键。
const permThread = "oryn-thread"

// serveConn 在 c 上当 SSH 服务端：按登录名选主机、拨通上游后双向代理，任一侧结束即两侧全关。
func (s *Service) serveConn(ctx context.Context, orb db.Orb, c net.Conn) {
	g := &gwConn{s: s, v: orbViewer(orb), c: c}
	defer func() {
		if g.untrack != nil {
			g.untrack()
		}
		if g.untrackKey != nil {
			g.untrackKey()
		}
	}()
	defer g.close()
	cfg := &ssh.ServerConfig{ServerVersion: serverVersion}
	if orbs.IsRunner(orb) {
		// runner：PublicKeyCallback 只按公钥认出线程（公钥查询时也会调用，不拨号）；客户端证明持有私钥之后才在
		// VerifiedPublicKeyCallback 里按该线程可见的主机拨号。
		cfg.PublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			t, err := s.runnerThread(ctx, orb, key)
			if err != nil {
				if !errors.Is(err, errUnknownKey) {
					slog.Warn("SSH 网关：查线程失败", "orb", orb.ID, "err", err)
				}
				s.audit(ctx, auditRow{source: srcOrb, orbID: orb.ID, hostName: meta.User(), kind: "deny", detail: errUnknownKey.Error()})
				return nil, errUnknownKey
			}
			return &ssh.Permissions{Extensions: map[string]string{permThread: t.ID}}, nil
		}
		cfg.VerifiedPublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey, perms *ssh.Permissions, _ string) (*ssh.Permissions, error) {
			if g.up != nil {
				return perms, nil
			}
			// 认出之后到这里之间线程可能已被归档或公钥已注销：再认一次，并以线程行的当前项目判定可见性
			t, err := s.runnerThread(ctx, orb, key)
			if err != nil || t.ID != perms.Extensions[permThread] {
				return nil, errUnknownKey
			}
			g.v, g.threadID = threadViewer(orb, t), t.ID
			if err := g.auth(ctx, meta.User()); err != nil {
				return nil, &ssh.BannerError{Err: err, Message: "oryn: " + err.Error() + "\n"}
			}
			// 挂到公钥下：该公钥之后被注销或被同线程新公钥替换时，这条连接随之关闭。挂上在拨通之后（g.up 已写入，
			// 关闭方经 keys.mu 看得到它）；认证到这里之间公钥已失效则作废刚拨通的上游。
			untrack, ok := s.trackKeyConn(key, t.ID, g.close)
			if !ok {
				if g.untrack != nil {
					g.untrack()
					g.untrack = nil
				}
				g.up.Close()
				g.up = nil
				return nil, errUnknownKey
			}
			g.untrackKey = untrack
			return perms, nil
		}
	} else {
		g.threadID = s.threadOf(ctx, orb.ID)
		cfg.NoClientAuth = true
		cfg.NoClientAuthCallback = func(meta ssh.ConnMetadata) (*ssh.Permissions, error) {
			if g.up != nil {
				return &ssh.Permissions{}, nil
			}
			if err := g.auth(ctx, meta.User()); err != nil {
				return nil, &ssh.BannerError{Err: err, Message: "oryn: " + err.Error() + "\n"}
			}
			return &ssh.Permissions{}, nil
		}
	}
	cfg.AddHostKey(s.signer)
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go func() { _ = g.up.client.Wait(); g.close() }()
	stop := context.AfterFunc(ctx, g.close)
	defer stop()
	go g.globalRequests(reqs)

	var wg sync.WaitGroup
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			wg.Go(func() { g.session(ctx, nc) })
		case "direct-tcpip":
			wg.Go(func() { g.forward(ctx, nc) })
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
	g.close()
	wg.Wait()
}

// auth 按登录名找主机并拨通上游；失败时记审计并返回给 orb 看的原因。
func (g *gwConn) auth(ctx context.Context, name string) error {
	s := g.s
	orbID := g.v.orb.ID
	since := s.snapshot() // 先取快照再读主机：之后的任何修改都会让登记失败
	h, deny, err := s.resolve(ctx, g.v, name)
	if err != nil {
		slog.Warn("SSH 网关：查主机失败", "orb", orbID, "err", err)
		return errors.New("读取主机目录失败")
	}
	if deny != "" {
		s.audit(ctx, auditRow{source: srcOrb, orbID: orbID, threadID: g.threadID, host: h, hostName: name,
			kind: "deny", detail: deny})
		return errors.New(deny)
	}
	up, err := s.dial(ctx, h)
	if err == nil {
		// 拨通即登记（握手还没完成时主机被改也能断开）；拨号期间链上主机被改过则作废。
		// g.up 先于登记写入：登记后的并发 invalidate 经锁看得到它。
		g.host, g.up = h, up
		var ok bool
		if g.untrack, ok = s.track(up.hostIDs, since, g.close); !ok {
			g.up = nil
			up.Close()
			err = errStale
		}
	}
	if err != nil {
		s.audit(ctx, auditRow{source: srcOrb, orbID: orbID, threadID: g.threadID, host: h, kind: "connect", detail: err.Error()})
		return err
	}
	s.audit(ctx, auditRow{source: srcOrb, orbID: orbID, threadID: g.threadID, host: h, kind: "connect", ok: true})
	return nil
}

// globalRequests 处理 orb 的全局请求：保活本地应答，其余（含远程转发）拒绝。
func (g *gwConn) globalRequests(reqs <-chan *ssh.Request) {
	for r := range reqs {
		if r.WantReply {
			_ = r.Reply(r.Type == "keepalive@openssh.com", nil)
		}
	}
}

// session 代理一条会话通道。
func (g *gwConn) session(ctx context.Context, nc ssh.NewChannel) {
	upCh, upReqs, err := g.up.client.OpenChannel("session", nil)
	if err != nil {
		rejectLike(nc, err)
		return
	}
	orbCh, orbReqs, err := nc.Accept()
	if err != nil {
		_ = upCh.Close()
		return
	}
	start := time.Now()
	var (
		mu       sync.Mutex
		kind     string
		detail   string
		exitCode *int64
	)

	// replyMu：转发中的请求持读锁直到把应答回给 orb；关 orb 侧通道前取写锁，
	// 免得命令瞬间结束时通道先关、orb 等不到 exec 的应答。
	var replyMu sync.RWMutex
	var reqWG sync.WaitGroup
	// orb → 上游：白名单内的请求原样转发并回传应答。
	reqWG.Go(func() {
		for r := range orbReqs {
			if !forwardToUpstream[r.Type] {
				if r.WantReply {
					_ = r.Reply(false, nil)
				}
				continue
			}
			switch r.Type {
			case "exec", "subsystem":
				var p struct{ Value string }
				if ssh.Unmarshal(r.Payload, &p) == nil {
					mu.Lock()
					if kind == "" {
						kind, detail = r.Type, p.Value
					}
					mu.Unlock()
				}
			case "shell":
				mu.Lock()
				if kind == "" {
					kind = "shell"
				}
				mu.Unlock()
			}
			replyMu.RLock()
			ok, err := upCh.SendRequest(r.Type, r.WantReply, r.Payload)
			if r.WantReply {
				_ = r.Reply(ok && err == nil, nil)
			}
			replyMu.RUnlock()
		}
		// orb 关了通道
		_ = upCh.Close()
	})
	// 上游 → orb
	upDone := make(chan struct{})
	go func() {
		defer close(upDone)
		for r := range upReqs {
			if !forwardToOrb[r.Type] {
				if r.WantReply {
					_ = r.Reply(false, nil)
				}
				continue
			}
			if r.Type == "exit-status" {
				var p struct{ Status uint32 }
				if ssh.Unmarshal(r.Payload, &p) == nil {
					mu.Lock()
					exitCode = new(int64(p.Status))
					mu.Unlock()
				}
			}
			ok, err := orbCh.SendRequest(r.Type, r.WantReply, r.Payload)
			if r.WantReply {
				_ = r.Reply(ok && err == nil, nil)
			}
		}
	}()

	var outWG sync.WaitGroup
	outWG.Go(func() { _, _ = io.Copy(orbCh, upCh) })
	outWG.Go(func() { _, _ = io.Copy(orbCh.Stderr(), upCh.Stderr()) })
	go func() {
		_, _ = io.Copy(upCh, orbCh)
		_ = upCh.CloseWrite()
	}()
	go func() { _, _ = io.Copy(upCh.Stderr(), orbCh.Stderr()) }()

	// 上游的输出拷完、请求流结束（通道已关）之后再关 orb 侧，保证 exit-status 先于关闭送达。
	outWG.Wait()
	_ = orbCh.CloseWrite()
	<-upDone
	replyMu.Lock()
	_ = orbCh.Close()
	replyMu.Unlock()
	_ = upCh.Close()
	reqWG.Wait()

	mu.Lock()
	k, d, code := kind, detail, exitCode
	mu.Unlock()
	if k == "" {
		return
	}
	ok := true
	if k == "exec" {
		ok = code != nil && *code == 0
	} else if code != nil {
		ok = *code == 0
	}
	g.s.audit(ctx, auditRow{
		source: srcOrb, orbID: g.v.orb.ID, threadID: g.threadID, host: g.host, kind: k, detail: d,
		exitCode: code, durationMs: new(time.Since(start).Milliseconds()), ok: ok,
	})
}

// forward 代理一条本地端口转发（direct-tcpip），只在主机开了「允许端口转发」时放行。
func (g *gwConn) forward(ctx context.Context, nc ssh.NewChannel) {
	var p struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}
	target := "?"
	if ssh.Unmarshal(nc.ExtraData(), &p) == nil {
		target = net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
	}
	row := auditRow{source: srcOrb, orbID: g.v.orb.ID, threadID: g.threadID, host: g.host, kind: "forward", detail: target}
	if !g.host.AllowForward {
		msg := fmt.Sprintf("oryn: 主机 %s 未开启端口转发", g.host.Name)
		_ = nc.Reject(ssh.Prohibited, msg)
		row.detail = target + "：" + msg
		g.s.audit(ctx, row)
		return
	}
	upCh, upReqs, err := g.up.client.OpenChannel("direct-tcpip", nc.ExtraData())
	if err != nil {
		rejectLike(nc, err)
		row.detail = target + "：" + err.Error()
		g.s.audit(ctx, row)
		return
	}
	orbCh, orbReqs, err := nc.Accept()
	if err != nil {
		_ = upCh.Close()
		return
	}
	row.ok = true
	g.s.audit(ctx, row)
	go ssh.DiscardRequests(upReqs)
	go ssh.DiscardRequests(orbReqs)
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = io.Copy(upCh, orbCh); _ = upCh.CloseWrite() })
	wg.Go(func() { _, _ = io.Copy(orbCh, upCh); _ = orbCh.CloseWrite() })
	wg.Wait()
	_ = orbCh.Close()
	_ = upCh.Close()
}

// rejectLike 按上游拒绝的原因拒绝 orb 的通道。
func rejectLike(nc ssh.NewChannel, err error) {
	var oe *ssh.OpenChannelError
	if errors.As(err, &oe) {
		_ = nc.Reject(oe.Reason, oe.Message)
		return
	}
	_ = nc.Reject(ssh.ConnectionFailed, err.Error())
}
