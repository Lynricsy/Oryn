package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

var providerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// providerJSON 组装客户端视图；online = 有实例持有未过期租约且未停用。
func providerJSON(p db.Provider, now int64) apigen.Provider {
	out := apigen.Provider{
		ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, DisabledAt: p.DisabledAt,
		InstanceID: p.InstanceID, LeaseUntil: p.LeaseUntil, OrbTypes: []apigen.OrbType{},
		Online: p.DisabledAt == nil && p.InstanceID != nil && p.LeaseUntil != nil && *p.LeaseUntil > now,
	}
	if p.Driver != "" {
		out.Driver = new(apigen.ProviderDriver(p.Driver))
		var caps apigen.ProviderCapabilities
		if json.Unmarshal([]byte(p.Capabilities), &caps) == nil {
			out.Capabilities = &caps
		}
	}
	if p.DefaultType != "" {
		out.DefaultType = new(p.DefaultType)
	}
	var types []apigen.OrbType
	if json.Unmarshal([]byte(p.OrbTypes), &types) == nil && types != nil {
		out.OrbTypes = types
	}
	return out
}

// ListProviders 列出 provider（含已停用）。
func (s *Server) ListProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.Q.ListProviders(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	now := time.Now().UnixMilli()
	out := apigen.ProviderList{Providers: make([]apigen.Provider, 0, len(rows))}
	for _, p := range rows {
		out.Providers = append(out.Providers, providerJSON(p, now))
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateProvider 新建 provider；opv_ 令牌只在本响应中出现。
func (s *Server) CreateProvider(w http.ResponseWriter, r *http.Request) {
	var req apigen.ProviderCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !providerNameRe.MatchString(req.Name) {
		WriteError(w, http.StatusBadRequest, "invalid_request", "provider 名须匹配 ^[a-z0-9][a-z0-9_-]*$，至多 64 字符")
		return
	}
	ctx := r.Context()
	tok := ids.Token("opv")
	id := ids.New("prov")
	err := s.Store.Q.CreateProvider(ctx, db.CreateProviderParams{ID: id, Name: req.Name, TokenHash: ids.Hash(tok), CreatedAt: time.Now().UnixMilli()})
	if isUniqueViolation(err) {
		WriteError(w, http.StatusConflict, "conflict", "已有同名 provider")
		return
	} else if err != nil {
		writeErr(w, r, err)
		return
	}
	p, err := s.Store.Q.GetProvider(ctx, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, apigen.ProviderCreateResponse{Provider: providerJSON(p, time.Now().UnixMilli()), Token: tok})
}

// DeleteProvider 删除 provider；仍被 orb / 任务引用时改为停用（令牌立即失效，名称保留）。
func (s *Server) DeleteProvider(w http.ResponseWriter, r *http.Request, id apigen.ProviderIDPath) {
	ctx := r.Context()
	if _, err := s.Store.Q.GetProvider(ctx, id); errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "provider 不存在")
		return
	} else if err != nil {
		writeErr(w, r, err)
		return
	}
	_, err := s.Store.Q.DeleteProvider(ctx, id)
	if isForeignKeyViolation(err) {
		_, err = s.Store.Q.DisableProvider(ctx, db.DisableProviderParams{DisabledAt: new(time.Now().UnixMilli()), ID: id})
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
