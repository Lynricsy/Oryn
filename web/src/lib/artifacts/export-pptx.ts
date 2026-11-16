/**
 * 幻灯片 artifact 导出成 PowerPoint（计划 M14 5.4）。
 *
 * 两步：`pptxPlan(deck)` 按八种版式算出每张幻灯片的摆放清单（位置以英寸计，页面 10 英寸宽，高 5.625（16:9）或
 * 7.5（4:3），四周留 0.6 英寸，字体 Arial、正文 18pt，颜色只取 `DECK_PALETTES`），可以直接断言；`buildPptx` 再把清单
 * 交给 `pptxgenjs`。讲者备注写进 PowerPoint 的备注。`pptxgenjs` 很大，只在导出时动态加载。
 */

import {
	assetPath,
	type RasterImage,
	toBase64,
	toRaster,
} from "./office-images";
import {
	type Bullet,
	type Column,
	DECK_PALETTES,
	type Deck,
	type DeckPalette,
	parseDeck,
	type Slide,
	type SlideLayout,
} from "./slides";

export interface PptxBox {
	x: number;
	y: number;
	w: number;
	h: number;
}

export interface PptxParagraph {
	text: string;
	/** 项目符号的层级（0 或 1）；缺省 = 不是要点。 */
	bullet?: number;
	bold?: boolean;
	/** 覆盖文本框的字号（pt）与颜色。 */
	size?: number;
	color?: string;
}

export type PptxRole =
	| "title"
	| "subtitle"
	| "body"
	| "caption"
	| "quote"
	| "attribution"
	| "value"
	| "label";

export type PptxItem =
	| { kind: "rect"; box: PptxBox; fill: string }
	| {
			kind: "text";
			role: PptxRole;
			box: PptxBox;
			paragraphs: PptxParagraph[];
			size: number;
			color: string;
			bold?: boolean;
			italic?: boolean;
			align: "left" | "center" | "right";
			valign: "top" | "middle" | "bottom";
	  }
	/** 图片在框里等比缩放、居中。 */
	| { kind: "image"; box: PptxBox; path: string; alt: string }
	| {
			kind: "table";
			box: PptxBox;
			columns: string[];
			rows: string[][];
			colW: number[];
			size: number;
	  };

export interface PptxSlidePlan {
	layout: SlideLayout;
	items: PptxItem[];
	notes?: string;
}

export interface PptxPlan {
	/** 页面尺寸（英寸）。 */
	width: number;
	height: number;
	/** 颜色都是不带 `#` 的六位十六进制（pptxgenjs 的格式）。 */
	colors: DeckPalette;
	slides: PptxSlidePlan[];
}

export const PPTX_FONT = "Arial";
export const PPTX_BODY_PT = 18;
const WIDTH = 10;
const MARGIN = 0.6;
/** 有标题的版式里内容区从这里开始。 */
const TITLE_H = 0.8;
const CONTENT_GAP = 0.25;

function hexColors(p: DeckPalette): DeckPalette {
	return Object.fromEntries(
		Object.entries(p).map(([k, v]) => [k, v.slice(1)]),
	) as unknown as DeckPalette;
}

function bulletParas(items: readonly Bullet[]): PptxParagraph[] {
	return items.flatMap((b) =>
		typeof b === "string"
			? [{ text: b, bullet: 0 }]
			: [
					{ text: b.text, bullet: 0 },
					...(b.bullets ?? []).map((t) => ({ text: t, bullet: 1 })),
				],
	);
}

/** 一张幻灯片的摆放：页面尺寸、调色板与这一张的内容。 */
class Layout {
	readonly items: PptxItem[] = [];
	readonly W: number;
	readonly H: number;
	readonly c: DeckPalette;
	constructor(W: number, H: number, c: DeckPalette) {
		this.W = W;
		this.H = H;
		this.c = c;
	}

	text(
		role: PptxRole,
		box: PptxBox,
		paragraphs: PptxParagraph[],
		style: Partial<
			Pick<
				Extract<PptxItem, { kind: "text" }>,
				"size" | "color" | "bold" | "italic" | "align" | "valign"
			>
		> = {},
	): void {
		this.items.push({
			kind: "text",
			role,
			box,
			paragraphs,
			size: PPTX_BODY_PT,
			color: this.c.text,
			align: "left",
			valign: "top",
			...style,
		});
	}

