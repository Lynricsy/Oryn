package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/stats"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// toolCtx 是一次宿主工具调用的上下文。
type toolCtx struct {
	threads.HostToolCall
	space *db.Space // Space 协调者所属的 Space；助手为 nil
}

func (c toolCtx) inSpace() bool { return c.space != nil }

// HostToolCall 实现 threads.Coordinator：执行宿主工具。结果的 Text 是紧凑 JSON（read_thread 另加不可信包裹），
// Details 是同一对象；失败时 IsError、Text 以错误码开头。
func (s *Service) HostToolCall(ctx context.Context, c threads.HostToolCall) threads.HostToolResult {
	tc := toolCtx{HostToolCall: c}
	if c.Kind == threads.KindSpace && c.SpaceID != "" {
		if sp, err := s.st.Q.GetSpace(ctx, c.SpaceID); err == nil {
			tc.space = &sp
		}
	}
	var (
		v   any
		err error
	)
	switch c.Name {
	case ToolListThreads:
		v, err = s.toolListThreads(ctx, tc)
	case ToolReadThread:
		return s.toolReadThread(ctx, tc)
	case ToolCreateThread:
		v, err = s.toolCreateThread(ctx, tc)
	case ToolSendMessage:
		v, err = s.toolSendMessage(ctx, tc)
	case ToolArchiveThread:
		v, err = s.toolArchiveThread(ctx, tc)
	case ToolSetSchedule:
		v, err = s.toolSetSchedule(ctx, tc)
	case ToolListSchedules:
		v, err = s.toolListSchedules(ctx, tc)
	case ToolListProjects:
		v, err = s.toolListProjects(ctx)
	case ToolGetStats:
		v, err = s.toolGetStats(ctx, tc)
	default:
		err = errf("unknown_tool", "no tool named %q", c.Name)
	}
	if err != nil {
		return failResult(err)
	}
	return threads.HostToolResult{Text: jsonText(v), Details: v}
}

func failResult(err error) threads.HostToolResult {
	return threads.HostToolResult{Text: errCode(err) + ": " + errMessage(err), IsError: true}
}

// decodeArgs 解析工具参数（缺省为空对象）。
func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return errf(CodeInvalid, "invalid arguments: %s", err.Error())
	}
	return nil
}

// resolveTarget 是宿主工具唯一的目标校验：线程必须存在且是工作线程。协调对话之间不能互相发消息、归档、读取——
// 这是防止协调者互相唤醒成环的结构规则，不是权限。
func (s *Service) resolveTarget(ctx context.Context, threadID string) (db.Thread, error) {
	if threadID == "" {
		return db.Thread{}, errf(CodeInvalid, "threadId is required")
	}
	t, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Thread{}, errf(CodeNotFound, "thread %s not found", threadID)
	}
	if err != nil {
		return db.Thread{}, err
	}
	if t.Kind != threads.KindWork {
		return db.Thread{}, errf(CodeInvalidTarget, "%s is a coordinator conversation, not a work thread", threadID)
	}
	return t, nil
}

// projectByName 按名称取项目；不存在 → not_found。
func (s *Service) projectByName(ctx context.Context, name string) (db.Project, error) {
	p, err := s.st.Q.GetProjectByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Project{}, errf(CodeNotFound, "project %q not found", name)
	}
	return p, err
}

// projectNames 返回 项目 id → 名称。
func (s *Service) projectNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.st.Q.ListProjects(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, p := range rows {
		out[p.ID] = p.Name
	}
	return out, nil
}

// ---- list_threads ----

type listThreadsArgs struct {
	Scope    string `json:"scope"`
	State    string `json:"state"`
	Project  string `json:"project"`
	Archived bool   `json:"archived"`
	Query    string `json:"query"`
	Limit    int    `json:"limit"`
}

type threadItem struct {
	ID         string                        `json:"id"`
	Title      string                        `json:"title"`
	State      string                        `json:"state"`
	Project    string                        `json:"project,omitempty"`
	SpaceID    string                        `json:"spaceId,omitempty"`
	Unread     bool                          `json:"unread"`
	ParentID   string                        `json:"parentId,omitempty"`
	Schedule   *apigen.ThreadScheduleSummary `json:"schedule,omitempty"`
	ScheduleID string                        `json:"scheduleId,omitempty"`
	UpdatedAt  int64                         `json:"updatedAt"`
}

