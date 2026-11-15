#!/usr/bin/env bun
import {
	existsSync,
	mkdirSync,
	readdirSync,
	readFileSync,
	writeFileSync,
} from "node:fs";
import { homedir } from "node:os";
/**
 * M12 真机验收（计划「验证（真机验收清单）」Y1–Y12）：对 dev 部署（钉 Core oryn-v18.19.0-5 的镜像与二进制）逐项执行，
 * 每项打印 `Y<n> PASS|FAIL|SKIP (秒)` 与证据行，失败时给该场景最近操作的线程页截图并留一份 orynd.log（与 runner 日志）末尾。
 *
 * 用法（web/ 目录，以 root 运行）：bun run accept:m12 [-- --only Y1,Y3] [-- --help]
 * 环境变量：ORYN_URL（缺省 http://localhost:7600）、ORYN_ROOT、ORYN_TOKEN、ORYN_CREDENTIALS（缺省 .dev/credentials.json）；
 *   M12_RUNNER_URL（runner 的 ORYN_URL，缺省 http://127.0.0.1:7600）；M12_USER（跑 runner 的普通用户，缺省 oryn-m11b）；
 *   M12_PROJECT（普通 orb 线程的项目，缺省 sandbox）；M12_ORB_PROVIDER（普通 orb 的 provider，缺省 local）；M12_REAL_MODEL（Y2 用的真实网关模型，缺省 anthropic/claude-opus-5-5；
 *   置空串 = 不指定、用 runner 用户 omp 的默认模型）；M12_SHOTS（缺省 /tmp/oryn-shots/m12-accept）；CHROMIUM。
 * CLI 一律 `./bin/oryn`，`ORYN_CREDENTIALS=$ROOT/.dev/credentials.json`。
 *
 * 固定布置：
 *   - runner 照 m11b：用户 `oryn-m11b`（不存在就 `useradd -m`）；`bin/orbd` 与 `build/core/x64/omp` 拷到 /var/tmp/m12/{orbd,bin/omp}；
 *     runner 以 `runuser -u oryn-m11b -- env -i … orbd run --runner --dir /tmp/m12-a --dir /tmp/m12-b [--terminal]` 起在后台，
 *     输出追加到 /var/tmp/m12/runner-<RUN>.log；root 的 ~/.omp/agent/models.yml 拷给该用户（0600）。
 *   - Y1/Y3/Y4 的假 Anthropic 端点：脚本内 Bun.serve 监听 127.0.0.1 的随机端口，记录每次请求；请求体含 `"tool_addition"`
 *     回 400（Anthropic 错误 JSON），否则回 `ok` 的 SSE 流。runner 用户的 models.yml 临时加独立 provider `m12fake`
 *     （api anthropic-messages、模型 claude-opus-5-5）。Core 的 compat 规则按 provider 名开启工具变更
 *     （oryn-core packages/catalog/src/compat/rules/classes/anthropic.kdl 的 `on "anthropic" "cloudflare-ai-gateway" "google-vertex"`），
 *     自定 provider 名不会命中，所以在该 provider 的 `compat` 里显式写 `supportsMidConversationToolChanges: true`。
 *     线程经 `POST threads` 的 `model: {provider, id}` 选模型（CLI `thread new` 没有 --model）。结尾还原 models.yml。
 *   - 执行顺序 Y1、Y3、Y4、Y2、Y5…Y12：Y3 依赖 Y1 那个 omp 进程内的闩锁，Y2 要重启 runner，放在 Y4 之后才不打断它。
 *   - 普通 orb 线程：项目 M12_PROJECT 下新建一个 orb（第一个线程「只回复 ok」），其余场景用 `thread new --orb` 挂到同一个 orb；
 *     Y8 冻结 orb 后，后续场景先 `POST orbs/{id}/resume`。
 *   - 场景之间的前置惰性准备，`--only` 单跑时缺什么现建什么（Y3/Y4 缺 Y1 就先跑 Y1，Y8 缺 Y7 就先跑 Y7）。
 *   - 结尾（含异常与 Ctrl-C）：停假端点、还原 models.yml、杀 runner 进程（及该用户的全部进程）、删除本次建的 runner、
 *     删除本次建的 orb；假端点的请求摘要写到 $M12_SHOTS/fake-requests-<RUN>.json。
 *
 * 与清单的出入：
 *   - Y2 缺省显式选 anthropic/claude-opus-5-5：runner 用户没有 settings，omp 的「默认模型」不确定，若落到非 Anthropic 模型
 *     就根本不会发工具变更，判 native 没有意义。
 *   - Y1/Y3/Y4 只统计带非空 `tools` 的 `/v1/messages` 请求（agent 回合）；标题生成等无工具的辅助请求只记录不计数。
 *   - Y5 若 set_goal 之后 30 秒内没有自动续跑（goal 续跑受 Core `goal.continuationModes` 是否含 rpc 控制），补发一条消息推进。
 *   - Y6 若 agent 经 xd:// 派发的不是 ast_edit（计划 A7：orb 里该设备被设置关掉），按实际派发的工具名断言并注明。
 *   - Y11「卡片内除刷新外没有开关或按钮」不计卡片标题本身的折叠按钮（Card 的 header 按钮）。
 */
import type { FileSink, Server, Subprocess } from "bun";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Locator,
	type Page,
} from "playwright-core";

const IDS = [
	"Y1",
	"Y2",
	"Y3",
	"Y4",
	"Y5",
	"Y6",
	"Y7",
	"Y7b",
	"Y8",
	"Y9",
	"Y10",
	"Y11",
	"Y12",
] as const;
const USAGE = `用法：bun scripts/m12-accept.ts [--only Y1,Y3,…] [--help]
  场景：${IDS.join(" ")}（执行顺序 Y1 Y3 Y4 Y2 Y5…Y12）
  以 root 运行；环境变量见文件头注释（ORYN_URL、ORYN_CREDENTIALS、M12_USER、M12_PROJECT、M12_REAL_MODEL、M12_SHOTS、CHROMIUM 等）。`;

if (process.argv.includes("--help") || process.argv.includes("-h")) {
	console.log(USAGE);
	process.exit(0);
}
const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0
		? new Set(
				(process.argv[onlyArg + 1] ?? "")
					.split(",")
					.map((s) => s.trim())
					.filter(Boolean),
			)
		: null;
const unknownIds = [...(ONLY ?? [])].filter(
	(x) => !(IDS as readonly string[]).includes(x),
);
if (ONLY && (ONLY.size === 0 || unknownIds.length > 0)) {
	console.error(
		`--only 只接受 ${IDS.join(",")}${unknownIds.length ? `（未知：${unknownIds.join(",")}）` : ""}\n${USAGE}`,
	);
	process.exit(2);
}

