import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { WALLPAPER_IMAGE_KEY } from "../lib/wallpaper";
import {
	applyTheme,
	parseTheme,
	resolveMode,
	THEME_KEY,
	themeStore,
} from "./theme";

const DEFAULTS = { mode: "system", accent: "teal", wallpaper: "aurora" };

describe("parseTheme", () => {
	it.each([
		["null", null],
		["非法 JSON", "{oops"],
		["非对象", "42"],
	])("%s 回落默认", (_, raw) => {
		expect(parseTheme(raw)).toEqual(DEFAULTS);
	});

	it("未知字段逐项回落", () => {
		expect(
			parseTheme(JSON.stringify({ mode: "dark", accent: "neon" })),
		).toEqual({ mode: "dark", accent: "teal", wallpaper: "aurora" });
		expect(parseTheme(JSON.stringify({ mode: "dim", accent: "fox" }))).toEqual({
			mode: "system",
			accent: "fox",
			wallpaper: "aurora",
		});
	});

	it("非法 wallpaper 与 tone 回落；合法值保留", () => {
		expect(
			parseTheme(
				JSON.stringify({ mode: "dark", wallpaper: "neon", tone: [2, "x"] }),
			),
		).toEqual({ mode: "dark", accent: "teal", wallpaper: "aurora" });
		expect(
			parseTheme(JSON.stringify({ wallpaper: "custom", tone: [0.2, 0.9] })),
		).toEqual({ ...DEFAULTS, wallpaper: "custom", tone: [0.2, 0.9] });
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
	const root = document.documentElement;
	afterEach(() => {
		delete root.dataset.theme;
		delete root.dataset.accent;
		delete root.dataset.wallpaper;
		root.removeAttribute("style");
		localStorage.clear();
	});

	it("写入 data-theme 与 data-accent；teal 不写 data-accent", () => {
		applyTheme("dark", "cobalt");
		expect({ ...root.dataset }).toEqual({
			theme: "dark",
			accent: "cobalt",
			wallpaper: "aurora",
		});
		applyTheme("light", "teal");
		expect({ ...root.dataset }).toEqual({
			theme: "light",
			wallpaper: "aurora",
		});
	});

	it("写入 data-wallpaper；custom 带上图片与亮度变量，没有图片时退回 aurora", () => {
		applyTheme("light", "teal", "waves");
		expect(root.dataset.wallpaper).toBe("waves");
		applyTheme("light", "teal", "custom", [0.2, 0.7]);
		expect(root.dataset.wallpaper).toBe("aurora");
		expect(root.style.getPropertyValue("--wallpaper-image")).toBe("");
		localStorage.setItem(WALLPAPER_IMAGE_KEY, "data:image/webp;base64,AAAA");
		applyTheme("light", "teal", "custom", [0.2, 0.7]);
		expect(root.dataset.wallpaper).toBe("custom");
		expect(root.style.getPropertyValue("--wallpaper-image")).toContain(
			"data:image/webp;base64,AAAA",
		);
		expect(root.style.getPropertyValue("--wp-lo")).toBe("0.2");
		expect(root.style.getPropertyValue("--wp-hi")).toBe("0.7");
		applyTheme("light", "teal", "grain");
		expect(root.style.getPropertyValue("--wallpaper-image")).toBe("");
	});
});

describe("themeStore", () => {
	beforeEach(() => {
		localStorage.clear();
		themeStore.setState({
			mode: "system",
			accent: "teal",
			wallpaper: "aurora",
			tone: undefined,
		});
	});

	it("cycleMode 三步回到原值并持久化", () => {
		const seen: string[] = [];
		for (let i = 0; i < 3; i++) {
			themeStore.getState().cycleMode();
			seen.push(themeStore.getState().mode);
		}
		expect(seen).toEqual(["light", "dark", "system"]);
		expect(JSON.parse(localStorage.getItem(THEME_KEY) ?? "null")).toEqual(
			DEFAULTS,
		);
	});

	it("setAccent 持久化当前模式与主题色", () => {
		themeStore.getState().setMode("dark");
		themeStore.getState().setAccent("fox");
		expect(JSON.parse(localStorage.getItem(THEME_KEY) ?? "null")).toEqual({
			mode: "dark",
			accent: "fox",
			wallpaper: "aurora",
		});
	});

	it("壁纸与 mode、accent 存在同一个 JSON；没有图片时不能选 custom", () => {
		themeStore.getState().setWallpaper("grain");
		themeStore.getState().setWallpaper("custom");
		expect(themeStore.getState().wallpaper).toBe("grain");
		expect(JSON.parse(localStorage.getItem(THEME_KEY) ?? "null")).toEqual({
			...DEFAULTS,
			wallpaper: "grain",
		});
	});

	it("自定义图片：存入后切到 custom，移除后回到 aurora 并删掉图片", () => {
		themeStore
			.getState()
			.setCustomWallpaper("data:image/webp;base64,BBBB", [0.3, 0.6]);
		expect(themeStore.getState()).toMatchObject({
			wallpaper: "custom",
			tone: [0.3, 0.6],
		});
		expect(localStorage.getItem(WALLPAPER_IMAGE_KEY)).toBe(
			"data:image/webp;base64,BBBB",
		);
		expect(JSON.parse(localStorage.getItem(THEME_KEY) ?? "null")).toEqual({
			...DEFAULTS,
			wallpaper: "custom",
			tone: [0.3, 0.6],
		});
		themeStore.getState().removeCustomWallpaper();
		expect(themeStore.getState().wallpaper).toBe("aurora");
		expect(localStorage.getItem(WALLPAPER_IMAGE_KEY)).toBeNull();
		expect(JSON.parse(localStorage.getItem(THEME_KEY) ?? "null")).toEqual(
			DEFAULTS,
		);
	});
});
