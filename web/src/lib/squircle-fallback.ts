/**
 * 角形降级引擎：只在不支持 `corner-shape` 的引擎里由 lib/squircle.ts 动态加载（Firefox、WebKitGTK、旧版 Safari / WKWebView）。
 *
 * 三部分：
 * 1. 包络圆（CSS，JS 跑之前就生效）：squircle-patch.ts 把样式表里每个 border-*-radius 改写成「原值 × --sq-s」，
 *    超椭圆表面的 --sq-s 是注册的初始值 ROUND_SCALE（正圆与胶囊由 index.css「角形」段落设为 1）：原生圆角缩成恰好包住超椭圆的圆（最大偏差约 1.4% 半径）。
 *    背景、投影、焦点环、backdrop-filter 由浏览器照常绘制、照常过渡，跟着这个圆走。
 * 2. 外轮廓（clip-path）：对每个超椭圆盒子（元素本身及其 ::before / ::after）算出 clip-path，只挖掉圆与超椭圆之间
 *    那条薄片，背景与内容的外轮廓就是精确的超椭圆。裁剪区向外扩到投影与焦点环够不着的地方（overflow 可见的元素扩 BLEED，
 *    容纳溢出的子元素），盒子外的投影、焦点环、溢出内容不受影响；自带 backdrop-filter 的盒子紧贴自身剪（外扩会扩大模糊范围）。
 * 3. 边框（border-image）：裁剪只能删像素、挪不动边框，所以实色边框改由 SVG 环带绘制：外缘超椭圆、内缘是规范给出的
 *    同心超椭圆（半径减去边框宽度），颜色取计算值，悬停、聚焦、切换主题后重读。原生边框随之不画。
 *
 * 半径之和超过边长时（CSS 等比缩小规则），把 --sq-s 改成 ROUND_SCALE × 缩小系数。
 * 不剪（只有 1、3 两部分，停在包络圆，偏差 ≤ 1.4% 半径）的情况：clip-path 会新建层叠上下文并成为 backdrop root，所以
 * 背景透明、却包着 backdrop-filter 后代的元素（玻璃宿主，其模糊层在 ::before 上、照常被剪）、包着 fixed / 带 z-index 的后代
 * 且自身不是层叠上下文的元素、backdrop-filter 与 mask-image 同在的盒子（线程页头的渐进模糊纱，被剪后在 Chromium 里模糊整块铺满）、
 * 自带 backdrop-filter 又有投影或焦点环的盒子、作者自己写了 clip-path 的元素、半径正在过渡的盒子。
 *
 * 性能：
 * - 裁剪与环带写成构造样式表里的共享规则（squircle-rules.ts），盒子只挂一个属性，同一组声明只解析一次。
 * - 环带是九宫格，与盒子尺寸无关；裁剪默认是按尺寸写死的 path()（样式重算时最便宜），尺寸连续变化过的盒子
 *   （Track.fluid：侧栏开合时跟着变宽的卡片、流式增高的消息）改用与尺寸无关的 polygon()，之后变尺寸不必重写。
 * - --sq-s 注册成不继承、初始值为 ROUND_SCALE 的自定义属性，不用通配规则给每个元素都设一遍。
 * - 仍与尺寸有关的盒子（path() 裁剪、半径被等比缩小、贴边装不下九宫格的环带）在尺寸连续变化期间退回包络圆，
 *   稳定 SETTLE_MS 后再剪；边框色 / 宽度过渡与关键帧动画期间不画环带（原生边框照常插值）。
 * - 切换主题时马上按新颜色换环带（本帧样式已经算好，读颜色很便宜），其余属性留到之后按 FRAME_BUDGET_MS 的预算逐帧重读。
 */
import {
	type BorderWidths,
	type CornerRadius,
	type Radii,
	ROUND_SCALE,
	ringImage,
	sliverClipPath,
	usedRadii,
} from "./squircle-geometry";
import { patchDeclarations, patchSheet, SCALE_VAR } from "./squircle-patch";
import { type Pseudo, RULE_ATTR, ShapeRules } from "./squircle-rules";

const PSEUDOS: readonly Pseudo[] = ["", "::before", "::after"];

/**
 * clip-path 大矩形向外扩的距离：要盖住最远的投影（如抽屉 100vmax 的遮罩投影）与溢出的子元素。
 * 坐标不能太大：Chromium（Skia）对 ±32768 以外的路径坐标会出现裁剪裂缝，10000 足够覆盖 4K 屏的 100vmax。
 */
const BLEED = 10_000;
/** 根元素上的开关属性（index.css 里正圆与胶囊的 --sq-s = 1 挂在它下面）。 */
const ROOT_ATTR = "data-squircle";
/** 可能改变 :hover / :focus / :active 等状态的事件：之后重读受管盒子的边框色。 */
const STATE_EVENTS = [
	"pointerover",
	"pointerout",
	"pointerdown",
	"pointerup",
	"focusin",
	"focusout",
];
/**
 * 尺寸变化间隔小于这个值（ms）视为连续变化（侧栏与面板开合的宽度过渡、流式消息增高）。裁剪与九宫格环带与尺寸无关，
 * 照常保留；只有仍与尺寸有关的盒子（Box.sized）期间退回包络圆，最后一次变化后这么久再按最终尺寸剪。
 */
const SETTLE_MS = 150;
/**
 * 每帧用于「可以晚几帧」的重读（主题切换后全部盒子、祖先类名变化波及的后代）的时间预算（ms），超出的留到下一帧。
 * 晚处理期间盒子停在包络圆或保持原样，不会画错。
 */
const FRAME_BUDGET_MS = 3;
/** 上面的重读在动画进行中先不做，但最多等这么久（ms）：一直不停的边框动画也不能让它永远拖着。 */
const DEFER_MAX_MS = 1000;

