// Package config 读取并校验 orynd 与 provider 的 YAML 配置。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 支持在 YAML 中用 "5m" 这类字符串表示时长。
type Duration time.Duration

// UnmarshalYAML 解析 Go 时长字符串。
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("无效时长 %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// D 返回 time.Duration。
func (d Duration) D() time.Duration { return time.Duration(d) }

// Idle 是 orb 空闲策略。
type Idle struct {
	AgentIdle       Duration `yaml:"agent_idle"`
	UserIdle        Duration `yaml:"user_idle"`
	StopAfterFrozen Duration `yaml:"stop_after_frozen"`
	WakeOnFocus     bool     `yaml:"wake_on_focus"`
}

// OrbDefaults 是新建 orb 时的默认 provider 与类型。
type OrbDefaults struct {
	Provider string `yaml:"provider"`
	Type     string `yaml:"type"`
}

// Gateway 是官方推送网关（M28，iOS 包与 Android `gms` 包的推送通道）。
type Gateway struct {
	// URL 是网关的 origin（不带路径）：https，或回环主机的 http（本机验收）；空串 = 关闭网关。
	URL string `yaml:"url"`
}

// Push 是推送通道配置。
type Push struct {
	Gateway Gateway `yaml:"gateway"`
}

// Coordinator 是协调宿主配置（M9 协调宿主分册 H6）。
type Coordinator struct {
	// OmpPath 是宿主上的 omp 二进制（与 orb 镜像同一份发布产物）。
	OmpPath string `yaml:"omp_path"`
	// Dir 是协调宿主的工作目录（omp 副本、HOME、host.log）；空 = <data_dir>/coordinator。
	Dir string `yaml:"dir"`
	// User 是 orynd 以 root 运行时协调宿主降权到的账号。空（缺省）、nobody、nfsnobody 或 uid 65534 的账号 =
	// 自动：使用（必要时用 useradd 创建）本实例专用的系统账号 oryn-coord-<协调目录路径的 fnv32a>。
	// 写别的名字则用该账号（须不是 root，且不被本实例以外的进程使用）。
	User string `yaml:"user"`
	// EgressPort 是协调宿主内模型代理监听的回环端口。
	EgressPort int `yaml:"egress_port"`
	// VerifyOmp 为 nil 或 true 时校验 omp 的 sha256 与 core/source.json 一致。
	VerifyOmp *bool `yaml:"verify_omp"`
	// Model 是协调对话的默认模型（"provider/id"）；空 = omp 配置的默认模型。
	Model string `yaml:"model"`
	// WebAllowHosts 是全局助手联网时允许连接的非公网目标（M19）：小写的 host 或 host:port，只对域名有意义；
	// 不在表里的目标若解析到回环、私网、链路本地等地址，正向代理一律拒绝。
	WebAllowHosts []string `yaml:"web_allow_hosts"`
}

// Verify 报告是否校验 omp 的版本钉（缺省 true）。
func (c Coordinator) Verify() bool { return c.VerifyOmp == nil || *c.VerifyOmp }

// ArtifactsRender 是控制面渲染 artifact（缩略图、PNG、PDF）用的 headless Chromium（M14 步骤 4.1）。
type ArtifactsRender struct {
	// Chromium 是可执行文件路径；空 = 依次在 PATH 里找 chromium、chromium-browser、google-chrome-stable、google-chrome。
	Chromium string `yaml:"chromium"`
	// User 是 orynd 以 root 运行时跑 Chromium 的账号（不能是 root）；root 下为空则渲染停用。非 root 时不用。
	User string `yaml:"user"`
	// NoSandbox 为真时给 Chromium 传 --no-sandbox：只在 orynd 自己跑在专用容器里、Chromium 的命名空间沙箱起不来时才打开。
	NoSandbox bool `yaml:"no_sandbox"`
	// Concurrency 是同时运行的 Chromium 上限；0（缺省）= 2。
	Concurrency int `yaml:"concurrency"`
	// Dir 是作业目录的上级目录；空 = <data_dir>/artifacts/render。渲染账号必须能进入它的每一级上级目录
	// （data_dir 是 root 的 0700 目录时，要设到 /var/tmp/oryn-render 这类位置）。
	Dir string `yaml:"dir"`
}

// Artifacts 是 M14 Artifacts 的配置。
type Artifacts struct {
	Render ArtifactsRender `yaml:"render"`
}

// Repos 是项目仓库的控制面裸仓库（M24 阶段 ②）。
type Repos struct {
	// Git 是 git 可执行文件；空 = 在 PATH 里找 git（不带斜杠的名字也按 PATH 查找）。
	Git string `yaml:"git"`
	// MaxMirrorGB 是单个镜像的大小上限；0（缺省）= 5。拉取后超出即删除镜像并记 too_large。
	MaxMirrorGB int `yaml:"max_mirror_gb"`
	// MinFreeGB 是数据盘要保留的可用空间：低于它时不克隆、不拉取、不接受推送；0（缺省）= 2。
	MinFreeGB int `yaml:"min_free_gb"`
	// MaxLFSCacheGB 是镜像项目 Git LFS 对象缓存的总上限（所有项目合计，按最近使用淘汰）；0（缺省）= 2。
	MaxLFSCacheGB int `yaml:"max_lfs_cache_gb"`
}

// Schedule 是定时调度参数（M9）。
type Schedule struct {
	DeliverTimeout Duration `yaml:"deliver_timeout"`
	MaxFailures    int      `yaml:"max_failures"`
}

// StatsRetention 是统计数据的保留期（M10；0 = 永久，非零值至少 24h）。
type StatsRetention struct {
	// Samples 是 orb 资源采样（orb_samples）的保留期。
	Samples Duration `yaml:"samples"`
	// Events 是已删除 orb 的事件与 setup 记录、预算告警账本的保留期（活 orb 的事件永不删）。
	Events Duration `yaml:"events"`
	// Usage 是用量 / 工具调用 / 回合事实行的保留期。
	Usage Duration `yaml:"usage"`
}

// Stats 是统计分析配置（M10）。
type Stats struct {
	Retention StatsRetention `yaml:"retention"`
}

// Snapshots 是项目快照配置（M11a）。
type Snapshots struct {
	// Enabled 是全局开关；关闭后既不建立也不使用项目快照（项目自己的开关之上）。
	Enabled bool `yaml:"enabled"`
	// MaxAge 是一条快照血统（从基础镜像全新 setup 起算，刷新不重置）的最长复用时间。
	MaxAge Duration `yaml:"max_age"`
	// MaxDepth 是同一血统内的刷新次数上限，超过即退役、下次从基础镜像重建。
	MaxDepth int `yaml:"max_depth"`
	// MinSetup 是值得建立新血统的最短 setup 耗时；更快的 setup 不建快照。
	MinSetup Duration `yaml:"min_setup"`
	// MaxTotalGB 是每个 provider 上项目快照（相对基础镜像）合计占用的上限。
	MaxTotalGB int `yaml:"max_total_gb"`
	// MaxSnapshotGB 是单个快照（相对基础镜像）的大小上限；超出即退役删除，不挤占其它快照。
	// 0 = 未设置：取 min(DefaultMaxSnapshotGB, MaxTotalGB)，见 MaxSnapshotBytes。
	MaxSnapshotGB int `yaml:"max_snapshot_gb"`
}

// MaxTotalBytes 返回 max_total_gb 换算的字节数。
func (s Snapshots) MaxTotalBytes() int64 { return int64(s.MaxTotalGB) << 30 }

// DefaultMaxSnapshotGB 是 max_snapshot_gb 未设置时的单快照上限（再受 max_total_gb 限制）。
const DefaultMaxSnapshotGB = 10

// MaxSnapshotBytes 返回单快照上限的字节数：max_snapshot_gb 为 0（未设置）时取 min(10, max_total_gb) GiB。
func (s Snapshots) MaxSnapshotBytes() int64 {
	g := s.MaxSnapshotGB
	if g == 0 {
		g = min(DefaultMaxSnapshotGB, s.MaxTotalGB)
	}
	return int64(g) << 30
}

// OrbTLS 是 orb link 专用的 TLS 监听（M11a 审查修复）；Listen 为空即不开。
type OrbTLS struct {
	// Listen 是 TLS 监听地址（host:port，host 必须是 IP，例如 172.18.0.1:7643）；这些 IP 总会写进服务端证书。
	Listen []string `yaml:"listen"`
	// Hosts 是服务端证书里额外的 SAN（主机名或 IP）。
	Hosts []string `yaml:"hosts"`
}

// ListenIPs 返回 Listen 各地址的 IP（去重，按出现顺序）；须在 Validate 通过后调用。
func (t OrbTLS) ListenIPs() []net.IP {
	var ips []net.IP
	for _, l := range t.Listen {
		host, _, err := net.SplitHostPort(l)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil || slices.ContainsFunc(ips, ip.Equal) {
			continue
		}
		ips = append(ips, ip)
	}
	return ips
}

// validate 校验 orb_tls：listen 可解析、host 为 IP、端口 1–65535；hosts 为 IP 或合法主机名。
func (t OrbTLS) validate() []error {
	var errs []error
	for _, l := range t.Listen {
		host, port, err := net.SplitHostPort(l)
		if err != nil {
			errs = append(errs, fmt.Errorf("orb_tls.listen %q: %w", l, err))
			continue
		}
		if net.ParseIP(host) == nil {
			errs = append(errs, fmt.Errorf("orb_tls.listen %q：主机部分必须是 IP", l))
		}
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			errs = append(errs, fmt.Errorf("orb_tls.listen %q：端口必须在 1–65535 之间", l))
		}
	}
	for _, h := range t.Hosts {
		if net.ParseIP(h) == nil && !sanHostRe.MatchString(h) {
			errs = append(errs, fmt.Errorf("orb_tls.hosts %q 不是合法的 IP 或主机名", h))
		}
	}
	return errs
}

