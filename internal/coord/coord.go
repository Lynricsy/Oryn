// Package coord 是协调层（M9）：Space 与全局助手的协调对话、宿主工具、线程回报、orb 内 agent 间协作的 ctl 方法，
// 以及协调动作的审计记录。协调对话本身是 threads 表里的线程（kind = space | assistant），跑在内置 provider @host
// 的协调 orb 上（控制面宿主上受沙箱约束的 orynd coordinator-host）。
//
// 主人的决定：协调者动任意工作线程不设闸门，靠不可信内容包裹、来源头行与审计记录兜底；唯一的结构规则是
// 宿主工具的目标必须是工作线程（协调对话之间不能互相操作，防止成环），限速只作失控保护。orb 内 agent 不在
// 这一决定之内，仍按亲属规则（只能联系父子线程、只能改自己线程的定时）。
package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
	"github.com/Lynricsy/Oryn/internal/github"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/library"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// HostChecker 报告协调宿主是否可用（*coordhost.Host 实现）；不可用时返回带原因的错误。
type HostChecker interface {
	Check(ctx context.Context) error
}

// Config 是协调服务参数。
type Config struct {
	// Now 默认 time.Now（限速、回报、审计与进度图用）。
	Now func() time.Time
	// Model 是协调对话的默认模型（coordinator.model，"provider/id"；空 = omp 配置的默认模型）。
	Model string
	// PublicURL 用于线程链接（<public_url>/t/<id>）。
	PublicURL string
	// AfterFunc 默认 time.AfterFunc（M19 主线的静止定时器）；测试注入以手动触发。
	AfterFunc func(d time.Duration, f func()) (stop func())
}

// Scheduler 是定时服务（*schedule.Service 实现）。
type Scheduler interface {
	// Put：id 空 = 新建。
	Put(ctx context.Context, id string, in schedule.Input, createdBy string) (apigen.Schedule, error)
	PutForThread(ctx context.Context, threadID string, in schedule.Input, createdBy string) (apigen.Schedule, error)
	Get(ctx context.Context, id string) (apigen.Schedule, error)
	GetByThread(ctx context.Context, threadID string) (apigen.Schedule, error)
	Delete(ctx context.Context, id string) error
	SetEnabled(ctx context.Context, id string, enabled bool) (apigen.Schedule, error)
	List(ctx context.Context, f schedule.Filter) ([]apigen.Schedule, error)
}

// 错误码（REST 与宿主工具共用；工具结果的文本以错误码开头）。
const (
	CodeCoordinatorUnavailable = "coordinator_unavailable"
	CodeSpaceNotFound          = "space_not_found"
	CodeSpaceNameTaken         = "space_name_taken"
	CodeInvalid                = "invalid_request"
	CodeNotFound               = "not_found"
	CodeInvalidTarget          = "invalid_target"
	CodeRateLimited            = "rate_limited"
	CodeForbidden              = "forbidden"
	CodeInvalidSchedule        = "invalid_schedule"
	CodeThreadArchived         = "thread_archived"
	CodeTooLarge               = "too_large"
)