func (s *Service) toolListThreads(ctx context.Context, c toolCtx) (any, error) {
	var a listThreadsArgs
	if err := decodeArgs(c.Args, &a); err != nil {
		return nil, err
	}
	f := threads.ListFilter{State: a.State, Q: a.Query}
	if a.Archived {
		f.Archived = &a.Archived
	}
	if c.inSpace() && a.Scope != "all" {
		f.Space = c.space.ID
	}
	if a.Project != "" {
		p, err := s.projectByName(ctx, a.Project)
		if err != nil {
			return nil, err
		}
		f.Project = p.ID
	}
	list, err := s.th.List(ctx, f)
	if err != nil {
		return nil, err
	}
	names, err := s.projectNames(ctx)
	if err != nil {
		return nil, err
	}
	sums, err := s.st.Q.ListScheduleSummaries(ctx)
	if err != nil {
		return nil, err
	}
	schedIDs := make(map[string]string, len(sums))
	for _, r := range sums {
		schedIDs[r.ThreadID] = r.ID
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 30
	}
	limit = min(limit, 100)
	items := make([]threadItem, 0, min(len(list), limit))
	for _, t := range list {
		if len(items) >= limit {
			break
		}
		it := threadItem{ID: t.ID, Title: t.Title, State: string(t.State), Unread: t.Unread, Schedule: t.Schedule,
			ScheduleID: schedIDs[t.ID], UpdatedAt: t.UpdatedAt}
		if t.ProjectID != nil {
			it.Project = names[*t.ProjectID]
		}
		if t.SpaceID != nil {
			it.SpaceID = *t.SpaceID
		}
		if t.ParentThreadID != nil {
			it.ParentID = *t.ParentThreadID
		}
		items = append(items, it)
	}
	return map[string]any{"threads": items}, nil
}

// ---- read_thread ----

type readThreadArgs struct {
	ThreadID string `json:"threadId"`
	Tail     int    `json:"tail"`
}

func (s *Service) toolReadThread(ctx context.Context, c toolCtx) threads.HostToolResult {
	var a readThreadArgs
	if err := decodeArgs(c.Args, &a); err != nil {
		return failResult(err)
	}
	t, err := s.resolveTarget(ctx, a.ThreadID)
	if err != nil {
		return failResult(err)
	}
	n := a.Tail
	if n <= 0 {
		n = defaultReadTail
	}
	n = min(n, maxReadTail)
	msgs, err := s.recentMessages(ctx, t, n)
	if err != nil {
		return failResult(err)
	}
	v := map[string]any{
		"thread":   map[string]any{"id": t.ID, "title": t.Title, "state": t.State},
		"messages": msgs,
	}
	return threads.HostToolResult{Text: wrapUntrusted(t.ID, jsonText(v)), Details: v}
}

// ---- create_thread ----

type createThreadArgs struct {
	Prompt        string `json:"prompt"`
	Project       string `json:"project"`
	Title         string `json:"title"`
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinkingLevel"`
	OrbType       string `json:"orbType"`
}

func (s *Service) toolCreateThread(ctx context.Context, c toolCtx) (any, error) {
	var a createThreadArgs
	err := decodeArgs(c.Args, &a)
	newID := ""
	defer func() {
		s.audit(ctx, c.Kind, c.ThreadID, c.OwnerTurn, string(apigen.ActionCreateThread), newID, "", a.Prompt, err)
	}()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Prompt) == "" {
		err = errf(CodeInvalid, "prompt is required")
		return nil, err
	}
	p := threads.CreateParams{Prompt: a.Prompt, Title: a.Title, ThinkingLevel: a.ThinkingLevel, Type: a.OrbType, ParentThreadID: c.ThreadID}
	if a.Model != "" {
		m, ok := parseModelRef(a.Model)
		if !ok {
			err = errf(CodeInvalid, "model must be provider/id")
			return nil, err
		}
		p.Model = m
	}
	projectName := a.Project
	switch {
	case a.Project != "":
		var prj db.Project
		if prj, err = s.projectByName(ctx, a.Project); err != nil {
			return nil, err
		}
		p.ProjectID = prj.ID
	case c.inSpace() && c.space.ProjectID != nil:
		p.ProjectID = *c.space.ProjectID
		if names, nerr := s.projectNames(ctx); nerr == nil {
			projectName = names[p.ProjectID]
		}
	}
	if c.inSpace() {
		p.SpaceID = c.space.ID
	}
	if !s.lim.allow(c.ThreadID+"|create_thread", s.cfg.Now(), createThreadRules) {
		err = errf(CodeRateLimited, "create_thread limit reached (10 per 10 minutes, 40 per 24 hours per conversation)")
		return nil, err
	}
	var t apigen.Thread
	if t, err = s.th.Create(ctx, p); err != nil {
		return nil, err
	}
	newID = t.ID
	s.requestReport(ctx, t.ID, c.ThreadID)
	return map[string]any{
		"threadId": t.ID, "title": t.Title, "project": projectName, "url": s.threadURL(t.ID), "threadIds": []string{t.ID},
	}, nil
}

