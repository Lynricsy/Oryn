package agentcfg_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
)

func newProjectHomes(t *testing.T) (*agentcfg.ProjectHomes, string) {
	t.Helper()
	dir := t.TempDir()
	p, err := agentcfg.NewProjectHomes(dir)
	require.NoError(t, err)
	return p, dir
}

func paths(t *testing.T, h *agentcfg.Home) []string {
	t.Helper()
	list, err := h.List()
	require.NoError(t, err)
	out := []string{}
	for _, f := range list {
		out = append(out, f.Path)
	}
	return out
}

func tarOf(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return &buf
}

func TestProjectExcluded(t *testing.T) {
	for _, p := range []string{"models.yml", "plans/a.md", "plans", "sessions/x.jsonl", "auth.json", "ssh.json"} {
		assert.True(t, agentcfg.ProjectExcluded(p), p)
	}
	for _, p := range []string{"rules/a.md", "config.yml", "mcp.json", "skills/plans/SKILL.md", "rules/models.yml"} {
		assert.False(t, agentcfg.ProjectExcluded(p), p)
	}
}

func TestProjectHomesInvalidID(t *testing.T) {
	p, _ := newProjectHomes(t)
	for _, bad := range []string{"", "../x", "a/b", "a.b", "..", strings.Repeat("a", 65)} {
		_, err := p.For(bad)
		assert.ErrorIs(t, err, agentcfg.ErrInvalidPath, bad)
		assert.ErrorIs(t, p.Remove(bad), agentcfg.ErrInvalidPath, bad)
		_, _, err = p.Load(bad)
		assert.ErrorIs(t, err, agentcfg.ErrInvalidPath, bad)
	}
	_, err := p.For(strings.Repeat("a", 64))
	assert.NoError(t, err)
}

// 未写过的项目：列表与 Load 为空，且不建目录。
func TestProjectHomesEmptyNoDir(t *testing.T) {
	p, dir := newProjectHomes(t)
	h, err := p.For("prj_A")
	require.NoError(t, err)
	assert.Empty(t, paths(t, h))
	files, modes, err := p.Load("prj_A")
	require.NoError(t, err)
	assert.Empty(t, files)
	assert.Empty(t, modes)
	_, err = os.Stat(filepath.Join(dir, agentcfg.ProjectsDirName, "prj_A"))
	assert.ErrorIs(t, err, fs.ErrNotExist)
	_, _, err = h.Get("rules/a.md")
	assert.ErrorIs(t, err, fs.ErrNotExist)
	assert.ErrorIs(t, h.Delete("rules/a.md"), fs.ErrNotExist)
	h2, err := p.For("prj_A")
	require.NoError(t, err)
	assert.Same(t, h, h2, "同一项目共用一个 *Home")
}

// 两个项目与全局互不可见。
func TestProjectHomesIsolation(t *testing.T) {
	ctx := context.Background()
	p, dir := newProjectHomes(t)
	global, err := agentcfg.NewHome(dir)
	require.NoError(t, err)
	a, err := p.For("prj_A")
	require.NoError(t, err)
	b, err := p.For("prj_B")
	require.NoError(t, err)

	res, err := a.Put(ctx, "rules/a.md", []byte("# a"), 0o600, nil)
	require.NoError(t, err)
	assert.Equal(t, "rules/a.md", res.File.Path)
	_, err = b.Put(ctx, "skills/b/SKILL.md", []byte("# b"), 0, nil)
	require.NoError(t, err)
	_, err = global.Put(ctx, "rules/g.md", []byte("# g"), 0, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"rules/a.md"}, paths(t, a))
	assert.Equal(t, []string{"skills/b/SKILL.md"}, paths(t, b))
	assert.Equal(t, []string{"rules/g.md"}, paths(t, global))
	_, _, err = b.Get("rules/a.md")
	assert.ErrorIs(t, err, fs.ErrNotExist)

	files, modes, err := p.Load("prj_A")
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{"rules/a.md": []byte("# a")}, files)
	assert.Equal(t, map[string]os.FileMode{"rules/a.md": 0o600}, modes)
	assert.DirExists(t, filepath.Join(dir, agentcfg.ProjectsDirName, "prj_A", "rules"))
}

