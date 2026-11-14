// Package schedule 是 M9 定时：一张表、一个调度器、两种触发动作——
//   - thread：到点向一个已有线程追加提示词（threads.Deliver，自动化身份）；
//   - new_thread：每次运行用固定提示词新建一个线程（threads.Create），并按 keepLast 自动归档旧线程。
//
// 并发约定：定义的增删改、到点触发、手动触发与事件处理（ack、线程状态变化、回合终态、投递超时）全部在 loopMu 下串行执行，
// 「同一定时的未结束运行数」由此做到先查后插而不需要唯一索引。Observer 回调与 ack 在线程服务持锁时同步调用，
// 只入队（mu），由 Run 的循环或下一次 Tick 处理。锁顺序 loopMu → mu。测试用注入的时钟与定时器驱动，不 sleep。
//
// 运行的终态（契约 C1.5 / C1.6）：本进程投递的运行带标签（运行 id），只按 threads 的 TurnEnded 结算，线程的
// idle 只是候选；重启后恢复的运行没有标签，按已入库转录推断（Threads.RecoveredOutcomeMatch）。
package schedule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// Config 是调度器参数。
type Config struct {
	// Now 默认 time.Now。
	Now func() time.Time
	// AfterFunc 默认 time.AfterFunc；测试注入以手动触发。
	AfterFunc func(d time.Duration, f func()) (stop func())
	// DeliverTimeout 是线程内定时等 omp 应答的上限（默认 10m）。
	DeliverTimeout time.Duration
	// MaxFailures 是连续失败多少次后自动暂停（默认 3）。
	MaxFailures int
	// Changed 在某个定时的定义或运行记录变化后调用（契约 C7.2，调用时不持 mu）；nil = 不推送。
	Changed func(scheduleID string)
}

// Threads 是调度器需要的线程能力（*threads.Service 实现）。
type Threads interface {
	Get(ctx context.Context, id string) (apigen.Thread, error)
	Deliver(ctx context.Context, threadID string, m threads.AutoMessage, ack func(error)) (func() bool, error)
	Publish(ctx context.Context, threadID string)
	Create(ctx context.Context, p threads.CreateParams) (apigen.Thread, error)
	Archive(ctx context.Context, id string) error
	// RecoveredOutcomeMatch 从已入库的转录推断一次重启前已下发的投递的终态（补充契约 C1.6）：
	// 锚点是 ts >= since、match(正文) 为真的第一条 user 消息。
	RecoveredOutcomeMatch(ctx context.Context, threadID string, match func(text string) bool, since int64) (threads.TurnEvent, bool, error)
}

// Orbs 是保留策略删除独占 orb 用的能力（*orbs.Service 实现）。
type Orbs interface {
	Delete(ctx context.Context, orbID string) error
}

// Notifier 补发线程状态机看不到的失败（*notify.Service 实现）；threadID 可为空。
type Notifier interface {
	Post(ctx context.Context, threadID, title, body string) error
}

// Input 是定时的定义（REST、协调者与 orb 内方法共用）；字符串空值 = 未给出。
type Input struct {
	Name, Kind, Cron, Every, Timezone, Prompt string
	At, EndsAt                                *int64
	Enabled                                   *bool
	Mode, ThreadID                            string
	// 以下仅 new_thread。
	ProjectID, SpaceID, ThinkingLevel, Provider, OrbType, Overlap string
	Model                                                         *apigen.ModelRef
	KeepLast                                                      *int
	// IfRev 非 nil 时只在定时当前 rev 等于它时才替换（契约 C7.1，仅 REST 的两个 PUT 使用）；nil = 不做版本检查。
	IfRev *int64
}

// Filter 是 List 的过滤条件；空串 = 不限。NoProject 只列没有项目的 new_thread 定时。
type Filter struct {
	Mode, ProjectID, SpaceID, ThreadID string
	NoProject                          bool
}

