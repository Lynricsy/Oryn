#!/usr/bin/env bun
/**
 * mock 模式截图：起 `VITE_MOCK=1` 的 vite 开发服务器，用本机 Chromium 截图，输出到 `$ORYN_SHOTS_DIR`（默认 /tmp/oryn-shots）。
 *
 * 用法（web/ 目录）：
 *   `bun run shots`：390 / 720 / 1280 宽下截 `/` 与 `/t/<id>`（仅浅色）。
 *   `--m25`：改截 M2.5 环境提示条（setup 失败 + 日志对话框、orb 失败），390 / 1280 宽，输出到 `<目录>/m25/`。
 *   `--all`（`bun run shots:all`）：全界面矩阵 = 明暗 × 宽度 {390, 1280} × 界面清单（见 `SCREENS`）；
 *     声明了 `widths` 的界面另可截 360（目前只有统计页），
 *     文件名 `<界面>-<宽>-<scheme>.png`。仅 `--all` 可用的参数：
 *     `--scheme light|dark|both`（默认 both，经 `browser.newContext({ colorScheme })` 设置）；
 *     `--accent <名>`（经 `addInitScript` 写 `localStorage["oryn.theme"] = {mode: <scheme>, accent}`）；
 *     `--only <前缀>[,<前缀>…]`（只截界面名以其开头的项）；
 *     `--check`（在 `thread-bottom` 上断言输入坞不遮挡最后一条转录行，含模拟软键盘的第二次断言；
 *     统计页断言内容区没有横向溢出）。
 *   任一界面失败或断言不通过时打印原因并以非零码退出（其余界面照常截完）。
 * 环境变量：CHROMIUM=<可执行文件>（默认 /usr/bin/chromium）；WEBKIT=<Playwright WebKit 启动脚本> 时改用 WebKit 截图
 *   （近似 Safari：核对 iOS 上的动画、圆角、backdrop-filter 等差异；需与 playwright-core 版本匹配的 WebKit 构建）。
 */
import { mkdir } from "node:fs/promises";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Page,
	webkit,
} from "playwright-core";
import {
	DESKTOP_THREADS,
	ENV_THREADS,
	M9_IDS,
	M11A_IDS,
	M11B_IDS,
	MAIN_THREAD,
	projects,
	TOOL_ZOO_THREAD,
} from "../src/mock/fixtures";
import { MOCK_SHARE_SLUGS } from "../src/mock/server";

const ARGS = process.argv.slice(2);
const flag = (name: string) => ARGS.includes(name);
function option(name: string): string | undefined {
	const i = ARGS.indexOf(name);
	if (i === -1) return undefined;
	const v = ARGS[i + 1];
	if (v === undefined || v.startsWith("--")) {
		console.error(`${name} 需要一个值`);
		process.exit(2);
	}
	return v;
}

const PORT = 5179;
const M25 = flag("--m25");
const ALL = flag("--all");
const SCHEME_ARG = option("--scheme") ?? "both";
const ACCENT = option("--accent");
const ONLY = option("--only")
	?.split(",")
	.map((s) => s.trim())
	.filter(Boolean);
const CHECK = flag("--check");

const ACCENTS = ["teal", "cobalt", "violet", "berry", "fox", "graphite"];
if (!ALL && (ACCENT || ONLY || CHECK || ARGS.includes("--scheme"))) {
	console.error("--scheme / --accent / --only / --check 只能与 --all 一起用");
	process.exit(2);
}
if (!["light", "dark", "both"].includes(SCHEME_ARG)) {
	console.error(`--scheme 只接受 light|dark|both，收到 ${SCHEME_ARG}`);
	process.exit(2);
}
if (ACCENT && !ACCENTS.includes(ACCENT)) {
	console.error(`--accent 只接受 ${ACCENTS.join("|")}，收到 ${ACCENT}`);
	process.exit(2);
}

const OUT = `${process.env.ORYN_SHOTS_DIR ?? "/tmp/oryn-shots"}${M25 ? "/m25" : ""}`;
const BASE = `http://127.0.0.1:${PORT}`;
const WIDTHS = [
	{ width: 390, height: 844 },
	{ width: 720, height: 1000 },
	{ width: 1280, height: 860 },
];
const PAGES = [
	{ name: "home", path: "/", ready: "textarea" },
	{
		name: "thread",
		path: `/t/${MAIN_THREAD}`,
		ready: "[data-testid=transcript]",
	},
];

async function waitForServer(url: string, timeoutMs: number) {
	const deadline = Date.now() + timeoutMs;
	while (Date.now() < deadline) {
		try {
			const res = await fetch(url);
			if (res.ok) return;
		} catch {
			// 尚未就绪
		}
		await Bun.sleep(200);
	}
	throw new Error(`vite 未在 ${timeoutMs}ms 内就绪`);
}

const server = Bun.spawn(
	[
		"bunx",
		"vite",
		"--port",
		String(PORT),
		"--strictPort",
		"--host",
		"127.0.0.1",
	],
	{
		cwd: `${import.meta.dir}/..`,
		env: { ...process.env, VITE_MOCK: "1" },
		stdout: "ignore",
		stderr: "inherit",
	},
);

/** M2.5：setup 失败提示条 → 日志对话框；orb 失败提示条。 */
async function shootEnv(browser: Browser): Promise<string[]> {
	const written: string[] = [];
	for (const size of WIDTHS.filter((w) => w.width !== 720)) {
		const context = await browser.newContext({
			viewport: size,
			deviceScaleFactor: 2,
			locale: "zh-CN",
		});
		const page = await context.newPage();
		const shot = async (name: string) => {
			const file = `${OUT}/${name}-${size.width}.png`;
			await page.screenshot({ path: file });
			written.push(file);
		};
		await page.goto(`${BASE}/t/${ENV_THREADS.setupFailed}`, {
			waitUntil: "networkidle",
		});
		await page.waitForSelector("[data-testid=env-banner]", { timeout: 15_000 });
		await page.waitForTimeout(800);
		await shot("setup-failed");
		await page
			.locator("[data-testid=env-banner]")
			.getByRole("button", { name: "查看日志" })
			.click();
		await page.waitForSelector("[data-testid=setup-log]", { timeout: 15_000 });
		await page.waitForTimeout(500);
		await shot("setup-log");
		await page.keyboard.press("Escape");
		await page.goto(`${BASE}/t/${ENV_THREADS.orbFailed}`, {
			waitUntil: "networkidle",
		});
		await page.waitForSelector("[data-testid=env-banner]", { timeout: 15_000 });
		await page.waitForTimeout(800);
		await shot("orb-failed");
		await context.close();
	}
	return written;
}

/** 默认模式：390 / 720 / 1280 宽下的首页与线程页（含一张转录中部）。 */
async function shootDefault(browser: Browser): Promise<string[]> {
	const written: string[] = [];
	for (const size of WIDTHS) {
		const context = await browser.newContext({
			viewport: size,
			deviceScaleFactor: 2,
			locale: "zh-CN",
		});
		const page = await context.newPage();
		for (const p of PAGES) {
			await page.goto(`${BASE}${p.path}`, { waitUntil: "networkidle" });
			await page.waitForSelector(p.ready, { timeout: 15_000 });
			// 等待懒加载的高亮 / mermaid / KaTeX 渲染完成
			await page.waitForTimeout(2500);
			const file = `${OUT}/${p.name}-${size.width}.png`;
			await page.screenshot({ path: file });
			written.push(file);
			if (p.name === "thread") {
				// 额外一张：滚动到转录中部，展示思考块与工具卡片
				await page.evaluate(() => {
					const el = document.querySelector<HTMLElement>(
						"[data-testid=transcript]",
					);
					if (el) el.scrollTop = el.scrollHeight * 0.25;
				});
				await page.waitForTimeout(1200);
				const mid = `${OUT}/${p.name}-${size.width}-mid.png`;
				await page.screenshot({ path: mid });
				written.push(mid);
			}
		}
		await context.close();
	}
	return written;
}

// ---------------------------------------------------------------- --all 矩阵

type Scheme = "light" | "dark";

interface Shot {
	page: Page;
	width: number;
	scheme: Scheme;
}

interface Screen {
	name: string;
	/** 只在这些宽度下截（缺省两种宽度都截）。 */
	widths?: readonly number[];
	/** 视口拉高到能装下整条转录（用 1 倍像素比，避免超出位图上限）。 */
	tall?: boolean;
	prepare(s: Shot): Promise<void>;
	/** 截图之后的断言（仅 `--check`）；返回失败原因或 null。 */
	verify?(s: Shot): Promise<string | null>;
	/** 页面脚本之前注入的脚本源码（假桌面壳）；有它的界面在独立的 context 里截，不污染流水线里的其它界面。 */
	init?: string;
	/** 触屏设备（`hasTouch` + `isMobile`，`(pointer: coarse)` 成立）；同样在独立的 context 里截。 */
	touch?: boolean;
}

