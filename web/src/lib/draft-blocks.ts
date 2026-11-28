/**
 * 写作块（Writing Block）围栏解析：把助手消息的 text 块拆成普通 Markdown 段与草稿段。
 * - 开围栏：行首 0–3 空格 + 3 个以上反引号或波浪号 + `draft-email` / `draft-message` / `draft-doc` + 可选属性串；
 * - 闭围栏：同一字符、长度不小于开围栏，后面只有空白；
 * - 普通（非 draft）围栏内部出现的 draft 开围栏不算；未闭合的草稿延伸到文末（流式中），`closed` 为 false。
 * 块键为 `<contentIndex>.<ordinal>`：`ordinal` 是该 text 块内第几个草稿（从 0 起），由调用方拼接。
 */
import type { TFunction } from "i18next";

export type DraftKind = "email" | "message" | "doc";

export interface DraftAttrs {
	title?: string;
	subject?: string;
	to?: string;
}

export type DraftSegment =
	| { type: "markdown"; text: string }
	| {
			type: "draft";
			ordinal: number;
			kind: DraftKind;
			attrs: DraftAttrs;
			body: string;
			closed: boolean;
	  };

const DRAFT_OPEN_RE =
	/^( {0,3})(`{3,}|~{3,})[ \t]*draft-(email|message|doc)(?:[ \t]+(.*))?$/;
const FENCE_OPEN_RE = /^( {0,3})(`{3,}|~{3,})(.*)$/;
const ATTR_RE = /(\w+)="([^"]*)"/g;
const ATTR_KEYS: Record<string, true> = {
	title: true,
	subject: true,
	to: true,
};
const ATTR_MAX = 200;

interface Fence {
	char: string;
	len: number;
	/** 开围栏的缩进：正文行最多去掉这么多前导空格（同 CommonMark）。 */
	indent: number;
}

function parseAttrs(raw: string | undefined): DraftAttrs {
	const attrs: DraftAttrs = {};
	if (!raw) return attrs;
	for (const m of raw.matchAll(ATTR_RE)) {
		const key = m[1] ?? "";
		if (!Object.hasOwn(ATTR_KEYS, key)) continue;
		const value = (m[2] ?? "").trim().slice(0, ATTR_MAX);
		if (value) attrs[key as keyof DraftAttrs] = value;
	}
	return attrs;
}

function isClose(line: string, f: Fence): boolean {
	const m = /^ {0,3}(`+|~+)[ \t]*$/.exec(line);
	const run = m?.[1];
	return !!run && run[0] === f.char && run.length >= f.len;
}

function stripIndent(line: string, indent: number): string {
	let i = 0;
	while (i < indent && line[i] === " ") i++;
	return line.slice(i);
}

/** 拆分一段助手正文；不含 `draft-` 时直接整段返回（快路径）。纯空白的 Markdown 段丢弃。 */
export function splitDrafts(text: string): DraftSegment[] {
	const src = text.replace(/\r\n/g, "\n");
	if (!src.includes("draft-")) return [{ type: "markdown", text: src }];

	const segs: DraftSegment[] = [];
	let md: string[] = [];
	let outer: Fence | null = null;
	let draft:
		| (Fence & { kind: DraftKind; attrs: DraftAttrs; body: string[] })
		| null = null;
	let ordinal = 0;

	const flushMarkdown = () => {
		const joined = md.join("\n");
		if (joined.trim()) segs.push({ type: "markdown", text: joined });
		md = [];
	};
	const pushDraft = (closed: boolean) => {
		if (!draft) return;
		segs.push({
			type: "draft",
			ordinal: ordinal++,
			kind: draft.kind,
			attrs: draft.attrs,
			body: draft.body.join("\n"),
			closed,
		});
		draft = null;
	};

	for (const line of src.split("\n")) {
		if (draft) {
			if (isClose(line, draft)) pushDraft(true);
			else draft.body.push(stripIndent(line, draft.indent));
			continue;
		}
		if (outer) {
			if (isClose(line, outer)) outer = null;
			md.push(line);
			continue;
		}
		const open = DRAFT_OPEN_RE.exec(line);
		if (open) {
			flushMarkdown();
			const fence = open[2] ?? "```";
			draft = {
				char: fence[0] ?? "`",
				len: fence.length,
				indent: open[1]?.length ?? 0,
				kind: open[3] as DraftKind,
				attrs: parseAttrs(open[4]),
				body: [],
			};
			continue;
		}
		const fence = FENCE_OPEN_RE.exec(line);
		// 反引号围栏的 info 串不能含反引号（否则是行内代码）
		if (fence?.[2] && !(fence[2][0] === "`" && fence[3]?.includes("`")))
			outer = {
				char: fence[2][0] ?? "`",
				len: fence[2].length,
				indent: fence[1]?.length ?? 0,
			};
		md.push(line);
	}
	pushDraft(false);
	flushMarkdown();
	return segs;
}

/** 块的显示标题：`title` → `subject` → 类型名。 */
export function draftTitle(
	kind: DraftKind,
	attrs: DraftAttrs,
	t: TFunction,
): string {
	return attrs.title ?? attrs.subject ?? t(`writingBlock.kind.${kind}`);
}

/** 下载 / 存档用的文件名主干：去掉路径与保留字符、控制字符，最长 80 字符，空则 `draft`。 */
export function draftFileBase(title: string): string {
	// biome-ignore lint/suspicious/noControlCharactersInRegex: 文件名里要剔除控制字符
	const base = title.replace(/[/\\:*?"<>|\u0000-\u001f\u007f]/g, "_").trim();
	return base.slice(0, 80) || "draft";
}
