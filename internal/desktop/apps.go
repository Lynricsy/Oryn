package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// 桌面应用与窗口（protocol.md「M25：桌面应用与窗口」）：启动应用、操作窗口经 fs 流转发到 orbd 本机 API，
// 回包里的状态快照经 ApplyDesktopReply 写镜像（apps / windows 只认 desktop.state 通知）；图标不唤醒 orb，原样转发图片字节。

// 变量仅为测试可调小，生产代码不修改。
var (
	// maxIconBytes 是应用图标的字节上限。
	maxIconBytes int64 = 1 << 20
	// iconTimeout 是取一个图标（含读完响应体）的上限。
	iconTimeout = 15 * time.Second
)

var (
	appIDRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	windowIDRe = regexp.MustCompile(`^[0-9]{1,10}$`)
)

// LaunchApp 在 orb 桌面上启动应用 appID（每次一个新实例），把 orbd 回包的状态写进镜像。
// orbd 未声明 desktop-apps → orbs.ErrOrbdOutdated；agent 正在操作桌面 → 409 control_agent；没有该应用 → 404 app_not_found。
func (s *Service) LaunchApp(ctx context.Context, orbID, appID string) error {
	if !appIDRe.MatchString(appID) {
		return errf(http.StatusBadRequest, "invalid_request", "应用 id 无效")
	}
	return s.deskAction(ctx, orbID, "/desktop/apps/"+appID+"/launch")
}

// WindowAction 对 orb 桌面上的窗口 windowID 执行 action（activate | minimize | close），把 orbd 回包的状态写进镜像。
// 窗口已不在 → 404 window_not_found；其余错误同 LaunchApp。
func (s *Service) WindowAction(ctx context.Context, orbID, windowID, action string) error {
	if !windowIDRe.MatchString(windowID) || !apigen.OrbDesktopWindowAction(action).Valid() {
		return errf(http.StatusBadRequest, "invalid_request", "窗口 id 或操作无效")
	}
	return s.deskAction(ctx, orbID, "/desktop/windows/"+windowID+"/"+action)
}

// deskAction 唤醒 orb（至多 wakeWait）后向 orbd 发 POST path，把回包的状态快照写进镜像。
func (s *Service) deskAction(ctx context.Context, orbID, path string) error {
	if err := s.o.EnsureUsable(ctx, orbID, orbs.FeatureDesktopApps, true, wakeWait); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, browserTimeout)
	defer cancel()
	resp, err := s.orbdCall(cctx, orbID, http.MethodPost, path, nil, nil, 0)
	if err != nil {
		return appErr(err)
	}
	defer resp.Body.Close()
	var st apigen.LinkCtlDesktopStateParams
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&st); err != nil {
		return fmt.Errorf("desktop: orbd 返回的桌面状态无效: %w", err)
	}
	s.o.ApplyDesktopReply(ctx, orbID, st)
	return nil
}

// appErr 把 orbd 本机 API 的业务错误映射成同码的 *Error；not_ready 同 SetBrowser 回 503 orb_unavailable。
func appErr(err error) error {
	var oe *orbdError
	if !errors.As(err, &oe) {
		return err
	}
	switch oe.Code {
	case "control_agent":
		return errf(http.StatusConflict, oe.Code, "agent 正在操作桌面，先接管")
	case "app_not_found":
		return errf(http.StatusNotFound, oe.Code, "没有该应用")
	case "window_not_found":
		return errf(http.StatusNotFound, oe.Code, "窗口已不存在")
	case "invalid_request":
		return errf(http.StatusBadRequest, oe.Code, "orbd 拒绝了请求："+oe.Message)
	case "not_ready":
		return errf(http.StatusServiceUnavailable, "orb_unavailable", "桌面尚未就绪")
	}
	return err
}

// AppIcon 取应用 appID 的图标：返回 image/svg+xml 或 image/png 与图标字节（调用方关闭）。不唤醒 orb：
// 不可用 → 409 orb_unavailable；没有该应用或它没有图标 → 404 app_not_found；orbd 回了别的类型、没给长度或超过
// maxIconBytes → 502 orbd_bad_response。
func (s *Service) AppIcon(ctx context.Context, orbID, appID string) (contentType string, body io.ReadCloser, err error) {
	if !appIDRe.MatchString(appID) {
		return "", nil, errf(http.StatusBadRequest, "invalid_request", "应用 id 无效")
	}
	if err := s.o.EnsureUsable(ctx, orbID, orbs.FeatureDesktopApps, false, 0); err != nil {
		if errors.Is(err, orbs.ErrOrbNotReady) {
			return "", nil, errf(http.StatusConflict, "orb_unavailable", "orb 未运行；取图标不唤醒它")
		}
		return "", nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, iconTimeout)
	resp, err := s.orbdCall(cctx, orbID, http.MethodGet, "/desktop/apps/"+appID+"/icon", nil, nil, 0)
	if err != nil {
		cancel()
		if orbdCode(err) == "app_not_found" {
			return "", nil, errf(http.StatusNotFound, "app_not_found", "没有该应用或它没有图标")
		}
		return "", nil, err
	}
	// 内容来自 orb：类型只认两种图片（不让 orb 借控制面的源站回 text/html），长度须事先声明且不超上限
	mt, _, perr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if perr != nil || (mt != "image/svg+xml" && mt != "image/png") || resp.ContentLength < 0 || resp.ContentLength > maxIconBytes {
		_ = resp.Body.Close()
		cancel()
		return "", nil, errf(http.StatusBadGateway, "orbd_bad_response", "orbd 返回的图标无效")
	}
	return mt, &iconBody{Reader: io.LimitReader(resp.Body, resp.ContentLength), body: resp.Body, cancel: cancel}, nil
}

// iconBody 是转发中的图标：关闭时一并结束请求的超时上下文。
type iconBody struct {
	io.Reader
	body   io.Closer
	cancel context.CancelFunc
}

func (b *iconBody) Close() error {
	err := b.body.Close()
	b.cancel()
	return err
}
