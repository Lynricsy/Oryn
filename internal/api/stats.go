package api

import (
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/stats"
)

// GetUsageStats 实现 GET /api/v1/stats/usage。
func (s *Server) GetUsageStats(w http.ResponseWriter, r *http.Request, params apigen.GetUsageStatsParams) {
	switch params.GroupBy {
	case apigen.GetUsageStatsParamsGroupByDay, apigen.GetUsageStatsParamsGroupByModel,
		apigen.GetUsageStatsParamsGroupByThread, apigen.GetUsageStatsParamsGroupByProject:
	default:
		WriteError(w, http.StatusBadRequest, "invalid_request", "groupBy 必须是 day / model / thread / project")
		return
	}
	from, to := stats.Window(params.From, params.To, time.Now())
	u, err := stats.Usage(r.Context(), s.Store.Q, string(params.GroupBy), from, to)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, u)
}

// GetOrbStats 实现 GET /api/v1/stats/orbs。
func (s *Server) GetOrbStats(w http.ResponseWriter, r *http.Request, params apigen.GetOrbStatsParams) {
	from, to := stats.Window(params.From, params.To, time.Now())
	o, err := stats.Orbs(r.Context(), s.Store.Q, from, to)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, o)
}
