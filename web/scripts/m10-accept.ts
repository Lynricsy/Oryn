#!/usr/bin/env bun
/**
 * M10 真机验收（计划「验证」Y2–Y17 与预算修订的 Y17；Y1 自动化、Y18 基准另跑）：对 dev 部署（orynd + docker provider）
 * 逐项执行，每项打印 `Y<n> PASS|FAIL` 与证据，失败时给打开的页面截图。
 *
 * 用法（web/ 目录）：bun run accept:m10 [-- --only Y2,Y3]
 * 环境变量：ORYN_URL、ORYN_ROOT、ORYN_TOKEN（同 m5）；M10_THREAD（复用已有的 $T，缺省复用 .dev/m10.thread，
 *   没有则新建）；M10_NO_RESTART=1 跳过 Y16 的 orynd 重启；CHROMIUM；M10_SHOTS（默认 /tmp/oryn-shots/m10-accept）。
 * CLI 一律 `./bin/oryn`，`ORYN_CREDENTIALS=$ROOT/.dev/credentials.json`。
 *
 * 场景依赖：Y3–Y11 依赖 Y2 建的 $T；Y5 建 $F1 / $F2（写入 .dev/m10.forks）；Y10 归档 $T（之后 $T 不再发消息，
 * 所以 Y8 在 Y10 之前）；Y11 删除 $F1。单独 `--only` 时缺什么现建什么。
 *
 * 依赖的界面选择器（切片 D 按此实现）：
 *   页脚成本 `a[href^="/stats/usage?thread="]`；指标 `[data-testid=kpi-<name>]`（cost / tokens / requests …）；
 *   主图 `[data-testid=stats-chart]` 内每桶 `g[data-bucket]`；浮层 `[data-testid=chart-tooltip]`；
 *   范围按钮 `[data-range=<range>]`；分组切换 `[data-by=<by>]`；明细表行 `[data-row-key=<key>]`；
 *   标签 `a[href^="/stats/orbs"]` / `a[href^="/stats/output"]`；orb 列表行 `[data-orb-row=<orbId>]`；
 *   抽屉 `[data-testid=orb-drawer]`（内含 `[data-testid=timeline-strip]` 与两张 `[data-testid=line-chart]`）；
 *   导出 `[data-testid=stats-export]` → `[data-export=view-csv]`；预算卡 `[data-testid=budget-card]`，
 *   当日条 `[data-budget-period=day]` 带 `data-level`。
 */
import { createHmac } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Page,
} from "playwright-core";

const ROOT = process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`;
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const CREDS = `${ROOT}/.dev/credentials.json`;
const TOKEN =
	process.env.ORYN_TOKEN ??
	(JSON.parse(readFileSync(CREDS, "utf8")) as { token: string }).token;
const SHOTS = process.env.M10_SHOTS ?? "/tmp/oryn-shots/m10-accept";
const DB = `${ROOT}/.dev/data/oryn.db`;
const ORYN = `${ROOT}/bin/oryn`;
const RUN = Math.random().toString(36).slice(2, 6);
const PROJECT = "sandbox-m10";
const SANDBOX_REPO = "https://github.com/Lynricsy/oryn-setup-sandbox.git";
const SANDBOX_BRANCH = "main";
const TZ = "Asia/Shanghai";
const EPS = 1e-6;
const DAY = 86_400_000;
mkdirSync(SHOTS, { recursive: true });

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const te = new TextEncoder();

function sh(
	cmd: string,
	opts: { stdin?: string; cwd?: string } = {},
): { code: number; out: string; stdout: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], {
		cwd: opts.cwd ?? ROOT,
		stdin: opts.stdin !== undefined ? te.encode(opts.stdin) : undefined,
	});
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
		stdout: r.stdout.toString().trim(),
	};
}

const CLI_ENV = `ORYN_CREDENTIALS=${q(CREDS)}`;

/** `./bin/oryn <args>`（异步：本进程里的假 Webhook 要能应答）。 */
async function cli(args: string, env = "") {
	const p = Bun.spawn(["bash", "-c", `${CLI_ENV} ${env} ${q(ORYN)} ${args}`], {
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
	const r = Bun.spawnSync(["sqlite3", "-json", DB, qs]);
	const s = r.stdout.toString().trim();
	return s ? (JSON.parse(s) as T[]) : [];
}
const one = <T = Record<string, unknown>>(qs: string) => sql<T>(qs)[0];

interface ThreadRow {
	state: string;
	last_seq: number;
	orb_id: string | null;
	title: string | null;
	transcript_rev: number;
	created_at: number;
}
const threadRow = (tid: string) =>
	one<ThreadRow>(
		`select state, last_seq, orb_id, title, transcript_rev, created_at from threads where id='${tid}'`,
	);
const seqOf = (tid: string) => threadRow(tid)?.last_seq ?? 0;
function orbOf(tid: string): string {
	const o = threadRow(tid)?.orb_id;
	if (!o) throw new Error(`线程 ${tid} 没有 orb`);
	return o;
}
const orbState = (orb: string) =>
	one<{ state: string }>(`select state from orbs where id='${orb}'`)?.state ??
	"?";

const dexec = (orb: string, cmd: string) =>
	sh(`docker exec ${orb} bash -lc ${q(cmd)}`);

interface ApiRes {
	status: number;
	json: unknown;
	text: string;
	headers: Headers;
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
		// 非 JSON 体（CSV）
	}
	return { status: res.status, json, text, headers: res.headers };
}

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

/** 与 web/src/lib/format.ts 的 formatCost 同规则（那边依赖 i18n，脚本里不引）。 */
function formatCost(usd: number): string {
	if (!Number.isFinite(usd) || usd === 0) return "$0";
	if (usd < 0.01) return `$${usd.toFixed(4)}`;
	return `$${usd.toFixed(2)}`;
}
const near = (a: number, b: number) => Math.abs(a - b) <= EPS;

// ---------------------------------------------------------------- 结果

type Verdict = "PASS" | "FAIL";
const results: { id: string; verdict: Verdict; evidence: string[] }[] = [];
let evidence: string[] = [];
const note = (s: string) => {
	evidence.push(s);
	console.log(`    · ${s}`);
};
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};

async function shot(page: Page, name: string) {
	const p = `${SHOTS}/${name}.png`;
	await page.screenshot({ path: p, fullPage: true }).catch(() => {});
	note(`截图 ${p}`);
}

// ---------------------------------------------------------------- 统计接口

interface UsageStat {
	key: string;
	label?: string;
	start?: number;
	requests: number;
	requestErrors: number;
	inputTokens: number;
	outputTokens: number;
	cacheReadTokens: number;
	cacheWriteTokens: number;
	costUsd: number;
	toolCalls: number;
	toolErrors: number;
	turns: number;
}
interface UsageStats {
	groupBy: string;
	from: number;
	to: number;
	tz: string;
	total: UsageStat;
	groups: UsageStat[];
	series?: {
		key: string;
		start: number;
		total: UsageStat;
		groups: UsageStat[];
	}[];
}
interface ToolStats {
	total: { tool: string; calls: number; errors: number };
	tools: { tool: string; calls: number; errors: number }[];
}
interface OrbStat {
	key: string;
	label?: string;
	orbs: number;
	runningMs: number;
	frozenMs: number;
	stoppedMs: number;
	setupMs: number;
	spawns: number;
	snapshotSpawns: number;
	freezes: number;
	stops: number;
	wakes: number;
	failures: number;
}
interface OrbStats {
	total: OrbStat;
	groups: OrbStat[];
	setup: { runs: number; failures: number };
}
interface OutputStat {
	key: string;
	label?: string;
	threadsCreated: number;
	threadsShipped: number;
	threadsArchived: number;
	shippedCostUsd: number;
	commits: number;
	turns: number;
}
interface OutputStats {
	total: OutputStat;
	groups: OutputStat[];
}
interface BudgetPeriod {
	key: string;
	spentUsd: number;
	limitUsd?: number;
	level: "none" | "warn" | "over";
}
interface BudgetStatus {
	budget: {
		dailyUsd?: number;
		monthlyUsd?: number;
		warnPct: number;
		timezone: string;
		enabled: boolean;
	};
	day: BudgetPeriod;
	month: BudgetPeriod;
}

const qs = (p: Record<string, string | number | undefined>) =>
	Object.entries(p)
		.filter(([, v]) => v !== undefined)
		.map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`)
		.join("&");
