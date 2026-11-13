#!/usr/bin/env bun
/**
 * M5 真机验收（计划「验证」N1–N18；可选的 ntfy.sh 外网项不做）：对 dev 部署（orynd + docker provider）逐项执行，
 * 每项打印 `N<n> PASS|FAIL` 与证据，失败时给打开的页面截图。
 *
 * 浏览器上下文：
 *   A：`extraHTTPHeaders` 带设备令牌（所有者界面），授予 notifications / clipboard-read / clipboard-write；
 *   C：什么都不带（匿名访客）。
 * 本机假服务（`Bun.serve`，127.0.0.1 随机端口；渠道由 orynd 在宿主上调用，orb 不需要访问它们）：
 *   假 Telegram Bot API（`apiBase`；getMe / getUpdates 长轮询可注入 message 与 callback_query / sendMessage /
 *   answerCallbackQuery / editMessageReplyMarkup 全部记录）、假 Webhook（校验 `X-Oryn-Signature`）、
 *   假 ntfy（可切换为恒回 500）、假 Bark、假 Web Push 端点（自持 P-256 密钥与 auth secret，WebCrypto 按
 *   RFC 8291 aes128gcm 解密，并用 `GET push/vapid-key` 的公钥校验 VAPID JWT）。
 *
 * 用法（web/ 目录）：bun run accept:m5 [-- --only N1,N2]
 * 环境变量：ORYN_URL、ORYN_ROOT、ORYN_TOKEN（同 m3 / m4）；
 *   M5_THREAD（sandbox 项目、新镜像上的线程；缺省时复用 .dev/m5.thread，没有则新建并写入）；
 *   N18_THREAD（M5 之前镜像上的线程；默认 .dev/m4.thread）；CHROMIUM；M5_SHOTS（默认 /tmp/oryn-shots/m5-accept）。
 * CLI 一律 `./bin/oryn`，`ORYN_CREDENTIALS=$ROOT/.dev/credentials.json`。
 *
 * 场景之间的依赖：N3 / N4 / N6 复用 N1 / N2 的分享（单独 `--only` 时按需现建）；N10 复用 N9 的 Telegram 绑定；
 * N17 / N18 在 /tmp/m5-sync 不干净时重新克隆。名字以 `m5-accept` 开头的通知渠道与回环地址的推送订阅在开跑前删除，
 * 跑完把本次建的渠道停用（保留投递记录便于查看）。
 *
 * 依赖的接口与字段（schema/openapi.yaml 与 M5 计划；后端未落地前按计划编码）：
 *   threads/{id}/shares（GET / POST，409 `secrets_found` + `scan.findings[].rule|preview`）、shares（GET，`files[]`）、
 *   shares/{id}（DELETE）、attachments?name=&threadId=（POST 原始字节）、threads/{id}/attachments（`publicUrl`）、
 *   published-files/{id}（DELETE）、public/shares/{slug}[/entries|/blobs/{sha}|/ws]、/s/<slug>[.md]、/f/<slug>/<name>、
 *   notification-channels（POST / GET / PATCH / DELETE、/{id}/telegram/link → `code`）、notifications/read、
 *   push/vapid-key、push/subscriptions、orbs/{id}/sync/prepare（409 `orbd_outdated`）、ws-ticket + /api/v1/ws 的 `cmd`/`res`。
 *   库表：thread_shares(slug, until_seq, findings, thread_id)、threads(last_seq, leaf_entry_id, state, title)、
 *   notifications(thread_id, kind, title, created_at, read_at)、notification_deliveries(notification_id, channel_id,
 *   status, attempts, next_attempt_at, last_error)。
 *   Telegram：done 通知正文以「✅」开头、待答以「❓」开头且含 `/t/<线程 id>`；按钮 `callback_data = o:<投递 id>:<序号>`；
 *   steer 成功回「已发送。」、绑定成功回「已绑定 Oryn 通知。」（internal/notify/text.go）。
 *   Web Push 载荷 `{v,id,kind,title,body,url:"/t/<id>",tag}`；Webhook 体 `{event,id,ts,thread:{id,title,state,url},title,body}`。
 *
 * 待核对的界面选择器（读自 Oryn-m5web worktree 的 S4 / S5 代码，可能仍在变）：
 *   - 线程头「更多操作」按钮 → menuitem「分享…」→ dialog 名「分享线程」；模式是包着 Radio 的 label（文案「冻结在当前内容」/
 *     「之后的新内容也会公开」）；按钮「创建链接」；成功提示「已创建，链接已复制」；已有链接行 = `li` 内含 `[title=<url>]`，
 *     按钮「撤销」→「确认撤销」；命中区 `[data-testid=share-findings]`，勾选 label「我已检查，仍然分享」，按钮「仍然创建」。
 *   - 分享页：撤销后 EmptyState 文案「链接不存在或已被撤销」；转录消息 `[data-role=user|assistant]`；图片 `img[src*=<sha>]`。
 *   - 附件：menuitem「附件」→ dialog（含「附件」）→ 行 `li`（含文件名）→ 按钮「发布」，已发布显示「已公开」。
 *   - Files：`[data-panel-tab=files]`，文件行 `[role=treeitem][data-path$=<name>]`，右键 → menuitem「发布为公开链接」，
 *     提示 `[role=status]`「已复制公开链接…」。Changes：`[data-panel-tab=changes]`，按钮「同步」→ menuitem「复制一次性应用命令」。
 *   - 通知：铃铛按钮名「通知」/「通知（<n> 条未读）」；toast 区 `section[aria-label=通知提示]` 内 `[role=status]`；
 *     设置页 /settings/notifications：渠道行 `li`（含渠道名）内按钮「测试」，结果「测试消息已发送」；「最近通知」投递行文案
 *     「待重试（已尝试 <n> 次）」+ 错误原文，类名 `text-warn`。
 *   - 待答卡片：`[role=dialog]`（aria-label = 请求标题）；输入框 label「消息」。
 */
import { createHash, createHmac, randomBytes } from "node:crypto";
import {
	existsSync,
	mkdirSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Page,
	type WebSocket as PwWebSocket,
} from "playwright-core";