const ROOT = process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`;
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const CREDS = process.env.ORYN_CREDENTIALS ?? `${ROOT}/.dev/credentials.json`;
const TOKEN =
	process.env.ORYN_TOKEN ??
	(JSON.parse(readFileSync(CREDS, "utf8")) as { token: string }).token;
const SHOTS = process.env.M12_SHOTS ?? "/tmp/oryn-shots/m12-accept";
const DB = `${ROOT}/.dev/data/oryn.db`;
const ORYN = `${ROOT}/bin/oryn`;
const ORYND_LOG = `${ROOT}/.dev/orynd.log`;
const WEB_STATE = `${ROOT}/.dev/e2e/state.json`;
const RUNNER_URL = process.env.M12_RUNNER_URL ?? "http://127.0.0.1:7600";
const RUSER = process.env.M12_USER ?? "oryn-m11b";
const PROJECT = process.env.M12_PROJECT ?? "sandbox";
const ORB_PROVIDER = process.env.M12_ORB_PROVIDER ?? "local";
const REAL_MODEL = process.env.M12_REAL_MODEL ?? "anthropic/claude-opus-5-5";
const STAGE = "/var/tmp/m12";
const RBIN = `${STAGE}/bin`;
const RORBD = `${STAGE}/orbd`;
const OMP_SRC = `${ROOT}/build/core/x64/omp`;
const DIR_A = "/tmp/m12-a";
const DIR_B = "/tmp/m12-b";
const FAKE_PROVIDER = "m12fake";
const FAKE_MODEL = "claude-opus-5-5";
const TOOL_CHANGE_REFUSAL =
	"messages.1.content.0: tool_addition/tool_removal references unknown tool 'terminal'";
mkdirSync(SHOTS, { recursive: true });

const RUN = Math.random().toString(36).slice(2, 6);
const RUNNER_LOG = `${STAGE}/runner-${RUN}.log`;

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const sec = (ms: number) => `${(ms / 1000).toFixed(1)}s`;
const isRecord = (v: unknown): v is Record<string, unknown> =>
	typeof v === "object" && v !== null && !Array.isArray(v);

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
	stderr: string;
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
	return {
		code: code as number,
		out: `${stdout}${stderr}`.trim(),
		stdout,
		stderr,
	};
}

/** `oryn thread rpc <id> <frame>`：返回退出码与解析后的应答（stdout 是缩进 JSON）。 */
async function rpc(tid: string, frame: Record<string, unknown>) {
	const r = await cli(`thread rpc ${tid} ${q(JSON.stringify(frame))}`);
	let resp: Record<string, unknown> | null = null;
	try {
		const v = JSON.parse(r.stdout) as unknown;
		if (isRecord(v)) resp = v;
	} catch {
		// 非 JSON（如托管拒绝只有 stderr）
	}
	return { ...r, resp };
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

/** 新的浏览器上下文（登录方式照 m11b：Bearer 头 + 网页会话 Cookie）。 */
async function makeContext(): Promise<BrowserContext> {
	browser ??= await chromium.launch({
		executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
		headless: true,
		args: ["--no-sandbox"],
	});
	const c = await browser.newContext({
		locale: "zh-CN",
		viewport: { width: 1400, height: 900 },
		extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
	});
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
		localStorage.setItem("oryn.panel.width", "760");
	});
	return c;
}

async function newPage(): Promise<Page> {
	bctx ??= await makeContext();
	return bctx.newPage();
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

interface RunnerView {
	id: string;
	name: string;
	online: boolean;
	orbId: string;
	features: string[];
}
interface ThreadView {
	id: string;
	state: string;
	orbId?: string;
}

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
		.map((x) => (isRecord(x) && x.type === "text" ? String(x.text ?? "") : ""))
		.join("");
}

interface Msg {
	role?: string;
	content?: unknown;
	stopReason?: string;
	toolName?: string;
	details?: unknown;
}
const msgOf = (e: Entry): Msg | undefined =>
	e.type === "message" && isRecord(e.payload.message)
		? (e.payload.message as Msg)
		: undefined;

/** 最后一条 assistant 消息（不论有无文本）。 */
async function lastAssistantMsg(
	tid: string,
): Promise<{ text: string; stopReason: string; seq: number }> {
	const es = await entriesOf(tid);
	for (let i = es.length - 1; i >= 0; i--) {
		const e = es[i];
		const m = e ? msgOf(e) : undefined;
		if (!e || m?.role !== "assistant") continue;
		return {
			text: textOf(m.content).trim(),
			stopReason: m.stopReason ?? "",
			seq: e.seq,
		};
	}
	return { text: "", stopReason: "", seq: 0 };
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
const lastSeq = (tid: string) => threadRow(tid)?.last_seq ?? 0;

const SETTLED: Record<string, true> = {
	idle: true,
	awaiting_input: true,
	error: true,
};

/** beforeSeq 之后新增的 assistant 消息条数。 */
const assistantsAfter = (tid: string, beforeSeq: number) =>
	one<{ n: number }>(
		`select count(*) n from entries where thread_id='${tid}' and seq>${beforeSeq} and type='message' and instr(cast(payload as text), '"role":"assistant"')>0`,
	)?.n ?? 0;

/** 等 beforeSeq 之后出现助手消息且线程静止，返回状态。 */
async function waitTurn(tid: string, beforeSeq: number, timeoutMs = 900_000) {
	return waitFor(
		`线程 ${tid} 回合结束`,
		() => {
			const r = threadRow(tid);
			if (!r || !SETTLED[r.state] || r.last_seq <= beforeSeq) return undefined;
			return assistantsAfter(tid, beforeSeq) ? r.state : undefined;
		},
		timeoutMs,
		1000,
	);
}

async function waitIdle(tid: string, beforeSeq = 0, timeoutMs = 900_000) {
	const st = await waitTurn(tid, beforeSeq, timeoutMs);
	check(st === "idle", `${tid} 到 idle（${st}）`);
}

/** 发一条消息并等到回合结束（idle），返回发出前的 last_seq。 */
async function say(tid: string, msg: string, timeoutMs = 900_000) {
	const before = lastSeq(tid);
	const t0 = Date.now();
	const r = await cli(`thread send ${tid} ${q(msg)}`);
	if (r.code !== 0) throw new Error(`thread send 失败：${r.out}`);
	const st = await waitTurn(tid, before, timeoutMs);
	check(
		st === "idle",
		`${tid} 回合结束于 idle（${st}，${sec(Date.now() - t0)}）`,
	);
	return before;
}

async function waitOrbDeleted(orb: string, timeoutMs = 180_000) {
	await waitFor(
		`${orb} deleted`,
		() => orbDbState(orb) === "deleted",
		timeoutMs,
		1000,
	);
}

// ---------------------------------------------------------------- 浏览器

async function openTab(page: Page, tab: string) {
	const btn = page.locator(`[data-panel-tab=${tab}]`);
	await btn.waitFor({ timeout: 60_000 });
	if ((await btn.getAttribute("aria-selected")) !== "true") await btn.click();
}

/** 信息面板里标题恰为 title 的卡片（`components/ui/data.tsx` 的 Card：section > header > h3 [> button]）。 */
const cardSection = (page: Page, title: string) =>
	page.locator("section").filter({
		has: page.locator("header h3", { hasText: new RegExp(`^${title}$`) }),
	});

/** 可折叠卡片若收起则点开。 */
async function expandCard(section: Locator) {
	const btn = section.locator("header h3 button");
	if ((await btn.getAttribute("aria-expanded")) !== "true") await btn.click();
}

/** 转录里名字恰为 name 的工具卡（`tool-render/ToolView.tsx` 的 .tv-card / .tv-name）。 */
const toolCard = (page: Page, name: string) =>
	page
		.locator(".tv-card")
		.filter({
			has: page.locator(".tv-name", { hasText: new RegExp(`^${name}$`) }),
		})
		.last();

// ---------------------------------------------------------------- 假 Anthropic 端点

interface FakeReq {
	n: number;
	at: number;
	method: string;
	path: string;
	status: number;
	model: string;
	stream: boolean;
	tools: string[];
	toolAddition: boolean;
	toolRemoval: boolean;
	/** tool_addition / tool_removal 块引用的工具名。 */
	changeRefs: string[];
}

const fakeReqs: FakeReq[] = [];
let fakeServer: Server<undefined> | undefined;

/** 请求体里的工具变更块（照 oryn-core packages/ai/test/oryn-tool-change-fallback.test.ts 的 toolChangeBlocks）。 */
function toolChanges(body: Record<string, unknown>) {
	const types: string[] = [];
	const refs: string[] = [];
	const msgs = Array.isArray(body.messages) ? body.messages : [];
	for (const m of msgs) {
		if (!isRecord(m) || !Array.isArray(m.content)) continue;
		for (const b of m.content) {
			if (!isRecord(b)) continue;
			if (b.type !== "tool_addition" && b.type !== "tool_removal") continue;
			types.push(b.type);
			const tool = isRecord(b.tool) ? b.tool : undefined;
			if (typeof tool?.name === "string") refs.push(tool.name);
		}
	}
	return { types, refs };
}

function sse(model: string): Response {
	const usage = {
		input_tokens: 10,
		output_tokens: 0,
		cache_creation_input_tokens: 0,
		cache_read_input_tokens: 0,
	};
	const events: Record<string, unknown>[] = [
		{
			type: "message_start",
			message: {
				id: `msg_m12_${fakeReqs.length}`,
				type: "message",
				role: "assistant",
				model,
				content: [],
				stop_reason: null,
				stop_sequence: null,
				usage,
			},
		},
		{
			type: "content_block_start",
			index: 0,
			content_block: { type: "text", text: "" },
		},
		{
			type: "content_block_delta",
			index: 0,
			delta: { type: "text_delta", text: "ok" },
		},
		{ type: "content_block_stop", index: 0 },
		{
			type: "message_delta",
			delta: { stop_reason: "end_turn", stop_sequence: null },
			usage: { ...usage, output_tokens: 1 },
		},
		{ type: "message_stop" },
	];
	const body = `${events.map((e) => `event: ${String(e.type)}\ndata: ${JSON.stringify(e)}`).join("\n\n")}\n\n`;
	return new Response(body, {
		status: 200,
		headers: {
			"Content-Type": "text/event-stream",
			"request-id": `req_m12_${fakeReqs.length}`,
		},
	});
}

const errorJson = (status: number, type: string, message: string) =>
	new Response(JSON.stringify({ type: "error", error: { type, message } }), {
		status,
		headers: {
			"Content-Type": "application/json",
			"request-id": `req_m12_${fakeReqs.length}`,
		},
	});

async function fakeHandle(req: Request): Promise<Response> {
	const url = new URL(req.url);
	const raw = req.method === "POST" ? await req.text() : "";
	let body: Record<string, unknown> = {};
	try {
		const v = JSON.parse(raw || "{}") as unknown;
		if (isRecord(v)) body = v;
	} catch {
		// 非 JSON 体
	}
	const changes = toolChanges(body);
	const rec: FakeReq = {
		n: fakeReqs.length + 1,
		at: Date.now(),
		method: req.method,
		path: url.pathname,
		status: 0,
		model: typeof body.model === "string" ? body.model : "",
		stream: body.stream === true,
		tools: (Array.isArray(body.tools) ? body.tools : [])
			.map((t) => (isRecord(t) && typeof t.name === "string" ? t.name : ""))
			.filter(Boolean),
		toolAddition: raw.includes('"tool_addition"'),
		toolRemoval: changes.types.includes("tool_removal"),
		changeRefs: changes.refs,
	};
	fakeReqs.push(rec);
	let res: Response;
	if (req.method !== "POST")
		res = errorJson(404, "not_found_error", `m12 fake: ${url.pathname}`);
	else if (url.pathname.endsWith("/v1/messages/count_tokens"))
		res = Response.json({ input_tokens: 10 });
	else if (!url.pathname.endsWith("/v1/messages"))
		res = errorJson(404, "not_found_error", `m12 fake: ${url.pathname}`);
	else if (rec.toolAddition)
		res = errorJson(400, "invalid_request_error", TOOL_CHANGE_REFUSAL);
	else if (!rec.stream)
		res = Response.json({
			id: `msg_m12_${rec.n}`,
			type: "message",
			role: "assistant",
			model: rec.model,
			content: [{ type: "text", text: "ok" }],
			stop_reason: "end_turn",
			stop_sequence: null,
			usage: { input_tokens: 10, output_tokens: 1 },
		});
	else res = sse(rec.model);
	rec.status = res.status;
	return res;
}

function startFake(): number {
	fakeServer ??= Bun.serve({
		hostname: "127.0.0.1",
		port: 0,
		idleTimeout: 120,
		fetch: fakeHandle,
	});
	const port = fakeServer.port;
	if (!port) throw new Error("假端点没有拿到端口");
	return port;
}

/** 回合请求：带非空 tools 的 /v1/messages（排除无工具的辅助请求）。 */
const turnReqs = (from: number) =>
	fakeReqs
		.slice(from)
		.filter((r) => r.path.endsWith("/v1/messages") && r.tools.length > 0);
const reqLine = (r: FakeReq) =>
	`#${r.n} ${r.status} tools=${r.tools.length}${r.tools.includes("terminal") ? "(含 terminal)" : ""} tool_addition=${r.toolAddition}${r.changeRefs.length ? `→${r.changeRefs.join(",")}` : ""}`;

