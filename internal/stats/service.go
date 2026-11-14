package stats

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/store"
)

// stats_state 的键（与 internal/threads 的回填约定一致）。
const (
	stateFactsVersion  = "facts_version"
	stateBackfillDone  = "backfill_done"
	stateBackfillTotal = "backfill_total"
)

// 默认周期。
const (
	DefaultBudgetInterval = 60 * time.Second
	DefaultSampleDelay    = 5 * time.Second
	sweepInterval         = time.Hour
	sampleTimeout         = 15 * time.Second
)

// Config 是统计服务的参数；零值字段取默认。
type Config struct {
	// Now 默认 time.Now。
	Now func() time.Time
	// AfterFunc 默认 time.AfterFunc（提交采样的去抖计时器）。
	AfterFunc func(d time.Duration, f func()) (stop func())
	// Retention 是保留期（0 = 永久）。
	Retention config.StatsRetention
	// BudgetInterval 是预算评估周期（默认 60s）。
	BudgetInterval time.Duration
	// SampleDelay 是线程转入 idle 后到提交采样的去抖时长（默认 5s）。
	SampleDelay time.Duration
}

// OrbClient 是提交采样用到的 orb 服务（*orbs.Service 实现）。计划里叫 Orbs，与 orb 统计函数 Orbs 同名，故改名。
type OrbClient interface {
	EnsureUsable(ctx context.Context, orbID, feature string, wake bool, wait time.Duration) error
	Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error)
}

// Service 是统计的后台部分：提交采样（实现 threads.Observer）、保留期清理、预算评估，以及 meta / 预算接口。
type Service struct {
	cfg  Config
	st   *store.Store
	orbs OrbClient
	n    Notifier

	mu      sync.Mutex
	gen     uint64
	pending map[string]pendingSample // 线程 → 去抖中的计时器
	ready   map[string]bool          // 去抖到期、待采样的线程
	wake    chan struct{}
}

// pendingSample 是一个去抖中的采样计时器；gen 区分被取代的旧计时器。
type pendingSample struct {
	gen  uint64
	stop func()
}

// New 构造统计服务；n 为 nil 时预算只落账本不通知，orbs 为 nil 时不采样提交。
func New(cfg Config, st *store.Store, orbs OrbClient, n Notifier) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.AfterFunc == nil {
		cfg.AfterFunc = func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		}
	}
	if cfg.BudgetInterval <= 0 {
		cfg.BudgetInterval = DefaultBudgetInterval
	}
	if cfg.SampleDelay <= 0 {
		cfg.SampleDelay = DefaultSampleDelay
	}
	return &Service{
		cfg: cfg, st: st, orbs: orbs, n: n,
		pending: map[string]pendingSample{}, ready: map[string]bool{}, wake: make(chan struct{}, 1),
	}
}

// Now 返回服务时钟的当前时刻（毫秒）。
func (s *Service) Now() int64 { return s.cfg.Now().UnixMilli() }

// Run 启动时清理一次，之后循环：采样到期线程的提交、每小时清理、每 BudgetInterval 评估预算。ctx 取消即返回。
func (s *Service) Run(ctx context.Context) {
	s.Sweep(ctx)
	sweep := time.NewTicker(sweepInterval)
	defer sweep.Stop()
	budget := time.NewTicker(s.cfg.BudgetInterval)
	defer budget.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			for _, p := range s.pending {
				p.stop()
			}
			clear(s.pending)
			s.mu.Unlock()
			return
		case <-s.wake:
			s.drainSamples(ctx)
		case <-sweep.C:
			s.Sweep(ctx)
		case <-budget.C:
			s.EvaluateBudget(ctx)
		}
	}
}

// Meta 返回服务端时间、事实行版本、回填进度、最早的用量时间与保留期。
func (s *Service) Meta(ctx context.Context) (apigen.StatsMeta, error) {
	out := apigen.StatsMeta{
		Now: s.Now(),
		Retention: apigen.StatsRetention{
			SamplesMs: s.cfg.Retention.Samples.D().Milliseconds(),
			EventsMs:  s.cfg.Retention.Events.D().Milliseconds(),
			UsageMs:   s.cfg.Retention.Usage.D().Milliseconds(),
		},
	}
	v, ok, err := s.stateInt(ctx, stateFactsVersion)
	if err != nil {
		return apigen.StatsMeta{}, err
	}
	if ok {
		out.FactsVersion = int(v)
	}
	if total, ok, err := s.stateInt(ctx, stateBackfillTotal); err != nil {
		return apigen.StatsMeta{}, err
	} else if ok {
		done, _, err := s.stateInt(ctx, stateBackfillDone)
		if err != nil {
			return apigen.StatsMeta{}, err
		}
		out.Backfill = &apigen.StatsBackfill{Done: done, Total: total}
	}
	minTs, err := s.st.Q.MinUsageTs(ctx)
	if err != nil {
		return apigen.StatsMeta{}, err
	}
	if minTs > 0 {
		out.EarliestTS = new(minTs)
	}
	return out, nil
}

// stateInt 读 stats_state 中的整数；不存在或不是整数时 ok 为假。
func (s *Service) stateInt(ctx context.Context, key string) (int64, bool, error) {
	v, err := s.st.Q.GetStatsState(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		slog.Warn("stats_state 值不是整数", "key", key, "value", v)
		return 0, false, nil
	}
	return n, true, nil
}
