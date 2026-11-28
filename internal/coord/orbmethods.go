package coord

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/research"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// orb 内 agent 的限额（亲属规则之外的失控保护）。
const (
	maxOpenChildren   = 10
	maxDepth          = 3
	maxAgentThreads   = 30
	agentThreadWindow = time.Hour
	// threadPullWait 是 thread.pull 等目标 orb 唤醒的时长：停机的 orb 冷启动比冻结的久。
	threadPullWait = 2 * time.Minute
)

// registerOrbMethods 注册 orb 内 `oryn thread …` / `oryn schedule …` / `oryn library put` / `oryn follow add` 经 orbd.sock 转来的十一个 ctl 请求。
func (s *Service) registerOrbMethods(handle func(method string, fn orbs.RequestFunc)) {
	handle(string(apigen.MethodThreadCreate), s.onThreadCreate)
	handle(string(apigen.MethodThreadSend), s.onThreadSend)
	handle(string(apigen.MethodThreadGet), s.onThreadGet)
	handle(string(apigen.MethodThreadPull), s.onThreadPull)
	handle(string(apigen.MethodThreadList), s.onThreadList)
	handle(string(apigen.MethodScheduleGet), s.onScheduleGet)
	handle(string(apigen.MethodSchedulePut), s.onSchedulePut)
	handle(string(apigen.MethodScheduleDelete), s.onScheduleDelete)
	handle(string(apigen.MethodScheduleEnable), s.onScheduleEnable)
	handle(string(apigen.MethodLibraryPut), s.onLibraryPut)
	handle(string(apigen.MethodFollowAdd), s.onFollowAdd)
}

func ctlErr(code, msg string) *link.CtlError { return &link.CtlError{Code: code, Message: msg} }

// asCtlErr 把业务错误转成 ctl 错误（保留错误码）。
func asCtlErr(err error) error {
	if err == nil {
		return nil
	}
	var ce *link.CtlError
	if errors.As(err, &ce) {
		return ce
	}
	return ctlErr(errCode(err), errMessage(err))
}

// caller 校验调用方线程：存在、是工作线程、未归档、属于发起 link 的 orb；否则 forbidden。
func (s *Service) caller(ctx context.Context, orbID, threadID string) (db.Thread, error) {
	t, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Thread{}, ctlErr(CodeForbidden, "calling thread not found")
	}
	if err != nil {
		return db.Thread{}, err
	}
	if t.Kind != threads.KindWork || t.ArchivedAt != nil || t.OrbID == nil || *t.OrbID != orbID {
		return db.Thread{}, ctlErr(CodeForbidden, "calling thread does not belong to this orb")
	}
	return t, nil
}

// relative 校验目标是调用方的父线程或子线程（且是工作线程）；否则 forbidden。
func (s *Service) relative(ctx context.Context, caller db.Thread, targetID string) (db.Thread, error) {
	t, err := s.st.Q.GetThread(ctx, targetID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Thread{}, ctlErr(CodeForbidden, "thread not found or not related to the calling thread")
	}
	if err != nil {
		return db.Thread{}, err
	}
	child := t.ParentThreadID != nil && *t.ParentThreadID == caller.ID
	parent := caller.ParentThreadID != nil && *caller.ParentThreadID == t.ID
	if t.Kind != threads.KindWork || (!child && !parent) {
		return db.Thread{}, ctlErr(CodeForbidden, "only the thread's parent and the threads it started can be contacted")
	}
	return t, nil
}

// pullSource 取 thread.pull 的目标并按 pullRelation 校验。
func (s *Service) pullSource(ctx context.Context, c db.Thread, targetID string) (db.Thread, error) {
	t, err := s.st.Q.GetThread(ctx, targetID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Thread{}, errPullDenied
	}
	if err != nil {
		return db.Thread{}, err
	}
	if _, err := s.pullRelation(ctx, c, t); err != nil {
		return db.Thread{}, err
	}
	return t, nil
}

var errPullDenied = ctlErr(CodeForbidden, "only threads of the same project or Space, and this thread's parent and children, can be pulled from")

