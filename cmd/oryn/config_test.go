package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestPullTarget(t *testing.T) {
	dir := t.TempDir()

	dst, renamed, err := pullTarget(dir, "models.yml", false)
	require.NoError(t, err)
	assert.True(t, renamed)
	assert.Equal(t, filepath.Join(dir, "models.oryn.yml"), dst)

	dst, renamed, err = pullTarget(dir, "models.yml", true)
	require.NoError(t, err)
	assert.False(t, renamed)
	assert.Equal(t, filepath.Join(dir, "models.yml"), dst)

	dst, _, err = pullTarget(dir, "skills/a/SKILL.md", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "skills", "a", "SKILL.md"), dst)

	for _, bad := range []string{"../x", "/etc/passwd", "a/../../x", "..", "", "sessions/a.jsonl"} {
		_, _, err := pullTarget(dir, bad, false)
		assert.Error(t, err, bad)
	}
}

func TestPushExclusions(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"config.yml": "x", "extensions/omp-notify/index.ts": "x", "extensions/keep.ts": "x", "omp-notify.json": "x", "sessions/x.jsonl": "x",
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".orynignore"), []byte("# 通知插件\nextensions/omp-notify/\n"), 0o644))
	ignore, err := readOrynIgnore(dir)
	require.NoError(t, err)
	files, err := collectPushFiles(dir, agentcfg.Excluded, append(ignore, "omp-notify*"))
	require.NoError(t, err)
	var rels []string
	for _, f := range files {
		rels = append(rels, f.rel)
	}
	assert.Equal(t, []string{"config.yml", "extensions/keep.ts"}, rels)
}

func TestConfigScopeDefaultDir(t *testing.T) {
	dir, err := configScope{project: "demo"}.defaultDir(nil)
	require.NoError(t, err)
	assert.Equal(t, ".omp", dir, "项目级缺省为当前目录下的 .omp")

	dir, err = configScope{}.defaultDir(nil)
	require.NoError(t, err)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".omp", "agent"), dir)

	dir, err = configScope{project: "demo"}.defaultDir([]string{"x"})
	require.NoError(t, err)
	assert.Equal(t, "x", dir)
}

// projectsAPI 返回带两个项目的假服务端。
func projectsAPI(t *testing.T) *fakeAPI {
	f := newFakeAPI(t)
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "demo"}, {ID: "prj_B", Name: "other"}}})
	return f
}

// pushTree 是推送用的配置目录：含项目级不收的 models.yml、plans/ 与全局也排除的 sessions/。
var pushTree = map[string]string{
	"rules/a.md":     "# rule",
	"config.yml":     "theme: dark\n",
	"models.yml":     "providers: {}\n",
	"plans/p.md":     "plan",
	"sessions/x.log": "x",
}

// runPush 打包 dir 并上传到 scope，返回 stderr。
func runPush(t *testing.T, f *fakeAPI, scope configScope, dir string, exclude []string) (string, error) {
	t.Helper()
	var stderr bytes.Buffer
	tarball, err := buildConfigTar(scope, dir, exclude, &stderr)
	if err == nil {
		err = uploadConfigTar(context.Background(), f.client(), scope, tarball, &stderr)
	}
	return stderr.String(), err
}

