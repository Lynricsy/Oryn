package repos

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// hostedMarker 是托管正本（阶段 ③）目录里的标记文件：镜像的清理逻辑永远不删带它的目录。
const hostedMarker = "oryn-hosted"

const (
	stopWait      = 10 * time.Second // Remove / 删除镜像时等进行中的拉取退出
	staleLockAge  = 10 * time.Minute // 启动时清掉更早的残留锁文件
	errMessageMax = 500
	gitOutMax     = 64 << 10
)

var (
	errSyncNoSpace  = &syncError{code: "no_space", msg: "控制面磁盘空间不足"}
	errSyncTooLarge = &syncError{code: "too_large", msg: "仓库超过镜像大小上限"}
	repoDirRe       = regexp.MustCompile(`^(prj_[0-9A-HJKMNP-TV-Z]{26})\.git$`)
)

// flight 是一个项目进行中的克隆或拉取；again = 跑完再来一轮。
type flight struct {
	cancel context.CancelFunc
	done   chan struct{}
	again  bool
}

func (s *Service) flying(projectID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.flights[projectID]
	return ok
}

// start 发起后台克隆或拉取（单飞）。已有飞行时：again 为真则让它跑完再来一轮，否则不动（懒触发）。
func (s *Service) start(projectID string, again bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if f := s.flights[projectID]; f != nil {
		f.again = f.again || again
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	f := &flight{cancel: cancel, done: make(chan struct{})}
	s.flights[projectID] = f
	s.wg.Add(1)
	go s.fly(ctx, projectID, f)
}

func (s *Service) fly(ctx context.Context, projectID string, f *flight) {
	defer s.wg.Done()
	defer f.cancel()
	s.changed(projectID)
	for {
		s.syncOnce(ctx, projectID)
		s.mu.Lock()
		if !f.again || ctx.Err() != nil {
			delete(s.flights, projectID)
			s.mu.Unlock()
			break
		}
		f.again = false
		s.mu.Unlock()
	}
	close(f.done)
	s.changed(projectID)
}

// stopFlight 取消项目进行中的拉取，至多等 stopWait。
func (s *Service) stopFlight(projectID string) {
	s.mu.Lock()
	f := s.flights[projectID]
	s.mu.Unlock()
	if f == nil {
		return
	}
	f.cancel()
	t := time.NewTimer(stopWait)
	defer t.Stop()
	select {
	case <-f.done:
	case <-t.C:
		slog.Warn("等待项目仓库拉取退出超时", "project", projectID)
	}
}

// ensure 核对仓库地址（指向别的仓库时删除镜像），auto 时按需在后台克隆或拉取；返回核对后的镜像行。
// 托管项目（阶段 ③）不克隆、不拉取、不比较地址，原样返回 project_repos 行。
func (s *Service) ensure(ctx context.Context, prj db.Project, auto bool) (*db.ProjectRepo, error) {
	row, err := s.repoRow(ctx, prj.ID)
	if err != nil || prj.Hosted {
		return row, err
	}
	if row, err = s.reconcile(ctx, prj, row); err != nil {
		return nil, err
	}
	if !auto || s.reason != "" || !hasRepo(prj) {
		return row, nil
	}
	if exists(s.dir(prj.ID)) {
		var last int64
		if row != nil {
			last = max(deref(row.SyncedAt), deref(row.ErrorAt))
		}
		if s.cfg.Now().UnixMilli()-last > s.cfg.StaleAfter.Milliseconds() {
			s.start(prj.ID, false)
		}
	} else if row == nil || row.ErrorCode == nil {
		// 首次克隆失败后不自动重试，等手动同步或推送事件
		s.start(prj.ID, false)
	}
	return row, nil
}

// reconcile 在项目不再有仓库地址、或地址指向别的仓库时删除镜像；GitHub 改名（绑定的仓库 id 不变）只更新 remote_url。
func (s *Service) reconcile(ctx context.Context, prj db.Project, row *db.ProjectRepo) (*db.ProjectRepo, error) {
	if !hasRepo(prj) {
		if row != nil || exists(s.dir(prj.ID)) {
			return nil, s.dropMirror(ctx, prj.ID)
		}
		return nil, nil
	}
	if row == nil || row.RemoteUrl == nil || repoKey(*row.RemoteUrl) == repoKey(*prj.RepoUrl) {
		return row, nil
	}
	if row.GithubRepoID != nil && prj.GithubRepoID != nil && *row.GithubRepoID == *prj.GithubRepoID {
		r := *row
		r.RemoteUrl, r.UpdatedAt = prj.RepoUrl, s.cfg.Now().UnixMilli()
		if err := s.st.Q.UpsertProjectRepo(ctx, db.UpsertProjectRepoParams(r)); err != nil {
			return nil, err
		}
		return &r, nil
	}
	return nil, s.dropMirror(ctx, prj.ID)
}

// repoKey 是比较仓库地址用的键：host/小写 path；不合法的地址按原文小写比较。
func repoKey(raw string) string {
	if host, path, ok := orbs.HTTPSRepo(raw); ok {
		return host + "/" + strings.ToLower(path)
	}
	return strings.ToLower(raw)
}

// dropMirror 删除镜像（保留项目）：取消飞行、删行、删目录，然后广播。
func (s *Service) dropMirror(ctx context.Context, projectID string) error {
	s.stopFlight(projectID)
	if err := s.st.Q.DeleteProjectRepo(ctx, projectID); err != nil {
		return err
	}
	if err := removeRepoDir(s.dir(projectID), false); err != nil {
		slog.Warn("删除项目仓库镜像", "project", projectID, "err", err)
	}
	s.dropLFSCache(projectID)
	s.changed(projectID)
	return nil
}

// syncOnce 克隆（没有镜像时）或拉取一次，并把结果写进 project_repos。
func (s *Service) syncOnce(ctx context.Context, projectID string) {
	prj, err := s.project(ctx, projectID)
	if err != nil {
		if !errors.Is(err, errNotFound) && ctx.Err() == nil {
			slog.Warn("读取项目", "project", projectID, "err", err)
		}
		return
	}
	if !hasRepo(prj) || prj.Hosted {
		return
	}
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-s.sem }()
	row, err := s.repoRow(ctx, projectID)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("读取项目仓库记录", "project", projectID, "err", err)
		}
		return
	}
	rm, err := s.remote(ctx, prj)
	if err != nil {
		s.fail(ctx, prj, row, rm, err)
		return
	}
	if free, err := freeBytes(s.cfg.Dir); err == nil && free < s.minFreeBytes() {
		s.fail(ctx, prj, row, rm, errSyncNoSpace)
		return
	}
	dir := s.dir(projectID)
	target, fresh := dir, !exists(dir)
	if fresh {
		target = dir + ".tmp-" + randSuffix()
		if err := s.initMirror(ctx, target, rm.url); err != nil {
			_ = os.RemoveAll(target)
			s.fail(ctx, prj, row, rm, err)
			return
		}
	} else if err := s.gitOK(ctx, target, "config", "remote.origin.url", rm.url); err != nil {
		s.fail(ctx, prj, row, rm, err)
		return
	}
	def, err := s.fetch(ctx, target, rm, fresh, deref(deref(row).DefaultBranch))
	if err != nil {
		if fresh {
			_ = os.RemoveAll(target)
		}
		s.fail(ctx, prj, row, rm, err)
		return
	}
	if fresh {
		if err := os.Rename(target, dir); err != nil {
			_ = os.RemoveAll(target)
			s.fail(ctx, prj, row, rm, err)
			return
		}
	}
	size, err := dirSize(dir)
	if err != nil {
		slog.Warn("统计项目仓库镜像大小", "project", projectID, "err", err)
	}
	if size > s.maxBytes() {
		if err := removeRepoDir(dir, false); err != nil {
			slog.Warn("删除超限的项目仓库镜像", "project", projectID, "err", err)
		}
		s.dropLFSCache(projectID)
		s.fail(ctx, prj, row, rm, errSyncTooLarge)
		return
	}
	now := s.cfg.Now().UnixMilli()
	p := db.UpsertProjectRepoParams{
		ProjectID: projectID, Kind: string(KindMirror), RemoteUrl: prj.RepoUrl, GithubRepoID: prj.GithubRepoID,
		SyncedAt: &now, SizeBytes: size, CreatedAt: now, UpdatedAt: now,
	}
	if def != "" {
		p.DefaultBranch = &def
	}
	if row != nil {
		p.CreatedAt = row.CreatedAt
	}
	if err := s.st.Q.UpsertProjectRepo(ctx, p); err != nil {
		if ctx.Err() == nil {
			slog.Warn("写入项目仓库记录", "project", projectID, "err", err)
		}
		return
	}
	// 线程经出站代理推送的提交此时已在镜像里：展开成「提交 → 线程」，之后才广播状态变化（fly 里的 changed）
	s.expandPushes(ctx, prj)
}

