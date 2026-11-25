/**
 * 设计画布的实时预览（M29 T6）：`GET artifacts/{id}/design` 的 `live` 非空（orb 里该画布的 dev 服务在跑）、跟随最新版本、
 * 线程有 portal 地址模板，且用户没有切到「静态」时，离视口最近的至多 `MAX_LIVE` 个带 `source` 的画框换成实时帧。
 *
 * - 「实时 / 静态」是本机偏好（localStorage `oryn.design.preview`），对所有设计画布生效。
 * - 实时帧 `LIVE_READY_MS` 内没 `ready`（WebKit 下 portal 握手被带到主站登录页，探针 P3）：本画布退回静态帧，
 *   提示「在新窗口授权实时预览」；`authorize` 顶层打开一次 portal 根地址（拿到 portal 主机的 Cookie），窗口回到焦点后重建实时帧。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { useArtifactDesign } from "../../../api/artifact-queries";
import type { Box, CanvasPage } from "../../../lib/artifacts/canvas-doc";
import {
	getThreadStore,
	type ThreadStore,
	useIndex,
} from "../../../state/stores";
import { portalFrameSrc } from "../../portal/portal-url";
import { nearestPages, type PageContent } from "./frames";
import {
	frameIdOf,
	LIVE_READY_MS,
	liveFrameTarget,
	MAX_LIVE,
} from "./LiveFrame";

/** 「实时 / 静态」偏好的 localStorage 键。 */
export const PREVIEW_PREF_KEY = "oryn.design.preview";

export type PreviewMode = "live" | "static";

function readPref(): PreviewMode {
	try {
		return globalThis.localStorage?.getItem(PREVIEW_PREF_KEY) === "static"
			? "static"
			: "live";
	} catch {
		return "live";
	}
}

function writePref(mode: PreviewMode) {
	try {
		if (mode === "live") globalThis.localStorage?.removeItem(PREVIEW_PREF_KEY);
		else globalThis.localStorage?.setItem(PREVIEW_PREF_KEY, mode);
	} catch {
		// 隐私模式下 localStorage 不可用：只在本次会话生效
	}
}

// 还不知道 live 在哪个线程时读的空线程状态（不为空 id 建线程 store）
const NO_THREAD = createStore<Pick<ThreadStore, "thread">>(() => ({}));

/** 线程的 portal 地址模板：取法同 Portal 面板（线程 store），线程 store 还没有快照时退回索引里的线程。 */
function usePortalTemplate(threadId: string | null): string | undefined {
	const fromThread = useStore(
		threadId ? getThreadStore(threadId) : NO_THREAD,
		(s) => s.thread?.portalUrlTemplate,
	);
	const fromIndex = useIndex((s) =>
		threadId ? s.threads[threadId]?.portalUrlTemplate : undefined,
	);
	return fromThread ?? fromIndex;
}

export interface DesignPreview {
	/** 是设计画布（canvas.json 带顶层 design）。 */
	design: boolean;
	/** 实时预览可用（dev 服务在跑、跟随最新版本、有 portal 模板）。 */
	available: boolean;
	/** 实际显示的模式：不可用或握手失败时是 static。 */
	mode: PreviewMode;
	/** 用户的偏好。 */
	pref: PreviewMode;
	setPref(mode: PreviewMode): void;
	/** 开着实时帧的画框 id。 */
	liveIds: ReadonlySet<string>;
	/** 画框的实时内容；不开实时帧的画框为 null。 */
	liveContent(page: CanvasPage): PageContent | null;
	/** 有实时帧在 `LIVE_READY_MS` 内没 ready，已退回静态帧，等用户授权。 */
	blocked: boolean;
	onTimeout(): void;
	/** 新窗口打开 portal 根地址授权；窗口回到焦点后重建实时帧。 */
	authorize(): void;
}

export function useDesignPreview({
	artifactId,
	design,
	pinned,
	pages,
	view,
}: {
	artifactId: string;
	/** 文档带顶层 design。 */
	design: boolean;
	/** 看的是旧版本。 */
	pinned: boolean;
	/** 当前画布页上的画框。 */
	pages: readonly CanvasPage[];
	/** 视口（世界坐标）。 */
	view: Box;
}): DesignPreview {
	const state = useArtifactDesign(artifactId, design);
	const live = design && !pinned ? (state.data?.live ?? null) : null;
	const template = usePortalTemplate(live?.threadId ?? null);
	const [pref, setPrefState] = useState(readPref);
	const [blocked, setBlocked] = useState(false);
	const [nonce, setNonce] = useState(0);
	// 换了 dev 服务（端口或线程）就重新尝试
	const target = live && template ? `${template}|${live.port}` : "";
	const seenTarget = useRef(target);
	if (seenTarget.current !== target) {
		seenTarget.current = target;
		if (blocked) setBlocked(false);
	}
	const available = !!live && !!template;
	const mode: PreviewMode =
		available && pref === "live" && !blocked ? "live" : "static";

	const sourced = useMemo(
		() => pages.filter((p) => frameIdOf(p.source) !== null),
		[pages],
	);
	const liveIds = useMemo(
		() =>
			mode === "live"
				? nearestPages(sourced, view, MAX_LIVE)
				: new Set<string>(),
		[mode, sourced, view],
	);

	const setPref = useCallback((next: PreviewMode) => {
		writePref(next);
		setPrefState(next);
	}, []);

	const onTimeout = useCallback(() => setBlocked(true), []);

	// 授权窗口打开后等主窗口重新拿到焦点；卸载时撤掉还没触发的监听
	const pendingFocus = useRef<(() => void) | null>(null);
	useEffect(
		() => () => {
			if (pendingFocus.current)
				window.removeEventListener("focus", pendingFocus.current);
		},
		[],
	);
	const authorize = useCallback(() => {
		if (!live || !template) return;
		window.open(portalFrameSrc(template, live.port, "/"), "_blank", "noopener");
		if (pendingFocus.current)
			window.removeEventListener("focus", pendingFocus.current);
		const onFocus = () => {
			pendingFocus.current = null;
			setBlocked(false);
			setNonce((n) => n + 1);
		};
		pendingFocus.current = onFocus;
		window.addEventListener("focus", onFocus, { once: true });
	}, [live, template]);

	const liveContent = (page: CanvasPage): PageContent | null => {
		const frame = frameIdOf(page.source);
		if (!live || !template || !frame || !liveIds.has(page.id)) return null;
		return {
			kind: "live",
			...liveFrameTarget(template, live.port, frame),
			nonce,
		};
	};

	return {
		design,
		available,
		mode,
		pref,
		setPref,
		liveIds,
		liveContent,
		blocked,
		onTimeout,
		authorize,
	};
}

export { LIVE_READY_MS };
