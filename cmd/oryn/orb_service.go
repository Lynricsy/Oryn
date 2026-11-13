package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// `oryn orb service …`：orb 内经 orbd 本地控制口管理受管服务（M4）。只在 orb 内（ORYN_ORB=1）可用。
//
// 输出约定：每个受影响的服务往 stderr 写一行 `<name>: <status>, port <port>`，有 PUBLIC_URL 的再往 stdout
// 写一行 `<publicUrl>/`。start / restart 的退出码：0 = 全部 running；1 = 有服务不是 running；2 = 调用错误。

const (
	exitServiceOK         = 0
	exitServiceNotRunning = 1
	exitServiceUsage      = 2
)

// serviceLogPoll 是 `logs -f` 的轮询间隔。
var serviceLogPoll = 200 * time.Millisecond

var serviceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

var orbServiceCmd = &cobra.Command{
	Use:   "service",
	Short: "orb 内：启动 / 停止 / 查看受管服务（.oryn/services.yaml 与临时服务）",
}

var orbServiceStartCmd = &cobra.Command{
	Use:   "start [name]",
	Short: "无 name：按 services.yaml 确保全部服务运行；--command：建立 / 替换临时服务；否则启动已声明的服务",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		f := cmd.Flags()
		o := serviceStartOpts{}
		if len(args) == 1 {
			o.name = args[0]
		}
		o.command, _ = f.GetString("command")
		o.hasCommand = f.Changed("command")
		o.port, _ = f.GetInt("port")
		o.cwd, _ = f.GetString("cwd")
		o.env, _ = f.GetStringArray("env")
		o.health, _ = f.GetString("health")
		o.portal, _ = f.GetBool("portal")
		exitIfNot0(serviceStart(cmd.Context(), o, os.Stdout, os.Stderr))
	},
}

var orbServiceStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "停止服务（--remove：同时删除临时服务的定义）",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		remove, _ := cmd.Flags().GetBool("remove")
		exitIfNot0(serviceStop(cmd.Context(), args[0], remove, os.Stdout, os.Stderr))
	},
}

var orbServiceRestartCmd = &cobra.Command{
	Use:   "restart <name>",
	Short: "重读 services.yaml 后重启服务",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		exitIfNot0(serviceRestart(cmd.Context(), args[0], os.Stdout, os.Stderr))
	},
}

var orbServiceLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "列出受管服务",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, _ []string) {
		asJSON, _ := cmd.Flags().GetBool("json")
		exitIfNot0(serviceList(cmd.Context(), asJSON, os.Stdout, os.Stderr))
	},
}

var orbServiceLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "显示服务日志末尾（-f：持续输出新内容）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		n, _ := cmd.Flags().GetInt("lines")
		follow, _ := cmd.Flags().GetBool("follow")
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return serviceLogs(ctx, args[0], n, follow, os.Stdout)
	},
}

func exitIfNot0(code int) {
	if code != 0 {
		os.Exit(code)
	}
}

type serviceStartOpts struct {
	name, command, cwd, health string
	hasCommand, portal         bool
	port                       int
	env                        []string
}

