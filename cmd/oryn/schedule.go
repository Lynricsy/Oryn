package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// `oryn schedule …`（M9）：线程内定时（set，到点向线程追加提示词）与项目定时（add，每次运行新建一个线程）。
// orb 内只有 set / show / pause / resume / rm，且作用于调用方线程自己的定时（经 orbd 控制口）。

var scheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "定时：线程内定时（set）与每次新建线程的项目定时（add）",
}

// scheduleFlags 是 set / add / update 的参数。
type scheduleFlags struct {
	cron, every, at, tz, name, until, prompt, promptFile string
	disabled, json                                       bool
	// 仅 add / update（new_thread）。
	project                                            string
	noProject                                          bool
	space, model, thinking, provider, orbType, overlap string
	keep                                               int
}

func addTimeFlags(cmd *cobra.Command, f *scheduleFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.cron, "cron", "", "cron 表达式（5 段，或 @hourly / @daily / @weekly / @monthly / @yearly）")
	fl.StringVar(&f.every, "every", "", "固定间隔（Go 时长，≥1m，如 30m、2h）")
	fl.StringVar(&f.at, "at", "", "一次性：RFC3339 时刻，或相对现在的 +时长（如 +15m）")
	fl.StringVar(&f.tz, "tz", "", "IANA 时区（缺省取环境变量 TZ，再缺省 UTC）")
	fl.StringVar(&f.name, "name", "", "名称")
	fl.StringVar(&f.until, "until", "", "截止时刻（RFC3339）")
	fl.StringVar(&f.prompt, "prompt", "", "到点要做的事（提示词；支持 {{now}} {{date}} {{scheduled_at}} {{run_number}} {{last_run_at}}）")
	fl.StringVar(&f.promptFile, "prompt-file", "", "从文件读取提示词（- = stdin）")
	fl.BoolVar(&f.disabled, "disabled", false, "保存为暂停")
	fl.BoolVar(&f.json, "json", false, "以 JSON 输出定时")
}

func addNewThreadFlags(cmd *cobra.Command, f *scheduleFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.space, "space", "", "新线程加入的 Space（名称或 id；update 时 none = 不加入）")
	fl.StringVar(&f.model, "model", "", "模型（provider/id）")
	fl.StringVar(&f.thinking, "thinking", "", "思考级别")
	fl.StringVar(&f.provider, "provider", "", "orb provider 名称")
	fl.StringVar(&f.orbType, "type", "", "orb 类型")
	fl.StringVar(&f.overlap, "overlap", "", "上一次运行未结束时：skip（缺省）跳过 / allow 照常新建（至多 3 个并存）")
	fl.IntVar(&f.keep, "keep", 5, "只保留最近 N 个未归档的运行线程（1–50），更早且已静止的自动归档")
}

// scheduleTimezone 返回 --tz 的缺省值：环境变量 TZ，再缺省 UTC（并在 stderr 提示）。
func scheduleTimezone(stderr io.Writer) string {
	if tz := os.Getenv("TZ"); tz != "" && !strings.HasPrefix(tz, ":") {
		return tz
	}
	fmt.Fprintln(stderr, "oryn: 未给 --tz 且环境变量 TZ 为空，按 UTC 解释时间")
	return "UTC"
}

