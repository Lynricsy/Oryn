#!/usr/bin/env bun
import { realpathSync } from "node:fs";
/**
 * M20 验收（docs/plan-m20.md 步骤 3.2）：下沉侧栏的根滚动模式在浏览器里的网页侧行为。
 * 起 `VITE_MOCK=1` 的 vite，本机 Chromium 393×852、触摸；`addInitScript` 注入 iOS 壳的 `__ORYN_HOST__`（bridge 8）
 * 与记录全部调用、一律应答的 `__TAURI_INTERNALS__.invoke`。假壳代做原生程序化开合与松手落点，不给产品页面加 CSS 吸附。
 * 触摸经 CDP `Input.dispatchTouchEvent` 逐步移动（`synthesizeScrollGesture` 在这里不产生滚动）。
 * iOS 上 UIScrollView 的拖动、吸附与原生跟随由 ios-check 的端到端覆盖，这里只证明网页侧的同步、守卫与回退。
 *
 * 用法（web/ 目录）：bun run accept:m20
 * 逐项打印 `N<n> PASS|FAIL：<证据>`，有 FAIL 时退出码 1。环境变量：CHROMIUM=<可执行文件>（默认 /usr/bin/chromium）。
 */
import {
	type Browser,
	type CDPSession,
	chromium,
	type Page,
} from "playwright-core";
import { createServer } from "vite";
import { MAIN_THREAD } from "../src/mock/fixtures";
import { type BrowserNetns, startBrowserNetns } from "./browser-netns";

const PORT = 5193;
const BASE = `http://127.0.0.1:${PORT}`;
const VIEW = { width: 393, height: 852 };

interface Call {
	cmd: string;
	args: unknown;
}
interface State {
	sx: number;
	max: number;
	cardLeft: number;
	open: boolean;
	sideInert: boolean;
	native: boolean;
}

declare global {
	interface Window {
		__m20Calls?: Call[];
	}
}

let failed = 0;
function report(id: string, pass: boolean, detail: string) {
	if (!pass) failed++;
	console.log(`${id} ${pass ? "PASS" : "FAIL"}：${detail}`);
}

const sleep = (ms: number) => Bun.sleep(ms);

/**
 * 假 iOS 壳：bridge 可选；invoke 记录调用，其余一律应答。`host_sink_show` 照原生 SinkFollow 的做法逐帧落位
 * （260ms、ease-out 三次，每帧一次 instant scrollTo）。
 */
function shellScript(bridge: number): string {
	return `(() => {
	window.__ORYN_HOST__ = { kind: "tauri", platform: "ios", version: "0", bridge: ${bridge} };
	const calls = [];
	window.__m20Calls = calls;
	let anim = 0;
	const show = (open, animated) => {
		cancelAnimationFrame(anim);
		const max = document.documentElement.scrollWidth - innerWidth;
		const from = scrollX;
		const to = open ? 0 : max;
		if (!animated) return scrollTo({ left: to, behavior: "instant" });
		const start = performance.now();
		const step = (now) => {
			const t = Math.min((now - start) / 260, 1);
			scrollTo({ left: from + (to - from) * (1 - (1 - t) ** 3), behavior: "instant" });
			if (t < 1) anim = requestAnimationFrame(step);
		};
		anim = requestAnimationFrame(step);
	};
	window.__TAURI_INTERNALS__ = {
		invoke: async (cmd, args) => {
			calls.push({ cmd, args: args ?? null });
			if (cmd === "host_ready") return { bridge: ${bridge}, visible: true, pendingPath: null };
			if (cmd === "host_sink_show") show(args.open, args.animated);
			return null;
		},
	};
})();`;
}

/** 原生页头显示调用的透明度（不是 host_header_show 时为 undefined）。 */
function alphaOf(c: Call): number | undefined {
	if (c.cmd !== "host_header_show") return undefined;
	const a = c.args;
	if (!a || typeof a !== "object" || !("show" in a)) return undefined;
	const s = a.show;
	if (!s || typeof s !== "object" || !("alpha" in s)) return undefined;
	return typeof s.alpha === "number" ? s.alpha : undefined;
}

