package github

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

func init() {
	on("check_suite", (*Service).onCheckSuite)
	on("status", (*Service).onStatus)
}

type pullKey struct{ repo, number int64 }

// pendingRollup 是一个已排队（计时中或在任务队列里）的 CI 汇总；fresh 见 scheduleRollup。
type pendingRollup struct{ fresh bool }

// rollupQuery 取 PR 头提交与其 statusCheckRollup。
const rollupQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){headRefOid commits(last:1){nodes{commit{statusCheckRollup{state}}}}}}}`

// scheduleRollup 在 delay 之后汇总一次 PR 的 CI；同一 PR 已排队则不重复排。fresh 表示「关联刚建立」：
// 即使库里的 CI 状态没变，也把当前结果当作一次变化交给 onRollup（关联前就已失败的 CI 也要回流）。
func (s *Service) scheduleRollup(repoID, number int64, delay time.Duration, fresh bool) {
	k := pullKey{repoID, number}
	s.mu.Lock()
	if p, ok := s.rollups[k]; ok {
		p.fresh = p.fresh || fresh
		s.mu.Unlock()
		return
	}
	if s.rollups == nil {
		s.rollups = map[pullKey]*pendingRollup{}
	}
	p := &pendingRollup{fresh: fresh}
	s.rollups[k] = p
	s.mu.Unlock()
	run := func() {
		ok := s.enqueue(func(ctx context.Context) {
			s.mu.Lock()
			fresh := p.fresh
			delete(s.rollups, k)
			s.mu.Unlock()
			s.rollup(ctx, repoID, number, fresh)
		})
		if !ok {
			s.mu.Lock()
			delete(s.rollups, k)
			s.mu.Unlock()
			slog.Warn("github: 任务队列已满，跳过 CI 汇总", "repo", repoID, "number", number)
		}
	}
	if delay <= 0 {
		run()
		return
	}
	s.cfg.AfterFunc(delay, run)
}

// onCheckSuite / onStatus：头提交的 CI 有变化，防抖后汇总该提交上打开的 PR。
func (s *Service) onCheckSuite(ctx context.Context, _ string, body []byte) (string, error) {
	var ev struct {
		CheckSuite struct {
			HeadSHA string `json:"head_sha"`
		} `json:"check_suite"`
		Repository repoPayload `json:"repository"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return "", err
	}
	return s.rollupHead(ctx, ev.Repository.ID, ev.CheckSuite.HeadSHA)
}

func (s *Service) onStatus(ctx context.Context, _ string, body []byte) (string, error) {
	var ev struct {
		SHA        string      `json:"sha"`
		Repository repoPayload `json:"repository"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return "", err
	}
	return s.rollupHead(ctx, ev.Repository.ID, ev.SHA)
}

func (s *Service) rollupHead(ctx context.Context, repoID int64, sha string) (string, error) {
	pulls, err := s.st.Q.ListOpenGithubPullsByHead(ctx, db.ListOpenGithubPullsByHeadParams{RepoID: repoID, HeadSha: sha})
	if err != nil {
		return "", err
	}
	if len(pulls) == 0 || sha == "" {
		return "ignored:no_pull", nil
	}
	for _, p := range pulls {
		s.scheduleRollup(repoID, p.Number, rollupDebounce, false)
	}
	return "handled", nil
}

// rollupState 把 GraphQL 的 StatusState 映射成库里的 checks（空串 = 没有汇总）。
func rollupState(v string) string {
	switch v {
	case "SUCCESS":
		return "success"
	case "FAILURE", "ERROR":
		return "failure"
	case "PENDING", "EXPECTED":
		return "pending"
	}
	return ""
}

func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// rollup 用 GraphQL 取 PR 头提交的 CI 汇总并落库；头提交与库里不同时放弃（等后续事件）。
func (s *Service) rollup(ctx context.Context, repoID, number int64, fresh bool) {
	if err := s.doRollup(ctx, repoID, number, fresh); err != nil && ctx.Err() == nil {
		slog.Warn("github: CI 汇总失败", "repo", repoID, "number", number, "err", err)
	}
}

func (s *Service) doRollup(ctx context.Context, repoID, number int64, fresh bool) error {
	pull, err := s.st.Q.GetGithubPull(ctx, db.GetGithubPullParams{RepoID: repoID, Number: number})
	if err != nil {
		if isNoRows(err) {
			return nil
		}
		return err
	}
	prjs, err := s.st.Q.ListProjectsByGithubRepo(ctx, &repoID)
	if err != nil || len(prjs) == 0 {
		return err
	}
	tok, err := s.installToken(ctx, *prjs[0].GithubInstallationID)
	if err != nil {
		return err
	}
	owner, name, _ := strings.Cut(pull.RepoFullName, "/")
	var out struct {
		Repository *struct {
			PullRequest *struct {
				HeadRefOid string `json:"headRefOid"`
				Commits    struct {
					Nodes []struct {
						Commit struct {
							StatusCheckRollup *struct {
								State string `json:"state"`
							} `json:"statusCheckRollup"`
						} `json:"commit"`
					} `json:"nodes"`
				} `json:"commits"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := s.graphql(ctx, "Bearer "+tok, rollupQuery, map[string]any{"owner": owner, "name": name, "number": number}, &out); err != nil {
		return err
	}
	if out.Repository == nil || out.Repository.PullRequest == nil {
		return nil
	}
	pr := out.Repository.PullRequest
	if pr.HeadRefOid != pull.HeadSha {
		return nil
	}
	next := ""
	if n := pr.Commits.Nodes; len(n) > 0 && n[len(n)-1].Commit.StatusCheckRollup != nil {
		next = rollupState(n[len(n)-1].Commit.StatusCheckRollup.State)
	}
	prev := deref(pull.Checks)
	stored := prev != next || deref(pull.ChecksSha) != pull.HeadSha
	if stored {
		if err := s.st.Q.SetGithubPullChecks(ctx, db.SetGithubPullChecksParams{
			Checks: strPtr(next), ChecksSha: &pull.HeadSha, RepoID: repoID, Number: number,
		}); err != nil {
			return err
		}
		pull.Checks, pull.ChecksSha = strPtr(next), &pull.HeadSha
		live, err := s.st.Q.ListLivePullThreads(ctx, db.ListLivePullThreadsParams{RepoID: repoID, Number: number})
		if err != nil {
			return err
		}
		for _, l := range live {
			s.th.Publish(ctx, l.ThreadID)
		}
	}
	return s.onRollup(ctx, pull, prev, next, stored || fresh)
}

// onRollup 在一次 CI 汇总之后调用：prev / next 是汇总前后的 checks（空串 = 没有），changed 表示库里的值或
// checks_sha 变了、或关联刚建立（步骤 6.2 实现）。
func (s *Service) onRollup(context.Context, db.GithubPull, string, string, bool) error { return nil }
