#!/usr/bin/env bun
/**
 * M22 验收（docs/plan-m22.md「步骤 8.1」与「Verification」R1–R21）：统一上下文菜单与选区旁路提问，逐条走界面上能观察到的项。
 * 每条打印 `R<n> PASS|FAIL (原因)`，截图存进 `.dev/m22/`（R1、R2、R13、R20 另存明暗两套主题的截图供人工看图），
 * 结果另写 `.dev/m22/results.json`。全部 PASS 时退出码 0。
 *
 * 用法（web/ 目录）：bun run accept:m22 [-- --only R1,R13] [-- --help]
 *
 * - 脚本起 `VITE_MOCK=1` 的 vite（127.0.0.1:5195，cacheDir `node_modules/.vite-m22`），浏览器内 MockServer 应答一切；
 *   浏览器跑在只有回环的网络命名空间里（见 browser-netns.ts，需要 root）。
 * - 每个场景一个全新浏览器上下文（mock 状态随整页加载重建，场景内只用 SPA 跳转）。
 * - 初始化脚本设 `localStorage["oryn.debug"]="1"`（挂出 `window.__orynMock`：读 btw 记录里发出去的信封问题、往转录里补条目），
 *   并在 window 冒泡阶段记下每次 `contextmenu` 事件是否已被 `preventDefault`（分发器挂在 document 冒泡阶段，先于 window）。
 * - btw 回答的分段间隔由 `localStorage["oryn.mock.btw-delay"]` 控制（R4–R6 设 2000ms，让「在跑」的窗口足够长）。
 * - 夹具里没有的对象（带代码块的助手消息、带图片的用户消息）在场景里经 `__orynMock.appendEntry` 补进主线程。
 * - 触屏长按（R20）用 CDP `Input.dispatchTouchEvent`：touchStart、等 600ms、touchEnd。
 *
 * 环境变量：M22_SHOTS（缺省 <仓库>/.dev/m22）、CHROMIUM（缺省 /usr/bin/chromium）。
 */
import { mkdirSync, openSync, writeFileSync } from "node:fs";
import type { Subprocess } from "bun";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Locator,
	type Page,
} from "playwright-core";
import { type BrowserNetns, startBrowserNetns } from "./browser-netns";

// ---------------------------------------------------------------- 参数

const RS = Array.from({ length: 21 }, (_, i) => `R${i + 1}`);
const USAGE = `用法：bun scripts/m22-accept.ts [--only R1,R13,…] [--help]
  环境变量见文件头注释。`;
if (process.argv.includes("--help") || process.argv.includes("-h")) {
	console.log(USAGE);
	process.exit(0);
}
function argValue(name: string): string | undefined {
	const i = process.argv.indexOf(name);
	return i >= 0 ? process.argv[i + 1] : undefined;
}
const onlyArg = argValue("--only");
const ONLY = onlyArg
	? new Set(
			onlyArg
				.split(",")
				.map((s) => s.trim().toUpperCase())
				.filter(Boolean),
		)
	: null;
if (ONLY && [...ONLY].some((r) => !RS.includes(r))) {
	console.error(`--only 只接受 R1…R21\n${USAGE}`);
	process.exit(2);
}

const ROOT = `${import.meta.dir}/../..`;
const WEB = `${import.meta.dir}/..`;
const SHOTS = process.env.M22_SHOTS ?? `${ROOT}/.dev/m22`;
const CHROMIUM = process.env.CHROMIUM ?? "/usr/bin/chromium";
const RUN = Math.random().toString(36).slice(2, 6);
mkdirSync(SHOTS, { recursive: true });

const PORT = 5195;
const BASE = `http://127.0.0.1:${PORT}`;

// mock 固定数据（web/src/mock/fixtures.ts / server.ts）
const MAIN_THREAD = "T-0193a1b2-0000-7000-8000-00000000a001";
const SHARE_SLUG = "5a0c1e7f3b9d42a8c6e1f0b2d4a69c3e";
/** 主线程最后一条助手消息（FINAL_TEXT）里的两段纯文字，都落在单个文本节点里 */
const PASSAGE_A = "重连后对每个已订阅线程发";
const PASSAGE_B = "由调用方决定是否重发";
const FINAL_MARK = "重连逻辑已改好";
const ASSISTANT_ROW = '[data-index][data-role="assistant"]';
/** 补进主线程的代码块（R10） */
const CODE_SRC =
	"export function backoffMs(n: number): number {\n\treturn Math.min(15_000, 500 * 2 ** n);\n}";
/** 96×64 的小图（同 fixtures.ts 的 ZOO_PNG），补进主线程的用户消息（R11） */
const PNG =
	"iVBORw0KGgoAAAANSUhEUgAAAGAAAABACAIAAABqVuVZAAAB10lEQVR42u3XWVICQQyA4ZzAU3sE76YoqAMMsy+ABxBLH7RmJr0m6SlT9T92N8nHgwjN+KEhQT1cNSQFUqBAoGq4akhQ9VcNSYFMQGV/0ZAUyAjUXTQkKLqLhqRAawO6u39IC+jUngW7cdgkOCEkTiPOBKugEWSCFdGIMEHennmKqPMdz9iQN2eGouv8GNFPDuI61NdDgY7NSBqym8g7rokByT7lAlSPdC2ulMyDxuBQj3TNLpPgm0iwLh1+I1agtTz+B2hfDRTNfsmWF0nfd40PKPrC/wgI+Y0jD5RVA0XT6e1Pel+kWASycqBoZnrrk94XKRZRIAkg+z0t/2sP/JQgoPeyp2g6uv1J74sUiyiQEajoKZqZ3vqk90WKReCt6CmaTu902O8WxSJJAC0Zxf2I1IEsF7A/xgT0euqImi6wlsd/xwoUaw26l1mBiDbh1PkC2p06upb+NqXzoDHY5R1piyuJPmWfGJDrYrHecQba5i11+A9l6uuBcQAZl/SOYXLYHlue4uuwjM0HFNeIbWZ4ObbMBdIwTysA5M0kMqcYkBOT4ITwfGiS6saR1DzJAaWWApmANodGQ4LNvtGQFMgE9LSvNSQFMgJltYYEj1mtISmQAoX1CdOvjQj2s62vAAAAAElFTkSuQmCC";

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();
const clip = (s: string, n = 240) =>
	s.length > n ? `${s.slice(0, n)}…（共 ${s.length} 字）` : s;

async function waitFor<T>(
	what: string,
	fn: () =>
		| Promise<T | undefined | null | false>
		| T
		| undefined
		| null
		| false,
	timeoutMs = 10_000,
	intervalMs = 100,
): Promise<T> {
	const deadline = Date.now() + timeoutMs;
	let last: unknown;
	while (Date.now() < deadline) {
		try {
			const v = await fn();
			if (v) return v as T;
		} catch (e) {
			last = e;
		}
		await sleep(intervalMs);
	}
	throw new Error(
		`等待超时（${timeoutMs}ms）：${what}${last ? `；最后错误：${last instanceof Error ? last.message : String(last)}` : ""}`,
	);
}

// ---------------------------------------------------------------- 结果

type Verdict = "PASS" | "FAIL";
interface Outcome {
	r: string;
	verdict: Verdict;
	reason?: string;
	seconds: number;
	evidence: string[];
}
const outcomes: Outcome[] = [];
let evidence: string[] = [];
let softFails = 0;

