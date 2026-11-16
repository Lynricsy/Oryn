package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
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
		s.onMerged(ctx, threadID, pull)
	case "open":
		s.scheduleRollup(repoID, number, 0, true)
	}
	return true, nil
}

// onMerged 处理关联 PR 的合并（步骤 5.3 实现）。
func (s *Service) onMerged(context.Context, string, db.GithubPull) {}

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
	prjs, err := s.st.Q.ListProjectsByGithubRepo(ctx, &repoID)
	if err != nil {
		return "", err
	}
	if len(prjs) == 0 {
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
	for _, l := range live {
		s.th.Publish(ctx, l.ThreadID)
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
// （兜住丢失的 PR 事件与连接 App 之前就有的 PR）。
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
	if prj.GithubRepoID == nil || prj.GithubInstallationID == nil {
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
	for _, pr := range pulls {
		if !sameRepoPull(repoID, pr) || pr.Head.SHA != head {
			continue
		}
		if st := pullState(pr); st != "open" && st != "merged" {
			continue
		}
		name := full
		if pr.Base.Repo != nil && pr.Base.Repo.FullName != "" {
			name = pr.Base.Repo.FullName
		}
		if err := s.upsertPull(ctx, repoID, name, pr); err != nil {
			return err
		}
		if _, err := s.link(ctx, threadID, repoID, pr.Number); err != nil {
			return err
		}
	}
	return nil
}
