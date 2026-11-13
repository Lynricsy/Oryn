#!/usr/bin/env bun
/**
 * M3 真机验收（计划「验证」T1–T14）：对 dev 部署（orynd + docker provider）逐项执行，
 * 每项打印 `T<n> PASS|FAIL` 与证据。浏览器步骤用本机无头 Chromium（playwright-core），
 * 以设备令牌经 `setExtraHTTPHeaders` 登录，`localStorage["oryn.debug"]="1"` 打开 `window.__orynDebug`。
 *
 * 用法（web/ 目录）：bun run accept:m3 [-- --only T1,T2]
 * 环境变量：
 *   ORYN_URL（默认 http://localhost:7600）、ORYN_ROOT（仓库根，默认本脚本的上两级）
 *   ORYN_TOKEN（默认读 $ORYN_ROOT/.dev/credentials.json 的 token）
 *   M3_THREAD（sandbox 项目、新镜像上的线程；默认 .dev/m3.thread）
 *   T14_THREAD（旧镜像上的线程；默认 .dev/t14.thread）
 *   CHROMIUM（默认 /usr/bin/chromium）、M3_SHOTS（截图目录，默认 /tmp/oryn-shots/m3-accept）
 *   M3_WEB_STATE（默认 .dev/e2e/state.json）：有网页会话 Cookie 时一并注入——Chromium 的下载请求由浏览器进程
 *   发出，不带 `extraHTTPHeaders`，只能靠 Cookie 鉴权（T9 的浏览器下载）

 */
import { createHash } from "node:crypto";
import { createReadStream, existsSync, mkdirSync, readFileSync } from "node:fs";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Page,
} from "playwright-core";

