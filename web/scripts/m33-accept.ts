#!/usr/bin/env bun
/**
 * M33「视图」（原生生成式 UI）真机验收（docs/plan-m33.md「验证」的 T1–T11）：逐项打印 `M33 T<n> PASS|FAIL|SKIP` 与证据行，
 * 截图写进 $M33_SHOTS（缺省 <仓库>/.dev/m33/），最后汇总。没有 FAIL 时退出码 0；SKIP 逐条写明原因（未验证，不等于通过）。
 *
 * 用法（web/ 目录）：bun run accept:m33 [-- --only T1,T8] [-- --help]
 *
 * 环境（照 accept:m26 自建、互不干扰，全部在 /var/tmp/oryn-m33-<run>/ 下，结束时删除；M33_KEEP=1 保留）：
 *   - orynd：全新 data_dir，听 127.0.0.1:$M33_PORT 与 docker0 网关（orb 回连用），public_url = http://localhost:$M33_PORT，
 *     portal_domain 为空（HTML 视图帧走主域回退路径 `/_af/<令牌>/`）。Web 用 orynd 内嵌的构建（先 make web && make build：
 *     视图帧的运行时 sandbox/view.js、chart.js、tailwind.js 也是嵌进 orynd 的）。
 *   - 协调宿主（全局助手）：钉住的 Core（build/core/x64/omp）、本轮新建的目录 $M33_COORD_DIR/oryn-m33-coord-<run>，
 *     账号固定为 oryn-coord-<fnv32a(<父目录>/oryn-m33-coord)>（没有时 useradd 一次，之后复用）。
 *   - docker provider `local`：镜像是在 $M33_BASE_IMAGE 上只替换 orbd / oryn / omp 的薄层（oryn-orb.ts 扩展嵌在 orbd 里，
 *     所以工作线程拿到的是本次构建的视图工具），结束时删除容器与镜像。
 *   - 模型：$M33_MODELS（缺省 ~/.omp/agent/models.yml）原样写进全局配置，config.yml 的 default 角色与协调宿主都用 $M33_MODEL。
 *   - 浏览器：本机 Chromium（playwright-core）。主人页面用 `orynd admin create-web-session` 的会话 Cookie 登录（不用
 *     extraHTTPHeaders：视图帧会加载第三方 https 图片，令牌不能跟着出去）；T10 的「第二台设备」是另一个会话；分享页是匿名上下文。
 *
 * 驱动策略：真实模型 + 点名参数的提示词（「用这些参数调用 render_ui 一次」），断言只看帧、转录条目、REST 状态与页面 DOM，不看措辞。
 *   - 工作线程：第一条线程新建 orb，其余条目在同一个 orb 上各开新线程（转录短，不受虚拟滚动影响，也省掉 orb 冷启动）。
 *   - 视图在页面里按 `[data-view-call="<toolCallId>"]` 定位（render-view.tsx 的直出包装），表单控件按字段 label，按钮按文字，
 *     HTML 视图经 iframe 的 contentFrame 操作与取值。
 *   - T9：子 agent 的工具列表与系统提示词基线取自 orb 里子会话文件的 `session_init` 条目（Core 落盘的真实工具表，确定性）；
 *     `/tan` 经 WS `cmd{frame:{type:"tan"}}` 发起（与 Web 的 /tan 斜杠命令同一帧）。子 agent 的自述只作旁证。
 *   - T11：要求模型把 `blocks` 二次编码成字符串；转录里的原始参数仍是字符串才说明 Core 修复过，否则 SKIP。用 `await` 让调用停在
 *     「执行中」，此时页面只能靠 tool_execution_start 的已修复参数显示，作答后刷新再按 `details.view.spec` 显示，比较两次的文字。
 *   - T6：关开关后在同一 orb 上新开线程、新开助手对话；工作线程另核对 omp 进程环境里没有 ORYN_VIEW_TOOLS。结束时恢复开关。
 *
 * 可能的 SKIP：T5 的 https 图片在本机访问不了公网时只验 fetch 与父页面隔离（整条记 SKIP，写明原因）；T11 模型没有按要求二次编码。
 * 前置条目没通过时依赖它的条目判 FAIL（T7 要 T1、T4，T10 要 T1）。
 *
 * 环境变量：M33_BIN（缺省 <仓库>/bin）、M33_PORT（缺省 7733）、M33_COORD_EGRESS（缺省 7983）、M33_COORD_DIR（缺省 /var/tmp）、
 *   M33_BASE_IMAGE（缺省 oryn-orb:dev）、M33_MODELS、M33_MODEL（缺省 claude-pro/claude-sonnet-5-5）、M33_SHOTS、CHROMIUM、M33_KEEP、
 *   M33_HOLD=1（条目跑完后保留环境运行、打印登录信息，Ctrl-C 再收尾；排查用）。
 */
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
import type { Subprocess } from "bun";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Frame,
	type Locator,
	type Page,
} from "playwright-core";

// ---------------------------------------------------------------- 参数

const IDS = Array.from({ length: 11 }, (_, i) => `T${i + 1}`);
const USAGE = `用法：bun scripts/m33-accept.ts [--only T1,T2,…] [--help]
  环境变量见文件头注释。`;
if (process.argv.includes("--help") || process.argv.includes("-h")) {
	console.log(USAGE);
	process.exit(0);
}
const onlyAt = process.argv.indexOf("--only");
const ONLY =
	onlyAt >= 0
		? new Set(
				(process.argv[onlyAt + 1] ?? "")
					.split(",")
					.map((s) => s.trim().toUpperCase())
					.filter(Boolean),
			)
		: null;
if (ONLY && (ONLY.size === 0 || [...ONLY].some((k) => !IDS.includes(k)))) {
	console.error(`--only 只接受 T1…T11\n${USAGE}`);
	process.exit(2);
}

const ROOT = resolve(import.meta.dir, "../..");
const BIN = process.env.M33_BIN ?? `${ROOT}/bin`;
const OMP = `${ROOT}/build/core/x64/omp`;
const KEEP = process.env.M33_KEEP === "1";
const HOLD = process.env.M33_HOLD === "1";
const RUN = Math.random().toString(36).slice(2, 6);
const TMP = `/var/tmp/oryn-m33-${RUN}`;
const LOGS = `${TMP}/logs`;
const DB = `${TMP}/data/oryn.db`;
const PORT = Number(process.env.M33_PORT ?? 7733);
const API = `http://127.0.0.1:${PORT}`;
/** 浏览器与 public_url 用 localhost：`__Host-` 会话 Cookie 要求安全上下文，Chromium 把 http://localhost 视为安全。 */
const UI = `http://localhost:${PORT}`;
const COORD_EGRESS = Number(process.env.M33_COORD_EGRESS ?? 7983);
const COORD_PARENT = process.env.M33_COORD_DIR ?? "/var/tmp";
const COORD_DIR = `${COORD_PARENT}/oryn-m33-coord-${RUN}`;
const COORD_USER = `oryn-coord-${fnv32a(`${COORD_PARENT}/oryn-m33-coord`)}`;
const BASE_IMAGE = process.env.M33_BASE_IMAGE ?? "oryn-orb:dev";
const IMAGE = `oryn-orb:m33accept-${RUN}`;
const MODELS_SRC =
	process.env.M33_MODELS ??
	`${process.env.HOME ?? "/root"}/.omp/agent/models.yml`;
const MODEL = process.env.M33_MODEL ?? "claude-pro/claude-sonnet-5-5";
const SHOTS = process.env.M33_SHOTS ?? `${ROOT}/.dev/m33`;
const CHROMIUM = process.env.CHROMIUM ?? "/usr/bin/chromium";
mkdirSync(SHOTS, { recursive: true });

/** 一个回合（真实模型）的等待上限。 */
const TURN = 300_000;
/** 首条工作线程要先建 docker orb。 */
const ORB_TURN = 1_200_000;
/** 视图指引的标题（C11）：出现在系统提示词里即说明注入了指引。 */
const GUIDE_HEAD = "# Views (render_ui / render_html)";
/** 子 agent 自述用的探针任务（T9、T6）。 */
const PREFIX =
	"This is an automated acceptance test of Oryn views. Follow the instructions exactly; do not ask questions and do not call tools you are not told to call.\n\n";

function fnv32a(s: string): string {
	let h = 0x811c9dc5;
	for (const b of new TextEncoder().encode(s))
		h = Math.imul(h ^ b, 0x01000193) >>> 0;
	return h.toString(16).padStart(8, "0");
}

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();
const clip = (s: string, n = 240) =>
	s.length > n ? `${s.slice(0, n)}…（共 ${s.length} 字）` : s;
const isRecord = (v: unknown): v is Record<string, unknown> =>
	typeof v === "object" && v !== null && !Array.isArray(v);
const sec = (ms: number) => `${(ms / 1000).toFixed(0)}s`;
const nonce = () => Math.random().toString(36).slice(2, 8);

function sh(
	cmd: string,
	env?: Record<string, string | undefined>,
): { code: number; out: string; stdout: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], {
		cwd: ROOT,
		env: env ?? process.env,
	});
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
	intervalMs = 1_000,
): Promise<T> {
	const deadline = Date.now() + timeoutMs;
	let last: unknown;
	while (Date.now() < deadline) {
		try {
			const v = await fn();
			if (v) return v as T;
		} catch (e) {
			if (e instanceof Fatal) throw e;
			last = e;
		}
		await sleep(intervalMs);
	}
	throw new Error(
		`等待超时（${sec(timeoutMs)}）：${what}${last ? `；最后错误：${last instanceof Error ? last.message : String(last)}` : ""}`,
	);
}

// ---------------------------------------------------------------- 结果

const note = (s: string) => console.log(`    · ${s}`);
let softFails = 0;
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
class Skip extends Error {}
function skip(why: string): never {
	throw new Skip(why);
}
/** 等待中遇到不会自行恢复的状态（线程停在 error）：waitFor 不再重试，立即结束。 */
class Fatal extends Error {}

// ---------------------------------------------------------------- 后台进程

interface Bg {
	name: string;
	proc: Subprocess;
}
const bgs: Bg[] = [];

function bg(
	name: string,
	cmd: string[],
	env?: Record<string, string | undefined>,
): Bg {
	mkdirSync(LOGS, { recursive: true });
	const fd = openSync(`${LOGS}/${name}.log`, "a");
	const proc = Bun.spawn(cmd, {
		cwd: ROOT,
		env: env ?? process.env,
		stdin: "ignore",
		stdout: fd,
		stderr: fd,
	});
	const b = { name, proc };
	bgs.push(b);
	return b;
}

// ---------------------------------------------------------------- REST

let token = "";
let sessions: string[] = [];

