package api

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/portal"
)

// 受管服务与 portal 分享接口（M4 §3.5）。服务列表读控制面镜像；启停与日志经 orb link 的 fs 流转发到
// orbd 本机 API `/services/*`（hello 未声明 services 的旧 orbd 由 EnsureUsable 得到 409 orbd_outdated）；
// 公开分享委托 internal/portal。

const (
	// serviceCallTimeout 是服务启动 / 重启（orbd 侧等待就绪至多 60s）的代理上限。
	serviceCallTimeout = 90 * time.Second
	// maxServiceLogTail 是 GET …/log 的 tail 上限（字节）。
	maxServiceLogTail = 1 << 20
)

var serviceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// validServiceName 校验路径里的服务名；不合法时已写出 400。
func validServiceName(w http.ResponseWriter, name string) bool {
	if !serviceNameRE.MatchString(name) {
		WriteError(w, http.StatusBadRequest, "invalid_request", "服务名须匹配 ^[a-z0-9][a-z0-9-]{0,31}$")
		return false
	}
	return true
}

// ListOrbServices 实现 GET /api/v1/orbs/{id}/services：读控制面镜像，不唤醒、不计用户交互。
func (s *Server) ListOrbServices(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	ctx := r.Context()
	if _, err := s.Store.Q.GetOrb(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = orbs.ErrNotFound
		}
		writeOrbErr(w, err)
		return
	}
	services, portals, err := orbs.ServicesView(ctx, s.Store.Q, id, time.Now().UnixMilli())
	if err != nil {
		writeOrbErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.OrbServiceList{Services: services, Portals: portals})
}

// StartOrbService 实现 POST /api/v1/orbs/{id}/services/{name}/start。
func (s *Server) StartOrbService(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, name apigen.ServiceNamePath) {
	s.serviceOp(w, r, id, name, "start")
}

// StopOrbService 实现 POST /api/v1/orbs/{id}/services/{name}/stop。
func (s *Server) StopOrbService(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, name apigen.ServiceNamePath) {
	s.serviceOp(w, r, id, name, "stop")
}

// RestartOrbService 实现 POST /api/v1/orbs/{id}/services/{name}/restart。
func (s *Server) RestartOrbService(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, name apigen.ServiceNamePath) {
	s.serviceOp(w, r, id, name, "restart")
}

func (s *Server) serviceOp(w http.ResponseWriter, r *http.Request, id, name, op string) {
	if !validServiceName(w, name) {
		return
	}
	s.proxyOrb(w, r, id, "/services/"+name+"/"+op, orbProxyOpt{timeout: serviceCallTimeout, maxBody: maxOrbJSONBody})
}

// GetOrbServiceLog 实现 GET /api/v1/orbs/{id}/services/{name}/log（text/plain，tail 为字节数）。
func (s *Server) GetOrbServiceLog(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, name apigen.ServiceNamePath, params apigen.GetOrbServiceLogParams) {
	if !validServiceName(w, name) {
		return
	}
	if params.Tail != nil && (*params.Tail < 1 || *params.Tail > maxServiceLogTail) {
		WriteError(w, http.StatusBadRequest, "invalid_request", "tail 须在 1–1048576 之间")
		return
	}
	s.proxyOrb(w, r, id, "/services/"+name+"/log", orbProxyOpt{bg: bgFlag(params.Bg), text: true})
}

// portalSvc 返回 portal 服务；未配置 portal_domain（未注入）时写出 409 portal_disabled。
func (s *Server) portalSvc(w http.ResponseWriter) (*portal.Service, bool) {
	if s.PortalShares == nil || s.PortalShares.Service == nil {
		WriteError(w, http.StatusConflict, "portal_disabled", "控制面未配置 portal_domain")
		return nil, false
	}
	return s.PortalShares.Service, true
}

// shareTarget 校验端口并确认 orb 存在且未删除；失败时已写出错误。
func (s *Server) shareTarget(w http.ResponseWriter, r *http.Request, id string, port int) bool {
	if port < 1 || port > 65535 {
		WriteError(w, http.StatusBadRequest, "invalid_request", "端口须在 1–65535 之间")
		return false
	}
	orb, err := s.Store.Q.GetOrb(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "orb 不存在")
		return false
	}
	if err != nil {
		writeOrbErr(w, err)
		return false
	}
	if orb.State == orbs.StateDeleting || orb.State == orbs.StateDeleted {
		WriteError(w, http.StatusGone, "orb_deleted", "orb 已删除")
		return false
	}
	return true
}

// writeShareErr 把分享接口的错误映射为 {code, message}。
func writeShareErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, portal.ErrNotFound):
		WriteError(w, http.StatusNotFound, "portal_not_found", "该端口没有 portal")
	case errors.Is(err, orbs.ErrOrbGone):
		WriteError(w, http.StatusGone, "orb_deleted", "orb 已删除")
	default:
		writeOrbErr(w, err)
	}
}

// SetOrbPortalShare 实现 PUT /api/v1/orbs/{id}/portals/{port}/share：首次公开与续期（expiresAt = now + duration）。
func (s *Server) SetOrbPortalShare(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, port apigen.PortalPortPath) {
	svc, ok := s.portalSvc(w)
	if !ok || !s.shareTarget(w, r, id, port) {
		return
	}
	var req apigen.PortalShareRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	d, ok := portal.ShareDuration(req.Duration)
	if !ok {
		WriteError(w, http.StatusBadRequest, "invalid_request", "duration 须为 1h / 3h / 1d / 7d")
		return
	}
	out, err := svc.SetShare(r.Context(), id, port, d)
	if err != nil {
		writeShareErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// EndOrbPortalShare 实现 DELETE /api/v1/orbs/{id}/portals/{port}/share：终止公开（未公开时同样 204）。
func (s *Server) EndOrbPortalShare(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, port apigen.PortalPortPath) {
	svc, ok := s.portalSvc(w)
	if !ok || !s.shareTarget(w, r, id, port) {
		return
	}
	if err := svc.EndShare(r.Context(), id, port); err != nil {
		writeShareErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