// 错误码。
const (
	CodeInvalidSchedule  = "invalid_schedule"
	CodeScheduleNotFound = "schedule_not_found"
	CodeScheduleBusy     = "schedule_busy"
	CodeThreadArchived   = "thread_archived"
	CodeNameTaken        = "schedule_name_taken"
	CodeScheduleLimit    = "schedule_limit"
	CodeScheduleConflict = "schedule_conflict"
	CodeNotFound         = "not_found"
	CodeConflict         = "conflict"
	createdByUser        = "user"
	createdByAgent       = "agent"
	maxEnabledSchedules  = 200
	// maxAgentEnabledSchedules 是 agent 定义的启用中定时上限（契约 C6.2）。
	maxAgentEnabledSchedules = 20
	// minAgentNewThreadGap 是 agent 定义的 new_thread 定时相邻两次触发的最小间隔（契约 C6.2）。
	minAgentNewThreadGap = 15 * time.Minute
	// agentCronSamples 是校验 agent 定义的 cron 间隔时取的触发次数。
	agentCronSamples = 7
	// recoverSettleTimeout 是恢复运行从第一次「线程已静止但转录里还推断不出终态」起等可验证终态的上限，
	// 超时记 outcome_unknown（不计连续失败；补充契约 C1.6）。
	recoverSettleTimeout  = 10 * time.Minute
	keepRuns              = 200
	defaultDeliverTimeout = 10 * time.Minute
	defaultMaxFailures    = 3
	// overlapCap 是 overlap = allow 时同一定时并存的未结束运行上限。
	overlapCap = 3
	// lateAfter 之后才到点处理的运行记为 catchup。
	lateAfter = 60 * time.Second
	// staleAfter 之前开始、至今未结束的运行在恢复时记为 stale。
	staleAfter = 24 * time.Hour
	// maxArm 是定时器的最长延迟（防时钟跳变）。
	maxArm = time.Hour
	// maxRunError 是运行记录里错误文本的上限（字符）。
	maxRunError = 500
)

// 运行状态与触发方式（与 apigen 同值）。
const (
	runPending    = "pending"
	runRunning    = "running"
	runNeedsInput = "needs_input"
	runCompleted  = "completed"
	runFailed     = "failed"
	runSkipped    = "skipped"

	triggerSchedule = "schedule"
	triggerCatchup  = "catchup"
	triggerManual   = "manual"

	pausedUser     = "user"
	pausedFailures = "failures"
	pausedArchived = "archived"
	pausedDone     = "done"
)

// Error 是定时的业务错误；API 层按 Code 映射 HTTP 状态。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

var errScheduleNotFound = &Error{Code: CodeScheduleNotFound, Message: "schedule not found"}

// Service 是调度器。
type Service struct {
	cfg  Config
	st   *store.Store
	th   Threads
	orbs Orbs
	n    Notifier

	// loopMu 串行化定义的增删改、触发与事件处理；读最早触发时间并替换定时器也只在持有它时进行（契约 C8）。
	loopMu sync.Mutex

	mu sync.Mutex
	// open 是线程 → 该线程上未结束的运行（启动时由 ListOpenScheduleRuns 重建）。
	open map[string]string
	// pending 是等 omp 应答的线程内投递（运行 id → 投递）。
	pending map[string]*delivery
	// tagged 是本进程带标签投递的运行（运行 id → 定时 id）：终态只由 TurnEnded 结算（契约 C1.5）。
	tagged map[string]string
	// recovered 是重启后重新登记的运行（运行 id → 恢复信息）：终态由 RecoveredOutcomeMatch 推断（契约 C1.6 / C1.7）。
	recovered map[string]*recoveredRun
	// creating 是进行中的 new_thread 建线程数：期间的状态事件一律入队（新线程还没登记进 open）。
	creating int
	queue    []event
	tickDue  bool
	stopArm  func()
	wake     chan struct{}

	// 测试钩子（nil = 不调用）。
	// afterMinNext 在 armLocked 读到最早触发时间之后、取 mu 之前调用（契约 C8）。
	afterMinNext func()
	// afterCheckTarget 在 createLocked / updateLocked 的目标预检之后、写库事务之前调用（契约 C4）。
	afterCheckTarget func()
}

type delivery struct {
	runID, scheduleID, threadID string
	cancel                      func() bool
	stop                        func()
}

// recoveredRun 是一次恢复运行的推断参数。
type recoveredRun struct {
	scheduleID, threadID string
	// match 认出该运行投递的头行（runAnchor）；since 是运行开始时刻（毫秒）。
	match func(text string) bool
	since int64
	// waiting 为真表示线程已静止过、但转录里还推断不出终态：之后每批条目入库都重查一次（契约 C1.7）。
	waiting bool
	// stop 撤掉兜底超时（waiting 首次置真时上）。
	stop func()
}

type evKind int

const (
	evState evKind = iota
	evAck
	evTimeout
	// evTurn 是带标签投递的终态（TurnEnded）。
	evTurn
	// evRecheck 是恢复运行所在线程有新条目入库（EntriesAppended）。
	evRecheck
	// evSettleTimeout 是恢复运行等可验证终态超时。
	evSettleTimeout
)

type event struct {
	kind            evKind
	threadID, state string
	promptError     bool
	runID           string
	err             error
	turn            threads.TurnEvent
}

