#!/usr/bin/env bun
/**
 * M6 真机验收（计划「验证」D1–D15，Linux x64）：自建一套隔离环境，逐项打印 `D<n> PASS|FAIL` 与证据，退出时清理。
 *
 * 用法（web/ 目录）：bun run accept:m6 [-- --only D2,D5]
 *   `accept:m6` 把本脚本包在 `dbus-run-session -- xvfb-run …` 里：私有 D-Bus 会话总线 + Xvfb，宿主的会话与显示不受影响。
 *
 * 前置（一次，见 docs/desktop.md「本机验收」）：
 *   sudo pacman -S --needed libayatana-appindicator dunst gnome-keyring patchelf
 *   make web && make build                                   # bin/orynd bin/orbd bin/oryn bin/fakeomp
 *   go run ./dev/tlsproxy -init -dir .dev/tls                # 本机 CA 与 localhost 证书
 *   sudo trust anchor --store .dev/tls/ca.crt                # 让 WebKitGTK / reqwest 信任该 CA
 *   cd web && bun run tauri build --no-bundle                # 或让 D1 构建全部安装包
 *
 * 隔离环境（全部在临时目录 $TMP 下）：
 *   orynd（listen 127.0.0.1:7610，public_url https://localhost:7643，data_dir $TMP/data）；
 *   dev/tlsproxy（127.0.0.1:7643 → 7610，证书目录 .dev/tls，`-static $TMP/updates` 供 D11）；
 *   provider（driver: process，orbd = bin/orbd，agent = bin/fakeomp，orb 根目录 $TMP/orbs）；
 *   gnome-keyring-daemon（HOME / XDG_* 指向 $TMP/keyring，只挂在私有会话总线上）；dunst（所有通知 timeout = 0）；
 *   PATH 前置一个只把参数写进 $TMP/xdg-open.log 的假 xdg-open；XDG_RUNTIME_DIR = $TMP/run。
 *   App 一律带 XDG_CONFIG_HOME / XDG_DATA_HOME / XDG_CACHE_HOME = $TMP/<app>/* 与 ORYN_DESKTOP_EVENT_LOG=$TMP/events.jsonl 启动；
 *   观察面 = 事件日志（web/src-tauri/src/eventlog.rs）+ 服务端 API / 库表 + xdotool / dunstctl / secret-tool。
 *
 * 环境变量：
 *   ORYN_DESKTOP_BIN  App 二进制（默认 web/src-tauri/target/release/oryn-desktop）
 *   ORYN_URL          App 连接的服务器 origin（默认 https://localhost:7643；改成已有真实证书的域名时，该域名须反代到 127.0.0.1:7610，
 *                     此时 D12 的断网步骤不再有效）
 *   M6_WM             先启动的窗口管理器（如 openbox；没有窗口管理器时 xdotool 的窗口操作不生效再设）
 *   M6_D11_TARGET     D11 两次签名构建用的 CARGO_TARGET_DIR（默认 .dev/m6-d11-target，与正式构建隔离、跨次复用）
 *   M6_NO_STRIP=1     构建 AppImage 时设 NO_STRIP=true（Arch 上 linuxdeploy 的 strip 失败时）
 *   M6_TAG            D14 / D15 检查的 tag（默认 desktop-v<Cargo.toml 版本>-rc.1；需先推送该 tag，脚本不推送）
 *   M6_OUT            结果与日志的保存目录（默认 /tmp/oryn-m6-accept/<run>）
 *   M6_KEEP=1         不删除 $TMP（排查用）
 *
 * 场景之间的依赖：D3 / D6–D10 / D12 需要已配对的主 App（`--only` 单跑时自动先配对，不做 D2 的断言）；D6 / D8 / D12 共用线程 $T；
 * D11 会先退出主 App（同一总线上单实例），D12 再把它启动起来；D12 吊销设备后，再跑需要主 App 的场景会重新配对。
 */
import {
	appendFileSync,
	chmodSync,
	copyFileSync,
	cpSync,
	existsSync,
	mkdirSync,
	mkdtempSync,
	openSync,
	readdirSync,
	readFileSync,
	readlinkSync,
	rmSync,
	statSync,
	writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import type { Subprocess } from "bun";
import { buildManifest } from "./updater-manifest.ts";

const ROOT = `${import.meta.dir}/../..`;
const BIN = `${ROOT}/bin`;
const APP =
	process.env.ORYN_DESKTOP_BIN ??
	`${ROOT}/web/src-tauri/target/release/oryn-desktop`;
const IDENT = "com.lynricsy.oryn";
const ORYND_PORT = 7610;
const TLS_PORT = 7643;
const BASE = `http://127.0.0.1:${ORYND_PORT}`;
const LOCAL_ORIGIN = `https://localhost:${TLS_PORT}`;
const ORIGIN = process.env.ORYN_URL ?? LOCAL_ORIGIN;
const TLS_DIR = `${ROOT}/.dev/tls`;
const RUN = Math.random().toString(36).slice(2, 6);
const TMP = mkdtempSync(`${tmpdir()}/oryn-m6-`);
const OUT = process.env.M6_OUT ?? `/tmp/oryn-m6-accept/${RUN}`;
const DB = `${TMP}/data/oryn.db`;
const EVENTS = `${TMP}/events.jsonl`;
const XDG_LOG = `${TMP}/xdg-open.log`;
const LOGS = `${TMP}/logs`;
const VERSION = cargoVersion();

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

for (const d of [LOGS, `${TMP}/run`, `${TMP}/fakebin`, `${TMP}/updates`])
	mkdirSync(d, { recursive: true });
chmodSync(`${TMP}/run`, 0o700);

type Env = Record<string, string | undefined>;
/** 所有子进程的基础环境：私有运行时目录；会话总线与 DISPLAY 继承自 dbus-run-session / xvfb-run。 */
const ENV: Env = { ...process.env, XDG_RUNTIME_DIR: `${TMP}/run` };

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;

function cargoVersion(): string {
	const toml = readFileSync(`${ROOT}/web/src-tauri/Cargo.toml`, "utf8");
	const pkg = toml.split(/^\[/m).find((s) => s.startsWith("package]")) ?? "";
	const m = pkg.match(/^version\s*=\s*"([^"]+)"/m);
	if (!m?.[1]) throw new Error("读不到 web/src-tauri/Cargo.toml 的版本");
	return m[1];
}

function sh(
	cmd: string,
	opts: { env?: Env; cwd?: string; timeoutMs?: number } = {},
): { code: number; out: string; stdout: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], {
		cwd: opts.cwd ?? ROOT,
		env: opts.env ?? ENV,
		timeout: opts.timeoutMs,
	});
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
		stdout: r.stdout.toString().trim(),
	};
}

function have(tool: string): boolean {
	return sh(`command -v ${tool}`).code === 0;
}

interface Bg {
	name: string;
	proc: Subprocess;
	log: string;
}
const bgs: Bg[] = [];

/** 后台进程，stdout / stderr 追加到 $TMP/logs/<name>.log；退出时统一清理。 */
function bg(
	name: string,
	cmd: string[],
	opts: { env?: Env; cwd?: string; stdin?: "pipe" | "ignore" } = {},
): Bg {
	const log = `${LOGS}/${name}.log`;
	const fd = openSync(log, "a");
	const proc = Bun.spawn(cmd, {
		cwd: opts.cwd ?? ROOT,
		env: opts.env ?? ENV,
		stdin: opts.stdin ?? "ignore",
		stdout: fd,
		stderr: fd,
	});
	const b = { name, proc, log };
	bgs.push(b);
	return b;
}

const alive = (p: Subprocess) => p.exitCode === null && p.signalCode === null;