// ---------------------------------------------------------------- runner（照 m11b）

let uhome = "";
let hostPrepared = false;
const userModels = () => `${uhome}/.omp/agent/models.yml`;
/** 改动前 runner 用户 models.yml 的原文；undefined = 没改过。 */
let modelsBackup: string | undefined;

function prepareRunnerHost() {
	if (hostPrepared) return;
	if (process.getuid?.() !== 0)
		throw new Error("runner 场景要以 root 运行（useradd / runuser）");
	must(
		sh(`id -u ${RUSER} >/dev/null 2>&1 || useradd -m -s /bin/bash ${RUSER}`),
		`建用户 ${RUSER}`,
	);
	uhome = must(sh(`getent passwd ${RUSER} | cut -d: -f6`), "取家目录");
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
	if (!existsSync(models))
		throw new Error(`root 没有 ${models}：runner 上的 agent 无法应答`);
	must(
		sh(
			`install -d -o ${RUSER} -g ${RUSER} -m 700 ${q(`${uhome}/.omp`)} ${q(`${uhome}/.omp/agent`)} && install -o ${RUSER} -g ${RUSER} -m 600 ${q(models)} ${q(userModels())}`,
		),
		"拷贝 models.yml",
	);
	snote(`已把 root 的 ${models} 拷到 ${userModels()}（0600，属主 ${RUSER}）`);
	must(
		sh(
			`rm -rf ${DIR_A} ${DIR_B} && install -d -o ${RUSER} -g ${RUSER} -m 755 ${DIR_A} ${DIR_B}`,
		),
		"重建目录",
	);
	snote(`${DIR_A}、${DIR_B} 已重建，属主 ${RUSER}`);
	hostPrepared = true;
}

/** runner 用户 models.yml 加 provider m12fake（JSON 也是合法 YAML，原文先备份，收尾还原）。 */
function installFakeProvider(port: number) {
	const f = userModels();
	const text = readFileSync(f, "utf8");
	modelsBackup ??= text;
	const doc = (Bun.YAML.parse(modelsBackup) ?? {}) as Record<string, unknown>;
	const providers = isRecord(doc.providers) ? doc.providers : {};
	providers[FAKE_PROVIDER] = {
		baseUrl: `http://127.0.0.1:${port}`,
		apiKey: "m12-fake-key",
		api: "anthropic-messages",
		auth: "apiKey",
		// 规则只对 provider anthropic / cloudflare-ai-gateway / google-vertex 打开工具变更，自定 provider 要显式开
		compat: { supportsMidConversationToolChanges: true },
		models: [
			{
				id: FAKE_MODEL,
				name: "M12 fake Claude Opus 5.5",
				reasoning: false,
				input: ["text"],
				contextWindow: 200_000,
				maxTokens: 8_192,
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
			},
		],
	};
	doc.providers = providers;
	writeFileSync(f, `${JSON.stringify(doc, null, 2)}\n`);
	note(
		`${f} 加 provider ${FAKE_PROVIDER}（baseUrl http://127.0.0.1:${port}，compat.supportsMidConversationToolChanges: true）`,
	);
}

function restoreModels() {
	if (modelsBackup === undefined) return;
	writeFileSync(userModels(), modelsBackup);
	modelsBackup = undefined;
}

let runnerSeq = 0;
let runner: RunnerView | undefined;
let runnerToken = "";
let runnerProc: Subprocess | undefined;
let terminalFlag = false;

const runnerName = () => `m12-${RUN}${runnerSeq > 1 ? `-${runnerSeq}` : ""}`;

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
		`runner ${id} 在线${wantTerminal ? "且带 terminal" : "且不带 terminal"}`,
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

/** （重）起 runner 并等它以期望的 terminal 特性在线。 */
async function restartRunner(terminal: boolean): Promise<RunnerView> {
	prepareRunnerHost();
	if (!runner) await createRunner();
	await stopRunner();
	startRunner(terminal);
	const r = await waitRunnerOnline(terminal);
	note(`runner 在线，特性 ${r.features.join(",")}`);
	return r;
}

/** `POST threads` 在 runner 目录建线程（可指定模型），返回线程 id。 */
async function newRunnerThread(
	dir: string,
	tag: string,
	prompt: string,
	model?: { provider: string; id: string },
): Promise<string> {
	const r = runner;
	if (!r) throw new Error("还没有 runner");
	const res = await api("POST", "/api/v1/threads", {
		prompt,
		title: `m12 ${RUN} ${tag}`,
		...(model ? { model } : {}),
		executor: { kind: "runner", runnerId: r.id, dir, shareDir: true },
	});
	const tid = (res.json as { thread?: { id?: string } } | null)?.thread?.id;
	if (res.status >= 300 || !tid)
		throw new Error(`POST threads（runner）→ ${res.status} ${res.text}`);
	focus = tid;
	note(
		`${tag} 线程 ${tid}（runner ${r.name}，dir ${dir}${model ? `，模型 ${model.provider}/${model.id}` : ""}）`,
	);
	return tid;
}

/** runner 用户的 http-400 落盘目录里的文件名。 */
const http400Dir = () => `${uhome}/.omp/logs/http-400-requests`;
const http400Files = () =>
	existsSync(http400Dir()) ? new Set(readdirSync(http400Dir())) : new Set();

// ---------------------------------------------------------------- 普通 orb

const orbs = new Set<string>();
let mainT = "";

async function ensureOrbReady(orb: string): Promise<string> {
	const st = orbDbState(orb);
	if (st !== "ready") {
		if (st === "frozen" || st === "stopped" || st === "failed") {
			const r = await api("POST", `/api/v1/orbs/${orb}/resume`);
			note(`orb ${orb} 为 ${st}，POST resume → ${r.status}`);
		}
		await waitFor(
			`orb ${orb} ready`,
			() => orbDbState(orb) === "ready",
			300_000,
			1000,
		);
	}
	return orb;
}

