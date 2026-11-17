/**
 * 斜杠命令拦截（计划 H5.7「TUI 专属命令的拦截映射」）。
 * 表内命令由客户端处理，**不得**当作文本发给模型；其余命令经 `prompt "/xxx"` 交给 omp 执行（显示 command_output）。
 * omp 只在 `prompt` 命令里执行 builtin（`steer` / `follow_up` 直接把文本交给模型），所以文本路径一律发 `prompt`。
 */
import type { OmpCommand, RpcAvailableSlashCommand } from "../omp/types";

/** 在 Oryn 中不可用的 TUI 专属命令（菜单里不列）。其余只有 TUI 实现的命令按 Core 的 `textModeExecutable` 判定。 */
export const UNAVAILABLE_COMMANDS = [
	"settings",
	"setup",
	"skills",
	"collab",
	"join",
	"leave",
	"live",
	"record",
	"seance",
	"debug",
	"cleanse",
	"restart",
	"exit",
	"quit",
	"copy",
	"open",
] as const;

/** 不可用的原因（i18n `slash.unavailable.<reason>`）。 */
export type UnavailableReason =
	| "tui"
	| "workspace"
	| "stats"
	| "export"
	| "sessionDelete"
	| "sshManaged";

/**
 * 有文本实现、但在 Oryn 里执行会出问题的命令，客户端先行拦下并说明原因：
 * move / wt 直接改 agent 的工作目录，绕过控制面对线程与工作区的绑定（控制面已拦 `move_session` / `worktree_create`）；
 * stats 在 orb 里起本地仪表盘，浏览器打不开；export 把 HTML 写进 orb 的文件系统，Web 没有下载入口。
 */
const BLOCKED_COMMANDS: Record<string, UnavailableReason> = {
	move: "workspace",
	wt: "workspace",
	worktree: "workspace",
	stats: "stats",
	export: "export",
};

export type ReportKind =
	| "get_context_report"
	| "get_usage"
	| "get_jobs"
	| "get_active_tools"
	| "get_session_stats";

export type SlashAction =
	| { kind: "rpc"; frame: OmpCommand }
	/** `/plan <prompt>`、`/vibe <prompt>`：先进入模式（已在模式里则 `frame` 为 null），再把 prompt 作为一条消息发出。 */
	| { kind: "mode_prompt"; frame: OmpCommand | null; prompt: string }
	/** `/queue <消息>`：排到 agent 停下之后（`follow_up`，可带附件）。 */
	| { kind: "queue"; message: string }
	| { kind: "open_tree" }
	| { kind: "open_fork" }
	| { kind: "new_thread" }
	| { kind: "thread_search" }
	| { kind: "open_goal" }
	| { kind: "open_btw" }
	/** `/btw <问题>`：发起旁路提问并打开 btw 对话框看回答。 */
	| { kind: "ask_btw"; question: string }
	| { kind: "open_model" }
	/** `/model <选择器>`：能对上唯一模型就直接切换，否则打开模型对话框并预填搜索。 */
	| { kind: "select_model"; selector: string }
	/** `/git`：右栏改动面板。 */
	| { kind: "open_changes" }
	/** `/agents`、`/hub`：右栏信息面板的子 agent 卡。 */
	| { kind: "open_subagents" }
	/** `/plan-review`：重新打开收起的计划提案。 */
	| { kind: "review_plan" }
	| { kind: "report"; report: ReportKind }
	| { kind: "needs_args"; name: string; usage: string }
	/** 参数不认识：不执行，报用法。 */
	| { kind: "bad_args"; name: string; arg: string; usage: string }
	| { kind: "unavailable"; name: string; reason: UnavailableReason }
	/** 交给 omp 的文本命令。`attach`：命令会把文本当消息交给模型（skill / 自定义 / 文件命令），附件随之发送。 */
	| { kind: "prompt"; text: string; attach: boolean };

