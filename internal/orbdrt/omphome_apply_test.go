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

// omphome.apply：更新托管文件、清理被移除的托管文件、保留 omp 自己写的文件；不带 projectConfig 时项目层不动，带了就镜像。
func TestOmpHomeApply(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)

	base := filepath.Join(rt.home, ".omp", "agent")
	proj := filepath.Join(rt.home, ".oryn", "project-config", ".omp")
	setup := apigen.LinkCtlSetupRunParams{
		GitHosts: []string{}, Env: map[string]string{},
		OmpHome:       []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "v1"), ompFile("skills/x/SKILL.md", "x")},
		ProjectConfig: &apigen.LinkCtlProjectConfig{Files: []apigen.LinkCtlOmpHomeFile{ompFile("AGENTS.md", "p1")}},
	}
	require.NoError(t, cp.call(t, "setup.run", setup, nil))
	require.NoError(t, os.WriteFile(filepath.Join(base, "agent.db"), []byte("omp"), 0o644))

	var res apigen.LinkCtlOkResult
	require.NoError(t, cp.call(t, string(apigen.MethodOmpHomeApply),
		apigen.LinkCtlOmpHomeApplyParams{OmpHome: []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "v2")}}, &res))
	assert.True(t, res.Ok)
	assert.Equal(t, "v2", readFile(t, filepath.Join(base, "config.yml")))
	assert.NoFileExists(t, filepath.Join(base, "skills/x/SKILL.md"), "被删的托管文件被清理")
	assert.FileExists(t, filepath.Join(base, "agent.db"), "非托管文件保留")
	assert.Equal(t, "p1", readFile(t, filepath.Join(proj, "AGENTS.md")), "不带 projectConfig 时项目层不动")

	require.NoError(t, cp.call(t, string(apigen.MethodOmpHomeApply), apigen.LinkCtlOmpHomeApplyParams{
		OmpHome:       []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "v2")},
		ProjectConfig: &apigen.LinkCtlProjectConfig{Files: []apigen.LinkCtlOmpHomeFile{ompFile("RULES.md", "r")}},
	}, &res))
	assert.Equal(t, "r", readFile(t, filepath.Join(proj, "RULES.md")))
	assert.NoFileExists(t, filepath.Join(proj, "AGENTS.md"), "项目层按清单镜像")
}

// 先到达、后写入的 setup（等互斥时 omphome.apply 已写过）跳过 apply 写过的层，其余层照常写入。
func TestOmpHomeWriteOrder(t *testing.T) {
	r := &Runtime{home: t.TempDir()}
	base := filepath.Join(r.home, ".omp", "agent")
	proj := filepath.Join(r.home, ".oryn", "project-config", ".omp")
	setupSeq := r.nextOmpSeq()
	applySeq := r.nextOmpSeq()

	home, project, err := r.writeOmpConfigAt(applySeq, []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "new")}, nil, false)
	require.NoError(t, err)
	assert.True(t, home)
	assert.False(t, project)

	home, project, err = r.writeOmpConfigAt(setupSeq, []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "old"), ompFile("stale.md", "s")},
		&apigen.LinkCtlProjectConfig{Files: []apigen.LinkCtlOmpHomeFile{ompFile("AGENTS.md", "p")}}, true)
	require.NoError(t, err)
	assert.False(t, home, "用户层已有更新的内容")
	assert.True(t, project, "apply 没碰项目层，setup 照常写")
	assert.Equal(t, "new", readFile(t, filepath.Join(base, "config.yml")))
	assert.NoFileExists(t, filepath.Join(base, "stale.md"))
	assert.Equal(t, "p", readFile(t, filepath.Join(proj, "AGENTS.md")))

	// 之后到达的请求照常写入。
	home, _, err = r.writeOmpConfigAt(r.nextOmpSeq(), []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "later")}, nil, false)
	require.NoError(t, err)
	assert.True(t, home)
}
