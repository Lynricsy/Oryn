// Package orbs 管理 orb 生命周期（计划 E 节）：状态机与 orb_events、spawn / pause / snapshot / delete 任务、
// 30s 空闲评估（冻结 / 停机）、唤醒、connecting 超时；以及 /orb/v1/link 处理器（hello → setup / resume、
// metrics.sample、setup.log、egress 流）。Threads 只经本包导出的 Service API 与 orb 交互。
package orbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orb 状态（与 apigen.OrbState 取值一致）。
const (
	StateRequested  = "requested"
	StateSpawning   = "spawning"
	StateConnecting = "connecting"
	StateSettingUp  = "setting_up"
	StateResuming   = "resuming"
	StateReady      = "ready"
	StateFreezing   = "freezing"
	StateFrozen     = "frozen"
	StateStopping   = "stopping"
	StateStopped    = "stopped"
	StateWaking     = "waking"
	StateDeleting   = "deleting"
	StateDeleted    = "deleted"
	StateFailed     = "failed"
)

// 暂停方式。
const (
	PauseFreeze = "freeze"
	PauseStop   = "stop"
)

// Event 种类。
const (
	// EventState 表示状态变化（From → To）。
	EventState = "state"
	// EventLinkUp 表示 orbd link 已连上并收到 hello（From == To == 当前状态）。
	EventLinkUp = "link_up"
	// EventLinkDown 表示 link 断开。
	EventLinkDown = "link_down"
)

var (
	// ErrNotFound 表示 orb 不存在。
	ErrNotFound = errors.New("orbs: not found")
	// ErrInvalidState 表示当前状态不允许该操作。
	ErrInvalidState = errors.New("orbs: operation not allowed in current state")
	// ErrNoProvider 表示无法确定 provider。
	ErrNoProvider = errors.New("orbs: no provider")
	// ErrBadType 表示 orb 类型无效。
	ErrBadType = errors.New("orbs: invalid orb type")
)

// Event 是 orb 状态或 link 变化通知。
type Event struct {
	OrbID string
	Kind  string
	From  string
	To    string
	Orb   db.Orb
}

// Config 是生命周期参数。
type Config struct {
	Idle     config.Idle
	Defaults config.OrbDefaults
	// PublicURL 作为 spawn 任务的 ORYN_URL（provider 会用自己的 orb_url 覆盖）。
	PublicURL string
	// TokenKey 是 orb 令牌派生密钥（LoadOrCreateTokenKey）；为空时用进程内随机密钥（仅测试）。
	TokenKey []byte
	// EvalInterval 是空闲评估周期（默认 30s）。
	EvalInterval time.Duration
	// ConnectTimeout 是 connecting 状态等待 hello 的时长（默认 120s）。
	ConnectTimeout time.Duration
	// ShutdownGrace 是 shutdown 时给 orbd 的宽限秒数（默认 20）。
	ShutdownGrace int
	// SetupTimeout 是 setup.run 的应答超时（默认 25 分钟；orbd 侧合计 20 分钟）。
	SetupTimeout time.Duration
}

// CreateParams 是新建 orb 的参数。空 Provider / Type 按 C3 规则取默认。
type CreateParams struct {
	Provider  string
	Type      string
	ProjectID string
	// SnapshotOf 是源 orb id（仅记录，并在未给 Provider 时沿用其 provider）。
	SnapshotOf string
	// SnapshotID 是用作镜像的快照。
	SnapshotID string
}

type (
	// RequestFunc 处理 orbd→orynd 的 ctl 请求。
	RequestFunc func(ctx context.Context, orbID string, params json.RawMessage) (any, error)
	// NotifyFunc 处理 orbd→orynd 的 ctl 通知（在 ctl 读循环中同步调用，须尽快返回）。
	NotifyFunc func(ctx context.Context, orbID string, params json.RawMessage)
	// StreamFunc 处理 orbd 发起的流（独立 goroutine，返回时流被关闭）。
	StreamFunc func(ctx context.Context, orbID string, h link.StreamHeader, st *link.Stream)
)

