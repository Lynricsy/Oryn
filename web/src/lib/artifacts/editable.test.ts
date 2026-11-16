import { describe, expect, it } from "vitest";
import {
	cleanStyle,
	EDITABLE_PROPS,
	MAX_EDIT_TEXT_RUNES,
	runeCount,
	validEditText,
	validStyleValue,
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