async function http(
	method: string,
	url: string,
	body?: unknown,
	headers: Record<string, string> = {},
) {
	const raw = typeof body === "string";
	const res = await fetch(url, {
		method,
		headers: {
			...headers,
			...(body === undefined
				? {}
				: {
						"Content-Type": raw
							? "application/octet-stream"
							: "application/json",
					}),
		},
		body: body === undefined ? undefined : raw ? body : JSON.stringify(body),
		redirect: "manual",
	});
	const text = await res.text();
	let json: unknown = null;
	try {
		json = JSON.parse(text);
	} catch {
		// 非 JSON
	}
	return { status: res.status, text, json, headers: res.headers };
}
const api = (method: string, path: string, body?: unknown) =>
	http(method, `${API}${path}`, body, {
		Authorization: `Bearer ${token}`,
		"X-Oryn-CSRF": "1",
	});
async function rjson<T>(
	method: string,
	path: string,
	body?: unknown,
): Promise<T> {
	const r = await api(method, path, body);
	if (r.status >= 300)
		throw new Error(`${method} ${path} → ${r.status} ${clip(r.text)}`);
	return r.json as T;
}

/** 跑 oryn CLI（异步：等待期间事件循环照常）。 */
async function cli(args: string[]): Promise<{ code: number; out: string }> {
	const p = Bun.spawn([`${BIN}/oryn`, ...args], {
		cwd: ROOT,
		env: { ...process.env, ORYN_API_URL: API, ORYN_API_TOKEN: token },
		stdout: "pipe",
		stderr: "pipe",
	});
	const [out, err, code] = await Promise.all([
		new Response(p.stdout).text(),
		new Response(p.stderr).text(),
		p.exited,
	]);
	return { code, out: (out + err).trim() };
}

/** 经 WS 给线程发一条 omp RPC 命令（与 Web 的 realtime.cmd 同一帧），返回应答。 */
async function wsCmd(
	tid: string,
	frame: Record<string, unknown>,
	timeoutMs = 60_000,
): Promise<unknown> {
	const { ticket } = await rjson<{ ticket: string }>(
		"POST",
		"/api/v1/ws-ticket",
	);
	const ws = new WebSocket(
		`${API.replace(/^http/, "ws")}/api/v1/ws?ticket=${encodeURIComponent(ticket)}`,
	);
	const id = `m33-${nonce()}`;
	const { promise, resolve, reject } = Promise.withResolvers<unknown>();
	const timer = setTimeout(
		() => reject(new Error(`WS cmd ${String(frame.type)} 超时`)),
		timeoutMs,
	);
	ws.onopen = () =>
		ws.send(JSON.stringify({ type: "cmd", threadId: tid, id, frame }));
	ws.onerror = () => reject(new Error("WS 连接失败"));
	ws.onmessage = (ev) => {
		let m: unknown;
		try {
			m = JSON.parse(String(ev.data));
		} catch {
			return;
		}
		if (!isRecord(m)) return;
		if (m.type === "res" && m.id === id) resolve(m.response);
		else if (m.type === "error" && (m.id === id || m.id === undefined))
			reject(new Error(`WS error：${clip(JSON.stringify(m))}`));
	};
	try {
		return await promise;
	} finally {
		clearTimeout(timer);
		ws.close();
	}
}

/** 线程主会话当前激活的顶层工具名（omp `get_active_tools`，与 Web「当前工具」面板同源）。 */
async function activeTools(tid: string): Promise<string[]> {
	const r = await wsCmd(tid, { type: "get_active_tools" });
	const tools = isRecord(r) && isRecord(r.data) ? r.data.tools : undefined;
	if (!Array.isArray(tools))
		throw new Error(
			`get_active_tools 应答没有 tools：${clip(JSON.stringify(r))}`,
		);
	return tools
		.filter((t) => isRecord(t) && t.mounted !== true)
		.map((t) => (isRecord(t) ? String(t.name) : ""));
}

// ---------------------------------------------------------------- 线程与转录

interface ThreadView {
	id: string;
	state: string;
	orbId?: string;
}
interface Entry {
	seq: number;
	type: string;
	payload: Record<string, unknown>;
}
interface Call {
	id: string;
	name: string;
	args: Record<string, unknown>;
	seq: number;
}
interface Result {
	text: string;
	isError: boolean;
	view: Record<string, unknown> | undefined;
	seq: number;
}

const getThread = (id: string) =>
	rjson<ThreadView>("GET", `/api/v1/threads/${id}`);

async function entries(tid: string): Promise<Entry[]> {
	const out: Entry[] = [];
	let cur = 0;
	for (let i = 0; i < 100; i++) {
		const p = await rjson<{ entries: Entry[]; hasMore: boolean }>(
			"GET",
			`/api/v1/threads/${tid}/entries?after=${cur}&limit=500`,
		);
		out.push(...p.entries);
		const last = p.entries.at(-1);
		if (!p.hasMore || !last) break;
		cur = last.seq;
	}
	return out;
}

function textOf(c: unknown): string {
	if (typeof c === "string") return c;
	if (!Array.isArray(c)) return "";
	return c
		.map((x) =>
			isRecord(x) && x.type === "text" && typeof x.text === "string"
				? x.text
				: "",
		)
		.join("");
}
const msgOf = (e: Entry) =>
	e.type === "message" && isRecord(e.payload.message)
		? e.payload.message
		: undefined;
const roleOf = (e: Entry) => String(msgOf(e)?.role ?? "");
const userText = (e: Entry) =>
	roleOf(e) === "user" ? textOf(msgOf(e)?.content) : undefined;
const assistantText = (e: Entry) =>
	roleOf(e) === "assistant" ? textOf(msgOf(e)?.content) : undefined;

function callsOf(es: Entry[]): Call[] {
	const out: Call[] = [];
	for (const e of es) {
		const m = msgOf(e);
		if (m?.role !== "assistant" || !Array.isArray(m.content)) continue;
		for (const b of m.content)
			if (isRecord(b) && b.type === "toolCall" && typeof b.id === "string")
				out.push({
					id: b.id,
					name: String(b.name ?? ""),
					args: isRecord(b.arguments) ? b.arguments : {},
					seq: e.seq,
				});
	}
	return out;
}

function resultIn(es: Entry[], callId: string): Result | undefined {
	for (const e of es) {
		const m = msgOf(e);
		if (m?.role !== "toolResult" || m.toolCallId !== callId) continue;
		const d = isRecord(m.details) ? m.details : undefined;
		return {
			text: textOf(m.content),
			isError: m.isError === true,
			view: d && isRecord(d.view) ? d.view : undefined,
			seq: e.seq,
		};
	}
	return undefined;
}

/** 等转录里出现名为 name、参数（JSON）含 marker 的工具调用。 */
function waitCall(tid: string, name: string, marker: string, timeoutMs = TURN) {
	return waitFor(
		`线程 ${tid} 出现 ${name}（含 ${marker}）`,
		async () => {
			await failOnError(tid);
			return callsOf(await entries(tid)).find(
				(c) => c.name === name && JSON.stringify(c.args).includes(marker),
			);
		},
		timeoutMs,
		1_500,
	);
}

function waitResult(tid: string, callId: string, timeoutMs = TURN) {
	return waitFor(
		`调用 ${callId} 的工具结果`,
		async () => {
			const es = await entries(tid);
			const r = resultIn(es, callId);
			if (!r) await failOnError(tid);
			return r;
		},
		timeoutMs,
		1_000,
	);
}

/** 等 afterSeq 之后有满足 pred 的助手文字。 */
function waitReply(
	tid: string,
	what: string,
	pred: (s: string) => boolean,
	afterSeq: number,
	timeoutMs = TURN,
) {
	return waitFor(
		`线程 ${tid} 的助手回复：${what}`,
		async () => {
			const hit = (await entries(tid)).find((e) => {
				const s = assistantText(e);
				return e.seq > afterSeq && s !== undefined && pred(s);
			});
			if (!hit) await failOnError(tid);
			return hit;
		},
		timeoutMs,
		1_500,
	);
}

/** 线程停在 error 时抛 Fatal（带最后一条助手消息的错误，如上游拒绝），等待随即结束；否则返回线程。 */
async function failOnError(tid: string): Promise<ThreadView> {
	const t = await getThread(tid);
	if (t.state !== "error") return t;
	const err = (await entries(tid))
		.map(msgOf)
		.findLast(
			(m) => m?.role === "assistant" && m.errorMessage !== undefined,
		)?.errorMessage;
	throw new Fatal(
		`线程 ${tid} 停在 error${err === undefined ? "" : `：${clip(oneLine(String(err)), 300)}`}`,
	);
}

function waitUser(
	tid: string,
	what: string,
	pred: (s: string) => boolean,
	afterSeq = 0,
	timeoutMs = 60_000,
) {
	return waitFor(
		`线程 ${tid} 的用户消息：${what}`,
		async () =>
			(await entries(tid)).find((e) => {
				const s = userText(e);
				return e.seq > afterSeq && s !== undefined && pred(s);
			}),
		timeoutMs,
		1_000,
	);
}

const SETTLED: Record<string, true> = { idle: true, awaiting_input: true };
function waitState(tid: string, want: (s: string) => boolean, what: string) {
	return waitFor(
		`线程 ${tid} ${what}`,
		async () => {
			const t = await failOnError(tid);
			return want(t.state) ? t : null;
		},
		TURN,
		1_000,
	);
}
const waitIdle = (tid: string) =>
	waitState(tid, (s) => s === "idle", "回到 idle");

const lastSeq = async (tid: string) => (await entries(tid)).at(-1)?.seq ?? 0;

async function say(tid: string, msg: string) {
	const r = await cli(["thread", "send", tid, msg]);
	if (r.code !== 0)
		throw new Error(`oryn thread send：${clip(oneLine(r.out))}`);
}

const receiptHead = (callId: string, block: string, choice?: number) =>
	`<oryn-view-answer call="${callId}" block="${block}"${choice === undefined ? "" : ` choice="${choice}"`}>`;

const callArgs = (tool: string, args: unknown) =>
	`Call the \`${tool}\` tool exactly once, with exactly these arguments (pass them verbatim):\n\`\`\`json\n${JSON.stringify(args, null, 1)}\n\`\`\`\n`;

// ---- 工作线程：第一条建 orb，其余在同一 orb 上开新线程

let projectId = "";
let orbId = "";
async function newThread(prompt: string, title: string): Promise<string> {
	projectId ||= (
		await rjson<{ id: string }>("POST", "/api/v1/projects", {
			name: `m33-${RUN}`,
			shallowClone: true,
		})
	).id;
	const r = await rjson<{ thread: ThreadView }>("POST", "/api/v1/threads", {
		projectId,
		prompt,
		title: `m33 ${title}`,
		executor: orbId
			? { kind: "orb", orbId }
			: { kind: "new_orb", provider: "local" },
	});
	note(
		`工作线程 ${r.thread.id}（${title}${orbId ? `，orb ${orbId}` : "，新 orb"}）`,
	);
	if (!orbId) {
		const t = await waitFor(
			"新线程分到 orb",
			async () => (await getThread(r.thread.id)).orbId,
			120_000,
		);
		orbId = t;
	}
	return r.thread.id;
}

