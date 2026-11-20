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

// threadKeys 是「公钥 → 线程」表（并发安全）；每个线程至多一把，新登记替换旧的。
type threadKeys struct {
	mu       sync.Mutex
	byKey    map[string]threadKey // ssh.PublicKey.Marshal() → 线程
	byThread map[string]string    // 线程 → 其公钥的 Marshal()
}

func newThreadKeys() *threadKeys {
	return &threadKeys{byKey: map[string]threadKey{}, byThread: map[string]string{}}
}

// RegisterThreadKey 登记 runner 线程的公钥（authorized_keys 单行）；同一线程的旧公钥随之失效。
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
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if old, ok := ks.byThread[threadID]; ok && old != k {
		delete(ks.byKey, old)
	}
	if prev, ok := ks.byKey[k]; ok && prev.threadID != threadID {
		delete(ks.byThread, prev.threadID) // 同一把公钥不会属于两个线程：后登记的为准
	}
	ks.byKey[k] = threadKey{threadID: threadID, orbID: orbID}
	ks.byThread[threadID] = k
	return nil
}

// DropThreadKey 注销线程的公钥（监督器结束、线程停止 / 归档 / 删除）。
func (s *Service) DropThreadKey(threadID string) {
	ks := s.keys
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if k, ok := ks.byThread[threadID]; ok {
		delete(ks.byKey, k)
		delete(ks.byThread, threadID)
	}
}

// DropOrbKeys 注销一台 runner 上全部线程的公钥（runner 离线定时器到期、orb 停止或删除）。
func (s *Service) DropOrbKeys(orbID string) {
	ks := s.keys
	ks.mu.Lock()
	defer ks.mu.Unlock()
	for k, tk := range ks.byKey {
		if tk.orbID == orbID {
			delete(ks.byKey, k)
			delete(ks.byThread, tk.threadID)
		}
	}
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
