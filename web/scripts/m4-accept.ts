#!/usr/bin/env bun
/**
 * M4 真机验收（计划「验证」P1–P16）：对 dev 部署（orynd + docker provider，`portal_domain: localhost`）逐项执行，
 * 每项打印 `P<n> PASS|FAIL` 与证据。
 *
 * 三个浏览器上下文：
 *   A：`extraHTTPHeaders` 带设备令牌，驱动主站界面；
 *   B：只带主站会话 Cookie `oryn_session`（`orynd admin create-web-session` 现签），走真实握手；
 *   C：什么都不带（匿名访客）。
 * 命令行请求一律 `curl -H "Host: <portal 主机>" http://127.0.0.1:7600/…`，不依赖本机对 *.localhost 的解析。
 *
 * 用法（web/ 目录）：bun run accept:m4 [-- --only P1,P2]
 * 环境变量：ORYN_URL、ORYN_ROOT、ORYN_TOKEN（同 m3-accept）；
 *   M4_THREAD（sandbox-services 项目、新镜像上的线程；缺省时新建并写入 .dev/m4.thread）；
 *   P16_THREAD（M4 之前镜像上的线程；默认 .dev/m3fresh.thread）；
 *   M4_SESSION（网页会话 Cookie 值；缺省时现签）；CHROMIUM；M4_SHOTS（默认 /tmp/oryn-shots/m4-accept）。
 */
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Page,
} from "playwright-core";

const ROOT = process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`;
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const BASE_HOST = new URL(BASE).host; // localhost:7600
const TOKEN =
	process.env.ORYN_TOKEN ??
	(
		JSON.parse(readFileSync(`${ROOT}/.dev/credentials.json`, "utf8")) as {
			token: string;
		}
	).token;
const SHOTS = process.env.M4_SHOTS ?? "/tmp/oryn-shots/m4-accept";
const DB = `${ROOT}/.dev/data/oryn.db`;
const RUN = Math.random().toString(36).slice(2, 6);
const PROJECT = "sandbox-services";
const SANDBOX_REPO = "https://github.com/Lynricsy/oryn-setup-sandbox.git";
const SANDBOX_BRANCH = "m4-services";
mkdirSync(SHOTS, { recursive: true });

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);

function sh(cmd: string, stdin?: string): { code: number; out: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], {
		cwd: ROOT,
		stdin: stdin !== undefined ? new TextEncoder().encode(stdin) : undefined,
	});
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

function orbRow(orbId: string) {
	return sql<{ state: string; last_user_interaction_at: number }>(
		`select state, last_user_interaction_at from orbs where id='${orbId}'`,
	)[0];
}

/** 在 orb 容器里以 user 身份跑 shell 命令（带 ORYN_THREAD_ID，等同 agent 的环境）。 */
function dexec(orb: string, cmd: string, threadId?: string) {
	const env = threadId ? `-e ORYN_THREAD_ID=${threadId} ` : "";
	return sh(
		`docker exec ${env}${orb} bash -lc '${cmd.replaceAll("'", "'\\''")}'`,
	);
}

function dwrite(orb: string, path: string, content: string) {
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

interface CurlRes {
	status: number;
	headers: Record<string, string>;
	body: string;
}

/** 经 127.0.0.1 带 Host 头请求 portal（不跟随重定向）。 */
function curl(host: string, path: string, args: string[] = []): CurlRes {
	const hdrFile = `/tmp/m4-curl-${RUN}.h`;
	const quoted = args.map((a) => `'${a.replaceAll("'", "'\\''")}'`).join(" ");
	const r = sh(
		`curl -s -m 60 -D ${hdrFile} -H 'Host: ${host}' ${quoted} 'http://127.0.0.1:${new URL(BASE).port || 80}${path}'; echo; cat ${hdrFile}`,
	);
	const raw = readFileSync(hdrFile, "utf8");
	const blocks = raw.split(/\r?\n\r?\n/).filter((b) => b.trim());
	const last = blocks[blocks.length - 1] ?? "";
	const lines = last.split(/\r?\n/);
	const status = Number(lines[0]?.split(" ")[1] ?? 0);
	const headers: Record<string, string> = {};
	for (const l of lines.slice(1)) {
		const i = l.indexOf(":");
		if (i > 0)
			headers[l.slice(0, i).trim().toLowerCase()] = l.slice(i + 1).trim();
	}
	const body = r.out.slice(0, r.out.length - raw.trim().length).trim();
	return { status, headers, body };
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

async function ensureThread(): Promise<string> {
	if (process.env.M4_THREAD) return process.env.M4_THREAD;
	const f = `${ROOT}/.dev/m4.thread`;
	if (existsSync(f)) {
		const id = readFileSync(f, "utf8").trim();
		const st = sql<{ orb_id: string | null }>(
			`select orb_id from threads where id='${id}'`,
		)[0];
		if (st?.orb_id) return id;
	}
	const projectId = await ensureProject();
	const r = await api("POST", "/api/v1/threads", {
		projectId,
		prompt: "只回复 ok",
	});
	if (r.status !== 201) throw new Error(`建线程失败 ${r.status} ${r.text}`);
	const id = ((r.json as { thread?: { id: string } }).thread?.id ??
		(r.json as { id: string }).id) as string;
	writeFileSync(f, id);
	return id;
}

const P16_THREAD =
	process.env.P16_THREAD ??
	readFileSync(`${ROOT}/.dev/m3fresh.thread`, "utf8").trim();

let THREAD = "";
let CODE = "";
let ORB = "";
const H = (port: number) => `t-${CODE}-p${port}.${BASE_HOST}`;
const portalURL = (port: number, path = "/") => `http://${H(port)}${path}`;

