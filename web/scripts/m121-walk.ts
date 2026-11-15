#!/usr/bin/env bun
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
/**
 * M12.1 真机走查（W1–W7）：在 dev 部署上新建一个 sandbox 线程（provider local、真实模型），逐项操作 M12.1 新做的交互界面，
 * 每个画面截 1280×900 亮色 + 暗色，关键画面再加 390×844；每项打印 `W<n> PASS|FAIL (秒)` 与证据行，结尾删除本次建的 orb。
 *
 * 用法（web/ 目录）：bun run walk:m121 [-- --only W1,W3] [-- --help]
 * 环境变量：ORYN_URL（缺省 http://localhost:7600）、ORYN_ROOT、ORYN_TOKEN、ORYN_CREDENTIALS（缺省 .dev/credentials.json）；
 *   M121_PROJECT（缺省 sandbox）、M121_PROVIDER（缺省 local）、M121_SHOTS（缺省 /tmp/oryn-shots/m121）、CHROMIUM。
 *
 * 固定布置：
 *   - 一个线程走到底：`oryn thread new --project sandbox --provider local "只回复 ok"`，等到 idle；
 *     dev 的 orb 闲置 30 秒就冻结，每个场景开头（以及要用到 agent 的步骤前）先 `POST orbs/{id}/resume` 并等 `get_state` 可答。
 *   - agent 侧设置（只改本次 orb 内的 ~/.omp/agent/config.yml，orb 结尾删除）：`btw.inject = true`（W3 的 inject 卡）、
 *     `checkpoint.enabled = true`（W6 的 xd://checkpoint / rewind）。orb 唤醒时控制面会重新同步配置，所以每次唤醒后重设。
 *   - 浏览器：照 m12-accept（playwright-core + /usr/bin/chromium、Bearer 头 + 网页会话 Cookie），另加 SwiftShader（玻璃模糊）、
 *     reducedMotion、剪贴板权限；明暗经 `emulateMedia({colorScheme})` 切换（主题缺省跟随系统）。
 *   - 截图文件：$M121_SHOTS/<画面>-<宽>-<light|dark>.png；失败附 <W>-FAIL-1280-light.png；汇总写 results.json。
 */
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Locator,
	type Page,
	type WebSocket,
} from "playwright-core";

const IDS = ["W1", "W2", "W3", "W4", "W5", "W6", "W7"] as const;
const USAGE = `用法：bun scripts/m121-walk.ts [--only W1,W3,…] [--help]
  场景：${IDS.join(" ")}
  环境变量见文件头注释（ORYN_URL、ORYN_CREDENTIALS、M121_PROJECT、M121_PROVIDER、M121_SHOTS、CHROMIUM）。`;

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
const SHOTS = process.env.M121_SHOTS ?? "/tmp/oryn-shots/m121";
const PROJECT = process.env.M121_PROJECT ?? "sandbox";
const PROVIDER = process.env.M121_PROVIDER ?? "local";
const DB = `${ROOT}/.dev/data/oryn.db`;
const ORYN = `${ROOT}/bin/oryn`;
const WEB_STATE = `${ROOT}/.dev/e2e/state.json`;
mkdirSync(SHOTS, { recursive: true });

const RUN = Math.random().toString(36).slice(2, 6);
const BOX = 'textarea[aria-label="消息"]';

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const isRecord = (v: unknown): v is Record<string, unknown> =>
	typeof v === "object" && v !== null && !Array.isArray(v);
const clip = (s: string, n = 160) =>
	s.length > n ? `${s.slice(0, n)}…（共 ${s.length} 字）` : s;
const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();

interface CliRes {
	code: number;
	out: string;
	stdout: string;
	stderr: string;
}

async function cli(args: string): Promise<CliRes> {
	const p = Bun.spawn(
		["bash", "-c", `ORYN_CREDENTIALS=${q(CREDS)} ${q(ORYN)} ${args}`],
		{ cwd: ROOT, stdout: "pipe", stderr: "pipe" },
	);
	const [stdout, stderr, code] = await Promise.all([
		new Response(p.stdout).text(),
		new Response(p.stderr).text(),
		p.exited,
	]);
	return { code, out: `${stdout}${stderr}`.trim(), stdout, stderr };
}

/** `oryn thread rpc <id> <frame>`：应答 JSON 的 `data`（失败时为 null，原文在 out）。 */
async function rpc(tid: string, frame: Record<string, unknown>) {
	const r = await cli(`thread rpc ${tid} ${q(JSON.stringify(frame))}`);
	let data: unknown = null;
	try {
		const v = JSON.parse(r.stdout) as unknown;
		if (isRecord(v)) data = v.data ?? null;
	} catch {
		// 非 JSON（被拒时只有 stderr）
	}
	return { ...r, data };
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

async function api(method: string, path: string, body?: unknown) {
	const res = await fetch(`${BASE}${path}`, {
		method,
		headers: {
			Authorization: `Bearer ${TOKEN}`,
			"X-Oryn-CSRF": "1",
			...(body !== undefined ? { "Content-Type": "application/json" } : {}),
		},
		body: body !== undefined ? JSON.stringify(body) : undefined,
	});
	return { status: res.status, text: await res.text() };
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

const results: {
	id: string;
	verdict: "PASS" | "FAIL";
	seconds: number;
	evidence: string[];
}[] = [];
const shotFiles: string[] = [];
const cleanupNotes: string[] = [];
let evidence: string[] = [];
let softFails = 0;
const note = (s: string) => {
	evidence.push(s);
	console.log(`    · ${s}`);
};
/** 硬断言：不成立就结束本场景。 */
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};
/** 软断言：记下失败继续走（场景结尾判 FAIL），好把后面的画面也截下来。 */
const expect = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) softFails++;
	return ok;
};
/** 观察到的产品问题（不改判场景结果，汇总时单列）。 */
const issues: string[] = [];
let current = "";
const issue = (s: string) => {
	issues.push(`${current}：${s}`);
	note(`⚠ 产品问题：${s}`);
};

// ---------------------------------------------------------------- 线程与 orb

let tid = "";
let orbId = "";
let settingsApplied = false;

interface ThreadRow {
	state: string;
	last_seq: number;
	orb_id: string | null;
}
const threadRow = (id: string) =>
	one<ThreadRow>(
		`select state, last_seq, orb_id from threads where id='${id}'`,
	);
const orbState = (orb: string) =>
	one<{ state: string }>(`select state from orbs where id='${orb}'`)?.state ??
	"?";
const lastSeq = () => threadRow(tid)?.last_seq ?? 0;
const assistantsAfter = (beforeSeq: number) =>
	one<{ n: number }>(
		`select count(*) n from entries where thread_id='${tid}' and seq>${beforeSeq} and type='message' and instr(cast(payload as text), '"role":"assistant"')>0`,
	)?.n ?? 0;
const userMsgsAfter = (beforeSeq: number) =>
	one<{ n: number }>(
		`select count(*) n from entries where thread_id='${tid}' and seq>${beforeSeq} and type='message' and instr(cast(payload as text), '"role":"user"')>0`,
	)?.n ?? 0;

const SETTLED: Record<string, true> = {
	idle: true,
	awaiting_input: true,
	error: true,
};

/**
 * 等 beforeSeq 之后出现助手消息且线程静止，返回状态。`idleOnly`：只认 idle（有待答 UI 时线程整回合都是
 * awaiting_input，不能拿它当回合结束）。
 */
async function waitTurn(
	beforeSeq: number,
	timeoutMs = 600_000,
	idleOnly = false,
) {
	return waitFor(
		`线程 ${tid} 回合结束`,
		() => {
			const r = threadRow(tid);
			if (!r || r.last_seq <= beforeSeq) return undefined;
			if (
				idleOnly ? r.state !== "idle" && r.state !== "error" : !SETTLED[r.state]
			)
				return undefined;
			return assistantsAfter(beforeSeq) ? r.state : undefined;
		},
		timeoutMs,
		1000,
	);
}

/**
 * 线程不在跑回合（后台任务结束送达等会自己起回合）。DB 状态卡在 working 时（停掉后台任务后控制面不再收到
 * state 帧，pendingAsync 不复位）退而看 Core 的 get_state：连续静止且没有待完成的异步工作就放行，并记一笔。
 */
