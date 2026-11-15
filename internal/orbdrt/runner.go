package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// runner 形态（M11b 计划 7.2）：用户自己的机器以 `orbd run --runner --dir <目录> …` 主动接入，线程直接跑在
// 声明的目录里。runner 完全信任控制面（能以本用户身份执行任意命令），但控制面的密钥不来这里：不开 egress、
// 不写 omp 扩展与技能、不改 git 配置；本机 API 只看得到线程的工作目录（X-Oryn-Workdir）。

// RunnerTokenPrefix 是 runner 令牌的前缀。
const RunnerTokenPrefix = "ort_"

// WorkdirHeader 是控制面转给 runner 本机 API 的线程工作目录（runner 形态必填）。
const WorkdirHeader = "X-Oryn-Workdir"

// ctl 错误码（M11b）。
const (
	CodeNotSupported  = "not_supported"
	CodeDirNotServed  = "dir_not_served"
	CodeWorktreeDirty = "worktree_dirty"
)

// whoamiTimeout 是启动时 GET /orb/v1/whoami 的上限。
const whoamiTimeout = 20 * time.Second

// RunnerConfig 是 runner 形态的配置（`orbd run --runner` 的参数）。
type RunnerConfig struct {
	// Dirs 是 --dir 声明的目录；New 解析符号链接后存绝对路径（必须存在且是目录）。
	Dirs []string
	// Terminal 为真时上报 terminal 特性（终端面板可用）。
	Terminal bool
}

// RunnerOptions 是 `orbd run --runner` 的命令行参数。
type RunnerOptions struct {
	Dirs     []string
	Terminal bool
	// TokenFile 是 ORYN_RUNNER_TOKEN 缺省时读取令牌的文件（空 = $HOME/.config/oryn/runner.token）。
	TokenFile string
}

// runnerUnsupported 是 runner 形态回 not_supported 的 ctl 方法（runner 没有 setup / 快照 / 受管服务 / 桌面，
// 也不由控制面关机）。
var runnerUnsupported = map[string]bool{
	string(apigen.MethodSetupRun):          true,
	string(apigen.MethodResumeRun):         true,
	string(apigen.MethodProjectScriptsGet): true,
	string(apigen.MethodProjectScriptsSet): true,
	string(apigen.MethodSnapshotPrepare):   true,
	string(apigen.MethodShutdown):          true,
	string(apigen.MethodPortalConfigure):   true,
	string(apigen.MethodProfileMode):       true,
	string(apigen.MethodOmpHomeApply):      true,
}

// RunnerConfigFromEnv 读取 runner 形态的配置：ORYN_URL 必填；令牌取 ORYN_RUNNER_TOKEN，否则读令牌文件，
// 必须以 ort_ 开头。orb id 不在这里取得（New 经 whoami）。
func RunnerConfigFromEnv(version string, opts RunnerOptions) (Config, error) {
	cfg := Config{
		URL:      os.Getenv("ORYN_URL"),
		AgentBin: os.Getenv("ORYN_AGENT_BIN"),
		Version:  version,
		OrbCAPEM: os.Getenv("ORYN_ORB_CA_PEM"),
		Runner:   &RunnerConfig{Dirs: opts.Dirs, Terminal: opts.Terminal},
	}
	if cfg.URL == "" {
		return cfg, errors.New("missing environment: ORYN_URL")
	}
	if len(opts.Dirs) == 0 {
		return cfg, errors.New("at least one --dir is required")
	}
	tok, err := runnerToken(opts.TokenFile)
	if err != nil {
		return cfg, err
	}
	cfg.Token = tok
	if p := os.Getenv("ORYN_ORBD_REQUEST_LOG"); p != "" {
		cfg.OnLocalRequest = requestLogger(p)
	}
	return cfg, nil
}

