#!/usr/bin/env bun
import { existsSync, mkdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { deflateSync } from "node:zlib";
/**
 * M9.6 真机验收（原稿 local://m96-sidebar-identity-plan.md「验证」R2–R18；修订稿 local://m96-revision.md §10 替换 R13–R16、
 * 新增 R19–R25，冲突时以修订稿为准）：对 dev 部署逐项执行，每项打印 `R<n> PASS|FAIL|SKIP (耗时)` 与证据，
 * 失败时给打开的页面截图，结束时写 results JSON（格式同 m9-accept.ts）。R1 是自动化测试（make test / bun run test），不在这里跑。
 *
 * 用法（web/ 目录）：bun run accept:m96 [-- --only R4,R14]
 *
 * 前提（dev 状态）：
 *   - orynd 跑在 ORYN_URL，docker provider 在线；Oryn Core oryn-v18.19.0-4 已发布、orb 镜像 oryn-orb:dev 已用新 Core 重建、
 *     协调宿主的 omp 已换成新 Core（R7 / R24 需要协调宿主可用）。
 *   - 旧 Core 线程 $OLD：M96_OLD_THREAD，缺省读 .dev/m96old.thread（升级前建、发过消息，orb 未删除，容器来自 oryn-orb:core3）。
 *     脚本不删 $OLD，也不删它的 orb。
 *   - omp-home 的 config.yml（`oryn config push` 推上去的）里 cycleOrder 至少有三个 role 的 `modelRoles` 是 `provider/id[:level]`
 *     且模型在 agent 的 get_available_models 里，另外不算 default 的别名目标；不满足时 R13–R16、R20、R23、R24 失败并打印原因。
 *   - 宿主有 sqlite3、docker、Chromium（CHROMIUM，默认 /usr/bin/chromium）；设备凭据 .dev/credentials.json（或 ORYN_TOKEN）。
 *   - R18 是桌面壳人工走查：需要桌面壳（web/src-tauri/target/release/oryn-desktop 或 `cd web && bun run tauri dev`）与图形会话
 *     （无显示器时用 `dbus-run-session -- xvfb-run -a …` 起壳，同 accept:m6）。脚本只打印清单。
 *
 * 环境变量：
 *   ORYN_URL（默认 http://localhost:7600）、ORYN_ROOT、ORYN_TOKEN（默认 .dev/credentials.json 的 token）；
 *   M96_PROJECT（真实线程挂的项目名，默认 sandbox；不存在时建一个无仓库的同名项目）；
 *   M96_EDIT_PROJECT（R8 / R17 的项目 $P；缺省本次新建一个无仓库项目 m96-accept-<run>，结束时删除；给了已有项目时结束时还原其 setup 脚本）；
 *   M96_ROLE_A / M96_ROLE_B / M96_ROLE_C（覆盖自动挑选的 role；A 设头像、B 不设、C 是 R16 里「此前没用过且有头像」的 role）；
 *   M96_OLD_THREAD（R23）；M96_FALLBACK_THREAD（R22：一个发生过重试回退的线程，缺省 SKIP）；
 *   M96_NO_DESKTOP=1（R18 显式 SKIP）；M96_SHELL_OK=1（R18 人工走查通过后记 PASS，否则 SKIP；SKIP 不算 PASS）；
 *   M96_KEEP=1（不删本次建的线程、orb、分享与项目；身份与头像设置仍然还原）；M96_SHOTS（默认 /tmp/oryn-shots/m96-accept）；CHROMIUM。
 *
 * 约定：
 *   - 库直接读 .dev/data/oryn.db（sqlite3 -json）；来源记录统一用
 *     `select seq, payload from entries where thread_id = … and type = 'custom' and instr(payload, 'oryn-model-source') > 0 order by seq`
 *     （再限主会话文件、未 stale）。
 *   - 开跑前备份 agent / 助手 / 用户三组身份与全部 role 头像（另写 <shots>/identity-backup-<run>.json），结束时（含场景失败）还原。
 *   - $A / $B / $C / $U：cycleOrder 里模型可用、且不是 default 别名目标的 role，按顺序取（环境变量可覆盖 A / B / C）；
 *     A、C、U（若有）设各不相同尺寸的 PNG 头像（data URL 前 64 个字符互不相同，便于查泄漏），B 清掉头像；
 *     default 的别名目标（config.yml 的 `modelRoles.default`，只跟一跳）也设头像，R19 再与库里记录的 `alias` 对照。
 *   - 线程：$R（R19 建，R13–R16、R3 / R5 / R6 / R11 / R17 共用）、$D（R20）、R21 从新建线程框建的线程、助手对话（R7 / R24）。
 *     单独 `--only` 时按需现建。保活：本进程一条 /api/v1/ws，每 5 秒对需要醒着的线程发 `focus`（同 m9）。
 *   - 回合头读 `[data-testid=transcript] [data-turn-identity]`（滚动整个转录收集，虚拟列表不全量渲染），签名 =
 *     `data-turn-identity / data-role / data-turn-source`，另取 `turn-name` 文字、头像（图片 src 或字形）与模型短名。
 *   - 全新浏览器上下文用 `browser.newContext(...)` 另起（同一个设备令牌头）；匿名上下文不带任何鉴权头。
 * 场景顺序：身份类（R19 R13 R14 R20 R21 R15 R22 R23 R24 R16 R25）在前，侧栏类（R2–R12 R17 R18）在后——R19 要观察 $R 第一个回合的流式过程。
 * 与计划的偏差（都写进证据）：
 *   - R14 / R20 / R23 / R24「点轨道里的 role」改为在模型对话框（`/model`）的角色列表里点 `[data-role=…]`：输入坞上方的角色轨道
 *     只在 Ctrl+P 切换时浮出，而 Ctrl+P 本身就会切一次 role（多出一条来源记录）。两处走同一个 `requestRole`。
 *   - R8 默认用本次新建的项目（改 setup 脚本会影响之后在该项目里开的 orb，不动 sandbox）。
 *   - R16 先给 Agent 身份设名字与头像：Agent 身份为空时分享页的回落是默认品牌标（`default`），与站内的 `agent` 无从比较。
 *   - R19「流式期间回合头已是最终身份」：第一个回合没观察到流式（太快）时，在同一条启动记录下再发一条较长的消息观察，至多 3 次。
 *   - R24 在计划的两步之后多一步「模型列表手动选」，硬断言回落为助手身份（`assistant`）。
 *   - R25 另加一张渐变图（非正方形）：拖动 + 缩放后核对「预览 = 保存」与「保存 = 源图对应区域」。
 *   - R18 按原稿是人工走查，脚本只打印清单。
 */
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Locator,
	type Page,
	type WebSocket as PwWebSocket,
} from "playwright-core";
import { MORE_ENTRIES } from "../src/components/layout/more-entries";
import zh from "../src/i18n/locales/zh-CN.json";

const ROOT = resolve(process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`);
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const CREDS = `${ROOT}/.dev/credentials.json`;
const TOKEN =
	process.env.ORYN_TOKEN ??
	(JSON.parse(readFileSync(CREDS, "utf8")) as { token: string }).token;
const SHOTS = process.env.M96_SHOTS ?? "/tmp/oryn-shots/m96-accept";
const DB = `${ROOT}/.dev/data/oryn.db`;
const ORYN = `${ROOT}/bin/oryn`;
const RUN = Math.random().toString(36).slice(2, 6);
const PROJECT = process.env.M96_PROJECT ?? "sandbox";
const KEEP = process.env.M96_KEEP === "1";
const TZ = "Asia/Shanghai";
const DESKTOP_BIN =
	process.env.ORYN_DESKTOP_BIN ??
	`${ROOT}/web/src-tauri/target/release/oryn-desktop`;
mkdirSync(SHOTS, { recursive: true });

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

/** 视口：桌面、手机两档（计划「浏览器视口」）；转录类页面用高一点的桌面视口，少滚几屏。 */
const DESKTOP = { width: 1280, height: 860 };
const TALL = { width: 1280, height: 1400 };
const PHONE = { width: 390, height: 844 };
const PHONE_S = { width: 360, height: 780 };

// ---------------------------------------------------------------- 工具

const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const sqlStr = (s: string) => `'${s.replaceAll("'", "''")}'`;
const te = new TextEncoder();

interface ShRes {
	code: number;
	out: string;
	stdout: string;
	stderr: string;
}

/** 异步跑 shell（不卡事件循环：保活连接要在等待期间照常发送）。 */
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
	return {
		code,
		out: `${stdout}${stderr}`.trim(),
		stdout: stdout.trim(),
		stderr: stderr.trim(),
	};
}

/** 宿主上的 oryn CLI（设备凭据）。 */
const cli = (args: string, timeoutMs = 120_000) =>
	sh(`ORYN_CREDENTIALS=${q(CREDS)} ${q(ORYN)} ${args}`, { timeoutMs });

function sql<T = Record<string, unknown>>(qs: string): T[] {
	const r = Bun.spawnSync(["sqlite3", "-json", DB, qs]);
	const s = r.stdout.toString().trim();
	return s ? (JSON.parse(s) as T[]) : [];
}

const one = <T = Record<string, unknown>>(qs: string): T | undefined =>
	sql<T>(qs)[0];

interface ApiRes {
	status: number;
	json: unknown;
	text: string;
}

async function api(
	method: string,
	path: string,
	body?: unknown,
	auth = true,
): Promise<ApiRes> {
	const res = await fetch(`${BASE}${path}`, {
		method,
		headers: {
			...(auth ? { Authorization: `Bearer ${TOKEN}`, "X-Oryn-CSRF": "1" } : {}),
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
		// 非 JSON 体
	}
	return { status: res.status, json, text };
}

async function apiJson<T>(method: string, path: string, body?: unknown) {
	const r = await api(method, path, body);
	if (r.status >= 300)
		throw new Error(`${method} ${path} → ${r.status} ${r.text.slice(0, 300)}`);
	return r.json as T;
}

/** 匿名请求（公开分享面）。 */
const anon = (path: string) => api("GET", path, undefined, false);

const codeOf = (r: ApiRes) => (r.json as { code?: string } | null)?.code ?? "";

/** waitFor 里抛它会立即中止等待。 */
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
	intervalMs = 500,
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

// ---------------------------------------------------------------- 结果

type Verdict = "PASS" | "FAIL" | "SKIP";
const results: {
	id: string;
	verdict: Verdict;
	seconds: number;
	evidence: string[];
}[] = [];
let evidence: string[] = [];
const note = (s: string) => {
	evidence.push(s);
	console.log(`    · ${s}`);
};
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};
/** 不中断场景的断言：记下失败，场景跑完后判 FAIL（后面的观察照样收集）。 */
let deferred: string[] = [];
const expect = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) deferred.push(what);
};

class Skip extends Error {}

async function shot(page: Page, name: string) {
	const p = `${SHOTS}/${name}.png`;
	await page.screenshot({ path: p }).catch(() => {});
	note(`截图 ${p}`);
}

const j = (v: unknown) => JSON.stringify(v);

// ---------------------------------------------------------------- 条目

interface ThreadRow {
	state: string;
	last_seq: number;
	orb_id: string | null;
	model: string | null;
	kind: string;
	title: string | null;
	archived_at: number | null;
}

const threadRow = (tid: string) =>
	one<ThreadRow>(
		`select state, last_seq, orb_id, model, kind, title, archived_at from threads where id=${sqlStr(tid)}`,
	);

const orbState = (orb: string) =>
	one<{ state: string }>(`select state from orbs where id=${sqlStr(orb)}`)
		?.state ?? "?";

interface Msg {
	seq: number;
	role: string;
	text: string;
	raw: Record<string, unknown>;
}

/** 主会话文件里 `afterSeq` 之后的消息条目。 */
function messages(tid: string, afterSeq = 0): Msg[] {
	const rows = sql<{ seq: number; p: string }>(
		`select seq, cast(payload as text) p from entries where thread_id=${sqlStr(tid)} and seq>${afterSeq} and type='message' and stale=0 and instr(file, '/')=0 order by seq`,
	);
	const out: Msg[] = [];
	for (const r of rows) {
		const m = (JSON.parse(r.p) as { message?: Record<string, unknown> })
			.message;
		if (!m) continue;
		const c = m.content;
		let text = "";
		if (typeof c === "string") text = c;
		else if (Array.isArray(c))
			text = (c as { type?: string; text?: string }[])
				.filter((b) => b?.type === "text" && b.text)
				.map((b) => b.text)
				.join("\n");
		out.push({ seq: r.seq, role: String(m.role ?? ""), text, raw: m });
	}
	return out;
}

const lastSeq = (tid: string) => threadRow(tid)?.last_seq ?? 0;

const SETTLED: Record<string, true> = {
	idle: true,
	awaiting_input: true,
	error: true,
};

/** 等一个回合结束：`beforeSeq` 之后出现助手消息，且线程处于 idle / awaiting_input / error。 */
function waitTurn(tid: string, beforeSeq: number, timeoutMs = 300_000) {
	return waitFor(
		`线程 ${tid} 回合结束`,
		() => {
			const r = threadRow(tid);
			if (!r || !SETTLED[r.state] || r.last_seq <= beforeSeq) return undefined;
			return messages(tid, beforeSeq).some((m) => m.role === "assistant")
				? r.state
				: undefined;
		},
		timeoutMs,
		1000,
	);
}

/** 会话里的模型选择来源记录（`oryn-model-source`，主会话文件、未 stale、v=1）。 */
interface SourceRec {
	seq: number;
	source: string;
	model: string;
	role?: string;
	alias?: string;
}

function records(tid: string): SourceRec[] {
	const rows = sql<{ seq: number; p: string }>(
		`select seq, cast(payload as text) p from entries where thread_id=${sqlStr(tid)} and type='custom' and stale=0 and instr(file, '/')=0 and instr(payload, 'oryn-model-source') > 0 order by seq`,
	);
	const out: SourceRec[] = [];
	for (const r of rows) {
		const p = JSON.parse(r.p) as {
			customType?: string;
			data?: {
				v?: number;
				source?: string;
				model?: string;
				role?: string;
				alias?: string;
			};
		};
		const d = p.data;
		if (p.customType !== "oryn-model-source" || !d || d.v !== 1) continue;
		out.push({
			seq: r.seq,
			source: String(d.source),
			model: String(d.model),
			role: d.role,
			alias: d.alias,
		});
	}
	return out;
}

const recText = (r: SourceRec | undefined) =>
	r
		? `${r.source}${r.role ? `(${r.role}${r.alias ? `→${r.alias}` : ""})` : ""} ${r.model} @${r.seq}`
		: "无";

// ---------------------------------------------------------------- 保活

const awake = new Set<string>();
let keepWs = null as WebSocket | null;
let keepBusy = false;

async function openWs(): Promise<WebSocket> {
	const r = await api("POST", "/api/v1/ws-ticket");
	const ticket = (r.json as { ticket?: string } | null)?.ticket;
	if (!ticket) throw new Error(`ws-ticket ${r.status}`);
	const ws = new WebSocket(
		`${BASE.replace(/^http/, "ws")}/api/v1/ws?ticket=${encodeURIComponent(ticket)}`,
	);
	const opened = Promise.withResolvers<void>();
	ws.onopen = () => opened.resolve();
	ws.onerror = () => opened.reject(new Error("WS 连接失败"));
	await opened.promise;
	return ws;
}

async function keepTick() {
	if (keepBusy || awake.size === 0) return;
	keepBusy = true;
	try {
		if (!keepWs) {
			const ws = await openWs();
			ws.onmessage = () => {};
			ws.onclose = () => {
				if (keepWs === ws) keepWs = null;
			};
			keepWs = ws;
		}
		for (const tid of awake)
			keepWs.send(JSON.stringify({ type: "focus", threadId: tid }));
		keepWs.send(JSON.stringify({ type: "focus", threadId: null }));
	} catch {
		keepWs = null;
	} finally {
		keepBusy = false;
	}
}

/** 经 WS 对线程发一条 omp 命令，返回 `res.response` 或 `error`。 */
async function wsCmd(
	threadId: string,
	frame: Record<string, unknown>,
	timeoutMs = 90_000,
): Promise<{
	type: string;
	response?: Record<string, unknown>;
	code?: string;
	message?: string;
}> {
	const ws = await openWs();
	const id = `m96-${RUN}-${Math.random().toString(36).slice(2, 8)}`;
	const done = Promise.withResolvers<{
		type: string;
		response?: Record<string, unknown>;
		code?: string;
		message?: string;
	}>();
	ws.onmessage = (e) => {
		const m = JSON.parse(String(e.data)) as {
			type: string;
			id?: string;
			response?: Record<string, unknown>;
			code?: string;
			message?: string;
		};
		if ((m.type === "res" || m.type === "error") && m.id === id)
			done.resolve(m);
	};
	const timer = setTimeout(
		() => done.reject(new Error(`WS cmd ${String(frame.type)} 超时`)),
		timeoutMs,
	);
	ws.send(JSON.stringify({ type: "cmd", threadId, id, frame }));
	try {
		return await done.promise;
	} finally {
		clearTimeout(timer);
		ws.close();
	}
}

// ---------------------------------------------------------------- 线程与 orb

const createdOrbs = new Set<string>();
const createdThreads = new Set<string>();
const createdShares = new Set<string>();

let projectCache = "";
/** 真实线程挂的项目（M96_PROJECT）；不存在时建一个无仓库的同名项目。 */
async function projectId(): Promise<string> {
	if (projectCache) return projectCache;
	const r = one<{ id: string }>(
		`select id from projects where name=${sqlStr(PROJECT)} and archived_at is null`,
	);
	if (r) {
		projectCache = r.id;
		return r.id;
	}
	const p = await apiJson<{ id: string }>("POST", "/api/v1/projects", {
		name: PROJECT,
		shallowClone: true,
	});
	console.log(`  建项目 ${PROJECT} = ${p.id}（无仓库）`);
	projectCache = p.id;
	return p.id;
}

async function ensureReady(tid: string, timeoutMs = 900_000): Promise<string> {
	let lastWake = 0;
	return waitFor(
		`线程 ${tid} 的 orb ready`,
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
			return st === "ready" ? o : undefined;
		},
		timeoutMs,
		2000,
	);
}