// initMirror 建空的裸仓库并写入镜像用的 remote 配置。
func (s *Service) initMirror(ctx context.Context, dir, url string) error {
	if err := s.gitOK(ctx, dir, "init", "--bare", "-q", "--template="); err != nil {
		return err
	}
	for _, kv := range [][]string{
		{"remote.origin.url", url},
		{"remote.origin.fetch", "+refs/heads/*:refs/heads/*"},
		{"--add", "remote.origin.fetch", "+refs/tags/*:refs/tags/*"},
		{"remote.origin.tagOpt", "--no-tags"},
		{"gc.autoDetach", "false"},
	} {
		if err := s.gitOK(ctx, dir, append([]string{"config"}, kv...)...); err != nil {
			return err
		}
	}
	return nil
}

// gitOK 执行本地的小命令（init / config / symbolic-ref），要求退出码 0。
func (s *Service) gitOK(ctx context.Context, dir string, args ...string) error {
	res, err := s.git(ctx, gitRun{dir: dir, limit: gitOutMax}, args...)
	if err != nil {
		return err
	}
	if res.code != 0 {
		return fmt.Errorf("git %s: exit %d: %s", args[0], res.code, strings.TrimSpace(res.stderr))
	}
	return nil
}

// fetch 取默认分支并拉取全部分支与标签；进行中每 monitorEvery 复查磁盘与大小。返回选定的默认分支。
func (s *Service) fetch(ctx context.Context, dir string, rm remote, fresh bool, oldDefault string) (string, error) {
	mctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := s.watch(mctx, cancel, dir)
	defer stop()
	causeErr := func(err error) error {
		if c := context.Cause(mctx); errors.Is(c, errSyncNoSpace) || errors.Is(c, errSyncTooLarge) {
			return c
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return &syncError{code: "timeout", msg: "拉取超时"}
		}
		return err
	}
	var lsDefault string
	res, err := s.git(mctx, gitRun{dir: dir, env: rm.env, limit: gitOutMax, timeout: lsRemoteTimeout}, "ls-remote", "--symref", "origin", "HEAD")
	switch {
	case err != nil && mctx.Err() != nil:
		return "", causeErr(err)
	case err == nil && res.code == 0:
		lsDefault = parseSymref(string(res.out))
	}
	timeout := fetchTimeout
	if fresh {
		timeout = cloneTimeout
	}
	res, err = s.git(mctx, gitRun{dir: dir, env: rm.env, limit: gitOutMax, timeout: timeout},
		"fetch", "--prune", "--quiet", "--no-write-fetch-head", "origin")
	if err != nil {
		return "", causeErr(err)
	}
	if res.code != 0 {
		return "", &syncError{code: classify(res.stderr, rm.token != ""), msg: redactSecrets(res.stderr, rm.token)}
	}
	def := s.chooseDefault(ctx, dir, lsDefault, oldDefault)
	if def != "" && s.hasBranch(ctx, dir, def) {
		if err := s.gitOK(ctx, dir, "symbolic-ref", "HEAD", "refs/heads/"+def); err != nil {
			return "", err
		}
	}
	return def, nil
}

