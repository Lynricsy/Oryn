package coord

import (
	"sync"
	"time"
)

// rule 是一条滑动窗口限额：window 内至多 n 次。
type rule struct {
	n      int
	window time.Duration
}

// 协调者的限额（每个对话）：失控保护，不是权限。
var (
	createThreadRules = []rule{{10, 10 * time.Minute}, {40, 24 * time.Hour}}
	sendMessageRules  = []rule{{60, 10 * time.Minute}}
	reportRules       = []rule{{60, time.Hour}}
)

// limiter 是内存中的滑动窗口计数（控制面重启后清零）。
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

// allow 在 key 的全部规则都未超限时记一次并返回 true。
func (l *limiter) allow(key string, now time.Time, rules []rule) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	var longest time.Duration
	for _, r := range rules {
		longest = max(longest, r.window)
	}
	hs := l.hits[key]
	keep := hs[:0]
	for _, h := range hs {
		if now.Sub(h) < longest {
			keep = append(keep, h)
		}
	}
	hs = keep
	for _, r := range rules {
		n := 0
		for _, h := range hs {
			if now.Sub(h) < r.window {
				n++
			}
		}
		if n >= r.n {
			l.hits[key] = hs
			return false
		}
	}
	l.hits[key] = append(hs, now)
	return true
}
