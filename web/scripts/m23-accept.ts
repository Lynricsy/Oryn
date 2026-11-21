#!/usr/bin/env bun
/**
 * M23 真机验收（计划步骤 8.2，B1–B8）：对 dev 部署（orynd + docker provider，`portal_domain: localhost`）逐项执行，
 * 每项打印 `B<n> PASS|FAIL` 与证据。app 用 `cmd/fakeapp`，部署走源线程 orb 里真实的 `oryn boxes deploy`。
 *
 * 三个浏览器上下文：
 *   A：`extraHTTPHeaders` 带设备令牌，驱动主站界面（B1、B2、B5–B8 的操作）；
 *   B：全新上下文，只带主站会话 Cookie `__Host-oryn_session`（`orynd admin create-web-session` 现签），
 *      没有任何 app 主机的 Cookie、不带设备令牌（B3、B4）；
 *   C：什么都不带（无痕访客，B8）。
 *
 * B3 的私有 app 一半验证只凭主站 Cookie 的内嵌预览握手（计划 Assumptions 末条的判据）；它通过，所以没有降级、没有 B3′。
 *
 * 用法（web/ 目录）：bun run accept:m23 [-- --only B1,B2]
 * 前置：make web && make build && make image（或在 oryn-orb:dev 上叠新的 orbd / oryn），重启 orynd 与 provider；
 *   `go build -o <dir>/fakeapp ./cmd/fakeapp`（缺省 /tmp/m23img/fakeapp，M23_FAKEAPP 覆盖；脚本缺它时自己构建）。
 * 环境变量：ORYN_URL、ORYN_ROOT、ORYN_TOKEN（同 m4-accept）；M23_THREAD（源线程；缺省时新建并写入 .dev/m23.thread）；
 *   M23_SESSION（网页会话 Cookie 值；缺省时现签）；CHROMIUM；M23_SHOTS（默认 /tmp/oryn-shots/m23-accept）。
 * 每次运行先删掉上次留下的 box `acc`（连同 app `hello` / `pub`），跑完保留新建的 box 供查看。
 */
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Page,
	type Request,
} from "playwright-core";

const ROOT = process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`;
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const BASE_URL = new URL(BASE);
const TOKEN =
	process.env.ORYN_TOKEN ??
	(
		JSON.parse(readFileSync(`${ROOT}/.dev/credentials.json`, "utf8")) as {
			token: string;
		}
	).token;
const SHOTS = process.env.M23_SHOTS ?? "/tmp/oryn-shots/m23-accept";
const DB = `${ROOT}/.dev/data/oryn.db`;
const FAKEAPP = process.env.M23_FAKEAPP ?? "/tmp/m23img/fakeapp";
const RUN = Math.random().toString(36).slice(2, 6);
const BOX = "acc";
const HELLO = "hello";
const PUB = "pub";
mkdirSync(SHOTS, { recursive: true });

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);

function sh(
	cmd: string,
	stdin?: string | Uint8Array,
): { code: number; out: string; stdout: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], {
		cwd: ROOT,
		stdin:
			stdin === undefined
				? undefined
				: typeof stdin === "string"
					? new TextEncoder().encode(stdin)
					: stdin,
	});
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
		stdout: r.stdout.toString().trim(),
	};
}

function sql<T = Record<string, unknown>>(q: string): T[] {
	const r = Bun.spawnSync(["sqlite3", "-json", DB, q]);
	const s = r.stdout.toString().trim();
	return s ? (JSON.parse(s) as T[]) : [];
}

const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;

/** 在 orb 容器里以 user 身份跑 shell 命令（带 ORYN_THREAD_ID，等同 agent 的环境）。 */
function dexec(orb: string, cmd: string, threadId?: string) {
	const env = threadId ? `-e ORYN_THREAD_ID=${threadId} ` : "";
	return sh(`docker exec ${env}${orb} bash -lc ${q(cmd)}`);
}

function dwrite(orb: string, path: string, content: string | Uint8Array) {
	const r = sh(
		`docker exec -i ${orb} bash -c 'mkdir -p "$(dirname ${path})" && cat > ${path}'`,
		content,
	);
	if (r.code !== 0) throw new Error(`写 ${path} 失败：${r.out}`);
}

async function api(
	method: string,
	path: string,
	body?: unknown,
	headers: Record<string, string> = { Authorization: `Bearer ${TOKEN}` },
): Promise<{ status: number; json: unknown; text: string }> {
	const res = await fetch(`${BASE}${path}`, {
		method,
		headers: {
			...headers,
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

// ---------------------------------------------------------------- 夹具

interface BoxService {
	name: string;
	status: string;
}
interface BoxApp {
	name: string;
	url?: string;
	public: boolean;
	preview: boolean;
	deploying: boolean;
	live?: { id: string; commit: string };
	services: BoxService[];
}
interface BoxView {
	id: string;
	name: string;
	orbId: string;
	state: string;
	desired: string;
	apps: BoxApp[];
}

async function getBox(): Promise<BoxView | undefined> {
	const r = await api("GET", `/api/v1/boxes/${BOX}`);
	return r.status === 200 ? (r.json as BoxView) : undefined;
}

const appOf = (b: BoxView | undefined, name: string) =>
	b?.apps.find((a) => a.name === name);

const appHost = (app: string) => `${app}.${BASE_URL.host}`;
const appURL = (app: string, path = "/") =>
	`${BASE_URL.protocol}//${appHost(app)}${path}`;

