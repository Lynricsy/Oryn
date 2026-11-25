#!/usr/bin/env bun
/**
 * M29 设计画布端到端验收（docs/plan-m29.md「V 验收脚本与文档」与「验证（端到端）」，K1–K7）：
 * 覆盖静态帧、实时预览与热更（含同项目跨 orb 接管）、源码定位、探针检查、旧版查看与恢复对账、原型预览、样机导出。
 *
 * 用法（web/ 目录）：bun run accept:m29 [-- --only K1,K2,…] [-- --help]
 *
 * 两种运行模式：
 * 1. 自建隔离环境（缺省，未设 M29_URL）：在 /var/tmp/oryn-m29-<run>/ 下起一套全新的 orynd
 *    （public_url = http://localhost:7660，portal_domain = localhost，Chromium 渲染降权到 nobody）与 provider
 *    （优先 docker 驱动 + M29_BASE_IMAGE 叠层镜像，镜像含预装设计引擎与 uid 1000 的 Chromium；缺 docker 或基础镜像时
 *    可由 M29_DRIVER=process 回退 process 驱动），全部结束后自动清理（M29_KEEP=1 保留）。
 * 2. 接入已有本机 orynd（设 M29_URL，如 http://localhost:7600）：读 M29_TOKEN（缺省 .dev/credentials.json）与
 *    M29_SESSION（缺省调 orynd admin create-web-session 现签），要求目标 orynd 已配 portal_domain 与 Chromium 渲染器，
 *    且至少有一个在线的 docker provider。
 *
 * 绝不假通过：环境不满足前置（缺少 portal_domain、缺少渲染器、缺少 orb / Chromium 等）直接报错并给出明确前置提示。
 *
 * 环境变量：M29_URL、M29_TOKEN、M29_SESSION、M29_CONFIG、M29_PROVIDER、M29_DRIVER（auto|docker|process）、
 *   M29_BASE_IMAGE（缺省 oryn-orb:dev）、M29_BIN（缺省 <仓库>/bin）、M29_PORT（缺省 7660）、
 *   M29_SHOTS（缺省 <仓库>/.dev/m29-design）、M29_COORD_DIR（缺省 /var/tmp/oryn-m29-coord）、
 *   M29_KEEP=1、CHROMIUM（缺省 /usr/bin/chromium）。
 */
import {
	chmodSync,
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
import { connect } from "node:net";
import type { Subprocess } from "bun";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Frame,
	type Page,
} from "playwright-core";
import { type BrowserNetns, startBrowserNetns } from "./browser-netns";

// ---------------------------------------------------------------- 用例与参数

interface CaseSpec {
	id: string;
	name: string;
	summary: string;
}

const CASES: readonly CaseSpec[] = [
	{
		id: "K1",
		name: "设计画布静态帧",
		summary:
			"新建设计画布、封存登录/注册两帧（带 <Link to>），查看器里两帧静态渲染可见",
	},
	{
		id: "K2",
		name: "实时预览与热更",
		summary:
			"dev 服务运行中显示「实时」徽标并热更新源码；另验同项目跨 orb 接管切换 live.threadId 与热更新",
	},
	{
		id: "K3",
		name: "源码定位",
		summary:
			"帧内元素带 data-oryn-loc，右面板点击元素后 SelectionChip 显示 src/frames/…:行，且评论锚点带 anchor.loc",
	},
	{
		id: "K4",
		name: "探针检查",
		summary:
			"放置截断按钮，design.check 与 check_artifact 均返回 text-clipped 与 loc，版本历史显示问题徽标",
	},
	{
		id: "K5",
		name: "旧版查看与恢复对账",
		summary:
			"钉 v1 显示静态帧旧内容与旧版静态横幅 → 恢复到 v1 → 下一回合工作副本自动对账为恢复后的源码",
	},
	{
		id: "K6",
		name: "原型预览",
		summary: "进入原型模式，点击帧内 <Link>，原型顶栏标题切换为目标帧名",
	},
	{
		id: "K7",
		name: "样机导出",
		summary:
			"调用 format=mockup-png（不同 frames/scale/shell 组合）与 mockup-pdf，校验 PNG 头部与实际宽高和选项一致",
	},
];

const KS = CASES.map((c) => c.id);

const USAGE = [
	"用法：bun scripts/m29-design-accept.ts [--only K1,K2,…] [--help]",
	"",
	"验收用例（K1–K7）：",
	...CASES.map((c) => `  ${c.id}  ${c.name} — ${c.summary}`),
	"",
	"环境变量：",
	"  M29_URL         接入已有本机 orynd（如 http://localhost:7600）；未设则自建隔离环境",
	"  M29_TOKEN       已有 orynd 的设备令牌（缺省读 .dev/credentials.json）",
	"  M29_SESSION     已有 orynd 的网页会话 Cookie（缺省尝试 orynd admin create-web-session）",
	"  M29_CONFIG      已有 orynd 的配置文件路径（配合 M29_URL 现签会话时用）",
	"  M29_PROVIDER    已有 orynd 上新建 orb 用的 provider 名称（缺省自动选在线 docker provider）",
	"  M29_DRIVER      自建环境的 provider 驱动：auto（缺省）| docker | process",
	"  M29_BASE_IMAGE  docker 驱动的基础 orb 镜像（缺省 oryn-orb:dev，须含预装设计引擎）",
	"  M29_BIN         二进制目录（缺省 <仓库>/bin，须含 orynd/orbd/oryn/fakeomp）",
	"  M29_PORT        自建 orynd 端口（缺省 7660）",
	"  M29_SHOTS       截图与结果目录（缺省 <仓库>/.dev/m29-design）",
	"  M29_KEEP=1      保留自建环境的临时目录 /var/tmp/oryn-m29-<run>",
	"  CHROMIUM        无头浏览器路径（缺省 /usr/bin/chromium）",
].join("\n");

const argv = process.argv.slice(2);
if (argv.includes("--help") || argv.includes("-h")) {
	console.log(USAGE);
	process.exit(0);
}

function argValue(name: string): string | undefined {
	for (let i = 0; i < argv.length; i++) {
		const a = argv[i];
		if (a === name) return argv[i + 1];
		if (a?.startsWith(`${name}=`)) return a.slice(name.length + 1);
	}
	return undefined;
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
if (ONLY && (ONLY.size === 0 || [...ONLY].some((k) => !KS.includes(k)))) {
	console.error(`--only 只接受 K1…K7\n\n${USAGE}`);
	process.exit(2);
}

const ROOT = `${import.meta.dir}/../..`;
const SHOTS = process.env.M29_SHOTS ?? `${ROOT}/.dev/m29-design`;
const CHROMIUM = process.env.CHROMIUM ?? "/usr/bin/chromium";
const BIN = process.env.M29_BIN ?? `${ROOT}/bin`;
const COORD_DIR = process.env.M29_COORD_DIR ?? "/var/tmp/oryn-m29-coord";
const KEEP = process.env.M29_KEEP === "1";
const BASE_IMAGE = process.env.M29_BASE_IMAGE ?? "oryn-orb:dev";
const REAL_PORT = Number(process.env.M29_PORT ?? 7660);
const COORD_EGRESS = 7961;
const RUN = Math.random().toString(36).slice(2, 6);
const TMP = `/var/tmp/oryn-m29-${RUN}`;
const LOGS = `${TMP}/logs`;
const DB = `${TMP}/data/oryn.db`;
const OVERLAY_IMAGE = `oryn-orb:m29accept-${RUN}`;

const EXTERNAL_URL = process.env.M29_URL?.replace(/\/+$/, "");

// ---------------------------------------------------------------- 基础工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();
const clip = (s: string, n = 260) =>
	s.length > n ? `${s.slice(0, n)}…（共 ${s.length} 字）` : s;

function sh(
	cmd: string,
	opts?: { env?: Record<string, string | undefined>; stdin?: string },
): { code: number; out: string; stdout: string; stderr: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], {
		cwd: ROOT,
		env: opts?.env,
		stdin:
			opts?.stdin !== undefined
				? new TextEncoder().encode(opts.stdin)
				: undefined,
	});
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
		stdout: r.stdout.toString().trim(),
		stderr: r.stderr.toString().trim(),
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

/** 解析 PNG IHDR 宽高并校验 8 字节签名与 IHDR 块头。 */
function pngInfo(b: Uint8Array): { w: number; h: number } | null {
	const sig = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];
	if (b.length < 24) return null;
	for (let i = 0; i < sig.length; i++) if (b[i] !== sig[i]) return null;
	// IHDR 块类型（偏移 12..15）
	if (b[12] !== 0x49 || b[13] !== 0x48 || b[14] !== 0x44 || b[15] !== 0x52)
		return null;
	const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
	return { w: v.getUint32(16), h: v.getUint32(20) };
}

/** 线程短码 = 线程 id（T-<uuidv7>）末 12 位小写十六进制（与 internal/portalhost.Code 一致）。 */
function threadShortCode(threadId: string): string {
	const hex = threadId.replace(/^T-/, "").replaceAll("-", "").toLowerCase();
	return hex.slice(-12);
}

/** 设计画布 dev 服务的受管服务名 `dsn-<sha256(id) 前 10 位 hex>`（与 C3 一致）。 */
function designServiceName(artifactId: string): string {
	const h = new Bun.CryptoHasher("sha256");
	h.update(artifactId);
	return `dsn-${h.digest("hex").slice(0, 10)}`;
}

// ---------------------------------------------------------------- 结果记录

type Verdict = "PASS" | "FAIL";
interface Outcome {
	k: string;
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
/** 硬断言：不成立立即中止本场景。 */
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};
/** 软断言：记下失败继续检查，场景结尾判 FAIL。 */
const expect = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) softFails++;
};

// ---------------------------------------------------------------- 后台进程管理

interface Bg {
	name: string;
	proc: Subprocess;
}
const bgs: Bg[] = [];

function bg(
	name: string,
	cmd: string[],
	env: Record<string, string | undefined> = process.env,
	cwd = ROOT,
): Subprocess {
	mkdirSync(LOGS, { recursive: true });
	const log = openSync(`${LOGS}/${name}.log`, "a");
	const proc = Bun.spawn(cmd, {
		cwd,
		env,
		stdout: log,
		stderr: log,
	});
	bgs.push({ name, proc });
	return proc;
}

async function stopBg(b: Bg) {
	if (b.proc.exitCode === null && b.proc.signalCode === null) {
		b.proc.kill("SIGTERM");
		const r = await Promise.race([
			b.proc.exited,
			sleep(8_000).then(() => "timeout"),
		]);
		if (r === "timeout") b.proc.kill("SIGKILL");
	}
}

