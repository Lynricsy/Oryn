package github

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
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
	now := s.nowMs()
	sealed, err := s.sealTokens(slot, tok, now)
	if err != nil {
		return "", err
	}
	name := user.Name
	if name != nil && *name == "" {
		name = nil
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		connectedAt := s.nextConnectedAt(ctx, q, slot, now)
		if err := q.UpsertGithubIdentity(ctx, db.UpsertGithubIdentityParams{
			Slot: slot, GithubID: user.ID, Login: user.Login, Name: name, AvatarUrl: user.AvatarURL,
			AccessTokenEnc: sealed.accessEnc, AccessExpiresAt: sealed.accessExpiresAt,
			RefreshTokenEnc: sealed.refreshEnc, RefreshExpiresAt: sealed.refreshExpiresAt,
			ConnectedAt: connectedAt, UpdatedAt: now,
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

// tokenGen 标识一次 OAuth 授权代际（同一账号的同一次连接）。
// refresh_token 轮换不改动 github_id 与 connected_at；重新授权或改绑异账号必然推进代际，
// 以此防止单飞刷新与 401 重放跨代际/跨账号错用新授权令牌。
type tokenGen struct {
	githubID    int64
	connectedAt int64
}

func genOf(row db.GithubIdentity) tokenGen {
	return tokenGen{githubID: row.GithubID, connectedAt: row.ConnectedAt}
}

func errIdentityChanged(slot string) *Error {
	return errf("github_identity_changed", "GitHub identity "+slot+" authorization changed")
}

// nextConnectedAt 保证同一槽位每次 FinishAuthorize 的 connected_at 严格递增（包括断开后同毫秒重连），
// 使 tokenGen 在单测假时钟与生产环境中都能唯一区分每次授权代际。
func (s *Service) nextConnectedAt(ctx context.Context, q *db.Queries, slot string, now int64) int64 {
	connectedAt := now
	if prev, err := q.GetGithubIdentity(ctx, slot); err == nil && connectedAt <= prev.ConnectedAt {
		connectedAt = prev.ConnectedAt + 1
	}
	s.refreshMu.Lock()
	if s.lastConnectedAt == nil {
		s.lastConnectedAt = make(map[string]int64, 2)
	}
	if connectedAt <= s.lastConnectedAt[slot] {
		connectedAt = s.lastConnectedAt[slot] + 1
	}
	s.lastConnectedAt[slot] = connectedAt
	s.refreshMu.Unlock()
	return connectedAt
}

type refreshCall struct {
	gen   tokenGen
	done  chan struct{}
	token string
	err   error
}

type sealedTokens struct {
	accessEnc        []byte
	accessExpiresAt  *int64
	refreshEnc       []byte
	refreshExpiresAt *int64
}

// sealTokens 加密 OAuth 访问令牌与可选的刷新令牌，并按 nowMs 计算过期时间戳（毫秒）。
func (s *Service) sealTokens(slot string, tok oauthTokenResponse, nowMs int64) (sealedTokens, error) {
	var out sealedTokens
	accessEnc, err := s.cfg.Box.Seal(aadAccess(slot), []byte(tok.AccessToken))
	if err != nil {
		return out, err
	}
	out.accessEnc = accessEnc
	if tok.ExpiresIn > 0 {
		exp := nowMs + tok.ExpiresIn*1000
		out.accessExpiresAt = &exp
	}
	if tok.RefreshToken != "" {
		refreshEnc, err := s.cfg.Box.Seal(aadRefresh(slot), []byte(tok.RefreshToken))
		if err != nil {
			return out, err
		}
		out.refreshEnc = refreshEnc
		if tok.RefreshTokenExpiresIn > 0 {
			rexp := nowMs + tok.RefreshTokenExpiresIn*1000
			out.refreshExpiresAt = &rexp
		}
	}
	return out, nil
}

// needsRefresh 报告身份行是否持有刷新令牌且访问令牌已进入提前刷新窗口。
func needsRefresh(row db.GithubIdentity, nowMs int64) bool {
	return len(row.RefreshTokenEnc) > 0 &&
		row.AccessExpiresAt != nil &&
		nowMs+tokenRefreshSkew.Milliseconds() >= *row.AccessExpiresAt
}

// isRefreshTokenRevoked 报告 OAuth 令牌端点返回的业务错误码是否表示刷新令牌已确定失效（需重新授权）。
func isRefreshTokenRevoked(code string) bool {
	switch code {
	case "bad_refresh_token", "invalid_grant", "token_expired", "invalid_token", "access_denied":
		return true
	default:
		return false
	}
}

// tokenFor 返回槽位的用户访问令牌：持有刷新令牌且临近/已经过期时自动轮换；未连接或待重新授权时返回 github_identity_required。
func (s *Service) tokenFor(ctx context.Context, slot string) (string, error) {
	tok, _, err := s.tokenForGen(ctx, slot)
	return tok, err
}

// tokenForGen 返回槽位的访问令牌及其授权代际，供需要做 401 重试的调用方绑定初始授权代际。
func (s *Service) tokenForGen(ctx context.Context, slot string) (string, tokenGen, error) {
	if !validSlot(slot) {
		return "", tokenGen{}, errf("invalid_request", "slot must be user or agent")
	}
	row, err := s.st.Q.GetGithubIdentity(ctx, slot)
	if err != nil {
		if isNoRows(err) {
			return "", tokenGen{}, errf("github_identity_required", "GitHub identity "+slot+" is not connected")
		}
		return "", tokenGen{}, err
	}
	if row.Status == "reauth" {
		return "", tokenGen{}, errf("github_identity_required", "GitHub identity "+slot+" needs to be re-authorized")
	}
	gen := genOf(row)
	if needsRefresh(row, s.nowMs()) {
		tok, err := s.refreshSlot(ctx, slot, row.AccessTokenEnc, gen, false)
		return tok, gen, err
	}
	b, err := s.cfg.Box.Open(aadAccess(slot), row.AccessTokenEnc)
	return string(b), gen, err
}

// refreshSlot 按槽位合并同一授权代际的并发刷新（per-slot singleflight）：GitHub 的 refresh_token 是一次性轮换的，
// 并发发出两次刷新会让第二次拿到 bad_refresh_token 误标 reauth。
// 结果严格绑定 expectedGen：若在途期间槽位被重新授权或改绑为其他账号，绝不复用或返回新代际的令牌。
// 领跑协程一旦发起上游刷新即脱离调用方取消（context.WithoutCancel），防止 GitHub 已轮换却未落盘新 refresh_token；
// 其余等待者随自己的 ctx 取消而返回。
func (s *Service) refreshSlot(ctx context.Context, slot string, prevAccessEnc []byte, expectedGen tokenGen, forceOn401 bool) (string, error) {
	var call *refreshCall
	for {
		s.refreshMu.Lock()
		existing := s.refreshCalls[slot]
		if existing != nil {
			s.refreshMu.Unlock()
			select {
			case <-existing.done:
				if existing.gen == expectedGen {
					return existing.token, existing.err
				}
				// 在途刷新属于不同代际，绝不复用其结果，待它结束后重新检查本代际。
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			s.refreshMu.Unlock()
			return "", err
		}
		call = &refreshCall{gen: expectedGen, done: make(chan struct{})}
		if s.refreshCalls == nil {
			s.refreshCalls = make(map[string]*refreshCall, 2)
		}
		s.refreshCalls[slot] = call
		s.refreshMu.Unlock()
		break
	}

	defer func() {
		s.refreshMu.Lock()
		delete(s.refreshCalls, slot)
		s.refreshMu.Unlock()
		close(call.done)
	}()

	call.token, call.err = s.doRefreshSlot(ctx, slot, prevAccessEnc, expectedGen, forceOn401)
	return call.token, call.err
}

// doRefreshSlot 在 singleflight 保护下执行一次实际的 refresh_token 轮换与落盘（严格限定在 expectedGen 代际内）。
func (s *Service) doRefreshSlot(ctx context.Context, slot string, prevAccessEnc []byte, expectedGen tokenGen, forceOn401 bool) (string, error) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRefreshTimeout)
	defer cancel()

	row, err := s.st.Q.GetGithubIdentity(rctx, slot)
	if err != nil {
		if isNoRows(err) {
			return "", errf("github_identity_required", "GitHub identity "+slot+" is not connected")
		}
		return "", err
	}
	if genOf(row) != expectedGen {
		return "", errIdentityChanged(slot)
	}
	if row.Status == "reauth" {
		return "", errf("github_identity_required", "GitHub identity "+slot+" needs to be re-authorized")
	}
	// 同代际已被前一次并发刷新换了新令牌，且尚未进入刷新窗口：直接复用，避免重复消费 refresh_token。
	if !bytes.Equal(row.AccessTokenEnc, prevAccessEnc) && !needsRefresh(row, s.nowMs()) {
		b, err := s.cfg.Box.Open(aadAccess(slot), row.AccessTokenEnc)
		return string(b), err
	}
	if len(row.RefreshTokenEnc) == 0 {
		if forceOn401 {
			if merr := s.markSlotReauth(rctx, slot, row.AccessTokenEnc, "http_401"); merr != nil {
				return "", merr
			}
			return "", errf("github_identity_required", "GitHub identity "+slot+" needs to be re-authorized")
		}
		b, err := s.cfg.Box.Open(aadAccess(slot), row.AccessTokenEnc)
		return string(b), err
	}
	clientID, secret, err := s.oauthClient(rctx)
	if err != nil {
		return "", err
	}
	refreshPlain, err := s.cfg.Box.Open(aadRefresh(slot), row.RefreshTokenEnc)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", string(refreshPlain))
	tok, err := s.oauthToken(rctx, form)
	if err != nil {
		// 网络/超时/5xx/429 等临时故障绝不标 reauth，保留有效 refresh_token 待下次重试。
		slog.Warn("github: 刷新用户令牌失败（临时错误，保留槽位）", "slot", slot, "err", err)
		return "", err
	}
	if tok.Error != "" || tok.AccessToken == "" {
		if isRefreshTokenRevoked(tok.Error) {
			slog.Warn("github: 刷新令牌失效，槽位需重新授权", "slot", slot, "error", tok.Error, "desc", tok.ErrorDescription)
			if merr := s.markSlotReauth(rctx, slot, row.AccessTokenEnc, "refresh_"+tok.Error); merr != nil {
				return "", merr
			}
			return "", errf("github_identity_required", "GitHub identity "+slot+" needs to be re-authorized")
		}
		slog.Warn("github: 刷新用户令牌被拒（保留槽位）", "slot", slot, "error", tok.Error, "desc", tok.ErrorDescription)
		msg := tok.Error
		if tok.ErrorDescription != "" {
			msg += ": " + tok.ErrorDescription
		}
		if msg == "" {
			msg = "empty access_token in refresh response"
		}
		return "", errf("refresh_failed", msg)
	}
	if tok.ExpiresIn > 0 && tok.RefreshToken == "" {
		slog.Warn("github: 刷新返回了会过期的访问令牌但缺少新的 refresh_token", "slot", slot, "expires_in", tok.ExpiresIn)
	}
	now := s.nowMs()
	sealed, err := s.sealTokens(slot, tok, now)
	if err != nil {
		return "", err
	}
	n, err := s.st.Q.UpdateGithubIdentityTokens(rctx, db.UpdateGithubIdentityTokensParams{
		AccessTokenEnc:     sealed.accessEnc,
		AccessExpiresAt:    sealed.accessExpiresAt,
		RefreshTokenEnc:    sealed.refreshEnc,
		RefreshExpiresAt:   sealed.refreshExpiresAt,
		UpdatedAt:          now,
		Slot:               slot,
		PrevAccessTokenEnc: row.AccessTokenEnc,
	})
	if err != nil {
		return "", err
	}
	if n == 0 {
		// 刷新在途期间槽位被重新授权/改绑、断开或置为 reauth：
		// 仅当仍在同一授权代际内才复用当前令牌；代际已变则绝不返回新授权（尤其异账号）的令牌。
		cur, err := s.st.Q.GetGithubIdentity(rctx, slot)
		if err != nil {
			if isNoRows(err) {
				return "", errf("github_identity_required", "GitHub identity "+slot+" is not connected")
			}
			return "", err
		}
		if genOf(cur) != expectedGen {
			return "", errIdentityChanged(slot)
		}
		if cur.Status == "reauth" {
			return "", errf("github_identity_required", "GitHub identity "+slot+" needs to be re-authorized")
		}
		b, err := s.cfg.Box.Open(aadAccess(slot), cur.AccessTokenEnc)
		return string(b), err
	}
	slog.Info("github: 已自动刷新用户令牌", "slot", slot, "expires_in", tok.ExpiresIn)
	return tok.AccessToken, nil
}

// UserToken 返回「我的账号」的访问令牌（实现 repos.GitHub：控制面拉取 github.com 镜像用，M24）。
func (s *Service) UserToken(ctx context.Context) (string, error) { return s.tokenFor(ctx, slotUser) }

// markSlotReauth 条件更新把槽位置为 reauth（仅当数据库里的密文仍为 accessEnc 且非 reauth 时生效）并记一条 WARN 日志。
func (s *Service) markSlotReauth(ctx context.Context, slot string, accessEnc []byte, reason string) error {
	n, err := s.st.Q.MarkGithubIdentityReauth(ctx, db.MarkGithubIdentityReauthParams{
		UpdatedAt: s.nowMs(), Slot: slot, AccessTokenEnc: accessEnc,
	})
	if err != nil {
		return err
	}
	if n > 0 {
		slog.Warn("github: 身份槽位已标记为需要重新授权", "slot", slot, "reason", reason)
		s.changed()
	}
	return nil
}

// markTokenReauth 直接把仍处于 expectedGen 代际且持有明文 tok 的槽位置为 reauth（不再尝试刷新；用于刷新重试后仍 401）。
func (s *Service) markTokenReauth(ctx context.Context, tok string, expectedGen tokenGen, reason string, slots ...string) error {
	if tok == "" {
		return nil
	}
	for _, slot := range slots {
		row, err := s.st.Q.GetGithubIdentity(ctx, slot)
		if isNoRows(err) {
			continue
		}
		if err != nil {
			return err
		}
		if genOf(row) != expectedGen || row.Status == "reauth" {
			continue
		}
		cur, err := s.cfg.Box.Open(aadAccess(slot), row.AccessTokenEnc)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(cur, []byte(tok)) != 1 {
			continue
		}
		if err := s.markSlotReauth(ctx, slot, row.AccessTokenEnc, reason); err != nil {
			return err
		}
	}
	return nil
}

// handleUnauthorized 供控制面请求（client.go 的 do）在收到 401 后调用，严格绑定首次取令牌时的授权代际 expectedGen：
//   - 若槽位已被断开、标为 reauth 或代际已变（手动重连 / 改绑异账号），不刷新也不重放，让调用方返回原 401 错误；
//   - 若同代际已被并发刷新换上新令牌，返回同代际新令牌供本次请求透明重试；
//   - 若同代际仍持有 tok 且有 refresh_token，执行单飞刷新并在仍属同代际时返回新令牌供重试；
//   - 若同代际无 refresh_token，条件把槽位标为 reauth。
func (s *Service) handleUnauthorized(ctx context.Context, slot, tok string, expectedGen tokenGen) (newTok string, refreshed bool, err error) {
	if tok == "" || !validSlot(slot) {
		return "", false, nil
	}
	row, err := s.st.Q.GetGithubIdentity(ctx, slot)
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if row.Status == "reauth" || genOf(row) != expectedGen {
		return "", false, nil
	}
	cur, err := s.cfg.Box.Open(aadAccess(slot), row.AccessTokenEnc)
	if err != nil {
		return "", false, err
	}
	if subtle.ConstantTimeCompare(cur, []byte(tok)) != 1 {
		// 同代际已被并发刷新换上新令牌：若新令牌又临近过期则继续绑定 expectedGen 刷新，否则直接复用。
		if needsRefresh(row, s.nowMs()) {
			nt, err := s.refreshSlot(ctx, slot, row.AccessTokenEnc, expectedGen, false)
			if err != nil {
				if codeOfErr(err) == "github_identity_changed" {
					return "", false, nil
				}
				return "", false, err
			}
			return nt, true, nil
		}
		return string(cur), true, nil
	}
	if len(row.RefreshTokenEnc) > 0 {
		nt, err := s.refreshSlot(ctx, slot, row.AccessTokenEnc, expectedGen, true)
		if err != nil {
			if codeOfErr(err) == "github_identity_changed" {
				return "", false, nil
			}
			return "", false, err
		}
		return nt, true, nil
	}
	if err := s.markSlotReauth(ctx, slot, row.AccessTokenEnc, "http_401"); err != nil {
		return "", false, err
	}
	return "", false, nil
}

func codeOfErr(err error) string {
	var ge *Error
	if err != nil && errors.As(err, &ge) {
		return ge.Code
	}
	return ""
}

// tokenRejected 在上游对令牌 tok 回 401 之后调用（R2）：按明文比对找到仍持有 tok 的槽位。
// 若槽位有 refresh_token 则按该行授权代际尝试轮换恢复（仅确定性失效才标 reauth，临时网络错误保留槽位）；
// 若无 refresh_token 则以读到的密文条件更新为 reauth（MarkGithubIdentityReauth），不误伤在途重连换上的新令牌；
// 与任何槽位都不匹配的令牌（如密钥库里 github.com 的 PAT）直接忽略。
func (s *Service) tokenRejected(ctx context.Context, tok string, slots ...string) error {
	if tok == "" {
		return nil
	}
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
		if len(row.RefreshTokenEnc) > 0 {
			if _, err := s.refreshSlot(ctx, slot, row.AccessTokenEnc, genOf(row), true); err != nil {
				if codeOfErr(err) == "github_identity_changed" {
					continue
				}
				return err
			}
			continue
		}
		if err := s.markSlotReauth(ctx, slot, row.AccessTokenEnc, "egress_401"); err != nil {
			return err
		}
	}
	return nil
}

// GitHubTokenRejected 实现 orbs.GitHub：出站代理注入的令牌被上游回 401 时调用（R2 同样适用于 orb 经代理的请求，
// 含 GET），按令牌找到槽位后优先尝试用 refresh_token 刷新恢复，不可刷新或刷新失效时置为 reauth。失败只记日志。
func (s *Service) GitHubTokenRejected(ctx context.Context, orbID, token string) {
	if err := s.tokenRejected(ctx, token, slotUser, slotAgent); err != nil {
		slog.Warn("github: 出站代理的令牌被拒，刷新或标记重新授权失败", "orb", orbID, "err", err)
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
