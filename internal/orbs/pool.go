package orbs

// 预热池（M11a 步骤 3）：池 orb = pool_state 非空的普通 orb，没有线程，其余（spawn、setup、快照命中与发布、
// 空闲冻结）与任何 orb 一样。pool_state：warming（已创建、未 ready）→ idle（ready 或 frozen，可认领）→ 认领后
// claimed（刷新中）→ NULL；回收时 retiring。所有变更都是带旧值条件的比较并交换，这就是认领、回收、重复认领之间
// 的全部围栏。

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orbs.pool_state 取值。retiring 不对外暴露（View 只给出 warming / idle / claimed）。
const (
	PoolWarming  = "warming"
	PoolIdle     = "idle"
	PoolClaimed  = "claimed"
	PoolRetiring = "retiring"
)

const (
	// poolSetupBackoff 是池 orb setup 失败后该项目暂停预热的时长。
	poolSetupBackoff = time.Hour
	// maxWarmOrbs 是单个项目 warm_orbs 的上限（API 同样校验 0–3）。
	maxWarmOrbs = 3
)

// PoolKick 立即触发一次预热池对账（容量 1，合并重复触发）；给出的项目同时清掉其 setup 失败退避
// （项目设置被修改后应重新尝试）。
func (s *Service) PoolKick(projectIDs ...string) {
	if len(projectIDs) > 0 {
		s.mu.Lock()
		for _, id := range projectIDs {
			delete(s.poolBackoff, id)
		}
		s.mu.Unlock()
	}
	select {
	case s.poolKick <- struct{}{}:
	default:
	}
}

// poolUsable 报告 pool_state 是否是可认领 / 计入池容量的状态（warming 或 idle）。
func poolUsable(orb db.Orb) bool {
	ps := derefStr(orb.PoolState)
	return ps == PoolWarming || ps == PoolIdle
}

// poolDead 报告池 orb 是否已经不可能再就绪（需要回收）。
func poolDead(state string) bool {
	switch state {
	case StateFailed, StateStopping, StateStopped, StateDeleting, StateDeleted:
		return true
	}
	return false
}

// ClaimPool 认领一台与 p 匹配（项目、provider、类型、配置哈希全等）的预热池 orb（M11a 3.2）。顺序：idle 且 ready →
// idle 且冻结中 / 已冻结 → warming（创建早的在前）；第一个比较并交换成功的即返回。没有可认领的返回 false。
// 请求了浏览器 Profile、指定了快照的不走池（Profile 须在 ready 之前装载）。
func (s *Service) ClaimPool(ctx context.Context, p CreateParams) (db.Orb, bool) {
	if s.cfg.Pool.MaxTotal <= 0 || p.ProjectID == "" || p.SnapshotID != "" || p.BrowserProfileID != "" || p.Coordinator || p.Pool {
		return db.Orb{}, false
	}
	t, err := s.resolveTarget(ctx, p)
	if err != nil || t.prj == nil {
		return db.Orb{}, false
	}
	hash, err := s.configHash(ctx, s.st.Q, *t.prj, t.prov, t.typ)
	if err != nil {
		return db.Orb{}, false
	}
	rows, err := s.st.Q.ListPoolOrbs(ctx, &p.ProjectID)
	if err != nil {
		slog.Warn("读取预热池失败", "project", p.ProjectID, "err", err)
		return db.Orb{}, false
	}
	rank := func(o db.Orb) int {
		switch {
		case derefStr(o.PoolState) == PoolIdle && o.State == StateReady:
			return 0
		case derefStr(o.PoolState) == PoolIdle && (o.State == StateFrozen || o.State == StateFreezing):
			return 1
		case derefStr(o.PoolState) == PoolWarming && !poolDead(o.State):
			return 2
		}
		return -1
	}
	var cands []db.Orb
	for _, o := range rows {
		if o.ProviderID == t.prov.ID && o.Type == t.typ && derefStr(o.ConfigHash) == hash && rank(o) >= 0 {
			cands = append(cands, o)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return rank(cands[i]) < rank(cands[j]) })
	for _, o := range cands {
		var ok bool
		switch rank(o) {
		case 0:
			ok = s.claimReady(ctx, o.ID)
		case 1:
			ok = s.claimFrozen(ctx, o.ID)
		case 2:
			ok = s.claimWarming(ctx, o.ID)
		}
		if !ok {
			continue
		}
		slog.Info("认领预热池 orb", "orb", o.ID, "project", p.ProjectID, "state", o.State, "pool", derefStr(o.PoolState))
		s.TouchUser(ctx, o.ID)
		s.PoolKick() // 补位
		orb, err := s.Get(ctx, o.ID)
		if err != nil {
			return db.Orb{}, false
		}
		return orb, true
	}
	return db.Orb{}, false
}