/** 建工作线程（挂 M96_PROJECT，不选模型），不等就绪。 */
async function createThread(
	label: string,
	extra: Record<string, unknown> = {},
): Promise<string> {
	const r = await api("POST", "/api/v1/threads", {
		prompt: "只回复 ok",
		projectId: await projectId(),
		title: `${label} ${RUN}`,
		...extra,
	});
	if (r.status !== 201)
		throw new Error(`建线程 ${label} ${r.status} ${r.text.slice(0, 300)}`);
	const tid = (r.json as { thread: { id: string } }).thread.id;
	createdThreads.add(tid);
	awake.add(tid);
	note(`建线程 ${label} = ${tid}`);
	return tid;
}

/** 等线程的 orb 就绪与第一个回合结束。 */
async function settleFirstTurn(tid: string) {
	const orb = await ensureReady(tid);
	createdOrbs.add(orb);
	await waitTurn(tid, 0, 600_000);
	note(`线程 ${tid} 首回合结束，orb ${orb}`);
}

let R = "";
/** $R：不选模型的新线程（R19 建；单独跑别的场景时现建）。 */
async function ensureR(): Promise<string> {
	if (R) return R;
	const tid = await createThread("m96-R");
	await settleFirstTurn(tid);
	R = tid;
	return R;
}

let D = "";
async function ensureD(): Promise<string> {
	if (D) return D;
	const tid = await createThread("m96-D");
	await settleFirstTurn(tid);
	D = tid;
	return D;
}

let editProjectId = "";
let editProjectCreated = false;
let editProjectSetup: string | undefined;
/** $P（R8 / R17）：M96_EDIT_PROJECT，缺省本次新建。 */
async function ensureEditProject(): Promise<{ id: string; name: string }> {
	const want = process.env.M96_EDIT_PROJECT;
	if (editProjectId) {
		const p = await apiJson<{ id: string; name: string }>(
			"GET",
			`/api/v1/projects/${editProjectId}`,
		);
		return p;
	}
	if (want) {
		const r = one<{ id: string }>(
			`select id from projects where name=${sqlStr(want)} and archived_at is null`,
		);
		if (!r) throw new Error(`M96_EDIT_PROJECT=${want} 不存在`);
		editProjectId = r.id;
		const p = await apiJson<{ id: string; name: string; setupScript?: string }>(
			"GET",
			`/api/v1/projects/${r.id}`,
		);
		editProjectSetup = p.setupScript ?? undefined;
		return p;
	}
	const p = await apiJson<{ id: string; name: string }>(
		"POST",
		"/api/v1/projects",
		{ name: `m96-accept-${RUN}`, shallowClone: true },
	);
	editProjectId = p.id;
	editProjectCreated = true;
	note(`建项目 $P = ${p.name}（${p.id}，无仓库）`);
	return p;
}

/** agent 的可用模型（get_available_models；线程须就绪）。 */
async function availableModels(tid: string): Promise<Set<string>> {
	await ensureReady(tid);
	const r = await wsCmd(tid, { type: "get_available_models" });
	const data = (r.response?.data ?? {}) as {
		models?: { provider: string; id: string }[];
	};
	if (!data.models)
		throw new Error(`get_available_models 失败：${j(r).slice(0, 300)}`);
	return new Set(data.models.map((m) => `${m.provider}/${m.id}`));
}

// ---------------------------------------------------------------- 身份与 role

interface AgentIdentity {
	name?: string | null;
	avatarIcon?: string | null;
	avatarImage?: string | null;
}
interface RoleIdentity {
	role: string;
	name?: string | null;
	inCycle: boolean;
	selector?: string | null;
	avatarIcon?: string | null;
	avatarImage?: string | null;
	models?: unknown;
}

/** 去掉 null / 空串（PUT 是整体替换，空字段即清除）。 */
function clean(v: AgentIdentity): AgentIdentity {
	const out: AgentIdentity = {};
	if (v.name) out.name = v.name;
	if (v.avatarIcon) out.avatarIcon = v.avatarIcon;
	if (v.avatarImage) out.avatarImage = v.avatarImage;
	return out;
}

const getIdentity = (who: "agent" | "assistant" | "user") =>
	apiJson<AgentIdentity>("GET", `/api/v1/settings/${who}-identity`);
const putIdentity = (
	who: "agent" | "assistant" | "user",
	body: AgentIdentity,
) => apiJson<AgentIdentity>("PUT", `/api/v1/settings/${who}-identity`, body);
const roleList = async () =>
	apiJson<{ roles: RoleIdentity[]; configError?: string }>(
		"GET",
		"/api/v1/settings/role-identities",
	);
const putRole = (
	role: string,
	avatar: { avatarIcon?: string; avatarImage?: string } = {},
) =>
	api("PUT", "/api/v1/settings/role-identities", {
		role,
		...avatar,
	});

interface Backup {
	agent: AgentIdentity;
	assistant: AgentIdentity;
	user: AgentIdentity;
	roles: RoleIdentity[];
}
let backup: Backup | null = null;

async function backupIdentities() {
	backup = {
		agent: clean(await getIdentity("agent")),
		assistant: clean(await getIdentity("assistant")),
		user: clean(await getIdentity("user")),
		roles: (await roleList()).roles,
	};
	await Bun.write(
		`${SHOTS}/identity-backup-${RUN}.json`,
		JSON.stringify(backup, null, 2),
	);
	console.log(
		`身份备份：${SHOTS}/identity-backup-${RUN}.json（role 头像 ${backup.roles.filter(hasAvatar).length} 个）`,
	);
}

const hasAvatar = (r: {
	avatarIcon?: string | null;
	avatarImage?: string | null;
}) => Boolean(r.avatarIcon || r.avatarImage);

async function restoreIdentities() {
	if (!backup) return;
	const errs: string[] = [];
	for (const who of ["agent", "assistant", "user"] as const) {
		const r = await api("PUT", `/api/v1/settings/${who}-identity`, backup[who]);
		if (r.status >= 300) errs.push(`${who} ${r.status}`);
	}
	const now = (await roleList()).roles;
	const before = new Map(backup.roles.map((r) => [r.role, r]));
	for (const r of now) {
		const b = before.get(r.role);
		const want = b && hasAvatar(b);
		if (want && b) {
			if (b.avatarIcon === r.avatarIcon && b.avatarImage === r.avatarImage)
				continue;
			const res = await putRole(
				r.role,
				b.avatarImage
					? { avatarImage: b.avatarImage }
					: { avatarIcon: b.avatarIcon ?? undefined },
			);
			if (res.status >= 300) errs.push(`role ${r.role} ${res.status}`);
		} else if (hasAvatar(r)) {
			const res = await putRole(r.role);
			if (res.status >= 300) errs.push(`role ${r.role} 清除 ${res.status}`);
		}
	}
	console.log(
		`还原身份与 role 头像：${errs.length ? `失败 ${errs.join("、")}` : "完成"}`,
	);
}

/** omp-home 的 config.yml（经 agent-config 接口读，不碰磁盘）。 */
interface OmpConfig {
	cycleOrder?: unknown;
	modelRoles?: Record<string, unknown>;
	modelTags?: Record<string, { name?: unknown }>;
}
let cfgCache: OmpConfig | null = null;
async function ompConfig(): Promise<OmpConfig> {
	if (cfgCache) return cfgCache;
	const r = await api("GET", "/api/v1/agent-config/files/config.yml");
	if (r.status === 404) {
		cfgCache = {};
		return cfgCache;
	}
	if (r.status >= 300)
		throw new Error(`读 config.yml → ${r.status} ${r.text.slice(0, 200)}`);
	const parsed = Bun.YAML.parse(r.text) as OmpConfig | null;
	cfgCache = parsed ?? {};
	return cfgCache;
}

/** `modelRoles.<role>` 的原始值（数组以 ", " 连接；同 internal/agentcfg 的 Selector）。 */
function rawSelector(cfg: OmpConfig, role: string): string | undefined {
	const v = cfg.modelRoles?.[role];
	if (typeof v === "string") return v.trim() || undefined;
	if (Array.isArray(v)) {
		const s = v
			.filter((x): x is string => typeof x === "string")
			.map((x) => x.trim())
			.filter(Boolean)
			.join(", ");
		return s || undefined;
	}
	return undefined;
}

const BUILTIN_ROLES = [
	"default",
	"smol",
	"slow",
	"vision",
	"plan",
	"commit",
	"tiny",
	"memory",
	"task",
	"advisor",
	"widget",
	"image",
	"web",
	"speech",
	"dictation",
	"judge",
];

/** default 的单一别名目标（修订稿 §3.4 `roleAliasTarget`，只跟一跳）。 */
function aliasTarget(cfg: OmpConfig, role: string): string | undefined {
	const raw = rawSelector(cfg, role);
	if (!raw || raw.includes(",")) return undefined;
	let rest: string;
	if (raw === "*" || raw.startsWith("*:")) rest = `default${raw.slice(1)}`;
	else if (raw.startsWith("@")) rest = raw.slice(1);
	else if (raw.startsWith("pi/")) rest = raw.slice(3);
	else return undefined;
	const known = (r: string) =>
		BUILTIN_ROLES.includes(r) || rawSelector(cfg, r) !== undefined;
	let target: string | undefined;
	if (known(rest)) target = rest;
	else {
		const i = rest.lastIndexOf(":");
		if (i > 0 && known(rest.slice(0, i))) target = rest.slice(0, i);
	}
	return target && target !== role ? target : undefined;
}

const LEVELS = new Set([
	"inherit",
	"off",
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
	"auto",
]);

/** 字面选择器 `provider/id[:level]` → `provider/id`；别名、回退链、裸 id → undefined。 */
function directModel(selector: string | undefined): string | undefined {
	if (!selector || selector.includes(",")) return undefined;
	if (/^(@|pi\/|\*)/.test(selector)) return undefined;
	let s = selector;
	const i = s.lastIndexOf(":");
	if (i > 0 && LEVELS.has(s.slice(i + 1))) s = s.slice(0, i);
	const slash = s.indexOf("/");
	return slash > 0 && slash < s.length - 1 ? s : undefined;
}

/** role 的显示名（同 TurnHead：modelTags 名 → 内置 role 的本地化名 → id）。 */
function displayName(role: string, list: readonly RoleIdentity[]): string {
	const n = list.find((r) => r.role === role)?.name;
	if (n) return n;
	const names = zh.roles.names as Record<string, string>;
	return Object.hasOwn(names, role) ? (names[role] ?? role) : role;
}

/** 各不相同尺寸的纯色 PNG：data URL 前 64 个字符互不相同（IHDR 的宽高落在其中）。 */
const IMG = {
	alias: () => pngDataUrl(7, 7, [230, 180, 40]),
	A: () => pngDataUrl(9, 9, [40, 90, 230]),
	C: () => pngDataUrl(11, 11, [40, 180, 90]),
	U: () => pngDataUrl(13, 13, [150, 60, 200]),
	A2: () => pngDataUrl(15, 15, [220, 50, 50]),
	assistant: () => pngDataUrl(17, 17, [30, 160, 170]),
};

interface Roles {
	alias?: string;
	A: string;
	B: string;
	C: string;
	U?: string;
	/** A 的模型（provider/id） */
	modelA: string;
	modelB: string;
	modelC: string;
}
let roles: Roles | null = null;
let aliasDone = false;
let aliasRole: string | undefined;

/** default 的别名目标设头像（R19 之前就要设：首回合流式期间的回合头已是最终身份）。 */
async function ensureAliasAvatar(): Promise<string | undefined> {
	if (aliasDone) return aliasRole;
	const cfg = await ompConfig();
	const list = (await roleList()).roles;
	aliasRole = aliasTarget(cfg, "default");
	note(
		`config.yml：default = ${rawSelector(cfg, "default") ?? "（未配置）"} → 别名目标 ${aliasRole ?? "无"}`,
	);
	if (aliasRole && list.some((r) => r.role === aliasRole && r.inCycle)) {
		const r = await putRole(aliasRole, { avatarImage: IMG.alias() });
		check(r.status === 200, `给别名目标 ${aliasRole} 设头像 → ${r.status}`);
	} else if (aliasRole) {
		note(`别名目标 ${aliasRole} 不在 cycleOrder 里，不能设头像`);
	}
	aliasDone = true;
	return aliasRole;
}

/** 挑 A / B / C / U 并设头像（需要 $R 就绪以取可用模型）。 */
async function ensureRoles(): Promise<Roles> {
	if (roles) return roles;
	const alias = await ensureAliasAvatar();
	const tid = await ensureR();
	const models = await availableModels(tid);
	const cfg = await ompConfig();
	const list = (await roleList()).roles;
	const usable = (r: RoleIdentity) => {
		const m = directModel(r.selector ?? rawSelector(cfg, r.role));
		return r.inCycle && m !== undefined && models.has(m) ? m : undefined;
	};
	const cands = list
		.filter((r) => usable(r) && r.role !== alias)
		.map((r) => r.role);
	note(
		`cycleOrder 里模型可用、不是别名目标的 role：${cands.join(" / ") || "无"}（可用模型 ${models.size} 个）`,
	);
	const pick = (env: string | undefined, taken: string[]) => {
		if (env) {
			const r = list.find((x) => x.role === env);
			if (!r || !usable(r))
				throw new Error(
					`${env} 不在 cycleOrder 里或模型不可用（${r?.selector ?? "无配置"}）`,
				);
			return env;
		}
		const c = cands.find((x) => !taken.includes(x));
		if (!c)
			throw new Error(
				"cycleOrder 里可用的 role 不足（A / B / C 各需一个，且不算 default 的别名目标）；用 M96_ROLE_A/B/C 指定或先 oryn config push",
			);
		return c;
	};
	const A = pick(process.env.M96_ROLE_A, []);
	const B = pick(process.env.M96_ROLE_B, [A]);
	const C = pick(process.env.M96_ROLE_C, [A, B]);
	const U = cands.find((x) => ![A, B, C].includes(x));
	const modelOf = (role: string) =>
		directModel(
			list.find((r) => r.role === role)?.selector ?? rawSelector(cfg, role),
		) ?? "";
	roles = {
		alias,
		A,
		B,
		C,
		U,
		modelA: modelOf(A),
		modelB: modelOf(B),
		modelC: modelOf(C),
	};
	for (const [role, img] of [
		[A, IMG.A()],
		[C, IMG.C()],
		...(U ? [[U, IMG.U()] as const] : []),
	] as const) {
		const r = await putRole(role, { avatarImage: img });
		check(r.status === 200, `给 ${role} 设头像 → ${r.status}`);
	}
	const rb = await putRole(B);
	check(rb.status === 200, `清掉 ${B} 的头像 → ${rb.status}`);
	note(
		`$A=${A}（${roles.modelA}）$B=${B}（${roles.modelB}，无头像）$C=${C}（${roles.modelC}）$U=${U ?? "无"}；别名目标 ${alias ?? "无"}`,
	);
	return roles;
}

/** 当前的 role 头像：role → 头像签名（`img:<src>` / `icon:<字形>`）。 */
async function avatarMap(): Promise<Map<string, string>> {
	const m = new Map<string, string>();
	for (const r of (await roleList()).roles) {
		if (r.avatarImage) m.set(r.role, `img:${r.avatarImage}`);
		else if (r.avatarIcon) m.set(r.role, `icon:${r.avatarIcon}`);
	}
	return m;
}

/** 修订稿 §4 第 3、5 步：记录 → 期望的回合头（不处理回退 / 否决，本脚本的线程里不会出现）。 */
function expectedHead(
	rec: SourceRec | undefined,
	fallbackKind: "agent" | "assistant",
	avatars: Map<string, string>,
): { identity: string; role: string | null } {
	if (!rec || rec.source === "manual")
		return { identity: fallbackKind, role: null };
	for (const r of [rec.role, rec.alias])
		if (r && avatars.has(r)) return { identity: "role", role: r };
	return { identity: fallbackKind, role: null };
}

// ---------------------------------------------------------------- PNG

const CRC_TABLE = (() => {
	const t = new Uint32Array(256);
	for (let n = 0; n < 256; n++) {
		let c = n;
		for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
		t[n] = c >>> 0;
	}
	return t;
})();

function crc32(buf: Uint8Array): number {
	let c = 0xffffffff;
	for (const b of buf) c = (CRC_TABLE[(c ^ b) & 0xff] ?? 0) ^ (c >>> 8);
	return (c ^ 0xffffffff) >>> 0;
}

type Rgb = readonly [number, number, number];

/** 最小 PNG 编码（RGB 8 位、无交错）。 */
function pngBytes(w: number, h: number, px: (x: number, y: number) => Rgb) {
	const row = w * 3 + 1;
	const raw = Buffer.alloc(row * h);
	for (let y = 0; y < h; y++) {
		raw[y * row] = 0;
		for (let x = 0; x < w; x++) {
			const [r, g, b] = px(x, y);
			const o = y * row + 1 + x * 3;
			raw[o] = r;
			raw[o + 1] = g;
			raw[o + 2] = b;
		}
	}
	const chunk = (type: string, data: Buffer) => {
		const len = Buffer.alloc(4);
		len.writeUInt32BE(data.length);
		const td = Buffer.concat([Buffer.from(type, "ascii"), data]);
		const crc = Buffer.alloc(4);
		crc.writeUInt32BE(crc32(td));
		return Buffer.concat([len, td, crc]);
	};
	const ihdr = Buffer.alloc(13);
	ihdr.writeUInt32BE(w, 0);
	ihdr.writeUInt32BE(h, 4);
	ihdr[8] = 8;
	ihdr[9] = 2;
	return Buffer.concat([
		Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
		chunk("IHDR", ihdr),
		chunk("IDAT", deflateSync(raw)),
		chunk("IEND", Buffer.alloc(0)),
	]);
}

const pngDataUrl = (w: number, h: number, rgb: Rgb) =>
	`data:image/png;base64,${pngBytes(w, h, () => rgb).toString("base64")}`;

// ---------------------------------------------------------------- 浏览器