// parseAt 解析 --at：RFC3339，或 +时长（相对 now）。
func parseAt(s string, now time.Time) (int64, error) {
	if rest, ok := strings.CutPrefix(s, "+"); ok {
		d, err := time.ParseDuration(rest)
		if err != nil || d <= 0 {
			return 0, usageErr("--at %q 不是有效的 +时长", s)
		}
		return now.Add(d).UnixMilli(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, usageErr("--at %q 须为 RFC3339（如 2026-01-02T09:00:00+08:00）或 +时长", s)
	}
	return t.UnixMilli(), nil
}

// applyScheduleFlags 把时间、提示词等通用参数合并进 in。fresh 为真表示新建：须给出一种时间与提示词，
// 时区缺省按 scheduleTimezone；否则（update）只覆盖给出的参数。
func applyScheduleFlags(in *apigen.ScheduleInput, f scheduleFlags, changed func(string) bool, fresh bool, now time.Time, stdin io.Reader, stderr io.Writer) error {
	var kinds []string
	for _, k := range []string{"cron", "every", "at"} {
		if changed(k) {
			kinds = append(kinds, "--"+k)
		}
	}
	switch {
	case len(kinds) > 1:
		return usageErr("%s 只能给一个", strings.Join(kinds, " / "))
	case len(kinds) == 0 && fresh:
		return usageErr("须给出 --cron、--every 或 --at 之一")
	}
	if len(kinds) == 1 {
		in.Cron, in.Every, in.At = nil, nil, nil
		switch kinds[0] {
		case "--cron":
			in.Kind, in.Cron = apigen.ScheduleKindCron, &f.cron
		case "--every":
			in.Kind, in.Every = apigen.ScheduleKindEvery, &f.every
		case "--at":
			at, err := parseAt(f.at, now)
			if err != nil {
				return err
			}
			in.Kind, in.At = apigen.ScheduleKindOnce, &at
		}
	}
	switch {
	case changed("tz"):
		in.Timezone = &f.tz
	case fresh:
		in.Timezone = new(scheduleTimezone(stderr))
	}
	if changed("until") {
		if f.until == "" {
			in.EndsAt = nil
		} else {
			t, err := time.Parse(time.RFC3339, f.until)
			if err != nil {
				return usageErr("--until %q 须为 RFC3339", f.until)
			}
			in.EndsAt = new(t.UnixMilli())
		}
	}
	if changed("name") {
		in.Name = &f.name
	}
	switch {
	case changed("prompt") && changed("prompt-file"):
		return usageErr("--prompt 与 --prompt-file 只能给一个")
	case changed("prompt"):
		in.Prompt = f.prompt
	case changed("prompt-file"):
		var b []byte
		var err error
		if f.promptFile == "-" {
			b, err = io.ReadAll(stdin)
		} else {
			b, err = os.ReadFile(f.promptFile)
		}
		if err != nil {
			return err
		}
		in.Prompt = strings.TrimRight(string(b), "\n")
	case fresh:
		return usageErr("须给出 --prompt 或 --prompt-file")
	}
	if f.disabled {
		in.Enabled = new(false)
	} else if fresh {
		in.Enabled = nil
	}
	return nil
}

// applyNewThreadFlags 合并 new_thread 的参数（只覆盖给出的）；fresh 时 --keep 的缺省值也写入。
func applyNewThreadFlags(ctx context.Context, c *apiClient, in *apigen.ScheduleInput, f scheduleFlags, changed func(string) bool, fresh bool) error {
	if changed("space") {
		switch f.space {
		case "", "none":
			in.SpaceID = nil
		default:
			id, err := resolveSpace(ctx, c, f.space)
			if err != nil {
				return err
			}
			in.SpaceID = &id
		}
	}
	if changed("model") {
		p, id, ok := strings.Cut(f.model, "/")
		if !ok || p == "" || id == "" {
			return usageErr("--model 须为 provider/id")
		}
		in.Model = &apigen.ModelRef{Provider: p, ID: id}
	}
	if changed("thinking") {
		in.ThinkingLevel = optional(f.thinking)
	}
	if changed("provider") {
		in.Provider = optional(f.provider)
	}
	if changed("type") {
		in.OrbType = optional(f.orbType)
	}
	if changed("overlap") {
		o := apigen.ScheduleOverlap(f.overlap)
		if o != apigen.ScheduleOverlapSkip && o != apigen.ScheduleOverlapAllow {
			return usageErr("--overlap 须为 skip 或 allow")
		}
		in.Overlap = &o
	}
	if changed("keep") || fresh {
		in.KeepLast = new(f.keep)
	}
	return nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// inputFromSchedule 把已有定义转回请求体（update 先 GET 再合并）。
func inputFromSchedule(s apigen.Schedule) apigen.ScheduleInput {
	in := apigen.ScheduleInput{
		Kind: s.Kind, Cron: s.Cron, Every: s.Every, At: s.At, Timezone: &s.Timezone, EndsAt: s.EndsAt, Prompt: s.Prompt,
		Enabled: new(s.Enabled), Mode: new(s.Mode),
	}
	if s.Name != "" {
		in.Name = new(s.Name)
	}
	if s.Mode == apigen.ScheduleModeThread {
		in.ThreadID = s.ThreadID
		return in
	}
	in.ProjectID, in.SpaceID, in.Model, in.ThinkingLevel = s.ProjectID, s.SpaceID, s.Model, s.ThinkingLevel
	in.Provider, in.OrbType, in.Overlap, in.KeepLast = s.Provider, s.OrbType, new(s.Overlap), new(s.KeepLast)
	return in
}

// resolveScheduleRef 把 <ref> 解析为定时 id：sch_… 原样；T-… 取该线程的线程内定时。
func resolveScheduleRef(ctx context.Context, c *apiClient, ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "sch_"):
		return ref, nil
	case strings.HasPrefix(ref, "T-"):
		var s apigen.Schedule
		if err := c.do(ctx, http.MethodGet, "/api/v1/threads/"+url.PathEscape(ref)+"/schedule", nil, &s); err != nil {
			return "", err
		}
		return s.ID, nil
	}
	return "", usageErr("%q 不是定时 id（sch_…）或线程 id（T-…）", ref)
}

