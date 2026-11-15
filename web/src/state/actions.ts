/**
 * 线程操作：输入区发送、UI 应答、线程管理（REST）与 omp 命令的薄封装。
 * 组件只调这里，不直接拼命令帧。
 */

import type { ParseKeys } from "i18next";
import {
	api,
	rawFetch,
	type Schemas,
	type Thread,
	unwrap,
} from "../api/client";
import type { ComposerAction } from "../lib/composer";
import { formatCount, formatDuration } from "../lib/format";
import {
	parseSlash,
	resolveSlash,
	type SlashAction,
	type SlashContext,
} from "../lib/slash";
import type {
	ExtensionUiResponse,
	ImageContent,
	OmpCommand,
	OrynPromptExtras,
	RpcLoopModeState,
	SessionState,
} from "../omp/types";
import { realtime } from "../realtime/socket";
import { getThreadStore, indexStore, type ThreadStore } from "./stores";

/** agent 是否忙：忙时新消息按 steer / 排队处理，而不是直接 prompt。 */
export function isThreadBusy(s: ThreadStore): boolean {
	return s.running || s.thread?.state === "working";
}

export type SendMode = Extract<
	ComposerAction,
	"prompt" | "steer" | "follow_up" | "abort_and_prompt"
>;

/**
 * 消息帧的附件字段：内联图片 `images`，已上传附件的 id 写入 Oryn 扩展字段 `orynAttachments`，
 * 助手文件库的文件 id（全局助手对话）写入 `orynFiles`。
 */
function promptExtras(
	images: ImageContent[],
	attachmentIds: readonly string[],
	fileIds: readonly string[],
): { images?: ImageContent[] } & OrynPromptExtras {
	const extra: { images?: ImageContent[] } & OrynPromptExtras = {};
	if (images.length > 0) extra.images = images;
	if (attachmentIds.length > 0) extra.orynAttachments = [...attachmentIds];
	if (fileIds.length > 0) extra.orynFiles = [...fileIds];
	return extra;
}

/** 按发送方式构造命令帧（附件见 `promptExtras`）。 */
export function promptFrame(
	mode: SendMode,
	message: string,
	images: ImageContent[],
	attachmentIds: readonly string[] = [],
	fileIds: readonly string[] = [],
): OmpCommand & OrynPromptExtras {
	const extra = promptExtras(images, attachmentIds, fileIds);
	switch (mode) {
		case "prompt":
			return { type: "prompt", message, ...extra };
		case "steer":
			return { type: "steer", message, ...extra };
		case "follow_up":
			return { type: "follow_up", message, ...extra };
		case "abort_and_prompt":
			return { type: "abort_and_prompt", message, ...extra };
	}
}

/**
 * 以普通消息发送一段程序生成的提示词（环境提示条的「让 agent …」）：
 * agent 忙时排队（`follow_up`），不打断当前工作。
 */
export async function sendGeneratedPrompt(threadId: string, message: string) {
	const busy = isThreadBusy(getThreadStore(threadId).getState());
	await realtime.cmd(
		threadId,
		promptFrame(busy ? "follow_up" : "prompt", message, []),
	);
}

/** 一次 setup / resume 运行的日志（`text/plain`，末尾 256KiB）。 */
export async function fetchSetupRunLog(
	orbId: string,
	runId: string,
): Promise<string> {
	const res = await rawFetch(
		`/api/v1/orbs/${encodeURIComponent(orbId)}/setup-runs/${encodeURIComponent(runId)}/log`,
	);
	return res.text();
}

export function slashContextOf(threadId: string): SlashContext {
	const s = getThreadStore(threadId).getState();
	return {
		planModeEnabled: s.state?.planModeEnabled ?? false,
		vibeEnabled: s.vibeEnabled ?? false,
		loopEnabled: s.loopMode?.enabled ?? false,
		agentsPaused: s.state?.agentsPaused ?? false,
		fastModeEnabled: s.state?.fastModeEnabled ?? false,
		slowModeEnabled: s.state?.slowModeEnabled ?? false,
		commands: s.commands ?? [],
	};
}

export function resolveSlashText(
	threadId: string,
	text: string,
): SlashAction | null {
	const parsed = parseSlash(text);
	return parsed ? resolveSlash(parsed, slashContextOf(threadId)) : null;
}

