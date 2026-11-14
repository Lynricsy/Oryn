#!/usr/bin/env bun
import {
	chmodSync,
	copyFileSync,
	existsSync,
	mkdirSync,
	mkdtempSync,
	openSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
/**
 * M8 真机验收（计划「验证」B1–B20 + 主人追加 B21–B23 + Web 侧新建线程 409 选择对话框 B24）：对 dev 部署
 * （orynd + docker provider，`make image` 之后的 oryn-orb:dev）逐项执行，每项打印 `B<n> PASS|FAIL|SKIP (耗时)` 与证据，
 * 失败时给打开的页面截图，结束时写 results JSON。
 *
 * 浏览器上下文：
 *   A：`extraHTTPHeaders` 带设备令牌，`clipboard-read / clipboard-write`，视口 1400x900（所有者界面）；
 *   B：只带网页会话 Cookie `__Host-oryn_session`（`orynd admin create-web-session` 现签），并在页面里记录每条 WebSocket 的关闭码；
 *   C：匿名；
 *   M：390x800、hasTouch、isMobile，带设备令牌。
 * orb 内一律 `docker exec <orb> …`（容器默认用户 user）。CDP 只经 orb 内前门 `127.0.0.1:7822`（`curl …/json/*`；需要协议调用时
 * 在 orb 里用 bun 跑一个小 CDP 客户端 /tmp/m8cdp.mjs）。「浏览器主进程」= 命令行含 `--remote-debugging-port=` 且不含 `--type=`。
 * 两个测试站点：
 *   orb 内站点（python3，`oryn orb service start m8site --port 8765`）：标题 `m8-home`，顶部自动聚焦的输入框（输入时标题变成
 *     `typed:<值>`），下半页铺满一个按钮（点击后标题 `clicked`）；
 *   宿主站点（本进程 `Bun.serve`，监听 dev/provider.yaml 里 `orb_url` 的网桥地址）：`/login?sid=X&o=<标记>` 设 `sid` Cookie 并写
 *     localStorage，再回报 `/stored`；`/whoami?o=<标记>` 回报 `/seen?ls=<localStorage>`。请求按查询串里的标记归属到具体 orb，
 *     每个请求的 Cookie 头都记下来。
 * 让 orb 保持唤醒：本进程开一条 `/api/v1/ws`，每 5 秒对「需要醒着的线程」各发一次 `focus`（= 一次用户交互）；要等它冻结 /
 *   停机前先移出该集合，并关掉打开这个线程的页面（页面也会 focus，且 dev 开着 wake_on_focus）。
 *
 * 用法（web/ 目录）：bun run accept:m8 [-- --only B1,B2]
 * 环境变量：
 *   ORYN_URL（默认 http://localhost:7600）、ORYN_ROOT、ORYN_TOKEN（默认 .dev/credentials.json 的 token）；
 *   M8_SESSION（网页会话 Cookie 值；缺省时现签）；M8_THREAD（B2–B8 / B14 / B16 / B17 用的线程；缺省时新建，不带 Profile）；
 *   M8_OLD_THREAD（旧镜像线程，默认 .dev/m8-old.thread）；M8_PROJECT（新线程挂的项目名，默认不挂项目，开机更快）；
 *   M8_HOST_IP（宿主站点监听地址，默认取 dev/provider.yaml 的 orb_url 主机）；M8_IMAGE / M8_PRE_IMAGE（B1 比较的两个镜像）；
 *   M8_B20_APP=1（B20 与 B17 的真实按键分支：见下）；M8_KEEP=1（不清理本次建的 orb 与 Profile）；
 *   CHROMIUM（默认 /usr/bin/chromium）；ORYN_DESKTOP_BIN（默认 web/src-tauri/target/release/oryn-desktop，不存在时
 *   `bun run tauri build --no-bundle` 现建）；M8_SHOTS（默认 /tmp/oryn-shots/m8-accept）。
 *
 * 真跑（M8_B20_APP=1；需要 Xvfb openbox xdotool xclip xprop import dbus-daemon trust go，以及 .dev/tls 的本机 CA）：
 *   自起一个 Xvfb（1440x900）+ openbox + 无 service 目录的私有会话总线；dev/tlsproxy 把 https://localhost:<随机端口> 反代到
 *   ORYN_URL（WebKitGTK 不收 http:// 下发的 `__Host-` Secure Cookie）；系统还不信任 .dev/tls/ca.crt 时临时
 *   `trust anchor --store`，结束（含 Ctrl+C）时 `--remove`。真实 Tauri App 在这个显示上启动并配对（设备结束时吊销），
 *   WebKit 远程检查器（WEBKIT_INSPECTOR_HTTP_SERVER）只用来读 DOM 与坐标；点击与按键一律是 xdotool 发出的真实 X 事件，
 *   每步 `import -window root` 截屏。B17 另在同一显示上以普通进程起有界面 Chromium（非 headless，只经一条裸 CDP 读状态）。
 *   未开启时 B20 记 SKIP，B17 只跑 headless 兼容路径（仅验证退出全屏后的清理）。
 *
 * 场景之间的依赖（单独 `--only` 时按需现建，见各场景开头）：
 *   B2–B8 / B14 / B16 / B17 共用线程 T（B2 要求 T 刚建好、浏览器还没启动过；B3 记录的画面哈希给 B4 比较）；B20 自建线程。
 *   B9–B13 / B18 是一条 Profile P 的链：B9 建 P、A1、A2；B10 接管出 A3；B11 查 B9/B10 写入的 sid；B12 / B13 / B18 用 A3。
 *   B19 前要求没有别的 orb 在保存 Profile（链上的 A3 被保持唤醒）。B22 建的接管对给 B23 用。
 * 与计划原文的差异（以 S0 契约与主人决定为准）：建线程请求用 `profileMode: "takeover"`（不是 `browserProfileTakeover`）；
 *   B10 被接管的一方按主人决定降级为只读（`status=readonly`、`readonlyReason=taken_over`、浏览器照常运行、本地目录保留），
 *   不再是 `in_use` + 清空目录，Web 侧看的是「只读 Profile · 已被「…」接管」横幅而不是提醒点。
 * 已知前提：Chromium 约 30 秒才把 Cookie 落盘，活体保存（冻结前、接管前）只带走已落盘的部分（计划「假设」已写明）。
 *   B10 / B21 / B22 在活体保存前先轮询 orb 里 Cookies 库直到新值落盘（前置条件，不是断言）。
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
const BASE_URL = new URL(BASE);
const BASE_HOST = BASE_URL.host;
const BASE_PORT = BASE_URL.port || "80";
const CREDS = `${ROOT}/.dev/credentials.json`;
const TOKEN =
	process.env.ORYN_TOKEN ??
	(JSON.parse(readFileSync(CREDS, "utf8")) as { token: string }).token;
const SHOTS = process.env.M8_SHOTS ?? "/tmp/oryn-shots/m8-accept";
const DB = `${ROOT}/.dev/data/oryn.db`;
const PROFILES_DIR = `${ROOT}/.dev/data/browser-profiles`;
const ORYN = `${ROOT}/bin/oryn`;
const RUN = Math.random().toString(36).slice(2, 6);
const PROJECT = process.env.M8_PROJECT ?? "";
const OLD_THREAD =
	process.env.M8_OLD_THREAD ??
	(existsSync(`${ROOT}/.dev/m8-old.thread`)
		? readFileSync(`${ROOT}/.dev/m8-old.thread`, "utf8").trim()
		: "");
const IMAGE = process.env.M8_IMAGE ?? "oryn-orb:dev";
const PRE_IMAGE = process.env.M8_PRE_IMAGE ?? "oryn-orb:pre-m8";
const KEEP = process.env.M8_KEEP === "1";
const CDP = "http://127.0.0.1:7822";
const SITE_PORT = 8765;
const ORB_PROFILE_DIR = "/home/user/.oryn/desktop/profile";
const HOST_IP = process.env.M8_HOST_IP ?? bridgeIp();
mkdirSync(SHOTS, { recursive: true });

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

/** dev/provider.yaml 的 `orb_url` 主机（orb 访问宿主用的网桥地址）。 */
function bridgeIp(): string {
	try {
		const y = readFileSync(`${ROOT}/dev/provider.yaml`, "utf8");
		const m = /^orb_url:\s*"?([^"\s]+)"?/m.exec(y);
		if (m?.[1]) return new URL(m[1]).hostname;
	} catch {
		// 没有 provider.yaml
	}
	return "172.17.0.1";
}

// ---------------------------------------------------------------- 工具

const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const te = new TextEncoder();

interface ShRes {
	code: number;
	out: string;
	stdout: string;
}

/** 异步跑 shell（不卡事件循环：宿主站点与保活连接要在等待期间照常应答）。 */
async function sh(
	cmd: string,
	opts: { stdin?: string; timeoutMs?: number } = {},
): Promise<ShRes> {
	const p = Bun.spawn(["bash", "-c", cmd], {
		cwd: ROOT,
		stdin: opts.stdin !== undefined ? te.encode(opts.stdin) : "ignore",
		stdout: "pipe",
		stderr: "pipe",
	});
	const timer = opts.timeoutMs
		? setTimeout(() => p.kill(), opts.timeoutMs)
		: undefined;
	const [stdout, stderr, code] = await Promise.all([
		new Response(p.stdout).text(),
		new Response(p.stderr).text(),
		p.exited,
	]);
	clearTimeout(timer);
	return { code, out: `${stdout}${stderr}`.trim(), stdout: stdout.trim() };
}

function sql<T = Record<string, unknown>>(qs: string): T[] {
	const r = Bun.spawnSync(["sqlite3", "-json", DB, qs]);
	const s = r.stdout.toString().trim();
	return s ? (JSON.parse(s) as T[]) : [];
}

interface ThreadRow {
	state: string;
	last_seq: number;
	orb_id: string | null;
	title: string | null;
}

const threadRow = (tid: string) =>
	sql<ThreadRow>(
		`select state, last_seq, orb_id, title from threads where id='${tid}'`,
	)[0];

const orbState = (orb: string) =>
	sql<{ state: string }>(`select state from orbs where id='${orb}'`)[0]
		?.state ?? "?";

/** orb 容器里以 user 身份跑 shell（登录 shell，PATH 与 agent 相同）。 */
function dexec(orb: string, cmd: string, env: Record<string, string> = {}) {
	const e = Object.entries(env)
		.map(([k, v]) => `-e ${q(`${k}=${v}`)} `)
		.join("");
	return sh(`docker exec ${e}${orb} bash -lc ${q(cmd)}`, {
		timeoutMs: 120_000,
	});
}

async function dwrite(orb: string, path: string, content: string) {
	const r = await sh(
		`docker exec -i ${orb} bash -c ${q(`mkdir -p "$(dirname ${path})" && cat > ${path}`)}`,
		{ stdin: content },
	);
	if (r.code !== 0) throw new Error(`写 ${orb}:${path} 失败：${r.out}`);
}

interface ApiRes {
	status: number;
	json: unknown;
	text: string;
}

async function req(
	method: string,
	path: string,
	init: { body?: unknown; headers?: Record<string, string> } = {},
): Promise<ApiRes> {
	const res = await fetch(path.startsWith("http") ? path : `${BASE}${path}`, {
		method,
		headers: {
			...init.headers,
			...(init.body !== undefined
				? { "Content-Type": "application/json" }
				: {}),
		},
		body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
		redirect: "manual",
	});
	const text = await res.text();
	let json: unknown = null;
	try {
		json = JSON.parse(text);
	} catch {
		// 非 JSON 体
	}
	return { status: res.status, json, text };
}

/** 所有者接口（设备令牌 + CSRF 头）。 */
const api = (method: string, path: string, body?: unknown) =>
	req(method, path, {
		body,
		headers: { Authorization: `Bearer ${TOKEN}`, "X-Oryn-CSRF": "1" },
	});

const codeOf = (r: ApiRes) => (r.json as { code?: string } | null)?.code ?? "";

/** waitFor 里抛它会立即中止等待（例如 orb 进了 failed）。 */
class Abort extends Error {}

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
			if (e instanceof Abort) throw e;
			last = e;
		}
		await Bun.sleep(intervalMs);
	}
	throw new Error(
		`超时（${timeoutMs}ms）：${what}${last ? `；最后错误 ${String(last)}` : ""}`,
	);
}

/** 在 ms 内每次取值都成立（「保持不变」类观察）；任一次不成立即失败。 */
async function holdFor(
	what: string,
	fn: () => Promise<boolean> | boolean,
	ms: number,
	intervalMs = 250,
) {
	const end = Date.now() + ms;
	while (Date.now() < end) {
		if (!(await fn())) throw new Error(`未能保持：${what}`);
		await Bun.sleep(intervalMs);
	}
}

// ---------------------------------------------------------------- 结果

type Verdict = "PASS" | "FAIL" | "SKIP";
const results: {
	id: string;
	verdict: Verdict;
	seconds: number;
	evidence: string[];
}[] = [];
let evidence: string[] = [];
let current = "";
const note = (s: string) => {
	evidence.push(s);
	console.log(`    · ${s}`);
};
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};
/** 计划里「写进结果」的数值：记下是否落在预期区间，不判失败。 */
const soft = (ok: boolean, what: string) => note(`${ok ? "✓" : "⚠"} ${what}`);

/** 需要人工完成的场景。 */
class Skip extends Error {}

async function shot(page: Page, name: string) {
	const p = `${SHOTS}/${name}.png`;
	await page.screenshot({ path: p }).catch(() => {});
	note(`截图 ${p}`);
}

// ---------------------------------------------------------------- 模型

interface Holder {
	orbId: string;
	threadIds: string[];
	threadId?: string;
	threadTitle?: string;
	since: number;
}

interface BrowserProfile {
	id: string;
	name: string;
	revision: number;
	sizeBytes: number;
	savedAt?: number;
	saveError?: string;
	holder?: Holder;
	readers?: unknown[];
}

interface DeskProfile {
	id: string;
	name: string;
	status: string;
	mode: string;
	revision: number;
	error?: string;
	heldBy?: Holder;
	readonlyReason?: string;
	takenOverBy?: { threadId?: string; threadTitle?: string; at: number };
}

interface OrbJson {
	id: string;
	state: string;
	desktop?: { displayReady: boolean; browser: string; profile?: DeskProfile };
}

interface Th {
	tid: string;
	orb: string;
}

async function getOrb(orb: string): Promise<OrbJson> {
	const r = await api("GET", `/api/v1/orbs/${orb}`);
	if (r.status !== 200) throw new Error(`GET orbs/${orb} ${r.status}`);
	return r.json as OrbJson;
}

const createdOrbs = new Set<string>();
const createdProfiles = new Set<string>();

async function profileList(): Promise<BrowserProfile[]> {
	const r = await api("GET", "/api/v1/browser-profiles");
	if (r.status !== 200) throw new Error(`列 Profile ${r.status} ${r.text}`);
	return (r.json as { profiles: BrowserProfile[] }).profiles;
}

async function profileGet(id: string): Promise<BrowserProfile> {
	const p = (await profileList()).find((x) => x.id === id);
	if (!p) throw new Error(`Profile ${id} 不存在`);
	return p;
}

async function createProfile(name: string): Promise<string> {
	const r = await api("POST", "/api/v1/browser-profiles", { name });
	if (r.status !== 201) throw new Error(`建 Profile ${r.status} ${r.text}`);
	const id = (r.json as BrowserProfile).id;
	createdProfiles.add(id);
	note(`Profile ${name} = ${id}`);
	return id;
}

interface ProfileEvent {
	event: string;
	orb_id: string | null;
	ts: number;
	detail: string | null;
}

const profileEvents = (pid: string, since: number) =>
	sql<ProfileEvent>(
		`select event, orb_id, ts, detail from browser_profile_events where profile_id='${pid}' and ts>=${since} order by ts`,
	);

// ---------------------------------------------------------------- 线程与 orb

let projectCache: string | undefined;
function projectId(): string | undefined {
	if (!PROJECT) return undefined;
	if (projectCache) return projectCache;
	const r = sql<{ id: string }>(
		`select id from projects where name='${PROJECT}'`,
	)[0];
	if (!r) throw new Error(`项目 ${PROJECT} 不存在`);
	projectCache = r.id;
	return r.id;
}

function postThread(extra: Record<string, unknown>) {
	const pid = projectId();
	return api("POST", "/api/v1/threads", {
		prompt: "只回复 ok",
		...(pid ? { projectId: pid } : {}),
		...extra,
	});
}

const SETTLED: Record<string, true> = {
	idle: true,
	awaiting_input: true,
	error: true,
};

/** 等一个回合结束：`beforeSeq` 之后出现助手消息条目，且线程处于 idle / awaiting_input / error。 */
function waitTurn(tid: string, beforeSeq: number, timeoutMs = 300_000) {
	return waitFor(
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
		500,
	);
}

/** `beforeSeq` 之后助手消息的文本（按顺序拼接）。 */
function assistantText(tid: string, beforeSeq: number): string {
	const rows = sql<{ p: string }>(
		`select cast(payload as text) p from entries where thread_id='${tid}' and seq>${beforeSeq} and type='message' order by seq`,
	);
	const out: string[] = [];
	for (const r of rows) {
		const m = (
			JSON.parse(r.p) as { message?: { role?: string; content?: unknown } }
		).message;
		if (m?.role !== "assistant") continue;
		if (typeof m.content === "string") out.push(m.content);
		else if (Array.isArray(m.content))
			for (const b of m.content as { type?: string; text?: string }[])
				if (b?.type === "text" && b.text) out.push(b.text);
	}
	return out.join("\n");
}

async function say(tid: string, msg: string) {
	const before = threadRow(tid)?.last_seq ?? 0;
	const r = await sh(
		`ORYN_CREDENTIALS=${q(CREDS)} ${q(ORYN)} thread send ${tid} ${q(msg)}`,
		{ timeoutMs: 120_000 },
	);
	if (r.code !== 0) throw new Error(`thread send 失败：${r.out}`);
	await waitTurn(tid, before);
	return assistantText(tid, before);
}

