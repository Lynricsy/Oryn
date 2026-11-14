/**
 * 超椭圆角形的几何（纯函数，无 DOM）：与 CSS Borders 4「corner-shape: superellipse(K)」同一条曲线。
 *
 * 规范（§3.9.4.2 Computing a contoured path）：每个角是单位超椭圆 |x|^n + |y|^n = 1（n = 2^K）按该角的
 * border-radius（水平、垂直半径可不同）缩放后的四分之一段；相邻半径之和超过边长时按 CSS 规则整体等比缩小。
 * 本站 K = 1.6，n ≈ 3.03。
 *
 * 降级思路（见 squircle-fallback.ts）：原生圆角取 ROUND_SCALE × 半径，这个圆刚好把超椭圆整个包住、在对角线上相切
 * （两者最大偏差约 1.4% 半径）；再用 clip-path 只剪掉两条曲线之间那条薄片，得到精确的超椭圆外轮廓。
 */

/** 本站选定的 superellipse() 参数 K。 */
export const SUPERELLIPSE_K = 1.6;
/** 超椭圆指数 n = 2^K。 */
export const EXPONENT = 2 ** SUPERELLIPSE_K;

/**
 * 包络圆的半径系数：半径为 k·r 的圆角恰好包住半径 r 的超椭圆角，两者在 45° 对角线上相切。
 * 对角线上超椭圆离两条边各 r·(1 − 2^(−1/n))，圆离两条边各 k·r·(1 − √½)，令二者相等得 k。
 */
export const ROUND_SCALE = (1 - 2 ** (-1 / EXPONENT)) / (1 - Math.SQRT1_2);

/** 一个角的半径（水平 x、垂直 y），单位 px。 */
export interface CornerRadius {
	x: number;
	y: number;
}

/** 四个角，顺序：左上、右上、右下、左下。 */
export type Radii = readonly [
	CornerRadius,
	CornerRadius,
	CornerRadius,
	CornerRadius,
];

/**
 * CSS 的半径重叠规则（css-backgrounds-3 §5.5）：f = min(边长 / 该边两端半径之和)，f < 1 时四个角一起乘 f。
 * 返回实际使用的半径与系数 f（≤ 1）。
 */
export function usedRadii(
	width: number,
	height: number,
	radii: Radii,
): { radii: Radii; factor: number } {
	const [tl, tr, br, bl] = radii;
	let factor = 1;
	const limit = (side: number, sum: number) => {
		if (sum > 0) factor = Math.min(factor, side / sum);
	};
	limit(width, tl.x + tr.x);
	limit(height, tr.y + br.y);
	limit(width, br.x + bl.x);
	limit(height, bl.y + tl.y);
	factor = Math.max(0, factor);
	if (factor >= 1) return { radii, factor: 1 };
	const scale = (c: CornerRadius) => ({ x: c.x * factor, y: c.y * factor });
	return { radii: [scale(tl), scale(tr), scale(br), scale(bl)], factor };
}

/** 一个角的折线段数：随半径增长，半径 ≤ 800px 时弦高误差都小于 0.04px（单测里验证）。 */
export function cornerSegments(radius: number): number {
	return Math.min(96, Math.max(12, Math.ceil(radius / 1.5)));
}

/** unitCorner 的结果按段数缓存（段数只有几十种，每个点要两次幂运算）。 */
const unitCorners = new Map<number, ReadonlyArray<readonly [number, number]>>();

/**
 * 单位超椭圆角（以角的圆心为原点、朝外为正）上的采样点 (a, b)，满足 a^n + b^n = 1：
 * 从 (0, 1) 走到 (1, 0)。按 θ 均匀取 a = sin(θ)^(2/n)、b = cos(θ)^(2/n)，点在曲线上分布较均匀。
 */
export function unitCorner(
	segments: number,
): ReadonlyArray<readonly [number, number]> {
	const hit = unitCorners.get(segments);
	if (hit) return hit;
	const out: Array<readonly [number, number]> = [];
	const p = 2 / EXPONENT;
	for (let i = 0; i <= segments; i++) {
		const t = (i / segments) * (Math.PI / 2);
		// 端点精确取 0 / 1，避免 cos(π/2) 的浮点残差
		const a = i === segments ? 1 : Math.sin(t) ** p;
		const b = i === 0 ? 1 : i === segments ? 0 : Math.cos(t) ** p;
		out.push([a, b]);
	}
	unitCorners.set(segments, out);
	return out;
}

/**
 * clip-path 与边框环的字符串缓存：同尺寸（或与尺寸无关的写法）、同半径的盒子与不改变几何的重算（主题切换、悬停）直接复用。
 * 只按条数封顶，满了整体清空。
 */
