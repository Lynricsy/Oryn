#!/usr/bin/env bun
/**
 * M25 真机验收（计划 `docs/plan-m25.md` 步骤 4.1 的 C1–C10，加主 agent 追加的 C11）：对 dev 部署（orynd + docker provider，
 * `make image` 之后的 oryn-orb:dev，dev 配真实模型）逐项执行，每项打印 `C<n> PASS|FAIL (耗时)` 与证据，失败时给打开的页面截图，
 * 结束时写 results JSON。写法与夹具照 `m8-accept.ts`。
 *
 *   C1  新线程 T 的 Desktop 标签：任务栏 6 个图标按清单顺序出现，图标都加载成功（naturalWidth > 0）。
 *   C2  逐个点 6 个图标：20 秒内出现运行圆点；orb 里 `xprop -root _NET_CLIENT_LIST` 的窗口数与 `desktop.windows` 相符。
 *   C3  VS Code 只有一个窗口：点图标最小化（`_NET_WM_STATE_HIDDEN`），再点还原到前台。
 *   C4  任务栏右键终端 →「新建窗口」，终端有两个窗口；再点终端图标弹出菜单，菜单里两行窗口。
 *   C5  先经 REST 关掉 C4 多开的终端窗口（同一进程的两个同名窗口会让 `{app}` 选择与 AT-SPI frame 匹配都报歧义），
 *       再在 orb 里起 omp RPC（`negotiate_protocol` v2 + `oryn_tool_call` 执行 eval，同日志 0417 的探针）：
 *       `computer.capabilities().ax` 为真，`computer.window({app:"xfce4-terminal"}).ax({maxDepth:3})` 非空。
 *   C6  让 T 的 agent（真实模型）调 `desktop_record` start → `sleep 3` → stop：工具结果 durationMs > 2000；
 *       对话里的视频卡片在浏览器里真实播放（`play()` 后 currentTime > 0，duration > 2），再拖到 duration×0.75，
 *       等 `seeked` 后落点与目标相差 < 0.5s，并记下视频地址的响应状态（206 与否）。
 *   C7  助手对话调 `computer_record` start → `computer_bash sleep 3` → stop（助手经 `write xd://computer_record` 调用，
 *       结果在 `details.xdev.inner`）：文件库多一个 mp4；内容接口带 Range 回 206；对话里的视频卡片真实播放、拖动同 C6，
 *       且浏览器取视频时出现 206 响应。
 *   C8  T 的 agent 回合进行中（`control.state == agent`）点任务栏：弹出接管确认；取消后控制状态不变。
 *   C9  临时目录 `git init` 并提交可执行的 `.oryn/desktop/resume`，`oryn project create --push` 推成托管项目；项目里新线程
 *       的 Desktop 标签 60 秒内出现标题 `m25-resume` 的窗口（xfce4-terminal 若把 shell 的动态标题接在后面，按
 *       「m25-resume - 」前缀也认），进程 cwd 是 `$HOME/workspace/repo`；冻结再唤醒后仍只有同一个；
 *       停机再启动后又出现一个（新进程）。
 *   C10 1280 宽 DPR 2 亮色截图（侧栏收起、面板 820）：T 的 Desktop 标签只留 VS Code 与终端各一个窗口，
 *       存 `docs/assets/desktop-dock-light.png`。
 *   C11 idle 状态经 orbd.sock 开始录制，再经 REST 接管桌面：录制以 `stoppedBy=control` 结束（结果由下一次 stop 取走）。
 *
 * 触发路径：C6 / C7 / C8 用 dev 的真实模型（`oryn thread send`、`POST /assistant/conversations`）；C11 不经模型，
 *   在 orb 里直接连 `$HOME/.oryn/orbd.sock` 发 `desktop.record.start / stop`（与 `desktop_record` 工具同一条 sock 路径）。
 * orb 内一律 `docker exec <orb> bash -lc …`（容器默认用户 user），需要 X 的命令带 DISPLAY=:1。
 * 让 orb 保持唤醒：同 m8，本进程开一条 `/api/v1/ws`，每 5 秒对「需要醒着的线程」各发一次 `focus`。
 *
 * 用法（web/ 目录）：bun run accept:m25 [-- --only C1,C2]
 * 环境变量：ORYN_URL（默认 http://localhost:7600）、ORYN_ROOT、ORYN_TOKEN（默认 .dev/credentials.json 的 token）；
 *   M25_BIN（`oryn` / `orynd` 所在目录，默认 <ROOT>/bin；验收已提交版本时指向干净 worktree 的 bin）；
 *   M25_THREAD（C1–C8 / C10 / C11 共用的线程 T；缺省时新建）；M25_KEEP=1（不清理本次建的 orb、线程、项目与文件）；
 *   CHROMIUM（默认 /usr/bin/chromium）；M25_SHOTS（默认 /tmp/oryn-shots/m25-accept）。
 * 场景依赖：C2 要求 T 上还没开过这些应用（新建的 T 满足）；C3 / C4 / C5 / C10 用 C2 打开的窗口，单独 `--only` 时按需补开。
 */
import {
	copyFileSync,
	mkdirSync,
	mkdtempSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Page,
	type Response as PwResponse,
} from "playwright-core";

const ROOT = process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`;
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const CREDS = `${ROOT}/.dev/credentials.json`;
const TOKEN =
	process.env.ORYN_TOKEN ??
	(JSON.parse(readFileSync(CREDS, "utf8")) as { token: string }).token;
const BIN = process.env.M25_BIN ?? `${ROOT}/bin`;
const ORYN = `${BIN}/oryn`;
const SHOTS = process.env.M25_SHOTS ?? "/tmp/oryn-shots/m25-accept";
const DOC_SHOT = `${ROOT}/docs/assets/desktop-dock-light.png`;
const DB = `${ROOT}/.dev/data/oryn.db`;
const RUN = Math.random().toString(36).slice(2, 6);
const KEEP = process.env.M25_KEEP === "1";
const ORB_HOME = "/home/user";
const ORB_SOCK = `${ORB_HOME}/.oryn/orbd.sock`;
/** orbd 给桌面进程与 agent 的无障碍环境（计划「已定的取舍」的无障碍环境一条）。 */
const DESK_ENV = {
	DISPLAY: ":1",
	XDG_RUNTIME_DIR: `${ORB_HOME}/.oryn/desktop/run`,
	DBUS_SESSION_BUS_ADDRESS: `unix:path=${ORB_HOME}/.oryn/desktop/run/bus`,
	GTK_A11Y: "atspi",
	ACCESSIBILITY_ENABLED: "1",
};
/** 镜像清单 `desktop-apps.json` 的顺序（计划步骤 1.2）。 */
const APPS = [
	"chromium",
	"terminal",
	"vscode",
	"files",
	"editor",
	"libreoffice",
] as const;
mkdirSync(SHOTS, { recursive: true });

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

// ---------------------------------------------------------------- 工具

const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const te = new TextEncoder();

interface ShRes {
	code: number;
	out: string;
	stdout: string;
}

/** 异步跑 shell（不卡事件循环：保活连接要在等待期间照常发送）。 */
async function sh(
	cmd: string,
	opts: { stdin?: string; timeoutMs?: number; cwd?: string } = {},
): Promise<ShRes> {
	const p = Bun.spawn(["bash", "-c", cmd], {
		cwd: opts.cwd ?? ROOT,
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
}

const threadRow = (tid: string) =>
	sql<ThreadRow>(
		`select state, last_seq, orb_id from threads where id='${tid}'`,
	)[0];

const orbState = (orb: string) =>
	sql<{ state: string }>(`select state from orbs where id='${orb}'`)[0]
		?.state ?? "?";

/** orb 容器里以 user 身份跑 shell（登录 shell，PATH 与 agent 相同）。 */
function dexec(
	orb: string,
	cmd: string,
	env: Record<string, string> = {},
	timeoutMs = 120_000,
) {
	const e = Object.entries(env)
		.map(([k, v]) => `-e ${q(`${k}=${v}`)} `)
		.join("");
	return sh(`docker exec ${e}${orb} bash -lc ${q(cmd)}`, { timeoutMs });
}

/** 同 dexec，退出码非 0 时抛错，返回 stdout。 */
async function dout(
	orb: string,
	cmd: string,
	env: Record<string, string> = {},
	timeoutMs = 120_000,
) {
	const r = await dexec(orb, cmd, env, timeoutMs);
	if (r.code !== 0)
		throw new Error(`orb 内 \`${cmd}\` 退出码 ${r.code}：${r.out.slice(-600)}`);
	return r.stdout;
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

