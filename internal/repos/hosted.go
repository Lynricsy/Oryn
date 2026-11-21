package repos

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Identity 是托管仓库初始提交的作者与提交者。
type Identity struct{ Name, Email string }

// ErrRepoExists 表示目标目录已存在，而且不是托管正本（没有 oryn-hosted 标记）。
var ErrRepoExists = &Error{Status: http.StatusConflict, Code: "repo_exists", Message: "项目已有仓库"}

var (
	errHostedMissing = &Error{Status: http.StatusConflict, Code: "repo_not_ready", Message: "托管仓库不存在"}
	objectIDRe       = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

const (
	zeroOID          = "0000000000000000000000000000000000000000"
	initialCommitMsg = "Initial commit"
	pushLockWait     = 60 * time.Second // 推送等每项目推送锁的上限
)

var errPushBusy = errors.New("推送锁等待超时")

// InitHosted 建托管裸仓库 <Dir>/<projectID>.git：HEAD → refs/heads/<base>；initialCommit 为真时在 base 上建一个空树提交。
// 目录已存在且带 oryn-hosted 标记时认领它（adopted=true），不重建也不动内容。只动文件系统与 git，不读写数据库：
// 库只有一条连接，在 Store.Tx 的回调里调用读库的函数会卡死，所以 project_repos 行由 RecordHosted 在事务外另写。
func (s *Service) InitHosted(ctx context.Context, projectID, base string, initialCommit bool, id Identity) (adopted bool, err error) {
	if s.reason != "" {
		return false, errUnavailable
	}
	if !ValidRef(base) {
		return false, invalid("baseBranch 不是合法的分支名：%q", base)
	}
	dir := s.dir(projectID)
	if adopted, err := hostedDirState(dir); adopted || err != nil {
		return adopted, err
	}
	tmp := dir + ".tmp-" + randSuffix()
	if err := s.buildHosted(ctx, tmp, projectID, base, initialCommit, id); err != nil {
		removeTmp(tmp)
		return false, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		removeTmp(tmp)
		if exists(dir) { // 并发的创建抢先建好了
			return false, ErrRepoExists
		}
		return false, fmt.Errorf("启用托管仓库目录: %w", err)
	}
	return false, nil
}

// hostedDirState 检查目标目录：不存在 → (false, nil)；带托管标记 → 认领 (true, nil)；其它 → ErrRepoExists。
func hostedDirState(dir string) (adopted bool, err error) {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if exists(filepath.Join(dir, hostedMarker)) {
		return true, nil
	}
	return false, ErrRepoExists
}

// buildHosted 在临时目录里建好完整的托管仓库（含标记文件），调用方随后改名到正式位置。
func (s *Service) buildHosted(ctx context.Context, tmp, projectID, base string, withCommit bool, id Identity) error {
	if err := s.gitOK(ctx, tmp, "init", "--bare", "-q", "--template="); err != nil {
		return err
	}
	if err := s.gitOK(ctx, tmp, "symbolic-ref", "HEAD", "refs/heads/"+base); err != nil {
		return err
	}
	// 基线分支不设保护（允许强推与删分支）；被删或被强推覆盖的旧值由推送记账留存到 refs/oryn/saved/（阶段 ③）。
	if err := s.gitOK(ctx, tmp, "config", "core.logAllRefUpdates", "true"); err != nil {
		return err
	}
	if withCommit {
		if err := s.commitEmptyTree(ctx, tmp, base, id); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, hostedMarker), []byte(projectID+"\n"), 0o600); err != nil {
		return fmt.Errorf("写托管标记: %w", err)
	}
	return nil
}

// commitEmptyTree 在 base 上建初始提交：空树、消息 Initial commit（不需要工作区）。
func (s *Service) commitEmptyTree(ctx context.Context, dir, base string, id Identity) error {
	tree, err := s.gitOID(ctx, gitRun{dir: dir, stdin: strings.NewReader("")}, "hash-object", "-t", "tree", "-w", "--stdin")
	if err != nil {
		return err
	}
	date := fmt.Sprintf("%d +0000", s.cfg.Now().Unix())
	env := []string{
		"GIT_AUTHOR_NAME=" + id.Name, "GIT_AUTHOR_EMAIL=" + id.Email, "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=" + id.Name, "GIT_COMMITTER_EMAIL=" + id.Email, "GIT_COMMITTER_DATE=" + date,
	}
	commit, err := s.gitOID(ctx, gitRun{dir: dir, env: env}, "commit-tree", tree, "-m", initialCommitMsg)
	if err != nil {
		return err
	}
	return s.gitOK(ctx, dir, "update-ref", "refs/heads/"+base, commit, zeroOID)
}