/** 等线程的 orb 到 ready（需要时唤醒；`desktop` 时还要求 displayReady）。 */
async function ensureReady(
	tid: string,
	opts: { desktop?: boolean; timeoutMs?: number } = {},
): Promise<string> {
	const desktop = opts.desktop ?? true;
	let lastWake = 0;
	return waitFor(
		`线程 ${tid} 的 orb ready${desktop ? " 且桌面就绪" : ""}`,
		async () => {
			const o = threadRow(tid)?.orb_id;
			if (!o) return undefined;
			const st = orbState(o);
			if (st === "deleted") throw new Abort(`orb ${o} 已删除`);
			if (
				(st === "frozen" || st === "stopped" || st === "failed") &&
				Date.now() - lastWake > 15_000
			) {
				lastWake = Date.now();
				await api("POST", `/api/v1/threads/${tid}/wake`);
			}
			if (st !== "ready") return undefined;
			if (!desktop) return o;
			return (await getOrb(o)).desktop?.displayReady ? o : undefined;
		},
		opts.timeoutMs ?? 600_000,
		2000,
	);
}

/** 建线程（断言 201），等 orb 与首个回合就绪，并加入保活集合。 */
async function newThread(
	label: string,
	extra: Record<string, unknown> = {},
): Promise<Th> {
	const r = await postThread(extra);
	if (r.status !== 201)
		throw new Error(`建线程 ${label} ${r.status} ${r.text.slice(0, 300)}`);
	const created = r.json as { thread?: { id: string }; id?: string };
	const tid = created.thread?.id ?? created.id ?? "";
	awake.add(tid);
	const orb = await ensureReady(tid);
	createdOrbs.add(orb);
	await waitTurn(tid, 0);
	note(`线程 ${label} = ${tid}，orb ${orb}`);
	return { tid, orb };
}

const waitOrbState = (orb: string, state: string, timeoutMs: number) =>
	waitFor(
		`orb ${orb} 进入 ${state}`,
		() => {
			const s = orbState(orb);
			if (s === "failed" && state !== "failed")
				throw new Abort(`orb ${orb} failed`);
			return s === state;
		},
		timeoutMs,
		1000,
	);

async function deleteOrb(orb: string, wait: boolean) {
	const r = await api("DELETE", `/api/v1/orbs/${orb}`);
	note(`DELETE orbs/${orb} → ${r.status}`);
	if (wait) await waitOrbState(orb, "deleted", 300_000);
}

async function pauseOrb(orb: string, mode: "freeze" | "stop") {
	const r = await api("POST", `/api/v1/orbs/${orb}/pause`, { mode });
	note(`pause ${mode} ${orb} → ${r.status}`);
	if (r.status >= 300) throw new Error(`pause ${r.status} ${r.text}`);
	await waitOrbState(orb, mode === "freeze" ? "frozen" : "stopped", 300_000);
}

// ---------------------------------------------------------------- 保活

const awake = new Set<string>();
let keepWs = null as WebSocket | null;
let keepBusy = false;

async function keepConnect() {
	const r = await api("POST", "/api/v1/ws-ticket");
	const ticket = (r.json as { ticket?: string } | null)?.ticket;
	if (!ticket) throw new Error(`ws-ticket ${r.status}`);
	const ws = new WebSocket(
		`${BASE.replace(/^http/, "ws")}/api/v1/ws?ticket=${encodeURIComponent(ticket)}`,
	);
	const opened = Promise.withResolvers<void>();
	ws.onopen = () => opened.resolve();
	ws.onerror = () => opened.reject(new Error("保活 WS 连接失败"));
	await opened.promise;
	ws.onmessage = () => {};
	ws.onclose = () => {
		if (keepWs === ws) keepWs = null;
	};
	keepWs = ws;
}

async function keepTick() {
	if (keepBusy || awake.size === 0) return;
	keepBusy = true;
	try {
		if (!keepWs) await keepConnect();
		for (const tid of awake)
			keepWs?.send(JSON.stringify({ type: "focus", threadId: tid }));
		keepWs?.send(JSON.stringify({ type: "focus", threadId: null }));
	} catch (e) {
		console.log(`    [保活] ${String(e)}`);
	} finally {
		keepBusy = false;
	}
}

// ---------------------------------------------------------------- 宿主站点

interface Hit {
	t: number;
	path: string;
	params: URLSearchParams;
	cookie: string;
}
const hits: Hit[] = [];

const html = (
	title: string,
	script: string,
	extra: Record<string, string> = {},
) =>
	new Response(
		`<!doctype html><meta charset=utf-8><title>${title}</title><body>${title}<script>${script}</script>`,
		{
			headers: {
				"Content-Type": "text/html; charset=utf-8",
				"Cache-Control": "no-store",
				...extra,
			},
		},
	);

const site = Bun.serve({
	hostname: HOST_IP,
	port: 0,
	fetch(r) {
		const u = new URL(r.url);
		hits.push({
			t: Date.now(),
			path: u.pathname,
			params: u.searchParams,
			cookie: r.headers.get("cookie") ?? "",
		});
		const tag = JSON.stringify(u.searchParams.get("o") ?? "");
		if (u.pathname === "/login") {
			const sid = u.searchParams.get("sid") ?? "";
			return html(
				"m8-login",
				`localStorage.setItem("ls", ${JSON.stringify(sid)});fetch("/stored?" + new URLSearchParams({ o: ${tag}, ls: localStorage.getItem("ls") || "" }));`,
				{
					"Set-Cookie": `sid=${encodeURIComponent(sid)}; Max-Age=86400; Path=/`,
				},
			);
		}
		if (u.pathname === "/whoami")
			return html(
				"m8-whoami",
				`fetch("/seen?" + new URLSearchParams({ o: ${tag}, ls: localStorage.getItem("ls") || "" }));`,
			);
		if (u.pathname === "/stored" || u.pathname === "/seen")
			return new Response(null, { status: 204 });
		return new Response("not found", { status: 404 });
	},
});

const siteUrl = (path: string, params: Record<string, string> = {}) => {
	const qs = new URLSearchParams(params).toString();
	return `http://${HOST_IP}:${site.port}${path}${qs ? `?${qs}` : ""}`;
};

let tagSeq = 0;
const newTag = (label: string) => `${label}-${RUN}-${++tagSeq}`;

const waitHit = (tag: string, path: string, timeoutMs = 90_000) =>
	waitFor(
		`宿主站点收到 ${path}（${tag}）`,
		() => hits.find((h) => h.path === path && h.params.get("o") === tag),
		timeoutMs,
		250,
	);

function cookieVal(header: string, name: string): string | undefined {
	for (const part of header.split(";")) {
		const i = part.indexOf("=");
		if (i > 0 && part.slice(0, i).trim() === name)
			return decodeURIComponent(part.slice(i + 1).trim());
	}
	return undefined;
}

// ---------------------------------------------------------------- orb 内浏览器

interface Target {
	id: string;
	type: string;
	title: string;
	url: string;
}

async function cdpList(orb: string): Promise<Target[]> {
	const r = await dexec(orb, `curl -s --max-time 40 ${CDP}/json/list`);
	return JSON.parse(r.stdout) as Target[];
}

/** 经 CDP 前门新开一个标签页并切到前台，返回 target id。 */
async function openInOrb(orb: string, url: string): Promise<string> {
	const r = await dexec(
		orb,
		`curl -s --max-time 40 -X PUT ${q(`${CDP}/json/new?${encodeURIComponent(url)}`)}`,
	);
	let id = "";
	try {
		id = (JSON.parse(r.stdout) as { id: string }).id;
	} catch {
		throw new Error(`json/new 失败：${r.out.slice(0, 200)}`);
	}
	await dexec(orb, `curl -s --max-time 10 ${CDP}/json/activate/${id}`);
	return id;
}

const targetTitle = async (orb: string, id: string) =>
	(await cdpList(orb)).find((t) => t.id === id)?.title ?? "";

interface Proc {
	pid: number;
	cmd: string;
}

/** 浏览器主进程：命令行含 `--remote-debugging-port=` 且不含 `--type=`。 */
async function browserMains(orb: string): Promise<Proc[]> {
	const r = await dexec(orb, "pgrep -af -- --remote-debugging-port || true");
	return r.stdout
		.split("\n")
		.filter(
			(l) =>
				l.includes("--remote-debugging-port=") &&
				!l.includes("--type=") &&
				!l.includes("pgrep"),
		)
		.map((l) => ({ pid: Number(l.split(" ")[0]), cmd: l }));
}

async function xdpy(orb: string): Promise<{ w: number; h: number }> {
	const r = await dexec(
		orb,
		"DISPLAY=:1 xdpyinfo | awk '/dimensions/{print $2}'",
	);
	const m = /^(\d+)x(\d+)/.exec(r.stdout);
	if (!m) throw new Error(`xdpyinfo：${r.out.slice(0, 200)}`);
	return { w: Number(m[1]), h: Number(m[2]) };
}

const CDP_TOOL = `const [target, callsJson] = process.argv.slice(2);
const base = "${CDP}";
async function wsUrl() {
  if (target === "browser") return (await (await fetch(base + "/json/version")).json()).webSocketDebuggerUrl;
  const list = await (await fetch(base + "/json/list")).json();
  const t = list.find((x) => x.id === target);
  if (!t) throw new Error("no target " + target);
  return t.webSocketDebuggerUrl;
}
const ws = new WebSocket(await wsUrl());
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error("ws error")); });
const pending = new Map();
ws.onmessage = (e) => { const m = JSON.parse(String(e.data)); const f = pending.get(m.id); if (f) { pending.delete(m.id); f(m); } };
const out = [];
let id = 0;
for (const c of JSON.parse(callsJson)) {
  const m = await new Promise((res) => { id += 1; pending.set(id, res); ws.send(JSON.stringify({ id, method: c.method, params: c.params ?? {} })); });
  out.push(m.error ? { error: m.error } : m.result);
}
console.log(JSON.stringify(out));
ws.close();
process.exit(0);
`;

const COOKIE_TOOL = `import os, shutil, sqlite3, sys
base = os.path.expanduser("${ORB_PROFILE_DIR}/Default")
src = next((p for p in (base + "/Network/Cookies", base + "/Cookies") if os.path.exists(p)), None)
if not src:
    print("")
    sys.exit(0)
shutil.copy(src, "/tmp/m8c.db")
for ext in ("-journal", "-wal"):
    if os.path.exists(src + ext):
        shutil.copy(src + ext, "/tmp/m8c.db" + ext)
    elif os.path.exists("/tmp/m8c.db" + ext):
        os.remove("/tmp/m8c.db" + ext)
c = sqlite3.connect("/tmp/m8c.db")
rows = c.execute("select hex(encrypted_value) || value from cookies where name = ? and host_key like ?", (sys.argv[1], "%" + sys.argv[2])).fetchall()
print(rows[0][0] if rows else "")
`;

const toolsWritten = new Set<string>();
async function ensureTools(orb: string) {
	if (toolsWritten.has(orb)) return;
	await dwrite(orb, "/tmp/m8cdp.mjs", CDP_TOOL);
	await dwrite(orb, "/tmp/m8cookie.py", COOKIE_TOOL);
	toolsWritten.add(orb);
}

/** 在 orb 里对浏览器（"browser"）或某个页面 target 依次发 CDP 调用，返回各自的 result（出错为 `{error}`）。 */
async function cdp(
	orb: string,
	target: string,
	calls: { method: string; params?: Record<string, unknown> }[],
): Promise<unknown[]> {
	await ensureTools(orb);
	const r = await dexec(
		orb,
		`timeout 60 bun /tmp/m8cdp.mjs ${q(target)} ${q(JSON.stringify(calls))}`,
	);
	const last = r.stdout.split("\n").pop() ?? "";
	try {
		return JSON.parse(last) as unknown[];
	} catch {
		throw new Error(`CDP 调用失败：${r.out.slice(0, 300)}`);
	}
}

/** orb 里 Cookies 库中该 Cookie 的签名（密文 hex + 明文）；没有为空串。 */
async function cookieSig(orb: string, name: string): Promise<string> {
	await ensureTools(orb);
	const r = await dexec(
		orb,
		`python3 /tmp/m8cookie.py ${q(name)} ${q(HOST_IP)}`,
	);
	return r.stdout;
}

/** 前置：等 Chromium 把新的 Cookie 写进磁盘（与 `before` 不同且非空；Chromium 约 30 秒落盘一次）。 */
async function waitCookieOnDisk(orb: string, name: string, before: string) {
	const t0 = Date.now();
	await waitFor(
		`Cookie ${name} 落盘`,
		async () => {
			const s = await cookieSig(orb, name);
			return s && s !== before;
		},
		120_000,
		2000,
	);
	note(
		`前置：Cookie ${name} 已落盘（等了 ${((Date.now() - t0) / 1000).toFixed(0)}s）`,
	);
}

/** 在 orb 的桌面浏览器里打开宿主站点 /login，等页面写好 localStorage 并回报。 */
async function browserLogin(orb: string, sid: string, label: string) {
	const tag = newTag(label);
	await openInOrb(orb, siteUrl("/login", { sid, o: tag }));
	const h = await waitHit(tag, "/stored");
	check(
		h.params.get("ls") === sid,
		`${label} 登录 sid=${sid}（页面已写 localStorage）`,
	);
}

/** 打开宿主站点 /whoami，返回该次请求的 Cookie 头与页面报告的 localStorage。 */
async function whoami(orb: string, label: string) {
	const tag = newTag(label);
	await openInOrb(orb, siteUrl("/whoami", { o: tag }));
	const seen = await waitHit(tag, "/seen");
	const who = hits.find(
		(h) => h.path === "/whoami" && h.params.get("o") === tag,
	);
	const res = { cookie: who?.cookie ?? "", ls: seen.params.get("ls") ?? "" };
	note(`${label} /whoami Cookie「${res.cookie}」 ls「${res.ls}」`);
	return res;
}

// ---------------------------------------------------------------- orb 内站点

const SITE_PY = `import http.server, os
HTML = b"""<!doctype html><html><head><meta charset=utf-8><title>m8-home</title>
<style>html,body{margin:0;height:100%}input{display:block;box-sizing:border-box;width:90%;margin:24px 5%;height:56px;font-size:28px}
button{position:fixed;left:0;right:0;bottom:0;width:100%;height:50vh;font-size:40px}</style></head>
<body><input id=i autofocus oninput="document.title='typed:'+this.value"><button onclick="document.title='clicked'">m8 button</button></body></html>"""
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(HTML)))
        self.end_headers()
        self.wfile.write(HTML)
    def log_message(self, *a):
        pass
http.server.ThreadingHTTPServer(("127.0.0.1", int(os.environ.get("PORT", "${SITE_PORT}"))), H).serve_forever()
`;

async function ensureSite(th: Th) {
	const ok = async () =>
		(
			await dexec(th.orb, `curl -s --max-time 5 http://127.0.0.1:${SITE_PORT}/`)
		).stdout.includes("<title>m8-home</title>");
	if (await ok()) return;
	await dwrite(th.orb, "/home/user/m8/site.py", SITE_PY);
	const r = await dexec(
		th.orb,
		`oryn orb service start m8site --command 'python3 $HOME/m8/site.py' --port ${SITE_PORT}`,
		{ ORYN_THREAD_ID: th.tid },
	);
	note(`起 orb 内站点 → ${r.code} ${r.out.split("\n").pop() ?? ""}`);
	await waitFor("orb 内站点可访问", ok, 60_000, 1000);
}

/** 新开一个 orb 内站点的标签页（输入框为空、已聚焦）并等标题就绪。 */
async function openSitePage(orb: string): Promise<string> {
	const id = await openInOrb(
		orb,
		`http://127.0.0.1:${SITE_PORT}/?n=${RUN}${++tagSeq}`,
	);
	await waitFor(
		"站点页标题 m8-home",
		async () => (await targetTitle(orb, id)) === "m8-home",
		20_000,
		500,
	);
	return id;
}

// ---------------------------------------------------------------- 浏览器上下文与页面

let ctxA: BrowserContext;
let ctxB: BrowserContext;
let ctxC: BrowserContext;
let ctxM: BrowserContext;
let SESSION = "";
let pageA: Page | undefined;
const pageErrors: string[] = [];

function wire(page: Page, tag: string) {
	page.on("pageerror", (e) => {
		pageErrors.push(`[${tag}] ${e.message}`);
		console.log(`    [${tag} pageerror] ${e.message}`);
	});
	return page;
}

/** 开一个临时页面；出错时先截图再关闭。 */
async function withPage<T>(
	ctx: BrowserContext,
	tag: string,
	fn: (p: Page) => Promise<T>,
): Promise<T> {
	const p = wire(await ctx.newPage(), tag);
	try {
		return await fn(p);
	} catch (e) {
		await shot(p, `${current}-${tag}-FAIL-${RUN}`);
		throw e;
	} finally {
		await p.close().catch(() => {});
	}
}

const DESK_TAB = "[data-panel-tab=desktop]";
const STAGE = "[data-testid=desktop-stage]";
const CANVAS = `${STAGE} canvas`;

async function openDesktopTab(page: Page) {
	await page.locator(DESK_TAB).first().waitFor({ timeout: 60_000 });
	// compact 是顶部同级标签（thread-tabs），其余是右侧面板的标签条
	const inTabs = page.locator(`[data-testid=thread-tabs] ${DESK_TAB}`);
	const tab =
		(await inTabs.count()) > 0
			? inTabs.first()
			: page.locator(DESK_TAB).first();
	if ((await tab.getAttribute("aria-selected")) !== "true") await tab.click();
	await page
		.locator("[data-testid=desktop-panel]")
		.waitFor({ timeout: 30_000 });
}

const waitConnected = (page: Page, timeoutMs: number) =>
	page
		.locator("[data-testid=desktop-status][data-state=connected]")
		.waitFor({ timeout: timeoutMs });

interface CanvasStats {
	distinct: number;
	hash: number;
	width: number;
	height: number;
	cssWidth: number;
}

/** 舞台 canvas 的像素统计（每 4 个像素取一个：不同颜色数与滚动哈希）。 */
const canvasStats = (page: Page) =>
	page.evaluate((sel): CanvasStats | null => {
		const c = document.querySelector<HTMLCanvasElement>(sel);
		if (!c?.width) return null;
		const ctx = c.getContext("2d");
		if (!ctx) return null;
		const d = ctx.getImageData(0, 0, c.width, c.height).data;
		const set = new Set<number>();
		let h = 0;
		for (let i = 0; i < d.length; i += 16) {
			const v = ((d[i] ?? 0) << 16) | ((d[i + 1] ?? 0) << 8) | (d[i + 2] ?? 0);
			set.add(v);
			h = (Math.imul(h, 31) + v) >>> 0;
		}
		return {
			distinct: set.size,
			hash: h,
			width: c.width,
			height: c.height,
			cssWidth: c.getBoundingClientRect().width,
		};
	}, CANVAS);

