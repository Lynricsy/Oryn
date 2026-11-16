package api

import (
	"errors"
	"net/http"
	"net/url"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/github"
)

// GitHubDeps 是 GitHub 集成接口（M14）的依赖。
type GitHubDeps struct {
	Service *github.Service
}

func (s *Server) githubSvc(w http.ResponseWriter) (*github.Service, bool) {
	if s.GitHub == nil || s.GitHub.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "github service not configured")
		return nil, false
	}
	return s.GitHub.Service, true
}

// githubStatus 把 *github.Error 映射为 HTTP 状态；不是 *github.Error 时返回 0。
func githubStatus(err error) (int, string, string) {
	var ge *github.Error
	if !errors.As(err, &ge) {
		return 0, "", ""
	}
	if ge.Status != 0 {
		return ge.Status, ge.Code, ge.Message
	}
	switch ge.Code {
	case "conflict", "github_identity_required", "github_agent_required", "github_not_connected", "github_not_bound":
		return http.StatusConflict, ge.Code, ge.Message
	case "invalid_request", "state_invalid":
		return http.StatusBadRequest, ge.Code, ge.Message
	case "not_found":
		return http.StatusNotFound, ge.Code, ge.Message
	}
	return http.StatusBadGateway, "github_upstream", ge.Error()
}

// writeGitHubErr 写出 GitHub 服务的错误：领域错误按码映射，上游故障回 502 github_upstream，其余按通用映射。
func writeGitHubErr(w http.ResponseWriter, r *http.Request, err error) {
	if status, code, msg := githubStatus(err); status != 0 {
		WriteError(w, status, code, msg)
		return
	}
	if github.IsUpstream(err) {
		WriteError(w, http.StatusBadGateway, "github_upstream", err.Error())
		return
	}
	writeErr(w, r, err)
}

// githubErrCode 是公开回调重定向时带回设置页的错误码。
func githubErrCode(err error) string {
	var ge *github.Error
	switch {
	case errors.As(err, &ge):
		return ge.Code
	case github.IsUpstream(err):
		return "github_upstream"
	}
	return "internal"
}

// GetGitHubStatus 对应 `GET /api/v1/github`。
func (s *Server) GetGitHubStatus(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	st, err := svc.Status(r.Context())
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, st)
}

// writeGitHubStatus 在变更成功后回当前状态。
func (s *Server) writeGitHubStatus(w http.ResponseWriter, r *http.Request, svc *github.Service) {
	st, err := svc.Status(r.Context())
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, st)
}

// BeginGitHubAppManifest 对应 `POST /api/v1/github/app/manifest`。
func (s *Server) BeginGitHubAppManifest(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	var req apigen.GitHubManifestRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := svc.BeginManifest(r.Context(), req)
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.GitHubRedirect{URL: u})
}

// DeleteGitHubApp 对应 `DELETE /api/v1/github/app`。
func (s *Server) DeleteGitHubApp(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteApp(r.Context()); err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AuthorizeGitHubIdentity 对应 `POST /api/v1/github/identities/{slot}/authorize`。
func (s *Server) AuthorizeGitHubIdentity(w http.ResponseWriter, r *http.Request, slot apigen.GitHubIdentitySlotPath) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	u, err := svc.BeginAuthorize(r.Context(), string(slot))
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.GitHubRedirect{URL: u})
}

// DeleteGitHubIdentity 对应 `DELETE /api/v1/github/identities/{slot}`。
func (s *Server) DeleteGitHubIdentity(w http.ResponseWriter, r *http.Request, slot apigen.GitHubIdentitySlotPath) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	if err := svc.Disconnect(r.Context(), string(slot)); err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PutGitHubSettings 对应 `PUT /api/v1/github/settings`。
func (s *Server) PutGitHubSettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	var req apigen.GitHubSettings
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := svc.SetAutomationIdentity(r.Context(), string(req.AutomationIdentity)); err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	s.writeGitHubStatus(w, r, svc)
}

// SyncGitHubInstallations 对应 `POST /api/v1/github/installations/sync`。
func (s *Server) SyncGitHubInstallations(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	if err := svc.SyncInstallations(r.Context()); err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	s.writeGitHubStatus(w, r, svc)
}

// ListGitHubRepos 对应 `GET /api/v1/github/repos`：「我的账号」经 App 可访问的仓库。
func (s *Server) ListGitHubRepos(w http.ResponseWriter, r *http.Request, params apigen.ListGitHubReposParams) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	q := ""
	if params.Q != nil {
		q = *params.Q
	}
	if utf8.RuneCountInString(q) > 100 {
		WriteError(w, http.StatusBadRequest, "invalid_request", "q 不得超过 100 个字符")
		return
	}
	repos, err := svc.ListRepos(r.Context(), q)
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.GitHubRepoList{Repos: repos})
}

