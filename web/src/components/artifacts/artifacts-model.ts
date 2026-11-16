/**
 * Artifacts 总览页（计划 M14 2.5）的纯逻辑：视图参数与 URL 查询串互转、筛选、三种排序、按项目与线程分组、
 * 卡片网格的方向键移动、导入时的字段名。不依赖 React 与 store，便于单测。
 */
import type { Artifact } from "../../api/artifact-queries";
import { type ArtifactKind, isArtifactKind } from "../../lib/artifacts/kinds";
import { fold } from "../../lib/palette-match";

export type KindTab = "all" | ArtifactKind;
export const KIND_TABS: readonly KindTab[] = [
	"all",
	"document",
	"slides",
	"web",
	"canvas",
];

export type SortKey = "recent" | "updated" | "name";
export const SORT_KEYS: readonly SortKey[] = ["recent", "updated", "name"];

export type LayoutKey = "none" | "project-thread";
export const LAYOUT_KEYS: readonly LayoutKey[] = ["none", "project-thread"];

/** 来源：线程产出（`agent`）或导入的（`import`）。 */
export type OriginKey = Artifact["origin"];
export const ORIGIN_KEYS: readonly OriginKey[] = ["agent", "import"];

/** 项目筛选里「无项目」的取值。 */
export const NO_PROJECT = "none";

export interface ArtifactsView {
	kind: KindTab;
	/** 项目 id 或 `NO_PROJECT`；空 = 不筛。 */
	projects: string[];
	/** 空 = 不筛。 */
	origins: OriginKey[];
	sort: SortKey;
	layout: LayoutKey;
	/** 标题搜索。 */
	q: string;
}

export const DEFAULT_VIEW: ArtifactsView = {
	kind: "all",
	projects: [],
	origins: [],
	sort: "recent",
	layout: "none",
	q: "",
};

function oneOf<T extends string>(
	all: readonly T[],
	raw: string | null,
	fallback: T,
): T {
	return all.find((x) => x === raw) ?? fallback;
}

/** 从查询串读视图；不认识的值回落到缺省。 */
export function parseView(sp: URLSearchParams): ArtifactsView {
	const kind = sp.get("kind");
	return {
		kind: isArtifactKind(kind) ? kind : "all",
		projects: [...new Set(sp.getAll("project").filter(Boolean))],
		origins: [
			...new Set(
				sp
					.getAll("origin")
					.filter((o): o is OriginKey => ORIGIN_KEYS.includes(o as OriginKey)),
			),
		],
		sort: oneOf(SORT_KEYS, sp.get("sort"), DEFAULT_VIEW.sort),
		layout: oneOf(LAYOUT_KEYS, sp.get("layout"), DEFAULT_VIEW.layout),
		q: sp.get("q") ?? "",
	};
}

/** 视图写回查询串：只写与缺省不同的项（刷新、分享后视图不变）。 */
export function viewSearch(v: ArtifactsView): string {
	const sp = new URLSearchParams();
	if (v.kind !== "all") sp.set("kind", v.kind);
	for (const p of v.projects) sp.append("project", p);
	for (const o of v.origins) sp.append("origin", o);
	if (v.sort !== DEFAULT_VIEW.sort) sp.set("sort", v.sort);
	if (v.layout !== DEFAULT_VIEW.layout) sp.set("layout", v.layout);
	if (v.q) sp.set("q", v.q);
	return sp.toString();
}

/** 有任何筛选（类型、项目、来源、搜索）；排序与布局不算。 */
export function hasFilters(v: ArtifactsView): boolean {
	return (
		v.kind !== "all" ||
		v.projects.length > 0 ||
		v.origins.length > 0 ||
		v.q.trim() !== ""
	);
}

/** 去掉全部筛选，保留排序与布局。 */
export function clearFilters(v: ArtifactsView): ArtifactsView {
	return { ...v, kind: "all", projects: [], origins: [], q: "" };
}

/** 标题匹配：查询按空白切词，每个词都要出现在折叠后（大小写、变音符）的标题里。 */
export function titleMatches(title: string, q: string): boolean {
	const terms = fold(q).text.split(/\s+/).filter(Boolean);
	if (terms.length === 0) return true;
	const hay = fold(title).text;
	return terms.every((term) => hay.includes(term));
}

export function filterArtifacts(
	list: readonly Artifact[],
	v: ArtifactsView,
): Artifact[] {
	return list.filter(
		(a) =>
			(v.kind === "all" || a.kind === v.kind) &&
			(v.projects.length === 0 ||
				v.projects.includes(a.projectId ?? NO_PROJECT)) &&
			(v.origins.length === 0 || v.origins.includes(a.origin)) &&
			titleMatches(a.title, v.q),
	);
}

/** 「最近」排序的时间：打开过与更新过里较晚的一个。 */
export function recentAt(a: Pick<Artifact, "openedAt" | "updatedAt">): number {
	return Math.max(a.openedAt ?? 0, a.updatedAt);
}

const collator = new Intl.Collator(undefined, {
	numeric: true,
	sensitivity: "base",
});