	/** 版式顶部的标题；返回内容区的上沿。没有标题时内容从上边距开始。 */
	title(title: string | undefined): number {
		if (title === undefined) return MARGIN;
		this.text(
			"title",
			{ x: MARGIN, y: MARGIN, w: this.W - 2 * MARGIN, h: TITLE_H },
			[{ text: title }],
			{ size: 28, bold: true, valign: "middle" },
		);
		return MARGIN + TITLE_H + CONTENT_GAP;
	}

	column(box: PptxBox, col: Column): void {
		this.text("body", box, [
			...(col.heading !== undefined
				? [{ text: col.heading, bold: true, size: 22, color: this.c.accent }]
				: []),
			...(col.text !== undefined ? [{ text: col.text }] : []),
			...bulletParas(col.bullets ?? []),
		]);
	}
}

function placeSlide(L: Layout, s: Slide): void {
	const { W, H, c } = L;
	const inner = W - 2 * MARGIN;
	switch (s.layout) {
		case "title": {
			L.items.push({
				kind: "rect",
				box: { x: MARGIN, y: H / 2 - 0.95, w: 1, h: 0.08 },
				fill: c.accent,
			});
			L.text(
				"title",
				{ x: MARGIN, y: H / 2 - 0.8, w: inner, h: 1.2 },
				[{ text: s.title }],
				{ size: 40, bold: true, valign: "bottom" },
			);
			if (s.subtitle !== undefined)
				L.text(
					"subtitle",
					{ x: MARGIN, y: H / 2 + 0.5, w: inner, h: 0.8 },
					[{ text: s.subtitle }],
					{ size: 22, color: c.muted },
				);
			return;
		}
		case "section": {
			L.items.push({
				kind: "rect",
				box: { x: MARGIN, y: H / 2 - 0.6, w: 0.08, h: 1.4 },
				fill: c.accent,
			});
			L.text(
				"title",
				{ x: MARGIN + 0.3, y: H / 2 - 0.6, w: inner - 0.3, h: 0.8 },
				[{ text: s.title }],
				{ size: 36, bold: true, valign: "bottom" },
			);
			if (s.subtitle !== undefined)
				L.text(
					"subtitle",
					{ x: MARGIN + 0.3, y: H / 2 + 0.25, w: inner - 0.3, h: 0.6 },
					[{ text: s.subtitle }],
					{ size: 20, color: c.muted },
				);
			return;
		}
		case "bullets": {
			const top = L.title(s.title);
			const h = H - MARGIN - top;
			if (s.image === undefined) {
				L.text(
					"body",
					{ x: MARGIN, y: top, w: inner, h },
					bulletParas(s.bullets),
				);
				return;
			}
			// 带图时要点在左半边，图在右半边
			const half = inner / 2 - 0.2;
			L.text("body", { x: MARGIN, y: top, w: half, h }, bulletParas(s.bullets));
			L.items.push({
				kind: "image",
				box: { x: W / 2 + 0.2, y: top, w: W - MARGIN - (W / 2 + 0.2), h },
				path: s.image,
				alt: s.alt ?? "",
			});
			return;
		}
		case "two-column": {
			const top = L.title(s.title);
			const gap = 0.4;
			const w = (inner - gap) / 2;
			const h = H - MARGIN - top;
			L.column({ x: MARGIN, y: top, w, h }, s.left);
			L.column({ x: MARGIN + w + gap, y: top, w, h }, s.right);
			return;
		}
		case "image": {
			const top = L.title(s.title);
			const captionH = s.caption !== undefined ? 0.5 : 0;
			L.items.push({
				kind: "image",
				box: {
					x: MARGIN,
					y: top,
					w: inner,
					h: H - MARGIN - top - (captionH ? captionH + 0.1 : 0),
				},
				path: s.image,
				alt: s.alt,
			});
			if (s.caption !== undefined)
				L.text(
					"caption",
					{ x: MARGIN, y: H - MARGIN - captionH, w: inner, h: captionH },
					[{ text: s.caption }],
					{ size: 14, color: c.muted, align: "center", valign: "middle" },
				);
			return;
		}
		case "quote": {
			const by = s.attribution !== undefined ? 0.8 : 0;
			L.text(
				"quote",
				{ x: MARGIN + 0.4, y: MARGIN, w: inner - 0.8, h: H - 2 * MARGIN - by },
				[{ text: `“${s.quote}”` }],
				{ size: 30, italic: true, align: "center", valign: "middle" },
			);
			if (s.attribution !== undefined)
				L.text(
					"attribution",
					{ x: MARGIN + 0.4, y: H - MARGIN - 0.7, w: inner - 0.8, h: 0.6 },
					[{ text: `— ${s.attribution}` }],
					{ color: c.muted, align: "right", valign: "middle" },
				);
			return;
		}
		case "stats": {
			const top = L.title(s.title);
			const n = s.stats.length;
			const gap = 0.3;
			const w = (inner - gap * (n - 1)) / n;
			const avail = H - MARGIN - top;
			const h = Math.min(2.2, avail);
			const y = top + (avail - h) / 2;
			s.stats.forEach((st, i) => {
				const x = MARGIN + i * (w + gap);
				L.items.push({ kind: "rect", box: { x, y, w, h }, fill: c.surface });
				L.text(
					"value",
					{ x, y: y + 0.15, w, h: h * 0.5 },
					[{ text: st.value }],
					{
						size: 40,
						bold: true,
						color: c.accent,
						align: "center",
						valign: "bottom",
					},
				);
				if (st.label !== undefined)
					L.text(
						"label",
						{ x, y: y + h * 0.5 + 0.25, w, h: h * 0.5 - 0.35 },
						[{ text: st.label }],
						{ size: 16, color: c.muted, align: "center" },
					);
			});
			return;
		}
		case "table": {
			const top = L.title(s.title);
			const h = H - MARGIN - top;
			const n = s.columns.length;
			// 行多时缩小字号，免得表格溢出页面（最多 20 行；行高约为字号的 1.7 倍）
			const size = Math.max(
				10,
				Math.min(
					PPTX_BODY_PT,
					Math.floor(((h / (s.rows.length + 1)) * 72) / 1.7),
				),
			);
			L.items.push({
				kind: "table",
				box: { x: MARGIN, y: top, w: inner, h },
				columns: s.columns,
				rows: s.rows,
				colW: Array.from({ length: n }, () => inner / n),
				size,
			});
			return;
		}
	}
}

