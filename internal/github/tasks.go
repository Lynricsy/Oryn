package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// issue 线程的任务（M15 R15 / R16 / R25，步骤 4.3）：状态机、终态应答、实现授权、任务时限与完成提醒。

// 任务状态（R15）。终态见 issueTaskTerminal。
const (
	taskNew         = "new"
	taskReproducing = "reproducing"
	taskFixing      = "fixing"
	taskReviewing   = "reviewing"
	taskOpened      = "opened"
	taskNeedsInfo   = "needs_info"
	taskMerged      = "merged"
	taskClosed      = "closed"
	taskAbandoned   = "abandoned"
)

var taskStates = map[string]bool{
	taskNew: true, taskReproducing: true, taskFixing: true, taskReviewing: true, taskOpened: true, taskNeedsInfo: true,
	taskMerged: true, taskClosed: true, taskAbandoned: true,
}

// implAuthLabel 是授权实现的标签（R16）。
const implAuthLabel = "accepted"

// goAhead 是授权实现的评论用语（R16，不区分大小写）。
var goAhead = regexp.MustCompile(`(?i)\bgo ahead\b`)

// turnTagPrefix 是 GitHub 投递给线程的自动消息的标签前缀：只有这些回合的结束（Observer.TurnEnded）驱动完成提醒。
const turnTagPrefix = "github:"

func newTurnTag() string { return turnTagPrefix + ids.New("turn") }

// SetTaskState 把线程的 issue 任务置为 state（R15）；note 记进日志。进入终态时冻结线程的 orb。线程没有任务时回 not_found。
func (s *Service) SetTaskState(ctx context.Context, threadID, state, note string) error {
	if !taskStates[state] {
		return errf("invalid_request", "unknown task state "+state)
	}
	ti, err := s.st.Q.GetThreadIssueByThread(ctx, threadID)
	if err != nil {
		if isNoRows(err) {
			return errf("not_found", "thread has no GitHub task")
		}
		return err
	}
	return s.setTask(ctx, ti, state, note)
}

// CleanupTask 把 repo（owner/name）#number 的任务置为 abandoned 并冻结 orb（R24 的 cleanup）。
func (s *Service) CleanupTask(ctx context.Context, repo string, number int) error {
	all, err := s.st.Q.ListThreadIssues(ctx)
	if err != nil {
		return err
	}
	names := map[int64]string{}
	for _, ti := range all {
		if ti.Number != int64(number) {
			continue
		}
		full, ok := names[ti.RepoID]
		if !ok {
			if full, err = s.repoFullName(ctx, ti.RepoID); err != nil {
				return err
			}
			names[ti.RepoID] = full
		}
		if strings.EqualFold(full, repo) {
			return s.setTask(ctx, ti, taskAbandoned, "cleanup")
		}
	}
	return errf("not_found", "no GitHub task for "+repo+"#"+fmt.Sprint(number))
}

// setTask 写入状态并发布线程。目标是终态时取消该线程待执行的自动关闭（R18），并排队冻结线程的 orb（已冻结时无副作用）。
func (s *Service) setTask(ctx context.Context, ti db.ThreadIssue, state, note string) error {
	return s.setTaskFreeze(ctx, ti, state, note, false)
}

// setTaskFreeze 同 setTask；afterTurn 为真时（调用方是该线程自己的工具调用，如 github_abort_task）不立即冻结，而是登记到
// freezeAfterTurn，等线程离开 working 再冻结（见 turnObserver.StateChanged）：工具应答还要经 ctl 响应、orbd 转给 omp，
// 任务队列里的冻结随时可能先执行，截断这条应答。
func (s *Service) setTaskFreeze(ctx context.Context, ti db.ThreadIssue, state, note string, afterTurn bool) error {
	if _, err := s.st.Q.SetThreadIssueState(ctx, db.SetThreadIssueStateParams{State: state, ThreadID: ti.ThreadID}); err != nil {
		return err
	}
	slog.Info("github: 任务状态变化", "thread", ti.ThreadID, "number", ti.Number, "from", ti.State, "to", state, "note", note)
	if issueTaskTerminal(state) {
		if _, err := s.st.Q.CancelGithubAutoclosesByThread(ctx, db.CancelGithubAutoclosesByThreadParams{Outcome: &state, ThreadID: ti.ThreadID}); err != nil {
			slog.Warn("github: 取消自动关闭失败", "thread", ti.ThreadID, "err", err)
		}
		if afterTurn {
			s.mu.Lock()
			if s.freezeAfterTurn == nil {
				s.freezeAfterTurn = map[string]time.Time{}
			}
			s.freezeAfterTurn[ti.ThreadID] = s.now()
			s.mu.Unlock()
		} else {
			s.queueFreeze(ti.ThreadID)
		}
	}
	s.th.Publish(ctx, ti.ThreadID)
	s.changed()
	return nil
}

