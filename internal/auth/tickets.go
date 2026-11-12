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
type Tickets struct {
	mu sync.Mutex
	m  map[string]time.Time
}

// NewTickets 返回空的票据表。
func NewTickets() *Tickets { return &Tickets{m: map[string]time.Time{}} }

// Issue 签发票据，返回票据与有效秒数。
func (t *Tickets) Issue() (string, int) {
	tk := ids.Token("owt")
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, exp := range t.m {
		if now.After(exp) {
			delete(t.m, k)
		}
	}
	t.m[tk] = now.Add(TicketTTL)
	return tk, int(TicketTTL / time.Second)
}

// Redeem 兑换票据；未知、已用或过期返回 false。
func (t *Tickets) Redeem(ticket string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	exp, ok := t.m[ticket]
	delete(t.m, ticket)
	return ok && time.Now().Before(exp)
}
