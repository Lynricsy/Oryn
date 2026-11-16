import { describe, expect, it } from "vitest";
import {
	alignFromCss,
	cleanStyle,
	EDITABLE_PROPS,
	expandHex,
	hexFromCss,
	MAX_EDIT_TEXT_RUNES,
	panelStyle,
	pxFromCss,
	runeCount,
	sourceTextAt,
	validEditText,
	validStyleValue,
	weightFromCss,
} from "./editable";

describe("可编辑值的校验", () => {
	it.each([
		["color", "#ff0000", true],
		["color", "#FF000080", true],
		["color", "#f00", false],
		["color", "red", false],
		["background-color", "#00000", false],
		["font-size", "16px", true],
		["font-size", "1px", true],
		["font-size", "0.5px", false],
		["font-size", "0px", false],
		["font-size", "12.25px", true],
		["font-size", "12.255px", false],
		["font-size", "10000px", false],
		["font-size", "16", false],
		["border-radius", "0px", true],
		["border-radius", "01px", false],
		["font-weight", "700", true],
		["font-weight", "1000", false],
		["font-weight", "bold", false],
		["text-align", "justify", true],
		["text-align", "start", false],
		["padding", "4px", true],
		["padding", "1px 2px 3px 4.5px", true],
		["padding", "1px 2px 3px 4px 5px", false],
		["padding", "1px  2px", false],
		["padding", " 1px", false],
		["padding", "", false],
	] as const)("%s: %s → %s", (prop, value, ok) => {
		expect(validStyleValue(prop, value)).toBe(ok);
	});

	it("cleanStyle 只留七个属性里合规的项", () => {
		expect(
			cleanStyle({
				color: "#123456",
				"font-weight": 400,
				margin: "1px",
				"text-align": "center",
			}),
		).toEqual({ color: "#123456", "text-align": "center" });
		expect(cleanStyle(null)).toEqual({});
		expect(cleanStyle(["#123456"])).toEqual({});
		expect(EDITABLE_PROPS).toHaveLength(7);
	});

	it("文字按码点计上限", () => {
		expect(runeCount("a中😀")).toBe(3);
		expect(validEditText("😀".repeat(MAX_EDIT_TEXT_RUNES))).toBe(true);
		expect(validEditText("a".repeat(MAX_EDIT_TEXT_RUNES + 1))).toBe(false);
		expect(validEditText(1)).toBe(false);
	});
});

describe("编辑面板的值", () => {
	it.each([
		["#abc", "#aabbcc"],
		["ABC", "#aabbcc"],
		["#abcd", "#aabbccdd"],
		["#A1B2C3", "#a1b2c3"],
		[" 11223344 ", "#11223344"],
		["#abcde", null],
		["red", null],
		["", null],
	])("expandHex(%j) → %j", (input, want) => {
		const got = expandHex(input);
		expect(got).toBe(want);
		if (got) expect(validStyleValue("color", got)).toBe(true);
	});

	it.each([
		["rgb(255, 0, 0)", "#ff0000"],
		["rgba(0, 0, 0, 0)", "#00000000"],
		["rgba(16, 32, 48, 0.5)", "#10203080"],
		["rgb(1 2 3 / 50%)", "#01020380"],
		["#F00", "#ff0000"],
		["oklch(0.5 0.1 200)", ""],
	])("hexFromCss(%j) → %j", (input, want) => {
		expect(hexFromCss(input)).toBe(want);
	});

	it("长度、字重与对齐规整成可写回的值", () => {
		expect(pxFromCss("13.3333px")).toBe("13.33px");
		expect(pxFromCss("0px")).toBe("0px");
		expect(pxFromCss("-2px")).toBe("");
		expect(pxFromCss("1em")).toBe("");
		expect(weightFromCss("700")).toBe("700");
		expect(weightFromCss("450")).toBe("500");
		expect(weightFromCss("bold")).toBe("700");
		expect(weightFromCss("")).toBe("");
		expect(alignFromCss("start")).toBe("left");
		expect(alignFromCss("-webkit-center")).toBe("center");
		expect(alignFromCss("match-parent")).toBe("");
	});

	it("panelStyle 的每一项要么合规、要么为空", () => {
		const s = panelStyle({
			color: "rgb(24, 24, 27)",
			"background-color": "rgba(0, 0, 0, 0)",
			"font-size": "13.3333px",
			"font-weight": "400",
			"text-align": "start",
			padding: "8px 16.5px",
			"border-radius": "1e+06px",
		});
		expect(s).toEqual({
			color: "#18181b",
			"background-color": "#00000000",
			"font-size": "13.33px",
			"font-weight": "400",
			"text-align": "left",
			padding: "8px 16.5px",
			"border-radius": "",
		});
	});

	it("sourceTextAt 取起始标签之后到结束标签之间的原文", () => {
		const html = `<div><h1 class="a>b" data-x='1'>  标题 &amp; 😀\n 第二行 </h1><p>a <b>b</b></p><br/><p>x < y</p><i>a</I ></div>`;
		const bytes = new TextEncoder().encode(html);
		const at = (needle: string) =>
			new TextEncoder().encode(html.slice(0, html.indexOf(needle))).length;
		expect(sourceTextAt(bytes, at("<h1"), "h1")).toBe(
			"  标题 &amp; 😀\n 第二行 ",
		);
		expect(sourceTextAt(bytes, at("<p>a"), "p")).toBeNull();
		expect(sourceTextAt(bytes, at("<br/>"), "br")).toBeNull();
		expect(sourceTextAt(bytes, at("<p>x"), "p")).toBe("x < y");
		expect(sourceTextAt(bytes, at("<i>"), "i")).toBe("a");
		expect(sourceTextAt(bytes, at("<h1"), "h2")).toBeNull();
		expect(sourceTextAt(bytes, at("<h1") + 1, "h1")).toBeNull();
	});
});