// pullRelation 判定 c 能否取 t 的改动，能取时返回关系（依次取父线程、子线程、同一项目、同一 Space 中第一个成立的）。
// 平级（同项目 / 同 Space）互取有两条例外：全自动 orb 上的线程（入口可能由不可信输入驱动）只能与亲属互取 → forbidden；
// 跑在 runner 上的线程一律不作目标（不把主人机器上的未提交内容搬进 orb）→ runner_unsupported。
func (s *Service) pullRelation(ctx context.Context, c, t db.Thread) (apigen.LinkCtlThreadListItemRelation, error) {
	if t.Kind != threads.KindWork || t.ID == c.ID {
		return "", errPullDenied
	}
	var rel apigen.LinkCtlThreadListItemRelation
	switch {
	case c.ParentThreadID != nil && *c.ParentThreadID == t.ID:
		rel = apigen.ThreadRelationParent
	case t.ParentThreadID != nil && *t.ParentThreadID == c.ID:
		rel = apigen.ThreadRelationChild
	case c.ProjectID != nil && t.ProjectID != nil && *c.ProjectID == *t.ProjectID:
		rel = apigen.ThreadRelationProject
	case c.SpaceID != nil && t.SpaceID != nil && *c.SpaceID == *t.SpaceID:
		rel = apigen.ThreadRelationSpace
	default:
		return "", errPullDenied
	}
	if rel == apigen.ThreadRelationProject || rel == apigen.ThreadRelationSpace {
		for _, x := range []db.Thread{c, t} {
			auto, err := s.automationThread(ctx, x)
			if err != nil {
				return "", err
			}
			if auto {
				return "", ctlErr(CodeForbidden, "threads started by automation can only pull from, and be pulled by, their parent and children")
			}
		}
	}
	runner, err := s.isRunnerThread(ctx, t)
	if err != nil {
		return "", err
	}
	if runner {
		return "", ctlErr(threads.CodeRunnerUnsupported, t.ID+" runs on a runner; its changes cannot be pulled into an orb")
	}
	return rel, nil
}

// maxThreadList 是 thread.list 一次列出的线程上限。
const maxThreadList = 100

