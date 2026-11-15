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
	"strings"
	"syscall"
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
	files, err := collectPushFiles(dir, agentcfg.Excluded, append(ignore, "omp-notify*"), false, io.Discard)
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
	// 用 UTF-8 文本填满（rules/ 下必须是文本，稀疏文件全是 NUL 会被文本校验先拒）
	sparse := func(t *testing.T, p string, size int64) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, bytes.Repeat([]byte("a"), int(size)), 0o644))
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

var (
	projectScope = configScope{project: "demo"}
	globalScope  = configScope{}
)

// pushErr 以给定的各 scope 打包 dir，断言都在本地报错且不发请求，返回各次的错误文本。
func pushErr(t *testing.T, dir string, exclude []string, scopes ...configScope) []string {
	t.Helper()
	var out []string
	for _, scope := range scopes {
		f := projectsAPI(t)
		_, err := runPush(t, f, scope, dir, exclude)
		require.Error(t, err, "scope=%+v", scope)
		assert.Empty(t, f.requests(), "本地先行报错，不发请求")
		out = append(out, err.Error())
	}
	return out
}

// pushTar 以 scope 打包 dir，要求成功，返回 tar 内容与 stderr。
func pushTar(t *testing.T, scope configScope, dir string, exclude []string) (map[string]string, string) {
	t.Helper()
	var stderr bytes.Buffer
	tarball, err := buildConfigTar(scope, dir, exclude, &stderr)
	require.NoError(t, err, "scope=%+v", scope)
	return untar(t, tarball), stderr.String()
}

func TestConfigPushRejectsEscapingLinks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, ".omp")
	home := filepath.Join(base, "home")
	writeTree(t, dir, map[string]string{"rules/a.md": "# rule"})
	writeTree(t, home, map[string]string{".ssh/id_ed25519": "PRIVATE KEY"})
	links := map[string]string{
		"rules/abs.md":  filepath.Join(home, ".ssh", "id_ed25519"), // 绝对路径指向根外
		"skills/x/ref":  "../../../home/.ssh",                      // 相对路径逃出根目录（目录）
		"rules/env.md":  "/proc/self/environ",                      // 伪文件系统
		"rules/gone.md": "missing.md",                              // 悬空链接也不静默跳过
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "x"), 0o755))
	for name, target := range links {
		require.NoError(t, os.Symlink(target, filepath.Join(dir, filepath.FromSlash(name))))
	}

	for _, msg := range pushErr(t, dir, nil, projectScope) {
		for name := range links {
			assert.Contains(t, msg, name)
		}
		assert.Contains(t, msg, "符号链接指向目录之外")
		assert.NotContains(t, msg, "rules/a.md", "合规文件不在拒绝列表里")
	}

	// 用户显式排除这些链接后照常上传，tar 里没有根外内容
	f := projectsAPI(t)
	f.json("POST /api/v1/projects/prj_A/agent-config/import", 200, apigen.AgentConfigImportResult{})
	_, err := runPush(t, f, configScope{project: "demo"}, dir, slices.Collect(maps.Keys(links)))
	require.NoError(t, err)
	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, map[string]string{"rules/a.md": "# rule"}, untar(t, reqs[1].Body))
}

