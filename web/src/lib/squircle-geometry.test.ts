/**
 * 超椭圆几何：采样点落在 |x|^n + |y|^n = 1（n = 2^1.6）上；包络圆系数让圆角恰好包住超椭圆、在对角线上相切；
 * 半径按 CSS 规则等比缩小；四角半径可以各不相同；薄片裁剪（与尺寸无关的 polygon）与九宫格边框环带。
 */
import { describe, expect, it } from "vitest";
import {
	type CornerRadius,
	cornerSegments,
	EXPONENT,
	innerContour,
	type Radii,
	ROUND_SCALE,
	ringImage,
	ringSlices,
	SUPERELLIPSE_K,
	sliverClipPath,
	superellipsePath,
	unitCorner,
	usedRadii,
} from "./squircle-geometry";

const r = (x: number, y = x): CornerRadius => ({ x, y });
const all = (v: number): Radii => [r(v), r(v), r(v), r(v)];

/** 把 SVG 路径里的坐标对按出现顺序取出（只含 M / L / H / V / A / Z 的路径）。 */
function points(d: string): Array<[number, number]> {
	const out: Array<[number, number]> = [];
	for (const m of d.matchAll(/[ML](-?[\d.]+) (-?[\d.]+)/g)) {
		out.push([Number(m[1]), Number(m[2])]);
	}
	return out;
}

/** 单位超椭圆角上参数 θ 处的精确点。 */
function exact(theta: number): [number, number] {
	const p = 2 / EXPONENT;
	return [Math.sin(theta) ** p, Math.cos(theta) ** p];
}

describe("超椭圆参数", () => {
	it("n = 2^K，K = 1.6", () => {
		expect(SUPERELLIPSE_K).toBe(1.6);
		expect(EXPONENT).toBeCloseTo(3.0314, 4);
	});

	it("单位角的采样点满足 a^n + b^n = 1，首尾落在两条边上", () => {
		for (const segments of [6, 12, 24, 96]) {
			const pts = unitCorner(segments);
			expect(pts).toHaveLength(segments + 1);
			expect(pts[0]).toEqual([0, 1]);
			expect(pts.at(-1)).toEqual([1, 0]);
			for (const [a, b] of pts) {
				expect(a ** EXPONENT + b ** EXPONENT).toBeCloseTo(1, 12);
			}
		}
	});

	it("折线与真实曲线的弦高误差：半径 ≤ 800px 时小于 0.04px", () => {
		for (const radius of [3.5, 5, 8, 12, 18, 24, 36, 48, 96, 200, 400, 800]) {
			const n = cornerSegments(radius);
			let worst = 0;
			for (let i = 0; i < n; i++) {
				const [a0, b0] = exact((i / n) * (Math.PI / 2));
				const [a1, b1] = exact(((i + 1) / n) * (Math.PI / 2));
				for (let k = 1; k < 16; k++) {
					const [a, b] = exact(((i + k / 16) / n) * (Math.PI / 2));
					const dx = a1 - a0;
					const dy = b1 - b0;
					const t = Math.max(
						0,
						Math.min(1, ((a - a0) * dx + (b - b0) * dy) / (dx * dx + dy * dy)),
					);
					worst = Math.max(
						worst,
						Math.hypot(a - a0 - t * dx, b - b0 - t * dy) * radius,
					);
				}
			}
			expect(worst, `r=${radius}`).toBeLessThan(0.04);
		}
	});
});

