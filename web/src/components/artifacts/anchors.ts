/**
 * 评论锚点（计划 M14 3.3）：由页面运行时报来的选中构造锚点、锚点的显示文字、评论串的编号与页面图钉。
 * 编号规则：未解决的串按创建先后从 1 编号（与页面上的图钉一致），已解决的不编号。
 * 类型化 artifact（M31）的锚点由查看器的点选构造（`anchorOfViewer`），图钉经 `anchor-pins` 交给查看器画。
 */
import type { TFunction } from "i18next";
import {
	type AnchorPin,
	MAX_SELECT_PINS,
	type PageSelection,
	type SelectPin,
	type ViewerAnchor,
} from "../../../sandbox/runtime/messages";
import type {
	ArtifactCommentAnchor,
	ArtifactCommentThread,
} from "../../api/artifact-queries";
import { typedAnchorKind } from "../../lib/artifacts/typed-anchor";

/** 锚点所在的上下文：帧正在显示的版本、页面路径，以及画布上的画框（阅读顺序里的序号与名字）。 */
export interface AnchorShown {
	version: number;
	/** 页面在版本里的路径；选中里带了路径时以选中的为准。 */
	path: string;
	page?: { number: number; name: string } | null;
}

/**
 * 页面选中 → 评论锚点。`selection` 为空（点了 `<html>` / `<body>` / 空白处）时是「整个页面」：`offset`、`tag` 都为空。
 * 页面脚本生成的元素 `src` 为空，锚点也不带 `offset`（之后不会重新定位）。设计画布的元素带源码位置 `loc`
 * （`src/frames/x.tsx:行:列`），锚点随之带上，agent 据此直接定位源码。
 */
export function anchorOf(
	selection: PageSelection | null,
	shown: AnchorShown,
): ArtifactCommentAnchor {
	const page = shown.page ?? null;
	if (!selection)
		return {
			version: shown.version,
			path: shown.path,
			page,
			offset: null,
			tag: null,
			text: "",
			missing: false,
		};
	return {
		version: shown.version,
		path: selection.path || shown.path,
		page,
		offset: selection.src,
		tag: selection.tag,
		text: selection.text,
		missing: false,
		...(selection.loc ? { loc: selection.loc } : {}),
	};
}

/** 查看器点选 → 评论锚点；`anchor` 为空时是整篇 / 整页（各字段都空）。 */
export function anchorOfViewer(
	anchor: ViewerAnchor | null,
	shown: AnchorShown,
): ArtifactCommentAnchor {
	return {
		version: shown.version,
		path: shown.path,
		page: anchor?.page ?? null,
		offset: null,
		tag: null,
		text: anchor?.text ?? "",
		missing: false,
		x: anchor?.x ?? null,
		y: anchor?.y ?? null,
		range: anchor?.range ?? null,
		block: anchor?.block ?? null,
	};
}

/** 类型化锚点的显示文字：「第 n 页」「第 n 张」「{工作表} 的 {区域}」「工作表 {名}」「第 n 段」；HTML 锚点为 `null`。 */
function typedLabel(
	anchor: ArtifactCommentAnchor,
	t: TFunction,
): string | null {
	const kind = typedAnchorKind(anchor.path);
	if (kind === null) return null;
	const page = anchor.page;
	switch (kind) {
		case "block":
			return anchor.block === null || anchor.block === undefined
				? t("artifacts.comments.wholePage")
				: t("artifacts.comments.blockAt", { n: anchor.block + 1 });
		case "sheet": {
			if (!page) return t("artifacts.comments.wholePage");
			const sheet = page.name || String(page.number);
			return anchor.range
				? t("artifacts.comments.sheetRange", { sheet, range: anchor.range })
				: t("artifacts.comments.sheetOnly", { sheet });
		}
		case "slide":
			return page
				? t("artifacts.comments.slideAt", { n: page.number })
				: t("artifacts.comments.wholePage");
		case "page":
			return page
				? t("artifacts.comments.pageAt", { n: page.number })
				: t("artifacts.comments.wholePage");
	}
}

