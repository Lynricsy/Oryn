package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/boxcfg"
)

// `oryn boxes …`（M23）：常驻机器与应用部署。orb 内经 box.call 由控制面执行（调用方是本线程的 agent），
// orb 外用设备令牌的 REST。部署只在 orb 内可用（release 经 orb link 取走）。

// newBoxesCmd 构造 `oryn boxes` 命令树；api 在测试时替换为假传输。
func newBoxesCmd(api func() (boxAPI, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "boxes",
		Aliases: []string{"box"},
		Short:   "Boxes：常驻机器与应用部署",
	}
	secrets := &cobra.Command{Use: "secrets", Short: "app 的密钥与环境变量"}
	secrets.AddCommand(newBoxSecretsSetCmd(api), newBoxSecretsLsCmd(api), newBoxSecretsRmCmd(api))
	cmd.AddCommand(
		newBoxListCmd(api),
		newBoxShowCmd(api),
		newBoxCreateCmd(api),
		newBoxStartStopCmd(api, "start"),
		newBoxStartStopCmd(api, "stop"),
		newBoxRenameCmd(api),
		newBoxScheduleCmd(api),
		newBoxDeleteCmd(api),
		newBoxDeploymentsCmd(api),
		newBoxDeployCmd(api),
		newBoxCancelDeployCmd(api),
		newBoxUndeployCmd(api),
		newBoxAppOpCmd(api, "restart", "重启 app 的全部服务（重新下发密钥）"),
		newBoxRollbackCmd(api),
		newBoxExecCmd(api),
		newBoxCpCmd(api),
		newBoxLogsCmd(api),
		secrets,
	)
	return cmd
}

func init() { root.AddCommand(newBoxesCmd(newBoxAPI)) }

func boxShow(ctx context.Context, a boxAPI, ref string) (apigen.Box, error) {
	var b apigen.Box
	err := a.call(ctx, "show", map[string]any{"box": ref}, &b)
	return b, err
}

// fmtSpec 返回 `type · N cpu · M GB`。
func fmtSpec(b apigen.Box) string {
	return fmt.Sprintf("%s · %g cpu · %s", b.Type, b.Cpus, fmtMemory(b.MemoryMB))
}