/** 项目 M12_PROJECT 下的主 orb 线程（第一条「只回复 ok」到 idle）；返回 orb id。 */
async function ensureOrb(): Promise<string> {
	if (mainT) {
		const o = orbOf(mainT);
		if (!["deleting", "deleted", "?"].includes(orbDbState(o)))
			return ensureOrbReady(o);
	}
	const out = await cli(
		`thread new --project ${q(PROJECT)} --provider ${q(ORB_PROVIDER)} --title ${q(`m12 ${RUN} main`)} ${q("只回复 ok")}`,
	);
	if (out.code !== 0)
		throw new Error(`oryn thread new --project 失败：${out.out}`);
	const tid = out.stdout.trim().split("\n").at(-1) ?? "";
	focus = tid;
	const orb = await waitFor(
		`线程 ${tid} 分到 orb`,
		() => threadRow(tid)?.orb_id ?? undefined,
		120_000,
	);
	orbs.add(orb);
	await waitIdle(tid, 0, 1_200_000);
	mainT = tid;
	note(`主 orb 线程 ${tid}（项目 ${PROJECT}，orb ${orb}）`);
	return ensureOrbReady(orb);
}

/** 挂到主 orb 上的新线程；waitIdleFirst 为真时等第一条回合到 idle。 */
async function newOnOrb(
	prompt: string,
	tag: string,
	waitIdleFirst = true,
): Promise<string> {
	const orb = await ensureOrb();
	const out = await cli(
		`thread new --orb ${orb} --title ${q(`m12 ${RUN} ${tag}`)} ${q(prompt)}`,
	);
	if (out.code !== 0) throw new Error(`oryn thread new --orb 失败：${out.out}`);
	const tid = out.stdout.trim().split("\n").at(-1) ?? "";
	focus = tid;
	note(`${tag} 线程 ${tid}（orb ${orb}）`);
	if (waitIdleFirst) await waitIdle(tid);
	return tid;
}

// ---------------------------------------------------------------- 场景：工具变更降级（runner + 假端点）

let t1 = "";
let y1Done = false;

async function Y1() {
	note(
		"假 Anthropic 端点：不带 --terminal 建线程回复 ok → 带 --terminal 重启再发，tool_addition 400 后同回合降级重放",
	);
	prepareRunnerHost();
	const port = startFake();
	note(`假端点 http://127.0.0.1:${port}`);
	await stopRunner();
	installFakeProvider(port);
	await restartRunner(false);
	const m0 = fakeReqs.length;
	t1 = await newRunnerThread(DIR_A, "Y1", "回复 ok", {
		provider: FAKE_PROVIDER,
		id: FAKE_MODEL,
	});
	await waitIdle(t1, 0, 300_000);
	const st = await rpc(t1, { type: "get_state" });
	const model = isRecord(st.resp?.data) ? st.resp.data.model : undefined;
	const mp = isRecord(model)
		? `${String(model.provider)}/${String(model.id)}`
		: "?";
	check(mp === `${FAKE_PROVIDER}/${FAKE_MODEL}`, `get_state 的模型 ${mp}`);
	const first = turnReqs(m0);
	for (const r of first) note(`回合 1 请求 ${reqLine(r)}`);
	check(first.length >= 1, `回合 1 打到假端点（${first.length} 次）`);
	check(
		first.every((r) => !r.toolAddition && !r.tools.includes("terminal")),
		"回合 1 请求不含 tool_addition、tools 不含 terminal",
	);

	await restartRunner(true);
	const m1 = fakeReqs.length;
	const before400 = http400Files();
	await say(t1, "回复 ok", 300_000);
	const last = await lastAssistantMsg(t1);
	check(
		last.stopReason !== "error",
		`最后一条 assistant stopReason=${last.stopReason}（${last.text.slice(0, 40)}）`,
	);
	const second = turnReqs(m1);
	for (const r of second) note(`回合 2 请求 ${reqLine(r)}`);
	const rejected = second.filter((r) => r.status === 400);
	check(rejected.length === 1, `回合 2 恰好 1 次 400（${rejected.length}）`);
	const r400 = rejected[0];
	check(
		!!r400?.toolAddition && r400.changeRefs.includes("terminal"),
		`被拒的请求带 tool_addition → terminal（${r400?.changeRefs.join(",")}）`,
	);
	const after = second.filter((r) => r400 && r.n > r400.n);
	check(after.length === 1, `400 之后恰好 1 次请求（${after.length}）`);
	const replay = after[0];
	check(
		!!replay &&
			replay.status === 200 &&
			!replay.toolAddition &&
			replay.tools.includes("terminal"),
		`重放：200、不含 tool_addition、tools 含 terminal（${replay ? reqLine(replay) : "无"}）`,
	);
	const new400 = [...http400Files()].filter((f) => !before400.has(f));
	note(
		`runner 用户 http-400-requests 新增 ${new400.length} 份：${new400.join(", ")}`,
	);
	y1Done = true;
}

async function Y3() {
	note("Y1 的线程再发第三条：同一 omp 进程内已闩锁，假端点 0 次 400");
	if (!y1Done) await Y1();
	focus = t1;
	const r = runner;
	check(
		!!r && terminalFlag && !!runnerProc,
		"runner 仍是 Y1 那次带 --terminal 的进程（未重启）",
	);
	const m = fakeReqs.length;
	await say(t1, "回复 ok", 300_000);
	const reqs = turnReqs(m);
	for (const x of reqs) note(`回合 3 请求 ${reqLine(x)}`);
	check(reqs.length >= 1, `回合 3 打到假端点（${reqs.length} 次）`);
	check(
		reqs.every((x) => x.status !== 400),
		`回合 3 0 次 400（${reqs.filter((x) => x.status === 400).length}）`,
	);
	check(
		reqs.every((x) => !x.toolAddition),
		"回合 3 请求不含 tool_addition",
	);
	const last = await lastAssistantMsg(t1);
	check(last.stopReason !== "error", `stopReason=${last.stopReason}`);
}

async function Y4() {
	note(
		"Y1 的线程：runner 去掉 --terminal 重启后再发一条，tools 不含 terminal、无 400",
	);
	if (!y1Done) await Y1();
	focus = t1;
	await restartRunner(false);
	const m = fakeReqs.length;
	await say(t1, "回复 ok", 300_000);
	const reqs = turnReqs(m);
	for (const x of reqs) note(`回合 4 请求 ${reqLine(x)}`);
	check(reqs.length >= 1, `回合 4 打到假端点（${reqs.length} 次）`);
	check(
		reqs.every((x) => !x.tools.includes("terminal")),
		"回合 4 请求的 tools 不含 terminal",
	);
	check(
		reqs.every((x) => x.status !== 400),
		`回合 4 0 次 400（${reqs.filter((x) => x.status === 400).length}）`,
	);
	const last = await lastAssistantMsg(t1);
	check(last.stopReason !== "error", `stopReason=${last.stopReason}`);
}

async function Y2() {
	note(
		`真实网关（${REAL_MODEL || "runner 用户 omp 的默认模型"}）：不带 --terminal 建线程 → 带 --terminal 重启再发；判 fallback / native`,
	);
	prepareRunnerHost();
	const doc = Bun.YAML.parse(readFileSync(userModels(), "utf8")) as {
		providers?: Record<string, { compat?: Record<string, unknown> } | null>;
	} | null;
	const [prov = "", ...rest] = REAL_MODEL.split("/");
	const id = rest.join("/");
	const pc = prov ? doc?.providers?.[prov]?.compat : undefined;
	check(
		pc?.supportsMidConversationToolChanges !== false,
		`${userModels()} 里 provider ${prov || "?"} 没有 compat.supportsMidConversationToolChanges: false 止血（否则走不到降级路径）`,
	);
	await restartRunner(false);
	const tid = await newRunnerThread(
		DIR_B,
		"Y2",
		"回复 ok",
		prov && id ? { provider: prov, id } : undefined,
	);
	await waitIdle(tid, 0, 600_000);
	await restartRunner(true);
	const before = http400Files();
	await say(tid, "回复 ok", 600_000);
	const last = await lastAssistantMsg(tid);
	check(
		last.stopReason !== "error",
		`最后一条 assistant stopReason=${last.stopReason}（${last.text.slice(0, 40)}）`,
	);
	const added = [...http400Files()].filter((f) => !before.has(f));
	const hits = added.filter((f) =>
		readFileSync(`${http400Dir()}/${f}`, "utf8").includes(
			"references unknown tool 'terminal'",
		),
	);
	note(
		`http-400-requests 新增 ${added.length} 份，含 references unknown tool 'terminal' 的 ${hits.length} 份：${hits.join(", ")}`,
	);
	note(`路径：${hits.length > 0 ? "fallback" : "native"}`);
	console.log(`    Y2 路径 = ${hits.length > 0 ? "fallback" : "native"}`);
}

// ---------------------------------------------------------------- 场景：普通 orb

/** 轮询线程直到 beforeSeq 之后有 assistant 且连续 10 秒停在 idle；期间见过 error 即记下。 */
async function settleStable(tid: string, beforeSeq: number, timeoutMs: number) {
	const deadline = Date.now() + timeoutMs;
	let sawError = false;
	let idleSince = 0;
	while (Date.now() < deadline) {
		const r = threadRow(tid);
		if (r?.state === "error") sawError = true;
		if (r?.state === "idle" && assistantsAfter(tid, beforeSeq) > 0) {
			idleSince ||= Date.now();
			if (Date.now() - idleSince >= 10_000) return { state: r.state, sawError };
		} else idleSince = 0;
		await sleep(500);
	}
	throw new Error(`超时（${timeoutMs}ms）：线程 ${tid} 稳定在 idle`);
}

