/**
 * 未保存的文件草稿：按 `来源 id + 路径` 存在模块级 store 里，不随 `FileViewer` / 面板 / 线程卸载而丢失。
 * 草稿记住它所基于的磁盘内容与 etag：保存时带这个 etag（期间被他人改过即 412），回来时也能看出磁盘已变。
 * 草稿只会因用户的选择而消失：保存成功（且保存后没有再编辑）、改回原样、冲突时选「重新加载」、「放弃改动」、删除文件。
 * 存在草稿期间挂 `beforeunload`，关闭 / 刷新页面前由浏览器询问。
 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { isWithin } from "../../lib/file-source";

export interface Draft {
	text: string;
	/** 草稿基于的磁盘内容（改回它即不再算草稿） */
	baseText: string;
	/** 草稿基于的版本；保存时作为 `If-Match` */
	etag?: string;
	/** 每次编辑递增：保存完成时只清除提交时那个版本的草稿 */
	rev: number;
}

interface DraftsState {
	drafts: Record<string, Draft>;
}

export const draftStore = createStore<DraftsState>(() => ({ drafts: {} }));

export function draftKey(sourceId: string, path: string): string {
	return `${sourceId}|${path}`;
}

function splitKey(key: string): [string, string] {
	const i = key.indexOf("|");
	return [key.slice(0, i), key.slice(i + 1)];
}

function setDrafts(fn: (d: Record<string, Draft>) => Record<string, Draft>) {
	draftStore.setState((s) => ({ drafts: fn(s.drafts) }));
}

function without(d: Record<string, Draft>, key: string) {
	if (!(key in d)) return d;
	const { [key]: _gone, ...rest } = d;
	return rest;
}

/** 记录一次编辑；`base` 只在新建草稿时采用。改回基线内容即删除草稿。 */
export function editDraft(
	key: string,
	text: string,
	base: { text: string; etag?: string },
): void {
	setDrafts((d) => {
		const cur = d[key];
		const next: Draft = cur
			? { ...cur, text, rev: cur.rev + 1 }
			: { text, baseText: base.text, etag: base.etag, rev: 1 };
		if (next.text === next.baseText) return without(d, key);
		return { ...d, [key]: next };
	});
}

/**
 * 保存成功：基线前移到已写入的内容与新 etag。
 * 提交后又有编辑（`rev` 不同）时保留较新的草稿，否则清除。
 */
export function markSaved(
	key: string,
	submittedRev: number,
	savedText: string,
	etag: string | undefined,
): void {
	setDrafts((d) => {
		const cur = d[key];
		if (!cur) return d;
		if (cur.rev === submittedRev || cur.text === savedText)
			return without(d, key);
		return { ...d, [key]: { ...cur, baseText: savedText, etag } };
	});
}

export function discardDraft(key: string): void {
	setDrafts((d) => without(d, key));
}

/** 重命名：`from`（文件或目录）下的草稿跟随到 `to`。 */
export function moveDrafts(sourceId: string, from: string, to: string): void {
	setDrafts((d) => {
		let out = d;
		for (const [key, draft] of Object.entries(d)) {
			const [sid, path] = splitKey(key);
			if (sid !== sourceId || !isWithin(path, from)) continue;
			out = without(out, key);
			out = {
				...out,
				[draftKey(sid, to + path.slice(from.length))]: draft,
			};
		}
		return out;
	});
}

/** 删除：`path`（文件或目录）下的草稿一并丢弃。 */
export function dropDrafts(sourceId: string, path: string): void {
	setDrafts((d) => {
		let out = d;
		for (const key of Object.keys(d)) {
			const [sid, p] = splitKey(key);
			if (sid === sourceId && isWithin(p, path)) out = without(out, key);
		}
		return out;
	});
}

export function useDraft(key: string): Draft | undefined {
	return useStore(draftStore, (s) => s.drafts[key]);
}

/** 某个来源下有草稿的路径。 */
export function useDraftPaths(sourceId: string | undefined): string[] {
	const drafts = useStore(draftStore, (s) => s.drafts);
	return Object.keys(drafts)
		.map(splitKey)
		.filter(([sid]) => sid === sourceId)
		.map(([, p]) => p);
}

// ---------------------------------------------------------------- 关闭页面保护

function onBeforeUnload(e: BeforeUnloadEvent) {
	e.preventDefault();
	// 旧版浏览器需要 returnValue 才弹确认
	e.returnValue = "";
}

let guarded = false;
function syncUnloadGuard({ drafts }: DraftsState) {
	const dirty = Object.keys(drafts).length > 0;
	if (dirty === guarded || typeof window === "undefined") return;
	guarded = dirty;
	if (dirty) window.addEventListener("beforeunload", onBeforeUnload);
	else window.removeEventListener("beforeunload", onBeforeUnload);
}
draftStore.subscribe(syncUnloadGuard);