// claimWarming 认领尚未就绪的池 orb：直接离开池（pool_state = NULL）；它照常走完 setup 进入 ready，不需要额外刷新。
func (s *Service) claimWarming(ctx context.Context, orbID string) bool {
	n, err := s.st.Q.ClaimPoolOrb(ctx, db.ClaimPoolOrbParams{PoolState: nil, ClaimedAt: s.nowMs(), ID: orbID, Seen: PoolWarming})
	if err != nil || n == 0 {
		return false
	}
	s.noteOrb(ctx, orbID, map[string]any{"pool": PoolClaimed})
	return true
}

// claimReady 认领就绪的池 orb：ready → connecting（守卫 pool_state = idle，同一事务 idle → claimed，并轮换令牌、
// 入队 spawn，S-P2-3），随后由新令牌重连的 link 触发 runPhase，以带 repo.update 的 setup.run 刷新、回到 ready 时离开池。
func (s *Service) claimReady(ctx context.Context, orbID string) bool {
	ch, err := s.transitionTx(ctx, orbID, StateReady, StateConnecting, map[string]any{"reason": "pool claim"}, transitionOpts{
		guard: func(o db.Orb) bool { return derefStr(o.PoolState) == PoolIdle },
		then: func(t *txn) error {
			n, err := t.q.ClaimPoolOrb(t.ctx, db.ClaimPoolOrbParams{PoolState: new(PoolClaimed), ClaimedAt: t.now, ID: orbID, Seen: PoolIdle})
			if err == nil && n == 0 {
				err = errPoolMoved
			}
			if err != nil {
				return err
			}
			return s.spawnIn(t)
		},
	})
	if err != nil || ch == nil {
		return false
	}
	s.publish(ch)
	return true
}

// refreshClaimed 把处于 from 的已认领池 orb（pool_state = claimed）移到 connecting，走与 claimReady 相同的刷新路径
// （轮换令牌、spawn，重连后 setup.run 带 repo.update）；不是已认领的池 orb 或状态已变时返回 false。
func (s *Service) refreshClaimed(ctx context.Context, orbID, from string, detail map[string]any) bool {
	d := map[string]any{"reason": "pool claim refresh"}
	for k, v := range detail {
		d[k] = v
	}
	ch, err := s.transitionTx(ctx, orbID, from, StateConnecting, d, transitionOpts{
		guard: func(o db.Orb) bool { return derefStr(o.PoolState) == PoolClaimed },
		then:  s.spawnIn,
	})
	if err != nil {
		slog.Warn("刷新已认领的池 orb 失败", "orb", orbID, "err", err)
	}
	if err != nil || ch == nil {
		return false
	}
	s.publish(ch)
	return true
}

// claimFrozen 认领冻结的池 orb：先 idle → claimed，再唤醒（唤醒失败只记录：线程自己的唤醒逻辑会重试）。
func (s *Service) claimFrozen(ctx context.Context, orbID string) bool {
	n, err := s.st.Q.ClaimPoolOrb(ctx, db.ClaimPoolOrbParams{PoolState: new(PoolClaimed), ClaimedAt: s.nowMs(), ID: orbID, Seen: PoolIdle})
	if err != nil || n == 0 {
		return false
	}
	s.noteOrb(ctx, orbID, map[string]any{"pool": PoolClaimed})
	if err := s.Wake(ctx, orbID); err != nil {
		slog.Warn("唤醒认领的池 orb 失败", "orb", orbID, "err", err)
	}
	return true
}

var errPoolMoved = errors.New("orbs: pool state moved")

// retirePoolOrb 回收池 orb：pool_state seen → retiring（比较并交换）成功后删除。返回是否回收。
func (s *Service) retirePoolOrb(ctx context.Context, orbID, seen, reason string) bool {
	n, err := s.st.Q.SetOrbPoolState(ctx, db.SetOrbPoolStateParams{PoolState: new(PoolRetiring), ID: orbID, Seen: seen})
	if err != nil || n == 0 {
		return false
	}
	slog.Info("回收预热池 orb", "orb", orbID, "reason", reason)
	s.noteOrb(ctx, orbID, map[string]any{"pool": PoolRetiring, "reason": reason})
	if err := s.Delete(ctx, orbID); err != nil {
		slog.Warn("删除池 orb 失败", "orb", orbID, "err", err)
	}
	return true
}