func TestProjectHomeExcluded(t *testing.T) {
	ctx := context.Background()
	p, dir := newProjectHomes(t)
	h, err := p.For("prj_A")
	require.NoError(t, err)
	for _, bad := range []string{"models.yml", "plans/a.md", "auth.json"} {
		_, err := h.Put(ctx, bad, []byte("x"), 0, nil)
		assert.ErrorIs(t, err, agentcfg.ErrExcludedPath, bad)
		_, _, err = h.Get(bad)
		assert.ErrorIs(t, err, agentcfg.ErrExcludedPath, bad)
		assert.ErrorIs(t, h.Delete(bad), agentcfg.ErrExcludedPath, bad)
	}
	_, err = h.Put(ctx, "../x", []byte("x"), 0, nil)
	assert.ErrorIs(t, err, agentcfg.ErrInvalidPath)
	// 全局仍接受 models.yml 与 plans/（项目规则不外溢）
	global, err := agentcfg.NewHome(dir)
	require.NoError(t, err)
	_, err = global.Put(ctx, "plans/a.md", []byte("x"), 0, nil)
	assert.NoError(t, err)
	assert.Equal(t, agentcfg.MaxFileSize, global.MaxFileSize())
	assert.Equal(t, agentcfg.ProjectMaxFileSize, h.MaxFileSize())
	assert.Equal(t, agentcfg.ProjectMaxTotalSize, h.MaxTotalSize())
}

func TestProjectHomeQuota(t *testing.T) {
	ctx := context.Background()
	p, _ := newProjectHomes(t)
	h, err := p.For("prj_A")
	require.NoError(t, err)

	_, err = h.Put(ctx, "big.md", make([]byte, agentcfg.ProjectMaxFileSize+1), 0, nil)
	assert.ErrorIs(t, err, agentcfg.ErrTooLarge)

	// 16 个 1 MiB 文件恰好到合计上限；第 17 个超限，但覆盖已有文件不算新增
	full := bytes.Repeat([]byte("x"), agentcfg.ProjectMaxFileSize)
	for i := range agentcfg.ProjectMaxTotalSize / agentcfg.ProjectMaxFileSize {
		_, err := h.Put(ctx, fmt.Sprintf("f/%02d.md", i), full, 0, nil)
		require.NoError(t, err, i)
	}
	_, err = h.Put(ctx, "over.md", []byte("x"), 0, nil)
	assert.ErrorIs(t, err, agentcfg.ErrTooLarge)
	_, err = h.Put(ctx, "f/00.md", full, 0, nil)
	assert.NoError(t, err, "覆盖同路径不计旧内容")
	_, err = os.Stat(filepath.Join(filepath.Dir(h.Dir()), "prj_A", "over.md"))
	assert.ErrorIs(t, err, fs.ErrNotExist)

	// tar 导入同样受项目配额约束
	_, err = h.ImportTar(ctx, tarOf(t, map[string]string{"a.md": strings.Repeat("x", agentcfg.ProjectMaxFileSize+1)}), nil)
	assert.ErrorIs(t, err, agentcfg.ErrTooLarge)
}

func TestProjectHomeFileCountQuota(t *testing.T) {
	ctx := context.Background()
	p, _ := newProjectHomes(t)
	h, err := p.For("prj_A")
	require.NoError(t, err)
	files := map[string]string{}
	for i := range agentcfg.ProjectMaxFiles {
		files[fmt.Sprintf("r/%04d.md", i)] = ""
	}
	require.Len(t, files, agentcfg.ProjectMaxFiles)
	_, err = h.ImportTar(ctx, tarOf(t, files), nil)
	require.NoError(t, err)
	_, err = h.Put(ctx, "one-more.md", []byte("x"), 0, nil)
	assert.ErrorIs(t, err, agentcfg.ErrTooLarge)
	files["one-more.md"] = ""
	_, err = h.ImportTar(ctx, tarOf(t, files), nil)
	assert.ErrorIs(t, err, agentcfg.ErrTooLarge)
}

