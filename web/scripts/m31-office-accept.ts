#!/usr/bin/env bun
/**
 * M31 Office Artifacts 端到端验收（计划 local://office-artifacts-plan.md 步骤 8、「Verification」的 K1–K14）。
 * 只有真机部分：每条打印 `K<n> [real] PASS|FAIL|SKIP`，截图与取回的文件存进 `.dev/m31-office/`，最后按 K 编号汇总；
 * 全部 PASS（或只有计划允许的条件性 SKIP）时退出码 0。
 *
 * 用法（web/ 目录）：bun run accept:m31-office [-- --only K1,K9] [-- --help]
 *
 * 环境与 accept:m14-artifacts 的真机部分同构（全部在 /var/tmp/oryn-m31o-<run>/ 下，结束时删除；M31O_KEEP=1 保留）：
 *   - orynd（127.0.0.1:7690，data_dir 全新，`portal_domain` 为空 → 帧与发布页走主域回退路径 `/_af/…`、`/p/…`）；
 *     控制面渲染器配成本机 Chromium（root 下降权到 nobody）。
 *   - provider `local`（driver process，orbd = $M31O_BIN/orbd，agent = fakeomp）：orb 的 HOME 是
 *     `<tmp>/orbs/orbs/<orb id>/home`。fakeomp 不执行扩展工具，所以 artifact 由脚本以线程身份在 orb 环境里跑
 *     `oryn artifact call <op> --params-file … --json` 创建 / 修改、跑 `oryn artifact pull` 取回（与扩展工具同一条 orbd.sock 链路）。
 *   - 一个非 mock 的 vite（127.0.0.1:5197，/api、/_af/、/p/ 等代理到这套 orynd）给浏览器用；浏览器跑在只有回环的
 *     独立网络命名空间里（scripts/browser-netns.ts）。
 *   端口避开 accept:m14-artifacts（5193 / 5194 / 7650 / 7951）与其余验收脚本。
 *   前提：以 root 运行；有 git、curl、sqlite3、/usr/bin/chromium；先 `make web && make build`（orynd 要在 `make web`
 *   之后构建：帧里的查看器 bundle 与运行时是嵌进 orynd 的）。
 *
 * 样本：web/sandbox/viewers/testdata/office/sample.{docx,xlsx,pptx,pdf}（scripts/office-fixtures.py 生成）。
 * 条件性 SKIP（计划写明的）：控制面渲染器不可用（`GET /api/v1/artifacts/capabilities` 的 render 为假）时 K1 的缩略图与
 * K9 的 docx PDF 跳过（该条其余断言照常判定，都过了才记 SKIP）。
 *
 * 环境变量：M31O_SHOTS（缺省 <仓库>/.dev/m31-office）、CHROMIUM、M31O_BIN、M31O_COORD_DIR、M31O_KEEP。
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
import type { Subprocess } from "bun";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Frame,
	type Page,
} from "playwright-core";
import { type BrowserNetns, startBrowserNetns } from "./browser-netns";

// ---------------------------------------------------------------- 参数

const KS = Array.from({ length: 14 }, (_, i) => `K${i + 1}`);
const USAGE = `用法：bun scripts/m31-office-accept.ts [--only K1,K9,…] [--help]
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
if (ONLY && [...ONLY].some((k) => !KS.includes(k))) {
	console.error(`--only 只接受 K1…K14\n${USAGE}`);
	process.exit(2);
}

const ROOT = `${import.meta.dir}/../..`;
const WEB = `${import.meta.dir}/..`;
const SHOTS = process.env.M31O_SHOTS ?? `${ROOT}/.dev/m31-office`;
const CHROMIUM = process.env.CHROMIUM ?? "/usr/bin/chromium";
const BIN = process.env.M31O_BIN ?? `${ROOT}/bin`;
const COORD_DIR = process.env.M31O_COORD_DIR ?? "/var/tmp/oryn-m31o-coord";
const KEEP = process.env.M31O_KEEP === "1";
const RUN = Math.random().toString(36).slice(2, 6);
mkdirSync(SHOTS, { recursive: true });

const REAL_PORT = 7690;
const REAL_API = `http://127.0.0.1:${REAL_PORT}`;
const UI_PORT = 5197;
/** public_url 与浏览器都用 localhost（vite 只听 127.0.0.1，Chromium 的 localhost 落到回环）。 */
const REAL_UI = `http://localhost:${UI_PORT}`;
const COORD_EGRESS = 7971;
const TMP = `/var/tmp/oryn-m31o-${RUN}`;
const DB = `${TMP}/data/oryn.db`;

// ---- 样本与类型（计划冻结契约 C1）

type OfficeType = "docx" | "xlsx" | "pptx" | "pdf";
const FIXTURES = `${WEB}/sandbox/viewers/testdata/office`;
const SAMPLE: Record<OfficeType, Bytes> = {
	docx: new Uint8Array(readFileSync(`${FIXTURES}/sample.docx`)),
	xlsx: new Uint8Array(readFileSync(`${FIXTURES}/sample.xlsx`)),
	pptx: new Uint8Array(readFileSync(`${FIXTURES}/sample.pptx`)),
	pdf: new Uint8Array(readFileSync(`${FIXTURES}/sample.pdf`)),
};
const ENTRY: Record<OfficeType, string> = {
	docx: "document.docx",
	xlsx: "workbook.xlsx",
	pptx: "presentation.pptx",
	pdf: "document.pdf",
};

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();
const clip = (s: string, n = 240) =>
	s.length > n ? `${s.slice(0, n)}…（共 ${s.length} 字）` : s;
const isRecord = (v: unknown): v is Record<string, unknown> =>
	typeof v === "object" && v !== null && !Array.isArray(v);
const sha256 = (b: Uint8Array) =>
	new Bun.CryptoHasher("sha256").update(b).digest("hex");
const sameBytes = (a: Uint8Array, b: Uint8Array) =>
	Buffer.from(a).equals(Buffer.from(b));

/** 响应体、下载的字节（`new Uint8Array(ArrayBuffer)` 的类型，可直接交给 Blob）。 */
type Bytes = Uint8Array<ArrayBuffer>;

function sh(
	cmd: string,
	env?: Record<string, string | undefined>,
): { code: number; out: string; stdout: string; stderr: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], { cwd: ROOT, env });
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

function jpegSize(b: Uint8Array): { w: number; h: number } | null {
	if (b[0] !== 0xff || b[1] !== 0xd8) return null;
	let i = 2;
	while (i + 9 < b.length) {
		if (b[i] !== 0xff) return null;
		const m = b[i + 1] ?? 0;
		const len = ((b[i + 2] ?? 0) << 8) | (b[i + 3] ?? 0);
		if (m >= 0xc0 && m <= 0xcf && m !== 0xc4 && m !== 0xc8 && m !== 0xcc)
			return {
				h: ((b[i + 5] ?? 0) << 8) | (b[i + 6] ?? 0),
				w: ((b[i + 7] ?? 0) << 8) | (b[i + 8] ?? 0),
			};
		i += 2 + len;
	}
	return null;
}

function pdfPages(b: Uint8Array): number {
	const s = Buffer.from(b).toString("latin1");
	const typed = s.match(/\/Type\s*\/Page(?![s\w])/g)?.length ?? 0;
	if (typed > 0) return typed;
	const counts = [...s.matchAll(/\/Count\s+(\d+)/g)].map((m) => Number(m[1]));
	return counts.length ? Math.max(...counts) : 0;
}

/** `Content-Disposition` 里的文件名（优先 RFC 5987 的 `filename*`）。 */
function dispositionName(h: string | null): string {
	if (!h) return "";
	const star = /filename\*=UTF-8''([^;]+)/i.exec(h)?.[1];
	if (star) return decodeURIComponent(star);
	return /filename="?([^";]+)"?/i.exec(h)?.[1] ?? "";
}

/**
 * 改 zip 里第一个本地文件头的「修改时间」低字节：恰好改一个字节，容器仍然合法（读 zip 走中央目录，
 * 不校验本地头的时间），用来造一个内容不同的新版本。
 */
function touchZip(b: Bytes): Bytes {
	const out = new Uint8Array(b);
	if (out[0] !== 0x50 || out[1] !== 0x4b || out[2] !== 0x03 || out[3] !== 0x04)
		throw new Error("不是以本地文件头开头的 zip");
	out[10] = (out[10] ?? 0) ^ 0x01;
	return out;
}

// ---------------------------------------------------------------- 结果

