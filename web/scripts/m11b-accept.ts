#!/usr/bin/env bun
import {
	existsSync,
	mkdirSync,
	readFileSync,
	statSync,
	writeFileSync,
} from "node:fs";
import { homedir, hostname, release } from "node:os";
/**
 * M11b 真机验收（验证分册「M11b 真机」X24–X37；X23 自动化另跑）：对 dev 部署（orynd + Firecracker provider `fc`）逐项执行，
 * 每项打印 `X<n> PASS|FAIL|SKIP (秒)` 与证据行，失败时给该场景最近操作的线程页截图并留一份 orynd.log（与 runner 日志）末尾。
 *
 * 用法（web/ 目录，以 root 运行）：bun run accept:m11b [-- --only X24,X25]
 * 环境变量：ORYN_URL（缺省 http://localhost:7600）、ORYN_ROOT、ORYN_TOKEN、ORYN_CREDENTIALS（缺省 .dev/credentials.json）；
 *   M11B_RUNNER_URL（runner 的 ORYN_URL，缺省 http://127.0.0.1:7600）；M11B_USER（跑 runner 的普通用户，缺省 oryn-m11b）；
 *   M11B_FC（Firecracker provider 名，缺省 fc）；M11B_FC_STATE（其 state_dir，缺省 /var/lib/oryn-fc）；
 *   M11B_SHOTS（默认 /tmp/oryn-shots/m11b-accept）；CHROMIUM。
 * CLI 一律 `./bin/oryn`，`ORYN_CREDENTIALS=$ROOT/.dev/credentials.json`。
 *
 * 固定布置：
 *   - runner 用户 `oryn-m11b`（不存在就 `useradd -m`）；`bin/orbd` 与 `build/core/x64/omp` 拷到 /var/tmp/m11b/{orbd,bin/omp}，
 *     runner 以 `runuser -u oryn-m11b -- env -i HOME=… PATH=/var/tmp/m11b/bin:… ORYN_URL=… ORYN_RUNNER_TOKEN=… orbd run --runner
 *     --dir /tmp/m11-a --dir /tmp/m11-b [--terminal]` 起在后台，输出追加到 /var/tmp/m11b/runner-<RUN>.log。
 *     root 的 ~/.omp/agent/models.yml 拷给该用户（0600），agent 才能应答。
 *   - 每次运行重建 /tmp/m11-a（git 仓库、一个提交）、/tmp/m11-b、/tmp/m11-c（不在 --dir 里），属主为该用户；
 *     runner 名 `box-<RUN>`（与 provider 名共用唯一约束，删除后名字不能复用，所以每次运行换新名）。
 *   - 场景之间的前置惰性准备（ensureRunnerOnline / ensureT25 / ensureFcThread / ensureKeep），`--only` 单跑时缺什么现建什么。
 *   - Firecracker 场景（X31–X37）要 `fc` 在线，否则整体 SKIP；用完的 VM 在场景里就删掉，避免撞 `limits.max_vms: 3`。
 *   - 结尾（含异常与 Ctrl-C）：杀掉 runner 进程（及该用户的全部进程）、删除本次建的 runner、删除本次建的全部 Firecracker orb、
 *     清掉 fc 项目的快照。
 *
 * 与分册的出入：
 *   - X36：guest 的 link 走 TLS（https://172.31.255.1:7643）；`curl http://172.31.255.1:7600/healthz` 应失败（端口未放行），
 *     改以 `--cacert <orbd 环境里的 ORYN_ORB_CA_PEM> https://172.31.255.1:7643/orb/v1/whoami` 拿到 HTTP 应答证明 link 端口可达。
 *   - guest 里没有 `docker exec`：让 agent 用 bash 执行并只回复输出，脚本从最后一条 assistant 消息里取。
 *   - X32 的内核版本取自 images/fc/artifacts.lock.json（kernel.sourceTag 里的 x.y.z），不硬编码。
 *   - X27 / X28 的状态码经 REST（`POST threads`）断言；X37 的「重试」在线程页点按钮（即 `POST threads/{id}/wake`）。
 */
import type { Subprocess } from "bun";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Page,
} from "playwright-core";

