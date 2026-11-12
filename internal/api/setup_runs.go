package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ListOrbSetupRuns 实现 GET /api/v1/orbs/{id}/setup-runs：最近 20 次 setup / resume 运行（新到旧，不含日志）。
func (s *Server) ListOrbSetupRuns(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	ctx := r.Context()
	if _, err := s.Store.Q.GetOrb(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = orbs.ErrNotFound
		}
		writeOrbErr(w, err)
		return
	}
	rows, err := s.Store.Q.ListOrbSetupRuns(ctx, id)
	if err != nil {
		writeOrbErr(w, err)
		return
	}
	out := apigen.OrbSetupRunList{Runs: make([]apigen.OrbSetupRun, 0, len(rows))}
	for _, row := range rows {
		out.Runs = append(out.Runs, orbs.SetupRunView(row))
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetOrbSetupRunLog 实现 GET /api/v1/orbs/{id}/setup-runs/{runId}/log：该次运行的输出末尾（text/plain）。
// 运行记录不属于该 orb 时与不存在同样返回 404。
func (s *Server) GetOrbSetupRunLog(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, runID apigen.SetupRunID) {
	tail, err := s.Store.Q.GetOrbSetupRunLog(r.Context(), db.GetOrbSetupRunLogParams{ID: runID, OrbID: id})
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "setup 运行记录不存在")
		return
	}
	if err != nil {
		writeOrbErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(tail)
}
