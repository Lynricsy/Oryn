/**
 * 斜杠命令拦截（计划 H5.7「TUI 专属命令的拦截映射」）。
 * 表内命令由客户端处理，**不得**当作文本发给模型；其余命令经 `prompt "/xxx"` 交给 omp 执行（显示 command_output）。
 */
import type { OmpCommand } from "../omp/types";

/** 在 Oryn 中不可用的 TUI 专属命令。 */
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

export type ReportKind =
	| "get_context_report"
	| "get_usage"
	| "get_jobs"
	| "get_active_tools"
	| "get_session_stats";

export type SlashAction =
	| { kind: "rpc"; frame: OmpCommand }
	| { kind: "open_tree" }
	| { kind: "open_fork" }
	| { kind: "new_thread" }
	| { kind: "thread_search" }
	| { kind: "open_goal" }
	| { kind: "open_btw" }
	| { kind: "open_model" }
	| { kind: "report"; report: ReportKind }
	| { kind: "needs_args"; name: string }
	| { kind: "unavailable"; name: string }
	| { kind: "prompt"; text: string };

/** 解析时需要的当前模式状态（用于开关类命令取反）。 */
export interface SlashContext {
	planModeEnabled: boolean;
	vibeEnabled: boolean;
	loopEnabled: boolean;
	agentsPaused: boolean;
	fastModeEnabled: boolean;
	slowModeEnabled: boolean;
	/** omp 公布的 `/compact` 子命令（压缩模式）名。 */
	compactModes: readonly string[];
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

function onOff(args: string, current: boolean): boolean {
	const a = args.toLowerCase();
	if (a === "on" || a === "true" || a === "1") return true;
	if (a === "off" || a === "false" || a === "0") return false;
	return !current;
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
	stats: "get_session_stats",
};

/** 把一条斜杠命令解析成客户端动作。 */
export function resolveSlash(
	parsed: ParsedSlash,
	ctx: SlashContext,
): SlashAction {
	const { name, args } = parsed;
	if ((UNAVAILABLE_COMMANDS as readonly string[]).includes(name))
		return { kind: "unavailable", name };
	switch (name) {
		case "plan":
			return {
				kind: "rpc",
				frame: {
					type: "set_plan_mode",
					enabled: onOff(args, ctx.planModeEnabled),
				},
			};
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
		case "clear":
			return { kind: "rpc", frame: { type: "clear_context" } };
		case "queue":
			return args
				? { kind: "rpc", frame: { type: "follow_up", message: args } }
				: { kind: "needs_args", name };
		case "btw":
			return args
				? { kind: "rpc", frame: { type: "btw", question: args } }
				: { kind: "open_btw" };
		case "tan":
			return args
				? { kind: "rpc", frame: { type: "tan", work: args } }
				: { kind: "needs_args", name };
		case "omfg":
			return args
				? { kind: "rpc", frame: { type: "omfg", complaint: args } }
				: { kind: "needs_args", name };
		case "vibe":
			return {
				kind: "rpc",
				frame: { type: "set_vibe_mode", enabled: onOff(args, ctx.vibeEnabled) },
			};
		case "loop": {
			const a = args.toLowerCase();
			if (a === "off")
				return {
					kind: "rpc",
					frame: { type: "set_loop_mode", enabled: false },
				};
			if (!args || a === "on")
				return {
					kind: "rpc",
					frame: { type: "set_loop_mode", enabled: !ctx.loopEnabled },
				};
			return {
				kind: "rpc",
				frame: { type: "set_loop_mode", enabled: true, args },
			};
		}
		case "goal": {
			const a = args.toLowerCase();
			if (!args) return { kind: "open_goal" };
			if (a === "pause" || a === "resume" || a === "drop")
				return { kind: "rpc", frame: { type: "set_goal", action: a } };
			return { kind: "rpc", frame: { type: "set_goal", objective: args } };
		}
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
					enabled: onOff(args, ctx.agentsPaused),
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
			if (first && ctx.compactModes.includes(first))
				return { kind: "prompt", text: `/compact ${args}` };
			return {
				kind: "rpc",
				frame: args
					? { type: "compact", customInstructions: args }
					: { type: "compact" },
			};
		}
		case "shake":
			return {
				kind: "rpc",
				frame: {
					type: "shake_context",
					mode: SHAKE_MODES[args.toLowerCase()] ?? "elide",
				},
			};
		case "fresh":
			return { kind: "rpc", frame: { type: "fresh" } };
		case "retry":
			return { kind: "rpc", frame: { type: "retry" } };
		case "model":
			return { kind: "open_model" };
		// 思考级别不在模型对话框里了：`/thinking <级别>` 直接设置，校验交给 omp
		case "thinking":
			return args
				? {
						kind: "rpc",
						frame: {
							type: "set_thinking_level",
							level: args.toLowerCase() as "auto",
						},
					}
				: { kind: "needs_args", name };
		// 与 omp 的 `/fast [on|ultra|off]` 一致：ultra 选 OpenAI ultrafast 档
		case "fast":
			return /^ultra(?:fast)?$/i.test(args)
				? {
						kind: "rpc",
						frame: { type: "set_ultrafast_mode", enabled: true },
					}
				: {
						kind: "rpc",
						frame: {
							type: "set_fast_mode",
							enabled: onOff(args, ctx.fastModeEnabled),
						},
					};
		case "slow":
			return {
				kind: "rpc",
				frame: {
					type: "set_slow_mode",
					enabled: onOff(args, ctx.slowModeEnabled),
				},
			};
		default: {
			const report = REPORTS[name];
			if (report) return { kind: "report", report };
			return { kind: "prompt", text: args ? `/${name} ${args}` : `/${name}` };
		}
	}
}

/** 客户端自带的命令菜单项（与 omp 公布的命令合并显示）。 */
export const CLIENT_COMMANDS = [
	"plan",
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
] as const;

export type ClientCommand = (typeof CLIENT_COMMANDS)[number];