const ROOT = process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`;
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const CREDS = process.env.ORYN_CREDENTIALS ?? `${ROOT}/.dev/credentials.json`;
const TOKEN =
	process.env.ORYN_TOKEN ??
	(JSON.parse(readFileSync(CREDS, "utf8")) as { token: string }).token;
const SHOTS = process.env.M11B_SHOTS ?? "/tmp/oryn-shots/m11b-accept";
const DB = `${ROOT}/.dev/data/oryn.db`;
const ORYN = `${ROOT}/bin/oryn`;
const ORYND_LOG = `${ROOT}/.dev/orynd.log`;
const WEB_STATE = `${ROOT}/.dev/e2e/state.json`;
const RUNNER_URL = process.env.M11B_RUNNER_URL ?? "http://127.0.0.1:7600";
const RUSER = process.env.M11B_USER ?? "oryn-m11b";
const STAGE = "/var/tmp/m11b";
const RBIN = `${STAGE}/bin`;
const RORBD = `${STAGE}/orbd`;
const OMP_SRC = `${ROOT}/build/core/x64/omp`;
const DIR_A = "/tmp/m11-a";
const DIR_B = "/tmp/m11-b";
const DIR_C = "/tmp/m11-c";
const FC_NAME = process.env.M11B_FC ?? "fc";
const FC_STATE = process.env.M11B_FC_STATE ?? "/var/lib/oryn-fc";
const FC_CONFIG = `${ROOT}/dev/provider-fc.yaml`;
const FC_MAX_VMS = 3;
const FC_LOCK = `${ROOT}/images/fc/artifacts.lock.json`;
const FC_HOST = "172.31.255.1";
const SETUP_FC = `sleep 15; echo "run" >> ~/setup-runs.txt`;
const GIT_ID = "-c user.name=m11b -c user.email=m11b@example.invalid";
mkdirSync(SHOTS, { recursive: true });

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

const RUN = Math.random().toString(36).slice(2, 6);
const RUNNER_LOG = `${STAGE}/runner-${RUN}.log`;
const KEEP = `keep-${RUN}`;

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const sec = (ms: number) => `${(ms / 1000).toFixed(1)}s`;
const mib = (b: number) => `${(b / 1048576).toFixed(1)}MiB`;

function sh(
	cmd: string,
	opts: { cwd?: string } = {},
): { code: number; out: string; stdout: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], { cwd: opts.cwd ?? ROOT });
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
		stdout: r.stdout.toString().trim(),
	};
}

function must(r: { code: number; out: string }, what: string): string {
	if (r.code !== 0) throw new Error(`${what}失败（${r.code}）：${r.out}`);
	return r.out;
}

const CLI_ENV = `ORYN_CREDENTIALS=${q(CREDS)}`;

interface CliRes {
	code: number;
	out: string;
	stdout: string;
}

async function cli(args: string): Promise<CliRes> {
	const p = Bun.spawn(["bash", "-c", `${CLI_ENV} ${q(ORYN)} ${args}`], {
		cwd: ROOT,
		stdout: "pipe",
		stderr: "pipe",
	});
	const [stdout, stderr, code] = await Promise.all([
		new Response(p.stdout).text(),
		new Response(p.stderr).text(),
		p.exited,
	]);
	return { code: code as number, out: `${stdout}${stderr}`.trim(), stdout };
}

function sql<T = Record<string, unknown>>(qs: string): T[] {
	const r = Bun.spawnSync([
		"sqlite3",
		"-json",
		"-cmd",
		".timeout 5000",
		DB,
		qs,
	]);
	if (r.exitCode !== 0)
		throw new Error(`sqlite3：${r.stderr.toString().trim()}`);
	const s = r.stdout.toString().trim();
	return s ? (JSON.parse(s) as T[]) : [];
}
const one = <T = Record<string, unknown>>(qs: string) => sql<T>(qs)[0];

interface ApiRes {
	status: number;
	json: unknown;
	text: string;
}

async function api(
	method: string,
	path: string,
	body?: unknown,
): Promise<ApiRes> {
	const res = await fetch(`${BASE}${path}`, {
		method,
		headers: {
			Authorization: `Bearer ${TOKEN}`,
			"X-Oryn-CSRF": "1",
			...(body !== undefined ? { "Content-Type": "application/json" } : {}),
		},
		body: body !== undefined ? JSON.stringify(body) : undefined,
		redirect: "manual",
	});
	const text = await res.text();
	let json: unknown = null;
	try {
		json = JSON.parse(text);
	} catch {
		// 空体（204）
	}
	return { status: res.status, json, text };
}

const codeOf = (r: ApiRes) => (r.json as { code?: string } | null)?.code ?? "";

async function getJson<T>(path: string): Promise<T> {
	const r = await api("GET", path);
	if (r.status !== 200) throw new Error(`GET ${path} → ${r.status} ${r.text}`);
	return r.json as T;
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
	intervalMs = 500,
): Promise<T> {
	const deadline = Date.now() + timeoutMs;
	let last: unknown;
	while (Date.now() < deadline) {
		try {
			const v = await fn();
			if (v) return v;
		} catch (e) {
			last = e;
		}
		await sleep(intervalMs);
	}
	throw new Error(
		`超时（${timeoutMs}ms）：${what}${last ? `；最后错误 ${String(last)}` : ""}`,
	);
}

// ---------------------------------------------------------------- 结果

type Verdict = "PASS" | "FAIL" | "SKIP";
class Skip extends Error {}
const results: {
	id: string;
	verdict: Verdict;
	seconds: number;
	evidence: string[];
}[] = [];
/** 准备阶段（用户、二进制、模型配置、目录）与收尾的记录，写进结果 JSON。 */
const setupNotes: string[] = [];
const cleanupNotes: string[] = [];
let evidence: string[] = [];
const note = (s: string) => {
	evidence.push(s);
	console.log(`    · ${s}`);
};
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};
const snote = (s: string) => {
	setupNotes.push(s);
	console.log(`    [准备] ${s}`);
};

/** 当前场景最近操作的线程：失败时截它的线程页。 */
let focus = "";
let browser: Browser | undefined;
let bctx: BrowserContext | undefined;

async function browserCtx(): Promise<BrowserContext> {
	browser ??= await chromium.launch({
		executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
		headless: true,
		args: ["--no-sandbox"],
	});
	if (bctx) return bctx;
	const c = await browser.newContext({
		locale: "zh-CN",
		viewport: { width: 1400, height: 900 },
		extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
	});
	// 终端走 WebSocket：有网页会话的 Cookie 就一并带上（同 m3）
	if (existsSync(WEB_STATE))
		await c.addCookies(
			(
				JSON.parse(readFileSync(WEB_STATE, "utf8")) as {
					cookies: Parameters<BrowserContext["addCookies"]>[0];
				}
			).cookies,
		);
	await c.addInitScript(() => {
		localStorage.setItem("oryn.debug", "1");
		// 面板宽度给足，Files / Changes 走左右分栏
		localStorage.setItem("oryn.panel.width", "760");
	});
	bctx = c;
	return c;
}

async function newPage(): Promise<Page> {
	return (await browserCtx()).newPage();
}

async function shot(page: Page, name: string) {
	const p = `${SHOTS}/${name}-${RUN}.png`;
	await page.screenshot({ path: p, fullPage: true });
	note(`截图 ${p}`);
}

async function failArtifacts(id: string) {
	const log = `${SHOTS}/${id}-FAIL-${RUN}.orynd.log`;
	writeFileSync(log, sh(`tail -n 200 ${q(ORYND_LOG)}`).stdout);
	note(`orynd.log 末尾 ${log}`);
	if (existsSync(RUNNER_LOG)) {
		const rl = `${SHOTS}/${id}-FAIL-${RUN}.runner.log`;
		writeFileSync(rl, sh(`tail -n 200 ${q(RUNNER_LOG)}`).stdout);
		note(`runner 日志末尾 ${rl}`);
	}
	if (!focus) return;
	try {
		const page = await newPage();
		await page
			.goto(`${BASE}/t/${focus}`, { waitUntil: "networkidle", timeout: 30_000 })
			.catch(() => {});
		await sleep(1500);
		const p = `${SHOTS}/${id}-FAIL-${RUN}.png`;
		await page.screenshot({ path: p, fullPage: true });
		note(`截图 ${p}`);
		await page.close();
	} catch (e) {
		note(`截图失败：${e instanceof Error ? e.message : String(e)}`);
	}
}

// ---------------------------------------------------------------- 契约模型（只取用到的字段）

interface SetupRun {
	ok: boolean;
	setupRan: boolean;
	durationMs: number;
	prepared?: boolean;
}
interface OrbView {
	id: string;
	providerId: string;
	state: string;
	kind?: "orb" | "runner";
	features?: string[];
	projectId?: string;
	snapshotOf?: string;
	snapshotKind?: "project" | "fork";
	threadIds: string[];
	lastSetup?: SetupRun;
	failureReason?: string;
}
interface RunnerDir {
	path: string;
	name: string;
	git: boolean;
}
interface RunnerView {
	id: string;
	name: string;
	online: boolean;
	orbId: string;
	hostname?: string;
	dirs: RunnerDir[];
	features: string[];
	orbdVersion?: string;
}
interface ThreadView {
	id: string;
	state: string;
	orbId?: string;
	cwd?: string;
	worktree?: { repo: string; dir: string; branch: string };
	error?: { kind: string; reason: string };
}
interface ProviderView {
	id: string;
	name: string;
	online: boolean;
	driver?: string;
}
interface Snap {
	id: string;
	providerId: string;
	state: "building" | "ready" | "retired";
	sizeBytes?: number;
	retireReason?: string;
}
interface Project {
	id: string;
	name: string;
}

/** Firecracker guest 里 agent 用户的家目录。 */
const GUEST_HOME = "/home/user";

/** 经 Files API(PUT fs/file)把文本写进 orb,覆盖同名文件。 */
async function putOrbFile(
	orb: string,
	tid: string,
	path: string,
	body: string,
) {
	const res = await fetch(
		`${BASE}/api/v1/orbs/${orb}/fs/file?path=${encodeURIComponent(path)}&thread=${tid}&overwrite=true`,
		{
			method: "PUT",
			headers: {
				Authorization: `Bearer ${TOKEN}`,
				"X-Oryn-CSRF": "1",
				"Content-Type": "application/octet-stream",
			},
			body,
		},
	);
	if (!res.ok)
		throw new Error(`PUT fs/file ${path} -> ${res.status} ${await res.text()}`);
}

/** 经 Files API(GET fs/file)读回 orb 里的文本文件。 */
async function getOrbFile(orb: string, tid: string, path: string) {
	const res = await fetch(
		`${BASE}/api/v1/orbs/${orb}/fs/file?path=${encodeURIComponent(path)}&thread=${tid}`,
		{ headers: { Authorization: `Bearer ${TOKEN}` } },
	);
	const text = await res.text();
	if (!res.ok) throw new Error(`GET fs/file ${path} -> ${res.status} ${text}`);
	return text;
}

const orbView = (id: string) => getJson<OrbView>(`/api/v1/orbs/${id}`);
// GET threads/{id} 直接返回 Thread（建线程才是 ThreadEnvelope {thread}）
const threadView = (tid: string) =>
	getJson<ThreadView>(`/api/v1/threads/${tid}`);
const runners = async () =>
	(await getJson<{ runners: RunnerView[] }>("/api/v1/runners")).runners;
const orbDbState = (orb: string) =>
	one<{ state: string }>(`select state from orbs where id='${orb}'`)?.state ??
	"?";

// ---------------------------------------------------------------- 线程

interface Entry {
	seq: number;
	type: string;
	payload: Record<string, unknown>;
}

async function entriesOf(tid: string): Promise<Entry[]> {
	const out: Entry[] = [];
	let after = 0;
	for (let i = 0; i < 200; i++) {
		const p = await getJson<{ entries: Entry[]; hasMore: boolean }>(
			`/api/v1/threads/${tid}/entries?after=${after}&limit=500`,
		);
		out.push(...p.entries);
		const last = p.entries.at(-1);
		if (!p.hasMore || !last) break;
		after = last.seq;
	}
	return out;
}

function textOf(c: unknown): string {
	if (typeof c === "string") return c;
	if (!Array.isArray(c)) return "";
	return c
		.map((x) =>
			x &&
			typeof x === "object" &&
			"type" in x &&
			x.type === "text" &&
			"text" in x
				? String(x.text ?? "")
				: "",
		)
		.join("");
}

/** 最后一条带文本的 assistant 消息。 */
async function lastAssistant(tid: string): Promise<string> {
	const es = await entriesOf(tid);
	for (let i = es.length - 1; i >= 0; i--) {
		const e = es[i];
		if (e?.type !== "message") continue;
		const m = e.payload.message as
			| { role?: string; content?: unknown }
			| undefined;
		if (m?.role !== "assistant") continue;
		const t = textOf(m.content).trim();
		if (t) return t;
	}
	return "";
}

interface ThreadRow {
	state: string;
	last_seq: number;
	orb_id: string | null;
	created_at: number;
}
const threadRow = (tid: string) =>
	one<ThreadRow>(
		`select state, last_seq, orb_id, created_at from threads where id='${tid}'`,
	);
function orbOf(tid: string): string {
	const o = threadRow(tid)?.orb_id;
	if (!o) throw new Error(`线程 ${tid} 没有 orb`);
	return o;
}

const SETTLED: Record<string, true> = {
	idle: true,
	awaiting_input: true,
	error: true,
};

/** 等 beforeSeq 之后出现助手消息且线程静止，返回状态。 */
async function waitTurn(tid: string, beforeSeq: number, timeoutMs = 900_000) {
	return waitFor(
		`线程 ${tid} 回合结束`,
		() => {
			const r = threadRow(tid);
			if (!r || !SETTLED[r.state] || r.last_seq <= beforeSeq) return undefined;
			const n = one<{ n: number }>(
				`select count(*) n from entries where thread_id='${tid}' and seq>${beforeSeq} and type='message' and instr(cast(payload as text), '"role":"assistant"')>0`,
			)?.n;
			return n ? r.state : undefined;
		},
		timeoutMs,
		1000,
	);
}

async function waitIdle(tid: string, beforeSeq = 0) {
	const st = await waitTurn(tid, beforeSeq);
	check(st === "idle", `${tid} 到 idle（${st}）`);
}

/** 「耗时」= 线程从创建到首次离开 provisioning（毫秒）。 */
async function leaveProvisioning(tid: string, timeoutMs = 1_200_000) {
	return waitFor(
		`线程 ${tid} 离开 provisioning`,
		() => {
			const r = threadRow(tid);
			return r && r.state !== "provisioning"
				? Math.max(1, Date.now() - r.created_at)
				: undefined;
		},
		timeoutMs,
		200,
	);
}

/** 发一条消息并等到回合结束，返回最后一条 assistant 文本与发出时刻 / 用时。 */
async function say(tid: string, msg: string, timeoutMs = 900_000) {
	const before = threadRow(tid)?.last_seq ?? 0;
	const t0 = Date.now();
	const r = await cli(`thread send ${tid} ${q(msg)}`);
	if (r.code !== 0) throw new Error(`thread send 失败：${r.out}`);
	const st = await waitTurn(tid, before, timeoutMs);
	const ms = Date.now() - t0;
	check(st === "idle", `${tid} 回合结束于 idle（${st}，${sec(ms)}）`);
	return { text: await lastAssistant(tid), t0, ms };
}

async function ensureAwake(tid: string): Promise<string> {
	const o = orbOf(tid);
	if (orbDbState(o) !== "ready")
		await api("POST", `/api/v1/threads/${tid}/wake`);
	return waitFor(
		`线程 ${tid} 的 orb ready`,
		() => (orbDbState(o) === "ready" ? o : undefined),
		300_000,
		1000,
	);
}

async function waitOrbDeleted(orb: string, timeoutMs = 180_000) {
	await waitFor(
		`${orb} deleted`,
		() => orbDbState(orb) === "deleted",
		timeoutMs,
		1000,
	);
}

// ---------------------------------------------------------------- 浏览器（线程页右面板、终端；照 m3）

interface TermDebug {
	terminalId: string;
	kind: string;
	status: string;
}

const terminals = (page: Page, tid: string) =>
	page.evaluate(
		(t) =>
			(
				window as unknown as {
					__orynDebug?: { terminals(t: string): unknown[] };
				}
			).__orynDebug?.terminals(t) ?? [],
		tid,
	) as Promise<TermDebug[]>;

const termText = (page: Page, tid: string, id: string) =>
	page.evaluate(
		([t, i]) =>
			(
				window as unknown as {
					__orynDebug?: { terminalText(t: string, i: string): string };
				}
			).__orynDebug?.terminalText(t as string, i as string) ?? "",
		[tid, id],
	);

async function openTab(page: Page, tab: "changes" | "files" | "terminal") {
	const btn = page.locator(`[data-panel-tab=${tab}]`);
	await btn.waitFor({ timeout: 60_000 });
	if ((await btn.getAttribute("aria-selected")) !== "true") await btn.click();
}

const panelTabs = (page: Page) =>
	page
		.locator("[data-panel-tab]")
		.evaluateAll((els) =>
			els.map((e) => e.getAttribute("data-panel-tab") ?? ""),
		);

/** 打开线程页的 Terminal 标签，返回一个 live 的 shell 终端 id。 */
async function openShell(page: Page, tid: string) {
	await openTab(page, "terminal");
	return waitFor(
		"live shell 终端",
		async () => {
			const wake = page.locator("[data-testid=terminal-offline] button");
			if (await wake.isVisible().catch(() => false)) await wake.click();
			const ts = await terminals(page, tid);
			return ts.find((t) => t.kind === "shell" && t.status === "live")
				?.terminalId;
		},
		120_000,
		500,
	);
}

async function typeLine(page: Page, id: string, line: string) {
	const tab = page.locator(`[data-terminal-tab="${id}"]`);
	if ((await tab.getAttribute("aria-selected")) !== "true") await tab.click();
	const view = page.locator(
		`[data-testid=terminal-view][data-terminal-id="${id}"]`,
	);
	await view.waitFor({ timeout: 10_000 });
	await view.click();
	await page.keyboard.type(line, { delay: 5 });
	await page.keyboard.press("Enter");
}

const hasLine = (s: string, line: string) =>
	s.split("\n").some((l) => l.trimEnd() === line);

// ---------------------------------------------------------------- runner

let uhome = "";
let hostPrepared = false;

/** 以 runner 用户在干净环境里跑一段 bash。 */
const asUser = (cmd: string) =>
	sh(
		`runuser -u ${RUSER} -- env -i HOME=${q(uhome)} USER=${RUSER} LOGNAME=${RUSER} PATH=/usr/local/bin:/usr/bin:/bin bash -c ${q(cmd)}`,
	);

/** 宿主 models.yml 里 provider → 字面 apiKey 的前 12 位（跳过 `!命令`、环境变量名与过短的值）。 */
function hostModelKeys(): Map<string, string> {
	const f = `${homedir()}/.omp/agent/models.yml`;
	const out = new Map<string, string>();
	if (!existsSync(f)) return out;
	const doc = Bun.YAML.parse(readFileSync(f, "utf8")) as {
		providers?: Record<string, { apiKey?: unknown } | null>;
	} | null;
	for (const [p, v] of Object.entries(doc?.providers ?? {})) {
		const k = typeof v?.apiKey === "string" ? v.apiKey : "";
		if (k.startsWith("!") || /^[A-Z_][A-Z0-9_]*$/.test(k) || k.length < 12)
			continue;
		out.set(p, k.slice(0, 12));
	}
	return out;
}

/**
 * guest 环境里「名=值前 12 位」形态的密钥变量中不合规的名字：值不是占位符，且不是「降级变量名、值正是该 provider
 * 自己的 key」。allowed 只来自宿主配置与控制面元数据。
 */
function envSecretViolations(
	envVars: string[],
	allowed: Map<string, string>,
): string[] {
	return envVars
		.map((x) => x.split("="))
		.filter(([n = "", v = ""]) => {
			if (v.startsWith("oryn-ph-")) return false;
			return allowed.get(n) !== v;
		})
		.map(([n = ""]) => n);
}

/** 用户、二进制副本、模型配置与目录（每个进程一次）。 */
function prepareRunnerHost() {
	if (hostPrepared) return;
	if (process.getuid?.() !== 0)
		throw new Error("runner 场景要以 root 运行（useradd / runuser）");
	must(
		sh(`id -u ${RUSER} >/dev/null 2>&1 || useradd -m -s /bin/bash ${RUSER}`),
		`建用户 ${RUSER}`,
	);
	uhome = must(sh(`getent passwd ${RUSER} | cut -d: -f6`), "取家目录");
	// 上次中断遗留的 runner（同一用户只能跑一个实例，也会让二进制 Text file busy）
	sh(`pkill -KILL -u ${RUSER}`);
	must(
		sh(
			`install -d -m 755 ${STAGE} ${RBIN} && install -m 755 ${q(`${ROOT}/bin/orbd`)} ${RORBD} && install -m 755 ${q(OMP_SRC)} ${RBIN}/omp`,
		),
		"拷贝 orbd / omp",
	);
	snote(
		`用户 ${RUSER}（HOME ${uhome}）；orbd → ${RORBD}；omp（${OMP_SRC}）→ ${RBIN}/omp`,
	);
	const models = `${homedir()}/.omp/agent/models.yml`;
	if (existsSync(models)) {
		must(
			sh(
				`install -d -o ${RUSER} -g ${RUSER} -m 700 ${q(`${uhome}/.omp`)} ${q(`${uhome}/.omp/agent`)} && install -o ${RUSER} -g ${RUSER} -m 600 ${q(models)} ${q(`${uhome}/.omp/agent/models.yml`)}`,
			),
			"拷贝 models.yml",
		);
		snote(
			`已把 root 的 ~/.omp/agent/models.yml 拷到 ${uhome}/.omp/agent/models.yml（0600，属主 ${RUSER}），runner 上的 agent 用它应答${hostModelKeys().size ? "" : "；注意：其中没有字面 apiKey（`!命令` / 环境变量形态），该用户下可能取不到 key"}`,
		);
	} else
		snote(`root 没有 ~/.omp/agent/models.yml：runner 上的 agent 可能无法应答`);
	must(
		sh(
			`rm -rf ${DIR_A} ${DIR_A}-* ${DIR_B} ${DIR_C} && install -d -o ${RUSER} -g ${RUSER} -m 755 ${DIR_A} ${DIR_B} ${DIR_C}`,
		),
		"重建目录",
	);
	must(
		asUser(
			`cd ${DIR_A} && git init -q -b main && echo m11b > README.md && git add README.md && git ${GIT_ID} commit -qm init`,
		),
		`git init ${DIR_A}`,
	);
	snote(
		`${DIR_A}（git 仓库，1 个提交）、${DIR_B}、${DIR_C} 已重建，属主 ${RUSER}`,
	);
	hostPrepared = true;
}

let runnerSeq = 0;
let runner: RunnerView | undefined;
let runnerToken = "";
let runnerProc: Subprocess | undefined;
let terminalFlag = false;

const runnerName = () => `box-${RUN}${runnerSeq > 1 ? `-${runnerSeq}` : ""}`;
const orbdPid = () =>
	sh(`pgrep -u ${RUSER} -f ${q(`^${RORBD} run`)}`).stdout.split("\n")[0] ?? "";

async function createRunner(): Promise<RunnerView> {
	runnerSeq++;
	const name = runnerName();
	const r = await cli(`runner create ${name}`);
	if (r.code !== 0) throw new Error(`oryn runner create 失败：${r.out}`);
	runnerToken = r.stdout.trim().split("\n")[0] ?? "";
	check(
		runnerToken.startsWith("ort_"),
		`oryn runner create ${name} → ort_ 令牌`,
	);
	writeFileSync(`${ROOT}/.dev/runner.token`, `${runnerToken}\n`, {
		mode: 0o600,
	});
	const rv = (await runners()).find((x) => x.name === name);
	if (!rv) throw new Error(`GET runners 里没有 ${name}`);
	runner = rv;
	note(`runner ${name} = ${rv.id} orb=${rv.orbId}`);
	return rv;
}

function startRunner(terminal: boolean) {
	terminalFlag = terminal;
	const env = [
		`HOME=${uhome}`,
		`USER=${RUSER}`,
		`LOGNAME=${RUSER}`,
		"SHELL=/bin/bash",
		"LANG=C.UTF-8",
		`PATH=${RBIN}:/usr/local/bin:/usr/bin:/bin`,
		`ORYN_URL=${RUNNER_URL}`,
		`ORYN_RUNNER_TOKEN=${runnerToken}`,
	]
		.map(q)
		.join(" ");
	const args = `run --runner --dir ${DIR_A} --dir ${DIR_B}${terminal ? " --terminal" : ""}`;
	runnerProc = Bun.spawn(
		[
			"bash",
			"-c",
			`exec runuser -u ${RUSER} -- env -i ${env} ${RORBD} ${args} >>${q(RUNNER_LOG)} 2>&1`,
		],
		{ cwd: "/", stdin: "ignore", stdout: "ignore", stderr: "ignore" },
	);
	note(`起 runner：orbd ${args}（日志 ${RUNNER_LOG}）`);
}

async function stopRunner() {
	const p = runnerProc;
	if (!p) return;
	runnerProc = undefined;
	sh(`pkill -TERM -u ${RUSER} -f ${q(`^${RORBD} run`)}`);
	const done = await Promise.race([
		p.exited.then(() => true),
		sleep(15_000).then(() => false),
	]);
	if (!done) {
		sh(`pkill -KILL -u ${RUSER}`);
		p.kill(9);
		await p.exited;
	}
}

async function waitRunnerOnline(wantTerminal: boolean): Promise<RunnerView> {
	const id = runner?.id;
	if (!id) throw new Error("还没有 runner");
	return waitFor(
		`runner ${id} 在线${wantTerminal ? "且带 terminal" : ""}`,
		async () => {
			if (runnerProc?.exitCode != null)
				throw new Error(
					`runner 进程已退出（${runnerProc.exitCode}）：${sh(`tail -n 20 ${q(RUNNER_LOG)}`).stdout}`,
				);
			const r = (await runners()).find((x) => x.id === id);
			return r?.online && r.features.includes("terminal") === wantTerminal
				? r
				: undefined;
		},
		90_000,
		1000,
	);
}

async function ensureRunnerOnline(): Promise<RunnerView> {
	prepareRunnerHost();
	if (!runner) await createRunner();
	if (!runnerProc) startRunner(terminalFlag);
	return waitRunnerOnline(terminalFlag);
}

/** 用户的 git 全局配置与 ~/.agents 的指纹（X24 比对前后）。 */
function userFootprint() {
	const g = asUser("git config --global -l 2>&1; echo rc=$?");
	const a = asUser(
		'cd ~ && if [ -e .agents ]; then find .agents -printf "%p %s %T@\\n" | sort; else echo absent; fi',
	);
	return { git: g.stdout, agents: a.stdout };
}

interface RunnerThreadOpts {
	dir: string;
	prompt?: string;
	tag: string;
	worktree?: string;
	shareDir?: boolean;
}

async function postRunnerThread(o: RunnerThreadOpts): Promise<ApiRes> {
	const r = runner;
	if (!r) throw new Error("还没有 runner");
	return api("POST", "/api/v1/threads", {
		prompt: o.prompt ?? "只回复 ok",
		title: `m11b ${RUN} ${o.tag}`,
		executor: {
			kind: "runner",
			runnerId: r.id,
			dir: o.dir,
			...(o.worktree ? { worktree: { name: o.worktree } } : {}),
			...(o.shareDir ? { shareDir: true } : {}),
		},
	});
}

const PROMPT_25 = "用 bash 执行 pwd && hostname，只回复输出";
let t25 = "";
let t25Ms = 0;

/** X25 的线程：`oryn thread new --runner … --dir /tmp/m11-a`，等到 idle。 */
async function ensureT25(): Promise<string> {
	const r = await ensureRunnerOnline();
	if (t25 && threadRow(t25)?.orb_id === r.orbId) return t25;
	const out = await cli(
		`thread new --runner ${r.name} --dir ${DIR_A} --title ${q(`m11b ${RUN} X25`)} ${q(PROMPT_25)}`,
	);
	if (out.code !== 0)
		throw new Error(`oryn thread new --runner 失败：${out.out}`);
	const tid = out.stdout.trim().split("\n").at(-1) ?? "";
	focus = tid;
	t25Ms = await leaveProvisioning(tid, 300_000);
	await waitIdle(tid);
	t25 = tid;
	note(`X25 线程 ${tid}（runner orb ${r.orbId}，耗时 ${sec(t25Ms)}）`);
	return tid;
}

// ---------------------------------------------------------------- Firecracker

const fcOrbs = new Set<string>();
let fcT = "";
let keepFor = "";
let otherT = "";
let fcProjectId = "";
const NOT_LIVE = new Set(["stopped", "deleting", "deleted", "failed"]);

async function fcProvider(): Promise<ProviderView> {
	const ps = (await getJson<{ providers: ProviderView[] }>("/api/v1/providers"))
		.providers;
	const p = ps.find((x) => x.name === FC_NAME);
	if (!p?.online)
		throw new Skip(
			`provider ${FC_NAME} ${p ? "不在线" : "不存在"}（由父 agent 以 dev/provider-fc.yaml 起）`,
		);
	return p;
}

const fcLive = async (pid: string) =>
	(await getJson<{ orbs: OrbView[] }>("/api/v1/orbs")).orbs.filter(
		(o) => o.providerId === pid && !NOT_LIVE.has(o.state),
	);

const fcProcs = (orb: string) =>
	sh("pgrep -af firecracker")
		.stdout.split("\n")
		.filter((l) => l.includes(orb));

const dockerByName = (orb: string) =>
	sh(`docker ps -aq --filter name=${q(orb)}`).stdout;
const dockerIds = () =>
	new Set(sh("docker ps -q --no-trunc").stdout.split("\n").filter(Boolean));

/** images/fc/artifacts.lock.json 里 guest 内核的 x.y.z。 */
function guestKernel(): string {
	const lock = JSON.parse(readFileSync(FC_LOCK, "utf8")) as {
		kernel?: { sourceTag?: string };
	};
	const v = lock.kernel?.sourceTag?.match(/(\d+\.\d+\.\d+)/)?.[1];
	if (!v)
		throw new Error(
			`${FC_LOCK} 的 kernel.sourceTag 里没有版本号：${lock.kernel?.sourceTag}`,
		);
	return v;
}

const PROMPT_32 = "用 bash 执行 uname -r && cat /proc/1/comm，只回复输出";

/** X32 的 microVM 线程（CLI 建）：`oryn thread new --no-project --provider fc …`。 */
async function createFcMain() {
	const out = await cli(
		`thread new --no-project --provider ${FC_NAME} --title ${q(`m11b ${RUN} microVM`)} ${q(PROMPT_32)}`,
	);
	if (out.code !== 0)
		throw new Error(`oryn thread new --provider 失败：${out.out}`);
	const id = out.stdout.trim().split("\n").at(-1) ?? "";
	focus = id;
	const o0 = threadRow(id)?.orb_id;
	if (o0) fcOrbs.add(o0);
	const ms = await leaveProvisioning(id);
	await waitIdle(id);
	const orb = orbOf(id);
	fcOrbs.add(orb);
	fcT = id;
	keepFor = "";
	note(`microVM 线程 ${id} orb=${orb} 耗时 ${sec(ms)}`);
	return { id, orb, ms };
}

async function ensureFcThread(): Promise<string> {
	if (fcT) {
		const o = threadRow(fcT)?.orb_id;
		if (o && !NOT_LIVE.has(orbDbState(o))) return fcT;
		if (o && orbDbState(o) === "stopped") {
			await ensureAwake(fcT);
			return fcT;
		}
	}
	return (await createFcMain()).id;
}

/** 让 microVM 线程的 agent 写 ~/keep.txt（每台 orb 一次）。 */
async function ensureKeep(): Promise<string> {
	const t = await ensureFcThread();
	focus = t;
	const orb = orbOf(t);
	if (keepFor !== orb) {
		const r = await say(
			t,
			`用 bash 执行 echo ${KEEP} > ~/keep.txt && cat ~/keep.txt，只回复输出`,
		);
		check(r.text.includes(KEEP), `写 ~/keep.txt：${r.text.slice(0, 80)}`);
		keepFor = orb;
	}
	return t;
}

interface FcThreadOpts {
	prompt?: string;
	tag: string;
	projectId?: string;
}

async function startFcThread(o: FcThreadOpts): Promise<ThreadView> {
	const r = await api("POST", "/api/v1/threads", {
		...(o.projectId ? { projectId: o.projectId } : {}),
		prompt: o.prompt ?? "只回复 ok",
		title: `m11b ${RUN} ${o.tag}`,
		executor: { kind: "new_orb", provider: FC_NAME },
	});
	if (r.status !== 201)
		throw new Error(`建 ${FC_NAME} 线程（${o.tag}）失败 ${r.status} ${r.text}`);
	const t = (r.json as { thread: ThreadView }).thread;
	focus = t.id;
	if (t.orbId) fcOrbs.add(t.orbId);
	return t;
}

async function newFcIdle(o: FcThreadOpts) {
	const t = await startFcThread(o);
	const ms = await leaveProvisioning(t.id);
	await waitIdle(t.id);
	const orb = orbOf(t.id);
	fcOrbs.add(orb);
	note(`${o.tag} = ${t.id} orb=${orb} 耗时 ${sec(ms)}`);
	return { id: t.id, orb, ms };
}

async function deleteOrb(orb: string) {
	const d = await api("DELETE", `/api/v1/orbs/${orb}`);
	check(d.status === 202, `DELETE orbs/${orb} → ${d.status}`);
	await waitOrbDeleted(orb);
}

const fcMeta = (orb: string) =>
	JSON.parse(readFileSync(`${FC_STATE}/vms/${orb}/meta.json`, "utf8")) as {
		guestIP: string;
		base: string;
		tap: string;
	};

/** 文件实际占用（块数 × 512）与表观大小。 */
function usage(path: string): { alloc: number; size: number } {
	const st = statSync(path);
	return { alloc: st.blocks * 512, size: st.size };
}

/** 回复里的 `KEY=VALUE` 行（容忍代码围栏与反引号）。 */
function parseKv(text: string): Record<string, string> {
	const out: Record<string, string> = {};
	for (const raw of text.split("\n")) {
		const m = raw
			.replaceAll("`", "")
			.trim()
			.match(/^([A-Z]+)=(.*)$/);
		if (m?.[1]) out[m[1]] = (m[2] ?? "").trim();
	}
	return out;
}

