package secrets_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func newVault(t *testing.T) *secrets.Vault {
	t.Helper()
	v, _ := newVaultStore(t)
	return v
}

func newVaultStore(t *testing.T) (*secrets.Vault, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(make([]byte, 32))
	require.NoError(t, err)
	return secrets.NewVault(st, box), st
}

func createProject(t *testing.T, st *store.Store, id, name string) {
	t.Helper()
	require.NoError(t, st.Q.CreateProject(context.Background(), db.CreateProjectParams{ID: id, Name: name, CreatedAt: 1, UpdatedAt: 1}))
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

func TestVaultProjectScope(t *testing.T) {
	ctx := context.Background()
	v, st := newVaultStore(t)
	createProject(t, st, "prj_A", "alpha")
	createProject(t, st, "prj_B", "beta")

	m, err := v.PutProject(ctx, "prj_A", "FOO", secrets.ModeEnv, "bar", false)
	require.NoError(t, err)
	assert.Equal(t, "prj_A", m.ProjectID)
	assert.False(t, m.Secret)

	list, err := v.ListProject(ctx, "prj_A")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "FOO", list[0].Name)
	assert.False(t, list[0].Secret)
	require.NotNil(t, list[0].Value)
	assert.Equal(t, "bar", *list[0].Value)

	list, err = v.ListProject(ctx, "prj_B")
	require.NoError(t, err)
	assert.Empty(t, list)
	global, err := v.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, global)
	_, _, ok := v.Lookup("FOO")
	assert.False(t, ok, "Lookup 只解析全局行")
	_, _, ok = v.SecretByID(m.ID)
	assert.False(t, ok, "占位符只解析全局行")

	// 覆盖保留 id；保密条目不给值
	m2, err := v.PutProject(ctx, "prj_A", "FOO", secrets.ModeEnv, "baz", true)
	require.NoError(t, err)
	assert.Equal(t, m.ID, m2.ID)
	list, err = v.ListProject(ctx, "prj_A")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.True(t, list[0].Secret)
	assert.Nil(t, list[0].Value)
	env, err := v.ProjectEnv("prj_A")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"FOO": "baz"}, env)

	_, err = v.PutProject(ctx, "prj_A", "git/gitlab.com/token", secrets.ModeProxy, "glpat", true)
	require.NoError(t, err)
	tok, found, err := v.ProjectGitToken("prj_A", "GitLab.com")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "glpat", tok)
	_, found, err = v.ProjectGitToken("prj_B", "gitlab.com")
	require.NoError(t, err)
	assert.False(t, found)
	_, ok = v.GitToken("gitlab.com")
	assert.False(t, ok, "全局 GitToken 看不到项目令牌")
	hosts, err := v.ProjectGitHosts("prj_A")
	require.NoError(t, err)
	assert.Equal(t, []string{"gitlab.com"}, hosts)
	env, err = v.ProjectEnv("prj_A")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"FOO": "baz"}, env, "git 令牌不进环境变量")

	_, err = v.PutProject(ctx, "prj_nope", "FOO", secrets.ModeEnv, "x", false)
	require.ErrorIs(t, err, secrets.ErrNoProject)
	_, err = v.PutProject(ctx, "prj_A", "EMPTY", secrets.ModeEnv, "", false)
	require.Error(t, err)

	deleted, err := v.DeleteProject(ctx, "prj_B", "FOO")
	require.NoError(t, err)
	assert.False(t, deleted)

	_, err = st.Q.DeleteProject(ctx, "prj_A")
	require.NoError(t, err)
	list, err = v.ListProject(ctx, "prj_A")
	require.NoError(t, err)
	assert.Empty(t, list, "删除项目级联删除条目")
}

func TestVaultProjectAAD(t *testing.T) {
	ctx := context.Background()
	v, st := newVaultStore(t)
	createProject(t, st, "prj_A", "alpha")
	createProject(t, st, "prj_B", "beta")

	_, err := v.PutProject(ctx, "prj_A", "FOO", secrets.ModeEnv, "bar", true)
	require.NoError(t, err)
	_, err = st.DB.ExecContext(ctx, `UPDATE secrets SET project_id = 'prj_B' WHERE name = 'FOO'`)
	require.NoError(t, err)
	_, err = v.ProjectEnv("prj_B")
	assert.Error(t, err, "密文挪到别的项目解不开")

	_, err = v.PutProject(ctx, "prj_A", "BAR", secrets.ModeEnv, "v", true)
	require.NoError(t, err)
	_, err = st.DB.ExecContext(ctx, `UPDATE secrets SET secret = 0 WHERE name = 'BAR'`)
	require.NoError(t, err)
	_, err = v.ProjectEnv("prj_A")
	assert.Error(t, err, "翻转保密位解不开")
	list, err := v.ListProject(ctx, "prj_A")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Nil(t, list[0].Value)

	_, err = v.PutProject(ctx, "prj_A", "git/gitlab.com/token", secrets.ModeProxy, "t", true)
	require.NoError(t, err)
	_, err = st.DB.ExecContext(ctx, `UPDATE secrets SET name = 'git/github.com/token' WHERE name = 'git/gitlab.com/token'`)
	require.NoError(t, err)
	_, found, err := v.ProjectGitToken("prj_A", "github.com")
	assert.Error(t, err, "密文换名字解不开")
	assert.False(t, found)
}

func TestVaultAllKnownSet(t *testing.T) {
	ctx := context.Background()
	v, st := newVaultStore(t)
	createProject(t, st, "prj_A", "alpha")

	_, err := v.Put(ctx, "X", secrets.ModeEnv, "sk-global-123456", nil)
	require.NoError(t, err)
	_, err = v.PutProject(ctx, "prj_A", "S", secrets.ModeEnv, "sk-proj-123456", true)
	require.NoError(t, err)
	_, err = v.PutProject(ctx, "prj_A", "P", secrets.ModeEnv, "plain-value-123", false)
	require.NoError(t, err)

	all, err := v.All(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"X": "sk-global-123456", "alpha:S": "sk-proj-123456"}, all)
}