async function Y5() {
	note(
		"普通 orb 线程先回复 ok，再 oryn thread rpc set_goal：线程最终 idle、不进 error",
	);
	const tid = await newOnOrb("只回复 ok", "Y5");
	const before = lastSeq(tid);
	const r = await rpc(tid, {
		type: "set_goal",
		objective: "回复 ok 后结束目标",
	});
	note(
		`thread rpc set_goal → ${r.code} ${r.stdout.replaceAll("\n", " ").slice(0, 200)}`,
	);
	check(r.code === 0 && r.resp?.success === true, "set_goal 成功");
	let sawError = false;
	const started = await waitFor(
		"set_goal 后自动续跑",
		() => {
			const row = threadRow(tid);
			if (row?.state === "error") sawError = true;
			return row?.state === "working" || assistantsAfter(tid, before) > 0;
		},
		30_000,
	).catch(() => false);
	if (!started) {
		note(
			"30 秒内没有自动续跑（goal.continuationModes 不含 rpc？），补发一条消息推进目标",
		);
		const s = await cli(
			`thread send ${tid} ${q("继续推进当前目标：回复 ok 后结束目标")}`,
		);
		check(s.code === 0, `thread send → ${s.code}`);
	}
	const res = await settleStable(tid, before, 900_000);
	check(!sawError && !res.sawError, "期间线程没有进入 error");
	check(res.state === "idle", `线程最终 ${res.state}`);
	const es = (await entriesOf(tid)).filter((e) => e.seq > before);
	const replies = es
		.map(msgOf)
		.filter((m) => m?.role === "assistant" && textOf(m.content).trim());
	check(
		replies.length > 0,
		`set_goal 之后有 assistant 回复（${replies.length} 条）`,
	);
	const goalEntries = es.filter((e) => /goal/i.test(JSON.stringify(e.payload)));
	note(
		`goal 相关条目：${goalEntries.map((e) => `${e.seq}:${e.type}`).join(", ") || "无"}`,
	);
	check(goalEntries.length > 0, "转录里有 goal 相关条目");
	const last = await lastAssistantMsg(tid);
	check(last.stopReason !== "error", `stopReason=${last.stopReason}`);
	await rpc(tid, { type: "set_goal", action: "drop" });
}

/** xdev 派发的真实工具名（照 web/src/tool-render/ToolView.tsx 的 executeXdevDispatch）。 */
function xdevTools(es: Entry[]): string[] {
	const out: string[] = [];
	for (const e of es) {
		const m = msgOf(e);
		if (m?.role !== "toolResult" || m.toolName !== "write") continue;
		const d = isRecord(m.details) ? m.details : undefined;
		const x = d && isRecord(d.xdev) ? d.xdev : undefined;
		if (x?.mode === "execute" && typeof x.tool === "string") out.push(x.tool);
	}
	return out;
}

async function Y6() {
	note(
		"orb 线程经 write xd://ast_edit 调工具：tool_calls 记真实工具名；Web 卡片名 xd://<工具>；facts_version=5",
	);
	// 当前 Core 没有 ast_grep 设备（`No such tool`），改用已挂载的 ast_edit：提案只暂存，随后 reject，不改文件
	const tid = await newOnOrb(
		"先用 bash 执行 `printf 'console.log(1)\\n' > /tmp/m12-y6.ts`，再用 `write xd://ast_edit` 对 /tmp/m12-y6.ts 提一个把 `console.log($A)` 改成 `console.info($A)` 的改写，然后写 `xd://reject` 放弃这个提案，最后只回复 done。只在本机执行，不要用 MCP 或远程主机工具",
		"Y6",
	);
	const tools = xdevTools(await entriesOf(tid));
	note(`xdev 派发：${tools.join(",") || "无"}`);
	check(
		tools.length > 0,
		"转录里有 xdev 派发（write + details.xdev.mode=execute）",
	);
	const want = tools.includes("ast_edit") ? "ast_edit" : (tools[0] ?? "");
	if (want !== "ast_edit")
		note(`agent 没有派发 ast_edit（计划 A7），改按 ${want} 断言`);
	const rows = await waitFor(
		`tool_calls 出现 ${want}`,
		() => {
			const r = sql<{ tool: string }>(
				`select tool from tool_calls where thread_id='${tid}'`,
			).map((x) => x.tool);
			return r.includes(want) ? r : undefined;
		},
		60_000,
		1000,
	);
	note(`tool_calls.tool：${rows.join(",")}`);
	check(rows.includes(want), `tool_calls 含 ${want}`);
	const fv = one<{ value: string }>(
		"select value from stats_state where key='facts_version'",
	)?.value;
	check(fv === "5", `stats_state.facts_version = ${fv}`);
	const page = await newPage();
	try {
		await page.goto(`${BASE}/t/${tid}`);
		const name = page.locator(".tv-name", {
			hasText: new RegExp(`^xd://${want}$`),
		});
		await name.first().waitFor({ timeout: 60_000 });
		check(true, `Web 卡片名 xd://${want}`);
		await shot(page, "Y6-xdev-card");
	} finally {
		await page.close();
	}
}

/** task 卡片（`tool-render/tools/task.tsx`）里进度行的状态徽标取值。 */
const PROGRESS_STATUSES = [
	"pending",
	"queued",
	"running",
	"completed",
	"failed",
	"aborted",
];

interface TaskProbe {
	ids: string[];
	running: boolean;
	partial: number;
	open: boolean;
	badges: string[];
}

/**
 * 读 task 卡片：进度行 = `.tv-body .tv-row` 中 `.tv-row-key` 非空（子 agent id，AgentLink）且 `.tv-row-val` 的第一个直接
 * `.tv-badge` 是状态值（AgentProgressRow）；任务清单行的第一个徽标是 agent 名，不会混进来。
 */
const probeTask = (card: Locator) =>
	card.evaluate((el, statuses) => {
		const rows = [...el.querySelectorAll(".tv-body .tv-row")].filter((r) => {
			const b = r.querySelector(":scope > .tv-row-val > .tv-badge");
			const k = r.querySelector(":scope > .tv-row-key");
			return (
				!!b &&
				statuses.includes((b.textContent ?? "").trim()) &&
				!!k?.textContent?.trim()
			);
		});
		return {
			ids: rows.map(
				(r) =>
					r.querySelector(":scope > .tv-row-key")?.textContent?.trim() ?? "",
			),
			running: !!el.querySelector(".tv-head .tv-spin"),
			partial: el.querySelectorAll(".tv-partial").length,
			open:
				el.querySelector(".tv-head")?.getAttribute("aria-expanded") === "true",
			badges: [...el.querySelectorAll(".tv-badge")].map((b) =>
				(b.textContent ?? "").trim(),
			),
		};
	}, PROGRESS_STATUSES) as Promise<TaskProbe>;

let t7 = "";

/** 独占 orb 上的新线程（第一条「只回复 ok」到 idle）：改 omp 设置（会落盘到该 orb）的场景用它，不污染主 orb。 */
async function newOwnOrb(tag: string): Promise<string> {
	const out = await cli(
		`thread new --project ${q(PROJECT)} --provider ${q(ORB_PROVIDER)} --title ${q(`m12 ${RUN} ${tag}`)} ${q("只回复 ok")}`,
	);
	if (out.code !== 0)
		throw new Error(`oryn thread new --project 失败：${out.out}`);
	const tid = out.stdout.trim().split("\n").at(-1) ?? "";
	focus = tid;
	const orb = await waitFor(
		`线程 ${tid} 分到 orb`,
		() => threadRow(tid)?.orb_id ?? undefined,
		120_000,
	);
	orbs.add(orb);
	await waitIdle(tid, 0, 1_200_000);
	note(`${tag} 线程 ${tid}（独占 orb ${orb}）`);
	return tid;
}

// scout 类型没有本地 bash，会改用 MCP 远程执行工具；指定默认 task 类型并禁止远程 / MCP 工具，保证只在 orb 内执行
const TASK_PROMPT =
	"用 task 工具并行派两个子 agent（agent 类型用默认的 task，不要用 scout），每个子 agent 只用自己的 bash 工具在本机执行 `sleep 25`，禁止使用任何 MCP 或远程主机工具（例如 onessh），执行完回复 done；两个都完成后你只回复 done";