// ---- send_message ----

type sendMessageArgs struct {
	ThreadID string `json:"threadId"`
	Message  string `json:"message"`
	Steer    bool   `json:"steer"`
}

// sourceLine 是协调者发出的消息的来源头行。
func (s *Service) sourceLine(ctx context.Context, c toolCtx) string {
	if c.inSpace() {
		return "[Oryn message from Space " + quote(c.space.Name) + " (" + c.ThreadID + ") — sent by an agent, not typed by the owner]"
	}
	title := ""
	if t, err := s.st.Q.GetThread(ctx, c.ThreadID); err == nil {
		title = t.Title
	}
	return "[Oryn message from assistant " + quote(title) + " (" + c.ThreadID + ") — sent by an agent, not typed by the owner]"
}

func quote(s string) string { return "\"" + strings.ReplaceAll(s, "\"", "'") + "\"" }

func (s *Service) toolSendMessage(ctx context.Context, c toolCtx) (any, error) {
	var a sendMessageArgs
	err := decodeArgs(c.Args, &a)
	defer func() {
		s.audit(ctx, c.Kind, c.ThreadID, c.OwnerTurn, string(apigen.ActionSendMessage), a.ThreadID, "", a.Message, err)
	}()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Message) == "" {
		err = errf(CodeInvalid, "message is required")
		return nil, err
	}
	if _, err = s.resolveTarget(ctx, a.ThreadID); err != nil {
		return nil, err
	}
	if !s.lim.allow(c.ThreadID+"|send_message", s.cfg.Now(), sendMessageRules) {
		err = errf(CodeRateLimited, "send_message limit reached (60 per 10 minutes per conversation)")
		return nil, err
	}
	text := s.sourceLine(ctx, c) + "\n" + a.Message
	if _, err = s.th.Deliver(ctx, a.ThreadID, threads.AutoMessage{Text: text, Steer: a.Steer}, nil); err != nil {
		return nil, err
	}
	s.requestReport(ctx, a.ThreadID, c.ThreadID)
	return map[string]any{"ok": true, "threadIds": []string{a.ThreadID}}, nil
}

// ---- archive_thread ----

type archiveThreadArgs struct {
	ThreadID string `json:"threadId"`
	Outcome  string `json:"outcome"`
}

func (s *Service) toolArchiveThread(ctx context.Context, c toolCtx) (any, error) {
	var a archiveThreadArgs
	err := decodeArgs(c.Args, &a)
	defer func() {
		s.audit(ctx, c.Kind, c.ThreadID, c.OwnerTurn, string(apigen.ActionArchiveThread), a.ThreadID, "", "outcome: "+outcomeOf(a.Outcome), err)
	}()
	if err != nil {
		return nil, err
	}
	if a.Outcome != "" && a.Outcome != "shipped" && a.Outcome != "archived" {
		err = errf(CodeInvalid, "outcome must be shipped or archived")
		return nil, err
	}
	if _, err = s.resolveTarget(ctx, a.ThreadID); err != nil {
		return nil, err
	}
	if a.Outcome == "shipped" {
		yes := true
		_, err = s.th.Patch(ctx, a.ThreadID, apigen.ThreadPatch{Shipped: &yes})
	} else {
		err = s.th.Archive(ctx, a.ThreadID)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "threadIds": []string{a.ThreadID}}, nil
}

func outcomeOf(o string) string {
	if o == "" {
		return "archived"
	}
	return o
}

// ---- set_schedule ----