/** 由 border-image 绘制的实色边框。 */
interface Border {
	widths: BorderWidths;
	color: string;
}

interface Box {
	/** 原半径（按 --sq-s 反推，未按边长等比缩小）。 */
	radii: Radii;
	/** 实色、同色的边框；null 表示没有或交给原生绘制（虚线、各边异色、作者自己的 border-image）。 */
	border: Border | null;
	/** 作者自己写了 clip-path：不剪。 */
	authorClip: boolean;
	/** 投影与焦点环伸出边框盒的最远距离（px），决定 clip-path 要外扩多少。 */
	reach: number;
	/** 自身有 backdrop-filter：clip-path 的范围就是模糊的范围，只能紧贴盒子剪。 */
	backdrop: boolean;
	/** 同时有 backdrop-filter 与 mask-image：不剪（见 Hazard.masked）。 */
	masked: boolean;
	/** 元素的 overflow 为 visible：后代可能画到盒子外，clip-path 要留足余量。 */
	overflowVisible: boolean;
	/** 已写入的 --sq-s、clip-path、border-image（"" 表示没写）。 */
	scale: string;
	clip: string;
	ring: string;
	/** 挂在宿主上、套用以上声明的共享规则属性（"" 表示没挂）。 */
	attr: string;
	/** 写入的声明与尺寸有关（半径被等比缩小、环带装不下九宫格）：尺寸变了要重写。 */
	sized: boolean;
}

interface Track {
	boxes: Map<Pseudo, Box>;
	/** 边框盒尺寸，来自 ResizeObserver；-1 表示还没收到。 */
	width: number;
	height: number;
	/**
	 * 自身已是层叠上下文（包着带 z-index 的后代也可以剪）、背景不透明（后代的 backdrop-filter 本来就只看得到它，剪了也不影响模糊）。
	 * 只有风险元素的祖先才用得到，按需读（updateSafety）；null 表示还没读，重读该元素时清回 null。
	 */
	stacking: boolean | null;
	opaque: boolean | null;
	unsafe: boolean;
	/** 半径正在过渡的盒子（不剪）。 */
	animating: Set<Pseudo>;
	/** 上一次尺寸变化的时刻（performance.now()）。 */
	lastResize: number;
	/** 尺寸在连续变化（动画、流式增长）：与尺寸有关的盒子先退回包络圆，稳定 SETTLE_MS 后再按最终尺寸剪。 */
	resizing: boolean;
	/**
	 * 尺寸连续变化过（侧栏开合时跟着变宽的卡片、流式增高的消息）：此后裁剪改用与尺寸无关的 polygon，变尺寸不必重写。
	 * 其余盒子用按尺寸写死的 path()（样式重算时便宜得多，见 sliverClipPath）。
	 */
	fluid: boolean;
	/** 各盒子正在进行的边框色 / 宽度过渡与关键帧动画数：大于 0 时不画环带，由原生边框跟着过渡。 */
	borderAnims: Map<Pseudo, number>;
}

/** measure 量出的一个盒子：伪元素、盒子、边框盒宽高。 */
type Sized = [Pseudo, Box, number, number];

interface Hazard {
	/** 自身有 backdrop-filter：背景不透明的祖先以外都不能剪。 */
	backdrop: boolean;
	/** 伪元素有 backdrop-filter：自己与背景透明的祖先不能剪。 */
	pseudoBackdrop: boolean;
	/**
	 * backdrop-filter 与 mask-image 同在一个盒子上（线程页头的六层渐进模糊纱）：实测这些层带 clip-path 时 Chromium 把模糊
	 * 整块铺满、渐变遮罩看起来失效（单层的隔离场景未复现，保守处理），所以这种盒子自身不剪；祖先按普通模糊层的规则处理。
	 */
	masked: boolean;
	/** fixed 或 z-index 不为 auto：不是层叠上下文的祖先不能剪。 */
	stack: boolean;
}

/** 解析一个圆角长手的计算值（"12px" 或 "12px 8px"）；含百分比或非 px 时返回 null。 */
function parseCorner(value: string, scale: number): CornerRadius | null {
	const parts = value.trim().split(/\s+/);
	if (parts.some((p) => !p.endsWith("px"))) return null;
	const x = Number.parseFloat(parts[0] ?? "");
	const y = Number.parseFloat(parts[1] ?? parts[0] ?? "");
	if (!Number.isFinite(x) || !Number.isFinite(y)) return null;
	return { x: x / scale, y: y / scale };
}

/** 计算值颜色的不透明度（rgb/rgba、带「/ α」的现代语法、transparent）。 */
export function colorAlpha(color: string): number {
	const c = color.trim();
	if (c === "transparent") return 0;
	const slash = c.match(/\/\s*([\d.]+)(%?)\s*\)$/);
	if (slash) return Number.parseFloat(slash[1] ?? "1") / (slash[2] ? 100 : 1);
	const rgba = c.match(/^rgba\((?:[^,]+,){3}\s*([\d.]+)\s*\)$/);
	if (rgba) return Number.parseFloat(rgba[1] ?? "1");
	return 1;
}

function hasBackdrop(s: CSSStyleDeclaration): boolean {
	const v =
		s.getPropertyValue("backdrop-filter") ||
		s.getPropertyValue("-webkit-backdrop-filter");
	return v !== "" && v !== "none";
}

function hasMask(s: CSSStyleDeclaration): boolean {
	const v =
		s.getPropertyValue("mask-image") ||
		s.getPropertyValue("-webkit-mask-image");
	return v !== "" && v !== "none";
}

