package github

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 防回流与审计（M15 步骤 2.3，R7 / R23）。
//
// orb 的 GitHub 写操作都经出站代理：转发前 GitHubWriteBegin 把 (槽位, 类别, 目标, 正文哈希) 预登记进 github_self_writes，
// 所以 webhook 比上游响应先到时也能认出；响应后 GitHubWriteEnd 把 REST 响应里的补充键记进 github_self_keys，并为每个写操作
// （推送按 ref）记一行 github_audit。事件到达时 isSelfEvent 按 Agent 登录名、补充键、预登记三种方式判定。

// selfWriteCategories 是参与预登记的写操作类别：它们产生的事件会经 hook 或轮询回到 Oryn。
var selfWriteCategories = map[string]bool{
	egress.CatCommentCreate: true,
	egress.CatIssueCreate:   true,
	egress.CatIssueUpdate:   true,
	egress.CatPRCreate:      true,
	egress.CatPRUpdate:      true,
	egress.CatPRMerge:       true,
	egress.CatReview:        true,
	egress.CatReviewComment: true,
}

// GitHubWriteBegin 实现 orbs.GitHub：出站代理转发 orb 的 GitHub 写操作之前预登记防回流记录。
// 返回非空表示拒绝（门禁，步骤 5.4 接入；w.Unparsed 时门禁开启须拒绝）；目前恒放行。
func (s *Service) GitHubWriteBegin(ctx context.Context, orbID string, w egress.GitHubWrite) string {
	slot := s.orbSlot(ctx, orbID)
	s.trackSelfWrite(slot, 1)
	if !w.Unparsed && w.Target != "" && selfWriteCategories[w.Category] {
		var sha *string
		if w.BodySHA256 != "" {
			sha = &w.BodySHA256
		}
		if err := s.st.Q.InsertGithubSelfWrite(ctx, db.InsertGithubSelfWriteParams{
			Slot: slot, Category: w.Category, Target: w.Target, BodySha256: sha, CreatedAt: s.nowMs(),
		}); err != nil {
			slog.Warn("github: 预登记自身写入失败", "orb", orbID, "op", w.Op, "err", err)
		}
	}
	return ""
}

// GitHubWriteEnd 实现 orbs.GitHub：记下成功写操作的补充键，并写审计。
func (s *Service) GitHubWriteEnd(ctx context.Context, orbID string, w egress.GitHubWrite, status int, respKeys []string) {
	slot := s.orbSlot(ctx, orbID)
	if status/100 == 2 {
		s.rememberSelfKeys(ctx, respKeys)
	}
	// 补充键落库之后才算结束在途：正在等待复查的事件因此能看到它们
	s.trackSelfWrite(slot, -1)
	s.audit(ctx, orbID, slot, w, status)
}

// audit 为一个写操作记审计（R23）：推送每个 ref 一行，op 为 ref；仓库取不到时把 GraphQL 目标的 node id 记进 op。
func (s *Service) audit(ctx context.Context, orbID, slot string, w egress.GitHubWrite, status int) {
	row := db.InsertGithubAuditParams{
		At: s.nowMs(), ThreadID: s.orbThread(ctx, orbID), OrbID: orbID, Slot: slot, Method: w.Method, Op: w.Op,
		Repo: w.Repo, Status: int64(status), Gate: w.Gate,
	}
	if w.BodySHA256 != "" {
		row.BodySha256 = &w.BodySHA256
	}
	if row.Repo == "" && w.Target != "" && !strings.HasPrefix(w.Target, "/") {
		row.Op += " " + w.Target
	}
	rows := []db.InsertGithubAuditParams{row}
	if len(w.Refs) > 0 {
		rows = rows[:0]
		for _, u := range w.Refs {
			r := row
			r.Op = u.Ref
			rows = append(rows, r)
		}
	}
	for _, r := range rows {
		if err := s.st.Q.InsertGithubAudit(ctx, r); err != nil {
			slog.Warn("github: 记录审计失败", "orb", orbID, "op", r.Op, "err", err)
		}
	}
}

