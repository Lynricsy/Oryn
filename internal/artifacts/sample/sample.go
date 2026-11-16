// Package sample 是 artifact 页面 `sample` 能力的执行端（M14 步骤 7.5）：由控制面自己一次性运行钉住的 Core
// （coordinator.omp_path），不经过 orb。每个问题一个临时 HOME（0700，用完即删）：omp-home 的 models.yml 与
// config.yml 渲染成指向本机回环 egress 代理的配置，代理按本次渲染出的路由注入真实密钥、不放行任何 git 访问；
// omp 以 `--mode rpc --no-session --no-tools …` 运行，环境变量只有 HOME、PI_CODING_AGENT_DIR 与 PATH。
// 帧循环见 rpc.go。
package sample

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/secrets"
)

const (
	// DefaultTimeout 是一个问题（含启动 omp 与模型回答）的时限。
	DefaultTimeout = 120 * time.Second
	// DefaultMaxProcs 是全局同时运行的 omp 进程数上限；满了的问题排队等待。
	DefaultMaxProcs = 4
	// waitDelay 是进程被杀之后等它的输出管道关闭的上限。
	waitDelay = 5 * time.Second
	// egressID 是这些运行在 egress 日志里的标识（代替 orb id）。
	egressID = "artifact-sample"
	// maxErrDetail 是错误信息里附带的 stderr 末尾字节数上限。
	maxErrDetail = 2048
)

// exitGrace 是拿到 prompt_result、关闭 stdin 后等 omp 自行退出的时间，超时则杀掉进程组（测试可缩短）。
var exitGrace = 3 * time.Second

// ErrTimeout 表示问题在 Config.Timeout 内没有得到回答。
var ErrTimeout = errors.New("sample: 回答超时")

// ModelError 表示 omp 给出了失败的 prompt_result（模型报错、没有可用的模型等）。
type ModelError struct{ Message string }

func (e *ModelError) Error() string { return "sample: 模型没有给出回答: " + e.Message }

// Image 是随问题发给模型的一张图片：MimeType 是 image/png 之类，Data 是标准 base64。
type Image struct {
	MimeType string
	Data     string
}

// Config 是执行端的配置。
type Config struct {
	// OmpPath 是宿主上的 omp 二进制（coordinator.omp_path）；文件不存在时能力不可用。
	OmpPath string
	// Home 是全局 omp-home（取 models.yml 与 config.yml）；nil = 不渲染任何配置（测试用）。
	Home *agentcfg.Home
	// Vault 解析 models.yml 里的密钥引用；nil = 任何密钥都查不到。
	Vault *secrets.Vault
	// Timeout 为 0 时取 DefaultTimeout；MaxProcs 为 0 时取 DefaultMaxProcs。
	Timeout  time.Duration
	MaxProcs int
	// Run 运行一次 omp RPC：home 是临时 HOME（渲染后的配置在 AgentDir(home) 下），stdin / stdout 是 RPC 的两端。
	// 必须在 ctx 结束时尽快返回，返回前不再写 stdout。nil = 以从零构造的环境变量运行 OmpPath（测试可替换）。
	Run func(ctx context.Context, home string, stdin io.Reader, stdout io.Writer) error
}

// Sampler 执行页面的问题，并发安全。
type Sampler struct {
	cfg   Config
	res   resolver
	slots chan struct{}
}

// New 构造执行端。
func New(cfg Config) *Sampler {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxProcs <= 0 {
		cfg.MaxProcs = DefaultMaxProcs
	}
	s := &Sampler{cfg: cfg, res: resolver{v: cfg.Vault}, slots: make(chan struct{}, cfg.MaxProcs)}
	if s.cfg.Run == nil {
		s.cfg.Run = s.runOmp
	}
	return s
}

// Available 报告 OmpPath 是否指向一个存在的普通文件。
func (s *Sampler) Available() bool {
	if s == nil || s.cfg.OmpPath == "" {
		return false
	}
	fi, err := os.Stat(s.cfg.OmpPath)
	return err == nil && fi.Mode().IsRegular()
}

