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
	"sync/atomic"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Kind 是项目仓库的来源（也写进 project_repos.kind）。
type Kind = apigen.ProjectRepoStatusKind

const (
	KindMirror Kind = apigen.ProjectRepoStatusKindMirror
	KindNone   Kind = apigen.ProjectRepoStatusKindNone
	// KindHosted 是 Oryn 托管的正本（阶段 ③）：<Dir>/<projectID>.git 带 oryn-hosted 标记，不拉取、不按大小删除。
	KindHosted Kind = apigen.ProjectRepoStatusKindHosted
)

// State 是镜像的状态。
type State = apigen.ProjectRepoStatusState

const (
	StateAbsent  State = apigen.ProjectRepoStatusStateAbsent
	StateCloning State = apigen.ProjectRepoStatusStateCloning
	StateReady   State = apigen.ProjectRepoStatusStateReady
	StateError   State = apigen.ProjectRepoStatusStateError
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
	PublicURL    string                 // cfg.PublicURL：托管仓库克隆地址的前缀（阶段 ③）
	Ships        Shipper                // 托管仓库自动交付入口（*threads.Service）；nil = 不自动交付

	// MaxLFSCacheBytes 是镜像项目 LFS 对象缓存的总上限（MaxLFSCacheGB<<30）；0 = 2<<30。
	MaxLFSCacheBytes int64
	// LFSFlightTimeout 是一次取 LFS 对象（batch、下载、落盘）的总时限；0 = 15 分钟。
	LFSFlightTimeout time.Duration
	// Box 封存托管仓库 Webhook 的签名密钥（AAD = WebhookAAD(projectID)）；nil = 不能保存或使用签名密钥。
	Box *secrets.Box
	// WebhookTimeout 是一次 Webhook 投递的总时限；0 = 10 秒。
	WebhookTimeout time.Duration
}

// GitHub 是 github.com 拉取凭据、「提交 → PR → 线程」反查与提交作者头像（*github.Service 实现）。
type GitHub interface {
	UserToken(ctx context.Context) (string, error)
	CommitThreads
	// CommitAvatars 查提交作者与共同作者对上的 GitHub 账号与头像；仓库不在 github.com 时返回 (nil, nil)。
	CommitAvatars(ctx context.Context, projectID, sha string) ([]apigen.ProjectRepoAvatar, error)
}

// CommitThreads 按「提交所属 PR → 最早关联该 PR 的线程」反查提交的线程（GitHub 项目补 squash 合并）。
// ok=false 且 err=nil 表示确实没有线程（可以缓存）；err 非 nil 时不缓存（如身份未连接）。
type CommitThreads interface {
	CommitThread(ctx context.Context, projectID, sha string) (threadID string, ok bool, err error)
}

// Secrets 是其它主机的 git 令牌（*secrets.Vault 实现）：项目级优先，没有时用全局的。
type Secrets interface {
	GitToken(host string) (string, bool)
	// ProjectGitToken 是项目的 git/<host>/token；err 非 nil 时调用方拒绝，不回落到全局。
	ProjectGitToken(projectID, host string) (tok string, found bool, err error)
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
	errRepoHosted  = &Error{Status: http.StatusConflict, Code: "repo_hosted", Message: "托管仓库不需要同步"}
)

