package orbs

// 项目快照（M11a 步骤 2）：配置哈希、建 orb 时选快照、setup 之后发布、周期回收与配额。
//
// 键 = 项目 + provider + 类型；同键至多一行 building、一行 ready（部分唯一索引）。快照只在 setting_up 的 orb 上拍
// （agent 从未运行过；认领的池 orb 已跑过受管服务，不拍），且同步拍在 resume（支持推迟时）与 BeforeReady 之前：
// 快照里没有会话、附件、浏览器 Profile，也没有 resume 与受管服务的副作用。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
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
	// RetireSetupChanged：增量刷新时仓库内 setup / services.yaml 变了或基线不是快进（S-P2-1）。
	RetireSetupChanged = "setup_changed"
)

// orbd hello 特性（M11a）。
const (
	// FeatureRepoUpdate = orbd 支持 setup.run 的 repo.update。
	FeatureRepoUpdate = "repo-update"
	// FeatureDocker = orb 内的 dockerd 由 orbd 监督（嵌套 Docker）；发布项目快照前先 snapshot.prepare。
	FeatureDocker = "docker"
	// FeatureSetupDeferResume = orbd 支持 setup.run 的 deferResume（只跑 setup，resume 由控制面另行调 resume.run）。
	FeatureSetupDeferResume = "setup-defer-resume"
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

// configHash 是项目 orb 的配置哈希（M11a 2.1），按 provider 当前的基础镜像身份计算（建 orb、池对账与认领、发布复核用）。
func (s *Service) configHash(ctx context.Context, q *db.Queries, prj db.Project, prov db.Provider, typ string) (string, error) {
	stamps, err := q.ListConfigSecretStamps(ctx)
	if err != nil {
		return "", fmt.Errorf("读取密钥修改时间: %w", err)
	}
	return projectConfigHash(prj, prov.BaseImageID, typ, stamps), nil
}

// projectConfigHash 只含会改变 setup 结果、又不由「HEAD 变了就重跑」覆盖的输入。
// 不进哈希：resume 脚本（每次启动都跑）、仓库内 .oryn/setup（随基线提交变化）、omp-home（每次下发重写）、git 身份。
// 可能进入 orb 环境的密钥（env 模式密钥、可能被降级为环境变量的模型 key）只取名字与修改时间，不对值做任何摘要。
func projectConfigHash(prj db.Project, baseImage, typ string, stamps []db.ListConfigSecretStampsRow) string {
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
		V: 2, Repo: derefStr(prj.RepoUrl), Branch: derefStr(prj.BaseBranch), Shallow: prj.ShallowClone,
		Setup: scriptSHA(nonEmpty(prj.SetupScript)), Type: typ, BaseImage: baseImage, Env: env,
	}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sameStamps 报告两次读取的密钥修改时间是否一致（读值前后各读一次，判断值与时间戳出自同一份快照）。
func sameStamps(a, b []db.ListConfigSecretStampsRow) bool {
	return slices.Equal(a, b)
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

// recordAppliedHash 在 setup.run 实际跑了 setup 且成功后，把本次参数的配置哈希记为这台 orb 环境已应用的配置
// （orbs.config_hash，池匹配与对账用）。resume、跳过的 setup（完成标记已在）都不能证明新配置已生效，不更新。
func (s *Service) recordAppliedHash(ctx context.Context, orbID, hash string) {
	if hash == "" {
		return
	}
	if err := s.st.Q.SetOrbConfigHash(ctx, db.SetOrbConfigHashParams{ConfigHash: hash, ID: orbID}); err != nil {
		slog.Warn("记录 orb 配置哈希失败", "orb", orbID, "err", err)
	}
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

// detailSnapshotUnknown 是 orb 事件 detail 里的键：首次 spawn 带了项目快照、provider 却没上报 snapshotUsed，值为该快照 id。
const detailSnapshotUnknown = "snapshotUsedUnknown"

// snapshotUsageUnknown 报告 orb 的事件里是否记过「来源未知」（S-P1-1）。只在发布新血统前查一次，事件数有限。
func (s *Service) snapshotUsageUnknown(ctx context.Context, orbID string) bool {
	evs, err := s.st.Q.ListOrbEvents(ctx, orbID)
	if err != nil {
		slog.Warn("读取 orb 事件失败，按来源未知处理", "orb", orbID, "err", err)
		return true
	}
	for _, e := range evs {
		if !strings.Contains(e.Detail, detailSnapshotUnknown) {
			continue
		}
		var d map[string]any
		if json.Unmarshal([]byte(e.Detail), &d) == nil && d[detailSnapshotUnknown] != nil {
			return true
		}
	}
	return false
}

// snapshotSource 读取 orb 参与项目快照所需的项目、provider 与来源快照。来源只看 orb.snapshot_of（S-P1-1：
// 它只在 provider 确认实际用了快照时保留，见 onJobFinished），不看 orbd 自报的 prepared。ok=false 表示不参与：
// 无项目、协调 orb、快照未开启，或 orb 来自连环境 fork 的快照（用过快照的 orb 永远不开新血统）。
func (s *Service) snapshotSource(ctx context.Context, orb db.Orb) (prj db.Project, prov db.Provider, src *db.ProjectSnapshot, ok bool) {
	if orb.ProjectID == nil || orb.Coordinator || !s.cfg.Snapshots.Enabled {
		return prj, prov, nil, false
	}
	var err error
	if prj, err = s.st.Q.GetProject(ctx, *orb.ProjectID); err != nil {
		return prj, prov, nil, false
	}
	if prov, err = s.st.Q.GetProvider(ctx, orb.ProviderID); err != nil || !s.snapshotsOn(&prj, prov) {
		return prj, prov, nil, false
	}
	if orb.SnapshotOf != nil && *orb.SnapshotOf != "" {
		v, err := s.st.Q.GetProjectSnapshot(ctx, *orb.SnapshotOf)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				slog.Warn("查询项目快照失败", "orb", orb.ID, "err", err)
			}
			return prj, prov, nil, false // 连环境 fork 的快照：不参与项目快照
		}
		src = &v
	}
	return prj, prov, src, true
}

// mayPublish 报告 setting_up 的这台 orb 这次 setup.run 之后可能发布项目快照（S-P1-2：据此让支持的 orbd 推迟 resume）。
func (s *Service) mayPublish(ctx context.Context, orb db.Orb, hash string) bool {
	if hash == "" {
		return false
	}
	_, _, _, ok := s.snapshotSource(ctx, orb)
	return ok
}

// afterSetupSnapshot 在 setup.run 成功应答之后、resume 与 BeforeReady 之前按 M11a 2.5 处理项目快照。
// pristine = agent 从未在这台 orb 上运行过（setting_up）：只有它发布新快照；claimed = 刚被认领的池 orb：
// 它已跑过几个小时的 resume 与受管服务，不发布刷新快照，但来源快照的退役规则（刷新失败、setup 变化）照旧。
// hash = 本次 setup 参数对应的配置哈希。返回是否拍了快照（拍快照期间容器可能被暂停或停机，调用方须检查 link 是否还在）。
func (s *Service) afterSetupSnapshot(ctx context.Context, l *orbLink, orb db.Orb, res apigen.LinkCtlSetupRunResult, pristine, claimed bool, hash string) bool {
	if !pristine && !claimed {
		return false
	}
	prj, prov, src, ok := s.snapshotSource(ctx, orb)
	if !ok {
		return false
	}
	if !res.Ok {
		// 失败的 setup 永不发布；刷新失败则旧快照也不再可信
		if src != nil && res.SetupRan {
			s.retireSnapshot(ctx, src.ID, RetireRefreshFailed)
		}
		return false
	}
	if src != nil && res.SetupChanged != nil && *res.SetupChanged {
		// S-P2-1：仓库内 setup / services.yaml 变了或基线不是快进，增量刷新会继承快照里的旧状态：不以 src 为父发布，
		// 退役它；下一个 orb 从基础镜像开新血统。
		s.retireSnapshot(ctx, src.ID, RetireSetupChanged)
		s.noteOrb(ctx, orb.ID, map[string]any{"snapshotSkipped": RetireSetupChanged, "snapshotId": src.ID})
		return false
	}
	if !res.SetupRan || !pristine || hash == "" {
		return false // 命中（HEAD 未变）、没有可保存的东西，或认领的池 orb
	}
	now := s.nowMs()
	depth, lineage := int64(0), now
	if src == nil {
		if time.Duration(res.DurationMs)*time.Millisecond < s.cfg.Snapshots.MinSetup.D() {
			return false
		}
		if s.snapshotUsageUnknown(ctx, orb.ID) {
			// 从快照 spawn、provider 却没说是否用上：磁盘可能带着快照内容，不能洗成 depth 0 的新血统
			s.noteOrb(ctx, orb.ID, map[string]any{"snapshotSkipped": "provenance_unknown"})
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
		s.discardBuild(ctx, prov.ID, orb.ID, snapID)
		note := "failed"
		if errors.Is(err, errBuildCancelled) {
			note = "cancelled"
			slog.Info("项目快照构建已作废", "orb", orb.ID, "snapshot", snapID, "reason", err)
		} else {
			slog.Warn("发布项目快照失败", "orb", orb.ID, "snapshot", snapID, "err", err)
		}
		s.noteOrb(ctx, orb.ID, map[string]any{"snapshot": note, "snapshotId": snapID, "error": err.Error()})
		return true
	}
	// 发布后合计超出 max_total_gb：按 LRU 淘汰其它键的 ready 快照；刚发布的这份不动（同键跳过）
	s.reserveSnapshotQuota(ctx, prov.ID, prj.ID, orb.Type)
	return true
}

var (
	// errBuildGone 表示 building 行已不在（被回收器当作残留删掉）。
	errBuildGone = errors.New("orbs: 项目快照的 building 行已不存在")
	// errBuildCancelled 表示这次构建在发布前被作废（S-P2-2 / S-P2-4），镜像由构建者清理。
	errBuildCancelled = errors.New("orbs: 项目快照构建已作废")
)

// commitRe 是 git 提交 id（SHA-1 40 位或 SHA-256 64 位小写十六进制）。
var commitRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// validCommit 返回合法的提交 id；orbd 上报的不是十六进制提交（S-P2-5：可被 orb 内的假 git 伪造）时返回空串。
func validCommit(c *string) string {
	if c == nil || !commitRe.MatchString(*c) {
		return ""
	}
	return *c
}

// publishSnapshot 在一个事务里退役同键旧 ready（superseded）、把 building 转为 ready；项目配置在构建期间变了
// （哈希不等）则发布后立即退役（config_changed）。构建期间被作废（清除快照、关闭项目快照、删除项目、停用
// provider），或单个快照超过 max_snapshot_gb 时不发布：building → retired 并返回 errBuildCancelled，不去挤掉
// 同键的旧快照与其它项目。
func (s *Service) publishSnapshot(ctx context.Context, orb db.Orb, snapID, hash string, res apigen.LinkCtlSetupRunResult, size *int64) error {
	var stale bool
	var cancelled string
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		row, err := q.GetProjectSnapshot(ctx, snapID)
		if errors.Is(err, sql.ErrNoRows) {
			return errBuildGone
		}
		if err != nil {
			return err
		}
		if row.State != SnapshotBuilding {
			cancelled = derefStr(row.RetireReason)
			return nil
		}
		now := s.nowMs()
		var prj db.Project
		var prov db.Provider
		var perr, verr error
		if orb.ProjectID == nil {
			perr = sql.ErrNoRows
		} else {
			prj, perr = q.GetProject(ctx, *orb.ProjectID)
		}
		prov, verr = q.GetProvider(ctx, orb.ProviderID)
		switch {
		case errors.Is(perr, sql.ErrNoRows):
			cancelled = RetireProjectDeleted
		case perr != nil:
			return perr
		case errors.Is(verr, sql.ErrNoRows) || (verr == nil && prov.DisabledAt != nil):
			cancelled = RetireProviderRemoved
		case verr != nil:
			return verr
		case !prj.Snapshots:
			cancelled = RetireManual
		case size != nil && s.cfg.Snapshots.MaxSnapshotBytes() > 0 && *size > s.cfg.Snapshots.MaxSnapshotBytes():
			cancelled = RetireQuota
		}
		if cancelled != "" {
			_, err := q.CancelBuildingProjectSnapshot(ctx, db.CancelBuildingProjectSnapshotParams{
				RetiredAt: now, RetireReason: cancelled, SizeBytes: size, ID: snapID,
			})
			return err
		}
		cur, err := s.configHash(ctx, q, prj, prov, orb.Type)
		if err != nil {
			return err
		}
		stale = cur != hash
		// 先退役同键旧 ready：ready 的部分唯一索引不允许同键出现第二行 ready
		if err := q.RetireOtherReadySnapshots(ctx, db.RetireOtherReadySnapshotsParams{RetiredAt: now, RetireReason: RetireSuperseded, ID: snapID}); err != nil {
			return err
		}
		n, err := q.PublishProjectSnapshot(ctx, db.PublishProjectSnapshotParams{ReadyAt: now, SizeBytes: size, BaseCommit: validCommit(res.BaseCommit), ID: snapID})
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
	if cancelled != "" {
		return fmt.Errorf("%w（%s）", errBuildCancelled, cancelled)
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

// discardBuild 放弃一次构建并清理可能已提交的镜像（幂等）。只有赢得 building 行的一方删镜像（CP-2）：
//   - 删掉了 building 行：入队 delete_snapshot；
//   - 行已发布（ready，或发布后才退役）：别人的有效快照，什么也不做；
//   - 行在构建期间被作废（retired 且从未 ready）：镜像无人发布，由这里入队删除并登记到行上（gcRetired 不再重复入队）。
//
// orbID 非空时任务挂在该 orb 上，与它的 snapshot 任务串行（不会抢在仍排队的提交之前删除）。
func (s *Service) discardBuild(ctx context.Context, providerID, orbID, snapID string) {
	n, err := s.st.Q.DeleteBuildingProjectSnapshot(ctx, snapID)
	if err != nil {
		slog.Warn("删除 building 快照行失败", "snapshot", snapID, "err", err)
		return
	}
	if n == 0 {
		row, err := s.st.Q.GetProjectSnapshot(ctx, snapID)
		if err != nil || row.State != SnapshotRetired || row.ReadyAt != nil || row.GcJobID != nil {
			return
		}
		job, err := s.jobs.Enqueue(ctx, providerID, orbID, jobs.OpDeleteSnapshot, apigen.JobDeleteSnapshotParams{SnapshotID: snapID})
		if err != nil {
			slog.Warn("入队快照删除失败", "snapshot", snapID, "err", err)
			return
		}
		if err := s.st.Q.MarkProjectSnapshotGC(ctx, db.MarkProjectSnapshotGCParams{GcJobID: job.ID, GcAttemptAt: s.nowMs(), ID: snapID}); err != nil {
			slog.Warn("记录项目快照回收失败", "snapshot", snapID, "err", err)
		}
		return
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
	if r.ReadyAt == nil && r.GcJobID == nil && now-r.CreatedAt < snapshotBuildStale.Milliseconds() {
		// 构建期间被作废、尚未发布过：提交可能还在进行，镜像由构建者（discardBuild）挂在源 orb 上串行清理
		return
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

// reserveSnapshotQuota 按 LRU 缓存语义为 provider 上的（项目、类型）快照腾出配额：max_total_gb 约束的是快照缓存，
// 即 provider 上 ready 快照的大小之和（大小未上报按 0 计）。超过时按 LRU 退役其它键的 ready 快照（quota），同键的不动，
// 直到不超；返回此时是否不超。退役之后那份大小就不再计入：仍被 orb 引用的退役镜像属于引用它的 orb，
// 等 orb 删除后由回收器清掉，再退役别的快照也不能让它少占一个字节。超大的单个快照由 max_snapshot_gb 管。
func (s *Service) reserveSnapshotQuota(ctx context.Context, providerID, projectID, typ string) bool {
	limit := s.cfg.Snapshots.MaxTotalBytes()
	if limit <= 0 {
		return true
	}
	used, err := s.st.Q.SumProjectSnapshotBytes(ctx, providerID)
	if err != nil || used <= limit {
		return true
	}
	ready, err := s.st.Q.ListReadySnapshotsLRU(ctx, providerID)
	if err != nil {
		return true
	}
	for _, r := range ready {
		if used <= limit {
			break
		}
		if derefStr(r.ProjectID) == projectID && r.Type == typ {
			continue
		}
		if s.retireSnapshot(ctx, r.ID, RetireQuota) {
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
