/** CSS 变量（oklch 等任意格式）→ `#rrggbb`：画到 1×1 画布上读回像素。供 xterm 主题、mermaid 主题、`<meta name="theme-color">` 使用。 */
export function cssColor(name: string, fallback: string): string {
	if (typeof document === "undefined") return fallback;
	const raw = getComputedStyle(document.documentElement)
		.getPropertyValue(name)
		.trim();
	if (!raw) return fallback;
	const canvas = document.createElement("canvas");
	canvas.width = 1;
	canvas.height = 1;
	let ctx: CanvasRenderingContext2D | null = null;
	try {
		ctx = canvas.getContext("2d", { willReadFrequently: true });
	} catch {
		// jsdom 未实现画布
	}
	if (!ctx) return fallback;
	ctx.fillStyle = fallback;
	ctx.fillStyle = raw;
	ctx.fillRect(0, 0, 1, 1);
	const [r = 0, g = 0, b = 0] = ctx.getImageData(0, 0, 1, 1).data;
	return `#${[r, g, b].map((v) => v.toString(16).padStart(2, "0")).join("")}`;
}