func serviceStart(ctx context.Context, o serviceStartOpts, stdout, stderr io.Writer) int {
	onLog := func(_, data string) { _, _ = io.WriteString(stderr, data) }
	if o.name == "" {
		if o.hasCommand || o.port != 0 || o.cwd != "" || len(o.env) > 0 || o.health != "" || o.portal {
			fmt.Fprintln(stderr, "oryn: --command / --port / --cwd / --env / --health / --portal 需要同时给出服务名")
			return exitServiceUsage
		}
		var res orbsock.ServiceListResult
		if err := sockCall(ctx, orbsock.MethodServiceEnsure, nil, &res, onLog); err != nil {
			fmt.Fprintln(stderr, "oryn:", err)
			return exitServiceUsage
		}
		if res.FileError != "" {
			fmt.Fprintln(stderr, "oryn: services.yaml:", res.FileError)
		}
		if len(res.Services) == 0 {
			fmt.Fprintln(stderr, "oryn: 没有受管服务（.oryn/services.yaml 不存在或为空）")
		}
		code := exitServiceOK
		for _, s := range res.Services {
			if s.Status == apigen.OrbServiceStatusStopped {
				// 用户停掉的服务 ensure 不会拉起，不算失败。
				printService(s, stdout, stderr)
				continue
			}
			if printService(s, stdout, stderr) != exitServiceOK {
				code = exitServiceNotRunning
			}
		}
		return code
	}
	p := orbsock.ServiceStartParams{
		Name: o.name, Cwd: o.cwd, Port: o.port, Health: o.health, Portal: o.portal,
		ThreadID: os.Getenv("ORYN_THREAD_ID"),
	}
	if o.hasCommand {
		p.Command = &o.command
	}
	if len(o.env) > 0 {
		p.Env = map[string]string{}
		for _, kv := range o.env {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				fmt.Fprintf(stderr, "oryn: --env 需要 KEY=VALUE，得到 %q\n", kv)
				return exitServiceUsage
			}
			p.Env[k] = v
		}
	}
	var res orbsock.ServiceResult
	if err := sockCall(ctx, orbsock.MethodServiceStart, p, &res, onLog); err != nil {
		fmt.Fprintln(stderr, "oryn:", err)
		return exitServiceUsage
	}
	return printService(res.Service, stdout, stderr)
}

func serviceStop(ctx context.Context, name string, remove bool, stdout, stderr io.Writer) int {
	var res orbsock.ServiceResult
	if err := sockCall(ctx, orbsock.MethodServiceStop, orbsock.ServiceStopParams{Name: name, Remove: remove}, &res, nil); err != nil {
		fmt.Fprintln(stderr, "oryn:", err)
		return exitServiceUsage
	}
	printService(res.Service, stdout, stderr)
	if remove {
		fmt.Fprintf(stderr, "已删除临时服务 %s 的定义\n", name)
	}
	return exitServiceOK
}

func serviceRestart(ctx context.Context, name string, stdout, stderr io.Writer) int {
	var res orbsock.ServiceResult
	onLog := func(_, data string) { _, _ = io.WriteString(stderr, data) }
	if err := sockCall(ctx, orbsock.MethodServiceRestart, orbsock.ServiceNameParams{Name: name}, &res, onLog); err != nil {
		fmt.Fprintln(stderr, "oryn:", err)
		return exitServiceUsage
	}
	return printService(res.Service, stdout, stderr)
}

// printService 按输出约定打印一个服务，返回它对应的退出码（running → 0，其余 → 1）。
func printService(s apigen.OrbService, stdout, stderr io.Writer) int {
	line := fmt.Sprintf("%s: %s, port %s", s.Name, s.Status, portText(s.Port))
	if s.Error != nil && *s.Error != "" {
		line += " (" + *s.Error + ")"
	}
	fmt.Fprintln(stderr, line)
	active := s.Status != apigen.OrbServiceStatusStopped && s.Status != apigen.OrbServiceStatusFailed
	switch {
	case !active:
	case s.PublicURL != nil && *s.PublicURL != "":
		fmt.Fprintln(stdout, *s.PublicURL+"/")
	case len(s.Links) > 0:
		fmt.Fprintf(stderr, "%s: portal 未启用（控制面未配置 portal_domain 或服务没有所属线程），orb 内可经 http://127.0.0.1:%s 访问\n", s.Name, portText(s.Port))
	}
	if s.Status != apigen.OrbServiceStatusRunning {
		return exitServiceNotRunning
	}
	return exitServiceOK
}

func portText(p *int) string {
	if p == nil {
		return "-"
	}
	return strconv.Itoa(*p)
}

