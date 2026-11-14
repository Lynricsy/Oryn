package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// `oryn stats …`：统计分析（M10）。全部走只读 REST（预算的 set / off 除外）；orb 内（无 API 凭据）不可用。

// statsEnv 是 stats 命令的外部依赖，测试时替换。
type statsEnv struct {
	client func() (*apiClient, error)
	now    func() time.Time
	// localtime 返回 /etc/localtime 的链接目标（时区回落的第三步）。
	localtime func() (string, error)
	// isTTY 报告 w 是否是终端（决定 --csv 是否去掉控制字符）；nil = 都不是。
	isTTY func(w io.Writer) bool
}

func defaultStatsEnv() statsEnv {
	return statsEnv{
		client:    newClient,
		now:       time.Now,
		localtime: func() (string, error) { return os.Readlink("/etc/localtime") },
		isTTY: func(w io.Writer) bool {
			f, ok := w.(*os.File)
			return ok && isTerminal(f)
		},
	}
}

var (
	usageByValues  = []string{"total", "hour", "day", "week", "month", "model", "thread", "project", "space", "purpose"}
	grainValues    = []string{"hour", "day", "week", "month"}
	orbByValues    = []string{"type", "day", "week", "month", "project", "orb"}
	outputByValues = []string{"day", "week", "month", "project", "space"}
)

// newStatsCmd 构造 `oryn stats` 命令树；`oryn stats` 本身等同 `oryn stats usage`。
func newStatsCmd(env statsEnv) *cobra.Command {
	cmd := newStatsUsageCmd(env, "stats", "统计分析：用量、工具、orb、产出、预算（不带子命令时同 `stats usage`）")
	cmd.PersistentPreRunE = func(*cobra.Command, []string) error {
		if inOrb() {
			return errUnavailableInOrb("stats")
		}
		return nil
	}
	cmd.AddCommand(
		newStatsUsageCmd(env, "usage", "按模型 / 时间 / 线程 / 项目 / Space / 用途汇总 token、成本与请求"),
		newStatsToolsCmd(env),
		newStatsOrbsCmd(env),
		newStatsOutputCmd(env),
		newStatsThreadCmd(env),
		newStatsBudgetCmd(env),
	)
	return cmd
}

func init() { root.AddCommand(newStatsCmd(defaultStatsEnv())) }

// ---- 公共参数 ----

// statsWindowOpts 是时间窗参数。
type statsWindowOpts struct {
	since, from, to, tz string
}

func addStatsWindowFlags(cmd *cobra.Command, o *statsWindowOpts) {
	fl := cmd.Flags()
	fl.StringVar(&o.since, "since", "7d", "窗口长度（距今）：Go 时长（如 36h）或 Nd / Nw")
	fl.StringVar(&o.from, "from", "", "起始本地日期 YYYY-MM-DD（含；与 --since 互斥）")
	fl.StringVar(&o.to, "to", "", "结束本地日期 YYYY-MM-DD（含；缺省到现在）")
	addStatsTZFlag(cmd, &o.tz)
}

func addStatsTZFlag(cmd *cobra.Command, tz *string) {
	cmd.Flags().StringVar(tz, "tz", "", "IANA 时区（缺省取环境变量 TZ，再取 /etc/localtime，都没有则 UTC）")
}

// statsWindow 是解析后的窗口：[from, to) 毫秒与时区名。
type statsWindow struct {
	from, to int64
	tz       string
}

func (w statsWindow) query() url.Values {
	q := url.Values{}
	q.Set("from", strconv.FormatInt(w.from, 10))
	q.Set("to", strconv.FormatInt(w.to, 10))
	q.Set("tz", w.tz)
	return q
}

