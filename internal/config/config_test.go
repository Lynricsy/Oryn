package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
