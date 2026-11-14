package api

import (
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// ListSpaces 对应 `GET spaces?archived=`。
func (s *Server) ListSpaces(w http.ResponseWriter, r *http.Request, params apigen.ListSpacesParams) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	list, err := svc.ListSpaces(r.Context(), params.Archived != nil && *params.Archived)
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.SpaceList{Spaces: list})
}

// CreateSpace 对应 `POST spaces`：协调宿主不可用时 409 coordinator_unavailable（message 是原因）。
func (s *Server) CreateSpace(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	var req apigen.SpaceCreate
	if !decodeJSON(w, r, &req) {
		return
	}
	sp, err := svc.CreateSpace(r.Context(), req)
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, sp)
}

// GetSpace 对应 `GET spaces/{id}`。
func (s *Server) GetSpace(w http.ResponseWriter, r *http.Request, id apigen.SpaceIDPath) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	sp, err := svc.GetSpace(r.Context(), id)
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, sp)
}

// UpdateSpace 对应 `PATCH spaces/{id}`。
func (s *Server) UpdateSpace(w http.ResponseWriter, r *http.Request, id apigen.SpaceIDPath) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	var req apigen.SpacePatch
	if !decodeJSON(w, r, &req) {
		return
	}
	sp, err := svc.UpdateSpace(r.Context(), id, req)
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, sp)
}

// DeleteSpace 对应 `DELETE spaces/{id}`：线程移出 Space（不删除），聊天线程随 Space 删除。
func (s *Server) DeleteSpace(w http.ResponseWriter, r *http.Request, id apigen.SpaceIDPath) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteSpace(r.Context(), id); err != nil {
		writeCoordErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetSpaceProgress 对应 `GET spaces/{id}/progress?days=&tz=`。
func (s *Server) GetSpaceProgress(w http.ResponseWriter, r *http.Request, id apigen.SpaceIDPath, params apigen.GetSpaceProgressParams) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	days := 30
	if params.Days != nil {
		if *params.Days < 1 || *params.Days > 90 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "days 须在 1–90 之间")
			return
		}
		days = *params.Days
	}
	loc := time.UTC
	if params.Tz != nil && *params.Tz != "" {
		l, err := time.LoadLocation(*params.Tz)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_request", "tz 不是合法的 IANA 时区")
			return
		}
		loc = l
	}
	p, err := svc.Progress(r.Context(), id, days, loc)
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, p)
}