type Verdict = "PASS" | "FAIL" | "SKIP";
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
/** 本条里条件性跳过的部分（渲染器不可用）；其余断言都过了时整条记 SKIP。 */
let skipped: string[] = [];

const note = (s: string) => {
	evidence.push(s);
	console.log(`    · ${s}`);
};
/** 硬断言：不成立就结束本条。 */
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};
/** 软断言：记下失败继续走，本条结尾判 FAIL。 */
const expect = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) softFails++;
};

let renderOk = false;
let renderReason = "";
/** 渲染器可用时回真；不可用时记下这一部分跳过（计划允许的条件性 SKIP），回假。 */
function canRender(what: string): boolean {
	if (renderOk) return true;
	const why = `控制面渲染器不可用（${renderReason || "capabilities.render = false"}），${what}跳过`;
	note(why);
	skipped.push(why);
	return false;
}

// ---------------------------------------------------------------- 浏览器

let browser: Browser | undefined;
let netns: BrowserNetns | undefined;
const contexts: BrowserContext[] = [];
const pageErrors: string[] = [];
const pageDiag: string[] = [];

/** 浏览器进程命令行里的记号：关闭卡死时按它找到本轮的浏览器进程强制结束（Chromium 忽略不认识的开关）。 */
const BROWSER_MARK = `--oryn-m31o-run=${RUN}`;
const CLOSE_TIMEOUT = 30_000;

async function getBrowser(): Promise<Browser> {
	if (browser) return browser;
	netns ??= await startBrowserNetns({
		name: `oryn-m31o-${RUN}`,
		ports: [UI_PORT, REAL_PORT],
		chromium: CHROMIUM,
	});
	browser = await chromium.launch({
		executablePath: netns.executablePath,
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
			const argv = readFileSync(`/proc/${pid}/cmdline`, "utf8").split("\0");
			if (argv.includes(BROWSER_MARK)) process.kill(Number(pid), "SIGKILL");
		} catch {
			// 已退出
		}
	}
	browser = undefined;
}

/** 顶层页面的本地开关：调试模式、面板宽度、首次发布说明已看过。 */
const INIT_SCRIPT = `(() => {
	if (window.top !== window) return;
	try {
		localStorage.setItem("oryn.debug", "1");
		localStorage.setItem("oryn.panel.width", "760");
		localStorage.setItem("oryn.artifacts.firstPublishSeen", "1");
	} catch {}
})();`;

interface RealAuth {
	token: string;
	session: string;
}