/** 所有者接口（设备令牌 + CSRF 头）。 */
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
		// 非 JSON 体
	}
	return { status: res.status, json, text };
}

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
	intervalMs = 500,
) {
	const end = Date.now() + ms;
	while (Date.now() < end) {
		if (!(await fn())) throw new Error(`未能保持：${what}`);
		await Bun.sleep(intervalMs);
	}
}

// ---------------------------------------------------------------- 结果

type Verdict = "PASS" | "FAIL";
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

async function shot(page: Page, name: string) {
	const p = `${SHOTS}/${name}.png`;
	await page.screenshot({ path: p }).catch(() => {});
	note(`截图 ${p}`);
	return p;
}

// ---------------------------------------------------------------- 模型

interface DeskApp {
	id: string;
	name: string;
	running: boolean;
	launching: boolean;
}

interface DeskWin {
	id: string;
	appId?: string;
	title: string;
	active: boolean;
	minimized: boolean;
}

interface OrbJson {
	id: string;
	state: string;
	desktop?: {
		displayReady: boolean;
		browser: string;
		control?: { state: string; userHeld?: boolean };
		apps?: DeskApp[];
		windows?: DeskWin[];
	};
}

interface Th {
	tid: string;
	orb: string;
}

interface Recording {
	path: string;
	bytes: number;
	durationMs: number;
	width: number;
	height: number;
	truncated: boolean;
	stoppedBy: string;
}

async function getOrb(orb: string): Promise<OrbJson> {
	const r = await api("GET", `/api/v1/orbs/${orb}`);
	if (r.status !== 200) throw new Error(`GET orbs/${orb} ${r.status}`);
	return r.json as OrbJson;
}

const wins = async (orb: string) => (await getOrb(orb)).desktop?.windows ?? [];
const winsOf = async (orb: string, app: string) =>
	(await wins(orb)).filter((w) => w.appId === app);
const appOf = async (orb: string, app: string) =>
	(await getOrb(orb)).desktop?.apps?.find((a) => a.id === app);

/** orb 里 X 根窗口的 `_NET_CLIENT_LIST`（十进制窗口 id）。 */
async function xClients(orb: string): Promise<string[]> {
	const out = await dout(orb, "xprop -root _NET_CLIENT_LIST", DESK_ENV);
	const m = /#\s*(.*)$/.exec(out);
	if (!m?.[1]) return [];
	return m[1]
		.split(",")
		.map((s) => s.trim())
		.filter(Boolean)
		.map((h) => String(Number.parseInt(h, 16)));
}

const xWinState = (orb: string, wid: string) =>
	dout(orb, `xprop -id ${wid} _NET_WM_STATE`, DESK_ENV);

async function xWinPid(orb: string, wid: string): Promise<number> {
	const out = await dout(orb, `xprop -id ${wid} _NET_WM_PID`, DESK_ENV);
	const m = /=\s*(\d+)/.exec(out);
	if (!m?.[1]) throw new Error(`窗口 ${wid} 没有 _NET_WM_PID：${out}`);
	return Number(m[1]);
}

const createdOrbs = new Set<string>();
const createdThreads = new Set<string>();

// ---------------------------------------------------------------- 线程与 orb

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

interface ToolResultMsg {
	role: string;
	toolName?: string;
	isError?: boolean;
	content?: { type?: string; text?: string }[];
	details?: unknown;
}

interface XdevDetails {
	xdev?: { tool?: string; inner?: unknown };
}

/**
 * `beforeSeq` 之后名为 toolName 的工具结果（按顺序）。助手经设备调用宿主工具时（`write xd://computer_record`），
 * 条目里的 toolName 是 write，真实工具名与结果在 `details.xdev.{tool, inner}`：换成真实的名字与 details。
 */
function toolResults(
	tid: string,
	beforeSeq: number,
	toolName: string,
): ToolResultMsg[] {
	const rows = sql<{ p: string }>(
		`select cast(payload as text) p from entries where thread_id='${tid}' and seq>${beforeSeq} and type='message' and instr(cast(payload as text), '"role":"toolResult"')>0 and instr(cast(payload as text), '"${toolName}"')>0 order by seq`,
	);
	const out: ToolResultMsg[] = [];
	for (const r of rows) {
		const m = (JSON.parse(r.p) as { message?: ToolResultMsg }).message;
		if (m?.role !== "toolResult") continue;
		if (m.toolName === toolName) out.push(m);
		else {
			const x = (m.details as XdevDetails | undefined)?.xdev;
			if (x?.tool === toolName) out.push({ ...m, toolName, details: x.inner });
		}
	}
	return out;
}

const resultText = (m: ToolResultMsg) =>
	(m.content ?? [])
		.map((c) => c.text ?? "")
		.join("")
		.slice(0, 300);

/** 发一条消息（不等回合结束），返回发之前的 last_seq。 */
async function send(tid: string, msg: string): Promise<number> {
	const before = threadRow(tid)?.last_seq ?? 0;
	const r = await sh(
		`ORYN_CREDENTIALS=${q(CREDS)} ${q(ORYN)} thread send ${tid} ${q(msg)}`,
		{ timeoutMs: 120_000 },
	);
	if (r.code !== 0) throw new Error(`thread send 失败：${r.out}`);
	return before;
}

