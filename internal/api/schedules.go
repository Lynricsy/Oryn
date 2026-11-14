package api

import (
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/schedule"
)

// ScheduleDeps 是定时接口的依赖（M9 协调与定时分册 3.3）。
type ScheduleDeps struct {
	Service *schedule.Service
}

// createdByUser 是 REST 写入的定时来源（REST 一律视为主人操作）。
const createdByUser = "user"

func (s *Server) scheduleSvc(w http.ResponseWriter) (*schedule.Service, bool) {
	if s.Schedules == nil || s.Schedules.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "schedule service not configured")
		return nil, false
	}
	return s.Schedules.Service, true
}

// writeScheduleErr 把 *schedule.Error 按码映射为 HTTP 状态，其余按通用映射。
func writeScheduleErr(w http.ResponseWriter, r *http.Request, err error) {
	var se *schedule.Error
	if !errors.As(err, &se) {
		writeErr(w, r, err)
		return
	}
	status := http.StatusBadRequest
	switch se.Code {
	case schedule.CodeNotFound, schedule.CodeScheduleNotFound:
		status = http.StatusNotFound
	case schedule.CodeScheduleBusy, schedule.CodeNameTaken, schedule.CodeThreadArchived, schedule.CodeScheduleLimit, schedule.CodeConflict,
		schedule.CodeScheduleConflict:
		status = http.StatusConflict
	}
	WriteError(w, status, se.Code, se.Message)
}

// ListSchedules 对应 `GET schedules`：project=none 只列没有项目的 new_thread 定时。
func (s *Server) ListSchedules(w http.ResponseWriter, r *http.Request, params apigen.ListSchedulesParams) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	var f schedule.Filter
	if params.Mode != nil {
		if !params.Mode.Valid() {
			WriteError(w, http.StatusBadRequest, "invalid_request", "mode 须为 thread 或 new_thread")
			return
		}
		f.Mode = string(*params.Mode)
	}
	if params.Project != nil {
		if *params.Project == "none" {
			f.NoProject = true
		} else {
			f.ProjectID = *params.Project
		}
	}
	if params.Space != nil {
		f.SpaceID = *params.Space
	}
	if params.Thread != nil {
		f.ThreadID = *params.Thread
	}
	list, err := svc.List(r.Context(), f)
	if err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.ScheduleList{Schedules: list})
}

// CreateSchedule 对应 `POST schedules`（必须给 mode）。
func (s *Server) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	var in apigen.ScheduleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	sc, err := svc.Put(r.Context(), "", schedule.InputFromAPI(in), createdByUser)
	if err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, sc)
}

// PreviewSchedule 对应 `POST schedules/preview`：只看时间字段，给出接下来至多 5 次。
func (s *Server) PreviewSchedule(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	var in apigen.ScheduleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	next, err := svc.Preview(schedule.InputFromAPI(in))
	if err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.SchedulePreview{Next: next})
}

// GetSchedule 对应 `GET schedules/{id}`。
func (s *Server) GetSchedule(w http.ResponseWriter, r *http.Request, id apigen.ScheduleIDPath) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	sc, err := svc.Get(r.Context(), id)
	if err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, sc)
}

// PutSchedule 对应 `PUT schedules/{id}`：替换定义，目标不可改。
func (s *Server) PutSchedule(w http.ResponseWriter, r *http.Request, id apigen.ScheduleIDPath) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	var in apigen.ScheduleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	sc, err := svc.Put(r.Context(), id, schedule.InputFromAPI(in), createdByUser)
	if err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, sc)
}

// DeleteSchedule 对应 `DELETE schedules/{id}`。
func (s *Server) DeleteSchedule(w http.ResponseWriter, r *http.Request, id apigen.ScheduleIDPath) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	if err := svc.Delete(r.Context(), id); err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PauseSchedule 对应 `POST schedules/{id}/pause`。
func (s *Server) PauseSchedule(w http.ResponseWriter, r *http.Request, id apigen.ScheduleIDPath) {
	s.setScheduleEnabled(w, r, id, false)
}

// ResumeSchedule 对应 `POST schedules/{id}/resume`。
func (s *Server) ResumeSchedule(w http.ResponseWriter, r *http.Request, id apigen.ScheduleIDPath) {
	s.setScheduleEnabled(w, r, id, true)
}

func (s *Server) setScheduleEnabled(w http.ResponseWriter, r *http.Request, id string, enabled bool) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	sc, err := svc.SetEnabled(r.Context(), id, enabled)
	if err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, sc)
}

// RunSchedule 对应 `POST schedules/{id}/run`：手动触发一次，返回该次运行。
func (s *Server) RunSchedule(w http.ResponseWriter, r *http.Request, id apigen.ScheduleIDPath) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	run, err := svc.RunNow(r.Context(), id)
	if err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, run)
}

// ListScheduleRuns 对应 `GET schedules/{id}/runs`（limit ≤100，默认 20，新的在前）。
func (s *Server) ListScheduleRuns(w http.ResponseWriter, r *http.Request, id apigen.ScheduleIDPath, params apigen.ListScheduleRunsParams) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	limit := 20
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 100 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "limit 须在 1..100")
			return
		}
		limit = *params.Limit
	}
	runs, err := svc.Runs(r.Context(), id, limit)
	if err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.ScheduleRunList{Runs: runs})
}

// GetThreadSchedule 对应 `GET threads/{id}/schedule`。
func (s *Server) GetThreadSchedule(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	sc, err := svc.GetByThread(r.Context(), id)
	if err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, sc)
}

// PutThreadSchedule 对应 `PUT threads/{id}/schedule`：设置（新建或替换）线程内定时，隐含 mode = thread。
func (s *Server) PutThreadSchedule(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	var in apigen.ScheduleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	sc, err := svc.PutForThread(r.Context(), id, schedule.InputFromAPI(in), createdByUser)
	if err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, sc)
}

// DeleteThreadSchedule 对应 `DELETE threads/{id}/schedule`。
func (s *Server) DeleteThreadSchedule(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.scheduleSvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteByThread(r.Context(), id); err != nil {
		writeScheduleErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
