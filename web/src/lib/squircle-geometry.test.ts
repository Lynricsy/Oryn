/**
 * 超椭圆几何：n = 2^1.6；包络圆系数让圆角恰好包住超椭圆、在对角线上相切，最大偏差 1.42% 半径；
 * index.css 把正圆与胶囊的系数设回 1。
 */
import { describe, expect, it } from "vitest";
import { EXPONENT, ROUND_SCALE, SUPERELLIPSE_K } from "./squircle-geometry";

/**
 * 半径 r 的超椭圆角上的点：以角的顶点为原点、指向盒子内部为正 (u, v)，从横边 (r, 0) 走到竖边 (0, r)。
 * 按 θ 均匀取单位曲线上的 a = sin(θ)^(2/n)、b = cos(θ)^(2/n)（满足 a^n + b^n = 1）。
 */
function superellipseCorner(
	radius: number,
	samples: number,
): Array<[number, number]> {
	const p = 2 / EXPONENT;
	const out: Array<[number, number]> = [];
	for (let i = 0; i <= samples; i++) {
		const t = (i / samples) * (Math.PI / 2);
		out.push([
			radius - radius * Math.sin(t) ** p,
			radius - radius * Math.cos(t) ** p,
		]);
	}
	return out;
}

/**
 * 角内一点 (u, v) 到半径 k 的原生圆角轮廓的距离：在圆弧区（u、v 都小于 k）是到弧的距离（弧内为正），
 * 否则是到最近那条直边的距离。
 */
function gapToRound(u: number, v: number, k: number): number {
	return u < k && v < k ? k - Math.hypot(u - k, v - k) : Math.min(u, v);
}

describe("超椭圆参数", () => {
	it("n = 2^K，K = 1.6", () => {
		expect(SUPERELLIPSE_K).toBe(1.6);
		expect(EXPONENT).toBeCloseTo(3.0314, 4);
	});
});

describe("包络圆系数 ROUND_SCALE", () => {
	it("半径 k·r 的圆角包住半径 r 的超椭圆，并在 45° 对角线上相切", () => {
		const radius = 100;
		const k = ROUND_SCALE * radius;
		expect(ROUND_SCALE).toBeCloseTo(0.6979, 4);
		let minGap = Number.POSITIVE_INFINITY;
		for (const [u, v] of superellipseCorner(radius, 20_000)) {
			// 超椭圆上的点都在圆角形状之内（gap ≥ 0），允许浮点误差
			const gap = gapToRound(u, v, k);
			expect(gap).toBeGreaterThan(-1e-9);
			if (u < k && v < k) minGap = Math.min(minGap, gap);
		}
		expect(minGap).toBeLessThan(1e-3);
		// 对角线上两条曲线离边的距离相等
		const diag = radius * (1 - 2 ** (-1 / EXPONENT));
		expect(k * (1 - Math.SQRT1_2)).toBeCloseTo(diag, 10);
	});

	it("两条曲线的最大偏差是 1.42% 半径（r = 36px 时约 0.51px）：降级只用包络圆的依据", () => {
		const radius = 36;
		const k = ROUND_SCALE * radius;
		let worst = 0;
		for (const [u, v] of superellipseCorner(radius, 20_000)) {
			worst = Math.max(worst, gapToRound(u, v, k));
		}
		expect(worst / radius).toBeCloseTo(0.0142, 4);
		expect(worst).toBeCloseTo(0.51, 2);
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
