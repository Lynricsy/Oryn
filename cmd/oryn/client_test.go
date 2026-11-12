package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OD12：ORYN_CREDENTIALS 指向共享目录时不得改动该目录的权限；凭据文件仍为 0600。
func TestSaveCredentialsKeepsCustomParentMode(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	require.NoError(t, os.Mkdir(shared, 0o755))
	require.NoError(t, os.Chmod(shared, 0o777|os.ModeSticky))
	p := filepath.Join(shared, "creds.json")
	t.Setenv("ORYN_CREDENTIALS", p)
	require.NoError(t, saveCredentials(credentials{URL: "http://x", Token: "odt_x"}))
	st, err := os.Stat(shared)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o777)|os.ModeSticky, st.Mode()&(os.ModePerm|os.ModeSticky), "共享父目录权限不变")
	fi, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	// 缺失的目录照常以 0700 创建。
	fresh := filepath.Join(t.TempDir(), "new", "dir", "creds.json")
	t.Setenv("ORYN_CREDENTIALS", fresh)
	require.NoError(t, saveCredentials(credentials{URL: "http://x", Token: "odt_x"}))
	st, err = os.Stat(filepath.Dir(fresh))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), st.Mode().Perm())
}
