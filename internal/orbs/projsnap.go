package orbs

// 项目快照（M11a 步骤 2）：配置哈希、建 orb 时选快照、setup 之后发布、周期回收与配额。
//
// 键 = 项目 + provider + 类型；同键至多一行 building、一行 ready（部分唯一索引）。快照只在「agent 从未在这台 orb
// 上运行过」时拍（setting_up，或刚被认领的池 orb），且同步拍在 BeforeReady 之前：快照里没有会话、附件与浏览器 Profile。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 项目快照状态（与 apigen.ProjectSnapshotState 取值一致）。
const (
	SnapshotBuilding = "building"
	SnapshotReady    = "ready"
	SnapshotRetired  = "retired"
)

// 项目快照的退役原因（与 apigen.ProjectSnapshot.retireReason 的说明一致）。
const (
	RetireConfigChanged   = "config_changed"
	RetireExpired         = "expired"
	RetireImageMissing    = "image_missing"
	RetireRefreshFailed   = "refresh_failed"
	RetireDepth           = "depth"
	RetireSuperseded      = "superseded"
	RetireQuota           = "quota"
	RetireManual          = "manual"
	RetireProjectDeleted  = "project_deleted"
	RetireProviderRemoved = "provider_removed"
)

// orbd hello 特性（M11a）。
const (
	// FeatureRepoUpdate = orbd 支持 setup.run 的 repo.update。
	FeatureRepoUpdate = "repo-update"
	// FeatureDocker = orb 内的 dockerd 由 orbd 监督（嵌套 Docker）；发布项目快照前先 snapshot.prepare。
	FeatureDocker = "docker"
)

// EventPool 表示 orb 的预热池状态变化或项目快照发布进展（非状态事件，只需推送 orb 更新）。
const EventPool = "pool"

const (
	// snapshotBuildStale 是 building 行的最长存活时间：超过即视为控制面重启留下的残留，删行并清理镜像。
	snapshotBuildStale = 15 * time.Minute
	// snapshotGCRetry 是同一份退役快照两次入队 delete_snapshot 的最小间隔。
	snapshotGCRetry = 10 * time.Minute
	// snapshotPrepareTimeout 是 snapshot.prepare（停掉内层容器）的应答超时。
	snapshotPrepareTimeout = 2 * time.Minute
)

// snapPick 是建 orb 时对项目快照的选择：hash = 当前配置哈希（有项目时），snapID = 命中的就绪快照（可空）。
type snapPick struct{ hash, snapID string }

