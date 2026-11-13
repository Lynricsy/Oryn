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

// Orynd 对应 /etc/oryn/orynd.yaml。
type Orynd struct {
	Listen       []string    `yaml:"listen"`
	PublicURL    string      `yaml:"public_url"`
	PortalDomain string      `yaml:"portal_domain"`
	DataDir      string      `yaml:"data_dir"`
	LogLevel     string      `yaml:"log_level"`
	Idle         Idle        `yaml:"idle"`
	OrbDefaults  OrbDefaults `yaml:"orb_defaults"`
}

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
	}
}

// LoadOrynd 读取文件，未出现的键保持默认值；data_dir 相对路径按当前工作目录解析。
func LoadOrynd(path string) (Orynd, error) {
	c := DefaultOrynd()
	if err := decodeStrict(path, &c); err != nil {
		return c, err
	}
	if err := c.Validate(); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return c, err
	}
	c.DataDir = abs
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
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log_level %q 无效", c.LogLevel))
	}
	if c.Idle.AgentIdle <= 0 || c.Idle.UserIdle <= 0 || c.Idle.StopAfterFrozen <= 0 {
		errs = append(errs, errors.New("idle 的各项时长必须为正"))
	}
	return errors.Join(errs...)
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
}

// Docker 是 docker 驱动配置。
type Docker struct {
	Image        string                `yaml:"image"`
	ExtraRunArgs []string              `yaml:"extra_run_args"`
	Types        map[string]DockerType `yaml:"types"`
	DefaultType  string                `yaml:"default_type"`
}

// Dispatch 是 dispatch 驱动配置。
type Dispatch struct {
	Script string `yaml:"script"`
}

// Provider 对应 /etc/oryn/provider.yaml。
type Provider struct {
	URL       string   `yaml:"url"`
	TokenFile string   `yaml:"token_file"`
	OrbURL    string   `yaml:"orb_url"`
	Driver    string   `yaml:"driver"`
	Docker    Docker   `yaml:"docker"`
	Dispatch  Dispatch `yaml:"dispatch"`
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
	c := Provider{Driver: "docker", Docker: Docker{Image: "oryn-orb:dev", DefaultType: "medium"}}
	if err := decodeStrict(path, &c); err != nil {
		return c, err
	}
	if len(c.Docker.Types) == 0 {
		c.Docker.Types = DefaultDockerTypes()
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