const usage = (p: Record<string, string | number | undefined>) =>
	getJson<UsageStats>(`/api/v1/stats/usage?${qs(p)}`);

// ---------------------------------------------------------------- 线程

interface Entry {
	seq: number;
	file: string;
	entryId: string;
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

/** 条目里的 usage：assistant 消息取 message.usage；model_usage 取顶层 usage。 */
function usageOf(e: Entry): { cost: number } | null {
	if (e.type === "message") {
		const m = e.payload.message as
			| { role?: string; usage?: { cost?: { total?: number } } }
			| undefined;
		if (m?.role === "assistant" && m.usage)
			return { cost: m.usage.cost?.total ?? 0 };
		return null;
	}
	if (e.type === "model_usage") {
		const u = e.payload.usage as { cost?: { total?: number } } | undefined;
		if (u) return { cost: u.cost?.total ?? 0 };
	}
	return null;
}

async function projectId(): Promise<string> {
	const r = await getJson<{ projects: { id: string; name: string }[] }>(
		"/api/v1/projects",
	);
	const hit = r.projects.find((p) => p.name === PROJECT);
	if (hit) return hit.id;
	const c = await api("POST", "/api/v1/projects", {
		name: PROJECT,
		repoUrl: SANDBOX_REPO,
		baseBranch: SANDBOX_BRANCH,
		shallowClone: false,
	});
	if (c.status >= 300) throw new Error(`建项目失败 ${c.status} ${c.text}`);
	return (c.json as { id: string }).id;
}

const threadIdOf = (j: unknown) =>
	((j as { thread?: { id: string } }).thread?.id ??
		(j as { id: string }).id) as string;

const SETTLED: Record<string, true> = {
	idle: true,
	awaiting_input: true,
	error: true,
};

/** 等 beforeSeq 之后出现助手消息且线程静止。 */
async function waitTurn(tid: string, beforeSeq: number, timeoutMs = 300_000) {
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
	);
}

async function newThread(prompt: string, title: string): Promise<string> {
	const r = await api("POST", "/api/v1/threads", {
		projectId: await projectId(),
		prompt,
		title,
	});
	if (r.status !== 201) throw new Error(`建线程失败 ${r.status} ${r.text}`);
	const id = threadIdOf(r.json);
	await waitTurn(id, 0, 600_000);
	return id;
}

async function say(tid: string, msg: string, timeoutMs = 300_000) {
	const before = seqOf(tid);
	const r = await cli(`thread send ${tid} ${q(msg)}`);
	if (r.code !== 0) throw new Error(`thread send 失败：${r.out}`);
	await waitTurn(tid, before, timeoutMs);
}

async function ensureAwake(tid: string): Promise<string> {
	if (orbState(orbOf(tid)) !== "ready")
		await api("POST", `/api/v1/threads/${tid}/wake`);
	return waitFor(
		`线程 ${tid} 的 orb ready`,
		() => {
			const o = threadRow(tid)?.orb_id;
			return o && orbState(o) === "ready" ? o : undefined;
		},
		300_000,
		1000,
	);
}

let T = "";
let T_START = 0;
async function ensureT(): Promise<string> {
	if (T) return T;
	const f = `${ROOT}/.dev/m10.thread`;
	const env = process.env.M10_THREAD;
	const old = env ?? (existsSync(f) ? readFileSync(f, "utf8").trim() : "");
	if (old && threadRow(old)?.orb_id) {
		T = old;
	} else {
		T_START = Date.now();
		T = await newThread("用 bash 执行 `uname -a` 并只回复其输出", `m10 ${RUN}`);
		writeFileSync(f, T);
	}
	T_START ||= (threadRow(T)?.created_at ?? Date.now()) - 60_000;
	note(`$T = ${T}`);
	return T;
}

const FORKS = `${ROOT}/.dev/m10.forks`;
function forks(): { f1: string; f2: string } | null {
	if (!existsSync(FORKS)) return null;
	const v = JSON.parse(readFileSync(FORKS, "utf8")) as {
		f1: string;
		f2: string;
	};
	return threadRow(v.f1) || threadRow(v.f2) ? v : null;
}

// ---------------------------------------------------------------- 假 Webhook

interface HookReq {
	headers: Record<string, string>;
	json: {
		event?: string;
		id?: string;
		thread?: { id?: string };
		title?: string;
		body?: string;
		url?: string;
	};
	sigOk: boolean;
}
const HOOK_SECRET = `m10-secret-${RUN}`;
const hooks: HookReq[] = [];
const hookSrv = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(r: Request) {
		const raw = await r.text();
		const sig = r.headers.get("x-oryn-signature") ?? "";
		const want = `sha256=${createHmac("sha256", HOOK_SECRET).update(raw).digest("hex")}`;
		let json: HookReq["json"] = {};
		try {
			json = JSON.parse(raw) as HookReq["json"];
		} catch {
			// 非 JSON
		}
		hooks.push({
			headers: Object.fromEntries(r.headers.entries()),
			json,
			sigOk: sig === want,
		});
		return new Response("ok");
	},
});
const HOOK_URL = `http://127.0.0.1:${hookSrv.port}/hook`;
let hookChannel = "";

async function ensureHook(): Promise<string> {
	if (hookChannel) return hookChannel;
	const r = await api("POST", "/api/v1/notification-channels", {
		kind: "webhook",
		name: `m10-accept-webhook-${RUN}`,
		locale: "zh-CN",
		webhook: { url: HOOK_URL, secret: HOOK_SECRET },
	});
	if (r.status !== 201)
		throw new Error(`建 webhook 渠道 ${r.status} ${r.text}`);
	hookChannel = (r.json as { id: string }).id;
	const ev = (r.json as { events?: string[] }).events ?? [];
	note(`webhook 渠道 ${hookChannel}，events=${JSON.stringify(ev)}`);
	return hookChannel;
}

