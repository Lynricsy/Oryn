package agentcfg_test

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
)

func batchPaths(files []agentcfg.FileInfo) []string {
	out := []string{}
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// 替换目录：同批 delete 目录 + 写同目录新文件，旧文件消失；未匹配的 delete 忽略；mode 生效；二进制内容原样落盘。
func TestHomeApplyReplaceDir(t *testing.T) {
	ctx := context.Background()
	h, v, _ := newHome(t)
	for _, p := range []string{"skills/a/SKILL.md", "skills/a/scripts/old.sh", "skills/b/SKILL.md"} {
		_, err := h.Put(ctx, p, []byte("old"), 0, v)
		require.NoError(t, err)
	}
	bin := []byte{0x00, 0xff, 0x10, 0x80}
	res, err := h.Apply(ctx, agentcfg.Batch{
		Delete: []string{"skills/a", "nope/missing"},
		Files: []agentcfg.BatchFile{
			{Path: "skills/a/scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0o755},
			{Path: "skills/a/SKILL.md", Content: []byte("---\ndescription: a\n---\n")},
			{Path: "skills/a/assets/logo.bin", Content: bin},
		},
	}, v)
	require.NoError(t, err)
	assert.Equal(t, []string{"skills/a/SKILL.md", "skills/a/assets/logo.bin", "skills/a/scripts/run.sh"}, batchPaths(res.Files))
	assert.Equal(t, []string{"skills/a/scripts/old.sh"}, res.Deleted, "重新写入的 SKILL.md 不算删除")
	assert.Equal(t, []string{}, res.ExtractedSecrets)
	assert.Equal(t, fs.FileMode(0o755), res.Files[2].Mode)
	assert.Equal(t, fs.FileMode(0o644), res.Files[0].Mode)
	assert.NoFileExists(t, filepath.Join(h.Dir(), "skills/a/scripts/old.sh"))

	all, err := h.LoadAll()
	require.NoError(t, err)
	assert.Equal(t, bin, all["skills/a/assets/logo.bin"])
	assert.Equal(t, "old", string(all["skills/b/SKILL.md"]), "其它条目不受影响")
	assert.Len(t, all, 4)
}

// 目录删除：只删该目录下的配置文件，变空的父目录一并清掉；同名前缀的兄弟目录不受影响；运行时数据不碰。
func TestHomeApplyDeleteDir(t *testing.T) {
	ctx := context.Background()
	h, v, _ := newHome(t)
	for _, p := range []string{"extensions/x/index.ts", "extensions/x/lib/a.ts", "extensions/xy.ts"} {
		_, err := h.Put(ctx, p, []byte("x"), 0, v)
		require.NoError(t, err)
	}
	// 运行时数据（如依赖目录）不在配置之列，目录删除不碰它
	nm := filepath.Join(h.Dir(), "extensions/x/node_modules/dep/index.js")
	require.NoError(t, os.MkdirAll(filepath.Dir(nm), 0o700))
	require.NoError(t, os.WriteFile(nm, []byte("dep"), 0o600))

	res, err := h.Apply(ctx, agentcfg.Batch{Delete: []string{"extensions/x"}}, v)
	require.NoError(t, err)
	assert.Equal(t, []string{"extensions/x/index.ts", "extensions/x/lib/a.ts"}, res.Deleted)
	assert.Equal(t, []agentcfg.FileInfo{}, res.Files)
	assert.NoDirExists(t, filepath.Join(h.Dir(), "extensions/x/lib"))
	assert.FileExists(t, nm)
	assert.FileExists(t, filepath.Join(h.Dir(), "extensions/xy.ts"))

	// 全部未匹配：成功且什么都不删
	res, err = h.Apply(ctx, agentcfg.Batch{Delete: []string{"skills/none"}}, v)
	require.NoError(t, err)
	assert.Equal(t, []string{}, res.Deleted)
}

// 整批先校验：任何一项不合规则整批失败，盘上不变。
func TestHomeApplyRejects(t *testing.T) {
	ctx := context.Background()
	h, v, _ := newHome(t)
	_, err := h.Put(ctx, "skills/a/SKILL.md", []byte("keep"), 0, v)
	require.NoError(t, err)
	_, err = h.Put(ctx, "tools/t", []byte("keep"), 0, v)
	require.NoError(t, err)
	before, err := h.LoadAll()
	require.NoError(t, err)

	ok := agentcfg.BatchFile{Path: "skills/new/SKILL.md", Content: []byte("new")}
	for _, c := range []struct {
		name string
		b    agentcfg.Batch
		want error
	}{
		{"空批次", agentcfg.Batch{}, agentcfg.ErrInvalidContent},
		{"路径穿越", agentcfg.Batch{Files: []agentcfg.BatchFile{ok, {Path: "../x", Content: []byte("x")}}}, agentcfg.ErrInvalidPath},
		{"删除路径穿越", agentcfg.Batch{Delete: []string{"../x"}, Files: []agentcfg.BatchFile{ok}}, agentcfg.ErrInvalidPath},
		{"运行时数据", agentcfg.Batch{Delete: []string{"sessions"}, Files: []agentcfg.BatchFile{ok}}, agentcfg.ErrExcludedPath},
		{"重复路径", agentcfg.Batch{Files: []agentcfg.BatchFile{ok, {Path: "skills/./new/SKILL.md", Content: []byte("y")}}}, agentcfg.ErrInvalidPath},
		{"批内文件/目录冲突", agentcfg.Batch{Files: []agentcfg.BatchFile{ok, {Path: "skills/new", Content: []byte("y")}}}, agentcfg.ErrInvalidPath},
		{"与现有文件冲突", agentcfg.Batch{Files: []agentcfg.BatchFile{ok, {Path: "tools/t/index.ts", Content: []byte("y")}}}, agentcfg.ErrInvalidPath},
		{"与现有目录冲突", agentcfg.Batch{Files: []agentcfg.BatchFile{ok, {Path: "skills/a", Content: []byte("y")}}}, agentcfg.ErrInvalidPath},
		{"内容不合规则", agentcfg.Batch{Files: []agentcfg.BatchFile{ok, {Path: "config.yml", Content: []byte("- a\n")}}}, agentcfg.ErrInvalidContent},
		{"文本路径含 NUL", agentcfg.Batch{Files: []agentcfg.BatchFile{ok, {Path: "skills/x/SKILL.md", Content: []byte("a\x00")}}}, agentcfg.ErrInvalidContent},
		{"单文件超限", agentcfg.Batch{Files: []agentcfg.BatchFile{ok, {Path: "big.bin", Content: make([]byte, agentcfg.MaxFileSize+1)}}}, agentcfg.ErrTooLarge},
	} {
		_, err := h.Apply(ctx, c.b, v)
		assert.ErrorIs(t, err, c.want, c.name)
	}
	after, err := h.LoadAll()
	require.NoError(t, err)
	assert.Equal(t, before, after)

	// 先删掉现有文件再写同名目录下的文件不冲突
	res, err := h.Apply(ctx, agentcfg.Batch{Delete: []string{"tools/t"}, Files: []agentcfg.BatchFile{{Path: "tools/t/index.ts", Content: []byte("y")}}}, v)
	require.NoError(t, err)
	assert.Equal(t, []string{"tools/t"}, res.Deleted)
	assert.Equal(t, []string{"skills/a/SKILL.md", "tools/t/index.ts"}, paths(t, h))
}

// models.yml 做密钥抽取（全局层）；项目层拒绝 models.yml。
func TestHomeApplyModels(t *testing.T) {
	ctx := context.Background()
	h, v, _ := newHome(t)
	res, err := h.Apply(ctx, agentcfg.Batch{Files: []agentcfg.BatchFile{{Path: "models.yml", Content: []byte(modelsYML)}}}, v)
	require.NoError(t, err)
	assert.Equal(t, []string{"omp/models/mine/apiKey", "omp/models/weird/apiKey"}, res.ExtractedSecrets)
	kinds := map[string]bool{}
	for _, w := range res.Warnings {
		kinds[w.Kind] = true
	}
	assert.True(t, kinds[agentcfg.WarnEnvFallback], res.Warnings)
	b, _, err := h.Get("models.yml")
	require.NoError(t, err)
	assert.NotContains(t, string(b), "sk-literal")

	ph, _ := newProjectHomes(t)
	p, err := ph.For("prj_a")
	require.NoError(t, err)
	_, err = p.Apply(ctx, agentcfg.Batch{Files: []agentcfg.BatchFile{{Path: "models.yml", Content: []byte("providers: {}\n")}}}, nil)
	assert.ErrorIs(t, err, agentcfg.ErrExcludedPath)
	_, err = p.Apply(ctx, agentcfg.Batch{Delete: []string{"plans"}}, nil)
	assert.ErrorIs(t, err, agentcfg.ErrExcludedPath)
	assert.NoDirExists(t, p.Dir(), "校验失败不建目录")
}

// 项目配额按变更后的整层算：超限整批失败、盘上不变；同批删掉旧文件腾出空间则通过。只删不写的批次不查配额。
func TestHomeApplyProjectQuota(t *testing.T) {
	ctx := context.Background()
	ph, _ := newProjectHomes(t)
	h, err := ph.For("prj_q")
	require.NoError(t, err)
	full := bytes.Repeat([]byte("x"), agentcfg.ProjectMaxFileSize)
	n := agentcfg.ProjectMaxTotalSize / agentcfg.ProjectMaxFileSize
	files := make([]agentcfg.BatchFile, 0, n)
	for i := range n {
		files = append(files, agentcfg.BatchFile{Path: "skills/s/" + string(rune('a'+i)), Content: full})
	}
	_, err = h.Apply(ctx, agentcfg.Batch{Files: files}, nil)
	require.NoError(t, err)

	_, err = h.Apply(ctx, agentcfg.Batch{Files: []agentcfg.BatchFile{{Path: "rules/x.md", Content: []byte("x")}}}, nil)
	assert.ErrorIs(t, err, agentcfg.ErrTooLarge)
	assert.Len(t, paths(t, h), n, "超限不改盘")

	res, err := h.Apply(ctx, agentcfg.Batch{Delete: []string{"skills/s/a"}, Files: []agentcfg.BatchFile{{Path: "rules/x.md", Content: []byte("x")}}}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"skills/s/a"}, res.Deleted)

	res, err = h.Apply(ctx, agentcfg.Batch{Delete: []string{"skills"}}, nil)
	require.NoError(t, err)
	assert.Len(t, res.Deleted, n-1)
	assert.Equal(t, []string{"rules/x.md"}, paths(t, h))

	// 已删项目：写入返回 ErrProjectRemoved，不复活目录
	require.NoError(t, ph.Remove("prj_q"))
	_, err = h.Apply(ctx, agentcfg.Batch{Files: []agentcfg.BatchFile{{Path: "rules/y.md", Content: []byte("y")}}}, nil)
	assert.ErrorIs(t, err, agentcfg.ErrProjectRemoved)
	assert.NoDirExists(t, h.Dir())
}

// 写入在落盘时才失败（文件名段超过文件系统的 255 字节上限，CleanPath 的 1024 字节路径校验放得过）：
// 替换批次整体失败，被删除与将被覆盖的旧文件原样还在，没有半个新文件，也不留暂存目录。
func TestHomeApplyWriteFailureKeepsOld(t *testing.T) {
	ctx := context.Background()
	h, v, _ := newHome(t)
	old := map[string]string{
		"skills/a/SKILL.md":       "old skill",
		"skills/a/scripts/old.sh": "old script",
		"skills/b/SKILL.md":       "other",
	}
	for p, c := range old {
		_, err := h.Put(ctx, p, []byte(c), 0, v)
		require.NoError(t, err)
	}
	long := strings.Repeat("x", 256) + ".md"
	_, err := h.Apply(ctx, agentcfg.Batch{
		Delete: []string{"skills/a"},
		Files: []agentcfg.BatchFile{
			{Path: "skills/a/SKILL.md", Content: []byte("new skill")},
			{Path: "skills/a/new.md", Content: []byte("new")},
			{Path: "skills/a/" + long, Content: []byte("too long")},
		},
	}, v)
	require.Error(t, err)

	all, err := h.LoadAll()
	require.NoError(t, err)
	got := map[string]string{}
	for p, b := range all {
		got[p] = string(b)
	}
	assert.Equal(t, old, got)
	entries, err := os.ReadDir(filepath.Dir(h.Dir()))
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".batch-", "暂存目录已清理")
	}
}