const ROOT = process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`;
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const TOKEN =
	process.env.ORYN_TOKEN ??
	(
		JSON.parse(readFileSync(`${ROOT}/.dev/credentials.json`, "utf8")) as {
			token: string;
		}
	).token;
const readId = (f: string) => readFileSync(`${ROOT}/.dev/${f}`, "utf8").trim();
const THREAD = process.env.M3_THREAD ?? readId("m3.thread");
const T14_THREAD = process.env.T14_THREAD ?? readId("t14.thread");
const SHOTS = process.env.M3_SHOTS ?? "/tmp/oryn-shots/m3-accept";
const DB = `${ROOT}/.dev/data/oryn.db`;
const LOG = `${ROOT}/.dev/orynd.log`;
const RUN = Math.random().toString(36).slice(2, 6);
const REPO = "/home/user/workspace/repo";
mkdirSync(SHOTS, { recursive: true });

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);

function sh(cmd: string): { code: number; out: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], { cwd: ROOT });
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
	};
}

function sql<T = Record<string, unknown>>(q: string): T[] {
	const r = Bun.spawnSync(["sqlite3", "-json", DB, q]);
	const s = r.stdout.toString().trim();
	return s ? (JSON.parse(s) as T[]) : [];
}

function orbOf(threadId: string): string {
	const r = sql<{ orb_id: string }>(
		`select orb_id from threads where id='${threadId}'`,
	)[0];
	if (!r?.orb_id) throw new Error(`线程 ${threadId} 没有 orb`);
	return r.orb_id;
}

function orbState(orbId: string): string {
	return (
		sql<{ state: string }>(`select state from orbs where id='${orbId}'`)[0]
			?.state ?? "?"
	);
}

function threadState(threadId: string): string {
	return (
		sql<{ state: string }>(
			`select state from threads where id='${threadId}'`,
		)[0]?.state ?? "?"
	);
}

/** 在 orb 容器里以 user 身份跑 shell 命令。 */
function dexec(orb: string, cmd: string) {
	// 单引号包裹：$(…) 等留给容器里的 bash 展开
	return sh(`docker exec ${orb} bash -lc '${cmd.replaceAll("'", "'\\''")}'`);
}

async function api(
	method: string,
	path: string,
	body?: unknown,
): Promise<{ status: number; json: unknown; text: string }> {
	const res = await fetch(`${BASE}${path}`, {
		method,
		headers: {
			Authorization: `Bearer ${TOKEN}`,
			"X-Oryn-CSRF": "1",
			...(body !== undefined ? { "Content-Type": "application/json" } : {}),
		},
		body: body !== undefined ? JSON.stringify(body) : undefined,
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

async function sha256File(path: string): Promise<string> {
	const h = createHash("sha256");
	for await (const chunk of createReadStream(path)) h.update(chunk as Buffer);
	return h.digest("hex");
}

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

// ---------------------------------------------------------------- 浏览器

let ctx: BrowserContext;
const pageErrors: string[] = [];

interface WsStats {
	opened: number;
	liveReset: number;
	resized: { terminalId: string; cols: number; rows: number }[];
	lagged: number;
}
const wsStats = new WeakMap<Page, WsStats>();

async function newPage(
	tag: string,
	viewport?: { width: number; height: number },
) {
	const page = await ctx.newPage();
	if (viewport) await page.setViewportSize(viewport);
	const st: WsStats = { opened: 0, liveReset: 0, resized: [], lagged: 0 };
	wsStats.set(page, st);
	page.on("websocket", (ws) => {
		if (!ws.url().includes("/api/v1/ws")) return;
		st.opened++;
		if (process.env.M3_DEBUG_WS) {
			ws.on("framesent", (f) => {
				const p = String(f.payload);
				if (p.includes("resize"))
					console.log(`    [${tag} ws→] ${p.slice(0, 200)}`);
			});
			ws.on("framereceived", (f) => {
				const p = String(f.payload);
				if (p.includes("resize"))
					console.log(`    [${tag} ws←] ${p.slice(0, 200)}`);
			});
		}
		ws.on("framereceived", (f) => {
			const p = typeof f.payload === "string" ? f.payload : "";
			if (
				p.startsWith('{"type":"live.reset"') ||
				p.includes('"type":"live.reset"')
			)
				st.liveReset++;
			if (p.includes('"type":"term.lagged"')) st.lagged++;
			if (p.includes('"type":"term.resized"')) {
				try {
					const m = JSON.parse(p) as {
						terminalId: string;
						cols: number;
						rows: number;
					};
					st.resized.push({
						terminalId: m.terminalId,
						cols: m.cols,
						rows: m.rows,
					});
				} catch {
					// 非单条 JSON
				}
			}
		});
	});
	page.on("response", (r) => {
		if (r.status() >= 400)
			console.log(
				`    [${tag} HTTP ${r.status()}] ${r.request().method()} ${r.url().slice(0, 160)}`,
			);
	});
	page.on("pageerror", (e) => {
		pageErrors.push(`[${tag}] ${e.message}`);
		console.log(`    [${tag} pageerror] ${e.message}`);
	});
	page.on("console", (m) => {
		if (m.type() === "error")
			console.log(`    [${tag} console.error] ${m.text().slice(0, 200)}`);
	});
	return page;
}

interface TermDebug {
	terminalId: string;
	kind: string;
	status: string;
	running: boolean;
	exitCode?: number | null;
	lastSeq: number;
	termCols?: number;
	termRows?: number;
	owner?: boolean;
	cols: number;
	rows: number;
}

const terminals = (page: Page, tid = THREAD) =>
	page.evaluate(
		(t) =>
			(
				window as unknown as {
					__orynDebug?: { terminals(t: string): unknown[] };
				}
			).__orynDebug?.terminals(t) ?? [],
		tid,
	) as Promise<TermDebug[]>;

const termText = (page: Page, id: string, tid = THREAD) =>
	page.evaluate(
		([t, i]) =>
			(
				window as unknown as {
					__orynDebug?: { terminalText(t: string, i: string): string };
				}
			).__orynDebug?.terminalText(t as string, i as string) ?? "",
		[tid, id],
	);

async function openTab(
	page: Page,
	tab: "info" | "changes" | "files" | "terminal",
) {
	const btn = page.locator(`[data-panel-tab=${tab}]`);
	await btn.waitFor({ timeout: 30_000 });
	if ((await btn.getAttribute("aria-selected")) !== "true") await btn.click();
}

/** 打开线程页的 Terminal 标签，必要时点「唤醒」，返回一个 live 的 shell 终端 id。 */
async function openShell(page: Page, tid = THREAD, timeoutMs = 180_000) {
	if (!page.url().includes(`/t/${tid}`)) await page.goto(`${BASE}/t/${tid}`);
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
		timeoutMs,
		500,
	);
}

async function activate(page: Page, id: string) {
	const tab = page.locator(`[data-terminal-tab="${id}"]`);
	if ((await tab.getAttribute("aria-selected")) !== "true") await tab.click();
	const view = page.locator(
		`[data-testid=terminal-view][data-terminal-id="${id}"]`,
	);
	await view.waitFor({ timeout: 10_000 });
	await view.click();
}

async function typeLine(page: Page, id: string, line: string) {
	await activate(page, id);
	await page.keyboard.type(line, { delay: 5 });
	await page.keyboard.press("Enter");
}

async function waitText(
	page: Page,
	id: string,
	pred: (s: string) => boolean,
	what: string,
	timeoutMs = 30_000,
	tid = THREAD,
) {
	return waitFor(
		what,
		async () => {
			const s = await termText(page, id, tid);
			return pred(s) ? s : undefined;
		},
		timeoutMs,
		300,
	);
}

const hasLine = (s: string, line: string) =>
	s.split("\n").some((l) => l.trimEnd() === line);

async function lastAssistant(page: Page) {
	return page.evaluate(() => {
		const els = [...document.querySelectorAll('[data-role="assistant"]')];
		return (els.at(-1) as HTMLElement | undefined)?.textContent ?? "";
	});
}

async function sendPrompt(page: Page, text: string) {
	const box = page.getByLabel("消息", { exact: true });
	await box.fill(text);
	await box.press("Enter");
}

/** 等线程从 working 回到 idle（先看到 working 或给定时间内本来就 idle 的消息数增加）。 */
async function waitTurn(tid: string, timeoutMs = 300_000) {
	await waitFor(
		`线程 ${tid} 进入 working`,
		() => threadState(tid) === "working",
		60_000,
		300,
	).catch(() => undefined);
	return waitFor(
		`线程 ${tid} 回到 idle`,
		() => {
			const s = threadState(tid);
			return s === "idle" || s === "error" || s === "awaiting_input"
				? s
				: undefined;
		},
		timeoutMs,
		500,
	);
}

// ---------------------------------------------------------------- 用例

let pageA: Page | undefined;
async function mainPage() {
	if (!pageA || pageA.isClosed()) pageA = await newPage("A");
	return pageA;
}

async function T1() {
	const page = await mainPage();
	const orb = orbOf(THREAD);
	const id = await openShell(page);
	note(`thread=${THREAD} orb=${orb} shell=${id}`);
	await typeLine(page, id, "echo oryn-$((6*7)); echo $TERM");
	const text = await waitText(
		page,
		id,
		(s) => hasLine(s, "oryn-42") && hasLine(s, "xterm-256color"),
		"终端出现 oryn-42 与 xterm-256color",
	);
	check(hasLine(text, "oryn-42"), "terminalText 含 oryn-42");
	check(hasLine(text, "xterm-256color"), "terminalText 含 xterm-256color");
	const pg = sh(`docker exec ${orb} pgrep -fa 'bash -l'`);
	note(`docker exec pgrep -fa 'bash -l' → ${pg.out.replace(/\n/g, " | ")}`);
	check(pg.code === 0 && pg.out.length > 0, "orb 内有 bash -l 进程");
	await shot(page, "T1-terminal");
}

async function T2() {
	const page = await mainPage();
	const id = await openShell(page);
	const tag = `L${RUN}`;
	await typeLine(
		page,
		id,
		`for i in $(seq 1 400); do echo ${tag}-$i; sleep 0.02; done; echo ${tag}-done`,
	);
	const re = new RegExp(`^${tag}-(\\d+)$`);
	const count = (s: string) =>
		s
			.split("\n")
			.map((l) => re.exec(l.trimEnd())?.[1])
			.filter(Boolean)
			.map(Number);
	await waitText(page, id, (s) => count(s).length >= 150, "输出过半", 60_000);
	const before = (await count(await termText(page, id))).length;
	note(`刷新前已有 ${before} 行`);
	await page.reload();
	const id2 = await openShell(page);
	check(id2 === id, `刷新后终端 id 相同（${id2}）`);
	const text = await waitText(
		page,
		id2,
		(s) => hasLine(s, `${tag}-done`),
		"循环结束",
		60_000,
	);
	const nums = count(text);
	const exact = nums.length === 400 && nums.every((n, i) => n === i + 1);
	const dups = nums.length - new Set(nums).size;
	note(
		`刷新后行数 ${nums.length}，重复 ${dups}，首 ${nums[0]} 末 ${nums.at(-1)}`,
	);
	check(exact, `${tag}-1…${tag}-400 各恰好一次且顺序正确`);
	await shot(page, "T2-after-reload");
}

async function T3() {
	const a = await mainPage();
	await a.setViewportSize({ width: 1400, height: 900 });
	const id = await openShell(a);
	const b = await newPage("B");
	try {
		const idB = await openShell(b);
		check(idB === id, `B 打开同一 shell（${idB}）`);
		const own = async (p: Page) =>
			(await terminals(p)).find((t) => t.terminalId === id)?.owner;
		// B 打开终端即认领尺寸：等 B 的 fit / resize 回显落定，再在 A 里操作
		await waitFor("B 成为尺寸属主", own.bind(null, b), 10_000);
		await sleep(1000);
		await typeLine(a, id, `echo from-a-${RUN}`);
		await waitText(b, id, (s) => hasLine(s, `from-a-${RUN}`), "B 出现 from-a");
		note(`B 出现 from-a-${RUN}`);
		note(`属主：A=${await own(a)} B=${await own(b)}`);
		const stB = wsStats.get(b);
		const n0 = stB?.resized.length ?? 0;
		await a.setViewportSize({ width: 1000, height: 500 });
		const rz = await waitFor(
			"B 收到 term.resized",
			() => (stB && stB.resized.length > n0 ? stB.resized.at(-1) : undefined),
			15_000,
		);
		note(`B 收到 term.resized ${JSON.stringify(rz)}`);
		const sizes = await waitFor(
			"A / B 的 xterm cols 一致",
			async () => {
				const ta = (await terminals(a)).find((t) => t.terminalId === id);
				const tb = (await terminals(b)).find((t) => t.terminalId === id);
				return ta?.termCols &&
					ta.termCols === tb?.termCols &&
					ta.termCols === rz.cols
					? { a: ta.termCols, b: tb.termCols, rows: [ta.termRows, tb.termRows] }
					: undefined;
			},
			10_000,
		);
		check(
			true,
			`xterm cols A=${sizes.a} B=${sizes.b}（rows ${sizes.rows.join("/")}）`,
		);
		await typeLine(b, id, `echo from-b-${RUN}`);
		await waitText(a, id, (s) => hasLine(s, `from-b-${RUN}`), "A 出现 from-b");
		note(`A 出现 from-b-${RUN}`);
		await shot(a, "T3-A");
		await shot(b, "T3-B");
	} finally {
		await b.close();
		await a.setViewportSize({ width: 1400, height: 900 });
	}
}

async function T4() {
	const page = await mainPage();
	await openShell(page);
	await openTab(page, "info");
	const known = new Set((await terminals(page)).map((t) => t.terminalId));
	await sendPrompt(
		page,
		"用 bash 工具并设 pty:true 运行 `read -p 'name? ' n; echo hi-$n`，把输出告诉我",
	);
	const cmd = await waitFor(
		"出现 kind=command 的��端",
		async () =>
			(await terminals(page)).find(
				(t) => t.kind === "command" && !known.has(t.terminalId),
			),
		240_000,
		500,
	);
	note(`新终端 ${cmd.terminalId} kind=${cmd.kind}`);
	const sel = await page
		.locator("[data-panel-tab=terminal]")
		.getAttribute("aria-selected");
	check(sel === "true", "面板自动切到 Terminal");
	await waitText(
		page,
		cmd.terminalId,
		(s) => s.includes("name?"),
		"终端出现 name?",
		60_000,
	);
	note("终端文本含 name?");
	await shot(page, "T4-prompt");
	await typeLine(page, cmd.terminalId, "oryn");
	await waitTurn(THREAD);
	const reply = await waitFor(
		"agent 回复含 hi-oryn",
		async () => {
			const t = await lastAssistant(page);
			return t.includes("hi-oryn") ? t : undefined;
		},
		60_000,
		500,
	);
	note(`agent 回复：${reply.slice(0, 160).replace(/\n/g, " ")}`);
	await shot(page, "T4-reply");
}

async function T5() {
	const page = await mainPage();
	await openShell(page);
	const known = new Set((await terminals(page)).map((t) => t.terminalId));
	await sendPrompt(
		page,
		"用 terminal 工具的 run 启动 `python3 -m http.server 8123`，然后用 list 列出终端。不要停止它。",
	);
	const bg = await waitFor(
		"出现 background 终端",
		async () =>
			(await terminals(page)).find(
				(t) => t.kind === "background" && !known.has(t.terminalId),
			),
		240_000,
		500,
	);
	note(`新终端 ${bg.terminalId} kind=background`);
	await waitTurn(THREAD);
	const card = page.getByText("run python3 -m http.server 8123").first();
	await card.waitFor({ timeout: 15_000 });
	const listCard = await page.getByText("在终端面板查看").count();
	check(
		true,
		`转录 terminal 工具卡片渲染（run 摘要可见，「在终端面板查看」×${listCard}）`,
	);
	await shot(page, "T5-transcript");
	await openTab(page, "terminal");
	await activate(page, bg.terminalId);
	await page.keyboard.press("Control+C");
	const done = await waitFor(
		"background 终端退出",
		async () => {
			const t = (await terminals(page)).find(
				(x) => x.terminalId === bg.terminalId,
			);
			return t && t.status !== "live" ? t : undefined;
		},
		30_000,
		300,
	);
	const notice = await page
		.locator("[data-testid=terminal-notice]")
		.textContent();
	note(
		`退出状态 status=${done.status} exitCode=${done.exitCode}；提示「${notice.trim()}」`,
	);
	check(/退出|终止/.test(notice), "终端显示退出状态");
	await shot(page, "T5-exited");
}

async function T6() {
	const page = await mainPage();
	const id = await openShell(page);
	const st = wsStats.get(page);
	const opened0 = st?.opened ?? 0;
	const reset0 = st?.liveReset ?? 0;
	const lag0 = st?.lagged ?? 0;
	const logOff = Number(sh(`stat -c %s ${LOG}`).out);
	const t0 = Date.now();
	await typeLine(
		page,
		id,
		`yes | head -c 300000000; echo flood-${RUN}-$((1+1))`,
	);
	await waitText(
		page,
		id,
		(s) => hasLine(s, `flood-${RUN}-2`),
		"洪泛结束",
		900_000,
	);
	note(`洪泛 300MB 用时 ${((Date.now() - t0) / 1000).toFixed(1)}s`);
	await typeLine(page, id, `echo ok-${RUN}`);
	await waitText(page, id, (s) => hasLine(s, `ok-${RUN}`), "echo ok 回显");
	note(`之后 echo ok-${RUN} 正常回显`);
	const opened = (st?.opened ?? 0) - opened0;
	const resets = (st?.liveReset ?? 0) - reset0;
	note(
		`期间新 WS 连接 ${opened}，live.reset ${resets}，term.lagged ${(st?.lagged ?? 0) - lag0}`,
	);
	check(opened === 0, "WS 未重连（无新连接）");
	check(resets === 0, "线程未收到 live.reset");
	const slow = sh(
		`tail -c +${logOff + 1} ${LOG} | grep -c '慢消费者' || true`,
	).out;
	check(slow.trim() === "0", `orynd 日志无「慢消费者」（${slow.trim()}）`);
	await shot(page, "T6-after-flood");
}

async function T7() {
	const page = await mainPage();
	const orb = orbOf(THREAD);
	const old = await openShell(page);
	const k = sh(`docker exec ${orb} pkill -f 'omp --mode rpc-ui'`);
	note(`pkill → code ${k.code}`);
	await waitFor(
		"旧终端变为 lost",
		async () =>
			(await terminals(page)).find((t) => t.terminalId === old)?.status ===
			"lost",
		60_000,
		300,
	);
	// 新 shell 创建后面板会自动切到它：先等新 shell 出现，再切回旧终端读提示，避免两者竞态。
	const fresh = await waitFor(
		"新 shell 出现",
		async () =>
			(await terminals(page)).find(
				(t) =>
					t.kind === "shell" && t.status === "live" && t.terminalId !== old,
			)?.terminalId,
		120_000,
		500,
	);
	note(`新 shell ${fresh}`);
	await activate(page, old).catch(() => undefined);
	const notice = await page
		.locator("[data-testid=terminal-notice]")
		.textContent({ timeout: 10_000 });
	check(notice.includes("终端会话已终止"), `旧终端提示「${notice.trim()}」`);
	await shot(page, "T7-lost");
	await sendPrompt(page, `只回复 pong-${RUN}`);
	await waitTurn(THREAD);
	const reply = await waitFor(
		"agent 回复",
		async () => {
			const t = await lastAssistant(page);
			return t.includes(`pong-${RUN}`) ? t : undefined;
		},
		60_000,
		500,
	);
	note(`重启后回复：${reply.slice(0, 80).replace(/\n/g, " ")}`);
	await shot(page, "T7-new-shell");
}

async function T8() {
	const page = await mainPage();
	const orb = orbOf(THREAD);
	const id = await openShell(page);
	await typeLine(page, id, "while true; do date +tick-%s; sleep 1; done");
	await waitText(page, id, (s) => /^tick-\d+$/m.test(s), "date 循环开始");
	const t0 = Date.now();
	note(
		"等待空闲冻结（dev 阈值 agent_idle=30s, user_idle=30s，评估周期 30s），不使用 REST pause",
	);
	await waitFor(
		`orb ${orb} 进入 frozen`,
		() => orbState(orb) === "frozen",
		300_000,
		1000,
	);
	note(`orbs.state=frozen，用时 ${((Date.now() - t0) / 1000).toFixed(0)}s`);
	const docker = sh(
		`docker ps -a --filter name=${orb} --format '{{.Status}}'`,
	).out;
	note(`docker ps：${docker}`);
	const overlay = page.locator("[data-testid=terminal-offline]");
	await overlay.waitFor({ timeout: 30_000 });
	note(`遮罩：${(await overlay.textContent()).replace(/\n/g, " ")}`);
	const before = await termText(page, id);
	const ticks = (s: string) =>
		s.split("\n").filter((l) => /^tick-\d+$/.test(l.trimEnd()));
	const lastBefore = ticks(before).at(-1) ?? "";
	note(`冻结前最后一行 ${lastBefore}`);
	await shot(page, "T8-frozen-overlay");
	const wokeAt = Math.floor(Date.now() / 1000);
	await overlay.locator("button").click();
	const id2 = await openShell(page);
	check(id2 === id, `唤醒后仍是同一终端 id（${id2}）`);
	const after = await waitText(
		page,
		id,
		(s) => {
			const t = ticks(s);
			// 唤醒之后才打印的行（date 取真实时间，冻结期间没有输出）
			return t.includes(lastBefore) && Number(t.at(-1)?.slice(5)) > wokeAt;
		},
		"唤醒后继续输出",
		60_000,
	);
	note(
		`唤醒于 tick-${wokeAt}，之后同一终端输出 ${ticks(after).at(-1)}，画面仍含 ${lastBefore}`,
	);
	await shot(page, "T8-woken");
	await activate(page, id);
	await page.keyboard.press("Control+C");
}

async function T9() {
	const page = await mainPage();
	const orb = orbOf(THREAD);
	await openShell(page);
	await openTab(page, "files");
	const tree = page.locator("[role=tree]");
	await tree.waitFor({ timeout: 60_000 });
	// 根切到「工作区」
	const ws = page.getByRole("tab", { name: "工作区" });
	if (await ws.count()) await ws.click();
	const readme = page.locator(`[role=treeitem][data-path="${REPO}/README.md"]`);
	await readme.waitFor({ timeout: 30_000 });
	await readme.click();
	const editor = page.locator(".cm-editor .cm-content");
	await editor.waitFor({ timeout: 30_000 });
	const line1 = `m3-accept-${RUN}-1`;
	await editor.click();
	await page.keyboard.press("Control+End");
	await page.keyboard.type(`\n${line1}`);
	await page.getByRole("button", { name: "保存", exact: true }).click();
	await waitFor(
		"容器内 README 含新行",
		() => dexec(orb, `grep -c ${line1} ${REPO}/README.md`).out === "1",
		15_000,
	);
	note(`保存后容器内 README.md 含 ${line1}`);
	dexec(orb, `echo x >> ${REPO}/README.md`);
	await editor.click();
	await page.keyboard.press("Control+End");
	await page.keyboard.type(`\nm3-accept-${RUN}-2`);
	await page.getByRole("button", { name: "保存", exact: true }).click();
	const conflict = page.getByRole("dialog").filter({ hasText: "文件已被修改" });
	await conflict.waitFor({ timeout: 15_000 });
	check(true, "外部修改后保存弹出冲突对话框（412）");
	await shot(page, "T9-conflict");
	await conflict.getByRole("button", { name: "重新加载" }).click();

	// 新建 a/b.txt → 重命名 c.txt → 删除
	await page.getByRole("button", { name: "新建文件", exact: true }).click();
	await page.getByLabel("名称", { exact: true }).fill(`a-${RUN}/b.txt`);
	await page.getByRole("button", { name: "创建", exact: true }).click();
	await waitFor(
		"容器内出现 a/b.txt",
		() => dexec(orb, `test -f ${REPO}/a-${RUN}/b.txt && echo y`).out === "y",
		15_000,
	);
	note(`新建 ${REPO}/a-${RUN}/b.txt 在容器内可见`);
	const bItem = page.locator(
		`[role=treeitem][data-path="${REPO}/a-${RUN}/b.txt"]`,
	);
	await bItem.waitFor({ timeout: 15_000 });
	await page.getByRole("button", { name: "b.txt 的操作" }).click();
	await page.getByRole("menuitem", { name: "重命名" }).click();
	await page.getByLabel("名称", { exact: true }).fill("c.txt");
	await page
		.getByRole("dialog")
		.getByRole("button", { name: "重命名", exact: true })
		.click();
	await waitFor(
		"容器内 b.txt → c.txt",
		() =>
			dexec(
				orb,
				`test -f ${REPO}/a-${RUN}/c.txt && ! test -e ${REPO}/a-${RUN}/b.txt && echo y`,
			).out === "y",
		15_000,
	);
	note("重命名为 c.txt 在容器内可见");
	await page.getByRole("button", { name: "c.txt 的操作" }).click();
	await page.getByRole("menuitem", { name: "删除" }).click();
	await page
		.getByRole("dialog")
		.getByRole("button", { name: "删除", exact: true })
		.click();
	await waitFor(
		"容器内 c.txt 已删除",
		() =>
			dexec(orb, `test -e ${REPO}/a-${RUN}/c.txt || echo gone`).out === "gone",
		15_000,
	);
	note("删除后容器内不存在");
	dexec(orb, `rmdir ${REPO}/a-${RUN}`);

	// 512MiB 上传 / 下载
	const big = "/tmp/oryn-m3-big.bin";
	if (!existsSync(big) || Bun.file(big).size !== 512 * 1024 * 1024)
		sh(`dd if=/dev/urandom of=${big} bs=1M count=512 status=none`);
	const srcSha = await sha256File(big);
	note(`源文件 512MiB sha256=${srcSha}`);
	const name = `big-${RUN}.bin`;
	const target = `${REPO}/${name}`;
	// 上传进根目录：输入框的 change 事件把文件放进当前根（hardlink 改名，让上传文件名带 RUN）
	const input = page.locator("[data-testid=file-upload-input]");
	const tmpNamed = `/tmp/${name}`;
	sh(`ln -f ${big} ${tmpNamed}`);
	const tUp = Date.now();
	await input.setInputFiles(tmpNamed);
	await waitFor(
		"容器内上传完成",
		() =>
			dexec(orb, `stat -c %s ${target} 2>/dev/null`).out ===
			String(512 * 1024 * 1024),
		600_000,
		2000,
	);
	// 等上传队列收尾（临时文件 rename 完成后大小才对）
	const upSha = dexec(orb, `sha256sum ${target} | cut -d' ' -f1`).out;
	note(
		`上传用时 ${((Date.now() - tUp) / 1000).toFixed(1)}s，容器内 sha256=${upSha}`,
	);
	check(upSha === srcSha, "上传 sha256 与源文件相等");
	// 浏览器下载
	const item = page.locator(`[role=treeitem][data-path="${target}"]`);
	await page.getByRole("button", { name: "刷新", exact: true }).first().click();
	await item.waitFor({ timeout: 30_000 });
	await item.click();
	const binCard = page.getByText("application/octet-stream").first();
	await binCard.waitFor({ timeout: 15_000 });
	const hasEditor = await page.locator(".cm-editor").count();
	check(hasEditor === 0, "二进制文件只显示下载卡片（无编辑器）");
	await shot(page, "T9-binary-card");
	const [dl] = await Promise.all([
		page.waitForEvent("download", { timeout: 60_000 }),
		page.getByRole("button", { name: "下载", exact: true }).last().click(),
	]);
	const dlPath = `/tmp/oryn-m3-dl-${RUN}.bin`;
	note(`下载地址 ${dl.url()}`);
	await dl.saveAs(dlPath);
	const dlSha = await sha256File(dlPath);
	note(`浏览器下载 sha256=${dlSha}`);
	check(dlSha === srcSha, "下载 sha256 与源文件相等");
	sh(`rm -f ${dlPath} ${tmpNamed}`);

	// 图片预览
	const png = `/tmp/m3-shot-${RUN}.png`;
	await page.screenshot({
		path: png,
		clip: { x: 0, y: 0, width: 200, height: 120 },
	});
	await input.setInputFiles(png);
	const pngItem = page.locator(
		`[role=treeitem][data-path="${REPO}/m3-shot-${RUN}.png"]`,
	);
	await pngItem.waitFor({ timeout: 30_000 });
	await pngItem.click();
	const img = page.locator(`img[alt="m3-shot-${RUN}.png"]`);
	await img.waitFor({ timeout: 15_000 });
	const natural = await img.evaluate(
		(el) => (el as HTMLImageElement).naturalWidth,
	);
	check(natural > 0, `图片有预览（naturalWidth=${natural}）`);
	await shot(page, "T9-image-preview");
	sh(`rm -f ${png}`);

	// 513MiB → 413；/etc → 403
	const big513 = "/tmp/oryn-m3-513.bin";
	if (!existsSync(big513) || Bun.file(big513).size !== 513 * 1024 * 1024)
		sh(`truncate -s 513M ${big513}`);
	const put = sh(
		`curl -s -o /tmp/oryn-m3-413.json -w '%{http_code}' -T ${big513} -H "Authorization: Bearer ${TOKEN}" -H 'X-Oryn-CSRF: 1' '${BASE}/api/v1/orbs/${orb}/fs/file?path=${encodeURIComponent(`${REPO}/big513.bin`)}'`,
	);
	const body413 = sh("cat /tmp/oryn-m3-413.json").out;
	note(`curl -T 513MiB → HTTP ${put.out} ${body413}`);
	check(
		put.out === "413" && body413.includes("too_large"),
		"513MiB 返回 413 too_large",
	);
	const left = dexec(orb, `ls -a ${REPO} | grep -c big513 || true`).out;
	check(left.trim() === "0", "513MiB 失败后容器内无残留");
	const etc = await api("GET", `/api/v1/orbs/${orb}/fs/list?path=/etc`);
	note(`GET fs/list?path=/etc → ${etc.status} ${etc.text.slice(0, 120)}`);
	check(
		etc.status === 403 && etc.text.includes("outside_home"),
		"/etc 返回 403 outside_home",
	);
	// 清理大文件，避免进入 T10 的改动列表
	dexec(orb, `rm -f ${target} ${REPO}/m3-shot-${RUN}.png`);
}

interface UiFile {
	path: string;
	letter: string;
	add: string;
	del: string;
	binary: boolean;
	staged: string | null;
}

async function uiFiles(page: Page): Promise<UiFile[]> {
	return page.evaluate(() => {
		const ul = document.querySelector(
			'ul[aria-label="改动的文件"], ul[aria-label]',
		);
		const rows = [...(ul?.querySelectorAll("li li") ?? [])];
		return rows.map((li) => {
			const btn = li.querySelector(
				"button[aria-current]",
			) as HTMLElement | null;
			const spans = btn ? [...btn.querySelectorAll(":scope > span")] : [];
			const letter = (spans[0]?.textContent ?? "").trim();
			const name = spans[1] as HTMLElement | undefined;
			const stats = (spans[2]?.textContent ?? "").trim();
			const box = li.querySelector(
				"input[type=checkbox]",
			) as HTMLInputElement | null;
			const m = /\+(\d+)/.exec(stats);
			const d = /−(\d+)/.exec(stats);
			return {
				path: name?.getAttribute("title") ?? "",
				letter,
				add: m?.[1] ?? "",
				del: d?.[1] ?? "",
				binary: !m && !d && stats.length > 0,
				staged: box?.dataset.staged ?? null,
			};
		});
	});
}

async function T10() {
	const page = await mainPage();
	const orb = orbOf(THREAD);
	await openShell(page);
	const git = `git -C ${REPO} -c user.name=m3 -c user.email=m3@oryn.local`;
	// 准备：一次提交、一个已暂存改动、一个未暂存改动、一个未跟踪文件、一次 git mv、一个二进制文件
	const prep = dexec(
		orb,
		[
			`set -e; cd ${REPO}`,
			// 从基线干净状态开始（可重复执行）
			"git reset -q --hard origin/main && git clean -fdq -e .agent-logs",
			`echo "commit-${RUN}" > commit-${RUN}.txt && ${git} add commit-${RUN}.txt && ${git} commit -qm "m3 accept ${RUN}"`,
			`echo "// staged ${RUN}" >> src/slug.js && ${git} add src/slug.js`,
			`printf 'unstaged-${RUN}\\n' >> README.md`,
			`echo "untracked ${RUN}" > untracked-${RUN}.txt`,
			`${git} mv scripts/db-check.sh scripts/db-check-${RUN}.sh`,
			`head -c 4096 /dev/urandom > bin-${RUN}.dat && ${git} add bin-${RUN}.dat`,
			`git rev-parse HEAD`,
		].join("; "),
	);
	note(`准备 git 状态 → code ${prep.code} HEAD=${prep.out.split("\n").at(-1)}`);
	check(prep.code === 0, "容器内 git 准备成功");
	const commitSha = prep.out.split("\n").at(-1) ?? "";
	// 行首加「|」再去掉：sh() 会 trim，否则首行的前导空格（工作区改动）丢失
	const porcelain = () =>
		dexec(orb, `git -C ${REPO} status --porcelain | sed 's/^/|/'`)
			.out.split("\n")
			.map((l) => l.slice(1))
			.join("\n");
	note(`git status --porcelain：${porcelain().replace(/\n/g, " | ")}`);
	const numstat = dexec(
		orb,
		`cd ${REPO} && git diff --numstat -M $(git merge-base HEAD origin/main)`,
	).out;
	note(`git diff --numstat <merge-base>：${numstat.replace(/\n/g, " | ")}`);

	await openTab(page, "changes");
	await page
		.getByRole("button", { name: "刷新", exact: true })
		.first()
		.click()
		.catch(() => undefined);
	const files = await waitFor(
		"改动列表出现全部文件",
		async () => {
			const fs = await uiFiles(page);
			return fs.some((f) => f.path.includes(`untracked-${RUN}`)) &&
				fs.some((f) => f.path.includes(`bin-${RUN}`)) &&
				fs.some((f) => f.path.includes(`commit-${RUN}`))
				? fs
				: undefined;
		},
		60_000,
		1000,
	);
	for (const f of files)
		note(
			`UI ${f.letter} ${f.path} ${f.binary ? "binary" : `+${f.add} −${f.del}`} staged=${f.staged}`,
		);
	await shot(page, "T10-all-changes");
	const expectStatus: [string, string][] = [
		[`commit-${RUN}.txt`, "A"],
		["src/slug.js", "M"],
		["README.md", "M"],
		[`untracked-${RUN}.txt`, "U"],
		[`scripts/db-check-${RUN}.sh`, "R"],
		[`bin-${RUN}.dat`, "A"],
	];
	for (const [p, letter] of expectStatus) {
		const f = files.find((x) => x.path === p);
		check(
			f?.letter === letter,
			`${p} 状态 ${letter}（UI ${f?.letter ?? "缺失"}）`,
		);
	}
	// +/− 与 git numstat 一致
	for (const line of numstat.split("\n")) {
		const [a, d, ...rest] = line.split("\t");
		const p = rest.join("\t");
		if (!p) continue;
		const path = p.includes("=>")
			? p.replace(/\{?([^{}]*) => ([^{}]*)\}?/, "$2").replace("//", "/")
			: p;
		const f = files.find((x) => x.path === path);
		if (!f) continue;
		if (a === "-") check(f.binary, `${path} 二进制`);
		else
			check(
				f.add === a && f.del === d,
				`${path} +${a} −${d}（UI +${f.add} −${f.del}）`,
			);
	}
	const bin = files.find((f) => f.path === `bin-${RUN}.dat`);
	check(bin?.binary === true, "二进制文件标注 binary");

	// 点文件显示 diff
	await page
		.locator("button[aria-current]", { hasText: "slug.js" })
		.first()
		.click();
	await waitFor(
		"diff 出现",
		async () =>
			(await page.locator("body").textContent()).includes(`// staged ${RUN}`),
		15_000,
	);
	note("点 src/slug.js 显示 diff（含新增行）");
	await shot(page, "T10-diff");

	// 暂存 / 取消暂存
	const cb = page.getByLabel(`暂存 untracked-${RUN}.txt`, { exact: true });
	// 受控复选框：状态在暂存请求完成后才变，用 click 而不是 check
	await cb.click();
	await waitFor(
		"porcelain 显示 A untracked",
		() => porcelain().split("\n").includes(`A  untracked-${RUN}.txt`),
		15_000,
	);
	const after1 = (await uiFiles(page)).find(
		(f) => f.path === `untracked-${RUN}.txt`,
	);
	note(
		`勾选后 porcelain「A  untracked-${RUN}.txt」，UI staged=${after1?.staged} 状态 ${after1?.letter}`,
	);
	check(after1?.staged === "full", "UI 勾选状态与 porcelain 一致（已暂存）");
	await cb.click();
	await waitFor(
		"porcelain 显示 ?? untracked",
		() => porcelain().split("\n").includes(`?? untracked-${RUN}.txt`),
		15_000,
	);
	const after2 = (await uiFiles(page)).find(
		(f) => f.path === `untracked-${RUN}.txt`,
	);
	check(
		after2?.staged === "none",
		`取消暂存后 porcelain「?? untracked-${RUN}.txt」，UI staged=${after2?.staged}`,
	);
	// 全部文件的勾选与 porcelain 对照
	const por = porcelain().split("\n");
	for (const f of await uiFiles(page)) {
		const line = por.find(
			(l) => l.endsWith(` ${f.path}`) || l.endsWith(`-> ${f.path}`),
		);
		if (!line) continue;
		const x = line[0];
		const y = line[1];
		const want =
			x === "?"
				? "none"
				: x !== " " && y === " "
					? "full"
					: x === " "
						? "none"
						: "partial";
		check(
			f.staged === want,
			`${f.path} porcelain「${line.slice(0, 2)}」↔ UI staged=${f.staged}`,
		);
	}

	// 单个 commit
	await page
		.getByLabel("改动范围", { exact: false })
		.selectOption(`commit:${commitSha}`)
		.catch(async () => {
			await page.locator("select").first().selectOption(`commit:${commitSha}`);
		});
	const cfiles = await waitFor(
		"commit 范围只显示该提交",
		async () => {
			const fs = await uiFiles(page);
			return fs.length > 0 && fs.every((f) => f.path === `commit-${RUN}.txt`)
				? fs
				: undefined;
		},
		15_000,
		500,
	);
	note(
		`commit ${commitSha.slice(0, 7)} 范围：${cfiles.map((f) => f.path).join(",")}`,
	);
	await shot(page, "T10-commit-scope");
	await page.locator("select").first().selectOption("all");

	// 非 git 线程
	const np = sh(
		`ORYN_CREDENTIALS=${ROOT}/.dev/credentials.json ${ROOT}/bin/oryn thread new --no-project "只回复 ok" --json`,
	);
	const npId = (JSON.parse(np.out.slice(np.out.indexOf("{"))) as { id: string })
		.id;
	created.push(npId);
	note(`--no-project 线程 ${npId}`);
	const p2 = await newPage("noproj");
	try {
		await p2.goto(`${BASE}/t/${npId}`);
		await waitFor(
			"no-project 线程有 orb 且就绪",
			() => {
				const r = sql<{ state: string }>(
					`select o.state from threads t join orbs o on o.id=t.orb_id where t.id='${npId}'`,
				)[0];
				return r?.state === "ready";
			},
			240_000,
			1000,
		);
		await openTab(p2, "changes");
		await p2.getByText("工作目录不是 git 仓库").waitFor({ timeout: 60_000 });
		check(true, "--no-project 线程显示「工作目录不是 git 仓库」");
		await shot(p2, "T10-not-repo");
	} finally {
		await p2.close();
	}
}

