package sshgw

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// runner 线程的 SSH 公钥登记（M21）：一台 runner 同时跑多个项目的线程，网关收到的 `ssh` 流只知道来自哪台 runner。
// orbd 为每个线程的监督器生成一把 ed25519 密钥（私钥只在 runner 本机），公钥随 agent.start 的结果回到 orynd，由 threads
// 在这里登记；网关对 runner 的流只接受公钥认证，按公钥认出线程。登记只在内存：orynd 重启后 runner 重连、线程重新
// agent.start（幂等，回同一把公钥）时补上。

// threadKey 是一把已登记公钥所属的线程与 runner orb。
type threadKey struct {
	threadID, orbID string
}

// threadKeys 是「公钥 → 线程」表与经各公钥认证的网关连接（并发安全）；每个线程至多一把，新登记替换旧的。
// 锁序：只有 mu 一把；关闭连接（gwConn.close，不取 mu）一律在放开 mu 之后进行，连接退出时的注销（untrack）再取 mu。
type threadKeys struct {
	mu       sync.Mutex
	byKey    map[string]threadKey // ssh.PublicKey.Marshal() → 线程
	byThread map[string]string    // 线程 → 其公钥的 Marshal()
	// conns 是经该公钥认证、仍在服务的 runner 网关连接：公钥被注销或被同线程的新公钥替换时一并关闭（旧一代 agent
	// 残留的 ssh master 随之退出）。
	conns map[string]map[*keyConn]struct{}
}

// keyConn 是一条经线程公钥认证的网关连接。
type keyConn struct{ close func() }

func newThreadKeys() *threadKeys {
	return &threadKeys{byKey: map[string]threadKey{}, byThread: map[string]string{}, conns: map[string]map[*keyConn]struct{}{}}
}

// removeLocked 删掉公钥 k 的登记，返回经它认证的连接的关闭函数（调用方放开 mu 之后调用）。调用方持有 mu。
func (ks *threadKeys) removeLocked(k string) []func() {
	delete(ks.byKey, k)
	var closers []func()
	for c := range ks.conns[k] {
		closers = append(closers, c.close)
	}
	delete(ks.conns, k)
	return closers
}

// closeAll 依次关闭连接（不持 mu）。
func closeAll(closers []func()) {
	for _, c := range closers {
		c()
	}
}

// RegisterThreadKey 登记 runner 线程的公钥（authorized_keys 单行）；同一线程的旧公钥随之失效，经旧公钥认证的连接关闭。
// 同值重复登记（同一监督器的幂等 agent.start、崩溃自重启）不动已有连接。
func (s *Service) RegisterThreadKey(threadID, orbID, authorizedKey string) error {
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		return fmt.Errorf("sshgw: 线程公钥无效: %w", err)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return errors.New("sshgw: 线程公钥须为单行")
	}
	k := string(pub.Marshal())
	ks := s.keys
	var closers []func()
	ks.mu.Lock()
	if old, ok := ks.byThread[threadID]; ok && old != k {
		closers = append(closers, ks.removeLocked(old)...)
	}
	if prev, ok := ks.byKey[k]; ok && prev.threadID != threadID {
		// 同一把公钥不会属于两个线程：后登记的为准，经它认证的旧线程的连接关闭
		delete(ks.byThread, prev.threadID)
		closers = append(closers, ks.removeLocked(k)...)
	}
	ks.byKey[k] = threadKey{threadID: threadID, orbID: orbID}
	ks.byThread[threadID] = k
	ks.mu.Unlock()
	closeAll(closers)
	return nil
}

// DropThreadKey 注销线程的公钥（监督器结束、线程删除），经它认证的连接关闭。
func (s *Service) DropThreadKey(threadID string) {
	ks := s.keys
	var closers []func()
	ks.mu.Lock()
	if k, ok := ks.byThread[threadID]; ok {
		closers = ks.removeLocked(k)
		delete(ks.byThread, threadID)
	}
	ks.mu.Unlock()
	closeAll(closers)
}

// DropOrbKeys 注销一台 runner 上全部线程的公钥（runner 离线定时器到期、orb 停止或删除），经它们认证的连接关闭。
func (s *Service) DropOrbKeys(orbID string) {
	ks := s.keys
	var closers []func()
	ks.mu.Lock()
	for k, tk := range ks.byKey {
		if tk.orbID == orbID {
			closers = append(closers, ks.removeLocked(k)...)
			delete(ks.byThread, tk.threadID)
		}
	}
	ks.mu.Unlock()
	closeAll(closers)
}

// trackKeyConn 把一条经公钥 pub 认证、属于 threadID 的连接挂到该公钥下；公钥此刻已不再登记给这个线程时返回 false
// （认证与挂上之间被注销或替换），调用方拒绝这条连接。返回的 untrack 在连接结束时调用。
func (s *Service) trackKeyConn(pub ssh.PublicKey, threadID string, close func()) (untrack func(), ok bool) {
	k := string(pub.Marshal())
	ks := s.keys
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if tk, ok := ks.byKey[k]; !ok || tk.threadID != threadID {
		return nil, false
	}
	c := &keyConn{close: close}
	if ks.conns[k] == nil {
		ks.conns[k] = map[*keyConn]struct{}{}
	}
	ks.conns[k][c] = struct{}{}
	return func() {
		ks.mu.Lock()
		defer ks.mu.Unlock()
		if m := ks.conns[k]; m != nil {
			delete(m, c)
			if len(m) == 0 {
				delete(ks.conns, k)
			}
		}
	}, true
}

// lookupThreadKey 返回公钥登记到的线程。
func (s *Service) lookupThreadKey(pub ssh.PublicKey) (threadKey, bool) {
	ks := s.keys
	ks.mu.Lock()
	defer ks.mu.Unlock()
	tk, ok := ks.byKey[string(pub.Marshal())]
	return tk, ok
}

// errUnknownKey 是公钥认证失败时给 runner 看的原因（不区分未登记、属于别的 runner、线程已归档）。
var errUnknownKey = errors.New("公钥未登记到这台 runner 上运行中的线程")

// runnerThread 按公钥认出 orb（runner）上的线程：公钥已登记、登记的 runner 与线程行的 orb_id 都等于发起这条流的
// orb，且线程是未归档的工作线程。
func (s *Service) runnerThread(ctx context.Context, orb db.Orb, pub ssh.PublicKey) (db.Thread, error) {
	tk, ok := s.lookupThreadKey(pub)
	if !ok || tk.orbID != orb.ID {
		return db.Thread{}, errUnknownKey
	}
	t, err := s.st.Q.GetThread(ctx, tk.threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Thread{}, errUnknownKey
	}
	if err != nil {
		return db.Thread{}, err
	}
	if t.Kind != threadKindWork || t.ArchivedAt != nil || t.OrbID == nil || *t.OrbID != orb.ID {
		return db.Thread{}, errUnknownKey
	}
	return t, nil
}