async function waitQuiet(timeoutMs = 300_000) {
	let coreQuiet = 0;
	await waitFor(
		`线程 ${tid} 静止`,
		async () => {
			if (SETTLED[threadRow(tid)?.state ?? ""] === true) return true;
			const r = await rpc(tid, { type: "get_state" });
			const s = isRecord(r.data) ? r.data : {};
			coreQuiet =
				s.isSettled === true &&
				s.isStreaming !== true &&
				s.hasPendingAsyncWork !== true
					? coreQuiet + 1
					: 0;
			if (coreQuiet < 5) return false;
			note(
				`线程在库里仍是 ${threadRow(tid)?.state}，但 Core get_state 已静止且 hasPendingAsyncWork=false，按静止继续`,
			);
			return true;
		},
		timeoutMs,
		2000,
	);
}

async function applySettings() {
	for (const [path, value] of [
		["btw.inject", true],
		["checkpoint.enabled", true],
	] as const) {
		const r = await rpc(tid, { type: "set_setting", path, value });
		if (r.code !== 0) throw new Error(`set_setting ${path} 失败：${r.out}`);
	}
	settingsApplied = true;
}

/** orb 冻结了就唤醒，并等 agent 可答；唤醒后重设 agent 侧设置。 */
async function ensureAwake() {
	const st = orbState(orbId);
	let resumed = false;
	if (st !== "ready") {
		if (st === "frozen" || st === "stopped" || st === "failed") {
			const r = await api("POST", `/api/v1/orbs/${orbId}/resume`);
			console.log(
				`    [唤醒] orb ${orbId} 为 ${st}，POST resume → ${r.status}`,
			);
			resumed = true;
		}
		await waitFor(
			`orb ${orbId} ready`,
			() => orbState(orbId) === "ready",
			300_000,
			1000,
		);
	}
	await waitFor(
		"agent 可答 get_state",
		async () => (await rpc(tid, { type: "get_state" })).code === 0,
		180_000,
		2000,
	);
	if (resumed || !settingsApplied) await applySettings();
}

async function ensureThread() {
	if (tid) return;
	const out = await cli(
		`thread new --project ${q(PROJECT)} --provider ${q(PROVIDER)} --title ${q(`m121 ${RUN} walk`)} ${q("只回复 ok")}`,
	);
	if (out.code !== 0) throw new Error(`oryn thread new 失败：${out.out}`);
	tid = out.stdout.trim().split("\n").at(-1) ?? "";
	orbId = await waitFor(
		`线程 ${tid} 分到 orb`,
		() => threadRow(tid)?.orb_id ?? undefined,
		120_000,
	);
	await waitTurn(0, 1_200_000);
	await ensureAwake();
	const state = await rpc(tid, { type: "get_state" });
	const model = isRecord(state.data) ? state.data.model : undefined;
	console.log(
		`    [准备] 线程 ${tid}（项目 ${PROJECT}，orb ${orbId}，模型 ${isRecord(model) ? `${model.provider}/${model.id}` : "?"}）`,
	);
}

/** 最后一条 assistant 消息的文本。 */
async function lastAssistantText(): Promise<string> {
	const r = await api(
		"GET",
		`/api/v1/threads/${tid}/entries?after=${Math.max(0, lastSeq() - 40)}&limit=500`,
	);
	const p = JSON.parse(r.text) as {
		entries: { type: string; payload: Record<string, unknown> }[];
	};
	for (const e of [...p.entries].reverse()) {
		const m = e.type === "message" ? e.payload.message : undefined;
		if (!isRecord(m) || m.role !== "assistant") continue;
		const c = m.content;
		if (typeof c === "string") return c;
		if (Array.isArray(c))
			return c
				.map((x) => (isRecord(x) && x.type === "text" ? String(x.text) : ""))
				.join("")
				.trim();
	}
	return "";
}

/** beforeSeq 之后 assistant 发出的工具调用（名字 + 参数摘要）。 */
async function toolCallsAfter(beforeSeq: number): Promise<string[]> {
	const r = await api(
		"GET",
		`/api/v1/threads/${tid}/entries?after=${beforeSeq}&limit=500`,
	);
	const p = JSON.parse(r.text) as {
		entries: { type: string; payload: Record<string, unknown> }[];
	};
	const out: string[] = [];
	for (const e of p.entries) {
		const m = e.type === "message" ? e.payload.message : undefined;
		if (!isRecord(m) || m.role !== "assistant" || !Array.isArray(m.content))
			continue;
		for (const c of m.content)
			if (isRecord(c) && c.type === "toolCall")
				out.push(`${String(c.name)} ${clip(JSON.stringify(c.arguments), 140)}`);
	}
	return out;
}

/** 发一条消息（CLI `thread send`），不等回合结束；返回发出前的 last_seq。 */
async function send(msg: string) {
	await ensureAwake();
	await waitQuiet();
	const before = lastSeq();
	const r = await cli(`thread send ${tid} ${q(msg)}`);
	if (r.code !== 0) throw new Error(`thread send 失败：${r.out}`);
	return before;
}

/** 发一条消息并等回合结束到 idle；返回发出前的 last_seq。 */
async function say(msg: string, timeoutMs = 600_000) {
	const before = await send(msg);
	const st = await waitTurn(before, timeoutMs, true);
	note(`回合结束于 ${st}`);
	return before;
}

// ---------------------------------------------------------------- 浏览器

let browser: Browser | undefined;
let bctx: BrowserContext | undefined;
let page: Page | undefined;

async function makeContext(): Promise<BrowserContext> {
	browser ??= await chromium.launch({
		executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
		headless: true,
		args: ["--no-sandbox", "--use-gl=angle", "--use-angle=swiftshader"],
	});
	const c = await browser.newContext({
		locale: "zh-CN",
		viewport: { width: 1280, height: 900 },
		colorScheme: "light",
		reducedMotion: "reduce",
		extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
	});
	await c.grantPermissions(["clipboard-read", "clipboard-write"], {
		origin: BASE,
	});
	if (existsSync(WEB_STATE))
		await c.addCookies(
			(
				JSON.parse(readFileSync(WEB_STATE, "utf8")) as {
					cookies: Parameters<BrowserContext["addCookies"]>[0];
				}
			).cookies,
		);
	return c;
}

/** 页面未捕获的异常与 console.error（场景结尾把新增的写进证据）。 */
const pageErrors: string[] = [];
let pageErrorsSeen = 0;

/** 线程页（整个走查共用一页；不在线程页、被关掉或白屏了就重开）。 */
async function threadPage(reload = false): Promise<Page> {
	bctx ??= await makeContext();
	if (!page || page.isClosed()) {
		page = await bctx.newPage();
		page.on("pageerror", (e) =>
			pageErrors.push(
				`pageerror：${oneLine(`${e.message} ${e.stack ?? ""}`).slice(0, 600)}`,
			),
		);
		page.on("console", (m) => {
			if (m.type() === "error")
				pageErrors.push(`console.error：${oneLine(m.text()).slice(0, 600)}`);
		});
	}
	const onThread = page.url().includes(`/t/${tid}`);
	const alive =
		onThread &&
		(await page
			.locator(BOX)
			.waitFor({ state: "visible", timeout: 3000 })
			.then(() => true)
			.catch(() => false));
	if (reload || !alive)
		await page.goto(`${BASE}/t/${tid}`, { waitUntil: "domcontentloaded" });
	await page.locator(BOX).waitFor({ state: "visible", timeout: 60_000 });
	return page;
}

/**
 * 确认条卸载即白屏的根因（2026-11 走查定位）：ConfirmStrip 的 useEffect 隐式返回了 scrollIntoView 的返回值；
 * Chromium 156 起 scrollIntoView 返回 Promise，React 把它当清理函数，确认条卸载时调用它抛 TypeError。
 */
const CONFIRM_STRIP_CAUSE =
	"根因：ConfirmStrip 的 useEffect 隐式返回 scrollIntoView 的返回值（web/src/components/thread/reports/parts.tsx:162）；" +
	"Chromium 156 的 scrollIntoView 返回 Promise（本机 /usr/bin/chromium 实测 [object Promise]），React 把它当清理函数，确认条卸载（确认、保留或关对话框）时调用 → TypeError「l is not a function」，" +
	"应用没有错误边界，整页白屏，要刷新才能恢复。同一组件还用在目标「放弃」（web/src/components/thread/Dialogs.tsx:1005）和用量「使用重置额度」（web/src/components/thread/reports/UsageReport.tsx:320）。";