async function Y7() {
	note(
		"同步 task（独占 orb 上 async.enabled=false）并行两个子 agent 各 sleep 25：运行中展开卡片见进度行、无 .tv-partial；完成后结果行与 2 succeeded",
	);
	const tid = await newOwnOrb("Y7");
	// RPC 宿主默认异步派发（task 立即返回、进度在 wait 里）；关掉才走 task 卡片自身的运行中进度
	const set = await cli(
		`thread rpc ${tid} ${q(JSON.stringify({ type: "set_setting", path: "async.enabled", value: false }))}`,
	);
	check(set.code === 0, `set_setting async.enabled=false → ${set.code}`);
	const before = lastSeq(tid);
	const s = await cli(`thread send ${tid} ${q(TASK_PROMPT)}`);
	if (s.code !== 0) throw new Error(`thread send 失败：${s.out}`);
	const page = await newPage();
	try {
		await page.goto(`${BASE}/t/${tid}`);
		const card = toolCard(page, "task");
		await card.waitFor({ timeout: 600_000 });
		const deadline = Date.now() + 600_000;
		let seen: TaskProbe | undefined;
		while (Date.now() < deadline) {
			const p = await probeTask(card);
			if (
				!p.running &&
				p.badges.some((b) => /succeeded|failed|aborted/.test(b))
			)
				throw new Error("task 卡片在出现进度行之前就结束了");
			if (p.running && !p.open) await card.locator(".tv-head").click();
			else if (p.running && p.ids.length > 0) {
				seen = p;
				break;
			}
			await sleep(300);
		}
		if (!seen) throw new Error("超时：运行中的 task 卡片没有出现进度行");
		note(`运行中进度行：${seen.ids.join(", ")}`);
		check(
			seen.ids.length >= 1,
			`完成前卡片内有 ${seen.ids.length} 条带子 agent id 的进度行`,
		);
		check(seen.partial === 0, `卡片下方没有 .tv-partial（${seen.partial}）`);
		await shot(page, "Y7-progress");
		await waitIdle(tid, before, 900_000);
		const done = await waitFor(
			"task 卡片出现 2 succeeded",
			async () => {
				const p = await probeTask(card);
				if (!p.open) await card.locator(".tv-head").click();
				return p.badges.includes("2 succeeded") ? p : undefined;
			},
			60_000,
		);
		note(`完成后徽标：${done.badges.join(" | ").slice(0, 300)}`);
		check(done.badges.includes("2 succeeded"), "完成后有「2 succeeded」");
		check(
			!done.running && done.partial === 0,
			"完成后无运行标记、无 .tv-partial",
		);
		// 子 agent 的工具调用条目里不得出现 MCP 远程执行（只看 toolCall 名，工具清单文本不算）
		const remote = sql<{ n: number }>(
			`select count(*) as n from entries where thread_id='${tid}' and payload like '%"type":"toolCall"%' and payload like '%mcp\\_\\_onessh%' escape '\\'`,
		)[0]?.n;
		check(remote === 0, `没有经 MCP 远程执行（${remote} 条）`);
		await shot(page, "Y7-done");
	} finally {
		await page.close();
	}
	t7 = tid;
}

/** 默认异步派发：task 立即返回已派发的子 agent，进度与结果在随后的 wait 卡片里。 */
async function Y7b() {
	note(
		"异步 task（默认）：task 卡片成功结束并列出派发；wait 卡片成功；子 agent 列表出现两条；全程不经 MCP 远程执行",
	);
	const tid = await newOnOrb(TASK_PROMPT, "Y7b", false);
	await waitIdle(tid, 0, 900_000);
	const calls = sql<{ tool: string }>(
		`select tool from tool_calls where thread_id='${tid}'`,
	).map((x) => x.tool);
	note(`tool_calls：${calls.join(",")}`);
	check(calls.includes("task"), "主线调用了 task");
	check(calls.includes("wait"), "主线随后用 wait 等子 agent（异步派发）");
	const remote = sql<{ n: number }>(
		`select count(*) as n from entries where thread_id='${tid}' and payload like '%"type":"toolCall"%' and payload like '%mcp\\_\\_onessh%' escape '\\'`,
	)[0]?.n;
	check(remote === 0, `没有经 MCP 远程执行（${remote} 条）`);
	const page = await newPage();
	try {
		await page.goto(`${BASE}/t/${tid}`);
		const task = toolCard(page, "task");
		await task.waitFor({ timeout: 60_000 });
		check(
			(await task.locator(".tv-status--ok").count()) === 1,
			"task 卡片成功结束",
		);
		const wait = toolCard(page, "wait");
		await wait.waitFor({ timeout: 60_000 });
		check(
			(await wait.locator(".tv-status--err").count()) === 0,
			"wait 卡片没有失败",
		);
		await openTab(page, "info");
		const sec = cardSection(page, "子 agent");
		await sec.waitFor({ timeout: 60_000 });
		await expandCard(sec);
		const rows = sec.locator("ul > li", { hasText: /sleep 25/ });
		const n = await waitFor(
			"子 agent 列表出现两条 sleep 任务",
			async () => ((await rows.count()) >= 2 ? rows.count() : undefined),
			30_000,
		).catch(async () => rows.count());
		check(n >= 2, `子 agent 列表有 ${n} 条 sleep 任务`);
		await shot(page, "Y7b-async");
	} finally {
		await page.close();
	}
}

async function Y8() {
	note(
		"Y7 结束后在已打开的线程页上冻结 orb：子 agent 列表 2 行；对话框显示已保存的转录且控制按钮不可点",
	);
	if (!t7) await Y7();
	const tid = t7;
	focus = tid;
	const orb = orbOf(tid);
	await ensureOrbReady(orb);
	const page = await newPage();
	try {
		// 先打开页面再冻结：页面加载时的 focus 会按 dev 的 wake_on_focus 立即唤醒冻结的 orb，
		// 冻结后才打开就看不到离线状态
		await page.goto(`${BASE}/t/${tid}`);
		await openTab(page, "info");
		const sec8 = cardSection(page, "子 agent");
		await sec8.waitFor({ timeout: 60_000 });
		await expandCard(sec8);
		const p = await api("POST", `/api/v1/orbs/${orb}/pause`, {
			mode: "freeze",
		});
		check(
			p.status === 202,
			`POST orbs/${orb}/pause {mode: freeze} → ${p.status}`,
		);
		await waitFor(
			`orb ${orb} frozen`,
			() => orbDbState(orb) === "frozen",
			180_000,
			1000,
		);
		await checkOfflineSubagents(page, sec8, "Y8-subagent-offline");
	} finally {
		await page.close();
	}
	// 冷加载：新页面只拿入库的条目与快照。只滤掉该页发往服务端的 focus 帧（否则按 wake_on_focus 会唤醒 orb），
	// 订阅、快照与转录请求照常走真实服务端
	const cold = await newPage();
	let focusDropped = 0;
	try {
		await cold.routeWebSocket(/\/api\/v1\/ws\b/, (ws) => {
			const server = ws.connectToServer();
			ws.onMessage((m) => {
				if (typeof m === "string" && JSON.parse(m).type === "focus") {
					focusDropped++;
					return;
				}
				server.send(m);
			});
		});
		await cold.goto(`${BASE}/t/${tid}`);
		await openTab(cold, "info");
		const sec = cardSection(cold, "子 agent");
		await sec.waitFor({ timeout: 60_000 });
		await expandCard(sec);
		// 布局检查放在冷加载页：切到手机宽度会让线程页重新挂载并再发 focus，这一页的 focus 已被过滤，不会唤醒 orb
		await checkOfflineSubagents(cold, sec, "Y8-subagent-offline-cold", true);
		note(`冷加载页滤掉 focus 帧 ${focusDropped} 个`);
		check(focusDropped > 0, "冷加载页确实发过 focus（已被过滤）");
		check(
			orbDbState(orb) === "frozen",
			`冷加载全程 orb 保持冻结（${orbDbState(orb)}）`,
		);
	} finally {
		await cold.close();
	}
}

