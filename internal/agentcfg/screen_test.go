package agentcfg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScreenSecrets(t *testing.T) {
	files := map[string][]byte{
		"mcp.json":          []byte(`{"mcpServers":{"x":{"headers":{"Authorization":"Bearer sk-abcdefghijklmnopqrstuv"}}}}`),
		"config.yml":        []byte("note: hunter2-password-value\n"),
		"models.yml":        []byte("providers:\n  a:\n    apiKey: oryn-ph-sec_01JABCDEFGHJKMNPQRSTVWXYZ0\n"),
		"AGENTS.md":         []byte("plain text, short pin 1234\n"),
		"skills/bin/x.png":  {0x89, 'P', 'N', 'G', 0, 's', 'k', '-'},
		"rules/ulid-ref.md": []byte("hash 0123456789abcdef0123456789abcdef01234567\n"),
	}
	known := map[string]string{"db/password": "hunter2-password-value", "pin": "1234"}
	got := ScreenSecrets(files, known)
	require.Len(t, got, 2)
	assert.Equal(t, "config.yml", got[0].Path)
	assert.Equal(t, "包含密钥 db/password 的值", got[0].Reason)
	assert.Equal(t, "mcp.json", got[1].Path)
	assert.Contains(t, got[1].Reason, "形似密钥")
	for _, w := range got {
		assert.NotContains(t, w.Reason, "hunter2-password-value")
		assert.NotContains(t, w.Reason, "sk-abcdefghijklmnopqrstuv")
	}
	assert.NotContains(t, files, "mcp.json")
	assert.NotContains(t, files, "config.yml")
	for _, p := range []string{"models.yml", "AGENTS.md", "skills/bin/x.png", "rules/ulid-ref.md"} {
		assert.Contains(t, files, p, "干净文件、占位符、短值、二进制与纯十六进制照常下发")
	}
	assert.True(t, strings.HasPrefix(string(files["models.yml"]), "providers:"))
}