// Error 是协调层的业务错误。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Coded 让 threads 把它映射成同码的线程错误（协调线程自愈时）。
func (e *Error) Coded() (code, message string) { return e.Code, e.Message }

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Service 是协调服务。
type Service struct {
	cfg   Config
	st    *store.Store
	th    *threads.Service
	orbs  *orbs.Service
	host  HostChecker
	sched Scheduler
	lim   *limiter
	// lib / libHost 是全局助手文件库（M13，SetLibrary 设置）；nil 时文件工具与 orb 内 library.put 不可用。
	lib     *library.Service
	libHost LibraryHost
	// artifacts 是 M14 的 artifact 服务（SetArtifacts 设置）；nil 时 artifact 工具不可用。
	artifacts *artifacts.Service
	// mem / eng 是 M17 主人记忆与 M16 工程记忆（SetOwnerMemory 设置）；mem 为 nil 时全局助手不注入记忆段、没有记忆工具。
	mem OwnerMemory
	eng EngineeringMemory
	// home / projectCfg / envSecrets 是全局与项目级 omp 配置、env 模式密钥（M19，SetAgentConfig 设置）。
	home       GlobalConfig
	projectCfg ProjectConfigs
	envSecrets EnvSecrets
	// refreshPolicy 以当前设置刷新协调宿主的出站策略（M19；orbs 为 nil 时为 nil，测试可替换）。
	refreshPolicy func(ctx context.Context, orbID string) error
	// machines 是 orb 与 Runner 的操作入口（M19，SetMachines 设置）；nil 时依赖它的助手工具回 coordinator_unavailable。
	machines Machines
	// comp 是助手的电脑的单飞锁与在途调用（M19 第二层）。
	comp computerState
	// projects / triggers 是项目与 GitHub 触发规则的操作入口（M19，SetProjects / SetTriggers 设置）；nil 时对应的助手工具
	// 回 coordinator_unavailable。
	projects Projects
	triggers Triggers
	// shares / portals 是线程分享与 Portal 公开的入口（M19，SetShares 设置）；portals 为 nil 表示没有配置 Portal。
	shares  ThreadShares
	portals PortalShares

	ensureMu sync.Mutex
	// createMu 串行化 orb 内 thread.create 的「计数 + Create」（契约 C6.1）：并发请求不能都通过额度检查。
	createMu sync.Mutex
	// repMu 让「登记 + 投递」与回报处理互斥（契约 C3）；锁顺序 repMu → 线程锁。Observer 回调只取 mu。
	repMu sync.Mutex

	// mu 保护回报事件队列、事件序号、登记状态与待答问题摘要（Observer 回调持线程锁调用，只入队）。
	mu     sync.Mutex
	events []repEvent
	seq    uint64
	// armed 是目标线程 → 本进程内写下的登记；登记被消费或替换时更新。
	armed map[string]armedReport
	// recheck 是有非 tagged 登记、上次 RecoveredOutcome 为 !ok 的线程：条目入库后重查（契约 C1.7）。
	recheck   map[string]bool
	questions map[string]string
	// mainID / mainEp 是主线 id 与当前一段的计数和静止定时器（M19；Observer 回调据此识别主线，不读库）。
	mainID string
	mainEp mainEpisode
	// inbox 是主线收件（交给助手的运行结果）的合并定时器（M19 步骤 3.3，mu 保护）；inboxMu 串行化投递。
	inbox   inboxState
	inboxMu sync.Mutex
	wake      chan struct{}
	// confirmers 是确认卡动作 → 主人批准后的执行函数（onConfirm 在 New 里登记，之后只读）。
	confirmers map[string]confirmExec
	// sentConfirm（仅测试）在一条确认结果投递成功之后调用。
	sentConfirm func(threadID, text string)

	// afterReport（仅测试）在一次回报处理完之后调用。
	afterReport func(threadID string)
	// afterEpisode（仅测试）在主线一段结束、调用 RefreshAgent 之前调用（reason 为 idle / turns）。
	afterEpisode func(threadID, reason string)
	// sentInbox（仅测试）在一条合并的收件消息投递成功、标记已投递之后调用。
	sentInbox func(mainID, text string)
	// afterSend（仅测试）在 send_message 的 Deliver 成功返回之后、释放 repMu 之前调用。
	afterSend func()
	// sentReport（仅测试）在一条回报投递成功之后调用（持 repMu）。
	sentReport func(reportTo, text string)
	// afterQuotaCheck（仅测试）在 thread.create 的三项计数之后、Create 之前调用（仍持 createMu）。
	afterQuotaCheck func()
	// xferMax（仅测试）覆盖整文件传输的单文件上限；0 = transferMaxBytes。
	xferMax int64
}

// New 创建协调服务，并向 orbs 注册 orb 内的八个 ctl 方法。orbs 为 nil 时不注册（单测）。
func New(cfg Config, st *store.Store, th *threads.Service, o *orbs.Service, host HostChecker, sched Scheduler) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.AfterFunc == nil {
		cfg.AfterFunc = func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		}
	}
	s := &Service{
		cfg: cfg, st: st, th: th, orbs: o, host: host, sched: sched,
		lim: newLimiter(), questions: map[string]string{}, armed: map[string]armedReport{}, recheck: map[string]bool{},
		wake: make(chan struct{}, 1), confirmers: map[string]confirmExec{},
	}
	// 确认卡的执行函数（M19）
	s.onConfirm(ToolDeleteThread, s.confirmDeleteThread)
	s.onConfirm(ToolDeleteProject, s.confirmDeleteProject)
	s.onConfirm(ToolDeleteSpace, s.confirmDeleteSpace)
	s.onConfirm(ToolDeleteTrigger, s.confirmDeleteTrigger)
	s.onConfirm(ToolDeleteOrb, s.confirmDeleteOrb)
	s.onConfirm(string(apigen.ActionDeleteSchedule), s.confirmDeleteSchedule)
	s.onConfirm(ToolShareThread, s.confirmShareThread)
	s.onConfirm(ToolSharePortal, s.confirmSharePortal)
	s.onConfirm(ToolPublishArtifact, s.confirmPublishArtifact)
	if o != nil {
		s.registerOrbMethods(o.HandleRequest)
		s.refreshPolicy = func(ctx context.Context, orbID string) error {
			_, err := o.CoordinatorSetup(ctx, orbID)
			return err
		}
	}
	// 主线 id 在构造时读出：Observer 回调（主线分段）不读库
	s.loadMainID(context.Background())
	return s
}