func fmtMemory(mb int) string {
	if mb >= 1024 && mb%1024 == 0 {
		return fmt.Sprintf("%d GB", mb/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

func boxTime(ms *int64) string {
	if ms == nil || *ms == 0 {
		return "-"
	}
	return time.UnixMilli(*ms).Local().Format("2006-01-02 15:04")
}

func boxStateText(b apigen.Box) string {
	s := string(b.State)
	if b.Desired == apigen.BoxDesiredStopped && b.StoppedBy != nil {
		s += " (stopped by " + string(*b.StoppedBy) + ")"
	}
	return s
}

func newBoxListCmd(api func() (boxAPI, error)) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "列出全部 box",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			var list apigen.BoxList
			if err := a.call(cmd.Context(), "list", nil, &list); err != nil {
				return err
			}
			if asJSON {
				return printJSONTo(cmd.OutOrStdout(), list)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSTATE\tSIZE\tAPPS\tLAST DEPLOY")
			for _, b := range list.Boxes {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", b.Name, boxStateText(b), fmtSpec(b), len(b.Apps), boxTime(b.LastDeployAt))
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	return c
}

func newBoxShowCmd(api func() (boxAPI, error)) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "show <box>",
		Short: "查看一台 box：时段、停机原因、各 app 的地址与 live commit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			b, err := boxShow(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSONTo(cmd.OutOrStdout(), b)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%s (%s)\n", b.Name, b.ID)
			fmt.Fprintf(w, "  state:    %s\n", boxStateText(b))
			if b.FailureReason != nil && *b.FailureReason != "" {
				fmt.Fprintf(w, "  failure:  %s\n", *b.FailureReason)
			}
			fmt.Fprintf(w, "  size:     %s (%s)\n", fmtSpec(b), b.Provider)
			if b.Schedule != nil {
				fmt.Fprintf(w, "  schedule: %s–%s %s\n", b.Schedule.On, b.Schedule.Off, b.Schedule.Tz)
			} else {
				fmt.Fprintln(w, "  schedule: always on")
			}
			for _, app := range b.Apps {
				commit := "deploying"
				if app.Live != nil {
					commit = shortSHA(app.Live.Commit)
				}
				vis := "public"
				if !app.Public {
					vis = "private"
				}
				fmt.Fprintf(w, "  app %s  %s  %s  %s\n", app.Name, deref(app.URL), vis, commit)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	return c
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func newBoxCreateCmd(api func() (boxAPI, error)) *cobra.Command {
	var provider, typ string
	c := &cobra.Command{
		Use:   "create <name>",
		Short: "新建 box",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			p := map[string]any{"name": args[0]}
			if provider != "" {
				p["provider"] = provider
			}
			if typ != "" {
				p["type"] = typ
			}
			var b apigen.Box
			if err := a.call(cmd.Context(), "create", p, &b); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created box %s (%s, %s)\n", b.Name, b.ID, fmtSpec(b))
			return nil
		},
	}
	c.Flags().StringVar(&provider, "provider", "", "provider 名（缺省按默认落点）")
	c.Flags().StringVar(&typ, "type", "", "orb 类型")
	return c
}

func newBoxStartStopCmd(api func() (boxAPI, error), op string) *cobra.Command {
	short := map[string]string{"start": "启动 box（保持到下一个时段边界）", "stop": "停止 box（保持到下一个时段边界）"}[op]
	return &cobra.Command{
		Use:   op + " <box>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			var b apigen.Box
			if err := a.call(cmd.Context(), op, map[string]any{"box": args[0]}, &b); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (desired %s)\n", b.Name, b.State, b.Desired)
			return nil
		},
	}
}

func newBoxRenameCmd(api func() (boxAPI, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <box> <new>",
		Short: "改名（app 地址不变）；顺带改写当前仓库 .oryn/box-deploy.json 里指向旧名的 target",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			old, err := boxShow(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			var b apigen.Box
			if err := a.call(cmd.Context(), "rename", map[string]any{"box": args[0], "name": args[1]}, &b); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "renamed %s to %s\n", old.Name, b.Name)
			changed, err := renameTargets(cmd.Context(), old.Name, old.ID, b.Name)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "oryn: %s not updated: %v\n", boxcfg.TargetsFile, err)
				return nil
			}
			for _, k := range changed {
				fmt.Fprintf(cmd.OutOrStdout(), "updated target %s in %s\n", k, boxcfg.TargetsFile)
			}
			return nil
		},
	}
}

// renameTargets 在当前 git 检出里把 `on` 为旧名（或 id）的 target 改成新名；不在检出里时什么都不做。
func renameTargets(ctx context.Context, oldName, id, newName string) ([]string, error) {
	repo, err := gitOut(ctx, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil
	}
	t, err := boxcfg.LoadTargets(repo)
	if err != nil {
		return nil, err
	}
	var changed []string
	for k, v := range t.Targets {
		if v.On == oldName || v.On == id {
			v.On = newName
			t.Targets[k] = v
			changed = append(changed, k)
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	return changed, boxcfg.SaveTargets(repo, t)
}

var hhmmRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func newBoxScheduleCmd(api func() (boxAPI, error)) *cobra.Command {
	var on, off, tz string
	var clear bool
	c := &cobra.Command{
		Use:   "schedule <box>",
		Short: "设置每日运行时段（--on HH:MM --off HH:MM --tz <IANA>），或 --clear 清除",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := map[string]any{"box": args[0]}
			if clear {
				p["clear"] = true
			} else {
				if !hhmmRe.MatchString(on) || !hhmmRe.MatchString(off) || tz == "" {
					return usageErr("schedule needs --on HH:MM --off HH:MM --tz <IANA time zone>, or --clear")
				}
				p["on"], p["off"], p["tz"] = on, off, tz
			}
			a, err := api()
			if err != nil {
				return err
			}
			var b apigen.Box
			if err := a.call(cmd.Context(), "schedule", p, &b); err != nil {
				return err
			}
			if b.Schedule != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s runs %s–%s %s\n", b.Name, b.Schedule.On, b.Schedule.Off, b.Schedule.Tz)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%s is always on\n", b.Name)
			}
			return nil
		},
	}
	c.Flags().StringVar(&on, "on", "", "开机时刻 HH:MM")
	c.Flags().StringVar(&off, "off", "", "关机时刻 HH:MM")
	c.Flags().StringVar(&tz, "tz", "", "IANA 时区，如 Europe/Berlin")
	c.Flags().BoolVar(&clear, "clear", false, "清除时段（一直运行）")
	return c
}

