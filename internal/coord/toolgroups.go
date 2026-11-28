package coord

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 按需加载的宿主工具组（只给全局助手）。协调宿主的 omp 没有 bash，Core 的 tool commands（discoverable 工具挂成 bash
// 命令、提示词里只留一行目录）在这里不生效：宿主工具不论 loadMode 都带着完整 schema 进每一次请求。不常用或占上下文多的
// 工具因此分组：对话开始时只下发常驻工具与 load_tools，系统提示词里列出各组的目录；模型调用 load_tools 后，控制面把组记进
// threads.tool_groups，经 threads.Service.SetHostTools 把该组加进 agent 的工具表、等到应答，再在结果里给出该组的用法。
// agent 重启（工具定义刷新、设置变化）时已加载的组照样下发、用法进系统提示词；主线每段都从空上下文开始，
// 每次启动清空已加载的组。受限的一段（M26）与 Space 协调者的工具表本就很小，不拆。

// ToolLoadTools 是加载工具组的宿主工具。
const ToolLoadTools = "load_tools"

// toolGroup 是一组按需加载的宿主工具。
type toolGroup struct {
	name string
	// summary 是目录里的一句话：这组工具做什么、什么时候该加载。
	summary string
	// guide 返回加载后的用法（load_tools 的结果；已加载的组放进系统提示词）；nil = 工具说明已足够。
	guide func() string
	// has 报告工具名是否属于这一组。
	has func(name string) bool
}

// computerGuide 是助手的电脑的用法（computer 组）。
const computerGuide = `Your computer:
- It sleeps when idle and wakes on the next computer_* call. computer_status starts it; the first time, the other computer_* tools appear from your next turn.
- computer_upload copies a library file (or a file under ~/library/out/ or ~/tmp/) onto it; computer_download saves a file from it into the library.
- To show the owner something on its desktop, call computer_record start, demonstrate it, then computer_record stop: the video is saved into the library.`

// filesGuide 是文件库工具的用法（files 组）。
const filesGuide = `Library files:
- list_files lists every library file you can read.
- A file you wrote (or an image generate_image saved under ~/tmp/) is not kept until you call save_file with its path; it then appears in the owner's library.
- share_file shares a library file with every conversation, or makes one of this conversation's files private again.
- Pass library file ids in create_thread's files to hand files to a new thread.`

// toolGroups 是全部按需组，顺序即目录与 tool_groups 的顺序。
var toolGroups = []toolGroup{
	{
		name:    "artifacts",
		summary: "deliverables the owner opens in Oryn: reports, prototypes, designs, documents, slides. Load it before making or changing one.",
		guide:   func() string { return assistantArtifacts },
		has:     func(n string) bool { return topLevelArtifactTools[n] },
	},
	{
		name:    "views",
		summary: "interactive answers rendered inline in this conversation: comparisons across several dimensions, numbers as stats, charts or tables, plans and checklists, forms and choices to click, small calculators or simulators. Load it whenever an answer has structure.",
		guide:   func() string { return viewTools().Guide },
		has:     func(n string) bool { return n == ToolRenderUI || n == ToolRenderHTML },
	},
	{
		name:    "computer",
		summary: "your own persistent Linux machine: run scripts, process files, use a browser, record its desktop.",
		guide:   func() string { return computerGuide },
		has:     func(n string) bool { return strings.HasPrefix(n, computerToolPrefix) },
	},
	{
		name:    "memory",
		summary: "facts about the owner from past conversations, full-text search of past conversations and threads, the coding agents' engineering memory, and saving what the owner asks you to remember.",
		guide:   func() string { return memoryGuide },
		has:     isMemoryTool,
	},
	{
		name:    "schedules",
		summary: "delayed or recurring work and reminders.",
		has:     func(n string) bool { return n == ToolSetSchedule || n == ToolListSchedules },
	},
	{
		name:    "stats",
		summary: "usage, cost, tool and output statistics.",
		has:     func(n string) bool { return n == ToolGetStats },
	},
	{
		name:    "files",
		summary: "keep files you wrote in the owner's library, list library files, share them across conversations.",
		guide:   func() string { return filesGuide },
		has:     func(n string) bool { return n == ToolSaveFile || n == ToolListFiles || n == ToolShareFile },
	},
}

