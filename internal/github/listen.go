package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

func init() {
	on("pull_request_review", (*Service).onReview)
	on("pull_request_review_comment", (*Service).onReviewComment)
	on("issue_comment", (*Service).onIssueComment)
}

// hookIssue 是事件载荷里的 issue 或 pull_request（本包用到的字段）。
type hookIssue struct {
	Number  int64         `json:"number"`
	NodeID  string        `json:"node_id"`
	Title   string        `json:"title"`
	Body    *string       `json:"body"`
	HTMLURL string        `json:"html_url"`
	User    senderPayload `json:"user"`
	// AuthorAssociation 是作者与仓库的关系（OWNER / MEMBER / COLLABORATOR / CONTRIBUTOR / NONE …）。
	AuthorAssociation string `json:"author_association"`
	Labels            []struct {
		Name string `json:"name"`
	} `json:"labels"`
	// PullRequest 只在 issue 视图出现：非空表示这个 issue 是 PR。
	PullRequest json.RawMessage `json:"pull_request"`
}

func (i *hookIssue) isPull() bool {
	return len(i.PullRequest) > 0 && string(i.PullRequest) != "null"
}

func (i *hookIssue) body() string {
	if i.Body == nil {
		return ""
	}
	return *i.Body
}

// hookEvent 是评审、评论与 issue 事件载荷里本包用到的字段。
type hookEvent struct {
	Repository  repoPayload   `json:"repository"`
	Sender      senderPayload `json:"sender"`
	PullRequest *hookIssue    `json:"pull_request"`
	Issue       *hookIssue    `json:"issue"`
	Review      struct {
		ID                int64   `json:"id"`
		NodeID            string  `json:"node_id"`
		Body              *string `json:"body"`
		State             string  `json:"state"`
		AuthorAssociation string  `json:"author_association"`
	} `json:"review"`
	Comment struct {
		ID                int64  `json:"id"`
		NodeID            string `json:"node_id"`
		Body              string `json:"body"`
		Path              string `json:"path"`
		Line              *int   `json:"line"`
		OriginalLine      *int   `json:"original_line"`
		AuthorAssociation string `json:"author_association"`
		// InReplyTo / ReviewID 只在行内评审评论出现（防回流用）。
		InReplyTo int64 `json:"in_reply_to_id"`
		ReviewID  int64 `json:"pull_request_review_id"`
	} `json:"comment"`
	Label *struct {
		Name string `json:"name"`
	} `json:"label"`
}

// listenTarget 是一条回流的目标线程。
type listenTarget struct {
	threadID  string
	pull      bool // 来自 thread_pulls（否则来自 thread_issues）
	listening bool
}

// pullTarget 取 PR 关联的目标线程；没有返回 ok = false。
func (s *Service) pullTarget(ctx context.Context, repoID, number int64) (listenTarget, bool, error) {
	row, err := s.st.Q.PullTargetThread(ctx, db.PullTargetThreadParams{RepoID: repoID, Number: number})
	if err != nil {
		if isNoRows(err) {
			return listenTarget{}, false, nil
		}
		return listenTarget{}, false, err
	}
	return listenTarget{threadID: row.ThreadID, pull: true, listening: row.Listening}, true, nil
}

// issueTarget 取触发规则建立的 issue 关联线程；线程已归档视为没有。
func (s *Service) issueTarget(ctx context.Context, repoID, number int64) (listenTarget, bool, error) {
	row, err := s.st.Q.GetThreadIssue(ctx, db.GetThreadIssueParams{RepoID: repoID, Number: number})
	if err != nil {
		if isNoRows(err) {
			return listenTarget{}, false, nil
		}
		return listenTarget{}, false, err
	}
	if row.ArchivedAt != nil {
		return listenTarget{}, false, nil
	}
	return listenTarget{threadID: row.ThreadID, listening: true}, true, nil
}

// decodeEvent 解析载荷并做公共前置：准入见 admitted（编号取载荷里的 PR 或 issue）。
func (s *Service) decodeEvent(ctx context.Context, body []byte) (hookEvent, string, error) {
	var ev hookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return ev, "", err
	}
	var number int64
	switch {
	case ev.PullRequest != nil:
		number = ev.PullRequest.Number
	case ev.Issue != nil:
		number = ev.Issue.Number
	}
	ok, err := s.admitted(ctx, ev.Repository.ID, number)
	if err != nil {
		return ev, "", err
	}
	if !ok {
		return ev, "ignored:unbound_repo", nil
	}
	return ev, "", nil
}