// New 创建调度器；Run 之前不触发任何定时。
func New(cfg Config, st *store.Store, th Threads, orbs Orbs, n Notifier) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.AfterFunc == nil {
		cfg.AfterFunc = func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		}
	}
	if cfg.DeliverTimeout <= 0 {
		cfg.DeliverTimeout = defaultDeliverTimeout
	}
	if cfg.MaxFailures <= 0 {
		cfg.MaxFailures = defaultMaxFailures
	}
	return &Service{
		cfg: cfg, st: st, th: th, orbs: orbs, n: n,
		open:      map[string]string{},
		pending:   map[string]*delivery{},
		tagged:    map[string]string{},
		recovered: map[string]*recoveredRun{},
		wake:      make(chan struct{}, 1),
	}
}

// InputFromAPI 把请求体转换成 Input。
func InputFromAPI(in apigen.ScheduleInput) Input {
	out := Input{
		Name: deref(in.Name), Kind: string(in.Kind), Cron: deref(in.Cron), Every: deref(in.Every), Timezone: deref(in.Timezone),
		Prompt: in.Prompt, At: in.At, EndsAt: in.EndsAt, Enabled: in.Enabled, ThreadID: deref(in.ThreadID),
		ProjectID: deref(in.ProjectID), SpaceID: deref(in.SpaceID), ThinkingLevel: deref(in.ThinkingLevel),
		Provider: deref(in.Provider), OrbType: deref(in.OrbType), Model: in.Model, KeepLast: in.KeepLast,
		IfRev: in.IfRev,
	}
	if in.Mode != nil {
		out.Mode = string(*in.Mode)
	}
	if in.Overlap != nil {
		out.Overlap = string(*in.Overlap)
	}
	return out
}

// ---- 定义 ----

// Put 新建（id 为空，须给 Mode）或替换定时。替换时目标（mode / threadId / projectId）不可改，保留 id 与
// run_count，清零 failures 与 paused_reason，重算 next_run_at。新建时忽略 IfRev。
func (s *Service) Put(ctx context.Context, id string, in Input, createdBy string) (apigen.Schedule, error) {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	if id == "" {
		switch in.Mode {
		case "":
			return apigen.Schedule{}, invalid("mode is required")
		case ModeThread:
			if in.ThreadID == "" {
				return apigen.Schedule{}, invalid("threadId is required for mode thread")
			}
		}
		in.IfRev = nil
		return s.createLocked(ctx, in, createdBy)
	}
	return s.updateLocked(ctx, id, in, createdBy)
}

// PutForThread 设置线程的线程内定时（至多一个）：已有则替换，否则新建。
// IfRev（契约 C7.1）：0 = 只在线程没有定时时新建；非 0 = 只在现有定时的 rev 等于它时替换（没有定时同样冲突）。
func (s *Service) PutForThread(ctx context.Context, threadID string, in Input, createdBy string) (apigen.Schedule, error) {
	if in.Mode != "" && in.Mode != ModeThread {
		return apigen.Schedule{}, invalid("a thread schedule has mode thread")
	}
	if in.ThreadID != "" && in.ThreadID != threadID {
		return apigen.Schedule{}, invalid("threadId does not match the thread")
	}
	in.Mode, in.ThreadID = ModeThread, threadID
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	cur, err := s.st.Q.GetScheduleByThread(ctx, threadID)
	switch {
	case err == nil:
		if in.IfRev != nil && *in.IfRev == 0 {
			return apigen.Schedule{}, errf(CodeScheduleConflict, "the thread already has a schedule")
		}
		return s.updateLocked(ctx, cur.ID, in, createdBy)
	case errors.Is(err, sql.ErrNoRows):
		if in.IfRev != nil && *in.IfRev != 0 {
			return apigen.Schedule{}, errf(CodeScheduleConflict, "the schedule no longer exists")
		}
		in.IfRev = nil
		return s.createLocked(ctx, in, createdBy)
	default:
		return apigen.Schedule{}, err
	}
}