/** 冻结 orb 时的子 agent 列表与对话框：2 行 task 子 agent；对话框显示已保存的转录、无错误、控制按钮不可点。 */
async function checkOfflineSubagents(
	page: Page,
	sec: Locator,
	shotName: string,
	checkLayout = false,
): Promise<void> {
	// 只数 Y7 派发的 task 子 agent：行里显示的是模型自拟的描述（不一定含「sleep 25」），按排除常驻 advisor 计数
	const rows = sec.locator("ul > li").filter({ hasNotText: /advisor/ });
	const n = await waitFor(
		"子 agent 列表 2 行",
		async () => ((await rows.count()) === 2 ? 2 : undefined),
		30_000,
	).catch(async () => rows.count());
	note(
		`子 agent 行：${(await rows.allInnerTexts()).map((s) => s.replaceAll("\n", " ")).join(" | ")}`,
	);
	check(n === 2, `列表有 2 行（${n}）`);
	await rows.first().locator("button").click();
	const dlg = page.locator("[role=dialog]");
	await dlg.waitFor({ timeout: 10_000 });
	await dlg
		.getByText("agent 未运行，显示已保存的转录")
		.waitFor({ timeout: 10_000 });
	check(true, "对话框显示「agent 未运行，显示已保存的转录」");
	const msgs = await waitFor(
		"对话框里出现消息",
		async () => {
			const c = await dlg
				.locator("div.flex.flex-col.gap-2")
				.first()
				.locator(":scope > *")
				.count();
			return c > 0 ? c : undefined;
		},
		15_000,
	).catch(() => 0);
	check(msgs >= 1, `对话框显示 ${msgs} 条消息`);
	const alerts = await dlg.locator("[role=alert]").allInnerTexts();
	check(alerts.length === 0, `无错误文字（${alerts.join(" | ")}）`);
	for (const name of ["取消", "终止", "复活", "发送"]) {
		const b = dlg.getByRole("button", { name, exact: true });
		check(
			(await b.count()) === 1 && (await b.isDisabled()),
			`按钮「${name}」不可点`,
		);
	}
	await shot(page, shotName);
	if (!checkLayout) return;
	// 「发送」按钮与引导输入框同在一行 flex 里：桌面宽度与手机宽度下按钮文字都只占一行，且两者都不超出表单；
	// 跨过手机断点后对话框仍开着（外壳换结构让线程页重新挂载，同一线程不关对话框）
	const send = dlg.getByRole("button", { name: "发送", exact: true });
	const desktop = page.viewportSize() ?? { width: 1400, height: 900 };
	try {
		for (const vp of [desktop, { width: 390, height: 844 }]) {
			await page.setViewportSize(vp);
			const layout = await send.evaluate((btn) => {
				const range = document.createRange();
				range.selectNodeContents(btn);
				const lines = new Set(
					[...range.getClientRects()]
						.filter((r) => r.width > 0)
						.map((r) => Math.round(r.top)),
				).size;
				const form = btn.closest("form");
				if (!form) return { lines, overflow: ["没有 form"] };
				const box = form.getBoundingClientRect();
				const overflow = [...form.children]
					.map((el) => el.getBoundingClientRect())
					.filter((r) => r.left < box.left - 0.5 || r.right > box.right + 0.5)
					.map((r) => `${Math.round(r.left)}..${Math.round(r.right)}`);
				return {
					lines,
					overflow:
						overflow.length > 0
							? [
									`表单 ${Math.round(box.left)}..${Math.round(box.right)}`,
									...overflow,
								]
							: [],
				};
			});
			check(
				layout.lines === 1,
				`${vp.width}px 宽度下「发送」文字占 ${layout.lines} 行`,
			);
			check(
				layout.overflow.length === 0,
				`${vp.width}px 宽度下输入框与按钮不超出表单（${layout.overflow.join(" ")}）`,
			);
		}
		await shot(page, `${shotName}-390`);
	} finally {
		await page.setViewportSize(desktop);
	}
}

async function Y9() {
	note(
		"CLI 审批计划：set_plan_mode → send → 管道 stdin 的 attach 见 [plan] 后写 1；state 离开 awaiting_input、Web 计划对话框消失",
	);
	const tid = await newOnOrb("只回复 ok", "Y9");
	const pm = await rpc(tid, { type: "set_plan_mode", enabled: true });
	check(
		pm.code === 0,
		`thread rpc set_plan_mode → ${pm.code} ${pm.stderr.trim()}`,
	);
	const s = await cli(
		`thread send ${tid} ${q("写一个只含一步的计划（这一步：只回复 ok）并直接提交审批，不要向我提问")}`,
	);
	check(s.code === 0, `thread send → ${s.code}`);
	// 用 `cat |` 让 attach 的 stdin 是匿名管道（attach.go 的 isPipe：管道 EOF 只结束输入、不退出）
	const proc = Bun.spawn(
		["bash", "-c", `cat | ${CLI_ENV} ${q(ORYN)} thread attach ${tid}`],
		{ cwd: ROOT, stdin: "pipe", stdout: "pipe", stderr: "pipe" },
	);
	let out = "";
	const pump = async (s: ReadableStream<Uint8Array>) => {
		const dec = new TextDecoder();
		for await (const chunk of s) out += dec.decode(chunk, { stream: true });
	};
	void pump(proc.stdout);
	void pump(proc.stderr);
	const stdin = proc.stdin as FileSink;
	const page = await newPage();
	try {
		await page.goto(`${BASE}/t/${tid}`);
		await waitFor(
			"attach 输出 [plan]",
			() => out.includes("[plan] "),
			900_000,
			500,
		);
		await sleep(500);
		const opts = out.match(/^\s+\d+\) \S.*$/gm) ?? [];
		note(
			`attach：${out.slice(out.indexOf("[plan] "), out.indexOf("[plan] ") + 120).replaceAll("\n", " | ")}`,
		);
		note(`编号选项：${opts.map((o) => o.trim()).join(" ; ")}`);
		check(opts.length > 0, "attach 输出含 [plan] 与编号选项");
		const show = await cli(`thread show ${tid} --json`);
		const st0 = (JSON.parse(show.stdout) as ThreadView).state;
		check(st0 === "awaiting_input", `作答前 state=${st0}`);
		const dlg = page.locator('[role=dialog][aria-label^="计划提案："]');
		await dlg.waitFor({ timeout: 30_000 });
		check(true, "Web 上计划对话框出现");
		stdin.write("1\n");
		await stdin.flush();
		const st1 = await waitFor(
			"thread show 的 state 离开 awaiting_input",
			async () => {
				const r = await cli(`thread show ${tid} --json`);
				const st = (JSON.parse(r.stdout) as ThreadView).state;
				return st !== "awaiting_input" ? st : undefined;
			},
			120_000,
			2000,
		);
		check(true, `作答后 state=${st1}`);
		await dlg.waitFor({ state: "detached", timeout: 60_000 });
		check(true, "Web 上计划对话框消失");
		await shot(page, "Y9-plan-approved");
	} finally {
		await page.close();
		writeFileSync(`${SHOTS}/Y9-attach-${RUN}.log`, out);
		note(`attach 输出 ${SHOTS}/Y9-attach-${RUN}.log`);
		await Promise.resolve(stdin.end()).catch(() => {});
		proc.kill();
		await proc.exited;
	}
	await waitTurn(tid, 0, 900_000).catch((e) =>
		note(`计划执行未在时限内结束：${String(e)}`),
	);
}

async function Y10() {
	note(
		"CLI：thread rpc get_state / new_session（托管拒绝）、abort 中止 sleep 60、fork --json",
	);
	const tid = await newOnOrb("只回复 ok", "Y10");
	const gs = await cli(`thread rpc ${tid} ${q('{"type":"get_state"}')}`);
	check(
		gs.code === 0 && gs.stdout.includes('"sessionId"'),
		`get_state → ${gs.code}，输出含 "sessionId"`,
	);
	const ns = await cli(`thread rpc ${tid} ${q('{"type":"new_session"}')}`);
	note(`new_session → ${ns.code} stderr：${ns.stderr.trim().slice(0, 200)}`);
	check(
		ns.code !== 0 && ns.stderr.includes("managed_by_oryn"),
		"new_session 非零退出且 stderr 含 managed_by_oryn",
	);
	const before = lastSeq(tid);
	const s = await cli(
		`thread send ${tid} ${q("用 bash 执行 sleep 60，然后只回复 done")}`,
	);
	check(s.code === 0, `thread send → ${s.code}`);
	await waitFor(
		"bash sleep 60 的工具调用入库",
		() =>
			(one<{ n: number }>(
				`select count(*) n from entries where thread_id='${tid}' and seq>${before} and type='message' and instr(cast(payload as text), '"toolCall"')>0 and instr(cast(payload as text), 'sleep 60')>0`,
			)?.n ?? 0) > 0,
		300_000,
		1000,
	);
	await sleep(3000);
	check(threadRow(tid)?.state === "working", "中止前线程在 working");
	const t0 = Date.now();
	const ab = await cli(`thread abort ${tid}`);
	check(ab.code === 0, `thread abort → ${ab.code} ${ab.out}`);
	await waitFor(
		"中止后 idle",
		() => threadRow(tid)?.state === "idle",
		15_000 - (Date.now() - t0),
		300,
	);
	check(true, `中止后 ${sec(Date.now() - t0)} 内 idle`);
	const last = await waitFor(
		"最后一条 assistant 入库",
		async () => {
			const m = await lastAssistantMsg(tid);
			return m.seq > before ? m : undefined;
		},
		15_000,
	);
	check(
		last.stopReason === "aborted",
		`最后一条 assistant stopReason=${last.stopReason}`,
	);
	const fk = await cli(`thread fork ${tid} --json`);
	check(
		fk.code === 0,
		`thread fork --json → ${fk.code} ${fk.code ? fk.out : ""}`,
	);
	const nid = (JSON.parse(fk.stdout) as ThreadView).id;
	const sh2 = await cli(`thread show ${nid}`);
	check(sh2.code === 0, `fork 出的 ${nid} 能被 thread show 查到`);
}

