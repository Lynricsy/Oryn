// Package jobs 是控制面侧的 provider 任务队列（计划 C1）：入队、长轮询认领、完成上报、
// 认领租约过期重排（attempt 上限 3）与 provider 单实例租约，并提供 /provider/v1/* 的 HTTP 处理器。
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 任务操作（与 apigen.JobOp 取值一致）。
const (
	OpSpawn          = "spawn"
	OpPause          = "pause"
	OpSnapshot       = "snapshot"
	OpDelete         = "delete"
	OpDeleteSnapshot = "delete_snapshot"
)

// 任务状态。
const (
	StateQueued    = "queued"
	StateClaimed   = "claimed"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
)

const (
	// ProviderLease 是 provider 实例租约时长（hello / claim 时续租）。
	ProviderLease = 45 * time.Second
	// DefaultClaimLease 是任务认领租约：超过即视为本次尝试失败并重排。
	DefaultClaimLease = 6 * time.Minute
	// DefaultMaxAttempts 是单个任务的最大尝试次数。
	DefaultMaxAttempts = 3
	// MaxOutputBytes 是保存的任务输出尾部上限。
	MaxOutputBytes = 64 << 10
	// MaxClaimWait 是单次长轮询的最长等待。
	MaxClaimWait  = 60 * time.Second
	sweepInterval = 15 * time.Second
)

// ErrNotFound 表示任务不存在或不属于该 provider。
var ErrNotFound = errors.New("jobs: not found")

// ErrNotClaimed 表示任务当前不处于 claimed 状态（已被重排或已完成）。
var ErrNotClaimed = errors.New("jobs: job is not claimed")

// Hooks 是任务生命周期回调。均在状态写库成功后同步调用，实现方应尽快返回。
type Hooks struct {
	// OnClaimed 在任务被 provider 认领后调用（每次尝试一次）。
	OnClaimed func(ctx context.Context, job db.Job)
	// OnFinished 在任务进入终态（succeeded / failed）后调用一次。
	OnFinished func(ctx context.Context, job db.Job, ok bool)
}

// Queue 是任务队列。
type Queue struct {
	st *store.Store

	// Now 返回当前时间（测试可替换）。
	Now func() time.Time
	// ClaimLease 是认领租约时长。
	ClaimLease time.Duration
	// MaxAttempts 是最大尝试次数。
	MaxAttempts int

	hooksMu sync.RWMutex
	hooks   Hooks

	// hookAfterAuth（仅测试）在 /provider/v1/hello 与 claim 完成鉴权、尚未处理租约时调用。
	hookAfterAuth func()

	mu      sync.Mutex
	signals map[string]chan struct{} // providerID → 有新任务时关闭并替换
	waiters map[string][]chan db.Job // jobID → 等待终态的调用方
}

// New 创建队列。回调用 SetHooks 设置（orbs 服务构造时注册）。
func New(st *store.Store) *Queue {
	return &Queue{
		st:          st,
		Now:         time.Now,
		ClaimLease:  DefaultClaimLease,
		MaxAttempts: DefaultMaxAttempts,
		signals:     map[string]chan struct{}{},
		waiters:     map[string][]chan db.Job{},
	}
}

// SetHooks 设置回调。
func (q *Queue) SetHooks(h Hooks) {
	q.hooksMu.Lock()
	q.hooks = h
	q.hooksMu.Unlock()
}

func (q *Queue) getHooks() Hooks {
	q.hooksMu.RLock()
	defer q.hooksMu.RUnlock()
	return q.hooks
}

func (q *Queue) nowMs() int64 { return q.Now().UnixMilli() }

// Enqueue 为 provider 入队一个任务；params 序列化为 JSON 对象（nil = {}）。
func (q *Queue) Enqueue(ctx context.Context, providerID, orbID, op string, params any) (db.Job, error) {
	job, err := q.EnqueueTx(ctx, q.st.Q, q.nowMs(), providerID, orbID, op, params)
	if err != nil {
		return db.Job{}, err
	}
	q.Signal(providerID)
	return job, nil
}

