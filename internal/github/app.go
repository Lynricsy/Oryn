package github

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 公开回调路由（固定字面量）。
const (
	webhookPath          = "/api/v1/public/github/webhook"
	manifestStartPath    = "/api/v1/public/github/manifest/start"
	manifestCallbackPath = "/api/v1/public/github/manifest/callback"
	oauthCallbackPath    = "/api/v1/public/github/oauth/callback"
)

var orgRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)

// defaultWebhookURL 是默认的 webhook 地址。
func (s *Service) defaultWebhookURL() string { return s.cfg.PublicURL + webhookPath }

// appManifest 是提交给 GitHub 的 App manifest（字段顺序即 JSON 顺序）。
type appManifest struct {
	Name                  string            `json:"name"`
	URL                   string            `json:"url"`
	HookAttributes        hookAttributes    `json:"hook_attributes"`
	RedirectURL           string            `json:"redirect_url"`
	CallbackURLs          []string          `json:"callback_urls"`
	SetupURL              string            `json:"setup_url"`
	SetupOnUpdate         bool              `json:"setup_on_update"`
	RequestOAuthOnInstall bool              `json:"request_oauth_on_install"`
	Public                bool              `json:"public"`
	DefaultPermissions    map[string]string `json:"default_permissions"`
	DefaultEvents         []string          `json:"default_events"`
}

type hookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// Status 汇总 GitHub 集成的当前状态（`GET /api/v1/github`）。
func (s *Service) Status(ctx context.Context) (apigen.GitHubStatus, error) {
	out := apigen.GitHubStatus{
		AutomationIdentity: apigen.GitHubIdentitySlot(slotUser),
		Installations:      []apigen.GitHubInstallation{},
		WebhookURL:         s.defaultWebhookURL(),
	}
	app, err := s.st.Q.GetGithubApp(ctx)
	switch {
	case err == nil:
		out.App = &apigen.GitHubApp{
			AppID: app.AppID, Slug: app.Slug, Name: app.Name, HTMLURL: app.HtmlUrl, OwnerLogin: app.OwnerLogin,
			Public: app.Public, InstallURL: app.HtmlUrl + "/installations/new",
		}
		out.WebhookURL = app.WebhookUrl
		out.LastDeliveryAt = app.LastDeliveryAt
		if app.LastDeliverySrc != nil {
			src := apigen.GitHubStatusLastDeliverySource(*app.LastDeliverySrc)
			out.LastDeliverySource = &src
		}
	case !isNoRows(err):
		return out, err
	}
	idents, err := s.st.Q.ListGithubIdentities(ctx)
	if err != nil {
		return out, err
	}
	for _, gi := range idents {
		v := &apigen.GitHubIdentity{
			Login: gi.Login, GithubID: gi.GithubID, Name: gi.Name, AvatarURL: gi.AvatarUrl,
			Status: apigen.GitHubIdentityStatus(gi.Status), ConnectedAt: gi.ConnectedAt,
		}
		switch gi.Slot {
		case slotUser:
			out.Identities.User = v
		case slotAgent:
			out.Identities.Agent = v
		}
	}
	insts, err := s.st.Q.ListGithubInstallations(ctx)
	if err != nil {
		return out, err
	}
	for _, in := range insts {
		out.Installations = append(out.Installations, apigen.GitHubInstallation{
			ID: in.ID, AccountLogin: in.AccountLogin, AccountType: in.AccountType,
			RepositorySelection: in.RepositorySelection, Suspended: in.Suspended,
		})
	}
	if v, err := s.st.Q.GetOwnerGithubAutomation(ctx); err == nil {
		out.AutomationIdentity = apigen.GitHubIdentitySlot(v)
	} else if !isNoRows(err) {
		return out, err
	}
	return out, nil
}

