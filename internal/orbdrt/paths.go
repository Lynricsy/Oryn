package orbdrt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolvePath 把控制面给出的路径落到本机：`~` / `~/x` / 相对路径以 $HOME 为根，绝对路径原样。
// 控制面按约定发送 `~/…` 形式，这样 orb 内（HOME=/home/user）与 process 驱动的测试（HOME=临时目录）通用。
func (r *Runtime) resolvePath(p string) string {
	switch {
	case p == "~":
		return r.home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(r.home, p[2:])
	case filepath.IsAbs(p):
		return filepath.Clean(p)
	default:
		return filepath.Join(r.home, p)
	}
}

// safePath 解析路径并要求它（跟随已存在部分的符号链接后）位于 $HOME 之下且不是 $HOME 本身。
func (r *Runtime) safePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("empty path")
	}
	abs := r.resolvePath(p)
	real, err := evalExisting(abs)
	if err != nil {
		return "", err
	}
	home, err := filepath.EvalSymlinks(r.home)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(home, real)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path %q is outside $HOME", p)
	}
	return abs, nil
}

// evalExisting 对路径中已存在的最长前缀做 EvalSymlinks，再拼回尚不存在的后缀。
func evalExisting(abs string) (string, error) {
	cur := abs
	var suffix []string
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			parts := append([]string{real}, suffix...)
			return filepath.Join(parts...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
	}
}

// relUnder 返回 base 下的相对路径 rel 的绝对形式，拒绝绝对路径与 `..` 逃逸。
func relUnder(base, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid relative path %q", rel)
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid relative path %q", rel)
	}
	return filepath.Join(base, clean), nil
}

// writeFileAtomic 先写同目录临时文件再 rename，权限为 mode。
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".orbd-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
