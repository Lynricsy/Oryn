package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
)

// MemoryDeps 是 M16 记忆管理接口的依赖。
type MemoryDeps struct {
	Service *memory.Service
}

func (s *Server) memorySvc(w http.ResponseWriter) (*memory.Service, bool) {
	if s.Memory == nil || s.Memory.Service == nil {
		WriteError(w, http.StatusNotImplemented, "memory_unavailable", "记忆服务未装配")
		return nil, false
	}
	return s.Memory.Service, true
}

// writeMemoryErr 写出记忆服务的错误：领域错误按自带的状态与码，其余按通用映射。
func writeMemoryErr(w http.ResponseWriter, r *http.Request, err error) {
	var me *memory.Error
	if errors.As(err, &me) {
		WriteError(w, me.Status, me.Code, me.Message)
		return
	}
	writeErr(w, r, err)
}

// GetMemorySettings 对应 `GET /api/v1/memory/settings`。
func (s *Server) GetMemorySettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Settings(r.Context())
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutMemorySettings 对应 `PUT /api/v1/memory/settings`。
func (s *Server) PutMemorySettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	var in apigen.MemorySettingsUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutSettings(r.Context(), in)
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetMemoryStatus 对应 `GET /api/v1/memory/status`。
func (s *Server) GetMemoryStatus(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Status(r.Context())
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateMemoryEnvFile 对应 `POST /api/v1/memory/env-file`。
func (s *Server) CreateMemoryEnvFile(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	out, err := svc.GenerateEnvFile(r.Context())
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, out)
}

// ListMemoryProjects 对应 `GET /api/v1/memory/projects`。
func (s *Server) ListMemoryProjects(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	out, err := svc.ListProjects(r.Context())
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutMemoryProject 对应 `PUT /api/v1/memory/projects/{id}`。
func (s *Server) PutMemoryProject(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	var in apigen.MemoryProjectUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutProject(r.Context(), id, in.Enabled)
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// memoryReply 写出记忆服务的结果：出错按 writeMemoryErr，否则以 status 写 JSON。
func memoryReply[T any](w http.ResponseWriter, r *http.Request, status int, out T, err error) {
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, status, out)
}

// memoryNoContent 写出没有响应体的结果。
func memoryNoContent(w http.ResponseWriter, r *http.Request, status int, err error) {
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	w.WriteHeader(status)
}

// memoryCatalog 取中央模型目录里的 chat 模型（不强制刷新）；目录不可用时返回 nil。
func (s *Server) memoryCatalog(r *http.Request) []memory.CatalogModel {
	if s.Auth == nil || s.Auth.ModelCatalog == nil {
		return nil
	}
	res, err := s.Auth.ModelCatalog.Get(r.Context(), false)
	if err != nil {
		return nil
	}
	out := make([]memory.CatalogModel, 0, len(res.Models))
	for _, m := range res.Models {
		if m.Kind == "chat" {
			out = append(out, memory.CatalogModel{ProviderKey: m.Provider, ModelID: m.ID, Name: m.Name})
		}
	}
	return out
}

// GetMemoryModels 对应 `GET /api/v1/memory/models`。
func (s *Server) GetMemoryModels(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.Models(r.Context(), s.memoryCatalog(r))
		memoryReply(w, r, http.StatusOK, out, err)
	}
}

// PutMemoryModels 对应 `PUT /api/v1/memory/models`。
func (s *Server) PutMemoryModels(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	var in apigen.MemoryModelsUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutModels(r.Context(), in, s.memoryCatalog(r))
	memoryReply(w, r, http.StatusOK, out, err)
}

// ConfirmMemoryModelsDeployed 对应 `POST /api/v1/memory/models/deployed`。
func (s *Server) ConfirmMemoryModelsDeployed(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.ConfirmDeployed(r.Context(), s.memoryCatalog(r))
		memoryReply(w, r, http.StatusOK, out, err)
	}
}

// GetMemoryEndpoints 对应 `GET /api/v1/memory/endpoints`。
func (s *Server) GetMemoryEndpoints(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.Endpoints(r.Context())
		memoryReply(w, r, http.StatusOK, out, err)
	}
}

// PutMemoryEndpoint 对应 `PUT /api/v1/memory/endpoints/{kind}`。
func (s *Server) PutMemoryEndpoint(w http.ResponseWriter, r *http.Request, kind apigen.MemoryEndpointKind) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	var in apigen.MemoryEndpointUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutEndpoint(r.Context(), kind, in)
	memoryReply(w, r, http.StatusOK, out, err)
}

// DeleteMemoryEndpoint 对应 `DELETE /api/v1/memory/endpoints/{kind}`（只允许 rerank）。
func (s *Server) DeleteMemoryEndpoint(w http.ResponseWriter, r *http.Request, kind apigen.MemoryEndpointKind) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.DeleteEndpoint(r.Context(), kind)
		memoryReply(w, r, http.StatusOK, out, err)
	}
}

// GetMemoryUsage 对应 `GET /api/v1/memory/usage`。
func (s *Server) GetMemoryUsage(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.Usage(r.Context())
		memoryReply(w, r, http.StatusOK, out, err)
	}
}

// ListMemoryDocuments 对应 `GET /api/v1/memory/documents`。
func (s *Server) ListMemoryDocuments(w http.ResponseWriter, r *http.Request, params apigen.ListMemoryDocumentsParams) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	offset, limit := 0, 30
	if params.Offset != nil {
		offset = *params.Offset
	}
	if params.Limit != nil {
		limit = *params.Limit
	}
	out, err := svc.ListDocuments(r.Context(), params.ProjectID, params.Global != nil && *params.Global, offset, limit)
	memoryReply(w, r, http.StatusOK, out, err)
}

