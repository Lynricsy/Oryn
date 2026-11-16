package github

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// errNotConfigured 是既没有 OAuth App、也没有旧 App 时的错误。
func errNotConfigured() *Error {
	return &Error{Status: http.StatusConflict, Code: "github_not_configured", Message: "GitHub OAuth App not configured"}
}

// oauthClient 返回 OAuth 授权流用的 client 凭据：优先取 github_oauth_app；
// 没有时退回旧 App 的 client 凭据（M14 过渡，步骤 1.4 删除）。都没有时返回 github_not_configured（409）。
func (s *Service) oauthClient(ctx context.Context) (id, secret string, err error) {
	row, err := s.st.Q.GetGithubOAuthApp(ctx)
	switch {
	case err == nil:
		b, err := s.cfg.Box.Open(aadOAuthSecret, row.ClientSecretEnc)
		return row.ClientID, string(b), err
	case !isNoRows(err):
		return "", "", err
	}
	app, err := s.st.Q.GetGithubApp(ctx)
	if err != nil {
		if isNoRows(err) {
			return "", "", errNotConfigured()
		}
		return "", "", err
	}
	secret, err = s.clientSecret(app)
	return app.ClientID, secret, err
}

// SetOAuthApp 保存 OAuth App 的 Client ID / Secret 与 webhook 基址（R1）。
// secret 为空时沿用已存的值（首次保存必填）；webhookBase 为空表示取 public_url。
// 生效的 webhook 基址变化时（含首次保存）调用 ResyncHooks，全量改写各仓库 hook 的 config.url；同步失败只记日志。
func (s *Service) SetOAuthApp(ctx context.Context, clientID, secret, webhookBase string) error {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || strings.ContainsAny(clientID, " \t\r\n/?#&") {
		return errf("invalid_request", "clientId is required")
	}
	var base *string
	if v := strings.TrimRight(strings.TrimSpace(webhookBase), "/"); v != "" {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return errf("invalid_request", "webhookBase must be an http(s) URL without query or fragment")
		}
		base = &v
	}
	var secretEnc []byte
	if secret = strings.TrimSpace(secret); secret != "" {
		enc, err := s.cfg.Box.Seal(aadOAuthSecret, []byte(secret))
		if err != nil {
			return err
		}
		secretEnc = enc
	} else {
		cur, err := s.st.Q.GetGithubOAuthApp(ctx)
		if err != nil {
			if isNoRows(err) {
				return errf("invalid_request", "clientSecret is required on first save")
			}
			return err
		}
		secretEnc = cur.ClientSecretEnc
	}
	prevBase, _, err := s.webhookBase(ctx)
	if err != nil {
		return err
	}
	if err := s.st.Q.UpsertGithubOAuthApp(ctx, db.UpsertGithubOAuthAppParams{
		ClientID: clientID, ClientSecretEnc: secretEnc, WebhookBase: base, CreatedAt: s.nowMs(),
	}); err != nil {
		return err
	}
	s.changed()
	if nextBase, _, err := s.webhookBase(ctx); err != nil || nextBase != prevBase {
		if err := errors.Join(err, s.ResyncHooks(ctx)); err != nil {
			slog.Warn("github: webhook 基址变更后同步仓库 hook 失败", "err", err)
		}
	}
	return nil
}

// DeleteOAuthApp 删除 OAuth App 配置（R1）：先删除 Oryn 建的全部仓库 hook，再断开两个身份并删掉配置行。
func (s *Service) DeleteOAuthApp(ctx context.Context) error {
	if err := s.dropAllHooks(ctx); err != nil {
		return err
	}
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.DeleteGithubIdentities(ctx); err != nil {
			return err
		}
		return q.DeleteGithubOAuthApp(ctx)
	})
	if err != nil {
		return err
	}
	s.clearTokenCaches()
	s.changed()
	return nil
}

// fillOAuthStatus 往状态里填 OAuth App 配置与 webhook 基址（缺省 = public_url）。
func (s *Service) fillOAuthStatus(ctx context.Context, out *apigen.GitHubStatus) error {
	base := s.cfg.PublicURL
	row, err := s.st.Q.GetGithubOAuthApp(ctx)
	switch {
	case err == nil:
		out.OauthApp = &apigen.GitHubOAuthApp{ClientID: row.ClientID, CallbackURL: s.cfg.PublicURL + oauthCallbackPath}
		if row.WebhookBase != nil {
			base = *row.WebhookBase
		}
	case !isNoRows(err):
		return err
	}
	out.WebhookBase = &base
	return nil
}

// scopeImpliedBy 列出包含某个所需 scope 的更宽 scope：X-OAuth-Scopes 只列出授予的顶层项。
var scopeImpliedBy = map[string][]string{
	"read:org":   {"write:org", "admin:org"},
	"user:email": {"user"},
}

// parseScopes 解析 scope 列表：兼容 X-OAuth-Scopes 的 `a, b, c` 与存储用的空格分隔；去重保序。
func parseScopes(v string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// joinScopes 把 scope 列表存成空格分隔（与 oauthScopes 同形）。
func joinScopes(scopes []string) string { return strings.Join(scopes, " ") }

// missingScopes 返回 oauthScopes 中未被 granted 覆盖的项；全部覆盖时返回 nil。
func missingScopes(granted []string) []string {
	var out []string
	for _, need := range strings.Fields(oauthScopes) {
		if slices.Contains(granted, need) || slices.ContainsFunc(scopeImpliedBy[need], func(w string) bool { return slices.Contains(granted, w) }) {
			continue
		}
		out = append(out, need)
	}
	return out
}
