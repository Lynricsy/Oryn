package threads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// runner 线程（M11b，计划 7.3）：线程直接跑在用户机器上 runner 声明的目录（或 Oryn 在其旁边建的 worktree）里。

// 错误码（HTTP 状态见 internal/api 的映射表）。
const (
	CodeRunnerOffline     = "runner_offline"
	CodeRunnerUnsupported = "runner_unsupported"
	CodeDirNotServed      = "dir_not_served"
	CodeDirInUse          = "dir_in_use"
	CodeWorktreeFailed    = "worktree_failed"
	CodeWorktreeDirty     = "worktree_dirty"
)

// worktreeTimeout 是 worktree.add / worktree.remove 的应答上限（orbd 侧 fetch 限 30 秒）。
var worktreeTimeout = 60 * time.Second

// runnerTarget 是新线程在 runner 上的落点：常驻 orb、工作目录与（要建时）worktree。
type runnerTarget struct {
	orb      db.Orb
	cwd      string
	worktree *apigen.ThreadWorktree
}

// servedDir 返回 dir 规范化之后的形式，以及它是否等于或位于 runner 声明的某个目录之下。
// runner 上报的都是 POSIX 绝对路径（解析符号链接之后），按 POSIX 语义比较。
func servedDir(dirs []apigen.RunnerDir, dir string) (string, bool) {
	if !path.IsAbs(dir) {
		return "", false
	}
	clean := path.Clean(dir)
	for _, d := range dirs {
		root := path.Clean(d.Path)
		if !path.IsAbs(root) {
			continue
		}
		if clean == root || strings.HasPrefix(clean, strings.TrimSuffix(root, "/")+"/") {
			return clean, true
		}
	}
	return clean, false
}

// prepareRunner 校验 runner 与目录，按需建 worktree（在建线程行之前），返回落点。
func (s *Service) prepareRunner(ctx context.Context, p CreateParams) (*runnerTarget, error) {
	if p.BrowserProfileID != nil && *p.BrowserProfileID != "" {
		return nil, errf("invalid", "browserProfileId cannot be set for a runner")
	}
	r, err := s.orbs.RunnerOrb(ctx, p.RunnerID)
	if errors.Is(err, orbs.ErrNotFound) {
		return nil, errf("not_found", "runner not found")
	}
	if err != nil {
		return nil, err
	}
	if !r.Online() {
		return nil, errf(CodeRunnerOffline, fmt.Sprintf("runner %s is offline", r.Name))
	}
	dir, ok := servedDir(r.Dirs, p.Dir)
	if !ok {
		return nil, errf(CodeDirNotServed, fmt.Sprintf("%q is not inside a directory served by runner %s", p.Dir, r.Name))
	}
	t := &runnerTarget{orb: r.Orb, cwd: dir}
	if p.Worktree == "" {
		if !p.ShareDir {
			ids, err := s.st.Q.ListThreadsByOrbCwd(ctx, db.ListThreadsByOrbCwdParams{OrbID: r.Orb.ID, Cwd: dir})
			if err != nil {
				return nil, err
			}
			if len(ids) > 0 {
				msg := fmt.Sprintf("%s is already the working directory of %d thread(s)", dir, len(ids))
				e := errf(CodeDirInUse, msg)
				e.Body = apigen.DirInUseConflict{Code: apigen.DirInUseConflictCodeDirInUse, Message: msg, ThreadIds: ids}
				return nil, e
			}
		}
		return t, nil
	}
	ctl, ok := s.orbs.Ctl(r.Orb.ID)
	if !ok {
		return nil, errf(CodeRunnerOffline, fmt.Sprintf("runner %s is not connected", r.Name))
	}
	cctx, cancel := context.WithTimeout(ctx, worktreeTimeout)
	defer cancel()
	var res apigen.LinkCtlWorktreeAddResult
	if err := ctl.Call(cctx, string(apigen.MethodWorktreeAdd), apigen.LinkCtlWorktreeAddParams{Repo: dir, Name: p.Worktree}, &res); err != nil {
		return nil, worktreeErr(err, CodeWorktreeFailed)
	}
	if res.Dir == "" {
		return nil, errf(CodeWorktreeFailed, "worktree.add returned no directory")
	}
	t.cwd = path.Clean(res.Dir)
	t.worktree = &apigen.ThreadWorktree{Repo: dir, Dir: t.cwd, Branch: res.Branch}
	return t, nil
}