// configHash 是项目 orb 的配置哈希（M11a 2.1）：只含会改变 setup 结果、又不由「HEAD 变了就重跑」覆盖的输入。
// 不进哈希：resume 脚本（每次启动都跑）、仓库内 .oryn/setup（随基线提交变化）、omp-home（每次下发重写）、git 身份。
// env 模式密钥只取名字与修改时间，不对值做任何摘要。
func (s *Service) configHash(ctx context.Context, q *db.Queries, prj db.Project, prov db.Provider, typ string) (string, error) {
	stamps, err := q.ListEnvSecretStamps(ctx)
	if err != nil {
		return "", fmt.Errorf("读取 env 密钥: %w", err)
	}
	env := make([][2]string, 0, len(stamps))
	for _, st := range stamps {
		env = append(env, [2]string{st.Name, strconv.FormatInt(st.UpdatedAt, 10)})
	}
	sort.Slice(env, func(i, j int) bool { return env[i][0] < env[j][0] })
	v := struct {
		V         int         `json:"v"`
		Repo      string      `json:"repo"`
		Branch    string      `json:"branch"`
		Shallow   bool        `json:"shallow"`
		Setup     string      `json:"setup"`
		Type      string      `json:"type"`
		BaseImage string      `json:"baseImage"`
		Env       [][2]string `json:"env"`
	}{
		V: 1, Repo: derefStr(prj.RepoUrl), Branch: derefStr(prj.BaseBranch), Shallow: prj.ShallowClone,
		Setup: scriptSHA(nonEmpty(prj.SetupScript)), Type: typ, BaseImage: prov.BaseImageID, Env: env,
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// snapshotsOn 报告该项目在该 provider 上是否建立并使用项目快照：全局开关、项目开关，且 provider 上报了基础镜像身份。
func (s *Service) snapshotsOn(prj *db.Project, prov db.Provider) bool {
	return s.cfg.Snapshots.Enabled && prj != nil && prj.Snapshots && prov.BaseImageID != ""
}

// isProjectSnapshot 报告 id 是否是项目快照（否则是连环境 fork 的快照）。
func (s *Service) isProjectSnapshot(ctx context.Context, q *db.Queries, id string) bool {
	_, err := q.GetProjectSnapshot(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("查询项目快照失败", "snapshot", id, "err", err)
	}
	return err == nil
}

// pickProjectSnapshot 计算新 orb 的配置哈希，并按 M11a 2.2 选用就绪的项目快照：哈希不等 → 退役（config_changed）；
// 血统超过 max_age → 退役（expired）；否则命中（由 insertOrb 在同一事务里登记使用）。
func (s *Service) pickProjectSnapshot(ctx context.Context, p CreateParams, t target) snapPick {
	if t.prj == nil || p.Coordinator {
		return snapPick{}
	}
	hash, err := s.configHash(ctx, s.st.Q, *t.prj, t.prov, t.typ)
	if err != nil {
		slog.Warn("计算项目配置哈希失败", "project", t.prj.ID, "err", err)
		return snapPick{}
	}
	pick := snapPick{hash: hash}
	if p.SnapshotID != "" || !s.snapshotsOn(t.prj, t.prov) {
		return pick
	}
	snap, err := s.st.Q.GetReadyProjectSnapshot(ctx, db.GetReadyProjectSnapshotParams{ProjectID: t.prj.ID, ProviderID: t.prov.ID, Type: t.typ})
	if errors.Is(err, sql.ErrNoRows) {
		return pick
	}
	if err != nil {
		slog.Warn("查询项目快照失败", "project", t.prj.ID, "err", err)
		return pick
	}
	switch {
	case snap.ConfigHash != hash:
		s.retireSnapshot(ctx, snap.ID, RetireConfigChanged)
	case s.nowMs()-snap.LineageAt >= s.cfg.Snapshots.MaxAge.D().Milliseconds():
		s.retireSnapshot(ctx, snap.ID, RetireExpired)
	default:
		pick.snapID = snap.ID
	}
	return pick
}

// retireSnapshot 把 ready 的项目快照退役；返回是否发生了变化。
func (s *Service) retireSnapshot(ctx context.Context, id, reason string) bool {
	n, err := s.st.Q.RetireProjectSnapshot(ctx, db.RetireProjectSnapshotParams{RetiredAt: s.nowMs(), RetireReason: reason, ID: id})
	if err != nil {
		slog.Warn("退役项目快照失败", "snapshot", id, "reason", reason, "err", err)
		return false
	}
	if n > 0 {
		slog.Info("项目快照已退役", "snapshot", id, "reason", reason)
	}
	return n > 0
}

// recordConfigHash 为项目 orb 计算这次 setup 实际使用的配置哈希并写入 orbs.config_hash（池匹配与发布都用它）；
// 无项目、协调 orb 或计算失败时返回空串。
func (s *Service) recordConfigHash(ctx context.Context, orb db.Orb) string {
	if orb.ProjectID == nil || orb.Coordinator {
		return ""
	}
	prj, err := s.st.Q.GetProject(ctx, *orb.ProjectID)
	if err != nil {
		return ""
	}
	prov, err := s.st.Q.GetProvider(ctx, orb.ProviderID)
	if err != nil {
		return ""
	}
	hash, err := s.configHash(ctx, s.st.Q, prj, prov, orb.Type)
	if err != nil {
		slog.Warn("计算项目配置哈希失败", "orb", orb.ID, "err", err)
		return ""
	}
	if err := s.st.Q.SetOrbConfigHash(ctx, db.SetOrbConfigHashParams{ConfigHash: hash, ID: orb.ID}); err != nil {
		slog.Warn("记录 orb 配置哈希失败", "orb", orb.ID, "err", err)
	}
	return hash
}

// wantRepoUpdate 报告这次 setup.run 是否置 repo.update：orbd 支持，且 agent 从未在这台 orb 上运行过——
// 来自项目快照、从未 ready 的新 orb，或刚被认领的池 orb。旧 orbd（hello 不含 repo-update）一律不置。
func (s *Service) wantRepoUpdate(ctx context.Context, l *orbLink, orb db.Orb, everReady bool) bool {
	if !slices.Contains(l.hello.Features, FeatureRepoUpdate) {
		return false
	}
	if derefStr(orb.PoolState) == PoolClaimed {
		return true
	}
	return !everReady && orb.SnapshotOf != nil && *orb.SnapshotOf != "" && s.isProjectSnapshot(ctx, s.st.Q, *orb.SnapshotOf)
}

// noteOrb 给 orb 写一条同状态事件（审计 / 统计用的旁注，如快照发布进展、池认领），并推送 orb 更新。
func (s *Service) noteOrb(ctx context.Context, orbID string, detail map[string]any) {
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return
	}
	raw, _ := json.Marshal(detail)
	if _, err := s.st.Q.InsertOrbEventSeq(ctx, db.InsertOrbEventSeqParams{
		OrbID: orbID, Ts: s.nowMs(), FromState: orb.State, ToState: orb.State, Detail: string(raw),
	}); err != nil {
		slog.Warn("写入 orb 事件失败", "orb", orbID, "err", err)
		return
	}
	s.emitInfo(ctx, EventPool, orb)
}

// afterSetupSnapshot 在 setup.run 成功应答之后、BeforeReady 之前按 M11a 2.5 发布项目快照。pristine = agent 从未在
// 这台 orb 上运行过（setting_up，或刚被认领的池 orb）；hash = 本次 setup 参数对应的配置哈希。返回是否拍了快照
// （拍快照期间容器被暂停，调用方须检查 link 是否还在）。
func (s *Service) afterSetupSnapshot(ctx context.Context, l *orbLink, orb db.Orb, res apigen.LinkCtlSetupRunResult, pristine bool, hash string) bool {
	if !pristine || orb.ProjectID == nil || orb.Coordinator || hash == "" || !s.cfg.Snapshots.Enabled {
		return false
	}
	prj, err := s.st.Q.GetProject(ctx, *orb.ProjectID)
	if err != nil {
		return false
	}
	prov, err := s.st.Q.GetProvider(ctx, orb.ProviderID)
	if err != nil || !s.snapshotsOn(&prj, prov) {
		return false
	}
	var src *db.ProjectSnapshot
	if orb.SnapshotOf != nil && *orb.SnapshotOf != "" {
		v, err := s.st.Q.GetProjectSnapshot(ctx, *orb.SnapshotOf)
		if errors.Is(err, sql.ErrNoRows) {
			return false // 连环境 fork 的快照：不参与项目快照
		}
		if err != nil {
			slog.Warn("查询项目快照失败", "orb", orb.ID, "err", err)
			return false
		}
		src = &v
	}
	if src != nil && (res.Prepared == nil || !*res.Prepared) {
		// 完成标记不在 = provider 退回了基础镜像（快照镜像已不存在）
		s.retireSnapshot(ctx, src.ID, RetireImageMissing)
		src = nil
	}
	if !res.Ok {
		// 失败的 setup 永不发布；刷新失败则旧快照也不再可信
		if src != nil && res.SetupRan {
			s.retireSnapshot(ctx, src.ID, RetireRefreshFailed)
		}
		return false
	}
	if !res.SetupRan {
		return false // 命中（HEAD 未变），或没有可保存的东西
	}
	now := s.nowMs()
	depth, lineage := int64(0), now
	if src == nil {
		if time.Duration(res.DurationMs)*time.Millisecond < s.cfg.Snapshots.MinSetup.D() {
			return false
		}
	} else {
		depth, lineage = src.Depth+1, src.LineageAt
		if depth > int64(s.cfg.Snapshots.MaxDepth) {
			s.retireSnapshot(ctx, src.ID, RetireDepth)
			return false
		}
	}
	if !s.reserveSnapshotQuota(ctx, prov.ID, prj.ID, orb.Type) {
		s.noteOrb(ctx, orb.ID, map[string]any{"snapshotSkipped": "quota"})
		return false
	}
	snapID := ids.Snapshot()
	n, err := s.st.Q.InsertBuildingProjectSnapshot(ctx, db.InsertBuildingProjectSnapshotParams{
		ID: snapID, ProjectID: prj.ID, ProviderID: prov.ID, Type: orb.Type, ConfigHash: hash,
		Depth: depth, LineageAt: lineage, SourceOrbID: &orb.ID, CreatedAt: now,
	})
	if err != nil {
		slog.Warn("登记项目快照失败", "orb", orb.ID, "err", err)
		return false
	}
	if n == 0 {
		return false // 同键已有构建在进行：并发去重，这台 orb 直接进 ready
	}
	s.noteOrb(ctx, orb.ID, map[string]any{"snapshot": SnapshotBuilding, "snapshotId": snapID})
	if s.hookSnapshotBuilding != nil {
		s.hookSnapshotBuilding(orb.ID, snapID)
	}
	if slices.Contains(l.hello.Features, FeatureDocker) {
		cctx, cancel := context.WithTimeout(ctx, snapshotPrepareTimeout)
		var r apigen.LinkCtlSnapshotPrepareResult
		if err := l.ctl.Call(cctx, string(apigen.MethodSnapshotPrepare), map[string]any{}, &r); err != nil {
			slog.Warn("snapshot.prepare 失败（继续）", "orb", orb.ID, "err", err)
		}
		cancel()
	}
	cur, err := s.Get(ctx, orb.ID)
	if err == nil {
		// 等待上限同 setup：provider 长时间离线时不让 orb 卡在 setting_up
		sctx, cancel := context.WithTimeout(ctx, s.cfg.SetupTimeout)
		var job db.Job
		job, err = s.snapshotAs(sctx, cur, snapID)
		cancel()
		if err == nil {
			err = s.publishSnapshot(ctx, orb, snapID, hash, res, jobs.ResultOf(job).SnapshotBytes)
		}
	}
	if err != nil {
		slog.Warn("发布项目快照失败", "orb", orb.ID, "snapshot", snapID, "err", err)
		s.discardBuild(ctx, prov.ID, orb.ID, snapID)
		s.noteOrb(ctx, orb.ID, map[string]any{"snapshot": "failed", "snapshotId": snapID, "error": err.Error()})
		return true
	}
	s.reserveSnapshotQuota(ctx, prov.ID, prj.ID, orb.Type)
	return true
}

// errBuildGone 表示 building 行已不在（被回收器当作残留删掉）。
var errBuildGone = errors.New("orbs: 项目快照的 building 行已不存在")

// publishSnapshot 在一个事务里退役同键旧 ready（superseded）、把 building 转为 ready；项目配置在构建期间变了
// （哈希不等或项目已删除）则发布后立即退役（config_changed）。
func (s *Service) publishSnapshot(ctx context.Context, orb db.Orb, snapID, hash string, res apigen.LinkCtlSetupRunResult, size *int64) error {
	var stale bool
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		stale = true
		if orb.ProjectID != nil {
			prj, perr := q.GetProject(ctx, *orb.ProjectID)
			prov, verr := q.GetProvider(ctx, orb.ProviderID)
			if perr == nil && verr == nil {
				cur, err := s.configHash(ctx, q, prj, prov, orb.Type)
				if err != nil {
					return err
				}
				stale = cur != hash
			}
		}
		now := s.nowMs()
		// 先退役同键旧 ready：ready 的部分唯一索引不允许同键出现第二行 ready
		if err := q.RetireOtherReadySnapshots(ctx, db.RetireOtherReadySnapshotsParams{RetiredAt: now, RetireReason: RetireSuperseded, ID: snapID}); err != nil {
			return err
		}
		n, err := q.PublishProjectSnapshot(ctx, db.PublishProjectSnapshotParams{ReadyAt: now, SizeBytes: size, BaseCommit: derefStr(res.BaseCommit), ID: snapID})
		if err != nil {
			return err
		}
		if n == 0 {
			return errBuildGone
		}
		if stale {
			_, err = q.RetireProjectSnapshot(ctx, db.RetireProjectSnapshotParams{RetiredAt: now, RetireReason: RetireConfigChanged, ID: snapID})
		}
		return err
	})
	if err != nil {
		return err
	}
	state := SnapshotReady
	if stale {
		state = SnapshotRetired
	}
	attrs := []any{"orb", orb.ID, "snapshot", snapID, "state", state}
	if size != nil {
		attrs = append(attrs, "bytes", *size)
	}
	slog.Info("项目快照已发布", attrs...)
	s.noteOrb(ctx, orb.ID, map[string]any{"snapshot": state, "snapshotId": snapID})
	return nil
}