// ---------------------------------------------------------------- 浏览器

let ctx: BrowserContext;
const pageErrors: string[] = [];

/**
 * 下载用的独立上下文：extraHTTPHeaders 不作用于下载请求（会拿到 401 的 JSON），真实用户靠会话 Cookie。
 * 新建一个网页会话，按 domain + path 写 `__Host-` Cookie（给 url 会被 CDP 拒绝，做法同 m3-accept）。
 */
async function downloadContext(): Promise<BrowserContext> {
	const r = sh(
		`${q(`${ROOT}/bin/orynd`)} --config dev/orynd.yaml admin create-web-session --user-agent m10-accept`,
	);
	if (r.code !== 0) throw new Error(`create-web-session 失败：${r.out}`);
	const c = await browser.newContext({
		locale: "zh-CN",
		timezoneId: TZ,
		acceptDownloads: true,
	});
	await c.addCookies([
		{
			name: "__Host-oryn_session",
			value: r.stdout.trim(),
			domain: new URL(BASE).hostname,
			path: "/",
			secure: true,
			httpOnly: true,
			sameSite: "Lax",
		},
	]);
	return c;
}

async function newPage(viewport = { width: 1280, height: 900 }): Promise<Page> {
	const p = await ctx.newPage();
	await p.setViewportSize(viewport);
	p.on("pageerror", (e) => {
		pageErrors.push(e.message);
		console.log(`    [pageerror] ${e.message}`);
	});
	p.on("console", (m) => {
		if (m.type() === "error") pageErrors.push(`console: ${m.text()}`);
	});
	return p;
}

/** 本地日期 YYYY-MM-DD（在 tz 下）。 */
const localDay = (ms: number, tz: string) =>
	new Intl.DateTimeFormat("en-CA", {
		timeZone: tz,
		year: "numeric",
		month: "2-digit",
		day: "2-digit",
	}).format(ms);

// ---------------------------------------------------------------- 场景

async function Y2() {
	const tid = await ensureT();
	const es = await entriesOf(tid);
	let cost = 0;
	let reqs = 0;
	// omp 重启后会把子 agent（如 __advisor）的历史原样拷进新会话目录：同 id 同时间的条目只算一次（同服务端 ukey）
	const seen = new Set<string>();
	for (const e of es) {
		const u = usageOf(e);
		if (!u || seen.has(e.entryId)) continue;
		seen.add(e.entryId);
		cost += u.cost;
		reqs++;
	}
	const turns = es.filter(
		(e) =>
			e.type === "message" &&
			!e.file.includes("/") &&
			(e.payload.message as { role?: string } | undefined)?.role === "user",
	).length;
	const u = await usage({ groupBy: "thread", thread: tid, from: 0 });
	const g = u.groups.find((x) => x.key === tid);
	check(!!g, `groupBy=thread 有 $T 一组`);
	if (!g) return;
	note(
		`条目累加 cost=${cost} requests=${reqs} turns=${turns}；接口 cost=${g.costUsd} requests=${g.requests} turns=${g.turns}`,
	);
	check(near(g.costUsd, cost), "costUsd 等于条目 usage.cost.total 之和");
	check(g.requests === reqs, "requests 等于带用量的条目数");
	check(g.turns === turns, `turns 等于主文件 user 消息数（${turns}）`);

	const page = await newPage();
	await page.goto(`${BASE}/t/${tid}`);
	const link = page.locator(`a[href^="/stats/usage?thread=${tid}"]`).first();
	await link.waitFor({ timeout: 30_000 });
	// 统计接口返回前页脚先显示本地条目累加（条目也可能还没加载完），等它稳定到接口值
	const footer = await waitFor(
		`页脚成本变为 ${formatCost(g.costUsd)}`,
		async () => {
			const s = (await link.textContent())?.trim() ?? "";
			return s.includes(formatCost(g.costUsd)) ? s : undefined;
		},
		20_000,
		250,
	).catch(async () => (await link.textContent())?.trim() ?? "");
	check(
		footer.includes(formatCost(g.costUsd)),
		`页脚「${footer}」含 ${formatCost(g.costUsd)}`,
	);
	await link.click();
	await page.waitForURL(/\/stats\/usage\?/, { timeout: 15_000 });
	check(
		page.url().includes(`thread=${tid}`),
		`跳到 ${new URL(page.url()).pathname}${new URL(page.url()).search}`,
	);
	const kpi = page.locator("[data-testid=kpi-cost]");
	await kpi.waitFor({ timeout: 30_000 });
	const kt = (await kpi.textContent()) ?? "";
	check(
		kt.includes(formatCost(g.costUsd)),
		`统计页成本指标「${kt.trim().slice(0, 60)}」同值`,
	);
	await shot(page, "Y2-stats-thread");
	await page.close();
}

async function Y3() {
	const tid = await ensureT();
	const es = await entriesOf(tid);
	const want = new Map<string, number>();
	for (const e of es) {
		if (e.type !== "model_usage" || !e.payload.usage) continue;
		const p = String(e.payload.purpose ?? "") || "unknown";
		want.set(p, (want.get(p) ?? 0) + 1);
	}
	const u = await usage({ groupBy: "purpose", thread: tid, from: 0 });
	note(
		`分组：${u.groups.map((g) => `${g.key || "∅"}=${g.requests}`).join(" ")}`,
	);
	check(
		u.groups.some((g) => g.key === ""),
		"有对话回合（key 为空串）一组",
	);
	for (const [p, n] of want) {
		const g = u.groups.find((x) => x.key === p);
		check(
			g?.requests === n,
			`用途 ${p} 的 requests=${g?.requests} 等于条目数 ${n}`,
		);
	}
	if (want.size === 0)
		note("该线程没有 model_usage 条目（只验证了对话回合一组）");
}

function factCounts(tid: string) {
	return one<{ u: number; t: number; r: number; c: number; d: number }>(
		`select (select count(*) from usage_records where thread_id='${tid}') u,
		        (select count(*) from tool_calls where thread_id='${tid}') t,
		        (select count(*) from turns where thread_id='${tid}') r,
		        (select coalesce(sum(cost_usd),0) from usage_records where thread_id='${tid}') c,
		        (select count(distinct ukey) from usage_records where thread_id='${tid}') d`,
	);
}

async function Y4() {
	const tid = await ensureT();
	const before = factCounts(tid);
	const rev = threadRow(tid)?.transcript_rev ?? 0;
	note(
		`改写前 用量 ${before?.u} 工具 ${before?.t} 回合 ${before?.r} 成本 ${before?.c} rev ${rev}`,
	);
	const orb = await ensureAwake(tid);
	const r = dexec(
		orb,
		`f=$(ls ~/.oryn/threads/${tid}/*.jsonl | head -1); cp "$f" "$f.tmp" && mv "$f.tmp" "$f" && echo "$f"`,
	);
	check(r.code === 0, `原样改写会话文件 ${r.out}`);
	await waitFor(
		"transcript_rev 加一",
		() => (threadRow(tid)?.transcript_rev ?? 0) > rev,
		60_000,
	);
	await sleep(1500);
	const after = factCounts(tid);
	note(
		`改写后 用量 ${after?.u} 工具 ${after?.t} 回合 ${after?.r} 成本 ${after?.c} 去重键 ${after?.d}`,
	);
	check(
		after?.u === before?.u && after?.t === before?.t && after?.r === before?.r,
		"三张事实表行数不变",
	);
	check(near(after?.c ?? -1, before?.c ?? -2), "SUM(cost_usd) 不变");
	check(after?.u === after?.d, "count(*) 等于 count(distinct ukey)");
}

