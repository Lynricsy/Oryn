package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// threadErrStatus 把线程服务的错误码映射为 HTTP 状态。
var threadErrStatus = map[string]int{
	"not_found":            http.StatusNotFound,
	"attachment_not_found": http.StatusNotFound,
	"invalid":              http.StatusBadRequest,
	"invalid_frame":        http.StatusBadRequest,
	"orb_create_failed":    http.StatusBadRequest,
	"no_orb":               http.StatusConflict,
	"orb_unavailable":      http.StatusConflict,
	"queue_full":           http.StatusTooManyRequests,
	"agent_error":          http.StatusBadGateway,
	"timeout":              http.StatusGatewayTimeout,
}

func writeThreadErr(w http.ResponseWriter, err error) {
	var te *threads.Error
	if errors.As(err, &te) {
		status, ok := threadErrStatus[te.Code]
		if !ok {
			status = http.StatusBadRequest
		}
		WriteError(w, status, te.Code, te.Message)
		return
	}
	slog.Error("api: 线程请求失败", "err", err)
	WriteError(w, http.StatusInternalServerError, "internal", "internal error")
}

func (s *Server) threadsSvc(w http.ResponseWriter) (*threads.Service, bool) {
	if s.Threads == nil || s.Threads.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "threads service not configured")
		return nil, false
	}
	return s.Threads.Service, true
}

// ListThreads 对应 `GET threads?state=&project=&archived=&q=`。
func (s *Server) ListThreads(w http.ResponseWriter, r *http.Request, params apigen.ListThreadsParams) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	f := threads.ListFilter{Archived: params.Archived}
	if params.State != nil {
		f.State = string(*params.State)
	}
	if params.Project != nil {
		f.Project = *params.Project
	}
	if params.Q != nil {
		f.Q = *params.Q
	}
	list, err := svc.List(r.Context(), f)
	if err != nil {
		writeThreadErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.ThreadList{Threads: list})
}

// CreateThread 对应 `POST threads`。
func (s *Server) CreateThread(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	var req apigen.CreateThreadRequest
	if !decodeThreadBody(w, r, &req) {
		return
	}
	p := threads.CreateParams{Prompt: req.Prompt, Images: req.Images, Model: req.Model, Labels: req.Labels, Attachments: req.Attachments}
	if req.ProjectID != nil {
		p.ProjectID = *req.ProjectID
	}
	if req.ThinkingLevel != nil {
		p.ThinkingLevel = *req.ThinkingLevel
	}
	if req.Title != nil {
		p.Title = *req.Title
	}
	if kind, _ := req.Executor.Discriminator(); kind != "" {
		v, err := req.Executor.ValueByDiscriminator()
		if err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_request", "executor: "+err.Error())
			return
		}
		switch e := v.(type) {
		case apigen.ThreadExecutorNewOrb:
			if e.Provider != nil {
				p.Provider = *e.Provider
			}
			if e.Type != nil {
				p.Type = *e.Type
			}
		case apigen.ThreadExecutorOrb:
			if e.OrbID == "" {
				WriteError(w, http.StatusBadRequest, "invalid_request", "executor.orbId required")
				return
			}
			p.OrbID = e.OrbID
		}
	}
	t, err := svc.Create(r.Context(), p)
	if err != nil {
		writeThreadErr(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, apigen.ThreadEnvelope{Thread: t})
}

// maxThreadBody 是带内联图片的新建线程请求体上限。
const maxThreadBody = 16 << 20

func decodeThreadBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxThreadBody)).Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON: "+err.Error())
		return false
	}
	return true
}

// GetThread 对应 `GET threads/{id}`。
func (s *Server) GetThread(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	t, err := svc.Get(r.Context(), id)
	if err != nil {
		writeThreadErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, t)
}

// UpdateThread 对应 `PATCH threads/{id}`；改名同时向 agent 发 set_session_name。
func (s *Server) UpdateThread(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	var p apigen.ThreadPatch
	if !decodeJSON(w, r, &p) {
		return
	}
	t, err := svc.Patch(r.Context(), id, p)
	if err != nil {
		writeThreadErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, t)
}

// DeleteThread 对应 `DELETE threads/{id}`。
func (s *Server) DeleteThread(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	if err := svc.Delete(r.Context(), id); err != nil {
		writeThreadErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListThreadEntries 对应 `GET threads/{id}/entries?after=&limit=`。
func (s *Server) ListThreadEntries(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath, params apigen.ListThreadEntriesParams) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	var after int64
	if params.After != nil {
		after = *params.After
	}
	limit := 0
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 500 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "limit must be 1..500")
			return
		}
		limit = *params.Limit
	}
	page, err := svc.Entries(r.Context(), id, after, limit)
	if err != nil {
		writeThreadErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, page)
}

// ForkThread 对应 `POST threads/{id}/fork {entryId?, env}`。
func (s *Server) ForkThread(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	var req apigen.ForkThreadRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := svc.Fork(r.Context(), id, req.EntryID, string(req.Env))
	if err != nil {
		writeThreadErr(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, apigen.ThreadEnvelope{Thread: t})
}

// WakeThread 对应 `POST threads/{id}/wake`（异步）。
func (s *Server) WakeThread(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	if err := svc.Wake(r.Context(), id); err != nil {
		writeThreadErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
