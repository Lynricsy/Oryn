package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// `oryn sandbox …`（M30）：agent 沙箱。orb 内经 orbd.sock 的 sandbox.call 由控制面执行（权限主体是本 orb），
// orb 外只有 ls 与 rm（设备令牌的 REST）。

// sandboxAPI 是沙箱命令的传输层；测试时替换为假传输。
type sandboxAPI interface {
	// inOrb 报告是否在 orb 内（走 sandbox.call）。
	inOrb() bool
	// call 执行一个 op，把结果解到 out（nil 时丢弃）；onLog 收 exec 的输出。
	call(ctx context.Context, op string, params, out any, onLog func(stream, data string)) error
}

func newSandboxCmd(api func() (sandboxAPI, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sandbox",
		Aliases: []string{"sandboxes", "sbx"},
		Short:   "Agent 沙箱：零凭据、用完即焚的隔离环境",
	}
	jobs := &cobra.Command{
		Use:   "jobs <id> [ls | logs <job> | kill <job>]",
		Short: "沙箱里的后台作业",
		Args:  cobra.RangeArgs(1, 3),
	}
	var offset int64
	jobs.Flags().Int64Var(&offset, "offset", -1, "logs：从这个字节偏移开始读（省略 = 末尾 256 KiB）")
	jobs.RunE = func(cmd *cobra.Command, args []string) error {
		a, err := inOrbAPI(api, "jobs")
		if err != nil {
			return err
		}
		return sandboxJobs(cmd, a, args, offset)
	}
	cmd.AddCommand(
		newSandboxUpCmd(api),
		newSandboxExecCmd(api),
		newSandboxCpCmd(api),
		newSandboxNetCmd(api),
		jobs,
		newSandboxExtendCmd(api),
		newSandboxLsCmd(api),
		newSandboxRmCmd(api),
	)
	return cmd
}

func init() { root.AddCommand(newSandboxCmd(newSandboxAPI)) }

// inOrbAPI 取传输层并要求在 orb 内。
func inOrbAPI(api func() (sandboxAPI, error), sub string) (sandboxAPI, error) {
	a, err := api()
	if err != nil {
		return nil, err
	}
	if !a.inOrb() {
		return nil, usageErr("`oryn sandbox %s` 只能在 orb 内使用", sub)
	}
	return a, nil
}

// ceilMinutes 把时长向上取整到分钟。
func ceilMinutes(d time.Duration) int {
	return int((d + time.Minute - 1) / time.Minute)
}

func newSandboxUpCmd(api func() (sandboxAPI, error)) *cobra.Command {
	var network, backend, typ string
	var idle, ttl time.Duration
	var asJSON bool
	c := &cobra.Command{
		Use:   "up --network none|egress [--backend docker|kvm] [--type T] [--idle 30m] [--ttl 2h]",
		Short: "开一台沙箱（就绪后打印 id）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := inOrbAPI(api, "up")
			if err != nil {
				return err
			}
			if network != "none" && network != "egress" {
				return usageErr("--network 必须是 none 或 egress")
			}
			p := map[string]any{"network": network}
			if backend != "" {
				p["backend"] = backend
			}
			if typ != "" {
				p["type"] = typ
			}
			if idle > 0 {
				p["idleMinutes"] = ceilMinutes(idle)
			}
			if ttl > 0 {
				p["ttlMinutes"] = ceilMinutes(ttl)
			}
			var res struct {
				Sandbox apigen.Sandbox `json:"sandbox"`
			}
			if err := a.call(cmd.Context(), "create", p, &res, nil); err != nil {
				return err
			}
			if asJSON {
				return printJSONTo(cmd.OutOrStdout(), res.Sandbox)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), res.Sandbox.ID)
			return err
		},
	}
	c.Flags().StringVar(&network, "network", "", "none = 完全没有网卡；egress = 可访问公网（必填）")
	c.Flags().StringVar(&backend, "backend", "", "docker 或 kvm（省略由 Oryn 选择）")
	c.Flags().StringVar(&typ, "type", "", "provider 的 orb 类型名（省略取默认）")
	c.Flags().DurationVar(&idle, "idle", 0, "空闲多久后销毁（5m–4h，缺省 30m）")
	c.Flags().DurationVar(&ttl, "ttl", 0, "绝对期限：多久后销毁（哪怕命令在跑）")
	c.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出整个沙箱")
	_ = c.MarkFlagRequired("network")
	return c
}

