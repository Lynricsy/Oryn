package github

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func validSlot(slot string) bool { return slot == slotUser || slot == slotAgent }

func otherSlot(slot string) string {
	if slot == slotUser {
		return slotAgent
	}
	return slotUser
}

// BeginAuthorize 开始连接一个身份槽位：返回 GitHub 的授权地址（可在另一个浏览器里打开），scope 恰为 oauthScopes。
func (s *Service) BeginAuthorize(ctx context.Context, slot string) (string, error) {
	if !validSlot(slot) {
		return "", errf("invalid_request", "slot must be user or agent")
	}
	clientID, _, err := s.oauthClient(ctx)
	if err != nil {
		return "", err
	}
	state := ids.Token("ogs")
	if err := s.st.Q.InsertGithubState(ctx, db.InsertGithubStateParams{
		StateHash: ids.Hash(state), Kind: "identity", Slot: &slot, ExpiresAt: s.now().Add(stateTTL).UnixMilli(),
	}); err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", s.cfg.PublicURL+oauthCallbackPath)
	q.Set("scope", oauthScopes)
	q.Set("state", state)
	q.Set("prompt", "select_account")
	return s.cfg.WebURL + "/login/oauth/authorize?" + q.Encode(), nil
}

// clientSecret 解出 App 的 client secret。
func (s *Service) clientSecret(app db.GithubApp) (string, error) {
	b, err := s.cfg.Box.Open(aadClientSecret, app.ClientSecretEnc)
	return string(b), err
}

// FinishAuthorize 处理 OAuth 回调：消费 state，兑换用户令牌，查出账号与实际授予的 scope 并写进槽位。
func (s *Service) FinishAuthorize(ctx context.Context, code, state string) (string, error) {
	row, err := s.consumeState(ctx, state, "identity")
	if err != nil {
		return "", err
	}
	if row.Slot == nil || !validSlot(*row.Slot) {
		return "", errf("state_invalid", "state has no slot")
	}
	slot := *row.Slot
	clientID, secret, err := s.oauthClient(ctx)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	form.Set("code", code)
	form.Set("redirect_uri", s.cfg.PublicURL+oauthCallbackPath)
	tok, err := s.oauthToken(ctx, form)
	if err != nil {
		return "", err
	}
	if tok.Error != "" || tok.AccessToken == "" {
		return "", errf("exchange_failed", tok.Error)
	}
	var user struct {
		ID        int64   `json:"id"`
		Login     string  `json:"login"`
		Name      *string `json:"name"`
		AvatarURL string  `json:"avatar_url"`
	}
	h, err := s.request(ctx, http.MethodGet, "/user", "Bearer "+tok.AccessToken, nil, &user)
	if err != nil {
		return "", err
	}
	scopes := joinScopes(parseScopes(h.Get("X-OAuth-Scopes")))
	if other, err := s.st.Q.GetGithubIdentity(ctx, otherSlot(slot)); err == nil && other.GithubID == user.ID {
		return "", errf("same_account", "the other slot is already connected to this GitHub account")
	} else if err != nil && !isNoRows(err) {
		return "", err
	}
	accessEnc, err := s.cfg.Box.Seal(aadAccess(slot), []byte(tok.AccessToken))
	if err != nil {
		return "", err
	}
	name := user.Name
	if name != nil && *name == "" {
		name = nil
	}
	now := s.nowMs()
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpsertGithubIdentity(ctx, db.UpsertGithubIdentityParams{
			Slot: slot, GithubID: user.ID, Login: user.Login, Name: name, AvatarUrl: user.AvatarURL,
			AccessTokenEnc: accessEnc, ConnectedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		return q.SetGithubIdentityScopes(ctx, db.SetGithubIdentityScopesParams{Scopes: scopes, UpdatedAt: now, Slot: slot})
	})
	if err != nil {
		if isUnique(err) {
			return "", errf("same_account", "the other slot is already connected to this GitHub account")
		}
		return "", err
	}
	s.changed()
	return slot, nil
}