interface SvcInfo {
	name: string;
	status: string;
	port?: number;
	pid?: number;
	restarts: number;
	source: string;
	links: { title: string; path: string }[];
}

function services(): SvcInfo[] {
	const r = dexec(ORB, "oryn orb service ls --json", THREAD);
	if (r.code !== 0) throw new Error(`service ls 失败：${r.out}`);
	const j = JSON.parse(r.out) as SvcInfo[] | { services: SvcInfo[] };
	return Array.isArray(j) ? j : j.services;
}

const svc = (name: string) => services().find((s) => s.name === name);

async function waitRunning(name: string, timeoutMs = 60_000) {
	return waitFor(
		`服务 ${name} running`,
		() => {
			const s = svc(name);
			return s?.status === "running" ? s : undefined;
		},
		timeoutMs,
		500,
	);
}

/** 起一个动态服务，返回打印的 URL 与端口。 */
function startService(name: string, command: string, portal = true) {
	const r = dexec(
		ORB,
		`oryn orb service start ${name} --command ${JSON.stringify(command)}${portal ? " --portal" : ""}`,
		THREAD,
	);
	const last = r.out.trim().split("\n").pop() ?? "";
	return { code: r.code, out: r.out, last };
}

async function ensureService(name: string, command: string): Promise<number> {
	const s = svc(name);
	if (s?.status !== "running") {
		const r = startService(name, command);
		if (r.code !== 0 && !/name_conflict|already/.test(r.out))
			throw new Error(`起 ${name} 失败：${r.out}`);
	}
	const run = await waitRunning(name);
	if (!run.port) throw new Error(`${name} 无端口`);
	return run.port;
}

const HDR_PY = `import http.server, json, os
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        b = json.dumps({k: v for k, v in self.headers.items()}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("X-Frame-Options", "DENY")
        self.send_header("Set-Cookie", "app=1; Path=/")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)
    do_POST = do_GET
    def log_message(self, *a):
        pass
http.server.ThreadingHTTPServer(("127.0.0.1", int(os.environ["PORT"])), H).serve_forever()
`;
const WS_JS = `Bun.serve({
  port: Number(process.env.PORT), hostname: "127.0.0.1",
  fetch(req, srv) { if (srv.upgrade(req)) return; return new Response("<!doctype html><title>ws</title>ws", { headers: { "content-type": "text/html" } }); },
  websocket: { message(ws, m) { ws.send("echo:" + m); } },
});
`;

const DEMO_CMD = 'python3 -m http.server "$PORT"';
const HDR_CMD = "python3 $HOME/m4/hdr.py";
const WS_CMD = "bun $HOME/m4/ws.js";

// ---------------------------------------------------------------- 浏览器

let ctxA: BrowserContext;
let ctxB: BrowserContext;
let ctxC: BrowserContext;
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

async function mainPage(): Promise<Page> {
	if (pageA && !pageA.isClosed()) return pageA;
	pageA = wire(await ctxA.newPage(), "A");
	await pageA.goto(`${BASE}/t/${THREAD}`);
	return pageA;
}

async function openPortalTab(page: Page) {
	const btn = page.locator("[data-panel-tab=portal]");
	await btn.waitFor({ timeout: 60_000 });
	if ((await btn.getAttribute("aria-selected")) !== "true") await btn.click();
}