// discardBuild 删掉 building 行并入队一次 delete_snapshot 清理可能已提交的半成品镜像（幂等）。orbID 非空时任务挂在
// 该 orb 上，与它的 snapshot 任务串行（不会抢在仍排队的提交之前删除）。
func (s *Service) discardBuild(ctx context.Context, providerID, orbID, snapID string) {
	if err := s.st.Q.DeleteBuildingProjectSnapshot(ctx, snapID); err != nil {
		slog.Warn("删除 building 快照行失败", "snapshot", snapID, "err", err)
	}
	if _, err := s.jobs.Enqueue(ctx, providerID, orbID, jobs.OpDeleteSnapshot, apigen.JobDeleteSnapshotParams{SnapshotID: snapID}); err != nil {
		slog.Warn("入队快照删除失败", "snapshot", snapID, "err", err)
	}
}

// onSnapshotDeleted 在 delete_snapshot 成功后删除对应的退役项目快照行（fork 快照不在表里，无操作）。
func (s *Service) onSnapshotDeleted(ctx context.Context, job db.Job) {
	var p apigen.JobDeleteSnapshotParams
	if err := json.Unmarshal([]byte(job.Params), &p); err != nil || p.SnapshotID == "" {
		return
	}
	row, err := s.st.Q.GetProjectSnapshot(ctx, p.SnapshotID)
	if err != nil || row.State != SnapshotRetired {
		return
	}
	if err := s.st.Q.DeleteProjectSnapshot(ctx, p.SnapshotID); err != nil {
		slog.Warn("删除项目快照行失败", "snapshot", p.SnapshotID, "err", err)
		return
	}
	slog.Info("项目快照镜像已删除", "snapshot", p.SnapshotID)
}