const note = (s: string) => {
	evidence.push(s);
	console.log(`    · ${s}`);
};
/** 硬断言：不成立就结束本场景。 */
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};
/** 软断言：记下失败继续走，场景结尾判 FAIL。 */
const expect = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) softFails++;
};

// ---------------------------------------------------------------- 浏览器

let browser: Browser | undefined;
let netns: BrowserNetns | undefined;
const contexts: BrowserContext[] = [];
const pageErrors: string[] = [];
const pageDiag: string[] = [];

async function getBrowser(): Promise<Browser> {
	netns ??= await startBrowserNetns({
		name: `oryn-m22-${RUN}`,
		ports: [PORT],
		chromium: CHROMIUM,
	});
	browser ??= await chromium.launch({
		executablePath: netns.executablePath,
		headless: true,
		args: ["--no-sandbox", "--use-gl=angle", "--use-angle=swiftshader"],
	});
	return browser;
}

/** 调试开关，以及 contextmenu 记录器（window 冒泡阶段：分发器已在 document 上处理过）。 */
const INIT_SCRIPT = `(() => {
	try { localStorage.setItem("oryn.debug", "1"); } catch {}
	const log = [];
	Object.defineProperty(window, "__m22ctx", { value: log });
	window.addEventListener("contextmenu", (e) => {
		const t = e.target instanceof Element ? e.target : null;
		log.push({ prevented: e.defaultPrevented, tag: t ? t.tagName.toLowerCase() : "", shift: e.shiftKey });
	});
})();`;

interface PageOpts {
	width?: number;
	height?: number;
	touch?: boolean;
	scheme?: "light" | "dark";
	/** btw 回答分段间隔（毫秒） */
	btwDelay?: number;
}

async function openPage(o: PageOpts = {}): Promise<Page> {
	const b = await getBrowser();
	const ctx = await b.newContext({
		locale: "zh-CN",
		viewport: { width: o.width ?? 1280, height: o.height ?? 800 },
		colorScheme: o.scheme ?? "light",
		hasTouch: o.touch ?? false,
		isMobile: o.touch ?? false,
		acceptDownloads: true,
	});
	await ctx.grantPermissions(["clipboard-read", "clipboard-write"], {
		origin: BASE,
	});
	await ctx.addInitScript(INIT_SCRIPT);
	if (o.btwDelay !== undefined)
		await ctx.addInitScript(
			`try { localStorage.setItem("oryn.mock.btw-delay", "${o.btwDelay}"); } catch {}`,
		);
	contexts.push(ctx);
	const p = await ctx.newPage();
	p.on("pageerror", (e) =>
		pageErrors.push(`pageerror：${oneLine(e.message).slice(0, 300)}`),
	);
	p.on("console", (m) => {
		if (m.type() === "error")
			pageDiag.push(`console：${oneLine(m.text()).slice(0, 300)}`);
	});
	return p;
}

async function closeAll() {
	for (const c of contexts.splice(0)) await c.close().catch(() => {});
}

async function goto(p: Page, path: string) {
	await p.goto(`${BASE}${path}`, { waitUntil: "domcontentloaded" });
}

/** SPA 内跳转（不重载页面，mock 状态保留）。 */
async function spaGo(p: Page, path: string) {
	await p.evaluate((to) => {
		history.pushState({}, "", to);
		dispatchEvent(new PopStateEvent("popstate", { state: {} }));
	}, path);
}

async function shot(p: Page, name: string) {
	const f = `${SHOTS}/${name}.png`;
	await p.screenshot({ path: f });
	note(`截图 ${f.slice(SHOTS.length + 1)}`);
}

/** 等页面上有限次的动画与过渡（菜单的 pop-in、换主题时的颜色过渡）都放完，截图才是稳态。 */
async function settled(p: Page) {
	await waitFor(
		"动画放完",
		() =>
			p.evaluate(() =>
				document
					.getAnimations()
					.every(
						(a) =>
							a.playState !== "running" ||
							a.effect?.getComputedTiming().iterations === Infinity,
					),
			),
		5_000,
		50,
	);
}

/** 当前页面切到暗色（主题跟随系统，emulateMedia 触发 matchMedia 的 change）后截图，再切回浅色。 */
async function shotBoth(p: Page, name: string) {
	await settled(p);
	await shot(p, `${name}-light`);
	await p.emulateMedia({ colorScheme: "dark" });
	await waitFor(
		"切到暗色主题",
		() => p.evaluate(() => document.documentElement.dataset.theme === "dark"),
		5_000,
	);
	await settled(p);
	await shot(p, `${name}-dark`);
	await p.emulateMedia({ colorScheme: "light" });
	await waitFor(
		"切回浅色主题",
		() => p.evaluate(() => document.documentElement.dataset.theme === "light"),
		5_000,
	);
}

// ---------------------------------------------------------------- 页面里的读写

interface BtwTurn {
	question: string;
	answer: string;
	status: string;
}
interface BtwRec extends BtwTurn {
	id: string;
	followUps?: BtwTurn[];
}
interface MockHandle {
	btwRecords: Map<string, BtwRec[]>;
	threads: Map<string, { id: string; title: string; pinned?: boolean }>;
	appendEntry(threadId: string, message: unknown): void;
}
type W = Window & {
	__orynMock: MockHandle;
	__m22ctx: { prevented: boolean; tag: string; shift: boolean }[];
};

const btwOf = (p: Page, threadId = MAIN_THREAD) =>
	p.evaluate(
		(id) =>
			JSON.parse(
				JSON.stringify(
					(window as unknown as W).__orynMock.btwRecords.get(id) ?? [],
				),
			) as BtwRec[],
		threadId,
	);
const latest = (r: BtwRec): BtwTurn => r.followUps?.at(-1) ?? r;

const ctxLog = (p: Page) =>
	p.evaluate(() => (window as unknown as W).__m22ctx.slice());

interface MenuEntry {
	key: string;
	name: string;
	disabled: boolean;
}
const MENU = '[data-testid="context-menu"]';
const SHEET = '[data-testid="context-sheet"]';
const CARD = '[data-testid="side-ask-card"]';

async function menuEntries(p: Page, host = MENU): Promise<MenuEntry[]> {
	await p.locator(host).waitFor({ timeout: 5_000 });
	return p.evaluate(
		(h) =>
			[...document.querySelectorAll<HTMLElement>(`${h} [data-menu-key]`)].map(
				(el) => ({
					key: el.getAttribute("data-menu-key") ?? "",
					name: (el.textContent ?? "").replace(/\s+/g, " ").trim(),
					disabled:
						(el as HTMLButtonElement).disabled ||
						el.getAttribute("aria-disabled") === "true",
				}),
			),
		host,
	);
}
const keysOf = (es: MenuEntry[]) => es.map((e) => e.key);
const namesOf = (es: MenuEntry[]) => es.map((e) => e.name);

async function clickItem(p: Page, key: string, host = MENU) {
	await p.locator(`${host} [data-menu-key="${key}"]`).click();
}

/** `want` 是否按顺序出现在 `have` 里（中间可夹别的项）。 */
function inOrder(have: string[], want: string[]): boolean {
	let i = 0;
	for (const h of have) if (h === want[i]) i++;
	return i === want.length;
}