// sanHostRe 是证书 SAN 允许的主机名形态（不带端口与尾点）。
var sanHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

// Pool 是预热池配置（M11a）。
type Pool struct {
	// MaxTotal 是全局预热 orb（warming + idle）上限；0 = 关闭预热池。
	MaxTotal int `yaml:"max_total"`
	// MaxAge 是 idle 池 orb 的最长存活时间，到期回收换新。
	MaxAge Duration `yaml:"max_age"`
	// ActiveWindow 是项目「活跃」的判定窗口：窗口内没有新线程的项目不预热。
	ActiveWindow Duration `yaml:"active_window"`
}

// GitHub 是 GitHub 集成的上游地址（M14）；默认 github.com，测试与验收脚本指向假服务。
type GitHub struct {
	APIURL string `yaml:"api_url"`
	WebURL string `yaml:"web_url"`
}

// validate 要求两者都是带 scheme（http/https）与 host 的绝对地址，不带 query 与 fragment。
func (g GitHub) validate() []error {
	var errs []error
	for _, f := range []struct{ key, v string }{{"api_url", g.APIURL}, {"web_url", g.WebURL}} {
		u, err := url.Parse(f.v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
			errs = append(errs, fmt.Errorf("github.%s %q 不是合法的 http(s) 地址", f.key, f.v))
		}
	}
	return errs
}

