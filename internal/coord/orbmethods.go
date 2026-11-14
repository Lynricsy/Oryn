package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// orb 内 agent 的限额（亲属规则之外的失控保护）。
const (
	maxOpenChildren   = 10
	maxDepth          = 3
	maxAgentThreads   = 30
	agentThreadWindow = time.Hour
)

// registerOrbMethods 注册 orb 内 `oryn thread …` / `oryn schedule …` 经 orbd.sock 转来的七个 ctl 请求。
func (s *Service) registerOrbMethods(handle func(method string, fn orbs.RequestFunc)) {
	handle(string(apigen.MethodThreadCreate), s.onThreadCreate)
	handle(string(apigen.MethodThreadSend), s.onThreadSend)
	handle(string(apigen.MethodThreadGet), s.onThreadGet)
	handle(string(apigen.MethodScheduleGet), s.onScheduleGet)
	handle(string(apigen.MethodSchedulePut), s.onSchedulePut)
	handle(string(apigen.MethodScheduleDelete), s.onScheduleDelete)
	handle(string(apigen.MethodScheduleEnable), s.onScheduleEnable)
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
	cp := threads.CreateParams{Prompt: p.Prompt, ParentThreadID: c.ID}
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
	text := "[Oryn message from thread " + quote(c.Title) + " (" + c.ID + ") — sent by an agent, not typed by the owner]\n" + p.Message
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
	in := p.Input
	if (in.Mode != nil && *in.Mode != apigen.ScheduleModeThread) || in.ProjectID != nil || in.SpaceID != nil || in.Model != nil ||
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
