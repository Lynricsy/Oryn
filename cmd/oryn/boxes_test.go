package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/boxcfg"
)

// fakeBoxAPI 记录调用；handlers 按 op 给结果。
type fakeBoxAPI struct {
	orb      bool
	calls    []fakeBoxCall
	handlers map[string]func(p map[string]any) (any, error)
	copies   []boxCopyParams
	execOut  string
}

type fakeBoxCall struct {
	op string
	p  map[string]any
}

func (f *fakeBoxAPI) inOrb() bool { return f.orb }

func (f *fakeBoxAPI) call(_ context.Context, op string, p map[string]any, out any) error {
	f.calls = append(f.calls, fakeBoxCall{op, p})
	h := f.handlers[op]
	if h == nil {
		return &boxErr{Code: "not_found", Message: "no handler for " + op}
	}
	res, err := h(p)
	if err != nil || out == nil {
		return err
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

func (f *fakeBoxAPI) exec(_ context.Context, p boxExecParams, stdout, stderr io.Writer) (int, error) {
	f.calls = append(f.calls, fakeBoxCall{"exec", map[string]any{"box": p.Box, "app": p.App, "command": p.Command}})
	return decodeExecStream(strings.NewReader(f.execOut), stdout, stderr)
}

func (f *fakeBoxAPI) copy(_ context.Context, p boxCopyParams) (int64, error) {
	f.copies = append(f.copies, p)
	return 3, nil
}

func (f *fakeBoxAPI) ops() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.op)
	}
	return out
}

func runBoxes(f *fakeBoxAPI, args ...string) (string, string, error) {
	cmd := newBoxesCmd(func() (boxAPI, error) { return f, nil })
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var out, errb bytes.Buffer
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

// gitRepo 建一个带初始提交的临时仓库；files 的值为空串时只建目录。
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFiles(t, dir, files)
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o755))
	}
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// tarEntries 列出 gzip tar 的条目名（去掉 ./ 前缀）与文件内容。
func tarEntries(t *testing.T, p string) map[string]string {
	t.Helper()
	f, err := os.Open(p)
	require.NoError(t, err)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		name := strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")
		if name == "" || name == "." {
			continue
		}
		body, _ := io.ReadAll(tr)
		out[name] = string(body)
	}
	return out
}

func hasPrefixKey(m map[string]string, prefix string) bool {
	for k := range m {
		if k == prefix || strings.HasPrefix(k, prefix+"/") {
			return true
		}
	}
	return false
}

// deployFake 返回一个在 commit 时读出包内容的假传输。
func deployFake(t *testing.T, entries *map[string]string, commit *boxCommitParams) *fakeBoxAPI {
	return &fakeBoxAPI{orb: true, handlers: map[string]func(map[string]any) (any, error){
		"deploy.prepare": func(p map[string]any) (any, error) {
			return boxPrepareResult{DeployID: "bxd_1", ReleaseID: "bxr_1", Box: str(p, "box"), App: str(p, "app"),
				URL: "https://" + str(p, "app") + ".apps.test", ArchivePath: "~/.cache/oryn/box-releases/bxd_1.tar.gz", MaxBytes: 512 << 20,
				Env: map[string]string{"ORYN_DEPLOYMENT_NAME": str(p, "app")}}, nil
		},
		"deploy.commit": func(p map[string]any) (any, error) {
			b, _ := json.Marshal(p)
			require.NoError(t, json.Unmarshal(b, commit))
			archive, err := expandHome("~/.cache/oryn/box-releases/bxd_1.tar.gz")
			require.NoError(t, err)
			*entries = tarEntries(t, archive)
			return boxCommitResult{OK: true, URL: "https://x.apps.test"}, nil
		},
		"deploy.cancel": func(map[string]any) (any, error) { return map[string]any{}, nil },
	}}
}