func newSandboxExecCmd(api func() (sandboxAPI, error)) *cobra.Command {
	var cwd string
	var env []string
	var timeout int
	var detach bool
	c := &cobra.Command{
		Use:   "exec <id> [--cwd D] [--env K=V]... [--timeout 600] [--detach] -- <命令…>",
		Short: "在沙箱里运行命令（输出实时转发，退出码透传；--detach 起后台作业）",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := inOrbAPI(api, "exec")
			if err != nil {
				return err
			}
			p := map[string]any{"sandbox": args[0], "command": strings.Join(args[1:], " ")}
			if cwd != "" {
				p["cwd"] = cwd
			}
			if len(env) > 0 {
				m := map[string]string{}
				for _, kv := range env {
					k, v, ok := strings.Cut(kv, "=")
					if !ok || k == "" {
						return usageErr("--env 必须是 K=V：%q", kv)
					}
					m[k] = v
				}
				p["env"] = m
			}
			if cmd.Flags().Changed("timeout") {
				p["timeoutSec"] = timeout
			}
			if detach {
				p["detach"] = true
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			var res struct {
				Exit  *int   `json:"exit"`
				JobID string `json:"jobId"`
			}
			stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
			err = a.call(ctx, "exec", p, &res, func(stream, data string) {
				if stream == orbsock.StreamStderr {
					_, _ = io.WriteString(stderr, data)
				} else {
					_, _ = io.WriteString(stdout, data)
				}
			})
			if err != nil {
				return err
			}
			if detach {
				_, err = fmt.Fprintln(stdout, res.JobID)
				return err
			}
			if res.Exit != nil && *res.Exit != 0 {
				return &exitCodeError{code: *res.Exit, err: fmt.Errorf("remote command exited with %d", *res.Exit)}
			}
			return nil
		},
	}
	c.Flags().StringVar(&cwd, "cwd", "", "沙箱里的工作目录（缺省 ~）")
	c.Flags().StringArrayVar(&env, "env", nil, "额外的环境变量 K=V（可重复）")
	c.Flags().IntVar(&timeout, "timeout", 600, "超时秒数（同步 1–1800；--detach 时 0 = 不限）")
	c.Flags().BoolVar(&detach, "detach", false, "作为后台作业启动并打印作业 id")
	return c
}

var sandboxSideRe = regexp.MustCompile(`^(sbx_[0-9A-Za-z]+):(.+)$`)

// parseSandboxCp 定出哪一侧是 `<id>:<路径>`（恰有一侧是），orb 一侧的相对路径按当前目录转成绝对路径。
func parseSandboxCp(src, dst string) (id, direction, source, dest string, err error) {
	ms, md := sandboxSideRe.FindStringSubmatch(src), sandboxSideRe.FindStringSubmatch(dst)
	switch {
	case md != nil && ms == nil:
		id, direction, source, dest = md[1], "to_sandbox", src, md[2]
		source, err = orbSidePath(source)
	case ms != nil && md == nil:
		id, direction, source, dest = ms[1], "from_sandbox", ms[2], dst
		dest, err = orbSidePath(dest)
	default:
		err = usageErr("cp 恰有一侧必须是 <sandbox>:<路径>")
	}
	return id, direction, source, dest, err
}

// orbSidePath：`~` 开头的原样交给 orbd，其余转成绝对路径。
func orbSidePath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || filepath.IsAbs(p) {
		return p, nil
	}
	return filepath.Abs(p)
}