/** 解析时需要的当前状态（开关类命令取反、omp 公布的命令）。 */
export interface SlashContext {
	planModeEnabled: boolean;
	vibeEnabled: boolean;
	loopEnabled: boolean;
	agentsPaused: boolean;
	fastModeEnabled: boolean;
	slowModeEnabled: boolean;
	/** omp 公布的命令（`get_available_commands`）；尚未拿到时为空。 */
	commands: readonly RpcAvailableSlashCommand[];
	/** 线程的 SSH 主机由 Oryn 集中管理（orb 上的工作线程）：`/ssh add|remove|rm` 不可用。 */
	sshManaged: boolean;
}

export interface ParsedSlash {
	name: string;
	args: string;
}

/** `"/name args"` → `{name, args}`；不是斜杠命令返回 null。 */
export function parseSlash(text: string): ParsedSlash | null {
	const m = /^\/([^\s/]+)(?:\s+([\s\S]*))?$/.exec(text.trim());
	if (!m?.[1]) return null;
	return { name: m[1].toLowerCase(), args: (m[2] ?? "").trim() };
}

/** 显式的开 / 关参数；其它词返回 undefined。 */
function switchArg(args: string): boolean | undefined {
	const a = args.toLowerCase();
	if (a === "on" || a === "true" || a === "1") return true;
	if (a === "off" || a === "false" || a === "0") return false;
	return undefined;
}

/** 无参或 `toggle` 取反，on / off 照办，其它词 undefined（报用法）。 */
function toggleArg(args: string, current: boolean): boolean | undefined {
	if (!args || args.toLowerCase() === "toggle") return !current;
	return switchArg(args);
}

const SHAKE_MODES: Record<string, "elide" | "images" | "thinking"> = {
	elide: "elide",
	images: "images",
	thinking: "thinking",
};

const REPORTS: Record<string, ReportKind> = {
	context: "get_context_report",
	usage: "get_usage",
	jobs: "get_jobs",
	tools: "get_active_tools",
	session: "get_session_stats",
};

/**
 * 客户端接管的命令的参数提示（菜单里命令名后的灰字、用法报错）。语法沿用 omp 的 `inlineHint` 写法；
 * 未列出的命令用 omp 公布的 `input.hint`。
 */
export const COMMAND_HINTS: Record<string, string> = {
	plan: "[prompt]",
	vibe: "[prompt]",
	queue: "<message>",
	btw: "[question]",
	tan: "<work>",
	omfg: "<complaint>",
	loop: "[count|duration] [--while|--until '<cmd>'] [prompt]",
	goal: "[objective | subcommand]",
	"guided-goal": "[rough objective]",
	pause: "[on|off]",
	handoff: "[focus]",
	compact: "[mode] [focus]",
	shake: "[elide|images|thinking]",
	model: "[model]",
	thinking: "<level>",
	fast: "[on|ultra|off|status]",
	slow: "[on|off|status]",
};

function usageOf(name: string, hint = COMMAND_HINTS[name]): string {
	return hint ? `/${name} ${hint}` : `/${name}`;
}

/** 按名字或别名找 omp 公布的命令。 */
export function findCommand(
	commands: readonly RpcAvailableSlashCommand[],
	name: string,
): RpcAvailableSlashCommand | undefined {
	return commands.find((c) => c.name === name || c.aliases?.includes(name));
}

function modeWithPrompt(
	type: "set_plan_mode" | "set_vibe_mode",
	args: string,
	enabled: boolean,
): SlashAction {
	const on = toggleArg(args, enabled);
	if (on !== undefined) return { kind: "rpc", frame: { type, enabled: on } };
	return {
		kind: "mode_prompt",
		frame: enabled ? null : { type, enabled: true },
		prompt: args,
	};
}