// window 解析时间窗；--since 与 --from/--to 互斥，日期按所选时区的本地零点换算，--to 含当日（换成次日零点）。
func (e statsEnv) window(cmd *cobra.Command, o statsWindowOpts) (statsWindow, error) {
	if cmd.Flags().Changed("since") && (o.from != "" || o.to != "") {
		return statsWindow{}, usageErr("--since 与 --from / --to 不能同时使用")
	}
	if o.to != "" && o.from == "" {
		return statsWindow{}, usageErr("--to 需要同时给出 --from")
	}
	var since time.Duration
	if o.from == "" {
		d, err := parseSince(o.since)
		if err != nil {
			return statsWindow{}, err
		}
		since = d
	}
	tz, loc, err := resolveStatsTZ(o.tz, os.Getenv("TZ"), e.localtime, cmd.ErrOrStderr())
	if err != nil {
		return statsWindow{}, err
	}
	now := e.now()
	if o.from == "" {
		return statsWindow{from: now.Add(-since).UnixMilli(), to: now.UnixMilli(), tz: tz}, nil
	}
	from, err := time.ParseInLocation(time.DateOnly, o.from, loc)
	if err != nil {
		return statsWindow{}, usageErr("--from %q 不是 YYYY-MM-DD 日期", o.from)
	}
	to := now
	if o.to != "" {
		d, err := time.ParseInLocation(time.DateOnly, o.to, loc)
		if err != nil {
			return statsWindow{}, usageErr("--to %q 不是 YYYY-MM-DD 日期", o.to)
		}
		to = d.AddDate(0, 0, 1)
	}
	if !from.Before(to) {
		return statsWindow{}, usageErr("--from 必须早于 --to")
	}
	return statsWindow{from: from.UnixMilli(), to: to.UnixMilli(), tz: tz}, nil
}

