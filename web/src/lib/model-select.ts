/**
 * 角色 / 模型切换命令（M9.6 §7.2）。新 Core 用 Oryn 的 `oryn_select_model`：它在切换的同时往会话里写来源记录
 * （点 role = `source: "role"`，模型列表手动选 = `manual`），回合头只认这条记录（`lib/turn-role.ts`）。
 * 旧 Core（升级前建的 orb）应答 `Unknown command`：改发原来的 `set_model`（+ `set_thinking_level`），
 * 本页面之后对该线程不再试新命令——切换照常生效，只是没有来源记录、回合头一律回落。
 */
import type { OmpCommand } from "../omp/types";
import { OmpCommandError, realtime } from "../realtime/socket";
import type { RoleSlot } from "./model-roles";

/** 已探明是旧 Core 的线程；只存在内存里，刷新页面后重新探测一次。 */
const legacyThreads = new Set<string>();

/** 这些线程的 agent 不认识 Oryn 的选择命令（旧 Core）。 */
export function isLegacyCore(threadId: string): boolean {
	return legacyThreads.has(threadId);
}

/** 测试用：忘掉探测结果。 */
export function resetLegacyCore(): void {
	legacyThreads.clear();
}

/** 先试新命令；旧 Core 不认识时记下并返回 false（调用方改走旧路径），其它错误原样抛出。 */
async function trySelect(
	threadId: string,
	frame: OmpCommand,
): Promise<boolean> {
	if (legacyThreads.has(threadId)) return false;
	try {
		await realtime.cmd(threadId, frame);
		return true;
	} catch (e) {
		if (
			!(e instanceof OmpCommandError && e.message.startsWith("Unknown command"))
		)
			throw e;
		legacyThreads.add(threadId);
		return false;
	}
}

/** 点 role。新 Core：oryn_select_model{role}；旧 Core：set_model + （slot.thinkingLevel 有值时）set_thinking_level。返回是否走了旧路径。 */
export async function selectRole(
	threadId: string,
	slot: RoleSlot,
): Promise<{ legacy: boolean }> {
	if (await trySelect(threadId, { type: "oryn_select_model", role: slot.id }))
		return { legacy: false };
	const model = slot.model;
	if (!model) throw new Error(`Role has no available model: ${slot.id}`);
	await realtime.cmd(threadId, {
		type: "set_model",
		provider: model.provider,
		modelId: model.id,
	});
	if (slot.thinkingLevel)
		await realtime.cmd(threadId, {
			type: "set_thinking_level",
			level: slot.thinkingLevel as "auto",
		});
	return { legacy: true };
}

/** 模型列表里直接选。新 Core：oryn_select_model{provider, modelId}；旧 Core：set_model。 */
export async function selectModel(
	threadId: string,
	model: { provider: string; id: string },
): Promise<void> {
	const picked = await trySelect(threadId, {
		type: "oryn_select_model",
		provider: model.provider,
		modelId: model.id,
	});
	if (!picked)
		await realtime.cmd(threadId, {
			type: "set_model",
			provider: model.provider,
			modelId: model.id,
		});
}