// AgentDir 返回临时 HOME 下 omp 的配置目录（PI_CODING_AGENT_DIR）。
func AgentDir(home string) string { return filepath.Join(home, ".omp", "agent") }

// Ask 把 prompt（与 images）交给模型，回答的增量逐段交给 onText（onText 报错即中止），返回完整回答。
// 全局进程数满时排队，排队与回答都受 ctx 约束；ctx 结束时 omp 被杀掉、临时 HOME 被删，回 ctx 的错误。
// 超过 Config.Timeout 回 ErrTimeout，模型报错回 *ModelError。
func (s *Sampler) Ask(ctx context.Context, prompt string, images []Image, onText func(string) error) (string, error) {
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.slots }()
	tctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	answer, err := s.ask(tctx, prompt, images, onText)
	if err != nil && ctx.Err() == nil && errors.Is(tctx.Err(), context.DeadlineExceeded) {
		return "", ErrTimeout
	}
	if err != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	return answer, err
}

// ask 建临时 HOME、起 egress 代理、运行 omp 并走完一次帧循环。返回时 omp 已退出、临时 HOME 已删除。
func (s *Sampler) ask(ctx context.Context, prompt string, images []Image, onText func(string) error) (string, error) {
	home, err := os.MkdirTemp("", "oryn-sample-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(home) }()
	if err := os.MkdirAll(AgentDir(home), 0o700); err != nil {
		return "", err
	}
	srv, err := s.serveConfig(home)
	if err != nil {
		return "", err
	}
	defer func() { _ = srv.Close() }()
	return s.converseRun(ctx, home, prompt, images, onText)
}

// serveConfig 在 127.0.0.1:0 上起 egress 代理，把 omp-home 的配置渲染成指向它的版本写进临时 HOME。
// 调用方负责 Close 返回的 server（同时关闭监听）。
func (s *Sampler) serveConfig(home string) (*http.Server, error) {
	files := map[string][]byte{}
	if s.cfg.Home != nil {
		for _, p := range []string{agentcfg.ModelsFile, agentcfg.ConfigFile} {
			b, _, err := s.cfg.Home.Get(p)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			files[p] = b
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("监听 egress 代理: %w", err)
	}
	rendered, err := agentcfg.RenderAt(files, s.res, ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	srv := &http.Server{
		Handler:           egress.NewHandler(egressID, policyResolver{resolver: s.res, routes: rendered.Routes}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("sample: egress 代理退出", "err", err)
		}
	}()
	for p, b := range rendered.Files {
		if err := os.WriteFile(filepath.Join(AgentDir(home), filepath.FromSlash(p)), b, 0o600); err != nil {
			_ = srv.Close()
			return nil, err
		}
	}
	return srv, nil
}

// resolver 把可为 nil 的密钥库适配为 agentcfg.Resolver：nil 时任何密钥都查不到。
type resolver struct{ v *secrets.Vault }

func (r resolver) Lookup(name string) (string, string, bool) {
	if r.v == nil {
		return "", "", false
	}
	return r.v.Lookup(name)
}

func (r resolver) SecretByID(id string) (string, []string, bool) {
	if r.v == nil {
		return "", nil, false
	}
	return r.v.SecretByID(id)
}

// policyResolver 是一次运行的 egress.Resolver：只放行本次渲染出的模型路由，没有任何 git 访问。
type policyResolver struct {
	resolver
	egress.NoGateLookup // 没有 GitHub 路由，门禁查询不会被调用
	routes              map[string]egress.Route
}

func (p policyResolver) Policy() egress.Policy { return egress.Policy{Routes: p.routes, NoGit: true} }

func (policyResolver) GitToken(string) (string, bool) { return "", false }

func (policyResolver) GitHubToken() (string, bool) { return "", false }

// 一次运行没有 GitHub 路由，不会出现 GitHub 写操作。
func (policyResolver) GitHubWriteBegin(egress.GitHubWrite) string       { return "" }
func (policyResolver) GitHubWriteEnd(egress.GitHubWrite, int, []string) {}
