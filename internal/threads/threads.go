package threads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// CreateParams 是新建线程的参数（对应 `POST threads` 请求体）。
type CreateParams struct {
	ProjectID string
	Prompt    string
	Images    []apigen.ImageContent
	// OrbID 非空时挂到已有 orb；否则新建 orb（Provider / Type 为空时由 orbs 取默认）。
	OrbID          string
	Provider, Type string
	Model          *apigen.ModelRef
	ThinkingLevel  string
	Title          string
	Labels         []string
	// Attachments 是已上传附件的 id：建线程时绑定，首条提示词在 orb 与 agent 就绪、附件投递之后才下发。
	Attachments []string
	// BrowserProfileID 是新 orb 选用的浏览器 Profile（M8）：nil = 项目默认；空串 = 不用；否则为 bpf_…。
	// 只对新建 orb 有效；挂到已有 orb 时给非空值 → invalid。
	BrowserProfileID *string
	// ProfileMode 是选用方式（空 = exclusive）。
	ProfileMode apigen.BrowserProfileMode
	// SpaceID 非空时新线程加入该 Space（M9；须存在）。
	SpaceID string
	// ParentThreadID 是发起它的线程（协调对话或 orb 内 agent，M9）。
	ParentThreadID string
	// ScheduleID / ScheduleRun 记录创建它的「新建线程」定时与运行序号（M9）。
	ScheduleID  string
	ScheduleRun int64
	// Tag 非空时跟踪首条提示词的终态：恰好得到一次 Observer.TurnEnded（契约 C1）；空 = 不跟踪。
	Tag string
	// RunnerID 非空时线程跑在该 runner 上（M11b，与 OrbID / Provider / Type 互斥）：Dir 是工作目录（须在 runner 声明的
	// 目录之内）；Worktree 非空时先在 Dir 旁边建同名 worktree，线程的工作目录取它；ShareDir 为真时允许与其它线程共用 Dir。
	RunnerID, Dir string
	Worktree      string
	ShareDir      bool
	// Role 非空时新线程以该 omp 模型 role 开场：首批命令在 prompt 之前先发 oryn_select_model{role}（写入模型来源
	// 记录，回合头据此显示 role 身份）；同时给了 ThinkingLevel 时紧接着再发 set_thinking_level，用户选的思考档位
	// 压过 role 自带的档位。只作用于首条提示词：之后 agent 重启时不再重选（会话里已有模型来源记录）。
	Role string
	// Automation 为真表示由全自动入口创建——GitHub 触发、项目级定时、发布哨兵（M14 / M15）：新 orb 的 GitHub 身份由
	// GitHub.ResolveAutomationSlot 决定（R6），回退到我的账号时把回退提示追加到首条提示词末尾（Q6）。
	Automation bool
	// Untrusted 与 Automation 同用：入口由不可信输入驱动（「任何人」档规则、外部 PR 评审，M15 Q6）。身份只用 Agent 账号，
	// Agent 槽位不可用时 Create 原样返回 GitHub.ResolveAutomationSlot 的错误，不回退、不建线程。
	Untrusted bool
	// InitTx 非 nil 时在写线程行与首批命令的同一事务里执行（SetThreadStartupFrames 之后），threadID 是新线程的 id：
	// 调用方据此写入与线程同生共死的初始化（如 GitHub 任务的 review_only，M15）。返回错误时整个 Create 失败，
	// 按建线程失败清理（删本次新建的 orb、worktree），不下发任何帧。
	InitTx func(ctx context.Context, q *db.Queries, threadID string) error
}

// maxRoleLen 是新建线程时 role 名的长度上限（字节），与 role 头像接口的校验一致。
const maxRoleLen = 128

// githubIdentity 决定新 orb 的 GitHub 身份槽位（M14 / M15 R6）：orb 内派生的子线程沿用父线程 orb 的身份；全自动入口按
// GitHub.ResolveAutomationSlot；其余一律 user。note 非空时是要写进线程的回退提示。
func (s *Service) githubIdentity(ctx context.Context, p CreateParams) (slot, note string, err error) {
	if p.ParentThreadID != "" {
		parent, err := s.st.Q.GetThread(ctx, p.ParentThreadID)
		if err == nil && parent.OrbID != nil {
			if o, err := s.st.Q.GetOrb(ctx, *parent.OrbID); err == nil && o.GithubIdentity != "" {
				return o.GithubIdentity, "", nil
			}
		}
		return "user", "", nil
	}
	if p.Automation && s.gh != nil {
		return s.gh.ResolveAutomationSlot(ctx, p.Untrusted)
	}
	return "user", "", nil
}

// orbAutomation 决定新 orb 是否标为全自动（M18 D3）：orb 内派生的子线程沿用父线程 orb 的标记，其余取 p.Automation。
// 子线程本身带了 Automation 时同样标为全自动（只会更严）。
func (s *Service) orbAutomation(ctx context.Context, p CreateParams) bool {
	if p.ParentThreadID != "" {
		parent, err := s.st.Q.GetThread(ctx, p.ParentThreadID)
		if err == nil && parent.OrbID != nil {
			if o, err := s.st.Q.GetOrb(ctx, *parent.OrbID); err == nil && o.Automation {
				return true
			}
		}
	}
	return p.Automation
}

