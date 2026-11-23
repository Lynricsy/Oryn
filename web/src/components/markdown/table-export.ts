/**
 * Markdown 表格的三种导出：CSV、Markdown 源码、PNG 图片。
 * - 单元格文字从 DOM 取：`<br>` 记作换行，KaTeX 公式取其 TeX 注释（`$…$`），不取渲染出的字形。
 * - Markdown 优先切原文（hast 节点的 position 偏移），保留行内格式；位于列表 / 引用里时去掉后续行的缩进与 `> ` 前缀。
 *   拿不到位置或原文是内嵌 HTML（文档模式的 `<table>`）时，由单元格文字重新拼一张 GFM 表。
 * - 图片不经 SVG foreignObject（WebKit 会污染画布，Tauri 的 macOS / iOS 壳就是 WebKit），而是按 DOM 几何逐项画到画布：
 *   单元格底色与发丝线、行内代码的底块、文字（按文本节点的行框分段）。字体取自页面已加载的字体，与屏幕所见一致。
 */

/** hast 节点上的源码位置（只用到偏移与列号）。 */
export interface SourcePosition {
	start: { column: number; offset?: number };
	end: { offset?: number };
}

function cellText(el: Element): string {
	let out = "";
	const walk = (n: Node) => {
		if (n.nodeType === Node.TEXT_NODE) {
			out += n.nodeValue ?? "";
			return;
		}
		if (!(n instanceof Element)) return;
		if (n.tagName === "BR") {
			out += "\n";
			return;
		}
		if (n.classList.contains("katex")) {
			const tex = n.querySelector('annotation[encoding="application/x-tex"]');
			out += tex ? `$${tex.textContent ?? ""}$` : (n.textContent ?? "");
			return;
		}
		for (const c of n.childNodes) walk(c);
	};
	walk(el);
	return out.trim();
}

/** 表格的单元格文字，按行。 */
export function tableRows(table: HTMLTableElement): string[][] {
	return Array.from(table.rows, (row) => Array.from(row.cells, cellText));
}

export function rowsToCsv(rows: string[][]): string {
	return rows
		.map((r) =>
			r
				.map((s) => (/[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s))
				.join(","),
		)
		.join("\n");
}

export function rowsToMarkdown(rows: string[][]): string {
	const [head, ...body] = rows;
	if (!head) return "";
	const width = Math.max(...rows.map((r) => r.length));
	const line = (r: string[]) =>
		`| ${Array.from({ length: width }, (_, i) =>
			(r[i] ?? "")
				.replace(/\\/g, "\\\\")
				.replace(/\|/g, "\\|")
				.replace(/\n/g, "<br>"),
		).join(" | ")} |`;
	return [
		line(head),
		`| ${Array.from({ length: width }, () => "---").join(" | ")} |`,
		...body.map(line),
	].join("\n");
}

/** 从原文切出表格的 Markdown；拿不到位置或原文是内嵌 HTML 时为 null。 */
export function sliceMarkdown(
	source: string,
	pos: SourcePosition | undefined,
): string | null {
	const from = pos?.start.offset;
	const to = pos?.end.offset;
	if (from === undefined || to === undefined || to <= from) return null;
	const raw = source.slice(from, to);
	if (raw.trimStart().startsWith("<")) return null;
	// 列表 / 引用里的表格：后续行带着容器前缀（缩进、`> `），宽度与首行起点的列号相同
	const indent = pos?.start.column ? pos.start.column - 1 : 0;
	return raw
		.split("\n")
		.map((l, i) =>
			i > 0 && indent > 0 && /^[\s>]*$/.test(l.slice(0, indent))
				? l.slice(indent)
				: l,
		)
		.join("\n");
}

/* ---------------- 图片 ---------------- */

function visible(color: string): boolean {
	return (
		color !== "" &&
		color !== "transparent" &&
		!/^rgba\(.*,\s*0\)$/.test(color) &&
		!/\/\s*0\)$/.test(color)
	);
}

function px(v: string): number {
	return Number.parseFloat(v) || 0;
}

/** 单元格与表格结构元素：底色与边线按单元格画，不当行内装饰。 */
const STRUCTURE: Record<string, true> = {
	TABLE: true,
	THEAD: true,
	TBODY: true,
	TFOOT: true,
	TR: true,
	TH: true,
	TD: true,
};

interface Run {
	text: string;
	x: number;
	top: number;
	height: number;
	right: number;
}

/** 文本节点按行框切成若干段：整段只占一行时直接用，跨行时逐字素量位置再按行合并。 */
function textRuns(node: Text, segmenter: Intl.Segmenter | null): Run[] {
	const text = node.nodeValue ?? "";
	const range = document.createRange();
	range.selectNodeContents(node);
	const lines = Array.from(range.getClientRects()).filter((r) => r.width > 0);
	if (lines.length === 0) return [];
	if (lines.length === 1) {
		const r = lines[0] as DOMRect;
		return [
			{
				text: text.replace(/\s+/g, " "),
				x: r.left,
				top: r.top,
				height: r.height,
				right: r.right,
			},
		];
	}
	const runs: Run[] = [];
	let cur: Run | null = null;
	const segments = segmenter
		? Array.from(segmenter.segment(text), (s) => ({
				seg: s.segment,
				at: s.index,
			}))
		: Array.from(text, (seg, at) => ({ seg, at }));
	for (const { seg, at } of segments) {
		range.setStart(node, at);
		range.setEnd(node, at + seg.length);
		const r = Array.from(range.getClientRects()).find((x) => x.width > 0);
		if (!r) continue;
		if (cur && Math.abs(r.top - cur.top) < 1 && r.left >= cur.right - 1) {
			cur.text += seg;
			cur.right = r.right;
		} else {
			if (cur) runs.push(cur);
			cur = {
				text: seg,
				x: r.left,
				top: r.top,
				height: r.height,
				right: r.right,
			};
		}
	}
	if (cur) runs.push(cur);
	for (const run of runs) run.text = run.text.replace(/\s+/g, " ").trimEnd();
	return runs;
}

