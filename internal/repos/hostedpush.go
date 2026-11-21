package repos

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	savedRefsPrefix  = hiddenRefs + "/saved/" // 被删或被强推覆盖的 ref 旧值：refs/oryn/saved/<毫秒>/<原 ref 去掉 refs/>
	savedRefsKeep    = 30 * 24 * time.Hour
	refsSnapshotMax  = 64 << 20         // for-each-ref 输出上限
	pushAfterTimeout = 20 * time.Second // 推送后取快照与留存旧值的时限
	pushBookTimeout  = 30 * time.Second // 推送后记账（afterHostedPush）的时限
)

// Shipper 是自动交付入口（*threads.Service 实现）：线程把提交推上基线分支后标为已交付。
type Shipper interface {
	MarkShipped(ctx context.Context, id string, at int64) error
}

// serveHostedPush 服务托管仓库的一次 POST receive-pack：在每项目推送锁内前后各取一次 ref 快照，求差得到 ref 变化，
// 把被删除或非快进覆盖的旧值留存到 refs/oryn/saved/，解锁后同步记账（afterHostedPush）。快照与记账脱离请求的取消：
// 客户端先断开时 ref 可能已经更新，记账不能跟着丢。响应体此时已写完，记账的失败只记日志。
func (s *Service) serveHostedPush(w http.ResponseWriter, r *http.Request, prj db.Project, dir string, pusher egress.HostedPusher) {
	ctx := r.Context()
	release, err := s.pushLocks.lock(ctx, prj.ID)
	if err != nil {
		w.Header().Set("Retry-After", "5")
		smartError(w, http.StatusServiceUnavailable, "oryn: 仓库正忙，请稍后重试")
		return
	}
	updates, err := s.pushLocked(w, r, prj, dir, pusher)
	release()
	if err != nil || len(updates) == 0 {
		return
	}
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pushBookTimeout)
	defer cancel()
	s.afterHostedPush(bctx, prj, pusher, updates, s.cfg.Now())
}

// pushLocked 是 serveHostedPush 在推送锁内的部分：快照、服务、再快照、留存旧值。返回 ref 变化。
func (s *Service) pushLocked(w http.ResponseWriter, r *http.Request, prj db.Project, dir string, pusher egress.HostedPusher) ([]egress.RefUpdate, error) {
	before, err := s.refsSnapshot(r.Context(), dir)
	if err != nil {
		// 没有推送前的快照就留存不了旧值，宁可拒绝推送
		slog.Warn("托管仓库：推送前读取 ref", "project", prj.ID, "err", err)
		smartError(w, http.StatusInternalServerError, "oryn: 读取仓库 ref 失败")
		return nil, err
	}
	s.serveSmartHTTP(w, r, dir, EndpointReceivePack, pusher.OrbID)
	actx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), pushAfterTimeout)
	defer cancel()
	after, err := s.refsSnapshot(actx, dir)
	if err != nil {
		slog.Warn("托管仓库：推送后读取 ref", "project", prj.ID, "err", err)
		return nil, err
	}
	updates := diffRefs(before, after)
	s.saveOldRefs(actx, prj.ID, dir, updates, s.cfg.Now())
	return updates, nil
}

// refsSnapshot 返回仓库全部 ref（refname → 对象 id），不含 refs/oryn/ 下留存的旧值。
func (s *Service) refsSnapshot(ctx context.Context, dir string) (map[string]string, error) {
	res, err := s.git(ctx, gitRun{dir: dir, limit: refsSnapshotMax}, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return nil, err
	}
	if res.truncated || res.code != 0 {
		return nil, fmt.Errorf("git for-each-ref: exit %d (truncated=%v): %s", res.code, res.truncated, strings.TrimSpace(res.stderr))
	}
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(res.out))
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		oid, ref, ok := strings.Cut(sc.Text(), " ")
		if !ok || strings.HasPrefix(ref, hiddenRefs+"/") {
			continue
		}
		out[ref] = oid
	}
	return out, sc.Err()
}

// diffRefs 求两次快照之间的 ref 变化，按 ref 名排序；新建的 Old、删除的 New 为 40 个 0（托管仓库恒为 SHA-1）。
func diffRefs(before, after map[string]string) []egress.RefUpdate {
	var out []egress.RefUpdate
	for ref, oldOID := range before {
		if newOID := after[ref]; newOID != oldOID {
			out = append(out, egress.RefUpdate{Ref: ref, Old: oldOID, New: cmp.Or(newOID, zeroOID)})
		}
	}
	for ref, newOID := range after {
		if _, ok := before[ref]; !ok {
			out = append(out, egress.RefUpdate{Ref: ref, Old: zeroOID, New: newOID})
		}
	}
	slices.SortFunc(out, func(a, b egress.RefUpdate) int { return strings.Compare(a.Ref, b.Ref) })
	return out
}

