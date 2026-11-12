package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/orbs"
)

// OrbsDeps 是 Lifecycle 领域（orb、provider 协议、统计）的依赖。
type OrbsDeps struct {
	// Service 是 orb 生命周期服务；其 Jobs() 提供 /provider/v1/* 处理器。
	Service *orbs.Service
}

// orbsSvc 返回 orb 服务；未注入时写出 503。
func (s *Server) orbsSvc(w http.ResponseWriter) (*orbs.Service, bool) {
	if s.Orbs == nil || s.Orbs.Service == nil {
		WriteError(w, http.StatusServiceUnavailable, "orb_unavailable", "orb 服务未启用")
		return nil, false
	}
	return s.Orbs.Service, true
}

// writeOrbErr 把 orbs 包的错误映射为 {code, message}。
func writeOrbErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, orbs.ErrNotFound):
		WriteError(w, http.StatusNotFound, "not_found", "orb 不存在")
	case errors.Is(err, orbs.ErrInvalidState):
		WriteError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, orbs.ErrNoProvider), errors.Is(err, orbs.ErrBadType):
		WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// writeOrbHTTPErr 把 orb HTTP 通道（EnsureUsable / 拨号）的错误映射为 {code, message}。
// bg 为真（后台轮询、不唤醒）时不可用回 409 orb_not_ready；否则（唤醒超时或 failed）回 503 orb_unavailable。
func writeOrbHTTPErr(w http.ResponseWriter, err error, bg bool) {
	switch {
	case errors.Is(err, orbs.ErrNotFound):
		WriteError(w, http.StatusNotFound, "not_found", "orb 不存在")
	case errors.Is(err, orbs.ErrOrbGone):
		WriteError(w, http.StatusGone, "orb_deleted", "orb 已删除")
	case errors.Is(err, orbs.ErrOrbdOutdated):
		WriteError(w, http.StatusConflict, "orbd_outdated", "该 orb 的 orbd 版本过旧，不支持此功能；新建线程即可")
	case errors.Is(err, orbs.ErrOrbNotReady) && bg:
		WriteError(w, http.StatusConflict, "orb_not_ready", err.Error())
	case errors.Is(err, orbs.ErrOrbNotReady):
		w.Header().Set("Retry-After", "3")
		WriteError(w, http.StatusServiceUnavailable, "orb_unavailable", err.Error())
	case errors.Is(err, context.Canceled):
		// 客户端已离开
	default:
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}
