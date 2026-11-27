#!/usr/bin/env bun
/**
 * M32 Office 引擎层端到端验收（计划 local://office-artifacts-plan.md 步骤 13、「Verification」的 E1–E8）。
 * 只有真机部分：每条打印 `E<n> [real] PASS|FAIL|SKIP`，截图、取回的文件与日志存进 `.dev/m32-office/`，最后按 E 编号汇总；
 * 全部 PASS 时退出码 0。本机没有可用的 docker（或拉不到 collabora/code）时计划允许全部 SKIP，退出码 0。
 *
 * 用法（web/ 目录）：bun run accept:m32-office-engine [-- --only E1,E3] [-- --help]
 *
 * 环境（全部在 /var/tmp/oryn-m32o-<run>/ 下，结束时删除；M32O_KEEP=1 保留）：
 *   - Office 引擎：脚本自己 `docker run collabora/code`（容器名 oryn-m32o-<run>，host 网络，端口 9983，结束时删除）。
 *     SSL 关掉；aliasgroup1 = WOPI 回调地址的源（http://127.0.0.1:7692）；CSP 的 frame-ancestors 放行宿主页的源；
 *     空闲自动保存缩短到 10 秒（E4）；宏执行关闭。
 *   - orynd（127.0.0.1:7692，data_dir 全新，`portal_domain` 为空）配 `artifacts.office`：url = http://localhost:9983
 *     （浏览器）、internal_url = http://127.0.0.1:9983（控制面）、wopi_base = http://127.0.0.1:7692（引擎回调）。
 *     控制面渲染器配成本机 Chromium（root 下降权到 nobody）。
 *   - provider `local`（driver process，orbd = $M32O_BIN/orbd，agent = fakeomp）：artifact 由脚本以线程身份在 orb
 *     环境里跑 `oryn artifact call <op> --params-file … --json` 创建 / 修改、跑 `oryn artifact pull` 取回。
 *   - 一个非 mock 的 vite（127.0.0.1:5199，/api、/_af/ 等代理到这套 orynd）；浏览器跑在只有回环的独立网络命名空间里
 *     （scripts/browser-netns.ts），转发 5199、7692 与引擎的 9983。
 *   端口避开 accept:m31-office（5197 / 7690 / 7971）与其余验收脚本。
 *   前提：以 root 运行；有 git、curl、sqlite3、pandoc、/usr/bin/chromium、docker；先 `make web && make build`。
 *
 * E8 在本环境里去掉 `artifacts.office` 重启 orynd 后检查能力与「编辑」按钮，再整体跑一遍 `bun scripts/m31-office-accept.ts`
 * （它自起一套隔离环境），要求 K1–K14 全部 PASS。
 *
 * 环境变量：M32O_SHOTS（缺省 <仓库>/.dev/m32-office）、CHROMIUM、M32O_BIN、M32O_COORD_DIR、M32O_KEEP、
 * M32O_IMAGE（缺省 collabora/code:latest）。
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

const ES = Array.from({ length: 8 }, (_, i) => `E${i + 1}`);
const USAGE = `用法：bun scripts/m32-office-engine-accept.ts [--only E1,E3,…] [--help]
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
if (ONLY && [...ONLY].some((k) => !ES.includes(k))) {
	console.error(`--only 只接受 E1…E8\n${USAGE}`);
	process.exit(2);
}

const ROOT = `${import.meta.dir}/../..`;
const WEB = `${import.meta.dir}/..`;
const SHOTS = process.env.M32O_SHOTS ?? `${ROOT}/.dev/m32-office`;
const CHROMIUM = process.env.CHROMIUM ?? "/usr/bin/chromium";
const BIN = process.env.M32O_BIN ?? `${ROOT}/bin`;
const COORD_DIR = process.env.M32O_COORD_DIR ?? "/var/tmp/oryn-m32o-coord";
const KEEP = process.env.M32O_KEEP === "1";
const IMAGE = process.env.M32O_IMAGE ?? "collabora/code:latest";
const RUN = Math.random().toString(36).slice(2, 6);
mkdirSync(SHOTS, { recursive: true });

const REAL_PORT = 7692;
const REAL_API = `http://127.0.0.1:${REAL_PORT}`;
const UI_PORT = 5199;
/** public_url 与浏览器都用 localhost（vite 只听 127.0.0.1，Chromium 的 localhost 落到回环）。 */
const REAL_UI = `http://localhost:${UI_PORT}`;
const COORD_EGRESS = 7973;
const ENGINE_PORT = 9983;
/** 浏览器访问引擎的地址（artifacts.office.url）；也是引擎 postMessage 的来源。 */
const ENGINE_URL = `http://localhost:${ENGINE_PORT}`;
/** 控制面与脚本访问引擎的地址（artifacts.office.internal_url）。 */
const ENGINE_INTERNAL = `http://127.0.0.1:${ENGINE_PORT}`;
/** 引擎回调 WOPI 的地址（artifacts.office.wopi_base，也是 aliasgroup1）。 */
const WOPI_BASE = REAL_API;
/** 引擎空闲多久做一次自动保存（缺省 30 秒，这里缩短，E4 等得起）。 */
const IDLESAVE_SECS = 10;
const ENGINE_NAME = `oryn-m32o-${RUN}`;
const TMP = `/var/tmp/oryn-m32o-${RUN}`;
const DB = `${TMP}/data/oryn.db`;

