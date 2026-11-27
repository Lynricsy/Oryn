// 父页面（Oryn 的 SPA）与沙箱里的 artifact 页面之间的 postMessage 协议。
// 两边共用：SPA 从这里 import，沙箱运行时把它打进 runtime.js。
// 这里只放类型与纯函数，不 import 任何依赖应用状态的模块。

import {
	cleanStyle,
	EDITABLE_PROPS,
	type EditableStyle,
	runeCount,
	validEditText,
} from "../../src/lib/artifacts/editable";
import {
	type ParamValue,
	parseParamValues,
} from "../../src/lib/artifacts/params";

export const PROTOCOL_VERSION = 1;

/** 单条消息（JSON 序列化后的 UTF-8 字节数）的上限；超出的消息两边都直接丢弃。 */
export const MAX_MESSAGE_BYTES = 64 * 1024;

/** 父页面为每个帧生成的通道标识：16 位小写十六进制。 */
export const CHANNEL_PATTERN = /^[0-9a-f]{16}$/;

// 先缓存，页面脚本之后改写全局的 JSON 不影响协议的编解码
const stringify = JSON.stringify;

interface Envelope {
	v: typeof PROTOCOL_VERSION;
	channel: string;
}

/** 选择与评论模式：选择（V）选中元素来编辑或提问，评论（C）给元素留评论；`null` 是关闭。 */
export type SelectMode = "select" | "comment" | null;

/** 评论图钉：`n` 是评论串编号；`src` 是元素的源码偏移（整页的或已不在的为 `null`，不画）；agent 回复过的 `answered`。 */
export interface SelectPin {
	n: number;
	src: number | null;
	answered: boolean;
}

/** 一页最多画的图钉数；超出的整条 `select-pins` 被丢弃。 */
export const MAX_SELECT_PINS = 200;

/** `select-mode.accent`（覆盖层的强调色，任意 CSS 颜色）的长度上限。 */
export const MAX_ACCENT_CHARS = 64;

/** 选中元素的文本摘录按码点计的长度。 */
export const SELECTION_TEXT_CHARS = 120;

/** 选中元素回报的计算样式值单个的长度上限。 */
export const MAX_STYLE_VALUE_CHARS = 200;

/** 锚点里的标签名：小写，与服务端的评论锚点校验一致。 */
export const TAG_PATTERN = /^[a-z][a-z0-9-]{0,63}$/;

/** 设计画布元素的源码位置 `src/frames/x.tsx:行:列`（`data-oryn-loc`），与服务端的评论锚点 `loc` 校验一致。 */
export const LOC_PATTERN =
	/^src\/[A-Za-z0-9._/-]+\.(tsx|ts|jsx|js):[1-9][0-9]*:[1-9][0-9]*$/;
/** `loc` 的长度上限（字节，全是 ASCII）。 */
export const MAX_LOC_CHARS = 512;

/** 设计画布的帧 id：`src/frames/<id>.tsx` 的文件名（与 canvas.json 的 `source` 校验一致）。 */
export const FRAME_ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,63}$/;
/** `ready.design.links` 的条数上限。 */
export const MAX_DESIGN_LINKS = 256;

/** 表格单元格区域（如 `B2:C3`），与服务端的评论锚点 `range` 校验一致。 */
export const RANGE_PATTERN =
	/^[A-Z]{1,3}[1-9][0-9]{0,6}(:[A-Z]{1,3}[1-9][0-9]{0,6})?$/;

/**
 * 类型化 artifact 查看器里的评论锚点：页（幻灯片 / 工作表；Markdown 文档为 `null`）、页内归一化坐标
 * （同为 `null` 表示整页）、表格的单元格区域、Markdown 的块序号（从 0 起）与点选处的文本片段。
 */
export interface ViewerAnchor {
	page: { number: number; name: string } | null;
	x: number | null;
	y: number | null;
	range: string | null;
	block: number | null;
	text: string;
}

/** 查看器里的评论图钉：`n` 是评论串编号，agent 回复过的 `answered`。 */
export interface AnchorPin {
	n: number;
	answered: boolean;
	anchor: ViewerAnchor;
}

/** 设计画布 dev 帧的帧信息：当前帧与它链接到的帧（引擎从 `<Link>` 推出）。 */
export interface DesignFrameInfo {
	frame: string;
	links: string[];
}

/** 视口坐标里的矩形（CSS 像素）。 */
export interface Box {
	x: number;
	y: number;
	width: number;
	height: number;
}

