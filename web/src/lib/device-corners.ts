/**
 * 屏幕圆角同心（M7）：移动壳报告物理屏幕四角半径 `cornerRadii` 与原生容器加在 WebView 外的 `insets`
 * （系统栏 + 刘海 + 软键盘）。WebView 自己的角离屏幕边缘有 inset 那么远，所以视口角上的同心半径
 * = max(0, 屏幕半径 − max(相邻两边 inset))，写到根元素的 `--device-r-tl/tr/bl/br`。
 * 贴边的浮层（抽屉、侧栏、底部弹层、输入坞）用 index.css 的 `.dc-*` 取
 * max(自身令牌圆角, 视口角半径 − 与边缘的间距)；浏览器与桌面壳里变量恒为 0，视觉不变。
 */
import {
	type CornerRadii,
	type DeviceGeometry,
	type Host,
	parseGeometry,
} from "./host";

export const DEVICE_CORNER_VARS = {
	tl: "--device-r-tl",
	tr: "--device-r-tr",
	bl: "--device-r-bl",
	br: "--device-r-br",
} as const;

/** 视口四角的同心半径：屏幕半径减去该角相邻两边 inset 的较大者，不小于 0。 */
export function viewportCornerRadii(g: DeviceGeometry): CornerRadii {
	const { cornerRadii: r, insets: i } = g;
	return {
		tl: Math.max(0, r.tl - Math.max(i.top, i.left)),
		tr: Math.max(0, r.tr - Math.max(i.top, i.right)),
		bl: Math.max(0, r.bl - Math.max(i.bottom, i.left)),
		br: Math.max(0, r.br - Math.max(i.bottom, i.right)),
	};
}

/** 贴边元素的角半径（同心公式，与 `.dc-*` 的 CSS 一致）：max(令牌, 视口角半径 − 间距)。 */
export function concentricRadius(
	token: number,
	viewportRadius: number,
	gap: number,
): number {
	return Math.max(token, viewportRadius - gap);
}

/** 把视口角半径写到根元素的 CSS 变量（整数 px）。 */
export function applyDeviceCorners(root: HTMLElement, g: DeviceGeometry): void {
	const r = viewportCornerRadii(g);
	for (const k of Object.keys(DEVICE_CORNER_VARS) as (keyof CornerRadii)[])
		root.style.setProperty(DEVICE_CORNER_VARS[k], `${Math.round(r[k])}px`);
}

/** 移动壳下安装：先取 `MobileInfo` 里的几何，之后跟随 `insets` 事件。其它宿主不做任何事。 */
export function installDeviceCorners(h: Host, root: HTMLElement): () => void {
	const mobile = h.mobile;
	if (!mobile) return () => {};
	let live = true;
	let gotEvent = false;
	const off = h.onInsets((g) => {
		gotEvent = true;
		applyDeviceCorners(root, g);
	});
	void mobile.info().then(
		(info) => {
			// 事件比初值新：已收到事件就不再用初值覆盖
			if (!live || gotEvent) return;
			const g = parseGeometry(info);
			if (g) applyDeviceCorners(root, g);
		},
		() => undefined,
	);
	return () => {
		live = false;
		off();
	};
}