function resolveGoal(args: string): SlashAction {
	const lower = args.toLowerCase();
	if (!args || lower === "show") return { kind: "open_goal" };
	if (lower === "pause" || lower === "resume" || lower === "drop")
		return { kind: "rpc", frame: { type: "set_goal", action: lower } };
	const m = /^(\S+)(?:\s+([\s\S]*))?$/.exec(args);
	const sub = m?.[1]?.toLowerCase();
	const rest = (m?.[2] ?? "").trim();
	if (sub === "set")
		return rest
			? { kind: "rpc", frame: { type: "set_goal", objective: rest } }
			: {
					kind: "needs_args",
					name: "goal set",
					usage: "/goal set <objective>",
				};
	if (sub === "budget") {
		// 与 TUI 一致：无值进入调整（Web 的 goal 对话框），`off` 清除，其余必须是正整数
		if (!rest) return { kind: "open_goal" };
		if (rest.toLowerCase() === "off")
			return { kind: "rpc", frame: { type: "set_goal", tokenBudget: null } };
		const n = /^\d+$/.test(rest) ? Number(rest) : 0;
		return n > 0
			? { kind: "rpc", frame: { type: "set_goal", tokenBudget: n } }
			: {
					kind: "bad_args",
					name: "goal budget",
					arg: rest,
					usage: "/goal budget <N|off>",
				};
	}
	return { kind: "rpc", frame: { type: "set_goal", objective: args } };
}

