/**
 * noVNC 鼠标捕获的兜底释放。
 *
 * noVNC 1.7 在画布 mousedown 时调用 `setCapture`：浏览器没有原生 setCapture 时，它在 body 上放一个盖满整页的代理层
 * （`#noVNC_mouse_capture_elem`，z-index 10000），等代理层收到 mouseup 再 `releaseCapture` 收起。
 * 全屏时代理层在全屏顶层之下收不到事件，mouseup 直接落到画布上，又被 noVNC 拦下不冒泡，捕获就一直不放；
 * 退出全屏后代理层盖着整页，吞掉用户的下一次点击。
 *
 * `releaseCapture` 在 `core/util/events.js` 里，而包的 exports 只开放 `core/rfb.js`，导入不到。
 * 这里给代理层补发一次 mouseup：noVNC 自己把它转给画布（远端收到一次松开），并调用同一模块实例的 `releaseCapture`。
 */
const PROXY_ID = "noVNC_mouse_capture_elem";

/** 捕获仍开着就释放；`init` 是这次松开的坐标与按键状态。返回是否释放了。 */
export function releaseStaleCapture(init: MouseEventInit): boolean {
	const doc = document as Document & { captureElement?: Element | null };
	const proxy = document.getElementById(PROXY_ID);
	if (!doc.captureElement || !proxy || proxy.style.display === "none")
		return false;
	proxy.dispatchEvent(
		new MouseEvent("mouseup", { ...init, bubbles: true, cancelable: true }),
	);
	return true;
}
