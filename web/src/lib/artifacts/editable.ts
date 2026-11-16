// 元素直接编辑的可编辑值：七个样式属性与文字的校验规则。
// 服务端 `internal/artifacts/edit.go` 有逐条相同的一份；沙箱运行时的 `preview-style` / `preview-text` 也用这里的函数，
// 所以这个文件只放纯函数，不 import 任何依赖应用状态的模块。

/** 可直接编辑的七个样式属性，顺序即编辑面板里的顺序。 */
export const EDITABLE_PROPS = [
	"color",
	"background-color",
	"font-size",
	"font-weight",
	"text-align",
	"padding",
	"border-radius",
] as const;

export type EditableProp = (typeof EDITABLE_PROPS)[number];

/** 七个属性各自的值（页面回报时是计算值，写回时是通过校验的值）。 */
export type EditableStyle = Record<EditableProp, string>;

/** 直接编辑的文字按码点计的上限（同 `limits.go` 的 `MaxEditTextRunes`）。 */
export const MAX_EDIT_TEXT_RUNES = 10_000;

const HEX_COLOR = /^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$/;
const PX = /^(0|[1-9][0-9]{0,3})(\.[0-9]{1,2})?px$/;
const FONT_WEIGHT = /^[1-9]00$/;
const TEXT_ALIGN: readonly string[] = ["left", "center", "right", "justify"];

export function isEditableProp(name: string): name is EditableProp {
	return (EDITABLE_PROPS as readonly string[]).includes(name);
}

/** `prop` 的值 `value` 是否可以写进元素的 `style`。 */
export function validStyleValue(prop: EditableProp, value: string): boolean {
	switch (prop) {
		case "color":
		case "background-color":
			return HEX_COLOR.test(value);
		case "font-size":
			return PX.test(value) && Number.parseFloat(value) >= 1;
		case "border-radius":
			return PX.test(value);
		case "font-weight":
			return FONT_WEIGHT.test(value);
		case "text-align":
			return TEXT_ALIGN.includes(value);
		case "padding": {
			const parts = value.split(" ");
			return (
				parts.length >= 1 && parts.length <= 4 && parts.every((p) => PX.test(p))
			);
		}
	}
}

/** 只留下七个属性里值通过校验的项；其余（未知属性、非字符串、不合规的值）一律丢弃。 */
export function cleanStyle(style: unknown): Partial<EditableStyle> {
	const out: Partial<EditableStyle> = {};
	if (typeof style !== "object" || style === null || Array.isArray(style)) {
		return out;
	}
	const src = style as Record<string, unknown>;
	for (const prop of EDITABLE_PROPS) {
		if (!Object.hasOwn(src, prop)) continue;
		const value = src[prop];
		if (typeof value === "string" && validStyleValue(prop, value)) {
			out[prop] = value;
		}
	}
	return out;
}

/** 按码点计的长度。 */
export function runeCount(s: string): number {
	let n = 0;
	for (let i = 0; i < s.length; i++) {
		const c = s.charCodeAt(i);
		// 高代理后紧跟低代理算一个码点
		if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
			const d = s.charCodeAt(i + 1);
			if (d >= 0xdc00 && d <= 0xdfff) i++;
		}
		n++;
	}
	return n;
}

/** 直接编辑的文字：字符串且不超过 `MAX_EDIT_TEXT_RUNES` 个码点。 */
export function validEditText(text: unknown): text is string {
	return typeof text === "string" && runeCount(text) <= MAX_EDIT_TEXT_RUNES;
}