// Orynd 对应 /etc/oryn/orynd.yaml。
type Orynd struct {
	Listen       []string    `yaml:"listen"`
	PublicURL    string      `yaml:"public_url"`
	PortalDomain string      `yaml:"portal_domain"`
	DataDir      string      `yaml:"data_dir"`
	LogLevel     string      `yaml:"log_level"`
	Idle         Idle        `yaml:"idle"`
	OrbDefaults  OrbDefaults `yaml:"orb_defaults"`
	Push         Push        `yaml:"push"`
	Coordinator  Coordinator `yaml:"coordinator"`
	Schedule     Schedule    `yaml:"schedule"`
	Stats        Stats       `yaml:"stats"`
	Snapshots    Snapshots   `yaml:"snapshots"`
	Pool         Pool        `yaml:"pool"`
	OrbTLS       OrbTLS      `yaml:"orb_tls"`
	GitHub       GitHub      `yaml:"github"`
	Artifacts    Artifacts   `yaml:"artifacts"`
	Repos        Repos       `yaml:"repos"`
}

// CoordinatorDir 返回协调宿主目录（未配置时为 <data_dir>/coordinator）。
func (c Orynd) CoordinatorDir() string {
	if c.Coordinator.Dir != "" {
		return c.Coordinator.Dir
	}
	return filepath.Join(c.DataDir, "coordinator")
}

