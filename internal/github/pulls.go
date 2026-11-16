package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// samplingWindow 是 PR 事件找不到已采样提交时，要求立即采样的线程的活跃窗口。
const samplingWindow = 24 * time.Hour

func init() {
	on("pull_request", (*Service).onPullRequest)
}

// pullActions 是 pull_request 事件里处理的动作。
var pullActions = map[string]bool{
	"opened": true, "reopened": true, "closed": true, "synchronize": true, "edited": true,
	"ready_for_review": true, "converted_to_draft": true,
}

// pullState 是 PR 入库用的状态：已合并为 merged，否则取载荷的 state。
func pullState(pr pullPayload) string {
	if pr.merged() {
		return "merged"
	}
	return pr.State
}

// sameRepoPull 报告 PR 的头分支是否与 base 在同一仓库（fork 来的 PR 不关联线程）。
func sameRepoPull(repoID int64, pr pullPayload) bool {
	base := repoID
	if pr.Base.Repo != nil {
		base = pr.Base.Repo.ID
	}
	return pr.Head.Repo != nil && pr.Head.Repo.ID == base
}

// upsertPull 把 PR 写进 github_pulls（头提交变化时库里的 CI 汇总随之清空）。
func (s *Service) upsertPull(ctx context.Context, repoID int64, repoFullName string, pr pullPayload) error {
	var mergedAt *int64
	if pr.MergedAt != nil {
		if ms := githubTimeMs(*pr.MergedAt); ms != 0 {
			mergedAt = &ms
		}
	}
	return s.st.Q.UpsertGithubPull(ctx, db.UpsertGithubPullParams{
		RepoID: repoID, Number: pr.Number, RepoFullName: repoFullName, Title: pr.Title, State: pullState(pr),
		Draft: pr.Draft, SameRepo: sameRepoPull(repoID, pr), HeadRef: pr.Head.Ref, HeadSha: pr.Head.SHA,
		BaseRef: pr.Base.Ref, AuthorLogin: pr.User.Login, HtmlUrl: pr.HTMLURL, MergedAt: mergedAt, UpdatedAt: s.nowMs(),
	})
}

// link 建立线程 ↔ PR 关联；该 PR 已有未取消、线程未归档的关联时不再建第二个。返回是否新建。
func (s *Service) link(ctx context.Context, threadID string, repoID, number int64) (bool, error) {
	n, err := s.st.Q.CountLivePullLinks(ctx, db.CountLivePullLinksParams{RepoID: repoID, Number: number})
	if err != nil || n > 0 {
		return false, err
	}
	rows, err := s.st.Q.LinkThreadPull(ctx, db.LinkThreadPullParams{ThreadID: threadID, RepoID: repoID, Number: number, LinkedAt: s.nowMs()})
	if err != nil || rows == 0 {
		return false, err // rows == 0：已有关联，或用户取消关联留下的墓碑
	}
	s.th.Publish(ctx, threadID)
	pull, err := s.st.Q.GetGithubPull(ctx, db.GetGithubPullParams{RepoID: repoID, Number: number})
	if err != nil {
		return true, err
	}
	switch pull.State {
	case "merged":
		return true, s.onMerged(ctx, threadID, pull)
	case "open":
		s.registerWatch(ctx, "pull", repoID, pull.RepoFullName, number, threadID)
		s.scheduleRollup(repoID, number, 0, true)
	}
	return true, nil
}

// onMerged 把关联线程标为已交付（时间 = 合并时间，不归档）并发一条通知；通知失败只记日志。
func (s *Service) onMerged(ctx context.Context, threadID string, pull db.GithubPull) error {
	at := s.nowMs()
	if pull.MergedAt != nil {
		at = *pull.MergedAt
	}
	if err := s.th.MarkShipped(ctx, threadID, at); err != nil {
		if errors.Is(err, threads.ErrNotFound) {
			return nil
		}
		return err
	}
	if s.n != nil {
		if err := s.n.PostThread(ctx, threadID, "done", fmt.Sprintf("PR #%d merged: %s", pull.Number, pull.Title)); err != nil {
			slog.Warn("github: 发送 PR 合并通知失败", "thread", threadID, "err", err)
		}
	}
	return nil
}

