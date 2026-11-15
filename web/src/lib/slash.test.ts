import { describe, expect, it } from "vitest";
import type { RpcAvailableSlashCommand } from "../omp/types";
import {
	carriesAttachments,
	coordinatorAllows,
	hiddenFromMenu,
	matchModelSelector,
	parseSlash,
	resolveSlash,
	type SlashContext,
	UNAVAILABLE_COMMANDS,
} from "./slash";

const COMMANDS: RpcAvailableSlashCommand[] = [
	{
		name: "compact",
		source: "builtin",
		allowArgs: true,
		subcommands: [{ name: "snap" }, { name: "remote" }],
		textModeExecutable: true,
	},
	{ name: "hotkeys", source: "builtin", textModeExecutable: false },
	{
		name: "extensions",
		aliases: ["status"],
		source: "builtin",
		textModeExecutable: false,
	},
	{ name: "branch", source: "builtin", textModeExecutable: false },
	{
		name: "effort",
		source: "builtin",
		allowArgs: true,
		textModeExecutable: true,
	},
	{ name: "skill:review", source: "skill", allowArgs: true },
	{
		name: "move",
		source: "builtin",
		allowArgs: true,
		textModeExecutable: true,
	},
];

const ctx: SlashContext = {
	planModeEnabled: false,
	vibeEnabled: true,
	loopEnabled: false,
	agentsPaused: false,
	fastModeEnabled: false,
	slowModeEnabled: false,
	commands: COMMANDS,
};

function resolve(text: string, c: SlashContext = ctx) {
	const parsed = parseSlash(text);
	if (!parsed) throw new Error(`not a slash command: ${text}`);
	return resolveSlash(parsed, c);
}

