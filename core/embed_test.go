package core

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPin(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	p := Pin()
	for _, k := range []string{"linux-x64", "linux-arm64"} {
		require.Contains(t, p, k)
		assert.Regexp(t, hex64, p[k])
	}
	// 返回副本：改动不影响下一次调用。
	p["linux-x64"] = "x"
	assert.NotEqual(t, "x", Pin()["linux-x64"])
}

func TestSource(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	r := Source()
	assert.Regexp(t, `^[^/\s]+/[^/\s]+$`, r.Repo)
	assert.Regexp(t, `^oryn-v`, r.Tag)
	for _, k := range []string{"linux-x64", "linux-arm64"} {
		require.Contains(t, r.SHA256, k)
		assert.Regexp(t, hex64, r.SHA256[k])
	}
	assert.Equal(t, Pin(), r.SHA256)
	// 返回副本：改动不影响下一次调用。
	r.SHA256["linux-x64"] = "x"
	assert.NotEqual(t, "x", Source().SHA256["linux-x64"])
}
