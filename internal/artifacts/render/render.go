// Package render 在控制面主机上用关在沙箱里的 headless Chromium 渲染 artifact（缩略图、PNG、PDF；M14 步骤 4.1）。
//
// 隔离分四层：
//  1. Chromium 自己的多进程沙箱（除非配置了 no_sandbox，绝不传 --no-sandbox）；不可信的页面脚本只在渲染进程里执行。
//  2. 强制代理：Chromium 的全部网络（含回环）都经本包在 127.0.0.1 上开的代理，只放行渲染作业的内容与白名单 CDN。
//  3. Landlock 网络规则：Chromium 进程树只能 TCP connect 代理这一个端口（内核 Landlock ABI < 4 时跳过并记入自检日志）。
//  4. 独立的低权限进程：`orynd render-exec` 降权到非 root 的 uid、设 rlimit 与 no_new_privs 后再执行 Chromium，
//     进程自成一组，超时整组杀掉，orynd 退出时它被 SIGKILL；环境变量从零构造。
package render

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable 表示渲染不可用（自检没通过或还没完成）；错误文本带原因。
var ErrUnavailable = errors.New("渲染不可用")

// 作业的超时。
const (
	screenshotTimeout = 30 * time.Second
	pdfTimeout        = 60 * time.Second
	// waitDelay 是超时杀掉进程组之后等待输出管道关闭的上限。
	waitDelay = 5 * time.Second
	// maxSide 是窗口宽高的上限（与 render.png 接口的上限相同）。
	maxSide = 8000
	// stderrTail 是保留的 Chromium stderr 末尾字节数（用于失败原因）。
	stderrTail = 16 << 10
)

// Config 是渲染器的配置。
type Config struct {
	// Chromium 是可执行文件路径；空 = 依次在 PATH 里找 chromium、chromium-browser、google-chrome-stable、google-chrome。
	Chromium string
	// User 是 orynd 以 root 运行时跑 Chromium 的账号；root 下为空则渲染停用。非 root 时 Chromium 以 orynd 自己的 uid 运行。
	User string
	// NoSandbox 为真时给 Chromium 传 --no-sandbox：只在 orynd 自己跑在专用容器里、Chromium 的命名空间沙箱起不来时才打开。
	NoSandbox bool
	// Concurrency 是同时运行的 Chromium 上限；0 = 2。
	Concurrency int
	// Dir 是作业目录的上级目录（每个作业一个 0700 的子目录，root 下 chown 给 User）；渲染账号必须能进入它。
	Dir string
	// Exe 是带 render-exec 子命令的可执行文件（orynd 自己）。
	Exe string
	// CDNs 是非严格作业可以经代理连接的 CDN（`https://<主机>`）。
	CDNs []string
	// Site 提供作业的内容。
	Site Site
}

// Job 是一次渲染。
type Job struct {
	ArtifactID string
	// Version 为 0 时取作业开始时的最新版本。
	Version int
	// Path 是作业根之内的相对路径（空 = 入口）；Query 是不带 ? 的查询串，原样透传给页面（例如 view=thumbnail）。
	Path, Query string
	// Width、Height 是窗口尺寸（CSS 像素）；Scale 是设备像素比，0 = 1。
	Width, Height int
	Scale         float64
	// PDF 为真时打印成 PDF，否则截 PNG。
	PDF bool
}

// Renderer 运行渲染作业。
type Renderer struct {
	cfg      Config
	proxy    *proxy
	sem      chan struct{}
	chromium string
	uid, gid int
	// setup 非空表示配置本身就不能渲染（找不到 Chromium、root 下没配账号等），自检不必再跑。
	setup error

	mu     sync.Mutex
	ok     bool
	reason string
}

// New 开始监听代理并检查配置；配置问题不算错误（渲染停用，原因经 Status 给出），只有代理监听失败才回错误。
// 调用方随后应调 Preflight，在那之前 Status 为假。
func New(cfg Config) (*Renderer, error) {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 2
	}
	p, err := newProxy(cfg.Site, cfg.CDNs)
	if err != nil {
		return nil, fmt.Errorf("render: 代理监听失败：%w", err)
	}
	r := &Renderer{cfg: cfg, proxy: p, sem: make(chan struct{}, cfg.Concurrency), reason: "渲染自检还没有完成"}
	r.setup = r.configure()
	if r.setup != nil {
		r.reason = r.setup.Error()
	}
	return r, nil
}