const PATH_CACHE_LIMIT = 1024;
const clipCache = new Map<string, string | null>();
/** 边框环：已 URL 编码的 SVG 前半（到 fill: 为止）与后半（路径之后，含切片参数），颜色夹在中间。 */
const ringCache = new Map<string, readonly [string, string]>();

function remember<T>(cache: Map<string, T>, key: string, make: () => T): T {
	if (cache.has(key)) return cache.get(key) as T;
	if (cache.size >= PATH_CACHE_LIMIT) cache.clear();
	const value = make();
	cache.set(key, value);
	return value;
}

function radiiKey(radii: Radii): string {
	return radii.map((c) => `${c.x},${c.y}`).join(";");
}

/** 坐标保留 3 位小数，去掉多余的 0。 */
function num(v: number): string {
	const r = Math.round(v * 1000) / 1000;
	return Object.is(r, -0) ? "0" : String(r);
}

/** 离左 / 上边 v px 的坐标。 */
function near(v: number): string {
	const s = num(v);
	return s === "0" ? "0" : `${s}px`;
}

/** 离右 / 下边 v px 的坐标（v < 0 时在盒子外）。 */
function far(v: number): string {
	const s = num(v);
	if (s === "0") return "100%";
	return s.startsWith("-")
		? `calc(100% + ${s.slice(1)}px)`
		: `calc(100% - ${s}px)`;
}

/**
 * 一个角的「薄片」（原生圆角 = 半径 a = scale·rx、b = scale·ry 的椭圆弧以内、超椭圆（半径 rx、ry）以外）的闭合折线，
 * 局部坐标 (u, v)：u 从顶点沿横边向内，v 沿竖边向内。起止于横边上的 (rx, 0)。
 * 椭圆弧用外切折线（顶点在弧外 1/cos(Δ/2) 处）：折线把弧整个包在薄片里，弧外只多剪掉不到 0.05px 的投影，
 * 不会在超椭圆外留下原生背景的小凸起。
 */
function sliverLoop(c: CornerRadius, scale: number): Array<[number, number]> {
	const a = c.x * scale;
	const b = c.y * scale;
	const loop: Array<[number, number]> = [
		[c.x, 0],
		[a, 0],
	];
	// 弧上 θ 处的点 (a·(1 − sin θ), b·(1 − cos θ))，θ 从 0（横边）到 π/2（竖边）
	const n = cornerSegments(Math.max(a, b));
	const step = Math.PI / 2 / n;
	const out = 1 / Math.cos(step / 2);
	for (let i = 0; i < n; i++) {
		const t = (i + 0.5) * step;
		loop.push([a - a * Math.sin(t) * out, b - b * Math.cos(t) * out]);
	}
	loop.push([0, b], [0, c.y]);
	// 超椭圆从竖边 (0, ry) 回到横边 (rx, 0)：局部点 = 圆心 (rx, ry) − (rx·a', ry·b')
	const pts = unitCorner(cornerSegments(Math.max(c.x, c.y)));
	for (let i = pts.length - 2; i >= 1; i--) {
		const [ua, ub] = pts[i] as readonly [number, number];
		loop.push([c.x - c.x * ua, c.y - c.y * ub]);
	}
	loop.push([c.x, 0]);
	return loop;
}

/**
 * 同一片薄片的 SVG 路径（页面坐标，弧用精确的 A 命令）：ox, oy 是该角的顶点；sx, sy 是从顶点指向盒子内部的方向（±1）。
 */
function sliverPath(
	ox: number,
	oy: number,
	sx: number,
	sy: number,
	c: CornerRadius,
	scale: number,
): string {
	const a = c.x * scale;
	const b = c.y * scale;
	// 弧从横边上的 (a, 0) 走到竖边上的 (0, b)（局部坐标），弧心在 (a, b)，凸向顶点；镜像奇数次时弧的方向翻转
	const sweep = sx * sy > 0 ? 0 : 1;
	let d = `M${num(ox + sx * c.x)} ${num(oy)}L${num(ox + sx * a)} ${num(oy)}`;
	d += `A${num(a)} ${num(b)} 0 0 ${sweep} ${num(ox)} ${num(oy + sy * b)}`;
	d += `L${num(ox)} ${num(oy + sy * c.y)}`;
	const pts = unitCorner(cornerSegments(Math.max(c.x, c.y)));
	for (let i = pts.length - 2; i >= 1; i--) {
		const [ua, ub] = pts[i] as readonly [number, number];
		d += `L${num(ox + sx * (c.x - c.x * ua))} ${num(oy + sy * (c.y - c.y * ub))}`;
	}
	return `${d}Z`;
}

/** 薄片小于这个宽度（px）时不值得剪：k·r 的圆与超椭圆最大偏差约 0.0143·r，r < 3.5px 时不到 0.05px。 */
const MIN_RADIUS = 3.5;