async function Y5() {
	const tid = await ensureT();
	// $T 的异步子 agent（如 __advisor）可能在回合结束后才补写用量：增量比较时把 $T 自己的变化扣掉
	const threadCost = async (t: string) =>
		(await usage({ groupBy: "thread", thread: t, from: 0 })).total.costUsd;
	const tot0 = (await usage({ groupBy: "total", from: 0 })).total;
	const t0 = await threadCost(tid);
	note(`fork 前全库 cost=${tot0.costUsd} requests=${tot0.requests}；$T ${t0}`);
	await ensureAwake(tid);
	const mk = async (env: "share" | "copy") => {
		const r = await api("POST", `/api/v1/threads/${tid}/fork`, { env });
		if (r.status !== 201) throw new Error(`fork ${env} ${r.status} ${r.text}`);
		return threadIdOf(r.json);
	};
	const f1 = await mk("share");
	const f2 = await mk("copy");
	writeFileSync(FORKS, JSON.stringify({ f1, f2 }));
	note(`$F1=${f1}（共用 orb） $F2=${f2}（连环境）`);
	for (const f of [f1, f2])
		await waitFor(
			`${f} 入库完成并静止`,
			() => {
				const r = threadRow(f);
				return r && r.last_seq > 0 && SETTLED[r.state];
			},
			600_000,
			1000,
		);
	await sleep(3000);
	for (const f of [f1, f2]) {
		const n = one<{ n: number }>(
			`select count(*) n from usage_records u join entries e on e.thread_id=u.thread_id and e.seq=u.entry_seq
			 where u.thread_id='${f}' and e.entry_id in (select entry_id from entries where thread_id='${tid}')`,
		)?.n;
		check(n === 0, `${f} 名下没有拷贝来的条目的用量行（${n}）`);
	}
	const tot1 = (await usage({ groupBy: "total", from: 0 })).total;
	const t1 = await threadCost(tid);
	const own = (await threadCost(f1)) + (await threadCost(f2));
	note(`fork 后全库 cost=${tot1.costUsd}；$T ${t1}；$F1+$F2 自身 ${own}`);
	check(
		near(tot1.costUsd - tot0.costUsd - (t1 - t0), own),
		"全库增量（扣除 $T 自身的迟到用量）等于两个 fork 自己的成本",
	);
	const tBefore = (await usage({ groupBy: "thread", thread: tid, from: 0 }))
		.total.costUsd;
	const fBefore = (await usage({ groupBy: "thread", thread: f1, from: 0 }))
		.total.costUsd;
	await say(f1, "只回复 ok");
	const tAfter = (await usage({ groupBy: "thread", thread: tid, from: 0 }))
		.total.costUsd;
	const fAfter = (await usage({ groupBy: "thread", thread: f1, from: 0 })).total
		.costUsd;
	note(`$F1 ${fBefore} → ${fAfter}；$T ${tBefore} → ${tAfter}`);
	check(fAfter > fBefore, "$F1 发消息后成本增加");
	check(near(tAfter, tBefore), "$T 的成本不变");
}

async function Y6() {
	const from = Date.now() - 7 * DAY;
	const a = await usage({ groupBy: "day", from, tz: TZ });
	const b = await usage({ groupBy: "day", from, tz: "UTC" });
	note(
		`${TZ} 桶 ${a.groups.length} 个，UTC 桶 ${b.groups.length} 个；成本 ${a.total.costUsd} / ${b.total.costUsd}`,
	);
	check(near(a.total.costUsd, b.total.costUsd), "两种时区 total.costUsd 相等");
	for (const [u, tz] of [
		[a, TZ],
		[b, "UTC"],
	] as const) {
		const days = new Set<string>();
		for (let t = u.from; t < u.to; t += 3_600_000) days.add(localDay(t, tz));
		days.add(localDay(u.to - 1, tz));
		const keys = u.groups.map((g) => g.key);
		check(
			keys.length === days.size && keys.every((k) => days.has(k)),
			`${tz} 桶恰好覆盖窗口内每个本地日（${keys[0]}…${keys.at(-1)}）`,
		);
		check(
			keys.at(-1) === localDay(Date.now(), tz),
			`${tz} 末桶是今天 ${localDay(Date.now(), tz)}`,
		);
	}
	const bad = await api(
		"GET",
		`/api/v1/stats/usage?groupBy=day&tz=Mars/Phobos`,
	);
	check(
		bad.status === 400 &&
			(bad.json as { code?: string })?.code === "invalid_timezone",
		`tz=Mars/Phobos → ${bad.status} ${(bad.json as { code?: string })?.code}`,
	);
}

async function Y7() {
	await ensureT();
	const pid = await projectId();
	const sp = await api("POST", "/api/v1/spaces", {
		name: `m10-space-${RUN}`,
		projectId: pid,
	});
	if (sp.status !== 201) throw new Error(`建 Space ${sp.status} ${sp.text}`);
	const spaceId = ((sp.json as { space?: { id: string } }).space?.id ??
		(sp.json as { id: string }).id) as string;
	const st = await newThread("只回复 ok", `m10 space ${RUN}`);
	const p = await api("PATCH", `/api/v1/threads/${st}`, { spaceId });
	check(p.status === 200, `线程 ${st} 加入 Space ${spaceId}`);
	const bySpace = await usage({ groupBy: "space", from: 0 });
	const sg = bySpace.groups.find((g) => g.key === spaceId);
	check(
		!!sg && sg.costUsd > 0 && sg.label === `m10-space-${RUN}`,
		`space 分组有该 Space（label=${sg?.label}，cost=${sg?.costUsd}）`,
	);
	const byProject = await usage({ groupBy: "project", from: 0 });
	const pg = byProject.groups.find((g) => g.key === pid);
	const filtered = await usage({ groupBy: "model", project: pid, from: 0 });
	check(
		near(filtered.total.costUsd, pg?.costUsd ?? -1),
		`project 筛选的总额 ${filtered.total.costUsd} 等于项目分组 ${pg?.costUsd}`,
	);
	const ser = await usage({
		groupBy: "model",
		series: "day",
		from: Date.now() - 7 * DAY,
		tz: TZ,
	});
	const badPoint = (ser.series ?? []).find(
		(pt) =>
			!near(
				pt.groups.reduce((s, g) => s + g.costUsd, 0),
				pt.total.costUsd,
			),
	);
	check(
		!badPoint && (ser.series?.length ?? 0) > 0,
		`series 每点 groups 之和等于 total（${ser.series?.length} 点）`,
	);
	// 时间粒度从 0 起会超过 1000 个桶：所有分组都从最早的用量时间（stats/meta.earliestTs）起算
	const earliest =
		(await getJson<{ earliestTs?: number }>("/api/v1/stats/meta")).earliestTs ??
		0;
	const totals = await Promise.all(
		["total", "day", "model", "thread", "project", "space", "purpose"].map(
			async (g) =>
				[
					g,
					(await usage({ groupBy: g, from: earliest, tz: TZ })).total.costUsd,
				] as const,
		),
	);
	note(totals.map(([g, c]) => `${g}=${c}`).join(" "));
	check(
		totals.every(([, c]) => near(c, totals[0]?.[1] ?? Number.NaN)),
		"各 groupBy 的 total.costUsd 相等",
	);
}