/** 生成一个约 2MiB、首段为可读文本（未压缩内容流）的 PDF。 */
function makePdf(firstParagraph: string, padBytes: number): Buffer {
	const esc = (s: string) => s.replace(/[\\()]/g, (c) => `\\${c}`);
	const content = `BT /F1 12 Tf 72 720 Td (${esc(firstParagraph)}) Tj ET\nBT /F1 12 Tf 72 690 Td (Second paragraph: filler.) Tj ET\n`;
	const pad = Buffer.alloc(padBytes, 0x41);
	const objs = [
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		`<< /Length ${content.length} >>\nstream\n${content}endstream`,
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	];
	const parts: Buffer[] = [Buffer.from("%PDF-1.4\n")];
	let off = parts[0]?.length ?? 0;
	const offsets: number[] = [];
	for (const [i, body] of objs.entries()) {
		offsets.push(off);
		const b = Buffer.from(`${i + 1} 0 obj\n${body}\nendobj\n`);
		parts.push(b);
		off += b.length;
	}
	// 填充对象（未被引用）：把体积撑到约 2MiB
	offsets.push(off);
	const head = Buffer.from(`6 0 obj\n<< /Length ${pad.length} >>\nstream\n`);
	const tail = Buffer.from("\nendstream\nendobj\n");
	parts.push(head, pad, tail);
	off += head.length + pad.length + tail.length;
	const xref = [
		"xref",
		`0 ${offsets.length + 1}`,
		"0000000000 65535 f ",
		...offsets.map((o) => `${String(o).padStart(10, "0")} 00000 n `),
		`trailer << /Size ${offsets.length + 1} /Root 1 0 R >>`,
		"startxref",
		String(off),
		"%%EOF\n",
	].join("\n");
	parts.push(Buffer.from(xref));
	return Buffer.concat(parts);
}