// sweepSnapshots 是项目快照的周期回收（M11a 2.6）：building 残留、ready 血统过期、retired 且无引用的镜像删除。
func (s *Service) sweepSnapshots(ctx context.Context) {
	now := s.nowMs()
	building, err := s.st.Q.ListProjectSnapshotsByState(ctx, SnapshotBuilding)
	if err != nil {
		slog.Warn("项目快照回收失败", "err", err)
		return
	}
	for _, r := range building {
		if now-r.CreatedAt >= snapshotBuildStale.Milliseconds() {
			slog.Info("清理残留的 building 项目快照", "snapshot", r.ID)
			s.discardBuild(ctx, r.ProviderID, derefStr(r.SourceOrbID), r.ID)
		}
	}
	if maxAge := s.cfg.Snapshots.MaxAge.D(); maxAge > 0 {
		ready, err := s.st.Q.ListProjectSnapshotsByState(ctx, SnapshotReady)
		if err != nil {
			slog.Warn("项目快照回收失败", "err", err)
			return
		}
		for _, r := range ready {
			if now-r.LineageAt >= maxAge.Milliseconds() {
				s.retireSnapshot(ctx, r.ID, RetireExpired)
			}
		}
	}
	retired, err := s.st.Q.ListProjectSnapshotsByState(ctx, SnapshotRetired)
	if err != nil {
		slog.Warn("项目快照回收失败", "err", err)
		return
	}
	for _, r := range retired {
		s.gcRetired(ctx, r, now)
	}
}

