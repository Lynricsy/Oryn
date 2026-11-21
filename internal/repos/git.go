package repos

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// gitProc 是一个已启动的 git 进程：调用方读 stdout，读完（或 stop 之后）必须调用 finish 收尾。
type gitProc struct {
	name   string
	cmd    *exec.Cmd
	stdout io.ReadCloser
	tail   tailBuffer
	ctx    context.Context
	cancel context.CancelFunc

	killMu sync.Mutex
	hard   *time.Timer
	exited bool
}

// startGit 启动 git。取消与超时先向整个进程组发 SIGTERM（git 靠信号处理清理 *.lock），killGrace 后仍未退出才 SIGKILL。
func (s *Service) startGit(ctx context.Context, o gitRun, args ...string) (*gitProc, error) {
	if s.reason != "" {
		return nil, errUnavailable
	}
	var cancel context.CancelFunc
	if o.timeout == noTimeout {
		ctx, cancel = context.WithCancel(ctx)
	} else {
		ctx, cancel = context.WithTimeout(ctx, cmpDuration(o.timeout, readTimeout))
	}
	argv := append(baseArgs(o.dir, o.read), args...)
	env := append(s.baseEnv(o.read), o.env...)
	if s.trace != nil {
		s.trace(append([]string{s.gitBin}, argv...), env)
	}
	cmd := exec.CommandContext(ctx, s.gitBin, argv...)
	p := &gitProc{name: subcommand(args), cmd: cmd, ctx: ctx, cancel: cancel}
	cmd.Env = env
	cmd.Dir = s.cfg.Dir
	cmd.Stdin = o.stdin
	cmd.Stderr = &p.tail
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = p.terminate
	cmd.WaitDelay = killGrace + time.Second
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	p.stdout = pipe
	return p, nil
}

