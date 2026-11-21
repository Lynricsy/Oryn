package threads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
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
	// M23 box：box 被停下、box 线程不支持的操作、把线程以 executor.kind = orb 挂到 box 的 orb 上。
	CodeBoxStopped     = "box_stopped"
	CodeBoxUnsupported = "box_unsupported"
	CodeOrbIsBox       = "orb_is_box"
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
		// 目录占用（dir_in_use）在写线程行的事务里检查（checkDirFreeTx）
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

// checkDirFreeTx 在写线程行的事务里检查 cwd 不是该 orb 上其它未归档线程的工作目录；占用 → dir_in_use（带占用线程的 id）。
func checkDirFreeTx(ctx context.Context, q *db.Queries, orbID, cwd string) error {
	ids, err := q.ListThreadsByOrbCwd(ctx, db.ListThreadsByOrbCwdParams{OrbID: orbID, Cwd: cwd})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	msg := fmt.Sprintf("%s is already the working directory of %d thread(s)", cwd, len(ids))
	e := errf(CodeDirInUse, msg)
	e.Body = apigen.DirInUseConflict{Code: apigen.DirInUseConflictCodeDirInUse, Message: msg, ThreadIds: ids}
	return e
}

// checkOrbTx 在写线程行的事务里复核线程要挂的 orb：已删除或正在删除 → not_found（与「最后一个线程删除时回收 orb」
// 串行，挂上去的线程不会落在被回收的 orb 上）；runner 另需 runners 关联仍在、provider 未停用。
func checkOrbTx(ctx context.Context, q *db.Queries, orbID string) error {
	o, err := q.GetOrb(ctx, orbID)
	if errors.Is(err, sql.ErrNoRows) {
		// orb 行不存在：交给外键报错
		return nil
	}
	if err != nil {
		return err
	}
	deleted := o.DeletedAt != nil || o.State == orbStateDeleted || o.State == orbs.StateDeleting
	if !orbs.IsRunner(o) {
		if deleted {
			return errf("not_found", "orb not found")
		}
		return nil
	}
	gone := errf("not_found", "runner not found")
	if deleted {
		return gone
	}
	r, err := q.GetRunnerByOrb(ctx, orbID)
	if errors.Is(err, sql.ErrNoRows) {
		return gone
	}
	if err != nil {
		return err
	}
	p, err := q.GetProvider(ctx, r.ProviderID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (p.Driver != orbs.DriverRunner || p.DisabledAt != nil)) {
		return gone
	}
	return err
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

// runnerProjectConfig 返回 runner 线程 agent.start 的项目层（M21）：runner 一台机器可同时跑多个项目的线程，项目层按线程
// 交付——项目层文件，加上该线程可用的本项目 SSH 主机合成的 ssh.json（RunnerAgent.ProjectSSH）；runner 是用户自己的机器，
// 交付前过一遍无密钥投递校验，含形似密钥内容的文件扣下不发，写进线程日志与 runner orb 的事件备注。只对 hello 含
// omphome-apply 的 runner 给出（旧版 orbd 用那台机器自己的 omp 配置）；线程不属于项目、项目已删除或一个文件都没有时
// 返回 nil。orb 线程返回 nil（orb 的项目层经 setup / omphome.apply 下发）。
func (s *Service) runnerProjectConfig(ctx context.Context, threadID, orbID string) (*apigen.ProjectID, *apigen.LinkCtlProjectConfig, error) {
	if s.cfg.Projects == nil {
		return nil, nil, nil
	}
	if kind, feats := s.orbs.Features(orbID); kind != orbs.KindRunner || !slices.Contains(feats, orbs.FeatureOmpHomeApply) {
		return nil, nil, nil
	}
	row, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil {
		return nil, nil, err
	}
	if row.ProjectID == nil || *row.ProjectID == "" {
		return nil, nil, nil
	}
	if _, err := s.st.Q.GetProject(ctx, *row.ProjectID); errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	pid := *row.ProjectID
	extra := map[string][]byte{}
	var known orbs.KnownSecrets
	if ra := s.runnerAgent; ra != nil {
		b, err := ra.ProjectSSH(ctx, threadID)
		if err != nil {
			return nil, nil, fmt.Errorf("读取项目 SSH 主机: %w", err)
		}
		if b != nil {
			extra["ssh.json"] = b
		}
		known = ra.KnownSecrets
	}
	screen, err := orbs.RunnerScreen(ctx, known)
	if err != nil {
		return nil, nil, err
	}
	cfg, withheld, err := orbs.RunnerProjectConfigOf(s.cfg.Projects, pid, extra, screen)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range withheld {
		slog.Warn("threads: runner 线程项目层的文件含形似密钥的内容，未交付", "thread", threadID, "project", pid, "path", w.Path, "reason", w.Reason)
	}
	if s.runnerAgent != nil {
		s.runnerAgent.NoteWithheld(ctx, orbID, threadID, withheld)
	}
	if cfg == nil {
		return nil, nil, nil
	}
	return &pid, cfg, nil
}

// dropThreadKey 注销 runner 线程的 SSH 公钥（M21）；没有 RunnerAgent 时什么也不做。
func (s *Service) dropThreadKey(threadID string) {
	if s.runnerAgent != nil {
		s.runnerAgent.DropThreadKey(threadID)
	}
}