let browser: Browser | null = null;
let ctxA: BrowserContext | null = null;
const extraContexts = new Set<BrowserContext>();
const pageErrors: string[] = [];
/** 当前场景里各页面的控制台错误（R23 断言为空）。 */
let consoleErrors: string[] = [];

let current = "";

async function getBrowser(): Promise<Browser> {
	if (!browser)
		browser = await chromium.launch({
			executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
			headless: true,
			args: ["--no-sandbox"],
		});
	return browser;
}

function watch(page: Page) {
	page.on("pageerror", (e) => {
		pageErrors.push(`${current} ${page.url()}: ${e.message}`);
		consoleErrors.push(`pageerror ${e.message}`);
	});
	page.on("console", (m) => {
		if (m.type() === "error")
			consoleErrors.push(`console.error ${m.text().slice(0, 300)}`);
	});
}

/** 另起一个浏览器上下文（`auth` = 带设备令牌头；否则匿名）。 */
async function newContext(
	auth: boolean,
	extra: Parameters<Browser["newContext"]>[0] = {},
): Promise<BrowserContext> {
	const ctx = await (await getBrowser()).newContext({
		locale: "zh-CN",
		timezoneId: TZ,
		viewport: DESKTOP,
		...(auth ? { extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` } } : {}),
		...extra,
	});
	extraContexts.add(ctx);
	return ctx;
}

async function newPage(viewport = DESKTOP) {
	if (!ctxA) ctxA = await newContext(true);
	const page = await ctxA.newPage();
	await page.setViewportSize(viewport);
	watch(page);
	return page;
}

async function withPage<T>(
	fn: (page: Page) => Promise<T>,
	viewport?: { width: number; height: number },
): Promise<T> {
	const page = await newPage(viewport);
	try {
		return await fn(page);
	} catch (e) {
		await shot(page, `${current}-FAIL-${RUN}`);
		throw e;
	} finally {
		await page.close();
	}
}

const pathOf = (page: Page) => {
	const u = new URL(page.url());
	return u.pathname + u.search;
};

async function waitPath(page: Page, want: string | RegExp, timeoutMs = 15_000) {
	return waitFor(
		`路径 ${String(want)}（当前 ${pathOf(page)}）`,
		() => {
			const p = pathOf(page);
			return (typeof want === "string" ? p === want : want.test(p))
				? p
				: undefined;
		},
		timeoutMs,
		100,
	);
}

const tabBar = (page: Page) => page.getByTestId("section-tabbar").first();
const tab = (page: Page, key: string) =>
	tabBar(page).locator(`button[data-tab=${key}]`);
const pane = (page: Page, id: string) =>
	page.locator(`[data-testid=section-pane][data-section=${id}]`).first();

async function activeTab(page: Page): Promise<string | null> {
	return tabBar(page)
		.locator('button[aria-current="true"]')
		.getAttribute("data-tab", { timeout: 5_000 })
		.catch(() => null);
}

async function waitTab(page: Page, key: string) {
	return waitFor(
		`底栏选中 ${key}`,
		async () => ((await activeTab(page)) === key ? key : undefined),
		10_000,
		100,
	);
}

/** `<html data-conn>`（AppShell 写入的连接状态）。 */
const connOf = (page: Page) =>
	page.evaluate(() => document.documentElement.dataset.conn ?? "");

const waitOpen = (page: Page) =>
	waitFor(
		"连接 open",
		async () => (await connOf(page)) === "open",
		30_000,
		200,
	);

const composer = (page: Page) =>
	page.getByLabel(zh.composer.label, { exact: true });

/** 在输入坞发一条消息，等这一回合结束。 */
async function sendUi(page: Page, tid: string, text: string) {
	const before = lastSeq(tid);
	const box = composer(page);
	await box.waitFor({ timeout: 30_000 });
	await box.fill(text);
	await box.press("Enter");
	note(`→ ${tid.slice(0, 14)}…「${text}」`);
	await waitTurn(tid, before);
	return before;
}

/** 输入 `/model` 打开模型对话框，等角色列表与模型列表都出来。 */
async function openModelDialog(page: Page) {
	const box = composer(page);
	await box.waitFor({ timeout: 30_000 });
	await box.fill("/model");
	await box.press("Enter");
	const dlg = page
		.getByRole("dialog")
		.filter({ has: page.locator("button[data-model]") });
	await dlg.locator("button[data-model]").first().waitFor({ timeout: 60_000 });
	await dlg.locator("button[data-role]").first().waitFor({ timeout: 30_000 });
	return dlg;
}

/** 在模型对话框的角色列表里点 role（收在「更多角色」里时先展开）。 */
async function clickRole(page: Page, role: string) {
	const dlg = await openModelDialog(page);
	const btn = dlg.locator(`button[data-role="${role}"]`).first();
	if (!(await btn.isVisible()))
		await dlg
			.locator("details > summary")
			.first()
			.click()
			.catch(() => {});
	await btn.click();
	await dlg.waitFor({ state: "hidden", timeout: 15_000 });
}

/** 点 role 并等来源记录入库（新 Core）。 */
async function pickRole(page: Page, tid: string, role: string) {
	const n = records(tid).length;
	await clickRole(page, role);
	const rec = await waitFor(
		`来源记录 role=${role}`,
		() => {
			const rs = records(tid);
			const last = rs.at(-1);
			return rs.length > n && last?.source === "role" && last.role === role
				? last
				: undefined;
		},
		45_000,
	);
	note(`点 role ${role} → 记录 ${recText(rec)}`);
	return rec;
}

/** 在模型列表里直接点模型并等来源记录入库。 */
async function pickModel(page: Page, tid: string, model: string) {
	const n = records(tid).length;
	const dlg = await openModelDialog(page);
	await dlg.locator(`button[data-model="${model}"]`).first().click();
	await dlg.waitFor({ state: "hidden", timeout: 15_000 });
	const rec = await waitFor(
		`来源记录 manual ${model}`,
		() => {
			const rs = records(tid);
			const last = rs.at(-1);
			return rs.length > n && last?.source === "manual" ? last : undefined;
		},
		45_000,
	);
	note(`模型列表选 ${model} → 记录 ${recText(rec)}`);
	return rec;
}

interface Head {
	idx: number;
	identity: string;
	role: string | null;
	source: string | null;
	name: string | null;
	avatar: string;
	model: string | null;
}

const sig = (h: Head) => `${h.identity}/${h.role ?? "-"}/${h.source ?? "-"}`;
const headText = (h: Head) =>
	`${sig(h)}${h.name ? ` 「${h.name}」` : ""} ${h.avatar.slice(0, 40)} ${h.model ?? ""}`;

/** 滚动整个转录，按行序号收集全部回合头（虚拟列表只渲染可见附近的行）。 */
async function heads(page: Page): Promise<Head[]> {
	await page.getByTestId("transcript").first().waitFor({ timeout: 30_000 });
	return page.evaluate(async () => {
		const sc = document.querySelector<HTMLElement>("[data-testid=transcript]");
		if (!sc) return [];
		const out = new Map<number, Head>();
		const grab = () => {
			for (const el of sc.querySelectorAll("[data-turn-identity]")) {
				const row = el.closest("[data-index]");
				const idx = Number(row?.getAttribute("data-index") ?? -1);
				const av = el.querySelector("[data-avatar]");
				const avatar =
					av instanceof HTMLImageElement
						? `img:${av.getAttribute("src") ?? ""}`
						: av?.getAttribute("data-avatar") === "icon"
							? `icon:${av.textContent ?? ""}`
							: "default";
				out.set(idx, {
					idx,
					identity: el.getAttribute("data-turn-identity") ?? "",
					role: el.getAttribute("data-role"),
					source: el.getAttribute("data-turn-source"),
					name:
						el.querySelector("[data-testid=turn-name]")?.textContent ?? null,
					avatar,
					model: el.querySelector("span[title]")?.textContent ?? null,
				});
			}
		};
		const frame = () =>
			new Promise<void>((r) =>
				requestAnimationFrame(() => setTimeout(() => r(), 40)),
			);
		// 转录贴底：程序设的 scrollTop 没有「用户往上翻」的意图会被拉回底部，长线程就扫不到前面的回合头。
		// 每一步先发一个向上的 wheel 事件声明意图（Transcript 被动监听 wheel deltaY<0）
		const intent = () =>
			sc.dispatchEvent(new WheelEvent("wheel", { deltaY: -1, bubbles: true }));
		intent();
		sc.scrollTop = 0;
		await frame();
		await frame();
		grab();
		for (let i = 0; i < 300; i++) {
			if (sc.scrollTop + sc.clientHeight >= sc.scrollHeight - 1) break;
			intent();
			sc.scrollTop += Math.max(120, sc.clientHeight * 0.6);
			await frame();
			await frame();
			grab();
		}
		sc.scrollTop = sc.scrollHeight;
		return [...out.values()].sort((a, b) => a.idx - b.idx);
	});
}

/** 等回合头数量到 `n`（转录入库与渲染有延迟）。 */
async function waitHeads(page: Page, n: number, timeoutMs = 60_000) {
	return waitFor(
		`回合头数量到 ${n}`,
		async () => {
			const hs = await heads(page);
			return hs.length >= n ? hs : undefined;
		},
		timeoutMs,
		1000,
	);
}

const logHeads = (label: string, hs: Head[]) =>
	note(
		`${label}：${hs.map((h, i) => `#${i + 1} ${headText(h)}`).join(" ｜ ")}`,
	);

/** 打开线程页（工作线程 `/t/<id>`、助手对话 `/assistant/<id>`），等输入坞出来（空对话没有转录行）。 */
async function openThread(page: Page, tid: string, path = `/t/${tid}`) {
	await page.goto(`${BASE}${path}`);
	await composer(page).waitFor({ timeout: 60_000 });
}

/** 线程已完成的回合数（主会话里的 user 消息数；本脚本每条 user 消息都得到一个回合）。 */
const turnCount = (tid: string) =>
	messages(tid).filter((m) => m.role === "user").length;

/** 收集页面 WS 收发的帧（JSON 文本）。 */
function wsTap(page: Page) {
	const sent: Record<string, unknown>[] = [];
	const received: Record<string, unknown>[] = [];
	const parse = (s: string | Buffer) => {
		try {
			return JSON.parse(String(s)) as Record<string, unknown>;
		} catch {
			return null;
		}
	};
	page.on("websocket", (ws: PwWebSocket) => {
		ws.on("framesent", (f) => {
			const m = parse(f.payload);
			if (m) sent.push(m);
		});
		ws.on("framereceived", (f) => {
			const m = parse(f.payload);
			if (m) received.push(m);
		});
	});
	return { sent, received };
}

const frameType = (m: Record<string, unknown>) =>
	String((m.frame as { type?: string } | undefined)?.type ?? "");

/** 页面上解码 data URL，取像素（x, y 为图像坐标）。 */
async function decodeImage(
	page: Page,
	url: string,
	pts: readonly (readonly [number, number])[],
) {
	return page.evaluate(
		async ({ url, pts }) => {
			const img = new Image();
			img.src = url;
			await img.decode();
			const c = document.createElement("canvas");
			c.width = img.naturalWidth;
			c.height = img.naturalHeight;
			const g = c.getContext("2d");
			if (!g) throw new Error("no 2d context");
			g.drawImage(img, 0, 0);
			return {
				w: img.naturalWidth,
				h: img.naturalHeight,
				px: pts.map(([x, y]) =>
					Array.from(g.getImageData(x, y, 1, 1).data.slice(0, 3)),
				),
			};
		},
		{ url, pts },
	);
}

// ---------------------------------------------------------------- 场景：身份

/** R19 新线程首回合：启动记录、回合头身份、流式期间不跳变。 */
async function R19() {
	const alias = await ensureAliasAvatar();
	await withPage(async (page) => {
		let tid = R;
		let fresh = false;
		if (!tid) {
			tid = await createThread("m96-R");
			R = tid;
			fresh = true;
		}
		awake.add(tid);
		await page.goto(`${BASE}/t/${tid}`);
		// 记下回合头随时间的变化：每 40ms 采一次，变化时追加（含「此刻有没有在途的流式消息」）
		await page.evaluate(() => {
			const w = window as unknown as {
				__m96?: { t: number; live: boolean; heads: string[] }[];
			};
			w.__m96 = [];
			let last = "";
			setInterval(() => {
				const hs = [
					...document.querySelectorAll(
						"[data-testid=transcript] [data-turn-identity]",
					),
				].map(
					(el) =>
						`${el.getAttribute("data-turn-identity")}/${el.getAttribute("data-role") ?? "-"}/${el.getAttribute("data-turn-source") ?? "-"}`,
				);
				const live = !!document.querySelector(
					'[data-testid=transcript] [data-live="1"]',
				);
				const key = `${live}|${hs.join(",")}`;
				if (key !== last) {
					last = key;
					w.__m96?.push({ t: Date.now(), live, heads: hs });
				}
			}, 40);
		});
		const readObs = () =>
			page.evaluate(
				() =>
					(
						window as unknown as {
							__m96?: { t: number; live: boolean; heads: string[] }[];
						}
					).__m96 ?? [],
			);
		if (fresh) await settleFirstTurn(tid);
		else await ensureReady(tid);
		const rs = records(tid);
		const first = rs[0];
		note(`记录：${rs.map(recText).join("；")}`);
		check(
			first?.source === "startup" && first.role === "default",
			`第一条记录 source=startup、role=default（${recText(first)}）`,
		);
		check(
			first?.alias === alias,
			`记录的 alias = config.yml 里 default 的别名目标（${first?.alias ?? "无"} / ${alias ?? "无"}）`,
		);
		const firstUser = messages(tid).find((m) => m.role === "user");
		check(
			!!firstUser && !!first && first.seq < firstUser.seq,
			`记录 seq ${first?.seq} 小于第一条 user 消息 seq ${firstUser?.seq}`,
		);
		const avatars = await avatarMap();
		const want = expectedHead(first, "agent", avatars);
		note(
			`期望首回合：${want.identity}/${want.role ?? "-"}（default ${avatars.has("default") ? "有" : "无"}头像，别名目标 ${alias ?? "无"} ${alias && avatars.has(alias) ? "有" : "无"}头像）`,
		);
		// 流式期间的回合头：第一个回合没观察到流式（太快、或页面打开时已结束）就再发较长的消息（同一条启动记录下）
		let observed = false;
		for (let attempt = 1; attempt <= 3 && !observed; attempt++) {
			const hs = await waitHeads(page, Math.max(1, turnCount(tid)));
			const k = hs.length;
			if (attempt > 1 || !fresh) {
				await page.evaluate(() => {
					(window as unknown as { __m96?: unknown[] }).__m96 = [];
				});
				await sendUi(page, tid, "从 1 数到 40，每个数字单独一行，不要别的内容");
			}
			const after = await waitHeads(page, attempt > 1 || !fresh ? k + 1 : 1);
			const idx = attempt > 1 || !fresh ? k : 0;
			const final = after[idx];
			if (!final) throw new Error(`没有第 ${idx + 1} 个回合头`);
			const obs = await readObs();
			const during = obs.filter((o) => o.live && o.heads.length === idx + 1);
			note(
				`第 ${attempt} 次观察（回合 #${idx + 1}）：采样 ${obs.length} 次，其中流式中 ${during.length} 次；最终 ${sig(final)}`,
			);
			if (during.length === 0) continue;
			observed = true;
			const seen = [...new Set(during.map((o) => o.heads[idx]))];
			check(
				seen.length === 1 && seen[0] === sig(final),
				`流式期间回合头已是最终身份、之后不变（流式中见过 ${seen.join(" | ")}；最终 ${sig(final)}）`,
			);
		}
		check(observed, "观察到了回合流式期间的回合头（至多 3 次）");
		const all = await heads(page);
		logHeads("回合头", all);
		const h = all[0];
		if (!h) throw new Error("没有回合头");
		check(
			h.source === "startup",
			`首回合 data-turn-source=startup（${h.source}）`,
		);
		check(
			h.identity === want.identity && h.role === want.role,
			`首回合身份 ${sig(h)} = 期望 ${want.identity}/${want.role ?? "-"}`,
		);
		if (want.role)
			check(
				h.avatar === avatars.get(want.role),
				`首回合头像是 ${want.role} 的头像`,
			);
		check(
			h.role === null || h.role === "default" || h.role === first?.alias,
			`data-role 只可能是 default 或记录里的 alias（${h.role ?? "无"}）`,
		);
		await shot(page, `R19-first-turn-${RUN}`);
	}, TALL);
}

/** R13 role 头像接口：顺序、selector、无 models、设置 / 清除 / 不在 cycle 的 400。 */
async function R13() {
	const rs = await ensureRoles();
	const cfg = await ompConfig();
	const list = await roleList();
	const cycle = list.roles.filter((r) => r.inCycle).map((r) => r.role);
	const order = Array.isArray(cfg.cycleOrder)
		? [
				...new Set(
					(cfg.cycleOrder as unknown[])
						.filter((x): x is string => typeof x === "string")
						.map((x) => x.trim())
						.filter(Boolean),
				),
			]
		: [];
	const wantOrder = order.length ? order : ["smol", "default", "slow"];
	note(
		`inCycle：${cycle.join(" / ")}；config.yml cycleOrder：${wantOrder.join(" / ")}`,
	);
	check(j(cycle) === j(wantOrder), "inCycle 项顺序等于 cycleOrder");
	check(
		list.roles.every((r) => !("models" in r)),
		"每项都没有 models 字段",
	);
	for (const r of list.roles.filter((x) => x.inCycle)) {
		const want = rawSelector(cfg, r.role);
		expect(
			(r.selector ?? undefined) === want,
			`${r.role} 的 selector = config.yml 原始值（${r.selector ?? "无"} / ${want ?? "无"}）`,
		);
	}
	const img = pngDataUrl(19, 19, [10, 10, 10]);
	const set = await putRole(rs.A, { avatarImage: img });
	check(set.status === 200, `PUT ${rs.A} 图片头像 → ${set.status}`);
	const after = (await roleList()).roles.find((r) => r.role === rs.A);
	check(after?.avatarImage === img, "列表带回 avatarImage");
	const bad = await putRole(`m96-not-in-cycle-${RUN}`, { avatarIcon: "🦊" });
	check(
		bad.status === 400 && codeOf(bad) === "invalid_request",
		`不在 cycle 里的 role → ${bad.status} ${codeOf(bad)}`,
	);
	const clr = await putRole(rs.A);
	check(clr.status === 200, `清除 ${rs.A} → ${clr.status}`);
	const cleared = (await roleList()).roles.find((r) => r.role === rs.A);
	check(!cleared?.avatarImage && !cleared?.avatarIcon, "清除后头像字段消失");
	const back = await putRole(rs.A, { avatarImage: IMG.A() });
	check(back.status === 200, `还原 ${rs.A} 的验收头像 → ${back.status}`);
}

