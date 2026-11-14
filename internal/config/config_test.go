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