/** 在页面里开一条 WebSocket，返回句柄名；消息与关闭事件记在 window 上。 */
async function openWs(page: Page, url: string, key: string) {
	await page.evaluate(
		([u, k]) =>
			new Promise<void>((res, rej) => {
				const w = window as unknown as Record<string, unknown>;
				const ws = new WebSocket(u as string);
				const st = { msgs: [] as string[], closed: false, ws };
				w[k as string] = st;
				ws.onmessage = (e) => st.msgs.push(String(e.data));
				ws.onclose = () => {
					st.closed = true;
				};
				ws.onopen = () => res();
				ws.onerror = () => rej(new Error("ws error"));
			}),
		[url, key],
	);
}

const wsState = (page: Page, key: string) =>
	page.evaluate((k) => {
		const st = (window as unknown as Record<string, unknown>)[k] as {
			msgs: string[];
			closed: boolean;
		};
		return { msgs: st.msgs, closed: st.closed };
	}, key);

const wsSend = (page: Page, key: string, m: string) =>
	page.evaluate(
		([k, msg]) =>
			(
				(window as unknown as Record<string, unknown>)[k as string] as {
					ws: WebSocket;
				}
			).ws.send(msg as string),
		[key, m],
	);

// ---------------------------------------------------------------- 场景

let DEMO_PORT = 0;
let HDR_PORT = 0;
let WS_PORT = 0;

async function P1() {
	// 先清掉上次遗留的 demo，确保看到首次启动的打印
	dexec(ORB, "oryn orb service stop demo --remove", THREAD);
	const r = startService("demo", DEMO_CMD);
	note(`exit=${r.code} 末行 ${r.last}`);
	check(r.code === 0, "退出码 0");
	const m = new RegExp(
		`^http://t-${CODE}-p(\\d+)\\.${BASE_HOST.replace(".", "\\.")}/$`,
	).exec(r.last);
	check(!!m, "末行是 portal URL");
	DEMO_PORT = Number(m?.[1]);
	const s = await waitRunning("demo");
	check(s.port === DEMO_PORT, `ls 中 demo running，端口 ${s.port}`);
	const row = await waitFor(
		"orb_services 有 demo",
		() =>
			sql<{ has_portal: number; port: number }>(
				`select has_portal, port from orb_services where orb_id='${ORB}' and name='demo'`,
			)[0],
		15_000,
	);
	check(row.has_portal === 1, "orb_services.has_portal=1");
}

async function P2() {
	const before = orbRow(ORB)?.last_user_interaction_at;
	const html = curl(H(DEMO_PORT), "/a?b=1", ["-H", "Accept: text/html"]);
	const want = `${BASE}/portal/auth?t=${CODE}&p=${DEMO_PORT}&next=%2Fa%3Fb%3D1`;
	note(`HTML → ${html.status} Location ${html.headers.location}`);
	check(html.status === 302, "导航请求 302");
	check(html.headers.location === want, "Location 指向主域名握手");
	const js = curl(H(DEMO_PORT), "/a?b=1", ["-H", "Accept: application/json"]);
	note(`JSON → ${js.status} ${js.body.slice(0, 120)}`);
	check(js.status === 401, "非导航请求 401");
	check(js.body.includes("portal_unauthorized"), "code=portal_unauthorized");
	check(
		orbRow(ORB)?.last_user_interaction_at === before,
		"last_user_interaction_at 未变",
	);
}

async function P3() {
	const page = wire(await ctxB.newPage(), "B");
	try {
		const authHits: string[] = [];
		page.on("request", (r) => {
			if (r.url().includes("/portal/auth")) authHits.push(r.url());
		});
		await page.goto(portalURL(DEMO_PORT));
		await page.getByText("Directory listing for /").waitFor({
			timeout: 30_000,
		});
		check(
			page.url() === portalURL(DEMO_PORT),
			`最终 URL 仍是 portal（${page.url()}）`,
		);
		check(authHits.length > 0, "首次访问经过 /portal/auth");
		const ck = (await ctxB.cookies(portalURL(DEMO_PORT))).find(
			(c) => c.name === "__Host-oryn_portal",
		);
		note(
			`cookie ${ck ? `${ck.name} httpOnly=${ck.httpOnly} secure=${ck.secure} sameSite=${ck.sameSite}` : "无"}`,
		);
		check(
			!!ck && ck.httpOnly && ck.secure && ck.sameSite === "None",
			"__Host-oryn_portal（HttpOnly/Secure/SameSite=None）",
		);
		authHits.length = 0;
		await page.goto(portalURL(DEMO_PORT));
		await page.getByText("Directory listing for /").waitFor();
		check(authHits.length === 0, "再次访问不再经过主域名");
		await shot(page, "P3-handshake");
	} finally {
		await page.close();
	}
}