async function Y11() {
	note(
		"「能力」卡：折叠期间 WS 不发 get_skills；展开后技能 / MCP 行数与 thread rpc 一致；只读（除刷新外无控件）",
	);
	await ensureOrb();
	const tid = mainT;
	focus = tid;
	// 卡片只在 agent 可查询（agentStatus ready 且 orb ready，web/src/state/stores.ts 的 agentQueryable）时渲染；
	// Y8 冻结过 orb，resume 后 agent 不一定已就绪，先跑一回合
	await say(tid, "只回复 ok");
	const skills = await rpc(tid, { type: "get_skills" });
	const mcp = await rpc(tid, { type: "get_mcp_servers" });
	const sd = isRecord(skills.resp?.data) ? skills.resp.data : {};
	const md = isRecord(mcp.resp?.data) ? mcp.resp.data : {};
	const wantSkills = Array.isArray(sd.skills) ? sd.skills.length : -1;
	const wantMcp = Array.isArray(md.servers) ? md.servers.length : -1;
	check(
		wantSkills >= 0 && wantMcp >= 0,
		`thread rpc：skills ${wantSkills}，MCP ${wantMcp}`,
	);
	// 新上下文：卡片展开状态存在 localStorage，不能沿用别的场景的
	const ctx = await makeContext();
	const page = await ctx.newPage();
	const sent: string[] = [];
	page.on("websocket", (ws) =>
		ws.on("framesent", (f) => {
			sent.push(
				typeof f.payload === "string" ? f.payload : f.payload.toString(),
			);
		}),
	);
	try {
		await page.goto(`${BASE}/t/${tid}`);
		await openTab(page, "info");
		const card = cardSection(page, "能力");
		await card.waitFor({ timeout: 60_000 });
		const head = card.locator("header h3 button");
		check(
			(await head.getAttribute("aria-expanded")) === "false",
			"卡片默认折叠",
		);
		await sleep(8000);
		check(sent.length > 0, `已捕获 WS 发送帧 ${sent.length} 条`);
		check(
			!sent.some((s) => s.includes('"get_skills"')),
			"折叠期间 WS 发送帧里没有 get_skills",
		);
		await head.click();
		await waitFor(
			"展开后发出 get_skills",
			() => sent.some((s) => s.includes('"get_skills"')),
			30_000,
		);
		// 分组标题（i18n capabilities.skills / capabilities.mcp）→ 行数；Group 的标题 div 首个文本节点是标题，其后是 ul
		const groups = async () =>
			card.evaluate((el) => {
				const out = { skills: 0, mcp: 0, all: {} as Record<string, number> };
				for (const ul of el.querySelectorAll("ul")) {
					const t = ul.previousElementSibling?.firstChild?.textContent?.trim();
					if (!t) continue;
					const n = ul.querySelectorAll(":scope > li").length;
					out.all[t] = n;
					if (t === "技能") out.skills = n;
					if (t === "MCP 服务器") out.mcp = n;
				}
				return out;
			});
		const g = await waitFor(
			"技能 / MCP 行数对上",
			async () => {
				const x = await groups();
				return x.skills === wantSkills && x.mcp === wantMcp ? x : undefined;
			},
			30_000,
		).catch(groups);
		note(`卡片分组行数：${JSON.stringify(g.all)}`);
		check(g.skills === wantSkills, `技能行 ${g.skills} = ${wantSkills}`);
		check(g.mcp === wantMcp, `MCP 行 ${g.mcp} = ${wantMcp}`);
		const controls = await card.evaluate((el) => {
			const head = el.querySelector("header h3 button");
			return [
				...el.querySelectorAll(
					"button, input, select, textarea, [role=switch], [role=checkbox], [role=button]",
				),
			]
				.filter((x) => x !== head)
				.map(
					(x) =>
						x.getAttribute("aria-label") ??
						x.getAttribute("title") ??
						(x.textContent ?? "").trim(),
				);
		});
		note(`卡片内控件（不含标题折叠钮）：${controls.join(" | ") || "无"}`);
		check(
			controls.length === 1 && controls[0] === "刷新",
			"除「刷新」外没有任何开关或按钮",
		);
		await shot(page, "Y11-capabilities");
	} finally {
		await page.close();
		await ctx.close();
	}
}

async function Y12() {
	note("Web 输入 /clear：转录在原有消息之后出现「上下文已清空」分隔行");
	const marker = `m12y12-${RUN}`;
	const tid = await newOnOrb(`只回复 ${marker}`, "Y12");
	const before = lastSeq(tid);
	const page = await newPage();
	try {
		await page.goto(`${BASE}/t/${tid}`);
		const tr = page.locator("[data-testid=transcript]");
		await tr.getByText(marker).first().waitFor({ timeout: 60_000 });
		const box = page.locator('textarea[aria-label="消息"]');
		await box.waitFor({ timeout: 30_000 });
		await box.fill("/clear");
		await box.press("Escape");
		await box.press("Enter");
		const seq = await waitFor(
			"reset_boundary 条目入库",
			() =>
				one<{ seq: number }>(
					`select seq from entries where thread_id='${tid}' and seq>${before} and type='reset_boundary' order by seq limit 1`,
				)?.seq,
			60_000,
			1000,
		);
		note(`reset_boundary 条目 seq=${seq}`);
		await tr
			.getByText("上下文已清空", { exact: true })
			.waitFor({ timeout: 30_000 });
		const after = await tr.evaluate((root, m) => {
			const leaves = [...root.querySelectorAll("*")].filter(
				(e) => e.childElementCount === 0,
			);
			const meta = leaves.find(
				(e) => (e.textContent ?? "").trim() === "上下文已清空",
			);
			const prior = leaves
				.filter((e) => (e.textContent ?? "").includes(m))
				.at(-1);
			if (!meta || !prior) return false;
			return (
				(prior.compareDocumentPosition(meta) &
					Node.DOCUMENT_POSITION_FOLLOWING) !==
				0
			);
		}, marker);
		check(after, "「上下文已清空」出现在原有消息之后");
		await shot(page, "Y12-context-cleared");
	} finally {
		await page.close();
	}
}

/** 执行顺序：Y3 依赖 Y1 那个 omp 进程内的闩锁，Y2 要重启 runner，放在 Y4 之后。 */
const TESTS: [string, () => Promise<void>][] = [
	["Y1", Y1],
	["Y3", Y3],
	["Y4", Y4],
	["Y2", Y2],
	["Y5", Y5],
	["Y6", Y6],
	["Y7", Y7],
	["Y7b", Y7b],
	["Y8", Y8],
	["Y9", Y9],
	["Y10", Y10],
	["Y11", Y11],
	["Y12", Y12],
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
	if (fakeServer) {
		fakeServer.stop(true);
		fakeServer = undefined;
		const f = `${SHOTS}/fake-requests-${RUN}.json`;
		writeFileSync(f, JSON.stringify(fakeReqs, null, 2));
		cn(`停假端点；请求摘要 ${f}（${fakeReqs.length} 次）`);
	}
	try {
		if (modelsBackup !== undefined) {
			restoreModels();
			cn(`已还原 ${userModels()}`);
		}
	} catch (e) {
		cn(`还原 models.yml 失败：${String(e)}`);
	}
	await stopRunner().catch((e) => cn(`停 runner 失败：${String(e)}`));
	if (hostPrepared) sh(`pkill -KILL -u ${RUSER}`);
	if (runner) {
		const r = await api("DELETE", `/api/v1/runners/${runner.id}`).catch(
			(e) => ({ status: 0, text: String(e) }) as ApiRes,
		);
		cn(`删除 runner ${runner.name} → ${r.status}`);
		runner = undefined;
	}
	const left = [...orbs].filter(
		(o) => !["deleting", "deleted", "?"].includes(orbDbState(o)),
	);
	for (const o of left) {
		const r = await api("DELETE", `/api/v1/orbs/${o}`).catch(
			(e) => ({ status: 0, text: String(e) }) as ApiRes,
		);
		cn(`删除 orb ${o} → ${r.status}`);
	}
	await Promise.all(
		left.map((o) =>
			waitOrbDeleted(o, 180_000).catch((e) =>
				cn(`${o} 未删干净：${String(e)}`),
			),
		),
	);
	await browser?.close().catch(() => {});
}

for (const s of ["SIGINT", "SIGTERM"] as const)
	process.on(s, () => {
		console.log(`\n收到 ${s}，清理后退出`);
		void cleanup().finally(() => process.exit(130));
	});

// ---------------------------------------------------------------- 主流程

console.log(
	`M12 验收 run=${RUN} base=${BASE} runner 用户 ${RUSER} 项目 ${PROJECT} shots=${SHOTS}`,
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