/**
 * 斜杠命令的文本路径帧：omp 只在 `prompt` 里执行 builtin，`steer` / `follow_up` 会把 `/xxx` 原样交给模型，
 * 所以无论 agent 忙不忙都发 `prompt`。builtin 照常立即执行；会变成消息的命令（skill、自定义命令）忙时按
 * `streamingBehavior` 引导，Ctrl+Enter（follow_up）时排队。
 */
export function slashPromptFrame(
	mode: SendMode,
	message: string,
	images: ImageContent[] = [],
	attachmentIds: readonly string[] = [],
	fileIds: readonly string[] = [],
): OmpCommand & OrynPromptExtras {
	return {
		type: "prompt",
		message,
		streamingBehavior: mode === "follow_up" ? "followUp" : "steer",
		...promptExtras(images, attachmentIds, fileIds),
	};
}

/** 与 plan_approval 一样的长超时：大会话的压缩 / 交接、等用户在对话框里选的 omfg、引导访谈都可能远超默认 120s。 */
export const LONG_COMMAND_TIMEOUT_MS = 600_000;

const LONG_COMMANDS: Record<string, true> = {
	compact: true,
	handoff: true,
	omfg: true,
	guided_goal: true,
	// 文本路径的 builtin（如 `/compact <模式>`、`/mcp reauth`）在应答前同步执行完
	prompt: true,
};

/** 这条命令的应答超时（未列出的走 realtime 的默认值）。 */
export function commandTimeout(frame: OmpCommand): number | undefined {
	return LONG_COMMANDS[frame.type] ? LONG_COMMAND_TIMEOUT_MS : undefined;
}

/** 斜杠命令应答的本地提示：i18n 键（`slash.result.*`）+ 参数；`whyKey` 是填进 `{{why}}` 的原因说明。 */
export interface SlashResultNotice {
	level: "info" | "warning";
	key: ParseKeys;
	params?: Record<string, string | number>;
	whyKey?: ParseKeys;
}

/** 控制面没写入中央配置的原因（C5 `data.oryn.reason`）；不认识的按 `store_failed` 说明。 */
const OMFG_SYNC_WHY: Record<string, ParseKeys> = {
	no_project: "slash.result.omfgWhy.no_project",
	unsupported_path: "slash.result.omfgWhy.unsupported_path",
	scope_unconfirmed: "slash.result.omfgWhy.scope_unconfirmed",
	orb_read_failed: "slash.result.omfgWhy.orb_read_failed",
	too_large: "slash.result.omfgWhy.too_large",
	invalid_content: "slash.result.omfgWhy.invalid_content",
	store_failed: "slash.result.omfgWhy.store_failed",
	conflict: "slash.result.omfgWhy.conflict",
	content_mismatch: "slash.result.omfgWhy.content_mismatch",
};

/**
 * 把命令应答变成一句给人看的提示；不需要提示的命令（结果在别处已经可见，如模式开关、压缩卡片）返回 null。
 * 命令本身失败时 realtime.cmd 抛错，错误留在输入区，不经这里。
 */