// onDemandHeader 是系统提示词里目录的开头。
const onDemandHeader = `On-demand tools:
To keep this conversation light, the tool groups below are not loaded yet. Call load_tools with the groups you need before using any of their tools; tools named elsewhere in these instructions that are not in your tool list belong to these groups. Loaded tools are usable from your next step, and the result explains how to use them. Load a group whenever it would help; never tell the owner you cannot do something a group below provides.`

// groupOf 返回工具所属的按需组；常驻工具返回 nil。
func groupOf(name string) *toolGroup {
	for i := range toolGroups {
		if toolGroups[i].has(name) {
			return &toolGroups[i]
		}
	}
	return nil
}

// availableGroup 是一个对话可用的组与其中的工具名（按工具表顺序）。
type availableGroup struct {
	group *toolGroup
	tools []string
}

// splitTools 把完整工具表 all 拆成本次下发的工具（常驻工具与已加载组的工具，保持原顺序，末尾加 load_tools）与可用的组
// （按组定义顺序，含已加载的）。没有可用组时不加 load_tools。
func splitTools(all []apigen.OmpFrame, loaded map[string]bool) ([]apigen.OmpFrame, []availableGroup) {
	members := map[string][]string{}
	active := make([]apigen.OmpFrame, 0, len(all)+1)
	for _, d := range all {
		name := frameName(d)
		g := groupOf(name)
		if g == nil || loaded[g.name] {
			active = append(active, d)
		}
		if g != nil {
			members[g.name] = append(members[g.name], name)
		}
	}
	var avail []availableGroup
	for i := range toolGroups {
		if tools := members[toolGroups[i].name]; len(tools) > 0 {
			avail = append(avail, availableGroup{group: &toolGroups[i], tools: tools})
		}
	}
	if len(avail) > 0 {
		active = append(active, loadToolsDef(avail))
	}
	return active, avail
}

// loadToolsDef 是 load_tools 的定义：groups 的枚举是这个对话可用的组（含已加载的：再要一次无害）。
func loadToolsDef(avail []availableGroup) apigen.OmpFrame {
	names := make([]string, len(avail))
	for i, a := range avail {
		names[i] = a.group.name
	}
	return essentialTool(ToolLoadTools, `Load tool groups listed under "On-demand tools" in your instructions. Their tools are usable from your next step and stay loaded for the rest of this conversation; the result explains how to use them.`, obj{
		"groups": obj{"type": "array", "minItems": 1, "items": obj{"type": "string", "enum": names}, "description": "Names of the groups to load."},
	}, "groups")
}