async function P4() {
	const page = await mainPage();
	await openPortalTab(page);
	const frame = page.frameLocator("[data-testid=portal-frame]");
	await frame.getByText("Directory listing for /").first().waitFor({
		timeout: 60_000,
	});
	check(true, "iframe 内看到目录列表");
	const pill = await page.locator("[data-testid=portal-url]").textContent();
	check(!!pill?.includes(`t-${CODE}-p`), `URL 胶囊「${pill?.trim()}」`);
	const sandbox =
		(await page
			.locator("[data-testid=portal-frame]")
			.getAttribute("sandbox")) ?? "";
	check(
		!sandbox.includes("allow-top-navigation"),
		`sandbox 不含 allow-top-navigation（${sandbox}）`,
	);
	await shot(page, "P4-portal");
	// 只剩 web（文件服务）时标签仍在；把 demo 移除后，若它是唯一 portal 则标签消失
	const others = services().filter(
		(s) => s.name !== "demo" && s.links.length > 0,
	);
	dexec(ORB, "oryn orb service stop demo --remove", THREAD);
	if (others.length === 0) {
		await page
			.locator("[data-panel-tab=portal]")
			.waitFor({ state: "detached", timeout: 30_000 });
		const info = await page
			.locator("[data-panel-tab=info]")
			.getAttribute("aria-selected");
		check(info === "true", "标签消失并退回信息标签");
	} else {
		note(
			`仍有其它 portal 服务（${others.map((s) => s.name).join(",")}），标签保留`,
		);
		await waitFor(
			"demo 从 portal 链接里消失",
			async () => {
				const r = await api("GET", `/api/v1/orbs/${ORB}/services`);
				const ps = (r.json as { portals: { service: string }[] }).portals;
				return !ps.some((p) => p.service === "demo");
			},
			30_000,
			500,
		);
		check(true, "demo 的 portal 已撤下");
	}
	const again = startService("demo", DEMO_CMD);
	check(again.code === 0, "重新 start demo");
	DEMO_PORT = (await waitRunning("demo")).port ?? DEMO_PORT;
}

async function P5() {
	dwrite(ORB, "/home/user/m4/hdr.py", HDR_PY);
	HDR_PORT = await ensureService("hdr", HDR_CMD);
	const r = curl(H(HDR_PORT), "/", [
		"-H",
		`Authorization: Bearer ${TOKEN}`,
		"-H",
		"Cookie: foo=bar",
	]);
	note(`令牌请求 → ${r.status}`);
	check(r.status === 200, "200");
	const echo = JSON.parse(r.body) as Record<string, string>;
	const get = (k: string) =>
		Object.entries(echo).find(([n]) => n.toLowerCase() === k)?.[1];
	check(
		get("x-oryn-authenticated") === "owner=yes",
		"X-Oryn-Authenticated: owner=yes",
	);
	check(
		get("x-forwarded-host") === H(HDR_PORT),
		`X-Forwarded-Host = ${get("x-forwarded-host")}`,
	);
	check(get("cookie") === "foo=bar", `Cookie 只剩 foo=bar（${get("cookie")}）`);
	check(get("authorization") === undefined, "无 Authorization");
	check(!r.headers["x-frame-options"], "响应去掉 X-Frame-Options");
	const csp = r.headers["content-security-policy"] ?? "";
	check(
		csp.includes("frame-ancestors 'self' http://localhost:7600"),
		`CSP ${csp}`,
	);
	const page = wire(await ctxB.newPage(), "B");
	try {
		const resp = await page.goto(portalURL(HDR_PORT));
		const body = (await resp?.text()) ?? "";
		const e2 = JSON.parse(body) as Record<string, string>;
		const cookieB =
			Object.entries(e2).find(([n]) => n.toLowerCase() === "cookie")?.[1] ?? "";
		check(
			!cookieB.includes("__Host-oryn_portal"),
			`B 的回显里没有 portal Cookie（${cookieB}）`,
		);
		const app = (await ctxB.cookies(portalURL(HDR_PORT))).find(
			(c) => c.name === "app",
		);
		check(!!app, "app=1 被浏览器收下");
	} finally {
		await page.close();
	}
}

async function P6() {
	dwrite(ORB, "/home/user/m4/ws.js", WS_JS);
	WS_PORT = await ensureService("ws", WS_CMD);
	const page = wire(await ctxB.newPage(), "B");
	try {
		await page.goto(portalURL(WS_PORT));
		await openWs(page, `ws://${H(WS_PORT)}/`, "w1");
		await wsSend(page, "w1", "hi");
		await waitFor(
			"echo:hi",
			async () => (await wsState(page, "w1")).msgs.includes("echo:hi"),
			10_000,
		);
		check(true, "收到 echo:hi");
		await sleep(30_000);
		check(!(await wsState(page, "w1")).closed, "保持 30s 不断");
	} finally {
		await page.close();
	}
}