// gcRetired 在退役快照不再被任何未删除的 orb 引用时入队 delete_snapshot（上一次入队的任务仍在途则等待；已成功
// 但删行的回调因重启丢失则补删行；失败或从未入队则至多每 10 分钟重试一次）。
func (s *Service) gcRetired(ctx context.Context, r db.ProjectSnapshot, now int64) {
	id := r.ID
	if n, err := s.st.Q.CountLiveOrbsBySnapshot(ctx, &id); err != nil || n > 0 {
		return
	}
	if r.GcJobID != nil {
		if job, err := s.st.Q.GetJob(ctx, *r.GcJobID); err == nil {
			switch job.State {
			case jobs.StateQueued, jobs.StateClaimed:
				return
			case jobs.StateSucceeded:
				s.onSnapshotDeleted(ctx, job)
				return
			}
		}
	}
	if r.GcAttemptAt != nil && now-*r.GcAttemptAt < snapshotGCRetry.Milliseconds() {
		return
	}
	job, err := s.jobs.Enqueue(ctx, r.ProviderID, "", jobs.OpDeleteSnapshot, apigen.JobDeleteSnapshotParams{SnapshotID: id})
	if err != nil {
		slog.Warn("入队项目快照删除失败", "snapshot", id, "err", err)
		return
	}
	if err := s.st.Q.MarkProjectSnapshotGC(ctx, db.MarkProjectSnapshotGCParams{GcJobID: job.ID, GcAttemptAt: now, ID: id}); err != nil {
		slog.Warn("记录项目快照回收失败", "snapshot", id, "err", err)
	}
	slog.Info("退役的项目快照已无引用，入队删除", "snapshot", id)
}