interface Box {
	x: number;
	y: number;
	left: number;
	top: number;
	right: number;
	bottom: number;
}

/**
 * 在 `scope` 匹配的元素里找到包含 `needle` 的文本节点，返回它的视口矩形与首字左缘、末字右缘的坐标（供鼠标拖选）；
 * `select` 为真时顺带用 Range 选中它。不滚动页面（滚动见 {@link scrollToText}）。
 */
async function locateText(
	p: Page,
	scope: string,
	needle: string,
	select: boolean,
): Promise<Box & { sx: number; ex: number; y0: number; y1: number }> {
	const r = await p.evaluate(
		({ scope, needle, select }) => {
			for (const root of document.querySelectorAll(scope)) {
				const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
				for (let n = w.nextNode(); n; n = w.nextNode()) {
					const text = n as Text;
					const i = text.data.indexOf(needle);
					if (i < 0) continue;
					const range = document.createRange();
					range.setStart(text, i);
					range.setEnd(text, i + needle.length);
					const b = range.getBoundingClientRect();
					const first = document.createRange();
					first.setStart(text, i);
					first.setEnd(text, i + 1);
					const last = document.createRange();
					last.setStart(text, i + needle.length - 1);
					last.setEnd(text, i + needle.length);
					const f = first.getBoundingClientRect();
					const l = last.getBoundingClientRect();
					if (select) {
						const s = document.getSelection();
						s?.removeAllRanges();
						s?.addRange(range);
					}
					return {
						x: b.left + b.width / 2,
						y: b.top + b.height / 2,
						left: b.left,
						top: b.top,
						right: b.right,
						bottom: b.bottom,
						sx: f.left + 1,
						ex: l.right - 1,
						y0: f.top + f.height / 2,
						y1: l.top + l.height / 2,
					};
				}
			}
			return null;
		},
		{ scope, needle, select },
	);
	if (!r) throw new Error(`页面里找不到文字「${needle}」（${scope}）`);
	return r;
}

/**
 * 像用户一样用滚轮把这段文字滚进视口（程序滚动会被转录的贴底跟随拉回底部），等坐标连续两次相同
 * （mermaid 图、公式还在异步排版）后返回位置；不改选区。
 */
async function scrollToText(p: Page, needle: string) {
	let prev = "";
	return waitFor(
		`「${needle}」滚进视口且位置稳定`,
		async () => {
			const at = await locateText(p, ASSISTANT_ROW, needle, false);
			const vh = p.viewportSize()?.height ?? 800;
			if (at.top < 80 || at.bottom > vh - 200) {
				await p.mouse.move(at.x, vh / 2);
				await p.mouse.wheel(0, at.top < 80 ? -200 : 200);
				prev = "";
				return null;
			}
			const sig = `${at.sx},${at.y0},${at.ex},${at.y1}`;
			const stable = sig === prev;
			prev = sig;
			return stable ? at : null;
		},
		10_000,
		200,
	);
}

/** 滚到助手消息里的这段文字并用 Range 选中它（不拖鼠标，选区与像素位置无关）。 */
async function selectText(p: Page, needle: string): Promise<Box> {
	await scrollToText(p, needle);
	return locateText(p, ASSISTANT_ROW, needle, true);
}

async function rightClick(p: Page, x: number, y: number) {
	await p.mouse.click(x, y, { button: "right" });
}

/** 选中助手消息里的一段文字并在选区上右键，等菜单出现。 */
async function selectAndMenu(p: Page, needle: string): Promise<Box> {
	const box = await selectText(p, needle);
	await rightClick(p, box.x, box.y);
	await p.locator(MENU).waitFor({ timeout: 5_000 });
	return box;
}

async function threadPage(o: PageOpts = {}): Promise<Page> {
	const p = await openPage(o);
	await goto(p, `/t/${MAIN_THREAD}`);
	await p.getByText(FINAL_MARK).first().waitFor({ timeout: 60_000 });
	await p
		.getByRole("textbox", { name: "消息", exact: true })
		.waitFor({ timeout: 30_000 });
	return p;
}

/** 选区 → 菜单 → 解释，等到 mock 里出现第 `n` 条记录。 */
async function explain(p: Page, needle: string, n: number): Promise<BtwRec> {
	await selectAndMenu(p, needle);
	await clickItem(p, "explain");
	await p.locator(CARD).waitFor({ timeout: 5_000 });
	return waitFor(
		`mock 收到第 ${n} 条旁路提问`,
		async () => (await btwOf(p))[n - 1],
		5_000,
	);
}

const cardText = async (p: Page) =>
	oneLine((await p.locator(CARD).textContent()) ?? "");

async function waitComplete(p: Page, id: string, turns = 1) {
	return waitFor(
		`记录 ${id} 第 ${turns} 轮完成`,
		async () => {
			const r = (await btwOf(p)).find((x) => x.id === id);
			return r &&
				1 + (r.followUps?.length ?? 0) === turns &&
				latest(r).status === "complete"
				? r
				: null;
		},
		15_000,
	);
}

// ---------------------------------------------------------------- 场景

async function R1() {
	const p = await threadPage();
	// 鼠标拖选（首字左缘按下、末字右缘松开），再在选区上右键
	const pos = await scrollToText(p, PASSAGE_A);
	await p.mouse.move(pos.sx, pos.y0);
	await p.mouse.down();
	await p.mouse.move((pos.sx + pos.ex) / 2, pos.y0, { steps: 4 });
	await p.mouse.move(pos.ex, pos.y1, { steps: 4 });
	await p.mouse.up();
	const sel = await p.evaluate(() => document.getSelection()?.toString() ?? "");
	check(sel.includes(PASSAGE_A), `拖选得到「${clip(sel, 60)}」`);
	await rightClick(p, pos.x, pos.y);
	const items = await menuEntries(p);
	note(`菜单：${namesOf(items).join(" / ")}`);
	expect(
		inOrder(keysOf(items), [
			"explain",
			"ask",
			"translate",
			"quote",
			"copy",
			"search",
		]),
		"依次有 解释、就此提问…、翻译、引用回复、复制、搜索",
	);
	const names = namesOf(items);
	expect(
		inOrder(names, ["解释", "就此提问…", "翻译", "引用回复", "复制", "搜索"]),
		"项的文字与计划一致",
	);
	const log = await ctxLog(p);
	expect(log.at(-1)?.prevented === true, "该次 contextmenu 已 preventDefault");
	await assertMenuInViewport(p);
	await shotBoth(p, "R1-selection-menu");
	await p.keyboard.press("Escape");
	await p.locator(MENU).waitFor({ state: "detached" });
	// 贴右下角边缘触发：在同一条消息上派发坐标为视口右下角的 contextmenu，菜单要翻到触发点上方并夹回视口
	const fired = await p.evaluate((text) => {
		const walker = document.createTreeWalker(
			document.querySelector('[data-testid="transcript"]') ?? document.body,
			NodeFilter.SHOW_TEXT,
		);
		for (let n = walker.nextNode(); n; n = walker.nextNode())
			if (n.textContent?.includes(text)) {
				document.getSelection()?.removeAllRanges();
				return n.parentElement?.dispatchEvent(
					new MouseEvent("contextmenu", {
						bubbles: true,
						cancelable: true,
						clientX: innerWidth - 2,
						clientY: innerHeight - 2,
					}),
				);
			}
		return null;
	}, PASSAGE_A);
	check(fired === false, "右下角的 contextmenu 被接管");
	await p.locator(MENU).waitFor();
	await assertMenuInViewport(p);
	await shotBoth(p, "R1-edge-menu");
	await p.keyboard.press("Escape");
	await p.locator(MENU).waitFor({ state: "detached" });
}

