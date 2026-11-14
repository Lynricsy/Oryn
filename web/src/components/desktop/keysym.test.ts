import { describe, expect, it } from "vitest";
import { KEYSYM, keysymOf } from "./keysym";

describe("keysymOf", () => {
	it("可见 ASCII 与 Latin-1 取码点本身", () => {
		expect(keysymOf("a")).toBe(0x61);
		expect(keysymOf(" ")).toBe(0x20);
		expect(keysymOf("~")).toBe(0x7e);
		expect(keysymOf("é")).toBe(0xe9);
		expect(keysymOf("\u00a0")).toBe(0xa0);
	});

	it("其余 Unicode 取 0x01000000 + 码点（含中文与补充平面）", () => {
		expect(keysymOf("中")).toBe(0x01000000 + 0x4e2d);
		expect(keysymOf("€")).toBe(0x01000000 + 0x20ac);
		expect(keysymOf("😀")).toBe(0x01000000 + 0x1f600);
	});

	it("换行与制表符映射为 Return / Tab", () => {
		expect(keysymOf("\n")).toBe(KEYSYM.Return);
		expect(keysymOf("\r")).toBe(KEYSYM.Return);
		expect(keysymOf("\t")).toBe(KEYSYM.Tab);
	});

	it("常量与 X11 keysymdef 一致", () => {
		expect(KEYSYM).toEqual({
			BackSpace: 0xff08,
			Tab: 0xff09,
			Return: 0xff0d,
			Escape: 0xff1b,
			Control_L: 0xffe3,
			Alt_L: 0xffe9,
			Left: 0xff51,
			Up: 0xff52,
			Right: 0xff53,
			Down: 0xff54,
		});
	});
});