/** 等线程的 orb 到 ready（需要时唤醒；还要求 displayReady）。 */
async function ensureReady(tid: string, timeoutMs = 600_000): Promise<string> {
	let lastWake = 0;
	return waitFor(
		`线程 ${tid} 的 orb ready 且桌面就绪`,
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
			return (await getOrb(o)).desktop?.displayReady ? o : undefined;
		},
		timeoutMs,
		2000,
	);
}

/** 建线程（断言 201），等 orb 与首个回合就绪，并加入保活集合。 */
async function newThread(
	label: string,
	extra: Record<string, unknown> = {},
): Promise<Th> {
	const r = await api("POST", "/api/v1/threads", {
		prompt: "只回复 ok",
		browserProfileId: "",
		...extra,
	});
	if (r.status !== 201)
		throw new Error(`建线程 ${label} ${r.status} ${r.text.slice(0, 300)}`);
	const created = r.json as { thread?: { id: string }; id?: string };
	const tid = created.thread?.id ?? created.id ?? "";
	createdThreads.add(tid);
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

// ---------------------------------------------------------------- 浏览器页面

let ctxA: BrowserContext;
let ctxDoc: BrowserContext;
let pageT: Page | undefined;
const pageErrors: string[] = [];

function wire(page: Page, tag: string) {
	page.on("pageerror", (e) => {
		pageErrors.push(`[${tag}] ${e.message}`);
		console.log(`    [${tag} pageerror] ${e.message}`);
	});
	return page;
}

const DESK_TAB = "[data-panel-tab=desktop]";

async function openDesktopTab(page: Page) {
	const tab = page.locator(DESK_TAB).first();
	await tab.waitFor({ timeout: 60_000 });
	if ((await tab.getAttribute("aria-selected")) !== "true") await tab.click();
	await page
		.locator("[data-testid=desktop-panel]")
		.waitFor({ timeout: 30_000 });
	await page
		.locator("[data-testid=desktop-status][data-state=connected]")
		.waitFor({ timeout: 60_000 });
}

/**
 * 打开线程页（Desktop 标签已连接）。缺省面板宽 760、侧栏展开；`doc` 给文档配图：收起侧栏、面板放宽到 820，
 * 画面里少些无关的线程列表。
 */
async function openThreadDesk(
	ctx: BrowserContext,
	tid: string,
	tag: string,
	doc = false,
) {
	const page = wire(await ctx.newPage(), tag);
	await page.goto(`${BASE}/`);
	await page.evaluate((doc) => {
		localStorage.setItem("oryn.panel.width", doc ? "820" : "760");
		localStorage.setItem("oryn.sidebar.collapsed", doc ? "1" : "0");
		localStorage.removeItem("oryn.desktop.scale");
		localStorage.removeItem("oryn.desktop.control");
	}, doc);
	await page.goto(`${BASE}/t/${tid}`);
	await openDesktopTab(page);
	return page;
}

const dock = (page: Page, key: string) =>
	page.locator(`[data-testid=desktop-dock] [data-testid=dock-${key}]`);
const runningDot = (page: Page, key: string) =>
	dock(page, key).locator("[data-testid=dock-running-dot]");

/**
 * 展开 transcript 里名为 toolName 的工具卡，找到其中的录像 `<video>`：静音播放到 currentTime > 0（`preload="none"`，
 * 元数据在 play 之后才取）；再等元数据就绪（duration 有限且 > 0），拖到 duration × 0.75，等 `seeked`，断言落点与目标
 * 相差 < 0.5s（目标不在 seekable 范围内时取范围里最近的值）。同时用 page.on('response') 记下这个视频地址的每个响应状态。
 */
async function playCardVideo(page: Page, toolName: string) {
	const cards = page.locator(".tv-card").filter({
		// 助手经设备调用的宿主工具，卡片名写成 `xd://<工具>`（读文档的卡片同名，展开后没有视频，不影响）
		has: page.locator(".tv-name", {
			hasText: new RegExp(`^(xd://)?${toolName}$`),
		}),
	});
	await cards.first().waitFor({ timeout: 60_000 });
	const n = await cards.count();
	for (let i = 0; i < n; i++) {
		const head = cards.nth(i).locator(".tv-head");
		if ((await head.getAttribute("aria-expanded")) !== "true")
			await head.click();
	}
	const video = cards.locator("[data-testid=record-video]");
	check((await video.count()) === 1, `${toolName} 卡片里恰有 1 个 <video>`);
	await video.scrollIntoViewIfNeeded();
	const src = (await video.getAttribute("src")) ?? "";
	const url = new URL(src, BASE).href;
	const statuses: string[] = [];
	const onResponse = (r: PwResponse) => {
		if (r.url() === url)
			statuses.push(
				`${r.status()}${r.headers()["content-range"] ? ` ${r.headers()["content-range"]}` : ""}`,
			);
	};
	page.on("response", onResponse);
	try {
		const played = await video.evaluate(async (el) => {
			const v = el as HTMLVideoElement;
			const sleep = (ms: number) => {
				const { promise, resolve } = Promise.withResolvers<void>();
				setTimeout(resolve, ms);
				return promise;
			};
			v.muted = true;
			try {
				await v.play();
			} catch (e) {
				return {
					error: `play() 失败：${String(e)}（MediaError ${v.error?.code ?? 0}）`,
				};
			}
			const t0 = Date.now();
			while (v.currentTime <= 0 && Date.now() - t0 < 20_000) await sleep(100);
			const playedTo = v.currentTime;
			v.pause();
			const t1 = Date.now();
			while (
				!(Number.isFinite(v.duration) && v.duration > 0) &&
				Date.now() - t1 < 20_000
			)
				await sleep(100);
			const duration = v.duration;
			if (!(Number.isFinite(duration) && duration > 0))
				return { error: `元数据没有就绪：duration=${duration}` };
			const target = duration * 0.75;
			const ranges: [number, number][] = [];
			for (let i = 0; i < v.seekable.length; i++)
				ranges.push([v.seekable.start(i), v.seekable.end(i)]);
			// 目标不在任何 seekable 区间里时，取最近的可拖位置
			let expect = target;
			if (!ranges.some(([a, b]) => target >= a && target <= b)) {
				let best = Number.POSITIVE_INFINITY;
				for (const [a, b] of ranges)
					for (const p of [a, b])
						if (Math.abs(p - target) < Math.abs(best - target)) best = p;
				if (Number.isFinite(best)) expect = best;
			}
			const seeked = Promise.withResolvers<boolean>();
			v.addEventListener("seeked", () => seeked.resolve(true), { once: true });
			const timer = setTimeout(() => seeked.resolve(false), 15_000);
			v.currentTime = target;
			const ok = await seeked.promise;
			clearTimeout(timer);
			return {
				playedTo,
				duration,
				target,
				expect,
				seekedAt: v.currentTime,
				seekedEvent: ok,
				seekable: ranges,
				w: v.videoWidth,
				h: v.videoHeight,
			};
		});
		note(`视频 src=${src} 结果 ${JSON.stringify(played)}`);
		if ("error" in played) throw new Error(played.error);
		check(
			played.playedTo > 0,
			`play() 后 currentTime=${played.playedTo.toFixed(2)} > 0`,
		);
		check(
			played.seekedEvent && Math.abs(played.seekedAt - played.expect) < 0.5,
			`拖到 ${played.target.toFixed(2)}s（duration×0.75${played.expect !== played.target ? `，seekable 内最近 ${played.expect.toFixed(2)}s` : ""}）后收到 seeked，currentTime=${played.seekedAt.toFixed(2)}，相差 < 0.5s`,
		);
		note(`该视频地址的响应：${statuses.join("，") || "（无）"}`);
		return { duration: played.duration, src, statuses };
	} finally {
		page.off("response", onResponse);
	}
}

// ---------------------------------------------------------------- 共享夹具：线程 T

let T: Th | null = null;

async function ensureT(): Promise<Th> {
	if (T && orbState(T.orb) !== "deleted") {
		awake.add(T.tid);
		T.orb = await ensureReady(T.tid);
		return T;
	}
	if (process.env.M25_THREAD) {
		const tid = process.env.M25_THREAD;
		awake.add(tid);
		T = { tid, orb: await ensureReady(tid) };
		note(`线程 T = ${tid}（M25_THREAD），orb ${T.orb}`);
	} else T = await newThread("T");
	return T;
}

/** T 的线程页（复用；Desktop 标签已连接）。 */
async function deskPage(): Promise<{ page: Page; th: Th }> {
	const th = await ensureT();
	if (pageT && !pageT.isClosed() && pageT.url().includes(th.tid)) {
		await openDesktopTab(pageT);
		return { page: pageT, th };
	}
	pageT = await openThreadDesk(ctxA, th.tid, "T");
	return { page: pageT, th };
}

/** 应用没有窗口时经 REST 启动并等到窗口出现（单独 `--only` 时补齐前置窗口用，不算断言）。 */
async function ensureAppWindow(orb: string, app: string) {
	if ((await winsOf(orb, app)).length > 0) return;
	const r = await api("POST", `/api/v1/orbs/${orb}/desktop/apps/${app}/launch`);
	if (r.status !== 200) throw new Error(`launch ${app} ${r.status} ${r.text}`);
	await waitFor(
		`${app} 出现窗口`,
		async () => (await winsOf(orb, app)).length > 0,
		60_000,
		500,
	);
	note(`（前置）经 REST 启动了 ${app}`);
}

/** orb 里起 omp RPC，经 `oryn_tool_call` 执行一段 eval（js），返回工具结果文本。驱动同日志 0417 的探针 cu.py。 */
const OMP_RPC = `import json, os, subprocess, sys
overlay = "/tmp/m25-omp-overlay.yml"
open(overlay, "w").write("computer:\\n  enabled: true\\n")
p = subprocess.Popen(
    ["omp", "--mode", "rpc", "--no-session", "--no-skills", "--no-extensions", "--no-rules",
     "--no-context-files", "--cwd", os.environ["HOME"], "--config", overlay],
    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=open("/tmp/m25-omp.err", "w"), text=True, bufsize=1)
def frames():
    for line in p.stdout:
        try:
            yield json.loads(line)
        except Exception:
            pass
it = frames()
def send(o):
    p.stdin.write(json.dumps(o) + "\\n"); p.stdin.flush()
def wait(pred):
    for f in it:
        if f.get("type") == "extension_ui_request":
            send({"type": "extension_ui_response", "id": f["id"], "cancelled": True}); continue
        if pred(f):
            return f
    raise SystemExit("omp 退出：" + open("/tmp/m25-omp.err").read()[-800:])
wait(lambda f: f.get("type") == "ready")
send({"id": "n1", "type": "negotiate_protocol", "protocolVersion": 2})
wait(lambda f: f.get("id") == "n1")
send({"id": "c1", "type": "oryn_tool_call", "callId": "m25", "toolName": "eval",
      "arguments": {"language": "js", "code": open(sys.argv[1]).read()}})
r = wait(lambda f: f.get("id") == "c1")
res = r.get("data", r)
texts = []
def walk(o):
    if isinstance(o, dict):
        if o.get("type") == "text" and isinstance(o.get("text"), str):
            texts.append(o["text"])
        for v in o.values():
            walk(v)
    elif isinstance(o, list):
        for v in o:
            walk(v)
walk(res)
print("\\n".join(texts) if texts else json.dumps(res)[:4000])
p.kill()
`;

/** orb 里对 orbd.sock 发一行请求（`desktop.record.*` 与 desktop_record 工具同一条路径），打印 result / error 行。 */
const SOCK_CALL = `import json, socket, sys
s = socket.socket(socket.AF_UNIX)
s.connect("${ORB_SOCK}")
s.sendall((json.dumps({"method": sys.argv[1]}) + "\\n").encode())
for line in s.makefile():
    o = json.loads(line)
    if o.get("type") in ("result", "error"):
        print(json.dumps(o))
        break
`;

async function sockCall(
	orb: string,
	method: string,
): Promise<{
	type: string;
	result?: unknown;
	code?: string;
	message?: string;
}> {
	await dwrite(orb, "/tmp/m25-sock.py", SOCK_CALL);
	const out = await dout(orb, `python3 /tmp/m25-sock.py ${method}`);
	const line = out.split("\n").pop() ?? "";
	note(`orbd.sock ${method} → ${line.slice(0, 300)}`);
	return JSON.parse(line);
}

// ---------------------------------------------------------------- 场景

async function C1() {
	const { page, th } = await deskPage();
	const ids = await waitFor(
		"任务栏出现 6 个应用图标",
		async () => {
			const v = await page
				.locator("[data-testid=desktop-dock] [role=toolbar] > button")
				.evaluateAll((els) => els.map((e) => e.getAttribute("data-testid")));
			return v.length >= APPS.length ? v : undefined;
		},
		30_000,
	);
	note(`任务栏按钮：${ids.join(" ")}`);
	check(
		JSON.stringify(ids.slice(0, APPS.length)) ===
			JSON.stringify(APPS.map((a) => `dock-${a}`)),
		`前 6 个图标依次是 ${APPS.join(" ")}`,
	);
	const sizes = await waitFor(
		"6 个图标都加载完成",
		async () => {
			const v = await page
				.locator("[data-testid=desktop-dock] [role=toolbar] > button img")
				.evaluateAll((els) =>
					els.map((e) => {
						const img = e as HTMLImageElement;
						return {
							alt: img.alt,
							complete: img.complete,
							w: img.naturalWidth,
							h: img.naturalHeight,
						};
					}),
				);
			return v.length === APPS.length && v.every((x) => x.complete)
				? v
				: undefined;
		},
		30_000,
	);
	note(
		`图标 naturalWidth：${sizes.map((s) => `${s.alt}=${s.w}x${s.h}`).join("，")}`,
	);
	check(
		sizes.every((s) => s.w > 0),
		"6 个图标都加载成功（naturalWidth > 0）",
	);
	check(
		(await page.locator("[data-testid=dock-icon-fallback]").count()) === 0,
		"没有图标退回占位图标",
	);
	const apps = (await getOrb(th.orb)).desktop?.apps ?? [];
	note(`orb.desktop.apps：${apps.map((a) => `${a.id}(${a.name})`).join(" ")}`);
	await shot(page, "C1-dock");
}

async function C2() {
	const { page, th } = await deskPage();
	for (const app of APPS) {
		const before = await appOf(th.orb, app);
		if (before?.running) {
			note(`${app} 已在运行（不是全新的 T），跳过点击`);
			continue;
		}
		const t0 = Date.now();
		await dock(page, app).click();
		await runningDot(page, app).waitFor({ timeout: 20_000 });
		note(`点 ${app}：${((Date.now() - t0) / 1000).toFixed(1)}s 后出现运行圆点`);
		check(true, `${app} 20 秒内出现运行圆点`);
	}
	// 每个应用都有窗口（chromium 的 running 另看 browser，窗口单独等）
	const listed = await waitFor(
		"6 个应用都有窗口",
		async () => {
			const w = await wins(th.orb);
			return APPS.every((a) => w.some((x) => x.appId === a)) ? w : undefined;
		},
		60_000,
		500,
	);
	note(
		`desktop.windows：${listed.map((w) => `${w.id}:${w.appId ?? "-"}「${w.title.slice(0, 40)}」`).join("；")}`,
	);
	// _NET_CLIENT_LIST 与 desktop.windows 相符（窗口变化的上报至多合并 500ms，等两边一致）
	const [x, api] = await waitFor(
		"_NET_CLIENT_LIST 与 desktop.windows 一致",
		async () => {
			const xs = await xClients(th.orb);
			const ws = (await wins(th.orb)).map((w) => w.id);
			return xs.length === ws.length &&
				[...xs].sort().join() === [...ws].sort().join()
				? [xs, ws]
				: undefined;
		},
		15_000,
		1000,
	);
	note(`xprop -root _NET_CLIENT_LIST：${x.length} 个窗口 ${x.join(",")}`);
	check(
		x.length === api.length && x.length >= APPS.length,
		`X 里 ${x.length} 个窗口，与 desktop.windows 的 ${api.length} 个相同（id 集合一致）`,
	);
	await shot(page, "C2-apps");
}

async function C3() {
	const { page, th } = await deskPage();
	await ensureAppWindow(th.orb, "vscode");
	const vs = await winsOf(th.orb, "vscode");
	check(
		vs.length === 1,
		`VS Code 只有 1 个窗口（${vs.map((w) => w.id).join(",")}）`,
	);
	const wid = vs[0]?.id ?? "";
	const state = async () => (await winsOf(th.orb, "vscode"))[0];
	// 先让它成为活动窗口：单窗口时，非活动 → 点击 = activate
	if (!(await state())?.active) {
		await dock(page, "vscode").click();
		await waitFor(
			"VS Code 成为活动窗口",
			async () => (await state())?.active,
			10_000,
		);
		note("VS Code 原本不是活动窗口：先点一次激活");
	}
	await page
		.locator("[data-testid=dock-vscode][data-active]")
		.waitFor({ timeout: 10_000 });
	await dock(page, "vscode").click();
	const min = await waitFor(
		"VS Code 最小化",
		async () => {
			const w = await state();
			return w?.minimized ? w : undefined;
		},
		10_000,
	);
	const hidden = await xWinState(th.orb, wid);
	note(`最小化后 desktop.windows：${JSON.stringify(min)}；xprop：${hidden}`);
	check(
		hidden.includes("_NET_WM_STATE_HIDDEN"),
		"点图标后 VS Code 最小化（_NET_WM_STATE_HIDDEN）",
	);
	await dock(page, "vscode").click();
	const back = await waitFor(
		"VS Code 还原",
		async () => {
			const w = await state();
			return w && !w.minimized && w.active ? w : undefined;
		},
		10_000,
	);
	const shown = await xWinState(th.orb, wid);
	note(`还原后 desktop.windows：${JSON.stringify(back)}；xprop：${shown}`);
	check(
		!shown.includes("_NET_WM_STATE_HIDDEN"),
		"再点图标后 VS Code 还原并成为活动窗口",
	);
}

async function C4() {
	const { page, th } = await deskPage();
	await ensureAppWindow(th.orb, "terminal");
	const before = (await winsOf(th.orb, "terminal")).length;
	check(before === 1, `终端现有 ${before} 个窗口`);
	await dock(page, "terminal").click({ button: "right" });
	const menu = page.locator("[data-testid=desktop-dock-menu]");
	await menu.getByRole("menuitem", { name: "新建窗口" }).click();
	const two = await waitFor(
		"终端出现第 2 个窗口",
		async () => {
			const w = await winsOf(th.orb, "terminal");
			return w.length === 2 ? w : undefined;
		},
		30_000,
		500,
	);
	note(`终端窗口：${two.map((w) => `${w.id}「${w.title}」`).join("；")}`);
	await menu.waitFor({ state: "detached", timeout: 5_000 });
	// 两个窗口时左键点图标就弹菜单
	await dock(page, "terminal").click();
	await menu.waitFor({ timeout: 5_000 });
	const rows = menu.getByRole("menuitem", { name: /^关闭「/ });
	await waitFor(
		"菜单里两行窗口",
		async () => (await rows.count()) === 2,
		5_000,
	);
	const labels = await rows.evaluateAll((els) =>
		els.map((e) => e.getAttribute("aria-label")),
	);
	note(`菜单行：${labels.join("；")}`);
	check(labels.length === 2, "终端菜单里有两行窗口（各带关闭按钮）");
	await shot(page, "C4-menu");
	await page.keyboard.press("Escape");
	await menu.waitFor({ state: "detached", timeout: 5_000 });
}

async function C5() {
	const th = await ensureT();
	await ensureAppWindow(th.orb, "terminal");
	// C4 之后终端有两个同名窗口（同一进程）：按 { app } 选会报「multiple windows match」，按 id 选又会因两个 AT-SPI frame
	// 同名而报 AxFailed。先经 REST 关掉多余的终端窗口，再按计划写的 { app: "xfce4-terminal" } 选
	const terms = await winsOf(th.orb, "terminal");
	for (const w of terms.slice(1)) {
		const r = await api(
			"POST",
			`/api/v1/orbs/${th.orb}/desktop/windows/${w.id}/close`,
		);
		note(`关闭多余的终端窗口 ${w.id} → ${r.status}`);
	}
	await waitFor(
		"终端只剩一个窗口",
		async () => (await winsOf(th.orb, "terminal")).length === 1,
		15_000,
		500,
	);
	const code = `const caps = await computer.capabilities();
const win = await computer.window({ app: "xfce4-terminal" });
const tree = await win.ax({ maxDepth: 3 });
const s = JSON.stringify(tree);
print("M25AX=" + JSON.stringify({ ax: caps.ax, len: s.length, head: s.slice(0, 240) }));`;
	await dwrite(th.orb, "/tmp/m25-rpc.py", OMP_RPC);
	await dwrite(th.orb, "/tmp/m25-ax.js", code);
	const out = await dout(
		th.orb,
		"python3 /tmp/m25-rpc.py /tmp/m25-ax.js",
		DESK_ENV,
		180_000,
	);
	const m = /M25AX=(\{.*\})/.exec(out);
	if (!m?.[1]) throw new Error(`eval 输出里没有 M25AX：${out.slice(0, 1500)}`);
	const r = JSON.parse(m[1]) as { ax: boolean; len: number; head: string };
	note(`eval 输出：${m[1].slice(0, 400)}`);
	check(r.ax === true, "computer.capabilities().ax 为真");
	check(
		r.len > 2 && r.head !== "null" && r.head !== "{}" && r.head !== "[]",
		`xfce4-terminal 窗口的 ax({maxDepth:3}) 非空（JSON ${r.len} 字节）`,
	);
}

async function C6() {
	const { page, th } = await deskPage();
	const before = await send(
		th.tid,
		"请严格按顺序做三件事：1）调用 desktop_record 工具，action 为 start；2）用 bash 执行 `sleep 3`；3）调用 desktop_record 工具，action 为 stop。完成后只回复 done，不要做别的事。",
	);
	await waitTurn(th.tid, before);
	const rs = toolResults(th.tid, before, "desktop_record");
	note(
		`desktop_record 结果：${rs.map((m) => `${m.isError ? "错误 " : ""}${resultText(m)}`).join(" | ")}`,
	);
	const stop = rs
		.map((m) => m.details as Recording | undefined)
		.find((d) => d && typeof d.durationMs === "number");
	check(
		!!stop,
		"对话里有 desktop_record stop 的结果（details 是 DesktopRecording）",
	);
	const rec = stop as Recording;
	note(`DesktopRecording：${JSON.stringify(rec)}`);
	check(
		rec.durationMs > 2000 && rec.bytes > 0,
		`durationMs=${rec.durationMs} > 2000，bytes=${rec.bytes}`,
	);
	const { duration } = await playCardVideo(page, "desktop_record");
	check(duration > 2, `视频 duration=${duration.toFixed(2)}s > 2`);
	await shot(page, "C6-video");
}

async function C7() {
	const comp0 = (await api("GET", "/api/v1/assistant/computer")).json as {
		orbId?: string;
		state?: string;
		toolsReady: boolean;
	};
	note(`助手的电脑（开始前）：${JSON.stringify(comp0)}`);
	const comp = await waitFor(
		"助手的电脑就绪",
		async () => {
			const r = await api("POST", "/api/v1/assistant/computer/wake");
			if (r.status !== 200) {
				note(`computer/wake → ${r.status} ${r.text.slice(0, 160)}`);
				return undefined;
			}
			return r.json as { orbId?: string; toolsReady: boolean };
		},
		600_000,
		5000,
	);
	// 电脑是本次新建的：清理时整台重置（`computer/reset`），不只删 orb
	if (!comp0.orbId && comp.orbId) compCreated = comp.orbId;
	await waitFor(
		"computer 工具定义已缓存（toolsReady）",
		async () =>
			(
				(await api("GET", "/api/v1/assistant/computer")).json as {
					toolsReady: boolean;
				}
			).toolsReady,
		120_000,
		2000,
	);
	note(`助手的电脑 ${comp.orbId}，toolsReady`);
	const mp4s = async () =>
		(
			(await api("GET", "/api/v1/assistant/files")).json as {
				files: { id: string; name: string; mime: string; size: number }[];
			}
		).files.filter((f) => f.mime === "video/mp4" || f.name.endsWith(".mp4"));
	const before = await mp4s();
	const r = await api("POST", "/api/v1/assistant/conversations", {
		prompt:
			"请严格按顺序做三件事：1）调用 computer_record 工具，action 为 start；2）用 computer_bash 执行 `sleep 3`；3）调用 computer_record 工具，action 为 stop。完成后只回复 done，不要做别的事。",
	});
	if (r.status !== 201) throw new Error(`建助手对话 ${r.status} ${r.text}`);
	const env = r.json as { thread?: { id: string }; id?: string };
	const tid = env.thread?.id ?? env.id ?? "";
	createdThreads.add(tid);
	note(`助手对话 ${tid}`);
	await waitTurn(tid, 0);
	const rs = toolResults(tid, 0, "computer_record");
	note(
		`computer_record 结果：${rs.map((m) => `${m.isError ? "错误 " : ""}${resultText(m)}`).join(" | ")}`,
	);
	const det = rs
		.map(
			(m) =>
				m.details as
					| { recording?: Recording; libraryFileId?: string }
					| undefined,
		)
		.find((d) => d?.recording);
	check(!!det?.libraryFileId, "computer_record stop 的结果带 libraryFileId");
	note(`details：${JSON.stringify(det)}`);
	const after = await mp4s();
	const added = after.filter((f) => !before.some((b) => b.id === f.id));
	note(
		`文件库 mp4：之前 ${before.length} 个，之后 ${after.length} 个；新增 ${JSON.stringify(added)}`,
	);
	check(
		added.length === 1 && added[0]?.id === det?.libraryFileId,
		"文件库多了一个 mp4，正是 computer_record 存进去的那个",
	);
	const fid = det?.libraryFileId ?? "";
	createdFiles.add(fid);
	const res = await fetch(`${BASE}/api/v1/assistant/files/${fid}/content`, {
		headers: { Authorization: `Bearer ${TOKEN}`, Range: "bytes=0-99" },
	});
	const body = new Uint8Array(await res.arrayBuffer());
	const ftyp = new TextDecoder().decode(body.slice(4, 8));
	note(
		`GET files/${fid}/content Range: bytes=0-99 → ${res.status}，Content-Range: ${res.headers.get("content-range")}，Content-Type: ${res.headers.get("content-type")}，${body.length} 字节，box=${ftyp}`,
	);
	check(
		res.status === 206 && body.length === 100 && ftyp === "ftyp",
		"文件库视频的 Range 请求回 206（前 100 字节，mp4 ftyp 开头）",
	);
	await withPage(ctxA, "C7", async (page) => {
		await page.goto(`${BASE}/assistant/${tid}`);
		const { duration, statuses } = await playCardVideo(page, "computer_record");
		check(duration > 2, `视频 duration=${duration.toFixed(2)}s > 2`);
		check(
			statuses.some((s) => s.startsWith("206")),
			"浏览器播放 / 拖动文件库视频时出现 206 响应",
		);
		await shot(page, "C7-video");
	});
}

const createdFiles = new Set<string>();
let compCreated = "";

/** 开一个临时页面；出错时先截图再关闭。 */
async function withPage<R>(
	ctx: BrowserContext,
	tag: string,
	fn: (p: Page) => Promise<R>,
): Promise<R> {
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

async function C8() {
	const { page, th } = await deskPage();
	const before = await send(
		th.tid,
		"用 bash 执行 `sleep 40`，完成后只回复 ok。",
	);
	try {
		await waitFor(
			"control.state == agent（回合进行中）",
			async () => (await getOrb(th.orb)).desktop?.control?.state === "agent",
			60_000,
			500,
		);
		note("回合进行中：control.state = agent");
		await dock(page, "editor").click();
		const dialog = page.getByRole("dialog");
		await dialog.waitFor({ timeout: 10_000 });
		const text = ((await dialog.textContent()) ?? "").replace(/\s+/g, " ");
		note(`弹窗：${text}`);
		check(
			text.includes("接管桌面后再打开？") &&
				((await page.getByTestId("confirm-ok").textContent()) ?? "").length > 0,
			"点任务栏弹出接管确认",
		);
		await shot(page, "C8-confirm");
		await page.getByTestId("confirm-cancel").click();
		await dialog.waitFor({ state: "detached", timeout: 5_000 });
		const st = (await getOrb(th.orb)).desktop?.control;
		note(`取消后 control：${JSON.stringify(st)}`);
		check(
			st?.state === "agent" && !st.userHeld,
			"取消后没有接管，agent 继续操作",
		);
	} finally {
		await waitTurn(th.tid, before).catch((e) => note(String(e)));
	}
}

async function C10() {
	const th = await ensureT();
	await ensureAppWindow(th.orb, "vscode");
	await ensureAppWindow(th.orb, "terminal");
	// 只留 VS Code 与终端各一个窗口：其余窗口经 REST 关闭
	const keep = new Set<string>();
	for (const app of ["vscode", "terminal"]) {
		const w = await winsOf(th.orb, app);
		if (w[0]) keep.add(w[0].id);
	}
	for (const w of await wins(th.orb)) {
		if (keep.has(w.id)) continue;
		const r = await api(
			"POST",
			`/api/v1/orbs/${th.orb}/desktop/windows/${w.id}/close`,
		);
		note(`关闭 ${w.appId ?? "-"} 窗口 ${w.id} → ${r.status}`);
	}
	const left = await waitFor(
		"只剩 VS Code 与终端各一个窗口",
		async () => {
			const w = await wins(th.orb);
			return w.length === 2 && w.every((x) => keep.has(x.id)) ? w : undefined;
		},
		30_000,
		500,
	);
	const term = left.find((w) => w.appId === "terminal");
	if (term) {
		const r = await api(
			"POST",
			`/api/v1/orbs/${th.orb}/desktop/windows/${term.id}/activate`,
		);
		note(`激活终端 ${term.id} → ${r.status}`);
	}
	await waitFor(
		"终端成为活动窗口",
		async () =>
			(await wins(th.orb)).some(
				(w) => w.appId === "terminal" && w.active && !w.minimized,
			),
		10_000,
	);
	const page = await openThreadDesk(ctxDoc, th.tid, "C10", true);
	try {
		check(
			(await page.evaluate(() => document.documentElement.dataset.theme)) ===
				"light",
			"页面是亮色主题",
		);
		await runningDot(page, "vscode").waitFor({ timeout: 10_000 });
		await runningDot(page, "terminal").waitFor({ timeout: 10_000 });
		// 画面稳定：舞台 canvas 的像素哈希连续 2 秒不变
		let last = -1;
		let since = Date.now();
		await waitFor(
			"舞台画面稳定",
			async () => {
				const h = await page.evaluate(() => {
					const c = document.querySelector<HTMLCanvasElement>(
						"[data-testid=desktop-stage] canvas",
					);
					const ctx = c?.width ? c.getContext("2d") : null;
					if (!c || !ctx) return -1;
					const d = ctx.getImageData(0, 0, c.width, c.height).data;
					let x = 0;
					for (let i = 0; i < d.length; i += 64)
						x = (Math.imul(x, 31) + (d[i] ?? 0)) >>> 0;
					return x;
				});
				if (h !== last || h === -1) {
					last = h;
					since = Date.now();
					return false;
				}
				return Date.now() - since >= 2_000;
			},
			30_000,
			500,
		);
		await page.mouse.move(0, 0);
		const p = await shot(page, "C10-desktop-dock-light");
		copyFileSync(p, DOC_SHOT);
		note(`复制到 ${DOC_SHOT}`);
		const png = readFileSync(DOC_SHOT);
		const w = png.readUInt32BE(16);
		const h = png.readUInt32BE(20);
		check(w === 2560, `截图 ${w}x${h}（1280 宽 × DPR 2）`);
	} finally {
		await page.close().catch(() => {});
	}
}

async function C11() {
	const th = await ensureT();
	await waitFor(
		"T 的控制状态是 idle",
		async () => (await getOrb(th.orb)).desktop?.control?.state === "idle",
		60_000,
		500,
	);
	const start = await sockCall(th.orb, "desktop.record.start");
	check(start.type === "result", "idle 状态下经 orbd.sock 开始录制");
	const path = (start.result as { path: string }).path;
	try {
		// 录一小段再接管（ffmpeg 起来之前接管也应结束录制，这里让画面里有几帧）
		await Bun.sleep(1500);
		const take = await api("POST", `/api/v1/orbs/${th.orb}/desktop/control`, {
			action: "take",
		});
		const ctl = (take.json as OrbJson | null)?.desktop?.control;
		note(
			`POST desktop/control take → ${take.status}，control=${JSON.stringify(ctl)}`,
		);
		check(take.status === 200 && ctl?.state === "user", "经 REST 接管桌面");
		const stop = await sockCall(th.orb, "desktop.record.stop");
		const rec = stop.result as Recording | undefined;
		check(
			stop.type === "result" && rec?.stoppedBy === "control",
			`录制以 stoppedBy=control 结束（${JSON.stringify(rec)}）`,
		);
		check(
			rec?.path === path && (rec?.bytes ?? 0) > 0,
			`结果就是这次录制（${path}，${rec?.bytes} 字节）`,
		);
		const again = await sockCall(th.orb, "desktop.record.start");
		check(
			again.type === "error" && again.code === "control_user",
			"用户持有控制时再开始录制回 control_user",
		);
	} finally {
		const rel = await api("POST", `/api/v1/orbs/${th.orb}/desktop/control`, {
			action: "release",
		});
		note(`POST desktop/control release → ${rel.status}`);
	}
}

async function C9() {
	const dir = mkdtempSync(`${tmpdir()}/m25-resume-`);
	const name = `m25-resume-${RUN}`;
	let projectId = "";
	let th: Th | null = null;
	let page: Page | null = null;
	try {
		mkdirSync(`${dir}/.oryn/desktop`, { recursive: true });
		writeFileSync(
			`${dir}/.oryn/desktop/resume`,
			"#!/bin/sh\nxfce4-terminal --title=m25-resume &\n",
			{ mode: 0o755 },
		);
		writeFileSync(`${dir}/README.md`, "M25 resume 验收\n");
		const g = await sh(
			"git init -q -b main && git add -A && git -c user.name=m25 -c user.email=m25@example.invalid commit -qm init && git ls-files -s .oryn/desktop/resume",
			{ cwd: dir },
		);
		if (g.code !== 0) throw new Error(`git init 失败：${g.out}`);
		note(`临时仓库 ${dir}：${g.stdout}`);
		check(g.stdout.startsWith("100755"), "resume 以可执行文件提交");
		const c = await sh(
			`ORYN_CREDENTIALS=${q(CREDS)} ${q(ORYN)} project create ${name} --push --json`,
			{ cwd: dir, timeoutMs: 300_000 },
		);
		if (c.code !== 0)
			throw new Error(`oryn project create --push 失败：${c.out}`);
		const pj = JSON.parse(c.stdout.slice(c.stdout.indexOf("{"))) as {
			id?: string;
			project?: { id: string };
		};
		projectId = pj.id ?? pj.project?.id ?? "";
		note(`托管项目 ${name} = ${projectId}`);
		check(!!projectId, "oryn project create --push 建出托管项目");
		th = await newThread("R", { projectId });
		const orb = th.orb;
		page = await openThreadDesk(ctxA, th.tid, "C9");
		// 实测标题就是 m25-resume；xfce4-terminal 的动态标题模式若把 shell 设的标题接在 --title 后面
		// （「m25-resume - user@host:~/workspace/repo」），也按前缀认
		const resumeWins = async () =>
			(await wins(orb)).filter(
				(w) => w.title === "m25-resume" || w.title.startsWith("m25-resume - "),
			);
		const first = await waitFor(
			"出现标题 m25-resume 的窗口",
			async () => {
				const w = await resumeWins();
				return w.length > 0 ? w : undefined;
			},
			60_000,
			1000,
		);
		note(`m25-resume 窗口：${JSON.stringify(first)}`);
		check(first.length === 1, "60 秒内出现 1 个 m25-resume 窗口");
		const wid = first[0]?.id ?? "";
		const pid = await xWinPid(orb, wid);
		const cwd = await dout(orb, `readlink /proc/${pid}/cwd`);
		note(`窗口 ${wid} 的进程 ${pid} cwd=${cwd}`);
		check(
			cwd === `${ORB_HOME}/workspace/repo`,
			"窗口进程的 cwd 是线程工作目录 $HOME/workspace/repo",
		);
		// 冻结再唤醒：代数不变，不重复执行
		await page.close();
		page = null;
		awake.delete(th.tid);
		await pauseOrb(orb, "freeze");
		awake.add(th.tid);
		await ensureReady(th.tid, 300_000);
		note(`唤醒后 orb ${orbState(orb)}`);
		await holdFor(
			"冻结唤醒后 m25-resume 窗口仍只有一个（同一窗口）",
			async () => {
				const w = await resumeWins();
				return w.length === 1 && w[0]?.id === wid;
			},
			10_000,
		);
		check(true, "冻结并唤醒后 m25-resume 窗口仍只有一个（同一窗口）");
		// 停机再启动：新的 orbd 进程，resume.run 之后再执行一次
		awake.delete(th.tid);
		await pauseOrb(orb, "stop");
		awake.add(th.tid);
		await ensureReady(th.tid, 600_000);
		const again = await waitFor(
			"停机再启动后出现 m25-resume 窗口",
			async () => {
				const w = await resumeWins();
				return w.length > 0 ? w : undefined;
			},
			120_000,
			1000,
		);
		const pid2 = await xWinPid(orb, again[0]?.id ?? "");
		const cwd2 = await dout(orb, `readlink /proc/${pid2}/cwd`);
		note(
			`重启后 m25-resume 窗口：${JSON.stringify(again)}，进程 ${pid2} cwd=${cwd2}`,
		);
		check(
			again.length === 1 && cwd2 === `${ORB_HOME}/workspace/repo`,
			"停机再启动后又出现一个 m25-resume 窗口（cwd 仍是 $HOME/workspace/repo）",
		);
	} finally {
		await page?.close().catch(() => {});
		if (th) awake.delete(th.tid);
		rmSync(dir, { recursive: true, force: true });
		if (projectId && !KEEP) createdProjects.add(projectId);
	}
}

const createdProjects = new Set<string>();

// ---------------------------------------------------------------- 主流程

const TESTS: [string, () => Promise<void>][] = [
	["C1", C1],
	["C2", C2],
	["C3", C3],
	["C4", C4],
	["C5", C5],
	["C6", C6],
	["C7", C7],
	["C8", C8],
	["C9", C9],
	["C10", C10],
	["C11", C11],
];

const keepTimer = setInterval(() => void keepTick(), 5_000);

const browser: Browser = await chromium.launch({
	executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
	headless: true,
	args: ["--no-sandbox", "--autoplay-policy=no-user-gesture-required"],
});
ctxA = await browser.newContext({
	locale: "zh-CN",
	viewport: { width: 1400, height: 900 },
	colorScheme: "light",
	extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
});
ctxDoc = await browser.newContext({
	locale: "zh-CN",
	viewport: { width: 1280, height: 800 },
	deviceScaleFactor: 2,
	colorScheme: "light",
	extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
});
console.log(`M25 验收 run=${RUN} base=${BASE} bin=${BIN} shots=${SHOTS}`);

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
		note(`错误：${e instanceof Error ? e.message : String(e)}`);
		verdict = "FAIL";
		const pages = [ctxA, ctxDoc].flatMap((c) => c.pages());
		for (const [i, p] of pages.entries())
			await shot(p, `${id}-FAIL-${RUN}-${i}`);
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
if (KEEP) console.log("M25_KEEP=1：保留本次建的 orb、线程、项目与文件");
else {
	for (const fid of createdFiles)
		await api("DELETE", `/api/v1/assistant/files/${fid}`);
	if (compCreated) await api("POST", "/api/v1/assistant/computer/reset");
	for (const orb of createdOrbs) {
		const st = orbState(orb);
		if (st !== "deleted" && st !== "deleting")
			await api("DELETE", `/api/v1/orbs/${orb}`);
	}
	for (const tid of createdThreads) {
		const r = await api("DELETE", `/api/v1/threads/${tid}`);
		if (r.status >= 300 && r.status !== 404)
			console.log(
				`清理：删除线程 ${tid} → ${r.status} ${r.text.slice(0, 200)}`,
			);
	}
	for (const pid of createdProjects) {
		const r = await api("DELETE", `/api/v1/projects/${pid}`);
		if (r.status >= 300 && r.status !== 404)
			console.log(
				`清理：删除项目 ${pid} → ${r.status} ${r.text.slice(0, 200)}`,
			);
	}
	console.log(
		`已清理 orb ${createdOrbs.size} 台、线程 ${createdThreads.size} 个、项目 ${createdProjects.size} 个、文件 ${createdFiles.size} 个`,
	);
}

const out = `${SHOTS}/results-${RUN}.json`;
await Bun.write(
	out,
	JSON.stringify({ run: RUN, results, pageErrors }, null, 2),
);
console.log(`\n汇总（${out}）`);
for (const r of results)
	console.log(`${r.id} ${r.verdict} (${r.seconds.toFixed(1)}s)`);
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
