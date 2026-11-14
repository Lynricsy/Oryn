#!/usr/bin/env bun
/**
 * M7 真机验收（计划「验证」A1–A20，按主人决定调整：不启用 App Links、minSdk 33、带扫码与 FCM）：
 * 对现有 dev 部署（`make dev` 的 orynd + provider，`dev/orynd.yaml`，public_url http://localhost:7600）在 Android 模拟器上
 * 逐项执行，打印 `A<n> PASS|FAIL|SKIP(...)` 与证据。SKIP 不算通过。
 *
 * 用法（web/ 目录，先 `source ../.dev/m7-env.sh`）：bun run accept:m7 [-- --only A2,A6] [--avd oryn-m7-fold] [--window]
 *
 * 设备选择：只按 AVD 名（默认 oryn-m7）挑模拟器——逐个 `adb -s emulator-N emu avd name` 比对；没有在跑的就以一个空闲的
 * 偶数端口启动它。其它设备（本机另有一个无关的 emulator-5554）不安装、不改动、不关闭；所有 adb 命令都带 `-s <serial>`。
 *
 * 观察面：
 *   事件  = App 的事件日志 `cache/events.jsonl`（debug 包，`adb exec-out run-as com.lynricsy.oryn cat …`；Rust 与 Kotlin 同写一个文件）；
 *   DOM   = 经 CDP 在 WebView 里求值（playwright-core 的 `_android` → `device.webView({pkg})`，仅 debug 包）；
 *   界面  = `uiautomator dump` + `input tap`（不装 playwright 的驱动 APK）；
 *   服务端 = REST（设备令牌 `.dev/credentials.json`；批准配对 / 吊销用 `orynd admin create-web-session` 现签的网页会话）与 SQLite。
 *
 * 前置（见 docs/mobile.md「本机验收」）：
 *   - 步骤 0 的环境（.dev/m7-env.sh、AVD oryn-m7 / oryn-m7-fold）；dev 部署在跑（make dev、provider、至少一个可用模型）；
 *   - debug APK（foss）：`make android DIST=foss` 或 `bun run tauri android build --debug --apk --target x86_64`，
 *     默认路径见 M7_APK；
 *   - A6–A8 / A18：`docker pull binwiederhier/ntfy:v2.37.2`，ntfy Android APK 放在 .dev/m7/（文件名含 fdroid，或用 M7_NTFY_APK 指定）；
 *   - A19：发布密钥 .dev/android-release.jks 与口令 M7_KEYSTORE_PASSWORD（别名 M7_KEY_ALIAS，默认 oryn），docs/mobile.md 里已记录证书指纹；
 *   - A23（FCM）：gms debug APK（M7_GMS_APK，默认 .dev/m7/oryn-gms-debug.apk）；dev/orynd.yaml 的
 *     `push.fcm.service_account_secret: fcm/service-account` 且已 `./bin/oryn secrets set fcm/service-account < .dev/m7/fcm-service-account.json`；
 *     模拟器镜像带 Google Play 服务且能连外网。
 *
 * 环境变量：ORYN_URL（默认 http://localhost:7600）、ORYN_ROOT、ORYN_TOKEN、M7_APK、M7_GMS_APK、M7_NTFY_APK、M7_KEYSTORE_PASSWORD、
 *   M7_KEY_ALIAS、M7_OUT（结果目录，默认 /tmp/oryn-m7-accept/<run>）、M7_THREAD（复用的线程；缺省用 .dev/m7.thread，没有则新建）。
 *
 * 场景之间的依赖：A3–A18、A22 需要已配对（`--only` 单跑时自动先配对，不做 A2 的断言）；A6–A8 共用 A6 启用的 UnifiedPush；
 * A18 吊销设备（之后的场景按需重新配对）；A23 临时停用 ntfy 并换装 gms 包，结束时装回 foss debug 包；A19 卸载 debug 包装 release 包，
 * 结束时装回 debug 包。A21（CI）不在本脚本内：见 docs/mobile.md「发布」。
 */