/** orb 容器里以 user 身份跑命令。 */
const dexec = (cmd: string) => sh(`docker exec ${orbId} bash -lc ${q(cmd)}`);

// ---------------------------------------------------------------- 浏览器

let browser: Browser | undefined;
const contexts: BrowserContext[] = [];
const pageErrors: string[] = [];

async function openPage(
	path: string,
	o: { session?: string; anon?: boolean; width?: number } = {},
): Promise<Page> {
	browser ??= await chromium.launch({
		executablePath: CHROMIUM,
		headless: true,
		args: ["--no-sandbox", "--use-gl=angle", "--use-angle=swiftshader"],
	});
	const ctx = await browser.newContext({
		locale: "zh-CN",
		viewport: { width: o.width ?? 1280, height: 900 },
		colorScheme: "light",
	});
	if (!o.anon)
		await ctx.addCookies([
			{
				name: "__Host-oryn_session",
				value: o.session ?? sessions[0] ?? "",
				domain: "localhost",
				path: "/",
				secure: true,
				httpOnly: true,
				sameSite: "Lax",
			},
		]);
	// 帧事件日志（失败时打印）：iframe 的插入 / 移除与帧发给父页面的消息
	await ctx.addInitScript(FRAME_LOG);
	contexts.push(ctx);
	const p = await ctx.newPage();
	p.on("pageerror", (e) => {
		pageErrors.push(oneLine(e.message).slice(0, 300));
		note(`[pageerror] ${oneLine(e.message).slice(0, 200)}`);
	});
	await p.goto(`${UI}${path}`, { waitUntil: "domcontentloaded" });
	return p;
}

async function closePages() {
	for (const c of contexts.splice(0)) await c.close().catch(() => undefined);
}

/** 页面里记下的帧事件：`[毫秒, 事件, 详情]`。 */
const FRAME_LOG = `(() => {
	if (window.top !== window) return;
	const t0 = performance.now();
	const log = (window.__m33 = []);
	const at = () => Math.round(performance.now() - t0);
	const tail = (f) => f.src.split("#")[0].slice(-28);
	addEventListener("message", (e) => {
		const d = e.data;
		if (d && d.v === 1 && typeof d.kind === "string") log.push([at(), "msg", d.kind]);
	}, true);
	new MutationObserver((ms) => {
		for (const m of ms) {
			for (const n of m.addedNodes)
				if (n instanceof Element) for (const f of n.matches("iframe") ? [n] : n.querySelectorAll("iframe")) log.push([at(), "iframe+", tail(f)]);
			for (const n of m.removedNodes)
				if (n instanceof Element) for (const f of n.matches("iframe") ? [n] : n.querySelectorAll("iframe")) log.push([at(), "iframe-", tail(f)]);
		}
	}).observe(document, { subtree: true, childList: true });
})();`;

async function dumpFrameLog(p: Page) {
	const log = await p
		.evaluate(() => ("__m33" in window ? window.__m33 : null))
		.catch(() => null);
	if (Array.isArray(log) && log.length)
		note(`帧事件：${clip(JSON.stringify(log), 1200)}`);
}

async function shot(p: Page, name: string) {
	const f = `${SHOTS}/${name}.png`;
	await p.screenshot({ path: f }).catch(() => undefined);
	note(`截图 ${f}`);
}

/** 转录里这次调用的视图（直出包装）。 */
const viewOf = (p: Page, callId: string): Locator =>
	p.locator(`[data-view-call="${callId}"]`);

/**
 * 转录里这次调用的视图。转录是虚拟列表，目标离视口远时不在 DOM 里：5 秒还没出现就在转录上方用滚轮往上翻再找
 * （页面从底部打开，较早的视图都在上方）。
 */
async function waitView(p: Page, callId: string, kind: "ui" | "html") {
	const v = p.locator(
		`[data-view-call="${callId}"][data-testid="view-${kind}"]`,
	);
	const t0 = Date.now();
	while (Date.now() - t0 < 90_000) {
		if (await v.count()) {
			// 虚拟列表滚动中元素可能刚好被卸下：本轮落空就继续找
			const shown = await v
				.scrollIntoViewIfNeeded({ timeout: 5_000 })
				.then(() => v.waitFor({ state: "visible", timeout: 10_000 }))
				.then(
					() => true,
					() => false,
				);
			if (shown) return v;
		}
		if (Date.now() - t0 > 5_000) {
			const box = await p
				.getByLabel("消息", { exact: true })
				.boundingBox()
				.catch(() => null);
			await p.mouse.move(
				box ? box.x + box.width / 2 : 640,
				box ? Math.max(120, box.y - 250) : 400,
			);
			await p.mouse.wheel(0, -600);
		}
		await sleep(500);
	}
	throw new Error(`90 秒内页面里没有出现视图 ${callId}（${kind}）`);
}

/**
 * HTML 视图的帧：等父页面与帧握手完成（HtmlView 收到 `ready`，帧容器去掉骨架动画），再取当时的 iframe。
 * 首帧 8 秒没握手时 HtmlView 会换一个新帧重开（C9 的兜底）；那说明首帧的握手丢了，记一条软失败。
 */
async function viewFrame(p: Page, callId: string): Promise<Frame> {
	const iframe = viewOf(p, callId).locator("iframe");
	const first = await iframe.getAttribute("src", { timeout: 60_000 });
	await viewOf(p, callId)
		.locator("div:has(> iframe):not(.animate-pulse)")
		.waitFor({ timeout: 30_000 });
	const src = await iframe.getAttribute("src");
	expect(
		src === first,
		src === first
			? "首帧即握手成功（收到 ready）"
			: "首帧没有握手成功，8 秒后 HtmlView 换了新帧才就绪",
	);
	const el = await iframe.elementHandle();
	const f = await el?.contentFrame();
	if (!f) throw new Error("iframe 没有 contentFrame");
	await f.waitForFunction(
		() =>
			"oryn" in window &&
			typeof window.oryn === "object" &&
			window.oryn !== null &&
			"sendPrompt" in window.oryn &&
			typeof window.oryn.sendPrompt === "function",
		undefined,
		{ timeout: 30_000 },
	);
	return f;
}

/** 帧内的 click 计数（捕获阶段）：确认一次点击真的落进了帧里。 */
const CLICK_PROBE = `(() => {
	if (window.__m33Clicks === undefined) {
		window.__m33Clicks = 0;
		addEventListener("click", () => { window.__m33Clicks++; }, true);
	}
	return window.__m33Clicks;
})()`;

/**
 * 点 HTML 视图帧里的元素。转录在回合里会随内容增长滚动（贴底），Playwright 对跨进程帧算好坐标后帧可能已经移动，
 * 鼠标点击落空：先把帧滚到视口中间、稍等布局稳定再点，点完用帧内的 click 计数确认落点。两次都落空时记下落点处
 * 父页面的元素作诊断，改为在帧里派发 DOM click（页面的 onclick 照常执行；D16 不要求用户手势，回传链路不变）。
 * 落空说明页面根本没收到点击，补派不会掩盖「收到了却没回传」的问题。
 */
async function clickInFrame(p: Page, callId: string, f: Frame, sel: string) {
	const target = f.locator(sel);
	for (let i = 0; i < 2; i++) {
		await viewOf(p, callId)
			.locator("iframe")
			.evaluate((el) => el.scrollIntoView({ block: "center" }));
		await sleep(500);
		const before = Number(await f.evaluate(CLICK_PROBE));
		const box = await target.boundingBox();
		await target.click();
		await sleep(300);
		const after = Number(await f.evaluate(CLICK_PROBE));
		if (after > before) return;
		const x = box ? box.x + box.width / 2 : 0;
		const y = box ? box.y + box.height / 2 : 0;
		const hit = await p.evaluate(
			([px, py]) => {
				const e = document.elementFromPoint(px ?? 0, py ?? 0);
				return e
					? `${e.tagName.toLowerCase()}.${String(e.className).slice(0, 60)}`
					: "无";
			},
			[x, y],
		);
		const frameBox = await viewOf(p, callId).locator("iframe").boundingBox();
		note(
			`第 ${i + 1} 次鼠标点击没有落进帧里（帧内 click ${before}→${after}；目标中心 ${Math.round(x)},${Math.round(y)}，此处父页面元素 ${hit}，帧 ${JSON.stringify(frameBox)}）`,
		);
	}
	await target.dispatchEvent("click");
	const n = Number(await f.evaluate(CLICK_PROBE));
	note(`改为在帧里派发 DOM click（帧内 click 计数 ${n}）`);
}

// ---------------------------------------------------------------- 环境

let envState: "todo" | "ok" | "failed" = "todo";
let envWhy = "";
let gateway = "";
let imageBuilt = false;