/** 在调用记录里插一条步骤标记（N7 的证据按步骤分段）。 */
const mark = (page: Page, step: string) =>
	page.evaluate((s) => {
		window.__m20Calls?.push({ cmd: `#${s}`, args: null });
	}, step);

/**
 * 页面里求值。返回键弹出侧栏的历史标记是同文档的 traverse，Chromium 仍会让正在进行的 evaluate 报
 * 「Execution context was destroyed」：这种情况稍等重试（真的换了文档时最后一次照样抛出）。
 */
async function evaluate<T>(page: Page, fn: () => T): Promise<T> {
	for (let i = 0; ; i++) {
		try {
			return await page.evaluate(fn);
		} catch (e) {
			if (i >= 5 || !String(e).includes("Execution context was destroyed"))
				throw e;
			await sleep(50);
		}
	}
}

function state(page: Page): Promise<State> {
	return evaluate(page, () => {
		const root = document.documentElement;
		const card = document.querySelector<HTMLElement>(
			'[data-testid="sink-card"]',
		);
		const side = document.querySelector<HTMLElement>(
			'[data-testid="sink-sidebar"]',
		);
		return {
			sx: Math.round(window.scrollX),
			max: root.scrollWidth - window.innerWidth,
			cardLeft: Math.round(card?.getBoundingClientRect().left ?? Number.NaN),
			open: card?.hasAttribute("data-open") ?? false,
			sideInert: side?.inert ?? true,
			native: root.hasAttribute("data-sink-native"),
		};
	});
}

const calls = (page: Page): Promise<Call[]> =>
	evaluate(page, () => window.__m20Calls ?? []);

/** 等条件成立（最多 ms），返回最后一次读数。 */
async function until(
	page: Page,
	ms: number,
	cond: (s: State) => boolean,
): Promise<State> {
	const end = Date.now() + ms;
	let s = await state(page);
	while (!cond(s) && Date.now() < end) {
		await sleep(30);
		s = await state(page);
	}
	return s;
}

/** 慢速拖动：每 16ms 走 4px，到终点按住 hold ms 再松手。 */
async function drag(
	cdp: CDPSession,
	from: [number, number],
	to: [number, number],
	hold = 200,
) {
	const send = (type: string, x: number, y: number) =>
		cdp.send("Input.dispatchTouchEvent", {
			type,
			touchPoints: type === "touchEnd" ? [] : [{ x, y }],
		});
	const [x0, y0] = from;
	const [x1, y1] = to;
	await send("touchStart", x0, y0);
	const steps = Math.max(1, Math.ceil(Math.hypot(x1 - x0, y1 - y0) / 4));
	for (let i = 1; i <= steps; i++) {
		await send(
			"touchMove",
			x0 + ((x1 - x0) * i) / steps,
			y0 + ((y1 - y0) * i) / steps,
		);
		await sleep(16);
	}
	await sleep(hold);
	await send("touchEnd", x1, y1);
}

/** 用 Chromium 的真实拖动触发根滚动；松手后仅代替 UIKit 选落点，不代替网页提交开合状态。 */
async function dragNative(
	page: Page,
	cdp: CDPSession,
	from: [number, number],
	to: [number, number],
): Promise<boolean> {
	const before = (await state(page)).sx;
	await drag(cdp, from, to);
	return page.evaluate((start) => {
		const max = document.documentElement.scrollWidth - innerWidth;
		const x = scrollX;
		if (Math.abs(x - start) < 1 || x <= 0 || x >= max) return false;
		scrollTo({ left: x <= max / 2 ? 0 : max, behavior: "smooth" });
		return true;
	}, before);
}

async function openPage(browser: Browser, bridge: number, path: string) {
	const context = await browser.newContext({
		viewport: VIEW,
		deviceScaleFactor: 3,
		isMobile: true,
		hasTouch: true,
		locale: "zh-CN",
	});
	await context.addInitScript(shellScript(bridge));
	const page = await context.newPage();
	page.on("pageerror", (e) => console.warn(`[page] ${e.message}`));
	await page.goto(`${BASE}${path}`, { waitUntil: "networkidle" });
	const cdp = await context.newCDPSession(page);
	return { context, page, cdp };
}