/** 新数组：`recent` / `updated` 新的在前，`name` 按标题自然序；并列时按 id 稳定。 */
export function sortArtifacts(
	list: readonly Artifact[],
	sort: SortKey,
): Artifact[] {
	const byId = (x: Artifact, y: Artifact) => (x.id < y.id ? -1 : 1);
	return list.toSorted((x, y) => {
		const d =
			sort === "name"
				? collator.compare(x.title, y.title)
				: sort === "updated"
					? y.updatedAt - x.updatedAt
					: recentAt(y) - recentAt(x);
		return d || byId(x, y);
	});
}

/** 项目里的一段：某个线程的产出、导入的、或创建线程已删除的。 */
export interface ThreadSection {
	key: string;
	type: "thread" | "imported" | "deletedThread";
	threadId?: string;
	items: Artifact[];
}

/** 一个项目（`projectId` 为 `null` = 无项目）及其下各段。 */
export interface ProjectSection {
	key: string;
	projectId: string | null;
	sections: ThreadSection[];
}

/**
 * 「按项目与会话」布局：先按项目、项目内按线程分段。导入的归「导入的」，创建线程已删除的（`origin = agent`
 * 但没有 `threadId`）归「已删除的线程」。分组与段的顺序沿用输入（已排好序）里首次出现的顺序。
 */
export function groupArtifacts(sorted: readonly Artifact[]): ProjectSection[] {
	const projects = new Map<
		string,
		{ projectId: string | null; sections: Map<string, ThreadSection> }
	>();
	for (const a of sorted) {
		const pKey = a.projectId ?? NO_PROJECT;
		let p = projects.get(pKey);
		if (!p) {
			p = { projectId: a.projectId ?? null, sections: new Map() };
			projects.set(pKey, p);
		}
		const sKey =
			a.origin === "import"
				? "imported"
				: a.threadId
					? `t:${a.threadId}`
					: "deletedThread";
		let s = p.sections.get(sKey);
		if (!s) {
			s =
				sKey === "imported" || sKey === "deletedThread"
					? { key: sKey, type: sKey, items: [] }
					: { key: sKey, type: "thread", threadId: a.threadId, items: [] };
			p.sections.set(sKey, s);
		}
		s.items.push(a);
	}
	return [...projects].map(([key, p]) => ({
		key,
		projectId: p.projectId,
		sections: [...p.sections.values()],
	}));
}

/** 分组后按阅读顺序摊平（键盘导航与焦点恢复用）。 */
export function flattenGroups(groups: readonly ProjectSection[]): Artifact[] {
	return groups.flatMap((g) => g.sections.flatMap((s) => s.items));
}

/**
 * 卡片网格里按一个键移动焦点：左右 ±1、上下 ±一行（`columns` 张），`Home` / `End` 首尾；越界停在边上。
 * 不处理的键回 `null`。
 */
export function gridMove(
	index: number,
	key: string,
	count: number,
	columns: number,
): number | null {
	if (count === 0) return null;
	const cols = Math.max(1, columns);
	const clamp = (n: number) => Math.min(count - 1, Math.max(0, n));
	switch (key) {
		case "ArrowRight":
			return clamp(index + 1);
		case "ArrowLeft":
			return clamp(index - 1);
		case "ArrowDown":
			return clamp(index + cols);
		case "ArrowUp":
			return clamp(index - cols);
		case "Home":
			return 0;
		case "End":
			return count - 1;
		default:
			return null;
	}
}

/** 导入时的一个文件：表单字段名（相对路径）与内容。 */
export interface ImportEntry {
	path: string;
	file: File;
}

/**
 * 选中的文件 → 导入条目。单个 HTML 文件用它的文件名；文件夹（`webkitRelativePath` 形如 `site/a/b.html`）
 * 去掉顶层文件夹名。`name` 是给服务端的回落标题：HTML 文件名去掉扩展名，或顶层文件夹名。
 */
export function importEntries(files: readonly File[]): {
	entries: ImportEntry[];
	name: string;
} {
	const entries: ImportEntry[] = [];
	let folder = "";
	for (const file of files) {
		const rel = file.webkitRelativePath;
		if (rel?.includes("/")) {
			const cut = rel.indexOf("/");
			folder ||= rel.slice(0, cut);
			entries.push({ path: rel.slice(cut + 1), file });
		} else entries.push({ path: file.name, file });
	}
	const first = files[0];
	const name = folder || (first ? first.name.replace(/\.html?$/i, "") : "");
	return { entries, name };
}

/** 导入失败的错误码（服务端 `import.go`）；其余错误按通用文案显示。 */
export const IMPORT_ERROR_CODES = [
	"too_large",
	"too_many",
	"no_index",
	"bad_canvas",
	"bad_name",
	"not_utf8",
] as const;
export type ImportErrorCode = (typeof IMPORT_ERROR_CODES)[number];

export function isImportErrorCode(code: unknown): code is ImportErrorCode {
	return (
		typeof code === "string" &&
		(IMPORT_ERROR_CODES as readonly string[]).includes(code)
	);
}
