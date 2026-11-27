// 查看器 bundle 读帧运行时的评论桥（`window.__orynViewer`，见 sandbox/runtime/viewer.ts）：父页面的评论模式、
// 要画的图钉、要定位的锚点，以及把点选报回父页面。没有运行时（顶层帧、发布页、渲染模式）时全部为空。

import { useEffect, useMemo, useState } from "react";
import type {
	AnchorPin,
	SelectMode,
	ViewerAnchor,
} from "../../runtime/messages";
import type { ViewerBridge } from "../../runtime/viewer";

export interface ViewerComments {
	/** 父页面的模式；`"comment"` 时查看器响应点选。 */
	mode: SelectMode;
	/** 要画的图钉（未解决的评论串）。 */
	pins: readonly AnchorPin[];
	/** 最近一次要定位到的锚点；每次请求都是新对象，同一锚点再点也会重新定位。 */
	locate: ViewerAnchor | null;
	/** 报一次点选（`anchor` 为空表示整篇）；没有桥时为空。 */
	pick:
		| ((anchor: ViewerAnchor | null, at: { x: number; y: number }) => void)
		| null;
}

const NO_PINS: readonly AnchorPin[] = Object.freeze([]);

function bridgeOf(win: Window): ViewerBridge | null {
	const b = (win as Window & { __orynViewer?: unknown }).__orynViewer;
	return typeof b === "object" &&
		b !== null &&
		typeof (b as ViewerBridge).onMode === "function"
		? (b as ViewerBridge)
		: null;
}

export function useViewerBridge(): ViewerComments {
	const [bridge] = useState(() => bridgeOf(window));
	const [mode, setMode] = useState<SelectMode>(null);
	const [pins, setPins] = useState<readonly AnchorPin[]>(NO_PINS);
	const [locate, setLocate] = useState<ViewerAnchor | null>(null);

	useEffect(() => {
		if (!bridge) return;
		// 注册时桥立即重放最近一次的模式与图钉
		const offs = [
			bridge.onMode(setMode),
			bridge.onPins(setPins),
			bridge.onLocate(setLocate),
		];
		return () => {
			for (const off of offs) off();
		};
	}, [bridge]);

	const pick = useMemo(
		() =>
			bridge
				? (anchor: ViewerAnchor | null, at: { x: number; y: number }) =>
						bridge.pick(anchor, at)
				: null,
		[bridge],
	);

	return useMemo(
		() => ({ mode, pins, locate, pick }),
		[mode, pins, locate, pick],
	);
}
