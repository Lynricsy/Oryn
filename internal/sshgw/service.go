// Package sshgw 是 M18 的 SSH 主机目录与控制面 SSH 网关：主人在控制面登记主机与钥匙，
// orb 内的 ssh 经 orb link 连到这里，由网关校验固定的主机指纹、走跳板链并代为登录目标机；私钥与密码只在控制面。
package sshgw

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	gatewayKeyFile = "ssh-gateway.key"
	auditRetention = 30 * 24 * time.Hour
)

// Config 是网关服务的依赖与回调。
type Config struct {
	Box     *secrets.Box
	DataDir string // 网关主机密钥放 <DataDir>/ssh-gateway.key
	// Apply 在 orb 可见的内容变了（主机增删改）之后调用；projectID 空 = 全局层。
	Apply func(projectID string)
	// Changed 在任何主机 / 钥匙 / 指纹 / 测试结果变化后调用（只用来刷新页面）。
	Changed func()
	// Dial 拨第一跳的 TCP；nil = (&net.Dialer{}).DialContext。测试注入。
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	Now  func() time.Time // nil = time.Now
}

// Orbs 是网关需要的 orb 查询。
type Orbs interface {
	Get(ctx context.Context, id string) (db.Orb, error)
}

// Error 是给调用方（REST / CLI）原样展示的业务错误。
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

func invalid(format string, a ...any) *Error {
	return &Error{Code: "invalid_request", Message: fmt.Sprintf(format, a...)}
}
func notFound(format string, a ...any) *Error {
	return &Error{Code: "not_found", Message: fmt.Sprintf(format, a...)}
}
func conflict(format string, a ...any) *Error {
	return &Error{Code: "conflict", Message: fmt.Sprintf(format, a...)}
}

// live 是一条依赖某些主机的活动连接（网关会话、网页终端、SFTP 连接）；链上任一主机失效时被关闭。
type live struct {
	hostIDs []string
	close   func()
}

// Service 是 SSH 主机目录与网关。
type Service struct {
	cfg    Config
	st     *store.Store
	orbs   Orbs
	signer ssh.Signer

	mu   sync.Mutex
	live map[*live]struct{}
}

// New 读取或生成网关主机密钥并构造服务。
func New(cfg Config, st *store.Store, orbs Orbs) (*Service, error) {
	if cfg.Box == nil {
		return nil, errors.New("sshgw: 缺少 secrets.Box")
	}
	if cfg.Dial == nil {
		cfg.Dial = (&net.Dialer{}).DialContext
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Apply == nil {
		cfg.Apply = func(string) {}
	}
	if cfg.Changed == nil {
		cfg.Changed = func() {}
	}
	signer, err := loadOrCreateHostKey(filepath.Join(cfg.DataDir, gatewayKeyFile))
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, st: st, orbs: orbs, signer: signer, live: map[*live]struct{}{}}, nil
}

// loadOrCreateHostKey 读取网关主机密钥；不存在时生成 ed25519 并 0600 原子写入。解析失败直接报错，
// 不自动重建，免得 orb 里的 known_hosts 全部失配。
func loadOrCreateHostKey(p string) (ssh.Signer, error) {
	b, err := os.ReadFile(p)
	if err == nil {
		s, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return nil, fmt.Errorf("解析 SSH 网关主机密钥 %s: %w", p, err)
		}
		return s, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("读取 SSH 网关主机密钥: %w", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "oryn-ssh-gateway")
	if err != nil {
		return nil, err
	}
	data := pem.EncodeToMemory(block)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+gatewayKeyFile+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("写入 SSH 网关主机密钥: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return nil, fmt.Errorf("写入 SSH 网关主机密钥: %w", err)
	}
	return ssh.NewSignerFromKey(priv)
}

// Run 做周期维护：每小时清理 30 天前的审计；ctx 结束时关掉全部活动连接。
func (s *Service) Run(ctx context.Context) {
	s.pruneAudit(ctx)
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	for {
		select {
		case <-ctx.Done():
			s.closeAll()
			return
		case <-hourly.C:
			s.pruneAudit(ctx)
		}
	}
}

func (s *Service) pruneAudit(ctx context.Context) {
	before := s.cfg.Now().Add(-auditRetention).UnixMilli()
	if err := s.st.Q.DeleteSshAuditBefore(ctx, before); err != nil && ctx.Err() == nil {
		slog.Warn("清理 SSH 审计失败", "err", err)
	}
}

func (s *Service) now() int64 { return s.cfg.Now().UnixMilli() }

// track 登记一条活动连接，返回注销函数。
func (s *Service) track(hostIDs []string, closeFn func()) (untrack func()) {
	l := &live{hostIDs: hostIDs, close: closeFn}
	s.mu.Lock()
	s.live[l] = struct{}{}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.live, l)
		s.mu.Unlock()
	}
}

// invalidate 关闭链上含 hostID 的全部活动连接（主机被改、删、停用、重置指纹，或它是别人的跳板时）。
func (s *Service) invalidate(hostID string) {
	s.mu.Lock()
	var victims []*live
	for l := range s.live {
		if slices.Contains(l.hostIDs, hostID) {
			victims = append(victims, l)
			delete(s.live, l)
		}
	}
	s.mu.Unlock()
	for _, l := range victims {
		l.close()
	}
}

func (s *Service) closeAll() {
	s.mu.Lock()
	victims := make([]*live, 0, len(s.live))
	for l := range s.live {
		victims = append(victims, l)
	}
	clear(s.live)
	s.mu.Unlock()
	for _, l := range victims {
		l.close()
	}
}

// gatewayPublicKey 返回网关主机公钥（authorized_keys 一行，无注释、无换行）。
func (s *Service) gatewayPublicKey() string {
	return trimNL(string(ssh.MarshalAuthorizedKey(s.signer.PublicKey())))
}

func trimNL(v string) string {
	for len(v) > 0 && (v[len(v)-1] == '\n' || v[len(v)-1] == '\r') {
		v = v[:len(v)-1]
	}
	return v
}