// ModelCatalogDir 返回中央模型目录的缓存目录（<data_dir>/model-catalog）。
func (c Orynd) ModelCatalogDir() string { return filepath.Join(c.DataDir, "model-catalog") }

// ArtifactRenderDir 返回 artifact 渲染作业目录的上级（未配置时为 <data_dir>/artifacts/render）。
func (c Orynd) ArtifactRenderDir() string {
	if c.Artifacts.Render.Dir != "" {
		return c.Artifacts.Render.Dir
	}
	return filepath.Join(c.DataDir, "artifacts", "render")
}

// ReposDir 返回项目仓库裸仓库的目录（<data_dir>/repos）。
func (c Orynd) ReposDir() string { return filepath.Join(c.DataDir, "repos") }

// DefaultOrynd 返回带默认值的配置。
func DefaultOrynd() Orynd {
	return Orynd{
		Listen:   []string{"127.0.0.1:7600"},
		DataDir:  "/var/lib/oryn",
		LogLevel: "info",
		Idle: Idle{
			AgentIdle:       Duration(5 * time.Minute),
			UserIdle:        Duration(20 * time.Minute),
			StopAfterFrozen: Duration(24 * time.Hour),
			WakeOnFocus:     true,
		},
		Coordinator: Coordinator{OmpPath: "/usr/local/lib/oryn/omp", EgressPort: 7811},
		Schedule:    Schedule{DeliverTimeout: Duration(10 * time.Minute), MaxFailures: 3},
		Stats: Stats{Retention: StatsRetention{
			Samples: Duration(720 * time.Hour),
			Events:  Duration(8760 * time.Hour),
		}},
		Snapshots: Snapshots{
			Enabled:    true,
			MaxAge:     Duration(72 * time.Hour),
			MaxDepth:   16,
			MinSetup:   Duration(10 * time.Second),
			MaxTotalGB: 40,
		},
		Pool:   Pool{MaxTotal: 2, MaxAge: Duration(12 * time.Hour), ActiveWindow: Duration(72 * time.Hour)},
		GitHub: GitHub{APIURL: "https://api.github.com", WebURL: "https://github.com"},
		Push:   Push{Gateway: Gateway{URL: DefaultPushGateway}},
	}
}

// DefaultPushGateway 是官方推送网关的 origin（push.gateway.url 的缺省值）。
const DefaultPushGateway = "https://push.oryn.best"

// LoadOrynd 读取文件，未出现的键保持默认值；data_dir 相对路径按当前工作目录解析。
func LoadOrynd(path string) (Orynd, error) {
	c := DefaultOrynd()
	if err := decodeStrict(path, &c); err != nil {
		return c, err
	}
	c.GitHub.APIURL = strings.TrimRight(c.GitHub.APIURL, "/")
	c.GitHub.WebURL = strings.TrimRight(c.GitHub.WebURL, "/")
	if err := c.Validate(); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return c, err
	}
	c.DataDir = abs
	if c.Coordinator.OmpPath, err = filepath.Abs(c.Coordinator.OmpPath); err != nil {
		return c, err
	}
	if c.Coordinator.Dir != "" {
		if c.Coordinator.Dir, err = filepath.Abs(c.Coordinator.Dir); err != nil {
			return c, err
		}
	}
	if c.Artifacts.Render.Dir != "" {
		if c.Artifacts.Render.Dir, err = filepath.Abs(c.Artifacts.Render.Dir); err != nil {
			return c, err
		}
	}
	// chromium 写的是不带斜杠的名字时按 PATH 查找，不转绝对路径
	if p := c.Artifacts.Render.Chromium; strings.Contains(p, "/") {
		if c.Artifacts.Render.Chromium, err = filepath.Abs(p); err != nil {
			return c, err
		}
	}
	// repos.git 同理：不带斜杠的名字按 PATH 查找
	if p := c.Repos.Git; strings.Contains(p, "/") {
		if c.Repos.Git, err = filepath.Abs(p); err != nil {
			return c, err
		}
	}
	return c, nil
}

