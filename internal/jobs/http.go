package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// errProviderUnauthorized 表示 provider 令牌无效或已停用。
var errProviderUnauthorized = errors.New("provider unauthorized")

// authProvider 校验 `Authorization: Bearer opv_…`。
func (q *Queue) authProvider(r *http.Request) (db.Provider, error) {
	v := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(v) <= len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return db.Provider{}, errProviderUnauthorized
	}
	tok := strings.TrimSpace(v[len(prefix):])
	if ids.TokenPrefix(tok) != "opv" {
		return db.Provider{}, errProviderUnauthorized
	}
	p, err := q.st.Q.GetProviderByTokenHash(r.Context(), ids.Hash(tok))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && p.DisabledAt != nil) {
		return db.Provider{}, errProviderUnauthorized
	}
	return p, err
}

// leaseHeldByOther 判断 provider 租约是否被另一个未过期的实例持有（且未双方声明 allowMultiple）。
func (q *Queue) leaseHeldByOther(p db.Provider, instanceID string, allowMultiple bool) bool {
	if p.InstanceID == nil || *p.InstanceID == instanceID || p.LeaseUntil == nil {
		return false
	}
	if *p.LeaseUntil <= q.nowMs() {
		return false
	}
	return !(allowMultiple && p.AllowMultiple)
}

// Hello 处理 POST /provider/v1/hello。
func (q *Queue) Hello(w http.ResponseWriter, r *http.Request) {
	p, ok := q.authOrFail(w, r)
	if !ok {
		return
	}
	var req apigen.ProviderHelloRequest
	if !decode(w, r, &req) {
		return
	}
	if req.InstanceID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "instanceId 必填")
		return
	}
	if q.leaseHeldByOther(p, req.InstanceID, req.AllowMultiple) {
		writeError(w, http.StatusConflict, "provider_already_connected", "另一个 provider 实例持有未过期的租约")
		return
	}
	caps, _ := json.Marshal(req.Capabilities)
	types := req.Types
	if types == nil {
		types = []apigen.OrbType{}
	}
	typesJSON, _ := json.Marshal(types)
	lease := q.nowMs() + ProviderLease.Milliseconds()
	inst := req.InstanceID
	if err := q.st.Q.UpdateProviderHello(r.Context(), db.UpdateProviderHelloParams{
		Driver: string(req.Driver), Version: req.Version, Capabilities: string(caps), OrbTypes: string(typesJSON),
		DefaultType: req.DefaultType, AllowMultiple: req.AllowMultiple, InstanceID: &inst, LeaseUntil: &lease, ID: p.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	slog.Info("provider 上线", "provider", p.Name, "instance", req.InstanceID, "driver", req.Driver, "version", req.Version)
	writeJSON(w, http.StatusOK, apigen.ProviderHelloResponse{ProviderID: p.ID, LeaseSeconds: int(ProviderLease / time.Second)})
}

// renewLease 在认领时续租；租约被别的实例持有时返回 false。
func (q *Queue) renewLease(ctx context.Context, p db.Provider, instanceID string) (bool, error) {
	if q.leaseHeldByOther(p, instanceID, p.AllowMultiple) {
		return false, nil
	}
	lease := q.nowMs() + ProviderLease.Milliseconds()
	inst := instanceID
	return true, q.st.Q.SetProviderLease(ctx, db.SetProviderLeaseParams{InstanceID: &inst, LeaseUntil: &lease, ID: p.ID})
}

// ClaimHTTP 处理 POST /provider/v1/jobs/claim。
func (q *Queue) ClaimHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := q.authOrFail(w, r)
	if !ok {
		return
	}
	var req apigen.ProviderClaimRequest
	if !decode(w, r, &req) {
		return
	}
	if req.InstanceID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "instanceId 必填")
		return
	}
	held, err := q.renewLease(r.Context(), p, req.InstanceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !held {
		writeError(w, http.StatusConflict, "provider_already_connected", "另一个 provider 实例持有未过期的租约")
		return
	}
	job, found, err := q.Claim(r.Context(), p.ID, time.Duration(req.WaitSeconds)*time.Second)
	if err != nil {
		if r.Context().Err() == nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	params := map[string]any{}
	_ = json.Unmarshal([]byte(job.Params), &params)
	writeJSON(w, http.StatusOK, apigen.ProviderClaimResponse{Job: apigen.ProviderJob{
		ID: job.ID, Op: apigen.JobOp(job.Op), OrbID: deref(job.OrbID), Params: params, Attempt: int(job.Attempt),
	}})
}

// CompleteHTTP 处理 POST /provider/v1/jobs/{id}/complete。
func (q *Queue) CompleteHTTP(w http.ResponseWriter, r *http.Request, jobID string) {
	p, ok := q.authOrFail(w, r)
	if !ok {
		return
	}
	var req apigen.ProviderCompleteRequest
	if !decode(w, r, &req) {
		return
	}
	res := Result{OK: req.Ok, Output: req.Output, DurationMs: req.DurationMs}
	if req.Error != nil {
		res.Error = *req.Error
	}
	// 回调可能较慢（写库、入队后续任务）；不随 provider 断开而中止。
	err := q.Complete(context.WithoutCancel(r.Context()), p.ID, jobID, res)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "任务不存在")
	case errors.Is(err, ErrNotClaimed):
		writeError(w, http.StatusConflict, "conflict", "任务不处于已认领状态（可能已因租约过期被重排）")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// Bye 处理 POST /provider/v1/bye：释放该实例的租约。
func (q *Queue) Bye(w http.ResponseWriter, r *http.Request) {
	p, ok := q.authOrFail(w, r)
	if !ok {
		return
	}
	var req struct {
		InstanceID string `json:"instanceId"`
	}
	if !decode(w, r, &req) {
		return
	}
	inst := req.InstanceID
	if err := q.st.Q.ClearProviderLease(r.Context(), db.ClearProviderLeaseParams{ID: p.ID, InstanceID: &inst}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	slog.Info("provider 下线", "provider", p.Name, "instance", req.InstanceID)
	w.WriteHeader(http.StatusNoContent)
}

// Handler 返回独立的 /provider/v1/* 路由（测试或单独挂载用）。
func (q *Queue) Handler() http.Handler {
	r := chi.NewRouter()
	r.Post("/provider/v1/hello", q.Hello)
	r.Post("/provider/v1/jobs/claim", q.ClaimHTTP)
	r.Post("/provider/v1/jobs/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		q.CompleteHTTP(w, r, chi.URLParam(r, "id"))
	})
	r.Post("/provider/v1/bye", q.Bye)
	return r
}

func (q *Queue) authOrFail(w http.ResponseWriter, r *http.Request) (db.Provider, bool) {
	p, err := q.authProvider(r)
	if errors.Is(err, errProviderUnauthorized) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "provider 令牌无效")
		return p, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return p, false
	}
	return p, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "请求体无效: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apigen.Error{Code: code, Message: message})
}
