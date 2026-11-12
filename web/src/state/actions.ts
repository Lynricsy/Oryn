/**
 * 线程操作：输入区发送、UI 应答、线程管理（REST）与 omp 命令的薄封装。
 * 组件只调这里，不直接拼命令帧。
 */
import { api, type Schemas, unwrap } from "../api/client";
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
} from "../omp/types";
import { realtime } from "../realtime/socket";
import { getThreadStore, indexStore } from "./stores";

export type SendMode = Extract<
	ComposerAction,
	"prompt" | "steer" | "follow_up" | "abort_and_prompt"
>;

/** 按发送方式构造命令帧。 */
export function promptFrame(
	mode: SendMode,
	message: string,
	images: ImageContent[],
): OmpCommand {
	const imgs = images.length > 0 ? { images } : {};
	switch (mode) {
		case "prompt":
			return { type: "prompt", message, ...imgs };
		case "steer":
			return { type: "steer", message, ...imgs };
		case "follow_up":
			return { type: "follow_up", message, ...imgs };
		case "abort_and_prompt":
			return { type: "abort_and_prompt", message, ...imgs };
	}
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

export async function patchThread(
	threadId: string,
	patch: Schemas["ThreadPatch"],
) {
	const thread = await unwrap(
		api.PATCH("/api/v1/threads/{id}", {
			params: { path: { id: threadId } },
			body: patch,
		}),
	);
	indexStore.getState().upsertThread(thread);
	getThreadStore(threadId)
		.getState()
		.apply([{ type: "thread", thread }]);
	return thread;
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