// parseSince 解析 Go 时长，另认 Nd（天）与 Nw（周）。
func parseSince(s string) (time.Duration, error) {
	if n := len(s); n > 1 && (s[n-1] == 'd' || s[n-1] == 'w') {
		if v, err := strconv.Atoi(s[:n-1]); err == nil && v > 0 {
			unit := 24 * time.Hour
			if s[n-1] == 'w' {
				unit *= 7
			}
			return time.Duration(v) * unit, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, usageErr("--since %q 无效：用 Go 时长（如 36h）或 Nd / Nw（如 7d、2w）", s)
	}
	return d, nil
}

// resolveStatsTZ 取统计时区：--tz → 环境变量 TZ → /etc/localtime 链接目标里 `zoneinfo/` 之后的部分 → UTC（并在 stderr 提示）。
func resolveStatsTZ(flag, envTZ string, localtime func() (string, error), stderr io.Writer) (string, *time.Location, error) {
	if flag != "" {
		loc, err := time.LoadLocation(flag)
		if err != nil {
			return "", nil, usageErr("--tz %q 不是可用的 IANA 时区", flag)
		}
		return flag, loc, nil
	}
	// TZ 以 ":" 或 "/" 开头是文件路径形式，不是 IANA 名
	if envTZ != "" && !strings.HasPrefix(envTZ, ":") && !strings.HasPrefix(envTZ, "/") {
		if loc, err := time.LoadLocation(envTZ); err == nil {
			return envTZ, loc, nil
		}
	}
	if localtime != nil {
		if target, err := localtime(); err == nil {
			if i := strings.LastIndex(target, "zoneinfo/"); i >= 0 {
				if name := target[i+len("zoneinfo/"):]; name != "" {
					if loc, err := time.LoadLocation(name); err == nil {
						return name, loc, nil
					}
				}
			}
		}
	}
	fmt.Fprintln(stderr, "oryn: 未给 --tz，TZ 为空且无法从 /etc/localtime 判定时区，按 UTC 统计")
	return "UTC", time.UTC, nil
}

// statsFilterOpts 是筛选参数；各子命令只登记适用的项。
type statsFilterOpts struct {
	project, space, thread, model string
	limit                         int
}

// apply 把筛选写进查询串；项目 / Space 名称经列表接口解析，`none` 原样传给服务端。
func (o statsFilterOpts) apply(ctx context.Context, c *apiClient, q url.Values) error {
	if o.project != "" {
		id := o.project
		if id != "none" && !strings.HasPrefix(id, "prj_") {
			var err error
			if id, err = resolveProject(ctx, c, o.project); err != nil {
				return err
			}
		}
		q.Set("project", id)
	}
	if o.space != "" {
		id := o.space
		if id != "none" {
			var err error
			if id, err = resolveSpace(ctx, c, o.space); err != nil {
				return err
			}
		}
		q.Set("space", id)
	}
	if o.thread != "" {
		q.Set("thread", o.thread)
	}
	if o.model != "" {
		q.Set("model", o.model)
	}
	if o.limit > 0 {
		q.Set("limit", strconv.Itoa(o.limit))
	}
	return nil
}

// statsOutOpts 是输出方式：表格（缺省）、--json（服务端 JSON 原样）、--csv（服务端 CSV 原样）。
type statsOutOpts struct{ json, csv bool }

func addStatsOutFlags(cmd *cobra.Command, o *statsOutOpts, csv bool) {
	cmd.Flags().BoolVar(&o.json, "json", false, "原样打印服务端 JSON")
	if csv {
		cmd.Flags().BoolVar(&o.csv, "csv", false, "以 CSV 输出（服务端 format=csv 的响应）")
	}
}

func (o statsOutOpts) check() error {
	if o.json && o.csv {
		return usageErr("--json 与 --csv 不能同时使用")
	}
	return nil
}

func checkEnum(flag, v string, allowed []string) error {
	if !slices.Contains(allowed, v) {
		return usageErr("--%s 只能是 %s", flag, strings.Join(allowed, " | "))
	}
	return nil
}

// statsFetch 取统计接口：--json / --csv 时把响应体写到 stdout 并返回 true；否则把 JSON 解码到 out。
// --json 原样；--csv 写到文件 / 管道时原样，写到终端时去掉控制字符（只保留换行），防止服务端文本里的转义序列被终端执行。
func statsFetch(cmd *cobra.Command, env statsEnv, c *apiClient, path string, q url.Values, o statsOutOpts, out any) (bool, error) {
	if o.csv {
		q.Set("format", "csv")
	}
	full := path + "?" + q.Encode()
	if !o.json && !o.csv {
		return false, c.do(cmd.Context(), http.MethodGet, full, nil, out)
	}
	rc, err := c.getRaw(cmd.Context(), full)
	if err != nil {
		return true, err
	}
	defer rc.Close()
	w := cmd.OutOrStdout()
	if o.csv && env.isTTY != nil && env.isTTY(w) {
		return true, copyTermSafe(w, rc)
	}
	_, err = io.Copy(w, rc)
	return true, err
}

// isTermControl 与 threads.cleanTitle 同规则：控制字符（C0 / C1）与行 / 段分隔符。
func isTermControl(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }

// termText 是写到终端表格的服务端文本：控制字符换成空格并合并空白（同 threads.cleanTitle，不截断）。
func termText(s string) string {
	s = strings.Map(func(r rune) rune {
		if isTermControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// copyTermSafe 把 r 复制到终端 w：换行保留，其余控制字符换成空格；非法 UTF-8 字节由 ReadRune 读成 U+FFFD。
func copyTermSafe(w io.Writer, r io.Reader) error {
	br := bufio.NewReader(r)
	bw := bufio.NewWriter(w)
	for {
		c, _, err := br.ReadRune()
		if err == io.EOF {
			return bw.Flush()
		}
		if err != nil {
			_ = bw.Flush()
			return err
		}
		if c != '\n' && isTermControl(c) {
			c = ' '
		}
		if _, err := bw.WriteRune(c); err != nil {
			return err
		}
	}
}

// getJSON 取一个 JSON 响应的原始字节。
func getJSON(ctx context.Context, c *apiClient, path string) (json.RawMessage, error) {
	rc, err := c.getRaw(ctx, path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// ---- usage ----

type statsUsageOpts struct {
	by, series string
	win        statsWindowOpts
	filter     statsFilterOpts
	out        statsOutOpts
}

func newStatsUsageCmd(env statsEnv, use, short string) *cobra.Command {
	var o statsUsageOpts
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStatsUsage(cmd, env, o)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&o.by, "by", "model", "分组："+strings.Join(usageByValues, " | "))
	fl.StringVar(&o.series, "series", "", "附带时间序列的粒度："+strings.Join(grainValues, " | "))
	addStatsWindowFlags(cmd, &o.win)
	fl.StringVar(&o.filter.project, "project", "", "只统计该项目（名称 / id / none）")
	fl.StringVar(&o.filter.space, "space", "", "只统计该 Space（名称 / id / none）")
	fl.StringVar(&o.filter.thread, "thread", "", "只统计该线程")
	fl.StringVar(&o.filter.model, "model", "", "只统计该模型（<provider>/<model>）")
	fl.IntVar(&o.filter.limit, "limit", 0, "非时间分组最多列出几组（1–500；缺省 50，其余并入 __other__）")
	addStatsOutFlags(cmd, &o.out, true)
	return cmd
}

func runStatsUsage(cmd *cobra.Command, env statsEnv, o statsUsageOpts) error {
	if err := o.out.check(); err != nil {
		return err
	}
	if err := checkEnum("by", o.by, usageByValues); err != nil {
		return err
	}
	if o.series != "" {
		if err := checkEnum("series", o.series, grainValues); err != nil {
			return err
		}
		if slices.Contains(grainValues, o.by) {
			return usageErr("--by 是时间粒度时不能再给 --series")
		}
	}
	w, err := env.window(cmd, o.win)
	if err != nil {
		return err
	}
	c, err := env.client()
	if err != nil {
		return err
	}
	q := w.query()
	q.Set("groupBy", o.by)
	if o.series != "" {
		q.Set("series", o.series)
	}
	if err := o.filter.apply(cmd.Context(), c, q); err != nil {
		return err
	}
	var st apigen.UsageStats
	if done, err := statsFetch(cmd, env, c, "/api/v1/stats/usage", q, o.out, &st); done || err != nil {
		return err
	}
	tw := newStatsTable(cmd.OutOrStdout())
	writeUsageRows(tw, o.by, st.Groups, &st.Total)
	if len(st.Series) > 0 {
		fmt.Fprintln(tw)
		fmt.Fprintln(tw, "BUCKET\tREQ\tERR%\tIN\tOUT\tCACHE-R\tCACHE-W\tCOST\tTTFT\tTOK/S\tTOOLS\tTURNS")
		for _, p := range st.Series {
			writeUsageRow(tw, p.Key, p.Total)
		}
	}
	return tw.Flush()
}

func newStatsTable(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0) }

// writeUsageRows 打印用量表头、各组与（total 非 nil 时）末行 TOTAL。
func writeUsageRows(tw io.Writer, by string, groups []apigen.UsageStat, total *apigen.UsageStat) {
	fmt.Fprintln(tw, "KEY\tREQ\tERR%\tIN\tOUT\tCACHE-R\tCACHE-W\tCOST\tTTFT\tTOK/S\tTOOLS\tTURNS")
	for _, g := range groups {
		writeUsageRow(tw, usageKey(by, g), g)
	}
	if total != nil {
		writeUsageRow(tw, "TOTAL", *total)
	}
}

func writeUsageRow(tw io.Writer, key string, s apigen.UsageStat) {
	fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n", termText(key), s.Requests,
		fmtPct(float64(s.RequestErrors), float64(s.Requests)),
		fmtTokens(s.InputTokens), fmtTokens(s.OutputTokens), fmtTokens(s.CacheReadTokens), fmtTokens(s.CacheWriteTokens),
		fmtUSD(s.CostUsd), fmtTTFT(s.AvgTtftMs), fmtRate(s.TokensPerSecond), s.ToolCalls, s.Turns)
}

// usageKey 给分组一个可读的键：有显示名用显示名，空键按分组维度说明含义。
func usageKey(by string, s apigen.UsageStat) string {
	if s.Key == "__other__" {
		return "(other)"
	}
	if s.Label != nil && *s.Label != "" {
		return oneLine(*s.Label, 40)
	}
	if s.Key == "" {
		return emptyKeyName(by)
	}
	return s.Key
}

func emptyKeyName(by string) string {
	switch by {
	case "thread":
		return "(deleted)"
	case "project":
		return "(no project)"
	case "space":
		return "(no space)"
	case "purpose":
		return "(turns)"
	}
	return "-"
}

// ---- tools ----

type statsToolsOpts struct {
	win    statsWindowOpts
	filter statsFilterOpts
	out    statsOutOpts
}

func newStatsToolsCmd(env statsEnv) *cobra.Command {
	var o statsToolsOpts
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "工具调用次数与错误率",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.out.check(); err != nil {
				return err
			}
			w, err := env.window(cmd, o.win)
			if err != nil {
				return err
			}
			c, err := env.client()
			if err != nil {
				return err
			}
			q := w.query()
			if err := o.filter.apply(cmd.Context(), c, q); err != nil {
				return err
			}
			var st apigen.ToolStats
			if done, err := statsFetch(cmd, env, c, "/api/v1/stats/tools", q, o.out, &st); done || err != nil {
				return err
			}
			tw := newStatsTable(cmd.OutOrStdout())
			writeToolRows(tw, st.Tools, &st.Total)
			return tw.Flush()
		},
	}
	addStatsWindowFlags(cmd, &o.win)
	fl := cmd.Flags()
	fl.StringVar(&o.filter.project, "project", "", "只统计该项目（名称 / id / none）")
	fl.StringVar(&o.filter.space, "space", "", "只统计该 Space（名称 / id / none）")
	fl.StringVar(&o.filter.thread, "thread", "", "只统计该线程")
	fl.IntVar(&o.filter.limit, "limit", 0, "最多列出几个工具（1–500；缺省 50）")
	addStatsOutFlags(cmd, &o.out, true)
	return cmd
}

func writeToolRows(tw io.Writer, tools []apigen.ToolStat, total *apigen.ToolStat) {
	fmt.Fprintln(tw, "TOOL\tCALLS\tERRORS\tERR%")
	row := func(name string, s apigen.ToolStat) {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\n", termText(name), s.Calls, s.Errors, fmtPct(float64(s.Errors), float64(s.Calls)))
	}
	for _, s := range tools {
		name := s.Tool
		if name == "__other__" {
			name = "(other)"
		}
		row(name, s)
	}
	if total != nil {
		row("TOTAL", *total)
	}
}

// ---- orbs ----

type statsOrbsOpts struct {
	by, orb, project string
	win              statsWindowOpts
	out              statsOutOpts
}

func newStatsOrbsCmd(env statsEnv) *cobra.Command {
	var o statsOrbsOpts
	cmd := &cobra.Command{
		Use:   "orbs",
		Short: "orb 运行 / 冻结 / 停机时长与 setup 汇总",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.out.check(); err != nil {
				return err
			}
			if err := checkEnum("by", o.by, orbByValues); err != nil {
				return err
			}
			w, err := env.window(cmd, o.win)
			if err != nil {
				return err
			}
			c, err := env.client()
			if err != nil {
				return err
			}
			q := w.query()
			q.Set("groupBy", o.by)
			if o.orb != "" {
				q.Set("orb", o.orb)
			}
			if err := (statsFilterOpts{project: o.project}).apply(cmd.Context(), c, q); err != nil {
				return err
			}
			var st apigen.OrbStats
			if done, err := statsFetch(cmd, env, c, "/api/v1/stats/orbs", q, o.out, &st); done || err != nil {
				return err
			}
			tw := newStatsTable(cmd.OutOrStdout())
			fmt.Fprintln(tw, "KEY\tORBS\tRUNNING\tFROZEN\tFROZEN%\tSTOPPED\tSPAWNS\tWAKES\tFAILS")
			row := func(key string, s apigen.OrbStat) {
				fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%d\t%d\t%d\n", termText(key), s.Orbs, fmtDur(s.RunningMs), fmtDur(s.FrozenMs),
					fmtPct(float64(s.FrozenMs), float64(s.RunningMs+s.FrozenMs)), fmtDur(s.StoppedMs), s.Spawns, s.Wakes, s.Failures)
			}
			for _, g := range st.Groups {
				row(orbKey(o.by, g), g)
			}
			row("TOTAL", st.Total)
			if err := tw.Flush(); err != nil {
				return err
			}
			return writeSetupSummary(cmd.OutOrStdout(), st)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&o.by, "by", "type", "分组："+strings.Join(orbByValues, " | "))
	fl.StringVar(&o.orb, "orb", "", "只统计该 orb")
	fl.StringVar(&o.project, "project", "", "只统计该项目（名称 / id / none）")
	addStatsWindowFlags(cmd, &o.win)
	addStatsOutFlags(cmd, &o.out, true)
	return cmd
}

