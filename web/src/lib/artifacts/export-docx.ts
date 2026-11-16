/**
 * 文档 artifact 导出成 Word（计划 M14 5.4）。
 *
 * 两步：`docxPlan(markdown)` 用与查看器相同的解析器（unified + remark-parse + remark-gfm）把 Markdown 转成一份可断言的
 * 块列表；`buildDocx` 再把块逐个映射成 `docx` 库的段落、表格与图片。这是结构到结构的映射，不经过 HTML。
 * 原生 HTML、脚注不导出（查看器也不显示）。`docx` 库很大，只在导出时动态加载。
 */

import type { IParagraphOptions, Paragraph, ParagraphChild, Table } from "docx";
import type {
	TableRow as MdTableRow,
	Nodes,
	PhrasingContent,
	Root,
	RootContent,
} from "mdast";
import remarkGfm from "remark-gfm";
import remarkParse from "remark-parse";
import { unified } from "unified";
import {
	assetPath,
	type RasterImage,
	rasterInfo,
	toRaster,
} from "./office-images";

/** 一段同样式的文字。 */
export interface DocxSpan {
	text: string;
	bold?: true;
	italic?: true;
	strike?: true;
	code?: true;
}

export type DocxInline =
	| ({ kind: "text" } & DocxSpan)
	| { kind: "link"; url: string; children: DocxSpan[] }
	| { kind: "image"; src: string; alt: string }
	| { kind: "break" };

export type DocxAlign = "left" | "center" | "right" | null;

interface BlockBase {
	/** 引用的层数（0 或缺省 = 不在引用里）。 */
	quote?: number;
	/** 列表项里的后续块相对正文的缩进层数。 */
	indent?: number;
}

export type DocxBlock = BlockBase &
	(
		| { kind: "heading"; level: 1 | 2 | 3 | 4 | 5 | 6; inlines: DocxInline[] }
		| { kind: "paragraph"; inlines: DocxInline[] }
		| {
				kind: "list-item";
				ordered: boolean;
				/** 嵌套层数，0 是最外层。 */
				level: number;
				/** 所属列表的序号：每个列表（含嵌套的）各自编号。 */
				list: number;
				/** 任务列表的勾选状态；普通列表项为 `null`。 */
				checked: boolean | null;
				inlines: DocxInline[];
		  }
		| {
				kind: "table";
				align: DocxAlign[];
				/** 第一行是表头；每行的单元格数都补齐到表头的列数。 */
				rows: DocxInline[][][];
		  }
		| { kind: "code"; lang: string | null; text: string }
		| { kind: "rule" }
	);

const parser = unified().use(remarkParse).use(remarkGfm);

interface Definition {
	url: string;
}

interface Walk {
	out: DocxBlock[];
	defs: Map<string, Definition>;
	lists: number;
}

interface Place {
	quote: number;
	indent: number;
	level: number;
}

function placed<T extends DocxBlock>(b: T, at: Place): T {
	if (at.quote) b.quote = at.quote;
	if (at.indent) b.indent = at.indent;
	return b;
}

/** 链接与图片引用按定义解析（标签不分大小写、折叠空白，同 CommonMark）。 */
function defKey(label: string): string {
	return label.trim().replace(/\s+/g, " ").toLowerCase();
}

function collectDefs(node: Nodes, defs: Map<string, Definition>): void {
	if (node.type === "definition") {
		const key = defKey(node.identifier);
		if (!defs.has(key)) defs.set(key, { url: node.url });
		return;
	}
	if ("children" in node)
		for (const c of node.children as Nodes[]) collectDefs(c, defs);
}

type Marks = Omit<DocxSpan, "text">;

function spans(nodes: PhrasingContent[], marks: Marks, w: Walk): DocxSpan[] {
	const out: DocxSpan[] = [];
	for (const inline of inlines(nodes, marks, w)) {
		if (inline.kind === "text") {
			const { kind: _, ...span } = inline;
			out.push(span);
		} else if (inline.kind === "link") out.push(...inline.children);
		else if (inline.kind === "image" && inline.alt)
			out.push({ text: inline.alt, ...marks });
	}
	return out;
}