// failWarming 处理 warming 池 orb 的 setup 失败：回收它并让该项目退避 1 小时；返回是否回收（已被认领的 orb
// 照常进入 ready，agent 要能进去修脚本）。
func (s *Service) failWarming(ctx context.Context, orb db.Orb) bool {
	if !s.retirePoolOrb(ctx, orb.ID, PoolWarming, "setup_failed") {
		return false
	}
	if orb.ProjectID != nil {
		s.mu.Lock()
		s.poolBackoff[*orb.ProjectID] = s.now().Add(poolSetupBackoff)
		s.mu.Unlock()
	}
	return true
}

// recoverPool 在启动时补做被重启打断的池操作：retiring 但还没进入删除的补删（回收在 CAS 与 Delete 之间被打断）；
// 已认领却停在 ready 的补刷新、停在 frozen 的补唤醒（认领后的刷新 / 延迟唤醒只在内存里登记过）。
func (s *Service) recoverPool(ctx context.Context) {
	rows, err := s.st.Q.ListPoolOrbs(ctx, nil)
	if err != nil {
		slog.Warn("恢复预热池失败", "err", err)
		return
	}
	for _, o := range rows {
		switch derefStr(o.PoolState) {
		case PoolRetiring:
			if o.State == StateDeleting || o.State == StateDeleted {
				continue
			}
			slog.Info("对账：补删回收中的池 orb", "orb", o.ID, "state", o.State)
			if err := s.Delete(ctx, o.ID); err != nil {
				slog.Warn("删除池 orb 失败", "orb", o.ID, "err", err)
			}
		case PoolClaimed:
			switch o.State {
			case StateReady:
				slog.Info("对账：补刷新已认领的池 orb", "orb", o.ID)
				s.refreshClaimed(ctx, o.ID, StateReady, nil)
			case StateFrozen:
				slog.Info("对账：补唤醒已认领的池 orb", "orb", o.ID)
				if err := s.Wake(ctx, o.ID); err != nil {
					slog.Warn("唤醒认领的池 orb 失败", "orb", o.ID, "err", err)
				}
			}
		}
	}
}

// poolPlan 是一个项目本轮的预热目标：want = 应常备的台数；t / hash 在可解析时给出（用于判断现有池 orb 是否过期）。
type poolPlan struct {
	want     int
	resolved bool
	t        target
	hash     string
}

// planPool 计算项目的预热目标（M11a 3.4 第 1 步的 eligible 条件全部成立才 want = warm_orbs）。
func (s *Service) planPool(ctx context.Context, prj *db.Project, now time.Time) poolPlan {
	var pl poolPlan
	if prj == nil || prj.ArchivedAt != nil {
		return pl
	}
	t, err := s.resolveTarget(ctx, CreateParams{ProjectID: prj.ID})
	if err == nil {
		if hash, herr := s.configHash(ctx, s.st.Q, *t.prj, t.prov, t.typ); herr == nil {
			pl.resolved, pl.t, pl.hash = true, t, hash
		}
	}
	if s.cfg.Pool.MaxTotal <= 0 || prj.WarmOrbs <= 0 || !pl.resolved {
		return pl
	}
	if derefStr(prj.RepoUrl) == "" && derefStr(prj.SetupScript) == "" {
		return pl // 没有可预热的东西
	}
	last, err := s.st.Q.MaxThreadCreatedAtByProject(ctx, prj.ID)
	if err != nil || last < now.Add(-s.cfg.Pool.ActiveWindow.D()).UnixMilli() {
		return pl // 冷项目不占资源
	}
	if t.prov.LeaseUntil == nil || *t.prov.LeaseUntil <= now.UnixMilli() {
		return pl // provider 离线
	}
	s.mu.Lock()
	until, backoff := s.poolBackoff[prj.ID]
	s.mu.Unlock()
	if backoff && now.Before(until) {
		return pl
	}
	pl.want = min(int(prj.WarmOrbs), maxWarmOrbs)
	return pl
}