/** R14 点 role 与手动选同一模型：记录、回合头、无 set_model、无本机记忆。 */
async function R14() {
	const rs = await ensureRoles();
	const tid = await ensureR();
	const list = (await roleList()).roles;
	await withPage(async (page) => {
		const tap = wsTap(page);
		await openThread(page, tid);
		const h0 = await waitHeads(page, turnCount(tid));
		const n0 = records(tid).length;
		const recA = await pickRole(page, tid, rs.A);
		await sendUi(page, tid, "只回复 ok");
		const recM = await pickModel(page, tid, recA.model);
		await sendUi(page, tid, "只回复 ok");
		await pickRole(page, tid, rs.B);
		await sendUi(page, tid, "只回复 ok");
		await pickRole(page, tid, rs.A);
		await sendUi(page, tid, "只回复 ok");
		const recs = records(tid).slice(n0);
		note(`新增记录：${recs.map(recText).join("；")}`);
		check(
			j(recs.map((r) => r.source)) === j(["role", "manual", "role", "role"]),
			"记录 source 序列 = role, manual, role, role",
		);
		check(
			recs[0]?.model === recs[1]?.model && recM.model === recA.model,
			`前两条记录 model 相同（${recs[0]?.model} / ${recs[1]?.model}）`,
		);
		check(
			recs[0]?.role === rs.A &&
				recs[1]?.role === undefined &&
				recs[2]?.role === rs.B &&
				recs[3]?.role === rs.A,
			"记录的 role 依次 A、（manual 无 role）、B、A",
		);
		const hs = await waitHeads(page, h0.length + 4);
		const t = hs.slice(h0.length);
		logHeads("新增回合头", t);
		const avatars = await avatarMap();
		const nameA = displayName(rs.A, list);
		const [t1, t2, t3, t4] = t;
		if (!t1 || !t2 || !t3 || !t4) throw new Error("回合头不足 4 个");
		check(
			t1.identity === "role" && t1.role === rs.A && t1.source === "role",
			`回合 1 = role/${rs.A}/role（${sig(t1)}）`,
		);
		check(
			t1.avatar === avatars.get(rs.A) && t1.name === nameA,
			`回合 1 有 ${rs.A} 的头像与名字「${nameA}」`,
		);
		check(
			t2.identity === "agent" && t2.role === null && t2.source === "manual",
			`回合 2 = agent（无 data-role，source=manual）（${sig(t2)}）`,
		);
		check(
			t2.avatar !== avatars.get(rs.A) && t2.name !== nameA,
			`回合 2 没有 ${rs.A} 的头像元素与名字`,
		);
		check(
			!!t1.model && t2.model === t1.model,
			`回合 2 的模型短名与回合 1 相同（${t1.model} / ${t2.model}）`,
		);
		check(
			t3.identity === "agent" && t3.role === null && t3.source === "role",
			`回合 3 = agent（${rs.B} 没头像，source=role）（${sig(t3)}）`,
		);
		check(
			t4.identity === "role" && t4.role === rs.A,
			`回合 4 = role/${rs.A}（${sig(t4)}）`,
		);
		const setModel = tap.sent.filter(
			(m) => m.type === "cmd" && frameType(m) === "set_model",
		);
		const select = tap.sent.filter(
			(m) => m.type === "cmd" && frameType(m) === "oryn_select_model",
		);
		note(
			`WS：oryn_select_model ${select.length} 次，set_model ${setModel.length} 次`,
		);
		check(select.length >= 4, "切换都走 oryn_select_model");
		check(setModel.length === 0, "整个过程没有发出 set_model");
		const mem = await page.evaluate(
			(k) => localStorage.getItem(k),
			`oryn.role.${tid}`,
		);
		check(mem === null, `localStorage 里没有 oryn.role.${tid}`);
		await shot(page, `R14-${RUN}`);
	}, TALL);
}

/** R20 default role 与 default 的模型。 */
async function R20() {
	const rs = await ensureRoles();
	const tid = await ensureD();
	await withPage(async (page) => {
		await openThread(page, tid);
		const h0 = await waitHeads(page, turnCount(tid));
		const start = records(tid)[0];
		note(`$D 启动记录：${recText(start)}`);
		// 启动记录的 role 就是 default：此时角色列表里 default 已是「当前」，点它不发命令（RoleList 跳过当前项）。
		// 先点 $A（不发消息）离开 default，再点回 default。
		await pickRole(page, tid, rs.A);
		const recDef = await pickRole(page, tid, "default");
		await sendUi(page, tid, "只回复 ok");
		const recMan = await pickModel(page, tid, recDef.model);
		await sendUi(page, tid, "只回复 ok");
		check(
			recDef.source === "role" &&
				recDef.role === "default" &&
				recMan.source === "manual" &&
				recMan.model === recDef.model,
			`两条记录 role(default) 与 manual、model 相同（${recText(recDef)}；${recText(recMan)}）`,
		);
		check(
			recDef.alias === rs.alias,
			`role(default) 记录带别名目标 ${rs.alias ?? "无"}（${recDef.alias ?? "无"}）`,
		);
		const hs = await waitHeads(page, h0.length + 2);
		logHeads("$D 回合头", hs);
		const first = hs[0];
		const a = hs[h0.length];
		const b = hs[h0.length + 1];
		if (!first || !a || !b) throw new Error("回合头不足");
		const want = expectedHead(recDef, "agent", await avatarMap());
		check(
			a.identity === want.identity && a.role === want.role,
			`点 default 的回合 = ${want.identity}/${want.role ?? "-"}（${sig(a)}）`,
		);
		check(
			a.identity === first.identity && a.role === first.role,
			`与新线程首回合（启动记录）同一身份（${sig(first)}）`,
		);
		check(
			b.identity === "agent" && b.role === null && b.source === "manual",
			`手动选 default 的模型的回合 = agent、无 data-role（${sig(b)}）`,
		);
		await shot(page, `R20-${RUN}`);
	}, TALL);
}

/** R21 建线程时显式选模型：只有 manual 记录，回合头回落。 */
async function R21() {
	await ensureAliasAvatar();
	const r = await ensureR();
	const start = records(r)[0];
	if (start?.source !== "startup")
		throw new Error(`$R 没有启动记录（${recText(start)}）`);
	const model = start.model;
	const avatars = await avatarMap();
	const r19 = expectedHead(start, "agent", avatars);
	const r19Identity = `${r19.identity}/${r19.role ?? "-"}`;
	check(
		r19.identity === "role",
		`R19 首回合的身份是 role（${r19Identity}；default 须是指向 cycle role 的别名且该 role 有头像，否则与回落无从区分）`,
	);
	await withPage(async (page) => {
		await page.goto(`${BASE}/`);
		const pill = page.getByRole("button", {
			name: zh.newThread.defaultModel,
			exact: true,
		});
		await pill.click();
		await page.getByLabel(zh.newThread.modelPlaceholder).fill(model);
		await page.keyboard.press("Escape");
		// 胶囊本身（aria-label = 模型）；弹层里「最近用过」的同名行也叫这个名字，所以按 aria-expanded 区分并等弹层关上
		await page
			.locator(`button[aria-expanded="false"][aria-label="${model}"]`)
			.waitFor({ timeout: 5_000 });
		note(
			`新建线程框选了模型 ${model}（default 当前解析到的模型，取自 $R 的启动记录）`,
		);
		await page
			.getByRole("textbox", { name: zh.newThread.prompt })
			.fill("只回复 ok");
		await page
			.getByRole("button", { name: zh.composer.send, exact: true })
			.click();
		const p = await waitPath(page, /^\/t\/T-[^/]+$/, 60_000);
		const tid = p.slice(3);
		createdThreads.add(tid);
		awake.add(tid);
		note(`新线程 ${tid}`);
		await settleFirstTurn(tid);
		const rs = records(tid);
		note(`记录：${rs.map(recText).join("；")}`);
		check(
			rs[0]?.source === "manual" && rs[0].model === model,
			`第一条记录 source=manual、model=${model}`,
		);
		check(!rs.some((x) => x.source === "startup"), "没有 startup 记录");
		check(threadRow(tid)?.model === model, `线程模型 = ${model}`);
		await openThread(page, tid);
		const hs = await waitHeads(page, 1);
		logHeads("回合头", hs);
		const h = hs[0];
		if (!h) throw new Error("没有回合头");
		check(
			h.identity === "agent" && h.role === null && h.source === "manual",
			`回合头 agent（${sig(h)}）`,
		);
		check(
			`${h.identity}/${h.role ?? "-"}` !== r19Identity,
			`不是 R19 首回合的身份（${r19Identity}）`,
		);
		await shot(page, `R21-${RUN}`);
	}, TALL);
}

/** R15 刷新、清 localStorage、全新浏览器上下文：回合头序列不变。 */
async function R15() {
	const tids = [await ensureR(), await ensureD()];
	for (const tid of tids) {
		await withPage(async (page) => {
			await openThread(page, tid);
			const base = await waitHeads(page, turnCount(tid));
			const want = base.map(sig);
			logHeads(`${tid.slice(0, 14)}… 基准`, base);
			await page.reload();
			const a = await waitHeads(page, base.length);
			check(j(a.map(sig)) === j(want), "刷新后回合头序列不变");
			await page.evaluate(() => localStorage.clear());
			await page.reload();
			const b = await waitHeads(page, base.length);
			check(j(b.map(sig)) === j(want), "localStorage.clear() 后刷新，序列不变");
			const ctx = await newContext(true, { viewport: TALL });
			const p2 = await ctx.newPage();
			watch(p2);
			try {
				await openThread(p2, tid);
				const c = await waitHeads(p2, base.length);
				check(j(c.map(sig)) === j(want), "全新浏览器上下文里序列不变");
				const keys = await p2.evaluate(() =>
					Object.keys(localStorage).filter((k) => k.startsWith("oryn.role.")),
				);
				check(
					keys.length === 0,
					`新上下文的 localStorage 没有 oryn.role.* 键（${keys.join(",")}）`,
				);
			} finally {
				await ctx.close();
				extraContexts.delete(ctx);
			}
		}, TALL);
	}
}

/** R22 重试回退（真机只在 M96_FALLBACK_THREAD 给了时核对）。 */
async function R22() {
	const tid = process.env.M96_FALLBACK_THREAD;
	if (!tid)
		throw new Skip(
			"自动化覆盖（turn-role.test.ts、Transcript.test.tsx 的回合 5 / 6）；真机设 M96_FALLBACK_THREAD=<发生过回退的线程> 再核对",
		);
	// 按修订稿 §4 沿主会话条目走一遍轨迹，给每个回合（user 之后的第一条带模型的助手消息）算期望
	const rows = sql<{ seq: number; type: string; p: string }>(
		`select seq, type, cast(payload as text) p from entries where thread_id=${sqlStr(tid)} and stale=0 and instr(file, '/')=0 and type in ('message','model_change','custom') order by seq`,
	);
	const avatars = await avatarMap();
	let rec: SourceRec | undefined;
	let fallback = false;
	let inTurn = false;
	const turns: {
		fallback: boolean;
		want: { identity: string; role: string | null };
	}[] = [];
	for (const r of rows) {
		const p = JSON.parse(r.p) as {
			role?: string;
			model?: string;
			resolvedModelIsFallback?: boolean;
			customType?: string;
			data?: {
				v?: number;
				source?: string;
				model?: string;
				role?: string;
				alias?: string;
			};
			message?: { role?: string; provider?: string; model?: string };
		};
		if (r.type === "custom") {
			if (
				p.customType === "oryn-model-source" &&
				p.data?.v === 1 &&
				p.data.model
			) {
				rec = {
					seq: r.seq,
					source: String(p.data.source),
					model: p.data.model,
					role: p.data.role,
					alias: p.data.alias,
				};
				fallback = false;
			}
		} else if (r.type === "model_change") {
			if (p.role === "fallback" || p.role === "prewalk")
				fallback = p.resolvedModelIsFallback === true;
			else {
				rec = undefined;
				fallback = false;
			}
		} else if (p.message?.role === "user") inTurn = false;
		else if (p.message?.role === "assistant" && p.message.model && !inTurn) {
			inTurn = true;
			const m = `${p.message.provider}/${p.message.model}`.toLowerCase();
			const ok = rec && !fallback && rec.model.toLowerCase() === m;
			turns.push({
				fallback,
				want: ok
					? expectedHead(rec, "agent", avatars)
					: { identity: "agent", role: null },
			});
		}
	}
	check(
		turns.some((t) => t.fallback),
		"该线程里有回退期间的回合",
	);
	await withPage(async (page) => {
		await openThread(page, tid);
		const hs = await waitHeads(page, turns.length);
		logHeads("回合头", hs);
		for (const [i, t] of turns.entries()) {
			const h = hs[i];
			if (!h) {
				expect(false, `回合 ${i + 1} 没有回合头`);
				continue;
			}
			if (t.fallback)
				expect(
					h.role === null && h.identity !== "role",
					`回合 ${i + 1}（回退期间）没有 data-role（${sig(h)}）`,
				);
			else
				expect(
					h.identity === t.want.identity && h.role === t.want.role,
					`回合 ${i + 1} = ${t.want.identity}/${t.want.role ?? "-"}（${sig(h)}）`,
				);
		}
	}, TALL);
}

/** R23 旧 Core orb：先 oryn_select_model 失败（Unknown command）再 set_model；回合一律回落；无来源记录；无控制台错误。 */
async function R23() {
	const rs = await ensureRoles();
	const file = `${ROOT}/.dev/m96old.thread`;
	const old =
		process.env.M96_OLD_THREAD ??
		(existsSync(file) ? readFileSync(file, "utf8").trim() : "");
	check(
		!!old,
		`$OLD = ${old || "（没有 M96_OLD_THREAD，也没有 .dev/m96old.thread）"}`,
	);
	const row = threadRow(old);
	check(
		!!row && row.kind === "work",
		`$OLD 是工作线程（${row?.kind ?? "不存在"}）`,
	);
	note(
		`$OLD 状态 ${row?.state}，orb ${row?.orb_id} ${row?.orb_id ? orbState(row.orb_id) : ""}，模型 ${row?.model}`,
	);
	check(records(old).length === 0, "开始前 $OLD 没有来源记录");
	// 点 $A；$OLD 当前已在 $A 的模型上时（角色列表把它当「当前」、点了不发命令）改点 $C / $B
	const [role, model] = (
		[
			[rs.A, rs.modelA],
			[rs.C, rs.modelC],
			[rs.B, rs.modelB],
		] as const
	).find(([, m]) => m !== row?.model) ?? [rs.A, rs.modelA];
	note(`点 role ${role}（${model}）`);
	awake.add(old);
	try {
		await ensureReady(old);
		consoleErrors = [];
		await withPage(async (page) => {
			const tap = wsTap(page);
			await openThread(page, old);
			const h0 = await waitHeads(page, turnCount(old));
			await clickRole(page, role);
			await waitFor(
				`$OLD 模型换成 ${model}`,
				() => threadRow(old)?.model === model,
				45_000,
			);
			note(`模型已换：${threadRow(old)?.model}`);
			const sel = tap.sent.find(
				(m) => m.type === "cmd" && frameType(m) === "oryn_select_model",
			);
			const setM = tap.sent.find(
				(m) => m.type === "cmd" && frameType(m) === "set_model",
			);
			const resOf = (id: unknown) =>
				tap.received.find(
					(m) => (m.type === "res" || m.type === "error") && m.id === id,
				);
			const selRes = sel ? resOf(sel.id) : undefined;
			const setRes = setM ? resOf(setM.id) : undefined;
			note(
				`oryn_select_model 应答 ${j(selRes).slice(0, 240)}；set_model 应答 ${j(setRes).slice(0, 160)}`,
			);
			const selResp = (selRes?.response ?? {}) as {
				success?: boolean;
				error?: string;
			};
			const selErr = String(selResp.error ?? selRes?.message ?? "");
			check(
				!!sel &&
					selResp.success !== true &&
					selErr.startsWith("Unknown command"),
				"先有一条 oryn_select_model 的失败应答（Unknown command）",
			);
			check(
				!!setM &&
					tap.sent.indexOf(setM) >
						tap.sent.indexOf(sel as Record<string, unknown>) &&
					(setRes?.response as { success?: boolean } | undefined)?.success ===
						true,
				"随后 set_model 成功",
			);
			const trackBox = page
				.getByRole("radiogroup", { name: zh.roles.track })
				.locator("xpath=..");
			const errText = (await trackBox.count())
				? await trackBox.locator(".text-err").count()
				: 0;
			check(errText === 0, "角色轨道下方没有错误文字");
			await sendUi(page, old, "只回复 ok");
			const last = messages(old)
				.filter((m) => m.role === "assistant")
				.at(-1);
			check(
				`${String(last?.raw.provider)}/${String(last?.raw.model)}` === model,
				`新回合跑在 ${model} 上（${String(last?.raw.provider)}/${String(last?.raw.model)}）`,
			);
			const hs = await waitHeads(page, h0.length + 1);
			logHeads("$OLD 回合头", hs);
			check(
				hs.every(
					(h) => h.identity === "agent" && h.role === null && h.source === null,
				),
				"所有回合头回落 agent（无 data-role、无 data-turn-source）",
			);
			await page.reload();
			const again = await waitHeads(page, hs.length);
			check(
				again.every((h) => h.identity === "agent" && h.role === null),
				"刷新后仍全部回落",
			);
			await shot(page, `R23-${RUN}`);
		}, TALL);
		check(records(old).length === 0, "库里 $OLD 没有任何来源记录");
		note(`控制台错误：${consoleErrors.join(" | ") || "无"}`);
		check(consoleErrors.length === 0, "无控制台错误");
	} finally {
		awake.delete(old);
	}
}