// ---------------------------------------------------------------- 环境状态

type OrbDriver = "docker" | "process";
interface Auth {
	token: string;
	session: string;
}
interface EnvState {
	/** 浏览器访问的主站地址（http://localhost:<port>）。 */
	uiBase: string;
	/** 脚本调 REST API 的地址（http://127.0.0.1:<port> 或 M29_URL）。 */
	apiBase: string;
	auth: Auth;
	driver: OrbDriver;
	provider: string;
	isolated: boolean;
	imageBuilt: boolean;
	framesMode: string;
	renderOk: boolean;
	renderReason: string;
}

let envState: EnvState | undefined;
function requireEnv(): EnvState {
	if (!envState) throw new Error("验收环境未就绪");
	return envState;
}

// ---------------------------------------------------------------- HTTP / API

async function api(
	method: string,
	path: string,
	body?: unknown,
	opts?: { headers?: Record<string, string>; raw?: boolean },
): Promise<{
	status: number;
	headers: Headers;
	text: string;
	bytes: Uint8Array<ArrayBuffer>;
}> {
	const { apiBase, auth } = requireEnv();
	const headers: Record<string, string> = {
		Authorization: `Bearer ${auth.token}`,
		"X-Oryn-CSRF": "1",
		...(body !== undefined ? { "Content-Type": "application/json" } : {}),
		...(opts?.headers ?? {}),
	};
	const res = await fetch(`${apiBase}${path}`, {
		method,
		headers,
		body: body !== undefined ? JSON.stringify(body) : undefined,
	});
	const buf = await res.arrayBuffer();
	const bytes = new Uint8Array(buf);
	const text = opts?.raw ? "" : new TextDecoder().decode(bytes);
	return { status: res.status, headers: res.headers, text, bytes };
}

async function rjson<T>(
	method: string,
	path: string,
	body?: unknown,
): Promise<T> {
	const r = await api(method, path, body);
	if (r.status < 200 || r.status >= 300)
		throw new Error(
			`${method} ${path} → HTTP ${r.status}：${clip(r.text, 240)}`,
		);
	return JSON.parse(r.text) as T;
}

// ---------------------------------------------------------------- 浏览器

let browser: Browser | undefined;
let netns: BrowserNetns | undefined;
const contexts: BrowserContext[] = [];
const pageErrors: string[] = [];
const pageDiag: string[] = [];

const BROWSER_MARK = `--oryn-m29-run=${RUN}`;
const CLOSE_TIMEOUT = 30_000;

async function within(p: Promise<unknown>, ms: number): Promise<boolean> {
	const timeout = Promise.withResolvers<boolean>();
	const timer = setTimeout(() => timeout.resolve(false), ms);
	const done = await Promise.race([
		p.then(
			() => true,
			() => true,
		),
		timeout.promise,
	]);
	clearTimeout(timer);
	return done;
}

function killBrowser() {
	for (const pid of readdirSync("/proc")) {
		if (!/^\d+$/.test(pid)) continue;
		try {
			const argvProc = readFileSync(`/proc/${pid}/cmdline`, "utf8").split("\0");
			if (argvProc.includes(BROWSER_MARK)) process.kill(Number(pid), "SIGKILL");
		} catch {
			// 已退出
		}
	}
	browser = undefined;
}

async function getBrowser(): Promise<Browser> {
	if (browser) return browser;
	const { uiBase } = requireEnv();
	const u = new URL(uiBase);
	const port = Number(u.port || (u.protocol === "https:" ? 443 : 80));
	let executablePath = CHROMIUM;
	if (process.getuid?.() === 0) {
		netns ??= await startBrowserNetns({
			name: `oryn-m29-${RUN}`,
			ports: [port],
			chromium: CHROMIUM,
		});
		executablePath = netns.executablePath;
	}
	browser = await chromium.launch({
		executablePath,
		headless: true,
		args: [
			"--no-sandbox",
			"--use-gl=angle",
			"--use-angle=swiftshader",
			BROWSER_MARK,
		],
	});
	return browser;
}

async function openPage(opts?: {
	previewPref?: "live" | "static";
	width?: number;
	height?: number;
}): Promise<Page> {
	const { uiBase, auth } = requireEnv();
	const b = await getBrowser();
	const ctx = await b.newContext({
		locale: "zh-CN",
		viewport: { width: opts?.width ?? 1440, height: opts?.height ?? 900 },
		colorScheme: "light",
		acceptDownloads: true,
	});
	const host = new URL(uiBase).hostname;
	await ctx.addCookies([
		{
			name: "__Host-oryn_session",
			value: auth.session,
			domain: host,
			path: "/",
			secure: true,
			httpOnly: true,
			sameSite: "Lax",
		},
	]);
	const pref = opts?.previewPref;
	await ctx.addInitScript(`(() => {
		if (window.top !== window) return;
		try {
			localStorage.setItem("oryn.debug", "1");
			localStorage.setItem("oryn.panel.width", "760");
			${pref ? `localStorage.setItem("oryn.design.preview", ${JSON.stringify(pref)});` : `localStorage.removeItem("oryn.design.preview");`}
		} catch {}
	})();`);
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
		pageDiag.push(
			`请求失败 ${r.method()} ${r.url().slice(0, 200)}：${r.failure()?.errorText ?? "?"}`,
		),
	);
	return p;
}

async function closeAll() {
	for (const c of contexts.splice(0)) {
		if (await within(c.close(), CLOSE_TIMEOUT)) continue;
		note(
			`关闭浏览器 context 超过 ${CLOSE_TIMEOUT / 1000}s，强制结束浏览器，下一条重新启动`,
		);
		killBrowser();
		return;
	}
}

async function goto(p: Page, path: string) {
	const { uiBase } = requireEnv();
	await p.goto(`${uiBase}${path}`, { waitUntil: "domcontentloaded" });
}

async function shot(p: Page, name: string) {
	mkdirSync(SHOTS, { recursive: true });
	await p.screenshot({ path: `${SHOTS}/${name}.png` });
}

/** 切到线程右面板的指定标签（同 m14-artifacts-accept.ts 的 openPanel）。 */
async function openPanel(p: Page, tab: string) {
	const views = p.locator("[data-testid=thread-view-menu]").first();
	if (await views.isVisible().catch(() => false)) {
		await views.click();
		const item = p.locator(`[data-view=${tab}]`);
		await item.waitFor({ timeout: 10_000 });
		if ((await item.getAttribute("aria-checked")) === "true")
			await p.keyboard.press("Escape");
		else await item.click();
		await sleep(400);
		return;
	}
	const btn = p.locator(`[data-panel-tab=${tab}]`);
	if (!(await btn.isVisible().catch(() => false)))
		await p.getByRole("button", { name: "信息面板", exact: true }).click();
	await btn.waitFor({ timeout: 15_000 });
	if ((await btn.getAttribute("aria-selected")) !== "true") await btn.click();
	await sleep(400);
}

/** 在页面内按 iframe 选择器找包含指定文字（或选择器）的子帧。 */
async function waitCanvasFrame(
	p: Page,
	opts: {
		iframeSelector: string;
		text?: string;
		selector?: string;
		urlIncludes?: string;
		timeoutMs?: number;
	},
): Promise<{ frame: Frame; src: string }> {
	return waitFor(
		`画布帧就绪（${opts.iframeSelector}${opts.text ? ` 含「${opts.text}」` : ""}）`,
		async () => {
			const handles = await p.locator(opts.iframeSelector).elementHandles();
			for (const h of handles) {
				const src = (await h.getAttribute("src")) ?? "";
				if (opts.urlIncludes && !src.includes(opts.urlIncludes)) continue;
				const f = await h.contentFrame();
				if (!f) continue;
				const ready = await f
					.evaluate(() => document.readyState === "complete")
					.catch(() => false);
				if (!ready) continue;
				if (opts.selector) {
					const vis = await f
						.locator(opts.selector)
						.first()
						.isVisible()
						.catch(() => false);
					if (!vis) continue;
				}
				if (opts.text) {
					const body = await f
						.evaluate(() => document.body?.textContent ?? "")
						.catch(() => "");
					if (!body.includes(opts.text)) continue;
				}
				return { frame: f, src };
			}
			return null;
		},
		opts.timeoutMs ?? 45_000,
		300,
	);
}

// ---------------------------------------------------------------- 环境搭建

async function setupEnv() {
	mkdirSync(SHOTS, { recursive: true });
	if (!existsSync(CHROMIUM))
		throw new Error(
			`找不到 Chromium：${CHROMIUM}（设 CHROMIUM=/path/to/chromium）`,
		);

	if (EXTERNAL_URL) {
		await setupExternal(EXTERNAL_URL);
		return;
	}
	await setupIsolated();
}

async function setupExternal(baseUrl: string) {
	const health = await fetch(`${baseUrl}/healthz`).catch(() => null);
	if (!health?.ok)
		throw new Error(
			`无法连接 M29_URL=${baseUrl}/healthz：请先启动配置了 portal_domain 的本机 orynd`,
		);

	const credPath = `${ROOT}/.dev/credentials.json`;
	let token = process.env.M29_TOKEN ?? "";
	if (!token && existsSync(credPath)) {
		const parsed: unknown = JSON.parse(readFileSync(credPath, "utf8"));
		if (
			parsed !== null &&
			typeof parsed === "object" &&
			"token" in parsed &&
			typeof parsed.token === "string"
		)
			token = parsed.token;
	}
	if (!token)
		throw new Error(
			`缺少设备令牌：设 M29_TOKEN 或确保 ${credPath} 存在（oryn login 生成）`,
		);

	let session = process.env.M29_SESSION ?? "";
	if (!session) {
		const cfg = process.env.M29_CONFIG ?? `${ROOT}/.dev/orynd.yaml`;
		if (!existsSync(`${BIN}/orynd`) || !existsSync(cfg))
			throw new Error(
				`缺少网页会话 Cookie：设 M29_SESSION，或确保 ${BIN}/orynd 与 ${cfg} 存在以现签会话`,
			);
		const ses = sh(
			`${q(`${BIN}/orynd`)} --config ${q(cfg)} admin create-web-session --user-agent m29-accept`,
		);
		if (ses.code !== 0)
			throw new Error(`orynd admin create-web-session 失败：${ses.out}`);
		session = ses.stdout.split("\n").at(-1)?.trim() ?? "";
	}

	envState = {
		uiBase: baseUrl,
		apiBase: baseUrl,
		auth: { token, session },
		driver: "docker",
		provider: "",
		isolated: false,
		imageBuilt: false,
		framesMode: "",
		renderOk: false,
		renderReason: "",
	};

	const provs = (
		await rjson<{
			providers: { name: string; online: boolean; driver?: string }[];
		}>("GET", "/api/v1/providers")
	).providers;
	const wantProv = process.env.M29_PROVIDER;
	const chosen = wantProv
		? provs.find((p) => p.name === wantProv && p.online)
		: (provs.find((p) => p.online && p.driver === "docker") ??
			provs.find((p) => p.online));
	if (!chosen)
		throw new Error(
			`目标 orynd（${baseUrl}）没有在线的 provider${wantProv ? ` ${wantProv}` : ""}；请先启动 provider（含预装设计引擎的 orb 镜像）`,
		);
	envState.provider = chosen.name;
	envState.driver = chosen.driver === "process" ? "process" : "docker";

	await checkCapabilities();
}