async function T11() {
	const page = await mainPage();
	const magic = `lantern-${RUN}`;
	const para = `Oryn M3 acceptance report. The first paragraph says the magic word is ${magic}.`;
	const dir = `/tmp/oryn-m3-att-${RUN}`;
	mkdirSync(dir, { recursive: true });
	const pdf = `${dir}/report.pdf`;
	await Bun.write(pdf, makePdf(para, 2 * 1024 * 1024 - 2048));
	const big = `${dir}/big-100m.bin`;
	sh(`dd if=/dev/urandom of=${big} bs=1M count=100 status=none`);
	const png = `${dir}/screenshot.png`;
	await page.goto(`${BASE}/`);
	await page.screenshot({
		path: png,
		clip: { x: 0, y: 0, width: 320, height: 200 },
	});
	const files = [pdf, big, png];
	const sizes = files.map((f) => Bun.file(f).size);
	const shas = await Promise.all(files.map(sha256File));
	note(
		`本地文件：${files.map((f, i) => `${f.split("/").at(-1)} ${sizes[i]}B`).join("，")}`,
	);

	// 新建线程框：项目 sandbox + 3 个附件
	const attachInput = page
		.locator("label", { hasText: "添加附件" })
		.locator("input[type=file]");
	await attachInput.waitFor({ state: "attached", timeout: 15_000 });
	await page.keyboard.press("Control+j");
	await page.getByRole("button", { name: /^sandbox\s*github/ }).click();
	await attachInput.setInputFiles(files);
	const send = page.getByRole("button", { name: "发送" }).last();
	const box = page.locator("textarea").first();
	await box.fill("告诉我 PDF 第一段的内容和那个大文件的字节数");
	await waitFor(
		"附件上传完成（发送可用）",
		() => send.isEnabled(),
		300_000,
		500,
	);
	await shot(page, "T11-new-thread");
	await send.click();
	await page.waitForURL(/\/t\/T-/, { timeout: 60_000 });
	const tid = page.url().split("/t/")[1] ?? "";
	created.push(tid);
	note(`线程 ${tid}`);
	await waitFor(
		"线程回到 idle",
		() => {
			const s = threadState(tid);
			return s === "idle" || s === "error" ? s : undefined;
		},
		480_000,
		1000,
	);
	const orb = orbOf(tid);
	const found = dexec(orb, `find ~/.oryn/attachments/${tid} -type f`).out;
	note(
		`docker exec find ~/.oryn/attachments/${tid} -type f：${found.replace(/\n/g, " | ")}`,
	);
	check(
		found.split("\n").filter(Boolean).length === 3,
		"orb 内有 3 个附件文件",
	);
	const inOrbSha = dexec(
		orb,
		`find ~/.oryn/attachments/${tid} -type f -exec sha256sum {} +`,
	).out;
	for (const [i, f] of files.entries())
		check(
			inOrbSha.includes(shas[i] ?? "-"),
			`${f.split("/").at(-1)} 在 orb 内字节一致（sha256 ${shas[i]?.slice(0, 12)}…）`,
		);
	const por = dexec(orb, `git -C ${REPO} status --porcelain`).out;
	note(`git status --porcelain：「${por.replace(/\n/g, " | ")}」`);
	check(
		!/report\.pdf|big-100m|screenshot\.png|attachments/.test(por),
		"附件未进入仓库",
	);
	await page
		.locator('[data-role="assistant"]')
		.last()
		.waitFor({ timeout: 30_000 });
	const reply = await lastAssistant(page);
	note(`agent 回复：${reply.slice(0, 240).replace(/\n/g, " ")}`);
	const bytes = String(100 * 1024 * 1024);
	check(reply.includes(magic), `回答含 PDF 首段的 ${magic}`);
	check(
		reply.replace(/[,，\s]/g, "").includes(bytes),
		`回答含大文件字节数 ${bytes}`,
	);
	await shot(page, "T11-reply");

	// 线程菜单「附件」
	await page.getByRole("button", { name: "更多操作" }).first().click();
	await page.getByRole("menuitem", { name: "附件" }).click();
	const dialog = page.getByRole("dialog").filter({ hasText: "附件" });
	await dialog.locator("li").nth(2).waitFor({ timeout: 15_000 });
	const names = await dialog.locator("li").allTextContents();
	note(`附件对话框：${names.map((n) => n.slice(0, 40)).join(" / ")}`);
	check(names.length === 3, "线程菜单「附件」列出 3 项");
	await shot(page, "T11-attachments-dialog");
	const list = (await api("GET", `/api/v1/threads/${tid}/attachments`))
		.json as {
		attachments: { id: string; name: string }[];
	};
	const atts = list.attachments;
	const victim = atts.find((a) => a.name === "screenshot.png");
	check(victim !== undefined, "REST 列表含 screenshot.png");
	page.once("dialog", (d) => void d.accept());
	await dialog
		.locator("li", { hasText: "screenshot.png" })
		.getByRole("button", { name: "删除" })
		.click();
	await dialog
		.locator("li")
		.nth(2)
		.waitFor({ state: "detached", timeout: 15_000 });
	const gone = await api("GET", `/api/v1/attachments/${victim?.id}/content`);
	note(`删除后 GET attachments/${victim?.id}/content → ${gone.status}`);
	check(gone.status === 404, "删除的附件 content 返回 404");
	const orbCopy = dexec(
		orb,
		`find ~/.oryn/attachments/${tid} -type f | wc -l`,
	).out;
	note(`orb 内剩余附件文件 ${orbCopy}`);
	await page.keyboard.press("Escape");

	// 101MiB：界面拒绝；REST 413
	const over = `${dir}/big-101m.bin`;
	sh(`truncate -s 101M ${over}`);
	await attachInput.setInputFiles(over);
	const err = page.getByText("超过单个附件上限");
	await err.first().waitFor({ timeout: 15_000 });
	check(true, "界面拒绝 101MiB 附件（超过单个附件上限）");
	await shot(page, "T11-101m-rejected");
	const r413 = sh(
		`curl -s -o ${dir}/413.json -w '%{http_code}' -H "Authorization: Bearer ${TOKEN}" -H 'X-Oryn-CSRF: 1' -H 'Content-Type: application/octet-stream' --data-binary @${over} '${BASE}/api/v1/attachments?name=big-101m.bin&threadId=${tid}'`,
	).out;
	const body = sh(`cat ${dir}/413.json`).out;
	note(`REST 上传 101MiB → HTTP ${r413} ${body}`);
	check(r413 === "413", "101MiB 上传返回 413");

	// 删除 orb 后其余附件仍可下载
	const del = await api("DELETE", `/api/v1/orbs/${orb}`);
	note(`DELETE orbs/${orb} → ${del.status}`);
	await waitFor(
		"orb deleted",
		() => orbState(orb) === "deleted",
		120_000,
		1000,
	);
	for (const a of atts.filter((x) => x.id !== victim?.id)) {
		const out = `${dir}/dl-${a.id}`;
		const code = sh(
			`curl -s -o ${out} -w '%{http_code}' -H "Authorization: Bearer ${TOKEN}" '${BASE}/api/v1/attachments/${a.id}/content'`,
		).out;
		const sha = await sha256File(out);
		const i = files.findIndex((f) => f.endsWith(`/${a.name}`));
		note(`orb 删除后下载 ${a.name} → ${code} sha256 ${sha.slice(0, 12)}…`);
		check(
			code === "200" && sha === shas[i],
			`${a.name} 仍可从控制面下载且字节一致`,
		);
	}
	sh(`rm -rf ${dir}`);
}