// onThreadList 列出调用方可以 thread.pull 的线程：SQL 在 LIMIT 之前就按 pull 规则排除 runner 与全自动 orb 上的平级线程
// （否则较新的禁入线程会把可取的挤出前 100 条），再逐个按 pullRelation 复核。
func (s *Service) onThreadList(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlThreadListParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	c, err := s.caller(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	auto, err := s.automationThread(ctx, c)
	if err != nil {
		return nil, err
	}
	rows, err := s.st.Q.ListThreadPullCandidates(ctx, db.ListThreadPullCandidatesParams{ID: c.ID, ParentID: derefStr(c.ParentThreadID),
		Siblings: !auto, ProjectID: derefStr(c.ProjectID), SpaceID: derefStr(c.SpaceID), Lim: maxThreadList})
	if err != nil {
		return nil, err
	}
	res := apigen.LinkCtlThreadListResult{Threads: []apigen.LinkCtlThreadListItem{}}
	for _, t := range rows {
		rel, err := s.pullRelation(ctx, c, t)
		var ce *link.CtlError
		if errors.As(err, &ce) {
			continue
		}
		if err != nil {
			return nil, err
		}
		at := t.UpdatedAt
		if t.LastMessageAt != nil {
			at = *t.LastMessageAt
		}
		res.Threads = append(res.Threads, apigen.LinkCtlThreadListItem{ID: t.ID, Title: t.Title, State: apigen.ThreadState(t.State),
			Relation: rel, UpdatedAt: at, URL: s.threadURL(t.ID)})
	}
	return res, nil
}

// automationThread 报告线程所在 orb 是否标为全自动（M18 D3）；没有 orb 时为假。
func (s *Service) automationThread(ctx context.Context, t db.Thread) (bool, error) {
	if t.OrbID == nil {
		return false, nil
	}
	o, err := s.st.Q.GetOrb(ctx, *t.OrbID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return o.Automation, err
}

func decodeParams(params json.RawMessage, v any) error {
	if err := json.Unmarshal(params, v); err != nil {
		return ctlErr("invalid_params", err.Error())
	}
	return nil
}

// depth 返回线程沿 parent_thread_id 上溯的祖先数（至多查 16 层）。
func (s *Service) depth(ctx context.Context, t db.Thread) int {
	d := 0
	for cur := t; cur.ParentThreadID != nil && d < 16; d++ {
		p, err := s.st.Q.GetThread(ctx, *cur.ParentThreadID)
		if err != nil {
			return d + 1
		}
		cur = p
	}
	return d
}

func (s *Service) onThreadCreate(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlThreadCreateParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	c, err := s.caller(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	newID := ""
	defer func() {
		s.audit(ctx, string(apigen.ActorKindAgent), c.ID, false, string(apigen.ActionCreateThread), newID, "", p.Prompt, err)
	}()
	if strings.TrimSpace(p.Prompt) == "" {
		err = ctlErr("invalid_params", "prompt is required")
		return nil, err
	}
	// 「计数 + Create」持 createMu（契约 C6.1）：并发的 thread.create 不能都通过额度检查
	s.createMu.Lock()
	defer s.createMu.Unlock()
	open, cerr := s.st.Q.CountOpenChildren(ctx, c.ID)
	if cerr != nil {
		err = cerr
		return nil, err
	}
	if open >= maxOpenChildren {
		err = ctlErr(CodeRateLimited, "this thread already has 10 unarchived child threads")
		return nil, err
	}
	if s.depth(ctx, c)+1 > maxDepth {
		err = ctlErr(CodeRateLimited, "thread nesting is limited to 3 levels")
		return nil, err
	}
	recent, cerr := s.st.Q.CountAgentThreadsSince(ctx, s.cfg.Now().Add(-agentThreadWindow).UnixMilli())
	if cerr != nil {
		err = cerr
		return nil, err
	}
	if recent >= maxAgentThreads {
		err = ctlErr(CodeRateLimited, "agents created 30 threads in the last hour")
		return nil, err
	}
	if s.afterQuotaCheck != nil {
		s.afterQuotaCheck()
	}
	// 首条提示词前加来源头行（C9.3），与 thread.send 同形
	cp := threads.CreateParams{Prompt: threadSourceLine(c.ID) + "\n" + p.Prompt, ParentThreadID: c.ID}
	if c.ProjectID != nil {
		cp.ProjectID = *c.ProjectID
	}
	if c.SpaceID != nil {
		cp.SpaceID = *c.SpaceID
	}
	if p.Title != nil {
		cp.Title = *p.Title
	}
	if p.Type != nil {
		cp.Type = *p.Type
	}
	t, cerr := s.th.Create(ctx, cp)
	if cerr != nil {
		err = asCtlErr(cerr)
		return nil, err
	}
	newID = t.ID
	return s.threadInfo(ctx, t.ID)
}

// threadInfo 是 thread.create / thread.get 的结果。
func (s *Service) threadInfo(ctx context.Context, id string) (apigen.LinkCtlThreadInfo, error) {
	t, err := s.st.Q.GetThread(ctx, id)
	if err != nil {
		return apigen.LinkCtlThreadInfo{}, asCtlErr(err)
	}
	info := apigen.LinkCtlThreadInfo{ID: t.ID, Title: t.Title, State: apigen.ThreadState(t.State), URL: s.threadURL(t.ID)}
	switch t.State {
	case threads.StateIdle, threads.StateAwaitingInput, threads.StateError:
		info.Settled = true
	}
	if text, err := s.lastAssistantText(ctx, t, maxInfoRunes); err == nil && text != "" {
		info.LastAssistantText = &text
	}
	return info, nil
}

func (s *Service) onThreadSend(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlThreadSendParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	c, err := s.caller(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	defer func() {
		s.audit(ctx, string(apigen.ActorKindAgent), c.ID, false, string(apigen.ActionSendMessage), p.Target, "", p.Message, err)
	}()
	if _, err = s.relative(ctx, c, p.Target); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Message) == "" {
		err = ctlErr("invalid_params", "message is required")
		return nil, err
	}
	text := threadSourceLine(c.ID) + "\n" + p.Message
	steer := p.Steer != nil && *p.Steer
	if _, derr := s.th.Deliver(ctx, p.Target, threads.AutoMessage{Text: text, Steer: steer}, nil); derr != nil {
		err = asCtlErr(derr)
		return nil, err
	}
	return apigen.LinkCtlOkResult{Ok: true}, nil
}

func (s *Service) onThreadGet(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlThreadGetParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	c, err := s.caller(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	if _, err := s.relative(ctx, c, p.Target); err != nil {
		return nil, err
	}
	return s.threadInfo(ctx, p.Target)
}

// onThreadPull 把可取线程（见 pullSource）仓库的改动取到调用方 orb：在目标 orb 上 /sync/prepare（快照未提交改动、按调用方的 have
// 打包缺少的对象），有 bundle 时把它原样写到调用方 orb 的 bundlePath（不覆盖），由调用方自己 fetch。目标 orb 只多出
// refs/oryn/sync/* 与悬空的快照提交，工作区、index 与分支不变。
func (s *Service) onThreadPull(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlThreadPullParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	c, err := s.caller(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	t, err := s.pullSource(ctx, c, p.Target)
	if err != nil {
		return nil, err
	}
	if !path.IsAbs(p.BundlePath) {
		return nil, ctlErr("invalid_params", "bundlePath must be an absolute path")
	}
	if t.OrbID == nil {
		return nil, ctlErr(CodeNoOrb, "thread "+t.ID+" has no orb (it never started, or its orb was deleted)")
	}
	m, err := s.machinesOr()
	if err != nil {
		return nil, asCtlErr(err)
	}
	body, err := json.Marshal(apigen.OrbSyncPrepareRequest{Have: p.Have})
	if err != nil {
		return nil, err
	}
	var st apigen.OrbSyncState
	if err := s.orbJSON(ctx, m, t, orbRequest{method: http.MethodPost, path: "/sync/prepare",
		body: bytes.NewReader(body), size: int64(len(body)), wait: threadPullWait}, &st); err != nil {
		return nil, asCtlErr(err)
	}
	res := apigen.LinkCtlThreadPullResult{Branch: st.Branch, Head: st.Head, Snapshot: st.Snapshot, Dirty: st.Dirty}
	if st.Bundle == nil {
		return res, nil
	}
	src, err := s.orbDo(ctx, m, t, orbRequest{method: http.MethodGet, path: "/sync/bundle", query: url.Values{"id": {st.Bundle.ID}}})
	if err != nil {
		return nil, asCtlErr(err)
	}
	defer src.Body.Close()
	var e apigen.FsEntry
	if err := s.orbJSON(ctx, m, c, orbRequest{method: http.MethodPut, path: "/fs/file", query: writeQuery(p.BundlePath, false),
		body: io.LimitReader(src.Body, st.Bundle.Size), size: st.Bundle.Size}, &e); err != nil {
		return nil, asCtlErr(err)
	}
	res.Bundled = true
	return res, nil
}

// ownSchedule 返回调用方线程上的线程内定时（没有 → nil）。
func (s *Service) ownSchedule(ctx context.Context, threadID string) (*apigen.Schedule, error) {
	if s.sched == nil {
		return nil, ctlErr(CodeCoordinatorUnavailable, "schedules are not available")
	}
	sc, err := s.sched.GetByThread(ctx, threadID)
	if err != nil {
		var se *schedule.Error
		if errors.As(err, &se) && (se.Code == schedule.CodeScheduleNotFound || se.Code == schedule.CodeNotFound) {
			return nil, nil
		}
		return nil, asCtlErr(err)
	}
	return &sc, nil
}

func (s *Service) onScheduleGet(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlScheduleParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if _, err := s.caller(ctx, orbID, p.ThreadID); err != nil {
		return nil, err
	}
	sc, err := s.ownSchedule(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	return apigen.LinkCtlScheduleResult{Schedule: sc}, nil
}

func (s *Service) onSchedulePut(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlSchedulePutParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	c, err := s.caller(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	var sc apigen.Schedule
	defer func() {
		summary := "set"
		if sc.ID != "" {
			summary = scheduleSummary(sc)
		}
		s.audit(ctx, string(apigen.ActorKindAgent), c.ID, false, string(apigen.ActionSetSchedule), c.ID, sc.ID, summary, err)
	}()
	if s.sched == nil {
		err = ctlErr(CodeCoordinatorUnavailable, "schedules are not available")
		return nil, err
	}
	if !s.lim.allow(c.ID+"|set_schedule", s.cfg.Now(), setScheduleRules) {
		err = ctlErr(CodeRateLimited, "schedule changes are limited to 10 per 10 minutes and 40 per 24 hours per thread")
		return nil, err
	}
	in := p.Input
	if (in.Mode != nil && *in.Mode != apigen.ScheduleModeThread) || in.ProjectID != nil || in.SpaceID != nil || in.Role != nil || in.Model != nil ||
		in.ThinkingLevel != nil || in.Provider != nil || in.OrbType != nil || in.Overlap != nil || in.KeepLast != nil {
		err = ctlErr(CodeInvalidSchedule, "agents can only set the schedule of their own thread")
		return nil, err
	}
	if in.ThreadID != nil && *in.ThreadID != c.ID {
		err = ctlErr(CodeForbidden, "agents can only set the schedule of their own thread")
		return nil, err
	}
	var perr error
	if sc, perr = s.sched.PutForThread(ctx, c.ID, schedule.InputFromAPI(in), "agent"); perr != nil {
		err = asCtlErr(perr)
		return nil, err
	}
	return apigen.LinkCtlScheduleResult{Schedule: &sc}, nil
}

func (s *Service) onScheduleDelete(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlScheduleParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	c, err := s.caller(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	var cur *apigen.Schedule
	defer func() {
		id, summary := "", "delete"
		if cur != nil {
			id, summary = cur.ID, "delete · "+scheduleSummary(*cur)
		}
		s.audit(ctx, string(apigen.ActorKindAgent), c.ID, false, string(apigen.ActionDeleteSchedule), c.ID, id, summary, err)
	}()
	if !s.lim.allow(c.ID+"|set_schedule", s.cfg.Now(), setScheduleRules) {
		err = ctlErr(CodeRateLimited, "schedule changes are limited to 10 per 10 minutes and 40 per 24 hours per thread")
		return nil, err
	}
	if cur, err = s.ownSchedule(ctx, c.ID); err != nil {
		return nil, err
	}
	if cur == nil {
		err = ctlErr(schedule.CodeScheduleNotFound, "this thread has no schedule")
		return nil, err
	}
	if derr := s.sched.Delete(ctx, cur.ID); derr != nil {
		err = asCtlErr(derr)
		return nil, err
	}
	return apigen.LinkCtlOkResult{Ok: true}, nil
}

func (s *Service) onScheduleEnable(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlScheduleEnableParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	c, err := s.caller(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	if !s.lim.allow(c.ID+"|set_schedule", s.cfg.Now(), setScheduleRules) {
		return nil, ctlErr(CodeRateLimited, "schedule changes are limited to 10 per 10 minutes and 40 per 24 hours per thread")
	}
	cur, err := s.ownSchedule(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, ctlErr(schedule.CodeScheduleNotFound, "this thread has no schedule")
	}
	sc, err := s.sched.SetEnabled(ctx, cur.ID, p.Enabled)
	if err != nil {
		return nil, asCtlErr(err)
	}
	return apigen.LinkCtlScheduleResult{Schedule: &sc}, nil
}

// onFollowAdd 是 orb 内 `oryn follow add`（M26）：主人在本线程里要求跟进某件事时登记一个公网跟进话题（origin=thread）。
// quote 必须逐字出现在本线程活动分支上某条主人亲手输入的消息里，否则 forbidden。
func (s *Service) onFollowAdd(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlFollowAddParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	c, err := s.caller(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	defer func() {
		s.audit(ctx, string(apigen.ActorKindAgent), c.ID, false, string(apigen.ActionFollowTopic), c.ID, "", p.Title, err)
	}()
	if !s.lim.allow(c.ID+"|follow_add", s.cfg.Now(), followAddRules) {
		err = ctlErr(CodeRateLimited, "follow-ups are limited to 5 per 10 minutes and 20 per 24 hours per thread")
		return nil, err
	}
	quoted, qerr := s.ownerSaid(ctx, c, p.Quote)
	if qerr != nil {
		err = asCtlErr(qerr)
		return nil, err
	}
	if !quoted {
		err = ctlErr(CodeForbidden, "the owner did not say this in this thread; pass the owner's own words with --quote")
		return nil, err
	}
	if s.topics == nil {
		err = ctlErr(CodeCoordinatorUnavailable, "background research is not available")
		return nil, err
	}
	in := research.TopicInput{Title: p.Title, Question: p.Question, Origin: string(apigen.ResearchOriginThread), SourceThreadID: c.ID}
	if p.Kind != nil {
		in.Kind = string(*p.Kind)
	}
	topic, terr := s.topics.CreateTopic(ctx, in)
	if terr != nil {
		var re *research.Error
		if errors.As(terr, &re) {
			err = ctlErr(re.Code, re.Message)
		} else {
			err = asCtlErr(terr)
		}
		return nil, err
	}
	return apigen.LinkCtlFollowAddResult{TopicID: topic.ID}, nil
}

// ownerSaid 判断 quote（去首尾空白后非空）是否逐字出现在线程活动分支上某条主人亲手输入的消息里（自动化消息不算）。
func (s *Service) ownerSaid(ctx context.Context, t db.Thread, quote string) (bool, error) {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return false, nil
	}
	branch, err := s.activeBranch(ctx, t)
	if err != nil {
		return false, err
	}
	for _, e := range branch {
		m, ok := transcript.ParseMessage(e)
		if ok && m.Role == "user" && !transcript.IsAutomatedUserText(m.Text) && strings.Contains(m.Text, quote) {
			return true, nil
		}
	}
	return false, nil
}