describe("包络圆系数 ROUND_SCALE", () => {
	it("半径 k·r 的圆角包住半径 r 的超椭圆，并在 45° 对角线上相切", () => {
		const radius = 100;
		const k = ROUND_SCALE * radius;
		// 以角的顶点为原点、指向盒子内部为正：圆角切掉的是 u < k、v < k 且离圆心 (k, k) 超过 k 的区域
		let minGap = Number.POSITIVE_INFINITY;
		for (const [a, b] of unitCorner(2000)) {
			const u = radius - radius * a;
			const v = radius - radius * b;
			const inCircleCorner = u < k && v < k;
			const gap = inCircleCorner ? k - Math.hypot(u - k, v - k) : 0;
			// 超椭圆上的点都在圆角形状之内（gap ≥ 0），允许浮点误差
			expect(gap).toBeGreaterThan(-1e-9);
			if (inCircleCorner) minGap = Math.min(minGap, gap);
		}
		expect(minGap).toBeLessThan(1e-3);
		// 对角线上两条曲线离边的距离相等
		const diag = radius * (1 - 2 ** (-1 / EXPONENT));
		expect(k * (1 - Math.SQRT1_2)).toBeCloseTo(diag, 10);
	});

	it("index.css 降级段落把正圆与胶囊的 --sq-s 设为 1（超椭圆表面用注册的初始值 ROUND_SCALE）", async () => {
		// vitest 会把 CSS 导入（含 ?raw）处理成空串，这里直接读源文件（vitest 的工作目录是 web/）；
		// 前端 tsconfig 不带 Node 类型，只声明用到的函数
		const fs = (await import("node:fs" as string)) as {
			readFileSync(path: string, encoding: "utf8"): string;
		};
		const indexCss = fs.readFileSync("src/index.css", "utf8");
		expect(indexCss).toMatch(
			/:root\[data-squircle\] \[class\*="rounded-full"\]::after\s*\{\s*--sq-s:\s*1;/,
		);
	});
});

describe("usedRadii：相邻半径之和超过边长时等比缩小", () => {
	it("放得下时原样返回", () => {
		const radii = all(12);
		expect(usedRadii(100, 40, radii)).toEqual({ radii, factor: 1 });
	});

	it("取各边 边长 / 半径和 的最小值，四角一起乘", () => {
		const { radii, factor } = usedRadii(100, 40, all(36));
		expect(factor).toBeCloseTo(40 / 72, 12);
		for (const c of radii) {
			expect(c.x).toBeCloseTo(20, 10);
			expect(c.y).toBeCloseTo(20, 10);
		}
	});

	it("四角不同半径、椭圆半径：由最挤的一条边决定", () => {
		const input: Radii = [r(60, 10), r(50, 30), r(0), r(20, 40)];
		// 上边 60+50=110 > 100 → 0.909；右边 30+0；下边 0+20；左边 40+10=50 ≤ 60
		const { radii, factor } = usedRadii(100, 60, input);
		expect(factor).toBeCloseTo(100 / 110, 12);
		expect(radii[0]).toEqual({ x: 60 * factor, y: 10 * factor });
		expect(radii[3]).toEqual({ x: 20 * factor, y: 40 * factor });
		expect(radii[2]).toEqual({ x: 0, y: 0 });
	});
});

/** 把 polygon() 按盒子尺寸 w × h 解析成点列（坐标只有 0、100%、Npx、calc(100% ± Npx) 四种）。 */
function resolve(poly: string, w: number, h: number): Array<[number, number]> {
	const head = "polygon(evenodd, ";
	expect(poly.startsWith(head)).toBe(true);
	const coord = (s: string, size: number): number => {
		if (s === "0") return 0;
		if (s === "100%") return size;
		const px = s.match(/^(-?[\d.]+)px$/);
		if (px) return Number(px[1]);
		const calc = s.match(/^calc\(100% ([+-]) ([\d.]+)px\)$/);
		if (!calc) throw new Error(`坐标格式不对：${s}`);
		return size + (calc[1] === "+" ? 1 : -1) * Number(calc[2]);
	};
	return poly
		.slice(head.length, -1)
		.split(", ")
		.map((p) => {
			const m = p.match(/^(calc\([^)]*\)|\S+) (calc\([^)]*\)|\S+)$/);
			if (!m) throw new Error(`点格式不对：${p}`);
			return [coord(m[1] ?? "", w), coord(m[2] ?? "", h)];
		});
}

/** evenodd 填充规则下点是否在多边形内（射线法）。 */
function inside(pts: Array<[number, number]>, x: number, y: number): boolean {
	let odd = false;
	for (let i = 0, j = pts.length - 1; i < pts.length; j = i++) {
		const [xi, yi] = pts[i] as [number, number];
		const [xj, yj] = pts[j] as [number, number];
		if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi)
			odd = !odd;
	}
	return odd;
}