func orbKey(by string, s apigen.OrbStat) string {
	if by != "orb" && s.Label != nil && *s.Label != "" {
		return oneLine(*s.Label, 40)
	}
	if s.Key == "" {
		return emptyKeyName(by)
	}
	return s.Key
}

// writeSetupSummary 打印 orb 表之后的 setup / resume 汇总与快照启动占比。
func writeSetupSummary(w io.Writer, st apigen.OrbStats) error {
	s := st.Setup
	optDur := func(v *int64) string {
		if v == nil {
			return "-"
		}
		return fmtDur(*v)
	}
	_, err := fmt.Fprintf(w, "\nSetup   runs %d  failures %d  slow(≥2m) %d  p50 %s  p95 %s  max %s\nResume  runs %d  failures %d\nSnapshot spawns  %d/%d (%s)\n",
		s.Runs, s.Failures, s.SlowRuns, optDur(s.P50Ms), optDur(s.P95Ms), optDur(s.MaxMs),
		s.ResumeRuns, s.ResumeFailures,
		st.Total.SnapshotSpawns, st.Total.Spawns, fmtPct(float64(st.Total.SnapshotSpawns), float64(st.Total.Spawns)))
	return err
}

// ---- output ----

type statsOutputOpts struct {
	by     string
	win    statsWindowOpts
	filter statsFilterOpts
	out    statsOutOpts
}