import {
	existsSync,
	mkdirSync,
	readdirSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import {
	type AndroidDevice,
	_android as android,
	type Browser,
	chromium,
	type Page,
} from "playwright-core";

const ROOT = process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`;
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const ORIGIN = new URL(BASE).origin;
const PORT = new URL(BASE).port || "80";
const CREDS = `${ROOT}/.dev/credentials.json`;
const TOKEN =
	process.env.ORYN_TOKEN ??
	(JSON.parse(readFileSync(CREDS, "utf8")) as { token: string }).token;
const DB = `${ROOT}/.dev/data/oryn.db`;
const ORYN = `${ROOT}/bin/oryn`;
const ORYND = `${ROOT}/bin/orynd`;
const PKG = "com.lynricsy.oryn";
const ACTIVITY = `${PKG}/.MainActivity`;
const NTFY_PKG = "io.heckel.ntfy";
const NTFY_PORT = 7680;
const NTFY_IMAGE = "binwiederhier/ntfy:v2.37.2";
const NTFY_CONTAINER = "oryn-m7-ntfy";
const APK =
	process.env.M7_APK ??
	`${ROOT}/web/src-tauri/gen/android/app/build/outputs/apk/universal/debug/app-universal-debug.apk`;
const GMS_APK = process.env.M7_GMS_APK ?? `${ROOT}/.dev/m7/oryn-gms-debug.apk`;
const RELEASE_APK = `${ROOT}/web/src-tauri/gen/android/app/build/outputs/apk/universal/release/app-universal-release.apk`;
const KEYSTORE = `${ROOT}/.dev/android-release.jks`;
const RUN = Math.random().toString(36).slice(2, 6);
const OUT = process.env.M7_OUT ?? `/tmp/oryn-m7-accept/${RUN}`;
const SDK = process.env.ANDROID_HOME ?? "/opt/android-sdk";
const ADB = existsSync(`${SDK}/platform-tools/adb`)
	? `${SDK}/platform-tools/adb`
	: "adb";
const EMULATOR = existsSync(`${SDK}/emulator/emulator`)
	? `${SDK}/emulator/emulator`
	: "emulator";
mkdirSync(OUT, { recursive: true });

const argv = process.argv.slice(2);
const argOf = (name: string) => {
	const i = argv.indexOf(name);
	return i >= 0 ? argv[i + 1] : undefined;
};
const ONLY = argOf("--only") ? new Set(argOf("--only")?.split(",")) : null;
const AVD = argOf("--avd") ?? "oryn-m7";
const WINDOW = argv.includes("--window");

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const te = new TextEncoder();

function sh(
	cmd: string,
	opts: { stdin?: string | Uint8Array; cwd?: string; timeoutMs?: number } = {},
): { code: number; out: string; stdout: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], {
		cwd: opts.cwd ?? ROOT,
		stdin:
			opts.stdin === undefined
				? undefined
				: typeof opts.stdin === "string"
					? te.encode(opts.stdin)
					: opts.stdin,
		timeout: opts.timeoutMs ?? 120_000,
	});
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
		stdout: r.stdout.toString().trim(),
	};
}

function must(cmd: string, opts: Parameters<typeof sh>[1] = {}) {
	const r = sh(cmd, opts);
	if (r.code !== 0) throw new Error(`命令失败（${r.code}）：${cmd}\n${r.out}`);
	return r;
}

let SERIAL = "";
/** `adb -s <serial> <args>`（args 由调用方负责引号）。 */
const adb = (args: string, timeoutMs?: number) =>
	sh(`${q(ADB)} -s ${SERIAL} ${args}`, { timeoutMs });
/** 设备上的 shell 命令（整条命令作为一个参数传给 adb shell）。 */
const ash = (cmd: string, timeoutMs?: number) =>
	adb(`shell ${q(cmd)}`, timeoutMs);
/** 以 App 身份在其数据目录里跑命令（debug 包）。 */
const runAs = (cmd: string) => adb(`exec-out run-as ${PKG} sh -c ${q(q(cmd))}`);

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

function sql<T = Record<string, unknown>>(qs: string): T[] {
	const r = Bun.spawnSync(["sqlite3", "-json", DB, qs]);
	const s = r.stdout.toString().trim();
	return s ? (JSON.parse(s) as T[]) : [];
}

interface ApiRes {
	status: number;
	json: unknown;
	text: string;
}

async function req(
	method: string,
	path: string,
	headers: Record<string, string>,
	body?: unknown,
): Promise<ApiRes> {
	const raw = body instanceof Uint8Array;
	const res = await fetch(`${BASE}${path}`, {
		method,
		headers: {
			"X-Oryn-CSRF": "1",
			...(body !== undefined
				? {
						"Content-Type": raw
							? "application/octet-stream"
							: "application/json",
					}
				: {}),
			...headers,
		},
		body: body === undefined ? undefined : raw ? body : JSON.stringify(body),
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

/** 所有者接口（CLI 的设备令牌）。 */
const api = (method: string, path: string, body?: unknown) =>
	req(method, path, { Authorization: `Bearer ${TOKEN}` }, body);

let WEB = "";
/** 网页会话接口（批准配对、吊销设备）：`orynd admin create-web-session` 现签。 */
function webApi(method: string, path: string, body?: unknown) {
	if (!WEB)
		WEB = must(
			`${q(ORYND)} --config dev/orynd.yaml admin create-web-session --user-agent m7-accept`,
		).stdout;
	return req(method, path, { Cookie: `__Host-oryn_session=${WEB}` }, body);
}

async function cli(args: string) {
	const p = Bun.spawn(
		["bash", "-c", `ORYN_CREDENTIALS=${q(CREDS)} ${q(ORYN)} ${args}`],
		{ cwd: ROOT, stdout: "pipe", stderr: "pipe" },
	);
	const [stdout, stderr, code] = await Promise.all([
		new Response(p.stdout).text(),
		new Response(p.stderr).text(),
		p.exited,
	]);
	return { code, out: `${stdout}${stderr}`.trim(), stdout: stdout.trim() };
}

// ---------------------------------------------------------------- 结果

type Verdict = "PASS" | "FAIL" | "SKIP";
const results: {
	id: string;
	verdict: Verdict;
	reason?: string;
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
class Skip extends Error {}

// ---------------------------------------------------------------- 设备

/** 正在运行的模拟器：serial → AVD 名。 */
function runningAvds(): Map<string, string> {
	const out = new Map<string, string>();
	for (const line of sh(`${q(ADB)} devices`).stdout.split("\n")) {
		const m = line.match(/^(emulator-\d+)\s+device$/);
		if (!m?.[1]) continue;
		const name = sh(`${q(ADB)} -s ${m[1]} emu avd name`)
			.stdout.split("\n")[0]
			?.trim();
		if (name) out.set(m[1], name);
	}
	return out;
}

async function bootDevice(): Promise<string> {
	for (const [serial, name] of runningAvds()) if (name === AVD) return serial;
	const busy = new Set(
		[...sh(`${q(ADB)} devices`).stdout.matchAll(/emulator-(\d+)/g)].map((m) =>
			Number(m[1]),
		),
	);
	let port = 5560;
	while (busy.has(port) || busy.has(port + 1)) port += 2;
	const serial = `emulator-${port}`;
	const log = `${OUT}/emulator.log`;
	Bun.spawn(
		[
			"bash",
			"-c",
			`exec ${q(EMULATOR)} -avd ${q(AVD)} -port ${port} -no-snapshot-save -no-audio -no-boot-anim -gpu swiftshader_indirect ${WINDOW ? "" : "-no-window"} > ${q(log)} 2>&1`,
		],
		{ cwd: ROOT },
	).unref();
	console.log(`  启动模拟器 ${AVD} → ${serial}（日志 ${log}）`);
	await waitFor(
		`${serial} 启动完成`,
		() =>
			sh(`${q(ADB)} -s ${serial} shell getprop sys.boot_completed`).stdout ===
			"1",
		300_000,
		2000,
	);
	return serial;
}

let DEVICE: AndroidDevice;
let PAGE: Page | null = null;
let PAGE_PID = "";

const pidof = () => ash(`pidof ${PKG}`).stdout.trim();

/** WebView 的页面；App 进程换了就重新取。 */
async function page(timeoutMs = 30_000): Promise<Page> {
	const pid = await waitFor("App 进程", () => pidof() || undefined, timeoutMs);
	if (PAGE && PAGE_PID === pid && !PAGE.isClosed()) return PAGE;
	const wv = await DEVICE.webView({ pkg: PKG }, { timeout: timeoutMs });
	PAGE = await wv.page();
	PAGE_PID = pid;
	return PAGE;
}

async function domPath(): Promise<string> {
	return (await page()).evaluate(() => location.pathname);
}

async function goto(path: string) {
	const p = await page();
	await p.evaluate((u) => {
		history.pushState(null, "", u);
		dispatchEvent(new PopStateEvent("popstate"));
	}, path);
	await waitFor(
		`DOM 路径 ${path}`,
		async () => (await domPath()) === path,
		10_000,
	);
}

function foreground(): string {
	const out = ash("dumpsys activity activities").stdout;
	const m =
		out.match(/topResumedActivity=ActivityRecord\{[^ ]+ [^ ]+ ([^ }]+)/) ??
		out.match(/mResumedActivity: ActivityRecord\{[^ ]+ [^ ]+ ([^ }]+)/);
	return m?.[1] ?? "";
}
const ours = () => foreground().startsWith(`${PKG}/`);

function launch() {
	must(`${q(ADB)} -s ${SERIAL} shell am start -W -n ${ACTIVITY}`);
}

function grantAll() {
	for (const p of ["POST_NOTIFICATIONS", "CAMERA"])
		ash(`pm grant ${PKG} android.permission.${p}`);
}

/** `pm clear` 后重新授权并启动，等到连接页就绪。 */
async function freshStart() {
	ash(`pm clear ${PKG}`);
	grantAll();
	launch();
	await ev("launcher.ready", { timeoutMs: 30_000 });
}

// ---- 界面层

interface UiNode {
	text: string;
	desc: string;
	res: string;
	cls: string;
	pkg: string;
	bounds: [number, number, number, number];
}

function uiDump(): UiNode[] {
	const xml = adb("exec-out uiautomator dump /dev/tty", 30_000).stdout;
	const out: UiNode[] = [];
	for (const m of xml.matchAll(/<node [^>]*>/g)) {
		const a = (k: string) =>
			m[0]
				.match(new RegExp(`${k}="([^"]*)"`))?.[1]
				?.replaceAll("&amp;", "&")
				.replaceAll("&quot;", '"')
				.replaceAll("&lt;", "<")
				.replaceAll("&gt;", ">") ?? "";
		const b = a("bounds").match(/\[(\d+),(\d+)\]\[(\d+),(\d+)\]/);
		if (!b) continue;
		out.push({
			text: a("text"),
			desc: a("content-desc"),
			res: a("resource-id"),
			cls: a("class"),
			pkg: a("package"),
			bounds: [Number(b[1]), Number(b[2]), Number(b[3]), Number(b[4])],
		});
	}
	return out;
}

const tapAt = (x: number, y: number) =>
	ash(`input tap ${Math.round(x)} ${Math.round(y)}`);

async function uiTap(
	what: string,
	pred: (n: UiNode) => boolean,
	timeoutMs = 15_000,
): Promise<UiNode> {
	const n = await waitFor(
		`界面元素 ${what}`,
		() => uiDump().find(pred),
		timeoutMs,
		1000,
	);
	const [l, t, r, b] = n.bounds;
	tapAt((l + r) / 2, (t + b) / 2);
	note(`点击界面元素 ${what}`);
	return n;
}

function webViewBounds(): [number, number, number, number] {
	const n = uiDump().find(
		(x) => x.cls === "android.webkit.WebView" && x.pkg === PKG,
	);
	if (!n) throw new Error("界面里找不到本包的 WebView");
	return n.bounds;
}

/** 以真实触摸点 DOM 元素（打开系统文件选择器 / 相机 / 软键盘需要用户手势）。 */
async function touch(selector: string) {
	const p = await page();
	const r = await p.evaluate((s) => {
		const el = document.querySelector(s);
		if (!el) return null;
		el.scrollIntoView({ block: "center" });
		const b = el.getBoundingClientRect();
		return {
			x: b.left + b.width / 2,
			y: b.top + b.height / 2,
			dpr: devicePixelRatio,
		};
	}, selector);
	if (!r) throw new Error(`DOM 里没有 ${selector}`);
	const [l, t] = webViewBounds();
	tapAt(l + r.x * r.dpr, t + r.y * r.dpr);
	note(`触摸 ${selector}`);
}

// ---- 事件日志

interface Ev {
	ts: number;
	event: string;
	i: number;
	[k: string]: unknown;
}

function events(): Ev[] {
	const r = runAs("cat cache/events.jsonl 2>/dev/null");
	const out: Ev[] = [];
	for (const line of r.stdout.split("\n")) {
		if (!line.trim()) continue;
		try {
			out.push({ ...(JSON.parse(line) as Ev), i: out.length });
		} catch {
			// 写了一半的行
		}
	}
	return out;
}

const mark = () => events().length;
const since = (after: number, name?: string) =>
	events().filter((e) => e.i >= after && (!name || e.event === name));
const show = (e: Ev) => {
	const { i: _i, ts: _ts, ...rest } = e;
	return JSON.stringify(rest);
};

async function ev(
	name: string,
	opts: { after?: number; where?: (e: Ev) => boolean; timeoutMs?: number } = {},
): Promise<Ev> {
	const e = await waitFor(
		`事件 ${name}`,
		() =>
			since(opts.after ?? 0, name).find((x) => !opts.where || opts.where(x)),
		opts.timeoutMs ?? 15_000,
		500,
	);
	note(`事件 ${show(e)}`);
	return e;
}

// ---- 通知

interface Notif {
	key: string;
	title: string;
}

function notifications(): Notif[] {
	const out = ash("dumpsys notification --noredact").stdout;
	const res: Notif[] = [];
	for (const chunk of out.split("NotificationRecord(").slice(1)) {
		if (!chunk.includes(`pkg=${PKG}`)) continue;
		const key = chunk.match(/key=([^:\s]+:[^\s]+)/)?.[1] ?? chunk.slice(0, 40);
		const title = chunk.match(/android\.title=String \((.*)\)/)?.[1] ?? "";
		if (!res.some((n) => n.key === key)) res.push({ key, title });
	}
	return res;
}

// ---------------------------------------------------------------- 线程

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
const seqOf = (tid: string) => threadRow(tid)?.last_seq ?? 0;