// Validate 校验字段取值。
func (c Orynd) Validate() error {
	var errs []error
	if len(c.Listen) == 0 {
		errs = append(errs, errors.New("listen 不能为空"))
	}
	for _, l := range c.Listen {
		if _, _, err := net.SplitHostPort(l); err != nil {
			errs = append(errs, fmt.Errorf("listen %q: %w", l, err))
		}
	}
	if u, err := url.Parse(c.PublicURL); err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, fmt.Errorf("public_url %q 必须是绝对 URL", c.PublicURL))
	} else if err := validatePortalDomain(c.PortalDomain, u); err != nil {
		errs = append(errs, err)
	}
	if c.DataDir == "" {
		errs = append(errs, errors.New("data_dir 不能为空"))
	}
	errs = append(errs, c.GitHub.validate()...)
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log_level %q 无效", c.LogLevel))
	}
	if c.Idle.AgentIdle <= 0 || c.Idle.UserIdle <= 0 || c.Idle.StopAfterFrozen <= 0 {
		errs = append(errs, errors.New("idle 的各项时长必须为正"))
	}
	if err := validatePushGatewayURL(c.Push.Gateway.URL); err != nil {
		errs = append(errs, err)
	}
	if p := c.Coordinator.EgressPort; p < 1 || p > 65535 {
		errs = append(errs, fmt.Errorf("coordinator.egress_port %d 必须在 1–65535 之间", p))
	}
	if m := c.Coordinator.Model; m != "" && !strings.Contains(m, "/") {
		errs = append(errs, fmt.Errorf("coordinator.model %q 必须是 provider/id", m))
	}
	if c.Coordinator.OmpPath == "" {
		errs = append(errs, errors.New("coordinator.omp_path 不能为空"))
	}
	if c.OrbDefaults.Provider == "@host" {
		errs = append(errs, errors.New(`orb_defaults.provider 不能是保留的 "@host"`))
	}
	if c.Schedule.MaxFailures < 1 {
		errs = append(errs, errors.New("schedule.max_failures 至少为 1"))
	}
	if c.Schedule.DeliverTimeout.D() < time.Minute {
		errs = append(errs, errors.New("schedule.deliver_timeout 至少为 1m"))
	}
	for _, r := range []struct {
		key string
		d   Duration
	}{
		{"stats.retention.samples", c.Stats.Retention.Samples},
		{"stats.retention.events", c.Stats.Retention.Events},
		{"stats.retention.usage", c.Stats.Retention.Usage},
	} {
		if r.d != 0 && r.d.D() < 24*time.Hour {
			errs = append(errs, fmt.Errorf("%s 必须为 0（永久）或至少 24h", r.key))
		}
	}
	if c.Snapshots.MaxAge <= 0 || c.Snapshots.MinSetup <= 0 {
		errs = append(errs, errors.New("snapshots.max_age 与 snapshots.min_setup 必须为正"))
	}
	if d := c.Snapshots.MaxDepth; d < 1 || d > 64 {
		errs = append(errs, fmt.Errorf("snapshots.max_depth %d 必须在 1–64 之间", d))
	}
	if c.Snapshots.MaxTotalGB < 1 {
		errs = append(errs, fmt.Errorf("snapshots.max_total_gb %d 至少为 1", c.Snapshots.MaxTotalGB))
	}
	if g := c.Snapshots.MaxSnapshotGB; g < 0 || g > max(c.Snapshots.MaxTotalGB, 1) {
		errs = append(errs, fmt.Errorf("snapshots.max_snapshot_gb %d 必须在 1 与 snapshots.max_total_gb（%d）之间（0 = 自动）", g, c.Snapshots.MaxTotalGB))
	}
	if n := c.Pool.MaxTotal; n < 0 || n > 16 {
		errs = append(errs, fmt.Errorf("pool.max_total %d 必须在 0–16 之间（0 = 关闭预热池）", n))
	}
	if c.Pool.MaxAge <= 0 || c.Pool.ActiveWindow <= 0 {
		errs = append(errs, errors.New("pool.max_age 与 pool.active_window 必须为正"))
	}
	errs = append(errs, c.OrbTLS.validate()...)
	if n := c.Artifacts.Render.Concurrency; n < 0 || n > 16 {
		errs = append(errs, fmt.Errorf("artifacts.render.concurrency %d 必须在 0–16 之间（0 = 2）", n))
	}
	if c.Artifacts.Render.User == "root" {
		errs = append(errs, errors.New("artifacts.render.user 不能是 root"))
	}
	if c.Repos.MaxMirrorGB < 0 {
		errs = append(errs, errors.New("repos.max_mirror_gb 不能为负"))
	}
	if c.Repos.MinFreeGB < 0 {
		errs = append(errs, errors.New("repos.min_free_gb 不能为负"))
	}
	if c.Repos.MaxLFSCacheGB < 0 {
		errs = append(errs, errors.New("repos.max_lfs_cache_gb 不能为负"))
	}
	return errors.Join(errs...)
}

