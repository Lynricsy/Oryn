/**
 * 线程操作：输入区发送、UI 应答、线程管理（REST）与 omp 命令的薄封装。
 * 组件只调这里，不直接拼命令帧。
 */
import {
	api,
	rawFetch,
	type Schemas,
	type Thread,
	unwrap,
} from "../api/client";
import type { ComposerAction } from "../lib/composer";
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

/** 按发送方式构造命令帧；attachmentIds 写入 Oryn 扩展字段 `orynAttachments`。 */
export function promptFrame(
	mode: SendMode,
	message: string,
	images: ImageContent[],
	attachmentIds: readonly string[] = [],
): OmpCommand & OrynPromptExtras {
	const extra: { images?: ImageContent[] } & OrynPromptExtras = {};
	if (images.length > 0) extra.images = images;
	if (attachmentIds.length > 0) extra.orynAttachments = [...attachmentIds];
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
	const compact = s.commands?.find((c) => c.name === "compact");
	return {
		planModeEnabled: s.state?.planModeEnabled ?? false,
		vibeEnabled: s.vibeEnabled ?? false,
		loopEnabled: s.loopMode?.enabled ?? false,
		agentsPaused: s.state?.agentsPaused ?? false,
		fastModeEnabled: s.state?.fastModeEnabled ?? false,
		slowModeEnabled: s.state?.slowModeEnabled ?? false,
		compactModes: compact?.subcommands?.map((c) => c.name) ?? [],
	};
}

export function resolveSlashText(
	threadId: string,
	text: string,
): SlashAction | null {
	const parsed = parseSlash(text);
	return parsed ? resolveSlash(parsed, slashContextOf(threadId)) : null;
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
	const data: unknown = await realtime.cmd(threadId, frame);
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
