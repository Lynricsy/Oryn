// 设计探针的规则（plan-m29 C8）：在页面渲染完成后量一遍 DOM，找出可计数的版面与审美问题。
//
// 前 6 条是版面规则，总是运行；后 8 条是审美规则，只在启动方配置了 aesthetic（画布选定了设计方向）时运行，
// surface 为 app 时再关掉 eyebrow-excess 与 split-repeat。每条规则最多报 MAX_PER_RULE 处。
// 规则思路参考 Vetta（版面类）与 HyperSkills frontend-design（审美类阈值），按判据独立实现。
// 页面运行在不可信的沙箱里，这里不 import 任何应用代码；所有 DOM 访问都经传入的 window，测试可以换成 jsdom。

/** 每条规则最多报告的处数。 */
export const MAX_PER_RULE = 3;

/** 一处问题：loc 取最近祖先（含自身）的 data-oryn-loc，没有时为 null；text 是相关可见文本（截短）。 */
export interface Issue {
	rule: string;
	loc: string | null;
	text: string;
	detail?: number | string;
}

/** 启动方注入的配置（window.__orynProbe）。 */
export interface Options {
	aesthetic: boolean;
	surface: string;
}

/** 源码位置属性：设计引擎的 babel 插件给每个 JSX 起始标签打上 `src/frames/x.tsx:行:列`。 */
export const LOC_ATTR = "data-oryn-loc";

const TEXT_LIMIT = 60;

/** 不参与测量的元素（不渲染或不是页面内容）。 */
const SKIP_TAGS: Record<string, true> = {
	SCRIPT: true,
	STYLE: true,
	LINK: true,
	META: true,
	TEMPLATE: true,
	NOSCRIPT: true,
	TITLE: true,
	HEAD: true,
	BASE: true,
};

/** 0..1 的 sRGB（gamma 编码）分量与不透明度。 */
export type RGBA = [r: number, g: number, b: number, a: number];

interface Hit {
	el: Element;
	detail?: number | string;
}

interface Rule {
	name: string;
	aesthetic: boolean;
	/** 为真时 surface 为 app 不运行。 */
	landingOnly?: boolean;
	run(ctx: Ctx): Hit[];
}

/** 一次测量的上下文：缓存计算样式与盒子，候选元素按文档顺序。 */
class Ctx {
	readonly win: Window;
	readonly doc: Document;
	readonly elements: Element[];
	private styles = new Map<Element, CSSStyleDeclaration>();
	private rects = new Map<Element, DOMRect>();
	private shown = new Map<Element, boolean>();

	constructor(win: Window) {
		this.win = win;
		this.doc = win.document;
		this.elements = [];
		const body = this.doc.body;
		if (!body) return;
		for (const el of body.querySelectorAll("*")) {
			if (SKIP_TAGS[el.tagName.toUpperCase()]) continue;
			// svg 内部的图形只看外层 svg
			if (el.parentElement?.closest("svg")) continue;
			if (el.id === "oryn-probe") continue;
			this.elements.push(el);
		}
	}

	style(el: Element): CSSStyleDeclaration {
		let s = this.styles.get(el);
		if (!s) {
			s = this.win.getComputedStyle(el);
			this.styles.set(el, s);
		}
		return s;
	}

	rect(el: Element): DOMRect {
		let r = this.rects.get(el);
		if (!r) {
			r = el.getBoundingClientRect();
			this.rects.set(el, r);
		}
		return r;
	}

	/** 自己与祖先都不是 display:none，自己不是 visibility:hidden，也没有完全透明。 */
	rendered(el: Element): boolean {
		const cached = this.shown.get(el);
		if (cached !== undefined) return cached;
		const s = this.style(el);
		let ok = s.display !== "none";
		if (ok && el.parentElement && el.parentElement !== this.doc.documentElement)
			ok = this.renderedTree(el.parentElement);
		if (ok) ok = s.visibility !== "hidden" && s.opacity !== "0";
		this.shown.set(el, ok);
		return ok;
	}

	/** 只看 display 与透明度（visibility 可被子元素覆盖）。 */
	private renderedTree(el: Element): boolean {
		for (let n: Element | null = el; n; n = n.parentElement) {
			const s = this.style(n);
			if (s.display === "none" || s.opacity === "0") return false;
		}
		return true;
	}

	/** 已渲染且盒子宽高都大于 1px（排除 sr-only 一类的视觉隐藏）。 */
	visible(el: Element): boolean {
		if (!this.rendered(el)) return false;
		const r = this.rect(el);
		return r.width > 1 && r.height > 1;
	}

