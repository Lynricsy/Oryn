#!/usr/bin/env bun
// biome-ignore-all lint/performance/useDomNodeTextContent: 这里调用的是 Playwright Locator.innerText()，用于断言可见文本；换成 textContent() 会把隐藏内容算入结果。
import {
	copyFileSync,
	existsSync,
	mkdirSync,
	openSync,
	readdirSync,
	readFileSync,
	readlinkSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { resolve } from "node:path";
/**
 * M13 验收（docs/plan-m13.md「需求」R1–R31，步骤 6.7）：分两部分逐条走界面上能观察到的项，截图存进 `.dev/m13/`，
 * 每条打印 `R<n> [mock|real] PASS|FAIL|SKIP (原因)`，最后按 R 编号汇总（任一部分 FAIL → FAIL；有部分没跑或跳过 → SKIP；
 * 其余 PASS）。全部 PASS 时退出码 0。
 *
 * 用法（web/ 目录）：bun run accept:m13 [-- --part mock|real] [-- --only R1,R13] [-- --help]
 *
 * 一、mock 部分（`--part mock`）：脚本起 `VITE_MOCK=1` 的 vite（127.0.0.1:5181），浏览器内 MockServer 应答一切。
 *   - 每个场景一个全新浏览器上下文（mock 状态随整页加载重建，所以场景内只用 SPA 跳转：`history.pushState` + popstate）。
 *   - 初始化脚本设 `localStorage["oryn.debug"]="1"`（挂出 `window.__orynMock`，用来注入子 agent 帧、task 卡片与来源记录），
 *     并在 `window.fetch` 上挂记录器：mock 在页面里替换了 fetch，Playwright 的网络事件看不到这些请求，断言请求体只能读记录器。
 *
 * 二、真机部分（`--part real`）：自建一套隔离环境（全部在 /var/tmp/oryn-m13-<run>/ 下，结束时删除；M13_KEEP=1 保留）：
 *   - orynd（127.0.0.1:7620，data_dir 全新）；provider `local`（driver process，orbd = $M13_BIN/orbd，agent = fakeomp 的 shell 包装）；
 *   - 协调宿主：`coordinator.omp_path` 是用 `go build -overlay` 给 fakeomp 多加一个 init 编出的 ELF（宿主按 ELF 头核对可执行路径，
 *     shell 包装不行）——`omp models …`（中央模型目录）与 `--no-session`（设置 schema）转给真 omp（$M13_OMP，缺省
 *     build/core/x64/omp），其余（全局助手会话）就是 fakeomp；`verify_omp: false`；目录固定为 $M13_COORD_DIR（缺省
 *     /var/tmp/oryn-m13-coord：root 运行时协调宿主账号按目录派生，固定目录才能复用同一个账号）；
 *   - 第二个 vite（127.0.0.1:5182，非 mock，/api 等代理到这套 orynd）给浏览器用，浏览器与 public_url 用 localhost:5182
 *     （WebAuthn 的 RPID 不能是 IP）；设备令牌走 Authorization 头，另带网页会话 Cookie。
 *   - 全局助手是 fakeomp：提示词里的 `probe read|write <路径>` 在沙箱里读写，`hosttool <工具> <json>` 调宿主工具（save_file、
 *     create_thread），回复逐行给出结果（`probe:read:ok`、`host:save_file:ok:…`）。
 *   - orb 里的 agent 是 fakeomp；R9 需要带 task 调用的转录：标志文件存在时包装脚本给 fakeomp 设 FAKEOMP_SCRIPT（脚本写一条
 *     scout + reviewer 的 task 调用与结果），建完该线程即删标志。
 *   - R23「orb 里执行 oryn library put」：以 orb 的环境（HOME = orb 家目录、ORYN_ORB=1、ORYN_THREAD_ID）直接运行 $M13_BIN/oryn。
 *   前提：以 root 运行（协调宿主降权需要）；有 go 工具链；`make build`（或 M13_BIN 指向含 orynd/orbd/oryn/fakeomp 的目录）。
 *
 * 环境变量：M13_SHOTS（缺省 <仓库>/.dev/m13）、CHROMIUM（缺省 /usr/bin/chromium）、M13_BIN、M13_OMP、M13_COORD_DIR、M13_KEEP。
 *
 * 只能在一部分里观察到的项：
 *   - R9（分享页 agent 头像）mock 的分享身份不带 agents → 只在真机部分；R20–R23、R25 只在真机部分。
 *   - R10–R12（子 agent 状态与 task 卡片）fakeomp 不产生子 agent 帧 → 只在 mock 部分（注入帧与条目）。
 *   - R6 mock 只接受 cycleOrder 里的 role 设头像 → 内置 role 与只在 modelRoles 里的 role 在真机部分验。
 *   - R16 mock 新建线程忽略 role → 「回合头显示该 role」在真机部分验；R18「换设备一致」在真机部分用另一个浏览器上下文验。
 */
import type { Subprocess } from "bun";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Locator,
	type Page,
} from "playwright-core";
import {
	AGENT_DEFAULT_GLYPHS,
	GENERIC_AGENT_GLYPH,
	ROLE_DEFAULT_GLYPHS,
} from "../src/lib/avatar-defaults";
import { type BrowserNetns, startBrowserNetns } from "./browser-netns";

// ---------------------------------------------------------------- 参数

const RS = Array.from({ length: 31 }, (_, i) => `R${i + 1}`);
type Part = "mock" | "real";
const USAGE = `用法：bun scripts/m13-accept.ts [--part mock|real] [--only R1,R13,…] [--help]
  不给 --part 时两部分都跑；环境变量见文件头注释。`;
if (process.argv.includes("--help") || process.argv.includes("-h")) {
	console.log(USAGE);
	process.exit(0);
}
function argValue(name: string): string | undefined {
	const i = process.argv.indexOf(name);
	return i >= 0 ? process.argv[i + 1] : undefined;
}
const partArg = argValue("--part");
if (partArg !== undefined && partArg !== "mock" && partArg !== "real") {
	console.error(`--part 只接受 mock 或 real\n${USAGE}`);
	process.exit(2);
}
const PARTS: Part[] = partArg ? [partArg] : ["mock", "real"];
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
	console.error(`--only 只接受 R1…R31\n${USAGE}`);
	process.exit(2);
}

const ROOT = `${import.meta.dir}/../..`;
const WEB = `${import.meta.dir}/..`;
const SHOTS = process.env.M13_SHOTS ?? `${ROOT}/.dev/m13`;
const CHROMIUM = process.env.CHROMIUM ?? "/usr/bin/chromium";
const BIN = process.env.M13_BIN ?? `${ROOT}/bin`;
const OMP_REAL = process.env.M13_OMP ?? `${ROOT}/build/core/x64/omp`;
const COORD_DIR = process.env.M13_COORD_DIR ?? "/var/tmp/oryn-m13-coord";
const KEEP = process.env.M13_KEEP === "1";
const RUN = Math.random().toString(36).slice(2, 6);
mkdirSync(SHOTS, { recursive: true });

const MOCK_PORT = 5181;
const MOCK_BASE = `http://127.0.0.1:${MOCK_PORT}`;
const REAL_PORT = 7620;
const REAL_API = `http://127.0.0.1:${REAL_PORT}`;
const REAL_UI_PORT = 5182;
/** WebAuthn 的 RPID 必须是域名：public_url 与浏览器都用 localhost（vite 只听 127.0.0.1，Chromium 的 localhost 落到回环）。 */
const REAL_UI = `http://localhost:${REAL_UI_PORT}`;
const TMP = `/var/tmp/oryn-m13-${RUN}`;

// mock 固定数据（web/src/mock/fixtures.ts / library.ts）
const MAIN_THREAD = "T-0193a1b2-0000-7000-8000-00000000a001";
const ROLE_THREAD = "T-0193a1b2-0000-7000-8000-00000000c001";
const ASSISTANT_THREAD = "T-0193a1b2-0000-7000-8000-00000000b010";
const SPACE_IDLE_THREAD = "T-0193a1b2-0000-7000-8000-00000000b004";
const MOCK_CYCLE = ["Hyacinthia", "Nahida", "Mare", "Cyrene", "Arianna"];
const BUILTIN_ROLES = [
	"default",
	"smol",
	"slow",
	"vision",
	"plan",
	"commit",
	"tiny",
	"memory",
	"task",
	"advisor",
];
const BUILTIN_AGENTS = [
	"task",
	"sonic",
	"scout",
	"reviewer",
	"security-reviewer",
	"advisor",
];
const AGENT_PAGES = [
	["agent-skills", "技能"],
	["agent-subagents", "子 agent"],
	["agent-roles", "模型角色"],
	["agent-prompts", "提示词与上下文"],
	["agent-commands", "命令与规则"],
	["agent-extensions", "扩展与工具"],
] as const;
const SCOPE_NOTE = "不作用于全局助手";
/** 各子页顶部的作用范围说明：模型角色只改 config.yml，而 config.yml 也下发给全局助手，这一页标「也下发给全局助手」。 */
const scopeNote = (page: string) =>
	page === "agent-roles" ? "也下发给全局助手" : SCOPE_NOTE;
const LEVEL_ZH: Record<string, string> = {
	auto: "自动",
	off: "关闭",
	minimal: "最低",
	low: "低",
	medium: "中",
	high: "高",
	xhigh: "超高",
	max: "极限",
};

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();
const clip = (s: string, n = 240) =>
	s.length > n ? `${s.slice(0, n)}…（共 ${s.length} 字）` : s;
const isRecord = (v: unknown): v is Record<string, unknown> =>
	typeof v === "object" && v !== null && !Array.isArray(v);
const sameSet = (a: Iterable<string>, b: Iterable<string>) => {
	const x = [...new Set(a)].sort();
	const y = [...new Set(b)].sort();
	return x.length === y.length && x.every((v, i) => v === y[i]);
};

function sh(
	cmd: string,
	env?: Record<string, string | undefined>,
): { code: number; out: string; stdout: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], { cwd: ROOT, env });
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
		stdout: r.stdout.toString().trim(),
	};
}

