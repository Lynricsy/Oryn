package designengine

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Lynricsy/Oryn/internal/boxcfg"
)

func engineDeps(t *testing.T) map[string]string {
	t.Helper()
	var p struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(EnginePackageJSON(), &p); err != nil {
		t.Fatal(err)
	}
	return p.Dependencies
}

type manifestShape struct {
	Name         string            `json:"name"`
	Private      bool              `json:"private"`
	Type         string            `json:"type"`
	Scripts      map[string]string `json:"scripts"`
	Dependencies map[string]string `json:"dependencies"`
}

func parseManifest(t *testing.T, b []byte) manifestShape {
	t.Helper()
	var m manifestShape
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest 不是 JSON：%v\n%s", err, b)
	}
	return m
}

// 没有设计依赖：manifest 的依赖就是引擎依赖；锁由引擎锁改写根条目，与 manifest 严格对应，其余条目不变。
func TestProjectNoDesignDeps(t *testing.T) {
	for _, pkg := range []string{"", "  \n", `{"name":"x","dependencies":{"react":"19.1.1"}}`} {
		b, err := ProjectManifest([]byte(pkg))
		if err != nil {
			t.Fatal(err)
		}
		m := parseManifest(t, b)
		if m.Name != ProjectName || !m.Private || m.Type != "module" {
			t.Errorf("manifest 头部 %+v", m)
		}
		if m.Scripts["build"] != "vite build" || m.Scripts["dev"] != "vite" || m.Scripts["start"] != "node server.mjs" {
			t.Errorf("scripts = %v", m.Scripts)
		}
		if !maps.Equal(m.Dependencies, engineDeps(t)) {
			t.Errorf("依赖 %v，应等于引擎依赖", m.Dependencies)
		}

		lock, ok, err := ProjectLock([]byte(pkg))
		if err != nil || !ok {
			t.Fatalf("ProjectLock(%q) ok=%v err=%v", pkg, ok, err)
		}
		if err := VerifyProjectLock([]byte(pkg), lock); err != nil {
			t.Fatal(err)
		}
		var got, eng struct {
			Name            string                     `json:"name"`
			LockfileVersion int                        `json:"lockfileVersion"`
			Packages        map[string]json.RawMessage `json:"packages"`
		}
		_ = json.Unmarshal(lock, &got)
		_ = json.Unmarshal(engineLock(), &eng)
		if got.Name != ProjectName || got.LockfileVersion != eng.LockfileVersion {
			t.Errorf("锁头部 name=%q lockfileVersion=%d", got.Name, got.LockfileVersion)
		}
		if !slices.Equal(slices.Sorted(maps.Keys(got.Packages)), slices.Sorted(maps.Keys(eng.Packages))) {
			t.Error("锁的条目集合与引擎锁不同")
		}
		for k, v := range eng.Packages {
			if k != "" && string(got.Packages[k]) != string(v) {
				t.Errorf("锁条目 %q 被改动了", k)
			}
		}
	}
}