/** 删掉上次留下的 box（连同 app），并确认 hello / pub 没有被别的 box 占着。 */
async function cleanBoxes() {
	const list = (
		(await api("GET", "/api/v1/boxes")).json as { boxes: BoxView[] }
	).boxes;
	const mine = list.find((b) => b.name === BOX);
	if (mine) {
		const d = await api("DELETE", `/api/v1/boxes/${mine.id}`);
		if (d.status !== 204)
			throw new Error(`删除旧 box ${BOX} 失败 ${d.status} ${d.text}`);
		console.log(`已删除上次的 box ${BOX}（${mine.id}）`);
	}
	for (const b of list)
		if (b.name !== BOX)
			for (const a of b.apps)
				if (a.name === HELLO || a.name === PUB)
					throw new Error(`app ${a.name} 在别的 box ${b.name} 上，先清理`);
}

async function ensureThread(): Promise<string> {
	if (process.env.M23_THREAD) return process.env.M23_THREAD;
	const f = `${ROOT}/.dev/m23.thread`;
	if (existsSync(f)) {
		const id = readFileSync(f, "utf8").trim();
		const st = sql<{ orb_id: string | null; deleted: number }>(
			`select t.orb_id, coalesce(o.state = 'deleted', 1) as deleted from threads t left join orbs o on o.id = t.orb_id where t.id='${id}'`,
		)[0];
		if (st?.orb_id && !st.deleted) return id;
	}
	const r = await api("POST", "/api/v1/threads", { prompt: "只回复 ok" });
	if (r.status !== 201) throw new Error(`建线程失败 ${r.status} ${r.text}`);
	const id = ((r.json as { thread?: { id: string } }).thread?.id ??
		(r.json as { id: string }).id) as string;
	writeFileSync(f, id);
	return id;
}

let THREAD = "";
let ORB = "";
let BOX_ID = "";
let BOX_THREAD = "";
const PREVIEW_TEXT = (app: string) => `M23 预览 ${app} ${RUN}`;

const BOX_YAML =
	"source: false\nartifacts: [dist]\nservices:\n  web:\n    command: ./fakeapp\n    cwd: dist\n";

/** 在源线程的 orb 里准备 app 仓库 ~/m23/<app>（dist 里放 fakeapp、version.txt、带预览标签的 preview.html）并提交。 */
function prepareRepo(app: string) {
	const dir = `~/m23/${app}`;
	dwrite(ORB, `${dir}/.oryn/box.yaml`, BOX_YAML);
	dwrite(ORB, `${dir}/dist/version.txt`, `${app}-${RUN}\n`);
	dwrite(
		ORB,
		`${dir}/dist/preview.html`,
		`<!doctype html><html><head><meta charset="utf-8"><meta name="oryn-preview" content="v1; enabled; iframe"><title>${app}</title></head><body><h1>${PREVIEW_TEXT(app)}</h1></body></html>`,
	);
	dwrite(ORB, `${dir}/dist/fakeapp`, readFileSync(FAKEAPP));
	const r = dexec(
		ORB,
		`set -e; cd ${dir}; chmod +x dist/fakeapp; [ -d .git ] || git init -q -b main; git add -A; git -c user.name=m23 -c user.email=m23@oryn.test commit -qm "m23 ${RUN}"; git rev-parse --short HEAD`,
	);
	if (r.code !== 0) throw new Error(`准备仓库 ${app} 失败：${r.out}`);
	note(`仓库 ${dir} 提交 ${r.stdout.split("\n").pop()}`);
}