const stageWidth = (page: Page) =>
	page.$eval(STAGE, (el) => (el as HTMLElement).clientWidth);

/** orb 桌面上某页面元素中心的屏幕坐标（经 CDP 取元素矩形 + 窗口位置 + 浏览器边框）。 */
async function remotePoint(orb: string, targetId: string, selector: string) {
	const expression = `(() => { const el = document.querySelector(${JSON.stringify(selector)}); if (!el) return null; const r = el.getBoundingClientRect(); const top = window.outerHeight - window.innerHeight; const left = (window.outerWidth - window.innerWidth) / 2; return { x: window.screenX + left + r.left + r.width / 2, y: window.screenY + top + r.top + r.height / 2 }; })()`;
	const [res] = await cdp(orb, targetId, [
		{ method: "Runtime.evaluate", params: { expression, returnByValue: true } },
	]);
	const v = (res as { result?: { value?: { x: number; y: number } | null } })
		.result?.value;
	if (!v) throw new Error(`orb 页面里没有 ${selector}`);
	return v;
}

/** 把远端屏幕坐标换算成舞台 canvas 上的页面坐标（canvas 的像素宽高 = 远端帧缓冲）。 */
async function stagePoint(page: Page, p: { x: number; y: number }) {
	const v = await page.evaluate(
		({ sel, x, y }) => {
			const c = document.querySelector<HTMLCanvasElement>(sel);
			if (!c?.width) return null;
			const r = c.getBoundingClientRect();
			return {
				x: r.left + (x * r.width) / c.width,
				y: r.top + (y * r.height) / c.height,
			};
		},
		{ sel: CANVAS, x: p.x, y: p.y },
	);
	if (!v) throw new Error("舞台上没有 canvas");
	return v;
}

/** 在舞台上点（或触屏点按）orb 页面里的某个元素。 */
async function clickRemote(
	page: Page,
	orb: string,
	targetId: string,
	selector: string,
	touch = false,
) {
	const pt = await stagePoint(page, await remotePoint(orb, targetId, selector));
	if (touch) await page.touchscreen.tap(pt.x, pt.y);
	else await page.mouse.click(pt.x, pt.y);
}

const focusCanvas = (page: Page) =>
	page.evaluate((sel) => {
		document.querySelector<HTMLCanvasElement>(sel)?.focus();
	}, CANVAS);

// ---------------------------------------------------------------- 共享夹具

let T: Th | null = null;
let b3Hash: number | null = null;

/** 线程 T：不带 Profile，B2 要求刚建好（浏览器没启动过），并在 orb 里起好站点服务。 */
async function ensureT(): Promise<Th> {
	if (T && orbState(T.orb) !== "deleted") {
		awake.add(T.tid);
		T.orb = await ensureReady(T.tid);
		return T;
	}
	if (process.env.M8_THREAD) {
		const tid = process.env.M8_THREAD;
		awake.add(tid);
		T = { tid, orb: await ensureReady(tid) };
		note(`线程 T = ${tid}（M8_THREAD），orb ${T.orb}`);
	} else {
		T = await newThread("T", { browserProfileId: "" });
	}
	await ensureSite(T);
	return T;
}

/** A 上下文的主页面：停在 T，Desktop 面板已连接（面板宽 760、可控制、缩放默认）。 */
async function deskPageA(): Promise<{ page: Page; th: Th }> {
	const th = await ensureT();
	if (pageA && !pageA.isClosed() && pageA.url().includes(th.tid)) {
		await openDesktopTab(pageA);
		await waitConnected(pageA, 30_000);
		return { page: pageA, th };
	}
	pageA = wire(await ctxA.newPage(), "A");
	await pageA.goto(`${BASE}/`);
	await pageA.evaluate(() => {
		localStorage.setItem("oryn.panel.width", "760");
		localStorage.removeItem("oryn.desktop.scale");
		localStorage.removeItem("oryn.desktop.control");
	});
	await pageA.goto(`${BASE}/t/${th.tid}`);
	await openDesktopTab(pageA);
	await waitConnected(pageA, 30_000);
	return { page: pageA, th };
}

// Profile 链（B9–B13、B18）
let P = "";
const sids: string[] = [];
let A2: Th | null = null;
let A3: Th | null = null;

async function ensureP(): Promise<string> {
	if (!P) P = await createProfile(`m8-${RUN}`);
	return P;
}

/** P 当前在一台 ready 的 orb 上被独占持有时，返回那台 orb 与它���线程。 */
async function holderOf(pid: string): Promise<Th | null> {
	const h = (await profileGet(pid)).holder;
	if (!h?.orbId || orbState(h.orbId) !== "ready") return null;
	const tid =
		h.threadId ??
		sql<{ id: string }>(
			`select id from threads where orb_id='${h.orbId}' order by created_at limit 1`,
		)[0]?.id;
	return tid ? { tid, orb: h.orbId } : null;
}

/** A3：独占持有 P 的线程（单独跑时取现有持有者，没有就新建）。 */
async function ensureA3(): Promise<Th> {
	const pid = await ensureP();
	if (!A3 || orbState(A3.orb) === "deleted")
		A3 =
			(await holderOf(pid)) ??
			(await newThread("A3", { browserProfileId: pid }));
	awake.add(A3.tid);
	A3.orb = await ensureReady(A3.tid);
	return A3;
}

/** 关掉 A 上下文里打开这个线程的页面（页面会 focus 线程并保持桌面连接）。 */
async function closePagesOf(tid: string) {
	for (const p of ctxA.pages()) if (p.url().includes(tid)) await p.close();
	if (pageA?.isClosed()) pageA = undefined;
}

/** 让 orb 进入冻结 / 停机前：移出保活、关页面。 */
async function letSleep(th: Th) {
	awake.delete(th.tid);
	await closePagesOf(th.tid);
}

async function listProfileFiles(): Promise<string> {
	return (
		await sh(
			`[ -d ${q(PROFILES_DIR)} ] && find ${q(PROFILES_DIR)} -type f -printf '%P %s\\n' | sort || true`,
		)
	).stdout;
}

// ---------------------------------------------------------------- 场景

async function B1() {
	// Debian 的 TigerVNC 1.15 执行 `Xtigervnc -version` 不输出任何内容，版本改从 dpkg 取
	// （`dpkg-query -W` 输出「包名<Tab>版本」）；同时确认 Xtigervnc 可执行文件在 PATH 上
	const r = await sh(
		`docker run --rm ${IMAGE} bash -lc ${q(
			'command -v Xtigervnc >/dev/null && echo "tigervnc $(dpkg-query -W tigervnc-standalone-server | cut -f2)"; chromium --version; openbox --version | head -1; fc-list :lang=zh | wc -l; echo $ORYN_DESKTOP',
		)}`,
		{ timeoutMs: 180_000 },
	);
	const lines = r.stdout
		.split("\n")
		.map((l) => l.trim())
		.filter(Boolean);
	for (const l of lines) note(l);
	check(r.code === 0 && lines.length === 5, `5 行输出（退出码 ${r.code}）`);
	check(/^tigervnc \d+\.\d+/i.test(lines[0] ?? ""), "TigerVNC 版本");
	check(/chromium \d+/i.test(lines[1] ?? ""), "Chromium 版本");
	check(/openbox \d/i.test(lines[2] ?? ""), "Openbox 版本");
	check(Number(lines[3]) > 0, `中文字体数 ${lines[3]} > 0`);
	check(lines[4] === "1", "ORYN_DESKTOP=1");
	const s = await sh(
		`docker image inspect -f '{{.Size}}' ${IMAGE} ${PRE_IMAGE}`,
	);
	const [now, pre] = s.stdout.split("\n").map(Number);
	if (now && pre) {
		const gb = (now - pre) / 1e9;
		note(
			`${IMAGE} ${(now / 1e9).toFixed(2)}GB，${PRE_IMAGE} ${(pre / 1e9).toFixed(2)}GB`,
		);
		soft(gb >= 0.8 && gb <= 1.1, `体积差 ${gb.toFixed(2)}GB（预期 0.8–1.1GB）`);
	} else note(`镜像体积取不到：${s.out}`);
}

async function B2() {
	const th = await ensureT();
	const orb = th.orb;
	const xv = await dexec(orb, "pgrep -x Xtigervnc");
	const ob = await dexec(orb, "pgrep -x openbox");
	check(xv.code === 0, `Xtigervnc 在运行（${xv.stdout}）`);
	check(ob.code === 0, `openbox 在运行（${ob.stdout}）`);
	const bm = await browserMains(orb);
	check(bm.length === 0, `没有浏览器主进程（${bm.length}）`);
	const net = await dexec(orb, "cat /proc/net/tcp /proc/net/tcp6 2>/dev/null");
	const listen: string[] = [];
	for (const line of net.stdout.split("\n")) {
		const f = line.trim().split(/\s+/);
		if (f[3] !== "0A" || !f[1]?.includes(":")) continue;
		const [hexIp = "", hexPort = ""] = f[1].split(":");
		const port = Number.parseInt(hexPort, 16);
		const ip =
			hexIp.length === 8
				? [6, 4, 2, 0]
						.map((i) => Number.parseInt(hexIp.slice(i, i + 2), 16))
						.join(".")
				: `[v6:${hexIp.slice(-8)}]`;
		listen.push(`${ip}:${port}`);
	}
	note(`监听 ${listen.join(" ")}`);
	check(listen.includes("127.0.0.1:7822"), "监听里有 127.0.0.1:7822");
	const vncPorts = listen.filter((l) => {
		const p = Number(l.split(":").pop());
		return p >= 5900 && p < 6100;
	});
	check(
		vncPorts.length === 0,
		`没有 59xx / 60xx 端口（${vncPorts.join(" ")}）`,
	);
	const sock = await dexec(orb, "test -S ~/.oryn/vnc.sock && echo yes");
	check(sock.stdout === "yes", "~/.oryn/vnc.sock 是 socket");
	const rss = await dexec(orb, "ps -o rss= -C Xtigervnc,openbox");
	const mb =
		rss.stdout
			.split("\n")
			.map((x) => Number(x.trim()) || 0)
			.reduce((a, b) => a + b, 0) / 1024;
	soft(
		mb <= 80,
		`Xtigervnc + openbox RSS 合计 ${mb.toFixed(1)}MB（预期 ≤ 80MB）`,
	);
	const o = await getOrb(orb);
	note(`GET orbs → desktop ${JSON.stringify(o.desktop)}`);
	check(o.desktop?.displayReady === true, "desktop.displayReady == true");
	check(o.desktop?.browser === "stopped", "desktop.browser == stopped");
}

async function B3() {
	const { page, th } = await deskPageA();
	const tabs = await page.$$eval("[data-panel-tab]", (els) =>
		els.map((e) => e.getAttribute("data-panel-tab") ?? ""),
	);
	note(`标签 ${tabs.join(", ")}`);
	const di = tabs.indexOf("desktop");
	const pi = tabs.indexOf("portal");
	check(di >= 0, "有 Desktop 标签");
	check(pi < 0 || di > pi, "Desktop 排在 Portal 之后（若有 Portal）");
	const s0 = await waitFor(
		"canvas 宽度 ≥ 600",
		async () => {
			const s = await canvasStats(page);
			return s && s.cssWidth >= 600 ? s : undefined;
		},
		15_000,
	);
	note(
		`canvas ${s0.width}x${s0.height}，CSS 宽 ${s0.cssWidth.toFixed(0)}，颜色数 ${s0.distinct}`,
	);
	if ((await getOrb(th.orb)).desktop?.browser === "running")
		note("浏览器已在运行（不是全新的 T），跳过点按钮");
	else {
		await page
			.locator("[data-testid=desktop-start-browser]")
			.click({ timeout: 15_000 });
		note("点了「启动浏览器」");
	}
	await waitFor(
		"desktop.browser == running",
		async () => (await getOrb(th.orb)).desktop?.browser === "running",
		60_000,
		1000,
	);
	check(true, "desktop.browser == running");
	const s1 = await waitFor(
		"canvas 颜色数 > 50",
		async () => {
			const s = await canvasStats(page);
			return s && s.distinct > 50 ? s : undefined;
		},
		30_000,
		500,
	);
	check(true, `canvas 不同颜色数 ${s1.distinct} > 50`);
	b3Hash = s1.hash;
	await shot(page, `B3-${RUN}`);
}

async function B4() {
	const { page, th } = await deskPageA();
	const before = await canvasStats(page);
	const reply = await say(
		th.tid,
		`用 browser 工具打开 http://127.0.0.1:${SITE_PORT}/ ，然后只回复页面标题`,
	);
	note(`回复：${reply.slice(0, 200)}`);
	check(reply.includes("m8-home"), "回复含 m8-home");
	const list = await cdpList(th.orb);
	check(
		list.some((t) => t.url.startsWith(`http://127.0.0.1:${SITE_PORT}`)),
		"/json/list 里有该 URL 的页面",
	);
	const bm = await browserMains(th.orb);
	note(`浏览器主进程 ${bm.map((p) => p.pid).join(",")}`);
	check(bm.length === 1, "浏览器主进程恰好 1 个");
	check(
		bm[0]?.cmd.includes(`user-data-dir=${ORB_PROFILE_DIR}`) === true,
		`命令行含 user-data-dir=${ORB_PROFILE_DIR}`,
	);
	const ref = b3Hash ?? before?.hash ?? null;
	if (ref === null) note("没有 B3 的画面可比（单独跑）");
	else {
		const s = await waitFor(
			"canvas 画面与 B3 不同",
			async () => {
				const x = await canvasStats(page);
				return x && x.hash !== ref ? x : undefined;
			},
			15_000,
			500,
		);
		check(true, `canvas 画面哈希 ${s.hash} ≠ ${ref}`);
	}
	await shot(page, `B4-${RUN}`);
}

async function B5() {
	const { page, th } = await deskPageA();
	const id = await openSitePage(th.orb);
	const title = () => targetTitle(th.orb, id);
	await clickRemote(page, th.orb, id, "#i");
	await page.keyboard.type("abc");
	await waitFor(
		"标题 typed:abc",
		async () => (await title()) === "typed:abc",
		10_000,
		300,
	);
	check(true, "控制模式输入 → 标题 typed:abc");
	const ctl = page.locator("[data-testid=desktop-control]");
	await ctl.click();
	await page
		.getByText("仅观看", { exact: true })
		.first()
		.waitFor({ timeout: 5_000 });
	note("已切到仅观看");
	await focusCanvas(page);
	await page.keyboard.type("zzz");
	await holdFor(
		"观看期间标题不变",
		async () => (await title()) === "typed:abc",
		2_000,
		300,
	);
	check(true, "观看期间 2 秒内标题保持 typed:abc");
	await ctl.click();
	await focusCanvas(page);
	await page.keyboard.type("d");
	await waitFor(
		"标题 typed:abcd",
		async () => (await title()) === "typed:abcd",
		10_000,
		300,
	);
	check(true, "切回控制后输入生效（typed:abcd）");
}

async function B6() {
	const { th } = await deskPageA();
	const reply = await say(
		th.tid,
		"用 computer 工具截一次屏，只回复截图的宽x高",
	);
	note(`回复：${reply.slice(0, 200)}`);
	const m = /(\d{3,5})\s*[xX×*]\s*(\d{3,5})/.exec(reply);
	check(!!m, "回复里有 <宽>x<高>");
	const d = await xdpy(th.orb);
	note(`xdpyinfo ${d.w}x${d.h}`);
	check(
		Number(m?.[1]) === d.w && Number(m?.[2]) === d.h,
		"回复尺寸 = xdpyinfo",
	);
	const env = await dexec(
		th.orb,
		"for p in $(pgrep -f '[-]-mode rpc-ui'); do tr '\\0' '\\n' < /proc/$p/environ | grep '^DISPLAY='; done",
	);
	note(`omp 进程环境 ${env.stdout.replaceAll("\n", " ")}`);
	check(env.stdout.split("\n").includes("DISPLAY=:1"), "omp 环境含 DISPLAY=:1");
}

async function B7() {
	const { page, th } = await deskPageA();
	const reloadWith = async (width: number) => {
		await page.evaluate(
			(w) => localStorage.setItem("oryn.panel.width", String(w)),
			width,
		);
		await page.reload();
		await openDesktopTab(page);
		await waitConnected(page, 30_000);
	};
	await page.evaluate(() =>
		localStorage.setItem("oryn.desktop.scale", "follow"),
	);
	const widths: number[] = [];
	for (const w of [760, 900]) {
		await reloadWith(w);
		const sw = await stageWidth(page);
		const d = await waitFor(
			`远端宽度跟随舞台（${sw}）`,
			async () => {
				const x = await xdpy(th.orb);
				return Math.abs(x.w - sw) <= 2 ? x : undefined;
			},
			20_000,
			500,
		);
		note(`面板 ${w}：舞台 clientWidth ${sw}，xdpyinfo ${d.w}x${d.h}`);
		widths.push(d.w);
	}
	check(widths[0] !== widths[1], `两次远端宽度不同（${widths.join(" / ")}）`);
	const more = page
		.locator("[data-testid=desktop-panel] [role=toolbar]")
		.getByRole("button", { name: "更多", exact: true });
	await more.click();
	await page.getByRole("menuitemradio", { name: "缩放以适应" }).click();
	note("「更多」→「缩放以适应」");
	const fixed = await xdpy(th.orb);
	await reloadWith(760);
	await holdFor(
		"切到适应后远端尺寸不变",
		async () => {
			const x = await xdpy(th.orb);
			return x.w === fixed.w && x.h === fixed.h;
		},
		5_000,
		500,
	);
	check(true, `适应模式下面板改回 760，远端保持 ${fixed.w}x${fixed.h}`);
	const s = await canvasStats(page);
	const sw = await stageWidth(page);
	check(
		!!s && s.cssWidth <= sw + 1,
		`canvas CSS 宽 ${s?.cssWidth.toFixed(0)} ≤ 容器 ${sw}`,
	);
	await page.evaluate(() => localStorage.removeItem("oryn.desktop.scale"));
	await reloadWith(760);
}