// ---------------------------------------------------------------- 场景：runner

async function X24() {
	note(
		"建 runner（oryn runner create）并以普通用户起 orbd run --runner，核对 GET runners、端口与用户配置",
	);
	await stopRunner();
	prepareRunnerHost();
	const before = userFootprint();
	note(
		`起之前：git config --global -l → ${before.git.replaceAll("\n", " | ")}；~/.agents → ${before.agents.replaceAll("\n", " | ")}`,
	);
	if (!runner) await createRunner();
	startRunner(false);
	const r = await waitRunnerOnline(false);
	note(
		`GET runners：online=${r.online} host=${r.hostname} dirs=${r.dirs.map((d) => `${d.path}${d.git ? "(git)" : ""}`).join(",")} features=${r.features.join(",")} orbd=${r.orbdVersion}`,
	);
	check(r.online, "runner online");
	const paths = r.dirs.map((d) => d.path).sort();
	check(
		paths.length === 2 && paths[0] === DIR_A && paths[1] === DIR_B,
		`两个目录 ${paths.join(", ")}`,
	);
	check(
		r.dirs.find((d) => d.path === DIR_A)?.git === true,
		`${DIR_A} 标为 git`,
	);
	check(
		r.features.includes("fs") && r.features.includes("git"),
		`特性含 fs、git（${r.features.join(",")}）`,
	);
	check(!r.features.includes("terminal"), "特性不含 terminal");
	const pid = orbdPid();
	check(pid !== "", `runner orbd 进程 pid=${pid}`);
	const listens = sh("ss -ltnpH")
		.stdout.split("\n")
		.filter((l) => l.includes(`pid=${pid},`));
	note(
		`该进程的 TCP 监听：${listens.map((l) => l.split(/\s+/)[3]).join(", ") || "无"}`,
	);
	check(
		!listens.some((l) => /:7811\b/.test(l)),
		"runner 进程没有监听 7811（egress 代理不开）",
	);
	await sleep(3000);
	const after = userFootprint();
	check(after.git === before.git, "该用户的 git config --global -l 前后一致");
	check(after.agents === before.agents, "该用户的 ~/.agents 前后一致");
	for (const s of setupNotes) note(`准备：${s}`);
}