func (s *Service) createLocked(ctx context.Context, in Input, createdBy string) (apigen.Schedule, error) {
	if createdBy != createdByAgent {
		createdBy = createdByUser
	}
	if createdBy == createdByAgent && in.Mode == ModeNewThread && in.Overlap != "" && in.Overlap != OverlapSkip {
		return apigen.Schedule{}, invalid("agent-defined new_thread schedules cannot overlap (overlap must be skip)")
	}
	now := s.cfg.Now()
	norm, loc, err := Validate(in, now)
	if err != nil {
		return apigen.Schedule{}, err
	}
	if createdBy == createdByAgent && norm.Mode == ModeNewThread {
		if err := checkAgentGap(norm, loc, now); err != nil {
			return apigen.Schedule{}, err
		}
	}
	if err := s.checkTarget(ctx, norm); err != nil {
		return apigen.Schedule{}, err
	}
	if s.afterCheckTarget != nil {
		s.afterCheckTarget()
	}
	enabled := norm.Enabled == nil || *norm.Enabled
	var next *int64
	var paused *string
	if enabled {
		if next = firstRun(norm, loc, now); next == nil {
			return apigen.Schedule{}, invalid("the schedule would not fire before endsAt")
		}
	} else {
		paused = new(pausedUser)
	}
	nowMs := now.UnixMilli()
	p := db.InsertScheduleParams{
		ID: ids.New("sch"), Mode: norm.Mode, Name: norm.Name, Kind: norm.Kind, Spec: norm.spec(), Timezone: norm.Timezone,
		EndsAt: norm.EndsAt, Prompt: norm.Prompt, Enabled: enabled, PausedReason: paused, NextRunAt: next,
		CreatedBy: createdBy, CreatedAt: nowMs, UpdatedAt: nowMs, Overlap: OverlapSkip, KeepLast: defaultKeepLast,
	}
	if norm.Kind == KindOnce {
		p.RunAt = norm.At
	}
	if norm.Mode == ModeThread {
		p.ThreadID = new(norm.ThreadID)
	} else {
		p.ProjectID, p.SpaceID = optStr(norm.ProjectID), optStr(norm.SpaceID)
		p.Model, p.ThinkingLevel = modelStr(norm.Model), optStr(norm.ThinkingLevel)
		p.Provider, p.OrbType = optStr(norm.Provider), optStr(norm.OrbType)
		p.Overlap, p.KeepLast = norm.Overlap, int64(*norm.KeepLast)
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		// Space 的存在性在写引用的事务里复核：与删除 Space 的事务串行，不留悬空引用（契约 C4）
		if err := checkSpaceTx(ctx, q, norm.SpaceID); err != nil {
			return err
		}
		if enabled {
			if err := checkLimit(ctx, q, createdBy); err != nil {
				return err
			}
		}
		if norm.Mode == ModeThread {
			if _, err := q.GetScheduleByThread(ctx, norm.ThreadID); err == nil {
				return errf(CodeConflict, "the thread already has a schedule")
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		} else if err := checkName(ctx, q, norm.Name, p.ProjectID, ""); err != nil {
			return err
		}
		return q.InsertSchedule(ctx, p)
	})
	if err != nil {
		return apigen.Schedule{}, err
	}
	s.changed(ctx, p.ID, norm.ThreadID)
	return s.Get(ctx, p.ID)
}