async function T12() {
	const page = await mainPage();
	const orig = await api("GET", "/api/v1/agent-config/files/AGENTS.md");
	check(orig.status === 200, "读到 AGENTS.md 原文");
	const line = `<!-- m3-accept ${RUN} -->`;
	try {
		await page.goto(`${BASE}/settings/agent-config`);
		const item = page.locator('[role=treeitem][data-path="AGENTS.md"]');
		await item.waitFor({ timeout: 30_000 });
		await item.click();
		const ed = page.locator(".cm-editor .cm-content");
		await ed.waitFor({ timeout: 15_000 });
		check(
			(await page.locator(".cm-editor").count()) > 0,
			"编辑器是 CodeMirror（.cm-editor）",
		);
		await ed.click();
		await page.keyboard.press("Control+End");
		await page.keyboard.type(`\n${line}`);
		await page.getByRole("button", { name: "保存", exact: true }).click();
		const now = await waitFor(
			"REST 读到新内容",
			async () => {
				const r = await api("GET", "/api/v1/agent-config/files/AGENTS.md");
				return r.text.includes(line) ? r : undefined;
			},
			15_000,
			500,
		);
		note(`GET agent-config/files/AGENTS.md → ${now.status}，含 ${line}`);
		await shot(page, "T12-settings-editor");
		await page.goto(`${BASE}/t/${THREAD}`);
		await openTab(page, "files");
		await page.getByRole("tab", { name: "Agent 配置" }).click();
		const item2 = page.locator('[role=treeitem][data-path="AGENTS.md"]');
		await item2.waitFor({ timeout: 30_000 });
		await item2.click();
		await waitFor(
			"线程面板打开同一文件",
			async () =>
				(await page.locator(".cm-editor .cm-content").textContent()).includes(
					`m3-accept ${RUN}`,
				),
			15_000,
			300,
		);
		check(true, "线程 Files 面板「Agent 配置」根打开 AGENTS.md，内容含新行");
		await shot(page, "T12-thread-agent-config");
	} finally {
		// 还原用户的 AGENTS.md
		const r = await fetch(`${BASE}/api/v1/agent-config/files/AGENTS.md`, {
			method: "PUT",
			headers: { Authorization: `Bearer ${TOKEN}`, "X-Oryn-CSRF": "1" },
			body: orig.text,
		});
		note(`还原 AGENTS.md → ${r.status}`);
	}
}

