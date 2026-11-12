/**
 * 由条目 + 叠加层推导出转录的渲染项（活动分支）。
 *
 * - 主会话文件 = `file` 不含 `/` 的 JSONL；子 agent 转录在子目录里，不进主转录。
 * - 一个线程可先后拥有多个主会话文件，按首次出现顺序拼接，文件之间插入「新会话」分隔。
 * - 含叶子指针的文件沿 `parentId` 从叶子回溯到根得到活动分支；其它文件以其最后一条为叶子。
 * - 叠加层只渲染尚未被条目覆盖的在途消息（`(role,timestamp)`）与运行中工具（`toolCallId`）。
 */
import type { Entry } from "../api/client";
import { isRecord } from "../tool-render/util";
import type { JsonRecord } from "./message-builder";
import type { LiveTool, Notice, ThreadViewState } from "./thread-reducer";
import { entryCoversMessage, toolResultIdOf } from "./thread-reducer";

export type TranscriptItem =
	| { kind: "session_divider"; key: string; file: string; seq: number }
	| {
			kind: "message";
			key: string;
			seq: number;
			entryId?: string;
			message: JsonRecord;
			live: boolean;
	  }
	| {
			kind: "tool";
			key: string;
			seq: number;
			toolCallId: string;
			toolName: string;
	  }
	| { kind: "compaction"; key: string; seq: number; entry: JsonRecord }
	| { kind: "branch_summary"; key: string; seq: number; entry: JsonRecord }
	| { kind: "custom_message"; key: string; seq: number; entry: JsonRecord }
	| { kind: "meta"; key: string; seq: number; entry: JsonRecord }
	| { kind: "notice"; key: string; seq: number; notice: Notice };

const SYNTHETIC_IDS: Record<string, true> = {
	__title__: true,
	__session__: true,
};

/** 渲染时不显示的条目类型（标题槽、会话头、标签、用量等）。 */
const HIDDEN_ENTRY_TYPES: Record<string, true> = {
	title: true,
	session: true,
	label: true,
	model_usage: true,
	credential_pin: true,
	session_init: true,
	reset_boundary: true,
	custom: true,
	service_tier_change: true,
};

/** 以元信息行显示的条目类型。 */
const META_ENTRY_TYPES: Record<string, true> = {
	model_change: true,
	thinking_level_change: true,
	mode_change: true,
	ttsr_injection: true,
	title_change: true,
};

export function isMainFile(file: string): boolean {
	return !file.includes("/");
}

/** 按主会话文件分组（保持首次出现顺序）。 */
export function groupMainFiles(
	entries: readonly Entry[],
): Map<string, Entry[]> {
	const files = new Map<string, Entry[]>();
	for (const e of entries) {
		if (!isMainFile(e.file)) continue;
		let list = files.get(e.file);
		if (!list) {
			list = [];
			files.set(e.file, list);
		}
		list.push(e);
	}
	return files;
}

/** 沿 parentId 从 leaf 回溯，得到根 → 叶子的路径。 */
export function branchPath(
	list: readonly Entry[],
	leafId: string | undefined,
): Entry[] {
	const byId = new Map<string, Entry>();
	for (const e of list) if (!SYNTHETIC_IDS[e.entryId]) byId.set(e.entryId, e);
	let leaf = leafId ? byId.get(leafId) : undefined;
	if (!leaf) {
		// 与 omp 加载器一致：叶子 = 最后一条；写在活动分支之外的条目（带 `activeLeafId`，如迟到的
		// model_usage）不移动叶子，而是指回它写入时的活动叶子
		for (let i = list.length - 1; i >= 0; i--) {
			const e = list[i];
			if (e && !SYNTHETIC_IDS[e.entryId]) {
				const p = isRecord(e.payload) ? e.payload : undefined;
				leaf =
					p && "activeLeafId" in p
						? typeof p.activeLeafId === "string"
							? byId.get(p.activeLeafId)
							: undefined
						: e;
				break;
			}
		}
	}
	const path: Entry[] = [];
	const seen = new Set<string>();
	let cur = leaf;
	while (cur && !seen.has(cur.entryId)) {
		seen.add(cur.entryId);
		path.push(cur);
		cur = cur.parentId ? byId.get(cur.parentId) : undefined;
	}
	return path.reverse();
}

/** 活动分支上的主会话条目（跨文件按顺序拼接）。 */
export function activeBranch(
	entries: readonly Entry[],
	leafEntryId: string | undefined,
): Entry[][] {
	const files = groupMainFiles(entries);
	const out: Entry[][] = [];
	for (const list of files.values()) {
		const hasLeaf =
			leafEntryId !== undefined && list.some((e) => e.entryId === leafEntryId);
		out.push(branchPath(list, hasLeaf ? leafEntryId : undefined));
	}
	return out;
}

function messageItem(e: Entry, message: JsonRecord): TranscriptItem | null {
	const role = message.role;
	if (role === "toolResult") return null; // 在对应 assistant 的工具卡片里渲染
	if (
		(role === "custom" || role === "hookMessage") &&
		message.display === false
	)
		return null;
	return {
		kind: "message",
		key: `e${e.seq}`,
		seq: e.seq,
		entryId: e.entryId,
		message,
		live: false,
	};
}