// queueFreeze 排队冻结线程的 orb；队列满时记日志。
func (s *Service) queueFreeze(threadID string) {
	if !s.enqueue(func(ctx context.Context) { s.freezeThread(ctx, threadID) }) {
		slog.Warn("github: 任务队列已满，没有冻结 orb", "thread", threadID)
	}
}

// onThreadLeftWorking 在线程离开 working 时调用：登记过回合结束后冻结的，此时排队冻结。
func (s *Service) onThreadLeftWorking(threadID string) {
	s.mu.Lock()
	_, due := s.freezeAfterTurn[threadID]
	delete(s.freezeAfterTurn, threadID)
	s.mu.Unlock()
	if due {
		s.queueFreeze(threadID)
	}
}

// scanFreezeAfterTurn 是「回合结束后冻结」的兜底：登记超过 freezeAfterTurnGrace 仍没离开 working 的线程，中止当前回合
// 并直接冻结（不再等 StateChanged）。此时工具应答早已送达，冻结不会截断它。
func (s *Service) scanFreezeAfterTurn(ctx context.Context) {
	cutoff := s.now().Add(-freezeAfterTurnGrace)
	var overdue []string
	s.mu.Lock()
	for threadID, at := range s.freezeAfterTurn {
		if !at.After(cutoff) {
			overdue = append(overdue, threadID)
			delete(s.freezeAfterTurn, threadID)
		}
	}
	s.mu.Unlock()
	for _, threadID := range overdue {
		slog.Warn("github: 放弃任务后回合迟迟未结束，中止并冻结 orb", "thread", threadID, "grace", freezeAfterTurnGrace)
		if err := s.th.Abort(ctx, threadID); err != nil {
			slog.Warn("github: 中止回合失败", "thread", threadID, "err", err)
		}
		s.freezeThread(ctx, threadID)
	}
}

// finishTask 让 (repoID, number) 的任务因 GitHub 上的变化（issue 关闭、PR 合并或关闭）进入终态；已在终态（如先合并、
// 后因 Fixes #N 关闭）时不覆盖。返回是否有任务。
func (s *Service) finishTask(ctx context.Context, repoID, number int64, state string) (bool, error) {
	row, err := s.st.Q.GetThreadIssue(ctx, db.GetThreadIssueParams{RepoID: repoID, Number: number})
	if err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, err
	}
	if issueTaskTerminal(row.State) {
		return true, nil
	}
	return true, s.setTask(ctx, threadIssueOf(row), state, "github")
}

// finishThreadTask 让线程（PR 合并时的关联线程）的任务进入终态；没有任务或已在终态时什么也不做。失败只记日志。
func (s *Service) finishThreadTask(ctx context.Context, threadID, state string) {
	ti, err := s.st.Q.GetThreadIssueByThread(ctx, threadID)
	if err != nil {
		if !isNoRows(err) {
			slog.Warn("github: 读取线程任务失败", "thread", threadID, "err", err)
		}
		return
	}
	if issueTaskTerminal(ti.State) {
		return
	}
	if err := s.setTask(ctx, ti, state, "github"); err != nil {
		slog.Warn("github: 更新线程任务失败", "thread", threadID, "err", err)
	}
}

func threadIssueOf(r db.GetThreadIssueRow) db.ThreadIssue {
	return db.ThreadIssue{
		RepoID: r.RepoID, Number: r.Number, ThreadID: r.ThreadID, TriggerID: r.TriggerID, CreatedAt: r.CreatedAt, State: r.State,
		Classification: r.Classification, ImplAuthorized: r.ImplAuthorized, ReviewOnly: r.ReviewOnly, RuleID: r.RuleID,
		DeadlineAt: r.DeadlineAt, Reminders: r.Reminders,
	}
}

// freezeThread 冻结线程的 orb（R15）；没有 orb 能力、线程没有 orb 或冻结失败时只记日志。
func (s *Service) freezeThread(ctx context.Context, threadID string) {
	if s.orbs == nil {
		return
	}
	th, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil || th.OrbID == nil {
		return
	}
	if err := s.orbs.Freeze(ctx, *th.OrbID); err != nil {
		slog.Warn("github: 任务进入终态，冻结 orb 失败", "thread", threadID, "orb", *th.OrbID, "err", err)
	}
}