async function shareViaUI(page: Page, port: number, label: string) {
	await openPortalTab(page);
	// 切到对应端口的服务
	const pick = page.locator("[data-testid=portal-service-select]");
	if (await pick.isVisible().catch(() => false)) {
		await pick.click();
		await page.locator(`[data-portal-port="${port}"]`).first().click();
	}
	await page.locator("[data-testid=portal-access]").click();
	await page.getByRole("button", { name: label }).click();
	const confirm = page.locator("[data-testid=portal-share-confirm]");
	if (await confirm.isVisible().catch(() => false)) await confirm.click();
}

async function P7() {
	const page = await mainPage();
	await shareViaUI(page, HDR_PORT, "1 小时");
	const row = await waitFor(
		"portal_shares 有 hdr",
		() =>
			sql<{ expires_at: number }>(
				`select expires_at from portal_shares where orb_id='${ORB}' and port=${HDR_PORT}`,
			)[0],
		15_000,
	);
	note(`expires_at=${new Date(row.expires_at).toISOString()}`);
	await shot(page, "P7-shared");
	const pageC = wire(await ctxC.newPage(), "C");
	try {
		const resp = await pageC.goto(portalURL(HDR_PORT));
		check(resp?.status() === 200, `匿名访问 hdr ${resp?.status()}`);
		const e = JSON.parse((await resp?.text()) ?? "{}") as Record<
			string,
			string
		>;
		const auth = Object.entries(e).find(
			([n]) => n.toLowerCase() === "x-oryn-authenticated",
		)?.[1];
		check(auth === "owner=no", "回显 owner=no");
		const robots = (await resp?.allHeaders())?.["x-robots-tag"] ?? "";
		check(robots.includes("noindex"), `X-Robots-Tag ${robots}`);
	} finally {
		await pageC.close();
	}
	const demo = await ctxC.request.get(portalURL(DEMO_PORT), {
		maxRedirects: 0,
		headers: { Accept: "text/html" },
	});
	check(demo.status() === 302, `demo 仍 302（${demo.status()}）`);
	const th = await fetch(`${BASE}/api/v1/threads/${THREAD}`);
	check(th.status === 401, `线程接口 401（${th.status}）`);
}

async function P8() {
	const page = await mainPage();
	const r = await api("PUT", `/api/v1/orbs/${ORB}/portals/${WS_PORT}/share`, {
		duration: "1h",
	});
	check(r.status === 200, `公开 ws（${r.status}）`);
	const pageC = wire(await ctxC.newPage(), "C");
	try {
		await pageC.goto(portalURL(WS_PORT));
		await openWs(pageC, `ws://${H(WS_PORT)}/`, "c1");
		const e0 = sql<{ expires_at: number }>(
			`select expires_at from portal_shares where orb_id='${ORB}' and port=${WS_PORT}`,
		)[0].expires_at;
		await shareViaUI(page, WS_PORT, "3 小时");
		const e1 = await waitFor(
			"expires_at 变大",
			() => {
				const v = sql<{ expires_at: number }>(
					`select expires_at from portal_shares where orb_id='${ORB}' and port=${WS_PORT}`,
				)[0]?.expires_at;
				return v && v > e0 ? v : undefined;
			},
			15_000,
		);
		note(`续期 ${e0} → ${e1}`);
		check(!(await wsState(pageC, "c1")).closed, "续期后 WebSocket 不断");
		await page.locator("[data-testid=portal-access]").click();
		await page.locator("[data-testid=portal-share-end]").click();
		const t0 = Date.now();
		await waitFor(
			"C 的 WebSocket 关闭",
			async () => (await wsState(pageC, "c1")).closed,
			5_000,
			100,
		);
		const dt = Date.now() - t0;
		check(dt <= 2_000, `终止后 ${dt}ms 内断开`);
		const again = await ctxC.request.get(portalURL(WS_PORT), {
			maxRedirects: 0,
			headers: { Accept: "text/html" },
		});
		check(again.status() === 302, `再访问 302（${again.status()}）`);
		const left = sql(
			`select 1 from portal_shares where orb_id='${ORB}' and port=${WS_PORT}`,
		);
		check(left.length === 0, "portal_shares 无该行");
	} finally {
		await pageC.close();
	}
	// hdr 的公开也撤掉，免得影响后续
	await api("DELETE", `/api/v1/orbs/${ORB}/portals/${HDR_PORT}/share`);
}