/**
 * 假桌面 / 移动壳（M6 / M7）：`window.__TAURI__.core.invoke` 按命令表应答，`handlers` 是 `{命令: 应答}` 的 JSON；
 * `host` 非假时另注入 `__ORYN_HOST__`（远程页面里的宿主适配）：`true` / `"desktop"` = linux 桌面壳，
 * `"android"` = 移动壳（bridge 2）；连接页不需要它，`launcherPlatform` 只给连接页设平台。
 */
function fakeTauri(
	handlers: Record<string, unknown>,
	host: boolean | "desktop" | "android" = false,
	launcherPlatform?: "android",
): string {
	const meta =
		host === "android"
			? `{ kind: "tauri", platform: "android", version: "0.1.0", bridge: 2 }`
			: host
				? `{ kind: "tauri", platform: "linux", version: "0.1.0", bridge: 1 }`
				: launcherPlatform
					? `{ kind: "tauri", platform: "${launcherPlatform}", version: "0.1.0", bridge: 2 }`
					: "";
	return `(() => {
		const replies = ${JSON.stringify(handlers)};
		${meta ? `window.__ORYN_HOST__ = ${meta};` : ""}
		// 配对码按截图时刻起算 9 分 30 秒后过期
		const state = replies.launcher_state && replies.launcher_state.state;
		if (state && state.step === "pairing") state.expiresAt = Date.now() + 570000;
		window.__TAURI__ = { core: { invoke: async (cmd) => {
			if (!(cmd in replies)) throw new Error("unexpected " + cmd);
			return replies[cmd];
		} } };
	})();`;
}

const LAUNCHER_SERVER = "https://oryn.example.com";

/**
 * 无系统标题栏的假桌面壳（bridge 3）：`__ORYN_HOST__.chrome` 按平台给布局，`host_window` 应答窗口状态。
 * macOS 的红绿灯由系统画、浏览器里没有：这里在窗口坐标 (22, 24) 起画三个 12px 圆点占位（与 chrome.rs 的
 * trafficLightPosition 同一位置、按钮框 14×16、间距 20），只为看留白与对齐，不是页面的一部分。
 * `collapsed`：侧栏收起（写 localStorage）；`launcher`：连接页（另应答 `launcher_state`）。
 */
function chromeShell(
	platform: "macos" | "windows" | "linux-left",
	opts: {
		collapsed?: boolean;
		launcher?: boolean;
		window?: { maximized: boolean; fullscreen: boolean; focused: boolean };
	} = {},
): string {
	const chrome =
		platform === "macos"
			? {
					platform: "macos",
					controls: "native-left",
					buttons: [],
					inset: { top: 48, left: 84, right: 0 },
				}
			: {
					platform: platform === "windows" ? "windows" : "linux",
					controls: platform === "windows" ? "custom-right" : "custom-left",
					buttons:
						platform === "windows"
							? ["minimize", "maximize", "close"]
							: ["close", "minimize", "maximize"],
					inset: { top: 48, left: 0, right: 0 },
				};
	const meta = {
		kind: "tauri",
		platform: chrome.platform,
		version: "0.1.0",
		bridge: 3,
		chrome,
	};
	const replies = {
		host_ready: { bridge: 3, visible: true },
		host_set_status: null,
		host_set_language: null,
		host_notify: null,
		host_window: opts.window ?? {
			maximized: false,
			fullscreen: false,
			focused: true,
		},
		...(opts.launcher
			? {
					launcher_state: {
						state: { step: "connect", serverUrl: "" },
						lang: "zh-CN",
						version: "0.1.0",
						tokenStore: "keyring",
					},
				}
			: {}),
	};
	return `(() => {
		const replies = ${JSON.stringify(replies)};
		window.__ORYN_HOST__ = Object.freeze(${JSON.stringify(meta)});
		window.__TAURI__ = { core: { invoke: async (cmd) => {
			if (!(cmd in replies)) throw new Error("unexpected " + cmd);
			return replies[cmd];
		} } };
		${opts.collapsed ? `localStorage.setItem("oryn.sidebar.collapsed", "1");` : ""}
		${
			platform === "macos"
				? `document.addEventListener("DOMContentLoaded", () => {
			const lights = document.createElement("div");
			lights.setAttribute("aria-hidden", "true");
			lights.style.cssText = "position:fixed;left:22px;top:24px;z-index:2147483647;pointer-events:none;display:flex;gap:8px";
			for (const c of ["#ff5f57", "#febc2e", "#28c840"]) {
				const dot = document.createElement("span");
				// 全局 corner-shape 是超椭圆（index.css），这里是内联样式、不带 rounded-full，须显式 round 才是正圆
				dot.style.cssText = "width:12px;height:12px;margin:2px 0 2px 0;border-radius:50%;corner-shape:round;background:" + c + ";box-shadow:inset 0 0 0 0.5px rgba(0,0,0,.25)";
				lights.append(dot);
			}
			// 全屏时系统收起红绿灯：跟着壳的 window 事件显隐（逐帧动画脚本也靠它）
			const sync = (fs) => { lights.style.visibility = fs ? "hidden" : "visible"; };
			sync(replies.host_window.fullscreen);
			window.addEventListener("oryn:host", (e) => {
				const d = e.detail;
				if (d && d.type === "window" && typeof d.fullscreen === "boolean") sync(d.fullscreen);
			});
			document.body.append(lights);
		});`
				: ""
		}
	})();`;
}

/** 关掉宽屏线程页默认展开的右面板。 */
async function closePanel(page: Page) {
	const toggle = page.getByRole("button", { name: "信息面板", exact: true });
	if ((await toggle.getAttribute("aria-pressed")) === "true") {
		await toggle.click();
		await settle(page, 500);
	}
}

/**
 * 侧栏最窄（256px，即 840–1023 宽的三栏）与 macOS 全屏的几组线程页：`narrow` 时先把视口改成 900 宽
 * （文件名仍带所在流水线的 1280）。
 */
function chromeExtraScreens(
	tag: string,
	platform: "macos" | "windows" | "linux-left",
	opts: { narrow?: boolean; fullscreen?: boolean },
): Screen[] {
	const widths = [1280] as const;
	const init = (collapsed: boolean) =>
		chromeShell(platform, {
			collapsed,
			...(opts.fullscreen
				? { window: { maximized: false, fullscreen: true, focused: true } }
				: {}),
		});
	const thread =
		(panel: boolean) =>
		async ({ page }: Shot) => {
			if (opts.narrow) await page.setViewportSize({ width: 900, height: 860 });
			await openThread(page, MAIN_THREAD);
			if (panel) await openPanel(page, "info");
			else await closePanel(page);
		};
	const out: Screen[] = [
		{
			name: `chrome-${tag}`,
			widths,
			init: init(false),
			prepare: thread(false),
		},
		{
			name: `chrome-${tag}-panel`,
			widths,
			init: init(false),
			prepare: thread(true),
		},
	];
	if (opts.fullscreen)
		out.push({
			name: `chrome-${tag}-collapsed`,
			widths,
			init: init(true),
			prepare: thread(false),
		});
	return out;
}

/** 无系统标题栏的一组界面（1280 宽，明暗各一张）：线程页侧栏展开 / 收起 × 面板开 / 关、首页、设置、连接页。 */
function chromeScreens(
	tag: "mac" | "win" | "linux-left",
	platform: "macos" | "windows" | "linux-left",
): Screen[] {
	const widths = [1280] as const;
	const thread =
		(panel: boolean) =>
		async ({ page }: Shot) => {
			await openThread(page, MAIN_THREAD);
			if (panel) await openPanel(page, "info");
			else await closePanel(page);
		};
	return [
		{
			name: `chrome-${tag}-thread`,
			widths,
			init: chromeShell(platform),
			prepare: thread(false),
		},
		{
			name: `chrome-${tag}-thread-panel`,
			widths,
			init: chromeShell(platform),
			prepare: thread(true),
		},
		{
			name: `chrome-${tag}-collapsed`,
			widths,
			init: chromeShell(platform, { collapsed: true }),
			prepare: thread(false),
		},
		{
			name: `chrome-${tag}-collapsed-panel`,
			widths,
			init: chromeShell(platform, { collapsed: true }),
			prepare: thread(true),
		},
		{
			name: `chrome-${tag}-home`,
			widths,
			init: chromeShell(platform),
			prepare: async ({ page }) => {
				await open(page, "/", "textarea");
			},
		},
		{
			name: `chrome-${tag}-home-collapsed`,
			widths,
			init: chromeShell(platform, { collapsed: true }),
			prepare: async ({ page }) => {
				await open(page, "/", "textarea");
			},
		},
		{
			name: `chrome-${tag}-settings`,
			widths,
			init: chromeShell(platform),
			prepare: async ({ page }) => {
				await open(page, "/settings/appearance", "main");
			},
		},
		{
			name: `chrome-${tag}-launcher`,
			widths,
			init: chromeShell(platform, { launcher: true }),
			prepare: async ({ page }) => {
				await open(page, "/launcher.html", "h1");
			},
		},
	];
}