// admitted 是事件的准入条件（M15 步骤 3.1）：仓库已绑定（有未归档项目），或 (repoID, number) 有未取消、线程未归档的
// PR 关联或 issue 关联（如向上游开的 PR、无项目线程的 PR）。
func (s *Service) admitted(ctx context.Context, repoID, number int64) (bool, error) {
	prjs, err := s.st.Q.ListProjectsByGithubRepo(ctx, &repoID)
	if err != nil || len(prjs) > 0 {
		return err == nil, err
	}
	if number == 0 {
		return false, nil
	}
	n, err := s.st.Q.CountLivePullLinks(ctx, db.CountLivePullLinksParams{RepoID: repoID, Number: number})
	if err != nil || n > 0 {
		return err == nil, err
	}
	_, ok, err := s.issueTarget(ctx, repoID, number)
	return ok, err
}

func (s *Service) onReview(ctx context.Context, action string, body []byte) (string, error) {
	if action != "submitted" {
		return "ignored:action", nil
	}
	ev, skip, err := s.decodeEvent(ctx, body)
	if err != nil || skip != "" {
		return skip, err
	}
	if ev.PullRequest == nil {
		return "ignored:payload", nil
	}
	text := ""
	if ev.Review.Body != nil {
		text = *ev.Review.Body
	}
	state := strings.ToLower(ev.Review.State)
	if text == "" && state == "commented" {
		return "ignored:empty", nil // 行内评论各自有事件
	}
	tgt, ok, err := s.pullTarget(ctx, ev.Repository.ID, ev.PullRequest.Number)
	if err != nil || !ok {
		return "ignored:no_thread", err
	}
	return s.listen(ctx, ev, tgt, "review", ev.PullRequest, true, outboxItem{State: state, Body: text})
}

func (s *Service) onReviewComment(ctx context.Context, action string, body []byte) (string, error) {
	if action != "created" {
		return "ignored:action", nil
	}
	ev, skip, err := s.decodeEvent(ctx, body)
	if err != nil || skip != "" {
		return skip, err
	}
	if ev.PullRequest == nil {
		return "ignored:payload", nil
	}
	tgt, ok, err := s.pullTarget(ctx, ev.Repository.ID, ev.PullRequest.Number)
	if err != nil || !ok {
		return "ignored:no_thread", err
	}
	line := ev.Comment.Line
	if line == nil {
		line = ev.Comment.OriginalLine
	}
	it := outboxItem{Path: ev.Comment.Path, Body: ev.Comment.Body}
	if line != nil {
		it.Line = *line
	}
	return s.listen(ctx, ev, tgt, "review_comment", ev.PullRequest, true, it)
}

func (s *Service) onIssueComment(ctx context.Context, action string, body []byte) (string, error) {
	if action != "created" {
		return "ignored:action", nil
	}
	ev, skip, err := s.decodeEvent(ctx, body)
	if err != nil || skip != "" {
		return skip, err
	}
	if ev.Issue == nil {
		return "ignored:payload", nil
	}
	repoID, number := ev.Repository.ID, ev.Issue.Number
	var tgt listenTarget
	ok := false
	if ev.Issue.isPull() {
		if tgt, ok, err = s.pullTarget(ctx, repoID, number); err != nil {
			return "", err
		}
	}
	if !ok {
		// issue，或 PR 没有关联（如触发规则在 fork PR 上建的线程）：看 issue 关联
		if tgt, ok, err = s.issueTarget(ctx, repoID, number); err != nil {
			return "", err
		}
	}
	if !ok {
		return s.onUntargetedComment(ctx, ev)
	}
	return s.listen(ctx, ev, tgt, "comment", ev.Issue, ev.Issue.isPull(), outboxItem{Body: ev.Comment.Body})
}