const ROOT = process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`;
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const ORIGIN = new URL(BASE).origin;
const CREDS = `${ROOT}/.dev/credentials.json`;
const TOKEN =
	process.env.ORYN_TOKEN ??
	(JSON.parse(readFileSync(CREDS, "utf8")) as { token: string }).token;
const SHOTS = process.env.M5_SHOTS ?? "/tmp/oryn-shots/m5-accept";
const DB = `${ROOT}/.dev/data/oryn.db`;
const ORYN = `${ROOT}/bin/oryn`;
const RUN = Math.random().toString(36).slice(2, 6);
const PROJECT = "sandbox";
const SANDBOX_REPO = "https://github.com/Lynricsy/oryn-setup-sandbox.git";
const SANDBOX_BRANCH = "main";
const REPO = "/home/user/workspace/repo";
const SYNC_DIR = "/tmp/m5-sync";
const SECRET_LINE = "sk-test-abcdefghijklmnopqrstuvwx0123";
const CH_PREFIX = "m5-accept";
mkdirSync(SHOTS, { recursive: true });

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const te = new TextEncoder();
const td = new TextDecoder();

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

/**
 * `./bin/oryn <args>`（args 由调用方负责引号）。异步执行：`spawnSync` 会卡住事件循环，
 * 期间本进程里的假服务（Webhook / Telegram / 推送）无法应答，orynd 的投递会超时重试。
 */
async function cli(args: string, cwd = ROOT) {
	const p = Bun.spawn(["bash", "-c", `${CLI_ENV} ${q(ORYN)} ${args}`], {
		cwd,
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
		stdout: stdout.trim(),
	};
}

/** 后台跑 `oryn …`（exec 替换掉 bash，信号直达 oryn），输出写到 log。 */
function cliBg(args: string, cwd: string, log: string) {
	return Bun.spawn(
		["bash", "-c", `exec env ${CLI_ENV} ${q(ORYN)} ${args} > ${q(log)} 2>&1`],
		{ cwd },
	);
}

async function exited(
	p: { exited: Promise<number> },
	ms: number,
): Promise<number | "timeout"> {
	return Promise.race([p.exited, sleep(ms).then(() => "timeout" as const)]);
}

function sql<T = Record<string, unknown>>(qs: string): T[] {
	const r = Bun.spawnSync(["sqlite3", "-json", DB, qs]);
	const s = r.stdout.toString().trim();
	return s ? (JSON.parse(s) as T[]) : [];
}

interface ThreadRow {
	state: string;
	last_seq: number;
	leaf_entry_id: string | null;
	orb_id: string | null;
	title: string | null;
}

function threadRow(tid: string): ThreadRow | undefined {
	return sql<ThreadRow>(
		`select state, last_seq, leaf_entry_id, orb_id, title from threads where id='${tid}'`,
	)[0];
}

const stateOf = (tid: string) => threadRow(tid)?.state ?? "?";
const seqOf = (tid: string) => threadRow(tid)?.last_seq ?? 0;

function orbOf(tid: string): string {
	const o = threadRow(tid)?.orb_id;
	if (!o) throw new Error(`线程 ${tid} 没有 orb`);
	return o;
}

const orbState = (orb: string) =>
	sql<{ state: string }>(`select state from orbs where id='${orb}'`)[0]
		?.state ?? "?";

/** 在 orb 容器里以 user 身份跑 shell 命令。 */
function dexec(orb: string, cmd: string) {
	return sh(`docker exec ${orb} bash -lc ${q(cmd)}`);
}

function dwrite(orb: string, path: string, content: string) {
	const r = sh(
		`docker exec -i ${orb} bash -c 'mkdir -p "$(dirname ${path})" && cat > ${path}'`,
		{ stdin: content },
	);
	if (r.code !== 0) throw new Error(`写 ${path} 失败：${r.out}`);
}

interface ApiRes {
	status: number;
	json: unknown;
	text: string;
	headers: Headers;
	bytes: Uint8Array;
}

async function req(
	method: string,
	url: string,
	init: { body?: BodyInit; headers?: Record<string, string> } = {},
): Promise<ApiRes> {
	const res = await fetch(url.startsWith("http") ? url : `${BASE}${url}`, {
		method,
		headers: init.headers,
		body: init.body,
		redirect: "manual",
	});
	const bytes = new Uint8Array(await res.arrayBuffer());
	const text = td.decode(bytes);
	let json: unknown = null;
	try {
		json = JSON.parse(text);
	} catch {
		// 非 JSON 体
	}
	return { status: res.status, json, text, headers: res.headers, bytes };
}

/** 所有者接口（设备令牌 + CSRF 头）。 */
function api(method: string, path: string, body?: unknown) {
	return req(method, path, {
		headers: {
			Authorization: `Bearer ${TOKEN}`,
			"X-Oryn-CSRF": "1",
			...(body !== undefined ? { "Content-Type": "application/json" } : {}),
		},
		body: body !== undefined ? JSON.stringify(body) : undefined,
	});
}

/** 匿名请求（什么都不带）。 */
const anon = (path: string, method = "GET") => req(method, path);

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

const sha256 = (b: Uint8Array | string) =>
	createHash("sha256").update(b).digest("hex");
const b64u = (b: Uint8Array) => Buffer.from(b).toString("base64url");
const unb64u = (s: string) => new Uint8Array(Buffer.from(s, "base64url"));

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
	await page.screenshot({ path: p }).catch(() => {});
	note(`截图 ${p}`);
}

// ---------------------------------------------------------------- 条目

interface Entry {
	seq: number;
	file: string;
	entryId: string;
	parentId?: string;
	type: string;
	payload: Record<string, unknown>;
}

interface EntryPage {
	entries: Entry[];
	hasMore: boolean;
	lastSeq: number;
	leafEntryId?: string;
}

async function pagesOf(
	fetchPage: (after: number) => Promise<ApiRes>,
): Promise<{ entries: Entry[]; raw: string[] }> {
	const entries: Entry[] = [];
	const raw: string[] = [];
	let after = 0;
	for (let i = 0; i < 200; i++) {
		const r = await fetchPage(after);
		if (r.status !== 200) throw new Error(`条目页 ${r.status} ${r.text}`);
		raw.push(r.text);
		const p = r.json as EntryPage;
		entries.push(...p.entries);
		const last = p.entries.at(-1);
		if (!p.hasMore || !last) break;
		after = last.seq;
	}
	return { entries, raw };
}

const privateEntries = (tid: string) =>
	pagesOf((after) =>
		api("GET", `/api/v1/threads/${tid}/entries?after=${after}&limit=500`),
	);

const publicEntries = (slug: string) =>
	pagesOf((after) =>
		anon(`/api/v1/public/shares/${slug}/entries?after=${after}&limit=500`),
	);

function msgOf(e: Entry): { role: string; text: string } | null {
	if (e.type !== "message") return null;
	const m = e.payload.message as
		| { role?: string; content?: unknown }
		| undefined;
	if (!m?.role) return null;
	const c = m.content;
	let text = "";
	if (typeof c === "string") text = c;
	else if (Array.isArray(c))
		text = c
			.map((b: { type?: string; text?: string }) =>
				b?.type === "text" ? (b.text ?? "") : "",
			)
			.join("\n");
	return { role: m.role, text };
}

const isMain = (e: Entry) => !e.file.includes("/");

/** 从叶子沿 parentId 回溯（主会话文件），返回分支上的条目 id。 */
function branchIds(entries: Entry[], leaf: string | null): Set<string> {
	const byId = new Map(entries.filter(isMain).map((e) => [e.entryId, e]));
	const out = new Set<string>();
	let cur = leaf ? byId.get(leaf) : undefined;
	while (cur && !out.has(cur.entryId)) {
		out.add(cur.entryId);
		cur = cur.parentId ? byId.get(cur.parentId) : undefined;
	}
	return out;
}

// ---------------------------------------------------------------- 线程与回合

async function ensureProject(): Promise<string> {
	const r = await api("GET", "/api/v1/projects");
	const list = ((r.json as { projects?: unknown[] })?.projects ??
		r.json ??
		[]) as { id: string; name: string }[];
	const hit = list.find((p) => p.name === PROJECT);
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

async function ensureThread(): Promise<string> {
	if (process.env.M5_THREAD) return process.env.M5_THREAD;
	const f = `${ROOT}/.dev/m5.thread`;
	if (existsSync(f)) {
		const id = readFileSync(f, "utf8").trim();
		if (threadRow(id)?.orb_id) return id;
	}
	const projectId = await ensureProject();
	const r = await api("POST", "/api/v1/threads", {
		projectId,
		prompt: "只回复 ok",
	});
	if (r.status !== 201) throw new Error(`建线程失败 ${r.status} ${r.text}`);
	const id = threadIdOf(r.json);
	writeFileSync(f, id);
	return id;
}

const N18_THREAD =
	process.env.N18_THREAD ??
	(existsSync(`${ROOT}/.dev/m4.thread`)
		? readFileSync(`${ROOT}/.dev/m4.thread`, "utf8").trim()
		: "");

let THREAD = "";
let ORB = "";

/** 唤醒线程的 orb 并等到 ready（dexec / Files 面板前用；暂停中的容器 docker exec 会失败）。 */
async function ensureAwake(tid: string, timeoutMs = 300_000): Promise<string> {
	if (orbState(orbOf(tid)) !== "ready")
		await api("POST", `/api/v1/threads/${tid}/wake`);
	return waitFor(
		`线程 ${tid} 的 orb ready`,
		() => {
			const o = threadRow(tid)?.orb_id;
			return o && orbState(o) === "ready" ? o : undefined;
		},
		timeoutMs,
		1000,
	);
}

const SETTLED: Record<string, true> = {
	idle: true,
	awaiting_input: true,
	error: true,
};

/**
 * 等一个回合结束：`beforeSeq` 之后出现了助手消息条目，且线程处于 idle / awaiting_input / error。
 * （只看状态会撞上新线程 agent 就绪到首条 prompt 之间短暂的 idle。）返回结束时刻与状态。
 */
async function waitTurn(tid: string, beforeSeq: number, timeoutMs = 300_000) {
	const state = await waitFor(
		`线程 ${tid} 回合结束`,
		() => {
			const r = threadRow(tid);
			if (!r || !SETTLED[r.state] || r.last_seq <= beforeSeq) return undefined;
			const n = sql<{ n: number }>(
				`select count(*) n from entries where thread_id='${tid}' and seq>${beforeSeq} and type='message' and instr(cast(payload as text), '"role":"assistant"')>0`,
			)[0]?.n;
			return n ? r.state : undefined;
		},
		timeoutMs,
		250,
	);
	return { state, at: Date.now() };
}

/** `oryn thread send`（不 focus），返回发送前的 last_seq。 */
async function sendCli(tid: string, msg: string): Promise<number> {
	const before = seqOf(tid);
	const r = await cli(`thread send ${tid} ${q(msg)}`);
	if (r.code !== 0) throw new Error(`thread send 失败：${r.out}`);
	return before;
}

async function say(tid: string, msg: string, timeoutMs = 300_000) {
	const before = await sendCli(tid, msg);
	const t = await waitTurn(tid, before, timeoutMs);
	return { before, ...t };
}

/** 一次性 WS 命令（票据 → /api/v1/ws → cmd → res），与 `oryn thread send` 同路径；不发 focus。 */
async function wsCmd(
	tid: string,
	frame: Record<string, unknown>,
	timeoutMs = 300_000,
): Promise<Record<string, unknown>> {
	const tk = await api("POST", "/api/v1/ws-ticket");
	const ticket = (tk.json as { ticket?: string })?.ticket;
	if (!ticket) throw new Error(`ws-ticket ${tk.status} ${tk.text}`);
	const url = `${ORIGIN.replace(/^http/, "ws")}/api/v1/ws?ticket=${encodeURIComponent(ticket)}`;
	const id = `m5-${RUN}-${Math.random().toString(36).slice(2, 8)}`;
	const { promise, resolve, reject } =
		Promise.withResolvers<Record<string, unknown>>();
	const ws = new WebSocket(url);
	const timer = setTimeout(() => {
		ws.close();
		reject(new Error(`ws cmd ${String(frame.type)} 超时`));
	}, timeoutMs);
	ws.onopen = () =>
		ws.send(JSON.stringify({ type: "cmd", threadId: tid, id, frame }));
	ws.onmessage = (ev) => {
		let m: Record<string, unknown>;
		try {
			m = JSON.parse(String(ev.data)) as Record<string, unknown>;
		} catch {
			return;
		}
		if (m.id !== id) return;
		clearTimeout(timer);
		ws.close();
		if (m.type === "res")
			resolve((m.response ?? {}) as Record<string, unknown>);
		else reject(new Error(`ws ${String(m.code)} ${String(m.message)}`));
	};
	ws.onerror = () => {
		clearTimeout(timer);
		reject(new Error("ws error"));
	};
	return promise;
}

// ---------------------------------------------------------------- 假服务

interface HookReq {
	at: number;
	headers: Record<string, string>;
	raw: string;
	json: {
		event?: string;
		id?: string;
		thread?: { id?: string; title?: string; state?: string; url?: string };
		title?: string;
		body?: string;
	};
	sigOk: boolean;
}
const HOOK_SECRET = `m5-secret-${RUN}`;
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
			at: Date.now(),
			headers: Object.fromEntries(r.headers.entries()),
			raw,
			json,
			sigOk: sig === want,
		});
		return new Response("ok");
	},
});
const HOOK_URL = `http://127.0.0.1:${hookSrv.port}/hook`;

interface SimpleReq {
	at: number;
	path: string;
	headers: Record<string, string>;
	json: Record<string, unknown>;
}
const ntfyReqs: SimpleReq[] = [];
let ntfyFail = false;
const ntfySrv = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(r: Request) {
		const raw = await r.text();
		let json: Record<string, unknown> = {};
		try {
			json = JSON.parse(raw) as Record<string, unknown>;
		} catch {
			// 非 JSON
		}
		ntfyReqs.push({
			at: Date.now(),
			path: new URL(r.url).pathname,
			headers: Object.fromEntries(r.headers.entries()),
			json,
		});
		return ntfyFail
			? new Response("m5 fake ntfy 500", { status: 500 })
			: Response.json({ id: "m5", event: "message" });
	},
});
const NTFY_URL = `http://127.0.0.1:${ntfySrv.port}`;

const barkReqs: SimpleReq[] = [];
const barkSrv = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(r: Request) {
		let json: Record<string, unknown> = {};
		try {
			json = (await r.json()) as Record<string, unknown>;
		} catch {
			// 非 JSON
		}
		barkReqs.push({
			at: Date.now(),
			path: new URL(r.url).pathname,
			headers: Object.fromEntries(r.headers.entries()),
			json,
		});
		return Response.json({ code: 200, message: "success" });
	},
});
const BARK_URL = `http://127.0.0.1:${barkSrv.port}`;

// 假 Web Push 端点：自持 P-256 密钥对与 auth secret
const uaKeys = (await crypto.subtle.generateKey(
	{ name: "ECDH", namedCurve: "P-256" },
	true,
	["deriveBits"],
)) as CryptoKeyPair;
const uaPub = new Uint8Array(
	await crypto.subtle.exportKey("raw", uaKeys.publicKey),
);
const uaAuth = new Uint8Array(randomBytes(16));
interface PushReq {
	at: number;
	path: string;
	headers: Record<string, string>;
	body: Bytes;
	status: number;
}
const pushReqs: PushReq[] = [];
let pushStatus = 201;
const pushSrv = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(r: Request) {
		const body = new Uint8Array(await r.arrayBuffer());
		pushReqs.push({
			at: Date.now(),
			path: new URL(r.url).pathname,
			headers: Object.fromEntries(r.headers.entries()),
			body,
			status: pushStatus,
		});
		return new Response(null, { status: pushStatus });
	},
});
const PUSH_ORIGIN = `http://127.0.0.1:${pushSrv.port}`;
const PUSH_ENDPOINT = `${PUSH_ORIGIN}/push/m5-${RUN}`;

/** WebCrypto 只收 ArrayBuffer 背书的视图（不收 SharedArrayBuffer）。 */
type Bytes = Uint8Array<ArrayBuffer>;

const concat = (...parts: Bytes[]): Bytes => {
	const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
	let o = 0;
	for (const p of parts) {
		out.set(p, o);
		o += p.length;
	}
	return out;
};

async function hkdf(salt: Bytes, ikm: Bytes, info: Bytes, len: number) {
	const key = await crypto.subtle.importKey("raw", ikm, "HKDF", false, [
		"deriveBits",
	]);
	return new Uint8Array(
		await crypto.subtle.deriveBits(
			{ name: "HKDF", hash: "SHA-256", salt, info },
			key,
			len * 8,
		),
	);
}

/** RFC 8291 aes128gcm 单记录解密：salt(16) ‖ rs(4) ‖ idlen(1) ‖ as_public ‖ 密文。 */
async function decryptPush(body: Bytes): Promise<string> {
	const salt = body.slice(0, 16);
	const idlen = body[20] ?? 0;
	const asPub = body.slice(21, 21 + idlen);
	const ct = body.slice(21 + idlen);
	const asKey = await crypto.subtle.importKey(
		"raw",
		asPub,
		{ name: "ECDH", namedCurve: "P-256" },
		false,
		[],
	);
	const shared = new Uint8Array(
		await crypto.subtle.deriveBits(
			{ name: "ECDH", public: asKey },
			uaKeys.privateKey,
			256,
		),
	);
	const ikm = await hkdf(
		uaAuth,
		shared,
		concat(te.encode("WebPush: info\0"), uaPub, asPub),
		32,
	);
	const cek = await hkdf(
		salt,
		ikm,
		te.encode("Content-Encoding: aes128gcm\0"),
		16,
	);
	const nonce = await hkdf(
		salt,
		ikm,
		te.encode("Content-Encoding: nonce\0"),
		12,
	);
	const k = await crypto.subtle.importKey("raw", cek, "AES-GCM", false, [
		"decrypt",
	]);
	const pt = new Uint8Array(
		await crypto.subtle.decrypt({ name: "AES-GCM", iv: nonce }, k, ct),
	);
	let end = pt.length;
	while (end > 0 && pt[end - 1] === 0) end--;
	if (pt[end - 1] !== 2) throw new Error("末记录分隔符不是 0x02");
	return td.decode(pt.slice(0, end - 1));
}