// runnerToken 取 runner 令牌：ORYN_RUNNER_TOKEN 优先，否则读 tokenFile（缺省 $HOME/.config/oryn/runner.token）。
func runnerToken(tokenFile string) (string, error) {
	tok := strings.TrimSpace(os.Getenv("ORYN_RUNNER_TOKEN"))
	if tok == "" {
		if tokenFile == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve $HOME: %w", err)
			}
			tokenFile = filepath.Join(home, ".config", "oryn", "runner.token")
		}
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("no runner token: set ORYN_RUNNER_TOKEN or write it to %s (%w)", tokenFile, err)
		}
		tok = strings.TrimSpace(string(b))
	}
	if !strings.HasPrefix(tok, RunnerTokenPrefix) {
		return "", fmt.Errorf("the runner token must start with %q", RunnerTokenPrefix)
	}
	return tok, nil
}

// plainRemoteURL 报告 u 是否为明文（http / ws）且主机不是回环地址。
func plainRemoteURL(u string) bool {
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "ws") {
		return false
	}
	host := pu.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// whoami 以 runner 令牌请求 GET <url>/orb/v1/whoami 得到自己的 orb id；hc 与 link 拨号是同一个
// （钉死 ORYN_ORB_CA_PEM 的）client，nil = http.DefaultClient。
func whoami(ctx context.Context, hc *http.Client, base, token string) (apigen.OrbWhoami, error) {
	var who apigen.OrbWhoami
	switch {
	case strings.HasPrefix(base, "ws://"):
		base = "http://" + strings.TrimPrefix(base, "ws://")
	case strings.HasPrefix(base, "wss://"):
		base = "https://" + strings.TrimPrefix(base, "wss://")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/orb/v1/whoami", nil)
	if err != nil {
		return who, fmt.Errorf("whoami: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return who, fmt.Errorf("whoami: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return who, fmt.Errorf("whoami: the runner token was rejected (unknown or revoked): %w", link.ErrUnauthorized)
	case resp.StatusCode != http.StatusOK:
		return who, fmt.Errorf("whoami: unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &who); err != nil {
		return who, fmt.Errorf("whoami: decode: %w", err)
	}
	if who.OrbID == "" {
		return who, errors.New("whoami: empty orbId")
	}
	if who.Kind != apigen.OrbKindRunner {
		return who, fmt.Errorf("whoami: the token belongs to a %q, not a runner", who.Kind)
	}
	return who, nil
}

// runnerState 是 runner 形态的运行时状态。
type runnerState struct {
	dirs     []string // 声明的目录（EvalSymlinks 后的绝对路径，去重，按声明顺序）
	terminal bool
	lock     *os.File
	unlockMu sync.Once

	// wtMu 串行化 worktree.add / worktree.remove（目录与分支名的探测和创建之间不能被插队）。
	wtMu sync.Mutex
	wt   *worktreeStore
}

// newRunnerState 校验并解析声明的目录、对 $HOME/.oryn/orbd/runner.lock 取 flock（同一用户第二个实例报错），
// 读取 worktree 登记。
func newRunnerState(home string, cfg *RunnerConfig) (*runnerState, error) {
	dirs, err := resolveRunnerDirs(cfg.Dirs)
	if err != nil {
		return nil, err
	}
	lock, err := lockRunner(filepath.Join(home, ".oryn", "orbd", "runner.lock"))
	if err != nil {
		return nil, err
	}
	return &runnerState{
		dirs:     dirs,
		terminal: cfg.Terminal,
		lock:     lock,
		wt:       loadWorktrees(filepath.Join(home, ".oryn", "orbd", "worktrees.json")),
	}, nil
}

func (s *runnerState) unlock() {
	s.unlockMu.Do(func() { _ = s.lock.Close() }) // 关闭即释放 flock
}

// resolveRunnerDirs 要求每个目录存在且是目录，返回 EvalSymlinks 之后的绝对路径（去重）。
func resolveRunnerDirs(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, errors.New("runner: at least one --dir is required")
	}
	var out []string
	seen := map[string]bool{}
	for _, d := range in {
		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, fmt.Errorf("--dir %s: %w", d, err)
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("--dir %s: %w", d, err)
		}
		st, err := os.Stat(real)
		if err != nil {
			return nil, fmt.Errorf("--dir %s: %w", d, err)
		}
		if !st.IsDir() {
			return nil, fmt.Errorf("--dir %s: not a directory", d)
		}
		if !seen[real] {
			seen[real] = true
			out = append(out, real)
		}
	}
	return out, nil
}

// lockRunner 以 flock(LOCK_EX|LOCK_NB) 锁住 path；已被另一个实例锁住时报错。
func lockRunner(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another orbd runner of this user is already running (%s is locked)", path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}

// allowedRoots 返回 runner 可服务的根：声明的目录与已登记的 worktree。
func (r *Runtime) allowedRoots() []string {
	return append(append([]string(nil), r.runner.dirs...), r.runner.wt.dirs()...)
}

// allowed 报告绝对路径 p（解析已存在部分的符号链接后）是否等于或位于某个声明的目录或已登记的 worktree 之下，
// 并返回解析后的路径。orb 形态恒为 false。
func (r *Runtime) allowed(p string) (string, bool) {
	if r.runner == nil || !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
		return "", false
	}
	real, err := evalExisting(filepath.Clean(p))
	if err != nil {
		return "", false
	}
	for _, root := range r.allowedRoots() {
		if within(real, root) {
			return real, true
		}
	}
	return "", false
}

// scope 返回一次本机 API 请求可见的范围：root 是路径范围的根（已展开符号链接，本次请求内固定不变，见 relTo），
// workdir 是缺省工作目录（git 面板的缺省根、不可删除的目录）。orb 形态为 (真实 $HOME, 最近一次 setup 的
// 工作目录)；runner 形态取请求头 X-Oryn-Workdir（缺失 400 workdir_required，不在声明的目录或已登记的
// worktree 之内 403 dir_not_served），两者都是 allowed 解析出的真实路径：请求进行中工作目录被换成指向别处
// 的链接，也不会把范围跟着带过去。
func (r *Runtime) scope(req *http.Request) (root, workdir string, err error) {
	if r.runner == nil {
		home, err := filepath.EvalSymlinks(r.home)
		if err != nil {
			return "", "", err
		}
		return home, r.currentWorkdir(), nil
	}
	wd := req.Header.Get(WorkdirHeader)
	if wd == "" {
		return "", "", errf(http.StatusBadRequest, "workdir_required", "%s is required on a runner", WorkdirHeader)
	}
	real, ok := r.allowed(wd)
	if !ok {
		return "", "", errf(http.StatusForbidden, CodeDirNotServed, "%q is not inside a directory served by this runner", wd)
	}
	return real, real, nil
}

// threadsDir 是会话目录的父目录（$HOME/.oryn/threads）。
func (r *Runtime) threadsDir() string { return filepath.Join(r.home, ".oryn", "threads") }

// underThreads 解析路径并要求它（跟随已存在部分的符号链接后）严格位于 $HOME/.oryn/threads 之下；
// runner 形态的 sessionDir、fs.copyTree、fs.move 只能动这里。
func (r *Runtime) underThreads(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("empty path")
	}
	abs := r.resolvePath(p)
	real, err := evalExisting(abs)
	if err != nil {
		return "", err
	}
	base, err := filepath.EvalSymlinks(r.threadsDir())
	if err != nil {
		return "", err
	}
	if !within(real, base) || real == base {
		return "", fmt.Errorf("path %q is outside $HOME/.oryn/threads", p)
	}
	return abs, nil
}

// runnerFeatures 是 runner 的 hello 特性：fs、attachments、metrics；PATH 里有 git 时加 git，--terminal 时加 terminal。
func (r *Runtime) runnerFeatures() []string {
	features := []string{"fs", "attachments", "metrics"}
	if _, err := r.bins.look("git"); err == nil {
		features = append(features, "git")
	}
	if r.runner.terminal {
		features = append(features, "terminal")
	}
	return features
}

// runnerInfo 是 hello 的 runner 附加信息：主机名与声明的目录（git = 目录本身含 .git）。
func (r *Runtime) runnerInfo() *apigen.LinkRunnerInfo {
	host, _ := os.Hostname()
	dirs := make([]apigen.RunnerDir, 0, len(r.runner.dirs))
	for _, d := range r.runner.dirs {
		_, err := os.Lstat(filepath.Join(d, ".git"))
		dirs = append(dirs, apigen.RunnerDir{Path: d, Name: filepath.Base(d), Git: err == nil})
	}
	return &apigen.LinkRunnerInfo{Hostname: host, Dirs: dirs}
}