async function setupEnv() {
	if (process.getuid?.() !== 0)
		throw new Error("要以 root 运行（协调宿主降权、docker）");
	for (const b of ["orynd", "orbd", "oryn"])
		if (!existsSync(`${BIN}/${b}`))
			throw new Error(
				`缺少 ${BIN}/${b}：先 make web && make build（或设 M33_BIN）`,
			);
	for (const tool of ["docker", "sqlite3"])
		if (sh(`command -v ${tool}`).code !== 0)
			throw new Error(`本机缺少 ${tool}`);
	if (!existsSync(CHROMIUM)) throw new Error(`缺少 ${CHROMIUM}`);
	if (!existsSync(MODELS_SRC))
		throw new Error(`缺少 ${MODELS_SRC}（设 M33_MODELS）`);
	const want = JSON.parse(readFileSync(`${ROOT}/core/source.json`, "utf8")) as {
		tag: string;
		sha256: Record<string, string>;
	};
	const got = sh(`sha256sum ${q(OMP)} | cut -d' ' -f1`).stdout;
	check(
		got === want.sha256["linux-x64"],
		`build/core/x64/omp 是钉住的 Core ${want.tag}（不一致先跑 scripts/fetch-core.sh）`,
	);
	check(
		sh(`ss -Hltn 'sport = :${PORT}'`).stdout === "",
		`端口 ${PORT} 空闲（设 M33_PORT 换端口）`,
	);
	check(
		sh(`docker image inspect ${q(BASE_IMAGE)} >/dev/null 2>&1`).code === 0,
		`基础镜像 ${BASE_IMAGE} 存在（先 make image 或设 M33_BASE_IMAGE）`,
	);
	gateway = sh(
		"docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}'",
	).stdout;
	check(/^\d+\.\d+\.\d+\.\d+$/.test(gateway), `docker0 网关地址 ${gateway}`);
	check(!existsSync(COORD_DIR), `协调宿主目录 ${COORD_DIR} 不存在（本轮独占）`);
	mkdirSync(COORD_DIR, { recursive: true });
	if (sh(`id -u ${q(COORD_USER)}`).code !== 0) {
		const add = sh(
			`useradd --system --no-create-home --home-dir /nonexistent --shell "$(command -v nologin || echo /usr/sbin/nologin)" --user-group ${q(COORD_USER)}`,
		);
		check(add.code === 0, `创建协调宿主账号 ${COORD_USER}：${clip(add.out)}`);
	}
	mkdirSync(LOGS, { recursive: true });

	// orb 镜像：基础镜像 + 本次的 omp / orbd / oryn
	mkdirSync(`${TMP}/image`, { recursive: true });
	for (const [src, name] of [
		[OMP, "omp"],
		[`${BIN}/orbd`, "orbd"],
		[`${BIN}/oryn`, "oryn"],
	] as const)
		copyFileSync(src, `${TMP}/image/${name}`);
	writeFileSync(
		`${TMP}/image/Dockerfile`,
		`FROM ${BASE_IMAGE}
USER root
COPY --chmod=0755 omp orbd oryn /usr/local/bin/
USER user
RUN omp --version && omp --smoke-test && rm -rf /home/user/.omp/logs /home/user/.omp/run
`,
	);
	const t0 = Date.now();
	const built = sh(
		`docker build -q -t ${IMAGE} ${q(`${TMP}/image`)} > ${q(`${LOGS}/image.log`)} 2>&1`,
	);
	imageBuilt = built.code === 0;
	check(
		imageBuilt,
		`构建 orb 镜像 ${IMAGE}（基于 ${BASE_IMAGE}，${sec(Date.now() - t0)}）${built.code ? `：${clip(readFileSync(`${LOGS}/image.log`, "utf8").slice(-400))}` : ""}`,
	);
	rmSync(`${TMP}/image`, { recursive: true, force: true });

	const ynd = `${TMP}/orynd.yaml`;
	writeFileSync(
		ynd,
		`listen: ["127.0.0.1:${PORT}", "${gateway}:${PORT}"]
public_url: "${UI}"
portal_domain: ""
data_dir: "${TMP}/data"
log_level: "debug"
idle: { agent_idle: "30m", user_idle: "30m", stop_after_frozen: "24h" }
orb_defaults: { provider: "local", type: "" }
coordinator: { omp_path: "${OMP}", dir: "${COORD_DIR}", user: "${COORD_USER}", egress_port: ${COORD_EGRESS}, model: "${MODEL}" }
`,
	);
	bg("orynd", [`${BIN}/orynd`, "--config", ynd]);
	await waitFor(
		"orynd 就绪",
		async () => (await fetch(`${API}/healthz`)).ok,
		30_000,
		300,
	);
	const admin = (a: string) =>
		sh(`${q(`${BIN}/orynd`)} --config ${q(ynd)} admin ${a}`);
	const dev = admin("create-device --name m33-accept --kind cli");
	check(
		dev.code === 0,
		`orynd admin create-device${dev.code ? `：${clip(dev.out)}` : ""}`,
	);
	token = dev.stdout.split("\n").at(-1) ?? "";
	sessions = [1, 2].map((i) => {
		const s = admin(`create-web-session --user-agent m33-accept-${i}`);
		check(
			s.code === 0,
			`orynd admin create-web-session #${i}${s.code ? `：${clip(s.out)}` : ""}`,
		);
		return s.stdout.split("\n").at(-1) ?? "";
	});
	// 全新库里补一个 owner 行（正式部署由首次 Passkey 注册创建；生成式 UI 开关存在这一行上）
	const own = sh(
		`sqlite3 ${q(DB)} ${q(`INSERT OR IGNORE INTO owner (id, display_name, user_handle, created_at) VALUES (1, 'm33', randomblob(16), ${Date.now()})`)}`,
	);
	check(own.code === 0, `写 owner 行${own.code ? `：${clip(own.out)}` : ""}`);

	const prov = await rjson<{ token: string }>("POST", "/api/v1/providers", {
		name: "local",
	});
	writeFileSync(`${TMP}/provider.token`, `${prov.token}\n`, { mode: 0o600 });
	writeFileSync(
		`${TMP}/provider.yaml`,
		`url: "${API}"
token_file: "${TMP}/provider.token"
orb_url: "http://${gateway}:${PORT}"
driver: docker
docker:
  image: "${IMAGE}"
`,
	);
	bg("provider", [
		`${BIN}/oryn`,
		"provider",
		"run",
		"--config",
		`${TMP}/provider.yaml`,
	]);
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
	const put = await api(
		"PUT",
		"/api/v1/agent-config/files/models.yml?mode=384",
		readFileSync(MODELS_SRC, "utf8"),
	);
	check(
		put.status === 200,
		`全局配置 models.yml ← ${MODELS_SRC}（${put.status}）`,
	);
	const cfg = await api(
		"PUT",
		"/api/v1/agent-config/files/config.yml",
		`modelRoles:\n  default: "${MODEL}"\n`,
	);
	check(
		cfg.status === 200,
		`全局配置 config.yml：default=${MODEL}（${cfg.status}）`,
	);
	await api("GET", "/api/v1/models/catalog?refresh=true");
	await waitFor(
		`中央模型目录含 ${MODEL}`,
		async () =>
			(
				await rjson<{ models: { provider: string; id: string }[] }>(
					"GET",
					"/api/v1/models/catalog",
				)
			).models.some((m) => `${m.provider}/${m.id}` === MODEL),
		300_000,
		3_000,
	);
	const gui = await rjson<{ enabled: boolean }>(
		"GET",
		"/api/v1/settings/generative-ui",
	);
	check(gui.enabled, "生成式 UI 开关缺省开启");
	note(
		`orynd ${API}（public_url ${UI}，另听 ${gateway}:${PORT}），provider local（docker，${IMAGE}），模型 ${MODEL}`,
	);
}

let coordReady: boolean | undefined;
async function ensureCoordinator() {
	if (coordReady !== undefined) {
		check(coordReady, "协调宿主可用");
		return;
	}
	let reason = "";
	coordReady = await waitFor(
		"协调宿主可用",
		async () => {
			const c = await rjson<{ available: boolean; reason?: string }>(
				"GET",
				"/api/v1/coordinator",
			);
			if (!c.available && c.reason) reason = c.reason;
			return c.available;
		},
		180_000,
		1_000,
	).catch(() => false);
	check(coordReady, `协调宿主可用${reason ? `（${reason}）` : ""}`);
}

// ---------------------------------------------------------------- 用例

/** 各条之间共享的结果（前置条目写入）。 */
const shared: {
	t1?: { tid: string; callId: string; city: string };
	t4?: { tid: string; callId: string };
	t8?: { tid: string; callId: string };
} = {};

/** T1：工作线程 render_ui 出表单，提交后 agent 收到回执并继续；刷新后表单仍是已提交。 */
async function T1() {
	const n = nonce();
	const city = `Hangzhou-${n}`;
	const args = {
		title: `T1 ${n}`,
		blocks: [
			{
				type: "form",
				title: "T1 form",
				fields: [
					{ name: "city", label: "City", kind: "text", required: true },
					{ name: "note", label: "Note", kind: "text" },
				],
				submitLabel: "Send T1",
			},
		],
	};
	const tid = await newThread(
		`${PREFIX}${callArgs("render_ui", args)}Do not set \`await\`. After the call, end your turn immediately without writing anything. Later, when my form answer arrives, reply with exactly one line: \`T1-ACK city=<the city value>\`.`,
		"T1",
	);
	const call = await waitCall(tid, "render_ui", `T1 ${n}`, ORB_TURN);
	note(`toolCall ${call.id}`);
	const res = await waitResult(tid, call.id);
	check(!res.isError, `工具结果非错误：${clip(oneLine(res.text), 160)}`);
	check(
		res.view?.outcome === "rendered" &&
			res.text.startsWith("Rendered. The user's input will arrive"),
		"结果是 C3 的「有交互块、非 await」",
	);
	const spec = isRecord(res.view?.spec) ? res.view.spec : undefined;
	check(
		Array.isArray(spec?.blocks) &&
			spec.blocks.length === 1 &&
			!("await" in spec),
		"details.view.spec 是执行时的参数（1 个块，不含 await）",
	);
	await waitIdle(tid);

	const p = await openPage(`/t/${tid}`);
	const v = await waitView(p, call.id, "ui");
	const submit = v.getByRole("button", { name: "Send T1" });
	check(await submit.isEnabled(), "调用已结束：表单可提交");
	await v.getByLabel("City").fill(city);
	await shot(p, "T1-form");
	const before = await lastSeq(tid);
	await submit.click();
	await v.getByRole("status").filter({ hasText: "已提交" }).waitFor({
		timeout: 30_000,
	});
	check(true, "提交后表单锁定为「已提交」");
	const u = await waitUser(
		tid,
		"视图回执",
		(s) => s.startsWith(receiptHead(call.id, "0")),
		before,
	);
	const body = userText(u) ?? "";
	check(
		body.includes(`- City: ${city}`) &&
			body.includes(`\`\`\`json\n${JSON.stringify({ city })}`.slice(0, -1)),
		`回执正文含字段行与 JSON 围栏：${clip(oneLine(body), 200)}`,
	);
	const reply = await waitReply(
		tid,
		"T1-ACK",
		(s) => s.includes("T1-ACK") && s.includes(city),
		u.seq,
	);
	check(
		true,
		`agent 收到回执后继续：${clip(oneLine(assistantText(reply) ?? ""), 120)}`,
	);
	await waitIdle(tid);
	await shot(p, "T1-submitted");

	await p.reload({ waitUntil: "domcontentloaded" });
	const v2 = await waitView(p, call.id, "ui");
	await v2.getByRole("status").filter({ hasText: "已提交" }).waitFor({
		timeout: 30_000,
	});
	const input = v2.getByLabel("City");
	check(
		(await input.inputValue()) === city && (await input.isDisabled()),
		`刷新后仍是已提交：City=${await input.inputValue()}，控件禁用`,
	);
	check(
		(await v2.getByRole("button", { name: "Send T1" }).count()) === 0,
		"刷新后没有提交按钮",
	);
	await shot(p, "T1-reloaded");
	shared.t1 = { tid, callId: call.id, city };
}

