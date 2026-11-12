package orbdrt

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// loginEnvTimeout 是求登录 shell 环境的上限；超时或失败时沿用原环境（测试可调小）。
var loginEnvTimeout = 10 * time.Second

// loginEnvWaitDelay 是 bash 退出后等待 stdout 关闭的上限：profile 里 `cmd &` 启动的后代进程会继承并
// 一直持有 stdout 管道，不设上限时 Run 会等到它们退出为止，超时也拦不住（ctx 只杀 bash 本身）。
var loginEnvWaitDelay = time.Second

// loginShellSkip 是 bash 自己维护、不应从登录 shell 带出的变量。
var loginShellSkip = map[string]bool{"_": true, "SHLVL": true, "PWD": true, "OLDPWD": true}

// loginEnv 在 dir 下以 base 为环境启动 `bash -lc 'env -0'`，返回登录 shell 求出的完整环境
// （~/.profile / ~/.bash_profile 中的 PATH 与导出变量都在其中）。
//
// 这样 setup 写进 profile 的开发环境对之后启动的 agent 与 resume 脚本生效：omp 的 bash 工具只叠加
// ~/.bashrc 的快照，不读 profile，agent 进程环境就是命令的基础环境。base 中的键在登录 shell 里
// 被删掉的，结果中同样删除；失败时返回 base 的副本。
func loginEnv(ctx context.Context, dir string, base map[string]string) map[string]string {
	ctx, cancel := context.WithTimeout(ctx, loginEnvTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-lc", "env -0")
	cmd.Dir = dir
	cmd.Env = envList(base)
	cmd.Stdin = nil
	var out bytes.Buffer
	cmd.Stdout = &out
	// 独立进程组：超时时连同 profile 拉起的后代一起杀掉
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = loginEnvWaitDelay
	err := cmd.Run()
	if cmd.Process != nil {
		// bash 已退出；组里剩下的只可能是 profile 留下的后台进程，它们不该随 agent / 脚本存活
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// ErrWaitDelay：bash 正常退出（`env -0` 已写完），只是后代仍占着 stdout，输出可用
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		slog.Warn("登录 shell 环境求值失败，沿用原环境", "dir", dir, "err", err)
		return maps.Clone(base)
	}
	env := make(map[string]string, len(base))
	for _, kv := range strings.Split(out.String(), "\x00") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || loginShellSkip[k] {
			continue
		}
		env[k] = v
	}
	if len(env) == 0 {
		return maps.Clone(base)
	}
	return env
}