func newStatsOutputCmd(env statsEnv) *cobra.Command {
	var o statsOutputOpts
	cmd := &cobra.Command{
		Use:   "output",
		Short: "线程产出：新建 / 交付 / 归档、提交数、回合与每交付成本",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.out.check(); err != nil {
				return err
			}
			if err := checkEnum("by", o.by, outputByValues); err != nil {
				return err
			}
			w, err := env.window(cmd, o.win)
			if err != nil {
				return err
			}
			c, err := env.client()
			if err != nil {
				return err
			}
			q := w.query()
			q.Set("groupBy", o.by)
			if err := o.filter.apply(cmd.Context(), c, q); err != nil {
				return err
			}
			var st apigen.OutputStats
			if done, err := statsFetch(cmd, env, c, "/api/v1/stats/output", q, o.out, &st); done || err != nil {
				return err
			}
			tw := newStatsTable(cmd.OutOrStdout())
			fmt.Fprintln(tw, "KEY\tCREATED\tSHIPPED\tARCHIVED\tCOMMITS\tTURNS\tCOST/SHIPPED")
			row := func(key string, s apigen.OutputStat) {
				per := "-"
				if s.ThreadsShipped > 0 {
					per = fmtUSD(s.ShippedCostUsd / float64(s.ThreadsShipped))
				}
				fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%s\n", termText(key), s.ThreadsCreated, s.ThreadsShipped, s.ThreadsArchived, s.Commits, s.Turns, per)
			}
			for _, g := range st.Groups {
				key := g.Key
				switch {
				case g.Label != nil && *g.Label != "":
					key = oneLine(*g.Label, 40)
				case key == "":
					key = emptyKeyName(o.by)
				}
				row(key, g)
			}
			row("TOTAL", st.Total)
			return tw.Flush()
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&o.by, "by", "day", "分组："+strings.Join(outputByValues, " | "))
	addStatsWindowFlags(cmd, &o.win)
	fl.StringVar(&o.filter.project, "project", "", "只统计该项目（名称 / id / none）")
	fl.StringVar(&o.filter.space, "space", "", "只统计该 Space（名称 / id / none）")
	addStatsOutFlags(cmd, &o.out, true)
	return cmd
}