async function Y8() {
	const tid = await ensureT();
	await ensureAwake(tid);
	await say(tid, "用 bash 执行 `false`，然后只回复 done");
	const t = await getJson<ToolStats>(
		`/api/v1/stats/tools?${qs({ thread: tid, from: 0 })}`,
	);
	const bash = t.tools.find((x) => x.tool === "bash");
	note(
		`工具：${t.tools.map((x) => `${x.tool}=${x.calls}/${x.errors}`).join(" ")}`,
	);
	check(
		(bash?.calls ?? 0) >= 2 && (bash?.errors ?? 0) >= 1,
		`bash calls=${bash?.calls} errors=${bash?.errors}`,
	);
	const u = await usage({ groupBy: "thread", thread: tid, from: 0 });
	check(
		(u.groups[0]?.toolErrors ?? 0) >= 1,
		`usage toolErrors=${u.groups[0]?.toolErrors}`,
	);
}

async function Y9() {
	const tid = await ensureT();
	const orb = orbOf(tid);
	await waitFor("orb 冻结", () => orbState(orb) === "frozen", 300_000, 2000);
	await sleep(20_000);
	const g = (
		await getJson<OrbStats>(
			`/api/v1/stats/orbs?${qs({ groupBy: "orb", orb, from: T_START - DAY })}`,
		)
	).groups.find((x) => x.key === orb);
	note(
		`orb ${orb}：running=${g?.runningMs} frozen=${g?.frozenMs} setup=${g?.setupMs} spawns=${g?.spawns} freezes=${g?.freezes}`,
	);
	check(
		!!g && g.runningMs > 0 && g.frozenMs >= 15_000 && g.setupMs > 0,
		"运行 / 冻结 / setup 时长",
	);
	check(
		(g?.spawns ?? 0) >= 1 && (g?.freezes ?? 0) >= 1,
		"spawns ≥ 1、freezes ≥ 1",
	);
	const tl = await getJson<{
		segments: { from: number; to: number; state: string }[];
	}>(`/api/v1/orbs/${orb}/timeline?${qs({ from: T_START - DAY })}`);
	const states = tl.segments.map((s) => s.state);
	note(`时间线 ${states.join(" → ")}`);
	const order = ["setting_up", "ready", "freezing", "frozen"];
	let i = 0;
	for (const s of states) if (s === order[i]) i++;
	check(i === order.length, "依次含 setting_up → ready → freezing → frozen");
	check(
		(tl.segments.at(-1)?.to ?? Infinity) <= Date.now(),
		"末段截止不晚于当前",
	);
	const st = await getJson<OrbStats>(
		`/api/v1/stats/orbs?${qs({ groupBy: "orb", orb, from: T_START - DAY })}`,
	);
	check(st.setup.runs >= 1, `setup.runs=${st.setup.runs}`);
	const byDay = await getJson<OrbStats>(
		`/api/v1/stats/orbs?${qs({ groupBy: "day", tz: TZ })}`,
	);
	const today = byDay.groups.find((x) => x.key === localDay(Date.now(), TZ));
	check(
		(today?.runningMs ?? 0) >= 0 && !!today,
		`按日今天桶 runningMs=${today?.runningMs}`,
	);
}

async function Y10() {
	const tid = await ensureT();
	await ensureAwake(tid);
	await say(
		tid,
		// dev 没有配置 git 身份（设置 → git 身份）：在命令里显式给出，避免提交因身份未知失败
		"新建文件 m10.txt 写入 hello，然后执行 `git add m10.txt && git -c user.name=m10 -c user.email=m10@oryn.test commit -m m10` 提交。完成后只回复 ok",
	);
	await waitFor(
		"thread_commits 采到提交",
		() =>
			(one<{ n: number }>(
				`select count(*) n from thread_commits where thread_id='${tid}'`,
			)?.n ?? 0) >= 1,
		60_000,
		1000,
	);
	const life = (await usage({ groupBy: "thread", thread: tid, from: 0 })).total
		.costUsd;
	const ship = await api("PATCH", `/api/v1/threads/${tid}`, { shipped: true });
	check(ship.status === 200, "$T 标记为已交付");
	const f = forks();
	if (f?.f2 && threadRow(f.f2))
		await api("PATCH", `/api/v1/threads/${f.f2}`, { archived: true });
	const out = await getJson<OutputStats>(
		`/api/v1/stats/output?${qs({ groupBy: "day", tz: TZ })}`,
	);
	const today = out.groups.find((g) => g.key === localDay(Date.now(), TZ));
	note(
		`今天：created=${today?.threadsCreated} shipped=${today?.threadsShipped} archived=${today?.threadsArchived} commits=${today?.commits} shippedCost=${today?.shippedCostUsd}`,
	);
	check(
		(today?.threadsShipped ?? 0) >= 1 && (today?.commits ?? 0) >= 1,
		"今天 shipped ≥ 1、commits ≥ 1",
	);
	if (f?.f2) check((today?.threadsArchived ?? 0) >= 1, "今天 archived ≥ 1");
	check(
		(today?.shippedCostUsd ?? 0) >= life - EPS,
		`shippedCostUsd 含 $T 全生命周期成本 ${life}`,
	);
	const byP = await getJson<OutputStats>(
		`/api/v1/stats/output?${qs({ groupBy: "project", tz: TZ })}`,
	);
	const pid = await projectId();
	const pg = byP.groups.find((g) => g.key === pid);
	check(
		(pg?.threadsShipped ?? 0) >= 1 && (pg?.commits ?? 0) >= 1,
		`项目分组 shipped=${pg?.threadsShipped} commits=${pg?.commits}`,
	);
}

