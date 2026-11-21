// Package repos 是项目仓库的控制面裸仓库（M24 阶段 ②）：orynd 在 <data_dir>/repos/<projectId>.git 为每个有 https
// 仓库地址的项目维护完整裸镜像（全部分支与标签），git 由控制面自己执行，凭据只经环境变量交给 git，未担保主机经
// egress.PublicProxy 只连公网地址。
package repos

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Kind 是项目仓库的来源；阶段 ③ 加 KindHosted。
type Kind string

const (
	KindMirror Kind = "mirror"
	KindNone   Kind = "none"
)

// State 是镜像的状态。
type State string

const (
	StateAbsent  State = "absent"
	StateCloning State = "cloning"
	StateReady   State = "ready"
	StateError   State = "error"
)

// Config 是 Service 的配置。
type Config struct {
	Dir          string                 // cfg.ReposDir()
	Git          string                 // cfg.Repos.Git；空 = PATH 里的 git
	GitHubWebURL string                 // cfg.GitHub.WebURL：github.com 仓库实际拉取的源
	MaxBytes     int64                  // MaxMirrorGB<<30；0 = 5<<30
	MinFreeBytes int64                  // MinFreeGB<<30；0 = 2<<30
	StaleAfter   time.Duration          // 0 = 60s
	Now          func() time.Time       // nil = time.Now
	ProxyEnv     []string               // 担保主机拉取时追加的代理变量（KEY=VALUE）；nil = 不追加
	Changed      func(projectID string) // 状态变化后广播；nil = 不广播

	// MaxLFSCacheBytes 是镜像项目 LFS 对象缓存的总上限（MaxLFSCacheGB<<30）；0 = 2<<30。
	MaxLFSCacheBytes int64
}

// GitHub 是 github.com 拉取凭据（*github.Service 实现）。
type GitHub interface {
	UserToken(ctx context.Context) (string, error)
}

// Secrets 是其它主机的 git 令牌（*secrets.Vault 实现）。
type Secrets interface {
	GitToken(host string) (string, bool)
}

// Error 是领域错误，api 按 Status / Code 原样输出。
type Error struct {
	Status        int
	Code, Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

var (
	errNotFound    = &Error{Status: http.StatusNotFound, Code: "not_found", Message: "项目不存在"}
	errUnavailable = &Error{Status: http.StatusServiceUnavailable, Code: "repo_unavailable", Message: "控制面没有可用的 git"}
	errNoRepo      = &Error{Status: http.StatusConflict, Code: "no_repo", Message: "项目没有仓库地址"}
)

// RepoStatus 是项目仓库的状态（与 OpenAPI 的 ProjectRepoStatus 同形；步骤 2.5 改为直接返回生成类型）。
type RepoStatus struct {
	Kind              Kind
	State             State
	Available         bool
	UnavailableReason string // "" | git_missing | git_too_old
	Syncing           bool   // state=ready 时后台拉取进行中
	Empty             bool   // 镜像就绪但没有任何分支
	CloneURL          string
	DefaultBranch     string
	BaseBranch        string // 项目 baseBranch 在镜像里存在时取它，否则 DefaultBranch
	SyncedAt          *int64
	SizeBytes         int64
	LastError         *RepoError
}

// RepoError 是最近一次失败的拉取。
type RepoError struct {
	Code, Message string
	At            int64
}

// Service 维护全部项目的裸镜像。
type Service struct {
	cfg    Config
	st     *store.Store
	sec    Secrets
	gh     GitHub
	gitBin string
	reason string // 不可用原因；"" = 可用
	guard  *egress.PublicProxy

	lfsCache *lfsCache // 镜像项目的 LFS 对象缓存；git 不可用时为 nil

	sem          chan struct{} // 同时最多两个克隆或拉取
	monitorEvery time.Duration // 拉取中复查磁盘与大小的间隔
	trace        func(argv, env []string)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	flights map[string]*flight
}

// New 定位 git 并准备目录与守卫代理。git 不可用不是错误：Available 报告原因，仓库浏览停用。
func New(cfg Config, st *store.Store, sec Secrets, gh GitHub) (*Service, error) {
	cfg.MaxBytes = cmp.Or(cfg.MaxBytes, 5<<30)
	cfg.MinFreeBytes = cmp.Or(cfg.MinFreeBytes, 2<<30)
	cfg.MaxLFSCacheBytes = cmp.Or(cfg.MaxLFSCacheBytes, 2<<30)
	cfg.StaleAfter = cmp.Or(cfg.StaleAfter, 60*time.Second)
	cfg.GitHubWebURL = strings.TrimRight(cmp.Or(cfg.GitHubWebURL, "https://github.com"), "/")
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		cfg: cfg, st: st, sec: sec, gh: gh,
		sem: make(chan struct{}, 2), monitorEvery: 5 * time.Second,
		ctx: ctx, cancel: cancel, flights: map[string]*flight{},
	}
	s.gitBin, s.reason = locateGit(cmp.Or(cfg.Git, "git"))
	if s.reason != "" {
		slog.Warn("控制面没有可用的 git，仓库浏览停用", "reason", s.reason)
		return s, nil
	}
	if err := os.MkdirAll(filepath.Join(cfg.Dir, ".home", ".config"), 0o700); err != nil {
		cancel()
		return nil, fmt.Errorf("创建仓库目录: %w", err)
	}
	lc, err := openLFSCache(filepath.Join(cfg.Dir, ".lfs-cache"), cfg.MaxLFSCacheBytes)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("打开 LFS 对象缓存: %w", err)
	}
	s.lfsCache = lc
	guard, err := egress.ListenPublicProxy()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("启动公网守卫代理: %w", err)
	}
	s.guard = guard
	return s, nil
}

