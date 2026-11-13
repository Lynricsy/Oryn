/**
 * 外观主题：明暗模式（跟随系统 / 浅色 / 深色）、主题色预设与壁纸。
 * 只存本机浏览器（localStorage `oryn.theme`），写到 `<html data-theme data-accent data-wallpaper>`；令牌值在 index.css。
 * 自定义壁纸的图片另存 `oryn.wallpaper.image`（模糊后的 data URL，见 lib/wallpaper），经 `<html>` 的内联变量
 * `--wallpaper-image` 交给 `.shell-wash`；亮度分位 `tone` 写成 `--wp-lo / --wp-hi`，index.css 据此加厚壁纸上的纱。
 * `public/theme-init.js` 在首帧前按同样规则写一次，避免闪色。
 */
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import { cssColor } from "../lib/css-color";
import {
	clearWallpaperImage,
	readWallpaperImage,
	storeWallpaperImage,
	type WallpaperTone,
} from "../lib/wallpaper";

export type ThemeMode = "system" | "light" | "dark";
export const MODES = [
	"system",
	"light",
	"dark",
] as const satisfies readonly ThemeMode[];
export const ACCENTS = [
	"teal",
	"cobalt",
	"violet",
	"berry",
	"fox",
	"graphite",
] as const;
export type AccentName = (typeof ACCENTS)[number];
export const WALLPAPERS = ["aurora", "waves", "grain", "custom"] as const;
export type WallpaperName = (typeof WALLPAPERS)[number];
export const THEME_KEY = "oryn.theme";
/** 主题实际应用后在 window 上派发；mermaid、xterm 据此重绘。 */
export const THEME_EVENT = "oryn:themechange";

export interface StoredTheme {
	mode: ThemeMode;
	accent: AccentName;
	wallpaper: WallpaperName;
	/** 自定义壁纸的亮度分位，仅 wallpaper=custom 时有意义 */
	tone?: WallpaperTone;
}

export interface ThemeState extends StoredTheme {
	setMode(m: ThemeMode): void;
	setAccent(a: AccentName): void;
	/** 选内置壁纸；选 custom 时本机须已有图片，否则忽略 */
	setWallpaper(w: WallpaperName): void;
	/** 存入处理好的图片并切到 custom；存储失败抛 WallpaperStorageError，状态不变 */
	setCustomWallpaper(url: string, tone: WallpaperTone): void;
	/** 删除图片并回到默认壁纸 */
	removeCustomWallpaper(): void;
	cycleMode(): void;
}

const DEFAULT = {
	mode: "system",
	accent: "teal",
	wallpaper: "aurora",
} as const;

function isMode(v: unknown): v is ThemeMode {
	return (MODES as readonly unknown[]).includes(v);
}

function isAccent(v: unknown): v is AccentName {
	return (ACCENTS as readonly unknown[]).includes(v);
}

function isWallpaper(v: unknown): v is WallpaperName {
	return (WALLPAPERS as readonly unknown[]).includes(v);
}

function isTone(v: unknown): v is WallpaperTone {
	return (
		Array.isArray(v) &&
		v.length === 2 &&
		v.every((x) => typeof x === "number" && x >= 0 && x <= 1)
	);
}

/** 解析存储值；非法 JSON 或字段逐项回落默认（旧数据没有 wallpaper 时为 aurora）。 */
export function parseTheme(raw: string | null): StoredTheme {
	if (!raw) return { ...DEFAULT };
	try {
		const v: unknown = JSON.parse(raw);
		if (!v || typeof v !== "object") return { ...DEFAULT };
		const o = v as Record<string, unknown>;
		const out: StoredTheme = {
			mode: isMode(o.mode) ? o.mode : DEFAULT.mode,
			accent: isAccent(o.accent) ? o.accent : DEFAULT.accent,
			wallpaper: isWallpaper(o.wallpaper) ? o.wallpaper : DEFAULT.wallpaper,
		};
		if (isTone(o.tone)) out.tone = o.tone;
		return out;
	} catch {
		return { ...DEFAULT };
	}
}

export function resolveMode(
	mode: ThemeMode,
	prefersDark: boolean,
): "light" | "dark" {
	if (mode === "system") return prefersDark ? "dark" : "light";
	return mode;
}