// reserveSnapshotQuota 为 provider 上即将建立的（项目、类型）快照腾出配额：合计占用超过 max_total_gb 时按 LRU 退役
// 其它键的 ready 快照（quota），直到不超；仍超（剩下的都是被 orb 引用着的退役快照）返回 false。
// 占用 = ready 的大小 + 仍被 orb 引用的退役快照的大小（未被引用的退役快照马上会被回收器删掉）；大小未上报按 0 计。
func (s *Service) reserveSnapshotQuota(ctx context.Context, providerID, projectID, typ string) bool {
	limit := s.cfg.Snapshots.MaxTotalBytes()
	if limit <= 0 {
		return true
	}
	total, err := s.st.Q.SumProjectSnapshotBytes(ctx, providerID)
	if err != nil || total <= limit {
		return true
	}
	pinned := func(id string) bool {
		n, err := s.st.Q.CountLiveOrbsBySnapshot(ctx, &id)
		return err != nil || n > 0
	}
	ready, err := s.st.Q.ListReadySnapshotsLRU(ctx, providerID)
	if err != nil {
		return true
	}
	retired, err := s.st.Q.ListProjectSnapshotsByState(ctx, SnapshotRetired)
	if err != nil {
		return true
	}
	var used int64
	for _, r := range ready {
		used += deref64(r.SizeBytes)
	}
	for _, r := range retired {
		if r.ProviderID == providerID && pinned(r.ID) {
			used += deref64(r.SizeBytes)
		}
	}
	for _, r := range ready {
		if used <= limit {
			break
		}
		if derefStr(r.ProjectID) == projectID && r.Type == typ {
			continue
		}
		if s.retireSnapshot(ctx, r.ID, RetireQuota) && !pinned(r.ID) {
			used -= deref64(r.SizeBytes)
		}
	}
	return used <= limit
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
