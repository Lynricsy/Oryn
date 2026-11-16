// 查看器页的配置与环境：服务端在页面里放 `<script type="application/json" id="oryn-viewer">`（见
// internal/artifacts/site.go 的 viewerConfig），`?view=thumbnail|print` 留在页面地址里。
// 查看器包运行在不可信的沙箱里：不 import `src/` 下依赖应用状态的模块，只用 `src/lib/artifacts/` 的纯函数与类型。

export interface ViewerConfig {
	type: "document" | "slides";
	format: number;
	/** 页面显示的版本。 */
	version: number;
	/** 申请帧那一刻的最新版本。 */
	head: number;
	/** 入口文件（相对路径，按页面地址解析，所以读到的是页面所在的版本）。 */
	entry: string;
	/** 界面语言；缺省按浏览器语言。 */
	locale?: string;
	/** 帧根（以 `/` 结尾）：`<root>_oryn/v/<n>/` 是版本 n 的根。 */
	root: string;
}

/** 渲染器用的视图：缩略图（只有画面）、打印（幻灯片逐页）；`null` 是普通查看。 */
export type ViewMode = "thumbnail" | "print" | null;

export type ViewerLocale = "zh-CN" | "en";

/** 读页面里的配置；缺失或形状不对时返回 `null`。 */
export function readViewerConfig(doc: Document): ViewerConfig | null {
	const el = doc.getElementById("oryn-viewer");
	if (!el?.textContent) return null;
	let raw: unknown;
	try {
		raw = JSON.parse(el.textContent);
	} catch {
		return null;
	}
	if (typeof raw !== "object" || raw === null) return null;
	const c = raw as Record<string, unknown>;
	if (
		(c.type !== "document" && c.type !== "slides") ||
		typeof c.format !== "number" ||
		typeof c.version !== "number" ||
		typeof c.head !== "number" ||
		typeof c.entry !== "string" ||
		typeof c.root !== "string"
	)
		return null;
	return {
		type: c.type,
		format: c.format,
		version: c.version,
		head: c.head,
		entry: c.entry,
		locale: typeof c.locale === "string" ? c.locale : undefined,
		root: c.root,
	};
}

export function viewModeOf(search: string): ViewMode {
	const view = new URLSearchParams(search).get("view");
	return view === "thumbnail" || view === "print" ? view : null;
}

/** 配置给的语言优先，其次浏览器语言；中文一律 zh-CN，其余 en。 */
export function pickLocale(locale: string | undefined): ViewerLocale {
	const l =
		locale || (typeof navigator === "undefined" ? "" : navigator.language);
	return l.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
}

/** 版本 n 的根地址：换到别的版本后，相对路径（images/…）按它解析。 */
export function versionBase(
	root: string,
	version: number,
	page: string,
): string {
	return new URL(`${root}_oryn/v/${version}/`, page).href;
}

/**
 * 把内容里的相对地址按 `base` 解析成绝对地址；绝对地址与 `data:` 原样返回。
 * 解析失败时返回原值（交给浏览器与 CSP 处理）。
 */
export function resolveAsset(src: string, base: string): string {
	try {
		return new URL(src, base).href;
	} catch {
		return src;
	}
}
