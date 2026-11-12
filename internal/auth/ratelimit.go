package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// ClientIP 返回请求方 IP（RemoteAddr 的主机部分）。
// 是否采信 X-Forwarded-For / X-Real-IP 由路由层的中间件决定（只信任回环地址上的反代），
// 这里只读中间件处理后的 RemoteAddr。
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Limit 是一类免鉴权端点的限流桶参数。
type Limit struct {
	Name  string
	Burst float64
	Every time.Duration // 每补充一个令牌的间隔
}

// 免鉴权端点的限流参数（按客户端 IP 计）。
var (
	LimitLogin     = Limit{Name: "login", Burst: 10, Every: 6 * time.Second}
	LimitSetup     = Limit{Name: "setup", Burst: 10, Every: 6 * time.Second}
	LimitRecovery  = Limit{Name: "recovery", Burst: 5, Every: 30 * time.Second}
	LimitPairStart = Limit{Name: "pair_start", Burst: 5, Every: 20 * time.Second}
	// 轮询间隔 2s，正常客户端每秒至多 0.5 次
	LimitPairPoll = Limit{Name: "pair_poll", Burst: 10, Every: time.Second}
)

// maxLimiterKeys 是限流表的规模上限；超出时清掉已回满的桶，仍超出则整表重置。
const maxLimiterKeys = 50000

type bucket struct {
	tokens float64
	at     time.Time
}

// limiter 是内存中的按键令牌桶。
type limiter struct {
	mu sync.Mutex
	m  map[string]*bucket
}

func newLimiter() *limiter { return &limiter{m: map[string]*bucket{}} }

// allow 消耗一个令牌；不足时返回 false 与建议的重试等待时间。
func (l *limiter) allow(lim Limit, key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := lim.Name + "\x00" + key
	b, ok := l.m[k]
	if !ok {
		if len(l.m) >= maxLimiterKeys {
			l.prune(now)
		}
		b = &bucket{tokens: lim.Burst, at: now}
		l.m[k] = b
	} else {
		b.tokens = min(lim.Burst, b.tokens+float64(now.Sub(b.at))/float64(lim.Every))
		b.at = now
	}
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(lim.Every))
	}
	b.tokens--
	return true, 0
}

// prune 删除已回满的桶（以最长补充周期估算）；仍超限时整表重置。
func (l *limiter) prune(now time.Time) {
	for k, b := range l.m {
		if now.Sub(b.at) > 10*time.Minute {
			delete(l.m, k)
		}
	}
	if len(l.m) >= maxLimiterKeys {
		l.m = map[string]*bucket{}
	}
}

// RateLimit 按客户端 IP 限流；超限返回 429 错误（Message 含重试秒数）。
func (s *Service) RateLimit(r *http.Request, lim Limit) error {
	ok, wait := s.limiter.allow(lim, ClientIP(r), time.Now())
	if ok {
		return nil
	}
	secs := int(wait/time.Second) + 1
	return &Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "请求过于频繁，请稍后再试", RetryAfter: secs}
}
