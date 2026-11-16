package github

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 任务管理接口（`/api/v1/github/tasks|tasks/cleanup|triage`，M15 步骤 7.1，R24）。

// Tasks 列出 issue 任务与发布哨兵（`GET /api/v1/github/tasks`），按最近变化倒序。
func (s *Service) Tasks(ctx context.Context) ([]apigen.GitHubTask, error) {
	issues, err := s.st.Q.ListGithubIssueTasks(ctx)
	if err != nil {
		return nil, err
	}
	releases, err := s.st.Q.ListGithubReleases(ctx)
	if err != nil {
		return nil, err
	}
	name := s.repoNames()
	out := make([]apigen.GitHubTask, 0, len(issues)+len(releases))
	for _, r := range issues {
		full, err := name(ctx, r.RepoID)
		if err != nil {
			return nil, err
		}
		out = append(out, issueTaskAPI(r, full))
	}
	for _, r := range releases {
		full, err := name(ctx, r.RepoID)
		if err != nil {
			return nil, err
		}
		rounds, version := int(r.Rounds), r.Version
		out = append(out, apigen.GitHubTask{
			Kind: apigen.GitHubTaskKindRelease, Repo: full, Version: &version, ThreadID: r.ThreadID, State: r.State,
			Rounds: &rounds, UpdatedAt: r.UpdatedAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

func issueTaskAPI(r db.ListGithubIssueTasksRow, full string) apigen.GitHubTask {
	number, thread := r.Number, r.ThreadID
	return apigen.GitHubTask{
		Kind: apigen.GitHubTaskKindIssue, Repo: full, Number: &number, ThreadID: &thread, State: r.State,
		Classification: r.Classification, UpdatedAt: r.UpdatedAt,
	}
}

// repoNames 返回按仓库 id 取 `owner/repo` 的查找函数（带缓存）：先取记录过该 id 的项目（repoFullName，跟随改名），
// 项目都已解绑时退回 hook 表里的仓库名。
func (s *Service) repoNames() func(ctx context.Context, repoID int64) (string, error) {
	names := map[int64]string{}
	var hooks map[int64]string
	return func(ctx context.Context, repoID int64) (string, error) {
		if full, ok := names[repoID]; ok {
			return full, nil
		}
		full, err := s.repoFullName(ctx, repoID)
		if err != nil {
			return "", err
		}
		if full == "" {
			if hooks == nil {
				rows, err := s.st.Q.ListGithubHooks(ctx)
				if err != nil {
					return "", err
				}
				hooks = make(map[int64]string, len(rows))
				for _, h := range rows {
					hooks[h.RepoID] = h.FullName
				}
			}
			full = hooks[repoID]
		}
		names[repoID] = full
		return full, nil
	}
}

// CleanupTaskAPI 是 `POST /api/v1/github/tasks/cleanup`：经 CleanupTask 把任务置为 abandoned 并冻结 orb，返回修改后的任务。
func (s *Service) CleanupTaskAPI(ctx context.Context, repo string, number int64) (apigen.GitHubTask, error) {
	if err := checkTaskRef(repo, number); err != nil {
		return apigen.GitHubTask{}, err
	}
	if err := s.CleanupTask(ctx, repo, int(number)); err != nil {
		return apigen.GitHubTask{}, err
	}
	rows, err := s.st.Q.ListGithubIssueTasks(ctx)
	if err != nil {
		return apigen.GitHubTask{}, err
	}
	name := s.repoNames()
	for _, r := range rows {
		if r.Number != number {
			continue
		}
		full, err := name(ctx, r.RepoID)
		if err != nil {
			return apigen.GitHubTask{}, err
		}
		if strings.EqualFold(full, repo) {
			return issueTaskAPI(r, full), nil
		}
	}
	return apigen.GitHubTask{}, errf("not_found", fmt.Sprintf("no GitHub task for %s#%d", repo, number))
}

// checkTaskRef 校验 `owner/repo` 与 issue 编号。
func checkTaskRef(repo string, number int64) error {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return errf("invalid_request", "repo must be owner/repo")
	}
	if number < 1 {
		return errf("invalid_request", "number must be positive")
	}
	return nil
}

// Triage 是手动分诊（`POST /api/v1/github/triage`，R24）：以我的账号拉取线上 issue，合成 issues.opened 交给触发规则。
// 与 webhook 走同一条 fire 路径：同样的驱动判定（发送者是我的账号）、限流与 Agent 账号要求，任务在建线程的事务里落库。
// ruleID 非空时只用这条规则（须属于绑定该仓库的项目，发布哨兵规则除外，停用的规则也可指定）；否则取仓库启用中的
// issue_opened 规则。返回新建的线程，或 issue 已关联的线程。
func (s *Service) Triage(ctx context.Context, repo string, number int64, ruleID string) (string, error) {
	if err := checkTaskRef(repo, number); err != nil {
		return "", err
	}
	prj, err := s.boundProjectByName(ctx, repo)
	if err != nil {
		return "", err
	}
	repoID := *prj.GithubRepoID
	full, _ := githubRepoPath(prj.RepoUrl)
	var cands []db.GithubTrigger
	if ruleID != "" {
		t, err := s.st.Q.GetGithubTrigger(ctx, ruleID)
		if err != nil {
			if isNoRows(err) {
				return "", errf("not_found", "trigger not found")
			}
			return "", err
		}
		if t.Event == evWorkflowRun {
			return "", errf("invalid_request", "workflow_run_completed triggers cannot triage an issue")
		}
		owner, err := s.st.Q.GetProject(ctx, t.ProjectID)
		if err != nil {
			return "", err
		}
		if owner.ArchivedAt != nil || owner.GithubRepoID == nil || *owner.GithubRepoID != repoID {
			return "", errf("invalid_request", "trigger does not belong to a project bound to "+full)
		}
		cands = []db.GithubTrigger{t}
	} else {
		cands, err = s.st.Q.ListEnabledGithubTriggersByRepo(ctx, db.ListEnabledGithubTriggersByRepoParams{RepoID: &repoID, Event: evIssueOpened})
		if err != nil {
			return "", err
		}
		if len(cands) == 0 {
			return "", &Error{Status: http.StatusConflict, Code: "conflict", Message: "no enabled issue_opened trigger for " + full}
		}
	}
	var issue hookIssue
	if err := s.callAs(ctx, slotUser, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", full, number), nil, &issue); err != nil {
		if errKind(err) == kindNotFound {
			return "", errf("not_found", fmt.Sprintf("issue %s#%d not found on GitHub", full, number))
		}
		return "", err
	}
	user, err := s.st.Q.GetGithubIdentity(ctx, slotUser)
	if err != nil {
		return "", err
	}
	ev := triggerEvent{
		event: evIssueOpened, repoID: repoID, repo: full, issue: issue, sender: sender{Login: user.Login, Type: "User"},
	}
	// 同 onIssues：作者关系只在发送者就是作者时可用
	if strings.EqualFold(user.Login, issue.User.Login) {
		ev.assoc = issue.AuthorAssociation
	}
	outcome, err := s.fire(ctx, ev, cands)
	if err != nil {
		return "", err
	}
	switch outcome {
	case "handled", "ignored:already_linked":
		row, err := s.st.Q.GetThreadIssue(ctx, db.GetThreadIssueParams{RepoID: repoID, Number: number})
		if err != nil {
			return "", err
		}
		return row.ThreadID, nil
	case "ignored:agent_required":
		return "", errf("github_agent_required", "this trigger needs the Agent GitHub account, which is not connected or needs re-authorization")
	}
	return "", &Error{Status: http.StatusConflict, Code: "conflict", Message: "triage skipped: " + strings.TrimPrefix(outcome, "ignored:")}
}

// boundProjectByName 取绑定到 repo（owner/repo，不区分大小写）的未归档项目（最早的一个）；没有 → github_not_bound。
func (s *Service) boundProjectByName(ctx context.Context, repo string) (db.Project, error) {
	prjs, err := s.st.Q.ListGithubCandidateProjects(ctx)
	if err != nil {
		return db.Project{}, err
	}
	for _, p := range prjs {
		if full, ok := githubRepoPath(p.RepoUrl); ok && p.GithubRepoID != nil && strings.EqualFold(full, repo) {
			return p, nil
		}
	}
	return db.Project{}, errf("github_not_bound", "no project is bound to "+repo)
}