// updateLocked 整份替换定义；created_by 记最后一次定义它的一方（用户在界面里改回来也记 user）。
// in.IfRev 非 nil 时在读到现值之后比较（持 loopMu，定时行的写入都在 loopMu 下，比较即原子）。
func (s *Service) updateLocked(ctx context.Context, id string, in Input, createdBy string) (apigen.Schedule, error) {
	if createdBy != createdByAgent {
		createdBy = createdByUser
	}
	cur, err := s.getRow(ctx, id)
	if err != nil {
		if in.IfRev != nil && errors.Is(err, errScheduleNotFound) {
			return apigen.Schedule{}, errf(CodeScheduleConflict, "the schedule no longer exists")
		}
		return apigen.Schedule{}, err
	}
	if in.IfRev != nil && *in.IfRev != cur.Rev {
		return apigen.Schedule{}, errf(CodeScheduleConflict, "the schedule changed since it was loaded (rev %d)", cur.Rev)
	}
	if in.Mode != "" && in.Mode != cur.Mode {
		return apigen.Schedule{}, invalid("mode cannot be changed")
	}
	if in.ThreadID != "" && in.ThreadID != deref(cur.ThreadID) {
		return apigen.Schedule{}, invalid("threadId cannot be changed")
	}
	if in.ProjectID != "" && in.ProjectID != deref(cur.ProjectID) {
		return apigen.Schedule{}, invalid("projectId cannot be changed")
	}
	in.Mode = cur.Mode
	if cur.Mode == ModeThread {
		in.ThreadID = deref(cur.ThreadID)
	} else {
		// 项目已在库里，不再复核其存在性与归档状态（项目被归档时触发会记 project_archived）
		in.ProjectID = ""
	}
	agentNew := createdBy == createdByAgent && cur.Mode == ModeNewThread
	if agentNew {
		if err := keepAgentPolicy(&in, cur); err != nil {
			return apigen.Schedule{}, err
		}
	}
	now := s.cfg.Now()
	norm, loc, err := Validate(in, now)
	if err != nil {
		return apigen.Schedule{}, err
	}
	if agentNew {
		if err := checkAgentGap(norm, loc, now); err != nil {
			return apigen.Schedule{}, err
		}
	}
	if err := s.checkTarget(ctx, norm); err != nil {
		return apigen.Schedule{}, err
	}
	if s.afterCheckTarget != nil {
		s.afterCheckTarget()
	}
	enabled := norm.Enabled == nil || *norm.Enabled
	var next *int64
	var paused *string
	if enabled {
		if next = firstRun(norm, loc, now); next == nil {
			return apigen.Schedule{}, invalid("the schedule would not fire before endsAt")
		}
	} else {
		paused = new(pausedUser)
	}
	p := db.UpdateScheduleDefParams{
		ID: id, Name: norm.Name, Kind: norm.Kind, Spec: norm.spec(), Timezone: norm.Timezone, EndsAt: norm.EndsAt,
		Prompt: norm.Prompt, Enabled: enabled, PausedReason: paused, NextRunAt: next, UpdatedAt: now.UnixMilli(),
		Overlap: OverlapSkip, KeepLast: defaultKeepLast, CreatedBy: createdBy,
	}
	if norm.Kind == KindOnce {
		p.RunAt = norm.At
	}
	if cur.Mode == ModeNewThread {
		p.SpaceID, p.Model, p.ThinkingLevel = optStr(norm.SpaceID), modelStr(norm.Model), optStr(norm.ThinkingLevel)
		p.Provider, p.OrbType = optStr(norm.Provider), optStr(norm.OrbType)
		p.Overlap, p.KeepLast = norm.Overlap, int64(*norm.KeepLast)
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if err := checkSpaceTx(ctx, q, norm.SpaceID); err != nil {
			return err
		}
		switch {
		case enabled && !cur.Enabled:
			if err := checkLimit(ctx, q, createdBy); err != nil {
				return err
			}
		case enabled && createdBy == createdByAgent && cur.CreatedBy != createdByAgent:
			// 主人的启用中定时被 agent 改写后计入 agent 的上限（全局数不变）
			if err := checkAgentLimit(ctx, q); err != nil {
				return err
			}
		}
		if cur.Mode == ModeNewThread {
			if err := checkName(ctx, q, norm.Name, cur.ProjectID, id); err != nil {
				return err
			}
		}
		n, err := q.UpdateScheduleDef(ctx, p)
		if err == nil && n == 0 {
			return errScheduleNotFound
		}
		return err
	})
	if err != nil {
		return apigen.Schedule{}, err
	}
	s.changed(ctx, id, deref(cur.ThreadID))
	return s.Get(ctx, id)
}

// keepAgentPolicy 是 agent 改写 new_thread 定时时的沿用规则（契约 C6.2）：overlap 空 = 沿用现值、给出则须等于现值；
// keepLast 空 = 沿用现值、给出则不得小于现值。
func keepAgentPolicy(in *Input, cur db.Schedule) error {
	switch {
	case in.Overlap == "":
		in.Overlap = cur.Overlap
	case in.Overlap != cur.Overlap:
		return invalid("agents cannot change overlap of a new_thread schedule (currently %s)", cur.Overlap)
	}
	if in.KeepLast == nil {
		in.KeepLast = new(int(cur.KeepLast))
	} else if int64(*in.KeepLast) < cur.KeepLast {
		return invalid("agents cannot lower keepLast (currently %d)", cur.KeepLast)
	}
	return nil
}

// checkAgentGap 要求 agent 定义的 new_thread 定时相邻两次触发至少隔 minAgentNewThreadGap：every 直接比较；
// cron 取从 now 起的 agentCronSamples 次触发，比较相邻间隔的最小值。
func checkAgentGap(norm Input, loc *time.Location, now time.Time) error {
	switch norm.Kind {
	case KindEvery:
		d, err := time.ParseDuration(norm.Every)
		if err != nil || d < minAgentNewThreadGap {
			return invalid("agent-defined new_thread schedules run at most every %s", "15m")
		}
	case KindCron:
		prev, ok := NextAfter(KindCron, norm.Cron, loc, now)
		for i := 1; ok && i < agentCronSamples; i++ {
			next, more := NextAfter(KindCron, norm.Cron, loc, prev)
			if !more {
				break
			}
			if next.Sub(prev) < minAgentNewThreadGap {
				return invalid("agent-defined new_thread schedules run at most every %s (the cron fires more often)", "15m")
			}
			prev = next
		}
	}
	return nil
}

