// fakeomp 是脚本化的 omp RPC 替身（计划 M1 G8），供 orbd / 控制面的测试使用。
//
// 用法与 omp 一致：`fakeomp --mode rpc-ui --cwd <dir> --session-dir <dir>`，
// stdin 读命令、stdout 写帧（JSONL）。可选：
//
//	--script <file>   用脚本替换默认的 prompt 帧序列（也可用环境变量 FAKEOMP_SCRIPT）
//	--delay <dur>     流式帧之间的间隔（默认 20ms；也可用 FAKEOMP_DELAY_MS）
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
