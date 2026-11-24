/**
 * 分区模型（M9.6）：线程 / 助手 / 项目 / 更多。当前分区完全由路由决定（`sectionOf`），不另存状态；
 * 底栏按钮只是「导航到该分区记住的路径」。分区记忆存 sessionStorage（每个标签页 / 窗口各一份，刷新保留）。
 * 纯函数，不依赖 React 与 store；`kindOf` 由调用方注入（一律取索引里线程的 `kind`）。
 */
import { MORE_ENTRIES } from "../components/layout/more-entries";

export type SectionId = "threads" | "assistant" | "projects" | "more";

/** 底栏顺序 = 侧栏轨道顺序 = Ctrl/⌘+1…4 */
export const SECTION_ORDER: readonly SectionId[] = [
	"threads",
	"assistant",
	"projects",
	"more",
];

/** sessionStorage 键：`Partial<Record<SectionId, string>>` 的 JSON。 */
export const SECTION_PATHS_KEY = "oryn.sections.paths";

export type ThreadKindOf = (
	threadId: string,
) => "work" | "space" | "assistant" | undefined;

/** 去掉 search 与 hash，只留 pathname。 */
function pathOnly(path: string): string {
	const cut = path.search(/[?#]/);
	return cut < 0 ? path : path.slice(0, cut);
}

/** 按整段的前缀匹配：`/projects` 匹配 `/projects` 与 `/projects/x`，不匹配 `/projectsx`。 */
function under(path: string, base: string): boolean {
	return path === base || path.startsWith(`${base}/`);
}

/** 以单个 "/" 开头（排除 `//host` 这类协议相对路径）。 */
function isLocalPath(path: unknown): path is string {
	return (
		typeof path === "string" && path.startsWith("/") && !path.startsWith("//")
	);
}

/** `/assistant…` → assistant；`/projects…` → projects；`/settings…`、`/stats…`、`/artifacts…`、`/boxes…`、`/providers…` → more；其余（`/`、`/t/…`、`/space/…`、`/inbox`）→ threads。 */
export function sectionOf(pathname: string): SectionId {
	const p = pathOnly(pathname);
	if (under(p, "/assistant")) return "assistant";
	if (under(p, "/projects")) return "projects";
	if (
		under(p, "/settings") ||
		under(p, "/stats") ||
		under(p, "/artifacts") ||
		under(p, "/boxes") ||
		under(p, "/providers")
	)
		return "more";
	return "threads";
}

/** threads → "/"；assistant → "/assistant"；projects → "/projects"；more → MORE_ENTRIES[0].to */
export function defaultSectionPath(section: SectionId): string {
	switch (section) {
		case "threads":
			return "/";
		case "assistant":
			return "/assistant";
		case "projects":
			return "/projects";
		case "more":
			return MORE_ENTRIES[0]?.to ?? "/settings";
	}
}

/** `/t/<id>` 且该线程是协调对话（会被线程页重定向到别处）→ false；其余 true。 */
export function settles(path: string, kindOf: ThreadKindOf): boolean {
	const m = /^\/t\/([^/]+)\/?$/.exec(pathOnly(path));
	if (!m?.[1]) return true;
	const kind = kindOf(m[1]);
	return kind !== "assistant" && kind !== "space";
}

/** 记住的路径仍合法（以单个 "/" 开头、属于该分区、settles）就用它，否则用缺省路径。 */
export function sectionTarget(
	section: SectionId,
	remembered: string | undefined,
	kindOf: ThreadKindOf,
): string {
	if (
		isLocalPath(remembered) &&
		sectionOf(remembered) === section &&
		settles(remembered, kindOf)
	)
		return remembered;
	return defaultSectionPath(section);
}

/** 读 sessionStorage；逐项校验（字符串、以单个 "/" 开头、sectionOf 与键一致），坏项丢弃。 */
export function loadSectionPaths(): Partial<Record<SectionId, string>> {
	let raw: unknown;
	try {
		const text = globalThis.sessionStorage?.getItem(SECTION_PATHS_KEY);
		if (!text) return {};
		raw = JSON.parse(text);
	} catch {
		return {};
	}
	if (raw === null || typeof raw !== "object" || Array.isArray(raw)) return {};
	const out: Partial<Record<SectionId, string>> = {};
	for (const id of SECTION_ORDER) {
		const v = (raw as Record<string, unknown>)[id];
		if (isLocalPath(v) && sectionOf(v) === id) out[id] = v;
	}
	return out;
}