func TestConfigPushGlobalFollowsOutsideLinks(t *testing.T) {
	// 模拟用户自己的 ~/.omp/agent：skills 目录与 agents/oracle.md 链接到根外
	base := t.TempDir()
	dir := filepath.Join(base, "agent")
	writeTree(t, dir, map[string]string{"config.yml": "theme: dark\n", "sessions/x.jsonl": "{}"})
	writeTree(t, base, map[string]string{"agents-src/skills/s1/SKILL.md": "skill one", "chore/oracle.md": "# oracle"})
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(base, "agents-src", "skills"), filepath.Join(dir, "skills"))) // 绝对路径目录链接
	require.NoError(t, os.Symlink("../../chore/oracle.md", filepath.Join(dir, "agents", "oracle.md")))        // 相对路径文件链接
	require.NoError(t, os.Symlink("missing.md", filepath.Join(dir, "gone.md")))                               // 悬空：提示后跳过
	require.NoError(t, os.Symlink("sessions/x.jsonl", filepath.Join(dir, "s.md")))                            // 指向被排除路径：提示后跳过

	f := newFakeAPI(t)
	f.json("POST /api/v1/agent-config/import", 200, apigen.AgentConfigImportResult{})
	stderr, err := runPush(t, f, globalScope, dir, nil)
	require.NoError(t, err)
	reqs := f.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, map[string]string{
		"config.yml":         "theme: dark\n",
		"skills/s1/SKILL.md": "skill one",
		"agents/oracle.md":   "# oracle",
	}, untar(t, reqs[0].Body))
	assert.Contains(t, stderr, "跳过 gone.md：")
	assert.Contains(t, stderr, "跳过 s.md：符号链接指向被排除的路径")

	// 同一目录用 --project 推送：根外链接、悬空链接、指向被排除路径的链接一律拒绝
	msg := pushErr(t, dir, nil, projectScope)[0]
	for _, name := range []string{"skills", "agents/oracle.md", "gone.md", "s.md"} {
		assert.Contains(t, msg, name)
	}
}

func TestConfigPushRejectsLinkToExcluded(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"rules/a.md": "# rule", "sessions/x.jsonl": "{}", "private/k.md": "k"})
	require.NoError(t, os.Symlink("../sessions/x.jsonl", filepath.Join(dir, "rules", "s.md")))
	require.NoError(t, os.Symlink("../private/k.md", filepath.Join(dir, "rules", "k.md")))
	for _, msg := range pushErr(t, dir, []string{"private"}, projectScope) {
		assert.Contains(t, msg, "rules/s.md")
		assert.Contains(t, msg, "rules/k.md")
		assert.Contains(t, msg, "符号链接指向被排除的路径")
	}
	// 全局：跳过并提示，不上传被排除的内容
	got, stderr := pushTar(t, globalScope, dir, []string{"private"})
	assert.Equal(t, map[string]string{"rules/a.md": "# rule"}, got)
	assert.Contains(t, stderr, "跳过 rules/s.md：符号链接指向被排除的路径")
	assert.Contains(t, stderr, "跳过 rules/k.md：符号链接指向被排除的路径")
}

func TestConfigPushAliasRespectsRealExclusion(t *testing.T) {
	// a -> skills：排除真实子路径 skills/private 后，别名 a/private/… 也不能漏出去
	newTree := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{
			"rules/a.md": "# rule", "skills/pub/SKILL.md": "pub", "skills/private/key.md": "KEY",
		})
		require.NoError(t, os.Symlink("skills", filepath.Join(dir, "a")))
		return dir
	}
	want := map[string]string{"rules/a.md": "# rule", "skills/pub/SKILL.md": "pub", "a/pub/SKILL.md": "pub"}
	check := func(t *testing.T, dir string, exclude []string) {
		t.Helper()
		got, stderr := pushTar(t, globalScope, dir, exclude)
		assert.Equal(t, want, got)
		assert.NotContains(t, got, "a/private/key.md")
		assert.Contains(t, stderr, "跳过 a/private：经符号链接指向被排除的路径 skills/private")

		msg := pushErr(t, dir, exclude, projectScope)[0]
		assert.Contains(t, msg, "a/private → skills/private（经符号链接指向被排除的路径）")
		// 把别名路径也排除后 --project 照常上传，tar 里同样没有 a/private/key.md
		got, _ = pushTar(t, projectScope, dir, append(slices.Clone(exclude), "a/private"))
		assert.Equal(t, want, got)
	}
	t.Run("--exclude", func(t *testing.T) {
		check(t, newTree(t), []string{"skills/private"})
	})
	t.Run(".orynignore", func(t *testing.T) {
		dir := newTree(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".orynignore"), []byte("skills/private/\n"), 0o644))
		check(t, dir, nil)
	})
}