export function describeSlashResult(
	frame: OmpCommand,
	data: unknown,
): SlashResultNotice | null {
	const d = isObject(data) ? data : {};
	const str = (v: unknown) => (typeof v === "string" && v ? v : undefined);
	switch (frame.type) {
		case "shake_context": {
			const summary = str(d.removed);
			return summary
				? { level: "info", key: "slash.result.shake", params: { summary } }
				: null;
		}
		case "retry":
			return d.retried === true
				? { level: "info", key: "slash.result.retried" }
				: { level: "warning", key: "slash.result.nothingToRetry" };
		case "handoff": {
			if (data === null)
				return { level: "warning", key: "slash.result.handoffCancelled" };
			const path = str(d.savedPath);
			return path
				? { level: "info", key: "slash.result.handoffSaved", params: { path } }
				: { level: "info", key: "slash.result.handoff" };
		}
		case "tan": {
			const id = str(d.jobId);
			return id
				? { level: "info", key: "slash.result.tan", params: { id } }
				: null;
		}
		case "omfg": {
			if (d.state === "saved") {
				const path = str(d.savedPath);
				// 控制面把规则写回中央配置的结果（M12.2）；没有 `oryn` = 没有尝试同步（runner 线程等），照旧提示
				if (isObject(d.oryn)) {
					const o = d.oryn;
					if (o.synced === true)
						return {
							level: "info",
							key:
								o.scope === "project"
									? "slash.result.omfgSyncedProject"
									: "slash.result.omfgSyncedGlobal",
							params: { path: str(o.path) ?? path ?? "" },
						};
					const reason = str(o.reason);
					return {
						level: "warning",
						key: "slash.result.omfgLocalOnly",
						params: { path: path ?? "" },
						whyKey:
							(reason && Object.hasOwn(OMFG_SYNC_WHY, reason)
								? OMFG_SYNC_WHY[reason]
								: undefined) ?? "slash.result.omfgWhy.store_failed",
					};
				}
				return path
					? { level: "info", key: "slash.result.omfgSaved", params: { path } }
					: { level: "info", key: "slash.result.omfgSavedNoPath" };
			}
			if (d.state === "rejected")
				return { level: "warning", key: "slash.result.omfgRejected" };
			if (d.state === "aborted")
				return { level: "warning", key: "slash.result.omfgAborted" };
			return null;
		}
		case "clear_context":
			return typeof d.droppedCount === "number"
				? {
						level: "info",
						key: "slash.result.cleared",
						params: { count: d.droppedCount },
					}
				: { level: "info", key: "slash.result.clearedNoCount" };
		case "fresh":
			return { level: "info", key: "slash.result.fresh" };
		case "guided_goal":
			return { level: "info", key: "slash.result.guidedGoal" };
		case "set_agents_paused": {
			if (d.paused === true)
				return { level: "info", key: "slash.result.paused" };
			const held = typeof d.heldMs === "number" ? formatDuration(d.heldMs) : "";
			return held
				? {
						level: "info",
						key: "slash.result.resumedAfter",
						params: { duration: held },
					}
				: { level: "info", key: "slash.result.resumed" };
		}
		case "set_goal":
			if (frame.tokenBudget === null)
				return { level: "info", key: "slash.result.goalBudgetCleared" };
			if (typeof frame.tokenBudget === "number" && !frame.objective)
				return {
					level: "info",
					key: "slash.result.goalBudgetSet",
					params: { n: formatCount(frame.tokenBudget) },
				};
			return null;
		default:
			return null;
	}
}

/** 在转录里插一条本地提示（与 omp 的 `notice` 帧同一条渲染路径，`source` 标明来自客户端）。 */
export function pushLocalNotice(
	threadId: string,
	level: "info" | "warning" | "error",
	message: string,
) {
	getThreadStore(threadId)
		.getState()
		.apply([
			{
				type: "frame",
				frame: { type: "notice", level, message, source: "oryn-slash" },
			},
		]);
}

/** 应答一个 extension UI 请求：发给控制面（先到先得），本地立即关闭对话框。 */
export function answerUi(
	threadId: string,
	response: ExtensionUiResponse,
): boolean {
	const ok = realtime.uiResponse(threadId, response);
	if (ok)
		getThreadStore(threadId)
			.getState()
			.apply([{ type: "ui.resolved", requestId: response.id }]);
	return ok;
}

/**
 * REST 应答里只取本次 PATCH 改动的元数据字段并入当前线程：状态、叶子等由 WS 推送维护，
 * 请求期间若已收到更新的 WS 摘要，不能被这份可能更旧的快照回滚。
 */
export function mergePatchedThread(
	current: Thread | undefined,
	patch: Schemas["ThreadPatch"],
	res: Thread,
): Thread {
	if (!current) return res;
	const next: Thread = { ...current };
	if ("title" in patch) {
		next.title = res.title;
		next.titleSource = res.titleSource;
	}
	if ("color" in patch) next.color = res.color;
	if ("pinned" in patch) next.pinned = res.pinned;
	if ("labels" in patch) next.labels = res.labels;
	if ("archived" in patch) next.archivedAt = res.archivedAt;
	if ("snoozedUntil" in patch) next.snoozedUntil = res.snoozedUntil;
	if ("unread" in patch) next.unread = res.unread;
	if ("spaceId" in patch) next.spaceId = res.spaceId;
	if ("shipped" in patch) {
		next.shippedAt = res.shippedAt;
		next.archivedAt = res.archivedAt;
	}
	return next;
}

