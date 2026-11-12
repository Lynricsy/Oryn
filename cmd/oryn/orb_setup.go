package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// `oryn orb setup …`：orb 内经 orbd 本地控制口（$HOME/.oryn/orbd.sock）重跑与查看 setup / resume、
// 读写本 orb 所属项目的项目脚本（M2.5）。只在 orb 内（ORYN_ORB=1）可用。

// setupRun 的退出码。
const (
	exitSetupOK     = 0
	exitSetupFailed = 1
	exitSetupUsage  = 2
)

var orbSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "orb 内：重跑与查看环境 setup / resume、读写项目脚本",
}

var orbSetupRunCmd = &cobra.Command{
	Use:   "run",
	Short: "重跑 setup（--resume：重跑 resume）并实时输出；退出码 0 成功 / 1 失败 / 2 调用错误",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, _ []string) {
		resume, _ := cmd.Flags().GetBool("resume")
		if code := setupRun(cmd.Context(), resume, os.Stdout, os.Stderr); code != exitSetupOK {
			os.Exit(code)
		}
	},
}

var orbSetupStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "显示最近一次 setup / resume 的结果",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		asJSON, _ := cmd.Flags().GetBool("json")
		return setupStatus(cmd.Context(), asJSON, os.Stdout)
	},
}

var orbSetupLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "显示 setup 日志末尾（--resume：resume 日志）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		resume, _ := cmd.Flags().GetBool("resume")
		n, _ := cmd.Flags().GetInt("lines")
		return setupLogs(resume, n, os.Stdout)
	},
}

var orbSetupProjectScriptCmd = &cobra.Command{
	Use:   "project-script",
	Short: "读写本 orb 所属项目的 setup / resume 项目脚本",
}

var orbSetupProjectScriptGetCmd = &cobra.Command{
	Use:   "get [setup|resume]",
	Short: "打印项目脚本（给出阶段时只打印该脚本的原文）",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		phase := ""
		if len(args) == 1 {
			phase = args[0]
		}
		return projectScriptGet(cmd.Context(), phase, os.Stdout, os.Stderr)
	},
}

var orbSetupProjectScriptSetCmd = &cobra.Command{
	Use:   "set setup|resume --file <path>|-",
	Short: "从文件（- 为标准输入）设置项目脚本；空内容表示清除",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		return projectScriptSet(cmd.Context(), args[0], file, os.Stdin, os.Stdout)
	},
}

// orbSocket 返回 orbd 控制口路径；不在 orb 内时给出明确的错误。
func orbSocket() (string, error) {
	if os.Getenv("ORYN_ORB") != "1" {
		return "", errors.New("`oryn orb setup` 只能在 Oryn orb 内使用（ORYN_ORB 不是 1）")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := orbsock.Path(home)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("找不到 orbd 控制口 %s（orbd 未运行？）", path)
	}
	return path, nil
}

// sockCall 调用 orbd 控制口并把 result 解码到 out。
func sockCall(ctx context.Context, method string, params, out any, onLog func(stream, data string)) error {
	path, err := orbSocket()
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := orbsock.Call(ctx, path, method, params, onLog)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func setupRun(ctx context.Context, resume bool, stdout, stderr io.Writer) int {
	var res apigen.LinkCtlSetupResultParams
	err := sockCall(ctx, orbsock.MethodSetupRerun, orbsock.RerunParams{Resume: resume}, &res, func(stream, data string) {
		if stream == orbsock.StreamStdout {
			_, _ = io.WriteString(stdout, data)
		} else {
			_, _ = io.WriteString(stderr, data)
		}
	})
	if err != nil {
		fmt.Fprintln(stderr, "oryn:", err)
		return exitSetupUsage
	}
	fmt.Fprintln(stderr, "oryn:", describeResult(&res))
	if !res.Ok {
		return exitSetupFailed
	}
	return exitSetupOK
}

func describeResult(r *apigen.LinkCtlSetupResultParams) string {
	d := (time.Duration(r.DurationMs) * time.Millisecond).Round(100 * time.Millisecond)
	var b strings.Builder
	fmt.Fprintf(&b, "%s", r.Phase)
	switch {
	case !r.Ok:
		msg := "unknown error"
		if r.Error != nil {
			msg = *r.Error
		}
		fmt.Fprintf(&b, " 失败（%s，%s）", d, msg)
	case r.Phase == apigen.SetupPhaseSetup && !r.SetupRan:
		b.WriteString(" 已完成过，本次跳过")
	default:
		fmt.Fprintf(&b, " 成功（%s）", d)
	}
	var scripts []string
	if r.HasProjectScript {
		scripts = append(scripts, "项目脚本")
	}
	if r.HasRepoScript {
		scripts = append(scripts, "仓库脚本")
	}
	if len(scripts) == 0 {
		b.WriteString("；没有可执行的脚本")
	} else {
		b.WriteString("；执行了 " + strings.Join(scripts, " + "))
	}
	fmt.Fprintf(&b, "；触发 %s，开始于 %s", r.Trigger, time.UnixMilli(r.StartedAt).Local().Format(time.DateTime))
	return b.String()
}

func setupStatus(ctx context.Context, asJSON bool, stdout io.Writer) error {
	var st orbsock.Status
	if err := sockCall(ctx, orbsock.MethodSetupStatus, nil, &st, nil); err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	for _, r := range []*apigen.LinkCtlSetupResultParams{st.Setup, st.Resume} {
		if r != nil {
			fmt.Fprintln(stdout, describeResult(r))
		}
	}
	if st.Setup == nil && st.Resume == nil {
		fmt.Fprintln(stdout, "还没有 setup / resume 运行记录")
	}
	return nil
}

func setupLogs(resume bool, n int, stdout io.Writer) error {
	if os.Getenv("ORYN_ORB") != "1" {
		return errors.New("`oryn orb setup` 只能在 Oryn orb 内使用（ORYN_ORB 不是 1）")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	name := "setup.log"
	if resume {
		name = "resume.log"
	}
	b, err := os.ReadFile(filepath.Join(home, ".cache", "oryn", "logs", name))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("还没有 %s", name)
	}
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, tailLines(string(b), n))
	return err
}