func TestConfigPushAliasIntoSessions(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"rules/a.md": "# rule", "sessions/x.jsonl": "{}", "sessions/sub/y.jsonl": "{}"})
	require.NoError(t, os.Symlink("sessions", filepath.Join(dir, "s")))
	require.NoError(t, os.Symlink("sessions/sub", filepath.Join(dir, "deep")))
	require.NoError(t, os.Symlink(".", filepath.Join(dir, "all"))) // 根的别名：不会以 all/sessions/… 再走一遍

	got, stderr := pushTar(t, globalScope, dir, nil)
	assert.Equal(t, map[string]string{"rules/a.md": "# rule"}, got)
	assert.Contains(t, stderr, "跳过 s：符号链接指向被排除的路径")
	assert.Contains(t, stderr, "跳过 deep：符号链接指向被排除的路径")

	msg := pushErr(t, dir, nil, projectScope)[0]
	assert.Contains(t, msg, "s → ")
	assert.Contains(t, msg, "deep → ")
	assert.NotContains(t, msg, "all →")
}

func TestConfigPushInRootLinks(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "dotfiles")
	writeTree(t, real, map[string]string{"shared/base.md": "# base", "skills/x/SKILL.md": "skill"})
	require.NoError(t, os.MkdirAll(filepath.Join(real, "rules"), 0o755))
	require.NoError(t, os.Symlink("../shared/base.md", filepath.Join(real, "rules", "base.md")))     // 根内文件链接
	require.NoError(t, os.Symlink(filepath.Join(real, "skills", "x"), filepath.Join(real, "alias"))) // 根内目录链接（绝对路径）
	require.NoError(t, os.Symlink(".", filepath.Join(real, "skills", "loop")))                       // 成环：只走一次
	// 推送根本身是链接（如 ~/.omp/agent → dotfiles）：全局允许
	root := filepath.Join(base, "agent")
	require.NoError(t, os.Symlink(real, root))
	got, _ := pushTar(t, globalScope, root, nil)
	assert.Equal(t, map[string]string{
		"shared/base.md":    "# base",
		"rules/base.md":     "# base",
		"skills/x/SKILL.md": "skill",
		"alias/SKILL.md":    "skill",
	}, got)
	// --project 时根是链接直接拒绝；直接给真实路径则照常上传
	assert.Contains(t, pushErr(t, root, nil, projectScope)[0], "推送目录本身是符号链接")
	got, _ = pushTar(t, projectScope, real, nil)
	assert.Len(t, got, 4)
}

func TestConfigPushProjectRejectsLinkedRoot(t *testing.T) {
	// 恶意仓库把整个 .omp 指向假 ~/.ssh（含 id_rsa，ProjectExcluded 不排除它）
	base := t.TempDir()
	ssh := filepath.Join(base, "home", ".ssh")
	writeTree(t, ssh, map[string]string{"id_rsa": "PRIVATE KEY", "config": "Host *"})
	repo := filepath.Join(base, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, os.Symlink(ssh, filepath.Join(repo, ".omp")))
	t.Chdir(repo)

	dir, err := projectScope.defaultDir(nil) // 缺省 ./.omp
	require.NoError(t, err)
	f := projectsAPI(t)
	_, err = runPush(t, f, projectScope, dir, nil)
	require.ErrorContains(t, err, "推送目录本身是符号链接")
	assert.Empty(t, f.requests(), "服务端没收到任何请求")
}

func TestConfigPushRejectsSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"rules/a.md": "# rule"})
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "rules", "pipe.md"), 0o644))
	require.NoError(t, os.Symlink("pipe.md", filepath.Join(dir, "rules", "via.md"))) // 根内链接指向 FIFO 同样拒绝
	for _, msg := range pushErr(t, dir, nil, projectScope, globalScope) {
		assert.Contains(t, msg, "rules/pipe.md（FIFO，不是普通文件）")
		assert.Contains(t, msg, "rules/via.md（FIFO，不是普通文件）")
	}
}

func TestReadOrynIgnoreRejectsNonRegular(t *testing.T) {
	// 链接到字符设备：只 stat 不打开，不会阻塞
	dev := t.TempDir()
	require.NoError(t, os.Symlink("/dev/null", filepath.Join(dev, orynIgnoreFile)))
	_, err := readOrynIgnore(dev)
	require.ErrorContains(t, err, "必须是普通文件")

	// 超过上限的普通文件（稀疏文件，不占磁盘）
	big := t.TempDir()
	f, err := os.Create(filepath.Join(big, orynIgnoreFile))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(maxOrynIgnoreBytes+1))
	require.NoError(t, f.Close())
	_, err = readOrynIgnore(big)
	require.ErrorContains(t, err, "超过")

	// 正常文件照常解析
	ok := t.TempDir()
	writeTree(t, ok, map[string]string{orynIgnoreFile: "# c\n/skills/x/\n"})
	got, err := readOrynIgnore(ok)
	require.NoError(t, err)
	assert.Equal(t, []string{"skills/x"}, got)
}