// config.yml 在全局与项目层同一套校验：只收一个 YAML 文档，且为空、null 或顶层映射（PUT 与 tar 导入都管）。
// 多文档在 Core（Bun.YAML.parse）里是数组，会让 agent 起不来。
func TestHomeConfigYAML(t *testing.T) {
	ctx := context.Background()
	p, dir := newProjectHomes(t)
	prj, err := p.For("prj_A")
	require.NoError(t, err)
	global, err := agentcfg.NewHome(dir)
	require.NoError(t, err)
	bads := []string{
		"- a\n", "a: [\n", "just a string\n",
		"theme: dark\n---\ntheme: light\n", "theme: dark\n---\n", "~\n---\n~\n", "a: 1\n...\n---\n", "a: 1\n---\nb: [\n",
	}
	for name, h := range map[string]*agentcfg.Home{"project": prj, "global": global} {
		for _, bad := range bads {
			_, err := h.Put(ctx, agentcfg.ConfigFile, []byte(bad), 0, nil)
			assert.ErrorIs(t, err, agentcfg.ErrInvalidContent, "%s %q", name, bad)
			_, err = h.ImportTar(ctx, tarOf(t, map[string]string{agentcfg.ConfigFile: bad}), nil)
			assert.ErrorIs(t, err, agentcfg.ErrInvalidContent, "%s %q", name, bad)
		}
		assert.Empty(t, paths(t, h), name)
		for _, ok := range []string{"theme: dark\n", "", "# 只有注释\n", "~\n", "---\ntheme: dark\n...\n"} {
			_, err := h.Put(ctx, agentcfg.ConfigFile, []byte(ok), 0, nil)
			assert.NoError(t, err, "%s %q", name, ok)
		}
		// 只有根目录的 config.yml 是 omp 配置
		_, err = h.Put(ctx, "rules/config.yml", []byte("- a\n"), 0, nil)
		assert.NoError(t, err, name)
	}
}

// 必须是文本的路径（rules/**、*.md、*.yml、*.yaml、*.json）拒收含 NUL 或非 UTF-8 的内容，全局与项目层、PUT 与导入都管；
// 其它路径（扩展脚本、图片等）照收。
func TestHomeTextOnlyPaths(t *testing.T) {
	ctx := context.Background()
	p, dir := newProjectHomes(t)
	prj, err := p.For("prj_A")
	require.NoError(t, err)
	global, err := agentcfg.NewHome(dir)
	require.NoError(t, err)
	environ := "GITHUB_TOKEN=ghp_x\x00AWS_SECRET=y\x00"
	latin1 := "caf\xe9"
	textPaths := []string{"rules/env.md", "rules/env", "AGENTS.md", "SYSTEM.md", "skills/x/SKILL.MD", "mcp.json", "config.yml", "agents/a.yaml"}
	for name, h := range map[string]*agentcfg.Home{"project": prj, "global": global} {
		for _, rel := range textPaths {
			for _, body := range []string{environ, latin1} {
				_, err := h.Put(ctx, rel, []byte(body), 0, nil)
				assert.ErrorIs(t, err, agentcfg.ErrInvalidContent, "%s %s %q", name, rel, body)
				_, err = h.ImportTar(ctx, tarOf(t, map[string]string{rel: body}), nil)
				assert.ErrorIs(t, err, agentcfg.ErrInvalidContent, "%s %s %q", name, rel, body)
			}
		}
		assert.Empty(t, paths(t, h), name)
		for _, rel := range []string{"extensions/a.bin", "skills/x/logo.png", "notes.txt"} {
			_, err := h.Put(ctx, rel, []byte(environ), 0, nil)
			assert.NoError(t, err, "%s %s", name, rel)
		}
		_, err = h.Put(ctx, "rules/ok.md", []byte("# 中文规则 ✓\n"), 0, nil)
		assert.NoError(t, err, name)
	}
}

