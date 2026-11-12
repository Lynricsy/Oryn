package auth

import (
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/ids"
)

// TicketTTL 是 WebSocket 票据有效期。
const TicketTTL = 30 * time.Second

// Tickets 签发一次性的 WebSocket 票据（只在内存中，30 秒有效）。
// 浏览器的 WebSocket 无法自定义头，故先以常规鉴权调 `POST ws-ticket`，再以 `?ticket=` 建连。
// 票据记录签发它的主体，WebSocket 连接据此在吊销（Service.Subscribe）时被关闭。
type Tickets struct {
	mu sync.Mutex
	m  map[string]ticket
}

type ticket struct {
	p   Principal
	exp time.Time
}

// maxTickets 是未兑换票据的上限（只有已鉴权请求能签发，正常远小于此）。
const maxTickets = 10000

// NewTickets 返回空的票据表。
func NewTickets() *Tickets { return &Tickets{m: map[string]ticket{}} }

// Issue 为主体 p 签发票据，返回票据与有效秒数。
func (t *Tickets) Issue(p Principal) (string, int) {
	tk := ids.Token("owt")
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.m {
		if now.After(v.exp) {
			delete(t.m, k)
		}
	}
	if len(t.m) >= maxTickets {
		t.m = map[string]ticket{}
	}
	t.m[tk] = ticket{p: p, exp: now.Add(TicketTTL)}
	return tk, int(TicketTTL / time.Second)
}

// Redeem 兑换票据，返回签发时的主体；未知、已用或过期返回 ok=false。
func (t *Tickets) Redeem(tk string) (Principal, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.m[tk]
	delete(t.m, tk)
	if !ok || !time.Now().Before(v.exp) {
		return Principal{}, false
	}
	return v.p, true
}

// Revoke 作废主体 (kind, id) 名下全部未兑换的票据（订阅 Service.Subscribe 的吊销事件；H3：
// 否则持有被盗令牌的一方可预先囤积票据，在设备被吊销后仍能建立新连接）。
func (t *Tickets) Revoke(kind, id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.m {
		if v.p.Kind == kind && v.p.ID == id {
			delete(t.m, k)
		}
	}
}
