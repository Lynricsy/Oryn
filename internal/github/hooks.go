package github

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 仓库 hook 管理（M15 步骤 1.2）：已绑定仓库上，我的账号是 admin 时由 Oryn 建一个仓库 webhook。
// GET hook 时 GitHub 把 config.secret 打码成 ********（docs/plan-m15.md 附录 B-2），所以每轮同步都连同库里的 secret
// 一起 PATCH 整个 config，从不「先读后写」secret。hook 只在配置了 OAuth App 时管理（删除 OAuth 配置时全部删除）。

// hookEvents 是仓库 hook 订阅的事件（固定字面量，见 docs/plan-m15.md「全局约束」）。push 是 M24 加的（触发项目仓库镜像拉取）：
// ensureHook 每轮整体 PATCH Events，已有 hook 在下一次 ResyncHooks（≤ hookSyncInterval）内订阅上。
var hookEvents = []string{
	"issues", "issue_comment", "pull_request", "pull_request_review", "pull_request_review_comment",
	"check_suite", "status", "workflow_run", "repository", "push",
}

// hook 状态（github_hooks.status）。org_restricted / sso_required 直接取 ghError 的类别。
const (
	hookActive  = "active"
	hookNoAdmin = "no_admin"
	hookError   = "error"
)

// hookSecretBytes 是每个 hook 独立 secret 的随机字节数（存 hex）。
const hookSecretBytes = 32

func aadHookSecret(repoID int64) string { return fmt.Sprintf("github/hook/%d/secret", repoID) }

// hookConfig / hookParams 是创建与改写 hook 的请求体（参数见 docs/plan-m15.md「全局约束」）。
type hookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Secret      string `json:"secret"`
	InsecureSSL string `json:"insecure_ssl"`
}

type hookParams struct {
	Name   string     `json:"name,omitempty"` // 只在创建时带 web
	Active bool       `json:"active"`
	Events []string   `json:"events"`
	Config hookConfig `json:"config"`
}

