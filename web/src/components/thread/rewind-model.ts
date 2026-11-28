/**
 * 原位 Rewind（回退）纯数据模型：与 omp 原生 `RewindSelectorComponent` / `appendOutlineEntries` 语义对齐。
 *
 * - 只处理当前（最后一个）主会话文件里的条目（Core 的 `switch_leaf` 只能切当前会话文件）。
 * - 每条渲染出来的消息条目（用户消息、助手回复、工具调用回合、可见 custom_message）都是可选的 rewind 目标；
 *   紧跟在助手消息后面的 `toolResult` 折叠进所属目标，并把 `targetEntryId` 推进到最后一个 `toolResult` 的条目 id，
 *   保证回退到工具调用时保留完整工具输出、不破坏 `toolCall` / `toolResult` 配对。
 * - 支持多词过滤（拉丁词整词匹配，CJK 等子串匹配，与 omp `#compileFilter` 一致）与同分叉点的兄弟分支发现。
 */
import type { Entry } from "../../api/client";
import { parseAttachmentBlock } from "../../lib/attachments";
import { parseDraftEditsBlock } from "../../lib/draft-edits";
import { parseOrynMessage } from "../../lib/oryn-message";
import { parseViewReceipt } from "../../lib/view/answer";
import type { JsonRecord } from "../../state/message-builder";
import { branchPath, groupMainFiles } from "../../state/transcript";
import { isRecord } from "../../tool-render/util";

export type RewindTargetKind = "user" | "assistant" | "tool" | "custom";

export interface RewindTarget {
	/** 对应 `TranscriptItem.key`（`e${seq}`），用于原位高亮与定位滚动。 */
	rowKey: string;
	/** 该渲染块对应的首条条目 id（未折叠前的 user / assistant / custom_message id），用于树查询与分叉点识别。 */
	anchorEntryId: string;
	/** 提交给 `switch_leaf` 的条目 id：若有紧跟的 `toolResult`，推进到最后一个 `toolResult` 的条目 id。 */
	targetEntryId: string;
	/** 该目标涵盖的全部条目 id（`anchorEntryId` 加上折叠进来的 `toolResult` 条目 id），用于 `startAt` 定位。 */
	entryIds: string[];
	/** 真实用户提问（排除自动投递、IRC 转述与合成消息），驱动 `←/→` 在用户回合间跳转。 */
	isUserTurn: boolean;
	/**
	 * `switch_leaf` 是否会退到该条目之前并把原文放回输入框（与 Core `agent-session.ts` 一致：
	 * `role === "user"` 或非 `skill-prompt` 的 `custom_message`）。
	 */
	rewindsBefore: boolean;
	kind: RewindTargetKind;
	/** 单行摘要（无障碍读屏与分支标签用）。 */
	preview: string;
	/** 小写化检索全文（正文、思考、工具名、参数、工具结果输出），供 `f` 过滤匹配。 */
	searchText: string;
	/** `anchorEntryId` 在会话树里的父节点 id（根为 null）。 */
	parentId: string | null;
}

export interface SiblingBranch {
	/** 该分支在分叉点下的首条条目 id（树子节点 id）。 */
	rootEntryId: string;
	/** 该分支上的首条可 rewind 目标的 `anchorEntryId`。 */
	firstTargetId: string;
	/** 沿该分支最新子节点链走到底的叶子 id（传给 `buildTranscript` 预览该分支）。 */
	leafEntryId: string;
	/** 分支标签（取该分支首条可见目标的预览文本）。 */
	label: string;
	/** 是否为当前正在浏览的分支。 */
	isCurrent: boolean;
}

const SYNTHETIC_IDS: Record<string, true> = {
	__title__: true,
	__session__: true,
};

/** 抽取消息 content 里的纯文本（跳过思考与工具参数）。 */
function plainTextOfContent(content: unknown): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	const parts: string[] = [];
	for (const b of content) {
		if (isRecord(b) && b.type === "text" && typeof b.text === "string")
			parts.push(b.text);
	}
	return parts.join("\n");
}