// parseSymref 从 `ls-remote --symref origin HEAD` 的输出取默认分支；不合法时为空。
func parseSymref(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		target, ok := strings.CutPrefix(line, "ref: refs/heads/")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(target, "\t")
		if ValidRef(name) {
			return name
		}
	}
	return ""
}

// chooseDefault 依次取 ls-remote 的值、旧值、main、master 中第一个存在的分支，否则第一个分支；
// 一个分支都没有时保留 ls-remote 的值或旧值。
func (s *Service) chooseDefault(ctx context.Context, dir, lsDefault, oldDefault string) string {
	for _, c := range []string{lsDefault, oldDefault, "main", "master"} {
		if c != "" && ValidRef(c) && s.hasBranch(ctx, dir, c) {
			return c
		}
	}
	res, err := s.git(ctx, gitRun{dir: dir, read: true, limit: 1024}, "for-each-ref", "--count=1", "--format=%(refname:strip=2)", "refs/heads/")
	if err == nil && res.code == 0 {
		if b := strings.TrimSpace(string(res.out)); ValidRef(b) {
			return b
		}
	}
	if lsDefault != "" {
		return lsDefault
	}
	if ValidRef(oldDefault) {
		return oldDefault
	}
	return ""
}

// watch 每 monitorEvery 复查数据盘可用空间与 dir 的大小，不足或超限时以对应原因取消 ctx。
func (s *Service) watch(ctx context.Context, cancel context.CancelCauseFunc, dir string) (stop func()) {
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(s.monitorEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if free, err := freeBytes(s.cfg.Dir); err == nil && free < s.minFreeBytes() {
				cancel(errSyncNoSpace)
				return
			}
			if n, _ := dirSize(dir); n > s.maxBytes() {
				cancel(errSyncTooLarge)
				return
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

// fail 记录一次失败：已有镜像时保留它（状态仍 ready），首次克隆失败时 synced_at 为 NULL。被取消时什么都不写。
func (s *Service) fail(ctx context.Context, prj db.Project, row *db.ProjectRepo, rm remote, err error) {
	var se *syncError
	if !errors.As(err, &se) {
		if ctx.Err() != nil {
			return
		}
		se = &syncError{code: "fetch_failed", msg: err.Error()}
	}
	msg := tailRunes(strings.TrimSpace(redactSecrets(se.msg, rm.token)), errMessageMax)
	slog.Warn("项目仓库拉取失败", "project", prj.ID, "code", se.code, "message", msg)
	now := s.cfg.Now().UnixMilli()
	p := db.UpsertProjectRepoParams{
		ProjectID: prj.ID, Kind: string(KindMirror), RemoteUrl: prj.RepoUrl, GithubRepoID: prj.GithubRepoID,
		ErrorCode: &se.code, ErrorMessage: &msg, ErrorAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if row != nil {
		p.CreatedAt = row.CreatedAt
		if exists(s.dir(prj.ID)) {
			p.DefaultBranch, p.SyncedAt, p.SizeBytes = row.DefaultBranch, row.SyncedAt, row.SizeBytes
		}
	}
	if err := s.st.Q.UpsertProjectRepo(context.WithoutCancel(ctx), p); err != nil {
		slog.Warn("写入项目仓库记录", "project", prj.ID, "err", err)
	}
}

// tailRunes 保留 s 末尾至多 n 个字符。
func tailRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[len(r)-n:])
}

func randSuffix() string { return strings.ToLower(rand.Text()[:10]) }

// removeRepoDir 删除仓库目录：带托管标记（阶段 ③ 的正本）且 hostedOK 为假时拒绝并记错误。
// 先改名为 <dir>.trash-<rand> 再在后台删除，目录名立刻空出来；后台删除失败只记日志（下次启动清扫）。
func removeRepoDir(dir string, hostedOK bool) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if !hostedOK && exists(filepath.Join(dir, hostedMarker)) {
		slog.Error("拒绝删除托管仓库正本", "dir", dir)
		return fmt.Errorf("%s 带 %s 标记，是托管仓库正本，拒绝删除", dir, hostedMarker)
	}
	trash := dir + ".trash-" + randSuffix()
	if err := os.Rename(dir, trash); err != nil {
		return err
	}
	go func() {
		if err := os.RemoveAll(trash); err != nil {
			slog.Warn("删除仓库目录", "dir", trash, "err", err)
		}
	}()
	return nil
}

// dirSize 是目录下全部普通文件的大小之和；遍历期间消失的文件忽略。
func dirSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n, err
}

