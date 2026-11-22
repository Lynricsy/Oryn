package research

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// Config 是研究服务的参数。
type Config struct {
	// Now 与 AfterFunc 是可注入的时钟（nil = 真实时钟）。引擎的节拍用 AfterFunc 重排，测试时钟才拨得动。
	Now       func() time.Time
	AfterFunc func(d time.Duration, f func()) (stop func())
	Store     *store.Store
	Host      Host
	Notifier  Notifier
	// WebAccess 报告全局助手的联网开关（关着时不发起公网运行）；nil = 开着。
	WebAccess func(ctx context.Context) bool
	// MineIdle 覆盖挖掘前对话须静止的时长（0 = mineIdleAfter；只给真机验收用）。
	MineIdle time.Duration
}

// Service 是后台研究服务：设置、话题、运行记录与引擎。
type Service struct {
	cfg Config
	st  *store.Store

	// mu 保护 current、turns、lastMineScan、backoff 与定时器（Observer 回调持线程锁调用 TurnEnded，只入队）。调 Host 时
	// 不得持有 mu。
	mu sync.Mutex
	// current 是每个角色当前的运行（至多一个）。
	current map[string]*runState
	turns   []threads.TurnEvent
	// lastMineScan 是上一次挖掘扫描的时间（内存字段：重启后立即再扫一次）。
	lastMineScan time.Time
	// backoff 是内部关联与 MCP 运行失败后的冷却截止时间（游标只在成功后推进，失败了不能每个节拍都重来）。
	backoff map[string]time.Time
	// tick 是下一次节拍的取消函数；retry 是等研究员停下的重查定时器。
	tick  func()
	retry func()

	wake chan struct{}
}

// New 创建研究服务。
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.AfterFunc == nil {
		cfg.AfterFunc = func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		}
	}
	if cfg.MineIdle <= 0 {
		cfg.MineIdle = mineIdleAfter
	}
	return &Service{cfg: cfg, st: cfg.Store, current: map[string]*runState{}, backoff: map[string]time.Time{}, wake: make(chan struct{}, 1)}
}

// runState 是一条进行中的研究运行。
type runState struct {
	id, role, pipeline, topicID, subject string
	started                              time.Time
	// mine：运行结束时写回的挖掘游标。
	mineLastMsgAt *int64
	mineLastSeq   int64
}

// job 是一条到期待发起的研究工作。
type job struct {
	pipeline, topicID, subject, prompt string
	mineLastMsgAt                      *int64
	mineLastSeq                        int64
}

func (s *Service) now() time.Time { return s.cfg.Now() }

// Wake 让引擎尽快跑一轮（设置或话题变化后）。
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run 运行引擎直到 ctx 结束：启动时把遗留的 running 运行标为失败（orynd 重启后研究员都是新进程）并清理旧记录；
// 之后每 tickEvery 一轮，或被 Wake / 回合结束唤醒。
func (s *Service) Run(ctx context.Context) {
	s.recover(ctx)
	for {
		s.step(ctx)
		s.mu.Lock()
		if s.tick != nil {
			s.tick()
		}
		s.tick = s.cfg.AfterFunc(tickEvery, s.Wake)
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.mu.Lock()
			if s.tick != nil {
				s.tick()
			}
			if s.retry != nil {
				s.retry()
			}
			s.mu.Unlock()
			return
		case <-s.wake:
		}
	}
}

// CurrentPipeline 返回角色 role 当前运行的管线；没有运行时为空（coord 据此给私有研究员工具表）。
func (s *Service) CurrentPipeline(role string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.current[role]; r != nil {
		return r.pipeline
	}
	return ""
}

// TurnEnded 实现 threads.Observer：研究运行（标签 rsr_…）的回合终态入队并唤醒引擎。持线程锁调用，只入队。
func (s *Service) TurnEnded(ev threads.TurnEvent) {
	if !strings.HasPrefix(ev.Tag, RunTagPrefix) {
		return
	}
	s.mu.Lock()
	s.turns = append(s.turns, ev)
	s.mu.Unlock()
	s.Wake()
}