function isStackingContext(s: CSSStyleDeclaration): boolean {
	return (
		(s.position !== "static" && s.zIndex !== "auto") ||
		s.position === "fixed" ||
		s.position === "sticky" ||
		Number.parseFloat(s.opacity) < 1 ||
		s.transform !== "none" ||
		(s.translate !== "" && s.translate !== "none") ||
		(s.scale !== "" && s.scale !== "none") ||
		(s.rotate !== "" && s.rotate !== "none") ||
		s.filter !== "none" ||
		s.isolation === "isolate" ||
		s.mixBlendMode !== "normal" ||
		hasBackdrop(s) ||
		/paint|strict|content/.test(s.contain)
	);
}

/**
 * 读边框：各条可见边都是 solid 且同色时返回宽度与颜色，交给 SVG 环带绘制；
 * 没有可见边框、颜色全透明、虚线等其它样式、各边异色、作者写了 border-image 时返回 null（原生绘制）。
 */
function readBorder(s: CSSStyleDeclaration, ownRing: string): Border | null {
	if (
		!ownRing &&
		s.borderImageSource !== "none" &&
		s.borderImageSource !== ""
	) {
		return null;
	}
	const widths: BorderWidths = [
		Number.parseFloat(s.borderTopWidth) || 0,
		Number.parseFloat(s.borderRightWidth) || 0,
		Number.parseFloat(s.borderBottomWidth) || 0,
		Number.parseFloat(s.borderLeftWidth) || 0,
	];
	const styles = [
		s.borderTopStyle,
		s.borderRightStyle,
		s.borderBottomStyle,
		s.borderLeftStyle,
	];
	const colors = [
		s.borderTopColor,
		s.borderRightColor,
		s.borderBottomColor,
		s.borderLeftColor,
	];
	let color = "";
	for (let i = 0; i < 4; i++) {
		if ((widths[i] ?? 0) <= 0) continue;
		if (styles[i] !== "solid") return null;
		const c = colors[i] ?? "";
		if (color && c !== color) return null;
		color = c;
	}
	if (!color || colorAlpha(color) === 0) return null;
	return { widths, color };
}

/** 盒子的边框盒尺寸（伪元素用计算值；内容盒尺寸时补上内边距与边框）。 */
function pseudoSize(
	s: CSSStyleDeclaration,
): { width: number; height: number } | null {
	let width = Number.parseFloat(s.width);
	let height = Number.parseFloat(s.height);
	if (!Number.isFinite(width) || !Number.isFinite(height)) return null;
	if (s.boxSizing === "content-box") {
		const sum = (...v: string[]) =>
			v.reduce((a, b) => a + (Number.parseFloat(b) || 0), 0);
		width += sum(
			s.paddingLeft,
			s.paddingRight,
			s.borderLeftWidth,
			s.borderRightWidth,
		);
		height += sum(
			s.paddingTop,
			s.paddingBottom,
			s.borderTopWidth,
			s.borderBottomWidth,
		);
	}
	return { width, height };
}

/** 跳过不画盒子的元素与 SVG 内部。 */
function skipElement(el: Element): boolean {
	if (el instanceof SVGElement) return !(el instanceof SVGSVGElement);
	return /^(HEAD|SCRIPT|STYLE|LINK|META|TITLE|TEMPLATE|NOSCRIPT|BR|WBR)$/.test(
		el.tagName,
	);
}

/** 元素的行内样式（HTML 与 SVG 元素都有；其它命名空间返回 null）。 */
function inlineStyle(el: Element): CSSStyleDeclaration | null {
	return "style" in el && el.style instanceof CSSStyleDeclaration
		? el.style
		: null;
}

/** 投影（不含内阴影）与焦点环伸出边框盒的最远距离。 */
function decorationReach(s: CSSStyleDeclaration): number {
	let reach = 0;
	if (s.boxShadow && s.boxShadow !== "none") {
		for (const shadow of s.boxShadow.split(/,(?![^(]*\))/)) {
			if (shadow.includes("inset")) continue;
			const [x = 0, y = 0, blur = 0, spread = 0] = (
				shadow.match(/-?[\d.]+px/g) ?? []
			).map(Number.parseFloat);
			reach = Math.max(
				reach,
				Math.max(Math.abs(x), Math.abs(y)) + blur + spread,
			);
		}
	}
	if (s.outlineStyle !== "none") {
		const w = Number.parseFloat(s.outlineWidth) || 0;
		reach = Math.max(
			reach,
			w + Math.max(0, Number.parseFloat(s.outlineOffset) || 0),
		);
	}
	return Math.ceil(reach);
}

/** 读一个盒子：不是超椭圆（--sq-s ≥ 1 即正圆 / 胶囊）、没有圆角或半径不是 px 时返回 null。 */
function readBox(
	s: CSSStyleDeclaration,
	prev: Box | undefined,
	pseudo: boolean,
): Box | null {
	const scale = Number.parseFloat(s.getPropertyValue(SCALE_VAR));
	if (!(scale > 0 && scale < 0.999)) return null;
	const tl = parseCorner(s.borderTopLeftRadius, scale);
	const tr = parseCorner(s.borderTopRightRadius, scale);
	const br = parseCorner(s.borderBottomRightRadius, scale);
	const bl = parseCorner(s.borderBottomLeftRadius, scale);
	if (!tl || !tr || !br || !bl) return null;
	if ([tl, tr, br, bl].every((c) => c.x <= 0 || c.y <= 0)) return null;
	const clipPath = s.clipPath;
	return {
		radii: [tl, tr, br, bl],
		border: readBorder(s, prev?.ring ?? ""),
		authorClip: !prev?.clip && clipPath !== "" && clipPath !== "none",
		reach: decorationReach(s),
		backdrop: hasBackdrop(s),
		masked: hasBackdrop(s) && hasMask(s),
		overflowVisible:
			!pseudo && s.overflowX === "visible" && s.overflowY === "visible",
		scale: prev?.scale ?? "",
		clip: prev?.clip ?? "",
		ring: prev?.ring ?? "",
		attr: prev?.attr ?? "",
		sized: prev?.sized ?? false,
	};
}

