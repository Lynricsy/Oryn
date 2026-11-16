package github

import (
	"context"
	"net/url"
	"time"

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

// BeginAuthorize 开始连接一个身份槽位：返回 GitHub 的授权地址（可在另一个浏览器里打开）。
func (s *Service) BeginAuthorize(ctx context.Context, slot string) (string, error) {
	if !validSlot(slot) {
		return "", errf("invalid_request", "slot must be user or agent")
	}
	app, err := s.app(ctx)
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
	q.Set("client_id", app.ClientID)
	q.Set("redirect_uri", s.cfg.PublicURL+oauthCallbackPath)
	q.Set("state", state)
	q.Set("prompt", "select_account")
	return s.cfg.WebURL + "/login/oauth/authorize?" + q.Encode(), nil
}

// clientSecret 解出 App 的 client secret。
func (s *Service) clientSecret(app db.GithubApp) (string, error) {
	b, err := s.cfg.Box.Open(aadClientSecret, app.ClientSecretEnc)
	return string(b), err
}

// expiresAt 把「n 秒后过期」换成毫秒时间戳；n 为 0 表示不过期。
func (s *Service) expiresAt(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	v := s.now().Add(time.Duration(n) * time.Second).UnixMilli()
	return &v
}

// FinishAuthorize 处理 OAuth 回调：消费 state，兑换用户令牌，查出账号并写进槽位。
func (s *Service) FinishAuthorize(ctx context.Context, code, state string) (string, error) {
	row, err := s.consumeState(ctx, state, "identity")
	if err != nil {
		return "", err
	}
	if row.Slot == nil || !validSlot(*row.Slot) {
		return "", errf("state_invalid", "state has no slot")
	}
	slot := *row.Slot
	app, err := s.app(ctx)
	if err != nil {
		return "", err
	}
	secret, err := s.clientSecret(app)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("client_id", app.ClientID)
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
	if err := s.call(ctx, "GET", "/user", "Bearer "+tok.AccessToken, nil, &user); err != nil {
		return "", err
	}
	if other, err := s.st.Q.GetGithubIdentity(ctx, otherSlot(slot)); err == nil && other.GithubID == user.ID {
		return "", errf("same_account", "the other slot is already connected to this GitHub account")
	} else if err != nil && !isNoRows(err) {
		return "", err
	}
	accessEnc, err := s.cfg.Box.Seal(aadAccess(slot), []byte(tok.AccessToken))
	if err != nil {
		return "", err
	}
	var refreshEnc []byte
	if tok.RefreshToken != "" {
		if refreshEnc, err = s.cfg.Box.Seal(aadRefresh(slot), []byte(tok.RefreshToken)); err != nil {
			return "", err
		}
	}
	name := user.Name
	if name != nil && *name == "" {
		name = nil
	}
	now := s.nowMs()
	if err := s.st.Q.UpsertGithubIdentity(ctx, db.UpsertGithubIdentityParams{
		Slot: slot, GithubID: user.ID, Login: user.Login, Name: name, AvatarUrl: user.AvatarURL,
		AccessTokenEnc: accessEnc, AccessExpiresAt: s.expiresAt(tok.ExpiresIn),
		RefreshTokenEnc: refreshEnc, RefreshExpiresAt: s.expiresAt(tok.RefreshTokenExpiresIn),
		ConnectedAt: now, UpdatedAt: now,
	}); err != nil {
		if isUnique(err) {
			return "", errf("same_account", "the other slot is already connected to this GitHub account")
		}
		return "", err
	}
	s.changed()
	return slot, nil
}

// baseToken 返回槽位的用户访问令牌；临近过期时用刷新令牌换新（按槽位串行：刷新令牌用一次即废）。
func (s *Service) baseToken(ctx context.Context, slot string) (string, error) {
	mu, ok := s.slotMu[slot]
	if !ok {
		return "", errf("invalid_request", "slot must be user or agent")
	}
	mu.Lock()
	defer mu.Unlock()
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
	if row.AccessExpiresAt == nil || *row.AccessExpiresAt > s.now().Add(refreshSkew).UnixMilli() {
		b, err := s.cfg.Box.Open(aadAccess(slot), row.AccessTokenEnc)
		return string(b), err
	}
	if len(row.RefreshTokenEnc) == 0 {
		return "", s.markReauth(ctx, slot)
	}
	refresh, err := s.cfg.Box.Open(aadRefresh(slot), row.RefreshTokenEnc)
	if err != nil {
		return "", err
	}
	app, err := s.app(ctx)
	if err != nil {
		return "", err
	}
	secret, err := s.clientSecret(app)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("client_id", app.ClientID)
	form.Set("client_secret", secret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", string(refresh))
	tok, err := s.oauthToken(ctx, form)
	if err != nil {
		return "", err
	}
	if tok.Error != "" || tok.AccessToken == "" {
		return "", s.markReauth(ctx, slot)
	}
	accessEnc, err := s.cfg.Box.Seal(aadAccess(slot), []byte(tok.AccessToken))
	if err != nil {
		return "", err
	}
	refreshEnc, refreshExp := row.RefreshTokenEnc, row.RefreshExpiresAt
	if tok.RefreshToken != "" {
		if refreshEnc, err = s.cfg.Box.Seal(aadRefresh(slot), []byte(tok.RefreshToken)); err != nil {
			return "", err
		}
		refreshExp = s.expiresAt(tok.RefreshTokenExpiresIn)
	}
	if err := s.st.Q.SetGithubIdentityTokens(ctx, db.SetGithubIdentityTokensParams{
		AccessTokenEnc: accessEnc, AccessExpiresAt: s.expiresAt(tok.ExpiresIn), RefreshTokenEnc: refreshEnc,
		RefreshExpiresAt: refreshExp, UpdatedAt: s.nowMs(), Slot: slot,
	}); err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// markReauth 把槽位标为需要重新授权，返回 github_identity_required。
func (s *Service) markReauth(ctx context.Context, slot string) error {
	if err := s.st.Q.SetGithubIdentityStatus(ctx, db.SetGithubIdentityStatusParams{Status: "reauth", UpdatedAt: s.nowMs(), Slot: slot}); err != nil {
		return err
	}
	s.changed()
	return errf("github_identity_required", "GitHub identity "+slot+" needs to be re-authorized")
}

// Disconnect 断开一个身份槽位；断开 Agent 账号时自动化身份退回我的账号。
func (s *Service) Disconnect(ctx context.Context, slot string) error {
	if !validSlot(slot) {
		return errf("invalid_request", "slot must be user or agent")
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
