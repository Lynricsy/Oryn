package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// scopedKey 是按仓库收窄的令牌的缓存键。
type scopedKey struct {
	slot   string
	repoID int64
}

// scopedToken 是缓存的收窄令牌；baseSum 是签发时基础令牌的摘要：基础令牌刷新后旧条目自然失配，
// 并发签发写回的旧条目也不会被用到。
type scopedToken struct {
	token   string
	baseSum [32]byte
	exp     time.Time
}

// orbManaged 判断 orb 是否归 GitHub App 管，并返回其项目与身份槽位行。
// state 为 GitHubTokenOK 表示归管且槽位可用；Unmanaged 表示不归管（调用方可回落 PAT）；Unavailable 表示归管但槽位不可用。
func (s *Service) orbManaged(ctx context.Context, orb db.Orb, prj *db.Project) (db.Project, db.GithubIdentity, orbs.GitHubTokenState) {
	var p db.Project
	if orb.ProjectID == nil || orbs.IsRunner(orb) || orb.Coordinator {
		return p, db.GithubIdentity{}, orbs.GitHubTokenUnmanaged
	}
	if _, err := s.st.Q.GetGithubApp(ctx); err != nil {
		if !isNoRows(err) {
			slog.Warn("github: 读取 App 失败", "err", err)
		}
		return p, db.GithubIdentity{}, orbs.GitHubTokenUnmanaged
	}
	if prj != nil {
		p = *prj
	} else {
		var err error
		if p, err = s.st.Q.GetProject(ctx, *orb.ProjectID); err != nil {
			return p, db.GithubIdentity{}, orbs.GitHubTokenUnmanaged
		}
	}
	if p.GithubRepoID == nil || p.GithubInstallationID == nil || p.GithubOwnerID == nil {
		return p, db.GithubIdentity{}, orbs.GitHubTokenUnmanaged
	}
	slot := orb.GithubIdentity
	if slot != slotAgent {
		slot = slotUser
	}
	id, err := s.st.Q.GetGithubIdentity(ctx, slot)
	if err != nil || id.Status != "ok" {
		if err != nil && !isNoRows(err) {
			slog.Warn("github: 读取身份失败", "slot", slot, "err", err)
		}
		if slot == slotUser {
			return p, id, orbs.GitHubTokenUnmanaged
		}
		return p, id, orbs.GitHubTokenUnavailable
	}
	return p, id, orbs.GitHubTokenOK
}

// OrbSetup 返回一台 orb 的 GitHub 下发参数；不归 App 管或身份不可用时 ok 为假。
func (s *Service) OrbSetup(ctx context.Context, orb db.Orb, prj db.Project) (orbs.GitHubSetup, bool) {
	_, id, st := s.orbManaged(ctx, orb, &prj)
	if st != orbs.GitHubTokenOK {
		return orbs.GitHubSetup{}, false
	}
	name := id.Login
	if id.Name != nil && *id.Name != "" {
		name = *id.Name
	}
	return orbs.GitHubSetup{
		Login: id.Login,
		GitIdentity: apigen.GitIdentity{
			Name:  name,
			Email: noreplyEmail(id.GithubID, id.Login),
		},
		Agent:     id.Slot == slotAgent,
		APIOrigin: s.cfg.APIURL,
		GitOrigin: s.cfg.WebURL,
	}, true
}

// noreplyEmail 是 GitHub 账号的 noreply 提交邮箱。
func noreplyEmail(id int64, login string) string {
	return strconv.FormatInt(id, 10) + "+" + login + "@users.noreply.github.com"
}

// OrbToken 返回 orb 用的、只对项目仓库有效的 GitHub 令牌。
func (s *Service) OrbToken(ctx context.Context, orbID string) (string, orbs.GitHubTokenState) {
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return "", orbs.GitHubTokenUnmanaged
	}
	prj, id, st := s.orbManaged(ctx, orb, nil)
	if st != orbs.GitHubTokenOK {
		return "", st
	}
	base, err := s.tokenFor(ctx, id.Slot)
	if err != nil {
		slog.Warn("github: 取基础令牌失败", "orb", orbID, "slot", id.Slot, "err", err)
		return "", orbs.GitHubTokenUnavailable
	}
	sum := sha256.Sum256([]byte(base))
	key := scopedKey{slot: id.Slot, repoID: *prj.GithubRepoID}
	now := s.now()
	s.mu.Lock()
	if e, ok := s.scoped[key]; ok && e.baseSum == sum && now.Before(e.exp) {
		s.mu.Unlock()
		return e.token, orbs.GitHubTokenOK
	}
	s.mu.Unlock()

	tok, exp, err := s.issueScoped(ctx, base, *prj.GithubOwnerID, *prj.GithubRepoID)
	if err != nil {
		slog.Warn("github: 签发收窄令牌失败", "orb", orbID, "repo", *prj.GithubRepoID, "err", err)
		return "", orbs.GitHubTokenUnavailable
	}
	s.mu.Lock()
	if s.scoped == nil {
		s.scoped = map[scopedKey]scopedToken{}
	}
	s.scoped[key] = scopedToken{token: tok, baseSum: sum, exp: exp}
	s.mu.Unlock()
	return tok, orbs.GitHubTokenOK
}

// issueScoped 用 App 的 client 凭据把用户令牌收窄到一个仓库。
func (s *Service) issueScoped(ctx context.Context, base string, ownerID, repoID int64) (string, time.Time, error) {
	app, err := s.app(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	secret, err := s.clientSecret(app)
	if err != nil {
		return "", time.Time{}, err
	}
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(app.ClientID+":"+secret))
	in := map[string]any{"access_token": base, "target_id": ownerID, "repository_ids": []int64{repoID}}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := s.call(ctx, http.MethodPost, "/applications/"+app.ClientID+"/token/scoped", auth, in, &out); err != nil {
		return "", time.Time{}, err
	}
	if out.Token == "" {
		return "", time.Time{}, errf("github_upstream", "scoped token response has no token")
	}
	exp := s.now().Add(scopedTokenTTL)
	if t, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil {
		if early := t.Add(-5 * time.Minute); early.Before(exp) {
			exp = early
		}
	}
	return out.Token, exp, nil
}
