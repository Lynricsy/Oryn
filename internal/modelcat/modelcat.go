// Package modelcat 是控制面的中央模型目录（M13 步骤 1.2）：在临时 HOME 里用钉住的 Core 跑
// `omp models --json --kind all`，模型发现请求经本机回环 egress 代理注入真实密钥，结果按缓存键与 TTL 缓存。
package modelcat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/core"
	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/secrets"
)

const (
	// DefaultTTL 是同一缓存键下结果的有效期。
	DefaultTTL = 24 * time.Hour
	// DefaultTimeout 是单次 omp 运行（含代理请求）的时限。
	DefaultTimeout = 60 * time.Second
	// cacheFile 是缓存目录下保存最近一份成功结果的文件。
	cacheFile = "catalog.json"
	// keyEgressAddr 是计算缓存键时渲染用的固定代理地址：真实监听端口每次运行都不同，不能进入缓存键。
	keyEgressAddr = "127.0.0.1:0"
	// maxErrDetail 是错误信息里附带的 stderr 末尾字节数上限。
	maxErrDetail = 2048
)

// Cost 是每百万 token 的价格（美元）。
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// Model 是 `omp models --json` 输出中的一个模型。
type Model struct {
	Provider      string   `json:"provider"`
	ID            string   `json:"id"`
	Selector      string   `json:"selector"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	ContextWindow int      `json:"contextWindow"`
	MaxTokens     int      `json:"maxTokens"`
	Reasoning     bool     `json:"reasoning"`
	Thinking      []string `json:"thinking"`
	// ThinkingDefault 是模型自带的默认思考档位（Core 的 thinking.defaultLevel）：新会话与切模型都优先用它。
	ThinkingDefault ReportedLevel `json:"thinkingDefault,omitzero"`
	Input           []string      `json:"input"`
	Cost            *Cost         `json:"cost"`
	PricingStatus   string        `json:"pricingStatus"`
}

// ReportedLevel 是 Core 报告的一个可空档位，并记下键是否出现：旧版 Core 不输出 thinkingDefault（未知），
// 新版对没有默认档位的模型输出 null（已知没有），两者要区分开。
type ReportedLevel struct {
	// Set 表示 JSON 里有这个键（值可以是 null）。
	Set bool
	// Value 是档位；null 时为空串。
	Value string
}

// UnmarshalJSON 记下键出现过；null 也会走到这里。
func (r *ReportedLevel) UnmarshalJSON(b []byte) error {
	r.Set = true
	if string(b) == "null" {
		r.Value = ""
		return nil
	}
	return json.Unmarshal(b, &r.Value)
}

// MarshalJSON 写回同样的形状（落盘缓存）：没有档位写 null；键没出现过由 omitzero 省略。
func (r ReportedLevel) MarshalJSON() ([]byte, error) {
	if r.Value == "" {
		return []byte("null"), nil
	}
	return json.Marshal(r.Value)
}

// IsZero 让 omitzero 在旧版 Core 没报告时省略这个键。
func (r ReportedLevel) IsZero() bool { return !r.Set }

// Result 是一次目录查询的结果。Models 与 Warnings 在多个调用方之间共享，只读。
type Result struct {
	Models    []Model
	FetchedAt time.Time
	// Stale 为真表示这不是按当前配置刚跑出的结果：刷新失败时回退的上一份结果，
	// 或进程重启后从磁盘恢复、尚未重跑验证的结果。
	Stale bool
	// Warnings 是 omp 的 stderr（按行），以及刷新失败时的错误说明。
	Warnings []string
}

// Config 是目录的运行参数。
type Config struct {
	// OmpPath 是宿主上的 omp 二进制（coordinator.omp_path）。
	OmpPath string
	// CacheDir 保存最近一份成功结果，重启后立即可用；空 = 不落盘。
	CacheDir string
	// TTL 为 0 时取 DefaultTTL。
	TTL time.Duration
	// Timeout 为 0 时取 DefaultTimeout。
	Timeout time.Duration
	// Run 执行 `omp models --json --kind all` 并返回 stdout 与 stderr；home 是临时 HOME，
	// 渲染后的配置在 AgentDir(home) 下。nil = 以从零构造的环境变量运行 OmpPath（测试可替换）。
	Run func(ctx context.Context, home string) (stdout, stderr []byte, err error)
	// SchemaRun 以 RPC 模式运行 omp、发送 get_settings_schema 并返回 id 匹配的 response 行；home 同 Run。
	// nil = 运行 OmpPath（测试可替换）。
	SchemaRun func(ctx context.Context, home string) (response []byte, err error)
	// AgentsRun 执行 `omp agents unpack --dir <dir> --json` 把内置子代理写到 dir；nil = 运行 OmpPath（测试可替换）。
	AgentsRun func(ctx context.Context, dir string) error
}

// AgentDir 返回临时 HOME 下 omp 的配置目录（PI_CODING_AGENT_DIR）。
func AgentDir(home string) string { return filepath.Join(home, ".omp", "agent") }

// Catalog 是带缓存的中央模型目录，并发安全。
type Catalog struct {
	cfg  Config
	home *agentcfg.Home
	res  vaultResolver

	mu       sync.Mutex
	last     *entry // 最近一份成功结果
	inflight *call  // 正在进行的运行（同一时刻至多一个）

	schemaMu     sync.Mutex
	schema       *schemaEntry // 最近一份成功的设置 schema
	schemaFlight *schemaCall  // 正在进行的 schema 运行

	agentsMu  sync.Mutex
	agentsKey string
	agents    []agentcfg.AgentSpec
}

// entry 是一份成功结果及其缓存键。
type entry struct {
	key string
	res Result
	// verified 为假表示从磁盘恢复、本进程尚未重跑过。
	verified bool
}

// call 是一次运行；done 关闭后 res/err 只读。
type call struct {
	key  string
	done chan struct{}
	res  Result
	err  error
}

// diskEntry 是 cacheFile 的格式。
type diskEntry struct {
	Key       string    `json:"key"`
	FetchedAt time.Time `json:"fetchedAt"`
	Models    []Model   `json:"models"`
	Warnings  []string  `json:"warnings,omitempty"`
}

// New 构造目录，并从 cfg.CacheDir 恢复上一份结果（读不出时忽略）。vault 可为 nil（不解析任何密钥引用）。
func New(cfg Config, home *agentcfg.Home, vault *secrets.Vault) *Catalog {
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	c := &Catalog{cfg: cfg, home: home, res: vaultResolver{v: vault}}
	if c.cfg.Run == nil {
		c.cfg.Run = c.runOmp
	}
	if c.cfg.SchemaRun == nil {
		c.cfg.SchemaRun = c.rpcSettingsSchema
	}
	c.last = c.loadDisk()
	c.schema = c.loadSchemaDisk()
	return c
}

// Get 返回模型目录。每次调用都重新读取 models.yml 与 config.yml 计算缓存键：键不变且未过期时直接返回缓存；
// 键不变但结果已过期或来自磁盘时先返回旧结果（Stale）并在后台重跑；键变了、force 为真或没有结果时运行 omp 并等待。
// 并发调用合并为一次运行。运行失败时返回上一份结果并标 Stale，只有从未成功过才返回错误。
func (c *Catalog) Get(ctx context.Context, force bool) (Result, error) {
	key, files, err := c.loadConfig()
	if err != nil {
		return c.fallback(fmt.Errorf("读取模型配置: %w", err))
	}
	c.mu.Lock()
	for c.inflight != nil && c.inflight.key != key {
		// 正在按旧配置运行：等它结束再按当前配置判断
		cl := c.inflight
		c.mu.Unlock()
		select {
		case <-cl.done:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
		c.mu.Lock()
	}
	cl := c.inflight
	if cl == nil {
		if last := c.last; !force && last != nil && last.key == key {
			r := last.res
			if last.verified && time.Since(r.FetchedAt) < c.cfg.TTL {
				c.mu.Unlock()
				return r, nil
			}
			c.startLocked(key, files)
			c.mu.Unlock()
			r.Stale = true
			return r, nil
		}
		cl = c.startLocked(key, files)
	}
	c.mu.Unlock()
	select {
	case <-cl.done:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	if cl.err != nil {
		return c.fallback(cl.err)
	}
	return cl.res, nil
}

// fallback 在刷新失败时返回上一份结果（Stale，附错误说明）；没有结果时返回 err。
func (c *Catalog) fallback(err error) (Result, error) {
	c.mu.Lock()
	last := c.last
	c.mu.Unlock()
	if last == nil {
		return Result{}, err
	}
	r := last.res
	r.Stale = true
	r.Warnings = append(slices.Clip(r.Warnings), "刷新模型目录失败："+err.Error())
	return r, nil
}

// startLocked 在后台启动一次运行（调用方持有 c.mu）。运行不受任何调用方 ctx 取消的影响，只受 Timeout 限制。
func (c *Catalog) startLocked(key string, files map[string][]byte) *call {
	cl := &call{key: key, done: make(chan struct{})}
	c.inflight = cl
	go c.run(cl, files)
	return cl
}

func (c *Catalog) run(cl *call, files map[string][]byte) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Timeout)
	defer cancel()
	res, err := c.fetch(ctx, files)
	if err != nil {
		slog.Warn("模型目录刷新失败", "err", err)
	} else if err := c.saveDisk(cl.key, res); err != nil {
		slog.Warn("模型目录缓存写入失败", "dir", c.cfg.CacheDir, "err", err)
	}
	c.mu.Lock()
	if err == nil {
		c.last = &entry{key: cl.key, res: res, verified: true}
	}
	cl.res, cl.err = res, err
	c.inflight = nil
	c.mu.Unlock()
	close(cl.done)
}

// loadConfig 读取 models.yml 与 config.yml（不存在的跳过），并计算缓存键：
// sha256(渲染后的 models.yml ‖ config.yml ‖ 本架构 Core 钉住的 sha256)，各段带长度前缀。
func (c *Catalog) loadConfig() (string, map[string][]byte, error) {
	files := map[string][]byte{}
	for _, p := range []string{agentcfg.ModelsFile, agentcfg.ConfigFile} {
		b, _, err := c.home.Get(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		files[p] = b
	}
	rendered, err := agentcfg.RenderAt(files, c.res, keyEgressAddr)
	if err != nil {
		return "", nil, err
	}
	h := sha256.New()
	for _, part := range [][]byte{
		rendered.Files[agentcfg.ModelsFile],
		rendered.Files[agentcfg.ConfigFile],
		[]byte(core.Pin()[pinKey()]),
	} {
		_ = binary.Write(h, binary.BigEndian, uint64(len(part)))
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil)), files, nil
}

// pinKey 返回本机架构在 core.Pin() 中的键。
func pinKey() string {
	if runtime.GOARCH == "arm64" {
		return "linux-arm64"
	}
	return "linux-x64"
}

// newTempHome 建临时 HOME（0700）与其中的 AgentDir；调用方用完后 os.RemoveAll。
func newTempHome() (string, error) {
	tmp, err := os.MkdirTemp("", "oryn-modelcat-")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(AgentDir(tmp), 0o700); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	return tmp, nil
}

// ompEnv 是一次性运行 omp 的环境变量，从零构造。
func ompEnv(home string) []string {
	return []string{"HOME=" + home, "PI_CODING_AGENT_DIR=" + AgentDir(home), "PATH=/usr/bin:/bin"}
}

// fetch 渲染配置到临时 HOME（0700，结束即删），起本机回环 egress 代理，运行 omp 并解析输出。
func (c *Catalog) fetch(ctx context.Context, files map[string][]byte) (Result, error) {
	tmp, err := newTempHome()
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Result{}, fmt.Errorf("监听 egress 代理: %w", err)
	}
	rendered, err := agentcfg.RenderAt(files, c.res, ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return Result{}, err
	}
	srv := serveEgress(ln, c.res, rendered.Routes)
	defer func() { _ = srv.Close() }()
	for p, b := range rendered.Files {
		if err := os.WriteFile(filepath.Join(AgentDir(tmp), filepath.FromSlash(p)), b, 0o600); err != nil {
			return Result{}, err
		}
	}
	stdout, stderr, err := c.cfg.Run(ctx, tmp)
	if err != nil {
		return Result{}, fmt.Errorf("omp models: %w%s", err, errDetail(stderr))
	}
	models, err := parse(stdout)
	if err != nil {
		return Result{}, fmt.Errorf("解析 omp models 输出: %w%s", err, errDetail(stderr))
	}
	return Result{Models: models, FetchedAt: time.Now().UTC(), Warnings: lines(stderr)}, nil
}

// runOmp 是默认的 Config.Run：环境变量从零构造，工作目录为临时 HOME。
func (c *Catalog) runOmp(ctx context.Context, home string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, c.cfg.OmpPath, "models", "--json", "--kind", "all")
	cmd.Dir = home
	cmd.Env = ompEnv(home)
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// parse 解析 `{"models":[...]}`；未知字段忽略。
func parse(stdout []byte) ([]Model, error) {
	var out struct {
		Models *[]Model `json:"models"`
	}
	if err := json.Unmarshal(stdout, &out); err != nil {
		return nil, err
	}
	if out.Models == nil {
		return nil, errors.New("缺少 models 字段")
	}
	return *out.Models, nil
}

// lines 把 stderr 拆成非空行。
func lines(b []byte) []string {
	var out []string
	for l := range strings.SplitSeq(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// errDetail 返回附在错误后的 stderr 末尾（为空时返回空串）。
func errDetail(stderr []byte) string {
	s := strings.TrimSpace(string(stderr))
	if s == "" {
		return ""
	}
	if len(s) > maxErrDetail {
		s = "…" + s[len(s)-maxErrDetail:]
	}
	return ": " + s
}

// readCache 读取缓存目录下的 JSON 文件到 v；未配置缓存目录、文件不存在或损坏时返回 false。
func (c *Catalog) readCache(name string, v any) bool {
	if c.cfg.CacheDir == "" {
		return false
	}
	path := filepath.Join(c.cfg.CacheDir, name)
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("读取模型目录缓存失败", "path", path, "err", err)
		}
		return false
	}
	if err := json.Unmarshal(b, v); err != nil {
		slog.Warn("模型目录缓存损坏，已忽略", "path", path, "err", err)
		return false
	}
	return true
}

// writeCache 把 v 以 JSON 原子写入缓存目录（目录 0700，文件 0600）；未配置缓存目录时什么都不做。
func (c *Catalog) writeCache(name string, v any) error {
	if c.cfg.CacheDir == "" {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.cfg.CacheDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(c.cfg.CacheDir, name+".*")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(f.Name(), filepath.Join(c.cfg.CacheDir, name))
	}
	if werr != nil {
		_ = os.Remove(f.Name())
	}
	return werr
}

// loadDisk 读取落盘的上一份目录结果；不存在或损坏时返回 nil。
func (c *Catalog) loadDisk() *entry {
	var d diskEntry
	if !c.readCache(cacheFile, &d) || d.Key == "" || d.Models == nil {
		return nil
	}
	return &entry{key: d.Key, res: Result{Models: d.Models, FetchedAt: d.FetchedAt, Warnings: d.Warnings}}
}

// saveDisk 落盘最近一份成功的目录结果。
func (c *Catalog) saveDisk(key string, r Result) error {
	return c.writeCache(cacheFile, diskEntry{Key: key, FetchedAt: r.FetchedAt, Models: r.Models, Warnings: r.Warnings})
}
