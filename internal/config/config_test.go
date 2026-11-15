package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePortalDomain(t *testing.T) {
	cases := []struct {
		public, domain string
		ok             bool
	}{
		{"https://oryn.example.com", "", true},
		{"https://oryn.example.com", "orb.example.com", true},
		{"http://localhost:7600", "localhost", true},             // 主域名等于 portal_domain 本身：允许
		{"https://example.com", "example.com", true},             // 同上
		{"https://a.b.orb.example.com", "orb.example.com", true}, // 多级子域不会被当成 portal 主机
		{"https://oryn.orb.example.com", "orb.example.com", false},
		{"https://ORYN.orb.example.com.", "orb.example.com", false},
		{"https://oryn.example.com", "Orb.example.com", false},
		{"https://oryn.example.com", "orb.example.com.", false},
		{"https://oryn.example.com", "orb.example.com:443", false},
		{"https://oryn.example.com", "-orb.example.com", false},
		{"https://oryn.example.com", "*.orb.example.com", false},
	}
	for _, c := range cases {
		cfg := DefaultOrynd()
		cfg.PublicURL = c.public
		cfg.PortalDomain = c.domain
		err := cfg.Validate()
		if c.ok {
			assert.NoError(t, err, "%s + %s", c.public, c.domain)
		} else {
			assert.Error(t, err, "%s + %s", c.public, c.domain)
		}
	}
}

func TestValidateFCMSecretName(t *testing.T) {
	for name, ok := range map[string]bool{
		"":                    true, // 未配置 = 关闭 FCM
		"fcm/service-account": true,
		"FCM_SA.json":         true,
		"/fcm":                false,
		"fcm sa":              false,
		"fcm\n":               false,
	} {
		cfg := DefaultOrynd()
		cfg.PublicURL = "https://oryn.example.com"
		cfg.Push.FCM.ServiceAccountSecret = name
		if ok {
			assert.NoError(t, cfg.Validate(), "%q", name)
		} else {
			assert.Error(t, cfg.Validate(), "%q", name)
		}
	}
}

// M9：协调宿主与定时的校验、默认值与路径解析。
func TestValidateCoordinatorAndSchedule(t *testing.T) {
	base := func() Orynd {
		c := DefaultOrynd()
		c.PublicURL = "https://oryn.example.com"
		return c
	}
	assert.NoError(t, base().Validate())
	for name, mod := range map[string]func(*Orynd){
		"端口为 0":           func(c *Orynd) { c.Coordinator.EgressPort = 0 },
		"端口越界":            func(c *Orynd) { c.Coordinator.EgressPort = 65536 },
		"model 不含 /":      func(c *Orynd) { c.Coordinator.Model = "sonnet" },
		"omp_path 为空":     func(c *Orynd) { c.Coordinator.OmpPath = "" },
		"默认 provider 保留名": func(c *Orynd) { c.OrbDefaults.Provider = "@host" },
		"失败上限 < 1":        func(c *Orynd) { c.Schedule.MaxFailures = 0 },
		"送达超时 < 1m":       func(c *Orynd) { c.Schedule.DeliverTimeout = Duration(30 * time.Second) },
	} {
		c := base()
		mod(&c)
		assert.Error(t, c.Validate(), name)
	}
	c := base()
	c.Coordinator.Model = "anthropic/claude-sonnet"
	assert.NoError(t, c.Validate())
	assert.True(t, c.Coordinator.Verify(), "verify_omp 缺省为真")
	no := false
	c.Coordinator.VerifyOmp = &no
	assert.False(t, c.Coordinator.Verify())
	c.DataDir = "/srv/oryn"
	assert.Equal(t, "/srv/oryn/coordinator", c.CoordinatorDir())
	c.Coordinator.Dir = "/x/coord"
	assert.Equal(t, "/x/coord", c.CoordinatorDir())
}