/** 菜单整个落在视口里，带玻璃材质与圆角（与现有菜单同一套类名）。 */
async function assertMenuInViewport(p: Page, host = MENU) {
	const m = await p.evaluate((h) => {
		const el = document.querySelector<HTMLElement>(h);
		if (!el) return null;
		const menu = el.matches('[role="menu"]')
			? el
			: el.querySelector<HTMLElement>('[role="menu"]');
		const box = menu ?? el;
		// 量排版盒：去掉 .pop-in 进入过渡的 transform（无头 Chromium 里这段过渡常停在起始帧
		// translateY(6px) scale(0.98)，包围盒会比实际排版多出 6px）
		const prev = [box.style.transition, box.style.transform] as const;
		box.style.transition = "none";
		box.style.transform = "none";
		const r = box.getBoundingClientRect();
		[box.style.transition, box.style.transform] = prev;
		const cs = menu ? getComputedStyle(menu) : null;
		return {
			left: r.left,
			top: r.top,
			right: r.right,
			bottom: r.bottom,
			vw: innerWidth,
			vh: innerHeight,
			cls: menu?.className ?? "",
			radius: cs?.borderTopLeftRadius ?? "",
		};
	}, host);
	check(m !== null, "菜单在页面上");
	if (!m) return;
	expect(
		m.left >= 0 && m.top >= 0 && m.right <= m.vw && m.bottom <= m.vh,
		`菜单在视口内（${Math.round(m.left)},${Math.round(m.top)}–${Math.round(m.right)},${Math.round(m.bottom)} / ${m.vw}×${m.vh}）`,
	);
	note(`菜单类名「${clip(m.cls, 160)}」，圆角 ${m.radius}`);
}

async function R2() {
	const p = await threadPage({ btwDelay: 400 });
	await selectAndMenu(p, PASSAGE_A);
	// 在页面里每 30ms 采样卡片文字长度，确认是逐段增长的
	await p.evaluate((card) => {
		const lens: number[] = [];
		(window as unknown as { __m22lens: number[] }).__m22lens = lens;
		const timer = setInterval(() => {
			const el = document.querySelector(card);
			const n = el?.textContent?.length ?? 0;
			if (el && lens.at(-1) !== n) lens.push(n);
		}, 30);
		setTimeout(() => clearInterval(timer), 8_000);
	}, CARD);
	await clickItem(p, "explain");
	await p.locator(CARD).waitFor({ timeout: 5_000 });
	const rec = await waitFor(
		"mock 收到旁路提问",
		async () => (await btwOf(p))[0],
		5_000,
	);
	await waitComplete(p, rec.id);
	await waitFor(
		"卡片显示完整回答",
		async () => (await cardText(p)).includes("模拟回答（explain）"),
		5_000,
	);
	const lens = await p.evaluate(
		() => (window as unknown as { __m22lens: number[] }).__m22lens,
	);
	expect(
		lens.filter((n, i) => i > 0 && n > (lens[i - 1] ?? 0)).length >= 3,
		`卡片文字逐段增长（长度序列 ${lens.join("→")}）`,
	);
	const q = rec.question;
	note(`question：${clip(q, 400)}`);
	expect(
		q.startsWith(
			`<oryn-selection action="explain" source="the assistant's reply`,
		),
		'以 <oryn-selection action="explain" source="the assistant\'s reply 开头',
	);
	expect(
		q.includes(`<selected>\n${PASSAGE_A}\n</selected>`),
		"含 <selected>\\n{原文}\\n</selected>",
	);
	expect(
		/<before>[^<]+<\/before>|<after>[^<]+<\/after>/.test(q),
		"含非空的 <before> 或 <after>",
	);
	// 解释卡不挡住选区那一行（按此刻的位置量：卡片不跟随滚动，转录若在这期间滚动过也要算进去）
	const card = await p.locator(CARD).boundingBox();
	const box = await locateText(p, ASSISTANT_ROW, PASSAGE_A, false);
	check(card !== null, "卡片有位置");
	if (card)
		expect(
			card.y >= box.bottom || card.y + card.height <= box.top,
			`卡片（y ${Math.round(card.y)}–${Math.round(card.y + card.height)}）不挡选区行（y ${Math.round(box.top)}–${Math.round(box.bottom)}）`,
		);
	await shotBoth(p, "R2-side-ask-card");
}

async function R3() {
	const p = await threadPage();
	const ta = p.getByRole("textbox", { name: "消息", exact: true });
	const tb = await ta.boundingBox();
	check(tb !== null, "输入框有位置");
	if (!tb) return;
	await rightClick(p, tb.x + tb.width / 2, tb.y + tb.height / 2);
	await sleep(300);
	let log = await ctxLog(p);
	expect(
		log.at(-1)?.tag === "textarea" && log.at(-1)?.prevented === false,
		`输入框右键未被拦（${JSON.stringify(log.at(-1))}）`,
	);
	expect((await p.locator(MENU).count()) === 0, "输入框右键没有统一菜单");
	await p.keyboard.press("Escape");
	const pos = await scrollToText(p, PASSAGE_A);
	await p.keyboard.down("Shift");
	await rightClick(p, pos.x, pos.y);
	await p.keyboard.up("Shift");
	await sleep(300);
	log = await ctxLog(p);
	expect(
		log.at(-1)?.shift === true && log.at(-1)?.prevented === false,
		`Shift+右键未被拦（${JSON.stringify(log.at(-1))}）`,
	);
	expect((await p.locator(MENU).count()) === 0, "Shift+右键没有统一菜单");
}

async function R4() {
	const p = await threadPage({ btwDelay: 2000 });
	await selectAndMenu(p, PASSAGE_A);
	await clickItem(p, "explain");
	await p
		.locator(CARD)
		.getByRole("button", { name: "关闭", exact: true })
		.click();
	await p.locator(CARD).waitFor({ state: "detached", timeout: 3_000 });
	note("卡片已关闭");
	const rec = await waitFor(
		"记录被取消",
		async () => {
			const r = (await btwOf(p))[0];
			return r && latest(r).status === "cancelled" ? r : null;
		},
		5_000,
	);
	expect(
		latest(rec).status === "cancelled",
		`记录 ${rec.id} 最新一轮 cancelled`,
	);
}