function sameBorder(a: Border | null, b: Border | null): boolean {
	if (!a || !b) return a === b;
	return a.color === b.color && a.widths.every((w, i) => w === b.widths[i]);
}

/**
 * 元素的行内样式从 old 变成 now，会不会波及后代的圆角、边框与投影：后代只能经继承受影响，相关的只有自定义属性、
 * color（currentColor 边框）、font-size（em 半径）与 zoom。虚拟列表每帧改行的 transform / top 之类不算，
 * 免得整个列表的受管后代都跟着重读。scratch 是解析旧值用的空白声明块（会被改写）。
 */
export function styleChangeCascades(
	old: string | null,
	now: CSSStyleDeclaration,
	scratch: CSSStyleDeclaration,
): boolean {
	scratch.cssText = old ?? "";
	const inherited = (s: CSSStyleDeclaration) => {
		const out = new Map<string, string>();
		for (let i = 0; i < s.length; i++) {
			const p = s.item(i);
			if (
				p.startsWith("--") ||
				p === "color" ||
				p === "font-size" ||
				p === "zoom"
			)
				out.set(p, s.getPropertyValue(p));
		}
		return out;
	};
	const a = inherited(scratch);
	const b = inherited(now);
	if (a.size !== b.size) return true;
	for (const [p, v] of a) if (b.get(p) !== v) return true;
	return false;
}

/**
 * 把 --sq-s 注册成不继承、初始值为 ROUND_SCALE 的自定义属性：每个元素与伪元素不写任何规则就是包络圆系数，
 * 正圆与胶囊由 index.css 设为 1，被等比缩小的盒子由共享规则改写——都不会传给后代。
 * 比通配规则（`*, ::before, ::after { --sq-s: … }`）便宜：通配规则让每个元素在每次样式重算里都要匹配、带上自己的变量表。
 * 不支持 CSS.registerProperty 的引擎（Firefox 128 以前）退回通配规则，写在 sheet 里。
 */
function registerScale(sheet: CSSStyleSheet): void {
	const initial = ROUND_SCALE.toFixed(5);
	if (
		typeof CSS !== "undefined" &&
		typeof CSS.registerProperty === "function"
	) {
		try {
			// syntax "*"：值只当作记号传给 var()，不参与插值（作者的 transition: all 不会把它过渡）
			CSS.registerProperty({
				name: SCALE_VAR,
				syntax: "*",
				inherits: false,
				initialValue: initial,
			});
			return;
		} catch {
			// 同名属性已被注册（不会发生：每个文档只装一次）——仍按通配规则兜底
		}
	}
	sheet.insertRule(
		`:root[${ROOT_ATTR}],:root[${ROOT_ATTR}] *,:root[${ROOT_ATTR}] ::before,:root[${ROOT_ATTR}] ::after{${SCALE_VAR}:${initial}}`,
		0,
	);
}

/**
 * 启用降级：改写现有与以后加入的样式表，扫描文档并持续跟踪。每个文档只装一次（以根元素上的开关属性为准）。
 * 须在首屏渲染前调用（main.tsx 在挂载 React 之前 await），否则已经画出来的圆角会先闪一下普通圆角。
 */