func serviceList(ctx context.Context, asJSON bool, stdout, stderr io.Writer) int {
	var res orbsock.ServiceListResult
	if err := sockCall(ctx, orbsock.MethodServiceList, nil, &res, nil); err != nil {
		fmt.Fprintln(stderr, "oryn:", err)
		return exitServiceUsage
	}
	if res.Services == nil {
		res.Services = []apigen.OrbService{}
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return exitServiceOK
	}
	if res.FileError != "" {
		fmt.Fprintln(stderr, "oryn: services.yaml:", res.FileError)
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tPORT\tRESTARTS\tSOURCE\tURL")
	for _, s := range res.Services {
		url := "-"
		if s.PublicURL != nil && *s.PublicURL != "" {
			url = *s.PublicURL + "/"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", s.Name, s.Status, portText(s.Port), s.Restarts, s.Source, url)
	}
	_ = tw.Flush()
	return exitServiceOK
}

// serviceLogs 打印服务日志末尾 n 行；follow 时每 serviceLogPoll 读一次新增内容直到 ctx 结束
// （日志轮转后从新文件开头继续）。
func serviceLogs(ctx context.Context, name string, n int, follow bool, stdout io.Writer) error {
	if os.Getenv("ORYN_ORB") != "1" {
		return errNotInOrb
	}
	if !serviceNameRe.MatchString(name) {
		return fmt.Errorf("无效的服务名 %q", name)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".cache", "oryn", "logs", "services", name+".log")
	// 末尾 n 行与之后的跟随读同一个文件描述符：跟随从这次读到的位置接着读，文件身份也以它为准。
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) && !follow {
		return fmt.Errorf("还没有服务 %s 的日志", name)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var b []byte
	if f != nil {
		defer func() { _ = f.Close() }()
		if b, err = io.ReadAll(f); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(stdout, tailLines(string(b), n)); err != nil {
		return err
	}
	if !follow {
		return nil
	}
	t := time.NewTicker(serviceLogPoll)
	defer t.Stop()
	return followLog(ctx, path, f, t.C, stdout)
}

// followLog 每收到一次 tick 就把 f 上的新增内容写到 stdout，直到 ctx 结束；f 为 nil 表示文件尚不存在。
// 轮转（path 已指向另一个文件，以 os.SameFile 判定）时先读完旧文件剩余内容，再从新文件开头读；
// 同一文件被截短时从头读。新文件的身份取自打开后的描述符，stat 与 open 之间再次轮转也不会错位。
// 返回时关闭跟随过程中打开的文件（不关闭传入的 f）。
func followLog(ctx context.Context, path string, f *os.File, tick <-chan time.Time, stdout io.Writer) error {
	cur := f
	defer func() {
		if cur != nil && cur != f {
			_ = cur.Close()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick:
		}
		if cur != nil {
			if _, err := io.Copy(stdout, cur); err != nil {
				return err
			}
		}
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		if cur != nil {
			cst, err := cur.Stat()
			if err == nil && os.SameFile(cst, st) {
				if pos, err := cur.Seek(0, io.SeekCurrent); err == nil && cst.Size() < pos {
					// 同一文件被截短：从头读
					if _, err := cur.Seek(0, io.SeekStart); err == nil {
						if _, err := io.Copy(stdout, cur); err != nil {
							return err
						}
					}
				}
				continue
			}
		}
		nf, err := os.Open(path)
		if err != nil {
			continue
		}
		if cur != nil {
			// 轮转之前写进旧文件、本轮开头还没读到的内容
			if _, err := io.Copy(stdout, cur); err != nil {
				_ = nf.Close()
				return err
			}
			if cur != f {
				_ = cur.Close()
			}
		}
		cur = nf
		if _, err := io.Copy(stdout, cur); err != nil {
			return err
		}
	}
}

func init() {
	f := orbServiceStartCmd.Flags()
	f.String("command", "", "临时服务的命令（以 bash -lc 运行，须监听 $PORT）")
	f.Int("port", 0, "固定端口（省略则自动分配，跨重启不变）")
	f.String("cwd", "", "相对工作区根的工作目录")
	f.StringArray("env", nil, "环境变量 KEY=VALUE（可重复）")
	f.String("health", "", "就绪探测的 HTTP 路径（如 /healthz）")
	f.Bool("portal", false, "为该服务开 portal（打印可从浏览器访问的地址）")
	orbServiceStopCmd.Flags().Bool("remove", false, "同时删除临时服务的定义与端口分配")
	orbServiceLsCmd.Flags().Bool("json", false, "输出 JSON")
	orbServiceLogsCmd.Flags().IntP("lines", "n", 200, "显示末尾 N 行（0 = 全部）")
	orbServiceLogsCmd.Flags().BoolP("follow", "f", false, "持续输出新内容直到中断")
	orbServiceCmd.AddCommand(orbServiceStartCmd, orbServiceStopCmd, orbServiceRestartCmd, orbServiceLsCmd, orbServiceLogsCmd)
}