func (s *Service) nowMs() int64 { return s.cfg.Now().UnixMilli() }

// Host 返回可用性检查（API 的 GET coordinator 用）。
func (s *Service) Host() HostChecker { return s.host }

// Run 处理线程回报，并每天裁剪一次 90 天前的审计记录、每小时让超过 24 小时未答的确认卡过期，直到 ctx 结束。
// 启动时先把仍待答的确认卡放回各自的对话（线程服务的待答请求只在内存里）。
func (s *Service) Run(ctx context.Context) {
	s.expireConfirmations(ctx)
	s.restoreConfirmations(ctx)
	s.trimActions(ctx)
	trim := time.NewTicker(24 * time.Hour)
	defer trim.Stop()
	sweep := time.NewTicker(confirmSweepEvery)
	defer sweep.Stop()
	inbox := time.NewTicker(inboxRetryEvery)
	defer inbox.Stop()
	for {
		s.processReports(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-trim.C:
			s.trimActions(ctx)
		case <-sweep.C:
			s.expireConfirmations(ctx)
		case <-inbox.C:
			s.retryInbox(ctx)
		}
	}
}

// actionRetention 是 coord_actions 的保留期。
const actionRetention = 90 * 24 * time.Hour

func (s *Service) trimActions(ctx context.Context) {
	if err := s.st.Q.TrimCoordActions(ctx, s.cfg.Now().Add(-actionRetention).UnixMilli()); err != nil && ctx.Err() == nil {
		slog.Warn("coord: 裁剪操作记录失败", "err", err)
	}
}