const SETTLED: Record<string, true> = {
	idle: true,
	awaiting_input: true,
	error: true,
};

async function waitTurn(tid: string, beforeSeq: number, timeoutMs = 300_000) {
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
		1000,
	);
}

/** `oryn thread send`（不 focus），返回发送前的 last_seq。 */
async function send(tid: string, msg: string): Promise<number> {
	const before = seqOf(tid);
	const r = await cli(`thread send ${tid} ${q(msg)}`);
	if (r.code !== 0) throw new Error(`thread send 失败：${r.out}`);
	return before;
}

async function newThread(prompt: string, title: string): Promise<string> {
	const r = await api("POST", "/api/v1/threads", { prompt, title });
	if (r.status !== 201) throw new Error(`建线程失败 ${r.status} ${r.text}`);
	const j = r.json as { thread?: { id: string }; id?: string };
	const id = (j.thread?.id ?? j.id) as string;
	await waitTurn(id, 0);
	return id;
}

let THREAD = "";
let THREAD_TITLE = "";
async function ensureThread(): Promise<string> {
	if (THREAD) return THREAD;
	const f = `${ROOT}/.dev/m7.thread`;
	let id = process.env.M7_THREAD ?? "";
	if (!id && existsSync(f)) id = readFileSync(f, "utf8").trim();
	if (!id || !threadRow(id)?.orb_id) {
		id = await newThread("只回复 ok", "m7 验收");
		writeFileSync(f, id);
	}
	THREAD = id;
	THREAD_TITLE = `m7 推送 ${RUN}`;
	await api("PATCH", `/api/v1/threads/${id}`, { title: THREAD_TITLE });
	note(`线程 ${id}（标题「${THREAD_TITLE}」）`);
	return id;
}

// ---------------------------------------------------------------- 配对

let DEVICE_ID = "";

/** 从连接页填地址、连接、批准，等到 web.ready；返回设备 id 与 pair.started 事件。 */
async function pairFromLauncher(): Promise<{ deviceId: string; started: Ev }> {
	const after = mark();
	const p = await page();
	await p.waitForSelector("[data-testid=launcher-url]", { timeout: 30_000 });
	await p.fill("[data-testid=launcher-url]", BASE);
	await p.click("[data-testid=launcher-connect]");
	const started = await ev("pair.started", { after, timeoutMs: 30_000 });
	await approve(String(started.code));
	const approved = await ev("pair.approved", { after, timeoutMs: 30_000 });
	await ev("web.ready", { after, timeoutMs: 60_000 });
	return { deviceId: String(approved.deviceId), started };
}

async function approve(code: string) {
	const r = await webApi("POST", "/api/v1/devices/pair/approve", { code });
	if (r.status >= 300) throw new Error(`批准配对失败 ${r.status} ${r.text}`);
	note(`已批准配对 ${code}`);
}

const paired = () =>
	runAs("cat shared_prefs/oryn_vault.xml 2>/dev/null").stdout.includes(
		`token:${ORIGIN}`,
	);

/** 保证 App 已配对并在远程页面上（`--only` 单跑时用）。 */
async function ensurePaired() {
	if (DEVICE_ID && paired() && pidof()) {
		if (!ours()) launch();
		return;
	}
	if (!pidof()) launch();
	if (paired()) {
		const after = mark();
		ash(`am force-stop ${PKG}`);
		launch();
		await ev("web.ready", { after, timeoutMs: 60_000 });
		DEVICE_ID = latestMobileDevice() ?? "";
		if (DEVICE_ID) return;
	}
	await freshStart();
	DEVICE_ID = (await pairFromLauncher()).deviceId;
}

function latestMobileDevice(): string | undefined {
	return sql<{ id: string }>(
		"select id from devices where kind='mobile' and revoked_at is null order by created_at desc limit 1",
	)[0]?.id;
}

// ---------------------------------------------------------------- ntfy（UnifiedPush 分发器）

let ntfyReady = false;
function ntfyApk(): string {
	if (process.env.M7_NTFY_APK) return process.env.M7_NTFY_APK;
	const dir = `${ROOT}/.dev/m7`;
	const hit = existsSync(dir)
		? readdirSync(dir).find((f) => f.endsWith(".apk") && f.includes("fdroid"))
		: undefined;
	if (!hit)
		throw new Error(
			"找不到 ntfy APK：放到 .dev/m7/（文件名含 fdroid）或设 M7_NTFY_APK",
		);
	return `${dir}/${hit}`;
}

async function ensureNtfy() {
	if (ntfyReady) return;
	if (
		sh(`docker inspect -f '{{.State.Running}}' ${NTFY_CONTAINER}`).stdout !==
		"true"
	)
		must(
			`docker run -d --rm --name ${NTFY_CONTAINER} -p 127.0.0.1:${NTFY_PORT}:80 ${NTFY_IMAGE} serve --base-url http://localhost:${NTFY_PORT}`,
		);
	adb(`reverse tcp:${NTFY_PORT} tcp:${NTFY_PORT}`);
	if (!ash(`pm path ${NTFY_PKG}`).stdout.includes("package:"))
		must(`${q(ADB)} -s ${SERIAL} install -r -g ${q(ntfyApk())}`);
	ash(`pm enable ${NTFY_PKG}`);
	ash(`monkey -p ${NTFY_PKG} 1`);
	await sleep(3000);
	ash(`am force-stop ${NTFY_PKG}`);
	// 默认服务器改成本机 ntfy（需要 adb root；属主与权限照同目录其它文件）
	adb("root", 30_000);
	await waitFor(
		"adb root 后重连",
		() => ash("id -u").stdout === "0",
		30_000,
		1000,
	);
	const dir = `/data/data/${NTFY_PKG}/shared_prefs`;
	const file = `${dir}/MainPreferences.xml`;
	const xml = `<?xml version='1.0' encoding='utf-8' standalone='yes' ?>\n<map>\n    <string name="DefaultBaseURL">http://localhost:${NTFY_PORT}</string>\n</map>\n`;
	ash(`mkdir -p ${dir}`);
	must(`${q(ADB)} -s ${SERIAL} shell 'cat > ${file}'`, { stdin: xml });
	const owner = ash(`stat -c %U:%G /data/data/${NTFY_PKG}`).stdout;
	ash(`chown -R ${owner} ${dir} && chmod 660 ${file} && restorecon -R ${dir}`);
	ash(`dumpsys deviceidle whitelist +${NTFY_PKG}`);
	ash(`monkey -p ${NTFY_PKG} 1`);
	await sleep(3000);
	ash("input keyevent 3");
	ntfyReady = true;
	note(`ntfy 已就绪（默认服务器 http://localhost:${NTFY_PORT}）`);
}

// ---------------------------------------------------------------- 杂项

/** 一张 1×1 PNG。 */
const PNG = Uint8Array.from(
	Buffer.from(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
		"base64",
	),
);

function webviewVersion(): number {
	const out = ash("dumpsys webviewupdate").stdout;
	const v = out.match(
		/Current WebView package \(name, version\): \([^,]+, ([\d.]+)\)/,
	)?.[1];
	return Number(v?.split(".")[0] ?? 0);
}

function aapt2(): string {
	const dir = `${SDK}/build-tools`;
	const v = readdirSync(dir)
		.sort((a, b) => a.localeCompare(b, undefined, { numeric: true }))
		.pop();
	return `${dir}/${v}`;
}

function installDebug(apk = APK) {
	if (!existsSync(apk)) throw new Error(`APK 不存在：${apk}`);
	must(`${q(ADB)} -s ${SERIAL} install -r -g ${q(apk)}`, {
		timeoutMs: 300_000,
	});
}

// ---------------------------------------------------------------- 场景

const TESTS: [string, () => Promise<void>][] = [];
const scenario = (id: string, fn: () => Promise<void>) => TESTS.push([id, fn]);

scenario("A1", async () => {
	const bt = aapt2();
	const badging = must(`${q(`${bt}/aapt2`)} dump badging ${q(APK)}`).stdout;
	const perms = must(`${q(`${bt}/aapt2`)} dump permissions ${q(APK)}`).stdout;
	check(badging.includes(`package: name='${PKG}'`), `包名 ${PKG}`);
	check(badging.includes("sdkVersion:'33'"), "sdkVersion:'33'");
	check(badging.includes("targetSdkVersion:'37'"), "targetSdkVersion:'37'");
	const has = (p: string) => perms.includes(`name='${p}'`);
	for (const p of [
		"android.permission.INTERNET",
		"android.permission.POST_NOTIFICATIONS",
		"android.permission.CAMERA",
	])
		check(has(p), `声明 ${p}`);
	for (const p of [
		"android.permission.RECEIVE_BOOT_COMPLETED",
		"android.permission.WAKE_LOCK",
		"android.permission.READ_EXTERNAL_STORAGE",
		"android.permission.WRITE_EXTERNAL_STORAGE",
		"android.permission.READ_MEDIA_IMAGES",
		"android.permission.MANAGE_EXTERNAL_STORAGE",
		"com.google.android.c2dm.permission.RECEIVE",
	])
		check(!has(p), `foss 包未声明 ${p}`);
	await freshStart();
	await ev("app.start");
	check(
		!sh(`${q(ADB)} -s ${SERIAL} logcat -d -b crash`).out.includes(PKG),
		"崩溃缓冲区里没有本包",
	);
});