// selfWrite 以槽位 slot 直连 GitHub 做一次写操作（控制面自己的写入，不经出站代理）：先预登记 (类别, 目标, 正文)，
// webhook 比响应先到时也能认出；响应里的补充键随后记下。targets 是事件探针会比对的目标（如 REST 写请求的规范化路径）。
func (s *Service) selfWrite(ctx context.Context, slot, method, path, category string, targets []string, body map[string]any, text string) (json.RawMessage, error) {
	if _, err := s.tokenFor(ctx, slot); err != nil { // 槽位不可用时不预登记
		return nil, err
	}
	var sha *string
	if h := egress.BodyHash(text); h != "" {
		sha = &h
	}
	for _, t := range targets {
		if err := s.st.Q.InsertGithubSelfWrite(ctx, db.InsertGithubSelfWriteParams{
			Slot: slot, Category: category, Target: t, BodySha256: sha, CreatedAt: s.nowMs(),
		}); err != nil {
			slog.Warn("github: 预登记自身写入失败", "path", path, "err", err)
		}
	}
	var resp json.RawMessage
	if err := s.callAs(ctx, slot, method, path, body, &resp); err != nil {
		return nil, err
	}
	s.rememberSelfKeys(ctx, egress.RespKeys(resp))
	return resp, nil
}

// issuePath 是仓库 issue 的 REST 规范化路径（与出站代理识别器一致：owner/repo 小写）。
func issuePath(repo string, number int64, tail string) string {
	return fmt.Sprintf("/repos/%s/issues/%d%s", strings.ToLower(repo), number, tail)
}

// replyFinalized 在终态任务的 issue 下回一条固定回帖（R15，正文移植自 robomp）。失败只记日志。
func (s *Service) replyFinalized(ctx context.Context, threadID, repo string, number int64) {
	text := finalizedReply()
	path := issuePath(repo, number, "/comments")
	if _, err := s.selfWrite(ctx, s.threadSlot(ctx, threadID), http.MethodPost, path, egress.CatCommentCreate, []string{path},
		map[string]any{"body": text}, text); err != nil {
		slog.Warn("github: 终态回帖失败", "thread", threadID, "err", err)
	}
}

