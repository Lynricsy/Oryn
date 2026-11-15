package orbdrt

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

const projThread2 = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ac"

func projFile(p, content string, mode int) apigen.LinkCtlOmpHomeFile {
	return apigen.LinkCtlOmpHomeFile{Path: p, Mode: mode, ContentB64: base64.StdEncoding.EncodeToString([]byte(content))}
}

func projCfg(files ...apigen.LinkCtlOmpHomeFile) *apigen.LinkCtlProjectConfig {
	if files == nil {
		files = []apigen.LinkCtlOmpHomeFile{}
	}
	return &apigen.LinkCtlProjectConfig{Files: files}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

func perm(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Lstat(p)
	require.NoError(t, err)
	require.True(t, st.Mode().IsRegular(), "%s 应是普通文件，实为 %v", p, st.Mode())
	return st.Mode().Perm()
}

// 严格镜像：写入清单内的文件（权限照下发）；下一次只留清单内的，其余文件（含 orb 内手放的）与空目录都删掉；
// 清单里的路径在磁盘上是目录时被替换成文件；空清单留下空的 .omp；nil 删除整个 <root>。
func TestWriteProjectOmpMirror(t *testing.T) {
	r := &Runtime{home: t.TempDir()}
	root := r.projectConfigRoot()
	assert.Equal(t, filepath.Join(r.home, ".oryn", "project-config"), root)
	base := filepath.Join(root, ".omp")

	require.NoError(t, r.writeProjectOmp(projCfg(
		projFile("rules/a.md", "A", 0o644),
		projFile("hooks/pre/run.sh", "#!/bin/sh\n", 0o755),
	)))
	assert.Equal(t, "A", readFile(t, filepath.Join(base, "rules/a.md")))
	assert.Equal(t, os.FileMode(0o644), perm(t, filepath.Join(base, "rules/a.md")))
	assert.Equal(t, "#!/bin/sh\n", readFile(t, filepath.Join(base, "hooks/pre/run.sh")))
	assert.Equal(t, os.FileMode(0o755), perm(t, filepath.Join(base, "hooks/pre/run.sh")))

	// orb 内手放的文件、技能目录、残留的临时文件；以及与清单路径同名的目录。
	writeTree(t, base, map[string]string{
		"rules/local.md":       "local",
		"skills/x/SKILL.md":    "x",
		"skills/x/.orbd-12345": "half",
		"mcp.json/inner":       "dir in the way",
	})
	require.NoError(t, r.writeProjectOmp(projCfg(
		projFile("rules/a.md", "A2", 0), // 0 → 0644
		projFile("mcp.json", "{}", 0o600),
	)))
	assert.Equal(t, "A2", readFile(t, filepath.Join(base, "rules/a.md")))
	assert.Equal(t, os.FileMode(0o644), perm(t, filepath.Join(base, "rules/a.md")))
	assert.Equal(t, "{}", readFile(t, filepath.Join(base, "mcp.json")))
	assert.Equal(t, os.FileMode(0o600), perm(t, filepath.Join(base, "mcp.json")))
	for _, gone := range []string{"rules/local.md", "hooks", "skills"} {
		assert.NoFileExists(t, filepath.Join(base, gone))
		assert.NoDirExists(t, filepath.Join(base, gone))
	}
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{"mcp.json", "rules"}, names)

	require.NoError(t, r.writeProjectOmp(projCfg()))
	entries, err = os.ReadDir(base)
	require.NoError(t, err)
	assert.Empty(t, entries, "属于项目但没有文件：.omp 留空")

	require.NoError(t, r.writeProjectOmp(nil))
	assert.NoDirExists(t, root, "不属于项目：删除整个 <root>")
	require.NoError(t, r.writeProjectOmp(nil), "目录不存在时也成功")
}

// 路径穿越：清单里任何一条非法（../、绝对路径、清理后逃逸、坏 base64）都整体报错，<root> 之外没有新文件，
// 已有的镜像保持原样（先校验再动磁盘）。
func TestWriteProjectOmpRejectsTraversal(t *testing.T) {
	r := &Runtime{home: t.TempDir()}
	base := filepath.Join(r.projectConfigRoot(), ".omp")
	require.NoError(t, r.writeProjectOmp(projCfg(projFile("rules/keep.md", "keep", 0o644))))

	for _, bad := range []apigen.LinkCtlOmpHomeFile{
		projFile("../x", "x", 0o644),
		projFile("../../x", "x", 0o644),
		projFile("rules/../../x", "x", 0o644),
		projFile("..", "x", 0o644),
		projFile(".", "x", 0o644),
		projFile("", "x", 0o644),
		projFile(filepath.Join(r.home, "abs"), "x", 0o644),
		{Path: "rules/b64.md", Mode: 0o644, ContentB64: "!!!"},
	} {
		err := r.writeProjectOmp(projCfg(projFile("rules/new.md", "new", 0o644), bad))
		require.Error(t, err, "%q", bad.Path)
		assert.Equal(t, "keep", readFile(t, filepath.Join(base, "rules/keep.md")), "%q：已有镜像不动", bad.Path)
		assert.NoFileExists(t, filepath.Join(base, "rules/new.md"), "%q：同批的合法文件也不写", bad.Path)
	}
	for _, p := range []string{
		filepath.Join(r.home, "x"), filepath.Join(r.home, ".oryn", "x"), filepath.Join(r.home, ".oryn", "project-config", "x"),
		filepath.Join(r.home, "abs"),
	} {
		assert.NoFileExists(t, p)
	}
}

