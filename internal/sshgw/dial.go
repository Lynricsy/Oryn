package sshgw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	connectTimeout = 10 * time.Second // 每一跳：TCP 建连 + SSH 握手
	dialBudget     = 20 * time.Second // 整条链；必须小于 Core 的 30 秒 helper 超时
	keepaliveEvery = 30 * time.Second
	clientVersion  = "SSH-2.0-Oryn"
)

// HostKeyMismatchError 是目标机的主机公钥与固定的不一致。
type HostKeyMismatchError struct {
	Host, Expected, Got string
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("主机 %q 的指纹变了（期望 %s，收到 %s）；确认无误后到 设置 → SSH 主机 里重置指纹", e.Host, e.Expected, e.Got)
}

// upstream 是拨通的一条链：目标机与途经的跳板。
type upstream struct {
	client  *ssh.Client   // 目标机
	jumps   []*ssh.Client // 跳板，外层在前
	hostIDs []string      // 链上全部主机 id（含目标）
}

// Close 先关目标机，再从内到外关跳板。
func (u *upstream) Close() {
	if u.client != nil {
		_ = u.client.Close()
	}
	for i := len(u.jumps) - 1; i >= 0; i-- {
		_ = u.jumps[i].Close()
	}
}

// chain 把目标解析成整条链（外层跳板在前，目标在末），并复查环、深度与跳板启用状态（数据可能在校验之后被改）。
func (s *Service) chain(ctx context.Context, target db.SshHost) ([]db.SshHost, error) {
	hops := []db.SshHost{target}
	seen := map[string]bool{target.ID: true}
	cur := target
	for cur.JumpHostID != nil {
		j, err := s.st.Q.GetSshHost(ctx, *cur.JumpHostID)
		if err != nil {
			return nil, fmt.Errorf("读取 %q 的跳板: %w", cur.Name, err)
		}
		if seen[j.ID] {
			return nil, errors.New("跳板链存在循环")
		}
		if len(hops) > maxJumpChain {
			return nil, fmt.Errorf("跳板链超过 %d 级", maxJumpChain)
		}
		if !j.Enabled {
			return nil, fmt.Errorf("跳板 %q 已停用", j.Name)
		}
		seen[j.ID] = true
		hops = append([]db.SshHost{j}, hops...)
		cur = j
	}
	return hops, nil
}

// dial 沿跳板链拨到目标机并登录，整条链受 dialBudget 约束。任何一跳失败都会关掉已建好的连接。
func (s *Service) dial(ctx context.Context, target db.SshHost) (*upstream, error) {
	ctx, cancel := context.WithTimeout(ctx, dialBudget)
	defer cancel()
	hops, err := s.chain(ctx, target)
	if err != nil {
		return nil, err
	}
	up := &upstream{}
	for _, h := range hops {
		up.hostIDs = append(up.hostIDs, h.ID)
	}
	var prev *ssh.Client
	for i, h := range hops {
		c, err := s.dialHop(ctx, h, prev)
		if err != nil {
			up.Close()
			if i < len(hops)-1 {
				return nil, fmt.Errorf("跳板 %q：%w", h.Name, err)
			}
			return nil, err
		}
		if i < len(hops)-1 {
			up.jumps = append(up.jumps, c)
		} else {
			up.client = c
		}
		prev = c
	}
	return up, nil
}

// dialHop 拨一跳：第一跳用 cfg.Dial，之后经上一跳的 direct-tcpip。
func (s *Service) dialHop(ctx context.Context, h db.SshHost, via *ssh.Client) (*ssh.Client, error) {
	addr := net.JoinHostPort(h.Address, strconv.Itoa(int(h.Port)))
	cfg, err := s.clientConfig(ctx, h)
	if err != nil {
		return nil, err
	}
	var pinned ssh.PublicKey
	if h.HostKey != nil {
		if pinned, _, _, _, err = ssh.ParseAuthorizedKey([]byte(*h.HostKey)); err != nil {
			return nil, fmt.Errorf("固定的主机公钥损坏：%w", err)
		}
		if pinned.Type() == ssh.KeyAlgoRSA {
			cfg.HostKeyAlgorithms = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
		} else {
			cfg.HostKeyAlgorithms = []string{pinned.Type()}
		}
	}
	var seen ssh.PublicKey
	cfg.HostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
		seen = key
		if pinned != nil && !bytes.Equal(key.Marshal(), pinned.Marshal()) {
			return &HostKeyMismatchError{Host: h.Name, Expected: ssh.FingerprintSHA256(pinned), Got: ssh.FingerprintSHA256(key)}
		}
		return nil
	}

	dctx, dcancel := context.WithTimeout(ctx, connectTimeout)
	var conn net.Conn
	if via == nil {
		conn, err = s.cfg.Dial(dctx, "tcp", addr)
	} else {
		conn, err = via.DialContext(dctx, "tcp", addr)
	}
	dcancel()
	if err != nil {
		return nil, fmt.Errorf("连不上 %s：%w", addr, err)
	}
	client, err := handshake(ctx, conn, addr, cfg)
	if err != nil {
		var mm *HostKeyMismatchError
		switch {
		case errors.As(err, &mm):
			return nil, mm
		case isAuthErr(err):
			return nil, fmt.Errorf("登录 %s@%s 失败：%w", h.Username, addr, err)
		default:
			return nil, fmt.Errorf("连不上 %s：%w", addr, err)
		}
	}
	if pinned == nil && seen != nil {
		if err := s.pin(ctx, h, seen); err != nil {
			_ = client.Close()
			return nil, err
		}
	}
	go keepalive(client)
	return client, nil
}