func newSandboxCpCmd(api func() (sandboxAPI, error)) *cobra.Command {
	var exclude []string
	var gitignore bool
	c := &cobra.Command{
		Use:   "cp [--exclude P]... [--gitignore] <src> <dst>",
		Short: "在本机与沙箱之间复制文件或目录（恰有一侧形如 <id>:<路径>；dst 是完整目标路径）",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := inOrbAPI(api, "cp")
			if err != nil {
				return err
			}
			id, direction, source, dest, err := parseSandboxCp(args[0], args[1])
			if err != nil {
				return err
			}
			p := map[string]any{"sandbox": id, "direction": direction, "source": source, "dest": dest}
			if len(exclude) > 0 {
				p["exclude"] = exclude
			}
			if gitignore {
				p["gitignore"] = true
			}
			var res apigen.OrbFsTarResult
			if err := a.call(cmd.Context(), "cp", p, &res, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "copied %d files, %d bytes\n", res.Files, res.Bytes)
			if res.Skipped > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "skipped %d entries (links leaving the destination, hard links or special files)\n", res.Skipped)
			}
			return nil
		},
	}
	c.Flags().StringArrayVar(&exclude, "exclude", nil, "排除的 glob（带 / 匹配相对路径，否则匹配任意层的文件名；可重复）")
	c.Flags().BoolVar(&gitignore, "gitignore", false, "只复制 git 会跟踪的文件（源是 git 工作树里的目录）")
	return c
}

func newSandboxNetCmd(api func() (sandboxAPI, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "net <id> none|egress",
		Short: "切换沙箱的网络（创建时为 none 的不能开网）",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := inOrbAPI(api, "net")
			if err != nil {
				return err
			}
			if args[1] != "none" && args[1] != "egress" {
				return usageErr("网络必须是 none 或 egress")
			}
			var res struct {
				Sandbox apigen.Sandbox `json:"sandbox"`
			}
			if err := a.call(cmd.Context(), "network", map[string]any{"sandbox": args[0], "network": args[1]}, &res, nil); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s network: %s\n", res.Sandbox.ID, res.Sandbox.Network)
			return err
		},
	}
}

func sandboxJobs(cmd *cobra.Command, a sandboxAPI, args []string, offset int64) error {
	id, action := args[0], "ls"
	if len(args) > 1 {
		action = args[1]
	}
	out := cmd.OutOrStdout()
	switch action {
	case "ls":
		if len(args) > 2 {
			return usageErr("用法：oryn sandbox jobs <id> [ls]")
		}
		var res apigen.OrbSandboxJobList
		if err := a.call(cmd.Context(), "jobs", map[string]any{"sandbox": id, "action": "list"}, &res, nil); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "JOB\tSTATE\tEXIT\tCOMMAND")
		for _, j := range res.Jobs {
			exit := "-"
			if j.Exit != nil {
				exit = fmt.Sprint(*j.Exit)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", j.ID, j.State, exit, j.Command)
		}
		return tw.Flush()
	case "logs", "kill":
		if len(args) != 3 {
			return usageErr("用法：oryn sandbox jobs <id> %s <job>", action)
		}
		if action == "kill" {
			if err := a.call(cmd.Context(), "jobs", map[string]any{"sandbox": id, "action": "kill", "job": args[2]}, nil, nil); err != nil {
				return err
			}
			_, err := fmt.Fprintf(out, "killed %s\n", args[2])
			return err
		}
		p := map[string]any{"sandbox": id, "action": "logs", "job": args[2]}
		if offset >= 0 {
			p["offset"] = offset
		}
		var res apigen.OrbSandboxJobLog
		if err := a.call(cmd.Context(), "jobs", p, &res, nil); err != nil {
			return err
		}
		if _, err := io.WriteString(out, res.Text); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "next offset: %d\n", res.NextOffset)
		return nil
	}
	return usageErr("未知的 jobs 子命令 %q（ls、logs、kill）", action)
}

func newSandboxExtendCmd(api func() (sandboxAPI, error)) *cobra.Command {
	var ttl time.Duration
	c := &cobra.Command{
		Use:   "extend <id> --ttl 1h",
		Short: "把沙箱的绝对期限设为从现在起 --ttl（不超过最长寿命）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := inOrbAPI(api, "extend")
			if err != nil {
				return err
			}
			if ttl <= 0 {
				return usageErr("--ttl 必须为正")
			}
			var res struct {
				Sandbox apigen.Sandbox `json:"sandbox"`
			}
			if err := a.call(cmd.Context(), "extend", map[string]any{"sandbox": args[0], "ttlMinutes": ceilMinutes(ttl)}, &res, nil); err != nil {
				return err
			}
			at := "-"
			if res.Sandbox.TTLDeadline != nil {
				at = time.UnixMilli(*res.Sandbox.TTLDeadline).Local().Format(time.DateTime)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s expires at %s\n", res.Sandbox.ID, at)
			return err
		},
	}
	c.Flags().DurationVar(&ttl, "ttl", 0, "从现在起多久后销毁（必填）")
	_ = c.MarkFlagRequired("ttl")
	return c
}

