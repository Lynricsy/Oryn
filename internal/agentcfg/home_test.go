package agentcfg_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
)

var _ agentcfg.Resolver = (*secrets.Vault)(nil)

func newHome(t *testing.T) (*agentcfg.Home, *secrets.Vault, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(make([]byte, 32))
	require.NoError(t, err)
	h, err := agentcfg.NewHome(dir)
	require.NoError(t, err)
	return h, secrets.NewVault(st, box), dir
}

func TestCleanPath(t *testing.T) {
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", "..", ".", "a\\b", "a\x00b", "sessions/x.jsonl", "auth.json", "agent.db"} {
		_, err := agentcfg.CleanPath(bad)
		assert.Error(t, err, bad)
	}
	p, err := agentcfg.CleanPath("skills/./foo/SKILL.md")
	require.NoError(t, err)
	assert.Equal(t, "skills/foo/SKILL.md", p)
	p, err = agentcfg.CleanPath("a/b/../c")
	require.NoError(t, err)
	assert.Equal(t, "a/c", p)
}

const modelsYML = `providers:
  mine:
    baseUrl: https://llm.example.com/v1
    apiKey: sk-literal-abcdefghijklmnop
  weird:
    apiKey: plain-secret-value
`

func TestHomePutGetDelete(t *testing.T) {
	ctx := context.Background()
	h, v, _ := newHome(t)

	res, err := h.Put(ctx, "models.yml", []byte(modelsYML), 0, v)
	require.NoError(t, err)
	assert.Equal(t, []string{"omp/models/mine/apiKey", "omp/models/weird/apiKey"}, res.ExtractedSecrets)
	assert.Equal(t, fs.FileMode(0o644), res.File.Mode)
	// weird 没有 baseUrl 且不在内置表 → 环境变量降级警告
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, agentcfg.WarnEnvFallback, res.Warnings[0].Kind)

	b, fi, err := h.Get("models.yml")
	require.NoError(t, err)
	assert.NotContains(t, string(b), "sk-literal")
	assert.Contains(t, string(b), "${oryn:secret:omp/models/mine/apiKey}")
	assert.Equal(t, res.File.SHA256, fi.SHA256)
	val, meta, ok, err := v.Get(ctx, "omp/models/mine/apiKey")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "sk-literal-abcdefghijklmnop", val)
	assert.Equal(t, []string{"llm.example.com"}, meta.Hosts, "hosts 取自 provider 的 baseUrl")

	res, err = h.Put(ctx, "agents/AGENTS.md", []byte("token ghp_abcdefghijklmnopqrstuvwx\n"), 0o600, v)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o600), res.File.Mode)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, agentcfg.WarnPossibleSecret, res.Warnings[0].Kind)
	assert.Equal(t, "agents/AGENTS.md", res.Warnings[0].Path)

	_, err = h.Put(ctx, "../escape", []byte("x"), 0, v)
	assert.ErrorIs(t, err, agentcfg.ErrInvalidPath)
	_, err = h.Put(ctx, "auth.json", []byte("{}"), 0, v)
	assert.ErrorIs(t, err, agentcfg.ErrExcludedPath)
	_, err = h.Put(ctx, "models.yml", []byte("providers: [unclosed"), 0, v)
	assert.ErrorIs(t, err, agentcfg.ErrInvalidContent)

	list, err := h.List()
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "agents/AGENTS.md", list[0].Path)

	modes, err := h.Modes()
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o600), modes["agents/AGENTS.md"])

	require.NoError(t, h.Delete("agents/AGENTS.md"))
	assert.NoDirExists(t, filepath.Join(h.Dir(), "agents"))
	assert.ErrorIs(t, h.Delete("agents/AGENTS.md"), fs.ErrNotExist)
	_, _, err = h.Get("nope.md")
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

type tarEntry struct {
	name string
	body string
	typ  byte
	mode int64
}

func mkTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Size: int64(len(e.body))}
		if e.typ == tar.TypeSymlink {
			hdr.Linkname, hdr.Size = e.body, 0
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if e.typ == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func TestHomeImportTar(t *testing.T) {
	ctx := context.Background()
	h, v, _ := newHome(t)
	_, err := h.Put(ctx, "old.md", []byte("stale"), 0, v)
	require.NoError(t, err)

	res, err := h.ImportTar(ctx, bytes.NewReader(mkTar(t, []tarEntry{
		{name: "skills/", typ: tar.TypeDir, mode: 0o755},
		{name: "skills/a/SKILL.md", body: "# a", typ: tar.TypeReg, mode: 0o644},
		{name: "./config.yml", body: "theme: dark\n", typ: tar.TypeReg, mode: 0o600},
		{name: "models.yml", body: modelsYML, typ: tar.TypeReg, mode: 0o644},
		{name: "sessions/x.jsonl", body: "{}", typ: tar.TypeReg, mode: 0o644},
		{name: "link", body: "/etc/passwd", typ: tar.TypeSymlink},
	})), v)
	require.NoError(t, err)
	paths := make([]string, 0, len(res.Files))
	for _, f := range res.Files {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"config.yml", "models.yml", "skills/a/SKILL.md"}, paths)
	assert.Equal(t, []string{"omp/models/mine/apiKey", "omp/models/weird/apiKey"}, res.ExtractedSecrets)
	kinds := map[string]string{}
	for _, w := range res.Warnings {
		kinds[w.Path] = w.Kind
	}
	assert.Equal(t, agentcfg.WarnSkipped, kinds["link"])
	assert.Equal(t, agentcfg.WarnEnvFallback, kinds["models.yml"])

	all, err := h.LoadAll()
	require.NoError(t, err)
	assert.Equal(t, "theme: dark\n", string(all["config.yml"]))
	assert.NotContains(t, all, "old.md")
	assert.NotContains(t, string(all["models.yml"]), "sk-literal")

	// 没有残留的临时目录
	entries, err := os.ReadDir(filepath.Dir(h.Dir()))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), agentcfg.HomeDirName+"."), e.Name())
	}

	// 路径逃逸使整包失败且不改动现状
	_, err = h.ImportTar(ctx, bytes.NewReader(mkTar(t, []tarEntry{{name: "../evil", body: "x", typ: tar.TypeReg}})), v)
	assert.ErrorIs(t, err, agentcfg.ErrInvalidPath)
	_, err = h.ImportTar(ctx, strings.NewReader("not a tar"), v)
	assert.True(t, errors.Is(err, agentcfg.ErrInvalidContent), err)
	all, err = h.LoadAll()
	require.NoError(t, err)
	assert.Len(t, all, 3)
}