// 冲突取设计级版本、额外依赖并入、devDependencies 也并入 dependencies；这时没有离线锁（ok=false）。
func TestProjectMergeConflicts(t *testing.T) {
	pkg := []byte(`{"dependencies":{"clsx":"2.1.1","lucide-react":"1.78.0"},"devDependencies":{"vite":"7.3.8","zod":"^4.0.0"}}`)
	b, err := ProjectManifest(pkg)
	if err != nil {
		t.Fatal(err)
	}
	m := parseManifest(t, b)
	want := engineDeps(t)
	want["clsx"] = "2.1.1"
	want["lucide-react"] = "1.78.0"
	want["zod"] = "^4.0.0"
	if !maps.Equal(m.Dependencies, want) {
		t.Errorf("依赖 %v，应为 %v", m.Dependencies, want)
	}
	if !strings.Contains(string(b), `"zod": "^4.0.0"`) {
		t.Errorf("版本范围被转义了：%s", b)
	}
	c, err := ProjectConflicts(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 1 || c[0] != (Conflict{Name: "lucide-react", Engine: engineDeps(t)["lucide-react"], Design: "1.78.0"}) {
		t.Errorf("冲突 %v", c)
	}
	if _, ok, err := ProjectLock(pkg); ok || err != nil {
		t.Errorf("有额外依赖时 ProjectLock ok=%v err=%v，应为 false、nil", ok, err)
	}
	// 只改版本、没有新包，同样不能离线给锁
	if _, ok, _ := ProjectLock([]byte(`{"dependencies":{"react":"19.1.0"}}`)); ok {
		t.Error("改了引擎依赖版本时不应离线给锁")
	}
	if _, err := ProjectManifest([]byte("{")); err == nil {
		t.Error("坏 JSON 应报错")
	}
}

// VerifyProjectLock 拒绝根依赖、根 name 或 lockfileVersion 对不上的锁。
func TestProjectVerifyLock(t *testing.T) {
	lock, _, err := ProjectLock(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyProjectLock([]byte(`{"dependencies":{"clsx":"2.1.1"}}`), lock); err == nil {
		t.Error("少了 clsx 的锁应被拒绝")
	}
	for _, bad := range []string{
		`{"lockfileVersion":1,"packages":{"":{"name":"oryn-design-project"}}}`,
		`{"lockfileVersion":3,"packages":{}}`,
		`{"lockfileVersion":3,"packages":{"":{"name":"other"}}}`,
		`not json`,
	} {
		if err := VerifyProjectLock(nil, []byte(bad)); err == nil {
			t.Errorf("%s 应被拒绝", bad)
		}
	}
}

// 工程外壳：模板齐全、README 填标题与冲突表、styles.css 按需引入 theme.css、oryn-frames.json 记帧；box.yaml 能被 boxcfg 解析。
func TestProjectFiles(t *testing.T) {
	files := ProjectFiles(Project{
		Title: "登录\n流程", Start: "signup", Theme: true,
		Frames:    []ProjectFrame{{ID: "login", Name: "登录"}, {ID: "signup", Name: "注册"}},
		Conflicts: []Conflict{{Name: "lucide-react", Engine: "1.79.0", Design: "1.78.0"}},
	})
	got := map[string]string{}
	var paths []string
	for _, f := range files {
		got[f.Path] = string(f.Data)
		paths = append(paths, f.Path)
	}
	want := []string{".gitignore", ".oryn/box.yaml", "README.md", "index.html", "main.tsx", "oryn-design.tsx", "oryn-frames.json", "server.mjs", "styles.css", "tsconfig.json", "vite.config.ts"}
	if !slices.Equal(paths, want) {
		t.Fatalf("文件 %v，应为 %v", paths, want)
	}
	if !strings.HasPrefix(got["README.md"], "# 登录 流程\n") || !strings.Contains(got["README.md"], "| `lucide-react` | `1.79.0` | `1.78.0` |") {
		t.Errorf("README:\n%s", got["README.md"])
	}
	if strings.Contains(got["README.md"], "oryn:") && strings.Contains(got["README.md"], "<!--") {
		t.Error("README 里留了占位注释")
	}
	if !strings.Contains(got["styles.css"], `@import "./src/theme.css";`) || strings.Contains(got["styles.css"], "oryn:theme") {
		t.Errorf("styles.css:\n%s", got["styles.css"])
	}
	var meta struct {
		Title, Start string
		Frames       []ProjectFrame
	}
	if err := json.Unmarshal([]byte(got[FramesFile]), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Title != "登录 流程" || meta.Start != "signup" || len(meta.Frames) != 2 || meta.Frames[1] != (ProjectFrame{ID: "signup", Name: "注册"}) {
		t.Errorf("oryn-frames.json = %+v", meta)
	}
	cfg, warnings, err := boxcfg.Parse([]byte(got[".oryn/box.yaml"]))
	if err != nil || len(warnings) > 0 {
		t.Fatalf("box.yaml: err=%v warnings=%v", err, warnings)
	}
	if cfg.Build != "npm ci && npm run build" || cfg.Source || !slices.Equal(cfg.Artifacts, []string{"dist", "server.mjs"}) ||
		len(cfg.Services) != 1 || cfg.Services[0] != (boxcfg.Service{Name: "web", Command: "node server.mjs"}) {
		t.Errorf("box.yaml = %+v", cfg)
	}

	plain := ProjectFiles(Project{})
	for _, f := range plain {
		switch f.Path {
		case "styles.css":
			if strings.Contains(string(f.Data), "@import \"./src/theme.css\"") || strings.Contains(string(f.Data), "oryn:theme") {
				t.Error("没有 theme.css 时不应引入")
			}
		case "README.md":
			if strings.Contains(string(f.Data), "依赖版本冲突") || !strings.HasPrefix(string(f.Data), "# Oryn 设计稿\n") {
				t.Errorf("README:\n%s", f.Data)
			}
		case FramesFile:
			if !strings.Contains(string(f.Data), `"frames": []`) {
				t.Errorf("oryn-frames.json:\n%s", f.Data)
			}
		}
	}
}

// WriteProjectLock：没有额外依赖时离线写出 dist/_project；有额外依赖时调 npm 解析锁（这里用假 npm 记录参数、
// 交出预先算好的锁），并校验 npm 给的锁；npm 给错锁时报错。
func TestProjectWriteLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假 npm 是 sh 脚本")
	}
	ctx := context.Background()
	work := t.TempDir()
	if err := WriteProjectLock(ctx, work); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(work, filepath.FromSlash(ProjectDir))
	manifest, _ := os.ReadFile(filepath.Join(out, "package.json"))
	lock, _ := os.ReadFile(filepath.Join(out, "package-lock.json"))
	wantManifest, _ := ProjectManifest(nil)
	wantLock, _, _ := ProjectLock(nil)
	if string(manifest) != string(wantManifest) || string(lock) != string(wantLock) {
		t.Error("无设计依赖时应写出 ProjectManifest 与 ProjectLock")
	}

	design := []byte(`{"dependencies":{"clsx":"2.1.1"}}`)
	if err := os.MkdirAll(filepath.Join(work, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "src", "package.json"), design, 0o644); err != nil {
		t.Fatal(err)
	}
	deps := engineDeps(t)
	deps["clsx"] = "2.1.1"
	good, _ := json.Marshal(map[string]any{"name": ProjectName, "lockfileVersion": 3, "requires": true,
		"packages": map[string]any{"": map[string]any{"name": ProjectName, "dependencies": deps}}})
	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args")
	lockFile := filepath.Join(bin, "lock.json")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + argsFile + "\ntest -f package.json && test -f package-lock.json || exit 3\ncp " + lockFile + " package-lock.json\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := os.WriteFile(lockFile, good, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjectLock(ctx, work); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	if strings.TrimSpace(string(args)) != "install --package-lock-only --ignore-scripts --no-audit --no-fund" {
		t.Errorf("npm 参数 %q", args)
	}
	manifest, _ = os.ReadFile(filepath.Join(out, "package.json"))
	lock, _ = os.ReadFile(filepath.Join(out, "package-lock.json"))
	wantManifest, _ = ProjectManifest(design)
	if string(manifest) != string(wantManifest) || string(lock) != string(good) {
		t.Error("有额外依赖时应写出合并后的 manifest 与 npm 解析的锁")
	}

	stale, _, _ := ProjectLock(nil) // 少了 clsx
	if err := os.WriteFile(lockFile, stale, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjectLock(ctx, work); err == nil {
		t.Error("npm 给出与 manifest 对不上的锁时应报错")
	}
}