const FIXTURES = `${WEB}/sandbox/viewers/testdata/office`;
type OfficeType = "docx" | "xlsx";
const SAMPLE: Record<OfficeType, Bytes> = {
	docx: new Uint8Array(readFileSync(`${FIXTURES}/sample.docx`)),
	xlsx: new Uint8Array(readFileSync(`${FIXTURES}/sample.xlsx`)),
};
const ENTRY: Record<OfficeType, string> = {
	docx: "document.docx",
	xlsx: "workbook.xlsx",
};
/** sample.docx 正文首句里的一段（判定编辑、转换后原有内容仍在）。 */
const SAMPLE_BODY = "本季度营收稳步增长";

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();
const clip = (s: string, n = 240) =>
	s.length > n ? `${s.slice(0, n)}…（共 ${s.length} 字）` : s;
const sha256 = (b: Uint8Array) =>
	new Bun.CryptoHasher("sha256").update(b).digest("hex");

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

// ---------------------------------------------------------------- 浏览器

let browser: Browser | undefined;
let netns: BrowserNetns | undefined;
const contexts: BrowserContext[] = [];
const pageErrors: string[] = [];
const pageDiag: string[] = [];

/** 浏览器进程命令行里的记号：关闭卡死时按它找到本轮的浏览器进程强制结束（Chromium 忽略不认识的开关）。 */
const BROWSER_MARK = `--oryn-m32o-run=${RUN}`;
const CLOSE_TIMEOUT = 30_000;