/**
 * 左上角附近 (u, v)（离两条边的距离）应不应该保留：只有「原生圆角（半径 k·r 的圆）以内、超椭圆以外」的薄片被剪掉；
 * 圆外的角尖（投影所在）与超椭圆以内都保留。离任一条曲线不到 0.15px 的点（折线近似的误差带内）算不准，返回 null。
 */
function expectedKeep(u: number, v: number, radius: number): boolean | null {
	const a = radius * ROUND_SCALE;
	const inCircle = (x: number, y: number) =>
		x >= a || y >= a || (a - x) ** 2 + (a - y) ** 2 <= a * a;
	const inSquircle = (x: number, y: number) =>
		x >= radius ||
		y >= radius ||
		((radius - x) / radius) ** EXPONENT + ((radius - y) / radius) ** EXPONENT <=
			1;
	for (const test of [inCircle, inSquircle]) {
		const here = test(u, v);
		for (const [dx, dy] of [
			[0.15, 0.15],
			[0.15, -0.15],
			[-0.15, 0.15],
			[-0.15, -0.15],
		] as const) {
			if (test(u + dx, v + dy) !== here) return null;
		}
	}
	return u < 0 || v < 0 || !inCircle(u, v) || inSquircle(u, v);
}

describe("sliverClipPath：外扩矩形挖掉四角薄片", () => {
	it("evenodd 的 polygon，只用百分比与 px 坐标", () => {
		const d = sliverClipPath(all(24), ROUND_SCALE, 12);
		expect(d).toMatch(/^polygon\(evenodd, /);
		// 外框四角
		const pts = resolve(d ?? "", 200, 100);
		for (const corner of [
			[-12, -12],
			[212, -12],
			[212, 112],
			[-12, 112],
		])
			expect(pts).toContainEqual(corner);
	});

	it("任意尺寸下四个角都只剪掉圆与超椭圆之间的薄片（角尖的投影、盒内、外扩区都保留）", () => {
		const radius = 24;
		const bleed = 6;
		const d = sliverClipPath(all(radius), ROUND_SCALE, bleed) ?? "";
		for (const [w, h] of [
			[200, 100],
			[337.5, 61],
		] as const) {
			const pts = resolve(d, w, h);
			let checked = 0;
			// (u, v) 是离该角两条边的距离；四个角按镜像映射到页面坐标
			for (let u = -5; u <= radius + 4; u += 0.37) {
				for (let v = -5; v <= radius + 4; v += 0.41) {
					const keep = expectedKeep(u, v, radius);
					if (keep === null) continue;
					for (const [x, y] of [
						[u, v],
						[w - u, v],
						[w - u, h - v],
						[u, h - v],
					] as const) {
						expect(inside(pts, x, y), `${w}×${h} (${x}, ${y})`).toBe(keep);
						checked++;
					}
				}
			}
			expect(checked).toBeGreaterThan(10_000);
			// 外扩区以内保留，以外剪掉；连到外框的「桥」（沿上下边水平去回）不占面积
			expect(inside(pts, -bleed + 0.5, 0.2)).toBe(true);
			expect(inside(pts, -bleed + 0.5, -0.2)).toBe(true);
			expect(inside(pts, w / 2, -bleed - 0.5)).toBe(false);
		}
	});

	it("各角半径不同（含椭圆角）时每个角按自己的半径剪", () => {
		const d = sliverClipPath([r(40, 20), r(0), r(10), r(3)], ROUND_SCALE, 0);
		const pts = resolve(d ?? "", 160, 90);
		// 左上：超椭圆（横向半径 40、纵向 20）上的一点，往角尖方向一点在薄片里被剪掉，往里一点保留
		const [ua, ub] = unitCorner(cornerSegments(40))[6] as readonly [
			number,
			number,
		];
		const sx = 40 - 40 * ua;
		const sy = 20 - 20 * ub;
		expect(inside(pts, sx - 0.2, sy - 0.2)).toBe(false);
		expect(inside(pts, sx + 0.3, sy + 0.3)).toBe(true);
		// 右上是直角：顶点旁边保留；左下 3px 太小不剪
		expect(inside(pts, 159.9, 0.1)).toBe(true);
		expect(inside(pts, 0.3, 90 - 1.2)).toBe(true);
	});

	it("太小的角不剪；全部太小时返回 null", () => {
		expect(sliverClipPath(all(3), ROUND_SCALE, 0)).toBeNull();
		expect(
			sliverClipPath(all(3), ROUND_SCALE, 0, { width: 50, height: 20 }),
		).toBeNull();
	});

	it("给了尺寸时写成 path()：外框按 bleed 外扩，每个大于阈值的角一片，超椭圆一侧与 polygon 写法逐点相同", () => {
		const radii: Radii = [r(24), r(40, 20), r(3), r(16)];
		const d = sliverClipPath(radii, ROUND_SCALE, 12, {
			width: 200,
			height: 100,
		});
		expect(d).toMatch(/^path\(evenodd, "M-12 -12H212V112H-12Z/);
		// 外框 + 三个角（右下 3px 太小）
		expect(d?.match(/M/g)).toHaveLength(4);
		const poly = resolve(
			sliverClipPath(radii, ROUND_SCALE, 12) ?? "",
			200,
			100,
		);
		const round = (v: number) => Math.round(v * 1000) / 1000;
		const curve = points(d ?? "").filter(
			// 超椭圆上的点：不在盒子边上（横边、竖边上的是弧与薄片的端点）
			([x, y]) => x > 0 && x < 200 && y > 0 && y < 100,
		);
		expect(curve.length).toBeGreaterThan(30);
		for (const [x, y] of curve)
			expect(poly.map(([px, py]) => [round(px), round(py)])).toContainEqual([
				x,
				y,
			]);
	});
});

describe("superellipsePath 与边框环带", () => {
	it("直角留下顶点；圆角的点都落在缩放后的超椭圆上", () => {
		const d = superellipsePath(10, 20, 100, 50, [r(0), r(16), r(0), r(0)]);
		const pts = points(d);
		expect(pts).toContainEqual([10, 20]);
		expect(pts).toContainEqual([110, 70]);
		expect(pts).toContainEqual([10, 70]);
		const corner = pts.filter(([x, y]) => x >= 94 && y <= 36);
		expect(corner.length).toBe(cornerSegments(16) + 1);
		for (const [x, y] of corner) {
			const a = (x - 94) / 16;
			const b = (36 - y) / 16;
			expect(Math.abs(a ** EXPONENT + b ** EXPONENT - 1)).toBeLessThan(5e-4);
		}
	});

	it("内缘：盒子收缩边框宽度，半径减去相邻边框，减到 0 以下变直角", () => {
		const inner = innerContour(
			100,
			60,
			[r(8), r(1), r(8, 2), r(0)],
			[1, 2, 3, 4],
		);
		expect(inner).toEqual({
			x: 4,
			y: 1,
			width: 94,
			height: 56,
			radii: [r(4, 7), r(0), r(0), r(0)],
		});
		expect(innerContour(10, 10, all(4), [5, 5, 5, 5])).toBeNull();
	});

	const svgOf = (image: string) =>
		decodeURIComponent(
			image.slice(image.indexOf(",") + 1, image.indexOf('")')),
		);
	const sizeOf = (svg: string) =>
		(svg.match(/width="([\d.]+)" height="([\d.]+)"/) ?? [])
			.slice(1)
			.map(Number);

	it("切片：每边取两端角的半径与该边边框宽度的最大者，向上取整（浮点误差不进位）", () => {
		expect(
			ringSlices([r(12.2, 6), r(8, 9.0000001), r(0), r(3)], [1, 2, 10, 4]),
		).toEqual([9, 8, 10, 13]);
	});

	it("装得下四个角块时是九宫格：与尺寸无关、同参数共用同一张图，不带 fill", () => {
		const a = ringImage(300, 80, all(16), [1, 1, 1, 1], "rgb(1, 2, 3)");
		const b = ringImage(512.5, 33, all(16), [1, 1, 1, 1], "rgb(1, 2, 3)");
		expect(a.sized).toBe(false);
		expect(b.image).toBe(a.image);
		expect(a.image).toMatch(
			/^url\("data:image\/svg\+xml,[^"]*"\) 16 16 16 16 \/ 16px 16px 16px 16px \/ 0 stretch$/,
		);
		const svg = svgOf(a.image);
		expect(svg).toContain('fill-rule="evenodd"');
		expect(svg).toContain("fill:rgb(1, 2, 3)");
		// 图 = 两个角块 + 2px 可拉伸的中段；外缘 + 内缘两条子路径
		expect(sizeOf(svg)).toEqual([34, 34]);
		expect(svg.match(/M/g)).toHaveLength(2);
	});

	it("九宫格的角块与按整盒画的环带逐点相同（拼起来没有错位）", () => {
		const radii: Radii = [r(20, 12), r(6), r(14), r(0)];
		const widths = [1, 2, 3, 1] as const;
		const nine = ringImage(240, 90, radii, widths, "red");
		const [w, h] = sizeOf(svgOf(nine.image)) as [number, number];
		const inner = innerContour(240, 90, radii, widths);
		expect(inner).not.toBeNull();
		const full = points(
			superellipsePath(0, 0, 240, 90, radii) +
				(inner
					? superellipsePath(
							inner.x,
							inner.y,
							inner.width,
							inner.height,
							inner.radii,
						)
					: ""),
		);
		const tile = points(
			(svgOf(nine.image).match(/ d="([^"]*)"/) ?? [])[1] ?? "",
		);
		const [top, right, bottom, left] = ringSlices(radii, widths);
		// 每个角块里的点，平移到整盒的同一角后应能在整盒环带里找到
		const corners = [
			{ hit: (x: number, y: number) => x <= left && y <= top, dx: 0, dy: 0 },
			{
				hit: (x: number, y: number) => x >= w - right && y <= top,
				dx: 240 - w,
				dy: 0,
			},
			{
				hit: (x: number, y: number) => x >= w - right && y >= h - bottom,
				dx: 240 - w,
				dy: 90 - h,
			},
			{
				hit: (x: number, y: number) => x <= left && y >= h - bottom,
				dx: 0,
				dy: 90 - h,
			},
		];
		const round = (v: number) => Math.round(v * 1000) / 1000;
		for (const c of corners) {
			const mine = tile.filter(([x, y]) => c.hit(x, y));
			expect(mine.length).toBeGreaterThan(0);
			for (const [x, y] of mine)
				expect(full).toContainEqual([round(x + c.dx), round(y + c.dy)]);
		}
	});

	it("装不下角块（贴边的胶囊形）时按整盒拉伸，sized 为真", () => {
		const v = ringImage(31, 31, all(15.5), [1, 1, 1, 1], "red");
		expect(v.sized).toBe(true);
		expect(v.image).toMatch(/"\) 0 fill \/ 0 \/ 0 stretch$/);
		expect(sizeOf(svgOf(v.image))).toEqual([31, 31]);
	});

	it("换颜色只换 fill，路径与切片不变", () => {
		const a = svgOf(ringImage(80, 30, all(10), [1, 1, 1, 1], "red").image);
		const b = svgOf(
			ringImage(80, 30, all(10), [1, 1, 1, 1], "rgb(0 0 0 / 50%)").image,
		);
		expect(b).toContain("fill:rgb(0 0 0 / 50%)");
		expect(b.replace("rgb(0 0 0 / 50%)", "red")).toBe(a);
	});
});

describe("缓存：同参数复用同一结果", () => {
	it("unitCorner 按段数返回同一份采样点", () => {
		expect(unitCorner(24)).toBe(unitCorner(24));
	});

	it("clip-path 同参数结果一致，参数变了结果跟着变", () => {
		const a = sliverClipPath(all(18), ROUND_SCALE, 4);
		expect(sliverClipPath(all(18), ROUND_SCALE, 4)).toBe(a);
		expect(sliverClipPath(all(19), ROUND_SCALE, 4)).not.toBe(a);
		expect(sliverClipPath(all(18), ROUND_SCALE, 5)).not.toBe(a);
	});

	it("环带：九宫格与整盒拉伸的缓存互不串用", () => {
		const nine = ringImage(100, 100, all(20), [1, 1, 1, 1], "red");
		const full = ringImage(39, 39, all(19.5), [1, 1, 1, 1], "red");
		expect(nine.sized).toBe(false);
		expect(full.sized).toBe(true);
		expect(ringImage(100, 100, all(20), [1, 1, 1, 1], "red").image).toBe(
			nine.image,
		);
	});
});
