package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/Lynricsy/Oryn/internal/orbs"
)

// 出站代理门禁的控制面查询（M15 步骤 5.4）：egress.GateLookup 经 orbs 转到这里，每次写请求现查，
// 结果只取控制面的任务状态与该 orb 自己槽位的 GitHub 视角。查询失败一律按不放行处理。

// 进行中的发布任务状态（R22）：其余状态（green / failed / superseded）的线程不再享有推默认分支的例外。
var releaseActiveStates = map[string]bool{"awaiting_ci": true, "fixing": true}

// 不需要实现授权就能开 PR 的分类（R16）。
var implFreeClasses = map[string]bool{"bug": true, "documentation": true}

// ImplAuthorized 报告 orb 线程的任务是否已获得实现授权（R16）：分类是 bug / documentation，或 impl_authorized 已置位
// （accepted 标签、go ahead 评论）。线程没有任务或读取失败时为假。
func (s *Service) ImplAuthorized(ctx context.Context, orbID string) bool {
	tid := s.orbThread(ctx, orbID)
	if tid == nil {
		return false
	}
	ti, err := s.st.Q.GetThreadIssueByThread(ctx, *tid)
	if err != nil {
		if !isNoRows(err) {
			slog.Warn("github: 读取线程任务失败", "orb", orbID, "err", err)
		}
		return false
	}
	return ti.ImplAuthorized || (ti.Classification != nil && implFreeClasses[*ti.Classification])
}

// ReleaseThread 报告 orb 的线程是否为进行中的发布任务（v4：推送门禁开启时只有它能推默认分支）。
func (s *Service) ReleaseThread(ctx context.Context, orbID string) bool {
	tid := s.orbThread(ctx, orbID)
	if tid == nil {
		return false
	}
	rel, err := s.st.Q.GetGithubReleaseByThread(ctx, tid)
	if err != nil {
		if !isNoRows(err) {
			slog.Warn("github: 读取发布任务失败", "orb", orbID, "err", err)
		}
		return false
	}
	return releaseActiveStates[rel.State]
}

// IsOrynHook 报告 id 是否是 Oryn 管理的仓库 hook（R9）。hook id 在 GitHub 上全局唯一，所以只按 id 判定（不像 isOrynHook
// 那样比对仓库名）：仓库改名、转移后库里的 full_name 可能还是旧的，按名字比对会放过；repo 为空（/repositories/{id}）时同样适用。
func (s *Service) IsOrynHook(ctx context.Context, _ string, id int64) bool {
	_, err := s.st.Q.GetGithubHookByHookID(ctx, &id)
	if err != nil && !isNoRows(err) {
		slog.Warn("github: 读取 hook 失败", "hook", id, "err", err)
		return true // 读不到时按 Oryn 的 hook 处理（只多拒绝）
	}
	return err == nil
}

// HeadSHA 以 orb 自己的槽位查询分支当前指向的提交（PR 门禁）。repo 是 owner/repo、数值仓库 id 或仓库 node id；
// ref 是分支短名，或 `owner:branch`（owner 名下与 repo 同名的 fork）。
func (s *Service) HeadSHA(ctx context.Context, orbID, repo, ref string) (string, error) {
	slot, err := s.gateSlot(ctx, orbID)
	if err != nil {
		return "", err
	}
	rp, err := s.gateRepo(ctx, slot, repo)
	if err != nil {
		return "", err
	}
	full, branch := rp.FullName, ref
	if owner, b, fork := strings.Cut(ref, ":"); fork {
		_, name, _ := strings.Cut(full, "/")
		full, branch = owner+"/"+name, b
	}
	if branch == "" {
		return "", fmt.Errorf("github: 分支名为空")
	}
	var out struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err := s.callAs(ctx, slot, http.MethodGet, "/repos/"+full+"/git/ref/heads/"+escapeSegments(branch), nil, &out); err != nil {
		return "", err
	}
	if out.Object.Type != "" && out.Object.Type != "commit" {
		return "", fmt.Errorf("github: %s 的 %s 指向 %s 而不是提交", full, branch, out.Object.Type)
	}
	return strings.ToLower(out.Object.SHA), nil
}

// DefaultBranch 以 orb 自己的槽位查询仓库的默认分支（推送门禁的默认分支规则；缓存在出站代理里）。repo 的形式同 HeadSHA。
func (s *Service) DefaultBranch(ctx context.Context, orbID, repo string) (string, error) {
	slot, err := s.gateSlot(ctx, orbID)
	if err != nil {
		return "", err
	}
	rp, err := s.gateRepo(ctx, slot, repo)
	if err != nil {
		return "", err
	}
	if rp.DefaultBranch == "" {
		return "", fmt.Errorf("github: %s 没有默认分支", rp.FullName)
	}
	return rp.DefaultBranch, nil
}

// gateSlot 是门禁查询用的槽位：orb 必须归 GitHub 集成管且槽位可用（不回落到别的身份）。
func (s *Service) gateSlot(ctx context.Context, orbID string) (string, error) {
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return "", err
	}
	id, st := s.orbManaged(ctx, orb)
	if st != orbs.GitHubTokenOK {
		return "", fmt.Errorf("github: orb %s 的 GitHub 身份不可用", orbID)
	}
	return id.Slot, nil
}

// gateRepo 按 owner/repo、数值 id 或 node id 读出仓库（以 slot 的视角，规范 full_name 与默认分支）。
func (s *Service) gateRepo(ctx context.Context, slot, repo string) (repoPayload, error) {
	var rp repoPayload
	path := ""
	switch {
	case repo == "":
		return rp, fmt.Errorf("github: 仓库未知")
	case strings.Contains(repo, "/"):
		owner, name, _ := strings.Cut(repo, "/")
		if owner == "" || name == "" || strings.Contains(name, "/") {
			return rp, fmt.Errorf("github: 仓库 %q 无效", repo)
		}
		path = "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	case isDigits(repo):
		path = "/repositories/" + repo
	default:
		var out struct {
			Node *struct {
				NameWithOwner string `json:"nameWithOwner"`
			} `json:"node"`
		}
		if err := s.graphql(ctx, slot, repoNodeQuery, map[string]any{"id": repo}, &out); err != nil {
			return rp, err
		}
		if out.Node == nil || out.Node.NameWithOwner == "" {
			return rp, fmt.Errorf("github: 仓库 node %s 不存在", repo)
		}
		path = "/repos/" + out.Node.NameWithOwner
	}
	if err := s.callAs(ctx, slot, http.MethodGet, path, nil, &rp); err != nil {
		return rp, err
	}
	if rp.FullName == "" {
		return rp, fmt.Errorf("github: 读不到仓库 %s", repo)
	}
	return rp, nil
}

// escapeSegments 逐段转义 ref 路径（分支名里的 / 保留为路径分隔）。
func escapeSegments(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
