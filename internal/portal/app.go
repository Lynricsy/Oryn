package portal

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Lynricsy/Oryn/internal/orbs"
)

// box app 主机（M23）：`<app>.<portal_domain>` 代理到 box 的 orb 上该 app 的端口。
// 公开 app 放行匿名访客（键 app/<名字>），私有 app 与 thread portal 同一套所有者握手（`/portal/auth?h=<app>`）。
// 与 thread portal 的区别：不计用户交互、不唤醒（box 的启停只由 boxes.desired 决定），没有 live release 时不拨号，
// 普通响应不改写防嵌套头（公开站点由 app 自己决定）。

// AppTarget 是 app 主机解析出的目标。
type AppTarget struct {
	BoxID, App, OrbID string
	Port              int
	Public, Live      bool // Live = 已有成功激活过的 release
}

// Apps 解析 box app（由 *boxes.Service 实现）。
type Apps interface {
	ResolveApp(ctx context.Context, name string) (AppTarget, bool, error)
}

// SetApps 接入 box app 的解析；须在开始服务请求之前调用。未设置时 app 主机一律 404。
func (s *Service) SetApps(a Apps) { s.apps = a }

// DropApp 取消该 app 的全部匿名连接（改成私有、下线时调用）；同时递增登记表的 epoch，
// 正在判定中的匿名请求随之重新判定。
func (s *Service) DropApp(app string) { s.appReg.cancelKey(appAnonKey(app)) }

func appAnonKey(app string) string { return "app/" + app }

// serveApp 处理 app 主机上的请求。次序是硬约束：epoch 先于解析取得，「改成私有」与在途匿名请求赛跑时，
// 匿名请求要么登记在 DropApp 之前、被它取消，要么登记时发现 epoch 变了、整个重新判定。
func (s *Service) serveApp(w http.ResponseWriter, r *http.Request, name string, nav bool) {
	ctx := r.Context()
	epoch := s.appReg.current()
	p, ok := s.resolveApp(w, r, name, nav)
	if !ok {
		return
	}
	if isReserved(r.URL.Path) {
		s.reserved(w, r, p)
		return
	}
	who, ok := s.admitApp(w, r, p)
	if !ok {
		return
	}

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var leave func()
	var err error
	for i := range 3 {
		leave, err = s.appReg.enter(epoch, p.orbID, who.key, who.anon, cancel)
		if !errors.Is(err, errStale) || i == 2 {
			break
		}
		// 判定与登记之间发生过吊销 / DropApp：app 可能已改成私有、下线或换了 box，从解析起整个重来
		epoch = s.appReg.current()
		if p, ok = s.resolveApp(w, r, name, nav); !ok {
			return
		}
		if who, ok = s.admitApp(w, r, p); !ok {
			return
		}
	}
	if err != nil {
		w.Header().Set("Retry-After", "3")
		s.fail(w, nav, http.StatusServiceUnavailable, "portal_busy", "应用并发连接过多，请稍后重试")
		return
	}
	defer leave()
	t := &target{
		orbID: p.orbID, port: p.port, owner: !who.anon, nav: nav, clientCtx: ctx,
		app: true, preview: r.URL.Query().Has("oryn-preview"),
	}
	s.proxy.ServeHTTP(w, r.WithContext(context.WithValue(pctx, targetKey{}, t)))
}

// resolveApp 解析 app 与它所在的 orb；返回 false 时已写出响应。
func (s *Service) resolveApp(w http.ResponseWriter, r *http.Request, name string, nav bool) (*portalReq, bool) {
	ctx := r.Context()
	at, found, err := s.apps.ResolveApp(ctx, name)
	if err != nil {
		s.internal(w, nav, "读取 box app 失败", err)
		return nil, false
	}
	if !found {
		s.fail(w, nav, http.StatusNotFound, "portal_not_found", "应用不存在")
		return nil, false
	}
	orb, err := s.orbs.Get(ctx, at.OrbID)
	if errors.Is(err, orbs.ErrNotFound) {
		s.fail(w, nav, http.StatusNotFound, "portal_not_found", "应用不存在")
		return nil, false
	}
	if err != nil {
		s.internal(w, nav, "读取 orb 失败", err)
		return nil, false
	}
	if orb.State == orbs.StateDeleting || orb.State == orbs.StateDeleted {
		s.fail(w, nav, http.StatusNotFound, "portal_not_found", "应用不存在")
		return nil, false
	}
	root := s.base.AppURL(name)
	return &portalReq{
		port: at.Port, label: name, orbID: orb.ID, orbState: orb.State, nav: nav,
		root: root, origin: strings.TrimSuffix(root, "/"), app: &at,
	}, true
}

// admitApp 依次做鉴权、部署状态与可用性判定；返回 false 时已写出响应。
func (s *Service) admitApp(w http.ResponseWriter, r *http.Request, p *portalReq) (identity, bool) {
	who, ok := s.authorize(w, r, p)
	if !ok {
		return identity{}, false
	}
	// 放在鉴权之后：私有 app 不向匿名访客透露部署状态。也不能交给拨号去发现——
	// 同一台 box 上若有普通服务恰好开着这个端口，请求会落到别人头上。
	if !p.app.Live {
		s.fail(w, p.nav, http.StatusBadGateway, "app_not_deployed", "这个应用还没有部署成功")
		return identity{}, false
	}
	// 不计用户交互、不唤醒：box 的启停只由 boxes.desired 决定
	err := s.orbs.EnsureUsable(r.Context(), p.orbID, featurePortal, false, 0)
	if errors.Is(err, orbs.ErrOrbNotReady) {
		w.Header().Set("Retry-After", "5")
		s.failPage(w, p.nav, http.StatusServiceUnavailable, "app_unavailable", "这个应用暂时不可用", 5)
		return identity{}, false
	}
	if err != nil {
		s.usableErr(w, p, err)
		return identity{}, false
	}
	return who, true
}