// Service 是 orb 生命周期服务。
type Service struct {
	cfg     Config
	st      *store.Store
	jobs    *jobs.Queue
	setup   SetupSource
	secrets EgressSecrets
	now     func() time.Time

	mu        sync.Mutex
	links     map[string]*orbLink
	linkGen   uint64
	timers    map[string]*time.Timer
	wakeAfter map[string]bool
	egress    map[string]*egressServer
	policies  map[string]egress.Policy

	hmu       sync.RWMutex
	reqs      map[string]RequestFunc
	notifies  map[string]NotifyFunc
	streams   map[string]StreamFunc
	subs      []func(Event)
	quiescent func(orbID string) bool

	bg sync.WaitGroup
}

// New 创建服务，并把任务回调注册到 q。setup / secrets 可为 nil。
func New(cfg Config, st *store.Store, q *jobs.Queue, setup SetupSource, secrets EgressSecrets) *Service {
	if cfg.EvalInterval <= 0 {
		cfg.EvalInterval = 30 * time.Second
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 120 * time.Second
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = 20
	}
	if cfg.SetupTimeout <= 0 {
		cfg.SetupTimeout = 25 * time.Minute
	}
	if len(cfg.TokenKey) == 0 {
		slog.Warn("orbs: 未配置 TokenKey，使用进程内随机密钥（重启后无法为已有 orb 重算令牌）")
		cfg.TokenKey = []byte(ids.Token("key"))
	}
	s := &Service{
		cfg: cfg, st: st, jobs: q, setup: setup, secrets: secrets, now: time.Now,
		links: map[string]*orbLink{}, timers: map[string]*time.Timer{},
		wakeAfter: map[string]bool{}, egress: map[string]*egressServer{}, policies: map[string]egress.Policy{},
		reqs: map[string]RequestFunc{}, notifies: map[string]NotifyFunc{}, streams: map[string]StreamFunc{},
	}
	q.SetHooks(jobs.Hooks{OnClaimed: s.onJobClaimed, OnFinished: s.onJobFinished})
	return s
}

// Jobs 返回任务队列（REST 层转发 /provider/v1/* 用）。
func (s *Service) Jobs() *jobs.Queue { return s.jobs }

// Config 返回生效的配置。
func (s *Service) Config() Config { return s.cfg }

func (s *Service) nowMs() int64 { return s.now().UnixMilli() }

// Subscribe 注册事件回调（同步调用，不得阻塞）。
func (s *Service) Subscribe(fn func(Event)) {
	s.hmu.Lock()
	s.subs = append(s.subs, fn)
	s.hmu.Unlock()
}

// SetQuiescence 设置「该 orb 全部线程已 settled、无 pendingUi、无 planProposal」的判定（Threads 提供）。
// 未设置时视为始终静止。
func (s *Service) SetQuiescence(fn func(orbID string) bool) {
	s.hmu.Lock()
	s.quiescent = fn
	s.hmu.Unlock()
}

func (s *Service) isQuiescent(orbID string) bool {
	s.hmu.RLock()
	fn := s.quiescent
	s.hmu.RUnlock()
	return fn == nil || fn(orbID)
}

func (s *Service) emit(ev Event) {
	s.hmu.RLock()
	subs := append([]func(Event){}, s.subs...)
	s.hmu.RUnlock()
	for _, fn := range subs {
		fn(ev)
	}
}

// Get 返回 orb。
func (s *Service) Get(ctx context.Context, orbID string) (db.Orb, error) {
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if errors.Is(err, sql.ErrNoRows) {
		return orb, ErrNotFound
	}
	return orb, err
}

