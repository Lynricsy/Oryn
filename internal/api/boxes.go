package api

import (
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/boxes"
)

// BoxesDeps 是 Boxes 接口（M23）的依赖。
type BoxesDeps struct {
	Service *boxes.Service
}

func (s *Server) boxesSvc(w http.ResponseWriter) (*boxes.Service, bool) {
	if s.Boxes == nil || s.Boxes.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "boxes service not configured")
		return nil, false
	}
	return s.Boxes.Service, true
}

// boxErrStatus 把 boxes 的错误码映射为 HTTP 状态（未列出的码一律 400）。
var boxErrStatus = map[string]int{
	boxes.CodeInvalid:            http.StatusBadRequest,
	boxes.CodeForbidden:          http.StatusForbidden,
	boxes.CodeBoxNotFound:        http.StatusNotFound,
	boxes.CodeAppNotFound:        http.StatusNotFound,
	boxes.CodeReleaseNotFound:    http.StatusNotFound,
	boxes.CodeDeployNotFound:     http.StatusNotFound,
	boxes.CodeBoxExists:          http.StatusConflict,
	boxes.CodeBoxLimit:           http.StatusConflict,
	boxes.CodeBoxStopped:         http.StatusConflict,
	boxes.CodeBoxNotReady:        http.StatusConflict,
	boxes.CodeBoxUnsupported:     http.StatusConflict,
	boxes.CodeOrbIsBox:           http.StatusConflict,
	boxes.CodeAppExistsElsewhere: http.StatusConflict,
	boxes.CodeAppRepoMismatch:    http.StatusConflict,
	boxes.CodeAppNotDeployed:     http.StatusConflict,
	boxes.CodeAppLimit:           http.StatusConflict,
	boxes.CodeDeployInProgress:   http.StatusConflict,
	boxes.CodeBoxChanges:         http.StatusConflict,
	boxes.CodeReleaseTooLarge:    http.StatusRequestEntityTooLarge,
	// orbd 对 restart / rollback 的业务失败（OrbBoxResult.code）
	"not_listening": http.StatusConflict,
	"switch_failed": http.StatusConflict,
	"no_space":      http.StatusConflict,
	"not_found":     http.StatusNotFound,
}

// writeBoxErr 写出 boxes 服务的错误：*boxes.Error 按码映射 HTTP 状态并原样输出文案，其余按通用映射（500）。
func writeBoxErr(w http.ResponseWriter, r *http.Request, err error) {
	var be *boxes.Error
	if errors.As(err, &be) {
		status, ok := boxErrStatus[be.Code]
		if !ok {
			status = http.StatusBadRequest
		}
		WriteError(w, status, be.Code, be.Message)
		return
	}
	writeErr(w, r, err)
}

// ownerActor 是 REST 调用者：Boxes 接口只给主人（Cookie 或设备令牌）。
var ownerActor = boxes.Actor{Kind: boxes.ActorOwner}

// ListBoxes 实现 GET /api/v1/boxes。
func (s *Server) ListBoxes(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	list, err := svc.List(r.Context())
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.BoxList{Boxes: list})
}

// CreateBox 实现 POST /api/v1/boxes。
func (s *Server) CreateBox(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	var req apigen.CreateBoxRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	b, err := svc.Create(r.Context(), req.Name, deref(req.Provider), deref(req.Type), ownerActor)
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, b)
}

// GetBox 实现 GET /api/v1/boxes/{box}。
func (s *Server) GetBox(w http.ResponseWriter, r *http.Request, box apigen.BoxPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	b, err := svc.Get(r.Context(), box)
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, b)
}

// UpdateBox 实现 PATCH /api/v1/boxes/{box}：先改名，再设置或清除时段。
func (s *Server) UpdateBox(w http.ResponseWriter, r *http.Request, box apigen.BoxPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	var req apigen.UpdateBoxRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	clearSch := req.ClearSchedule != nil && *req.ClearSchedule
	if clearSch && req.Schedule != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "schedule and clearSchedule are mutually exclusive")
		return
	}
	ctx := r.Context()
	ref := box
	var b apigen.Box
	var err error
	if req.Name != nil {
		if b, err = svc.Rename(ctx, ref, *req.Name, ownerActor); err != nil {
			writeBoxErr(w, r, err)
			return
		}
		ref = b.ID
	}
	switch {
	case clearSch:
		b, err = svc.SetSchedule(ctx, ref, nil, ownerActor)
	case req.Schedule != nil:
		b, err = svc.SetSchedule(ctx, ref, req.Schedule, ownerActor)
	case req.Name == nil:
		b, err = svc.Get(ctx, ref)
	}
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, b)
}

// DeleteBox 实现 DELETE /api/v1/boxes/{box}。
func (s *Server) DeleteBox(w http.ResponseWriter, r *http.Request, box apigen.BoxPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	if err := svc.Delete(r.Context(), box, ownerActor); err != nil {
		writeBoxErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// StartBox 实现 POST /api/v1/boxes/{box}/start。
func (s *Server) StartBox(w http.ResponseWriter, r *http.Request, box apigen.BoxPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	b, err := svc.Start(r.Context(), box, ownerActor)
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, b)
}

// StopBox 实现 POST /api/v1/boxes/{box}/stop。
func (s *Server) StopBox(w http.ResponseWriter, r *http.Request, box apigen.BoxPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	b, err := svc.Stop(r.Context(), box, ownerActor)
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, b)
}

// boxActivityLimit 取 limit 查询参数（缺省 50）。
func boxActivityLimit(p *apigen.BoxActivityLimit) int {
	if p == nil {
		return 50
	}
	return *p
}