scenario("A2", async () => {
	await freshStart();
	const after = mark();
	const p = await page();
	await p.waitForSelector("[data-testid=launcher-url]", { timeout: 30_000 });
	await p.fill("[data-testid=launcher-url]", BASE);
	await p.click("[data-testid=launcher-connect]");
	const started = await ev("pair.started", { after, timeoutMs: 30_000 });
	check(
		started.verifyUrl === `${ORIGIN}/pair`,
		`verifyUrl = ${String(started.verifyUrl)}`,
	);
	await sleep(3000);
	check(since(after, "browser.open").length === 0, "点按钮前没有 browser.open");
	check(ours(), `前台仍是本包（${foreground()}）`);
	const code = await p.textContent("[data-testid=launcher-code]");
	check(
		(code ?? "").replace(/\s/g, "").includes(String(started.code)),
		`界面显示配对码 ${String(started.code)}`,
	);
	await p.click("[data-testid=launcher-open-browser]");
	await ev("browser.open", { after });
	await waitFor("浏览器到前台", () => !ours() && foreground(), 15_000);
	note(`前台 ${foreground()}`);
	launch();
	await approve(String(started.code));
	const approved = await ev("pair.approved", { after, timeoutMs: 30_000 });
	const stored = await ev("token.stored", { after });
	check(
		stored.store === "keystore",
		`token.stored.store = ${String(stored.store)}`,
	);
	await ev("session.minted", { after, timeoutMs: 30_000 });
	const ready = await ev("web.ready", { after, timeoutMs: 60_000 });
	check(
		ready.origin === ORIGIN && ready.bridge === 2,
		`web.ready ${show(ready)}`,
	);
	DEVICE_ID = String(approved.deviceId);
	const devs = (await api("GET", "/api/v1/devices")).json as {
		devices: { id: string; kind: string; platform: string }[];
	};
	const d = devs.devices.find((x) => x.id === DEVICE_ID);
	check(d?.kind === "mobile", `设备 kind = ${d?.kind}`);
	check(!!d?.platform.startsWith("android-"), `设备 platform = ${d?.platform}`);
	const n = sql<{ n: number }>(
		`select count(*) n from web_sessions where device_id='${DEVICE_ID}'`,
	)[0]?.n;
	check(n === 1, `web_sessions where device_id = ${n} 行`);
});

scenario("A3", async () => {
	await ensurePaired();
	const vault = runAs("cat shared_prefs/oryn_vault.xml").stdout;
	const m = vault.match(
		new RegExp(`name="token:${ORIGIN.replace(/[.:/]/g, "\\$&")}">([^<]+)<`),
	);
	check(!!m, `oryn_vault.xml 有 token:${ORIGIN}`);
	check(/^[A-Za-z0-9+/=_-]{24,}$/.test(m?.[1] ?? ""), "值是 base64 密文");
	check(
		!runAs("grep -rl odt_ . 2>/dev/null").stdout,
		"数据目录（含事件日志）里搜不到 odt_",
	);
	const cfgPath = runAs("find . -name config.json").stdout.split("\n")[0] ?? "";
	check(!!cfgPath, `壳配置 ${cfgPath}`);
	const cfg = JSON.parse(runAs(`cat ${cfgPath}`).stdout) as {
		tokenStore?: string;
	};
	check(cfg.tokenStore === "keystore", `config.tokenStore = ${cfg.tokenStore}`);
	check(
		!runAs("find . -name credentials.json").stdout,
		"没有 credentials.json",
	);
});

scenario("A4", async () => {
	await ensurePaired();
	ash(`am force-stop ${PKG}`);
	const after = mark();
	launch();
	await ev("session.minted", { after, timeoutMs: 30_000 });
	await ev("web.ready", { after, timeoutMs: 60_000 });
	check(since(after, "pair.started").length === 0, "冷启动不经配对");
	const before = mark();
	ash("input keyevent 4");
	await waitFor("返回键后离开本包", () => !ours(), 10_000);
	await sleep(2000);
	check(since(before, "launcher.ready").length === 0, "返回键没有退回连接页");
});

scenario("A5", async () => {
	await ensurePaired();
	const T = await ensureThread();
	await api("PATCH", `/api/v1/threads/${T}`, { unread: true });
	launch();
	let after = mark();
	must(
		`${q(ADB)} -s ${SERIAL} shell am start -a android.intent.action.VIEW -d ${q(`oryn://thread/${T}`)} ${PKG}`,
	);
	await ev("deeplink", { after, where: (e) => e.path === `/t/${T}` });
	await ev("open", { after, where: (e) => e.delivered === true });
	await waitFor(
		"热启动后 DOM 路径",
		async () => (await domPath()) === `/t/${T}`,
		10_000,
	);
	await waitFor(
		"unread == false",
		async () =>
			((await api("GET", `/api/v1/threads/${T}`)).json as { unread?: boolean })
				.unread === false,
		5000,
	);
	note("热：路径与已读正确");

	ash(`am force-stop ${PKG}`);
	after = mark();
	must(
		`${q(ADB)} -s ${SERIAL} shell am start -a android.intent.action.VIEW -d ${q(`oryn://thread/${T}`)} ${PKG}`,
	);
	await ev("open", {
		after,
		where: (e) => e.delivered === false,
		timeoutMs: 30_000,
	});
	await ev("web.ready", { after, timeoutMs: 60_000 });
	await waitFor(
		"冷启动后 DOM 路径",
		async () => (await domPath()) === `/t/${T}`,
		15_000,
	);

	after = mark();
	must(
		`${q(ADB)} -s ${SERIAL} shell am start -a android.intent.action.VIEW -d ${q("oryn://thread/../../settings")} ${PKG}`,
	);
	await ev("deeplink", { after, where: (e) => e.path === null });
	await sleep(2000);
	check(since(after, "open").length === 0, "非法深链接没有 open");
});

let PUSH_SUB = "";
scenario("A6", async () => {
	await ensureNtfy();
	await ensurePaired();
	const T = await ensureThread();
	launch();
	await goto("/settings/notifications");
	const p = await page();
	await p.waitForSelector("[data-testid=mobile-push-card]", {
		timeout: 15_000,
	});
	const after = mark();
	await p.click("[data-testid=mobile-push-enable]");
	await waitFor(
		"推送状态 active",
		() =>
			p
				.getAttribute("[data-testid=mobile-push-card]", "data-push-state")
				.then((s) => s === "active"),
		60_000,
		1000,
	);
	check(
		(await p.getAttribute(
			"[data-testid=mobile-push-card]",
			"data-push-via",
		)) === "unifiedpush",
		"通道 unifiedpush",
	);
	const synced = await ev("push.synced", {
		after,
		where: (e) => e.kind === "webpush",
	});
	PUSH_SUB = String(synced.id);
	const subs = (await api("GET", "/api/v1/push/subscriptions")).json as {
		subscriptions: { id: string; kind: string; endpoint: string }[];
	};
	const s = subs.subscriptions.find((x) => x.id === PUSH_SUB);
	check(s?.kind === "webpush", `订阅 kind = ${s?.kind}`);
	check(
		!!s?.endpoint.startsWith(`http://localhost:${NTFY_PORT}/up`),
		`端点 ${s?.endpoint}`,
	);

	ash("input keyevent 3");
	await waitFor("回到桌面", () => !ours(), 10_000);
	const evAfter = mark();
	const before = await send(T, "数到 3");
	await waitTurn(T, before);
	const n = await waitFor(
		"系统通知",
		() => notifications().find((x) => x.title.includes(THREAD_TITLE)),
		15_000,
		1000,
	);
	note(`通知「${n.title}」`);
	await ev("push.received", {
		after: evAfter,
		where: (e) => e.via === "unifiedpush",
	});
	await ev("notify.posted", {
		after: evAfter,
		where: (e) => e.path === `/t/${T}`,
	});
});

scenario("A7", async () => {
	await ensureNtfy();
	await ensurePaired();
	const T = await ensureThread();
	launch(); // 回前台会清掉旧通知
	await sleep(2000);
	ash("input keyevent 3");
	await waitFor("回到桌面", () => !ours(), 10_000);
	ash(`am kill ${PKG}`);
	await waitFor("进程已不在", () => !pidof(), 10_000);
	note("pidof 为空");
	const after = mark();
	const before = await send(T, "数到 3");
	await waitTurn(T, before);
	await waitFor(
		"进程不在时的系统通知",
		() => notifications().find((x) => x.title.includes(THREAD_TITLE)),
		20_000,
		1000,
	);
	await ev("push.received", { after });
	ash("cmd statusbar expand-notifications");
	await sleep(1500);
	const clickAfter = mark();
	await uiTap(`通知「${THREAD_TITLE}」`, (x) => x.text.includes(THREAD_TITLE));
	await ev("app.start", { after: clickAfter, timeoutMs: 30_000 });
	await ev("deeplink", {
		after: clickAfter,
		where: (e) => e.path === `/t/${T}`,
		timeoutMs: 30_000,
	});
	await waitFor(
		"DOM 路径",
		async () => (await domPath()) === `/t/${T}`,
		30_000,
	);
	await waitFor(
		"unread == false",
		async () =>
			((await api("GET", `/api/v1/threads/${T}`)).json as { unread?: boolean })
				.unread === false,
		10_000,
	);
});