// gitOID 执行输出一个对象 id 的命令，要求退出码 0 且输出是 40 位十六进制。
func (s *Service) gitOID(ctx context.Context, o gitRun, args ...string) (string, error) {
	o.limit = 128
	res, err := s.git(ctx, o, args...)
	if err != nil {
		return "", err
	}
	if res.code != 0 {
		return "", fmt.Errorf("git %s: exit %d: %s", args[0], res.code, strings.TrimSpace(res.stderr))
	}
	oid := strings.TrimSpace(string(res.out))
	if !objectIDRe.MatchString(oid) {
		return "", fmt.Errorf("git %s: 输出不是对象 id：%q", args[0], oid)
	}
	return oid, nil
}

func removeTmp(tmp string) {
	if err := os.RemoveAll(tmp); err != nil {
		slog.Warn("清理托管仓库临时目录", "dir", tmp, "err", err)
	}
}

// SetHostedHead 把托管仓库 HEAD 指到 refs/heads/<base>（改基线分支时；分支可以还不存在）。只动 git，不读写数据库。
func (s *Service) SetHostedHead(ctx context.Context, projectID, base string) error {
	if s.reason != "" {
		return errUnavailable
	}
	if !ValidRef(base) {
		return invalid("baseBranch 不是合法的分支名：%q", base)
	}
	dir, err := s.hostedDir(projectID)
	if err != nil {
		return err
	}
	return s.gitOK(ctx, dir, "symbolic-ref", "HEAD", "refs/heads/"+base)
}

// RecordHosted 写 project_repos 行（kind=hosted、default_branch=base、size_bytes=目录大小）。只能在事务外调用。
func (s *Service) RecordHosted(ctx context.Context, projectID, base string) error {
	if !ValidRef(base) {
		return invalid("baseBranch 不是合法的分支名：%q", base)
	}
	dir, err := s.hostedDir(projectID)
	if err != nil {
		return err
	}
	size, err := dirSize(dir)
	if err != nil {
		return fmt.Errorf("统计托管仓库大小: %w", err)
	}
	now := s.cfg.Now().UnixMilli()
	return s.st.Q.UpsertProjectRepo(ctx, db.UpsertProjectRepoParams{
		ProjectID: projectID, Kind: string(KindHosted), DefaultBranch: &base, SizeBytes: size,
		CreatedAt: now, UpdatedAt: now,
	})
}

// hostedDir 返回项目的托管仓库目录：不存在 → repo_not_ready；存在但没有托管标记（是镜像）→ ErrRepoExists。
func (s *Service) hostedDir(projectID string) (string, error) {
	dir := s.dir(projectID)
	switch adopted, err := hostedDirState(dir); {
	case err != nil:
		return "", err
	case !adopted:
		return "", errHostedMissing
	}
	return dir, nil
}

// hostedStatus 是托管项目的仓库状态：目录在（带标记）→ ready，否则 absent；没有 syncedAt / lastError。
// 目录在而 project_repos 行缺失或基线不一致（创建或改基线时崩在两步之间）时先自愈再返回。
func (s *Service) hostedStatus(ctx context.Context, prj db.Project) (apigen.ProjectRepoStatus, error) {
	out := apigen.ProjectRepoStatus{
		Kind: KindHosted, Available: s.reason == "", State: StateAbsent,
		CloneURL: new(orbs.HostedCloneURL(s.cfg.PublicURL, prj.ID)),
	}
	if s.reason != "" {
		out.UnavailableReason = new(apigen.ProjectRepoStatusUnavailableReason(s.reason))
	}
	dir := s.dir(prj.ID)
	if adopted, err := hostedDirState(dir); err != nil || !adopted {
		return out, nil
	}
	row, err := s.healHosted(ctx, prj)
	if err != nil {
		return apigen.ProjectRepoStatus{}, err
	}
	out.State = StateReady
	out.BaseBranch = prj.BaseBranch
	if row != nil {
		out.DefaultBranch = row.DefaultBranch
		out.SizeBytes = new(row.SizeBytes)
	}
	if s.reason == "" {
		res, err := s.git(ctx, gitRun{dir: dir, read: true, limit: 512}, "for-each-ref", "--count=1", "--format=%(refname)", "refs/heads/")
		out.Empty = err == nil && res.code == 0 && len(strings.TrimSpace(string(res.out))) == 0
	}
	return out, nil
}