/** 页面里选中的元素。 */
export interface PageSelection {
	/** 页面在 artifact 版本里的路径（运行时配置的 `path`）。 */
	path: string;
	/** 元素起始标签在原文件里的 UTF-8 字节偏移；没有本次响应源码标记的元素（页面脚本生成的）为 `null`，只能评论不能编辑。 */
	src: number | null;
	/** 小写标签名。 */
	tag: string;
	/** 折叠空白后的前 `SELECTION_TEXT_CHARS` 个字符。 */
	text: string;
	/** 子节点全是文本（可以直接改文字）。 */
	textOnly: boolean;
	/** 七个可编辑属性的计算值。 */
	style: EditableStyle;
	/** 设计画布：元素或最近祖先的 `data-oryn-loc`；没有时为 `null`。旧运行时不带这个字段。 */
	loc?: string | null;
}

/** 父页面 → 页面。 */
export type ParentMessage = Envelope &
	(
		| { kind: "hello" }
		| { kind: "keep-scroll" }
		| { kind: "restore-scroll"; x: number; y: number }
		/** 开关选择 / 评论模式；关闭时页面同时清掉悬停与选中。 */
		| { kind: "select-mode"; mode: SelectMode; accent: string }
		/** 用帧视口坐标驱动悬停（画布编辑器的覆盖层挡在画框上方时用）。 */
		| { kind: "select-point"; x: number; y: number }
		| { kind: "select-leave" }
		/** 用帧视口坐标选取，回 `select`（带 `at`）。 */
		| { kind: "select-pick"; x: number; y: number }
		/** 选中这个偏移的元素并回 `select`（找不到回 `selection: null`）；`null` 只清掉选中，不回消息。 */
		| { kind: "select-source"; src: number | null }
		| { kind: "select-pins"; pins: SelectPin[] }
		/** 查看器：画评论图钉（类型化锚点）。 */
		| { kind: "anchor-pins"; pins: AnchorPin[] }
		/** 查看器：滚动到这个锚点。 */
		| { kind: "anchor-locate"; anchor: ViewerAnchor }
		/** 问这个偏移的元素的包围盒，回 `select`（带 `located`，`selection` 是当前选中、不变）。 */
		| { kind: "select-locate"; src: number }
		/** 预览样式：只含通过校验的属性（解析时已丢掉不合规的项）。 */
		| { kind: "preview-style"; style: Partial<EditableStyle> }
		| { kind: "preview-text"; text: string }
		| { kind: "preview-reset" }
		/** 预览调节参数（不落盘，页面重载后失效）：只含参数名与基本类型的值，是否符合声明由页面运行时再判。 */
		| { kind: "params"; values: Record<string, ParamValue> }
		/** 设计画布：选中 `data-oryn-loc` 等于 `loc` 的第一个元素并回 `select`（找不到回 `selection: null`）。 */
		| { kind: "select-loc"; loc: string }
		/** 设计画布：整页重新加载（开发服务的热更不可用、或工作副本对账后）。 */
		| { kind: "design-reload" }
		/** 设计画布：帧内跳到另一帧（dev 帧 `./<frame>`，静态产物 `./<frame>.html`）。 */
		| { kind: "navigate"; frame: string }
	);

/** 页面 → 父页面。 */
export type PageMessage = Envelope &
	/** `params` 是页面声明的调节参数（规整后，「参数名 → 定义」）；父页面用 `parseParamsDecl` 再校验一遍。 */

	/** `design` 只在设计画布的 dev 帧里有（引擎入口随运行时配置给出）。 */
	(
		| {
				kind: "ready";
				params: Record<string, unknown>;
				design?: DesignFrameInfo;
		  }
		| { kind: "scroll"; x: number; y: number }
		| { kind: "blocked"; url: string }
		/** 类型化 artifact 的查看器进入 / 退出演示：父页面把帧铺满窗口或还原。 */
		| { kind: "present"; on: boolean }
		/** 设计画布：页面现在显示的帧（每次带 `design` 的 `ready` 之后紧跟一条，帧内跳转后即新帧）。 */
		| { kind: "navigated"; frame: string }
		/** 查看器在评论模式下的点选：`anchor` 为 `null` 表示点在可评论的内容之外；`at` 是点击处的视口坐标。 */
		| {
				kind: "anchor-pick";
				anchor: ViewerAnchor | null;
				at: { x: number; y: number };
		  }
		/** 选中变化或回应 `select-source` / `select-locate`；`at` 是点击处的视口坐标。 */
		| {
				kind: "select";
				selection: PageSelection | null;
				at?: { x: number; y: number };
				located?: { src: number; box: Box | null };
		  }
	);

