package portalhost

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCode(t *testing.T) {
	cases := []struct {
		id   string
		code string
		ok   bool
	}{
		{"T-01932f6e-8d1a-7c3b-9f2e-3f9a1c0b7e2d", "3f9a1c0b7e2d", true},
		{"T-01932F6E-8d1a-7c3b-9f2e-3f9a1c0b7e2d", "", false}, // 大写
		{"T-01932f6e-8d1a-7c3b-9f2e-3f9a1c0b7e2", "", false},  // 末段 11 位
		{"01932f6e-8d1a-7c3b-9f2e-3f9a1c0b7e2d", "", false},
		{"T-abc", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		code, ok := Code(c.id)
		assert.Equal(t, c.ok, ok, c.id)
		assert.Equal(t, c.code, code, c.id)
	}
}

func TestLabelRoundTrip(t *testing.T) {
	assert.Equal(t, "t-3f9a1c0b7e2d-p3000", Label("3f9a1c0b7e2d", 3000))
	code, port, ok := ParseLabel(Label("3f9a1c0b7e2d", 65535))
	require.True(t, ok)
	assert.Equal(t, "3f9a1c0b7e2d", code)
	assert.Equal(t, 65535, port)
}

func TestParseLabel(t *testing.T) {
	cases := []struct {
		label string
		code  string
		port  int
		ok    bool
	}{
		{"t-3f9a1c0b7e2d-p1", "3f9a1c0b7e2d", 1, true},
		{"t-3f9a1c0b7e2d-p8080", "3f9a1c0b7e2d", 8080, true},
		{"t-3f9a1c0b7e2d-p65535", "3f9a1c0b7e2d", 65535, true},
		{"t-3f9a1c0b7e2d-p65536", "", 0, false},
		{"t-3f9a1c0b7e2d-p99999", "", 0, false},
		{"t-3f9a1c0b7e2d-p0", "", 0, false},
		{"t-3f9a1c0b7e2d-p080", "", 0, false},
		{"t-3f9a1c0b7e2d-p123456", "", 0, false},
		{"t-3F9A1C0B7E2D-p80", "", 0, false},
		{"t-3f9a1c0b7e2-p80", "", 0, false},
		{"t-3f9a1c0b7e2d-p", "", 0, false},
		{"x-3f9a1c0b7e2d-p80", "", 0, false},
		{"t-3f9a1c0b7e2d-p80.extra", "", 0, false},
		{"", "", 0, false},
	}
	for _, c := range cases {
		code, port, ok := ParseLabel(c.label)
		assert.Equal(t, c.ok, ok, c.label)
		assert.Equal(t, c.code, code, c.label)
		assert.Equal(t, c.port, port, c.label)
	}
}

func TestExpand(t *testing.T) {
	tpl := "https://t-{code}-p{port}.orb.example.com/"
	assert.Equal(t, "https://t-3f9a1c0b7e2d-p8080.orb.example.com/", Expand(tpl, "3f9a1c0b7e2d", 8080))
	assert.Equal(t, "https://t-3f9a1c0b7e2d-p{port}.orb.example.com/", Expand(tpl, "3f9a1c0b7e2d", 0))
	assert.Equal(t, "", Expand("", "3f9a1c0b7e2d", 80))
}

func TestNewBase(t *testing.T) {
	cases := []struct {
		name, publicURL, domain string
		want                    Base
		enabled                 bool
		url, template, frameSrc string
	}{
		{
			name: "dev localhost", publicURL: "http://localhost:7600", domain: "localhost",
			want:     Base{Scheme: "http", Domain: "localhost", Port: "7600", Origin: "http://localhost:7600"},
			enabled:  true,
			url:      "http://t-3f9a1c0b7e2d-p8080.localhost:7600/",
			template: "http://t-{code}-p{port}.localhost:7600/",
			frameSrc: "http://*.localhost:7600",
		},
		{
			name: "prod", publicURL: "https://oryn.example.com", domain: "orb.example.com",
			want:     Base{Scheme: "https", Domain: "orb.example.com", Origin: "https://oryn.example.com"},
			enabled:  true,
			url:      "https://t-3f9a1c0b7e2d-p8080.orb.example.com/",
			template: "https://t-{code}-p{port}.orb.example.com/",
			frameSrc: "https://*.orb.example.com",
		},
		{
			name: "explicit default port, trailing path, upper-case domain", publicURL: "https://Oryn.Example.com:443/app/", domain: "ORB.example.com.",
			want:     Base{Scheme: "https", Domain: "orb.example.com", Origin: "https://oryn.example.com"},
			enabled:  true,
			url:      "https://t-3f9a1c0b7e2d-p8080.orb.example.com/",
			template: "https://t-{code}-p{port}.orb.example.com/",
			frameSrc: "https://*.orb.example.com",
		},
		{
			name: "non-default https port", publicURL: "https://oryn.example.com:8443", domain: "orb.example.com",
			want:     Base{Scheme: "https", Domain: "orb.example.com", Port: "8443", Origin: "https://oryn.example.com:8443"},
			enabled:  true,
			url:      "https://t-3f9a1c0b7e2d-p8080.orb.example.com:8443/",
			template: "https://t-{code}-p{port}.orb.example.com:8443/",
			frameSrc: "https://*.orb.example.com:8443",
		},
		{
			name: "disabled", publicURL: "http://localhost:7600", domain: "",
			want: Base{Scheme: "http", Port: "7600", Origin: "http://localhost:7600"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := NewBase(c.publicURL, c.domain)
			require.NoError(t, err)
			assert.Equal(t, c.want, b)
			assert.Equal(t, c.enabled, b.Enabled())
			assert.Equal(t, c.url, b.URL("3f9a1c0b7e2d", 8080))
			assert.Equal(t, c.template, b.Template())
			assert.Equal(t, c.frameSrc, b.FrameSrc())
			if c.enabled {
				assert.Equal(t, b.URL("3f9a1c0b7e2d", 8080), Expand(b.Template(), "3f9a1c0b7e2d", 8080))
			}
		})
	}
}

func TestNewBaseErrors(t *testing.T) {
	for _, c := range []struct{ publicURL, domain string }{
		{"ftp://oryn.example.com", "orb.example.com"},
		{"oryn.example.com", "orb.example.com"},
		{"https://", "orb.example.com"},
		{"://bad", ""},
		{"https://oryn.example.com", "orb.example.com:8080"},
		{"https://oryn.example.com", "*.orb.example.com"},
		{"https://oryn.example.com", "orb_example.com"},
	} {
		_, err := NewBase(c.publicURL, c.domain)
		assert.Error(t, err, "%s + %s", c.publicURL, c.domain)
	}
}

func TestMatch(t *testing.T) {
	dev, err := NewBase("http://localhost:7600", "localhost")
	require.NoError(t, err)
	prod, err := NewBase("https://oryn.example.com", "orb.example.com")
	require.NoError(t, err)
	off, err := NewBase("https://oryn.example.com", "")
	require.NoError(t, err)

	cases := []struct {
		name  string
		base  Base
		host  string
		label string
		ok    bool
	}{
		{"dev with port", dev, "t-3f9a1c0b7e2d-p8080.localhost:7600", "t-3f9a1c0b7e2d-p8080", true},
		{"dev other port ignored", dev, "t-3f9a1c0b7e2d-p8080.localhost:9999", "t-3f9a1c0b7e2d-p8080", true},
		{"dev main host", dev, "localhost:7600", "", false},
		{"dev bare main host", dev, "localhost", "", false},
		{"prod", prod, "t-3f9a1c0b7e2d-p8080.orb.example.com", "t-3f9a1c0b7e2d-p8080", true},
		{"prod upper-case and trailing dot", prod, "T-3F9A1C0B7E2D-P8080.ORB.example.com.", "t-3f9a1c0b7e2d-p8080", true},
		{"prod with port", prod, "anything.orb.example.com:443", "anything", true},
		{"prod main host", prod, "oryn.example.com", "", false},
		{"prod domain itself", prod, "orb.example.com", "", false},
		{"prod two labels", prod, "a.t-3f9a1c0b7e2d-p8080.orb.example.com", "", false},
		{"prod suffix without dot", prod, "evilorb.example.com", "", false},
		{"prod other domain", prod, "t-3f9a1c0b7e2d-p8080.orb.example.org", "", false},
		{"disabled", off, "t-3f9a1c0b7e2d-p8080.orb.example.com", "", false},
		{"empty", prod, "", "", false},
	}
	for _, c := range cases {
		label, ok := c.base.Match(c.host)
		assert.Equal(t, c.ok, ok, c.name)
		assert.Equal(t, c.label, label, c.name)
	}
}

func TestDisabledOutputsEmpty(t *testing.T) {
	b, err := NewBase("https://oryn.example.com", "")
	require.NoError(t, err)
	assert.False(t, b.Enabled())
	assert.Empty(t, b.URL("3f9a1c0b7e2d", 80))
	assert.Empty(t, b.Template())
	assert.Empty(t, b.FrameSrc())
	assert.Equal(t, "https://oryn.example.com", b.Origin)
}