async function R5() {
	const p = await threadPage({ btwDelay: 2000 });
	const ta = p.getByRole("textbox", { name: "消息", exact: true });
	await ta.fill("/btw 慢问题");
	await ta.press("Enter");
	const dlg = p.getByRole("dialog", { name: "旁路提问" });
	await dlg.waitFor({ timeout: 5_000 });
	const rec = await waitFor(
		"/btw 记录在跑",
		async () => {
			const r = (await btwOf(p))[0];
			return r?.status === "running" ? r : null;
		},
		5_000,
	);
	note(`/btw 记录 ${rec.id}：${rec.question}`);
	await p.keyboard.press("Escape");
	await dlg.waitFor({ state: "detached", timeout: 3_000 });
	await selectAndMenu(p, PASSAGE_A);
	const items = await menuEntries(p);
	const ai = items.filter((e) =>
		["explain", "ask", "translate"].includes(e.key),
	);
	note(
		`AI 项：${ai.map((e) => `${e.name}${e.disabled ? "(禁用)" : ""}`).join(" / ")}`,
	);
	expect(
		ai.length === 3 && ai.every((e) => e.disabled),
		"解释 / 就此提问… / 翻译 都是 disabled",
	);
	const still = (await btwOf(p))[0];
	expect(still?.status === "running", "检查时 /btw 仍在跑");
}

async function R6() {
	const p = await threadPage({ btwDelay: 2000 });
	const first = await explain(p, PASSAGE_A, 1);
	check(latest(first).status === "running", `第一条 ${first.id} 在跑`);
	const second = await explain(p, PASSAGE_B, 2);
	const recs = await waitFor(
		"第一条取消、第二条在跑",
		async () => {
			const rs = await btwOf(p);
			const a = rs.find((r) => r.id === first.id);
			const b = rs.find((r) => r.id === second.id);
			return a && b && latest(a).status === "cancelled" ? { a, b } : null;
		},
		5_000,
	);
	expect(latest(recs.a).status === "cancelled", "第一条 cancelled");
	expect(latest(recs.b).status === "running", "第二条 running");
	expect(
		recs.b.question.includes(`<selected>\n${PASSAGE_B}\n</selected>`),
		"第二条的选区是第二段文字",
	);
	expect((await p.locator(CARD).count()) === 1, "只有一张卡片");
	const text = await cardText(p);
	expect(
		text.includes(PASSAGE_B) && !text.includes(PASSAGE_A),
		`卡片显示第二条（${clip(text, 80)}）`,
	);
}

async function R7() {
	const p = await threadPage();
	const rec = await explain(p, PASSAGE_A, 1);
	await waitComplete(p, rec.id);
	const input = p
		.locator(CARD)
		.getByRole("textbox", { name: "接着这个话题问…" });
	await input.fill("再短一点");
	await input.press("Enter");
	const done = await waitComplete(p, rec.id, 2);
	expect(done.followUps?.length === 1, "followUps.length === 1");
	await waitFor(
		"卡片出现追问气泡与新回答",
		async () => {
			const t = await cardText(p);
			return t.includes("再短一点") && t.includes("模拟回答：再短一点");
		},
		5_000,
	);
	note("卡片里有追问「再短一点」与回答「模拟回答：再短一点」");
}

async function R8() {
	const p = await threadPage();
	const rec = await explain(p, PASSAGE_A, 1);
	await waitComplete(p, rec.id);
	await p
		.locator(CARD)
		.getByRole("button", { name: "在旁路提问里打开" })
		.click();
	const dlg = p.getByRole("dialog", { name: "旁路提问" });
	await dlg.waitFor({ timeout: 5_000 });
	await p.locator(CARD).waitFor({ state: "detached", timeout: 3_000 });
	const opt = dlg.getByRole("option", { selected: true });
	const text = oneLine((await opt.textContent({ timeout: 5_000 })) ?? "");
	note(`选中的话题：${clip(text, 120)}`);
	expect(text.startsWith("解释："), "列表项文字以「解释：」开头");
	expect(!text.includes("<oryn-selection"), "列表项不含 <oryn-selection");
	expect(text.includes(PASSAGE_A), "选中的是这条话题");
	const detail = oneLine((await dlg.textContent()) ?? "");
	expect(detail.includes("模拟回答（explain）"), "详情里有这条回答");
}

async function R9() {
	const p = await threadPage();
	await selectAndMenu(p, PASSAGE_A);
	await clickItem(p, "ask");
	const input = p.locator(CARD).getByRole("textbox", { name: "想问什么？" });
	await input.fill("这句对吗");
	await p.locator(CARD).getByRole("button", { name: "发送" }).click();
	const rec = await waitFor(
		"mock 收到提问",
		async () => (await btwOf(p))[0],
		5_000,
	);
	note(`question 尾部：${clip(rec.question.slice(-120), 120)}`);
	expect(rec.question.includes('action="ask"'), '含 action="ask"');
	expect(
		rec.question.endsWith("Question: 这句对吗"),
		"以 Question: 这句对吗 结尾",
	);
}

/** 往主线程补一条消息（mock 推给已订阅的页面），等它渲染出来。 */
async function inject(p: Page, message: Record<string, unknown>) {
	await p.evaluate(
		({ id, message }) =>
			(window as unknown as W).__orynMock.appendEntry(id, {
				...message,
				timestamp: Date.now(),
			}),
		{ id: MAIN_THREAD, message },
	);
}

async function R10() {
	const p = await threadPage();
	await inject(p, {
		role: "assistant",
		content: [
			{
				type: "text",
				text: `退避函数：\n\n\`\`\`ts\n${CODE_SRC}\n\`\`\`\n`,
			},
		],
		api: "anthropic-messages",
		provider: "anthropic",
		model: "claude-sonnet-5",
		stopReason: "stop",
	});
	const block = p.locator(".md-code", { hasText: "backoffMs" }).last();
	await block.waitFor({ timeout: 10_000 });
	await block.scrollIntoViewIfNeeded();
	const bb = await block.boundingBox();
	check(bb !== null, "代码块有位置");
	if (!bb) return;
	await rightClick(p, bb.x + bb.width / 2, bb.y + bb.height - 12);
	const items = await menuEntries(p);
	note(`菜单：${namesOf(items).join(" / ")}`);
	expect(namesOf(items).includes("复制代码"), "有 复制代码");
	expect(namesOf(items).includes("解释这段代码"), "有 解释这段代码");
	await p
		.locator(MENU)
		.getByRole("menuitem", { name: "复制代码", exact: true })
		.click();
	const clip1 = await waitFor(
		"剪贴板写入",
		async () => {
			const t = await p.evaluate(() => navigator.clipboard.readText());
			return t.includes("backoffMs") ? t : null;
		},
		5_000,
	);
	expect(
		clip1 === CODE_SRC,
		`剪贴板等于代码块源码（${JSON.stringify(clip(clip1, 80))}）`,
	);
}

async function R11() {
	const p = await threadPage();
	await inject(p, {
		role: "user",
		content: [
			{ type: "text", text: "看看这张图" },
			{ type: "image", data: PNG, mimeType: "image/png" },
		],
	});
	const img = p.locator('[data-index][data-role="user"] img').last();
	await img.waitFor({ timeout: 10_000 });
	await img.scrollIntoViewIfNeeded();
	const bb = await img.boundingBox();
	check(bb !== null, "图片有位置");
	if (!bb) return;
	await rightClick(p, bb.x + bb.width / 2, bb.y + bb.height / 2);
	const items = await menuEntries(p);
	note(`菜单：${namesOf(items).join(" / ")}`);
	for (const n of ["复制图片", "下载图片", "引用到输入框"])
		expect(namesOf(items).includes(n), `有 ${n}`);
	await clickItem(p, "attach");
	await p
		.getByRole("button", { name: /^移除 / })
		.first()
		.waitFor({ timeout: 5_000 });
	const chips = await p.getByRole("button", { name: /^移除 / }).count();
	expect(chips === 1, `输入框出现 ${chips} 个附件条目`);
}