async function setupIsolated() {
	if (process.getuid?.() !== 0)
		throw new Error(
			"自建隔离环境需要 root 权限（用于浏览器网络命名空间与 Chromium 渲染降权到 nobody）；非 root 环境请先起本机 orynd 并设 M29_URL",
		);
	for (const b of ["orynd", "orbd", "oryn", "fakeomp"])
		if (!existsSync(`${BIN}/${b}`))
			throw new Error(
				`缺少 ${BIN}/${b}：请先在仓库根执行 make web && make build（或设 M29_BIN）`,
			);
	for (const tool of ["git", "curl", "sqlite3"])
		if (sh(`command -v ${tool}`).code !== 0)
			throw new Error(`本机缺少必需命令：${tool}`);
	if (sh(`ss -Hltn 'sport = :${REAL_PORT}'`).stdout !== "")
		throw new Error(`127.0.0.1:${REAL_PORT} 已被占用（设 M29_PORT 换端口）`);

	const wantDriver = (process.env.M29_DRIVER ?? "auto").toLowerCase();
	if (!["auto", "docker", "process"].includes(wantDriver))
		throw new Error("M29_DRIVER 只接受 auto、docker 或 process");

	let driver: OrbDriver;
	if (wantDriver === "docker") {
		if (sh("docker info >/dev/null 2>&1").code !== 0)
			throw new Error("M29_DRIVER=docker 但本机 docker 不可用");
		if (sh(`docker image inspect ${q(BASE_IMAGE)} >/dev/null 2>&1`).code !== 0)
			throw new Error(
				`缺少 orb 基础镜像 ${BASE_IMAGE}：请先执行 make image（或设 M29_BASE_IMAGE 指向含设计引擎的 orb 镜像）`,
			);
		driver = "docker";
	} else if (wantDriver === "process") {
		driver = "process";
	} else {
		const hasDocker =
			sh("docker info >/dev/null 2>&1").code === 0 &&
			sh(`docker image inspect ${q(BASE_IMAGE)} >/dev/null 2>&1`).code === 0;
		driver = hasDocker ? "docker" : "process";
	}

	mkdirSync(`${TMP}/bin`, { recursive: true });
	mkdirSync(`${TMP}/render`, { recursive: true });
	mkdirSync(`${TMP}/data`, { recursive: true });
	mkdirSync(LOGS, { recursive: true });
	for (const d of [TMP, `${TMP}/bin`, `${TMP}/render`]) chmodSync(d, 0o755);

	copyFileSync(`${BIN}/fakeomp`, `${TMP}/bin/fakeomp`);
	chmodSync(`${TMP}/bin/fakeomp`, 0o755);
	writeFileSync(
		`${TMP}/bin/agent`,
		`#!/bin/sh\nexec ${TMP}/bin/fakeomp "$@"\n`,
		{ mode: 0o755 },
	);
	if (existsSync(COORD_DIR))
		for (const f of readdirSync(COORD_DIR))
			rmSync(`${COORD_DIR}/${f}`, { recursive: true, force: true });

	let gateway = "";
	let imageBuilt = false;
	if (driver === "docker") {
		gateway = sh(
			"docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}'",
		).stdout;
		if (!/^\d+\.\d+\.\d+\.\d+$/.test(gateway))
			throw new Error(`无法取得 docker0 网关地址：${gateway}`);
		mkdirSync(`${TMP}/image`, { recursive: true });
		copyFileSync(`${BIN}/fakeomp`, `${TMP}/image/omp`);
		copyFileSync(`${BIN}/orbd`, `${TMP}/image/orbd`);
		copyFileSync(`${BIN}/oryn`, `${TMP}/image/oryn`);
		writeFileSync(
			`${TMP}/image/Dockerfile`,
			`FROM ${BASE_IMAGE}\nUSER root\nCOPY --chmod=0755 omp orbd oryn /usr/local/bin/\nUSER user\n`,
		);
		const built = sh(
			`docker build -q -t ${q(OVERLAY_IMAGE)} ${q(`${TMP}/image`)} > ${q(`${LOGS}/image.log`)} 2>&1`,
		);
		rmSync(`${TMP}/image`, { recursive: true, force: true });
		if (built.code !== 0) {
			const tail = existsSync(`${LOGS}/image.log`)
				? readFileSync(`${LOGS}/image.log`, "utf8").slice(-400)
				: built.out;
			throw new Error(
				`构建 orb 叠层镜像 ${OVERLAY_IMAGE}（基于 ${BASE_IMAGE}）失败：${oneLine(tail)}`,
			);
		}
		imageBuilt = true;
		console.log(
			`  orb 镜像：${OVERLAY_IMAGE}（基于 ${BASE_IMAGE}，docker 驱动）`,
		);
	} else {
		console.log(
			"  orb 驱动：process（未检测到 docker / oryn-orb:dev；注意 root 下 orbd 侧 design.check 需非 root 或 docker 镜像）",
		);
	}

	const listenAddrs =
		driver === "docker"
			? `["127.0.0.1:${REAL_PORT}", "${gateway}:${REAL_PORT}"]`
			: `["127.0.0.1:${REAL_PORT}"]`;
	const uiBase = `http://localhost:${REAL_PORT}`;
	const apiBase = `http://127.0.0.1:${REAL_PORT}`;

	writeFileSync(
		`${TMP}/orynd.yaml`,
		`listen: ${listenAddrs}
public_url: "${uiBase}"
portal_domain: "localhost"
data_dir: "${TMP}/data"
log_level: "debug"
idle: { agent_idle: "30m", user_idle: "30m", stop_after_frozen: "24h", wake_on_focus: true }
orb_defaults: { provider: "local", type: "" }
coordinator: { omp_path: "${TMP}/bin/fakeomp", dir: "${COORD_DIR}", egress_port: ${COORD_EGRESS}, verify_omp: false }
artifacts: { render: { chromium: "${CHROMIUM}", user: "nobody", dir: "${TMP}/render" } }
`,
	);

	bg("orynd", [`${BIN}/orynd`, "--config", `${TMP}/orynd.yaml`]);
	await waitFor(
		"orynd 就绪",
		async () => (await fetch(`${apiBase}/healthz`).catch(() => null))?.ok,
		30_000,
	);

	// 校验 orynd 内嵌了前端静态产物（make web && make build）
	const rootPage = await fetch(`${apiBase}/`);
	const rootHtml = await rootPage.text();
	if (!rootPage.ok || !/<div[^>]+id="root"/i.test(rootHtml))
		throw new Error(
			"orynd 未提供内嵌 Web UI（GET / 不是 SPA 入口）：请先在仓库根执行 make web && make build",
		);

	const admin = (a: string) =>
		sh(`${q(`${BIN}/orynd`)} --config ${q(`${TMP}/orynd.yaml`)} admin ${a}`);
	const dev = admin("create-device --name m29-accept --kind cli");
	if (dev.code !== 0) throw new Error(`create-device 失败：${dev.out}`);
	const ses = admin("create-web-session --user-agent m29-accept");
	if (ses.code !== 0) throw new Error(`create-web-session 失败：${ses.out}`);
	const auth: Auth = {
		token: dev.stdout.split("\n").at(-1)?.trim() ?? "",
		session: ses.stdout.split("\n").at(-1)?.trim() ?? "",
	};

	const own = sh(
		`sqlite3 ${q(DB)} ${q(`INSERT OR IGNORE INTO owner (id, display_name, user_handle, created_at) VALUES (1, 'm29', randomblob(16), ${Date.now()})`)}`,
	);
	if (own.code !== 0) throw new Error(`写 owner 行失败：${own.out}`);

	envState = {
		uiBase,
		apiBase,
		auth,
		driver,
		provider: "local",
		isolated: true,
		imageBuilt,
		framesMode: "",
		renderOk: false,
		renderReason: "",
	};

	const prov = await rjson<{ token: string }>("POST", "/api/v1/providers", {
		name: "local",
	});
	writeFileSync(`${TMP}/provider.token`, `${prov.token}\n`, { mode: 0o600 });

	if (driver === "docker") {
		writeFileSync(
			`${TMP}/provider.yaml`,
			`url: "${apiBase}"
token_file: "${TMP}/provider.token"
orb_url: "http://${gateway}:${REAL_PORT}"
driver: docker
docker:
  image: "${OVERLAY_IMAGE}"
`,
		);
		bg("provider", [
			`${BIN}/oryn`,
			"provider",
			"run",
			"--config",
			`${TMP}/provider.yaml`,
		]);
	} else {
		writeFileSync(
			`${TMP}/provider.yaml`,
			`url: "${apiBase}"
token_file: "${TMP}/provider.token"
orb_url: "${apiBase}"
driver: process
`,
		);
		bg(
			"provider",
			[`${BIN}/oryn`, "provider", "run", "--config", `${TMP}/provider.yaml`],
			{
				...process.env,
				ORYN_ORBD_BIN: `${BIN}/orbd`,
				ORYN_AGENT_BIN: `${TMP}/bin/agent`,
				ORYN_PROCESS_ROOT: `${TMP}/orbs`,
				ORYN_EGRESS_ADDR: "127.0.0.1:0",
			},
		);
	}

	await waitFor(
		"provider local 在线",
		async () =>
			(
				await rjson<{ providers: { name: string; online: boolean }[] }>(
					"GET",
					"/api/v1/providers",
				)
			).providers.some((p) => p.name === "local" && p.online),
		60_000,
		500,
	);

	await checkCapabilities();
}