// ImportTar 只替换本项目；被排除的路径静默跳过。
func TestProjectHomeImportTar(t *testing.T) {
	ctx := context.Background()
	p, dir := newProjectHomes(t)
	a, err := p.For("prj_A")
	require.NoError(t, err)
	b, err := p.For("prj_B")
	require.NoError(t, err)
	_, err = a.Put(ctx, "rules/old.md", []byte("old"), 0, nil)
	require.NoError(t, err)
	_, err = b.Put(ctx, "rules/b.md", []byte("b"), 0, nil)
	require.NoError(t, err)

	res, err := a.ImportTar(ctx, tarOf(t, map[string]string{
		"rules/new.md": "new", "./mcp.json": "{}", "models.yml": "providers: {}\n", "plans/x.md": "x", "config.yml": "theme: dark\n",
	}), nil)
	require.NoError(t, err)
	got := []string{}
	for _, f := range res.Files {
		got = append(got, f.Path)
	}
	assert.Equal(t, []string{"config.yml", "mcp.json", "rules/new.md"}, got)
	assert.Equal(t, got, paths(t, a))
	assert.Equal(t, []string{"rules/b.md"}, paths(t, b))
	// 临时目录与旧目录都已清理
	entries, err := os.ReadDir(filepath.Join(dir, agentcfg.ProjectsDirName))
	require.NoError(t, err)
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"prj_A", "prj_B"}, names)
}

func TestProjectHomesRemove(t *testing.T) {
	ctx := context.Background()
	p, dir := newProjectHomes(t)
	a, err := p.For("prj_A")
	require.NoError(t, err)
	_, err = a.Put(ctx, "rules/a.md", []byte("a"), 0, nil)
	require.NoError(t, err)
	b, err := p.For("prj_B")
	require.NoError(t, err)
	_, err = b.Put(ctx, "rules/b.md", []byte("b"), 0, nil)
	require.NoError(t, err)

	require.NoError(t, p.Remove("prj_A"))
	_, err = os.Stat(filepath.Join(dir, agentcfg.ProjectsDirName, "prj_A"))
	assert.True(t, errors.Is(err, fs.ErrNotExist), "%v", err)
	a2, err := p.For("prj_A")
	require.NoError(t, err)
	assert.Empty(t, paths(t, a2))
	assert.Equal(t, []string{"rules/b.md"}, paths(t, b))
	require.NoError(t, p.Remove("prj_never"), "目录不存在不报错")
}

// Remove 之后：删除前借出的 *Home 与之后 For 取到的是同一个（同一目录只有一把锁），其上的 Put / ImportTar / Delete
// 一律 ErrProjectRemoved（按不存在处理），不会把已删项目的目录重新建出来。
func TestProjectHomesRemoveInvalidatesHome(t *testing.T) {
	ctx := context.Background()
	p, dir := newProjectHomes(t)
	prjDir := filepath.Join(dir, agentcfg.ProjectsDirName, "prj_A")
	stale, err := p.For("prj_A")
	require.NoError(t, err)
	_, err = stale.Put(ctx, "rules/a.md", []byte("a"), 0, nil)
	require.NoError(t, err)

	require.NoError(t, p.Remove("prj_A"))
	fresh, err := p.For("prj_A")
	require.NoError(t, err)
	assert.Same(t, stale, fresh, "Remove 之后 For 不能另建一个持有别的锁的 *Home")

	for name, h := range map[string]*agentcfg.Home{"stale": stale, "fresh": fresh} {
		_, err = h.Put(ctx, "rules/b.md", []byte("b"), 0, nil)
		assert.ErrorIs(t, err, agentcfg.ErrProjectRemoved, name)
		assert.ErrorIs(t, err, fs.ErrNotExist, name)
		_, err = h.ImportTar(ctx, tarOf(t, map[string]string{"rules/c.md": "c"}), nil)
		assert.ErrorIs(t, err, agentcfg.ErrProjectRemoved, name)
		assert.ErrorIs(t, h.Delete("rules/a.md"), agentcfg.ErrProjectRemoved, name)
		assert.Empty(t, paths(t, h), name)
	}
	_, err = os.Lstat(prjDir)
	assert.ErrorIs(t, err, fs.ErrNotExist, "已删项目的目录不能复活")
	files, modes, err := p.Load("prj_A")
	require.NoError(t, err)
	assert.Empty(t, files)
	assert.Empty(t, modes)
	entries, err := os.ReadDir(filepath.Join(dir, agentcfg.ProjectsDirName))
	require.NoError(t, err)
	assert.Empty(t, entries, "导入的临时目录也已清理")
}