// transition 以比较并交换的方式把 orb 从 from 移到 to，并写 orb_events。返回是否发生了变化。
func (s *Service) transition(ctx context.Context, orbID, from, to string, detail map[string]any) (db.Orb, bool, error) {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, _ := json.Marshal(detail)
	now := s.nowMs()
	var orb db.Orb
	moved := false
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		n, err := q.TransitionOrbState(ctx, db.TransitionOrbStateParams{ToState: to, StateChangedAt: now, ID: orbID, FromState: from})
		if err != nil || n == 0 {
			return err
		}
		moved = true
		if to == StateDeleted {
			if err := q.MarkOrbDeleted(ctx, db.MarkOrbDeletedParams{DeletedAt: now, ID: orbID}); err != nil {
				return err
			}
		}
		if err := q.InsertOrbEvent(ctx, db.InsertOrbEventParams{OrbID: orbID, Ts: now, FromState: from, ToState: to, Detail: string(raw)}); err != nil {
			return err
		}
		orb, err = q.GetOrb(ctx, orbID)
		return err
	})
	if err != nil || !moved {
		return orb, false, err
	}
	slog.Info("orb 状态变化", "orb", orbID, "from", from, "to", to, "detail", string(raw))
	s.armConnectTimer(orbID, to)
	s.emit(Event{OrbID: orbID, Kind: EventState, From: from, To: to, Orb: orb})
	return orb, true, nil
}

// armConnectTimer 在进入 connecting 时启动 hello 超时；离开时取消。
func (s *Service) armConnectTimer(orbID, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.timers[orbID]; t != nil {
		t.Stop()
		delete(s.timers, orbID)
	}
	if state != StateConnecting {
		return
	}
	s.timers[orbID] = time.AfterFunc(s.cfg.ConnectTimeout, func() {
		ctx := context.Background()
		_, moved, err := s.transition(ctx, orbID, StateConnecting, StateFailed, map[string]any{"reason": "no hello within connect timeout"})
		if err != nil {
			slog.Warn("connecting 超时处理失败", "orb", orbID, "err", err)
		} else if moved {
			slog.Warn("orb 在超时时间内没有连上", "orb", orbID, "timeout", s.cfg.ConnectTimeout)
		}
	})
}

// resolveProvider 按「显式 → 项目默认 → orb_defaults → 源 orb → 唯一 provider」确定 provider。
func (s *Service) resolveProvider(ctx context.Context, name string, prj *db.Project, src *db.Orb) (db.Provider, error) {
	if name == "" && prj != nil && prj.DefaultProvider != nil {
		name = *prj.DefaultProvider
	}
	if name == "" {
		name = s.cfg.Defaults.Provider
	}
	if name != "" {
		p, err := s.st.Q.GetProviderByName(ctx, name)
		if errors.Is(err, sql.ErrNoRows) {
			return p, fmt.Errorf("%w: provider %q 不存在", ErrNoProvider, name)
		}
		if err == nil && p.DisabledAt != nil {
			return p, fmt.Errorf("%w: provider %q 已停用", ErrNoProvider, name)
		}
		return p, err
	}
	if src != nil {
		return s.st.Q.GetProvider(ctx, src.ProviderID)
	}
	all, err := s.st.Q.ListProviders(ctx)
	if err != nil {
		return db.Provider{}, err
	}
	var live []db.Provider
	for _, p := range all {
		if p.DisabledAt == nil {
			live = append(live, p)
		}
	}
	if len(live) == 1 {
		return live[0], nil
	}
	return db.Provider{}, fmt.Errorf("%w: 未指定 provider 且无法确定默认值（共 %d 个可用）", ErrNoProvider, len(live))
}