func (p *gitProc) terminate() error {
	p.killMu.Lock()
	defer p.killMu.Unlock()
	if p.exited {
		return nil
	}
	pid := p.cmd.Process.Pid
	if p.hard == nil {
		p.hard = time.AfterFunc(killGrace, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	}
	return syscall.Kill(-pid, syscall.SIGTERM)
}

// stop 提前终止进程（截断或不再需要后续输出）并读空 stdout。
func (p *gitProc) stop() {
	_ = p.terminate()
	_, _ = io.Copy(io.Discard, p.stdout)
}

// finish 等进程退出并返回退出码。stopped = 调用方主动终止过，此时不把终止当错误；rerr 是读 stdout 的错误。
// 只有无法读取、超时或被取消（且非主动终止）时返回错误，非零退出码由调用方按 code 判断。
func (p *gitProc) finish(stopped bool, rerr error) (int, error) {
	defer p.cancel()
	werr := p.cmd.Wait()
	p.killMu.Lock()
	p.exited = true
	if p.hard != nil {
		p.hard.Stop()
	}
	p.killMu.Unlock()
	if p.ctx.Err() != nil && !stopped {
		return 0, fmt.Errorf("git %s: %w", p.name, p.ctx.Err())
	}
	if rerr != nil && !stopped {
		return 0, rerr
	}
	var ee *exec.ExitError
	switch {
	case werr == nil:
		return 0, nil
	case errors.As(werr, &ee):
		code := ee.ExitCode()
		if code < 0 && !stopped {
			return code, fmt.Errorf("git %s: %w", p.name, werr)
		}
		return code, nil
	case !stopped:
		return 0, werr
	}
	return 0, nil
}

// git 执行 git 并收集至多 o.limit 字节的 stdout（超出即终止进程并置 truncated）；只有无法启动、超时或被取消
// （且非截断）时返回错误，非零退出码由调用方按 code 判断。
func (s *Service) git(ctx context.Context, o gitRun, args ...string) (gitResult, error) {
	p, err := s.startGit(ctx, o, args...)
	if err != nil {
		return gitResult{}, err
	}
	limit := max(o.limit, 0)
	out, rerr := io.ReadAll(io.LimitReader(p.stdout, int64(limit)+1))
	res := gitResult{out: out}
	if len(out) > limit {
		res.out, res.truncated = out[:limit], true
		p.stop()
	}
	res.code, err = p.finish(res.truncated, rerr)
	res.stderr = p.tail.String()
	return res, err
}

// gitStream 是 git 的 stdout 读取器：读到 EOF 时收尾，退出码非 0 时 Read 返回错误（而不是 EOF）；
// 没读完就 Close 时终止进程。
type gitStream struct {
	p    *gitProc
	done bool
}

// openGit 启动 git 并返回其 stdout 的流式读取器（大文件内容等）；调用方必须 Close。
func (s *Service) openGit(ctx context.Context, o gitRun, args ...string) (io.ReadCloser, error) {
	p, err := s.startGit(ctx, o, args...)
	if err != nil {
		return nil, err
	}
	return &gitStream{p: p}, nil
}

func (g *gitStream) Read(b []byte) (int, error) {
	if g.done {
		return 0, io.EOF
	}
	n, err := g.p.stdout.Read(b)
	if err == nil {
		return n, nil
	}
	g.done = true
	rerr := err
	if errors.Is(err, io.EOF) {
		rerr = nil
	}
	code, ferr := g.p.finish(false, rerr)
	switch {
	case ferr != nil:
		return n, ferr
	case code != 0:
		return n, fmt.Errorf("git %s: exit %d: %s", g.p.name, code, strings.TrimSpace(g.p.tail.String()))
	}
	return n, io.EOF
}

func (g *gitStream) Close() error {
	if g.done {
		return nil
	}
	g.done = true
	g.p.stop()
	_, err := g.p.finish(true, nil)
	return err
}

// noTimeout 用作 gitRun.timeout：不限时，只随 ctx 取消（smart-HTTP 的 upload-pack / receive-pack）。
const noTimeout time.Duration = -1

// gitPipe 执行 git 并把 stdout 流式写入 w（w 有 Flush() error 时每次写后 Flush），stdin 原样喂入；没有超时、
// 没有 stdout 上限。取消方式同 startGit（向进程组发 SIGTERM，killGrace 后才 SIGKILL）。写 w 失败（客户端断开）后
// 继续读空 stdout，让 git 自己走完（不因写失败杀进程）；stdout 结束后关闭实现了 io.Closer 的 stdin。
// 返回 stderr 末尾 4 KiB；无法启动、被取消、退出码非 0 或写 w 失败时返回错误。
func (s *Service) gitPipe(ctx context.Context, o gitRun, w io.Writer, args ...string) (stderrTail string, err error) {
	o.timeout = noTimeout
	p, err := s.startGit(ctx, o, args...)
	if err != nil {
		if c, ok := o.stdin.(io.Closer); ok {
			_ = c.Close()
		}
		return "", err
	}
	flusher, _ := w.(interface{ Flush() error })
	buf := make([]byte, 32<<10)
	var werr, rerr error
	for {
		n, err := p.stdout.Read(buf)
		if n > 0 && werr == nil {
			if _, werr = w.Write(buf[:n]); werr == nil && flusher != nil {
				werr = flusher.Flush()
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				rerr = err
			}
			break
		}
	}
	if c, ok := o.stdin.(io.Closer); ok {
		_ = c.Close()
	}
	code, ferr := p.finish(false, rerr)
	tail := p.tail.String()
	switch {
	case ferr != nil:
		return tail, ferr
	case code != 0:
		return tail, fmt.Errorf("git %s: exit %d", p.name, code)
	case werr != nil:
		return tail, werr
	}
	return tail, nil
}

// subcommand 是 args 里第一个不属于「-c <键=值>」的参数（错误信息里的名字）。
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return ""
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
// 令牌只进环境变量（GIT_CONFIG_*），不进 argv、不进磁盘配置。非 github.com 主机的令牌先取项目的 git/<host>/token、
// 再取全局的（M24 阶段 ④）；读项目令牌出错时拒绝（auth_failed），不回落到可能更宽的全局令牌。
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
		tok, found, err := s.sec.ProjectGitToken(prj.ID, host)
		if err != nil {
			slog.Warn("读取项目 Git 令牌失败，拒绝拉取", "project", prj.ID, "host", host, "err", err)
			return remote{}, &syncError{code: "auth_failed", msg: "读取项目 Git 令牌失败"}
		}
		if !found {
			tok, found = s.sec.GitToken(host)
		}
		if found && tok != "" {
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