// Close 取消进行中的克隆与拉取并等它们退出，然后关闭守卫代理。
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
	if s.lfsCache != nil {
		s.lfsCache.bg.Wait()
	}
	if s.guard != nil {
		if err := s.guard.Close(); err != nil {
			slog.Warn("关闭公网守卫代理", "err", err)
		}
	}
}

// Available 报告控制面是否有可用的 git（≥2.31）；不可用时 reason 为 git_missing 或 git_too_old。
func (s *Service) Available() (ok bool, reason string) { return s.reason == "", s.reason }

var refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,200}$`)

// ValidRef 报告 ref 是否是可接受的分支或标签名（不含 refs/ 前缀）。
func ValidRef(ref string) bool {
	return refRe.MatchString(ref) && !strings.Contains(ref, "..") && !strings.Contains(ref, "//") &&
		!strings.HasSuffix(ref, "/") && !strings.HasSuffix(ref, ".lock")
}

// dir 是项目镜像的目录。
func (s *Service) dir(projectID string) string { return filepath.Join(s.cfg.Dir, projectID+".git") }

func (s *Service) changed(projectID string) {
	if s.cfg.Changed != nil {
		s.cfg.Changed(projectID)
	}
}

func (s *Service) project(ctx context.Context, projectID string) (db.Project, error) {
	prj, err := s.st.Q.GetProject(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return prj, errNotFound
	}
	return prj, err
}

func (s *Service) repoRow(ctx context.Context, projectID string) (*db.ProjectRepo, error) {
	row, err := s.st.Q.GetProjectRepo(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func hasRepo(prj db.Project) bool { return prj.RepoUrl != nil && *prj.RepoUrl != "" }

// Status 返回项目仓库的状态；未归档项目在需要时顺带在后台克隆或拉取（不阻塞）。
func (s *Service) Status(ctx context.Context, projectID string) (RepoStatus, error) {
	prj, err := s.project(ctx, projectID)
	if err != nil {
		return RepoStatus{}, err
	}
	row, err := s.ensure(ctx, prj, prj.ArchivedAt == nil)
	if err != nil {
		return RepoStatus{}, err
	}
	return s.status(ctx, prj, row), nil
}

// Sync 立即在后台克隆或拉取（已归档项目也可以），返回开始后的状态。
func (s *Service) Sync(ctx context.Context, projectID string) (RepoStatus, error) {
	prj, err := s.project(ctx, projectID)
	if err != nil {
		return RepoStatus{}, err
	}
	if s.reason != "" {
		return RepoStatus{}, errUnavailable
	}
	if !hasRepo(prj) {
		return RepoStatus{}, errNoRepo
	}
	row, err := s.ensure(ctx, prj, false)
	if err != nil {
		return RepoStatus{}, err
	}
	s.start(projectID, true)
	return s.status(ctx, prj, row), nil
}

// Kick 请求一次后台拉取（推送事件等）；进行中时跑完再来一轮。
func (s *Service) Kick(projectID string) {
	if s.reason == "" {
		s.start(projectID, true)
	}
}

// Remove 删除项目的镜像（项目删除时调用；行由外键级联，这里也显式删除）。失败只记日志。
func (s *Service) Remove(projectID string) {
	s.stopFlight(projectID)
	if err := s.st.Q.DeleteProjectRepo(context.Background(), projectID); err != nil {
		slog.Warn("删除项目仓库记录", "project", projectID, "err", err)
	}
	if err := removeRepoDir(s.dir(projectID), true); err != nil {
		slog.Warn("删除项目仓库目录", "project", projectID, "err", err)
	}
	s.dropLFSCache(projectID)
}

// dropLFSCache 删掉项目的 LFS 对象缓存（镜像被删除时一并清掉）。
func (s *Service) dropLFSCache(projectID string) {
	if s.lfsCache != nil {
		s.lfsCache.removeProject(projectID)
	}
}

// ProjectChanged 在项目的仓库地址变化后调用：地址指向别的仓库时删除镜像（GitHub 改名跟随除外）。
func (s *Service) ProjectChanged(ctx context.Context, projectID string) {
	prj, err := s.project(ctx, projectID)
	if err != nil {
		if !errors.Is(err, errNotFound) {
			slog.Warn("读取项目", "project", projectID, "err", err)
		}
		return
	}
	if _, err := s.ensure(ctx, prj, false); err != nil {
		slog.Warn("核对项目仓库地址", "project", projectID, "err", err)
	}
	s.changed(projectID)
}

// status 由项目、镜像行、目录与飞行状态算出对外状态。
func (s *Service) status(ctx context.Context, prj db.Project, row *db.ProjectRepo) RepoStatus {
	out := RepoStatus{Kind: KindMirror, Available: s.reason == "", UnavailableReason: s.reason, State: StateAbsent}
	if !hasRepo(prj) {
		out.Kind = KindNone
		return out
	}
	out.CloneURL = *prj.RepoUrl
	mirror := exists(s.dir(prj.ID))
	flying := s.flying(prj.ID)
	if row != nil {
		out.DefaultBranch = deref(row.DefaultBranch)
		out.SyncedAt = row.SyncedAt
		out.SizeBytes = row.SizeBytes
		if row.ErrorCode != nil {
			out.LastError = &RepoError{Code: *row.ErrorCode, Message: deref(row.ErrorMessage), At: deref(row.ErrorAt)}
		}
	}
	switch {
	case mirror:
		out.State, out.Syncing = StateReady, flying
	case flying:
		out.State = StateCloning
	case out.LastError != nil:
		out.State = StateError
	}
	if out.State != StateReady {
		return out
	}
	out.BaseBranch = out.DefaultBranch
	if s.reason != "" {
		return out
	}
	dir := s.dir(prj.ID)
	res, err := s.git(ctx, gitRun{dir: dir, read: true, limit: 512}, "for-each-ref", "--count=1", "--format=%(refname)", "refs/heads/")
	out.Empty = err == nil && res.code == 0 && len(strings.TrimSpace(string(res.out))) == 0
	if b := deref(prj.BaseBranch); b != "" && ValidRef(b) && s.hasBranch(ctx, dir, b) {
		out.BaseBranch = b
	}
	return out
}

// hasBranch 报告镜像里是否有 refs/heads/<b>。
func (s *Service) hasBranch(ctx context.Context, dir, b string) bool {
	res, err := s.git(ctx, gitRun{dir: dir, read: true, limit: 512}, "show-ref", "--verify", "--quiet", "--end-of-options", "refs/heads/"+b)
	return err == nil && res.code == 0
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