scenario("A8", async () => {
	await ensureNtfy();
	await ensurePaired();
	const T = await ensureThread();
	const U = await newThread("只回复 ok", `m7 另一线程 ${RUN}`);
	launch();
	await goto(`/t/${U}`);
	const p = await page();
	await p.evaluate((title) => {
		const w = window as unknown as { __m7toasts: string[] };
		w.__m7toasts = [];
		new MutationObserver(() => {
			for (const el of document.querySelectorAll("[role=status]"))
				if (el.textContent?.includes(title)) w.__m7toasts.push(el.textContent);
		}).observe(document.body, {
			subtree: true,
			childList: true,
			characterData: true,
		});
	}, THREAD_TITLE);
	const known = new Set(notifications().map((n) => n.key));
	const after = mark();
	const before = await send(T, "数到 3");
	await waitTurn(T, before);
	await ev("push.dropped", {
		after,
		where: (e) => e.reason === "foreground",
		timeoutMs: 20_000,
	});
	await sleep(15_000);
	const fresh = notifications().filter((n) => !known.has(n.key));
	check(fresh.length === 0, `15 秒内没有新的系统通知（${fresh.length}）`);
	const toasts = await p.evaluate(
		() => (window as unknown as { __m7toasts: string[] }).__m7toasts.length,
	);
	check(toasts > 0, `站内 toast 出现 ${toasts} 次`);
});

scenario("A9", async () => {
	await ensurePaired();
	const T = await ensureThread();
	launch();
	ash("wm size reset && wm density reset");
	await goto(`/t/${T}`);
	const p = await page();
	await waitFor(
		"data-bp=compact",
		() => p.evaluate(() => document.documentElement.dataset.bp === "compact"),
		10_000,
	);
	const tabs = await p.$$eval(
		"[data-testid=thread-tabs] [data-panel-tab]",
		(els) => els.map((e) => e.getAttribute("data-panel-tab")),
	);
	note(`标签 ${tabs.join(", ")}`);
	check(
		tabs.slice(0, 4).join(",") === "agent,changes,files,terminal" &&
			tabs.at(-1) === "info",
		"标签顺序 agent, changes, files, terminal, …, info",
	);
	check((await p.$$(".fixed.inset-0.z-40")).length === 0, "没有抽屉遮罩");
	const top0 = await p.evaluate(() => {
		const t = document.querySelector<HTMLElement>("[data-testid=transcript]");
		if (t) t.scrollTop = Math.max(0, t.scrollHeight / 2);
		return t?.scrollTop ?? -1;
	});
	await p.click("[data-testid=thread-tabs] [data-panel-tab=terminal]");
	await waitFor(
		"转录隐藏、终端可见",
		() =>
			p.evaluate(() => {
				const t = document.querySelector<HTMLElement>(
					"[data-testid=transcript]",
				);
				const term = document.querySelector<HTMLElement>(
					"[data-testid=terminal-panel]",
				);
				return (
					!!t && t.offsetParent === null && !!term && term.offsetParent !== null
				);
			}),
		10_000,
	);
	await p.click("[data-testid=thread-tabs] [data-panel-tab=agent]");
	const top1 =
		(await waitFor(
			"转录重新可见",
			() =>
				p.evaluate(() => {
					const t = document.querySelector<HTMLElement>(
						"[data-testid=transcript]",
					);
					return t && t.offsetParent !== null ? t.scrollTop + 1 : 0;
				}),
			10_000,
		)) - 1;
	check(Math.abs(top1 - top0) <= 2, `切回后 scrollTop ${top0} → ${top1}`);

	ash("wm size 1400x2000 && wm density 320");
	await waitFor(
		"data-bp=medium",
		() => p.evaluate(() => document.documentElement.dataset.bp === "medium"),
		15_000,
	);
	check(
		(await p.$$("[data-testid=thread-tabs] [data-panel-tab=agent]")).length ===
			1,
		"700dp：线程列仍是同级标签",
	);
	ash("wm size 2000x1200 && wm density 320");
	await waitFor(
		"data-bp=expanded",
		() => p.evaluate(() => document.documentElement.dataset.bp === "expanded"),
		15_000,
	);
	check(
		(await p.$$("[data-panel-tab=agent]")).length === 0,
		"1000dp：没有 agent 标签（三栏）",
	);
	ash("wm size reset && wm density reset");
});

scenario("A10", async () => {
	await ensurePaired();
	launch();
	const p = await page();
	const token = Math.random();
	await p.evaluate((v) => {
		(window as unknown as { __m7: number }).__m7 = v;
	}, token);
	const starts = () =>
		[since(0, "app.start").length, since(0, "web.ready").length].join("/");
	const base = starts();
	const verify = async (what: string) => {
		await sleep(2500);
		const v = await (await page()).evaluate(
			() => (window as unknown as { __m7?: number }).__m7,
		);
		check(v === token && pidof() === PAGE_PID, `${what}：window.__m7 仍在`);
		check(
			starts() === base,
			`${what}：app.start / web.ready 条数不变（${base}）`,
		);
	};
	ash(
		"settings put system accelerometer_rotation 0 && settings put system user_rotation 1",
	);
	await verify("横屏");
	ash("settings put system user_rotation 0");
	await verify("竖屏");
	ash("wm size 1400x2000");
	await verify("改尺寸 1");
	ash("wm size 1080x1800");
	await verify("改尺寸 2");
	ash("wm size reset");
	const states = ash("cmd device_state print-states").stdout;
	const ids = [...states.matchAll(/identifier=(\d+)/g)].map((m) => m[1]);
	if (ids.length > 1) {
		for (const id of ids) {
			ash(`cmd device_state state ${id}`);
			await verify(`device_state ${id}`);
		}
		ash("cmd device_state state reset");
	} else note("非折叠 AVD：跳过折叠 / 展开");
});

scenario("A11", async () => {
	const states = ash("cmd device_state print-states").stdout;
	const half = states.match(/identifier=(\d+), name='[^']*HALF[^']*'/)?.[1];
	const open = states.match(
		/identifier=(\d+), name='[^']*(?:OPEN|UNFOLD)[^']*'/,
	)?.[1];
	if (!half || !open) throw new Skip("不是折叠 AVD（用 --avd oryn-m7-fold）");
	const wv = webviewVersion();
	if (wv < 138) throw new Skip(`webview <138（${wv}）`);
	await ensurePaired();
	const T = await ensureThread();
	launch();
	await goto(`/t/${T}`);
	ash(`cmd device_state state ${half}`);
	const p = await page();
	const r = await waitFor(
		"两个 viewport segment",
		() =>
			p.evaluate(() => {
				const segs = (
					window as unknown as { viewport?: { segments?: DOMRect[] } }
				).viewport?.segments;
				const a = document
					.querySelector("[data-pane=primary]")
					?.getBoundingClientRect();
				const b = document
					.querySelector("[data-pane=secondary]")
					?.getBoundingClientRect();
				if (segs?.length !== 2 || !a || !b) return null;
				return {
					n: segs.length,
					attr: document.documentElement.dataset.segments,
					s0r: segs[0]?.right ?? 0,
					s1l: segs[1]?.left ?? 0,
					ar: a.right,
					bl: b.left,
				};
			}),
		20_000,
	);
	note(`segments ${JSON.stringify(r)}`);
	check(r.attr === "2", 'html[data-segments="2"]');
	check(r.ar <= r.s0r + 1, "primary 右边缘 ≤ 第一段右边缘");
	check(r.bl >= r.s1l - 1, "secondary 左边缘 ≥ 第二段左边缘");
	ash(`cmd device_state state ${open}`);
	await waitFor(
		"回到单段",
		() => p.evaluate(() => document.documentElement.dataset.segments === "1"),
		15_000,
	);
	ash("cmd device_state state reset");
});

function insets(): { statusBottom: number; navTop: number; height: number } {
	const out = ash("dumpsys window").stdout;
	const frame = (type: string) => {
		const m = out.match(
			new RegExp(`type=${type} frame=\\[(\\d+),(\\d+)\\]\\[(\\d+),(\\d+)\\]`),
		);
		return m ? m.slice(1).map(Number) : null;
	};
	const size = ash("wm size").stdout.match(/(\d+)x(\d+)\s*$/);
	const height = Number(size?.[2] ?? 0);
	const sb = frame("statusBars");
	const nb = frame("navigationBars");
	return { statusBottom: sb?.[3] ?? 0, navTop: nb?.[1] ?? height, height };
}