function inlines(
	nodes: PhrasingContent[],
	marks: Marks,
	w: Walk,
): DocxInline[] {
	const out: DocxInline[] = [];
	for (const n of nodes) {
		switch (n.type) {
			case "text":
				// 软换行在段落里显示为空白
				if (n.value)
					out.push({
						kind: "text",
						text: n.value.replace(/\s*\n\s*/g, " "),
						...marks,
					});
				break;
			case "inlineCode":
				out.push({ kind: "text", text: n.value, ...marks, code: true });
				break;
			case "emphasis":
				out.push(...inlines(n.children, { ...marks, italic: true }, w));
				break;
			case "strong":
				out.push(...inlines(n.children, { ...marks, bold: true }, w));
				break;
			case "delete":
				out.push(...inlines(n.children, { ...marks, strike: true }, w));
				break;
			case "break":
				out.push({ kind: "break" });
				break;
			case "link":
				out.push({
					kind: "link",
					url: n.url,
					children: spans(n.children, marks, w),
				});
				break;
			case "linkReference": {
				const def = w.defs.get(defKey(n.identifier));
				const children = spans(n.children, marks, w);
				if (def) out.push({ kind: "link", url: def.url, children });
				else for (const s of children) out.push({ kind: "text", ...s });
				break;
			}
			case "image":
				out.push({ kind: "image", src: n.url, alt: n.alt ?? "" });
				break;
			case "imageReference": {
				const def = w.defs.get(defKey(n.identifier));
				if (def) out.push({ kind: "image", src: def.url, alt: n.alt ?? "" });
				break;
			}
			// 原生 HTML 与脚注引用不导出
		}
	}
	return out;
}

function tableRows(rows: MdTableRow[], w: Walk): DocxInline[][][] {
	const cols = rows[0]?.children.length ?? 0;
	return rows.map((r) => {
		const cells = r.children
			.slice(0, cols)
			.map((c) => inlines(c.children, {}, w));
		while (cells.length < cols) cells.push([]);
		return cells;
	});
}

function blocks(nodes: RootContent[], at: Place, w: Walk): void {
	for (const n of nodes) {
		switch (n.type) {
			case "heading":
				w.out.push(
					placed(
						{
							kind: "heading",
							level: n.depth,
							inlines: inlines(n.children, {}, w),
						},
						at,
					),
				);
				break;
			case "paragraph":
				w.out.push(
					placed(
						{ kind: "paragraph", inlines: inlines(n.children, {}, w) },
						at,
					),
				);
				break;
			case "blockquote":
				blocks(n.children, { ...at, quote: at.quote + 1 }, w);
				break;
			case "list": {
				const list = w.lists++;
				for (const item of n.children) {
					const [first, ...rest] = item.children;
					const lead = first?.type === "paragraph" ? first : undefined;
					w.out.push(
						placed(
							{
								kind: "list-item",
								ordered: n.ordered === true,
								level: at.level,
								list,
								checked: item.checked ?? null,
								inlines: lead ? inlines(lead.children, {}, w) : [],
							},
							{ ...at, indent: 0 },
						),
					);
					blocks(
						lead ? rest : item.children,
						{ ...at, indent: at.level + 1, level: at.level + 1 },
						w,
					);
				}
				break;
			}
			case "code":
				w.out.push(
					placed({ kind: "code", lang: n.lang ?? null, text: n.value }, at),
				);
				break;
			case "table":
				w.out.push(
					placed(
						{
							kind: "table",
							align: (n.align ?? []).map((a) => a ?? null),
							rows: tableRows(n.children, w),
						},
						at,
					),
				);
				break;
			case "thematicBreak":
				w.out.push(placed({ kind: "rule" }, at));
				break;
			// html、definition、footnoteDefinition 不导出
		}
	}
}

/** 把 Markdown 转成 Word 的块列表（可断言的中间结构）。 */
export function docxPlan(markdown: string): DocxBlock[] {
	const root: Root = parser.parse(markdown);
	const w: Walk = { out: [], defs: new Map(), lists: 0 };
	collectDefs(root, w.defs);
	blocks(root.children, { quote: 0, indent: 0, level: 0 }, w);
	return w.out;
}

/** 计划里出现的全部图片地址（去重，按出现顺序）。 */
export function docxImageSources(plan: DocxBlock[]): string[] {
	const seen = new Set<string>();
	const visit = (list: DocxInline[]) => {
		for (const i of list) if (i.kind === "image") seen.add(i.src);
	};
	for (const b of plan) {
		if (b.kind === "table") for (const r of b.rows) for (const c of r) visit(c);
		else if ("inlines" in b) visit(b.inlines);
	}
	return [...seen];
}

