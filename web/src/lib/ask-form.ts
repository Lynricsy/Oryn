/**
 * omp `ask` 工具表单解析、安全清洗、校验与提交引擎。
 *
 * - 首选从当前运行中的 `ask` 工具调用参数提取原始 `<form>` HTML，完整保留图片、内联 SVG、
 *   表格、代码块、`<select>`、`<input type="number|date|email|url">` 与 HTML5 校验属性；
 * - 无原始 HTML 时（如刷新恢复或非 `ask` 扩展发起的 `form` 请求）无缝降级为 RPC `sections`；
 * - 所有解析均通过惰性 `DOMParser` + 严格元素/属性白名单完成，绝不执行脚本或发起未授权请求。
 */

import type { Entry } from "../api/client";
import type { FormField, FormSection, FormSpan } from "../omp/types";
import type { LiveTool } from "../state/thread-reducer";
import { isRecord } from "../tool-render/util";
import { rasterMime } from "./image-src";

// ---------------------------------------------------------------- AST 类型

export type RichNode =
	| { kind: "text"; text: string }
	| { kind: "code"; text: string }
	| { kind: "strong"; children: RichNode[] }
	| { kind: "em"; children: RichNode[] }
	| { kind: "link"; href: string; children: RichNode[] }
	| { kind: "br" }
	| { kind: "image"; src: string; alt: string };

export interface AskChoiceOption {
	value: string;
	label: RichNode[];
	plainLabel: string;
	checked: boolean;
	disabled: boolean;
	group?: string;
}

export interface AskChoiceField {
	kind: "choice";
	name: string;
	multi: boolean;
	/** 来自 `<select>`（单选渲染为下拉框，多选渲染为多选列表）。 */
	select: boolean;
	/** 单个独立复选框（未勾选时按 omp 规范省略，不额外添加「其他」输入框）。 */
	standalone: boolean;
	/** 是否自动附加「其他」自由文本回答（单选组、2 项及以上复选框组）。 */
	other: boolean;
	label: RichNode[];
	options: AskChoiceOption[];
	required: boolean;
}

export interface AskTextField {
	kind: "text";
	name: string;
	type: string;
	multiline: boolean;
	label: RichNode[];
	value: string;
	placeholder: string;
	required: boolean;
	min?: number;
	max?: number;
	step?: string;
	pattern?: string;
	minLength?: number;
	maxLength?: number;
	rows: number;
}

export type AskFormField = AskChoiceField | AskTextField;

export type AskFormBlock =
	| { kind: "heading"; level: number; nodes: RichNode[] }
	| { kind: "paragraph"; nodes: RichNode[] }
	| { kind: "list"; ordered: boolean; items: RichNode[][] }
	| { kind: "pre"; text: string }
	| { kind: "blockquote"; nodes: RichNode[] }
	| { kind: "hr" }
	| { kind: "table"; headerRows: number; rows: RichNode[][][] }
	| { kind: "image"; src: string; alt: string; width?: string; height?: string }
	| { kind: "svg"; markup: string; alt: string }
	| {
			kind: "details";
			summary: RichNode[];
			blocks: AskFormBlock[];
			open?: boolean;
	  }
	| { kind: "field"; field: AskFormField };

export interface AskFormSection {
	title: RichNode[];
	blocks: AskFormBlock[];
}

export interface AskFormDoc {
	sections: AskFormSection[];
	fields: AskFormField[];
	source: "html" | "rpc";
}

/** 表单控件运行时状态（按字段 name 索引）。 */
export interface AskFieldValue {
	/** 文本类控件值，或选项类控件的「其他」自由文本。 */
	text: string;
	/** 选项类控件选中的 option.value 列表。 */
	selected: string[];
	/** 单选组当前是否选中了「其他」项。 */
	otherActive?: boolean;
}

export type AskFormValues = Record<string, AskFieldValue>;

export interface AskValidationError {
	key:
		| "required"
		| "number"
		| "min"
		| "max"
		| "minLength"
		| "maxLength"
		| "pattern"
		| "email"
		| "url";
	params?: Record<string, string | number>;
}

// ---------------------------------------------------------------- 工具函数

const SKIP_INPUT_TYPES: Readonly<Record<string, true>> = {
	hidden: true,
	submit: true,
	button: true,
	reset: true,
	image: true,
};

export function richNodesToPlainText(nodes: readonly RichNode[]): string {
	const parts: string[] = [];
	for (const n of nodes) {
		switch (n.kind) {
			case "text":
			case "code":
				parts.push(n.text);
				break;
			case "strong":
			case "em":
			case "link":
				parts.push(richNodesToPlainText(n.children));
				break;
			case "br":
				parts.push("\n");
				break;
			case "image":
				if (n.alt) parts.push(n.alt);
				break;
		}
	}
	return parts.join("").replace(/\s+/g, " ").trim();
}

function spansToRichNodes(spans: readonly FormSpan[]): RichNode[] {
	const out: RichNode[] = [];
	for (const s of spans) {
		if (!s.text) continue;
		if (s.style === "code") out.push({ kind: "code", text: s.text });
		else if (s.style === "strong")
			out.push({ kind: "strong", children: [{ kind: "text", text: s.text }] });
		else if (s.style === "em")
			out.push({ kind: "em", children: [{ kind: "text", text: s.text }] });
		else out.push({ kind: "text", text: s.text });
	}
	return out;
}

