// fakeomp 是脚本化的 omp RPC 替身（计划 M1 G8），供 orbd / 控制面的测试使用。
//
// 用法与 omp 一致：`fakeomp --mode rpc-ui --cwd <dir> --session-dir <dir>`，
// stdin 读命令、stdout 写帧（JSONL）。可选：
//
//	--script <file>   用脚本替换默认的 prompt 帧序列（也可用环境变量 FAKEOMP_SCRIPT）
//	--delay <dur>     流式帧之间的间隔（默认 20ms；也可用 FAKEOMP_DELAY_MS）
//	-e <path>         扩展文件（与 omp 同名参数，接受并忽略）
//	--no-tools 等     M9 协调档的开关（--no-tools --no-skills --no-extensions --no-rules --no-context-files --no-lsp，接受并忽略）
//	--system-prompt   系统提示词（不含换行的值按文件路径读，同 omp 的 resolvePromptInput）；首行 `#dump <HOME 内路径>`
//	                  时把参数、环境、uid、cwd、解析后的提示词写成 JSON 到该路径
//
// 环境变量 FAKEOMP_DUMP_DIR 非空时同样转储到 `<dir>/<ORYN_THREAD_ID 或 pid>.json`。
// 提示词里的 `hosttool <name> <json>` 行发起宿主工具调用（host_tool_call → 等 host_tool_result），
// `probe <read|write|exec|tcp|unix|kill> <arg>` 与 `probe args` 行在本进程内做探测；
// 回合最后的 assistant 文本每步一行：`host:<name>:<ok|error>:<结果前 200 字符>` / `probe:<op>:<ok|错误串>`。
// 当前模型 id 为 `does-not-exist` 或提示词含 `fakeomp:error` 时，回合以 provider 错误结束：assistant 消息
// stopReason=error 带 errorMessage，prompt_result{status:"error", error:{message}}（与 omp 同形）。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}