/** 在源线程的 orb 里跑真实的 `oryn boxes deploy`。 */
function deploy(app: string, ...args: string[]) {
	const r = dexec(
		ORB,
		`cd ~/m23/${app} && oryn boxes deploy --on ${BOX} --name ${app} ${args.join(" ")}`,
		THREAD,
	);
	note(
		`deploy ${app} ${args.join(" ")} → exit=${r.code}；${r.out.split("\n").slice(-3).join(" ⏎ ")}`,
	);
	return r;
}

async function waitPreview(app: string) {
	return waitFor(
		`${app} 的 preview = true`,
		async () => (appOf(await getBox(), app)?.preview ? true : undefined),
		120_000,
		1000,
	);
}

async function waitBoxState(state: string, timeoutMs = 600_000) {
	return waitFor(
		`box ${BOX} state = ${state}`,
		async () => {
			const b = await getBox();
			return b?.state === state ? b : undefined;
		},
		timeoutMs,
		1000,
	);
}

// ---------------------------------------------------------------- 浏览器

let ctxA: BrowserContext;
let ctxB: BrowserContext;
let ctxC: BrowserContext;
let SESSION = "";
let pageA: Page | undefined;
let pageB: Page | undefined;
const consoleB: string[] = [];

function wire(page: Page, tag: string, sink?: string[]) {
	page.on("pageerror", (e) =>
		console.log(`    [${tag} pageerror] ${e.message}`),
	);
	page.on("console", (m) => {
		sink?.push(m.text());
		if (/Refused to frame|Refused to display/.test(m.text()))
			console.log(`    [${tag} console] ${m.text()}`);
	});
	return page;
}

async function mainPage(): Promise<Page> {
	if (pageA && !pageA.isClosed()) return pageA;
	pageA = wire(await ctxA.newPage(), "A");
	return pageA;
}

async function cookiePage(): Promise<Page> {
	if (pageB && !pageB.isClosed()) return pageB;
	pageB = wire(await ctxB.newPage(), "B", consoleB);
	return pageB;
}

async function gotoBox(page: Page) {
	await page.goto(`${BASE}/boxes/${BOX_ID}`);
	await page.getByTestId("box-about").waitFor({ timeout: 30_000 });
}

const appRow = (page: Page, app: string) =>
	page.locator(`[data-testid=box-app-row][data-app=${app}]`);

/** 点确认对话框里的动作按钮。 */
async function confirm(page: Page, action: string) {
	const dlg = page.getByRole("dialog");
	await dlg.waitFor({ timeout: 10_000 });
	await dlg.getByRole("button", { name: action, exact: true }).click();
	await dlg.waitFor({ state: "hidden", timeout: 10_000 });
}

/** 指针移到页面角落并等预览浮层卸载。 */
async function leavePreview(page: Page) {
	await page.mouse.move(2, 600);
	await page
		.getByTestId("app-preview-frame")
		.waitFor({ state: "detached", timeout: 5_000 });
}

// ---------------------------------------------------------------- 场景

async function B1() {
	const page = await mainPage();
	await page.goto(`${BASE}/`);
	await page.locator("[data-tab=more]").click();
	await page.locator("[data-more-entry=boxes]").click();
	await page.waitForURL(/\/boxes$/, { timeout: 15_000 });
	note(`「更多 → Boxes」到达 ${new URL(page.url()).pathname}`);
	await shot(page, "B1-boxes-hero");
	await page
		.getByRole("button", { name: /新建 Box/ })
		.first()
		.click();
	const dlg = page.getByRole("dialog");
	await dlg.getByRole("textbox", { name: "名字" }).fill(BOX);
	await shot(page, "B1-new-box-dialog");
	await dlg.getByRole("button", { name: "创建", exact: true }).click();
	await page.waitForURL(/\/boxes\/box_[0-9A-Z]{26}$/, { timeout: 60_000 });
	BOX_ID = new URL(page.url()).pathname.split("/").pop() ?? "";
	note(`进入 box 页 ${new URL(page.url()).pathname}`);
	const about = page.getByTestId("box-about");
	const seen: string[] = [];
	let starting = false;
	await waitFor(
		"box 页状态显示「运行中」",
		async () => {
			const txt = ((await about.textContent()) ?? "").replace(/\s+/g, " ");
			const s = /启动中|运行中|停止中|已停止|启动失败/.exec(txt)?.[0] ?? "?";
			if (seen.at(-1) !== s) seen.push(s);
			if (s === "启动中" && !starting) {
				starting = true;
				await shot(page, "B1-starting");
			}
			return s === "运行中";
		},
		600_000,
		200,
	);
	note(`状态序列 ${seen.join(" → ")}`);
	check(seen.includes("启动中"), "出现过「启动中」");
	check(seen.at(-1) === "运行中", "最终「运行中」");
	await shot(page, "B1-running");
	const b = await getBox();
	check(b?.id === BOX_ID && b.state === "running", `API state=${b?.state}`);
}

