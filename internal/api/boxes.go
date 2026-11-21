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