scenario("A12", async () => {
	await ensurePaired();
	const T = await ensureThread();
	launch();
	await goto(`/t/${T}`);
	ash("input keyevent 111"); // 收起键盘
	await sleep(1000);
	const ins = insets();
	const [, top, , bottom] = webViewBounds();
	note(
		`WebView 顶 ${top} 底 ${bottom}；状态栏底 ${ins.statusBottom} 导航栏顶 ${ins.navTop}`,
	);
	check(Math.abs(top - ins.statusBottom) <= 2, "WebView 顶 = 状态栏高度");
	check(Math.abs(bottom - ins.navTop) <= 2, "WebView 底 = 屏高 − 导航栏高度");
	const p = await page();
	const h0 = await p.evaluate(() => innerHeight);
	await touch("textarea");
	await waitFor(
		"软键盘弹出",
		() => ash("dumpsys input_method").stdout.includes("mInputShown=true"),
		10_000,
	);
	await sleep(1000);
	const [, , , bottom2] = webViewBounds();
	const r = await p.evaluate(() => ({
		h: innerHeight,
		b:
			document.querySelector("textarea")?.getBoundingClientRect().bottom ??
			Number.POSITIVE_INFINITY,
	}));
	note(
		`键盘弹出：WebView 底 ${bottom2}，innerHeight ${h0} → ${r.h}，输入框底 ${r.b}`,
	);
	check(bottom2 < bottom, "WebView 底上移到键盘顶");
	check(r.h < h0, "innerHeight 变小");
	check(r.b <= r.h, "输入框在可视区内");
	ash("input keyevent 111");
});

scenario("A13", async () => {
	await ensurePaired();
	const T = await ensureThread();
	launch();
	await api("POST", `/api/v1/threads/${T}/wake`);
	await goto(`/t/${T}`);
	const p = await page();
	await p.click("[data-testid=thread-tabs] [data-panel-tab=terminal]");
	await p.waitForSelector("[data-testid=terminal-panel]", { timeout: 15_000 });
	if (!(await p.$("[data-testid=terminal-view]")))
		await p.click(
			'button[aria-label="New terminal"], button[aria-label="新建终端"]',
		);
	await p.waitForSelector("[data-testid=terminal-view]", { timeout: 60_000 });
	await p.click("[data-testid=terminal-line-toggle]");
	check(
		(await p.getAttribute(
			"[data-testid=terminal-line-toggle]",
			"aria-pressed",
		)) === "true",
		"输入行已打开",
	);
	await touch("[data-testid=terminal-line-input]");
	await waitFor(
		"软键盘弹出",
		() => ash("dumpsys input_method").stdout.includes("mInputShown=true"),
		10_000,
	);
	const marker = `m7-ok-${RUN}`;
	ash(`input text ${q(`echo%s${marker}%s>/tmp/m7-term-${RUN}`)}`);
	ash("input keyevent 66");
	const orb = threadRow(T)?.orb_id ?? "";
	await waitFor(
		"终端命令执行",
		() => sh(`docker exec ${orb} cat /tmp/m7-term-${RUN}`).stdout === marker,
		15_000,
		1000,
	);
	note(`orb 里 /tmp/m7-term-${RUN} = ${marker}`);
	const rows = await p.evaluate(
		() =>
			document.querySelector("[data-testid=terminal-view]")?.textContent ?? "",
	);
	if (rows) note(`终端 DOM 文本含命令：${rows.includes(marker)}`);
	const kb = await p.evaluate(() => ({
		b:
			document
				.querySelector("[data-testid=terminal-line-input]")
				?.getBoundingClientRect().bottom ?? 1e9,
		h: innerHeight,
	}));
	check(
		kb.b <= kb.h,
		`键盘弹出期间输入行 bottom ${kb.b} ≤ innerHeight ${kb.h}`,
	);
	await p.fill("[data-testid=terminal-line-input]", `cat # m7-${RUN}`);
	await p.press("[data-testid=terminal-line-input]", "Enter");
	await waitFor(
		"cat 在跑",
		() => sh(`docker exec ${orb} pgrep -f 'cat # m7-${RUN}'`).code === 0,
		10_000,
	);
	await p.click('button:has-text("Ctrl")');
	await p.fill("[data-testid=terminal-line-input]", "c");
	await p.press("[data-testid=terminal-line-input]", "Enter");
	await waitFor(
		"cat 被中断",
		() => sh(`docker exec ${orb} pgrep -f 'cat # m7-${RUN}'`).code !== 0,
		10_000,
	);
	note("Ctrl + c 中断了 cat");
	ash("input keyevent 111");
});

scenario("A14", async () => {
	await ensurePaired();
	launch();
	const name = `m7-${RUN}.txt`;
	must(`${q(ADB)} -s ${SERIAL} shell 'cat > /sdcard/Download/${name}'`, {
		stdin: `m7 attachment ${RUN}\n`,
	});
	ash(
		`am broadcast -a android.intent.action.MEDIA_SCANNER_SCAN_FILE -d file:///sdcard/Download/${name}`,
	);
	await goto("/");
	const p = await page();
	await touch("label:has([data-testid=attach-file-input])");
	await waitFor("系统文件选择器", () => !ours(), 10_000);
	const picked = uiDump().find((n) => n.text === name);
	if (!picked) {
		await uiTap("Show roots", (n) => /Show roots|显示根目录/.test(n.desc));
		await uiTap("Downloads", (n) => /^(Downloads|下载)$/.test(n.text));
	}
	await uiTap(name, (n) => n.text === name);
	await waitFor(
		"附件上传完成",
		() => p.evaluate((nm) => document.body.textContent.includes(nm), name),
		20_000,
	);
	await p.fill("textarea", `m7 附件 ${RUN}：只回复 ok`);
	await p.press("textarea", "Enter");
	const tid = await waitFor(
		"进入新线程",
		async () => (await domPath()).match(/^\/t\/(T-[0-9a-f-]+)$/)?.[1],
		30_000,
	);
	const atts = (await api("GET", `/api/v1/threads/${tid}/attachments`))
		.json as { attachments?: { name: string }[] };
	check(
		!!atts.attachments?.some((a) => a.name === name),
		`线程 ${tid} 的附件含 ${name}`,
	);

	await goto("/");
	const pid = pidof();
	await touch("[data-testid=attach-camera]");
	const fg = await waitFor(
		"相机类应用到前台",
		() => (!ours() ? foreground() : undefined),
		10_000,
	);
	note(`前台 ${fg}`);
	check(/camera/i.test(fg), "前台是相机");
	ash("input keyevent 4");
	await waitFor("回到 App", () => ours(), 10_000);
	check(pidof() === pid, "进程没有重启（没有崩溃）");
	const chips = await p.evaluate(() =>
		document.body.textContent.includes(".jpg"),
	);
	check(!chips, "没有新增附件");
});

scenario("A15", async () => {
	await ensurePaired();
	launch();
	const p = await page();
	let after = mark();
	must(
		`${q(ADB)} -s ${SERIAL} shell am start -a android.intent.action.SEND -t text/plain --es android.intent.extra.TEXT ${q(q("m7 share"))} -n ${ACTIVITY}`,
	);
	await ev("share.received", {
		after,
		where: (e) => e.text === true && e.files === 0,
	});
	await waitFor("DOM 路径 /", async () => (await domPath()) === "/", 15_000);
	await waitFor(
		"输入框含分享文本",
		() =>
			p.evaluate(() =>
				(document.querySelector("textarea")?.value ?? "").includes("m7 share"),
			),
		10_000,
	);
	await p.fill("textarea", "");

	const img = `m7-${RUN}.png`;
	must(`${q(ADB)} -s ${SERIAL} shell 'cat > /sdcard/Pictures/${img}'`, {
		stdin: PNG,
	});
	ash(
		`am broadcast -a android.intent.action.MEDIA_SCANNER_SCAN_FILE -d file:///sdcard/Pictures/${img}`,
	);
	const id = await waitFor(
		"MediaStore id",
		() =>
			ash(
				`content query --uri content://media/external/images/media --projection _id:_display_name --where "_display_name='${img}'"`,
			).stdout.match(/_id=(\d+)/)?.[1],
		15_000,
		1000,
	);
	const attBefore =
		sql<{ n: number }>("select count(*) n from attachments")[0]?.n ?? 0;
	after = mark();
	must(
		`${q(ADB)} -s ${SERIAL} shell am start -a android.intent.action.SEND -t image/png --eu android.intent.extra.STREAM content://media/external/images/media/${id} --grant-read-uri-permission -n ${ACTIVITY}`,
	);
	await ev("share.uploaded", {
		after,
		where: (e) => e.count === 1,
		timeoutMs: 30_000,
	});
	await waitFor(
		"附件条目含图片",
		() => p.evaluate((n) => document.body.textContent.includes(n), img),
		15_000,
	);
	const attAfter =
		sql<{ n: number }>("select count(*) n from attachments")[0]?.n ?? 0;
	check(
		attAfter === attBefore + 1,
		`attachments 表 ${attBefore} → ${attAfter}`,
	);
	await ev("share.delivered", { after });
	await waitFor(
		"分享缓存已清空",
		() => !runAs("ls cache/share 2>/dev/null").stdout,
		10_000,
	);
	await p.fill("textarea", "");
});