type setScheduleArgs struct {
	Action        string `json:"action"`
	ScheduleID    string `json:"scheduleId"`
	Mode          string `json:"mode"`
	ThreadID      string `json:"threadId"`
	Project       string `json:"project"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Cron          string `json:"cron"`
	Every         string `json:"every"`
	At            *int64 `json:"at"`
	Timezone      string `json:"timezone"`
	EndsAt        *int64 `json:"endsAt"`
	Prompt        string `json:"prompt"`
	Enabled       *bool  `json:"enabled"`
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinkingLevel"`
	OrbType       string `json:"orbType"`
	Overlap       string `json:"overlap"`
	KeepLast      *int   `json:"keepLast"`
}

func (s *Service) toolSetSchedule(ctx context.Context, c toolCtx) (any, error) {
	var a setScheduleArgs
	err := decodeArgs(c.Args, &a)
	var (
		sc     apigen.Schedule
		target string
	)
	defer func() {
		action := string(apigen.ActionSetSchedule)
		if a.Action == "delete" {
			action = string(apigen.ActionDeleteSchedule)
		}
		summary := a.Action
		if sc.ID != "" {
			summary = scheduleSummary(sc)
			if a.Action != "set" {
				summary = a.Action + " · " + summary
			}
		}
		if target == "" && sc.ThreadID != nil {
			target = *sc.ThreadID
		}
		id := sc.ID
		if id == "" {
			id = a.ScheduleID
		}
		s.audit(ctx, c.Kind, c.ThreadID, c.OwnerTurn, action, target, id, summary, err)
	}()
	if err != nil {
		return nil, err
	}
	if s.sched == nil {
		err = errf(CodeCoordinatorUnavailable, "schedules are not available")
		return nil, err
	}
	// thread 模式的目标：缺省 = 本对话；给了别的线程则必须是工作线程
	threadTarget := func() (string, error) {
		if a.ThreadID == "" || a.ThreadID == c.ThreadID {
			return c.ThreadID, nil
		}
		if _, err := s.resolveTarget(ctx, a.ThreadID); err != nil {
			return "", err
		}
		return a.ThreadID, nil
	}
	switch a.Action {
	case "set":
		sc, target, err = s.putSchedule(ctx, c, a, threadTarget)
	case "pause", "resume", "delete":
		id := a.ScheduleID
		if id == "" {
			tid, terr := threadTarget()
			if terr != nil {
				err = terr
				return nil, err
			}
			cur, gerr := s.sched.GetByThread(ctx, tid)
			if gerr != nil {
				err = gerr
				return nil, err
			}
			id = cur.ID
		}
		if sc, err = s.sched.Get(ctx, id); err != nil {
			return nil, err
		}
		if sc.ThreadID != nil && *sc.ThreadID != c.ThreadID {
			if _, err = s.resolveTarget(ctx, *sc.ThreadID); err != nil {
				return nil, err
			}
		}
		switch a.Action {
		case "delete":
			if err = s.sched.Delete(ctx, id); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "scheduleId": id}, nil
		default:
			var next apigen.Schedule
			if next, err = s.sched.SetEnabled(ctx, id, a.Action == "resume"); err != nil {
				return nil, err
			}
			sc = next
		}
	default:
		err = errf(CodeInvalid, "action must be set, pause, resume or delete")
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"schedule": sc}, nil
}