// poolReconcile 是预热池对账（M11a 3.4）：回收过期的池 orb，把每个项目的池削到目标台数；全局 warming + idle 超过
// pool.max_total（例如上限被调低）时跨项目回收超额的（CP-4）；最后在全局未满时为不足的项目补位，每个项目每轮至多
// 新建一台。只在 Run 的循环里调用（串行）。
func (s *Service) poolReconcile(ctx context.Context) {
	rows, err := s.st.Q.ListPoolOrbs(ctx, nil)
	if err != nil {
		slog.Warn("预热池对账失败", "err", err)
		return
	}
	byProject := map[string][]db.Orb{}
	total := 0
	for _, o := range rows {
		if !poolUsable(o) {
			continue
		}
		byProject[derefStr(o.ProjectID)] = append(byProject[derefStr(o.ProjectID)], o)
		total++
	}
	var projects []db.Project
	if s.cfg.Pool.MaxTotal > 0 {
		archived := false
		if projects, err = s.st.Q.ListProjects(ctx, &archived); err != nil {
			slog.Warn("预热池对账失败", "err", err)
			return
		}
	}
	ids := map[string]bool{}
	for _, p := range projects {
		if p.WarmOrbs > 0 {
			ids[p.ID] = true
		}
	}
	for id := range byProject {
		ids[id] = true
	}
	order := make([]string, 0, len(ids))
	for id := range ids {
		order = append(order, id)
	}
	sort.Strings(order)
	now := s.now()
	// leave 回收一台池 orb（带旧 pool_state 的比较并交换）；返回它是否已不在可用池里（回收成功，或期间被认领 / 回收）。
	leave := func(o db.Orb, reason string) bool {
		if s.retirePoolOrb(ctx, o.ID, derefStr(o.PoolState), reason) {
			return true
		}
		cur, err := s.st.Q.GetOrb(ctx, o.ID)
		return err == nil && !poolUsable(cur)
	}
	byAge := func(live []db.Orb) {
		// 先最老的 idle，再最老的 warming
		sort.SliceStable(live, func(i, j int) bool {
			ii, jj := derefStr(live[i].PoolState) == PoolIdle, derefStr(live[j].PoolState) == PoolIdle
			if ii != jj {
				return ii
			}
			return live[i].CreatedAt < live[j].CreatedAt
		})
	}
	lives := map[string][]db.Orb{}
	wants := map[string]int{}
	for _, pid := range order {
		var prj *db.Project
		if pid != "" {
			v, err := s.st.Q.GetProject(ctx, pid)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				slog.Warn("预热池对账：读取项目失败", "project", pid, "err", err)
				continue
			}
			if err == nil {
				prj = &v
			}
		}
		pl := s.planPool(ctx, prj, now)
		// 1. 回收过期的：状态已不可能就绪、配置 / provider / 类型变了、idle 超过 max_age
		var live []db.Orb
		for _, o := range byProject[pid] {
			reason := ""
			switch {
			case poolDead(o.State):
				reason = "dead"
			case pl.resolved && (o.ProviderID != pl.t.prov.ID || o.Type != pl.t.typ || derefStr(o.ConfigHash) != pl.hash):
				reason = "stale"
			case derefStr(o.PoolState) == PoolIdle && s.cfg.Pool.MaxAge > 0 && now.UnixMilli()-o.CreatedAt >= s.cfg.Pool.MaxAge.D().Milliseconds():
				reason = "expired"
			}
			if reason == "" {
				live = append(live, o)
			} else if leave(o, reason) {
				total--
			}
		}
		// 2. 多了：先回收最老的 idle，再回收最老的 warming
		if len(live) > pl.want {
			byAge(live)
			for _, o := range live[:len(live)-pl.want] {
				if leave(o, "surplus") {
					total--
				}
			}
			live = live[len(live)-pl.want:]
		}
		lives[pid], wants[pid] = live, pl.want
	}
	// 3. 全局超额（pool.max_total 被调低）：跨项目同样先回收最老的 idle、再最老的 warming
	if limit := max(s.cfg.Pool.MaxTotal, 0); total > limit {
		var all []db.Orb
		for _, pid := range order {
			all = append(all, lives[pid]...)
		}
		byAge(all)
		gone := map[string]bool{}
		for _, o := range all {
			if total <= limit {
				break
			}
			if leave(o, "over_cap") {
				total--
				gone[o.ID] = true
			}
		}
		for pid, live := range lives {
			lives[pid] = slices.DeleteFunc(live, func(o db.Orb) bool { return gone[o.ID] })
		}
	}
	// 4. 少了：全局未满时每轮至多新建一台
	for _, pid := range order {
		if len(lives[pid]) < wants[pid] && total < s.cfg.Pool.MaxTotal {
			orb, err := s.Create(ctx, CreateParams{ProjectID: pid, Pool: true})
			if err != nil {
				slog.Warn("新建预热池 orb 失败", "project", pid, "err", err)
				continue
			}
			slog.Info("新建预热池 orb", "orb", orb.ID, "project", pid)
			total++
		}
	}
}