func TestResolveTarget(t *testing.T) {
	repo := gitRepo(t, map[string]string{"crm/.oryn/box.yaml": "services: {web: {command: x}}\n", "status/.oryn/box.yaml": "services: {web: {command: x}}\n"})
	crm := filepath.Join(repo, "crm")

	_, err := resolveTarget(repo, crm, deployOpts{})
	assert.ErrorContains(t, err, "the first deploy needs --on <box> --name <app>")
	_, err = resolveTarget(repo, repo, deployOpts{On: "b1", Name: "x"})
	assert.ErrorContains(t, err, "no .oryn/box.yaml found")

	got, err := resolveTarget(repo, crm, deployOpts{On: "b1", Name: "tiny-crm"})
	require.NoError(t, err)
	assert.Equal(t, deployTarget{key: "crm", on: "b1", name: "tiny-crm", root: "crm"}, got)

	require.NoError(t, boxcfg.SaveTargets(repo, boxcfg.Targets{Targets: map[string]boxcfg.Target{
		"crm":    {On: "b1", Name: "tiny-crm", Root: "crm", Private: true},
		"status": {On: "b1", Name: "status", Root: "status"},
	}}))
	// 当前目录定 root，再找 root 相同的 target；target 的 private 生效
	got, err = resolveTarget(repo, crm, deployOpts{})
	require.NoError(t, err)
	assert.Equal(t, "tiny-crm", got.name)
	require.NotNil(t, got.public)
	assert.False(t, *got.public)
	// --public 覆盖 target 的 private
	got, err = resolveTarget(repo, crm, deployOpts{Public: true})
	require.NoError(t, err)
	assert.True(t, *got.public)
	// 没有 private 的 target、没给参数 → nil（已有 app 不变）
	got, err = resolveTarget(repo, crm, deployOpts{Target: "status"})
	require.NoError(t, err)
	assert.Equal(t, "status", got.root)
	assert.Nil(t, got.public)
	got, err = resolveTarget(repo, repo, deployOpts{Target: "status", Private: true, On: "b2"})
	require.NoError(t, err)
	assert.Equal(t, "b2", got.on)
	assert.False(t, *got.public)
	_, err = resolveTarget(repo, repo, deployOpts{Target: "nope"})
	assert.ErrorContains(t, err, "have: crm, status")
}

func TestDeployRequiresOrb(t *testing.T) {
	f := &fakeBoxAPI{}
	_, _, err := runBoxes(f, "deploy", "--on", "b1", "--name", "x")
	assert.ErrorContains(t, err, "runs inside a thread's environment")
	assert.Empty(t, f.calls)
}

func TestDeploySourceFalse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := gitRepo(t, map[string]string{
		".oryn/box.yaml": "build: mkdir -p dist && echo \"$ORYN_DEPLOYMENT_NAME\" > dist/name\nsource: false\nartifacts: [dist]\nservices:\n  web: {command: ./app, cwd: dist}\n",
		"src/main.go":    "package main\n",
		".gitignore":     "dist/\n",
	})
	var entries map[string]string
	var commit boxCommitParams
	f := deployFake(t, &entries, &commit)
	var out, errb bytes.Buffer
	require.NoError(t, runDeploy(context.Background(), f, deployOpts{On: "b1", Name: "hello"}, repo, &out, &errb), errb.String())
	assert.Equal(t, map[string]string{"dist": "", "dist/name": "hello\n"}, entries)
	assert.Equal(t, []apigen.OrbBoxService{{Name: "web", Command: "./app", Cwd: "dist"}}, commit.Services)
	assert.NotEmpty(t, commit.SHA256)
	assert.Contains(t, out.String(), "deployed hello to b1")
	assert.Equal(t, []string{"deploy.prepare", "deploy.commit"}, f.ops())
	prep := f.calls[0].p
	assert.Equal(t, false, prep["source"])
	assert.Equal(t, "main", prep["branch"])
	assert.True(t, strings.HasPrefix(str(prep, "repo"), "local:"))
	tg, err := boxcfg.LoadTargets(repo)
	require.NoError(t, err)
	assert.Equal(t, boxcfg.Target{On: "b1", Name: "hello"}, tg.Targets["default"])
	// 包已删除
	_, err = os.Stat(filepath.Join(os.Getenv("HOME"), ".cache/oryn/box-releases/bxd_1.tar.gz"))
	assert.True(t, os.IsNotExist(err))
}

func TestDeploySourceSubdirWithInclude(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := gitRepo(t, map[string]string{
		"package.json":          "{}\n",
		".gitignore":            "build/\n",
		"crm/.oryn/box.yaml":    "include: [\"../shared\"]\nservices:\n  web: {command: node build, cwd: srv}\n",
		"crm/srv/index.js":      "x\n",
		"shared/ui.js":          "ui\n",
		"status/.oryn/box.yaml": "services: {web: {command: x}}\n",
		"status/app.js":         "s\n",
	})
	gitT(t, repo, "remote", "add", "origin", "git@github.com:Acme/Mono.git")
	writeFiles(t, repo, map[string]string{"crm/build/out.js": "built\n"}) // 被忽略的构建产物：工作区仍然干净
	var entries map[string]string
	var commit boxCommitParams
	f := deployFake(t, &entries, &commit)
	var out, errb bytes.Buffer
	require.NoError(t, runDeploy(context.Background(), f, deployOpts{On: "b1", Name: "tiny-crm"}, filepath.Join(repo, "crm", "srv"), &out, &errb), errb.String())
	assert.Equal(t, "x\n", entries["crm/srv/index.js"])
	assert.Equal(t, "built\n", entries["crm/build/out.js"])
	assert.Equal(t, "ui\n", entries["shared/ui.js"])
	assert.Equal(t, "{}\n", entries["package.json"])
	assert.True(t, hasPrefixKey(entries, ".git"))
	assert.False(t, hasPrefixKey(entries, "status"), "别的 app 的目录不进 release")
	assert.Equal(t, []apigen.OrbBoxService{{Name: "web", Command: "node build", Cwd: "crm/srv"}}, commit.Services)
	prep := f.calls[0].p
	assert.Equal(t, "github.com/acme/mono", prep["repo"])
	assert.Equal(t, "crm", prep["root"])
	tg, err := boxcfg.LoadTargets(repo)
	require.NoError(t, err)
	assert.Equal(t, boxcfg.Target{On: "b1", Name: "tiny-crm", Root: "crm"}, tg.Targets["crm"])
}

func TestDeployDirtyUsesWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := gitRepo(t, map[string]string{
		".oryn/box.yaml": "source: false\nartifacts: [dist]\nservices:\n  web: {command: ./app, cwd: dist}\n",
		"dist/version":   "v1\n",
	})
	writeFiles(t, repo, map[string]string{"dist/version": "v2-uncommitted\n", "dist/extra": "untracked\n"})
	var entries map[string]string
	var commit boxCommitParams
	f := deployFake(t, &entries, &commit)
	var out, errb bytes.Buffer
	require.NoError(t, runDeploy(context.Background(), f, deployOpts{On: "b1", Name: "hello"}, repo, &out, &errb), errb.String())
	assert.Contains(t, errb.String(), "uncommitted changes are not deployed")
	assert.Equal(t, "v1\n", entries["dist/version"])
	_, ok := entries["dist/extra"]
	assert.False(t, ok)
	assert.NotContains(t, gitT(t, repo, "worktree", "list"), "box-build", "临时 worktree 已移除")
}

func TestDeployFailureCancels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := gitRepo(t, map[string]string{
		".oryn/box.yaml": "build: exit 3\nsource: false\nartifacts: [dist]\nservices:\n  web: {command: ./app}\n",
	})
	var entries map[string]string
	var commit boxCommitParams
	f := deployFake(t, &entries, &commit)
	err := runDeploy(context.Background(), f, deployOpts{On: "b1", Name: "hello"}, repo, io.Discard, io.Discard)
	assert.ErrorContains(t, err, "build failed")
	assert.Equal(t, []string{"deploy.prepare", "deploy.cancel"}, f.ops())

	// activate_unknown：不取消，提示稍后查看
	f = deployFake(t, &entries, &commit)
	f.handlers["deploy.commit"] = func(map[string]any) (any, error) {
		return boxCommitResult{Code: "activate_unknown", Message: "the box did not answer"}, nil
	}
	repo = gitRepo(t, map[string]string{".oryn/box.yaml": "source: false\nartifacts: [x]\nservices:\n  web: {command: ./app}\n", "x": "1"})
	var errb bytes.Buffer
	err = runDeploy(context.Background(), f, deployOpts{On: "b1", Name: "hello", Move: true}, repo, io.Discard, &errb)
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 1, ec.code)
	assert.Contains(t, errb.String(), "check oryn boxes deployments b1 in a minute")
	assert.Equal(t, []string{"deploy.prepare", "deploy.commit"}, f.ops())

	// --move 遇到 box_changes：提示加 --discard-box-changes
	f = deployFake(t, &entries, &commit)
	f.handlers["deploy.commit"] = func(map[string]any) (any, error) {
		return boxCommitResult{Code: "box_changes", Message: "commits made on the box are not in the deployed branch", Log: "tail"}, nil
	}
	errb.Reset()
	err = runDeploy(context.Background(), f, deployOpts{On: "b1", Name: "hello", Move: true}, repo, io.Discard, &errb)
	require.Error(t, err)
	assert.Contains(t, errb.String(), "add --discard-box-changes")
	assert.Contains(t, errb.String(), "tail")
	assert.Equal(t, "deploy.cancel", f.ops()[len(f.ops())-1])
}