async function Y11() {
	const f = forks();
	if (!f?.f1 || !threadRow(f.f1)) throw new Error("没有 $F1（先跑 Y5）");
	const pid = await projectId();
	const p0 =
		(await usage({ groupBy: "project", from: 0 })).groups.find(
			(g) => g.key === pid,
		)?.costUsd ?? 0;
	// $F1 自己要有用量，删除后才有行可核对（Y5 中途失败时它可能还没发过消息）
	if (
		(await usage({ groupBy: "thread", thread: f.f1, from: 0 })).total
			.costUsd === 0
	)
		await say(f.f1, "只回复 ok");
	const f1 = (await usage({ groupBy: "thread", thread: f.f1, from: 0 })).total
		.costUsd;
	const d0 =
		(await usage({ groupBy: "thread", from: 0, limit: 500 })).groups.find(
			(g) => g.key === "",
		)?.costUsd ?? 0;
	const d = await api("DELETE", `/api/v1/threads/${f.f1}`);
	check(d.status === 204, `删除 $F1 → ${d.status}`);
	const p1 =
		(await usage({ groupBy: "project", from: 0 })).groups.find(
			(g) => g.key === pid,
		)?.costUsd ?? 0;
	const d1 =
		(await usage({ groupBy: "thread", from: 0, limit: 500 })).groups.find(
			(g) => g.key === "",
		)?.costUsd ?? 0;
	note(`项目 ${p0} → ${p1}；已删线程组 ${d0} → ${d1}；$F1 原成本 ${f1}`);
	check(near(p0, p1), "项目成本不变");
	check(near(d1 - d0, f1), "已删线程组（key 为空）增加了 $F1 的成本");
	const rows = sql<{ n: number; p: number }>(
		`select count(*) n, sum(project_id='${pid}') p from usage_records where thread_id is null and ukey in
		 (select ukey from usage_records where thread_id is null and project_id='${pid}')`,
	)[0];
	check(
		(rows?.n ?? 0) > 0 && rows?.n === rows?.p,
		`已删行 thread_id 为空且 project_id=$P（${rows?.n} 行）`,
	);
}

async function Y12() {
	const r = await api("GET", "/api/v1/stats/usage?groupBy=model&format=csv");
	const ct = r.headers.get("content-type") ?? "";
	const cd = r.headers.get("content-disposition") ?? "";
	note(`${ct}｜${cd}`);
	check(
		ct.startsWith("text/csv") && ct.includes("charset=utf-8"),
		"Content-Type text/csv; charset=utf-8",
	);
	check(
		/attachment; filename="oryn-usage-model-\d{8}-\d{8}\.csv"/.test(cd),
		"附件名 oryn-usage-model-…csv",
	);
	check(
		r.text.split("\n")[0] ===
			"bucket,key,label,requests,request_errors,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cost_usd,subagent_cost_usd,avg_ttft_ms,tokens_per_second,tool_calls,tool_errors,turns",
		"首行列名",
	);
	const from = Date.now() - 7 * DAY;
	const long = await api(
		"GET",
		`/api/v1/stats/usage?${qs({ groupBy: "model", series: "day", from, tz: TZ, format: "csv" })}`,
	);
	const js = await usage({ groupBy: "model", series: "day", from, tz: TZ });
	const nonzero = (js.series ?? []).reduce(
		(s, p) =>
			s +
			p.groups.filter(
				(g) =>
					g.requests > 0 || g.costUsd > 0 || g.toolCalls > 0 || g.turns > 0,
			).length,
		0,
	);
	const lines = long.text.trim().split("\n").length - 1;
	check(lines === nonzero, `长表行数 ${lines} = 非零桶×组 ${nonzero}`);
	const dayFrom = Date.now() - DAY;
	const rec = await api(
		"GET",
		`/api/v1/stats/usage/records?${qs({ from: dayFrom })}`,
	);
	const recLines = rec.text.trim().split("\n");
	const to = Date.now();
	const n =
		one<{ n: number }>(
			`select count(*) n from usage_records where ts>=${dayFrom} and ts<${to}`,
		)?.n ?? -1;
	check(recLines[0]?.startsWith("ts,time,thread_id,") ?? false, "明细首行列名");
	check(
		recLines.length - 1 === n,
		`明细行数 ${recLines.length - 1} = 库里 ${n}`,
	);

	// 下载导航不带 extraHTTPHeaders（会拿到 401 的 JSON）；这里经 route 补令牌
	const cctx = await downloadContext();
	const page = await cctx.newPage();
	await page.goto(`${BASE}/stats/usage?by=model`);
	await page.locator("[data-testid=stats-export]").click();
	const [dl] = await Promise.all([
		page.waitForEvent("download", { timeout: 15_000 }),
		page.locator("[data-export=view-csv]").click(),
	]);
	const name = dl.suggestedFilename();
	check(/^oryn-usage-model-\d{8}-\d{8}\.csv$/.test(name), `页面下载 ${name}`);
	await cctx.close();
}

async function Y13() {
	await ensureT();
	const pid = await projectId();
	const page = await newPage();
	await page.goto(`${BASE}/stats`);
	await page.waitForURL(/\/stats\/usage/, { timeout: 15_000 });
	await page.locator('[data-range="7d"]').first().click();
	await page.locator('[data-by="project"]').first().click();
	await page.locator(`[data-row-key="${pid}"]`).first().click();
	await waitFor(
		"URL 带筛选",
		() =>
			/range=7d/.test(page.url()) &&
			/by=project/.test(page.url()) &&
			page.url().includes(`project=${pid}`),
		10_000,
		200,
	);
	note(`URL ${new URL(page.url()).search}`);
	await page.reload();
	const kpi = page.locator("[data-testid=kpi-cost]");
	await kpi.waitFor({ timeout: 30_000 });
	const sp = new URL(page.url()).searchParams;
	check(
		sp.get("range") === "7d" &&
			sp.get("by") === "project" &&
			sp.get("project") === pid,
		"刷新后 URL 状态保留",
	);
	const now = new Date();
	const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
	const midnight = new Date(
		now.getFullYear(),
		now.getMonth(),
		now.getDate(),
	).getTime();
	const rest = await usage({
		groupBy: "project",
		series: "day",
		project: pid,
		from: midnight - 6 * DAY,
		to: midnight + DAY,
		tz,
	});
	await waitFor(
		"成本指标出数",
		async () => ((await kpi.textContent()) ?? "").includes("$"),
		30_000,
	);
	const kt = (await kpi.textContent()) ?? "";
	check(
		kt.includes(formatCost(rest.total.costUsd)),
		`成本指标「${kt.trim().slice(0, 40)}」= REST ${formatCost(rest.total.costUsd)}`,
	);
	const bars = await page
		.locator("[data-testid=stats-chart] g[data-bucket]")
		.count();
	check(bars === 7, `柱子桶数 ${bars} = 7`);
	await shot(page, "Y13-usage");

	await page.locator('a[href^="/stats/orbs"]').first().click();
	const orb = orbOf(T);
	const row = page.locator(`[data-orb-row="${orb}"]`).first();
	await row.waitFor({ timeout: 30_000 });
	await row.click();
	const drawer = page.locator("[data-testid=orb-drawer]");
	await drawer.waitFor({ timeout: 15_000 });
	await drawer
		.locator("[data-testid=timeline-strip]")
		.waitFor({ timeout: 15_000 });
	const charts = await drawer.locator("[data-testid=line-chart]").count();
	check(charts === 2, `抽屉里状态色带 + ${charts} 张曲线`);
	await shot(page, "Y13-orb-drawer");
	await page.keyboard.press("Escape");
	await page.locator('a[href^="/stats/output"]').first().click();
	await page.locator("[data-testid=kpi-shipped]").waitFor({ timeout: 30_000 });
	await shot(page, "Y13-output");
	check(
		pageErrors.length === 0,
		`没有控制台错误（${pageErrors.slice(0, 3).join(" | ")}）`,
	);
	await page.close();
}

