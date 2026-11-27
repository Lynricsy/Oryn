// 父页面（Oryn 的 SPA）与沙箱里的 HTML 视图之间的 postMessage 协议（M33，docs/plan-m33.md 的 C9）。
// 两边共用：SPA 从这里 import，视图运行时把它打进 view.js。只放类型与纯函数，不依赖应用状态，
// 也不并入 artifact 帧的 PageMessage（信封与握手相同，消息种类各自独立）。

export const VIEW_PROTOCOL_VERSION = 1;

/** 单条消息（JSON 序列化后的 UTF-8 字节数）的上限；超出的消息两边都直接丢弃。 */
export const MAX_VIEW_MESSAGE_BYTES = 64 * 1024;

/** 父页面为每个帧生成的通道标识：16 位小写十六进制。 */
export const VIEW_CHANNEL_PATTERN = /^[0-9a-f]{16}$/;

/** `prompt.text` 的长度上限（字符）。 */
export const MAX_VIEW_PROMPT_CHARS = 4000;

/** 帧的最小高度（px）。跟随内容时没有上限：帧一直长到装下全部内容，不在帧内滚动。 */
export const VIEW_MIN_HEIGHT = 120;

/**
 * 页面高度随帧高变化（`100vh`、`height: 100%` 一类布局）时运行时给出的高度（px）：按内容定高会一路撑高，
 * 运行时认出这种耦合后报一次这个高度，之后不再随视口变高（见 runtime.ts）。
 */
export const VIEW_FILL_HEIGHT = 480;

/** 主题变量（与 Core 的 SVG 约定、svg-theme.ts 的映射同名）。父页面只发这些，运行时只认这些。 */
export const VIEW_THEME_VARS = [
	"--fg",
	"--muted",
	"--border",
	"--surface",
	"--accent",
	"--success",
	"--warning",
	"--error",
	"--c1",
	"--c2",
	"--c3",
	"--c4",
	"--c5",
	"--c6",
	"--bg",
	"--font",
	"--mono",
	"--radius",
] as const;
export type ViewThemeVar = (typeof VIEW_THEME_VARS)[number];

/** 单个主题变量值的长度上限。 */
const MAX_THEME_VALUE_CHARS = 400;

export type ViewScheme = "light" | "dark";

export interface ViewTheme {
	scheme: ViewScheme;
	vars: Partial<Record<ViewThemeVar, string>>;
}

interface Envelope {
	v: typeof VIEW_PROTOCOL_VERSION;
	channel: string;
}

/** 父页面 → 视图。 */
export type ViewParentMessage = Envelope &
	({ kind: "hello" } | ({ kind: "theme" } & ViewTheme));

/** 视图 → 父页面。 */
export type ViewPageMessage = Envelope &
	(
		| { kind: "ready" }
		| { kind: "resize"; height: number }
		| { kind: "prompt"; text: string }
	);

type Body<M> = M extends unknown ? Omit<M, "v" | "channel"> : never;
export type ViewParentBody = Body<ViewParentMessage>;
export type ViewPageBody = Body<ViewPageMessage>;

// 先缓存，页面脚本之后改写全局的 JSON 不影响协议的编解码
const stringify = JSON.stringify;
const parse = JSON.parse;

function utf8Length(s: string): number {
	let n = 0;
	for (let i = 0; i < s.length; i++) {
		const c = s.charCodeAt(i);
		if (c < 0x80) n += 1;
		else if (c < 0x800) n += 2;
		else if (c >= 0xd800 && c <= 0xdbff) {
			n += 4;
			i++;
		} else n += 3;
	}
	return n;
}

/** 消息序列化后是否在上限之内。 */
export function fitsViewMessage(data: unknown): boolean {
	let json: string | undefined;
	try {
		json = stringify(data);
	} catch {
		return false;
	}
	return json !== undefined && utf8Length(json) <= MAX_VIEW_MESSAGE_BYTES;
}

type Fields = Record<string, unknown>;

function isFields(x: unknown): x is Fields {
	return typeof x === "object" && x !== null && !Array.isArray(x);
}

function envelope(data: unknown): (Fields & Envelope) | null {
	if (!isFields(data) || data.v !== VIEW_PROTOCOL_VERSION) return null;
	if (
		typeof data.channel !== "string" ||
		!VIEW_CHANNEL_PATTERN.test(data.channel)
	)
		return null;
	return fitsViewMessage(data) ? (data as Fields & Envelope) : null;
}

/** 去掉 CSS 值里能跳出声明的字符（`< > ; { }`），并截断。 */
export function cleanThemeValue(v: string): string {
	return v
		.replace(/[<>;{}]/g, "")
		.slice(0, MAX_THEME_VALUE_CHARS)
		.trim();
}

/** 校验并清洗主题：未知的变量名与非字符串值丢弃；scheme 不合法时整体无效。 */
export function cleanTheme(raw: unknown): ViewTheme | null {
	if (!isFields(raw)) return null;
	if (raw.scheme !== "light" && raw.scheme !== "dark") return null;
	const vars: Partial<Record<ViewThemeVar, string>> = {};
	if (isFields(raw.vars)) {
		for (const name of VIEW_THEME_VARS) {
			const v = raw.vars[name];
			if (typeof v !== "string") continue;
			const clean = cleanThemeValue(v);
			if (clean) vars[name] = clean;
		}
	}
	return { scheme: raw.scheme, vars };
}

/** 首帧主题走 URL 片段：`theme=<encodeURIComponent(JSON)>`（不带 `#`）。 */
export function encodeThemeFragment(theme: ViewTheme): string {
	return `theme=${encodeURIComponent(stringify(theme))}`;
}

/** 从 `location.hash` 读出主题；没有或不合法时为 `null`。 */
export function decodeThemeFragment(hash: string): ViewTheme | null {
	const m = /^#?theme=([^&]*)/.exec(hash);
	if (!m?.[1]) return null;
	try {
		return cleanTheme(parse(decodeURIComponent(m[1])));
	} catch {
		return null;
	}
}

export function parseViewParentMessage(
	data: unknown,
): ViewParentMessage | null {
	const m = envelope(data);
	if (!m) return null;
	const head: Envelope = { v: m.v, channel: m.channel };
	switch (m.kind) {
		case "hello":
			return { ...head, kind: "hello" };
		case "theme": {
			const theme = cleanTheme(m);
			return theme ? { ...head, kind: "theme", ...theme } : null;
		}
	}
	return null;
}

export function parseViewPageMessage(data: unknown): ViewPageMessage | null {
	const m = envelope(data);
	if (!m) return null;
	const head: Envelope = { v: m.v, channel: m.channel };
	switch (m.kind) {
		case "ready":
			return { ...head, kind: "ready" };
		case "resize":
			return typeof m.height === "number" &&
				Number.isFinite(m.height) &&
				m.height >= 0
				? { ...head, kind: "resize", height: m.height }
				: null;
		case "prompt": {
			if (typeof m.text !== "string") return null;
			const text = m.text.slice(0, MAX_VIEW_PROMPT_CHARS);
			return text.trim() ? { ...head, kind: "prompt", text } : null;
		}
	}
	return null;
}