// EnqueueTx 在调用方的事务 tx 中入队（created_at = now），用于把状态变化与它需要的任务一起提交。
// 事务提交后调用方须调用 Signal(providerID) 唤醒长轮询。
func (q *Queue) EnqueueTx(ctx context.Context, tx *db.Queries, now int64, providerID, orbID, op string, params any) (db.Job, error) {
	raw := []byte("{}")
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return db.Job{}, fmt.Errorf("jobs: encode params: %w", err)
		}
		raw = b
	}
	var orbRef *string
	if orbID != "" {
		orbRef = &orbID
	}
	id := ids.New("job")
	if err := tx.CreateJob(ctx, db.CreateJobParams{
		ID: id, ProviderID: providerID, OrbID: orbRef, Op: op, Params: string(raw), CreatedAt: now,
	}); err != nil {
		return db.Job{}, fmt.Errorf("jobs: enqueue %s: %w", op, err)
	}
	job, err := tx.GetJob(ctx, id)
	if err != nil {
		return db.Job{}, err
	}
	slog.Debug("任务入队", "job", id, "op", op, "orb", orbID, "provider", providerID)
	return job, nil
}

// Wait 阻塞到任务进入终态并返回最终记录。
func (q *Queue) Wait(ctx context.Context, jobID string) (db.Job, error) {
	ch := make(chan db.Job, 1)
	q.mu.Lock()
	q.waiters[jobID] = append(q.waiters[jobID], ch)
	q.mu.Unlock()
	defer q.removeWaiter(jobID, ch)

	job, err := q.st.Q.GetJob(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Job{}, ErrNotFound
	}
	if err != nil {
		return db.Job{}, err
	}
	if terminal(job.State) {
		return job, nil
	}
	select {
	case job := <-ch:
		return job, nil
	case <-ctx.Done():
		return db.Job{}, ctx.Err()
	}
}

func (q *Queue) removeWaiter(jobID string, ch chan db.Job) {
	q.mu.Lock()
	defer q.mu.Unlock()
	list := q.waiters[jobID]
	for i, c := range list {
		if c == ch {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(q.waiters, jobID)
	} else {
		q.waiters[jobID] = list
	}
}

func terminal(state string) bool { return state == StateSucceeded || state == StateFailed }

// Signal 唤醒该 provider 的长轮询。
func (q *Queue) Signal(providerID string) {
	q.mu.Lock()
	if ch, ok := q.signals[providerID]; ok {
		close(ch)
		delete(q.signals, providerID)
	}
	q.mu.Unlock()
}

// signalChan 返回该 provider 当前的唤醒通道。
func (q *Queue) signalChan(providerID string) <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	ch, ok := q.signals[providerID]
	if !ok {
		ch = make(chan struct{})
		q.signals[providerID] = ch
	}
	return ch
}

// claimOne 认领该 provider 最早的可执行任务（同一 orb 的任务串行、按入队顺序）；没有时返回 (_, false, nil)。
func (q *Queue) claimOne(ctx context.Context, providerID string) (db.Job, bool, error) {
	now := q.nowMs()
	lease := now + q.ClaimLease.Milliseconds()
	job, err := q.st.Q.ClaimNextJobSerial(ctx, db.ClaimNextJobSerialParams{ClaimedAt: &now, LeaseUntil: &lease, ProviderID: providerID})
	if errors.Is(err, sql.ErrNoRows) {
		return db.Job{}, false, nil
	}
	if err != nil {
		return db.Job{}, false, err
	}
	slog.Info("任务已认领", "job", job.ID, "op", job.Op, "orb", deref(job.OrbID), "attempt", job.Attempt, "claimGen", job.ClaimGen)
	if h := q.getHooks().OnClaimed; h != nil {
		h(ctx, job)
	}
	return job, true, nil
}

// Claim 长轮询认领：最多等待 wait（上限 MaxClaimWait）；超时返回 (_, false, nil)。
func (q *Queue) Claim(ctx context.Context, providerID string, wait time.Duration) (db.Job, bool, error) {
	if wait > MaxClaimWait {
		wait = MaxClaimWait
	}
	if wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		ch := q.signalChan(providerID)
		job, ok, err := q.claimOne(ctx, providerID)
		if err != nil || ok {
			return job, ok, err
		}
		select {
		case <-ch:
		case <-timer.C:
			return db.Job{}, false, nil
		case <-ctx.Done():
			return db.Job{}, false, ctx.Err()
		}
	}
}

// Result 是一次尝试的结果。
type Result struct {
	OK         bool
	Output     string
	Error      string
	DurationMs int64
}

// Complete 记录 provider 对第 claimGen 次认领上报的结果：成功即终态；失败且未达上限则重排，否则终态 failed。
// 任务已不处于该次认领（租约过期被重排 / 已被巡检判失败 / 已被更新的认领取代）时返回 ErrNotClaimed。
func (q *Queue) Complete(ctx context.Context, providerID, jobID string, claimGen int64, res Result) error {
	job, err := q.st.Q.GetJob(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && job.ProviderID != providerID) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if job.State != StateClaimed || job.ClaimGen != claimGen {
		return ErrNotClaimed
	}
	return q.finishAttempt(ctx, job, res)
}