// Service 维护全部项目的裸镜像。
type Service struct {
	cfg    Config
	st     *store.Store
	sec    Secrets
	gh     GitHub
	gitBin string
	reason string // 不可用原因；"" = 可用
	guard  *egress.PublicProxy

	lfsCache *lfsCache  // 镜像项目的 LFS 对象缓存；git 不可用时为 nil
	lfsc     *lfsClient // 镜像项目的 LFS 客户端（传输、单飞、名额）；git 不可用时为 nil

	sem          chan struct{} // 同时最多两个克隆或拉取
	monitorEvery time.Duration // 拉取中复查磁盘与大小的间隔
	trace        func(argv, env []string)
	smart        smartSlots // 托管仓库 smart-HTTP 的 git 进程名额（阶段 ③）
	pushLocks    pushLocks  // 托管仓库每项目的推送锁（阶段 ③）
	lfsSlots     lfsSlots   // 托管仓库 LFS 对象传输的名额（阶段 ③）

	hooks webhookQueues // 托管仓库推送后 Webhook 的每项目投递队列（阶段 ③）

	treeWalkMax  int   // 找子项最后提交时至多读的提交数（测试可改）
	maxBlobBytes int64 // blob 接口的大小上限（测试可改）
	inlineLFSMax int64 // LFS 对象内联预览（取对象、进缓存）的上限，超过只能 download=1（测试可改）
	treeMu       sync.Mutex
	treeCache    map[string]map[string]apigen.ProjectRepoCommitSummary // commit + "\x00" + dir → 子项名 → 最后提交
	avatarMu     sync.Mutex
	avatarCache  map[string]avatarEntry // 项目 id + "\x00" + 提交 sha → GitHub 头像（见 avatars.go）

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	flights map[string]*flight

	// lim 是运行时覆盖的镜像大小上限与磁盘留空（SetLimits；nil = 用 cfg 里构造时的初值）。
	lim atomic.Pointer[repoLimits]
}

// repoLimits 是可在运行时换掉的两项字节数上限。
type repoLimits struct{ maxBytes, minFreeBytes int64 }

// SetLimits 在运行时换掉镜像大小上限、磁盘留空与 LFS 缓存上限（控制面运维参数，见 internal/opsconf；调用方已做缺省
// 换算）。下一次检查时生效；LFS 缓存调小时立刻淘汰。
func (s *Service) SetLimits(maxBytes, minFreeBytes, maxLFSCacheBytes int64) {
	s.lim.Store(&repoLimits{maxBytes: maxBytes, minFreeBytes: minFreeBytes})
	if s.lfsCache != nil {
		s.lfsCache.setMax(maxLFSCacheBytes)
	}
}

// maxBytes 返回当前生效的单个镜像大小上限。
func (s *Service) maxBytes() int64 {
	if p := s.lim.Load(); p != nil {
		return p.maxBytes
	}
	return s.cfg.MaxBytes
}

// minFreeBytes 返回当前生效的数据盘留空下限。
func (s *Service) minFreeBytes() int64 {
	if p := s.lim.Load(); p != nil {
		return p.minFreeBytes
	}
	return s.cfg.MinFreeBytes
}