type Body<M> = M extends unknown ? Omit<M, "v" | "channel"> : never;

/** 去掉公共字段后的消息体，发送方补上 `v` 与 `channel`。 */
export type ParentMessageBody = Body<ParentMessage>;
export type PageMessageBody = Body<PageMessage>;
export type ParentMessageKind = ParentMessage["kind"];
export type PageMessageKind = PageMessage["kind"];

/** 字符串按 UTF-8 编码后的字节数。 */
export function utf8Length(s: string): number {
	let n = 0;
	for (let i = 0; i < s.length; i++) {
		const c = s.charCodeAt(i);
		if (c < 0x80) n += 1;
		else if (c < 0x800) n += 2;
		else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
			const d = s.charCodeAt(i + 1);
			if (d >= 0xdc00 && d <= 0xdfff) {
				n += 4;
				i++;
			} else n += 3;
		} else n += 3;
	}
	return n;
}

/** 消息能序列化成 JSON 且不超过 `MAX_MESSAGE_BYTES`。 */
export function fitsMessageLimit(data: unknown): boolean {
	let json: string | undefined;
	try {
		json = stringify(data);
	} catch {
		return false;
	}
	return json !== undefined && utf8Length(json) <= MAX_MESSAGE_BYTES;
}

type Fields = Record<string, unknown>;

// 唯一的对象形状判断：消息外壳与 `ready.params` 都只要求是普通对象，字段逐个另行校验
function isFields(x: unknown): x is Fields {
	return typeof x === "object" && x !== null && !Array.isArray(x);
}

// 通过校验的外壳：`v` 与 `channel` 已核对，其余字段仍待逐个校验
type Checked = Fields & Envelope;

function envelope(data: unknown): Checked | null {
	if (!isFields(data) || data.v !== PROTOCOL_VERSION) return null;
	if (typeof data.channel !== "string" || !CHANNEL_PATTERN.test(data.channel)) {
		return null;
	}
	return fitsMessageLimit(data) ? (data as Checked) : null;
}

function isCoord(x: unknown): x is number {
	return typeof x === "number" && Number.isFinite(x);
}

function isOffset(x: unknown): x is number {
	return typeof x === "number" && Number.isSafeInteger(x) && x >= 0;
}

// 偏移或 `null`；都不是时为 `undefined`
function offsetOrNull(x: unknown): number | null | undefined {
	if (x === null) return null;
	return isOffset(x) ? x : undefined;
}

// 运行时在页面里解析这些消息，所以只用下标循环，不依赖页面能改写的数组方法
function parsePins(x: unknown): SelectPin[] | null {
	if (!Array.isArray(x) || x.length > MAX_SELECT_PINS) return null;
	const out: SelectPin[] = [];
	for (let i = 0; i < x.length; i++) {
		const p: unknown = x[i];
		if (!isFields(p)) return null;
		const src = offsetOrNull(p.src);
		if (
			typeof p.n !== "number" ||
			!Number.isSafeInteger(p.n) ||
			p.n < 1 ||
			src === undefined ||
			typeof p.answered !== "boolean"
		) {
			return null;
		}
		out[i] = { n: p.n, src, answered: p.answered };
	}
	return out;
}

function unitOrNull(x: unknown): number | null | undefined {
	if (x === null) return null;
	return typeof x === "number" && x >= 0 && x <= 1 ? x : undefined;
}

/** 校验并重建查看器锚点（逐个字段）；不合规时返回 `null`。 */
export function parseViewerAnchor(x: unknown): ViewerAnchor | null {
	if (!isFields(x)) return null;
	const { page, range, block, text } = x;
	if (typeof text !== "string" || runeCount(text) > SELECTION_TEXT_CHARS) {
		return null;
	}
	let pg: ViewerAnchor["page"] = null;
	if (page !== null) {
		if (
			!isFields(page) ||
			typeof page.number !== "number" ||
			!Number.isSafeInteger(page.number) ||
			page.number < 1 ||
			typeof page.name !== "string" ||
			runeCount(page.name) > SELECTION_TEXT_CHARS
		) {
			return null;
		}
		pg = { number: page.number, name: page.name };
	}
	const px = unitOrNull(x.x);
	const py = unitOrNull(x.y);
	if (px === undefined || py === undefined || (px === null) !== (py === null)) {
		return null;
	}
	let rng: string | null = null;
	if (range !== null) {
		if (typeof range !== "string" || !RANGE_PATTERN.test(range)) return null;
		rng = range;
	}
	const blk = offsetOrNull(block);
	if (blk === undefined) return null;
	return { page: pg, x: px, y: py, range: rng, block: blk, text };
}