async function B8() {
	const { page, th } = await deskPageA();
	await page.bringToFront();
	const toolbar = page.locator("[data-testid=desktop-panel] [role=toolbar]");
	await toolbar.getByRole("button", { name: "剪贴板", exact: true }).click();
	const pop = page.locator("[data-testid=desktop-clipboard]");
	await pop.waitFor({ timeout: 10_000 });
	const t1 = `发往桌面-${RUN}`;
	await pop.getByRole("textbox", { name: "发送到桌面" }).fill(t1);
	await pop.getByRole("button", { name: "发送到桌面" }).click();
	const got = await waitFor(
		"桌面剪贴板 = 发送的文本",
		async () => {
			const r = await dexec(th.orb, "DISPLAY=:1 xclip -o -selection clipboard");
			return r.stdout === t1 ? r.stdout : undefined;
		},
		15_000,
		500,
	);
	check(true, `① xclip -o = ${got}`);
	const toDesk = (s: string) =>
		dexec(
			th.orb,
			`printf %s ${q(s)} | DISPLAY=:1 xclip -selection clipboard >/dev/null 2>&1`,
		);
	const t2 = `来自桌面-${RUN}`;
	await toDesk(t2);
	await waitFor(
		"弹层显示桌面剪贴板",
		async () =>
			(await pop
				.locator("[data-testid=desktop-clip-remote]")
				.textContent()
				.catch(() => "")) === t2,
		15_000,
		300,
	);
	check(true, `② 弹层显示 ${t2}`);
	// 桌面 → 本机只在点「复制到本机」时写本机剪贴板（不再有自动同步）
	const readLocal = () =>
		page.evaluate(() => navigator.clipboard.readText()).catch(() => "");
	const sentinel = `本机原值-${RUN}`;
	await page.evaluate((s) => navigator.clipboard.writeText(s), sentinel);
	const t3 = `来自桌面2-${RUN}`;
	await toDesk(t3);
	await waitFor(
		"弹层显示新的桌面剪贴板",
		async () =>
			(await pop
				.locator("[data-testid=desktop-clip-remote]")
				.textContent()
				.catch(() => "")) === t3,
		15_000,
		300,
	);
	await holdFor(
		"未点「复制到本机」时本机剪贴板不变",
		async () => (await readLocal()) === sentinel,
		2_000,
		250,
	);
	check(true, "③ 未点「复制到本机」前本机剪贴板保持原值");
	await pop.locator("[data-testid=desktop-clip-copy]").click();
	const local = await waitFor(
		"本机剪贴板 = 桌面文本",
		async () => {
			const v = await readLocal();
			return v === t3 ? v : undefined;
		},
		15_000,
		500,
	);
	check(true, `③ 点「复制到本机」后 readText() = ${local}`);
	await page.keyboard.press("Escape");
}

async function B9() {
	A2 = null;
	A3 = null;
	const pid = await ensureP();
	const a1 = await newThread("A1", { browserProfileId: pid });
	note(
		`A1 就绪时 desktop.browser = ${(await getOrb(a1.orb)).desktop?.browser}`,
	);
	const t = await dexec(
		a1.orb,
		`curl -s -o /dev/null --max-time 30 -w '%{http_code} %{time_total}' ${CDP}/json/version`,
	);
	note(`冷启动 json/version → ${t.stdout}`);
	const [code, secs] = t.stdout.split(" ");
	check(code === "200" && Number(secs) < 6, "冷启动的 curl 6 秒内返回 200");
	const r = `r${RUN}a`;
	sids.push(r);
	await browserLogin(a1.orb, r, "A1");
	awake.delete(a1.tid);
	await deleteOrb(a1.orb, true);
	const p = await waitFor(
		"P 修订前进且无持有者",
		async () => {
			const x = await profileGet(pid);
			return x.revision >= 1 && !x.holder ? x : undefined;
		},
		60_000,
		1000,
	);
	note(
		`P revision ${p.revision} sizeBytes ${p.sizeBytes} holder ${JSON.stringify(p.holder ?? null)}`,
	);
	check(p.sizeBytes > 0, "sizeBytes > 0、无 holder");
	A2 = await newThread("A2", { browserProfileId: pid });
	const w = await whoami(A2.orb, "A2");
	check(cookieVal(w.cookie, "sid") === r, `A2 的请求带 Cookie sid=${r}`);
	check(w.ls === r, `/seen?ls=${r}`);
}

async function B10() {
	const pid = await ensureP();
	if (!A2 || orbState(A2.orb) !== "ready")
		A2 =
			(await holderOf(pid)) ??
			(await newThread("A2", { browserProfileId: pid }));
	const a2 = A2;
	awake.add(a2.tid);
	const held = await profileGet(pid);
	check(held.holder?.orbId === a2.orb, "P 由 A2 的 orb 独占持有");
	const n0 = sql<{ n: number }>("select count(*) n from threads")[0]?.n ?? 0;
	const c = await postThread({ browserProfileId: pid });
	note(`exclusive → ${c.status} ${c.text.slice(0, 300)}`);
	check(
		c.status === 409 && codeOf(c) === "profile_in_use",
		"409 profile_in_use",
	);
	const body = c.json as { profileId?: string; holder?: Holder };
	check(
		body.profileId === pid &&
			body.holder?.orbId === a2.orb &&
			!!body.holder.since,
		"错误体带 profileId、持有者 orb 与开始时间",
	);
	soft(
		!!body.holder?.threadTitle,
		`错误体带持有者线程标题「${body.holder?.threadTitle ?? ""}」`,
	);
	check(
		(sql<{ n: number }>("select count(*) n from threads")[0]?.n ?? 0) === n0,
		"409 时没有建线程",
	);
	const r2 = `r${RUN}b`;
	sids.push(r2);
	const sig0 = await cookieSig(a2.orb, "sid");
	await browserLogin(a2.orb, r2, "A2");
	await waitCookieOnDisk(a2.orb, "sid", sig0);
	A3 = await newThread("A3", {
		browserProfileId: pid,
		profileMode: "takeover",
	});
	check(true, "takeover → 201");
	const a3 = A3;
	const p = await profileGet(pid);
	check(p.holder?.orbId === a3.orb, "P 的 holder.orbId 是 A3 的 orb");
	const o2 = await getOrb(a2.orb);
	const dp = o2.desktop?.profile;
	note(`A2 desktop ${JSON.stringify(o2.desktop)}`);
	check(
		dp?.status === "readonly" && dp.readonlyReason === "taken_over",
		"A2 降级为只读（status=readonly、readonlyReason=taken_over）",
	);
	check(o2.desktop?.browser === "running", "A2 的浏览器照常运行");
	const ex = await dexec(
		a2.orb,
		`test -e ${ORB_PROFILE_DIR}/Default && echo yes || echo no`,
	);
	check(ex.stdout === "yes", "A2 的本地 Profile 目录保留");
	const w = await whoami(a3.orb, "A3");
	check(cookieVal(w.cookie, "sid") === r2, `A3 的请求带 Cookie sid=${r2}`);
	await withPage(ctxA, "A10", async (page) => {
		await page.goto(`${BASE}/t/${a2.tid}`);
		await openDesktopTab(page);
		const panel = page.locator("[data-testid=desktop-panel]");
		const text = await waitFor(
			"A2 的 Desktop 面板显示接管横幅",
			async () => {
				const s = (await panel.textContent()) ?? "";
				return /只读 Profile · 已被「.+」接管/.test(s) ? s : undefined;
			},
			30_000,
			500,
		);
		check(true, `A2 面板：${/只读 Profile · 已被「.+」接管/.exec(text)?.[0]}`);
		await shot(page, `B10-A2-${RUN}`);
	});
	awake.delete(a2.tid);
	await deleteOrb(a2.orb, false);
	A2 = null;
}

async function B11() {
	const pid = await ensureP();
	check(
		sids.length > 0,
		`本次运行已写入的 sid：${sids.join(", ")}（依赖 B9 / B10）`,
	);
	const g = await sh(
		`grep -rlF ${sids.map((s) => `-e ${q(s)}`).join(" ")} ${q(PROFILES_DIR)} | wc -l`,
	);
	check(g.stdout === "0", `grep 明文 sid 命中 ${g.stdout} 个文件`);
	const files = (
		await sh(`find ${q(`${PROFILES_DIR}/${pid}`)} -type f -name '*.oss'`)
	).stdout
		.split("\n")
		.filter(Boolean);
	check(files.length >= 1, `P 的归档 ${files.length} 个`);
	for (const f of files) {
		const head = (await sh(`head -c 4 ${q(f)}`)).stdout;
		const mode = (await sh(`stat -c %a ${q(f)}`)).stdout;
		note(`${f.slice(PROFILES_DIR.length + 1)} 头 ${head} 权限 ${mode}`);
		check(head === "OSS1" && mode === "600", "OSS1、600");
	}
	const row = (
		await sh(`sqlite3 -json ${q(DB)} "select * from browser_profiles"`)
	).stdout;
	check(
		!sids.some((s) => row.includes(s)),
		"browser_profiles 行里没有 Cookie 值",
	);
	const a = await api("GET", `/api/v1/browser-profiles/${pid}/archive`);
	check(a.status === 404, `GET …/archive → ${a.status}`);
}

async function B12() {
	const a3 = await ensureA3();
	await ensureSite(a3);
	const siteId = await openSitePage(a3.orb);
	const pid0 = (await browserMains(a3.orb))[0]?.pid ?? 0;
	check(pid0 > 0, `冻结前浏览器主进程 ${pid0}`);
	await letSleep(a3);
	note("已关闭 A3 的面板并停止保活，等空闲冻结");
	await waitOrbState(a3.orb, "frozen", 300_000);
	const p = await profileGet(P);
	check(!p.holder, "冻结后 P 无 holder");
	awake.add(a3.tid);
	await api("POST", `/api/v1/threads/${a3.tid}/wake`);
	await ensureReady(a3.tid);
	const pid1 = (await browserMains(a3.orb))[0]?.pid ?? 0;
	check(pid1 === pid0, `唤醒后浏览器主进程 pid 不变（${pid1}）`);
	const list = await cdpList(a3.orb);
	check(
		list.some((t) => t.id === siteId),
		"/json/list 仍有冻结前打开的页面",
	);
	await waitFor(
		"P 的 holder 又是 A3 的 orb",
		async () => (await profileGet(P)).holder?.orbId === a3.orb,
		30_000,
		1000,
	);
	check(true, "P 的 holder 又是 A3 的 orb");
}

async function B13() {
	const a3 = await ensureA3();
	const pidBefore = (await browserMains(a3.orb))[0]?.pid ?? 0;
	note(`冻结前浏览器主进程 ${pidBefore || "无"}`);
	await letSleep(a3);
	await pauseOrb(a3.orb, "freeze");
	const a4 = await newThread("A4", { browserProfileId: P });
	check(true, "A4（不带接管）201");
	const r3 = `r${RUN}c`;
	sids.push(r3);
	await browserLogin(a4.orb, r3, "A4");
	awake.delete(a4.tid);
	await deleteOrb(a4.orb, true);
	await waitFor(
		"P 无持有者",
		async () => !(await profileGet(P)).holder,
		60_000,
		1000,
	);
	awake.add(a3.tid);
	await api("POST", `/api/v1/threads/${a3.tid}/wake`);
	await ensureReady(a3.tid);
	const pidAfter = (await browserMains(a3.orb))[0]?.pid ?? 0;
	check(
		pidAfter !== pidBefore,
		`唤醒后浏览器主进程与冻结前不同（${pidAfter || "未运行"}）`,
	);
	const w = await whoami(a3.orb, "A3");
	check(cookieVal(w.cookie, "sid") === r3, `A3 的请求带 Cookie sid=${r3}`);
}

async function B14() {
	const th = await ensureT();
	const orb = th.orb;
	const code = th.tid.slice(-12);
	const wsUrl = `http://127.0.0.1:${BASE_PORT}/api/v1/orbs/${orb}/desktop/ws`;
	const upgrade = async (url: string, headers: string[] = []) => {
		const r = await sh(
			`curl -s -o /dev/null -w '%{http_code}' --max-time 4 -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' ${headers.map((h) => `-H ${q(h)}`).join(" ")} ${q(url)}`,
		);
		return Number(r.stdout.trim());
	};
	// ① 票据
	const noTicket = await upgrade(wsUrl);
	check(noTicket === 401, `① 无票据 → ${noTicket}`);
	const tk = (
		(await api("POST", "/api/v1/ws-ticket")).json as { ticket: string }
	).ticket;
	const first = await upgrade(`${wsUrl}?ticket=${encodeURIComponent(tk)}`);
	note(`① 票据第一次 → ${first}`);
	const second = await upgrade(`${wsUrl}?ticket=${encodeURIComponent(tk)}`);
	check(second === 401, `① 同一张票据第二次 → ${second}`);
	const anonTicket = await req("POST", "/api/v1/ws-ticket", {
		headers: { "X-Oryn-CSRF": "1" },
	});
	check(
		anonTicket.status === 401,
		`① 匿名 POST ws-ticket → ${anonTicket.status}`,
	);
	// ② portal 主机上的 7822
	const host = `t-${code}-p7822.${BASE_HOST}`;
	const pv = await sh(
		`curl -s -m 30 -w '\\n%{http_code}' -H ${q(`Host: ${host}`)} -H ${q(`Authorization: Bearer ${TOKEN}`)} ${q(`http://127.0.0.1:${BASE_PORT}/json/version`)}`,
	);
	const nl = pv.stdout.lastIndexOf("\n");
	const pvStatus = Number(nl >= 0 ? pv.stdout.slice(nl + 1) : pv.stdout);
	const pvBody = nl >= 0 ? pv.stdout.slice(0, nl) : "";
	note(`② ${host}/json/version → ${pvStatus} ${pvBody.slice(0, 120)}`);
	check(
		pvStatus === 403 &&
			pvBody.includes("forbidden_port") &&
			!pvBody.includes("webSocketDebuggerUrl"),
		"② 403 forbidden_port，不是 Chromium 的 JSON",
	);
	const tk2 = (
		(await api("POST", "/api/v1/ws-ticket")).json as { ticket: string }
	).ticket;
	const viaPortal = await upgrade(
		`http://127.0.0.1:${BASE_PORT}/api/v1/orbs/${orb}/desktop/ws?ticket=${encodeURIComponent(tk2)}`,
		[`Host: ${host}`, `Authorization: Bearer ${TOKEN}`],
	);
	check(
		viaPortal !== 101,
		`② portal 主机上的桌面 WS → ${viaPortal}（不是 101）`,
	);
	// ③ 声明保留端口的服务
	const svcName = `m8x-${RUN}`;
	const sv = await dexec(
		orb,
		`oryn orb service start ${svcName} --command 'sleep 1000' --port 7822 --portal`,
		{ ORYN_THREAD_ID: th.tid },
	);
	note(
		`③ service start → 退出码 ${sv.code}：${sv.out.split("\n").slice(-2).join(" / ")}`,
	);
	check(sv.code === 1, "③ 退出码 1");
	const ls = await dexec(orb, "oryn orb service ls --json", {
		ORYN_THREAD_ID: th.tid,
	});
	const svc = (
		JSON.parse(ls.stdout) as {
			services: { name: string; status: string; error?: string }[];
		}
	).services.find((s) => s.name === svcName);
	check(
		svc?.status === "failed" && svc.error === "forbidden_port",
		`③ 服务 failed、error=forbidden_port（${svc?.status} ${svc?.error}）`,
	);
	await dexec(orb, `oryn orb service stop ${svcName}`, {
		ORYN_THREAD_ID: th.tid,
	});
	// ④ 公开分享页
	const sh1 = await api("POST", `/api/v1/threads/${th.tid}/shares`, {
		mode: "snapshot",
		acknowledgeFindings: true,
	});
	check(sh1.status === 201, `④ 建分享 ${sh1.status}`);
	const share = sh1.json as { id: string; slug: string };
	try {
		await withPage(ctxC, "C14", async (page) => {
			const paths: string[] = [];
			page.on("request", (r) => paths.push(new URL(r.url()).pathname));
			page.on("websocket", (w) => paths.push(new URL(w.url()).pathname));
			await page.goto(`${BASE}/s/${share.slug}`);
			await page
				.waitForLoadState("networkidle", { timeout: 30_000 })
				.catch(() => {});
			await page
				.locator("[data-role=user]")
				.first()
				.waitFor({ timeout: 30_000 })
				.catch(() => note("④ 分享页没找到用户消息（只看网络请求）"));
			note(`④ 分享页请求 ${paths.length} 个`);
			check(
				!paths.some((p) => p.includes("/desktop/ws")),
				"④ 分享页没有 /desktop/ws 请求",
			);
			const o = await page.request.get(`${BASE}/api/v1/orbs/${orb}`);
			check(o.status() === 401, `④ 匿名 GET orbs → ${o.status()}`);
		});
	} finally {
		await api("DELETE", `/api/v1/shares/${share.id}`);
	}
	// ⑤ 登出后桌面 WS 以 4401 关闭
	await withPage(ctxB, "B14", async (page) => {
		await page.goto(`${BASE}/t/${th.tid}`);
		await openDesktopTab(page);
		await waitConnected(page, 30_000);
		const t0 = Date.now();
		const lo = await req("POST", "/api/v1/auth/logout", {
			headers: { Cookie: `__Host-oryn_session=${SESSION}`, "X-Oryn-CSRF": "1" },
		});
		note(`⑤ logout → ${lo.status}`);
		const closed = await waitFor(
			"B 的桌面 WS 关闭",
			() =>
				page.evaluate(() =>
					(
						window as unknown as {
							__wsCloses: { url: string; code: number; t: number }[];
						}
					).__wsCloses.find((c) => c.url.includes("/desktop/ws")),
				),
			5_000,
			100,
		);
		note(`⑤ 关闭码 ${closed.code}，${closed.t - t0}ms`);
		check(
			closed.code === 4401 && closed.t - t0 <= 2_000,
			"⑤ 2 秒内以 4401 关闭",
		);
	});
}