// New 定位 git 并准备目录与守卫代理。git 不可用不是错误：Available 报告原因，仓库浏览停用。
func New(cfg Config, st *store.Store, sec Secrets, gh GitHub) (*Service, error) {
	cfg.MaxBytes = cmp.Or(cfg.MaxBytes, 5<<30)
	cfg.MinFreeBytes = cmp.Or(cfg.MinFreeBytes, 2<<30)
	cfg.MaxLFSCacheBytes = cmp.Or(cfg.MaxLFSCacheBytes, 2<<30)
	cfg.LFSFlightTimeout = cmp.Or(cfg.LFSFlightTimeout, 15*time.Minute)
	cfg.StaleAfter = cmp.Or(cfg.StaleAfter, 60*time.Second)
	cfg.GitHubWebURL = strings.TrimRight(cmp.Or(cfg.GitHubWebURL, "https://github.com"), "/")
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		cfg: cfg, st: st, sec: sec, gh: gh,
		sem: make(chan struct{}, 2), monitorEvery: 5 * time.Second,
		treeWalkMax: treeWalkMax, maxBlobBytes: blobMax, inlineLFSMax: lfsInlineMax,
		treeCache: map[string]map[string]apigen.ProjectRepoCommitSummary{}, ctx: ctx, cancel: cancel, flights: map[string]*flight{},
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
	if s.lfsc, err = newLFSClient(cfg.ProxyEnv, guard.URL()); err != nil {
		cancel()
		_ = guard.Close()
		return nil, err
	}
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
	if s.lfsc != nil {
		s.lfsc.close()
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

// Status 返回项目仓库的状态；未归档项目在需要时顺带在后台克隆或拉取（不阻塞）。托管项目见 hostedStatus。
func (s *Service) Status(ctx context.Context, projectID string) (apigen.ProjectRepoStatus, error) {
	prj, err := s.project(ctx, projectID)
	if err != nil {
		return apigen.ProjectRepoStatus{}, err
	}
	if prj.Hosted {
		return s.hostedStatus(ctx, prj)
	}
	row, err := s.ensure(ctx, prj, prj.ArchivedAt == nil)
	if err != nil {
		return apigen.ProjectRepoStatus{}, err
	}
	return s.status(ctx, prj, row), nil
}

// Sync 立即在后台克隆或拉取（已归档项目也可以），返回开始后的状态。
func (s *Service) Sync(ctx context.Context, projectID string) (apigen.ProjectRepoStatus, error) {
	prj, err := s.project(ctx, projectID)
	if err != nil {
		return apigen.ProjectRepoStatus{}, err
	}
	if prj.Hosted {
		return apigen.ProjectRepoStatus{}, errRepoHosted
	}
	if s.reason != "" {
		return apigen.ProjectRepoStatus{}, errUnavailable
	}
	if !hasRepo(prj) {
		return apigen.ProjectRepoStatus{}, errNoRepo
	}
	row, err := s.ensure(ctx, prj, false)
	if err != nil {
		return apigen.ProjectRepoStatus{}, err
	}
	s.start(projectID, true)
	return s.status(ctx, prj, row), nil
}

// Kick 请求一次后台拉取（推送事件等）；进行中时跑完再来一轮。托管仓库没有上游，不排队。
func (s *Service) Kick(projectID string) {
	if s.reason == "" && !exists(filepath.Join(s.dir(projectID), hostedMarker)) {
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
	if s.lfsc != nil {
		s.lfsc.forget(projectID)
	}
}

// dropLFSCache 删掉项目的 LFS 对象缓存（镜像被删除时一并清掉）。
func (s *Service) dropLFSCache(projectID string) {
	if s.lfsCache != nil {
		s.lfsCache.removeProject(projectID)
	}
}

// ProjectChanged 在项目的仓库地址变化后调用：地址指向别的仓库时删除镜像（GitHub 改名跟随除外）。托管项目直接返回。
func (s *Service) ProjectChanged(ctx context.Context, projectID string) {
	prj, err := s.project(ctx, projectID)
	if err != nil {
		if !errors.Is(err, errNotFound) {
			slog.Warn("读取项目", "project", projectID, "err", err)
		}
		return
	}
	if prj.Hosted {
		return
	}
	if _, err := s.ensure(ctx, prj, false); err != nil {
		slog.Warn("核对项目仓库地址", "project", projectID, "err", err)
	}
	s.changed(projectID)
}

// status 由项目、镜像行、目录与飞行状态算出对外状态。
func (s *Service) status(ctx context.Context, prj db.Project, row *db.ProjectRepo) apigen.ProjectRepoStatus {
	out := apigen.ProjectRepoStatus{Kind: KindMirror, Available: s.reason == "", State: StateAbsent}
	if s.reason != "" {
		out.UnavailableReason = new(apigen.ProjectRepoStatusUnavailableReason(s.reason))
	}
	if !hasRepo(prj) {
		out.Kind = KindNone
		return out
	}
	out.CloneURL = prj.RepoUrl
	mirror := exists(s.dir(prj.ID))
	flying := s.flying(prj.ID)
	if row != nil {
		out.DefaultBranch = row.DefaultBranch
		out.SyncedAt = row.SyncedAt
		out.SizeBytes = new(row.SizeBytes)
		if row.ErrorCode != nil {
			out.LastError = &apigen.ProjectRepoError{
				Code: apigen.ProjectRepoErrorCode(*row.ErrorCode), Message: deref(row.ErrorMessage), At: deref(row.ErrorAt),
			}
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
	if b := s.baseBranch(ctx, prj, row, dir); b != "" {
		out.BaseBranch = &b
	}
	return out
}

// baseBranch 是项目的 baseBranch（在镜像里存在时），否则镜像的默认分支；都没有时为空。
func (s *Service) baseBranch(ctx context.Context, prj db.Project, row *db.ProjectRepo, dir string) string {
	if b := deref(prj.BaseBranch); b != "" && ValidRef(b) && s.hasBranch(ctx, dir, b) {
		return b
	}
	if row != nil {
		return deref(row.DefaultBranch)
	}
	return ""
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