scenario("A16", async () => {
	await ensurePaired();
	const T = await ensureThread();
	launch();
	await goto(`/t/${T}`);
	const p = await page();
	const url0 = p.url();
	const after = mark();
	await p.evaluate(() => {
		const a = document.createElement("a");
		a.href = "https://example.com/m7";
		a.target = "_blank";
		a.id = "m7-ext";
		a.textContent = "m7 外链";
		document.querySelector("[data-testid=transcript]")?.prepend(a);
	});
	await p.click("#m7-ext");
	const o = await ev("open.external", { after });
	check(o.ok === true, "open.external.ok");
	await waitFor("浏览器到前台", () => !ours(), 10_000);
	check(p.url() === url0, "WebView 的 URL 没变");
	launch();

	const name = `m7-dl-${RUN}.txt`;
	const body = te.encode(`m7 download ${RUN}\n`);
	const up = await api(
		"POST",
		`/api/v1/attachments?name=${encodeURIComponent(name)}&threadId=${T}`,
		body,
	);
	if (up.status >= 300) throw new Error(`上传附件失败 ${up.status} ${up.text}`);
	const attId = (up.json as { id: string }).id;
	const dlAfter = mark();
	await p.evaluate((u) => {
		const a = document.createElement("a");
		a.href = u;
		a.id = "m7-dl";
		a.textContent = "m7 下载";
		document.querySelector("[data-testid=transcript]")?.prepend(a);
	}, `/api/v1/attachments/${attId}/content`);
	await p.click("#m7-dl");
	await ev("download.enqueued", {
		after: dlAfter,
		where: (e) => e.name === name,
	});
	const size = await waitFor(
		"下载完成",
		() =>
			Number(ash(`stat -c %s /sdcard/Download/${name}`).stdout) || undefined,
		20_000,
		1000,
	);
	check(size === body.length, `/sdcard/Download/${name} 大小 ${size}`);
});

scenario("A17", async () => {
	await ensurePaired();
	const T = await ensureThread();
	launch();
	await goto(`/t/${T}`);
	const p = await page();
	ash("input keyevent 3");
	await sleep(40_000);
	const conn = await p.evaluate(() => document.documentElement.dataset.conn);
	check(conn === "idle", `后台 40 秒 data-conn = ${conn}`);
	const svc = ash(`dumpsys activity services ${PKG}`).stdout;
	check(
		!/ServiceRecord\{[^}]*com\.lynricsy\.oryn/.test(svc),
		"本包没有运行中的服务",
	);
	const power = ash("dumpsys power").stdout;
	const locks = power.split("Wake Locks:")[1]?.split("\n\n")[0] ?? "";
	check(!locks.includes(PKG), "本包没有持有 wakelock");
	const marker = `m7-bg-${RUN}`;
	const before = await send(T, `只回复 ${marker}`);
	await waitTurn(T, before);
	launch();
	await waitFor(
		"回前台 data-conn=open",
		() => p.evaluate(() => document.documentElement.dataset.conn === "open"),
		5000,
	);
	await waitFor(
		"后台期间的条目补齐",
		() =>
			p.evaluate((m) => {
				const t = document.querySelector<HTMLElement>(
					"[data-testid=transcript]",
				);
				if (t) t.scrollTop = t.scrollHeight;
				return (t?.textContent ?? "").includes(m);
			}, marker),
		15_000,
	);
	const lastSeq = (
		(await api("GET", `/api/v1/threads/${T}/entries?after=${before}&limit=500`))
			.json as { lastSeq?: number }
	).lastSeq;
	note(`服务端 lastSeq ${lastSeq}，转录里有 ${marker}`);
});

scenario("A18", async () => {
	await ensureNtfy();
	await ensurePaired();
	const T = await ensureThread();
	launch();
	const dev = DEVICE_ID || latestMobileDevice() || "";
	const subIds = sql<{ id: string }>(
		`select id from push_subscriptions where device_id='${dev}'`,
	).map((r) => r.id);
	note(`设备 ${dev}，推送订阅 ${subIds.join(",") || "（无）"}`);
	const after = mark();
	const r = await webApi("DELETE", `/api/v1/devices/${dev}`);
	check(r.status === 204, `吊销 → ${r.status}`);
	await ev("reauth", { after, timeoutMs: 30_000 });
	await ev("disconnect", {
		after,
		where: (e) => e.reason === "revoked",
		timeoutMs: 30_000,
	});
	const p = await page();
	await p.waitForSelector("[data-testid=launcher-url]", { timeout: 15_000 });
	note("界面回到连接页");
	check(
		!runAs("cat shared_prefs/oryn_vault.xml 2>/dev/null").stdout.includes(
			`token:${ORIGIN}`,
		),
		"保险库里没有该条",
	);
	const pushXml = runAs("cat shared_prefs/oryn_push.xml 2>/dev/null").stdout;
	check(
		!/name="enabled" value="true"/.test(pushXml),
		"oryn_push.xml 的 enabled 为假",
	);
	const t0 = Date.now();
	const before = await send(T, "只回复 ok");
	await waitTurn(T, before);
	await sleep(5000);
	const rows = subIds.length
		? sql<{ n: number }>(
				`select count(*) n from notification_deliveries where subscription_id in (${subIds.map((s) => `'${s}'`).join(",")}) and created_at >= ${t0}`,
			)[0]?.n
		: 0;
	check(rows === 0, `吊销后指向该设备订阅的新投递 ${rows} 行`);
	DEVICE_ID = "";
});

scenario("A22", async () => {
	// 网页「设置 → 设备」的二维码（宿主 Chromium，设备令牌）
	const browser: Browser = await chromium.launch({
		executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
		headless: true,
		args: ["--no-sandbox"],
	});
	try {
		const ctx = await browser.newContext({
			extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
		});
		const web = await ctx.newPage();
		await web.goto(`${BASE}/settings/devices`);
		const qr = await web.waitForSelector("[data-testid=pair-qr]", {
			timeout: 15_000,
		});
		const payload = await qr.getAttribute("data-qr-payload");
		const expect = `oryn://pair?server=${encodeURIComponent(ORIGIN)}`;
		check(payload === expect, `二维码载荷 ${payload}`);
		await web.screenshot({ path: `${OUT}/A22-pair-qr.png` });
		note(`截图 ${OUT}/A22-pair-qr.png`);
	} finally {
		await browser.close();
	}

	// 系统相机扫到二维码 = 以载荷做深链接打开 App
	await freshStart();
	const after = mark();
	must(
		`${q(ADB)} -s ${SERIAL} shell am start -a android.intent.action.VIEW -d ${q(`oryn://pair?server=${encodeURIComponent(ORIGIN)}`)} ${PKG}`,
	);
	await ev("pair.link", {
		after,
		where: (e) => e.source === "deeplink" && e.server === ORIGIN,
	});
	const started = await ev("pair.started", {
		after,
		where: (e) => e.server === ORIGIN,
		timeoutMs: 30_000,
	});
	const p = await page();
	const code = await p.textContent("[data-testid=launcher-code]");
	check(
		(code ?? "").replace(/\s/g, "").includes(String(started.code)),
		"连接页显示配对码（批准仍要输码）",
	);
	check(since(after, "browser.open").length === 0, "扫码后不自动打开浏览器");
	await approve(String(started.code));
	const approved = await ev("pair.approved", { after, timeoutMs: 30_000 });
	await ev("web.ready", { after, timeoutMs: 60_000 });
	DEVICE_ID = String(approved.deviceId);

	// 连接页「扫码」按钮拉起相机（插件在本 Activity 内取景）
	await freshStart();
	const pid = pidof();
	const lp = await page();
	await lp.waitForSelector("[data-testid=launcher-scan]", { timeout: 15_000 });
	await touch("[data-testid=launcher-scan]");
	await waitFor(
		"本包占用相机",
		() =>
			/Client Package Name: com\.lynricsy\.oryn|package=com\.lynricsy\.oryn/.test(
				ash("dumpsys media.camera").stdout,
			),
		15_000,
		1000,
	);
	note("dumpsys media.camera 有本包的相机客户端");
	ash("input keyevent 4");
	await sleep(2000);
	check(pidof() === pid, "取消扫码后进程仍在（没有崩溃）");
	if (!ours()) launch();
	await (await page()).waitForSelector("[data-testid=launcher-url]", {
		timeout: 15_000,
	});
	note("回到连接页");
	DEVICE_ID = "";
});