// Create 按 H4.3 新建线程：建线程（provisioning）→ 需要时建 orb 并入队 spawn → orb ready 后 agent.start → 首条 prompt。
func (s *Service) Create(ctx context.Context, p CreateParams) (apigen.Thread, error) {
	if strings.TrimSpace(p.Prompt) == "" && len(p.Images) == 0 && len(p.Attachments) == 0 {
		return apigen.Thread{}, errf("invalid", "prompt, images or attachments required")
	}
	role := strings.TrimSpace(p.Role)
	if len(role) > maxRoleLen || strings.ContainsFunc(role, unicode.IsControl) {
		return apigen.Thread{}, errf("invalid", "invalid role")
	}
	if len(p.Attachments) > 0 {
		if s.attach == nil {
			return apigen.Thread{}, errf("invalid", "attachments are not supported")
		}
		if len(p.Attachments) > maxAttachmentsPerMessage {
			return apigen.Thread{}, errf("invalid", fmt.Sprintf("at most %d attachments per message", maxAttachmentsPerMessage))
		}
		// 预检在建 orb 之前：附件不存在或属于别的线程时不白建 orb
		if err := s.attach.Check(ctx, p.Attachments); err != nil {
			return apigen.Thread{}, asThreadErr(err)
		}
	}
	if p.ProjectID != "" {
		if _, err := s.st.Q.GetProject(ctx, p.ProjectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apigen.Thread{}, errf("not_found", "project not found")
			}
			return apigen.Thread{}, err
		}
	}
	if p.Provider == orbs.HostProviderName {
		return apigen.Thread{}, errf(CodeCoordinatorRestricted, "provider "+orbs.HostProviderName+" is reserved for coordinator conversations")
	}
	if p.SpaceID != "" {
		// 预检在建 orb 之前（不白建 orb）；写引用的事务里再复核一次（契约 C4，见 insertThread）
		if _, err := s.st.Q.GetSpace(ctx, p.SpaceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apigen.Thread{}, errf("space_not_found", "space not found")
			}
			return apigen.Thread{}, err
		}
	}
	title := cleanTitle(p.Title)
	explicitProfile := p.BrowserProfileID != nil && *p.BrowserProfileID != ""
	if p.OrbID != "" && explicitProfile {
		return apigen.Thread{}, errf("invalid", "browserProfileId cannot be set when attaching to an existing orb")
	}
	if p.RunnerID != "" && (p.OrbID != "" || p.Provider != "" || p.Type != "") {
		return apigen.Thread{}, errf("invalid", "a runner thread cannot also name an orb, provider or type")
	}
	var orb db.Orb
	newOrb := false
	var rt *runnerTarget
	switch {
	case p.RunnerID != "":
		// worktree.add 在建线程行之前：失败时不留下线程
		t, err := s.prepareRunner(ctx, p)
		if err != nil {
			return apigen.Thread{}, err
		}
		rt, orb = t, t.orb
	case p.OrbID != "":
		o, err := s.st.Q.GetOrb(ctx, p.OrbID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (o.DeletedAt != nil || o.State == orbStateDeleted || o.State == "deleting")) {
			return apigen.Thread{}, errf("not_found", "orb not found")
		}
		if err != nil {
			return apigen.Thread{}, err
		}
		orb = o
		if orbs.IsRunner(o) {
			// runner 线程的工作目录只能经 runner executor 给出（校验目录、在线与 dir_in_use）；不替它猜默认目录
			return apigen.Thread{}, errf(CodeRunnerUnsupported, "use executor kind=runner with a directory to start a thread on a runner")
		}
		if o.Coordinator {
			return apigen.Thread{}, errf(CodeCoordinatorRestricted, "threads cannot be attached to the coordinator orb")
		}
		if o.PoolState != nil {
			// 池 orb 只能经认领进入线程（认领会刷新仓库并让它离开池）；直接挂上去会与对账回收竞争
			return apigen.Thread{}, errf(CodeOrbIsPooled, "orb belongs to the warm pool")
		}
		if o.Computer == 1 {
			return apigen.Thread{}, errf(CodeOrbIsComputer, "orb is the assistant's computer; threads cannot run on it")
		}
	default:
		var profileID string
		switch {
		case s.profiles != nil:
			id, err := s.profiles.ResolveForNewOrb(ctx, p.BrowserProfileID, p.ProjectID, p.ProfileMode)
			if err != nil {
				return apigen.Thread{}, asThreadErr(err)
			}
			profileID = id
		case explicitProfile:
			return apigen.Thread{}, errf("invalid", "browser profiles are not supported")
		}
		slot, note, err := s.githubIdentity(ctx, p)
		if err != nil {
			return apigen.Thread{}, err
		}
		if note != "" {
			p.Prompt += "\n\n" + note
		}
		req := OrbCreate{
			Provider: p.Provider, Type: p.Type, ProjectID: p.ProjectID, BrowserProfileID: profileID, ProfileMode: string(p.ProfileMode),
			GithubIdentity: slot, Automation: s.orbAutomation(ctx, p),
		}
		// 预热池（M11a）：只给有项目、不选浏览器 Profile 的线程认领（Profile 须在 ready 之前装载，池 orb 早已过了那一步）
		var claimed bool
		if profileID == "" && p.ProjectID != "" {
			orb, claimed = s.orbs.ClaimPool(ctx, req)
		}
		if !claimed {
			o, err := s.orbs.Create(ctx, req)
			if err != nil {
				return apigen.Thread{}, errf("orb_create_failed", err.Error())
			}
			orb = o
		}
		newOrb = true
	}

	id := ids.Thread()
	row := db.CreateThreadParams{
		Kind: KindWork, ID: id, OrbID: &orb.ID, TitleSource: "auto", State: StateProvisioning, SessionDir: sessionDirOf(id),
		Model: modelString(p.Model), ShortCode: shortCode(id),
	}
	if rt != nil {
		// 项目只作归类，不触发 clone
		row.Cwd, row.Worktree = &rt.cwd, worktreeJSON(rt.worktree)
	}
	if p.SpaceID != "" {
		row.SpaceID = &p.SpaceID
	}
	if p.ParentThreadID != "" {
		row.ParentThreadID = &p.ParentThreadID
	}
	if p.ScheduleID != "" {
		row.ScheduleID, row.ScheduleRun = &p.ScheduleID, &p.ScheduleRun
	}
	if p.ProjectID != "" {
		row.ProjectID = &p.ProjectID
	}
	if title != "" {
		row.Title, row.TitleSource = title, "user"
	}
	if p.ThinkingLevel != "" {
		row.ThinkingLevel = &p.ThinkingLevel
	}
	prompt := map[string]any{"type": "prompt", "message": p.Prompt}
	if len(p.Images) > 0 {
		prompt["images"] = p.Images
	}
	if len(p.Attachments) > 0 {
		// 随首批命令持久化：重启后仍按「投递后再下发」续作（见 queueStartupFrameLocked）
		prompt[orynAttachmentsKey] = p.Attachments
	}
	if p.Tag != "" {
		// 投递标签随首批命令持久化，下发前由 queueStartupFrameLocked 取出（契约 C1 规则 1）
		prompt[orynTagKey] = p.Tag
	}
	frame, _ := json.Marshal(prompt)
	var frames [][]byte
	if role != "" {
		f, _ := json.Marshal(map[string]string{"type": "oryn_select_model", "role": role})
		frames = append(frames, f)
		if p.ThinkingLevel != "" {
			f, _ = json.Marshal(map[string]string{"type": "set_thinking_level", "level": p.ThinkingLevel})
			frames = append(frames, f)
		}
	}
	frames = append(frames, frame)
	if title != "" {
		f, _ := json.Marshal(map[string]string{"type": "set_session_name", "name": title})
		frames = append(frames, f)
	}
	// 未要求 worktree、未给 shareDir 的 runner 线程独占工作目录：检查与写线程行同一事务（BEGIN IMMEDIATE 串行），
	// 并发的同目录请求恰好一条成功
	var check func(q *db.Queries) error
	if rt != nil && rt.worktree == nil && !p.ShareDir {
		check = func(q *db.Queries) error { return checkDirFreeTx(ctx, q, orb.ID, rt.cwd) }
	}
	if s.afterSpacePrecheck != nil {
		s.afterSpacePrecheck()
	}
	// 首批命令与线程行同一事务落库，应答 201 之前即已持久：重启不会丢掉已受理的提示词（CP5）
	startup := startupFrames(frames...)
	if err := s.insertThread(ctx, row, p.Labels, check, func(q *db.Queries) error {
		if err := q.SetThreadStartupFrames(ctx, db.SetThreadStartupFramesParams{StartupFrames: startup, ID: id}); err != nil {
			return err
		}
		if len(p.Attachments) > 0 {
			if err := s.attach.BindTx(ctx, q, id, p.Attachments); err != nil {
				return err
			}
		}
		if p.InitTx != nil {
			return p.InitTx(ctx, q, id)
		}
		return nil
	}); err != nil {
		if newOrb {
			// 线程没能写入（例如 Space 在预检之后被删）：删掉本次新建的 orb，不留孤儿
			if derr := s.orbs.Delete(context.WithoutCancel(ctx), orb.ID); derr != nil {
				slog.Warn("threads: 删除建线程失败后的 orb 失败", "orb", orb.ID, "err", derr)
			}
		}
		if rt != nil && rt.worktree != nil {
			s.dropWorktree(context.WithoutCancel(ctx), orb.ID, rt.worktree)
		}
		return apigen.Thread{}, asThreadErr(err)
	}
	// 加载线程时按 startup_frames 排队（见 get / queueStartupLocked）
	t, err := s.get(ctx, id)
	if err != nil {
		return apigen.Thread{}, err
	}
	t.mu.Lock()
	// orb 先于线程行创建：在此之前到达的 orb 事件（例如 clone 立刻失败 → failed）找不到本线程，
	// 绑定后补算一次状态。事件处理先写 orb 状态缓存再列线程，两种先后顺序都不会漏。
	s.recompute(ctx, t)
	t.mu.Unlock()
	out, err := s.Get(ctx, id)
	if err != nil {
		return apigen.Thread{}, err
	}
	s.out().IndexUpsert(out)
	// 不变式：wake / scheduleStart（首条提示词因此可能开始执行）只在上面的事务提交之后发生，InitTx 写入的行此时
	// 必已可读——出站代理按线程任务现查的门禁（review_only 等）没有「线程已在跑、任务还没写」的窗口
	if p.OrbID != "" || rt != nil {
		s.wake(id, orb.ID)
	} else if s.orbState(orb.ID) == orbStateReady {
		s.scheduleStart(id)
	}
	return out, nil
}

