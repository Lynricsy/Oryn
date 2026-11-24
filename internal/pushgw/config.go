// Package pushgw 是官方推送网关（docs/protocol.md「官方推送网关」）：授权存储、签名接口、限流与 APNs / FCM 转发。
// 网关只转发 RFC 8291 密文，解不开载荷。
package pushgw

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/internal/apns"
)

// DefaultConfigPath 是 -config 的缺省值。
const DefaultConfigPath = "/etc/oryn-push-gateway/gateway.yaml"

// Config 是网关配置（YAML 严格解析）。上游地址不可配置：测试经 Options 覆盖。
type Config struct {
	Listen string `yaml:"listen"`
	// PublicOrigin 是客户端看到的网关 origin（签名输入的一部分）：https，或回环主机的 http；不带路径。
	PublicOrigin string `yaml:"public_origin"`
	DataDir      string `yaml:"data_dir"`
	LogLevel     string `yaml:"log_level"`
	APNs         APNs   `yaml:"apns"`
	FCM          FCM    `yaml:"fcm"`
	Limits       Limits `yaml:"limits"`
}

// APNs 是 APNs 通道配置；KeyFile 为空即不提供 APNs。
type APNs struct {
	// KeyFile 是 Apple Developer 下载的 AuthKey_<KeyID>.p8。
	KeyFile string `yaml:"key_file"`
	KeyID   string `yaml:"key_id"`
	TeamID  string `yaml:"team_id"`
	// Topic 是 App 的 bundle id，也是 challenge 里 appId 必须等于的值。
	Topic string `yaml:"topic"`
	// Environments 是接受的 APNs 环境（production / sandbox）；为空时两者都接受。
	Environments []string `yaml:"environments"`
}

// FCM 是 FCM 通道配置；ServiceAccountFile 为空即不提供 FCM。
type FCM struct {
	ServiceAccountFile string `yaml:"service_account_file"`
	// AppID 是 Android 应用标识（包名），challenge 里 appId 必须等于它。
	AppID string `yaml:"app_id"`
}

// Limits 是限额；缺省值见 DefaultConfig。
type Limits struct {
	GrantPerMinute      int `yaml:"grant_per_minute"`
	GrantPerDay         int `yaml:"grant_per_day"`
	HostPerMinute       int `yaml:"host_per_minute"`
	ChallengesPerIPHour int `yaml:"challenges_per_ip_hour"`
}

// Enabled 报告是否提供 APNs。
func (a APNs) Enabled() bool { return a.KeyFile != "" }

// Enabled 报告是否提供 FCM。
func (f FCM) Enabled() bool { return f.ServiceAccountFile != "" }

// DefaultConfig 是未写的键取的值。
func DefaultConfig() Config {
	return Config{
		Listen:   "127.0.0.1:7700",
		DataDir:  "/var/lib/oryn-push-gateway",
		LogLevel: "info",
		Limits:   Limits{GrantPerMinute: 60, GrantPerDay: 2000, HostPerMinute: 600, ChallengesPerIPHour: 30},
	}
}

// Load 读取并校验配置文件；data_dir 与密钥文件路径转为绝对路径。
func Load(path string) (Config, error) {
	c := DefaultConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	for _, p := range []*string{&c.DataDir, &c.APNs.KeyFile, &c.FCM.ServiceAccountFile} {
		if *p == "" {
			continue
		}
		if *p, err = filepath.Abs(*p); err != nil {
			return c, err
		}
	}
	return c, nil
}

// Validate 校验配置并规范化 public_origin（去尾斜杠）与 APNs 环境（为空时取两者）。
func (c *Config) Validate() error {
	var errs []error
	if c.Listen == "" {
		errs = append(errs, errors.New("listen 不能为空"))
	}
	c.PublicOrigin = strings.TrimRight(c.PublicOrigin, "/")
	if err := validOrigin(c.PublicOrigin); err != nil {
		errs = append(errs, fmt.Errorf("public_origin: %w", err))
	}
	if c.DataDir == "" {
		errs = append(errs, errors.New("data_dir 不能为空"))
	}
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(c.LogLevel)); err != nil {
		errs = append(errs, fmt.Errorf("log_level %q 无效（debug / info / warn / error）", c.LogLevel))
	}
	if c.APNs.Enabled() {
		if c.APNs.KeyID == "" || c.APNs.TeamID == "" || c.APNs.Topic == "" {
			errs = append(errs, errors.New("apns: 设置了 key_file 时 key_id、team_id、topic 必填"))
		}
		if len(c.APNs.Environments) == 0 {
			c.APNs.Environments = []string{apns.EnvProduction, apns.EnvSandbox}
		}
		for i, e := range c.APNs.Environments {
			if e != apns.EnvProduction && e != apns.EnvSandbox {
				errs = append(errs, fmt.Errorf("apns.environments: %q 不是 production / sandbox", e))
			} else if slices.Contains(c.APNs.Environments[:i], e) {
				errs = append(errs, fmt.Errorf("apns.environments: %q 重复", e))
			}
		}
	}
	if c.FCM.Enabled() && c.FCM.AppID == "" {
		errs = append(errs, errors.New("fcm: 设置了 service_account_file 时 app_id 必填"))
	}
	l := c.Limits
	if l.GrantPerMinute <= 0 || l.GrantPerDay <= 0 || l.HostPerMinute <= 0 || l.ChallengesPerIPHour <= 0 {
		errs = append(errs, errors.New("limits: 各项都必须是正整数"))
	}
	return errors.Join(errs...)
}

// validOrigin：scheme://host[:port]，https 或回环主机的 http，不带路径、查询与用户信息。
func validOrigin(s string) error {
	if s == "" {
		return errors.New("不能为空")
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.Opaque != "" {
		return fmt.Errorf("%q 不是 origin（scheme://host[:port]）", s)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		h := u.Hostname()
		if h == "localhost" {
			return nil
		}
		if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("%q：http 只允许回环主机", s)
	}
	return fmt.Errorf("%q：只能是 https，或回环主机的 http", s)
}
