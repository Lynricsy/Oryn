/**
 * 外观主题：明暗模式（跟随系统 / 浅色 / 深色）与主题色预设。
 * 只存本机浏览器（localStorage），写到 `<html data-theme data-accent>`；令牌值在 index.css。
 * `public/theme-init.js` 在首帧前按同样规则写一次，避免闪色。
 */
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import { cssColor } from "../lib/css-color";

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
export const THEME_KEY = "oryn.theme";
/** 主题实际应用后在 window 上派发；mermaid、xterm 据此重绘。 */
export const THEME_EVENT = "oryn:themechange";

export interface ThemeState {
	mode: ThemeMode;
	accent: AccentName;
	setMode(m: ThemeMode): void;
	setAccent(a: AccentName): void;
	cycleMode(): void;
}

const DEFAULT = { mode: "system", accent: "teal" } as const;

function isMode(v: unknown): v is ThemeMode {
	return (MODES as readonly unknown[]).includes(v);
}

function isAccent(v: unknown): v is AccentName {
	return (ACCENTS as readonly unknown[]).includes(v);
}

/** 解析存储值；非法 JSON 或字段逐项回落默认。 */
export function parseTheme(raw: string | null): {
	mode: ThemeMode;
	accent: AccentName;
} {
	if (!raw) return { ...DEFAULT };
	try {
		const v: unknown = JSON.parse(raw);
		if (!v || typeof v !== "object") return { ...DEFAULT };
		const o = v as Record<string, unknown>;
		return {
			mode: isMode(o.mode) ? o.mode : DEFAULT.mode,
			accent: isAccent(o.accent) ? o.accent : DEFAULT.accent,
		};
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

/** 把主题写到根元素并同步 `<meta name="theme-color">`，然后派发 THEME_EVENT。 */
export function applyTheme(mode: ThemeMode, accent: AccentName): void {
	if (typeof document === "undefined") return;
	const root = document.documentElement;
	root.dataset.theme = resolveMode(mode, darkQuery()?.matches ?? false);
	if (accent === "teal") delete root.dataset.accent;
	else root.dataset.accent = accent;
	const meta = document.querySelector<HTMLMetaElement>(
		'meta[name="theme-color"]',
	);
	if (meta) meta.content = cssColor("--bg", meta.content || "#eef0f4");
	window.dispatchEvent(new Event(THEME_EVENT));
}

function load(): { mode: ThemeMode; accent: AccentName } {
	try {
		return parseTheme(globalThis.localStorage?.getItem(THEME_KEY) ?? null);
	} catch {
		// 隐私模式下 localStorage 不可用
		return { ...DEFAULT };
	}
}

function save(mode: ThemeMode, accent: AccentName): void {
	try {
		globalThis.localStorage?.setItem(
			THEME_KEY,
			JSON.stringify({ mode, accent }),
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
	(set, get) => ({
		...load(),
		setMode(mode) {
			set({ mode });
			save(mode, get().accent);
		},
		setAccent(accent) {
			set({ accent });
			save(get().mode, accent);
		},
		cycleMode() {
			get().setMode(NEXT_MODE[get().mode]);
		},
	}),
);

export function useTheme<T>(selector: (s: ThemeState) => T): T {
	return useStore(themeStore, selector);
}

let installed = false;

/** 渲染前调用一次：应用当前主题，订阅 store 与系统明暗变化。 */
export function installTheme(): void {
	if (installed) return;
	installed = true;
	const { mode, accent } = themeStore.getState();
	applyTheme(mode, accent);
	themeStore.subscribe((s, prev) => {
		if (s.mode !== prev.mode || s.accent !== prev.accent)
			applyTheme(s.mode, s.accent);
	});
	darkQuery()?.addEventListener("change", () => {
		const s = themeStore.getState();
		if (s.mode === "system") applyTheme(s.mode, s.accent);
	});
}