/** 源码位置的显示形式：去掉列号，`src/frames/x.tsx:42:5` → `src/frames/x.tsx:42`。 */
export function locLabel(loc: string): string {
	return loc.replace(/:\d+$/, "");
}

/** 锚点的显示文字：「{页名} › <标签>“文本”」或「整个页面」；类型化锚点见 `typedLabel`。元素已不在由调用方另外标出。 */
export function anchorLabel(
	anchor: ArtifactCommentAnchor,
	t: TFunction,
): string {
	const typed = typedLabel(anchor, t);
	if (typed !== null) return typed;
	const target =
		anchor.tag === null
			? t("artifacts.comments.wholePage")
			: anchor.text
				? t("artifacts.comments.element", {
						tag: anchor.tag,
						text: anchor.text,
					})
				: t("artifacts.comments.elementTag", { tag: anchor.tag });
	return anchor.page
		? t("artifacts.comments.onPage", { page: anchor.page.name, target })
		: target;
}

/** 有 agent（线程）回复过的串。 */
export function isAnswered(thread: ArtifactCommentThread): boolean {
	return thread.comments.some((c) => !!c.authorThreadId);
}

export interface NumberedThread {
	thread: ArtifactCommentThread;
	/** 未解决的串的编号（从 1 起）。 */
	n: number;
}

/** 未解决的在前并按创建先后编号；已解决的保持创建先后、不编号。 */
export function orderThreads(threads: readonly ArtifactCommentThread[]): {
	open: NumberedThread[];
	resolved: ArtifactCommentThread[];
} {
	const sorted = threads.toSorted((a, b) => a.createdAt - b.createdAt);
	const open = sorted
		.filter((x) => x.resolvedAt === undefined)
		.map((thread, i) => ({ thread, n: i + 1 }));
	return {
		open,
		resolved: sorted.filter((x) => x.resolvedAt !== undefined),
	};
}

/**
 * 某个页面（`path`）上要画的图钉：未解决、锚在元素上（有 `offset`）且元素还在的串，编号同 `orderThreads`。
 * 最多 `MAX_SELECT_PINS` 个（运行时整条丢弃超限的消息）。
 */
export function pinsOf(
	threads: readonly ArtifactCommentThread[],
	path: string,
): SelectPin[] {
	const pins: SelectPin[] = [];
	for (const { thread, n } of orderThreads(threads).open) {
		const a = thread.anchor;
		if (a.path !== path || a.offset === null || a.missing) continue;
		pins.push({ n, src: a.offset, answered: isAnswered(thread) });
		if (pins.length === MAX_SELECT_PINS) break;
	}
	return pins;
}

/** 评论锚点 → 查看器锚点（`anchor-pins`、`anchor-locate` 用）。 */
export function viewerAnchorOf(a: ArtifactCommentAnchor): ViewerAnchor {
	return {
		page: a.page,
		x: a.x ?? null,
		y: a.y ?? null,
		range: a.range ?? null,
		block: a.block ?? null,
		text: a.text,
	};
}

/**
 * 查看器（类型化 artifact 的入口 `path`）上要画的图钉：未解决、锚点在这个入口上且没有标 missing 的串，
 * 编号同 `orderThreads`。最多 `MAX_SELECT_PINS` 个。
 */
export function anchorPinsOf(
	threads: readonly ArtifactCommentThread[],
	path: string,
): AnchorPin[] {
	const pins: AnchorPin[] = [];
	if (typedAnchorKind(path) === null) return pins;
	for (const { thread, n } of orderThreads(threads).open) {
		const a = thread.anchor;
		if (a.path !== path || a.missing || a.offset !== null || a.tag !== null)
			continue;
		pins.push({ n, answered: isAnswered(thread), anchor: viewerAnchorOf(a) });
		if (pins.length === MAX_SELECT_PINS) break;
	}
	return pins;
}