// pin 首次成功连接后固定主机公钥。按拨号所用快照的 rev 做 CAS：拨号期间主机被改或指纹被重置，
// 旧连接不得把它看到的公钥写回；并发下别人以同一 rev 先固定了，就按别人的比较。
func (s *Service) pin(ctx context.Context, h db.SshHost, key ssh.PublicKey) error {
	line := trimNL(string(ssh.MarshalAuthorizedKey(key)))
	n, err := s.st.Q.PinSshHostKey(ctx, db.PinSshHostKeyParams{HostKey: &line, At: new(s.now()), ID: h.ID, Rev: h.Rev})
	if err != nil {
		return fmt.Errorf("固定主机指纹：%w", err)
	}
	if n == 1 {
		s.cfg.Changed()
		return nil
	}
	cur, err := s.st.Q.GetSshHost(ctx, h.ID)
	if err != nil {
		return fmt.Errorf("固定主机指纹：%w", err)
	}
	if cur.Rev != h.Rev || cur.HostKey == nil {
		return errStale
	}
	pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(*cur.HostKey))
	if err != nil {
		return fmt.Errorf("固定的主机公钥损坏：%w", err)
	}
	if !bytes.Equal(pinned.Marshal(), key.Marshal()) {
		return &HostKeyMismatchError{Host: h.Name, Expected: ssh.FingerprintSHA256(pinned), Got: ssh.FingerprintSHA256(key)}
	}
	return nil
}

func (s *Service) clientConfig(ctx context.Context, h db.SshHost) (*ssh.ClientConfig, error) {
	cfg := &ssh.ClientConfig{User: h.Username, ClientVersion: clientVersion, Timeout: connectTimeout}
	switch h.AuthType {
	case "key":
		if h.KeyID == nil {
			return nil, fmt.Errorf("主机 %q 没有选钥匙", h.Name)
		}
		signer, err := s.signerFor(ctx, *h.KeyID)
		if err != nil {
			return nil, fmt.Errorf("读取主机 %q 的钥匙：%w", h.Name, err)
		}
		cfg.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	case "password":
		plain, err := s.cfg.Box.Open(passwordAAD(h.ID), h.PasswordEnc)
		if err != nil {
			return nil, fmt.Errorf("读取主机 %q 的密码：%w", h.Name, err)
		}
		cfg.Auth, cfg.AuthCallback = passwordAuthentication(string(plain))
		clear(plain)
	default:
		return nil, fmt.Errorf("不支持的认证方式 %q", h.AuthType)
	}
	return cfg, nil
}

// handshake 做 SSH 握手。ClientConfig.Timeout 只管建连，握手另设 connectTimeout，超时或取消时关掉底层连接。
func handshake(ctx context.Context, conn net.Conn, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	hctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	stop := context.AfterFunc(hctx, func() { _ = conn.Close() })
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	stopped := stop()
	if err != nil {
		_ = conn.Close()
		if hctx.Err() != nil {
			return nil, errors.New("握手超时")
		}
		return nil, err
	}
	if !stopped || hctx.Err() != nil {
		_ = cc.Close()
		return nil, errors.New("握手超时")
	}
	return ssh.NewClient(cc, chans, reqs), nil
}

func isAuthErr(err error) bool {
	m := err.Error()
	return strings.Contains(m, "unable to authenticate") || strings.Contains(m, "多因素") || strings.Contains(m, "keyboard-interactive")
}

// passwordAuthentication 同时注册 password 与只回答单个不回显提示的 keyboard-interactive；
// 服务端已接受一个因素（要求多因素）时中止，不把保存的密码用于 OTP。
func passwordAuthentication(password string) ([]ssh.AuthMethod, ssh.ClientAuthCallback) {
	auths := []ssh.AuthMethod{ssh.Password(password), ssh.KeyboardInteractive(passwordChallenge(password))}
	callback := func(ctx *ssh.ClientAuthContext) (ssh.AuthMethod, error) {
		if len(ctx.PartialSuccessMethods) > 0 {
			return nil, fmt.Errorf("不支持 SSH 多因素认证（已完成 %s）", strings.Join(ctx.PartialSuccessMethods, ", "))
		}
		return nil, nil
	}
	return auths, callback
}

func passwordChallenge(password string) ssh.KeyboardInteractiveChallenge {
	var answered bool
	return func(_, _ string, questions []string, echos []bool) ([]string, error) {
		if len(questions) == 0 {
			return nil, nil // 部分服务端先发一轮空提示
		}
		if answered {
			return nil, errors.New("不支持多轮 keyboard-interactive 认证")
		}
		if len(questions) != 1 || len(echos) != 1 {
			return nil, errors.New("keyboard-interactive 提示数量异常")
		}
		if echos[0] {
			return nil, errors.New("拒绝向会回显的 keyboard-interactive 字段发送密码")
		}
		answered = true
		return []string{password}, nil
	}
}

// keepalive 每 keepaliveEvery 发一次保活请求，出错就关掉连接；连接关闭后退出。
func keepalive(c *ssh.Client) {
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	t := time.NewTicker(keepaliveEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				_ = c.Close()
				return
			}
		}
	}
}
