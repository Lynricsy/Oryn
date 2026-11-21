package repos

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	readTimeout     = 20 * time.Second // 读操作（同 orbdrt 的 gitTimeout）
	lsRemoteTimeout = 60 * time.Second
	cloneTimeout    = 15 * time.Minute // 首次克隆的 fetch
	fetchTimeout    = 5 * time.Minute  // 增量 fetch
	killGrace       = 10 * time.Second // SIGTERM 之后到 SIGKILL 的宽限
	stderrTail      = 4 << 10
	minGitMajor     = 2
	minGitMinor     = 31 // GIT_CONFIG_COUNT 2.31 起；--no-write-fetch-head 2.29；--end-of-options 2.24
)

var gitVersionRe = regexp.MustCompile(`^git version (\d+)\.(\d+)`)

// locateGit 找到 git 并检查版本；返回可执行文件路径与不可用原因（"" = 可用）。
func locateGit(name string) (path, reason string) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", "git_missing"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "version")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.Output()
	if err != nil {
		return "", "git_missing"
	}
	m := gitVersionRe.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return "", "git_missing"
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < minGitMajor || major == minGitMajor && minor < minGitMinor {
		return "", "git_too_old"
	}
	return p, ""
}

// gitRun 是一次 git 调用的参数。
type gitRun struct {
	dir     string    // --git-dir
	env     []string  // 追加到受控环境之后（凭据、代理）
	stdin   io.Reader // nil = 无输入
	limit   int       // stdout 上限；超出即终止进程并置 truncated
	timeout time.Duration
	read    bool // 读操作：GIT_OPTIONAL_LOCKS=0 与 core.bigFileThreshold
}

// gitResult 是一次 git 调用的结果；code 为退出码（被截断而终止时无意义）。
type gitResult struct {
	out       []byte
	truncated bool
	code      int
	stderr    string // 末尾 4 KiB
}

// baseArgs 是每次调用的固定前缀（argv[1:]）。
func baseArgs(dir string, read bool) []string {
	a := []string{
		"--git-dir", dir,
		"-c", "core.quotepath=off", "-c", "color.ui=false",
		"-c", "diff.noprefix=false", "-c", "diff.mnemonicPrefix=false",
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.http.allow=always",
		"-c", "core.hooksPath=/dev/null",
	}
	if read {
		a = append(a, "-c", "core.bigFileThreshold=16m")
	}
	return a
}

// baseEnv 是从空白开始的受控环境：不读系统与用户配置，不弹提示，凭据另由调用方追加。
func (s *Service) baseEnv(read bool) []string {
	home := filepath.Join(s.cfg.Dir, ".home")
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"GIT_HTTP_LOW_SPEED_LIMIT=1000",
		"GIT_HTTP_LOW_SPEED_TIME=60",
	}
	if read {
		env = append(env, "GIT_OPTIONAL_LOCKS=0")
	}
	return env
}

// git 执行 git；只有无法启动、超时或被取消（且非截断）时返回错误，非零退出码由调用方按 code 判断。
// 取消与超时先向整个进程组发 SIGTERM（git 靠信号处理清理 *.lock），killGrace 后仍未退出才 SIGKILL。
func (s *Service) git(ctx context.Context, o gitRun, args ...string) (gitResult, error) {
	if s.reason != "" {
		return gitResult{}, errUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, cmpDuration(o.timeout, readTimeout))
	defer cancel()
	argv := append(baseArgs(o.dir, o.read), args...)
	env := append(s.baseEnv(o.read), o.env...)
	if s.trace != nil {
		s.trace(append([]string{s.gitBin}, argv...), env)
	}
	cmd := exec.CommandContext(ctx, s.gitBin, argv...)
	cmd.Env = env
	cmd.Dir = s.cfg.Dir
	cmd.Stdin = o.stdin
	var tail tailBuffer
	cmd.Stderr = &tail
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var (
		killMu sync.Mutex
		hard   *time.Timer
		exited bool
	)
	terminate := func() error {
		killMu.Lock()
		defer killMu.Unlock()
		if exited {
			return nil
		}
		pid := cmd.Process.Pid
		if hard == nil {
			hard = time.AfterFunc(killGrace, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
		}
		return syscall.Kill(-pid, syscall.SIGTERM)
	}
	cmd.Cancel = terminate
	cmd.WaitDelay = killGrace + time.Second
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return gitResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return gitResult{}, err
	}
	limit := max(o.limit, 0)
	out, rerr := io.ReadAll(io.LimitReader(pipe, int64(limit)+1))
	res := gitResult{out: out}
	if len(out) > limit {
		res.out, res.truncated = out[:limit], true
		_ = terminate()
		_, _ = io.Copy(io.Discard, pipe)
	}
	werr := cmd.Wait()
	killMu.Lock()
	exited = true
	if hard != nil {
		hard.Stop()
	}
	killMu.Unlock()
	res.stderr = tail.String()
	if ctx.Err() != nil && !res.truncated {
		return res, fmt.Errorf("git %s: %w", args[0], ctx.Err())
	}
	if rerr != nil && !res.truncated {
		return res, rerr
	}
	var ee *exec.ExitError
	switch {
	case werr == nil:
	case errors.As(werr, &ee):
		res.code = ee.ExitCode()
		if res.code < 0 && !res.truncated {
			return res, fmt.Errorf("git %s: %w", args[0], werr)
		}
	case !res.truncated:
		return res, werr
	}
	return res, nil
}

func cmpDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// tailBuffer 只保留写入内容的末尾 stderrTail 字节。
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if over := len(t.b) - stderrTail; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(bytes.ToValidUTF8(t.b, []byte("?"))) }

// remote 是一次拉取要用的上游：地址、凭据与守卫。
type remote struct {
	url     string   // 拉取地址
	host    string   // 项目仓库的小写主机
	token   string   // "" = 匿名
	vouched bool     // github.com 或有令牌的主机：直连，沿用进程的代理变量
	env     []string // 追加给 ls-remote / fetch 的环境
}

// remote 按项目算出拉取地址、凭据与守卫；地址不是可放行的 https 仓库时返回 unsupported_url。
// 令牌只进环境变量（GIT_CONFIG_*），不进 argv、不进磁盘配置。
func (s *Service) remote(ctx context.Context, prj db.Project) (remote, error) {
	host, path, ok := orbs.HTTPSRepo(deref(prj.RepoUrl))
	if !ok {
		return remote{}, &syncError{code: "unsupported_url", msg: "只支持 https:// 仓库地址"}
	}
	rm := remote{url: *prj.RepoUrl, host: host}
	if host == "github.com" {
		if s.cfg.GitHubWebURL != "https://github.com" {
			rm.url = s.cfg.GitHubWebURL + "/" + path + ".git"
		}
		rm.vouched = true
		if s.gh != nil {
			if tok, err := s.gh.UserToken(ctx); err == nil {
				rm.token = tok
			}
		}
	} else if s.sec != nil {
		if tok, ok := s.sec.GitToken(host); ok && tok != "" {
			rm.token, rm.vouched = tok, true
		}
	}
	var kv [][2]string
	if rm.token != "" {
		u, err := urlOrigin(rm.url)
		if err != nil {
			return remote{}, &syncError{code: "unsupported_url", msg: "只支持 https:// 仓库地址"}
		}
		kv = append(kv,
			[2]string{"http." + u + "/.extraHeader", "Authorization: Basic " + basicAuth(rm.token)},
			[2]string{"http.followRedirects", "false"})
	}
	if rm.vouched {
		rm.env = append(rm.env, s.cfg.ProxyEnv...)
	} else {
		kv = append(kv, [2]string{"http.proxy", s.guard.URL()})
	}
	if len(kv) > 0 {
		rm.env = append(rm.env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(kv)))
		for i, p := range kv {
			rm.env = append(rm.env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, p[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, p[1]))
		}
	}
	return rm, nil
}

// urlOrigin 返回 scheme://host[:port]。
func urlOrigin(raw string) (string, error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return "", errors.New("地址没有 scheme")
	}
	host, _, _ := strings.Cut(rest, "/")
	if host == "" {
		return "", errors.New("地址没有主机")
	}
	return scheme + "://" + host, nil
}

// basicAuth 是 Basic 认证的凭据串（与出站代理 resolveGit 注入的头相同）。
func basicAuth(tok string) string {
	return base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
}

// redactSecrets 把 s 里的每个值、它的 base64 以及对应的 Basic 凭据串替换成 oryn-redacted。
func redactSecrets(s string, vals ...string) string {
	for _, v := range vals {
		if v == "" {
			continue
		}
		for _, x := range []string{basicAuth(v), base64.StdEncoding.EncodeToString([]byte(v)), v} {
			s = strings.ReplaceAll(s, x, "oryn-redacted")
		}
	}
	return s
}

// syncError 是一次克隆或拉取失败的分类结果。
type syncError struct{ code, msg string }

func (e *syncError) Error() string { return e.code + ": " + e.msg }

// classify 按 stderr（LC_ALL=C）把 git 失败分类成错误码。
func classify(stderr string, hasToken bool) string {
	has := func(subs ...string) bool {
		for _, x := range subs {
			if strings.Contains(stderr, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("Received HTTP code 403 from proxy after CONNECT", "CONNECT tunnel failed, response 403"):
		return "blocked_address"
	case has("terminal prompts disabled", "Authentication failed", "could not read Username"):
		if hasToken {
			return "auth_failed"
		}
		return "auth_required"
	case has("Repository not found", "returned error: 404"):
		return "not_found"
	case has("Could not resolve host", "Failed to connect", "Connection refused",
		"TLS connect error", "SSL certificate problem", "server certificate verification failed"):
		return "unreachable"
	}
	return "fetch_failed"
}
