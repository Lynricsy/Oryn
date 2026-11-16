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