/** 校验 `vapid t=<JWT>, k=<公钥>`：签名（ES256，r‖s）、k 与 vapid-key 一致、aud / exp。 */
async function verifyVapid(authz: string, vapidKey: string, aud: string) {
	const m = /^vapid t=([^,\s]+),\s*k=(\S+)$/.exec(authz);
	if (!m) return { ok: false, why: `格式 ${authz.slice(0, 40)}` };
	const [, jwt = "", k = ""] = m;
	const [h = "", p = "", s = ""] = jwt.split(".");
	const key = await crypto.subtle.importKey(
		"raw",
		unb64u(vapidKey),
		{ name: "ECDSA", namedCurve: "P-256" },
		false,
		["verify"],
	);
	const sigOk = await crypto.subtle.verify(
		{ name: "ECDSA", hash: "SHA-256" },
		key,
		unb64u(s),
		te.encode(`${h}.${p}`),
	);
	const claims = JSON.parse(td.decode(unb64u(p))) as {
		aud?: string;
		exp?: number;
		sub?: string;
	};
	const expOk =
		typeof claims.exp === "number" &&
		claims.exp > Date.now() / 1000 &&
		claims.exp <= Date.now() / 1000 + 24 * 3600;
	return {
		ok: sigOk && k === vapidKey && claims.aud === aud && expOk,
		why: `sig=${sigOk} k一致=${k === vapidKey} aud=${claims.aud} exp=${claims.exp} sub=${claims.sub}`,
	};
}

// 假 Telegram Bot API
interface TgCall {
	at: number;
	method: string;
	body: Record<string, unknown>;
	messageId?: number;
}
const tg = {
	updates: [] as Record<string, unknown>[],
	calls: [] as TgCall[],
	polls: 0,
	nextUpdate: 1,
	nextMsg: 1000,
	waiters: new Set<() => void>(),
};
const TG_CHAT = 424242;
const TG_OTHER_CHAT = 515151;
const TG_TOKEN = `123456:M5-FAKE-${RUN}`;

function tgInject(u: Record<string, unknown>) {
	tg.updates.push({ update_id: tg.nextUpdate++, ...u });
	for (const w of tg.waiters) w();
	tg.waiters.clear();
}

const tgSrv = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	idleTimeout: 60,
	async fetch(r: Request) {
		const m = /^\/bot([^/]+)\/([A-Za-z]+)$/.exec(new URL(r.url).pathname);
		if (!m || m[1] !== TG_TOKEN)
			return Response.json(
				{ ok: false, error_code: 404, description: "Not Found" },
				{ status: 404 },
			);
		const method = m[2] ?? "";
		let body: Record<string, unknown> = {};
		try {
			body = (await r.json()) as Record<string, unknown>;
		} catch {
			// 空体
		}
		const ok = (result: unknown) => Response.json({ ok: true, result });
		switch (method) {
			case "getMe":
				return ok({
					id: 99,
					is_bot: true,
					first_name: "M5 Fake",
					username: "m5_fake_bot",
				});
			case "getUpdates": {
				tg.polls++;
				const offset = Number(body.offset ?? 0);
				const pending = () =>
					tg.updates.filter((u) => Number(u.update_id) >= offset);
				if (pending().length === 0) {
					const wait = Math.min(Number(body.timeout ?? 0), 8) * 1000;
					const { promise, resolve } = Promise.withResolvers<void>();
					tg.waiters.add(resolve);
					setTimeout(resolve, wait);
					await promise;
				}
				return ok(pending());
			}
			case "sendMessage": {
				const id = tg.nextMsg++;
				tg.calls.push({ at: Date.now(), method, body, messageId: id });
				return ok({
					message_id: id,
					date: Math.floor(Date.now() / 1000),
					chat: { id: body.chat_id, type: "private" },
					text: body.text,
				});
			}
			case "answerCallbackQuery":
			case "editMessageReplyMarkup":
				tg.calls.push({ at: Date.now(), method, body });
				return ok(true);
			default:
				tg.calls.push({ at: Date.now(), method, body });
				return ok(true);
		}
	},
});
const TG_API = `http://127.0.0.1:${tgSrv.port}`;

const tgSends = (since = 0) =>
	tg.calls.filter((c) => c.method === "sendMessage" && c.at >= since);
const tgText = (c: TgCall) => String(c.body.text ?? "");

// ---------------------------------------------------------------- 渠道

interface Channel {
	id: string;
	name: string;
	kind: string;
	enabled: boolean;
	telegram?: { linked: boolean; botUsername: string; chatTitle?: string };
	lastError?: string;
}

async function channels(): Promise<Channel[]> {
	const r = await api("GET", "/api/v1/notification-channels");
	if (r.status !== 200) throw new Error(`列渠道 ${r.status} ${r.text}`);
	return (r.json as { channels: Channel[] }).channels;
}

const madeChannels: string[] = [];
const chIds: Record<string, string> = {};

async function ensureChannel(
	kind: "webhook" | "ntfy" | "bark" | "telegram",
): Promise<string> {
	const have = chIds[kind];
	if (have) return have;
	const name = `${CH_PREFIX}-${kind}-${RUN}`;
	const cfg = {
		webhook: { webhook: { url: HOOK_URL, secret: HOOK_SECRET } },
		ntfy: { ntfy: { server: NTFY_URL, topic: `m5-${RUN}` } },
		bark: { bark: { server: BARK_URL, deviceKey: `m5-device-${RUN}` } },
		telegram: { telegram: { botToken: TG_TOKEN, apiBase: TG_API } },
	}[kind];
	const r = await api("POST", "/api/v1/notification-channels", {
		kind,
		name,
		locale: "zh-CN",
		...cfg,
	});
	if (r.status !== 201)
		throw new Error(`建 ${kind} 渠道 ${r.status} ${r.text}`);
	const id = (r.json as Channel).id;
	madeChannels.push(id);
	chIds[kind] = id;
	note(`渠道 ${kind} ${id}（${name}）`);
	return id;
}

let tgLinked = false;

async function ensureTelegramLinked(): Promise<string> {
	const id = await ensureChannel("telegram");
	if (tgLinked) return id;
	const l = await api(
		"POST",
		`/api/v1/notification-channels/${id}/telegram/link`,
	);
	const code = (l.json as { code?: string })?.code;
	check(l.status === 200 && !!code, `绑定码 ${code}（${l.status}）`);
	const t0 = Date.now();
	tgInject({
		message: {
			message_id: 1,
			date: Math.floor(Date.now() / 1000),
			chat: { id: TG_CHAT, type: "private", first_name: "M5" },
			from: { id: TG_CHAT, is_bot: false, first_name: "M5" },
			text: `/start ${code}`,
		},
	});
	const ch = await waitFor(
		"Telegram 渠道已绑定",
		async () =>
			(await channels()).find((c) => c.id === id && c.telegram?.linked),
		30_000,
		500,
	);
	note(`telegram=${JSON.stringify(ch.telegram)}`);
	const linkedMsg = await waitFor(
		"假 API 收到「已绑定」回复",
		() =>
			tgSends(t0).find(
				(c) => c.body.chat_id === TG_CHAT && tgText(c).includes("已绑定"),
			),
		15_000,
	);
	note(`回复「${tgText(linkedMsg)}」；getUpdates 调用 ${tg.polls} 次`);
	tgLinked = true;
	return id;
}

// ---------------------------------------------------------------- 浏览器

let ctxA: BrowserContext;
let ctxC: BrowserContext;
let pageA: Page | undefined;
const pageErrors: string[] = [];

function wire(page: Page, tag: string) {
	page.on("pageerror", (e) => {
		pageErrors.push(`[${tag}] ${e.message}`);
		console.log(`    [${tag} pageerror] ${e.message}`);
	});
	return page;
}

async function mainPage(): Promise<Page> {
	if (pageA && !pageA.isClosed()) return pageA;
	pageA = wire(await ctxA.newPage(), "A");
	return pageA;
}

/** A 只留主页面并停在首页（没有任何 A 页面 focus 某线程）。 */
async function homeA(): Promise<Page> {
	const page = await mainPage();
	for (const p of ctxA.pages()) if (p !== page) await p.close();
	await page.goto(`${BASE}/`);
	await page
		.getByRole("button", { name: /^通知/ })
		.first()
		.waitFor({ timeout: 30_000 })
		.catch(() => {});
	return page;
}

async function openThread(page: Page, tid: string) {
	await page.goto(`${BASE}/t/${tid}`);
	await page
		.getByLabel("消息", { exact: true })
		.waitFor({ timeout: 60_000 })
		.catch(() => {});
}

async function openShareDialog(page: Page) {
	await page.getByRole("button", { name: "更多操作" }).first().click();
	await page.getByRole("menuitem", { name: "分享…" }).click();
	const dialog = page.getByRole("dialog", { name: "分享线程" });
	await dialog.waitFor({ timeout: 15_000 });
	return dialog;
}

interface Share {
	id: string;
	threadId: string;
	mode: "live" | "snapshot";
	slug: string;
	url: string;
	createdAt: number;
	findings: number;
	untilSeq?: number;
}

async function threadShares(tid: string): Promise<Share[]> {
	const r = await api("GET", `/api/v1/threads/${tid}/shares`);
	if (r.status !== 200) throw new Error(`列分享 ${r.status} ${r.text}`);
	return (r.json as { shares: Share[] }).shares;
}

/** 在分享对话框里新建一条链接，返回接口里新出现的那条。 */
async function createShareUI(
	page: Page,
	tid: string,
	mode: "live" | "snapshot",
): Promise<Share> {
	const before = new Set((await threadShares(tid)).map((s) => s.id));
	const dialog = await openShareDialog(page);
	await dialog
		.locator("label", {
			hasText: mode === "snapshot" ? "冻结在当前内容" : "之后的新内容也会公开",
		})
		.click();
	await dialog.getByRole("button", { name: "创建链接" }).click();
	await dialog.getByText("已创建，链接已复制").waitFor({ timeout: 30_000 });
	await shot(page, `share-dialog-${mode}-${RUN}`);
	await page.keyboard.press("Escape");
	const s = await waitFor(
		"接口里出现新分享",
		async () =>
			(await threadShares(tid)).find(
				(x) => !before.has(x.id) && x.mode === mode,
			),
		10_000,
	);
	note(`新${mode === "snapshot" ? "快照" : "实时"}分享 ${s.id} ${s.url}`);
	return s;
}

async function createShareApi(
	tid: string,
	mode: "live" | "snapshot",
): Promise<Share> {
	const r = await api("POST", `/api/v1/threads/${tid}/shares`, {
		mode,
		acknowledgeFindings: true,
	});
	if (r.status !== 201) throw new Error(`建分享 ${r.status} ${r.text}`);
	return r.json as Share;
}

/** 记录页面上公开 WebSocket 的消息类型、帧类型与关闭。 */
interface PubWs {
	types: string[];
	frameTypes: string[];
	opened: number;
	closed: boolean;
	closedAt: number;
}
function tapPublicWs(page: Page): PubWs {
	const st: PubWs = {
		types: [],
		frameTypes: [],
		opened: 0,
		closed: false,
		closedAt: 0,
	};
	page.on("websocket", (ws: PwWebSocket) => {
		if (!ws.url().includes("/api/v1/public/shares/")) return;
		st.opened++;
		st.closed = false;
		ws.on("framereceived", (f) => {
			const p = typeof f.payload === "string" ? f.payload : "";
			try {
				const m = JSON.parse(p) as {
					type?: string;
					frame?: { type?: string };
				};
				st.types.push(String(m.type));
				if (m.type === "frame") st.frameTypes.push(String(m.frame?.type));
			} catch {
				st.types.push("<非 JSON>");
			}
		});
		ws.on("close", () => {
			st.closed = true;
			st.closedAt = Date.now();
		});
	});
	return st;
}