// mentionsAgent 报告正文是否 @ 了 Agent 账号；Agent 账号未连接时为否。
func (s *Service) mentionsAgent(ctx context.Context, body string) (bool, error) {
	agent, err := s.st.Q.GetGithubIdentity(ctx, slotAgent)
	switch {
	case err == nil:
		re := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9-])@` + regexp.QuoteMeta(agent.Login) + `($|[^A-Za-z0-9-])`)
		return re.MatchString(body), nil
	case isNoRows(err):
		return false, nil
	}
	return false, err
}

// maintainer 报告 login（assoc 是其这条内容的 author_association）是不是任务的维护者：我的账号、OWNER / MEMBER /
// COLLABORATOR 或规则维护者。
func (s *Service) maintainer(ctx context.Context, login, assoc string, rule driveRule) (bool, error) {
	if rule.pragmaAllowed(login, assoc) {
		return true, nil
	}
	return s.isUserLogin(ctx, login)
}

// implAuthorizer 报告 login 能否授权实现（R16）：OWNER（author_association，或仓库 owner 本人、我的账号）或规则维护者。
func (s *Service) implAuthorizer(ctx context.Context, repo, login, assoc string, rule driveRule) (bool, error) {
	owner, _, _ := strings.Cut(repo, "/")
	if strings.EqualFold(assoc, "OWNER") || login != "" && strings.EqualFold(owner, login) || rule.isMaintainer(login) {
		return true, nil
	}
	return s.isUserLogin(ctx, login)
}

// authorizeImpl 记下 (repoID, number) 的任务已获实现授权（出站代理按任务现查，不需通知）。
func (s *Service) authorizeImpl(ctx context.Context, repoID, number int64, by string) error {
	n, err := s.st.Q.SetThreadIssueImplAuthorized(ctx, db.SetThreadIssueImplAuthorizedParams{ImplAuthorized: true, RepoID: repoID, Number: number})
	if err == nil && n > 0 {
		slog.Info("github: 任务获实现授权", "repo", repoID, "number", number, "by", by)
	}
	return err
}

// labelAuthorize 处理任务 issue 上的 accepted 标签（R16）：打标签的人能授权时记下实现授权并返回 true。
func (s *Service) labelAuthorize(ctx context.Context, ev hookEvent) (bool, error) {
	row, err := s.st.Q.GetThreadIssue(ctx, db.GetThreadIssueParams{RepoID: ev.Repository.ID, Number: ev.Issue.Number})
	if err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, err
	}
	rule := defaultDriveRule
	if trig, ok, err := s.threadRule(ctx, row.ThreadID); err != nil {
		return false, err
	} else if ok {
		rule = ruleOf(trig)
	}
	ok, err := s.implAuthorizer(ctx, ev.Repository.FullName, ev.Sender.Login, "", rule)
	if err != nil || !ok {
		return false, err
	}
	return true, s.authorizeImpl(ctx, row.RepoID, row.Number, ev.Sender.Login)
}

// taskComment 是回流评论对线程 issue 任务的作用（listen 在驱动判定通过后调用）；返回非空表示这条评论到此为止。
//   - 评论含 go ahead、作者能授权时记下实现授权（R16）；
//   - 任务在终态时：维护者 @Agent 账号的指令重开任务（状态回到 new），评论照常回流、唤醒线程；其余评论不回流，
//     会话评论（kind = comment）在所在 issue / PR 下收到一条固定回帖，记 ignored:finalized（R15）。
func (s *Service) taskComment(ctx context.Context, ev hookEvent, threadID, kind string, number int64, text, assoc string, rule driveRule) (string, error) {
	ti, err := s.st.Q.GetThreadIssueByThread(ctx, threadID)
	if err != nil {
		if isNoRows(err) {
			return "", nil
		}
		return "", err
	}
	login := ev.Sender.Login
	if !ti.ImplAuthorized && goAhead.MatchString(text) {
		ok, err := s.implAuthorizer(ctx, ev.Repository.FullName, login, assoc, rule)
		if err != nil {
			return "", err
		}
		if ok {
			if err := s.authorizeImpl(ctx, ti.RepoID, ti.Number, login); err != nil {
				return "", err
			}
		}
	}
	if !issueTaskTerminal(ti.State) {
		return "", nil
	}
	directive, err := s.maintainer(ctx, login, assoc, rule)
	if err != nil {
		return "", err
	}
	if directive {
		if directive, err = s.mentionsAgent(ctx, text); err != nil {
			return "", err
		}
	}
	if directive {
		return "", s.setTask(ctx, ti, taskNew, "reopened by "+login)
	}
	if kind == "comment" {
		s.replyFinalized(ctx, threadID, ev.Repository.FullName, number)
	}
	return "ignored:finalized", nil
}

// ---------- 任务时限与完成提醒（R25） ----------

// scan 是每 scanInterval 一轮的扫描：到点的任务时限、到期的问题自动关闭（autoclose.go）、等待中的发布（release.go）。
func (s *Service) scan(ctx context.Context) {
	s.scanDeadlines(ctx)
	s.scanAutoclose(ctx)
	s.scanFreezeAfterTurn(ctx)
	s.scanReleases(ctx)
}

// scanDeadlines 处理到点的任务时限：清掉时限（每个只处理一次）、中止当前回合并通知。
func (s *Service) scanDeadlines(ctx context.Context) {
	due, err := s.st.Q.ListDueThreadIssueDeadlines(ctx, new(s.nowMs()))
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("github: 读取到点的任务时限失败", "err", err)
		}
		return
	}
	for _, ti := range due {
		if err := s.st.Q.SetThreadIssueDeadline(ctx, db.SetThreadIssueDeadlineParams{DeadlineAt: nil, ThreadID: ti.ThreadID}); err != nil {
			slog.Warn("github: 清除任务时限失败", "thread", ti.ThreadID, "err", err)
			continue
		}
		if err := s.th.Abort(ctx, ti.ThreadID); err != nil {
			slog.Warn("github: 任务时限到点，中止回合失败", "thread", ti.ThreadID, "err", err)
		}
		if s.n != nil {
			full, _ := s.repoFullName(ctx, ti.RepoID)
			body := fmt.Sprintf("GitHub task time limit reached: the current turn on %s#%d was stopped.", full, ti.Number)
			if err := s.n.PostThread(ctx, ti.ThreadID, "error", body); err != nil {
				slog.Warn("github: 发送任务时限通知失败", "thread", ti.ThreadID, "err", err)
			}
		}
	}
}

// TurnObserver 返回 GitHub 集成的线程观察者：GitHub 投递（标签以 turnTagPrefix 开头）的回合正常结束时调用 OnTurnEnded；
// 线程离开 working 时执行登记过的「回合结束后冻结」。
func (s *Service) TurnObserver() threads.Observer { return turnObserver{s} }

type turnObserver struct{ s *Service }

var _ threads.Observer = turnObserver{}

func (o turnObserver) TurnEnded(ev threads.TurnEvent) {
	if !strings.HasPrefix(ev.Tag, turnTagPrefix) {
		return
	}
	if ev.Status == threads.TurnCompleted {
		o.s.OnTurnEnded(ev.ThreadID)
	}
	if !ev.Local && o.s.cfg.HandOff != nil {
		if !o.s.enqueue(func(ctx context.Context) { o.s.handOff(ctx, ev) }) {
			slog.Warn("github: 任务队列已满，跳过交给助手", "thread", ev.ThreadID)
		}
	}
}

// handOff 把开着“交给助手”的触发规则线程的一个回合结果交给 Config.HandOff（M19 步骤 3.3）。
func (s *Service) handOff(ctx context.Context, ev threads.TurnEvent) {
	trig, ok, err := s.threadRule(ctx, ev.ThreadID)
	if err != nil || !ok || trig.HandToAssistant == 0 {
		return
	}
	h := schedule.HandOff{Kind: "trigger", SourceID: trig.ID, SourceName: trig.Name, ThreadID: ev.ThreadID}
	switch ev.Status {
	case threads.TurnCompleted:
		h.Outcome = "completed"
	case threads.TurnAborted:
		h.Outcome, h.Error = "aborted", "aborted"
	case threads.TurnError:
		h.Outcome, h.Error = "failed", ev.Error
		if h.Error == "" {
			h.Error = "thread_error"
		}
	default:
		h.Outcome, h.Error = "failed", "outcome_unknown"
	}
	if ev.HasText {
		h.Text = ev.Text
	}
	s.cfg.HandOff(ctx, h)
}

// StateChanged 只看「离开 working」：agent 已收到本回合全部工具结果、回合结束（转为 idle、awaiting_input 或 error）。
func (o turnObserver) StateChanged(ev threads.StateEvent) {
	if ev.Prev == threads.StateWorking && ev.State != threads.StateWorking {
		o.s.onThreadLeftWorking(ev.ThreadID)
	}
}

func (turnObserver) InputRequested(threads.InputEvent) {}
func (turnObserver) InputResolved(_, _ string)         {}
func (turnObserver) Seen(string)                       {}
func (turnObserver) EntriesAppended(string)            {}

// OnTurnEnded 在线程的一个 GitHub 回合结束时排队检查完成提醒（不阻塞调用方；队列满时丢弃并记日志）。
func (s *Service) OnTurnEnded(threadID string) {
	if !s.enqueue(func(ctx context.Context) { s.remind(ctx, threadID) }) {
		slog.Warn("github: 任务队列已满，跳过完成提醒检查", "thread", threadID)
	}
}

// remindStates 是还需要提醒的任务状态；remindClasses 是需要提醒的分类（question 等停在 new 是正常收尾，v3）。
var (
	remindStates  = map[string]bool{taskNew: true, taskReproducing: true, taskFixing: true}
	remindClasses = map[string]bool{"bug": true, "documentation": true}
)

// remind 按 R25 补发完成提醒：规则开启了完成提醒、状态仍是 new / reproducing / fixing、分类为 bug / documentation 或尚未
// 分类，且提醒次数未满 maxCompletionReminders。提醒本身也是带标签的投递，它的回合结束后会再检查一次。
func (s *Service) remind(ctx context.Context, threadID string) {
	ti, err := s.st.Q.GetThreadIssueByThread(ctx, threadID)
	if err != nil {
		if !isNoRows(err) {
			slog.Warn("github: 读取线程任务失败", "thread", threadID, "err", err)
		}
		return
	}
	if !remindStates[ti.State] || ti.Classification != nil && !remindClasses[*ti.Classification] || ti.Reminders >= maxCompletionReminders {
		return
	}
	trig, ok, err := s.threadRule(ctx, threadID)
	if err != nil || !ok || !trig.CompletionReminders {
		return
	}
	n, err := s.st.Q.BumpThreadIssueReminders(ctx, threadID)
	if err != nil || n > maxCompletionReminders {
		return
	}
	full, _ := s.repoFullName(ctx, ti.RepoID)
	text := fmt.Sprintf("[Oryn completion reminder %d/%d for %s#%d — sent automatically, not typed by the owner]\n"+
		"The GitHub task is still in state %q. Finish the work the trigger asked for and record the outcome with "+
		"github_task_state, or call github_abort_task if only a human can fix the blocker.", n, maxCompletionReminders, full, ti.Number, ti.State)
	if _, err := s.th.Deliver(ctx, threadID, threads.AutoMessage{Text: text, Tag: newTurnTag()}, nil); err != nil {
		slog.Warn("github: 发送完成提醒失败", "thread", threadID, "err", err)
	}
}
