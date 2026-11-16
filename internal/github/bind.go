package github

import (
	"context"
	"encoding/json"
	"errors"
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

// ProjectGitHubAPI 是项目的 GitHub 绑定摘要：已绑定（github_repo_id 非 NULL）时 bound 为真；未绑定但记录了原因
// （not_found / org_restricted / sso_required，R8）时带 bindStatus；两者都没有返回 nil。
func ProjectGitHubAPI(p db.Project) *apigen.ProjectGitHub {
	if p.GithubRepoID == nil && p.GithubBindStatus == nil {
		return nil
	}
	out := &apigen.ProjectGitHub{
		Listen: apigen.GitHubListen{Reviews: p.GithubListenReviews, Comments: p.GithubListenComments, Ci: p.GithubListenCi},
	}
	if p.GithubRepoID != nil {
		out.RepoID, out.Bound = *p.GithubRepoID, true
	} else {
		st := apigen.ProjectGitHubBindStatus(*p.GithubBindStatus)
		out.BindStatus = &st
	}
	if p.RepoUrl != nil {
		if _, path, ok := orbs.HTTPSRepo(*p.RepoUrl); ok {
			out.FullName = path
		}
	}
	return out
}

// BindProject 按项目的仓库地址绑定 GitHub 仓库（记录仓库 id）；仓库改名时跟随。我的账号未连接或待重新授权时不做任何事。
func (s *Service) BindProject(ctx context.Context, projectID string) error {
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
//   - 以我的账号 GET /repos/{full} 成功：写入 github_repo_id 并清空 github_bind_status，随后建立或校正仓库 hook；
//   - 返回 404 或 R4 的 403（org_restricted / sso_required）：清空 repo id，并把原因写进 github_bind_status（R8）。
func (s *Service) bind(ctx context.Context, prj db.Project) (changed bool, err error) {
	full, ok := githubRepoPath(prj.RepoUrl)
	if !ok {
		if prj.GithubRepoID == nil && prj.GithubBindStatus == nil {
			return false, nil
		}
		if err := s.st.Q.ClearProjectGithub(ctx, prj.ID); err != nil {
			return true, err
		}
		s.releaseHook(ctx, prj.GithubRepoID)
		return true, nil
	}
	var repo struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	if err := s.callAs(ctx, slotUser, http.MethodGet, "/repos/"+full, nil, &repo); err != nil {
		var ge *Error
		switch kind := errKind(err); {
		case errors.As(err, &ge) && ge.Code == "github_identity_required":
			return false, nil // 连接我的账号后 FinishAuthorize 会重新绑定
		case kind == kindNotFound || kind == kindOrgRestricted || kind == kindSSORequired:
			return s.unbind(ctx, prj, kind)
		}
		return false, err
	}
	if prj.GithubRepoID == nil || *prj.GithubRepoID != repo.ID || prj.GithubBindStatus != nil {
		if err := s.st.Q.SetProjectGithub(ctx, db.SetProjectGithubParams{GithubRepoID: &repo.ID, ID: prj.ID}); err != nil {
			return false, err
		}
		changed = true
		if prj.GithubRepoID != nil && *prj.GithubRepoID != repo.ID {
			s.releaseHook(ctx, prj.GithubRepoID)
		}
	}
	name := full
	if repo.FullName != "" && !strings.EqualFold(repo.FullName, full) {
		// followRename 自己广播
		if err := s.followRename(ctx, repo.ID, repo.FullName); err != nil {
			return changed, err
		}
		name = repo.FullName
	}
	// 新绑定或还没有 hook 记录时建立仓库 hook（M15 步骤 1.2）；失败只记日志，hookSyncInterval 循环会再校正
	if _, herr := s.st.Q.GetGithubHook(ctx, repo.ID); changed || isNoRows(herr) {
		if err := s.ensureHook(ctx, repo.ID, name); err != nil {
			slog.Warn("github: 建立仓库 hook 失败", "repo", name, "err", err)
		}
	}
	return changed, nil
}

// unbind 把读不到仓库的项目置为未绑定并记下原因 status；原先绑定的仓库按需删除 hook。
func (s *Service) unbind(ctx context.Context, prj db.Project, status string) (bool, error) {
	if prj.GithubRepoID == nil && prj.GithubBindStatus != nil && *prj.GithubBindStatus == status {
		return false, nil
	}
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.ClearProjectGithub(ctx, prj.ID); err != nil {
			return err
		}
		return q.SetProjectGithubBindStatus(ctx, db.SetProjectGithubBindStatusParams{GithubBindStatus: &status, ID: prj.ID})
	})
	if err != nil {
		return false, err
	}
	s.releaseHook(ctx, prj.GithubRepoID)
	return true, nil
}

// releaseHook 在项目不再绑定 repoID 之后删除没有其他项目绑定的仓库 hook；失败只记日志（同步循环会清理）。
func (s *Service) releaseHook(ctx context.Context, repoID *int64) {
	if repoID == nil {
		return
	}
	if err := s.dropHook(ctx, *repoID); err != nil {
		slog.Warn("github: 删除仓库 hook 失败", "repo_id", *repoID, "err", err)
	}
}

// followRename 把记录了该仓库 id 的项目地址改成新名字（旧地址进 github_prev_urls），并同步 PR 记录与仓库 hook 记录里的仓库名。
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
		if err := q.SetGithubHooksRepoName(ctx, db.SetGithubHooksRepoNameParams{FullName: fullName, RepoID: repoID}); err != nil {
			return err
		}
		if err := q.SetGithubPullsRepoName(ctx, db.SetGithubPullsRepoNameParams{RepoFullName: fullName, RepoID: repoID}); err != nil {
			return err
		}
		// watch 的键含 full_name：改名后不跟着改会在新旧名字下各轮询一份
		return q.SetGithubWatchesRepoName(ctx, db.SetGithubWatchesRepoNameParams{FullName: fullName, RepoID: repoID})
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