/** 把一条斜杠命令解析成客户端动作。 */
export function resolveSlash(
	parsed: ParsedSlash,
	ctx: SlashContext,
): SlashAction {
	const { name, args } = parsed;
	const lower = args.toLowerCase();
	if ((UNAVAILABLE_COMMANDS as readonly string[]).includes(name))
		return { kind: "unavailable", name, reason: "tui" };
	const blocked = BLOCKED_COMMANDS[name];
	if (blocked) return { kind: "unavailable", name, reason: blocked };
	// `/session delete` 删掉线程绑定的会话文件（控制面同样拦 drop_session）：指向线程菜单的删除 / 归档
	if (name === "session" && lower.trim() === "delete")
		return { kind: "unavailable", name, reason: "sessionDelete" };
	// orb 线程的 SSH 主机由控制面下发（控制面同样拦）：增删指到设置页；`/ssh list|help` 照常交给 omp
	if (
		name === "ssh" &&
		ctx.sshManaged &&
		["add", "remove", "rm"].includes(lower.split(/\s+/)[0] ?? "")
	)
		return { kind: "unavailable", name, reason: "sshManaged" };
	const text = args ? `/${name} ${args}` : `/${name}`;
	const bad = (): SlashAction => ({
		kind: "bad_args",
		name,
		arg: args,
		usage: usageOf(name),
	});
	const needs = (): SlashAction => ({
		kind: "needs_args",
		name,
		usage: usageOf(name),
	});
	switch (name) {
		case "plan":
			return modeWithPrompt("set_plan_mode", args, ctx.planModeEnabled);
		case "vibe":
			return modeWithPrompt("set_vibe_mode", args, ctx.vibeEnabled);
		case "plan-review":
			return { kind: "review_plan" };
		case "tree":
		case "branch":
		case "rewind":
			return { kind: "open_tree" };
		case "fork":
			return { kind: "open_fork" };
		case "new":
			return { kind: "new_thread" };
		case "resume":
			return { kind: "thread_search" };
		case "git":
			return { kind: "open_changes" };
		case "agents":
		case "hub":
		case "subagents":
			return { kind: "open_subagents" };
		case "clear":
			return { kind: "rpc", frame: { type: "clear_context" } };
		case "queue":
			return args ? { kind: "queue", message: args } : needs();
		case "btw":
			return args ? { kind: "ask_btw", question: args } : { kind: "open_btw" };
		case "tan":
			return args
				? { kind: "rpc", frame: { type: "tan", work: args } }
				: needs();
		case "omfg":
			return args
				? { kind: "rpc", frame: { type: "omfg", complaint: args } }
				: needs();
		case "loop": {
			if (lower === "off")
				return {
					kind: "rpc",
					frame: { type: "set_loop_mode", enabled: false },
				};
			if (!args || lower === "on")
				return {
					kind: "rpc",
					frame: { type: "set_loop_mode", enabled: !ctx.loopEnabled },
				};
			return {
				kind: "rpc",
				frame: { type: "set_loop_mode", enabled: true, args },
			};
		}
		case "goal":
			return resolveGoal(args);
		case "guided-goal":
			return {
				kind: "rpc",
				frame: args
					? { type: "guided_goal", initial: args }
					: { type: "guided_goal" },
			};
		case "pause":
			return {
				kind: "rpc",
				frame: {
					type: "set_agents_paused",
					enabled: switchArg(args) ?? !ctx.agentsPaused,
				},
			};
		// ---- 命令菜单里的会话操作 ----
		case "handoff":
			return {
				kind: "rpc",
				frame: args
					? { type: "handoff", customInstructions: args }
					: { type: "handoff" },
			};
		case "compact": {
			const first = args.split(/\s+/)[0] ?? "";
			const modes =
				findCommand(ctx.commands, "compact")?.subcommands?.map((s) => s.name) ??
				[];
			if (first && modes.includes(first))
				return { kind: "prompt", text, attach: false };
			return {
				kind: "rpc",
				frame: args
					? { type: "compact", customInstructions: args }
					: { type: "compact" },
			};
		}
		case "shake": {
			const mode = args ? SHAKE_MODES[lower] : "elide";
			return mode
				? { kind: "rpc", frame: { type: "shake_context", mode } }
				: bad();
		}
		case "fresh":
			return { kind: "rpc", frame: { type: "fresh" } };
		case "retry":
			return { kind: "rpc", frame: { type: "retry" } };
		case "model":
		case "models":
			return args
				? { kind: "select_model", selector: args }
				: { kind: "open_model" };
		// 思考级别不在模型对话框里了：`/thinking <级别>` 直接设置，校验交给 omp
		case "thinking":
			return args
				? {
						kind: "rpc",
						frame: {
							type: "set_thinking_level",
							level: lower as "auto",
						},
					}
				: needs();
		// 与 omp 的 `/fast [on|ultra|off|status]` 一致：ultra 选 OpenAI ultrafast 档；status 由 omp 报告当前档
		case "fast": {
			if (/^ultra(?:fast)?$/.test(lower))
				return {
					kind: "rpc",
					frame: { type: "set_ultrafast_mode", enabled: true },
				};
			if (lower === "status") return { kind: "prompt", text, attach: false };
			const on = toggleArg(args, ctx.fastModeEnabled);
			return on === undefined
				? bad()
				: { kind: "rpc", frame: { type: "set_fast_mode", enabled: on } };
		}
		case "slow": {
			if (lower === "status") return { kind: "prompt", text, attach: false };
			const on = toggleArg(args, ctx.slowModeEnabled);
			return on === undefined
				? bad()
				: { kind: "rpc", frame: { type: "set_slow_mode", enabled: on } };
		}
		default: {
			const report = REPORTS[name];
			// 带参数的报告命令（`/jobs kill <id>`、`/session pin …`、`/usage reset …`）是操作，交给 omp 执行
			if (report && !args) return { kind: "report", report };
			const cmd = findCommand(ctx.commands, name);
			if (cmd?.textModeExecutable === false)
				return { kind: "unavailable", name, reason: "tui" };
			return {
				kind: "prompt",
				text,
				attach: cmd !== undefined && cmd.source !== "builtin",
			};
		}
	}
}

/** 这条动作会把输入区的附件一起发出去（其余动作执行后附件留在输入区）。 */
export function carriesAttachments(action: SlashAction): boolean {
	return (
		action.kind === "mode_prompt" ||
		action.kind === "queue" ||
		(action.kind === "prompt" && action.attach)
	);
}

/** 模型选择器能对上的唯一模型：`provider/id`、id、名称精确匹配优先，其次唯一的包含匹配；对不上返回 null。 */
export function matchModelSelector<
	M extends { provider: string; id: string; name?: string },
>(models: readonly M[], selector: string): M | null {
	const q = selector.trim().toLowerCase();
	if (!q) return null;
	const exact = models.filter(
		(m) =>
			`${m.provider}/${m.id}`.toLowerCase() === q ||
			m.id.toLowerCase() === q ||
			m.name?.toLowerCase() === q,
	);
	if (exact.length === 1) return exact[0] ?? null;
	if (exact.length > 1) return null;
	const loose = models.filter((m) =>
		`${m.provider}/${m.id} ${m.name ?? ""}`.toLowerCase().includes(q),
	);
	return loose.length === 1 ? (loose[0] ?? null) : null;
}