/** R24 助手对话：首回合按启动记录（回落为助手身份）；点 A → role/A；手动选模型 → assistant。 */
async function R24() {
	const rs = await ensureRoles();
	const conv = (
		await apiJson<{ thread: { id: string } }>(
			"POST",
			"/api/v1/assistant/conversations",
		)
	).thread.id;
	createdThreads.add(conv);
	note(`助手对话 ${conv}`);
	await withPage(async (page) => {
		await openThread(page, conv, `/assistant/${conv}`);
		await sendUi(page, conv, "只回复 ok");
		const hs1 = await waitHeads(page, 1);
		const rec0 = records(conv)[0];
		note(`首条记录：${recText(rec0)}`);
		check(rec0?.source === "startup", "首条记录 source=startup");
		const want = expectedHead(rec0, "assistant", await avatarMap());
		const h1 = hs1[0];
		if (!h1) throw new Error("没有回合头");
		check(
			h1.identity === want.identity && h1.role === want.role,
			`首回合 = ${want.identity}/${want.role ?? "-"}（${sig(h1)}）`,
		);
		await pickRole(page, conv, rs.A);
		await sendUi(page, conv, "只回复 ok");
		await pickModel(page, conv, rs.modelA);
		await sendUi(page, conv, "只回复 ok");
		const hs = await waitHeads(page, 3);
		logHeads("助手对话回合头", hs);
		const [, h2, h3] = hs;
		check(
			h2?.identity === "role" && h2.role === rs.A,
			`第二个回合 role/${rs.A}（${h2 ? sig(h2) : "无"}）`,
		);
		check(
			h3?.identity === "assistant" && h3.role === null,
			`手动选模型的回合回落为助手身份（${h3 ? sig(h3) : "无"}）`,
		);
		await shot(page, `R24-${RUN}`);
	}, TALL);
}

interface Share {
	id: string;
	slug: string;
	url: string;
	mode: string;
}

/** 分享的匿名面全部文本：identity、entries 全部分页、.md 导出。 */
async function publicText(slug: string) {
	const ident = await anon(`/api/v1/public/shares/${slug}/identity`);
	const pages: string[] = [];
	const changeRoles: string[] = [];
	let after = 0;
	for (let i = 0; i < 100; i++) {
		const r = await anon(
			`/api/v1/public/shares/${slug}/entries?after=${after}&limit=500`,
		);
		if (r.status !== 200) throw new Error(`entries → ${r.status}`);
		pages.push(r.text);
		const pg = r.json as {
			entries: { seq: number; type: string; payload?: { role?: string } }[];
			hasMore: boolean;
		};
		for (const e of pg.entries)
			if (e.type === "model_change" && e.payload?.role)
				changeRoles.push(e.payload.role);
		if (!pg.hasMore || pg.entries.length === 0) break;
		after = pg.entries.at(-1)?.seq ?? after;
	}
	const md = await anon(`/s/${slug}.md`);
	return {
		identity: ident,
		entries: pages.join("\n"),
		md: md.text,
		mdStatus: md.status,
		changeRoles,
	};
}

/** R16 分享页：快照固化、实时跟随、未用到的 role 不出网、撤销后 404。 */
async function R16() {
	const rs = await ensureRoles();
	const tid = await ensureR();
	const list = (await roleList()).roles;
	// 回落身份要有内容才能和站内比（Agent 身份为空时分享页的回落是默认品牌标）
	const name1 = `Oryn-${RUN}`;
	await putIdentity("agent", { name: name1, avatarIcon: "🦊" });
	note(`Agent 身份设为「${name1}」+ 🦊`);
	// 单独跑 R16 时 $R 里还没有 $A / $B 的回合：先补上（站内要同时有 role 回合与回落回合）
	const missing = [rs.A, rs.B].filter(
		(r) => !records(tid).some((x) => x.source === "role" && x.role === r),
	);
	if (missing.length)
		await withPage(async (page) => {
			await openThread(page, tid);
			for (const r of missing) {
				await pickRole(page, tid, r);
				await sendUi(page, tid, "只回复 ok");
			}
		}, TALL);
	check(
		!records(tid).some((x) => x.role === rs.C || x.alias === rs.C),
		`$R 里还没用过 ${rs.C}`,
	);
	const mk = async (mode: string) => {
		const s = await apiJson<Share>("POST", `/api/v1/threads/${tid}/shares`, {
			mode,
			acknowledgeFindings: true,
		});
		createdShares.add(s.id);
		note(`${mode} 分享 ${s.slug}（${s.id}）`);
		return s;
	};
	const snap = await mk("snapshot");
	const live = await mk("live");
	const anonCtx = await newContext(false, { viewport: TALL });
	const settingsReqs: string[] = [];
	anonCtx.on("request", (r) => {
		if (r.url().includes("/api/v1/settings/")) settingsReqs.push(r.url());
	});
	const snapPage = await anonCtx.newPage();
	const livePage = await anonCtx.newPage();
	watch(snapPage);
	watch(livePage);
	try {
		const site = await withPage(async (page) => {
			await openThread(page, tid);
			return waitHeads(page, turnCount(tid));
		}, TALL);
		logHeads("站内", site);
		await snapPage.goto(`${BASE}/s/${snap.slug}`);
		await livePage.goto(`${BASE}/s/${live.slug}`);
		const view = (h: Head) =>
			`${sig(h).replace(/\/[^/]*$/, "")} ${h.name ?? ""} ${h.avatar}`;
		const s1 = await waitHeads(snapPage, site.length);
		const l1 = await waitHeads(livePage, site.length);
		logHeads("快照页", s1);
		logHeads("实时页", l1);
		check(
			j(s1.map(view)) === j(site.map(view)),
			"快照页的回合头与站内一致（身份、role、名字、头像）",
		);
		check(j(l1.map(view)) === j(site.map(view)), "实时页的回合头与站内一致");
		check(
			site.some((h) => h.role === rs.A) &&
				site.some((h) => h.identity === "agent" && h.name === name1),
			`站内同时有 ${rs.A} 的回合与回落回合（回落显示 Agent 名字）`,
		);
		await shot(snapPage, `R16-snapshot-${RUN}`);
		// 改 Agent 名字、换 A 的头像 → 快照不变、实时变
		const name2 = `Oryn2-${RUN}`;
		await putIdentity("agent", { name: name2, avatarIcon: "🐙" });
		const a2 = IMG.A2();
		check(
			(await putRole(rs.A, { avatarImage: a2 })).status === 200,
			`换 ${rs.A} 的头像`,
		);
		await snapPage.reload();
		await livePage.reload();
		const s2 = await waitHeads(snapPage, s1.length);
		const l2 = await waitHeads(livePage, l1.length);
		check(j(s2.map(view)) === j(s1.map(view)), "改设置后快照页不变");
		const l2Agent = l2.filter((h) => h.identity === "agent");
		const l2A = l2.filter((h) => h.role === rs.A);
		check(
			l2Agent.length > 0 &&
				l2A.length > 0 &&
				l2Agent.every((h) => h.name === name2 && h.avatar === "icon:🐙") &&
				l2A.every((h) => h.avatar === `img:${a2}`),
			"改设置后实时页的回落名字 / 头像与 A 的头像都换了",
		);
		// 再点一个此前没用过且有头像的 role（C）发一条
		const sBefore = s2.length;
		await withPage(async (page) => {
			await openThread(page, tid);
			await pickRole(page, tid, rs.C);
			await sendUi(page, tid, "只回复 ok");
		}, TALL);
		const l3 = await waitHeads(livePage, l2.length + 1, 120_000);
		const lastLive = l3.at(-1);
		check(
			lastLive?.identity === "role" &&
				lastLive.role === rs.C &&
				lastLive.avatar === `img:${IMG.C()}`,
			`实时页（不刷新）出现 ${rs.C} 的回合并显示其头像（${lastLive ? headText(lastLive) : "无"}）`,
		);
		await snapPage.reload();
		const s3 = await waitHeads(snapPage, sBefore);
		check(
			s3.length === sBefore,
			`快照页没有那个回合（${s3.length} / ${sBefore}）`,
		);
		await shot(livePage, `R16-live-${RUN}`);
		// 负向：匿名面不含 B、未用到的 role 的 id / 名字 / 头像前缀
		const cycle = list.filter((r) => r.inCycle).map((r) => r.role);
		const now = (await roleList()).roles;
		const avatarPrefix = (role: string) => {
			const r = now.find((x) => x.role === role);
			return r?.avatarImage ? r.avatarImage.slice(0, 64) : undefined;
		};
		const used = {
			snapshot: [rs.alias, rs.A, rs.B].filter((x): x is string => !!x),
			live: [rs.alias, rs.A, rs.B, rs.C].filter((x): x is string => !!x),
		};
		for (const [label, s, u] of [
			["快照", snap, used.snapshot],
			["实时", live, used.live],
		] as const) {
			const pub = await publicText(s.slug);
			check(pub.identity.status === 200, `${label}：identity 200`);
			const blob = `${pub.identity.text}\n${pub.entries}\n${pub.md}`;
			const forbidden: [string, string][] = [[`$B ${rs.B}`, rs.B]];
			for (const r of cycle.filter((x) => !u.includes(x))) {
				forbidden.push([`未用 role id ${r}`, r]);
				const nm = now.find((x) => x.role === r)?.name;
				if (nm) forbidden.push([`未用 role 名 ${nm}`, nm]);
				const pre = avatarPrefix(r);
				if (pre) forbidden.push([`未用 role ${r} 的头像前缀`, pre]);
			}
			const hits = forbidden.filter(([, needle]) => blob.includes(needle));
			note(
				`${label}：检查 ${forbidden.length} 项（${forbidden.map(([w]) => w).join("、")}）`,
			);
			check(
				hits.length === 0,
				`${label}：匿名 identity / entries / .md 不含 ${hits.map(([w]) => w).join("、") || "任何禁止项"}`,
			);
			check(
				pub.changeRoles.every(
					(r) =>
						/^[0-9a-f]{16}$/.test(r) ||
						["default", "fallback", "prewalk", "temporary"].includes(r),
				),
				`${label}：model_change.role 只有不透明键或保留值（${[...new Set(pub.changeRoles)].join(",")}）`,
			);
			const idRoles =
				(
					pub.identity.json as { roles?: { role: string }[] } | null
				)?.roles?.map((r) => r.role) ?? [];
			note(`${label}：identity.roles = ${idRoles.join(" / ") || "无"}`);
			check(
				!idRoles.includes(rs.B),
				`${label}：identity 里没有没头像的 ${rs.B}`,
			);
			check(pub.mdStatus === 200, `${label}：.md 导出 200`);
		}
		check(
			settingsReqs.length === 0,
			`匿名页面没有 /api/v1/settings/ 请求（${settingsReqs.length}）`,
		);
		// 撤销
		for (const s of [snap, live]) {
			const r = await api("DELETE", `/api/v1/shares/${s.id}`);
			check(r.status === 204, `撤销 ${s.mode} → ${r.status}`);
			createdShares.delete(s.id);
			const st = await Promise.all([
				anon(`/api/v1/public/shares/${s.slug}/identity`),
				anon(`/api/v1/public/shares/${s.slug}/entries`),
				anon(`/s/${s.slug}`),
				anon(`/s/${s.slug}.md`),
			]);
			check(
				st.every((x) => x.status === 404),
				`${s.mode}：identity / entries / 页面 / .md 全部 404（${st.map((x) => x.status).join(",")}）`,
			);
		}
		const rows = sql<{ n: number }>(
			`select count(*) n from thread_share_identities where share_id in (${sqlStr(snap.id)}, ${sqlStr(live.id)})`,
		)[0]?.n;
		check(
			rows === 0,
			`thread_share_identities 里没有这两个分享的行（${rows}）`,
		);
	} finally {
		await anonCtx.close();
		extraContexts.delete(anonCtx);
		await putRole(rs.A, { avatarImage: IMG.A() });
	}
}

/** 400×200：左半红、右半蓝。 */
const RED_BLUE = () =>
	pngBytes(400, 200, (x) => (x < 200 ? [230, 30, 30] : [30, 30, 230]));
/** 400×200 渐变：R = x，G = y，B = 128。 */
const gradAt = (x: number, y: number): Rgb => [
	Math.round((x * 255) / 399),
	Math.round((y * 255) / 199),
	128,
];
const GRADIENT = () => pngBytes(400, 200, gradAt);

const near = (a: readonly number[], b: readonly number[], tol: number) =>
	a.every((v, i) => Math.abs(v - (b[i] ?? 0)) <= tol);