// healHosted 在托管仓库目录已在时补齐 HEAD 与 project_repos 行：行缺失、不是 hosted 或 default_branch 不等于项目
// baseBranch 时补跑 SetHostedHead 与 RecordHosted（失败只记日志）。返回（补齐后的）行。调用方已确认目录在。
func (s *Service) healHosted(ctx context.Context, prj db.Project) (*db.ProjectRepo, error) {
	row, err := s.repoRow(ctx, prj.ID)
	if err != nil {
		return nil, err
	}
	base := deref(prj.BaseBranch)
	if s.reason != "" || !ValidRef(base) ||
		(row != nil && row.Kind == string(KindHosted) && deref(row.DefaultBranch) == base) {
		return row, nil
	}
	if err := s.SetHostedHead(ctx, prj.ID, base); err != nil {
		slog.Warn("补齐托管仓库 HEAD", "project", prj.ID, "err", err)
		return row, nil
	}
	if err := s.RecordHosted(ctx, prj.ID, base); err != nil {
		slog.Warn("补写托管仓库记录", "project", prj.ID, "err", err)
		return row, nil
	}
	return s.repoRow(ctx, prj.ID)
}

// ServeHostedGit 服务托管仓库的一个 smart-HTTP 或 Git LFS 请求（调用方已鉴权）。pusher.OrbID 为空 = 主人本机。
// smart-HTTP 的错误一律 text/plain：git 把正文显示成 `remote: …`（remote-curl.c 的 show_http_message）；
// info/lfs/ 端点交给 serveHostedLFS（JSON 错误体，不拿推送锁、不做 ref 快照）。
func (s *Service) ServeHostedGit(w http.ResponseWriter, r *http.Request, projectID, endpoint string, pusher egress.HostedPusher) {
	if strings.HasPrefix(endpoint, "info/lfs/") {
		s.serveHostedLFS(w, r, projectID, endpoint, pusher)
		return
	}
	if s.reason != "" {
		smartError(w, http.StatusServiceUnavailable, "oryn: 控制面没有可用的 git")
		return
	}
	ctx := r.Context()
	prj, err := s.project(ctx, projectID)
	switch {
	case errors.Is(err, errNotFound) || (err == nil && !prj.Hosted):
		smartError(w, http.StatusNotFound, "oryn: 仓库不存在")
		return
	case err != nil:
		slog.Warn("托管仓库：读取项目", "project", projectID, "err", err)
		smartError(w, http.StatusInternalServerError, "oryn: 读取项目失败")
		return
	}
	ep := Endpoint(endpoint)
	if IsPush(r, ep) && prj.ArchivedAt != nil {
		smartError(w, http.StatusForbidden, "oryn: 项目已归档，仓库只读")
		return
	}
	dir := s.dir(projectID)
	if adopted, err := hostedDirState(dir); err != nil || !adopted {
		smartError(w, http.StatusNotFound, "oryn: 仓库不存在")
		return
	}
	// 创建或改基线时崩在两步之间：补齐 HEAD 与 project_repos 行（同 hostedStatus）
	if _, err := s.healHosted(ctx, prj); err != nil {
		slog.Warn("托管仓库：自愈", "project", projectID, "err", err)
	}
	if ep == EndpointReceivePack && r.Method == http.MethodPost {
		s.serveHostedPush(w, r, prj, dir, pusher)
		return
	}
	s.serveSmartHTTP(w, r, dir, ep, pusher.OrbID)
}

// pushLocks 是每项目的推送锁（容量 1 的 chan），零值可用。不用 sync.Mutex：等锁要能随请求取消，
// 否则一个迟迟不发请求体的推送会把后面的推送全卡住。
type pushLocks struct {
	m    sync.Map      // projectID → chan struct{}
	wait time.Duration // 等锁上限；0 = pushLockWait（测试可改）
}

// lock 等项目的推送锁：ctx 结束返回 ctx.Err()，等满上限返回 errPushBusy。
func (p *pushLocks) lock(ctx context.Context, projectID string) (release func(), err error) {
	v, ok := p.m.Load(projectID)
	if !ok {
		v, _ = p.m.LoadOrStore(projectID, make(chan struct{}, 1))
	}
	ch := v.(chan struct{})
	t := time.NewTimer(cmp.Or(p.wait, pushLockWait))
	defer t.Stop()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, errPushBusy
	}
}