async function B15() {
	check(
		!!OLD_THREAD,
		`旧镜像线程 ${OLD_THREAD || "（.dev/m8-old.thread 不存在）"}`,
	);
	awake.add(OLD_THREAD);
	try {
		const orb = await ensureReady(OLD_THREAD, { desktop: false });
		const img = await sh(`docker inspect -f '{{.Config.Image}}' ${orb}`);
		note(`旧 orb ${orb}，镜像 ${img.stdout}`);
		const o = await getOrb(orb);
		check(!o.desktop, "Orb 没有 desktop 字段");
		const pid = await ensureP();
		const r = await api("PUT", `/api/v1/orbs/${orb}/desktop/profile`, {
			profileId: pid,
		});
		note(`PUT desktop/profile → ${r.status} ${r.text.slice(0, 160)}`);
		check(
			r.status === 409 && codeOf(r) === "orbd_outdated",
			"409 orbd_outdated",
		);
		await withPage(ctxA, "A15", async (page) => {
			await page.goto(`${BASE}/t/${OLD_THREAD}`);
			await page
				.locator("[data-panel-tab=files]")
				.first()
				.waitFor({ timeout: 60_000 });
			check((await page.locator(DESK_TAB).count()) === 0, "没有 Desktop 标签");
			await page.locator("[data-panel-tab=files]").first().click();
			// 文件树是 WAI-ARIA tree（FileTree.tsx 根节点 role="tree"），没有 data-testid
			await page
				.locator("[role=tree] [role=treeitem]")
				.first()
				.waitFor({ timeout: 60_000 });
			check(true, "Files 面板正常（文件树有条目）");
			await page.locator("[data-panel-tab=terminal]").first().click();
			await page
				.locator("[data-testid=terminal-panel]")
				.waitFor({ timeout: 60_000 });
			check(true, "Terminal 面板正常");
			await shot(page, `B15-old-${RUN}`);
		});
		const name = `m8old-${RUN}`;
		const st = await dexec(
			orb,
			`oryn orb service start ${name} --command 'python3 -m http.server "$PORT"' --portal`,
			{ ORYN_THREAD_ID: OLD_THREAD },
		);
		note(`旧 orb 起服务 → ${st.code} ${st.out.split("\n").pop() ?? ""}`);
		try {
			const port = await waitFor(
				"旧 orb 服务 running",
				async () => {
					const ls = await dexec(orb, "oryn orb service ls --json", {
						ORYN_THREAD_ID: OLD_THREAD,
					});
					const s = (
						JSON.parse(ls.stdout) as {
							services: { name: string; status: string; port?: number }[];
						}
					).services.find((x) => x.name === name);
					return s?.status === "running" ? s.port : undefined;
				},
				60_000,
				1000,
			);
			const host = `t-${OLD_THREAD.slice(-12)}-p${port}.${BASE_HOST}`;
			const pr = await sh(
				`curl -s -m 30 -w '\\n%{http_code}' -H ${q(`Host: ${host}`)} -H ${q(`Authorization: Bearer ${TOKEN}`)} -H 'Accept: text/html' ${q(`http://127.0.0.1:${BASE_PORT}/`)}`,
			);
			const nl = pr.stdout.lastIndexOf("\n");
			const status = Number(pr.stdout.slice(nl + 1));
			note(`Portal ${host} → ${status}`);
			check(
				status === 200 && pr.stdout.includes("Directory listing"),
				"旧 orb 的 Portal 照常",
			);
		} finally {
			await dexec(orb, `oryn orb service stop ${name}`, {
				ORYN_THREAD_ID: OLD_THREAD,
			});
		}
	} finally {
		awake.delete(OLD_THREAD);
	}
}

async function B16() {
	const th = await ensureT();
	await withPage(ctxM, "M16", async (page) => {
		const siteId = await openSitePage(th.orb);
		const before = await xdpy(th.orb);
		note(`连接前远端 ${before.w}x${before.h}`);
		await page.goto(`${BASE}/t/${th.tid}`);
		await openDesktopTab(page);
		await waitConnected(page, 30_000);
		check(true, "手机视口已连接");
		await holdFor(
			"连接后远端尺寸不变",
			async () => {
				const x = await xdpy(th.orb);
				return x.w === before.w && x.h === before.h;
			},
			3_000,
			500,
		);
		check(true, "远端尺寸不变（默认适应）");
		const s = await canvasStats(page);
		const sw = await stageWidth(page);
		check(
			!!s && s.cssWidth <= sw + 1,
			`canvas CSS 宽 ${s?.cssWidth.toFixed(0)} ≤ 容器 ${sw}`,
		);
		await clickRemote(page, th.orb, siteId, "#i", true);
		await page.locator("[data-testid=desktop-keyboard]").click();
		const active = await waitFor(
			"软键盘输入框获得焦点",
			() =>
				page.evaluate(() =>
					(document.activeElement as HTMLElement | null)?.dataset.testid ===
					"desktop-kb-input"
						? "desktop-kb-input"
						: undefined,
				),
			5_000,
		);
		check(true, `document.activeElement = ${active}`);
		await page.keyboard.type("m");
		const title = await waitFor(
			"标题以 m 结尾的 typed:…",
			async () => {
				const t = await targetTitle(th.orb, siteId);
				return /^typed:.*m$/.test(t) ? t : undefined;
			},
			10_000,
			300,
		);
		check(true, `标题 ${title}`);
		await clickRemote(page, th.orb, siteId, "button", true);
		await waitFor(
			"点按后标题 clicked",
			async () => (await targetTitle(th.orb, siteId)) === "clicked",
			10_000,
			300,
		);
		check(true, "点按舞台下半部 → 标题 clicked");
		await shot(page, `B16-mobile-${RUN}`);
	});
}

// ---------------------------------------------------------------- 真跑：Xvfb 上的真实 App 与有界面 Chromium（M8_B20_APP=1）

const REAL = process.env.M8_B20_APP === "1";
const REAL_HINT =
	"设 M8_B20_APP=1 开启真跑：M8_B20_APP=1 bun run accept:m8 -- --only B17,B20（Xvfb + openbox + 真实 Tauri App / 有界面 Chromium，xdotool 发真实按键）";
const APP_BIN =
	process.env.ORYN_DESKTOP_BIN ??
	`${ROOT}/web/src-tauri/target/release/oryn-desktop`;
const TLS_DIR = `${ROOT}/.dev/tls`;

type Env = Record<string, string | undefined>;

interface XEnv {
	tmp: string;
	display: string;
	/** App 连接的 https origin（dev/tlsproxy → BASE） */
	origin: string;
	/** WebKit 远程检查器的 HTTP 端口 */
	inspector: number;
	/** 显示、私有会话总线与运行时目录（App 与有界面 Chromium 共用） */
	env: Record<string, string>;
	events: string;
	procs: Subprocess[];
	/** 本次运行临时把 .dev/tls/ca.crt 加进了系统信任库（结束时撤销） */
	caAdded: boolean;
	bin?: string;
	app?: Subprocess;
	deviceId?: string;
}
let X: XEnv | null = null;

function freePort(): number {
	const s = Bun.listen({
		hostname: "127.0.0.1",
		port: 0,
		socket: { data() {} },
	});
	const p = s.port;
	s.stop(true);
	return p;
}

/** 后台进程，输出写 $tmp/<name>.log；teardownX 统一结束。 */
function xbg(x: XEnv, name: string, cmd: string[], env: Env = x.env) {
	const fd = openSync(`${x.tmp}/${name}.log`, "a");
	const p = Bun.spawn(cmd, {
		cwd: ROOT,
		env,
		stdin: "ignore",
		stdout: fd,
		stderr: fd,
	});
	x.procs.push(p);
	return p;
}

/** 在 Xvfb 显示上跑 shell（xdotool / xclip / import / xprop）。 */
const xsh = (x: XEnv, cmd: string) =>
	sh(`export DISPLAY=${x.display}; ${cmd}`, { timeoutMs: 60_000 });

async function xshot(name: string) {
	if (!X) return;
	const p = `${SHOTS}/${name}-${RUN}.png`;
	if ((await xsh(X, `import -window root ${q(p)}`)).code === 0)
		note(`截图 ${p}`);
}

/** Xvfb + openbox + 私有会话总线 + tlsproxy（+ 需要时临时信任本机 CA）。 */
async function ensureX(): Promise<XEnv> {
	if (X) return X;
	const tools = [
		"Xvfb",
		"openbox",
		"xdotool",
		"xclip",
		"xprop",
		"import",
		"dbus-daemon",
		"trust",
		"go",
		"curl",
	];
	const missing = tools.filter((t) => !Bun.which(t));
	if (missing.length)
		throw new Error(
			`缺少工具：${missing.join(" ")}（Arch：pacman -S --needed xorg-server-xvfb openbox xdotool xclip xorg-xprop imagemagick）`,
		);
	if (!existsSync(`${TLS_DIR}/ca.crt`))
		throw new Error(
			`缺少 ${TLS_DIR}/ca.crt：先 go run ./dev/tlsproxy -init -dir .dev/tls`,
		);
	const tmp = mkdtempSync(`${tmpdir()}/oryn-m8app-`);
	mkdirSync(`${tmp}/run`, { recursive: true });
	chmodSync(`${tmp}/run`, 0o700);
	mkdirSync(`${tmp}/fakebin`, { recursive: true });
	// App 配对时会 xdg-open 验证页：只记下来，不在宿主上开浏览器
	writeFileSync(
		`${tmp}/fakebin/xdg-open`,
		`#!/bin/sh\nprintf '%s\\n' "$*" >> ${q(`${tmp}/xdg-open.log`)}\n`,
	);
	chmodSync(`${tmp}/fakebin/xdg-open`, 0o755);
	let n = 91;
	while (existsSync(`/tmp/.X11-unix/X${n}`) || existsSync(`/tmp/.X${n}-lock`))
		n++;
	const display = `:${n}`;
	const bus = `${tmp}/run/bus`;
	const tlsPort = freePort();
	const base: Record<string, string> = {};
	for (const [k, v] of Object.entries(process.env))
		if (v !== undefined) base[k] = v;
	const x: XEnv = {
		tmp,
		display,
		origin: `https://localhost:${tlsPort}`,
		inspector: freePort(),
		env: {
			...base,
			DISPLAY: display,
			DBUS_SESSION_BUS_ADDRESS: `unix:path=${bus}`,
			XDG_RUNTIME_DIR: `${tmp}/run`,
			PATH: `${tmp}/fakebin:${process.env.PATH ?? ""}`,
		},
		events: `${tmp}/events.jsonl`,
		procs: [],
		caAdded: false,
	};
	X = x;
	xbg(x, "xvfb", [
		"Xvfb",
		display,
		"-screen",
		"0",
		"1440x900x24",
		"-nolisten",
		"tcp",
	]);
	await waitFor(
		`Xvfb ${display} 就绪`,
		() => existsSync(`/tmp/.X11-unix/X${n}`),
		10_000,
	);
	xbg(x, "openbox", ["openbox"]);
	// 不带任何 service 目录的会话总线：单实例与深链接照常可用，也不会 D-Bus 激活出读写真实 HOME 的凭据库（App 退回文件存令牌）
	writeFileSync(
		`${tmp}/bus.conf`,
		`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig><type>session</type><listen>unix:path=${bus}</listen><auth>EXTERNAL</auth>
<policy context="default"><allow send_destination="*" eavesdrop="true"/><allow eavesdrop="true"/><allow own="*"/></policy></busconfig>
`,
	);
	xbg(x, "dbus", ["dbus-daemon", "--nofork", `--config-file=${tmp}/bus.conf`]);
	await waitFor("私有会话总线就绪", () => existsSync(bus), 10_000);
	const build = await sh(`go build -o ${q(`${tmp}/tlsproxy`)} ./dev/tlsproxy`, {
		timeoutMs: 300_000,
	});
	if (build.code !== 0) throw new Error(`构建 tlsproxy 失败：${build.out}`);
	xbg(x, "tlsproxy", [
		`${tmp}/tlsproxy`,
		"-listen",
		`127.0.0.1:${tlsPort}`,
		"-target",
		BASE,
		"-dir",
		TLS_DIR,
	]);
	const healthy = async (insecure: boolean) =>
		(
			await sh(
				`curl -s ${insecure ? "-k " : ""}-o /dev/null -w '%{http_code}' ${x.origin}/healthz`,
			)
		).stdout === "200";
	await waitFor("tlsproxy 就绪", () => healthy(true), 15_000, 300);
	if (!(await healthy(false))) {
		const r = await sh(`trust anchor --store ${q(`${TLS_DIR}/ca.crt`)}`);
		if (r.code !== 0) throw new Error(`trust anchor --store 失败：${r.out}`);
		x.caAdded = true;
		note(
			"临时信任 .dev/tls/ca.crt（trust anchor --store；CA 带名称约束，只能签 localhost；结束时 --remove）",
		);
		await waitFor("系统信任本机 CA", () => healthy(false), 10_000, 500);
	}
	note(`真跑环境：Xvfb ${display} + openbox，App 连接 ${x.origin} → ${BASE}`);
	return x;
}

function appEnv(x: XEnv): Record<string, string> {
	return {
		...x.env,
		XDG_CONFIG_HOME: `${x.tmp}/app/config`,
		XDG_DATA_HOME: `${x.tmp}/app/data`,
		XDG_CACHE_HOME: `${x.tmp}/app/cache`,
		ORYN_DESKTOP_EVENT_LOG: x.events,
		// Xvfb 下 WebKitGTK 的 DMA-BUF / 合成渲染不可用
		WEBKIT_DISABLE_DMABUF_RENDERER: "1",
		WEBKIT_DISABLE_COMPOSITING_MODE: "1",
		NO_AT_BRIDGE: "1",
		// 只用来读 DOM 与坐标（WebKit 检查器协议：Target.sendMessageToTarget → Runtime.evaluate）
		WEBKIT_INSPECTOR_HTTP_SERVER: `127.0.0.1:${x.inspector}`,
	};
}

interface AppEv {
	event: string;
	[k: string]: unknown;
}

function appEvents(x: XEnv): AppEv[] {
	if (!existsSync(x.events)) return [];
	const out: AppEv[] = [];
	for (const line of readFileSync(x.events, "utf8").split("\n")) {
		if (!line.trim()) continue;
		try {
			out.push(JSON.parse(line) as AppEv);
		} catch {
			// 写了一半的行
		}
	}
	return out;
}

async function appEv(
	x: XEnv,
	name: string,
	after: number,
	where: (e: AppEv) => boolean = () => true,
	timeoutMs = 30_000,
): Promise<AppEv> {
	const e = await waitFor(
		`App 事件 ${name}`,
		() =>
			appEvents(x)
				.slice(after)
				.find((v) => v.event === name && where(v)),
		timeoutMs,
		200,
	);
	const { ts: _ts, ...rest } = e;
	note(`App 事件 ${JSON.stringify(rest)}`);
	return e;
}

/** App 二进制：已有就用，没有就现建（`bun run tauri build --no-bundle`）。 */
async function appBinary(): Promise<string> {
	if (existsSync(APP_BIN)) return APP_BIN;
	note(`没有 ${APP_BIN}，构建 App（bun run tauri build --no-bundle）`);
	const r = await sh("cd web && bun run tauri build --no-bundle", {
		timeoutMs: 3_600_000,
	});
	if (r.code !== 0 || !existsSync(APP_BIN))
		throw new Error(`构建 App 失败：${r.out.slice(-800)}`);
	return APP_BIN;
}

/** 跑一次 App 的第二实例（深链接、--quit），等它退出。 */
async function appOnce(x: XEnv, args: string[]) {
	const p = Bun.spawn([x.bin ?? APP_BIN, ...args], {
		env: appEnv(x),
		stdin: "ignore",
		stdout: "ignore",
		stderr: "ignore",
	});
	if ((await Promise.race([p.exited, Bun.sleep(15_000)])) === undefined)
		p.kill("SIGKILL");
}

function appWindow(x: XEnv): Promise<string> {
	return waitFor(
		"App 窗口",
		async () =>
			(
				await xsh(x, "xdotool search --onlyvisible --name '^Oryn$'")
			).stdout.split("\n")[0],
		15_000,
		300,
	);
}

const activate = (x: XEnv, wid: string) =>
	xsh(
		x,
		`xdotool windowactivate --sync ${wid} 2>/dev/null || xdotool windowraise ${wid} windowfocus ${wid}`,
	);

/** 真实 App：在 Xvfb 上启动、配对到 dev 服务器（网页会话批准），进入远程页面。 */
async function ensureApp(): Promise<XEnv> {
	const x = await ensureX();
	if (x.app && x.app.exitCode === null && x.deviceId) return x;
	x.bin = await appBinary();
	const after = appEvents(x).length;
	if (x.deviceId) x.app = xbg(x, "app", [x.bin], appEnv(x));
	else {
		x.app = xbg(x, "app", [x.bin, "--server", x.origin], appEnv(x));
		const started = await appEv(x, "pair.started", after);
		const r = await req("POST", "/api/v1/devices/pair/approve", {
			body: { code: started.code },
			headers: {
				Cookie: `__Host-oryn_session=${SESSION}`,
				"X-Oryn-CSRF": "1",
			},
		});
		if (r.status >= 300)
			throw new Error(`批准配对失败 ${r.status} ${r.text.slice(0, 200)}`);
		const ok = await appEv(x, "pair.approved", after);
		x.deviceId = String(ok.deviceId);
	}
	await appEv(x, "web.ready", after, (e) => e.origin === x.origin);
	const w = await appWindow(x);
	await activate(x, w);
	await xsh(
		x,
		`xdotool windowsize --sync ${w} 1400 860 windowmove --sync ${w} 20 20`,
	);
	note(`App 已配对（设备 ${x.deviceId}），窗口 ${w} 1400x860`);
	return x;
}

/** 一个可被观察的页面：App（WebKit 检查器）或有界面 Chromium（裸 CDP）。操作只经 xdotool。 */
interface Surface {
	name: string;
	wid: string;
	/** 在页面里求值一个 JS 表达式（结果经 JSON 往返） */
	eval<T>(expr: string): Promise<T>;
	/** 视口原点的屏幕坐标 */
	origin(): Promise<{ x: number; y: number }>;
}