// configure 解析 Chromium、渲染账号与作业目录。
func (r *Renderer) configure() error {
	if r.cfg.Site == nil || r.cfg.Exe == "" || r.cfg.Dir == "" {
		return errors.New("渲染器没有配置完整")
	}
	if os.Geteuid() == 0 {
		if r.cfg.User == "" {
			return errors.New("orynd 以 root 运行，但没有配置 artifacts.render.user（跑 Chromium 的低权限账号），渲染已停用")
		}
		u, err := user.Lookup(r.cfg.User)
		if err != nil {
			return fmt.Errorf("找不到 artifacts.render.user 账号 %s：%w", r.cfg.User, err)
		}
		if r.uid, err = strconv.Atoi(u.Uid); err != nil {
			return fmt.Errorf("账号 %s 的 uid %q 不是数字", r.cfg.User, u.Uid)
		}
		if r.gid, err = strconv.Atoi(u.Gid); err != nil {
			return fmt.Errorf("账号 %s 的 gid %q 不是数字", r.cfg.User, u.Gid)
		}
		if r.uid == 0 {
			return fmt.Errorf("artifacts.render.user %s 是 root，不能用它跑 Chromium", r.cfg.User)
		}
	} else {
		r.uid, r.gid = os.Getuid(), os.Getgid()
	}
	c, err := findChromium(r.cfg.Chromium)
	if err != nil {
		return err
	}
	r.chromium = c
	// 上级目录 0711：渲染账号能进入自己的作业目录，但列不出别的作业。清掉上次异常退出留下的作业目录。
	if err := os.MkdirAll(r.cfg.Dir, 0o711); err != nil {
		return fmt.Errorf("建作业目录 %s：%w", r.cfg.Dir, err)
	}
	if err := os.Chmod(r.cfg.Dir, 0o711); err != nil {
		return fmt.Errorf("设置作业目录 %s 的权限：%w", r.cfg.Dir, err)
	}
	entries, err := os.ReadDir(r.cfg.Dir)
	if err != nil {
		return fmt.Errorf("读作业目录 %s：%w", r.cfg.Dir, err)
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(r.cfg.Dir, e.Name()))
	}
	return nil
}

// Close 停止代理（进行中的作业由各自的 ctx 取消）。
func (r *Renderer) Close() { r.proxy.close() }

// Status 报告渲染是否可用；不可用时 reason 说明原因（自检失败的原因、配置问题，或自检还没完成）。
func (r *Renderer) Status() (ok bool, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ok, r.reason
}

func (r *Renderer) setStatus(ok bool, reason string) {
	r.mu.Lock()
	r.ok, r.reason = ok, reason
	r.mu.Unlock()
}

// Run 渲染一个作业，返回 PNG 或 PDF 字节。渲染不可用时回 ErrUnavailable；artifact 不存在、在回收站里时回 Site 的错误。
// 排队等待受 ctx 约束；截图 30 秒、PDF 60 秒超时，超时整个进程组被杀掉。
func (r *Renderer) Run(ctx context.Context, j Job) ([]byte, error) {
	if ok, reason := r.Status(); !ok {
		return nil, fmt.Errorf("%w：%s", ErrUnavailable, reason)
	}
	return r.run(ctx, j, nil)
}