// listen 对一条评审 / 评论做准入，通过后写进回流队列。subject 是载荷里的 PR 或 issue。
func (s *Service) listen(ctx context.Context, ev hookEvent, tgt listenTarget, kind string, subject *hookIssue, isPull bool, it outboxItem) (string, error) {
	th, err := s.st.Q.GetThread(ctx, tgt.threadID)
	if err != nil {
		if isNoRows(err) {
			return "ignored:no_thread", nil
		}
		return "", err
	}
	// 项目取目标线程自己的项目：多个项目绑同一仓库时开关不能取错；无项目线程（M15 步骤 3.1）的开关都取默认开启
	listenComments, listenReviews := true, true
	if th.ProjectID != nil {
		prj, err := s.st.Q.GetProject(ctx, *th.ProjectID)
		if err != nil {
			if isNoRows(err) {
				return "ignored:no_thread", nil
			}
			return "", err
		}
		listenComments, listenReviews = prj.GithubListenComments, prj.GithubListenReviews
	}
	who := senderOf(ev.Sender)
	if s.isSelfEvent(ctx, listenProbe(ev, kind)) {
		return "ignored:self", nil
	}
	if kind == "comment" {
		// R18：期间有任何人（不是 agent 自己）发新评论，待执行的自动关闭同步取消
		s.cancelAutoclose(ctx, ev.Repository.ID, subject.Number, autocloseCommented)
	}
	// 评审取 review 的 author_association，评论取 comment 的
	assoc := ev.Comment.AuthorAssociation
	if kind == "review" {
		assoc = ev.Review.AuthorAssociation
	}
	// 驱动判定按线程来源规则的档位（R13）；没有来源规则的线程只认写权限者
	rule, pool := defaultDriveRule, []apigen.ModelRef(nil)
	if trig, ok, err := s.threadRule(ctx, tgt.threadID); err != nil {
		return "", err
	} else if ok {
		rule, pool = ruleOf(trig), modelsOf(trig)
	}
	d, err := s.canDrive(ctx, ev.Repository.FullName, who, subject.User.Login, assoc, rule)
	if err != nil {
		return "", err
	}
	if !d.ok {
		return "ignored:" + d.reason, nil
	}
	// issue 任务：评论授权实现（R16）；终态任务只接受维护者 @Agent 的重开指令，其余评论回一条固定回帖（R15）
	if out, err := s.taskComment(ctx, ev, tgt.threadID, kind, subject.Number, it.Body, assoc, rule); err != nil || out != "" {
		return out, err
	}
	listen := listenComments
	if kind == "review" || kind == "review_comment" {
		listen = listenReviews
	}
	if !listen || tgt.pull && !tgt.listening {
		return "ignored:muted", nil
	}
	repoID, number := ev.Repository.ID, subject.Number
	now := s.now()
	n, err := s.st.Q.CountGithubOutboxSince(ctx, db.CountGithubOutboxSinceParams{
		ThreadID: tgt.threadID, RepoID: repoID, Number: number, Since: now.Add(-time.Hour).UnixMilli(),
	})
	if err != nil {
		return "", err
	}
	if n >= maxPerLinkPerHour {
		if tgt.pull {
			s.pauseLink(ctx, tgt.threadID, ev.Repository.FullName, repoID, number)
		}
		return "ignored:rate_limited", nil
	}
	text := it.Body
	it.Actor = ev.Sender.Login
	it.Body = clip(it.Body, maxItemBody)
	raw, err := json.Marshal(it)
	if err != nil {
		return "", err
	}
	if err := s.st.Q.InsertGithubOutbox(ctx, db.InsertGithubOutboxParams{
		ID: ids.New("gho"), ThreadID: tgt.threadID, RepoID: repoID, Number: number, IsPull: isPull,
		Title: subject.Title, Url: subject.HTMLURL, Kind: kind, Item: string(raw),
		CreatedAt: now.UnixMilli(), DeliverAfter: now.Add(s.cfg.Debounce).UnixMilli(),
	}); err != nil {
		return "", err
	}
	s.applyPragmas(ctx, tgt.threadID, text, ev.Sender.Login, assoc, rule, pool)
	return "handled", nil
}