// BeginManifest 开始按 manifest 创建 App：记一次性 state，返回独立的跳转页地址（前端 CSP 不允许直接提交外部表单）。
func (s *Service) BeginManifest(ctx context.Context, in apigen.GitHubManifestRequest) (string, error) {
	if _, err := s.st.Q.GetGithubApp(ctx); err == nil {
		return "", errf("conflict", "GitHub App already created")
	} else if !isNoRows(err) {
		return "", err
	}
	org := ""
	if in.Org != nil {
		org = strings.TrimSpace(*in.Org)
		if org != "" && !orgRe.MatchString(org) {
			return "", errf("invalid_request", "invalid organization name")
		}
	}
	hook := s.defaultWebhookURL()
	if in.WebhookURL != nil && strings.TrimSpace(*in.WebhookURL) != "" {
		hook = strings.TrimSpace(*in.WebhookURL)
		u, err := url.Parse(hook)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return "", errf("invalid_request", "webhook URL must be an https URL")
		}
	}
	pub, err := url.Parse(s.cfg.PublicURL)
	if err != nil {
		return "", err
	}
	name := "Oryn " + pub.Hostname()
	if r := []rune(name); len(r) > 34 {
		name = string(r[:34])
	}
	m, err := json.Marshal(appManifest{
		Name:           name,
		URL:            s.cfg.PublicURL,
		HookAttributes: hookAttributes{URL: hook, Active: true},
		RedirectURL:    s.cfg.PublicURL + manifestCallbackPath,
		CallbackURLs:   []string{s.cfg.PublicURL + oauthCallbackPath},
		SetupURL:       s.cfg.PublicURL + "/settings/github",
		SetupOnUpdate:  true,
		Public:         in.Public,
		DefaultPermissions: map[string]string{
			"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read", "statuses": "read",
			"actions": "read", "metadata": "read",
		},
		DefaultEvents: []string{
			"pull_request", "pull_request_review", "pull_request_review_comment", "issue_comment", "issues",
			"check_suite", "status", "repository",
		},
	})
	if err != nil {
		return "", err
	}
	state := ids.Token("ogs")
	action := s.cfg.WebURL + "/settings/apps/new?state=" + url.QueryEscape(state)
	if org != "" {
		action = s.cfg.WebURL + "/organizations/" + url.PathEscape(org) + "/settings/apps/new?state=" + url.QueryEscape(state)
	}
	manifest := string(m)
	if err := s.st.Q.InsertGithubState(ctx, db.InsertGithubStateParams{
		StateHash: ids.Hash(state), Kind: "manifest", Manifest: &manifest, ActionUrl: &action, Public: in.Public,
		WebhookUrl: &hook, ExpiresAt: s.now().Add(stateTTL).UnixMilli(),
	}); err != nil {
		return "", err
	}
	return s.cfg.PublicURL + manifestStartPath + "?state=" + url.QueryEscape(state), nil
}

// liveState 读一条未过期、种类为 kind 的 state（不消费）；不符一律 notFoundCode。
func (s *Service) liveState(ctx context.Context, state, kind, notFoundCode string) (db.GithubOauthState, error) {
	row, err := s.st.Q.GetGithubState(ctx, ids.Hash(state))
	if err != nil {
		if isNoRows(err) {
			return row, errf(notFoundCode, "state not found")
		}
		return row, err
	}
	if row.Kind != kind || row.ExpiresAt < s.nowMs() {
		return row, errf(notFoundCode, "state not found")
	}
	return row, nil
}

// consumeState 读并消费一条 state：种类不符时不消费；已被消费或已过期 → state_invalid。
func (s *Service) consumeState(ctx context.Context, state, kind string) (db.GithubOauthState, error) {
	row, err := s.st.Q.GetGithubState(ctx, ids.Hash(state))
	if err != nil {
		if isNoRows(err) {
			return row, errf("state_invalid", "state not found")
		}
		return row, err
	}
	if row.Kind != kind {
		return row, errf("state_invalid", "state kind mismatch")
	}
	n, err := s.st.Q.DeleteGithubState(ctx, row.StateHash)
	if err != nil {
		return row, err
	}
	if n == 0 || row.ExpiresAt < s.nowMs() {
		return row, errf("state_invalid", "state expired or already used")
	}
	return row, nil
}