// Create 新建 orb（requested）并入队 spawn 任务。
func (s *Service) Create(ctx context.Context, p CreateParams) (_ db.Orb, err error) {
	if p.SnapshotID != "" && p.SnapshotOf != "" {
		// 快照是专为这次创建拍的：创建失败时没有 orb 会用它，交给源 orb 的 provider 回收。
		defer func() {
			if err != nil {
				bg := context.WithoutCancel(ctx)
				if src, gerr := s.st.Q.GetOrb(bg, p.SnapshotOf); gerr == nil {
					s.releaseSnapshot(bg, src.ProviderID, src.ID, p.SnapshotID)
				}
			}
		}()
	}
	var prj *db.Project
	if p.ProjectID != "" {
		v, err := s.st.Q.GetProject(ctx, p.ProjectID)
		if err != nil {
			return db.Orb{}, fmt.Errorf("读取项目 %s: %w", p.ProjectID, err)
		}
		prj = &v
	}
	var src *db.Orb
	if p.SnapshotOf != "" {
		if v, err := s.st.Q.GetOrb(ctx, p.SnapshotOf); err == nil {
			src = &v
		}
	}
	prov, err := s.resolveProvider(ctx, p.Provider, prj, src)
	if err != nil {
		return db.Orb{}, err
	}
	typ := p.Type
	if typ == "" && src != nil && p.Provider == "" {
		typ = src.Type
	}
	if typ == "" && prj != nil && prj.DefaultOrbType != nil {
		typ = *prj.DefaultOrbType
	}
	if typ == "" {
		typ = s.cfg.Defaults.Type
	}
	if typ == "" {
		typ = prov.DefaultType
	}
	if typ == "" {
		return db.Orb{}, fmt.Errorf("%w: 无法确定 orb 类型（provider %q 尚未上线？）", ErrBadType, prov.Name)
	}
	var types []apigen.OrbType
	_ = json.Unmarshal([]byte(prov.OrbTypes), &types)
	var spec *apigen.OrbType
	for i := range types {
		if types[i].Name == typ {
			spec = &types[i]
		}
	}
	if spec == nil && len(types) > 0 {
		return db.Orb{}, fmt.Errorf("%w: provider %q 没有类型 %q", ErrBadType, prov.Name, typ)
	}
	orbID := ids.Orb()
	token := orbToken(s.cfg.TokenKey, orbID)
	now := s.nowMs()
	params := db.CreateOrbParams{
		ID: orbID, ProviderID: prov.ID, Type: typ, Image: p.SnapshotID,
		State: StateRequested, StateChangedAt: now, TokenHash: ids.Hash(token), CreatedAt: now,
	}
	if spec != nil {
		params.Cpus = int64(math.Round(float64(spec.Cpus)))
		params.MemoryMb = int64(spec.MemoryMB)
	}
	if prj != nil {
		params.ProjectID = &prj.ID
	}
	if p.SnapshotID != "" {
		params.SnapshotOf = &p.SnapshotID
	}
	detail := map[string]any{"provider": prov.Name, "type": typ}
	if p.SnapshotOf != "" {
		detail["sourceOrb"] = p.SnapshotOf
	}
	raw, _ := json.Marshal(detail)
	var orb db.Orb
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateOrb(ctx, params); err != nil {
			return err
		}
		if err := q.InsertOrbEvent(ctx, db.InsertOrbEventParams{OrbID: orbID, Ts: now, FromState: "", ToState: StateRequested, Detail: string(raw)}); err != nil {
			return err
		}
		orb, err = q.GetOrb(ctx, orbID)
		return err
	})
	if err != nil {
		return db.Orb{}, fmt.Errorf("创建 orb: %w", err)
	}
	slog.Info("orb 已创建", "orb", orbID, "provider", prov.Name, "type", typ, "snapshot", p.SnapshotID)
	s.emit(Event{OrbID: orbID, Kind: EventState, From: "", To: StateRequested, Orb: orb})
	if err := s.enqueueSpawn(ctx, orb); err != nil {
		_, _, _ = s.transition(ctx, orbID, StateRequested, StateFailed, map[string]any{"reason": err.Error()})
		return orb, err
	}
	return orb, nil
}