const boxDeleteInOrb = "deleting a box needs the owner: use the Boxes page or `oryn boxes delete` on a signed-in machine"

func newBoxDeleteCmd(api func() (boxAPI, error)) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "delete <box>",
		Short: "删除 box（连同全部 app 与数据，不可恢复）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			if a.inOrb() {
				return usageErr("%s", boxDeleteInOrb)
			}
			b, err := boxShow(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(cmd.ErrOrStderr(), "This deletes box %s with all %d apps and their data. Type the box name to confirm: ", b.Name, len(b.Apps))
				line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if strings.TrimSpace(line) != b.Name {
					return usageErr("confirmation did not match; nothing deleted")
				}
			}
			if err := a.call(cmd.Context(), "delete", map[string]any{"box": b.ID}, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted box %s\n", b.Name)
			return nil
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "不再确认")
	return c
}

func newBoxDeploymentsCmd(api func() (boxAPI, error)) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "deployments <box>",
		Short: "列出 box 上的 app：地址、公开性、仓库、分支、commit、部署时间",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			b, err := boxShow(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSONTo(cmd.OutOrStdout(), b.Apps)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "APP\tURL\tACCESS\tREPO\tBRANCH\tCOMMIT\tDEPLOYED")
			for _, app := range b.Apps {
				access := "public"
				if !app.Public {
					access = "private"
				}
				branch, commit, at := "-", "deploying", "deploying"
				if app.Live != nil {
					branch, commit, at = app.Live.Branch, shortSHA(app.Live.Commit), boxTime(&app.Live.ActivatedAt)
				} else if !app.Deploying {
					commit, at = "-", "-"
				}
				url := deref(app.URL)
				if url == "" {
					url = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", app.Name, url, access, app.Repo, branch, commit, at)
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	return c
}

// appFlag 给子命令加必填的 --app。
func appFlag(c *cobra.Command, app *string) {
	c.Flags().StringVar(app, "app", "", "app 名")
	_ = c.MarkFlagRequired("app")
}

func newBoxCancelDeployCmd(api func() (boxAPI, error)) *cobra.Command {
	var app string
	c := &cobra.Command{
		Use:   "cancel-deploy <box> --app <app>",
		Short: "取消 app 卡住的部署（已进入激活的不能取消）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			if err := a.call(cmd.Context(), "deploy.cancel", map[string]any{"box": args[0], "app": app}, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "canceled the deploy of %s\n", app)
			return nil
		},
	}
	appFlag(c, &app)
	return c
}

func newBoxUndeployCmd(api func() (boxAPI, error)) *cobra.Command {
	var app string
	var purge bool
	c := &cobra.Command{
		Use:   "undeploy <box> --app <app>",
		Short: "下线 app（数据目录保留；--purge-data 一并删除，只在 orb 外可用）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			p := map[string]any{"box": args[0], "app": app}
			if purge {
				if a.inOrb() {
					return usageErr("--purge-data is only available outside a thread's environment")
				}
				p["purgeData"] = true
			}
			if err := a.call(cmd.Context(), "undeploy", p, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "undeployed %s\n", app)
			return nil
		},
	}
	appFlag(c, &app)
	c.Flags().BoolVar(&purge, "purge-data", false, "连数据目录一起删除")
	return c
}

func newBoxAppOpCmd(api func() (boxAPI, error), op, short string) *cobra.Command {
	var app string
	c := &cobra.Command{
		Use:   op + " <box> --app <app>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			var res apigen.BoxApp
			if err := a.call(cmd.Context(), op, map[string]any{"box": args[0], "app": app}, &res); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s done\n", app, op)
			return nil
		},
	}
	appFlag(c, &app)
	return c
}