async function R12() {
	const p = await threadPage();
	const card = p.locator('[data-tool-call-id="toolu_bash_1"] .tv-card');
	await card.waitFor({ timeout: 10_000 });
	await card.scrollIntoViewIfNeeded();
	const head = card.locator(".tv-head").first();
	const bb = await head.boundingBox();
	check(bb !== null, "工具卡头有位置");
	if (!bb) return;
	await rightClick(p, bb.x + bb.width / 2, bb.y + bb.height / 2);
	const items = await menuEntries(p);
	note(`菜单：${namesOf(items).join(" / ")}`);
	expect(namesOf(items).includes("复制命令"), "有 复制命令");
	expect(namesOf(items).includes("解释这次调用"), "有 解释这次调用");
	await clickItem(p, "explain");
	const rec = await waitFor(
		"mock 收到提问",
		async () => (await btwOf(p))[0],
		5_000,
	);
	note(`question：${clip(rec.question, 200)}`);
	expect(rec.question.includes('action="tool"'), '含 action="tool"');
	expect(rec.question.includes("bash: "), "含 bash: ");
}

/** 侧栏里一条非当前、未置顶、未归档的普通线程。 */
async function pickSidebarThread(
	p: Page,
): Promise<{ id: string; title: string; row: Locator }> {
	const scroll = p.getByTestId("threads-scroll");
	await scroll.waitFor({ timeout: 10_000 });
	const ids = await p.evaluate((main) => {
		const m = (window as unknown as W).__orynMock;
		const hrefs = [
			...document.querySelectorAll<HTMLAnchorElement>(
				'[data-testid="threads-scroll"] a[href^="/t/"]',
			),
		].map((a) => a.getAttribute("href")?.slice(3) ?? "");
		return hrefs.filter((id, i) => {
			const t = m.threads.get(id);
			return (
				id !== main && hrefs.indexOf(id) === i && t && !t.pinned && t.title
			);
		});
	}, MAIN_THREAD);
	const id = ids[0];
	check(id !== undefined, `侧栏有可用的非当前线程（${ids.length} 条）`);
	const title = await p.evaluate(
		(id) => (window as unknown as W).__orynMock.threads.get(id)?.title ?? "",
		id as string,
	);
	return {
		id: id as string,
		title,
		row: p.locator(`[data-testid="threads-scroll"] a[href="/t/${id}"]`),
	};
}

async function rightClickEl(p: Page, el: Locator) {
	await el.scrollIntoViewIfNeeded();
	const bb = await el.boundingBox();
	if (!bb) throw new Error("目标元素没有位置");
	await rightClick(p, bb.x + bb.width / 2, bb.y + bb.height / 2);
}

const pinnedHas = (p: Page, id: string) =>
	p.evaluate((id) => {
		for (const g of document.querySelectorAll(
			'[data-testid="threads-scroll"] > div',
		)) {
			const head = g.firstElementChild?.textContent ?? "";
			if (head.includes("已置顶"))
				return g.querySelector(`a[href="/t/${id}"]`) !== null;
		}
		return false;
	}, id);

async function R13() {
	const p = await threadPage();
	const { id, title, row } = await pickSidebarThread(p);
	note(`线程 ${id}「${title}」`);
	const headerBefore = await p.title();
	await rightClickEl(p, row.first());
	const items = await menuEntries(p);
	note(`菜单：${namesOf(items).join(" / ")}`);
	await assertMenuInViewport(p);
	await shotBoth(p, "R13-thread-row-menu");
	await clickItem(p, "pin");
	await waitFor("该行出现在置顶分组", () => pinnedHas(p, id), 5_000);
	note("已出现在置顶分组");
	await rightClickEl(p, row.first());
	await clickItem(p, "rename");
	const dlg = p.getByRole("dialog", { name: "改名" });
	await dlg.waitFor({ timeout: 5_000 });
	const input = dlg.getByRole("textbox");
	const init = await input.inputValue();
	expect(init === title, `对话框初值是这条线程的标题（${init}）`);
	const next = `M22 改名 ${RUN}`;
	await input.fill(next);
	await dlg.getByRole("button", { name: "保存" }).click();
	await dlg.waitFor({ state: "detached", timeout: 5_000 });
	await waitFor(
		"侧栏该行标题更新",
		async () => ((await row.first().textContent()) ?? "").includes(next),
		5_000,
	);
	note("侧栏该行标题已更新");
	const mainTitle = await p.evaluate(
		(id) => (window as unknown as W).__orynMock.threads.get(id)?.title ?? "",
		MAIN_THREAD,
	);
	expect(!mainTitle.includes(next), `当前线程标题不变（${mainTitle}）`);
	expect((await p.title()) === headerBefore, `页面标题不变（${headerBefore}）`);
}

async function R14() {
	const p = await openPage();
	await goto(p, "/inbox");
	const rows = p.locator("[data-inbox-row]");
	await rows.first().waitFor({ timeout: 60_000 });
	const id = await p.evaluate(() => {
		for (const a of document.querySelectorAll("[data-inbox-row]"))
			if (!a.querySelector('[aria-label="未读"]'))
				return a.getAttribute("data-inbox-row");
		return null;
	});
	check(id !== null, `收件箱有已读的行（${id}）`);
	const row = p.locator(`[data-inbox-row="${id}"]`);
	await rightClickEl(p, row);
	const items = await menuEntries(p);
	note(`菜单：${namesOf(items).join(" / ")}`);
	await p
		.locator(MENU)
		.getByRole("menuitem", { name: "标为未读", exact: true })
		.click();
	await row.getByRole("img", { name: "未读" }).waitFor({ timeout: 5_000 });
	note("该行出现未读点");
}

async function openPanelTab(p: Page, tab: string) {
	await p.locator(`[data-panel-tab=${tab}]`).click();
	await waitFor(
		`面板切到 ${tab}`,
		() =>
			p.evaluate(
				(tab) =>
					document
						.querySelector(`[data-panel-tab=${tab}]`)
						?.getAttribute("aria-selected") === "true",
				tab,
			),
		5_000,
	);
}

/** 文件面板里第一个文件（非目录）行。 */
async function firstFileRow(p: Page, ext?: string): Promise<Locator> {
	await openPanelTab(p, "files");
	const items = p.locator('[role="treeitem"][data-path]');
	await items.first().waitFor({ timeout: 15_000 });
	const path = await waitFor(
		"文件树里有文件",
		() =>
			p.evaluate((ext) => {
				for (const el of document.querySelectorAll(
					'[role="treeitem"][data-path]',
				)) {
					const path = el.getAttribute("data-path") ?? "";
					if (el.hasAttribute("aria-expanded")) continue;
					if (ext && !path.endsWith(ext)) continue;
					return path;
				}
				return null;
			}, ext),
		10_000,
	);
	return p.locator(`[role="treeitem"][data-path="${path}"]`);
}