func (s *Service) enqueueSpawn(ctx context.Context, orb db.Orb) error {
	p := apigen.JobSpawnParams{
		Image: "", Type: orb.Type,
		Env: apigen.JobSpawnEnv{OrynURL: s.cfg.PublicURL, OrynOrbID: orb.ID, OrynOrbToken: orbToken(s.cfg.TokenKey, orb.ID)},
	}
	if orb.SnapshotOf != nil && *orb.SnapshotOf != "" {
		p.SnapshotID = orb.SnapshotOf
	}
	_, err := s.jobs.Enqueue(ctx, orb.ProviderID, orb.ID, jobs.OpSpawn, p)
	return err
}

func (s *Service) enqueuePause(ctx context.Context, orb db.Orb, mode string) error {
	_, err := s.jobs.Enqueue(ctx, orb.ProviderID, orb.ID, jobs.OpPause, apigen.JobPauseParams{Mode: apigen.PauseMode(mode)})
	return err
}

// Wake 唤醒 orb：frozen / stopped / failed → waking 并入队 spawn；正在冻结 / 停机时在其完成后再唤醒；
// 已在运行或正在启动时无操作。
func (s *Service) Wake(ctx context.Context, orbID string) error {
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return err
	}
	switch orb.State {
	case StateFrozen, StateStopped, StateFailed:
		_, moved, err := s.transition(ctx, orbID, orb.State, StateWaking, nil)
		if err != nil {
			return err
		}
		if !moved {
			return s.Wake(ctx, orbID)
		}
		if err := s.enqueueSpawn(ctx, orb); err != nil {
			_, _, _ = s.transition(ctx, orbID, StateWaking, StateFailed, map[string]any{"reason": err.Error()})
			return err
		}
		return nil
	case StateFreezing, StateStopping:
		s.mu.Lock()
		s.wakeAfter[orbID] = true
		s.mu.Unlock()
		return nil
	case StateDeleting, StateDeleted:
		return fmt.Errorf("%w: orb %s 已删除", ErrInvalidState, orbID)
	}
	return nil
}

// Pause 手动冻结或停机。
func (s *Service) Pause(ctx context.Context, orbID, mode string) error {
	if mode != PauseFreeze && mode != PauseStop {
		return fmt.Errorf("%w: 暂停方式 %q", ErrInvalidState, mode)
	}
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return err
	}
	switch {
	case mode == PauseFreeze && orb.State == StateFrozen, mode == PauseStop && orb.State == StateStopped:
		return nil
	case mode == PauseFreeze && orb.State == StateReady:
		return s.freeze(ctx, orb, "manual")
	case mode == PauseStop && (orb.State == StateReady || orb.State == StateFrozen):
		return s.stop(ctx, orb, "manual")
	}
	return fmt.Errorf("%w: orb %s 处于 %s，不能 %s", ErrInvalidState, orbID, orb.State, mode)
}

func (s *Service) freeze(ctx context.Context, orb db.Orb, reason string) error {
	_, moved, err := s.transition(ctx, orb.ID, StateReady, StateFreezing, map[string]any{"reason": reason})
	if err != nil || !moved {
		return err
	}
	if err := s.enqueuePause(ctx, orb, PauseFreeze); err != nil {
		_, _, _ = s.transition(ctx, orb.ID, StateFreezing, StateReady, map[string]any{"reason": err.Error()})
		return err
	}
	return nil
}

// stop 停机：ready → stopping 后先 shutdown 再 pause{stop}；frozen → stopping 后先 spawn 解冻，
// link 恢复后再 shutdown 与 pause{stop}（见 onJobFinished）。
func (s *Service) stop(ctx context.Context, orb db.Orb, reason string) error {
	_, moved, err := s.transition(ctx, orb.ID, orb.State, StateStopping, map[string]any{"reason": reason})
	if err != nil || !moved {
		return err
	}
	if orb.State == StateFrozen {
		return s.enqueueSpawn(ctx, orb)
	}
	s.goBG(func() { s.shutdownThenPauseStop(orb, 0) })
	return nil
}