// ListBoxActivity 实现 GET /api/v1/boxes/{box}/activity。
func (s *Server) ListBoxActivity(w http.ResponseWriter, r *http.Request, box apigen.BoxPath, params apigen.ListBoxActivityParams) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	items, err := svc.Activity(r.Context(), box, boxActivityLimit(params.Limit))
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.BoxActivityList{Items: items})
}

// ListAllBoxActivity 实现 GET /api/v1/box-activity。
func (s *Server) ListAllBoxActivity(w http.ResponseWriter, r *http.Request, params apigen.ListAllBoxActivityParams) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	items, err := svc.Activity(r.Context(), "", boxActivityLimit(params.Limit))
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.BoxActivityList{Items: items})
}

// GetBoxUsage 实现 GET /api/v1/boxes/{box}/usage。
func (s *Server) GetBoxUsage(w http.ResponseWriter, r *http.Request, box apigen.BoxPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	u, err := svc.Usage(r.Context(), box)
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, u)
}

// UpdateBoxApp 实现 PATCH /api/v1/boxes/{box}/apps/{app}（公开 / 私有）。
func (s *Server) UpdateBoxApp(w http.ResponseWriter, r *http.Request, box apigen.BoxPath, app apigen.BoxAppPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	var req apigen.UpdateBoxAppRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a, err := svc.SetPublic(r.Context(), box, app, req.Public, ownerActor)
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, a)
}

// UndeployBoxApp 实现 DELETE /api/v1/boxes/{box}/apps/{app}。
func (s *Server) UndeployBoxApp(w http.ResponseWriter, r *http.Request, box apigen.BoxPath, app apigen.BoxAppPath, params apigen.UndeployBoxAppParams) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	if err := svc.Undeploy(r.Context(), box, app, deref(params.PurgeData), ownerActor); err != nil {
		writeBoxErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RestartBoxApp 实现 POST /api/v1/boxes/{box}/apps/{app}/restart。
func (s *Server) RestartBoxApp(w http.ResponseWriter, r *http.Request, box apigen.BoxPath, app apigen.BoxAppPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	a, err := svc.Restart(r.Context(), box, app, ownerActor)
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, a)
}

// RollbackBoxApp 实现 POST /api/v1/boxes/{box}/apps/{app}/rollback（releaseId 省略 = 上一个）。
func (s *Server) RollbackBoxApp(w http.ResponseWriter, r *http.Request, box apigen.BoxPath, app apigen.BoxAppPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	var req apigen.BoxRollbackRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	a, err := svc.Rollback(r.Context(), box, app, deref(req.ReleaseID), ownerActor)
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, a)
}

// CancelBoxDeploy 实现 POST /api/v1/boxes/{box}/apps/{app}/cancel-deploy。
func (s *Server) CancelBoxDeploy(w http.ResponseWriter, r *http.Request, box apigen.BoxPath, app apigen.BoxAppPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	if err := svc.CancelDeploy(r.Context(), box, app); err != nil {
		writeBoxErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListBoxReleases 实现 GET /api/v1/boxes/{box}/apps/{app}/releases。
func (s *Server) ListBoxReleases(w http.ResponseWriter, r *http.Request, box apigen.BoxPath, app apigen.BoxAppPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	rels, err := svc.Releases(r.Context(), box, app)
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.BoxReleaseList{Releases: rels})
}

// ListBoxSecrets 实现 GET /api/v1/boxes/{box}/apps/{app}/secrets。
func (s *Server) ListBoxSecrets(w http.ResponseWriter, r *http.Request, box apigen.BoxPath, app apigen.BoxAppPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	list, err := svc.Secrets(r.Context(), box, app)
	if err != nil {
		writeBoxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.BoxSecretList{Secrets: list})
}

// PutBoxSecret 实现 PUT /api/v1/boxes/{box}/apps/{app}/secrets/{name}（secret 缺省 true）。
func (s *Server) PutBoxSecret(w http.ResponseWriter, r *http.Request, box apigen.BoxPath, app apigen.BoxAppPath, name apigen.BoxSecretNamePath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	var req apigen.PutBoxSecretRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	secret := req.Secret == nil || *req.Secret
	if err := svc.PutSecret(r.Context(), box, app, name, req.Value, secret); err != nil {
		writeBoxErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteBoxSecret 实现 DELETE /api/v1/boxes/{box}/apps/{app}/secrets/{name}。
func (s *Server) DeleteBoxSecret(w http.ResponseWriter, r *http.Request, box apigen.BoxPath, app apigen.BoxAppPath, name apigen.BoxSecretNamePath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteSecret(r.Context(), box, app, name); err != nil {
		writeBoxErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ExecBox 实现 POST /api/v1/boxes/{box}/exec：把 orbd 的 NDJSON 边读边写，每次写之后 flush。客户端断开时请求随之取消，
// box 上的进程组被杀掉。
func (s *Server) ExecBox(w http.ResponseWriter, r *http.Request, box apigen.BoxPath) {
	svc, ok := s.boxesSvc(w)
	if !ok {
		return
	}
	var req apigen.BoxExecRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	fw := &flushWriter{w: w}
	if err := svc.Exec(r.Context(), box, req, fw); err != nil && !fw.started {
		writeBoxErr(w, r, err)
	}
}

// flushWriter 在第一次写时写出 200 与 NDJSON 头，之后每次写都 flush。
type flushWriter struct {
	w       http.ResponseWriter
	started bool
}

func (f *flushWriter) Write(p []byte) (int, error) {
	if !f.started {
		f.started = true
		h := f.w.Header()
		h.Set("Content-Type", "application/x-ndjson")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		f.w.WriteHeader(http.StatusOK)
	}
	n, err := f.w.Write(p)
	// ResponseController 能穿过包装了 ResponseWriter 的中间件
	_ = http.NewResponseController(f.w).Flush()
	return n, err
}