// ---- thread ----

// threadStatsJSON 是 `stats thread --json` 的输出：各接口的响应原样嵌入。
type threadStatsJSON struct {
	Thread   json.RawMessage `json:"thread"`
	Total    json.RawMessage `json:"total"`
	Models   json.RawMessage `json:"models"`
	Purposes json.RawMessage `json:"purposes"`
	Tools    json.RawMessage `json:"tools"`
	Orb      json.RawMessage `json:"orb,omitempty"`
}

func newStatsThreadCmd(env statsEnv) *cobra.Command {
	var tz string
	var out statsOutOpts
	cmd := &cobra.Command{
		Use:   "thread <id>",
		Short: "单个线程自创建以来的用量、工具与所在 orb 的时长",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatsThread(cmd, env, args[0], tz, out.json)
		},
	}
	addStatsTZFlag(cmd, &tz)
	addStatsOutFlags(cmd, &out, false)
	return cmd
}

func runStatsThread(cmd *cobra.Command, env statsEnv, id, tzFlag string, asJSON bool) error {
	tz, loc, err := resolveStatsTZ(tzFlag, os.Getenv("TZ"), env.localtime, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	c, err := env.client()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	var res threadStatsJSON
	if res.Thread, err = getJSON(ctx, c, "/api/v1/threads/"+url.PathEscape(id)); err != nil {
		return err
	}
	var th apigen.Thread
	if err := json.Unmarshal(res.Thread, &th); err != nil {
		return fmt.Errorf("线程响应无法解析：%w", err)
	}
	// 窗口 = 线程创建至今
	w := statsWindow{from: th.CreatedAt, to: max(env.now().UnixMilli(), th.CreatedAt+1), tz: tz}
	get := func(path string, set func(url.Values)) (json.RawMessage, error) {
		q := w.query()
		set(q)
		return getJSON(ctx, c, path+"?"+q.Encode())
	}
	usageBy := func(by string) func(url.Values) {
		return func(q url.Values) {
			q.Set("groupBy", by)
			q.Set("thread", th.ID)
		}
	}
	if res.Total, err = get("/api/v1/stats/usage", usageBy("thread")); err != nil {
		return err
	}
	if res.Models, err = get("/api/v1/stats/usage", usageBy("model")); err != nil {
		return err
	}
	if res.Purposes, err = get("/api/v1/stats/usage", usageBy("purpose")); err != nil {
		return err
	}
	if res.Tools, err = get("/api/v1/stats/tools", func(q url.Values) { q.Set("thread", th.ID) }); err != nil {
		return err
	}
	if th.OrbID != nil {
		orbID := *th.OrbID
		if res.Orb, err = get("/api/v1/stats/orbs", func(q url.Values) {
			q.Set("groupBy", "orb")
			q.Set("orb", orbID)
		}); err != nil {
			return err
		}
	}
	stdout := cmd.OutOrStdout()
	if asJSON {
		return printJSONTo(stdout, res)
	}
	var total, models, purposes apigen.UsageStats
	var tools apigen.ToolStats
	for _, p := range []struct {
		raw json.RawMessage
		v   any
	}{{res.Total, &total}, {res.Models, &models}, {res.Purposes, &purposes}, {res.Tools, &tools}} {
		if err := json.Unmarshal(p.raw, p.v); err != nil {
			return fmt.Errorf("统计响应无法解析：%w", err)
		}
	}
	tw := newStatsTable(stdout)
	fmt.Fprintf(tw, "Thread\t%s  %s\n", termText(th.ID), oneLine(termText(th.Title), 60))
	fmt.Fprintf(tw, "Window\t%s → now (%s)\n", time.UnixMilli(th.CreatedAt).In(loc).Format(time.DateTime), tz)
	fmt.Fprintln(tw, "\nTotal:")
	writeUsageRows(tw, "thread", nil, &total.Total)
	fmt.Fprintln(tw, "\nBy model:")
	writeUsageRows(tw, "model", models.Groups, nil)
	fmt.Fprintln(tw, "\nBy purpose:")
	writeUsageRows(tw, "purpose", purposes.Groups, nil)
	fmt.Fprintln(tw, "\nTools:")
	writeToolRows(tw, tools.Tools, &tools.Total)
	if res.Orb != nil {
		var orbs apigen.OrbStats
		if err := json.Unmarshal(res.Orb, &orbs); err != nil {
			return fmt.Errorf("统计响应无法解析：%w", err)
		}
		s := orbs.Total
		for _, g := range orbs.Groups {
			if g.Key == *th.OrbID {
				s = g
			}
		}
		fmt.Fprintln(tw, "\nOrb:")
		fmt.Fprintln(tw, "ORB\tTYPE\tRUNNING\tFROZEN\tSTOPPED\tSETUP")
		typ := "-"
		if s.Type != nil {
			typ = *s.Type
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", termText(*th.OrbID), termText(typ), fmtDur(s.RunningMs), fmtDur(s.FrozenMs), fmtDur(s.StoppedMs), fmtDur(s.SetupMs))
	}
	return tw.Flush()
}

// ---- budget ----

func newStatsBudgetCmd(env statsEnv) *cobra.Command {
	var showJSON bool
	show := func(cmd *cobra.Command, _ []string) error {
		c, err := env.client()
		if err != nil {
			return err
		}
		raw, err := getJSON(cmd.Context(), c, "/api/v1/stats/budget")
		if err != nil {
			return err
		}
		return printBudgetRaw(cmd.OutOrStdout(), raw, showJSON)
	}
	cmd := &cobra.Command{
		Use:   "budget",
		Short: "成本预算：查看（缺省）/ set / off",
		Args:  cobra.NoArgs,
		RunE:  show,
	}
	cmd.Flags().BoolVar(&showJSON, "json", false, "原样打印服务端 JSON")
	showCmd := &cobra.Command{Use: "show", Short: "打印上限、当日 / 当月已花与级别", Args: cobra.NoArgs, RunE: show}
	showCmd.Flags().BoolVar(&showJSON, "json", false, "原样打印服务端 JSON")

	var set struct {
		daily, monthly float64
		warn           int
		tz             string
		json           bool
	}
	setCmd := &cobra.Command{
		Use:   "set",
		Short: "设置预算（只改给出的项，并启用）；--daily / --monthly 给 0 表示取消该上限",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fl := cmd.Flags()
			if set.daily < 0 || set.monthly < 0 {
				return usageErr("--daily / --monthly 不能为负")
			}
			return updateBudget(cmd, env, set.json, func(b *apigen.Budget) {
				if fl.Changed("daily") {
					b.DailyUsd = positiveOrNil(set.daily)
				}
				if fl.Changed("monthly") {
					b.MonthlyUsd = positiveOrNil(set.monthly)
				}
				if fl.Changed("warn") {
					b.WarnPct = set.warn
				}
				if fl.Changed("tz") {
					b.Timezone = set.tz
				}
				b.Enabled = true
			})
		},
	}
	sf := setCmd.Flags()
	sf.Float64Var(&set.daily, "daily", 0, "日上限（美元；0 = 取消）")
	sf.Float64Var(&set.monthly, "monthly", 0, "月上限（美元；0 = 取消）")
	sf.IntVar(&set.warn, "warn", 0, "预警百分比（0–99；0 = 不预警）")
	sf.StringVar(&set.tz, "tz", "", "划分自然日 / 月的 IANA 时区")
	sf.BoolVar(&set.json, "json", false, "原样打印服务端 JSON")

	var offJSON bool
	offCmd := &cobra.Command{
		Use:   "off",
		Short: "停用预算告警（保留已设的上限）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return updateBudget(cmd, env, offJSON, func(b *apigen.Budget) { b.Enabled = false })
		},
	}
	offCmd.Flags().BoolVar(&offJSON, "json", false, "原样打印服务端 JSON")
	cmd.AddCommand(showCmd, setCmd, offCmd)
	return cmd
}