// DeleteMemoryDocument 对应 `DELETE /api/v1/memory/documents/{documentId}`（也用于删除全局记忆）。
func (s *Server) DeleteMemoryDocument(w http.ResponseWriter, r *http.Request, documentID apigen.MemoryDocumentIDPath) {
	if svc, ok := s.memorySvc(w); ok {
		memoryNoContent(w, r, http.StatusNoContent, svc.DeleteDocument(r.Context(), documentID))
	}
}

// SearchMemory 对应 `GET /api/v1/memory/search`。
func (s *Server) SearchMemory(w http.ResponseWriter, r *http.Request, params apigen.SearchMemoryParams) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.Search(r.Context(), params.Q, params.ProjectID)
		memoryReply(w, r, http.StatusOK, out, err)
	}
}

// ForgetMemoryThread 对应 `POST /api/v1/memory/threads/{id}/forget`。
func (s *Server) ForgetMemoryThread(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.ForgetThread(r.Context(), id)
		memoryReply(w, r, http.StatusOK, out, err)
	}
}

// ListMemoryCandidates 对应 `GET /api/v1/memory/candidates`。
func (s *Server) ListMemoryCandidates(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.ListCandidates(r.Context())
		memoryReply(w, r, http.StatusOK, out, err)
	}
}

// RejectMemoryCandidate 对应 `DELETE /api/v1/memory/candidates/{candidateId}`。
func (s *Server) RejectMemoryCandidate(w http.ResponseWriter, r *http.Request, candidateID apigen.MemoryCandidateIDPath) {
	if svc, ok := s.memorySvc(w); ok {
		memoryNoContent(w, r, http.StatusNoContent, svc.RejectCandidate(r.Context(), candidateID))
	}
}

// ApproveMemoryCandidate 对应 `POST /api/v1/memory/candidates/{candidateId}/approve`；请求体可省略（原样批准）。
func (s *Server) ApproveMemoryCandidate(w http.ResponseWriter, r *http.Request, candidateID apigen.MemoryCandidateIDPath) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "读取请求体失败: "+err.Error())
		return
	}
	var in apigen.MemoryCandidateApprove
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON: "+err.Error())
			return
		}
	}
	out, err := svc.ApproveCandidate(r.Context(), candidateID, in.Content)
	memoryReply(w, r, http.StatusOK, out, err)
}

// CreateGlobalMemory 对应 `POST /api/v1/memory/global`。
func (s *Server) CreateGlobalMemory(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	var in apigen.MemoryContent
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.CreateGlobal(r.Context(), in.Content)
	memoryReply(w, r, http.StatusCreated, out, err)
}

// UpdateGlobalMemory 对应 `PUT /api/v1/memory/global/{documentId}`。
func (s *Server) UpdateGlobalMemory(w http.ResponseWriter, r *http.Request, documentID apigen.MemoryDocumentIDPath) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	var in apigen.MemoryContent
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.UpdateGlobal(r.Context(), documentID, in.Content)
	memoryReply(w, r, http.StatusOK, out, err)
}

// ListMemoryMentalModels 对应 `GET /api/v1/memory/mental-models`。
func (s *Server) ListMemoryMentalModels(w http.ResponseWriter, r *http.Request, params apigen.ListMemoryMentalModelsParams) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.ListMentalModels(r.Context(), params.ProjectID)
		memoryReply(w, r, http.StatusOK, out, err)
	}
}

// RefreshMemoryMentalModel 对应 `POST /api/v1/memory/mental-models/{modelId}/refresh`。
func (s *Server) RefreshMemoryMentalModel(w http.ResponseWriter, r *http.Request, modelID string) {
	if svc, ok := s.memorySvc(w); ok {
		memoryNoContent(w, r, http.StatusAccepted, svc.RefreshMentalModel(r.Context(), modelID))
	}
}

// ListMemoryPendingDeletes 对应 `GET /api/v1/memory/pending-deletes`。
func (s *Server) ListMemoryPendingDeletes(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.ListPendingDeletes(r.Context())
		memoryReply(w, r, http.StatusOK, out, err)
	}
}

// CreateMemoryExport 对应 `POST /api/v1/memory/exports`。
func (s *Server) CreateMemoryExport(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.StartExport(r.Context())
		memoryReply(w, r, http.StatusAccepted, out, err)
	}
}

// GetMemoryExport 对应 `GET /api/v1/memory/exports/{exportId}`。
func (s *Server) GetMemoryExport(w http.ResponseWriter, r *http.Request, exportID apigen.MemoryExportIDPath) {
	if svc, ok := s.memorySvc(w); ok {
		out, err := svc.GetExport(r.Context(), exportID)
		memoryReply(w, r, http.StatusOK, out, err)
	}
}

// DownloadMemoryExport 对应 `GET /api/v1/memory/exports/{exportId}/download`：orynd 带 Bearer 从 Hindsight 取包后
// 原样转给浏览器，Hindsight 的响应头一概不转发。
func (s *Server) DownloadMemoryExport(w http.ResponseWriter, r *http.Request, exportID apigen.MemoryExportIDPath) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	arc, err := svc.OpenExport(r.Context(), exportID)
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	defer arc.Body.Close()
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="`+arc.Filename+`"`)
	h.Set("Cache-Control", "no-store")
	if arc.Size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(arc.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, arc.Body); err != nil {
		slog.Warn("转发记忆导出包中断", "export", exportID, "err", err)
	}
}