async function waitFor<T>(
	what: string,
	fn: () =>
		| Promise<T | undefined | null | false>
		| T
		| undefined
		| null
		| false,
	timeoutMs: number,
	intervalMs = 250,
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

type Verdict = "PASS" | "FAIL" | "SKIP";
interface Outcome {
	r: string;
	part: Part;
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
const contexts: BrowserContext[] = [];
const pageErrors: string[] = [];
/** 控制台报错、5xx 应答与失败的请求：本身不判失败（有意的 401 / 404 也会报），只在条目失败时附上，供排查白屏。 */
const pageDiag: string[] = [];

let netns: BrowserNetns | undefined;

async function getBrowser(): Promise<Browser> {
	// 浏览器放进只有回环的网络命名空间：本机 Docker orb 的 veth 变化会让 Chromium 中止在途请求（见 browser-netns.ts）
	netns ??= await startBrowserNetns({
		name: `oryn-m13-${RUN}`,
		ports: [MOCK_PORT, REAL_UI_PORT, REAL_PORT],
		chromium: CHROMIUM,
	});
	browser ??= await chromium.launch({
		executablePath: netns.executablePath,
		headless: true,
		args: ["--no-sandbox", "--use-gl=angle", "--use-angle=swiftshader"],
	});
	return browser;
}

/**
 * 页面初始化：调试开关，以及 fetch 记录器——mock 在模块加载时替换 `globalThis.fetch`，这里用访问器把之后赋上来的实现
 * 包一层记录（初始值保持原生 fetch，mock 自己捕获的 realFetch 不经记录器，避免递归）。
 */
const INIT_SCRIPT = `(() => {
	try { localStorage.setItem("oryn.debug", "1"); localStorage.setItem("oryn.panel.width", "760"); } catch {}
	const log = [];
	Object.defineProperty(window, "__m13req", { value: log });
	const wrap = (fn) => async function (input, init) {
		try {
			const req = input instanceof Request ? input : new Request(input, init);
			const ent = { method: req.method, url: req.url, body: null, at: Date.now() };
			log.push(ent);
			if (req.method !== "GET" && req.method !== "HEAD") {
				const ct = req.headers.get("content-type") || "";
				if (!ct.startsWith("application/octet-stream") || !(init && init.body instanceof Blob)) {
					req.clone().text().then((t) => { ent.body = t.slice(0, 4000); }, () => {});
				} else ent.body = "<blob>";
			}
		} catch {}
		return fn(input, init);
	};
	let current = window.fetch;
	Object.defineProperty(window, "fetch", {
		configurable: true,
		get: () => current,
		set: (v) => { current = wrap(v); },
	});
})();`;

interface PageOpts {
	base: string;
	width?: number;
	height?: number;
	reduced?: boolean;
	auth?: RealAuth;
	scheme?: "light" | "dark";
}

interface RealAuth {
	token: string;
	session: string;
}

async function openPage(o: PageOpts): Promise<Page> {
	const b = await getBrowser();
	const ctx = await b.newContext({
		locale: "zh-CN",
		viewport: { width: o.width ?? 1280, height: o.height ?? 900 },
		colorScheme: o.scheme ?? "light",
		reducedMotion: o.reduced ? "reduce" : "no-preference",
		acceptDownloads: true,
		...(o.auth
			? { extraHTTPHeaders: { Authorization: `Bearer ${o.auth.token}` } }
			: {}),
	});
	if (o.auth)
		await ctx.addCookies([
			{
				name: "__Host-oryn_session",
				value: o.auth.session,
				domain: new URL(o.base).hostname,
				path: "/",
				secure: true,
				httpOnly: true,
				sameSite: "Lax",
			},
		]);
	await ctx.addInitScript(INIT_SCRIPT);
	contexts.push(ctx);
	const p = await ctx.newPage();
	p.on("pageerror", (e) =>
		pageErrors.push(`pageerror：${oneLine(e.message).slice(0, 300)}`),
	);
	p.on("console", (m) => {
		if (m.type() === "error")
			pageDiag.push(`console：${oneLine(m.text()).slice(0, 300)}`);
	});
	p.on("response", (r) => {
		if (r.status() >= 500) pageDiag.push(`HTTP ${r.status()} ${r.url()}`);
	});
	p.on("requestfailed", (r) =>
		pageDiag.push(`请求失败 ${r.url()}：${r.failure()?.errorText ?? ""}`),
	);
	return p;
}

async function closeAll() {
	for (const c of contexts.splice(0)) await c.close().catch(() => {});
}

async function goto(p: Page, base: string, path: string) {
	await p.goto(`${base}${path}`, { waitUntil: "domcontentloaded" });
}

/** SPA 内跳转（不重载页面，mock 状态保留）。 */
async function spaGo(p: Page, path: string) {
	await p.evaluate((to) => {
		history.pushState({}, "", to);
		dispatchEvent(new PopStateEvent("popstate", { state: {} }));
	}, path);
	await sleep(400);
}

async function shot(p: Page, name: string) {
	const vp = p.viewportSize();
	const f = `${SHOTS}/${name}-${vp?.width ?? 0}.png`;
	await p.screenshot({ path: f });
	note(`截图 ${f.slice(SHOTS.length + 1)}`);
}

interface ReqRec {
	method: string;
	url: string;
	body: string | null;
	at: number;
}
const reqs = (p: Page) =>
	p.evaluate(() =>
		(window as unknown as { __m13req: ReqRec[] }).__m13req.slice(),
	);
const apiPath = (u: string) => new URL(u).pathname + new URL(u).search;

/** 页面里调 API（mock 下走 MockServer，真机下经 vite 代理）；字符串体按原文发送。 */
async function pfetch(
	p: Page,
	method: string,
	path: string,
	body?: unknown,
): Promise<{ status: number; text: string; json: unknown }> {
	const r = await p.evaluate(
		async ([m, u, b]) => {
			const headers: Record<string, string> = { "X-Oryn-CSRF": "1" };
			let payload: string | undefined;
			if (b !== undefined) {
				if (typeof b === "string") {
					headers["Content-Type"] = "application/octet-stream";
					payload = b;
				} else {
					headers["Content-Type"] = "application/json";
					payload = JSON.stringify(b);
				}
			}
			const res = await fetch(u as string, {
				method: m as string,
				headers,
				body: payload,
			});
			return { status: res.status, text: await res.text() };
		},
		[method, path, body] as const,
	);
	let json: unknown = null;
	try {
		json = JSON.parse(r.text);
	} catch {
		// 非 JSON
	}
	return { ...r, json };
}

/** `window.__orynMock` 的私有方法（运行时就是普通方法）。 */
async function mockCall(p: Page, method: string, ...args: unknown[]) {
	await p.evaluate(
		([m, a]) => {
			const mock = (window as unknown as Record<string, unknown>)
				.__orynMock as Record<string, (...x: unknown[]) => unknown>;
			const fn = mock[m as string];
			if (typeof fn !== "function") throw new Error(`__orynMock.${m} 不存在`);
			fn.apply(mock, a as unknown[]);
		},
		[method, args] as const,
	);
}

/** 回合头签名（`[data-turn-identity]`）。 */
interface Head {
	identity: string;
	role: string;
	source: string;
	name: string;
	avatar: string;
	glyph: string;
	svg: boolean;
}
async function heads(p: Page): Promise<Head[]> {
	return p
		.locator("[data-testid=transcript] [data-turn-identity]")
		.evaluateAll((els) =>
			els.map((el) => {
				const av = el.querySelector("[data-avatar]");
				return {
					identity: el.getAttribute("data-turn-identity") ?? "",
					role: el.getAttribute("data-role") ?? "",
					source: el.getAttribute("data-turn-source") ?? "",
					name:
						el.querySelector("[data-testid=turn-name]")?.textContent?.trim() ??
						"",
					avatar: av?.getAttribute("data-avatar") ?? "",
					glyph: av?.tagName === "IMG" ? "" : (av?.textContent?.trim() ?? ""),
					svg: !!av?.querySelector("svg"),
				};
			}),
		);
}
const headSig = (h: Head) =>
	`${h.identity}${h.role ? `:${h.role}` : ""}${h.source ? `(${h.source})` : ""}${h.name ? `「${h.name}」` : ""}${h.avatar ? `[${h.avatar}${h.glyph ? ` ${h.glyph}` : ""}]` : ""}`;

/** 在头像编辑器里用「自定义」输入一个字形并保存。 */
async function setGlyph(scope: Locator, glyph: string) {
	const input = scope.getByLabel("自定义（单个 emoji 或 Nerd 字形）");
	await input.fill(glyph);
	await scope.getByRole("button", { name: "使用", exact: true }).click();
}

const capsule = (p: Page) => p.getByRole("button", { name: /^对象：/ });
const popover = (p: Page) =>
	p.locator(
		'[data-testid="role-capsule-popover"], [data-testid="role-capsule-drawer"]',
	);

async function openCapsule(p: Page) {
	if ((await popover(p).count()) === 0) await capsule(p).click();
	await popover(p).first().waitFor();
}

/** 首屏胶囊里选一个对象行（全局助手 / role 名；只认对象行，不点同名的模型行）。 */
async function pickTarget(p: Page, name: string) {
	await openCapsule(p);
	await popover(p)
		.getByRole("button", { name: new RegExp(`^${name}`) })
		.and(p.locator("[data-entry]"))
		.first()
		.click();
	await sleep(300);
}

/** 首屏胶囊选择器里按厂商列出的模型（`[data-model]`；常用区用 `data-model-shortcut`，不重复计）。 */
async function capsuleModels(p: Page): Promise<string[]> {
	await openCapsule(p);
	await popover(p).locator("[data-model]").first().waitFor();
	return popover(p)
		.locator("[data-model]")
		.evaluateAll((els) => els.map((e) => e.getAttribute("data-model") ?? ""));
}

/** 模型角色页某个 role 的模型字段：点开它的模型选择器，读其中按厂商列出的模型（`[data-model]`）。 */
async function settingsModels(p: Page, roleLabel: string): Promise<string[]> {
	await p
		.getByRole("button", { name: `设置 ${roleLabel}`, exact: true })
		.click();
	await p
		.getByRole("button", { name: `${roleLabel} 的模型`, exact: true })
		.click();
	const picker = p.locator("[data-model-picker]").last();
	await picker.waitFor();
	const read = () =>
		picker
			.locator("[data-model]")
			.evaluateAll((els) => els.map((e) => e.getAttribute("data-model") ?? ""));
	// 目录是异步读的：等到有模型行再取
	const models = await waitFor(
		"模型角色编辑器的模型选择器出现模型",
		async () => {
			const v = await read();
			return v.length > 0 ? v : null;
		},
		30_000,
	).catch(read);
	await p.keyboard.press("Escape");
	return models;
}

/** 线程页的模型选择器：输入坞模型按钮上的弹层（桌面）/ 底部抽屉（窄屏），两者都是名为「模型」的 dialog。 */
const threadPicker = (p: Page) => p.getByRole("dialog", { name: "模型" });

/** 线程页 `/model` 打开的模型选择器里的模型。 */
async function dialogModels(p: Page): Promise<string[]> {
	const box = p.locator('textarea[aria-label="消息"]');
	await box.waitFor({ timeout: 30_000 });
	await box.fill("/model");
	await sleep(300);
	await p.keyboard.press("Escape");
	await box.press("Enter");
	const dlg = threadPicker(p);
	await dlg.waitFor();
	await dlg.locator("[data-model]").first().waitFor();
	return dlg
		.locator("[data-model]")
		.evaluateAll((els) => els.map((e) => e.getAttribute("data-model") ?? ""));
}

const configText = async (p: Page) =>
	(await pfetch(p, "GET", "/api/v1/agent-config/files/config.yml")).text;
const fileGet = (p: Page, path: string) =>
	pfetch(p, "GET", `/api/v1/agent-config/files/${encodeURIComponent(path)}`);

/** 设置页对话框（当前打开的最上层）。 */
const dialog = (p: Page) => p.getByRole("dialog").last();

// ================================================================ mock 部分

let mockVite: Subprocess | undefined;

/**
 * 起一个 vite 开发服务器的模块代码。每个服务器用自己的 cacheDir：同一 web/ 下的多个 vite（mock、真机、别处常驻的）
 * 共用 node_modules/.vite 时，任一个重新预构建依赖都会让其他的懒加载路由拿到 504 Outdated Optimize Dep，页面白屏。
 * entries 覆盖全部源码，启动时一次发现全部依赖，跑到中途不会再因新依赖重新预构建、整页重载。
 */
function viteModule(name: string, port: number, proxy?: string): string {
	const dir = `${WEB}/node_modules/.m13`;
	mkdirSync(dir, { recursive: true });
	const file = `${dir}/vite-${name}.mjs`;
	writeFileSync(
		file,
		`import { createServer } from "vite";
${proxy ?? "const proxy = undefined;"}
const s = await createServer({
	configFile: "vite.config.ts",
	cacheDir: "node_modules/.vite-m13-${name}",
	optimizeDeps: { entries: ["index.html", "src/**/*.{ts,tsx}", "!src/**/*.test.{ts,tsx}"] },
	server: { port: ${port}, strictPort: true, host: "127.0.0.1", proxy },
});
await s.listen();
console.log("ready");
`,
	);
	return file;
}

async function startMock() {
	mkdirSync(SHOTS, { recursive: true });
	const log = openSync(`${SHOTS}/mock-vite.log`, "w");
	mockVite = Bun.spawn(["bun", viteModule("mock", MOCK_PORT)], {
		cwd: WEB,
		env: { ...process.env, VITE_MOCK: "1" },
		stdout: log,
		stderr: log,
	});
	await waitFor(
		"mock vite 就绪",
		async () => (await fetch(MOCK_BASE)).ok,
		60_000,
		300,
	);
	// 冷启动的 vite 首次按需编译要很久：先把首屏与设置页各加载一遍，免得第一个场景的 30 秒等待被编译吃掉
	const warm = await openPage({ base: MOCK_BASE });
	for (const [path, ready] of [
		["/", 'textarea[aria-label="提示词"]'],
		["/settings/identity", '[data-testid="identity-new-thread"]'],
	] as const) {
		await goto(warm, MOCK_BASE, path);
		await warm.locator(ready).waitFor({ timeout: 180_000 });
	}
	await closeAll();
}

const mockPage = (o: Omit<PageOpts, "base"> = {}) =>
	openPage({ ...o, base: MOCK_BASE });

async function mockHome(o: Omit<PageOpts, "base"> = {}) {
	const p = await mockPage(o);
	await goto(p, MOCK_BASE, "/");
	await p.getByRole("textbox", { name: "提示词" }).waitFor({ timeout: 30_000 });
	await capsule(p).waitFor();
	return p;
}

const catalogOf = async (p: Page) =>
	(await pfetch(p, "GET", "/api/v1/models/catalog")).json as {
		models: {
			provider: string;
			id: string;
			kind: string;
			thinking?: string[];
		}[];
	};
const chatSelectors = (c: Awaited<ReturnType<typeof catalogOf>>) =>
	c.models.filter((m) => m.kind === "chat").map((m) => `${m.provider}/${m.id}`);

async function mockR1() {
	const p = await mockHome();
	const catalog = await catalogOf(p);
	const chat = chatSelectors(catalog);
	note(`目录里 chat 模型 ${chat.length} 个`);
	const early = (await reqs(p)).map((r) => `${r.method} ${apiPath(r.url)}`);
	expect(
		early.some((r) => r.startsWith("GET /api/v1/models/catalog")),
		"首屏读的是中央模型目录 GET /api/v1/models/catalog",
	);
	expect(
		!early.some((r) =>
			/\/threads\/[^/]+\/(wake|rpc)|POST \/api\/v1\/threads/.test(r),
		),
		"读模型列表前没有唤醒或打开任何线程",
	);
	const home = await capsuleModels(p);
	await shot(p, "mock-R1-home-models");
	expect(
		sameSet(home, chat),
		`首屏胶囊选择器的模型列表 = 目录 chat 模型（${home.length} 个）`,
	);
	expect(!home.includes("web/exa"), "非对话模型（web/exa）不在列表里");
	// 思考档位：选 Opus 5.5 后，再打开时检视栏（高亮落在选中项上）的思考级别等于目录的 thinking
	const opus = catalog.models.find((m) => m.id === "claude-opus-5-5");
	await popover(p).locator('[data-model="anthropic/claude-opus-5-5"]').click();
	await openCapsule(p);
	const radios = await popover(p)
		.getByRole("radiogroup", { name: "思考级别" })
		.getByRole("radio")
		.allInnerTexts();
	const want = ["自动", ...(opus?.thinking ?? []).map((l) => LEVEL_ZH[l] ?? l)];
	expect(
		radios.map((s) => s.trim()).join(",") === want.join(","),
		`选中 Opus 5.5 后思考级别 = ${radios.join("/")}（目录 ${opus?.thinking?.join("/")}）`,
	);
	await shot(p, "mock-R1-home-thinking");
	await p.keyboard.press("Escape");
	await spaGo(p, `/t/${MAIN_THREAD}`);
	const dlg = await dialogModels(p);
	await shot(p, "mock-R1-thread-dialog");
	expect(
		sameSet(dlg, chat),
		`线程内模型对话框列表 = 目录 chat 模型（${dlg.length} 个）`,
	);
	await p.keyboard.press("Escape");
	await spaGo(p, "/settings/agent-roles");
	const set = await settingsModels(p, "快速");
	await shot(p, "mock-R1-settings-roles");
	expect(
		sameSet(set, chat),
		`设置页模型角色编辑器的候选 = 目录 chat 模型（${set.length} 个）`,
	);
	expect(sameSet(home, dlg) && sameSet(dlg, set), "三处列表是同一份");
	note("「不唤醒任何 orb」与 models.yml 自定义 provider 在真机部分核对");
}

async function mockR2() {
	const p = await mockHome();
	await capsuleModels(p);
	const refresh = popover(p).getByRole("button", { name: "刷新" });
	await refresh.click();
	await waitFor("刷新按钮恢复可用", () => refresh.isEnabled(), 10_000);
	const rs = (await reqs(p)).map((r) => apiPath(r.url));
	expect(
		rs.some(
			(u) =>
				u.startsWith("/api/v1/models/catalog") && u.includes("refresh=true"),
		),
		"首屏模型列表的「刷新」发 GET /api/v1/models/catalog?refresh=true",
	);
	await shot(p, "mock-R2-refresh");
	await p.keyboard.press("Escape");
	await spaGo(p, `/t/${MAIN_THREAD}`);
	await dialogModels(p);
	const n0 = (await reqs(p)).length;
	// 「刷新」在选择器自己的页脚里（弹层内），不在输入坞或页面别处
	await threadPicker(p).getByRole("button", { name: "刷新" }).click();
	await sleep(500);
	expect(
		(await reqs(p))
			.slice(n0)
			.some((r) => apiPath(r.url).includes("models/catalog?refresh=true")),
		"线程内模型对话框也有「刷新」，强制重拉目录",
	);
	note(
		"「改 models.yml 后列表反映新配置」在真机部分核对；24 小时过期是服务端缓存键 + TTL（go test 覆盖），界面无法等 24 小时",
	);
}

async function mockR3() {
	const p = await mockPage();
	await goto(p, MOCK_BASE, "/settings/identity");
	const agentCard = p.getByTestId("identity-agent");
	await agentCard.waitFor({ timeout: 30_000 });
	expect(
		(
			await p
				.locator("h3")
				.filter({ hasText: /Agent 身份$/ })
				.first()
				.textContent()
		)?.trim() === "默认 Agent 身份",
		"「Agent 身份」卡片标题为「默认 Agent 身份」",
	);
	const main = (await p.locator("main").first().textContent()) ?? "";
	expect(
		!/跟随[^，。]{0,8}(Agent|agent)/.test(main) &&
			!/跟随/.test(
				(await p.getByTestId("identity-assistant").textContent()) ?? "",
			),
		"身份页没有「跟随 Agent 身份」选项",
	);
	// 给默认 Agent 身份设头像与名字，全局助手留空：仍显示「全局助手」与 sparkle
	await setGlyph(agentCard, "🦊");
	await sleep(500);
	expect(
		(
			await agentCard.locator('[data-avatar="icon"]').first().innerText()
		).includes("🦊"),
		"默认 Agent 身份头像设为 🦊",
	);
	const preview = p.getByTestId("assistant-tab-preview");
	const pv = await preview.innerText();
	const def = preview.locator(
		'[data-avatar-of="assistant"][data-avatar="default"] svg',
	);
	expect(
		pv.includes("全局助手"),
		`全局助手预览名为「全局助手」（${oneLine(pv)}）`,
	);
	expect(
		(await def.count()) > 0,
		"全局助手预览是默认 sparkle 标记，不跟随默认 Agent 身份的 🦊",
	);
	await shot(p, "mock-R3-identity");
}

async function mockR4() {
	const p = await mockPage({ height: 1600 });
	await goto(p, MOCK_BASE, `/assistant/${ASSISTANT_THREAD}`);
	await p
		.locator("[data-testid=transcript] [data-turn-identity]")
		.first()
		.waitFor({ timeout: 30_000 });
	const before = await heads(p);
	note(`回合头 ${before.map(headSig).join(" ")}`);
	expect(
		before.length > 0 &&
			before.every(
				(h) => h.identity === "assistant" && h.name === "全局助手" && h.svg,
			),
		`助手对话每个回合头都是全局助手（${before.length} 个）`,
	);
	// 切到有头像的 role（Cyrene 🌸）后再来一个回合：回合头仍是全局助手
	await mockCall(p, "appendRaw", ASSISTANT_THREAD, "custom", {
		customType: "oryn-model-source",
		data: {
			v: 1,
			source: "role",
			role: "Cyrene",
			model: "anthropic/claude-opus-5-5",
		},
	});
	await mockCall(p, "appendEntry", ASSISTANT_THREAD, {
		role: "user",
		content: [{ type: "text", text: "m13 R4：切到 Cyrene 之后" }],
		timestamp: Date.now(),
	});
	await mockCall(p, "appendEntry", ASSISTANT_THREAD, {
		role: "assistant",
		content: [{ type: "text", text: "仍由全局助手回答" }],
		provider: "anthropic",
		model: "claude-opus-5-5",
		api: "mock",
		stopReason: "stop",
		timestamp: Date.now(),
	});
	await waitFor(
		"新回合头出现",
		async () => (await heads(p)).length > before.length,
		10_000,
	);
	const after = await heads(p);
	const last = after.at(-1);
	expect(
		!!last &&
			last.identity === "assistant" &&
			last.role === "" &&
			last.name === "全局助手",
		`切到 Cyrene 后的回合头仍是全局助手（${last ? headSig(last) : "无"}）`,
	);
	await shot(p, "mock-R4-assistant-heads");
}

async function mockR5() {
	const p = await mockPage({ height: 2400 });
	await goto(p, MOCK_BASE, `/t/${ROLE_THREAD}`);
	await p
		.locator("[data-testid=transcript] [data-turn-identity]")
		.first()
		.waitFor({ timeout: 30_000 });
	await sleep(800);
	const hs = await heads(p);
	note(`回合头 ${hs.map(headSig).join(" ")}`);
	const want = [
		"role:Cyrene",
		"role:Mare",
		"agent",
		"agent",
		"agent",
		"role:Hyacinthia",
		"agent",
	];
	expect(
		hs.map((h) => `${h.identity}${h.role ? `:${h.role}` : ""}`).join(",") ===
			want.join(","),
		`有自定义头像的 role（Cyrene/Mare/Hyacinthia）用自己的头像，自定义 role 没头像（Nahida）回落到默认 Agent 身份：${want.join(",")}`,
	);
	expect(
		hs[0]?.avatar === "icon" && hs[0]?.glyph === "🌸",
		"Cyrene 回合头是它的自定义头像 🌸",
	);
	// 内置 role（smol，没设头像）：回合头用 smol 的默认字形
	await mockCall(p, "appendRaw", ROLE_THREAD, "custom", {
		customType: "oryn-model-source",
		data: {
			v: 1,
			source: "role",
			role: "smol",
			model: "anthropic/claude-sonnet-5",
		},
	});
	await mockCall(p, "appendEntry", ROLE_THREAD, {
		role: "user",
		content: [{ type: "text", text: "m13 R5：切到 smol" }],
		timestamp: Date.now(),
	});
	await mockCall(p, "appendEntry", ROLE_THREAD, {
		role: "assistant",
		content: [{ type: "text", text: "smol 回答" }],
		provider: "anthropic",
		model: "claude-sonnet-5",
		api: "mock",
		stopReason: "stop",
		timestamp: Date.now(),
	});
	await waitFor(
		"smol 回合头出现",
		async () => (await heads(p)).length > hs.length,
		10_000,
	);
	const last = (await heads(p)).at(-1);
	expect(
		!!last &&
			last.identity === "role" &&
			last.role === "smol" &&
			last.glyph === ROLE_DEFAULT_GLYPHS.smol,
		`内置 role smol（没设头像）回合头是它的默认 Nerd 字形（${last ? headSig(last) : "无"}）`,
	);
	await p.locator("[data-turn-identity]").last().scrollIntoViewIfNeeded();
	await shot(p, "mock-R5-role-heads");
}

async function mockR6() {
	const p = await mockPage();
	await goto(p, MOCK_BASE, "/settings/devices");
	await p
		.getByRole("heading", { name: "设置" })
		.first()
		.waitFor({ timeout: 30_000 });
	await pfetch(
		p,
		"PUT",
		"/api/v1/agent-config/files/config.yml",
		`cycleOrder: [${MOCK_CYCLE.join(", ")}]\nmodelRoles:\n  m13extra: anthropic/claude-sonnet-5\n`,
	);
	await spaGo(p, "/settings/agent-roles");
	await p.locator('[data-testid="roles-cycle"]').waitFor();
	const known = await p
		.locator("li[data-role]")
		.evaluateAll((els) => els.map((e) => e.getAttribute("data-role") ?? ""));
	note(`模型角色页列出 ${known.join(",")}`);
	expect(
		MOCK_CYCLE.every((r) => known.includes(r)),
		"cycleOrder 里的 role 都列出",
	);
	expect(
		known.includes("m13extra"),
		"只在 modelRoles 里的 role（m13extra）也列出",
	);
	expect(
		BUILTIN_ROLES.every((r) => known.includes(r)),
		"内置 role 都列出（default…advisor）",
	);
	// cycleOrder 里没头像的 Nahida 设头像
	const row = p.locator('li[data-role="Nahida"]');
	await p.getByRole("button", { name: "设置 Nahida", exact: true }).click();
	await setGlyph(row, "🦉");
	await waitFor(
		"Nahida 头像变成 🦉",
		async () =>
			(
				await row
					.locator('[data-avatar="icon"]')
					.first()
					.innerText()
					.catch(() => "")
			).includes("🦉"),
		10_000,
	);
	expect(true, "cycleOrder 里的 role（Nahida）在模型角色页设头像成功");
	// 内置 role 的默认字形
	const smolGlyph = await p
		.locator('li[data-role="smol"] [data-avatar-of="role"]')
		.first()
		.innerText();
	expect(
		smolGlyph.trim() === ROLE_DEFAULT_GLYPHS.smol,
		"没设头像的内置 role（smol）显示默认字形",
	);
	await shot(p, "mock-R6-roles");
	note(
		"内置 role 与只在 modelRoles 里的 role 能设头像：mock 只接受 cycleOrder 里的 role，在真机部分核对",
	);
}

async function mockR7() {
	const p = await mockPage({ height: 1400 });
	await goto(p, MOCK_BASE, "/settings/agent-subagents");
	await p.locator("li[data-agent]").first().waitFor({ timeout: 30_000 });
	for (const a of BUILTIN_AGENTS) {
		const g = (
			await p
				.locator(`li[data-agent="${a}"] [data-avatar-of="agent"]`)
				.first()
				.innerText()
				.catch(() => "")
		).trim();
		expect(
			g === AGENT_DEFAULT_GLYPHS[a],
			`内置 agent ${a} 没设头像时显示默认字形`,
		);
	}
	await createAgent(p, "m13-helper", "M13 验收用的自定义 agent");
	const custom = p.locator('li[data-agent="m13-helper"]');
	await custom.waitFor();
	expect(
		(
			await custom.locator('[data-avatar-of="agent"]').first().innerText()
		).trim() === GENERIC_AGENT_GLYPH,
		"自定义 agent（agents/m13-helper.md）没设头像时显示通用机器人字形",
	);
	for (const [name, glyph] of [
		["scout", "🔭"],
		["m13-helper", "🧪"],
	] as const) {
		const row = p.locator(`li[data-agent="${name}"]`);
		await row
			.getByRole("button", { name: `设置 ${name} 的模型与头像` })
			.click();
		await setGlyph(row, glyph);
		await waitFor(
			`${name} 头像变成 ${glyph}`,
			async () =>
				(
					await row
						.locator('[data-avatar="icon"]')
						.first()
						.innerText()
						.catch(() => "")
				).includes(glyph),
			10_000,
		);
		expect(true, `${name} 在子 agent 页设头像 ${glyph} 成功`);
	}
	await shot(p, "mock-R7-subagents");
	const list = (await pfetch(p, "GET", "/api/v1/settings/agent-avatars"))
		.json as {
		agents: { name: string; source: string; avatarIcon?: string }[];
	};
	expect(
		list.agents.some(
			(a) =>
				a.name === "scout" && a.source === "bundled" && a.avatarIcon === "🔭",
		) &&
			list.agents.some(
				(a) =>
					a.name === "m13-helper" &&
					a.source === "user" &&
					a.avatarIcon === "🧪",
			),
		"头像存进 agent 头像接口（scout=bundled，m13-helper=user）",
	);
}

async function createAgent(p: Page, name: string, desc: string) {
	await p
		.locator("section")
		.filter({ has: p.getByRole("heading", { name: "自定义 agent" }) })
		.getByRole("button", { name: "新建" })
		.click();
	const d = dialog(p);
	await d.getByLabel("名称").fill(name);
	await d.getByLabel("描述").fill(desc);
	await d.getByRole("button", { name: "创建" }).click();
	await d.waitFor({ state: "detached" });
}

async function mockR8() {
	const p = await mockPage({ height: 1600 });
	await goto(p, MOCK_BASE, "/settings/identity");
	await p.getByTestId("identity-agent").waitFor({ timeout: 30_000 });
	const ids = await p
		.locator('[data-testid^="identity-"]')
		.evaluateAll((els) =>
			els
				.map((e) => e.getAttribute("data-testid") ?? "")
				.filter((t) => !t.includes("-preview")),
		);
	note(`身份页卡片 ${ids.join(",")}`);
	const titles = await p.locator("main h3, section h3").allInnerTexts();
	note(`卡片标题 ${titles.map((s) => s.trim()).join(" / ")}`);
	for (const t of ["默认 Agent 身份", "全局助手", "新对话默认对象", "我的头像"])
		expect(
			titles.some((s) => s.trim() === t),
			`身份页有「${t}」`,
		);
	expect(
		(await p
			.locator('[data-testid="identity-roles"] a[href="/settings/agent-roles"]')
			.count()) === 1,
		"role 头像只剩一个指向「Agent › 模型角色」的链接",
	);
	expect(
		(await p.locator('[data-avatar-of="role"]').count()) === 0 &&
			(await p
				.locator('[data-avatar-of="agent"]')
				.filter({ hasNot: p.locator("[data-testid=identity-agent]") })
				.count()) <=
				(await p
					.getByTestId("identity-agent")
					.locator('[data-avatar-of="agent"]')
					.count()),
		"身份页没有 role / 子 agent 头像编辑",
	);
	await shot(p, "mock-R8-identity");
	await spaGo(p, "/settings/agent-roles");
	await p
		.getByRole("button", { name: /^设置 / })
		.first()
		.click();
	expect(
		(await p.getByLabel("自定义（单个 emoji 或 Nerd 字形）").count()) > 0,
		"「模型角色」页的 role 设置里有头像编辑",
	);
	await spaGo(p, "/settings/agent-subagents");
	await p.getByRole("button", { name: "设置 scout 的模型与头像" }).click();
	expect(
		(await p.getByLabel("自定义（单个 emoji 或 Nerd 字形）").count()) > 0,
		"「子 agent」页的 agent 设置里有头像编辑",
	);
}

/** 往线程注入子 agent：5 个在线状态（帧）+ 1 个只剩转录的存档（条目）。 */
async function injectSubagents(p: Page, tid: string) {
	const base = { parentToolCallId: "call_m13" };
	const subs = [
		{
			id: "0-Runner",
			agent: "scout",
			description: "在跑的侦察",
			status: "started",
			progress: {
				status: "running",
				modelRole: "smol",
				resolvedModel: "anthropic/claude-sonnet-5",
			},
		},
		{
			id: "1-Queued",
			agent: "reviewer",
			description: "排队中的评审",
			status: "started",
			progress: { status: "pending" },
		},
		{
			id: "2-Done",
			agent: "task",
			description: "已完成的子任务",
			status: "completed",
		},
		{
			id: "3-Failed",
			agent: "sonic",
			description: "失败的子任务",
			status: "failed",
		},
		{
			id: "4-Aborted",
			agent: "security-reviewer",
			description: "被中止的评审",
			status: "aborted",
		},
	];
	for (const [i, s] of subs.entries()) {
		await mockCall(p, "frame", tid, {
			type: "subagent_lifecycle",
			payload: {
				...base,
				id: s.id,
				index: i,
				agent: s.agent,
				description: s.description,
				status: s.status,
			},
		});
		if (s.progress)
			await mockCall(p, "frame", tid, {
				type: "subagent_progress",
				payload: {
					...base,
					id: s.id,
					index: i,
					agent: s.agent,
					progress: { id: s.id, ...s.progress },
				},
			});
	}
	// 存档：较早主会话与当前主会话的产物目录下各一份同 id 的 9-Archived.jsonl（含 session_init）；
	// 较早那份是「不是同 id 的在线那一个」（superseded），当前那份在 agent 不可查询时是「已保存的转录」（offline）
	await p.evaluate((t) => {
		const mock = (window as unknown as Record<string, unknown>).__orynMock as {
			entries: Map<string, { seq: number; file: string; entryId: string }[]>;
			broadcast: (m: unknown, t: string) => void;
		};
		const list = mock.entries.get(t) ?? [];
		const main = list.find((e) => !e.file.includes("/"));
		const dir = (main?.file ?? "session.jsonl").replace(/\.jsonl$/, "");
		const oldDir = `${dir.slice(0, dir.lastIndexOf("/") + 1)}0000-older-session`;
		const seq0 = list.at(-1)?.seq ?? 0;
		const mk = (
			n: number,
			file: string,
			type: string,
			payload: Record<string, unknown>,
		) => ({
			seq: seq0 + n,
			file,
			entryId: `m13arch${n}`,
			type,
			ts: Date.now(),
			payload: { type, id: `m13arch${n}`, ...payload },
		});
		const add = [
			mk(1, `${oldDir}/9-Archived.jsonl`, "session_init", {
				agent: "advisor",
				task: "较早会话的顾问任务",
			}),
			mk(2, `${oldDir}/9-Archived.jsonl`, "title_change", {
				title: "较早会话的顾问",
			}),
			mk(3, `${dir}/9-Archived.jsonl`, "session_init", {
				agent: "advisor",
				task: "本会话的顾问任务",
			}),
			mk(4, `${dir}/9-Archived.jsonl`, "title_change", {
				title: "本会话的顾问",
			}),
		];
		list.push(...(add as never[]));
		mock.entries.set(t, list);
		const leaf = [...list]
			.reverse()
			.find((e) => !e.file.includes("/"))?.entryId;
		mock.broadcast(
			{
				type: "entries",
				threadId: t,
				entries: add,
				leafEntryId: leaf,
				lastSeq: seq0 + 4,
			},
			t,
		);
	}, tid);
}

const subagentCard = (p: Page) =>
	p
		.locator("aside[aria-label='信息面板'] section")
		.filter({ has: p.getByRole("heading", { name: "子 agent", exact: true }) });

async function openSubagents(o: Omit<PageOpts, "base"> = {}) {
	const p = await mockPage({ height: 1400, ...o });
	await goto(p, MOCK_BASE, `/t/${MAIN_THREAD}`);
	await p.locator("[data-testid=transcript]").waitFor({ timeout: 30_000 });
	if ((await p.locator("aside[aria-label='信息面板']").count()) === 0)
		await p.getByRole("button", { name: "信息面板" }).click();
	await sleep(500);
	await injectSubagents(p, MAIN_THREAD);
	await subagentCard(p).waitFor({ timeout: 10_000 });
	await sleep(500);
	return p;
}

const TOOLTIP: Record<string, RegExp> = {
	running: /运行中/,
	pending: /排队/,
	done: /已完成/,
	failed: /失败/,
	aborted: /已中止/,
	archived: /已保存的转录/,
};

async function mockR10() {
	const p = await openSubagents();
	const card = subagentCard(p);
	await card.scrollIntoViewIfNeeded();
	const rows = await card.locator("li").evaluateAll((els) =>
		els.map((li) => {
			const av = li.querySelector('[role="img"][data-state]');
			const face = av?.querySelector("[data-agent-face]");
			return {
				text: (li.textContent ?? "").trim(),
				state: av?.getAttribute("data-state") ?? "",
				title: av?.getAttribute("title") ?? "",
				first:
					!!av &&
					li.querySelector("button")?.firstElementChild?.contains(av) === true,
				grayscale: !!face?.className.toString().includes("grayscale"),
				ring:
					av?.querySelector("svg[data-ring]")?.getAttribute("data-ring") ?? "",
				badge:
					av?.querySelector("[data-badge]")?.getAttribute("data-badge") ?? "",
				dot: !!li.querySelector("[data-status-dot], .status-dot"),
			};
		}),
	);
	for (const r of rows)
		note(
			`行 ${clip(r.text, 40)} → ${r.state} ring=${r.ring} badge=${r.badge} gray=${r.grayscale} title=${r.title}`,
		);
	const by = (s: string) => rows.find((r) => r.state === s);
	for (const s of [
		"running",
		"pending",
		"done",
		"failed",
		"aborted",
		"archived",
	]) {
		const r = by(s);
		expect(
			!!r && (TOOLTIP[s]?.test(r.title) ?? false),
			`${s} 行有头像且 tooltip「${r?.title ?? "—"}」`,
		);
	}
	expect(
		by("running")?.ring === "running",
		"运行中：外圈光弧（svg[data-ring=running]）",
	);
	expect(by("pending")?.ring === "pending", "排队：虚线外圈");
	expect(by("done")?.badge === "done", "完成：右下角小勾");
	expect(
		by("failed")?.badge === "failed" && by("failed")?.grayscale === true,
		"失败：去饱和 + 叉",
	);
	expect(
		by("aborted")?.badge === "aborted" && by("aborted")?.grayscale === true,
		"中止：去饱和 + 叉",
	);
	expect(
		by("archived")?.ring === "archived" &&
			by("archived")?.badge === "archived" &&
			by("archived")?.grayscale === false,
		"只剩转录：原色 + 静止虚线环 + 存档角标",
	);
	expect(
		rows.every((r) => !r.dot),
		"行首不再有状态点",
	);
	const head = (await card.locator("header").textContent()) ?? "";
	expect(
		/1 个运行中/.test(head) &&
			(await card.locator("header svg[data-ring='running']").count()) === 1,
		`标题栏运行汇总用同一套光环（${oneLine(head)}）`,
	);
	const spin = await card
		.locator("svg[data-ring='running']")
		.first()
		.evaluate((el) => getComputedStyle(el).animationName);
	expect(spin !== "none", `运行中的外圈在转（animation-name ${spin}）`);
	await shot(p, "mock-R10-overview");
	// 收起卡片时标题栏露出运行汇总
	await card.locator("header h3 button").click();
	await sleep(400);
	await shot(p, "mock-R10-summary");
	// 减少动态效果：外圈不转
	const r = await openSubagents({ reduced: true });
	const still = await subagentCard(r)
		.locator("svg[data-ring='running']")
		.first()
		.evaluate((el) => {
			const arc = el.querySelector(".agent-ring-arc");
			const cs = getComputedStyle(el);
			return {
				iter: cs.animationIterationCount,
				dur: cs.animationDuration,
				arc: arc ? getComputedStyle(arc).strokeDasharray : "",
			};
		});
	expect(
		still.iter === "1" && still.arc === "none",
		`减少动态效果时外圈不转、改为整圈实线（iteration ${still.iter}，duration ${still.dur}，arc dasharray ${still.arc}）`,
	);
	await subagentCard(r).scrollIntoViewIfNeeded();
	await shot(r, "mock-R10-overview-reduced");
}

async function mockR11() {
	const p = await openSubagents();
	const card = subagentCard(p);
	await card.getByRole("button", { name: /在跑的侦察/ }).click();
	const d = dialog(p);
	await d.waitFor();
	const info = await d.evaluate((el) => {
		const av = el.querySelector('[role="img"][data-state]');
		const row = av?.closest("div");
		return {
			state: av?.getAttribute("data-state") ?? "",
			row: (row?.textContent ?? "").trim(),
			mono: row?.querySelector(".font-mono")?.textContent?.trim() ?? "",
			role:
				row?.querySelector('[title="所用 role"]')?.textContent?.trim() ?? "",
			model:
				row?.querySelector('[title^="所用模型"]')?.getAttribute("title") ?? "",
			desc: row?.nextElementSibling?.textContent?.trim() ?? "",
			status: [...el.querySelectorAll('[role="status"]')].filter((s) =>
				s.textContent?.trim(),
			).length,
		};
	});
	note(`详情第一行「${clip(info.row, 80)}」；第二行「${info.desc}」`);
	expect(info.state === "running", "第一行是带动效的头像（running）");
	expect(
		info.row.includes("scout") && info.mono === "0-Runner",
		"第一行有类型 badge（scout）与等宽短名（0-Runner）",
	);
	expect(
		info.role === "smol" && info.model.includes("claude-sonnet-5"),
		`右侧显示所用 role（${info.role}）与模型（${info.model}）`,
	);
	expect(info.desc.includes("在跑的侦察"), "第二行是 description");
	expect(info.status === 0, "没有灰色说明条（role=status 的 Callout）");
	await shot(p, "mock-R11-detail-running");
	await p.keyboard.press("Escape");
	// 两种存档：较早会话的同 id 存档（superseded）；agent 不可查询时当前会话的存档（offline）
	const archived = async (label: RegExp, tip: RegExp, name: string) => {
		await card.getByRole("button", { name: label }).first().click();
		const d2 = dialog(p);
		await d2.waitFor();
		const arch = await d2.evaluate((el) => {
			const av = el.querySelector('[role="img"][data-state]');
			return {
				state: av?.getAttribute("data-state") ?? "",
				title: av?.getAttribute("title") ?? "",
				status: [...el.querySelectorAll('[role="status"]')].filter((s) =>
					s.textContent?.trim(),
				).length,
				btns: [...el.querySelectorAll("button")]
					.filter((b) =>
						["取消", "终止", "复活"].includes((b.textContent ?? "").trim()),
					)
					.map((b) => `${(b.textContent ?? "").trim()}:${b.disabled}`),
			};
		});
		expect(
			arch.state === "archived" && tip.test(arch.title),
			`${name}：头像 tooltip 说明「${arch.title}」`,
		);
		expect(arch.status === 0, `${name}：没有说明条`);
		expect(
			arch.btns.length === 3 && arch.btns.every((b) => b.endsWith(":true")),
			`${name}：控制按钮仍禁用（${arch.btns.join(" ")}）`,
		);
		await shot(p, `mock-R11-detail-${name}`);
		await p.keyboard.press("Escape");
	};
	await archived(/较早会话的顾问/, /较早会话里已保存的转录/, "superseded");
	// 让 agent 不可查询：orb 进入 frozen
	await p.evaluate((t) => {
		const mock = (window as unknown as Record<string, unknown>).__orynMock as {
			threads: Map<string, { orbId?: string }>;
			orbs: Map<string, Record<string, unknown>>;
			setOrb: (o: Record<string, unknown>) => void;
		};
		const orb = mock.orbs.get(mock.threads.get(t)?.orbId ?? "");
		if (orb) mock.setOrb({ ...orb, state: "frozen" });
	}, MAIN_THREAD);
	await sleep(500);
	await archived(/本会话的顾问/, /已保存的转录/, "offline");
}

async function injectTaskCall(p: Page, tid: string) {
	const callId = "call_m13task";
	await mockCall(p, "appendEntry", tid, {
		role: "user",
		content: [{ type: "text", text: "m13：派两个子 agent" }],
		timestamp: Date.now(),
	});
	await mockCall(p, "appendEntry", tid, {
		role: "assistant",
		content: [
			{
				type: "toolCall",
				id: callId,
				name: "task",
				arguments: {
					agent: "scout",
					tasks: [
						{ name: "ScoutA", agent: "scout", task: "看看代码" },
						{ name: "ReviewB", agent: "reviewer", task: "评审改动" },
					],
				},
			},
		],
		provider: "anthropic",
		model: "claude-opus-5-5",
		api: "mock",
		stopReason: "toolUse",
		timestamp: Date.now(),
	});
	await mockCall(p, "appendEntry", tid, {
		role: "toolResult",
		toolCallId: callId,
		toolName: "task",
		content: [{ type: "text", text: "两个子 agent 都完成了" }],
		details: {
			results: [
				{ id: "ScoutA", agent: "scout", exitCode: 0, output: "ok" },
				{ id: "ReviewB", agent: "reviewer", exitCode: 1, output: "失败" },
			],
		},
		isError: false,
		timestamp: Date.now(),
	});
}

async function mockR12() {
	const p = await mockPage({ height: 1400 });
	await goto(p, MOCK_BASE, "/");
	await capsule(p).waitFor({ timeout: 30_000 });
	await pfetch(p, "PUT", "/api/v1/settings/agent-avatars", {
		name: "scout",
		avatarIcon: "🔭",
	});
	await spaGo(p, `/t/${MAIN_THREAD}`);
	await p.locator("[data-testid=transcript]").waitFor({ timeout: 30_000 });
	await injectTaskCall(p, MAIN_THREAD);
	// 混合 agent 的批次只在展开的卡片正文里逐行显示头像
	const head = p
		.locator(".tv-card .tv-head")
		.filter({ has: p.locator(".tv-name", { hasText: /^task$/ }) })
		.last();
	await head.waitFor({ timeout: 10_000 });
	if ((await head.getAttribute("aria-expanded")) !== "true") await head.click();
	const card = p.locator(".tv-agent").first();
	await card.waitFor({ timeout: 10_000 });
	await card.scrollIntoViewIfNeeded();
	const avs = await p.locator(".tv-agent").evaluateAll((els) =>
		els.map((e) => {
			const av = e.querySelector('[role="img"][data-state]');
			const face = av?.querySelector("[data-avatar]");
			return {
				text: (e.textContent ?? "").trim(),
				state: av?.getAttribute("data-state") ?? "",
				avatar: face?.getAttribute("data-avatar") ?? "",
			};
		}),
	);
	for (const a of avs)
		note(
			`task 卡片 agent「${clip(a.text, 40)}」state=${a.state} avatar=${a.avatar}`,
		);
	expect(
		avs.length > 0 && avs.every((a) => a.state !== ""),
		"task 工具卡片里每个 agent 前都有 agent 头像",
	);
	expect(
		avs.some((a) => a.text.includes("🔭")),
		"设了自定义头像的 scout 显示 🔭",
	);
	expect(
		avs.some((a) => a.text.includes(AGENT_DEFAULT_GLYPHS.reviewer ?? "")),
		"没设头像的 reviewer 显示默认字形",
	);
	await shot(p, "mock-R12-task-card");
}

async function mockR13() {
	const p = await mockHome();
	expect(
		(await p.getByRole("button", { name: /^思考级别：/ }).count()) === 0,
		"首屏没有单独的思考强度按钮",
	);
	expect(
		(await p.getByRole("button", { name: "默认模型" }).count()) === 0,
		"首屏没有单独的模型胶囊",
	);
	const label = (await capsule(p).getAttribute("aria-label")) ?? "";
	expect(/^对象：/.test(label), `只有一个 role 胶囊（${label}）`);
	await openCapsule(p);
	expect(
		(await p.getByTestId("role-capsule-popover").count()) === 1,
		"桌面端是锚在胶囊上的弹层",
	);
	// 选择器列表的行序：对象行（`data-entry`）在前，然后是按厂商分组的模型行（`data-model`）
	const items = await popover(p).evaluate((el) =>
		[...el.querySelectorAll("[data-entry], [data-model]")].map((n) =>
			n.hasAttribute("data-entry")
				? `entry:${n.getAttribute("data-entry")}`
				: "model",
		),
	);
	note(`选择器行序 ${items.slice(0, 12).join(" | ")}`);
	const iA = items.indexOf("entry:assistant");
	const iRoles = MOCK_CYCLE.map((r) => items.indexOf(`entry:${r}`));
	const iModel = items.indexOf("model");
	expect(iA === 0, "第一项是全局助手");
	expect(
		iRoles.every((v, i) => v > iA && (i === 0 || v > (iRoles[i - 1] ?? -1))),
		"然后是 cycleOrder 里的各 role，按顺序",
	);
	expect(iModel > Math.max(...iRoles), "然后是按厂商分组的模型");
	const roleRow = popover(p)
		.getByRole("button", { name: /^Mare/ })
		.and(p.locator("[data-entry]"));
	expect(
		(await roleRow.locator("[data-avatar]").count()) > 0 &&
			/gpt|sol/i.test(await roleRow.innerText()),
		"role 行带头像、名字与 selector 解析出的模型",
	);
	await shot(p, "mock-R13-popover");
	// 选一个有思考档位的 role 后再打开：高亮落在它上面，检视栏里是它的思考级别
	await roleRow.click();
	await openCapsule(p);
	const inspector = await popover(p).evaluate((el) => {
		const rg = el.querySelector("aside [role=radiogroup]");
		const hi = el.querySelector("[data-highlighted] [data-role]");
		return (
			!!rg &&
			hi?.getAttribute("data-role") === "Mare" &&
			hi.getAttribute("aria-current") === "true"
		);
	});
	expect(inspector, "思考级别在检视栏里，跟着高亮的选中项（Mare）");
	const lbl2 = (await capsule(p).getAttribute("aria-label")) ?? "";
	expect(
		lbl2 === "对象：Mare" &&
			(await capsule(p).locator("[data-avatar]").count()) > 0,
		`胶囊显示当前对象的头像和名字（${lbl2}）`,
	);
	await p.keyboard.press("Escape");
	// 手机端：底部抽屉
	const m = await mockHome({ width: 390, height: 844 });
	await openCapsule(m);
	const box = await m
		.getByTestId("role-capsule-drawer")
		.locator("xpath=ancestor::*[@role='dialog'][1]")
		.boundingBox();
	expect(
		!!box && Math.abs(box.y + box.height - 844) < 4,
		`手机端是底部抽屉（对话框底边 ${box ? Math.round(box.y + box.height) : "?"} / 844）`,
	);
	await shot(m, "mock-R13-drawer");
}

async function mockR14() {
	const p = await mockHome();
	const pills = p.getByTestId("home-pills");
	expect(await pills.isVisible(), "选普通 role 时执行位置 / 项目胶囊在");
	const attach = p.getByTestId("attach-file-input");
	expect((await attach.count()) === 1, "附件按钮在（role）");
	await pickTarget(p, "全局助手");
	const cls = (await pills.getAttribute("class").catch(() => "")) ?? "";
	expect(
		cls.includes("home-pills-out") || (await pills.count()) === 0,
		`选全局助手时胶囊以动画收起（class ${cls || "已移除"}）`,
	);
	await waitFor(
		"胶囊收起后移出 DOM",
		async () => (await pills.count()) === 0,
		2_000,
	);
	expect(true, "收起后执行位置 / 项目 / profile 胶囊不在 DOM 里");
	expect((await attach.count()) === 1, "附件按钮仍在（全局助手）");
	await shot(p, "mock-R14-assistant");
	await pickTarget(p, "Mare");
	await pills.waitFor();
	const cls2 = (await pills.getAttribute("class")) ?? "";
	expect(
		cls2.includes("home-pills-in"),
		`切回普通 role 时以动画展开（class ${cls2}）`,
	);
	await shot(p, "mock-R14-role");
}

async function stageStyle(p: Page) {
	return p.locator(".home-stage").evaluate((el) => {
		const trayAfter = getComputedStyle(
			el.querySelector(".home-tray") as Element,
			"::after",
		);
		const dots = getComputedStyle(
			el.querySelector(".home-dots") as Element,
			"::before",
		);
		const lineAfter = getComputedStyle(
			el.querySelector(".home-line-l") as Element,
			"::after",
		);
		const field = el.querySelector(".home-field");
		const fs = field ? getComputedStyle(field) : null;
		return {
			target: el.getAttribute("data-target") ?? "",
			trayAfter: trayAfter.content,
			dotsAnim: dots.animationName,
			dotColor: dots.getPropertyValue("--home-dot").trim(),
			flowAnim: lineAfter.animationName,
			flowContent: lineAfter.content,
			fieldH: fs?.getPropertyValue("--field-h").trim() ?? "",
			bg: fs?.backgroundImage.slice(0, 160) ?? "",
			roleH: (el as HTMLElement).style.getPropertyValue("--role-h"),
		};
	});
}

async function mockR15() {
	const p = await mockHome();
	await pickTarget(p, "全局助手");
	await sleep(500);
	const a = await stageStyle(p);
	note(`全局助手：${JSON.stringify(a)}`);
	expect(
		a.target === "assistant" &&
			a.dotsAnim === "home-dots-breathe" &&
			a.flowAnim === "home-line-flow",
		"全局助手：点阵呼吸（home-dots-breathe），两侧的线上有流光（home-line-flow）",
	);
	expect(
		a.trayAfter === "none",
		`托盘外缘没有描边特效（::after content ${a.trayAfter}）`,
	);
	await shot(p, "mock-R15-assistant");
	await pickTarget(p, "Mare");
	await sleep(500);
	const r = await stageStyle(p);
	note(`Mare：${JSON.stringify(r)}`);
	expect(
		r.target === "role" &&
			r.dotsAnim === "none" &&
			r.flowContent === "none" &&
			r.trayAfter === "none",
		"普通 role：点阵与线都静止，没有描边特效",
	);
	expect(
		r.roleH !== "" && r.bg !== a.bg && r.dotColor !== a.dotColor,
		`普通 role 的色场用该 role 的色相（--role-h ${r.roleH}），与助手色场不同；点阵是中性色、不染助手色`,
	);
	await shot(p, "mock-R15-role");
	const rm = await mockHome({ reduced: true });
	await pickTarget(rm, "全局助手");
	await sleep(500);
	const s = await stageStyle(rm);
	note(`减少动态效果 + 全局助手：${JSON.stringify(s)}`);
	expect(
		s.dotsAnim === "none" && s.flowContent === "none",
		`减少动态效果时点阵不呼吸、线上无流光（animation ${s.dotsAnim}，流光 content ${s.flowContent}）`,
	);
	await shot(rm, "mock-R15-assistant-reduced");
	await pickTarget(rm, "Mare");
	await sleep(500);
	const sr = await stageStyle(rm);
	expect(
		sr.dotColor !== s.dotColor && sr.bg !== s.bg,
		"减少动态效果时仍靠颜色区分全局助手与普通 role（点阵色、色场）",
	);
}

async function mockR16() {
	const p = await mockHome();
	await pickTarget(p, "全局助手");
	await openCapsule(p);
	await popover(p).locator('[data-model="anthropic/claude-sonnet-5"]').click();
	await p.getByTestId("attach-file-input").setInputFiles({
		name: "m13-r16.txt",
		mimeType: "text/plain",
		buffer: Buffer.from("m13 r16\n"),
	});
	await p
		.getByRole("textbox", { name: "提示词" })
		.fill("m13 R16：问问全局助手");
	await sleep(500);
	await shot(p, "mock-R16-assistant-before");
	await p.getByRole("button", { name: "发送" }).click();
	await p.waitForURL(/\/assistant\/T-/, { timeout: 15_000 });
	expect(true, `跳转到 ${new URL(p.url()).pathname}`);
	const post = (await reqs(p)).find(
		(r) =>
			r.method === "POST" &&
			apiPath(r.url) === "/api/v1/assistant/conversations",
	);
	const body = post?.body
		? (JSON.parse(post.body) as Record<string, unknown>)
		: {};
	note(`createAssistantConversation 请求体 ${clip(post?.body ?? "无", 300)}`);
	expect(body.prompt === "m13 R16：问问全局助手", "新助手对话带首条消息");
	expect(
		Array.isArray(body.files) && body.files.length === 1,
		"带上附件（文件库 id）",
	);
	expect(
		isRecord(body.model) &&
			body.model.provider === "anthropic" &&
			body.model.id === "claude-sonnet-5",
		"带上所选模型",
	);
	const up = (await reqs(p)).find(
		(r) =>
			r.method === "POST" &&
			apiPath(r.url).startsWith("/api/v1/assistant/files"),
	);
	expect(!!up, `附件先传进文件库（${up ? apiPath(up.url) : "无"}）`);
	await shot(p, "mock-R16-assistant-after");
	// 普通 role
	const r = await mockHome();
	await pickTarget(r, "Mare");
	await r.getByRole("textbox", { name: "提示词" }).fill("m13 R16：交给 Mare");
	await r.getByRole("button", { name: "发送" }).click();
	await r.waitForURL(/\/t\/T-/, { timeout: 20_000 });
	const tp = (await reqs(r)).find(
		(x) => x.method === "POST" && apiPath(x.url) === "/api/v1/threads",
	);
	note(`createThread 请求体 ${clip(tp?.body ?? "无", 300)}`);
	expect(
		!!tp?.body &&
			(JSON.parse(tp.body) as Record<string, unknown>).role === "Mare",
		"选普通 role 发送：新线程带 role=Mare",
	);
	await shot(r, "mock-R16-role-thread");
	note("「转录回合头显示该 role」：mock 新建线程不处理 role，在真机部分核对");
}

async function mockR17() {
	const p = await mockPage();
	await goto(p, MOCK_BASE, `/t/${MAIN_THREAD}`);
	const box = p.locator('textarea[aria-label="消息"]');
	await box.waitFor({ timeout: 30_000 });
	const dock = box
		.locator(
			"xpath=ancestor::form[1] | ancestor::*[contains(@class,'glass')][1]",
		)
		.last();
	expect(
		(await dock.getByText("全局助手").count()) === 0,
		"线程输入坞里没有「全局助手」",
	);
	expect(
		(await p.getByRole("button", { name: /^对象：/ }).count()) === 0,
		"线程里没有带全局助手的 role 胶囊",
	);
	await dialogModels(p);
	expect(
		(await threadPicker(p).getByText("全局助手").count()) === 0,
		"线程内模型对话框没有「全局助手」选项",
	);
	await shot(p, "mock-R17-thread-model-dialog");
}

async function mockR18() {
	const p = await mockPage();
	await goto(p, MOCK_BASE, "/settings/identity");
	const card = p.getByTestId("identity-new-thread");
	await card.waitFor({ timeout: 30_000 });
	await card.getByRole("radio", { name: /全局助手/ }).dispatchEvent("click");
	await sleep(500);
	const put = (await reqs(p)).find(
		(r) => r.method === "PUT" && apiPath(r.url) === "/api/v1/settings/composer",
	);
	expect(
		!!put && put.body?.includes('"assistant"') === true,
		`「新对话默认对象」写到服务端（PUT settings/composer ${put?.body ?? "无"}）`,
	);
	await shot(p, "mock-R18-identity");
	await spaGo(p, "/");
	await waitFor(
		"首屏按默认对象预选全局助手",
		async () =>
			(await p.locator(".home-stage").getAttribute("data-target")) ===
			"assistant",
		5_000,
	);
	expect(
		(await capsule(p).getAttribute("aria-label")) === "对象：全局助手",
		"首屏预选全局助手",
	);
	await pickTarget(p, "Mare");
	await spaGo(p, "/settings/devices");
	await spaGo(p, "/");
	await waitFor("回到首屏", () => p.locator(".home-stage").isVisible(), 5_000);
	await sleep(300);
	expect(
		(await p.locator(".home-stage").getAttribute("data-target")) ===
			"assistant",
		"再次打开首屏仍按默认对象预选，不记上一次选的 Mare",
	);
	await shot(p, "mock-R18-home");
	note("换设备一致在真机部分用另一个浏览器上下文核对");
}

async function openLibrary(p: Page) {
	await p.getByRole("button", { name: "文件库" }).click();
	const d = p.getByRole("dialog", { name: "文件库" });
	await d.waitFor();
	return d;
}

async function mockR19() {
	const p = await mockPage();
	await goto(p, MOCK_BASE, `/assistant/${ASSISTANT_THREAD}`);
	const box = p.locator('textarea[aria-label="消息"]');
	await box.waitFor({ timeout: 30_000 });
	await p.getByTestId("attach-file-input").setInputFiles({
		name: "m13-r19.txt",
		mimeType: "text/plain",
		buffer: Buffer.from("m13 r19 附件\n"),
	});
	await p.locator('li[title="m13-r19.txt"]').waitFor({ timeout: 10_000 });
	const up = (await reqs(p)).find(
		(r) =>
			r.method === "POST" &&
			apiPath(r.url).startsWith("/api/v1/assistant/files"),
	);
	expect(
		!!up && apiPath(up.url).includes(`threadId=${ASSISTANT_THREAD}`),
		`助手对话里的附件上传进文件库（${up ? apiPath(up.url) : "无"}）`,
	);
	await box.fill("m13 R19：看看这个附件");
	await box.press("Enter");
	await sleep(1500);
	const lib = await openLibrary(p);
	const row = lib.locator('li[aria-label="m13-r19.txt"]');
	await row.waitFor({ timeout: 10_000 });
	const scope = row.getByTestId("scope");
	expect(
		(await scope.innerText()).includes("仅此对话") &&
			(await row.innerText()).includes("上传"),
		"附件进入文件库，默认属于该对话（仅此对话 / 上传）",
	);
	await row.getByRole("button", { name: "设为库内共享" }).click();
	await waitFor(
		"切换为库内共享",
		async () => (await scope.innerText()).includes("库内共享"),
		5_000,
	);
	const patch = (await reqs(p)).find(
		(r) =>
			r.method === "PATCH" &&
			apiPath(r.url).startsWith("/api/v1/assistant/files/"),
	);
	expect(
		!!patch && patch.body?.includes("shared") === true,
		`面板把文件切换为库内共享（PATCH ${patch?.body ?? "无"}）`,
	);
	await shot(p, "mock-R19-library");
}

async function mockR24() {
	const p = await mockPage({ height: 1000 });
	await goto(p, MOCK_BASE, `/assistant/${ASSISTANT_THREAD}`);
	await p.locator('textarea[aria-label="消息"]').waitFor({ timeout: 30_000 });
	const lib = await openLibrary(p);
	const rows = lib.locator("li[aria-label]");
	await rows.first().waitFor({ timeout: 10_000 });
	const names = await rows.evaluateAll((els) =>
		els.map((e) => e.getAttribute("aria-label") ?? ""),
	);
	expect(names.length >= 3, `列表 ${names.join("、")}`);
	await shot(p, "mock-R24-list");
	const orbRow = lib.locator('li[aria-label="shots-index.json"]');
	const orbText = await orbRow.innerText();
	expect(
		orbText.includes("来源线程") &&
			orbText.includes("orb") &&
			orbText.includes("库内共享"),
		`能看到来源（orb、来源线程、库内共享）：${oneLine(orbText)}`,
	);
	expect(
		(await orbRow.locator(`a[href="/t/${SPACE_IDLE_THREAD}"]`).count()) === 1,
		"来源线程链接到 /t/<线程>",
	);
	const own = lib.locator('li[aria-label="周报-11月第2周.md"]');
	expect(
		(await own.innerText()).includes("所属对话") &&
			(await own.innerText()).includes("本对话"),
		"能看到所属对话（本对话）",
	);
	// 预览
	await own.getByRole("button", { name: "预览" }).click();
	await waitFor(
		"预览出正文",
		async () => (await lib.innerText()).includes("完成 M12 的配置下发"),
		10_000,
	);
	expect(true, "预览显示文件内容");
	await shot(p, "mock-R24-preview");
	await lib.getByRole("button", { name: /返回/ }).first().click();
	// 下载
	const [dl] = await Promise.all([
		p.waitForEvent("download", { timeout: 10_000 }),
		lib
			.locator('li[aria-label="周报-11月第2周.md"]')
			.getByRole("button", { name: "下载" })
			.click(),
	]);
	expect(
		dl.suggestedFilename() === "周报-11月第2周.md",
		`下载（${dl.suggestedFilename()}）`,
	);
	// 切换共享 / 私有
	const csv = lib.locator('li[aria-label="等待中的线程.csv"]');
	await csv.getByRole("button", { name: "设为库内共享" }).click();
	await waitFor(
		"变为库内共享",
		async () =>
			(await csv.getByTestId("scope").innerText()).includes("库内共享"),
		5_000,
	);
	await csv.getByRole("button", { name: "改回仅所属对话" }).click();
	await waitFor(
		"改回仅此对话",
		async () =>
			(await csv.getByTestId("scope").innerText()).includes("仅此对话"),
		5_000,
	);
	expect(true, "共享 / 私有来回切换");
	// 删除
	p.once("dialog", (d) => void d.accept());
	await csv.getByRole("button", { name: "删除" }).click();
	await waitFor("删除后行消失", async () => (await csv.count()) === 0, 5_000);
	expect(
		(await reqs(p)).some(
			(r) =>
				r.method === "DELETE" &&
				apiPath(r.url).startsWith("/api/v1/assistant/files/"),
		),
		"删除（DELETE /api/v1/assistant/files/:id）",
	);
	await shot(p, "mock-R24-after");
	expect(
		!(await lib.innerText()).match(/上限|配额|已用/),
		"面板没有总量上限提示",
	);
}

async function mockR26() {
	const p = await mockPage({ height: 1000 });
	await goto(p, MOCK_BASE, "/settings/agent-skills");
	const group = p.locator('[data-more-group="agent"]').first();
	await group.waitFor({ timeout: 30_000 });
	const entries = await group
		.locator("a[data-more-entry]")
		.evaluateAll((els) =>
			els.map(
				(e) =>
					`${e.getAttribute("data-more-entry")}=${(e.textContent ?? "").trim()}`,
			),
		);
	note(`Agent 分组 ${entries.join("、")}`);
	const want = [
		...AGENT_PAGES.map(([k, v]) => `${k}=${v}`),
		"agent-config=高级",
	];
	expect(
		want.every((w) => entries.includes(w)) &&
			entries.at(-1) === "agent-config=高级",
		"设置页有 Agent 分组：六个子页 + 「高级」原始文件浏览器",
	);
	await shot(p, "mock-R26-group");
	for (const [k] of AGENT_PAGES) {
		await group.locator(`a[data-more-entry="${k}"]`).click();
		await sleep(600);
		expect(
			new URL(p.url()).pathname === `/settings/${k}` &&
				(await p.getByText(scopeNote(k)).count()) > 0,
			`${k} 子页可打开`,
		);
	}
	await group.locator('a[data-more-entry="agent-config"]').click();
	await p.locator('[role="tree"]').first().waitFor({ timeout: 10_000 });
	expect(
		(await p.locator('[role="treeitem"][data-path="config.yml"]').count()) ===
			1,
		"「高级」是原始文件浏览器（列出 config.yml）",
	);
	await shot(p, "mock-R26-advanced");
}

/** 在设置页某张卡片（h3 = title）里点「新建」。 */
async function newIn(p: Page, title: string) {
	await p
		.locator("section")
		.filter({ has: p.getByRole("heading", { name: title, exact: true }) })
		.getByRole("button", { name: /新建/ })
		.first()
		.click();
	return dialog(p);
}

async function confirmDelete(p: Page, name: string) {
	await p
		.getByRole("button", { name: `删除 ${name}`, exact: true })
		.first()
		.click();
	const d = dialog(p);
	await d.getByRole("button", { name: "删除", exact: true }).click();
	await d.waitFor({ state: "detached" });
}

async function mockR27() {
	const p = await mockPage({ height: 1200 });
	await goto(p, MOCK_BASE, "/settings/devices");
	await p
		.getByRole("heading", { name: "设置" })
		.first()
		.waitFor({ timeout: 30_000 });
	await pfetch(
		p,
		"PUT",
		"/api/v1/agent-config/files/config.yml",
		"# m13 验收：这行注释保存后必须保留\ntheme: dark # 行尾注释\n",
	);
	const cfg = () => configText(p);
	const hasKey = async (k: string) => (await cfg()).includes(k);
	// 技能
	await spaGo(p, "/settings/agent-skills");
	let d = await newIn(p, "技能");
	await d.getByLabel("名称").fill("m13-skill");
	await d.getByLabel("描述").fill("M13 验收技能");
	await d.getByRole("button", { name: "创建" }).click();
	await d.waitFor({ state: "detached" });
	expect(
		(await fileGet(p, "skills/m13-skill/SKILL.md")).text.includes(
			"name: m13-skill",
		),
		"技能：新建写出 skills/m13-skill/SKILL.md",
	);
	await p.getByRole("button", { name: "编辑 m13-skill", exact: true }).click();
	d = dialog(p);
	await d.getByLabel("描述").fill("M13 验收技能（改）");
	await d.getByRole("button", { name: "保存" }).click();
	await d.waitFor({ state: "detached" });
	expect(
		(await fileGet(p, "skills/m13-skill/SKILL.md")).text.includes("（改）"),
		"技能：编辑保存",
	);
	await p
		.getByRole("checkbox", { name: "启用 m13-skill" })
		.dispatchEvent("click");
	await waitFor(
		"disabledExtensions 含 skill:m13-skill",
		() => hasKey("skill:m13-skill"),
		5_000,
	);
	expect(true, "技能：停用写 disabledExtensions 的 skill:m13-skill");
	await shot(p, "mock-R27-skills");
	await confirmDelete(p, "m13-skill");
	expect(
		(await fileGet(p, "skills/m13-skill/SKILL.md")).status === 404,
		"技能：删除",
	);
	// 命令 / 规则 / 提示片段 / 指令
	await spaGo(p, "/settings/agent-commands");
	for (const [title, name, key, path] of [
		["斜杠命令", "m13-cmd", "slash-command:m13-cmd", "commands/m13-cmd.md"],
		["规则", "m13-rule", "rule:m13-rule", "rules/m13-rule.md"],
		["提示片段", "m13-prompt", "prompt:m13-prompt", "prompts/m13-prompt.md"],
	] as const) {
		d = await newIn(p, title);
		await d.getByLabel("名称").fill(name);
		const desc = d.getByLabel("描述");
		if ((await desc.count()) > 0) await desc.fill(`M13 ${title}`);
		await d.getByRole("button", { name: "创建" }).click();
		await d.waitFor({ state: "detached" });
		expect((await fileGet(p, path)).status === 200, `${title}：新建 ${path}`);
		await p
			.getByRole("checkbox", { name: `启用 ${name}` })
			.dispatchEvent("click");
		await waitFor(`disabledExtensions 含 ${key}`, () => hasKey(key), 5_000);
		expect(true, `${title}：停用写 ${key}`);
		await confirmDelete(p, name);
		expect((await fileGet(p, path)).status === 404, `${title}：删除`);
	}
	await shot(p, "mock-R27-commands");
	// 提示词与上下文：AGENTS.md 编辑 + 单独停用
	await spaGo(p, "/settings/agent-prompts");
	await p
		.getByRole("checkbox", { name: "启用 AGENTS.md" })
		.dispatchEvent("click");
	await waitFor(
		"context-file:user:AGENTS.md",
		() => hasKey("context-file:user:AGENTS.md"),
		5_000,
	);
	expect(true, "上下文文件：单独停用写 context-file:user:AGENTS.md");
	await p.getByRole("button", { name: "创建 RULES.md", exact: true }).click();
	d = dialog(p);
	await d.locator(".cm-content").first().click();
	await p.keyboard.type("# m13 规则\n");
	await d.getByRole("button", { name: "保存" }).click();
	await d.waitFor({ state: "detached" });
	expect(
		(await fileGet(p, "RULES.md")).text.includes("m13 规则"),
		"提示词文件：新建 RULES.md",
	);
	await confirmDelete(p, "RULES.md");
	expect(
		(await fileGet(p, "RULES.md")).status === 404,
		"提示词文件：删除 RULES.md",
	);
	await shot(p, "mock-R27-prompts");
	// 扩展 / hook / 工具
	await spaGo(p, "/settings/agent-extensions");
	await p.getByRole("button", { name: "在「Hook」中新建" }).click();
	d = dialog(p);
	await d.getByLabel("文件名").fill("bash.ts");
	await d.getByRole("button", { name: "继续" }).click();
	d = dialog(p);
	await d.locator(".cm-content").first().click();
	await p.keyboard.type("export default {};\n");
	await d.getByRole("button", { name: "保存" }).click();
	await d.waitFor({ state: "detached" });
	expect(
		(await fileGet(p, "hooks/pre/bash.ts")).status === 200,
		"hook：新建 hooks/pre/bash.ts",
	);
	await p
		.getByRole("checkbox", { name: "启用 bash.ts" })
		.dispatchEvent("click");
	await waitFor(
		"hook:pre:bash:bash.ts",
		() => hasKey("hook:pre:bash:bash.ts"),
		5_000,
	);
	expect(true, "hook：停用写 hook:pre:bash:bash.ts");
	await confirmDelete(p, "bash.ts");
	expect((await fileGet(p, "hooks/pre/bash.ts")).status === 404, "hook：删除");
	// 子 agent：新建 / 停用写 task.disabledAgents / 删除
	await spaGo(p, "/settings/agent-subagents");
	await createAgent(p, "m13-agent", "M13 验收子 agent");
	expect(
		(await fileGet(p, "agents/m13-agent.md")).text.includes("name: m13-agent"),
		"子 agent：新建 agents/m13-agent.md",
	);
	await p
		.getByRole("checkbox", { name: "启用 m13-agent" })
		.dispatchEvent("click");
	await waitFor(
		"task.disabledAgents 含 m13-agent",
		async () => /disabledAgents:[\s\S]*m13-agent/.test(await cfg()),
		5_000,
	);
	expect(true, "子 agent：停用写 task.disabledAgents");
	await confirmDelete(p, "m13-agent");
	expect(
		(await fileGet(p, "agents/m13-agent.md")).status === 404,
		"子 agent：删除",
	);
	const finalCfg = await cfg();
	note(`config.yml：${clip(oneLine(finalCfg), 300)}`);
	expect(
		finalCfg.includes("# m13 验收：这行注释保存后必须保留") &&
			finalCfg.includes("# 行尾注释"),
		"多次 GUI 修改后 config.yml 原有注释保留",
	);
}

async function mockR28() {
	const p = await mockPage({ height: 1200 });
	await goto(p, MOCK_BASE, "/settings/agent-subagents");
	const row = p.locator('li[data-agent="scout"]');
	await row.waitFor({ timeout: 30_000 });
	expect(
		(await row.getByRole("button", { name: "编辑 scout" }).count()) === 0 &&
			(await row.getByRole("button", { name: "删除 scout" }).count()) === 0,
		"内置 agent 没有编辑 / 删除（定义只读）",
	);
	const text = await row.innerText();
	expect(
		text.includes("scout") && text.includes("侦察"),
		`只读展示名字与简介（${oneLine(text)}）`,
	);
	await row
		.getByRole("checkbox", { name: "启用 scout" })
		.dispatchEvent("click");
	await waitFor(
		"task.disabledAgents 含 scout",
		async () => /disabledAgents:[\s\S]*scout/.test(await configText(p)),
		5_000,
	);
	expect(true, "内置 agent 可停用（task.disabledAgents）");
	await row.getByRole("button", { name: "设置 scout 的模型与头像" }).click();
	// 「添加」打开模型选择器：筛选后点目录里的模型行（目录里没有时点「使用 …」）
	await row.getByRole("button", { name: "为 scout 添加覆盖模型" }).click();
	const overridePicker = p.locator("[data-model-picker]").last();
	await overridePicker.getByRole("textbox").fill("anthropic/claude-sonnet-5");
	await overridePicker
		.locator(
			'[data-model="anthropic/claude-sonnet-5"], [data-model-custom="anthropic/claude-sonnet-5"]',
		)
		.first()
		.click();
	await waitFor(
		"agentModelOverrides.scout",
		async () =>
			/agentModelOverrides:[\s\S]*scout:\s*anthropic\/claude-sonnet-5/.test(
				await configText(p),
			),
		5_000,
	);
	expect(true, "内置 agent 可设模型覆盖（task.agentModelOverrides.scout）");
	await setGlyph(row, "🛰");
	await waitFor(
		"scout 头像",
		async () =>
			(
				await row
					.locator('[data-avatar="icon"]')
					.first()
					.innerText()
					.catch(() => "")
			).includes("🛰"),
		5_000,
	);
	expect(true, "内置 agent 头像可改");
	await shot(p, "mock-R28-builtin");
}

async function applyStatus(p: Page) {
	return (await pfetch(p, "GET", "/api/v1/agent-config/apply-status")).json as {
		threads: { threadId: string; reasons: string[]; scheduled: boolean }[];
		pendingReloads: number;
		push: { pending: boolean; appliedAt?: number; failed: unknown[] };
	};
}

async function mockR29() {
	const p = await mockPage();
	await goto(p, MOCK_BASE, "/settings/agent-skills");
	await p.getByText(SCOPE_NOTE).waitFor({ timeout: 30_000 });
	const d = await newIn(p, "技能");
	await d.getByLabel("名称").fill("m13-live");
	await d.getByLabel("描述").fill("M13 下发");
	const t0 = Date.now();
	await d.getByRole("button", { name: "创建" }).click();
	const pending = await waitFor(
		"写入后 apply-status push.pending=true",
		async () => (await applyStatus(p)).push.pending,
		1_500,
		100,
	);
	expect(
		pending && Date.now() - t0 < 1_500,
		`写入后 ${Date.now() - t0}ms 内 push.pending=true`,
	);
	await p
		.getByText("正在下发到运行中的 orb")
		.waitFor({ timeout: 3_000 })
		.then(
			() => expect(true, "状态条先显示「正在下发到运行中的 orb……」"),
			() => expect(false, "状态条先显示「正在下发到运行中的 orb……」"),
		);
	await shot(p, "mock-R29-pushing");
	const done = await waitFor(
		"push.pending 转 false",
		async () => {
			const s = await applyStatus(p);
			return !s.push.pending && s.push.appliedAt ? s : null;
		},
		10_000,
		200,
	);
	expect(
		!!done.push.appliedAt,
		`下发完成（appliedAt 距写入 ${(done.push.appliedAt ?? 0) - t0}ms）`,
	);
	await p.getByText("已下发：空闲的会话随即生效").waitFor({ timeout: 10_000 });
	expect(
		true,
		"状态条显示「已下发：空闲的会话随即生效，正在跑回合的会话在回合结束后生效。」",
	);
	expect(
		(await p.getByText(/个会话需重启后生效/).count()) === 0,
		"技能改动不要求重启会话",
	);
	await shot(p, "mock-R29-pushed");
	note("写进 ready orb、10 秒内、会话热生效在真机部分核对");
}

async function mockR30() {
	const p = await mockPage();
	await goto(p, MOCK_BASE, "/settings/agent-extensions");
	await p.getByText(SCOPE_NOTE).waitFor({ timeout: 30_000 });
	await p.getByRole("button", { name: "在「Hook」中新建" }).click();
	let d = dialog(p);
	await d.getByLabel("文件名").fill("bash.ts");
	await d.getByRole("button", { name: "继续" }).click();
	d = dialog(p);
	await d.locator(".cm-content").first().click();
	await p.keyboard.type("export default {};\n");
	await d.getByRole("button", { name: "保存" }).click();
	await d.waitFor({ state: "detached" });
	const title = p.getByText(/^\d+ 个会话需重启后生效$/);
	await title.waitFor({ timeout: 10_000 });
	const n = Number((await title.innerText()).match(/\d+/)?.[0] ?? 0);
	const st = await applyStatus(p);
	expect(
		n > 0 && n === st.threads.length,
		`顶部显示「${n} 个会话需重启后生效」`,
	);
	expect(
		st.threads.every((t) => t.reasons.includes("hooks/pre/bash.ts")),
		"原因是 hooks/pre/bash.ts",
	);
	const btn = p.getByRole("button", { name: "重启这些会话" });
	expect(await btn.isVisible(), "给出「重启这些会话」按钮");
	await shot(p, "mock-R30-banner");
	await btn.click();
	await p.getByText("已安排重启").waitFor({ timeout: 5_000 });
	expect(
		(await reqs(p)).some(
			(r) =>
				r.method === "POST" &&
				apiPath(r.url) === "/api/v1/agent-config/restart-sessions",
		),
		"点按钮调用 restart-sessions，显示「已安排重启」",
	);
	expect((await btn.count()) === 0, "安排后按钮消失");
	await shot(p, "mock-R30-scheduled");
	note("会话空闲时停掉 omp、下次发消息时自动拉起在真机部分核对");
}

async function mockR31() {
	const p = await mockPage();
	await goto(p, MOCK_BASE, "/settings/agent-skills");
	await p.getByText(SCOPE_NOTE).first().waitFor({ timeout: 30_000 });
	for (const [k, label] of AGENT_PAGES) {
		await spaGo(p, `/settings/${k}`);
		await sleep(400);
		const want = scopeNote(k);
		expect(
			(await p.getByText(want).count()) > 0,
			`「${label}」页标明「${want}」`,
		);
	}
	await shot(p, "mock-R31-roles-note");
	await spaGo(p, "/settings/agent-config");
	await p.locator('[role="tree"]').first().waitFor({ timeout: 10_000 });
	expect(
		(await p.getByText(SCOPE_NOTE).count()) === 0,
		"「高级」（config.yml / models.yml 所在的原始文件浏览器）不标这句",
	);
	note("config.yml / models.yml 确实下发给全局助手在真机部分核对");
}

// ================================================================ 真机部分

interface Bg {
	name: string;
	proc: Subprocess;
}
const bgs: Bg[] = [];
let auth: RealAuth | undefined;
let realReady = false;
let realSetupError = "";
const LOGS = `${TMP}/logs`;

function bg(
	name: string,
	cmd: string[],
	env?: Record<string, string | undefined>,
	cwd = ROOT,
): Bg {
	mkdirSync(LOGS, { recursive: true });
	const fd = openSync(`${LOGS}/${name}.log`, "a");
	const proc = Bun.spawn(cmd, {
		cwd,
		env: env ?? process.env,
		stdin: "ignore",
		stdout: fd,
		stderr: fd,
	});
	const b = { name, proc };
	bgs.push(b);
	return b;
}

const realEnv = () => ({
	...process.env,
	ORYN_API_URL: REAL_API,
	ORYN_API_TOKEN: auth?.token ?? "",
});
const cli = (args: string) => sh(`${q(`${BIN}/oryn`)} ${args}`, realEnv());

async function rapi(method: string, path: string, body?: unknown) {
	const raw = typeof body === "string";
	const res = await fetch(`${REAL_API}${path}`, {
		method,
		headers: {
			Authorization: `Bearer ${auth?.token ?? ""}`,
			"X-Oryn-CSRF": "1",
			...(body !== undefined
				? {
						"Content-Type": raw
							? "application/octet-stream"
							: "application/json",
					}
				: {}),
		},
		body:
			body === undefined
				? undefined
				: raw
					? (body as string)
					: JSON.stringify(body),
	});
	const text = await res.text();
	let json: unknown = null;
	try {
		json = JSON.parse(text);
	} catch {
		// 空体
	}
	return { status: res.status, text, json };
}
async function rjson<T>(
	method: string,
	path: string,
	body?: unknown,
): Promise<T> {
	const r = await rapi(method, path, body);
	if (r.status >= 300)
		throw new Error(`${method} ${path} → ${r.status} ${clip(r.text)}`);
	return r.json as T;
}

const REAL_CONFIG = `# m13 验收：这行注释保存后必须保留
cycleOrder: [plan, slow]
modelRoles:
  plan: m13fake/m13-model-a
  slow: m13fake/m13-model-a
  m13extra: m13fake/m13-model-a
`;
const modelsYml = (ids: string[]) =>
	`providers:\n  m13fake:\n    baseUrl: http://127.0.0.1:9/v1\n    apiKey: m13-fake-key\n    api: openai-completions\n    models:\n${ids.map((id) => `      - id: ${id}\n        name: ${id.toUpperCase()}\n        reasoning: true\n`).join("")}`;

/** R9 用的 fakeomp 脚本：一条 scout + reviewer 的 task 调用与结果（Core task 工具的参数与 details 形状）。 */
const TASK_SCRIPT = [
	{
		entry: {
			role: "user",
			content: [{ type: "text", text: "{{prompt}}" }],
			attribution: "user",
		},
	},
	{ emit: { type: "agent_start" } },
	{
		entry: {
			role: "assistant",
			content: [
				{
					type: "toolCall",
					id: "call_m13task",
					name: "task",
					arguments: {
						agent: "scout",
						tasks: [
							{ name: "ScoutA", agent: "scout", task: "look around" },
							{ name: "ReviewB", agent: "reviewer", task: "review" },
						],
					},
				},
			],
			api: "fake",
			provider: "fake",
			model: "fake-model",
			stopReason: "toolUse",
			usage: {
				input: 1,
				output: 1,
				cacheRead: 0,
				cacheWrite: 0,
				totalTokens: 2,
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
			},
		},
	},
	{
		entry: {
			role: "toolResult",
			toolCallId: "call_m13task",
			toolName: "task",
			content: [{ type: "text", text: "both done" }],
			details: {
				results: [
					{ id: "ScoutA", agent: "scout", exitCode: 0, output: "ok" },
					{ id: "ReviewB", agent: "reviewer", exitCode: 0, output: "ok" },
				],
			},
			isError: false,
		},
	},
	{
		entry: {
			role: "assistant",
			content: [{ type: "text", text: "m13 task done" }],
			api: "fake",
			provider: "fake",
			model: "fake-model",
			stopReason: "stop",
			usage: {
				input: 1,
				output: 1,
				cacheRead: 0,
				cacheWrite: 0,
				totalTokens: 2,
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
			},
		},
	},
	{
		emit: { type: "agent_end", messages: [], isTerminal: true, yielded: true },
	},
	{
		emit: {
			type: "prompt_result",
			agentInvoked: true,
			status: "completed",
			sessionSettled: true,
		},
	},
	{ emit: { type: "session_settled" } },
]
	.map((l) => JSON.stringify(l))
	.join("\n");

async function setupReal() {
	if (process.getuid?.() !== 0)
		throw new Error("真机部分要以 root 运行（协调宿主降权）");
	for (const b of ["orynd", "orbd", "oryn", "fakeomp"])
		if (!existsSync(`${BIN}/${b}`))
			throw new Error(`缺少 ${BIN}/${b}：先 make build（或设 M13_BIN）`);
	if (!existsSync(OMP_REAL))
		throw new Error(`缺少 ${OMP_REAL}（scripts/fetch-core.sh）`);
	mkdirSync(`${TMP}/bin`, { recursive: true });
	chmodSync(TMP, 0o755);
	chmodSync(`${TMP}/bin`, 0o755);
	copyFileSync(`${BIN}/fakeomp`, `${TMP}/bin/fakeomp`);
	chmodSync(`${TMP}/bin/fakeomp`, 0o755);
	// 协调宿主的 omp 必须是 ELF（宿主按 ELF 头核对可执行路径），所以不能用 shell 包装：用 go build -overlay 给 fakeomp
	// 加一个 init，`omp models …`（中央模型目录）与 `--no-session`（设置 schema）转给真 omp，其余照常是 fakeomp
	const delegate = resolve(ROOT, "cmd/fakeomp/zz_m13_delegate.go");
	writeFileSync(
		`${TMP}/delegate.go`,
		`package main

import (
	"os"
	"slices"
	"syscall"
)

var m13RealOmp string

func init() {
	if m13RealOmp != "" && len(os.Args) > 1 && (os.Args[1] == "models" || slices.Contains(os.Args[1:], "--no-session")) {
		_ = syscall.Exec(m13RealOmp, append([]string{m13RealOmp}, os.Args[1:]...), os.Environ())
	}
}
`,
	);
	writeFileSync(
		`${TMP}/overlay.json`,
		JSON.stringify({ Replace: { [delegate]: `${TMP}/delegate.go` } }),
	);
	const built = sh(
		`CGO_ENABLED=0 go build -overlay ${q(`${TMP}/overlay.json`)} -ldflags ${q(`-X main.m13RealOmp=${resolve(OMP_REAL)}`)} -o ${q(`${TMP}/bin/omp`)} ./cmd/fakeomp`,
	);
	if (built.code !== 0)
		throw new Error(`构建协调宿主用的 omp 失败：${built.out}`);
	chmodSync(`${TMP}/bin/omp`, 0o755);
	writeFileSync(`${TMP}/task-script.jsonl`, `${TASK_SCRIPT}\n`, {
		mode: 0o644,
	});
	writeFileSync(
		`${TMP}/bin/agent`,
		`#!/bin/sh\nif [ -f ${TMP}/task-mode ]; then export FAKEOMP_SCRIPT=${TMP}/task-script.jsonl; fi\nexec ${TMP}/bin/fakeomp "$@"\n`,
		{ mode: 0o755 },
	);
	// 协调宿主目录：固定路径（复用降权账号），每次清空
	if (existsSync(COORD_DIR))
		for (const f of readdirSync(COORD_DIR))
			rmSync(`${COORD_DIR}/${f}`, { recursive: true, force: true });
	writeFileSync(
		`${TMP}/orynd.yaml`,
		`listen: ["127.0.0.1:${REAL_PORT}"]
public_url: "${REAL_UI}"
portal_domain: ""
data_dir: "${TMP}/data"
log_level: "debug"
idle: { agent_idle: "30m", user_idle: "30m", stop_after_frozen: "24h", wake_on_focus: true }
orb_defaults: { provider: "local", type: "" }
coordinator: { omp_path: "${TMP}/bin/omp", dir: "${COORD_DIR}", egress_port: 7911, verify_omp: false }
`,
	);
	writeFileSync(
		`${TMP}/provider.yaml`,
		`url: "${REAL_API}"\ntoken_file: "${TMP}/provider.token"\norb_url: "${REAL_API}"\ndriver: process\n`,
	);
	bg("orynd", [`${BIN}/orynd`, "--config", `${TMP}/orynd.yaml`]);
	await waitFor(
		"orynd 就绪",
		async () => (await fetch(`${REAL_API}/healthz`)).ok,
		30_000,
	);
	const admin = (a: string) =>
		sh(`${q(`${BIN}/orynd`)} --config ${q(`${TMP}/orynd.yaml`)} admin ${a}`);
	const dev = admin("create-device --name m13-accept --kind cli");
	if (dev.code !== 0) throw new Error(`create-device：${dev.out}`);
	const ses = admin("create-web-session --user-agent m13-accept");
	if (ses.code !== 0) throw new Error(`create-web-session：${ses.out}`);
	auth = {
		token: dev.stdout.split("\n").at(-1) ?? "",
		session: ses.stdout.split("\n").at(-1) ?? "",
	};
	// 首屏偏好存在 owner 行上（没有 owner → 409）：全新库里补一个 owner（正式部署由首次 Passkey 注册创建）
	const own = sh(
		`sqlite3 ${q(`${TMP}/data/oryn.db`)} ${q(`INSERT OR IGNORE INTO owner (id, display_name, user_handle, created_at) VALUES (1, 'm13', randomblob(16), ${Date.now()})`)}`,
	);
	if (own.code !== 0) throw new Error(`写 owner 行：${own.out}`);
	const prov = cli("provider create local");
	if (prov.code !== 0) throw new Error(`provider create：${prov.out}`);
	writeFileSync(
		`${TMP}/provider.token`,
		`${prov.stdout.split("\n").at(-1)}\n`,
		{ mode: 0o600 },
	);
	bg(
		"provider",
		[`${BIN}/oryn`, "provider", "run", "--config", `${TMP}/provider.yaml`],
		{
			...process.env,
			ORYN_ORBD_BIN: `${BIN}/orbd`,
			ORYN_AGENT_BIN: `${TMP}/bin/agent`,
			ORYN_PROCESS_ROOT: `${TMP}/orbs`,
			// process 驱动的 orbd 共用宿主网络：多台 orb 不能都占 7811
			ORYN_EGRESS_ADDR: "127.0.0.1:0",
		},
	);
	await waitFor(
		"provider 在线",
		async () => {
			const r = await rjson<{ providers: { name: string; online: boolean }[] }>(
				"GET",
				"/api/v1/providers",
			);
			return r.providers.some((p) => p.name === "local" && p.online);
		},
		30_000,
		500,
	);
	// 第二个 vite：非 mock，代理到这套 orynd
	const proxyTargets = [
		"/api",
		"/healthz",
		"/portal",
		"/f/",
		"^/s/[0-9a-f]{32}\\.md$",
	];
	const proxy = `const target = ${JSON.stringify(REAL_API)};
const proxy = Object.fromEntries(${JSON.stringify(proxyTargets)}.map((k) => [k, { target, ws: k === "/api", changeOrigin: true }]));`;
	bg(
		"vite-real",
		["bun", viteModule("real", REAL_UI_PORT, proxy)],
		{ ...process.env, VITE_MOCK: "" },
		WEB,
	);
	await waitFor(
		"真机 vite 就绪",
		async () => (await fetch(`http://127.0.0.1:${REAL_UI_PORT}`)).ok,
		60_000,
		300,
	);
	await rjson("PUT", "/api/v1/agent-config/files/config.yml", REAL_CONFIG);
	await rjson(
		"PUT",
		"/api/v1/agent-config/files/models.yml",
		modelsYml(["m13-model-a"]),
	);
	const coord = await waitFor(
		"协调宿主可用",
		async () => {
			const r = await rjson<{ available: boolean; reason?: string }>(
				"GET",
				"/api/v1/coordinator",
			);
			return r.available ? r : null;
		},
		60_000,
		1_000,
	).catch((e: unknown) => (e instanceof Error ? e.message : String(e)));
	console.log(`    · 协调宿主：${typeof coord === "string" ? coord : "可用"}`);
	realReady = true;
}

function killStragglers() {
	for (const pid of readdirSync("/proc")) {
		if (!/^\d+$/.test(pid) || Number(pid) === process.pid) continue;
		try {
			const cwd = readlinkSync(`/proc/${pid}/cwd`);
			if (cwd.startsWith(TMP) || cwd.startsWith(COORD_DIR))
				process.kill(Number(pid), "SIGKILL");
		} catch {
			// 已退出或无权限
		}
	}
}

async function stopReal() {
	for (const b of [...bgs].reverse()) {
		if (b.proc.exitCode === null && b.proc.signalCode === null) {
			b.proc.kill("SIGTERM");
			const r = await Promise.race([
				b.proc.exited,
				sleep(5_000).then(() => "timeout"),
			]);
			if (r === "timeout") b.proc.kill("SIGKILL");
		}
	}
	killStragglers();
	if (existsSync(TMP)) {
		if (KEEP) console.log(`保留 ${TMP}（日志在 logs/）`);
		else {
			for (const f of ["orynd", "provider", "vite-real"])
				if (existsSync(`${LOGS}/${f}.log`))
					copyFileSync(`${LOGS}/${f}.log`, `${SHOTS}/real-${f}.log`);
			rmSync(TMP, { recursive: true, force: true });
		}
	}
}

const realPage = (o: Omit<PageOpts, "base" | "auth"> = {}) => {
	if (!auth) throw new Error("真机环境未就绪");
	return openPage({ ...o, base: REAL_UI, auth });
};

interface ThreadView {
	id: string;
	state: string;
	orbId?: string;
	kind?: string;
	agentStatus?: string;
	title?: string;
}
const getThread = (id: string) =>
	rjson<ThreadView>("GET", `/api/v1/threads/${id}`);

interface EntryRow {
	seq: number;
	type: string;
	file: string;
	payload: Record<string, unknown>;
}
async function entries(id: string): Promise<EntryRow[]> {
	const out: EntryRow[] = [];
	for (let after = 0; ; ) {
		const r = await rjson<{ entries: EntryRow[]; hasMore: boolean }>(
			"GET",
			`/api/v1/threads/${id}/entries?after=${after}&limit=500`,
		);
		out.push(...r.entries);
		if (!r.hasMore || r.entries.length === 0) return out;
		after = r.entries.at(-1)?.seq ?? after;
	}
}
function msgText(e: EntryRow): string {
	const m = e.payload.message;
	if (!isRecord(m)) return "";
	const c = m.content;
	if (typeof c === "string") return c;
	if (!Array.isArray(c)) return "";
	return c
		.map((x) => (isRecord(x) && typeof x.text === "string" ? x.text : ""))
		.join("\n");
}
const msgRole = (e: EntryRow) =>
	isRecord(e.payload.message) ? String(e.payload.message.role ?? "") : "";

/** 等线程里至少有 n 条带文本的 assistant 消息且线程回到 idle，返回第 n 条起的全部回复文本。 */
async function waitAssistant(
	id: string,
	n: number,
	timeoutMs = 90_000,
): Promise<string> {
	return waitFor(
		`线程 ${id} 第 ${n} 条助手回复`,
		async () => {
			const as = (await entries(id)).filter(
				(e) =>
					e.type === "message" &&
					msgRole(e) === "assistant" &&
					msgText(e).trim(),
			);
			const th = await getThread(id);
			if (th.state === "awaiting_input" || th.state === "error")
				throw new Error(`线程 ${id} 停在 ${th.state}`);
			return as.length >= n && th.state === "idle"
				? as
						.slice(n - 1)
						.map((e) => msgText(e))
						.join("\n")
				: null;
		},
		timeoutMs,
		1_000,
	);
}

async function newWorkThread(prompt: string): Promise<ThreadView> {
	const r = cli(`thread new --no-project --provider local --json ${q(prompt)}`);
	if (r.code !== 0) throw new Error(`thread new：${r.out}`);
	const t = JSON.parse(r.stdout.slice(r.stdout.indexOf("{"))) as
		| ThreadView
		| { thread: ThreadView };
	const th = "thread" in t ? t.thread : t;
	await waitAssistant(th.id, 1, 180_000);
	return getThread(th.id);
}

const orbHome = (orbId: string) => `${TMP}/orbs/orbs/${orbId}/home`;
let workThread: ThreadView | undefined;
async function ensureWorkThread(): Promise<ThreadView> {
	workThread ??= await newWorkThread("m13 真机：普通线程");
	return workThread;
}

let assistantId = "";
async function ensureAssistant(): Promise<string> {
	if (assistantId) return assistantId;
	const r = await rjson<{ thread: ThreadView }>(
		"POST",
		"/api/v1/assistant/conversations",
		{ prompt: "m13 真机：你好" },
	);
	assistantId = r.thread.id;
	await waitAssistant(assistantId, 1, 120_000);
	return assistantId;
}
/** 协调宿主沙箱里该助手对话所在 orb 的 HOME（root 直接读）。 */
async function coordHome(tid: string): Promise<string> {
	const th = await getThread(tid);
	if (!th.orbId) throw new Error("助手对话没有 orb");
	return `${COORD_DIR}/homes/${th.orbId}`;
}

/** 在助手对话里发一条消息（fakeomp 的 probe / hosttool 行），返回这一回合的助手回复。 */
async function askAssistant(
	tid: string,
	text: string,
	extra: Record<string, unknown> = {},
): Promise<string> {
	const before = (await entries(tid)).filter(
		(e) =>
			e.type === "message" && msgRole(e) === "assistant" && msgText(e).trim(),
	).length;
	const frame = { type: "prompt", message: text, ...extra };
	const r = cli(`thread rpc ${tid} ${q(JSON.stringify(frame))}`);
	if (r.code !== 0) throw new Error(`thread rpc prompt：${clip(r.out)}`);
	return waitAssistant(tid, before + 1, 90_000);
}

async function libFiles(threadId?: string) {
	return (
		await rjson<{
			files: {
				id: string;
				name: string;
				scope: string;
				source: string;
				threadId?: string;
				sourceThreadId?: string;
				sha256: string;
			}[];
		}>(
			"GET",
			`/api/v1/assistant/files${threadId ? `?threadId=${threadId}` : ""}`,
		)
	).files;
}

// ---- 真机场景

async function realR1() {
	const orbs0 = (
		await rjson<{ orbs: { id: string; coordinator?: boolean }[] }>(
			"GET",
			"/api/v1/orbs",
		)
	).orbs.filter((o) => !o.coordinator);
	const threads0 = (
		await rjson<{ threads: unknown[] }>("GET", "/api/v1/threads")
	).threads.length;
	note(`开始时普通 orb ${orbs0.length} 台、线程 ${threads0} 个`);
	const cat = await rjson<{
		models: {
			provider: string;
			id: string;
			kind: string;
			thinking?: string[];
		}[];
		stale: boolean;
	}>("GET", "/api/v1/models/catalog");
	const chat = cat.models
		.filter((m) => m.kind === "chat")
		.map((m) => `${m.provider}/${m.id}`);
	const fake = cat.models.find(
		(m) => m.provider === "m13fake" && m.id === "m13-model-a",
	);
	note(
		`中央目录 ${cat.models.length} 个模型（chat ${chat.length}），stale=${cat.stale}`,
	);
	check(
		!!fake,
		"目录包含 models.yml 自定义 provider 的模型 m13fake/m13-model-a",
	);
	expect(
		(fake?.thinking?.length ?? 0) > 0,
		`模型带思考档位（${fake?.thinking?.join("/")}）`,
	);
	const p = await realPage();
	await goto(p, REAL_UI, "/settings/agent-roles");
	await p.locator('[data-testid="roles-cycle"]').waitFor({ timeout: 30_000 });
	const set = await settingsModels(p, "架构");
	expect(
		set.includes("m13fake/m13-model-a") && sameSet(set, chat),
		`设置页模型角色编辑器 = 中央目录 chat 模型（${set.length} 个）`,
	);
	await shot(p, "real-R1-settings");
	await spaGo(p, "/");
	await capsule(p).waitFor({ timeout: 20_000 });
	const home = await capsuleModels(p);
	expect(
		sameSet(home, chat),
		`首屏胶囊选择器的模型列表 = 中央目录（${home.length} 个）`,
	);
	await popover(p).locator('[data-model="m13fake/m13-model-a"]').click();
	await openCapsule(p);
	const radios = (
		await popover(p)
			.getByRole("radiogroup", { name: "思考级别" })
			.getByRole("radio")
			.allInnerTexts()
	).map((s) => s.trim());
	const want = ["自动", ...(fake?.thinking ?? []).map((l) => LEVEL_ZH[l] ?? l)];
	expect(
		radios.join(",") === want.join(","),
		`首屏选中 m13fake 模型后的思考级别 = ${radios.join("/")}`,
	);
	await shot(p, "real-R1-home");
	await p.keyboard.press("Escape");
	const orbs1 = (
		await rjson<{ orbs: { coordinator?: boolean }[] }>("GET", "/api/v1/orbs")
	).orbs.filter((o) => !o.coordinator);
	const threads1 = (
		await rjson<{ threads: unknown[] }>("GET", "/api/v1/threads")
	).threads.length;
	expect(
		orbs1.length === orbs0.length && threads1 === threads0,
		`看两处列表没有新建或唤醒任何 orb / 线程（orb ${orbs1.length}，线程 ${threads1}）`,
	);
	const th = await ensureWorkThread();
	await spaGo(p, `/t/${th.id}`);
	const dlg = await dialogModels(p);
	expect(sameSet(dlg, chat), `线程内模型对话框 = 中央目录（${dlg.length} 个）`);
	await shot(p, "real-R1-thread");
}

async function realR2() {
	const p = await realPage();
	await goto(p, REAL_UI, "/");
	const before = await capsuleModels(p);
	check(
		!before.includes("m13fake/m13-model-b"),
		"改之前列表里没有 m13-model-b",
	);
	await rjson(
		"PUT",
		"/api/v1/agent-config/files/models.yml",
		modelsYml(["m13-model-a", "m13-model-b"]),
	);
	await popover(p).getByRole("button", { name: "刷新" }).click();
	await waitFor(
		"刷新后出现 m13-model-b",
		async () =>
			(await popover(p)
				.locator('[data-model="m13fake/m13-model-b"]')
				.count()) === 1,
		60_000,
		500,
	);
	expect(
		true,
		"改 models.yml 后点「刷新」，列表出现新模型 m13fake/m13-model-b",
	);
	await shot(p, "real-R2-refresh");
	// 不点刷新：新打开的页面也反映（服务端按 models.yml / config.yml 的缓存键重跑）
	await rjson(
		"PUT",
		"/api/v1/agent-config/files/models.yml",
		modelsYml(["m13-model-a", "m13-model-c"]),
	);
	const p2 = await realPage();
	await goto(p2, REAL_UI, "/");
	const after = await capsuleModels(p2);
	expect(
		after.includes("m13fake/m13-model-c") &&
			!after.includes("m13fake/m13-model-b"),
		"再改 models.yml 后重新打开模型列表即反映新配置（无需刷新）",
	);
	const cat = await rjson<{ fetchedAt: number; stale: boolean }>(
		"GET",
		"/api/v1/models/catalog",
	);
	note(
		`目录 fetchedAt=${new Date(cat.fetchedAt).toISOString()} stale=${cat.stale}；24 小时 TTL 由 go test 覆盖`,
	);
	await rjson(
		"PUT",
		"/api/v1/agent-config/files/models.yml",
		modelsYml(["m13-model-a"]),
	);
}

async function realR6() {
	const p = await realPage({ height: 1400 });
	await goto(p, REAL_UI, "/settings/agent-roles");
	await p.locator('[data-testid="roles-cycle"]').waitFor({ timeout: 30_000 });
	for (const [role, label, glyph] of [
		["smol", "快速", "🐇"],
		["m13extra", "m13extra", "🧩"],
		["plan", "架构", "🗺"],
	] as const) {
		const row = p.locator(`li[data-role="${role}"]`);
		await p.getByRole("button", { name: `设置 ${label}`, exact: true }).click();
		await setGlyph(row, glyph);
		await waitFor(
			`${role} 头像`,
			async () =>
				(
					await row
						.locator('[data-avatar="icon"]')
						.first()
						.innerText()
						.catch(() => "")
				).includes(glyph),
			10_000,
		).then(
			() =>
				expect(
					true,
					`${role}（${role === "smol" ? "内置、不在 cycleOrder" : role === "m13extra" ? "只在 modelRoles 里" : "cycleOrder 里"}）设头像 ${glyph} 成功`,
				),
			(e: unknown) =>
				expect(
					false,
					`${role} 设头像：${e instanceof Error ? e.message : String(e)}`,
				),
		);
		await p.getByRole("button", { name: `设置 ${label}`, exact: true }).click();
	}
	const ids = (
		await rjson<{ roles: { role: string; avatarIcon?: string }[] }>(
			"GET",
			"/api/v1/settings/role-identities",
		)
	).roles;
	expect(
		["smol", "m13extra", "plan"].every((r) =>
			ids.some((x) => x.role === r && x.avatarIcon),
		),
		"服务端 role-identities 存下三个头像",
	);
	await shot(p, "real-R6-roles");
}

async function realR7() {
	await rjson(
		"PUT",
		"/api/v1/agent-config/files/agents%2Fm13-real.md",
		"---\nname: m13-real\ndescription: M13 真机自定义 agent\n---\nDo nothing.\n",
	);
	const p = await realPage({ height: 1400 });
	await goto(p, REAL_UI, "/settings/agent-subagents");
	await p.locator("li[data-agent]").first().waitFor({ timeout: 30_000 });
	for (const a of BUILTIN_AGENTS) {
		const g = (
			await p
				.locator(`li[data-agent="${a}"] [data-avatar-of="agent"]`)
				.first()
				.innerText()
				.catch(() => "")
		).trim();
		expect(g === AGENT_DEFAULT_GLYPHS[a], `内置 agent ${a} 列出且显示默认字形`);
	}
	const custom = p.locator('li[data-agent="m13-real"]');
	expect(
		(
			await custom.locator('[data-avatar-of="agent"]').first().innerText()
		).trim() === GENERIC_AGENT_GLYPH,
		"omp-home agents/m13-real.md 列出，显示通用字形",
	);
	for (const [name, glyph] of [
		["scout", "🔭"],
		["m13-real", "🧪"],
	] as const) {
		const row = p.locator(`li[data-agent="${name}"]`);
		await row
			.getByRole("button", { name: `设置 ${name} 的模型与头像` })
			.click();
		await setGlyph(row, glyph);
		await waitFor(
			`${name} 头像`,
			async () =>
				(
					await row
						.locator('[data-avatar="icon"]')
						.first()
						.innerText()
						.catch(() => "")
				).includes(glyph),
			10_000,
		);
		expect(true, `${name} 设头像 ${glyph}`);
	}
	await shot(p, "real-R7-subagents");
}

async function realR9() {
	const list = (
		await rjson<{ agents: { name: string; avatarIcon?: string }[] }>(
			"GET",
			"/api/v1/settings/agent-avatars",
		)
	).agents;
	if (!list.some((a) => a.name === "scout" && a.avatarIcon))
		await rjson("PUT", "/api/v1/settings/agent-avatars", {
			name: "scout",
			avatarIcon: "🔭",
		});
	await rjson(
		"PUT",
		"/api/v1/agent-config/files/agents%2Fm13-real.md",
		"---\nname: m13-real\ndescription: M13 真机自定义 agent\n---\nDo nothing.\n",
	);
	if (!list.some((a) => a.name === "m13-real" && a.avatarIcon))
		await rjson("PUT", "/api/v1/settings/agent-avatars", {
			name: "m13-real",
			avatarIcon: "🧪",
		});
	writeFileSync(`${TMP}/task-mode`, "1");
	let th: ThreadView;
	try {
		th = await newWorkThread("m13 R9：派两个子 agent");
	} finally {
		rmSync(`${TMP}/task-mode`, { force: true });
	}
	const share = await rjson<{ slug: string; url: string }>(
		"POST",
		`/api/v1/threads/${th.id}/shares`,
		{ mode: "snapshot", acknowledgeFindings: true },
	);
	const idRes = await fetch(
		`${REAL_API}/api/v1/public/shares/${share.slug}/identity`,
	);
	const ident = (await idRes.json()) as {
		agents?: { name: string; avatarIcon?: string }[];
	};
	note(`公开身份 agents=${JSON.stringify(ident.agents ?? [])}`);
	expect(
		ident.agents?.length === 1 &&
			ident.agents[0]?.name === "scout" &&
			ident.agents[0]?.avatarIcon === "🔭",
		"分享里用到的 scout（有头像）下发，reviewer（无头像）不下发",
	);
	expect(
		!(ident.agents ?? []).some((a) => a.name === "m13-real"),
		"没用到的 m13-real（有头像）不出网",
	);
	const b = await getBrowser();
	const anon = await b.newContext({
		locale: "zh-CN",
		viewport: { width: 1280, height: 1200 },
	});
	contexts.push(anon);
	const p = await anon.newPage();
	await p.goto(`${REAL_UI}/s/${share.slug}`, { waitUntil: "domcontentloaded" });
	// 混合 agent 的批次：头像在展开的卡片正文里逐行显示
	const head = p.locator(".tv-card .tv-head").first();
	await head.waitFor({ timeout: 30_000 });
	if ((await head.getAttribute("aria-expanded")) !== "true") await head.click();
	const agent = p.locator(".tv-agent").first();
	await agent.waitFor({ timeout: 10_000 });
	await agent.scrollIntoViewIfNeeded();
	const texts = await p.locator(".tv-agent").allInnerTexts();
	note(`分享页 task 卡片 agent：${texts.map((t) => oneLine(t)).join(" | ")}`);
	expect(
		texts.some((t) => t.includes("🔭")),
		"分享页 task 卡片里 scout 显示自定义头像 🔭",
	);
	await shot(p, "real-R9-share");
}

async function realR16() {
	const p = await realPage({ height: 1200 });
	await goto(p, REAL_UI, "/");
	await capsule(p).waitFor({ timeout: 30_000 });
	await pickTarget(p, "全局助手");
	await p.getByTestId("attach-file-input").setInputFiles({
		name: "m13-r16.txt",
		mimeType: "text/plain",
		buffer: Buffer.from("m13 r16 real\n"),
	});
	await p.locator('li[title="m13-r16.txt"]').waitFor({ timeout: 15_000 });
	await p
		.getByRole("textbox", { name: "提示词" })
		// fakeomp 的默认回合在提示词含「ask」时会弹选择卡（附件块里的 "unless asked" 就会触发），带一行 probe 让它走探测回合
		.fill("m13 R16 真机：问问全局助手\nprobe args");
	await p.getByRole("button", { name: "发送" }).click();
	await p.waitForURL(/\/assistant\/T-/, { timeout: 30_000 });
	const aid = new URL(p.url()).pathname.split("/").pop() ?? "";
	const firstUser = await waitFor(
		"首条用户消息",
		async () =>
			(await entries(aid)).find(
				(e) => e.type === "message" && msgRole(e) === "user",
			),
		60_000,
		1_000,
	);
	const ft = msgText(firstUser);
	note(`助手对话首条消息：${clip(oneLine(ft), 200)}`);
	expect(
		ft.includes("m13 R16 真机：问问全局助手") && ft.includes("m13-r16.txt"),
		"选全局助手发送：新助手对话首条消息带提示词与附件（文件库路径）",
	);
	expect(
		(await libFiles(aid)).some(
			(f) => f.name === "m13-r16.txt" && f.threadId === aid,
		),
		"附件进文件库并绑定到新对话",
	);
	await shot(p, "real-R16-assistant");
	// 普通 role（plan，内置、设过头像时用头像，否则默认字形）
	const r = await realPage({ height: 1200 });
	await goto(r, REAL_UI, "/");
	await pickTarget(r, "架构");
	await r
		.getByRole("textbox", { name: "提示词" })
		.fill("m13 R16 真机：交给 plan");
	await r.getByRole("button", { name: "发送" }).click();
	await r.waitForURL(/\/t\/T-/, { timeout: 60_000 });
	const tid = new URL(r.url()).pathname.split("/").pop() ?? "";
	await waitAssistant(tid, 1, 180_000);
	const src = (await entries(tid)).filter(
		(e) =>
			e.type === "custom" &&
			JSON.stringify(e.payload).includes("oryn-model-source"),
	);
	note(
		`来源记录 ${src.map((e) => JSON.stringify((e.payload as { data?: unknown }).data)).join(" ")}`,
	);
	expect(
		src.some((e) => JSON.stringify(e.payload).includes('"role":"plan"')),
		"新线程先切到 plan（oryn_select_model 的来源记录 role=plan）",
	);
	await r
		.locator("[data-testid=transcript] [data-turn-identity]")
		.first()
		.waitFor({ timeout: 30_000 });
	const hs = await heads(r);
	note(`回合头 ${hs.map(headSig).join(" ")}`);
	expect(
		hs.length > 0 && hs[0]?.identity === "role" && hs[0]?.role === "plan",
		"转录回合头显示该 role（plan）",
	);
	await shot(r, "real-R16-role-thread");
}

async function realR18() {
	const p = await realPage();
	await goto(p, REAL_UI, "/settings/identity");
	const card = p.getByTestId("identity-new-thread");
	await card.waitFor({ timeout: 30_000 });
	await card.getByRole("radio", { name: /全局助手/ }).dispatchEvent("click");
	await waitFor(
		"服务端存下 assistant",
		async () =>
			(
				await rjson<{ defaultTarget: string }>(
					"GET",
					"/api/v1/settings/composer",
				)
			).defaultTarget === "assistant",
		5_000,
	);
	expect(
		true,
		"「新对话默认对象」存在服务端（GET settings/composer = assistant）",
	);
	// 另一台「设备」：全新浏览器上下文
	const other = await realPage({ width: 390, height: 844 });
	await goto(other, REAL_UI, "/");
	await waitFor(
		"另一设备首屏预选全局助手",
		async () =>
			(await other.locator(".home-stage").getAttribute("data-target")) ===
			"assistant",
		15_000,
	);
	expect(true, "换一个浏览器上下文（另一台设备）首屏也预选全局助手");
	await shot(other, "real-R18-other-device");
	await card
		.getByRole("radio", { name: /Agent（default role）/ })
		.dispatchEvent("click");
	await waitFor(
		"还原为 role",
		async () =>
			(
				await rjson<{ defaultTarget: string }>(
					"GET",
					"/api/v1/settings/composer",
				)
			).defaultTarget === "role",
		5_000,
	);
}

async function realR19() {
	const aid = await ensureAssistant();
	const p = await realPage();
	await goto(p, REAL_UI, `/assistant/${aid}`);
	const box = p.locator('textarea[aria-label="消息"]');
	await box.waitFor({ timeout: 30_000 });
	await p.getByTestId("attach-file-input").setInputFiles({
		name: "m13-r19.txt",
		mimeType: "text/plain",
		buffer: Buffer.from("m13 r19 real\n"),
	});
	await p.locator('li[title="m13-r19.txt"]').waitFor({ timeout: 15_000 });
	await box.fill("m13 R19 真机：附件\nprobe args");
	await box.press("Enter");
	await sleep(1_500);
	await waitFor(
		"助手回合结束",
		async () => (await getThread(aid)).state === "idle",
		60_000,
		1_000,
	);
	const f = await waitFor(
		"文件库里出现 m13-r19.txt",
		async () => (await libFiles(aid)).find((x) => x.name === "m13-r19.txt"),
		30_000,
		1_000,
	);
	expect(
		f.scope === "conversation" && f.source === "upload" && f.threadId === aid,
		"助手对话里上传的附件进文件库，默认属于该对话",
	);
	const lib = await openLibrary(p);
	const row = lib.locator('li[aria-label="m13-r19.txt"]');
	await row.waitFor({ timeout: 10_000 });
	await row.getByRole("button", { name: "设为库内共享" }).click();
	await waitFor(
		"服务端 scope=shared",
		async () =>
			(await libFiles()).find((x) => x.id === f.id)?.scope === "shared",
		10_000,
	);
	expect(true, "面板切换为库内共享（服务端 scope=shared）");
	await shot(p, "real-R19-library");
}

/** 沙箱里的绝对路径（root 视角与协调宿主视角相同：协调宿主只降权，不换根）。 */
async function realR20() {
	const aid = await ensureAssistant();
	const files = await libFiles(aid);
	const conv =
		files.find((x) => x.threadId === aid && x.scope === "conversation") ??
		(await uploadToLibrary(aid, "m13-r20.txt", "m13 r20\n"));
	let shared = files.find((x) => x.scope === "shared");
	if (!shared) {
		shared = await uploadToLibrary(aid, "m13-r20-shared.txt", "m13 shared\n");
		await rjson("PATCH", `/api/v1/assistant/files/${shared.id}`, {
			scope: "shared",
		});
	}
	const home = await coordHome(aid);
	const pConv = `${home}/library/threads/${aid}/${conv.id}/${conv.name}`;
	const pShared = `${home}/library/shared/${shared.id}/${shared.name}`;
	await waitFor(
		"沙箱里有本对话文件与共享文件",
		() => existsSync(pConv) && existsSync(pShared),
		30_000,
		500,
	).then(
		() =>
			expect(
				true,
				`同步进沙箱：${pConv.slice(home.length)}、${pShared.slice(home.length)}`,
			),
		() =>
			expect(
				false,
				`沙箱里缺文件：${existsSync(pConv) ? "" : pConv} ${existsSync(pShared) ? "" : pShared}；~/library 下：${sh(`find ${q(`${home}/library`)} -type f | head -20`).out}`,
			),
	);
	const reply = await askAssistant(
		aid,
		`probe read ${pConv}\nprobe read ${pShared}`,
	);
	note(`助手回复：${oneLine(reply)}`);
	expect(
		(reply.match(/probe:read:ok/g) ?? []).length === 2,
		"助手（沙箱内进程）能读所属对话的文件与共享文件",
	);
	note(
		"PDF / docx 等格式的解析是 omp 自带 read 工具的能力；fakeomp 只核实沙箱里文件可读，格式解析未覆盖",
	);
}

async function uploadToLibrary(aid: string, name: string, text: string) {
	const up = await rjson<{ id: string; name: string; scope: string }>(
		"POST",
		`/api/v1/assistant/files?name=${encodeURIComponent(name)}&threadId=${aid}`,
		text,
	);
	await askAssistant(aid, `m13：附上 ${name}\nprobe args`, {
		orynFiles: [up.id],
	});
	return { ...up, threadId: aid, source: "upload", sha256: "" };
}

async function realR21() {
	const aid = await ensureAssistant();
	const home = await coordHome(aid);
	const out = `${home}/library/out/m13-r21.txt`;
	const reply = await askAssistant(
		aid,
		`probe write ${out}\nhosttool save_file {"path":"~/library/out/m13-r21.txt","name":"m13-r21.txt"}`,
	);
	note(`助手回复：${oneLine(reply)}`);
	expect(reply.includes("probe:write:ok"), "助手在 ~/library/out/ 写文件");
	expect(/host:save_file:ok/.test(reply), "宿主工具 save_file 存进文件库");
	const f = await waitFor(
		"文件库出现 m13-r21.txt",
		async () => (await libFiles()).find((x) => x.name === "m13-r21.txt"),
		10_000,
	);
	expect(f.source === "assistant", `来源为助手保存（source=${f.source}）`);
	const p = await realPage();
	await goto(p, REAL_UI, `/assistant/${aid}`);
	await p.locator('textarea[aria-label="消息"]').waitFor({ timeout: 30_000 });
	const lib = await openLibrary(p);
	const row = lib.locator('li[aria-label="m13-r21.txt"]');
	await row.waitFor({ timeout: 10_000 });
	expect(
		(await row.innerText()).includes("助手保存"),
		"面板里能看到（助手保存）",
	);
	const [dl] = await Promise.all([
		p.waitForEvent("download", { timeout: 15_000 }),
		row.getByRole("button", { name: "下载" }).click(),
	]);
	const path = await dl.path();
	expect(
		readFileSync(path, "utf8") === "probe\n",
		"可以下载，内容与助手写的一致",
	);
	await shot(p, "real-R21-library");
}

async function realR22() {
	const aid = await ensureAssistant();
	const f =
		(await libFiles(aid))[0] ??
		(await uploadToLibrary(aid, "m13-r22.txt", "m13 r22\n"));
	const reply = await askAssistant(
		aid,
		`hosttool create_thread {"prompt":"m13 R22：读一下附件","files":["${f.id}"],"title":"m13 R22"}`,
	);
	note(`助手回复：${clip(oneLine(reply), 300)}`);
	expect(/host:create_thread:ok/.test(reply), "create_thread 带 files 成功");
	const tid =
		/T-[0-9a-z-]+/i.exec(
			reply.slice(reply.indexOf("host:create_thread")),
		)?.[0] ?? "";
	check(!!tid, `新线程 ${tid}`);
	const first = await waitFor(
		"新线程首条用户消息",
		async () =>
			(await entries(tid)).find(
				(e) => e.type === "message" && msgRole(e) === "user",
			),
		180_000,
		1_000,
	);
	const raw = JSON.stringify(first.payload);
	expect(raw.includes(f.name), `新线程首条消息的附件条里有 ${f.name}`);
	const th = await waitFor(
		"新线程有 orb",
		async () => (await getThread(tid)).orbId,
		60_000,
		1_000,
	);
	const found = sh(
		`find ${q(orbHome(th))} -type f -name ${q(f.name)} 2>/dev/null | head -3`,
	).stdout;
	expect(
		!!found,
		`orb 里能读到附件（${found.split("\n")[0]?.slice(orbHome(th).length) ?? "未找到"}）`,
	);
	const p = await realPage({ height: 1200 });
	await goto(p, REAL_UI, `/t/${tid}`);
	await p.locator("[data-testid=transcript]").waitFor({ timeout: 30_000 });
	await sleep(1500);
	await shot(p, "real-R22-thread");
}

async function realR23() {
	const th = await ensureWorkThread();
	const home = orbHome(th.orbId ?? "");
	const file = `${home}/m13-r23.txt`;
	writeFileSync(file, "m13 r23 from orb\n");
	const r = sh(
		`cd ${q(home)} && env -i PATH=/usr/bin:/bin HOME=${q(home)} ORYN_ORB=1 ORYN_THREAD_ID=${th.id} ${q(`${BIN}/oryn`)} library put --json m13-r23.txt`,
	);
	note(`oryn library put：${clip(oneLine(r.out), 300)}`);
	expect(r.code === 0, "orb 里执行 oryn library put 成功");
	const f = await waitFor(
		"文件库出现 m13-r23.txt",
		async () => (await libFiles()).find((x) => x.name === "m13-r23.txt"),
		15_000,
	);
	expect(
		f.scope === "shared" && f.source === "orb" && f.sourceThreadId === th.id,
		`以库内共享进入文件库（scope=${f.scope} source=${f.source} 来源线程=${f.sourceThreadId}）`,
	);
	const aid = await ensureAssistant();
	expect(
		(await libFiles(aid)).some((x) => x.id === f.id),
		"助手对话可见（共享）",
	);
	const home2 = await coordHome(aid);
	await waitFor(
		"同步进协调宿主沙箱",
		() => existsSync(`${home2}/library/shared/${f.id}/m13-r23.txt`),
		20_000,
		500,
	).then(
		() => expect(true, "共享文件同步进助手沙箱 ~/library/shared/"),
		() => expect(false, "共享文件没有同步进助手沙箱"),
	);
}

async function realR25() {
	const aid = await ensureAssistant();
	const home = await coordHome(aid);
	const cfg = `${home}/.omp/agent/config.yml`;
	const managed = existsSync(cfg) ? readFileSync(cfg, "utf8") : "";
	check(
		managed.length > 0,
		`协调宿主沙箱里有下发的 config.yml（${managed.length} 字节）`,
	);
	writeFileSync(cfg, "# m13 R25：被改坏的配置\ntheme: broken\n");
	const r = await rjson<{ thread: ThreadView }>(
		"POST",
		"/api/v1/assistant/conversations",
		{ prompt: "m13 R25：再启动一次" },
	);
	await waitAssistant(r.thread.id, 1, 120_000);
	const now = readFileSync(cfg, "utf8");
	expect(
		now === managed,
		"下一次助手 agent 启动前，被改动的 config.yml 被重写回下发的内容",
	);
	note(
		"系统提示与 overlay 的重写由 go test（internal/threads）覆盖，界面上不可见",
	);
}

async function realR29() {
	const th = await ensureWorkThread();
	const home = orbHome(th.orbId ?? "");
	const p = await realPage();
	await goto(p, REAL_UI, "/settings/agent-skills");
	await p.getByText(SCOPE_NOTE).waitFor({ timeout: 30_000 });
	const d = await newIn(p, "技能");
	await d.getByLabel("名称").fill("m13-live");
	await d.getByLabel("描述").fill("M13 真机下发");
	const t0 = Date.now();
	await d.getByRole("button", { name: "创建" }).click();
	const target = `${home}/.omp/agent/skills/m13-live/SKILL.md`;
	await waitFor(
		"技能写进 ready orb",
		() => existsSync(target),
		10_000,
		200,
	).then(
		() =>
			expect(
				true,
				`保存后 ${Date.now() - t0}ms 写进 ready orb（${target.slice(home.length)}）`,
			),
		() => expect(false, `10 秒内没写进 orb：${target}`),
	);
	const st = await waitFor(
		"下发完成",
		async () => {
			const s = await rjson<{
				push: { pending: boolean; appliedAt?: number; failed: unknown[] };
			}>("GET", "/api/v1/agent-config/apply-status");
			return !s.push.pending && s.push.appliedAt && s.push.appliedAt >= t0
				? s
				: null;
		},
		15_000,
		300,
	);
	expect(st.push.failed.length === 0, "apply-status：下发完成且没有失败的 orb");
	await p
		.getByText("已下发：空闲的会话随即生效")
		.waitFor({ timeout: 10_000 })
		.then(
			() => expect(true, "状态条显示「已下发」"),
			() => expect(false, "状态条没显示「已下发」"),
		);
	await shot(p, "real-R29-pushed");
	const log = existsSync(`${LOGS}/orynd.log`)
		? readFileSync(`${LOGS}/orynd.log`, "utf8")
		: "";
	const reload = log
		.split("\n")
		.filter((l) => l.includes("reload_plugins") && l.includes(th.id));
	note(
		reload.length
			? `orynd 日志：${clip(reload.at(-1) ?? "", 200)}（fakeomp 不实现 reload_plugins；真 omp 收到后的热生效见计划假设 A4）`
			: "orynd 日志里没找到发给该线程的 reload_plugins 记录",
	);
	expect(
		reload.length > 0,
		"控制面在会话空闲时随即向它下发 reload_plugins（技能热生效）",
	);
}

async function realR30() {
	const th = await ensureWorkThread();
	type ApplyThreads = { threads: { threadId: string; reasons: string[] }[] };
	const status = () =>
		rjson<ApplyThreads>("GET", "/api/v1/agent-config/apply-status");
	// 先前的场景（改 models.yml 等）可能已经留下需重启的会话：先重启掉、再让工作线程的 agent 跑起来，从干净状态开始
	const leftover = await status();
	if (leftover.threads.length > 0) {
		note(
			`开始前已有需重启的会话：${leftover.threads.map((t) => `${t.threadId}(${t.reasons.join(",")})`).join(" ")}，先重启`,
		);
		await rjson("POST", "/api/v1/agent-config/restart-sessions");
		await waitFor(
			"需重启列表清空",
			async () => (await status()).threads.length === 0,
			30_000,
			500,
		);
	}
	if (agentPids(th.orbId ?? "").length === 0) {
		const n0 = (await entries(th.id)).filter(
			(e) =>
				e.type === "message" && msgRole(e) === "assistant" && msgText(e).trim(),
		).length;
		cli(`thread send ${th.id} ${q("m13 R30：先把 agent 拉起来")}`);
		await waitAssistant(th.id, n0 + 1, 120_000);
	}
	const p = await realPage();
	await goto(p, REAL_UI, "/settings/agent-extensions");
	await p.getByText(SCOPE_NOTE).waitFor({ timeout: 30_000 });
	await p.getByRole("button", { name: "在「Hook」中新建" }).click();
	let d = dialog(p);
	await d.getByLabel("文件名").fill("bash.ts");
	await d.getByRole("button", { name: "继续" }).click();
	d = dialog(p);
	await d.locator(".cm-content").first().click();
	await p.keyboard.type("export default {};\n");
	await d.getByRole("button", { name: "保存" }).click();
	await d.waitFor({ state: "detached" });
	const st = await waitFor(
		"apply-status 列出 hooks/pre/bash.ts",
		async () => {
			const s = await status();
			return s.threads.some((t) =>
				t.reasons.some((r) => r.includes("hooks/pre/bash.ts")),
			)
				? s
				: null;
		},
		15_000,
		300,
	);
	const title = p.getByText(/^\d+ 个会话需重启后生效$/);
	await title.waitFor({ timeout: 15_000 });
	note(
		`需重启：${st.threads.map((t) => `${t.threadId}(${t.reasons.join(",")})`).join(" ")}`,
	);
	expect(
		st.threads.some(
			(t) =>
				t.threadId === th.id &&
				t.reasons.some((r) => r.includes("hooks/pre/bash.ts")),
		),
		`顶部「${await title.innerText()}」，列出线程 ${th.id}，原因 hooks/pre/bash.ts`,
	);
	await shot(p, "real-R30-banner");
	const pidsBefore = agentPids(th.orbId ?? "");
	note(`重启前 fakeomp 进程 ${pidsBefore.join(",") || "无"}`);
	await p.getByRole("button", { name: "重启这些会话" }).click();
	await waitFor(
		"omp 进程被停掉",
		() => pidsBefore.every((pid) => !existsSync(`/proc/${pid}`)),
		30_000,
		500,
	).then(
		() => expect(pidsBefore.length > 0, "会话空闲时停掉 omp 进程"),
		() => expect(false, "30 秒内 omp 进程没有停"),
	);
	// 只看本线程：别的场景留下的会话若停在回合中（例如 fakeomp 等用户作答），按设计要等它静止才重启，仍会列着
	await waitFor(
		"本线程离开需重启列表",
		async () => !(await status()).threads.some((t) => t.threadId === th.id),
		15_000,
		500,
	).then(
		() => expect(true, "空闲的本线程重启后离开需重启列表"),
		() => expect(false, "本线程重启后仍在需重启列表里"),
	);
	const rest = (await status()).threads;
	note(
		`仍在列表里的会话（回合未结束，等静止后重启）：${rest.map((t) => t.threadId).join(" ") || "无"}`,
	);
	const n0 = (await entries(th.id)).filter(
		(e) =>
			e.type === "message" && msgRole(e) === "assistant" && msgText(e).trim(),
	).length;
	const r = cli(`thread send ${th.id} ${q("m13 R30：重启后再问一次")}`);
	note(`thread send：${clip(oneLine(r.out), 120)}`);
	await waitAssistant(th.id, n0 + 1, 120_000);
	const pidsAfter = agentPids(th.orbId ?? "");
	expect(
		pidsAfter.length > 0 && pidsAfter.every((x) => !pidsBefore.includes(x)),
		`下次发消息时自动拉起新的 omp 进程（${pidsAfter.join(",")}）并回答`,
	);
	await rapi("DELETE", "/api/v1/agent-config/files/hooks%2Fpre%2Fbash.ts");
}

/** process 驱动下某 orb 的 agent（fakeomp）进程：cwd 在 orb 家目录下且命令行是 fakeomp。 */
function agentPids(orbId: string): number[] {
	const home = orbHome(orbId);
	const out: number[] = [];
	for (const pid of readdirSync("/proc")) {
		if (!/^\d+$/.test(pid)) continue;
		try {
			const cmd = readFileSync(`/proc/${pid}/cmdline`, "utf8");
			if (!cmd.includes("fakeomp")) continue;
			if (readFileSync(`/proc/${pid}/environ`, "utf8").includes(`HOME=${home}`))
				out.push(Number(pid));
		} catch {
			// 已退出
		}
	}
	return out;
}

async function realR27() {
	const cfg = (await rapi("GET", "/api/v1/agent-config/files/config.yml")).text;
	note(`真机 config.yml：${clip(oneLine(cfg), 300)}`);
	expect(
		cfg.includes("# m13 验收：这行注释保存后必须保留"),
		"真机上经 GUI 改过 config.yml 后原有注释保留",
	);
}

async function realR31() {
	const aid = await ensureAssistant();
	const home = await coordHome(aid);
	await rjson(
		"PUT",
		"/api/v1/agent-config/files/config.yml",
		`${(await rapi("GET", "/api/v1/agent-config/files/config.yml")).text}# m13-r31-marker\n`,
	);
	await rjson(
		"PUT",
		"/api/v1/agent-config/files/skills%2Fm13-r31%2FSKILL.md",
		"---\nname: m13-r31\ndescription: r31\n---\nx\n",
	);
	await waitFor(
		"config.yml 下发进协调宿主",
		() =>
			existsSync(`${home}/.omp/agent/config.yml`) &&
			readFileSync(`${home}/.omp/agent/config.yml`, "utf8").includes(
				"m13-r31-marker",
			),
		20_000,
		500,
	).then(
		() =>
			expect(
				true,
				"config.yml 也下发给全局助手（协调宿主沙箱里的 config.yml 更新）",
			),
		() => expect(false, "config.yml 没有下发进协调宿主"),
	);
	expect(
		existsSync(`${home}/.omp/agent/models.yml`),
		"models.yml 也在全局助手沙箱里",
	);
	await sleep(2_000);
	expect(
		!existsSync(`${home}/.omp/agent/skills/m13-r31`),
		"技能不下发给全局助手（不作用于全局助手）",
	);
	await rapi(
		"DELETE",
		"/api/v1/agent-config/files/skills%2Fm13-r31%2FSKILL.md",
	);
}

// ================================================================ 场景表

interface Plan {
	mock?: () => Promise<void>;
	real?: () => Promise<void>;
}
const PLAN: Record<string, Plan> = {
	R1: { mock: mockR1, real: realR1 },
	R2: { mock: mockR2, real: realR2 },
	R3: { mock: mockR3 },
	R4: { mock: mockR4 },
	R5: { mock: mockR5 },
	R6: { mock: mockR6, real: realR6 },
	R7: { mock: mockR7, real: realR7 },
	R8: { mock: mockR8 },
	R9: { real: realR9 },
	R10: { mock: mockR10 },
	R11: { mock: mockR11 },
	R12: { mock: mockR12 },
	R13: { mock: mockR13 },
	R14: { mock: mockR14 },
	R15: { mock: mockR15 },
	R16: { mock: mockR16, real: realR16 },
	R17: { mock: mockR17 },
	R18: { mock: mockR18, real: realR18 },
	R19: { mock: mockR19, real: realR19 },
	R20: { real: realR20 },
	R21: { real: realR21 },
	R22: { real: realR22 },
	R23: { real: realR23 },
	R24: { mock: mockR24 },
	R25: { real: realR25 },
	R26: { mock: mockR26 },
	R27: { mock: mockR27, real: realR27 },
	R28: { mock: mockR28 },
	R29: { mock: mockR29, real: realR29 },
	R30: { mock: mockR30, real: realR30 },
	R31: { mock: mockR31, real: realR31 },
};
/** 真机场景的执行顺序（R1 要在建任何线程之前；R27 在 GUI 改过 config.yml 之后）。 */
const REAL_ORDER = [
	"R1",
	"R2",
	"R6",
	"R7",
	"R9",
	"R18",
	"R16",
	"R19",
	"R20",
	"R21",
	"R22",
	"R23",
	"R25",
	"R29",
	"R30",
	"R31",
	"R27",
];

async function runOne(r: string, part: Part, fn: () => Promise<void>) {
	evidence = [];
	softFails = 0;
	const errs0 = pageErrors.length;
	const diag0 = pageDiag.length;
	console.log(`── ${r} [${part}]`);
	const t0 = Date.now();
	let verdict: Verdict = "PASS";
	let reason: string | undefined;
	try {
		await fn();
		if (softFails > 0) verdict = "FAIL";
	} catch (e) {
		verdict = "FAIL";
		reason = e instanceof Error ? oneLine(e.message).slice(0, 400) : String(e);
		note(`错误：${reason}`);
	}
	const errs = pageErrors.slice(errs0);
	if (errs.length) {
		note(`页面未捕获异常 ${errs.length} 条：${clip(errs.join(" ｜ "), 600)}`);
		if (verdict === "PASS") verdict = "FAIL";
	}
	if (verdict === "FAIL") {
		const diag = pageDiag.slice(diag0);
		if (diag.length)
			note(`页面诊断 ${diag.length} 条：${clip(diag.join(" ｜ "), 1500)}`);
		let i = 0;
		for (const c of contexts)
			for (const pg of c.pages()) {
				const f = `${SHOTS}/fail-${part}-${r}-${i++}.png`;
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
	console.log(`${r} [${part}] ${verdict} ${seconds}s`);
	outcomes.push({ r, part, verdict, reason, seconds, evidence });
}

const selected = (r: string) => !ONLY || ONLY.has(r);

async function cleanup() {
	await closeAll();
	await browser?.close().catch(() => {});
	netns?.stop();
	if (mockVite && mockVite.exitCode === null) mockVite.kill("SIGTERM");
	await stopReal();
}
for (const sig of ["SIGINT", "SIGTERM"] as const)
	process.on(sig, () => {
		void cleanup().then(() => process.exit(130));
	});

console.log(`M13 验收 run=${RUN} parts=${PARTS.join(",")} shots=${SHOTS}`);
try {
	if (PARTS.includes("mock")) {
		const todo = RS.filter((r) => selected(r) && PLAN[r]?.mock);
		if (todo.length) {
			console.log("== mock 部分");
			await startMock();
			for (const r of todo)
				await runOne(r, "mock", PLAN[r]?.mock as () => Promise<void>);
		}
	}
	if (PARTS.includes("real")) {
		const todo = REAL_ORDER.filter((r) => selected(r) && PLAN[r]?.real);
		if (todo.length) {
			console.log("== 真机部分");
			try {
				await setupReal();
			} catch (e) {
				realSetupError = e instanceof Error ? oneLine(e.message) : String(e);
				console.log(`真机环境搭建失败：${realSetupError}`);
			}
			for (const r of todo) {
				if (!realReady) {
					outcomes.push({
						r,
						part: "real",
						verdict: "FAIL",
						reason: `真机环境搭建失败：${realSetupError}`,
						seconds: 0,
						evidence: [],
					});
					console.log(`${r} [real] FAIL（真机环境搭建失败）`);
					continue;
				}
				await runOne(r, "real", PLAN[r]?.real as () => Promise<void>);
			}
		}
	}
} finally {
	await cleanup();
}

// ---------------------------------------------------------------- 汇总

console.log("\n== 汇总（每条 R）");
const summary: { r: string; verdict: Verdict; detail: string }[] = [];
for (const r of RS) {
	if (!selected(r)) continue;
	const plan = PLAN[r] ?? {};
	const parts: string[] = [];
	let verdict: Verdict = "PASS";
	for (const part of ["mock", "real"] as const) {
		if (!plan[part]) continue;
		const o = outcomes.find((x) => x.r === r && x.part === part);
		if (!o) {
			parts.push(`${part}=未跑`);
			if (verdict === "PASS") verdict = "SKIP";
			continue;
		}
		parts.push(
			`${part}=${o.verdict}${o.reason ? `（${clip(o.reason, 120)}）` : ""}`,
		);
		if (o.verdict === "FAIL") verdict = "FAIL";
		else if (o.verdict === "SKIP" && verdict === "PASS") verdict = "SKIP";
	}
	summary.push({ r, verdict, detail: parts.join("；") });
	console.log(`${r} ${verdict}  ${parts.join("；")}`);
}
writeFileSync(
	`${SHOTS}/results.json`,
	JSON.stringify({ run: RUN, parts: PARTS, summary, outcomes }, null, 2),
);
console.log(`\n结果：${SHOTS}/results.json`);
process.exit(summary.every((s) => s.verdict === "PASS") ? 0 : 1);