function parseAnchorPins(x: unknown): AnchorPin[] | null {
	if (!Array.isArray(x) || x.length > MAX_SELECT_PINS) return null;
	const out: AnchorPin[] = [];
	for (let i = 0; i < x.length; i++) {
		const p: unknown = x[i];
		if (!isFields(p)) return null;
		const anchor = parseViewerAnchor(p.anchor);
		if (
			typeof p.n !== "number" ||
			!Number.isSafeInteger(p.n) ||
			p.n < 1 ||
			typeof p.answered !== "boolean" ||
			!anchor
		) {
			return null;
		}
		out[i] = { n: p.n, answered: p.answered, anchor };
	}
	return out;
}

function parseSelectRequest(head: Envelope, m: Checked): ParentMessage | null {
	switch (m.kind) {
		case "select-mode":
			return (m.mode === "select" || m.mode === "comment" || m.mode === null) &&
				typeof m.accent === "string" &&
				m.accent.length <= MAX_ACCENT_CHARS
				? { ...head, kind: m.kind, mode: m.mode, accent: m.accent }
				: null;
		case "select-point":
		case "select-pick":
			return isCoord(m.x) && isCoord(m.y)
				? { ...head, kind: m.kind, x: m.x, y: m.y }
				: null;
		case "select-leave":
		case "preview-reset":
			return { ...head, kind: m.kind };
		case "select-source": {
			const src = offsetOrNull(m.src);
			return src === undefined ? null : { ...head, kind: m.kind, src };
		}
		case "select-locate":
			return isOffset(m.src) ? { ...head, kind: m.kind, src: m.src } : null;
		case "select-pins": {
			const pins = parsePins(m.pins);
			return pins ? { ...head, kind: m.kind, pins } : null;
		}
		case "anchor-pins": {
			const pins = parseAnchorPins(m.pins);
			return pins ? { ...head, kind: m.kind, pins } : null;
		}
		case "anchor-locate": {
			const anchor = parseViewerAnchor(m.anchor);
			return anchor ? { ...head, kind: m.kind, anchor } : null;
		}
		case "preview-style":
			return isFields(m.style)
				? { ...head, kind: m.kind, style: cleanStyle(m.style) }
				: null;
		case "preview-text":
			return validEditText(m.text)
				? { ...head, kind: m.kind, text: m.text }
				: null;
		default:
			return null;
	}
}

function parseBox(x: unknown): Box | null {
	if (!isFields(x)) return null;
	const { x: left, y: top, width, height } = x;
	return isCoord(left) &&
		isCoord(top) &&
		isCoord(width) &&
		isCoord(height) &&
		width >= 0 &&
		height >= 0
		? { x: left, y: top, width, height }
		: null;
}

/** 合规的源码位置（`data-oryn-loc`）。 */
export function isLoc(x: unknown): x is string {
	return (
		typeof x === "string" && x.length <= MAX_LOC_CHARS && LOC_PATTERN.test(x)
	);
}

/** 合规的设计帧 id。 */
export function isFrameId(x: unknown): x is string {
	return typeof x === "string" && FRAME_ID_PATTERN.test(x);
}

/** 校验并重建帧信息 `{frame, links}`；任一项不合规时返回 `null`。运行时配置与 `ready.design` 共用。 */
export function parseDesignInfo(x: unknown): DesignFrameInfo | null {
	if (!isFields(x) || !isFrameId(x.frame)) return null;
	const links = x.links;
	if (!Array.isArray(links) || links.length > MAX_DESIGN_LINKS) return null;
	const out: string[] = [];
	for (let i = 0; i < links.length; i++) {
		const id: unknown = links[i];
		if (!isFrameId(id)) return null;
		out[i] = id;
	}
	return { frame: x.frame, links: out };
}