async function X25() {
	note(
		"oryn thread new --runner … --dir /tmp/m11-a 让 agent 执行 pwd && hostname；核对回复、会话目录与 docker",
	);
	const r = await ensureRunnerOnline();
	const dockerBefore = dockerIds();
	t25 = "";
	const tid = await ensureT25();
	const text = await lastAssistant(tid);
	note(`回复：${text.replaceAll("\n", " | ").slice(0, 200)}`);
	check(text.includes(DIR_A), `回复含 ${DIR_A}`);
	const host = hostname();
	check(text.includes(host), `回复含宿主主机名 ${host}`);
	const tv = await threadView(tid);
	check(tv.cwd === DIR_A, `thread.cwd = ${tv.cwd}`);
	check(tv.orbId === r.orbId, `thread.orbId = runner 的 orb ${r.orbId}`);
	const sess = `${uhome}/.oryn/threads/${tid}`;
	const ls = asUser(`ls -A ${q(sess)} 2>/dev/null | head -n 20`);
	note(`会话目录 ${sess}：${ls.stdout.replaceAll("\n", " ") || "（空）"}`);
	check(
		ls.code === 0 && ls.stdout !== "",
		"会话文件在该用户的 ~/.oryn/threads/<T>/",
	);
	check(dockerByName(r.orbId) === "", "没有以 runner orb 命名的容器");
	const added = [...dockerIds()].filter((x) => !dockerBefore.has(x));
	check(added.length === 0, `docker ps 没有新增容器（${added.length}）`);
	note(`耗时（创建到离开 provisioning）${sec(t25Ms)}`);
}