// ---- set：线程内定时 ----

var scheduleSetOpts scheduleFlags

var scheduleSetCmd = &cobra.Command{
	Use:   "set <thread>",
	Short: "设置线程内定时（每线程一个；到点提示词作为新消息出现在该线程）；orb 内省略 <thread>，作用于当前线程",
	Args:  cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		o := scheduleSetOpts
		var in apigen.ScheduleInput
		if err := applyScheduleFlags(&in, o, cmd.Flags().Changed, true, time.Now(), os.Stdin, os.Stderr); err != nil {
			return err
		}
		var s apigen.Schedule
		if inOrb() {
			if len(args) > 0 {
				return usageErr("orb 内的 `schedule set` 不接受线程参数（只能设置当前线程的定时）")
			}
			var err error
			if s, err = orbScheduleSet(cmd.Context(), in); err != nil {
				return err
			}
		} else {
			if len(args) != 1 {
				return usageErr("用法：oryn schedule set <thread> …")
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			if err := c.do(cmd.Context(), http.MethodPut, "/api/v1/threads/"+url.PathEscape(args[0])+"/schedule", in, &s); err != nil {
				return err
			}
		}
		return printScheduleResult(s, o.json)
	},
}

// printScheduleResult 打印 set / add / update 的结果：--json 为定时，否则 stdout 只打印 id、stderr 打印摘要。
func printScheduleResult(s apigen.Schedule, asJSON bool) error {
	if asJSON {
		return printJSON(s)
	}
	fmt.Println(s.ID)
	fmt.Fprintf(os.Stderr, "%s · %s\n", scheduleSpec(s), nextText(s))
	return nil
}

// ---- add / update：每次新建线程的项目定时 ----

var scheduleAddOpts scheduleFlags

var scheduleAddCmd = &cobra.Command{
	Use:   "add (--project <名称> | --no-project) --name <N> (--cron | --every | --at) (--prompt | --prompt-file)",
	Short: "新建项目定时：每次运行用固定提示词新建一个线程；stdout 打印定时 id",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if inOrb() {
			return errUnavailableInOrb("schedule add")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		s, err := scheduleAdd(cmd.Context(), c, scheduleAddOpts, cmd.Flags().Changed, os.Stdin, os.Stderr)
		if err != nil {
			return err
		}
		return printScheduleResult(s, scheduleAddOpts.json)
	},
}