// run 执行一个作业；fixed 非空时是自检作业，代理直接回这段 HTML，不涉及任何 artifact。
func (r *Renderer) run(ctx context.Context, j Job, fixed []byte) ([]byte, error) {
	if r.setup != nil {
		return nil, fmt.Errorf("%w：%s", ErrUnavailable, r.setup.Error())
	}
	if j.Scale == 0 {
		j.Scale = 1
	}
	switch {
	case j.Width < 1 || j.Width > maxSide || j.Height < 1 || j.Height > maxSide:
		return nil, fmt.Errorf("render: 窗口尺寸 %d×%d 超出 1–%d", j.Width, j.Height, maxSide)
	case j.Scale < 0.1 || j.Scale > 4:
		return nil, fmt.Errorf("render: 倍率 %v 超出 0.1–4", j.Scale)
	case strings.HasPrefix(j.Path, "/") || strings.ContainsAny(j.Path, "?#\x00"):
		return nil, fmt.Errorf("render: 路径 %q 必须是不带查询串的相对路径", j.Path)
	case strings.ContainsAny(j.Query, "# \t\r\n\x00"):
		return nil, fmt.Errorf("render: 查询串 %q 不合法", j.Query)
	}
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-r.sem }()

	token := newToken()
	pj := &proxyJob{fixed: fixed}
	if fixed == nil {
		version, strict, err := r.cfg.Site.RenderTarget(ctx, j.ArtifactID, j.Version)
		if err != nil {
			return nil, err
		}
		pj.target = Target{ArtifactID: j.ArtifactID, Version: version, Strict: strict, Self: "http://" + Host + "/" + token + "/"}
	}
	r.proxy.add(token, pj)
	defer r.proxy.remove(token)

	dir := filepath.Join(r.cfg.Dir, token)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("render: 建作业目录：%w", err)
	}
	defer os.RemoveAll(dir)
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, r.uid, r.gid); err != nil {
			return nil, fmt.Errorf("render: 把作业目录交给渲染账号：%w", err)
		}
	}
	u := url.URL{Scheme: "http", Host: Host, Path: "/" + token + "/" + j.Path, RawQuery: j.Query}
	cj := chromiumJob{Dir: dir, Port: r.proxy.port(), URL: u.String(), Width: j.Width, Height: j.Height,
		Scale: j.Scale, PDF: j.PDF, NoSandbox: r.cfg.NoSandbox}
	timeout := screenshotTimeout
	if j.PDF {
		timeout = pdfTimeout
	}
	if err := r.exec(ctx, cj, timeout); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(cj.output())
	if err != nil || len(out) == 0 {
		return nil, errors.New("render: Chromium 没有写出结果")
	}
	return out, nil
}

// exec 经 `orynd render-exec` 运行一次 Chromium 并等它退出。
func (r *Renderer) exec(ctx context.Context, cj chromiumJob, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := ExecArgs{UID: r.uid, GID: r.gid, Port: uint16(cj.Port), Argv: append([]string{r.chromium}, chromiumArgs(cj)...)}
	cmd := exec.CommandContext(ctx, r.cfg.Exe, args.Args()...)
	cmd.Env = chromiumEnv(cj.Dir)
	cmd.Dir = cj.Dir
	cmd.SysProcAttr = procAttr()
	var stderr tailBuffer
	cmd.Stderr = &stderr
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	if cmd.Process != nil {
		// Chromium 的子进程（zygote、渲染进程）可能比主进程活得久：整组清掉
		_ = killGroup(cmd.Process.Pid)
	}
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("render: Chromium 超过 %s 没有完成", timeout)
		}
		return ctx.Err()
	}
	return fmt.Errorf("render: Chromium 失败（%v）：%s", err, stderr.reason())
}

// newToken 返回 26 位随机小写 base32 作业令牌（16 字节）。
func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// tailBuffer 只保留写入内容的最后 stderrTail 字节。
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - stderrTail; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

// reason 从 stderr 末尾挑出最能说明失败的一行：render-exec 自己的原因优先，其次是 Chromium 的 FATAL 行，
// 再次是最后一个非空行。
func (t *tailBuffer) reason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(string(bytes.TrimSpace(t.buf)), "\n")
	for _, prefix := range []string{"render-exec: ", ""} {
		for i := len(lines) - 1; i >= 0; i-- {
			l := strings.TrimSpace(lines[i])
			if prefix != "" && strings.HasPrefix(l, prefix) {
				return l
			}
			if prefix == "" && strings.Contains(l, ":FATAL:") {
				return l
			}
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "没有输出"
}