/** T2：await（render_ui 与 render_html 各一次）：挂起期间视图可操作、线程 awaiting_input、没有工具结果；作答后同一回合继续。 */
async function T2() {
	const n = nonce();
	const code = `C${n}`;
	const uiArgs = {
		title: `T2 ui ${n}`,
		await: true,
		blocks: [
			{
				type: "form",
				fields: [{ name: "code", label: "Code", kind: "text", required: true }],
				submitLabel: "Send T2",
			},
		],
	};
	const htmlText = `T2H-${n}`;
	const htmlArgs = {
		title: `T2 html ${n}`,
		await: true,
		height: 200,
		html: `<button id="go" style="font-size:18px;padding:8px 16px" onclick="window.oryn.sendPrompt('${htmlText}')">Go T2</button>`,
	};
	const tid = await newThread(
		`${PREFIX}Step 1: ${callArgs("render_ui", uiArgs)}Step 2: after it returns, ${callArgs("render_html", htmlArgs)}Step 3: after that returns too, reply with exactly one line: \`T2-ACK <the code value from step 1> <the text returned by step 2>\`.`,
		"T2",
	);
	const p = await openPage(`/t/${tid}`);

	// ---- render_ui
	const ui = await waitCall(tid, "render_ui", `T2 ui ${n}`, ORB_TURN);
	await waitState(tid, (s) => s === "awaiting_input", "进入 awaiting_input");
	check(true, "render_ui await：线程 awaiting_input");
	const v = await waitView(p, ui.id, "ui");
	await v.getByTestId("view-awaiting").waitFor({ timeout: 30_000 });
	const submit = v.getByRole("button", { name: "Send T2" });
	check(
		await submit.isEnabled(),
		"挂起期间表单可提交，显示「agent 正在等你…」",
	);
	check(
		resultIn(await entries(tid), ui.id) === undefined,
		"此时还没有工具结果",
	);
	await shot(p, "T2-ui-awaiting");
	await v.getByLabel("Code").fill(code);
	await submit.click();
	const r1 = await waitResult(tid, ui.id, 60_000);
	check(
		r1.view?.outcome === "answered" &&
			r1.text.startsWith("The user answered in the view:\n") &&
			r1.text.includes(code),
		`提交后出现工具结果 answered，含作答：${clip(oneLine(r1.text), 160)}`,
	);
	const ans = isRecord(r1.view?.answer) ? r1.view.answer : undefined;
	check(
		ans?.kind === "form" &&
			ans.block === "0" &&
			isRecord(ans.values) &&
			ans.values.code === code,
		`details.view.answer = ${clip(JSON.stringify(ans), 160)}`,
	);

	// ---- render_html
	const html = await waitCall(tid, "render_html", `T2 html ${n}`);
	await waitState(
		tid,
		(s) => s === "awaiting_input",
		"再次进入 awaiting_input",
	);
	const f = await viewFrame(p, html.id);
	check(true, "render_html await：帧已挂载、运行时就绪");
	check(
		resultIn(await entries(tid), html.id) === undefined,
		"此时还没有工具结果",
	);
	await shot(p, "T2-html-awaiting");
	await clickInFrame(p, html.id, f, "#go");
	const r2 = await waitResult(tid, html.id, 60_000);
	check(
		r2.view?.outcome === "answered" && r2.text.includes(htmlText),
		`点击帧内按钮后出现工具结果 answered：${clip(oneLine(r2.text), 160)}`,
	);
	const a2 = isRecord(r2.view?.answer) ? r2.view.answer : undefined;
	check(
		a2?.kind === "prompt" && a2.block === "html",
		`details.view.answer = ${JSON.stringify(a2)}`,
	);

	// ---- 同一回合
	const reply = await waitReply(
		tid,
		"T2-ACK",
		(s) => s.includes("T2-ACK") && s.includes(code),
		r2.seq,
	);
	const es = await entries(tid);
	const users = es.filter(
		(e) => userText(e) !== undefined && e.seq > ui.seq && e.seq < reply.seq,
	);
	check(
		users.length === 0,
		`从第一次调用到 T2-ACK 之间没有用户消息（同一回合，${users.length} 条）`,
	);
	await waitIdle(tid);
	await shot(p, "T2-done");
}

/** T3：await 期间在输入框发消息：调用以 dismissed 结束，消息送达。 */
async function T3() {
	const n = nonce();
	const args = {
		title: `T3 ${n}`,
		await: true,
		blocks: [
			{
				type: "form",
				fields: [{ name: "x", label: "X", kind: "text" }],
				submitLabel: "Send T3",
			},
		],
	};
	const tid = await newThread(
		`${PREFIX}${callArgs("render_ui", args)}After it returns, follow whatever my next message says.`,
		"T3",
	);
	const p = await openPage(`/t/${tid}`);
	const call = await waitCall(tid, "render_ui", `T3 ${n}`, ORB_TURN);
	await waitState(tid, (s) => s === "awaiting_input", "进入 awaiting_input");
	const v = await waitView(p, call.id, "ui");
	await v.getByTestId("view-awaiting").waitFor({ timeout: 30_000 });
	const msg = `T3 typed reply ${n}: reply with exactly one line \`T3-ACK ${n}\`.`;
	const box = p.getByLabel("消息", { exact: true });
	await box.fill(msg);
	await box.press("Enter");
	const res = await waitResult(tid, call.id, 60_000);
	check(
		res.view?.outcome === "dismissed" &&
			res.text ===
				"The user chose to reply in text instead; their message follows.",
		`调用以 dismissed 结束：${clip(res.text, 120)}`,
	);
	await waitUser(tid, "输入框发的消息", (s) =>
		s.includes(`T3 typed reply ${n}`),
	);
	check(true, "输入框的消息送达");
	await waitReply(tid, "T3-ACK", (s) => s.includes(`T3-ACK ${n}`), res.seq);
	check(true, "agent 读到消息并回复 T3-ACK");
	await waitIdle(tid);
	await shot(p, "T3-dismissed");
}

/** T4：render_html + libs:["chartjs"]：帧里图表画出；sendPrompt 送达；第二次调用被忽略。 */
async function T4() {
	const n = nonce();
	const first = `T4-first-${n}`;
	const second = `T4-second-${n}`;
	const args = {
		title: `T4 ${n}`,
		libs: ["chartjs"],
		height: 360,
		html: `<canvas id="c" width="480" height="240"></canvas>
<button id="send" style="font-size:16px;padding:6px 12px">Send T4</button>
<script>
new Chart(document.getElementById("c"), { type: "bar", data: { labels: ["a", "b", "c"], datasets: [{ label: "T4", data: [3, 5, 2] }] }, options: { animation: false, responsive: false } });
document.getElementById("send").onclick = function () { window.oryn.sendPrompt("${first}"); window.oryn.sendPrompt("${second}"); };
</script>`,
	};
	const tid = await newThread(
		`${PREFIX}${callArgs("render_html", args)}Do not set \`await\`. After the call, end your turn immediately without writing anything. When a message from the view arrives later, reply with exactly one line: \`T4-ACK\`.`,
		"T4",
	);
	const call = await waitCall(tid, "render_html", `T4 ${n}`, ORB_TURN);
	const res = await waitResult(tid, call.id);
	const spec = isRecord(res.view?.spec) ? res.view.spec : undefined;
	check(
		res.view?.outcome === "rendered" &&
			res.text === "Rendered." &&
			JSON.stringify(spec?.libs) === '["chartjs"]' &&
			!("html" in (spec ?? {})),
		`结果 rendered，spec = ${JSON.stringify(spec)}`,
	);
	await waitIdle(tid);
	const p = await openPage(`/t/${tid}`);
	await waitView(p, call.id, "html");
	const f = await viewFrame(p, call.id);
	const painted = await f.evaluate(() => {
		const c = document.getElementById("c") as HTMLCanvasElement | null;
		const g = c?.getContext("2d");
		if (!c || !g) return -1;
		const d = g.getImageData(0, 0, c.width, c.height).data;
		let k = 0;
		for (let i = 3; i < d.length; i += 4) if (d[i]) k++;
		return k;
	});
	const hasChart = await f.evaluate(() =>
		"Chart" in window ? typeof window.Chart : "undefined",
	);
	check(
		hasChart === "function" && painted > 500,
		`帧里 Chart 已注入（${hasChart}），画布有 ${painted} 个非透明像素`,
	);
	await shot(p, "T4-chart");
	const before = await lastSeq(tid);
	await clickInFrame(p, call.id, f, "#send");
	const u = await waitUser(
		tid,
		"HTML 视图回执",
		(s) => s.startsWith(receiptHead(call.id, "html")),
		before,
	);
	check(
		(userText(u) ?? "").includes(first),
		`sendPrompt 送达：${clip(oneLine(userText(u) ?? ""), 160)}`,
	);
	await viewOf(p, call.id).getByTestId("html-view-sent").waitFor({
		timeout: 30_000,
	});
	// 再点一次：每次调用只回传一次
	await clickInFrame(p, call.id, f, "#send");
	await waitReply(tid, "T4-ACK", (s) => s.includes("T4-ACK"), u.seq);
	await waitIdle(tid);
	await sleep(5_000);
	const es = await entries(tid);
	const extra = es.filter((e) => {
		const s = userText(e);
		return (
			s?.startsWith("<oryn-view-answer") === true &&
			(s.includes(second) || e.seq > u.seq)
		);
	});
	check(
		extra.length === 0,
		`第二次 sendPrompt 与再次点击都被忽略（多余回执 ${extra.length} 条）`,
	);
	await shot(p, "T4-sent");
	shared.t4 = { tid, callId: call.id };
}

/** T5：帧的 CSP：fetch 被拒；https 图片能加载；帧读不到父页面。 */
async function T5() {
	const n = nonce();
	const IMG = "https://www.google.com/favicon.ico";
	const hostImg = await fetch(IMG, { signal: AbortSignal.timeout(15_000) })
		.then((r) => r.ok)
		.catch(() => false);
	const args = {
		title: `T5 ${n}`,
		height: 160,
		html: `<p id="r">pending</p><img id="im" alt="probe" src="${IMG}">
<script>
var out = {};
function show() { document.getElementById("r").textContent = JSON.stringify(out); }
try { out.parent = "readable:" + String(window.parent.document.title); } catch (e) { out.parent = "blocked:" + e.name; }
fetch("https://example.com/").then(function () { out.fetch = "ok"; show(); }, function (e) { out.fetch = "blocked:" + e.name; show(); });
var im = document.getElementById("im");
im.onload = function () { out.img = "loaded"; show(); };
im.onerror = function () { out.img = "error"; show(); };
show();
</script>`,
	};
	const tid = await newThread(
		`${PREFIX}${callArgs("render_html", args)}Do not set \`await\`. After the call, end your turn immediately without writing anything.`,
		"T5",
	);
	const call = await waitCall(tid, "render_html", `T5 ${n}`, ORB_TURN);
	await waitResult(tid, call.id);
	await waitIdle(tid);
	const p = await openPage(`/t/${tid}`);
	await waitView(p, call.id, "html");
	const f = await viewFrame(p, call.id);
	const out = await waitFor(
		"帧内探针出结论",
		async () => {
			const s = await f.locator("#r").textContent();
			const o = JSON.parse(s ?? "{}") as Record<string, string>;
			return o.fetch && o.img && o.parent ? o : null;
		},
		30_000,
		500,
	);
	note(`帧内探针：${JSON.stringify(out)}`);
	check(
		out.fetch?.startsWith("blocked") === true,
		"fetch 被拒（connect-src 'none'）",
	);
	check(
		out.parent?.startsWith("blocked") === true,
		"帧读不到父页面（不透明源）",
	);
	// 帧根的响应头：策略字面量（C8）
	const fr = await rjson<{ token: string; url: string }>(
		"POST",
		`/api/v1/threads/${tid}/view-frames`,
		{ toolCallId: call.id },
	);
	const root = await http("GET", fr.url.replace(UI, API));
	const csp = root.headers.get("content-security-policy") ?? "";
	note(`帧根 ${root.status}，CSP：${clip(csp, 400)}`);
	check(
		root.status === 200 &&
			csp.includes("sandbox allow-scripts") &&
			csp.includes("connect-src 'none'") &&
			csp.includes("img-src data: blob: https:") &&
			csp.includes("form-action 'none'"),
		"帧根的 CSP 含 sandbox allow-scripts、connect-src 'none'、img-src https:、form-action 'none'",
	);
	await api("DELETE", `/api/v1/artifact-frames/${fr.token}`);
	await shot(p, "T5-csp");
	if (!hostImg)
		skip(
			`本机访问不了 ${IMG}（没有公网），https 图片能加载未验证（fetch 被拒、父页面隔离已验证）`,
		);
	check(out.img === "loaded", `https 图片能加载（${IMG}）`);
}