async function X26() {
	note(
		"面板按声明：API 的 fs 范围、线程页的标签；重启 runner 加 --terminal 后终端标签出现并可输入",
	);
	const tid = await ensureT25();
	const r = await ensureRunnerOnline();
	focus = tid;
	const ov = await orbView(r.orbId);
	note(`orb kind=${ov.kind} features=${ov.features?.join(",")}`);
	check(ov.kind === "runner", "orb.kind = runner");
	const roots = await api(
		"GET",
		`/api/v1/orbs/${r.orbId}/fs/roots?thread=${tid}`,
	);
	const rj = roots.json as { home?: string; workdir?: string } | null;
	check(
		roots.status === 200 && rj?.home === DIR_A && rj.workdir === DIR_A,
		`fs/roots?thread → ${roots.status} home=${rj?.home} workdir=${rj?.workdir}`,
	);
	const bare = await api("GET", `/api/v1/orbs/${r.orbId}/fs/roots`);
	check(
		bare.status === 400 && codeOf(bare) === "thread_required",
		`不带 thread → ${bare.status} ${codeOf(bare)}`,
	);
	const etc = await api(
		"GET",
		`/api/v1/orbs/${r.orbId}/fs/list?path=/etc&thread=${tid}`,
	);
	check(
		etc.status === 403,
		`fs/list?path=/etc&thread → ${etc.status} ${codeOf(etc)}`,
	);
	const page = await newPage();
	try {
		await page.goto(`${BASE}/t/${tid}`);
		await page.locator("[data-panel-tab=files]").waitFor({ timeout: 60_000 });
		await sleep(2000);
		const tabs = await panelTabs(page);
		note(`线程页标签：${tabs.join(",")}`);
		check(
			tabs.includes("files") && tabs.includes("changes"),
			"有 Files / Changes",
		);
		check(!tabs.includes("terminal"), "没有 Terminal");
		await openTab(page, "files");
		const titles = await waitFor(
			"Files 面板的根",
			async () => {
				const t = await page
					.locator('button[role="tab"][title^="/"]')
					.evaluateAll((els) => els.map((e) => e.getAttribute("title") ?? ""));
				return t.length ? t : undefined;
			},
			60_000,
		);
		check(
			titles.every((t) => t === DIR_A),
			`Files 的根 = ${[...new Set(titles)].join(",")}`,
		);
		await shot(page, "X26-files");
		await stopRunner();
		startRunner(true);
		const r2 = await waitRunnerOnline(true);
		note(`重启后特性：${r2.features.join(",")}`);
		await page.reload();
		await page
			.locator("[data-panel-tab=terminal]")
			.waitFor({ timeout: 60_000 });
		check(true, "Terminal 标签出现");
		const id = await openShell(page, tid);
		await typeLine(page, id, "echo m11b-$((6*7)); pwd");
		const text = await waitFor(
			"终端出现 m11b-42",
			async () => {
				const s = await termText(page, tid, id);
				return hasLine(s, "m11b-42") ? s : undefined;
			},
			30_000,
			300,
		);
		check(hasLine(text, "m11b-42"), "终端可输入（输出 m11b-42）");
		note(`终端里 pwd：${hasLine(text, DIR_A) ? DIR_A : "未见"}`);
		await shot(page, "X26-terminal");
	} finally {
		await page.close();
	}
}

async function X27() {
	note(
		"目录边界：/etc 与 /tmp/m11-a/../m11-c 被控制面拒绝；指向 /etc 的符号链接由 runner 拒绝",
	);
	await ensureRunnerOnline();
	for (const dir of ["/etc", `${DIR_A}/../m11-c`]) {
		const r = await postRunnerThread({ dir, tag: `X27 ${dir}` });
		check(
			r.status === 400 && codeOf(r) === "dir_not_served",
			`--dir ${dir} → ${r.status} ${codeOf(r)}`,
		);
	}
	must(asUser(`ln -sfn /etc ${DIR_A}/esc`), "建符号链接");
	const esc = `${DIR_A}/esc`;
	const r = await postRunnerThread({ dir: esc, tag: "X27 esc" });
	check(r.status === 201, `--dir ${esc}（→ /etc）→ ${r.status}`);
	const tid = (r.json as { thread: ThreadView }).thread.id;
	focus = tid;
	const tv = await waitFor(
		`${tid} 进 error`,
		async () => {
			const t = await threadView(tid);
			return t.state === "error" ? t : undefined;
		},
		180_000,
		1000,
	);
	note(`线程 ${tid} error=${JSON.stringify(tv.error)}`);
	check(
		JSON.stringify(tv.error ?? {}).includes("dir_not_served"),
		"原因含 dir_not_served",
	);
}

async function X28() {
	note(
		"同目录：dir_in_use → --share-dir → --worktree fix；worktree 有未提交改动时移除被拒，提交后移除成功",
	);
	const tid25 = await ensureT25();
	const a = await postRunnerThread({ dir: DIR_A, tag: "X28 同目录" });
	const ids = (a.json as { threadIds?: string[] } | null)?.threadIds ?? [];
	check(
		a.status === 409 && codeOf(a) === "dir_in_use" && ids.includes(tid25),
		`同目录 → ${a.status} ${codeOf(a)} threadIds=${ids.join(",")}`,
	);
	const b = await postRunnerThread({
		dir: DIR_A,
		tag: "X28 share",
		shareDir: true,
	});
	check(b.status === 201, `--share-dir → ${b.status}`);
	const bt = (b.json as { thread: ThreadView }).thread;
	focus = bt.id;
	await waitIdle(bt.id);
	const c = await postRunnerThread({
		dir: DIR_A,
		tag: "X28 worktree",
		worktree: "fix",
		prompt: "用 bash 执行 echo m11b > dirty.txt && pwd，只回复输出",
	});
	const ct = (c.json as { thread?: ThreadView } | null)?.thread;
	const wt = `${DIR_A}-fix`;
	check(
		c.status === 201 && ct?.cwd === wt,
		`--worktree fix → ${c.status} cwd=${ct?.cwd}`,
	);
	if (!ct) return;
	focus = ct.id;
	note(`thread.worktree = ${JSON.stringify(ct.worktree)}`);
	check(
		ct.worktree?.dir === wt && ct.worktree.branch === "fix",
		"worktree.dir / branch",
	);
	const list = asUser(`git -C ${DIR_A} worktree list`).stdout;
	note(`git worktree list：${list.replaceAll("\n", " | ")}`);
	check(list.includes(wt), `worktree list 含 ${wt}`);
	await waitIdle(ct.id);
	const text = await lastAssistant(ct.id);
	note(`回复：${text.slice(0, 120)}`);
	check(existsSync(`${wt}/dirty.txt`), "agent 在 worktree 里写了 dirty.txt");
	const rm1 = await api("POST", `/api/v1/threads/${ct.id}/worktree/remove`);
	check(
		rm1.status === 409 && codeOf(rm1) === "worktree_dirty",
		`有未提交改动时移除 → ${rm1.status} ${codeOf(rm1)}`,
	);
	check(existsSync(wt), "目录仍在");
	must(
		asUser(`cd ${wt} && git add -A && git ${GIT_ID} commit -qm m11b`),
		"在 worktree 里提交",
	);
	const rm2 = await api("POST", `/api/v1/threads/${ct.id}/worktree/remove`);
	check(rm2.status === 204, `提交后移除 → ${rm2.status} ${rm2.text}`);
	check(!existsSync(wt), "目录消失");
	const after = await threadView(ct.id);
	check(!after.worktree, "thread.worktree 已清空");
}

async function X29() {
	note("杀掉 runner：35 秒后离线、线程页有提示、发送被接受；重启后应答");
	// 用本场景新建的线程，不复用 X25 的：X26 之后 runner 带 --terminal 重启，X25 的会话从无 terminal 工具变成有，
	// 下一回合 omp 会发 tool_addition，上游代理以「unknown tool 'terminal'」拒绝（omp / 上游行为，与离线送达无关）。
	const r0 = await ensureRunnerOnline();
	const created = await cli(
		`thread new --runner ${r0.name} --dir ${DIR_A} --share-dir --title ${q(`m11b ${RUN} X29`)} ${q("用 bash 执行 pwd，只回复输出")}`,
	);
	if (created.code !== 0)
		throw new Error(`oryn thread new --runner 失败：${created.out}`);
	const tid = created.stdout.trim().split("\n").at(-1) ?? "";
	focus = tid;
	await leaveProvisioning(tid, 300_000);
	await waitIdle(tid);
	note(`X29 线程 ${tid}（runner 带 --terminal=${terminalFlag}）`);
	await stopRunner();
	note("已杀掉 runner 进程，等 35 秒");
	await sleep(35_000);
	const rv = (await runners()).find((x) => x.id === r0.id);
	check(rv?.online === false, `GET runners online = ${rv?.online}`);
	note(`runner orb 状态 ${orbDbState(r0.orbId)}`);
	const page = await newPage();
	try {
		await page.goto(`${BASE}/t/${tid}`);
		const n = page.locator("[data-testid=runner-offline]");
		await n.waitFor({ timeout: 30_000 });
		note(`离线提示：${(await n.textContent())?.trim()}`);
		check(true, "线程页有离线提示");
		await shot(page, "X29-offline");
	} finally {
		await page.close();
	}
	const before = threadRow(tid)?.last_seq ?? 0;
	const token = `back-${RUN}`;
	const box: { r?: CliRes } = {};
	const sendP = cli(
		`thread send ${tid} ${q(`用 bash 执行 echo ${token}，只回复输出`)}`,
	).then((x) => {
		box.r = x;
		return x;
	});
	await waitFor("离线时 thread send 返回", () => box.r, 20_000).catch(
		() => undefined,
	);
	const offlineRes = box.r;
	note(
		`离线时 thread send：${offlineRes ? `${offlineRes.code} ${offlineRes.out}` : "20 秒内未返回"}`,
	);
	startRunner(terminalFlag);
	const restartAt = Date.now();
	await waitRunnerOnline(terminalFlag);
	note(`重启后 ${sec(Date.now() - restartAt)} 在线`);
	const s = await sendP;
	check(s.code === 0, `oryn thread send 退出码 ${s.code}`);
	const st = await waitTurn(tid, before, 300_000);
	const ms = Date.now() - restartAt;
	const text = await lastAssistant(tid);
	note(`应答（${st}，重启后 ${sec(ms)}）：${text.slice(0, 80)}`);
	check(text.includes(token), `应答含 ${token}`);
	check(ms <= 30_000, `重启后 30 秒内得到应答（${sec(ms)}）`);
	// `oryn thread send` 等到 agent 应答才退出（--timeout 默认 5m，含等 runner 重连）：离线期间不被拒绝、
	// 重连后以 0 退出并得到应答，即「发送被接受」
	check(
		offlineRes === undefined || offlineRes.code === 0,
		`离线期间发送没有被拒绝（${offlineRes ? `退出码 ${offlineRes.code}` : "仍在等 runner 重连"}）`,
	);
}