/** 客户端自带的命令菜单项（与 omp 公布的命令合并显示）。 */
export const CLIENT_COMMANDS = [
	"plan",
	"plan-review",
	"tree",
	"fork",
	"new",
	"resume",
	"clear",
	"queue",
	"btw",
	"tan",
	"omfg",
	"vibe",
	"loop",
	"goal",
	"guided-goal",
	"pause",
	"handoff",
	"compact",
	"shake",
	"fresh",
	"retry",
	"model",
	"fast",
	"slow",
	"context",
	"usage",
	"jobs",
	"tools",
	"session",
	"agents",
	"git",
] as const;

export type ClientCommand = (typeof CLIENT_COMMANDS)[number];

/** omp 标为只能在 TUI 执行、但客户端另有入口而在菜单里保留的命令（不在 CLIENT_COMMANDS 里的）。 */
const MENU_KEEPS_TUI_ONLY: Record<string, true> = {
	branch: true,
	rewind: true,
};

/** omp 公布的这条命令不进菜单：Oryn 不可用，或只能在 TUI 执行且客户端没有接管。 */
export function hiddenFromMenu(c: RpcAvailableSlashCommand): boolean {
	if (
		(UNAVAILABLE_COMMANDS as readonly string[]).includes(c.name) ||
		BLOCKED_COMMANDS[c.name]
	)
		return true;
	return c.textModeExecutable === false && !MENU_KEEPS_TUI_ONLY[c.name];
}

/**
 * 协调对话（Space 聊天、助手对话，M9）允许的 RPC：与控制面协调线程的命令白名单同一批（`get_*` 另行放行），
 * 其余（计划 / goal / vibe / loop、bash、btw、fork、handoff…）服务端回 `coordinator_restricted`，客户端先行拦下。
 */
const COORDINATOR_RPCS: Record<string, true> = {
	prompt: true,
	steer: true,
	follow_up: true,
	abort: true,
	abort_and_prompt: true,
	queue_edit: true,
	queue_remove: true,
	queue_move: true,
	queue_clear: true,
	promote_queued_message: true,
	set_model: true,
	set_thinking_level: true,
	set_fast_mode: true,
	set_slow_mode: true,
	switch_leaf: true,
	navigate_tree: true,
	abort_branch_summary: true,
	set_label: true,
	set_entry_label: true,
	compact: true,
	clear_context: true,
	retry: true,
	set_session_name: true,
};

/** 服务端不放行的 `get_*`（协调宿主没有工作区）。 */
const COORDINATOR_DENIED_GETS: Record<string, true> = {
	get_git_changes: true,
	get_git_diff: true,
	get_git_status: true,
	get_ssh_hosts: true,
};

/** 协调对话里这条斜杠命令能不能执行（打开对话框类按其背后的 RPC 判断）。 */
export function coordinatorAllows(action: SlashAction): boolean {
	switch (action.kind) {
		case "rpc": {
			const type = action.frame.type;
			return type.startsWith("get_")
				? COORDINATOR_DENIED_GETS[type] !== true
				: COORDINATOR_RPCS[type] === true;
		}
		case "mode_prompt":
		case "open_fork":
		case "open_goal":
		case "open_btw":
		case "ask_btw":
		case "open_changes":
		case "open_subagents":
		case "review_plan":
			return false;
		default:
			return true;
	}
}

/** 协调对话命令菜单里出现的客户端命令。 */
export const COORDINATOR_CLIENT_COMMANDS: Partial<Record<ClientCommand, true>> =
	{
		tree: true,
		new: true,
		resume: true,
		clear: true,
		queue: true,
		compact: true,
		retry: true,
		model: true,
		fast: true,
		slow: true,
		context: true,
		usage: true,
		jobs: true,
		tools: true,
		session: true,
	};