func scheduleAdd(ctx context.Context, c *apiClient, o scheduleFlags, changed func(string) bool, stdin io.Reader, stderr io.Writer) (apigen.Schedule, error) {
	if (o.project != "") == o.noProject {
		return apigen.Schedule{}, usageErr("须给出 --project <名称> 或 --no-project 之一")
	}
	if strings.TrimSpace(o.name) == "" {
		return apigen.Schedule{}, usageErr("须给出 --name")
	}
	in := apigen.ScheduleInput{Mode: new(apigen.ScheduleModeNewThread)}
	if err := applyScheduleFlags(&in, o, changed, true, time.Now(), stdin, stderr); err != nil {
		return apigen.Schedule{}, err
	}
	if err := applyNewThreadFlags(ctx, c, &in, o, changed, true); err != nil {
		return apigen.Schedule{}, err
	}
	if o.project != "" {
		id, err := resolveProject(ctx, c, o.project)
		if err != nil {
			return apigen.Schedule{}, err
		}
		in.ProjectID = &id
	}
	var s apigen.Schedule
	err := c.do(ctx, http.MethodPost, "/api/v1/schedules", in, &s)
	return s, err
}

var scheduleUpdateOpts scheduleFlags

var scheduleUpdateCmd = &cobra.Command{
	Use:   "update <ref>",
	Short: "修改定时（只改给出的参数；<ref> 为 sch_… 或线程 id）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			return errUnavailableInOrb("schedule update")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		s, err := scheduleUpdate(cmd.Context(), c, args[0], scheduleUpdateOpts, cmd.Flags().Changed, os.Stdin, os.Stderr)
		if err != nil {
			return err
		}
		return printScheduleResult(s, scheduleUpdateOpts.json)
	},
}

func scheduleUpdate(ctx context.Context, c *apiClient, ref string, o scheduleFlags, changed func(string) bool, stdin io.Reader, stderr io.Writer) (apigen.Schedule, error) {
	id, err := resolveScheduleRef(ctx, c, ref)
	if err != nil {
		return apigen.Schedule{}, err
	}
	path := "/api/v1/schedules/" + url.PathEscape(id)
	var cur apigen.Schedule
	if err := c.do(ctx, http.MethodGet, path, nil, &cur); err != nil {
		return apigen.Schedule{}, err
	}
	in := inputFromSchedule(cur)
	if err := applyScheduleFlags(&in, o, changed, false, time.Now(), stdin, stderr); err != nil {
		return apigen.Schedule{}, err
	}
	if cur.Mode == apigen.ScheduleModeNewThread {
		if err := applyNewThreadFlags(ctx, c, &in, o, changed, false); err != nil {
			return apigen.Schedule{}, err
		}
	} else {
		for _, name := range []string{"space", "model", "thinking", "provider", "type", "overlap", "keep"} {
			if changed(name) {
				return apigen.Schedule{}, usageErr("--%s 只适用于每次新建线程的定时", name)
			}
		}
	}
	var s apigen.Schedule
	err = c.do(ctx, http.MethodPut, path, in, &s)
	return s, err
}

// ---- 查看与操作 ----

var scheduleLsOpts struct {
	project, mode string
	json          bool
}

var scheduleLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "列出定时（--project 只列该项目的项目定时；none = 没有项目的）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if inOrb() {
			return errUnavailableInOrb("schedule ls")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		list, err := scheduleList(cmd.Context(), c, scheduleLsOpts.project, scheduleLsOpts.mode)
		if err != nil {
			return err
		}
		if scheduleLsOpts.json {
			return printJSON(apigen.ScheduleList{Schedules: list})
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tMODE\tNAME\tTARGET\tWHEN\tNEXT\tLAST")
		for _, s := range list {
			last := "-"
			if s.LastRun != nil {
				last = string(s.LastRun.Status)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Mode, displayScheduleName(s.Name), scheduleTarget(s), scheduleSpec(s), nextText(s), last)
		}
		return tw.Flush()
	},
}