async function X30() {
	note("oryn runner rm：runner 的重连被拒；线程 orbId 为空、转录可读");
	const tid = await ensureT25();
	const r0 = await ensureRunnerOnline();
	focus = tid;
	const off = existsSync(RUNNER_LOG) ? statSync(RUNNER_LOG).size : 0;
	const rm = await cli(`runner rm ${r0.name}`);
	check(rm.code === 0, `oryn runner rm ${r0.name} → ${rm.code} ${rm.out}`);
	runner = undefined;
	const line = await waitFor(
		"runner 日志里重连被拒",
		() =>
			readFileSync(RUNNER_LOG)
				.subarray(off)
				.toString()
				.split("\n")
				.find((l) => /unauthori[sz]ed|rejected|revoked|\b401\b/i.test(l)),
		120_000,
		1000,
	);
	check(true, `runner 重连被拒：${line.slice(0, 200)}`);
	await stopRunner();
	t25 = "";
	const listed = (await runners()).some((x) => x.id === r0.id);
	check(!listed, "GET runners 不再列出");
	note(`runner orb ${r0.orbId} 状态 ${orbDbState(r0.orbId)}`);
	const tv = await threadView(tid);
	check(!tv.orbId, `线程 orbId 为空（${tv.orbId ?? "省略"}）`);
	const es = await entriesOf(tid);
	const text = await lastAssistant(tid);
	check(
		es.length > 0 && text.length > 0,
		`转录可读（${es.length} 条，最后回复 ${text.slice(0, 40)}）`,
	);
}

// ---------------------------------------------------------------- 场景：Firecracker

async function X31() {
	note("oryn provider preflight --config dev/provider-fc.yaml 逐项检查");
	await fcProvider();
	const r = sh(`${q(ORYN)} provider preflight --config ${q(FC_CONFIG)}`);
	for (const l of r.out.split("\n")) note(`  ${l}`);
	check(r.code === 0, `退出码 ${r.code}`);
	const lines = r.stdout.split("\n").filter(Boolean);
	check(!lines.some((l) => l.startsWith("✗")), "没有失败项");
	const kernel = lines.find((l) => l.includes("宿主内核"));
	const mm = release().split(".").slice(0, 2).join(".");
	const supported = ["5.10", "6.1", "6.18"].includes(mm);
	check(
		!!kernel && kernel.startsWith(supported ? "✓" : "!"),
		`宿主内核 ${release()} 一项为${supported ? "通过" : "警告"}`,
	);
	check(
		lines.filter((l) => l !== kernel).every((l) => l.startsWith("✓")),
		"其余各项 ok",
	);
}

async function X32() {
	note("microVM 线程：uname -r 与 PID 1；宿主有 firecracker 进程、没有新容器");
	const fc = await fcProvider();
	const kv = guestKernel();
	note(
		`artifacts.lock.json 的内核版本 ${kv}；当前 ${FC_NAME} 运行中 VM ${(await fcLive(fc.id)).length} 台`,
	);
	const dockerBefore = dockerIds();
	const t = await createFcMain();
	const text = await lastAssistant(t.id);
	note(`回复：${text.replaceAll("\n", " | ").slice(0, 160)}`);
	check(text.includes(kv), `回复含内核版本 ${kv}`);
	check(/\borbd\b/.test(text), "回复含 orbd（PID 1）");
	const v = await orbView(t.orb);
	check(v.providerId === fc.id, `orb.providerId = ${FC_NAME}`);
	const procs = fcProcs(t.orb);
	note(`firecracker 进程：${procs.join(" | ").slice(0, 300)}`);
	check(procs.length > 0, "宿主有带该 orb id 的 firecracker 进程");
	check(dockerByName(t.orb) === "", "没有以该 orb 命名的容器");
	const added = [...dockerIds()].filter((x) => !dockerBefore.has(x));
	check(added.length === 0, `docker ps 没有新增容器（${added.length}）`);
	note(`耗时（创建到离开 provisioning）${sec(t.ms)}`);
}

async function X33() {
	note(
		"空闲到 frozen：api.sock 报 Paused；发消息执行 date +%s，15 秒内应答且与宿主时间相差 < 2 秒",
	);
	await fcProvider();
	const tid = await ensureFcThread();
	focus = tid;
	const orb = orbOf(tid);
	const t0 = Date.now();
	await waitFor(
		"orb frozen",
		() => orbDbState(orb) === "frozen",
		900_000,
		1000,
	);
	note(`${orb} 等待 ${sec(Date.now() - t0)} 后 frozen`);
	const sock = `${FC_STATE}/jail/firecracker/${orb}/root/api.sock`;
	const info = must(
		sh(`curl -sS --unix-socket ${q(sock)} http://localhost/`),
		"curl api.sock",
	);
	note(`api.sock /：${info.slice(0, 200)}`);
	check(
		(JSON.parse(info) as { state?: string }).state === "Paused",
		"state = Paused",
	);
	const r = await say(tid, "用 bash 执行 date +%s，只回复输出", 300_000);
	const end = Date.now();
	const g = Number(r.text.match(/\b(\d{10})\b/)?.[1] ?? Number.NaN);
	const lo = Math.floor(r.t0 / 1000);
	const hi = Math.ceil(end / 1000);
	const off = g < lo ? lo - g : g > hi ? g - hi : 0;
	note(
		`guest ${g}，宿主发送 ${lo} … 应答 ${hi}；超出该窗口 ${off} 秒；用时 ${sec(r.ms)}`,
	);
	check(r.ms <= 15_000, `15 秒内应答（${sec(r.ms)}）`);
	check(Number.isFinite(g) && off < 2, "guest 时间与宿主相差 < 2 秒");
}

async function X34() {
	note(
		"写 ~/keep.txt → pause {mode: stop}：进程与 tap 消失、disk.ext4 在 → resume 后内容还在",
	);
	await fcProvider();
	const tid = await ensureKeep();
	const orb = orbOf(tid);
	const tap = fcMeta(orb).tap;
	const p = await api("POST", `/api/v1/orbs/${orb}/pause`, { mode: "stop" });
	check(p.status === 202, `POST orbs/${orb}/pause {mode: stop} → ${p.status}`);
	await waitFor(
		"orb stopped",
		() => orbDbState(orb) === "stopped",
		180_000,
		1000,
	);
	check(fcProcs(orb).length === 0, "firecracker 进程消失");
	const link = sh(`ip link show ${q(tap)}`);
	check(link.code !== 0, `tap ${tap} 消失`);
	const disk = `${FC_STATE}/vms/${orb}/disk.ext4`;
	check(existsSync(disk), `${disk} 在（实际占用 ${mib(usage(disk).alloc)}）`);
	const r = await api("POST", `/api/v1/orbs/${orb}/resume`);
	check(r.status === 202, `POST orbs/${orb}/resume → ${r.status}`);
	const t0 = Date.now();
	await waitFor("orb ready", () => orbDbState(orb) === "ready", 300_000, 1000);
	note(`恢复用时 ${sec(Date.now() - t0)}`);
	const s = await say(tid, "用 bash 执行 cat ~/keep.txt，只回复输出");
	check(s.text.includes(KEEP), `恢复后 ~/keep.txt = ${s.text.slice(0, 60)}`);
}