// 符号链接：<root>、.omp 与其下的符号链接只删链接本身，写入不会经链接落到镜像目录之外；nil 也只删链接。
func TestWriteProjectOmpSymlinks(t *testing.T) {
	r := &Runtime{home: t.TempDir()}
	root := r.projectConfigRoot()
	base := filepath.Join(root, ".omp")
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("secret"), 0o644))
	untouched := func() {
		t.Helper()
		assert.Equal(t, "secret", readFile(t, secret))
		entries, err := os.ReadDir(outside)
		require.NoError(t, err)
		require.Len(t, entries, 1, "镜像目录之外不应出现新文件")
	}
	files := projCfg(projFile("rules/a.md", "A", 0o644), projFile("link.md", "L", 0o644))

	// 1. .omp 下的目录链接与文件链接。
	require.NoError(t, os.MkdirAll(base, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(base, "rules")))
	require.NoError(t, os.Symlink(secret, filepath.Join(base, "link.md")))
	require.NoError(t, os.Symlink(secret, filepath.Join(base, "stray.md")))
	require.NoError(t, r.writeProjectOmp(files))
	untouched()
	assert.Equal(t, "A", readFile(t, filepath.Join(base, "rules/a.md")))
	assert.Equal(t, "L", readFile(t, filepath.Join(base, "link.md")))
	st, err := os.Lstat(filepath.Join(base, "rules"))
	require.NoError(t, err)
	assert.True(t, st.IsDir(), "rules 换成真实目录")
	_, err = os.Lstat(filepath.Join(base, "stray.md"))
	assert.ErrorIs(t, err, os.ErrNotExist)

	// 2. .omp 本身是链接。
	require.NoError(t, os.RemoveAll(base))
	require.NoError(t, os.Symlink(outside, base))
	require.NoError(t, r.writeProjectOmp(files))
	untouched()
	st, err = os.Lstat(base)
	require.NoError(t, err)
	assert.True(t, st.IsDir())

	// 3. <root> 本身是链接；随后 nil 只删链接。
	require.NoError(t, os.RemoveAll(root))
	require.NoError(t, os.Symlink(outside, root))
	require.NoError(t, r.writeProjectOmp(files))
	untouched()
	st, err = os.Lstat(root)
	require.NoError(t, err)
	assert.True(t, st.IsDir())
	require.NoError(t, os.RemoveAll(root))
	require.NoError(t, os.Symlink(outside, root))
	require.NoError(t, r.writeProjectOmp(nil))
	untouched()
	_, err = os.Lstat(root)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// applyProjectConfig：ORYN_CENTRAL_CONFIG 恒为 1（覆盖 env 里的同名值）；ORYN_PROJECT_CONFIG_ROOT 仅在 <root>/.omp
