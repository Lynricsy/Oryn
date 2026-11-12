package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
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

// orbView 把库记录转为协议形态（附 providerName 与 threadIds）。
func (s *Server) orbView(ctx context.Context, o db.Orb, names map[string]string) (apigen.Orb, error) {
	threadIDs, err := s.Store.Q.ListThreadIDsByOrb(ctx, &o.ID)
	if err != nil {
		return apigen.Orb{}, err
	}
	name, ok := names[o.ProviderID]
	if !ok {
		if p, err := s.Store.Q.GetProvider(ctx, o.ProviderID); err == nil {
			name = p.Name
		}
		names[o.ProviderID] = name
	}
	v := apigen.Orb{
		ID: o.ID, ProviderID: o.ProviderID, Type: o.Type, Cpus: float32(o.Cpus), MemoryMB: int(o.MemoryMb),
		Image: o.Image, ProjectID: o.ProjectID, State: apigen.OrbState(o.State), StateChangedAt: o.StateChangedAt,
		SnapshotOf: o.SnapshotOf, LastAgentActivityAt: o.LastAgentActivityAt, LastUserInteractionAt: o.LastUserInteractionAt,
		OrbdVersion: o.OrbdVersion, AgentVersion: o.AgentVersion, CreatedAt: o.CreatedAt, DeletedAt: o.DeletedAt,
		ThreadIds: threadIDs,
	}
	if name != "" {
		v.ProviderName = &name
	}
	return v, nil
}
