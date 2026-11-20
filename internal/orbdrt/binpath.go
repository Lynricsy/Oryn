package orbdrt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// 纵深防御（M11a 审查 S-P1-1）：orbd 自己起的子进程（omp、git、sudo、docker、dockerd、bash 等）
// 一律在「可信 PATH 目录」里解析成绝对路径再执行。可信目录 = orbd 启动时 PATH 中的绝对路径项，
// 去掉 $HOME 之下的（orb 镜像把 ~/.local/bin 放在 PATH 最前，setup 脚本或 agent 往那里放一个同名程序，
// orbd 下次调用时就会执行它）。解析只在 orbd 内部发生；交给子进程的 PATH 不变。

// binResolver 在固定的目录表里查找可执行文件。
type binResolver struct{ dirs []string }

// newBinResolver 由 PATH 值与 $HOME 构造：相对路径项（含空项，即当前目录）与 $HOME 之下的项都被去掉；
// home 为空时只去掉相对路径项。orb 与 runner（M21 起 agent 是下载的 Core，不再需要 ~/.bun/bin）都传真实 $HOME。
func newBinResolver(pathEnv, home string) binResolver {
	if home != "" {
		home = filepath.Clean(home)
	}
	var dirs []string
	seen := map[string]bool{}
	for _, d := range filepath.SplitList(pathEnv) {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		d = filepath.Clean(d)
		if (home != "" && underDir(d, home)) || seen[d] {
			continue
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	return binResolver{dirs: dirs}
}

// underDir 报告 p 是否就是 dir 或位于其下（按路径分量比较）。
func underDir(p, dir string) bool {
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// look 返回 name 的绝对路径。name 含路径分隔符时原样返回（由配置显式给出，例如 ORYN_AGENT_BIN）。
func (b binResolver) look(name string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		return name, nil
	}
	for _, d := range b.dirs {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: executable not found in trusted PATH directories %v", name, b.dirs)
}

// command 构造执行 name 的命令（不需要取消时传 context.Background()）。解析失败时命令照常返回，
// 但 Start / Run 会报这个错误，绝不回退到进程 PATH 查找。
func (b binResolver) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	path, err := b.look(name)
	if err != nil {
		path = name
	}
	cmd := exec.CommandContext(ctx, path, args...)
	if err != nil {
		cmd.Err = err
	}
	return cmd
}