// 是目录时设置（否则连 env 里伪造的值一起删掉）；config.yml 存在、非空且顶层为映射才追加 --config，不合格时告警。
func TestApplyProjectConfig(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, base string)
		root     bool
		config   bool
		warnPart string
	}{
		{name: "no dir"},
		{name: ".omp is a file", setup: func(t *testing.T, base string) {
			require.NoError(t, os.MkdirAll(filepath.Dir(base), 0o755))
			require.NoError(t, os.WriteFile(base, []byte("x"), 0o644))
		}},
		{name: "dir without config.yml", setup: mkTree(map[string]string{"rules/a.md": "A"}), root: true},
		{name: "valid config.yml", setup: mkTree(map[string]string{"config.yml": "theme: dark\n"}), root: true, config: true},
		{name: "empty mapping", setup: mkTree(map[string]string{"config.yml": "{}\n"}), root: true, config: true},
		{name: "empty config.yml", setup: mkTree(map[string]string{"config.yml": " \n"}), root: true},
		{name: "comment-only config.yml", setup: mkTree(map[string]string{"config.yml": "# nothing\n"}), root: true},
		{name: "null config.yml", setup: mkTree(map[string]string{"config.yml": "~\n"}), root: true},
		{name: "list config.yml", setup: mkTree(map[string]string{"config.yml": "- a\n"}), root: true, warnPart: "not a YAML mapping"},
		{name: "scalar config.yml", setup: mkTree(map[string]string{"config.yml": "hello\n"}), root: true, warnPart: "not a YAML mapping"},
		{name: "broken config.yml", setup: mkTree(map[string]string{"config.yml": "a: [\n"}), root: true, warnPart: "invalid YAML"},
		{name: "config.yml is a dir", setup: mkTree(map[string]string{"config.yml/x": "a: 1\n"}), root: true, warnPart: "not a regular file"},
		{name: "config.yml is a symlink", setup: func(t *testing.T, base string) {
			target := filepath.Join(t.TempDir(), "c.yml")
			require.NoError(t, os.WriteFile(target, []byte("a: 1\n"), 0o644))
			require.NoError(t, os.MkdirAll(base, 0o755))
			require.NoError(t, os.Symlink(target, filepath.Join(base, "config.yml")))
		}, root: true, warnPart: "not a regular file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			r := &Runtime{home: t.TempDir(), log: slog.New(slog.NewTextHandler(&logs, nil))}
			root := r.projectConfigRoot()
			base := filepath.Join(root, ".omp")
			if tc.setup != nil {
				tc.setup(t, base)
			}
			env := map[string]string{"ORYN_CENTRAL_CONFIG": "0", "ORYN_PROJECT_CONFIG_ROOT": "/evil", "KEEP": "1"}
			in := []string{"--mode", "rpc-ui", "-e", "ext.ts"}
			args := r.applyProjectConfig(env, in)

			assert.Equal(t, "1", env["ORYN_CENTRAL_CONFIG"])
			assert.Equal(t, "1", env["KEEP"])
			if tc.root {
				assert.Equal(t, root, env["ORYN_PROJECT_CONFIG_ROOT"])
			} else {
				assert.NotContains(t, env, "ORYN_PROJECT_CONFIG_ROOT")
			}
			want := []string{"--mode", "rpc-ui", "-e", "ext.ts"}
			if tc.config {
				want = append(want, "--config", filepath.Join(base, "config.yml"))
			}
			assert.Equal(t, want, args)
			if tc.warnPart != "" {
				assert.Contains(t, logs.String(), "project config.yml ignored")
				assert.Contains(t, logs.String(), tc.warnPart)
			} else {
				assert.Empty(t, logs.String())
			}
		})
	}
}

func mkTree(files map[string]string) func(t *testing.T, base string) {
	return func(t *testing.T, base string) {
		writeTree(t, base, files)
	}
}

// setupDump 读 fakeomp 按线程写出的启动参数与环境。
func setupDump(t *testing.T, dir, thread string) agentDump {
	t.Helper()
	var rec agentDump
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(filepath.Join(dir, thread+".json"))
		return err == nil && json.Unmarshal(b, &rec) == nil
	}, 10*time.Second, 20*time.Millisecond)
	return rec
}

// 端到端（orb 形态）：setup.run 带项目层 → 镜像落盘、agent 带 ORYN_PROJECT_CONFIG_ROOT 与紧随 -e 之后的 --config，
// env 密钥不能覆盖这两个变量；resume.run 用新清单重写并删除多余文件；不带项目层的 setup.run 删除整个目录，
// 之后的 agent 只有 ORYN_CENTRAL_CONFIG=1。
func TestProjectConfigSetupAndAgentStart(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	root := rt.projectConfigRoot()
	base := filepath.Join(root, ".omp")
	dump := filepath.Join(rt.home, "dump")
	userEnv := map[string]string{"FAKEOMP_DUMP_DIR": dump, "ORYN_PROJECT_CONFIG_ROOT": "/evil", "ORYN_CENTRAL_CONFIG": "0"}

	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{ProjectConfig: projCfg(
		projFile("config.yml", "theme: dark\n", 0o644), projFile("rules/a.md", "A", 0o644),
	)}, &res))
	require.True(t, res.Ok)
	assert.Equal(t, "A", readFile(t, filepath.Join(base, "rules/a.md")))

	startAgent(t, cp, thread1, userEnv)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	rec := setupDump(t, dump, thread1)
	assert.Equal(t, "1", rec.Env["ORYN_CENTRAL_CONFIG"])
	assert.Equal(t, root, rec.Env["ORYN_PROJECT_CONFIG_ROOT"])
	i := indexOf(rec.Args, "-e")
	require.GreaterOrEqual(t, i, 0, "argv %v", rec.Args)
	require.Equal(t, []string{"-e", rt.extensionPath(), "--config", filepath.Join(base, "config.yml")}, rec.Args[i:], "--config 紧随 -e、排在末尾")

	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{ProjectConfig: projCfg(
		projFile("rules/b.md", "B", 0o644),
	)}, &res))
	assert.NoFileExists(t, filepath.Join(base, "rules/a.md"))
	assert.NoFileExists(t, filepath.Join(base, "config.yml"))
	assert.Equal(t, "B", readFile(t, filepath.Join(base, "rules/b.md")))

	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{}, &res))
	assert.NoDirExists(t, root)
	startAgent(t, cp, projThread2, userEnv)
	cp.waitNotif(t, 0, "agent.status", statusIs(projThread2, "ready"))
	rec = setupDump(t, dump, projThread2)
	assert.Equal(t, "1", rec.Env["ORYN_CENTRAL_CONFIG"])
	assert.NotContains(t, rec.Env, "ORYN_PROJECT_CONFIG_ROOT")
	assert.NotContains(t, rec.Args, "--config")
}

