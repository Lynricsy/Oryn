package orbs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orb HTTP 通道（M3）：控制面在 orb link 上主动开 `fs` 流，流上跑 HTTP/1.1（一条流一个请求），
// orbd 以 http.Server 服务 /fs/*、/git/*、/attachments/*。与 egress 对称、方向相反。

var (
	// ErrOrbNotReady 表示 orb 当前不可用（未运行、link 未就绪、唤醒超时或已 failed）。
	ErrOrbNotReady = errors.New("orbs: orb not ready")
	// ErrOrbGone 表示 orb 已删除或正在删除。
	ErrOrbGone = errors.New("orbs: orb deleted")
	// ErrOrbdOutdated 表示 orbd 的 hello 未声明所需特性（M3 之前创建的 orb）。
	ErrOrbdOutdated = errors.New("orbs: orbd does not support this feature")
)

// 本通道上的 orbd 特性名（hello.features）。
const (
	FeatureFS          = "fs"
	FeatureGit         = "git"
	FeatureAttachments = "attachments"
)

// usableState 报告 orb 处于 state 时 orbd 是否在跑、可服务 fs 流。
func usableState(state string) bool {
	return state == StateReady || state == StateSettingUp || state == StateResuming
}

// Transport 返回经 orb link 开 fs 流的 RoundTripper：URL 的 host = orbID，每个请求一条新流（DisableKeepAlives）。
// 它不检查 orb 状态（调用方先 EnsureUsable）；没有已 hello 的 link 时拨号返回 ErrOrbNotReady。
func (s *Service) Transport() http.RoundTripper {
	s.httpOnce.Do(func() {
		s.httpTr = &http.Transport{
			DialContext:        s.dialFS,
			DisableKeepAlives:  true,
			DisableCompression: true, // 透明代理：不擅自加 Accept-Encoding、不解压
			// 不设 ResponseHeaderTimeout：512MiB 上传在请求体传完后才有响应头；由请求 ctx 取消
			MaxResponseHeaderBytes: 64 << 10,
		}
	})
	return s.httpTr
}

// dialFS 在 orb 当前 link 上打开一条 fs 流；addr 的主机名即 orbID。
func (s *Service) dialFS(ctx context.Context, _, addr string) (net.Conn, error) {
	orbID, _, err := net.SplitHostPort(addr)
	if err != nil {
		orbID = addr
	}
	s.mu.Lock()
	l := s.links[orbID]
	s.mu.Unlock()
	if l == nil || !l.helloSeen() {
		return nil, fmt.Errorf("%w: orb %s has no link", ErrOrbNotReady, orbID)
	}
	if !slices.Contains(l.hello.Features, FeatureFS) {
		return nil, ErrOrbdOutdated
	}
	st, err := l.sess.Open(ctx, link.StreamHeader{T: link.StreamFS})
	if err != nil {
		return nil, err
	}
	return st, nil
}

// Do 把 req 发到 orb 的 orbd 本机 API：只取 req.URL 的 Path 与 RawQuery，其余（方法、头、体）原样。
// 不跟随重定向；调用方负责关闭响应体。
func (s *Service) Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error) {
	out := req.Clone(ctx)
	out.URL = &url.URL{Scheme: "http", Host: orbID, Path: req.URL.Path, RawPath: req.URL.RawPath, RawQuery: req.URL.RawQuery}
	out.Host = orbID
	out.RequestURI = ""
	return s.Transport().RoundTrip(out)
}

// EnsureUsable 确认 orb 可经 fs 流服务 feature：有已 hello 的 link、hello.features 含 feature、
// 状态 ∈ {ready, setting_up, resuming}。
//
// wake=false（后台轮询，bg=1）：不计用户交互、不唤醒，不可用即返回 ErrOrbNotReady。
// wake=true：先记一次用户交互，再判定可用性并按需唤醒，然后每 100ms 复查至多 wait；进入 failed 立即返回 ErrOrbNotReady。
// 交互必须先于判定写入：空闲冻结在事务内复核交互时间，因此冻结要么先于交互提交（此处看到 freezing / frozen 而唤醒），
// 要么因看到新交互而放弃；先判定后记交互会让中间提交的冻结被一个过期的「可用」掩盖。
// 已 failed 的 orb 不会因文件请求被重试（重试走 threads/{id}/wake）。
func (s *Service) EnsureUsable(ctx context.Context, orbID, feature string, wake bool, wait time.Duration) error {
	if !wake {
		orb, ok, err := s.usable(ctx, orbID, feature)
		if err != nil || ok {
			return err
		}
		return fmt.Errorf("%w: orb %s is %s", ErrOrbNotReady, orbID, orb.State)
	}
	s.TouchUser(ctx, orbID)
	if hookAfterTouch != nil {
		hookAfterTouch(orbID)
	}
	orb, ok, err := s.usable(ctx, orbID, feature)
	if err != nil || ok {
		return err
	}
	if orb.State == StateFailed {
		return fmt.Errorf("%w: orb %s failed", ErrOrbNotReady, orbID)
	}
	if err := s.Wake(ctx, orbID); err != nil {
		if errors.Is(err, ErrInvalidState) {
			return ErrOrbGone
		}
		return err
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%w: orb %s did not become ready within %s", ErrOrbNotReady, orbID, wait)
		case <-tick.C:
		}
		orb, ok, err = s.usable(ctx, orbID, feature)
		if err != nil || ok {
			return err
		}
		if orb.State == StateFailed {
			return fmt.Errorf("%w: orb %s failed", ErrOrbNotReady, orbID)
		}
	}
}

// hookAfterTouch 仅供测试：前台 EnsureUsable 记下用户交互之后立即调用（在此提交并发的冻结）。
var hookAfterTouch func(orbID string)

// usable 读取 orb 并判定可用性。已删除 → ErrOrbGone；有已 hello 的 link 但缺少 feature → ErrOrbdOutdated
// （即使 orb 已冻结也据此提前判定，避免为旧版 orbd 白白唤醒）。
func (s *Service) usable(ctx context.Context, orbID, feature string) (db.Orb, bool, error) {
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return orb, false, err
	}
	if orb.State == StateDeleting || orb.State == StateDeleted {
		return orb, false, ErrOrbGone
	}
	s.mu.Lock()
	l := s.links[orbID]
	s.mu.Unlock()
	if l == nil || !l.helloSeen() {
		return orb, false, nil
	}
	if !slices.Contains(l.hello.Features, feature) {
		return orb, false, ErrOrbdOutdated
	}
	return orb, usableState(orb.State), nil
}
