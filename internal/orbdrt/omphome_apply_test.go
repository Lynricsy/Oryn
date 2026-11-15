package orbdrt

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func ompFile(path, content string) apigen.LinkCtlOmpHomeFile {
	return apigen.LinkCtlOmpHomeFile{Path: path, Mode: 0o644, ContentB64: base64.StdEncoding.EncodeToString([]byte(content))}
}

// omphome.apply：更新托管文件、清理被移除的托管文件、保留 omp 自己写的文件。
func TestOmpHomeApply(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)

	base := filepath.Join(rt.home, ".omp", "agent")
	setup := apigen.LinkCtlSetupRunParams{
		GitHosts: []string{}, Env: map[string]string{},
		OmpHome: []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "v1"), ompFile("skills/x/SKILL.md", "x")},
	}
	require.NoError(t, cp.call(t, "setup.run", setup, nil))
	require.NoError(t, os.WriteFile(filepath.Join(base, "agent.db"), []byte("omp"), 0o644))

	var res apigen.LinkCtlOkResult
	require.NoError(t, cp.call(t, string(apigen.MethodOmpHomeApply),
		apigen.LinkCtlOmpHomeApplyParams{OmpHome: []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "v2")}}, &res))
	assert.True(t, res.Ok)

	b, err := os.ReadFile(filepath.Join(base, "config.yml"))
	require.NoError(t, err)
	assert.Equal(t, "v2", string(b))
	assert.NoFileExists(t, filepath.Join(base, "skills/x/SKILL.md"), "被删的托管文件被清理")
	assert.FileExists(t, filepath.Join(base, "agent.db"), "非托管文件保留")
}

// 先到达、后写入的 setup（等互斥时 omphome.apply 已写过）跳过写入，不用旧内容覆盖新内容。
func TestOmpHomeWriteOrder(t *testing.T) {
	r := &Runtime{home: t.TempDir()}
	setupSeq := r.nextOmpSeq()
	applySeq := r.nextOmpSeq()

	skipped, err := r.writeOmpHomeAt(applySeq, []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "new")})
	require.NoError(t, err)
	assert.False(t, skipped)
	skipped, err = r.writeOmpHomeAt(setupSeq, []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "old"), ompFile("stale.md", "s")})
	require.NoError(t, err)
	assert.True(t, skipped)

	base := filepath.Join(r.home, ".omp", "agent")
	b, err := os.ReadFile(filepath.Join(base, "config.yml"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(b))
	assert.NoFileExists(t, filepath.Join(base, "stale.md"))

	// 之后到达的请求照常写入。
	skipped, err = r.writeOmpHomeAt(r.nextOmpSeq(), []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "later")})
	require.NoError(t, err)
	assert.False(t, skipped)
}