scenario("A23", async () => {
	if (!existsSync(GMS_APK))
		throw new Error(`没有 gms debug APK：${GMS_APK}（M7_GMS_APK）`);
	const probe = await api("POST", "/api/v1/push/subscriptions", {
		kind: "fcm",
		token: `m7-probe-${RUN}`,
	});
	if (probe.status === 409)
		throw new Error(
			`服务器未启用 FCM：${probe.text}（dev/orynd.yaml 的 push.fcm.service_account_secret + oryn secrets set）`,
		);
	check(probe.status === 201, `FCM 订阅接口可用（${probe.status}）`);
	await api(
		"DELETE",
		`/api/v1/push/subscriptions/${(probe.json as { id: string }).id}`,
	);
	if (
		!ash("pm list packages com.google.android.gms").stdout.includes(
			"com.google.android.gms",
		)
	)
		throw new Skip("镜像没有 Google Play 服务");
	const T = await ensureThread();
	ash(`pm disable-user --user 0 ${NTFY_PKG}`); // 没有 UnifiedPush 分发器 → gms 包走 FCM
	try {
		installDebug(GMS_APK);
		DEVICE_ID = "";
		await freshStart();
		DEVICE_ID = (await pairFromLauncher()).deviceId;
		await goto("/settings/notifications");
		const p = await page();
		await p.waitForSelector("[data-testid=mobile-push-card]", {
			timeout: 15_000,
		});
		const after = mark();
		await p.click("[data-testid=mobile-push-enable]");
		await waitFor(
			"推送状态 active（fcm）",
			async () =>
				(await p.getAttribute(
					"[data-testid=mobile-push-card]",
					"data-push-state",
				)) === "active" &&
				(await p.getAttribute(
					"[data-testid=mobile-push-card]",
					"data-push-via",
				)) === "fcm",
			60_000,
			1000,
		);
		const synced = await ev("push.synced", {
			after,
			where: (e) => e.kind === "fcm",
			timeoutMs: 30_000,
		});
		const row = sql<{ kind: string; device_id: string }>(
			`select kind, device_id from push_subscriptions where id='${String(synced.id)}'`,
		)[0];
		check(
			row?.kind === "fcm" && row.device_id === DEVICE_ID,
			`订阅 ${String(synced.id)}：kind=${row?.kind}，归属本设备`,
		);
		check(
			!since(after).some((e) => JSON.stringify(e).match(/"token":"[^"]{20,}/)),
			"事件日志不含 FCM 令牌",
		);

		ash("input keyevent 3");
		await waitFor("回到桌面", () => !ours(), 10_000);
		let evAfter = mark();
		let before = await send(T, "数到 3");
		await waitTurn(T, before);
		await ev("push.received", {
			after: evAfter,
			where: (e) => e.via === "fcm",
			timeoutMs: 30_000,
		});
		await ev("notify.posted", {
			after: evAfter,
			where: (e) => e.path === `/t/${T}`,
		});
		await waitFor(
			"系统通知",
			() => notifications().find((x) => x.title.includes(THREAD_TITLE)),
			15_000,
			1000,
		);
		const sub = sql<{ last_success_at: number | null }>(
			`select last_success_at from push_subscriptions where id='${String(synced.id)}'`,
		)[0];
		check(!!sub?.last_success_at, "服务端记录了 FCM 投递成功");

		ash(`am kill ${PKG}`);
		await waitFor("进程已不在", () => !pidof(), 10_000);
		evAfter = mark();
		before = await send(T, "数到 3");
		await waitTurn(T, before);
		await ev("push.received", {
			after: evAfter,
			where: (e) => e.via === "fcm",
			timeoutMs: 60_000,
		});
		note("进程被杀后 FCM 高优先级消息仍拉起进程并发出通知");
	} finally {
		ash(`pm enable ${NTFY_PKG}`);
		installDebug(APK);
		DEVICE_ID = "";
	}
});

scenario("A20", async () => {
	await freshStart();
	const p = await page();
	const cdp = await p.context().newCDPSession(p);
	const ua = (major: number) =>
		`Mozilla/5.0 (Linux; Android 14; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/${major}.0.0.0 Mobile Safari/537.36`;
	await cdp.send("Emulation.setUserAgentOverride", { userAgent: ua(110) });
	await p.reload();
	await p.waitForSelector("#webview-outdated", { timeout: 15_000 });
	const text = await p.textContent("#webview-outdated");
	note(`提示：${text}`);
	check(/WebView/.test(text ?? ""), "内核 110 时插入提示");
	const ui = uiDump().some(
		(n) =>
			n.text.includes("系统 WebView 版本过旧") ||
			/WebView .*outdated|update Android System WebView/i.test(n.text),
	);
	check(ui, "界面层读得到提示");
	await cdp.send("Emulation.setUserAgentOverride", { userAgent: ua(111) });
	await p.reload();
	await sleep(3000);
	check(!(await p.$("#webview-outdated")), "内核 111 时不插入");
	check(!!pidof(), "进程没有崩溃");
	await cdp
		.send("Emulation.setUserAgentOverride", { userAgent: "" })
		.catch(() => {});
});

scenario("A19", async () => {
	const pw = process.env.M7_KEYSTORE_PASSWORD;
	if (!pw || !existsSync(KEYSTORE))
		throw new Error("需要 .dev/android-release.jks 与 M7_KEYSTORE_PASSWORD");
	const docs = readFileSync(`${ROOT}/docs/mobile.md`, "utf8");
	const expectFp = docs.match(
		/SHA-256[^\n]*?((?:[0-9A-F]{2}:){31}[0-9A-F]{2})/,
	)?.[1];
	if (!expectFp)
		throw new Error("docs/mobile.md 里没有记录发布证书的 SHA-256 指纹");
	const props = `${ROOT}/web/src-tauri/gen/android/keystore.properties`;
	writeFileSync(
		props,
		`keyAlias=${process.env.M7_KEY_ALIAS ?? "oryn"}\npassword=${pw}\nstoreFile=${KEYSTORE}\n`,
		{ mode: 0o600 },
	);
	try {
		must("bun run tauri android build --apk --aab --target x86_64", {
			cwd: `${ROOT}/web`,
			timeoutMs: 1_800_000,
		});
	} finally {
		rmSync(props, { force: true });
	}
	const bt = aapt2();
	const certs = must(
		`${q(`${bt}/apksigner`)} verify --print-certs ${q(RELEASE_APK)}`,
	).stdout;
	const fp = certs
		.match(/certificate SHA-256 digest: ([0-9a-f]+)/)?.[1]
		?.toUpperCase()
		.match(/../g)
		?.join(":");
	check(fp === expectFp, `证书 SHA-256 ${fp}`);
	check(
		sh(`${q(`${bt}/zipalign`)} -c -P 16 -v 4 ${q(RELEASE_APK)}`).code === 0,
		"zipalign 校验通过",
	);
	ash(`pm uninstall ${PKG}`);
	try {
		must(`${q(ADB)} -s ${SERIAL} install -g ${q(RELEASE_APK)}`, {
			timeoutMs: 300_000,
		});
		const resolved = ash(
			"pm resolve-activity -a android.intent.action.VIEW -d oryn://thread/x",
		).stdout;
		check(resolved.includes(PKG), "oryn://thread/x 解析到本包");
		launch();
		const devBefore =
			sql<{ n: number }>(
				"select count(*) n from devices where kind='mobile'",
			)[0]?.n ?? 0;
		await uiTap(
			"地址输入框",
			(n) => n.cls === "android.widget.EditText" && n.pkg === PKG,
			30_000,
		);
		ash(`input text ${q(BASE)}`);
		await uiTap(
			"连接按钮",
			(n) => n.pkg === PKG && /^(连接|Connect)$/.test(n.text || n.desc),
		);
		const code = await waitFor(
			"新的配对码",
			() =>
				sql<{ code: string }>(
					"select code from device_pairings order by created_at desc limit 1",
				)[0]?.code,
			30_000,
			1000,
		);
		await approve(code);
		await waitFor(
			"界面层出现线程列表",
			() =>
				uiDump().some(
					(n) =>
						n.pkg === PKG &&
						(n.text.includes(THREAD_TITLE || "m7") || n.text.includes("m7")),
				),
			60_000,
			2000,
		);
		const devAfter =
			sql<{ n: number }>(
				"select count(*) n from devices where kind='mobile'",
			)[0]?.n ?? 0;
		check(devAfter === devBefore + 1, `mobile 设备 ${devBefore} → ${devAfter}`);
	} finally {
		ash(`pm uninstall ${PKG}`);
		installDebug(APK);
		DEVICE_ID = "";
	}
});

// ---------------------------------------------------------------- 运行

SERIAL = await bootDevice();
console.log(`M7 验收 run=${RUN} avd=${AVD} serial=${SERIAL} out=${OUT}`);
adb(`reverse tcp:${PORT} tcp:${PORT}`);
installDebug(APK);
grantAll();
const wv = webviewVersion();
console.log(`  WebView 主版本 ${wv}`);
const devices = await android.devices();
const found = devices.find((d) => d.serial() === SERIAL);
for (const d of devices) if (d !== found) await d.close();
if (!found) throw new Error(`playwright 找不到 ${SERIAL}`);
DEVICE = found;

const health = await req("GET", "/healthz", {});
if (health.status !== 200)
	throw new Error(`dev 部署不可用：GET /healthz → ${health.status}`);

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
		if (e instanceof Skip) {
			console.log(`${id} SKIP(${e.message})`);
			results.push({ id, verdict: "SKIP", reason: e.message, evidence });
			continue;
		}
		note(`错误：${e instanceof Error ? e.message : String(e)}`);
		const shot = `${OUT}/${id}-FAIL.png`;
		Bun.write(shot, await DEVICE.screenshot().catch(() => new Uint8Array()));
		note(`截图 ${shot}`);
		writeFileSync(`${OUT}/${id}-events.jsonl`, events().map(show).join("\n"));
		console.log(`${id} FAIL (${((Date.now() - t0) / 1000).toFixed(1)}s)`);
		results.push({ id, verdict: "FAIL", evidence });
	}
}

// 收尾：恢复显示设置、撤销端口转发、停 ntfy 容器；模拟器保持运行
ash("wm size reset && wm density reset && settings put system user_rotation 0");
adb(`reverse --remove tcp:${PORT}`);
if (ntfyReady) {
	adb(`reverse --remove tcp:${NTFY_PORT}`);
	sh(`docker stop ${NTFY_CONTAINER}`);
}
await DEVICE.close();
writeFileSync(
	`${OUT}/results.json`,
	JSON.stringify(
		{ run: RUN, avd: AVD, serial: SERIAL, webview: wv, results },
		null,
		2,
	),
);
console.log("\n汇总");
for (const r of results)
	console.log(`${r.id} ${r.verdict}${r.reason ? `(${r.reason})` : ""}`);
process.exit(results.some((r) => r.verdict !== "PASS") ? 1 : 0);