async function windowGeometry(x: XEnv, wid: string) {
	const g = (await xsh(x, `xdotool getwindowgeometry --shell ${wid}`)).stdout;
	const v = (k: string) =>
		Number(new RegExp(`^${k}=(-?\\d+)`, "m").exec(g)?.[1]);
	return { x: v("X"), y: v("Y"), w: v("WIDTH"), h: v("HEIGHT") };
}

interface WkMsg {
	id?: number;
	method?: string;
	params?: {
		targetInfo?: { targetId: string; type: string };
		message?: string;
	};
	error?: unknown;
	result?: { result?: { value?: string }; wasThrown?: boolean };
}

/** WebKit 远程检查器求值：每次新连一条，取最新的 page target，经 Target.sendMessageToTarget 发 Runtime.evaluate。 */
async function wkEval<T>(x: XEnv, expr: string): Promise<T> {
	const index = await (await fetch(`http://127.0.0.1:${x.inspector}/`)).text();
	const rows = [
		...index.matchAll(
			/targeturl">([^<]*)<[\s\S]*?window\.location\.host \+ '([^']+)'/g,
		),
	];
	const row = rows.find((m) => m[1]?.startsWith(x.origin)) ?? rows[0];
	if (!row) throw new Error("WebKit 检查器里没有页面");
	const ws = new WebSocket(`ws://127.0.0.1:${x.inspector}${row[2]}`);
	const message = JSON.stringify({
		id: 2,
		method: "Runtime.evaluate",
		params: { expression: `JSON.stringify((${expr}))`, returnByValue: true },
	});
	const { promise, resolve, reject } = Promise.withResolvers<T>();
	let target = "";
	const timer = setTimeout(
		() => reject(new Error(`检查器求值超时：${expr.slice(0, 80)}`)),
		10_000,
	);
	ws.onerror = () => reject(new Error("WebKit 检查器连接失败"));
	ws.onmessage = (e) => {
		const m = JSON.parse(String(e.data)) as WkMsg;
		const info = m.params?.targetInfo;
		if (m.method === "Target.targetCreated" && info?.type === "page") {
			// 连上时先报已有 target；稍等一下取最后一个（导航中可能还有临时页）
			if (!target)
				setTimeout(
					() =>
						ws.send(
							JSON.stringify({
								id: 1,
								method: "Target.sendMessageToTarget",
								params: { targetId: target, message },
							}),
						),
					150,
				);
			target = info.targetId;
		}
		if (m.method !== "Target.dispatchMessageFromTarget") return;
		const inner = JSON.parse(m.params?.message ?? "{}") as WkMsg;
		if (inner.id !== 2) return;
		if (inner.error || inner.result?.wasThrown)
			reject(
				new Error(
					`检查器求值出错：${JSON.stringify(inner.error ?? inner.result).slice(0, 300)}`,
				),
			);
		else resolve(JSON.parse(inner.result?.result?.value ?? "null") as T);
	};
	try {
		return await promise;
	} finally {
		clearTimeout(timer);
		ws.close();
	}
}

/**
 * App 的视口原点 = 窗口左上角：App 不要系统标题栏（D16），WebView 铺满窗口。
 * 不用 mousemove 的 screenX：实测 WebKitGTK 退出全屏后 screenX 仍按全屏时的窗口位置（0,0）报，偏了窗口的偏移量。
 */
async function appSurface(x: XEnv): Promise<Surface> {
	const wid = await appWindow(x);
	return {
		name: "App",
		wid,
		eval: (expr) => wkEval(x, expr),
		origin: () => windowGeometry(x, wid),
	};
}

const sel = (s: string) => `document.querySelector(${JSON.stringify(s)})`;

/** 元素中心的视口坐标（不可见为 null）。 */
const rectOf = (s: Surface, find: string) =>
	s.eval<{ x: number; y: number } | null>(
		`(() => { const el = ${find}; if (!el) return null; const r = el.getBoundingClientRect(); if (!r.width || !r.height) return null; return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`,
	);

/**
 * 有界面 Chromium 的视口原点：把指针挪进窗口，读 mousemove 的 screenX − clientX（浏览器工具栏、全屏都自动算进去）。
 */
async function viewportOrigin(x: XEnv, s: Surface) {
	await s.eval(
		`(window.__m8o = null, window.__m8oOn || (window.__m8oOn = true, addEventListener("mousemove", (e) => { window.__m8o = { x: e.screenX - e.clientX, y: e.screenY - e.clientY }; }, true)), true)`,
	);
	const g = await windowGeometry(x, s.wid);
	const cx = g.x + Math.round(g.w / 2);
	const cy = g.y + Math.round(g.h / 2);
	return waitFor(
		`${s.name} 视口原点`,
		async () => {
			await xsh(
				x,
				`xdotool mousemove --sync ${cx} ${cy} sleep 0.05 mousemove --sync ${cx + 3} ${cy + 2}`,
			);
			return s.eval<{ x: number; y: number } | null>("window.__m8o");
		},
		10_000,
		200,
	);
}

/** 在页面坐标 p 处真实点击（xdotool）。 */
async function xclickAt(x: XEnv, s: Surface, p: { x: number; y: number }) {
	const o = await s.origin();
	await xsh(
		x,
		`xdotool mousemove --sync ${Math.round(o.x + p.x)} ${Math.round(o.y + p.y)} sleep 0.15 click 1`,
	);
}

async function xclick(x: XEnv, s: Surface, find: string, what: string) {
	const p = await waitFor(
		`${s.name}：${what}`,
		() => rectOf(s, find),
		30_000,
		300,
	);
	await xclickAt(x, s, p);
}

/** 点舞台上 orb 页面里的某个元素（远端屏幕坐标 → canvas 上的页面坐标 → 屏幕坐标）。 */
async function xclickRemote(
	x: XEnv,
	s: Surface,
	orb: string,
	targetId: string,
	selector: string,
) {
	const rp = await remotePoint(orb, targetId, selector);
	const p = await s.eval<{ x: number; y: number } | null>(
		`(() => { const c = document.querySelector(${JSON.stringify(CANVAS)}); if (!c?.width) return null; const r = c.getBoundingClientRect(); return { x: r.left + (${rp.x} * r.width) / c.width, y: r.top + (${rp.y} * r.height) / c.height }; })()`,
	);
	if (!p) throw new Error(`${s.name}：舞台上没有 canvas`);
	await xclickAt(x, s, p);
}

const xtype = (x: XEnv, text: string) =>
	xsh(x, `xdotool type --delay 70 -- ${q(text)}`);

/** 用深链接（第二实例）让 App 打开线程页。 */
async function appOpenThread(x: XEnv, s: Surface, tid: string) {
	const after = appEvents(x).length;
	await appOnce(x, [`oryn://thread/${tid}`]);
	await appEv(x, "open", after, (e) => e.path === `/t/${tid}`);
	await waitFor(
		`App 进入 /t/${tid}`,
		async () => (await s.eval<string>("location.pathname")) === `/t/${tid}`,
		30_000,
		500,
	);
	await activate(x, s.wid);
}

/** 真实点击 Desktop 标签，等 canvas 出现且状态 connected。 */
async function xOpenDesktop(x: XEnv, s: Surface) {
	const tab = `[...document.querySelectorAll(${JSON.stringify(DESK_TAB)})].find((e) => e.getBoundingClientRect().width > 0)`;
	await waitFor(`${s.name}：Desktop 标签`, () => rectOf(s, tab), 60_000, 500);
	if (
		!(await s.eval<boolean>(`${tab}.getAttribute("aria-selected") === "true"`))
	)
		await xclick(x, s, tab, "Desktop 标签");
	await waitFor(
		`${s.name}：Desktop 面板已连接`,
		() =>
			s.eval<boolean>(
				`!!document.querySelector("[data-testid=desktop-status][data-state=connected]") && (document.querySelector(${JSON.stringify(CANVAS)})?.width ?? 0) > 0`,
			),
		90_000,
		500,
	);
	check(true, `${s.name}：点开 Desktop 标签，canvas 出现且状态 connected`);
}

/** orb 里的浏览器没在跑时，真实点击舞台上的「启动浏览器」。 */
async function xStartBrowser(x: XEnv, s: Surface, orb: string) {
	if ((await getOrb(orb)).desktop?.browser === "running") {
		note("orb 里浏览器已在运行");
		return;
	}
	await xclick(
		x,
		s,
		sel("[data-testid=desktop-start-browser]"),
		"「启动浏览器」",
	);
	await waitFor(
		"orb 里浏览器 running",
		async () => (await getOrb(orb)).desktop?.browser === "running",
		120_000,
		1000,
	);
	check(true, `${s.name}：点「启动浏览器」后 orb 里浏览器在运行`);
}

async function remoteEval<T>(
	orb: string,
	targetId: string,
	expression: string,
): Promise<T> {
	const [res] = await cdp(orb, targetId, [
		{ method: "Runtime.evaluate", params: { expression, returnByValue: true } },
	]);
	return (res as { result?: { value?: T } }).result?.value as T;
}

/** 在 orb 站点页上数 Escape 的 keydown（捕获阶段，首次调用时挂上），返回当前次数。 */
const remoteEsc = (orb: string, targetId: string) =>
	remoteEval<number>(
		orb,
		targetId,
		`(window.__m8escOn || (window.__m8escOn = true, window.__m8esc = 0, addEventListener("keydown", (e) => { if (e.key === "Escape") window.__m8esc += 1; }, true)), window.__m8esc)`,
	);

const remoteClipboard = async (orb: string) =>
	(await dexec(orb, "xclip -o -selection clipboard", { DISPLAY: ":1" })).stdout;

const FS_ID =
	"document.fullscreenElement ? (document.fullscreenElement.dataset.testid ?? '?') : null";

interface EscResult {
	shortExit: boolean;
	shortRemote: number;
	longExit: boolean;
	longRemote: number;
}

/** noVNC 的鼠标捕获代理层（setCapture 的模拟：盖满整页的 div，等 mouseup 才收起）是否还开着。 */
const STALE_CAPTURE = `(() => { const el = document.getElementById("noVNC_mouse_capture_elem"); return !!document.captureElement && !!el && getComputedStyle(el).display !== "none"; })()`;

/**
 * 真实点击全屏按钮进入全屏，再点舞台上的远端输入框（焦点进 canvas 与远端页面）。
 * 全屏中点过 canvas 后，noVNC 的捕获代理层（挂在 body 上，在全屏顶层之下收不到 mouseup）不会收起；
 * 退出全屏后它盖满整页，用户的下一次点击被它吞掉。遇到时记为产品问题（⚠）并截图，再点一次继续。
 */
async function xEnterFullscreen(
	x: XEnv,
	s: Surface,
	orb: string,
	siteId: string,
	tag: string,
) {
	// 记下面板 requestFullscreen 的结果（失败时面板会静默退到「面板最大化」）与点击落在了谁身上
	await s.eval(
		`(() => { const p = Element.prototype; if (!p.__m8rf) { p.__m8rf = p.requestFullscreen; p.requestFullscreen = function (...a) { window.__m8fs = "pending"; const r = p.__m8rf.apply(this, a); r.then(() => { window.__m8fs = "ok"; }, (e) => { window.__m8fs = "rejected: " + e; }); return r; }; } window.__m8fs = "not-called"; window.__m8ev = []; if (!window.__m8evOn) { window.__m8evOn = true; for (const t of ["pointerdown", "mousedown", "mouseup", "click"]) addEventListener(t, (e) => { const el = e.target; window.__m8ev.push(t + ":" + (el?.id || el?.dataset?.testid || el?.getAttribute?.("aria-label") || el?.tagName)); }, true); } return true; })()`,
	);
	const stale = await s.eval<boolean>(STALE_CAPTURE);
	const why = () =>
		s.eval<string>(
			`"requestFullscreen " + window.__m8fs + "，事件 " + window.__m8ev.join(" ") + "，点击后捕获层仍开着=" + ${STALE_CAPTURE}`,
		);
	const entered = () =>
		waitFor(
			`${s.name}：进入全屏`,
			() => s.eval<string | null>(FS_ID),
			5_000,
			100,
		).catch(() => null);
	await xclick(x, s, sel("[data-testid=desktop-fullscreen]"), "全屏按钮");
	let id = await entered();
	if (!id && stale) {
		const w = await why();
		soft(
			false,
			`产品问题：退出全屏后第一次点击被 noVNC 的捕获代理层吞掉（全屏中点过 canvas，mouseup 没到代理层，代理层一直盖着整页）；点全屏按钮无反应（${w}）`,
		);
		await xshot(`${tag}-click-swallowed`);
		await xclick(x, s, sel("[data-testid=desktop-fullscreen]"), "全屏按钮");
		id = await entered();
		if (id) note(`${s.name}：第二次点击全屏按钮才进入全屏`);
	}
	if (!id) throw new Error(`${s.name}：点全屏按钮没进入全屏（${await why()}）`);
	check(id === "desktop-stage", `${s.name}：fullscreenElement = ${id}`);
	await Bun.sleep(1000);
	const wm = (await xsh(x, `xprop -id ${s.wid} _NET_WM_STATE`)).stdout;
	check(
		wm.includes("_NET_WM_STATE_FULLSCREEN"),
		`${s.name}：窗口进入真全屏（${wm}）`,
	);
	await xclickRemote(x, s, orb, siteId, "#i");
	await Bun.sleep(300);
}

/**
 * 全屏下真实按 Esc：先短按（xdotool key），再长按约 2.5 秒（keydown → sleep → keyup）。
 * 每次记录是否退出全屏、远端页面收到几次 Escape keydown。长按后仍没退出时用 exitFullscreen 收尾（不让后续步骤困在全屏里）。
 */
async function escTrial(
	x: XEnv,
	s: Surface,
	orb: string,
	siteId: string,
	tag: string,
): Promise<EscResult> {
	const exited = (ms: number) =>
		waitFor(
			"退出全屏",
			async () => (await s.eval<string | null>(FS_ID)) === null,
			ms,
			100,
		).then(
			() => true,
			() => false,
		);
	await xEnterFullscreen(x, s, orb, siteId, tag);
	await xshot(`${tag}-fullscreen`);
	let n0 = await remoteEsc(orb, siteId);
	await xsh(x, "xdotool key Escape");
	const shortExit = await exited(2000);
	await Bun.sleep(500);
	const shortRemote = (await remoteEsc(orb, siteId)) - n0;
	note(
		`${s.name}：短按 Esc → ${shortExit ? "退出了全屏" : "仍在全屏"}，远端收到 Escape ${shortRemote} 次`,
	);
	await xshot(`${tag}-after-short-esc`);
	if (shortExit) await xEnterFullscreen(x, s, orb, siteId, tag);
	n0 = await remoteEsc(orb, siteId);
	await xsh(x, "xdotool keydown Escape sleep 2.5 keyup Escape");
	const longExit = await exited(3000);
	await Bun.sleep(500);
	const longRemote = (await remoteEsc(orb, siteId)) - n0;
	note(
		`${s.name}：长按 Esc 2.5 秒 → ${longExit ? "退出了全屏" : "仍在全屏"}，远端收到 Escape ${longRemote} 次`,
	);
	await xshot(`${tag}-after-long-esc`);
	if (!longExit) {
		await s.eval("(document.exitFullscreen(), true)").catch(() => {});
		await exited(3000);
	}
	// 退出全屏后 noVNC 的捕获代理层可能还盖着整页（见 xEnterFullscreen）：真实点一下舞台让它收起，后续步骤的点击不再被吞
	if (await s.eval<boolean>(STALE_CAPTURE)) {
		note(
			`${s.name}：退出全屏后 noVNC 捕获代理层仍盖着整页，点一下舞台让它收起`,
		);
		await xclick(x, s, sel(STAGE), "舞台（收起捕获层）");
		await waitFor(
			"noVNC 捕获代理层收起",
			async () => !(await s.eval<boolean>(STALE_CAPTURE)),
			5_000,
			200,
		);
	}
	return { shortExit, shortRemote, longExit, longRemote };
}

/** 收尾：退出 App（--quit）、吊销它的设备、结束 X 环境的进程、撤销临时信任的 CA、保存日志。 */
async function teardownX() {
	const x = X;
	if (!x) return;
	X = null;
	const stopped = async (p: Subprocess, ms: number) =>
		(await Promise.race([p.exited, Bun.sleep(ms)])) !== undefined;
	if (x.app && x.app.exitCode === null) {
		await appOnce(x, ["--quit"]);
		if (!(await stopped(x.app, 15_000))) x.app.kill("SIGKILL");
	}
	if (x.deviceId) {
		const r = await api("DELETE", `/api/v1/devices/${x.deviceId}`);
		console.log(`吊销 App 设备 ${x.deviceId} → ${r.status}`);
	}
	for (const p of [...x.procs].reverse()) {
		if (p.exitCode !== null || p.signalCode !== null) continue;
		p.kill("SIGTERM");
		if (!(await stopped(p, 3000))) p.kill("SIGKILL");
	}
	if (x.caAdded) {
		const r = await sh(`trust anchor --remove ${q(`${TLS_DIR}/ca.crt`)}`);
		console.log(
			`撤销本机 CA 信任（trust anchor --remove）→ ${r.code === 0 ? "完成" : `失败：${r.out}`}`,
		);
	}
	const keep = `${SHOTS}/app-${RUN}`;
	mkdirSync(keep, { recursive: true });
	for (const f of ["events.jsonl", "app.log", "tlsproxy.log", "xdg-open.log"])
		if (existsSync(`${x.tmp}/${f}`))
			copyFileSync(`${x.tmp}/${f}`, `${keep}/${f}`);
	console.log(`App 事件日志与输出：${keep}`);
	if (!KEEP) rmSync(x.tmp, { recursive: true, force: true });
}

for (const sig of ["SIGINT", "SIGTERM"] as const)
	process.on(sig, () => {
		void teardownX().finally(() => process.exit(130));
	});

async function B17() {
	await b17Headless();
	if (!REAL) {
		note(`真实按键分支未开启（${REAL_HINT}）`);
		return;
	}
	const fails: string[] = [];
	for (const [name, fn] of [
		["桌面 App（WebKitGTK）", b17App],
		["有界面 Chromium", b17Headful],
	] as const) {
		note(`—— 真跑分支：${name}`);
		try {
			await fn();
		} catch (e) {
			const m = e instanceof Error ? e.message : String(e);
			note(`✗ ${name}：${m}`);
			await xshot(`B17-${fn.name}-FAIL`);
			fails.push(`${name}：${m}`);
		}
	}
	if (fails.length) throw new Error(fails.join("；"));
}