/** 假移动壳的 `MobileInfo`：默认屏幕四角 40px、无 inset（自查屏幕圆角同心；`mobileScreen` 可改半径）。 */
const MOBILE_INFO = {
	version: "0.1.0",
	platform: "android",
	tokenStore: "keystore",
	webview: "146.0.7680.0",
	push: {
		state: "active",
		channel: "unifiedpush",
		distributor: "ntfy",
		endpointHost: "ntfy.sh",
	},
	cornerRadii: { tl: 40, tr: 40, bl: 40, br: 40 },
	insets: { top: 0, right: 0, bottom: 0, left: 0 },
};

/** 假移动壳（android，bridge 2）的命令表；`radius` = 屏幕四角半径。 */
function mobileHandlers(radius: number) {
	return {
		host_ready: { bridge: 2, visible: true },
		host_set_status: null,
		host_set_language: null,
		host_set_chrome: null,
		host_mobile_info: {
			...MOBILE_INFO,
			cornerRadii: { tl: radius, tr: radius, bl: radius, br: radius },
		},
		host_notify: null,
	};
}

/** 移动壳里的界面（390 宽）：独立 context 注入假移动壳（屏幕圆角 `radius`，默认 40）。 */
function mobileScreen(
	name: string,
	prepare: Screen["prepare"],
	radius = 40,
): Screen {
	return {
		name,
		widths: [390],
		init: fakeTauri(mobileHandlers(radius), "android"),
		prepare,
	};
}

/**
 * 打开过的下沉侧栏会在历史里压一条返回标记；同一个页面再 `goto` 同一线程时，SinkLayout 会把落在残留标记上的状态
 * 当成「返回键」再退一步，退回上一张截图的页面。先换到空白页，丢掉这段历史。
 */
async function leaveSink(page: Page) {
	await page.goto("about:blank");
}

/** 手机下沉式侧栏：打开线程页后点页头的侧栏开关（等卡片移到位）。 */
async function openSink(page: Page) {
	await leaveSink(page);
	await openThread(page, MAIN_THREAD);
	await page
		.getByRole("button", { name: "侧边栏", exact: true })
		.first()
		.click();
	await page.locator("[data-testid=sink-card][data-open]").waitFor();
	await settle(page, 600);
}

function launcherScreen(
	name: string,
	state: Record<string, unknown>,
	platform?: "android",
): Screen {
	return {
		name,
		...(platform ? { widths: [390] } : {}),
		init: fakeTauri(
			{
				launcher_state: {
					state,
					lang: "zh-CN",
					version: "0.1.0",
					tokenStore: platform ? "keystore" : "keyring",
				},
				// 移动连接页同步系统栏颜色
				...(platform ? { host_set_chrome: null } : {}),
			},
			false,
			platform,
		),
		prepare: async ({ page }) => {
			await open(page, "/launcher.html", "h1");
		},
	};
}

/** M9：Space 页、全局助手、定时（两种模式的对话框、项目里的列表、侧栏折叠、运行线程的页头）、`[Oryn ` 小签。 */
function m9Screens(): Screen[] {
	const openSpace = (page: Page) =>
		open(page, `/space/${M9_IDS.space}`, "[data-testid=transcript]", 1800);
	const scheduleDialog = (page: Page) =>
		page
			.getByRole("dialog")
			.locator("[data-testid=schedule-preview] li")
			.first()
			.waitFor({ timeout: 15_000 });
	return [
		{ name: "m9-space", prepare: ({ page }) => openSpace(page) },
		{
			name: "m9-space-threads",
			widths: [390],
			prepare: async ({ page }) => {
				await openSpace(page);
				await page.locator("[data-space-tab=threads]").first().click();
				await page.locator("[data-testid=space-progress]").waitFor();
				await settle(page, 600);
			},
		},
		{
			name: "m9-assistant",
			prepare: ({ page }) =>
				open(
					page,
					`/assistant/${M9_IDS.assistant}`,
					"[data-testid=transcript]",
					1800,
				),
		},
		{
			name: "m9-assistant-down",
			init: `localStorage.setItem("oryn.mock.coordinator", "down");`,
			prepare: async ({ page }) => {
				await open(
					page,
					`/assistant/${M9_IDS.assistant}`,
					"[data-testid=transcript]",
					1800,
				);
				await page.getByText("全局助手暂不可用").first().waitFor();
			},
		},
		{
			name: "m9-schedule-thread",
			prepare: async ({ page }) => {
				await openThread(page, M9_IDS.scheduledThread);
				await openThreadActions(page);
				// 宽页头是「定时…」；手机标题菜单里已有定时的线程由「定时」状态行打开
				await page.getByRole("menuitem", { name: /^定时/ }).first().click();
				await scheduleDialog(page);
				await settle(page, 400);
			},
		},
		{
			name: "m9-schedule-project",
			prepare: async ({ page }) => {
				await open(
					page,
					`/settings/automations?schedule=${M9_IDS.projectSchedule}`,
					"main",
				);
				await scheduleDialog(page);
				await page.locator("[data-testid=schedule-runs]").waitFor();
				await settle(page, 400);
			},
		},
		{
			name: "m9-schedule-new",
			prepare: async ({ page }) => {
				await open(page, "/settings/automations", "main");
				await page
					.getByRole("button", { name: "创建", exact: true })
					.first()
					.click();
				await scheduleDialog(page);
				await settle(page, 400);
			},
		},
		{
			name: "m9-project-schedules",
			prepare: async ({ page }) => {
				// M9.6：项目定时直接显示在项目页 /projects/<id>（原设置里的项目列表已搬走）
				await open(page, `/projects/${projects[0]?.id}`, "main");
				await page
					.locator(`[data-testid^=project-schedules-] [data-schedule]`)
					.first()
					.waitFor();
				await settle(page, 400);
			},
		},
		{
			name: "m9-sidebar-stack",
			prepare: async ({ page, width }) => {
				if (width < 600) await openSink(page);
				else await open(page, "/", "textarea", 1000);
				const stack = page.locator(
					`[data-schedule-stack="${M9_IDS.projectSchedule}"] button`,
				);
				await stack.first().click();
				await page
					.locator(`[data-schedule-stack="${M9_IDS.projectSchedule}"] a`)
					.first()
					.waitFor();
				await stack.first().scrollIntoViewIfNeeded();
				await settle(page, 400);
			},
		},
		{
			name: "m9-run-thread",
			prepare: ({ page }) => openThread(page, M9_IDS.run2),
		},
		{
			name: "m9-oryn-label",
			prepare: ({ page }) => openThread(page, M9_IDS.scheduledThread),
		},
	];
}