async function B2() {
	prepareRepo(HELLO);
	const r = deploy(HELLO, "--private");
	check(r.code === 0, "deploy 退出 0");
	check(
		r.out.includes(`deployed ${HELLO} to ${BOX}`),
		"输出 deployed hello to acc",
	);
	const page = await mainPage();
	await gotoBox(page);
	const row = appRow(page, HELLO);
	await row.waitFor({ timeout: 30_000 });
	await row.getByText("私有", { exact: true }).waitFor({ timeout: 10_000 });
	const link = row.locator(`a[href^="${appURL(HELLO, "")}"]`);
	await link.waitFor({ timeout: 10_000 });
	check(true, `APPS 有 hello，「私有」徽标，地址 ${await link.textContent()}`);
	await shot(page, "B2-hello-private");
}

async function B3() {
	prepareRepo(PUB);
	const r = deploy(PUB);
	check(r.code === 0, "pub 部署退出 0");
	await waitPreview(PUB);
	await waitPreview(HELLO);
	note("API：hello 与 pub 的 preview = true");
	const page = await cookiePage();
	const cookies = await ctxB.cookies();
	check(
		cookies.length === 1 && cookies[0]?.name === "__Host-oryn_session",
		`上下文 B 只有主站 Cookie（${cookies.map((c) => `${c.name}@${c.domain}`).join(", ")}）`,
	);
	await gotoBox(page);
	// 公开的 pub
	await appRow(page, PUB)
		.getByRole("button", { name: `预览 ${PUB}` })
		.hover();
	const frame = page.frameLocator("[data-testid=app-preview-frame]");
	await frame.getByText(PREVIEW_TEXT(PUB)).waitFor({ timeout: 30_000 });
	check(true, `pub 预览帧里出现「${PREVIEW_TEXT(PUB)}」`);
	await shot(page, "B3-preview-pub");
	await leavePreview(page);
	// 私有的 hello：内嵌帧经 /__oryn/login → /portal/auth?h=hello 握手
	const auth: string[] = [];
	const onReq = (req: Request) => {
		if (req.url().includes("/portal/auth?h=hello")) auth.push(req.url());
	};
	page.on("request", onReq);
	const before = consoleB.length;
	try {
		await appRow(page, HELLO)
			.getByRole("button", { name: `预览 ${HELLO}` })
			.hover();
		const ok = await frame
			.getByText(PREVIEW_TEXT(HELLO))
			.waitFor({ timeout: 30_000 })
			.then(() => true)
			.catch(() => false);
		await shot(page, "B3-preview-hello");
		const refused = consoleB
			.slice(before)
			.filter((m) => /Refused to frame/.test(m));
		const src = await page
			.getByTestId("app-preview-frame")
			.getAttribute("src")
			.catch(() => null);
		note(`hello 帧 src ${src}`);
		note(`/portal/auth?h=hello 请求 ${auth.length} 次`);
		for (const m of consoleB.slice(before)) note(`控制台：${m.slice(0, 200)}`);
		check(auth.length > 0, "有一次对 /portal/auth?h=hello 的请求");
		check(refused.length === 0, "控制台没有 Refused to frame");
		check(ok, `hello 预览帧里出现「${PREVIEW_TEXT(HELLO)}」`);
	} finally {
		page.off("request", onReq);
		await leavePreview(page).catch(() => {});
	}
}