// worktreeErr 把 worktree.add / worktree.remove 的 ctl 错误转成线程错误；fallback 是 git 失败时的错误码。
func worktreeErr(err error, fallback string) error {
	var ce *link.CtlError
	switch {
	case errors.As(err, &ce) && ce.Code == CodeDirNotServed:
		return errf(CodeDirNotServed, ce.Message)
	case errors.As(err, &ce) && ce.Code == CodeWorktreeDirty:
		return errf(CodeWorktreeDirty, ce.Message)
	case errors.As(err, &ce):
		return errf(fallback, ce.Message)
	case errors.Is(err, link.ErrCtlClosed):
		return errf(CodeRunnerOffline, "runner disconnected")
	}
	return errf(fallback, err.Error())
}

// dropWorktree 在线程没能写入时尽力撤掉刚建的 worktree（失败只记录：目录与分支留在用户机器上）。
func (s *Service) dropWorktree(ctx context.Context, orbID string, wt *apigen.ThreadWorktree) {
	ctl, ok := s.orbs.Ctl(orbID)
	if !ok {
		slog.Warn("threads: 无法撤掉建线程失败后的 worktree（runner 已断开）", "orb", orbID, "dir", wt.Dir)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, worktreeTimeout)
	defer cancel()
	if err := ctl.Call(cctx, string(apigen.MethodWorktreeRemove), apigen.LinkCtlWorktreeRemoveParams{Dir: wt.Dir}, nil); err != nil {
		slog.Warn("threads: 撤掉建线程失败后的 worktree 失败", "orb", orbID, "dir", wt.Dir, "err", err)
	}
}

func worktreeJSON(wt *apigen.ThreadWorktree) *string {
	if wt == nil {
		return nil
	}
	b, _ := json.Marshal(wt)
	return new(string(b))
}

func parseWorktree(raw *string) *apigen.ThreadWorktree {
	if raw == nil || *raw == "" {
		return nil
	}
	var wt apigen.ThreadWorktree
	if json.Unmarshal([]byte(*raw), &wt) != nil || wt.Dir == "" {
		return nil
	}
	return &wt
}

// RemoveWorktree 对应 `POST threads/{id}/worktree/remove`：经 ctl worktree.remove 移除线程的 worktree（不强制：
// 有未提交改动 → worktree_dirty），成功后清空 threads.worktree。线程没有 worktree → conflict；runner 不在线 → runner_offline。
func (s *Service) RemoveWorktree(ctx context.Context, threadID string) error {
	row, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	wt := parseWorktree(row.Worktree)
	if wt == nil {
		return errf("conflict", "thread has no worktree")
	}
	if row.OrbID == nil {
		return errf(CodeRunnerOffline, "thread's runner no longer exists")
	}
	orbID := *row.OrbID
	ctl, ok := s.orbs.Ctl(orbID)
	if !ok || s.orbState(orbID) != orbStateReady {
		return errf(CodeRunnerOffline, "runner is offline")
	}
	cctx, cancel := context.WithTimeout(ctx, worktreeTimeout)
	defer cancel()
	var res apigen.LinkCtlWorktreeRemoveResult
	if err := ctl.Call(cctx, string(apigen.MethodWorktreeRemove), apigen.LinkCtlWorktreeRemoveParams{Dir: wt.Dir}, &res); err != nil {
		var ce *link.CtlError
		if !errors.As(err, &ce) || ce.Code != "not_found" {
			return worktreeErr(err, CodeWorktreeFailed)
		}
		// orbd 的登记里已没有它（例如用户手动移除过）：Oryn 不再管理这个目录，照常清掉
		slog.Info("threads: worktree 已不在 runner 的登记里", "thread", threadID, "dir", wt.Dir)
	}
	if err := s.st.Q.SetThreadWorktree(ctx, db.SetThreadWorktreeParams{Worktree: nil, ID: threadID}); err != nil {
		return err
	}
	slog.Info("threads: worktree 已移除", "thread", threadID, "dir", wt.Dir, "branchKept", res.BranchKept != nil && *res.BranchKept)
	s.publish(ctx, threadID)
	return nil
}

// terminalEnabled 报告线程所在 orb 是否开终端：runner 只在上报了 terminal 特性时开，普通 orb 不变（总是开）。
func (s *Service) terminalEnabled(orbID string) bool {
	kind, feats := s.orbs.Features(orbID)
	if kind != orbs.KindRunner {
		return true
	}
	for _, f := range feats {
		if f == orbs.FeatureTerminal {
			return true
		}
	}
	return false
}