/** M14：GitHub 设置页（已连接 / 未连接）、线程页头的 PR 徽标与手机标题菜单的 PR 行、改动面板的「推送并开 PR」、项目页 GitHub 卡片。 */
function m14Screens(): Screen[] {
	return [
		{
			name: "m14-settings-github",
			prepare: ({ page }) => open(page, "/settings/github", "main", 1200),
		},
		{
			name: "m14-settings-github-empty",
			init: `localStorage.setItem("oryn.mock.github","none");`,
			prepare: ({ page }) => open(page, "/settings/github", "main", 1200),
		},
		{
			name: "m14-thread-pr",
			prepare: async ({ page, width }) => {
				await openThread(page, MAIN_THREAD);
				if (width >= 600) await page.getByTestId("pr-chip").first().waitFor();
			},
		},
		{
			name: "m14-thread-pr-menu",
			prepare: async ({ page, width }) => {
				await openThread(page, MAIN_THREAD);
				if (width >= 600) {
					await page.getByTestId("pr-chip").first().click();
					await page.getByRole("menu").first().waitFor();
				} else {
					await page.getByTestId("thread-title").click();
					await page.getByRole("menuitem", { name: /#128/ }).first().waitFor();
				}
				await settle(page, 400);
			},
		},
		{
			name: "m14-changes-ship",
			prepare: async ({ page }) => {
				await openThread(page, MAIN_THREAD);
				await openPanel(page, "changes");
				await page
					.getByTestId("ship-button")
					.first()
					.waitFor({ timeout: 15_000 });
				await settle(page, 400);
			},
		},
		{
			name: "m14-project-github",
			prepare: ({ page }) =>
				open(page, `/projects/${projects[0]?.id}`, "main", 1200),
		},
	];
}

const SETTINGS_SECTIONS = [
	"devices",
	"providers",
	"projects",
	"secrets",
	"git-identity",
	"github",
	"agent-config",
	"notifications",
	"shares",
	"appearance",
	"automations",
	"about",
];

const ALL_SIZES = [
	{ width: 360, height: 780 },
	{ width: 390, height: 844 },
	{ width: 1280, height: 860 },
] as const;
/** 没声明 `widths` 的界面截这两种宽度（360 只给显式要它的界面）。 */
const DEFAULT_WIDTHS: readonly number[] = [390, 1280];
/** 统计页（M10）：手机两档 + 桌面。 */
const STATS_WIDTHS = [360, 390, 1280] as const;
/** M11a 的界面：手机两档 + 桌面。 */
const M11A_WIDTHS = [360, 390, 1280] as const;

/** 打开统计页某个标签并等指标行出现。 */
async function openStats(page: Page, path: string, kpi: string) {
	await open(page, path, `[data-testid=kpi-${kpi}]`, 1000);
}

/** 统计页内容区不得横向溢出（内容区裁掉了 x 方向，溢出会被静默切掉，所以量它自己的 scrollWidth）。 */
async function statsNoOverflow({ page }: Shot): Promise<string | null> {
	const r = await page.evaluate(() => {
		const el = document.querySelector<HTMLElement>(
			"[data-testid=stats-scroll]",
		);
		const doc = document.scrollingElement;
		return {
			inner: el ? el.scrollWidth - el.clientWidth : -1,
			doc: doc ? doc.scrollWidth - window.innerWidth : 0,
		};
	});
	if (r.inner < 0) return "找不到 stats-scroll";
	if (r.inner > 1) return `内容区横向溢出 ${r.inner}px`;
	if (r.doc > 0) return `页面横向溢出 ${r.doc}px`;
	return null;
}

/** 高截图的最大高度（CSS 像素）。 */
const TALL_MAX = 16_000;

async function settle(page: Page, ms = 800) {
	await page.evaluate(() => document.fonts.ready.then(() => undefined));
	await page.waitForTimeout(ms);
}

async function open(page: Page, path: string, ready: string, ms = 800) {
	await page.goto(`${BASE}${path}`, { waitUntil: "networkidle" });
	await page.waitForSelector(ready, { timeout: 20_000 });
	await settle(page, ms);
}

/** 打开线程页；等转录与懒加载的高亮 / mermaid / KaTeX。 */
async function openThread(page: Page, id: string) {
	await open(page, `/t/${id}`, "[data-testid=transcript]", 1800);
}

/** 打开线程操作菜单：宽页头点「更多操作」，手机（悬浮页头）点标题胶囊打开标题菜单；两边的菜单项同名。 */
async function openThreadActions(page: Page) {
	const more = page.getByRole("button", { name: "更多操作" }).first();
	if (await more.isVisible().catch(() => false)) await more.click();
	else await page.getByTestId("thread-title").click();
	await page.getByRole("menu").first().waitFor({ timeout: 10_000 });
}

/** 把转录滚到 `frac`（0 顶部，1 底部）处。 */
async function scrollTranscript(page: Page, frac: number) {
	await page.evaluate((f) => {
		const el = document.querySelector<HTMLElement>("[data-testid=transcript]");
		if (el) el.scrollTop = (el.scrollHeight - el.clientHeight) * f;
	}, frac);
	await page.waitForTimeout(600);
}

const composer = (page: Page) => page.getByLabel("消息", { exact: true });

async function sendPrompt(page: Page, text: string) {
	await composer(page).fill(text);
	await composer(page).press("Enter");
}

/**
 * 切到右侧面板的指定标签：宽屏先打开面板列再点标签；medium 点线程页的同级标签；
 * 手机（compact）没有常驻标签条，经页头右上角的视图菜单（`thread-view-menu` → `[data-view]`）切换。
 */
async function openPanel(
	page: Page,
	tab: "info" | "changes" | "files" | "terminal" | "portal" | "desktop",
) {
	const views = page.locator("[data-testid=thread-view-menu]").first();
	if (await views.isVisible().catch(() => false)) {
		await views.click();
		const item = page.locator(`[data-view=${tab}]`);
		await item.waitFor({ timeout: 10_000 });
		if ((await item.getAttribute("aria-checked")) === "true")
			await page.keyboard.press("Escape");
		else await item.click();
		await page.waitForTimeout(400);
		return;
	}
	const btn = page.locator(`[data-panel-tab=${tab}]`);
	if (!(await btn.isVisible().catch(() => false)))
		await page.getByRole("button", { name: "信息面板", exact: true }).click();
	await btn.waitFor({ timeout: 10_000 });
	if ((await btn.getAttribute("aria-selected")) !== "true") await btn.click();
	await page.waitForTimeout(400);
}

async function openTerminal(page: Page) {
	await openPanel(page, "terminal");
	const wake = page.locator("[data-testid=terminal-offline] button");
	if (await wake.isVisible().catch(() => false)) await wake.click();
	const view = page.locator("[data-testid=terminal-view]");
	await page.waitForTimeout(800);
	if ((await page.locator("[data-terminal-tab]").count()) === 0)
		await page.getByRole("button", { name: "新建终端", exact: true }).click();
	await view.first().waitFor({ timeout: 15_000 });
	await view.first().click();
	await page.keyboard.type("echo hi", { delay: 10 });
	await page.keyboard.press("Enter");
	await page.waitForTimeout(800);
}

/**
 * 拉高视口直到转录无需滚动（虚拟列表随之挂载全部行）；`expand` 时同时展开全部折叠的卡片。
 * 视口拉高后新挂载的行又是折叠的，展开后的卡片（高亮、图表）也要晚一步才长高：每轮点完都等
 * 转录高度稳定下来再量，且要连续两轮「没点到新折叠项、无需滚动」才算完；否则视口停在半高，
 * 贴底跟随把转录滚到中段，截图缺了前半段。
 */
async function growToTranscript(s: Shot, expand: boolean) {
	const transcript = s.page.locator("[data-testid=transcript]");
	let stable = 0;
	for (let round = 0; round < 16 && stable < 2; round++) {
		let clicked = 0;
		if (expand) {
			// 虚拟列表会重挂载行，逐个用 locator 点击容易等到超时；直接在页面里点掉全部折叠项
			clicked = await transcript.evaluate((el) => {
				const buttons = el.querySelectorAll<HTMLButtonElement>(
					"button[aria-expanded=false]",
				);
				for (const b of buttons) b.click();
				return buttons.length;
			});
		}
		const scroll = await settledHeight(s.page);
		const client = await transcript.evaluate((el) => el.clientHeight);
		const vp = s.page.viewportSize();
		if (!vp) return;
		const missing = scroll - client;
		const more = expand
			? await transcript.locator("button[aria-expanded=false]").count()
			: 0;
		if (clicked === 0 && missing <= 1 && more === 0) {
			stable++;
			continue;
		}
		stable = 0;
		// 刚点开的卡片可能还没长高：不拉视口，下一轮再量（不能就此收手，否则停在半高）
		if (missing <= 1) continue;
		const height = Math.min(TALL_MAX, vp.height + missing);
		if (height === vp.height) break; // 已到截图高度上限
		await s.page.setViewportSize({ width: vp.width, height });
	}
	await scrollTranscript(s.page, 0);
	await settle(s.page, 1200);
}

/** 等转录的 scrollHeight 连续 600ms 不再变化（最多 4 秒），返回稳定后的值。 */
async function settledHeight(page: Page): Promise<number> {
	const transcript = page.locator("[data-testid=transcript]");
	let last = await transcript.evaluate((el) => el.scrollHeight);
	let still = 0;
	for (let t = 0; t < 4000 && still < 600; t += 150) {
		await page.waitForTimeout(150);
		const now = await transcript.evaluate((el) => el.scrollHeight);
		still = now === last ? still + 150 : 0;
		last = now;
	}
	return last;
}

/** 输入坞断言：最后一条转录行的底边不得低于输入框所在托盘的顶边。 */
async function dockOverlap(page: Page): Promise<string | null> {
	return page.evaluate(() => {
		const ta =
			document.querySelector<HTMLTextAreaElement>(
				'textarea[aria-label="消息"]',
			) ?? document.querySelector<HTMLTextAreaElement>("textarea");
		if (!ta) return "找不到输入框 textarea";
		const tray = ta.closest(".glass") ?? ta.parentElement;
		const rows = document.querySelectorAll(
			"[data-testid=transcript] [data-role]",
		);
		const last = rows[rows.length - 1];
		if (!tray || !last) return "找不到托盘或转录行";
		const bottom = last.getBoundingClientRect().bottom;
		const top = tray.getBoundingClientRect().top;
		return bottom <= top + 0.5
			? null
			: `最后一条转录行底边 ${bottom.toFixed(1)} 低于托盘顶边 ${top.toFixed(1)}`;
	});
}

async function nextFrames(page: Page) {
	await page.evaluate(() => {
		const { promise, resolve } = Promise.withResolvers<void>();
		requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
		return promise;
	});
}

/**
 * 只滚元素所在的内部滚动容器，把它放到容器顶部（`start`，留 64px 给页头玻璃）或中间（`center`）。
 * 不用 `scrollIntoView`：它会连带滚动根元素，把整个外壳推出视口。
 */
async function scrollWithin(
	page: Page,
	selector: string,
	block: "start" | "center",
) {
	await page.evaluate(
		([sel, where]) => {
			const el = document.querySelector<HTMLElement>(sel as string);
			let box = el?.parentElement ?? null;
			while (box && box.scrollHeight <= box.clientHeight + 1)
				box = box.parentElement;
			if (!el || !box) return;
			const r = el.getBoundingClientRect();
			const b = box.getBoundingClientRect();
			box.scrollTop +=
				where === "start"
					? r.top - b.top - 64
					: r.top + r.height / 2 - (b.top + b.height / 2);
		},
		[selector, block],
	);
	await settle(page, 400);
}

const SCREENS: Screen[] = [
	{
		name: "home",
		prepare: ({ page }) => open(page, "/", "textarea", 1200),
	},
	{
		name: "thread-top",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await scrollTranscript(page, 0);
		},
	},
	{
		name: "thread-mid",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await scrollTranscript(page, 0.5);
		},
	},
	{
		name: "thread-bottom",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await scrollTranscript(page, 1);
		},
		verify: async ({ page }) => {
			const first = await dockOverlap(page);
			if (first) return first;
			// 模拟软键盘弹出 300px
			await page.evaluate(() =>
				document.documentElement.style.setProperty("--kb-inset", "300px"),
			);
			await nextFrames(page);
			await scrollTranscript(page, 1);
			await nextFrames(page);
			const second = await dockOverlap(page);
			return second ? `软键盘 300px 时：${second}` : null;
		},
	},
	{
		name: "panel-info",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await openPanel(page, "info");
		},
	},
	{
		name: "panel-changes",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await openPanel(page, "changes");
			const file = page
				.locator("ul[aria-label=改动的文件] button[aria-current]")
				.filter({ hasText: "main.ts" });
			await file.waitFor({ timeout: 15_000 });
			await file.click();
			await page
				.locator("[data-testid=diff-rows]")
				.waitFor({ timeout: 15_000 });
			await settle(page);
		},
	},
	{
		name: "panel-files",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await openPanel(page, "files");
			const item = page.locator('[role=treeitem][data-path$="/src/main.ts"]');
			if (!(await item.isVisible().catch(() => false))) {
				const src = page.locator('[role=treeitem][data-path$="/src"]');
				await src.waitFor({ timeout: 15_000 });
				await src.click();
			}
			await item.waitFor({ timeout: 15_000 });
			await item.click();
			await page.locator(".cm-editor").waitFor({ timeout: 15_000 });
			await settle(page);
		},
	},
	{
		name: "panel-terminal",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await openTerminal(page);
		},
	},
	{
		name: "thread-ask",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await sendPrompt(page, "跑一下 uname -a，ask 我要不要保留主机名");
			await page
				.getByText("输出要包含主机名吗？")
				.first()
				.waitFor({ timeout: 30_000 });
			await settle(page);
		},
	},
	{
		name: "thread-plan",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await sendPrompt(page, "先跑 uname -a，再给我一份计划");
			await page.getByText("重连改造").first().waitFor({ timeout: 30_000 });
			await settle(page);
		},
	},
	{
		name: "thread-slash",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await composer(page).fill("/");
			await page.getByRole("listbox").waitFor({ timeout: 10_000 });
			await settle(page, 400);
		},
	},
	{
		name: "thread-menu",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await openThreadActions(page);
			await settle(page, 400);
		},
	},
	{
		name: "tools",
		tall: true,
		prepare: async (s) => {
			await openThread(s.page, TOOL_ZOO_THREAD);
			await growToTranscript(s, false);
		},
	},
	{
		name: "tools-expanded",
		tall: true,
		prepare: async (s) => {
			await openThread(s.page, TOOL_ZOO_THREAD);
			await growToTranscript(s, true);
		},
	},
	...(
		[
			["env-setup-failed", ENV_THREADS.setupFailed],
			["env-orb-failed", ENV_THREADS.orbFailed],
			["env-missing-config", ENV_THREADS.missingConfig],
			["env-slow-setup", ENV_THREADS.slowSetup],
		] as const
	).map(
		([name, id]): Screen => ({
			name,
			prepare: async ({ page }) => {
				await openThread(page, id);
				await page
					.locator("[data-testid=env-banner]")
					.waitFor({ timeout: 15_000 });
				await settle(page);
			},
		}),
	),
	...SETTINGS_SECTIONS.map(
		(section): Screen => ({
			name: `settings-${section}`,
			prepare: async ({ page }) => {
				await open(page, `/settings/${section}`, "main", 400);
				await page.getByRole("heading").first().waitFor({ timeout: 15_000 });
				await settle(page);
			},
		}),
	),
	{
		// 侧栏铃铛：未读计数 + 打开的通知弹层（compact 档是 Dialog，先开侧栏抽屉）
		name: "notify-bell",
		prepare: async ({ page, width }) => {
			await open(page, "/", "textarea");
			if (width < 600)
				await page.getByRole("button", { name: "侧边栏", exact: true }).click();
			await page.getByRole("button", { name: /^通知（/ }).click();
			await page
				.getByRole("dialog", { name: "通知" })
				.waitFor({ timeout: 10_000 });
			await settle(page);
		},
	},
	{
		// 应用内 toast：与 socket 收到 WS `notification` 走同一个 store 入口
		name: "notify-toast",
		prepare: async ({ page }) => {
			await open(page, "/", "textarea");
			await page.evaluate(async (threadId) => {
				const m = await import(
					/* @vite-ignore */ "/src/state/notifications.ts"
				);
				m.notificationsStore.getState().onNotification({
					id: "ntf_shot",
					kind: "input",
					threadId,
					title: "🐳 DOCKER: Docker provider 冻结后 orbd 重连",
					body: "允许执行 `docker exec orb-… pkill -HUP -x orbd`？",
					url: `/t/${threadId}`,
					createdAt: Date.now(),
					deliveries: [],
				});
			}, "T-0193a1b2-0000-7000-8000-00000000a004");
			await page
				.locator("[role=status].glass")
				.first()
				.waitFor({ timeout: 5_000 });
			await settle(page, 400);
		},
	},
	{
		name: "login",
		prepare: async ({ page }) => {
			await open(page, "/login", "body");
			await page.getByRole("button").first().waitFor({ timeout: 15_000 });
			await settle(page);
		},
	},
	{
		name: "setup",
		prepare: async ({ page }) => {
			await open(page, "/setup", "body");
			await page.getByRole("button").first().waitFor({ timeout: 15_000 });
			await settle(page);
		},
	},
	{
		name: "pair",
		prepare: async ({ page }) => {
			await open(page, "/pair", "body");
			const code = page.getByLabel("配对码", { exact: true });
			await code.waitFor({ timeout: 15_000 });
			await code.fill("ABCD-EFGH");
			await page.getByRole("button", { name: "查看", exact: true }).click();
			await page
				.getByRole("button", { name: "批准", exact: true })
				.waitFor({ timeout: 15_000 });
			await settle(page);
		},
	},
	{
		name: "dialog-model",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await composer(page).fill("/model");
			await composer(page).press("Enter");
			await page.getByRole("dialog").waitFor({ timeout: 10_000 });
			await settle(page);
		},
	},
	{
		name: "dialog-tree",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			const tree = page.getByRole("button", { name: "会话树", exact: true });
			if (await tree.isVisible().catch(() => false)) await tree.click();
			else {
				await openThreadActions(page);
				await page.getByRole("menuitem", { name: "会话树" }).click();
			}
			await page.getByRole("dialog").waitFor({ timeout: 10_000 });
			await settle(page);
		},
	},
	{
		name: "dialog-attachments",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await openThreadActions(page);
			await page.getByRole("menuitem", { name: "附件" }).click();
			await page.getByRole("dialog").waitFor({ timeout: 10_000 });
			await settle(page);
		},
	},
	{
		// 匿名分享页（快照）：只读转录，从开头读起
		name: "share-page",
		prepare: ({ page }) =>
			open(
				page,
				`/s/${MOCK_SHARE_SLUGS.snapshot}`,
				"[data-testid=transcript]",
				1800,
			),
	},
	{
		// 匿名分享页（实时）：经匿名 WS 补发，贴底跟随
		name: "share-page-live",
		prepare: ({ page }) =>
			open(
				page,
				`/s/${MOCK_SHARE_SLUGS.live}`,
				"[data-testid=transcript]",
				1800,
			),
	},
	{
		name: "share-dialog",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await openShareDialog(page);
		},
	},
	{
		// 先发一条含疑似密钥的消息，再创建链接：对话框列出扫描命中项，「仍然创建」在勾选前禁用
		name: "share-dialog-findings",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await sendPrompt(
				page,
				"原样输出这一行：sk-test-abcdefghijklmnopqrstuvwx0123",
			);
			await page
				.locator("[data-testid=transcript] [data-role=user]")
				.filter({ hasText: "sk-test-" })
				.last()
				.waitFor({ timeout: 15_000 });
			await openShareDialog(page);
			await page
				.getByRole("dialog")
				.getByRole("button", { name: "创建链接", exact: true })
				.click();
			await page
				.locator("[data-testid=share-findings]")
				.waitFor({ timeout: 10_000 });
			await settle(page);
		},
	},
	// 手机下沉式侧栏：浏览器（屏幕圆角 0 → 卡片取 2xl 令牌）与移动壳（屏幕圆角 47）各一套，关 / 开两态
	{
		name: "sink-open",
		widths: [390],
		prepare: ({ page }) => openSink(page),
	},
	mobileScreen(
		"mobile-sink-closed-r47",
		({ page }) => openThread(page, MAIN_THREAD),
		47,
	),
	mobileScreen("mobile-sink-open-r47", ({ page }) => openSink(page), 47),
	// M9：手机底部胶囊属于侧栏层——侧栏关闭时被卡片盖住，打开时露出；项目 / 更多两种视图
	{
		name: "sink-closed",
		widths: [390],
		prepare: async ({ page }) => {
			await leaveSink(page);
			await openThread(page, MAIN_THREAD);
		},
	},
	{
		name: "sink-projects",
		widths: [390],
		prepare: async ({ page }) => {
			await openSink(page);
			await page.locator("[data-tab=projects]").click();
			await page.locator("[data-testid=sidebar-projects]").waitFor();
			await settle(page, 400);
		},
	},
	{
		name: "sink-more",
		widths: [390],
		prepare: async ({ page }) => {
			await openSink(page);
			await page.locator("[data-tab=more]").click();
			await page.locator("[data-testid=sidebar-more]").waitFor();
			await settle(page, 400);
		},
	},
	...m9Screens(),
	...m14Screens(),
	// 手机线程页的视图菜单（对话 / 改动 / 文件 / 终端 … / 信息），展开状态
	{
		name: "thread-compact-views",
		widths: [390],
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await page.locator("[data-testid=thread-view-menu]").first().click();
			await page.locator("[data-view=agent]").waitFor();
			await settle(page, 600);
		},
	},
	{
		// 手机：往上翻历史，输入坞收成信息胶囊
		name: "thread-compact-pill",
		widths: [390],
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await page.mouse.move(195, 400);
			await page.mouse.wheel(0, -600);
			await page.locator("[data-testid=dock-tray][data-dock=mini]").waitFor();
			await settle(page, 600);
		},
	},
	{
		name: "thread-compact-terminal",
		widths: [390],
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await openTerminal(page);
			await settle(page, 600);
		},
	},
	// M7：移动壳（假 android 壳，屏幕圆角 40px）——推送卡与贴边浮层的同心圆角自查
	mobileScreen("settings-mobile-push", async ({ page }) => {
		await open(page, "/settings/notifications", "main", 400);
		await page
			.locator("[data-testid=mobile-push-card]")
			.waitFor({ timeout: 15_000 });
		await settle(page);
	}),
	mobileScreen("mobile-corners-dialog", async ({ page }) => {
		await openThread(page, MAIN_THREAD);
		await openThreadActions(page);
		await page.getByRole("menuitem", { name: "附件" }).click();
		await page.getByRole("dialog").waitFor({ timeout: 10_000 });
		await settle(page);
	}),
	// M4：Portal 标签、访问弹层两种状态、信息面板的服务小节与日志对话框
	{
		name: "portal-panel",
		prepare: async ({ page }) => {
			await openPortal(page);
		},
	},
	{
		name: "portal-access",
		prepare: async ({ page }) => {
			await openPortal(page);
			await page.getByRole("button", { name: "访问权限", exact: true }).click();
			await page
				.locator("[data-testid=portal-access][data-state=private]")
				.waitFor({ timeout: 10_000 });
			await page
				.getByRole("button", { name: "公开 1 小时", exact: true })
				.click();
			await settle(page, 400);
		},
	},
	{
		name: "portal-access-public",
		prepare: async ({ page }) => {
			await openPortal(page);
			await page.getByRole("button", { name: "访问权限", exact: true }).click();
			await page
				.getByRole("button", { name: "公开 1 天", exact: true })
				.click();
			await page.getByRole("button", { name: "确认公开", exact: true }).click();
			await page
				.locator("[data-testid=portal-access][data-state=public]")
				.waitFor({ timeout: 10_000 });
			await settle(page, 400);
		},
	},
	{
		name: "portal-services",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await openPanel(page, "info");
			const section = page.locator("[data-testid=orb-services]");
			await section.waitFor({ timeout: 10_000 });
			await section.scrollIntoViewIfNeeded();
			await settle(page, 400);
		},
	},
	{
		name: "portal-service-log",
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await openPanel(page, "info");
			await page
				.getByRole("button", { name: "worker 的日志", exact: true })
				.click();
			await page
				.locator("[data-testid=service-log]")
				.waitFor({ timeout: 10_000 });
			await settle(page, 400);
		},
	},
	launcherScreen("launcher-connect", {
		step: "connect",
		serverUrl: "",
	}),
	launcherScreen("launcher-pairing", {
		step: "pairing",
		serverUrl: LAUNCHER_SERVER,
		code: "K7QM-W3XP",
		verifyUrl: `${LAUNCHER_SERVER}/pair`,
		expiresAt: 0, // fakeTauri 改成截图时刻 + 9:30
	}),
	launcherScreen("launcher-error", {
		step: "error",
		serverUrl: LAUNCHER_SERVER,
		error: { code: "unreachable", message: "error sending request" },
	}),
	// M7：移动连接页（无「Oryn Desktop」与桌面凭据库提示；扫码按钮；复制配对码并打开浏览器）
	launcherScreen(
		"launcher-mobile-connect",
		{ step: "connect", serverUrl: "" },
		"android",
	),
	launcherScreen(
		"launcher-mobile-pairing",
		{
			step: "pairing",
			serverUrl: LAUNCHER_SERVER,
			code: "K7QM-W3XP",
			verifyUrl: `${LAUNCHER_SERVER}/pair`,
			expiresAt: 0,
		},
		"android",
	),
	{
		// 设置 → 桌面应用：已下载新版本、令牌存在文件里（两种提示都在图上）
		name: "settings-desktop",
		init: fakeTauri(
			{
				host_ready: { bridge: 1, visible: true },
				host_set_status: null,
				host_set_language: null,
				host_info: {
					version: "0.1.0",
					settings: {
						shortcut: "Control+Alt+Space",
						closeToTray: true,
						autoUpdate: false,
					},
					shortcutError: null,
					tokenStore: "file",
					update: { state: "ready", version: "0.2.0" },
				},
			},
			true,
		),
		prepare: async ({ page }) => {
			await open(page, "/settings/desktop", "main", 400);
			await page
				.getByRole("heading", { name: "桌面应用" })
				.first()
				.waitFor({ timeout: 15_000 });
			await settle(page);
		},
	},
	// M8：Desktop 标签（已连接 / 浏览器未运行 + 被占用横幅 / 冲突对话框 / 只读（被接管）/ 触屏附加键）、
	// 信息面板的 Profile 行、设置页列表、新建线程框的 Profile 弹层与 409 冲突对话框
	{
		name: "desktop-connected",
		prepare: async ({ page }) => {
			await openDesktop(page, DESKTOP_THREADS.connected);
		},
	},
	{
		name: "desktop-in-use",
		prepare: async ({ page }) => {
			await openDesktop(page, DESKTOP_THREADS.inUse);
			await page
				.locator("[data-testid=desktop-start-browser]")
				.waitFor({ timeout: 10_000 });
		},
	},
	{
		name: "desktop-conflict",
		prepare: async ({ page }) => {
			await openDesktop(page, DESKTOP_THREADS.inUse);
			await page
				.getByRole("button", { name: "选择使用方式…", exact: true })
				.click();
			await page
				.locator("[data-testid=profile-conflict]")
				.waitFor({ timeout: 10_000 });
			await settle(page, 400);
		},
	},
	{
		name: "desktop-readonly",
		prepare: async ({ page }) => {
			await openDesktop(page, DESKTOP_THREADS.takenOver);
		},
	},
	{
		name: "desktop-menu",
		widths: [1280],
		prepare: async ({ page }) => {
			await openDesktop(page, DESKTOP_THREADS.connected);
			await page.locator("[data-testid=desktop-profile-menu]").click();
			await page.getByRole("menu").waitFor({ timeout: 10_000 });
			await settle(page, 400);
		},
	},
	{
		name: "desktop-clipboard",
		prepare: async ({ page }) => {
			await openDesktop(page, DESKTOP_THREADS.connected);
			await page.getByRole("button", { name: "剪贴板", exact: true }).click();
			await page
				.locator("[data-testid=desktop-clipboard]")
				.waitFor({ timeout: 10_000 });
			await settle(page, 400);
		},
	},
	{
		name: "desktop-compact-keys",
		widths: [390],
		touch: true,
		prepare: async ({ page }) => {
			await openDesktop(page, DESKTOP_THREADS.connected);
			await page.locator("[data-testid=desktop-keyboard]").click();
			await page
				.locator("[data-testid=desktop-softkeys]")
				.waitFor({ timeout: 10_000 });
			await settle(page, 400);
		},
	},
	{
		name: "desktop-info",
		prepare: async ({ page }) => {
			await openThread(page, DESKTOP_THREADS.takenOver);
			await openPanel(page, "info");
			const line = page.locator("[data-testid=info-desktop-profile]");
			await line.waitFor({ timeout: 10_000 });
			await line.scrollIntoViewIfNeeded();
			await settle(page, 400);
		},
	},
	{
		name: "desktop-settings",
		prepare: async ({ page }) => {
			await open(page, "/settings/browser-profiles", "main", 400);
			await page
				.locator("[data-testid=browser-profile-row]")
				.first()
				.waitFor({ timeout: 15_000 });
			await settle(page);
		},
	},
	{
		name: "desktop-new-thread",
		prepare: async ({ page }) => {
			await open(page, "/", "textarea");
			await page
				.getByRole("button", { name: /^浏览器 Profile：/ })
				.click({ timeout: 15_000 });
			await page
				.getByRole("button", { name: /^跟随项目默认/ })
				.waitFor({ timeout: 10_000 });
			await settle(page, 400);
		},
	},
	{
		name: "desktop-new-thread-conflict",
		prepare: async ({ page }) => {
			await open(page, "/", "textarea");
			await page
				.getByRole("button", { name: /^浏览器 Profile：/ })
				.click({ timeout: 15_000 });
			await page.getByRole("button", { name: /^GitHub 测试号/ }).click();
			await page.locator("textarea").first().fill("登录 GitHub 看看通知");
			await page.getByRole("button", { name: "发送", exact: true }).click();
			await page
				.locator("[data-testid=profile-conflict]")
				.waitFor({ timeout: 10_000 });
			await settle(page, 400);
		},
	},
	// 桌面 App 无系统标题栏（假桌面壳）：macOS 原生红绿灯在左（占位圆点）、Windows 自绘按钮在右、
	// Linux 按 GTK 布局放左；另有最大化后（还原图标）+ 窗口失焦变淡的一张
	...chromeScreens("mac", "macos"),
	...chromeScreens("win", "windows"),
	...chromeScreens("linux-left", "linux-left").filter((s) =>
		/-(thread|collapsed|thread-panel|launcher)$/.test(s.name),
	),
	...chromeExtraScreens("mac-fs", "macos", { fullscreen: true }),
	...chromeExtraScreens("mac-256", "macos", { narrow: true }),
	...chromeExtraScreens("mac-fs-256", "macos", {
		narrow: true,
		fullscreen: true,
	}),
	...chromeExtraScreens("win-256", "windows", { narrow: true }),
	...chromeExtraScreens("linux-left-256", "linux-left", { narrow: true }),
	{
		name: "chrome-win-maximized-blur",
		widths: [1280],
		init: chromeShell("windows", {
			window: { maximized: true, fullscreen: false, focused: false },
		}),
		prepare: async ({ page }) => {
			await openThread(page, MAIN_THREAD);
			await closePanel(page);
		},
	},
	// 统计页（M10）：三个标签、主图浮层、orb 抽屉、预算对话框；手机另截 360
	{
		name: "stats-usage",
		widths: STATS_WIDTHS,
		prepare: ({ page }) => openStats(page, "/stats/usage", "cost"),
		verify: statsNoOverflow,
	},
	{
		name: "stats-usage-tables",
		widths: STATS_WIDTHS,
		prepare: async ({ page }) => {
			await openStats(page, "/stats/usage", "cost");
			await page
				.locator("[data-testid=table-thread] [data-row-key]")
				.first()
				.waitFor({ timeout: 10_000 });
			// 按模型表顶对齐：看品牌 logo + 淡色短名，下方接着项目 / 线程表
			await page.evaluate(() => {
				const el = document.querySelector("[data-testid=stats-scroll]");
				const table = document.querySelector("[data-testid=table-model]");
				if (!el || !table) return;
				const top =
					table.getBoundingClientRect().top -
					el.getBoundingClientRect().top +
					el.scrollTop -
					12;
				el.scrollTo({ top });
			});
			await settle(page, 400);
		},
		verify: statsNoOverflow,
	},
	{
		name: "stats-usage-tooltip",
		prepare: async ({ page }) => {
			await openStats(page, "/stats/usage?range=7d&by=project", "cost");
			const bars = page.locator("[data-testid=stats-chart] g[data-bucket]");
			await bars.nth((await bars.count()) - 2).click();
			await page.locator("[data-testid=chart-tooltip]").waitFor();
			await settle(page, 300);
		},
	},
	{
		name: "stats-orbs",
		widths: STATS_WIDTHS,
		prepare: ({ page }) => openStats(page, "/stats/orbs", "running"),
		verify: statsNoOverflow,
	},
	{
		name: "stats-orb-drawer",
		widths: STATS_WIDTHS,
		prepare: async ({ page }) => {
			await openStats(page, "/stats/orbs", "running");
			await page.locator("[data-orb-row]").first().click();
			await page
				.locator("[data-testid=orb-drawer] [data-testid=timeline-strip]")
				.waitFor({ timeout: 10_000 });
			await settle(page, 600);
		},
	},
	{
		name: "stats-output",
		widths: STATS_WIDTHS,
		prepare: ({ page }) => openStats(page, "/stats/output", "shipped"),
		verify: statsNoOverflow,
	},
	{
		name: "stats-budget",
		prepare: async ({ page }) => {
			await openStats(page, "/stats/usage", "cost");
			await page
				.locator("[data-testid=budget-card]")
				.locator("xpath=ancestor::section[1]")
				.getByRole("button", { name: "设置预算" })
				.click();
			await page.getByRole("dialog", { name: "设置预算" }).waitFor();
			await settle(page, 400);
		},
	},
	// M11a：项目设置里的快照开关与预热步进器、快照与预热卡、信息面板的快照来源、执行位置选择器的 Docker 徽标
	{
		name: "m11a-project-settings",
		widths: M11A_WIDTHS,
		prepare: async ({ page }) => {
			await open(
				page,
				`/projects/${projects[0]?.id}`,
				"[data-testid=project-accel]",
			);
			await scrollWithin(page, "[data-testid=warm-orbs-value]", "center");
		},
	},
	{
		name: "m11a-project-accel",
		widths: M11A_WIDTHS,
		prepare: async ({ page }) => {
			await open(
				page,
				`/projects/${projects[0]?.id}`,
				"[data-testid=snapshot-list]",
			);
			await scrollWithin(page, "[data-testid=project-accel]", "start");
		},
	},
	{
		name: "m11a-panel-snapshot",
		widths: M11A_WIDTHS,
		prepare: async ({ page }) => {
			await openThread(page, M11A_IDS.snapshotThread);
			await openPanel(page, "info");
			await page
				.locator("[data-testid=env-snapshot]")
				.waitFor({ timeout: 10_000 });
			await scrollWithin(page, "[data-testid=env-snapshot]", "center");
		},
	},
	{
		name: "m11a-executor",
		widths: M11A_WIDTHS,
		prepare: async ({ page }) => {
			await open(page, "/", "textarea");
			await page.getByRole("button", { name: /新 orb（默认）/ }).click();
			await settle(page, 400);
		},
	},
	// M11b：执行位置的 Runners 分组、runner 线程（页头 runner · cwd、信息面板、没有终端标签）、离线提示、
	// 目录被占用对话框、归档带「同时移除 worktree」、设置 → Runners（新建后的令牌与启动命令）
	{
		name: "m11b-executor",
		prepare: async ({ page }) => {
			await open(page, "/", "textarea");
			await page.getByRole("button", { name: /新 orb（默认）/ }).click();
			await page.getByRole("button", { name: /^laptop/ }).click();
			await page.getByRole("button", { name: "新 worktree" }).first().click();
			await settle(page, 400);
		},
	},
	{
		name: "m11b-dir-in-use",
		prepare: async ({ page }) => {
			await open(page, "/", "textarea");
			await page.locator("textarea").fill("Fix login flicker");
			await page.getByRole("button", { name: /新 orb（默认）/ }).click();
			await page.getByRole("button", { name: /^laptop/ }).click();
			await page.getByRole("button", { name: /^repo/ }).click();
			await page.keyboard.press("Escape");
			await page.getByRole("button", { name: "发送" }).click();
			await page.locator("[data-testid=dir-in-use]").waitFor();
			await settle(page, 400);
		},
	},
	{
		name: "m11b-thread",
		prepare: async ({ page }) => {
			await openThread(page, M11B_IDS.runnerThread);
			await openPanel(page, "info");
			await page.locator("[data-testid=runner-specs]").waitFor();
			await settle(page, 400);
		},
	},
	{
		name: "m11b-offline",
		prepare: async ({ page }) => {
			await openThread(page, M11B_IDS.offlineThread);
			await page.locator("[data-testid=runner-offline]").waitFor();
			await settle(page, 400);
		},
	},
	{
		name: "m11b-archive",
		prepare: async ({ page }) => {
			await openThread(page, M11B_IDS.worktreeThread);
			await openThreadActions(page);
			await page.getByRole("menuitem", { name: "归档", exact: true }).click();
			await page.locator("[data-testid=archive-dialog]").waitFor();
			await settle(page, 400);
		},
	},
	{
		name: "m11b-settings",
		prepare: async ({ page }) => {
			await open(
				page,
				"/settings/runners",
				"[data-testid=runner-settings-row]",
			);
			await page.getByRole("textbox", { name: "名字" }).fill("studio-2");
			await page.getByRole("button", { name: "创建", exact: true }).click();
			await page.locator("[data-testid=runner-created]").waitFor();
			await settle(page, 400);
		},
	},
	{
		// 面板最窄（360px）时五个标签只显示图标、不换行；放在最后：它改了本条流水线的面板宽度
		name: "portal-narrow",
		widths: [1280],
		prepare: async ({ page }) => {
			await page.goto(`${BASE}/`, { waitUntil: "networkidle" });
			await page.evaluate(() =>
				localStorage.setItem("oryn.panel.width", "360"),
			);
			await openPortal(page);
		},
	},
];