// shutdownThenPauseStop 等待 link（至多 wait）后发 shutdown，再入队 pause{stop}。
func (s *Service) shutdownThenPauseStop(orb db.Orb, wait time.Duration) {
	ctx := context.Background()
	s.shutdownOrbd(ctx, orb.ID, wait)
	cur, err := s.Get(ctx, orb.ID)
	if err != nil || cur.State != StateStopping {
		return
	}
	if err := s.enqueuePause(ctx, cur, PauseStop); err != nil {
		slog.Warn("入队停机任务失败", "orb", orb.ID, "err", err)
		_, _, _ = s.transition(ctx, orb.ID, StateStopping, StateFailed, map[string]any{"reason": err.Error()})
	}
}

// shutdownOrbd 向 orbd 发 shutdown（无 link 时至多等待 wait）。失败只记录。
func (s *Service) shutdownOrbd(ctx context.Context, orbID string, wait time.Duration) {
	l := s.waitLink(ctx, orbID, wait)
	if l == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.ShutdownGrace+10)*time.Second)
	defer cancel()
	var res apigen.LinkCtlOkResult
	if err := l.ctl.Call(cctx, string(apigen.MethodShutdown), apigen.LinkCtlShutdownParams{GraceSeconds: s.cfg.ShutdownGrace}, &res); err != nil {
		slog.Warn("orbd shutdown 失败（继续）", "orb", orbID, "err", err)
	}
}

// Delete 删除 orb：→ deleting，向 orbd 发 shutdown（可达时），再入队 delete。
func (s *Service) Delete(ctx context.Context, orbID string) error {
	for {
		orb, err := s.Get(ctx, orbID)
		if err != nil {
			return err
		}
		if orb.State == StateDeleted || orb.State == StateDeleting {
			return nil
		}
		_, moved, err := s.transition(ctx, orbID, orb.State, StateDeleting, nil)
		if err != nil {
			return err
		}
		if !moved {
			continue
		}
		s.mu.Lock()
		delete(s.wakeAfter, orbID)
		s.mu.Unlock()
		frozen := orb.State == StateFrozen || orb.State == StateFreezing
		s.goBG(func() {
			ctx := context.Background()
			if !frozen {
				s.shutdownOrbd(ctx, orbID, 0)
			}
			if _, err := s.jobs.Enqueue(ctx, orb.ProviderID, orbID, jobs.OpDelete, nil); err != nil {
				slog.Warn("入队删除任务失败", "orb", orbID, "err", err)
				_, _, _ = s.transition(ctx, orbID, StateDeleting, StateFailed, map[string]any{"reason": err.Error()})
			}
		})
		return nil
	}
}

// releaseSnapshot 在没有未删除的 orb 使用快照 snap 时入队 delete_snapshot（使用它的容器删除后镜像才可删除）。
func (s *Service) releaseSnapshot(ctx context.Context, providerID, orbID, snap string) {
	n, err := s.st.Q.CountLiveOrbsBySnapshot(ctx, &snap)
	if err != nil {
		slog.Warn("查询快照引用失败", "snapshot", snap, "err", err)
		return
	}
	if n > 0 {
		return
	}
	if _, err := s.jobs.Enqueue(ctx, providerID, orbID, jobs.OpDeleteSnapshot, apigen.JobDeleteSnapshotParams{SnapshotID: snap}); err != nil {
		slog.Warn("入队快照删除失败", "snapshot", snap, "err", err)
		return
	}
	slog.Info("快照已无引用，入队删除", "snapshot", snap)
}