// 桌面 orb：项目层的 --config 排在桌面覆盖层之前（后者优先）。
func TestProjectConfigBeforeDesktopOverlay(t *testing.T) {
	e := newDeskEnv(t, nil)
	writeTree(t, filepath.Join(e.rt.projectConfigRoot(), ".omp"), map[string]string{"config.yml": "theme: dark\n"})
	pid := startAgent(t, e.cp, thread1, nil)
	e.cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	args := procArgs(t, pid)
	var configs []string
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			configs = append(configs, args[i+1])
		}
	}
	assert.Equal(t, []string{
		filepath.Join(e.rt.projectConfigRoot(), ".omp", "config.yml"),
		e.rt.desktopOverlayPath(),
	}, configs)
}

// runner 不设置、不加载项目层：即使 HOME 下恰好有 ~/.oryn/project-config/.omp/config.yml。
func TestProjectConfigNotOnRunner(t *testing.T) {
	f := newRunnerHome(t)
	writeTree(t, filepath.Join(f.home, ".oryn", "project-config", ".omp"), map[string]string{"config.yml": "a: 1\n"})
	startRunnerRT(t, f, nil)
	dump := filepath.Join(f.home, "dump")
	require.NoError(t, f.cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID: thread1, Cwd: f.work, SessionDir: "~/.oryn/threads/" + thread1,
		Env: map[string]string{"FAKEOMP_DUMP_DIR": dump}, InitFrames: initFrames(thread1),
	}, nil))
	f.cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	rec := setupDump(t, dump, thread1)
	assert.NotContains(t, rec.Env, "ORYN_CENTRAL_CONFIG")
	assert.NotContains(t, rec.Env, "ORYN_PROJECT_CONFIG_ROOT")
	assert.NotContains(t, rec.Args, "--config")
}

// 协调宿主不写、不加载项目层：setup.run 里的 projectConfig 被忽略；agent 没有这两个环境变量与项目层 --config。
func TestProjectConfigNotOnCoordinator(t *testing.T) {
	rt, cp, _, _ := startCoordinator(t)
	cp.waitNotif(t, 0, "hello", nil)
	base := filepath.Join(rt.projectConfigRoot(), ".omp")
	writeTree(t, base, map[string]string{"config.yml": "a: 1\n"})
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{
		OmpHome: []apigen.LinkCtlOmpHomeFile{}, ProjectConfig: projCfg(projFile("rules/x.md", "x", 0o644)),
	}, &res))
	assert.NoFileExists(t, filepath.Join(base, "rules/x.md"))
	assert.FileExists(t, filepath.Join(base, "config.yml"), "协调宿主不镜像")

	coord := apigen.AgentProfileCoordinator
	dump := filepath.Join(rt.home, "dump.json")
	prompt := "#dump " + dump + "\nYou are the Oryn coordinator."
	require.NoError(t, cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID: thread1, SessionDir: "~/.oryn/threads/" + thread1, Profile: &coord, SystemPrompt: &prompt,
		InitFrames: []apigen.OmpFrame{{"type": "open_session", "sessionDir": "~/.oryn/threads/" + thread1}},
	}, nil))
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	var rec agentDump
	require.NoError(t, json.Unmarshal([]byte(readFile(t, dump)), &rec))
	assert.NotContains(t, rec.Env, "ORYN_CENTRAL_CONFIG")
	assert.NotContains(t, rec.Env, "ORYN_PROJECT_CONFIG_ROOT")
	assert.NotContains(t, rec.Args, filepath.Join(base, "config.yml"))
}
