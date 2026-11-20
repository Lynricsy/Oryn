/**
 * M20 阶段 1：探针页（sink-spike.html）在本机 Chromium 里的逻辑自检（ci/ios-sink-spike 分支专用，不合入）。
 * 393×852、DPR 3、触摸；触摸经 CDP Input.dispatchTouchEvent 逐步移动。只说明页面逻辑与网页侧机制成立，
 * iOS 上 UIScrollView 的行为由 SinkSpikeTests.swift 在 ios-check 里验证。
 * 用法：cd web && bun src-tauri/ci/ios-e2e/spike/local-check.ts
 * 环境变量：CHROMIUM=<可执行文件>（默认 /usr/bin/chromium）。
 */
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { chromium } from "playwright-core";

const page_url = pathToFileURL(join(import.meta.dir, "sink-spike.html")).href;
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium" });
const context = await browser.newContext({
	viewport: { width: 393, height: 852 },
	deviceScaleFactor: 3,
	isMobile: true,
	hasTouch: true,
});
const page = await context.newPage();
const cdp = await context.newCDPSession(page);
let failed = 0;

type Reading = Record<string, number>;
const read = async (): Promise<Reading> => {
	const text = (await page.locator("#status").textContent()) ?? "";
	const out: Reading = {};
	for (const part of text.split(";")) {
		const [k, v] = part.split("=");
		if (k && v !== undefined && !Number.isNaN(Number(v))) out[k] = Number(v);
	}
	return out;
};
const sleep = (ms: number) => Bun.sleep(ms);
const near = (a: number, b: number, tol: number) => Math.abs(a - b) <= tol;
/** 等到条件成立（最多 ms），返回最后一次读数 */
const until = async (ms: number, cond: (r: Reading) => boolean): Promise<Reading> => {
	const end = Date.now() + ms;
	let r = await read();
	while (!cond(r) && Date.now() < end) {
		await sleep(50);
		r = await read();
	}
	return r;
};
/** 静止：sx 连续 300ms 不变 */
const still = async (ms = 3000): Promise<Reading> => {
	const end = Date.now() + ms;
	let prev = await read();
	let since = Date.now();
	while (Date.now() < end) {
		await sleep(50);
		const r = await read();
		if (r.sx !== prev.sx) since = Date.now();
		else if (Date.now() - since >= 300) return r;
		prev = r;
	}
	return prev;
};
const probe = (name: string, pass: boolean, detail: string) => {
	if (!pass) failed++;
	console.log(`${name} ${pass ? "PASS" : "FAIL"}: ${detail}`);
};
/** 慢速拖动：每 16ms 走 step px，到终点按住 hold ms 再松手 */
const drag = async (x0: number, y0: number, x1: number, y1: number, step = 4, hold = 300) => {
	const touch = (type: string, x: number, y: number) =>
		cdp.send("Input.dispatchTouchEvent", {
			type,
			touchPoints: type === "touchEnd" ? [] : [{ x, y }],
		});
	await touch("touchStart", x0, y0);
	const dist = Math.hypot(x1 - x0, y1 - y0);
	const steps = Math.max(1, Math.ceil(dist / step));
	for (let i = 1; i <= steps; i++) {
		await touch("touchMove", x0 + ((x1 - x0) * i) / steps, y0 + ((y1 - y0) * i) / steps);
		await sleep(16);
	}
	await sleep(hold);
	await touch("touchEnd", x1, y1);
};
const box = async (sel: string) => {
	const b = await page.locator(sel).boundingBox();
	if (!b) throw new Error(`没有 ${sel} 的矩形`);
	return b;
};
const fmt = (r: Reading) => JSON.stringify(r);

try {
	await page.goto(page_url);
	await sleep(300);

	// S1
	let r = await read();
	const W = r.max;
	probe(
		"S1",
		W > 0 && near(r.sx, W, 0.5) && r.sy === 0 && r.vs === 1 && r.iw === 393,
		fmt(r),
	);

	// S2
	const before = {
		open: await box("#open"),
		badge: await box("#badge"),
		status: await box("#status"),
		close: await box("#close"),
	};
	await page.locator("#open").tap();
	r = await until(2000, (x) => near(x.sx, 0, 0.5));
	const after = {
		open: await box("#open"),
		badge: await box("#badge"),
		status: await box("#status"),
		close: await box("#close"),
	};
	const shift = (k: keyof typeof before) => after[k].x - before[k].x;
	probe(
		"S2",
		near(r.sx, 0, 0.5) &&
			near(shift("open"), W, 1) &&
			near(shift("badge"), W, 1) &&
			near(shift("status"), 0, 1) &&
			near(shift("close"), 0, 1),
		`${fmt(r)} open+${shift("open")} badge+${shift("badge")} status+${shift("status")} close+${shift("close")}`,
	);

	// S3
	await page.locator("#close").tap();
	r = await until(2000, (x) => near(x.sx, W, 0.5));
	probe("S3", near(r.sx, W, 0.5), fmt(r));

	// S4：关闭态卡片中部慢拖 0.3W、0.7W；打开态在侧栏上左拖 0.7W
	const y = 620;
	await drag(60, y, 60 + 0.3 * W, y);
	const a = await still();
	await drag(60, y, 60 + 0.7 * W, y);
	const b = await still();
	await drag(0.85 * W, y, 0.15 * W, y);
	const c = await still();
	probe(
		"S4",
		near(a.sx, W, 0.5) && near(b.sx, 0, 0.5) && near(c.sx, W, 0.5),
		`0.3W→${a.sx} 0.7W→${b.sx} 左拖→${c.sx}`,
	);

	// S6：纵向滚动区上滑 300
	const n0 = (await read()).n;
	const vsBox = await box("#vs");
	await drag(vsBox.x + vsBox.width / 2, 360, vsBox.x + vsBox.width / 2, 60, 6, 100);
	r = await still();
	probe("S6", r.vst > 0 && near(r.sx, W, 0.5) && r.n === n0, `${fmt(r)} n0=${n0}`);

	// S7：pre 上左滑；再在最左处右滑（第二步只记录）
	const preBox = await box("#pre");
	const py = preBox.y + preBox.height / 2;
	await drag(300, py, 100, py);
	r = await still();
	probe("S7a", r.pre > 0 && near(r.sx, W, 0.5), fmt(r));
	await page.locator("#reset").tap();
	await drag(100, py, 300, py);
	r = await still();
	console.log(`S7b INFO: 最左处右滑后 sx=${r.sx}（${fmt(r)}）`);
} finally {
	await browser.close();
}
process.exit(failed ? 1 : 0);