async function R15() {
	const p = await threadPage();
	const row = await firstFileRow(p);
	const path = await row.getAttribute("data-path");
	note(`文件 ${path}`);
	// 行尾「⋯」给出的动作集作对照
	await row.hover();
	await row.getByRole("button", { name: /的操作$/ }).click();
	const viaMore = namesOf(await menuEntries(p));
	await p.keyboard.press("Escape");
	await p.locator(MENU).waitFor({ state: "detached" });
	await rightClickEl(p, row);
	const viaCtx = namesOf(await menuEntries(p));
	note(`右键：${viaCtx.join(" / ")}；「⋯」：${viaMore.join(" / ")}`);
	expect(viaCtx.join("|") === viaMore.join("|"), "右键与「⋯」的动作集相同");
	for (const n of ["重命名", "下载", "复制路径", "删除"])
		expect(viaCtx.includes(n), `有 ${n}`);
}

async function R16() {
	const p = await threadPage();
	// 根目录下没有源码文件：先展开 src
	await openPanelTab(p, "files");
	const src = p.locator('[role="treeitem"][data-path$="/src"]');
	await src.click();
	const row = await firstFileRow(p, ".ts");
	const path = (await row.getAttribute("data-path")) ?? "";
	note(`文件 ${path}`);
	await row.click();
	const editor = p.locator('[data-testid="code-editor"] .cm-content');
	await editor.waitFor({ timeout: 15_000 });
	// 光标放到第 1 行行首，Shift+↓ 再 Shift+End：选中第 1–2 行
	const firstLine = editor.locator(".cm-line").first();
	await firstLine.click({ position: { x: 2, y: 4 } });
	await p.keyboard.press("Control+Home");
	await p.keyboard.press("Shift+ArrowDown");
	await p.keyboard.press("Shift+End");
	const lines = await editor
		.locator(".cm-line")
		.evaluateAll((els) => els.slice(0, 2).map((e) => e.textContent ?? ""));
	const bb = await firstLine.boundingBox();
	check(bb !== null, "第一行有位置");
	if (!bb) return;
	await rightClick(p, bb.x + Math.min(20, bb.width / 2), bb.y + bb.height / 2);
	const items = await menuEntries(p);
	note(`菜单：${namesOf(items).join(" / ")}`);
	await clickItem(p, "insert");
	const ta = p.getByRole("textbox", { name: "消息", exact: true });
	const value = await waitFor(
		"输入框收到引用",
		async () => {
			const v = await ta.inputValue();
			return v.includes("```") ? v : null;
		},
		5_000,
	);
	note(`输入框：${clip(value, 300)}`);
	expect(
		new RegExp(`\`/[^\`]*${path.split("/").pop()}:1-2\``).test(value),
		"含 `<绝对路径>:1-2`",
	);
	expect(
		/```[a-z]*\n[\s\S]*\n```/.test(value) && value.includes(lines.join("\n")),
		"含三反引号围起来的选中代码",
	);
}

async function R17() {
	const p = await threadPage();
	await openPanelTab(p, "changes");
	// mock 工作区里改过且文件确实存在的文本文件（orbfs.ts 的 gitFixtures：src/main.ts modified）
	const path = "src/main.ts";
	const li = p.locator(`li[data-change-path="${path}"]`);
	await li.waitFor({ timeout: 15_000 });
	await rightClickEl(p, li);
	const items = await menuEntries(p);
	note(`菜单：${namesOf(items).join(" / ")}`);
	await clickItem(p, "openFile");
	await waitFor(
		"右面板切到文件标签",
		() =>
			p.evaluate(
				() =>
					document
						.querySelector("[data-panel-tab=files]")
						?.getAttribute("aria-selected") === "true",
			),
		5_000,
	);
	// 编辑器里是该文件的源码；往上找到同时含文件名的查看器容器（标题行显示路径，长路径中段会被省略）
	const shown = await waitFor(
		"查看器显示该文件",
		() =>
			p.evaluate(() => {
				const ed = document.querySelector('[data-testid="code-editor"]');
				if (!ed?.textContent?.includes('import { greet } from "./util"'))
					return null;
				for (let el = ed.parentElement; el; el = el.parentElement)
					if (el.textContent?.includes("main.ts")) return el.textContent;
				return null;
			}),
		10_000,
	);
	note(`查看器：${clip(oneLine(shown), 120)}`);
}

async function R18() {
	const p = await threadPage();
	await openPanelTab(p, "terminal");
	// 面板可能自己开着一个终端：等一会儿，没有再新建
	const xterm = p.locator(".xterm >> visible=true").first();
	const has = await xterm
		.waitFor({ timeout: 5_000 })
		.then(() => true)
		.catch(() => false);
	if (!has) await p.getByRole("button", { name: "新建终端" }).first().click();
	await xterm.waitFor({ timeout: 15_000 });
	await waitFor(
		"终端有输出",
		async () => ((await xterm.textContent()) ?? "").trim().length > 0,
		10_000,
	);
	await rightClickEl(p, xterm);
	let items = await menuEntries(p);
	note(`菜单：${namesOf(items).join(" / ")}`);
	for (const n of ["粘贴", "全选", "清屏"])
		expect(namesOf(items).includes(n), `有 ${n}`);
	await clickItem(p, "selectAll");
	await p.locator(MENU).waitFor({ state: "detached" });
	await rightClickEl(p, xterm);
	items = await menuEntries(p);
	note(`全选后菜单：${namesOf(items).join(" / ")}`);
	for (const n of ["解释", "引用到输入框", "复制"])
		expect(namesOf(items).includes(n), `有 ${n}`);
}

async function R19() {
	const p = await openPage();
	await goto(p, "/settings/selection-actions");
	await p.getByRole("button", { name: "添加动作" }).click();
	const dlg = p.getByRole("dialog");
	await dlg.waitFor({ timeout: 5_000 });
	await dlg.getByRole("textbox", { name: "名称" }).fill("挑毛病");
	await dlg.getByRole("textbox", { name: "提示词" }).fill("找出这段话里的漏洞");
	await dlg.getByRole("button", { name: "保存" }).click();
	await dlg.waitFor({ state: "detached", timeout: 5_000 });
	await p.getByText("挑毛病").first().waitFor({ timeout: 5_000 });
	note("设置页已保存「挑毛病」");
	await spaGo(p, `/t/${MAIN_THREAD}`);
	await p.getByText(FINAL_MARK).first().waitFor({ timeout: 30_000 });
	await selectAndMenu(p, PASSAGE_A);
	const items = await menuEntries(p);
	note(`菜单：${namesOf(items).join(" / ")}`);
	const custom = items.find((e) => e.name === "挑毛病");
	check(custom !== undefined, "菜单里有「挑毛病」");
	await clickItem(p, custom?.key ?? "");
	const rec = await waitFor(
		"mock 收到提问",
		async () => (await btwOf(p))[0],
		5_000,
	);
	note(`question：${clip(rec.question, 200)}`);
	expect(
		rec.question.includes('action="custom" name="挑毛病"'),
		'含 action="custom" name="挑毛病"',
	);
	expect(rec.question.includes("找出这段话里的漏洞"), "含提示词");
}

