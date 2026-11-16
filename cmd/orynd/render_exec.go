package main

import (
	"os"

	"github.com/Lynricsy/Oryn/internal/artifacts/render"
)

// renderExec 是 `orynd render-exec --uid <n> --gid <n> --port <n> -- <chromium 参数…>` 的入口（M14 步骤 4.1）：
// 由 orynd 自己拉起，降权、设 rlimit 与 no_new_privs、用 Landlock 只放行连渲染代理的端口，然后执行 Chromium。
// 不读配置文件；失败时以 70 退出并在 stderr 写一行原因。
func renderExec(args []string) int {
	return render.ExecMain(args, os.Stderr)
}
