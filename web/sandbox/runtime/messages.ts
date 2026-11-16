// 父页面（Oryn 的 SPA）与沙箱里的 artifact 页面之间的 postMessage 协议。
// 两边共用：SPA 从这里 import，沙箱运行时把它打进 runtime.js。
// 这里只放类型与纯函数，不 import 任何依赖应用状态的模块。

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

/** 父页面 → 页面。 */
export type ParentMessage = Envelope &
	(
		| { kind: "hello" }
		| { kind: "keep-scroll" }
		| { kind: "restore-scroll"; x: number; y: number }
	);

/** 页面 → 父页面。 */
export type PageMessage = Envelope &
	(
		| { kind: "ready"; params: Record<string, unknown> }
		| { kind: "scroll"; x: number; y: number }
		| { kind: "blocked"; url: string }
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
		default:
			return null;
	}
}