/**
 * 降级用的 clip-path：一个向四周外扩 bleed 的大矩形，挖掉四个角的薄片（evenodd）。
 * 只去掉「原生圆角画了、超椭圆不该有」的那条薄片，盒子外的投影、焦点环与溢出的子元素都保留。
 * radii 是实际使用的半径（已按 usedRadii 缩小），scale 是原生圆角相对它的系数（ROUND_SCALE）。没有值得剪的角时返回 null。
 *
 * 两种写法，形状相同：
 * - 给了 size：path()，按该尺寸写死的 px 坐标。样式重算时最便宜（整条路径解析一次、各元素共用），但尺寸一变就要重写。
 * - 不给 size：polygon() + 百分比坐标，与尺寸无关，同半径的盒子共用一条，尺寸变化（侧栏开合、流式增高）不必重写；
 *   代价是每次样式重算都要为每个 calc(100% − Npx) 坐标建一次计算值（每个盒子上百个），整页重算时明显更慢。
 *   polygon 只有一条闭合折线，四个薄片经「桥」接到外框上：桥沿盒子的上下边（y = 0 / 100%）水平地去了又回，
 *   水平边不跨扫描线，光栅化时不贡献覆盖，不会留下细缝。
 */
export function sliverClipPath(
	radii: Radii,
	scale: number,
	bleed: number,
	size?: { width: number; height: number },
): string | null {
	const key = `${size ? `${size.width}|${size.height}` : "%"}|${radiiKey(radii)}|${scale}|${bleed}`;
	return remember(clipCache, key, () => {
		const [tl, tr, br, bl] = radii;
		const big = (c: CornerRadius) => Math.min(c.x, c.y) >= MIN_RADIUS;
		if (![tl, tr, br, bl].some(big)) return null;
		if (size) {
			const { width, height } = size;
			const lo = num(-bleed);
			let d = `M${lo} ${lo}H${num(width + bleed)}V${num(height + bleed)}H${lo}Z`;
			if (big(tl)) d += sliverPath(0, 0, 1, 1, tl, scale);
			if (big(tr)) d += sliverPath(width, 0, -1, 1, tr, scale);
			if (big(br)) d += sliverPath(width, height, -1, -1, br, scale);
			if (big(bl)) d += sliverPath(0, height, 1, -1, bl, scale);
			return `path(evenodd, "${d}")`;
		}
		const pts: string[] = [];
		// 一个角：从外框上与该角横边同高的点出发，沿横边走到薄片起点，绕薄片一圈，再原路回到外框
		const corner = (c: CornerRadius, right: boolean, bottom: boolean) => {
			const x = right ? far : near;
			const y = bottom ? far : near;
			const edge = `${x(-bleed)} ${y(0)}`;
			pts.push(edge);
			if (!big(c)) return;
			for (const [u, v] of sliverLoop(c, scale)) pts.push(`${x(u)} ${y(v)}`);
			pts.push(edge);
		};
		corner(tl, false, false);
		pts.push(
			`${near(-bleed)} ${near(-bleed)}`,
			`${far(-bleed)} ${near(-bleed)}`,
		);
		corner(tr, true, false);
		corner(br, true, true);
		pts.push(`${far(-bleed)} ${far(-bleed)}`, `${near(-bleed)} ${far(-bleed)}`);
		corner(bl, false, true);
		return `polygon(evenodd, ${pts.join(", ")})`;
	});
}

/**
 * 超椭圆圆角矩形的完整轮廓（顺时针，从左上角的横边端点起）：盒子 (x, y, width, height)，四角半径 radii。
 * 半径为 0 的角就是直角。
 */
export function superellipsePath(
	x: number,
	y: number,
	width: number,
	height: number,
	radii: Radii,
): string {
	const [tl, tr, br, bl] = radii;
	const r = x + width;
	const b = y + height;
	const pts: string[] = [];
	// 每个角：圆心 (cx, cy)，map 把单位点 (a, b) 映射到页面坐标；a 从 0 到 1 时从上一条边走到下一条边
	const corner = (
		c: CornerRadius,
		vx: number,
		vy: number,
		map: (a: number, b: number) => [number, number],
	) => {
		if (c.x <= 0 || c.y <= 0) {
			pts.push(`${num(vx)} ${num(vy)}`);
			return;
		}
		for (const [ua, ub] of unitCorner(cornerSegments(Math.max(c.x, c.y)))) {
			const [px, py] = map(ua, ub);
			pts.push(`${num(px)} ${num(py)}`);
		}
	};
	corner(tr, r, y, (a, u) => [r - tr.x + tr.x * a, y + tr.y - tr.y * u]);
	corner(br, r, b, (a, u) => [r - br.x + br.x * u, b - br.y + br.y * a]);
	corner(bl, x, b, (a, u) => [x + bl.x - bl.x * a, b - bl.y + bl.y * u]);
	corner(tl, x, y, (a, u) => [x + tl.x - tl.x * u, y + tl.y - tl.y * a]);
	return `M${pts.join("L")}Z`;
}