/** 记录页面发出的请求路径。 */
function tapRequests(page: Page): string[] {
	const out: string[] = [];
	page.on("request", (r) => out.push(new URL(r.url()).pathname));
	return out;
}

const lastAssistantText = (page: Page) =>
	page.evaluate(() => {
		const els = [...document.querySelectorAll('[data-role="assistant"]')];
		// biome-ignore lint/performance/useDomNodeTextContent: 要按渲染出的换行拆行（数到 20 的逐行比对）
		return (els.at(-1) as HTMLElement | undefined)?.innerText ?? "";
	});

async function makePng(label: string, color: string): Promise<Uint8Array> {
	const p = await ctxC.newPage();
	try {
		await p.setViewportSize({ width: 320, height: 160 });
		await p.setContent(
			`<body style="margin:0;background:${color};font:28px sans-serif;color:#fff;display:grid;place-items:center;height:160px">${label}</body>`,
		);
		return new Uint8Array(
			await p.screenshot({ clip: { x: 0, y: 0, width: 320, height: 160 } }),
		);
	} finally {
		await p.close();
	}
}

// ---------------------------------------------------------------- 场景

let SNAP: Share | undefined;
let LIVE: Share | undefined;
let IMG_SHA = "";
let pageCLive: Page | undefined;
let pubLive: PubWs | undefined;

const blobsIn = (text: string) =>
	[...text.matchAll(/blob:sha256:([0-9a-f]{64})/g)].map((m) => m[1] ?? "");

async function N1() {
	const orb = await ensureAwake(THREAD);
	note(`thread=${THREAD} orb=${orb}`);
	const png = await makePng(`M5 ${RUN}`, "#3b5bdb");
	const before = seqOf(THREAD);
	const res = await wsCmd(THREAD, {
		type: "prompt",
		message: "数到 5",
		images: [
			{
				type: "image",
				data: Buffer.from(png).toString("base64"),
				mimeType: "image/png",
			},
		],
	});
	check(
		res.success === true,
		`prompt（带图）应答 success（${JSON.stringify(res).slice(0, 120)}）`,
	);
	await waitTurn(THREAD, before);
	const rows = sql<{ p: string }>(
		`select cast(payload as text) p from entries where thread_id='${THREAD}' and seq>${before}`,
	);
	IMG_SHA = rows.flatMap((r) => blobsIn(r.p))[0] ?? "";
	check(!!IMG_SHA, `新条目引用了图片 blob ${IMG_SHA.slice(0, 12)}…`);

	const page = await mainPage();
	await openThread(page, THREAD);
	const seqAtCreate = seqOf(THREAD);
	SNAP = await createShareUI(page, THREAD, "snapshot");
	const row = sql<{ until_seq: number }>(
		`select until_seq from thread_shares where id='${SNAP.id}'`,
	)[0];
	check(
		row?.until_seq === seqAtCreate && seqOf(THREAD) === seqAtCreate,
		`thread_shares.until_seq=${row?.until_seq} 等于创建时 threads.last_seq=${seqAtCreate}`,
	);

	const pageC = wire(await ctxC.newPage(), "C1");
	try {
		const resp = await pageC.goto(`${BASE}/s/${SNAP.slug}`);
		const robots = (await resp?.allHeaders())?.["x-robots-tag"] ?? "";
		check(
			resp?.status() === 200 && robots.includes("noindex, nofollow"),
			`文档 ${resp?.status()}，X-Robots-Tag「${robots}」`,
		);
		await pageC.getByText("数到 5").first().waitFor({ timeout: 30_000 });
		const img = pageC.locator(`img[src*="${IMG_SHA}"]`).first();
		await img.waitFor({ timeout: 30_000 });
		const nw = await waitFor(
			"图片加载完成",
			() =>
				img.evaluate((el) =>
					(el as HTMLImageElement).complete
						? (el as HTMLImageElement).naturalWidth
						: 0,
				),
			15_000,
		);
		check(nw > 0, `图片 naturalWidth=${nw}`);
		const meta = await pageC.locator('meta[name="robots"]').count();
		check(meta > 0, `DOM 有 meta[name=robots]（${meta}）`);
		const inputs = await pageC
			.locator("textarea, [contenteditable=true]")
			.count();
		check(inputs === 0, `没有输入框（${inputs}）`);
		await shot(pageC, "N1-snapshot");

		// 公开范围 ⊇ 创建时活动分支上的全部消息
		const priv = (await privateEntries(THREAD)).entries.filter(
			(e) => e.seq <= seqAtCreate,
		);
		const leaf = threadRow(THREAD)?.leaf_entry_id ?? null;
		const branch = branchIds(priv, leaf);
		const pub = (await publicEntries(SNAP.slug)).entries;
		const pubIds = new Set(pub.map((e) => e.entryId));
		const missing = priv.filter(
			(e) => branch.has(e.entryId) && msgOf(e) && !pubIds.has(e.entryId),
		);
		check(
			missing.length === 0 && pub.every((e) => e.seq <= seqAtCreate),
			`公开条目 ${pub.length} 条覆盖活动分支 ${branch.size} 条中的全部消息（缺 ${missing.length}），且 seq ≤ ${seqAtCreate}`,
		);

		await say(THREAD, "数到 3");
		await pageC.reload();
		await pageC.getByText("数到 5").first().waitFor({ timeout: 30_000 });
		const txt = await pageC.evaluate(() => document.body.textContent ?? "");
		check(!txt.includes("数到 3"), "刷新后没有「数到 3」那一轮");
		const pub2 = (await publicEntries(SNAP.slug)).entries;
		check(
			pub2.length === pub.length &&
				!pub2.some((e) => msgOf(e)?.text.includes("数到 3")),
			`快照条目数不变（${pub2.length}）`,
		);
		await shot(pageC, "N1-after-refresh");
	} finally {
		await pageC.close();
	}
}

async function ensureSnapshot(): Promise<Share> {
	if (SNAP) return SNAP;
	SNAP = await createShareApi(THREAD, "snapshot");
	note(`现建快照分享 ${SNAP.slug}`);
	return SNAP;
}

function ensureImgSha(): string {
	if (IMG_SHA) return IMG_SHA;
	const rows = sql<{ p: string }>(
		`select cast(payload as text) p from entries where thread_id='${THREAD}' and instr(cast(payload as text),'blob:sha256:')>0 order by seq desc limit 5`,
	);
	IMG_SHA = rows.flatMap((r) => blobsIn(r.p))[0] ?? "";
	if (!IMG_SHA) throw new Error("线程里没有图片 blob（先跑 N1）");
	return IMG_SHA;
}

const ALLOWED_PUB = new Set([
	"share.snapshot",
	"entries",
	"frame",
	"live.reset",
	"transcript.reset",
	"share.state",
	"pong",
]);

async function N2() {
	await ensureAwake(THREAD);
	const page = await mainPage();
	await openThread(page, THREAD);
	LIVE = await createShareUI(page, THREAD, "live");
	const pageC = wire(await ctxC.newPage(), "C2");
	pageCLive = pageC;
	pubLive = tapPublicWs(pageC);
	const reqs = tapRequests(pageC);
	await pageC.goto(`${BASE}/s/${LIVE.slug}`);
	const pub = pubLive;
	await waitFor(
		"匿名 WS 收到 share.snapshot",
		() => pub.types.includes("share.snapshot"),
		30_000,
	);
	const before = await sendCli(THREAD, "数到 20，每个数字一行");
	const updatesBefore = () =>
		pub.frameTypes.filter((t) => t === "message_update").length;
	await waitFor(
		"C 收到 message_update 帧（流式）",
		() => updatesBefore() > 0,
		180_000,
	);
	const streamingText = await lastAssistantText(pageC);
	note(
		`流式中 C 的末条助手文本：「${streamingText.slice(0, 60).replace(/\n/g, " ")}」`,
	);
	await waitTurn(THREAD, before);
	const final = await waitFor(
		"C 不刷新出现完整的 1..20",
		async () => {
			const t = await lastAssistantText(pageC);
			const lines = t.split("\n").map((l) => l.trim());
			return lines.includes("19") && lines.includes("20") ? t : undefined;
		},
		60_000,
		500,
	);
	note(`C 末条助手文本 ${final.split("\n").length} 行`);
	check(pub.opened === 1, `整个过程只连了 ${pub.opened} 次匿名 WS（未刷新）`);
	const types = [...new Set(pub.types)];
	check(
		types.every((t) => ALLOWED_PUB.has(t)),
		`WS 消息类型 {${types.join(", ")}} ⊆ 允许集合`,
	);
	const ft = [...new Set(pub.frameTypes)];
	check(
		!ft.includes("extension_ui_request"),
		`frame 类型 {${ft.join(", ")}} 不含 extension_ui_request`,
	);
	const bad = reqs.filter(
		(p) =>
			p === "/api/v1/ws-ticket" || p === "/api/v1/me" || p === "/api/v1/ws",
	);
	check(bad.length === 0, `C 没有请求私有接口（${bad.join(", ") || "无"}）`);
	await shot(pageC, "N2-live");
}

async function N3() {
	const sha = ensureImgSha();
	if (!LIVE || !pageCLive || pageCLive.isClosed() || !pubLive) {
		LIVE = await createShareApi(THREAD, "live");
		pageCLive = wire(await ctxC.newPage(), "C3");
		pubLive = tapPublicWs(pageCLive);
		await pageCLive.goto(`${BASE}/s/${LIVE.slug}`);
		const p = pubLive;
		await waitFor(
			"匿名 WS 打开",
			() => p.types.includes("share.snapshot"),
			30_000,
		);
	}
	const live = LIVE;
	const pageC = pageCLive;
	const pub = pubLive;
	const imgPath = `/api/v1/public/shares/${live.slug}/blobs/${sha}`;
	const img0 = await anon(imgPath);
	check(img0.status === 200, `撤销前图片 ${img0.status}`);
	const page = await mainPage();
	await openThread(page, THREAD);
	const dialog = await openShareDialog(page);
	const row = dialog
		.locator("li")
		.filter({ has: page.locator(`[title="${live.url}"]`) });
	await row.getByRole("button", { name: "撤销", exact: true }).click();
	const t0 = Date.now();
	await row.getByRole("button", { name: "确认撤销" }).click();
	await waitFor(
		"C 显示已撤销且 WS 关闭",
		async () =>
			pub.closed &&
			(await pageC
				.getByText("链接不存在或已被撤销")
				.isVisible()
				.catch(() => false)),
		10_000,
		100,
	);
	const dt = Date.now() - t0;
	check(dt <= 2_000, `撤销后 ${dt}ms 内 C 显示「已被撤销」且 WS 关闭`);
	note(`WS 收到 share.revoked：${pub.types.includes("share.revoked")}`);
	await shot(pageC, "N3-revoked");
	await page.keyboard.press("Escape");
	for (const [what, path] of [
		["/s/<slug>", `/s/${live.slug}`],
		["public/shares/<slug>", `/api/v1/public/shares/${live.slug}`],
		["/s/<slug>.md", `/s/${live.slug}.md`],
		["图片", imgPath],
	] as const) {
		const r = await anon(path);
		check(r.status === 404, `${what} → ${r.status}`);
	}
	await pageC.close();
	pageCLive = undefined;
}

