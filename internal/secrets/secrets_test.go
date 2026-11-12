package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoxRoundTrip(t *testing.T) {
	dir := t.TempDir()
	b, err := LoadOrCreate(dir)
	require.NoError(t, err)
	st, err := os.Stat(filepath.Join(dir, keyFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	sealed, err := b.Seal("a", []byte("v"))
	require.NoError(t, err)
	b2, err := LoadOrCreate(dir)
	require.NoError(t, err)
	got, err := b2.Open("a", sealed)
	require.NoError(t, err)
	assert.Equal(t, "v", string(got))
	_, err = b2.Open("b", sealed)
	assert.Error(t, err, "名称不同的附加数据应解密失败")
}

func TestValidName(t *testing.T) {
	assert.NoError(t, ValidName("GITHUB_TOKEN", ModeEnv))
	assert.Error(t, ValidName("git/github.com/token", ModeEnv))
	assert.NoError(t, ValidName("git/github.com/token", ModeProxy))
}