async function B4() {
	note("观察项（不是 M23 的通过条件）：只凭 Cookie 的内嵌 Portal 握手");
	const ls = dexec(ORB, "oryn orb service ls --json", THREAD);
	if (!ls.out.includes('"m23demo"')) {
		const s = dexec(
			ORB,
			`oryn orb service start m23demo --command 'python3 -m http.server "$PORT"' --portal`,
			THREAD,
		);
		note(`起 m23demo：exit=${s.code} ${s.stdout.split("\n").pop()}`);
	}
	const page = await cookiePage();
	const auth: string[] = [];
	const onReq = (req: Request) => {
		if (req.url().includes("/portal/auth?t=")) auth.push(req.url());
	};
	page.on("request", onReq);
	const before = consoleB.length;
	try {
		await page.goto(`${BASE}/t/${THREAD}`);
		const tab = page.locator("[data-panel-tab=portal]");
		await tab.waitFor({ timeout: 60_000 });
		await tab.click();
		const frameEl = page.getByTestId("portal-frame");
		await frameEl.waitFor({ timeout: 30_000 });
		note(`Portal 帧 src ${await frameEl.getAttribute("src")}`);
		const shown = await page
			.frameLocator("[data-testid=portal-frame]")
			.getByText("Directory listing for /")
			.waitFor({ timeout: 30_000 })
			.then(() => true)
			.catch(() => false);
		await shot(page, "B4-portal-cookie-only");
		note(`/portal/auth?t= 请求 ${auth.length} 次`);
		for (const m of consoleB.slice(before))
			if (/Refused|cookie|frame/i.test(m)) note(`控制台：${m.slice(0, 200)}`);
		note(
			`观察结果：Portal 面板 iframe ${shown ? "显示出了服务页面（Directory listing）" : "没有显示出服务页面"}`,
		);
	} finally {
		page.off("request", onReq);
	}
}