func positiveOrNil(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}

// updateBudget 读出现值、按 mutate 合并后整体 PUT，并打印返回的状态。
func updateBudget(cmd *cobra.Command, env statsEnv, asJSON bool, mutate func(*apigen.Budget)) error {
	c, err := env.client()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	var st apigen.BudgetStatus
	if err := c.do(ctx, http.MethodGet, "/api/v1/stats/budget", nil, &st); err != nil {
		return err
	}
	b := st.Budget
	mutate(&b)
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodPut, "/api/v1/stats/budget", b, &raw); err != nil {
		return err
	}
	return printBudgetRaw(cmd.OutOrStdout(), raw, asJSON)
}

func printBudgetRaw(w io.Writer, raw json.RawMessage, asJSON bool) error {
	if asJSON {
		if _, err := w.Write(raw); err != nil {
			return err
		}
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			_, err := io.WriteString(w, "\n")
			return err
		}
		return nil
	}
	var st apigen.BudgetStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("预算响应无法解析：%w", err)
	}
	return printBudget(w, st)
}

func printBudget(w io.Writer, st apigen.BudgetStatus) error {
	b := st.Budget
	state := "enabled"
	if !b.Enabled {
		state = "disabled"
	}
	warn := "off"
	if b.WarnPct > 0 {
		warn = strconv.Itoa(b.WarnPct) + "%"
	}
	tw := newStatsTable(w)
	fmt.Fprintf(tw, "Budget\t%s  tz %s  warn %s\n\n", state, termText(b.Timezone), warn)
	fmt.Fprintln(tw, "PERIOD\tKEY\tSPENT\tLIMIT\tUSED\tLEVEL")
	for _, p := range []struct {
		name string
		p    apigen.BudgetPeriod
	}{{"day", st.Day}, {"month", st.Month}} {
		limit, used := "-", "-"
		if p.p.LimitUsd != nil {
			limit = fmtUSD(*p.p.LimitUsd)
			used = fmtPct(p.p.SpentUsd, *p.p.LimitUsd)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", p.name, termText(p.p.Key), fmtUSD(p.p.SpentUsd), limit, used, termText(string(p.p.Level)))
	}
	return tw.Flush()
}

// ---- 数值格式 ----

// fmtTokens：1234 → 1.2k，3400000 → 3.4M。
func fmtTokens(n int64) string {
	if n < 1000 && n > -1000 {
		return strconv.FormatInt(n, 10)
	}
	v := float64(n) / 1e3
	for _, unit := range []string{"k", "M", "B"} {
		if math.Abs(v) < 999.95 || unit == "B" {
			return strconv.FormatFloat(v, 'f', 1, 64) + unit
		}
		v /= 1e3
	}
	return ""
}

// fmtUSD：小于 1 美元保留 4 位小数（$0.0042），否则 2 位（$12.34）。
func fmtUSD(v float64) string {
	switch {
	case v == 0:
		return "$0.00"
	case math.Abs(v) < 1:
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

// fmtDur：毫秒 → 1h23m / 5m12s / 42s。
func fmtDur(ms int64) string {
	s := ms / 1000
	h, m, sec := s/3600, s%3600/60, s%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

// fmtPct 取整百分比；分母为 0 时为 "-"。
func fmtPct(num, den float64) string {
	if den == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", int64(math.Round(100*num/den)))
}

func fmtTTFT(ms *float64) string {
	switch {
	case ms == nil:
		return "-"
	case *ms < 1000:
		return fmt.Sprintf("%.0fms", *ms)
	}
	return fmt.Sprintf("%.1fs", *ms/1000)
}

func fmtRate(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f", *v)
}