// ListGitHubRefs 对应 `GET /api/v1/projects/{id}/github/refs`：输入框 `#` 引用的 issue / PR 候选。
func (s *Server) ListGitHubRefs(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, params apigen.ListGitHubRefsParams) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	q := ""
	if params.Q != nil {
		q = *params.Q
	}
	if utf8.RuneCountInString(q) > 100 {
		WriteError(w, http.StatusBadRequest, "invalid_request", "q 不得超过 100 个字符")
		return
	}
	refs, err := svc.ListRefs(r.Context(), id, q)
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.GitHubRefList{Refs: refs})
}

// AttachGitHubRef 对应 `POST /api/v1/projects/{id}/github/refs/{number}/attachment`：把 issue / PR 渲染成 Markdown 附件。
func (s *Server) AttachGitHubRef(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, number apigen.GitHubNumberPath) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	a, err := svc.AttachRef(r.Context(), id, int64(number))
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, attachmentAPI(a))
}

// StartGitHubManifest 对应公开的 `GET /api/v1/public/github/manifest/start`：返回提交 manifest 的独立页面。
func (s *Server) StartGitHubManifest(w http.ResponseWriter, r *http.Request, params apigen.StartGitHubManifestParams) {
	if !s.rateLimit(w, r, auth.LimitLogin) {
		return
	}
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	page, err := svc.ManifestStartPage(r.Context(), params.State)
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action "+s.Config.GitHub.WebURL+
		"; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(page))
}

// githubSettingsRedirect 302 回设置页的 GitHub 分区，query 为 key=value。
func (s *Server) githubSettingsRedirect(w http.ResponseWriter, r *http.Request, key, value string) {
	http.Redirect(w, r, s.Config.PublicURL+"/settings/github?"+url.Values{key: {value}}.Encode(), http.StatusFound)
}

// FinishGitHubManifest 对应公开的 `GET /api/v1/public/github/manifest/callback`。
func (s *Server) FinishGitHubManifest(w http.ResponseWriter, r *http.Request, params apigen.FinishGitHubManifestParams) {
	if !s.rateLimit(w, r, auth.LimitLogin) {
		return
	}
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	if err := svc.FinishManifest(r.Context(), params.Code, params.State); err != nil {
		s.githubSettingsRedirect(w, r, "error", githubErrCode(err))
		return
	}
	s.githubSettingsRedirect(w, r, "created", "1")
}

// FinishGitHubOAuth 对应公开的 `GET /api/v1/public/github/oauth/callback`。
func (s *Server) FinishGitHubOAuth(w http.ResponseWriter, r *http.Request, params apigen.FinishGitHubOAuthParams) {
	if !s.rateLimit(w, r, auth.LimitLogin) {
		return
	}
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	if params.Error != nil && *params.Error != "" {
		s.githubSettingsRedirect(w, r, "error", "access_denied")
		return
	}
	code, state := "", ""
	if params.Code != nil {
		code = *params.Code
	}
	if params.State != nil {
		state = *params.State
	}
	slot, err := svc.FinishAuthorize(r.Context(), code, state)
	if err != nil {
		s.githubSettingsRedirect(w, r, "error", githubErrCode(err))
		return
	}
	s.githubSettingsRedirect(w, r, "connected", slot)
}

// ReceiveGitHubWebhook 对应公开的 `POST /api/v1/public/github/webhook`：校验签名后受理投递，无响应体。
func (s *Server) ReceiveGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	if r.ContentLength > github.WebhookMaxBody {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	body, err := readBody(r, github.WebhookMaxBody)
	if err != nil {
		if errors.Is(err, agentcfg.ErrTooLarge) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
		return
	}
	w.WriteHeader(svc.HandleWebhook(r.Context(), r.Header, body))
}

// ListGitHubTriggers 对应 `GET /api/v1/projects/{id}/github/triggers`。
func (s *Server) ListGitHubTriggers(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	list, err := svc.ListTriggers(r.Context(), id)
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.GitHubTriggerList{Triggers: list})
}

// CreateGitHubTrigger 对应 `POST /api/v1/projects/{id}/github/triggers`。
func (s *Server) CreateGitHubTrigger(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	var req apigen.GitHubTriggerInput
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := svc.CreateTrigger(r.Context(), id, req)
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, t)
}

// UpdateGitHubTrigger 对应 `PATCH /api/v1/github/triggers/{id}`。
func (s *Server) UpdateGitHubTrigger(w http.ResponseWriter, r *http.Request, id apigen.GitHubTriggerIDPath) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	var req apigen.GitHubTriggerPatch
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := svc.UpdateTrigger(r.Context(), id, req)
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, t)
}

// DeleteGitHubTrigger 对应 `DELETE /api/v1/github/triggers/{id}`。
func (s *Server) DeleteGitHubTrigger(w http.ResponseWriter, r *http.Request, id apigen.GitHubTriggerIDPath) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteTrigger(r.Context(), id); err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
