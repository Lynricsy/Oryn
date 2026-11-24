package designengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// 导出工程（M29 D2）的 package.json 与 package-lock.json。工程依赖 = 引擎依赖 ∪ 设计级依赖（src/package.json 的
// dependencies 与 devDependencies），同名时取设计级的版本；全部写进 dependencies，与引擎一致。
// 锁文件必须与合并后的 package.json 严格对应（box 构建与验收都用 npm ci）：没有额外依赖时由嵌入的引擎锁改写根条目得到；
// 有额外依赖时由 orbd 封存时调 WriteProjectLock 生成，存进版本的 dist/_project/。

// ProjectName 是导出工程 package.json 的 name。
const ProjectName = "oryn-design-project"

// ProjectDir 是版本里存放工程 package.json / package-lock.json 的目录（WriteProjectLock 写到工作副本的同一位置）。
const ProjectDir = "dist/_project"

// projectScripts 是导出工程的 npm scripts。
var projectScripts = map[string]string{
	"dev":     "vite",
	"build":   "vite build",
	"preview": "vite preview",
	"start":   "node server.mjs",
}

// Conflict 是设计级依赖覆盖了引擎依赖的一项：Engine 是引擎锁定的版本，Design 是 src/package.json 里写的版本（被采用）。
type Conflict struct{ Name, Engine, Design string }

// packageJSON 是引擎 package.json 与设计 package.json 里用到的字段。
type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	AllowScripts    map[string]bool   `json:"allowScripts"`
}

// projectManifest 是写出的工程 package.json；字段顺序即输出顺序。
type projectManifest struct {
	Name         string            `json:"name"`
	Private      bool              `json:"private"`
	Type         string            `json:"type"`
	Scripts      map[string]string `json:"scripts"`
	Dependencies map[string]string `json:"dependencies"`
	AllowScripts map[string]bool   `json:"allowScripts,omitempty"`
}

// merge 算合并后的工程 manifest 与冲突（按包名排序）。designPkg 为空 = 没有设计依赖。
func merge(designPkg []byte) (projectManifest, []Conflict, error) {
	var eng packageJSON
	if err := json.Unmarshal(EnginePackageJSON(), &eng); err != nil {
		return projectManifest{}, nil, fmt.Errorf("designengine: 引擎 package.json: %w", err)
	}
	m := projectManifest{
		Name: ProjectName, Private: true, Type: "module",
		Scripts:      maps.Clone(projectScripts),
		Dependencies: maps.Clone(eng.Dependencies),
		AllowScripts: maps.Clone(eng.AllowScripts),
	}
	if len(bytes.TrimSpace(designPkg)) == 0 {
		return m, nil, nil
	}
	var d packageJSON
	if err := json.Unmarshal(designPkg, &d); err != nil {
		return projectManifest{}, nil, fmt.Errorf("src/package.json 不是合法的 package.json：%w", err)
	}
	var conflicts []Conflict
	// devDependencies 先写，dependencies 后写：两处都有时以 dependencies 为准
	for _, deps := range []map[string]string{d.DevDependencies, d.Dependencies} {
		for name, v := range deps {
			if e, ok := eng.Dependencies[name]; ok && e != v {
				conflicts = slices.DeleteFunc(conflicts, func(c Conflict) bool { return c.Name == name })
				conflicts = append(conflicts, Conflict{Name: name, Engine: e, Design: v})
			}
			m.Dependencies[name] = v
		}
	}
	if len(d.AllowScripts) > 0 {
		if m.AllowScripts == nil {
			m.AllowScripts = map[string]bool{}
		}
		maps.Copy(m.AllowScripts, d.AllowScripts)
	}
	slices.SortFunc(conflicts, func(a, b Conflict) int { return strings.Compare(a.Name, b.Name) })
	return m, conflicts, nil
}

func (m projectManifest) json() []byte { return indentJSON(m) }

// indentJSON 两空格缩进、末尾换行、不转义 <>&（版本范围里常有 >=）。
func indentJSON(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic(err) // 只编码本包构造的值
	}
	return b.Bytes()
}

// ProjectManifest 返回导出工程的 package.json：引擎依赖 ∪ 设计依赖（designPkg 是 src/package.json 原文，空 = 没有），
// 同名冲突取设计级的版本；name 固定为 ProjectName，带 dev / build / preview / start 四个 scripts。
func ProjectManifest(designPkg []byte) ([]byte, error) {
	m, _, err := merge(designPkg)
	if err != nil {
		return nil, err
	}
	return m.json(), nil
}

// ProjectConflicts 返回设计级依赖覆盖引擎依赖的各项（按包名排序）；没有冲突时为空。
func ProjectConflicts(designPkg []byte) ([]Conflict, error) {
	_, c, err := merge(designPkg)
	return c, err
}

