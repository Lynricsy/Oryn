package github

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// sender 是事件的发送者（载荷里的 sender）。
type sender struct{ Login, Type string }

func senderOf(p senderPayload) sender { return sender{Login: p.Login, Type: p.Type} }

type permKey struct {
	repoID int64
	login  string // 小写
}

type permEntry struct {
	ok  bool
	exp time.Time
}

// isSelf 报告事件是不是 agent 自己造成的：发送者是 Agent 槽位的账号，或任一正文带 egress.AgentMarker。
func (s *Service) isSelf(ctx context.Context, who sender, bodies ...string) bool {
	if who.Login != "" {
		if agent, err := s.st.Q.GetGithubIdentity(ctx, slotAgent); err == nil && strings.EqualFold(agent.Login, who.Login) {
			return true
		}
	}
	for _, b := range bodies {
		if strings.Contains(b, egress.AgentMarker) {
			return true
		}
	}
	return false
}

// canDrive 报告发送者能否驱动 agent：不是 Bot，且对仓库有 write / admin 权限。reason 非空表示拒绝；err 非 nil 表示上游故障（可重试）。
func (s *Service) canDrive(ctx context.Context, prj db.Project, who sender) (ok bool, reason string, err error) {
	if who.Type == "Bot" {
		return false, "bot", nil
	}
	if who.Login == "" {
		return false, "no_write_access", nil
	}
	if user, err := s.st.Q.GetGithubIdentity(ctx, slotUser); err == nil && strings.EqualFold(user.Login, who.Login) {
		return true, "", nil
	} else if err != nil && !isNoRows(err) {
		return false, "", err
	}
	full, bound := githubRepoPath(prj.RepoUrl)
	if !bound || prj.GithubRepoID == nil || prj.GithubInstallationID == nil {
		return false, "unbound_repo", nil
	}
	key := permKey{repoID: *prj.GithubRepoID, login: strings.ToLower(who.Login)}
	s.mu.Lock()
	c, hit := s.perms[key]
	s.mu.Unlock()
	if hit && s.now().Before(c.exp) {
		return permResult(c.ok)
	}
	tok, err := s.installToken(ctx, *prj.GithubInstallationID)
	if err != nil {
		return false, "", err
	}
	var out struct {
		Permission string `json:"permission"`
	}
	allowed := false
	err = s.call(ctx, http.MethodGet, "/repos/"+full+"/collaborators/"+url.PathEscape(who.Login)+"/permission", "Bearer "+tok, nil, &out)
	switch {
	case err == nil:
		allowed = out.Permission == "admin" || out.Permission == "write"
	case isStatus(err, http.StatusNotFound):
	default:
		return false, "", err
	}
	s.mu.Lock()
	if s.perms == nil {
		s.perms = map[permKey]permEntry{}
	}
	s.perms[key] = permEntry{ok: allowed, exp: s.now().Add(permTTL)}
	s.mu.Unlock()
	return permResult(allowed)
}

func permResult(ok bool) (bool, string, error) {
	if ok {
		return true, "", nil
	}
	return false, "no_write_access", nil
}
