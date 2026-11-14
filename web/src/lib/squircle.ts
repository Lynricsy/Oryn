/**
 * 角形降级的入口（随主包加载，只有特性检测）：引擎原生支持 `corner-shape: superellipse(1.6)` 时什么都不做；
 * 否则动态加载 squircle-fallback.ts（独立分块，原生路径不下载）。机制见 squircle-fallback.ts 与
 * docs/design/web-guidelines.md「超椭圆角形」。
 */

/** 当前引擎是否需要降级：不认 `corner-shape: superellipse(1.6)`（Firefox、WebKitGTK、旧版 Safari / WKWebView）。 */
export function needsSquircleFallback(
	css: Pick<typeof CSS, "supports"> | undefined = globalThis.CSS,
): boolean {
	return !css?.supports("corner-shape", "superellipse(1.6)");
}

/**
 * 在首屏渲染前调用。降级引擎加载失败时只打日志：页面照常渲染，角形退回普通圆角。
 */
export async function installSquircle(doc: Document = document): Promise<void> {
	if (!needsSquircleFallback()) return;
	try {
		// 动态导入是有意的：引擎只在不支持 corner-shape 的浏览器里下载，原生路径的包体积不变
		const { installSquircleFallback } = await import("./squircle-fallback");
		installSquircleFallback(doc);
	} catch (err) {
		console.error("角形降级加载失败，退回普通圆角", err);
	}
}