// Snapshot 为 orb 入队 snapshot 任务并等待完成，返回快照 id（Docker 镜像名）。
func (s *Service) Snapshot(ctx context.Context, orbID string) (string, error) {
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return "", err
	}
	if orb.State == StateDeleting || orb.State == StateDeleted {
		return "", fmt.Errorf("%w: orb %s 已删除", ErrInvalidState, orbID)
	}
	snap := ids.Snapshot()
	job, err := s.jobs.Enqueue(ctx, orb.ProviderID, orbID, jobs.OpSnapshot, apigen.JobSnapshotParams{SnapshotID: snap})
	if err != nil {
		return "", err
	}
	done, err := s.jobs.Wait(ctx, job.ID)
	if err != nil {
		return "", err
	}
	if done.State != jobs.StateSucceeded {
		msg := "snapshot failed"
		if done.Error != nil {
			msg = *done.Error
		}
		return "", fmt.Errorf("快照 orb %s: %s", orbID, msg)
	}
	return snap, nil
}

// TouchUser 记录一次用户交互。
func (s *Service) TouchUser(ctx context.Context, orbID string) {
	ts := s.nowMs()
	if err := s.st.Q.TouchOrbUserInteraction(ctx, db.TouchOrbUserInteractionParams{Ts: &ts, ID: orbID}); err != nil {
		slog.Warn("记录用户交互失败", "orb", orbID, "err", err)
	}
}

// TouchAgent 记录一次 agent 活动。
func (s *Service) TouchAgent(ctx context.Context, orbID string) {
	ts := s.nowMs()
	if err := s.st.Q.TouchOrbAgentActivity(ctx, db.TouchOrbAgentActivityParams{Ts: &ts, ID: orbID}); err != nil {
		slog.Warn("记录 agent 活动失败", "orb", orbID, "err", err)
	}
}

func jobPauseMode(job db.Job) string {
	var p apigen.JobPauseParams
	_ = json.Unmarshal([]byte(job.Params), &p)
	return string(p.Mode)
}

func (s *Service) onJobClaimed(ctx context.Context, job db.Job) {
	if job.OrbID == nil || job.Op != jobs.OpSpawn {
		return
	}
	if _, _, err := s.transition(ctx, *job.OrbID, StateRequested, StateSpawning, map[string]any{"job": job.ID, "attempt": job.Attempt}); err != nil {
		slog.Warn("spawn 认领状态更新失败", "orb", *job.OrbID, "err", err)
	}
}

func (s *Service) onJobFinished(ctx context.Context, job db.Job, ok bool) {
	if job.OrbID == nil {
		return
	}
	orbID := *job.OrbID
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return
	}
	detail := map[string]any{"job": job.ID}
	if job.Error != nil {
		detail["error"] = *job.Error
	}
	switch job.Op {
	case jobs.OpSpawn:
		switch orb.State {
		case StateSpawning, StateWaking, StateRequested:
			to := StateConnecting
			if !ok {
				to = StateFailed
			}
			if _, moved, _ := s.transition(ctx, orbID, orb.State, to, detail); moved && ok {
				s.advance(orbID)
			}
		case StateStopping:
			if ok {
				s.goBG(func() { s.shutdownThenPauseStop(orb, s.cfg.ConnectTimeout/2) })
			} else {
				_, _, _ = s.transition(ctx, orbID, StateStopping, StateFailed, detail)
			}
		}
	case jobs.OpPause:
		if jobPauseMode(job) == PauseFreeze {
			to := StateFrozen
			if !ok {
				to = StateReady
			}
			_, _, _ = s.transition(ctx, orbID, StateFreezing, to, detail)
		} else {
			to := StateStopped
			if !ok {
				to = StateFailed
			}
			_, _, _ = s.transition(ctx, orbID, StateStopping, to, detail)
		}
		s.mu.Lock()
		again := s.wakeAfter[orbID]
		delete(s.wakeAfter, orbID)
		s.mu.Unlock()
		if again {
			if err := s.Wake(ctx, orbID); err != nil {
				slog.Warn("延迟唤醒失败", "orb", orbID, "err", err)
			}
		}
	case jobs.OpDelete:
		if !ok {
			_, _, _ = s.transition(ctx, orbID, StateDeleting, StateFailed, detail)
			return
		}
		if _, moved, _ := s.transition(ctx, orbID, StateDeleting, StateDeleted, detail); moved {
			s.Kick(orbID, "orb deleted")
			s.closeEgress(orbID)
			if orb.SnapshotOf != nil && *orb.SnapshotOf != "" {
				s.releaseSnapshot(ctx, orb.ProviderID, orbID, *orb.SnapshotOf)
			}
		}
	}
}