/** 线程页「更多操作 → 分享…」。 */
async function openShareDialog(page: Page) {
	await openThreadActions(page);
	await page.getByRole("menuitem", { name: "分享…" }).click();
	await page.getByRole("dialog").waitFor({ timeout: 10_000 });
	await settle(page);
}

/** 打开主线程的 Portal 标签并等 iframe 出现。 */
async function openPortal(page: Page) {
	await openThread(page, MAIN_THREAD);
	await openPanel(page, "portal");
	await page.locator("[data-testid=portal-frame]").waitFor({ timeout: 15_000 });
	await settle(page, 600);
}

/** 打开线程的 Desktop 标签并等桌面连上（mock 会话立即 connected）。 */
async function openDesktop(page: Page, threadId: string) {
	await openThread(page, threadId);
	await openPanel(page, "desktop");
	await page
		.locator("[data-testid=desktop-status][data-state=connected]")
		.waitFor({ timeout: 15_000 });
	await settle(page, 600);
}

/** 一条宽度 × 明暗的流水线：依次截完全部界面。 */
async function shootLane(
	browser: Browser,
	size: (typeof ALL_SIZES)[number],
	scheme: Scheme,
	screens: Screen[],
	failures: string[],
): Promise<string[]> {
	const written: string[] = [];
	const contexts = new Map<number, BrowserContext>();
	const pageFor = async (dsf: number): Promise<Page> => {
		let context = contexts.get(dsf);
		if (!context) {
			context = await browser.newContext({
				viewport: { width: size.width, height: size.height },
				deviceScaleFactor: dsf,
				locale: "zh-CN",
				colorScheme: scheme,
				// 截静态图：关掉动效，品牌标的光标不会恰好闪到熄灭帧，过渡也不会停在半途
				reducedMotion: "reduce",
			});
			if (ACCENT) {
				await context.addInitScript(
					(value) => {
						try {
							localStorage.setItem("oryn.theme", value);
						} catch {
							// 无 localStorage
						}
					},
					JSON.stringify({ mode: scheme, accent: ACCENT }),
				);
			}
			contexts.set(dsf, context);
		}
		return context.pages()[0] ?? context.newPage();
	};
	for (const screen of screens) {
		if (!(screen.widths ?? DEFAULT_WIDTHS).includes(size.width)) continue;
		const id = `${screen.name}-${size.width}-${scheme}`;
		const isolated =
			screen.init || screen.touch
				? await browser.newContext({
						viewport: { width: size.width, height: size.height },
						deviceScaleFactor: 2,
						locale: "zh-CN",
						colorScheme: scheme,
						reducedMotion: "reduce",
						...(screen.touch ? { hasTouch: true, isMobile: true } : {}),
					})
				: null;
		if (isolated && (screen.init || ACCENT)) {
			if (ACCENT)
				await isolated.addInitScript({
					content: `localStorage.setItem("oryn.theme", ${JSON.stringify(JSON.stringify({ mode: scheme, accent: ACCENT }))});`,
				});
			if (screen.init) await isolated.addInitScript({ content: screen.init });
		}
		const page = isolated
			? await isolated.newPage()
			: await pageFor(screen.tall ? 1 : 2);
		await page.setViewportSize({ width: size.width, height: size.height });
		const shot: Shot = { page, width: size.width, scheme };
		try {
			await screen.prepare(shot);
			const file = `${OUT}/${id}.png`;
			await page.screenshot({ path: file });
			written.push(file);
			console.log(file);
			if (CHECK && screen.verify) {
				const problem = await screen.verify(shot);
				if (problem) failures.push(`${id}：断言失败：${problem}`);
				else console.log(`${id}：断言通过`);
			}
		} catch (e) {
			failures.push(`${id}：${e instanceof Error ? e.message : String(e)}`);
			// 失败现场另存到 failed/，不计入矩阵
			await mkdir(`${OUT}/failed`, { recursive: true });
			await page
				.screenshot({ path: `${OUT}/failed/${id}.png` })
				.catch(() => {});
		} finally {
			await isolated?.close();
		}
	}
	for (const context of contexts.values()) await context.close();
	return written;
}

