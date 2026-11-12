package api

import (
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/stats"
)

// ListOrbs 实现 GET /api/v1/orbs。
func (s *Server) ListOrbs(w http.ResponseWriter, r *http.Request, params apigen.ListOrbsParams) {
	ctx := r.Context()
	rows, err := s.Store.Q.ListOrbs(ctx, params.IncludeDeleted)
	if err != nil {
		writeOrbErr(w, err)
		return
	}
	out := apigen.OrbList{Orbs: make([]apigen.Orb, 0, len(rows))}
	for _, o := range rows {
		if params.State != nil && o.State != string(*params.State) {
			continue
		}
		v, err := orbs.View(ctx, s.Store.Q, o)
		if err != nil {
			writeOrbErr(w, err)
			return
		}
		out.Orbs = append(out.Orbs, v)
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetOrb 实现 GET /api/v1/orbs/{id}。
func (s *Server) GetOrb(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	s.writeOrb(w, r, id, http.StatusOK)
}

func (s *Server) writeOrb(w http.ResponseWriter, r *http.Request, id string, status int) {
	svc, ok := s.orbsSvc(w)
	if !ok {
		return
	}
	o, err := svc.Get(r.Context(), id)
	if err != nil {
		writeOrbErr(w, err)
		return
	}
	v, err := orbs.View(r.Context(), s.Store.Q, o)
	if err != nil {
		writeOrbErr(w, err)
		return
	}
	WriteJSON(w, status, v)
}

// PauseOrb 实现 POST /api/v1/orbs/{id}/pause。
func (s *Server) PauseOrb(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	svc, ok := s.orbsSvc(w)
	if !ok {
		return
	}
	var req apigen.OrbPauseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Mode != apigen.Freeze && req.Mode != apigen.Stop {
		WriteError(w, http.StatusBadRequest, "invalid_request", "mode 必须是 freeze 或 stop")
		return
	}
	if err := svc.Pause(r.Context(), id, string(req.Mode)); err != nil {
		writeOrbErr(w, err)
		return
	}
	s.writeOrb(w, r, id, http.StatusAccepted)
}

// ResumeOrb 实现 POST /api/v1/orbs/{id}/resume。
func (s *Server) ResumeOrb(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	svc, ok := s.orbsSvc(w)
	if !ok {
		return
	}
	svc.TouchUser(r.Context(), id)
	if err := svc.Wake(r.Context(), id); err != nil {
		writeOrbErr(w, err)
		return
	}
	s.writeOrb(w, r, id, http.StatusAccepted)
}

// DeleteOrb 实现 DELETE /api/v1/orbs/{id}。
func (s *Server) DeleteOrb(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	svc, ok := s.orbsSvc(w)
	if !ok {
		return
	}
	if err := svc.Delete(r.Context(), id); err != nil {
		writeOrbErr(w, err)
		return
	}
	s.writeOrb(w, r, id, http.StatusAccepted)
}

// GetOrbMetrics 实现 GET /api/v1/orbs/{id}/metrics。
func (s *Server) GetOrbMetrics(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.GetOrbMetricsParams) {
	ctx := r.Context()
	if _, err := s.Store.Q.GetOrb(ctx, id); err != nil {
		writeOrbErr(w, orbs.ErrNotFound)
		return
	}
	step := 0
	if params.Step != nil {
		if *params.Step < 0 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "step 不能为负")
			return
		}
		step = *params.Step
	}
	from, to := stats.Window(params.From, params.To, time.Now())
	m, err := stats.Metrics(ctx, s.Store.Q, id, from, to, step)
	if err != nil {
		writeOrbErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, m)
}

// ProviderHello 实现 POST /provider/v1/hello（provider 自有 Bearer opv_…，由 jobs 校验）。
func (s *Server) ProviderHello(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.orbsSvc(w); ok {
		svc.Jobs().Hello(w, r)
	}
}

// ProviderClaimJob 实现 POST /provider/v1/jobs/claim。
func (s *Server) ProviderClaimJob(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.orbsSvc(w); ok {
		svc.Jobs().ClaimHTTP(w, r)
	}
}

// ProviderCompleteJob 实现 POST /provider/v1/jobs/{id}/complete。
func (s *Server) ProviderCompleteJob(w http.ResponseWriter, r *http.Request, id apigen.JobID) {
	if svc, ok := s.orbsSvc(w); ok {
		svc.Jobs().CompleteHTTP(w, r, id)
	}
}

// ProviderBye 实现 POST /provider/v1/bye。
func (s *Server) ProviderBye(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.orbsSvc(w); ok {
		svc.Jobs().Bye(w, r)
	}
}