// orbSlot 返回 orb 的 GitHub 身份槽位（orb 读不到时按我的账号）。
func (s *Service) orbSlot(ctx context.Context, orbID string) string {
	if o, err := s.st.Q.GetOrb(ctx, orbID); err == nil && o.GithubIdentity == slotAgent {
		return slotAgent
	}
	return slotUser
}

// orbThread 返回 orb 上唯一未归档的工作线程；没有或不止一个时为 nil（审计的线程字段留空）。
func (s *Service) orbThread(ctx context.Context, orbID string) *string {
	ths, err := s.st.Q.ListThreadsByOrb(ctx, &orbID)
	if err != nil {
		slog.Warn("github: 读取 orb 的线程失败", "orb", orbID, "err", err)
		return nil
	}
	var id *string
	for _, th := range ths {
		if th.Kind != "work" || th.ArchivedAt != nil {
			continue
		}
		if id != nil {
			return nil
		}
		id = &th.ID
	}
	return id
}

// trackSelfWrite 增减槽位在途的写操作数。
func (s *Service) trackSelfWrite(slot string, d int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selfInflight == nil {
		s.selfInflight = map[string]int{}
	}
	if n := s.selfInflight[slot] + d; n > 0 {
		s.selfInflight[slot] = n
	} else {
		delete(s.selfInflight, slot)
	}
}

func (s *Service) selfWriteInflight(slot string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selfInflight[slot] > 0
}

// rememberSelfKeys 把自身写入的补充键（egress.RespKeys 的格式）记入 github_self_keys：出站代理转回的 REST 响应，
// 以及控制面自己写入 GitHub 时拿到的响应（如触发回执的回帖）。失败只记日志。
func (s *Service) rememberSelfKeys(ctx context.Context, keys []string) {
	now := s.nowMs()
	for _, k := range keys {
		if err := s.st.Q.InsertGithubSelfKey(ctx, db.InsertGithubSelfKeyParams{Key: k, CreatedAt: now}); err != nil {
			slog.Warn("github: 记录自身写入键失败", "key", k, "err", err)
		}
	}
}

// cleanupSelfWrites 清理超过保留期的预登记、补充键与审计（每小时，随 cleanup）。
func (s *Service) cleanupSelfWrites(ctx context.Context, now time.Time) {
	before := now.Add(-selfWriteRetention).UnixMilli()
	if err := s.st.Q.DeleteGithubSelfWritesBefore(ctx, before); err != nil && ctx.Err() == nil {
		slog.Warn("github: 清理自身写入记录失败", "err", err)
	}
	if err := s.st.Q.DeleteGithubSelfKeysBefore(ctx, before); err != nil && ctx.Err() == nil {
		slog.Warn("github: 清理自身写入键失败", "err", err)
	}
	if err := s.st.Q.DeleteGithubAuditBefore(ctx, now.Add(-auditRetention).UnixMilli()); err != nil && ctx.Err() == nil {
		slog.Warn("github: 清理审计记录失败", "err", err)
	}
}

// ---------- 事件判定 ----------

// selfProbe 是判断一个事件是否 agent 自身写入所需的信息（isSelfEvent）。
type selfProbe struct {
	sender   sender
	category string   // 事件对应的写操作类别（egress.Cat*）
	targets  []string // 预登记目标的候选：issue / PR / 仓库的 node_id，以及对应 REST 写请求的规范化路径
	keys     []string // 补充键（egress.RespKeys 的格式）
	body     string   // 事件正文；空 = 无正文的动作
}