/** 侧栏宽（= 打开时卡片左缘）。 */
const sinkW = (page: Page) =>
	page.evaluate(
		() =>
			document
				.querySelector('[data-testid="sink-sidebar"]')
				?.getBoundingClientRect().width ?? 0,
	);

/** 任一调用的参数里（任意深度）出现被删掉的旧字段。 */
function staleKeys(list: Call[]): string[] {
	const out: string[] = [];
	const walk = (v: unknown, cmd: string) => {
		if (!v || typeof v !== "object") return;
		for (const [k, w] of Object.entries(v)) {
			if (k === "dx" || k === "passive" || k === "at") out.push(`${cmd}.${k}`);
			walk(w, cmd);
		}
	};
	for (const c of list) walk(c.args, c.cmd);
	return out;
}

async function run(browser: Browser) {
	// N1：首页接管
	{
		const { context, page } = await openPage(browser, 8, "/");
		const s = await until(page, 10_000, (s) => s.native && s.sx === s.max);
		const w = Math.round(await sinkW(page));
		const list = await calls(page);
		const sinkAt = list.findIndex(
			(c) =>
				c.cmd === "host_sink_set" &&
				(c.args as { on?: boolean } | null)?.on === true,
		);
		const barAt = list.findIndex((c) => c.cmd === "host_tab_bar_set");
		report(
			"N1",
			s.native &&
				s.sx === s.max &&
				Math.abs(s.max - w) <= 1 &&
				s.cardLeft === 0 &&
				sinkAt >= 0 &&
				barAt > sinkAt,
			`data-sink-native=${s.native} scrollX=${s.sx} max=${s.max} 侧栏宽=${w} 卡片左缘=${s.cardLeft}；host_sink_set{on:true} 第 ${sinkAt} 条，首条 host_tab_bar_set 第 ${barAt} 条`,
		);
		await context.close();
	}

	// N2–N7：线程页
	{
		const { context, page, cdp } = await openPage(
			browser,
			8,
			`/t/${MAIN_THREAD}`,
		);
		await until(page, 10_000, (s) => s.native && s.sx === s.max);
		// 原生页头淡入后再开始数（之后开合不应再改页头的显示状态）
		const shown = async () =>
			(await calls(page)).filter((c) => c.cmd === "host_header_show");
		const t0 = Date.now();
		while (
			!(await shown()).some((c) => alphaOf(c) === 1) &&
			Date.now() - t0 < 10_000
		)
			await sleep(50);
		await sleep(500);
		const shows0 = (await shown()).length;
		const W = await sinkW(page);

		// N2：点页头的侧栏钮（原生接管时网页钮透明但仍在原位）
		await mark(page, "N2");
		await page.locator('[data-native-slot="lead"]').click({ force: true });
		const n2 = await until(page, 1000, (s) => s.sx === 0 && !s.sideInert);
		report(
			"N2",
			n2.sx === 0 && !n2.sideInert,
			`点击后 1s 内 scrollX=${n2.sx}，侧栏 inert=${n2.sideInert}`,
		);
		// 关回去
		await page.keyboard.press("Escape");
		await until(page, 2000, (s) => s.sx === s.max && !s.open);
		await sleep(300);

		// N3：关闭态慢拖 30% 弹回；慢拖 70% 打开。提交延迟在页面里量：最后一次到达 0 到卡片带上 data-open
		await mark(page, "N3");
		const y = 420;
		const draggedShort = await dragNative(
			page,
			cdp,
			[60, y],
			[60 + 0.3 * W, y],
		);
		const back = await until(page, 2000, (s) => s.sx === s.max);
		await sleep(300);
		const backStill = await state(page);
		await page.evaluate(() => {
			const lag: { reached: number; opened: number } = {
				reached: 0,
				opened: 0,
			};
			Object.assign(window, { __m20Lag: lag });
			addEventListener("scroll", () => {
				if (window.scrollX < 0.5) lag.reached = performance.now();
			});
			const card = document.querySelector('[data-testid="sink-card"]');
			if (card)
				new MutationObserver(() => {
					if (!lag.opened && card.hasAttribute("data-open"))
						lag.opened = performance.now();
				}).observe(card, { attributes: true, attributeFilter: ["data-open"] });
		});
		const draggedLong = await dragNative(page, cdp, [60, y], [60 + 0.7 * W, y]);
		const opened = await until(page, 2000, (s) => s.open);
		const lag = await page.evaluate(() => {
			const l = Reflect.get(window, "__m20Lag");
			return l && typeof l === "object" && "reached" in l && "opened" in l
				? Math.round(Number(l.opened) - Number(l.reached))
				: Number.NaN;
		});
		report(
			"N3",
			draggedShort &&
				draggedLong &&
				back.sx === back.max &&
				backStill.sx === backStill.max &&
				!backStill.open &&
				opened.sx === 0 &&
				opened.open &&
				lag >= 0 &&
				lag <= 150,
			`拖 30%：根有位移=${draggedShort}，scrollX=${backStill.sx}（max ${backStill.max}）data-open=${backStill.open}；拖 70%：根有位移=${draggedLong}，scrollX=${opened.sx}，到 0 后 ${lag}ms data-open=${opened.open}`,
		);

		// N4：打开态 scrollIntoView 被拉回
		await mark(page, "N4");
		await page.evaluate(() =>
			document
				.querySelector('[data-testid="sink-content"] textarea')
				?.scrollIntoView({ block: "nearest" }),
		);
		const moved = await state(page);
		await sleep(300);
		const n4 = await state(page);
		report(
			"N4",
			n4.sx === 0 && n4.open,
			`scrollIntoView 后立即 scrollX=${moved.sx}，300ms 后 scrollX=${n4.sx}，data-open=${n4.open}`,
		);

		// N5：拖开（N3）后 Esc 关上：到关闭位后不再离开（关闭时 history.back() 的同文档遍历不能把根滚动恢复到
		// 拖开时记下的打开位）。减少动效下关闭是一步落位，恢复不会被逐帧动画盖住；之后转录区纵向滑动
		await mark(page, "N5");
		await page.evaluate(() => {
			const bounce = { closed: false, left: -1 };
			Object.assign(window, { __m20Bounce: bounce });
			const max = () =>
				document.documentElement.scrollWidth - window.innerWidth;
			addEventListener("scroll", () => {
				if (window.scrollX >= max() - 0.5) bounce.closed = true;
				else if (bounce.closed && bounce.left < 0)
					bounce.left = Math.round(window.scrollX);
			});
		});
		await page.emulateMedia({ reducedMotion: "reduce" });
		await page.keyboard.press("Escape");
		await until(page, 2000, (s) => s.sx === s.max && !s.open);
		await page.emulateMedia({ reducedMotion: null });
		await sleep(1200);
		const bounced = await page.evaluate(() => {
			const b = Reflect.get(window, "__m20Bounce");
			return b && typeof b === "object" && "left" in b ? Number(b.left) : -2;
		});
		const top0 = await page.evaluate(
			() =>
				document.querySelector('[data-testid="transcript"]')?.scrollTop ?? -1,
		);
		const sx0 = (await state(page)).sx;
		await drag(cdp, [200, 300], [200, 600], 100);
		await sleep(400);
		const top1 = await page.evaluate(
			() =>
				document.querySelector('[data-testid="transcript"]')?.scrollTop ?? -1,
		);
		const n5 = await state(page);
		report(
			"N5",
			bounced === -1 && n5.sx === sx0 && top1 !== top0,
			`关上后离开关闭位：${bounced === -1 ? "无" : `scrollX=${bounced}`}；scrollX ${sx0} -> ${n5.sx}；转录区 scrollTop ${top0} -> ${top1}`,
		);

		// N6：Esc 与返回键
		await mark(page, "N6");
		await page.locator('[data-native-slot="lead"]').click({ force: true });
		await until(page, 2000, (s) => s.sx === 0 && s.open);
		await sleep(200);
		await page.keyboard.press("Escape");
		const esc = await until(page, 2000, (s) => s.sx === s.max && !s.open);
		await sleep(300);
		await page.locator('[data-native-slot="lead"]').click({ force: true });
		await until(page, 2000, (s) => s.sx === 0 && s.open);
		await sleep(200);
		await page.evaluate(() => history.back());
		const backKey = await until(page, 2000, (s) => s.sx === s.max && !s.open);
		report(
			"N6",
			esc.sx === esc.max &&
				!esc.open &&
				backKey.sx === backKey.max &&
				!backKey.open,
			`Esc 后 scrollX=${esc.sx}（max ${esc.max}）；history.back() 后 scrollX=${backKey.sx}`,
		);

		// N7：页头显示状态没动；全程没有旧字段
		await sleep(300);
		const all = await calls(page);
		const shows1 = all.filter((c) => c.cmd === "host_header_show").length;
		const from = all.findIndex((c) => c.cmd === "#N2");
		const trail = all
			.slice(from)
			.filter((c) => c.cmd.startsWith("#") || alphaOf(c) !== undefined)
			.map((c) => (c.cmd.startsWith("#") ? c.cmd : `alpha ${alphaOf(c)}`))
			.join(" ");
		const stale = staleKeys(all);
		report(
			"N7",
			shows1 === shows0 && stale.length === 0,
			`N2–N6 期间 host_header_show ${shows0} -> ${shows1} 条（${trail}）；带 dx / passive / at 的调用：${stale.join(", ") || "无"}`,
		);
		await context.close();
	}

	// N8：旧壳（bridge 7）回退到网页手势与网页部件
	{
		const { context, page, cdp } = await openPage(browser, 7, "/");
		await sleep(1000);
		const s0 = await state(page);
		const W = await sinkW(page);
		await drag(cdp, [4, 420], [4 + 0.7 * W, 420]);
		const s1 = await until(page, 1000, (s) => s.open);
		const used = (await calls(page))
			.map((c) => c.cmd)
			.filter((c) =>
				["host_sink_set", "host_tab_bar_set", "host_header_set"].includes(c),
			);
		report(
			"N8",
			!s0.native && used.length === 0 && s1.open,
			`data-sink-native=${s0.native}；原生部件调用：${used.join(", ") || "无"}；慢拖 70% 后 data-open=${s1.open}`,
		);
		await context.close();
	}
}