function parseSelection(x: unknown): PageSelection | null {
	if (!isFields(x)) return null;
	const src = offsetOrNull(x.src);
	const { path, tag, text, textOnly, style, loc } = x;
	if (
		src === undefined ||
		typeof path !== "string" ||
		typeof tag !== "string" ||
		!TAG_PATTERN.test(tag) ||
		typeof text !== "string" ||
		runeCount(text) > SELECTION_TEXT_CHARS ||
		typeof textOnly !== "boolean" ||
		!isFields(style) ||
		(loc !== undefined && loc !== null && !isLoc(loc))
	) {
		return null;
	}
	const values = {} as EditableStyle;
	for (const prop of EDITABLE_PROPS) {
		const v = style[prop];
		if (typeof v !== "string" || v.length > MAX_STYLE_VALUE_CHARS) return null;
		values[prop] = v;
	}
	const out: PageSelection = { path, src, tag, text, textOnly, style: values };
	if (loc !== undefined) out.loc = loc;
	return out;
}

// 页面发来的 `select`：这些数据来自不可信的页面，逐个字段校验后重建
function parseSelect(head: Envelope, m: Checked): PageMessage | null {
	const selection = m.selection === null ? null : parseSelection(m.selection);
	if (m.selection !== null && selection === null) return null;
	const msg: Extract<PageMessage, { kind: "select" }> = {
		...head,
		kind: "select",
		selection,
	};
	if (m.at !== undefined) {
		const at = m.at;
		if (!isFields(at) || !isCoord(at.x) || !isCoord(at.y)) return null;
		msg.at = { x: at.x, y: at.y };
	}
	if (m.located !== undefined) {
		const loc = m.located;
		if (!isFields(loc) || !isOffset(loc.src)) return null;
		const box = loc.box === null ? null : parseBox(loc.box);
		if (loc.box !== null && box === null) return null;
		msg.located = { src: loc.src, box };
	}
	return msg;
}

/** 校验并规整父页面发来的消息；形状不对、版本不符或超限时返回 `null`。 */
export function parseParentMessage(data: unknown): ParentMessage | null {
	const m = envelope(data);
	if (!m) return null;
	const head: Envelope = { v: m.v, channel: m.channel };
	switch (m.kind) {
		case "hello":
		case "keep-scroll":
			return { ...head, kind: m.kind };
		case "restore-scroll":
			return isCoord(m.x) && isCoord(m.y)
				? { ...head, kind: m.kind, x: m.x, y: m.y }
				: null;
		case "select-mode":
		case "select-point":
		case "select-leave":
		case "select-pick":
		case "select-source":
		case "select-pins":
		case "select-locate":
		case "anchor-pins":
		case "anchor-locate":
		case "preview-style":
		case "preview-text":
		case "preview-reset":
			return parseSelectRequest(head, m);
		case "params": {
			const values = parseParamValues(m.values);
			return values ? { ...head, kind: m.kind, values } : null;
		}
		case "select-loc":
			return isLoc(m.loc) ? { ...head, kind: m.kind, loc: m.loc } : null;
		case "design-reload":
			return { ...head, kind: m.kind };
		case "navigate":
			return isFrameId(m.frame)
				? { ...head, kind: m.kind, frame: m.frame }
				: null;
		default:
			return null;
	}
}

/** 校验并规整页面发来的消息；形状不对、版本不符或超限时返回 `null`。 */
export function parsePageMessage(data: unknown): PageMessage | null {
	const m = envelope(data);
	if (!m) return null;
	const head: Envelope = { v: m.v, channel: m.channel };
	switch (m.kind) {
		case "ready": {
			if (!isFields(m.params)) return null;
			if (m.design === undefined)
				return { ...head, kind: m.kind, params: m.params };
			const design = parseDesignInfo(m.design);
			return design
				? { ...head, kind: m.kind, params: m.params, design }
				: null;
		}
		case "navigated":
			return isFrameId(m.frame)
				? { ...head, kind: m.kind, frame: m.frame }
				: null;
		case "scroll":
			return isCoord(m.x) && isCoord(m.y)
				? { ...head, kind: m.kind, x: m.x, y: m.y }
				: null;
		case "blocked":
			return typeof m.url === "string"
				? { ...head, kind: m.kind, url: m.url }
				: null;
		case "present":
			return typeof m.on === "boolean"
				? { ...head, kind: m.kind, on: m.on }
				: null;
		case "select":
			return parseSelect(head, m);
		case "anchor-pick": {
			const anchor = m.anchor === null ? null : parseViewerAnchor(m.anchor);
			if (m.anchor !== null && anchor === null) return null;
			const at = m.at;
			if (!isFields(at) || !isCoord(at.x) || !isCoord(at.y)) return null;
			return { ...head, kind: m.kind, anchor, at: { x: at.x, y: at.y } };
		}
		default:
			return null;
	}
}