/** 页面白屏（输入框没了）时记成产品问题，附新增的页面错误。 */
async function crashCheck(p: Page, after: string, cause = "") {
	let crashed = false;
	for (let i = 0; i < 12 && !crashed; i++) {
		await sleep(500);
		crashed = (await p.locator(BOX).count()) === 0;
	}
	if (!crashed) return false;
	const errs = pageErrors.slice(pageErrorsSeen);
	issue(
		`${after}后整页白屏（React 树卸载，输入框都没了）。页面错误：${clip(errs.join(" ｜ "), 240) || "（没有捕获到）"}。${cause}`,
	);
	return true;
}

const dialogs = (p: Page) => p.locator('[role="dialog"]');
const dialog = (p: Page, name: string) =>
	p.getByRole("dialog", { name, exact: true });

/** 关掉所有对话框、忽略悬着的 agent 提问、清空输入框。 */
async function settle(p: Page) {
	for (let i = 0; i < 4 && (await dialogs(p).count()) > 0; i++) {
		await p.keyboard.press("Escape");
		await sleep(250);
	}
	const dismiss = p.getByRole("button", { name: "忽略", exact: true });
	for (let i = 0; i < 3 && (await dismiss.count()) > 0; i++) {
		await dismiss.first().click();
		await sleep(500);
	}
	await p.locator(BOX).fill("");
}

/**
 * 截 1280×900 亮 / 暗；给了 `redo` 时再截 390×844 亮 / 暗。跨过 compact 断点时线程页换布局、输入坞重新挂载，
 * 输入框文字、错误行、卡片展开这类组件内状态会丢，所以换宽度后先跑 `redo` 把画面摆回来（换回 1280 后也跑一次）。
 */
async function shots(p: Page, name: string, redo?: () => Promise<void>) {
	const sizes: [number, number][] = redo
		? [
				[1280, 900],
				[390, 844],
			]
		: [[1280, 900]];
	for (const [w, h] of sizes) {
		if (w !== 1280 && redo) {
			await p.setViewportSize({ width: w, height: h });
			await sleep(700);
			// 摆不回来也照截，图上就是用户在这个宽度看到的样子
			await redo().catch((e: unknown) =>
				expect(
					false,
					`${name} 在 ${w} 宽度下复原画面失败：${oneLine(String(e)).slice(0, 200)}`,
				),
			);
			await sleep(300);
		}
		for (const scheme of ["light", "dark"] as const) {
			await p.emulateMedia({ colorScheme: scheme });
			await sleep(350);
			const f = `${SHOTS}/${name}-${w}-${scheme}.png`;
			await p.screenshot({ path: f });
			shotFiles.push(f);
		}
		if (w !== 1280 && redo) {
			await p.setViewportSize({ width: 1280, height: 900 });
			await sleep(700);
			await redo().catch((e: unknown) =>
				expect(
					false,
					`${name} 回到 1280 后复原画面失败：${oneLine(String(e)).slice(0, 200)}`,
				),
			);
		}
	}
	await p.emulateMedia({ colorScheme: "light" });
	note(`截图 ${name}-{1280${redo ? ",390" : ""}}-{light,dark}.png`);
}

/** 对话框不在就用命令重新打开（给 `shots` 的 redo 用）。 */
async function ensureDialog(p: Page, name: string, cmd: string) {
	if (await dialog(p, name).isVisible()) return;
	await submit(p, cmd);
	await dialog(p, name).waitFor({ state: "visible", timeout: 15_000 });
	await sleep(800);
}

/** 在输入框里提交一条斜杠命令：先关掉命令菜单（免得 Enter 被菜单选项接走），再 Enter。 */
async function submit(p: Page, text: string) {
	const box = p.locator(BOX);
	await box.click();
	await box.fill(text);
	await sleep(300);
	const menuOpen =
		(await p.locator('[role="listbox"][aria-label="命令"]').count()) > 0 ||
		(await p.getByTestId("menu-usage").count()) > 0;
	if (menuOpen) {
		await box.press("Escape");
		await sleep(150);
	}
	await box.press("Enter");
}

/** 输入坞的错误行（`p[role=alert]`）文本；没有返回空串。 */
async function composerAlert(p: Page, timeoutMs = 8000): Promise<string> {
	const alert = p.locator("p[role=alert]").last();
	try {
		await alert.waitFor({ state: "visible", timeout: timeoutMs });
		return oneLine((await alert.textContent()) ?? "");
	} catch {
		return "";
	}
}

const notices = (p: Page, kind: string) => p.locator(`[data-notice="${kind}"]`);

async function textOf(l: Locator): Promise<string> {
	// 要渲染后的文字：隐藏元素不算、块与块之间有换行（证据行与包含判断都按用户看到的文字）
	// biome-ignore lint/performance/useDomNodeTextContent: 同上
	return oneLine((await l.innerText().catch(() => "")) ?? "");
}

// ---------------------------------------------------------------- 场景

/** W1 斜杠菜单。 */
async function w1() {
	const p = await threadPage();
	await settle(p);
	const box = p.locator(BOX);
	const options = p.locator(
		'[role="listbox"][aria-label="命令"] [role=option]',
	);
	const labels = async () =>
		p.evaluate(() =>
			[
				...document.querySelectorAll(
					'[role="listbox"][aria-label="命令"] [role=option]',
				),
			].map((b) => ({
				label: b.querySelector("span > span")?.textContent ?? "",
				hint: b.querySelector('[data-testid="menu-hint"]')?.textContent ?? null,
			})),
		);
	// omp 公布的命令随 get_available_commands 到达；没到就重载一次
	let list: { label: string; hint: string | null }[] = [];
	for (let attempt = 0; attempt < 2; attempt++) {
		await box.click();
		await box.fill("/");
		await options.first().waitFor({ state: "visible", timeout: 15_000 });
		try {
			list = await waitFor(
				"菜单出现 omp 公布的命令（/changelog）",
				async () => {
					const l = await labels();
					return l.some((x) => x.label === "/changelog") ? l : undefined;
				},
				20_000,
			);
			break;
		} catch (e) {
			if (attempt === 1) throw e;
			note("菜单里还没有 omp 公布的命令，重载线程页");
			await ensureAwake();
			await threadPage(true);
		}
	}
	const names = list.map((x) => x.label);
	note(`菜单共 ${names.length} 项：${clip(names.join(" "), 400)}`);
	const hidden = [
		"/hotkeys",
		"/extensions",
		"/settings",
		"/login",
		"/logout",
		"/delete",
		"/local",
		"/debug",
		"/exit",
		"/quit",
		"/move",
		"/wt",
		"/stats",
		"/export",
	];
	const leaked = hidden.filter((h) => names.includes(h));
	expect(
		leaked.length === 0,
		`TUI 专属 / 被拦的命令不在菜单里（检查 ${hidden.join(" ")}）${leaked.length ? `；出现了 ${leaked.join(" ")}` : ""}`,
	);
	expect(names.includes("/branch"), "/branch 在菜单里（客户端接管）");
	for (const c of ["/tree", "/btw", "/goal", "/jobs", "/usage", "/model"])
		expect(names.includes(c), `${c} 在菜单里`);
	const hintOf = (l: string) => list.find((x) => x.label === l)?.hint ?? null;
	for (const [l, want] of [
		["/goal", "[objective | subcommand]"],
		["/btw", "[question]"],
		["/changelog", "[full|last [N]]"],
		["/jobs", "[full|kill <id>|kill all]"],
	] as const)
		expect(
			hintOf(l) === want,
			`${l} 的参数提示为「${want}」（实际「${hintOf(l) ?? "无"}」）`,
		);
	expect(hintOf("/branch") === null, "/branch 没有参数提示");
	// 换宽度后输入坞重新挂载：顺带看草稿是否保住
	let draftLost = false;
	await shots(p, "w1-menu", async () => {
		if ((await box.inputValue()) !== "/") draftLost = true;
		await box.click();
		await box.fill("/");
		await options.first().waitFor({ state: "visible", timeout: 15_000 });
	});
	if (draftLost)
		issue(
			"窗口跨过 compact 断点（1280↔390，折叠屏展开 / 旋转同理）时线程页换布局、输入坞重新挂载，" +
				"未发送的草稿被清空（W7 里输入坞的错误行同样丢失）：Composer 的文字与错误只存在组件内 useState（web/src/components/composer/Composer.tsx:63-64）。" +
				"复现：线程页输入框打 /，把窗口从 1280 缩到 390，输入框变空。截图 w1-menu-390-*.png 由脚本重新输入后才截。",
		);

	// 参数阶段：命令名后加空格 → 子命令 + 底部用法
	await box.fill("/jobs ");
	const usage = p.getByTestId("menu-usage");
	await usage.waitFor({ state: "visible", timeout: 10_000 });
	const usageText = await textOf(usage);
	const subs = (await labels()).map((x) => x.label);
	expect(
		usageText.includes("/jobs") && usageText.includes("kill"),
		`「/jobs 」底部用法：${usageText}`,
	);
	expect(
		subs.includes("full") && subs.includes("kill"),
		`「/jobs 」子命令：${subs.join(" ")}`,
	);
	await shots(p, "w1-args");

	await box.fill("/br");
	await sleep(300);
	const br = (await labels()).map((x) => x.label);
	expect(
		br.includes("/branch"),
		`输入 /br 时列出 /branch（顺序：${br.join(" ")}）`,
	);
	await shots(p, "w1-filter");
	await settle(p);
}

