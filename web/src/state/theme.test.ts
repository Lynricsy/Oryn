import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
	applyTheme,
	parseTheme,
	resolveMode,
	THEME_KEY,
	themeStore,
} from "./theme";

describe("parseTheme", () => {
	it.each([
		["null", null],
		["非法 JSON", "{oops"],
		["非对象", "42"],
	])("%s 回落默认", (_, raw) => {
		expect(parseTheme(raw)).toEqual({ mode: "system", accent: "teal" });
	});

	it("未知字段逐项回落", () => {
		expect(
			parseTheme(JSON.stringify({ mode: "dark", accent: "neon" })),
		).toEqual({ mode: "dark", accent: "teal" });
		expect(parseTheme(JSON.stringify({ mode: "dim", accent: "fox" }))).toEqual({
			mode: "system",
			accent: "fox",
		});
	});
});

describe("resolveMode", () => {
	it("system 跟随系统偏好，显式模式不受影响", () => {
		expect(resolveMode("system", true)).toBe("dark");
		expect(resolveMode("system", false)).toBe("light");
		expect(resolveMode("light", true)).toBe("light");
		expect(resolveMode("dark", false)).toBe("dark");
	});
});

describe("applyTheme", () => {
	afterEach(() => {
		delete document.documentElement.dataset.theme;
		delete document.documentElement.dataset.accent;
	});

	it("写入 data-theme 与 data-accent；teal 不写 data-accent", () => {
		applyTheme("dark", "cobalt");
		expect({ ...document.documentElement.dataset }).toEqual({
			theme: "dark",
			accent: "cobalt",
		});
		applyTheme("light", "teal");
		expect({ ...document.documentElement.dataset }).toEqual({ theme: "light" });
	});
});

describe("themeStore", () => {
	beforeEach(() => {
		localStorage.clear();
		themeStore.setState({ mode: "system", accent: "teal" });
	});

	it("cycleMode 三步回到原值并持久化", () => {
		const seen: string[] = [];
		for (let i = 0; i < 3; i++) {
			themeStore.getState().cycleMode();
			seen.push(themeStore.getState().mode);
		}
		expect(seen).toEqual(["light", "dark", "system"]);
		expect(JSON.parse(localStorage.getItem(THEME_KEY) ?? "null")).toEqual({
			mode: "system",
			accent: "teal",
		});
	});

	it("setAccent 持久化当前模式与主题色", () => {
		themeStore.getState().setMode("dark");
		themeStore.getState().setAccent("fox");
		expect(JSON.parse(localStorage.getItem(THEME_KEY) ?? "null")).toEqual({
			mode: "dark",
			accent: "fox",
		});
	});
});