/** R25 头像裁剪。 */
async function R25() {
	await withPage(async (page) => {
		const puts: string[] = [];
		page.on("request", (r) => {
			if (r.method() === "PUT" && r.url().includes("/api/v1/settings/"))
				puts.push(r.url().replace(BASE, ""));
		});
		await page.goto(`${BASE}/settings/identity`);
		const agentCard = page.getByTestId("identity-agent");
		await agentCard.waitFor({ timeout: 30_000 });
		const dialog = page
			.getByRole("dialog")
			.filter({ has: page.getByTestId("crop-frame") });
		const frame = page.getByTestId("crop-frame");
		const cropOf = async () =>
			((await frame.getAttribute("data-crop")) ?? "").split(",").map(Number);
		const upload = async (
			scope: Locator,
			name: string,
			buffer: Buffer,
			mimeType = "image/png",
		) => {
			await scope.locator('input[type="file"]').first().setInputFiles({
				name,
				mimeType,
				buffer,
			});
		};
		const use = async (who: string) => {
			const saved = page.waitForResponse(
				(r) =>
					r.request().method() === "PUT" &&
					r.url().includes("/api/v1/settings/"),
				{ timeout: 15_000 },
			);
			await dialog
				.getByRole("button", { name: zh.avatarCrop.use, exact: true })
				.click();
			await dialog.waitFor({ state: "hidden", timeout: 10_000 });
			const r = await saved;
			check(
				r.status() === 200,
				`${who}：PUT ${r.url().replace(BASE, "")} → ${r.status()}`,
			);
		};
		const corners = [
			[2, 2],
			[125, 2],
			[2, 125],
			[125, 125],
		] as const;
		// 1. 拖到最右 → 全蓝
		await upload(agentCard, "rb.png", RED_BLUE());
		await frame.waitFor({ timeout: 10_000 });
		check(
			j(await cropOf()) === j([100, 0, 200]),
			`初始取景框居中最大正方形（${await cropOf()}）`,
		);
		const box = await frame.boundingBox();
		if (!box) throw new Error("取景框没有尺寸");
		await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
		await page.mouse.down();
		await page.mouse.move(box.x + box.width / 2 + 400, box.y + box.height / 2, {
			steps: 10,
		});
		await page.mouse.up();
		check(
			j(await cropOf()) === j([200, 0, 200]),
			`拖到最右（${await cropOf()}）`,
		);
		await use("Agent");
		const img1 = (await getIdentity("agent")).avatarImage ?? "";
		const d1 = await decodeImage(page, img1, corners);
		note(`第一次：${d1.w}×${d1.h}，${img1.length} 字符，四角 ${j(d1.px)}`);
		check(d1.w === 128 && d1.h === 128, "保存为 128×128");
		check(img1.length <= 131072, "整串 ≤ 131072");
		check(/^data:image\/(png|webp|jpeg);base64,/.test(img1), "是 data URL");
		check(
			d1.px.every((p) => (p[2] ?? 0) > 150 && (p[0] ?? 0) < 90),
			"四角为蓝色",
		);
		// 2. 滚轮放大 3 格、方向键左移到头 → 全红
		await upload(agentCard, "rb.png", RED_BLUE());
		await frame.waitFor({ timeout: 10_000 });
		const stage = page.getByTestId("crop-stage");
		const sb = await stage.boundingBox();
		if (!sb) throw new Error("舞台没有尺寸");
		await page.mouse.move(sb.x + sb.width / 2, sb.y + sb.height / 2);
		for (let i = 0; i < 3; i++) {
			await page.mouse.wheel(0, -100);
			await Bun.sleep(80);
		}
		const zoomed = await cropOf();
		check((zoomed[2] ?? 200) < 200, `滚轮 3 格后取景框变小（${zoomed}）`);
		await frame.focus();
		for (let i = 0; i < 40 && (await cropOf())[0] !== 0; i++)
			await page.keyboard.press("Shift+ArrowLeft");
		const left = await cropOf();
		check(left[0] === 0, `方向键左移到头（${left}）`);
		await use("Agent");
		const img2 = (await getIdentity("agent")).avatarImage ?? "";
		const d2 = await decodeImage(page, img2, corners);
		note(`第二次：${d2.w}×${d2.h}，四角 ${j(d2.px)}`);
		check(
			d2.w === 128 && d2.h === 128 && img2.length <= 131072,
			"128×128、≤128KiB",
		);
		check(
			d2.px.every((p) => (p[0] ?? 0) > 150 && (p[2] ?? 0) < 90),
			"全红",
		);
		// 3. 渐变图：拖动 + 缩放 → 预览 = 保存 = 源图对应区域
		await upload(agentCard, "grad.png", GRADIENT());
		await frame.waitFor({ timeout: 10_000 });
		await page.mouse.move(sb.x + sb.width / 2, sb.y + sb.height / 2);
		for (let i = 0; i < 2; i++) {
			await page.mouse.wheel(0, -100);
			await Bun.sleep(80);
		}
		const fb = await frame.boundingBox();
		if (!fb) throw new Error("取景框没有尺寸");
		await page.mouse.move(fb.x + fb.width / 2, fb.y + fb.height / 2);
		await page.mouse.down();
		await page.mouse.move(fb.x + fb.width / 2 + 30, fb.y + fb.height / 2 + 12, {
			steps: 6,
		});
		await page.mouse.up();
		const [cx = 0, cy = 0, cs = 0] = await cropOf();
		note(`渐变图取景框 ${cx},${cy},${cs}`);
		check(cs < 200 && cx > 0 && cy > 0, "拖动与缩放都生效（不在初始位置）");
		const rel = [
			[0.25, 0.25],
			[0.75, 0.25],
			[0.25, 0.75],
			[0.75, 0.75],
			[0.5, 0.5],
		] as const;
		const prevEl = page
			.getByTestId("crop-preview-tile")
			.locator("span")
			.first();
		const pshot = (await prevEl.screenshot()).toString("base64");
		const pv = await page.evaluate(
			async ({ b64, rel }) => {
				const img = new Image();
				img.src = `data:image/png;base64,${b64}`;
				await img.decode();
				const c = document.createElement("canvas");
				c.width = img.naturalWidth;
				c.height = img.naturalHeight;
				const g = c.getContext("2d");
				if (!g) throw new Error("no 2d context");
				g.drawImage(img, 0, 0);
				return rel.map(([u, v]) =>
					Array.from(
						g
							.getImageData(
								Math.floor(u * c.width),
								Math.floor(v * c.height),
								1,
								1,
							)
							.data.slice(0, 3),
					),
				);
			},
			{ b64: pshot, rel },
		);
		await use("Agent");
		const img3 = (await getIdentity("agent")).avatarImage ?? "";
		const d3 = await decodeImage(
			page,
			img3,
			rel.map(([u, v]) => [Math.floor(u * 128), Math.floor(v * 128)] as const),
		);
		const src = rel.map(([u, v]) => gradAt(cx + u * cs, cy + v * cs));
		note(`预览 ${j(pv)}；保存 ${j(d3.px)}；源图 ${j(src)}`);
		check(
			d3.w === 128 && d3.h === 128 && img3.length <= 131072,
			"渐变图保存为 128×128、≤128KiB",
		);
		check(
			d3.px.every((p, i) => near(p, src[i] ?? [], 24)),
			"保存的像素等于源图里取景框对应位置（容差 24）",
		);
		check(
			pv.every((p, i) => near(p, d3.px[i] ?? [], 28)),
			"预览与保存一致（容差 28）",
		);
		// 4. 取消：不发 PUT、身份不变
		await upload(agentCard, "rb.png", RED_BLUE());
		await frame.waitFor({ timeout: 10_000 });
		const n4 = puts.length;
		await dialog
			.getByRole("button", { name: zh.avatarCrop.cancel, exact: true })
			.click();
		await dialog.waitFor({ state: "hidden", timeout: 10_000 });
		await Bun.sleep(1000);
		check(puts.length === n4, "取消后没有 PUT");
		check((await getIdentity("agent")).avatarImage === img3, "取消后身份不变");
		// 5. 文本文件：解码失败、不出现裁剪对话框
		await upload(agentCard, "note.txt", Buffer.from("hello"), "text/plain");
		await agentCard
			.getByText(zh.appearance.avatarDecodeFailed)
			.waitFor({ timeout: 10_000 });
		check(
			(await dialog.count()) === 0,
			"文本文件：显示解码失败，没有裁剪对话框",
		);
		await shot(page, `R25-agent-${RUN}`);
		// 6. 其余三个入口都走同一个对话框
		const rs = await ensureRoles();
		const roleKey = rs.U ?? rs.C;
		const entries: [string, Locator, () => Promise<string>][] = [
			[
				"助手",
				page.getByTestId("identity-assistant"),
				async () => (await getIdentity("assistant")).avatarImage ?? "",
			],
			[
				`role ${roleKey}`,
				page.getByTestId("identity-roles"),
				async () =>
					(await roleList()).roles.find((r) => r.role === roleKey)
						?.avatarImage ?? "",
			],
			[
				"我的头像",
				page.getByTestId("identity-user"),
				async () => (await getIdentity("user")).avatarImage ?? "",
			],
		];
		for (const [who, scope, read] of entries) {
			const before = await read();
			let target = scope;
			if (who.startsWith("role")) {
				target = scope.locator(`li[data-role="${roleKey}"]`);
				await target
					.getByRole("button", { name: zh.identity.setAvatar })
					.click();
			}
			await upload(target, "rb.png", RED_BLUE());
			await frame.waitFor({ timeout: 10_000 });
			await use(who);
			const after = await waitFor(
				`${who} 的图片已保存`,
				async () => {
					const v = await read();
					return v && v !== before ? v : undefined;
				},
				10_000,
			);
			check(
				after.startsWith("data:image/"),
				`${who}：上传 → 裁剪对话框 → 使用 → 已保存`,
			);
		}
	}, DESKTOP);
	// 7. 390 宽：对话框不横向滚动，取景框可用触摸拖动
	const ctx = await newContext(true, {
		viewport: PHONE,
		hasTouch: true,
		isMobile: true,
	});
	try {
		const page = await ctx.newPage();
		watch(page);
		await page.goto(`${BASE}/settings/identity`);
		const card = page.getByTestId("identity-agent");
		await card.waitFor({ timeout: 30_000 });
		await card.locator('input[type="file"]').first().setInputFiles({
			name: "rb.png",
			mimeType: "image/png",
			buffer: RED_BLUE(),
		});
		const frame = page.getByTestId("crop-frame");
		await frame.waitFor({ timeout: 10_000 });
		const sw = await page.evaluate(() => ({
			sw: document.scrollingElement?.scrollWidth ?? 0,
			iw: innerWidth,
		}));
		check(sw.sw <= sw.iw, `390 宽对话框不产生横向滚动（${sw.sw} / ${sw.iw}）`);
		const c0 = await frame.getAttribute("data-crop");
		const b = await frame.boundingBox();
		if (!b) throw new Error("取景框没有尺寸");
		const cdp = await ctx.newCDPSession(page);
		const x = b.x + b.width / 2;
		const y = b.y + b.height / 2;
		await cdp.send("Input.dispatchTouchEvent", {
			type: "touchStart",
			touchPoints: [{ x, y }],
		});
		for (let i = 1; i <= 6; i++)
			await cdp.send("Input.dispatchTouchEvent", {
				type: "touchMove",
				touchPoints: [{ x: x - i * 12, y }],
			});
		await cdp.send("Input.dispatchTouchEvent", {
			type: "touchEnd",
			touchPoints: [],
		});
		const c1 = await frame.getAttribute("data-crop");
		note(`触摸拖动：${c0} → ${c1}`);
		check(c0 !== c1, "取景框可用触摸拖动");
		await shot(page, `R25-phone-${RUN}`);
		await page
			.getByRole("dialog")
			.getByRole("button", { name: zh.avatarCrop.cancel, exact: true })
			.click();
	} finally {
		await ctx.close();
		extraContexts.delete(ctx);
	}
}

// ---------------------------------------------------------------- 场景：分区

/** 各分区底栏标签的期望文字（助手 = 生效名字）。 */
async function expectedLabels(): Promise<string[]> {
	const a = await getIdentity("assistant");
	const g = await getIdentity("agent");
	return [
		zh.tabBar.threads,
		a.name || g.name || zh.tabBar.assistant,
		zh.tabBar.projects,
		zh.tabBar.more,
	];
}

/** 底栏各项的文字（只取文字 span，不含图标位的字形头像）。 */
const tabTexts = async (page: Page) =>
	(
		await tabBar(page)
			.locator("button[data-tab] > span:last-child")
			.allTextContents()
	).map((s) => s.trim());

/** R2 底栏（桌面）。 */
async function R2() {
	await withPage(async (page) => {
		await page.goto(`${BASE}/`);
		await tabBar(page).waitFor({ timeout: 20_000 });
		await waitOpen(page);
		const inSidebar = await page.evaluate(() => {
			const bar = document.querySelector("[data-testid=section-tabbar]");
			const nav = bar?.parentElement?.closest("nav[aria-label]");
			return {
				inNav: !!nav,
				last: nav?.lastElementChild === bar,
				sink: !!bar?.closest("[data-testid=sink-sidebar]"),
			};
		});
		check(
			inSidebar.inNav && inSidebar.last && !inSidebar.sink,
			`底栏是侧栏的最后一个子元素（${j(inSidebar)}）`,
		);
		const texts = await tabTexts(page);
		const want = await expectedLabels();
		check(
			j(texts) === j(want),
			`四项文字依次 ${want.join(" / ")}（${texts.join(" / ")}）`,
		);
		check(
			(await page.getByTestId("sidebar-footer").count()) === 0,
			"没有 sidebar-footer",
		);
		check(
			(await pane(page, "threads")
				.getByText(zh.tabBar.assistant, { exact: true })
				.count()) === 0,
			"线程分区（搜索框下）没有「全局助手」行",
		);
		check((await connOf(page)) === "open", "html[data-conn=open]");
		check(
			(await page.getByTestId("sidebar-connection").count()) === 0 &&
				(await page.getByTestId("tabbar-conn").count()) === 0,
			"已连接时没有 sidebar-connection 与 tabbar-conn",
		);
		await shot(page, `R2-${RUN}`);
	}, DESKTOP);
}

const sectionPaths = (page: Page) =>
	page.evaluate(() => {
		try {
			return JSON.parse(
				sessionStorage.getItem("oryn.sections.paths") ?? "{}",
			) as Record<string, string>;
		} catch {
			return {};
		}
	});

/** R3 分区记忆。 */
async function R3() {
	const tid = await ensureR();
	const moreDefault = MORE_ENTRIES[0]?.to ?? "";
	await withPage(async (page) => {
		await page.goto(`${BASE}/t/${tid}`);
		await tabBar(page).waitFor({ timeout: 20_000 });
		await waitTab(page, "threads");
		await tab(page, "more").click();
		await waitPath(page, moreDefault);
		check(true, `点「更多」→ ${moreDefault}`);
		await page
			.getByTestId("sidebar-more")
			.locator("[data-more-entry=secrets]")
			.click();
		await waitPath(page, "/settings/secrets");
		check(true, "点「密钥」→ /settings/secrets");
		await tab(page, "threads").click();
		await waitPath(page, `/t/${tid}`);
		check(true, `点「线程」→ /t/${tid}`);
		await tab(page, "more").click();
		await waitPath(page, "/settings/secrets");
		check(true, "点「更多」→ /settings/secrets");
		await page.reload();
		await tabBar(page).waitFor({ timeout: 20_000 });
		check(pathOf(page) === "/settings/secrets", "刷新后仍在 /settings/secrets");
		await waitTab(page, "more");
		check(true, "刷新后「更多」选中");
		await tab(page, "threads").click();
		await waitPath(page, `/t/${tid}`);
		check(true, `最后回到 /t/${tid}`);
		const mem = await sectionPaths(page);
		note(`sessionStorage：${j(mem)}`);
		check(
			mem.threads === `/t/${tid}` && mem.more === "/settings/secrets",
			"oryn.sections.paths 含线程与更多两项",
		);
	}, DESKTOP);
}

/** R4 一次切换的逐帧采样结果。 */
interface SlideRun {
	paneW: number;
	x0: number;
	samples: { t: number; x: number }[];
	dur: string;
	plateX: number;
	btnX: number;
	plateLeft: string;
	activeId: string | undefined;
	anim: string;
	animDur: string;
}

/** R4 分区动画（正常 200ms 滑动；减少动态 150ms 淡入）。 */
async function R4() {
	await withPage(async (page) => {
		await page.goto(`${BASE}/`);
		await tabBar(page).waitFor({ timeout: 20_000 });
		await waitTab(page, "threads");
		// 预热：先切一次「更多」再回来（「更多」的页面是懒加载的，首次导航会被 Suspense 推迟，测不准动画起点）
		await tab(page, "more").click();
		await waitTab(page, "more");
		await page.locator("main .max-w-3xl").first().waitFor({ timeout: 15_000 });
		await tab(page, "threads").click();
		await waitTab(page, "threads");
		await Bun.sleep(500);
		const run = (): Promise<SlideRun> =>
			page.evaluate(async () => {
				const track = document.querySelector<HTMLElement>(".section-track");
				const btn = document.querySelector<HTMLElement>(
					"[data-testid=section-tabbar] button[data-tab=more]",
				);
				const plate = document.querySelector<HTMLElement>(
					"[data-testid=section-tabbar-plate]",
				);
				if (!track || !btn || !plate || !track.parentElement)
					throw new Error("缺元素");
				const paneW = track.parentElement.getBoundingClientRect().width;
				const x0 = track.getBoundingClientRect().x;
				const dur = getComputedStyle(track).transitionDuration;
				// 逐帧采样 500ms：点击后渲染路由的耗时随机器负载变化，固定时刻采一个点测不准动画是否发生
				const frame = () =>
					new Promise<void>((r) => requestAnimationFrame(() => r()));
				btn.click();
				const t0 = performance.now();
				const samples: { t: number; x: number }[] = [];
				while (performance.now() - t0 < 500) {
					await frame();
					samples.push({
						t: performance.now() - t0,
						x: track.getBoundingClientRect().x,
					});
				}
				const active = document.querySelector<HTMLElement>(
					"[data-testid=section-pane][data-active]",
				);
				const cs = active ? getComputedStyle(active) : null;
				return {
					paneW,
					x0,
					samples,
					dur,
					plateX: plate.getBoundingClientRect().x,
					btnX: btn.getBoundingClientRect().x,
					plateLeft: plate.style.left,
					activeId: active?.dataset.section,
					anim: cs?.animationName ?? "",
					animDur: cs?.animationDuration ?? "",
				};
			});
		/** 从采样里算：起动时刻（第一次离开起点）、到位时刻、中间帧数。 */
		const motion = (a: SlideRun) => {
			const end = a.x0 - 3 * a.paneW;
			const start = a.samples.find((s) => Math.abs(s.x - a.x0) > 1);
			const settled = a.samples.find((s) => Math.abs(s.x - end) <= 1);
			const mid = a.samples.filter(
				(s) => s.x < a.x0 - 2 && s.x > end + 2,
			).length;
			const last = a.samples.at(-1)?.x ?? a.x0;
			return { end, start: start?.t, settled: settled?.t, mid, last };
		};
		const a = await run();
		const ma = motion(a);
		note(
			`正常：${j({ ...a, samples: a.samples.map((s) => `${s.t.toFixed(0)}:${s.x.toFixed(0)}`).join(" ") })}`,
		);
		const end = ma.end;
		check(
			a.dur === "0.2s",
			`.section-track transition-duration = 0.2s（${a.dur}）`,
		);
		check(
			ma.mid >= 2,
			`轨道滑动而不是跳变：起止之间有 ${ma.mid} 帧（${a.x0.toFixed(1)} → ${end.toFixed(1)}）`,
		);
		check(
			ma.start !== undefined &&
				ma.settled !== undefined &&
				ma.settled - ma.start >= 120 &&
				ma.settled - ma.start <= 320,
			`滑动耗时 ≈ 0.2s（起动 ${ma.start?.toFixed(0)}ms，到位 ${ma.settled?.toFixed(0)}ms）`,
		);
		check(
			Math.abs(ma.last - end) <= 1,
			`最终到位（${ma.last.toFixed(1)} ≈ ${end.toFixed(1)}）`,
		);
		note(
			`起动延迟 ${ma.start?.toFixed(0)}ms（点击到第一帧移动：渲染路由的耗时）`,
		);
		check(
			a.plateLeft === "75%" && Math.abs(a.plateX - a.btnX) <= 2,
			`底板同步到第四格（left ${a.plateLeft}，x ${a.plateX.toFixed(1)} ≈ ${a.btnX.toFixed(1)}）`,
		);
		check(a.activeId === "more", "当前 pane 为「更多」");
		await tab(page, "threads").click();
		await waitTab(page, "threads");
		await Bun.sleep(400);
		await page.emulateMedia({ reducedMotion: "reduce" });
		try {
			const b = await run();
			const mb = motion(b);
			note(
				`减少动态：${j({ ...b, samples: b.samples.map((s) => `${s.t.toFixed(0)}:${s.x.toFixed(0)}`).join(" ") })}`,
			);
			check(b.dur === "0s", `减少动态：transition-duration = 0s（${b.dur}）`);
			check(
				mb.mid === 0 && Math.abs(mb.last - mb.end) <= 1,
				`减少动态：没有中间帧、直接到位（中间 ${mb.mid} 帧，最终 ${mb.last.toFixed(1)} ≈ ${mb.end.toFixed(1)}）`,
			);
			check(
				b.anim === "section-fade" && b.animDur === "0.15s",
				`减少动态：当前 pane 淡入 section-fade 0.15s（${b.anim} ${b.animDur}）`,
			);
		} finally {
			await page.emulateMedia({ reducedMotion: "no-preference" });
		}
	}, DESKTOP);
}

