package designengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// manifestVersion 按 Version 注释里的算法从文件内容算版本。
func manifestVersion(t *testing.T, read func(name string) ([]byte, error)) string {
	t.Helper()
	var b strings.Builder
	for _, name := range slices.Sorted(slices.Values(files)) {
		data, err := read(name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:12]
}

func TestEmbeddedFilesComplete(t *testing.T) {
	var embedded []string
	if err := fs.WalkDir(engine, "engine", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			embedded = append(embedded, strings.TrimPrefix(p, "engine/"))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(embedded)
	if !slices.Equal(embedded, files) {
		t.Fatalf("嵌入文件 %v，files 清单 %v", embedded, files)
	}

	// 引擎目录里的源文件都要进清单（node_modules、dist 是本地安装或构建留下的，不算）
	entries, err := os.ReadDir("engine")
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []string
	for _, e := range entries {
		if e.Name() == "node_modules" || e.Name() == "dist" {
			continue
		}
		onDisk = append(onDisk, e.Name())
	}
	if !slices.Equal(onDisk, files) {
		t.Fatalf("engine/ 下的文件 %v 与 files 清单 %v 不一致", onDisk, files)
	}
	for _, name := range files {
		data, _ := fs.ReadFile(engine, "engine/"+name)
		if len(data) == 0 {
			t.Errorf("%s 为空", name)
		}
	}
}

func TestVersionMatchesContent(t *testing.T) {
	got := manifestVersion(t, func(name string) ([]byte, error) { return fs.ReadFile(engine, "engine/"+name) })
	if Version != got {
		t.Fatalf("Version = %q，按嵌入内容应为 %q（改了引擎文件后更新 designengine.go 的常量）", Version, got)
	}
}

func TestLockMatchesPackage(t *testing.T) {
	var pkg struct {
		Name         string            `json:"name"`
		Dependencies map[string]string `json:"dependencies"`
	}
	var lock struct {
		Name            string `json:"name"`
		LockfileVersion int    `json:"lockfileVersion"`
		Packages        map[string]struct {
			Name         string            `json:"name"`
			Version      string            `json:"version"`
			Dependencies map[string]string `json:"dependencies"`
		} `json:"packages"`
	}
	for name, v := range map[string]any{"package.json": &pkg, "package-lock.json": &lock} {
		data, err := fs.ReadFile(engine, "engine/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if lock.LockfileVersion != 3 {
		t.Errorf("lockfileVersion = %d，应为 3", lock.LockfileVersion)
	}
	top, ok := lock.Packages[""]
	if !ok {
		t.Fatal(`package-lock.json 缺少 packages[""]`)
	}
	if top.Name != pkg.Name {
		t.Errorf("lock 顶层 name = %q，package.json 为 %q", top.Name, pkg.Name)
	}
	if !maps.Equal(top.Dependencies, pkg.Dependencies) {
		t.Errorf("lock 顶层依赖 %v 与 package.json %v 不一致", top.Dependencies, pkg.Dependencies)
	}
	// package.json 一律写精确版本，且 lock 里实际装的就是这个版本
	for name, want := range pkg.Dependencies {
		if strings.ContainsAny(want, "^~<>=*| ") {
			t.Errorf("%s 的版本 %q 不是精确版本", name, want)
		}
		got, ok := lock.Packages["node_modules/"+name]
		if !ok {
			t.Errorf("lock 缺少 node_modules/%s", name)
			continue
		}
		if got.Version != want {
			t.Errorf("lock 里 %s 是 %s，package.json 要求 %s", name, got.Version, want)
		}
	}
	for _, name := range []string{"react", "react-dom", "lucide-react", "vite", "@vitejs/plugin-react", "tailwindcss", "@tailwindcss/vite", "@iconify/tailwind4"} {
		if _, ok := pkg.Dependencies[name]; !ok {
			t.Errorf("package.json 缺少引擎依赖 %s", name)
		}
	}
	for _, name := range []string{"react", "react-dom"} {
		if v := pkg.Dependencies[name]; v != "19.1.1" {
			t.Errorf("%s = %q，应锁 19.1.1", name, v)
		}
	}
}

func TestWriteTo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "engine", Version)
	// 已有的 node_modules 软链或安装目录不受影响
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vite.config.mjs"), []byte("旧内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteTo(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := slices.Sorted(slices.Values(append(slices.Clone(files), "node_modules")))
	if !slices.Equal(names, want) {
		t.Fatalf("写出 %v，应为 %v", names, want)
	}
	got := manifestVersion(t, func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, name)) })
	if got != Version {
		t.Fatalf("写出内容的版本 %q ≠ Version %q", got, Version)
	}
}