// freeBytes 是 dir 所在文件系统对非特权用户的可用空间。
func freeBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * st.Bsize, nil
}

// Run 启动时清扫仓库目录，之后每小时清理推送记录（过期未展开的记 missing、30 天前的删除）与托管仓库 30 天前
// 留存的 ref 旧值，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	s.sweep(ctx)
	tk := time.NewTicker(pushSweepEach)
	defer tk.Stop()
	for {
		s.sweepPushes(ctx)
		s.sweepSavedRefs(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}

// sweep 清掉上次留下的临时与回收目录（含托管仓库的 lfs/tmp）、项目已不存在的镜像（托管正本移到 orphaned/），
// 以及断电或 OOM 后的残留锁文件。
func (s *Service) sweep(ctx context.Context) {
	ents, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("读取仓库目录", "dir", s.cfg.Dir, "err", err)
		}
		return
	}
	for _, e := range ents {
		name := e.Name()
		path := filepath.Join(s.cfg.Dir, name)
		if strings.Contains(name, ".git.tmp-") || strings.Contains(name, ".git.trash-") {
			if id, _, _ := strings.Cut(name, ".git."); s.flying(id) {
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				slog.Warn("清理仓库临时目录", "dir", path, "err", err)
			}
			continue
		}
		m := repoDirRe.FindStringSubmatch(name)
		if m == nil || !e.IsDir() {
			continue
		}
		// 托管仓库 LFS 上传的临时文件（上次运行中断时留下的）
		if err := os.RemoveAll(filepath.Join(path, "lfs", "tmp")); err != nil {
			slog.Warn("清理 LFS 上传临时目录", "dir", path, "err", err)
		}
		_, err := s.st.Q.GetProject(ctx, m[1])
		switch {
		case errors.Is(err, sql.ErrNoRows):
			s.orphan(path, m[1])
		case err != nil:
			slog.Warn("读取项目", "project", m[1], "err", err)
		default:
			s.clearStaleLocks(path)
		}
	}
	if s.lfsCache != nil {
		s.lfsCache.prune(func(projectID string) bool {
			_, err := s.st.Q.GetProject(ctx, projectID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				slog.Warn("读取项目", "project", projectID, "err", err)
			}
			return !errors.Is(err, sql.ErrNoRows)
		})
	}
}

