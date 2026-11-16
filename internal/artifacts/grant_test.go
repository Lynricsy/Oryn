package artifacts

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGrantRoundTrip(t *testing.T) {
	key := DeriveGrantKey([]byte("0123456789abcdef0123456789abcdef"))
	now := time.UnixMilli(1_700_000_000_000)
	g := MakeGrant(key, "slug", 3, now.Add(GrantTTL))
	assert.True(t, VerifyGrant(key, g, "slug", 3, now))
	assert.True(t, VerifyGrant(key, g, "slug", 3, now.Add(GrantTTL-time.Millisecond)))

	assert.False(t, VerifyGrant(key, g, "slug", 3, now.Add(GrantTTL)), "过期")
	assert.False(t, VerifyGrant(key, g, "slug", 4, now), "代数变了")
	assert.False(t, VerifyGrant(key, g, "other", 3, now), "别的 slug")
	assert.False(t, VerifyGrant(DeriveGrantKey([]byte("another master key")), g, "slug", 3, now), "别的密钥")
	assert.False(t, VerifyGrant(key, "", "slug", 3, now))
	assert.False(t, VerifyGrant(key, strings.ReplaceAll(g, ".", ""), "slug", 3, now))
}

// TestGrantTamperAnyChar：改动授权里的任何一个字符都校验失败。
func TestGrantTamperAnyChar(t *testing.T) {
	key := DeriveGrantKey([]byte("master"))
	now := time.UnixMilli(1_700_000_000_000)
	g := MakeGrant(key, "abcdefghijklmnopqrstuvwxyz", 0, now.Add(time.Hour))
	for i := range len(g) {
		b := []byte(g)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		assert.False(t, VerifyGrant(key, string(b), "abcdefghijklmnopqrstuvwxyz", 0, now), "第 %d 个字符被改", i)
	}
}

func TestDeriveGrantKey(t *testing.T) {
	master := []byte("0123456789abcdef0123456789abcdef")
	assert.Equal(t, DeriveGrantKey(master), DeriveGrantKey(master))
	assert.NotEqual(t, master, DeriveGrantKey(master))
	assert.Len(t, DeriveGrantKey(master), 32)
}