func scheduleList(ctx context.Context, c *apiClient, project, mode string) ([]apigen.Schedule, error) {
	q := url.Values{}
	if mode != "" {
		if m := apigen.ScheduleMode(mode); m != apigen.ScheduleModeThread && m != apigen.ScheduleModeNewThread {
			return nil, usageErr("--mode 须为 thread 或 new_thread")
		}
		q.Set("mode", mode)
	}
	switch project {
	case "":
	case "none":
		q.Set("project", "none")
	default:
		id, err := resolveProject(ctx, c, project)
		if err != nil {
			return nil, err
		}
		q.Set("project", id)
	}
	path := "/api/v1/schedules"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var list apigen.ScheduleList
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	return list.Schedules, nil
}

var scheduleShowOpts struct{ json bool }

var scheduleShowCmd = &cobra.Command{
	Use:   "show <ref>",
	Short: "显示定时（<ref> 为 sch_… 或线程 id；orb 内省略，显示当前线程的定时）",
	Args:  cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		var s apigen.Schedule
		if inOrb() {
			if len(args) > 0 {
				return usageErr("orb 内的 `schedule show` 不接受参数")
			}
			got, err := orbScheduleGet(ctx)
			if err != nil {
				return err
			}
			if got == nil {
				if scheduleShowOpts.json {
					fmt.Println("null")
				} else {
					fmt.Fprintln(os.Stderr, "当前线程没有定时")
				}
				return nil
			}
			s = *got
		} else {
			if len(args) != 1 {
				return usageErr("用法：oryn schedule show <ref>")
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			id, err := resolveScheduleRef(ctx, c, args[0])
			if err != nil {
				return err
			}
			if err := c.do(ctx, http.MethodGet, "/api/v1/schedules/"+url.PathEscape(id), nil, &s); err != nil {
				return err
			}
		}
		if scheduleShowOpts.json {
			return printJSON(s)
		}
		return printSchedule(os.Stdout, s)
	},
}

func printSchedule(w io.Writer, s apigen.Schedule) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "ID\t%s\nMode\t%s\nName\t%s\nTarget\t%s\nWhen\t%s\nEnabled\t%v", s.ID, s.Mode, displayScheduleName(s.Name), scheduleTarget(s), scheduleSpec(s), s.Enabled)
	if s.PausedReason != nil {
		fmt.Fprintf(tw, " (%s)", *s.PausedReason)
	}
	fmt.Fprintf(tw, "\nNext\t%s\nRuns\t%d（未结束 %d，连续失败 %d）\nCreatedBy\t%s\n", nextText(s), s.RunCount, s.OpenRuns, s.Failures, s.CreatedBy)
	if s.EndsAt != nil {
		fmt.Fprintf(tw, "Until\t%s\n", fmtMs(*s.EndsAt))
	}
	if s.Mode == apigen.ScheduleModeNewThread {
		fmt.Fprintf(tw, "Overlap\t%s\nKeep\t%d\n", s.Overlap, s.KeepLast)
		if s.Model != nil {
			fmt.Fprintf(tw, "Model\t%s/%s\n", s.Model.Provider, s.Model.ID)
		}
		if s.Provider != nil || s.OrbType != nil {
			fmt.Fprintf(tw, "Orb\t%s %s\n", deref(s.Provider), deref(s.OrbType))
		}
	}
	if s.LastRun != nil {
		r := s.LastRun
		fmt.Fprintf(tw, "LastRun\t%s %s（%s）", fmtMs(r.StartedAt), r.Status, r.Trigger)
		if r.Error != nil {
			fmt.Fprintf(tw, " %s", *r.Error)
		}
		fmt.Fprintln(tw)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%s\n", s.Prompt)
	return nil
}