function darkQuery(): MediaQueryList | null {
	if (typeof window === "undefined" || typeof window.matchMedia !== "function")
		return null;
	return window.matchMedia("(prefers-color-scheme: dark)");
}

/**
 * 把主题写到根元素并同步 `<meta name="theme-color">`，然后派发 THEME_EVENT。
 * custom 壁纸在本机没有图片时按 aurora 写。
 */
export function applyTheme(
	mode: ThemeMode,
	accent: AccentName,
	wallpaper: WallpaperName = DEFAULT.wallpaper,
	tone?: WallpaperTone,
): void {
	if (typeof document === "undefined") return;
	const root = document.documentElement;
	root.dataset.theme = resolveMode(mode, darkQuery()?.matches ?? false);
	if (accent === "teal") delete root.dataset.accent;
	else root.dataset.accent = accent;
	const image = wallpaper === "custom" ? readWallpaperImage() : null;
	if (image) {
		root.style.setProperty("--wallpaper-image", `url("${image}")`);
		root.style.setProperty("--wp-lo", String(tone?.[0] ?? 0.5));
		root.style.setProperty("--wp-hi", String(tone?.[1] ?? 0.5));
	} else
		for (const p of ["--wallpaper-image", "--wp-lo", "--wp-hi"])
			root.style.removeProperty(p);
	root.dataset.wallpaper =
		wallpaper === "custom" && !image ? DEFAULT.wallpaper : wallpaper;
	const meta = document.querySelector<HTMLMetaElement>(
		'meta[name="theme-color"]',
	);
	if (meta) meta.content = cssColor("--bg", meta.content || "#eef0f4");
	window.dispatchEvent(new Event(THEME_EVENT));
}

function load(): StoredTheme {
	let stored: StoredTheme;
	try {
		stored = parseTheme(globalThis.localStorage?.getItem(THEME_KEY) ?? null);
	} catch {
		// 隐私模式下 localStorage 不可用
		return { ...DEFAULT };
	}
	// 图片被清掉（或从未存成）时不停在 custom
	if (stored.wallpaper === "custom" && !readWallpaperImage())
		return { mode: stored.mode, accent: stored.accent, wallpaper: "aurora" };
	return stored;
}

function save({ mode, accent, wallpaper, tone }: StoredTheme): void {
	try {
		globalThis.localStorage?.setItem(
			THEME_KEY,
			JSON.stringify({ mode, accent, wallpaper, tone }),
		);
	} catch {
		// 隐私模式下静默
	}
}

const NEXT_MODE: Record<ThemeMode, ThemeMode> = {
	system: "light",
	light: "dark",
	dark: "system",
};

export const themeStore: StoreApi<ThemeState> = createStore<ThemeState>(
	(set, get) => {
		const commit = (patch: Partial<StoredTheme>) => {
			set(patch);
			save(get());
		};
		return {
			...load(),
			setMode: (mode) => commit({ mode }),
			setAccent: (accent) => commit({ accent }),
			setWallpaper(wallpaper) {
				if (wallpaper === "custom" && !readWallpaperImage()) return;
				commit({ wallpaper });
			},
			setCustomWallpaper(url, tone) {
				storeWallpaperImage(url);
				commit({ wallpaper: "custom", tone });
			},
			removeCustomWallpaper() {
				clearWallpaperImage();
				commit({ wallpaper: DEFAULT.wallpaper, tone: undefined });
			},
			cycleMode() {
				get().setMode(NEXT_MODE[get().mode]);
			},
		};
	},
);

export function useTheme<T>(selector: (s: ThemeState) => T): T {
	return useStore(themeStore, selector);
}

let installed = false;

/** 渲染前调用一次：应用当前主题，订阅 store 与系统明暗变化。 */
export function installTheme(): void {
	if (installed) return;
	installed = true;
	const apply = (s: StoredTheme) =>
		applyTheme(s.mode, s.accent, s.wallpaper, s.tone);
	apply(themeStore.getState());
	themeStore.subscribe((s, prev) => {
		if (
			s.mode !== prev.mode ||
			s.accent !== prev.accent ||
			s.wallpaper !== prev.wallpaper ||
			s.tone !== prev.tone
		)
			apply(s);
	});
	darkQuery()?.addEventListener("change", () => {
		const s = themeStore.getState();
		if (s.mode === "system") apply(s);
	});
}
