package orbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// runner（M11b，计划 7.1）：用户自己的机器以 `orbd run --runner --dir …` 主动接入。
// runner = 一行 driver='runner' 的 provider（令牌 ort_ 的哈希存在 providers.token_hash）+ 一台 kind='runner' 的常驻 orb
// + 一行 runners（最近一次 hello 的主机名、目录与特性）。runner 的 orb 没有 provider 进程：从不入队任务，
// 状态只随 link 变化——接入时 stopped → connecting → ready，断开超过 RunnerOfflineGrace 后 → stopped。

const (
	// KindOrb / KindRunner 是 orbs.kind 的取值。
	KindOrb    = "orb"
	KindRunner = "runner"
	// DriverRunner 是 runner 的 provider 行的 driver。
	DriverRunner = "runner"
	// FeatureTerminal 表示终端面板可用（runner 只在带 --terminal 时上报；未上报时不发 terminal_configure）。
	FeatureTerminal = "terminal"
	// FeatureMetrics 表示 orbd 上报 metrics.sample。
	FeatureMetrics = "metrics"
	// HeaderWorkdir 是转给 orbd 本机 API 的线程工作目录请求头（runner 形态必填）。
	HeaderWorkdir = "X-Oryn-Workdir"
	// RunnerOfflineGrace 是 runner 的 link 断开后仍保持原状态的时长：期间重连不改变状态，到期仍无 link 才 → stopped。
	RunnerOfflineGrace = 30 * time.Second
)

var (
	// ErrRunnerOffline 表示 runner 不在线（其 orb 为 stopped），只能等它自己重新接入。
	ErrRunnerOffline = errors.New("orbs: runner offline")
	// ErrRunnerUnsupported 表示 runner 的 orb 不支持该操作（冻结 / 停机 / 删除 / 快照 / 以它为源新建）。
	ErrRunnerUnsupported = errors.New("orbs: operation not supported on a runner")
)

// Runner 是一个 runner 的完整信息。
type Runner struct {
	// Name 与 CreatedAt 取自其 provider 行。
	Name      string
	CreatedAt int64
	Orb       db.Orb
	Row       db.Runner
	// Dirs / Features 是最近一次 hello 上报的（runners.dirs / features 解析后）。
	Dirs     []apigen.RunnerDir
	Features []string
}

// Online 报告 runner 是否在线（其 orb 为 ready）。
func (r Runner) Online() bool { return r.Orb.State == StateReady }

// IsRunner 报告 orb 是否是 runner 的常驻 orb。
func IsRunner(o db.Orb) bool { return o.Kind == KindRunner }

// SetWorkdir 在线程有工作目录时给发往 orbd 本机 API 的请求加 X-Oryn-Workdir（runner 形态必填；普通 orb 不带）。
func SetWorkdir(h http.Header, cwd *string) {
	if cwd != nil && *cwd != "" {
		h.Set(HeaderWorkdir, *cwd)
	}
}

func parseRunnerDirs(raw string) []apigen.RunnerDir {
	out := []apigen.RunnerDir{}
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = []apigen.RunnerDir{}
	}
	return out
}