// putSchedule 处理 set_schedule{action: set}。
func (s *Service) putSchedule(ctx context.Context, c toolCtx, a setScheduleArgs, threadTarget func() (string, error)) (apigen.Schedule, string, error) {
	mode := a.Mode
	if mode == "" {
		mode = string(apigen.ScheduleModeThread)
	}
	in := schedule.Input{
		Name: a.Name, Kind: a.Kind, Cron: a.Cron, Every: a.Every, Timezone: a.Timezone, Prompt: a.Prompt,
		At: a.At, EndsAt: a.EndsAt, Enabled: a.Enabled, Mode: mode,
	}
	switch mode {
	case string(apigen.ScheduleModeThread):
		tid, err := threadTarget()
		if err != nil {
			return apigen.Schedule{}, "", err
		}
		if a.ScheduleID != "" {
			in.ThreadID = tid
			sc, err := s.sched.Put(ctx, a.ScheduleID, in, "agent")
			return sc, tid, err
		}
		sc, err := s.sched.PutForThread(ctx, tid, in, "agent")
		return sc, tid, err
	case string(apigen.ScheduleModeNewThread):
		in.ThinkingLevel, in.OrbType, in.Overlap, in.KeepLast = a.ThinkingLevel, a.OrbType, a.Overlap, a.KeepLast
		if a.Model != "" {
			m, ok := parseModelRef(a.Model)
			if !ok {
				return apigen.Schedule{}, "", errf(CodeInvalidSchedule, "model must be provider/id")
			}
			in.Model = m
		}
		switch {
		case a.Project != "":
			p, err := s.projectByName(ctx, a.Project)
			if err != nil {
				return apigen.Schedule{}, "", err
			}
			in.ProjectID = p.ID
		case c.inSpace() && c.space.ProjectID != nil:
			in.ProjectID = *c.space.ProjectID
		}
		if c.inSpace() {
			in.SpaceID = c.space.ID
		}
		if a.ScheduleID != "" {
			// 目标（mode / projectId）不可改：更新时不带它们
			in.ProjectID = ""
		}
		sc, err := s.sched.Put(ctx, a.ScheduleID, in, "agent")
		return sc, "", err
	}
	return apigen.Schedule{}, "", errf(CodeInvalidSchedule, "mode must be thread or new_thread")
}

// scheduleSummary 是审计里的定时摘要：`<name> · <kind> <spec> · <mode>`。
func scheduleSummary(sc apigen.Schedule) string {
	name := sc.Name
	if name == "" {
		name = "untitled"
	}
	spec := ""
	switch {
	case sc.Cron != nil:
		spec = *sc.Cron
	case sc.Every != nil:
		spec = *sc.Every
	case sc.At != nil:
		spec = time.UnixMilli(*sc.At).UTC().Format(time.RFC3339)
	}
	return name + " · " + string(sc.Kind) + " " + spec + " · " + string(sc.Mode)
}

// ---- list_schedules / list_projects / get_stats ----

type scopeArgs struct {
	Scope string `json:"scope"`
}

func (s *Service) toolListSchedules(ctx context.Context, c toolCtx) (any, error) {
	var a scopeArgs
	if err := decodeArgs(c.Args, &a); err != nil {
		return nil, err
	}
	if s.sched == nil {
		return nil, errf(CodeCoordinatorUnavailable, "schedules are not available")
	}
	f := schedule.Filter{}
	if c.inSpace() && a.Scope != "all" {
		f.SpaceID = c.space.ID
	}
	list, err := s.sched.List(ctx, f)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []apigen.Schedule{}
	}
	return map[string]any{"schedules": list}, nil
}

type projectItem struct {
	Name            string  `json:"name"`
	RepoURL         *string `json:"repoUrl,omitempty"`
	DefaultProvider *string `json:"defaultProvider,omitempty"`
	DefaultOrbType  *string `json:"defaultOrbType,omitempty"`
}

func (s *Service) toolListProjects(ctx context.Context) (any, error) {
	no := false
	rows, err := s.st.Q.ListProjects(ctx, &no)
	if err != nil {
		return nil, err
	}
	items := make([]projectItem, 0, len(rows))
	for _, p := range rows {
		items = append(items, projectItem{Name: p.Name, RepoURL: p.RepoUrl, DefaultProvider: p.DefaultProvider, DefaultOrbType: p.DefaultOrbType})
	}
	return map[string]any{"projects": items}, nil
}

type statsArgs struct {
	GroupBy string `json:"groupBy"`
	From    *int64 `json:"from"`
	To      *int64 `json:"to"`
}

func (s *Service) toolGetStats(ctx context.Context, c toolCtx) (any, error) {
	var a statsArgs
	if err := decodeArgs(c.Args, &a); err != nil {
		return nil, err
	}
	switch a.GroupBy {
	case "day", "model", "thread", "project":
	default:
		return nil, errf(CodeInvalid, "groupBy must be day, model, thread or project")
	}
	from, to := stats.Window(a.From, a.To, s.cfg.Now())
	return stats.Usage(ctx, s.st.Q, a.GroupBy, from, to)
}

// parseModelRef 解析 "provider/id"。
func parseModelRef(s string) (*apigen.ModelRef, bool) {
	p, id, ok := strings.Cut(s, "/")
	if !ok || p == "" || id == "" {
		return nil, false
	}
	return &apigen.ModelRef{Provider: p, ID: id}, true
}
