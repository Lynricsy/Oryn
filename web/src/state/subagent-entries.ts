/**
 * 从线程条目里取子 agent 的会话：控制面把主会话产物目录下的 `*.jsonl`（Core 把子 agent 会话写成
 * `<产物目录>/<id>.jsonl`，嵌套的写在父 agent 的产物子目录里，id 形如 `0-Scout.1-Worker`）一并入库，
 * `file` 是相对会话目录的路径，含 `/` 的即子 agent 文件。agent 不在线（orb 挂起）时据此离线展示。
 *
 * 存档的身份是完整 `file`，不是 id：同一线程的不同主会话各自从 `0-…` 编号，同名文件可以出现在不同目录。
 * 顾问转录（`__advisor.jsonl` / `__advisor.<slug>.jsonl`，主会话与每个子 agent 目录各一份）不是 task 子 agent，
 * 同 Core `registry-helpers` 一样排除。
 */
import type { Entry } from "../api/client";
import { isRecord } from "../tool-render/util";
import type { JsonRecord } from "./message-builder";

export interface SubagentFile {
	/** Core 子 agent id（文件名去掉 `.jsonl`）；RPC 控制用，不同主会话可能重名。 */
	id: string;
	/** 存档标识：相对会话目录的完整路径。 */
	file: string;
	agent?: string;
	/** `session_init.task`：完整任务正文，不作单行标签。 */
	task?: string;
	/** 已持久化的会话标题：最新 `title_change.title`，没有时取会话头的 `title`。 */
	title?: string;
}

/** Core `ADVISOR_TRANSCRIPT_STEM`。 */
const ADVISOR_STEM = "__advisor";

/** 文件名（路径末段）去掉 `.jsonl`。 */
function fileId(file: string): string {
	const base = file.slice(file.lastIndexOf("/") + 1);
	return base.endsWith(".jsonl") ? base.slice(0, -".jsonl".length) : base;
}

/**
 * 子 agent 会话文件，按首次出现的顺序。`agent` / `task` 取自该文件的 `session_init`；
 * `title` 取最新的 `title_change`（标题晚于初始化到达也能更新），没有时用会话头 `session` 条目的 `title`。
 */
export function subagentFiles(entries: readonly Entry[]): SubagentFile[] {
	const byFile = new Map<
		string,
		{
			row: SubagentFile;
			header?: string;
			changed?: { seq: number; title: string };
		}
	>();
	for (const e of entries) {
		if (!e.file.includes("/")) continue;
		let slot = byFile.get(e.file);
		if (!slot) {
			const id = fileId(e.file);
			if (id === ADVISOR_STEM || id.startsWith(`${ADVISOR_STEM}.`)) continue;
			slot = { row: { id, file: e.file } };
			byFile.set(e.file, slot);
		}
		if (!isRecord(e.payload)) continue;
		const p = e.payload;
		if (e.type === "session_init") {
			if (typeof p.agent === "string") slot.row.agent = p.agent;
			if (typeof p.task === "string") slot.row.task = p.task;
		} else if (e.type === "session") {
			if (typeof p.title === "string" && p.title) slot.header = p.title;
		} else if (e.type === "title_change") {
			if (
				typeof p.title === "string" &&
				p.title &&
				(!slot.changed || e.seq > slot.changed.seq)
			)
				slot.changed = { seq: e.seq, title: p.title };
		}
	}
	return [...byFile.values()].map(({ row, header, changed }) => {
		const title = changed?.title ?? header;
		return title ? { ...row, title } : row;
	});
}

/**
 * 选一个子 agent 的存档：给了 `file` 精确匹配；只有 id（实时子 agent、转录里的 task 卡片）时取该 id
 * 最后出现的存档——当前主会话的子 agent 最晚入库，较早主会话的同名存档不会被它顶替。
 */
export function subagentFileOf(
	files: readonly SubagentFile[],
	id: string,
	file?: string,
): SubagentFile | undefined {
	return file === undefined
		? files.findLast((f) => f.id === id)
		: files.find((f) => f.file === file);
}

/**
 * 子 agent 的单行标签（列表行与对话框标题共用）：实时 description 优先，其次已持久化的会话标题；
 * 都没有时为 undefined，由调用方回退到 id——完整 task 正文不作标签。
 */
export function subagentLabel(
	description: string | undefined,
	saved: SubagentFile | undefined,
): string | undefined {
	return description || saved?.title;
}

/** 存档 `file`（完整路径，精确匹配）的已入库消息，按 `seq` 升序。 */
export function subagentMessages(
	entries: readonly Entry[],
	file: string,
): JsonRecord[] {
	return entries
		.filter((e) => e.type === "message" && e.file === file)
		.sort((a, b) => a.seq - b.seq)
		.flatMap((e) =>
			isRecord(e.payload) && isRecord(e.payload.message)
				? [e.payload.message]
				: [],
		);
}
