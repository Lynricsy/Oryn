/**
 * 从线程条目里取子 agent 的会话：控制面把主会话产物目录下的 `*.jsonl`（Core 把子 agent 会话写成
 * `<产物目录>/<id>.jsonl`，嵌套的写在父 agent 的产物子目录里，id 形如 `0-Scout.1-Worker`）一并入库，
 * `file` 是相对会话目录的路径，含 `/` 的即子 agent 文件。agent 不在线（orb 挂起）时据此离线展示。
 */
import type { Entry } from "../api/client";
import { isRecord } from "../tool-render/util";
import type { JsonRecord } from "./message-builder";

export interface SubagentFile {
	id: string;
	file: string;
	agent?: string;
	task?: string;
}

/** 文件名（路径末段）去掉 `.jsonl`。 */
function fileId(file: string): string {
	const base = file.slice(file.lastIndexOf("/") + 1);
	return base.endsWith(".jsonl") ? base.slice(0, -".jsonl".length) : base;
}

/** 子 agent 会话文件，按首次出现的顺序；`agent` / `task` 取自该文件的 `session_init` 条目。 */
export function subagentFiles(entries: readonly Entry[]): SubagentFile[] {
	const byFile = new Map<string, SubagentFile>();
	for (const e of entries) {
		if (!e.file.includes("/")) continue;
		let row = byFile.get(e.file);
		if (!row) {
			row = { id: fileId(e.file), file: e.file };
			byFile.set(e.file, row);
		}
		if (e.type === "session_init" && isRecord(e.payload)) {
			const { agent, task } = e.payload;
			if (typeof agent === "string") row.agent = agent;
			if (typeof task === "string") row.task = task;
		}
	}
	return [...byFile.values()];
}

/** 子 agent `id` 的已入库消息（文件名为 `<id>.jsonl` 的 `message` 条目），按 `seq` 升序。 */
export function subagentMessages(
	entries: readonly Entry[],
	id: string,
): JsonRecord[] {
	const suffix = `/${id}.jsonl`;
	return entries
		.filter((e) => e.type === "message" && e.file.endsWith(suffix))
		.sort((a, b) => a.seq - b.seq)
		.flatMap((e) =>
			isRecord(e.payload) && isRecord(e.payload.message)
				? [e.payload.message]
				: [],
		);
}
