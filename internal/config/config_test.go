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