// orphan 处理项目行已不存在的仓库：托管正本移到 orphaned/（不删），镜像直接删除。
func (s *Service) orphan(dir, projectID string) {
	if !exists(filepath.Join(dir, hostedMarker)) {
		if err := removeRepoDir(dir, false); err != nil {
			slog.Warn("删除无主的项目仓库镜像", "dir", dir, "err", err)
		}
		return
	}
	dst := filepath.Join(s.cfg.Dir, "orphaned", projectID+".git-"+strconv.FormatInt(s.cfg.Now().Unix(), 10))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		slog.Error("找不到项目行的托管仓库无法移走", "dir", dir, "err", err)
		return
	}
	if err := os.Rename(dir, dst); err != nil {
		slog.Error("找不到项目行的托管仓库无法移走", "dir", dir, "err", err)
		return
	}
	slog.Error("托管仓库找不到项目行，已移到 orphaned/", "project", projectID, "to", dst)
}

// clearStaleLocks 删掉 staleLockAge 之前留下的 HEAD.lock、packed-refs.lock 与 refs/**/*.lock。
func (s *Service) clearStaleLocks(dir string) {
	cutoff := s.cfg.Now().Add(-staleLockAge)
	drop := func(p string) {
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			return
		}
		if err := os.Remove(p); err != nil {
			slog.Warn("删除残留锁文件", "path", p, "err", err)
			return
		}
		slog.Info("删除残留锁文件", "path", p)
	}
	drop(filepath.Join(dir, "HEAD.lock"))
	drop(filepath.Join(dir, "packed-refs.lock"))
	_ = filepath.WalkDir(filepath.Join(dir, "refs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".lock") {
			drop(p)
		}
		return nil
	})
}