export async function patchThread(
	threadId: string,
	patch: Schemas["ThreadPatch"],
) {
	const res = await unwrap(
		api.PATCH("/api/v1/threads/{id}", {
			params: { path: { id: threadId } },
			body: patch,
		}),
	);
	// 合并时取当下最新的摘要（await 期间可能已被 WS 更新）
	const current =
		indexStore.getState().threads[threadId] ??
		getThreadStore(threadId).getState().thread;
	const thread = mergePatchedThread(current, patch, res);
	indexStore.getState().upsertThread(thread);
	getThreadStore(threadId)
		.getState()
		.apply([{ type: "thread", thread }]);
	return thread;
}

/** 结果会改变模式开关状态、需要随后对账的命令。 */
const MODE_COMMANDS: Record<string, true> = {
	set_plan_mode: true,
	set_vibe_mode: true,
	set_loop_mode: true,
	set_goal: true,
	guided_goal: true,
	set_agents_paused: true,
	set_fast_mode: true,
	set_slow_mode: true,
	set_prewalk: true,
};

export function isModeCommand(frame: OmpCommand): boolean {
	return MODE_COMMANDS[frame.type] === true;
}

/**
 * 发送一条模式类命令（计划 / vibe / loop / goal / 暂停等），并把结果并入线程状态：
 * `set_vibe_mode` / `set_loop_mode` 的应答直接写回，随后刷新 `get_state`。模式条与斜杠命令共用。
 */
export async function runModeCommand(threadId: string, frame: OmpCommand) {
	const data: unknown = await realtime.cmd(
		threadId,
		frame,
		commandTimeout(frame),
	);
	const store = getThreadStore(threadId);
	if (frame.type === "set_vibe_mode" && isObject(data) && "enabled" in data)
		store.getState().patchLocal({ vibeEnabled: data.enabled === true });
	if (frame.type === "set_loop_mode" && isObject(data) && "enabled" in data)
		store
			.getState()
			.patchLocal({ loopMode: data as unknown as RpcLoopModeState });
	const state = (await realtime.cmd(threadId, {
		type: "get_state",
	})) as SessionState;
	store.getState().apply([{ type: "state", state }]);
	return data;
}

function isObject(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null;
}

/** 改名：PATCH 标题并同步给 agent（`set_session_name`）。 */
export async function renameThread(threadId: string, title: string) {
	const thread = await patchThread(threadId, { title });
	await realtime
		.cmd(threadId, { type: "set_session_name", name: title })
		.catch(() => undefined);
	return thread;
}

export async function deleteThread(threadId: string) {
	await unwrap(
		api.DELETE("/api/v1/threads/{id}", { params: { path: { id: threadId } } }),
	);
	indexStore.getState().removeThread(threadId);
}

export async function forkThread(
	threadId: string,
	env: "copy" | "share",
	entryId?: string,
) {
	const body: Schemas["ForkThreadRequest"] = entryId
		? { env, entryId }
		: { env };
	const res = await unwrap(
		api.POST("/api/v1/threads/{id}/fork", {
			params: { path: { id: threadId } },
			body,
		}),
	);
	indexStore.getState().upsertThread(res.thread);
	return res.thread;
}

export async function wakeThread(threadId: string) {
	await unwrap(
		api.POST("/api/v1/threads/{id}/wake", {
			params: { path: { id: threadId } },
		}),
	);
}

export async function pauseOrb(orbId: string, mode: "freeze" | "stop") {
	indexStore.getState().upsertOrb(
		await unwrap(
			api.POST("/api/v1/orbs/{id}/pause", {
				params: { path: { id: orbId } },
				body: { mode },
			}),
		),
	);
}

export async function resumeOrb(orbId: string) {
	indexStore.getState().upsertOrb(
		await unwrap(
			api.POST("/api/v1/orbs/{id}/resume", {
				params: { path: { id: orbId } },
			}),
		),
	);
}

export async function deleteOrb(orbId: string) {
	indexStore
		.getState()
		.upsertOrb(
			await unwrap(
				api.DELETE("/api/v1/orbs/{id}", { params: { path: { id: orbId } } }),
			),
		);
}

export async function createThread(body: Schemas["CreateThreadRequest"]) {
	const res = await unwrap(api.POST("/api/v1/threads", { body }));
	indexStore.getState().upsertThread(res.thread);
	return res.thread;
}