// 其余 Observer 回调不关心。
func (s *Service) StateChanged(threads.StateEvent)   {}
func (s *Service) InputRequested(threads.InputEvent) {}
func (s *Service) InputResolved(string, string)      {}
func (s *Service) Seen(string)                       {}
func (s *Service) EntriesAppended(string)            {}

var _ threads.Observer = (*Service)(nil)

// recover 是启动时的善后。
func (s *Service) recover(ctx context.Context) {
	running, err := s.st.Q.ListRunningResearchRuns(ctx)
	if err != nil {
		slog.Warn("research: 读取进行中的运行失败", "err", err)
	}
	for _, r := range running {
		s.finishRun(ctx, r.ID, "failed", "", "", "interrupted")
	}
	if _, err := s.st.Q.DeleteOldResearchRuns(ctx, s.now().Add(-runsKeep).UnixMilli()); err != nil {
		slog.Warn("research: 清理旧运行记录失败", "err", err)
	}
}

// step 是引擎的一轮。
func (s *Service) step(ctx context.Context) {
	s.drainTurns(ctx)
	set, err := s.loadSettings(ctx)
	if err != nil {
		slog.Warn("research: 读取设置失败", "err", err)
		return
	}
	s.checkTimeouts(ctx)
	if set.Enabled == 0 {
		return
	}
	if s.capped(ctx, set) {
		return
	}
	for _, role := range []string{RoleWeb, RolePrivate} {
		s.mu.Lock()
		busy := s.current[role] != nil
		s.mu.Unlock()
		if busy {
			continue
		}
		j, err := s.nextJob(ctx, role, set)
		if err != nil {
			slog.Warn("research: 取到期工作失败", "role", role, "err", err)
			continue
		}
		if j == nil {
			continue
		}
		if !s.cfg.Host.ResearcherCold(ctx, role) {
			// 研究员上一代 agent 还在（或还有命令排着）：先让它停下，停稳了再发起，免得这条运行落进旧进程
			s.cfg.Host.StopResearcher(ctx, role)
			s.retryLater()
			continue
		}
		s.start(ctx, role, j)
	}
}

// retryLater 在 researcherRetry 之后再跑一轮（已排过则不重排）。
func (s *Service) retryLater() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retry != nil {
		return
	}
	s.retry = s.cfg.AfterFunc(researcherRetry, func() {
		s.mu.Lock()
		s.retry = nil
		s.mu.Unlock()
		s.Wake()
	})
}

// nextJob 取角色 role 下一条到期的工作：公网研究员取到期话题；私有研究员按挖掘 → MCP → 内部关联的顺序取第一个到期的。
func (s *Service) nextJob(ctx context.Context, role string, set settingsRow) (*job, error) {
	if role == RoleWeb {
		return s.dueWeb(ctx, set)
	}
	for _, due := range []func(context.Context, settingsRow) (*job, error){s.dueMine, s.dueMCP, s.dueInternal} {
		j, err := due(ctx, set)
		if err != nil || j == nil {
			if err != nil {
				return nil, err
			}
			continue
		}
		s.mu.Lock()
		cool := s.now().Before(s.backoff[j.pipeline])
		s.mu.Unlock()
		if !cool {
			return j, nil
		}
	}
	return nil, nil
}

// start 发起一条运行：先记 running 与 current（coord 取 Profile 时据此给工具表），再投递。
func (s *Service) start(ctx context.Context, role string, j *job) {
	r := &runState{
		id: newRunID(), role: role, pipeline: j.pipeline, topicID: j.topicID, subject: j.subject, started: s.now(),
		mineLastMsgAt: j.mineLastMsgAt, mineLastSeq: j.mineLastSeq,
	}
	if err := s.insertRun(ctx, r); err != nil {
		slog.Warn("research: 记录运行失败", "pipeline", j.pipeline, "err", err)
		return
	}
	s.mu.Lock()
	s.current[role] = r
	s.mu.Unlock()
	if err := s.cfg.Host.DeliverResearch(ctx, role, r.id, j.prompt); err != nil {
		slog.Info("research: 投递研究运行失败", "pipeline", j.pipeline, "err", err)
		s.mu.Lock()
		delete(s.current, role)
		s.mu.Unlock()
		s.finishRun(ctx, r.id, "failed", "", "", err.Error())
		s.afterFailure(ctx, r)
	}
}