func newBoxRollbackCmd(api func() (boxAPI, error)) *cobra.Command {
	var app, to string
	c := &cobra.Command{
		Use:   "rollback <box> --app <app> [--to <releaseId>]",
		Short: "回滚到保留着的 release（缺省上一个）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			p := map[string]any{"box": args[0], "app": app}
			if to != "" {
				p["releaseId"] = to
			}
			var res apigen.BoxApp
			if err := a.call(cmd.Context(), "rollback", p, &res); err != nil {
				return err
			}
			live := "-"
			if res.Live != nil {
				live = res.Live.ID + " (" + shortSHA(res.Live.Commit) + ")"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s rolled back to %s\n", app, live)
			return nil
		},
	}
	appFlag(c, &app)
	c.Flags().StringVar(&to, "to", "", "目标 release id")
	return c
}

func newBoxExecCmd(api func() (boxAPI, error)) *cobra.Command {
	var app string
	var timeout int
	c := &cobra.Command{
		Use:   "exec <box> [--app <app>] [--timeout 秒] -- <命令…>",
		Short: "在 box 上执行命令（给了 --app 时在 app 的线上目录里）",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			if a.inOrb() && timeout > 1800 {
				return usageErr("--timeout is at most 1800 seconds inside a thread's environment")
			}
			if timeout > 3600 {
				return usageErr("--timeout is at most 3600 seconds")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			code, err := a.exec(ctx, boxExecParams{Box: args[0], App: app, Command: strings.Join(args[1:], " "), TimeoutSec: timeout},
				cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if code != 0 {
				return &exitCodeError{code: code, err: fmt.Errorf("remote command exited with %d", code)}
			}
			return nil
		},
	}
	c.Flags().StringVar(&app, "app", "", "在 app 的线上目录里执行")
	c.Flags().IntVar(&timeout, "timeout", 0, "超时秒数（缺省 600；orb 内至多 1800，orb 外至多 3600）")
	return c
}

var boxSideRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?|box_[0-9A-Za-z]+):(.*)$`)

// parseCpArgs 定出哪一侧是 `<box>:<路径>`；恰有一侧是。
func parseCpArgs(src, dst string) (box, boxPath, local string, toBox bool, err error) {
	ms, md := boxSideRe.FindStringSubmatch(src), boxSideRe.FindStringSubmatch(dst)
	switch {
	case ms != nil && md == nil:
		box, boxPath, local = ms[1], ms[3], dst
	case md != nil && ms == nil:
		box, boxPath, local, toBox = md[1], md[3], src, true
	default:
		return "", "", "", false, usageErr("exactly one side of cp must be <box>:<path>")
	}
	clean := filepathToSlashClean(boxPath)
	if boxPath == "" || strings.HasPrefix(boxPath, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", "", false, usageErr("the box path must be relative (to ~/ or the app's data directory) and stay inside it")
	}
	local, err = filepath.Abs(local)
	if err != nil {
		return "", "", "", false, err
	}
	return box, clean, local, toBox, nil
}

func filepathToSlashClean(p string) string { return filepath.ToSlash(filepath.Clean(p)) }

func newBoxCpCmd(api func() (boxAPI, error)) *cobra.Command {
	var app string
	c := &cobra.Command{
		Use:   "cp <源> <目标> [--app <app>]",
		Short: "在本地与 box 之间复制文件（恰有一侧是 <box>:<路径>；box 路径相对 ~/，给了 --app 则相对其数据目录）",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			box, boxPath, local, toBox, err := parseCpArgs(args[0], args[1])
			if err != nil {
				return err
			}
			a, err := api()
			if err != nil {
				return err
			}
			if a.inOrb() {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				if rel, err := filepath.Rel(home, local); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
					return usageErr("inside a thread's environment the local path must be under %s", home)
				}
			}
			n, err := a.copy(cmd.Context(), boxCopyParams{Box: box, App: app, BoxPath: boxPath, LocalPath: local, ToBox: toBox})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "copied %d bytes\n", n)
			return nil
		},
	}
	c.Flags().StringVar(&app, "app", "", "box 路径相对该 app 的数据目录")
	return c
}

func newBoxLogsCmd(api func() (boxAPI, error)) *cobra.Command {
	var app, svc string
	var tail int
	c := &cobra.Command{
		Use:   "logs <box> --app <app> [--service S] [--tail N]",
		Short: "app 服务的日志末尾",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			p := map[string]any{"box": args[0], "app": app}
			if svc != "" {
				p["service"] = svc
			}
			if tail > 0 {
				p["tail"] = tail
			}
			var res struct {
				Text string `json:"text"`
			}
			if err := a.call(cmd.Context(), "logs", p, &res); err != nil {
				return err
			}
			_, err = io.WriteString(cmd.OutOrStdout(), res.Text)
			return err
		},
	}
	appFlag(c, &app)
	c.Flags().StringVar(&svc, "service", "", "服务名（缺省第一个）")
	c.Flags().IntVar(&tail, "tail", 0, "末尾字节数（缺省 65536，至多 1048576）")
	return c
}

func newBoxSecretsSetCmd(api func() (boxAPI, error)) *cobra.Command {
	var box, app, dataFile, value string
	var plain bool
	c := &cobra.Command{
		Use:   "set <NAME> --box <box> --app <app> (--data-file F | --value V) [--plain]",
		Short: "设置密钥（--plain 为明文环境变量）；重启或下次部署后生效",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (dataFile == "") == !cmd.Flags().Changed("value") {
				return usageErr("give exactly one of --data-file or --value")
			}
			if dataFile != "" {
				b, err := os.ReadFile(dataFile)
				if err != nil {
					return err
				}
				value = string(b)
			}
			a, err := api()
			if err != nil {
				return err
			}
			p := map[string]any{"box": box, "app": app, "name": args[0], "value": value, "secret": !plain}
			if err := a.call(cmd.Context(), "secrets.set", p, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "set %s; run oryn boxes restart %s --app %s (or deploy again) to apply\n", args[0], box, app)
			return nil
		},
	}
	c.Flags().StringVar(&box, "box", "", "box 名")
	_ = c.MarkFlagRequired("box")
	appFlag(c, &app)
	c.Flags().StringVar(&dataFile, "data-file", "", "从文件读取值")
	c.Flags().StringVar(&value, "value", "", "值")
	c.Flags().BoolVar(&plain, "plain", false, "明文环境变量（API 可读回值）")
	return c
}

func newBoxSecretsLsCmd(api func() (boxAPI, error)) *cobra.Command {
	var box, app string
	c := &cobra.Command{
		Use:   "ls --box <box> --app <app>",
		Short: "列出 app 的密钥与环境变量",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			var list apigen.BoxSecretList
			if err := a.call(cmd.Context(), "secrets.list", map[string]any{"box": box, "app": app}, &list); err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tKIND\tVALUE\tUPDATED")
			for _, s := range list.Secrets {
				kind, val := "secret", "******"
				if !s.Secret {
					kind, val = "plain", deref(s.Value)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, kind, val, boxTime(&s.UpdatedAt))
			}
			return tw.Flush()
		},
	}
	c.Flags().StringVar(&box, "box", "", "box 名")
	_ = c.MarkFlagRequired("box")
	appFlag(c, &app)
	return c
}

func newBoxSecretsRmCmd(api func() (boxAPI, error)) *cobra.Command {
	var box, app string
	c := &cobra.Command{
		Use:   "rm <NAME> --box <box> --app <app>",
		Short: "删除密钥或环境变量",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := api()
			if err != nil {
				return err
			}
			if err := a.call(cmd.Context(), "secrets.unset", map[string]any{"box": box, "app": app, "name": args[0]}, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return nil
		},
	}
	c.Flags().StringVar(&box, "box", "", "box 名")
	_ = c.MarkFlagRequired("box")
	appFlag(c, &app)
	return c
}

// gitOut 在 dir（空 = 当前目录）里跑 git 并返回去掉首尾空白的 stdout。
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
