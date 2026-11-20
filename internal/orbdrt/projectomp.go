package orbdrt

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 项目级 omp 配置（M12.2，契约 C1）：控制面经 setup.run / resume.run 的 projectConfig 下发，orbd 把它严格镜像到
// `$HOME/.oryn/project-config/.omp/`（仓库之外，不进 git status；`$HOME/.oryn/**` 对 Web 文件接口只读），
// Oryn Core 经环境变量 ORYN_PROJECT_CONFIG_ROOT 把它当作优先于仓库 `.omp/` 的项目层加载。
const (
	envCentralConfig     = "ORYN_CENTRAL_CONFIG"
	envProjectConfigRoot = "ORYN_PROJECT_CONFIG_ROOT"
	projectOmpDir        = ".omp"
	projectConfigYML     = "config.yml"
)

// projectConfigRoot 是项目层根目录 `$HOME/.oryn/project-config`（C1 的 `<root>`）。
func (r *Runtime) projectConfigRoot() string {
	return filepath.Join(r.home, ".oryn", "project-config")
}

// writeProjectOmp 把 `<root>/.omp/` 严格镜像成 cfg.Files：不在清单里的文件与符号链接一律删除，空目录自底向上清掉。
// cfg == nil（orb 不属于项目）时删除整个 `<root>`。先校验全部路径与内容再动磁盘，坏清单不会只写一半。
//
// 符号链接：先清理再写入。`<root>`、`<root>/.omp` 是链接时换成真实目录；其下不在清单里的链接（含指向别处的目录
// 链接）只删链接本身、不跟随；清单路径上的链接由 writeFileAtomic 的 rename 整个替换。写入因此不会经链接落到镜像
// 目录之外。orb 内同 uid 的进程仍可在清理与写入之间重新放链接，但它本来就拥有整个 $HOME，没有越权。
func (r *Runtime) writeProjectOmp(cfg *apigen.LinkCtlProjectConfig) error {
	root := r.projectConfigRoot()
	if cfg == nil {
		return os.RemoveAll(root)
	}
	base := filepath.Join(root, projectOmpDir)
	items, err := projectItems(base, cfg.Files)
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(items))
	for _, it := range items {
		want[it.path] = true
	}
	for _, d := range []string{root, base} {
		if err := realDir(d); err != nil {
			return err
		}
	}
	if err := pruneMirror(base, want); err != nil {
		return err
	}
	for _, it := range items {
		if err := writeFileAtomic(it.path, it.data, it.mode); err != nil {
			return fmt.Errorf("%s: %w", it.rel, err)
		}
	}
	return removeEmptyDirs(base)
}

// projectItem 是项目层清单里校验、解码过的一个文件（path 为 base 之下的绝对路径）。
type projectItem struct {
	rel  string
	path string
	data []byte
	mode os.FileMode
}

// projectItems 校验并解码项目层清单：路径须是 base 之下的相对路径（不得为绝对路径或含越出的 `..`），内容为 base64；
// 缺省权限 0644。orb 的镜像与 runner 的按线程目录共用这一套校验。
func projectItems(base string, files []apigen.LinkCtlOmpHomeFile) ([]projectItem, error) {
	items := make([]projectItem, 0, len(files))
	for _, f := range files {
		path, err := relUnder(base, f.Path)
		if err != nil {
			return nil, err
		}
		data, err := base64.StdEncoding.DecodeString(f.ContentB64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		mode := os.FileMode(f.Mode) & os.ModePerm
		if mode == 0 {
			mode = 0o644
		}
		items = append(items, projectItem{rel: f.Path, path: path, data: data, mode: mode})
	}
	return items, nil
}

// realDir 确保 d 是真实目录：符号链接或非目录先删掉（RemoveAll 对符号链接只删链接本身）再建。
func realDir(d string) error {
	st, err := os.Lstat(d)
	switch {
	case err == nil && st.IsDir():
		return nil
	case err == nil:
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return os.MkdirAll(d, 0o755)
}

// pruneMirror 删除 base 下不在 want（绝对路径）里的非目录项（普通文件、符号链接等）；清单里的文件在磁盘上是目录时
// 整个删掉。WalkDir 用 lstat 判断类型，不跟随符号链接。
func pruneMirror(base string, want map[string]bool) error {
	return filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == base {
			return nil
		}
		switch {
		case d.IsDir():
			if want[path] {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				return filepath.SkipDir
			}
			return nil
		case !want[path]:
			return os.Remove(path)
		}
		return nil
	})
}

// removeEmptyDirs 自底向上删除 base 之下的空目录（base 本身保留）。
func removeEmptyDirs(base string) error {
	var dirs []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != base {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// WalkDir 按字典序先序遍历：倒序即先子后父。
	for _, d := range slices.Backward(dirs) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if err := os.Remove(d); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyProjectConfig 给 orb 形态的 agent 加 C1 的环境变量与 `--config`，返回新的 args。调用方须在合并完
// agent.start 的 env 之后调用（用户的 env 密钥不能覆盖这两个变量），并在桌面覆盖层的 `--config` 之前（后者优先）。
// 判断只看磁盘，不依赖内存状态，orbd 重启后依然正确。
func (r *Runtime) applyProjectConfig(env map[string]string, args []string) []string {
	return r.applyProjectRoot(env, args, r.projectConfigRoot())
}

// applyProjectRoot 以 root（其下 `.omp/` 为项目层）设置 C1 的环境变量与 `--config`：orb 的 root 是
// `$HOME/.oryn/project-config`，runner 是按线程下发的内容寻址目录（M21，见 runnerproject.go）。
func (r *Runtime) applyProjectRoot(env map[string]string, args []string, root string) []string {
	env[envCentralConfig] = "1"
	base := filepath.Join(root, projectOmpDir)
	if st, err := os.Lstat(base); err != nil || !st.IsDir() {
		delete(env, envProjectConfigRoot)
		return args
	}
	env[envProjectConfigRoot] = root
	cfg := filepath.Join(base, projectConfigYML)
	ok, err := projectConfigUsable(cfg)
	if err != nil {
		// omp 对 --config 覆盖层解析失败是硬错误：不合格就不传，agent 照常启动。
		r.log.Warn("project config.yml ignored", "path", cfg, "err", err)
	}
	if ok {
		args = append(args, "--config", cfg)
	}
	return args
}

// projectConfigUsable 报告 path 是否是可作为 `--config` 覆盖层的 config.yml：存在、是普通文件、非空、只有一个 YAML
// 文档且顶层为映射。不存在或为空（含只有注释 / null）返回 false, nil；其余不合格返回 false 与原因。
// Core 用 Bun.YAML.parse 读覆盖层，多文档（`---` 分隔，哪怕第二个为空）会被解析成数组而硬失败，所以要求第二次 Decode 即 EOF。
func projectConfigUsable(path string) (bool, error) {
	st, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case !st.Mode().IsRegular():
		return false, errors.New("not a regular file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc yaml.Node
	switch err := dec.Decode(&doc); {
	case errors.Is(err, io.EOF):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("invalid YAML: %w", err)
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return false, errors.New("multiple YAML documents")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return false, nil
	}
	top := doc.Content[0]
	if top.Kind == yaml.AliasNode && top.Alias != nil {
		top = top.Alias
	}
	switch {
	case top.Kind == yaml.MappingNode:
		return true, nil
	case top.Kind == yaml.ScalarNode && top.Tag == "!!null":
		return false, nil
	}
	return false, errors.New("top level is not a YAML mapping")
}