// isSelfEvent 报告事件是不是 agent 自身写入造成的（R7），满足任一即是：
//   - 发送者是 Agent 槽位的登录名；
//   - 命中补充键（评论 / 评审 / 行内评论 id、node_id）；
//   - 发送者是某个槽位的登录名，且该槽位在事件目标上有同类别的预登记：有正文时正文哈希相等，无正文时在 selfWriteWindow 内。
//
// 发送者是槽位登录名但都没命中、而该槽位有写操作在途（响应还没回来，补充键未落库）时，按服务时钟延后 selfRecheckDelay 复查一次。
func (s *Service) isSelfEvent(ctx context.Context, ev selfProbe) bool {
	slot := ""
	if ev.sender.Login != "" {
		ids, err := s.st.Q.ListGithubIdentities(ctx)
		if err != nil {
			slog.Warn("github: 读取身份失败", "err", err)
		}
		for _, id := range ids {
			if strings.EqualFold(id.Login, ev.sender.Login) {
				slot = id.Slot
			}
		}
	}
	if slot == slotAgent {
		return true
	}
	if s.selfMatch(ctx, slot, ev) {
		return true
	}
	if slot == "" || !s.selfWriteInflight(slot) || !s.sleep(ctx, selfRecheckDelay) {
		return false
	}
	return s.selfMatch(ctx, slot, ev)
}

// selfMatch 查补充键与（slot 非空时）该槽位的预登记。
func (s *Service) selfMatch(ctx context.Context, slot string, ev selfProbe) bool {
	if len(ev.keys) > 0 {
		hit, err := s.st.Q.GithubSelfKeyExists(ctx, ev.keys)
		if err != nil {
			slog.Warn("github: 查询自身写入键失败", "err", err)
		} else if hit {
			return true
		}
	}
	if slot == "" || ev.category == "" {
		return false
	}
	now := s.now()
	hash := egress.BodyHash(ev.body)
	since := now.Add(-selfWriteRetention).UnixMilli()
	if hash == "" {
		since = now.Add(-selfWriteWindow).UnixMilli()
	}
	for _, t := range ev.targets {
		if t == "" {
			continue
		}
		rows, err := s.st.Q.ListGithubSelfWrites(ctx, db.ListGithubSelfWritesParams{Target: t, Category: ev.category, Since: since})
		if err != nil {
			slog.Warn("github: 查询自身写入记录失败", "err", err)
			continue
		}
		for _, r := range rows {
			if r.Slot == slot && (hash == "" || r.BodySha256 != nil && *r.BodySha256 == hash) {
				return true
			}
		}
	}
	return false
}

// sleep 按服务时钟（cfg.AfterFunc，测试可注入）等待 d；ctx 先结束时返回 false。
func (s *Service) sleep(ctx context.Context, d time.Duration) bool {
	done := make(chan struct{})
	stop := s.cfg.AfterFunc(d, func() { close(done) })
	select {
	case <-done:
		return true
	case <-ctx.Done():
		stop()
		return false
	}
}

// listenProbe 是回流事件（listen 的 kind：comment / review / review_comment）的探针。
func listenProbe(ev hookEvent, kind string) selfProbe {
	switch kind {
	case "review":
		return reviewProbe(ev)
	case "review_comment":
		return reviewCommentProbe(ev)
	}
	return commentProbe(ev)
}

// repoPrefixes 是事件仓库在 REST 写请求里的两种路径前缀（与 egress 识别器的规范化一致：owner/repo 小写）。
func repoPrefixes(r repoPayload) []string {
	out := make([]string, 0, 2)
	if r.FullName != "" {
		out = append(out, "/repos/"+strings.ToLower(r.FullName))
	}
	if r.ID != 0 {
		out = append(out, "/repositories/"+strconv.FormatInt(r.ID, 10))
	}
	return out
}

// restTargets 把 tail（如 "/issues/7/comments"）接到仓库的各个路径前缀上。
func restTargets(r repoPayload, tail string) []string {
	pre := repoPrefixes(r)
	for i := range pre {
		pre[i] += tail
	}
	return pre
}

// objectKeys 是一个对象的补充键：<prefix><id> 与 node:<node_id>（缺省的跳过）。
func objectKeys(prefix string, id int64, nodeID string) []string {
	var keys []string
	if id != 0 {
		keys = append(keys, prefix+strconv.FormatInt(id, 10))
	}
	if nodeID != "" {
		keys = append(keys, "node:"+nodeID)
	}
	return keys
}