/** 按版式算出每张幻灯片的摆放清单。 */
export function pptxPlan(deck: Deck): PptxPlan {
	const height = deck.aspect === "4:3" ? 7.5 : 5.625;
	const colors = hexColors(DECK_PALETTES[deck.theme ?? "light"]);
	return {
		width: WIDTH,
		height,
		colors,
		slides: deck.slides.map((s) => {
			const L = new Layout(WIDTH, height, colors);
			placeSlide(L, s);
			return {
				layout: s.layout,
				items: L.items,
				...(s.notes !== undefined ? { notes: s.notes } : {}),
			};
		}),
	};
}

/** 计划里出现的全部图片路径（去重，按出现顺序）。 */
export function pptxImagePaths(plan: PptxPlan): string[] {
	const out = new Set<string>();
	for (const s of plan.slides)
		for (const i of s.items) if (i.kind === "image") out.add(i.path);
	return [...out];
}

/** 把 `w×h` 的图等比缩进框里并居中。 */
export function fitContain(box: PptxBox, w: number, h: number): PptxBox {
	const k = Math.min(box.w / w, box.h / h);
	const fw = w * k;
	const fh = h * k;
	return {
		x: box.x + (box.w - fw) / 2,
		y: box.y + (box.h - fh) / 2,
		w: fw,
		h: fh,
	};
}

const IMAGE_MIME = { png: "image/png", jpg: "image/jpeg", gif: "image/gif" };