// finishAttempt 以 job.ClaimGen 为条件结束这一次认领（比较并交换）：认领已被重排或取代时返回 ErrNotClaimed。
func (q *Queue) finishAttempt(ctx context.Context, job db.Job, res Result) error {
	out := TailOutput(res.Output)
	var errPtr *string
	if !res.OK {
		msg := res.Error
		if msg == "" {
			msg = "failed"
		}
		errPtr = &msg
	}
	if !res.OK && int(job.Attempt) < q.MaxAttempts {
		n, err := q.st.Q.RequeueFailedJob(ctx, db.RequeueFailedJobParams{Output: out, Error: errPtr, ID: job.ID, ClaimGen: job.ClaimGen})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotClaimed
		}
		slog.Warn("任务失败，重排", "job", job.ID, "op", job.Op, "orb", deref(job.OrbID), "attempt", job.Attempt, "err", deref(errPtr))
		q.Signal(job.ProviderID)
		return nil
	}
	state := StateSucceeded
	if !res.OK {
		state = StateFailed
	}
	now := q.nowMs()
	n, err := q.st.Q.FinishJob(ctx, db.FinishJobParams{State: state, FinishedAt: &now, Output: out, Error: errPtr, ID: job.ID, ClaimGen: job.ClaimGen})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotClaimed
	}
	if job.Op == OpSpawn {
		q.scrubToken(ctx, job)
	}
	final, err := q.st.Q.GetJob(ctx, job.ID)
	if err != nil {
		return err
	}
	if res.OK {
		slog.Info("任务成功", "job", job.ID, "op", job.Op, "orb", deref(job.OrbID), "durationMs", res.DurationMs)
	} else {
		slog.Warn("任务失败（已达尝试上限）", "job", job.ID, "op", job.Op, "orb", deref(job.OrbID), "err", deref(errPtr))
	}
	q.mu.Lock()
	ws := q.waiters[job.ID]
	delete(q.waiters, job.ID)
	q.mu.Unlock()
	for _, ch := range ws {
		ch <- final
	}
	// 同一 orb 的后续任务此前被串行规则挡住，现在可以认领。
	q.Signal(job.ProviderID)
	if h := q.getHooks().OnFinished; h != nil {
		h(ctx, final, res.OK)
	}
	return nil
}

// scrubToken 在 spawn 任务终结后从库中抹掉 orb 令牌明文（令牌只在任务在途期间存在于 params 中）。
func (q *Queue) scrubToken(ctx context.Context, job db.Job) {
	var p map[string]any
	if err := json.Unmarshal([]byte(job.Params), &p); err != nil {
		return
	}
	env, ok := p["env"].(map[string]any)
	if !ok {
		return
	}
	if _, ok := env["ORYN_ORB_TOKEN"]; !ok {
		return
	}
	env["ORYN_ORB_TOKEN"] = ""
	b, _ := json.Marshal(p)
	if err := q.st.Q.SetJobParams(ctx, db.SetJobParamsParams{Params: string(b), ID: job.ID}); err != nil {
		slog.Warn("抹除任务令牌失败", "job", job.ID, "err", err)
	}
}

// Sweep 处理认领租约已过期的任务（视为一次失败尝试）。每条按读到的 claim_gen 比较并交换：
// 读取之后该认领已完成、已重排或已被新认领取代时什么也不做。
func (q *Queue) Sweep(ctx context.Context) error {
	now := q.nowMs()
	expired, err := q.st.Q.ListExpiredClaimedJobs(ctx, &now)
	if err != nil {
		return err
	}
	for _, job := range expired {
		err := q.finishAttempt(ctx, job, Result{OK: false, Error: "claim lease expired", Output: job.Output})
		if err != nil && !errors.Is(err, ErrNotClaimed) {
			slog.Warn("处理过期任务失败", "job", job.ID, "err", err)
		}
	}
	return nil
}

// Run 周期性执行 Sweep，直到 ctx 结束。
func (q *Queue) Run(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		if err := q.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("任务租约巡检失败", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// TailOutput 截取输出尾部至 MaxOutputBytes（按 UTF-8 边界）。
func TailOutput(s string) string {
	if len(s) <= MaxOutputBytes {
		return s
	}
	s = s[len(s)-MaxOutputBytes:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
