package secrets_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
)

func newVault(t *testing.T) *secrets.Vault {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(make([]byte, 32))
	require.NoError(t, err)
	return secrets.NewVault(st, box)
}

func TestVaultCRUD(t *testing.T) {
	ctx := context.Background()
	v := newVault(t)

	m, err := v.Put(ctx, "omp/models/openai/apiKey", secrets.ModeProxy, "sk-one", []string{" API.OpenAI.com ", "api.openai.com"})
	require.NoError(t, err)
	assert.Equal(t, []string{"api.openai.com"}, m.Hosts)
	assert.Regexp(t, `^sec_[0-9A-Z]{26}$`, m.ID)

	// 覆盖保留 id，占位符继续有效
	m2, err := v.Put(ctx, "omp/models/openai/apiKey", secrets.ModeProxy, "sk-two", nil)
	require.NoError(t, err)
	assert.Equal(t, m.ID, m2.ID)
	assert.Equal(t, []string{"api.openai.com"}, m2.Hosts, "hosts=nil 保留原有 hosts")

	id, val, ok := v.Lookup("omp/models/openai/apiKey")
	require.True(t, ok)
	assert.Equal(t, m.ID, id)
	assert.Equal(t, "sk-two", val)

	val, hosts, ok := v.SecretByID(m.ID)
	require.True(t, ok)
	assert.Equal(t, "sk-two", val)
	assert.Equal(t, []string{"api.openai.com"}, hosts)

	// 空切片显式清空
	m3, err := v.Put(ctx, "omp/models/openai/apiKey", secrets.ModeProxy, "sk-two", []string{})
	require.NoError(t, err)
	assert.Empty(t, m3.Hosts)

	_, _, ok = v.SecretByID("sec_missing")
	assert.False(t, ok)
	_, _, ok = v.Lookup("nope")
	assert.False(t, ok)

	list, err := v.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "omp/models/openai/apiKey", list[0].Name)

	deleted, err := v.Delete(ctx, "omp/models/openai/apiKey")
	require.NoError(t, err)
	assert.True(t, deleted)
	deleted, err = v.Delete(ctx, "omp/models/openai/apiKey")
	require.NoError(t, err)
	assert.False(t, deleted)
}

func TestVaultValidation(t *testing.T) {
	ctx := context.Background()
	v := newVault(t)
	_, err := v.Put(ctx, "lower_case", secrets.ModeEnv, "x", nil)
	assert.Error(t, err)
	_, err = v.Put(ctx, "OK_NAME", secrets.ModeEnv, "", nil)
	assert.Error(t, err)
	_, err = v.Put(ctx, "../evil", secrets.ModeProxy, "x", nil)
	assert.Error(t, err)
}

func TestVaultEnvAndGit(t *testing.T) {
	ctx := context.Background()
	v := newVault(t)
	_, err := v.Put(ctx, "MY_TOKEN", secrets.ModeEnv, "env-value", nil)
	require.NoError(t, err)
	_, err = v.Put(ctx, secrets.GitTokenName("GitHub.com"), secrets.ModeProxy, "ghp_x", nil)
	require.NoError(t, err)
	_, err = v.Put(ctx, "git/a/b/token", secrets.ModeProxy, "ignored", nil)
	require.NoError(t, err)

	env, err := v.EnvSecrets()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"MY_TOKEN": "env-value"}, env)

	tok, ok := v.GitToken("github.com")
	require.True(t, ok)
	assert.Equal(t, "ghp_x", tok)
	_, ok = v.GitToken("gitlab.com")
	assert.False(t, ok)

	hosts, err := v.GitHosts()
	require.NoError(t, err)
	assert.Equal(t, []string{"github.com"}, hosts)
}