function entryItem(e: Entry): TranscriptItem | null {
	const p = isRecord(e.payload) ? e.payload : {};
	if (HIDDEN_ENTRY_TYPES[e.type]) return null;
	if (e.type === "message")
		return isRecord(p.message) ? messageItem(e, p.message) : null;
	if (e.type === "compaction")
		return { kind: "compaction", key: `e${e.seq}`, seq: e.seq, entry: p };
	if (e.type === "branch_summary")
		return { kind: "branch_summary", key: `e${e.seq}`, seq: e.seq, entry: p };
	if (e.type === "custom_message") {
		return p.display === false
			? null
			: { kind: "custom_message", key: `e${e.seq}`, seq: e.seq, entry: p };
	}
	if (META_ENTRY_TYPES[e.type])
		return { kind: "meta", key: `e${e.seq}`, seq: e.seq, entry: p };
	return null;
}

/** 主会话里所有 toolResult 条目，按 toolCallId 索引。 */
export function toolResultsOf(
	entries: readonly Entry[],
): Map<string, JsonRecord> {
	const map = new Map<string, JsonRecord>();
	for (const e of entries) {
		const id = toolResultIdOf(e);
		if (id && isRecord(e.payload) && isRecord(e.payload.message))
			map.set(id, e.payload.message);
	}
	return map;
}

function toolCallIdsOf(message: JsonRecord, into: Set<string>) {
	if (!Array.isArray(message.content)) return;
	for (const b of message.content) {
		if (isRecord(b) && b.type === "toolCall" && typeof b.id === "string")
			into.add(b.id);
	}
}

export interface TranscriptModel {
	items: TranscriptItem[];
	toolResults: Map<string, JsonRecord>;
	liveTools: Record<string, LiveTool>;
}

export function buildTranscript(
	s: Pick<
		ThreadViewState,
		| "entries"
		| "leafEntryId"
		| "liveMessage"
		| "liveTools"
		| "liveToolOrder"
		| "notices"
	>,
): TranscriptModel {
	const items: TranscriptItem[] = [];
	const branches = activeBranch(s.entries, s.leafEntryId);
	const rendered = branches.filter((b) => b.length > 0);
	rendered.forEach((branch, i) => {
		const first = branch[0];
		if (i > 0 && first) {
			items.push({
				kind: "session_divider",
				key: `d${first.seq}`,
				file: first.file,
				seq: first.seq - 0.5,
			});
		}
		for (const e of branch) {
			const item = entryItem(e);
			if (item) items.push(item);
		}
	});

	const toolResults = toolResultsOf(s.entries);
	const calledIds = new Set<string>();
	for (const it of items)
		if (it.kind === "message") toolCallIdsOf(it.message, calledIds);

	const lastSeq =
		s.entries.length > 0 ? (s.entries[s.entries.length - 1]?.seq ?? 0) : 0;
	const live = s.liveMessage;
	if (live && !s.entries.some((e) => entryCoversMessage(e, live))) {
		items.push({
			kind: "message",
			key: `live-${live.messageId ?? live.timestamp ?? "m"}`,
			seq: lastSeq + 0.6,
			message: live.message,
			live: !live.ended,
		});
		toolCallIdsOf(live.message, calledIds);
	}
	for (const id of s.liveToolOrder) {
		const tool = s.liveTools[id];
		if (!tool || toolResults.has(id) || calledIds.has(id)) continue;
		items.push({
			kind: "tool",
			key: `lt-${id}`,
			seq: lastSeq + 0.7,
			toolCallId: id,
			toolName: tool.toolName,
		});
	}

	// 内联提示按到达时的 lastSeq 插入
	if (s.notices.length > 0) {
		const out: TranscriptItem[] = [];
		let ni = 0;
		const notices = s.notices;
		for (const it of items) {
			while (
				ni < notices.length &&
				(notices[ni]?.afterSeq ?? 0) < it.seq &&
				it.seq <= lastSeq
			) {
				const n = notices[ni] as Notice;
				out.push({
					kind: "notice",
					key: n.key,
					seq: n.afterSeq + 0.1,
					notice: n,
				});
				ni++;
			}
			out.push(it);
		}
		for (; ni < notices.length; ni++) {
			const n = notices[ni] as Notice;
			out.push({
				kind: "notice",
				key: n.key,
				seq: n.afterSeq + 0.1,
				notice: n,
			});
		}
		return { items: out, toolResults, liveTools: s.liveTools };
	}
	return { items, toolResults, liveTools: s.liveTools };
}

/** 本线程全部 assistant 消息的成本合计（美元）。 */
export function threadCost(entries: readonly Entry[]): number {
	let total = 0;
	for (const e of entries) {
		if (
			e.type !== "message" ||
			!isRecord(e.payload) ||
			!isRecord(e.payload.message)
		)
			continue;
		const usage = e.payload.message.usage;
		if (
			isRecord(usage) &&
			isRecord(usage.cost) &&
			typeof usage.cost.total === "number"
		)
			total += usage.cost.total;
	}
	return total;
}
