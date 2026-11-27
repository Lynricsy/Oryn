package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
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

// HostToolCall 实现 threads.Coordinator：执行宿主工具。结果的 Text 是紧凑 JSON；read_thread / list_threads /
// list_schedules / get_stats / list_files / read_artifact / list_artifacts / list_artifact_versions / read_artifact_comments /
// read_artifact_data / design_notes 的结果含线程标题、定时名称、文件名、artifact 内容、页面文本与页面数据等不可信文本，另加不可信包裹（C9.1）。Details 是同一对象；
// 失败时 IsError、Text 以错误码开头。
func (s *Service) HostToolCall(ctx context.Context, c threads.HostToolCall) threads.HostToolResult {
	tc := toolCtx{HostToolCall: c}
	// M26：研究员对话的工具表随运行变化，放行以控制面记下的当前管线为准（表外一律 unknown_tool）；受限的主线只放行
	// 只读工具与 stay_silent
	if c.Research != "" {
		if err := s.researcherToolGate(c); err != nil {
			return failResult(err)
		}
	}
	if err := s.restrictedToolGate(c); err != nil {
		return failResult(err)
	}
	// Space 协调者的作用域是授权判断，必须 fail-closed：SpaceID 缺失或 Space 加载失败时拒绝一切工具，
	// 不能退回全局助手的权限（sec F6）。
	if c.Kind == threads.KindSpace {
		if c.SpaceID == "" {
			return failResult(errf(CodeForbidden, "space coordinator has no space"))
		}
		sp, err := s.st.Q.GetSpace(ctx, c.SpaceID)
		if errors.Is(err, sql.ErrNoRows) {
			return failResult(errf(CodeForbidden, "space %s no longer exists", c.SpaceID))
		}
		if err != nil {
			slog.Warn("加载 Space 失败，拒绝宿主工具", "space", c.SpaceID, "tool", c.Name, "err", err)
			return failResult(errf(CodeCoordinatorUnavailable, "space could not be loaded"))
		}
		tc.space = &sp
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
		v, err = s.toolListProjects(ctx, tc)
	case ToolGetStats:
		v, err = s.toolGetStats(ctx, tc)
	case ToolListModels:
		// 模型目录查询只给 Space 协调者（全局助手经 oryn 工具的 getModelCatalog 查询）
		if c.Kind != threads.KindSpace {
			err = errf("unknown_tool", "no tool named %q", c.Name)
			break
		}
		v, err = s.toolListModels(ctx, tc)
	case ToolSaveFile, ToolListFiles, ToolShareFile:
		// 文件库只属于全局助手（Space 协调者没有这些工具）
		if c.Kind != threads.KindAssistant {
			err = errf("unknown_tool", "no tool named %q", c.Name)
			break
		}
		switch c.Name {
		case ToolSaveFile:
			v, err = s.toolSaveFile(ctx, tc)
		case ToolListFiles:
			v, err = s.toolListFiles(ctx, tc)
		default:
			v, err = s.toolShareFile(ctx, tc)
		}
	case ToolUpdateNotes:
		// M19 3.2：主线专属（toolUpdateNotes 自己核对是主线）
		v, err = s.toolUpdateNotes(ctx, tc)
	case ToolStaySilent:
		// M19 3.3：主线专属（toolStaySilent 自己核对是主线）
		v, err = s.toolStaySilent(ctx, tc)
	case ToolRecallMemory, ToolSearchHistory, ToolRecallEngineering, ToolRemember:
		// 记忆工具只属于助手记忆正在运行的全局助手
		return s.toolMemory(ctx, tc)
	case ToolCreateArtifact, ToolReadArtifact, ToolUpdateArtifact, ToolListArtifacts, ToolDeleteArtifact,
		ToolListArtifactVersions, ToolRestoreArtifactVersion, ToolReadArtifactGuide,
		ToolReadArtifactComments, ToolReplyArtifactComment, ToolResolveArtifactComment,
		ToolReadArtifactData, ToolWriteArtifactData, ToolScreenshotArtifact,
		ToolCheckArtifact, ToolDesignDirections, ToolDesignDirection, ToolDesignNotes, ToolDesignResolveNote:
		// artifact 工具只属于全局助手；工具名就是 artifacts 的操作名
		return s.toolArtifact(ctx, tc, c.Name)
	case ToolExportArtifact, ToolImportArtifact:
		// M19：artifact 的补充工具（只给全局助手），coord 自己实现
		return s.toolArtifactExtra(ctx, tc)
	case ToolRenderUI, ToolRenderHTML:
		// M33：视图（只给全局助手）
		return s.toolView(ctx, tc)
	case ToolOryn:
		// oryn 技能的操作（只给全局助手）
		if c.Kind != threads.KindAssistant {
			err = errf("unknown_tool", "no tool named %q", c.Name)
			break
		}
		return s.toolOryn(ctx, tc)
	case ToolAbortThread, ToolSetThreadModel:
		// M19 第三层：中止与改模型（只给全局助手，经 oryn 转来）
		if c.Kind != threads.KindAssistant {
			err = errf("unknown_tool", "no tool named %q", c.Name)
			break
		}
		v, err = s.threadTool(ctx, tc)
	case ToolListThreadFiles, ToolReadThreadFile, ToolWriteThreadFile, ToolFetchThreadFile, ToolPutThreadFile, ToolGetThreadChanges:
		// M19 第三层：线程文件（只给全局助手，经 oryn 转来）
		return s.fileTool(ctx, tc)
	default:
		if strings.HasPrefix(c.Name, computerToolPrefix) {
			// M19 第二层：助手的电脑（只给全局助手）
			return s.computerTool(ctx, tc)
		}
		err = errf("unknown_tool", "no tool named %q", c.Name)
	}
	if err != nil {
		return failResult(err)
	}
	switch c.Name {
	case ToolListThreads, ToolListSchedules, ToolGetStats, ToolListFiles, ToolListModels:
		// list_files 的文件名可能来自 orb 内的 agent（oryn library put）；list_models 含模型提供商与名称
		return threads.HostToolResult{Text: wrapUntrusted("", v), Details: v}
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

// resolveTarget 是 Space 协调者写工具的目标校验：线程必须存在且是工作线程。协调对话之间不能互相发消息、归档、读取——
// 这是防止协调者互相唤醒成环的结构规则，不是权限。runner 线程（跑在用户自己的机器上）一律不作目标：协调者读到的
// orb 内容可能带注入的指令，不能经它转成用户机器上的操作（sec S6）→ runner_unsupported。全局助手走 resolveActTarget。
func (s *Service) resolveTarget(ctx context.Context, threadID string) (db.Thread, error) {
	t, err := s.getTarget(ctx, threadID)
	if err != nil {
		return db.Thread{}, err
	}
	if t.Kind != threads.KindWork {
		return db.Thread{}, errf(CodeInvalidTarget, "%s is a coordinator conversation, not a work thread", threadID)
	}
	runner, err := s.isRunnerThread(ctx, t)
	if err != nil {
		return db.Thread{}, err
	}
	if runner {
		return db.Thread{}, errf(threads.CodeRunnerUnsupported, "%s runs on a runner; coordinator tools cannot act on runner threads", threadID)
	}
	return t, nil
}

// resolveActTarget 是写工具的目标校验（M19 D6 / D10）。Space 协调者调用时等同 resolveTarget；全局助手可以操作任何工作线程
// （含 Runner 线程），allowSpace 为真时还可以以 Space 的协调对话为目标（send_message、线程内定时）。助手对话之间
// 仍不能互相操作（防止成环）。
func (s *Service) resolveActTarget(ctx context.Context, c toolCtx, threadID string, allowSpace bool) (db.Thread, error) {
	if c.Kind != threads.KindAssistant {
		return s.resolveTarget(ctx, threadID)
	}
	t, err := s.getTarget(ctx, threadID)
	if err != nil {
		return db.Thread{}, err
	}
	switch {
	case t.Kind == threads.KindWork:
		return t, nil
	case t.Kind == threads.KindSpace && allowSpace:
		return t, nil
	case t.Kind == threads.KindSpace:
		return db.Thread{}, errf(CodeInvalidTarget, "%s is a Space conversation; manage the Space instead", threadID)
	}
	return db.Thread{}, errf(CodeInvalidTarget, "%s is an assistant conversation, not a work thread", threadID)
}

// getTarget 按 id 取线程；空 id → invalid_request，不存在 → not_found。
func (s *Service) getTarget(ctx context.Context, threadID string) (db.Thread, error) {
	if threadID == "" {
		return db.Thread{}, errf(CodeInvalid, "threadId is required")
	}
	t, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Thread{}, errf(CodeNotFound, "thread %s not found", threadID)
	}
	return t, err
}

// isRunnerThread 报告线程是否跑在 runner 上：有 cwd（runner 删除后 orb_id 置空但 cwd 仍在），或所在 orb 是 runner 的常驻 orb。
func (s *Service) isRunnerThread(ctx context.Context, t db.Thread) (bool, error) {
	if t.Cwd != nil {
		return true, nil
	}
	if t.OrbID == nil {
		return false, nil
	}
	o, err := s.st.Q.GetOrb(ctx, *t.OrbID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return orbs.IsRunner(o), nil
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
	ThreadID  string `json:"threadId"`
	Tail      int    `json:"tail"`
	AroundSeq *int64 `json:"aroundSeq"`
	BeforeSeq *int64 `json:"beforeSeq"`
}

func (s *Service) toolReadThread(ctx context.Context, c toolCtx) threads.HostToolResult {
	var a readThreadArgs
	if err := decodeArgs(c.Args, &a); err != nil {
		return failResult(err)
	}
	if a.AroundSeq != nil && a.BeforeSeq != nil {
		return failResult(errf(CodeInvalid, "give aroundSeq or beforeSeq, not both"))
	}
	resolve := s.resolveTarget
	if c.Kind == threads.KindAssistant {
		resolve = s.resolveReadTarget
	}
	t, err := resolve(ctx, a.ThreadID)
	if err != nil {
		return failResult(err)
	}
	n := a.Tail
	if n <= 0 {
		n = defaultReadTail
	}
	n = min(n, maxReadTail)
	msgs, err := s.readMessages(ctx, t, n, a.AroundSeq, a.BeforeSeq)
	if err != nil {
		return failResult(err)
	}
	v := map[string]any{
		"thread":   map[string]any{"id": t.ID, "title": t.Title, "state": t.State, "kind": t.Kind},
		"messages": msgs,
	}
	return threads.HostToolResult{Text: wrapUntrusted(t.ID, v), Details: v}
}

// resolveReadTarget 是全局助手 read_thread 的目标校验（R15 / M19）：除工作线程（含 Runner 线程）外，还可以读取主人的
// 其他助手对话与 Space 的协调对话。读不会唤醒任何线程。
func (s *Service) resolveReadTarget(ctx context.Context, threadID string) (db.Thread, error) {
	return s.getTarget(ctx, threadID)
}

// ---- create_thread ----

type createThreadArgs struct {
	Prompt        string `json:"prompt"`
	Project       string `json:"project"`
	Title         string `json:"title"`
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinkingLevel"`
	OrbType       string `json:"orbType"`
	// Files 是要带给新线程的文件库 id（M13 4.4，只有全局助手可用）：逐个复制成附件，随首条提示词投递进新线程的 orb。
	Files []string `json:"files"`
	// M19：执行位置、Space、role 与标签。除 role 外仅全局助手可用（Space 协调者的 role 用于选择 subagent 配置，其余忽略）。
	assistantCreateArgs
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
	// 首条提示词前加来源头行（C9.3），与 send_message 同形
	p := threads.CreateParams{Prompt: s.sourceLine(c) + "\n" + a.Prompt, Title: a.Title, ThinkingLevel: a.ThinkingLevel, Type: a.OrbType, ParentThreadID: c.ThreadID}
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
	effectiveRole := ""
	if c.inSpace() {
		p.SpaceID = c.space.ID
		if effectiveRole, err = s.applySpaceCreateRole(ctx, a, &p); err != nil {
			return nil, err
		}
	} else if err = s.applyAssistantCreate(ctx, a.assistantCreateArgs, &p, &projectName); err != nil {
		return nil, err
	}
	if !s.lim.allow(c.ThreadID+"|create_thread", s.cfg.Now(), createThreadRules) {
		err = errf(CodeRateLimited, "create_thread limit reached (10 per 10 minutes, 40 per 24 hours per conversation)")
		return nil, err
	}
	if p.Attachments, err = s.threadAttachments(ctx, c, a.Files); err != nil {
		return nil, err
	}
	// 「建线程 + 登记」持 repMu（契约 C3）：首条提示词带登记代号作标签，回报处理看到的必然是写好的登记
	s.repMu.Lock()
	defer s.repMu.Unlock()
	seq0 := s.curSeq()
	gen := ids.New("rep")
	p.Tag = gen
	requestedAt := s.nowMs() // 写在投递之前：重启后按它在转录里找本次投递的锚点（C1.6）
	var t apigen.Thread
	if t, err = s.th.Create(ctx, p); err != nil {
		return nil, err
	}
	newID = t.ID
	if uerr := s.st.Q.UpsertThreadReport(ctx, db.UpsertThreadReportParams{ThreadID: t.ID, ReportTo: c.ThreadID, RequestedAt: requestedAt, Gen: gen}); uerr != nil {
		slog.Warn("coord: 登记回报失败", "thread", t.ID, "reportTo", c.ThreadID, "err", uerr)
	} else {
		s.setArmed(t.ID, armedReport{gen: gen, seq: seq0})
	}
	res := map[string]any{
		"threadId": t.ID, "title": t.Title, "project": projectName, "url": s.threadURL(t.ID), "threadIds": []string{t.ID},
	}
	if effectiveRole != "" {
		res["role"] = effectiveRole
	}
	return res, nil
}

// ---- send_message ----

type sendMessageArgs struct {
	ThreadID string `json:"threadId"`
	// Space 是 Space 名字（M19，只有全局助手可用；与 threadId 二选一）：消息发给该 Space 的协调对话。
	Space   string `json:"space"`
	Message string `json:"message"`
	Steer   bool   `json:"steer"`
}

// sourceLine 是协调者发出的消息（send_message、create_thread 的首条提示词）的来源头行（C9.3）：Space 用其名称
// （经 label 清洗）；助手的名称恒为空串——标题由 agent 控制，不进头行（Web 按 id 取当前标题）。
func (s *Service) sourceLine(c toolCtx) string {
	if c.inSpace() {
		return "[Oryn message from Space \"" + label(c.space.Name) + "\" (" + c.ThreadID + ") — sent by an agent, not typed by the owner]"
	}
	return "[Oryn message from assistant \"\" (" + c.ThreadID + ") — sent by an agent, not typed by the owner]"
}

// threadSourceLine 是 orb 内 agent 发出的消息（thread.send、thread.create 的首条提示词）的来源头行。
func threadSourceLine(callerID string) string {
	return "[Oryn message from thread \"\" (" + callerID + ") — sent by an agent, not typed by the owner]"
}

// label 清洗进头行的名称（C9.3）：控制字符与换行换成空格、`"` 换成 `'`、`[` `]` 换成 `(` `)`、截到 100 字符。
func label(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r) || r == '\u2028' || r == '\u2029':
			return ' '
		case r == '"':
			return '\''
		case r == '[':
			return '('
		case r == ']':
			return ')'
		}
		return r
	}, s)
	return truncRunes(s, 100)
}

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
	if !c.inSpace() && a.Space != "" {
		if a.ThreadID != "" {
			err = errf(CodeInvalid, "give threadId or space, not both")
			return nil, err
		}
		var sp db.Space
		if sp, err = s.spaceByName(ctx, a.Space); err != nil {
			return nil, err
		}
		a.ThreadID = sp.ThreadID
	}
	if _, err = s.resolveActTarget(ctx, c, a.ThreadID, true); err != nil {
		return nil, err
	}
	if !s.lim.allow(c.ThreadID+"|send_message", s.cfg.Now(), sendMessageRules) {
		err = errf(CodeRateLimited, "send_message limit reached (60 per 10 minutes per conversation)")
		return nil, err
	}
	// 「登记 + 投递」持 repMu（契约 C3）：登记写在投递之前，代号同时是投递标签；投递失败时恢复上一份登记
	s.repMu.Lock()
	defer s.repMu.Unlock()
	seq0 := s.curSeq()
	prev, perr := s.st.Q.GetThreadReport(ctx, a.ThreadID)
	hasPrev := perr == nil
	gen, err := s.arm(ctx, a.ThreadID, c.ThreadID, s.nowMs())
	if err != nil {
		return nil, err
	}
	text := s.sourceLine(c) + "\n" + a.Message
	if _, err = s.th.Deliver(ctx, a.ThreadID, threads.AutoMessage{Text: text, Steer: a.Steer, Tag: gen}, nil); err != nil {
		if hasPrev {
			if rerr := s.st.Q.UpsertThreadReport(context.WithoutCancel(ctx), db.UpsertThreadReportParams{
				ThreadID: prev.ThreadID, ReportTo: prev.ReportTo, RequestedAt: prev.RequestedAt, Gen: prev.Gen,
			}); rerr != nil {
				slog.Warn("coord: 恢复回报登记失败", "thread", a.ThreadID, "err", rerr)
			}
		} else if _, derr := s.st.Q.DeleteThreadReportGen(context.WithoutCancel(ctx), db.DeleteThreadReportGenParams{ThreadID: a.ThreadID, Gen: gen}); derr != nil {
			slog.Warn("coord: 撤回回报登记失败", "thread", a.ThreadID, "err", derr)
		}
		return nil, err
	}
	s.setArmed(a.ThreadID, armedReport{gen: gen, seq: seq0})
	if s.afterSend != nil {
		s.afterSend()
	}
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
	if _, err = s.resolveActTarget(ctx, c, a.ThreadID, false); err != nil {
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
	Role          string `json:"role"`
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinkingLevel"`
	OrbType       string `json:"orbType"`
	Overlap       string `json:"overlap"`
	KeepLast      *int   `json:"keepLast"`
	// HandToAssistant 仅全局助手（M19 步骤 3.3）：new_thread 的运行结果交给主线
	HandToAssistant *bool `json:"handToAssistant"`
}

