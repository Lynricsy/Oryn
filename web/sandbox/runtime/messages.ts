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
		/** 问这个偏移的元素的包围盒，回 `select`（带 `located`，`selection` 是当前选中、不变）。 */
		| { kind: "select-locate"; src: number }
		/** 预览样式：只含通过校验的属性（解析时已丢掉不合规的项）。 */
		| { kind: "preview-style"; style: Partial<EditableStyle> }
		| { kind: "preview-text"; text: string }
		| { kind: "preview-reset" }
	);

/** 页面 → 父页面。 */
export type PageMessage = Envelope &
	(
		| { kind: "ready"; params: Record<string, unknown> }
		| { kind: "scroll"; x: number; y: number }
		| { kind: "blocked"; url: string }
		/** 类型化 artifact 的查看器进入 / 退出演示：父页面把帧铺满窗口或还原。 */
		| { kind: "present"; on: boolean }
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

function parseSelection(x: unknown): PageSelection | null {
	if (!isFields(x)) return null;
	const src = offsetOrNull(x.src);
	const { path, tag, text, textOnly, style } = x;
	if (
		src === undefined ||
		typeof path !== "string" ||
		typeof tag !== "string" ||
		!TAG_PATTERN.test(tag) ||
		typeof text !== "string" ||
		runeCount(text) > SELECTION_TEXT_CHARS ||
		typeof textOnly !== "boolean" ||
		!isFields(style)
	) {
		return null;
	}
	const values = {} as EditableStyle;
	for (const prop of EDITABLE_PROPS) {
		const v = style[prop];
		if (typeof v !== "string" || v.length > MAX_STYLE_VALUE_CHARS) return null;
		values[prop] = v;
	}
	return { path, src, tag, text, textOnly, style: values };
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
		case "preview-style":
		case "preview-text":
		case "preview-reset":
			return parseSelectRequest(head, m);
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
		case "ready":
			return isFields(m.params)
				? { ...head, kind: m.kind, params: m.params }
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
		default:
			return null;
	}
}