async function getBrowser(): Promise<Browser> {
	if (browser) return browser;
	netns ??= await startBrowserNetns({
		name: `oryn-m32o-${RUN}`,
		ports: [UI_PORT, REAL_PORT, ENGINE_PORT],
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

/**
 * 顶层页面的本地开关，并记录引擎帧发来的 postMessage（只记来源等于引擎源的 JSON 字符串消息），
 * 供断言「宿主收到了 Document_Loaded」等。
 */
const INIT_SCRIPT = `(() => {
	if (window.top !== window) return;
	try {
		localStorage.setItem("oryn.debug", "1");
		localStorage.setItem("oryn.panel.width", "760");
		localStorage.setItem("oryn.artifacts.firstPublishSeen", "1");
	} catch {}
	const log = [];
	Object.defineProperty(window, "__m32oEngine", { value: log });
	window.addEventListener("message", (e) => {
		if (e.origin !== ${JSON.stringify(ENGINE_URL)} || typeof e.data !== "string") return;
		try {
			const m = JSON.parse(e.data);
			if (m && typeof m.MessageId === "string") log.push({ id: m.MessageId, values: m.Values ?? null, at: Date.now() });
		} catch {}
	});
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
		viewport: { width: o.width ?? 1440, height: o.height ?? 960 },
		colorScheme: "light",
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

/** 直接请求 orynd（带设备令牌）。 */
async function rapi(
	method: string,
	path: string,
	body?: unknown,
	o: { headers?: Record<string, string> } = {},
): Promise<ApiRes> {
	const raw =
		body instanceof FormData ||
		body instanceof URLSearchParams ||
		body instanceof Uint8Array;
	const res = await fetch(`${REAL_API}${path}`, {
		method,
		headers: {
			Authorization: `Bearer ${auth?.token ?? ""}`,
			"X-Oryn-CSRF": "1",
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

const ORYND_YAML = (office: boolean) => `listen: ["127.0.0.1:${REAL_PORT}"]
public_url: "${REAL_UI}"
portal_domain: ""
data_dir: "${TMP}/data"
log_level: "debug"
idle: { agent_idle: "30m", user_idle: "30m", stop_after_frozen: "24h", wake_on_focus: true }
orb_defaults: { provider: "local", type: "" }
coordinator: { omp_path: "${TMP}/bin/fakeomp", dir: "${COORD_DIR}", egress_port: ${COORD_EGRESS}, verify_omp: false }
artifacts:
  render: { chromium: "${CHROMIUM}", user: "nobody", dir: "${TMP}/render" }
${office ? `  office: { url: "${ENGINE_URL}", internal_url: "${ENGINE_INTERNAL}", wopi_base: "${WOPI_BASE}" }\n` : ""}`;

/**
 * 起 vite 开发服务器的模块代码（独立 cacheDir；关掉 HMR 与文件监视：工作区里别的 agent 同时在改 web/src，
 * 热更新会让正在跑的条目丢状态）。
 */
function viteModule(proxy: string): string {
	const dir = `${WEB}/node_modules/.m32o`;
	mkdirSync(dir, { recursive: true });
	const file = `${dir}/vite-real.mjs`;
	writeFileSync(
		file,
		`import { createServer } from "vite";
${proxy}
const s = await createServer({
	configFile: "vite.config.ts",
	cacheDir: "node_modules/.vite-m32o-real",
	optimizeDeps: { entries: ["index.html", "src/**/*.{ts,tsx}", "!src/**/*.test.{ts,tsx}"] },
	server: { port: ${UI_PORT}, strictPort: true, host: "127.0.0.1", hmr: false, watch: { ignored: ["**"] }, proxy },
});
await s.listen();
console.log("ready");
`,
	);
	return file;
}

// ---- Office 引擎（docker）

let engineStarted = false;

/** 本机能用 docker 且有引擎镜像（没有时先拉）；不能时返回原因。 */
function dockerProblem(): string {
	if (sh("command -v docker").code !== 0) return "本机没有 docker 命令";
	const info = sh("docker info --format '{{.ServerVersion}}'");
	if (info.code !== 0)
		return `docker 守护进程不可用：${clip(oneLine(info.out), 160)}`;
	if (sh(`docker image inspect ${q(IMAGE)}`).code === 0) return "";
	console.log(`  拉取 ${IMAGE}…`);
	const pull = sh(`docker pull ${q(IMAGE)}`);
	return pull.code === 0
		? ""
		: `拉不到 ${IMAGE}：${clip(oneLine(pull.out), 160)}`;
}

async function startEngine() {
	sh(`docker rm -f ${ENGINE_NAME}`);
	const args = [
		"docker",
		"run",
		"-d",
		"--name",
		ENGINE_NAME,
		"--network",
		"host",
		"-e",
		`aliasgroup1=${WOPI_BASE}`,
		IMAGE,
		"--o:ssl.enable=false",
		"--o:ssl.termination=false",
		`--port=${ENGINE_PORT}`,
		`--o:per_document.idlesave_duration_secs=${IDLESAVE_SECS}`,
		// 宿主页（public_url 的源）嵌引擎 iframe：引擎缺省只放行自己与 WOPI 宿主
		`--o:net.content_security_policy=frame-ancestors ${REAL_UI}`,
		"--o:security.enable_macros_execution=false",
		"--o:logging.level=information",
		// 关掉欢迎弹窗（截图里看得到文档本身；家用模式的连接与文档数上限对验收足够）
		"--o:home_mode.enable=true",
	];
	const r = Bun.spawnSync(args);
	if (r.exitCode !== 0)
		throw new Error(
			`docker run 失败：${oneLine(r.stderr.toString() + r.stdout.toString())}`,
		);
	engineStarted = true;
	await waitFor(
		"引擎就绪（/hosting/capabilities）",
		async () => (await fetch(`${ENGINE_INTERNAL}/hosting/capabilities`)).ok,
		120_000,
		1_000,
	);
	const caps = (await (
		await fetch(`${ENGINE_INTERNAL}/hosting/capabilities`)
	).json()) as { productVersion?: string };
	console.log(
		`  引擎：${IMAGE} ${caps.productVersion ?? "?"}（${ENGINE_INTERNAL}）`,
	);
}

function stopEngine() {
	if (!engineStarted) return;
	const logs = sh(`docker logs ${ENGINE_NAME}`);
	writeFileSync(`${SHOTS}/engine.log`, logs.out);
	sh(`docker rm -f ${ENGINE_NAME}`);
	engineStarted = false;
}

// ---- orynd

async function startOrynd(office: boolean) {
	writeFileSync(`${TMP}/orynd.yaml`, ORYND_YAML(office));
	bg(office ? "orynd" : "orynd-noengine", [
		`${BIN}/orynd`,
		"--config",
		`${TMP}/orynd.yaml`,
	]);
	await waitFor(
		"orynd 就绪",
		async () => (await fetch(`${REAL_API}/healthz`)).ok,
		30_000,
	);
}

async function setupReal() {
	if (process.getuid?.() !== 0)
		throw new Error(
			"要以 root 运行（浏览器网络命名空间、协调宿主、渲染器降权）",
		);
	for (const b of ["orynd", "orbd", "oryn", "fakeomp"])
		if (!existsSync(`${BIN}/${b}`))
			throw new Error(
				`缺少 ${BIN}/${b}：先 make web && make build（或设 M32O_BIN）`,
			);
	for (const tool of ["git", "curl", "sqlite3", "pandoc"])
		if (sh(`command -v ${tool}`).code !== 0)
			throw new Error(`本机缺少 ${tool}`);
	await startEngine();
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
	writeFileSync(
		`${TMP}/provider.yaml`,
		`url: "${REAL_API}"\ntoken_file: "${TMP}/provider.token"\norb_url: "${REAL_API}"\ndriver: process\n`,
	);
	await startOrynd(true);
	const admin = (a: string) =>
		sh(`${q(`${BIN}/orynd`)} --config ${q(`${TMP}/orynd.yaml`)} admin ${a}`);
	const dev = admin("create-device --name m32o-accept --kind cli");
	if (dev.code !== 0) throw new Error(`create-device：${dev.out}`);
	const ses = admin("create-web-session --user-agent m32o-accept");
	if (ses.code !== 0) throw new Error(`create-web-session：${ses.out}`);
	auth = {
		token: dev.stdout.split("\n").at(-1) ?? "",
		session: ses.stdout.split("\n").at(-1) ?? "",
	};
	// 全新库里补一个 owner 行（正式部署由首次 Passkey 注册创建）
	const own = sh(
		`sqlite3 ${q(DB)} ${q(`INSERT OR IGNORE INTO owner (id, display_name, user_handle, created_at) VALUES (1, 'm32o', randomblob(16), ${Date.now()})`)}`,
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
	const i = bgs.indexOf(b);
	if (i >= 0) bgs.splice(i, 1);
}

async function stopReal() {
	for (const b of [...bgs].reverse()) await stopBg(b);
	killStragglers();
	stopEngine();
	if (existsSync(TMP)) {
		for (const f of ["orynd", "orynd-noengine", "provider", "vite-real"])
			if (existsSync(`${LOGS}/${f}.log`))
				copyFileSync(`${LOGS}/${f}.log`, `${SHOTS}/real-${f}.log`);
		if (KEEP) console.log(`保留 ${TMP}（日志在 logs/）`);
		else rmSync(TMP, { recursive: true, force: true });
	}
}

const realPage = (o: { width?: number; height?: number } = {}) => {
	if (!auth) throw new Error("真机环境未就绪");
	return openPage({ ...o, auth });
};

// ---- 线程与 orb

interface Th {
	id: string;
	orbId: string;
}
interface ThreadView {
	id: string;
	state: string;
	orbId?: string;
}
const getThread = (id: string) =>
	rjson<ThreadView>("GET", `/api/v1/threads/${id}`);

interface EntryRow {
	seq: number;
	type: string;
	payload: Record<string, unknown>;
}
async function assistantCount(id: string): Promise<number> {
	let n = 0;
	for (let after = 0; ; ) {
		const r = await rjson<{ entries: EntryRow[]; hasMore: boolean }>(
			"GET",
			`/api/v1/threads/${id}/entries?after=${after}&limit=500`,
		);
		for (const e of r.entries) {
			const m = e.payload.message;
			if (
				e.type === "message" &&
				typeof m === "object" &&
				m !== null &&
				"role" in m &&
				m.role === "assistant"
			)
				n++;
		}
		if (!r.hasMore || r.entries.length === 0) return n;
		after = r.entries.at(-1)?.seq ?? after;
	}
}

const orbHome = (orbId: string) => `${TMP}/orbs/orbs/${orbId}/home`;
const ORB_PATH = "/usr/local/bin:/usr/bin:/bin";

let projectId = "";
let T1: Th | undefined;
/** 一条有项目的 orb 线程：artifact 都以它的身份建。 */
async function thread(): Promise<Th> {
	if (T1) return T1;
	projectId ||= (
		await rjson<{ id: string }>("POST", "/api/v1/projects", {
			name: `m32o-${RUN}`,
			shallowClone: true,
		})
	).id;
	const r = await rjson<{ thread: { id: string } }>("POST", "/api/v1/threads", {
		projectId,
		prompt: "m32o 真机：Office 引擎线程",
		executor: { kind: "new_orb", provider: "local" },
	});
	const th = await waitFor(
		`线程 ${r.thread.id} 第一条助手回复`,
		async () => {
			const th = await getThread(r.thread.id);
			if (th.state === "error") throw new Error(`线程 ${th.id} 停在 error`);
			return (await assistantCount(th.id)) >= 1 && th.state === "idle"
				? th
				: null;
		},
		180_000,
		1_000,
	);
	if (!th.orbId) throw new Error(`线程 ${th.id} 没有 orb`);
	await waitFor(
		"orb 的 orbd.sock",
		() => existsSync(`${orbHome(th.orbId ?? "")}/.oryn/orbd.sock`),
		60_000,
		500,
	);
	T1 = { id: th.id, orbId: th.orbId };
	return T1;
}

// ---- artifact 操作（以线程身份在 orb 里跑 `oryn` CLI）

interface CallResult {
	text: string;
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
/** 在 orb 里写一个文件（给 `source` 用），返回绝对路径。 */
function orbFile(t: Th, name: string, data: string | Uint8Array): string {
	const dir = `${orbHome(t.orbId)}/m32o-src`;
	mkdirSync(dir, { recursive: true });
	writeFileSync(`${dir}/${name}`, data);
	return `${dir}/${name}`;
}

/**
 * 改 zip 里第一个本地文件头的「修改时间」低字节：恰好改一个字节，容器仍然合法，用来造一个内容不同的新版本。
 */
function touchZip(b: Bytes): Bytes {
	const out = new Uint8Array(b);
	if (out[0] !== 0x50 || out[1] !== 0x4b || out[2] !== 0x03 || out[3] !== 0x04)
		throw new Error("不是以本地文件头开头的 zip");
	out[10] = (out[10] ?? 0) ^ 0x01;
	return out;
}

interface Created {
	id: string;
	version: number;
}
interface ArtifactView {
	id: string;
	title: string;
	kind: string;
	type?: string;
	version: number;
	origin: string;
}
interface Detail {
	artifact: ArtifactView;
	head: number;
	editing?: boolean;
	files: { path: string; size: number; sha256: string }[];
}
interface VersionView {
	version: number;
	authorThreadId?: string;
	authorLabel: string;
	note?: string;
}
interface Caps {
	render: boolean;
	office: { edit: boolean; convert: boolean; reason?: string };
}
const detail = (id: string) => rjson<Detail>("GET", `/api/v1/artifacts/${id}`);
const versions = async (id: string) =>
	(
		await rjson<{ versions: VersionView[] }>(
			"GET",
			`/api/v1/artifacts/${id}/versions`,
		)
	).versions;
const caps = () => rjson<Caps>("GET", "/api/v1/artifacts/capabilities");

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

/** 编辑占用的工作副本（数据库里 artifact_office_holds 的一行；没有占用时为 null）。 */
function hold(id: string): { base: number; draftSha: string } | null {
	const r = sh(
		`sqlite3 -json ${q(DB)} ${q(`SELECT base_version, draft_sha FROM artifact_office_holds WHERE artifact_id = '${id}'`)}`,
	);
	if (r.code !== 0) throw new Error(`查占用：${r.out}`);
	if (!r.stdout) return null;
	const rows = JSON.parse(r.stdout) as {
		base_version: number;
		draft_sha: string | null;
	}[];
	const row = rows[0];
	return row ? { base: row.base_version, draftSha: row.draft_sha ?? "" } : null;
}

// ---- 查看器与编辑器

/** 等 Office 查看器帧渲染完成（运行时已装上、`data-ready`、「已渲染 N 页」）。 */
async function officeFrame(p: Page): Promise<string> {
	return waitFor(
		"Office 查看器渲染完成（已渲染 N 页）",
		async () => {
			const el = await p
				.locator("[data-testid=artifact-frame]")
				.first()
				.elementHandle();
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
			return st.ready && st.runtime && st.rendered ? st.rendered : null;
		},
		120_000,
		300,
	);
}

/** 打开 `/artifacts/<id>`，等 Office 查看器帧渲染完成。 */
async function officeViewer(id: string): Promise<Page> {
	const p = await realPage();
	await goto(p, `/artifacts/${id}`);
	await p.locator("[data-testid=artifact-viewer]").waitFor({ timeout: 60_000 });
	note(`查看器 ${await officeFrame(p)}`);
	return p;
}

interface EngineMsg {
	id: string;
	values: Record<string, unknown> | null;
	at: number;
}
const engineMsgs = (p: Page) =>
	p.evaluate(
		() =>
			(window as unknown as { __m32oEngine: EngineMsg[] }).__m32oEngine ?? [],
	);

/** 引擎帧（`office-editor-frame` 里的 cool.html）。 */
async function engineFrame(p: Page): Promise<Frame> {
	return waitFor(
		"引擎帧",
		async () => {
			const el = await p
				.locator("[data-testid=office-editor-frame]")
				.elementHandle();
			const f = await el?.contentFrame();
			return f?.url().startsWith(ENGINE_URL) ? f : null;
		},
		30_000,
	);
}

/**
 * 在查看器里点「编辑」，等编辑器加载完成：宿主收到引擎的 `App_LoadingStatus`（`Document_Loaded`），
 * 编辑器撤掉加载遮罩。返回点击时刻（之后的引擎消息按它过滤）。
 */
async function openEditor(p: Page): Promise<number> {
	const btn = p.locator("[data-testid=artifact-office-edit]").first();
	await btn.waitFor({ state: "visible", timeout: 30_000 });
	const t0 = Date.now();
	await btn.click();
	await p
		.locator("[data-testid=office-editor-frame]")
		.waitFor({ timeout: 30_000 });
	await waitFor(
		"引擎报告 Document_Loaded",
		async () => {
			if (
				await p
					.locator("[data-testid=office-editor-error]")
					.isVisible()
					.catch(() => false)
			)
				throw new Error(
					`编辑器打不开：${oneLine((await p.locator("[data-testid=office-editor-error]").textContent()) ?? "")}`,
				);
			return (await engineMsgs(p)).some(
				(m) =>
					m.id === "App_LoadingStatus" &&
					m.values?.Status === "Document_Loaded",
			);
		},
		120_000,
		500,
	);
	await p
		.locator("[data-testid=office-editor-loading]")
		.waitFor({ state: "detached", timeout: 15_000 });
	// 文档内容（画布瓦片）在 Document_Loaded 之后才陆续画出来
	await sleep(3_000);
	return t0;
}

/** 向引擎帧发一条 postMessage（JSON 字符串，目标源 = 引擎源）。 */
async function postToEngine(
	p: Page,
	id: string,
	values: Record<string, unknown>,
) {
	await p.evaluate(
		({ id, values, origin }) => {
			const f = document.querySelector<HTMLIFrameElement>(
				"[data-testid=office-editor-frame]",
			);
			if (!f?.contentWindow) throw new Error("没有引擎 iframe");
			f.contentWindow.postMessage(
				JSON.stringify({ MessageId: id, SendTime: Date.now(), Values: values }),
				origin,
			);
		},
		{ id, values, origin: ENGINE_URL },
	);
}

/** 经 `Action_Paste` 往文档里写一段纯文本，等引擎报告文档已修改（`Doc_ModifiedStatus`）。 */
async function paste(p: Page, text: string) {
	const t0 = Date.now();
	await postToEngine(p, "Action_Paste", {
		Mimetype: "text/plain;charset=utf-8",
		Data: text,
	});
	const modified = await waitFor(
		"引擎报告文档已修改（Doc_ModifiedStatus）",
		async () =>
			(await engineMsgs(p)).some(
				(m) =>
					m.at >= t0 &&
					m.id === "Doc_ModifiedStatus" &&
					m.values?.Modified === true,
			),
		20_000,
		250,
	).catch(() => false);
	note(
		modified
			? "Action_Paste 后引擎回 Doc_ModifiedStatus {Modified: true}"
			: "Action_Paste 后 20 秒内没收到 Doc_ModifiedStatus（以版本与工作副本判定）",
	);
}

/** 点「完成」，等编辑器关闭、回到查看器。 */
async function finishEditor(p: Page) {
	const t0 = Date.now();
	await p.locator("[data-testid=office-editor-done]").click();
	const closed = await p
		.locator("[data-testid=office-editor]")
		.waitFor({ state: "detached", timeout: 30_000 })
		.then(
			() => true,
			() => false,
		);
	const resp = (await engineMsgs(p).catch(() => [])).filter(
		(m) => m.at >= t0 && m.id === "Action_Save_Resp",
	);
	note(
		`「完成」后引擎的 Action_Save_Resp：${resp.map((m) => JSON.stringify(m.values)).join("、") || "无"}`,
	);
	check(closed, "点「完成」后编辑器关闭、回到查看器");
}

/** 截下引擎帧的区域（亲自核对编辑器确实加载出文档）。 */
async function shotEditor(p: Page, name: string) {
	const f = `${SHOTS}/${name}.png`;
	await p.locator("[data-testid=office-editor-frame]").screenshot({ path: f });
	note(`编辑器截图 ${f.slice(SHOTS.length + 1)}`);
}

// ================================================================ 条目

/** E1：配好引擎后 capabilities 的 office.edit 与 office.convert 都为真。 */
async function realE1() {
	const c = await waitFor(
		"capabilities.office.edit 与 convert 为真",
		async () => {
			const c = await caps();
			return c.office?.edit && c.office?.convert ? c : null;
		},
		60_000,
		1_000,
	).catch(async () => caps());
	check(
		c.office?.edit === true && c.office?.convert === true,
		`GET /artifacts/capabilities → office=${JSON.stringify(c.office)}`,
	);
}

/** E2：点「编辑」→ 宿主收到 Document_Loaded；详情 editing=true；「完成」后占用释放、版本不变。 */
async function realE2() {
	const c = await createOffice("docx", "M32 E2 编辑器加载");
	const p = await officeViewer(c.id);
	const t0 = await openEditor(p);
	const msgs = (await engineMsgs(p)).filter((m) => m.at >= t0);
	const statuses = msgs
		.filter((m) => m.id === "App_LoadingStatus")
		.map((m) => String(m.values?.Status));
	check(
		statuses.includes("Document_Loaded"),
		`宿主收到引擎的 App_LoadingStatus：${statuses.join(" → ")}`,
	);
	const f = await engineFrame(p);
	note(`引擎帧 ${f.url().split("?")[0]}`);
	const d = await detail(c.id);
	check(d.editing === true, `详情 editing=${String(d.editing)}`);
	await shot(p, "e2-editor");
	await shotEditor(p, "e2-editor-frame");
	await finishEditor(p);
	await officeFrame(p);
	const released = await waitFor(
		"占用释放（关闭宽限期满后清扫）",
		async () => !(await detail(c.id)).editing,
		120_000,
		2_000,
	).catch(() => false);
	expect(released === true, "关闭编辑器后占用释放（editing=false）");
	const vs = await versions(c.id);
	expect(
		vs.length === 1,
		`没有改动 → 版本数仍为 ${vs.length}（head v${vs[0]?.version}）`,
	);
}

/** E3：Action_Paste 写一段文字后点「完成」→ 版本 +1、作者是用户；pull 取回的文件经 pandoc 转出的文本含该段文字。 */
async function realE3() {
	const t = await thread();
	const c = await createOffice("docx", "M32 E3 粘贴保存");
	const p = await officeViewer(c.id);
	await openEditor(p);
	const text = `M32验收粘贴段落 ${RUN}`;
	await paste(p, text);
	await shotEditor(p, "e3-pasted");
	await finishEditor(p);
	const d = await waitFor(
		"点「完成」后出版本 2",
		async () => {
			const d = await detail(c.id);
			return d.head >= 2 ? d : null;
		},
		90_000,
		1_000,
	);
	check(d.head === 2, `head v${d.head}`);
	const vs = await versions(c.id);
	check(
		vs[0]?.version === 2 &&
			vs[0]?.authorThreadId === undefined &&
			vs[1]?.authorThreadId === t.id,
		`v2 作者是用户（authorThreadId=${vs[0]?.authorThreadId ?? "无"}，authorLabel=${vs[0]?.authorLabel}，note=${JSON.stringify(vs[0]?.note ?? "")}；v1 是线程 ${vs[1]?.authorThreadId}）`,
	);
	expect(
		(vs[0]?.note ?? "") === "",
		"v2 来自显式保存（不是关闭后由清扫落的「编辑器自动保存」）",
	);
	const to = `${orbHome(t.orbId)}/m32o-pull/e3.docx`;
	mkdirSync(`${orbHome(t.orbId)}/m32o-pull`, { recursive: true });
	const r = orbCli(t, `artifact pull ${c.id} --to ${q(to)} --json`);
	check(
		r.code === 0,
		`oryn artifact pull 退出 ${r.code}：${clip(oneLine(r.out), 200)}`,
	);
	const got = new Uint8Array(readFileSync(to));
	copyFileSync(to, `${SHOTS}/e3-pulled.docx`);
	expect(
		sha256(got) === d.files[0]?.sha256 && sha256(got) !== sha256(SAMPLE.docx),
		`取回的是 v2（sha256 ${sha256(got).slice(0, 16)}…，不同于样本）`,
	);
	// 粘贴落在光标处（文档开头的 Title 样式标题段）；-s 才把标题段输出进纯文本
	const plain = sh(`pandoc -f docx -t plain -s ${q(to)}`);
	check(plain.code === 0, `pandoc 转文本退出 ${plain.code}`);
	writeFileSync(`${SHOTS}/e3-pulled.txt`, plain.stdout);
	check(
		plain.stdout.includes(text),
		`pandoc 转出的文本含「${text}」（共 ${plain.stdout.length} 字，前 80 字：${clip(oneLine(plain.stdout), 80)}）`,
	);
	// pandoc 的纯文本输出不含 Title 样式的标题段，用正文首句判定
	expect(
		plain.stdout.includes(SAMPLE_BODY),
		`原有正文（「${SAMPLE_BODY}」）仍在`,
	);
}

/** E4：编辑中等一次自动保存 → 工作副本更新、版本号不变；关闭后由清扫把工作副本落成用户版本。 */
async function realE4() {
	const c = await createOffice("docx", "M32 E4 自动保存");
	const p = await officeViewer(c.id);
	await openEditor(p);
	const h0 = hold(c.id);
	check(
		h0 !== null && h0.base === 1 && h0.draftSha === "",
		`打开后有占用（base_version=${h0?.base}，草稿=${h0?.draftSha || "无"}）`,
	);
	await paste(p, `M32自动保存段落 ${RUN}`);
	const t0 = Date.now();
	const h1 = await waitFor(
		`引擎空闲 ${IDLESAVE_SECS} 秒后自动保存（IsAutosave=true 的 PutFile 写出工作副本）`,
		() => {
			const h = hold(c.id);
			return h?.draftSha ? h : null;
		},
		90_000,
		1_000,
	);
	note(
		`粘贴后 ${((Date.now() - t0) / 1000).toFixed(1)}s 占用有了草稿 ${h1.draftSha.slice(0, 16)}…（base_version=${h1.base}）`,
	);
	const d = await detail(c.id);
	check(
		d.head === 1 && (await versions(c.id)).length === 1,
		`自动保存后版本号不变（head v${d.head}）`,
	);
	expect(
		h1.draftSha !== d.files[0]?.sha256,
		"草稿与最新版本的入口文件不同（确实保存了改动；只有 IsAutosave=true 的 PutFile 只写草稿不出版本）",
	);
	await shotEditor(p, "e4-autosaved");
	await finishEditor(p);
	const v2 = await waitFor(
		"关闭宽限期满后清扫把草稿落成 v2",
		async () => {
			const d = await detail(c.id);
			return d.head >= 2 && !d.editing ? d : null;
		},
		120_000,
		2_000,
	).catch(() => null);
	const vs = await versions(c.id);
	expect(
		v2 !== null &&
			v2.files[0]?.sha256 === h1.draftSha &&
			vs[0]?.authorThreadId === undefined,
		`关闭后草稿落成用户版本 v${v2?.head ?? "?"}（note=${JSON.stringify(vs[0]?.note ?? "")}，占用已释放）`,
	);
}

/** E5：编辑会话存在时 orb 里 update_artifact → locked；关闭编辑器后重试成功。 */
async function realE5() {
	const t = await thread();
	const c = await createOffice("docx", "M32 E5 编辑占用");
	const p = await officeViewer(c.id);
	await openEditor(p);
	const changed = touchZip(SAMPLE.docx);
	const params = {
		id: c.id,
		baseVersion: 1,
		files: [
			{ path: "document.docx", source: orbFile(t, "e5-changed.docx", changed) },
		],
	};
	const locked = acall(t, "update_artifact", params);
	check(
		!locked.ok && locked.out.includes("locked"),
		`编辑中 update_artifact → ${clip(oneLine(locked.out), 200)}`,
	);
	expect(
		locked.out.includes("用户正在编辑器里编辑这个文档"),
		"错误文案说明用户正在编辑",
	);
	const banner = p.locator("[data-testid=artifact-office-editing]");
	note(
		`编辑者自己的查看器不显示占用提示条：${(await banner.count()) === 0 ? "是" : "否"}`,
	);
	await finishEditor(p);
	const t0 = Date.now();
	const ok = await waitFor(
		"关闭编辑器后 update_artifact 成功",
		() => {
			const r = acall(t, "update_artifact", params);
			if (r.ok) return r;
			if (!r.out.includes("locked"))
				throw new Error(`非 locked 的失败：${clip(oneLine(r.out), 200)}`);
			return null;
		},
		120_000,
		3_000,
	);
	const v = JSON.parse(ok.res?.text ?? "{}") as { version?: number };
	check(
		v.version === 2,
		`关闭 ${((Date.now() - t0) / 1000).toFixed(0)}s 后重试 update_artifact → 版本 ${v.version}`,
	);
	expect(!(await detail(c.id)).editing, "详情 editing=false");
}

/** E6：导入 old.doc（引擎从样本 docx 转出）→ 引擎转换后得到 type=docx 的 artifact。 */
async function realE6() {
	const fd = new FormData();
	fd.append("data", new Blob([SAMPLE.docx]), "sample.docx");
	const conv = await fetch(`${ENGINE_INTERNAL}/cool/convert-to/doc`, {
		method: "POST",
		body: fd,
	});
	const doc = new Uint8Array(await conv.arrayBuffer());
	check(
		conv.ok &&
			doc[0] === 0xd0 &&
			doc[1] === 0xcf &&
			doc[2] === 0x11 &&
			doc[3] === 0xe0,
		`引擎把样本转成 old.doc：${conv.status}，${doc.length} 字节，OLE 头`,
	);
	writeFileSync(`${SHOTS}/e6-old.doc`, doc);
	const imp = new FormData();
	imp.append("old.doc", new Blob([doc]), "old.doc");
	const r = await rapi("POST", "/api/v1/artifacts/import", imp);
	const a = r.json as ArtifactView;
	check(
		r.status < 300 &&
			a.type === "docx" &&
			a.kind === "document" &&
			a.origin === "import",
		`导入 old.doc → ${r.status} type=${a.type} kind=${a.kind} origin=${a.origin} title=${a.title}${r.status >= 300 ? `：${clip(r.text, 200)}` : ""}`,
	);
	expect(a.title === "old", `标题取原文件名：${a.title}`);
	const d = await detail(a.id);
	const entry = await rapi(
		"GET",
		`/api/v1/artifacts/${a.id}/export?format=original`,
	);
	expect(
		d.files.length === 1 &&
			d.files[0]?.path === "document.docx" &&
			entry.bytes[0] === 0x50 &&
			entry.bytes[1] === 0x4b,
		`版本里只有 document.docx（${d.files[0]?.size} 字节，zip 包）`,
	);
	writeFileSync(`${SHOTS}/e6-imported.docx`, entry.bytes);
	const plain = sh(`pandoc -f docx -t plain ${q(`${SHOTS}/e6-imported.docx`)}`);
	expect(
		plain.code === 0 && plain.stdout.includes(SAMPLE_BODY),
		`转换后的 docx 保留样本正文（「${SAMPLE_BODY}」）`,
	);
	const p = await officeViewer(a.id);
	await shot(p, "e6-imported");
}

/** E7：xlsx 的 export?format=pdf 经引擎转换 → %PDF- 开头。 */
async function realE7() {
	const c = await createOffice("xlsx", "M32 E7 表格 PDF");
	const r = await rapi("GET", `/api/v1/artifacts/${c.id}/export?format=pdf`);
	const head = new TextDecoder().decode(r.bytes.slice(0, 5));
	check(
		r.status === 200 && head === "%PDF-",
		`export?format=pdf → ${r.status} ${r.headers.get("content-type")}，${r.bytes.length} 字节，开头 ${JSON.stringify(head)}${r.status !== 200 ? `：${clip(r.text, 200)}` : ""}`,
	);
	writeFileSync(`${SHOTS}/e7-sheet.pdf`, r.bytes);
	const txt = sh(`pdftotext -layout ${q(`${SHOTS}/e7-sheet.pdf`)} -`);
	if (txt.code === 0)
		expect(
			txt.stdout.includes("明细") || /\d/.test(txt.stdout),
			`PDF 里有表格内容（前 80 字：${clip(oneLine(txt.stdout), 80)}）`,
		);
}

/** E8：不配引擎重启 → office.edit=false、「编辑」按钮不出现；accept:m31-office 的 K1–K14 全过。 */
async function realE8() {
	const docx = await createOffice("docx", "M32 E8 无引擎");
	const old = bgs.find((b) => b.name === "orynd");
	if (!old) throw new Error("找不到配了引擎的 orynd 进程");
	await stopBg(old);
	await startOrynd(false);
	note("去掉 artifacts.office 重启 orynd");
	const c = await caps();
	check(
		c.office?.edit === false && c.office?.convert === false,
		`capabilities.office=${JSON.stringify(c.office)}`,
	);
	const p = await officeViewer(docx.id);
	await sleep(1_000);
	const n = await p.locator("[data-testid=artifact-office-edit]").count();
	check(n === 0, `查看器里「编辑」按钮个数 ${n}`);
	await shot(p, "e8-no-engine");
	const xlsx = await createOffice("xlsx", "M32 E8 无引擎表格");
	const pdf = await rapi(
		"GET",
		`/api/v1/artifacts/${xlsx.id}/export?format=pdf`,
	);
	expect(
		pdf.status === 400 && pdf.text.includes("需要 Office 引擎"),
		`无引擎时 xlsx 的 PDF → ${pdf.status}：${clip(oneLine(pdf.text), 120)}`,
	);
	await closeAll();
	console.log("    · 跑 accept:m31-office（K1–K14）…");
	const log = `${SHOTS}/e8-m31-office.log`;
	const fd = openSync(log, "w");
	const m31 = Bun.spawn(["bun", "scripts/m31-office-accept.ts"], {
		cwd: WEB,
		// 同一套二进制（M32O_BIN）
		env: { ...process.env, M31O_BIN: BIN, CHROMIUM },
		stdin: "ignore",
		stdout: fd,
		stderr: fd,
	});
	const code = await m31.exited;
	const out = readFileSync(log, "utf8");
	const summary = out.slice(out.lastIndexOf("== 汇总"));
	const verdicts = new Map(
		[...summary.matchAll(/^(K\d+) \[real\] (PASS|FAIL|SKIP)/gm)].map((m) => [
			m[1] ?? "",
			m[2] ?? "",
		]),
	);
	const ks = Array.from({ length: 14 }, (_, i) => `K${i + 1}`);
	note(
		`accept:m31-office 退出 ${code}：${ks.map((k) => `${k}=${verdicts.get(k) ?? "?"}`).join(" ")}（日志 ${log.slice(SHOTS.length + 1)}）`,
	);
	check(
		code === 0 && ks.every((k) => verdicts.get(k) === "PASS"),
		"accept:m31-office 的 K1–K14 全部 PASS",
	);
}

// ================================================================ 执行

const PLAN: Record<string, () => Promise<void>> = {
	E1: realE1,
	E2: realE2,
	E3: realE3,
	E4: realE4,
	E5: realE5,
	E6: realE6,
	E7: realE7,
	E8: realE8,
};

async function runOne(k: string, fn: () => Promise<void>) {
	evidence = [];
	softFails = 0;
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

const todo = ES.filter((k) => !ONLY || ONLY.has(k));
console.log(
	`M32 Office 引擎验收 run=${RUN} shots=${SHOTS} 条目=${todo.join(",")}`,
);
const started = Date.now();
const noDocker = dockerProblem();
if (noDocker) {
	// 计划允许：没有 docker 时全部 SKIP 并说明
	for (const k of todo) {
		outcomes.push({
			k,
			verdict: "SKIP",
			reason: noDocker,
			seconds: 0,
			evidence: [],
		});
		console.log(`${k} [real] SKIP  ${noDocker}`);
	}
} else {
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
process.exit(outcomes.every((o) => o.verdict !== "FAIL") ? 0 : 1);