func (s *Service) toolSetSchedule(ctx context.Context, c toolCtx) (any, error) {
	var a setScheduleArgs
	err := decodeArgs(c.Args, &a)
	var (
		sc     apigen.Schedule
		target string
		// pending：全局助手的删除已挂上确认卡，审计在主人作答、真正执行后写
		pending bool
	)
	defer func() {
		if pending {
			return
		}
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
	if !s.lim.allow(c.ThreadID+"|set_schedule", s.cfg.Now(), setScheduleRules) {
		err = errf(CodeRateLimited, "set_schedule limit reached (10 per 10 minutes, 40 per 24 hours per conversation)")
		return nil, err
	}
	// thread 模式的目标：缺省 = 本对话；给了别的线程则按 resolveActTarget 校验（助手还可指向 Runner 线程与 Space 对话）。main 表示目标是主线（只可能是本对话）
	threadTarget := func() (tid string, main bool, err error) {
		if a.ThreadID == "" || a.ThreadID == c.ThreadID {
			row, err := s.st.Q.GetThread(ctx, c.ThreadID)
			if err != nil {
				return "", false, err
			}
			return c.ThreadID, row.Main == 1, nil
		}
		if _, err := s.resolveActTarget(ctx, c, a.ThreadID, true); err != nil {
			return "", false, err
		}
		return a.ThreadID, false, nil
	}
	switch a.Action {
	case "set":
		sc, target, err = s.putSchedule(ctx, c, a, threadTarget)
	case "pause", "resume", "delete":
		id := a.ScheduleID
		if id == "" {
			tid, main, terr := threadTarget()
			if terr != nil {
				err = terr
				return nil, err
			}
			if main {
				err = errf(CodeInvalid, "the main conversation can have several reminders: pass scheduleId (list_schedules shows them)")
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
			if _, err = s.resolveActTarget(ctx, c, *sc.ThreadID, true); err != nil {
				return nil, err
			}
		}
		switch a.Action {
		case "delete":
			if !c.inSpace() {
				// 全局助手删除定时要主人确认（M19 D7）；Space 协调者仍直接删除
				var v any
				if v, err = s.requestDeleteSchedule(ctx, c, sc); err != nil {
					return nil, err
				}
				pending = true
				return v, nil
			}
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
func (s *Service) putSchedule(ctx context.Context, c toolCtx, a setScheduleArgs, threadTarget func() (string, bool, error)) (apigen.Schedule, string, error) {
	mode := a.Mode
	if mode == "" {
		mode = string(apigen.ScheduleModeThread)
	}
	in := schedule.Input{
		Name: a.Name, Kind: a.Kind, Cron: a.Cron, Every: a.Every, Timezone: a.Timezone, Prompt: a.Prompt,
		At: a.At, EndsAt: a.EndsAt, Enabled: a.Enabled, Mode: mode,
	}
	if c.Kind == threads.KindAssistant {
		// 交给助手只给全局助手；thread 模式带 true 由 Validate 拒绝（invalid_schedule）
		in.HandToAssistant = a.HandToAssistant
	}
	switch mode {
	case string(apigen.ScheduleModeThread):
		tid, main, err := threadTarget()
		if err != nil {
			return apigen.Schedule{}, "", err
		}
		in.ThreadID = tid
		if a.ScheduleID != "" {
			sc, err := s.sched.Put(ctx, a.ScheduleID, in, "agent")
			return sc, tid, err
		}
		if main {
			// 主线可有多条提醒：不带 scheduleId 一律新建，按名字区分
			if strings.TrimSpace(a.Name) == "" {
				return apigen.Schedule{}, "", errf(CodeInvalidSchedule, "reminders on the main conversation need a name; to change one, pass its scheduleId")
			}
			sc, err := s.sched.Put(ctx, "", in, "agent")
			return sc, tid, err
		}
		sc, err := s.sched.PutForThread(ctx, tid, in, "agent")
		return sc, tid, err
	case string(apigen.ScheduleModeNewThread):
		in.Role, in.ThinkingLevel, in.OrbType, in.Overlap, in.KeepLast = a.Role, a.ThinkingLevel, a.OrbType, a.Overlap, a.KeepLast
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

// deleteScheduleArgs 是助手 set_schedule{action: delete} 的确认执行参数。
type deleteScheduleArgs struct {
	ScheduleID string `json:"scheduleId"`
}

// requestDeleteSchedule 给全局助手的定时删除挂确认卡（M19 D7）；动作名与审计沿用 delete_schedule。
func (s *Service) requestDeleteSchedule(ctx context.Context, c toolCtx, sc apigen.Schedule) (any, error) {
	if err := s.allowMutate(c); err != nil {
		return nil, err
	}
	name := label(sc.Name)
	what := fmt.Sprintf("the schedule %q (%s)", name, sc.ID)
	if sc.Name == "" {
		what = "the schedule " + sc.ID
	}
	r := confirmReq{
		Action: string(apigen.ActionDeleteSchedule), Target: sc.ID, TargetLabel: name,
		Title:         fmt.Sprintf("Delete schedule %q (%s)?", name, sc.ID),
		Summary:       "The assistant wants to delete " + what + ".",
		AuditSchedule: sc.ID, Args: deleteScheduleArgs{ScheduleID: sc.ID},
	}
	if sc.ThreadID != nil {
		r.AuditThread = *sc.ThreadID
	}
	return s.requestConfirm(ctx, c, r)
}

// confirmDeleteSchedule 是 delete_schedule 的确认执行函数。
func (s *Service) confirmDeleteSchedule(ctx context.Context, _ db.AssistantConfirmation, args json.RawMessage) (string, error) {
	var a deleteScheduleArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if s.sched == nil {
		return "", errf(CodeCoordinatorUnavailable, "schedules are not available")
	}
	return "", s.sched.Delete(ctx, a.ScheduleID)
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
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	RepoURL         *string `json:"repoUrl,omitempty"`
	DefaultProvider *string `json:"defaultProvider,omitempty"`
	DefaultOrbType  *string `json:"defaultOrbType,omitempty"`
	// Archived 只在全局助手的结果里出现（M19；Space 协调者的结果不变）。
	Archived *bool `json:"archived,omitempty"`
}

// toolListProjects 列出未归档的项目；全局助手传 archived=true 时含已归档的，每项带 archived。
func (s *Service) toolListProjects(ctx context.Context, c toolCtx) (any, error) {
	var a struct {
		Archived bool `json:"archived"`
	}
	if !c.inSpace() {
		if err := decodeArgs(c.Args, &a); err != nil {
			return nil, err
		}
	}
	no := false
	filter := &no
	if a.Archived {
		filter = nil
	}
	rows, err := s.st.Q.ListProjects(ctx, filter)
	if err != nil {
		return nil, err
	}
	items := make([]projectItem, 0, len(rows))
	for _, p := range rows {
		it := projectItem{ID: p.ID, Name: p.Name, RepoURL: p.RepoUrl, DefaultProvider: p.DefaultProvider, DefaultOrbType: p.DefaultOrbType}
		if !c.inSpace() {
			it.Archived = new(p.ArchivedAt != nil)
		}
		items = append(items, it)
	}
	return map[string]any{"projects": items}, nil
}

// statsArgs 是 get_stats 的参数（计划 M10 3.9）。
type statsArgs struct {
	Kind    string `json:"kind"`
	GroupBy string `json:"groupBy"`
	Series  string `json:"series"`
	From    *int64 `json:"from"`
	To      *int64 `json:"to"`
	Tz      string `json:"tz"`
	Project string `json:"project"`
	Space   string `json:"space"`
	Thread  string `json:"thread"`
	Model   string `json:"model"`
	Limit   int    `json:"limit"`
}

// get_stats 的组数：缺省 10，上限 30（超出按上限）。
const (
	statsDefaultLimit = 10
	statsMaxLimit     = 30
)

// toolGetStats 返回与对应 REST 相同的 JSON。Space 协调者：space 强制为本 Space，kind=orbs 被拒（orb 不属于 Space）；
// 全局助手不受限。project / space 接受名称或 id（以及 none）。
func (s *Service) toolGetStats(ctx context.Context, c toolCtx) (any, error) {
	var a statsArgs
	if err := decodeArgs(c.Args, &a); err != nil {
		return nil, err
	}
	if a.Kind == "" {
		a.Kind = "usage"
	}
	if c.inSpace() {
		if a.Kind == "orbs" {
			return nil, errf(CodeForbidden, "orb stats are not available to a Space coordinator")
		}
		a.Space = c.space.ID
	}
	limit := a.Limit
	switch {
	case limit <= 0:
		limit = statsDefaultLimit
	case limit > statsMaxLimit:
		limit = statsMaxLimit
	}
	loc, err := stats.LoadLocation(a.Tz)
	if err != nil {
		return nil, statsErr(err)
	}
	from, to := stats.Window(a.From, a.To, s.cfg.Now())
	project, err := s.statsProject(ctx, a.Project)
	if err != nil {
		return nil, err
	}
	space, err := s.statsSpace(ctx, a.Space)
	if err != nil {
		return nil, err
	}
	f := stats.Filter{Project: project, Space: space, Thread: optStr(a.Thread), Model: optStr(a.Model)}
	var v any
	switch a.Kind {
	case "usage":
		if a.GroupBy == "" {
			a.GroupBy = "model"
		}
		v, err = stats.Usage(ctx, s.st.Q, stats.UsageQuery{GroupBy: a.GroupBy, Series: a.Series, From: from, To: to, Loc: loc, Filter: f, Limit: limit})
	case "tools":
		v, err = stats.Tools(ctx, s.st.Q, stats.ToolQuery{Series: a.Series, From: from, To: to, Loc: loc, Filter: f, Limit: limit})
	case "orbs":
		v, err = stats.Orbs(ctx, s.st.Q, stats.OrbQuery{GroupBy: a.GroupBy, From: from, To: to, Loc: loc, Project: project}, s.cfg.Now().UnixMilli())
	case "output":
		v, err = stats.Output(ctx, s.st.Q, stats.OutputQuery{GroupBy: a.GroupBy, From: from, To: to, Loc: loc, Project: project, Space: space})
	default:
		return nil, errf(CodeInvalid, "kind must be usage, tools, orbs or output")
	}
	if err != nil {
		return nil, statsErr(err)
	}
	return v, nil
}

// statsErr 把统计的参数错误换成工具错误码（其余原样返回）。
func statsErr(err error) error {
	var se *stats.Error
	if errors.As(err, &se) {
		return errf(se.Code, "%s", se.Message)
	}
	return err
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// statsProject 把项目名称或 id（或 none）解析成筛选值（项目 id / "none"）；空 = 不限。
func (s *Service) statsProject(ctx context.Context, ref string) (*string, error) {
	if ref == "" || ref == "none" {
		return optStr(ref), nil
	}
	if p, err := s.st.Q.GetProject(ctx, ref); err == nil {
		return &p.ID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	p, err := s.projectByName(ctx, ref)
	if err != nil {
		return nil, err
	}
	return &p.ID, nil
}

// statsSpace 把 Space 名称或 id（或 none）解析成筛选值（Space id / "none"）；空 = 不限。
func (s *Service) statsSpace(ctx context.Context, ref string) (*string, error) {
	if ref == "" || ref == "none" {
		return optStr(ref), nil
	}
	if sp, err := s.st.Q.GetSpace(ctx, ref); err == nil {
		return &sp.ID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	sp, err := s.st.Q.GetSpaceByName(ctx, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeNotFound, "space %q not found", ref)
	}
	if err != nil {
		return nil, err
	}
	return &sp.ID, nil
}

// parseModelRef 解析 "provider/id"。
func parseModelRef(s string) (*apigen.ModelRef, bool) {
	p, id, ok := strings.Cut(s, "/")
	if !ok || p == "" || id == "" {
		return nil, false
	}
	return &apigen.ModelRef{Provider: p, ID: id}, true
}