async function P9() {
	const pid0 = svc("demo")?.pid;
	const r = await api("POST", `/api/v1/orbs/${ORB}/pause`, { mode: "freeze" });
	check(r.status < 300, `pause freeze ${r.status}`);
	await waitFor("frozen", () => orbRow(ORB)?.state === "frozen", 60_000, 500);
	const evt0 = sql<{ id: number }>(
		`select max(id) id from orb_events where orb_id='${ORB}'`,
	)[0].id;
	const c = await ctxC.request.get(portalURL(DEMO_PORT), {
		maxRedirects: 0,
		headers: { Accept: "text/html" },
	});
	check(c.status() === 302, `匿名 302（${c.status()}）`);
	check(orbRow(ORB)?.state === "frozen", "orb 仍 frozen");
	const page = wire(await ctxB.newPage(), "B");
	try {
		await page.goto(portalURL(DEMO_PORT));
		const first = await page.content();
		note(`首屏 ${first.includes("Directory listing") ? "已是目录" : "过渡页"}`);
		await shot(page, "P9-waking");
		await page.getByText("Directory listing for /").waitFor({
			timeout: 30_000,
		});
		check(true, "30s 内变成目录列表");
	} finally {
		await page.close();
	}
	const seq = sql<{ s: string }>(
		`select group_concat(from_state||'>'||to_state, ' ') s from (select * from orb_events where orb_id='${ORB}' and id>${evt0} order by id)`,
	)[0]?.s;
	note(`orb_events ${seq}`);
	check(
		/frozen>waking.*waking>connecting.*connecting>resuming.*resuming>ready/.test(
			seq ?? "",
		),
		"frozen→waking→connecting→resuming→ready",
	);
	const pid1 = (await waitRunning("demo")).pid;
	note(`demo pid ${pid0} → ${pid1}`);
	check(pid0 === pid1, "冻结前后 pid 相同");
}

async function P10() {
	await waitFor(
		"线程 idle",
		() =>
			sql<{ state: string }>(
				`select state from threads where id='${THREAD}'`,
			)[0]?.state === "idle",
		120_000,
		1000,
	);
	const t0 = Date.now();
	while (Date.now() - t0 < 90_000) {
		const r = curl(H(DEMO_PORT), "/", ["-H", `Authorization: Bearer ${TOKEN}`]);
		if (r.status !== 200) note(`请求 ${r.status}`);
		const st = orbRow(ORB)?.state;
		if (st !== "ready")
			throw new Error(`第 ${Math.round((Date.now() - t0) / 1000)}s orb ${st}`);
		await sleep(5_000);
	}
	check(true, "90s 内一直 ready");
	const t1 = Date.now();
	await waitFor(
		"停止请求后冻结",
		() => orbRow(ORB)?.state === "frozen",
		120_000,
		1000,
	);
	note(`停止后 ${Math.round((Date.now() - t1) / 1000)}s 冻结`);
	check(Date.now() - t1 <= 100_000, "约 60s 内冻结（含评估周期）");
	// 解冻供后续使用
	await api("POST", `/api/v1/threads/${THREAD}/wake`);
	await waitFor("ready", () => orbRow(ORB)?.state === "ready", 60_000, 500);
}

async function P11() {
	const web = await waitRunning("web", 120_000);
	check(web.source === "file", "web source=file");
	const page = await mainPage();
	await openPortalTab(page);
	await page.locator("[data-testid=portal-links]").click();
	const items = await page.locator("[data-portal-link]").allTextContents();
	note(`链接菜单 ${items.join(" | ")}`);
	check(
		items.some((t) => t.includes("web")) &&
			items.some((t) => t.includes("Docs")),
		"链接菜单有 web 与 Docs",
	);
	await page.keyboard.press("Escape");
	const env = dexec(ORB, `tr '\\0' '\\n' < /proc/${web.pid}/environ`).out;
	const has = (k: string) => env.split("\n").find((l) => l.startsWith(`${k}=`));
	note(`${has("PORT")} ${has("PUBLIC_URL")} ${has("ORYN_THREAD_ID")}`);
	check(!!has("PORT"), "PORT");
	check(
		/^PUBLIC_URL=http:\/\/t-[0-9a-f]{12}-p\d+\.localhost:7600\/?$/.test(
			has("PUBLIC_URL") ?? "",
		),
		"PUBLIC_URL",
	);
	check(!!has("ORYN_THREAD_ID"), "ORYN_THREAD_ID");
	check(!env.includes("ORYN_ORB_TOKEN"), "不含 ORYN_ORB_TOKEN");
}