	/** 页面面积（文档滚动尺寸与视口取大）。 */
	pageArea(): number {
		const de = this.doc.documentElement;
		const w = Math.max(de.scrollWidth, this.win.innerWidth || 0);
		const h = Math.max(de.scrollHeight, this.win.innerHeight || 0);
		return Math.max(1, w * h);
	}
}

/** 直接子文本节点拼起来的文本（去掉首尾空白）。 */
function ownText(el: Element): string {
	let t = "";
	for (const n of el.childNodes) if (n.nodeType === 3) t += n.nodeValue ?? "";
	return t.trim();
}

function collapse(s: string): string {
	return s.replace(/\s+/g, " ").trim();
}

/** 元素的可见文本，按码点截到 TEXT_LIMIT。 */
export function textOf(el: Element): string {
	const t = Array.from(collapse(el.textContent ?? ""));
	return t.length > TEXT_LIMIT
		? `${t.slice(0, TEXT_LIMIT - 1).join("")}…`
		: t.join("");
}

/** 最近祖先（含自身）的 data-oryn-loc。 */
export function locOf(el: Element): string | null {
	return el.closest(`[${LOC_ATTR}]`)?.getAttribute(LOC_ATTR) ?? null;
}

function px(v: string): number {
	const n = Number.parseFloat(v);
	return Number.isFinite(n) ? n : 0;
}

// ---------- 颜色 ----------

type Mat = readonly [
	readonly [number, number, number],
	readonly [number, number, number],
	readonly [number, number, number],
];

function mul(m: Mat, v: readonly [number, number, number]) {
	return [
		m[0][0] * v[0] + m[0][1] * v[1] + m[0][2] * v[2],
		m[1][0] * v[0] + m[1][1] * v[1] + m[1][2] * v[2],
		m[2][0] * v[0] + m[2][1] * v[1] + m[2][2] * v[2],
	] as const;
}

const XYZ65_TO_LSRGB: Mat = [
	[3.2409699419045226, -1.537383177570094, -0.4986107602930034],
	[-0.9692436362808796, 1.8759675015077202, 0.04155505740717559],
	[0.05563007969699366, -0.20397695888897652, 1.0569715142428786],
];
const D50_TO_D65: Mat = [
	[0.955473421488075, -0.02309845494876471, 0.06325924320057072],
	[-0.0283697093338637, 1.0099953980813041, 0.021041441191917323],
	[0.012314014864481998, -0.020507649298898964, 1.330365926242124],
];
const LP3_TO_XYZ65: Mat = [
	[0.4865709486482162, 0.26566769316909306, 0.1982172852343625],
	[0.2289745640697488, 0.6917385218365064, 0.079286914093745],
	[0, 0.04511338185890264, 1.043944368900976],
];

function toLinear(c: number): number {
	const a = Math.abs(c);
	return a <= 0.04045 ? c / 12.92 : Math.sign(c) * ((a + 0.055) / 1.055) ** 2.4;
}

function toGamma(c: number): number {
	const a = Math.abs(c);
	return a <= 0.0031308
		? c * 12.92
		: Math.sign(c) * (1.055 * a ** (1 / 2.4) - 0.055);
}

function clamp01(n: number): number {
	return Math.min(1, Math.max(0, n));
}

function fromLinear(l: readonly [number, number, number], a: number): RGBA {
	return [
		clamp01(toGamma(l[0])),
		clamp01(toGamma(l[1])),
		clamp01(toGamma(l[2])),
		clamp01(a),
	];
}

function oklabToRGBA(L: number, A: number, B: number, alpha: number): RGBA {
	const l = (L + 0.3963377774 * A + 0.2158037573 * B) ** 3;
	const m = (L - 0.1055613458 * A - 0.0638541728 * B) ** 3;
	const s = (L - 0.0894841775 * A - 1.291485548 * B) ** 3;
	return fromLinear(
		[
			4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
			-1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
			-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
		],
		alpha,
	);
}

function labToRGBA(L: number, A: number, B: number, alpha: number): RGBA {
	const e = 216 / 24389;
	const k = 24389 / 27;
	const fy = (L + 16) / 116;
	const fx = fy + A / 500;
	const fz = fy - B / 200;
	const xr = fx ** 3 > e ? fx ** 3 : (116 * fx - 16) / k;
	const yr = L > k * e ? fy ** 3 : L / k;
	const zr = fz ** 3 > e ? fz ** 3 : (116 * fz - 16) / k;
	const d65 = mul(D50_TO_D65, [
		xr * 0.9642956764295677,
		yr,
		zr * 0.8251046025104602,
	]);
	return fromLinear(mul(XYZ65_TO_LSRGB, d65), alpha);
}