async function T13() {
	const orb = orbOf(THREAD);
	if (orbState(orb) !== "ready") {
		await api("POST", `/api/v1/orbs/${orb}/resume`);
		await waitFor("orb ready", () => orbState(orb) === "ready", 120_000, 1000);
	}
	const since = Date.now();
	const p = await api("POST", `/api/v1/orbs/${orb}/pause`, { mode: "freeze" });
	note(`POST pause{freeze} → ${p.status}`);
	await waitFor("orb frozen", () => orbState(orb) === "frozen", 60_000, 500);
	const path = encodeURIComponent("~");
	const bg = await api("GET", `/api/v1/orbs/${orb}/fs/list?path=${path}&bg=1`);
	note(
		`GET fs/list?path=~&bg=1 → ${bg.status} ${bg.text.slice(0, 120)}；orb=${orbState(orb)}`,
	);
	check(
		bg.status === 409 && bg.text.includes("orb_not_ready"),
		"bg=1 返回 409 orb_not_ready",
	);
	check(orbState(orb) === "frozen", "orb 仍 frozen");
	const t0 = Date.now();
	const fg = await api("GET", `/api/v1/orbs/${orb}/fs/list?path=${path}`);
	const dt = Date.now() - t0;
	note(`GET fs/list?path=~ → ${fg.status}（${dt}ms）${fg.text.slice(0, 80)}`);
	check(fg.status === 200 && dt < 25_000, "不带 bg 25s 内 200");
	// fs 请求只需 link 恢复，可能在 resume 脚本跑完（resuming→ready）之前就返回
	await waitFor(
		"orb 回到 ready",
		() => orbState(orb) === "ready",
		25_000 - dt,
		300,
	);
	note(`orb 回到 ready（距请求 ${Date.now() - t0}ms）`);
	const ev = sql<{ from_state: string; to_state: string }>(
		`select from_state, to_state from orb_events where orb_id='${orb}' and ts>=${since} order by id`,
	);
	const chain = ev.map((e) => `${e.from_state}→${e.to_state}`).join(", ");
	note(`orb_events：${chain}`);
	check(
		/frozen→waking/.test(chain) && /→ready$/.test(chain),
		"orb_events 记录 frozen→waking→…→ready",
	);
}

