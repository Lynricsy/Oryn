package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	pushExpandMax = 500            // 一次推送最多关联的提交数，超出记 truncated
	pushExpireAge = 24 * time.Hour // 新提交一直没出现在镜像里的推送，过后记 missing
	pushKeepAge   = 30 * 24 * time.Hour
	pushSweepEach = time.Hour
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// GitPushed 实现 orbs.PushSink：记下线程经出站代理成功推送的分支（M24 阶段 ②），然后请求一次拉取；
// 拉取成功后由 expandPushes 展开成「提交 → 线程」。推送的仓库必须就是项目的仓库；只记 refs/heads/ 且不是删除的 ref。
func (s *Service) GitPushed(ctx context.Context, p orbs.PushedRefs) {
	prj, err := s.project(ctx, p.ProjectID)
	if err != nil {
		if !errors.Is(err, errNotFound) {
			slog.Warn("记录推送：读取项目", "project", p.ProjectID, "err", err)
		}
		return
	}
	if !hasRepo(prj) || repoKey(*prj.RepoUrl) != p.Host+"/"+p.Repo {
		return
	}
	now := s.cfg.Now().UnixMilli()
	n := 0
	for _, u := range p.Refs {
		if !strings.HasPrefix(u.Ref, "refs/heads/") || !shaRe.MatchString(u.New) || isZeroSHA(u.New) || !shaRe.MatchString(u.Old) {
			continue
		}
		if err := s.st.Q.InsertRepoPush(ctx, db.InsertRepoPushParams{
			ProjectID: prj.ID, ThreadID: p.ThreadID, OrbID: p.OrbID, Ref: u.Ref, OldSha: u.Old, NewSha: u.New, PushedAt: now,
		}); err != nil {
			slog.Warn("记录推送", "project", prj.ID, "thread", p.ThreadID, "ref", u.Ref, "err", err)
			continue
		}
		n++
	}
	if n > 0 {
		s.Kick(prj.ID)
	}
}

func isZeroSHA(sha string) bool { return strings.Trim(sha, "0") == "" }

// expandPushes 把项目待展开的推送展开成「提交 → 线程」（source=push）：新提交还不在镜像里的留到下次（24 h 后由
// sweepPushes 记 missing）。范围是推送前后两个提交之间；新分支或旧提交不在镜像里时取新提交相对默认分支独有的提交
// （推的就是默认分支或没有默认分支时取全部历史）。每次至多 pushExpandMax 个，超出记 truncated。失败只记日志。
func (s *Service) expandPushes(ctx context.Context, prj db.Project) {
	pending, err := s.st.Q.ListPendingRepoPushes(ctx, prj.ID)
	if err != nil {
		slog.Warn("读取待展开的推送", "project", prj.ID, "err", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	row, err := s.repoRow(ctx, prj.ID)
	if err != nil {
		slog.Warn("读取项目仓库记录", "project", prj.ID, "err", err)
		return
	}
	dir := s.dir(prj.ID)
	def := deref(deref(row).DefaultBranch)
	if def != "" && (!ValidRef(def) || !s.hasBranch(ctx, dir, def)) {
		def = ""
	}
	for _, p := range pending {
		if !s.hasCommit(ctx, dir, p.NewSha) {
			continue
		}
		args := []string{"rev-list", fmt.Sprintf("--max-count=%d", pushExpandMax+1), "--end-of-options", p.NewSha}
		switch {
		case !isZeroSHA(p.OldSha) && s.hasCommit(ctx, dir, p.OldSha):
			args = append(args, "^"+p.OldSha)
		case def != "" && p.Ref != "refs/heads/"+def:
			args = append(args, "^refs/heads/"+def)
		}
		res, err := s.git(ctx, gitRun{dir: dir, read: true, limit: (pushExpandMax + 1) * 66}, args...)
		if err != nil || res.code != 0 || res.truncated {
			slog.Warn("展开推送", "project", prj.ID, "push", p.ID, "err", err, "code", res.code, "stderr", res.stderr)
			continue
		}
		shas := strings.Fields(string(bytes.TrimSpace(res.out)))
		var note *string
		if len(shas) > pushExpandMax {
			shas, note = shas[:pushExpandMax], new("truncated")
		}
		now := s.cfg.Now().UnixMilli()
		err = s.st.Tx(ctx, func(q *db.Queries) error {
			for _, sha := range shas {
				if err := q.LinkRepoCommitThread(ctx, db.LinkRepoCommitThreadParams{
					ProjectID: prj.ID, Sha: sha, ThreadID: &p.ThreadID, Source: "push", LinkedAt: now,
				}); err != nil {
					return err
				}
			}
			return q.FinishRepoPush(ctx, db.FinishRepoPushParams{ExpandedAt: now, ExpandNote: note, ID: p.ID})
		})
		if err != nil {
			slog.Warn("写入推送的提交关联", "project", prj.ID, "push", p.ID, "err", err)
		}
	}
}

// hasCommit 报告镜像里是否有提交 sha。
func (s *Service) hasCommit(ctx context.Context, dir, sha string) bool {
	res, err := s.git(ctx, gitRun{dir: dir, read: true, limit: 512}, "cat-file", "-e", "--end-of-options", sha+"^{commit}")
	return err == nil && res.code == 0
}

// sweepPushes 把超过 24 h 仍未展开的推送记为 missing，删掉 30 天前已展开的推送记录。
func (s *Service) sweepPushes(ctx context.Context) {
	now := s.cfg.Now()
	if err := s.st.Q.ExpireRepoPushes(ctx, db.ExpireRepoPushesParams{
		Now: now.UnixMilli(), Before: now.Add(-pushExpireAge).UnixMilli(),
	}); err != nil && ctx.Err() == nil {
		slog.Warn("过期未展开的推送", "err", err)
	}
	if err := s.st.Q.DeleteRepoPushesBefore(ctx, now.Add(-pushKeepAge).UnixMilli()); err != nil && ctx.Err() == nil {
		slog.Warn("清理旧的推送记录", "err", err)
	}
}