/** 解析一个分量：百分比按 scale 换算，`none` 当 0。 */
function component(s: string, scale: number): number {
	if (s === "none") return 0;
	if (s.endsWith("%")) return (Number.parseFloat(s) / 100) * scale;
	if (s.endsWith("deg")) return Number.parseFloat(s);
	if (s.endsWith("turn")) return Number.parseFloat(s) * 360;
	if (s.endsWith("rad")) return (Number.parseFloat(s) * 180) / Math.PI;
	return Number.parseFloat(s);
}

/**
 * 解析计算样式里的颜色：rgb/rgba、#hex、transparent、oklab/oklch、lab/lch、color(srgb|srgb-linear|display-p3)。
 * 认不出的格式返回 null（调用方跳过，不误报）。
 */
export function parseColor(raw: string): RGBA | null {
	const s = raw.trim().toLowerCase();
	if (s === "transparent") return [0, 0, 0, 0];
	const hex = /^#([0-9a-f]{3,8})$/.exec(s)?.[1];
	if (hex) {
		if (
			hex.length !== 3 &&
			hex.length !== 4 &&
			hex.length !== 6 &&
			hex.length !== 8
		)
			return null;
		const short = hex.length <= 4;
		const at = (i: number) =>
			short
				? Number.parseInt(hex.charAt(i) + hex.charAt(i), 16) / 255
				: Number.parseInt(hex.slice(i * 2, i * 2 + 2), 16) / 255;
		const n = short ? hex.length : hex.length / 2;
		return [at(0), at(1), at(2), n === 4 ? at(3) : 1];
	}
	const fn = /^([a-z-]+)\((.*)\)$/.exec(s);
	if (!fn) return null;
	const name = fn[1] ?? "";
	const [body = "", alphaPart] = (fn[2] ?? "").split("/");
	const parts = body.split(/[\s,]+/).filter(Boolean);
	let alpha = 1;
	if (alphaPart !== undefined) alpha = component(alphaPart.trim(), 1);
	else if (parts.length === 4 && (name === "rgba" || name === "rgb"))
		alpha = component(parts[3] ?? "1", 1);
	if (!Number.isFinite(alpha)) return null;
	const num = (i: number, scale: number) => component(parts[i] ?? "", scale);
	let out: RGBA | null = null;
	switch (name) {
		case "rgb":
		case "rgba":
			if (parts.length < 3) return null;
			out = [
				clamp01(num(0, 255) / 255),
				clamp01(num(1, 255) / 255),
				clamp01(num(2, 255) / 255),
				clamp01(alpha),
			];
			break;
		case "oklab":
			out = oklabToRGBA(num(0, 1), num(1, 0.4), num(2, 0.4), alpha);
			break;
		case "oklch": {
			const h = (num(2, 360) * Math.PI) / 180;
			const c = num(1, 0.4);
			out = oklabToRGBA(num(0, 1), c * Math.cos(h), c * Math.sin(h), alpha);
			break;
		}
		case "lab":
			out = labToRGBA(num(0, 100), num(1, 125), num(2, 125), alpha);
			break;
		case "lch": {
			const h = (num(2, 360) * Math.PI) / 180;
			const c = num(1, 150);
			out = labToRGBA(num(0, 100), c * Math.cos(h), c * Math.sin(h), alpha);
			break;
		}
		case "color": {
			const space = parts[0];
			const v = [num(1, 1), num(2, 1), num(3, 1)] as const;
			if (space === "srgb")
				out = [clamp01(v[0]), clamp01(v[1]), clamp01(v[2]), clamp01(alpha)];
			else if (space === "srgb-linear") out = fromLinear(v, alpha);
			else if (space === "display-p3")
				out = fromLinear(
					mul(
						XYZ65_TO_LSRGB,
						mul(LP3_TO_XYZ65, [toLinear(v[0]), toLinear(v[1]), toLinear(v[2])]),
					),
					alpha,
				);
			break;
		}
	}
	if (!out || out.some((n) => !Number.isFinite(n))) return null;
	return out;
}

/** fg 叠在不透明的 bg 上（sRGB 空间里混合，同浏览器）。 */
function over(fg: RGBA, bg: RGBA): RGBA {
	const a = fg[3];
	return [
		fg[0] * a + bg[0] * (1 - a),
		fg[1] * a + bg[1] * (1 - a),
		fg[2] * a + bg[2] * (1 - a),
		1,
	];
}