/** W2 报告对话框。 */
async function w2() {
	await ensureAwake();
	const p = await threadPage();
	await settle(p);

	// /usage
	await submit(p, "/usage");
	let d = dialog(p, "用量");
	await d.waitFor({ state: "visible", timeout: 15_000 });
	await waitFor(
		"用量报告加载完",
		async () => !(await textOf(d)).includes("正在向供应商查询用量"),
		90_000,
	);
	const usageText = await textOf(d);
	note(`用量：${clip(usageText, 300)}`);
	expect(!usageText.includes("读取用量失败"), "用量报告读取成功");
	expect(
		usageText.includes("本会话") || usageText.includes("供应商限额"),
		"用量报告有分区（本会话 / 供应商限额）",
	);
	expect(!/"\w+":/.test(usageText), "没有原始键值 JSON");
	await shots(p, "w2-usage", () => ensureDialog(p, "用量", "/usage"));
	await settle(p);

	// /tools：xd:// 挂载设备单独分组
	await submit(p, "/tools");
	d = dialog(p, "当前工具");
	await d.waitFor({ state: "visible", timeout: 15_000 });
	await waitFor(
		"工具报告加载完",
		async () => !(await textOf(d)).includes("正在读取工具列表"),
		60_000,
	);
	const toolsText = await textOf(d);
	note(`工具：${clip(toolsText, 300)}`);
	expect(toolsText.includes("xd:// 挂载设备"), "有「xd:// 挂载设备」分组");
	expect(
		toolsText.includes("xd://lsp"),
		"挂载设备以 xd://<名字> 列出（xd://lsp）",
	);
	expect(toolsText.includes("内置"), "有「内置」分组");
	await shots(p, "w2-tools");
	const xdHeading = d.getByText("xd:// 挂载设备", { exact: true }).first();
	await xdHeading.scrollIntoViewIfNeeded().catch(() => {});
	await sleep(300);
	await shots(p, "w2-tools-xd");
	await settle(p);

	// /session
	await submit(p, "/session");
	d = dialog(p, "会话统计");
	await d.waitFor({ state: "visible", timeout: 15_000 });
	await waitFor(
		"会话统计加载完",
		async () => !(await textOf(d)).includes("正在读取会话统计"),
		60_000,
	);
	const sessText = await textOf(d);
	note(`会话：${clip(sessText, 300)}`);
	expect(
		sessText.includes("会话 ID") && sessText.includes("消息"),
		"会话统计有会话 ID 与消息分项",
	);
	await shots(p, "w2-session");
	await settle(p);

	// /jobs：先让 agent 起后台 sleep。后台任务跑着时线程不回 idle（上一轮实测要等任务结束回合才算完），
	// 所以不等回合结束，直接轮询 get_jobs
	const before = await send(
		"这是一次界面走查。请调用一次 bash 工具，命令为 `sleep 300`，并把 bash 的 async 参数设为 true 让它在后台运行。" +
			"启动后立刻只回复「已启动」，不要等待它结束，也不要调用其它工具。",
	);
	const jobList = await waitFor(
		"get_jobs 出现运行中的任务",
		async () => {
			const r = await rpc(tid, { type: "get_jobs" });
			const list =
				isRecord(r.data) && Array.isArray(r.data.jobs)
					? (r.data.jobs as Record<string, unknown>[])
					: [];
			return list.some((j) => j.status === "running") ? list : undefined;
		},
		240_000,
		2000,
	).catch(() => [] as Record<string, unknown>[]);
	note(`agent 工具调用：${(await toolCallsAfter(before)).join(" | ") || "无"}`);
	const running = jobList.find((j) => j.status === "running");
	check(
		running !== undefined,
		`get_jobs 有运行中的任务（${jobList.map((j) => `${String(j.id)}:${String(j.status)}`).join(" ") || "空"}）`,
	);
	note(`任务运行中时线程状态：${threadRow(tid)?.state ?? "?"}`);
	await ensureAwake();
	await submit(p, "/jobs");
	d = dialog(p, "后台任务");
	await d.waitFor({ state: "visible", timeout: 15_000 });
	const stop = d.getByRole("button", { name: "停止", exact: true }).first();
	await stop.waitFor({ state: "visible", timeout: 20_000 });
	const jobsText = await textOf(d);
	note(`后台任务：${clip(jobsText, 300)}`);
	expect(jobsText.includes("运行中"), "运行中行带「运行中」徽标");
	expect(jobsText.includes("每 2 秒自动刷新"), "运行中时显示自动刷新");
	expect(await stop.isVisible(), "运行中行有「停止」按钮");
	await shots(p, "w2-jobs", async () => {
		await ensureDialog(p, "后台任务", "/jobs");
		await stop.waitFor({ state: "visible", timeout: 20_000 });
	});
	await stop.click();
	const confirm = d.getByRole("button", { name: "停止任务", exact: true });
	await confirm.waitFor({ state: "visible", timeout: 5000 });
	expect(
		(await textOf(d)).includes("停止后任务不会再继续"),
		"点停止后先出确认条（说明 + 保留 / 停止任务）",
	);
	await shots(p, "w2-jobs-confirm");
	await confirm.click();
	if (
		await crashCheck(p, "/jobs 对话框里点「停止任务」", CONFIRM_STRIP_CAUSE)
	) {
		await shots(p, "w2-jobs-crash");
		expect(false, "停止任务后对话框正常刷新（没有白屏）");
		// 重载后再开 /jobs 看任务落到什么状态
		await threadPage(true);
		await submit(p, "/jobs");
		await d.waitFor({ state: "visible", timeout: 15_000 });
	}
	const stopped = await waitFor(
		"任务变为非运行",
		async () => {
			const t = await textOf(d);
			const settled =
				(await stop.count()) === 0 &&
				!t.includes("正在停止") &&
				/已停止|已完成|失败/.test(t);
			return settled ? t : undefined;
		},
		30_000,
	).catch(() => "");
	expect(stopped !== "", `对话框里任务变为非运行：${clip(stopped, 200)}`);
	const after = await rpc(tid, { type: "get_jobs" });
	const st =
		isRecord(after.data) && Array.isArray(after.data.jobs)
			? (after.data.jobs as Record<string, unknown>[]).find(
					(j) => j.id === running?.id,
				)?.status
			: undefined;
	expect(st !== "running", `get_jobs 里该任务状态为 ${String(st)}`);
	await shots(p, "w2-jobs-stopped");
	await settle(p);
	// 停掉后台任务后线程状态要回到 idle
	await sleep(20_000);
	const dbState = threadRow(tid)?.state ?? "?";
	const core = await rpc(tid, { type: "get_state" });
	const pending = isRecord(core.data) ? core.data.hasPendingAsyncWork : "?";
	note(
		`停止 20 秒后：库里线程状态 ${dbState}；Core hasPendingAsyncWork=${String(pending)}`,
	);
	if (dbState === "working" && pending === false)
		issue(
			"经 /jobs 停掉 agent 的后台任务后，线程一直停在 working（侧栏转圈、后续等静止的操作全被挡住），而 Core get_state 已是 isSettled=true、hasPendingAsyncWork=false。" +
				"控制面的 pendingAsync 只在收到 state 帧时更新（internal/threads/live.go:271，Status 里 live.go:302 据此判 working），取消后没有新的 state 帧来复位。" +
				"复现：让 agent 用 async bash 跑 sleep 300，打开 /jobs 点停止→停止任务，之后线程不再回 idle（本次观察超过 15 分钟）。",
		);
}