/** 编辑面板颜色框的输入：可以省略 `#`，3 / 4 位简写展开成 6 / 8 位，结果小写；不是十六进制颜色回 `null`。 */
export function expandHex(input: string): string | null {
	const s = input.trim().replace(/^#/, "").toLowerCase();
	if (!/^[0-9a-f]+$/.test(s)) return null;
	if (s.length === 3 || s.length === 4) {
		return `#${Array.from(s, (c) => c + c).join("")}`;
	}
	return s.length === 6 || s.length === 8 ? `#${s}` : null;
}

const RGB =
	/^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/i;

function hexByte(n: number): string {
	return Math.round(Math.min(255, Math.max(0, n)))
		.toString(16)
		.padStart(2, "0");
}

/** 页面回报的计算颜色（`rgb(…)`、`rgba(…)` 或十六进制）换成面板用的十六进制；不透明时 6 位，否则 8 位。不认识的写法回空串。 */
export function hexFromCss(value: string): string {
	const v = value.trim();
	if (v.startsWith("#")) return expandHex(v) ?? "";
	const m = RGB.exec(v);
	if (!m) return "";
	const [r, g, b] = [m[1], m[2], m[3]].map(Number) as [number, number, number];
	const alpha = m[4];
	const a =
		alpha === undefined
			? 1
			: alpha.endsWith("%")
				? Number.parseFloat(alpha) / 100
				: Number(alpha);
	if (![r, g, b, a].every(Number.isFinite)) return "";
	const rgb = `#${hexByte(r)}${hexByte(g)}${hexByte(b)}`;
	return a >= 1 ? rgb : rgb + hexByte(a * 255);
}

/** 计算长度（如 `13.3333px`）规整成可以写回的 px 值：最多两位小数；不是 px、负数或不小于 10000 的回空串。 */
export function pxFromCss(value: string): string {
	const m = /^(-?\d*\.?\d+)px$/.exec(value.trim());
	if (!m) return "";
	const n = Math.round(Number(m[1]) * 100) / 100;
	if (!Number.isFinite(n) || n < 0 || n >= 10_000) return "";
	return `${n}px`;
}

/** 计算字重换成 100–900 的整百（`bold` = 700，`normal` = 400）；不认识的回空串。 */
export function weightFromCss(value: string): string {
	const v = value.trim();
	const n = v === "bold" ? 700 : v === "normal" ? 400 : Number(v);
	if (v === "" || !Number.isFinite(n)) return "";
	return String(Math.min(900, Math.max(100, Math.round(n / 100) * 100)));
}

/** 计算对齐换成面板的三档（`start` 按左、`end` 按右）；`justify` 也原样保留，其余回空串。 */
export function alignFromCss(value: string): string {
	const v = value.trim().replace(/^-webkit-/, "");
	if (v === "start") return "left";
	if (v === "end") return "right";
	return TEXT_ALIGN.includes(v) ? v : "";
}

/** 页面回报的七个计算值换成面板的初始值；每一项要么通过 `validStyleValue`，要么是空串（不认识）。 */
export function panelStyle(computed: EditableStyle): EditableStyle {
	const padding = computed.padding.trim().split(/\s+/).map(pxFromCss);
	const out: EditableStyle = {
		color: hexFromCss(computed.color),
		"background-color": hexFromCss(computed["background-color"]),
		"font-size": pxFromCss(computed["font-size"]),
		"font-weight": weightFromCss(computed["font-weight"]),
		"text-align": alignFromCss(computed["text-align"]),
		padding:
			padding.length <= 4 && padding.every((p) => p !== "")
				? padding.join(" ")
				: "",
		"border-radius": pxFromCss(computed["border-radius"]),
	};
	for (const prop of EDITABLE_PROPS) {
		if (out[prop] !== "" && !validStyleValue(prop, out[prop])) out[prop] = "";
	}
	return out;
}

const MARKUP_AFTER_LT = /[A-Za-z/!?]/;

/**
 * 源文件字节 `src` 里 `offset` 处起始标签 `tag` 的文字内容（解码实体之前的原文）：起始标签之后到同名结束标签之间
 * 不能有别的标签或注释，否则回 `null`。页面回报的摘录只有前 120 个字符且折叠了空白，文字框要填完整的原文；
 * 能不能改以服务端的判断为准。
 */
export function sourceTextAt(
	src: Uint8Array,
	offset: number,
	tag: string,
): string | null {
	if (offset < 0 || offset >= src.length || src[offset] !== 0x3c) return null;
	// offset 处是 ASCII 的 `<`，从这里解码不会切开多字节字符
	const s = new TextDecoder().decode(src.subarray(offset));
	const isEnd = (c: string | undefined) =>
		c === undefined || c === ">" || c === "/" || /\s/.test(c);
	if (s.slice(1, 1 + tag.length).toLowerCase() !== tag) return null;
	if (!isEnd(s[1 + tag.length])) return null;
	let i = 1 + tag.length;
	let quote = "";
	for (; i < s.length; i++) {
		const c = s[i];
		if (quote) {
			if (c === quote) quote = "";
		} else if (c === '"' || c === "'") quote = c;
		else if (c === ">") break;
	}
	if (i >= s.length || s[i - 1] === "/") return null;
	const start = i + 1;
	const close = `</${tag}`;
	for (let j = s.indexOf("<", start); j >= 0; j = s.indexOf("<", j + 1)) {
		if (!MARKUP_AFTER_LT.test(s[j + 1] ?? "")) continue;
		if (
			s.slice(j, j + close.length).toLowerCase() === close &&
			isEnd(s[j + close.length]) &&
			s[j + close.length] !== "/"
		) {
			return s.slice(start, j);
		}
		return null;
	}
	return null;
}