/** T6：关掉开关后新启动的 agent 没有这两个工具，旧视图仍显示。 */
async function T6() {
	const t1 = shared.t1;
	const hasView = (tools: string[]) =>
		tools.includes("render_ui") || tools.includes("render_html");
	// 对照：开关开启时启动的 agent 有视图工具（agent 还在跑时）
	for (const [what, tid] of [
		["T1 工作线程", t1?.tid],
		["T8 助手对话", shared.t8?.tid],
	] as const) {
		if (!tid) continue;
		const tools = await activeTools(tid).catch((e: unknown) => {
			note(
				`对照 ${what} 取工具表失败：${e instanceof Error ? e.message : String(e)}`,
			);
			return undefined;
		});
		if (tools)
			expect(
				hasView(tools),
				`对照：开关开启时的${what}有 render_ui / render_html`,
			);
	}
	try {
		const off = await rjson<{ enabled: boolean }>(
			"PUT",
			"/api/v1/settings/generative-ui",
			{ enabled: false },
		);
		check(!off.enabled, "PUT generative-ui enabled=false");
		const n = nonce();
		// 工作线程：同一 orb 上新开线程 → 新 agent 启动
		const tid = await newThread(
			`${PREFIX}Do not call any tool. Reply with exactly one line: \`T6W-${n}\`.`,
			"T6",
		);
		await waitReply(tid, "T6W", (s) => s.includes(`T6W-${n}`), 0, ORB_TURN);
		const wt = await activeTools(tid);
		note(`新工作线程 ${tid} 的工具表（${wt.length}）：${wt.join(",")}`);
		check(
			wt.length > 0 && !hasView(wt),
			"关掉开关后新启动的工作线程 agent 没有 render_ui / render_html",
		);
		const env = dexec(
			`for p in /proc/[0-9]*; do if tr '\\0' '\\n' < $p/environ 2>/dev/null | grep -qx 'ORYN_THREAD_ID=${tid}'; then echo "pid=\${p#/proc/} $(tr '\\0' '\\n' < $p/environ | grep -c '^ORYN_VIEW_TOOLS=')"; fi; done`,
		);
		note(
			`工作线程 agent 进程（pid 与 ORYN_VIEW_TOOLS 个数）：${oneLine(env.out) || "无"}`,
		);
		expect(
			env.out.includes("pid=") && !/ [1-9]/.test(env.out),
			"该 agent 的环境里没有 ORYN_VIEW_TOOLS",
		);
		// 全局助手：新对话
		await ensureCoordinator();
		const conv = await rjson<{ thread: ThreadView }>(
			"POST",
			"/api/v1/assistant/conversations",
			{
				prompt: `${PREFIX}Do not call any tool. Reply with exactly one line: \`T6A-${n}\`.`,
			},
		);
		await waitReply(conv.thread.id, "T6A", (s) => s.includes(`T6A-${n}`), 0);
		const at = await activeTools(conv.thread.id);
		note(
			`新助手对话 ${conv.thread.id} 的工具表（${at.length}）：${at.join(",")}`,
		);
		check(
			at.length > 0 && !hasView(at),
			"关掉开关后新开的全局助手对话没有 render_ui / render_html",
		);
		// 旧视图照常显示
		if (!t1) skip("T1 没有留下视图，旧视图仍显示未验证（其余已验证）");
		const p = await openPage(`/t/${t1.tid}`);
		const v = await waitView(p, t1.callId, "ui");
		await v.getByRole("status").filter({ hasText: "已提交" }).waitFor({
			timeout: 30_000,
		});
		check(true, "关掉开关后 T1 的视图照常显示（已提交）");
		if (shared.t8) {
			const pa = await openPage(`/assistant/${shared.t8.tid}`);
			await waitView(pa, shared.t8.callId, "ui");
			check(true, "全局助手 T8 的视图照常显示");
			await shot(pa, "T6-assistant-old-view");
		}
		await shot(p, "T6-old-view");
	} finally {
		const on = await api("PUT", "/api/v1/settings/generative-ui", {
			enabled: true,
		});
		note(`恢复开关 enabled=true → ${on.status}`);
	}
}

/** T7：公开分享页：声明式视图禁用；HTML 视图能运行；撤销分享后帧 404。 */
async function T7() {
	const t1 = shared.t1;
	const t4 = shared.t4;
	if (!t1 || !t4) throw new Error("前置 T1、T4 未通过");
	const share = async (tid: string) => {
		let r = await api("POST", `/api/v1/threads/${tid}/shares`, {
			mode: "snapshot",
		});
		if (r.status === 409)
			r = await api("POST", `/api/v1/threads/${tid}/shares`, {
				mode: "snapshot",
				acknowledgeFindings: true,
			});
		if (r.status !== 201)
			throw new Error(`创建分享 ${tid} → ${r.status} ${clip(r.text)}`);
		return r.json as { id: string; slug: string };
	};
	// 声明式视图
	const s1 = await share(t1.tid);
	const p1 = await openPage(`/s/${s1.slug}`, { anon: true });
	const v = await waitView(p1, t1.callId, "ui");
	const input = v.getByLabel("City");
	check(
		(await input.isDisabled()) && (await input.inputValue()) === t1.city,
		`分享页 T1 表单禁用并显示作答（City=${await input.inputValue()}）`,
	);
	check(
		(await v.locator("button:enabled").count()) === 0,
		"分享页视图里没有可用按钮",
	);
	await shot(p1, "T7-share-ui");
	// HTML 视图
	const s4 = await share(t4.tid);
	const p4 = await openPage(`/s/${s4.slug}`, { anon: true });
	await waitView(p4, t4.callId, "html");
	const f = await viewFrame(p4, t4.callId);
	const painted = await f.evaluate(() => {
		const c = document.getElementById("c") as HTMLCanvasElement | null;
		const g = c?.getContext("2d");
		if (!c || !g) return -1;
		const d = g.getImageData(0, 0, c.width, c.height).data;
		let k = 0;
		for (let i = 3; i < d.length; i += 4) if (d[i]) k++;
		return k;
	});
	check(
		painted > 500,
		`分享页的 HTML 视图运行（画布 ${painted} 个非透明像素）`,
	);
	const src =
		(await viewOf(p4, t4.callId).locator("iframe").getAttribute("src")) ?? "";
	const frameRoot = src.split("#")[0] ?? "";
	check(frameRoot.includes("/_af/"), `分享页的帧经公开端点申请：${frameRoot}`);
	const fr = await http(
		"POST",
		`${API}/api/v1/public/shares/${s4.slug}/view-frames`,
		{ toolCallId: t4.callId },
	);
	check(fr.status === 201, `匿名 POST public view-frames → ${fr.status}`);
	await shot(p4, "T7-share-html");
	// 撤销
	const del = await api("DELETE", `/api/v1/shares/${s4.id}`);
	check(del.status === 204, `撤销分享 → ${del.status}`);
	const after = await http("GET", frameRoot.replace(UI, API));
	check(after.status === 404, `撤销后帧根 → ${after.status}`);
	const again = await http(
		"POST",
		`${API}/api/v1/public/shares/${s4.slug}/view-frames`,
		{ toolCallId: t4.callId },
	);
	check(again.status === 404, `撤销后再申请公开帧 → ${again.status}`);
	await api("DELETE", `/api/v1/shares/${s1.id}`);
}

/** T8：全局助手：非阻塞与 await 各走一遍。 */
async function T8() {
	await ensureCoordinator();
	const n = nonce();
	const args = {
		title: `T8 ${n}`,
		blocks: [
			{ type: "text", text: "Pick one." },
			{
				type: "actions",
				buttons: [
					{ label: "Pick A", prompt: `T8 choice A ${n}` },
					{ label: "Pick B", prompt: `T8 choice B ${n}`, variant: "primary" },
				],
			},
		],
	};
	const conv = await rjson<{ thread: ThreadView }>(
		"POST",
		"/api/v1/assistant/conversations",
		{
			prompt: `${PREFIX}${callArgs("render_ui", args)}Do not set \`await\`. After the call, end your turn immediately without writing anything. When my choice arrives later, reply with exactly one line: \`T8-ACK <A or B>\`.`,
		},
	);
	const tid = conv.thread.id;
	note(`助手对话 ${tid}`);
	const call = await waitCall(tid, "render_ui", `T8 ${n}`);
	const res = await waitResult(tid, call.id);
	check(
		res.view?.outcome === "rendered" && isRecord(res.view.spec),
		`非阻塞结果 rendered（${clip(res.text, 80)}）`,
	);
	await waitState(tid, (s) => SETTLED[s] === true, "静止");
	const p = await openPage(`/assistant/${tid}`);
	const v = await waitView(p, call.id, "ui");
	const before = await lastSeq(tid);
	await v.getByRole("button", { name: "Pick B" }).click();
	const u = await waitUser(
		tid,
		"按钮回执",
		(s) => s.startsWith(receiptHead(call.id, "1", 1)),
		before,
	);
	check(
		(userText(u) ?? "").includes(`T8 choice B ${n}`),
		`回执：${clip(oneLine(userText(u) ?? ""), 160)}`,
	);
	await waitReply(tid, "T8-ACK", (s) => /T8-ACK\s*B/.test(s), u.seq);
	check(true, "助手收到按钮作答并继续");
	check(
		(await v
			.getByRole("button", { name: "Pick B" })
			.getAttribute("aria-pressed")) === "true",
		"按钮组锁定，Pick B 选中",
	);
	await shot(p, "T8-actions");
	shared.t8 = { tid, callId: call.id };

	// await
	await waitState(tid, (s) => SETTLED[s] === true, "静止");
	const code = `K${n}`;
	const awaitArgs = {
		title: `T8 await ${n}`,
		await: true,
		blocks: [
			{
				type: "form",
				fields: [{ name: "code", label: "Code", kind: "text", required: true }],
				submitLabel: "Send T8",
			},
		],
	};
	await say(
		tid,
		`${PREFIX}${callArgs("render_ui", awaitArgs)}After it returns, reply with exactly one line: \`T8-AWAIT-ACK <the code value>\`.`,
	);
	const c2 = await waitCall(tid, "render_ui", `T8 await ${n}`);
	const awaiting = await waitState(
		tid,
		(s) => s === "awaiting_input",
		"进入 awaiting_input",
	).then(
		() => true,
		() => false,
	);
	expect(awaiting, "助手 await 期间对话是 awaiting_input");
	const v2 = await waitView(p, c2.id, "ui");
	await v2.getByTestId("view-awaiting").waitFor({ timeout: 60_000 });
	const submit = v2.getByRole("button", { name: "Send T8" });
	check(await submit.isEnabled(), "await 期间表单可提交");
	check(
		resultIn(await entries(tid), c2.id) === undefined,
		"此时还没有工具结果",
	);
	await v2.getByLabel("Code").fill(code);
	await submit.click();
	const r2 = await waitResult(tid, c2.id, 60_000);
	check(
		r2.view?.outcome === "answered" && r2.text.includes(code),
		`助手 await：结果 answered，含作答（${clip(oneLine(r2.text), 120)}）`,
	);
	await waitReply(
		tid,
		"T8-AWAIT-ACK",
		(s) => s.includes(`T8-AWAIT-ACK ${code}`),
		r2.seq,
	);
	check(true, "同一回合继续并回复 T8-AWAIT-ACK");
	await shot(p, "T8-await");
}