// tailLines 返回 s 的最后 n 行（n ≤ 0 时全部）。
func tailLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	end := len(s)
	if strings.HasSuffix(s, "\n") {
		end--
	}
	for i := end - 1; i >= 0; i-- {
		if s[i] == '\n' {
			n--
			if n == 0 {
				return s[i+1:]
			}
		}
	}
	return s
}

func checkPhase(p string) error {
	if p != "setup" && p != "resume" {
		return fmt.Errorf("阶段必须是 setup 或 resume，得到 %q", p)
	}
	return nil
}

func projectScriptGet(ctx context.Context, phase string, stdout, stderr io.Writer) error {
	if phase != "" {
		if err := checkPhase(phase); err != nil {
			return err
		}
	}
	var ps apigen.LinkCtlProjectScripts
	if err := sockCall(ctx, orbsock.MethodProjectScriptsGet, nil, &ps, nil); err != nil {
		return err
	}
	pick := map[string]*string{"setup": ps.Setup, "resume": ps.Resume}
	if phase != "" {
		if s := pick[phase]; s != nil && *s != "" {
			_, err := io.WriteString(stdout, *s)
			return err
		}
		fmt.Fprintf(stderr, "项目 %s 没有 %s 项目脚本\n", ps.ProjectName, phase)
		return nil
	}
	fmt.Fprintf(stdout, "# 项目 %s（%s）\n", ps.ProjectName, ps.ProjectID)
	for _, name := range []string{"setup", "resume"} {
		if s := pick[name]; s != nil && *s != "" {
			fmt.Fprintf(stdout, "## %s\n%s", name, *s)
			if !strings.HasSuffix(*s, "\n") {
				fmt.Fprintln(stdout)
			}
		} else {
			fmt.Fprintf(stdout, "## %s\n（未设置）\n", name)
		}
	}
	return nil
}

func projectScriptSet(ctx context.Context, phase, file string, stdin io.Reader, stdout io.Writer) error {
	if err := checkPhase(phase); err != nil {
		return err
	}
	var content []byte
	var err error
	switch file {
	case "":
		return errors.New("需要 --file <path>（- 表示标准输入）")
	case "-":
		content, err = io.ReadAll(stdin)
	default:
		content, err = os.ReadFile(file)
	}
	if err != nil {
		return err
	}
	s := string(content)
	var p apigen.LinkCtlProjectScriptsSetParams
	if phase == "setup" {
		p.Setup = &s
	} else {
		p.Resume = &s
	}
	var ps apigen.LinkCtlProjectScripts
	if err := sockCall(ctx, orbsock.MethodProjectScriptsSet, p, &ps, nil); err != nil {
		return err
	}
	if strings.TrimSpace(s) == "" {
		fmt.Fprintf(stdout, "已清除项目 %s 的 %s 项目脚本\n", ps.ProjectName, phase)
	} else {
		fmt.Fprintf(stdout, "已保存项目 %s 的 %s 项目脚本（%d 字节）；对之后的 setup 与新 orb 生效\n", ps.ProjectName, phase, len(s))
	}
	return nil
}

func init() {
	orbSetupRunCmd.Flags().Bool("resume", false, "重跑 resume 而不是 setup")
	orbSetupStatusCmd.Flags().Bool("json", false, "输出 JSON")
	orbSetupLogsCmd.Flags().Bool("resume", false, "查看 resume 日志")
	orbSetupLogsCmd.Flags().IntP("lines", "n", 200, "显示末尾 N 行（0 = 全部）")
	orbSetupProjectScriptSetCmd.Flags().String("file", "", "脚本文件路径，- 表示标准输入")
	orbSetupProjectScriptCmd.AddCommand(orbSetupProjectScriptGetCmd, orbSetupProjectScriptSetCmd)
	orbSetupCmd.AddCommand(orbSetupRunCmd, orbSetupStatusCmd, orbSetupLogsCmd, orbSetupProjectScriptCmd)
}