/** 包一层 navigator.keyboard.lock / unlock，记下调用与 lock 的结果；没有该 API 时返回 false。 */
function hookKeyboardLock(): boolean {
	type K = { lock?(): Promise<void>; unlock?(): void };
	const kb = (navigator as Navigator & { keyboard?: K }).keyboard;
	const w = window as unknown as { __m8Kb: string[] };
	w.__m8Kb = [];
	if (!kb?.lock || !kb.unlock) return false;
	const lock = kb.lock.bind(kb);
	const unlock = kb.unlock.bind(kb);
	kb.lock = () => {
		w.__m8Kb.push("lock");
		return lock().then(
			() => {
				w.__m8Kb.push("lock:ok");
			},
			(e: unknown) => {
				w.__m8Kb.push(`lock:err:${String(e)}`);
				throw e;
			},
		);
	};
	kb.unlock = () => {
		w.__m8Kb.push("unlock");
		unlock();
	};
	return true;
}

/**
 * B17 headless 兼容路径：仅验证退出全屏后的清理。
 * Playwright 经 CDP 合成的按键直接送进渲染进程，不经过浏览器层的全屏处理（实测 headless Chromium 短按 / 长按 Esc
 * 都不会退出全屏），所以这里不判断 Esc 本身，直接 document.exitFullscreen()，只看进入全屏取键盘锁、退出后释放键盘锁、
 * 面板回到正常布局。Esc 的真实表现见真跑分支（b17App / b17Headful）。
 */
async function b17Headless() {
	note("headless 兼容路径：仅验证退出全屏后的清理（不判断 Esc）");
	const { page } = await deskPageA();
	const fsId = () =>
		page.evaluate(() =>
			document.fullscreenElement
				? ((document.fullscreenElement as HTMLElement).dataset.testid ?? "?")
				: null,
		);
	// 记下面板对键盘锁的调用：进入全屏应 lock（让 Esc 进桌面），退出全屏应 unlock
	const hooked = await page.evaluate(hookKeyboardLock);
	const kbCalls = () =>
		page.evaluate(
			() => (window as unknown as { __m8Kb: string[] }).__m8Kb ?? [],
		);
	await page.locator("[data-testid=desktop-fullscreen]").click();
	const id = await waitFor("进入全屏", fsId, 5_000, 100);
	check(id === "desktop-stage", `fullscreenElement = ${id}`);
	if (hooked) {
		await waitFor(
			"全屏后取得键盘锁",
			async () => (await kbCalls()).includes("lock"),
			3_000,
			100,
		);
		check(true, "进入全屏后调用了 navigator.keyboard.lock");
	} else note("浏览器没有 navigator.keyboard.lock，跳过键盘锁观察");
	await page.evaluate(() => document.exitFullscreen());
	await waitFor(
		"exitFullscreen 退出全屏",
		async () => (await fsId()) === null,
		3_000,
		100,
	);
	check(true, "exitFullscreen 后 fullscreenElement = null");
	if (hooked) {
		await waitFor(
			"退出全屏后释放键盘锁",
			async () => (await kbCalls()).includes("unlock"),
			3_000,
			100,
		);
		check(true, "退出全屏后调用了 navigator.keyboard.unlock");
	}
	await page
		.locator("[data-testid=desktop-fullscreen]")
		.waitFor({ state: "visible", timeout: 5_000 });
	check(true, "退出后全屏按钮可见，面板回到正常布局");
}

/** 线程 T 的 orb 里：浏览器在跑、站点页打开并开始数 Escape。 */
async function escSite(x: XEnv, s: Surface, th: Th) {
	await xStartBrowser(x, s, th.orb);
	const siteId = await openSitePage(th.orb);
	await remoteEsc(th.orb, siteId);
	await Bun.sleep(1500);
	return siteId;
}

/** B17 真跑①：桌面 App（WebKitGTK）里真实点全屏、真实按 Esc。 */
async function b17App() {
	const th = await ensureT();
	const x = await ensureApp();
	const s = await appSurface(x);
	await appOpenThread(x, s, th.tid);
	await xOpenDesktop(x, s);
	const siteId = await escSite(x, s, th);
	const ua = await s.eval<string>("navigator.userAgent");
	const kb = await s.eval<string>(
		`typeof navigator.keyboard === "undefined" ? "undefined" : typeof navigator.keyboard.lock`,
	);
	note(
		`WebKitGTK navigator.keyboard${kb === "undefined" ? " 不存在（不支持 Keyboard Lock）" : `.lock 的类型 ${kb}`}；UA ${ua}`,
	);
	const r = await escTrial(x, s, th.orb, siteId, "B17-app");
	check(r.shortExit || r.longExit, "App 里能用 Esc 退出全屏");
	if (kb !== "function")
		check(
			r.shortExit,
			"没有键盘锁：短按 Esc 即退出全屏（Esc 由 WebKit 处理，用户不会被困）",
		);
	check(
		!!(await rectOf(s, sel("[data-testid=desktop-fullscreen]"))),
		"退出后全屏按钮可见，面板回到正常布局",
	);
}

/** 宿主上一条裸 CDP 连接（只发 Runtime.evaluate / Storage.setCookies，不像 Playwright 那样接管页面）。 */
async function rawCdp(wsUrl: string) {
	const ws = new WebSocket(wsUrl);
	const opened = Promise.withResolvers<void>();
	ws.onopen = () => opened.resolve();
	ws.onerror = () => opened.reject(new Error(`CDP 连接失败：${wsUrl}`));
	await opened.promise;
	type Reply = { id?: number; result?: unknown; error?: { message?: string } };
	const pending = new Map<number, (m: Reply) => void>();
	ws.onmessage = (e) => {
		const m = JSON.parse(String(e.data)) as Reply;
		if (m.id === undefined) return;
		pending.get(m.id)?.(m);
		pending.delete(m.id);
	};
	let seq = 0;
	const call = async (method: string, params: Record<string, unknown> = {}) => {
		seq += 1;
		const { promise, resolve, reject } = Promise.withResolvers<Reply>();
		pending.set(seq, resolve);
		const timer = setTimeout(
			() => reject(new Error(`CDP ${method} 超时`)),
			15_000,
		);
		ws.send(JSON.stringify({ id: seq, method, params }));
		const m = await promise.finally(() => clearTimeout(timer));
		if (m.error) throw new Error(`CDP ${method}：${m.error.message}`);
		return m.result;
	};
	return { call, close: () => ws.close() };
}

/**
 * B17 真跑②：同一 Xvfb 上的有界面 Chromium（非 headless），键盘锁下短按 / 长按 Esc。
 * 浏览器以普通进程启动（独立用户目录 + --remote-debugging-port），观察只经一条裸 CDP 连接做 Runtime.evaluate。
 * 不用 Playwright 接管：实测 Playwright 启动或 connectOverCDP 接管的 Chromium 156 把元素全屏做成「标签页内全屏」
 * （fullscreenElement 有值，但窗口没有 _NET_WM_STATE_FULLSCREEN，浏览器工具栏仍在），与用户看到的真实全屏不同。
 */
async function b17Headful() {
	const th = await ensureT();
	const x = await ensureX();
	const port = freePort();
	const proc = xbg(x, "chromium", [
		process.env.CHROMIUM ?? "/usr/bin/chromium",
		"--no-sandbox",
		`--user-data-dir=${x.tmp}/chromium`,
		"--no-first-run",
		"--no-default-browser-check",
		"--lang=zh-CN",
		`--remote-debugging-port=${port}`,
		"--window-position=0,0",
		"--window-size=1400,880",
		"about:blank",
	]);
	const dt = `http://127.0.0.1:${port}`;
	const version = await waitFor(
		"有界面 Chromium 的调试端口",
		async () =>
			(await (await fetch(`${dt}/json/version`)).json()) as {
				Browser: string;
				webSocketDebuggerUrl: string;
			},
		20_000,
		300,
	);
	note(`有界面 Chromium ${version.Browser}（普通进程，非 headless）`);
	const browserCdp = await rawCdp(version.webSocketDebuggerUrl);
	const pageList = (await (await fetch(`${dt}/json/list`)).json()) as {
		type: string;
		webSocketDebuggerUrl: string;
	}[];
	const target = pageList.find((t) => t.type === "page");
	if (!target) throw new Error("Chromium 没有页面 target");
	const pageCdp = await rawCdp(target.webSocketDebuggerUrl);
	try {
		// 网页会话 Cookie（与上下文 B 相同）；url 形式即 host-only，满足 __Host- 前缀
		await browserCdp.call("Storage.setCookies", {
			cookies: [
				{
					name: "__Host-oryn_session",
					value: SESSION,
					url: `${BASE}/`,
					path: "/",
					secure: true,
					httpOnly: true,
					sameSite: "Lax",
				},
			],
		});
		const evalPage = async <T>(expr: string): Promise<T> => {
			const r = (await pageCdp.call("Runtime.evaluate", {
				expression: `JSON.stringify((${expr}))`,
				returnByValue: true,
				awaitPromise: true,
			})) as {
				result?: { value?: string };
				exceptionDetails?: { text?: string };
			};
			if (r.exceptionDetails)
				throw new Error(`页面求值出错：${JSON.stringify(r.exceptionDetails)}`);
			return JSON.parse(r.result?.value ?? "null") as T;
		};
		const goto = async (url: string) => {
			// 导航会销毁当前执行上下文，这次求值可能拿不到结果
			await evalPage(`(location.href = ${JSON.stringify(url)}, true)`).catch(
				() => {},
			);
			await waitFor(
				`Chromium 打开 ${url}`,
				async () =>
					(await evalPage<string>(
						`document.readyState === "complete" ? location.href : ""`,
					)) === url,
				30_000,
				300,
			);
		};
		await goto(`${BASE}/`);
		await evalPage(
			`(localStorage.setItem("oryn.panel.width", "760"), localStorage.removeItem("oryn.desktop.scale"), localStorage.removeItem("oryn.desktop.control"), true)`,
		);
		await goto(`${BASE}/t/${th.tid}`);
		const wid = await waitFor(
			"Chromium 窗口",
			async () =>
				(await xsh(x, "xdotool search --onlyvisible --class chromium")).stdout
					.split("\n")
					.filter(Boolean)
					.pop(),
			15_000,
			300,
		);
		const s: Surface = {
			name: "Chromium",
			wid,
			eval: evalPage,
			origin: () => viewportOrigin(x, s),
		};
		await activate(x, wid);
		await xOpenDesktop(x, s);
		const siteId = await escSite(x, s, th);
		check(
			await evalPage<boolean>(`(${hookKeyboardLock.toString()})()`),
			"Chromium 有 navigator.keyboard.lock",
		);
		const r = await escTrial(x, s, th.orb, siteId, "B17-chromium");
		const calls = await evalPage<string[]>("window.__m8Kb");
		note(`键盘锁调用：${calls.join(" → ")}`);
		check(
			calls.includes("lock:ok"),
			"进入全屏后 navigator.keyboard.lock() 成功",
		);
		check(!r.shortExit, "键盘锁生效：短按 Esc 不退出全屏");
		check(
			r.shortRemote > 0,
			`短按的 Esc 传进了远端页面（${r.shortRemote} 次）`,
		);
		check(
			r.longExit,
			r.longExit
				? "长按 Esc 2.5 秒退出全屏（fullscreenElement = null）"
				: "产品问题：键盘锁下长按 Esc 2.5 秒仍无法退出全屏，用户会被困在全屏里",
		);
		check(
			calls.includes("unlock"),
			"退出全屏后调用了 navigator.keyboard.unlock",
		);
	} finally {
		pageCdp.close();
		browserCdp.close();
		proc.kill("SIGTERM");
		await proc.exited;
	}
}

async function B18() {
	const a3 = await ensureA3();
	const r4 = `r${RUN}d`;
	sids.push(r4);
	await browserLogin(a3.orb, r4, "B18");
	const rev0 = (await profileGet(P)).revision;
	await letSleep(a3);
	await pauseOrb(a3.orb, "stop");
	const p = await waitFor(
		"停机后 P 修订前进且无 holder",
		async () => {
			const x = await profileGet(P);
			return x.revision > rev0 && !x.holder ? x : undefined;
		},
		60_000,
		1000,
	);
	check(true, `停机后 P revision ${rev0} → ${p.revision}，无 holder`);
	awake.add(a3.tid);
	const rs = await api("POST", `/api/v1/orbs/${a3.orb}/resume`);
	note(`resume → ${rs.status}`);
	await ensureReady(a3.tid);
	const w = await whoami(a3.orb, "B18");
	check(cookieVal(w.cookie, "sid") === r4, `恢复后请求带 Cookie sid=${r4}`);
	await waitFor(
		"P 的 holder 是该 orb",
		async () => (await profileGet(P)).holder?.orbId === a3.orb,
		30_000,
		1000,
	);
	check(true, "P 的 holder 是该 orb");
}

async function B19() {
	const n1 = await newThread("N1", { browserProfileId: "" });
	const r5 = `r${RUN}e`;
	await browserLogin(n1.orb, r5, "N1");
	const before = await listProfileFiles();
	note(`browser-profiles 文件 ${before.split("\n").filter(Boolean).length} 个`);
	awake.delete(n1.tid);
	await deleteOrb(n1.orb, true);
	const n2 = await newThread("N2", { browserProfileId: "" });
	try {
		const w = await whoami(n2.orb, "N2");
		check(
			!cookieVal(w.cookie, "sid") && w.ls !== r5,
			"第二个线程的请求没有 sid Cookie、没有 localStorage",
		);
		const after = await listProfileFiles();
		check(after === before, "删除前后 browser-profiles 的文件列表相同");
	} finally {
		awake.delete(n2.tid);
		await deleteOrb(n2.orb, false);
	}
}

/**
 * B20：真实 Tauri App（WebKitGTK）里走一遍 Desktop 面板：xdotool 点 Desktop 标签 → canvas 连接 → 点「启动浏览器」→
 * 在 orb 里经 CDP 打开站点页 → 点舞台上的输入框并键入（CDP 读回值）→ 剪贴板弹层「发送到桌面」（orb 里 xclip 读回）→
 * 远端输入框 Ctrl+A / Ctrl+C →「复制到本机」（Xvfb 上 xclip 读回）。每步截屏。
 */
async function B20() {
	if (!REAL) {
		note(REAL_HINT);
		throw new Skip("未开启真跑（M8_B20_APP=1）");
	}
	const th = await newThread("B20", { browserProfileId: "" });
	await ensureSite(th);
	const x = await ensureApp();
	const s = await appSurface(x);
	await appOpenThread(x, s, th.tid);
	await xOpenDesktop(x, s);
	await xshot("B20-1-connected");
	await xStartBrowser(x, s, th.orb);
	const siteId = await openSitePage(th.orb);
	await Bun.sleep(1500);
	await xshot("B20-2-browser");

	// 键入：点舞台上的远端输入框，xdotool 逐键输入，经 noVNC → Xtigervnc → orb 里的 Chromium
	const typed = `b20${RUN}`;
	await xclickRemote(x, s, th.orb, siteId, "#i");
	await xtype(x, typed);
	await waitFor(
		`远端标题 typed:${typed}`,
		async () => (await targetTitle(th.orb, siteId)) === `typed:${typed}`,
		20_000,
		500,
	);
	const value = await remoteEval<string>(
		th.orb,
		siteId,
		`document.getElementById("i").value`,
	);
	check(value === typed, `App 里点击并键入后，远端输入框的值 = ${value}`);
	await xshot("B20-3-typed");

	// 本机 → 桌面：弹层里填字，点「发送到桌面」
	const trigger = sel(
		"[data-testid=desktop-panel] [role=toolbar] button[aria-haspopup=dialog]",
	);
	const openClip = async () => {
		if (!(await rectOf(s, sel("[data-testid=desktop-clipboard]"))))
			await xclick(x, s, trigger, "剪贴板按钮");
		await waitFor(
			"剪贴板弹层",
			() => rectOf(s, sel("[data-testid=desktop-clipboard]")),
			10_000,
			200,
		);
	};
	const toDesk = `m8-b20-to-desktop-${RUN}`;
	await openClip();
	await xclick(
		x,
		s,
		sel("[data-testid=desktop-clipboard] textarea"),
		"弹层文本框",
	);
	await xtype(x, toDesk);
	await waitFor(
		"文本框里是键入的文字",
		async () =>
			(await s.eval<string>(
				`document.querySelector("[data-testid=desktop-clipboard] textarea").value`,
			)) === toDesk,
		10_000,
		200,
	);
	await xshot("B20-4-clip-draft");
	await xclick(
		x,
		s,
		sel("[data-testid=desktop-clipboard] section:nth-of-type(2) button"),
		"「发送到桌面」",
	);
	const inOrb = await waitFor(
		"orb 的 X 剪贴板 = 发送的文字",
		async () => {
			const v = await remoteClipboard(th.orb);
			return v === toDesk ? v : undefined;
		},
		15_000,
		500,
	);
	check(
		inOrb === toDesk,
		`点「发送到桌面」后 docker exec -e DISPLAY=:1 ${th.orb} xclip -o -selection clipboard = ${inOrb}`,
	);
	await xshot("B20-5-sent");

	// 桌面 → 本机：先收起弹层（它盖住舞台上方），在远端输入框里换上新文字并 Ctrl+A / Ctrl+C
	const fromDesk = `m8-b20-from-desktop-${RUN}`;
	await xclick(x, s, trigger, "剪贴板按钮（收起弹层）");
	await waitFor(
		"剪贴板弹层收起",
		async () => !(await rectOf(s, sel("[data-testid=desktop-clipboard]"))),
		10_000,
		200,
	);
	await xclickRemote(x, s, th.orb, siteId, "#i");
	await xsh(x, "xdotool key --delay 120 ctrl+a");
	await xtype(x, fromDesk);
	await waitFor(
		`远端标题 typed:${fromDesk}`,
		async () => (await targetTitle(th.orb, siteId)) === `typed:${fromDesk}`,
		20_000,
		500,
	);
	await xsh(x, "xdotool key --delay 120 ctrl+a ctrl+c");
	await waitFor(
		"桌面里复制后 orb 的 X 剪贴板 = 新文字",
		async () => (await remoteClipboard(th.orb)) === fromDesk,
		15_000,
		500,
	);
	check(true, `在桌面里 Ctrl+C 后 orb 的 X 剪贴板 = ${fromDesk}`);
	await openClip();
	const shown = await waitFor(
		"弹层「来自桌面」显示新文字",
		async () => {
			const t = await s.eval<string | null>(
				`document.querySelector("[data-testid=desktop-clip-remote]")?.textContent ?? null`,
			);
			return t === fromDesk ? t : undefined;
		},
		15_000,
		500,
	);
	check(shown === fromDesk, `弹层「来自桌面」= ${shown}`);
	await xshot("B20-6-remote-clip");
	await xclick(x, s, sel("[data-testid=desktop-clip-copy]"), "「复制到本机」");
	const host = await waitFor(
		"宿主 X 剪贴板 = 桌面复制的文字",
		async () => {
			const v = (await xsh(x, "timeout 5 xclip -o -selection clipboard"))
				.stdout;
			return v === fromDesk ? v : undefined;
		},
		15_000,
		500,
	);
	check(
		host === fromDesk,
		`点「复制到本机」后 Xvfb ${x.display} 上 xclip -o -selection clipboard = ${host}`,
	);
	await xshot("B20-7-copied");
}