// ProjectLock 在不访问网络的前提下给出工程锁：合并后的依赖与引擎依赖完全相同（没有设计依赖，或设计依赖都已在引擎里
// 且版本字符串一致）时，把嵌入的引擎锁的根条目（name、packages[""]）改写成与 ProjectManifest 一致，返回 ok=true；
// 有额外或改版本的依赖时返回 ok=false，锁只能由 WriteProjectLock 现场解析。
func ProjectLock(designPkg []byte) ([]byte, bool, error) {
	m, _, err := merge(designPkg)
	if err != nil {
		return nil, false, err
	}
	var eng packageJSON
	if err := json.Unmarshal(EnginePackageJSON(), &eng); err != nil {
		return nil, false, fmt.Errorf("designengine: 引擎 package.json: %w", err)
	}
	if !maps.Equal(m.Dependencies, eng.Dependencies) {
		return nil, false, nil
	}
	var lock map[string]json.RawMessage
	if err := json.Unmarshal(engineLock(), &lock); err != nil {
		return nil, false, fmt.Errorf("designengine: 引擎 package-lock.json: %w", err)
	}
	var pkgs map[string]json.RawMessage
	if err := json.Unmarshal(lock["packages"], &pkgs); err != nil {
		return nil, false, fmt.Errorf("designengine: 引擎 package-lock.json 的 packages: %w", err)
	}
	pkgs[""] = rawJSON(lockRoot{Name: ProjectName, Dependencies: m.Dependencies})
	lock["name"] = rawJSON(ProjectName)
	lock["packages"] = rawJSON(pkgs)
	return indentJSON(lock), true, nil
}

// lockRoot 是 package-lock.json 的 packages[""]。
type lockRoot struct {
	Name            string            `json:"name"`
	Dependencies    map[string]string `json:"dependencies,omitempty"`
	DevDependencies map[string]string `json:"devDependencies,omitempty"`
}

// rawJSON 是紧凑、不转义 <>& 的 JSON。
func rawJSON(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err) // 只编码本包构造的值
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// VerifyProjectLock 确认 lock 与 ProjectManifest(designPkg) 严格对应：lockfileVersion ≥ 2，根条目的 name 与依赖
// 与 manifest 相同、没有 devDependencies。不一致时返回说明原因的错误。
func VerifyProjectLock(designPkg, lock []byte) error {
	m, _, err := merge(designPkg)
	if err != nil {
		return err
	}
	var l struct {
		LockfileVersion int                 `json:"lockfileVersion"`
		Packages        map[string]lockRoot `json:"packages"`
	}
	if err := json.Unmarshal(lock, &l); err != nil {
		return fmt.Errorf("工程锁不是合法的 JSON：%w", err)
	}
	if l.LockfileVersion < 2 {
		return fmt.Errorf("工程锁的 lockfileVersion 是 %d，npm ci 需要 2 或 3", l.LockfileVersion)
	}
	root, ok := l.Packages[""]
	switch {
	case !ok:
		return errors.New(`工程锁缺少根条目 packages[""]`)
	case root.Name != ProjectName:
		return fmt.Errorf("工程锁的根 name 是 %q，应为 %q", root.Name, ProjectName)
	case len(root.DevDependencies) > 0 || !maps.Equal(root.Dependencies, m.Dependencies):
		return errors.New("工程锁的根依赖与 src/package.json 合并引擎依赖的结果不一致")
	}
	return nil
}

// WriteProjectLock 给工作副本 workDir 生成工程锁：读 <workDir>/src/package.json（可缺），把 ProjectManifest 与
// 工程锁写到 <workDir>/dist/_project/package.json、package-lock.json。没有额外依赖时锁取 ProjectLock（不访问网络）；
// 否则在临时目录里放好 manifest 与引擎锁，跑 `npm install --package-lock-only --ignore-scripts --no-audit --no-fund`
// 解析出新锁（需要 PATH 里有 npm 并能访问 registry）。
//
// 前提：调用方在 vite build 之后调用——build 会清空 dist/，先写的 _project 会被删掉。
func WriteProjectLock(ctx context.Context, workDir string) error {
	designPkg, err := os.ReadFile(filepath.Join(workDir, "src", "package.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("designengine: %w", err)
	}
	manifest, err := ProjectManifest(designPkg)
	if err != nil {
		return err
	}
	lock, ok, err := ProjectLock(designPkg)
	if err != nil {
		return err
	}
	if !ok {
		if lock, err = resolveLock(ctx, manifest); err != nil {
			return err
		}
		if err := VerifyProjectLock(designPkg, lock); err != nil {
			return fmt.Errorf("designengine: npm 生成的锁不符合预期：%w", err)
		}
	}
	out := filepath.Join(workDir, filepath.FromSlash(ProjectDir))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fmt.Errorf("designengine: %w", err)
	}
	if err := os.WriteFile(filepath.Join(out, "package.json"), manifest, 0o644); err != nil {
		return fmt.Errorf("designengine: %w", err)
	}
	if err := os.WriteFile(filepath.Join(out, "package-lock.json"), lock, 0o644); err != nil {
		return fmt.Errorf("designengine: %w", err)
	}
	return nil
}

// npmTail 是 npm 失败时错误里带的输出尾部字节数。
const npmTail = 4 << 10

// resolveLock 以引擎锁为起点，让 npm 只解析锁（不装依赖、不跑脚本），返回新锁。
func resolveLock(ctx context.Context, manifest []byte) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "oryn-project-lock-*")
	if err != nil {
		return nil, fmt.Errorf("designengine: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "package.json"), manifest, 0o644); err != nil {
		return nil, fmt.Errorf("designengine: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "package-lock.json"), engineLock(), 0o644); err != nil {
		return nil, fmt.Errorf("designengine: %w", err)
	}
	cmd := exec.CommandContext(ctx, "npm", "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund")
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		if len(out) > npmTail {
			out = out[len(out)-npmTail:]
		}
		return nil, fmt.Errorf("designengine: npm 解析工程锁失败：%w\n%s", err, out)
	}
	lock, err := os.ReadFile(filepath.Join(tmp, "package-lock.json"))
	if err != nil {
		return nil, fmt.Errorf("designengine: %w", err)
	}
	return lock, nil
}