describe("斜杠命令拦截映射", () => {
	it("/plan 切换计划模式（不发给模型）", () => {
		expect(resolve("/plan")).toEqual({
			kind: "rpc",
			frame: { type: "set_plan_mode", enabled: true },
		});
		expect(resolve("/plan", { ...ctx, planModeEnabled: true })).toEqual({
			kind: "rpc",
			frame: { type: "set_plan_mode", enabled: false },
		});
	});

	it("/plan <prompt>、/vibe <prompt> 进入模式后把 prompt 发出；已在模式里只发 prompt，不再切换", () => {
		expect(resolve("/plan 重构登录流程")).toEqual({
			kind: "mode_prompt",
			frame: { type: "set_plan_mode", enabled: true },
			prompt: "重构登录流程",
		});
		expect(
			resolve("/plan 重构登录流程", { ...ctx, planModeEnabled: true }),
		).toEqual({ kind: "mode_prompt", frame: null, prompt: "重构登录流程" });
		expect(resolve("/vibe 修首页", { ...ctx, vibeEnabled: false })).toEqual({
			kind: "mode_prompt",
			frame: { type: "set_vibe_mode", enabled: true },
			prompt: "修首页",
		});
		expect(resolve("/vibe off")).toEqual({
			kind: "rpc",
			frame: { type: "set_vibe_mode", enabled: false },
		});
	});

	it.each(["/tree", "/branch", "/rewind"])("%s 打开会话树", (cmd) => {
		expect(resolve(cmd)).toEqual({ kind: "open_tree" });
	});

	it("会话类命令映射到客户端动作", () => {
		expect(resolve("/fork")).toEqual({ kind: "open_fork" });
		expect(resolve("/new")).toEqual({ kind: "new_thread" });
		expect(resolve("/resume")).toEqual({ kind: "thread_search" });
		expect(resolve("/clear")).toEqual({
			kind: "rpc",
			frame: { type: "clear_context" },
		});
		expect(resolve("/queue 之后再看测试")).toEqual({
			kind: "queue",
			message: "之后再看测试",
		});
		expect(resolve("/queue")).toEqual({
			kind: "needs_args",
			name: "queue",
			usage: "/queue <message>",
		});
	});

	it("有 Web 入口的 TUI 命令打开对应入口", () => {
		expect(resolve("/git")).toEqual({ kind: "open_changes" });
		expect(resolve("/agents")).toEqual({ kind: "open_subagents" });
		expect(resolve("/hub")).toEqual({ kind: "open_subagents" });
		expect(resolve("/subagents")).toEqual({ kind: "open_subagents" });
		expect(resolve("/plan-review")).toEqual({ kind: "review_plan" });
	});

	it("/thinking 带级别直接设置，不带时要求参数（模型对话框里已没有思考级别）", () => {
		expect(resolve("/thinking High")).toEqual({
			kind: "rpc",
			frame: { type: "set_thinking_level", level: "high" },
		});
		expect(resolve("/thinking")).toMatchObject({
			kind: "needs_args",
			name: "thinking",
		});
	});

	it("/fast：ultra 选极速档，on/off/toggle/无参照旧开关；status 交给 omp 报告，不切换；未知词报用法", () => {
		expect(resolve("/fast ultra")).toEqual({
			kind: "rpc",
			frame: { type: "set_ultrafast_mode", enabled: true },
		});
		expect(resolve("/fast off", { ...ctx, fastModeEnabled: true })).toEqual({
			kind: "rpc",
			frame: { type: "set_fast_mode", enabled: false },
		});
		expect(resolve("/fast")).toEqual({
			kind: "rpc",
			frame: { type: "set_fast_mode", enabled: true },
		});
		expect(resolve("/fast status")).toEqual({
			kind: "prompt",
			text: "/fast status",
			attach: false,
		});
		expect(resolve("/fast foo")).toEqual({
			kind: "bad_args",
			name: "fast",
			arg: "foo",
			usage: "/fast [on|ultra|off|status]",
		});
	});

	it("/slow：status 交给 omp，未知词报用法", () => {
		expect(resolve("/slow status", { ...ctx, slowModeEnabled: true })).toEqual({
			kind: "prompt",
			text: "/slow status",
			attach: false,
		});
		expect(resolve("/slow on")).toEqual({
			kind: "rpc",
			frame: { type: "set_slow_mode", enabled: true },
		});
		expect(resolve("/slow sometimes")).toMatchObject({
			kind: "bad_args",
			usage: "/slow [on|off|status]",
		});
	});

	it("/btw 带问题发起旁路提问（并由输入区打开对话框），不带问题只打开对话框；协调对话不放行", () => {
		expect(resolve("/btw 这是什么")).toEqual({
			kind: "ask_btw",
			question: "这是什么",
		});
		expect(resolve("/btw")).toEqual({ kind: "open_btw" });
		expect(coordinatorAllows({ kind: "ask_btw", question: "x" })).toBe(false);
	});

	it("tan / omfg / loop / guided-goal / pause 走对应 RPC", () => {
		expect(resolve("/tan 顺便修 lint")).toEqual({
			kind: "rpc",
			frame: { type: "tan", work: "顺便修 lint" },
		});
		expect(resolve("/omfg 别再改测试了")).toEqual({
			kind: "rpc",
			frame: { type: "omfg", complaint: "别再改测试了" },
		});
		expect(resolve("/loop 每小时检查 CI")).toEqual({
			kind: "rpc",
			frame: { type: "set_loop_mode", enabled: true, args: "每小时检查 CI" },
		});
		expect(resolve("/loop off")).toEqual({
			kind: "rpc",
			frame: { type: "set_loop_mode", enabled: false },
		});
		expect(resolve("/guided-goal")).toEqual({
			kind: "rpc",
			frame: { type: "guided_goal" },
		});
		expect(resolve("/pause")).toEqual({
			kind: "rpc",
			frame: { type: "set_agents_paused", enabled: true },
		});
	});

	it("/goal 子命令：set / show / budget / pause / resume / drop，其余才当目标", () => {
		expect(resolve("/goal")).toEqual({ kind: "open_goal" });
		expect(resolve("/goal show")).toEqual({ kind: "open_goal" });
		expect(resolve("/goal 发布 M2")).toEqual({
			kind: "rpc",
			frame: { type: "set_goal", objective: "发布 M2" },
		});
		expect(resolve("/goal set 发布 M2")).toEqual({
			kind: "rpc",
			frame: { type: "set_goal", objective: "发布 M2" },
		});
		expect(resolve("/goal set")).toMatchObject({ kind: "needs_args" });
		expect(resolve("/goal budget 50000")).toEqual({
			kind: "rpc",
			frame: { type: "set_goal", tokenBudget: 50000 },
		});
		expect(resolve("/goal budget off")).toEqual({
			kind: "rpc",
			frame: { type: "set_goal", tokenBudget: null },
		});
		expect(resolve("/goal budget")).toEqual({ kind: "open_goal" });
		expect(resolve("/goal budget lots")).toEqual({
			kind: "bad_args",
			name: "goal budget",
			arg: "lots",
			usage: "/goal budget <N|off>",
		});
		expect(resolve("/goal budget 0")).toMatchObject({ kind: "bad_args" });
		expect(resolve("/goal pause")).toEqual({
			kind: "rpc",
			frame: { type: "set_goal", action: "pause" },
		});
		expect(resolve("/goal Drop")).toEqual({
			kind: "rpc",
			frame: { type: "set_goal", action: "drop" },
		});
	});

	it.each([...UNAVAILABLE_COMMANDS])("/%s 提示「只能在 TUI 用」", (name) => {
		expect(resolve(`/${name}`)).toEqual({
			kind: "unavailable",
			name,
			reason: "tui",
		});
	});

	it("/move /wt /worktree /stats /export 先行拦下并说明原因", () => {
		expect(resolve("/move ../other")).toEqual({
			kind: "unavailable",
			name: "move",
			reason: "workspace",
		});
		expect(resolve("/wt feature")).toMatchObject({ reason: "workspace" });
		expect(resolve("/worktree")).toMatchObject({ reason: "workspace" });
		expect(resolve("/stats")).toEqual({
			kind: "unavailable",
			name: "stats",
			reason: "stats",
		});
		expect(resolve("/export")).toMatchObject({ reason: "export" });
	});

	it("omp 标为只能在 TUI 执行的命令（含别名）不发给模型", () => {
		expect(resolve("/hotkeys")).toEqual({
			kind: "unavailable",
			name: "hotkeys",
			reason: "tui",
		});
		expect(resolve("/status")).toMatchObject({
			kind: "unavailable",
			reason: "tui",
		});
	});

	it("命令菜单里的会话操作", () => {
		expect(resolve("/handoff 交给下一位")).toEqual({
			kind: "rpc",
			frame: { type: "handoff", customInstructions: "交给下一位" },
		});
		expect(resolve("/compact snap 只保留结论")).toEqual({
			kind: "prompt",
			text: "/compact snap 只保留结论",
			attach: false,
		});
		expect(resolve("/compact 只保留结论")).toEqual({
			kind: "rpc",
			frame: { type: "compact", customInstructions: "只保留结论" },
		});
		expect(resolve("/fresh")).toEqual({
			kind: "rpc",
			frame: { type: "fresh" },
		});
		expect(resolve("/retry")).toEqual({
			kind: "rpc",
			frame: { type: "retry" },
		});
	});

	it("/shake：无参 elide，认识的模式照办，未知参数报用法", () => {
		expect(resolve("/shake")).toEqual({
			kind: "rpc",
			frame: { type: "shake_context", mode: "elide" },
		});
		expect(resolve("/shake images")).toEqual({
			kind: "rpc",
			frame: { type: "shake_context", mode: "images" },
		});
		expect(resolve("/shake foo")).toEqual({
			kind: "bad_args",
			name: "shake",
			arg: "foo",
			usage: "/shake [elide|images|thinking]",
		});
	});

	it("/model 不丢参数：带选择器走 select_model，别名 /models 同样处理", () => {
		expect(resolve("/model")).toEqual({ kind: "open_model" });
		expect(resolve("/model Sonnet")).toEqual({
			kind: "select_model",
			selector: "Sonnet",
		});
		expect(resolve("/models opus")).toEqual({
			kind: "select_model",
			selector: "opus",
		});
	});

	it("报告命令：无参开报告，带参数（kill / pin / reset）交给 omp 执行", () => {
		expect(resolve("/context")).toEqual({
			kind: "report",
			report: "get_context_report",
		});
		expect(resolve("/usage")).toEqual({ kind: "report", report: "get_usage" });
		expect(resolve("/jobs")).toEqual({ kind: "report", report: "get_jobs" });
		expect(resolve("/jobs kill bg_3")).toEqual({
			kind: "prompt",
			text: "/jobs kill bg_3",
			attach: false,
		});
		expect(resolve("/session pin")).toMatchObject({
			kind: "prompt",
			text: "/session pin",
		});
		expect(resolve("/usage reset anthropic")).toMatchObject({
			kind: "prompt",
			text: "/usage reset anthropic",
		});
	});

	it("其余命令经 prompt 交给 omp；skill 等会变成消息的命令带附件，builtin 不带", () => {
		expect(resolve("/effort high")).toEqual({
			kind: "prompt",
			text: "/effort high",
			attach: false,
		});
		expect(resolve("/skill:review HEAD~1")).toEqual({
			kind: "prompt",
			text: "/skill:review HEAD~1",
			attach: true,
		});
		// 还没拿到命令列表时按 builtin 处理（不把附件块拼进命令参数）
		expect(resolve("/review HEAD~1")).toEqual({
			kind: "prompt",
			text: "/review HEAD~1",
			attach: false,
		});
	});

	it("哪些动作会带走附件", () => {
		expect(carriesAttachments(resolve("/plan 做 X"))).toBe(true);
		expect(carriesAttachments(resolve("/queue 之后看"))).toBe(true);
		expect(carriesAttachments(resolve("/skill:review x"))).toBe(true);
		expect(carriesAttachments(resolve("/tan 修 lint"))).toBe(false);
		expect(carriesAttachments(resolve("/effort high"))).toBe(false);
	});

	it("协调对话：进模式发 prompt 与打开工作区入口不放行，排队与切模型放行", () => {
		expect(coordinatorAllows(resolve("/plan 做 X"))).toBe(false);
		expect(coordinatorAllows(resolve("/git"))).toBe(false);
		expect(coordinatorAllows(resolve("/queue x"))).toBe(true);
		expect(coordinatorAllows(resolve("/model opus"))).toBe(true);
	});

	it("路径不是命令", () => {
		expect(parseSlash("/usr/bin/env")).toBeNull();
		expect(parseSlash("普通文本")).toBeNull();
	});
});