func TestLoadOryndCoordinatorPaths(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "orynd.yaml")
	require.NoError(t, os.WriteFile(path, []byte("public_url: https://oryn.example.com\ndata_dir: data\ncoordinator:\n  omp_path: bin/omp\n  dir: coord\n  egress_port: 7999\n  verify_omp: false\n"), 0o600))
	c, err := LoadOrynd(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "bin/omp"), c.Coordinator.OmpPath)
	assert.Equal(t, filepath.Join(dir, "coord"), c.Coordinator.Dir)
	assert.Equal(t, 7999, c.Coordinator.EgressPort)
	assert.Equal(t, "", c.Coordinator.User, "未写的键保持默认：空 = root 部署时自动使用本实例的专用账号")
	assert.False(t, c.Coordinator.Verify())
	assert.Equal(t, 3, c.Schedule.MaxFailures)
}

// M10：stats.retention 的默认值、校验与 YAML 读取。
func TestStatsRetention(t *testing.T) {
	d := DefaultOrynd()
	assert.Equal(t, 720*time.Hour, d.Stats.Retention.Samples.D())
	assert.Equal(t, 8760*time.Hour, d.Stats.Retention.Events.D())
	assert.Zero(t, d.Stats.Retention.Usage.D(), "用量默认永久")

	base := func() Orynd {
		c := DefaultOrynd()
		c.PublicURL = "https://oryn.example.com"
		return c
	}
	for name, mod := range map[string]func(*Orynd){
		"samples < 24h": func(c *Orynd) { c.Stats.Retention.Samples = Duration(23 * time.Hour) },
		"events < 24h":  func(c *Orynd) { c.Stats.Retention.Events = Duration(time.Hour) },
		"usage 为负":      func(c *Orynd) { c.Stats.Retention.Usage = Duration(-48 * time.Hour) },
	} {
		c := base()
		mod(&c)
		assert.Error(t, c.Validate(), name)
	}
	c := base()
	c.Stats.Retention = StatsRetention{Samples: 0, Events: Duration(24 * time.Hour), Usage: Duration(90 * 24 * time.Hour)}
	assert.NoError(t, c.Validate(), "0 = 永久；恰好 24h 合法")

	dir := t.TempDir()
	path := filepath.Join(dir, "orynd.yaml")
	require.NoError(t, os.WriteFile(path, []byte("public_url: https://oryn.example.com\ndata_dir: "+dir+"\nstats:\n  retention:\n    samples: 48h\n    usage: 2160h\n"), 0o600))
	got, err := LoadOrynd(path)
	require.NoError(t, err)
	assert.Equal(t, 48*time.Hour, got.Stats.Retention.Samples.D())
	assert.Equal(t, 8760*time.Hour, got.Stats.Retention.Events.D(), "未写的键保持默认")
	assert.Equal(t, 2160*time.Hour, got.Stats.Retention.Usage.D())

	require.NoError(t, os.WriteFile(path, []byte("public_url: https://oryn.example.com\ndata_dir: "+dir+"\nstats:\n  retention:\n    samples: 1h\n"), 0o600))
	_, err = LoadOrynd(path)
	assert.ErrorContains(t, err, "stats.retention.samples")
}