async function T14() {
	const page = await newPage("T14");
	const orb = orbOf(T14_THREAD);
	const errs0 = pageErrors.length;
	try {
		const rest = await api("GET", `/api/v1/orbs/${orb}/fs/roots`);
		note(
			`GET fs/roots（旧 orb ${orb}）→ ${rest.status} ${rest.text.slice(0, 100)}`,
		);
		check(
			rest.status === 409 && rest.text.includes("orbd_outdated"),
			"接口返回 409 orbd_outdated",
		);
		const git = await api("GET", `/api/v1/orbs/${orb}/git/status`);
		note(`GET git/status → ${git.status} ${git.text.slice(0, 80)}`);
		const id = await openShell(page, T14_THREAD);
		await typeLine(page, id, `echo old-${RUN}`);
		await waitText(
			page,
			id,
			(s) => hasLine(s, `old-${RUN}`),
			"旧 orb 终端回显",
			30_000,
			T14_THREAD,
		);
		check(true, `旧 orb 终端可用（${id}）`);
		await shot(page, "T14-terminal");
		await openTab(page, "files");
		await page
			.getByText("这台 orb 创建于旧版本，不支持文件面板")
			.waitFor({ timeout: 30_000 });
		check(true, "Files 显示「这台 orb 创建于旧版本」");
		await shot(page, "T14-files");
		await openTab(page, "changes");
		await page
			.getByText("这台 orb 创建于旧版本，不支持改动面板")
			.waitFor({ timeout: 30_000 });
		check(true, "Changes 显示「这台 orb 创建于旧版本」");
		await shot(page, "T14-changes");
		check(
			pageErrors.length === errs0,
			`页面无报错（pageerror ${pageErrors.length - errs0}）`,
		);
	} finally {
		await page.close();
	}
}

