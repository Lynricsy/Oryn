/**
 * JS 驱动动效的总开关。CSS 动画由 index.css 的 `prefers-reduced-motion` 规则统一压低；逐帧滚动、高度过渡、
 * 逐字显现这类 JS 动效在这里判定：用户要求减少动效、或环境没有 `matchMedia`（jsdom、老 WebView）时一律直接到位。
 */
export function motionAllowed(): boolean {
	return (
		typeof window !== "undefined" &&
		typeof window.matchMedia === "function" &&
		!window.matchMedia("(prefers-reduced-motion: reduce)").matches
	);
}
