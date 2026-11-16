package github

import (
	"context"
	"crypto/subtle"
	"log/slog"
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
		StateHash: ids.Hash(state), Kind: "identity", Slot: slot, ExpiresAt: s.now().Add(stateTTL).UnixMilli(),
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

// consumeState 读并消费一条身份授权 state：已被消费或已过期 → state_invalid。
func (s *Service) consumeState(ctx context.Context, state string) (db.GithubOauthState, error) {
	row, err := s.st.Q.GetGithubState(ctx, ids.Hash(state))
	if err != nil {
		if isNoRows(err) {
			return row, errf("state_invalid", "state not found")
		}
		return row, err
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

// FinishAuthorize 处理 OAuth 回调：消费 state，兑换用户令牌，查出账号与实际授予的 scope 并写进槽位。
// 连接我的账号后在后台重新绑定全部项目（未连接时绑定无从进行）。
func (s *Service) FinishAuthorize(ctx context.Context, code, state string) (string, error) {
	row, err := s.consumeState(ctx, state)
	if err != nil {
		return "", err
	}
	if !validSlot(row.Slot) {
		return "", errf("state_invalid", "state has no slot")
	}
	slot := row.Slot
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
	// 刚兑换的令牌还没落进槽位：不经槽位发请求，401 也不改动槽位里现有的令牌
	req, err := s.newRequest(ctx, http.MethodGet, "/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	h, err := s.send("", req, &user)
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
	if slot == slotUser && !s.enqueue(func(ctx context.Context) {
		if err := s.BindProjects(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("github: 连接我的账号后绑定项目失败", "err", err)
		}
	}) {
		slog.Warn("github: 任务队列已满，项目绑定留待 hook 同步周期")
	}
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

// tokenRejected 在上游对令牌 tok 回 401 之后调用（R2）：slots 里当前令牌仍是 tok 的槽位置为 reauth，之后 tokenFor 拒绝该槽位，
// 请求不再发往上游，设置页显示需要重新授权。控制面自己的请求（client.go 的 do，槽位已知）与出站代理注入的令牌
// （GitHubTokenRejected，只知道令牌，两个槽位都比）共用这一处。先比明文、再以读到的密文做条件更新
// （MarkGithubIdentityReauth）：401 的请求在途期间槽位可能已重新授权，换上的新令牌不会被旧令牌的 401 置为 reauth；
// 与任何槽位都不匹配的令牌（如密钥库里 github.com 的 PAT）直接忽略。
func (s *Service) tokenRejected(ctx context.Context, tok string, slots ...string) error {
	if tok == "" {
		return nil
	}
	marked := false
	for _, slot := range slots {
		row, err := s.st.Q.GetGithubIdentity(ctx, slot)
		if isNoRows(err) {
			continue
		}
		if err != nil {
			return err
		}
		if row.Status == "reauth" {
			continue
		}
		cur, err := s.cfg.Box.Open(aadAccess(slot), row.AccessTokenEnc)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(cur, []byte(tok)) != 1 {
			continue
		}
		n, err := s.st.Q.MarkGithubIdentityReauth(ctx, db.MarkGithubIdentityReauthParams{
			UpdatedAt: s.nowMs(), Slot: slot, AccessTokenEnc: row.AccessTokenEnc,
		})
		if err != nil {
			return err
		}
		marked = marked || n > 0
	}
	if marked {
		s.changed()
	}
	return nil
}

// GitHubTokenRejected 实现 orbs.GitHub：出站代理注入的令牌被上游回 401 时调用（R2 同样适用于 orb 经代理的请求，
// 含 GET），按令牌找到槽位置为 reauth。失败只记日志。
func (s *Service) GitHubTokenRejected(ctx context.Context, orbID, token string) {
	if err := s.tokenRejected(ctx, token, slotUser, slotAgent); err != nil {
		slog.Warn("github: 出站代理的令牌被拒，标记重新授权失败", "orb", orbID, "err", err)
	}
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

// Disconnect 断开一个身份槽位；不改动「自动化任务的 GitHub 身份」设置（R6：断开 Agent 账号后自动化线程按 Q6 回退）。
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
	n, err := s.st.Q.DeleteGithubIdentity(ctx, slot)
	if err != nil {
		return err
	}
	if n == 0 {
		return errf("not_found", "GitHub identity "+slot+" is not connected")
	}
	s.changed()
	return nil
}

// SetAutomationIdentity 设置自动化任务（GitHub 触发、项目级定时、发布哨兵）使用的身份槽位。Agent 账号未连接时也可设为
// agent：实际使用时由 ResolveAutomationSlot 按 Q6 回退。
func (s *Service) SetAutomationIdentity(ctx context.Context, slot string) error {
	if !validSlot(slot) {
		return errf("invalid_request", "automationIdentity must be user or agent")
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

// errAgentRequired 是不可信输入驱动的自动化（「任何人」档规则、外部 PR 评审）在 Agent 账号不可用时的错误（Q6）。
var errAgentRequired = &Error{Code: "github_agent_required", Message: "this automation requires the Agent account to be connected"}

// agentFallbackNote 是全自动线程因 Agent 账号不可用而回退到我的账号时，追加在首条提示词末尾的提示（Q6）。
const agentFallbackNote = "[Oryn: the Agent GitHub account is unavailable, so this automated thread uses the owner's GitHub account instead.]"

// ResolveAutomationSlot 决定全自动线程使用的身份槽位（R6 / Q6）：
//   - untrusted 为真时只用 Agent 账号，Agent 槽位不是 ok 则返回 errAgentRequired，不回退；
//   - 否则按「自动化任务的 GitHub 身份」设置（缺省 agent）：设为 user 时用 user；设为 agent 且 Agent 槽位是 ok 时用 agent，
//     不可用时回退到 user，并返回回退提示 note（调用方写进线程）。
func (s *Service) ResolveAutomationSlot(ctx context.Context, untrusted bool) (slot, note string, err error) {
	agentOK := false
	row, err := s.st.Q.GetGithubIdentity(ctx, slotAgent)
	switch {
	case err == nil:
		agentOK = row.Status == "ok"
	case !isNoRows(err):
		return "", "", err
	}
	if untrusted {
		if !agentOK {
			return "", "", errAgentRequired
		}
		return slotAgent, "", nil
	}
	setting, err := s.st.Q.GetOwnerGithubAutomation(ctx)
	switch {
	case isNoRows(err):
		setting = slotAgent
	case err != nil:
		return "", "", err
	}
	switch {
	case setting != slotAgent:
		return slotUser, "", nil
	case agentOK:
		return slotAgent, "", nil
	}
	return slotUser, agentFallbackNote, nil
}
