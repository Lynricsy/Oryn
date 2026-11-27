/**
 * 模型选择器的本机偏好：收藏与最近使用（按 `provider/id` 记，不随账户同步）。
 * 每次读都对照 localStorage 原文（原文没变就复用上次解析的结果，满足 useSyncExternalStore 的稳定快照），
 * 别处清空或改写 localStorage（别的标签页、测试的 `localStorage.clear()`）都能立刻跟上。
 */
import { useSyncExternalStore } from "react";

export const MODEL_FAVORITES_KEY = "oryn.models.favorites";
export const MODEL_RECENT_KEY = "oryn.models.recent";
/** 最近使用只留这么多条（新的在前）。 */
export const RECENT_LIMIT = 5;

export interface ModelPrefs {
	favorites: readonly string[];
	recent: readonly string[];
}

const EMPTY: ModelPrefs = { favorites: [], recent: [] };

function readRaw(key: string): string | null {
	try {
		return globalThis.localStorage?.getItem(key) ?? null;
	} catch {
		return null;
	}
}

function parseList(raw: string | null): string[] {
	if (!raw) return [];
	try {
		const v: unknown = JSON.parse(raw);
		return Array.isArray(v)
			? v.filter((x): x is string => typeof x === "string" && x.length > 0)
			: [];
	} catch {
		return [];
	}
}

let lastKey = "";
let last: ModelPrefs = EMPTY;
const listeners = new Set<() => void>();

export function getModelPrefs(): ModelPrefs {
	const fav = readRaw(MODEL_FAVORITES_KEY);
	const rec = readRaw(MODEL_RECENT_KEY);
	const key = `${fav}\u0000${rec}`;
	if (key !== lastKey) {
		lastKey = key;
		last =
			fav === null && rec === null
				? EMPTY
				: { favorites: parseList(fav), recent: parseList(rec) };
	}
	return last;
}

function write(key: string, list: readonly string[]): void {
	try {
		globalThis.localStorage?.setItem(key, JSON.stringify(list));
	} catch {
		// 隐私模式下 localStorage 不可用：偏好只是锦上添花，静默放弃
	}
	for (const l of listeners) l();
}

/** 收藏 / 取消收藏一个模型。 */
export function toggleFavoriteModel(selector: string): void {
	const { favorites } = getModelPrefs();
	write(
		MODEL_FAVORITES_KEY,
		favorites.includes(selector)
			? favorites.filter((s) => s !== selector)
			: [...favorites, selector],
	);
}

/** 记一次使用：挪到最前，超出上限的丢掉。 */
export function noteRecentModel(selector: string): void {
	const { recent } = getModelPrefs();
	write(
		MODEL_RECENT_KEY,
		[selector, ...recent.filter((s) => s !== selector)].slice(0, RECENT_LIMIT),
	);
}

function subscribe(l: () => void): () => void {
	const onStorage = (e: StorageEvent) => {
		if (
			e.key === null ||
			e.key === MODEL_FAVORITES_KEY ||
			e.key === MODEL_RECENT_KEY
		)
			l();
	};
	listeners.add(l);
	window.addEventListener("storage", onStorage);
	return () => {
		listeners.delete(l);
		window.removeEventListener("storage", onStorage);
	};
}

export function useModelPrefs(): ModelPrefs {
	return useSyncExternalStore(subscribe, getModelPrefs, () => EMPTY);
}