func parseFeatures(raw string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

// RunnerView 把 ListRunners 的一行转换成 API 形态。
func RunnerView(r db.ListRunnersRow) apigen.Runner {
	out := apigen.Runner{
		ID: r.ProviderID, Name: r.Name, Online: r.OrbState == StateReady, OrbID: r.OrbID, CreatedAt: r.CreatedAt,
		Dirs: parseRunnerDirs(r.Dirs), Features: parseFeatures(r.Features), LastSeenAt: r.LastSeenAt,
		OrbdVersion: r.OrbdVersion, AgentVersion: r.AgentVersion,
	}
	if r.Hostname != "" {
		out.Hostname = new(r.Hostname)
	}
	if r.Os != "" {
		out.Os = new(r.Os)
	}
	if r.Arch != "" {
		out.Arch = new(r.Arch)
	}
	return out
}

// ListRunners 返回未删除的 runner（按名称排序）。
func (s *Service) ListRunners(ctx context.Context) ([]apigen.Runner, error) {
	rows, err := s.st.Q.ListRunners(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.Runner, 0, len(rows))
	for _, r := range rows {
		out = append(out, RunnerView(r))
	}
	return out, nil
}

// CreateRunner 在一个事务里建 runner 的 provider 行（driver='runner'，令牌只存哈希）、常驻 orb（kind='runner'、stopped）
// 与 runners 行，写 ” → stopped 事件。返回 runner 与只出现这一次的 ort_ 令牌。名字与 provider 共用唯一约束
// （重名时返回的错误含 UNIQUE constraint failed）。
func (s *Service) CreateRunner(ctx context.Context, name string) (apigen.Runner, string, error) {
	token := ids.RunnerToken()
	provID := ids.New("prov")
	orbID := ids.Orb()
	now := s.nowMs()
	raw, _ := json.Marshal(map[string]any{"runner": name})
	var ch *change
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateRunnerProvider(ctx, db.CreateRunnerProviderParams{ID: provID, Name: name, TokenHash: ids.Hash(token), CreatedAt: now}); err != nil {
			return err
		}
		// orb 令牌从不下发（runner 用 ort_ 接入）：token_hash 只为满足唯一非空，取一个 oot 之外前缀的随机值
		if err := q.CreateOrb(ctx, db.CreateOrbParams{
			ID: orbID, ProviderID: provID, Type: KindRunner, State: StateStopped, StateChangedAt: now,
			TokenHash: ids.Hash(ids.Token("rnd")), CreatedAt: now, Kind: new(KindRunner),
		}); err != nil {
			return err
		}
		if err := q.CreateRunner(ctx, db.CreateRunnerParams{ProviderID: provID, OrbID: orbID}); err != nil {
			return err
		}
		seq, err := q.InsertOrbEventSeq(ctx, db.InsertOrbEventSeqParams{OrbID: orbID, Ts: now, FromState: "", ToState: StateStopped, Detail: string(raw)})
		if err != nil {
			return err
		}
		orb, err := q.GetOrb(ctx, orbID)
		ch = &change{orb: orb, from: "", to: StateStopped, seq: seq, detail: string(raw)}
		return err
	})
	if err != nil {
		return apigen.Runner{}, "", fmt.Errorf("创建 runner: %w", err)
	}
	slog.Info("runner 已创建", "runner", name, "provider", provID, "orb", orbID)
	s.publish(ch)
	return RunnerView(db.ListRunnersRow{
		ProviderID: provID, OrbID: orbID, Dirs: "[]", Features: "[]", Name: name, CreatedAt: now, OrbState: StateStopped,
	}), token, nil
}

// errRunnerGone 表示 runner 的 provider 行已停用（事务内复核）。
var errRunnerGone = errors.New("orbs: runner already deleted")