async function openPage(
	o: { width?: number; height?: number; auth?: RealAuth } = {},
): Promise<Page> {
	const b = await getBrowser();
	const ctx = await b.newContext({
		locale: "zh-CN",
		viewport: { width: o.width ?? 1280, height: o.height ?? 900 },
		colorScheme: "light",
		acceptDownloads: true,
	});
	if (o.auth)
		await ctx.addCookies([
			{
				name: "__Host-oryn_session",
				value: o.auth.session,
				domain: new URL(REAL_UI).hostname,
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
	await p.goto(`${REAL_UI}${path}`, { waitUntil: "domcontentloaded" });
}

async function shot(p: Page, name: string): Promise<string> {
	const f = `${SHOTS}/${name}.png`;
	await p.screenshot({ path: f });
	note(`截图 ${f.slice(SHOTS.length + 1)}`);
	return f;
}

/** 等一次下载并返回建议文件名与字节。 */
async function download(
	p: Page,
	act: () => Promise<void>,
): Promise<{ name: string; bytes: Bytes; url: string }> {
	const [dl] = await Promise.all([
		p.waitForEvent("download", { timeout: 60_000 }),
		act(),
	]);
	const path = await dl.path();
	return {
		name: dl.suggestedFilename(),
		bytes: new Uint8Array(readFileSync(path)),
		url: dl.url(),
	};
}

/**
 * 点帧里的元素并确认这次点击真的送进了页面（跨进程的 artifact 子帧在机器忙时可能还没把命中测试数据交给合成器，
 * 鼠标事件落不进帧而 Playwright 照样返回成功）：先在帧里挂捕获阶段的一次性 click 监听，没送达就再点一次。
 */
async function clickInFrame(
	f: Frame,
	selector: string,
	o: { position?: { x: number; y: number } } = {},
) {
	const loc = f.locator(selector).first();
	await loc.scrollIntoViewIfNeeded();
	for (let i = 0; i < 2; i++) {
		await f.evaluate(() => {
			const w = window as unknown as { __m31click?: boolean };
			w.__m31click = false;
			window.addEventListener(
				"click",
				() => {
					w.__m31click = true;
				},
				{ capture: true, once: true },
			);
		});
		await loc.click(o.position ? { position: o.position } : {});
		const got = await waitFor(
			"点击送达帧",
			() =>
				f.evaluate(
					() => (window as unknown as { __m31click?: boolean }).__m31click,
				),
			3_000,
			100,
		).catch(() => false);
		if (got) return;
		note("点击没有送达帧里的页面（子帧的输入路由还没就绪），再点一次");
	}
	throw new Error(`点 ${selector} 两次都没有送达帧里的页面`);
}

/** 点进帧并确认帧的文档拿到焦点（之后的按键才会送到帧里）。 */
async function focusFrame(f: Frame) {
	for (let i = 0; i < 3; i++) {
		await f.locator("body").click({ position: { x: 5, y: 5 } });
		const focused = await waitFor(
			"焦点进入帧",
			() => f.evaluate(() => document.hasFocus()),
			3_000,
			50,
		).catch(() => false);
		if (focused) return;
		note("点进帧后帧没有拿到焦点，再点一次");
	}
	throw new Error("点了三次，帧的文档都没有拿到焦点");
}

/** 切到右面板的指定标签；面板收起时（标签按钮仍在 DOM 里但点不到）先点页头的「信息面板」展开。 */
async function openPanel(p: Page, tab: string) {
	const btn = p.locator(`[data-panel-tab=${tab}]`);
	await btn.waitFor({ state: "attached", timeout: 30_000 });
	const clicked = await btn
		.click({ timeout: 3_000 })
		.then(() => true)
		.catch(() => false);
	if (!clicked) {
		await p
			.getByRole("button", { name: "信息面板", exact: true })
			.first()
			.click();
		await btn.click({ timeout: 15_000 });
	}
	await waitFor(
		`面板切到 ${tab}`,
		async () => (await btn.getAttribute("aria-selected")) === "true",
		5_000,
	);
	await sleep(400);
}

/** 侧栏切到「评论」。 */
async function openComments(p: Page) {
	const tab = p.locator("[data-testid=artifact-side-comments]");
	if (!(await tab.isVisible().catch(() => false)))
		await p.locator("[data-testid=artifact-history-toggle]").first().click();
	await tab.click();
	await sleep(300);
}

/** 打开查看器的更多菜单并点某一项。 */
async function viewerMenu(p: Page, item: string) {
	await p
		.locator("[data-testid=artifact-viewer] [data-testid=artifact-menu]")
		.first()
		.click();
	await p.getByRole("menuitem", { name: item, exact: true }).click();
}

// ================================================================ 真机环境

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

const cli = (args: string) =>
	sh(`${q(`${BIN}/oryn`)} ${args}`, {
		...process.env,
		ORYN_API_URL: REAL_API,
		ORYN_API_TOKEN: auth?.token ?? "",
	});

interface ApiRes {
	status: number;
	text: string;
	json: unknown;
	headers: Headers;
	bytes: Bytes;
}

/** 直接请求 orynd（带设备令牌；`anon` 时不带任何凭据）。 */
async function rapi(
	method: string,
	path: string,
	body?: unknown,
	o: { anon?: boolean; headers?: Record<string, string> } = {},
): Promise<ApiRes> {
	const raw =
		body instanceof FormData ||
		body instanceof URLSearchParams ||
		body instanceof Uint8Array;
	const res = await fetch(`${REAL_API}${path}`, {
		method,
		headers: {
			...(o.anon
				? {}
				: { Authorization: `Bearer ${auth?.token ?? ""}`, "X-Oryn-CSRF": "1" }),
			...(body !== undefined && !raw
				? { "Content-Type": "application/json" }
				: {}),
			...o.headers,
		},
		body:
			body === undefined
				? undefined
				: raw
					? (body as BodyInit)
					: JSON.stringify(body),
		redirect: "manual",
	});
	const bytes = new Uint8Array(await res.arrayBuffer());
	const text = new TextDecoder().decode(bytes);
	let json: unknown = null;
	try {
		json = JSON.parse(text);
	} catch {
		// 非 JSON
	}
	return { status: res.status, text, json, headers: res.headers, bytes };
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

const ORYND_YAML = () => `listen: ["127.0.0.1:${REAL_PORT}"]
public_url: "${REAL_UI}"
portal_domain: ""
data_dir: "${TMP}/data"
log_level: "debug"
idle: { agent_idle: "30m", user_idle: "30m", stop_after_frozen: "24h", wake_on_focus: true }
orb_defaults: { provider: "local", type: "" }
coordinator: { omp_path: "${TMP}/bin/fakeomp", dir: "${COORD_DIR}", egress_port: ${COORD_EGRESS}, verify_omp: false }
artifacts: { render: { chromium: "${CHROMIUM}", user: "nobody", dir: "${TMP}/render" } }
`;

/**
 * 起 vite 开发服务器的模块代码（独立 cacheDir；关掉 HMR 与文件监视：工作区里别的 agent 同时在改 web/src，
 * 热更新会让正在跑的条目丢状态）。
 */
function viteModule(proxy: string): string {
	const dir = `${WEB}/node_modules/.m31o`;
	mkdirSync(dir, { recursive: true });
	const file = `${dir}/vite-real.mjs`;
	writeFileSync(
		file,
		`import { createServer } from "vite";
${proxy}
const s = await createServer({
	configFile: "vite.config.ts",
	cacheDir: "node_modules/.vite-m31o-real",
	optimizeDeps: { entries: ["index.html", "src/**/*.{ts,tsx}", "!src/**/*.test.{ts,tsx}"] },
	server: { port: ${UI_PORT}, strictPort: true, host: "127.0.0.1", hmr: false, watch: { ignored: ["**"] }, proxy },
});
await s.listen();
console.log("ready");
`,
	);
	return file;
}

async function setupReal() {
	if (process.getuid?.() !== 0)
		throw new Error(
			"要以 root 运行（浏览器网络命名空间、协调宿主、渲染器降权）",
		);
	for (const b of ["orynd", "orbd", "oryn", "fakeomp"])
		if (!existsSync(`${BIN}/${b}`))
			throw new Error(
				`缺少 ${BIN}/${b}：先 make web && make build（或设 M31O_BIN）`,
			);
	for (const tool of ["git", "curl", "sqlite3"])
		if (sh(`command -v ${tool}`).code !== 0)
			throw new Error(`本机缺少 ${tool}`);
	mkdirSync(`${TMP}/bin`, { recursive: true });
	mkdirSync(`${TMP}/render`, { recursive: true });
	mkdirSync(`${TMP}/calls`, { recursive: true });
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
	writeFileSync(`${TMP}/orynd.yaml`, ORYND_YAML());
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
	const dev = admin("create-device --name m31o-accept --kind cli");
	if (dev.code !== 0) throw new Error(`create-device：${dev.out}`);
	const ses = admin("create-web-session --user-agent m31o-accept");
	if (ses.code !== 0) throw new Error(`create-web-session：${ses.out}`);
	auth = {
		token: dev.stdout.split("\n").at(-1) ?? "",
		session: ses.stdout.split("\n").at(-1) ?? "",
	};
	// 全新库里补一个 owner 行（正式部署由首次 Passkey 注册创建）
	const own = sh(
		`sqlite3 ${q(DB)} ${q(`INSERT OR IGNORE INTO owner (id, display_name, user_handle, created_at) VALUES (1, 'm31o', randomblob(16), ${Date.now()})`)}`,
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
			ORYN_EGRESS_ADDR: "127.0.0.1:0",
		},
	);
	await waitFor(
		"provider 在线",
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
	const proxyTargets = ["/api", "/healthz", "/portal", "/f/", "/_af/", "/p/"];
	const proxy = `const target = ${JSON.stringify(REAL_API)};
const proxy = Object.fromEntries(${JSON.stringify(proxyTargets)}.map((k) => [k, { target, ws: k === "/api", changeOrigin: true }]));`;
	bg(
		"vite-real",
		["bun", viteModule(proxy)],
		{ ...process.env, VITE_MOCK: "" },
		WEB,
	);
	await waitFor(
		"vite 就绪",
		async () => (await fetch(`http://127.0.0.1:${UI_PORT}`)).ok,
		60_000,
		300,
	);
	type Caps = { render: boolean; renderReason?: string };
	// 渲染自检在 orynd 启动后异步跑，完成前 render 为假：等它有结论再定哪些部分跳过
	let caps = await rjson<Caps>("GET", "/api/v1/artifacts/capabilities");
	caps = await waitFor(
		"渲染自检完成",
		async () => {
			const c = await rjson<Caps>("GET", "/api/v1/artifacts/capabilities");
			caps = c;
			return c.render ? c : null;
		},
		60_000,
		500,
	).catch(() => caps);
	renderOk = caps.render;
	renderReason = caps.renderReason ?? "";
	console.log(
		`  能力：render=${caps.render}${caps.renderReason ? `（${caps.renderReason}）` : ""}`,
	);
	realReady = true;
	// 冷启动的 vite 首次按需编译要很久：先把总览页加载一遍
	const warm = await realPage();
	await goto(warm, "/artifacts");
	await warm
		.locator("[data-testid=artifacts-empty], [data-testid=artifact-card]")
		.first()
		.waitFor({ timeout: 180_000 });
	await closeAll();
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

async function stopReal() {
	for (const b of [...bgs].reverse()) await stopBg(b);
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

const realPage = (o: { width?: number; height?: number } = {}) => {
	if (!auth) throw new Error("真机环境未就绪");
	return openPage({ ...o, auth });
};
/** 无痕访客：没有任何凭据的独立上下文。 */
const anonPage = () => openPage();

// ---- 线程与 orb

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
	projectId?: string;
	cwd?: string;
}
const getThread = (id: string) =>
	rjson<ThreadView>("GET", `/api/v1/threads/${id}`);

interface EntryRow {
	seq: number;
	type: string;
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
async function userMessages(id: string): Promise<string[]> {
	return (await entries(id))
		.filter((e) => e.type === "message" && msgRole(e) === "user")
		.map(msgText);
}
const assistantCount = async (id: string) =>
	(await entries(id)).filter(
		(e) =>
			e.type === "message" && msgRole(e) === "assistant" && msgText(e).trim(),
	).length;

async function waitAssistant(id: string, n: number, timeoutMs = 180_000) {
	return waitFor(
		`线程 ${id} 第 ${n} 条助手回复`,
		async () => {
			const th = await getThread(id);
			if (th.state === "error") throw new Error(`线程 ${id} 停在 error`);
			return (await assistantCount(id)) >= n && th.state === "idle" ? th : null;
		},
		timeoutMs,
		1_000,
	);
}

const orbHome = (orbId: string) => `${TMP}/orbs/orbs/${orbId}/home`;
const ORB_PATH = "/usr/local/bin:/usr/bin:/bin";

let projectId = "";
let T1: Th | undefined;
/** 一条有项目的 orb 线程：artifact 都以它的身份建，文件面板也用它。 */
async function thread(): Promise<Th> {
	if (T1) return T1;
	projectId ||= (
		await rjson<{ id: string }>("POST", "/api/v1/projects", {
			name: `m31o-${RUN}`,
			shallowClone: true,
		})
	).id;
	const r = await rjson<{ thread: { id: string } }>("POST", "/api/v1/threads", {
		projectId,
		prompt: "m31o 真机：Office 线程",
		executor: { kind: "new_orb", provider: "local" },
	});
	const th = await waitAssistant(r.thread.id, 1);
	if (!th.orbId) throw new Error(`线程 ${th.id} 没有 orb`);
	await waitFor(
		"orb 的 orbd.sock",
		() => existsSync(`${orbHome(th.orbId ?? "")}/.oryn/orbd.sock`),
		60_000,
		500,
	);
	T1 = { id: th.id, orbId: th.orbId, title: th.title || th.id };
	return T1;
}

async function threadWorkdir(t: Th): Promise<string> {
	const th = await getThread(t.id);
	if (th.cwd) return th.cwd;
	const roots = await rjson<{ home: string; workdir: string }>(
		"GET",
		`/api/v1/orbs/${t.orbId}/fs/roots`,
	);
	if (!roots.workdir?.startsWith("/"))
		throw new Error(`/fs/roots 没给出工作目录：${JSON.stringify(roots)}`);
	mkdirSync(roots.workdir, { recursive: true });
	return roots.workdir;
}

// ---- artifact 操作（以线程身份在 orb 里跑 `oryn` CLI）

interface CallResult {
	text: string;
	details?: Record<string, unknown>;
}
let callSeq = 0;
/** 在 orb 环境里以线程 t 的身份跑 `oryn <args>`。 */
function orbCli(t: Th, args: string) {
	return sh(
		`cd ${q(orbHome(t.orbId))} && env -i PATH=${ORB_PATH} HOME=${q(orbHome(t.orbId))} ORYN_ORB=1 ORYN_THREAD_ID=${t.id} ${q(`${BIN}/oryn`)} ${args}`,
	);
}
function acall(
	t: Th,
	op: string,
	params: Record<string, unknown>,
): { ok: boolean; out: string; res?: CallResult } {
	const f = `${TMP}/calls/${++callSeq}-${op}.json`;
	writeFileSync(f, JSON.stringify(params));
	const r = orbCli(t, `artifact call ${op} --params-file ${q(f)} --json`);
	if (r.code !== 0) return { ok: false, out: r.out };
	return { ok: true, out: r.out, res: JSON.parse(r.stdout) as CallResult };
}
function art<T = Record<string, unknown>>(
	t: Th,
	op: string,
	params: Record<string, unknown>,
): T {
	const r = acall(t, op, params);
	if (!r.ok || !r.res)
		throw new Error(`${op} 失败：${clip(oneLine(r.out), 300)}`);
	return JSON.parse(r.res.text) as T;
}
function artErr(t: Th, op: string, params: Record<string, unknown>): string {
	const r = acall(t, op, params);
	if (r.ok)
		throw new Error(`${op} 本应失败却成功：${clip(r.res?.text ?? "", 200)}`);
	return oneLine(r.out);
}
/** 在 orb 里写一个文件（给 `source` 用），返回绝对路径。 */
function orbFile(t: Th, name: string, data: string | Uint8Array): string {
	const dir = `${orbHome(t.orbId)}/m31o-src`;
	mkdirSync(dir, { recursive: true });
	writeFileSync(`${dir}/${name}`, data);
	return `${dir}/${name}`;
}

interface Created {
	id: string;
	version: number;
	pages: number;
	kind: string;
}
interface ArtifactView {
	id: string;
	title: string;
	kind: string;
	type?: string;
	version: number;
	pages: number;
	size: number;
	origin: string;
	threadId?: string;
	projectId?: string;
}
interface Detail {
	artifact: ArtifactView;
	version: number;
	head: number;
	files: { path: string; size: number; mime: string; sha256: string }[];
}
interface VersionView {
	version: number;
	authorThreadId?: string;
	authorLabel: string;
}
interface Anchor {
	version: number;
	path: string;
	page: { number: number; name: string } | null;
	offset: number | null;
	tag: string | null;
	text: string;
	missing: boolean;
	x?: number | null;
	y?: number | null;
	range?: string | null;
	block?: number | null;
}
interface CommentThreadView {
	id: string;
	anchor: Anchor;
	sentTo?: string;
	comments: { body: string }[];
}
const detail = (id: string) => rjson<Detail>("GET", `/api/v1/artifacts/${id}`);
const versions = async (id: string) =>
	(
		await rjson<{ versions: VersionView[] }>(
			"GET",
			`/api/v1/artifacts/${id}/versions`,
		)
	).versions;
const comments = async (id: string) =>
	(
		await rjson<{ threads: CommentThreadView[] }>(
			"GET",
			`/api/v1/artifacts/${id}/comments`,
		)
	).threads;

/** 以线程身份用 `source` 登记一个 Office artifact。 */
async function createOffice(
	type: OfficeType,
	title: string,
	bytes: Uint8Array = SAMPLE[type],
): Promise<Created> {
	const t = await thread();
	return art<Created>(t, "create_artifact", {
		title,
		type,
		files: [
			{
				path: ENTRY[type],
				source: orbFile(t, `${RUN}-${++callSeq}.${type}`, bytes),
			},
		],
	});
}

// 各条共用的 artifact（--only 单跑某条时按需创建）
const shared: Partial<Record<OfficeType, { id: string; title: string }>> = {};
async function ensureOffice(
	type: OfficeType,
): Promise<{ id: string; title: string }> {
	const have = shared[type];
	if (have) return have;
	const title = `M31 样本 ${type}`;
	const c = await createOffice(type, title);
	shared[type] = { id: c.id, title };
	return shared[type];
}

// ---- 查看器

interface Rendered {
	f: Frame;
	text: string;
}

/**
 * 等 scope 里的 Office 查看器帧渲染完成：运行时已装上（`window.oryn`）、视图回调了 onReady（`data-ready`）、
 * 视觉隐藏的状态行写出「已渲染 N 页」。出现错误态就立即失败并带上原因。
 */
async function officeFrame(
	p: Page,
	scope = "[data-testid=artifact-frame]",
): Promise<Rendered> {
	return waitFor(
		"Office 查看器渲染完成（已渲染 N 页）",
		async () => {
			const el = await p.locator(scope).first().elementHandle();
			const f = await el?.contentFrame();
			if (!f) return null;
			const st = await f
				.evaluate(() => ({
					ready: document.documentElement.dataset.ready === "true",
					runtime: "oryn" in window,
					rendered:
						/已渲染 \d+ 页/.exec(document.body.textContent ?? "")?.[0] ?? "",
					error:
						document.querySelector(".oryn-o-error")?.textContent?.trim() ?? "",
				}))
				.catch(() => null);
			if (!st) return null;
			if (st.error) throw new Error(`查看器错误态：${st.error}`);
			return st.ready && st.runtime && st.rendered
				? { f, text: st.rendered }
				: null;
		},
		120_000,
		300,
	);
}

/** 打开 `/artifacts/<id>`，等 Office 查看器帧渲染完成。 */
async function officeViewer(id: string, o: { width?: number } = {}) {
	const p = await realPage(o);
	await goto(p, `/artifacts/${id}`);
	await p.locator("[data-testid=artifact-viewer]").waitFor({ timeout: 60_000 });
	const r = await officeFrame(p);
	return { p, ...r };
}

/** 等帧里出现 ready 选择器且运行时已装上（文档 / 幻灯片查看器）。 */
async function artifactFrame(p: Page, ready: string): Promise<Frame> {
	return waitFor(
		"artifact 帧加载",
		async () => {
			const el = await p
				.locator("[data-testid=artifact-frame]")
				.first()
				.elementHandle();
			const f = await el?.contentFrame();
			if (!f) return null;
			const ok =
				(await f
					.locator(ready)
					.first()
					.isVisible()
					.catch(() => false)) &&
				(await f
					.evaluate(
						() => document.readyState === "complete" && "oryn" in window,
					)
					.catch(() => false));
			return ok ? f : null;
		},
		60_000,
		300,
	);
}

async function commentMode(p: Page) {
	const mode = p.locator("[data-testid=artifact-mode-comment]").first();
	await mode.waitFor({ timeout: 15_000 });
	if ((await mode.getAttribute("aria-pressed")) !== "true") await mode.click();
	await waitFor(
		"进入评论模式",
		async () => (await mode.getAttribute("aria-pressed")) === "true",
		5_000,
	);
	await sleep(300);
}

/** 评论框已弹出：写字并提交，等新评论串出现在接口里，返回它。 */
async function submitComment(
	p: Page,
	id: string,
	text: string,
): Promise<CommentThreadView> {
	const before = (await comments(id)).length;
	const box = p.locator("[data-testid=comment-box]");
	await box.waitFor({ timeout: 10_000 });
	await box.locator("textarea").fill(text);
	await p.locator("[data-testid=comment-submit]").click();
	await box.waitFor({ state: "hidden", timeout: 10_000 });
	return waitFor(
		"新评论串",
		async () => {
			const list = await comments(id);
			return list.length > before
				? list.find((x) => x.comments[0]?.body === text)
				: null;
		},
		10_000,
	);
}

// ================================================================ 场景

/** K1：orb 里用 source 登记 docx → kind=document、type=docx、pages=1；查看器帧渲染完成；真实渲染管线出缩略图。 */
async function realK1() {
	const { id } = await ensureOffice("docx");
	const d = await detail(id);
	check(
		d.artifact.kind === "document" &&
			d.artifact.type === "docx" &&
			d.artifact.pages === 1,
		`详情 kind=${d.artifact.kind} type=${d.artifact.type} pages=${d.artifact.pages}`,
	);
	expect(
		d.files.length === 1 &&
			d.files[0]?.path === "document.docx" &&
			d.files[0]?.sha256 === sha256(SAMPLE.docx),
		`版本里只有 document.docx 且 sha256 等于样本（${d.files.map((f) => f.path).join(",")}）`,
	);
	const { p, text } = await officeViewer(id);
	note(`查看器帧：${text}`);
	await shot(p, "k1-docx-viewer");
	if (!canRender("缩略图")) return;
	const thumb = await waitFor(
		"缩略图",
		async () => {
			const r = await rapi("GET", `/api/v1/artifacts/${id}/thumbnail`);
			return r.status === 200 ? r : null;
		},
		120_000,
		1_000,
	);
	const size = jpegSize(thumb.bytes);
	writeFileSync(`${SHOTS}/k1-thumbnail.jpg`, thumb.bytes);
	check(
		thumb.headers.get("content-type") === "image/jpeg" && !!size,
		`GET …/thumbnail 200 ${thumb.headers.get("content-type")} ${size?.w}×${size?.h}（存为 k1-thumbnail.jpg）`,
	);
}

/** K2：xlsx → kind=sheet、pages=2（两个工作表）；Artifacts 页「表格」标签只列出它。 */
async function realK2() {
	const { id } = await ensureOffice("xlsx");
	const d = await detail(id);
	check(
		d.artifact.kind === "sheet" &&
			d.artifact.type === "xlsx" &&
			d.artifact.pages === 2,
		`详情 kind=${d.artifact.kind} type=${d.artifact.type} pages=${d.artifact.pages}`,
	);
	const { p, text } = await officeViewer(id);
	note(`查看器帧：${text}`);
	await shot(p, "k2-xlsx-viewer");
	await goto(p, "/artifacts");
	const tab = p.locator("button[data-kind=sheet]");
	await tab.waitFor({ timeout: 30_000 });
	expect(
		((await tab.textContent()) ?? "").trim() === "表格",
		"分类标签「表格」",
	);
	await tab.click();
	const cards = p.locator("[data-testid=artifact-card]");
	await cards.first().waitFor({ timeout: 15_000 });
	await sleep(800);
	const ids = await cards.evaluateAll((els) =>
		els.map((e) => e.getAttribute("data-artifact-id") ?? ""),
	);
	const all = (
		await rjson<{ artifacts: ArtifactView[] }>("GET", "/api/v1/artifacts")
	).artifacts;
	note(`全部 artifact ${all.length} 个：${all.map((a) => a.kind).join(",")}`);
	expect(
		ids.length === 1 && ids[0] === id,
		`「表格」标签只列出这个 xlsx（${ids.join(",")}）`,
	);
	await shot(p, "k2-sheet-tab");
}

/** K3：pptx → kind=slides、pages=3。 */
async function realK3() {
	const { id } = await ensureOffice("pptx");
	const d = await detail(id);
	check(
		d.artifact.kind === "slides" &&
			d.artifact.type === "pptx" &&
			d.artifact.pages === 3,
		`详情 kind=${d.artifact.kind} type=${d.artifact.type} pages=${d.artifact.pages}`,
	);
	const { p, text } = await officeViewer(id);
	expect(text === "已渲染 3 页", `查看器帧：${text}`);
	await shot(p, "k3-pptx-viewer");
}

/** K4：pdf → kind=document、type=pdf；帧里渲染出第 1 页。 */
async function realK4() {
	const { id } = await ensureOffice("pdf");
	const d = await detail(id);
	check(
		d.artifact.kind === "document" && d.artifact.type === "pdf",
		`详情 kind=${d.artifact.kind} type=${d.artifact.type} pages=${d.artifact.pages}`,
	);
	const { p, f, text } = await officeViewer(id);
	note(`查看器帧：${text}`);
	const page1 = await waitFor(
		"第 1 页的画布画出内容",
		() =>
			f.evaluate(() => {
				const c = document.querySelector<HTMLCanvasElement>(
					'.oryn-o-page[data-page="1"] canvas',
				);
				return c && c.width > 0 && c.height > 0
					? { w: c.width, h: c.height }
					: null;
			}),
		30_000,
	);
	expect(true, `第 1 页画布 ${page1.w}×${page1.h}`);
	await shot(p, "k4-pdf-viewer");
}

/** K5：类型与内容不符、多给文件、给 content 都回 invalid，文案指明正确写法。 */
async function realK5() {
	const t = await thread();
	const mismatch = artErr(t, "create_artifact", {
		title: "K5 错类型",
		type: "docx",
		files: [
			{
				path: "document.docx",
				source: orbFile(t, "k5-mismatch.docx", SAMPLE.xlsx),
			},
		],
	});
	expect(
		mismatch.includes("invalid") &&
			mismatch.includes("内容是 Excel 表格（xlsx）"),
		`xlsx 当 docx 传 → ${clip(mismatch, 200)}`,
	);
	const two = artErr(t, "create_artifact", {
		title: "K5 两个文件",
		type: "docx",
		files: [
			{ path: "document.docx", source: orbFile(t, "k5-two.docx", SAMPLE.docx) },
			{ path: "notes.txt", content: "多出来的文件" },
		],
	});
	expect(
		two.includes("invalid") && two.includes("只能有 document.docx 这一个文件"),
		`多给一个文件 → ${clip(two, 200)}`,
	);
	const content = artErr(t, "create_artifact", {
		title: "K5 content",
		type: "docx",
		files: [{ path: "document.docx", content: "不是二进制" }],
	});
	expect(content.includes("invalid"), `给 content → ${clip(content, 200)}`);
}

/** K6：`oryn artifact pull` 取回字节一致；残片存在时拒绝且不动残片；read_artifact 的二进制头；source 更新出版本 2。 */
async function realK6() {
	const t = await thread();
	const c = await createOffice("docx", "K6 取回");
	const dir = `${orbHome(t.orbId)}/m31o-pull`;
	mkdirSync(dir, { recursive: true });
	const to = `${dir}/out.docx`;
	const r = orbCli(t, `artifact pull ${c.id} --to ${q(to)} --json`);
	check(
		r.code === 0,
		`oryn artifact pull 退出 ${r.code}：${clip(oneLine(r.out), 200)}`,
	);
	const res = JSON.parse(r.stdout) as {
		version: number;
		path: string;
		size: number;
		sha256: string;
		to: string;
	};
	const got = new Uint8Array(readFileSync(to));
	check(
		sha256(got) === sha256(SAMPLE.docx) &&
			res.sha256 === sha256(SAMPLE.docx) &&
			res.size === SAMPLE.docx.length &&
			res.path === "document.docx" &&
			res.version === 1,
		`取回的文件 sha256 ${sha256(got).slice(0, 16)}… 等于样本（${res.size} 字节，v${res.version}）`,
	);
	expect(!existsSync(`${to}.oryn-part`), "取回完成后不留 .oryn-part");
	const again = orbCli(t, `artifact pull ${c.id} --to ${q(to)}`);
	expect(
		again.code !== 0 && again.out.includes("--overwrite"),
		`目标已存在且没给 --overwrite → 拒绝：${clip(oneLine(again.out), 120)}`,
	);
	// 残片：另一次取回正在写或上次中断留下的，不能覆盖也不能删
	const to2 = `${dir}/busy.docx`;
	const residue = "上次中断留下的残片";
	writeFileSync(`${to2}.oryn-part`, residue);
	const busy = orbCli(t, `artifact pull ${c.id} --to ${q(to2)}`);
	expect(
		busy.code !== 0 && busy.out.includes(".oryn-part"),
		`已有 ${to2.split("/").pop()}.oryn-part → 取回失败：${clip(oneLine(busy.out), 160)}`,
	);
	expect(
		readFileSync(`${to2}.oryn-part`, "utf8") === residue && !existsSync(to2),
		"残片原样保留，目标文件没有生成",
	);
	const read = acall(t, "read_artifact", { id: c.id });
	const head = (read.res?.text ?? "").split("\n")[0] ?? "";
	let meta: Record<string, unknown> = {};
	try {
		meta = JSON.parse(head) as Record<string, unknown>;
	} catch {
		// 下面的断言会失败
	}
	expect(
		read.ok &&
			meta.binary === true &&
			meta.hint ===
				"这是二进制 Office 文件；用 pull_artifact 取回到本机后再处理。",
		`read_artifact 头部：${clip(head, 300)}`,
	);
	const changed = touchZip(SAMPLE.docx);
	const upd = art<{ version: number }>(t, "update_artifact", {
		id: c.id,
		baseVersion: 1,
		files: [
			{ path: "document.docx", source: orbFile(t, "k6-changed.docx", changed) },
		],
	});
	check(
		upd.version === 2,
		`改一个字节后 update_artifact（baseVersion 1）→ 版本 ${upd.version}`,
	);
	const to3 = `${dir}/v2.docx`;
	const v2 = orbCli(t, `artifact pull ${c.id} --to ${q(to3)} --json`);
	expect(
		v2.code === 0 &&
			sha256(new Uint8Array(readFileSync(to3))) === sha256(changed),
		"取回的最新版本等于改过的文件",
	);
}

/** K7：REST 导入 `季度报告.docx` → origin=import、type=docx、title=季度报告；导入 `old.doc` → 400 unsupported_format。 */
async function realK7() {
	const fd = new FormData();
	fd.append("季度报告.docx", new Blob([SAMPLE.docx]), "季度报告.docx");
	const r = await rapi("POST", "/api/v1/artifacts/import", fd);
	const a = r.json as ArtifactView;
	check(
		r.status < 300 &&
			a.origin === "import" &&
			a.type === "docx" &&
			a.kind === "document" &&
			a.title === "季度报告",
		`导入 季度报告.docx → ${r.status} origin=${a.origin} type=${a.type} kind=${a.kind} title=${a.title}`,
	);
	const d = await detail(a.id);
	expect(
		d.files.length === 1 &&
			d.files[0]?.path === "document.docx" &&
			d.files[0]?.sha256 === sha256(SAMPLE.docx),
		"入口改名为 document.docx，字节不变",
	);
	const old = new FormData();
	old.append(
		"old.doc",
		new Blob([
			new Uint8Array([0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1]),
		]),
		"old.doc",
	);
	const bad = await rapi("POST", "/api/v1/artifacts/import", old);
	const code = isRecord(bad.json) ? bad.json.code : undefined;
	expect(
		bad.status === 400 && code === "unsupported_format",
		`导入 old.doc → ${bad.status} ${String(code)}：${clip(bad.text, 160)}`,
	);
}

/** K8：上传新版本（REST 与查看器菜单）→ 版本 +1、作者是用户；陈旧 baseVersion → 409。 */
async function realK8() {
	const c = await createOffice("docx", "K8 上传新版本");
	const v2 = touchZip(SAMPLE.docx);
	const up = await rapi(
		"POST",
		`/api/v1/artifacts/${c.id}/upload?baseVersion=1`,
		v2,
		{ headers: { "Content-Type": "application/octet-stream" } },
	);
	const a = up.json as ArtifactView;
	check(
		up.status === 200 && a.version === 2,
		`upload?baseVersion=1 → ${up.status} v${a.version}`,
	);
	const vs = await versions(c.id);
	expect(
		vs[0]?.version === 2 &&
			vs[0]?.authorThreadId === undefined &&
			vs[1]?.authorThreadId !== undefined,
		`v2 作者是用户（authorThreadId=${vs[0]?.authorThreadId ?? "无"}；v1 是线程 ${vs[1]?.authorThreadId}）`,
	);
	const stale = await rapi(
		"POST",
		`/api/v1/artifacts/${c.id}/upload?baseVersion=1`,
		SAMPLE.docx,
		{ headers: { "Content-Type": "application/octet-stream" } },
	);
	expect(stale.status === 409, `旧 baseVersion 再传 → ${stale.status}`);
	// 查看器菜单「上传新版本」：选文件 → 新版本
	const v3 = touchZip(v2);
	writeFileSync(`${TMP}/k8-v3.docx`, v3);
	const { p } = await officeViewer(c.id);
	const [chooser] = await Promise.all([
		p.waitForEvent("filechooser", { timeout: 15_000 }),
		viewerMenu(p, "上传新版本"),
	]);
	expect(
		(await chooser.element().getAttribute("accept")) === ".docx",
		`文件选择只接受 .docx（accept=${await chooser.element().getAttribute("accept")}）`,
	);
	await chooser.setFiles(`${TMP}/k8-v3.docx`);
	await waitFor(
		"菜单上传出 v3",
		async () => (await detail(c.id)).head === 3,
		30_000,
	);
	const d = await detail(c.id);
	expect(
		d.files[0]?.sha256 === sha256(v3) &&
			(await versions(c.id))[0]?.authorThreadId === undefined,
		"v3 是选中的文件，作者是用户",
	);
	await officeFrame(p);
	note("查看器换到 v3 后重新渲染完成");
	await shot(p, "k8-uploaded");
}

/** K9：导出原件的字节与文件名；docx 的 PDF 经真实渲染管线；xlsx 的 PDF 需要引擎。 */
async function realK9() {
	const docx = await ensureOffice("docx");
	const xlsx = await ensureOffice("xlsx");
	const orig = await rapi(
		"GET",
		`/api/v1/artifacts/${docx.id}/export?format=original`,
	);
	const name = dispositionName(orig.headers.get("content-disposition"));
	check(
		orig.status === 200 && sameBytes(orig.bytes, SAMPLE.docx),
		`format=original → ${orig.status}，${orig.bytes.length} 字节与原件一致`,
	);
	expect(name === `${docx.title}.docx`, `文件名 ${name}`);
	expect(
		orig.headers.get("content-type") ===
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		`Content-Type ${orig.headers.get("content-type")}`,
	);
	const xpdf = await rapi(
		"GET",
		`/api/v1/artifacts/${xlsx.id}/export?format=pdf`,
	);
	const msg = isRecord(xpdf.json) ? String(xpdf.json.message ?? "") : xpdf.text;
	expect(
		xpdf.status === 400 && msg.includes("需要 Office 引擎"),
		`xlsx 的 format=pdf → ${xpdf.status}：${clip(msg, 120)}`,
	);
	if (!canRender("docx 的 PDF 导出")) return;
	const pdf = await rapi(
		"GET",
		`/api/v1/artifacts/${docx.id}/export?format=pdf`,
	);
	writeFileSync(`${SHOTS}/k9-docx.pdf`, pdf.bytes);
	const head = Buffer.from(pdf.bytes.subarray(0, 5)).toString("latin1");
	check(
		pdf.status === 200 && head === "%PDF-",
		`docx 的 format=pdf → ${pdf.status}，开头 ${JSON.stringify(head)}（存为 k9-docx.pdf）`,
	);
	const pages = pdfPages(pdf.bytes);
	expect(pages >= 2, `PDF ${pages} 页（样本有第 2 页的「附录」）`);
	expect(
		dispositionName(pdf.headers.get("content-disposition")) ===
			`${docx.title}.pdf`,
		`PDF 文件名 ${dispositionName(pdf.headers.get("content-disposition"))}`,
	);
	if (sh("command -v pdftoppm").code === 0) {
		sh(
			`pdftoppm -png -r 40 ${q(`${SHOTS}/k9-docx.pdf`)} ${q(`${SHOTS}/k9-docx-page`)}`,
		);
		note("PDF 逐页转成 k9-docx-page-*.png 供人工核对");
	}
}

/** 线程页的文件面板：打开 `/t/<id>` 并切到「文件」，等某个文件出现在树里。 */
async function filesPanel(t: Th, name: string) {
	const p = await realPage({ width: 1440, height: 900 });
	await goto(p, `/t/${t.id}`);
	await openPanel(p, "files");
	const row = p.locator(`[role="treeitem"][data-path$="/${name}"]`).first();
	await row.waitFor({ timeout: 30_000 });
	return { p, row };
}

/** K10：文件面板里对 orb 的 report.xlsx 点「转为 artifact」→ 提示；新 artifact 的 threadId 是当前线程。 */
async function realK10() {
	const t = await thread();
	const wd = await threadWorkdir(t);
	writeFileSync(`${wd}/report.xlsx`, SAMPLE.xlsx);
	const { p, row } = await filesPanel(t, "report.xlsx");
	await row.hover();
	await row
		.getByRole("button", { name: "report.xlsx 的操作", exact: true })
		.click();
	await p.getByRole("menuitem", { name: "转为 artifact", exact: true }).click();
	const toast = p.getByText("已转为 artifact“report”");
	await toast.waitFor({ timeout: 30_000 });
	note("提示「已转为 artifact“report”」");
	await shot(p, "k10-to-artifact");
	const all = (
		await rjson<{ artifacts: ArtifactView[] }>("GET", "/api/v1/artifacts")
	).artifacts;
	const a = all.find((x) => x.title === "report" && x.type === "xlsx");
	check(
		!!a,
		`新 artifact：${a ? `${a.id} type=${a.type} kind=${a.kind}` : "没找到"}`,
	);
	if (!a) return;
	expect(a.threadId === t.id, `threadId ${a.threadId}（当前线程 ${t.id}）`);
	expect(a.projectId === projectId, `projectId ${a.projectId}`);
	expect(
		a.origin === "import" && a.kind === "sheet",
		`origin=${a.origin} kind=${a.kind}`,
	);
	const d = await detail(a.id);
	expect(d.files[0]?.sha256 === sha256(SAMPLE.xlsx), "字节等于 orb 里的文件");
	await p.getByRole("button", { name: "打开", exact: true }).first().click();
	await p.locator("[data-testid=artifact-viewer]").waitFor({ timeout: 30_000 });
	note("提示里的「打开」打开了查看器");
}

/** K11：docx 评论模式点第 1 页中部 → 锚点 page=1、x/y 在 (0,1)；帧里有图钉 1；发给线程的文本与 agent 读到的 JSON。 */
async function realK11() {
	const t = await thread();
	const { id } = await ensureOffice("docx");
	const { p, f } = await officeViewer(id);
	await commentMode(p);
	const pageSel = '.oryn-o-page[data-page="1"]';
	const box = await f.locator(pageSel).boundingBox();
	check(
		!!box,
		`第 1 页 ${box ? `${Math.round(box.width)}×${Math.round(box.height)}` : "不在"}`,
	);
	await clickInFrame(f, `${pageSel} .oryn-pick`);
	const th = await submitComment(p, id, "第 1 页中部要改");
	const a = th.anchor;
	check(
		a.page?.number === 1 &&
			typeof a.x === "number" &&
			typeof a.y === "number" &&
			a.x > 0 &&
			a.x < 1 &&
			a.y > 0 &&
			a.y < 1 &&
			a.path === "document.docx",
		`锚点 ${JSON.stringify(a)}`,
	);
	expect(a.offset === null && a.tag === null, "没有 HTML 的 offset / tag");
	const pin = f.locator(".oryn-pin", { hasText: /^1$/ });
	await pin.first().waitFor({ timeout: 15_000 });
	note("帧里出现编号 1 的图钉");
	await shot(p, "k11-docx-pin");
	await openComments(p);
	const send = p.locator("[data-testid=comments-send]");
	await send.waitFor({ timeout: 10_000 });
	// 发给哪个线程：缺省是 artifact 的创建线程（就是 t），下面以线程实际收到消息为准
	const target = send.getByLabel("发给哪个线程");
	note(`发给：${oneLine((await target.textContent()) ?? "")}`);
	const before = (await userMessages(t.id)).length;
	await send.getByRole("button", { name: "发送" }).click();
	const msg = await waitFor(
		"线程收到评论消息",
		async () =>
			(await userMessages(t.id)).slice(before).find((m) => m.includes(id)),
		30_000,
	);
	note(`消息：${clip(oneLine(msg), 300)}`);
	expect(
		msg.includes("page 1") && msg.includes("at x"),
		"发给线程的文本含 page 1 与 at x",
	);
	const r = acall(t, "read_artifact_comments", { id });
	const text = r.res?.text ?? "";
	const json = text.slice(text.indexOf("\n") + 1);
	expect(
		r.ok && json.includes('"point"'),
		`read_artifact_comments：${clip(oneLine(json), 300)}`,
	);
	let point: unknown;
	try {
		const body = JSON.parse(json) as {
			threads: { thread: string; point?: unknown; page?: unknown }[];
		};
		point = body.threads.find((x) => x.thread === th.id)?.point;
	} catch {
		point = undefined;
	}
	expect(
		isRecord(point) &&
			typeof point.x === "number" &&
			typeof point.y === "number",
		`point ${JSON.stringify(point)}`,
	);
}

/** 工作表里一个单元格在网格画布上的中心（CSS px，相对画布左上角），按 sample.xlsx 的列宽与行高推算。 */
function cellCenter(col: "B" | "C", row: 2 | 3): { x: number; y: number } {
	// 列宽（字符）：A 12、B 16、C 10；Calibri 11 的最大数字宽 7px → 像素 = 宽 × 7 + 5。行 1 高 24pt，其余 15pt。
	const px = (w: number) => Math.trunc(w * 7 + 5);
	const left = { B: 50 + px(12), C: 50 + px(12) + px(16) }[col];
	const width = { B: px(16), C: px(10) }[col];
	const top = 22 + 32 + (row - 2) * 20;
	return { x: left + width / 2, y: top + 10 };
}

/** K12：xlsx 选中 B2:C3 评论 → range=B2:C3；Markdown 第 2 段 → block=1；JSON 幻灯片第 2 张 → page.number=2。 */
async function realK12() {
	const t = await thread();
	// xlsx
	const { id } = await ensureOffice("xlsx");
	const { p, f } = await officeViewer(id);
	await commentMode(p);
	const grid = f.locator(".oryn-o-sheet-host canvas").first();
	const g = await grid.boundingBox();
	check(
		!!g,
		`网格画布 ${g ? `${Math.round(g.width)}×${Math.round(g.height)}` : "不在"}`,
	);
	if (!g) return;
	const from = cellCenter("B", 2);
	const to = cellCenter("C", 3);
	await p.mouse.move(g.x + from.x, g.y + from.y);
	await p.mouse.down();
	await p.mouse.move(g.x + (from.x + to.x) / 2, g.y + (from.y + to.y) / 2, {
		steps: 4,
	});
	await p.mouse.move(g.x + to.x, g.y + to.y, { steps: 4 });
	await p.mouse.up();
	const sheet = await submitComment(p, id, "这块区域核对一下");
	check(
		sheet.anchor.range === "B2:C3" &&
			sheet.anchor.page?.number === 1 &&
			sheet.anchor.page?.name === "汇总" &&
			sheet.anchor.x == null &&
			sheet.anchor.y == null,
		`xlsx 锚点 ${JSON.stringify(sheet.anchor)}`,
	);
	await f
		.locator(".oryn-pin", { hasText: /^1$/ })
		.first()
		.waitFor({ timeout: 15_000 });
	note("网格上出现编号 1 的图钉");
	await shot(p, "k12-xlsx-range");
	await closeAll();

	// Markdown 文档
	const md = "第一段文字。\n\n第二段文字，评论这一段。\n\n第三段文字。\n";
	const doc = art<Created>(t, "create_artifact", {
		title: "K12 文档",
		type: "document",
		files: [{ path: "document.md", content: md }],
	});
	const dp = await realPage();
	await goto(dp, `/artifacts/${doc.id}`);
	const df = await artifactFrame(dp, "article.oryn-doc p");
	await commentMode(dp);
	await clickInFrame(
		df,
		".oryn-block-comment:has-text('第二段文字') .oryn-block-hit",
	);
	const block = await submitComment(dp, doc.id, "第二段要展开");
	expect(
		block.anchor.block === 1 &&
			block.anchor.page === null &&
			block.anchor.path === "document.md" &&
			block.anchor.text.startsWith("第二段文字"),
		`文档锚点 ${JSON.stringify(block.anchor)}`,
	);
	await shot(dp, "k12-document-block");
	await closeAll();

	// JSON 幻灯片
	const deck = {
		title: "K12 幻灯片",
		theme: "light",
		aspect: "16:9",
		slides: [
			{ layout: "title", title: "第一张" },
			{ layout: "bullets", title: "第二张", bullets: ["要点甲", "要点乙"] },
			{ layout: "title", title: "第三张" },
		],
	};
	const sl = art<Created>(t, "create_artifact", {
		title: "K12 幻灯片",
		type: "slides",
		files: [
			{ path: "slides.json", content: `${JSON.stringify(deck, null, 2)}\n` },
		],
	});
	const sp = await realPage();
	await goto(sp, `/artifacts/${sl.id}`);
	const sf = await artifactFrame(sp, "body");
	const counter = async () =>
		oneLine((await sf.locator("body").textContent()) ?? "").match(
			/(\d+) \/ 3/,
		)?.[1];
	await waitFor("1 / 3", async () => (await counter()) === "1", 15_000);
	await focusFrame(sf);
	await sp.keyboard.press("ArrowRight");
	await waitFor("2 / 3", async () => (await counter()) === "2", 5_000);
	await commentMode(sp);
	await clickInFrame(sf, ".oryn-pick");
	const slide = await submitComment(sp, sl.id, "第二张加一张图");
	expect(
		slide.anchor.page?.number === 2 &&
			slide.anchor.page?.name === "第二张" &&
			typeof slide.anchor.x === "number" &&
			typeof slide.anchor.y === "number",
		`幻灯片锚点 ${JSON.stringify(slide.anchor)}`,
	);
	await sf
		.locator(".oryn-pin", { hasText: /^1$/ })
		.first()
		.waitFor({ timeout: 15_000 });
	note("第二张上出现编号 1 的图钉");
	await shot(sp, "k12-slides-page");
}

/**
 * 匿名访客打开公开的分享页，等渲染完成后点页内「下载原件」（在哪个帧里就在哪个帧里点），用浏览器的下载事件取回文件。
 * 返回下载的文件名与字节。
 */
async function anonDownload(url: string, shotName: string) {
	const p = await anonPage();
	await p.goto(url, { waitUntil: "domcontentloaded" });
	const frame = await waitFor(
		"分享页渲染完成（已渲染 N 页）",
		async () => {
			for (const f of p.frames()) {
				const ok = await f
					.evaluate(
						() =>
							document.documentElement.dataset.ready === "true" &&
							/已渲染 \d+ 页/.test(document.body.textContent ?? ""),
					)
					.catch(() => false);
				if (ok) return f;
			}
			return null;
		},
		120_000,
		300,
	);
	note(
		`分享页渲染完成（${frame === p.mainFrame() ? "顶层页面" : `帧 ${frame.url()}`}）`,
	);
	const link = frame.getByRole("link", { name: "下载原件", exact: true });
	await link.waitFor({ timeout: 15_000 });
	note(
		`下载链接 href=${await link.getAttribute("href")} download=${await link.getAttribute("download")}`,
	);
	await shot(p, shotName);
	const dl = await download(p, () => link.click());
	note(
		`下载 ${dl.name}（${dl.bytes.length} 字节，${dl.url.replace(REAL_UI, "")}）`,
	);
	return dl;
}

/** K13：docx 发布为公开 → 匿名浏览器看到渲染完成；点「下载原件」取回的字节与原件一致、文件名是标题；PDF 同样。 */
async function realK13() {
	for (const type of ["docx", "pdf"] as const) {
		const { id, title } = await ensureOffice(type);
		const pub = await rjson<{
			publication: { url: string; access: string } | null;
		}>("PUT", `/api/v1/artifacts/${id}/publication`, { access: "public" });
		const url = pub.publication?.url ?? "";
		check(
			pub.publication?.access === "public" && !!url,
			`${type} 发布为公开：${url}`,
		);
		const dl = await anonDownload(url, `k13-${type}-public`);
		expect(
			sameBytes(dl.bytes, SAMPLE[type]),
			`${type}：下载的字节与原件一致（sha256 ${sha256(dl.bytes).slice(0, 16)}…）`,
		);
		expect(dl.name === `${title}.${type}`, `${type}：建议文件名 ${dl.name}`);
		await closeAll();
	}
}

/** K14：文件面板预览 orb 里的 sample.xlsx → 表头填充色与图表可见（截图留在 .dev/m31-office/）。 */
async function realK14() {
	const t = await thread();
	const wd = await threadWorkdir(t);
	writeFileSync(`${wd}/sample.xlsx`, SAMPLE.xlsx);
	const { p, row } = await filesPanel(t, "sample.xlsx");
	await row.click();
	// 图表锚在 F2：把面板最大化，让预览宽到能看见它
	await p
		.getByRole("button", { name: "面板最大化", exact: true })
		.first()
		.click();
	const view = p.locator(".oryn-o-xlsx").first();
	await view.waitFor({ timeout: 60_000 });
	await waitFor(
		"文件面板的表格预览渲染完成",
		async () => /已渲染 \d+ 页/.test((await view.textContent()) ?? ""),
		60_000,
	);
	await sleep(1_000);
	const file = await shot(p, "k14-files-xlsx");
	// 在画布像素里找表头填充色 #FDE68A 与柱状图的柱色（主题强调色 1 #4472C4 一带的蓝）
	const px = await view.evaluate((root) => {
		let fill = 0;
		let blue = 0;
		for (const c of root.querySelectorAll("canvas")) {
			if (c.width === 0 || c.height === 0) continue;
			const ctx = c.getContext("2d");
			if (!ctx) continue;
			const d = ctx.getImageData(0, 0, c.width, c.height).data;
			for (let i = 0; i < d.length; i += 4) {
				const r = d[i] ?? 0;
				const g = d[i + 1] ?? 0;
				const b = d[i + 2] ?? 0;
				if (
					Math.abs(r - 0xfd) < 10 &&
					Math.abs(g - 0xe6) < 10 &&
					Math.abs(b - 0x8a) < 14
				)
					fill++;
				if (b > 150 && b - r > 60 && b - g > 20) blue++;
			}
		}
		return { fill, blue };
	});
	expect(px.fill > 500, `表头填充色像素 ${px.fill}`);
	expect(px.blue > 500, `图表柱色像素 ${px.blue}`);
	note(`人工核对截图 ${file.slice(SHOTS.length + 1)}`);
}

// ================================================================ 执行

const PLAN: Record<string, () => Promise<void>> = {
	K1: realK1,
	K2: realK2,
	K3: realK3,
	K4: realK4,
	K5: realK5,
	K6: realK6,
	K7: realK7,
	K8: realK8,
	K9: realK9,
	K10: realK10,
	K11: realK11,
	K12: realK12,
	K13: realK13,
	K14: realK14,
};

async function runOne(k: string, fn: () => Promise<void>) {
	evidence = [];
	softFails = 0;
	skipped = [];
	const errs0 = pageErrors.length;
	const diag0 = pageDiag.length;
	console.log(`── ${k} [real]`);
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
		if (verdict === "PASS") verdict = "FAIL";
	}
	if (verdict === "PASS" && skipped.length) {
		verdict = "SKIP";
		reason = skipped.join("；");
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
		`${k} [real] ${verdict} ${seconds}s${reason ? `  ${clip(reason, 200)}` : ""}`,
	);
	outcomes.push({ k, verdict, reason, seconds, evidence });
}

async function cleanup() {
	await closeAll();
	if (browser && !(await within(browser.close(), CLOSE_TIMEOUT))) {
		console.log(`关闭浏览器超过 ${CLOSE_TIMEOUT / 1000}s，强制结束`);
		killBrowser();
	}
	netns?.stop();
	await stopReal();
}
for (const sig of ["SIGINT", "SIGTERM"] as const)
	process.on(sig, () => {
		void cleanup().then(() => process.exit(130));
	});

const todo = KS.filter((k) => !ONLY || ONLY.has(k));
console.log(`M31 Office 验收 run=${RUN} shots=${SHOTS} 条目=${todo.join(",")}`);
const started = Date.now();
try {
	try {
		await setupReal();
	} catch (e) {
		realSetupError = e instanceof Error ? oneLine(e.message) : String(e);
		console.log(`真机环境搭建失败：${realSetupError}`);
	}
	for (const k of todo) {
		if (!realReady) {
			outcomes.push({
				k,
				verdict: "FAIL",
				reason: `真机环境搭建失败：${realSetupError}`,
				seconds: 0,
				evidence: [],
			});
			console.log(`${k} [real] FAIL（真机环境搭建失败）`);
			continue;
		}
		await runOne(k, PLAN[k] as () => Promise<void>);
	}
} finally {
	await cleanup();
}

console.log("\n== 汇总");
for (const o of outcomes)
	console.log(
		`${o.k} [real] ${o.verdict}${o.reason ? `  ${clip(o.reason, 160)}` : ""}`,
	);
writeFileSync(
	`${SHOTS}/results.json`,
	JSON.stringify({ run: RUN, outcomes }, null, 2),
);
console.log(
	`\n结果：${SHOTS}/results.json；总耗时 ${((Date.now() - started) / 1000).toFixed(0)}s`,
);
// 计划允许的条件性 SKIP（渲染器不可用）不算失败
process.exit(outcomes.every((o) => o.verdict !== "FAIL") ? 0 : 1);