// toolGroupsPrompt 是系统提示词里按需加载的一段：已加载组的用法，然后是还没加载的组的目录；都没有时为空串。
func toolGroupsPrompt(avail []availableGroup, loaded map[string]bool) string {
	var parts, catalog []string
	for _, a := range avail {
		if loaded[a.group.name] {
			if a.group.guide != nil {
				parts = append(parts, a.group.guide())
			}
			continue
		}
		catalog = append(catalog, "- "+a.group.name+" ("+strings.Join(a.tools, ", ")+"): "+a.group.summary)
	}
	if len(catalog) > 0 {
		parts = append(parts, onDemandHeader+"\n"+strings.Join(catalog, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

// loadedGroups 解析 threads.tool_groups；缺失或无法解析时为空。
func loadedGroups(row db.Thread) map[string]bool {
	loaded := map[string]bool{}
	if row.ToolGroups == nil {
		return loaded
	}
	var names []string
	if err := json.Unmarshal([]byte(*row.ToolGroups), &names); err != nil {
		slog.Warn("coord: 已加载的工具组无法解析", "thread", row.ID, "err", err)
		return loaded
	}
	for _, n := range names {
		loaded[n] = true
	}
	return loaded
}

// saveLoadedGroups 按组定义顺序写回已加载的组（空 = NULL）。
func (s *Service) saveLoadedGroups(ctx context.Context, threadID string, loaded map[string]bool) error {
	var names []string
	for _, g := range toolGroups {
		if loaded[g.name] {
			names = append(names, g.name)
		}
	}
	var v *string
	if len(names) > 0 {
		b, err := json.Marshal(names)
		if err != nil {
			return err
		}
		text := string(b)
		v = &text
	}
	return s.st.Q.SetThreadToolGroups(ctx, db.SetThreadToolGroupsParams{ToolGroups: v, ID: threadID})
}

// episodeGroups 返回这次启动时已加载的组。主线每段从空上下文开始（clear_context）：清空记录，一组都不带。
func (s *Service) episodeGroups(ctx context.Context, row db.Thread) map[string]bool {
	if row.Main != 1 {
		return loadedGroups(row)
	}
	if row.ToolGroups != nil {
		if err := s.saveLoadedGroups(ctx, row.ID, nil); err != nil {
			slog.Warn("coord: 清空主线已加载的工具组失败", "thread", row.ID, "err", err)
		}
	}
	return map[string]bool{}
}

// assistantToolSet 是全局助手对话完整工具表（拆分之前）的组成；restricted 为受限的一段（M26：没有视图工具）。
func (s *Service) assistantToolSet(ctx context.Context, row db.Thread, restricted bool) ToolSet {
	return ToolSet{
		Memory:   s.memoryOn(ctx, row.Kind),
		Main:     row.Main == 1,
		Views:    !restricted && s.generativeUI(ctx),
		Computer: s.computerDefs(ctx),
	}
}

// toolLoadTools 执行 load_tools：把组记进 threads.tool_groups，以新的工具表替换 agent 的宿主工具表并等到应答，再返回
// 新加载组的用法。已加载的组再要一次也照样下发整张表：orbd 自重启复用旧的初始化帧时据此补回。同一对话的调用按线程
// 串行：读取、合并、存库与 set_host_tools 应答整条链路持锁，后一次一定在前一次的结果之上合并、并在它之后下发。
func (s *Service) toolLoadTools(ctx context.Context, c toolCtx) threads.HostToolResult {
	if c.Kind != threads.KindAssistant {
		return failResult(errf("unknown_tool", "no tool named %q", c.Name))
	}
	var a struct {
		Groups []string `json:"groups"`
	}
	if err := decodeArgs(c.Args, &a); err != nil {
		return failResult(err)
	}
	if len(a.Groups) == 0 {
		return failResult(errf(CodeInvalid, "groups is required"))
	}
	mu, _ := s.toolGroupLocks.LoadOrStore(c.ThreadID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	row, err := s.st.Q.GetThread(ctx, c.ThreadID)
	if err != nil {
		return failResult(err)
	}
	all := ToolDefs(s.assistantToolSet(ctx, row, false))
	loaded := loadedGroups(row)
	_, avail := splitTools(all, loaded)
	var names []string
	for _, ag := range avail {
		names = append(names, ag.group.name)
	}
	var lines []string
	fresh := []string{}
	seen := map[string]bool{}
	for _, n := range a.Groups {
		if seen[n] {
			continue
		}
		seen[n] = true
		i := slices.IndexFunc(avail, func(ag availableGroup) bool { return ag.group.name == n })
		if i < 0 {
			return failResult(errf(CodeInvalid, "no tool group %q; available: %s", n, strings.Join(names, ", ")))
		}
		ag := avail[i]
		tools := strings.Join(ag.tools, ", ")
		if loaded[n] {
			lines = append(lines, "## "+n+"\nAlready loaded: "+tools+".")
			continue
		}
		loaded[n] = true
		fresh = append(fresh, n)
		line := "## " + n + "\nLoaded: " + tools + "."
		if ag.group.guide != nil {
			line += "\n\n" + ag.group.guide()
		}
		lines = append(lines, line)
	}
	if len(fresh) > 0 {
		if err := s.saveLoadedGroups(ctx, c.ThreadID, loaded); err != nil {
			return failResult(err)
		}
	}
	active, _ := splitTools(all, loaded)
	if s.th == nil {
		return failResult(errf(CodeCoordinatorUnavailable, "threads are not configured"))
	}
	if err := s.th.SetHostTools(ctx, c.ThreadID, active); err != nil {
		slog.Warn("coord: 替换宿主工具表失败", "thread", c.ThreadID, "err", err)
		return failResult(errf("tools_unavailable", "the tools could not be loaded: %s", err.Error()))
	}
	return threads.HostToolResult{
		Text:    strings.Join(lines, "\n\n"),
		Details: map[string]any{"loaded": fresh},
	}
}