/** R5 切走再回来：滚动位置与分组收起状态保留，四个 pane 始终在 DOM。 */
async function R5() {
	const projects = (
		await apiJson<{ projects: { name: string }[] }>("GET", "/api/v1/projects")
	).projects.map((p) => p.name);
	await withPage(async (page) => {
		await page.goto(`${BASE}/`);
		await tabBar(page).waitFor({ timeout: 20_000 });
		const scroller = page.getByTestId("threads-scroll");
		await scroller.waitFor({ timeout: 20_000 });
		// 等索引出来：至少有一个分组标题
		await scroller
			.locator("button[aria-expanded]")
			.first()
			.waitFor({ timeout: 30_000 });
		const picked = await page.evaluate(
			({ names, run }) => {
				const sc = document.querySelector("[data-testid=threads-scroll]");
				const btns = [
					...(sc?.querySelectorAll<HTMLButtonElement>(
						'button[aria-expanded="true"]',
					) ?? []),
				];
				const b = btns.find((x) =>
					names.includes(
						(x.querySelector("span.truncate")?.textContent ?? "").trim(),
					),
				);
				if (!b) return null;
				b.dataset.m96 = run;
				b.click();
				return (b.textContent ?? "").trim();
			},
			{ names: [...projects, zh.sidebar.noProject], run: RUN },
		);
		check(!!picked, `收起一个项目分组（${picked ?? "没找到展开的项目分组"}）`);
		const room = await scroller.evaluate(
			(el) => el.scrollHeight - el.clientHeight,
		);
		check(room >= 200, `线程分区可滚动 ≥200px（${room}）`);
		await scroller.evaluate((el) => {
			el.scrollTop = 200;
			(el as HTMLElement).dataset.m96 = "scroll";
		});
		await tab(page, "projects").click();
		await waitTab(page, "projects");
		const all = await page.evaluate(() =>
			[...document.querySelectorAll("[data-testid=section-pane]")].map((p) => ({
				id: p.getAttribute("data-section"),
				inert: p.hasAttribute("inert"),
				active: p.hasAttribute("data-active"),
			})),
		);
		note(`pane：${j(all)}`);
		check(all.length === 4, "四个 section-pane 都在 DOM 里");
		check(
			all.every((p) => p.inert === !p.active),
			"非当前的 pane 带 inert",
		);
		await tab(page, "threads").click();
		await waitTab(page, "threads");
		const st = await page.evaluate((run) => {
			const sc = document.querySelector<HTMLElement>(
				"[data-testid=threads-scroll]",
			);
			const b = document.querySelector<HTMLElement>(
				`button[data-m96="${run}"]`,
			);
			return {
				same: sc?.dataset.m96 === "scroll",
				top: sc?.scrollTop ?? -1,
				expanded: b?.getAttribute("aria-expanded"),
			};
		}, RUN);
		note(`切回后：${j(st)}`);
		check(st.same, "同一个滚动容器 DOM 节点");
		check(st.top === 200, `scrollTop 仍为 200（${st.top}）`);
		check(st.expanded === "false", "该分组仍收起");
	}, DESKTOP);
}

/** R6 收起侧栏后的快捷键。 */
async function R6() {
	const tid = await ensureR();
	const conv = await ensureConversation();
	const p = await ensureEditProject();
	await withPage(async (page) => {
		// 先在四个分区各留一条记忆
		for (const path of [
			`/assistant/${conv}`,
			`/projects/${p.id}`,
			"/settings/secrets",
			`/t/${tid}`,
		]) {
			await page.goto(`${BASE}${path}`);
			await tabBar(page).waitFor({ timeout: 20_000 });
			await waitPath(page, path);
			await Bun.sleep(300);
		}
		const mem = await sectionPaths(page);
		note(`记忆：${j(mem)}`);
		await page.evaluate(() => {
			const w = window as unknown as {
				__m96keys?: { code: string; prevented: boolean }[];
			};
			w.__m96keys = [];
			// 诊断用：记下路由的每次 history 调用
			const hw = window as unknown as { __m96hist?: string[] };
			hw.__m96hist = [];
			for (const name of ["pushState", "replaceState"] as const) {
				const orig = history[name].bind(history);
				history[name] = (...a: Parameters<History["pushState"]>) => {
					hw.__m96hist?.push(`${name} ${String(a[2])}`);
					return orig(...a);
				};
			}
			window.addEventListener(
				"keydown",
				(e) => {
					if (!e.ctrlKey) return;
					setTimeout(
						() =>
							w.__m96keys?.push({
								code: e.code,
								prevented: e.defaultPrevented,
							}),
						0,
					);
				},
				true,
			);
		});
		const pages0 = page.context().pages().length;
		// 诊断：装好按键记录之后文档是否被整页替换（替换后记录器随旧 window 一起丢失）
		let loads = 0;
		page.on("load", () => loads++);
		// 焦点不留在可编辑区域（Ctrl+B 在编辑区内不抢）
		await page.evaluate(() =>
			(document.activeElement as HTMLElement | null)?.blur(),
		);
		await page.keyboard.press("Control+b");
		const sidebarState = () =>
			page.locator("[data-sidebar]").first().getAttribute("data-sidebar");
		try {
			await waitFor(
				"侧栏收起",
				async () => (await sidebarState()) === "collapsed",
				5_000,
				100,
			);
			check(true, "Ctrl+B 收起侧栏");
			// 收起有滑出动画：等底栏完全移出屏外（data-sidebar 在动画开始时就已是 collapsed）
			const barHidden = await waitFor(
				"底栏移出屏外",
				() =>
					page.evaluate(() => {
						const bar = document.querySelector("[data-testid=section-tabbar]");
						const r = bar?.getBoundingClientRect();
						return !!bar?.closest("[inert]") && !!r && r.right <= 1;
					}),
				2_000,
				50,
			).catch(() => false);
			check(barHidden, "页面上没有图标栏（底栏随侧栏移出屏外且 inert）");
			const steps: [string, string | undefined][] = [
				["Control+2", mem.assistant],
				["Control+3", mem.projects],
				["Control+4", mem.more],
				["Control+1", mem.threads],
				["Control+/", mem.assistant],
			];
			for (const [key, want] of steps) {
				await page.keyboard.press(key);
				const got = await waitPath(page, want ?? "/__none__", 10_000).catch(
					async (err: unknown) => {
						// 诊断：这一步之前的 history 调用与按键记录（是否被页面拦下）
						const diag = await page.evaluate(() => {
							const w = window as unknown as {
								__m96keys?: { code: string; prevented: boolean }[];
								__m96hist?: string[];
							};
							return {
								keys: w.__m96keys ?? [],
								hist: w.__m96hist ?? [],
								active: document.activeElement?.outerHTML.slice(0, 120),
							};
						});
						note(`诊断 ${key}：${j(diag)}`);
						throw err;
					},
				);
				check(got === want, `${key} → ${got}`);
				check((await sidebarState()) === "collapsed", `${key} 后侧栏仍收起`);
			}
			await page.keyboard.press("Control+1");
			await waitPath(page, mem.threads ?? "/");
			const box = composer(page);
			await box.waitFor({ timeout: 30_000 });
			await box.focus();
			await page.keyboard.press("Control+4");
			const got = await waitPath(page, mem.more ?? "/__none__", 10_000);
			check(got === mem.more, `焦点在输入框里按 Ctrl+4 也生效（${got}）`);
			const keys = await page.evaluate(
				() =>
					(
						window as unknown as {
							__m96keys?: { code: string; prevented: boolean }[];
						}
					).__m96keys ?? [],
			);
			note(`按键：${j(keys)}；记录器装好后整页加载 ${loads} 次`);
			const digits = keys.filter((k) => /^Digit[1-4]$|^Slash$/.test(k.code));
			check(
				digits.length >= 6 && digits.every((k) => k.prevented),
				"Ctrl+数字 / Ctrl+/ 都被页面拦下（defaultPrevented）",
			);
			const vis = await page.evaluate(() => document.visibilityState);
			check(
				page.context().pages().length === pages0 && vis === "visible",
				"浏览器没有切换标签页",
			);
		} finally {
			// 收起状态会持久化：失败时也要展开回来，否则后面的场景点底栏都撞上 inert
			if ((await sidebarState()) === "collapsed") {
				await page.evaluate(() =>
					(document.activeElement as HTMLElement | null)?.blur(),
				);
				await page.keyboard.press("Control+b");
				await waitFor(
					"侧栏展开",
					async () => (await sidebarState()) !== "collapsed",
					5_000,
					100,
				);
			}
		}
	}, DESKTOP);
}

let convCache = "";
/** 一个未归档的助手对话（本次新建）。 */
async function ensureConversation(): Promise<string> {
	if (convCache && !threadRow(convCache)?.archived_at) return convCache;
	const r = await api("POST", "/api/v1/assistant/conversations");
	if (r.status !== 201 && r.status !== 200)
		throw new Error(`新建助手对话 → ${r.status} ${r.text.slice(0, 200)}`);
	convCache = (r.json as { thread: { id: string } }).thread.id;
	createdThreads.add(convCache);
	note(`助手对话 ${convCache}`);
	return convCache;
}

/** R7 助手分区。 */
async function R7() {
	const c1 = await ensureConversation();
	await withPage(async (page) => {
		await page.goto(`${BASE}/assistant/${c1}`);
		await tabBar(page).waitFor({ timeout: 20_000 });
		await waitPath(page, `/assistant/${c1}`);
		await Bun.sleep(300);
		await page.goto(`${BASE}/settings/about`);
		await tabBar(page).waitFor({ timeout: 20_000 });
		await tab(page, "assistant").click();
		await waitPath(page, `/assistant/${c1}`);
		check(true, `点「助手」→ 上次的对话 /assistant/${c1}`);
		const list = pane(page, "assistant").getByTestId("sidebar-assistant");
		await list.waitFor({ timeout: 10_000 });
		// URL 在 pushState 时就变了，pane 的 data-active 要等路由 transition 提交：等它而不是立刻断言
		const active = await waitFor(
			"助手 pane 成为当前",
			() =>
				pane(page, "assistant").evaluate((el) =>
					el.hasAttribute("data-active"),
				),
			5_000,
			100,
		).catch(() => false);
		check(active, "侧栏是助手会话列表");
		const rows = () => list.locator(":scope > div.mt-1 a[href^='/assistant/']");
		await waitFor(
			"列表含 c1",
			async () =>
				(await list.locator(`a[href="/assistant/${c1}"]`).count()) > 0,
			15_000,
		);
		const n0 = await rows().count();
		await list
			.getByRole("button", { name: zh.assistant.new, exact: true })
			.click();
		const waitNew = await waitFor(
			"跳到新对话",
			() => {
				const p = pathOf(page);
				return p !== `/assistant/${c1}` && /^\/assistant\/T-[^/]+$/.test(p)
					? p
					: undefined;
			},
			20_000,
		);
		const c2 = waitNew.slice("/assistant/".length);
		createdThreads.add(c2);
		await waitFor(
			"列表多一行",
			async () => ((await rows().count()) === n0 + 1 ? true : undefined),
			15_000,
		);
		check(true, `新建后路径是新对话 ${c2}，列表 ${n0} → ${n0 + 1} 行`);
		await list.locator(`a[href="/assistant/${c1}"]`).click();
		await waitPath(page, `/assistant/${c1}`);
		check(true, "点另一行即切换");
		const row = list.locator(`a[href="/assistant/${c1}"]`).locator("xpath=..");
		await row.hover();
		await row.locator("button").first().click();
		await waitFor(
			"离开被归档的对话",
			() => (pathOf(page) !== `/assistant/${c1}` ? true : undefined),
			15_000,
		);
		const landed = await waitPath(page, /^\/assistant(\/T-[^/]+)?$/, 15_000);
		check(
			landed !== `/assistant/${c1}`,
			`归档后回到 /assistant 的落点（${landed}）`,
		);
		await waitFor(
			"该行从列表消失",
			async () =>
				(await list
					.locator(`:scope > div.mt-1 a[href="/assistant/${c1}"]`)
					.count()) === 0
					? true
					: undefined,
			15_000,
		);
		check(true, "该行从列表消失");
		await list.getByRole("button", { name: zh.sidebar.archived }).click();
		await list
			.locator(`a[href="/assistant/${c1}"]`)
			.waitFor({ timeout: 15_000 });
		check(true, "出现在「已归档」里");
		convCache = "";
		check(
			(await page
				.getByRole("button", { name: zh.assistant.conversations, exact: true })
				.count()) === 0,
			"页头没有「对话」菜单",
		);
		check(
			(await page
				.getByRole("button", { name: zh.assistant.actions, exact: true })
				.count()) === 1,
			"页头有「对话操作」",
		);
		await shot(page, `R7-${RUN}`);
	}, DESKTOP);
}

/** R8 项目分区。 */
async function R8() {
	const p = await ensureEditProject();
	await withPage(async (page) => {
		await page.goto(`${BASE}/`);
		await tabBar(page).waitFor({ timeout: 20_000 });
		await tab(page, "projects").click();
		await waitPath(page, /^\/projects(\/|$)/);
		const sp = page.getByTestId("sidebar-projects");
		await sp.locator(`a[href="/projects/${p.id}"]`).click();
		await waitPath(page, `/projects/${p.id}`);
		const main = page.locator("main");
		for (const title of [
			zh.projectsPage.settings,
			zh.projectsPage.environment,
			zh.projectsPage.services,
			zh.projects.schedules,
		])
			await main
				.getByText(title, { exact: true })
				.first()
				.waitFor({ timeout: 15_000 });
		check(
			true,
			`主区是 /projects/${p.id}，四张卡（${zh.projectsPage.settings} / ${zh.projectsPage.environment} / ${zh.projectsPage.services} / ${zh.projects.schedules}）`,
		);
		const script = `echo m96-${RUN}`;
		await main.getByLabel(zh.projects.setupScript).fill(script);
		await main
			.getByRole("button", { name: zh.common.save, exact: true })
			.first()
			.click();
		const saved = await waitFor(
			"setupScript 已保存",
			async () => {
				const g = await apiJson<{ setupScript?: string }>(
					"GET",
					`/api/v1/projects/${p.id}`,
				);
				return g.setupScript === script ? g.setupScript : undefined;
			},
			15_000,
		);
		check(
			saved === script,
			`GET projects/$P 的 setupScript = 新值（${saved}）`,
		);
		await main
			.getByRole("button", { name: zh.projectsPage.viewThreads })
			.first()
			.click();
		await waitTab(page, "threads");
		const chip = page.getByTestId("project-filter");
		await chip.waitFor({ timeout: 10_000 });
		check(
			((await chip.textContent()) ?? "").includes(p.name),
			"「查看线程」后回到线程分区且出现 $P 的过滤签",
		);
		await chip
			.getByRole("button")
			.first()
			.click()
			.catch(() => {});
		await page.goto(`${BASE}/settings/projects`);
		const landed = await waitPath(page, /^\/projects(\/|$)/, 15_000);
		check(true, `/settings/projects 落到 ${landed}`);
		await shot(page, `R8-${RUN}`);
	}, DESKTOP);
}

/** R9 更多分区与断线。 */
async function R9() {
	await withPage(async (page) => {
		await page.goto(`${BASE}/`);
		await tabBar(page).waitFor({ timeout: 20_000 });
		await waitOpen(page);
		await tab(page, "more").click();
		await waitTab(page, "more");
		const more = page.getByTestId("sidebar-more");
		const shown = await more
			.locator("[data-more-entry]")
			.evaluateAll((els) => els.map((e) => e.getAttribute("data-more-entry")));
		const want = MORE_ENTRIES.filter((e) => !e.desktopOnly);
		check(
			j(shown) === j(want.map((e) => e.key)),
			`条目与 MORE_ENTRIES 一致、没有桌面专属项（${shown.join(",")}）`,
		);
		for (const e of want) {
			await more.locator(`[data-more-entry="${e.key}"]`).click();
			await waitPath(page, e.to);
			const main = page.locator("main");
			const navs = await main.locator("nav").count();
			const wide = await main.locator(".max-w-3xl").count();
			expect(
				navs === 0 && wide > 0,
				`${e.key} → ${e.to}：主区没有内导航（${navs}），内容 max-w-3xl（${wide}）`,
			);
		}
		await page.context().setOffline(true);
		try {
			await waitFor(
				"断线标记",
				async () =>
					(await tabBar(page).getByTestId("tabbar-conn").count()) > 0 &&
					(await more.getByTestId("sidebar-connection").count()) > 0,
				30_000,
				250,
			);
			note(`断线：data-conn=${await connOf(page)}`);
			check(
				true,
				"断线后「更多」标签出现 tabbar-conn、分区第一行出现 sidebar-connection",
			);
			const firstRow = await more.evaluate((el) =>
				el.firstElementChild?.getAttribute("data-testid"),
			);
			check(
				firstRow === "sidebar-connection",
				`连接行是「更多」分区第一行（${firstRow}）`,
			);
			await shot(page, `R9-offline-${RUN}`);
		} finally {
			await page.context().setOffline(false);
		}
		await waitFor(
			"恢复后两者消失",
			async () =>
				(await page.getByTestId("tabbar-conn").count()) === 0 &&
				(await page.getByTestId("sidebar-connection").count()) === 0,
			60_000,
			500,
		);
		check(true, "恢复后两者消失");
	}, DESKTOP);
}

/** 下沉侧栏的底栏是否露出（中心点没被卡片盖住、不在 inert 里）。 */
const sinkBarShown = (page: Page) =>
	page.evaluate(() => {
		const b = document.querySelector(
			"[data-testid=sink-sidebar] [data-testid=section-tabbar]",
		);
		if (!b || b.closest("[inert]")) return false;
		const r = b.getBoundingClientRect();
		const el = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
		return !!el && b.contains(el);
	});

/** R10 手机 390 / 360。 */
async function R10() {
	await ensureConversation();
	for (const vp of [PHONE, PHONE_S]) {
		await withPage(async (page) => {
			const label = `${vp.width}`;
			await page.goto(`${BASE}/`);
			const open = async () => {
				await page
					.getByRole("button", { name: zh.nav.sidebar, exact: true })
					.click();
				await waitFor("底栏露出", () => sinkBarShown(page), 10_000, 150);
			};
			await open();
			check(
				(await page
					.locator("[data-testid=sink-sidebar] [data-testid=section-tabbar]")
					.count()) === 1,
				`${label}：底栏在 sink-sidebar 里`,
			);
			await tab(page, "assistant").click();
			await waitPath(page, /^\/assistant(\/T-[^/]+)?$/);
			await waitPath(page, /^\/assistant\/T-[^/]+$/, 20_000);
			check(await sinkBarShown(page), `${label}：点「助手」后侧栏仍打开`);
			check(
				await pane(page, "assistant").evaluate(
					(el) =>
						el.hasAttribute("data-active") &&
						!!el.querySelector("[data-testid=sidebar-assistant]"),
				),
				`${label}：侧栏是会话列表，卡片下方路径已是助手对话（${pathOf(page)}）`,
			);
			await shot(page, `R10-${label}-assistant-${RUN}`);
			await tab(page, "assistant").click();
			await waitFor(
				"再点一次收起",
				async () => !(await sinkBarShown(page)),
				5_000,
				150,
			);
			check(true, `${label}：再点一次「助手」侧栏收起`);
			await open();
			await tab(page, "more").click();
			await waitTab(page, "more");
			await page
				.getByTestId("sidebar-more")
				.locator("[data-more-entry=about]")
				.click();
			await waitPath(page, "/settings/about");
			await waitFor(
				"点「关于」后侧栏收起",
				async () => !(await sinkBarShown(page)),
				5_000,
				150,
			);
			check(true, `${label}：点「关于」后侧栏收起，路径 /settings/about`);
			await open();
			await page.goBack();
			await waitFor(
				"返回键先收侧栏",
				async () => !(await sinkBarShown(page)),
				5_000,
				150,
			);
			check(
				pathOf(page) === "/settings/about",
				`${label}：返回键先收侧栏、路径不变（${pathOf(page)}）`,
			);
			const sw = await page.evaluate(() => ({
				sw: document.scrollingElement?.scrollWidth ?? 0,
				iw: innerWidth,
			}));
			check(
				sw.sw <= sw.iw,
				`${label}：scrollWidth ≤ innerWidth（${sw.sw} / ${sw.iw}）`,
			);
		}, vp);
	}
}