/** W3 btw 旁路提问。 */
async function w3() {
	await ensureAwake();
	await waitQuiet();
	// 抓 WS 下行的 btw 帧（btw_delta 条数、btw_record 里的 inject 状态），和界面画出来的状态对照
	const frames: { at: number; kind: string; inject?: string }[] = [];
	const onWs = (ws: WebSocket) =>
		ws.on("framereceived", (f) => {
			const s =
				typeof f.payload === "string" ? f.payload : f.payload.toString();
			if (!s.includes('"btw_')) return;
			for (const m of s.matchAll(/"type":"(btw_delta|btw_record)"/g))
				frames.push({ at: Date.now(), kind: m[1] ?? "" });
			const inj = /"inject":"(ready|sending|sent)"/.exec(s)?.[1];
			if (inj) frames.push({ at: Date.now(), kind: "inject", inject: inj });
		});
	const p0 = await threadPage();
	p0.on("websocket", onWs);
	const p = await threadPage(true);
	await settle(p);
	// 记录 inject 卡在界面上经过的状态（sending 可能一闪而过）
	await p.evaluate(() => {
		const w = window as unknown as { __m121Seen: string[] };
		w.__m121Seen = [];
		const add = (k: string) => {
			if (!w.__m121Seen.includes(k)) w.__m121Seen.push(k);
		};
		const scan = () => {
			const d = document.querySelector('[role="dialog"]');
			if (!d) return;
			// biome-ignore lint/performance/useDomNodeTextContent: 只认看得见的状态文字
			const t = (d as HTMLElement).innerText;
			if (t.includes("回答中")) add("running");
			if (t.includes("正在起草引导")) add("drafting");
			if (
				[...d.querySelectorAll("button")].some(
					(b) => b.textContent?.trim() === "注入主会话",
				)
			)
				add("ready");
			if (t.includes("正在注入")) add("sending");
			if (t.includes("已注入主会话")) add("sent");
		};
		new MutationObserver(scan).observe(document.body, {
			subtree: true,
			childList: true,
			characterData: true,
		});
	});
	const question = "用三句话说说：你在这个线程里能帮我做哪些事？";
	await submit(p, `/btw ${question}`);
	const d = dialog(p, "旁路提问");
	await d.waitFor({ state: "visible", timeout: 20_000 });
	const topic = d.getByRole("region", { name: question });
	await topic.waitFor({ state: "visible", timeout: 20_000 });
	// 流式：回答中时正文长度在涨
	const lens: number[] = [];
	let streamShot = false;
	await waitFor(
		"首轮回答完成",
		async () => {
			const t = await textOf(topic);
			if (t.includes("回答中")) {
				lens.push(t.length);
				if (!streamShot && t.length > (lens[0] ?? 0) + 15) {
					streamShot = true;
					await shots(p, "w3-streaming");
				}
				return undefined;
			}
			return t.length > question.length;
		},
		240_000,
		200,
	);
	const deltas = frames.filter((f) => f.kind === "btw_delta").length;
	const grew = lens.some((x, i) => i > 0 && x > (lens[i - 1] ?? 0));
	note(
		`首轮：WS 收到 btw_delta ${deltas} 帧；界面「回答中」采样 ${lens.length} 次，正文长度 ${lens.slice(0, 10).join("→")}`,
	);
	expect(lens.length > 0, "看到「回答中」状态");
	expect(
		grew || deltas <= 1,
		`回答中正文随 btw_delta 增长（流式）${grew ? "" : "：收到多帧增量但界面正文没变"}`,
	);
	const first = await textOf(topic);
	note(`首轮：${clip(first, 200)}`);
	expect(!first.includes("回答出错"), "首轮没有出错");
	const btwAgain = () => ensureDialog(p, "旁路提问", "/btw");
	await shots(p, "w3-first", btwAgain);

	// 追问一轮：要求起草给主线的引导
	const follow = d.getByLabel("接着这个话题问…");
	await follow.fill(
		"请给主线 agent 起草一条引导：让它下一轮只回复「btw 引导已收到」这几个字。",
	);
	await d.getByRole("button", { name: "追问", exact: true }).click();
	await waitFor(
		"追问开始",
		async () => (await textOf(topic)).includes("2 轮"),
		30_000,
	);
	await waitFor(
		"追问回答完成",
		async () => !(await textOf(topic)).includes("回答中"),
		240_000,
		500,
	);
	const second = await textOf(topic);
	note(`两轮后：${clip(second, 300)}`);
	expect(second.includes("2 轮"), "话题头显示「2 轮」");
	const injectBtn = topic.getByRole("button", {
		name: "注入主会话",
		exact: true,
	});
	const hasInject = await injectBtn
		.waitFor({ state: "visible", timeout: 15_000 })
		.then(() => true)
		.catch(() => false);
	check(
		hasInject,
		"第二轮回答带 inject 卡且可注入（ready）——需 btw.inject 开启且模型写出 <inject> 块",
	);
	await shots(p, "w3-multi-ready", btwAgain);
	const beforeSeq = lastSeq();
	const clickedAt = Date.now();
	await injectBtn.click();
	await topic
		.getByText("已注入主会话")
		.waitFor({ state: "visible", timeout: 60_000 });
	const seen = await p.evaluate(
		() => (window as unknown as { __m121Seen: string[] }).__m121Seen,
	);
	const wire = frames
		.filter((f) => f.kind === "inject" && f.at >= clickedAt)
		.map((f) => `${f.inject}@+${f.at - clickedAt}ms`);
	note(
		`inject：WS 上的状态 ${wire.join(" → ") || "无"}；界面画出过 ${seen.join(" → ")}`,
	);
	expect(seen.includes("ready"), "界面出现过 ready（注入主会话按钮）");
	expect(
		wire.some((x) => x.startsWith("sending")) &&
			wire.some((x) => x.startsWith("sent")),
		"Core 依次下发 sending、sent",
	);
	expect(seen.includes("sent"), "界面最终 sent（已注入主会话）");
	if (!seen.includes("sending"))
		note(
			"界面没画出 sending（正在注入…）：sending 与 sent 两帧的到达间隔见上一行，间隔很短时界面直接落到 sent",
		);
	await shots(p, "w3-sent", btwAgain);
	p.off("websocket", onWs);
	await settle(p);
	const st = await waitTurn(beforeSeq, 300_000).catch(() => "");
	expect(st !== "", `注入后主线起了一回合（${st || "没有"}）`);
	if (st) note(`主线回复：${clip(await lastAssistantText(), 120)}`);
}