// EnsureOrb 返回协调 orb（单飞）：已有则直接返回，否则先确认协调宿主可用再创建。实现 threads.Coordinator。
func (s *Service) EnsureOrb(ctx context.Context) (string, error) {
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	o, err := s.st.Q.GetCoordinatorOrb(ctx)
	if err == nil {
		return o.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err := s.checkHost(ctx); err != nil {
		return "", err
	}
	if s.orbs == nil {
		return "", errf(CodeCoordinatorUnavailable, "orb service is not configured")
	}
	created, err := s.orbs.Create(ctx, orbs.CreateParams{Coordinator: true})
	if err != nil {
		if errors.Is(err, orbs.ErrNoProvider) {
			return "", errf(CodeCoordinatorUnavailable, "%s", err.Error())
		}
		return "", err
	}
	slog.Info("coord: 已创建协调 orb", "orb", created.ID)
	return created.ID, nil
}

// checkHost 调用 HostChecker；不可用时返回 coordinator_unavailable（message 是具体原因）。
func (s *Service) checkHost(ctx context.Context) error {
	if s.host == nil {
		return errf(CodeCoordinatorUnavailable, "coordinator host is not configured")
	}
	if err := s.host.Check(ctx); err != nil {
		msg := strings.TrimPrefix(err.Error(), CodeCoordinatorUnavailable+": ")
		return errf(CodeCoordinatorUnavailable, "%s", msg)
	}
	return nil
}

// ---- 审计 ----

// audit 写一行协调动作记录（无论成败）。summary 截到 500 字符；err 为 nil 表示成功。
func (s *Service) audit(ctx context.Context, actorKind, actorThread string, owner bool, action, target, scheduleID, summary string, err error) {
	origin := string(apigen.OriginAuto)
	if owner {
		origin = string(apigen.OriginOwner)
	}
	row := db.InsertCoordActionParams{
		ID: ids.New("cac"), Ts: s.nowMs(), ActorKind: actorKind, Origin: origin, Action: action,
		Summary: truncRunes(summary, 500), Ok: err == nil,
	}
	if actorThread != "" {
		row.ActorThreadID = &actorThread
	}
	if target != "" {
		row.TargetThreadID = &target
	}
	if scheduleID != "" {
		row.ScheduleID = &scheduleID
	}
	if err != nil {
		code := errCode(err)
		row.Error = &code
	}
	if e := s.st.Q.InsertCoordAction(context.WithoutCancel(ctx), row); e != nil {
		slog.Warn("coord: 写入操作记录失败", "action", action, "err", e)
	}
}

// Actions 返回操作记录（新的在前）；threadID 非空时只要它作为发起方或目标的记录。limit 1–200，缺省 50。
func (s *Service) Actions(ctx context.Context, threadID string, limit int) ([]apigen.AutomationAction, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	p := db.ListCoordActionsParams{Limit: int64(limit)}
	if threadID != "" {
		p.ThreadID = &threadID
	}
	rows, err := s.st.Q.ListCoordActions(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.AutomationAction, 0, len(rows))
	for _, r := range rows {
		out = append(out, apigen.AutomationAction{
			ID: r.ID, TS: r.Ts, ActorKind: apigen.AutomationActionActorKind(r.ActorKind), ActorThreadID: r.ActorThreadID,
			Origin: apigen.AutomationActionOrigin(r.Origin), Action: apigen.AutomationActionAction(r.Action),
			TargetThreadID: r.TargetThreadID, ScheduleID: r.ScheduleID, Summary: r.Summary, Ok: r.Ok, Error: r.Error,
		})
	}
	return out, nil
}

// errCode 取错误码：本包、threads、schedule 的业务错误取其 Code，其余为 internal_error。
func errCode(err error) string {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	var te *threads.Error
	if errors.As(err, &te) {
		return te.Code
	}
	var se *schedule.Error
	if errors.As(err, &se) {
		return se.Code
	}
	var le *link.CtlError
	if errors.As(err, &le) {
		return le.Code
	}
	var ge *github.Error
	if errors.As(err, &ge) {
		return ge.Code
	}
	if oe, ok := artifacts.AsOpError(err); ok {
		return oe.Code
	}
	if code := cfgErrCode(err); code != "" {
		return code
	}
	if code := shareErrCode(err); code != "" {
		return code
	}
	if code := orbErrCode(err); code != "" {
		return code
	}
	return "internal_error"
}

// orbErrCode 把 orbs 的哨兵错误与 fs.ErrNotExist 映射成工具错误码；不认识的返回空串。
func orbErrCode(err error) string {
	switch {
	case errors.Is(err, orbs.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return CodeNotFound
	case errors.Is(err, orbs.ErrInvalidState):
		return "invalid_state"
	case errors.Is(err, orbs.ErrRunnerOffline):
		return "runner_offline"
	case errors.Is(err, orbs.ErrRunnerUnsupported):
		return threads.CodeRunnerUnsupported
	case errors.Is(err, orbs.ErrOrbNotReady):
		return "orb_not_ready"
	case errors.Is(err, orbs.ErrOrbGone):
		return "orb_gone"
	case errors.Is(err, orbs.ErrOrbdOutdated):
		return "orbd_outdated"
	}
	return ""
}

// errMessage 是错误的人类可读部分（去掉与码重复的前缀）。
func errMessage(err error) string {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Message
	}
	var te *threads.Error
	if errors.As(err, &te) {
		return te.Message
	}
	var se *schedule.Error
	if errors.As(err, &se) {
		return se.Message
	}
	var le *link.CtlError
	if errors.As(err, &le) {
		return le.Message
	}
	var ge *github.Error
	if errors.As(err, &ge) {
		return ge.Message
	}
	if oe, ok := artifacts.AsOpError(err); ok {
		return oe.Message
	}
	if msg := shareErrMessage(err); msg != "" {
		return msg
	}
	if orbErrCode(err) != "" {
		// orbs 的错误文本带 "orbs: " 前缀
		return strings.TrimPrefix(err.Error(), "orbs: ")
	}
	return err.Error()
}

// truncRunes 把 s 截到至多 n 个字符。
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// threadURL 是线程在 Web 上的地址。
func (s *Service) threadURL(id string) string {
	return strings.TrimSuffix(s.cfg.PublicURL, "/") + "/t/" + id
}

// jsonText 把 v 编成紧凑 JSON 文本。
func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