async function shootAll(browser: Browser): Promise<boolean> {
	const screens = SCREENS.filter(
		(s) => !ONLY || ONLY.some((prefix) => s.name.startsWith(prefix)),
	);
	if (screens.length === 0) {
		console.error(`--only ${ONLY?.join(",")} 没有匹配任何界面`);
		return false;
	}
	const schemes: Scheme[] =
		SCHEME_ARG === "both" ? ["light", "dark"] : [SCHEME_ARG as Scheme];
	const failures: string[] = [];
	const lanes = schemes.flatMap((scheme) =>
		ALL_SIZES.map((size) =>
			shootLane(browser, size, scheme, screens, failures),
		),
	);
	const written = (await Promise.all(lanes)).flat();
	console.log(`共 ${written.length} 张，输出到 ${OUT}`);
	if (CHECK && !screens.some((s) => s.verify))
		console.warn("--check：所选界面里没有带断言的项（thread-bottom）");
	for (const f of failures) console.error(`✗ ${f}`);
	return failures.length === 0;
}

let ok = true;
try {
	await waitForServer(BASE, 30_000);
	await mkdir(OUT, { recursive: true });
	const browser = process.env.WEBKIT
		? await webkit.launch({ executablePath: process.env.WEBKIT })
		: await chromium.launch({
				executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
				// 软件合成器下模拟 DPR（deviceScaleFactor）时 backdrop-filter 会部分失效，改走 SwiftShader 的 GPU 合成
				args: ["--use-gl=angle", "--use-angle=swiftshader"],
			});
	if (ALL) ok = await shootAll(browser);
	else {
		const written = M25 ? await shootEnv(browser) : await shootDefault(browser);
		for (const f of written) console.log(f);
	}
	await browser.close();
} finally {
	server.kill();
}
if (!ok) process.exit(1);
