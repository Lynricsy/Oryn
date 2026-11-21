package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
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
	"profile_not_found":    http.StatusNotFound,
	"profile_in_use":       http.StatusConflict,
	"queue_full":           http.StatusTooManyRequests,
	"agent_error":          http.StatusBadGateway,
	"timeout":              http.StatusGatewayTimeout,
	// M9
	threads.CodeCoordinatorRestricted: http.StatusBadRequest,
	"coordinator_unavailable":         http.StatusConflict,
	"space_not_found":                 http.StatusNotFound,
	"space_thread":                    http.StatusConflict,
	"archived":                        http.StatusConflict,
	// M11a：把线程挂到预热池里的 orb 上
	threads.CodeOrbIsPooled: http.StatusConflict,
	// M11b：runner
	threads.CodeRunnerOffline:     http.StatusConflict,
	threads.CodeRunnerUnsupported: http.StatusConflict,
	threads.CodeDirNotServed:      http.StatusBadRequest,
	threads.CodeDirInUse:          http.StatusConflict,
	threads.CodeWorktreeFailed:    http.StatusBadRequest,
	threads.CodeWorktreeDirty:     http.StatusConflict,
	"conflict":                    http.StatusConflict,
	// M19：主线对话不能归档、标记已交付或删除
	threads.CodeMainConversation: http.StatusConflict,
	// M19：把线程挂到助手的电脑上
	threads.CodeOrbIsComputer: http.StatusConflict,
	// M23：box
	threads.CodeBoxStopped:     http.StatusConflict,
	threads.CodeBoxUnsupported: http.StatusConflict,
	threads.CodeOrbIsBox:       http.StatusConflict,
}

func writeThreadErr(w http.ResponseWriter, err error) {
	var te *threads.Error
	if errors.As(err, &te) {
		status, ok := threadErrStatus[te.Code]
		if !ok {
			status = http.StatusBadRequest
		}
		if te.Body != nil {
			WriteJSON(w, status, te.Body)
			return
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
	if params.Kind != nil {
		if !params.Kind.Valid() {
			WriteError(w, http.StatusBadRequest, "invalid_request", "kind 须为 work / space / assistant / all")
			return
		}
		f.Kind = string(*params.Kind)
	}
	if params.Space != nil {
		f.Space = *params.Space
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
	p := threads.CreateParams{
		Prompt: req.Prompt, Images: req.Images, Model: req.Model, Labels: req.Labels, Attachments: req.Attachments,
		BrowserProfileID: req.BrowserProfileID,
	}
	if req.ProfileMode != nil {
		if !req.ProfileMode.Valid() {
			WriteError(w, http.StatusBadRequest, "invalid_request", "profileMode 须为 exclusive / readonly / takeover")
			return
		}
		p.ProfileMode = *req.ProfileMode
	}
	if req.ProjectID != nil {
		p.ProjectID = *req.ProjectID
	}
	if req.ThinkingLevel != nil {
		p.ThinkingLevel = *req.ThinkingLevel
	}
	if req.Role != nil {
		p.Role = *req.Role
	}
	if req.Title != nil {
		p.Title = *req.Title
	}
	if req.SpaceID != nil && *req.SpaceID != "" {
		p.SpaceID = *req.SpaceID
		// 未给项目时取 Space 的项目
		if p.ProjectID == "" {
			sp, err := s.Store.Q.GetSpace(r.Context(), p.SpaceID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					WriteError(w, http.StatusNotFound, "space_not_found", "space not found")
					return
				}
				writeThreadErr(w, err)
				return
			}
			if sp.ProjectID != nil {
				p.ProjectID = *sp.ProjectID
			}
		}
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
				if *e.Provider == orbs.HostProviderName {
					WriteError(w, http.StatusBadRequest, threads.CodeCoordinatorRestricted, "provider "+orbs.HostProviderName+" is reserved for coordinator conversations")
					return
				}
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
		case apigen.ThreadExecutorRunner:
			if e.RunnerID == "" || e.Dir == "" {
				WriteError(w, http.StatusBadRequest, "invalid_request", "executor.runnerId and executor.dir required")
				return
			}
			p.RunnerID, p.Dir = e.RunnerID, e.Dir
			if e.Worktree != nil {
				if e.Worktree.Name == "" {
					WriteError(w, http.StatusBadRequest, "invalid_request", "executor.worktree.name required")
					return
				}
				p.Worktree = e.Worktree.Name
			}
			p.ShareDir = e.ShareDir != nil && *e.ShareDir
		case apigen.ThreadExecutorBox:
			// M23：box 的 orb 与工作目录由服务端解析（停机、app 是否部署），客户端不给路径
			if e.BoxID == "" {
				WriteError(w, http.StatusBadRequest, "invalid_request", "executor.boxId required")
				return
			}
			bx, ok := s.boxesSvc(w)
			if !ok {
				return
			}
			app := ""
			if e.App != nil {
				app = *e.App
			}
			orbID, cwd, err := bx.ThreadTarget(r.Context(), e.BoxID, app)
			if err != nil {
				writeBoxErr(w, r, err)
				return
			}
			p.OrbID, p.Cwd = orbID, cwd
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

// ListThreadEntries 对应 `GET threads/{id}/entries?after=&before=&limit=`；after 与 before 互斥。
func (s *Server) ListThreadEntries(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath, params apigen.ListThreadEntriesParams) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	if params.After != nil && params.Before != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "after and before are mutually exclusive")
		return
	}
	limit := 0
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 500 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "limit must be 1..500")
			return
		}
		limit = *params.Limit
	}
	var page threads.EntryPage
	var err error
	if params.Before != nil {
		page, err = svc.EntriesBefore(r.Context(), id, *params.Before, limit)
	} else {
		var after int64
		if params.After != nil {
			after = *params.After
		}
		page, err = svc.Entries(r.Context(), id, after, limit)
	}
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