// M11a：snapshots / pool 的默认值、校验与 YAML 读取（未写的键保持默认）。
func TestSnapshotsAndPool(t *testing.T) {
	d := DefaultOrynd()
	assert.Equal(t, Snapshots{Enabled: true, MaxAge: Duration(72 * time.Hour), MaxDepth: 16, MinSetup: Duration(10 * time.Second), MaxTotalGB: 40}, d.Snapshots,
		"max_snapshot_gb 缺省 0 = 未设置")
	assert.Equal(t, Pool{MaxTotal: 2, MaxAge: Duration(12 * time.Hour), ActiveWindow: Duration(72 * time.Hour)}, d.Pool)
	assert.Equal(t, int64(40)<<30, d.Snapshots.MaxTotalBytes())
	assert.Equal(t, int64(10)<<30, d.Snapshots.MaxSnapshotBytes(), "未设置时取 min(10, max_total_gb)")
	assert.Equal(t, int64(3)<<30, Snapshots{MaxTotalGB: 3}.MaxSnapshotBytes(), "max_total_gb 小于 10 时取 max_total_gb")
	assert.Equal(t, int64(7)<<30, Snapshots{MaxTotalGB: 3, MaxSnapshotGB: 7}.MaxSnapshotBytes(), "显式值原样换算")

	base := func() Orynd {
		c := DefaultOrynd()
		c.PublicURL = "https://oryn.example.com"
		return c
	}
	for name, mod := range map[string]func(*Orynd){
		"snapshots.max_age 为 0":                 func(c *Orynd) { c.Snapshots.MaxAge = 0 },
		"snapshots.min_setup 为负":                func(c *Orynd) { c.Snapshots.MinSetup = Duration(-time.Second) },
		"snapshots.max_depth 为 0":               func(c *Orynd) { c.Snapshots.MaxDepth = 0 },
		"snapshots.max_depth 超 64":              func(c *Orynd) { c.Snapshots.MaxDepth = 65 },
		"snapshots.max_total_gb 为 0":            func(c *Orynd) { c.Snapshots.MaxTotalGB = 0 },
		"snapshots.max_snapshot_gb 为负":          func(c *Orynd) { c.Snapshots.MaxSnapshotGB = -1 },
		"snapshots.max_snapshot_gb 超 max_total": func(c *Orynd) { c.Snapshots.MaxSnapshotGB = 41 },
		"pool.max_total 为负":                     func(c *Orynd) { c.Pool.MaxTotal = -1 },
		"pool.max_total 超 16":                   func(c *Orynd) { c.Pool.MaxTotal = 17 },
		"pool.max_age 为 0":                      func(c *Orynd) { c.Pool.MaxAge = 0 },
		"pool.active_window 为负":                 func(c *Orynd) { c.Pool.ActiveWindow = Duration(-time.Hour) },
	} {
		c := base()
		mod(&c)
		assert.Error(t, c.Validate(), name)
	}
	c := base()
	c.Snapshots.MaxDepth, c.Pool.MaxTotal = 64, 0
	assert.NoError(t, c.Validate(), "max_depth = 64 与 pool.max_total = 0（关闭预热池）合法")
	c.Snapshots.MaxDepth, c.Pool.MaxTotal = 1, 16
	assert.NoError(t, c.Validate(), "边界值合法")
	c.Snapshots.MaxSnapshotGB = 0
	assert.NoError(t, c.Validate(), "显式 max_snapshot_gb = 0 视为未设置")
	c.Snapshots.MaxSnapshotGB = 1
	assert.NoError(t, c.Validate(), "max_snapshot_gb = 1 合法")
	c.Snapshots.MaxSnapshotGB = c.Snapshots.MaxTotalGB
	assert.NoError(t, c.Validate(), "max_snapshot_gb = max_total_gb 合法")

	dir := t.TempDir()
	path := filepath.Join(dir, "orynd.yaml")
	require.NoError(t, os.WriteFile(path, []byte("public_url: https://oryn.example.com\ndata_dir: "+dir+
		"\nsnapshots: {min_setup: 1s, enabled: false}\npool: {max_total: 0}\n"), 0o600))
	got, err := LoadOrynd(path)
	require.NoError(t, err)
	assert.False(t, got.Snapshots.Enabled)
	assert.Equal(t, time.Second, got.Snapshots.MinSetup.D())
	assert.Equal(t, 72*time.Hour, got.Snapshots.MaxAge.D(), "未写的键保持默认")
	assert.Equal(t, 16, got.Snapshots.MaxDepth)
	assert.Equal(t, 0, got.Pool.MaxTotal)
	assert.Equal(t, 12*time.Hour, got.Pool.MaxAge.D())
	assert.Equal(t, 0, got.Snapshots.MaxSnapshotGB, "未写的键保持默认（0 = 未设置）")

	// X9 回归：只写了小于 10 的 max_total_gb 的旧配置照常加载，单快照上限随之取 max_total_gb。
	require.NoError(t, os.WriteFile(path, []byte("public_url: https://oryn.example.com\ndata_dir: "+dir+"\nsnapshots: {max_total_gb: 1}\n"), 0o600))
	got, err = LoadOrynd(path)
	require.NoError(t, err)
	assert.Equal(t, int64(1)<<30, got.Snapshots.MaxSnapshotBytes())
	require.NoError(t, os.WriteFile(path, []byte("public_url: https://oryn.example.com\ndata_dir: "+dir+"\nsnapshots: {max_snapshot_gb: 0}\n"), 0o600))
	got, err = LoadOrynd(path)
	require.NoError(t, err, "显式 0 视为自动")
	assert.Equal(t, int64(10)<<30, got.Snapshots.MaxSnapshotBytes())

	for body, want := range map[string]string{
		"snapshots: {max_depth: 100}\n":                        "snapshots.max_depth",
		"snapshots: {max_total_gb: 0}\n":                       "snapshots.max_total_gb",
		"snapshots: {max_snapshot_gb: 41}\n":                   "snapshots.max_snapshot_gb", // 大于默认 max_total_gb 40
		"snapshots: {max_snapshot_gb: -1}\n":                   "snapshots.max_snapshot_gb",
		"snapshots: {max_total_gb: 20, max_snapshot_gb: 21}\n": "snapshots.max_snapshot_gb",
		"pool: {max_total: 17}\n":                              "pool.max_total",
		"pool: {active_window: 0s}\n":                          "pool.active_window",
		"snapshots: {max_age_hours: 1}\n":                      "max_age_hours", // 严格解码：未知键报错
	} {
		require.NoError(t, os.WriteFile(path, []byte("public_url: https://oryn.example.com\ndata_dir: "+dir+"\n"+body), 0o600))
		_, err = LoadOrynd(path)
		assert.ErrorContains(t, err, want, body)
	}
}