// ManifestStartPage 返回自动提交 manifest 的独立页面（不含脚本：由用户点按钮提交）。
func (s *Service) ManifestStartPage(ctx context.Context, state string) (string, error) {
	row, err := s.liveState(ctx, state, "manifest", "not_found")
	if err != nil {
		return "", err
	}
	action, manifest := "", ""
	if row.ActionUrl != nil {
		action = *row.ActionUrl
	}
	if row.Manifest != nil {
		manifest = *row.Manifest
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1"><title>创建 GitHub App</title>`)
	b.WriteString(`<style>body{font-family:system-ui,sans-serif;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}`)
	b.WriteString(`button{font-size:16px;padding:10px 18px;border-radius:8px;border:1px solid #888;cursor:pointer}</style></head><body>`)
	b.WriteString(`<form method="post" action="` + html.EscapeString(action) + `">`)
	b.WriteString(`<input type="hidden" name="manifest" value="` + html.EscapeString(manifest) + `">`)
	b.WriteString(`<button type="submit">继续前往 GitHub 创建 App</button></form></body></html>`)
	return b.String(), nil
}

// FinishManifest 处理 manifest 回调：消费 state，兑换 App 凭据并落库。
func (s *Service) FinishManifest(ctx context.Context, code, state string) error {
	row, err := s.consumeState(ctx, state, "manifest")
	if err != nil {
		return err
	}
	if _, err := s.st.Q.GetGithubApp(ctx); err == nil {
		return errf("conflict", "GitHub App already created")
	} else if !isNoRows(err) {
		return err
	}
	var conv struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		Name          string `json:"name"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		WebhookSecret string `json:"webhook_secret"`
		PEM           string `json:"pem"`
		HTMLURL       string `json:"html_url"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := s.call(ctx, http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &conv); err != nil {
		return err
	}
	box := s.cfg.Box
	keyEnc, err := box.Seal(aadPrivateKey, []byte(conv.PEM))
	if err != nil {
		return err
	}
	secretEnc, err := box.Seal(aadClientSecret, []byte(conv.ClientSecret))
	if err != nil {
		return err
	}
	hookEnc, err := box.Seal(aadWebhookSecret, []byte(conv.WebhookSecret))
	if err != nil {
		return err
	}
	hook := s.defaultWebhookURL()
	if row.WebhookUrl != nil {
		hook = *row.WebhookUrl
	}
	if err := s.st.Q.InsertGithubApp(ctx, db.InsertGithubAppParams{
		AppID: conv.ID, Slug: conv.Slug, Name: conv.Name, ClientID: conv.ClientID, OwnerLogin: conv.Owner.Login,
		HtmlUrl: conv.HTMLURL, Public: row.Public, WebhookUrl: hook, PrivateKeyEnc: keyEnc, ClientSecretEnc: secretEnc,
		WebhookSecretEnc: hookEnc, CreatedAt: s.nowMs(),
	}); err != nil {
		if isUnique(err) {
			return errf("conflict", "GitHub App already created")
		}
		return err
	}
	s.clearTokenCaches()
	s.changed()
	return nil
}

// DeleteApp 删除 App 连接及依附于它的身份、安装与项目安装绑定（PR 记录、关联与触发规则保留）。
func (s *Service) DeleteApp(ctx context.Context) error {
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.DeleteGithubApp(ctx); err != nil {
			return err
		}
		if err := q.DeleteGithubIdentities(ctx); err != nil {
			return err
		}
		if err := q.DeleteGithubInstallations(ctx); err != nil {
			return err
		}
		if err := q.ClearAllProjectGithubInstallations(ctx); err != nil {
			return err
		}
		_, err := q.SetOwnerGithubAutomation(ctx, slotUser)
		return err
	})
	if err != nil {
		return err
	}
	s.clearTokenCaches()
	s.changed()
	return nil
}