async function B5() {
	const prev = (await api("GET", "/api/v1/settings/composer")).json as {
		defaultTarget: string;
	};
	PREFS_BEFORE = prev;
	const put = await api("PUT", "/api/v1/settings/composer", {
		...prev,
		defaultTarget: "assistant",
	});
	check(
		put.status === 200,
		`首屏默认对象设为全局助手（原为 ${prev.defaultTarget}）`,
	);
	const page = await mainPage();
	await gotoBox(page);
	const row = appRow(page, HELLO);
	await row.hover();
	await row.getByRole("button", { name: "在这个应用上开线程" }).click();
	await shot(page, "B5-confirm");
	await confirm(page, "开线程");
	await page.waitForURL(`${BASE}/`, { timeout: 15_000 });
	const pills = page.getByTestId("home-pills");
	await pills.waitFor({ timeout: 15_000 });
	check(true, "首屏对象是工作线程（执行位置等胶囊可见，不是全局助手）");
	const pill = pills.getByRole("button", { name: `${BOX} · ${HELLO}` });
	await pill.waitFor({ timeout: 15_000 });
	check(true, `执行位置胶囊是「${BOX} · ${HELLO}」`);
	await shot(page, "B5-home-executor");
	await page.locator("textarea").first().fill("只回复 ok");
	const reqs: string[] = [];
	const onReq = (r: Request) => {
		if (r.method() === "POST" && r.url().includes("/api/v1/"))
			reqs.push(new URL(r.url()).pathname);
	};
	page.on("request", onReq);
	const [req] = await Promise.all([
		page.waitForRequest(
			(r) =>
				r.method() === "POST" &&
				new URL(r.url()).pathname === "/api/v1/threads",
			{ timeout: 15_000 },
		),
		page.getByRole("button", { name: "发送", exact: true }).click(),
	]);
	const body = req.postDataJSON() as {
		executor?: { kind: string; boxId?: string; app?: string };
	};
	note(`POST /api/v1/threads executor=${JSON.stringify(body.executor)}`);
	check(
		body.executor?.kind === "box" &&
			body.executor.boxId === BOX_ID &&
			body.executor.app === HELLO,
		"executor = {kind: box, boxId, app: hello}",
	);
	const res = await req.response();
	check(res?.status() === 201, `响应 ${res?.status()}`);
	const json = (await res?.json()) as { thread?: { id: string }; id?: string };
	BOX_THREAD = json.thread?.id ?? json.id ?? "";
	await page.waitForURL(/\/t\//, { timeout: 15_000 });
	page.off("request", onReq);
	check(
		!reqs.some((p) => p.includes("assistant")),
		`没有发往助手的请求（${reqs.join(", ")}）`,
	);
	note(`新线程 ${BOX_THREAD}`);
}

async function B6() {
	const page = await mainPage();
	if (!new URL(page.url()).pathname.endsWith(BOX_THREAD))
		await page.goto(`${BASE}/t/${BOX_THREAD}`);
	// 页头的 box 胶囊只在信息页不在旁边时出现（信息页旁置时它在信息页里）：先切到文件页
	const files = page.locator("[data-panel-tab=files]");
	await files.waitFor({ timeout: 60_000 });
	await files.click();
	const chip = page.getByTestId("box-chip");
	await chip.waitFor({ timeout: 30_000 });
	const chipText = ((await chip.textContent()) ?? "").trim();
	check(chipText === `${BOX} · ${HELLO}`, `页头显示「${chipText}」`);
	const dist = page.getByRole("treeitem", {
		name: "dist",
		exact: true,
		level: 1,
	});
	await dist.waitFor({ timeout: 30_000 });
	check(
		true,
		`文件面板根下能看到 dist（data-path=${await dist.getAttribute("data-path")}）`,
	);
	await shot(page, "B6-thread-files");
	await page.getByRole("button", { name: "更多操作" }).click();
	await page.getByRole("menuitem", { name: "分叉" }).click();
	const dlg = page.getByRole("dialog");
	await dlg.waitFor({ timeout: 10_000 });
	await dlg.getByText("共用当前 orb").waitFor({ timeout: 5_000 });
	const radios = await dlg.getByRole("radio").count();
	const copy = await dlg.getByText("连环境一起分叉").count();
	await shot(page, "B6-fork-dialog");
	check(
		radios === 1 && copy === 0,
		`fork 对话框只有「共用」（单选 ${radios} 个）`,
	);
	await dlg.getByRole("button", { name: "取消" }).click();
}

async function B7() {
	const page = await mainPage();
	await gotoBox(page);
	await page.getByRole("button", { name: "Box 操作" }).click();
	await page.getByRole("menuitem", { name: "停止 Box" }).click();
	await confirm(page, "停止");
	const b = await waitBoxState("stopped", 180_000);
	note(`box 已停止（desired=${b.desired}）`);
	await page.goto(`${BASE}/t/${BOX_THREAD}`);
	const ta = page.getByRole("textbox", { name: "消息", exact: true });
	await ta.waitFor({ timeout: 30_000 });
	await ta.fill("box 停了还能发吗");
	await ta.press("Enter");
	await page
		.getByText("这台 box 已停止，先在 Boxes 页启动它")
		.first()
		.waitFor({ timeout: 15_000 });
	check(true, "出现 box_stopped 的人话错误");
	await shot(page, "B7-box-stopped-error");
	await page.locator("[data-panel-tab=info]").click();
	await page.getByTestId("box-start-link").first().waitFor({ timeout: 15_000 });
	check(
		(await page.getByRole("button", { name: "唤醒", exact: true }).count()) ===
			0,
		"信息面板没有「唤醒」，有「去 Boxes 页启动」",
	);
	await shot(page, "B7-info-panel");
	await page.locator("[data-panel-tab=files]").click();
	await page.getByTestId("box-start-link").first().waitFor({ timeout: 15_000 });
	check(
		(await page.getByRole("button", { name: /唤醒/ }).count()) === 0,
		"文件面板没有「唤醒」，有「去 Boxes 页启动」",
	);
	await shot(page, "B7-files-panel");
}

async function B8() {
	const page = wire(await ctxC.newPage(), "C");
	await page.goto(appURL(HELLO));
	await page.waitForLoadState("networkidle").catch(() => {});
	const landed = new URL(page.url());
	note(`无痕打开 ${appURL(HELLO)} → ${page.url()}`);
	check(
		landed.host === BASE_URL.host && landed.pathname === "/login",
		"被带去主站登录页",
	);
	check(
		(await page.getByText(PREVIEW_TEXT(HELLO)).count()) === 0,
		"没有看到 app 页面",
	);
	await shot(page, "B8-incognito-login");
	const pa = await mainPage();
	await gotoBox(pa);
	await appRow(pa, HELLO)
		.getByRole("button", { name: `${HELLO} 的操作` })
		.click();
	await pa.getByRole("menuitem", { name: "设为公开" }).click();
	await confirm(pa, "设为公开");
	await waitFor(
		"hello 变为公开",
		async () => appOf(await getBox(), HELLO)?.public,
		15_000,
	);
	await pa.getByRole("button", { name: "Box 操作" }).click();
	await pa.getByRole("menuitem", { name: "启动 Box" }).click();
	await waitBoxState("running");
	await waitFor(
		"hello 的服务 running",
		async () =>
			appOf(await getBox(), HELLO)?.services.some(
				(s) => s.status === "running",
			),
		120_000,
		1000,
	);
	check(true, "UI 里设为公开并启动 box");
	await shot(pa, "B8-box-running-public");
	await page.goto(appURL(HELLO));
	await page.getByText(PREVIEW_TEXT(HELLO)).waitFor({ timeout: 30_000 });
	check(
		new URL(page.url()).host === appHost(HELLO),
		`无痕直接看到页面 ${page.url()}`,
	);
	await shot(page, "B8-incognito-public");
	await page.close();
}

// ---------------------------------------------------------------- 主流程

let PREFS_BEFORE: { defaultTarget: string } | undefined;

const TESTS: [string, () => Promise<void>][] = [
	["B1", B1],
	["B2", B2],
	["B3", B3],
	["B4", B4],
	["B5", B5],
	["B6", B6],
	["B7", B7],
	["B8", B8],
];

if (!existsSync(FAKEAPP)) {
	mkdirSync(FAKEAPP.slice(0, FAKEAPP.lastIndexOf("/")), { recursive: true });
	const b = sh(
		`CGO_ENABLED=0 go build -trimpath -o ${q(FAKEAPP)} ./cmd/fakeapp`,
	);
	if (b.code !== 0) throw new Error(`构建 fakeapp 失败：${b.out}`);
}

THREAD = await ensureThread();
await api("POST", `/api/v1/threads/${THREAD}/wake`);
await waitFor(
	"源线程有 orb 且 orb ready",
	() => {
		const r = sql<{ orb_id: string | null; state: string }>(
			`select t.orb_id, o.state from threads t join orbs o on o.id = t.orb_id where t.id='${THREAD}'`,
		)[0];
		if (!r?.orb_id) return undefined;
		ORB = r.orb_id;
		return r.state === "ready" ? ORB : undefined;
	},
	600_000,
	2000,
);
if (!ONLY || ONLY.has("B1")) await cleanBoxes();
else {
	const b = await getBox();
	if (!b) throw new Error(`--only 跳过 B1 时需要已有的 box ${BOX}`);
	BOX_ID = b.id;
	BOX_THREAD = process.env.M23_BOX_THREAD ?? "";
}
SESSION =
	process.env.M23_SESSION ??
	sh("./bin/orynd --config dev/orynd.yaml admin create-web-session").out.trim();
if (!SESSION) throw new Error("没有网页会话");

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
ctxB = await browser.newContext({
	locale: "zh-CN",
	viewport: { width: 1400, height: 900 },
});
await ctxB.addCookies([
	{
		name: "__Host-oryn_session",
		value: SESSION,
		// 不带前导点的 domain 在 CDP 里就是 host-only，满足 __Host- 前缀（url 形式会被 CDP 拒收）
		domain: BASE_URL.hostname,
		path: "/",
		secure: true,
		httpOnly: true,
		sameSite: "Lax",
	},
]);
ctxC = await browser.newContext({ locale: "zh-CN" });
console.log(`M23 验收 run=${RUN} thread=${THREAD} orb=${ORB} shots=${SHOTS}`);

try {
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
			if (pageA && !pageA.isClosed()) await shot(pageA, `${id}-FAIL-A`);
			if (pageB && !pageB.isClosed()) await shot(pageB, `${id}-FAIL-B`);
			console.log(`${id} FAIL (${((Date.now() - t0) / 1000).toFixed(1)}s)`);
			results.push({ id, verdict: "FAIL", evidence });
		}
	}
} finally {
	if (PREFS_BEFORE) {
		await api("PUT", "/api/v1/settings/composer", PREFS_BEFORE);
		console.log(`首屏默认对象已恢复为 ${PREFS_BEFORE.defaultTarget}`);
	}
	await browser.close();
}
await Bun.write(
	`${SHOTS}/results-${RUN}.json`,
	JSON.stringify(results, null, 2),
);
console.log(`\n汇总（box ${BOX} ${BOX_ID} 保留；box 线程 ${BOX_THREAD}）`);
for (const r of results) console.log(`${r.id} ${r.verdict}`);
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