/** R11 深链接与通知 toast。 */
async function R11() {
	const tid = await ensureR();
	const conv = await ensureConversation();
	await withPage(async (page) => {
		await page.goto(`${BASE}/t/${tid}`);
		await tabBar(page).waitFor({ timeout: 20_000 });
		await waitTab(page, "threads");
		await Bun.sleep(300);
		await page.goto(`${BASE}/settings/automations`);
		await tabBar(page).waitFor({ timeout: 20_000 });
		await waitTab(page, "more");
		check(true, "/settings/automations：「更多」选中");
		await page.goto(`${BASE}/t/${conv}`);
		await waitPath(page, `/assistant/${conv}`, 20_000);
		await waitTab(page, "assistant");
		check(true, `/t/<助手对话> 重定向到 /assistant/${conv}，「助手」选中`);
		await Bun.sleep(300);
		const mem = await sectionPaths(page);
		check(
			mem.threads !== `/t/${conv}`,
			`线程分区的记忆没有被助手对话污染（${mem.threads}）`,
		);
		await tab(page, "threads").click();
		const back = await waitPath(page, /^\/(t\/T-[^/]+)?$/);
		check(back === `/t/${tid}`, `点「线程」回到之前的线程路径（${back}）`);
		// 通知 toast：页面停在「更多」，$R 的回合在无人 focus 时结束
		await page.goto(`${BASE}/settings/automations`);
		await tabBar(page).waitFor({ timeout: 20_000 });
		await waitOpen(page);
		awake.delete(tid);
		try {
			await ensureReady(tid);
			const before = lastSeq(tid);
			const title = threadRow(tid)?.title ?? "";
			const region = page.getByRole("region", {
				name: zh.notify.toastRegion,
			});
			const toast = region
				.getByRole("status")
				.filter({ hasText: title })
				.first();
			// toast 6 秒后自动消失：先挂上等待，再发消息
			const shown = toast.waitFor({ timeout: 300_000 }).then(
				() => undefined,
				(e: unknown) => e,
			);
			const r = await cli(`thread send ${tid} ${q("只回复 ok")}`);
			check(r.code === 0, `oryn thread send → ${r.code}`);
			await waitTurn(tid, before);
			const err = await shown;
			if (err) throw err;
			note(`toast：${((await toast.textContent()) ?? "").slice(0, 120)}`);
			await toast.locator("button").first().click();
			await waitPath(page, `/t/${tid}`);
			await waitTab(page, "threads");
			check(true, `点 toast → /t/${tid}，「线程」选中`);
		} finally {
			awake.add(tid);
		}
	}, DESKTOP);
}

/** 各分区侧栏顶部没有写着分区名的标题。 */
async function noPaneTitles(page: Page) {
	const names = [
		zh.tabBar.threads,
		zh.tabBar.assistant,
		zh.tabBar.projects,
		zh.tabBar.more,
		"助手",
	];
	const tops = await page.evaluate(() =>
		[
			...document.querySelectorAll<HTMLElement>("[data-testid=section-pane]"),
		].map((p) => ({
			id: p.dataset.section,
			first: (() => {
				// 第一个非空文本节点（侧栏顶部第一行的文字）
				const w = document.createTreeWalker(p, NodeFilter.SHOW_TEXT);
				for (let n = w.nextNode(); n; n = w.nextNode()) {
					const s = (n.textContent ?? "").trim();
					if (s) return s;
				}
				return "";
			})(),
			headings: [...p.querySelectorAll("h1,h2,h3")].map((h) =>
				(h.textContent ?? "").trim(),
			),
		})),
	);
	note(`各分区第一行：${tops.map((t) => `${t.id}「${t.first}」`).join(" ")}`);
	return tops.every(
		(t) =>
			!names.includes(t.first) && !t.headings.some((h) => names.includes(h)),
	);
}

/** R12 助手身份：底栏标签与助手页头。 */
async function R12() {
	const conv = await ensureConversation();
	const img = IMG.assistant();
	await withPage(async (page) => {
		const assistTab = () => tab(page, "assistant");
		const avatar = () => assistTab().locator("[data-avatar-of=assistant]");
		const header = () =>
			page
				.getByTestId("assistant-page")
				.locator("[data-avatar-of=assistant][data-avatar-shape=circle]")
				.first();
		const round = (loc: Locator) =>
			loc.evaluate((el) => {
				const r = el.getBoundingClientRect();
				return (
					Number.parseFloat(getComputedStyle(el).borderTopLeftRadius) >=
					r.width / 2 - 0.5
				);
			});
		const load = async () => {
			await page.goto(`${BASE}/assistant/${conv}`);
			await tabBar(page).waitFor({ timeout: 20_000 });
			await page.getByTestId("assistant-page").waitFor({ timeout: 20_000 });
		};
		await putIdentity("agent", { name: "Oryn", avatarIcon: "🦊" });
		await putIdentity("assistant", {});
		await load();
		const label = async () =>
			(
				(await assistTab().locator(":scope > span").last().textContent()) ?? ""
			).trim();
		await waitFor(
			"标签文字 Oryn",
			async () => ((await label()) === "Oryn" ? true : undefined),
			10_000,
		);
		check(true, "第一步：底栏助手标签文字「Oryn」");
		check(
			(await avatar().getAttribute("data-avatar")) === "icon" &&
				(await avatar().textContent()) === "🦊" &&
				(await avatar().getAttribute("data-avatar-shape")) === "circle" &&
				(await round(avatar())),
			"图标位是 🦊 的圆形方块（border-radius 为正圆）",
		);
		check(
			(await header().textContent()) === "🦊" && (await round(header())),
			"助手页头是 🦊 圆形头像",
		);
		const first = {
			text: await label(),
			av: await avatar().textContent(),
		};
		await shot(page, `R12-agent-${RUN}`);
		await putIdentity("assistant", { name: "小助手", avatarImage: img });
		await load();
		await waitFor(
			"标签文字 小助手",
			async () => ((await label()) === "小助手" ? true : undefined),
			10_000,
		);
		check(
			(await avatar().getAttribute("data-avatar")) === "image" &&
				(await avatar().getAttribute("src")) === img &&
				(await round(avatar())),
			"第二步：文字「小助手」、图标位是圆形图片",
		);
		check((await header().getAttribute("src")) === img, "助手页头同步为该图片");
		await shot(page, `R12-assistant-${RUN}`);
		await putIdentity("assistant", {});
		await load();
		await waitFor(
			"回到第一步",
			async () => ((await label()) === first.text ? true : undefined),
			10_000,
		);
		check(
			(await avatar().textContent()) === first.av &&
				(await header().textContent()) === "🦊",
			"第三步：回到第一步的样子（标签与页头）",
		);
		check(await noPaneTitles(page), "四个分区侧栏顶部都没有分区标题");
	}, DESKTOP);
}

/** R17 明暗与截图：1280 / 390 / 360 × 明暗，四个分区各一张，另截身份设置、项目页与 $R 转录。 */
async function R17() {
	const p = await ensureEditProject();
	const tid = await ensureR();
	const g = await getIdentity("agent");
	await putIdentity("agent", {
		...clean(g),
		avatarIcon: g.avatarImage ? undefined : g.avatarIcon || "🦊",
	});
	const shotsDone: string[] = [];
	const shape = (page: Page) =>
		page.evaluate(() => {
			const bar = document.querySelector("[data-testid=section-tabbar]");
			const cap = bar?.firstElementChild as HTMLElement | null;
			const plate = document.querySelector<HTMLElement>(
				"[data-testid=section-tabbar-plate]",
			);
			const av = bar?.querySelector<HTMLElement>("[data-avatar-of=assistant]");
			const ok = (el: HTMLElement | null | undefined, side: "h" | "w") => {
				if (!el) return null;
				const r = el.getBoundingClientRect();
				return (
					Number.parseFloat(getComputedStyle(el).borderTopLeftRadius) >=
					(side === "h" ? r.height : r.width) / 2 - 0.5
				);
			};
			return {
				capsule: ok(cap, "h"),
				plate: ok(plate, "h"),
				avatar: ok(av, "w"),
			};
		});
	for (const scheme of ["light", "dark"] as const) {
		for (const vp of [DESKTOP, PHONE, PHONE_S]) {
			await withPage(async (page) => {
				await page.emulateMedia({ colorScheme: scheme });
				await page.goto(`${BASE}/`);
				await tabBar(page).waitFor({ timeout: 20_000 });
				const theme = await page.evaluate(
					() => document.documentElement.dataset.theme,
				);
				expect(
					theme === scheme,
					`${vp.width} ${scheme}：html[data-theme=${theme}]`,
				);
				const phone = vp.width < 600;
				if (phone) {
					await page
						.getByRole("button", { name: zh.nav.sidebar, exact: true })
						.click();
					await waitFor("底栏露出", () => sinkBarShown(page), 10_000, 150);
				}
				for (const key of ["threads", "assistant", "projects", "more"]) {
					await tab(page, key).click();
					await waitTab(page, key);
					await Bun.sleep(450);
					if (phone && !(await sinkBarShown(page))) {
						await page
							.getByRole("button", { name: zh.nav.sidebar, exact: true })
							.click();
						await waitFor("底栏露出", () => sinkBarShown(page), 10_000, 150);
						await Bun.sleep(300);
					}
					const name = `R17-${vp.width}-${scheme}-${key}-${RUN}`;
					await shot(page, name);
					shotsDone.push(name);
					const s = await shape(page);
					expect(
						s.capsule === true && s.plate === true && s.avatar !== false,
						`${vp.width} ${scheme} ${key}：胶囊 / 底板 / 圆形头像是胶囊或正圆（${j(s)}）`,
					);
				}
			}, vp);
		}
		await withPage(async (page) => {
			await page.emulateMedia({ colorScheme: scheme });
			for (const [path, name] of [
				["/settings/identity", "identity"],
				[`/projects/${p.id}`, "project"],
				[`/t/${tid}`, "transcript"],
			] as const) {
				await page.goto(`${BASE}${path}`);
				await tabBar(page).waitFor({ timeout: 20_000 });
				if (name === "transcript") await waitHeads(page, 1);
				else await Bun.sleep(1500);
				const n = `R17-1280-${scheme}-${name}-${RUN}`;
				await shot(page, n);
				shotsDone.push(n);
			}
		}, DESKTOP);
	}
	note(
		`共 ${shotsDone.length} 张，目录 ${SHOTS}（底栏胶囊、底板、圆形头像按守则人工再看一遍）`,
	);
}

/** R18 桌面壳走查（人工）。 */
async function R18() {
	if (process.env.M96_NO_DESKTOP === "1")
		throw new Skip("M96_NO_DESKTOP=1（未做桌面壳走查）");
	const list = [
		"① 底栏在侧栏底部、左下角与窗口圆角协调，不遮挡红绿灯 / 自绘按钮",
		"② 拖动品牌行仍能移动窗口，底栏不能拖动窗口",
		"③ Ctrl/⌘+1…4、Ctrl/⌘+/ 生效",
		"④ 侧栏收起后红绿灯 / 左侧按钮落到页头，快捷键切分区后仍在页头",
		"⑤ 四个分区滑动无残影（WebKitGTK 降级圆角下胶囊仍是胶囊）",
		"⑥ /projects/<id> 与设置页页头右端的窗口按钮在位",
		"⑦ 系统设为减少动态后切分区是淡入",
	];
	note(
		`桌面壳：${existsSync(DESKTOP_BIN) ? DESKTOP_BIN : `未找到 ${DESKTOP_BIN}（或用 cd web && bun run tauri dev）`}；无显示器时 dbus-run-session -- xvfb-run -a 起壳`,
	);
	for (const l of list) note(l);
	if (process.env.M96_SHELL_OK !== "1")
		throw new Skip(
			"人工走查未确认（逐项看完后设 M96_SHELL_OK=1 重跑 R18 记 PASS）",
		);
	check(true, "M96_SHELL_OK=1：人工走查清单 ①–⑦ 已确认");
}

// ---------------------------------------------------------------- 主流程

const TESTS: [string, () => Promise<void>][] = [
	["R19", R19],
	["R13", R13],
	["R14", R14],
	["R20", R20],
	["R21", R21],
	["R15", R15],
	["R22", R22],
	["R23", R23],
	["R24", R24],
	["R16", R16],
	["R25", R25],
	["R2", R2],
	["R3", R3],
	["R4", R4],
	["R5", R5],
	["R6", R6],
	["R7", R7],
	["R8", R8],
	["R9", R9],
	["R10", R10],
	["R11", R11],
	["R12", R12],
	["R17", R17],
	["R18", R18],
];

const RUN_START = Date.now();
const keepTimer = setInterval(() => void keepTick(), 5_000);
console.log(
	`M9.6 验收 run=${RUN} base=${BASE} project=${PROJECT} shots=${SHOTS}`,
);
await backupIdentities();

for (const [id, fn] of TESTS) {
	if (ONLY && !ONLY.has(id)) continue;
	evidence = [];
	deferred = [];
	consoleErrors = [];
	current = id;
	console.log(`── ${id}`);
	const t0 = Date.now();
	let verdict: Verdict = "PASS";
	try {
		await fn();
		if (deferred.length) throw new Error(`断言失败：${deferred.join("；")}`);
	} catch (e) {
		note(
			`${e instanceof Skip ? "跳过" : "错误"}：${e instanceof Error ? e.message : String(e)}`,
		);
		verdict = e instanceof Skip ? "SKIP" : "FAIL";
	}
	const seconds = (Date.now() - t0) / 1000;
	console.log(`${id} ${verdict} (${seconds.toFixed(1)}s)`);
	results.push({ id, verdict, seconds, evidence });
	await Bun.write(
		`${SHOTS}/results-${RUN}.json`,
		JSON.stringify({ run: RUN, results, pageErrors }, null, 2),
	);
}

// ---------------------------------------------------------------- 清理

clearInterval(keepTimer);
awake.clear();
// 主流程里 TS 把 keepWs / browser 收窄成 null（赋值都在函数里）：按声明类型取
(keepWs as WebSocket | null)?.close();
for (const ctx of extraContexts) await ctx.close().catch(() => {});
await (browser as Browser | null)?.close();
await restoreIdentities().catch((e) =>
	console.log(`清理：还原身份失败 ${String(e)}`),
);
if (editProjectId && !editProjectCreated) {
	const r = await api("PATCH", `/api/v1/projects/${editProjectId}`, {
		setupScript: editProjectSetup ?? "",
	});
	console.log(`清理：还原 $P 的 setup 脚本 → ${r.status}`);
}
if (KEEP) console.log("M96_KEEP=1：保留本次建的线程、orb、分享与项目");
else {
	for (const id of createdShares) {
		const r = await api("DELETE", `/api/v1/shares/${id}`);
		console.log(`清理：撤销分享 ${id} → ${r.status}`);
	}
	// 本次建的线程：脚本建的 + 本次出现的助手对话（R7 在界面上新建的）
	const runThreads = new Set(createdThreads);
	for (const r of sql<{ id: string }>(
		`select id from threads where kind='assistant' and created_at>=${RUN_START}`,
	))
		runThreads.add(r.id);
	const coordOrbs = new Set(
		sql<{ id: string }>("select id from orbs where coordinator=1").map(
			(r) => r.id,
		),
	);
	const oldFile = `${ROOT}/.dev/m96old.thread`;
	const old =
		process.env.M96_OLD_THREAD ??
		(existsSync(oldFile) ? readFileSync(oldFile, "utf8").trim() : "");
	const oldOrb = old ? threadRow(old)?.orb_id : undefined;
	for (const tid of runThreads) {
		const o = threadRow(tid)?.orb_id;
		if (o) createdOrbs.add(o);
	}
	runThreads.delete(old);
	for (const orb of createdOrbs) {
		if (coordOrbs.has(orb) || orb === oldOrb) continue;
		const st = orbState(orb);
		if (st !== "deleted" && st !== "deleting" && st !== "?")
			await api("DELETE", `/api/v1/orbs/${orb}`);
	}
	let deleted = 0;
	for (const tid of runThreads) {
		if (!threadRow(tid)) continue;
		const r = await api("DELETE", `/api/v1/threads/${tid}`);
		if (r.status === 204) deleted++;
		else {
			console.log(
				`清理：删线程 ${tid} → ${r.status} ${r.text.slice(0, 120)}，改为归档`,
			);
			await api("PATCH", `/api/v1/threads/${tid}`, { archived: true });
		}
	}
	if (editProjectCreated) {
		const r = await api("DELETE", `/api/v1/projects/${editProjectId}`);
		console.log(`清理：删项目 $P → ${r.status}`);
	}
	console.log(
		`已清理：线程 ${deleted} 个、orb ${createdOrbs.size} 台（不含 $OLD 与协调 orb）`,
	);
}

const out = `${SHOTS}/results-${RUN}.json`;
await Bun.write(
	out,
	JSON.stringify({ run: RUN, results, pageErrors }, null, 2),
);
if (pageErrors.length)
	console.log(
		`页面错误 ${pageErrors.length} 条：\n  ${pageErrors.join("\n  ")}`,
	);
console.log(`\n汇总（${out}）`);
for (const r of results)
	console.log(`${r.id} ${r.verdict} (${r.seconds.toFixed(1)}s)`);
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