async function P12() {
	const web0 = await waitRunning("web");
	dexec(ORB, `kill -9 ${web0.pid}`);
	const web1 = await waitFor(
		"web 重启",
		() => {
			const s = svc("web");
			return s?.status === "running" && s.pid !== web0.pid ? s : undefined;
		},
		30_000,
		300,
	);
	check(web1.restarts >= 1, `restarts=${web1.restarts}`);
	const logs = dexec(ORB, "oryn orb service logs web -n 20", THREAD).out;
	check(logs.includes("[orbd] exited"), "日志含 [orbd] exited");
	check(logs.includes("[orbd] starting"), "日志含 [orbd] starting");
	const page = await mainPage();
	const info = page.locator("[data-panel-tab=info]");
	if ((await info.getAttribute("aria-selected")) !== "true") await info.click();
	const row = page.locator('[data-service="web"]');
	await row.waitFor({ timeout: 15_000 });
	note(`信息面板行「${(await row.textContent())?.trim()}」`);
	await row.locator("[data-testid=service-log]").click();
	const dlg = page.getByRole("dialog");
	await dlg.waitFor();
	const txt = (await dlg.textContent()) ?? "";
	check(txt.includes("[orbd]"), "日志对话框有内容");
	await shot(page, "P12-log");
	await page.keyboard.press("Escape");
}

async function P13() {
	const before = Object.fromEntries(services().map((s) => [s.name, s]));
	let r = await api("POST", `/api/v1/orbs/${ORB}/pause`, { mode: "stop" });
	check(r.status < 300, `pause stop ${r.status}`);
	await waitFor(
		"stopped",
		() => orbRow(ORB)?.state === "stopped",
		120_000,
		1000,
	);
	r = await api("POST", `/api/v1/orbs/${ORB}/resume`);
	check(r.status < 300, `resume ${r.status}`);
	await waitFor("ready", () => orbRow(ORB)?.state === "ready", 180_000, 1000);
	for (const name of ["web", "demo"]) {
		const s = await waitRunning(name, 120_000);
		note(
			`${name} pid ${before[name]?.pid} → ${s.pid} 端口 ${before[name]?.port} → ${s.port}`,
		);
		check(s.pid !== before[name]?.pid, `${name} pid 变了`);
		check(s.port === before[name]?.port, `${name} 端口不变`);
	}
	const page = wire(await ctxB.newPage(), "B");
	try {
		await page.goto(portalURL(DEMO_PORT));
		await page
			.getByText("Directory listing for /")
			.waitFor({ timeout: 30_000 });
		check(true, "B 访问 demo 正常");
	} finally {
		await page.close();
	}
}

async function P15() {
	// ① egress 端口
	const e = curl(H(7811), "/llm/x/v1/models", [
		"-H",
		`Authorization: Bearer ${TOKEN}`,
	]);
	note(`① 7811 → ${e.status} ${e.body.slice(0, 80)}`);
	check(e.status === 403 || e.status === 404, "egress 端口 403/404");
	// ② 跨站 POST
	const cookie = (await ctxB.cookies(portalURL(DEMO_PORT))).find(
		(c) => c.name === "__Host-oryn_portal",
	);
	check(!!cookie, "B 有 portal Cookie");
	const x = curl(H(DEMO_PORT), "/", [
		"-X",
		"POST",
		"-H",
		"Sec-Fetch-Site: cross-site",
		"-H",
		`Cookie: __Host-oryn_portal=${cookie?.value}`,
	]);
	note(`② → ${x.status} ${x.body.slice(0, 80)}`);
	check(
		x.status === 403 && x.body.includes("cross_site_blocked"),
		"403 cross_site_blocked",
	);
	// ③ 非法参数
	const bad = await api("GET", "/portal/auth?t=zz&p=1", undefined, {
		Cookie: `oryn_session=${SESSION}`,
	});
	check(bad.status === 400, `③ 400（${bad.status}）`);
	// ④ 开放重定向
	const page = wire(await ctxB.newPage(), "B");
	try {
		await page.goto(
			`${BASE}/portal/auth?t=${CODE}&p=${DEMO_PORT}&next=${encodeURIComponent("//evil.example")}`,
		);
		await page
			.getByText("Directory listing for /")
			.waitFor({ timeout: 30_000 });
		check(
			page.url() === portalURL(DEMO_PORT),
			`④ 回到 portal 的 /（${page.url()}）`,
		);
	} finally {
		await page.close();
	}
	// ⑤ portal 主机上打主站 API
	const me = curl(H(DEMO_PORT), "/api/v1/me", [
		"-H",
		`Authorization: Bearer ${TOKEN}`,
	]);
	note(`⑤ → ${me.status} ${me.body.slice(0, 80)}`);
	check(
		!me.body.includes('"displayName"') && !me.body.includes('"owner"'),
		"⑤ 不是主站 API 的响应",
	);
}