// saveOldRefs 把被删除或非快进覆盖的 ref 旧值留存到 refs/oryn/saved/<毫秒>/<原 ref 去掉 refs/>：基线分支不设保护，
// 删 ref 时它的 reflog 一并消失，所以不靠 reflog 补救。留存 30 天（sweepSavedRefs）。失败只记日志。
func (s *Service) saveOldRefs(ctx context.Context, projectID, dir string, updates []egress.RefUpdate, at time.Time) {
	stamp := strconv.FormatInt(at.UnixMilli(), 10)
	for _, u := range updates {
		if isZeroSHA(u.Old) {
			continue
		}
		if !isZeroSHA(u.New) {
			// 退出码 0 = 旧值是新值的祖先（快进）；1 = 不是；其它（如旧值不是提交）一律当作覆盖留存
			res, err := s.git(ctx, gitRun{dir: dir, read: true, limit: 512}, "merge-base", "--is-ancestor", "--end-of-options", u.Old, u.New)
			if err == nil && res.code == 0 {
				continue
			}
		}
		saved := savedRefsPrefix + stamp + "/" + strings.TrimPrefix(u.Ref, "refs/")
		if err := s.gitOK(ctx, dir, "update-ref", "--end-of-options", saved, u.Old, zeroOID); err != nil {
			slog.Warn("托管仓库：留存 ref 旧值", "project", projectID, "ref", u.Ref, "old", u.Old, "err", err)
		}
	}
}

// afterHostedPush 是托管仓库推送成功后的记账：线程的推送写 repo_pushes 并立即展开成「提交 → 线程」、更新仓库大小、
// 广播 project_repo.changed；线程把提交推上项目基线分支时自动标为已交付。主人本机的推送（没有 orb / 线程）只更新与广播。
// 失败只记日志。
func (s *Service) afterHostedPush(ctx context.Context, prj db.Project, pusher egress.HostedPusher, updates []egress.RefUpdate, now time.Time) {
	if pusher.OrbID != "" && pusher.ThreadID != "" {
		for _, u := range updates {
			if !strings.HasPrefix(u.Ref, "refs/heads/") || isZeroSHA(u.New) {
				continue
			}
			if err := s.st.Q.InsertRepoPush(ctx, db.InsertRepoPushParams{
				ProjectID: prj.ID, ThreadID: pusher.ThreadID, OrbID: pusher.OrbID, Ref: u.Ref,
				OldSha: u.Old, NewSha: u.New, PushedAt: now.UnixMilli(),
			}); err != nil {
				slog.Warn("记录托管仓库推送", "project", prj.ID, "thread", pusher.ThreadID, "ref", u.Ref, "err", err)
			}
		}
		s.expandPushes(ctx, prj)
	}
	if err := s.RecordHosted(ctx, prj.ID, deref(prj.BaseBranch)); err != nil {
		slog.Warn("托管仓库：更新仓库大小", "project", prj.ID, "err", err)
	}
	s.changed(prj.ID)
	if pusher.ThreadID == "" || s.cfg.Ships == nil || deref(prj.BaseBranch) == "" {
		return
	}
	base := "refs/heads/" + deref(prj.BaseBranch)
	for _, u := range updates {
		if u.Ref == base && !isZeroSHA(u.New) {
			if err := s.cfg.Ships.MarkShipped(ctx, pusher.ThreadID, now.UnixMilli()); err != nil {
				slog.Warn("托管仓库：自动标记已交付", "project", prj.ID, "thread", pusher.ThreadID, "err", err)
			}
			return
		}
	}
}

// sweepSavedRefs 删掉各托管仓库里 30 天前留存的 refs/oryn/saved/*（按路径里的毫秒时间戳判断）。
func (s *Service) sweepSavedRefs(ctx context.Context) {
	if s.reason != "" {
		return
	}
	ents, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		return
	}
	cutoff := s.cfg.Now().Add(-savedRefsKeep).UnixMilli()
	for _, e := range ents {
		dir := filepath.Join(s.cfg.Dir, e.Name())
		if !e.IsDir() || !repoDirRe.MatchString(e.Name()) || !exists(filepath.Join(dir, hostedMarker)) {
			continue
		}
		if err := s.dropSavedBefore(ctx, dir, cutoff); err != nil && ctx.Err() == nil {
			slog.Warn("清理留存的 ref 旧值", "dir", dir, "err", err)
		}
	}
}

// dropSavedBefore 删掉 dir 里时间戳早于 cutoff（毫秒）的留存 ref。
func (s *Service) dropSavedBefore(ctx context.Context, dir string, cutoff int64) error {
	res, err := s.git(ctx, gitRun{dir: dir, read: true, limit: refsSnapshotMax}, "for-each-ref", "--format=%(refname)", savedRefsPrefix)
	if err != nil {
		return err
	}
	if res.truncated || res.code != 0 {
		return fmt.Errorf("git for-each-ref: exit %d (truncated=%v): %s", res.code, res.truncated, strings.TrimSpace(res.stderr))
	}
	var cmds strings.Builder
	for _, ref := range strings.Fields(string(res.out)) {
		stamp, _, _ := strings.Cut(strings.TrimPrefix(ref, savedRefsPrefix), "/")
		if ms, err := strconv.ParseInt(stamp, 10, 64); err == nil && ms < cutoff {
			cmds.WriteString("delete " + ref + "\n")
		}
	}
	if cmds.Len() == 0 {
		return nil
	}
	res, err = s.git(ctx, gitRun{dir: dir, stdin: strings.NewReader(cmds.String()), limit: gitOutMax}, "update-ref", "--stdin")
	if err != nil {
		return err
	}
	if res.code != 0 {
		return fmt.Errorf("git update-ref --stdin: exit %d: %s", res.code, strings.TrimSpace(res.stderr))
	}
	return nil
}