/** W4 Goal / Model 对话框。 */
async function w4() {
	await ensureAwake();
	await waitQuiet();
	const p = await threadPage();
	let d: Locator;
	await settle(p);
	// /model
	await submit(p, "/model");
	d = dialog(p, "模型");
	await d.waitFor({ state: "visible", timeout: 15_000 });
	await waitFor(
		"模型列表加载完",
		async () =>
			(await d.locator("button[data-model]").count()) > 0 ||
			(await textOf(d)).includes("没有可用的模型"),
		30_000,
	);
	const n = await d.locator("button[data-model]").count();
	expect(n > 0, `模型列表 ${n} 项`);
	const current = await d
		.locator("button[data-model].font-medium")
		.first()
		.getAttribute("data-model", { timeout: 3000 })
		.catch(() => null);
	note(`当前模型行：${current ?? "未标出"}`);
	expect(current !== null, "当前模型行有选中样式");
	await shots(p, "w4-model", async () => {
		await ensureDialog(p, "模型", "/model");
		await d
			.locator("button[data-model]")
			.first()
			.waitFor({ state: "visible", timeout: 30_000 });
	});
	await d.getByPlaceholder("搜索模型…").fill("zzzz-no-such-model");
	await sleep(300);
	expect(
		(await textOf(d)).includes("没有匹配「zzzz-no-such-model」的模型"),
		"无匹配时的空态文案",
	);
	await shots(p, "w4-model-nomatch");
	await settle(p);
	await submit(p, "/goal");
	d = dialog(p, "目标");
	await d.waitFor({ state: "visible", timeout: 15_000 });
	await waitFor(
		"目标读取完",
		async () => !(await textOf(d)).includes("正在读取目标"),
		30_000,
	);
	expect((await textOf(d)).includes("当前没有目标"), "开始时没有目标");
	await shots(p, "w4-goal-empty");
	// set_goal 会把目标当一条用户消息发出去跑一回合；目标写成做不完的占位，免得 agent 当场用 goal 工具把它结束
	const objective =
		"（走查用占位目标）先不要做任何事：只回复「收到，等待指示」然后停下，不要调用 goal 工具结束或修改这个目标";
	await d.locator("textarea").fill(objective);
	await d.getByLabel("token 预算（可选）").fill("50000");
	const goalSeq = lastSeq();
	await d.getByRole("button", { name: "设定目标", exact: true }).click();
	await d
		.locator("[data-goal-objective]")
		.waitFor({ state: "visible", timeout: 20_000 });
	const setText = await textOf(d);
	note(`设定后：${clip(setText, 300)}`);
	expect(
		setText.includes(objective) && setText.includes("进行中"),
		"当前目标显示目标与「进行中」",
	);
	expect(setText.includes("预算"), "出现「预算」分区");
	await shots(p, "w4-goal-set");

	// 只调预算
	await d.getByLabel("新的 token 预算").fill("80000");
	await sleep(200);
	const preview = await textOf(d);
	expect(preview.includes("预算将改为"), "填新预算后出现预览说明");
	await shots(p, "w4-goal-budget-preview");
	await d.getByRole("button", { name: "更新预算", exact: true }).click();
	const g = await waitFor(
		"get_goal 预算为 80000",
		async () => {
			const r = await rpc(tid, { type: "get_goal" });
			return isRecord(r.data) && r.data.tokenBudget === 80000
				? r.data
				: undefined;
		},
		20_000,
		1000,
	).catch(() => undefined);
	expect(g !== undefined, "预算改为 80000，目标不变");
	if (g)
		expect(g.objective === objective, `目标仍为「${String(g.objective)}」`);
	await sleep(500);
	await shots(p, "w4-goal-budget");

	// 暂停 → 仍可见「继续」。先等 set_goal 发出的那一回合跑完
	await waitTurn(goalSeq, 300_000, true).catch(() => undefined);
	const g1 = await rpc(tid, { type: "get_goal" });
	const st1 = isRecord(g1.data) ? String(g1.data.status) : "?";
	note(
		`暂停前 get_goal：status ${st1}，enabled ${isRecord(g1.data) ? String(g1.data.enabled) : "?"}`,
	);
	const shown = await textOf(
		d.locator("[data-goal-objective]").locator("xpath=.."),
	);
	if (st1 !== "active" && shown.includes("进行中"))
		issue(
			`目标在后台已变为 ${st1}，目标对话框仍显示「进行中」和「暂停」按钮（get_goal 只在对话框自己操作后才失效重取：web/src/components/thread/Dialogs.tsx:886）；` +
				"这时点暂停得到 Core 英文原文「No active goal to pause.」。复现：/goal 设一个 agent 一回合就能完成的目标，保持对话框打开等回合结束，再点暂停。",
		);
	await d.getByRole("button", { name: "暂停", exact: true }).click();
	const resume = d.getByRole("button", { name: "继续", exact: true });
	await resume.waitFor({ state: "visible", timeout: 20_000 });
	const pausedText = await textOf(d);
	expect(pausedText.includes("已暂停"), "徽标为「已暂停」");
	expect(
		pausedText.includes("先继续或放弃它"),
		"设定区换成「先继续或放弃」说明",
	);
	await shots(p, "w4-goal-paused", async () => {
		await ensureDialog(p, "目标", "/goal");
		await resume.waitFor({ state: "visible", timeout: 15_000 });
	});
	await settle(p);
	await submit(p, "/goal");
	d = dialog(p, "目标");
	await d.waitFor({ state: "visible", timeout: 15_000 });
	expect(
		await d
			.getByRole("button", { name: "继续", exact: true })
			.waitFor({ state: "visible", timeout: 15_000 })
			.then(() => true)
			.catch(() => false),
		"重新打开对话框，暂停的目标仍显示且有「继续」",
	);
	await shots(p, "w4-goal-paused-reopen");
	// 收尾：放弃（带确认）
	await d.getByRole("button", { name: "放弃", exact: true }).click();
	const dropConfirm = d.getByRole("button", { name: "放弃目标", exact: true });
	await dropConfirm.waitFor({ state: "visible", timeout: 5000 });
	await shots(p, "w4-goal-drop-confirm");
	await dropConfirm.click();
	if (
		await crashCheck(
			p,
			"目标对话框里点「放弃目标」",
			"根因同 W2 的 ConfirmStrip（web/src/components/thread/reports/parts.tsx:162）。",
		)
	) {
		await shots(p, "w4-goal-drop-crash");
		expect(false, "放弃目标后对话框正常刷新（没有白屏）");
		await threadPage(true);
		await submit(p, "/goal");
		d = dialog(p, "目标");
		await d.waitFor({ state: "visible", timeout: 15_000 });
	}
	expect(
		await d
			.getByText("当前没有目标。")
			.waitFor({ state: "visible", timeout: 15_000 })
			.then(() => true)
			.catch(() => false),
		"放弃后回到「当前没有目标」",
	);
	await settle(p);
}