export function installSquircleFallback(doc: Document): void {
	const root = doc.documentElement;
	if (root.hasAttribute(ROOT_ATTR)) return;
	root.setAttribute(ROOT_ATTR, "fallback");

	const tracks = new Map<Element, Track>();
	const hazards = new Map<Element, Hazard>();
	const ruleCounts = new WeakMap<CSSStyleSheet, number>();
	const ownSheet = new CSSStyleSheet();
	doc.adoptedStyleSheets = [...doc.adoptedStyleSheets, ownSheet];
	registerScale(ownSheet);
	const rules = new ShapeRules(ownSheet);

	// 待处理的工作，统一在下一帧（或 ResizeObserver 回调）里批量做：先读后写
	const pendingRoots = new Set<Element>();
	const pendingInspect = new Set<Element>();
	/** 上面那些里，变化可能波及后代的（类名、data-* 等经选择器影响后代；行内样式只在改了会被继承的属性时才算）。 */
	const pendingCascade = new Set<Element>();
	/**
	 * 切换主题后要按新颜色换的环带：留到主题切换的下一帧再换（换环带要重算这些盒子的样式，挤在整页重算的那一帧里会让它更长），
	 * recolorFrame 是排进来时的帧号。其间环带停在旧颜色一帧。
	 */
	const recolor = new Set<Element>();
	let recolorFrame = 0;
	let frameNo = 0;
	/** 可以晚几帧再重读的元素（主题切换后的全部盒子、祖先变化波及的后代）：每帧按 FRAME_BUDGET_MS 处理一部分。 */
	const deferred = new Set<Element>();
	const pendingRefresh = new Set<Element>();
	const pendingGeometry = new Set<Element>();
	/** 尺寸在连续变化、等着稳定的元素。 */
	const settling = new Set<Element>();
	let settleTimer = 0;
	/** 有边框色 / 宽度过渡或动画在跑的元素。 */
	const borderBusy = new Set<Element>();
	/** deferred 从空变为非空的时刻，及「动画一直不停时也要处理」的兜底定时器。 */
	let deferredSince = 0;
	let deferTimer = 0;
	let inspectAll = false;
	let rootChanged = false;
	let colorsDirty = false;
	let prune = false;
	let hazardsDirty = false;
	let frame = 0;

	/**
	 * 改写全部样式表。返回 "rescan"：有半径声明被改写（受管与未受管元素的半径都可能变了，整篇重扫）；
	 * "reinspect"：有样式表增加或规则数变化、但没有半径声明——只重读已受管的盒子（变量、边框、风险可能变了），
	 * 不必遍历整篇文档；null：没变。SVG 内的 <style>（Mermaid 每次重绘、包括每次切换主题都换一张，规则限定在该图表内）
	 * 没有半径声明时不触发重读。
	 */
	function patchSheets(): "rescan" | "reinspect" | null {
		let changed: "rescan" | "reinspect" | null = null;
		const sheets = [...Array.from(doc.styleSheets), ...doc.adoptedStyleSheets];
		for (const sheet of sheets) {
			if (sheet === ownSheet) continue;
			let count: number;
			try {
				count = sheet.cssRules.length;
			} catch {
				continue;
			}
			if (ruleCounts.get(sheet) === count) continue;
			const { ruleCount, patched } = patchSheet(sheet);
			ruleCounts.set(sheet, ruleCount);
			if (patched > 0) changed = "rescan";
			else if (!(sheet.ownerNode instanceof SVGElement))
				changed ??= "reinspect";
		}
		return changed;
	}

	function schedule() {
		if (frame) return;
		frame = requestAnimationFrame(() => {
			frame = 0;
			frameNo++;
			flush(true);
		});
	}

	/** 读一个元素：登记风险、找出超椭圆盒子；返回是否需要重算几何。 */
	function inspect(el: Element): boolean {
		if (!el.isConnected || skipElement(el)) return false;
		const style = inlineStyle(el);
		// 行内样式里的半径也要乘 --sq-s（只看有行内声明的元素；不用 getAttribute("style")：序列化整段行内样式
		// 比直接查九个半径属性贵）
		if (style && style.length > 0) patchDeclarations(style);
		const prev = tracks.get(el);
		const cs = getComputedStyle(el);
		const backdrop = hasBackdrop(cs);
		let pseudoBackdrop = false;
		let masked = backdrop && hasMask(cs);
		const boxes = new Map<Pseudo, Box>();
		for (const p of PSEUDOS) {
			const s = p ? getComputedStyle(el, p) : cs;
			if (p && (s.content === "none" || s.content === "normal")) continue;
			if (p && hasBackdrop(s)) {
				pseudoBackdrop = true;
				if (hasMask(s)) masked = true;
			}
			const box = readBox(s, prev?.boxes.get(p), p !== "");
			if (box) boxes.set(p, box);
		}

		const hazard: Hazard = {
			backdrop,
			pseudoBackdrop,
			masked,
			stack: cs.position === "fixed" || cs.zIndex !== "auto",
		};
		if (hazard.backdrop || hazard.pseudoBackdrop || hazard.stack) {
			const old = hazards.get(el);
			if (
				!old ||
				old.backdrop !== hazard.backdrop ||
				old.pseudoBackdrop !== hazard.pseudoBackdrop ||
				old.masked !== hazard.masked ||
				old.stack !== hazard.stack
			) {
				hazards.set(el, hazard);
				hazardsDirty = true;
			}
		} else if (hazards.delete(el)) {
			hazardsDirty = true;
		}

		if (boxes.size === 0) {
			if (!prev) return false;
			// 不再是超椭圆：撤掉写过的样式
			for (const [p, box] of prev.boxes) clearBox(el, p, box);
			tracks.delete(el);
			resizeObserver.unobserve(el);
			return false;
		}
		if (prev) {
			// 撤掉消失了的伪元素盒子
			for (const [p, box] of prev.boxes) {
				if (!boxes.has(p)) clearBox(el, p, box);
			}
			prev.boxes = boxes;
			if (prev.stacking !== null || prev.opaque !== null) {
				// 是风险元素的祖先：层叠上下文与背景可能变了，按需重读
				prev.stacking = null;
				prev.opaque = null;
				hazardsDirty = true;
			}
			return true;
		}
		tracks.set(el, {
			boxes,
			width: -1,
			height: -1,
			stacking: null,
			opaque: null,
			unsafe: false,
			animating: new Set(),
			lastResize: Number.NEGATIVE_INFINITY,
			resizing: false,
			fluid: false,
			borderAnims: new Map(),
		});
		hazardsDirty = true; // 新盒子要按现有风险算一次能不能剪
		resizeObserver.observe(el, { box: "border-box" });
		return true;
	}

	/** 只重读会随悬停、聚焦、颜色过渡变化的部分（边框、投影与焦点环的伸出量）：比 inspect 便宜，变了才重算几何。 */
	function refreshState(el: Element, t: Track) {
		for (const [p, box] of t.boxes) {
			const s = getComputedStyle(el, p || null);
			const border = readBorder(s, box.ring);
			const reach = decorationReach(s);
			if (!sameBorder(border, box.border) || reach !== box.reach) {
				box.border = border;
				box.reach = reach;
				pendingGeometry.add(el);
			}
		}
	}

	function discover(rootEl: Element) {
		const walker = doc.createTreeWalker(rootEl, NodeFilter.SHOW_ELEMENT, {
			acceptNode: (n) =>
				n instanceof Element && skipElement(n)
					? n instanceof SVGElement
						? NodeFilter.FILTER_REJECT
						: NodeFilter.FILTER_SKIP
					: NodeFilter.FILTER_ACCEPT,
		});
		if (!skipElement(rootEl) && inspect(rootEl)) pendingGeometry.add(rootEl);
		for (let n = walker.nextNode(); n; n = walker.nextNode()) {
			if (n instanceof Element && inspect(n)) pendingGeometry.add(n);
		}
	}

	/** 背景不透明（按需读，见 Track.opaque）。 */
	function opaqueOf(el: Element, t: Track): boolean {
		if (t.opaque === null) {
			const cs = getComputedStyle(el);
			t.opaque =
				cs.backgroundImage === "none" && colorAlpha(cs.backgroundColor) >= 1;
		}
		return t.opaque;
	}

	/** 按风险重算每个元素能不能剪：从每个风险元素往上标记祖先。 */
	function updateSafety() {
		const unsafe = new Set<Element>();
		for (const [h, hz] of hazards) {
			if (!h.isConnected) continue;
			const self = tracks.get(h);
			if ((hz.pseudoBackdrop && self && !opaqueOf(h, self)) || hz.masked)
				unsafe.add(h);
			for (let a = h.parentElement; a; a = a.parentElement) {
				const t = tracks.get(a);
				if (!t) continue;
				if ((hz.backdrop || hz.pseudoBackdrop) && !opaqueOf(a, t)) {
					unsafe.add(a);
				}
				if (hz.stack) {
					t.stacking ??= isStackingContext(getComputedStyle(a));
					if (!t.stacking) unsafe.add(a);
				}
			}
		}
		for (const [el, t] of tracks) {
			const u = unsafe.has(el);
			if (u !== t.unsafe) {
				t.unsafe = u;
				pendingGeometry.add(el);
			}
		}
	}

	/**
	 * 写一个盒子：把 --sq-s、clip-path、border-image 拼成一组声明，取得对应的共享规则（squircle-rules.ts），
	 * 在宿主上换挂它的属性。声明没变时什么都不碰：每次挂属性都会让该元素重算样式。
	 */
	function writeBox(
		el: Element,
		p: Pseudo,
		box: Box,
		scale: string,
		clip: string,
		ring: string,
	) {
		if (box.scale === scale && box.clip === clip && box.ring === ring) return;
		box.scale = scale;
		box.clip = clip;
		box.ring = ring;
		// !important：盖过作者样式里同名的普通声明（作者自己写了 clip-path / border-image 的盒子本来就不由这里写）
		let decl = "";
		if (scale) decl += `${SCALE_VAR}:${scale}!important;`;
		if (clip) decl += `clip-path:${clip}!important;`;
		if (ring) decl += `border-image:${ring}!important;`;
		const attr = decl ? rules.acquire(decl, p) : "";
		if (attr && attr === box.attr) {
			rules.release(attr);
			return;
		}
		if (attr) el.setAttribute(attr, "");
		if (box.attr) {
			el.removeAttribute(box.attr);
			rules.release(box.attr);
		}
		box.attr = attr;
	}

	function clearBox(el: Element, p: Pseudo, box: Box) {
		writeBox(el, p, box, "", "", "");
	}

	/** 不再跟踪的元素（已移出文档）：摘掉挂着的规则属性，归还引用（元素若被重新插入，会按新元素重新读）。 */
	function untrack(el: Element, t: Track) {
		for (const box of t.boxes.values()) {
			if (!box.attr) continue;
			el.removeAttribute(box.attr);
			rules.release(box.attr);
		}
	}

	/**
	 * 读：各受管盒子的边框盒尺寸。伪元素的尺寸要读 getComputedStyle 的 width / height（会触发样式与布局更新），
	 * 所以本轮所有元素先量完再统一写（geometry）：读写交错时每写一个元素、下一个伪元素的读取就要再重排一次。
	 * 尺寸在连续变化的元素不读伪元素尺寸：与尺寸无关的伪元素保持原样，有关的宽高记 -1（geometry 只撤掉裁剪与环带）。
	 */
	function measure(el: Element, t: Track): Sized[] {
		const sizes: Sized[] = [];
		for (const [p, box] of t.boxes) {
			if (p && t.resizing) {
				if (box.sized) sizes.push([p, box, -1, -1]);
				continue;
			}
			const size = p
				? pseudoSize(getComputedStyle(el, p))
				: t.width >= 0
					? { width: t.width, height: t.height }
					: null;
			if (size && size.width > 0 && size.height > 0)
				sizes.push([p, box, size.width, size.height]);
		}
		return sizes;
	}

	/** 写：按 measure 量出的尺寸算出 clip-path 与边框环并写入（不读布局）。 */
	function geometry(el: Element, t: Track, sizes: Sized[]) {
		for (const [p, box, width, height] of sizes) {
			if (width < 0) {
				writeBox(el, p, box, box.scale, "", "");
				continue;
			}
			const { radii, factor } = usedRadii(width, height, box.radii);
			// 取 4 位小数：子像素的尺寸抖动会让系数在 1e-6 量级上来回变，每次都换一条规则
			const scale = factor < 1 ? (ROUND_SCALE * factor).toFixed(4) : "";
			// clip-path 的外扩：带 backdrop-filter 的盒子紧贴自身（外扩会把模糊范围一起扩大），有投影或焦点环时干脆不剪；
			// 后代可能溢出的元素留足 BLEED；其余只留投影与焦点环的伸出量
			const bleed = box.backdrop
				? box.reach > 0 || box.masked
					? -1
					: 0
				: box.overflowVisible
					? BLEED
					: box.reach + 2;
			const clip =
				bleed < 0 || box.authorClip || t.animating.has(p) || (!p && t.unsafe)
					? ""
					: (sliverClipPath(
							radii,
							ROUND_SCALE,
							bleed,
							t.fluid ? undefined : { width, height },
						) ?? "");
			// 边框色在过渡 / 动画中：交给原生边框（颜色由浏览器逐帧插值，形状停在包络圆）
			const ring =
				box.border && !t.borderAnims.get(p)
					? ringImage(width, height, radii, box.border.widths, box.border.color)
					: null;
			box.sized =
				factor < 1 || ring?.sized === true || (clip !== "" && !t.fluid);
			if (t.resizing && box.sized) {
				// 连续变化中、声明又与尺寸有关：只维持等比缩小系数，停在包络圆、原生边框，稳定后再按最终尺寸剪
				writeBox(el, p, box, scale, "", "");
			} else {
				writeBox(el, p, box, scale, clip, ring?.image ?? "");
			}
		}
	}

	/** 尺寸稳定检查：最后一次变化后满 SETTLE_MS 的元素恢复裁剪与环带。 */
	function settle() {
		settleTimer = 0;
		const now = performance.now();
		for (const el of settling) {
			const t = tracks.get(el);
			if (t && now - t.lastResize < SETTLE_MS) continue;
			settling.delete(el);
			if (!t) continue;
			t.resizing = false;
			pendingGeometry.add(el);
		}
		if (pendingGeometry.size > 0) schedule();
		if (settling.size > 0) settleTimer = window.setTimeout(settle, SETTLE_MS);
	}

	function handleRecords(records: MutationRecord[]) {
		for (const r of records) {
			if (r.type === "childList") {
				for (const n of r.addedNodes) {
					if (n instanceof Element && !(n instanceof HTMLStyleElement)) {
						pendingRoots.add(n);
					}
				}
				if (r.removedNodes.length > 0) prune = true;
			} else if (r.type === "attributes" && r.target instanceof Element) {
				// 自己挂的规则属性不算；SVG 内部（如 Mermaid 换主题时重写的几百个 path / marker 属性）既不受管、也不是受管元素的祖先
				if (RULE_ATTR.test(r.attributeName ?? "") || skipElement(r.target))
					continue;
				if (r.target === root || r.target === doc.body) rootChanged = true;
				else {
					pendingInspect.add(r.target);
					if (
						r.attributeName !== "style" ||
						inlineCascades(r.target, r.oldValue)
					)
						pendingCascade.add(r.target);
				}
			}
			// <style> 的增删与文字变化（含 Vite 热更新）不用单独处理：flush 每次都比对各样式表的规则数
		}
	}

	/** 解析旧行内样式用的空白声明块。 */
	const scratch = doc.createElement("div").style;
	function inlineCascades(el: Element, old: string | null): boolean {
		const now = inlineStyle(el);
		return !now || styleChangeCascades(old, now, scratch);
	}

	/** 放进「可以晚几帧」的队列（见 FRAME_BUDGET_MS），记下排队开始的时刻。 */
	function defer(el: Element) {
		if (deferred.size === 0) deferredSince = performance.now();
		deferred.add(el);
	}

	/**
	 * 主流程：样式表 → 发现与重读 → 风险 → 几何（先读后写）→ 丢弃自己写入引起的变更记录。
	 * inFrame：由 requestAnimationFrame 调用（只有这时才按预算处理 deferred；ResizeObserver 回调里只做必须马上做的）。
	 */
	function flush(inFrame: boolean) {
		const start = performance.now();
		handleRecords(mutationObserver.takeRecords());
		const sheets = patchSheets();
		if (sheets === "rescan") {
			// 新的或变了的样式表改写了半径声明：整篇重扫
			pendingRoots.clear();
			pendingRoots.add(root);
			inspectAll = true;
		} else if (sheets === "reinspect") {
			inspectAll = true;
		}
		if (prune) {
			prune = false;
			for (const [el, t] of tracks) {
				if (el.isConnected) continue;
				tracks.delete(el);
				borderBusy.delete(el);
				resizeObserver.unobserve(el);
				untrack(el, t);
			}
			for (const el of hazards.keys()) {
				if (!el.isConnected) {
					hazards.delete(el);
					hazardsDirty = true;
				}
			}
		}
		for (const el of pendingRoots) if (el.isConnected) discover(el);
		pendingRoots.clear();
		if (rootChanged) {
			// 根元素或 body 的属性变了（切换主题、强调色、壁纸）：任何盒子的颜色、背景、投影都可能变。
			// 环带的颜色烤在 SVG 里：有边框的盒子在下一帧重读边框色、换上新颜色的环带（见 recolor）；其余属性按预算逐帧重读
			rootChanged = false;
			inspectAll = true;
			for (const [el, t] of tracks) {
				for (const box of t.boxes.values()) {
					if (box.border || box.ring) {
						recolor.add(el);
						break;
					}
				}
			}
			recolorFrame = frameNo;
		}
		if (inFrame && recolor.size > 0 && frameNo > recolorFrame) {
			for (const el of recolor) pendingRefresh.add(el);
			recolor.clear();
		}
		if (inspectAll) {
			inspectAll = false;
			for (const el of tracks.keys()) defer(el);
		}
		if (pendingInspect.size > 0) {
			// 祖先上的类名或行内变量变了：它的受管后代也可能跟着变（沿祖先链查，代价与受管盒子数 × 深度成正比）。
			// 变化的元素本身马上重读，后代只是「可能」受影响，放进 deferred 按预算处理
			if (pendingCascade.size > 0) {
				for (const el of tracks.keys()) {
					if (pendingInspect.has(el)) continue;
					for (let a = el.parentElement; a; a = a.parentElement) {
						if (pendingCascade.has(a)) {
							defer(el);
							break;
						}
					}
				}
			}
			for (const el of pendingInspect) {
				deferred.delete(el);
				if (inspect(el)) pendingGeometry.add(el);
			}
			pendingInspect.clear();
			pendingCascade.clear();
		}
		if (inFrame && deferred.size > 0) {
			// 动画进行中（有盒子在连续变尺寸、有边框在过渡）不和它抢帧：等安静下来再按预算处理，最多等 DEFER_MAX_MS。
			// 刚在本帧排进来的一批也留到下一帧：触发它的变化（切换主题的整页样式重算、侧栏开合的首帧）本身就是重帧
			const quiet = settling.size === 0 && borderBusy.size === 0;
			const waited = start - deferredSince;
			if ((quiet && waited > 0) || waited >= DEFER_MAX_MS) {
				for (const el of deferred) {
					if (performance.now() - start > FRAME_BUDGET_MS) break;
					deferred.delete(el);
					if (inspect(el)) pendingGeometry.add(el);
				}
			}
		}
		if (colorsDirty) {
			colorsDirty = false;
			for (const [el, t] of tracks) refreshState(el, t);
		} else {
			for (const el of pendingRefresh) {
				const t = tracks.get(el);
				if (t) refreshState(el, t);
			}
		}
		pendingRefresh.clear();
		if (hazardsDirty) {
			hazardsDirty = false;
			updateSafety();
		}
		const measured: Array<[Element, Track, Sized[]]> = [];
		for (const el of pendingGeometry) {
			const t = tracks.get(el);
			if (t) measured.push([el, t, measure(el, t)]);
		}
		pendingGeometry.clear();
		for (const [el, t, sizes] of measured) geometry(el, t, sizes);
		mutationObserver.takeRecords();
		if (recolor.size > 0) schedule();
		if (deferred.size > 0) {
			if (settling.size === 0 && borderBusy.size === 0) schedule();
			// 动画还在跑：安静下来时（settle、边框动画结束）会再排一帧；兜底在 DEFER_MAX_MS 后强制处理
			else if (!deferTimer)
				deferTimer = window.setTimeout(
					() => {
						deferTimer = 0;
						schedule();
					},
					Math.max(0, deferredSince + DEFER_MAX_MS - performance.now()),
				);
		}
	}

	const resizeObserver = new ResizeObserver((entries) => {
		const now = performance.now();
		for (const e of entries) {
			const t = tracks.get(e.target);
			const size = e.borderBoxSize[0];
			if (!t || !size) continue;
			if (size.inlineSize === t.width && size.blockSize === t.height) continue;
			// 刚变过又变：进入连续变化（停在包络圆），等稳定后再剪；首次尺寸与偶发的单次变化马上按新尺寸剪
			if (t.width >= 0 && now - t.lastResize < SETTLE_MS && !t.resizing) {
				t.resizing = true;
				t.fluid = true;
				settling.add(e.target);
				if (!settleTimer) settleTimer = window.setTimeout(settle, SETTLE_MS);
			}
			t.width = size.inlineSize;
			t.height = size.blockSize;
			t.lastResize = now;
			pendingGeometry.add(e.target);
		}
		flush(false);
	});

	const mutationObserver = new MutationObserver((records) => {
		handleRecords(records);
		schedule();
	});
	mutationObserver.observe(root, {
		subtree: true,
		// 行内样式变化要比对旧值（styleCascades）
		attributeOldValue: true,
		childList: true,
		attributes: true,
	});

	const onState = () => {
		colorsDirty = true;
		schedule();
	};
	for (const type of STATE_EVENTS) {
		doc.addEventListener(type, onState, { capture: true, passive: true });
	}

	const pseudoOf = (e: TransitionEvent | AnimationEvent): Pseudo =>
		e.pseudoElement === "::before" || e.pseudoElement === "::after"
			? e.pseudoElement
			: "";
	/** 边框色 / 宽度过渡或动画开始与结束：期间撤掉环带（原生边框跟着插值），结束后重读颜色、补回环带。 */
	const borderAnim = (el: Element, t: Track, p: Pseudo, running: boolean) => {
		const n = (t.borderAnims.get(p) ?? 0) + (running ? 1 : -1);
		if (n > 0) t.borderAnims.set(p, n);
		else {
			t.borderAnims.delete(p);
			pendingRefresh.add(el);
		}
		if (t.borderAnims.size > 0) borderBusy.add(el);
		else borderBusy.delete(el);
		pendingGeometry.add(el);
		schedule();
	};

	// 半径过渡（如手机侧栏卡片展开）期间不剪，由原生的包络圆跟着过渡；结束后按新半径重剪。
	const onTransition = (e: Event) => {
		if (!(e instanceof TransitionEvent) || !(e.target instanceof Element))
			return;
		const t = tracks.get(e.target);
		if (!t) return;
		const running = e.type === "transitionrun";
		const p = pseudoOf(e);
		if (e.propertyName.includes("radius")) {
			if (running) t.animating.add(p);
			else {
				t.animating.delete(p);
				pendingInspect.add(e.target);
			}
			pendingGeometry.add(e.target);
			schedule();
		} else if (e.propertyName.startsWith("border")) {
			borderAnim(e.target, t, p, running);
		}
	};
	for (const type of ["transitionrun", "transitionend", "transitioncancel"]) {
		doc.addEventListener(type, onTransition, true);
	}
	// 关键帧动画改边框色（如托盘描边闪角色色）：同上。关键帧名是全局的，是否碰边框按名字缓存（结束时动画已取不到）
	const borderKeyframes = new Map<string, boolean>();
	const onAnimation = (e: Event) => {
		if (!(e instanceof AnimationEvent) || !(e.target instanceof Element))
			return;
		const t = tracks.get(e.target);
		if (!t) return;
		const name = e.animationName;
		if (e.type === "animationstart" && !borderKeyframes.has(name)) {
			borderKeyframes.set(
				name,
				e.target
					.getAnimations()
					.some(
						(a) =>
							a instanceof CSSAnimation &&
							a.animationName === name &&
							a.effect instanceof KeyframeEffect &&
							a.effect
								.getKeyframes()
								.some((k) =>
									Object.keys(k).some((key) => key.startsWith("border")),
								),
					),
			);
		}
		if (!borderKeyframes.get(name)) return;
		borderAnim(e.target, t, pseudoOf(e), e.type === "animationstart");
	};
	for (const type of ["animationstart", "animationend", "animationcancel"]) {
		doc.addEventListener(type, onAnimation, true);
	}
	// <link> 样式表加载完才有规则（flush 会比对规则数并改写）
	doc.addEventListener(
		"load",
		(e) => {
			if (e.target instanceof HTMLLinkElement) schedule();
		},
		true,
	);

	patchSheets();
	discover(root);
	updateSafety();
	mutationObserver.takeRecords();
}
