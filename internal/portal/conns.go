package portal

import (
	"context"
	"errors"
	"sync"
)

var (
	errBusy  = errors.New("portal: too many connections")
	errStale = errors.New("portal: identity revoked during admission")
)

// registry 登记进入代理的请求（计划 4.8）：所有者按主体键（web/<会话哈希>、device/<id>），
// 匿名访客按 share/<orb>/<端口>。吊销与分享终止经 cancelKey 取消对应键的全部连接（含已升级的 WebSocket）。
//
// epoch 在每次 cancelKey 时递增：请求在判定身份之前取一次 epoch，登记时若已变化则说明判定与登记之间
// 发生过吊销，须重新判定，避免「读到有效凭据 → 凭据被吊销 → 才登记」的连接逃过取消。
type registry struct {
	mu      sync.Mutex
	epoch   uint64
	byKey   map[string]map[*liveConn]struct{}
	perOrb  map[string]int
	perAnon map[string]int
	maxOrb  int
	maxAnon int
}

type liveConn struct{ cancel context.CancelFunc }

func newRegistry(maxOrb, maxAnon int) *registry {
	return &registry{
		byKey: map[string]map[*liveConn]struct{}{}, perOrb: map[string]int{}, perAnon: map[string]int{},
		maxOrb: maxOrb, maxAnon: maxAnon,
	}
}

func (g *registry) current() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.epoch
}

// enter 取并发名额（orb 级；匿名另取端口级）并登记连接；返回注销函数。
func (g *registry) enter(epoch uint64, orbID, key string, anon bool, cancel context.CancelFunc) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.epoch != epoch {
		return nil, errStale
	}
	if g.perOrb[orbID] >= g.maxOrb || (anon && g.perAnon[key] >= g.maxAnon) {
		return nil, errBusy
	}
	g.perOrb[orbID]++
	if anon {
		g.perAnon[key]++
	}
	c := &liveConn{cancel: cancel}
	set := g.byKey[key]
	if set == nil {
		set = map[*liveConn]struct{}{}
		g.byKey[key] = set
	}
	set[c] = struct{}{}
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.perOrb[orbID]--; g.perOrb[orbID] <= 0 {
			delete(g.perOrb, orbID)
		}
		if anon {
			if g.perAnon[key]--; g.perAnon[key] <= 0 {
				delete(g.perAnon, key)
			}
		}
		if set := g.byKey[key]; set != nil {
			delete(set, c)
			if len(set) == 0 {
				delete(g.byKey, key)
			}
		}
	}, nil
}

// cancelKey 取消某键下的全部连接。
func (g *registry) cancelKey(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.epoch++
	for c := range g.byKey[key] {
		c.cancel()
	}
}
