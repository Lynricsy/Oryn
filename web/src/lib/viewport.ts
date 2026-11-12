/**
 * 自适应布局：三档断点（compact <600、medium 600–839、expanded ≥840）与软键盘处理。
 * 软键盘：用 `visualViewport` 计算被键盘遮挡的高度写入 CSS 变量 `--kb-inset`，输入区据此上移。
 */
import { useSyncExternalStore } from "react";

export type Breakpoint = "compact" | "medium" | "expanded";

export function breakpointFor(width: number): Breakpoint {
	if (width < 600) return "compact";
	if (width < 840) return "medium";
	return "expanded";
}

function subscribeResize(cb: () => void) {
	window.addEventListener("resize", cb);
	return () => window.removeEventListener("resize", cb);
}

export function useBreakpoint(): Breakpoint {
	return useSyncExternalStore(
		subscribeResize,
		() => breakpointFor(window.innerWidth),
		() => "expanded",
	);
}

/** 安装一次：跟踪软键盘高度（`--kb-inset`）与可视高度（`--vvh`）。 */
export function installViewportTracking(): () => void {
	const vv = window.visualViewport;
	const root = document.documentElement;
	const update = () => {
		const height = vv?.height ?? window.innerHeight;
		const offset = vv?.offsetTop ?? 0;
		const inset = Math.max(0, window.innerHeight - height - offset);
		root.style.setProperty("--kb-inset", `${Math.round(inset)}px`);
		root.style.setProperty("--vvh", `${Math.round(height)}px`);
	};
	update();
	vv?.addEventListener("resize", update);
	vv?.addEventListener("scroll", update);
	window.addEventListener("resize", update);
	return () => {
		vv?.removeEventListener("resize", update);
		vv?.removeEventListener("scroll", update);
		window.removeEventListener("resize", update);
	};
}
