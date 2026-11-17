package api

// owner_memory.go：M17 全局助手主人记忆的管理接口。

import (
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/ownermem"
)

// OwnerMemoryDeps 是 M17 主人记忆接口的依赖。
type OwnerMemoryDeps struct {
	Service *ownermem.Service
}

func (s *Server) ownerMemorySvc(w http.ResponseWriter) (*ownermem.Service, bool) {
	if s.OwnerMemory == nil || s.OwnerMemory.Service == nil {
		WriteError(w, http.StatusNotImplemented, "owner_memory_unavailable", "助手记忆服务未装配")
		return nil, false
	}
	return s.OwnerMemory.Service, true
}

// writeOwnerMemoryErr 写出主人记忆的错误：领域错误按自带的状态与码，其余按通用映射。
func writeOwnerMemoryErr(w http.ResponseWriter, r *http.Request, err error) {
	var me *memory.Error
	if errors.As(err, &me) {
		WriteError(w, me.Status, me.Code, me.Message)
		return
	}
	writeErr(w, r, err)
}

// GetOwnerMemorySettings 对应 `GET /api/v1/owner-memory/settings`。
func (s *Server) GetOwnerMemorySettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Settings(r.Context())
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutOwnerMemorySettings 对应 `PUT /api/v1/owner-memory/settings`。
func (s *Server) PutOwnerMemorySettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	var in apigen.OwnerMemorySettingsUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutSettings(r.Context(), in)
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutOwnerMemoryDecideEndpoint 对应 `PUT /api/v1/owner-memory/decide-endpoint`。
func (s *Server) PutOwnerMemoryDecideEndpoint(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	var in apigen.OwnerMemoryDecideUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutDecideEndpoint(r.Context(), in)
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// DeleteOwnerMemoryDecideEndpoint 对应 `DELETE /api/v1/owner-memory/decide-endpoint`。
func (s *Server) DeleteOwnerMemoryDecideEndpoint(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.DeleteDecideEndpoint(r.Context())
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetOwnerMemoryStatus 对应 `GET /api/v1/owner-memory/status`。
func (s *Server) GetOwnerMemoryStatus(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Status(r.Context())
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// ListOwnerMemoryTopics 对应 `GET /api/v1/owner-memory/topics`。
func (s *Server) ListOwnerMemoryTopics(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Topics(r.Context())
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// RefreshOwnerMemoryTopic 对应 `POST /api/v1/owner-memory/topics/{topicId}/refresh`。
func (s *Server) RefreshOwnerMemoryTopic(w http.ResponseWriter, r *http.Request, topicID apigen.OwnerMemoryTopicIDPath) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	if err := svc.RefreshTopic(r.Context(), topicID); err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// CurateOwnerMemoryTopics 对应 `POST /api/v1/owner-memory/curate`。
func (s *Server) CurateOwnerMemoryTopics(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Curate(r.Context())
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// ListOwnerMemoryFacts 对应 `GET /api/v1/owner-memory/facts`。
func (s *Server) ListOwnerMemoryFacts(w http.ResponseWriter, r *http.Request, params apigen.ListOwnerMemoryFactsParams) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Facts(r.Context(), params)
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// SetOwnerMemoryFactState 对应 `PUT /api/v1/owner-memory/facts/{factId}/state`。
func (s *Server) SetOwnerMemoryFactState(w http.ResponseWriter, r *http.Request, factID apigen.OwnerMemoryFactIDPath) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	var in apigen.OwnerMemoryFactStateUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := svc.SetFactState(r.Context(), factID, in); err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListOwnerMemorySources 对应 `GET /api/v1/owner-memory/sources`。
func (s *Server) ListOwnerMemorySources(w http.ResponseWriter, r *http.Request, params apigen.ListOwnerMemorySourcesParams) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Sources(r.Context(), params)
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// ForgetOwnerMemorySource 对应 `DELETE /api/v1/owner-memory/sources/{id}`。
func (s *Server) ForgetOwnerMemorySource(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.ForgetSource(r.Context(), id)
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// ListOwnerMemoryExclusions 对应 `GET /api/v1/owner-memory/exclusions`。
func (s *Server) ListOwnerMemoryExclusions(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Exclusions(r.Context())
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutOwnerMemoryExclusion 对应 `PUT /api/v1/owner-memory/exclusions/{kind}/{targetId}`。
func (s *Server) PutOwnerMemoryExclusion(w http.ResponseWriter, r *http.Request, kind apigen.OwnerMemoryExclusionKind, targetID string) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.PutExclusion(r.Context(), kind, targetID)
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// DeleteOwnerMemoryExclusion 对应 `DELETE /api/v1/owner-memory/exclusions/{kind}/{targetId}`。
func (s *Server) DeleteOwnerMemoryExclusion(w http.ResponseWriter, r *http.Request, kind apigen.OwnerMemoryExclusionKind, targetID string) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteExclusion(r.Context(), kind, targetID); err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListOwnerMemoryExplicit 对应 `GET /api/v1/owner-memory/explicit`。
func (s *Server) ListOwnerMemoryExplicit(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.ListExplicit(r.Context())
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateOwnerMemoryExplicit 对应 `POST /api/v1/owner-memory/explicit`。
func (s *Server) CreateOwnerMemoryExplicit(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	var in apigen.MemoryContent
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.CreateExplicit(r.Context(), in.Content)
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// UpdateOwnerMemoryExplicit 对应 `PATCH /api/v1/owner-memory/explicit/{explicitId}`。
func (s *Server) UpdateOwnerMemoryExplicit(w http.ResponseWriter, r *http.Request, explicitID apigen.OwnerMemoryExplicitIDPath) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	var in apigen.MemoryContent
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.UpdateExplicit(r.Context(), explicitID, in.Content)
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// DeleteOwnerMemoryExplicit 对应 `DELETE /api/v1/owner-memory/explicit/{explicitId}`。
func (s *Server) DeleteOwnerMemoryExplicit(w http.ResponseWriter, r *http.Request, explicitID apigen.OwnerMemoryExplicitIDPath) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteExplicit(r.Context(), explicitID); err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ApproveOwnerMemoryExplicit 对应 `POST /api/v1/owner-memory/explicit/{explicitId}/approve`。
func (s *Server) ApproveOwnerMemoryExplicit(w http.ResponseWriter, r *http.Request, explicitID apigen.OwnerMemoryExplicitIDPath) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	var in apigen.MemoryCandidateApprove
	if !decodeOptionalJSON(w, r, &in) {
		return
	}
	out, err := svc.ApproveExplicit(r.Context(), explicitID, in.Content)
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}