/** 解 `data:image/…;base64,…`；不是 base64 的 data: 地址回 `null`。 */
function dataUrlBytes(src: string): Uint8Array | null {
	const m = /^data:[^;,]*;base64,(.*)$/is.exec(src);
	if (!m?.[1]) return null;
	try {
		return Uint8Array.from(atob(m[1].replace(/\s+/g, "")), (c) =>
			c.charCodeAt(0),
		);
	} catch {
		return null;
	}
}

/** 图片在 Word 里的最大宽度（像素，96 dpi）。 */
export const DOCX_IMAGE_MAX_WIDTH = 600;

const MONO = "Consolas";
const CODE_FILL = "F2F2F2";
const MUTED = "595959";
const LINE = "BFBFBF";
const BULLETS = ["•", "◦", "▪"];

/** 用 `docx` 库生成 .docx。`images` 是取到并转好的图片（键为计划里的原始地址）；没有的画成「[图片：说明]」。 */
export async function buildDocx(
	plan: DocxBlock[],
	opts: { title: string; images: ReadonlyMap<string, RasterImage> },
): Promise<Blob> {
	// 动态加载：docx 库很大，只在用户真的导出 Word 时才下载这一块
	const d = await import("docx");
	const runs = (list: DocxInline[], extra: Marks = {}): ParagraphChild[] => {
		const run = (s: DocxSpan, style?: string) =>
			new d.TextRun({
				text: s.text,
				bold: s.bold ?? extra.bold,
				italics: s.italic ?? extra.italic,
				strike: s.strike ?? extra.strike,
				...(s.code
					? {
							font: MONO,
							shading: { type: d.ShadingType.CLEAR, fill: CODE_FILL },
						}
					: {}),
				...(style ? { style } : {}),
			});
		return list.map((i) => {
			switch (i.kind) {
				case "text":
					return run(i);
				case "break":
					return new d.TextRun({ break: 1 });
				case "link":
					return new d.ExternalHyperlink({
						link: i.url,
						children: (i.children.length ? i.children : [{ text: i.url }]).map(
							(s) => run(s, "Hyperlink"),
						),
					});
				case "image": {
					const img = opts.images.get(i.src);
					if (!img) {
						const label = i.alt || i.src;
						return /^https?:/i.test(i.src)
							? new d.ExternalHyperlink({
									link: i.src,
									children: [run({ text: label }, "Hyperlink")],
								})
							: new d.TextRun({
									text: `[${label}]`,
									italics: true,
									color: MUTED,
								});
					}
					const k = Math.min(1, DOCX_IMAGE_MAX_WIDTH / img.width);
					return new d.ImageRun({
						type: img.type,
						data: img.data,
						transformation: {
							width: Math.max(1, Math.round(img.width * k)),
							height: Math.max(1, Math.round(img.height * k)),
						},
						altText: { name: i.alt || "image", description: i.alt },
					});
				}
			}
			return new d.TextRun("");
		});
	};
	const frame = (b: DocxBlock): Partial<IParagraphOptions> => {
		const left = 720 * (b.indent ?? 0) + 360 * (b.quote ?? 0);
		return {
			...(left ? { indent: { left } } : {}),
			...(b.quote
				? {
						border: {
							left: {
								style: d.BorderStyle.SINGLE,
								size: 12,
								color: LINE,
								space: 8,
							},
						},
					}
				: {}),
		};
	};
	const quoteMarks = (b: DocxBlock): Marks => (b.quote ? { italic: true } : {});
	const HEADINGS = [
		d.HeadingLevel.HEADING_1,
		d.HeadingLevel.HEADING_2,
		d.HeadingLevel.HEADING_3,
		d.HeadingLevel.HEADING_4,
		d.HeadingLevel.HEADING_5,
		d.HeadingLevel.HEADING_6,
	];
	const ALIGN = {
		left: d.AlignmentType.LEFT,
		center: d.AlignmentType.CENTER,
		right: d.AlignmentType.RIGHT,
	} as const;
	const children: (Paragraph | Table)[] = [];
	for (const b of plan) {
		switch (b.kind) {
			case "heading":
				children.push(
					new d.Paragraph({
						heading: HEADINGS[b.level - 1],
						children: runs(b.inlines),
						...frame(b),
					}),
				);
				break;
			case "paragraph":
				children.push(
					new d.Paragraph({
						children: runs(b.inlines, quoteMarks(b)),
						spacing: { after: 120 },
						...frame(b),
					}),
				);
				break;
			case "list-item":
				children.push(
					new d.Paragraph({
						numbering: {
							reference: b.ordered ? "oryn-number" : "oryn-bullet",
							level: Math.min(b.level, 8),
							instance: b.list,
						},
						children: [
							...(b.checked === null
								? []
								: [new d.TextRun(b.checked ? "☑ " : "☐ ")]),
							...runs(b.inlines, quoteMarks(b)),
						],
						...frame(b),
					}),
				);
				break;
			case "code":
				children.push(
					new d.Paragraph({
						children: b.text
							.split("\n")
							.map(
								(line, i) =>
									new d.TextRun({ text: line, font: MONO, break: i ? 1 : 0 }),
							),
						shading: { type: d.ShadingType.CLEAR, fill: CODE_FILL },
						spacing: { before: 120, after: 120 },
						...frame(b),
					}),
				);
				break;
			case "table":
				children.push(
					new d.Table({
						width: { size: 100, type: d.WidthType.PERCENTAGE },
						rows: b.rows.map(
							(r, ri) =>
								new d.TableRow({
									tableHeader: ri === 0,
									children: r.map(
										(cell, ci) =>
											new d.TableCell({
												children: [
													new d.Paragraph({
														children: runs(
															cell,
															ri === 0 ? { bold: true } : {},
														),
														...(b.align[ci]
															? { alignment: ALIGN[b.align[ci] as "left"] }
															: {}),
													}),
												],
												...(ri === 0
													? {
															shading: {
																type: d.ShadingType.CLEAR,
																fill: CODE_FILL,
															},
														}
													: {}),
											}),
									),
								}),
						),
					}),
					// 表格之后紧跟的段落要有间隔
					new d.Paragraph({}),
				);
				break;
			case "rule":
				children.push(
					new d.Paragraph({
						border: {
							bottom: {
								style: d.BorderStyle.SINGLE,
								size: 6,
								color: LINE,
								space: 1,
							},
						},
						...frame(b),
					}),
				);
				break;
		}
	}
	const levels = (ordered: boolean) =>
		Array.from({ length: 9 }, (_, level) => ({
			level,
			format: ordered ? d.LevelFormat.DECIMAL : d.LevelFormat.BULLET,
			text: ordered ? `%${level + 1}.` : BULLETS[level % BULLETS.length],
			alignment: d.AlignmentType.LEFT,
			style: {
				paragraph: { indent: { left: 720 * (level + 1), hanging: 360 } },
			},
		}));
	const doc = new d.Document({
		title: opts.title,
		creator: "Oryn",
		numbering: {
			config: [
				{ reference: "oryn-bullet", levels: levels(false) },
				{ reference: "oryn-number", levels: levels(true) },
			],
		},
		sections: [{ children }],
	});
	return d.Packer.toBlob(doc);
}

/**
 * 导出一份文档：解析、取图（版本里的文件经 `readFile`，`data:` 地址直接解码，外部地址保留成链接）、生成 .docx。
 * `missing` 是版本里取不到或解不开的图片（画成占位文字）。
 */
export async function exportDocx(input: {
	title: string;
	markdown: string;
	readFile(path: string): Promise<Uint8Array>;
}): Promise<{ blob: Blob; missing: string[] }> {
	const plan = docxPlan(input.markdown);
	const images = new Map<string, RasterImage>();
	const missing: string[] = [];
	await Promise.all(
		docxImageSources(plan).map(async (src) => {
			if (/^https?:/i.test(src)) return;
			let img: RasterImage | null = null;
			if (src.startsWith("data:")) {
				const b = dataUrlBytes(src);
				const info = b && rasterInfo(b);
				img = b && info ? { data: b, ...info } : null;
			} else {
				const path = assetPath(src);
				img = await input.readFile(path).then(
					(b) => toRaster(b, path),
					() => null,
				);
			}
			if (img) images.set(src, img);
			else missing.push(src);
		}),
	);
	const blob = await buildDocx(plan, { title: input.title, images });
	return { blob, missing };
}