// checkTimeouts 让超过 runTimeout 的运行失败并停掉研究员。
func (s *Service) checkTimeouts(ctx context.Context) {
	now := s.now()
	var late []*runState
	s.mu.Lock()
	for role, r := range s.current {
		if now.Sub(r.started) > runTimeout {
			late = append(late, r)
			delete(s.current, role)
		}
	}
	s.mu.Unlock()
	for _, r := range late {
		s.finishRun(ctx, r.id, "failed", "", "", "timeout")
		s.afterFailure(ctx, r)
		s.cfg.Host.StopResearcher(ctx, r.role)
	}
}

// drainTurns 处理入队的回合终态。标签不等于当前运行的（迟到的旧回合）直接丢弃。
func (s *Service) drainTurns(ctx context.Context) {
	s.mu.Lock()
	evs := s.turns
	s.turns = nil
	s.mu.Unlock()
	for _, ev := range evs {
		s.mu.Lock()
		var r *runState
		for role, cur := range s.current {
			if cur.id == ev.Tag {
				r = cur
				delete(s.current, role)
				break
			}
		}
		s.mu.Unlock()
		if r == nil {
			continue
		}
		s.finishTurn(ctx, r, ev)
		s.cfg.Host.StopResearcher(ctx, r.role)
	}
}

// finishTurn 按回合终态结束一条运行：成功时取回复里最后一个 json 块交给该管线。
func (s *Service) finishTurn(ctx context.Context, r *runState, ev threads.TurnEvent) {
	if ev.Status != threads.TurnCompleted {
		msg := ev.Status
		if ev.Error != "" {
			msg = ev.Error
		}
		s.finishRun(ctx, r.id, "failed", "", "", msg)
		s.afterFailure(ctx, r)
		return
	}
	text := ev.Text
	if !ev.HasText {
		ids, err := s.cfg.Host.ResearcherThreads(ctx)
		if err == nil {
			for _, id := range ids {
				if t, err := s.st.Q.GetThread(ctx, id); err == nil && t.Research != nil && *t.Research == r.role {
					text, _ = s.cfg.Host.LastReply(ctx, id)
				}
			}
		}
	}
	raw, ok := lastJSONBlock(text)
	if !ok {
		s.finishRun(ctx, r.id, "failed", "", "", "unparseable report")
		s.afterFailure(ctx, r)
		return
	}
	var (
		outcome, report string
		err             error
	)
	switch r.pipeline {
	case PipelineWeb:
		outcome, report, err = s.applyWeb(ctx, r, raw)
	case PipelineMine:
		outcome, report, err = s.applyMine(ctx, r, raw)
	case PipelineInternal:
		outcome, report, err = s.applyInternal(ctx, r, raw)
	case PipelineMCP:
		outcome, report, err = s.applyMCP(ctx, r, raw)
	}
	if err != nil {
		s.finishRun(ctx, r.id, "failed", "", "", err.Error())
		s.afterFailure(ctx, r)
		return
	}
	s.finishRun(ctx, r.id, "completed", outcome, report, "")
}

// afterFailure 是运行失败后的管线善后：公网话题按原间隔再排；挖掘照样推进游标（等下一次有新消息再看）；内部关联与
// MCP 冷却一个周期。
func (s *Service) afterFailure(ctx context.Context, r *runState) {
	switch r.pipeline {
	case PipelineWeb:
		s.webFailed(ctx, r)
	case PipelineMine:
		s.mineDone(ctx, r)
	case PipelineInternal, PipelineMCP:
		every := internalEvery
		if r.pipeline == PipelineMCP {
			every = mcpEvery
		}
		s.mu.Lock()
		s.backoff[r.pipeline] = s.now().Add(every)
		s.mu.Unlock()
	}
}
