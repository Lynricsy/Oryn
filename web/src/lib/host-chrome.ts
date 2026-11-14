/**
 * 系统栏配色（M7 计划 4.1）：移动壳把状态栏 / 导航栏底色与图标明暗跟随页面。
 * 取 `<body>` 的计算背景色转 `#rrggbb`，`dark` = 相对亮度 < 0.5；监听 `<html>` 的 class / style / data-* 与系统明暗变化，值变了才回调。
 */

/** `rgb()` / `rgba()` / `#rgb` / `#rrggbb` → `#rrggbb`；其它格式（oklch 等）经 1×1 画布换算，换算不了为 null。 */
export function toHexColor(
	css: string,
	doc: Document = document,
): string | null {
	const s = css.trim().toLowerCase();
	const rgb = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/.exec(s);
	if (rgb)
		return `#${rgb
			.slice(1, 4)
			.map((v) =>
				Math.max(0, Math.min(255, Math.round(Number(v))))
					.toString(16)
					.padStart(2, "0"),
			)
			.join("")}`;
	const hex = /^#([0-9a-f]{3}|[0-9a-f]{6})$/.exec(s);
	if (hex) {
		const h = hex[1] ?? "";
		return h.length === 3 ? `#${[...h].map((c) => c + c).join("")}` : `#${h}`;
	}
	let ctx: CanvasRenderingContext2D | null = null;
	try {
		const canvas = doc.createElement("canvas");
		canvas.width = 1;
		canvas.height = 1;
		ctx = canvas.getContext("2d", { willReadFrequently: true });
	} catch {
		// jsdom 未实现画布
	}
	if (!ctx) return null;
	ctx.fillStyle = "#000";
	ctx.fillStyle = s;
	ctx.fillRect(0, 0, 1, 1);
	const [r = 0, g = 0, b = 0] = ctx.getImageData(0, 0, 1, 1).data;
	return `#${[r, g, b].map((v) => v.toString(16).padStart(2, "0")).join("")}`;
}

/** WCAG 相对亮度（0–1）。 */
export function relativeLuminance(hex: string): number {
	const n = Number.parseInt(hex.slice(1), 16);
	const lin = (c: number) => {
		const v = c / 255;
		return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
	};
	return (
		0.2126 * lin((n >> 16) & 255) +
		0.7152 * lin((n >> 8) & 255) +
		0.0722 * lin(n & 255)
	);
}

export interface ChromeColors {
	dark: boolean;
	color: string;
}

/** 订阅页面底色；先同步回调一次当前值（读不到颜色时不回调）。返回取消函数。 */
export function watchChrome(
	win: Window,
	cb: (c: ChromeColors) => void,
): () => void {
	const doc = win.document;
	let last = "";
	const check = () => {
		if (!doc.body) return;
		const color = toHexColor(
			win.getComputedStyle(doc.body).backgroundColor,
			doc,
		);
		if (!color) return;
		const dark = relativeLuminance(color) < 0.5;
		const key = `${color}|${dark}`;
		if (key === last) return;
		last = key;
		cb({ dark, color });
	};
	check();
	const mo = new MutationObserver(check);
	// 主题切换写 `<html>` 的 class / data-* / style；只看属性，批量回调已按微任务合并
	mo.observe(doc.documentElement, { attributes: true });
	const mq = win.matchMedia?.("(prefers-color-scheme: dark)");
	mq?.addEventListener("change", check);
	return () => {
		mo.disconnect();
		mq?.removeEventListener("change", check);
	};
}