async function Y14() {
	await ensureT();
	for (const vp of [
		{ width: 390, height: 844 },
		{ width: 360, height: 780 },
	]) {
		const page = await newPage(vp);
		for (const tab of ["usage", "orbs", "output"]) {
			await page.goto(`${BASE}/stats/${tab}`);
			await page
				.locator("[data-testid^=kpi-]")
				.first()
				.waitFor({ timeout: 30_000 });
			await sleep(500);
			const over = await page.evaluate(() => {
				const s = document.scrollingElement;
				return s ? s.scrollWidth - window.innerWidth : 0;
			});
			check(over <= 0, `${vp.width} ${tab} 无横向滚动（溢出 ${over}px）`);
			const cols = await page.evaluate(() => {
				const k = document.querySelector("[data-testid^=kpi-]");
				const grid = k?.parentElement;
				return grid
					? getComputedStyle(grid).gridTemplateColumns.split(" ").length
					: 0;
			});
			check(cols === 2, `${vp.width} ${tab} 指标行 ${cols} 列`);
			await shot(page, `Y14-${vp.width}-${tab}`);
		}
		await page.goto(`${BASE}/stats/usage?range=7d`);
		const bar = page.locator("[data-testid=stats-chart] g[data-bucket]").last();
		await bar.waitFor({ timeout: 30_000 });
		await bar.click();
		const tip = page.locator("[data-testid=chart-tooltip]");
		await tip.waitFor({ timeout: 5000 });
		check(await tip.isVisible(), "点柱后出现浮层");
		await page.mouse.click(5, vp.height - 5);
		await waitFor("浮层消失", async () => !(await tip.isVisible()), 5000, 100);
		note("点空白处浮层消失");
		await page.goto(`${BASE}/stats/orbs`);
		const row = page.locator("[data-orb-row]").first();
		await row.waitFor({ timeout: 30_000 });
		await row.click();
		const drawer = page.locator("[data-testid=orb-drawer]");
		await drawer.waitFor({ timeout: 15_000 });
		const box = await drawer.boundingBox();
		check(
			!!box && box.width >= vp.width - 1 && box.height >= vp.height - 1,
			`抽屉全屏 ${box?.width}×${box?.height}`,
		);
		await shot(page, `Y14-${vp.width}-drawer`);
		await page.close();
	}
}

const strip = (j: Record<string, unknown>) => {
	const { from: _f, to: _t, ...rest } = j;
	return JSON.stringify(rest);
};

async function Y15() {
	const tid = await ensureT();
	const a = await cli("stats --by model --since 7d --json");
	check(a.code === 0, "oryn stats --json 退出 0");
	const cj = JSON.parse(a.stdout) as UsageStats;
	const rj = await usage({
		groupBy: "model",
		from: cj.from,
		to: cj.to,
		tz: cj.tz,
	});
	check(
		strip(cj as unknown as Record<string, unknown>) ===
			strip(rj as unknown as Record<string, unknown>),
		"CLI JSON 与同参数 REST 逐字段相等",
	);
	const t = await cli(`stats thread ${tid}`);
	note(t.out.split("\n").slice(0, 12).join(" ⏎ "));
	const cost = (await usage({ groupBy: "thread", thread: tid, from: 0 })).total
		.costUsd;
	// CLI 成本按 4 位小数显示：取 TOTAL 行里的金额按数值比
	const shown = Number(
		/^TOTAL\b.*?\$([0-9.]+)/m.exec(t.out)?.[1] ?? Number.NaN,
	);
	check(
		t.code === 0 && Math.abs(shown - cost) < 5e-5,
		`thread 总计成本 $${shown} ≈ ${cost}`,
	);
	check(
		/RUNNING|运行/i.test(t.out) && /FROZEN|冻结/i.test(t.out),
		"列出 orb 的运行与冻结时长",
	);
	const o = await cli("stats orbs --by orb");
	check(
		o.code === 0 && o.out.includes(orbOf(tid)),
		"stats orbs --by orb 列出 $O",
	);
	const c = await cli(`stats --by day --csv --tz ${TZ}`);
	check(
		c.code === 0 &&
			(c.stdout.split("\n")[0] ?? "").startsWith("bucket,key,label,requests"),
		"CSV 首行为列名",
	);
}

async function Y16() {
	if (process.env.M10_NO_RESTART === "1") {
		note("M10_NO_RESTART=1，跳过");
		return;
	}
	const tid = await ensureT();
	const orb = orbOf(tid);
	const old = Date.now() - 40 * DAY;
	sql(
		`insert into orb_samples (orb_id, ts, cpu_pct, mem_used_mb, mem_limit_mb, disk_used_mb, net_rx_bytes, net_tx_bytes) values ('${orb}', ${old}, 1, 1, 1, 1, 0, 0)`,
	);
	const ins = one<{ n: number }>(
		`select count(*) n from orb_samples where orb_id='${orb}' and ts=${old}`,
	)?.n;
	check(ins === 1, "插入 40 天前的采样");
	const before = (await usage({ groupBy: "total", from: 0 })).total;
	sql(`update stats_state set value='1' where key='facts_version'`);
	const r = sh(`${ROOT}/.dev/e2e/restart.sh --no-build`);
	check(r.code === 0, `重启 orynd：${r.out.split("\n").at(-1)}`);
	await waitFor(
		"旧采样被清理",
		() =>
			(one<{ n: number }>(
				`select count(*) n from orb_samples where orb_id='${orb}' and ts=${old}`,
			)?.n ?? 1) === 0,
		60_000,
		1000,
	);
	note("60 秒内旧采样已删");
	const meta = await waitFor(
		"回填完成",
		async () => {
			const m = await getJson<{ factsVersion: number; backfill?: unknown }>(
				"/api/v1/stats/meta",
			);
			return !m.backfill && m.factsVersion === 2 ? m : undefined;
		},
		600_000,
		2000,
	);
	note(`meta ${JSON.stringify(meta)}`);
	const after = (await usage({ groupBy: "total", from: 0 })).total;
	note(
		`回填前 cost=${before.costUsd} req=${before.requests} turns=${before.turns}；回填后 ${after.costUsd} / ${after.requests} / ${after.turns}`,
	);
	check(
		near(before.costUsd, after.costUsd) &&
			before.requests === after.requests &&
			before.turns === after.turns,
		"回填前后总额一致",
	);
}