/** 子会话文件（子 agent / tan 克隆）里任务含探针标记的 `session_init`，连同该会话的助手文字。 */
interface SubSession {
	file: string;
	tools: string[];
	systemPrompt: string;
	assistant: string;
}

/** orb 里含 marker 的 session_init（子 agent / tan 克隆的会话文件）。 */
function sessionInits(marker: string): SubSession[] {
	const files = dexec(
		`grep -rlF --include='*.jsonl' ${q(marker)} /home/user/.omp /home/user/.oryn 2>/dev/null || true`,
	).stdout.split("\n");
	const out: SubSession[] = [];
	for (const file of files.filter(Boolean)) {
		const raw = dexec(`cat ${q(file)}`).stdout;
		let init: Record<string, unknown> | undefined;
		let assistant = "";
		for (const line of raw.split("\n")) {
			let e: unknown;
			try {
				e = JSON.parse(line);
			} catch {
				continue;
			}
			if (!isRecord(e)) continue;
			if (e.type === "session_init" && String(e.task ?? "").includes(marker))
				init = e;
			if (
				e.type === "message" &&
				isRecord(e.message) &&
				e.message.role === "assistant"
			)
				assistant += textOf(e.message.content);
		}
		if (!init) continue;
		const sp = init.systemPrompt;
		out.push({
			file,
			tools: Array.isArray(init.tools) ? init.tools.map(String) : [],
			systemPrompt: Array.isArray(sp) ? sp.join("\n") : String(sp ?? ""),
			assistant,
		});
	}
	return out;
}

/** T9：默认子 agent（task 工具派出）与 /tan 克隆都没有视图工具，系统提示词里没有视图指引。 */
async function T9() {
	const n = nonce();
	const work = (kind: string) =>
		`Acceptance probe T9-${kind}-${n}. Do not call any tool. Reply with exactly one line: TOOLS=<comma-separated exact names of every tool you can call> GUIDE=<yes if your system prompt contains a section headed "Views (render_ui / render_html)", otherwise no> ORB=<how many separate sections headed "# Oryn orb 环境" your system prompt contains>.`;
	const tid = await newThread(
		`${PREFIX}Use the \`task\` tool exactly once to run ONE subagent with the default agent type and no tool restrictions, giving it exactly this assignment:\n\n${work("task")}\n\nWhen it returns, reply with exactly one line: \`T9-TASK <the subagent's reply line>\`.`,
		"T9",
	);
	const r = await waitReply(
		tid,
		"T9-TASK",
		(s) => s.includes("T9-TASK"),
		0,
		ORB_TURN,
	);
	note(`主 agent 转述：${clip(oneLine(assistantText(r) ?? ""), 240)}`);
	const calls = callsOf(await entries(tid));
	check(
		calls.some((c) => c.name === "task"),
		`主 agent 调用了 task（${calls.map((c) => c.name).join(",")}）`,
	);
	const main = await activeTools(tid);
	check(
		main.includes("render_ui") && main.includes("render_html"),
		`对照：主 agent 的工具表有 render_ui / render_html（共 ${main.length} 个）`,
	);
	// session_init.tools 是 Core 在扩展 session_start 之前写下的（task/executor.ts 先 appendSessionInit、后 initializeExtensions），
	// 不反映扩展在 session_start 里 setActiveTools 的结果；发给模型的工具表只能看子 agent 自己报的那一行。用 session_init
	// 核对自述可信（报出的名字都在里面），再断言报出的工具里没有视图工具、自述 GUIDE=no、ORB=1。
	// session_init.systemPrompt 记的是 before_agent_start 的输入（基线）：task 子 agent 的基线里本就不该有视图指引；/tan 克隆
	// 的基线是父会话当时的系统提示词（含父会话追加的 orb 段与视图指引），由扩展在 before_agent_start 去掉后再追加自己的一段，
	// 所以克隆只断言基线里确有继承来的段（前提成立），发给模型的结果看自述：没有视图指引、orb 段只有一段（未修复时是两段）。
	const verify = (kind: string, inits: SubSession[], inherits: boolean) => {
		check(
			inits.length > 0,
			`找到 ${kind} 的子会话 session_init（${inits.map((i) => i.file).join("、")}）`,
		);
		for (const i of inits) {
			note(
				`${kind} session_init.tools（${i.tools.length}，session_start 之前）${i.tools.includes("render_ui") ? "含" : "不含"} render_ui`,
			);
			const said = /TOOLS=([^\n]*?)\s+GUIDE=(\w+)(?:\s+ORB=(\d+))?/.exec(
				i.assistant,
			);
			check(
				said !== null,
				`${kind} 在子会话里给出了 TOOLS=… GUIDE=… ORB=… 自述`,
			);
			const reported = (said?.[1] ?? "")
				.split(",")
				// 有的模型网关把工具名加上 `mcp__` 前缀再发给模型：去掉前缀后在 session_init 里的按原名算
				.map((s) => {
					const t = s.trim();
					return t.startsWith("mcp__") && i.tools.includes(t.slice(5))
						? t.slice(5)
						: t;
				})
				.filter(Boolean);
			note(`${kind} 自述的工具（${reported.length}）：${reported.join(",")}`);
			const known = reported.filter((t) => i.tools.includes(t)).length;
			check(
				reported.length >= 5 && known === reported.length,
				`${kind} 自述的工具都在 session_init 里（${known}/${reported.length}，自述可信）`,
			);
			check(
				!reported.includes("render_ui") && !reported.includes("render_html"),
				`${kind} 发给模型的工具里没有 render_ui / render_html`,
			);
			check(
				said?.[2]?.toLowerCase() === "no",
				`${kind} 系统提示词里没有视图指引（自述 GUIDE=${said?.[2]}）`,
			);
			check(
				said?.[3] === "1",
				`${kind} 系统提示词里只有一段 orb 指引（自述 ORB=${said?.[3]}）`,
			);
			if (inherits)
				note(
					`${kind} session_init 的基线${i.systemPrompt.includes(GUIDE_HEAD) ? "含" : "不含"}父会话继承来的视图指引（before_agent_start 的输入）`,
				);
			else
				check(
					!i.systemPrompt.includes(GUIDE_HEAD),
					`${kind} session_init 的系统提示词基线里没有视图指引`,
				);
		}
	};
	verify("task 子 agent", sessionInits(`T9-task-${n}`), false);

	// /tan：与 Web 的斜杠命令同一帧
	await waitIdle(tid);
	const tan = await wsCmd(tid, { type: "tan", work: work("tan") });
	note(`tan 应答：${clip(JSON.stringify(tan), 200)}`);
	const inits = await waitFor(
		"tan 克隆的会话出现 session_init 与回复",
		() => {
			const s = sessionInits(`T9-tan-${n}`);
			return s.length && s.every((i) => /TOOLS=/.test(i.assistant)) ? s : null;
		},
		TURN,
		5_000,
	).catch(() => sessionInits(`T9-tan-${n}`));
	verify("/tan 克隆", inits, true);
}

/** T10：第二台设备打开同一线程，看到同样的锁定状态。 */
async function T10() {
	const t1 = shared.t1;
	if (!t1) throw new Error("前置 T1 未通过");
	const p = await openPage(`/t/${t1.tid}`, { session: sessions[1] });
	const v = await waitView(p, t1.callId, "ui");
	await v.getByRole("status").filter({ hasText: "已提交" }).waitFor({
		timeout: 30_000,
	});
	const input = v.getByLabel("City");
	check(
		(await input.inputValue()) === t1.city && (await input.isDisabled()),
		`第二台设备：T1 表单已提交、City=${await input.inputValue()}、控件禁用`,
	);
	check(
		(await v.getByRole("button", { name: "Send T1" }).count()) === 0,
		"第二台设备：没有提交按钮",
	);
	await shot(p, "T10-device2");
	if (shared.t8) {
		const pa = await openPage(`/assistant/${shared.t8.tid}`, {
			session: sessions[1],
		});
		const va = await waitView(pa, shared.t8.callId, "ui");
		check(
			(await va
				.getByRole("button", { name: "Pick B" })
				.getAttribute("aria-pressed")) === "true" &&
				(await va.getByRole("button", { name: "Pick A" }).isDisabled()),
			"第二台设备：T8 按钮组锁定在 Pick B",
		);
	}
}