// commentProbe：issue / PR 的会话评论（addComment 的 subjectId，或 POST …/issues/{n}/comments）。
func commentProbe(ev hookEvent) selfProbe {
	p := selfProbe{
		sender: senderOf(ev.Sender), category: egress.CatCommentCreate, body: ev.Comment.Body,
		keys: objectKeys("comment:", ev.Comment.ID, ev.Comment.NodeID),
	}
	if ev.Issue != nil {
		p.targets = append([]string{ev.Issue.NodeID}, restTargets(ev.Repository, fmt.Sprintf("/issues/%d/comments", ev.Issue.Number))...)
	}
	return p
}

// reviewProbe：提交的评审（addPullRequestReview 的 pullRequestId，或 POST …/pulls/{n}/reviews[/{id}/events]）。
func reviewProbe(ev hookEvent) selfProbe {
	p := selfProbe{sender: senderOf(ev.Sender), category: egress.CatReview, keys: objectKeys("review:", ev.Review.ID, ev.Review.NodeID)}
	if ev.Review.Body != nil {
		p.body = *ev.Review.Body
	}
	if pr := ev.PullRequest; pr != nil {
		p.targets = append([]string{pr.NodeID}, restTargets(ev.Repository, fmt.Sprintf("/pulls/%d/reviews", pr.Number))...)
		if ev.Review.ID != 0 {
			p.targets = append(p.targets, restTargets(ev.Repository, fmt.Sprintf("/pulls/%d/reviews/%d/events", pr.Number, ev.Review.ID))...)
		}
	}
	return p
}

// reviewCommentProbe：行内评审评论。随评审一次提交的行内评论（POST …/pulls/{n}/reviews 带 comments，R26）
// 靠所属评审的补充键 review:<pull_request_review_id> 认出。
func reviewCommentProbe(ev hookEvent) selfProbe {
	c := ev.Comment
	p := selfProbe{sender: senderOf(ev.Sender), category: egress.CatReviewComment, body: c.Body, keys: objectKeys("review_comment:", c.ID, c.NodeID)}
	if c.ReviewID != 0 {
		p.keys = append(p.keys, "review:"+strconv.FormatInt(c.ReviewID, 10))
	}
	if pr := ev.PullRequest; pr != nil {
		p.targets = append([]string{pr.NodeID}, restTargets(ev.Repository, fmt.Sprintf("/pulls/%d/comments", pr.Number))...)
		if c.InReplyTo != 0 {
			p.targets = append(p.targets, restTargets(ev.Repository, fmt.Sprintf("/pulls/%d/comments/%d/replies", pr.Number, c.InReplyTo))...)
		}
	}
	return p
}

// issueProbe：issues 事件。opened 对应建 issue（createIssue 的 repositoryId，或 POST …/issues），其余动作对应改 issue
// （按 issue 的 node_id 或 PATCH …/issues/{n}、…/labels），无正文，按 selfWriteWindow 匹配。
func issueProbe(ev hookEvent, action string) selfProbe {
	p := selfProbe{sender: senderOf(ev.Sender)}
	is := ev.Issue
	if is == nil {
		return p
	}
	if action == "opened" {
		p.category, p.body, p.keys = egress.CatIssueCreate, is.body(), objectKeys("", 0, is.NodeID)
		p.targets = append([]string{ev.Repository.NodeID}, restTargets(ev.Repository, "/issues")...)
		return p
	}
	p.category = egress.CatIssueUpdate
	p.targets = append([]string{is.NodeID}, restTargets(ev.Repository, fmt.Sprintf("/issues/%d", is.Number))...)
	if action == "labeled" || action == "unlabeled" {
		p.targets = append(p.targets, restTargets(ev.Repository, fmt.Sprintf("/issues/%d/labels", is.Number))...)
	}
	return p
}
