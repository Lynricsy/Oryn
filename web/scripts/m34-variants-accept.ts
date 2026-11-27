#!/usr/bin/env bun
/**
 * M34 画布多版方案（方案组）端到端验收（docs/plan-m34.md「验证」，V1–V4）：
 * 在真实 orb 的设计画布上覆盖方案组并排、查看器里选定与改选、选定通知与落选帧跳过检查、交付排除落选方案。
 * 全程不经 LLM：工作副本由脚本直接写入，封存与检查走 orbd.sock，选定在查看器里点按钮。
 *
 * 用法（web/ 目录）：bun run accept:m34 [-- --only V1,V2,…] [-- --help]
 *
 * 两种运行模式（同 m29-design-accept.ts）：
 * 1. 自建隔离环境（缺省，未设 M34_URL）：在 /var/tmp/oryn-m34-<run>/ 下起一套全新的 orynd
 *    （public_url = http://localhost:7670，portal_domain = localhost，Chromium 渲染降权到 nobody）与 provider
 *    （优先 docker 驱动 + M34_BASE_IMAGE 叠层镜像；缺 docker 或基础镜像时可由 M34_DRIVER=process 回退 process 驱动），
 *    全部结束后自动清理（M34_KEEP=1 保留）。叠层镜像里若没有当前版本的预装设计引擎、但有锁文件逐字节相同的旧版本，
 *    就把旧版本的 node_modules 复制给当前版本（依赖完全相同，只省掉 orb 里的 npm ci；不用软链，与预装目录的布局一致）；
 *    锁不同则照常由 orbd 现装。
 * 2. 接入已有本机 orynd（设 M34_URL，如 http://localhost:7600）：读 M34_TOKEN（缺省 .dev/credentials.json）与
 *    M34_SESSION（缺省调 orynd admin create-web-session 现签），要求目标 orynd 已配 portal_domain 与 Chromium 渲染器，
 *    且至少有一个在线的 docker provider。
 *
 * 绝不假通过：环境不满足前置（缺少 portal_domain、渲染器、orb Chromium、npm 等）直接报错并给出明确前置提示。
 * V4 的导出工程在本机 `npm ci && npm run build`（需要能访问 npm 源）。部署到 box 不在本脚本覆盖范围：自建环境没有 box
 * 基础设施（boxes 见 m23-accept.ts）；部署走 eject，V4 在 orb 里实跑 design.eject 并核对产物，规则与导出工程相同。
 *
 * 环境变量：M34_URL、M34_TOKEN、M34_SESSION、M34_CONFIG、M34_PROVIDER、M34_DRIVER（auto|docker|process）、
 *   M34_BASE_IMAGE（缺省 oryn-orb:dev）、M34_BIN（缺省 <仓库>/bin）、M34_PORT（缺省 7670）、
 *   M34_PROJECT_PORT（缺省 M34_PORT+1，导出工程的静态服务）、M34_SHOTS（缺省 <仓库>/.dev/m34-variants）、
 *   M34_COORD_DIR（缺省 /var/tmp/oryn-m34-coord）、M34_KEEP=1、CHROMIUM（缺省 /usr/bin/chromium）。
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
		id: "V1",
		name: "方案组并排",
		summary:
			"封存三个带独立主题的方案帧与 src/variants.json：canvas.json 为 format 3、方案组三列同顶排在已有画框下方，查看器静态帧与实时帧的强调色各按其主题",
	},
	{
		id: "V2",
		name: "选定与备选",
		summary:
			"查看器里点「选定此方案」选 B：A/C 搬到「备选方案」页；在备选页「改选此方案」选 A 再改回 B；落版本后 picked=b、pickedBy=null",
	},
	{
		id: "V3",
		name: "通知与检查",
		summary:
			"design_notes 列出 pick:home:b，创建线程收到「[Oryn 设计便签]」自动消息且应答后记为已投递；落选帧里的问题不进 design.check，点名时照查",
	},
	{
		id: "V4",
		name: "交付排除",
		summary:
			"样机不含落选画框、点名落选画框回 400；导出工程不含落选帧与落选主题、不含已选定的清单，本机构建后 #/<落选帧> 显示「没有帧」、#/<选中帧> 为 B 的强调色；orb 内 eject 同样排除",
	},
];

const KS = CASES.map((c) => c.id);

const USAGE = [
	"用法：bun scripts/m34-variants-accept.ts [--only V1,V2,…] [--help]",
	"",
	"验收用例（V1–V4，后面的用例依赖前面的状态，单跑时自动补齐前置）：",
	...CASES.map((c) => `  ${c.id}  ${c.name} — ${c.summary}`),
	"",
	"环境变量：",
	"  M34_URL           接入已有本机 orynd（如 http://localhost:7600）；未设则自建隔离环境",
	"  M34_TOKEN         已有 orynd 的设备令牌（缺省读 .dev/credentials.json）",
	"  M34_SESSION       已有 orynd 的网页会话 Cookie（缺省尝试 orynd admin create-web-session）",
	"  M34_CONFIG        已有 orynd 的配置文件路径（配合 M34_URL 现签会话时用）",
	"  M34_PROVIDER      已有 orynd 上新建 orb 用的 provider 名称（缺省自动选在线 docker provider）",
	"  M34_DRIVER        自建环境的 provider 驱动：auto（缺省）| docker | process",
	"  M34_BASE_IMAGE    docker 驱动的基础 orb 镜像（缺省 oryn-orb:dev，须含 Chromium 与设计引擎依赖）",
	"  M34_BIN           二进制目录（缺省 <仓库>/bin，须含 orynd/orbd/oryn/fakeomp）",
	"  M34_PORT          自建 orynd 端口（缺省 7670）",
	"  M34_PROJECT_PORT  导出工程静态服务端口（缺省 M34_PORT+1）",
	"  M34_SHOTS         截图与结果目录（缺省 <仓库>/.dev/m34-variants）",
	"  M34_KEEP=1        保留临时目录 /var/tmp/oryn-m34-<run>",
	"  CHROMIUM          无头浏览器路径（缺省 /usr/bin/chromium）",
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
	console.error(`--only 只接受 V1…V4\n\n${USAGE}`);
	process.exit(2);
}

const ROOT = `${import.meta.dir}/../..`;
const SHOTS = process.env.M34_SHOTS ?? `${ROOT}/.dev/m34-variants`;
const CHROMIUM = process.env.CHROMIUM ?? "/usr/bin/chromium";
const BIN = process.env.M34_BIN ?? `${ROOT}/bin`;
const COORD_DIR = process.env.M34_COORD_DIR ?? "/var/tmp/oryn-m34-coord";
const KEEP = process.env.M34_KEEP === "1";
const BASE_IMAGE = process.env.M34_BASE_IMAGE ?? "oryn-orb:dev";
const REAL_PORT = Number(process.env.M34_PORT ?? 7670);
const PROJECT_PORT = Number(process.env.M34_PROJECT_PORT ?? REAL_PORT + 1);
const COORD_EGRESS = 7971;
const RUN = Math.random().toString(36).slice(2, 6);
const TMP = `/var/tmp/oryn-m34-${RUN}`;
const LOGS = `${TMP}/logs`;
const DB = `${TMP}/data/oryn.db`;
const OVERLAY_IMAGE = `oryn-orb:m34accept-${RUN}`;
const DESIGN_SHARE = "/usr/local/share/oryn/design-engine";

const EXTERNAL_URL = process.env.M34_URL?.replace(/\/+$/, "");

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
	if (b[12] !== 0x49 || b[13] !== 0x48 || b[14] !== 0x44 || b[15] !== 0x52)
		return null;
	const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
	return { w: v.getUint32(16), h: v.getUint32(20) };
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
	const proc = Bun.spawn(cmd, { cwd, env, stdout: log, stderr: log });
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
	/** 脚本调 REST API 的地址（http://127.0.0.1:<port> 或 M34_URL）。 */
	apiBase: string;
	auth: Auth;
	driver: OrbDriver;
	provider: string;
	isolated: boolean;
	imageBuilt: boolean;
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
	opts?: { raw?: boolean },
): Promise<{
	status: number;
	headers: Headers;
	text: string;
	bytes: Uint8Array<ArrayBuffer>;
}> {
	const { apiBase, auth } = requireEnv();
	const res = await fetch(`${apiBase}${path}`, {
		method,
		headers: {
			Authorization: `Bearer ${auth.token}`,
			"X-Oryn-CSRF": "1",
			...(body !== undefined ? { "Content-Type": "application/json" } : {}),
		},
		body: body !== undefined ? JSON.stringify(body) : undefined,
	});
	const bytes = new Uint8Array(await res.arrayBuffer());
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

const BROWSER_MARK = `--oryn-m34-run=${RUN}`;
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
			name: `oryn-m34-${RUN}`,
			ports: [port, PROJECT_PORT],
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

function watchPage(p: Page) {
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
}

/** 带登录会话的 Oryn 页面；previewPref 固定设计画布的预览方式。 */
async function openPage(opts?: {
	previewPref?: "live" | "static";
}): Promise<Page> {
	const { uiBase, auth } = requireEnv();
	const b = await getBrowser();
	const ctx = await b.newContext({
		locale: "zh-CN",
		viewport: { width: 1440, height: 900 },
		colorScheme: "light",
	});
	await ctx.addCookies([
		{
			name: "__Host-oryn_session",
			value: auth.session,
			domain: new URL(uiBase).hostname,
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
			${pref ? `localStorage.setItem("oryn.design.preview", ${JSON.stringify(pref)});` : `localStorage.removeItem("oryn.design.preview");`}
		} catch {}
	})();`);
	contexts.push(ctx);
	const p = await ctx.newPage();
	watchPage(p);
	return p;
}

/** 不带会话的普通页面（导出工程的静态服务用）。 */
async function openPlainPage(): Promise<Page> {
	const b = await getBrowser();
	const ctx = await b.newContext({
		locale: "zh-CN",
		viewport: { width: 1280, height: 800 },
		colorScheme: "light",
	});
	contexts.push(ctx);
	const p = await ctx.newPage();
	watchPage(p);
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
	note(`截图 ${SHOTS}/${name}.png`);
}

/** 在页面内按 iframe 选择器找包含指定文字的子帧；urlIncludes 对解码后的 src 匹配（实时帧经 `__oryn/login?next=` 进入）。 */
async function waitCanvasFrame(
	p: Page,
	opts: {
		iframeSelector: string;
		text: string;
		urlIncludes: string;
		timeoutMs?: number;
	},
): Promise<{ frame: Frame; src: string }> {
	return waitFor(
		`画布帧就绪（${opts.iframeSelector} ${opts.urlIncludes} 含「${opts.text}」）`,
		async () => {
			const handles = await p.locator(opts.iframeSelector).elementHandles();
			for (const h of handles) {
				const src = (await h.getAttribute("src")) ?? "";
				if (!decodeURIComponent(src).includes(opts.urlIncludes)) continue;
				const f = await h.contentFrame();
				if (!f) continue;
				const ready = await f
					.evaluate(() => document.readyState === "complete")
					.catch(() => false);
				if (!ready) continue;
				const body = await f
					.evaluate(() => document.body?.textContent ?? "")
					.catch(() => "");
				if (!body.includes(opts.text)) continue;
				return { frame: f, src };
			}
			return null;
		},
		opts.timeoutMs ?? 45_000,
		300,
	);
}

/** 帧（或页面）里第一个 `button.bg-accent` 的计算背景色与 `<html data-oryn-theme>`。 */
async function accentOf(
	f: Frame | Page,
): Promise<{ bg: string; theme: string }> {
	return f.evaluate(() => {
		const el = document.querySelector("button.bg-accent");
		return {
			bg: el ? getComputedStyle(el).backgroundColor : "",
			theme: document.documentElement.dataset.orynTheme ?? "",
		};
	});
}

// ---------------------------------------------------------------- 环境搭建

async function setupEnv() {
	mkdirSync(SHOTS, { recursive: true });
	if (!existsSync(CHROMIUM))
		throw new Error(
			`找不到 Chromium：${CHROMIUM}（设 CHROMIUM=/path/to/chromium）`,
		);
	for (const tool of ["unzip", "npm", "node"])
		if (sh(`command -v ${tool}`).code !== 0)
			throw new Error(`本机缺少必需命令：${tool}（V4 解包并构建导出工程用）`);
	if (sh(`ss -Hltn 'sport = :${PROJECT_PORT}'`).stdout !== "")
		throw new Error(
			`127.0.0.1:${PROJECT_PORT} 已被占用（设 M34_PROJECT_PORT 换端口）`,
		);
	mkdirSync(LOGS, { recursive: true });

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
			`无法连接 M34_URL=${baseUrl}/healthz：请先启动配置了 portal_domain 的本机 orynd`,
		);

	const credPath = `${ROOT}/.dev/credentials.json`;
	let token = process.env.M34_TOKEN ?? "";
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
			`缺少设备令牌：设 M34_TOKEN 或确保 ${credPath} 存在（oryn login 生成）`,
		);

	let session = process.env.M34_SESSION ?? "";
	if (!session) {
		const cfg = process.env.M34_CONFIG ?? `${ROOT}/.dev/orynd.yaml`;
		if (!existsSync(`${BIN}/orynd`) || !existsSync(cfg))
			throw new Error(
				`缺少网页会话 Cookie：设 M34_SESSION，或确保 ${BIN}/orynd 与 ${cfg} 存在以现签会话`,
			);
		const ses = sh(
			`${q(`${BIN}/orynd`)} --config ${q(cfg)} admin create-web-session --user-agent m34-accept`,
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
		renderOk: false,
		renderReason: "",
	};

	const provs = (
		await rjson<{
			providers: { name: string; online: boolean; driver?: string }[];
		}>("GET", "/api/v1/providers")
	).providers;
	const wantProv = process.env.M34_PROVIDER;
	const chosen = wantProv
		? provs.find((p) => p.name === wantProv && p.online)
		: (provs.find((p) => p.online && p.driver === "docker") ??
			provs.find((p) => p.online));
	if (!chosen)
		throw new Error(
			`目标 orynd（${baseUrl}）没有在线的 provider${wantProv ? ` ${wantProv}` : ""}；请先启动 provider（含设计引擎与 Chromium 的 orb 镜像）`,
		);
	envState.provider = chosen.name;
	envState.driver = chosen.driver === "process" ? "process" : "docker";

	await checkCapabilities();
}

/** 叠层镜像的 Dockerfile：换上本仓库的 omp/orbd/oryn，并在锁文件相同时把预装引擎依赖复制给当前引擎版本。 */
function overlayDockerfile(): string {
	const ver =
		/^const Version = "([0-9a-f]{12})"$/m.exec(
			readFileSync(`${ROOT}/internal/designengine/designengine.go`, "utf8"),
		)?.[1] ?? "";
	if (!ver)
		throw new Error(
			"取不到 internal/designengine/designengine.go 的 const Version",
		);
	const h = new Bun.CryptoHasher("sha256");
	h.update(
		readFileSync(`${ROOT}/internal/designengine/engine/package-lock.json`),
	);
	const lock = h.digest("hex");
	const link = [
		`d=${DESIGN_SHARE};`,
		`if [ ! -d "$d/${ver}/node_modules" ]; then`,
		`  for o in "$d"/*/; do`,
		`    if [ -f "$o/package-lock.json" ] && [ "$(sha256sum < "$o/package-lock.json" | cut -d' ' -f1)" = "${lock}" ]; then`,
		`      mkdir -p "$d/${ver}" && cp -a "\${o%/}/node_modules" "$d/${ver}/node_modules" && echo "引擎 ${ver} 依赖复制自 $o"; break;`,
		"    fi;",
		"  done;",
		"fi",
	].join("\n");
	return `FROM ${BASE_IMAGE}\nUSER root\nCOPY --chmod=0755 omp orbd oryn /usr/local/bin/\nRUN ${link.replaceAll("\n", " \\\n  ")}\nUSER user\n`;
}

async function setupIsolated() {
	if (process.getuid?.() !== 0)
		throw new Error(
			"自建隔离环境需要 root 权限（用于浏览器网络命名空间与 Chromium 渲染降权到 nobody）；非 root 环境请先起本机 orynd 并设 M34_URL",
		);
	for (const b of ["orynd", "orbd", "oryn", "fakeomp"])
		if (!existsSync(`${BIN}/${b}`))
			throw new Error(
				`缺少 ${BIN}/${b}：请先在仓库根执行 make web && make build（或设 M34_BIN）`,
			);
	for (const tool of ["git", "curl", "sqlite3"])
		if (sh(`command -v ${tool}`).code !== 0)
			throw new Error(`本机缺少必需命令：${tool}`);
	if (sh(`ss -Hltn 'sport = :${REAL_PORT}'`).stdout !== "")
		throw new Error(`127.0.0.1:${REAL_PORT} 已被占用（设 M34_PORT 换端口）`);

	const wantDriver = (process.env.M34_DRIVER ?? "auto").toLowerCase();
	if (!["auto", "docker", "process"].includes(wantDriver))
		throw new Error("M34_DRIVER 只接受 auto、docker 或 process");

	let driver: OrbDriver;
	if (wantDriver === "docker") {
		if (sh("docker info >/dev/null 2>&1").code !== 0)
			throw new Error("M34_DRIVER=docker 但本机 docker 不可用");
		if (sh(`docker image inspect ${q(BASE_IMAGE)} >/dev/null 2>&1`).code !== 0)
			throw new Error(
				`缺少 orb 基础镜像 ${BASE_IMAGE}：请先执行 make image（或设 M34_BASE_IMAGE）`,
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
		writeFileSync(`${TMP}/image/Dockerfile`, overlayDockerfile());
		const built = sh(
			`docker build --progress=plain -t ${q(OVERLAY_IMAGE)} ${q(`${TMP}/image`)} > ${q(`${LOGS}/image.log`)} 2>&1`,
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
		const linked = /引擎 [0-9a-f]{12} 依赖复制自 \/\S+/.exec(
			readFileSync(`${LOGS}/image.log`, "utf8"),
		)?.[0];
		console.log(
			`  orb 镜像：${OVERLAY_IMAGE}（基于 ${BASE_IMAGE}，docker 驱动${linked ? `；${linked}` : ""}）`,
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

	const rootPage = await fetch(`${apiBase}/`);
	const rootHtml = await rootPage.text();
	if (!rootPage.ok || !/<div[^>]+id="root"/i.test(rootHtml))
		throw new Error(
			"orynd 未提供内嵌 Web UI（GET / 不是 SPA 入口）：请先在仓库根执行 make web && make build",
		);

	const admin = (a: string) =>
		sh(`${q(`${BIN}/orynd`)} --config ${q(`${TMP}/orynd.yaml`)} admin ${a}`);
	const dev = admin("create-device --name m34-accept --kind cli");
	if (dev.code !== 0) throw new Error(`create-device 失败：${dev.out}`);
	const ses = admin("create-web-session --user-agent m34-accept");
	if (ses.code !== 0) throw new Error(`create-web-session 失败：${ses.out}`);
	const auth: Auth = {
		token: dev.stdout.split("\n").at(-1)?.trim() ?? "",
		session: ses.stdout.split("\n").at(-1)?.trim() ?? "",
	};

	const own = sh(
		`sqlite3 ${q(DB)} ${q(`INSERT OR IGNORE INTO owner (id, display_name, user_handle, created_at) VALUES (1, 'm34', randomblob(16), ${Date.now()})`)}`,
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
	st.renderOk = caps.render;
	st.renderReason = caps.renderReason ?? "";
	console.log(
		`  能力：frames=${caps.frames} render=${caps.render}${caps.renderReason ? `（${caps.renderReason}）` : ""} driver=${st.driver} provider=${st.provider}`,
	);
	if (caps.frames !== "host")
		throw new Error(
			`当前 orynd 未配置 portal_domain（capabilities.frames=${caps.frames}）：设计画布实时帧（V1）需要配置 portal_domain（例如 localhost）`,
		);
}

// ---------------------------------------------------------------- 线程与 orb 操作

interface Th {
	id: string;
	orbId: string;
}

interface ThreadView {
	id: string;
	state: string;
	orbId?: string;
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

function messageText(e: EntryRow, role: string): string {
	const m = e.payload.message;
	if (m?.role !== role) return "";
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
				(e) =>
					e.type === "message" && messageText(e, "assistant").trim() !== "",
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

let T1: Th | undefined;

/** 设计线程：新项目、新 orb，fakeomp 回第一条消息后 orbd.sock 就绪。 */
async function ensureT1(): Promise<Th> {
	if (T1) return T1;
	const { provider } = requireEnv();
	const prj = (
		await rjson<{ id: string }>("POST", "/api/v1/projects", {
			name: `m34-${RUN}`,
			shallowClone: true,
		})
	).id;
	const r = await rjson<{ thread: { id: string } }>("POST", "/api/v1/threads", {
		projectId: prj,
		prompt: "m34 验收：方案组设计线程",
		executor: { kind: "new_orb", provider },
	});
	const th = await waitAssistant(r.thread.id, 1);
	if (!th.orbId) throw new Error(`线程 ${th.id} 没有分配 orb`);
	spawnedOrbs.push(th.orbId);
	await waitFor(
		`orb ${th.orbId} 的 orbd.sock 就绪`,
		() => orbSockReady(th.orbId ?? ""),
		60_000,
		500,
	);
	T1 = { id: th.id, orbId: th.orbId };
	return T1;
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

/** 在 orb 里执行一条 shell 命令，返回标准输出。 */
function orbSh(th: Th, cmd: string): string {
	const { driver } = requireEnv();
	const r =
		driver === "process"
			? sh(`cd ${q(orbHome(th.orbId))} && ${cmd}`)
			: sh(`docker exec ${q(th.orbId)} bash -c ${q(cmd)}`);
	if (r.code !== 0)
		throw new Error(`orb 内执行失败（${clip(cmd, 120)}）：${clip(r.out, 240)}`);
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
}

interface DesignSealResult {
	sealed: { id: string; version: number }[];
	skipped: string[];
}

interface DesignCheckResult {
	status: "clean" | "issues" | "unverified";
	issues: { rule: string; frame?: string; loc?: string | null }[];
	message?: string;
}

interface CanvasPageDoc {
	id: string;
	canvas: string;
	name: string;
	file: string;
	source?: string;
	x: number;
	y: number;
	width: number;
	height: number;
}

interface CanvasGroupDoc {
	id: string;
	canvas: string;
	name: string;
	variants: {
		id: string;
		name: string;
		note: string;
		theme: string;
		pages: string[];
	}[];
	picked: string | null;
	pickedBy: string | null;
}

interface CanvasDocView {
	format: number;
	canvases: { id: string; name: string }[];
	pages: CanvasPageDoc[];
	groups?: CanvasGroupDoc[];
}

const getHead = async (id: string) =>
	(await rjson<{ head: number }>("GET", `/api/v1/artifacts/${id}`)).head;
const getFileText = async (id: string, path: string, version?: number) => {
	const r = await api(
		"GET",
		`/api/v1/artifacts/${id}/file?path=${encodeURIComponent(path)}${version ? `&version=${version}` : ""}`,
	);
	if (r.status !== 200)
		throw new Error(`读取 ${id} ${path} 失败：HTTP ${r.status}`);
	return r.text;
};
const getCanvas = async (id: string, version?: number) =>
	JSON.parse(await getFileText(id, "canvas.json", version)) as CanvasDocView;
const pageBySource = (doc: CanvasDocView, frame: string) =>
	doc.pages.find((p) => p.source === `src/frames/${frame}.tsx`);

// ---------------------------------------------------------------- 方案组样例源码

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

/** 基础主题与三个方案主题的强调色（计算样式为 rgb）。 */
const BASE_ACCENT = "rgb(15, 118, 110)";
interface VariantSpec {
	id: "a" | "b" | "c";
	frame: string;
	name: string;
	theme: string;
	hex: string;
	rgb: string;
	heading: string;
}
const VARIANTS: readonly VariantSpec[] = [
	{
		id: "a",
		frame: "home-a",
		name: "冷静克制",
		theme: "calm",
		hex: "#1d4ed8",
		rgb: "rgb(29, 78, 216)",
		heading: "方案 A 冷静克制",
	},
	{
		id: "b",
		frame: "home-b",
		name: "暖调杂志",
		theme: "warm",
		hex: "#c2410c",
		rgb: "rgb(194, 65, 12)",
		heading: "方案 B 暖调杂志",
	},
	{
		id: "c",
		frame: "home-c",
		name: "紫调科技",
		theme: "mono",
		hex: "#7c3aed",
		rgb: "rgb(124, 58, 237)",
		heading: "方案 C 紫调科技",
	},
];
const VB = VARIANTS[1] as VariantSpec;
const GROUP = "home";
const GROUP_NAME = "首页首屏";
const FRAME_W = 480;
const FRAME_H = 640;
const PAGE_GAP = 48; // canvas.PageGap
const INTRO_HEADING = "设计说明";

function frameTsx(opts: {
	name: string;
	heading: string;
	clippedButton?: boolean;
}): string {
	const extra = opts.clippedButton
		? `      <button
        type="button"
        style={{ width: "44px", overflow: "hidden", whiteSpace: "nowrap" }}
        className="mt-4 min-h-11 rounded-lg bg-surface px-2 text-sm font-medium text-text"
      >
        这是一个会被容器截断的超长按钮文案
      </button>\n`
		: "";
	return `export const frame = {
  name: ${JSON.stringify(opts.name)},
  width: ${FRAME_W},
  height: ${FRAME_H},
};

export default function Frame() {
  return (
    <main className="min-h-screen bg-bg p-8 text-text">
      <p className="text-sm text-muted">首页首屏</p>
      <h1 className="mt-2 text-3xl font-bold text-text">${opts.heading}</h1>
      <p className="mt-3 text-base text-muted">把团队的设计稿集中在一处评审与交付。</p>
      <button
        type="button"
        className="mt-6 min-h-11 rounded-lg bg-accent px-4 py-2 text-sm font-medium text-on-accent"
      >
        开始使用
      </button>
${extra}    </main>
  );
}
`;
}

const VARIANTS_JSON = `${JSON.stringify(
	{
		groups: [
			{
				id: GROUP,
				name: GROUP_NAME,
				variants: VARIANTS.map((v) => ({
					id: v.id,
					name: v.name,
					note: `${v.name}的首屏`,
					theme: v.theme,
					frames: [v.frame],
				})),
			},
		],
	},
	null,
	2,
)}\n`;

interface VariantDesign {
	id: string;
	dir: string;
	version: number;
	intro: CanvasPageDoc;
}

let variantDesign: VariantDesign | undefined;

async function sealTurn(th: Th, turnId: string): Promise<number> {
	const sealed = await orbSockOk<DesignSealResult>(
		th,
		"design.seal",
		{ threadId: th.id, turnId },
		300_000,
	);
	const v = sealed.sealed[0]?.version;
	if (!v) throw new Error(`封存没有产生版本：${JSON.stringify(sealed)}`);
	return v;
}

/** 建设计画布：第一回合只封存一个普通帧 intro，第二回合加三方案帧、方案主题与 src/variants.json 再封存。 */
async function ensureVariantDesign(): Promise<VariantDesign> {
	if (variantDesign) return variantDesign;
	const t1 = await ensureT1();
	const turn1 = `m34-turn-${RUN}-1`;
	await orbSockOk(t1, "design.turn", {
		threadId: t1.id,
		turnId: turn1,
		title: "做一个设计说明页",
	});
	const opened = await orbSockOk<DesignOpenResult>(
		t1,
		"design.open",
		{ threadId: t1.id, title: `M34 方案组 ${RUN}` },
		600_000,
	);
	orbWriteFile(t1, `${opened.dir}/src/theme.css`, THEME_CSS);
	orbWriteFile(
		t1,
		`${opened.dir}/src/frames/intro.tsx`,
		frameTsx({ name: "设计说明", heading: INTRO_HEADING }),
	);
	const v1 = await sealTurn(t1, turn1);
	const intro = pageBySource(await getCanvas(opened.id, v1), "intro");
	if (!intro) throw new Error(`v${v1} 的 canvas.json 里没有 intro 画框`);
	note(
		`封存 v${v1}：intro 画框 (${intro.x}, ${intro.y}) ${intro.width}×${intro.height}`,
	);

	const turn2 = `m34-turn-${RUN}-2`;
	await orbSockOk(t1, "design.turn", {
		threadId: t1.id,
		turnId: turn2,
		title: "首页首屏出三版方案",
	});
	for (const v of VARIANTS) {
		orbWriteFile(
			t1,
			`${opened.dir}/src/themes/${v.theme}.css`,
			`:root[data-oryn-theme="${v.theme}"] {\n  --color-accent: ${v.hex};\n}\n`,
		);
		orbWriteFile(
			t1,
			`${opened.dir}/src/frames/${v.frame}.tsx`,
			frameTsx({ name: `首页 · ${v.name}`, heading: v.heading }),
		);
	}
	orbWriteFile(t1, `${opened.dir}/src/variants.json`, VARIANTS_JSON);
	const v2 = await sealTurn(t1, turn2);
	note(
		`封存 v${v2}：三方案帧、src/themes/{calm,warm,mono}.css、src/variants.json`,
	);
	variantDesign = { id: opened.id, dir: opened.dir, version: v2, intro };
	return variantDesign;
}

// ---------------------------------------------------------------- 查看器里的选定

const pickBtn = (p: Page, v: string) =>
	p.locator(`[data-variant="${GROUP}/${v}"] [data-testid=canvas-group-pick]`);
const pickedBadge = (p: Page, v: string) =>
	p.locator(`[data-variant="${GROUP}/${v}"] [data-testid=canvas-group-picked]`);
const variantRow = (p: Page, v: string) =>
	p.locator(`[data-variant="${GROUP}/${v}"]`);

async function switchSheet(p: Page, name: string) {
	await p.locator("[data-testid=canvas-sheet-menu]").click();
	await p
		.getByRole("menuitemradio", { name: new RegExp(name) })
		.first()
		.click();
	await waitFor(
		`切到画布页「${name}」`,
		async () =>
			(
				(await p.locator("[data-testid=canvas-sheet-menu]").textContent()) ?? ""
			).includes(name),
		10_000,
		200,
	);
}

/** 关掉全部页面（最后一个观看者离开即落版本），等 head 越过 after 且 canvas.json 里组已选定 want。 */
async function flushPick(
	id: string,
	after: number,
	want: string,
): Promise<{ head: number; doc: CanvasDocView }> {
	await closeAll();
	return waitFor(
		`用户选定落成版本（head > v${after}，picked=${want}）`,
		async () => {
			const head = await getHead(id);
			if (head <= after) return null;
			const doc = await getCanvas(id, head);
			const g = doc.groups?.find((x) => x.id === GROUP);
			return g?.picked === want ? { head, doc } : null;
		},
		60_000,
		500,
	);
}

let pickedHead = 0;

/** V3/V4 单跑时的前置：在查看器里选定 B 并落版本（V2 跑过则直接复用）。 */
async function ensurePickedB(): Promise<VariantDesign> {
	const d = await ensureVariantDesign();
	if (pickedHead) return d;
	const p = await openPage({ previewPref: "static" });
	await goto(p, `/artifacts/${d.id}`);
	await p.locator("[data-testid=canvas-editor]").waitFor({ timeout: 30_000 });
	await pickBtn(p, "b").click();
	await pickedBadge(p, "b").waitFor({ timeout: 15_000 });
	const r = await flushPick(d.id, d.version, "b");
	pickedHead = r.head;
	note(`前置：查看器里选定方案 B，落成 v${r.head}`);
	return d;
}

// ---------------------------------------------------------------- V1–V4 场景

/** V1：方案组并排——format 3 的 groups、三列同顶排在已有画框下方，静态帧与实时帧的强调色各按其主题。 */
async function caseV1() {
	const d = await ensureVariantDesign();
	const doc = await getCanvas(d.id, d.version);
	const first = doc.canvases[0];
	check(
		doc.format === 3 && !!first,
		`canvas.json 为 format ${doc.format}（期望 3），首个画布页 ${first?.id}「${first?.name}」`,
	);
	const g = doc.groups?.find((x) => x.id === GROUP);
	check(
		doc.groups?.length === 1 &&
			!!g &&
			g.name === GROUP_NAME &&
			g.canvas === first?.id &&
			g.picked === null &&
			g.pickedBy === null,
		`groups 只有方案组 ${g?.id}「${g?.name}」，canvas=${g?.canvas}，picked=${g?.picked}，pickedBy=${g?.pickedBy}`,
	);
	if (!g || !first) return;
	const pages = VARIANTS.map((v) => pageBySource(doc, v.frame));
	check(
		JSON.stringify(g.variants.map((v) => [v.id, v.theme, v.pages])) ===
			JSON.stringify(VARIANTS.map((v, i) => [v.id, v.theme, [pages[i]?.id]])),
		`三个方案与主题、画框一一对应：${JSON.stringify(g.variants.map((v) => [v.id, v.theme, v.pages]))}`,
	);

	// C2 排位：新组在首个画布页现有画框（intro）下方，上边缘 = 最大下边缘 + PageGap*4，左对齐，列间距 PageGap*2
	const top = d.intro.y + d.intro.height + PAGE_GAP * 4;
	const want = VARIANTS.map((_, i) => ({
		x: d.intro.x + i * (FRAME_W + PAGE_GAP * 2),
		y: top,
	}));
	const got = pages.map((p) => ({ x: p?.x, y: p?.y, canvas: p?.canvas }));
	check(
		pages.every(
			(p, i) =>
				!!p &&
				p.canvas === first.id &&
				p.x === want[i]?.x &&
				p.y === want[i]?.y &&
				p.width === FRAME_W &&
				p.height === FRAME_H,
		),
		`三方案三列同顶：实际 ${JSON.stringify(got)}，期望 ${JSON.stringify(want)}（intro 下方 ${PAGE_GAP * 4}px，列距 ${PAGE_GAP * 2}px）`,
	);
	const introNow = pageBySource(doc, "intro");
	expect(
		introNow?.x === d.intro.x && introNow.y === d.intro.y,
		`已有画框 intro 不动（${introNow?.x}, ${introNow?.y}）`,
	);

	// 查看器：静态帧
	const ps = await openPage({ previewPref: "static" });
	await goto(ps, `/artifacts/${d.id}`);
	await ps.locator("[data-testid=canvas-editor]").waitFor({ timeout: 30_000 });
	await ps
		.locator(`[data-testid=canvas-groups] [data-group=${GROUP}]`)
		.waitFor({ state: "attached", timeout: 15_000 });
	const title = oneLine(
		(await ps
			.locator("[data-testid=canvas-group-title]")
			.first()
			.textContent()) ?? "",
	);
	expect(title.includes(GROUP_NAME), `方案组标题「${title}」`);
	for (const v of VARIANTS) {
		const btn = pickBtn(ps, v.id);
		await btn.waitFor({ timeout: 10_000 });
		expect(
			oneLine((await btn.textContent()) ?? "") === "选定此方案",
			`方案 ${v.id.toUpperCase()} 有「选定此方案」按钮`,
		);
	}
	for (const v of VARIANTS) {
		const f = await waitCanvasFrame(ps, {
			iframeSelector: "iframe[data-testid=canvas-frame]",
			urlIncludes: `dist/${v.frame}.html`,
			text: v.heading,
		});
		const a = await accentOf(f.frame);
		check(
			a.bg === v.rgb && a.theme === v.theme,
			`静态帧 ${v.frame}：bg-accent=${a.bg}（期望 ${v.rgb}），data-oryn-theme=${a.theme || "无"}`,
		);
	}
	const introStatic = await waitCanvasFrame(ps, {
		iframeSelector: "iframe[data-testid=canvas-frame]",
		urlIncludes: "dist/intro.html",
		text: INTRO_HEADING,
	});
	const ia = await accentOf(introStatic.frame);
	check(
		ia.bg === BASE_ACCENT && ia.theme === "",
		`静态帧 intro（不属于方案）：bg-accent=${ia.bg}（期望基础主题 ${BASE_ACCENT}），无 data-oryn-theme`,
	);
	await shot(ps, "v1-static");

	// 查看器：实时帧（dev 服务的 portal 地址）
	const pl = await openPage({ previewPref: "live" });
	await goto(pl, `/artifacts/${d.id}`);
	await pl.locator("[data-testid=canvas-editor]").waitFor({ timeout: 30_000 });
	await waitFor(
		"顶栏出现「实时」徽标",
		async () =>
			(await pl
				.locator("[data-testid=canvas-design-mode]")
				.getAttribute("data-mode")) === "live",
		30_000,
		300,
	);
	for (const v of VARIANTS) {
		const f = await waitCanvasFrame(pl, {
			iframeSelector: "iframe[data-testid=canvas-live-frame]",
			urlIncludes: `/frames/${v.frame}`,
			text: v.heading,
		}).catch(async (e: unknown) => {
			for (const h of await pl
				.locator("iframe[data-testid=canvas-live-frame]")
				.elementHandles()) {
				const body = await (await h.contentFrame())
					?.evaluate(() => document.body?.textContent ?? "")
					.catch((x: unknown) => `（读不到：${String(x)}）`);
				note(
					`实时帧 ${clip((await h.getAttribute("src")) ?? "", 120)}：「${clip(oneLine(body ?? "（无 frame）"), 80)}」`,
				);
			}
			throw e;
		});
		const a = await waitFor(
			`实时帧 ${v.frame} 样式就绪`,
			async () => {
				const x = await accentOf(f.frame);
				return x.bg ? x : null;
			},
			15_000,
			250,
		);
		check(
			a.bg === v.rgb && a.theme === v.theme,
			`实时帧 ${v.frame}：bg-accent=${a.bg}（期望 ${v.rgb}），data-oryn-theme=${a.theme || "无"}`,
		);
	}
	await shot(pl, "v1-live");
}

/** V2：选定与备选——选 B、A/C 搬到「备选方案」页，备选页改选 A 再改回 B，落版本后核对 canvas.json。 */
async function caseV2() {
	const d = await ensureVariantDesign();
	const before = await getCanvas(d.id);
	const first = before.canvases[0];
	if (!first) throw new Error("canvas.json 没有画布页");
	if (before.groups?.find((g) => g.id === GROUP)?.picked !== null)
		throw new Error("方案组已被选定过：V2 需要从未选定的状态开始（整套重跑）");
	const head0 = await getHead(d.id);

	const p = await openPage({ previewPref: "static" });
	await goto(p, `/artifacts/${d.id}`);
	await p.locator("[data-testid=canvas-editor]").waitFor({ timeout: 30_000 });
	await pickBtn(p, "b").waitFor({ timeout: 15_000 });

	// 1. 选 B：B 显示「已选定」，A/C 离开当前页
	await pickBtn(p, "b").click();
	await pickedBadge(p, "b").waitFor({ timeout: 15_000 });
	await waitFor(
		"A、C 离开首个画布页",
		async () =>
			(await variantRow(p, "a").count()) === 0 &&
			(await variantRow(p, "c").count()) === 0,
		10_000,
		200,
	);
	note("✓ 点「选定此方案」选 B 后 B 显示「已选定」，A、C 不在首个画布页");
	// 截图前等当前页上的静态帧渲染出来（换页后 iframe 要重新加载）
	const rendered = async (ids: string[]) => {
		for (const v of VARIANTS.filter((x) => ids.includes(x.id)))
			await waitCanvasFrame(p, {
				iframeSelector: "iframe[data-testid=canvas-frame]",
				urlIncludes: `dist/${v.frame}.html`,
				text: v.heading,
			});
	};
	await rendered(["b"]);
	await shot(p, "v2-picked-b");

	// 2. 备选方案页：A、C 显示「改选此方案」
	await switchSheet(p, "备选方案");
	for (const v of ["a", "c"]) {
		const btn = pickBtn(p, v);
		await btn.waitFor({ timeout: 10_000 });
		check(
			oneLine((await btn.textContent()) ?? "") === "改选此方案",
			`「备选方案」页上方案 ${v.toUpperCase()} 显示「改选此方案」`,
		);
	}
	await rendered(["a", "c"]);
	await shot(p, "v2-alternates");

	// 3. 改选 A：A 搬回首个画布页，B 搬到备选页
	await pickBtn(p, "a").click();
	await waitFor(
		"改选 A 后 A 离开备选页、B 出现在备选页",
		async () =>
			(await variantRow(p, "a").count()) === 0 &&
			(await pickBtn(p, "b")
				.isVisible()
				.catch(() => false)),
		10_000,
		200,
	);
	note("✓ 备选页上「改选此方案」选 A：A 搬回，B 搬到备选页");

	// 4. 再改回 B
	await pickBtn(p, "b").click();
	await waitFor(
		"改回 B 后 B 离开备选页，A、C 在备选页",
		async () =>
			(await variantRow(p, "b").count()) === 0 &&
			(await pickBtn(p, "a")
				.isVisible()
				.catch(() => false)) &&
			(await pickBtn(p, "c")
				.isVisible()
				.catch(() => false)),
		10_000,
		200,
	);
	await switchSheet(p, first.name);
	await pickedBadge(p, "b").waitFor({ timeout: 10_000 });
	note("✓ 改回 B：首个画布页上 B 显示「已选定」");
	await rendered(["b"]);
	await shot(p, "v2-final-b");

	// 5. 落版本后核对 canvas.json
	const { head, doc } = await flushPick(d.id, head0, "b");
	pickedHead = head;
	const g = doc.groups?.find((x) => x.id === GROUP);
	check(
		g?.picked === "b" && g.pickedBy === null,
		`v${head} canvas.json：picked=${g?.picked}，pickedBy=${g?.pickedBy}（用户选定）`,
	);
	const alt = doc.canvases.find((c) => c.id === "alternates");
	check(
		alt?.name === "备选方案",
		`新增画布页 alternates「${alt?.name ?? "无"}」`,
	);
	const where = Object.fromEntries(
		[...VARIANTS.map((v) => v.frame), "intro"].map((f) => [
			f,
			pageBySource(doc, f)?.canvas,
		]),
	);
	check(
		where["home-a"] === "alternates" &&
			where["home-c"] === "alternates" &&
			where["home-b"] === g?.canvas &&
			where.intro === first.id,
		`画框所在页：${JSON.stringify(where)}（A/C 在 alternates，B 回到组的 ${g?.canvas}）`,
	);
	const vs = (
		await rjson<{ versions: { version: number; turnId?: string }[] }>(
			"GET",
			`/api/v1/artifacts/${d.id}/versions`,
		)
	).versions;
	expect(
		vs.filter((v) => v.version > head0).length === 1 &&
			!vs.find((v) => v.version === head)?.turnId,
		`三次选定在观看者离开时合并成一个用户版本 v${head}（不属于任何回合）`,
	);
}

/** V3：通知与检查——design_notes 列出用户选定，自动消息投递并记为已投递；落选帧不参与 design.check。 */
async function caseV3() {
	const d = await ensurePickedB();
	const t1 = await ensureT1();

	type NotesPick = {
		ref: string;
		kind: string;
		new: boolean;
		group: string;
		variant: string;
		text: string;
		steps: string[];
	};
	const readPicks = (): NotesPick[] => {
		const r = orbArtifactCall(t1, "design_notes", { id: d.id });
		const nl = r.text.indexOf("\n");
		const body = JSON.parse(r.text.slice(nl + 1)) as { picks?: NotesPick[] };
		return body.picks ?? [];
	};
	const picks = readPicks();
	const pk = picks.find((x) => x.ref === `pick:${GROUP}:b`);
	check(
		picks.length === 1 &&
			!!pk &&
			pk.kind === "pick" &&
			pk.group === GROUP &&
			pk.variant === "b",
		`design_notes 的 picks：${JSON.stringify(picks.map((x) => [x.ref, x.kind, x.new]))}`,
	);
	expect(
		(pk?.steps ?? []).some((s) => s.includes(`src/themes/${VB.theme}.css`)),
		`选定的提升步骤指向 src/themes/${VB.theme}.css：${clip((pk?.steps ?? []).join(" ｜ "), 200)}`,
	);

	// 落选帧（home-a）里放一个截断按钮：不点名的 design.check 跳过它，点名时照查
	const aSpec = VARIANTS[0] as VariantSpec;
	const aPath = `${d.dir}/src/frames/${aSpec.frame}.tsx`;
	orbWriteFile(
		t1,
		aPath,
		frameTsx({
			name: `首页 · ${aSpec.name}`,
			heading: aSpec.heading,
			clippedButton: true,
		}),
	);
	try {
		const all = await orbSockOk<DesignCheckResult>(
			t1,
			"design.check",
			{ threadId: t1.id, id: d.id },
			120_000,
		);
		if (all.status === "unverified")
			throw new Error(
				`orb 侧 design.check 返回 unverified（${oneLine(all.message ?? "无详情")}）：请使用含 uid 1000 Chromium 的 docker orb 镜像运行（M34_DRIVER=docker / M34_BASE_IMAGE）`,
			);
		check(
			all.status === "clean" &&
				all.issues.length === 0 &&
				/跳过落选方案的帧：home-a, home-c/.test(all.message ?? ""),
			`不点名的 design.check：status=${all.status}，issues=${all.issues.length}，message「${clip(oneLine(all.message ?? ""), 160)}」`,
		);
		const named = await orbSockOk<DesignCheckResult>(
			t1,
			"design.check",
			{ threadId: t1.id, id: d.id, frames: [aSpec.frame] },
			120_000,
		);
		check(
			named.status === "issues" &&
				named.issues.some(
					(i) => i.rule === "text-clipped" && i.frame === aSpec.frame,
				),
			`点名 frames=[${aSpec.frame}] 时照查：status=${named.status}，规则 ${[...new Set(named.issues.map((i) => i.rule))].join(",")}`,
		);
	} finally {
		orbWriteFile(
			t1,
			aPath,
			frameTsx({ name: `首页 · ${aSpec.name}`, heading: aSpec.heading }),
		);
	}

	// 自动消息：去抖 30s 后投给创建线程，fakeomp 应答后记进 delivered
	const msg = await waitFor(
		"创建线程收到「[Oryn 设计便签]」自动消息",
		async () =>
			(await entries(t1.id))
				.map((e) => messageText(e, "user"))
				.find(
					(t) =>
						t.startsWith("[Oryn 设计便签]") &&
						t.includes(d.id) &&
						t.includes("在 1 个方案组里选定了方案"),
				),
		150_000,
		2_000,
	);
	check(true, `创建线程收到自动消息：「${clip(oneLine(msg), 160)}」`);
	await waitFor(
		"应答后 pick:home:b 记为已投递（design_notes new=false）",
		() => readPicks().find((x) => x.ref === `pick:${GROUP}:b`)?.new === false,
		60_000,
		2_000,
	);
	note(
		`✓ 应答后 design_notes 的 pick:${GROUP}:b 为 new=false（已记进 delivered）`,
	);
}

/** V4：交付排除——样机、导出工程（本机构建并打开）、orb 内 eject 都不含落选方案。 */
async function caseV4() {
	const st = requireEnv();
	if (!st.renderOk)
		throw new Error(
			`控制面渲染器不可用（${st.renderReason || "capabilities.render=false"}）：V4 样机导出需要控制面配置 Chromium 渲染器`,
		);
	const d = await ensurePickedB();
	const t1 = await ensureT1();
	const doc = await getCanvas(d.id);
	const pa = pageBySource(doc, "home-a");
	const pb = pageBySource(doc, "home-b");
	if (!pa || !pb) throw new Error("canvas.json 里缺少 home-a / home-b 画框");

	// 1. 样机：不传 frames 只含 intro 与 home-b（2 帧 480 宽：48*2 + 480*2 + 40 = 1096）
	const m1 = await api(
		"GET",
		`/api/v1/artifacts/${d.id}/export?format=mockup-png&scale=1&shell=false&bg=light`,
		undefined,
		{ raw: true },
	);
	const s1 = pngInfo(m1.bytes);
	check(
		m1.status === 200 && s1?.w === 96 + FRAME_W * 2 + 40,
		`不传 frames 的 mockup-png：HTTP ${m1.status}，${s1?.w}×${s1?.h}（期望宽 ${96 + FRAME_W * 2 + 40} = 2 帧；4 帧会是 ${96 + FRAME_W * 4 + 120}）`,
	);
	expect(s1?.h === 96 + FRAME_H, `样机高 ${s1?.h}（期望 ${96 + FRAME_H}）`);
	mkdirSync(SHOTS, { recursive: true });
	writeFileSync(`${SHOTS}/v4-mockup.png`, m1.bytes);
	note(`样机 ${SHOTS}/v4-mockup.png`);
	const m2 = await api(
		"GET",
		`/api/v1/artifacts/${d.id}/export?format=mockup-png&frames=${encodeURIComponent(pa.id)}`,
	);
	check(
		m2.status === 400 &&
			m2.text.includes("invalid") &&
			m2.text.includes("落选方案") &&
			m2.text.includes(GROUP_NAME),
		`点名落选画框 frames=${pa.id}：HTTP ${m2.status} ${clip(oneLine(m2.text), 160)}`,
	);

	// 2. 导出工程 zip
	const work = `${TMP}/v4`;
	rmSync(work, { recursive: true, force: true });
	mkdirSync(`${work}/project`, { recursive: true });
	const z = await api(
		"GET",
		`/api/v1/artifacts/${d.id}/export?format=project`,
		undefined,
		{ raw: true },
	);
	check(
		z.status === 200 &&
			(z.headers.get("content-type") ?? "").includes("application/zip"),
		`format=project → HTTP ${z.status} ${z.headers.get("content-type")}（${z.bytes.byteLength} 字节）`,
	);
	writeFileSync(`${work}/project.zip`, z.bytes);
	const list = sh(`unzip -Z1 ${q(`${work}/project.zip`)}`).stdout.split("\n");
	const has = (f: string) => list.includes(f);
	check(
		has("src/frames/intro.tsx") &&
			has("src/frames/home-b.tsx") &&
			!has("src/frames/home-a.tsx") &&
			!has("src/frames/home-c.tsx"),
		`zip 帧文件：${list.filter((f) => f.startsWith("src/frames/")).join(", ")}（不含落选的 home-a、home-c）`,
	);
	check(
		has("src/themes/warm.css") &&
			!has("src/themes/calm.css") &&
			!has("src/themes/mono.css") &&
			has("src/theme.css"),
		`zip 主题文件：${list.filter((f) => f.startsWith("src/them")).join(", ")}（只被落选方案引用的 calm、mono 不打包）`,
	);
	check(
		!has("src/variants.json"),
		"zip 不含 src/variants.json（唯一的方案组已选定，整组去掉）",
	);
	const meta = JSON.parse(
		sh(`unzip -p ${q(`${work}/project.zip`)} oryn-frames.json`).stdout,
	) as { frames: { id: string; name: string; theme?: string }[] };
	check(
		JSON.stringify(meta.frames.map((f) => [f.id, f.theme ?? null])) ===
			JSON.stringify([
				["intro", null],
				["home-b", VB.theme],
			]),
		`oryn-frames.json 帧：${JSON.stringify(meta.frames.map((f) => [f.id, f.theme ?? null]))}`,
	);

	// 3. 本机构建导出工程并在 Chromium 里打开
	const proj = `${work}/project`;
	const unz = sh(`unzip -q ${q(`${work}/project.zip`)} -d ${q(proj)}`);
	if (unz.code !== 0) throw new Error(`解包导出工程失败：${unz.out}`);
	const build = sh(
		`cd ${q(proj)} && npm ci --no-audit --no-fund > ${q(`${LOGS}/project-build.log`)} 2>&1 && npm run build >> ${q(`${LOGS}/project-build.log`)} 2>&1`,
	);
	if (build.code !== 0)
		throw new Error(
			`导出工程 npm ci && npm run build 失败（需要能访问 npm 源）：${oneLine(readFileSync(`${LOGS}/project-build.log`, "utf8").slice(-500))}`,
		);
	note("✓ 导出工程 npm ci && npm run build 成功");
	const distJs = sh(
		`grep -rl ${q(VB.heading)} ${q(`${proj}/dist`)} | wc -l; grep -rl -e ${q(VARIANTS[0]?.heading ?? "")} -e ${q(VARIANTS[2]?.heading ?? "")} ${q(`${proj}/dist`)} | wc -l`,
	).stdout.split("\n");
	check(
		Number(distJs[0]) > 0 && Number(distJs[1]) === 0,
		`构建产物含方案 B 的文案、不含 A/C 的文案（命中文件数 ${distJs[0]} / ${distJs[1]}）`,
	);
	bg(
		"project-server",
		["node", "server.mjs"],
		{
			...process.env,
			PORT: String(PROJECT_PORT),
		},
		proj,
	);
	await waitFor(
		"导出工程静态服务就绪",
		async () =>
			(await fetch(`http://127.0.0.1:${PROJECT_PORT}/`).catch(() => null))?.ok,
		20_000,
		300,
	);
	const pp = await openPlainPage();
	const base = `http://localhost:${PROJECT_PORT}/`;
	await pp.goto(`${base}#/home-a`, { waitUntil: "domcontentloaded" });
	await waitFor(
		"#/home-a 显示「没有帧「home-a」」",
		async () =>
			((await pp.locator("body").textContent()) ?? "").includes(
				"没有帧「home-a」",
			),
		20_000,
		250,
	);
	note("✓ 导出工程 #/home-a 显示「没有帧「home-a」」");
	await shot(pp, "v4-project-archived");
	await pp.goto(`${base}#/home-b`, { waitUntil: "domcontentloaded" });
	await pp.getByText(VB.heading).waitFor({ timeout: 20_000 });
	const ab = await accentOf(pp);
	check(
		ab.bg === VB.rgb && ab.theme === VB.theme,
		`导出工程 #/home-b：bg-accent=${ab.bg}（期望 ${VB.rgb}），data-oryn-theme=${ab.theme || "无"}`,
	);
	await shot(pp, "v4-project-picked");
	await pp.goto(`${base}#/intro`, { waitUntil: "domcontentloaded" });
	await pp.getByText(INTRO_HEADING).waitFor({ timeout: 20_000 });
	const ai = await accentOf(pp);
	expect(
		ai.bg === BASE_ACCENT && ai.theme === "",
		`导出工程 #/intro：bg-accent=${ai.bg}（期望 ${BASE_ACCENT}），切换后去掉 data-oryn-theme（${ai.theme || "无"}）`,
	);

	// 4. orb 内 eject（部署到 box 走同一份导出）
	const home = st.driver === "process" ? orbHome(t1.orbId) : "/home/user";
	const to = `${home}/m34-eject-${RUN}`;
	const ej = await orbSockOk<{ dir: string; files: number }>(
		t1,
		"design.eject",
		{ threadId: t1.id, id: d.id, to },
		300_000,
	);
	const ejFiles = orbSh(
		t1,
		`cd ${q(ej.dir)} && find src -type f | sort && cat oryn-frames.json`,
	);
	check(
		ejFiles.includes("src/frames/home-b.tsx") &&
			!ejFiles.includes("src/frames/home-a.tsx") &&
			!ejFiles.includes("src/frames/home-c.tsx") &&
			!ejFiles.includes("src/themes/calm.css") &&
			!ejFiles.includes("src/variants.json") &&
			/"theme":\s*"warm"/.test(ejFiles),
		`orb 内 design.eject 到 ${ej.dir}（${ej.files} 个文件）：不含落选帧、落选主题与已选定的清单，home-b 带 theme=warm`,
	);
	note(
		"部署到 box 未实跑：本环境没有 box 基础设施（boxes 验收见 m23-accept.ts）；部署由 orb 内 eject 出的工程提交后 oryn boxes deploy，交付范围即上面核对的 eject 结果",
	);
}

const RUNNERS: Record<string, () => Promise<void>> = {
	V1: caseV1,
	V2: caseV2,
	V3: caseV3,
	V4: caseV4,
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
	}
	if (existsSync(TMP)) {
		if (KEEP) console.log(`保留 ${TMP}（日志在 logs/）`);
		else {
			mkdirSync(SHOTS, { recursive: true });
			for (const f of ["orynd", "provider", "image", "project-build"])
				if (existsSync(`${LOGS}/${f}.log`))
					copyFileSync(`${LOGS}/${f}.log`, `${SHOTS}/${f}.log`);
			rmSync(TMP, { recursive: true, force: true });
		}
	}
}

for (const sig of ["SIGINT", "SIGTERM"] as const)
	process.on(sig, () => {
		void cleanup().then(() => process.exit(130));
	});

const todo = KS.filter((k) => !ONLY || ONLY.has(k));
console.log(`M34 方案组验收 run=${RUN} cases=${todo.join(",")} shots=${SHOTS}`);

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
		todo.length === KS.length ? "V1–V4 passed" : `${todo.join(",")} passed`,
	);
	process.exit(0);
}
process.exit(1);
