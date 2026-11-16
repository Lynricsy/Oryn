package github

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orbManaged 判断 orb 是否由 GitHub 集成下发凭据，并返回其身份槽位行（M15）：不是 runner、不是协调宿主、且 orb 的
// 身份槽位（github_identity 列）为 ok 的工作 orb，有没有项目都一样（OAuth 令牌不按仓库收窄，不再要求项目已绑定）。
// state 为 GitHubTokenOK 表示归管且槽位可用；Unmanaged 表示不归管（调用方可回落 PAT）；Unavailable 表示归管但槽位不可用。
func (s *Service) orbManaged(ctx context.Context, orb db.Orb) (db.GithubIdentity, orbs.GitHubTokenState) {
	if orbs.IsRunner(orb) || orb.Coordinator {
		return db.GithubIdentity{}, orbs.GitHubTokenUnmanaged
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
			return id, orbs.GitHubTokenUnmanaged
		}
		return id, orbs.GitHubTokenUnavailable
	}
	return id, orbs.GitHubTokenOK
}

// OrbSetup 返回一台 orb 的 GitHub 下发参数；不归管或身份不可用时 ok 为假。
func (s *Service) OrbSetup(ctx context.Context, orb db.Orb) (orbs.GitHubSetup, bool) {
	id, st := s.orbManaged(ctx, orb)
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

// OrbToken 返回 orb 身份槽位的 OAuth 用户令牌（只由出站代理注入，不进 orb；OAuth App 的令牌不能按仓库收窄）。
func (s *Service) OrbToken(ctx context.Context, orbID string) (string, orbs.GitHubTokenState) {
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return "", orbs.GitHubTokenUnmanaged
	}
	id, st := s.orbManaged(ctx, orb)
	if st != orbs.GitHubTokenOK {
		return "", st
	}
	tok, err := s.tokenFor(ctx, id.Slot)
	if err != nil {
		slog.Warn("github: 取 orb 令牌失败", "orb", orbID, "slot", id.Slot, "err", err)
		return "", orbs.GitHubTokenUnavailable
	}
	return tok, orbs.GitHubTokenOK
}
