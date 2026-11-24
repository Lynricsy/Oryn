package pushgw

import (
	"crypto/subtle"
	"errors"
	"sync"
	"time"
)

// challengeTTL 是 challenge 的有效期。
const challengeTTL = 5 * time.Minute

// maxChallenges 是内存里未用 challenge 的上限（每 IP 每小时 30 次的限额之外的兜底）。
const maxChallenges = 100000

// errChallengesFull 表示未用 challenge 太多（回 503）。
var errChallengesFull = errors.New("too many pending challenges")

// challenge 是一次「证明能收到这个令牌的推送」：网关把 nonce 推给令牌，设备拿着它来申请授权。
type challenge struct {
	id, nonce string
	// deviceKey 是发起者：只有它能用这个 challenge。
	deviceKey string
	// channel / appID / env / token 是 challenge 请求里待证明的推送目标。
	channel, appID, env, token string
	expires                    time.Time
}

// challenges 是内存里的 challenge 表：5 分钟有效、一次性。
type challenges struct {
	mu sync.Mutex
	m  map[string]*challenge
}

func newChallenges() *challenges { return &challenges{m: map[string]*challenge{}} }

// add 生成 id 与 nonce 并登记 ch。
func (c *challenges) add(ch challenge, now time.Time) (*challenge, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= maxChallenges {
		c.pruneLocked(now)
		if len(c.m) >= maxChallenges {
			return nil, errChallengesFull
		}
	}
	ch.id, ch.nonce, ch.expires = newID(), newID(), now.Add(challengeTTL)
	c.m[ch.id] = &ch
	return &ch, nil
}

// take 取走属于 deviceKey 且 nonce 正确的有效 challenge。发起者来取时无论 nonce 对错都作废它（一次性）；
// 别的设备拿着 id 来取不影响它。
func (c *challenges) take(id, nonce, deviceKey string, now time.Time) (*challenge, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.m[id]
	if !ok {
		return nil, false
	}
	if !now.Before(ch.expires) {
		delete(c.m, id)
		return nil, false
	}
	if ch.deviceKey != deviceKey {
		return nil, false
	}
	delete(c.m, id)
	if subtle.ConstantTimeCompare([]byte(ch.nonce), []byte(nonce)) != 1 {
		return nil, false
	}
	return ch, true
}

// remove 删除 challenge（推送没发出去时）。
func (c *challenges) remove(id string) {
	c.mu.Lock()
	delete(c.m, id)
	c.mu.Unlock()
}

// prune 删除过期的 challenge。
func (c *challenges) prune(now time.Time) {
	c.mu.Lock()
	c.pruneLocked(now)
	c.mu.Unlock()
}

func (c *challenges) pruneLocked(now time.Time) {
	for id, ch := range c.m {
		if !now.Before(ch.expires) {
			delete(c.m, id)
		}
	}
}