/** W5 模式与命令输出。 */
async function w5() {
	await ensureAwake();
	await waitQuiet();
	const p = await threadPage();
	await settle(p);
	const speed = async () =>
		(await p
			.locator('[aria-label^="速度："]')
			.first()
			.getAttribute("aria-label", { timeout: 3000 })
			.catch(() => null)) ?? "（无速度读数）";
	const stateOf = async () => {
		const r = await rpc(tid, { type: "get_state" });
		return isRecord(r.data) ? r.data : {};
	};

	// /fast
	const s0 = await stateOf();
	const sp0 = await speed();
	const infos0 = await notices(p, "notice").count();
	await submit(p, "/fast");
	const alert = await composerAlert(p, 4000);
	await sleep(1500);
	const s1 = await stateOf();
	const sp1 = await speed();
	const infos1 = await notices(p, "notice").count();
	note(
		`/fast：fastModeEnabled ${String(s0.fastModeEnabled)} → ${String(s1.fastModeEnabled)}；速度读数「${sp0}」→「${sp1}」；错误行「${alert}」；新提示 ${infos1 - infos0} 条`,
	);
	expect(
		s0.fastModeEnabled !== s1.fastModeEnabled ||
			sp0 !== sp1 ||
			alert !== "" ||
			infos1 > infos0,
		"/fast 有可见反馈（速度读数变化 / 提示 / 错误说明）",
	);
	await shots(p, "w5-fast");
	if (s1.fastModeEnabled === true) {
		await submit(p, "/fast off");
		await sleep(1500);
		note(`/fast off 后速度读数「${await speed()}」`);
	}
	await settle(p);

	// /plan
	await submit(p, "/plan");
	const planOn = p.locator('button[aria-label="计划"][aria-pressed="true"]');
	expect(
		await planOn
			.waitFor({ state: "visible", timeout: 15_000 })
			.then(() => true)
			.catch(() => false),
		"/plan 后模式条出现按下的「计划」",
	);
	const planAlert = await composerAlert(p, 500);
	if (planAlert) note(`/plan 错误行：${planAlert}`);
	await shots(p, "w5-plan");
	await submit(p, "/plan");
	expect(
		await waitFor(
			"计划模式关闭",
			async () => (await planOn.count()) === 0,
			15_000,
		)
			.then(() => true)
			.catch(() => false),
		"再发 /plan 退出计划模式",
	);
	await shots(p, "w5-plan-off");
	await settle(p);

	// 长命令输出：Markdown、折叠、复制
	let out: Locator | undefined;
	for (const cmd of ["/changelog", "/changelog full"]) {
		const n0 = await notices(p, "command_output").count();
		await submit(p, cmd);
		const ok = await waitFor(
			`${cmd} 输出`,
			async () => (await notices(p, "command_output").count()) > n0,
			90_000,
		)
			.then(() => true)
			.catch(() => false);
		if (!ok) {
			note(`${cmd} 没有命令输出；错误行「${await composerAlert(p, 500)}」`);
			continue;
		}
		const last = notices(p, "command_output").last();
		const folded =
			(await last.locator('[data-testid="notice-body"][data-folded]').count()) >
			0;
		note(
			`${cmd}：${folded ? "超高已折叠" : "未折叠"}，${clip(await textOf(last), 160)}`,
		);
		out = last;
		if (folded) break;
	}
	check(out !== undefined, "拿到一条命令输出");
	if (out) {
		await out.scrollIntoViewIfNeeded();
		const body = out.getByTestId("notice-body");
		const md = await body.evaluate(
			(el) => el.querySelectorAll("h1,h2,h3,h4,ul,ol,li,code,strong,a").length,
		);
		expect(md > 0, `正文按 Markdown 渲染（标题 / 列表 / 代码等元素 ${md} 个）`);
		const fold = out.getByRole("button", { name: /展开全部/ });
		const foldable = (await fold.count()) > 0;
		expect(foldable, "长输出有「展开全部」");
		const o = out;
		await shots(p, "w5-output-folded", () => o.scrollIntoViewIfNeeded());
		if (foldable) {
			await fold.click();
			await sleep(400);
			expect(
				(await out.getByRole("button", { name: "收起" }).count()) > 0,
				"展开后按钮变为「收起」",
			);
			await shots(p, "w5-output-expanded");
			await out.getByRole("button", { name: "收起" }).click();
		}
		await out.getByTestId("notice-copy").click();
		await sleep(300);
		const copied = await p
			.evaluate(() => navigator.clipboard.readText())
			.catch((e: unknown) => `（读剪贴板失败：${String(e)}）`);
		expect(copied.length > 100, `复制得到全文 ${copied.length} 字`);
	}
	await settle(p);

	// /omfg：扩展小部件（候选规则 Markdown）+ 存放位置的提问；最后忽略，不保存
	await ensureAwake();
	const warn0 = await notices(p, "notice").count();
	await submit(p, "/omfg 你上一轮的回答太啰嗦了，以后只给要点");
	const widget = p.locator('[data-widget="omfg"]');
	const gotWidget = await widget
		.waitFor({ state: "visible", timeout: 240_000 })
		.then(() => true)
		.catch(() => false);
	if (gotWidget) {
		const wt = await textOf(widget);
		note(`omfg 小部件：${clip(wt, 300)}`);
		expect(wt.includes("TTSR rule"), "小部件给出候选规则全文");
		const md = await widget.evaluate(
			(el) => el.querySelectorAll(".md, h1,h2,h3,ul,li,code,p").length,
		);
		expect(md > 0, `小部件按 Markdown 渲染（${md} 个元素）`);
		let widgetLost = false;
		await shots(p, "w5-omfg-widget", async () => {
			if ((await widget.count()) === 0) widgetLost = true;
			else await widget.scrollIntoViewIfNeeded();
		});
		if (widgetLost)
			issue(
				"/omfg 的候选规则预览（setWidget）和页脚状态行（setStatus）在线程页重新挂载 / 重新订阅后消失，而同一流程的「Save TTSR rule where?」提问还在，用户看不到要保存的是哪条规则。" +
					"控制面快照不保留 setWidget / setStatus（internal/orbdrt/pendingui.go:33 nonPendingMethods），客户端 applySnapshot 又把 statuses / widgets 重置为空（web/src/state/thread-reducer.ts:973）。" +
					"复现：/omfg <吐槽>，出现规则预览后把窗口从 1280 缩到 390，预览与状态行消失、提问卡仍在；刷新页面应同理（[推断]，同一条快照路径）。截图 w5-omfg-widget-390-*.png、w5-omfg-request-*.png。",
			);
	} else {
		expect(
			false,
			`/omfg 没有出现小部件；错误行「${await composerAlert(p, 500)}」`,
		);
	}
	const dismiss = p.getByRole("button", { name: "忽略", exact: true }).last();
	const asked = await dismiss
		.waitFor({ state: "visible", timeout: 240_000 })
		.then(() => true)
		.catch(() => false);
	if (asked) {
		await shots(p, "w5-omfg-request");
		await dismiss.click();
		const res = await waitFor(
			"omfg 结果提示",
			async () => {
				if ((await notices(p, "notice").count()) > warn0)
					return `提示：${await textOf(notices(p, "notice").last())}`;
				const a = await composerAlert(p, 200);
				return a ? `错误行：${a}` : undefined;
			},
			60_000,
		).catch(() => "（没有结果提示）");
		note(`忽略后：${res}`);
		expect(
			res.includes("规则未保存") || res.includes("未保存"),
			"忽略后提示规则未保存",
		);
		await shots(p, "w5-omfg-result");
	} else {
		expect(
			false,
			`/omfg 没有出现提问；错误行「${await composerAlert(p, 500)}」`,
		);
	}
	await settle(p);
}

/** W6 xd:// 设备卡片。 */
async function w6() {
	await ensureAwake();
	const before = await say(
		[
			"这是一次界面走查，请严格按顺序做下面 5 次工具调用，不要调用其它工具，也不要重试；每次失败也继续下一步。",
			'1. 用 write 工具写文件 /tmp/m121-hello.ts，内容为：export const greet = (name: string): string => "hi " + name;',
			'2. 用 write 工具写入路径 xd://lsp，content 为 JSON：{"action":"hover","file":"/tmp/m121-hello.ts","line":1,"symbol":"greet"}',
			'3. 用 write 工具写入路径 xd://nope，content 为 JSON：{"x":1}（这个设备不存在，预期失败）',
			'4. 用 write 工具写入路径 xd://checkpoint，content 为 JSON：{"goal":"m121 走查 checkpoint 卡片"}',
			'5. 用 write 工具写入路径 xd://rewind，content 为 JSON：{"report":"m121 走查：checkpoint 与 rewind 卡片已生成"}',
			"全部做完后用一句话总结。",
		].join("\n"),
		900_000,
	);
	const calls = await toolCallsAfter(before);
	note(`agent 工具调用：${calls.join(" | ") || "无"}`);
	const p = await threadPage(true);
	await settle(p);
	const card = (label: string) =>
		p
			.locator(".tv-card")
			.filter({
				has: p.locator(".tv-name").getByText(label, { exact: true }),
			})
			.last();
	const devices: [string, string, string[], boolean][] = [
		["xd://lsp", "w6-lsp", ["hover", "/tmp/m121-hello.ts", "greet"], true],
		["xd://nope", "w6-nope", ["nope"], false],
		["xd://checkpoint", "w6-checkpoint", ["m121 走查 checkpoint 卡片"], false],
		["xd://rewind", "w6-rewind", ["checkpoint 与 rewind 卡片已生成"], false],
	];
	for (const [label, name, want, mobile] of devices) {
		const c = card(label);
		const present = await c
			.waitFor({ state: "visible", timeout: 15_000 })
			.then(() => true)
			.catch(() => false);
		if (!present && label === "xd://rewind") {
			// rewind 把叶子移回 checkpoint：它自己的调用落在被剪掉的旁支上，转录里换成一行「分支摘要」
			note(
				"⚠ 观察：回合结束后转录里没有 xd://rewind 卡（rewind 调用在被剪掉的旁支上），只剩「分支摘要」行；专属的 rewind 卡只在回合进行中可见",
			);
			const summary = p.getByRole("button", { name: /分支摘要/ }).last();
			if (
				!expect(
					await summary
						.waitFor({ state: "visible", timeout: 5000 })
						.then(() => true)
						.catch(() => false),
					"rewind 之后转录里有「分支摘要」",
				)
			)
				continue;
			await summary.scrollIntoViewIfNeeded();
			await shots(p, "w6-rewind-summary-collapsed");
			await summary.click();
			await sleep(500);
			const st = await textOf(summary.locator("xpath=.."));
			note(`分支摘要展开：${clip(st, 300)}`);
			expect(
				want.every((w) => st.includes(w)),
				"分支摘要里有 rewind 的报告原文",
			);
			await shots(p, "w6-rewind-summary-expanded");
			await settle(p);
			continue;
		}
		if (!expect(present, `转录里有 ${label} 卡片`)) continue;
		await c.scrollIntoViewIfNeeded();
		await sleep(200);
		const head = c.locator(".tv-head");
		const status = await c
			.locator(".tv-status")
			.getAttribute("class", { timeout: 3000 })
			.catch(() => "");
		note(`${label} 头部：${await textOf(head)}（${status ?? ""}）`);
		await shots(p, `${name}-collapsed`);
		if ((await head.getAttribute("aria-expanded")) !== "true")
			await head.click();
		const body = c.locator(".tv-body");
		await body.waitFor({ state: "visible", timeout: 5000 });
		await c.scrollIntoViewIfNeeded();
		await sleep(300);
		const bt = await textOf(body);
		note(`${label} 展开体：${clip(bt, 300)}`);
		const missing = want.filter((w) => !bt.includes(w));
		expect(
			missing.length === 0,
			`${label} 展开体给出完整参数${missing.length ? `；缺 ${missing.join("、")}` : ""}`,
		);
		await shots(
			p,
			`${name}-expanded`,
			mobile
				? async () => {
						await c.scrollIntoViewIfNeeded();
						if ((await head.getAttribute("aria-expanded")) !== "true")
							await head.click();
						await body.waitFor({ state: "visible", timeout: 5000 });
						await c.scrollIntoViewIfNeeded();
					}
				: undefined,
		);
	}
	await settle(p);
}