// threadRule 取线程的来源规则：线程最近一条 issue 任务的 rule_id（缺省取 trigger_id）。没有任务或规则已删除时 ok 为假。
func (s *Service) threadRule(ctx context.Context, threadID string) (db.GithubTrigger, bool, error) {
	ti, err := s.st.Q.GetThreadIssueByThread(ctx, threadID)
	if err != nil {
		if isNoRows(err) {
			return db.GithubTrigger{}, false, nil
		}
		return db.GithubTrigger{}, false, err
	}
	id := ti.RuleID
	if id == nil {
		id = ti.TriggerID
	}
	if id == nil {
		return db.GithubTrigger{}, false, nil
	}
	t, err := s.st.Q.GetGithubTrigger(ctx, *id)
	if err != nil {
		if isNoRows(err) {
			return db.GithubTrigger{}, false, nil
		}
		return db.GithubTrigger{}, false, err
	}
	return t, true, nil
}

// pauseLink 在 PR 关联超出每小时上限时暂停它，并通知所有者。
func (s *Service) pauseLink(ctx context.Context, threadID, repo string, repoID, number int64) {
	if _, err := s.st.Q.SetThreadPullListening(ctx, db.SetThreadPullListeningParams{
		Listening: false, ThreadID: threadID, RepoID: repoID, Number: number,
	}); err != nil {
		slog.Warn("github: 暂停 PR 关联失败", "thread", threadID, "err", err)
		return
	}
	if s.n != nil {
		body := fmt.Sprintf("Too many events on %s#%d in the last hour; event delivery for this pull request is paused. Resume it from the pull request chip.", repo, number)
		if err := s.n.PostThread(ctx, threadID, "error", "GitHub events paused: "+body); err != nil {
			slog.Warn("github: 发送暂停通知失败", "thread", threadID, "err", err)
		}
	}
	s.th.Publish(ctx, threadID)
}

type outboxKey struct {
	threadID string
	repoID   int64
	number   int64
}

// flush 投递到期的回流：同一 (线程, 仓库, 编号) 的全部未投递行（含未到期的）合成一条自动消息。
func (s *Service) flush(ctx context.Context) {
	now := s.now()
	due, err := s.st.Q.ListDueGithubOutbox(ctx, now.UnixMilli())
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("github: 读取到期回流失败", "err", err)
		}
		return
	}
	var keys []outboxKey
	for _, r := range due {
		k := outboxKey{r.ThreadID, r.RepoID, r.Number}
		if len(keys) == 0 || keys[len(keys)-1] != k {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		s.flushKey(ctx, k, now)
	}
}

func (s *Service) flushKey(ctx context.Context, k outboxKey, now time.Time) {
	rows, err := s.st.Q.ListPendingGithubOutboxByKey(ctx, db.ListPendingGithubOutboxByKeyParams{
		ThreadID: k.threadID, RepoID: k.repoID, Number: k.number,
	})
	if err != nil || len(rows) == 0 {
		if err != nil && ctx.Err() == nil {
			slog.Warn("github: 读取回流行失败", "thread", k.threadID, "err", err)
		}
		return
	}
	repo := repoFromURL(rows[len(rows)-1].Url)
	// 带标签：回合正常结束时驱动完成提醒（R25，见 TurnObserver）
	_, err = s.th.Deliver(ctx, k.threadID, threads.AutoMessage{Text: composeEvent(repo, k.number, rows), Tag: newTurnTag()}, nil)
	var te *threads.Error
	switch {
	case err == nil:
	case errors.As(err, &te) && (te.Code == "not_found" || te.Code == "archived"):
		// 线程没了：丢弃
	case rows[0].CreatedAt < now.Add(-outboxMaxAge).UnixMilli():
		slog.Warn("github: 回流超过最长保留时间，丢弃", "thread", k.threadID, "repo", repo, "number", k.number, "err", err)
	default:
		if ctx.Err() == nil {
			slog.Info("github: 回流投递失败，下一轮重试", "thread", k.threadID, "err", err)
		}
		return
	}
	idList := make([]string, len(rows))
	for i, r := range rows {
		idList[i] = r.ID
	}
	at := now.UnixMilli()
	if err := s.st.Q.MarkGithubOutboxDelivered(ctx, db.MarkGithubOutboxDeliveredParams{DeliveredAt: &at, Ids: idList}); err != nil {
		slog.Warn("github: 标记回流已投递失败", "thread", k.threadID, "err", err)
	}
}