func displayScheduleName(n string) string {
	if n == "" {
		return "-"
	}
	return n
}

func scheduleTarget(s apigen.Schedule) string {
	if s.Mode == apigen.ScheduleModeThread {
		return deref(s.ThreadID)
	}
	t := "project " + deref(s.ProjectID)
	if s.ProjectID == nil {
		t = "no project"
	}
	if s.SpaceID != nil {
		t += " · space " + *s.SpaceID
	}
	return t
}

// scheduleSpec 是时间定义的一行描述。
func scheduleSpec(s apigen.Schedule) string {
	var spec string
	switch s.Kind {
	case apigen.ScheduleKindCron:
		spec = "cron " + deref(s.Cron)
	case apigen.ScheduleKindEvery:
		spec = "every " + deref(s.Every)
	case apigen.ScheduleKindOnce:
		if s.At != nil {
			spec = "once " + fmtMs(*s.At)
		} else {
			spec = "once"
		}
	}
	return spec + " (" + s.Timezone + ")"
}

func nextText(s apigen.Schedule) string {
	if s.NextRunAt == nil {
		if s.PausedReason != nil {
			return "paused: " + string(*s.PausedReason)
		}
		return "no next run"
	}
	return "next " + fmtMs(*s.NextRunAt)
}

func fmtMs(ms int64) string { return time.UnixMilli(ms).Local().Format(time.DateTime) }

// scheduleRefAction 是 pause / resume / rm 的共同流程：orb 内作用于当前线程的定时，orb 外解析 <ref>。
func scheduleRefAction(cmd *cobra.Command, args []string, inOrbFn func(ctx context.Context) error, apiFn func(ctx context.Context, c *apiClient, id string) error) error {
	ctx := cmd.Context()
	if inOrb() {
		if len(args) > 0 {
			return usageErr("orb 内的 `schedule %s` 不接受参数（作用于当前线程的定时）", cmd.Name())
		}
		return inOrbFn(ctx)
	}
	if len(args) != 1 {
		return usageErr("用法：oryn schedule %s <ref>", cmd.Name())
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	id, err := resolveScheduleRef(ctx, c, args[0])
	if err != nil {
		return err
	}
	return apiFn(ctx, c, id)
}

func scheduleEnableCmd(enabled bool) *cobra.Command {
	use, short, done := "pause [<ref>]", "暂停定时", "已暂停"
	if enabled {
		use, short, done = "resume [<ref>]", "恢复定时（从现在起重新计算下一次，不补跑暂停期间错过的）", "已恢复"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var s apigen.Schedule
			err := scheduleRefAction(cmd, args, func(ctx context.Context) error {
				var err error
				s, err = orbScheduleEnable(ctx, enabled)
				return err
			}, func(ctx context.Context, c *apiClient, id string) error {
				action := "pause"
				if enabled {
					action = "resume"
				}
				return c.do(ctx, http.MethodPost, "/api/v1/schedules/"+url.PathEscape(id)+"/"+action, nil, &s)
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s %s · %s\n", done, s.ID, nextText(s))
			return nil
		},
	}
}

var scheduleRmCmd = &cobra.Command{
	Use:   "rm [<ref>]",
	Short: "删除定时（连同运行记录）",
	Args:  cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		err := scheduleRefAction(cmd, args, orbScheduleDelete, func(ctx context.Context, c *apiClient, id string) error {
			return c.do(ctx, http.MethodDelete, "/api/v1/schedules/"+url.PathEscape(id), nil, nil)
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "已删除")
		return nil
	},
}

var scheduleRunOpts struct{ json bool }

var scheduleRunCmd = &cobra.Command{
	Use:   "run <ref>",
	Short: "立即运行一次（不改下一次时间；上一次未结束时回 schedule_busy）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			return errUnavailableInOrb("schedule run")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		id, err := resolveScheduleRef(ctx, c, args[0])
		if err != nil {
			return err
		}
		var r apigen.ScheduleRun
		if err := c.do(ctx, http.MethodPost, "/api/v1/schedules/"+url.PathEscape(id)+"/run", nil, &r); err != nil {
			return err
		}
		if scheduleRunOpts.json {
			return printJSON(r)
		}
		fmt.Println(r.ID)
		msg := string(r.Status)
		if r.ThreadID != nil {
			msg += " · " + *r.ThreadID
		}
		if r.Error != nil {
			msg += " · " + *r.Error
		}
		fmt.Fprintln(os.Stderr, msg)
		return nil
	},
}

var scheduleRunsOpts struct {
	n    int
	json bool
}

var scheduleRunsCmd = &cobra.Command{
	Use:   "runs <ref>",
	Short: "最近的运行（新的在前）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			return errUnavailableInOrb("schedule runs")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		id, err := resolveScheduleRef(ctx, c, args[0])
		if err != nil {
			return err
		}
		var list apigen.ScheduleRunList
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/schedules/%s/runs?limit=%d", url.PathEscape(id), scheduleRunsOpts.n), nil, &list); err != nil {
			return err
		}
		if scheduleRunsOpts.json {
			return printJSON(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "STARTED\tSCHEDULED\tTRIGGER\tSTATUS\tTHREAD\tERROR")
		for _, r := range list.Runs {
			trig := string(r.Trigger)
			if r.Missed > 0 {
				trig += fmt.Sprintf("(+%d)", r.Missed)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", fmtMs(r.StartedAt), fmtMs(r.ScheduledFor), trig, r.Status,
				displayScheduleName(deref(r.ThreadID)), oneLine(deref(r.Error), 80))
		}
		return tw.Flush()
	},
}