// validatePushGatewayURL 校验 push.gateway.url：空串 = 关闭；否则必须是不带路径、查询串与 userinfo 的 origin，
// 且为 https，或回环主机（localhost / 回环 IP）的 http。
func validatePushGatewayURL(raw string) error {
	if raw == "" {
		return nil
	}
	bad := fmt.Errorf("push.gateway.url %q 必须是不带路径的 origin（https，或回环主机的 http）", raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.HasSuffix(raw, "/") || u.String() != raw {
		return bad
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return bad
}

// portalDomainRe 是 portal_domain 允许的形态：小写主机名，不带端口与尾点。
var portalDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// validatePortalDomain 校验 portal_domain（空 = 不启用 portal）。public_url 的主机名不得是
// `<单标签>.<portal_domain>`，否则主域名的请求会被当成 portal 主机分流；主域名等于 portal_domain 本身是允许的
// （开发：localhost + *.localhost）。
func validatePortalDomain(domain string, public *url.URL) error {
	if domain == "" {
		return nil
	}
	if !portalDomainRe.MatchString(domain) {
		return fmt.Errorf("portal_domain %q 必须是小写主机名（如 orb.example.com）", domain)
	}
	host := strings.TrimSuffix(strings.ToLower(public.Hostname()), ".")
	if label, ok := strings.CutSuffix(host, "."+domain); ok && label != "" && !strings.Contains(label, ".") {
		return fmt.Errorf("public_url 的主机名 %q 落在 portal_domain %q 的单级子域上，会被当成 portal 主机", host, domain)
	}
	return nil
}

// DockerType 是一种 orb 规格。
type DockerType struct {
	CPUs   float64 `yaml:"cpus"`
	Memory string  `yaml:"memory"`
	// Runtime 是 `docker run --runtime` 的值（M11a）；只接受空（Docker 默认）或 sysbox-runc。
	Runtime string `yaml:"runtime"`
	// NestedDocker 为 true 时该类型的 orb 内由 orbd 监督 dockerd（M11a）；要求 Runtime 为 sysbox-runc。
	NestedDocker bool `yaml:"nested_docker"`
}

// SysboxRuntime 是嵌套 Docker 唯一支持的容器运行时。
const SysboxRuntime = "sysbox-runc"

// validateDockerTypes 校验各类型的 runtime / nested_docker（M11a）。
func validateDockerTypes(types map[string]DockerType) []error {
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	slices.Sort(names)
	var errs []error
	for _, name := range names {
		t := types[name]
		if t.Runtime != "" && t.Runtime != SysboxRuntime {
			errs = append(errs, fmt.Errorf("docker.types.%s.runtime %q 无效：只接受空或 %s", name, t.Runtime, SysboxRuntime))
		}
		if t.NestedDocker && t.Runtime != SysboxRuntime {
			errs = append(errs, fmt.Errorf("docker.types.%s：nested_docker 需要 runtime: %s；特权模式不受支持，见 docs/deploy.md", name, SysboxRuntime))
		}
	}
	return errs
}

// Docker 是 docker 驱动配置。
type Docker struct {
	Image        string                `yaml:"image"`
	ExtraRunArgs []string              `yaml:"extra_run_args"`
	Types        map[string]DockerType `yaml:"types"`
	DefaultType  string                `yaml:"default_type"`
	// PidsLimit 是所有类型的 `docker run --pids-limit`（M11a 审查修复）；缺省 4096，0 = 不限。
	PidsLimit int `yaml:"pids_limit"`
}

// DefaultPidsLimit 是 docker.pids_limit 的缺省值。
const DefaultPidsLimit = 4096

// Dispatch 是 dispatch 驱动配置。
type Dispatch struct {
	Script string `yaml:"script"`
}

// Provider 对应 /etc/oryn/provider.yaml。
type Provider struct {
	URL         string      `yaml:"url"`
	TokenFile   string      `yaml:"token_file"`
	OrbURL      string      `yaml:"orb_url"`
	Driver      string      `yaml:"driver"`
	Docker      Docker      `yaml:"docker"`
	Dispatch    Dispatch    `yaml:"dispatch"`
	Firecracker Firecracker `yaml:"firecracker"`
}

// DefaultDockerTypes 是默认 orb 规格。
func DefaultDockerTypes() map[string]DockerType {
	return map[string]DockerType{
		"small":  {CPUs: 2, Memory: "4g"},
		"medium": {CPUs: 4, Memory: "8g"},
		"large":  {CPUs: 8, Memory: "16g"},
	}
}

// LoadProvider 读取 provider 配置并补默认值。
func LoadProvider(path string) (Provider, error) {
	c := Provider{
		Driver:      "docker",
		Docker:      Docker{Image: "oryn-orb:dev", DefaultType: "medium", PidsLimit: DefaultPidsLimit},
		Firecracker: DefaultFirecracker(),
	}
	if err := decodeStrict(path, &c); err != nil {
		return c, err
	}
	if len(c.Docker.Types) == 0 {
		c.Docker.Types = DefaultDockerTypes()
	}
	if len(c.Firecracker.Types) == 0 {
		c.Firecracker.Types = DefaultFirecrackerTypes()
	}
	if c.OrbURL == "" {
		c.OrbURL = c.URL
	}
	var errs []error
	if u, err := url.Parse(c.URL); err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, fmt.Errorf("url %q 必须是绝对 URL", c.URL))
	}
	if c.TokenFile == "" {
		errs = append(errs, errors.New("token_file 不能为空"))
	}
	errs = append(errs, validateDockerTypes(c.Docker.Types)...)
	if c.Docker.PidsLimit < 0 {
		errs = append(errs, fmt.Errorf("docker.pids_limit %d 不能为负（0 = 不限）", c.Docker.PidsLimit))
	}
	switch c.Driver {
	case "docker":
		if _, ok := c.Docker.Types[c.Docker.DefaultType]; !ok {
			errs = append(errs, fmt.Errorf("docker.default_type %q 不在 types 中", c.Docker.DefaultType))
		}
	case "dispatch":
		if c.Dispatch.Script == "" {
			errs = append(errs, errors.New("driver=dispatch 时 dispatch.script 必填"))
		}
	case "process":
	case "firecracker":
		errs = append(errs, c.Firecracker.validate()...)
	case "runner":
		errs = append(errs, errors.New(`driver "runner" 无效：runner 不经 provider 进程，在那台机器上以 orbd run --runner 接入`))
	default:
		errs = append(errs, fmt.Errorf("driver %q 无效", c.Driver))
	}
	if err := errors.Join(errs...); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func decodeStrict(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