/** 抽取助手消息可见性与检索文本（正文、思考、工具调用名/意图/参数、错误）。 */
function inspectAssistant(msg: JsonRecord): {
	visible: boolean;
	hasToolCall: boolean;
	preview: string;
	searchParts: string[];
} {
	const content = Array.isArray(msg.content) ? msg.content : [];
	let visible = false;
	let hasToolCall = false;
	let preview = "";
	const searchParts: string[] = [];
	for (const b of content) {
		if (!isRecord(b)) continue;
		switch (b.type) {
			case "text":
				if (typeof b.text === "string" && b.text) {
					visible = true;
					searchParts.push(b.text);
					if (!preview) preview = oneLine(b.text);
				}
				break;
			case "thinking":
				if (typeof b.thinking === "string" && b.thinking) {
					visible = true;
					searchParts.push(b.thinking);
				}
				break;
			case "redactedThinking":
				visible = true;
				break;
			case "image":
				visible = true;
				break;
			case "toolCall": {
				visible = true;
				hasToolCall = true;
				const name = typeof b.name === "string" ? b.name : "tool";
				const intent = typeof b.intent === "string" ? b.intent : "";
				searchParts.push(name);
				if (intent) searchParts.push(intent);
				if (b.arguments !== undefined) {
					try {
						searchParts.push(
							typeof b.arguments === "string"
								? b.arguments
								: JSON.stringify(b.arguments),
						);
					} catch {
						// 忽略不可序列化的异常参数
					}
				}
				if (!preview) preview = intent || name;
				break;
			}
		}
	}
	const stop = typeof msg.stopReason === "string" ? msg.stopReason : "";
	if (stop === "error" || stop === "aborted") {
		visible = true;
		if (typeof msg.errorMessage === "string" && msg.errorMessage) {
			searchParts.push(msg.errorMessage);
			if (!preview) preview = oneLine(msg.errorMessage);
		}
	}
	return { visible, hasToolCall, preview, searchParts };
}

function oneLine(text: string, limit = 80): string {
	const flat = text.replace(/\s+/g, " ").trim();
	return flat.length > limit ? `${flat.slice(0, limit)}…` : flat;
}

/** 主人自己写的消息：IRC 转述、合成消息、自动消息（`[Oryn `）与视图回执（M33）都不算，不能「回到这里编辑」。 */
function isRealUserMessage(msg: JsonRecord): boolean {
	if (msg.role !== "user") return false;
	if (msg.synthetic === true || isRecord(msg.parentIrc)) return false;
	const { text } = parseDraftEditsBlock(
		parseAttachmentBlock(plainTextOfContent(msg.content)).text,
	);
	return parseOrynMessage(text) === null && parseViewReceipt(text) === null;
}

/** 当前（最后一个）主会话文件的全部条目。 */
export function currentSessionFileEntries(entries: readonly Entry[]): Entry[] {
	const files = [...groupMainFiles(entries).values()];
	return files[files.length - 1] ?? [];
}

/**
 * 推导活动分支（或预览分支）上所有可 rewind 目标，按转录先后排序。
 * 索引 `targets.length - 1` 即「所有可 rewind 对象的最后一个」，也是双击 Esc 时的默认选中项。
 */
export function buildRewindTargets(
	entries: readonly Entry[],
	leafEntryId: string | undefined,
): RewindTarget[] {
	const fileEntries = currentSessionFileEntries(entries);
	if (fileEntries.length === 0 || leafEntryId === "") return [];
	const hasLeaf =
		leafEntryId !== undefined &&
		fileEntries.some((e) => e.entryId === leafEntryId);
	const branch = branchPath(fileEntries, hasLeaf ? leafEntryId : undefined);
	return targetsFromBranch(branch);
}