// 全局层与项目层随同一条 ctl 消息下发：写入时两层按下发编码（EntrySize）合计不超过 DeliveryBudget，超了 413
// （ErrTooLarge）；项目层的检查加全局层，全局层的检查加最大的项目层；删除总是允许。
func TestDeliveryBudget(t *testing.T) {
	ctx := context.Background()
	p, dir := newProjectHomes(t)
	global, err := agentcfg.NewHome(dir)
	require.NoError(t, err)
	prj, err := p.For("prj_A")
	require.NoError(t, err)

	// 全局 4×16 MiB（编码后约 85.3 MiB，此前单层就能下发）。直接落盘：预算按磁盘计，省掉 64 MiB 的密钥扫描
	big := bytes.Repeat([]byte("g"), agentcfg.MaxFileSize)
	for i := range 4 {
		require.NoError(t, os.MkdirAll(filepath.Join(global.Dir(), "g"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(global.Dir(), "g", fmt.Sprintf("%d.md", i)), big, 0o644))
	}
	used := 4 * agentcfg.EntrySize("g/0.md", int64(len(big)))
	mb := bytes.Repeat([]byte("p"), agentcfg.ProjectMaxFileSize)
	one := agentcfg.EntrySize("rules/0.md", int64(len(mb)))
	require.LessOrEqual(t, used+one, int64(agentcfg.DeliveryBudget), "前提：再放一个 1 MiB 项目文件仍在预算内")
	require.Greater(t, used+2*one, int64(agentcfg.DeliveryBudget), "前提：两个就超")

	_, err = prj.Put(ctx, "rules/0.md", mb, 0, nil)
	require.NoError(t, err)
	_, err = prj.Put(ctx, "rules/1.md", mb, 0, nil)
	assert.ErrorIs(t, err, agentcfg.ErrTooLarge)
	assert.Contains(t, err.Error(), "单次下发上限")
	_, err = prj.ImportTar(ctx, tarOf(t, map[string]string{"rules/0.md": string(mb), "rules/1.md": string(mb)}), nil)
	assert.ErrorIs(t, err, agentcfg.ErrTooLarge)
	_, err = prj.Put(ctx, "rules/0.md", mb, 0, nil)
	assert.NoError(t, err, "覆盖同路径不计旧内容")
	assert.Equal(t, []string{"rules/0.md"}, paths(t, prj))

	// 全局层的检查加上最大的项目层，报出是哪个项目
	_, err = global.Put(ctx, "g/extra.md", mb, 0, nil)
	assert.ErrorIs(t, err, agentcfg.ErrTooLarge)
	assert.Contains(t, err.Error(), "prj_A")
	// 其它项目同样受全局层约束
	other, err := p.For("prj_B")
	require.NoError(t, err)
	_, err = other.Put(ctx, "rules/x.md", mb, 0, nil)
	assert.NoError(t, err)
	_, err = other.Put(ctx, "rules/y.md", mb, 0, nil)
	assert.ErrorIs(t, err, agentcfg.ErrTooLarge)

	// 删除总是允许；腾出空间后写入恢复
	require.NoError(t, global.Delete("g/3.md"))
	_, err = prj.Put(ctx, "rules/1.md", mb, 0, nil)
	assert.NoError(t, err)
	_, err = global.Put(ctx, "g/extra.md", mb, 0, nil)
	assert.NoError(t, err)
}