// checkTarget 核对目标：线程内定时的线程须存在且未归档；new_thread 的项目须存在且未归档、Space 须存在。
// 这是事务前的预检（不白跑后续步骤）；Space 另在写库事务里复核（checkSpaceTx）。
func (s *Service) checkTarget(ctx context.Context, in Input) error {
	if in.Mode == ModeThread {
		t, err := s.st.Q.GetThread(ctx, in.ThreadID)
		if errors.Is(err, sql.ErrNoRows) {
			return errf(CodeNotFound, "thread not found")
		}
		if err != nil {
			return err
		}
		if t.ArchivedAt != nil {
			return errf(CodeThreadArchived, "archived threads cannot have schedules")
		}
		return nil
	}
	if in.ProjectID != "" {
		pr, err := s.st.Q.GetProject(ctx, in.ProjectID)
		if errors.Is(err, sql.ErrNoRows) {
			return invalid("project not found")
		}
		if err != nil {
			return err
		}
		if pr.ArchivedAt != nil {
			return invalid("project is archived")
		}
	}
	return checkSpaceTx(ctx, s.st.Q, in.SpaceID)
}

// checkSpaceTx 检查 Space 存在（spaceID 为空时不查）。
func checkSpaceTx(ctx context.Context, q *db.Queries, spaceID string) error {
	if spaceID == "" {
		return nil
	}
	if _, err := q.GetSpace(ctx, spaceID); errors.Is(err, sql.ErrNoRows) {
		return invalid("space not found")
	} else if err != nil {
		return err
	}
	return nil
}

// checkLimit 检查启用上限：全局 maxEnabledSchedules，agent 定义的另有 maxAgentEnabledSchedules（契约 C6.2）。
func checkLimit(ctx context.Context, q *db.Queries, createdBy string) error {
	n, err := q.CountEnabledSchedules(ctx)
	if err != nil {
		return err
	}
	if n >= maxEnabledSchedules {
		return errf(CodeScheduleLimit, "at most %d schedules can be enabled", maxEnabledSchedules)
	}
	if createdBy == createdByAgent {
		return checkAgentLimit(ctx, q)
	}
	return nil
}

func checkAgentLimit(ctx context.Context, q *db.Queries) error {
	n, err := q.CountEnabledSchedulesBy(ctx, createdByAgent)
	if err != nil {
		return err
	}
	if n >= maxAgentEnabledSchedules {
		return errf(CodeScheduleLimit, "agents can have at most %d enabled schedules", maxAgentEnabledSchedules)
	}
	return nil
}

// checkName 检查同项目（或同为无项目）下 new_thread 定时的名字唯一；self 是正在更新的定时。
func checkName(ctx context.Context, q *db.Queries, name string, projectID *string, self string) error {
	other, err := q.GetScheduleByProjectName(ctx, db.GetScheduleByProjectNameParams{Name: name, ProjectID: projectID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if other.ID != self {
		return errf(CodeNameTaken, "a schedule named %q already exists in this project", name)
	}
	return nil
}

// Get 返回定时。
func (s *Service) Get(ctx context.Context, id string) (apigen.Schedule, error) {
	row, err := s.getRow(ctx, id)
	if err != nil {
		return apigen.Schedule{}, err
	}
	return s.toAPI(ctx, row)
}

// GetByThread 返回线程的线程内定时。
func (s *Service) GetByThread(ctx context.Context, threadID string) (apigen.Schedule, error) {
	row, err := s.st.Q.GetScheduleByThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.Schedule{}, errScheduleNotFound
	}
	if err != nil {
		return apigen.Schedule{}, err
	}
	return s.toAPI(ctx, row)
}

func (s *Service) getRow(ctx context.Context, id string) (db.Schedule, error) {
	row, err := s.st.Q.GetSchedule(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Schedule{}, errScheduleNotFound
	}
	return row, err
}

// Delete 删除定时及其运行记录。
func (s *Service) Delete(ctx context.Context, id string) error {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	row, err := s.getRow(ctx, id)
	if err != nil {
		return err
	}
	n, err := s.st.Q.DeleteSchedule(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return errScheduleNotFound
	}
	s.forgetRuns(id)
	s.changed(ctx, id, deref(row.ThreadID))
	return nil
}

// DeleteByThread 删除线程的线程内定时。
func (s *Service) DeleteByThread(ctx context.Context, threadID string) error {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	row, err := s.st.Q.GetScheduleByThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return errScheduleNotFound
	}
	if err != nil {
		return err
	}
	if _, err := s.st.Q.DeleteScheduleByThread(ctx, threadID); err != nil {
		return err
	}
	s.forgetRuns(row.ID)
	s.changed(ctx, row.ID, threadID)
	return nil
}