// M11a 审查修复：orb_tls 的默认值、校验与 YAML 读取。
func TestOrbTLS(t *testing.T) {
	d := DefaultOrynd()
	assert.Empty(t, d.OrbTLS.Listen, "缺省不开 TLS 监听")
	assert.Empty(t, d.OrbTLS.Hosts)

	base := func() Orynd {
		c := DefaultOrynd()
		c.PublicURL = "https://oryn.example.com"
		return c
	}
	for name, tc := range map[string]OrbTLS{
		"listen 无端口":   {Listen: []string{"172.18.0.1"}},
		"listen 主机名":   {Listen: []string{"localhost:7643"}},
		"listen 空主机":   {Listen: []string{":7643"}},
		"listen 端口非数字": {Listen: []string{"172.18.0.1:https"}},
		"listen 端口为 0": {Listen: []string{"172.18.0.1:0"}},
		"listen 端口超范围": {Listen: []string{"172.18.0.1:70000"}},
		"hosts 空串":     {Listen: []string{"172.18.0.1:7643"}, Hosts: []string{""}},
		"hosts 带端口":    {Hosts: []string{"oryn.example.com:443"}},
		"hosts 带通配":    {Hosts: []string{"*.example.com"}},
	} {
		c := base()
		c.OrbTLS = tc
		assert.ErrorContains(t, c.Validate(), "orb_tls", name)
	}
	c := base()
	c.OrbTLS = OrbTLS{
		Listen: []string{"172.18.0.1:7643", "[::1]:7643", "172.18.0.1:7644"},
		Hosts:  []string{"oryn.example.com", "10.0.0.5", "host.docker.internal"},
	}
	require.NoError(t, c.Validate())
	ips := c.OrbTLS.ListenIPs()
	require.Len(t, ips, 2, "同一 IP 只出现一次")
	assert.Equal(t, "172.18.0.1", ips[0].String())
	assert.Equal(t, "::1", ips[1].String())

	dir := t.TempDir()
	path := filepath.Join(dir, "orynd.yaml")
	require.NoError(t, os.WriteFile(path, []byte("public_url: https://oryn.example.com\ndata_dir: "+dir+
		"\norb_tls: {listen: [\"172.18.0.1:7643\"], hosts: [oryn.example.com]}\n"), 0o600))
	got, err := LoadOrynd(path)
	require.NoError(t, err)
	assert.Equal(t, OrbTLS{Listen: []string{"172.18.0.1:7643"}, Hosts: []string{"oryn.example.com"}}, got.OrbTLS)

	for body, want := range map[string]string{
		"orb_tls: {listen: [\"example.com:7643\"]}\n": "主机部分必须是 IP",
		"orb_tls: {listen: [\"172.18.0.1\"]}\n":       "orb_tls.listen",
		"orb_tls: {sans: [a]}\n":                      "sans", // 严格解码：未知键报错
	} {
		require.NoError(t, os.WriteFile(path, []byte("public_url: https://oryn.example.com\ndata_dir: "+dir+"\n"+body), 0o600))
		_, err = LoadOrynd(path)
		assert.ErrorContains(t, err, want, body)
	}
}