func TestConfigPushTextPaths(t *testing.T) {
	for name, content := range map[string]string{
		"rules/env.txt": "A=1\x00B=2\x00", // rules/ 下任何文件
		"rules/a.md":    "# \x00",
		"AGENTS.md":     "\xff\xfe",
		"config.yml":    "a: \xc3\x28\n",
		"x/c.yaml":      "\x00",
		"mcp.json":      "{\"a\":\"\x80\"}",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeTree(t, dir, map[string]string{name: content})
			for _, msg := range pushErr(t, dir, nil, projectScope, globalScope) {
				assert.Contains(t, msg, name+" 必须是 UTF-8 文本")
			}
		})
	}

	// 非文本路径的二进制内容照常打包；含 NUL 也照样做密钥告警
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"skills/x/blob.bin": "\x00\x01sk-abcdefghijklmnopqrstuvwxyz\x00"})
	var stderr bytes.Buffer
	_, err := buildConfigTar(configScope{project: "demo"}, dir, nil, &stderr)
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "警告：skills/x/blob.bin 含形似密钥的字符串")
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

func TestConfigPullRejectsLinkedParent(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "elsewhere")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	for name, target := range map[string]string{"abs": outside, "rel": "../elsewhere"} {
		t.Run(name, func(t *testing.T) {
			f := projectsAPI(t)
			f.json("GET /api/v1/projects/prj_A/agent-config/files", 200, apigen.AgentConfigFileList{Files: []apigen.AgentConfigFile{
				{Path: "config.yml"}, {Path: "rules/a.md"},
			}})
			dir := filepath.Join(base, ".omp-"+name)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			// 仓库里提交的 .omp/rules → 别处（相对目标从 dir 解析，同样指向 base/elsewhere）
			require.NoError(t, os.Symlink(target, filepath.Join(dir, "rules")))
			err := configPull(context.Background(), f.client(), configScope{project: "demo"}, dir, false, false, io.Discard, io.Discard)
			require.ErrorContains(t, err, "父目录经符号链接指向目标目录之外")
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			assert.Empty(t, entries, "不写到链接指向的目录")
			_, err = os.Stat(filepath.Join(dir, "config.yml"))
			assert.True(t, errors.Is(err, os.ErrNotExist), "先校验全部路径再写入")
			for _, r := range f.requests() {
				assert.False(t, strings.Contains(r.Path, "/files/"), "不下载任何文件：%s", r.Path)
			}
		})
	}
}

func TestConfigPullRejectsLinkedTarget(t *testing.T) {
	base := t.TempDir()
	victim := filepath.Join(base, "victim.yml")
	require.NoError(t, os.WriteFile(victim, []byte("orig"), 0o644))
	dir := filepath.Join(base, ".omp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Symlink(victim, filepath.Join(dir, "config.yml")))

	f := projectsAPI(t)
	f.json("GET /api/v1/projects/prj_A/agent-config/files", 200, apigen.AgentConfigFileList{Files: []apigen.AgentConfigFile{{Path: "config.yml"}}})
	err := configPull(context.Background(), f.client(), configScope{project: "demo"}, dir, false, false, io.Discard, io.Discard)
	require.ErrorContains(t, err, "目标是符号链接")
	b, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, "orig", string(b))
	st, err := os.Lstat(filepath.Join(dir, "config.yml"))
	require.NoError(t, err)
	assert.NotZero(t, st.Mode()&os.ModeSymlink, "链接原样保留")
}

func TestPullTargetInRootLink(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "shared"), 0o755))
	require.NoError(t, os.Symlink("shared", filepath.Join(dir, "rules")))
	dst, _, err := pullTarget(dir, "rules/a.md", false)
	require.NoError(t, err, "父目录链接仍在目标目录内时允许")
	assert.Equal(t, filepath.Join(dir, "rules", "a.md"), dst)
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
