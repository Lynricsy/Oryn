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
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
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
	wake      chan struct{}

	// afterReport（仅测试）在一次回报处理完之后调用。
	afterReport func(threadID string)
	// afterSend（仅测试）在 send_message 的 Deliver 成功返回之后、释放 repMu 之前调用。
	afterSend func()
	// sentReport（仅测试）在一条回报投递成功之后调用（持 repMu）。
	sentReport func(reportTo, text string)
	// afterQuotaCheck（仅测试）在 thread.create 的三项计数之后、Create 之前调用（仍持 createMu）。
	afterQuotaCheck func()
}

// New 创建协调服务，并向 orbs 注册 orb 内的七个 ctl 方法。orbs 为 nil 时不注册（单测）。
func New(cfg Config, st *store.Store, th *threads.Service, o *orbs.Service, host HostChecker, sched Scheduler) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Service{
		cfg: cfg, st: st, th: th, orbs: o, host: host, sched: sched,
		lim: newLimiter(), questions: map[string]string{}, armed: map[string]armedReport{}, recheck: map[string]bool{},
		wake: make(chan struct{}, 1),
	}
	if o != nil {
		s.registerOrbMethods(o.HandleRequest)
	}
	return s
}

func (s *Service) nowMs() int64 { return s.cfg.Now().UnixMilli() }

// Host 返回可用性检查（API 的 GET coordinator 用）。
func (s *Service) Host() HostChecker { return s.host }

// Run 处理线程回报，并每天裁剪一次 90 天前的审计记录，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	s.trimActions(ctx)
	trim := time.NewTicker(24 * time.Hour)
	defer trim.Stop()
	for {
		s.processReports(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-trim.C:
			s.trimActions(ctx)
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
	return "internal_error"
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