// ---------------------------------------------------------------- 主流程

const created: string[] = [];

const TESTS: [string, () => Promise<void>][] = [
	["T1", T1],
	["T2", T2],
	["T3", T3],
	["T4", T4],
	["T5", T5],
	["T6", T6],
	["T7", T7],
	["T8", T8],
	["T9", T9],
	["T10", T10],
	["T11", T11],
	["T12", T12],
	["T13", T13],
	["T14", T14],
];

const browser: Browser = await chromium.launch({
	executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
	headless: true,
	args: ["--no-sandbox"],
});
ctx = await browser.newContext({
	locale: "zh-CN",
	viewport: { width: 1400, height: 900 },
	extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
	acceptDownloads: true,
});
const webState = process.env.M3_WEB_STATE ?? `${ROOT}/.dev/e2e/state.json`;
if (existsSync(webState))
	await ctx.addCookies(
		(
			JSON.parse(readFileSync(webState, "utf8")) as {
				cookies: Parameters<BrowserContext["addCookies"]>[0];
			}
		).cookies,
	);
await ctx.addInitScript(() => {
	localStorage.setItem("oryn.debug", "1");
	// 面板宽度给足，Files / Changes 走左右分栏
	localStorage.setItem("oryn.panel.width", "760");
});
console.log(
	`M3 验收 run=${RUN} thread=${THREAD} t14=${T14_THREAD} shots=${SHOTS}`,
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
		if (pageA && !pageA.isClosed()) await shot(pageA, `${id}-FAIL`);
		console.log(`${id} FAIL (${((Date.now() - t0) / 1000).toFixed(1)}s)`);
		results.push({ id, verdict: "FAIL", evidence });
	}
}

for (const tid of created) {
	const orb = sql<{ orb_id: string }>(
		`select orb_id from threads where id='${tid}'`,
	)[0]?.orb_id;
	if (orb) {
		const r = await api("DELETE", `/api/v1/orbs/${orb}`);
		console.log(`清理：DELETE orbs/${orb} → ${r.status}`);
	}
}
await browser.close();
await Bun.write(
	`${SHOTS}/results-${RUN}.json`,
	JSON.stringify(results, null, 2),
);
console.log("\n汇总");
for (const r of results) console.log(`${r.id} ${r.verdict}`);
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