/** WCAG 2.x 相对亮度。 */
export function luminance(c: RGBA): number {
	const lin = (v: number) =>
		v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
	return 0.2126 * lin(c[0]) + 0.7152 * lin(c[1]) + 0.0722 * lin(c[2]);
}

/** WCAG 对比度（1–21）。 */
export function contrast(a: RGBA, b: RGBA): number {
	const la = luminance(a);
	const lb = luminance(b);
	return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

/** sRGB → HSL：h 0–360，s、l 0–1。 */
export function hsl(c: RGBA): [h: number, s: number, l: number] {
	const [r, g, b] = c;
	const max = Math.max(r, g, b);
	const min = Math.min(r, g, b);
	const l = (max + min) / 2;
	const d = max - min;
	if (d === 0) return [0, 0, l];
	const s = d / (1 - Math.abs(2 * l - 1));
	let h: number;
	if (max === r) h = ((g - b) / d) % 6;
	else if (max === g) h = (b - r) / d + 2;
	else h = (r - g) / d + 4;
	h *= 60;
	if (h < 0) h += 360;
	return [h, s, l];
}

/** 文本背后的不透明背景色：自下而上叠半透明背景，直到遇到不透明的；中途有背景图或取不到不透明色时返回 null。 */
function backdrop(ctx: Ctx, el: Element): RGBA | null {
	const layers: RGBA[] = [];
	for (let n: Element | null = el; n; n = n.parentElement) {
		const s = ctx.style(n);
		if (s.backgroundImage && s.backgroundImage !== "none") return null;
		const c = parseColor(s.backgroundColor);
		if (!c) return null;
		if (c[3] >= 1) {
			let out: RGBA = c;
			for (let i = layers.length - 1; i >= 0; i--) {
				const layer = layers[i];
				if (layer) out = over(layer, out);
			}
			return out;
		}
		if (c[3] > 0) layers.push(c);
	}
	return null;
}

// ---------- 规则 ----------

const CLICKABLE =
	'a[href], button, input:not([type="hidden"]), select, textarea, summary, [role="button"], [role="link"], [role="checkbox"], [role="radio"], [role="switch"], [role="tab"], [role="menuitem"], [onclick], [tabindex]:not([tabindex="-1"])';

const COMPACT =
	'button, [role="button"], label, nav a, nav li, [role="tab"], [role="menuitem"]';

const MEDIA = "img, picture, video, canvas, svg, iframe";

/** 元素文本占的行数（按 Range.getClientRects 的行顶分组）；量不了时返回 1。 */
function lineCount(ctx: Ctx, el: Element): number {
	const range = ctx.doc.createRange();
	if (typeof range.getClientRects !== "function") return 1;
	const tops: { top: number; height: number }[] = [];
	const walker = ctx.doc.createTreeWalker(el, 4 /* SHOW_TEXT */);
	for (let n = walker.nextNode(); n; n = walker.nextNode()) {
		if (!(n.nodeValue ?? "").trim()) continue;
		range.selectNodeContents(n);
		for (const r of Array.from(range.getClientRects()))
			if (r.width > 0 && r.height > 0)
				tops.push({ top: r.top, height: r.height });
	}
	tops.sort((a, b) => a.top - b.top);
	let lines = 0;
	let last = Number.NEGATIVE_INFINITY;
	for (const t of tops) {
		if (t.top >= last + Math.max(2, t.height / 2)) {
			lines++;
			last = t.top;
		}
	}
	return Math.max(lines, 1);
}

const textClipped: Rule = {
	name: "text-clipped",
	aesthetic: false,
	run(ctx) {
		const hits: Hit[] = [];
		const de = ctx.doc.documentElement;
		for (const el of ctx.elements) {
			if (el === de || el === ctx.doc.body) continue;
			const s = ctx.style(el);
			const clipX = s.overflowX === "hidden" || s.overflowX === "clip";
			const clipY = s.overflowY === "hidden" || s.overflowY === "clip";
			if (!clipX && !clipY) continue;
			if (el.clientWidth <= 1 || el.clientHeight <= 1) continue;
			const dx = clipX ? el.scrollWidth - el.clientWidth : 0;
			const dy = clipY ? el.scrollHeight - el.clientHeight : 0;
			if (dx <= 1 && dy <= 1) continue;
			if (!collapse(el.textContent ?? "") || !ctx.rendered(el)) continue;
			hits.push({ el, detail: Math.round(Math.max(dx, dy)) });
		}
		return hits;
	},
};

const unintendedWrap: Rule = {
	name: "unintended-wrap",
	aesthetic: false,
	run(ctx) {
		const hits: Hit[] = [];
		for (const el of ctx.doc.querySelectorAll(COMPACT)) {
			// 写了 <br> 的是有意换行；嵌套命中（nav li > a）只看最里层
			if (el.querySelector("br") || el.querySelector(COMPACT)) continue;
			if (!collapse(el.textContent ?? "") || !ctx.visible(el)) continue;
			const n = lineCount(ctx, el);
			if (n > 1) hits.push({ el, detail: n });
		}
		return hits;
	},
};

const iconMissing: Rule = {
	name: "icon-missing",
	aesthetic: false,
	run(ctx) {
		const hits: Hit[] = [];
		for (const el of ctx.elements) {
			const isIcon = (el.getAttribute("class") ?? "").includes("icon-[");
			const isSvg = el.tagName.toLowerCase() === "svg";
			if (!isIcon && !isSvg) continue;
			if (!ctx.rendered(el)) continue;
			if (
				isSvg &&
				!Array.from(el.children).some(
					(c) =>
						!["defs", "symbol", "title", "desc", "style"].includes(
							c.tagName.toLowerCase(),
						),
				)
			)
				continue;
			const r = ctx.rect(el);
			if (r.width === 0 || r.height === 0) {
				hits.push({ el, detail: "box" });
				continue;
			}
			if (isIcon) {
				const s = ctx.style(el);
				const mask =
					s.getPropertyValue("mask-image") ||
					s.getPropertyValue("-webkit-mask-image") ||
					"none";
				const bg = s.backgroundImage || "none";
				if (mask === "none" && bg === "none") hits.push({ el, detail: "mask" });
			}
		}
		return hits;
	},
};

const BLOCKISH: Record<string, true> = {
	block: true,
	flex: true,
	grid: true,
	table: true,
	"list-item": true,
	"flow-root": true,
};

const edgeMisaligned: Rule = {
	name: "edge-misaligned",
	aesthetic: false,
	run(ctx) {
		const hits: Hit[] = [];
		const parents = new Set<Element>();
		for (const el of ctx.elements)
			if (el.parentElement) parents.add(el.parentElement);
		for (const p of parents) {
			const kids: { el: Element; left: number }[] = [];
			for (const k of p.children) {
				if (SKIP_TAGS[k.tagName.toUpperCase()]) continue;
				const s = ctx.style(k);
				if (!BLOCKISH[s.display]) continue;
				if (s.position === "absolute" || s.position === "fixed") continue;
				if (!ctx.visible(k)) continue;
				kids.push({ el: k, left: ctx.rect(k).left });
			}
			if (kids.length < 2) continue;
			// 以出现最多的左缘为基准，差 1–3 px 的报出来（每个父容器一处）
			const freq = new Map<number, number>();
			for (const k of kids) {
				const key = Math.round(k.left * 2) / 2;
				freq.set(key, (freq.get(key) ?? 0) + 1);
			}
			let base = kids[0]?.left ?? 0;
			let best = 0;
			for (const [left, n] of freq)
				if (n > best) {
					best = n;
					base = left;
				}
			for (const k of kids) {
				const d = Math.abs(k.left - base);
				if (d >= 1 && d <= 3) {
					hits.push({ el: k.el, detail: Math.round(d * 10) / 10 });
					break;
				}
			}
		}
		return hits;
	},
};

/** 样式表里的规则（递归进 @media、@layer、@supports 等分组规则）；跨源读不到的表跳过。 */
function styleRules(doc: Document): CSSStyleRule[] {
	const out: CSSStyleRule[] = [];
	const walk = (rules: CSSRuleList) => {
		for (const r of Array.from(rules)) {
			if ("selectorText" in r && "style" in r) out.push(r as CSSStyleRule);
			if ("cssRules" in r && (r as CSSGroupingRule).cssRules)
				walk((r as CSSGroupingRule).cssRules);
		}
	};
	for (const sheet of Array.from(doc.styleSheets)) {
		try {
			walk(sheet.cssRules);
		} catch {
			// 跨源样式表
		}
	}
	return out;
}

/** 声明值里没有回退值的 var(--x) 引用。 */
function bareVars(value: string): string[] {
	const out: string[] = [];
	const re = /var\(\s*(--[\w-]+)\s*([,)])/g;
	for (let m = re.exec(value); m; m = re.exec(value))
		if (m[2] === ")" && m[1]) out.push(m[1]);
	return out;
}

const tokenUndefined: Rule = {
	name: "token-undefined",
	aesthetic: false,
	run(ctx) {
		const hits: Hit[] = [];
		const seen = new Set<Element>();
		const test = (el: Element, prop: string, name: string) => {
			if (seen.has(el) || name.startsWith("--tw-")) return;
			if (ctx.style(el).getPropertyValue(name).trim() !== "") return;
			if (!ctx.rendered(el)) return;
			seen.add(el);
			hits.push({ el, detail: `${prop}: ${name}` });
		};
		const decls = (style: CSSStyleDeclaration) => {
			const out: [string, string][] = [];
			for (let i = 0; i < style.length; i++) {
				const prop = style.item(i);
				if (prop.startsWith("--")) continue;
				const names = bareVars(style.getPropertyValue(prop));
				for (const n of names) out.push([prop, n]);
			}
			return out;
		};
		for (const rule of styleRules(ctx.doc)) {
			if (hits.length >= MAX_PER_RULE) break;
			const refs = decls(rule.style);
			if (!refs.length || rule.selectorText.includes("::")) continue;
			let els: NodeListOf<Element>;
			try {
				els = ctx.doc.querySelectorAll(rule.selectorText);
			} catch {
				continue;
			}
			for (const el of els)
				if (el.closest("body")) for (const [prop, n] of refs) test(el, prop, n);
		}
		for (const el of ctx.elements) {
			const style = (el as HTMLElement).style;
			if (style) for (const [prop, n] of decls(style)) test(el, prop, n);
		}
		return hits;
	},
};

const contrastLow: Rule = {
	name: "contrast-low",
	aesthetic: false,
	run(ctx) {
		const hits: Hit[] = [];
		for (const el of ctx.elements) {
			if (!ownText(el) || !ctx.visible(el)) continue;
			const s = ctx.style(el);
			const fg = parseColor(s.color);
			if (!fg || fg[3] === 0) continue;
			const bg = backdrop(ctx, el);
			if (!bg) continue;
			const ratio = contrast(over(fg, bg), bg);
			const size = px(s.fontSize);
			const bold =
				Number.parseInt(s.fontWeight, 10) >= 700 || s.fontWeight === "bold";
			const min = size >= 24 || (bold && size >= 18.66) ? 3 : 4.5;
			if (ratio < min) hits.push({ el, detail: Math.round(ratio * 100) / 100 });
		}
		return hits;
	},
};

const targetSmall: Rule = {
	name: "target-small",
	aesthetic: true,
	run(ctx) {
		const hits: Hit[] = [];
		for (const el of ctx.doc.querySelectorAll(CLICKABLE)) {
			if (!el.closest("body") || el.hasAttribute("disabled")) continue;
			if (!ctx.visible(el)) continue;
			const s = ctx.style(el);
			// 句中的行内链接是 SC 2.5.8 的例外
			if (
				el.tagName === "A" &&
				s.display === "inline" &&
				el.parentElement &&
				ownText(el.parentElement)
			)
				continue;
			const type = el.getAttribute("type");
			if (
				el.tagName === "INPUT" &&
				(type === "checkbox" || type === "radio") &&
				(el.closest("label") ||
					Array.from(ctx.doc.querySelectorAll("label")).some(
						(l) => l.htmlFor !== "" && l.htmlFor === el.id,
					))
			)
				continue;
			const r = ctx.rect(el);
			if (r.width < 24 || r.height < 24)
				hits.push({
					el,
					detail: `${Math.round(r.width)}x${Math.round(r.height)}`,
				});
		}
		return hits;
	},
};

const dashInCopy: Rule = {
	name: "dash-in-copy",
	aesthetic: true,
	run(ctx) {
		const hits: Hit[] = [];
		for (const el of ctx.elements) {
			if (el.closest("code, pre")) continue;
			const t = ownText(el);
			if (!/[\u2013\u2014]/.test(t) || !ctx.rendered(el)) continue;
			hits.push({ el, detail: t.includes("\u2014") ? "U+2014" : "U+2013" });
		}
		return hits;
	},
};

/** 大写、字距 > 0.05em、字号 ≤ 13px 的小标签。 */
function isEyebrow(ctx: Ctx, el: Element): boolean {
	const t = ownText(el);
	if (!t) return false;
	const s = ctx.style(el);
	const size = px(s.fontSize);
	if (size <= 0 || size > 13) return false;
	const upper =
		s.textTransform === "uppercase" ||
		(t.toUpperCase() === t && t.toLowerCase() !== t);
	if (!upper) return false;
	const ls = s.letterSpacing === "normal" ? 0 : px(s.letterSpacing);
	return ls / size > 0.05;
}

const eyebrowExcess: Rule = {
	name: "eyebrow-excess",
	aesthetic: true,
	landingOnly: true,
	run(ctx) {
		const eyebrows = ctx.elements.filter(
			(el) => isEyebrow(ctx, el) && ctx.rendered(el),
		);
		const sections = Math.max(1, ctx.doc.querySelectorAll("section").length);
		const limit = Math.ceil(sections / 3);
		if (eyebrows.length <= limit) return [];
		return eyebrows
			.slice(limit)
			.map((el) => ({ el, detail: `${eyebrows.length}/${limit}` }));
	},
};

const accentMultiple: Rule = {
	name: "accent-multiple",
	aesthetic: true,
	run(ctx) {
		const accents: { hue: number; el: Element }[] = [];
		const add = (el: Element, raw: string) => {
			const c = parseColor(raw);
			if (!c || c[3] < 0.5) return;
			const [h, s, l] = hsl(c);
			if (s > 0.5 && l >= 0.15 && l <= 0.85) accents.push({ hue: h, el });
		};
		for (const el of ctx.elements) {
			if (!ctx.visible(el)) continue;
			const s = ctx.style(el);
			add(el, s.backgroundColor);
			if (ownText(el)) add(el, s.color);
			if (px(s.borderTopWidth) > 0) add(el, s.borderTopColor);
		}
		if (accents.length < 2) return [];
		accents.sort((a, b) => a.hue - b.hue);
		// 30° 一簇：排序后相邻色相差超过 30° 开新簇；首尾跨 0° 相接时合并
		const starts: number[] = [0];
		for (let i = 1; i < accents.length; i++)
			if ((accents[i]?.hue ?? 0) - (accents[i - 1]?.hue ?? 0) > 30)
				starts.push(i);
		let clusters = starts.length;
		const first = accents[0]?.hue ?? 0;
		const last = accents[accents.length - 1]?.hue ?? 0;
		if (clusters > 1 && first + 360 - last <= 30) clusters--;
		if (clusters <= 1) return [];
		const second = accents[starts[1] ?? 0];
		return second ? [{ el: second.el, detail: clusters }] : [];
	},
};

const radiusSingle: Rule = {
	name: "radius-single",
	aesthetic: true,
	run(ctx) {
		const values = new Set<string>();
		let first: Element | null = null;
		let n = 0;
		for (const el of ctx.elements) {
			if (!ctx.visible(el)) continue;
			const s = ctx.style(el);
			const corner = [
				s.borderTopLeftRadius,
				s.borderTopRightRadius,
				s.borderBottomRightRadius,
				s.borderBottomLeftRadius,
			].find((v) => v && px(v) !== 0);
			if (!corner) continue;
			values.add(corner);
			first ??= el;
			n++;
		}
		if (n < 12 || values.size !== 1 || !first) return [];
		return [{ el: first, detail: [...values][0] }];
	},
};

function isPure(c: RGBA | null): "#000" | "#fff" | null {
	if (!c || c[3] < 1) return null;
	if (c[0] === 0 && c[1] === 0 && c[2] === 0) return "#000";
	if (c[0] === 1 && c[1] === 1 && c[2] === 1) return "#fff";
	return null;
}

const pureBlackWhite: Rule = {
	name: "pure-black-white",
	aesthetic: true,
	run(ctx) {
		const hits: Hit[] = [];
		const area = ctx.pageArea();
		const bgs = [ctx.doc.documentElement, ...ctx.elements];
		const flagged = new Set<Element>();
		for (const el of bgs) {
			const pure = isPure(parseColor(ctx.style(el).backgroundColor));
			if (!pure || !ctx.visible(el)) continue;
			// 外层已报过同色的不重复报
			let dup = false;
			for (let p = el.parentElement; p; p = p.parentElement)
				if (
					flagged.has(p) &&
					isPure(parseColor(ctx.style(p).backgroundColor)) === pure
				)
					dup = true;
			if (dup) continue;
			const r = ctx.rect(el);
			if ((r.width * r.height) / area > 0.05) {
				flagged.add(el);
				hits.push({ el, detail: `background ${pure}` });
			}
		}
		const text = new Map<string, { el: Element; area: number }>();
		for (const el of ctx.elements) {
			if (!ownText(el) || !ctx.visible(el)) continue;
			const pure = isPure(parseColor(ctx.style(el).color));
			if (!pure) continue;
			const r = ctx.rect(el);
			const t = text.get(pure);
			if (t) t.area += r.width * r.height;
			else text.set(pure, { el, area: r.width * r.height });
		}
		for (const [pure, t] of text)
			if (t.area / area > 0.05) hits.push({ el: t.el, detail: `text ${pure}` });
		return hits;
	},
};

/** section 是不是左右图文分栏：自身或单子元素链（至多 4 层）上有两个并排、各占 ≥25% 宽的子块，一边是媒体、另一边是文字。 */
function isSplit(ctx: Ctx, section: Element): boolean {
	const width = ctx.rect(section).width;
	if (width <= 0) return false;
	let node: Element | undefined = section;
	for (let depth = 0; node && depth < 4; depth++) {
		const kids: Element[] = Array.from(node.children).filter(
			(k) => !SKIP_TAGS[k.tagName.toUpperCase()] && ctx.visible(k),
		);
		if (kids.length === 2) {
			const [a, b] = kids as [Element, Element];
			const ra = ctx.rect(a);
			const rb = ctx.rect(b);
			const sideBySide =
				ra.width >= width * 0.25 &&
				rb.width >= width * 0.25 &&
				Math.abs(ra.left - rb.left) >= Math.min(ra.width, rb.width) * 0.5 &&
				ra.top < rb.bottom &&
				rb.top < ra.bottom;
			if (sideBySide) {
				const media = (el: Element) =>
					el.matches(MEDIA) ||
					el.querySelector(MEDIA) !== null ||
					/url\(/.test(ctx.style(el).backgroundImage);
				// 文字一侧至少 12 个字符（中文信息密度高，不按英文的长度算）
				const words = (el: Element) =>
					collapse(el.textContent ?? "").length >= 12;
				return (
					(media(a) && words(b) && !media(b)) ||
					(media(b) && words(a) && !media(a))
				);
			}
		}
		node = kids.length === 1 ? kids[0] : undefined;
	}
	return false;
}

const splitRepeat: Rule = {
	name: "split-repeat",
	aesthetic: true,
	landingOnly: true,
	run(ctx) {
		const hits: Hit[] = [];
		const sections = Array.from(ctx.doc.querySelectorAll("section")).filter(
			(s) => !s.parentElement?.closest("section") && ctx.visible(s),
		);
		let run = 0;
		for (const s of sections) {
			run = isSplit(ctx, s) ? run + 1 : 0;
			if (run === 3) hits.push({ el: s, detail: 3 });
		}
		return hits;
	},
};

/** 颜色字面量：#hex 或颜色函数。 */
const COLOR_LITERAL =
	/(^|[^\w-])(#[0-9a-f]{3,8}\b|(rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\()/i;
const COLOR_KEYWORDS: Record<string, true> = {
	transparent: true,
	currentcolor: true,
	inherit: true,
	initial: true,
	unset: true,
	revert: true,
	"revert-layer": true,
	none: true,
};

const rawColor: Rule = {
	name: "raw-color",
	aesthetic: true,
	run(ctx) {
		const hits: Hit[] = [];
		for (const el of ctx.elements) {
			const style = (el as HTMLElement).style;
			if (!style?.length) continue;
			for (let i = 0; i < style.length; i++) {
				const prop = style.item(i);
				if (prop.startsWith("--")) continue;
				const v = style.getPropertyValue(prop).trim();
				if (!v || v.includes("var(")) continue;
				const named =
					prop.endsWith("color") &&
					/^[a-z]+$/i.test(v) &&
					!COLOR_KEYWORDS[v.toLowerCase()];
				if (COLOR_LITERAL.test(v) || named) {
					hits.push({ el, detail: `${prop}: ${v}` });
					break;
				}
			}
		}
		return hits;
	},
};

/** 全部规则，顺序即 C8 表的顺序：前 6 条版面类，后 8 条审美类。 */
export const RULES: readonly Rule[] = [
	textClipped,
	unintendedWrap,
	iconMissing,
	edgeMisaligned,
	tokenUndefined,
	contrastLow,
	targetSmall,
	dashInCopy,
	eyebrowExcess,
	accentMultiple,
	radiusSingle,
	pureBlackWhite,
	splitRepeat,
	rawColor,
];

/** 按配置跑规则；单条规则抛错时跳过它，不影响其余规则。 */
export function check(win: Window, opts: Options): Issue[] {
	const ctx = new Ctx(win);
	const out: Issue[] = [];
	for (const rule of RULES) {
		if (rule.aesthetic && !opts.aesthetic) continue;
		if (rule.landingOnly && opts.surface === "app") continue;
		let hits: Hit[];
		try {
			hits = rule.run(ctx);
		} catch {
			continue;
		}
		for (const h of hits.slice(0, MAX_PER_RULE)) {
			const issue: Issue = {
				rule: rule.name,
				loc: locOf(h.el),
				text: textOf(h.el),
			};
			if (h.detail !== undefined) issue.detail = h.detail;
			out.push(issue);
		}
	}
	return out;
}