/** 把表格框（带圆角描边的滚动容器）连同完整表格画成 PNG；横向滚动藏起来的部分也画上。 */
export async function tableToPng(
	frame: HTMLElement,
	table: HTMLTableElement,
): Promise<Blob> {
	await document.fonts?.ready;
	const box = table.getBoundingClientRect();
	const fs = getComputedStyle(frame);
	const bw = px(fs.borderTopWidth);
	const width = Math.ceil(box.width + 2 * bw);
	const height = Math.ceil(box.height + 2 * bw);
	const scale = Math.max(2, Math.min(window.devicePixelRatio || 1, 3));
	const canvas = document.createElement("canvas");
	canvas.width = Math.round(width * scale);
	canvas.height = Math.round(height * scale);
	const ctx = canvas.getContext("2d");
	if (!ctx) throw new Error("canvas 2d unavailable");
	// 之后一律用视口坐标作画
	ctx.scale(scale, scale);
	ctx.translate(bw - box.left, bw - box.top);
	const outer = {
		x: box.left - bw,
		y: box.top - bw,
		w: width,
		h: height,
		r: px(fs.borderTopLeftRadius),
	};

	ctx.beginPath();
	ctx.roundRect(outer.x, outer.y, outer.w, outer.h, outer.r);
	ctx.fillStyle = fs.backgroundColor;
	ctx.fill();
	ctx.save();
	ctx.clip();

	// 单元格：底色 + 上下发丝线
	for (const cell of table.querySelectorAll<HTMLElement>("th, td")) {
		const cs = getComputedStyle(cell);
		const r = cell.getBoundingClientRect();
		if (visible(cs.backgroundColor)) {
			ctx.fillStyle = cs.backgroundColor;
			ctx.fillRect(r.left, r.top, r.width, r.height);
		}
		const top = px(cs.borderTopWidth);
		if (top > 0 && cs.borderTopStyle !== "none") {
			ctx.fillStyle = cs.borderTopColor;
			ctx.fillRect(r.left, r.top, r.width, top);
		}
		const bottom = px(cs.borderBottomWidth);
		if (bottom > 0 && cs.borderBottomStyle !== "none") {
			ctx.fillStyle = cs.borderBottomColor;
			ctx.fillRect(r.left, r.bottom - bottom, r.width, bottom);
		}
	}

	// 行内装饰（行内代码等）：底块与描边，逐个行框画
	for (const el of table.querySelectorAll<HTMLElement>("*")) {
		if (STRUCTURE[el.tagName] || el.closest(".katex-mathml")) continue;
		const cs = getComputedStyle(el);
		const fill = visible(cs.backgroundColor);
		const line = px(cs.borderTopWidth);
		const stroke = line > 0 && cs.borderTopStyle !== "none";
		if (!fill && !stroke) continue;
		const radius = px(cs.borderTopLeftRadius);
		for (const r of el.getClientRects()) {
			ctx.beginPath();
			ctx.roundRect(
				r.left + line / 2,
				r.top + line / 2,
				r.width - line,
				r.height - line,
				radius,
			);
			if (fill) {
				ctx.fillStyle = cs.backgroundColor;
				ctx.fill();
			}
			if (stroke) {
				ctx.lineWidth = line;
				ctx.strokeStyle = cs.borderTopColor;
				ctx.stroke();
			}
		}
	}

	// 文字
	const segmenter =
		typeof Intl.Segmenter === "function"
			? new Intl.Segmenter(undefined, { granularity: "grapheme" })
			: null;
	const walker = document.createTreeWalker(table, NodeFilter.SHOW_TEXT);
	for (let n = walker.nextNode(); n; n = walker.nextNode()) {
		const node = n as Text;
		const parent = node.parentElement;
		if (!parent || !node.nodeValue?.trim() || parent.closest(".katex-mathml"))
			continue;
		const cs = getComputedStyle(parent);
		ctx.font = `${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
		ctx.fillStyle = cs.color;
		ctx.textBaseline = "alphabetic";
		const underline = cs.textDecorationLine.includes("underline");
		for (const run of textRuns(node, segmenter)) {
			if (!run.text) continue;
			const m = ctx.measureText(run.text);
			const ascent = m.fontBoundingBoxAscent ?? px(cs.fontSize) * 0.8;
			const descent = m.fontBoundingBoxDescent ?? px(cs.fontSize) * 0.2;
			const baseline = run.top + (run.height - ascent - descent) / 2 + ascent;
			ctx.fillText(run.text, run.x, baseline);
			if (underline) {
				ctx.fillStyle = cs.textDecorationColor;
				ctx.fillRect(run.x, baseline + 2, m.width, 1);
				ctx.fillStyle = cs.color;
			}
		}
	}

	ctx.restore();
	// 外框描边（压在内容之上，与屏幕上一样）
	if (bw > 0) {
		ctx.beginPath();
		ctx.roundRect(
			outer.x + bw / 2,
			outer.y + bw / 2,
			outer.w - bw,
			outer.h - bw,
			Math.max(0, outer.r - bw / 2),
		);
		ctx.lineWidth = bw;
		ctx.strokeStyle = fs.borderTopColor;
		ctx.stroke();
	}

	return new Promise((resolve, reject) =>
		canvas.toBlob(
			(png) => (png ? resolve(png) : reject(new Error("toBlob failed"))),
			"image/png",
		),
	);
}