// tokenFor 返回槽位的用户访问令牌（OAuth App 的令牌不过期，直接解密）；未连接或待重新授权时返回 github_identity_required。
func (s *Service) tokenFor(ctx context.Context, slot string) (string, error) {
	if !validSlot(slot) {
		return "", errf("invalid_request", "slot must be user or agent")
	}
	row, err := s.st.Q.GetGithubIdentity(ctx, slot)
	if err != nil {
		if isNoRows(err) {
			return "", errf("github_identity_required", "GitHub identity "+slot+" is not connected")
		}
		return "", err
	}
	if row.Status == "reauth" {
		return "", errf("github_identity_required", "GitHub identity "+slot+" needs to be re-authorized")
	}
	b, err := s.cfg.Box.Open(aadAccess(slot), row.AccessTokenEnc)
	return string(b), err
}

// markReauth 把槽位标为需要重新授权（上游对该槽位的令牌回 401）。
func (s *Service) markReauth(ctx context.Context, slot string) error {
	if err := s.st.Q.SetGithubIdentityStatus(ctx, db.SetGithubIdentityStatusParams{Status: "reauth", UpdatedAt: s.nowMs(), Slot: slot}); err != nil {
		return err
	}
	s.changed()
	return nil
}

// identityView 把身份行转成 API 视图：scopes 取自授权时的 X-OAuth-Scopes，missingScopes = oauthScopes − scopes。
func identityView(gi db.GithubIdentity) *apigen.GitHubIdentity {
	granted := parseScopes(gi.Scopes)
	return &apigen.GitHubIdentity{
		Login: gi.Login, GithubID: gi.GithubID, Name: gi.Name, AvatarURL: gi.AvatarUrl,
		Status: apigen.GitHubIdentityStatus(gi.Status), ConnectedAt: gi.ConnectedAt,
		Scopes: granted, MissingScopes: missingScopes(granted),
	}
}

// Disconnect 断开一个身份槽位；断开 Agent 账号时自动化身份退回我的账号。
// 断开我的账号前先删除 Oryn 建的全部仓库 hook（hook 由我的账号管理，断开后无法再删）。
func (s *Service) Disconnect(ctx context.Context, slot string) error {
	if !validSlot(slot) {
		return errf("invalid_request", "slot must be user or agent")
	}
	if slot == slotUser {
		if _, err := s.st.Q.GetGithubIdentity(ctx, slot); err != nil {
			if isNoRows(err) {
				return errf("not_found", "GitHub identity "+slot+" is not connected")
			}
			return err
		}
		if err := s.dropAllHooks(ctx); err != nil {
			return err
		}
	}
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteGithubIdentity(ctx, slot)
		if err != nil {
			return err
		}
		if n == 0 {
			return errf("not_found", "GitHub identity "+slot+" is not connected")
		}
		if slot != slotAgent {
			return nil
		}
		cur, err := q.GetOwnerGithubAutomation(ctx)
		if err != nil {
			if isNoRows(err) {
				return nil
			}
			return err
		}
		if cur == slotAgent {
			_, err = q.SetOwnerGithubAutomation(ctx, slotUser)
		}
		return err
	})
	if err != nil {
		return err
	}
	s.changed()
	return nil
}

// SetAutomationIdentity 设置自动化任务（GitHub 触发、项目级定时）使用的身份槽位。
func (s *Service) SetAutomationIdentity(ctx context.Context, slot string) error {
	if !validSlot(slot) {
		return errf("invalid_request", "automationIdentity must be user or agent")
	}
	if slot == slotAgent {
		if _, err := s.st.Q.GetGithubIdentity(ctx, slotAgent); err != nil {
			if isNoRows(err) {
				return errf("github_agent_required", "connect the Agent account first")
			}
			return err
		}
	}
	n, err := s.st.Q.SetOwnerGithubAutomation(ctx, slot)
	if err != nil {
		return err
	}
	if n == 0 {
		return errf("conflict", "owner not initialized")
	}
	s.changed()
	return nil
}

// AutomationIdentity 返回自动化线程实际使用的槽位：设置为 agent 且 Agent 账号可用时为 agent，其余为 user。
func (s *Service) AutomationIdentity(ctx context.Context) string {
	v, err := s.st.Q.GetOwnerGithubAutomation(ctx)
	if err != nil || v != slotAgent {
		return slotUser
	}
	row, err := s.st.Q.GetGithubIdentity(ctx, slotAgent)
	if err != nil || row.Status != "ok" {
		return slotUser
	}
	return slotAgent
}