async function checkCapabilities() {
	const st = requireEnv();
	type Caps = {
		frames: string;
		render: boolean;
		renderReason?: string;
		sample: boolean;
	};
	let caps = await rjson<Caps>("GET", "/api/v1/artifacts/capabilities");
	if (!caps.render) {
		caps = await waitFor(
			"控制面渲染自检完成",
			async () => {
				const c = await rjson<Caps>("GET", "/api/v1/artifacts/capabilities");
				caps = c;
				return c.render ? c : null;
			},
			45_000,
			500,
		).catch(() => caps);
	}
	st.framesMode = caps.frames;
	st.renderOk = caps.render;
	st.renderReason = caps.renderReason ?? "";
	console.log(
		`  能力：frames=${caps.frames} render=${caps.render}${caps.renderReason ? `（${caps.renderReason}）` : ""} driver=${st.driver} provider=${st.provider}`,
	);
	if (caps.frames !== "host")
		throw new Error(
			`当前 orynd 未配置 portal_domain（capabilities.frames=${caps.frames}）：设计画布实时预览（K2）与泛域名帧需要配置 portal_domain（例如 localhost）`,
		);
}

// ---------------------------------------------------------------- 线程与 orb 操作

interface Th {
	id: string;
	orbId: string;
	title: string;
}

interface ThreadView {
	id: string;
	state: string;
	orbId?: string;
	title?: string;
}