async function N4() {
	const snap = await ensureSnapshot();
	const r = await anon(`/s/${snap.slug}.md`);
	const ct = r.headers.get("content-type") ?? "";
	const robots = r.headers.get("x-robots-tag") ?? "";
	note(`status=${r.status} content-type=${ct} x-robots-tag=${robots}`);
	writeFileSync(`${SHOTS}/N4-${RUN}.md`, r.text);
	note(`正文存 ${SHOTS}/N4-${RUN}.md（${r.text.length} 字符）`);
	check(r.status === 200, "200");
	check(ct.startsWith("text/markdown"), "Content-Type text/markdown");
	check(robots.includes("noindex"), "带 noindex 头");
	check(/^# /m.test(r.text), "含 `# ` 标题");
	check(r.text.includes("## User"), "含 `## User`");
	check(r.text.includes("## Assistant"), "含 `## Assistant`");
	check(r.text.includes("数到 5"), "含「数到 5」");
}

async function N5() {
	const orb = await ensureAwake(THREAD);
	const r = await api("POST", "/api/v1/threads", {
		prompt: `原样输出这一行：${SECRET_LINE}`,
		executor: { kind: "orb", orbId: orb },
		title: `m5 secrets ${RUN}`,
	});
	check(r.status === 201, `建线程 T2（挂 ${orb}）${r.status}`);
	const t2 = threadIdOf(r.json);
	note(`T2=${t2}`);
	await waitTurn(t2, 0, 300_000);
	const c = await api("POST", `/api/v1/threads/${t2}/shares`, {
		mode: "snapshot",
	});
	const body = c.json as {
		code?: string;
		scan?: { findings: { rule: string; preview: string; count: number }[] };
	};
	note(`POST shares → ${c.status} ${c.text.slice(0, 300)}`);
	check(c.status === 409 && body.code === "secrets_found", "409 secrets_found");
	const f = body.scan?.findings.find((x) => x.rule === "api_key_sk");
	check(!!f, `findings 含 api_key_sk（preview「${f?.preview}」）`);
	check(
		!c.text.includes(SECRET_LINE),
		"响应全文不含完整密钥（preview 已脱敏）",
	);

	const page = await mainPage();
	await openThread(page, t2);
	const dialog = await openShareDialog(page);
	await dialog.locator("label", { hasText: "冻结在当前内容" }).click();
	await dialog.getByRole("button", { name: "创建链接" }).click();
	const findings = dialog.locator("[data-testid=share-findings]");
	await findings.waitFor({ timeout: 15_000 });
	const ft = (await findings.textContent()) ?? "";
	note(`命中区：「${ft.slice(0, 200).replace(/\n/g, " | ")}」`);
	check(ft.includes("API 密钥（sk-）"), "列出 api_key_sk 的本地化规则名");
	const anyway = dialog.getByRole("button", { name: "仍然创建" });
	check(await anyway.isDisabled(), "未勾选时「仍然创建」禁用");
	await shot(page, "N5-findings");
	await dialog.locator("label", { hasText: "我已检查，仍然分享" }).click();
	check(await anyway.isEnabled(), "勾选后可点");
	await anyway.click();
	await dialog.getByText("已创建，链接已复制").waitFor({ timeout: 15_000 });
	await page.keyboard.press("Escape");
	const row = await waitFor(
		"thread_shares 有 T2 的分享",
		() =>
			sql<{ findings: number; slug: string }>(
				`select findings, slug from thread_shares where thread_id='${t2}' and revoked_at is null order by created_at desc limit 1`,
			)[0],
		10_000,
	);
	check(row.findings >= 1, `thread_shares.findings=${row.findings}`);
}

async function N6() {
	const snap = await ensureSnapshot();
	const sha = ensureImgSha();
	const meta = await anon(`/api/v1/public/shares/${snap.slug}`);
	check(meta.status === 200, `public/shares/<slug> ${meta.status}`);
	const { entries, raw } = await publicEntries(snap.slug);
	const all = meta.text + raw.join("");
	check(
		!all.includes(THREAD),
		`公开 JSON（${all.length} 字节）搜不到 ${THREAD}`,
	);
	check(!all.includes("orb-"), "搜不到 `orb-`");
	const sub = entries.filter((e) => e.file.includes("/"));
	check(sub.length === 0, `entries 里没有子 agent 文件（${sub.length}）`);
	const t = await ctxC.request.get(`${BASE}/api/v1/threads/${THREAD}`);
	check(t.status() === 401, `C GET /api/v1/threads/$T → ${t.status()}`);
	const b = await ctxC.request.get(`${BASE}/api/v1/blobs/${sha}`);
	check(b.status() === 401, `C GET /api/v1/blobs/<sha> → ${b.status()}`);
	const own = await anon(`/api/v1/public/shares/${snap.slug}/blobs/${sha}`);
	check(own.status === 200, `范围内图片 ${own.status}（对照）`);
	const mine = new Set(
		sql<{ p: string }>(
			`select cast(payload as text) p from entries where thread_id='${THREAD}' and instr(cast(payload as text),'blob:sha256:')>0`,
		).flatMap((r) => blobsIn(r.p)),
	);
	const other = sql<{ p: string }>(
		`select cast(payload as text) p from entries where thread_id!='${THREAD}' and instr(cast(payload as text),'blob:sha256:')>0 limit 50`,
	)
		.flatMap((r) => blobsIn(r.p))
		.find((s) => !mine.has(s));
	if (other) {
		const o = await anon(`/api/v1/public/shares/${snap.slug}/blobs/${other}`);
		check(
			o.status === 404,
			`别的线程的图片 ${other.slice(0, 12)}… → ${o.status}`,
		);
	} else note("库里没有别的线程的图片，跳过该项");

	// 旁支：跳回较早消息 → 发 ZZ-BRANCH-MARK → 跳回原叶子 → 新快照不含它
	await ensureAwake(THREAD);
	const priv = (await privateEntries(THREAD)).entries.filter(isMain);
	const early = priv.find((e) => msgOf(e)?.role === "assistant");
	const origLeaf = threadRow(THREAD)?.leaf_entry_id ?? "";
	check(
		!!early && !!origLeaf,
		`较早的助手消息 ${early?.entryId}，原叶子 ${origLeaf}`,
	);
	const mark = `ZZ-BRANCH-MARK-${RUN}`;
	const sw1 = await wsCmd(THREAD, {
		type: "switch_leaf",
		entryId: early?.entryId,
		summarize: false,
	});
	check(
		sw1.success === true,
		`switch_leaf → 较早消息 ${JSON.stringify(sw1).slice(0, 120)}`,
	);
	const before = seqOf(THREAD);
	const pr = await wsCmd(THREAD, {
		type: "prompt",
		message: `只回复 ${mark}`,
	});
	check(pr.success === true, "旁支 prompt 已接受");
	await waitTurn(THREAD, before);
	const privAll = (await privateEntries(THREAD)).entries;
	check(
		privAll.some((e) => msgOf(e)?.text.includes(mark)),
		"私有条目里有旁支消息（对照）",
	);
	const sw2 = await wsCmd(THREAD, {
		type: "switch_leaf",
		entryId: origLeaf,
		summarize: false,
	});
	check(sw2.success === true, "switch_leaf → 原叶子");
	await waitFor(
		"threads.leaf_entry_id 回到原叶子",
		() => threadRow(THREAD)?.leaf_entry_id === origLeaf,
		15_000,
	);
	const snap2 = await createShareApi(THREAD, "snapshot");
	const p2 = await publicEntries(snap2.slug);
	check(
		!p2.raw.join("").includes(mark),
		`新快照 ${snap2.slug.slice(0, 6)}… 的 ${p2.entries.length} 条公开条目搜不到 ${mark}`,
	);
}

interface Attachment {
	id: string;
	name: string;
	sha256: string;
	publicUrl?: string;
}
interface PublishedFile {
	id: string;
	attachmentId: string;
	name: string;
	url: string;
}

async function uploadAttachment(name: string, content: string) {
	const r = await req(
		"POST",
		`/api/v1/attachments?name=${encodeURIComponent(name)}&threadId=${THREAD}`,
		{
			headers: {
				Authorization: `Bearer ${TOKEN}`,
				"X-Oryn-CSRF": "1",
				"Content-Type": "application/octet-stream",
			},
			body: content,
		},
	);
	if (r.status !== 201) throw new Error(`上传 ${name} ${r.status} ${r.text}`);
	return r.json as Attachment;
}

async function publishedFiles(): Promise<PublishedFile[]> {
	const r = await api("GET", "/api/v1/shares");
	return (r.json as { files: PublishedFile[] }).files;
}

function checkPublicFileHeaders(what: string, h: Headers) {
	check(
		(h.get("x-content-type-options") ?? "") === "nosniff",
		`${what} nosniff`,
	);
	check(
		(h.get("content-security-policy") ?? "").includes("sandbox"),
		`${what} CSP「${h.get("content-security-policy")}」`,
	);
	check(
		(h.get("x-robots-tag") ?? "").includes("noindex"),
		`${what} X-Robots-Tag「${h.get("x-robots-tag")}」`,
	);
}

async function N7() {
	const orb = await ensureAwake(THREAD);
	const attName = `m5-pub-${RUN}.txt`;
	const attBody = `m5 attachment ${RUN}\n`;
	const att = await uploadAttachment(attName, attBody);
	const priv = await uploadAttachment(`m5-private-${RUN}.txt`, "private\n");
	note(`附件 ${att.id}（发布）与 ${priv.id}（不发布）`);

	const page = await mainPage();
	await openThread(page, THREAD);
	await page.getByRole("button", { name: "更多操作" }).first().click();
	await page.getByRole("menuitem", { name: "附件" }).click();
	const dialog = page.getByRole("dialog").filter({ hasText: "附件" });
	const row = dialog.locator("li", { hasText: attName });
	await row.getByRole("button", { name: "发布", exact: true }).click();
	await row.getByText("已公开").waitFor({ timeout: 15_000 });
	await shot(page, "N7-attachment-published");
	await page.keyboard.press("Escape");
	const list = (await api("GET", `/api/v1/threads/${THREAD}/attachments`))
		.json as { attachments: Attachment[] };
	const u1 = list.attachments.find((a) => a.id === att.id)?.publicUrl ?? "";
	check(!!u1, `附件 publicUrl ${u1}`);

	const orbName = `m5-orbfile-${RUN}.txt`;
	const orbPath = `${REPO}/${orbName}`;
	const orbBody = `orb file v1 ${RUN}\n`;
	dwrite(orb, orbPath, orbBody);
	await page.locator("[data-panel-tab=files]").click();
	const item = page.locator(`[role=treeitem][data-path$="${orbName}"]`);
	await waitFor(
		"Files 面板列出新文件",
		async () => {
			if (await item.isVisible().catch(() => false)) return true;
			// 目录列表可能缓存：切走再切回刷新
			await page
				.locator("[data-panel-tab=changes]")
				.click()
				.catch(() => {});
			await page
				.locator("[data-panel-tab=files]")
				.click()
				.catch(() => {});
			return false;
		},
		60_000,
		2_000,
	);
	await item.click({ button: "right" });
	await page.getByRole("menuitem", { name: "发布为公开链接" }).click();
	await page
		.getByRole("status")
		.filter({ hasText: "已复制公开链接" })
		.waitFor({ timeout: 60_000 });
	await shot(page, "N7-orbfile-published");
	const f2 = await waitFor(
		"shares 总览里有 orb 文件",
		async () => (await publishedFiles()).find((f) => f.name === orbName),
		15_000,
	);
	const u2 = f2.url;
	note(`orb 文件公开 URL ${u2}`);

	for (const [what, url, want] of [
		["附件", u1, attBody],
		["orb 文件", u2, orbBody],
	] as const) {
		const r = await req("GET", url);
		check(r.status === 200, `${what} URL ${r.status}`);
		check(
			sha256(r.bytes) === sha256(want),
			`${what} sha256 ${sha256(r.bytes).slice(0, 12)}… 与源一致`,
		);
		checkPublicFileHeaders(what, r.headers);
	}
	const pubA = (await publishedFiles()).find((f) => f.attachmentId === att.id);
	const del = await api("DELETE", `/api/v1/published-files/${pubA?.id}`);
	check(del.status === 204, `撤回附件发布 ${del.status}`);
	const gone = await req("GET", u1);
	check(gone.status === 404, `撤回后附件 URL ${gone.status}`);
	const p = await anon(`/api/v1/attachments/${priv.id}/content`);
	check(p.status === 401, `未发布附件匿名 content → ${p.status}`);
	dwrite(orb, orbPath, `orb file v2 ${RUN}\n`);
	const again = await req("GET", u2);
	check(
		again.status === 200 && sha256(again.bytes) === sha256(orbBody),
		"改动 orb 里的文件后公开 URL 内容不变",
	);
	dexec(orb, `rm -f ${orbPath}`);
}

const hooksFor = (tid: string, since: number, event = "thread.done") =>
	hooks.filter(
		(h) => h.at >= since && h.json.event === event && h.json.thread?.id === tid,
	);

async function N8() {
	await ensureChannel("webhook");
	await ensureAwake(THREAD);
	const page = await homeA();

	const t0 = Date.now();
	const r1 = await say(THREAD, "数到 3");
	const h1 = await waitFor(
		"Webhook 收到 thread.done",
		() => hooksFor(THREAD, t0)[0],
		30_000,
	);
	const delay = h1.at - r1.at;
	note(
		`回合结束 → Webhook ${delay}ms；event=${h1.json.event} url=${h1.json.thread?.url}`,
	);
	check(delay >= 1_000 && delay <= 10_000, `约 3s 后到达（${delay}ms）`);
	check(
		(h1.json.thread?.url ?? "").endsWith(`/t/${THREAD}`),
		"thread.url 指向 $T",
	);
	check(
		h1.sigOk,
		`X-Oryn-Signature 用 secret 重算一致（${h1.headers["x-oryn-signature"]?.slice(0, 20)}…）`,
	);
	check(
		h1.headers["x-oryn-event"] === "thread.done",
		"X-Oryn-Event thread.done",
	);
	await sleep(Math.max(0, r1.at + 12_000 - Date.now()));
	check(
		hooksFor(THREAD, t0).length === 1,
		`只收到一次（${hooksFor(THREAD, t0).length}）`,
	);

	await openThread(page, THREAD);
	const t1 = Date.now();
	const r2 = await say(THREAD, "数到 3");
	await sleep(10_000);
	check(
		hooksFor(THREAD, t1).length === 0 && r2.state === "idle",
		`线程页 focus 时回合结束后 10s 内没有请求（${hooksFor(THREAD, t1).length}）`,
	);

	await homeA();
	const t2 = Date.now();
	await say(THREAD, "数到 3");
	const h3 = await waitFor(
		"回首页后又收到 thread.done",
		() => hooksFor(THREAD, t2)[0],
		30_000,
	);
	check(h3.sigOk, "第三次签名一致");
}

async function N9() {
	await ensureTelegramLinked();
	await ensureAwake(THREAD);
	await homeA();
	const t0 = Date.now();
	await sendCli(
		THREAD,
		"用 ask 工具问我：选 A 还是 B（只给 A 和 B 两个选项）。拿到回答后只回复我选的那个字母。",
	);
	await waitFor(
		"线程进入 awaiting_input",
		() => stateOf(THREAD) === "awaiting_input",
		240_000,
		500,
	);
	const ask = await waitFor(
		"假 API 收到带按钮的待答消息",
		() =>
			tgSends(t0).find(
				(c) =>
					tgText(c).startsWith("❓") &&
					tgText(c).includes(`/t/${THREAD}`) &&
					!!(c.body.reply_markup as { inline_keyboard?: unknown } | undefined)
						?.inline_keyboard,
			),
		30_000,
	);
	const kb = (
		ask.body.reply_markup as {
			inline_keyboard: { text: string; callback_data: string }[][];
		}
	).inline_keyboard.flat();
	note(
		`sendMessage「${tgText(ask).replace(/\n/g, " ⏎ ").slice(0, 160)}」按钮 ${kb.map((b) => b.text).join(" / ")}`,
	);
	check(kb.length === 2, `inline_keyboard 两个按钮（${kb.length}）`);
	const second = kb[1];
	check(
		!!second && /^o:[^:]+:1$/.test(second.callback_data),
		`第 2 个按钮 data=${second?.callback_data}`,
	);
	const t1 = Date.now();
	tgInject({
		callback_query: {
			id: `cb-${RUN}`,
			from: { id: TG_CHAT, is_bot: false, first_name: "M5" },
			message: {
				message_id: ask.messageId,
				chat: { id: TG_CHAT, type: "private" },
				text: tgText(ask),
			},
			data: second?.callback_data,
		},
	});
	const acq = await waitFor(
		"answerCallbackQuery",
		() =>
			tg.calls.find(
				(c) =>
					c.at >= t1 &&
					c.method === "answerCallbackQuery" &&
					c.body.callback_query_id === `cb-${RUN}`,
			),
		30_000,
	);
	note(`answerCallbackQuery text=${String(acq.body.text)}`);
	const edit = await waitFor(
		"editMessageReplyMarkup 去掉按钮",
		() =>
			tg.calls.find(
				(c) =>
					c.at >= t1 &&
					c.method === "editMessageReplyMarkup" &&
					c.body.message_id === ask.messageId,
			),
		30_000,
	);
	const left = (
		edit.body.reply_markup as { inline_keyboard?: unknown[] } | undefined
	)?.inline_keyboard;
	check(
		Array.isArray(left) && left.length === 0,
		`按钮被清空 ${JSON.stringify(edit.body.reply_markup)}`,
	);
	await waitFor(
		"线程离开 awaiting_input 并完成",
		() => stateOf(THREAD) === "idle",
		240_000,
		500,
	);
	const priv = (await privateEntries(THREAD)).entries;
	const lastA = [...priv].reverse().find((e) => msgOf(e)?.role === "assistant");
	const reply = (lastA && msgOf(lastA)?.text) ?? "";
	note(`助手回复「${reply.slice(0, 80)}」`);
	check(/\bB\b/.test(reply), "回复体现选了 B");
	const page = await mainPage();
	await openThread(page, THREAD);
	await page
		.locator('[data-role="assistant"]')
		.last()
		.waitFor({ timeout: 30_000 });
	const cards = await page.locator("[role=dialog]").count();
	check(cards === 0, `线程页没有待答卡片（dialog ${cards}）`);
	await shot(page, "N9-answered");
	await homeA();
	N9_AT = t1;
}
let N9_AT = 0;

async function N10() {
	await ensureTelegramLinked();
	await ensureAwake(THREAD);
	await homeA();
	const isDone = (c: TgCall) =>
		tgText(c).startsWith("✅") && tgText(c).includes(`/t/${THREAD}`);
	let done = N9_AT
		? await waitFor(
				"N9 之后的 done 消息",
				() => tgSends(N9_AT).filter(isDone).at(-1),
				30_000,
			).catch(() => undefined)
		: undefined;
	if (!done) {
		const t = Date.now();
		await say(THREAD, "只回复 ok");
		done = await waitFor("done 消息", () => tgSends(t).find(isDone), 30_000);
	}
	note(
		`done 消息 message_id=${done.messageId}「${tgText(done).split("\n")[0]}」`,
	);
	const mark = `TG-STEER-OK-${RUN}`;
	const text = `再输出一行 ${mark}`;
	const countUser = async () =>
		(await privateEntries(THREAD)).entries.filter((e) => {
			const m = msgOf(e);
			return m?.role === "user" && m.text.includes(mark);
		}).length;
	const before = seqOf(THREAD);
	const t0 = Date.now();
	tgInject({
		message: {
			message_id: 50_000 + Math.floor(Math.random() * 1000),
			date: Math.floor(Date.now() / 1000),
			chat: { id: TG_CHAT, type: "private", first_name: "M5" },
			from: { id: TG_CHAT, is_bot: false, first_name: "M5" },
			text,
			reply_to_message: {
				message_id: done.messageId,
				chat: { id: TG_CHAT, type: "private" },
				text: tgText(done),
			},
		},
	});
	const sent = await waitFor(
		"假 API 收到「已发送」",
		() =>
			tgSends(t0).find(
				(c) => c.body.chat_id === TG_CHAT && tgText(c).startsWith("已发送"),
			),
		60_000,
	);
	note(`回复「${tgText(sent)}」`);
	await waitTurn(THREAD, before);
	check((await countUser()) === 1, "线程出现一条含 TG-STEER-OK 的用户消息");
	const priv = (await privateEntries(THREAD)).entries;
	const lastA = [...priv].reverse().find((e) => msgOf(e)?.role === "assistant");
	check(
		(lastA && msgOf(lastA)?.text.includes(mark)) === true,
		"并得到含该标记的回复",
	);
	const t1 = Date.now();
	tgInject({
		message: {
			message_id: 60_000,
			date: Math.floor(Date.now() / 1000),
			chat: { id: TG_OTHER_CHAT, type: "private", first_name: "Eve" },
			from: { id: TG_OTHER_CHAT, is_bot: false, first_name: "Eve" },
			text,
			reply_to_message: {
				message_id: done.messageId,
				chat: { id: TG_OTHER_CHAT, type: "private" },
			},
		},
	});
	await sleep(10_000);
	check((await countUser()) === 1, "另一 chat.id 注入后没有新的用户消息");
	check(
		tgSends(t1).every((c) => c.body.chat_id !== TG_OTHER_CHAT),
		"也没有回复那个 chat",
	);
}

async function N11() {
	await ensureAwake(THREAD);
	await homeA();
	pushStatus = 201;
	const vk = await api("GET", "/api/v1/push/vapid-key");
	const vapid = (vk.json as { publicKey?: string })?.publicKey ?? "";
	check(
		vk.status === 200 && unb64u(vapid).length === 65,
		`vapid-key ${vapid.slice(0, 16)}…`,
	);
	const c = await api("POST", "/api/v1/push/subscriptions", {
		endpoint: PUSH_ENDPOINT,
		keys: { p256dh: b64u(uaPub), auth: b64u(uaAuth) },
	});
	check(c.status === 201, `订阅 ${c.status} ${c.text.slice(0, 120)}`);
	const t0 = Date.now();
	await say(THREAD, "只回复 ok");
	const p = await waitFor(
		"假推送端点收到 POST",
		() => pushReqs.find((r) => r.at >= t0),
		30_000,
	);
	note(
		`headers ${JSON.stringify({ ce: p.headers["content-encoding"], ttl: p.headers.ttl, urgency: p.headers.urgency, topic: p.headers.topic })}`,
	);
	check(
		p.headers["content-encoding"] === "aes128gcm",
		"Content-Encoding: aes128gcm",
	);
	check(p.headers.ttl === "86400", `TTL ${p.headers.ttl}`);
	const v = await verifyVapid(
		p.headers.authorization ?? "",
		vapid,
		PUSH_ORIGIN,
	);
	note(`VAPID ${v.why}`);
	check(v.ok, "JWT 验签通过、aud = 假端点 origin、k = vapid-key");
	const payload = JSON.parse(await decryptPush(p.body)) as {
		title?: string;
		url?: string;
		kind?: string;
	};
	note(`解密 ${JSON.stringify(payload).slice(0, 200)}`);
	const title = threadRow(THREAD)?.title ?? "";
	check(payload.url === `/t/${THREAD}`, `url ${payload.url}`);
	check(
		payload.title === title,
		`title「${payload.title}」= 线程标题「${title}」`,
	);

	pushStatus = 410;
	const t1 = Date.now();
	await say(THREAD, "只回复 ok");
	await waitFor(
		"假端点回了 410",
		() => pushReqs.find((r) => r.at >= t1 && r.status === 410),
		30_000,
	);
	await waitFor(
		"push/subscriptions 不再列出它",
		async () => {
			const l = await api("GET", "/api/v1/push/subscriptions");
			return !(
				l.json as { subscriptions: { endpoint: string }[] }
			).subscriptions.some((s) => s.endpoint === PUSH_ENDPOINT);
		},
		15_000,
		500,
	);
	check(true, "410 之后订阅已删除");
}

async function N12() {
	const page = await homeA();
	const scope = await waitFor(
		"navigator.serviceWorker.ready",
		() =>
			page.evaluate(() =>
				Promise.race([
					navigator.serviceWorker.ready.then((r) => r.scope),
					new Promise<string>((res) => setTimeout(() => res(""), 2_000)),
				]),
			),
		30_000,
		500,
	);
	note(`SW scope ${scope}`);
	const sw = ctxA.serviceWorkers()[0];
	const cdp = await ctxA.newCDPSession(page);
	const regs = new Map<string, string>();
	cdp.on(
		"ServiceWorker.workerRegistrationUpdated",
		(e: {
			registrations: {
				registrationId: string;
				scopeURL: string;
				isDeleted: boolean;
			}[];
		}) => {
			for (const r of e.registrations)
				if (!r.isDeleted) regs.set(r.scopeURL, r.registrationId);
		},
	);
	await cdp.send("ServiceWorker.enable");
	// Playwright 默认对每个页面做焦点仿真（永远 focused）；关掉它，切到别的标签后本页才真正失焦
	await cdp.send("Emulation.setFocusEmulationEnabled", { enabled: false });
	const regId = await waitFor(
		"CDP 拿到 registrationId",
		() => regs.get(scope),
		10_000,
	);
	const blank = await ctxA.newPage();
	try {
		await blank.goto("about:blank");
		await blank.bringToFront();
		if (sw)
			note(
				`sw 看到的窗口 focused：${await sw.evaluate(() =>
					(self as unknown as ServiceWorkerGlobalScope).clients
						.matchAll({ type: "window", includeUncontrolled: true })
						.then((ws) => JSON.stringify(ws.map((w) => [w.url, w.focused]))),
				)}`,
			);
		await cdp.send("ServiceWorker.deliverPushMessage", {
			origin: ORIGIN,
			registrationId: regId,
			data: JSON.stringify({
				title: "t",
				body: "b",
				url: "/t/x",
				tag: "oryn-x",
			}),
		});
		await page.bringToFront();
		const ns = await waitFor(
			"registration.getNotifications() 有 1 条",
			async () => {
				const l = await page.evaluate(async () => {
					const r = await navigator.serviceWorker.ready;
					return (await r.getNotifications({ tag: "oryn-x" })).map((n) => ({
						title: n.title,
						tag: n.tag,
						url: (n.data as { url?: string } | null)?.url,
					}));
				});
				return l.length > 0 ? l : undefined;
			},
			10_000,
			300,
		);
		note(`notifications ${JSON.stringify(ns)}`);
		const n = ns[0];
		check(ns.length === 1, "恰好 1 条");
		check(
			n?.title === "t" && n.tag === "oryn-x" && n.url === "/t/x",
			"title / tag / data.url 正确",
		);
	} finally {
		await blank.close();
		await cdp.detach().catch(() => {});
	}
}

async function N13() {
	const ntfyId = await ensureChannel("ntfy");
	await ensureChannel("bark");
	ntfyFail = false;
	const page = await mainPage();
	for (const p of ctxA.pages()) if (p !== page) await p.close();
	await page.goto(`${BASE}/settings/notifications`);
	for (const [kind, reqs] of [
		["ntfy", ntfyReqs],
		["bark", barkReqs],
	] as const) {
		const name = `${CH_PREFIX}-${kind}-${RUN}`;
		const row = page.locator("li").filter({ hasText: name }).first();
		await row.waitFor({ timeout: 15_000 });
		const t = Date.now();
		await row.getByRole("button", { name: "测试", exact: true }).click();
		await row.getByText("测试消息已发送").waitFor({ timeout: 20_000 });
		const r = await waitFor(
			`假 ${kind} 收到测试`,
			() => reqs.find((x) => x.at >= t),
			10_000,
		);
		note(`${kind} ${r.path} ${JSON.stringify(r.json)}`);
		if (kind === "ntfy")
			check(
				["topic", "title", "message", "click", "tags", "priority"].every(
					(k) => k in r.json,
				),
				"ntfy 体含 topic/title/message/click/tags/priority",
			);
		else
			check(
				r.path === "/push" &&
					typeof r.json.device_key === "string" &&
					r.json.group === "Oryn",
				'Bark /push 含 device_key、group:"Oryn"',
			);
	}
	await shot(page, "N13-tests");

	ntfyFail = true;
	await ensureAwake(THREAD);
	await homeA();
	const t0 = Date.now();
	await say(THREAD, "数到 3");
	const row = await waitFor(
		"ntfy 投递 attempts=2 且 pending",
		() => {
			const r = sql<{
				status: string;
				attempts: number;
				next_attempt_at: number;
				last_error: string;
			}>(
				`select d.status, d.attempts, d.next_attempt_at, d.last_error from notification_deliveries d join notifications n on n.id=d.notification_id where n.thread_id='${THREAD}' and n.created_at>=${t0} and d.channel_id='${ntfyId}' order by n.created_at desc limit 1`,
			)[0];
			return r && r.attempts >= 2 ? { ...r, seenAt: Date.now() } : undefined;
		},
		60_000,
		250,
	);
	const ahead = row.next_attempt_at - row.seenAt;
	note(
		`投递 ${JSON.stringify(row)}；next_attempt_at 在 ${Math.round(ahead / 1000)}s 之后；假 ntfy 共 ${ntfyReqs.filter((r) => r.at >= t0).length} 次请求`,
	);
	check(
		row.attempts === 2 && row.status === "pending",
		`attempts=${row.attempts} status=${row.status}`,
	);
	check(
		(row.last_error ?? "").includes("500"),
		`last_error「${row.last_error}」含 500`,
	);
	check(ahead >= 40_000 && ahead <= 62_000, "next_attempt_at 约在 60s 之后");
	await page.goto(`${BASE}/settings/notifications`);
	const line = page
		// 只取最内层的 li（投递行），外层通知 li 也含同样的文字
		.locator("li:not(:has(li))")
		.filter({ hasText: `${CH_PREFIX}-ntfy-${RUN}` })
		.filter({ hasText: "待重试（已尝试 2 次）" })
		.first();
	await line.waitFor({ timeout: 15_000 });
	const cls = (await line.getAttribute("class")) ?? "";
	const txt = (await line.textContent()) ?? "";
	note(`设置页投递行「${txt.replace(/\n/g, " ")}」class=${cls}`);
	check(
		cls.includes("text-warn") && txt.includes("500"),
		"黄色待重试并显示失败原因",
	);
	await shot(page, "N13-recent");
	ntfyFail = false;
}

async function N14() {
	await ensureAwake(THREAD);
	const r = await api("POST", "/api/v1/notifications/read", {});
	check(r.status === 204, `先全部标为已读 ${r.status}`);
	const page = await homeA();
	await page
		.getByRole("button", { name: "通知", exact: true })
		.waitFor({ timeout: 15_000 });
	const t0 = Date.now();
	await say(THREAD, "数到 3");
	const n = await waitFor(
		"notifications 有新 done",
		() =>
			sql<{ id: string; title: string }>(
				`select id, title from notifications where thread_id='${THREAD}' and kind='done' and created_at>=${t0} order by created_at desc limit 1`,
			)[0],
		30_000,
	);
	const toast = page
		.locator('section[aria-label="通知提示"] [role=status]')
		.filter({ hasText: n.title || "未命名线程" })
		.first();
	await toast.waitFor({ timeout: 15_000 });
	note(`toast「${(await toast.textContent()) ?? ""}」`);
	const bell = page.getByRole("button", { name: "通知（1 条未读）" });
	check(await bell.isVisible(), "铃铛计数为 1");
	await shot(page, "N14-toast");
	await toast.locator("button").first().click();
	await page.waitForURL(`**/t/${THREAD}`, { timeout: 15_000 });
	await page
		.getByRole("button", { name: "通知", exact: true })
		.waitFor({ timeout: 15_000 });
	check(true, "进入线程页后铃铛无计数");
	const read = await waitFor(
		"notifications.read_at 已写",
		() =>
			sql<{ read_at: number | null }>(
				`select read_at from notifications where id='${n.id}'`,
			)[0]?.read_at,
		10_000,
	);
	note(`read_at=${read}`);
}

async function N15() {
	const mk = await cli(
		`thread new --no-project --title cli-n15 --json ${q("回复 ok")}`,
	);
	check(mk.code === 0, `thread new 退出码 ${mk.code}`);
	const t3 = (JSON.parse(mk.stdout) as { id: string }).id;
	note(`T3=${t3}`);
	await waitTurn(t3, 0, 900_000);
	const ls = await cli("thread list --json");
	check(ls.code === 0 && ls.stdout.includes(t3), "thread list --json 含 T3");
	const show = await cli(`thread show ${t3} --json`);
	check(show.code === 0 && show.stdout.includes(t3), "thread show --json");
	const r = await say(t3, "再回复一次 ok");
	check(r.state === "idle", "thread send 一轮完成");
	const out = `/tmp/m5-attach-${RUN}.txt`;
	rmSync(out, { force: true });
	const att = cliBg(`thread attach ${t3} --no-input --exit-on-idle`, ROOT, out);
	await waitFor(
		"attach 打完 tail（已补发）",
		() => existsSync(out) && readFileSync(out, "utf8").includes("[assistant]"),
		30_000,
	);
	const before = await sendCli(t3, "数到 3");
	const code = await exited(att, 240_000);
	await waitTurn(t3, before, 60_000);
	const text = readFileSync(out, "utf8");
	writeFileSync(`${SHOTS}/N15-attach-${RUN}.txt`, text);
	note(
		`attach 退出 ${code}；输出 ${text.split("\n").length} 行（存 N15-attach-${RUN}.txt）`,
	);
	check(code === 0, "attach 回合结束后自行退出 0");
	const after = text.slice(text.lastIndexOf("[user] 数到 3"));
	check(text.includes("[user] 数到 3"), "out.txt 含 `[user] 数到 3`");
	check(/\[assistant\]/.test(after.slice(1)), "其后有 `[assistant]` 行");
	const ar = await cli(`thread archive ${t3}`);
	check(ar.code === 0, `thread archive ${ar.code} ${ar.out}`);
	const l1 = await cli("thread list --json");
	const l2 = await cli("thread list --archived --json");
	check(l1.code === 0 && !l1.stdout.includes(t3), "默认列表没有它");
	check(l2.code === 0 && l2.stdout.includes(t3), "--archived 有它");
	const pname = `tmp-n15-${RUN}`;
	const pc = await cli(
		`provider create ${pname} >/dev/null && ${CLI_ENV} ${q(ORYN)} provider rm ${pname}`,
	);
	check(pc.code === 0, `provider create + rm ${pc.code} ${pc.out}`);
	const pl = await cli("provider ls --json");
	check(
		pl.code === 0 && !pl.stdout.includes(pname),
		`provider ls 里没有 ${pname}`,
	);
}

let TRACKED = "README.md";

function freshClone() {
	rmSync(SYNC_DIR, { recursive: true, force: true });
	const url =
		sql<{ repo_url: string }>(
			`select repo_url from projects where name='${PROJECT}'`,
		)[0]?.repo_url ?? SANDBOX_REPO;
	const r = sh(`git clone -q ${q(url)} ${SYNC_DIR}`);
	if (r.code !== 0) throw new Error(`git clone 失败：${r.out}`);
}

const lgit = (args: string) => sh(`git -C ${SYNC_DIR} ${args}`);
const ogit = (orb: string, args: string) =>
	dexec(orb, `git -C ${REPO} ${args}`);
const sortLines = (s: string) =>
	s.split("\n").filter(Boolean).sort().join("\n");
const localFile = (rel: string) =>
	existsSync(`${SYNC_DIR}/${rel}`)
		? readFileSync(`${SYNC_DIR}/${rel}`, "utf8")
		: "<缺失>";
const orbFile = (orb: string, rel: string) => {
	const r = dexec(orb, `cat ${REPO}/${rel}`);
	return r.code === 0 ? `${r.stdout}` : "<缺失>";
};

async function N16() {
	const orb = await ensureAwake(THREAD);
	TRACKED = ogit(orb, "ls-files README.md").stdout
		? "README.md"
		: (ogit(orb, "ls-files").stdout.split("\n")[0] ?? "README.md");
	const commitF = `m5-sync-commit-${RUN}.txt`;
	const untrackedF = `m5-sync-untracked-${RUN}.txt`;
	const mk = dexec(
		orb,
		`cd ${REPO} && printf 'commit ${RUN}\\n' > ${commitF} && git add ${commitF} && git -c user.name='Oryn M5' -c user.email=m5@oryn.invalid commit -qm 'm5 sync ${RUN}' && echo 'tracked change ${RUN}' >> ${TRACKED} && printf 'untracked ${RUN}\\n' > ${untrackedF}`,
	);
	check(
		mk.code === 0,
		`orb 内提交 + 改 ${TRACKED} + 未跟踪文件（${mk.out || "ok"}）`,
	);
	freshClone();
	const origBranch = lgit("symbolic-ref --short HEAD").stdout;
	const origSha = lgit("rev-parse HEAD").stdout;
	note(`本地 ${SYNC_DIR} ${origBranch}@${origSha.slice(0, 8)}`);
	const log = `/tmp/m5-sync-${RUN}.log`;
	const proc = cliBg(`sync ${THREAD}`, SYNC_DIR, log);
	try {
		const orbHead = ogit(orb, "rev-parse HEAD").stdout;
		await waitFor(
			"本地 HEAD = orb HEAD 且工作区到位",
			() =>
				lgit("rev-parse HEAD").stdout === orbHead &&
				localFile(TRACKED).trim() === orbFile(orb, TRACKED).trim(),
			90_000,
			500,
		);
		const lp = sortLines(lgit("status --porcelain").stdout);
		const op = sortLines(ogit(orb, "status --porcelain").stdout);
		note(
			`porcelain 本地「${lp.replace(/\n/g, " | ")}」 orb「${op.replace(/\n/g, " | ")}」`,
		);
		check(lp === op, "git status --porcelain 与 orb 逐行相同");
		for (const f of [commitF, TRACKED, untrackedF])
			check(localFile(f).trim() === orbFile(orb, f).trim(), `${f} 内容一致`);
		dexec(orb, `cd ${REPO} && echo 'second change ${RUN}' >> ${TRACKED}`);
		await waitFor(
			"第二次改动到本地",
			() => localFile(TRACKED).includes(`second change ${RUN}`),
			60_000,
			500,
		);
		check(
			localFile(TRACKED).trim() === orbFile(orb, TRACKED).trim(),
			"第二次改动已镜像",
		);
	} finally {
		proc.kill("SIGINT");
		const code = await exited(proc, 60_000);
		note(
			`sync 退出 ${code}；日志：${readFileSync(log, "utf8").trim().split("\n").slice(-4).join(" | ")}`,
		);
	}
	check(
		lgit("symbolic-ref --short HEAD").stdout === origBranch,
		`回到原分支 ${origBranch}`,
	);
	check(lgit("rev-parse HEAD").stdout === origSha, "回到原提交");
	check(
		lgit("status --porcelain").stdout === "",
		"git status --porcelain 为空",
	);
	check(lgit("for-each-ref refs/oryn").stdout === "", "refs/oryn 为空");
}

async function N17() {
	const orb = await ensureAwake(THREAD);
	if (
		!existsSync(`${SYNC_DIR}/.git`) ||
		lgit("status --porcelain").stdout !== ""
	)
		freshClone();
	const a1 = await cli(`sync --apply ${THREAD}`, SYNC_DIR);
	note(
		`sync --apply → ${a1.code}：${a1.out.split("\n").slice(-3).join(" | ")}`,
	);
	check(a1.code === 0, "第一次退出 0");
	const ob = ogit(orb, "symbolic-ref --short HEAD").stdout;
	check(
		lgit("symbolic-ref --short HEAD").stdout === ob,
		`本地在与 orb 同名的分支 ${ob}`,
	);
	check(
		lgit("rev-parse HEAD").stdout === ogit(orb, "rev-parse HEAD").stdout,
		"HEAD 相同",
	);
	const lp = lgit("status --porcelain --untracked-files=all").stdout;
	const op = ogit(orb, "status --porcelain --untracked-files=all").stdout;
	note(
		`本地「${sortLines(lp).replace(/\n/g, " | ")}」 orb「${sortLines(op).replace(/\n/g, " | ")}」`,
	);
	check(
		sortLines(lp.replace(/^.. /gm, "")) === sortLines(op.replace(/^.. /gm, "")),
		"未提交改动与未跟踪文件都在（路径集合一致）",
	);
	check(
		localFile(TRACKED).trim() === orbFile(orb, TRACKED).trim(),
		`${TRACKED} 内容与 orb 一致`,
	);
	sh(`echo x >> ${SYNC_DIR}/${TRACKED}`);
	const mine = localFile(TRACKED);
	const a2 = await cli(`sync --apply ${THREAD}`, SYNC_DIR);
	note(`第二次 → ${a2.code}：${a2.out.split("\n").slice(-2).join(" | ")}`);
	check(a2.code === 2, "第二次退出 2");
	check(localFile(TRACKED) === mine, `${TRACKED} 的本地修改原样保留`);

	const page = await mainPage();
	await openThread(page, THREAD);
	await page.locator("[data-panel-tab=changes]").click();
	await page.getByRole("button", { name: /^同步/ }).click();
	await page.getByRole("menuitem", { name: "复制一次性应用命令" }).click();
	await page.bringToFront();
	const clip = await waitFor(
		"剪贴板有内容",
		() => page.evaluate(() => navigator.clipboard.readText()),
		5_000,
	);
	note(`剪贴板「${clip}」`);
	check(
		clip === `oryn sync --apply ${THREAD}`,
		"剪贴板 = oryn sync --apply $T",
	);
	await shot(page, "N17-sync-menu");
}

async function N18() {
	if (!N18_THREAD) throw new Error("没有 N18_THREAD（.dev/m4.thread）");
	note(`旧镜像线程 ${N18_THREAD}`);
	const orb = await ensureAwake(N18_THREAD, 600_000);
	freshClone();
	const s = await cli(`sync --apply ${N18_THREAD}`, SYNC_DIR);
	note(`sync --apply → ${s.code}：${s.out.split("\n").slice(-2).join(" | ")}`);
	check(
		s.code !== 0 && s.out.includes("这台 orb 创建于旧版本，不支持 sync"),
		"提示旧版本不支持 sync",
	);
	const p = await api("POST", `/api/v1/orbs/${orb}/sync/prepare`, {});
	check(
		p.status === 409 && (p.json as { code?: string })?.code === "orbd_outdated",
		`接口 ${p.status} ${p.text.slice(0, 100)}`,
	);
	const sh18 = await createShareApi(N18_THREAD, "snapshot");
	const g = await anon(`/api/v1/public/shares/${sh18.slug}`);
	const e = await anon(`/api/v1/public/shares/${sh18.slug}/entries`);
	check(
		g.status === 200 && e.status === 200,
		`分享可匿名读取（${g.status}/${e.status}）`,
	);
	await ensureChannel("webhook");
	const t0 = Date.now();
	await say(N18_THREAD, "只回复 ok");
	const h = await waitFor(
		"Webhook 收到 N18 线程的 thread.done",
		() => hooksFor(N18_THREAD, t0)[0],
		30_000,
	);
	check(h.sigOk, "通知照常（签名一致）");
	const d = await api("DELETE", `/api/v1/shares/${sh18.id}`);
	check(d.status === 204, `撤销 ${d.status}`);
}

// ---------------------------------------------------------------- 主流程

const TESTS: [string, () => Promise<void>][] = [
	["N1", N1],
	["N2", N2],
	["N3", N3],
	["N4", N4],
	["N5", N5],
	["N6", N6],
	["N7", N7],
	["N8", N8],
	["N9", N9],
	["N10", N10],
	["N11", N11],
	["N12", N12],
	["N13", N13],
	["N14", N14],
	["N15", N15],
	["N16", N16],
	["N17", N17],
	["N18", N18],
];

THREAD = await ensureThread();
await api("POST", `/api/v1/threads/${THREAD}/wake`);
ORB = await waitFor(
	"线程有 orb 且 orb ready",
	() => {
		const o = threadRow(THREAD)?.orb_id;
		return o && orbState(o) === "ready" ? o : undefined;
	},
	600_000,
	2000,
);
// 首个回合（新建线程时）跑完再开始
if (!SETTLED[stateOf(THREAD)])
	await waitFor("线程空闲", () => SETTLED[stateOf(THREAD)], 300_000, 1000);

// 清掉上次遗留的渠道（同名前缀）与回环推送订阅
// （通知接口不可用时只告警：分享 / CLI / sync 场景照跑，通知场景各自失败）
try {
	for (const ch of await channels())
		if (ch.name.startsWith(CH_PREFIX))
			await api("DELETE", `/api/v1/notification-channels/${ch.id}`);
	const l = await api("GET", "/api/v1/push/subscriptions");
	for (const s of (
		l.json as { subscriptions?: { id: string; endpoint: string }[] }
	)?.subscriptions ?? [])
		if (s.endpoint.startsWith("http://127.0.0.1"))
			await api("DELETE", `/api/v1/push/subscriptions/${s.id}`);
} catch (e) {
	console.log(`  ! 清理遗留渠道 / 订阅失败：${String(e)}`);
}

const browser: Browser = await chromium.launch({
	executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
	headless: true,
	args: ["--no-sandbox"],
});
ctxA = await browser.newContext({
	locale: "zh-CN",
	viewport: { width: 1400, height: 900 },
	extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
});
await ctxA.grantPermissions(
	["notifications", "clipboard-read", "clipboard-write"],
	{
		origin: ORIGIN,
	},
);
await ctxA.addInitScript(() => {
	localStorage.setItem("oryn.debug", "1");
	localStorage.setItem("oryn.panel.width", "760");
});
ctxC = await browser.newContext({
	locale: "zh-CN",
	viewport: { width: 1280, height: 900 },
});
console.log(
	`M5 验收 run=${RUN} thread=${THREAD} orb=${ORB} n18=${N18_THREAD} shots=${SHOTS}\n` +
		`  假服务 webhook=${HOOK_URL} ntfy=${NTFY_URL} bark=${BARK_URL} push=${PUSH_ENDPOINT} telegram=${TG_API}`,
);

for (const [id, fn] of TESTS) {
	if (ONLY && !ONLY.has(id)) continue;
	evidence = [];
	console.log(`── ${id}`);
	const t0 = Date.now();
	try {
		await fn();
		console.log(`${id} PASS (${((Date.now() - t0) / 1000).toFixed(1)}s)`);
		results.push({ id, verdict: "PASS", evidence });
	} catch (e) {
		note(`错误：${e instanceof Error ? e.message : String(e)}`);
		let i = 0;
		for (const ctx of [ctxA, ctxC])
			for (const p of ctx.pages())
				if (!p.isClosed()) await shot(p, `${id}-FAIL-${i++}`);
		console.log(`${id} FAIL (${((Date.now() - t0) / 1000).toFixed(1)}s)`);
		results.push({ id, verdict: "FAIL", evidence });
	}
}

// 收尾：停用本次建的渠道（保留投递记录），关掉假服务
for (const id of madeChannels)
	await api("PATCH", `/api/v1/notification-channels/${id}`, {
		enabled: false,
	}).catch(() => undefined);
await browser.close();
for (const s of [hookSrv, ntfySrv, barkSrv, pushSrv, tgSrv]) s.stop(true);
if (pageErrors.length)
	console.log(
		`页面错误 ${pageErrors.length} 条：\n  ${pageErrors.join("\n  ")}`,
	);
await Bun.write(
	`${SHOTS}/results-${RUN}.json`,
	JSON.stringify(results, null, 2),
);
console.log("\n汇总");
for (const r of results) console.log(`${r.id} ${r.verdict}`);
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