function targetsFromBranch(branch: readonly Entry[]): RewindTarget[] {
	const targets: RewindTarget[] = [];
	for (const e of branch) {
		if (
			SYNTHETIC_IDS[e.entryId] ||
			(isRecord(e.payload) && "activeLeafId" in e.payload)
		)
			continue;
		const p = isRecord(e.payload) ? e.payload : {};

		if (e.type === "message" && isRecord(p.message)) {
			const msg = p.message;
			const role = msg.role;
			if (role === "toolResult") {
				const prev = targets[targets.length - 1];
				if (prev) {
					prev.targetEntryId = e.entryId;
					prev.entryIds.push(e.entryId);
					const out = plainTextOfContent(msg.content);
					const name = typeof msg.toolName === "string" ? msg.toolName : "";
					const extra = [name, out].filter(Boolean).join("\n").toLowerCase();
					if (extra)
						prev.searchText = prev.searchText
							? `${prev.searchText}\n${extra}`
							: extra;
				}
				continue;
			}
			if (
				(role === "custom" || role === "hookMessage") &&
				msg.display === false
			)
				continue;

			if (role === "user") {
				const raw = plainTextOfContent(msg.content);
				const { text } = parseDraftEditsBlock(parseAttachmentBlock(raw).text);
				const irc = isRecord(msg.parentIrc) ? msg.parentIrc : null;
				// 视图回执（M33）预览显示作答正文，不露出标签与 JSON 围栏
				const body = irc
					? String(irc.body ?? text)
					: (parseViewReceipt(text)?.display ?? text);
				targets.push({
					rowKey: `e${e.seq}`,
					anchorEntryId: e.entryId,
					targetEntryId: e.entryId,
					entryIds: [e.entryId],
					isUserTurn: isRealUserMessage(msg),
					rewindsBefore: true,
					kind: "user",
					preview: oneLine(body),
					searchText: body.toLowerCase(),
					parentId: e.parentId ?? null,
				});
				continue;
			}

			if (role === "assistant") {
				const info = inspectAssistant(msg);
				if (!info.visible) continue;
				targets.push({
					rowKey: `e${e.seq}`,
					anchorEntryId: e.entryId,
					targetEntryId: e.entryId,
					entryIds: [e.entryId],
					isUserTurn: false,
					rewindsBefore: false,
					kind: info.hasToolCall ? "tool" : "assistant",
					preview: info.preview,
					searchText: info.searchParts.join("\n").toLowerCase(),
					parentId: e.parentId ?? null,
				});
				continue;
			}

			// 其它渲染消息（custom / hookMessage / bashExecution 等）
			const body = plainTextOfContent(msg.content);
			targets.push({
				rowKey: `e${e.seq}`,
				anchorEntryId: e.entryId,
				targetEntryId: e.entryId,
				entryIds: [e.entryId],
				isUserTurn: false,
				rewindsBefore: role === "custom" && msg.attribution === "user",
				kind: "custom",
				preview: oneLine(body),
				searchText: body.toLowerCase(),
				parentId: e.parentId ?? null,
			});
			continue;
		}

		if (e.type === "custom_message" && p.display !== false) {
			const body = plainTextOfContent(p.content);
			targets.push({
				rowKey: `e${e.seq}`,
				anchorEntryId: e.entryId,
				targetEntryId: e.entryId,
				entryIds: [e.entryId],
				isUserTurn: false,
				rewindsBefore: p.customType !== "skill-prompt",
				kind: "custom",
				preview: oneLine(body),
				searchText: body.toLowerCase(),
				parentId: e.parentId ?? null,
			});
		}
	}
	return targets;
}

/**
 * 查找 `anchorEntryId` 所在分叉点的所有兄弟分支（包含当前分支在内，当前分支排在第 0 位）。
 * 不足 2 条含可见目标的分支时返回空数组。
 */