async function B21() {
	const qid = await createProfile(`m8q-${RUN}`);
	const r1 = await newThread("R1", { browserProfileId: qid });
	const s1 = `q${RUN}a`;
	await browserLogin(r1.orb, s1, "R1");
	awake.delete(r1.tid);
	await deleteOrb(r1.orb, true);
	await waitFor(
		"Q 已保存且无持有者",
		async () => {
			const x = await profileGet(qid);
			return x.revision >= 1 && !x.holder;
		},
		60_000,
		1000,
	);
	const r2 = await newThread("R2", {
		browserProfileId: qid,
		profileMode: "readonly",
	});
	try {
		const o = await getOrb(r2.orb);
		const dp = o.desktop?.profile;
		note(`R2 desktop.profile ${JSON.stringify(dp)}`);
		check(
			dp?.status === "readonly" && dp.mode === "readonly",
			"R2 以只读副本使用 Q",
		);
		const q0 = await profileGet(qid);
		note(
			`Q holder ${JSON.stringify(q0.holder ?? null)} readers ${JSON.stringify(q0.readers ?? [])}`,
		);
		check(!q0.holder, "只读不取租约（Q 无 holder）");
		const w = await whoami(r2.orb, "R2");
		check(
			cookieVal(w.cookie, "sid") === s1,
			`只读副本装载了最近保存的版本（sid=${s1}）`,
		);
		const s2 = `q${RUN}b`;
		const sig0 = await cookieSig(r2.orb, "sid");
		await browserLogin(r2.orb, s2, "R2");
		await waitCookieOnDisk(r2.orb, "sid", sig0);
		const rev0 = q0.revision;
		const t0 = Date.now();
		const sv = await api("POST", `/api/v1/orbs/${r2.orb}/desktop/profile/save`);
		check(
			sv.status === 409 && codeOf(sv) === "profile_readonly",
			`立即保存 → ${sv.status} ${codeOf(sv)}`,
		);
		await letSleep(r2);
		await pauseOrb(r2.orb, "freeze");
		const q1 = await profileGet(qid);
		const ev1 = profileEvents(qid, t0).filter((e) => e.orb_id === r2.orb);
		note(
			`冻结后 Q revision ${q1.revision}，R2 的事件 ${ev1.map((e) => e.event).join(",") || "无"}`,
		);
		check(q1.revision === rev0, `冻结后版本号不变（${rev0}）`);
		check(
			!ev1.some((e) => e.event === "profile.saved"),
			"冻结没有产生 R2 的 profile.saved",
		);
		// 恢复期间保活，避免就绪后被空闲评估抢先冻结；停机前再移出
		awake.add(r2.tid);
		await api("POST", `/api/v1/orbs/${r2.orb}/resume`);
		await ensureReady(r2.tid);
		await letSleep(r2);
		await pauseOrb(r2.orb, "stop");
		const q2 = await profileGet(qid);
		const ev2 = profileEvents(qid, t0).filter((e) => e.orb_id === r2.orb);
		note(
			`停机后 Q revision ${q2.revision}，R2 的事件 ${ev2.map((e) => e.event).join(",") || "无"}`,
		);
		check(q2.revision === rev0, `停机后版本号不变（${rev0}）`);
		check(
			!ev2.some((e) => e.event === "profile.saved"),
			"停机没有产生 R2 的 profile.saved",
		);
	} finally {
		awake.delete(r2.tid);
		await deleteOrb(r2.orb, false);
	}
}

interface Pair {
	q: string;
	x1: Th;
	x2: Th;
	rk: string;
	rev0: number;
	tTake: number;
}
let pair: Pair | null = null;

/** 接管对：X1 独占持有 Q2，在 X1 的桌面浏览器里经 CDP 设 Cookie（不手动保存），然后 X2 以 takeover 建线程。 */
async function takeoverPair(): Promise<Pair> {
	const qid = await createProfile(`m8k-${RUN}`);
	const x1 = await newThread("X1", { browserProfileId: qid });
	const st = await api("POST", `/api/v1/orbs/${x1.orb}/desktop/browser/start`);
	check(st.status === 200, `X1 启动浏览器 → ${st.status}`);
	const rk = `k${RUN}`;
	const [set] = await cdp(x1.orb, "browser", [
		{
			method: "Storage.setCookies",
			params: {
				cookies: [
					{
						name: "tk",
						value: rk,
						url: siteUrl("/"),
						expires: Math.floor(Date.now() / 1000) + 86_400,
					},
				],
			},
		},
	]);
	check(!(set as { error?: unknown }).error, `X1 经 CDP 设 Cookie tk=${rk}`);
	await waitCookieOnDisk(x1.orb, "tk", "");
	const rev0 = (await profileGet(qid)).revision;
	const tTake = Date.now();
	const x2 = await newThread("X2", {
		browserProfileId: qid,
		profileMode: "takeover",
	});
	pair = { q: qid, x1, x2, rk, rev0, tTake };
	return pair;
}

async function B22() {
	const pr = await takeoverPair();
	const [got] = await cdp(pr.x2.orb, "browser", [
		{ method: "Storage.getCookies" },
	]);
	const cookies =
		(got as { cookies?: { name: string; value: string }[] }).cookies ?? [];
	const tk = cookies.find((c) => c.name === "tk")?.value;
	check(tk === pr.rk, `接管方浏览器读到 Cookie tk=${tk ?? "（无）"}`);
	const w = await whoami(pr.x2.orb, "X2");
	check(
		cookieVal(w.cookie, "tk") === pr.rk,
		`接管方的请求带 Cookie tk=${pr.rk}`,
	);
	const p = await profileGet(pr.q);
	check(
		p.revision > pr.rev0,
		`Q2 revision ${pr.rev0} → ${p.revision}（接管前保存了原 orb）`,
	);
	check(p.holder?.orbId === pr.x2.orb, "Q2 的 holder 是 X2 的 orb");
	check(
		p.saveError !== "takeover_unsaved",
		`没有 takeover_unsaved（saveError=${p.saveError ?? "无"}）`,
	);
	const ev = profileEvents(pr.q, pr.tTake);
	note(`事件 ${ev.map((e) => `${e.event}@${e.orb_id}`).join(", ")}`);
	check(
		ev.some((e) => e.event === "profile.saved" && e.orb_id === pr.x1.orb),
		"接管时有原 orb 的 profile.saved",
	);
	check(
		ev.some((e) => e.event === "profile.taken_over"),
		"有 profile.taken_over",
	);
}

async function B23() {
	const pr =
		pair && orbState(pair.x1.orb) === "ready" ? pair : await takeoverPair();
	try {
		const o1 = await getOrb(pr.x1.orb);
		const dp = o1.desktop?.profile;
		note(`X1 desktop ${JSON.stringify(o1.desktop)}`);
		check(
			dp?.status === "readonly" && dp.readonlyReason === "taken_over",
			"X1：status=readonly、readonlyReason=taken_over",
		);
		check(
			!dp?.takenOverBy?.threadId || dp.takenOverBy.threadId === pr.x2.tid,
			`takenOverBy 指向 X2（${dp?.takenOverBy?.threadId ?? "无 threadId"}「${dp?.takenOverBy?.threadTitle ?? ""}」）`,
		);
		check(o1.desktop?.browser === "running", "X1 的浏览器照常运行");
		awake.add(pr.x1.tid);
		await withPage(ctxA, "A23", async (page) => {
			await page.goto(`${BASE}/t/${pr.x1.tid}`);
			await openDesktopTab(page);
			const panel = page.locator("[data-testid=desktop-panel]");
			const text = await waitFor(
				"X1 面板显示「只读 Profile · 已被…接管」",
				async () => {
					const s = (await panel.textContent()) ?? "";
					return /只读 Profile · 已被「.+」接管/.exec(s)?.[0];
				},
				30_000,
				500,
			);
			check(true, `X1 面板：${text}`);
			await shot(page, `B23-X1-${RUN}`);
		});
		const rev = (await profileGet(pr.q)).revision;
		const t0 = Date.now();
		await letSleep(pr.x1);
		await pauseOrb(pr.x1.orb, "freeze");
		const p = await profileGet(pr.q);
		const ev = profileEvents(pr.q, t0).filter((e) => e.orb_id === pr.x1.orb);
		note(
			`X1 冻结后 Q2 revision ${p.revision}，X1 的事件 ${ev.map((e) => e.event).join(",") || "无"}`,
		);
		check(p.revision === rev, `X1 冻结后版本号不变（${rev}）`);
		check(
			!ev.some((e) => e.event === "profile.saved"),
			"X1 冻结没有产生 profile.saved",
		);
	} finally {
		awake.delete(pr.x1.tid);
		awake.delete(pr.x2.tid);
		await deleteOrb(pr.x1.orb, false);
		await deleteOrb(pr.x2.orb, false);
		pair = null;
	}
}

async function B24() {
	const name = `m8w-${RUN}`;
	const wid = await createProfile(name);
	const h = await newThread("H", { browserProfileId: wid });
	let made: Th | null = null;
	try {
		await withPage(ctxA, "A24", async (page) => {
			await page.goto(`${BASE}/`);
			const ta = page.getByRole("textbox", { name: "提示词" });
			await ta.waitFor({ timeout: 30_000 });
			await ta.fill("只回复 ok");
			const pill = page.locator('button[aria-label^="浏览器 Profile："]');
			await pill.waitFor({ timeout: 30_000 });
			await pill.click();
			await page.locator("button", { hasText: name }).first().click();
			const label = (await pill.getAttribute("aria-label")) ?? "";
			check(label.includes(name), `Profile 胶囊：${label}`);
			const n0 =
				sql<{ n: number }>("select count(*) n from threads")[0]?.n ?? 0;
			await page.getByRole("button", { name: "发送", exact: true }).click();
			const dlg = page.locator("[data-testid=profile-conflict]");
			await dlg.waitFor({ timeout: 20_000 });
			const text = (await dlg.textContent()) ?? "";
			note(`对话框：${text.replaceAll("\n", " / ").slice(0, 300)}`);
			check(
				(await page
					.locator("[data-testid=profile-conflict-readonly]")
					.isVisible()) &&
					(await page
						.locator("[data-testid=profile-conflict-takeover]")
						.isVisible()),
				"弹出「只读使用 / 接管」选择对话框",
			);
			check(
				text.includes("只读使用") && text.includes("接管"),
				"两个选项都有说明文案",
			);
			check(
				(sql<{ n: number }>("select count(*) n from threads")[0]?.n ?? 0) ===
					n0,
				"409 时没有建线程",
			);
			await shot(page, `B24-conflict-${RUN}`);
			await page.locator("[data-testid=profile-conflict-readonly]").click();
			await page.waitForURL(/\/t\/T-/, { timeout: 60_000 });
			const tid = /\/t\/(T-[^/?#]+)/.exec(page.url())?.[1] ?? "";
			check(!!tid, `选「只读使用」后进入新线程 ${tid}`);
			awake.add(tid);
			const orb = await ensureReady(tid);
			createdOrbs.add(orb);
			made = { tid, orb };
		});
		const m = made as Th | null;
		if (!m) throw new Error("没有建出线程");
		const o = await getOrb(m.orb);
		note(`新线程 desktop.profile ${JSON.stringify(o.desktop?.profile)}`);
		check(
			o.desktop?.profile?.mode === "readonly" &&
				o.desktop.profile.status === "readonly",
			"新线程以只读副本使用该 Profile",
		);
		const wp = await profileGet(wid);
		check(wp.holder?.orbId === h.orb, "持有者仍是原线程（只读不抢租约）");
	} finally {
		awake.delete(h.tid);
		await deleteOrb(h.orb, false);
		const m = made as Th | null;
		if (m) {
			awake.delete(m.tid);
			await deleteOrb(m.orb, false);
		}
	}
}

// ---------------------------------------------------------------- 主流程

const TESTS: [string, () => Promise<void>][] = [
	["B1", B1],
	["B2", B2],
	["B3", B3],
	["B4", B4],
	["B5", B5],
	["B6", B6],
	["B7", B7],
	["B8", B8],
	["B9", B9],
	["B10", B10],
	["B11", B11],
	["B12", B12],
	["B13", B13],
	["B14", B14],
	["B15", B15],
	["B16", B16],
	["B17", B17],
	["B18", B18],
	["B19", B19],
	["B21", B21],
	["B22", B22],
	["B23", B23],
	["B24", B24],
	["B20", B20],
];

SESSION =
	process.env.M8_SESSION ??
	(
		await sh("./bin/orynd --config dev/orynd.yaml admin create-web-session")
	).stdout.trim();
if (!SESSION)
	throw new Error(
		"没有网页会话（M8_SESSION 或 orynd admin create-web-session）",
	);

const keepTimer = setInterval(() => void keepTick(), 5_000);

const browser: Browser = await chromium.launch({
	executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
	headless: true,
	args: ["--no-sandbox"],
});
ctxA = await browser.newContext({
	locale: "zh-CN",
	viewport: { width: 1400, height: 900 },
	permissions: ["clipboard-read", "clipboard-write"],
	extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
});
await ctxA.addInitScript(() => localStorage.setItem("oryn.debug", "1"));
ctxB = await browser.newContext({ locale: "zh-CN" });
await ctxB.addCookies([
	{
		name: "__Host-oryn_session",
		value: SESSION,
		// 不带前导点的 domain 在 CDP 里就是 host-only，满足 __Host- 前缀
		domain: BASE_URL.hostname,
		path: "/",
		secure: true,
		httpOnly: true,
		sameSite: "Lax",
	},
]);
await ctxB.addInitScript(() => {
	const w = window as unknown as {
		__wsCloses: { url: string; code: number; t: number }[];
	};
	w.__wsCloses = [];
	// 用 Proxy 而不是子类：返回的实例仍是原生 WebSocket，原型方法不变。
	// noVNC 的 Websock.attach 只查实例自身与其直接原型的属性名，子类原型上只有 constructor，
	// 会报「Raw channel missing property: send」导致桌面连不上
	window.WebSocket = new Proxy(window.WebSocket, {
		construct(Target, args: ConstructorParameters<typeof WebSocket>) {
			const ws = new Target(...args);
			ws.addEventListener("close", (e) =>
				w.__wsCloses.push({
					url: String(args[0]),
					code: e.code,
					t: Date.now(),
				}),
			);
			return ws;
		},
	});
});
ctxC = await browser.newContext({ locale: "zh-CN" });
ctxM = await browser.newContext({
	locale: "zh-CN",
	viewport: { width: 390, height: 800 },
	hasTouch: true,
	isMobile: true,
	extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
});
console.log(
	`M8 验收 run=${RUN} base=${BASE} 宿主站点=${HOST_IP}:${site.port} old=${OLD_THREAD || "无"} shots=${SHOTS}`,
);

for (const [id, fn] of TESTS) {
	if (ONLY && !ONLY.has(id)) continue;
	evidence = [];
	current = id;
	console.log(`── ${id}`);
	const t0 = Date.now();
	let verdict: Verdict = "PASS";
	try {
		await fn();
	} catch (e) {
		note(
			`${e instanceof Skip ? "跳过" : "错误"}：${e instanceof Error ? e.message : String(e)}`,
		);
		verdict = e instanceof Skip ? "SKIP" : "FAIL";
		if (verdict === "FAIL") {
			const pages = [ctxA, ctxB, ctxC, ctxM].flatMap((c) => c.pages());
			for (const [i, p] of pages.entries())
				await shot(p, `${id}-FAIL-${RUN}-${i}`);
			await xshot(`${id}-FAIL-x`);
		}
	}
	const seconds = (Date.now() - t0) / 1000;
	console.log(`${id} ${verdict} (${seconds.toFixed(1)}s)`);
	results.push({ id, verdict, seconds, evidence });
}

// ---------------------------------------------------------------- 清理

clearInterval(keepTimer);
awake.clear();
keepWs?.close();
await browser.close();
await teardownX();
if (KEEP) console.log("M8_KEEP=1：保留本次建的 orb 与 Profile");
else {
	for (const orb of createdOrbs) {
		const st = orbState(orb);
		if (st !== "deleted" && st !== "deleting")
			await api("DELETE", `/api/v1/orbs/${orb}`);
	}
	for (const pid of createdProfiles)
		await waitFor(
			`删除 Profile ${pid}`,
			async () => {
				const r = await api("DELETE", `/api/v1/browser-profiles/${pid}`);
				return r.status === 204 || r.status === 404;
			},
			120_000,
			2000,
		).catch((e) => console.log(`清理：${String(e)}`));
	console.log(
		`已清理 orb ${createdOrbs.size} 台、Profile ${createdProfiles.size} 个`,
	);
}
site.stop(true);

const out = `${SHOTS}/results-${RUN}.json`;
await Bun.write(
	out,
	JSON.stringify({ run: RUN, results, pageErrors }, null, 2),
);
console.log(`\n汇总（${out}）`);
for (const r of results)
	console.log(`${r.id} ${r.verdict} (${r.seconds.toFixed(1)}s)`);
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
