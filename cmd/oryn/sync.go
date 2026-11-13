package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/syncclient"
)

// `oryn sync`（M5 计划 4.4）：参数与信号；引擎在 internal/syncclient。

const (
	exitSyncPrecondition = 2
	exitSyncLocalEdits   = 3
)

type syncOptions struct {
	apply, detach, force, restore bool
	interval                      time.Duration
	root                          string
}

var syncOpts syncOptions

var syncCmd = &cobra.Command{
	Use:   "sync <thread>",
	Short: "把线程 orb 里的提交与未提交改动镜像到当前目录的干净检出（Ctrl-C 还原；--apply 一次性应用）",
	Long: "持续镜像：本地 HEAD 游离在 orb 的提交上，工作区 = orb 的工作区；Ctrl-C / SIGTERM 后还原到原分支原提交。\n" +
		"镜像期间本地文件被改动时停止且不覆盖（退出码 3），处理后用 `oryn sync --restore` 还原。\n" +
		"--apply：一次性挂到与 orb 同名的分支（能快进时）并带上未提交改动。\n" +
		"退出码：0 成功；1 运行或接口错误；2 前置条件不满足（不在仓库、检出不干净、有未还原的状态）；3 本地改动。",
	Args: func(cmd *cobra.Command, args []string) error {
		if syncOpts.restore {
			return cobra.MaximumNArgs(1)(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	Run: func(cmd *cobra.Command, args []string) {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		thread := ""
		if len(args) > 0 {
			thread = args[0]
		}
		exitIfNot0(runSync(ctx, thread, syncOpts, os.Stderr))
	},
}

func runSync(ctx context.Context, threadID string, o syncOptions, errw io.Writer) int {
	dir, err := os.Getwd()
	if err != nil {
		return syncExit(err, errw)
	}
	if o.restore {
		if err := syncclient.Restore(dir); err != nil {
			return syncExit(err, errw)
		}
		fmt.Fprintln(errw, "已还原")
		return 0
	}
	c, err := newClient()
	if err != nil {
		return syncExit(err, errw)
	}
	var t apigen.Thread
	if err := c.do(ctx, http.MethodGet, "/api/v1/threads/"+url.PathEscape(threadID), nil, &t); err != nil {
		return syncExit(err, errw)
	}
	if t.OrbID == nil {
		return syncExit(errors.New("线程没有 orb"), errw)
	}
	warnOriginMismatch(ctx, c, t, dir, errw)
	remote := &syncclient.HTTPRemote{BaseURL: c.base, Token: c.token, OrbID: *t.OrbID}
	opts := syncclient.Options{
		Dir: dir, ThreadID: t.ID, Root: o.root, Interval: o.interval, Force: o.force, Detach: o.detach,
		Log: func(format string, a ...any) { fmt.Fprintf(errw, format+"\n", a...) },
	}
	if o.apply {
		return syncExit(syncclient.Apply(ctx, remote, opts), errw)
	}
	fmt.Fprintf(errw, "正在把 %s 镜像到 %s（Ctrl-C 结束并还原）\n", t.ID, dir)
	if err := syncclient.Mirror(ctx, remote, opts); err != nil {
		return syncExit(err, errw)
	}
	fmt.Fprintln(errw, "已还原")
	return 0
}

// syncExit 打印错误并给出退出码。
func syncExit(err error, errw io.Writer) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	switch syncclient.Code(err) {
	case "orbd_outdated":
		msg = "这台 orb 创建于旧版本，不支持 sync"
	case "sync_shallow":
		msg += "\n提示：orb 里的仓库是浅克隆；先让本地有一个与 orb 共同的提交（`git fetch origin`），或在项目设置里关闭浅克隆"
	}
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == "not_found" {
		msg = "线程不存在"
	}
	fmt.Fprintln(errw, "oryn:", msg)
	switch {
	case errors.Is(err, syncclient.ErrNotARepo), errors.Is(err, syncclient.ErrDirty), errors.Is(err, syncclient.ErrStaleState):
		return exitSyncPrecondition
	case errors.Is(err, syncclient.ErrLocalEdits):
		return exitSyncLocalEdits
	}
	return 1
}

// warnOriginMismatch：线程所属项目有仓库地址而本地 origin 与之不同 → 警告（不阻止）。
func warnOriginMismatch(ctx context.Context, c *apiClient, t apigen.Thread, dir string, errw io.Writer) {
	if t.ProjectID == nil {
		return
	}
	var p apigen.Project
	if c.do(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(*t.ProjectID), nil, &p) != nil || p.RepoURL == nil || *p.RepoURL == "" {
		return
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return
	}
	if local := strings.TrimSpace(string(out)); normalizeRepoURL(local) != normalizeRepoURL(*p.RepoURL) {
		fmt.Fprintf(errw, "警告：本地 origin（%s）与项目仓库（%s）不同\n", local, *p.RepoURL)
	}
}

// normalizeRepoURL 去掉协议、用户与 .git 后缀，scp 形式（git@host:path）转为 host/path，主机名小写。
func normalizeRepoURL(s string) string {
	s = strings.TrimSpace(s)
	scheme := false
	if i := strings.Index(s, "://"); i >= 0 {
		s, scheme = s[i+3:], true
	}
	if i := strings.Index(s, "@"); i >= 0 && (strings.Index(s, "/") < 0 || i < strings.Index(s, "/")) {
		s = s[i+1:]
	}
	if !scheme {
		if i := strings.Index(s, ":"); i >= 0 && (strings.Index(s, "/") < 0 || i < strings.Index(s, "/")) {
			s = s[:i] + "/" + s[i+1:]
		}
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	host, rest, _ := strings.Cut(s, "/")
	return strings.ToLower(host) + "/" + rest
}

func init() {
	f := syncCmd.Flags()
	f.BoolVar(&syncOpts.apply, "apply", false, "一次性应用后退出（不写状态、不还原）")
	f.BoolVar(&syncOpts.detach, "detach", false, "--apply 时总是游离检出")
	f.DurationVar(&syncOpts.interval, "interval", 3*time.Second, "镜像模式的轮询间隔")
	f.StringVar(&syncOpts.root, "root", "", "orb 内仓库路径（缺省 = orb 当前工作目录）")
	f.BoolVar(&syncOpts.force, "force", false, "本地与 orb 没有共同提交时仍同步（传输完整历史）")
	f.BoolVar(&syncOpts.restore, "restore", false, "按状态文件还原上一次中断的镜像")
	root.AddCommand(syncCmd)
}