/** W7 被拦截的命令。 */
async function w7() {
	await ensureAwake();
	await waitQuiet();
	const p = await threadPage();
	await settle(p);
	const before = lastSeq();
	const cases: [string, string, string, boolean][] = [
		["/move /tmp", "w7-move", "会直接改 agent 的工作目录", true],
		["/wt feature-x", "w7-wt", "会直接改 agent 的工作目录", false],
		["/session delete", "w7-session-delete", "Oryn 管理", true],
		["/hotkeys", "w7-hotkeys", "只能在终端界面", false],
		["/stats", "w7-stats", "/stats 会在 orb 里启动本地仪表盘", false],
		["/export", "w7-export", "/export", false],
	];
	for (const [cmd, name, want, mobile] of cases) {
		await submit(p, cmd);
		const a = await composerAlert(p, 15_000);
		note(`${cmd} → 错误行「${a}」`);
		expect(a.includes(want), `${cmd} 给出明确提示（含「${want}」）`);
		if (cmd === "/session delete" && !a.includes("/session"))
			issue(
				`Web 发 /session delete 被控制面拦下（managed_by_oryn，原文「/session delete is managed by Oryn」），输入坞只显示通用文案「${a}」：` +
					"没点名被拦的命令，也没说为什么（会删掉线程已入库的会话文件）和该怎么做，和客户端先拦的 /move、/stats 的说明不在一个水平。" +
					"原因：describeError 对 managed_by_oryn 一律换成 i18n 固定句（web/src/components/ui/primitives.tsx:674、web/src/i18n/locales/zh-CN.json:844），丢了服务端消息里的命令名。" +
					"复现：线程页输入 /session delete 回车。截图 w7-session-delete-*.png。",
			);
		expect(
			(await p.locator(BOX).inputValue()) === cmd,
			`${cmd} 被拦后命令文本留在输入框`,
		);
		await shots(
			p,
			name,
			mobile
				? async () => {
						await submit(p, cmd);
						await composerAlert(p, 15_000);
					}
				: undefined,
		);
		await settle(p);
	}
	await sleep(1500);
	expect(
		userMsgsAfter(before) === 0 && assistantsAfter(before) === 0,
		`以上命令都没进模型（seq ${before} 之后用户消息 ${userMsgsAfter(before)} 条、助手消息 ${assistantsAfter(before)} 条）`,
	);
	// 绕过 Web 直接发 prompt：控制面同样拦下
	for (const msg of ["/move /tmp", "/session delete"]) {
		const r = await rpc(tid, { type: "prompt", message: msg });
		note(
			`thread rpc prompt「${msg}」→ 退出码 ${r.code}：${clip(oneLine(r.out), 200)}`,
		);
		expect(
			r.code !== 0 && r.out.includes("managed"),
			`控制面拦下 prompt「${msg}」（managed_by_oryn）`,
		);
	}
	expect(userMsgsAfter(before) === 0, "绕过 Web 的 prompt 也没进模型");
}

const TESTS: [string, () => Promise<void>][] = [
	["W1", w1],
	["W3", w3],
	["W4", w4],
	["W5", w5],
	["W6", w6],
	["W7", w7],
	// W2 放最后：停掉后台任务后线程会卡在 working（见 W2 的产品问题），放前面会挡住后面等静止的场景
	["W2", w2],
];

// ---------------------------------------------------------------- 收尾

let cleaned = false;
async function cleanup() {
	if (cleaned) return;
	cleaned = true;
	if (orbId && !["deleting", "deleted", "?"].includes(orbState(orbId))) {
		const r = await api("DELETE", `/api/v1/orbs/${orbId}`).catch((e) => ({
			status: 0,
			text: String(e),
		}));
		cleanupNotes.push(`删除 orb ${orbId} → ${r.status}`);
		await waitFor(
			`${orbId} deleted`,
			() => orbState(orbId) === "deleted",
			180_000,
			1000,
		).catch((e) => cleanupNotes.push(`${orbId} 未删干净：${String(e)}`));
	}
	for (const n of cleanupNotes) console.log(`    [收尾] ${n}`);
	await browser?.close().catch(() => {});
}

for (const s of ["SIGINT", "SIGTERM"] as const)
	process.on(s, () => {
		console.log(`\n收到 ${s}，清理后退出`);
		void cleanup().finally(() => process.exit(130));
	});

// ---------------------------------------------------------------- 主流程

console.log(
	`M12.1 走查 run=${RUN} base=${BASE} 项目 ${PROJECT} shots=${SHOTS}`,
);

try {
	await ensureThread();
	for (const [id, fn] of TESTS) {
		if (ONLY && !ONLY.has(id)) continue;
		evidence = [];
		softFails = 0;
		current = id;
		console.log(`── ${id}`);
		const t0 = Date.now();
		const took = () => Number(((Date.now() - t0) / 1000).toFixed(1));
		let verdict: "PASS" | "FAIL" = "PASS";
		try {
			await fn();
			if (softFails > 0) verdict = "FAIL";
		} catch (e) {
			note(`错误：${e instanceof Error ? e.message : String(e)}`);
			verdict = "FAIL";
		}
		const errs = pageErrors.slice(pageErrorsSeen);
		pageErrorsSeen = pageErrors.length;
		if (errs.length > 0)
			note(`本场景页面错误 ${errs.length} 条：${clip(errs.join(" ｜ "), 800)}`);
		if (verdict === "FAIL" && page && !page.isClosed()) {
			// 失败可能停在手机宽度 / 暗色（shots 中途抛错）
			await page.setViewportSize({ width: 1280, height: 900 }).catch(() => {});
			await page.emulateMedia({ colorScheme: "light" }).catch(() => {});
			const f = `${SHOTS}/${id}-FAIL-1280-light.png`;
			await page
				.screenshot({ path: f })
				.then(() => {
					shotFiles.push(f);
					note(`失败截图 ${f}`);
				})
				.catch(() => {});
			await settle(page).catch(() => {});
		}
		console.log(`${id} ${verdict} (${took()}s)`);
		results.push({ id, verdict, seconds: took(), evidence });
	}
} catch (e) {
	console.error(`准备失败：${e instanceof Error ? e.message : String(e)}`);
	results.push({
		id: "setup",
		verdict: "FAIL",
		seconds: 0,
		evidence: [String(e)],
	});
} finally {
	console.log("── 收尾");
	await cleanup();
}

const out = `${SHOTS}/results.json`;
writeFileSync(
	out,
	JSON.stringify(
		{
			run: RUN,
			thread: tid,
			orb: orbId,
			cleanup: cleanupNotes,
			results,
			issues,
			shots: shotFiles,
		},
		null,
		2,
	),
);
console.log(`\n汇总（${out}）`);
for (const r of results) console.log(`${r.id} ${r.verdict} ${r.seconds}s`);
if (issues.length > 0) {
	console.log("\n观察到的产品问题");
	for (const s of issues) console.log(`- ${s}`);
}
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