/** T11：被 Core 修复过参数的调用（blocks 二次编码成字符串）：实时显示与刷新后的显示一致。 */
async function T11() {
	const n = nonce();
	const heading = { type: "heading", text: `T11 heading ${n}` };
	const stats = {
		type: "stats",
		items: [
			{ label: "Alpha", value: 42, trend: "up" },
			{ label: "Beta", value: "7%" },
		],
	};
	const done = {
		type: "actions",
		buttons: [{ label: "Done T11", prompt: `T11 done ${n}` }],
	};
	// 依次尝试三种需要 Core 修复的参数，直到模型照做：blocks 二次编码、blocks 写成单个对象、progress.value 写成字符串
	const variants: { name: string; blocks: unknown; ask: string }[] = [
		{
			name: "blocks 二次编码成字符串",
			blocks: JSON.stringify([heading, stats, done]),
			ask: "the `blocks` argument MUST be a JSON-encoded STRING (double-encoded), not an array",
		},
		{
			name: "blocks 写成单个对象",
			blocks: { type: "card", blocks: [heading, stats, done] },
			ask: "the `blocks` argument MUST be a single OBJECT, not wrapped in an array",
		},
		{
			name: "progress.value 写成字符串",
			blocks: [
				heading,
				stats,
				{ type: "progress", label: "P", value: "40" },
				done,
			],
			ask: 'the progress block\'s `value` MUST be the STRING "40" (with quotes), not the number 40',
		},
	];
	/** 键排序后的 JSON：比较原始参数与执行时的参数是否不同（= Core 修复过）。 */
	const canon = (v: unknown) =>
		JSON.stringify(v, (_k, x: unknown) =>
			isRecord(x)
				? Object.fromEntries(
						Object.entries(x).sort(([a], [b]) => (a < b ? -1 : 1)),
					)
				: x,
		);
	// 挂起时的「agent 正在等你…」与作答后的读屏状态（sr-only）不属于视图内容
	const strip = (s: string) =>
		s
			.replace(/已选择「[^」]*」/g, "")
			.replace("agent 正在等你在这个视图里操作", "")
			.trim();
	let tid = "";
	let p: Page | undefined;
	for (const [i, vnt] of variants.entries()) {
		const title = `T11-${i} ${n}`;
		const prompt = `${PREFIX}Call the \`render_ui\` tool exactly once. This test checks argument repair, so ${vnt.ask}. Use exactly these arguments:\n\`\`\`json\n${JSON.stringify({ title, await: true, blocks: vnt.blocks }, null, 1)}\n\`\`\`\nAfter it returns, reply with exactly one line: \`T11-ACK ${i}\`.`;
		if (tid) await say(tid, prompt);
		else tid = await newThread(prompt, "T11");
		p ??= await openPage(`/t/${tid}`);
		const call = await waitCall(tid, "render_ui", title, ORB_TURN);
		await waitState(
			tid,
			(s) => s === "awaiting_input",
			"挂起（awaiting_input）",
		);
		const v = await waitView(p, call.id, "ui");
		await v.getByText(`T11 heading ${n}`).waitFor({ timeout: 30_000 });
		const live = oneLine((await v.textContent()) ?? "");
		await shot(p, `T11-live-${i}`);
		await v.getByRole("button", { name: "Done T11" }).click();
		const res = await waitResult(tid, call.id, 60_000);
		await waitReply(tid, "T11-ACK", (s) => s.includes(`T11-ACK ${i}`), res.seq);
		await waitIdle(tid);
		const spec = isRecord(res.view?.spec) ? res.view.spec : {};
		const { await: _, ...raw } = call.args;
		if (canon(raw) === canon(spec)) {
			note(
				`变体「${vnt.name}」：模型给的参数不需要修复（原始参数与执行时一致），换下一种`,
			);
			continue;
		}
		note(
			`变体「${vnt.name}」：原始 blocks ${clip(JSON.stringify(raw.blocks), 120)} → 执行时 ${clip(JSON.stringify(spec.blocks), 120)}`,
		);
		check(
			res.view?.outcome === "answered" && Array.isArray(spec.blocks),
			"Core 修复参数后调用照常执行，结果的 details.view.spec.blocks 是数组",
		);
		note(`实时显示（tool_execution_start 的已修复参数）：${clip(live, 200)}`);
		await p.reload({ waitUntil: "domcontentloaded" });
		const v2 = await waitView(p, call.id, "ui");
		await v2.getByText(`T11 heading ${n}`).waitFor({ timeout: 30_000 });
		const after = oneLine((await v2.textContent()) ?? "");
		note(`刷新后显示（details.view.spec）：${clip(after, 200)}`);
		await shot(p, "T11-reloaded");
		check(
			strip(live) === strip(after) &&
				live.includes("Alpha") &&
				live.includes("42"),
			"实时显示与刷新后的显示文字一致（含标题、stats）",
		);
		return;
	}
	skip(
		`三种变体（${variants.map((x) => x.name).join("、")}）模型都给出了合法参数，Core 的参数修复没有被触发`,
	);
}

// ---------------------------------------------------------------- 主流程

const TESTS: [string, () => Promise<void>, string[]][] = [
	["T1", T1, []],
	["T2", T2, []],
	["T3", T3, []],
	["T4", T4, []],
	["T5", T5, []],
	["T11", T11, []],
	["T9", T9, []],
	["T8", T8, []],
	["T10", T10, ["T1"]],
	["T7", T7, ["T1", "T4"]],
	["T6", T6, []],
];
const verdicts = new Map<
	string,
	{ v: "PASS" | "FAIL" | "SKIP"; why: string }
>();

function killStragglers() {
	for (const pid of readdirSync("/proc")) {
		if (!/^\d+$/.test(pid) || Number(pid) === process.pid) continue;
		try {
			const cwd = readlinkSync(`/proc/${pid}/cwd`);
			const cmd = readFileSync(`/proc/${pid}/cmdline`, "utf8");
			if ([cwd, cmd].some((s) => s.includes(TMP) || s.includes(COORD_DIR)))
				process.kill(Number(pid), "SIGKILL");
		} catch {
			// 已退出或无权限
		}
	}
}

let cleaning: Promise<void> | undefined;
async function doCleanup() {
	console.log("M33 收尾");
	await closePages();
	await browser?.close().catch(() => undefined);
	if (token) {
		const list = await api("GET", "/api/v1/orbs").catch(() => undefined);
		const ids =
			isRecord(list?.json) && Array.isArray(list.json.orbs)
				? list.json.orbs
						.filter((o) => isRecord(o) && o.coordinator !== true)
						.map((o) => (isRecord(o) ? String(o.id) : ""))
				: [];
		for (const id of ids) {
			const r = await api("DELETE", `/api/v1/orbs/${id}`).catch(
				() => undefined,
			);
			note(`DELETE orb ${id} → ${r?.status ?? "失败"}`);
		}
		if (ids.length)
			await waitFor(
				"orb 容器删除",
				() => sh(`docker ps -aq --filter ancestor=${IMAGE}`).stdout === "",
				120_000,
				2_000,
			).catch(() => note("部分 orb 容器未按时删除，改为 docker rm -f"));
	}
	if (imageBuilt) {
		sh(
			`docker ps -aq --filter ancestor=${IMAGE} | xargs -r docker rm -f >/dev/null 2>&1`,
		);
		sh(`docker rmi ${IMAGE} >/dev/null 2>&1`);
	}
	for (const b of bgs.splice(0).reverse()) {
		if (b.proc.exitCode === null && b.proc.signalCode === null) {
			b.proc.kill("SIGTERM");
			const r = await Promise.race([
				b.proc.exited,
				sleep(10_000).then(() => "timeout"),
			]);
			if (r === "timeout") b.proc.kill("SIGKILL");
		}
	}
	killStragglers();
	if (KEEP) {
		console.log(`保留 ${TMP}（日志在 logs/）与 ${COORD_DIR}`);
		return;
	}
	rmSync(TMP, { recursive: true, force: true });
	rmSync(COORD_DIR, { recursive: true, force: true });
}
const cleanup = () => {
	cleaning ??= doCleanup();
	return cleaning;
};
for (const sig of ["SIGINT", "SIGTERM"] as const)
	process.on(sig, () => {
		console.log(`\n收到 ${sig}，收尾后退出`);
		void cleanup().finally(() => process.exit(130));
	});

const selected = TESTS.filter(([id]) => !ONLY || ONLY.has(id));
console.log(
	`M33 真机验收（run ${RUN}，临时目录 ${TMP}，截图 ${SHOTS}；条目 ${selected.map(([id]) => id).join(" ")}）`,
);
try {
	for (const [id, fn, needs] of selected) {
		if (envState === "todo") {
			console.log("M33 环境");
			const t0 = Date.now();
			try {
				await setupEnv();
				envState = "ok";
				console.log(`M33 环境 PASS（${sec(Date.now() - t0)}）`);
			} catch (e) {
				envState = "failed";
				envWhy = e instanceof Error ? e.message : String(e);
				note(`✗ ${envWhy}`);
				console.log("M33 环境 FAIL");
			}
		}
		console.log(`── ${id}`);
		const blocked = needs.filter(
			(d) => selected.some(([s]) => s === d) && verdicts.get(d)?.v !== "PASS",
		);
		const missing = needs.filter((d) => !selected.some(([s]) => s === d));
		if (envState !== "ok" || blocked.length || missing.length) {
			const why =
				envState !== "ok"
					? `环境未就绪：${envWhy}`
					: `前置 ${[...blocked, ...missing].join("、")} 未通过${missing.length ? "（未选中）" : ""}`;
			verdicts.set(id, { v: "FAIL", why });
			console.log(`M33 ${id} FAIL（${clip(why, 200)}）`);
			continue;
		}
		softFails = 0;
		const t0 = Date.now();
		let v: "PASS" | "FAIL" | "SKIP" = "PASS";
		let why = "";
		try {
			await fn();
		} catch (e) {
			why = e instanceof Error ? e.message : String(e);
			if (e instanceof Skip) {
				note(`– 跳过：${why}`);
				v = "SKIP";
			} else {
				note(`✗ ${why}`);
				v = "FAIL";
			}
		}
		if (v === "PASS" && softFails) {
			v = "FAIL";
			why = `${softFails} 项软断言失败`;
		}
		if (v === "FAIL") {
			const last = contexts.at(-1)?.pages().at(-1);
			if (last) {
				await shot(last, `${id}-FAIL`);
				await dumpFrameLog(last);
			}
		}
		await closePages();
		verdicts.set(id, { v, why });
		console.log(
			`M33 ${id} ${v}（${sec(Date.now() - t0)}${v === "PASS" ? "" : `；${clip(oneLine(why), 200)}`}）`,
		);
	}
} finally {
	if (HOLD && envState === "ok") {
		console.log(
			`M33_HOLD：环境保持运行（${UI}，设备令牌 ${token}，会话 Cookie __Host-oryn_session=${sessions[0]}），Ctrl-C 收尾`,
		);
		await Promise.withResolvers<never>().promise;
	}
	await cleanup();
}

console.log("M33 汇总");
for (const id of IDS) {
	const r = verdicts.get(id);
	if (!r) continue;
	console.log(
		`  ${id.padEnd(4)} ${r.v}${r.why ? `  ${clip(oneLine(r.why), 160)}` : ""}`,
	);
}
if (pageErrors.length)
	console.log(`  页面报错 ${pageErrors.length} 条（见上方 [pageerror]）`);
const fails = [...verdicts.values()].filter((r) => r.v === "FAIL").length;
const skips = [...verdicts.values()].filter((r) => r.v === "SKIP").length;
console.log(
	fails
		? "M33 真机验收 FAIL"
		: `M33 真机验收 PASS${skips ? `（${skips} 项跳过，未验证）` : ""}`,
);
process.exit(fails ? 1 : 0);