/** 四条边框宽度：上、右、下、左。 */
export type BorderWidths = readonly [number, number, number, number];

/**
 * 边框的内缘：按规范（§3.9.4.2，K = 1.6 时控制点钳到 1），内缘是同圆心、各角半径减去相邻两边边框宽度的超椭圆；
 * 半径减到 0 以下的角是直角。返回内缘所在的盒子与半径；边框把盒子占满时返回 null。
 */
export function innerContour(
	width: number,
	height: number,
	radii: Radii,
	widths: BorderWidths,
): {
	x: number;
	y: number;
	width: number;
	height: number;
	radii: Radii;
} | null {
	const [t, r, b, l] = widths;
	const w = width - l - r;
	const h = height - t - b;
	if (w <= 0 || h <= 0) return null;
	const [tl, tr, br, bl] = radii;
	const shrink = (c: CornerRadius, dx: number, dy: number): CornerRadius => {
		const x = c.x - dx;
		const y = c.y - dy;
		return x > 0 && y > 0 ? { x, y } : { x: 0, y: 0 };
	};
	return {
		x: l,
		y: t,
		width: w,
		height: h,
		radii: [
			shrink(tl, l, t),
			shrink(tr, r, t),
			shrink(br, r, b),
			shrink(bl, l, b),
		],
	};
}

/** 九宫格环带中段（四条边上可拉伸的部分）在图里的长度（px）：内容沿拉伸方向处处相同，留 2px 免得采样时混进角块。 */
const RING_GAP = 2;

/**
 * 九宫格环带的切片（上、右、下、左，整数 px）：每边取该边两端角的半径与该边边框宽度中的最大者，
 * 角块里装下整段曲线（外缘与内缘），角块之外的边带是等宽直条，可以任意拉伸。取整让角块按 1:1 画、拼缝落在整像素上。
 */
export function ringSlices(radii: Radii, widths: BorderWidths): BorderWidths {
	const [tl, tr, br, bl] = radii;
	const [t, r, b, l] = widths;
	const up = (...v: number[]) =>
		Math.ceil(Math.round(Math.max(...v) * 1000) / 1000);
	return [
		up(tl.y, tr.y, t),
		up(tr.x, br.x, r),
		up(br.y, bl.y, b),
		up(tl.x, bl.x, l),
	];
}

/**
 * 边框环带（外缘超椭圆、内缘按 innerContour，evenodd 填充边框色），作为 border-image 画在原生边框的位置：
 * 背景之上、内容之下。
 *
 * 盒子装得下四个角块时画成与尺寸无关的九宫格（border-image-slice = border-image-width = ringSlices，不带 fill）：
 * 同半径、同边框、同色的盒子共用同一张图，尺寸变化不必重写；sized 为 false。
 * 装不下（角块之和超过边长，如贴边的胶囊形）时退回按边框盒尺寸整张拉伸（sized 为 true，尺寸变了要重写）。
 */
export function ringImage(
	width: number,
	height: number,
	radii: Radii,
	widths: BorderWidths,
	color: string,
): { image: string; sized: boolean } {
	const [t, r, b, l] = ringSlices(radii, widths);
	const nine = l + r <= width && t + b <= height;
	// 几何部分按半径、边框宽度（整张时再加尺寸）缓存，已 URL 编码；颜色单独编码后夹在中间：
	// 主题切换与边框色过渡只换颜色，不必重算路径
	const key = `${nine ? "9" : `${width}|${height}`}|${radiiKey(radii)}|${widths.join(",")}`;
	const [head, tail] = remember(ringCache, key, () => {
		const w = nine ? l + RING_GAP + r : width;
		const h = nine ? t + RING_GAP + b : height;
		let d = superellipsePath(0, 0, w, h, radii);
		const inner = innerContour(w, h, radii, widths);
		if (inner)
			d += superellipsePath(
				inner.x,
				inner.y,
				inner.width,
				inner.height,
				inner.radii,
			);
		const slice = nine
			? `${t} ${r} ${b} ${l} / ${t}px ${r}px ${b}px ${l}px / 0 stretch`
			: "0 fill / 0 / 0 stretch";
		return [
			encodeURIComponent(
				`<svg xmlns="http://www.w3.org/2000/svg" width="${num(w)}" height="${num(h)}" viewBox="0 0 ${num(w)} ${num(h)}" preserveAspectRatio="none"><path fill-rule="evenodd" style="fill:`,
			),
			`${encodeURIComponent(`" d="${d}"/></svg>`)}") ${slice}`,
		] as const;
	});
	return {
		image: `url("data:image/svg+xml,${head}${encodeURIComponent(color)}${tail}`,
		sized: !nine,
	};
}
