package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// maxPrevURLs 是 github_prev_urls 保留的旧地址个数上限。
const maxPrevURLs = 5

// githubRepoPath 取项目仓库地址里 github.com 的 `owner/repo`；不是恰好两段的 github.com 地址返回 false。
func githubRepoPath(repoURL *string) (string, bool) {
	if repoURL == nil {
		return "", false
	}
	host, path, ok := orbs.HTTPSRepo(*repoURL)
	if !ok || host != "github.com" || strings.Count(path, "/") != 1 {
		return "", false
	}
	return path, true
}

// ProjectGitHubAPI 是项目的 GitHub 绑定摘要：github_repo_id 为 NULL 时返回 nil。
func ProjectGitHubAPI(p db.Project) *apigen.ProjectGitHub {
	if p.GithubRepoID == nil {
		return nil
	}
	out := &apigen.ProjectGitHub{
		RepoID: *p.GithubRepoID,
		Bound:  p.GithubInstallationID != nil,
		Listen: apigen.GitHubListen{Reviews: p.GithubListenReviews, Comments: p.GithubListenComments, Ci: p.GithubListenCi},
	}
	if p.RepoUrl != nil {
		if _, path, ok := orbs.HTTPSRepo(*p.RepoUrl); ok {
			out.FullName = path
		}
	}
	return out
}

// sameBinding 报告两行项目的三列绑定是否一致。
func sameBinding(a, b db.Project) bool {
	eq := func(x, y *int64) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	return eq(a.GithubRepoID, b.GithubRepoID) && eq(a.GithubOwnerID, b.GithubOwnerID) &&
		eq(a.GithubInstallationID, b.GithubInstallationID)
}

// BindProject 按项目的仓库地址绑定 GitHub 仓库（记录仓库 id、所有者 id 与安装 id）；仓库改名时跟随。
func (s *Service) BindProject(ctx context.Context, projectID string) error {
	if _, err := s.st.Q.GetGithubApp(ctx); err != nil {
		if isNoRows(err) {
			return nil
		}
		return err
	}
	prj, err := s.st.Q.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	changed, err := s.bind(ctx, prj)
	if changed {
		s.changed()
	}
	return err
}

// bind 是 BindProject 的主体；changed 报告库里的绑定或仓库地址是否变了。
func (s *Service) bind(ctx context.Context, prj db.Project) (changed bool, err error) {
	full, ok := githubRepoPath(prj.RepoUrl)
	if !ok {
		if prj.GithubRepoID == nil && prj.GithubOwnerID == nil && prj.GithubInstallationID == nil {
			return false, nil
		}
		return true, s.st.Q.ClearProjectGithub(ctx, prj.ID)
	}
	auth, err := s.appAuth(ctx)
	if err != nil {
		return false, err
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	if err := s.call(ctx, http.MethodGet, "/repos/"+full+"/installation", auth, nil, &inst); err != nil {
		if isStatus(err, http.StatusNotFound) {
			if prj.GithubInstallationID == nil {
				return false, nil
			}
			return true, s.st.Q.ClearProjectGithubInstallation(ctx, prj.ID)
		}
		return false, err
	}
	tok, err := s.installToken(ctx, inst.ID)
	if err != nil {
		return false, err
	}
	var repo struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
		Owner    struct {
			ID int64 `json:"id"`
		} `json:"owner"`
	}
	if err := s.call(ctx, http.MethodGet, "/repos/"+full, "Bearer "+tok, nil, &repo); err != nil {
		return false, err
	}
	next := prj
	next.GithubRepoID, next.GithubOwnerID, next.GithubInstallationID = &repo.ID, &repo.Owner.ID, &inst.ID
	if !sameBinding(prj, next) {
		if err := s.st.Q.SetProjectGithub(ctx, db.SetProjectGithubParams{
			GithubRepoID: &repo.ID, GithubOwnerID: &repo.Owner.ID, GithubInstallationID: &inst.ID, ID: prj.ID,
		}); err != nil {
			return false, err
		}
		changed = true
	}
	if repo.FullName != "" && !strings.EqualFold(repo.FullName, full) {
		// followRename 自己广播
		if err := s.followRename(ctx, repo.ID, repo.FullName); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// followRename 把记录了该仓库 id 的项目地址改成新名字（旧地址进 github_prev_urls），并同步 PR 记录里的仓库名。
func (s *Service) followRename(ctx context.Context, repoID int64, fullName string) error {
	prjs, err := s.st.Q.ListProjectsByGithubRepoAny(ctx, &repoID)
	if err != nil {
		return err
	}
	next := "https://github.com/" + fullName
	now := s.nowMs()
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		for _, p := range prjs {
			if cur, ok := githubRepoPath(p.RepoUrl); ok && strings.EqualFold(cur, fullName) {
				continue
			}
			var prev []string
			if err := json.Unmarshal([]byte(p.GithubPrevUrls), &prev); err != nil {
				prev = nil
			}
			if p.RepoUrl != nil && *p.RepoUrl != "" && !slices.Contains(prev, *p.RepoUrl) {
				prev = append(prev, *p.RepoUrl)
			}
			if len(prev) > maxPrevURLs {
				prev = prev[len(prev)-maxPrevURLs:]
			}
			if prev == nil {
				prev = []string{}
			}
			b, err := json.Marshal(prev)
			if err != nil {
				return err
			}
			if err := q.SetProjectRepoURL(ctx, db.SetProjectRepoURLParams{
				RepoUrl: &next, GithubPrevUrls: string(b), UpdatedAt: now, ID: p.ID,
			}); err != nil {
				return err
			}
		}
		return q.SetGithubPullsRepoName(ctx, db.SetGithubPullsRepoNameParams{RepoFullName: fullName, RepoID: repoID})
	})
	if err != nil {
		return fmt.Errorf("github: 跟随仓库改名: %w", err)
	}
	s.changed()
	return nil
}

// BindProjects 按仓库地址重新绑定全部候选项目；单个失败只记日志。
func (s *Service) BindProjects(ctx context.Context) error {
	prjs, err := s.st.Q.ListGithubCandidateProjects(ctx)
	if err != nil {
		return err
	}
	for _, p := range prjs {
		if err := s.BindProject(ctx, p.ID); err != nil {
			slog.Warn("github: 绑定项目失败", "project", p.ID, "err", err)
		}
	}
	return nil
}