async function P14() {
	const page = wire(await ctxB.newPage(), "B");
	try {
		await page.goto(portalURL(WS_PORT));
		await openWs(page, `ws://${H(WS_PORT)}/`, "b1");
		const r = await api("POST", "/api/v1/auth/logout", undefined, {
			Cookie: `oryn_session=${SESSION}`,
		});
		note(`logout ${r.status}`);
		const t0 = Date.now();
		await waitFor(
			"B 的 WebSocket 关闭",
			async () => (await wsState(page, "b1")).closed,
			5_000,
			100,
		);
		check(Date.now() - t0 <= 2_000, `${Date.now() - t0}ms 内断开`);
		await page.goto(portalURL(WS_PORT));
		await page.waitForURL(/\/login/, { timeout: 15_000 });
		check(true, `回到登录页 ${page.url()}`);
	} finally {
		await page.close();
	}
}

async function P16() {
	const orb = orbOf(P16_THREAD);
	const page = wire(await ctxA.newPage(), "A16");
	try {
		await page.goto(`${BASE}/t/${P16_THREAD}`);
		await page.locator("[data-panel-tab=files]").waitFor({ timeout: 30_000 });
		check(
			(await page.locator("[data-panel-tab=portal]").count()) === 0,
			"旧 orb 没有 Portal 标签",
		);
		const code = P16_THREAD.slice(-12);
		const r = curl(`t-${code}-p8000.${BASE_HOST}`, "/", [
			"-H",
			`Authorization: Bearer ${TOKEN}`,
			"-H",
			"Accept: text/html",
		]);
		note(`旧 orb ${orb} portal → ${r.status}`);
		check(
			r.status === 409 || r.status === 404,
			"409 说明页（或未声明端口 404）",
		);
		await page.locator("[data-panel-tab=files]").click();
		await page
			.locator("[data-testid=file-tree]")
			.waitFor({ timeout: 60_000 })
			.catch(() => {});
		await shot(page, "P16-old");
	} finally {
		await page.close();
	}
}

// ---------------------------------------------------------------- 主流程

const TESTS: [string, () => Promise<void>][] = [
	["P1", P1],
	["P2", P2],
	["P3", P3],
	["P4", P4],
	["P5", P5],
	["P6", P6],
	["P7", P7],
	["P8", P8],
	["P9", P9],
	["P10", P10],
	["P11", P11],
	["P12", P12],
	["P13", P13],
	["P15", P15],
	["P14", P14], // 登出会废掉 B 的会话，放在最后
	["P16", P16],
];

THREAD = await ensureThread();
CODE = THREAD.slice(-12);
await waitFor(
	"线程有 orb 且 orb ready",
	() => {
		const r = sql<{ orb_id: string | null }>(
			`select orb_id from threads where id='${THREAD}'`,
		)[0];
		if (!r?.orb_id) return undefined;
		ORB = r.orb_id;
		return orbRow(ORB)?.state === "ready" ? ORB : undefined;
	},
	600_000,
	2000,
);
SESSION =
	process.env.M4_SESSION ??
	sh("./bin/orynd --config dev/orynd.yaml admin create-web-session").out.trim();
if (!SESSION) throw new Error("没有网页会话");

// 前置：动态服务端口（--only 时也需要）
DEMO_PORT = svc("demo")?.port ?? 0;
HDR_PORT = svc("hdr")?.port ?? 0;
WS_PORT = svc("ws")?.port ?? 0;

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
await ctxA.addInitScript(() => {
	localStorage.setItem("oryn.debug", "1");
	localStorage.setItem("oryn.panel.width", "760");
});
ctxB = await browser.newContext({ locale: "zh-CN" });
await ctxB.addCookies([
	{
		name: "oryn_session",
		value: SESSION,
		domain: "localhost",
		path: "/",
		secure: true,
		httpOnly: true,
		sameSite: "Lax",
	},
]);
ctxC = await browser.newContext({ locale: "zh-CN" });
console.log(
	`M4 验收 run=${RUN} thread=${THREAD} orb=${ORB} code=${CODE} shots=${SHOTS}`,
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

await browser.close();
await Bun.write(
	`${SHOTS}/results-${RUN}.json`,
	JSON.stringify(results, null, 2),
);
console.log("\n汇总");
for (const r of results) console.log(`${r.id} ${r.verdict}`);
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