func init() {
	addTimeFlags(scheduleSetCmd, &scheduleSetOpts)
	addTimeFlags(scheduleAddCmd, &scheduleAddOpts)
	addNewThreadFlags(scheduleAddCmd, &scheduleAddOpts)
	scheduleAddCmd.Flags().StringVar(&scheduleAddOpts.project, "project", "", "新线程所在的项目（名称或 id）")
	scheduleAddCmd.Flags().BoolVar(&scheduleAddOpts.noProject, "no-project", false, "新线程不关联项目")
	addTimeFlags(scheduleUpdateCmd, &scheduleUpdateOpts)
	addNewThreadFlags(scheduleUpdateCmd, &scheduleUpdateOpts)
	scheduleLsCmd.Flags().StringVar(&scheduleLsOpts.project, "project", "", "项目（名称、id 或 none）")
	scheduleLsCmd.Flags().StringVar(&scheduleLsOpts.mode, "mode", "", "thread 或 new_thread")
	scheduleLsCmd.Flags().BoolVar(&scheduleLsOpts.json, "json", false, "以 JSON 输出")
	scheduleShowCmd.Flags().BoolVar(&scheduleShowOpts.json, "json", false, "以 JSON 输出")
	scheduleRunCmd.Flags().BoolVar(&scheduleRunOpts.json, "json", false, "以 JSON 输出运行")
	scheduleRunsCmd.Flags().IntVarP(&scheduleRunsOpts.n, "n", "n", 20, "条数（≤100）")
	scheduleRunsCmd.Flags().BoolVar(&scheduleRunsOpts.json, "json", false, "以 JSON 输出")
	scheduleCmd.AddCommand(scheduleSetCmd, scheduleAddCmd, scheduleUpdateCmd, scheduleLsCmd, scheduleShowCmd,
		scheduleEnableCmd(false), scheduleEnableCmd(true), scheduleRmCmd, scheduleRunCmd, scheduleRunsCmd)
	root.AddCommand(scheduleCmd)
}
