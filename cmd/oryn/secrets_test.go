package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// runSecrets 用假服务端执行 `oryn secrets …`；stdin 为空串时标准输入为空。
func runSecrets(f *fakeAPI, stdin string, args ...string) (string, string, error) {
	cmd := newSecretsCmd(func() (*apiClient, error) { return f.client(), nil })
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var out, errb bytes.Buffer
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func secretsFixtures(f *fakeAPI) {
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "demo"}}})
}

func TestSecretsProjectSet(t *testing.T) {
	f := newFakeAPI(t)
	secretsFixtures(f)
	f.json("PUT /api/v1/projects/prj_A/secrets/FOO", 200, apigen.ProjectSecret{Name: "FOO", Mode: apigen.Env, Secret: false, Value: new("bar")})
	f.json("PUT /api/v1/projects/prj_A/secrets/git/gitlab.com/token", 200, apigen.ProjectSecret{Name: "git/gitlab.com/token", Mode: apigen.Proxy, Secret: true})

	_, errOut, err := runSecrets(f, "", "set", "FOO", "bar", "--project", "demo", "--plain")
	require.NoError(t, err)
	r := lastReq(t, f)
	assert.Equal(t, http.MethodPut, r.Method)
	assert.Equal(t, "/api/v1/projects/prj_A/secrets/FOO", r.Path)
	assert.JSONEq(t, `{"value":"bar","mode":"env","secret":false}`, string(r.Body))
	assert.Contains(t, errOut, "已写入项目 demo 的 FOO（env，不保密）")

	_, errOut, err = runSecrets(f, "", "set", "git/gitlab.com/token", "t", "--project", "demo")
	require.NoError(t, err)
	r = lastReq(t, f)
	assert.Equal(t, "/api/v1/projects/prj_A/secrets/git/gitlab.com/token", r.Path)
	assert.JSONEq(t, `{"value":"t","mode":"proxy","secret":true}`, string(r.Body))
	assert.Contains(t, errOut, "保密")

	// 值从标准输入读，显式 --mode 优先于推断
	_, _, err = runSecrets(f, "bar\n", "set", "FOO", "--project", "prj_A", "--mode", "env")
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":"bar","mode":"env","secret":true}`, string(lastReq(t, f).Body))
}

func TestSecretsUsageErrors(t *testing.T) {
	f := newFakeAPI(t)
	secretsFixtures(f)
	_, _, err := runSecrets(f, "", "set", "X", "v", "--plain")
	var ue *exitCodeError
	require.ErrorAs(t, err, &ue)
	assert.Equal(t, 2, ue.code)
	_, _, err = runSecrets(f, "", "set", "git/a.com/token", "v", "--project", "demo", "--hosts", "a.com")
	require.ErrorAs(t, err, &ue)
	assert.Empty(t, f.requests(), "用法错误不发请求")

	_, _, err = runSecrets(f, "", "ls", "--project", "nope")
	assert.ErrorContains(t, err, `项目 "nope" 不存在`)
}

func TestSecretsProjectLsRm(t *testing.T) {
	f := newFakeAPI(t)
	secretsFixtures(f)
	f.json("GET /api/v1/projects/prj_A/secrets", 200, apigen.ProjectSecretList{Secrets: []apigen.ProjectSecret{
		{Name: "FOO", Mode: apigen.Env, Secret: false, Value: new("bar")},
		{Name: "MULTI", Mode: apigen.Env, Secret: false, Value: new("a\tb")},
		{Name: "TOK", Mode: apigen.Env, Secret: true},
		{Name: "git/gitlab.com/token", Mode: apigen.Proxy, Secret: true},
	}})
	f.json("DELETE /api/v1/projects/prj_A/secrets/FOO", 204, nil)

	out, _, err := runSecrets(f, "", "ls", "--project", "demo")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 5)
	assert.Equal(t, []string{"NAME", "KIND", "SECRET", "VALUE", "UPDATED"}, strings.Fields(lines[0]))
	assert.Regexp(t, `^FOO\s+env\s+no\s+bar\s+-$`, lines[1])
	assert.Contains(t, lines[2], `"a\tb"`)
	assert.Regexp(t, `^TOK\s+env\s+yes\s+-\s+-$`, lines[3])
	assert.Regexp(t, `^git/gitlab.com/token\s+git\s+yes\s+-`, lines[4])

	out, _, err = runSecrets(f, "", "ls", "--project", "demo", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"value": "bar"`)

	_, _, err = runSecrets(f, "", "rm", "FOO", "--project", "demo")
	require.NoError(t, err)
	r := lastReq(t, f)
	assert.Equal(t, http.MethodDelete, r.Method)
	assert.Equal(t, "/api/v1/projects/prj_A/secrets/FOO", r.Path)
}

func TestSecretsGlobalUnchanged(t *testing.T) {
	f := newFakeAPI(t)
	f.json("PUT /api/v1/secrets/git/github.com/token", 200, apigen.SecretMeta{Name: "git/github.com/token", Mode: apigen.Proxy})
	f.json("GET /api/v1/secrets", 200, apigen.SecretList{Secrets: []apigen.SecretMeta{{Name: "X", Mode: apigen.Env, Hosts: []string{}}}})
	f.json("DELETE /api/v1/secrets/X", 204, nil)

	_, errOut, err := runSecrets(f, "", "set", "git/github.com/token", "t", "--hosts", "github.com")
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":"t","mode":"proxy","hosts":["github.com"]}`, string(lastReq(t, f).Body))
	assert.Contains(t, errOut, "已写入 git/github.com/token（proxy）")

	out, _, err := runSecrets(f, "", "ls")
	require.NoError(t, err)
	assert.Contains(t, out, "NAME  MODE  HOSTS  UPDATED")
	_, _, err = runSecrets(f, "", "rm", "X")
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/secrets/X", lastReq(t, f).Path)
}