// Run 恢复中间状态并运行空闲评估，直到 ctx 结束。任务队列的租约巡检（jobs.Queue.Run）由调用方另行运行。
func (s *Service) Run(ctx context.Context) {
	s.recover(ctx)
	t := time.NewTicker(s.cfg.EvalInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.EvaluateIdle(ctx)
		}
	}
}

// recover 处理 orynd 重启时遗留的中间状态：setup / resume 中的 orb 回到 connecting 等待重新 hello，
// connecting 的 orb 重新计时。
func (s *Service) recover(ctx context.Context) {
	orbs, err := s.st.Q.ListLiveOrbs(ctx)
	if err != nil {
		slog.Warn("恢复 orb 状态失败", "err", err)
		return
	}
	for _, orb := range orbs {
		switch orb.State {
		case StateSettingUp, StateResuming:
			_, _, _ = s.transition(ctx, orb.ID, orb.State, StateConnecting, map[string]any{"reason": "control plane restarted"})
		case StateConnecting:
			s.armConnectTimer(orb.ID, StateConnecting)
		}
	}
}

// EvaluateIdle 执行一次空闲评估：ready 且静止超时 → 冻结；frozen 超过 stop_after_frozen 且静止 → 停机。
func (s *Service) EvaluateIdle(ctx context.Context) {
	now := s.nowMs()
	for _, state := range []string{StateReady, StateFrozen} {
		orbs, err := s.st.Q.ListOrbsByState(ctx, state)
		if err != nil {
			slog.Warn("空闲评估失败", "err", err)
			return
		}
		for _, orb := range orbs {
			if !s.isQuiescent(orb.ID) {
				continue
			}
			if state == StateReady {
				agent := max(orb.StateChangedAt, deref64(orb.LastAgentActivityAt))
				user := max(orb.StateChangedAt, deref64(orb.LastUserInteractionAt))
				if now-agent >= s.cfg.Idle.AgentIdle.D().Milliseconds() && now-user >= s.cfg.Idle.UserIdle.D().Milliseconds() {
					if err := s.freeze(ctx, orb, "idle"); err != nil {
						slog.Warn("空闲冻结失败", "orb", orb.ID, "err", err)
					}
				}
				continue
			}
			if now-orb.StateChangedAt >= s.cfg.Idle.StopAfterFrozen.D().Milliseconds() {
				if err := s.stop(ctx, orb, "frozen too long"); err != nil {
					slog.Warn("空闲停机失败", "orb", orb.ID, "err", err)
				}
			}
		}
	}
}

// Close 关闭全部 link 与 egress 服务，并等待后台流程结束（最多 ShutdownGrace+15 秒）。
func (s *Service) Close() {
	s.mu.Lock()
	links := make([]*orbLink, 0, len(s.links))
	for _, l := range s.links {
		links = append(links, l)
	}
	orbIDs := make([]string, 0, len(s.egress))
	for id := range s.egress {
		orbIDs = append(orbIDs, id)
	}
	for _, t := range s.timers {
		t.Stop()
	}
	s.timers = map[string]*time.Timer{}
	s.mu.Unlock()
	for _, l := range links {
		_ = l.sess.Close()
	}
	for _, id := range orbIDs {
		s.closeEgress(id)
	}
	done := make(chan struct{})
	go func() { s.bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Duration(s.cfg.ShutdownGrace+15) * time.Second):
	}
}

func (s *Service) goBG(fn func()) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		fn()
	}()
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