// 不开 HMR：检查进行中不要被 vite 客户端整页刷新打断。node_modules 可能是指向别处的软链（git worktree）：
// 把它的真实路径也放进 fs.allow，否则字体等依赖文件 403
process.env.VITE_MOCK = "1";
const WEB = `${import.meta.dir}/..`;
const server = await createServer({
	root: WEB,
	logLevel: "error",
	server: {
		port: PORT,
		strictPort: true,
		host: "127.0.0.1",
		hmr: false,
		fs: { allow: [WEB, realpathSync(`${WEB}/node_modules`)] },
	},
});
await server.listen();
let netns: BrowserNetns | undefined;
try {
	const chromiumPath = process.env.CHROMIUM ?? "/usr/bin/chromium";
	// root 运行时放进只有回环的网络命名空间（同 screenshots.ts：本机 Docker 网络变化会中止在途请求）
	netns =
		process.getuid?.() === 0
			? await startBrowserNetns({
					name: `oryn-m20-${process.pid}`,
					ports: [PORT],
					chromium: chromiumPath,
				})
			: undefined;
	const browser = await chromium.launch({
		executablePath: netns?.executablePath ?? chromiumPath,
	});
	try {
		await run(browser);
	} finally {
		await browser.close();
	}
} finally {
	await server.close();
	netns?.stop();
}
console.log(failed === 0 ? "M20 全部通过" : `M20 有 ${failed} 项失败`);
process.exit(failed === 0 ? 0 : 1);