interface EntryRow {
	seq: number;
	type: string;
	payload: {
		message?: {
			role?: string;
			content?: string | { text?: string }[];
		};
	};
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

function assistantText(e: EntryRow): string {
	const m = e.payload.message;
	if (m?.role !== "assistant") return "";
	if (typeof m.content === "string") return m.content;
	if (!Array.isArray(m.content)) return "";
	return m.content.map((x) => x.text ?? "").join("\n");
}

async function waitAssistant(id: string, n: number, timeoutMs = 180_000) {
	return waitFor(
		`线程 ${id} 第 ${n} 条助手回复`,
		async () => {
			const th = await rjson<ThreadView>("GET", `/api/v1/threads/${id}`);
			if (th.state === "error") throw new Error(`线程 ${id} 进入 error 状态`);
			const count = (await entries(id)).filter(
				(e) => e.type === "message" && assistantText(e).trim() !== "",
			).length;
			return count >= n && th.state === "idle" ? th : null;
		},
		timeoutMs,
		1_000,
	);
}

const orbHome = (orbId: string) => `${TMP}/orbs/orbs/${orbId}/home`;
const spawnedOrbs: string[] = [];

async function orbSockReady(orbId: string): Promise<boolean> {
	const { driver } = requireEnv();
	if (driver === "process")
		return existsSync(`${orbHome(orbId)}/.oryn/orbd.sock`);
	return (
		sh(`docker exec ${q(orbId)} test -S /home/user/.oryn/orbd.sock`).code === 0
	);
}

let projectId = "";
async function ensureProject(): Promise<string> {
	if (projectId) return projectId;
	projectId = (
		await rjson<{ id: string }>("POST", "/api/v1/projects", {
			name: `m29-${RUN}`,
			shallowClone: true,
		})
	).id;
	return projectId;
}

async function newThread(
	prompt: string,
	opts?: { projectId?: string; orbId?: string },
): Promise<Th> {
	const { provider } = requireEnv();
	const r = await rjson<{ thread: { id: string } }>("POST", "/api/v1/threads", {
		...(opts?.projectId ? { projectId: opts.projectId } : {}),
		prompt,
		executor: opts?.orbId
			? { kind: "orb", orbId: opts.orbId }
			: { kind: "new_orb", provider },
	});
	const th = await waitAssistant(r.thread.id, 1);
	if (!th.orbId) throw new Error(`线程 ${th.id} 没有分配 orb`);
	if (!opts?.orbId) spawnedOrbs.push(th.orbId);
	await waitFor(
		`orb ${th.orbId} 的 orbd.sock 就绪`,
		() => orbSockReady(th.orbId ?? ""),
		60_000,
		500,
	);
	return { id: th.id, orbId: th.orbId, title: th.title || th.id };
}

let T1: Th | undefined;
let T2: Th | undefined;

async function ensureT1(): Promise<Th> {
	if (T1) return T1;
	const prj = await ensureProject();
	T1 = await newThread("m29 验收：主设计线程 A", { projectId: prj });
	return T1;
}

/** 与 T1 同项目但位于第二台独立 orb 上的线程 B（K2 跨 orb 接管用）。 */
async function ensureT2(): Promise<Th> {
	if (T2) return T2;
	const prj = await ensureProject();
	T2 = await newThread("m29 验收：同项目跨 orb 线程 B", { projectId: prj });
	return T2;
}

interface SockResponse<T = unknown> {
	type: "result" | "error";
	result?: T;
	code?: string;
	message?: string;
}

/** 经 orbd.sock 调用一个方法（自动适配 process / docker 驱动）。 */
async function orbSockCall<T = unknown>(
	th: Th,
	method: string,
	params: Record<string, unknown>,
	timeoutMs = 180_000,
): Promise<SockResponse<T>> {
	const { driver } = requireEnv();
	const payload = JSON.stringify({ method, params });

	if (driver === "process") {
		const sock = `${orbHome(th.orbId)}/.oryn/orbd.sock`;
		const { promise, resolve, reject } =
			Promise.withResolvers<SockResponse<T>>();
		const c = connect(sock);
		const timer = setTimeout(() => {
			c.destroy();
			reject(new Error(`orbd.sock ${method} 超时（${timeoutMs}ms）`));
		}, timeoutMs);
		let buf = "";
		c.on("data", (d) => {
			buf += d.toString();
			for (let i = buf.indexOf("\n"); i >= 0; i = buf.indexOf("\n")) {
				const line = buf.slice(0, i);
				buf = buf.slice(i + 1);
				if (!line.trim()) continue;
				const m = JSON.parse(line) as SockResponse<T> & { type: string };
				if (m.type === "result" || m.type === "error") {
					clearTimeout(timer);
					c.end();
					resolve(m);
				}
			}
		});
		c.on("error", (err) => {
			clearTimeout(timer);
			reject(err);
		});
		c.write(`${payload}\n`);
		return promise;
	}

	// docker 驱动：容器内自带 node，直接通过 unix socket 对话
	const script = `
const net = require("node:net");
const fs = require("node:fs");
const req = fs.readFileSync(0, "utf8").trim();
const c = net.connect(process.env.HOME + "/.oryn/orbd.sock");
const timer = setTimeout(() => { process.stderr.write("timeout\\n"); process.exit(124); }, ${timeoutMs});
let buf = "";
c.on("data", (d) => {
  buf += d.toString();
  for (let i = buf.indexOf("\\n"); i >= 0; i = buf.indexOf("\\n")) {
    const line = buf.slice(0, i);
    buf = buf.slice(i + 1);
    if (!line.trim()) continue;
    const m = JSON.parse(line);
    if (m.type === "result" || m.type === "error") {
      clearTimeout(timer);
      process.stdout.write(line + "\\n");
      c.end();
      process.exit(0);
    }
  }
});
c.on("error", (e) => { clearTimeout(timer); process.stderr.write(String(e) + "\\n"); process.exit(1); });
c.write(req + "\\n");
`;
	const r = sh(`docker exec -i ${q(th.orbId)} node -e ${q(script)}`, {
		stdin: `${payload}\n`,
	});
	if (r.code !== 0 || !r.stdout)
		throw new Error(`docker orbd.sock ${method} 失败：${clip(r.out, 300)}`);
	return JSON.parse(r.stdout.split("\n").at(-1) ?? "{}") as SockResponse<T>;
}

async function orbSockOk<T = unknown>(
	th: Th,
	method: string,
	params: Record<string, unknown>,
	timeoutMs = 180_000,
): Promise<T> {
	const res = await orbSockCall<T>(th, method, params, timeoutMs);
	if (res.type !== "result")
		throw new Error(
			`orbd.sock ${method} 返回错误 ${res.code ?? "unknown"}：${clip(res.message ?? "", 300)}`,
		);
	return res.result as T;
}

function orbWriteFile(th: Th, absPath: string, content: string) {
	const { driver } = requireEnv();
	if (driver === "process") {
		const dir = absPath.slice(0, absPath.lastIndexOf("/"));
		if (dir) mkdirSync(dir, { recursive: true });
		writeFileSync(absPath, content);
		return;
	}
	const r = sh(
		`docker exec -i ${q(th.orbId)} bash -c ${q(`mkdir -p "$(dirname ${q(absPath)})" && cat > ${q(absPath)}`)}`,
		{ stdin: content },
	);
	if (r.code !== 0)
		throw new Error(`写入 orb 文件 ${absPath} 失败：${clip(r.out, 240)}`);
}

function orbReadFile(th: Th, absPath: string): string {
	const { driver } = requireEnv();
	if (driver === "process") return readFileSync(absPath, "utf8");
	const r = sh(`docker exec ${q(th.orbId)} cat ${q(absPath)}`);
	if (r.code !== 0)
		throw new Error(`读取 orb 文件 ${absPath} 失败：${clip(r.out, 240)}`);
	return r.stdout;
}

interface ArtifactCallResult {
	text: string;
	details?: Record<string, unknown>;
}

/** 以线程身份在 orb 内执行 `oryn artifact call <op>`（走 orbd.sock → ctl artifact.call）。 */
function orbArtifactCall(
	th: Th,
	op: string,
	params: Record<string, unknown>,
): ArtifactCallResult {
	const { driver } = requireEnv();
	const json = JSON.stringify(params);
	if (driver === "process") {
		const f = `${TMP}/call-${Date.now()}-${Math.random().toString(36).slice(2, 6)}.json`;
		writeFileSync(f, json);
		const r = sh(
			`cd ${q(orbHome(th.orbId))} && env -i PATH=/usr/local/bin:/usr/bin:/bin HOME=${q(orbHome(th.orbId))} ORYN_ORB=1 ORYN_THREAD_ID=${q(th.id)} ${q(`${BIN}/oryn`)} artifact call ${q(op)} --params-file ${q(f)} --json`,
		);
		if (r.code !== 0)
			throw new Error(`oryn artifact call ${op} 失败：${clip(r.out, 300)}`);
		return JSON.parse(r.stdout) as ArtifactCallResult;
	}
	const cmd = `f=$(mktemp) && cat > "$f" && ORYN_ORB=1 ORYN_THREAD_ID=${q(th.id)} oryn artifact call ${q(op)} --params-file "$f" --json; rc=$?; rm -f "$f"; exit $rc`;
	const r = sh(`docker exec -i ${q(th.orbId)} bash -lc ${q(cmd)}`, {
		stdin: json,
	});
	if (r.code !== 0)
		throw new Error(`oryn artifact call ${op} 失败：${clip(r.out, 300)}`);
	return JSON.parse(r.stdout) as ArtifactCallResult;
}

// ---------------------------------------------------------------- REST 视图类型

interface DesignOpenResult {
	id: string;
	dir: string;
	version: number;
	devUrl: string;
}

interface DesignSealResult {
	sealed: { id: string; version: number }[];
	skipped: string[];
}

interface DesignCheckIssue {
	rule: string;
	frame?: string;
	loc?: string | null;
	text?: string;
	detail?: unknown;
}

interface DesignCheckResult {
	status: "clean" | "issues" | "unverified";
	issues: DesignCheckIssue[];
	images: { frame: string; data: string }[];
	message?: string;
}

interface DesignStatusResult {
	designs: {
		id: string;
		dir: string;
		dirty: boolean;
		check?: { status: string; issues: DesignCheckIssue[] } | null;
	}[];
}

interface ArtifactDesignView {
	design: boolean;
	live: { threadId: string; port: number } | null;
	direction: Record<string, unknown> | null;
	autoNotes: boolean;
	check?: { status: string; issues: DesignCheckIssue[] };
}

interface CanvasPageView {
	id: string;
	canvas: string;
	name: string;
	file: string;
	source?: string;
	device?: string;
	width: number;
	height: number;
}

interface ArtifactDetailView {
	artifact: { id: string; title: string; kind: string; version: number };
	version: number;
	head: number;
	files: { path: string; size: number; mime: string; sha256: string }[];
	canvas?: {
		format: number;
		design?: { engine: number; start: string | null };
		pages: CanvasPageView[];
	};
}

interface ArtifactVersionView {
	version: number;
	turnId?: string;
	title?: string;
	note?: string;
	check?: { status: string; issues: DesignCheckIssue[] };
}

interface CommentThreadView {
	id: string;
	anchor: {
		version: number;
		path: string;
		loc?: string | null;
		tag?: string | null;
		text: string;
	};
	comments: { n: number; body: string }[];
}

const getDetail = (id: string, version?: number) =>
	rjson<ArtifactDetailView>(
		"GET",
		`/api/v1/artifacts/${id}${version ? `?version=${version}` : ""}`,
	);
const getDesign = (id: string) =>
	rjson<ArtifactDesignView>("GET", `/api/v1/artifacts/${id}/design`);
const getVersions = async (id: string) =>
	(
		await rjson<{ versions: ArtifactVersionView[] }>(
			"GET",
			`/api/v1/artifacts/${id}/versions`,
		)
	).versions;
const getFileText = async (id: string, path: string, version?: number) => {
	const r = await api(
		"GET",
		`/api/v1/artifacts/${id}/file?path=${encodeURIComponent(path)}${version ? `&version=${version}` : ""}`,
	);
	if (r.status !== 200)
		throw new Error(`读取 ${id} ${path} 失败：HTTP ${r.status}`);
	return r.text;
};
const getComments = (id: string) =>
	rjson<{ version: number; threads: CommentThreadView[] }>(
		"GET",
		`/api/v1/artifacts/${id}/comments`,
	);

// ---------------------------------------------------------------- 设计画布样例源码

const THEME_CSS = `@theme {
  --color-bg: #f8fafc;
  --color-surface: #ffffff;
  --color-text: #0f172a;
  --color-muted: #475569;
  --color-accent: #0f766e;
  --color-on-accent: #ffffff;
  --font-display: ui-sans-serif, system-ui, sans-serif;
  --font-sans: ui-sans-serif, system-ui, sans-serif;
  --font-mono: ui-monospace, monospace;
}
`;

function loginFrameTsx(opts?: { heading?: string; clippedButton?: boolean }) {
	const heading = opts?.heading ?? "欢迎回来";
	const extra = opts?.clippedButton
		? `        <button
          type="button"
          style={{ width: "44px", overflow: "hidden", whiteSpace: "nowrap" }}
          className="min-h-11 rounded-lg bg-accent px-2 text-sm font-medium text-on-accent"
        >
          这是一个会被容器截断的超长登录按钮文案
        </button>\n`
		: "";
	return `import { Link } from "oryn:design";

export const frame = {
  name: "登录",
  width: 390,
  height: 844,
  device: "phone",
};

export default function LoginFrame() {
  return (
    <main className="min-h-screen bg-bg p-6 text-text">
      <h1 className="text-2xl font-bold text-text">${heading}</h1>
      <p className="mt-2 text-sm text-muted">使用账号继续访问工作区</p>
      <div className="mt-6 flex flex-col gap-3 rounded-xl bg-surface p-5">
        <button
          type="button"
          className="min-h-11 rounded-lg bg-accent px-4 py-2 text-sm font-medium text-on-accent"
        >
          立即登录
        </button>
${extra}        <Link
          to="signup"
          className="inline-flex min-h-10 items-center justify-center rounded-md px-3 text-sm font-medium text-accent underline"
        >
          前往注册
        </Link>
      </div>
    </main>
  );
}
`;
}

const SIGNUP_FRAME_TSX = `import { Link } from "oryn:design";

export const frame = {
  name: "注册",
  width: 390,
  height: 844,
  device: "phone",
};

export default function SignupFrame() {
  return (
    <main className="min-h-screen bg-bg p-6 text-text">
      <h1 className="text-2xl font-bold text-text">创建新账号</h1>
      <p className="mt-2 text-sm text-muted">填写邮箱完成注册</p>
      <div className="mt-6 flex flex-col gap-3 rounded-xl bg-surface p-5">
        <button
          type="button"
          className="min-h-11 rounded-lg bg-accent px-4 py-2 text-sm font-medium text-on-accent"
        >
          完成注册
        </button>
        <Link
          to="login"
          className="inline-flex min-h-10 items-center justify-center rounded-md px-3 text-sm font-medium text-accent underline"
        >
          返回登录
        </Link>
      </div>
    </main>
  );
}
`;

interface SharedDesign {
	id: string;
	dir: string;
	version: number;
	turnId: string;
}

let primaryDesign: SharedDesign | undefined;

/** 建一个带登录 / 注册两帧的干净设计画布并封存；供 K1 / K2 / K3 / K6 / K7 复用。 */
async function ensurePrimaryDesign(): Promise<SharedDesign> {
	if (primaryDesign) return primaryDesign;
	const t1 = await ensureT1();
	const turnId = `m29-turn-${RUN}-1`;
	await orbSockOk(t1, "design.turn", {
		threadId: t1.id,
		turnId,
		title: "做一个登录页和注册页，能互相跳转",
	});
	const opened = await orbSockOk<DesignOpenResult>(
		t1,
		"design.open",
		{ threadId: t1.id, title: `M29 登录与注册 ${RUN}` },
		600_000,
	);
	orbWriteFile(t1, `${opened.dir}/src/theme.css`, THEME_CSS);
	orbWriteFile(t1, `${opened.dir}/src/frames/login.tsx`, loginFrameTsx());
	orbWriteFile(t1, `${opened.dir}/src/frames/signup.tsx`, SIGNUP_FRAME_TSX);
	const sealed = await orbSockOk<DesignSealResult>(
		t1,
		"design.seal",
		{ threadId: t1.id, turnId },
		300_000,
	);
	const item = sealed.sealed.find((x) => x.id === opened.id);
	if (!item)
		throw new Error(`封存未返回 ${opened.id}：${JSON.stringify(sealed)}`);
	primaryDesign = {
		id: opened.id,
		dir: opened.dir,
		version: item.version,
		turnId,
	};
	return primaryDesign;
}

// ---------------------------------------------------------------- K1–K7 场景

/** K1：设计画布静态帧——新建设计画布、封存登录 / 注册两帧（带 <Link to>），查看器里两帧静态渲染可见。 */
async function caseK1() {
	const d = await ensurePrimaryDesign();
	const detail = await getDetail(d.id);
	check(
		detail.canvas?.format === 2 && detail.canvas.design?.engine === 1,
		`canvas.json 为 format 2 且 design.engine=1（head=v${detail.head}）`,
	);
	const pages = detail.canvas?.pages ?? [];
	const login = pages.find((p) => p.source === "src/frames/login.tsx");
	const signup = pages.find((p) => p.source === "src/frames/signup.tsx");
	check(
		!!login &&
			login.file === "dist/login.html" &&
			login.device === "phone" &&
			!!signup &&
			signup.file === "dist/signup.html" &&
			signup.device === "phone",
		`两帧对齐：login(${login?.file}, ${login?.device})、signup(${signup?.file}, ${signup?.device})`,
	);

	const framesJson = JSON.parse(
		await getFileText(d.id, "dist/frames.json"),
	) as { id: string; links: string[] }[];
	expect(
		framesJson.some((f) => f.id === "login" && f.links.includes("signup")) &&
			framesJson.some((f) => f.id === "signup" && f.links.includes("login")),
		`dist/frames.json 含双向跳转连线：${JSON.stringify(framesJson.map((f) => [f.id, f.links]))}`,
	);

	const vs = await getVersions(d.id);
	const top = vs.find((v) => v.version === d.version);
	expect(
		top?.turnId === d.turnId &&
			top.title === "做一个登录页和注册页，能互相跳转",
		`版本 v${d.version} 记录回合与标题（turnId=${top?.turnId}, title=${top?.title}）`,
	);

	// 强制静态预览：验证离线静态帧渲染
	const p = await openPage({ previewPref: "static" });
	await goto(p, `/artifacts/${d.id}`);
	await p.locator("[data-testid=canvas-editor]").waitFor({ timeout: 30_000 });

	const badge = p.locator("[data-testid=canvas-design-mode]");
	await badge.waitFor({ timeout: 15_000 });
	expect(
		(await badge.getAttribute("data-mode")) === "static" &&
			(await badge.textContent())?.includes("静态") === true,
		`顶栏预览徽标为静态（${oneLine((await badge.textContent()) ?? "")}）`,
	);

	const loginFrame = await waitCanvasFrame(p, {
		iframeSelector: "iframe[data-testid=canvas-frame]",
		urlIncludes: "dist/login.html",
		text: "欢迎回来",
	});
	const signupFrame = await waitCanvasFrame(p, {
		iframeSelector: "iframe[data-testid=canvas-frame]",
		urlIncludes: "dist/signup.html",
		text: "创建新账号",
	});
	expect(
		(await loginFrame.frame
			.locator("a", { hasText: "前往注册" })
			.isVisible()) &&
			(await signupFrame.frame
				.locator("a", { hasText: "返回登录" })
				.isVisible()),
		"两帧静态产物均正常渲染且 <Link to> 链接可见",
	);
	await shot(p, "k1-static");
}

/** K2：实时预览与热更——dev 服务运行中出现「实时」徽标并热更新；以及同项目跨 orb 接管切换 live.threadId 与热更新。 */
async function caseK2() {
	const t1 = await ensureT1();
	const d = await ensurePrimaryDesign();

	// 1. 本 orb 实时预览与热更
	const live1 = await waitFor(
		"GET /design 返回线程 A 的 live",
		async () => {
			const g = await getDesign(d.id);
			return g.design && g.live?.threadId === t1.id && g.live.port > 0
				? g.live
				: null;
		},
		20_000,
		400,
	);
	note(`线程 A live：threadId=${live1.threadId} port=${live1.port}`);

	const p1 = await openPage({ previewPref: "live" });
	await goto(p1, `/artifacts/${d.id}`);
	await p1.locator("[data-testid=canvas-editor]").waitFor({ timeout: 30_000 });

	const badge1 = p1.locator("[data-testid=canvas-design-mode]");
	await waitFor(
		"顶栏出现「实时」徽标",
		async () => (await badge1.getAttribute("data-mode")) === "live",
		20_000,
		300,
	);
	check(
		(await badge1.textContent())?.includes("实时") === true,
		`顶栏显示「实时」徽标（${oneLine((await badge1.textContent()) ?? "")}）`,
	);

	const host1 = `t-${threadShortCode(t1.id)}-p${live1.port}`;
	const lf1 = await waitCanvasFrame(p1, {
		iframeSelector: "iframe[data-testid=canvas-live-frame]",
		urlIncludes: host1,
		text: "欢迎回来",
	});
	note(`线程 A 实时帧加载：${clip(lf1.src, 140)}`);

	// 不封存，直接改工作副本源码，验证 vite HMR 热更新
	orbWriteFile(
		t1,
		`${d.dir}/src/frames/login.tsx`,
		loginFrameTsx({ heading: "欢迎回来·热更一" }),
	);
	await waitFor(
		"线程 A 实时帧热更新为「欢迎回来·热更一」",
		async () => {
			const txt = await lf1.frame
				.evaluate(() => document.body?.textContent ?? "")
				.catch(() => "");
			return txt.includes("欢迎回来·热更一");
		},
		20_000,
		250,
	);
	note("✓ 线程 A 修改工作副本源码后，实时帧内文本随热更变化");
	await shot(p1, "k2-hmr");

	// 还原文案并封存，使 head 干净，准备跨 orb 接管
	orbWriteFile(t1, `${d.dir}/src/frames/login.tsx`, loginFrameTsx());
	const cleanTurn = `m29-turn-${RUN}-k2-clean`;
	await orbSockOk(t1, "design.turn", {
		threadId: t1.id,
		turnId: cleanTurn,
		title: "K2 恢复干净基线",
	});
	await orbSockOk(t1, "design.seal", { threadId: t1.id, turnId: cleanTurn });

	// 2. 同项目跨 orb 接管：线程 B 在另一台 orb 上 design.open 同一画布
	const t2 = await ensureT2();
	check(
		t2.orbId !== t1.orbId,
		`线程 B 位于独立 orb（A=${t1.orbId}，B=${t2.orbId}）`,
	);
	const openedB = await orbSockOk<DesignOpenResult>(
		t2,
		"design.open",
		{ threadId: t2.id, id: d.id },
		600_000,
	);
	note(
		`线程 B design.open 接管完成：dir=${openedB.dir} devUrl=${openedB.devUrl}`,
	);

	const live2 = await waitFor(
		"GET /design 切换为线程 B 所在 orb 的 live",
		async () => {
			const g = await getDesign(d.id);
			return g.live?.threadId === t2.id && g.live.port > 0 ? g.live : null;
		},
		25_000,
		500,
	);
	check(
		live2.threadId === t2.id,
		`跨 orb 接管后 GET /design 返回 B 的 live（threadId=${live2.threadId} port=${live2.port}）`,
	);

	const p2 = await openPage({ previewPref: "live" });
	await goto(p2, `/artifacts/${d.id}`);
	await p2.locator("[data-testid=canvas-editor]").waitFor({ timeout: 30_000 });
	await waitFor(
		"跨 orb 查看器顶栏出现「实时」徽标",
		async () =>
			(await p2
				.locator("[data-testid=canvas-design-mode]")
				.getAttribute("data-mode")) === "live",
		20_000,
		300,
	);

	const host2 = `t-${threadShortCode(t2.id)}-p${live2.port}`;
	const lf2 = await waitCanvasFrame(p2, {
		iframeSelector: "iframe[data-testid=canvas-live-frame]",
		urlIncludes: host2,
		text: "欢迎回来",
	});
	note(`线程 B 实时帧走 B 的 portal 地址：${clip(lf2.src, 140)}`);

	orbWriteFile(
		t2,
		`${openedB.dir}/src/frames/login.tsx`,
		loginFrameTsx({ heading: "欢迎回来·跨Orb热更二" }),
	);
	await waitFor(
		"线程 B 实时帧热更新为「欢迎回来·跨Orb热更二」",
		async () => {
			const txt = await lf2.frame
				.evaluate(() => document.body?.textContent ?? "")
				.catch(() => "");
			return txt.includes("欢迎回来·跨Orb热更二");
		},
		20_000,
		250,
	);
	note("✓ 跨 orb 接管后修改线程 B 工作副本，查看器实时帧随热更变化");
	await shot(p2, "k2-cross-orb-hmr");

	// 收尾：停掉 B 的 dev 服务，把 live 归还给 A，避免影响后续用例
	await orbSockCall(t2, "service.stop", {
		name: designServiceName(d.id),
		remove: true,
	});
}

/** K3：源码定位——帧内有 data-oryn-loc、右面板点选元素 SelectionChip 显示 src/frames/…:行、评论锚点带 anchor.loc。 */
async function caseK3() {
	const t1 = await ensureT1();
	const d = await ensurePrimaryDesign();

	// 1. 在线程右面板 Artifacts 标签里点选设计画布元素，严格断言 SelectionChip 显示 src/frames/…:行
	const p = await openPage({ previewPref: "static" });
	await goto(p, `/t/${t1.id}`);
	await p
		.locator("[data-testid=transcript]")
		.first()
		.waitFor({ timeout: 30_000 });
	await openPanel(p, "artifacts");
	await p
		.locator("[data-testid=artifact-panel] [data-testid=canvas-editor]")
		.waitFor({ timeout: 30_000 });

	// 如该线程有多个 artifact，切到目标设计画布
	const titleText =
		(await p
			.locator("[data-testid=artifact-panel] [data-testid=artifact-switch]")
			.textContent()
			.catch(() => "")) ?? "";
	if (!titleText.includes(`M29 登录与注册 ${RUN}`)) {
		await p
			.locator("[data-testid=artifact-panel] [data-testid=artifact-switch]")
			.click();
		await p
			.getByRole("menuitem", { name: new RegExp(`M29 登录与注册 ${RUN}`) })
			.click();
		await sleep(400);
	}

	const panelFrame = await waitCanvasFrame(p, {
		iframeSelector:
			"[data-testid=artifact-panel] iframe[data-testid=canvas-frame], [data-testid=artifact-panel] iframe[data-testid=canvas-live-frame]",
		urlIncludes: "login",
		text: "欢迎回来",
	});

	// 校验帧内元素带 data-oryn-loc
	const h1Loc = await panelFrame.frame
		.locator("h1")
		.first()
		.getAttribute("data-oryn-loc");
	check(
		typeof h1Loc === "string" &&
			/^src\/frames\/login\.tsx:\d+:\d+$/.test(h1Loc),
		`帧内 <h1> 带 data-oryn-loc="${h1Loc ?? ""}"`,
	);

	// 启用选择模式（如查看器顶栏或画布工具条提供选择按钮）
	const selectBtn = p
		.locator(
			"[data-testid=artifact-panel] [data-testid=artifact-mode-select], [data-testid=artifact-panel] [data-testid=canvas-tool-select]",
		)
		.first();
	if (await selectBtn.isVisible().catch(() => false)) {
		await selectBtn.click();
		await sleep(300);
	}

	// 按 <h1> 的屏幕坐标点击（兼容覆盖层 select-pick 与帧内直接选择）
	const h1Box = await panelFrame.frame.locator("h1").first().boundingBox();
	check(!!h1Box, "取到右面板登录帧内 <h1> 的屏幕包围盒");
	if (h1Box) {
		await p.mouse.click(h1Box.x + h1Box.width / 2, h1Box.y + h1Box.height / 2);
	}

	const chipLoc = p.locator("[data-testid=composer-artifact-selection-loc]");
	await chipLoc.waitFor({ timeout: 10_000 });
	const chipText = oneLine((await chipLoc.textContent()) ?? "");
	check(
		/^src\/frames\/login\.tsx:\d+$/.test(chipText),
		`右面板输入区 SelectionChip 显示源码位置：${chipText}`,
	);
	await shot(p, "k3-selection-chip");

	// 2. 同时验证整页查看器里用评论工具点选元素，落出的评论锚点带 anchor.loc
	const p2 = await openPage({ previewPref: "static" });
	await goto(p2, `/artifacts/${d.id}`);
	await p2.locator("[data-testid=canvas-editor]").waitFor({ timeout: 30_000 });
	const fullFrame = await waitCanvasFrame(p2, {
		iframeSelector: "iframe[data-testid=canvas-frame]",
		urlIncludes: "dist/login.html",
		text: "欢迎回来",
	});
	await p2.locator("[data-testid=canvas-tool-comment]").click();
	await sleep(250);
	const fullH1 = await fullFrame.frame.locator("h1").first().boundingBox();
	check(!!fullH1, "取到整页查看器登录帧内 <h1> 的屏幕包围盒");
	if (fullH1) {
		await p2.mouse.click(
			fullH1.x + fullH1.width / 2,
			fullH1.y + fullH1.height / 2,
		);
	}
	const box = p2.locator("[data-testid=comment-box]");
	await box.waitFor({ timeout: 10_000 });
	await box.locator("textarea").fill("K3 源码定位评论");
	await box.locator("[data-testid=comment-submit]").click();
	await box.waitFor({ state: "detached", timeout: 10_000 });

	const list = await getComments(d.id);
	const created = list.threads.find((th) =>
		th.comments.some((c) => c.body === "K3 源码定位评论"),
	);
	check(
		!!created &&
			typeof created.anchor.loc === "string" &&
			/^src\/frames\/login\.tsx:\d+:\d+$/.test(created.anchor.loc),
		`评论工具落出的评论带 anchor.loc="${created?.anchor.loc ?? ""}"`,
	);
	await shot(p2, "k3-comment-loc");
}

/** K4：探针检查——故意放一个截断按钮，design.check / check_artifact 返回 text-clipped 和它的 loc，版本历史显示问题徽标。 */
async function caseK4() {
	const t1 = await ensureT1();
	const st = requireEnv();
	const turnId = `m29-turn-${RUN}-k4`;
	await orbSockOk(t1, "design.turn", {
		threadId: t1.id,
		turnId,
		title: "K4 探针截断按钮检查",
	});
	const opened = await orbSockOk<DesignOpenResult>(
		t1,
		"design.open",
		{ threadId: t1.id, title: `M29 K4 探针检查 ${RUN}` },
		600_000,
	);
	orbWriteFile(t1, `${opened.dir}/src/theme.css`, THEME_CSS);
	orbWriteFile(
		t1,
		`${opened.dir}/src/frames/login.tsx`,
		loginFrameTsx({ clippedButton: true }),
	);

	// 1. orb 侧 design.check
	const chk = await orbSockOk<DesignCheckResult>(
		t1,
		"design.check",
		{ threadId: t1.id, id: opened.id },
		120_000,
	);
	if (chk.status === "unverified") {
		throw new Error(
			`orb 侧 design.check 返回 unverified（${chk.message ?? "无详情"}）：请使用含 uid 1000 Chromium 的 docker orb 镜像运行（M29_DRIVER=docker / M29_BASE_IMAGE）`,
		);
	}
	const orbClipped = chk.issues.find(
		(i) =>
			i.rule === "text-clipped" &&
			typeof i.loc === "string" &&
			/^src\/frames\/login\.tsx:\d+:\d+$/.test(i.loc),
	);
	check(
		chk.status === "issues" && !!orbClipped,
		`design.check 返回 status=${chk.status}，命中 text-clipped @ ${orbClipped?.loc ?? "无"}`,
	);

	// 封存版本（自动带入缓存的 check 结果）
	const sealed = await orbSockOk<DesignSealResult>(
		t1,
		"design.seal",
		{ threadId: t1.id, turnId },
		300_000,
	);
	const sealedVer = sealed.sealed.find((s) => s.id === opened.id)?.version;
	check(!!sealedVer, `封存成功 → v${sealedVer ?? "?"}`);

	// 2. 控制面侧 check_artifact
	if (!st.renderOk)
		throw new Error(
			`控制面渲染器不可用（${st.renderReason || "capabilities.render=false"}）：K4 需要控制面 Chromium 跑 check_artifact`,
		);
	const ctlCheck = orbArtifactCall(t1, "check_artifact", { id: opened.id });
	check(
		ctlCheck.details?.status === "issues" &&
			ctlCheck.text.includes("text-clipped") &&
			/src\/frames\/login\.tsx:\d+:\d+/.test(ctlCheck.text),
		`check_artifact 返回 status=${String(ctlCheck.details?.status)}，含 text-clipped 与源码 loc`,
	);

	// 3. REST 版本列表与查看器版本历史显示问题徽标
	const vs = await getVersions(opened.id);
	const latest = vs.find((v) => v.version === sealedVer);
	check(
		latest?.check?.status === "issues" &&
			latest.check.issues.some(
				(i) =>
					i.rule === "text-clipped" &&
					/^src\/frames\/login\.tsx:\d+:\d+$/.test(i.loc ?? ""),
			),
		`版本 v${sealedVer} 持久化 check_json：status=${latest?.check?.status}，issues=${latest?.check?.issues.length}`,
	);

	const p = await openPage({ previewPref: "static" });
	await goto(p, `/artifacts/${opened.id}?history=1`);
	await p.locator("[data-testid=version-list]").waitFor({ timeout: 20_000 });
	// 同回合的创建(v1)与封存(v2)会按 I1 规则折叠为 version-group，组头即带检查徽标；展开后逐行亦带徽标
	const groupBtn = p
		.locator(`[data-testid=version-group][data-turn="${turnId}"] > button`)
		.first();
	if (await groupBtn.isVisible().catch(() => false)) {
		const groupBadge = groupBtn.locator(
			"[data-testid=version-check][data-status=issues]",
		);
		await groupBadge.waitFor({ timeout: 10_000 });
		await groupBtn.click();
	}
	const checkBadge = p
		.locator(
			`[data-testid=version-list] [data-version="${sealedVer}"] [data-testid=version-check][data-status=issues]`,
		)
		.first();
	await checkBadge.waitFor({ timeout: 10_000 });
	expect(
		await checkBadge.isVisible(),
		`版本历史行 v${sealedVer} 显示问题徽标（${oneLine((await checkBadge.textContent()) ?? "")}）`,
	);
	await shot(p, "k4-probe-badge");
	await orbSockCall(t1, "service.stop", {
		name: designServiceName(opened.id),
		remove: true,
	});
}

/** K5：旧版查看与恢复对账——钉 v1 → 静态帧旧内容与旧版静态横幅 → 恢复到 v1 → 下一回合工作副本自动对账为恢复后的源码。 */
async function caseK5() {
	const t1 = await ensureT1();

	// 建一个专用于 K5 的设计画布，连封两个有明确文案差异的版本
	const turnA = `m29-turn-${RUN}-k5-a`;
	await orbSockOk(t1, "design.turn", {
		threadId: t1.id,
		turnId: turnA,
		title: "K5 第一稿",
	});
	const opened = await orbSockOk<DesignOpenResult>(
		t1,
		"design.open",
		{ threadId: t1.id, title: `M29 K5 恢复对账 ${RUN}` },
		600_000,
	);
	orbWriteFile(t1, `${opened.dir}/src/theme.css`, THEME_CSS);
	orbWriteFile(
		t1,
		`${opened.dir}/src/frames/login.tsx`,
		loginFrameTsx({ heading: "旧版登录标题-V1" }),
	);
	const sealA = await orbSockOk<DesignSealResult>(
		t1,
		"design.seal",
		{ threadId: t1.id, turnId: turnA },
		300_000,
	);
	const vOld = sealA.sealed.find((s) => s.id === opened.id)?.version ?? 0;
	check(vOld >= 2, `封存旧版 v${vOld}（旧版登录标题-V1）`);

	const turnB = `m29-turn-${RUN}-k5-b`;
	await orbSockOk(t1, "design.turn", {
		threadId: t1.id,
		turnId: turnB,
		title: "K5 第二稿",
	});
	orbWriteFile(
		t1,
		`${opened.dir}/src/frames/login.tsx`,
		loginFrameTsx({ heading: "新版登录标题-V2" }),
	);
	const sealB = await orbSockOk<DesignSealResult>(
		t1,
		"design.seal",
		{ threadId: t1.id, turnId: turnB },
		300_000,
	);
	const vNew = sealB.sealed.find((s) => s.id === opened.id)?.version ?? 0;
	check(vNew === vOld + 1, `封存新版 v${vNew}（新版登录标题-V2）`);

	const st0 = await orbSockOk<DesignStatusResult>(t1, "design.status", {
		threadId: t1.id,
	});
	const cp0 = st0.designs.find((x) => x.id === opened.id);
	check(
		cp0?.dirty === false,
		`封存后工作副本干净（dirty=${String(cp0?.dirty)}）`,
	);

	// 以实时模式打开查看器并钉住旧版 vOld，验证强制静态帧 + 旧版静态横幅
	const p = await openPage({ previewPref: "live" });
	await goto(p, `/artifacts/${opened.id}?history=1`);
	await p.locator("[data-testid=version-list]").waitFor({ timeout: 25_000 });
	const turnAGroup = p
		.locator(`[data-testid=version-group][data-turn="${turnA}"] > button`)
		.first();
	if (await turnAGroup.isVisible().catch(() => false)) {
		await turnAGroup.click();
	}
	await p
		.locator(`[data-testid=version-list] [data-version="${vOld}"]`)
		.click();
	await p.locator("[data-testid=artifact-pinned]").waitFor({ timeout: 10_000 });
	const staticBanner = p.locator("[data-testid=artifact-pinned-static]");
	await staticBanner.waitFor({ timeout: 10_000 });
	check(
		(await staticBanner.textContent())?.includes("旧版静态帧") === true,
		`钉住旧版显示旧版静态横幅（${oneLine((await staticBanner.textContent()) ?? "")}）`,
	);

	const pinnedFrame = await waitCanvasFrame(p, {
		iframeSelector: "iframe[data-testid=canvas-frame]",
		urlIncludes: "dist/login.html",
		text: "旧版登录标题-V1",
	});
	expect(
		(await p.locator("iframe[data-testid=canvas-live-frame]").count()) === 0,
		"钉住旧版时不挂载实时帧，仅显示静态帧",
	);
	expect(
		!(await pinnedFrame.frame.evaluate(
			() => document.body?.textContent?.includes("新版登录标题-V2") ?? false,
		)),
		"静态帧渲染的是旧版内容「旧版登录标题-V1」",
	);
	await shot(p, "k5-pinned");

	// 点击恢复这个版本
	await p.locator("[data-testid=artifact-restore-version]").click();
	const vRestored = await waitFor(
		`恢复版本落成 v${vNew + 1}`,
		async () => {
			const det = await getDetail(opened.id);
			return det.head > vNew ? det.head : null;
		},
		15_000,
		300,
	);
	check(
		(await getFileText(opened.id, "src/frames/login.tsx", vRestored)).includes(
			"旧版登录标题-V1",
		),
		`服务端恢复为 v${vRestored}，源码回到「旧版登录标题-V1」`,
	);

	// 下一回合开始（只发 design.turn，不调用 design.open）：验证 orbd 自动对账工作副本
	const turnC = `m29-turn-${RUN}-k5-reconcile`;
	await orbSockOk(t1, "design.turn", {
		threadId: t1.id,
		turnId: turnC,
		title: "K5 恢复后下一回合",
	});

	const reconciledSrc = orbReadFile(t1, `${opened.dir}/src/frames/login.tsx`);
	const reconciledMeta = JSON.parse(
		orbReadFile(t1, `${opened.dir}/.oryn-design.json`),
	) as { baseVersion: number };
	check(
		reconciledSrc.includes("旧版登录标题-V1") &&
			!reconciledSrc.includes("新版登录标题-V2") &&
			reconciledMeta.baseVersion === vRestored,
		`下一回合工作副本自动对账完成（baseVersion=${reconciledMeta.baseVersion}，源码含「旧版登录标题-V1」）`,
	);
	await shot(p, "k5-restored");

	await orbSockCall(t1, "service.stop", {
		name: designServiceName(opened.id),
		remove: true,
	});
}

/** K6：原型预览——进入原型模式，点击帧内 <Link>，原型顶栏标题切换为目标帧名。 */
async function caseK6() {
	const d = await ensurePrimaryDesign();

	const p = await openPage({ previewPref: "static" });
	await goto(p, `/artifacts/${d.id}`);
	await p.locator("[data-testid=canvas-editor]").waitFor({ timeout: 30_000 });

	const openBtn = p.locator("[data-testid=canvas-prototype-open]");
	await openBtn.waitFor({ timeout: 15_000 });
	await openBtn.click();

	const dialog = p.locator("[data-testid=canvas-prototype]");
	await dialog.waitFor({ timeout: 15_000 });

	const title = dialog.locator("[data-testid=prototype-title]");
	await waitFor(
		"原型顶栏初始标题为「登录」",
		async () => oneLine((await title.textContent()) ?? "") === "登录",
		15_000,
		200,
	);
	note("✓ 原型模式打开，初始帧标题为「登录」");

	const shell = dialog.locator("[data-testid=device-shell][data-device=phone]");
	expect(await shell.isVisible(), "登录帧按 device=phone 套上手机设备外壳");

	const protoFrame1 = await waitCanvasFrame(p, {
		iframeSelector: "[data-testid=canvas-prototype] iframe",
		text: "前往注册",
	});
	await protoFrame1.frame.locator("a", { hasText: "前往注册" }).first().click();

	await waitFor(
		'点击 <Link to="signup"> 后原型顶栏标题切换为「注册」',
		async () => oneLine((await title.textContent()) ?? "") === "注册",
		15_000,
		200,
	);
	note("✓ 点击帧内 <Link> 后，原型顶栏标题切换为「注册」");

	const protoFrame2 = await waitCanvasFrame(p, {
		iframeSelector: "[data-testid=canvas-prototype] iframe",
		text: "创建新账号",
	});
	await shot(p, "k6-prototype-signup");

	// 再点回登录帧，验证双向跳转
	await protoFrame2.frame.locator("a", { hasText: "返回登录" }).first().click();
	await waitFor(
		'点击 <Link to="login"> 后原型顶栏标题切回「登录」',
		async () => oneLine((await title.textContent()) ?? "") === "登录",
		15_000,
		200,
	);
	note("✓ 点击「返回登录」后，原型顶栏标题切回「登录」");
}

/** K7：样机导出——调用 format=mockup-png 带缩放与选项，验证返回的 PNG 头部与实际宽高和选项一致。 */
async function caseK7() {
	const st = requireEnv();
	if (!st.renderOk)
		throw new Error(
			`控制面渲染器不可用（${st.renderReason || "capabilities.render=false"}）：K7 样机导出需要控制面配置 Chromium 渲染器`,
		);

	const d = await ensurePrimaryDesign();
	const detail = await getDetail(d.id);
	const pages = detail.canvas?.pages ?? [];
	const login = pages.find((p) => p.source === "src/frames/login.tsx");
	const signup = pages.find((p) => p.source === "src/frames/signup.tsx");
	check(
		!!login && !!signup,
		`定位到 login(${login?.id}) 与 signup(${signup?.id})`,
	);
	if (!login || !signup) return;

	// 1. 单帧 390×844 phone + shell=true (+24W,+60H) + padding 96 → 510×1000，scale=2 → 1020×2000
	const r1 = await api(
		"GET",
		`/api/v1/artifacts/${d.id}/export?format=mockup-png&frames=${encodeURIComponent(login.id)}&scale=2&shell=true&radius=16&border=true&shadow=true&bg=dark`,
		undefined,
		{ raw: true },
	);
	check(
		r1.status === 200 &&
			(r1.headers.get("content-type") ?? "").includes("image/png"),
		`mockup-png (scale=2, shell=true) → HTTP ${r1.status} ${r1.headers.get("content-type")}`,
	);
	const s1 = pngInfo(r1.bytes);
	check(
		s1?.w === 1020 && s1.h === 2000,
		`PNG 头部合法且尺寸为 ${s1?.w}×${s1?.h}（期望 1020×2000）`,
	);
	writeFileSync(`${SHOTS}/k7-mockup-2x-phone.png`, r1.bytes);

	// 2. 单帧 390×844 + shell=false + padding 96 → 486×940，scale=1 → 486×940
	const r2 = await api(
		"GET",
		`/api/v1/artifacts/${d.id}/export?format=mockup-png&frames=${encodeURIComponent(login.id)}&scale=1&shell=false&radius=0&border=false&shadow=false&bg=transparent`,
		undefined,
		{ raw: true },
	);
	check(
		r2.status === 200 &&
			(r2.headers.get("content-type") ?? "").includes("image/png"),
		`mockup-png (scale=1, shell=false, bg=transparent) → HTTP ${r2.status}`,
	);
	const s2 = pngInfo(r2.bytes);
	check(
		s2?.w === 486 && s2.h === 940,
		`无外壳 1× PNG 尺寸为 ${s2?.w}×${s2?.h}（期望 486×940）`,
	);

	// 3. 双帧 390×844 phone + shell=true + gap 40 + padding 96 → (96 + 414 + 40 + 414) × 1000 = 964×1000
	const r3 = await api(
		"GET",
		`/api/v1/artifacts/${d.id}/export?format=mockup-png&frames=${encodeURIComponent(`${login.id},${signup.id}`)}&scale=1&shell=true&bg=light`,
		undefined,
		{ raw: true },
	);
	check(
		r3.status === 200 &&
			(r3.headers.get("content-type") ?? "").includes("image/png"),
		`mockup-png 双帧 (scale=1, shell=true) → HTTP ${r3.status}`,
	);
	const s3 = pngInfo(r3.bytes);
	check(
		s3?.w === 964 && s3.h === 1000,
		`双帧 1× PNG 尺寸为 ${s3?.w}×${s3?.h}（期望 964×1000）`,
	);

	// 4. mockup-pdf 校验
	const r4 = await api(
		"GET",
		`/api/v1/artifacts/${d.id}/export?format=mockup-pdf&frames=${encodeURIComponent(login.id)}&shell=true`,
		undefined,
		{ raw: true },
	);
	const pdfHead = new TextDecoder("latin1").decode(r4.bytes.subarray(0, 5));
	expect(
		r4.status === 200 &&
			(r4.headers.get("content-type") ?? "").includes("application/pdf") &&
			pdfHead === "%PDF-",
		`mockup-pdf 返回有效 PDF（HTTP ${r4.status}，${r4.bytes.byteLength} 字节）`,
	);
}

const RUNNERS: Record<string, () => Promise<void>> = {
	K1: caseK1,
	K2: caseK2,
	K3: caseK3,
	K4: caseK4,
	K5: caseK5,
	K6: caseK6,
	K7: caseK7,
};

// ---------------------------------------------------------------- 执行与清理

async function runOne(k: string, fn: () => Promise<void>) {
	evidence = [];
	softFails = 0;
	const errs0 = pageErrors.length;
	const diag0 = pageDiag.length;
	console.log(`── ${k} ${CASES.find((c) => c.id === k)?.name ?? ""}`);
	const t0 = Date.now();
	let verdict: Verdict = "PASS";
	let reason: string | undefined;
	try {
		await fn();
		if (softFails > 0) verdict = "FAIL";
	} catch (e) {
		reason = e instanceof Error ? oneLine(e.message).slice(0, 400) : String(e);
		verdict = "FAIL";
		note(`错误：${reason}`);
	}
	const errs = pageErrors.slice(errs0);
	if (errs.length) {
		note(`页面未捕获异常 ${errs.length} 条：${clip(errs.join(" ｜ "), 600)}`);
		verdict = "FAIL";
	}
	if (verdict === "FAIL") {
		const diag = pageDiag.slice(diag0);
		if (diag.length)
			note(`页面诊断 ${diag.length} 条：${clip(diag.join(" ｜ "), 1500)}`);
		let i = 0;
		for (const c of contexts)
			for (const pg of c.pages()) {
				const f = `${SHOTS}/fail-${k}-${i++}.png`;
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
		`${k} ${verdict} ${seconds}s${reason ? `  ${clip(reason, 220)}` : ""}`,
	);
	outcomes.push({ k, verdict, reason, seconds, evidence });
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

async function cleanup() {
	await closeAll();
	if (browser && !(await within(browser.close(), CLOSE_TIMEOUT))) {
		console.log(`关闭浏览器超过 ${CLOSE_TIMEOUT / 1000}s，强制结束`);
		killBrowser();
	}
	netns?.stop();
	for (const b of [...bgs].reverse()) await stopBg(b);
	if (envState?.isolated) {
		killStragglers();
		if (envState.driver === "docker") {
			for (const orbId of spawnedOrbs)
				sh(`docker rm -f ${q(orbId)} >/dev/null 2>&1`);
			if (envState.imageBuilt)
				sh(`docker rmi -f ${q(OVERLAY_IMAGE)} >/dev/null 2>&1`);
		}
		if (existsSync(TMP)) {
			if (KEEP) console.log(`保留 ${TMP}（日志在 logs/）`);
			else {
				for (const f of ["orynd", "provider", "image"])
					if (existsSync(`${LOGS}/${f}.log`))
						copyFileSync(`${LOGS}/${f}.log`, `${SHOTS}/${f}.log`);
				rmSync(TMP, { recursive: true, force: true });
			}
		}
	}
}

for (const sig of ["SIGINT", "SIGTERM"] as const)
	process.on(sig, () => {
		void cleanup().then(() => process.exit(130));
	});

const todo = KS.filter((k) => !ONLY || ONLY.has(k));
console.log(
	`M29 设计画布验收 run=${RUN} cases=${todo.join(",")} shots=${SHOTS}`,
);

let setupError = "";
try {
	try {
		await setupEnv();
	} catch (e) {
		setupError = e instanceof Error ? oneLine(e.message) : String(e);
		console.error(`环境前置未满足：${setupError}`);
	}
	for (const k of todo) {
		if (setupError) {
			outcomes.push({
				k,
				verdict: "FAIL",
				reason: `环境前置未满足：${setupError}`,
				seconds: 0,
				evidence: [],
			});
			console.log(`${k} FAIL（环境前置未满足）`);
			continue;
		}
		await runOne(k, RUNNERS[k] as () => Promise<void>);
	}
} finally {
	await cleanup();
}

// ---------------------------------------------------------------- 汇总

console.log("\n== 汇总");
for (const o of outcomes) {
	console.log(
		`${o.k} ${o.verdict}${o.reason ? `  ${clip(o.reason, 180)}` : ""}`,
	);
}
mkdirSync(SHOTS, { recursive: true });
writeFileSync(
	`${SHOTS}/results.json`,
	JSON.stringify({ run: RUN, cases: todo, outcomes }, null, 2),
);
console.log(`结果：${SHOTS}/results.json`);

const allPassed =
	outcomes.length === todo.length &&
	outcomes.every((o) => o.verdict === "PASS");
if (allPassed) {
	console.log(
		todo.length === KS.length ? "K1–K7 passed" : `${todo.join(",")} passed`,
	);
	process.exit(0);
}
process.exit(1);
