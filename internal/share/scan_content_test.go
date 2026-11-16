package share

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

type mapSecrets map[string]string

func (m mapSecrets) All(context.Context) (map[string]string, error) { return m, nil }

// TestScanContent：文件内容按规则表与密钥库扫描，命中项的 entryId 是路径、按路径顺序，干净的文件只计数。
func TestScanContent(t *testing.T) {
	s := &Service{sec: mapSecrets{"db/password": "correct-horse-battery"}}
	res := s.ScanContent(context.Background(), map[string][]byte{
		"index.html":  []byte(`<script>const key = "sk-test-abcdefghijklmnopqrstuvwx0123";</script>`),
		"a/conf.json": []byte(`{"password":"correct-horse-battery"}`),
		"clean.css":   []byte("body { color: red }"),
	})
	assert.Equal(t, 3, res.Scanned)
	assert.False(t, res.Truncated)
	require.Len(t, res.Findings, 2)
	assert.Equal(t, "a/conf.json", res.Findings[0].EntryID)
	assert.Equal(t, apigen.ShareFindingRuleVaultSecret, res.Findings[0].Rule)
	assert.Equal(t, "index.html", res.Findings[1].EntryID)
	assert.Equal(t, apigen.ShareFindingRuleAPIKeySk, res.Findings[1].Rule)
	assert.Zero(t, res.Findings[1].Seq)

	empty := (&Service{}).ScanContent(context.Background(), nil)
	assert.NotNil(t, empty.Findings)
	assert.Zero(t, empty.Scanned)
}