// M11a：docker.types.<name>.runtime / nested_docker 的读取与校验。
func TestDockerTypeRuntime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider.yaml")
	load := func(types string) (Provider, error) {
		require.NoError(t, os.WriteFile(path, []byte("url: http://127.0.0.1:7600\ntoken_file: /tmp/t\ndocker:\n  default_type: small\n  types:\n"+types), 0o600))
		return LoadProvider(path)
	}

	c, err := load("    small: {cpus: 2, memory: 4g}\n    dind: {cpus: 4, memory: 8g, runtime: sysbox-runc, nested_docker: true}\n    sys: {cpus: 2, memory: 4g, runtime: sysbox-runc}\n")
	require.NoError(t, err)
	assert.Equal(t, DockerType{CPUs: 4, Memory: "8g", Runtime: SysboxRuntime, NestedDocker: true}, c.Docker.Types["dind"])
	assert.Equal(t, DockerType{CPUs: 2, Memory: "4g", Runtime: SysboxRuntime}, c.Docker.Types["sys"], "只换 runtime、不开嵌套 Docker 合法")
	assert.Equal(t, DockerType{CPUs: 2, Memory: "4g"}, c.Docker.Types["small"])

	_, err = load("    small: {cpus: 2, memory: 4g, nested_docker: true}\n")
	assert.ErrorContains(t, err, "nested_docker 需要 runtime: sysbox-runc")
	assert.ErrorContains(t, err, "docs/deploy.md")

	_, err = load("    small: {cpus: 2, memory: 4g, runtime: runc-foo}\n")
	assert.ErrorContains(t, err, `docker.types.small.runtime "runc-foo" 无效`)

	_, err = load("    small: {cpus: 2, memory: 4g, runtime: runc-foo, nested_docker: true}\n")
	assert.ErrorContains(t, err, "runtime")
	assert.ErrorContains(t, err, "nested_docker 需要")

	_, err = load("    small: {cpus: 2, memory: 4g, privileged: true}\n")
	assert.ErrorContains(t, err, "privileged", "严格解码：未知键报错")
}

// M11a 审查修复：docker.pids_limit 的默认值（4096）、0 = 不限、负数报错。
func TestDockerPidsLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider.yaml")
	load := func(extra string) (Provider, error) {
		require.NoError(t, os.WriteFile(path, []byte("url: http://127.0.0.1:7600\ntoken_file: /tmp/t\ndocker:\n  default_type: medium\n"+extra), 0o600))
		return LoadProvider(path)
	}

	c, err := load("")
	require.NoError(t, err)
	assert.Equal(t, DefaultPidsLimit, c.Docker.PidsLimit)
	assert.Equal(t, 4096, c.Docker.PidsLimit)

	c, err = load("  pids_limit: 0\n")
	require.NoError(t, err)
	assert.Equal(t, 0, c.Docker.PidsLimit, "显式 0 = 不限，不回填默认值")

	c, err = load("  pids_limit: 512\n")
	require.NoError(t, err)
	assert.Equal(t, 512, c.Docker.PidsLimit)

	_, err = load("  pids_limit: -1\n")
	assert.ErrorContains(t, err, "docker.pids_limit -1 不能为负")

	_, err = load("  pids_limit: many\n")
	assert.Error(t, err, "非整数报错")
}