async function exited(p: Subprocess, ms: number): Promise<number | null> {
	const r = await Promise.race([p.exited, sleep(ms).then(() => "timeout")]);
	return r === "timeout" ? null : (r as number);
}

async function stop(b: Bg | undefined, ms = 5000) {
	if (!b || !alive(b.proc)) return;
	b.proc.kill("SIGTERM");
	if ((await exited(b.proc, ms)) === null) b.proc.kill("SIGKILL");
	await b.proc.exited;
}

function sql<T = Record<string, unknown>>(qs: string): T[] {
	const r = Bun.spawnSync(["sqlite3", "-json", DB, qs]);
	const s = r.stdout.toString().trim();
	return s ? (JSON.parse(s) as T[]) : [];
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

let CLI = "";
let WEB = "";

interface ApiRes {
	status: number;
	json: unknown;
	text: string;
	headers: Headers;
}

async function api(
	method: string,
	path: string,
	body?: unknown,
	headers?: Record<string, string>,
): Promise<ApiRes> {
	const res = await fetch(`${BASE}${path}`, {
		method,
		headers: {
			...(headers ?? { Authorization: `Bearer ${CLI}` }),
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
	return { status: res.status, json, text, headers: res.headers };
}

function cli(args: string) {
	return sh(`${q(`${BIN}/oryn`)} ${args}`, {
		env: { ...ENV, ORYN_API_URL: BASE, ORYN_API_TOKEN: CLI },
	});
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

function screenshot(name: string) {
	if (!have("import")) return;
	mkdirSync(OUT, { recursive: true });
	const p = `${OUT}/${name}.png`;
	if (sh(`import -window root ${q(p)}`).code === 0) note(`截图 ${p}`);
}

// ---------------------------------------------------------------- 事件日志

interface Ev {
	ts: number;
	event: string;
	/** 在事件日志里的行号（0 起） */
	i: number;
	[k: string]: unknown;
}

function events(): Ev[] {
	if (!existsSync(EVENTS)) return [];
	const out: Ev[] = [];
	for (const line of readFileSync(EVENTS, "utf8").split("\n")) {
		if (!line.trim()) continue;
		try {
			out.push({ ...(JSON.parse(line) as Ev), i: out.length });
		} catch {
			// 写了一半的行
		}
	}
	return out;
}

/** 当前事件条数：之后 `ev(..., {after})` 只看这之后的事件。 */
const mark = () => events().length;

function since(after: number, name?: string): Ev[] {
	return events().filter((e) => e.i >= after && (!name || e.event === name));
}

const show = (e: Ev) => {
	const { i: _i, ts: _ts, ...rest } = e;
	return JSON.stringify(rest);
};

/** 在事件日志里等到 `name`（默认 15 秒）。 */
async function ev(
	name: string,
	opts: { after?: number; where?: (e: Ev) => boolean; timeoutMs?: number } = {},
): Promise<Ev> {
	const e = await waitFor(
		`事件 ${name}`,
		() =>
			since(opts.after ?? 0, name).find((x) => !opts.where || opts.where(x)),
		opts.timeoutMs ?? 15_000,
		200,
	);
	note(`事件 ${show(e)}`);
	return e;
}

// ---------------------------------------------------------------- App 进程

interface App {
	name: string;
	bin: string;
	env: Env;
	proc: Subprocess;
	/** 启动时的事件条数 */
	start: number;
}

function appEnv(name: string, extra: Env = {}): Env {
	const base = `${TMP}/${name}`;
	return {
		...ENV,
		XDG_CONFIG_HOME: `${base}/config`,
		XDG_DATA_HOME: `${base}/data`,
		XDG_CACHE_HOME: `${base}/cache`,
		ORYN_DESKTOP_EVENT_LOG: EVENTS,
		PATH: `${TMP}/fakebin:${process.env.PATH ?? ""}`,
		// Xvfb 下 WebKitGTK 的 DMA-BUF / 合成渲染不可用
		WEBKIT_DISABLE_DMABUF_RENDERER: "1",
		WEBKIT_DISABLE_COMPOSITING_MODE: "1",
		NO_AT_BRIDGE: "1",
		...extra,
	};
}

const cfgDir = (name: string) => `${TMP}/${name}/config/${IDENT}`;

function readConfig(name: string): Record<string, unknown> {
	return JSON.parse(readFileSync(`${cfgDir(name)}/config.json`, "utf8"));
}

function writeConfig(name: string, cfg: Record<string, unknown>) {
	mkdirSync(cfgDir(name), { recursive: true });
	writeFileSync(`${cfgDir(name)}/config.json`, JSON.stringify(cfg, null, 2));
}

let appSeq = 0;
function launch(
	name: string,
	args: string[] = [],
	opts: { bin?: string; extra?: Env } = {},
): App {
	const env = appEnv(name, opts.extra);
	const bin = opts.bin ?? APP;
	const start = mark();
	const b = bg(`app-${name}-${++appSeq}`, [bin, ...args], { env });
	return { name, bin, env, proc: b.proc, start };
}

/** 跑一次（第二实例、`--quit` 等）并等它退出。 */
async function once(
	name: string,
	args: string[],
	opts: { bin?: string; extra?: Env; timeoutMs?: number } = {},
): Promise<{ code: number | null; ms: number }> {
	const t0 = Date.now();
	const a = launch(name, args, opts);
	const code = await exited(a.proc, opts.timeoutMs ?? 10_000);
	if (code === null) a.proc.kill("SIGKILL");
	return { code, ms: Date.now() - t0 };
}

/** `--quit` 让运行中的实例正常退出；返回它的退出码（不是自己启动的进程时返回 null）。 */
async function quit(app: App | undefined, timeoutMs = 20_000) {
	if (!app) return null;
	await once(app.name, ["--quit"], { bin: app.bin, extra: pick(app.env) });
	if (!alive(app.proc)) return app.proc.exitCode;
	const code = await exited(app.proc, timeoutMs);
	if (code === null) {
		note(`${app.name} 没有响应 --quit，强制结束`);
		app.proc.kill("SIGKILL");
	}
	return code;
}

/** appEnv 之外的那部分（如单独的总线地址），让第二实例与第一实例在同一环境。 */
function pick(env: Env): Env {
	const keys = ["DBUS_SESSION_BUS_ADDRESS", "APPIMAGE_EXTRACT_AND_RUN"];
	return Object.fromEntries(keys.map((k) => [k, env[k]]).filter(([, v]) => v));
}

function pids(): number[] {
	const r = sh("pgrep -x oryn-desktop");
	return r.stdout ? r.stdout.split("\n").map(Number) : [];
}

function windowId(): string {
	for (const flag of ["--onlyvisible ", ""]) {
		const r = sh(`xdotool search ${flag}--name '^Oryn$'`);
		const id = r.stdout.split("\n")[0]?.trim();
		if (id) return id;
	}
	throw new Error("找不到标题为 Oryn 的窗口");
}

// ---------------------------------------------------------------- 线程

interface Thread {
	id: string;
	state: string;
	unread: boolean;
	title: string;
	orbId?: string | null;
}

async function getThread(tid: string): Promise<Thread> {
	const r = await api("GET", `/api/v1/threads/${tid}`);
	if (r.status !== 200) throw new Error(`GET threads/${tid} ${r.status}`);
	return r.json as Thread;
}

async function createThread(body: Record<string, unknown>): Promise<string> {
	const r = await api("POST", "/api/v1/threads", body);
	if (r.status !== 201) throw new Error(`建线程失败 ${r.status} ${r.text}`);
	const j = r.json as { thread?: { id: string }; id?: string };
	const id = j.thread?.id ?? j.id;
	if (!id) throw new Error(`建线程的应答里没有 id：${r.text}`);
	return id;
}

function entryCount(tid: string): number {
	return (
		sql<{ c: number }>(
			`select count(*) c from entries where thread_id='${tid}'`,
		)[0]?.c ?? 0
	);
}

async function waitTurn(tid: string, before: number, timeoutMs = 120_000) {
	return waitFor(
		`线程 ${tid} 这一轮结束`,
		async () => {
			if (entryCount(tid) < before + 2) return undefined;
			const t = await getThread(tid);
			return t.state === "idle" ? t : undefined;
		},
		timeoutMs,
		500,
	);
}

async function notificationCount(): Promise<number> {
	const r = await api("GET", "/api/v1/notifications?limit=100");
	if (r.status !== 200) throw new Error(`GET notifications ${r.status}`);
	return (r.json as { notifications: unknown[] }).notifications.length;
}

function secretEntries(): string[] {
	const r = sh(`secret-tool search --all service ${IDENT}`);
	return r.out.split("\n").filter((l) => /^secret = odt_/.test(l.trim()));
}

// ---------------------------------------------------------------- 环境

let TLS: Bg | undefined;
let MAIN: App | undefined;
let DEVICE = "";
let T = "";

function writeFiles() {
	writeFileSync(
		`${TMP}/orynd.yaml`,
		`listen: ["127.0.0.1:${ORYND_PORT}"]
public_url: "${ORIGIN}"
portal_domain: ""
data_dir: "${TMP}/data"
log_level: "info"
idle: { agent_idle: "30m", user_idle: "30m", stop_after_frozen: "24h", wake_on_focus: true }
orb_defaults: { provider: "local", type: "" }
`,
	);
	writeFileSync(
		`${TMP}/provider.yaml`,
		`url: "${BASE}"
token_file: "${TMP}/provider.token"
orb_url: "${BASE}"
driver: process
`,
	);
	writeFileSync(
		`${TMP}/fakebin/xdg-open`,
		`#!/bin/sh\nprintf '%s\\n' "$*" >> ${q(XDG_LOG)}\n`,
	);
	chmodSync(`${TMP}/fakebin/xdg-open`, 0o755);
	// 所有紧急级别都不自动消失，便于 dunstctl 读取与触发
	writeFileSync(
		`${TMP}/dunstrc`,
		"[global]\n    monitor = 0\n[urgency_low]\n    timeout = 0\n[urgency_normal]\n    timeout = 0\n[urgency_critical]\n    timeout = 0\n",
	);
}

function startTls() {
	TLS = bg("tlsproxy", [
		`${TMP}/bin/tlsproxy`,
		"-listen",
		`127.0.0.1:${TLS_PORT}`,
		"-target",
		BASE,
		"-dir",
		TLS_DIR,
		"-static",
		`${TMP}/updates`,
	]);
	return waitFor(
		"tlsproxy 就绪",
		() =>
			sh(
				`curl -sk -o ${q(`${TMP}/curl.out`)} -w '%{http_code}' ${LOCAL_ORIGIN}/healthz`,
			).stdout === "200",
		15_000,
	);
}

async function hasBusName(name: string, env: Env = ENV) {
	const r = sh(
		`gdbus call --session --dest org.freedesktop.DBus --object-path /org/freedesktop/DBus --method org.freedesktop.DBus.NameHasOwner ${name}`,
		{ env },
	);
	return r.stdout.includes("true");
}

async function setup() {
	if (!process.env.DISPLAY || !process.env.DBUS_SESSION_BUS_ADDRESS)
		throw new Error(
			"需要在 dbus-run-session + xvfb-run 里运行：cd web && bun run accept:m6",
		);
	const missing = [
		"curl",
		"sqlite3",
		"go",
		"gdbus",
		"dbus-daemon",
		"gnome-keyring-daemon",
		"secret-tool",
		"dunst",
		"dunstctl",
		"xdotool",
		"xdg-mime",
		"ar",
		"rg",
	].filter((t) => !have(t));
	if (missing.length) throw new Error(`缺少工具：${missing.join(" ")}`);
	for (const b of ["orynd", "orbd", "oryn", "fakeomp"])
		if (!existsSync(`${BIN}/${b}`))
			throw new Error(`缺少 ${BIN}/${b}：先 make web && make build`);
	writeFiles();

	if (process.env.M6_WM) bg("wm", [process.env.M6_WM]);

	// 私有凭据库：HOME / XDG_* 都在 $TMP，只注册在本脚本的会话总线上；空口令解锁
	const kr = `${TMP}/keyring`;
	for (const d of ["data", "config", "cache"])
		mkdirSync(`${kr}/${d}`, { recursive: true });
	const keyring = bg(
		"gnome-keyring",
		[
			"gnome-keyring-daemon",
			"--foreground",
			"--unlock",
			"--components=secrets",
		],
		{
			env: {
				...ENV,
				HOME: kr,
				XDG_DATA_HOME: `${kr}/data`,
				XDG_CONFIG_HOME: `${kr}/config`,
				XDG_CACHE_HOME: `${kr}/cache`,
			},
			stdin: "pipe",
		},
	);
	// 口令必须非空：空口令时守护进程不会自建 login 集合，而是去调图形提示器（Xvfb 下无显示可用），
	// 结果 Secret Service 没有默认集合，App 只能回退到文件。
	const stdin = keyring.proc.stdin;
	if (stdin && typeof stdin !== "number") {
		stdin.write("oryn-accept");
		stdin.end();
	}
	// 名字没被我们的守护进程占上就停：否则 App 的调用会经 D-Bus 激活一个读写真实 HOME 的实例
	await waitFor(
		"gnome-keyring 占用 org.freedesktop.secrets",
		() => hasBusName("org.freedesktop.secrets"),
		10_000,
	);

	bg("dunst", ["dunst", "-config", `${TMP}/dunstrc`]);
	await waitFor(
		"dunst 就绪",
		() => hasBusName("org.freedesktop.Notifications"),
		10_000,
	);

	bg("orynd", [`${BIN}/orynd`, "--config", `${TMP}/orynd.yaml`]);
	await waitFor(
		"orynd 就绪",
		async () => (await fetch(`${BASE}/healthz`)).ok,
		30_000,
	);

	const build = sh(`go build -o ${q(`${TMP}/bin/tlsproxy`)} ./dev/tlsproxy`);
	if (build.code !== 0) throw new Error(`构建 tlsproxy 失败：${build.out}`);
	await startTls();
	// 系统信任库里没有本机 CA 时，WebKitGTK 与 App 的 reqwest 都会拒绝 https://localhost:7643
	if (
		ORIGIN === LOCAL_ORIGIN &&
		sh(`curl -s -o ${q(`${TMP}/curl.out`)} ${LOCAL_ORIGIN}/healthz`).code !== 0
	)
		console.warn(
			`!! 系统不信任 ${TLS_DIR}/ca.crt：先 sudo trust anchor --store ${TLS_DIR}/ca.crt`,
		);

	const admin = (args: string) =>
		sh(`${q(`${BIN}/orynd`)} --config ${q(`${TMP}/orynd.yaml`)} admin ${args}`);
	const dev = admin("create-device --name accept --kind cli");
	if (dev.code !== 0) throw new Error(`create-device 失败：${dev.out}`);
	CLI = dev.stdout;
	const ses = admin("create-web-session --user-agent m6-accept");
	if (ses.code !== 0) throw new Error(`create-web-session 失败：${ses.out}`);
	WEB = ses.stdout;

	const prov = cli("provider create local");
	if (prov.code !== 0) throw new Error(`provider create 失败：${prov.out}`);
	writeFileSync(`${TMP}/provider.token`, `${prov.stdout}\n`, { mode: 0o600 });
	bg(
		"provider",
		[`${BIN}/oryn`, "provider", "run", "--config", `${TMP}/provider.yaml`],
		{
			env: {
				...ENV,
				ORYN_ORBD_BIN: `${BIN}/orbd`,
				ORYN_AGENT_BIN: `${BIN}/fakeomp`,
				ORYN_PROCESS_ROOT: `${TMP}/orbs`,
			},
		},
	);
}

/** 结束 cwd 落在 $TMP 下的残余进程（process 驱动起的 orbd / fakeomp 等）。 */
function killStragglers() {
	for (const pid of readdirSync("/proc")) {
		if (!/^\d+$/.test(pid) || Number(pid) === process.pid) continue;
		try {
			if (readlinkSync(`/proc/${pid}/cwd`).startsWith(TMP))
				process.kill(Number(pid), "SIGKILL");
		} catch {
			// 已退出或无权限
		}
	}
}

let cleaned = false;
async function cleanup() {
	if (cleaned) return;
	cleaned = true;
	for (const b of bgs.filter((x) => x.name.startsWith("app-")))
		if (alive(b.proc)) b.proc.kill("SIGTERM");
	// 自更新重启出来的实例不是本脚本的子进程
	for (const pid of pids()) {
		try {
			if (readFileSync(`/proc/${pid}/environ`, "utf8").includes(EVENTS))
				process.kill(pid, "SIGTERM");
		} catch {
			// 已退出
		}
	}
	for (const b of [...bgs].reverse()) await stop(b, 3000);
	killStragglers();
	try {
		mkdirSync(OUT, { recursive: true });
		for (const f of [EVENTS, XDG_LOG])
			if (existsSync(f)) copyFileSync(f, `${OUT}/${f.split("/").pop()}`);
		cpSync(LOGS, `${OUT}/logs`, { recursive: true });
		writeFileSync(`${OUT}/results.json`, JSON.stringify(results, null, 2));
	} catch (e) {
		console.warn(`保存结果失败：${String(e)}`);
	}
	if (process.env.M6_KEEP === "1") console.log(`保留临时目录 ${TMP}`);
	else rmSync(TMP, { recursive: true, force: true });
}

for (const sig of ["SIGINT", "SIGTERM"] as const)
	process.on(sig, () => {
		void cleanup().then(() => process.exit(130));
	});

// ---------------------------------------------------------------- 夹具

/** 在 name 目录下用 `--server` 启动并完成配对（`--only` 单跑时复用），返回设备号与启动前的事件位置。 */
async function pairApp(name: string, extra: Env = {}) {
	const after = mark();
	const app = launch(name, ["--server", ORIGIN], { extra });
	const started = await ev("pair.started", { after, timeoutMs: 30_000 });
	const r = await api(
		"POST",
		"/api/v1/devices/pair/approve",
		{ code: started.code },
		{ Cookie: `__Host-oryn_session=${WEB}` },
	);
	if (r.status >= 300) throw new Error(`批准配对失败 ${r.status} ${r.text}`);
	note(`已批准配对 ${String(started.code)}`);
	const approved = await ev("pair.approved", { after, timeoutMs: 30_000 });
	return { app, deviceId: String(approved.deviceId), started, after };
}

/** 主 App（目录 app）已配对并进入远程页面。 */
async function ensureMain(): Promise<App> {
	if (MAIN && alive(MAIN.proc) && DEVICE) return MAIN;
	if (!DEVICE) {
		const p = await pairApp("app");
		MAIN = p.app;
		DEVICE = p.deviceId;
		await ev("web.ready", { after: p.after, timeoutMs: 30_000 });
		return MAIN;
	}
	MAIN = launch("app");
	await ev("web.ready", { after: MAIN.start, timeoutMs: 30_000 });
	return MAIN;
}

/** 主 App 窗口当前是否可见（本进程启动后没有 window 事件 = 刚启动，可见）。 */
function mainVisible(): boolean {
	const last = since(MAIN?.start ?? 0, "window").pop();
	return last ? last.visible === true : true;
}

async function ensureVisible() {
	if (mainVisible()) return;
	const a = mark();
	await once("app", []); // 第二实例 → 显示并聚焦主窗口
	await ev("window", { after: a, where: (e) => e.visible === true });
}

async function ensureHidden() {
	if (!mainVisible()) return;
	const a = mark();
	sh(`xdotool windowquit ${windowId()}`);
	await ev("window", { after: a, where: (e) => e.visible === false });
}

/** 线程 $T：「回复 ok」跑完、未读。 */
async function ensureThread(): Promise<string> {
	if (T) return T;
	T = await createThread({
		prompt: "回复 ok",
		executor: { kind: "new_orb", provider: "local" },
	});
	note(`线程 ${T}`);
	await waitFor(
		`线程 ${T} idle 且未读`,
		async () => {
			const t = await getThread(T);
			return t.state === "idle" && t.unread ? t : undefined;
		},
		180_000,
		1000,
	);
	return T;
}

function orbOf(tid: string): string {
	const r = sql<{ orb_id: string | null }>(
		`select orb_id from threads where id='${tid}'`,
	)[0];
	if (!r?.orb_id) throw new Error(`线程 ${tid} 没有 orb`);
	return r.orb_id;
}

// ---------------------------------------------------------------- D1–D15

async function D1() {
	// tauri.conf.json 里配了更新公钥，打包时必须有对应私钥才能产出 .sig；本机用 .dev/ 下那把（CI 用 secrets）
	const keyFile = `${ROOT}/.dev/desktop-updater.key`;
	const signing = existsSync(keyFile)
		? {
				TAURI_SIGNING_PRIVATE_KEY: readFileSync(keyFile, "utf8"),
				TAURI_SIGNING_PRIVATE_KEY_PASSWORD: readFileSync(
					`${keyFile}.password`,
					"utf8",
				),
			}
		: {};
	const r = sh("bun run tauri build", {
		cwd: `${ROOT}/web`,
		env: {
			...ENV,
			...signing,
			...(process.env.M6_NO_STRIP === "1" ? { NO_STRIP: "true" } : {}),
		},
		timeoutMs: 60 * 60_000,
	});
	check(
		r.code === 0,
		`tauri build 退出码 ${r.code}${r.code ? `：${r.out.slice(-2000)}` : ""}`,
	);
	const bundle = `${ROOT}/web/src-tauri/target/release/bundle`;
	const deb = `${bundle}/deb/Oryn_${VERSION}_amd64.deb`;
	for (const f of [
		deb,
		`${bundle}/rpm/Oryn-${VERSION}-1.x86_64.rpm`,
		`${bundle}/appimage/Oryn_${VERSION}_amd64.AppImage`,
	])
		check(existsSync(f), `产物 ${f.slice(bundle.length + 1)}`);
	const x = `${TMP}/deb`;
	mkdirSync(x, { recursive: true });
	const desk = sh(
		`cd ${q(x)} && ar x ${q(deb)} && tar -xf data.tar.* && cat usr/share/applications/*.desktop`,
	);
	check(
		desk.code === 0 && /MimeType=.*x-scheme-handler\/oryn/.test(desk.stdout),
		`deb 的 .desktop 含 x-scheme-handler/oryn（${desk.stdout.split("\n").find((l) => l.startsWith("MimeType")) ?? desk.out.slice(0, 200)}）`,
	);
	const smoke = await once("smoke", ["--smoke"], { timeoutMs: 45_000 });
	check(smoke.code === 0, `--smoke 退出码 ${smoke.code}（${smoke.ms}ms）`);
}

async function D2() {
	if (MAIN) await quit(MAIN);
	rmSync(`${TMP}/app`, { recursive: true, force: true });
	const { app, deviceId, started, after } = await pairApp("app");
	MAIN = app;
	DEVICE = deviceId;
	const verifyUrl = String(started.verifyUrl);
	check(verifyUrl === `${ORIGIN}/pair`, `verifyUrl = ${verifyUrl}`);
	await waitFor(
		"假 xdg-open 收到 verifyUrl",
		() =>
			existsSync(XDG_LOG) &&
			readFileSync(XDG_LOG, "utf8")
				.split("\n")
				.some((l) => l.trim() === verifyUrl),
		10_000,
	);
	note(`xdg-open.log 含 ${verifyUrl}`);
	let cur = after;
	const seq: Ev[] = [];
	for (const name of [
		"pair.approved",
		"token.stored",
		"session.minted",
		"web.ready",
	]) {
		const e = await ev(name, { after: cur, timeoutMs: 30_000 });
		seq.push(e);
		cur = e.i + 1;
	}
	check(
		seq[1]?.store === "keyring",
		`token.stored.store = ${String(seq[1]?.store)}`,
	);
	check(
		seq[3]?.origin === ORIGIN && seq[3]?.bridge === 1,
		`web.ready origin = ${String(seq[3]?.origin)}、bridge = ${String(seq[3]?.bridge)}`,
	);
	const dev = await waitFor(
		"设备列表里有该桌面设备且 lastSeenAt 非空",
		async () => {
			const r = await api("GET", "/api/v1/devices");
			const list = (r.json as { devices: Record<string, unknown>[] }).devices;
			const d = list.find((x) => x.id === deviceId);
			return d?.kind === "desktop" && d.lastSeenAt ? d : undefined;
		},
		10_000,
	);
	note(`设备 ${JSON.stringify(dev)}`);
	const n = sql<{ c: number }>(
		`select count(*) c from web_sessions where device_id='${deviceId}'`,
	)[0]?.c;
	check(n === 1, `web_sessions where device_id = ${n} 行`);
}

async function D3() {
	await ensureMain();
	const s = secretEntries();
	check(
		s.length === 1,
		`secret-tool search 找到 ${s.length} 条 odt_ 令牌（值不打印）`,
	);
	const g = sh(`grep -rl odt_ ${q(`${TMP}/app`)} ${q(EVENTS)}`);
	check(
		g.stdout === "",
		`配置目录与事件日志里没有 odt_${g.stdout ? `：${g.stdout}` : ""}`,
	);
}

/** 不带任何 service 目录的私有会话总线：上面没有 Secret Service，也不会被 D-Bus 激活出来。 */
async function bareBus(): Promise<string> {
	const sock = `${TMP}/run/bus-bare`;
	writeFileSync(
		`${TMP}/bus-bare.conf`,
		`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=${sock}</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`,
	);
	bg("dbus-bare", [
		"dbus-daemon",
		"--nofork",
		`--config-file=${TMP}/bus-bare.conf`,
	]);
	await waitFor("私有总线就绪", () => existsSync(sock), 10_000);
	const addr = `unix:path=${sock}`;
	check(
		!(await hasBusName("org.freedesktop.secrets", {
			...ENV,
			DBUS_SESSION_BUS_ADDRESS: addr,
		})),
		"新总线上没有 org.freedesktop.secrets",
	);
	return addr;
}

async function D4() {
	// 「停掉 gnome-keyring」= 让 App 连到一条没有 Secret Service 的会话总线（不碰其它场景正在用的凭据库）
	const bus = await bareBus();
	rmSync(`${TMP}/app2`, { recursive: true, force: true });
	const { app, after } = await pairApp("app2", {
		DBUS_SESSION_BUS_ADDRESS: bus,
	});
	try {
		const st = await ev("token.stored", { after, timeoutMs: 30_000 });
		check(st.store === "file", `token.stored.store = ${String(st.store)}`);
		const cred = `${cfgDir("app2")}/credentials.json`;
		const mode = existsSync(cred)
			? (statSync(cred).mode & 0o777).toString(8)
			: "不存在";
		check(mode === "600", `${cred.slice(TMP.length + 1)} 权限 ${mode}`);
		const ts = readConfig("app2").tokenStore;
		check(ts === "file", `config.json tokenStore = ${String(ts)}`);
	} finally {
		await quit(app);
	}
}

async function D5() {
	const admin = sh(
		`${q(`${BIN}/orynd`)} --config ${q(`${TMP}/orynd.yaml`)} admin create-device --name b --kind desktop`,
	);
	check(admin.code === 0, "admin create-device --kind desktop");
	const D = admin.stdout;
	const mint = await api("POST", "/api/v1/auth/app-session", undefined, {
		Authorization: `Bearer ${D}`,
	});
	check(
		mint.status === 200,
		`desktop 令牌 POST auth/app-session → ${mint.status}`,
	);
	const code = (mint.json as { code: string }).code;
	const redeem = () =>
		fetch(
			`${BASE}/auth/app?code=${encodeURIComponent(code)}&next=${encodeURIComponent("/t/x")}`,
			{
				redirect: "manual",
			},
		);
	const r1 = await redeem();
	const cookies1 = r1.headers.getSetCookie();
	const sess = cookies1.find((c) => c.startsWith("__Host-oryn_session="));
	check(
		r1.status === 302 && r1.headers.get("location") === "/t/x",
		`兑换 → ${r1.status} Location: ${r1.headers.get("location")}`,
	);
	check(
		!!sess && /;\s*Secure/i.test(sess) && /;\s*HttpOnly/i.test(sess),
		`Set-Cookie: ${sess?.replace(/=[^;]+/, "=…") ?? "无"}`,
	);
	const cookieVal = sess?.split(";")[0] ?? "";
	const approve = await api(
		"POST",
		"/api/v1/devices/pair/approve",
		{ code: "ABCD-EFGH" },
		{ Cookie: cookieVal },
	);
	check(approve.status === 403, `App 会话批准配对 → ${approve.status}`);
	const cliMint = await api("POST", "/api/v1/auth/app-session");
	check(
		cliMint.status === 403 &&
			(cliMint.json as { code?: string })?.code === "app_session_unsupported",
		`CLI 令牌 → ${cliMint.status} ${cliMint.text}`,
	);
	const r2 = await redeem();
	const again = r2.headers
		.getSetCookie()
		.filter((c) => c.startsWith("__Host-oryn_session="));
	check(
		r2.status === 302 && again.length === 0,
		`同一个码再兑换 → ${r2.status}，Set-Cookie ${again.length} 条`,
	);
}

async function D6() {
	await ensureMain();
	const tid = await ensureThread();
	check((await getThread(tid)).unread === true, "线程未读");
	const a = mark();
	const second = await once("app", [`oryn://thread/${tid}`], {
		timeoutMs: 10_000,
	});
	check(
		second.code === 0 && second.ms <= 3000,
		`第二个进程退出码 ${second.code}，用时 ${second.ms}ms`,
	);
	const ps = pids();
	check(ps.length === 1, `pgrep -x oryn-desktop = ${ps.join(",") || "无"}`);
	await ev("instance.second", { after: a });
	await ev("deeplink", { after: a, where: (e) => e.path === `/t/${tid}` });
	await ev("open", {
		after: a,
		where: (e) => e.path === `/t/${tid}` && e.delivered === true,
	});
	await waitFor(
		"线程被页面打开后 unread = false",
		async () => (await getThread(tid)).unread === false,
		5_000,
	);
	note("unread = false");
	const mime = sh("xdg-mime query default x-scheme-handler/oryn", {
		env: MAIN?.env,
	});
	check(mime.stdout !== "", `xdg-mime 默认处理程序 = ${mime.stdout || "空"}`);

	const b = mark();
	const bad = await once("app", ["oryn://thread/../../settings"]);
	check(bad.code === 0, `非法链接的第二实例退出码 ${bad.code}`);
	await ev("deeplink", { after: b, where: (e) => e.path == null });
	await sleep(2000);
	const opens = since(b, "open");
	check(
		opens.length === 0,
		`非法链接之后没有新的 open（${opens.map(show).join(" ")}）`,
	);
}

async function D7() {
	await ensureMain();
	await ensureVisible();
	const tid = await ensureThread();
	// 托盘图标按「待处理 + 未读 > 0」切换：先让 $T 被页面打开（清未读），单跑 D7 时图标才会从 normal 起步
	if ((await getThread(tid)).unread) {
		await once("app", [`oryn://thread/${tid}`], { timeoutMs: 10_000 });
		await waitFor(
			"$T 未读被清",
			async () => ((await getThread(tid)).unread ? undefined : true),
			10_000,
		);
	}
	await waitFor(
		"托盘当前为 normal",
		() => (since(0, "tray").at(-1)?.icon === "normal" ? true : undefined),
		10_000,
	);
	const a = mark();
	sh(`xdotool windowquit ${windowId()}`);
	await ev("window", { after: a, where: (e) => e.visible === false });
	await sleep(1000);
	check(!!MAIN && alive(MAIN.proc), "关窗后进程仍在");

	const b = mark();
	const t2 = await createThread({
		prompt: "ask：请选一个选项",
		executor: { kind: "orb", orbId: orbOf(tid) },
	});
	note(`线程 ${t2}（挂在 $T 的 orb 上）`);
	await waitFor(
		`线程 ${t2} awaiting_input`,
		async () => (await getThread(t2)).state === "awaiting_input",
		120_000,
		500,
	);
	await ev("status", {
		after: b,
		where: (e) => e.awaiting === 1,
		timeoutMs: 30_000,
	});
	await ev("tray", { after: b, where: (e) => e.icon === "attention" });

	const c = mark();
	const p = await api("PATCH", `/api/v1/threads/${t2}`, { archived: true });
	check(p.status === 200, `归档 ${t2} → ${p.status}`);
	await ev("status", {
		after: c,
		where: (e) => e.awaiting === 0,
		timeoutMs: 30_000,
	});
	await ev("tray", { after: c, where: (e) => e.icon === "normal" });
}

interface DunstEntry {
	appname: string;
	summary: string;
}

function dunstHistory(): DunstEntry[] {
	const r = sh("dunstctl history");
	const j = JSON.parse(r.stdout || "{}") as {
		data?: Record<string, { data: unknown }>[][];
	};
	return (j.data?.[0] ?? []).map((n) => ({
		appname: String(n.appname?.data ?? ""),
		summary: String(n.summary?.data ?? ""),
	}));
}

async function D8() {
	await ensureMain();
	const tid = await ensureThread();
	await ensureHidden();
	sh("dunstctl close-all");
	const title = (await getThread(tid)).title;

	const a = mark();
	let before = entryCount(tid);
	check(
		cli(`thread send ${tid} ${q("数到 3")}`).code === 0,
		"oryn thread send",
	);
	await waitTurn(tid, before);
	const shown = await ev("notify.shown", {
		after: a,
		where: (e) => e.path === `/t/${tid}`,
		timeoutMs: 30_000,
	});
	const displayed = Number(sh("dunstctl count displayed").stdout);
	check(displayed >= 1, `dunst 正在显示 ${displayed} 条通知`);

	const b = mark();
	sh("dunstctl action 0");
	let clicked = await ev("notify.clicked", {
		after: b,
		timeoutMs: 5_000,
	}).catch(() => null);
	if (!clicked) {
		note("dunstctl action 0 没有触发点击，改用 D-Bus NotificationAction");
		sh(
			"gdbus call --session --dest org.freedesktop.Notifications --object-path /org/freedesktop/Notifications --method org.dunstproject.cmd0.NotificationAction 0",
		);
		clicked = await ev("notify.clicked", { after: b, timeoutMs: 10_000 });
	}
	await ev("window", { after: b, where: (e) => e.visible === true });
	await ev("open", {
		after: b,
		where: (e) => e.path === `/t/${tid}` && e.delivered === true,
	});
	sh("dunstctl close-all");
	const hit = dunstHistory().find(
		(n) => n.appname === "Oryn" && n.summary === String(shown.title),
	);
	check(
		!!hit && (!title || hit.summary.includes(title)),
		`dunst 历史：${hit ? JSON.stringify(hit) : JSON.stringify(dunstHistory().slice(0, 3))}（线程标题 ${JSON.stringify(title)}）`,
	);

	// 窗口可见且停在该线程页：不应再有系统通知，服务端也不生成通知
	await sleep(2000);
	const n0 = await notificationCount();
	const c = mark();
	before = entryCount(tid);
	check(cli(`thread send ${tid} ${q("数到 3")}`).code === 0, "再发一次");
	await waitTurn(tid, before);
	await sleep(10_000);
	const again = since(c, "notify.shown");
	check(
		again.length === 0,
		`可见时没有新的 notify.shown（${again.map(show).join(" ")}）`,
	);
	const n1 = await notificationCount();
	check(n1 === n0, `GET notifications 条数 ${n0} → ${n1}`);
}

async function D9() {
	await ensureMain();
	await ensureVisible();
	const w = windowId();
	// 有窗口管理器时要经 _NET_ACTIVE_WINDOW 激活（openbox 会忽略直接设焦点）；没有 WM 时退回 windowfocus
	sh(
		`xdotool windowactivate --sync ${w} 2>/dev/null || xdotool windowraise ${w} windowfocus ${w}`,
	);
	await sleep(500);
	const a = mark();
	sh("xdotool key ctrl+alt+space");
	await ev("shortcut.fired", { after: a });
	await ev("window", { after: a, where: (e) => e.visible === false });
	const b = mark();
	sh("xdotool key ctrl+alt+space");
	await ev("shortcut.fired", { after: b });
	await ev("window", { after: b, where: (e) => e.visible === true });

	const code = await quit(MAIN);
	note(`--quit 后退出码 ${code}`);
	const cfg = readConfig("app");
	writeConfig("app", { ...cfg, shortcut: "Control+Alt+K" });
	MAIN = launch("app");
	const s = await ev("shortcut", {
		after: MAIN.start,
		where: (e) => e.accelerator === "Control+Alt+K",
	});
	check(
		s.ok === true,
		`shortcut.ok = ${String(s.ok)}${s.error ? `（${String(s.error)}）` : ""}`,
	);
	await ev("web.ready", { after: MAIN.start, timeoutMs: 30_000 });
}

async function D10() {
	await ensureMain();
	await ensureVisible();
	const w = windowId();
	sh(`xdotool windowsize ${w} 1000 700`);
	sh(`xdotool windowmove ${w} 120 80`);
	await sleep(1500);
	note(
		`xdotool：${sh(`xdotool getwindowgeometry ${w}`).stdout.replaceAll("\n", " ")}`,
	);
	const code = await quit(MAIN);
	check(code === 0, `--quit 后主进程退出码 ${code}`);
	const ws = `${cfgDir("app")}/.window-state.json`;
	check(existsSync(ws), `${ws.slice(TMP.length + 1)} 存在`);
	MAIN = launch("app");
	const r = await ev("window.restored", {
		after: MAIN.start,
		timeoutMs: 30_000,
	});
	const near = (k: string, v: number) => Math.abs(Number(r[k]) - v) <= 2;
	check(
		near("width", 1000) &&
			near("height", 700) &&
			near("x", 120) &&
			near("y", 80),
		`恢复为 ${String(r.width)}×${String(r.height)} @ ${String(r.x)},${String(r.y)}`,
	);
	await ev("web.ready", { after: MAIN.start, timeoutMs: 30_000 });
}

/** D11：用一次性密钥签名构建一个版本的 AppImage，返回其路径与签名。 */
function buildSigned(
	dir: string,
	version: string,
	key: string,
	pub: string,
	pw: string,
) {
	const target = process.env.M6_D11_TARGET ?? `${ROOT}/.dev/m6-d11-target`;
	const conf = `${dir}/tauri.${version}.json`;
	writeFileSync(
		conf,
		JSON.stringify({
			version,
			bundle: { createUpdaterArtifacts: true },
			plugins: {
				updater: {
					pubkey: pub,
					endpoints: [`${LOCAL_ORIGIN}/updates/latest.json`],
				},
			},
		}),
	);
	const r = sh(`bun run tauri build --bundles appimage --config ${q(conf)}`, {
		cwd: `${ROOT}/web`,
		env: {
			...ENV,
			CARGO_TARGET_DIR: target,
			TAURI_SIGNING_PRIVATE_KEY: key,
			TAURI_SIGNING_PRIVATE_KEY_PASSWORD: pw,
			...(process.env.M6_NO_STRIP === "1" ? { NO_STRIP: "true" } : {}),
		},
		timeoutMs: 60 * 60_000,
	});
	if (r.code !== 0)
		throw new Error(`构建 ${version} 失败：${r.out.slice(-2000)}`);
	const name = `Oryn_${version}_amd64.AppImage`;
	const built = `${target}/release/bundle/appimage/${name}`;
	const out = `${dir}/${name}`;
	copyFileSync(built, out);
	chmodSync(out, 0o755);
	const sig = readFileSync(`${built}.sig`, "utf8");
	note(`构建 ${name}`);
	return { name, path: out, sig };
}

async function D11() {
	// 需要一份已配对的配置；随后退出主 App：同一总线上的单实例会把 0.0.1 当成第二实例
	await quit(await ensureMain());
	MAIN = undefined;
	const dir = `${TMP}/upd`;
	mkdirSync(dir, { recursive: true });
	const pw = `m6-${RUN}`;
	const gen = sh(
		`bun run tauri signer generate --ci -p ${q(pw)} -w ${q(`${dir}/updater.key`)}`,
		{ cwd: `${ROOT}/web` },
	);
	check(gen.code === 0, `一次性更新密钥${gen.code ? `：${gen.out}` : ""}`);
	const key = readFileSync(`${dir}/updater.key`, "utf8").trim();
	const pub = readFileSync(`${dir}/updater.key.pub`, "utf8").trim();
	const v1 = buildSigned(dir, "0.0.1", key, pub, pw);
	const v2 = buildSigned(dir, "0.0.2", key, pub, pw);

	const publish = (tamper: boolean) => {
		const bytes = readFileSync(v2.path);
		writeFileSync(`${TMP}/updates/${v2.name}`, bytes);
		if (tamper) appendFileSync(`${TMP}/updates/${v2.name}`, "\0");
		const m = buildManifest(
			[{ name: v2.name, url: `${LOCAL_ORIGIN}/updates/${v2.name}` }],
			{ [`${v2.name}.sig`]: v2.sig },
			"0.0.2",
			new Date().toISOString(),
		);
		writeFileSync(`${TMP}/updates/latest.json`, JSON.stringify(m, null, 2));
	};
	// 已配对的配置（同一凭据库条目）+ 下载后自动安装
	const cfg = existsSync(`${cfgDir("app")}/config.json`)
		? readConfig("app")
		: {};
	if (!cfg.serverUrl) throw new Error("没有已配对的主 App 配置（先跑 D2）");
	const run = `${dir}/run/Oryn.AppImage`;
	mkdirSync(`${dir}/run`, { recursive: true });
	const extra = { APPIMAGE_EXTRACT_AND_RUN: "1" };
	const quitAll = async () => {
		await once("app3", ["--quit"], { bin: run, extra, timeoutMs: 20_000 });
		await waitFor(
			"所有 oryn-desktop 进程退出",
			() => pids().length === 0,
			30_000,
		);
	};

	// 第一遍：签名正确 → 下载、安装、自行重启为 0.0.2
	publish(false);
	copyFileSync(v1.path, run);
	chmodSync(run, 0o755);
	rmSync(`${TMP}/app3`, { recursive: true, force: true });
	writeConfig("app3", { ...cfg, autoUpdate: true });
	const a = mark();
	launch("app3", [], { bin: run, extra });
	try {
		await ev("app.start", {
			after: a,
			where: (e) => e.version === "0.0.1",
			timeoutMs: 30_000,
		});
		const ready = await ev("update", {
			after: a,
			where: (e) => e.state === "ready" && e.version === "0.0.2",
			timeoutMs: 120_000,
		});
		await ev("app.start", {
			after: ready.i + 1,
			where: (e) => e.version === "0.0.2",
			timeoutMs: 60_000,
		});
	} finally {
		await quitAll();
	}

	// 第二遍：更新包末尾多一个字节 → 签名校验失败，不安装
	publish(true);
	copyFileSync(v1.path, run);
	chmodSync(run, 0o755);
	const b = mark();
	launch("app3", [], { bin: run, extra });
	try {
		const err = await ev("update", {
			after: b,
			where: (e) => e.state === "error",
			timeoutMs: 120_000,
		});
		check(
			/signature|签名|verif|minisign/i.test(String(err.error)),
			`update.error = ${String(err.error)}`,
		);
		await sleep(5000);
		const starts = since(b, "app.start").map((e) => String(e.version));
		check(
			starts.length > 0 && starts.every((v) => v === "0.0.1"),
			`app.start 版本 ${starts.join(",")}`,
		);
	} finally {
		await quitAll();
	}
}

async function D12() {
	await ensureMain();
	const tid = await ensureThread();
	const a = mark();
	await once("app", [`oryn://thread/${tid}`]);
	await ev("open", {
		after: a,
		where: (e) => e.path === `/t/${tid}` && e.delivered === true,
	});
	await ensureVisible();
	await sleep(2000);

	if (ORIGIN !== LOCAL_ORIGIN)
		note(`!! ORYN_URL=${ORIGIN}：停 tlsproxy 不会切断 App 的连接`);
	await stop(TLS);
	note("tlsproxy 已停");
	await sleep(10_000);
	check(!!MAIN && alive(MAIN.proc), "停机期间 App 进程仍在");
	await startTls();
	note("tlsproxy 已恢复");
	await sleep(30_000);
	const b = mark();
	const before = entryCount(tid);
	check(
		cli(`thread send ${tid} ${q("数到 3")}`).code === 0,
		"oryn thread send",
	);
	await waitTurn(tid, before);
	await sleep(8000);
	const t = await getThread(tid);
	check(
		t.unread === false,
		`这一轮结束后 unread = ${t.unread}（WebSocket 已重连并重发 focus）`,
	);
	const shown = since(b, "notify.shown");
	check(
		shown.length === 0,
		`没有新的 notify.shown（${shown.map(show).join(" ")}）`,
	);

	const c = mark();
	const del = await api("DELETE", `/api/v1/devices/${DEVICE}`);
	check(del.status === 204, `吊销 ${DEVICE} → ${del.status}`);
	await ev("reauth", { after: c, timeoutMs: 30_000 });
	await ev("disconnect", {
		after: c,
		where: (e) => e.reason === "revoked",
		timeoutMs: 30_000,
	});
	const s = secretEntries();
	check(s.length === 0, `secret-tool search 剩 ${s.length} 条`);
	const n = sql<{ c: number }>(
		`select count(*) c from web_sessions where device_id='${DEVICE}'`,
	)[0]?.c;
	check(n === 0, `web_sessions where device_id = ${n} 行`);
	DEVICE = "";
	await quit(MAIN);
	MAIN = undefined;
}

async function D13() {
	const dirs = [
		`${ROOT}/web/dist/assets`,
		`${ROOT}/internal/webui/dist/assets`,
	].filter(existsSync);
	check(dirs.length > 0, "有 Web 构建产物（先 make web）");
	for (const d of dirs) {
		const r = sh(`rg -l 'addon-webgl|WebglAddon|webglcontextlost' ${q(d)}`);
		check(
			r.stdout === "",
			`${d.slice(ROOT.length + 1)} 无 WebGL 渲染器代码${r.stdout ? `：${r.stdout}` : ""}`,
		);
	}
	const t = sh(
		"bunx vitest run src/components/terminal/xterm-renderer.test.ts",
		{
			cwd: `${ROOT}/web`,
		},
	);
	check(
		t.code === 0,
		`xterm-renderer.test.ts${t.code ? `：${t.out.slice(-1000)}` : " 通过"}`,
	);
}

const TAG = process.env.M6_TAG ?? `desktop-v${VERSION}-rc.1`;

interface GhJob {
	databaseId: number;
	name: string;
	conclusion: string;
	steps: { name: string; conclusion: string }[];
}

function releaseRun(): { id: number; jobs: GhJob[] } {
	const list = sh(
		`gh run list --workflow desktop.yml --event push --branch ${q(TAG)} --limit 5 --json databaseId,status,conclusion,headBranch`,
	);
	if (list.code !== 0) throw new Error(`gh run list 失败：${list.out}`);
	const runs = JSON.parse(list.stdout) as {
		databaseId: number;
		status: string;
		conclusion: string;
	}[];
	const run = runs[0];
	if (!run)
		throw new Error(
			`没有 ${TAG} 触发的 desktop 工作流（先推送该 tag：git push origin ${TAG}）`,
		);
	check(
		run.status === "completed" && run.conclusion === "success",
		`工作流 #${run.databaseId} ${run.status}/${run.conclusion}`,
	);
	const view = sh(`gh run view ${run.databaseId} --json jobs`);
	return {
		id: run.databaseId,
		jobs: (JSON.parse(view.stdout) as { jobs: GhJob[] }).jobs,
	};
}

async function D14() {
	const { jobs } = releaseRun();
	for (const name of [
		"build (linux)",
		"build (windows)",
		"build (macos)",
		"publish",
	]) {
		const j = jobs.find((x) => x.name === name);
		check(
			j?.conclusion === "success",
			`job ${name}：${j?.conclusion ?? "不存在"}`,
		);
		for (const s of j?.steps.filter((x) => x.name.startsWith("冒烟启动")) ?? [])
			if (s.conclusion !== "skipped")
				check(
					s.conclusion === "success",
					`${name} / ${s.name}：${s.conclusion}`,
				);
	}
	const rel = sh(
		`gh release view ${q(TAG)} --json assets,isPrerelease,isDraft`,
	);
	check(rel.code === 0, `gh release view ${TAG}`);
	const info = JSON.parse(rel.stdout) as {
		assets: { name: string }[];
		isPrerelease: boolean;
		isDraft: boolean;
	};
	check(!info.isDraft, "Release 已发布（非草稿）");
	const names = info.assets.map((x) => x.name);
	note(`资产：${names.join(" ")}`);
	for (const suf of [
		".AppImage",
		".deb",
		".rpm",
		"-setup.exe",
		".msi",
		".dmg",
		".app.tar.gz",
	])
		check(
			names.some((n) => n.endsWith(suf)),
			`有 *${suf}`,
		);
	const sigs = names.filter((n) => n.endsWith(".sig"));
	if (sigs.length === 0)
		note("没有 .sig：本次构建未配置更新私钥，不检查 latest.json");
	else {
		for (const suf of [".AppImage.sig", "-setup.exe.sig", ".app.tar.gz.sig"])
			check(
				sigs.some((n) => n.endsWith(suf)),
				`有 *${suf}`,
			);
		const m = sh(`gh release download ${q(TAG)} -p latest.json -O -`);
		check(m.code === 0, "有 latest.json");
		const keys = Object.keys(
			(JSON.parse(m.stdout) as { platforms: object }).platforms,
		).sort();
		check(
			keys.join(",") ===
				"darwin-aarch64,darwin-x86_64,linux-x86_64,windows-x86_64",
			`latest.json 平台键 ${keys.join(",")}`,
		);
	}
	if (info.isPrerelease) {
		const ch = sh("gh release download desktop-updater -p latest.json -O -");
		const v =
			ch.code === 0
				? (JSON.parse(ch.stdout) as { version: string }).version
				: "（无更新通道）";
		const tagVer = TAG.replace(/^desktop-v/, "");
		check(v !== tagVer, `预发布没有改动 desktop-updater（通道版本 ${v}）`);
	}
}

async function D15() {
	const { jobs } = releaseRun();
	const mac = jobs.find((x) => x.name === "build (macos)");
	check(
		mac?.conclusion === "success",
		`macOS job：${mac?.conclusion ?? "不存在"}`,
	);
	const log = sh(`gh run view --job ${mac?.databaseId} --log`, {
		timeoutMs: 120_000,
	}).stdout;
	const line = log.split("\n").find((l) => l.includes("代码签名")) ?? "";
	note(`签名配置：${line.replace(/^.*?代码签名/, "代码签名")}`);
	if (line.includes("已签名并公证")) {
		check(
			log.includes("The validate action worked!"),
			"stapler validate：The validate action worked!",
		);
		check(/accepted/.test(log), "spctl：accepted");
		check(
			log.includes("source=Notarized Developer ID"),
			"spctl：source=Notarized Developer ID",
		);
	} else if (line.includes("未签名（ad-hoc）")) {
		note("没有 Apple secrets：ad-hoc 签名，job 成功即通过");
	} else {
		check(false, "签名状态既不是「已签名并公证」也不是「未签名（ad-hoc）」");
	}
}

const TESTS: [string, () => Promise<void>][] = [
	["D1", D1],
	["D2", D2],
	["D3", D3],
	["D4", D4],
	["D5", D5],
	["D6", D6],
	["D7", D7],
	["D8", D8],
	["D9", D9],
	["D10", D10],
	["D11", D11],
	["D12", D12],
	["D13", D13],
	["D14", D14],
	["D15", D15],
];

// ---------------------------------------------------------------- 主流程

try {
	await setup();
	console.log(
		`M6 验收 run=${RUN} tmp=${TMP} origin=${ORIGIN} app=${APP} version=${VERSION}`,
	);
	for (const [id, fn] of TESTS) {
		if (ONLY && !ONLY.has(id)) continue;
		if (
			id !== "D1" &&
			Number(id.slice(1)) <= 12 &&
			!existsSync(APP) &&
			id !== "D5"
		) {
			console.log(
				`── ${id}\n    · 错误：缺少 ${APP}（先构建，或跑 D1）\n${id} FAIL`,
			);
			results.push({ id, verdict: "FAIL", evidence: [`缺少 ${APP}`] });
			continue;
		}
		evidence = [];
		console.log(`── ${id}`);
		const t0 = Date.now();
		try {
			await fn();
			console.log(`${id} PASS (${((Date.now() - t0) / 1000).toFixed(1)}s)`);
			results.push({ id, verdict: "PASS", evidence });
		} catch (e) {
			note(`错误：${e instanceof Error ? e.message : String(e)}`);
			screenshot(`${id}-FAIL`);
			console.log(`${id} FAIL (${((Date.now() - t0) / 1000).toFixed(1)}s)`);
			results.push({ id, verdict: "FAIL", evidence });
		}
	}
} catch (e) {
	console.error(`环境准备失败：${e instanceof Error ? e.message : String(e)}`);
	results.push({ id: "setup", verdict: "FAIL", evidence: [String(e)] });
} finally {
	await cleanup();
}

console.log("\n汇总");
for (const r of results) console.log(`${r.id} ${r.verdict}`);
console.log(`结果与日志：${OUT}`);
process.exit(
	results.length === 0 || results.some((r) => r.verdict === "FAIL") ? 1 : 0,
);