// insertThread 在一个事务里写入线程行、标签与 extra（持久化的续作）。工作线程带 Space 时，事务的第一条语句复核
// Space 仍存在（契约 C4：存储是单连接 + BEGIN IMMEDIATE，事务彼此串行，复核与写引用之间不会插入删除）。
// 线程挂在 runner 上时同样复核 runner 仍在用（删除 runner 先提交 → 建线程失败；后提交 → 其 deleted 事件必能列到本线程）。
// check 非 nil 时在写线程行之前执行（同一事务）。
func (s *Service) insertThread(ctx context.Context, row db.CreateThreadParams, labels []string, check, extra func(q *db.Queries) error) error {
	now := s.now()
	row.StateChangedAt, row.CreatedAt, row.UpdatedAt = now, now, now
	return s.st.Tx(ctx, func(q *db.Queries) error {
		if row.Kind == KindWork && row.SpaceID != nil {
			if err := checkSpaceTx(ctx, q, *row.SpaceID); err != nil {
				return err
			}
		}
		if row.OrbID != nil {
			if err := checkOrbTx(ctx, q, *row.OrbID); err != nil {
				return err
			}
		}
		if check != nil {
			if err := check(q); err != nil {
				return err
			}
		}
		if err := q.CreateThread(ctx, row); err != nil {
			return err
		}
		if extra != nil {
			if err := extra(q); err != nil {
				return err
			}
		}
		for _, l := range labels {
			if l = strings.TrimSpace(l); l != "" {
				if err := q.AddThreadLabel(ctx, db.AddThreadLabelParams{ThreadID: row.ID, Label: l}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Get 返回线程的 API 形态。
func (s *Service) Get(ctx context.Context, id string) (apigen.Thread, error) {
	row, err := s.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.Thread{}, ErrNotFound
	}
	if err != nil {
		return apigen.Thread{}, err
	}
	return s.threadAPI(ctx, row)
}

// ListFilter 对应 `GET threads` 的查询参数。
type ListFilter struct {
	State   string
	Project string // "none" = 只要无项目线程
	// Archived 为 nil 时只返回未归档线程。
	Archived *bool
	Q        string
	// Kind 为空时只列工作线程；"all" = 全部种类；否则只列该种类（M9）。
	Kind string
	// Space 非空时只列属于该 Space 的线程（M9）。
	Space string
}

// List 返回线程列表（置顶优先、最近活动优先）。
func (s *Service) List(ctx context.Context, f ListFilter) ([]apigen.Thread, error) {
	all, err := s.st.Q.ListThreads(ctx, db.ListThreadsParams{})
	if err != nil {
		return nil, err
	}
	labelRows, err := s.st.Q.ListAllThreadLabels(ctx)
	if err != nil {
		return nil, err
	}
	labels := map[string][]string{}
	for _, l := range labelRows {
		labels[l.ThreadID] = append(labels[l.ThreadID], l.Label)
	}
	children := childCountsOf(all)
	shareRows, err := s.st.Q.ListActiveThreadShareCounts(ctx)
	if err != nil {
		return nil, err
	}
	shares := make(map[string]int, len(shareRows))
	for _, r := range shareRows {
		shares[r.ThreadID] = int(r.N)
	}
	schedRows, err := s.st.Q.ListScheduleSummaries(ctx)
	if err != nil {
		return nil, err
	}
	schedules := make(map[string]*apigen.ThreadScheduleSummary, len(schedRows))
	for _, r := range schedRows {
		schedules[r.ThreadID] = &apigen.ThreadScheduleSummary{Enabled: r.Enabled, NextRunAt: r.NextRunAt}
	}
	pullRows, err := s.st.Q.ListThreadPullSummaries(ctx)
	if err != nil {
		return nil, err
	}
	pulls := map[string][]apigen.ThreadPull{}
	for _, r := range pullRows {
		pulls[r.ThreadID] = append(pulls[r.ThreadID], threadPullAPI(db.ListThreadPullsRow(r)))
	}
	taskRows, err := s.st.Q.ListThreadTaskSummaries(ctx)
	if err != nil {
		return nil, err
	}
	relRows, err := s.st.Q.ListThreadReleaseSummaries(ctx)
	if err != nil {
		return nil, err
	}
	// 同 threadTask：每线程按时间升序，留下最新的一条；issue 任务优先，发布哨兵只补没有 issue 任务的线程。
	tasks := make(map[string]*apigen.GitHubThreadTask, len(taskRows)+len(relRows))
	for _, r := range relRows {
		tasks[r.ThreadID] = releaseTaskAPI(r)
	}
	for _, r := range taskRows {
		tasks[r.ThreadID] = issueTaskAPI(r)
	}
	archived := false
	if f.Archived != nil {
		archived = *f.Archived
	}
	kind := f.Kind
	if kind == "" {
		kind = KindWork
	}
	q := strings.ToLower(f.Q)
	reasons := map[string]*apigen.ThreadError{}
	out := make([]apigen.Thread, 0, len(all))
	for _, t := range all {
		if (t.ArchivedAt != nil) != archived || (f.State != "" && t.State != f.State) {
			continue
		}
		if (kind != "all" && t.Kind != kind) || (f.Space != "" && (t.SpaceID == nil || *t.SpaceID != f.Space)) {
			continue
		}
		switch {
		case f.Project == "none" && t.ProjectID != nil:
			continue
		case f.Project != "" && f.Project != "none" && (t.ProjectID == nil || *t.ProjectID != f.Project):
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(t.Title), q) && !anyContains(labels[t.ID], q) {
			continue
		}
		api := ThreadToAPI(t, labels[t.ID])
		api.PortalURLTemplate = s.portalTemplate(t.ID)
		api.Error = s.threadError(ctx, t, reasons)
		if c, ok := children[t.ID]; ok {
			api.ChildCount, api.ChildDoneCount = &c[0], &c[1]
		}
		api.Activity = s.activityPtr(t.ID)
		if n := shares[t.ID]; n > 0 {
			api.PublicShares = &n
		}
		if t.Main == 0 {
			// 主线有多条提醒，单数的 schedule 摘要不适用
			api.Schedule = schedules[t.ID]
		}
		api.Pulls = pulls[t.ID]
		api.GithubTask = tasks[t.ID]
		out = append(out, api)
	}
	return out, nil
}

func anyContains(ls []string, q string) bool {
	for _, l := range ls {
		if strings.Contains(strings.ToLower(l), q) {
			return true
		}
	}
	return false
}

// Index 返回 WS `index` 的线程摘要（全部未归档与已归档线程）。
func (s *Service) Index(ctx context.Context) ([]apigen.Thread, error) {
	live, err := s.List(ctx, ListFilter{Kind: "all"})
	if err != nil {
		return nil, err
	}
	yes := true
	arch, err := s.List(ctx, ListFilter{Archived: &yes, Kind: "all"})
	if err != nil {
		return nil, err
	}
	return append(live, arch...), nil
}

// Patch 对应 `PATCH threads/{id}`：只改出现的字段；color 传空串清除，snoozedUntil 传 0 清除。
func (s *Service) Patch(ctx context.Context, id string, p apigen.ThreadPatch) (apigen.Thread, error) {
	t, err := s.get(ctx, id)
	if err != nil {
		return apigen.Thread{}, err
	}
	if t.main && ((p.Archived != nil && *p.Archived) || (p.Shipped != nil && *p.Shipped)) {
		return apigen.Thread{}, errf(CodeMainConversation, "the main conversation cannot be archived")
	}
	var newSpace *string // p.SpaceID 给出时的新值（nil = 移出）
	if p.SpaceID != nil {
		if t.kind != KindWork {
			return apigen.Thread{}, errf(CodeCoordinatorRestricted, "only work threads can join a Space")
		}
		if *p.SpaceID != "" {
			if _, err := s.st.Q.GetSpace(ctx, *p.SpaceID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return apigen.Thread{}, errf("space_not_found", "space not found")
				}
				return apigen.Thread{}, err
			}
			newSpace = p.SpaceID
		}
	}
	if s.afterSpacePrecheck != nil {
		s.afterSpacePrecheck()
	}
	t.mu.Lock()
	row, err := s.st.Q.GetThread(ctx, id)
	if err != nil {
		t.mu.Unlock()
		return apigen.Thread{}, err
	}
	now := s.now()
	m := db.UpdateThreadMetaParams{
		ID: id, Title: row.Title, TitleSource: row.TitleSource, Color: row.Color, Pinned: row.Pinned,
		SnoozedUntil: row.SnoozedUntil, ArchivedAt: row.ArchivedAt, Unread: row.Unread, UpdatedAt: now,
	}
	renamed := ""
	if p.Title != nil {
		// 清洗后为空视为没给（C9.4）
		if title := cleanTitle(*p.Title); title != "" && title != row.Title {
			m.Title, m.TitleSource = title, "user"
			renamed = title
		}
	}
	if p.Color != nil {
		m.Color = nil
		if *p.Color != "" {
			m.Color = p.Color
		}
	}
	if p.Pinned != nil {
		m.Pinned = *p.Pinned
	}
	if p.SnoozedUntil != nil {
		m.SnoozedUntil = nil
		if *p.SnoozedUntil != 0 {
			m.SnoozedUntil = p.SnoozedUntil
		}
	}
	if p.Archived != nil {
		switch {
		case *p.Archived && row.ArchivedAt == nil:
			m.ArchivedAt = &now
		case !*p.Archived:
			m.ArchivedAt = nil
		}
	}
	shipped := row.ShippedAt
	if p.Shipped != nil {
		if *p.Shipped {
			shipped = &now
			if m.ArchivedAt == nil {
				// 标记为已交付即同时归档
				m.ArchivedAt = &now
			}
		} else {
			shipped = nil
		}
	}
	if p.Unread != nil {
		m.Unread = *p.Unread
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.UpdateThreadMeta(ctx, m); err != nil {
			return err
		}
		if p.SpaceID != nil {
			if newSpace != nil {
				// 预检之后 Space 可能已被删：在写引用的事务里复核（契约 C4），不存在则整个事务回滚
				if err := checkSpaceTx(ctx, q, *newSpace); err != nil {
					return err
				}
			}
			if _, err := q.SetThreadSpace(ctx, db.SetThreadSpaceParams{SpaceID: newSpace, UpdatedAt: now, ID: id}); err != nil {
				return err
			}
		}
		if p.Shipped != nil {
			if err := q.SetThreadShipped(ctx, db.SetThreadShippedParams{ShippedAt: shipped, UpdatedAt: now, ID: id}); err != nil {
				return err
			}
		}
		if p.Labels != nil {
			if err := q.ClearThreadLabels(ctx, id); err != nil {
				return err
			}
			for _, l := range p.Labels {
				if l = strings.TrimSpace(l); l != "" {
					if err := q.AddThreadLabel(ctx, db.AddThreadLabelParams{ThreadID: id, Label: l}); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err == nil {
		t.unread = m.Unread
		if p.SpaceID != nil {
			t.spaceID = ""
			if newSpace != nil {
				t.spaceID = *newSpace
			}
		}
		s.publish(ctx, id)
	}
	t.mu.Unlock()
	if err != nil {
		return apigen.Thread{}, err
	}
	if renamed != "" {
		s.renameSession(t, renamed)
	}
	return s.Get(ctx, id)
}

// MarkShipped 把线程标为已交付（at 为毫秒时间戳），不归档；已有 shipped_at 时不改动（M14：关联 PR 合并）。
func (s *Service) MarkShipped(ctx context.Context, id string, at int64) error {
	row, err := s.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if row.ShippedAt != nil {
		return nil
	}
	if err := s.st.Q.SetThreadShipped(ctx, db.SetThreadShippedParams{ShippedAt: &at, UpdatedAt: s.now(), ID: id}); err != nil {
		return err
	}
	s.Publish(ctx, id)
	return nil
}

// Delete 删除线程（条目随之删除；没有别的线程再用它的 orb 时 orb 一并删除）。agent 若在运行则先停止。
// Space 的聊天线程不能直接删除（删 Space）。
func (s *Service) Delete(ctx context.Context, id string) error {
	t, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case t.kind == KindSpace:
		return errf("space_thread", "delete the Space instead of its chat thread")
	case t.main:
		return errf(CodeMainConversation, "the main conversation cannot be deleted")
	}
	return s.deleteThread(ctx, t, nil)
}

// DeleteSpaceThread 删除 Space 的聊天线程（只供 internal/coord 删除 Space 时调用）。inTx 非 nil 时在删线程行的同一事务里
// 先执行（清 Space 的引用、删 spaces 行；契约 C4）；耗时的 agent.stop 在事务之前。
func (s *Service) DeleteSpaceThread(ctx context.Context, id string, inTx func(q *db.Queries) error) error {
	t, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	return s.deleteThread(ctx, t, inTx)
}

func (s *Service) deleteThread(ctx context.Context, t *thread, inTx func(q *db.Queries) error) error {
	id := t.id
	t.mu.Lock()
	orbID := t.orbID
	if t.agent != nil {
		t.agent.close()
		t.agent = nil
	}
	s.failPendingLocked(t, "thread deleted", true)
	// 仍在跟踪的带标签投递不会再有终态（规则 8）
	s.endAllTurnsLocked(t, TurnError, "thread_deleted")
	cancelHostCallsLocked(t)
	t.queue = nil
	t.mu.Unlock()
	s.dropThreadKey(id) // M21：runner 线程的 SSH 公钥随线程删除失效
	if ctl, ok := s.orbs.Ctl(orbID); ok && orbID != "" {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := ctl.Call(cctx, "agent.stop", apigen.LinkCtlAgentStopParams{ThreadID: id, GraceSeconds: 5}, nil); err != nil {
			slog.Debug("threads: agent.stop 失败", "thread", id, "err", err)
		}
		cancel()
	}
	if s.attach != nil {
		// 先删附件文件与 orb 内副本（行随线程级联删除）；失败时孤儿文件由附件服务的清理回收
		if err := s.attach.DeleteThread(ctx, id); err != nil {
			slog.Warn("threads: 删除线程附件失败", "thread", id, "err", err)
		}
	}
	var n int64
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		// 一个事务里：读线程行 → 把它当前的项目 / Space 写进三张事实表（删除后 thread_id 经外键置空，事实行冻结，
		// 按项目 / Space 的历史不丢）→ inTx（它可能清 Space 引用，所以排在归属快照之后）→ 删线程行
		row, err := q.GetThread(ctx, id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		default:
			if err := tombstoneFacts(ctx, q, id, row.ProjectID, row.SpaceID); err != nil {
				return err
			}
		}
		if inTx != nil {
			if err := inTx(q); err != nil {
				return err
			}
		}
		n, err = q.DeleteThread(ctx, id)
		return err
	})
	if err != nil {
		return err
	}
	s.forget(id)
	if n == 0 {
		return ErrNotFound
	}
	t.mu.Lock()
	s.out().IndexRemove(id)
	t.mu.Unlock()
	s.releaseOrb(ctx, orbID)
	return nil
}

// releaseOrb 在线程删除后回收它的 orb：orb 的生命周期跟随线程，最后一个引用它的线程（含已归档的）删除时 orb 随之删除，
// 用户不需要手动清理。失败只记录：线程已经删掉了。
func (s *Service) releaseOrb(ctx context.Context, orbID string) {
	if orbID == "" {
		return
	}
	if _, err := s.orbs.DeleteUnused(ctx, orbID, ""); err != nil {
		slog.Warn("threads: 回收线程的 orb 失败", "orb", orbID, "err", err)
	}
}

// checkSpaceTx 在事务里确认 Space 存在；不存在 → space_not_found。
func checkSpaceTx(ctx context.Context, q *db.Queries, spaceID string) error {
	if _, err := q.GetSpace(ctx, spaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errf("space_not_found", "space not found")
		}
		return err
	}
	return nil
}

// maxTitleRunes 是线程标题的上限（C9.4）。
const maxTitleRunes = 200

// cleanTitle 清洗线程标题（C9.4）：控制字符与 U+2028 / U+2029 换成空格、连续空白折叠、去首尾空白、截到 200 字符。
// 标题可能来自 agent（session_info_update、会话文件的标题槽、协调者的 create_thread），进其它 agent 的上下文前先清洗。
func cleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
	return truncText(strings.Join(strings.Fields(s), " "), maxTitleRunes)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// EntryPage 是条目分页结果（REST `GET threads/{id}/entries`）。
type EntryPage struct {
	Entries     []Entry `json:"entries"`
	HasMore     bool    `json:"hasMore"`
	LastSeq     int64   `json:"lastSeq"`
	LeafEntryID *string `json:"leafEntryId,omitempty"`
}

// Entries 返回 seq > after 的条目，至多 limit 条（1–500，默认 200）。
func (s *Service) Entries(ctx context.Context, id string, after int64, limit int) (EntryPage, error) {
	if limit <= 0 {
		limit = entriesBatch
	}
	limit = min(limit, 500)
	row, err := s.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return EntryPage{}, ErrNotFound
	}
	if err != nil {
		return EntryPage{}, err
	}
	rows, err := s.st.Q.ListEntriesAfter(ctx, db.ListEntriesAfterParams{ThreadID: id, AfterSeq: after, Limit: int64(limit + 1)})
	if err != nil {
		return EntryPage{}, err
	}
	page := EntryPage{LastSeq: row.LastSeq, LeafEntryID: row.LeafEntryID, Entries: make([]Entry, 0, min(len(rows), limit))}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	for _, e := range rows {
		page.Entries = append(page.Entries, entryFromDB(e))
	}
	return page, nil
}

// EntriesBefore 返回 seq < before 的最近至多 limit 条（1–500，默认 200），按 seq 升序；
// HasMore 表示还有更早的条目（M19 向前翻页）。
func (s *Service) EntriesBefore(ctx context.Context, id string, before int64, limit int) (EntryPage, error) {
	if limit <= 0 {
		limit = entriesBatch
	}
	limit = min(limit, 500)
	row, err := s.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return EntryPage{}, ErrNotFound
	}
	if err != nil {
		return EntryPage{}, err
	}
	rows, err := s.st.Q.ListEntriesBefore(ctx, db.ListEntriesBeforeParams{ThreadID: id, BeforeSeq: before, Limit: int64(limit + 1)})
	if err != nil {
		return EntryPage{}, err
	}
	page := EntryPage{LastSeq: row.LastSeq, LeafEntryID: row.LeafEntryID}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	page.Entries = make([]Entry, len(rows))
	for i, e := range rows {
		page.Entries[len(rows)-1-i] = entryFromDB(e)
	}
	return page, nil
}

// TailStart 返回最后 n 条条目里最小的 seq 减一（订阅窗口起点）；不足 n 条返回 0。
// 只查库、不取线程锁（hub 在 Subscribe 的回调里调用它）。
func (s *Service) TailStart(ctx context.Context, id string, n int) (int64, error) {
	return s.st.Q.GetTailStartSeq(ctx, db.GetTailStartSeqParams{ThreadID: id, N: int64(n)})
}

// EntriesRange 返回 after < seq ≤ upTo 的条目（WS 订阅补发用）。
func (s *Service) EntriesRange(ctx context.Context, id string, after, upTo int64, limit int) ([]Entry, error) {
	rows, err := s.st.Q.ListEntriesAfter(ctx, db.ListEntriesAfterParams{ThreadID: id, AfterSeq: after, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rows))
	for _, e := range rows {
		if e.Seq > upTo {
			break
		}
		out = append(out, entryFromDB(e))
	}
	return out, nil
}

// Snapshot 是 WS `thread.snapshot` 消息。
type Snapshot struct {
	Type        string        `json:"type"`
	ThreadID    string        `json:"threadId"`
	Thread      apigen.Thread `json:"thread"`
	Orb         *apigen.Orb   `json:"orb,omitempty"`
	Agent       AgentSnapshot `json:"agent"`
	LeafEntryID *string       `json:"leafEntryId,omitempty"`
	LastSeq     int64         `json:"lastSeq"`
	// TranscriptRev 是当前转录版本；FullReplay 由 hub 按客户端的版本决定（见 realtime.subscribe）。
	TranscriptRev int64 `json:"transcriptRev"`
	FullReplay    bool  `json:"fullReplay"`
	// WindowStart 是带 tail 订阅时的补发窗口起点（M19）：只补发 seq > WindowStart 的条目；0 = 没有开窗。
	WindowStart int64 `json:"windowStart,omitempty"`
}

// AgentSnapshot 对应规格 AgentLiveState（帧保持原始字节）。
type AgentSnapshot struct {
	Status       apigen.AgentStatus `json:"status"`
	State        json.RawMessage    `json:"state,omitempty"`
	Queue        json.RawMessage    `json:"queue,omitempty"`
	PendingUI    []json.RawMessage  `json:"pendingUi"`
	DisplayUI    []json.RawMessage  `json:"displayUi"`
	PlanProposal json.RawMessage    `json:"planProposal,omitempty"`
	// ComposerPrediction 为空时省略（规格：没有预测）。
	ComposerPrediction []string          `json:"composerPrediction,omitempty"`
	Subagents          []json.RawMessage `json:"subagents"`
	Live               []json.RawMessage `json:"live"`
}

// Subscribe 在持有线程锁时构造快照并交给 fn（fn 必须在返回前完成订阅登记，
// 此后该线程的全部事件都晚于快照到达 Sink）。
func (s *Service) Subscribe(ctx context.Context, id string, fn func(Snapshot)) error {
	t, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	row, err := s.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	api, err := s.threadAPI(ctx, row)
	if err != nil {
		return err
	}
	snap := Snapshot{Type: "thread.snapshot", ThreadID: id, Thread: api, LeafEntryID: t.leaf, LastSeq: t.lastSeq, TranscriptRev: t.transcriptRev}
	if t.orbID != "" {
		if o, err := s.st.Q.GetOrb(ctx, t.orbID); err == nil {
			a := s.orbAPI(ctx, o)
			snap.Orb = &a
		}
	}
	l := t.live
	snap.Agent = AgentSnapshot{
		Status: t.agentStatus, State: l.State, Queue: l.Queue, PlanProposal: l.PlanProposal,
		ComposerPrediction: l.ComposerPrediction,
		PendingUI:          nonNilFrames(l.PendingUI()), DisplayUI: nonNilFrames(l.DisplayUI()),
		Subagents: nonNilFrames(l.Subagents()), Live: nonNilFrames(l.LiveFrames()),
	}
	fn(snap)
	return nil
}

func nonNilFrames(f []json.RawMessage) []json.RawMessage {
	if f == nil {
		return []json.RawMessage{}
	}
	return f
}

// ---- fork ----

// Fork 对应 `POST threads/{id}/fork`（H4.5）。env = "share" 共用 orb；"copy" 快照出副本 orb。
func (s *Service) Fork(ctx context.Context, srcID string, entryID *string, env string) (apigen.Thread, error) {
	src, err := s.get(ctx, srcID)
	if err != nil {
		return apigen.Thread{}, err
	}
	if src.kind != KindWork {
		return apigen.Thread{}, errf(CodeCoordinatorRestricted, "coordinator conversations cannot be forked")
	}
	row, err := s.st.Q.GetThread(ctx, srcID)
	if err != nil {
		return apigen.Thread{}, err
	}
	src.mu.Lock()
	orbID := src.orbID
	entry := ""
	if entryID != nil && *entryID != "" {
		entry = *entryID
	} else if src.leaf != nil {
		entry = *src.leaf
	}
	src.mu.Unlock()
	if orbID == "" {
		return apigen.Thread{}, errf("no_orb", "source thread has no orb")
	}
	if entry == "" {
		return apigen.Thread{}, errf("invalid", "source thread has no entries to fork from")
	}
	newID := ids.Thread()
	child := db.CreateThreadParams{
		Kind: "work", ID: newID, ProjectID: row.ProjectID, Title: row.Title, TitleSource: "auto", State: StateProvisioning,
		ParentThreadID: &row.ID, ForkedFromEntryID: &entry, SessionDir: sessionDirOf(newID),
		Model: row.Model, ThinkingLevel: row.ThinkingLevel, Color: row.Color, ShortCode: shortCode(newID),
		// runner 线程（M11b）：子线程在同一工作目录里继续，worktree 仍归源线程（不继承）
		Cwd: row.Cwd,
	}
	switch env {
	case "share":
		return s.forkShare(ctx, src, child, orbID, entry)
	case "copy":
		if o, err := s.st.Q.GetOrb(ctx, orbID); err == nil && orbs.IsRunner(o) {
			return apigen.Thread{}, errf(CodeRunnerUnsupported, "a runner thread can only be forked with env=share")
		}
		// 只有显式给了 entryId 才在副本上再 fork；否则副本即完整会话
		forkAt := ""
		if entryID != nil {
			forkAt = *entryID
		}
		return s.forkCopy(ctx, src, child, orbID, forkAt)
	}
	return apigen.Thread{}, errf("invalid", "env must be copy or share")
}

// forkShare：源 agent fork_from 写出新会话文件 → fs.move 到新线程会话目录 → agent.start。
// 回退（计划 Assumptions）：fs.move 失败，或新 agent 未能打开该文件就退出时，
// 改为以源会话目录的副本启动（fs.copyTree）再发 fork{entryId}。
func (s *Service) forkShare(ctx context.Context, src *thread, child db.CreateThreadParams, orbID, entry string) (apigen.Thread, error) {
	frame, _ := json.Marshal(map[string]string{"type": "fork_from", "entryId": entry})
	// fork_from 把新会话文件写进源会话目录：在途期间暂存新出现的主文件，免得产物被当成源线程的新会话入库
	src.mu.Lock()
	src.forking++
	src.mu.Unlock()
	_, data, err := s.call(ctx, src, frame)
	var res struct {
		SessionPath string `json:"sessionPath"`
	}
	if err == nil && (json.Unmarshal(data, &res) != nil || res.SessionPath == "") {
		err = errf("agent_error", "fork_from returned no sessionPath")
	}
	product := ""
	if err == nil {
		product = path.Base(res.SessionPath)
	}
	src.mu.Lock()
	s.endForkLocked(ctx, src, product)
	src.mu.Unlock()
	if err != nil {
		return apigen.Thread{}, err
	}
	ctl, ok := s.orbs.Ctl(orbID)
	if !ok {
		return apigen.Thread{}, errf("orb_unavailable", "orb link is not connected")
	}
	base := product
	child.OrbID = &orbID
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	moveErr := ctl.Call(cctx, "fs.move", apigen.LinkCtlFsMoveParams{From: res.SessionPath, To: child.SessionDir + "/" + base}, nil)
	cancel()
	if moveErr != nil {
		slog.Warn("threads: fork share 移动会话文件失败，改用目录副本", "thread", src.id, "err", moveErr)
	}
	var extra func(q *db.Queries) error
	if moveErr != nil {
		// 回退：以源会话目录副本启动再 fork；阶段随线程行落库，重启后照样续作（加载时恢复 copyFrom / forkEntry）
		op := forkOp{Stage: forkStageCopy, Src: src.id, SrcOrb: orbID, ForkAt: entry}
		extra = func(q *db.Queries) error {
			return q.SetThreadForkOp(ctx, db.SetThreadForkOpParams{ForkOp: op.bytes(), ID: child.ID})
		}
	}
	if err := s.insertThread(ctx, child, nil, nil, extra); err != nil {
		return apigen.Thread{}, err
	}
	t, err := s.get(ctx, child.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	t.mu.Lock()
	waiter := make(chan apigen.AgentStatus, 1)
	t.readyWaiters = append(t.readyWaiters, waiter)
	t.mu.Unlock()
	out, err := s.Get(ctx, child.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	s.out().IndexUpsert(out)
	s.go_(func(ctx context.Context) {
		s.startAgent(ctx, child.ID)
		if moveErr != nil {
			return
		}
		select {
		case st := <-waiter:
			if st == apigen.AgentStatusReady {
				return
			}
		case <-time.After(s.cfg.AgentStartTimeout):
			return
		case <-ctx.Done():
			return
		}
		s.forkShareFallback(ctx, t, src.id, orbID, base, entry)
	})
	return out, nil
}

// forkShareFallback：新 agent 打不开移入的会话文件 → 停掉它、移走该文件、清掉已入库条目，
// 再以源会话目录副本启动并发 fork{entryId}。
func (s *Service) forkShareFallback(ctx context.Context, t *thread, srcID, orbID, base, entry string) {
	slog.Warn("threads: fork share 的新 agent 未能就绪，改用目录副本", "thread", t.id)
	ctl, ok := s.orbs.Ctl(orbID)
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_ = ctl.Call(cctx, "agent.stop", apigen.LinkCtlAgentStopParams{ThreadID: t.id, GraceSeconds: 5}, nil)
	trash := fmt.Sprintf("~/.oryn/trash/%s-%s", t.id, base)
	if err := ctl.Call(cctx, "fs.move", apigen.LinkCtlFsMoveParams{From: t.sessionDir + "/" + base, To: trash}, nil); err != nil {
		var ce *link.CtlError
		if !errors.As(err, &ce) {
			return
		}
	}
	s.dropFile(ctx, t, base)
	t.mu.Lock()
	t.copyFrom, t.forkEntry = srcID, entry
	s.clearFailedLocked(t)
	_ = s.setForkOpLocked(ctx, t, &forkOp{Stage: forkStageCopy, Src: srcID, SrcOrb: orbID, ForkAt: entry})
	s.resetLiveLocked(t)
	t.mu.Unlock()
	s.startAgent(ctx, t.id)
}

// forkCopy：源 orb 快照 → 以快照新建 orb → 新 orb 就绪后 fs.copyTree 源会话目录 → agent.start →
// 给了 entryId 时向新 agent 发 fork{entryId}。快照与建 orb 在后台进行，接口立即返回新线程。
func (s *Service) forkCopy(ctx context.Context, src *thread, child db.CreateThreadParams, orbID, entry string) (apigen.Thread, error) {
	srcOrb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return apigen.Thread{}, err
	}
	op := forkOp{Stage: forkStageSnapshot, Src: src.id, SrcOrb: orbID, ForkAt: entry, Type: srcOrb.Type}
	if srcOrb.ProjectID != nil {
		op.ProjectID = *srcOrb.ProjectID
	}
	// 后台阶段先落库再应答：重启后由 recoverForks 从快照阶段续作（CP5）
	if err := s.insertThread(ctx, child, nil, nil, func(q *db.Queries) error {
		return q.SetThreadForkOp(ctx, db.SetThreadForkOpParams{ForkOp: op.bytes(), ID: child.ID})
	}); err != nil {
		return apigen.Thread{}, err
	}
	t, err := s.get(ctx, child.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	out, err := s.Get(ctx, child.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	s.out().IndexUpsert(out)
	s.go_(func(ctx context.Context) { s.runForkCopy(ctx, t, op) })
	return out, nil
}