async function R20() {
	const p = await openPage({ width: 390, height: 844, touch: true });
	await goto(p, "/");
	// 手机宽度的侧栏是抽屉：先点页头的「侧边栏」打开
	await p
		.getByRole("button", { name: "侧边栏", exact: true })
		.first()
		.tap({ timeout: 60_000 });
	await p.getByTestId("threads-scroll").waitFor({ timeout: 10_000 });
	const { id, row } = await pickSidebarThread(p);
	await row.first().scrollIntoViewIfNeeded();
	const bb = await row.first().boundingBox();
	check(bb !== null, `线程行 ${id} 有位置`);
	if (!bb) return;
	const url = p.url();
	const cdp = await p.context().newCDPSession(p);
	const pt = { x: bb.x + bb.width / 2, y: bb.y + bb.height / 2 };
	await cdp.send("Input.dispatchTouchEvent", {
		type: "touchStart",
		touchPoints: [pt],
	});
	await sleep(600);
	await cdp.send("Input.dispatchTouchEvent", {
		type: "touchEnd",
		touchPoints: [],
	});
	await p.locator(SHEET).waitFor({ timeout: 3_000 });
	const items = await menuEntries(p, SHEET);
	note(`动作面板：${namesOf(items).join(" / ")}`);
	expect(items.length > 0, "出现 context-sheet");
	await sleep(300);
	expect(p.url() === url, `路由没有变化（${p.url()}）`);
	await assertMenuInViewport(p, SHEET);
	await shotBoth(p, "R20-context-sheet");
}

async function R21() {
	const p = await openPage();
	await goto(p, `/s/${SHARE_SLUG}`);
	await p.getByText(FINAL_MARK).first().waitFor({ timeout: 60_000 });
	const box = await selectAndMenu(p, PASSAGE_A);
	let items = await menuEntries(p);
	note(`选区菜单：${namesOf(items).join(" / ")}`);
	expect(
		keysOf(items).join("|") === "copy" && namesOf(items)[0] === "复制",
		"选区菜单只有「复制」",
	);
	await p.keyboard.press("Escape");
	await p.locator(MENU).waitFor({ state: "detached" });
	// 同一条消息里、选区外的另一段文字上右键
	const other = await locateText(p, ASSISTANT_ROW, PASSAGE_B, false);
	check(other.top > box.bottom || other.bottom < box.top, "第二个点在选区之外");
	await rightClick(p, other.x, other.y);
	items = await menuEntries(p);
	note(`消息菜单：${namesOf(items).join(" / ")}`);
	expect(namesOf(items).join("|") === "复制消息", "消息菜单只有「复制消息」");
}

const PLAN: Record<string, () => Promise<void>> = {
	R1,
	R2,
	R3,
	R4,
	R5,
	R6,
	R7,
	R8,
	R9,
	R10,
	R11,
	R12,
	R13,
	R14,
	R15,
	R16,
	R17,
	R18,
	R19,
	R20,
	R21,
};

// ---------------------------------------------------------------- vite

let vite: Subprocess | undefined;

/** vite 开发服务器（mock）的模块代码；独立 cacheDir，与别处常驻的 vite 互不触发依赖重新预构建。 */
function viteModule(): string {
	const dir = `${WEB}/node_modules/.m22`;
	mkdirSync(dir, { recursive: true });
	const file = `${dir}/vite-mock.mjs`;
	writeFileSync(
		file,
		`import { createServer } from "vite";
const s = await createServer({
	configFile: "vite.config.ts",
	cacheDir: "node_modules/.vite-m22",
	optimizeDeps: { entries: ["index.html", "src/**/*.{ts,tsx}", "!src/**/*.test.{ts,tsx}"] },
	server: { port: ${PORT}, strictPort: true, host: "127.0.0.1" },
});
await s.listen();
console.log("ready");
`,
	);
	return file;
}

async function startVite() {
	const log = openSync(`${SHOTS}/vite.log`, "w");
	vite = Bun.spawn(["bun", viteModule()], {
		cwd: WEB,
		env: { ...process.env, VITE_MOCK: "1" },
		stdout: log,
		stderr: log,
	});
	await waitFor(
		"mock vite 就绪",
		async () => (await fetch(BASE)).ok,
		60_000,
		300,
	);
	// 冷启动按需编译要很久：先把线程页加载一遍
	const warm = await openPage();
	await goto(warm, `/t/${MAIN_THREAD}`);
	await warm.getByText(FINAL_MARK).first().waitFor({ timeout: 180_000 });
	await closeAll();
}

// ---------------------------------------------------------------- 运行

async function runOne(r: string, fn: () => Promise<void>) {
	console.log(`${r} …`);
	evidence = [];
	softFails = 0;
	const t0 = Date.now();
	const errs0 = pageErrors.length;
	const diag0 = pageDiag.length;
	let verdict: Verdict = "PASS";
	let reason: string | undefined;
	try {
		await fn();
		if (softFails) {
			verdict = "FAIL";
			reason = `${softFails} 条断言不成立`;
		}
	} catch (e) {
		verdict = "FAIL";
		reason = e instanceof Error ? oneLine(e.message).slice(0, 400) : String(e);
		note(`错误：${reason}`);
	}
	const errs = pageErrors.slice(errs0);
	if (errs.length) {
		note(`页面未捕获异常 ${errs.length} 条：${clip(errs.join(" ｜ "), 600)}`);
		if (verdict === "PASS") {
			verdict = "FAIL";
			reason = "页面未捕获异常";
		}
	}
	if (verdict === "FAIL") {
		const diag = pageDiag.slice(diag0);
		if (diag.length)
			note(`页面诊断 ${diag.length} 条：${clip(diag.join(" ｜ "), 1500)}`);
		let i = 0;
		for (const c of contexts)
			for (const pg of c.pages()) {
				const f = `${SHOTS}/fail-${r}-${i++}.png`;
				if (
					await pg.screenshot({ path: f }).then(
						() => true,
						() => false,
					)
				)
					note(`失败截图 ${f.slice(SHOTS.length + 1)}（${pg.url()}）`);
			}
	}
	await closeAll();
	const seconds = Number(((Date.now() - t0) / 1000).toFixed(1));
	console.log(
		`${r} ${verdict}${reason ? ` (${clip(reason, 200)})` : ""} ${seconds}s`,
	);
	outcomes.push({ r, verdict, reason, seconds, evidence });
}

async function cleanup() {
	await closeAll();
	await browser?.close().catch(() => {});
	netns?.stop();
	if (vite && vite.exitCode === null) vite.kill("SIGTERM");
}
for (const sig of ["SIGINT", "SIGTERM"] as const)
	process.on(sig, () => {
		void cleanup().then(() => process.exit(130));
	});

console.log(`M22 验收 run=${RUN} shots=${SHOTS}`);
const todo = RS.filter((r) => !ONLY || ONLY.has(r));
try {
	await startVite();
	for (const r of todo) await runOne(r, PLAN[r] as () => Promise<void>);
} finally {
	await cleanup();
}

console.log("\n== 汇总");
for (const o of outcomes)
	console.log(
		`${o.r} ${o.verdict}${o.reason ? `  ${clip(o.reason, 160)}` : ""}`,
	);
writeFileSync(
	`${SHOTS}/results.json`,
	JSON.stringify({ run: RUN, outcomes }, null, 2),
);
console.log(`\n结果：${SHOTS}/results.json`);
process.exit(
	outcomes.length === todo.length && outcomes.every((o) => o.verdict === "PASS")
		? 0
		: 1,
);