/** 用 pptxgenjs 生成 .pptx。`images` 里没有的图片画成带说明文字的占位框。 */
export async function buildPptx(
	plan: PptxPlan,
	opts: { title: string; images: ReadonlyMap<string, RasterImage> },
): Promise<Blob> {
	// 动态加载：pptxgenjs 很大，只在用户真的导出 PowerPoint 时才下载这一块
	const { default: PptxGenJS } = await import("pptxgenjs");
	const pptx = new PptxGenJS();
	pptx.defineLayout({ name: "ORYN", width: plan.width, height: plan.height });
	pptx.layout = "ORYN";
	pptx.title = opts.title;
	const c = plan.colors;
	for (const s of plan.slides) {
		const slide = pptx.addSlide();
		slide.background = { color: c.background };
		for (const it of s.items) {
			switch (it.kind) {
				case "rect":
					slide.addShape("rect", {
						...it.box,
						fill: { color: it.fill },
						line: { color: it.fill, width: 0 },
					});
					break;
				case "text":
					slide.addText(
						it.paragraphs.map((p) => ({
							text: p.text,
							options: {
								breakLine: true,
								...(p.bullet !== undefined
									? { bullet: { indent: 18 }, indentLevel: p.bullet }
									: {}),
								...(p.bold ? { bold: true } : {}),
								...(p.size ? { fontSize: p.size } : {}),
								...(p.color ? { color: p.color } : {}),
							},
						})),
						{
							...it.box,
							fontFace: PPTX_FONT,
							fontSize: it.size,
							color: it.color,
							bold: it.bold ?? false,
							italic: it.italic ?? false,
							align: it.align,
							valign: it.valign,
							margin: 0,
							paraSpaceAfter: 6,
						},
					);
					break;
				case "image": {
					const img = opts.images.get(it.path);
					if (img) {
						slide.addImage({
							data: `${IMAGE_MIME[img.type]};base64,${toBase64(img.data)}`,
							...fitContain(it.box, img.width, img.height),
							altText: it.alt,
						});
					} else {
						slide.addShape("rect", {
							...it.box,
							fill: { color: c.surface },
							line: { color: c.line, width: 1 },
						});
						slide.addText(it.alt || it.path, {
							...it.box,
							fontFace: PPTX_FONT,
							fontSize: 14,
							color: c.muted,
							align: "center",
							valign: "middle",
						});
					}
					break;
				}
				case "table":
					slide.addTable(
						[
							it.columns.map((text) => ({
								text,
								options: { bold: true, fill: { color: c.surface } },
							})),
							...it.rows.map((r) => r.map((text) => ({ text }))),
						],
						{
							x: it.box.x,
							y: it.box.y,
							w: it.box.w,
							colW: it.colW,
							fontFace: PPTX_FONT,
							fontSize: it.size,
							color: c.text,
							valign: "middle",
							border: { type: "solid", pt: 0.75, color: c.line },
						},
					);
					break;
			}
		}
		if (s.notes !== undefined) slide.addNotes(s.notes);
	}
	return (await pptx.write({ outputType: "blob" })) as Blob;
}

/**
 * 导出一份幻灯片：解析 `slides.json`、经 `readFile` 取图（非 PNG / JPEG / GIF 在浏览器里转成 PNG）、生成 .pptx。
 * `missing` 是取不到或转不了的图片路径（画成占位框）。`slides.json` 解析失败时抛错。
 */
export async function exportPptx(input: {
	title: string;
	deckText: string;
	readFile(path: string): Promise<Uint8Array>;
}): Promise<{ blob: Blob; missing: string[] }> {
	const parsed = parseDeck(input.deckText);
	if (!parsed.ok)
		throw new Error(
			parsed.problems.map((p) => p.where || "slides.json").join(", "),
		);
	const plan = pptxPlan(parsed.deck);
	const images = new Map<string, RasterImage>();
	const missing: string[] = [];
	await Promise.all(
		pptxImagePaths(plan).map(async (path) => {
			const file = assetPath(path);
			const img = await input.readFile(file).then(
				(b) => toRaster(b, file),
				() => null,
			);
			if (img) images.set(path, img);
			else missing.push(path);
		}),
	);
	const blob = await buildPptx(plan, {
		title: parsed.deck.title ?? input.title,
		images,
	});
	return { blob, missing };
}
