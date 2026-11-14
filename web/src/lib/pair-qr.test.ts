/** 配对二维码：内容格式、扫码内容解析、模块矩阵合并成横向段。 */
import { describe, expect, it } from "vitest";
import { pairUri, qrModules, scannedServer } from "./pair-qr";

describe("pairUri / scannedServer", () => {
	it("server 参数整体 URL 编码", () => {
		expect(pairUri("https://oryn.example.com:8443")).toBe(
			"oryn://pair?server=https%3A%2F%2Foryn.example.com%3A8443",
		);
	});

	it("oryn://pair 取 server；普通地址原样（去首尾空白）；缺参数原样", () => {
		expect(scannedServer(pairUri("https://a.example"))).toBe(
			"https://a.example",
		);
		expect(scannedServer(" https://b.example ")).toBe("https://b.example");
		expect(scannedServer("oryn://pair")).toBe("oryn://pair");
	});
});

describe("qrModules", () => {
	it("段都在码区内、互不重叠；左上角定位图形第一行是 7 个连续深色模块", () => {
		const { size, runs } = qrModules(pairUri("https://oryn.example.com"));
		expect(size).toBeGreaterThanOrEqual(21);
		expect((size - 17) % 4).toBe(0);
		const seen = new Set<string>();
		for (const r of runs) {
			expect(r.w).toBeGreaterThan(0);
			expect(r.x + r.w).toBeLessThanOrEqual(size);
			expect(r.y).toBeLessThan(size);
			for (let x = r.x; x < r.x + r.w; x++) {
				const k = `${x},${r.y}`;
				expect(seen.has(k)).toBe(false);
				seen.add(k);
			}
		}
		expect(runs.find((r) => r.y === 0 && r.x === 0)).toEqual({
			x: 0,
			y: 0,
			w: 7,
		});
		// 同一行相邻段之间至少隔一个浅色模块（已合并）
		const row0 = runs.filter((r) => r.y === 0);
		for (let i = 1; i < row0.length; i++) {
			const prev = row0[i - 1];
			const cur = row0[i];
			if (prev && cur) expect(cur.x).toBeGreaterThan(prev.x + prev.w);
		}
	});

	it("同样的内容得到同样的结果", () => {
		const uri = pairUri("https://x.example");
		expect(qrModules(uri)).toEqual(qrModules(uri));
	});
});