// DeleteRunner 删除 runner：provider 行置 disabled_at 并换掉令牌哈希（ort_ 立即失效），orb 直接 → deleted（与吊销同一事务），
// 然后关闭它的 link。线程经 deleted 事件置空 orb_id、保留转录。不是在用的 runner → ErrNotFound。
func (s *Service) DeleteRunner(ctx context.Context, providerID string) error {
	p, err := s.st.Q.GetProvider(ctx, providerID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (p.Driver != DriverRunner || p.DisabledAt != nil)) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	r, err := s.st.Q.GetRunnerByProvider(ctx, providerID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	revoke := func(ctx context.Context, q *db.Queries, now int64) error {
		n, err := q.RevokeRunnerProvider(ctx, db.RevokeRunnerProviderParams{DisabledAt: &now, TokenHash: ids.Hash(ids.Token("rnd")), ID: providerID})
		if err == nil && n == 0 {
			err = errRunnerGone
		}
		return err
	}
	for {
		orb, err := s.Get(ctx, r.OrbID)
		if err != nil {
			return err
		}
		if orb.State == StateDeleted {
			// orb 已删除（不应出现）：只吊销
			if err := s.st.Tx(ctx, func(q *db.Queries) error { return revoke(ctx, q, s.nowMs()) }); errors.Is(err, errRunnerGone) {
				return ErrNotFound
			} else if err != nil {
				return err
			}
			break
		}
		ch, err := s.transitionTx(ctx, orb.ID, orb.State, StateDeleted, map[string]any{"reason": "runner deleted"}, transitionOpts{
			then: func(t *txn) error { return revoke(t.ctx, t.q, t.now) },
		})
		if errors.Is(err, errRunnerGone) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if ch == nil {
			// 状态在读与比较并交换之间变了（link 恰好接入 / 断开）：按新状态重来
			continue
		}
		s.publish(ch)
		break
	}
	s.stopRunnerOffline(r.OrbID)
	s.Kick(r.OrbID, "runner deleted")
	slog.Info("runner 已删除", "provider", providerID, "orb", r.OrbID)
	return nil
}

// RunnerOrb 返回 provider id 对应的在用 runner（含其 orb 与最近一次 hello 的目录 / 特性）；不是在用的 runner → ErrNotFound。
func (s *Service) RunnerOrb(ctx context.Context, providerID string) (Runner, error) {
	p, err := s.st.Q.GetProvider(ctx, providerID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (p.Driver != DriverRunner || p.DisabledAt != nil)) {
		return Runner{}, ErrNotFound
	}
	if err != nil {
		return Runner{}, err
	}
	row, err := s.st.Q.GetRunnerByProvider(ctx, providerID)
	if errors.Is(err, sql.ErrNoRows) {
		return Runner{}, ErrNotFound
	}
	if err != nil {
		return Runner{}, err
	}
	orb, err := s.Get(ctx, row.OrbID)
	if err != nil {
		return Runner{}, err
	}
	if orb.State == StateDeleted || orb.State == StateDeleting {
		return Runner{}, ErrNotFound
	}
	return Runner{
		Name: p.Name, CreatedAt: p.CreatedAt, Orb: orb, Row: row,
		Dirs: parseRunnerDirs(row.Dirs), Features: parseFeatures(row.Features),
	}, nil
}

// LiveFeatures 返回 orb 当前已 hello 的 link 上报的特性；没有这样的 link 时 ok = false。
func (s *Service) LiveFeatures(orbID string) (features []string, ok bool) {
	s.mu.Lock()
	l := s.links[orbID]
	s.mu.Unlock()
	if l == nil || !l.helloSeen() {
		return nil, false
	}
	return l.hello.Features, true
}

// Features 返回 orb 的种类（orb / runner）与已知特性：有已 hello 的 link 时取其 hello；没有时 runner 取最近一次上报的，
// 普通 orb 为 nil（未知）。
func (s *Service) Features(orbID string) (string, []string) {
	ctx := context.Background()
	kind := KindOrb
	o, err := s.st.Q.GetOrb(ctx, orbID)
	if err == nil && o.Kind != "" {
		kind = o.Kind
	}
	if f, ok := s.LiveFeatures(orbID); ok {
		return kind, f
	}
	if kind == KindRunner {
		if r, err := s.st.Q.GetRunnerByOrb(ctx, orbID); err == nil {
			return kind, parseFeatures(r.Features)
		}
	}
	return kind, nil
}

// orbForToken 按 link / whoami 的 Bearer 令牌找到 orb：oot_ → 按哈希找 orb；ort_ → 按哈希找未停用的 runner provider，
// 再取它的常驻 orb。其余前缀、未知或已吊销的令牌返回错误。
func (s *Service) orbForToken(ctx context.Context, token string) (db.Orb, error) {
	switch ids.TokenPrefix(token) {
	case "oot":
		o, err := s.st.Q.GetOrbByTokenHash(ctx, ids.Hash(token))
		if errors.Is(err, sql.ErrNoRows) {
			return o, errors.New("unknown orb token")
		}
		return o, err
	case "ort":
		p, err := s.st.Q.GetProviderByTokenHash(ctx, ids.Hash(token))
		if errors.Is(err, sql.ErrNoRows) {
			return db.Orb{}, errors.New("unknown runner token")
		}
		if err != nil {
			return db.Orb{}, err
		}
		if p.Driver != DriverRunner || p.DisabledAt != nil {
			return db.Orb{}, errors.New("runner token revoked")
		}
		r, err := s.st.Q.GetRunnerByProvider(ctx, p.ID)
		if err != nil {
			return db.Orb{}, fmt.Errorf("runner %s: %w", p.ID, err)
		}
		return s.st.Q.GetOrb(ctx, r.OrbID)
	}
	return db.Orb{}, errors.New("not an orb or runner token")
}

// orbLinkAcceptable 按 orb 种类判断是否接受 link：runner 接受 stopped / connecting / ready（接入即上线），其余同 linkAcceptable。
func orbLinkAcceptable(o db.Orb) bool {
	if IsRunner(o) {
		switch o.State {
		case StateStopped, StateConnecting, StateReady:
			return true
		}
		return false
	}
	return linkAcceptable(o.State)
}

// runnerConnecting 在 runner 接入时把 stopped → connecting（其余状态无操作）。
func (s *Service) runnerConnecting(ctx context.Context, orbID string) {
	if _, _, err := s.transition(ctx, orbID, StateStopped, StateConnecting, map[string]any{"reason": "runner link"}); err != nil {
		slog.Warn("runner 上线状态更新失败", "orb", orbID, "err", err)
	}
}

// runnerHello 记录 runner 的 hello（主机、目录、特性）。
func (s *Service) runnerHello(ctx context.Context, l *orbLink) {
	h := l.hello
	var info apigen.LinkRunnerInfo
	if h.Runner != nil {
		info = *h.Runner
	}
	if info.Dirs == nil {
		info.Dirs = []apigen.RunnerDir{}
	}
	feats := h.Features
	if feats == nil {
		feats = []string{}
	}
	dirs, _ := json.Marshal(info.Dirs)
	fs, _ := json.Marshal(feats)
	if err := s.st.Q.UpdateRunnerHello(ctx, db.UpdateRunnerHelloParams{
		Hostname: info.Hostname, Os: h.Os, Arch: h.Arch, Dirs: string(dirs), Features: string(fs), LastSeenAt: s.nowMs(), OrbID: l.orbID,
	}); err != nil {
		slog.Warn("记录 runner hello 失败", "orb", l.orbID, "err", err)
	}
}

// runnerReady 是 runner 的 runPhase：没有 setup / resume / 快照 / Profile，connecting 直接 → ready。
func (s *Service) runnerReady(ctx context.Context, orbID string) {
	if _, _, err := s.transition(ctx, orbID, StateConnecting, StateReady, map[string]any{"reason": "runner link"}); err != nil {
		slog.Warn("runner 就绪状态更新失败", "orb", orbID, "err", err)
	}
}

// afterFunc 是可注入的定时器（测试替换为手动触发）；返回的函数停止定时器。
func (s *Service) afterFunc(d time.Duration, fn func()) func() bool {
	if s.hookAfterFunc != nil {
		return s.hookAfterFunc(d, fn)
	}
	return time.AfterFunc(d, fn).Stop
}

// runnerTimer 是一个 runner 的离线定时器（以指针区分新旧定时器）。
type runnerTimer struct{ stop func() bool }

// armRunnerOffline 在 runner 的 link 断开时起离线定时器：到期仍无 link → ready / connecting → stopped。
func (s *Service) armRunnerOffline(orbID string) {
	ts := s.nowMs()
	if err := s.st.Q.TouchRunner(context.Background(), db.TouchRunnerParams{LastSeenAt: ts, OrbID: orbID}); err != nil {
		slog.Warn("记录 runner 在线时间失败", "orb", orbID, "err", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if old := s.runnerOffline[orbID]; old != nil {
		old.stop()
	}
	t := &runnerTimer{}
	// 回调先取 s.mu：登记（本函数持锁）完成之前它不会读到 runnerOffline
	t.stop = s.afterFunc(RunnerOfflineGrace, func() {
		s.mu.Lock()
		mine := s.runnerOffline[orbID] == t
		_, linked := s.links[orbID]
		if mine {
			delete(s.runnerOffline, orbID)
		}
		s.mu.Unlock()
		if !mine || linked {
			return
		}
		ctx := context.Background()
		for _, from := range []string{StateReady, StateConnecting} {
			if _, moved, err := s.transition(ctx, orbID, from, StateStopped, map[string]any{"reason": "runner offline"}); err != nil {
				slog.Warn("runner 离线状态更新失败", "orb", orbID, "err", err)
			} else if moved {
				slog.Info("runner 离线", "orb", orbID, "grace", RunnerOfflineGrace)
			}
		}
	})
	s.runnerOffline[orbID] = t
}

// stopRunnerOffline 取消 runner 的离线定时器（新 link 到来或 runner 删除时）。
func (s *Service) stopRunnerOffline(orbID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.runnerOffline[orbID]; t != nil {
		t.stop()
		delete(s.runnerOffline, orbID)
	}
}

// WhoamiHandler 返回 GET /orb/v1/whoami 的处理器：Bearer oot_… | ort_… → 200 OrbWhoami{orbId, kind}；
// 令牌无效、已吊销或其 orb 已删除 → 401 unauthorized。
func (s *Service) WhoamiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, apigen.Error{Code: "method_not_allowed", Message: "GET only"})
			return
		}
		v := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(v) <= len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
			writeJSON(w, http.StatusUnauthorized, apigen.Error{Code: "unauthorized", Message: "missing bearer token"})
			return
		}
		o, err := s.orbForToken(r.Context(), strings.TrimSpace(v[len(prefix):]))
		if err != nil || o.State == StateDeleted || o.State == StateDeleting || o.DeletedAt != nil {
			writeJSON(w, http.StatusUnauthorized, apigen.Error{Code: "unauthorized", Message: "invalid token"})
			return
		}
		kind := apigen.OrbKindOrb
		if IsRunner(o) {
			kind = apigen.OrbKindRunner
		}
		writeJSON(w, http.StatusOK, apigen.OrbWhoami{OrbID: o.ID, Kind: kind})
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