/**
 * 拆解常见选项文案中的主标题与副说明（如 `SQLite: one file, no new service` 或 `方案 A — 推荐`）。
 * 跳过含 `://` 的 URL 前缀，避免误拆链接。
 */
export function splitOptionLabel(text: string): {
	title: string;
	description: string | null;
} {
	const trimmed = text.trim();
	const dashAt = trimmed.indexOf(" — ");
	if (dashAt > 0 && dashAt <= 60) {
		const desc = trimmed.slice(dashAt + 3).trim();
		if (desc)
			return { title: trimmed.slice(0, dashAt).trim(), description: desc };
	}
	const colonMatch = /^([^:：\n]{1,48})[：:]\s+(.+)$/.exec(trimmed);
	if (colonMatch) {
		const head = colonMatch[1]?.trim() ?? "";
		const desc = colonMatch[2]?.trim() ?? "";
		if (head && desc && !/^https?$/i.test(head)) {
			return { title: head, description: desc };
		}
	}
	return { title: trimmed, description: null };
}

function parseFiniteNum(raw: string | null): number | undefined {
	if (raw === null || raw.trim() === "") return undefined;
	const n = Number(raw);
	return Number.isFinite(n) ? n : undefined;
}

function safeHref(raw: string | null): string | null {
	if (!raw) return null;
	const v = raw.trim();
	if (/^(https?:|mailto:)/i.test(v)) return v;
	return null;
}

/** 仅允许安全尺寸字面量（纯像素或 px/%/em/rem/ch），防止注入。 */
function safeDimension(raw: string | null): string | undefined {
	if (!raw) return undefined;
	const v = raw.trim();
	if (/^\d+(?:\.\d+)?$/.test(v)) return `${v}px`;
	if (/^\d+(?:\.\d+)?(?:px|%|em|rem|ch|vw|vh)$/i.test(v)) return v;
	return undefined;
}

// ---------------------------------------------------------------- 图片与 SVG 安全处理

export type ResolvedFormImage =
	| { kind: "external"; url: string }
	| { kind: "data"; url: string }
	| { kind: "svg"; markup: string }
	| { kind: "orb"; path: string }
	| { kind: "invalid" };

function normalizePosixPath(path: string): string {
	const isAbs = path.startsWith("/");
	const parts = path.split("/");
	const stack: string[] = [];
	for (const seg of parts) {
		if (!seg || seg === ".") continue;
		if (seg === "..") {
			if (stack.length > 0 && stack[stack.length - 1] !== "..") stack.pop();
			else if (!isAbs) stack.push("..");
		} else {
			stack.push(seg);
		}
	}
	return `${isAbs ? "/" : ""}${stack.join("/")}` || (isAbs ? "/" : ".");
}

function decodeDataSvg(uri: string): string | null {
	const comma = uri.indexOf(",");
	if (comma < 0) return null;
	const header = uri.slice(0, comma).toLowerCase();
	const body = uri.slice(comma + 1);
	try {
		if (header.includes(";base64")) {
			return atob(body.replace(/\s+/g, ""));
		}
		return decodeURIComponent(body);
	} catch {
		return null;
	}
}

