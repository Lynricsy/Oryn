package orbs

import (
	"context"
	"fmt"
	"net"
	"slices"
	"time"

	"github.com/Lynricsy/Oryn/internal/link"
)

const (
	// FeatureBrowserRelay 是 M27 orbd 的 hello 特性：支持接收 browser_relay 流并按线程提供 omp browser-relay 前门。
	FeatureBrowserRelay = "browser-relay"

	browserRelayDialTimeout = 10 * time.Second
)

// DialBrowserRelay 在 orb 当前活动 link 上打开一条 browser_relay 流（M27）。
// 无已 hello 的 link → ErrOrbNotReady；hello 未声明 browser-relay → ErrOrbdOutdated。
func (s *Service) DialBrowserRelay(ctx context.Context, orbID, threadID string) (net.Conn, error) {
	l := s.currentLink(orbID)
	if l == nil || !l.helloSeen() {
		return nil, fmt.Errorf("%w: orb %s has no link", ErrOrbNotReady, orbID)
	}
	if !slices.Contains(l.hello.Features, FeatureBrowserRelay) {
		return nil, ErrOrbdOutdated
	}
	deadline := time.Now().Add(browserRelayDialTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	octx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	st, err := l.sess.Open(octx, link.StreamHeader{T: link.StreamBrowserRelay, ThreadID: threadID})
	if err != nil {
		return nil, fmt.Errorf("orbs: open browser_relay stream: %w", err)
	}
	return st, nil
}