// onPullRequest 处理 pull_request 事件：PR 入库，同仓库的打开 PR 按头提交关联到采到该提交的线程。
func (s *Service) onPullRequest(ctx context.Context, action string, body []byte) (string, error) {
	if !pullActions[action] {
		return "ignored:action", nil
	}
	var ev struct {
		PullRequest pullPayload `json:"pull_request"`
		Repository  repoPayload `json:"repository"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return "", err
	}
	repoID, pr := ev.Repository.ID, ev.PullRequest
	ok, err := s.admitted(ctx, repoID, pr.Number)
	if err != nil {
		return "", err
	}
	if !ok {
		return "ignored:unbound_repo", nil
	}
	if err := s.upsertPull(ctx, repoID, ev.Repository.FullName, pr); err != nil {
		return "", err
	}
	key := db.CountLivePullLinksParams{RepoID: repoID, Number: pr.Number}
	if sameRepoPull(repoID, pr) && pullState(pr) == "open" && pr.Head.SHA != "" {
		n, err := s.st.Q.CountLivePullLinks(ctx, key)
		if err != nil {
			return "", err
		}
		if n == 0 {
			if err := s.linkByHead(ctx, repoID, pr); err != nil {
				return "", err
			}
		}
	}
	live, err := s.st.Q.ListLivePullThreads(ctx, db.ListLivePullThreadsParams(key))
	if err != nil {
		return "", err
	}
	var merged *db.GithubPull
	if action == "closed" && pr.merged() {
		p, err := s.st.Q.GetGithubPull(ctx, db.GetGithubPullParams(key))
		if err != nil {
			return "", err
		}
		merged = &p
	}
	for _, l := range live {
		if merged != nil {
			if err := s.onMerged(ctx, l.ThreadID, *merged); err != nil {
				return "", err
			}
		}
		s.th.Publish(ctx, l.ThreadID)
	}
	// pull_request_opened 规则（M15 R12）：在入库与关联之后匹配，已关联到线程的 PR 不再触发
	if out, err := s.onPullTrigger(ctx, action, body); err != nil || out != "" {
		return out, err
	}
	return "handled", nil
}

// linkByHead 把 PR 关联到采到其头提交的线程；没有线程采到时，要求近期活跃的候选线程立即采样，
// 采样完成后经 ThreadCommitsSampled 建立关联（agent 刚 push 完、还没回到 idle 的情形）。
func (s *Service) linkByHead(ctx context.Context, repoID int64, pr pullPayload) error {
	tid, err := s.st.Q.ThreadByCommit(ctx, db.ThreadByCommitParams{Sha: pr.Head.SHA, RepoID: &repoID})
	if err == nil {
		_, err = s.link(ctx, tid, repoID, pr.Number)
		return err
	}
	if !isNoRows(err) {
		return err
	}
	if s.sampler == nil {
		return nil
	}
	since := s.now().Add(-samplingWindow).UnixMilli()
	ids, err := s.st.Q.ListSamplingCandidates(ctx, db.ListSamplingCandidatesParams{RepoID: &repoID, Since: &since})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !s.enqueue(func(ctx context.Context) {
			if err := s.sampler.SampleCommits(ctx, id); err != nil && ctx.Err() == nil {
				slog.Warn("github: 立即采样线程提交失败", "thread", id, "err", err)
			}
		}) {
			slog.Warn("github: 任务队列已满，跳过立即采样", "thread", id)
		}
	}
	return nil
}

// ThreadCommitsSampled 由提交采样在成功落库后调用；head 是采样时的 HEAD（可能为空）。不阻塞采样：
// 队列满时丢弃，下次采样再来。
func (s *Service) ThreadCommitsSampled(threadID, head string) {
	if !s.enqueue(func(ctx context.Context) {
		if err := s.linkSampled(ctx, threadID, head); err != nil && ctx.Err() == nil {
			slog.Warn("github: 按线程提交关联 PR 失败", "thread", threadID, "err", err)
		}
	}) {
		slog.Warn("github: 任务队列已满，跳过线程提交的 PR 关联", "thread", threadID)
	}
}

// linkSampled 按线程采到的提交关联库里已知的 PR；一个都没有时按 HEAD 向 GitHub 反查一次
// （兜住丢失的 PR 事件与连接 App 之前就有的 PR）。反查到的跨仓库 PR（头分支与 base 不在同一仓库，或 base 不是项目仓库，
// 如从项目 fork 向上游开的 PR）只在作者是已连接身份之一时关联（M15 步骤 3.2：陌生人复制同一 head 的 PR 不关联）。
func (s *Service) linkSampled(ctx context.Context, threadID, head string) error {
	rows, err := s.st.Q.PullsForThreadCommits(ctx, threadID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := s.link(ctx, threadID, r.RepoID, r.Number); err != nil {
			return err
		}
	}
	if len(rows) > 0 || head == "" {
		return nil
	}
	s.mu.Lock()
	seen := s.sampledHeads[threadID] == head
	s.mu.Unlock()
	if seen {
		return nil
	}
	t, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil {
		if isNoRows(err) {
			return nil
		}
		return err
	}
	if t.ProjectID == nil || t.ArchivedAt != nil {
		return nil
	}
	prj, err := s.st.Q.GetProject(ctx, *t.ProjectID)
	if err != nil {
		return err
	}
	if prj.GithubRepoID == nil {
		return nil
	}
	full, auth, err := s.projectAuth(ctx, prj)
	if err != nil {
		return err
	}
	var pulls []pullPayload
	if err := s.call(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/pulls?per_page=5", full, head), auth, nil, &pulls); err != nil {
		return err
	}
	s.mu.Lock()
	if s.sampledHeads == nil {
		s.sampledHeads = map[string]string{}
	}
	s.sampledHeads[threadID] = head
	s.mu.Unlock()
	repoID := *prj.GithubRepoID
	var logins map[string]bool
	for _, pr := range pulls {
		if pr.Head.SHA != head {
			continue
		}
		if st := pullState(pr); st != "open" && st != "merged" {
			continue
		}
		baseID, name := repoID, full
		if pr.Base.Repo != nil && pr.Base.Repo.ID != 0 {
			baseID = pr.Base.Repo.ID
		}
		if pr.Base.Repo != nil && pr.Base.Repo.FullName != "" {
			name = pr.Base.Repo.FullName
		}
		if baseID != repoID || !sameRepoPull(baseID, pr) {
			if logins == nil {
				if logins, err = s.identityLogins(ctx); err != nil {
					return err
				}
			}
			if !logins[strings.ToLower(pr.User.Login)] {
				continue
			}
		}
		if err := s.upsertPull(ctx, baseID, name, pr); err != nil {
			return err
		}
		if _, err := s.link(ctx, threadID, baseID, pr.Number); err != nil {
			return err
		}
	}
	return nil
}

// identityLogins 返回已连接的身份登录名（小写）。
func (s *Service) identityLogins(ctx context.Context) (map[string]bool, error) {
	ids, err := s.st.Q.ListGithubIdentities(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id.Login != "" {
			out[strings.ToLower(id.Login)] = true
		}
	}
	return out, nil
}

// repoNodeQuery 按 node id 取仓库的 owner/name（GraphQL 的 createPullRequest 只带 repositoryId）。
const repoNodeQuery = `query($id:ID!){node(id:$id){... on Repository{nameWithOwner}}}`

// pullCreated 由 GitHubWriteEnd 在 orb 经出站代理成功创建 PR 后调用（M15 步骤 3.2）：后台找到这个 PR，关联到 orb 的线程
// （created_via_egress=1，跨仓库同样关联），并登记对象级 watch。不阻塞出站代理：队列满时放弃（之后靠提交采样兜底）。
func (s *Service) pullCreated(orbID string, w egress.GitHubWrite, respKeys []string) {
	if !s.enqueue(func(ctx context.Context) {
		if err := s.linkCreatedPull(ctx, orbID, w, respKeys); err != nil && ctx.Err() == nil {
			slog.Warn("github: 关联出站代理创建的 PR 失败", "orb", orbID, "op", w.Op, "err", err)
		}
	}) {
		slog.Warn("github: 任务队列已满，跳过出站代理创建的 PR 关联", "orb", orbID)
	}
}

// linkCreatedPull 以 orb 的槽位读出刚创建的 PR：REST 按响应里的 node_id 在仓库最近创建的 PR 里找；GraphQL 先按 repositoryId
// 查出仓库，再找作者是本槽位、正文哈希相同（无正文时在 selfWriteWindow 内创建）的最近一个。取到 head 后入库并关联。
func (s *Service) linkCreatedPull(ctx context.Context, orbID string, w egress.GitHubWrite, respKeys []string) error {
	tid := s.orbThread(ctx, orbID)
	if tid == nil {
		return nil
	}
	slot := s.orbSlot(ctx, orbID)
	node := ""
	for _, k := range respKeys {
		if v, ok := strings.CutPrefix(k, "node:"); ok {
			node = v
			break
		}
	}
	full := w.Repo
	switch {
	case w.Op == "createPullRequest":
		tok, err := s.tokenFor(ctx, slot)
		if err != nil {
			return err
		}
		var out struct {
			Node *struct {
				NameWithOwner string `json:"nameWithOwner"`
			} `json:"node"`
		}
		if err := s.graphql(ctx, "Bearer "+tok, repoNodeQuery, map[string]any{"id": w.Target}, &out); err != nil {
			return err
		}
		if out.Node == nil || out.Node.NameWithOwner == "" {
			return fmt.Errorf("github: 仓库 node %s 不存在", w.Target)
		}
		full = out.Node.NameWithOwner
	case strings.HasPrefix(w.Op, http.MethodPost+" ") && full != "" && node != "":
	default:
		return nil // 其它形态（如 revertPullRequest）：不是新建 PR 的请求，交给提交采样
	}
	login := ""
	if id, err := s.st.Q.GetGithubIdentity(ctx, slot); err == nil {
		login = id.Login
	} else if !isNoRows(err) {
		return err
	}
	var list []json.RawMessage
	if err := s.callAs(ctx, slot, http.MethodGet, fmt.Sprintf("/repos/%s/pulls?state=all&sort=created&direction=desc&per_page=10", full), nil, &list); err != nil {
		return err
	}
	since := s.now().Add(-selfWriteWindow).UnixMilli()
	var found *pullPayload
	for _, raw := range list {
		var pr pullPayload
		var extra struct {
			NodeID    string `json:"node_id"`
			CreatedAt string `json:"created_at"`
		}
		if json.Unmarshal(raw, &pr) != nil || json.Unmarshal(raw, &extra) != nil {
			continue
		}
		match := node != "" && extra.NodeID == node
		if node == "" && login != "" && strings.EqualFold(pr.User.Login, login) {
			if w.BodySHA256 != "" {
				match = egress.BodyHash(deref(pr.Body)) == w.BodySHA256
			} else {
				match = githubTimeMs(extra.CreatedAt) >= since
			}
		}
		if match {
			found = &pr
			break
		}
	}
	if found == nil {
		return fmt.Errorf("github: 在 %s 最近的 PR 里找不到出站代理创建的 PR", full)
	}
	pr := *found
	repoID, name := int64(0), full
	if pr.Base.Repo != nil {
		repoID = pr.Base.Repo.ID
		if pr.Base.Repo.FullName != "" {
			name = pr.Base.Repo.FullName
		}
	}
	if repoID == 0 {
		return fmt.Errorf("github: PR %s#%d 缺少 base 仓库", full, pr.Number)
	}
	if err := s.upsertPull(ctx, repoID, name, pr); err != nil {
		return err
	}
	if _, err := s.st.Q.LinkThreadPullViaEgress(ctx, db.LinkThreadPullViaEgressParams{
		ThreadID: *tid, RepoID: repoID, Number: pr.Number, LinkedAt: s.nowMs(),
	}); err != nil {
		return err
	}
	if tgt, ok, err := s.pullTarget(ctx, repoID, pr.Number); err != nil || !ok || tgt.threadID != *tid {
		return err // 用户此前取消过这个关联（墓碑保持取消），或另有更近期的线程
	}
	s.th.Publish(ctx, *tid)
	pull, err := s.st.Q.GetGithubPull(ctx, db.GetGithubPullParams{RepoID: repoID, Number: pr.Number})
	if err != nil {
		return err
	}
	switch pull.State {
	case "merged":
		return s.onMerged(ctx, *tid, pull)
	case "open":
		s.registerWatch(ctx, "pull", repoID, name, pr.Number, *tid)
		s.scheduleRollup(repoID, pr.Number, 0, true)
	}
	return nil
}

// threadRepoID 取线程所在项目记录的 GitHub 仓库 id；线程不存在、无项目或项目没有仓库 id → not_found。
func (s *Service) threadRepoID(ctx context.Context, threadID string) (int64, error) {
	notFound := errf("not_found", "thread has no GitHub repository")
	t, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil {
		if isNoRows(err) {
			return 0, notFound
		}
		return 0, err
	}
	if t.ProjectID == nil {
		return 0, notFound
	}
	prj, err := s.st.Q.GetProject(ctx, *t.ProjectID)
	if err != nil {
		if isNoRows(err) {
			return 0, notFound
		}
		return 0, err
	}
	if prj.GithubRepoID == nil {
		return 0, notFound
	}
	return *prj.GithubRepoID, nil
}

// SetPullListening 暂停或恢复线程关联 PR 的事件回流；恢复时 CI 回流轮数清零。
func (s *Service) SetPullListening(ctx context.Context, threadID string, number int64, on bool) error {
	repoID, err := s.threadRepoID(ctx, threadID)
	if err != nil {
		return err
	}
	n, err := s.st.Q.SetThreadPullListening(ctx, db.SetThreadPullListeningParams{Listening: on, ThreadID: threadID, RepoID: repoID, Number: number})
	if err != nil {
		return err
	}
	if n == 0 {
		return errf("not_found", "pull request is not linked to this thread")
	}
	s.th.Publish(ctx, threadID)
	return nil
}

// UnlinkPull 取消线程与 PR 的关联（留下墓碑，阻止自动重连）。
func (s *Service) UnlinkPull(ctx context.Context, threadID string, number int64) error {
	repoID, err := s.threadRepoID(ctx, threadID)
	if err != nil {
		return err
	}
	n, err := s.st.Q.UnlinkThreadPull(ctx, db.UnlinkThreadPullParams{ThreadID: threadID, RepoID: repoID, Number: number})
	if err != nil {
		return err
	}
	if n == 0 {
		return errf("not_found", "pull request is not linked to this thread")
	}
	s.th.Publish(ctx, threadID)
	return nil
}