/** 解析 `<img>` 的 `src`：区分外链、栅格 `data:` URI、`data:image/svg+xml` 与工作区本地路径。 */
export function resolveFormImageSource(
	rawSrc: string,
	cwd?: string,
): ResolvedFormImage {
	const src = rawSrc.trim();
	if (!src) return { kind: "invalid" };
	if (/^https?:\/\//i.test(src)) return { kind: "external", url: src };
	if (/^data:/i.test(src)) {
		const m = /^data:([^;,]+)(;base64)?,(.*)$/is.exec(src);
		if (!m) return { kind: "invalid" };
		const rawMime = (m[1] ?? "").trim().toLowerCase();
		if (rawMime === "image/svg+xml") {
			const svg = decodeDataSvg(src);
			const clean = svg ? sanitizeSvgMarkup(svg) : null;
			return clean ? { kind: "svg", markup: clean } : { kind: "invalid" };
		}
		if (rasterMime(rawMime) && m[2]) {
			return { kind: "data", url: src };
		}
		return { kind: "invalid" };
	}
	// 拒绝其它协议（javascript:, vbscript:, blob: 等），只允许 file: 或文件路径
	if (/^[a-z][a-z0-9+.-]*:/i.test(src) && !/^file:\/\//i.test(src)) {
		return { kind: "invalid" };
	}
	let localPath = src;
	if (/^file:\/\//i.test(localPath)) {
		try {
			localPath = decodeURIComponent(localPath.replace(/^file:\/\//i, ""));
		} catch {
			localPath = localPath.replace(/^file:\/\//i, "");
		}
	}
	if (!localPath) return { kind: "invalid" };
	if (localPath.startsWith("~/") || localPath.startsWith("/")) {
		return {
			kind: "orb",
			path: localPath.startsWith("~/")
				? localPath
				: normalizePosixPath(localPath),
		};
	}
	const base = cwd?.replace(/\/+$/, "");
	return {
		kind: "orb",
		path: base ? normalizePosixPath(`${base}/${localPath}`) : localPath,
	};
}

const ALLOWED_SVG_TAGS: Readonly<Record<string, true>> = {
	svg: true,
	g: true,
	defs: true,
	symbol: true,
	use: true,
	title: true,
	desc: true,
	path: true,
	rect: true,
	circle: true,
	ellipse: true,
	line: true,
	polyline: true,
	polygon: true,
	text: true,
	tspan: true,
	textpath: true,
	lineargradient: true,
	radialgradient: true,
	stop: true,
	clippath: true,
	mask: true,
	pattern: true,
	marker: true,
	filter: true,
	fegaussianblur: true,
	fedropshadow: true,
	feoffset: true,
	feblend: true,
	fecolormatrix: true,
	femerge: true,
	femergenode: true,
	feflood: true,
	fecomposite: true,
};

const FORBIDDEN_SVG_ATTR = /^(on[a-z]+|xmlns:xlink)$/i;

function sanitizeCssValue(raw: string): string {
	// 拦截 url(http...)、expression、@import、javascript:，保留安全的 url(#id) 和 var(--...)
	return raw
		.split(";")
		.map((part) => part.trim())
		.filter((part) => {
			if (!part) return false;
			if (/expression\s*\(|javascript:|@import/i.test(part)) return false;
			const urls = part.match(/url\s*\(([^)]*)\)/gi) ?? [];
			for (const u of urls) {
				if (!/^url\s*\(\s*['"]?#[a-zA-Z0-9_-]+['"]?\s*\)$/i.test(u))
					return false;
			}
			return true;
		})
		.join("; ");
}

function cleanSvgElement(el: Element): void {
	const attrs = [...el.attributes];
	for (const attr of attrs) {
		const name = attr.name;
		const lower = name.toLowerCase();
		const value = attr.value.trim();
		if (FORBIDDEN_SVG_ATTR.test(lower)) {
			el.removeAttribute(name);
			continue;
		}
		if (lower === "href" || lower === "xlink:href") {
			if (!/^#[a-zA-Z0-9_-]+$/.test(value)) {
				el.removeAttribute(name);
			}
			continue;
		}
		if (lower === "style") {
			const clean = sanitizeCssValue(value);
			if (clean) el.setAttribute(name, clean);
			else el.removeAttribute(name);
			continue;
		}
		if (/javascript:/i.test(value)) {
			el.removeAttribute(name);
			continue;
		}
		if (/url\s*\(/i.test(value)) {
			const urls = value.match(/url\s*\(([^)]*)\)/gi) ?? [];
			if (
				urls.some(
					(u) => !/^url\s*\(\s*['"]?#[a-zA-Z0-9_-]+['"]?\s*\)$/i.test(u),
				)
			) {
				el.removeAttribute(name);
			}
		}
	}
	for (const child of [...el.childNodes]) {
		if (child.nodeType === 1) {
			const childEl = child as Element;
			if (!ALLOWED_SVG_TAGS[childEl.localName.toLowerCase()]) {
				childEl.remove();
			} else {
				cleanSvgElement(childEl);
			}
		} else if (child.nodeType === 8) {
			// 移除注释
			child.remove();
		}
	}
}

/**
 * 严格白名单清洗内联 `<svg>`：剔除 `<script>`、`<foreignObject>`、动画、事件处理器与外部引用，
 * 并保证 SVG 在容器内等比缩放不溢出。
 */
export function sanitizeSvgMarkup(markup: string): string | null {
	if (typeof DOMParser === "undefined" || !markup.trim()) return null;
	const parser = new DOMParser();
	let svg: Element | null = null;
	const xmlDoc = parser.parseFromString(markup, "image/svg+xml");
	if (
		!xmlDoc.querySelector("parsererror") &&
		xmlDoc.documentElement?.localName.toLowerCase() === "svg"
	) {
		svg = xmlDoc.documentElement;
	} else {
		const htmlDoc = parser.parseFromString(markup, "text/html");
		svg = htmlDoc.querySelector("svg");
	}
	if (!svg) return null;
	cleanSvgElement(svg);
	// 若只写了数值 width/height 而缺 viewBox，补出 viewBox 再交给 CSS 自适应
	if (!svg.getAttribute("viewBox")) {
		const w = Number.parseFloat(svg.getAttribute("width") ?? "");
		const h = Number.parseFloat(svg.getAttribute("height") ?? "");
		if (Number.isFinite(w) && w > 0 && Number.isFinite(h) && h > 0) {
			svg.setAttribute("viewBox", `0 0 ${w} ${h}`);
		}
	}
	svg.removeAttribute("width");
	svg.removeAttribute("height");
	svg.setAttribute("class", "ask-svg-graphic");
	return new XMLSerializer().serializeToString(svg);
}

// ---------------------------------------------------------------- HTML 表单解析器

const INLINE_TAGS: Readonly<Record<string, true>> = {
	SPAN: true,
	STRONG: true,
	B: true,
	EM: true,
	I: true,
	CODE: true,
	KBD: true,
	SAMP: true,
	SMALL: true,
	MARK: true,
	A: true,
	BR: true,
	SUB: true,
	SUP: true,
	ABBR: true,
	U: true,
	S: true,
	DEL: true,
	INS: true,
};

function extractRichNodes(
	container: Node,
	opts: { skipControls?: boolean; imageAsAlt?: boolean } = {},
): RichNode[] {
	const out: RichNode[] = [];
	for (const child of container.childNodes) {
		if (child.nodeType === 3) {
			const raw = child.textContent ?? "";
			const collapsed = raw.replace(/\s+/g, " ");
			if (collapsed) out.push({ kind: "text", text: collapsed });
			continue;
		}
		if (child.nodeType !== 1) continue;
		const el = child as Element;
		const tag = el.tagName.toUpperCase();
		if (tag === "SCRIPT" || tag === "STYLE" || tag === "BUTTON") continue;
		if (
			opts.skipControls &&
			(tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA")
		) {
			continue;
		}
		if (tag === "BR") {
			out.push({ kind: "br" });
			continue;
		}
		if (tag === "IMG") {
			const alt = (el.getAttribute("alt") ?? "").trim();
			const src = (el.getAttribute("src") ?? "").trim();
			if (opts.imageAsAlt) {
				if (alt) out.push({ kind: "text", text: alt });
			} else if (src) {
				out.push({ kind: "image", src, alt });
			}
			continue;
		}
		if (tag === "CODE" || tag === "KBD" || tag === "SAMP") {
			const text = (el.textContent ?? "").trim();
			if (text) out.push({ kind: "code", text });
			continue;
		}
		if (tag === "STRONG" || tag === "B") {
			const children = extractRichNodes(el, opts);
			if (children.length > 0) out.push({ kind: "strong", children });
			continue;
		}
		if (tag === "EM" || tag === "I") {
			const children = extractRichNodes(el, opts);
			if (children.length > 0) out.push({ kind: "em", children });
			continue;
		}
		if (tag === "A") {
			const href = safeHref(el.getAttribute("href"));
			const children = extractRichNodes(el, opts);
			if (children.length > 0) {
				if (href) out.push({ kind: "link", href, children });
				else out.push(...children);
			}
			continue;
		}
		out.push(...extractRichNodes(el, opts));
	}
	return trimRichNodes(out);
}

function trimRichNodes(nodes: RichNode[]): RichNode[] {
	const merged: RichNode[] = [];
	for (const n of nodes) {
		const prev = merged[merged.length - 1];
		if (n.kind === "text" && prev?.kind === "text") {
			prev.text = `${prev.text}${n.text}`.replace(/\s+/g, " ");
		} else {
			merged.push(n.kind === "text" ? { ...n } : n);
		}
	}
	while (merged[0]?.kind === "text") {
		merged[0].text = merged[0].text.replace(/^\s+/, "");
		if (!merged[0].text) merged.shift();
		else break;
	}
	while (merged[merged.length - 1]?.kind === "text") {
		const last = merged[merged.length - 1];
		if (last?.kind === "text") {
			last.text = last.text.replace(/\s+$/, "");
			if (!last.text) merged.pop();
			else break;
		}
	}
	return merged;
}

function parseTableElement(table: Element): AskFormBlock | null {
	const rows: RichNode[][][] = [];
	let headerRows = 0;
	const trs = table.querySelectorAll("tr");
	for (const tr of trs) {
		if (tr.closest("table") !== table) continue;
		const cells: RichNode[][] = [];
		let allTh = true;
		for (const cell of tr.children) {
			const tag = cell.tagName.toUpperCase();
			if (tag !== "TD" && tag !== "TH") continue;
			if (tag !== "TH") allTh = false;
			cells.push(extractRichNodes(cell, { imageAsAlt: true }));
		}
		if (cells.length === 0) continue;
		const inThead = tr.parentElement?.tagName.toUpperCase() === "THEAD";
		if (
			(inThead || (rows.length === 0 && allTh)) &&
			headerRows === rows.length
		) {
			headerRows++;
		}
		rows.push(cells);
	}
	if (rows.length === 0) return null;
	return { kind: "table", headerRows, rows };
}

/**
 * 解析 omp `ask` 工具传入的原始 HTML 字符串，返回结构化表单文档。
 * DOMParser 不可用或 HTML 不含可识别字段时返回 null。
 */
export function parseAskHtml(html: string): AskFormDoc | null {
	if (typeof DOMParser === "undefined" || !html.trim()) return null;
	const doc = new DOMParser().parseFromString(html, "text/html");
	const root = doc.querySelector("form") ?? doc.body;
	if (!root) return null;

	const fieldsByName = new Map<string, AskFormField>();
	const fields: AskFormField[] = [];
	const sections: AskFormSection[] = [];
	let looseBlocks: AskFormBlock[] = [];

	const flushLoose = () => {
		if (looseBlocks.length > 0) {
			sections.push({ title: [], blocks: looseBlocks });
			looseBlocks = [];
		}
	};

	const labelForControl = (el: Element): RichNode[] => {
		const wrapping = el.closest("label");
		if (wrapping) {
			return extractRichNodes(wrapping, {
				skipControls: true,
				imageAsAlt: true,
			});
		}
		const id = el.getAttribute("id");
		if (id && typeof CSS !== "undefined") {
			const linked = doc.querySelector(`label[for="${CSS.escape(id)}"]`);
			if (linked)
				return extractRichNodes(linked, {
					skipControls: true,
					imageAsAlt: true,
				});
		}
		return [];
	};

	const registerControl = (
		el: Element,
		targetBlocks: AskFormBlock[],
		labelNodes?: RichNode[],
	) => {
		const tag = el.tagName.toUpperCase();
		const name = (el.getAttribute("name") ?? "").trim();
		if (!name) return;
		const rawType = (el.getAttribute("type") ?? "text").trim().toLowerCase();
		if (tag === "INPUT" && SKIP_INPUT_TYPES[rawType]) return;
		if (tag === "INPUT" && (rawType === "radio" || rawType === "checkbox")) {
			const multi = rawType === "checkbox";
			const optLabel = labelNodes ?? labelForControl(el);
			const plain = richNodesToPlainText(optLabel);
			const value = el.getAttribute("value") ?? (plain || "on");
			const option: AskChoiceOption = {
				value,
				label: optLabel.length > 0 ? optLabel : [{ kind: "text", text: value }],
				plainLabel: plain || value,
				checked: el.hasAttribute("checked"),
				disabled: el.hasAttribute("disabled"),
			};
			const existing = fieldsByName.get(name);
			if (existing && existing.kind === "choice") {
				existing.options.push(option);
				if (el.hasAttribute("required")) existing.required = true;
				return;
			}
			const field: AskChoiceField = {
				kind: "choice",
				name,
				multi,
				select: false,
				standalone: false,
				other: false,
				label: [],
				options: [option],
				required: el.hasAttribute("required"),
			};
			fieldsByName.set(name, field);
			fields.push(field);
			targetBlocks.push({ kind: "field", field });
			return;
		}

		if (tag === "SELECT") {
			const multi = el.hasAttribute("multiple");
			const options: AskChoiceOption[] = [];
			const readOpt = (opt: Element, group?: string) => {
				const text = (opt.textContent ?? "").replace(/\s+/g, " ").trim();
				const value = opt.getAttribute("value") ?? text;
				options.push({
					value,
					label: [{ kind: "text", text: text || value }],
					plainLabel: text || value,
					checked: opt.hasAttribute("selected"),
					disabled: opt.hasAttribute("disabled"),
					group,
				});
			};
			for (const child of el.children) {
				const cTag = child.tagName.toUpperCase();
				if (cTag === "OPTION") readOpt(child);
				else if (cTag === "OPTGROUP") {
					const gLabel = child.getAttribute("label") ?? undefined;
					for (const opt of child.children) {
						if (opt.tagName.toUpperCase() === "OPTION") readOpt(opt, gLabel);
					}
				}
			}
			const field: AskChoiceField = {
				kind: "choice",
				name,
				multi,
				select: true,
				standalone: false,
				other: false,
				label: labelNodes ?? labelForControl(el),
				options,
				required: el.hasAttribute("required"),
			};
			if (!fieldsByName.has(name)) {
				fieldsByName.set(name, field);
				fields.push(field);
				targetBlocks.push({ kind: "field", field });
			}
			return;
		}

		if (tag === "TEXTAREA" || tag === "INPUT") {
			const multiline = tag === "TEXTAREA";
			const field: AskTextField = {
				kind: "text",
				name,
				type: multiline ? "textarea" : rawType,
				multiline,
				label: labelNodes ?? labelForControl(el),
				value: multiline
					? (el.textContent ?? "")
					: (el.getAttribute("value") ?? ""),
				placeholder: el.getAttribute("placeholder") ?? "",
				required: el.hasAttribute("required"),
				min: parseFiniteNum(el.getAttribute("min")),
				max: parseFiniteNum(el.getAttribute("max")),
				step: el.getAttribute("step") ?? undefined,
				pattern: el.getAttribute("pattern") ?? undefined,
				minLength: parseFiniteNum(el.getAttribute("minlength")),
				maxLength: parseFiniteNum(el.getAttribute("maxlength")),
				rows: multiline
					? Math.max(2, parseFiniteNum(el.getAttribute("rows")) ?? 3)
					: 1,
			};
			if (!fieldsByName.has(name)) {
				fieldsByName.set(name, field);
				fields.push(field);
				targetBlocks.push({ kind: "field", field });
			}
		}
	};

	const walkChildren = (
		parent: Element,
		scopedBlocks: AskFormBlock[] | null,
	) => {
		const target = () => scopedBlocks ?? looseBlocks;
		let inlineBuf: Node[] = [];
		const flushInline = () => {
			if (inlineBuf.length === 0) return;
			const wrapper = doc.createElement("span");
			for (const n of inlineBuf) wrapper.appendChild(n.cloneNode(true));
			inlineBuf = [];
			const nodes = extractRichNodes(wrapper);
			if (nodes.length > 0) target().push({ kind: "paragraph", nodes });
		};

		for (const child of parent.childNodes) {
			if (child.nodeType === 3) {
				if ((child.textContent ?? "").trim()) inlineBuf.push(child);
				continue;
			}
			if (child.nodeType !== 1) continue;
			const el = child as Element;
			const tag = el.tagName.toUpperCase();
			if (tag === "SCRIPT" || tag === "STYLE" || tag === "BUTTON") continue;
			if (
				INLINE_TAGS[tag] &&
				!el.querySelector("input, select, textarea, svg, img")
			) {
				inlineBuf.push(el);
				continue;
			}
			flushInline();

			if (tag === "FIELDSET") {
				if (scopedBlocks === null) flushLoose();
				const legend = el.querySelector(":scope > legend");
				const title = legend
					? extractRichNodes(legend, { imageAsAlt: true })
					: [];
				const secBlocks: AskFormBlock[] = [];
				walkChildren(el, secBlocks);
				if (scopedBlocks === null) {
					sections.push({ title, blocks: secBlocks });
				} else {
					if (title.length > 0)
						scopedBlocks.push({ kind: "heading", level: 3, nodes: title });
					scopedBlocks.push(...secBlocks);
				}
				continue;
			}
			if (tag === "LEGEND") continue;
			if (/^H[1-6]$/.test(tag)) {
				const nodes = extractRichNodes(el, { imageAsAlt: true });
				if (nodes.length > 0)
					target().push({
						kind: "heading",
						level: Number(tag[1]),
						nodes,
					});
				continue;
			}
			if (tag === "P") {
				if (el.querySelector("input, select, textarea")) {
					walkChildren(el, scopedBlocks);
				} else {
					const nodes = extractRichNodes(el);
					if (nodes.length > 0) target().push({ kind: "paragraph", nodes });
				}
				continue;
			}
			if (tag === "UL" || tag === "OL") {
				if (el.querySelector("input, select, textarea")) {
					walkChildren(el, scopedBlocks);
				} else {
					const items: RichNode[][] = [];
					for (const li of el.children) {
						if (li.tagName.toUpperCase() !== "LI") continue;
						const nodes = extractRichNodes(li);
						if (nodes.length > 0) items.push(nodes);
					}
					if (items.length > 0)
						target().push({ kind: "list", ordered: tag === "OL", items });
				}
				continue;
			}
			if (tag === "PRE") {
				const text = el.textContent ?? "";
				if (text.trim()) target().push({ kind: "pre", text });
				continue;
			}
			if (tag === "BLOCKQUOTE") {
				const nodes = extractRichNodes(el);
				if (nodes.length > 0) target().push({ kind: "blockquote", nodes });
				continue;
			}
			if (tag === "HR") {
				target().push({ kind: "hr" });
				continue;
			}
			if (tag === "TABLE") {
				const tbl = parseTableElement(el);
				if (tbl) target().push(tbl);
				continue;
			}
			if (tag === "IMG") {
				const src = (el.getAttribute("src") ?? "").trim();
				if (src) {
					target().push({
						kind: "image",
						src,
						alt: (el.getAttribute("alt") ?? "").trim(),
						width: safeDimension(el.getAttribute("width")),
						height: safeDimension(el.getAttribute("height")),
					});
				}
				continue;
			}
			if (tag === "SVG") {
				const clean = sanitizeSvgMarkup(el.outerHTML);
				if (clean) {
					const alt =
						el.getAttribute("aria-label")?.trim() ||
						el.querySelector("title")?.textContent?.trim() ||
						"";
					target().push({ kind: "svg", markup: clean, alt });
				}
				continue;
			}
			if (tag === "DETAILS") {
				const summaryEl = el.querySelector(":scope > summary");
				const summary = summaryEl
					? extractRichNodes(summaryEl, { imageAsAlt: true })
					: [];
				const inner: AskFormBlock[] = [];
				const clone = el.cloneNode(true) as Element;
				clone.querySelector(":scope > summary")?.remove();
				walkChildren(clone, inner);
				target().push({
					kind: "details",
					summary,
					blocks: inner,
					open: el.hasAttribute("open"),
				});
				continue;
			}
			if (tag === "SUMMARY") continue;
			if (tag === "LABEL") {
				const control = el.querySelector("input, select, textarea");
				if (control) {
					const labelNodes = extractRichNodes(el, {
						skipControls: true,
						imageAsAlt: true,
					});
					registerControl(control, target(), labelNodes);
				} else {
					const nodes = extractRichNodes(el, { imageAsAlt: true });
					if (nodes.length > 0) target().push({ kind: "paragraph", nodes });
				}
				continue;
			}
			if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA") {
				registerControl(el, target());
				continue;
			}
			// 通用容器（div / section / figure / figcaption / li / form …）：递归解包
			walkChildren(el, scopedBlocks);
		}
		flushInline();
	};

	walkChildren(root, null);
	flushLoose();

	// 按 omp 规范补齐每个 choice 字段的 standalone 与 other 标记
	for (const f of fields) {
		if (f.kind !== "choice" || f.select) continue;
		if (f.multi && f.options.length === 1) {
			f.standalone = true;
			f.other = false;
		} else {
			f.standalone = false;
			f.other = f.options.length >= (f.multi ? 2 : 1);
		}
	}

	if (fields.length === 0) return null;
	return { sections, fields, source: "html" };
}

// ---------------------------------------------------------------- RPC FormSection 归一化

function rpcFieldToAskField(field: FormField): AskFormField {
	if (field.kind === "text") {
		return {
			kind: "text",
			name: field.name,
			type: field.type || (field.multiline ? "textarea" : "text"),
			multiline: field.multiline,
			label: spansToRichNodes(field.label),
			value: field.value,
			placeholder: field.placeholder,
			required: field.required,
			min: field.min,
			max: field.max,
			pattern: field.pattern,
			minLength: field.minLength,
			maxLength: field.maxLength,
			rows: field.rows || (field.multiline ? 3 : 1),
		};
	}
	const standalone = !field.select && field.multi && field.options.length === 1;
	return {
		kind: "choice",
		name: field.name,
		multi: field.multi,
		select: field.select,
		standalone,
		other: field.other && !standalone,
		label: spansToRichNodes(field.label),
		options: field.options.map((o) => {
			const nodes = spansToRichNodes(o.label);
			return {
				value: o.value,
				label: nodes.length > 0 ? nodes : [{ kind: "text", text: o.value }],
				plainLabel: richNodesToPlainText(nodes) || o.value,
				checked: o.checked,
				disabled: o.disabled,
			};
		}),
		required: field.required,
	};
}

/** 将 RPC `extension_ui_request { method: "form" }` 的 `sections` 归一化为同一套 `AskFormDoc`。 */
export function normalizeRpcFormSections(
	rawSections: readonly FormSection[],
): AskFormDoc {
	const fields: AskFormField[] = [];
	const sections: AskFormSection[] = [];
	for (const sec of rawSections) {
		const blocks: AskFormBlock[] = [];
		for (const rawBlock of sec.blocks as readonly Record<string, unknown>[]) {
			const kind = rawBlock.kind;
			if (kind === "text" && Array.isArray(rawBlock.spans)) {
				const nodes = spansToRichNodes(rawBlock.spans as FormSpan[]);
				if (nodes.length > 0) {
					blocks.push(
						rawBlock.heading === true
							? { kind: "heading", level: 3, nodes }
							: { kind: "paragraph", nodes },
					);
				}
			} else if (kind === "list" && Array.isArray(rawBlock.items)) {
				blocks.push({
					kind: "list",
					ordered: rawBlock.ordered === true,
					items: (rawBlock.items as FormSpan[][]).map(spansToRichNodes),
				});
			} else if (kind === "pre" && typeof rawBlock.text === "string") {
				blocks.push({ kind: "pre", text: rawBlock.text });
			} else if (kind === "table" && Array.isArray(rawBlock.rows)) {
				blocks.push({
					kind: "table",
					headerRows:
						typeof rawBlock.headerRows === "number" ? rawBlock.headerRows : 0,
					rows: (rawBlock.rows as FormSpan[][][]).map((row) =>
						row.map(spansToRichNodes),
					),
				});
			} else if (kind === "image" && typeof rawBlock.src === "string") {
				blocks.push({
					kind: "image",
					src: rawBlock.src,
					alt: typeof rawBlock.alt === "string" ? rawBlock.alt : "",
				});
			} else if (kind === "svg" && typeof rawBlock.markup === "string") {
				const clean = sanitizeSvgMarkup(rawBlock.markup);
				if (clean) {
					blocks.push({
						kind: "svg",
						markup: clean,
						alt: typeof rawBlock.alt === "string" ? rawBlock.alt : "",
					});
				}
			} else if (kind === "field" && isRecord(rawBlock.field)) {
				const field = rpcFieldToAskField(
					rawBlock.field as unknown as FormField,
				);
				fields.push(field);
				blocks.push({ kind: "field", field });
			}
		}
		sections.push({
			title: spansToRichNodes(sec.title),
			blocks,
		});
	}
	return { sections, fields, source: "rpc" };
}

// ---------------------------------------------------------------- 从线程状态匹配当前 ask 原始 HTML

function rpcFieldNames(sections: readonly FormSection[]): Set<string> {
	const names = new Set<string>();
	for (const sec of sections) {
		for (const b of sec.blocks) {
			if (b.kind === "field") names.add(b.field.name);
		}
	}
	return names;
}

function htmlMatchesFields(
	html: string,
	expected: Set<string>,
): AskFormDoc | null {
	const parsed = parseAskHtml(html);
	if (!parsed) return null;
	if (expected.size === 0) return parsed;
	const got = new Set(parsed.fields.map((f) => f.name));
	for (const name of expected) {
		if (!got.has(name)) return null;
	}
	return parsed;
}

/**
 * 尝试从当前线程的运行中工具或最近一条 assistant 条目中，找到与该 `form` 请求匹配的原始 `ask` HTML。
 * 这样即便 RPC `RpcFormBlock` 剔除了图片和内联 SVG，前端也能完整还原 HTML 富内容与图文展示。
 */
export function resolveAskFormDoc(
	sections: readonly FormSection[],
	liveTools: Record<string, LiveTool>,
	liveToolOrder: readonly string[],
	entries: readonly Entry[],
): AskFormDoc {
	const expected = rpcFieldNames(sections);

	// 1. 优先查当前运行中的 `ask` liveTool（逆序取最新）
	for (let i = liveToolOrder.length - 1; i >= 0; i--) {
		const id = liveToolOrder[i];
		const tool = id ? liveTools[id] : undefined;
		if (!tool || tool.ended || tool.toolName !== "ask") continue;
		const html =
			isRecord(tool.args) && typeof tool.args.html === "string"
				? tool.args.html
				: null;
		if (!html) continue;
		const matched = htmlMatchesFields(html, expected);
		if (matched) return matched;
	}

	// 2. 兜底：查转录末尾若干条里最近一次未结束的 assistant `ask` toolCall（刷新恢复快照时有用）
	for (let i = entries.length - 1; i >= Math.max(0, entries.length - 12); i--) {
		const entry = entries[i];
		if (entry?.type !== "message" || !isRecord(entry.payload)) continue;
		const msg = entry.payload.message;
		if (
			!isRecord(msg) ||
			msg.role !== "assistant" ||
			!Array.isArray(msg.content)
		)
			continue;
		for (let j = msg.content.length - 1; j >= 0; j--) {
			const blk = msg.content[j];
			if (
				!isRecord(blk) ||
				blk.type !== "toolCall" ||
				blk.name !== "ask" ||
				!isRecord(blk.arguments) ||
				typeof blk.arguments.html !== "string"
			) {
				continue;
			}
			const matched = htmlMatchesFields(blk.arguments.html, expected);
			if (matched) return matched;
		}
	}

	// 3. 降级：直接使用 RPC `sections`
	return normalizeRpcFormSections(sections);
}

// ---------------------------------------------------------------- 状态初始化、校验与提交

export function createInitialFormValues(doc: AskFormDoc): AskFormValues {
	const values: AskFormValues = {};
	for (const f of doc.fields) {
		if (f.kind === "text") {
			values[f.name] = { text: f.value, selected: [] };
		} else {
			const checked = f.options.filter((o) => o.checked).map((o) => o.value);
			const firstEnabled = f.options.find((o) => !o.disabled);
			const initialSelected =
				f.select && !f.multi && checked.length === 0
					? firstEnabled
						? [firstEnabled.value]
						: []
					: f.multi
						? checked
						: checked.slice(0, 1);
			values[f.name] = {
				text: "",
				selected: initialSelected,
				otherActive: false,
			};
		}
	}
	return values;
}

export function isFieldAnswered(
	field: AskFormField,
	val: AskFieldValue | undefined,
): boolean {
	if (!val) return false;
	if (field.kind === "text") return val.text.trim() !== "";
	if (field.multi) return val.selected.length > 0 || val.text.trim() !== "";
	if (val.otherActive) return val.text.trim() !== "";
	return val.selected.length > 0;
}

export function validateAskForm(
	doc: AskFormDoc,
	values: AskFormValues,
): Record<string, AskValidationError> {
	const errors: Record<string, AskValidationError> = {};
	for (const f of doc.fields) {
		const v = values[f.name] ?? { text: "", selected: [] };
		if (f.kind === "choice") {
			if (f.required && !isFieldAnswered(f, v)) {
				errors[f.name] = { key: "required" };
			}
			continue;
		}
		const raw = v.text;
		const trimmed = raw.trim();
		if (f.required && trimmed === "") {
			errors[f.name] = { key: "required" };
			continue;
		}
		if (trimmed === "") continue;
		if (f.type === "number") {
			const n = Number(trimmed);
			if (!Number.isFinite(n)) {
				errors[f.name] = { key: "number" };
				continue;
			}
			if (f.min !== undefined && n < f.min) {
				errors[f.name] = { key: "min", params: { min: f.min } };
				continue;
			}
			if (f.max !== undefined && n > f.max) {
				errors[f.name] = { key: "max", params: { max: f.max } };
				continue;
			}
		}
		if (f.type === "email" && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed)) {
			errors[f.name] = { key: "email" };
			continue;
		}
		if (f.type === "url") {
			try {
				const parsed = new URL(trimmed);
				if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
					errors[f.name] = { key: "url" };
					continue;
				}
			} catch {
				errors[f.name] = { key: "url" };
				continue;
			}
		}
		if (f.minLength !== undefined && raw.length < f.minLength) {
			errors[f.name] = { key: "minLength", params: { min: f.minLength } };
			continue;
		}
		if (f.maxLength !== undefined && raw.length > f.maxLength) {
			errors[f.name] = { key: "maxLength", params: { max: f.maxLength } };
			continue;
		}
		if (f.pattern) {
			try {
				const re = new RegExp(`^(?:${f.pattern})$`);
				if (!re.test(raw)) {
					errors[f.name] = { key: "pattern" };
				}
			} catch {
				// 无效正则不阻断提交
			}
		}
	}
	return errors;
}

/**
 * 按 omp RPC `extension_ui_response.formData` 协议构造提交载荷：
 * 文本类控件（含 `type="number"`）与单选控件一律回传 `string`（数值转换由 Core 完成），
 * 多选控件（含 checkbox）一律回传 `string[]`。
 */
export function buildAskFormData(
	doc: AskFormDoc,
	values: AskFormValues,
): Record<string, string | string[]> {
	const out: Record<string, string | string[]> = {};
	for (const f of doc.fields) {
		const v = values[f.name] ?? { text: "", selected: [] };
		if (f.kind === "text") {
			out[f.name] = v.text;
			continue;
		}
		if (f.multi) {
			const other = f.other ? v.text.trim() : "";
			out[f.name] = other ? [...v.selected, other] : [...v.selected];
			continue;
		}
		// 单选 radio 或单选 <select>
		if (v.otherActive && v.text.trim() !== "") {
			out[f.name] = v.text.trim();
		} else {
			out[f.name] = v.selected[0] ?? "";
		}
	}
	return out;
}