async function Y17() {
	// 助手
	const c = await api("POST", "/api/v1/assistant/conversations");
	if (c.status !== 201 && c.status !== 200)
		throw new Error(`助手对话 ${c.status} ${c.text}`);
	const conv = threadIdOf(c.json);
	await say(
		conv,
		"最近 7 天成本最高的线程是哪个，它用了什么模型？请调用 get_stats 查询后回答。",
		600_000,
	);
	const es = await entriesOf(conv);
	const called = es.some((e) =>
		JSON.stringify(e.payload).includes("get_stats"),
	);
	check(called, "助手调用了 get_stats");
	// 空键是「已删除的线程」、__other__ 是折叠项：取第一个真实线程
	const top = (
		await usage({ groupBy: "thread", from: Date.now() - 7 * DAY, limit: 10 })
	).groups.find((g) => g.key !== "" && g.key !== "__other__");
	const reply =
		es
			.filter(
				(e) =>
					e.type === "message" &&
					(e.payload.message as { role?: string })?.role === "assistant",
			)
			.map((e) => JSON.stringify(e.payload))
			.at(-1) ?? "";
	check(
		!!top?.label && reply.includes(top.label),
		`回复里有成本最高线程的标题「${top?.label}」`,
	);

	// 预算
	await ensureHook();
	sql("DELETE FROM budget_alerts");
	let st = await getJson<BudgetStatus>("/api/v1/stats/budget");
	for (let i = 0; i < 5 && st.day.spentUsd < 0.01; i++) {
		await say(await ensureT(), "只回复 ok");
		st = await getJson<BudgetStatus>("/api/v1/stats/budget");
	}
	note(`今日已花 $${st.day.spentUsd}`);
	check(st.day.spentUsd >= 0.01, "当日花费 ≥ $0.01");
	const unread0 = await getJson<{
		notifications: {
			id: string;
			kind: string;
			threadId?: string;
			url: string;
			readAt?: number;
		}[];
		unread: number;
	}>("/api/v1/notifications?unread=true");
	const hooks0 = hooks.length;
	const s = await cli(`stats budget set --daily 0.01 --warn 80 --tz ${TZ}`);
	check(s.code === 0, "budget set 退出 0");
	const hook = await waitFor(
		"Webhook 收到 budget.alert",
		() =>
			hooks
				.slice(hooks0)
				.find((h) => h.headers["x-oryn-event"] === "budget.alert"),
		90_000,
		500,
	);
	check(hook.sigOk, "签名正确");
	check(
		hook.json.event === "budget.alert" &&
			hook.json.title === "Daily budget exceeded",
		`event=${hook.json.event} title=${hook.json.title}`,
	);
	check(!("thread" in hook.json), "体里没有 thread 键");
	check((hook.json.url ?? "").endsWith("/stats/usage"), `url=${hook.json.url}`);
	const list = await getJson<typeof unread0>(
		"/api/v1/notifications?unread=true",
	);
	const bn = list.notifications.find((n) => n.kind === "budget");
	check(
		!!bn && !bn.threadId && bn.url === "/stats/usage",
		`未读里有预算通知 ${bn?.id}`,
	);
	const others = list.notifications
		.filter((n) => n.id !== bn?.id)
		.map((n) => n.id);

	const page = await newPage();
	await page.goto(`${BASE}/`);
	const reads: string[] = [];
	page.on("request", (r) => {
		if (r.url().endsWith("/api/v1/notifications/read") && r.method() === "POST")
			reads.push(r.postData() ?? "");
	});
	await page.getByRole("button", { name: /^通知/ }).first().click();
	await page.getByText("Daily budget exceeded").first().click();
	await page.waitForURL(/\/stats\/usage/, { timeout: 15_000 });
	await waitFor("发出已读请求", () => reads.length > 0, 10_000, 100);
	note(`已读请求体 ${reads.join(" | ")}`);
	check(
		reads.every((b) => {
			const j = JSON.parse(b || "{}");
			return j.id === bn?.id && !("threadId" in j);
		}),
		"已读请求体只有 id",
	);
	const after = await getJson<typeof unread0>(
		"/api/v1/notifications?unread=true",
	);
	check(!after.notifications.some((n) => n.id === bn?.id), "预算通知已读");
	check(
		others.every((id) => after.notifications.some((n) => n.id === id)),
		`其它 ${others.length} 条未读通知仍未读`,
	);
	const card = page.locator(
		"[data-testid=budget-card] [data-budget-period=day]",
	);
	await card.waitFor({ timeout: 30_000 });
	check(
		(await card.getAttribute("data-level")) === "over",
		"预算卡当日条为 over",
	);
	await shot(page, "Y17-budget");
	await page.close();

	const n1 = hooks.length;
	await sleep(90_000);
	check(
		!hooks.slice(n1).some((h) => h.headers["x-oryn-event"] === "budget.alert"),
		"第二个 90 秒没有新的 budget.alert",
	);
	const cnt = one<{ n: number }>(
		"select count(*) n from notifications where kind='budget'",
	)?.n;
	const rows = sql<{ level: string }>(
		"select level from budget_alerts where period='day' order by level",
	);
	note(`budget 通知 ${cnt} 条；账本 ${rows.map((r) => r.level).join(",")}`);
	check(
		rows.map((r) => r.level).join(",") === "over,warn",
		"账本恰为 warn 与 over 两行",
	);
	const off = await cli("stats budget off");
	check(off.code === 0, "budget off");
}

const TESTS: [string, () => Promise<void>][] = [
	["Y2", Y2],
	["Y3", Y3],
	["Y4", Y4],
	["Y5", Y5],
	["Y6", Y6],
	["Y7", Y7],
	["Y8", Y8],
	["Y9", Y9],
	["Y10", Y10],
	["Y11", Y11],
	["Y12", Y12],
	["Y13", Y13],
	["Y14", Y14],
	["Y15", Y15],
	["Y16", Y16],
	["Y17", Y17],
];

// ---------------------------------------------------------------- 主流程

const browser: Browser = await chromium.launch({
	executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
	headless: true,
	args: ["--no-sandbox"],
});
ctx = await browser.newContext({
	locale: "zh-CN",
	timezoneId: TZ,
	acceptDownloads: true,
	extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
});
await ctx.addInitScript(() => localStorage.setItem("oryn.debug", "1"));
console.log(
	`M10 验收 run=${RUN} base=${BASE} shots=${SHOTS} webhook=${HOOK_URL}`,
);

for (const [id, fn] of TESTS) {
	if (ONLY && !ONLY.has(id)) continue;
	evidence = [];
	pageErrors.length = 0;
	console.log(`── ${id}`);
	const t0 = Date.now();
	try {
		await fn();
		console.log(`${id} PASS (${((Date.now() - t0) / 1000).toFixed(1)}s)`);
		results.push({ id, verdict: "PASS", evidence });
	} catch (e) {
		note(`错误：${e instanceof Error ? e.message : String(e)}`);
		let i = 0;
		for (const p of ctx.pages())
			if (!p.isClosed()) await shot(p, `${id}-FAIL-${RUN}-${i++}`);
		for (const p of ctx.pages()) await p.close().catch(() => {});
		console.log(`${id} FAIL (${((Date.now() - t0) / 1000).toFixed(1)}s)`);
		results.push({ id, verdict: "FAIL", evidence });
	}
}

if (hookChannel)
	await api("PATCH", `/api/v1/notification-channels/${hookChannel}`, {
		enabled: false,
	}).catch(() => undefined);
await browser.close();
hookSrv.stop(true);
await Bun.write(
	`${SHOTS}/results-${RUN}.json`,
	JSON.stringify(results, null, 2),
);
console.log("\n汇总");
for (const r of results) console.log(`${r.id} ${r.verdict}`);
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
