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

func TestValidProjectEntry(t *testing.T) {
	cases := []struct {
		name, mode string
		secret     bool
		err        string // "" = 通过
	}{
		{"FOO", ModeEnv, false, ""},
		{"FOO", ModeEnv, true, ""},
		{"git/gitlab.com/token", ModeProxy, true, ""},
		{"FOO", "nope", true, "未知模式"},
		{"lower", ModeEnv, true, "env 模式的密钥名必须匹配"},
		{"omp/models/openai/apiKey", ModeProxy, true, "只能在全局密钥里设置"},
		{"git/GitLab.com/token", ModeProxy, true, "只能是 git/<主机>/token"},
		{"git/a/b/token", ModeProxy, true, "只能是 git/<主机>/token"},
		{"other/name", ModeProxy, true, "只能是 git/<主机>/token"},
		{"git/gitlab.com/token", ModeProxy, false, "Git 令牌只能保密保存"},
	}
	for _, c := range cases {
		err := ValidProjectEntry(c.name, c.mode, c.secret)
		if c.err == "" {
			assert.NoError(t, err, c.name)
		} else if assert.Error(t, err, c.name) {
			assert.Contains(t, err.Error(), c.err, c.name)
		}
	}
	assert.Equal(t, "project:prj_A:FOO", ProjectAAD("prj_A", "FOO", true))
	assert.Equal(t, "project:prj_A:FOO:plain", ProjectAAD("prj_A", "FOO", false))
}