func TestBoxesRenameRewritesTargets(t *testing.T) {
	repo := gitRepo(t, map[string]string{"README": "x"})
	require.NoError(t, boxcfg.SaveTargets(repo, boxcfg.Targets{Targets: map[string]boxcfg.Target{
		"a": {On: "old", Name: "a"}, "b": {On: "box_X", Name: "b"}, "c": {On: "other", Name: "c"},
	}}))
	t.Chdir(repo)
	f := &fakeBoxAPI{handlers: map[string]func(map[string]any) (any, error){
		"show":   func(map[string]any) (any, error) { return apigen.Box{ID: "box_X", Name: "old"}, nil },
		"rename": func(p map[string]any) (any, error) { return apigen.Box{ID: "box_X", Name: str(p, "name")}, nil },
	}}
	out, _, err := runBoxes(f, "rename", "old", "new")
	require.NoError(t, err)
	assert.Contains(t, out, "updated target a")
	tg, err := boxcfg.LoadTargets(repo)
	require.NoError(t, err)
	assert.Equal(t, "new", tg.Targets["a"].On)
	assert.Equal(t, "new", tg.Targets["b"].On)
	assert.Equal(t, "other", tg.Targets["c"].On)
}

func TestBoxesCpPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	f := &fakeBoxAPI{orb: true}
	_, _, err := runBoxes(f, "cp", "./note.txt", "b1:note.txt", "--app", "hello")
	require.NoError(t, err)
	require.Len(t, f.copies, 1)
	assert.Equal(t, boxCopyParams{Box: "b1", App: "hello", BoxPath: "note.txt", LocalPath: filepath.Join(home, "note.txt"), ToBox: true}, f.copies[0])

	_, _, err = runBoxes(f, "cp", "b1:logs/a.log", "out.log")
	require.NoError(t, err)
	assert.False(t, f.copies[1].ToBox)
	assert.Equal(t, "logs/a.log", f.copies[1].BoxPath)

	for _, bad := range [][]string{{"a", "b"}, {"b1:x", "b2:y"}, {"x", "b1:../etc/passwd"}, {"x", "b1:/etc/passwd"}} {
		_, _, err := runBoxes(f, append([]string{"cp"}, bad...)...)
		assert.Error(t, err, bad)
	}
	// orb 内本地一侧须在 $HOME 之下
	_, _, err = runBoxes(f, "cp", "/etc/hosts", "b1:hosts")
	assert.ErrorContains(t, err, "must be under")
	assert.Len(t, f.copies, 2)
}

func TestBoxesExecStream(t *testing.T) {
	f := &fakeBoxAPI{execOut: `{"s":"o","d":"hello "}` + "\n" + `{"s":"e","d":"warn\n"}` + "\n" + `{"s":"o","d":"world\n"}` + "\n" + `{"exit":3}` + "\n"}
	out, errOut, err := runBoxes(f, "exec", "b1", "--app", "hello", "--", "cat", "x")
	assert.Equal(t, "hello world\n", out)
	assert.Equal(t, "warn\n", errOut)
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 3, ec.code)
	assert.Equal(t, "cat x", f.calls[0].p["command"])

	f.execOut = `{"exit":0}` + "\n"
	_, _, err = runBoxes(f, "exec", "b1", "--", "true")
	assert.NoError(t, err)

	_, err = decodeExecStream(strings.NewReader(`{"s":"o","d":"x"}`+"\n"), io.Discard, io.Discard)
	assert.ErrorContains(t, err, "without an exit code")

	f.orb = true
	_, _, err = runBoxes(f, "exec", "b1", "--timeout", "2000", "--", "true")
	assert.ErrorContains(t, err, "at most 1800")
}

func TestBoxesInOrbNeedsThread(t *testing.T) {
	t.Setenv("ORYN_ORB", "1")
	t.Setenv("ORYN_API_TOKEN", "")
	t.Setenv("ORYN_THREAD_ID", "")
	_, err := newBoxAPI()
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)
}

func TestBoxesDeleteNotInOrb(t *testing.T) {
	f := &fakeBoxAPI{orb: true}
	_, _, err := runBoxes(f, "delete", "b1", "--yes")
	assert.ErrorContains(t, err, "deleting a box needs the owner")
	assert.Empty(t, f.calls)

	f = &fakeBoxAPI{handlers: map[string]func(map[string]any) (any, error){
		"show":   func(map[string]any) (any, error) { return apigen.Box{ID: "box_1", Name: "b1"}, nil },
		"delete": func(map[string]any) (any, error) { return map[string]any{}, nil },
	}}
	cmd := newBoxesCmd(func() (boxAPI, error) { return f, nil })
	cmd.SetIn(strings.NewReader("wrong\n"))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"delete", "b1"})
	assert.Error(t, cmd.ExecuteContext(context.Background()))
	assert.Equal(t, []string{"show"}, f.ops())
	_, _, err = runBoxes(f, "delete", "b1", "--yes")
	require.NoError(t, err)
	assert.Equal(t, "box_1", f.calls[len(f.calls)-1].p["box"])
}