// forgetRuns 丢掉已删除定时在内存里的运行（运行记录随定时级联删除）。
func (s *Service) forgetRuns(scheduleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for runID, d := range s.pending {
		if d.scheduleID == scheduleID {
			if d.stop != nil {
				d.stop()
			}
			delete(s.pending, runID)
			if s.open[d.threadID] == runID {
				delete(s.open, d.threadID)
			}
		}
	}
	for runID, sid := range s.tagged {
		if sid == scheduleID {
			delete(s.tagged, runID)
		}
	}
	for runID, rr := range s.recovered {
		if rr.scheduleID == scheduleID {
			if rr.stop != nil {
				rr.stop()
			}
			delete(s.recovered, runID)
			if s.open[rr.threadID] == runID {
				delete(s.open, rr.threadID)
			}
		}
	}
}

// SetEnabled 暂停或恢复。恢复时从当前时刻重算 next_run_at（不补跑暂停期间错过的）并清零失败次数；
// 已结束的一次性定时（或已越过截止时间）须先改时间。
func (s *Service) SetEnabled(ctx context.Context, id string, enabled bool) (apigen.Schedule, error) {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	row, err := s.getRow(ctx, id)
	if err != nil {
		return apigen.Schedule{}, err
	}
	now := s.cfg.Now()
	p := db.SetScheduleEnabledParams{ID: id, Enabled: enabled, Failures: row.Failures, UpdatedAt: now.UnixMilli()}
	if !enabled {
		p.PausedReason = new(pausedUser)
	} else {
		if row.Mode == ModeThread {
			t, err := s.st.Q.GetThread(ctx, deref(row.ThreadID))
			if err != nil {
				return apigen.Schedule{}, err
			}
			if t.ArchivedAt != nil {
				return apigen.Schedule{}, errf(CodeThreadArchived, "archived threads cannot have schedules")
			}
		}
		next, ok := s.resumeNext(row, now)
		if !ok {
			return apigen.Schedule{}, invalid("the schedule has finished; change its time first")
		}
		p.NextRunAt, p.Failures = &next, 0
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if enabled && !row.Enabled {
			if err := checkLimit(ctx, q, row.CreatedBy); err != nil {
				return err
			}
		}
		return q.SetScheduleEnabled(ctx, p)
	})
	if err != nil {
		return apigen.Schedule{}, err
	}
	s.changed(ctx, id, deref(row.ThreadID))
	return s.Get(ctx, id)
}

// resumeNext 返回恢复时的 next_run_at；一次性已触发或下一次越过截止时间时为 false。
func (s *Service) resumeNext(row db.Schedule, now time.Time) (int64, bool) {
	if row.EndsAt != nil && *row.EndsAt <= now.UnixMilli() {
		return 0, false
	}
	if row.Kind == KindOnce {
		if row.RunAt == nil || (row.PausedReason != nil && *row.PausedReason == pausedDone) || row.RunCount > 0 ||
			*row.RunAt < now.Add(-onceGrace).UnixMilli() {
			return 0, false
		}
		return *row.RunAt, true
	}
	t, ok := NextAfter(row.Kind, row.Spec, loadLoc(row.Timezone), now)
	if !ok || (row.EndsAt != nil && t.UnixMilli() > *row.EndsAt) {
		return 0, false
	}
	return t.UnixMilli(), true
}