export function siblingBranchesFor(
	entries: readonly Entry[],
	anchorEntryId: string,
	currentLeafId: string | undefined,
): SiblingBranch[] {
	const fileEntries = currentSessionFileEntries(entries);
	if (fileEntries.length === 0) return [];

	const byId = new Map<string, Entry>();
	const children = new Map<string | null, Entry[]>();
	for (const e of fileEntries) {
		if (
			SYNTHETIC_IDS[e.entryId] ||
			(isRecord(e.payload) && "activeLeafId" in e.payload)
		)
			continue;
		byId.set(e.entryId, e);
		const pid = e.parentId ?? null;
		let list = children.get(pid);
		if (!list) {
			list = [];
			children.set(pid, list);
		}
		list.push(e);
	}
	for (const list of children.values()) list.sort((a, b) => a.seq - b.seq);

	const anchor = byId.get(anchorEntryId);
	if (!anchor) return [];

	// 若 anchor 与真正分叉点之间只夹了单链非转录元数据（如 model_change），向上找到该分叉点
	let node: Entry = anchor;
	let parentKey: string | null = node.parentId ?? null;
	while (parentKey !== null) {
		const sibs = children.get(parentKey) ?? [];
		if (sibs.length > 1) break;
		const parent = byId.get(parentKey);
		if (!parent) break;
		// 遇到上一条转录消息就停，不越过回合边界
		if (parent.type === "message" || parent.type === "custom_message") break;
		node = parent;
		parentKey = parent.parentId ?? null;
	}

	const rawSiblings = children.get(parentKey) ?? [];
	if (rawSiblings.length < 2) return [];

	// 当前活动路径的节点集合，用来识别哪一条是当前分支，以及保留当前分支实际所处的叶子
	const hasLeaf =
		currentLeafId !== undefined &&
		currentLeafId !== "" &&
		byId.has(currentLeafId);
	const currentPath =
		currentLeafId === ""
			? []
			: branchPath(fileEntries, hasLeaf ? currentLeafId : undefined);
	const currentLeaf = currentPath[currentPath.length - 1]?.entryId;

	const branches: SiblingBranch[] = [];
	for (const root of rawSiblings) {
		const isCurrent = root.entryId === node.entryId;
		let subPath: Entry[];
		let leafId: string;
		if (isCurrent && currentLeaf) {
			const idx = currentPath.findIndex((e) => e.entryId === root.entryId);
			subPath = idx >= 0 ? currentPath.slice(idx) : [];
			leafId = currentLeaf;
		} else {
			subPath = [];
			const seen = new Set<string>();
			let cur: Entry | undefined = root;
			while (cur && !seen.has(cur.entryId)) {
				seen.add(cur.entryId);
				subPath.push(cur);
				const kids = children.get(cur.entryId);
				cur = kids?.[kids.length - 1];
			}
			leafId = subPath[subPath.length - 1]?.entryId ?? root.entryId;
		}
		const subTargets = targetsFromBranch(subPath);
		const firstTarget = subTargets[0];
		if (!firstTarget) continue;
		const firstUser = subTargets.find((t) => t.isUserTurn) ?? firstTarget;
		branches.push({
			rootEntryId: root.entryId,
			firstTargetId: firstTarget.anchorEntryId,
			leafEntryId: leafId,
			label: firstUser.preview || root.entryId,
			isCurrent,
		});
	}
	if (branches.length < 2) return [];
	// 当前分支排在第一位，其余兄弟分支按树顺序排列
	branches.sort((a, b) => Number(b.isCurrent) - Number(a.isCurrent));
	return branches;
}

const LATIN_WORD_RE = /^[\p{Script=Latin}\p{N}_]+$/u;

/** 编译多词过滤正则：纯拉丁/数字词整词匹配，CJK 等其他字符子串匹配，不区分大小写。 */
export function compileRewindFilter(query: string): RegExp[] {
	const words = query.toLowerCase().trim().split(/\s+/).filter(Boolean);
	return words.map((word) => {
		const escaped = word.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
		return LATIN_WORD_RE.test(word)
			? new RegExp(`(?<![\\p{L}\\p{N}_])${escaped}(?![\\p{L}\\p{N}_])`, "u")
			: new RegExp(escaped, "u");
	});
}

/** 返回匹配 `query` 的目标索引列表；空查询返回全部索引。 */
export function matchRewindTargets(
	targets: readonly RewindTarget[],
	query: string,
): number[] {
	const patterns = compileRewindFilter(query);
	if (patterns.length === 0) return targets.map((_, i) => i);
	const out: number[] = [];
	for (let i = 0; i < targets.length; i++) {
		const hay = targets[i]?.searchText ?? "";
		if (patterns.every((re) => re.test(hay))) out.push(i);
	}
	return out;
}

/**
 * 计算按某个方向在用户回合之间跳转的目标索引；若分支上没有真实用户回合，
 * 退化为任意 `kind === "user"` 目标。
 */
export function stepUserTurn(
	targets: readonly RewindTarget[],
	fromIndex: number,
	delta: -1 | 1,
): number | null {
	const hasReal = targets.some((t) => t.isUserTurn);
	const accept = hasReal
		? (t: RewindTarget) => t.isUserTurn
		: (t: RewindTarget) => t.kind === "user";
	for (let i = fromIndex + delta; i >= 0 && i < targets.length; i += delta) {
		const t = targets[i];
		if (t && accept(t)) return i;
	}
	return null;
}