// webhookBase 返回 hook 投递地址的基址（OAuth 配置里的 webhook 基址，缺省 public_url）；没有 OAuth App 配置时 ok 为假。
func (s *Service) webhookBase(ctx context.Context) (base string, ok bool, err error) {
	row, err := s.st.Q.GetGithubOAuthApp(ctx)
	if err != nil {
		if isNoRows(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if row.WebhookBase != nil {
		return *row.WebhookBase, true, nil
	}
	return s.cfg.PublicURL, true, nil
}

// ensureHook 在仓库上建立或校正 Oryn 的 webhook，并把结果写进 github_hooks：
//   - 以我的账号 GET /repos/{full}，permissions.admin 为假时记 no_admin；403 按 R4 记 org_restricted / sso_required；
//   - 库里有 hook id 时 PATCH 它；否则找同 URL 的已有 hook 接管（PATCH 写入我们的 secret），都没有才创建；
//   - 其余上游错误记 error 与 message。
//
// 没有 OAuth App 配置或我的账号不可用时什么都不做；网络等本地错误原样返回（下轮重试）。
func (s *Service) ensureHook(ctx context.Context, repoID int64, full string) error {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	base, ok, err := s.webhookBase(ctx)
	if err != nil || !ok {
		return err
	}
	var repo struct {
		ID          int64  `json:"id"`
		FullName    string `json:"full_name"`
		Permissions struct {
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	if err := s.callAs(ctx, slotUser, http.MethodGet, "/repos/"+full, nil, &repo); err != nil {
		return s.hookFailed(ctx, repoID, full, err)
	}
	if repo.FullName != "" {
		full = repo.FullName
	}
	if repo.ID != repoID {
		return s.setHookStatus(ctx, repoID, full, hookError, fmt.Sprintf("repository id changed (%d)", repo.ID))
	}
	if !repo.Permissions.Admin {
		return s.setHookStatus(ctx, repoID, full, hookNoAdmin, "")
	}
	row, err := s.st.Q.GetGithubHook(ctx, repoID)
	have := err == nil
	if err != nil && !isNoRows(err) {
		return err
	}
	var secret string
	if have && len(row.SecretEnc) > 0 {
		b, err := s.cfg.Box.Open(aadHookSecret(repoID), row.SecretEnc)
		if err != nil {
			return err
		}
		secret = string(b)
	} else {
		b := make([]byte, hookSecretBytes)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		secret = hex.EncodeToString(b)
	}
	url := base + webhookPath
	p := hookParams{Active: true, Events: hookEvents, Config: hookConfig{URL: url, ContentType: "json", Secret: secret, InsecureSSL: "0"}}
	var hookID int64
	if have && row.HookID != nil {
		err := s.callAs(ctx, slotUser, http.MethodPatch, fmt.Sprintf("/repos/%s/hooks/%d", full, *row.HookID), p, nil)
		switch {
		case err == nil:
			hookID = *row.HookID
		case errKind(err) != kindNotFound: // 404 = hook 已被删掉：往下找同 URL 的或重建
			return s.hookFailed(ctx, repoID, full, err)
		}
	}
	if hookID == 0 {
		id, err := s.findHook(ctx, full, url)
		if err != nil {
			return s.hookFailed(ctx, repoID, full, err)
		}
		if id != 0 {
			// 接管同 URL 的已有 hook：secret 被打码读不到，直接写入我们的
			if err := s.callAs(ctx, slotUser, http.MethodPatch, fmt.Sprintf("/repos/%s/hooks/%d", full, id), p, nil); err != nil {
				return s.hookFailed(ctx, repoID, full, err)
			}
			hookID = id
		} else {
			p.Name = "web"
			var out struct {
				ID int64 `json:"id"`
			}
			if err := s.callAs(ctx, slotUser, http.MethodPost, "/repos/"+full+"/hooks", p, &out); err != nil {
				return s.hookFailed(ctx, repoID, full, err)
			}
			if out.ID == 0 {
				return s.setHookStatus(ctx, repoID, full, hookError, "create hook: response has no id")
			}
			hookID = out.ID
		}
	}
	enc, err := s.cfg.Box.Seal(aadHookSecret(repoID), []byte(secret))
	if err != nil {
		return err
	}
	if err := s.st.Q.UpsertGithubHook(ctx, db.UpsertGithubHookParams{
		RepoID: repoID, FullName: full, HookID: &hookID, SecretEnc: enc, Status: hookActive, UpdatedAt: s.nowMs(),
	}); err != nil {
		return err
	}
	if !have || row.Status != hookActive || row.HookID == nil || *row.HookID != hookID || row.FullName != full {
		s.changed()
	}
	return nil
}

// findHook 在仓库的 hook 列表里找 config.url 等于 url 的 hook；没有时返回 0。
func (s *Service) findHook(ctx context.Context, full, url string) (int64, error) {
	var found int64
	err := s.paged(ctx, slotUser, "/repos/"+full+"/hooks?per_page=100", 10, func(raw json.RawMessage) error {
		var hooks []struct {
			ID     int64 `json:"id"`
			Config struct {
				URL string `json:"url"`
			} `json:"config"`
		}
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return err
		}
		for _, h := range hooks {
			if found == 0 && h.Config.URL == url {
				found = h.ID
			}
		}
		return nil
	})
	return found, err
}

// hookFailed 把 ensureHook 途中的错误落到 hook 状态：我的账号不可用时保持现状；R4 的 403 记为对应状态；
// 其余上游错误记 error；网络等非上游错误原样返回。
func (s *Service) hookFailed(ctx context.Context, repoID int64, full string, err error) error {
	var de *Error
	if errors.As(err, &de) && de.Code == "github_identity_required" {
		return nil
	}
	var ge *ghError
	if !errors.As(err, &ge) {
		return err
	}
	switch ge.Kind {
	case kindSSORequired:
		msg := ge.SSOURL // 界面给出授权链接
		if msg == "" {
			msg = ge.Msg
		}
		return s.setHookStatus(ctx, repoID, full, kindSSORequired, msg)
	case kindOrgRestricted:
		return s.setHookStatus(ctx, repoID, full, kindOrgRestricted, ge.Msg)
	}
	return s.setHookStatus(ctx, repoID, full, hookError, clip(fmt.Sprintf("HTTP %d: %s", ge.Status, ge.Msg), 500))
}

// setHookStatus 写入非 active 的 hook 状态；已有行时保留 hook id 与 secret（已建的 hook 仍可能投递，验签要用）。
func (s *Service) setHookStatus(ctx context.Context, repoID int64, full, status, msg string) error {
	var errMsg *string
	if msg != "" {
		errMsg = &msg
	}
	row, err := s.st.Q.GetGithubHook(ctx, repoID)
	switch {
	case err == nil:
		if row.Status == status && row.FullName == full && (row.Error == nil) == (errMsg == nil) && (errMsg == nil || *row.Error == msg) {
			return nil
		}
		if err := s.st.Q.SetGithubHookStatus(ctx, db.SetGithubHookStatusParams{Status: status, Error: errMsg, UpdatedAt: s.nowMs(), RepoID: repoID}); err != nil {
			return err
		}
		if row.FullName != full {
			if err := s.st.Q.SetGithubHooksRepoName(ctx, db.SetGithubHooksRepoNameParams{FullName: full, RepoID: repoID}); err != nil {
				return err
			}
		}
	case isNoRows(err):
		if err := s.st.Q.UpsertGithubHook(ctx, db.UpsertGithubHookParams{
			RepoID: repoID, FullName: full, Status: status, Error: errMsg, UpdatedAt: s.nowMs(),
		}); err != nil {
			return err
		}
	default:
		return err
	}
	s.changed()
	return nil
}

// dropHook 在没有其他未归档项目绑定该仓库时删除 Oryn 的 hook（GitHub 上与库里）。
func (s *Service) dropHook(ctx context.Context, repoID int64) error {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	prjs, err := s.st.Q.ListProjectsByGithubRepoAny(ctx, &repoID)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(prjs, func(p db.Project) bool { return p.ArchivedAt == nil }) {
		return nil
	}
	row, err := s.st.Q.GetGithubHook(ctx, repoID)
	if err != nil {
		if isNoRows(err) {
			return nil
		}
		return err
	}
	if err := s.deleteRemoteHook(ctx, row); err != nil {
		if retryable(err) {
			return err // 保留行，下轮同步再删
		}
		slog.Warn("github: 删除仓库 hook 失败，只删本地记录", "repo", row.FullName, "err", err)
	}
	if _, err := s.st.Q.DeleteGithubHook(ctx, repoID); err != nil {
		return err
	}
	s.changed()
	return nil
}

// dropAllHooks 删除 Oryn 管理的全部仓库 hook：GitHub 上尽力删除，库里的行一律删除（删除 OAuth 配置、断开我的账号前调用）。
func (s *Service) dropAllHooks(ctx context.Context) error {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	rows, err := s.st.Q.ListGithubHooks(ctx)
	if err != nil || len(rows) == 0 {
		return err
	}
	for _, row := range rows {
		if err := s.deleteRemoteHook(ctx, row); err != nil {
			slog.Warn("github: 删除仓库 hook 失败", "repo", row.FullName, "err", err)
		}
	}
	if err := s.st.Q.DeleteGithubHooks(ctx); err != nil {
		return err
	}
	s.changed()
	return nil
}

// deleteRemoteHook 以我的账号删除 GitHub 上的 hook；没建成（hook id 为空）或已不存在（404）都算成功。
func (s *Service) deleteRemoteHook(ctx context.Context, row db.GithubHook) error {
	if row.HookID == nil {
		return nil
	}
	err := s.callAs(ctx, slotUser, http.MethodDelete, fmt.Sprintf("/repos/%s/hooks/%d", row.FullName, *row.HookID), nil, nil)
	if errKind(err) == kindNotFound {
		return nil
	}
	return err
}

// isOrynHook 报告 hookID 是否是 Oryn 在仓库 repo（owner/name，不区分大小写）上管理的 hook（出站代理据此拒绝 agent 改删，R9）。
func (s *Service) isOrynHook(ctx context.Context, repo string, hookID int64) bool {
	row, err := s.st.Q.GetGithubHookByHookID(ctx, &hookID)
	return err == nil && strings.EqualFold(row.FullName, repo)
}

// ResyncHooks 对全部已绑定（未归档、记录了 github_repo_id）的仓库重新校正 hook，并删除已无项目绑定的仓库上的 hook。
// 由 hookSyncInterval 循环调用；webhook 基址变化时 SetOAuthApp 也会调用（全量改写 config.url）。单个仓库失败只记日志。
func (s *Service) ResyncHooks(ctx context.Context) error {
	if _, ok, err := s.webhookBase(ctx); err != nil || !ok {
		return err
	}
	prjs, err := s.st.Q.ListGithubCandidateProjects(ctx)
	if err != nil {
		return err
	}
	want := map[int64]string{}
	var order []int64
	for _, p := range prjs {
		if p.GithubRepoID == nil {
			continue
		}
		full, ok := githubRepoPath(p.RepoUrl)
		if _, dup := want[*p.GithubRepoID]; !ok || dup {
			continue
		}
		want[*p.GithubRepoID] = full
		order = append(order, *p.GithubRepoID)
	}
	for _, id := range order {
		if err := s.ensureHook(ctx, id, want[id]); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("github: 校正仓库 hook 失败", "repo", want[id], "err", err)
		}
	}
	rows, err := s.st.Q.ListGithubHooks(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, ok := want[row.RepoID]; ok {
			continue
		}
		if err := s.dropHook(ctx, row.RepoID); err != nil && ctx.Err() == nil {
			slog.Warn("github: 删除无绑定仓库的 hook 失败", "repo", row.FullName, "err", err)
		}
	}
	return nil
}

// ReleaseHook 在项目解绑、归档或删除之后调用：没有其他未归档项目绑定该仓库时删除 Oryn 的 hook。
func (s *Service) ReleaseHook(ctx context.Context, repoID int64) error {
	return s.dropHook(ctx, repoID)
}