// List 列出定时（按创建时间）。
func (s *Service) List(ctx context.Context, f Filter) ([]apigen.Schedule, error) {
	rows, err := s.st.Q.ListSchedules(ctx, db.ListSchedulesParams{
		Mode: optStr(f.Mode), ProjectID: optStr(f.ProjectID), SpaceID: optStr(f.SpaceID), ThreadID: optStr(f.ThreadID), NoProject: f.NoProject,
	})
	if err != nil {
		return nil, err
	}
	out := make([]apigen.Schedule, 0, len(rows))
	for _, r := range rows {
		a, err := s.toAPI(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// Runs 返回定时最近的运行（新的在前）。
func (s *Service) Runs(ctx context.Context, id string, limit int) ([]apigen.ScheduleRun, error) {
	if _, err := s.getRow(ctx, id); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.st.Q.ListScheduleRuns(ctx, db.ListScheduleRunsParams{ScheduleID: id, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]apigen.ScheduleRun, len(rows))
	for i, r := range rows {
		out[i] = runAPI(r)
	}
	return out, nil
}

// Preview 只校验时间字段，返回接下来至多 5 次触发（毫秒）。
func (s *Service) Preview(in Input) ([]int64, error) {
	now := s.cfg.Now()
	norm, loc, err := validateTime(in, now)
	if err != nil {
		return nil, err
	}
	out := []int64{}
	if norm.Kind == KindOnce {
		if norm.EndsAt == nil || *norm.At <= *norm.EndsAt {
			out = append(out, *norm.At)
		}
		return out, nil
	}
	t := now
	for len(out) < previewCount {
		next, ok := NextAfter(norm.Kind, norm.spec(), loc, t)
		if !ok || (norm.EndsAt != nil && next.UnixMilli() > *norm.EndsAt) {
			break
		}
		out = append(out, next.UnixMilli())
		t = next
	}
	return out, nil
}

// changed 在定义变化后刷新线程摘要（线程内定时）、重新上定时器并推送失效通知。调用方持有 loopMu、不持 mu。
func (s *Service) changed(ctx context.Context, scheduleID, threadID string) {
	if threadID != "" {
		s.th.Publish(ctx, threadID)
	}
	s.armLocked(ctx)
	s.notifyChanged(scheduleID)
}

// notifyChanged 推送「某个定时的定义或运行记录变了」（契约 C7.2）。不得持 mu 调用。
func (s *Service) notifyChanged(scheduleID string) {
	if s.cfg.Changed != nil && scheduleID != "" {
		s.cfg.Changed(scheduleID)
	}
}

// ---- 转换 ----

func (s *Service) toAPI(ctx context.Context, r db.Schedule) (apigen.Schedule, error) {
	open, err := s.st.Q.CountOpenScheduleRuns(ctx, db.CountOpenScheduleRunsParams{ScheduleID: r.ID, IncludeNeedsInput: true})
	if err != nil {
		return apigen.Schedule{}, err
	}
	out := apigen.Schedule{
		ID: r.ID, Mode: apigen.ScheduleMode(r.Mode), Name: r.Name, Kind: apigen.ScheduleKind(r.Kind), Timezone: r.Timezone,
		EndsAt: r.EndsAt, Prompt: r.Prompt, Enabled: r.Enabled, NextRunAt: r.NextRunAt, LastRunAt: r.LastRunAt,
		RunCount: r.RunCount, Failures: int(r.Failures), CreatedBy: apigen.ScheduleCreatedBy(r.CreatedBy),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Overlap: apigen.ScheduleOverlap(r.Overlap), KeepLast: int(r.KeepLast),
		OpenRuns: int(open), Rev: r.Rev, ThreadID: r.ThreadID, ProjectID: r.ProjectID, SpaceID: r.SpaceID,
		ThinkingLevel: r.ThinkingLevel, Provider: r.Provider, OrbType: r.OrbType, Model: parseModel(r.Model),
	}
	switch r.Kind {
	case KindCron:
		out.Cron = new(r.Spec)
	case KindEvery:
		out.Every = new(r.Spec)
	case KindOnce:
		out.At = r.RunAt
	}
	if r.PausedReason != nil {
		out.PausedReason = new(apigen.SchedulePausedReason(*r.PausedReason))
	}
	last, err := s.st.Q.ListScheduleRuns(ctx, db.ListScheduleRunsParams{ScheduleID: r.ID, Limit: 1})
	if err != nil {
		return apigen.Schedule{}, err
	}
	if len(last) > 0 {
		out.LastRun = new(runAPI(last[0]))
	}
	return out, nil
}

func runAPI(r db.ScheduleRun) apigen.ScheduleRun {
	return apigen.ScheduleRun{
		ID: r.ID, ScheduleID: r.ScheduleID, ThreadID: r.ThreadID, Trigger: apigen.ScheduleRunTrigger(r.Trigger),
		ScheduledFor: r.ScheduledFor, Missed: int(r.Missed), StartedAt: r.StartedAt, DeliveredAt: r.DeliveredAt,
		FinishedAt: r.FinishedAt, Status: apigen.ScheduleRunStatus(r.Status), Error: r.Error,
	}
}

// modelStr 把模型写成库里的 "provider/id"。
func modelStr(m *apigen.ModelRef) *string {
	if m == nil {
		return nil
	}
	return new(strings.TrimSpace(m.Provider) + "/" + strings.TrimSpace(m.ID))
}

// parseModel 解析 "provider/id"（id 里可以再有斜杠）。
func parseModel(s *string) *apigen.ModelRef {
	if s == nil {
		return nil
	}
	p, id, ok := strings.Cut(*s, "/")
	if !ok {
		return nil
	}
	return &apigen.ModelRef{Provider: p, ID: id}
}

func loadLoc(tz string) *time.Location {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		slog.Warn("schedule: 时区无效，按 UTC 处理", "timezone", tz, "err", err)
		return time.UTC
	}
	return loc
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