describe("命令菜单过滤", () => {
	const byName = (n: string) => {
		const c = COMMANDS.find((x) => x.name === n);
		if (!c) throw new Error(`缺少测试命令 ${n}`);
		return c;
	};
	it("只能在 TUI 执行、客户端没有接管的不列；客户端接管的（/branch）保留；被拦的（/move）不列", () => {
		expect(hiddenFromMenu(byName("hotkeys"))).toBe(true);
		expect(hiddenFromMenu(byName("extensions"))).toBe(true);
		expect(hiddenFromMenu(byName("branch"))).toBe(false);
		expect(hiddenFromMenu(byName("effort"))).toBe(false);
		expect(hiddenFromMenu(byName("move"))).toBe(true);
		expect(hiddenFromMenu({ name: "settings", source: "builtin" })).toBe(true);
	});
});

describe("模型选择器", () => {
	const models = [
		{
			provider: "anthropic",
			id: "claude-sonnet-4-5",
			name: "Claude Sonnet 4.5",
		},
		{ provider: "anthropic", id: "claude-opus-4-1", name: "Claude Opus 4.1" },
		{ provider: "openai", id: "gpt-5", name: "GPT-5" },
		{ provider: "openrouter", id: "gpt-5", name: "GPT-5 (OpenRouter)" },
	];
	it("精确 provider/id、唯一包含匹配能对上；有歧义或对不上返回 null", () => {
		expect(matchModelSelector(models, "openai/gpt-5")?.provider).toBe("openai");
		expect(matchModelSelector(models, "sonnet")?.id).toBe("claude-sonnet-4-5");
		expect(matchModelSelector(models, "Claude Opus 4.1")?.id).toBe(
			"claude-opus-4-1",
		);
		expect(matchModelSelector(models, "gpt-5")).toBeNull();
		expect(matchModelSelector(models, "claude")).toBeNull();
		expect(matchModelSelector(models, "llama")).toBeNull();
	});
});
