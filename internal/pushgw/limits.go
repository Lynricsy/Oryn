package pushgw

import (
	"sync"
	"time"
)

// limit 是一类限额的令牌桶参数：容量 burst，每 every 补充一个。
type limit struct {
	name  string
	burst float64
	every time.Duration
}

// perPeriod 把「每 period 至多 n 次」换成令牌桶：容量 n，匀速补满。
func perPeriod(name string, n int, period time.Duration) limit {
	return limit{name: name, burst: float64(n), every: period / time.Duration(n)}
}

// limits 是网关的四类限额（docs/protocol.md「网关存储、限额与隐私」）。
type limits struct {
	grantMinute, grantDay, host, challengeIP limit
}

func newLimits(l Limits) limits {
	return limits{
		grantMinute: perPeriod("grant_minute", l.GrantPerMinute, time.Minute),
		grantDay:    perPeriod("grant_day", l.GrantPerDay, 24*time.Hour),
		host:        perPeriod("host", l.HostPerMinute, time.Minute),
		challengeIP: perPeriod("challenge_ip", l.ChallengesPerIPHour, time.Hour),
	}
}

// maxLimiterKeys 是限流表的规模上限；超出时清掉已回满的桶，仍超出则整表重置。
const maxLimiterKeys = 100000

type bucket struct {
	tokens float64
	at     time.Time
	// full 是从空到满所需的时间：闲置这么久的桶与新桶等价，可以清掉。
	full time.Duration
}

// limiter 是内存中的按键令牌桶（照 internal/auth/ratelimit.go；同一请求的多个桶一起扣，避免只扣一部分）。
type limiter struct {
	mu sync.Mutex
	m  map[string]*bucket
}

func newLimiter() *limiter { return &limiter{m: map[string]*bucket{}} }

// check 是一个待扣的桶。
type check struct {
	lim limit
	key string
}

// allow 在所有桶都至少有一个令牌时各扣一个并返回 true；否则不扣，返回 false 与最长的建议等待时间。
func (l *limiter) allow(now time.Time, checks ...check) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	bs := make([]*bucket, len(checks))
	var wait time.Duration
	for i, c := range checks {
		k := c.lim.name + "\x00" + c.key
		b, ok := l.m[k]
		if !ok {
			if len(l.m) >= maxLimiterKeys {
				l.prune(now)
			}
			b = &bucket{tokens: c.lim.burst, at: now, full: time.Duration(c.lim.burst) * c.lim.every}
			l.m[k] = b
		} else if now.After(b.at) {
			b.tokens = min(c.lim.burst, b.tokens+float64(now.Sub(b.at))/float64(c.lim.every))
			b.at = now
		}
		if b.tokens < 1 {
			wait = max(wait, time.Duration((1-b.tokens)*float64(c.lim.every)))
		}
		bs[i] = b
	}
	if wait > 0 {
		return false, wait
	}
	for _, b := range bs {
		b.tokens--
	}
	return true, 0
}

// prune 删除已回满的桶；仍超限时整表重置。
func (l *limiter) prune(now time.Time) {
	for k, b := range l.m {
		if now.Sub(b.at) >= b.full {
			delete(l.m, k)
		}
	}
	if len(l.m) >= maxLimiterKeys {
		l.m = map[string]*bucket{}
	}
}