func TestConfigPushProject(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, pushTree)

	for _, ref := range []string{"demo", "prj_A"} {
		t.Run(ref, func(t *testing.T) {
			f := projectsAPI(t)
			f.json("POST /api/v1/projects/prj_A/agent-config/import", 200, apigen.AgentConfigImportResult{Files: []apigen.AgentConfigFile{{Path: "config.yml"}, {Path: "rules/a.md"}}})
			stderr, err := runPush(t, f, configScope{project: ref}, dir, nil)
			require.NoError(t, err)
			assert.Contains(t, stderr, "项目级配置不支持 models.yml，已跳过")
			assert.Contains(t, stderr, "已上传 2 个文件")

			reqs := f.requests()
			require.Len(t, reqs, 2)
			assert.Equal(t, "GET /api/v1/projects", reqs[0].Method+" "+reqs[0].Path)
			assert.Equal(t, "POST /api/v1/projects/prj_A/agent-config/import", reqs[1].Method+" "+reqs[1].Path)
			assert.Equal(t, map[string]string{"config.yml": "theme: dark\n", "rules/a.md": "# rule"}, untar(t, reqs[1].Body),
				"tar 内不含 models.yml、plans/ 与 sessions/")
		})
	}

	// --exclude 排除了 models.yml 时不再提示
	f := projectsAPI(t)
	f.json("POST /api/v1/projects/prj_A/agent-config/import", 200, apigen.AgentConfigImportResult{})
	stderr, err := runPush(t, f, configScope{project: "demo"}, dir, []string{"models.yml"})
	require.NoError(t, err)
	assert.NotContains(t, stderr, "models.yml")
}

func TestConfigPushGlobalUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, pushTree)
	f := newFakeAPI(t)
	f.json("POST /api/v1/agent-config/import", 200, apigen.AgentConfigImportResult{})
	stderr, err := runPush(t, f, configScope{}, dir, nil)
	require.NoError(t, err)
	assert.NotContains(t, stderr, "已跳过")
	reqs := f.requests()
	require.Len(t, reqs, 1, "全局不解析项目")
	assert.Equal(t, "POST /api/v1/agent-config/import", reqs[0].Method+" "+reqs[0].Path)
	got := untar(t, reqs[0].Body)
	assert.ElementsMatch(t, []string{"config.yml", "models.yml", "plans/p.md", "rules/a.md"}, slices.Collect(maps.Keys(got)))
}

func TestConfigPushProjectNotFound(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, pushTree)
	f := projectsAPI(t)
	_, err := runPush(t, f, configScope{project: "nope"}, dir, nil)
	require.ErrorContains(t, err, `项目 "nope" 不存在`)
	for _, r := range f.requests() {
		assert.Equal(t, "GET", r.Method, "项目不存在时不上传")
	}
}

func TestConfigPushProjectQuota(t *testing.T) {
	sparse := func(t *testing.T, p string, size int64) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		f, err := os.Create(p)
		require.NoError(t, err)
		require.NoError(t, f.Truncate(size))
		require.NoError(t, f.Close())
	}
	project := configScope{project: "demo"}

	t.Run("单文件", func(t *testing.T) {
		dir := t.TempDir()
		sparse(t, filepath.Join(dir, "rules/big.md"), agentcfg.ProjectMaxFileSize+1)
		_, err := buildConfigTar(project, dir, nil, io.Discard)
		require.ErrorContains(t, err, "rules/big.md 超过单文件上限 1 MiB")
		_, err = buildConfigTar(configScope{}, dir, nil, io.Discard)
		require.NoError(t, err, "全局上限不变")
	})

	t.Run("合计", func(t *testing.T) {
		dir := t.TempDir()
		for i := range agentcfg.ProjectMaxTotalSize/agentcfg.ProjectMaxFileSize + 1 {
			sparse(t, filepath.Join(dir, fmt.Sprintf("rules/%02d.md", i)), agentcfg.ProjectMaxFileSize)
		}
		_, err := buildConfigTar(project, dir, nil, io.Discard)
		require.ErrorContains(t, err, "合计超过上限 16 MiB")
	})

	t.Run("文件数", func(t *testing.T) {
		dir := t.TempDir()
		for i := range agentcfg.ProjectMaxFiles + 1 {
			sparse(t, filepath.Join(dir, fmt.Sprintf("rules/%04d.md", i)), 1)
		}
		_, err := buildConfigTar(project, dir, nil, io.Discard)
		require.ErrorContains(t, err, "文件数 2001 超过上限 2000")
	})

	t.Run("不发请求", func(t *testing.T) {
		dir := t.TempDir()
		sparse(t, filepath.Join(dir, "rules/big.md"), agentcfg.ProjectMaxFileSize+1)
		f := projectsAPI(t)
		_, err := runPush(t, f, project, dir, nil)
		require.ErrorContains(t, err, "超过单文件上限")
		assert.Empty(t, f.requests(), "配额在本地先行报错")
	})
}