async function X35() {
	note(
		"连环境 fork 带 ~/keep.txt；fc 项目线程两次（第二次来自快照）；快照文件稀疏",
	);
	const fc = await fcProvider();
	const tid = await ensureKeep();
	await ensureAwake(tid);
	const r = await api("POST", `/api/v1/threads/${tid}/fork`, { env: "copy" });
	check(
		r.status === 201,
		`fork copy → ${r.status} ${r.status === 201 ? "" : r.text}`,
	);
	const f = (r.json as { thread: { id: string } }).thread.id;
	focus = f;
	await waitFor(
		`${f} 就绪并静止`,
		() => {
			const row = threadRow(f);
			if (row?.orb_id) fcOrbs.add(row.orb_id);
			return row && row.last_seq > 0 && SETTLED[row.state];
		},
		900_000,
		1000,
	);
	const fo = orbOf(f);
	fcOrbs.add(fo);
	const fv = await orbView(fo);
	note(`fork 线程 ${f} orb=${fo} snapshotOf=${fv.snapshotOf}`);
	check(fv.snapshotKind === "fork", "fork 出的 orb snapshotKind = fork");
	check(fv.providerId === fc.id, `fork 出的 orb 在 ${FC_NAME} 上`);
	const k = await say(f, "用 bash 执行 cat ~/keep.txt，只回复输出");
	check(
		k.text.includes(KEEP),
		`fork 出的 VM 里 ~/keep.txt = ${k.text.slice(0, 60)}`,
	);
	await deleteOrb(fo);
	let pid = fcProjectId;
	if (!pid) {
		const name = `m11b-${RUN}-fc`;
		const c = await api("POST", "/api/v1/projects", {
			name,
			setupScript: SETUP_FC,
			snapshots: true,
			warmOrbs: 0,
		});
		if (c.status !== 201)
			throw new Error(`建项目 ${name} 失败 ${c.status} ${c.text}`);
		pid = (c.json as Project).id;
		fcProjectId = pid;
		note(`建项目 ${name}（setup：${SETUP_FC}）`);
	}
	const a = await newFcIdle({ projectId: pid, tag: "项目线程 #1" });
	const av = await orbView(a.orb);
	note(
		`#1 lastSetup：setupRan=${av.lastSetup?.setupRan} prepared=${av.lastSetup?.prepared} ${sec(av.lastSetup?.durationMs ?? 0)}`,
	);
	const snap = await waitFor(
		`${FC_NAME} 上的 ready 快照`,
		async () =>
			(
				await getJson<{ snapshots: Snap[] }>(
					`/api/v1/projects/${pid}/snapshots`,
				)
			).snapshots.find((s) => s.state === "ready" && s.providerId === fc.id),
		600_000,
		2000,
	);
	note(`快照 ${snap.id} sizeBytes=${snap.sizeBytes ?? "未上报"}`);
	const b = await newFcIdle({ projectId: pid, tag: "项目线程 #2" });
	const bv = await orbView(b.orb);
	note(
		`#2 snapshotOf=${bv.snapshotOf} lastSetup：setupRan=${bv.lastSetup?.setupRan} prepared=${bv.lastSetup?.prepared}`,
	);
	check(bv.snapshotOf === snap.id, "#2 来自该快照");
	check(bv.lastSetup?.prepared === true, "#2 prepared = true");
	check(bv.lastSetup?.setupRan === false, "#2 setupRan = false");
	const file = `${FC_STATE}/snapshots/${snap.id}.ext4`;
	const base = `${FC_STATE}/bases/${fcMeta(b.orb).base}.ext4`;
	const su = usage(file);
	const bu = usage(base);
	note(
		`快照 ${file}：实际 ${mib(su.alloc)} / 表观 ${mib(su.size)}；基础盘 ${base}：实际 ${mib(bu.alloc)} / 表观 ${mib(bu.size)}`,
	);
	check(su.alloc * 4 < bu.alloc, "快照文件的实际占用远小于基础盘（< 1/4）");
	await deleteOrb(a.orb);
	await deleteOrb(b.orb);
}

/** 起一个只在本场景存活的 HTTP 服务（python3 -m http.server），返回停止函数。 */
function hostListener(port: number) {
	const dir = `${STAGE}/probe-${RUN}`;
	mkdirSync(dir, { recursive: true });
	const p = Bun.spawn(
		["python3", "-m", "http.server", String(port), "--bind", "0.0.0.0"],
		{ cwd: dir, stdin: "ignore", stdout: "ignore", stderr: "ignore" },
	);
	return () => p.kill(9);
}

/** 宿主上 curl 一次，返回 HTTP 状态码（连不上为 000）。 */
const hostCurl = (url: string) =>
	sh(`curl -s -m 5 -o /dev/null -w '%{http_code}' ${q(url)}`).stdout;

async function X36() {
	note(
		"guest 内网络隔离与密钥：每条「应当不通」的路径都先放一个真实监听、从宿主确认可达（阳性对照），再断言 guest 到不了；外网与 TLS link 端口可达；配置与环境只有占位符",
	);
	const fc = await fcProvider();
	const tid = await ensureFcThread();
	if (!otherT || NOT_LIVE.has(orbDbState(threadRow(otherT)?.orb_id ?? "")))
		otherT = (await newFcIdle({ tag: "另一台 guest" })).id;
	const otherOrb = await ensureAwake(otherT);
	const otherIp = fcMeta(otherOrb).guestIP;
	const lan =
		sh("ip -4 route get 1.1.1.1").stdout.match(/\bsrc (\S+)/)?.[1] ?? "";
	check(lan !== "", `宿主出口地址 ${lan}`);
	const H = 18091;
	const G = 18092;
	const D = 18093;
	const probeBox = `m11b-probe-${RUN}`;
	const stopHost = hostListener(H);
	try {
		// 阳性对照 1：宿主上 0.0.0.0:H 的服务，经 oryn-fc 地址与出口地址都可达
		const hc = await waitFor(
			`宿主 :${H} 监听就绪`,
			() => (hostCurl(`http://${FC_HOST}:${H}/`) === "200" ? true : undefined),
			15_000,
			300,
		);
		check(
			hc && hostCurl(`http://${lan}:${H}/`) === "200",
			`对照：宿主自己能访问 ${FC_HOST}:${H} 与 ${lan}:${H}`,
		);
		// 阳性对照 2：另一台 guest 里 0.0.0.0:G 的服务，本机与宿主都可达
		focus = otherT;
		const og = await say(
			otherT,
			// 冷启动的 python3 在 guest 里可能要好几秒才开始监听：轮询到 200（最多 15 秒）再回报
			`用 bash 执行 setsid nohup python3 -m http.server ${G} --bind 0.0.0.0 >/dev/null 2>&1 < /dev/null & for i in $(seq 30); do c=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:${G}/); [ "$c" = 200 ] && break; sleep 0.5; done; echo LOCAL=$c ，只回复输出`,
		);
		check(
			og.text.includes("LOCAL=200"),
			`对照：另一台 guest 本机 :${G} → ${og.text.slice(0, 40)}`,
		);
		check(
			hostCurl(`http://${otherIp}:${G}/`) === "200",
			`对照：宿主能访问另一台 guest ${otherIp}:${G}`,
		);
		// 阳性对照 3：Docker 网桥上的私有地址
		must(
			sh(
				`docker run -d --rm --name ${probeBox} --entrypoint python3 oryn-orb:dev -m http.server ${D} --bind 0.0.0.0`,
			),
			"起 Docker 探针容器",
		);
		const boxIp = sh(
			`docker inspect ${probeBox} --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'`,
		).stdout;
		await waitFor(
			`宿主能访问 Docker 探针 ${boxIp}:${D}`,
			() => (hostCurl(`http://${boxIp}:${D}/`) === "200" ? true : undefined),
			30_000,
			500,
		);
		check(true, `对照：宿主能访问 Docker 网桥上的 ${boxIp}:${D}`);

		const self = await ensureAwake(tid);
		note(
			`在 ${self} 里探测；另一台 guest ${otherOrb} = ${otherIp}；Docker 探针 ${boxIp}；provider ${fc.id}`,
		);
		focus = tid;
		const script = [
			`P=$(for d in /proc/[0-9]*; do [ "$(tr '\\0' ' ' < $d/cmdline 2>/dev/null)" = "orbd run " ] && basename $d; done | head -n1)`,
			`{ cat /proc/$P/environ 2>/dev/null || sudo -n cat /proc/$P/environ; } > /tmp/orbd.env`,
			`grep -z '^ORYN_ORB_CA_PEM=' /tmp/orbd.env | tr -d '\\0' | sed '1s/^ORYN_ORB_CA_PEM=//' > /tmp/ca.pem`,
			`c(){ code=$(curl -s -o /dev/null -w '%{http_code}' "$@"); rc=$?; echo "$code/$rc"; }`,
			`echo "GUEST=$(c -m 5 http://${otherIp}:${G}/)"`,
			`echo "HOSTPORT=$(c -m 5 http://${FC_HOST}:${H}/)"`,
			`echo "LAN=$(c -m 5 http://${lan}:${H}/)"`,
			`echo "PRIV=$(c -m 5 http://${boxIp}:${D}/)"`,
			`echo "WEB=$(c -m 10 https://example.com)"`,
			`echo "TLS=$(c -m 5 --cacert /tmp/ca.pem https://${FC_HOST}:7643/orb/v1/whoami)"`,
			`echo "CA=$(grep -c 'BEGIN CERTIFICATE' /tmp/ca.pem)"`,
			`echo "PH=$( (cat ~/.omp/agent/models.yml; env) | grep -o 'oryn-ph-[A-Za-z0-9_-]*' | sort -u | wc -l)"`,
			`echo "APIKEYS=$(grep -E '^[[:space:]]*apiKey:' ~/.omp/agent/models.yml | sed -E 's/^[[:space:]]*apiKey:[[:space:]]*//' | tr -d "\\"' " | cut -c1-12 | sort -u | tr '\\n' ',')"`,
			`echo "ENVSECRETS=$(env | grep -E '^[A-Za-z_]*(KEY|TOKEN|SECRET)[A-Za-z_]*=' | sed -E 's/=(.{0,12}).*/=\\1/' | tr '\\n' ',')"`,
		].join("\n");
		// 脚本经 Files API 放进 guest、输出写文件再经 Files API 读回：
		// 模型只看到「执行一个脚本文件」，不经手探测内容和输出（直接贴脚本会被上游按「导出密钥」拒答）。
		const probe = `${GUEST_HOME}/oryn-probe-${RUN}.sh`;
		const out = `${GUEST_HOME}/oryn-probe-${RUN}.out`;
		await putOrbFile(self, tid, probe, `${script}\n`);
		await say(
			tid,
			`用 bash 执行 \`bash ${probe} > ${out} 2>&1\`，执行完只回复 done`,
		);
		const r = { text: await getOrbFile(self, tid, out) };
		const kv = parseKv(r.text);
		note(`探测输出：${r.text.replaceAll("\n", " | ").slice(0, 600)}`);
		for (const k of [
			"GUEST",
			"HOSTPORT",
			"LAN",
			"PRIV",
			"WEB",
			"TLS",
			"CA",
			"PH",
			"APIKEYS",
			"ENVSECRETS",
		])
			if (!(k in kv))
				throw new Error(`回复里没有 ${k}=…：${r.text.slice(0, 400)}`);
		const res = (k: string) => {
			const [code = "", rc = ""] = (kv[k] ?? "").split("/");
			return { code, rc: Number(rc) };
		};
		// 不通 = 没拿到 HTTP 应答（000）且是连不上 / 超时（7 / 28）
		const blocked = (k: string) => {
			const x = res(k);
			return x.code === "000" && (x.rc === 7 || x.rc === 28);
		};
		check(
			blocked("GUEST"),
			`guest 到另一台 guest ${otherIp}:${G} 不通（${kv.GUEST}）`,
		);
		check(
			blocked("HOSTPORT"),
			`guest 到宿主 ${FC_HOST}:${H}（未放行端口）不通（${kv.HOSTPORT}）`,
		);
		check(blocked("LAN"), `guest 到宿主出口地址 ${lan}:${H} 不通（${kv.LAN}）`);
		check(
			blocked("PRIV"),
			`guest 到 Docker 网桥 ${boxIp}:${D} 不通（${kv.PRIV}）`,
		);
		const web = res("WEB");
		check(
			web.rc === 0 && /^[23]\d\d$/.test(web.code),
			`https://example.com 可达（${kv.WEB}）`,
		);
		check(Number(kv.CA) >= 1, `orbd 环境里有 ORYN_ORB_CA_PEM（${kv.CA}）`);
		const tls = res("TLS");
		check(
			tls.rc === 0 && tls.code !== "000",
			`TLS ${FC_HOST}:7643/orb/v1/whoami 拿到 HTTP 应答（${kv.TLS}）`,
		);
		const keys = (kv.APIKEYS ?? "").split(",").filter(Boolean);
		note(
			`models.yml 的 apiKey（前 12 位）：${keys.join(",") || "无"}；占位符 ${kv.PH} 个；环境里的密钥形态变量：${kv.ENVSECRETS || "无"}`,
		);
		check(Number(kv.PH) >= 1, "配置 / 环境里有 oryn-ph- 占位符");
		// 没绑定上游主机（控制面 secrets 里 hosts 为空）的 provider key 按设计走环境变量降级（docs/deploy.md「models.yml」
		// 一节）：apiKey 写成 ORYN_MODEL_KEY_<KEY>，真实值在环境里。豁免表只由宿主 models.yml（provider → 字面 key）与
		// 控制面的 hosts 元数据决定，不取被测输出：降级变量名下的值必须正是该 provider 自己的 key，其余 key 一律不得出现。
		const secrets = (
			await getJson<{ secrets: { name: string; hosts?: string[] }[] }>(
				"/api/v1/secrets",
			)
		).secrets;
		const fallbackProviders = new Set(
			secrets
				.filter((s) => (s.hosts ?? []).length === 0)
				.map((s) => s.name.match(/^omp\/models\/([^/]+)\/apiKey$/)?.[1])
				.filter((k): k is string => k !== undefined),
		);
		const hostKeys = hostModelKeys();
		/** 降级变量名 → 允许的真实值（宿主 models.yml 里该 provider 的字面 key 前 12 位）。 */
		const allowed = new Map<string, string>();
		for (const p of fallbackProviders) {
			const v = hostKeys.get(p);
			if (v)
				allowed.set(
					`ORYN_MODEL_KEY_${p.toUpperCase().replace(/[^A-Z0-9]/g, "_")}`,
					v,
				);
		}
		const secretKeys = [...hostKeys]
			.filter(([p]) => !fallbackProviders.has(p))
			.map(([, v]) => v);
		note(
			`环境变量降级的 provider：${[...fallbackProviders].join(",") || "无"}；其余字面 key ${secretKeys.length} 个`,
		);
		check(
			keys.every(
				(k) =>
					k.startsWith("oryn-ph-") ||
					[...allowed.keys()].some((n) => n.startsWith(k)),
			),
			"models.yml 的 apiKey 都是占位符或降级变量名",
		);
		const envVars = (kv.ENVSECRETS ?? "").split(",").filter(Boolean);
		const bad = envSecretViolations(envVars, allowed);
		check(
			bad.length === 0,
			`环境里的密钥形态变量都是占位符，或值正是该 provider 自己 key 的降级变量（${envVars.map((x) => x.split("=")[0]).join(",") || "无"}${bad.length ? `；违规 ${bad.join(",")}` : ""}）`,
		);
		// 负例：把一个非降级 provider 的真实 key 塞进允许的降级变量名，判定必须报违规（防止断言自我豁免）
		const [someAllowed] = allowed.keys();
		const [someSecret] = secretKeys;
		if (someAllowed && someSecret)
			check(
				envSecretViolations([`${someAllowed}=${someSecret}`], allowed)
					.length === 1,
				`负例：${someAllowed} 里装着非降级 key 会被判违规`,
			);
		check(
			secretKeys.every((p) => !r.text.includes(p)),
			`探测输出里没有非降级的真实模型 key（${secretKeys.length} 个，前 12 位）`,
		);
	} finally {
		// 另一台 guest 里的探针随 VM 删除而消失（收尾会删本次建的全部 fc orb）
		stopHost();
		sh(`docker rm -f ${probeBox} >/dev/null 2>&1`);
	}
}