func newSandboxLsCmd(api func() (sandboxAPI, error)) *cobra.Command {
	var asJSON bool
	var thread string
	c := &cobra.Command{
		Use:   "ls [--json] [--thread <id>]",
		Short: "列出沙箱（orb 内：本机的；orb 外：全部在用的，或 --thread 线程名下的）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			if a.inOrb() && thread != "" {
				return usageErr("--thread 只能在 orb 外使用")
			}
			var res apigen.SandboxList
			if err := a.call(cmd.Context(), "list", map[string]any{"thread": thread}, &res, nil); err != nil {
				return err
			}
			if asJSON {
				return printJSONTo(cmd.OutOrStdout(), res)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tBACKEND\tTYPE\tNETWORK\tSTATE\tIDLE-LEFT")
			now := time.Now()
			for _, s := range res.Sandboxes {
				state := string(s.State)
				if s.DestroyReason != nil {
					state += " (" + *s.DestroyReason + ")"
				}
				left := "-"
				if s.State == apigen.SandboxStateReady {
					left = max(time.UnixMilli(s.IdleDeadline).Sub(now), 0).Round(time.Second).String()
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Backend, s.Type, s.Network, state, left)
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	c.Flags().StringVar(&thread, "thread", "", "只列这个线程名下的（orb 外）")
	return c
}

func newSandboxRmCmd(api func() (sandboxAPI, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id>",
		Short: "销毁沙箱",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			if err := a.call(cmd.Context(), "destroy", map[string]any{"sandbox": args[0]}, nil, nil); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "destroyed %s\n", args[0])
			return err
		},
	}
}

// ---------------------------------------------------------------- 传输层

func newSandboxAPI() (sandboxAPI, error) {
	if inOrb() {
		thread, err := callerThread()
		if err != nil {
			return nil, err
		}
		return &orbSandboxAPI{thread: thread}, nil
	}
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	return &restSandboxAPI{c: c}, nil
}

// orbSandboxAPI 经 orbd.sock 的 sandbox.call 调控制面。
type orbSandboxAPI struct{ thread string }

func (a *orbSandboxAPI) inOrb() bool { return true }

func (a *orbSandboxAPI) call(ctx context.Context, op string, params, out any, onLog func(stream, data string)) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	err = sockCall(ctx, orbsock.MethodSandboxCall, orbsock.SandboxCallParams{ThreadID: a.thread, Op: op, Params: raw}, out, onLog)
	if err != nil {
		return &exitCodeError{code: 2, err: err}
	}
	return nil
}

// restSandboxAPI 是 orb 外的 REST：只有 list 与 destroy。
type restSandboxAPI struct{ c *apiClient }

func (a *restSandboxAPI) inOrb() bool { return false }

func (a *restSandboxAPI) call(ctx context.Context, op string, params, out any, _ func(stream, data string)) error {
	p, _ := params.(map[string]any)
	var err error
	switch op {
	case "list":
		path := "/api/v1/sandboxes"
		if t, _ := p["thread"].(string); t != "" {
			path += "?" + url.Values{"thread": {t}}.Encode()
		}
		err = a.c.do(ctx, http.MethodGet, path, nil, out)
	case "destroy":
		id, _ := p["sandbox"].(string)
		err = a.c.do(ctx, http.MethodDelete, "/api/v1/sandboxes/"+escapePathParam(id), nil, out)
	default:
		return usageErr("`oryn sandbox` 的 %s 只能在 orb 内使用", op)
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return &exitCodeError{code: 2, err: fmt.Errorf("%s: %s", ae.Code, ae.Message)}
	}
	return err
}