func TestConfigPullProject(t *testing.T) {
	f := projectsAPI(t)
	f.json("GET /api/v1/projects/prj_A/agent-config/files", 200, apigen.AgentConfigFileList{Files: []apigen.AgentConfigFile{
		{Path: "rules/a.md", Mode: 0o600, Size: 6},
		{Path: "config.yml", Size: 12},
	}})
	f.raw("GET /api/v1/projects/prj_A/agent-config/files/rules/a.md", "application/octet-stream", "# rule")
	f.raw("GET /api/v1/projects/prj_A/agent-config/files/config.yml", "application/octet-stream", "theme: dark\n")

	dir := filepath.Join(t.TempDir(), ".omp")
	var stdout, stderr bytes.Buffer
	require.NoError(t, configPull(context.Background(), f.client(), configScope{project: "demo"}, dir, false, false, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "已下载 2 个文件")

	b, err := os.ReadFile(filepath.Join(dir, "rules", "a.md"))
	require.NoError(t, err)
	assert.Equal(t, "# rule", string(b))
	st, err := os.Stat(filepath.Join(dir, "rules", "a.md"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	b, err = os.ReadFile(filepath.Join(dir, "config.yml"))
	require.NoError(t, err)
	assert.Equal(t, "theme: dark\n", string(b))
	st, err = os.Stat(filepath.Join(dir, "config.yml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), st.Mode().Perm(), "服务端未给权限时为 0644")

	for _, r := range f.requests() {
		assert.Equal(t, "GET", r.Method)
		assert.NotContains(t, r.Path, "/api/v1/agent-config", "不读全局 omp-home")
	}
}

func TestConfigPullProjectRejectsExcluded(t *testing.T) {
	for _, p := range []string{"models.yml", "plans/a.md"} {
		f := projectsAPI(t)
		f.json("GET /api/v1/projects/prj_B/agent-config/files", 200, apigen.AgentConfigFileList{Files: []apigen.AgentConfigFile{{Path: "rules/a.md"}, {Path: p}}})
		f.raw("GET /api/v1/projects/prj_B/agent-config/files/rules/a.md", "application/octet-stream", "x")
		dir := t.TempDir()
		err := configPull(context.Background(), f.client(), configScope{project: "prj_B"}, dir, true, false, io.Discard, io.Discard)
		require.ErrorContains(t, err, "项目级配置不支持", p)
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Empty(t, entries, "先校验全部路径再写入")
	}
}

func TestConfigPullProjectNotFound(t *testing.T) {
	f := projectsAPI(t)
	err := configPull(context.Background(), f.client(), configScope{project: "nope"}, t.TempDir(), false, false, io.Discard, io.Discard)
	require.ErrorContains(t, err, `项目 "nope" 不存在`)
	assert.Len(t, f.requests(), 1, "只查了项目列表")
}

func TestConfigPullGlobalRenamesModels(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/agent-config/files", 200, apigen.AgentConfigFileList{Files: []apigen.AgentConfigFile{{Path: "models.yml"}}})
	f.raw("GET /api/v1/agent-config/files/models.yml", "application/octet-stream", "providers: {}\n")
	dir := t.TempDir()
	require.NoError(t, configPull(context.Background(), f.client(), configScope{}, dir, false, false, io.Discard, io.Discard))
	_, err := os.Stat(filepath.Join(dir, pulledModelsFile))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "models.yml"))
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

// writeTree 在 dir 下按相对路径写入文件。
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
	}
}

// untar 把 tar 包解成 路径 → 内容。
func untar(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err)
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		out[h.Name] = string(body)
	}
}