async function X37() {
	note(
		`容量：已有 ${FC_MAX_VMS} 台运行中时第 4 台 failed（capacity）；删掉一台后在线程页点「重试」到 idle`,
	);
	const fc = await fcProvider();
	let live = await fcLive(fc.id);
	note(
		`当前运行中 ${live.length} 台：${live.map((o) => `${o.id}(${o.state})`).join(", ")}`,
	);
	let i = 0;
	while (live.length < FC_MAX_VMS) {
		await newFcIdle({ tag: `填充 #${++i}` });
		live = await fcLive(fc.id);
	}
	note(`已满 ${live.length} 台`);
	const t4 = await startFcThread({ tag: "第 4 台" });
	const o4 = t4.orbId ?? orbOf(t4.id);
	fcOrbs.add(o4);
	const failed = await waitFor(
		`${o4} failed`,
		async () => {
			const v = await orbView(o4);
			if (v.state === "ready") throw new Error(`${o4} 意外到了 ready`);
			return v.state === "failed" ? v : undefined;
		},
		600_000,
		2000,
	);
	note(`第 4 台 ${o4} failureReason=${failed.failureReason}`);
	check(
		(failed.failureReason ?? "").includes("capacity"),
		"failureReason 含 capacity",
	);
	const tv = await waitFor(
		`${t4.id} 进 error`,
		async () => {
			const t = await threadView(t4.id);
			return t.state === "error" ? t : undefined;
		},
		120_000,
		1000,
	);
	note(`线程 error=${JSON.stringify(tv.error)}`);
	const victim = live.find((o) => fcOrbs.has(o.id) && o.id !== o4);
	if (!victim)
		throw new Error(
			`运行中的 ${FC_NAME} VM 都不是本脚本建的，不便删除：${live.map((o) => o.id).join(",")}`,
		);
	await deleteOrb(victim.id);
	note(`删掉 ${victim.id}`);
	const page = await newPage();
	try {
		await page.goto(`${BASE}/t/${t4.id}`);
		const btn = page.getByRole("button", { name: "重试", exact: true }).first();
		await btn.waitFor({ timeout: 60_000 });
		await shot(page, "X37-failed");
		await btn.click();
		note("线程页点了「重试」");
	} finally {
		await page.close();
	}
	const st = await waitTurn(t4.id, 0, 900_000);
	check(st === "idle", `重试后线程到 idle（${st}）`);
	check(orbOf(t4.id) === o4, "仍是同一台 orb");
	note(`${o4} 状态 ${orbDbState(o4)}`);
}

const TESTS: [string, () => Promise<void>][] = [
	["X24", X24],
	["X25", X25],
	["X26", X26],
	["X27", X27],
	["X28", X28],
	["X29", X29],
	["X30", X30],
	["X31", X31],
	["X32", X32],
	["X33", X33],
	["X34", X34],
	["X35", X35],
	["X36", X36],
	["X37", X37],
];

// ---------------------------------------------------------------- 收尾

let cleaned = false;
async function cleanup() {
	if (cleaned) return;
	cleaned = true;
	const cn = (s: string) => {
		cleanupNotes.push(s);
		console.log(`    · ${s}`);
	};
	await stopRunner().catch((e) => cn(`停 runner 失败：${String(e)}`));
	if (hostPrepared) sh(`pkill -KILL -u ${RUSER}`);
	if (runner) {
		const r = await api("DELETE", `/api/v1/runners/${runner.id}`).catch(
			(e) => ({ status: 0, text: String(e) }) as ApiRes,
		);
		cn(`删除 runner ${runner.name} → ${r.status}`);
		runner = undefined;
	}
	const left = [...fcOrbs].filter(
		(o) => !["deleting", "deleted", "?"].includes(orbDbState(o)),
	);
	for (const o of left) {
		const r = await api("DELETE", `/api/v1/orbs/${o}`).catch(
			(e) => ({ status: 0, text: String(e) }) as ApiRes,
		);
		cn(`删除 ${FC_NAME} orb ${o} → ${r.status}`);
	}
	await Promise.all(
		left.map((o) =>
			waitOrbDeleted(o, 180_000).catch((e) =>
				cn(`${o} 未删干净：${String(e)}`),
			),
		),
	);
	if (fcProjectId) {
		const r = await api(
			"DELETE",
			`/api/v1/projects/${fcProjectId}/snapshots`,
		).catch((e) => ({ status: 0, text: String(e) }) as ApiRes);
		cn(`清掉项目 ${fcProjectId} 的快照 → ${r.status}`);
	}
	await browser?.close().catch(() => {});
}

for (const s of ["SIGINT", "SIGTERM"] as const)
	process.on(s, () => {
		console.log(`\n收到 ${s}，清理后退出`);
		void cleanup().finally(() => process.exit(130));
	});

// ---------------------------------------------------------------- 主流程

console.log(
	`M11b 验收 run=${RUN} base=${BASE} runner 用户 ${RUSER} provider ${FC_NAME} shots=${SHOTS}`,
);

try {
	for (const [id, fn] of TESTS) {
		if (ONLY && !ONLY.has(id)) continue;
		evidence = [];
		focus = "";
		console.log(`── ${id}`);
		const t0 = Date.now();
		const took = () => Number(((Date.now() - t0) / 1000).toFixed(1));
		try {
			await fn();
			console.log(`${id} PASS (${took()}s)`);
			results.push({ id, verdict: "PASS", seconds: took(), evidence });
		} catch (e) {
			if (e instanceof Skip) {
				note(`跳过：${e.message}`);
				console.log(`${id} SKIP (${took()}s)`);
				results.push({ id, verdict: "SKIP", seconds: took(), evidence });
				continue;
			}
			note(`错误：${e instanceof Error ? e.message : String(e)}`);
			await failArtifacts(id);
			console.log(`${id} FAIL (${took()}s)`);
			results.push({ id, verdict: "FAIL", seconds: took(), evidence });
		}
	}
} finally {
	console.log("── 收尾");
	await cleanup();
}

const out = `${SHOTS}/results-${RUN}.json`;
await Bun.write(
	out,
	JSON.stringify(
		{ run: RUN, setup: setupNotes, cleanup: cleanupNotes, results },
		null,
		2,
	),
);
console.log(`\n汇总（${out}）`);
for (const r of results) console.log(`${r.id} ${r.verdict} ${r.seconds}s`);
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
