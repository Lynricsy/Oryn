package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
)

// requireGit 在找不到 git 时跳过；ORYN_REQUIRE_GIT=1（CI）时改为失败。
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("ORYN_REQUIRE_GIT") == "1" {
			t.Fatal("ORYN_REQUIRE_GIT=1，但找不到 git")
		}
		t.Skip("找不到 git")
	}
}

// fakeGitHost 起一个带 smart-HTTP 的假 git 主机，返回它与一个开放推送的空裸仓库 acme/<name> 的克隆地址。
// 仓库带 initial commit（init=true）或完全为空。
func fakeGitHost(t *testing.T, name string, init bool) (*githubtest.Server, string) {
	t.Helper()
	fake := githubtest.New()
	t.Cleanup(fake.Close)
	fake.GitRoot = t.TempDir()
	full := "acme/" + name
	if init {
		require.NoError(t, fake.InitRepo(full))
	} else {
		bare := fake.BarePath(full)
		gitT(t, t.TempDir(), "init", "--bare", "-q", bare)
		gitT(t, bare, "config", "http.receivepack", "true")
	}
	return fake, fake.URL() + "/" + full + ".git"
}

func hostedProject(name, cloneURL string) apigen.Project {
	return apigen.Project{ID: "prj_01JX0000000000000000000000", Name: name, Hosted: new(true), CloneURL: new(cloneURL)}
}

func TestCloneArgs(t *testing.T) {
	assert.Equal(t,
		[]string{"clone", "-c", "credential.https://h.helper=", "-c", "credential.https://h.helper=!oryn git-credential", "--", "https://h/git/p.git", "demo"},
		cloneArgs("https://h/git/p.git", "demo", "!oryn git-credential"))
	assert.Equal(t, `'/a b/o'\''ryn'`, shellQuote("/a b/o'ryn"))

	o, ok := credentialOrigin("HTTPS://Oryn.Example.com:8443/git/p.git")
	assert.True(t, ok)
	assert.Equal(t, "https://oryn.example.com:8443", o)
	for _, bad := range []string{"ext::sh -c id", "file:///tmp/x", "git@h:o/r", "", "https:///p"} {
		_, ok := credentialOrigin(bad)
		assert.False(t, ok, bad)
	}
	assert.True(t, sameOrigin("https://h/git/p.git", "https://H/"))
	assert.False(t, sameOrigin("https://h/git/p.git", "http://h"))
	assert.False(t, sameOrigin("https://h/git/p.git", "https://h:8443"))

	assert.True(t, strings.HasSuffix(orynHelper(), " git-credential"))
	assert.True(t, strings.HasPrefix(orynHelper(), "!"))
}

func TestOrynCloneRejects(t *testing.T) {
	ext := apigen.Project{ID: "prj_ext", Name: "ext", RepoURL: new("https://github.com/acme/w")}
	none := apigen.Project{ID: "prj_none", Name: "none"}
	cases := []struct {
		name     string
		project  apigen.Project
		credURL  string
		usage    bool
		contains string
	}{
		{"外部仓库", ext, "https://oryn.test", true, "git clone https://github.com/acme/w"},
		{"没有仓库", none, "https://oryn.test", true, "没有仓库"},
		{"项目名不能作目录", hostedProject("a/b", "https://oryn.test/git/prj_x.git"), "https://oryn.test", true, "oryn clone prj_01JX0000000000000000000000 <目录>"},
		{"项目名以 - 开头", hostedProject("-x", "https://oryn.test/git/prj_x.git"), "https://oryn.test", true, "不能直接作目录名"},
		{"登录主机不同", hostedProject("demo", "https://oryn.test/git/prj_x.git"), "https://other.test", false, "与登录的服务器"},
		{"协议不同", hostedProject("demo", "http://oryn.test/git/prj_x.git"), "https://oryn.test", false, "与登录的服务器"},
		{"非 http 克隆地址", hostedProject("demo", "ext::sh -c id"), "https://oryn.test", false, "不是 http(s)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{tc.project}})
			err := orynClone(context.Background(), f.client(), tc.credURL, tc.project.Name, "", &bytes.Buffer{}, &bytes.Buffer{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.contains)
			var ec *exitCodeError
			assert.Equal(t, tc.usage, errors.As(err, &ec) && ec.code == 2)
		})
	}

	f := newFakeAPI(t)
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{}})
	err := orynClone(context.Background(), f.client(), "https://oryn.test", "missing", "", &bytes.Buffer{}, &bytes.Buffer{})
	require.ErrorContains(t, err, `项目 "missing" 不存在`)
	assert.Len(t, f.requests(), 2, "未归档与已归档都查过")
}

func TestOrynCloneHosted(t *testing.T) {
	requireGit(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	fake, cloneURL := fakeGitHost(t, "widgets", true)
	f := newFakeAPI(t)
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{hostedProject("demo", cloneURL)}})

	dir := filepath.Join(t.TempDir(), "w")
	require.NoError(t, orynClone(context.Background(), f.client(), fake.URL(), "demo", dir, &bytes.Buffer{}, &bytes.Buffer{}))
	assert.Equal(t, "initial commit", gitT(t, dir, "log", "-1", "--format=%s"))

	origin, _ := credentialOrigin(cloneURL)
	out, err := exec.Command("git", "-C", dir, "config", "--local", "--get-all", "credential."+origin+".helper").Output()
	require.NoError(t, err)
	helpers := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, helpers, 2)
	assert.Empty(t, helpers[0])
	assert.Equal(t, orynHelper(), helpers[1])

	// 目录已存在：git 自己报错，退出码原样带回
	err = orynClone(context.Background(), f.client(), fake.URL(), "demo", dir, &bytes.Buffer{}, &bytes.Buffer{})
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 128, ec.code)
}
