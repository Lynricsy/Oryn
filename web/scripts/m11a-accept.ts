#!/usr/bin/env bun
/**
 * M11a 真机验收（验证分册「M11a 真机」X2–X22；X1 自动化另跑）：对 dev 部署（orynd + docker provider）逐项执行，
 * 每项打印 `X<n> PASS|FAIL|SKIP (秒)` 与证据行，失败时给该场景最近操作的线程页截图并留一份 orynd.log 末尾。
 *
 * 用法（web/ 目录）：bun run accept:m11a [-- --only X2,X3]
 * 环境变量：ORYN_URL、ORYN_ROOT、ORYN_TOKEN（同 m10）；M11_REPO（沙盒仓库，缺省 Lynricsy/oryn-setup-sandbox）；
 *   M11_X12_THREAD（X12 用旧镜像建的线程，缺省读 .dev/m11-x12.thread）；M11_PROFILE（X13 / X19 用的浏览器 Profile id 或名字，
 *   缺省取第一个保存过的 Profile 以 readonly 选用，没有就新建一个以 exclusive 选用）；M11A_FRESH=1（丢弃 .dev/m11a.json，
 *   换新的 RUN / 项目 / 分支）；M11A_NO_RESTART=1（跳过 X9 的 orynd 重启）；M11A_KEEP_POOL=1（结尾不把预热数改回 0）；
 *   CHROMIUM；M11A_SHOTS（默认 /tmp/oryn-shots/m11a-accept）。
 * CLI 一律 `./bin/oryn`，`ORYN_CREDENTIALS=$ROOT/.dev/credentials.json`。
 *
 * 固定布置：
 *   - RUN 与各场景之间要接力的数据（T1 耗时、T2）存在 .dev/m11a.json，`--only` 单跑时缺什么现建什么。
 *   - 项目 P = `m11a-<RUN>`：仓库 = 沙盒，基线分支 = 脚本从 pristine 拉出并 push 的 `m11a-<RUN>`；setup 脚本按分册写死
 *     （`sleep 15; echo "run $(git rev-parse --short HEAD)" >> ~/setup-runs.txt`）。推进基线 = 在宿主临时检出
 *     /var/tmp/m11a-<RUN>/repo 里提交（空提交）并 push 到该分支。
 *   - 项目 Q = `m11a-<RUN>-q`（X9 / X19）：无仓库，setup 写 1200MiB 的压舱文件，保证它的快照单独就超过 1GB 配额。
 *   - 项目 D = `m11a-<RUN>-dind`（X22）：无仓库，setup = `docker pull alpine:3`，默认类型 `dind`。
 *   - X9 临时把 dev/orynd.yaml 的 `snapshots.max_total_gb` 改成 1 并重启 orynd；原文件先备份到 .dev/orynd.yaml.m11a-bak，
 *     场景结束（含失败）在 finally 里写回原文并再重启一次；脚本启动时发现备份残留（上次中断）也先还原并重启。
 *   - X21 / X22 需要 provider 声明 `dind` 类型（父 agent 集成时写进 dev/provider.yaml）；缺少时 SKIP。
 *   - 结尾：P / Q / D 的预热数改回 0（M11A_KEEP_POOL=1 时保留），删除本进程写入的 env 密钥 M11_SECRET。
 *
 * 契约里拿不到、改读 SQLite（.dev/data/oryn.db）的观察点：orb 事件序列（orb_events）、快照的 source_orb_id、
 * 池 orb 的 config_hash 与「曾是池 orb」（requested 事件 detail.pool）、jobs 表里的 snapshot 任务、线程首次离开
 * provisioning 的时刻（threads.state 轮询，created_at 取库里的值）。
 */
import { randomBytes } from "node:crypto";
import {
	existsSync,
	mkdirSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { homedir } from "node:os";
import { dirname } from "node:path";
import { type Browser, chromium } from "playwright-core";

const ROOT = process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`;
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const CREDS = `${ROOT}/.dev/credentials.json`;
const TOKEN =
	process.env.ORYN_TOKEN ??
	(JSON.parse(readFileSync(CREDS, "utf8")) as { token: string }).token;
const SHOTS = process.env.M11A_SHOTS ?? "/tmp/oryn-shots/m11a-accept";
const DB = `${ROOT}/.dev/data/oryn.db`;
const ORYN = `${ROOT}/bin/oryn`;
const STATE_FILE = `${ROOT}/.dev/m11a.json`;
const ORYND_YAML = `${ROOT}/dev/orynd.yaml`;
const ORYND_BAK = `${ROOT}/.dev/orynd.yaml.m11a-bak`;
const RESTART = `${ROOT}/.dev/e2e/restart.sh --no-build`;
const SANDBOX_REPO =
	process.env.M11_REPO ?? "https://github.com/Lynricsy/oryn-setup-sandbox.git";
const DIND = "dind";
const WORKDIR = "/home/user/workspace/repo";
const SETUP_P = `sleep 15; echo "run $(git rev-parse --short HEAD)" >> ~/setup-runs.txt`;
const SETUP_Q = `dd if=/dev/zero of="$HOME/.m11a-ballast" bs=1M count=1200 status=none; sleep 2; echo q >> ~/setup-runs.txt`;
const SETUP_D = "docker pull alpine:3";
const SECRET_NAME = "M11_SECRET";
const GB = 1e9;
mkdirSync(SHOTS, { recursive: true });

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

interface State {
	run: string;
	t1?: string;
	t1Ms?: number;
	t2?: string;
}
const state: State =
	process.env.M11A_FRESH !== "1" && existsSync(STATE_FILE)
		? (JSON.parse(readFileSync(STATE_FILE, "utf8")) as State)
		: { run: Math.random().toString(36).slice(2, 6) };
const save = () => writeFileSync(STATE_FILE, JSON.stringify(state, null, 2));
save();
const RUN = state.run;
const BRANCH = `m11a-${RUN}`;
const P_NAME = `m11a-${RUN}`;
const Q_NAME = `m11a-${RUN}-q`;
const D_NAME = `m11a-${RUN}-dind`;
const CHECKOUT = `/var/tmp/m11a-${RUN}/repo`;

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const te = new TextEncoder();
const sec = (ms: number) => `${(ms / 1000).toFixed(1)}s`;
const mb = (b?: number) =>
	b === undefined ? "未上报" : `${(b / 1048576).toFixed(1)}MiB`;

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

function must(r: { code: number; out: string }, what: string): string {
	if (r.code !== 0) throw new Error(`${what}失败（${r.code}）：${r.out}`);
	return r.out;
}

const CLI_ENV = `ORYN_CREDENTIALS=${q(CREDS)}`;

async function cli(args: string) {
	const p = Bun.spawn(["bash", "-c", `${CLI_ENV} ${q(ORYN)} ${args}`], {
		cwd: ROOT,
		stdout: "pipe",
		stderr: "pipe",
	});
	const [stdout, stderr, code] = await Promise.all([
		new Response(p.stdout).text(),
		new Response(p.stderr).text(),
		p.exited,
	]);
	return { code: code as number, out: `${stdout}${stderr}`.trim(), stdout };
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

const dexec = (orb: string, cmd: string) =>
	sh(`docker exec ${orb} bash -lc ${q(cmd)}`);
const imageExists = (ref: string) =>
	sh(`docker image inspect --format '{{.Id}}' ${q(ref)}`).code === 0;

interface ApiRes {
	status: number;
	json: unknown;
	text: string;
}

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
		// 空体（204）
	}
	return { status: res.status, json, text };
}

async function getJson<T>(path: string): Promise<T> {
	const r = await api("GET", path);
	if (r.status !== 200) throw new Error(`GET ${path} → ${r.status} ${r.text}`);
	return r.json as T;
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

type Verdict = "PASS" | "FAIL" | "SKIP";
class Skip extends Error {}
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

/** 当前场景最近操作的线程：失败时截它的线程页。 */
let focus = "";
let browser: Browser | undefined;

async function failArtifacts(id: string) {
	const log = `${SHOTS}/${id}-FAIL-${RUN}.orynd.log`;
	const tail = sh(`tail -n 200 ${q(`${ROOT}/.dev/orynd.log`)}`);
	writeFileSync(log, tail.stdout);
	note(`orynd.log 末尾 ${log}`);
	if (!focus) return;
	try {
		browser ??= await chromium.launch({
			executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
			headless: true,
			args: ["--no-sandbox"],
		});
		const ctx = await browser.newContext({
			locale: "zh-CN",
			viewport: { width: 1280, height: 900 },
			extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
		});
		const page = await ctx.newPage();
		await page
			.goto(`${BASE}/t/${focus}`, { waitUntil: "networkidle", timeout: 30_000 })
			.catch(() => {});
		await sleep(1500);
		const p = `${SHOTS}/${id}-FAIL-${RUN}.png`;
		await page.screenshot({ path: p, fullPage: true });
		note(`截图 ${p}`);
		await ctx.close();
	} catch (e) {
		note(`截图失败：${e instanceof Error ? e.message : String(e)}`);
	}
}

// ---------------------------------------------------------------- 契约模型（只取用到的字段）

interface SetupRun {
	id: string;
	phase: "setup" | "resume";
	trigger: string;
	ok: boolean;
	setupRan: boolean;
	durationMs: number;
	prepared?: boolean;
	baseCommit?: string;
	error?: string;
}
interface OrbView {
	id: string;
	providerId: string;
	type: string;
	image: string;
	state: string;
	projectId?: string;
	snapshotOf?: string;
	snapshotKind?: "project" | "fork";
	pool?: "warming" | "idle" | "claimed";
	nestedDocker?: boolean;
	threadIds: string[];
	lastSetup?: SetupRun;
	lastResume?: SetupRun;
	desktop?: { displayReady: boolean; profile?: { id: string } };
	orbdVersion?: string;
	failureReason?: string;
	createdAt: number;
}
interface Snap {
	id: string;
	providerId: string;
	type: string;
	state: "building" | "ready" | "retired";
	configHash: string;
	baseCommit?: string;
	depth: number;
	lineageAt: number;
	createdAt: number;
	readyAt?: number;
	useCount: number;
	sizeBytes?: number;
	retireReason?: string;
}
interface Project {
	id: string;
	name: string;
	setupScript?: string;
	warmOrbs: number;
	snapshots: boolean;
}
interface Provider {
	id: string;
	name: string;
	online: boolean;
	orbTypes: { name: string; features?: string[] }[];
}
interface Profile {
	id: string;
	name: string;
	revision: number;
}

const fmtRun = (r?: SetupRun) =>
	r
		? `ok=${r.ok} setupRan=${r.setupRan} prepared=${r.prepared} baseCommit=${r.baseCommit?.slice(0, 7) ?? "-"} ${sec(r.durationMs)}`
		: "无";
const fmtSnap = (s: Snap) =>
	`${s.id} ${s.type} ${s.state}${s.retireReason ? `/${s.retireReason}` : ""} depth=${s.depth} commit=${s.baseCommit?.slice(0, 7) ?? "-"} size=${mb(s.sizeBytes)} uses=${s.useCount}`;

const orbView = (id: string) => getJson<OrbView>(`/api/v1/orbs/${id}`);
const poolOrbs = async (pid?: string) =>
	(await getJson<{ orbs: OrbView[] }>("/api/v1/orbs")).orbs.filter(
		(o) =>
			(o.pool === "warming" || o.pool === "idle") &&
			(!pid || o.projectId === pid),
	);
const snaps = async (pid: string) =>
	(await getJson<{ snapshots: Snap[] }>(`/api/v1/projects/${pid}/snapshots`))
		.snapshots;
const readyOf = async (pid: string, type?: string) =>
	(await snaps(pid)).find(
		(s) => s.state === "ready" && (!type || s.type === type),
	);
const snapById = async (pid: string, id: string) =>
	(await snaps(pid)).find((s) => s.id === id);

async function clearSnapshots(pid: string) {
	const r = await api("DELETE", `/api/v1/projects/${pid}/snapshots`);
	if (r.status !== 204)
		throw new Error(`DELETE snapshots → ${r.status} ${r.text}`);
}

interface EventRow {
	ts: number;
	from_state: string;
	to_state: string;
	detail: string;
}
const events = (orb: string, since = 0) =>
	sql<EventRow>(
		`select ts, from_state, to_state, detail from orb_events where orb_id='${orb}' and ts>=${since} order by id`,
	);
const transitions = (ev: EventRow[]) =>
	ev.filter((e) => e.from_state !== e.to_state);
/** 状态迁移里是否有一段连续的 path[0]→path[1]→…（同状态事件不计）。 */
function hasPath(ev: EventRow[], path: string[]): boolean {
	const tr = transitions(ev);
	for (let i = 0; i + path.length - 1 <= tr.length; i++) {
		let ok = true;
		for (let k = 0; k < path.length - 1; k++) {
			const e = tr[i + k];
			if (!e || e.from_state !== path[k] || e.to_state !== path[k + 1]) {
				ok = false;
				break;
			}
		}
		if (ok) return true;
	}
	return false;
}
const pathStr = (ev: EventRow[]) =>
	transitions(ev)
		.slice(-12)
		.map((e) => `${e.from_state}→${e.to_state}`)
		.join(" ");
const orbDbState = (orb: string) =>
	one<{ state: string }>(`select state from orbs where id='${orb}'`)?.state ??
	"?";
const wasPoolOrb = (orb: string) =>
	(one<{ n: number }>(
		`select count(*) n from orb_events where orb_id='${orb}' and to_state='requested' and json_extract(detail,'$.pool')=1`,
	)?.n ?? 0) > 0;

// ---------------------------------------------------------------- 线程

interface Entry {
	seq: number;
	type: string;
	payload: Record<string, unknown>;
}

async function entriesOf(tid: string): Promise<Entry[]> {
	const out: Entry[] = [];
	let after = 0;
	for (let i = 0; i < 200; i++) {
		const p = await getJson<{ entries: Entry[]; hasMore: boolean }>(
			`/api/v1/threads/${tid}/entries?after=${after}&limit=500`,
		);
		out.push(...p.entries);
		const last = p.entries.at(-1);
		if (!p.hasMore || !last) break;
		after = last.seq;
	}
	return out;
}

function textOf(c: unknown): string {
	if (typeof c === "string") return c;
	if (!Array.isArray(c)) return "";
	return c
		.map((x) =>
			x &&
			typeof x === "object" &&
			"type" in x &&
			x.type === "text" &&
			"text" in x
				? String(x.text ?? "")
				: "",
		)
		.join("");
}

/** 最后一条带文本的 assistant 消息。 */
async function lastAssistant(tid: string): Promise<string> {
	const es = await entriesOf(tid);
	for (let i = es.length - 1; i >= 0; i--) {
		const e = es[i];
		if (e?.type !== "message") continue;
		const m = e.payload.message as
			| { role?: string; content?: unknown }
			| undefined;
		if (m?.role !== "assistant") continue;
		const t = textOf(m.content).trim();
		if (t) return t;
	}
	return "";
}

interface ThreadRow {
	state: string;
	last_seq: number;
	orb_id: string | null;
	created_at: number;
}
const threadRow = (tid: string) =>
	one<ThreadRow>(
		`select state, last_seq, orb_id, created_at from threads where id='${tid}'`,
	);
function orbOf(tid: string): string {
	const o = threadRow(tid)?.orb_id;
	if (!o) throw new Error(`线程 ${tid} 没有 orb`);
	return o;
}

const SETTLED: Record<string, true> = {
	idle: true,
	awaiting_input: true,
	error: true,
};

/** 等 beforeSeq 之后出现助手消息且线程静止，返回状态。 */
async function waitTurn(tid: string, beforeSeq: number, timeoutMs = 1_200_000) {
	return waitFor(
		`线程 ${tid} 回合结束`,
		() => {
			const r = threadRow(tid);
			if (!r || !SETTLED[r.state] || r.last_seq <= beforeSeq) return undefined;
			const n = one<{ n: number }>(
				`select count(*) n from entries where thread_id='${tid}' and seq>${beforeSeq} and type='message' and instr(cast(payload as text), '"role":"assistant"')>0`,
			)?.n;
			return n ? r.state : undefined;
		},
		timeoutMs,
		1000,
	);
}

async function waitIdle(tid: string, beforeSeq = 0) {
	const st = await waitTurn(tid, beforeSeq);
	check(st === "idle", `${tid} 到 idle（${st}）`);
}

interface ThreadOpts {
	projectId?: string;
	prompt?: string;
	type?: string;
	orbId?: string;
	browserProfileId?: string;
	profileMode?: string;
	tag: string;
}

function threadBody(o: ThreadOpts) {
	return {
		...(o.projectId ? { projectId: o.projectId } : {}),
		prompt: o.prompt ?? "只回复 ok",
		title: `m11a ${RUN} ${o.tag}`,
		executor: o.orbId
			? { kind: "orb", orbId: o.orbId }
			: { kind: "new_orb", ...(o.type ? { type: o.type } : {}) },
		...(o.browserProfileId
			? { browserProfileId: o.browserProfileId, profileMode: o.profileMode }
			: {}),
	};
}

interface Started {
	id: string;
	orbId?: string;
	createdAt: number;
}

async function startThread(o: ThreadOpts): Promise<Started> {
	const r = await api("POST", "/api/v1/threads", threadBody(o));
	if (r.status !== 201)
		throw new Error(`建线程（${o.tag}）失败 ${r.status} ${r.text}`);
	const t = (r.json as { thread: { id: string; orbId?: string } }).thread;
	focus = t.id;
	return {
		id: t.id,
		orbId: t.orbId,
		createdAt: threadRow(t.id)?.created_at ?? Date.now(),
	};
}

/** 「耗时」= 线程从创建到首次离开 provisioning（毫秒）。 */
async function leaveProvisioning(tid: string, timeoutMs = 1_200_000) {
	return waitFor(
		`线程 ${tid} 离开 provisioning`,
		() => {
			const r = threadRow(tid);
			return r && r.state !== "provisioning"
				? Math.max(1, Date.now() - r.created_at)
				: undefined;
		},
		timeoutMs,
		200,
	);
}

/** 建线程 → 记耗时 → 等到 idle。 */
async function newIdle(o: ThreadOpts) {
	const s = await startThread(o);
	const ms = await leaveProvisioning(s.id);
	await waitIdle(s.id);
	const orb = orbOf(s.id);
	note(`${o.tag} = ${s.id} orb=${orb} 耗时 ${sec(ms)}`);
	return { id: s.id, orb, ms };
}

async function ensureAwake(tid: string): Promise<string> {
	const o = orbOf(tid);
	if (orbDbState(o) !== "ready")
		await api("POST", `/api/v1/threads/${tid}/wake`);
	return waitFor(
		`线程 ${tid} 的 orb ready`,
		() => (orbDbState(o) === "ready" ? o : undefined),
		300_000,
		1000,
	);
}

async function waitOrbDeleted(orb: string, timeoutMs = 180_000) {
	const t0 = Date.now();
	await waitFor(
		`${orb} deleted`,
		() => orbDbState(orb) === "deleted",
		timeoutMs,
		1000,
	);
	return Date.now() - t0;
}

const setupLines = (orb: string) =>
	Number(
		dexec(orb, "wc -l < ~/setup-runs.txt 2>/dev/null || echo 0").stdout || 0,
	);

// ---------------------------------------------------------------- 仓库与项目

function remoteHead(): string {
	const r = sh(`git ls-remote ${q(SANDBOX_REPO)} refs/heads/${BRANCH}`);
	return r.code === 0 ? (r.stdout.split(/\s+/)[0] ?? "") : "";
}

function ensureBranch() {
	if (!existsSync(`${CHECKOUT}/.git`)) {
		mkdirSync(dirname(CHECKOUT), { recursive: true });
		must(
			sh(`git clone --quiet ${q(SANDBOX_REPO)} ${q(CHECKOUT)}`),
			"clone 沙盒仓库",
		);
	}
	const g = `git -C ${q(CHECKOUT)}`;
	if (!remoteHead()) {
		must(
			sh(
				`${g} fetch -q origin pristine && ${g} checkout -q -B ${BRANCH} origin/pristine && ${g} push -q -u origin ${BRANCH}`,
			),
			// pristine 没有 .oryn/setup（main 上那份要装 apt / Node / pnpm，太慢）；setup 只跑项目脚本
			`从 pristine 建并 push 分支 ${BRANCH}`,
		);
		note(`已 push 基线分支 ${BRANCH}`);
	} else {
		must(
			sh(
				`${g} fetch -q origin ${BRANCH} && ${g} checkout -q -B ${BRANCH} FETCH_HEAD`,
			),
			`同步分支 ${BRANCH}`,
		);
	}
}

/** 在宿主检出里提交一个空提交并 push，返回新提交。 */
function pushCommit(): string {
	ensureBranch();
	const g = `git -C ${q(CHECKOUT)}`;
	must(
		sh(
			`${g} -c user.name=oryn-m11a -c user.email=m11a@oryn.invalid commit -q --allow-empty -m ${q(`m11a ${RUN} bump ${Date.now()}`)} && ${g} push -q origin HEAD:${BRANCH}`,
		),
		"提交并 push",
	);
	const head = must(sh(`${g} rev-parse HEAD`), "rev-parse").trim();
	check(remoteHead() === head, `远端 ${BRANCH} 已前进到 ${head.slice(0, 7)}`);
	return head;
}

async function findProject(name: string): Promise<Project | undefined> {
	const r = await getJson<{ projects: Project[] }>("/api/v1/projects");
	return r.projects.find((p) => p.name === name);
}

async function ensureProject(
	name: string,
	body: Record<string, unknown>,
): Promise<Project> {
	const hit = await findProject(name);
	if (hit) return hit;
	const c = await api("POST", "/api/v1/projects", { name, ...body });
	if (c.status !== 201)
		throw new Error(`建项目 ${name} 失败 ${c.status} ${c.text}`);
	note(`建项目 ${name}`);
	return c.json as Project;
}

async function patchProject(
	pid: string,
	body: Record<string, unknown>,
): Promise<Project> {
	const r = await api("PATCH", `/api/v1/projects/${pid}`, body);
	if (r.status !== 200)
		throw new Error(
			`PATCH 项目 ${JSON.stringify(body)} → ${r.status} ${r.text}`,
		);
	return r.json as Project;
}

async function ensureP(): Promise<Project> {
	ensureBranch();
	const p = await ensureProject(P_NAME, {
		repoUrl: SANDBOX_REPO,
		baseBranch: BRANCH,
		setupScript: SETUP_P,
		shallowClone: false,
		snapshots: true,
		warmOrbs: 0,
	});
	return p;
}

const ensureQ = () =>
	ensureProject(Q_NAME, {
		setupScript: SETUP_Q,
		snapshots: true,
		warmOrbs: 0,
	});

/** 预热对账只照顾 72 小时内建过线程的项目：Q 没有就建一个。 */
async function ensureQRecent(qp: Project) {
	const n = one<{ n: number }>(
		`select count(*) n from threads where project_id='${qp.id}' and created_at>${Date.now() - 72 * 3600_000}`,
	)?.n;
	if (!n) await newIdle({ projectId: qp.id, tag: "Q 线程" });
}

/** P 的 ready 快照；没有就建一个线程让它发布。 */
async function ensureReady(pid: string, type?: string): Promise<Snap> {
	const hit = await readyOf(pid, type);
	if (hit) return hit;
	note("项目没有 ready 快照：建一个线程发布");
	await newIdle({ projectId: pid, type, tag: "发布快照" });
	return waitFor("出现 ready 快照", () => readyOf(pid, type), 180_000, 1000);
}

async function ensureWarm(p: Project, n: number) {
	if (p.warmOrbs !== n) await patchProject(p.id, { warmOrbs: n });
}

async function pickProfile(tag: string): Promise<{ id: string; mode: string }> {
	const list = (
		await getJson<{ profiles: Profile[] }>("/api/v1/browser-profiles")
	).profiles;
	const want = process.env.M11_PROFILE;
	if (want) {
		const hit = list.find((p) => p.id === want || p.name === want);
		if (!hit) throw new Error(`M11_PROFILE=${want} 不存在`);
		note(`Profile ${hit.name}（${hit.id}，rev ${hit.revision}）readonly`);
		return { id: hit.id, mode: "readonly" };
	}
	const saved = list.find((p) => p.revision > 0);
	if (saved) {
		note(`Profile ${saved.name}（${saved.id}，rev ${saved.revision}）readonly`);
		return { id: saved.id, mode: "readonly" };
	}
	const name = `m11a-${RUN}-${tag}`;
	let p = list.find((x) => x.name === name);
	if (!p) {
		const r = await api("POST", "/api/v1/browser-profiles", { name });
		if (r.status !== 201)
			throw new Error(`建 Profile 失败 ${r.status} ${r.text}`);
		p = r.json as Profile;
	}
	note(`没有保存过的 Profile：新建空 Profile ${name}（${p.id}）exclusive`);
	return { id: p.id, mode: "exclusive" };
}

async function dindProvider(): Promise<Provider | undefined> {
	const r = await getJson<{ providers: Provider[] }>("/api/v1/providers");
	return r.providers.find(
		(p) => p.online && p.orbTypes.some((t) => t.name === DIND),
	);
}

async function waitProviderOnline() {
	await waitFor(
		"provider 在线",
		async () =>
			(
				await getJson<{ providers: Provider[] }>("/api/v1/providers")
			).providers.some((p) => p.online),
		120_000,
		1000,
	);
}

async function restartOrynd(what: string) {
	const r = sh(RESTART);
	check(r.code === 0, `${what}：${r.out.split("\n").at(-1)}`);
	await waitProviderOnline();
}

/** 把 snapshots.max_total_gb 设成 gb（流式 `{…}` / 块状 / 缺失三种写法）。 */
function withMaxTotalGb(src: string, gb: number): string {
	const lines = src.split("\n");
	const i = lines.findIndex((l) => /^snapshots:/.test(l));
	if (i < 0)
		return `${src.replace(/\n*$/, "\n")}snapshots: { max_total_gb: ${gb} }\n`;
	const flow = (lines[i] ?? "").match(/^snapshots:\s*\{(.*)\}\s*(#.*)?$/);
	if (flow) {
		const items = (flow[1] ?? "")
			.split(",")
			.map((s) => s.trim())
			.filter((s) => s && !s.startsWith("max_total_gb"));
		items.push(`max_total_gb: ${gb}`);
		lines[i] =
			`snapshots: { ${items.join(", ")} }${flow[2] ? ` ${flow[2]}` : ""}`;
		return lines.join("\n");
	}
	const out = lines.slice(0, i + 1);
	out.push(`  max_total_gb: ${gb}`);
	let j = i + 1;
	for (; j < lines.length; j++) {
		const l = lines[j] ?? "";
		if (l.trim() && !/^\s/.test(l)) break;
		if (!/^\s+max_total_gb:/.test(l)) out.push(l);
	}
	return [...out, ...lines.slice(j)].join("\n");
}

/** 首个字面模型 key 的前 12 位（跳过 `!命令` 与环境变量名形态）。 */
function modelKeyPrefix(): string {
	const f = `${homedir()}/.omp/agent/models.yml`;
	if (!existsSync(f)) return "";
	for (const m of readFileSync(f, "utf8").matchAll(
		/^\s*apiKey:\s*["']?([^"'\s#]+)/gm,
	)) {
		const k = m[1] ?? "";
		if (k.startsWith("!") || /^[A-Z_][A-Z0-9_]*$/.test(k) || k.length < 12)
			continue;
		return k.slice(0, 12);
	}
	return "";
}

const quotaSum = () =>
	one<{ s: number }>(
		`select coalesce(sum(size_bytes),0) s from project_snapshots where state in ('ready','retired')`,
	)?.s ?? 0;

// ---------------------------------------------------------------- 场景：项目快照

async function X2() {
	const p = await ensureP();
	if ((await snaps(p.id)).some((s) => s.state !== "retired")) {
		await clearSnapshots(p.id);
		note("P 已有快照（重跑）：先清除");
	}
	const head = remoteHead();
	note(`P = ${p.name}（${p.id}）分支 ${BRANCH} 远端 HEAD ${head}`);
	const t = await newIdle({ projectId: p.id, tag: "T1" });
	state.t1 = t.id;
	state.t1Ms = t.ms;
	save();
	const s = await waitFor(
		"P 出现 ready 快照",
		() => readyOf(p.id),
		120_000,
		1000,
	);
	note(`快照 ${fmtSnap(s)}`);
	const ready = (await snaps(p.id)).filter((x) => x.state === "ready");
	check(ready.length === 1, `恰一条 ready（${ready.length}）`);
	check(s.depth === 0, "depth = 0");
	check(s.baseCommit === head, `baseCommit = 远端 HEAD ${head.slice(0, 7)}`);
	check(imageExists(s.id), `docker images 有 ${s.id}`);
	const o = await orbView(t.orb);
	note(`T1 orb lastSetup：${fmtRun(o.lastSetup)}`);
	check(
		o.lastSetup?.setupRan === true && o.lastSetup.prepared === false,
		"T1 的 orb lastSetup.setupRan = true、prepared = false",
	);
}

async function X3() {
	const p = await ensureP();
	if (!state.t1Ms) {
		note("没有 T1 的耗时：先跑 X2");
		await X2();
	}
	const t1Ms = state.t1Ms ?? 0;
	const s = await ensureReady(p.id);
	note(`命中目标 ${fmtSnap(s)}`);
	const t = await newIdle({ projectId: p.id, tag: "T2" });
	state.t2 = t.id;
	save();
	const o = await orbView(t.orb);
	note(`T2 orb snapshotOf=${o.snapshotOf} kind=${o.snapshotKind}`);
	note(
		`lastSetup：${fmtRun(o.lastSetup)}；lastResume：${fmtRun(o.lastResume)}`,
	);
	check(o.snapshotOf === s.id, "snapshotOf = 该快照");
	check(o.snapshotKind === "project", "snapshotKind = project");
	check(
		o.lastSetup?.prepared === true && o.lastSetup.setupRan === false,
		"lastSetup.prepared = true、setupRan = false",
	);
	check(o.lastResume !== undefined, "lastResume 存在");
	const n = setupLines(t.orb);
	check(n === 1, `~/setup-runs.txt ${n} 行`);
	check(
		t.ms < t1Ms / 2,
		`耗时 T2 ${sec(t.ms)} < T1 ${sec(t1Ms)} 的一半（${sec(t1Ms / 2)}）`,
	);
	await waitFor(
		"desktop.displayReady",
		async () => (await orbView(t.orb)).desktop?.displayReady === true,
		60_000,
		1000,
	);
	check(true, "desktop.displayReady = true");
}

async function X4() {
	const p = await ensureP();
	const old = await ensureReady(p.id);
	note(`旧快照 ${fmtSnap(old)}`);
	const head = pushCommit();
	const t = await newIdle({ projectId: p.id, tag: "T3" });
	const o = await orbView(t.orb);
	note(`lastSetup：${fmtRun(o.lastSetup)}`);
	check(
		o.lastSetup?.prepared === true && o.lastSetup.setupRan === true,
		"lastSetup.prepared = true、setupRan = true",
	);
	check(o.lastSetup?.baseCommit === head, "lastSetup.baseCommit = 新提交");
	// 深度 d 的快照里已有 d+1 行，本次增量再加 1 行（分册的 2 行对应 depth 0）
	const want = old.depth + 2;
	const n = setupLines(t.orb);
	check(n === want, `~/setup-runs.txt ${n} 行（期望 ${want}）`);
	const orbHead = dexec(t.orb, `git -C ${WORKDIR} rev-parse HEAD`).stdout;
	check(orbHead === head, `orb 内仓库 HEAD = 新提交（${orbHead.slice(0, 7)}）`);
	const nw = await waitFor(
		"刷新出的新 ready 快照",
		async () => {
			const s = await readyOf(p.id);
			return s && s.id !== old.id ? s : undefined;
		},
		120_000,
		1000,
	);
	note(`新快照 ${fmtSnap(nw)}`);
	check(nw.depth === old.depth + 1, `depth = ${old.depth + 1}`);
	check(nw.lineageAt === old.lineageAt, "lineageAt 与旧行相同");
	check(nw.baseCommit === head, "新行 baseCommit = 新提交");
	const was = await snapById(p.id, old.id);
	check(
		was?.state === "retired" && was.retireReason === "superseded",
		`旧行 ${was?.state}/${was?.retireReason}`,
	);
}

async function X5() {
	const p = await ensureP();
	const old = await ensureReady(p.id);
	note(`旧快照 ${fmtSnap(old)}`);
	const script = `${p.setupScript ?? SETUP_P}\n# x5 ${Date.now()}`;
	await patchProject(p.id, { setupScript: script });
	note("PATCH setupScript：追加一行注释");
	const t = await newIdle({ projectId: p.id, tag: "T4" });
	const o = await orbView(t.orb);
	check(!o.snapshotOf, `T4 的 orb 没有 snapshotOf（${o.snapshotOf ?? "无"}）`);
	const n = setupLines(t.orb);
	check(n === 1, `~/setup-runs.txt ${n} 行`);
	const was = await snapById(p.id, old.id);
	check(
		was?.state === "retired" && was.retireReason === "config_changed",
		`旧行 ${was?.state}/${was?.retireReason}`,
	);
	const nw = await waitFor(
		"新的 ready 快照",
		async () => {
			const s = await readyOf(p.id);
			return s && s.id !== old.id ? s : undefined;
		},
		120_000,
		1000,
	);
	note(`新快照 ${fmtSnap(nw)}`);
	check(nw.depth === 0, "新行 depth = 0");
}

async function X6() {
	const p = await ensureP();
	const old = await ensureReady(p.id);
	sql(
		`update project_snapshots set lineage_at = lineage_at - 73*3600*1000 where state='ready' and project_id='${p.id}'`,
	);
	note(`sqlite：${old.id} 的 lineage_at 回拨 73 小时`);
	const t = await newIdle({ projectId: p.id, tag: "过期后线程" });
	const o = await orbView(t.orb);
	check(!o.snapshotOf, `不用快照（snapshotOf ${o.snapshotOf ?? "无"}）`);
	const was = await snapById(p.id, old.id);
	check(
		was?.state === "retired" && was.retireReason === "expired",
		`该行 ${was?.state}/${was?.retireReason}`,
	);
}

async function X7() {
	const p = await ensureP();
	await clearSnapshots(p.id);
	const since = Date.now();
	note("DELETE projects/P/snapshots → 204");
	const started = await Promise.all(
		[1, 2, 3].map((i) => startThread({ projectId: p.id, tag: `并发 ${i}` })),
	);
	const ms = await Promise.all(started.map((s) => leaveProvisioning(s.id)));
	for (const s of started) await waitIdle(s.id);
	const orbs = started.map((s) => orbOf(s.id));
	started.forEach((s, i) => {
		note(`${s.id} orb=${orbs[i]} 耗时 ${sec(ms[i] ?? 0)}`);
	});
	const jobs = sql<{ id: string; orb_id: string; state: string }>(
		`select id, orb_id, state from jobs where op='snapshot' and created_at>=${since} and orb_id in (${orbs.map((o) => `'${o}'`).join(",")})`,
	);
	const all = one<{ n: number }>(
		`select count(*) n from jobs where op='snapshot' and created_at>=${since}`,
	)?.n;
	note(
		`此后 snapshot 任务：三台 orb ${jobs.length} 条（${jobs.map((j) => `${j.orb_id}:${j.state}`).join(" ")}），全库 ${all} 条`,
	);
	check(jobs.length === 1, "jobs 里 op = snapshot 恰 1 条");
	const ready = (await snaps(p.id)).filter((s) => s.state === "ready");
	check(ready.length === 1, `快照表恰一条 ready（${ready.length}）`);
}

async function X8() {
	const p = await ensureP();
	const old = await ensureReady(p.id);
	note(`ready 快照 ${fmtSnap(old)}`);
	must(sh(`docker rmi -f ${q(old.id)}`), `docker rmi -f ${old.id}`);
	check(!imageExists(old.id), "镜像已不存在");
	const t = await newIdle({ projectId: p.id, tag: "镜像缺失后线程" });
	const o = await orbView(t.orb);
	note(`lastSetup：${fmtRun(o.lastSetup)}`);
	check(
		o.lastSetup?.prepared === false && o.lastSetup.setupRan === true,
		"prepared = false、setupRan = true",
	);
	const was = await snapById(p.id, old.id);
	check(
		was?.state === "retired" && was.retireReason === "image_missing",
		`原行 ${was?.state}/${was?.retireReason}`,
	);
	const nw = await waitFor(
		"新的 ready 快照",
		async () => {
			const s = await readyOf(p.id);
			return s && s.id !== old.id ? s : undefined;
		},
		120_000,
		1000,
	);
	note(`新快照 ${fmtSnap(nw)}`);
}

async function X9() {
	if (process.env.M11A_NO_RESTART === "1")
		throw new Skip("M11A_NO_RESTART=1，跳过（需要重启 orynd）");
	const p = await ensureP();
	const qp = await ensureQ();
	const orig = readFileSync(ORYND_YAML, "utf8");
	writeFileSync(ORYND_BAK, orig);
	try {
		writeFileSync(ORYND_YAML, withMaxTotalGb(orig, 1));
		note(
			"dev/orynd.yaml 临时写入 snapshots.max_total_gb: 1（原文备份 .dev/orynd.yaml.m11a-bak）",
		);
		await restartOrynd("max_total_gb=1 重启 orynd");
		// 已退役但仍被本脚本早先 orb 引用的快照也计入配额：先删掉这些 orb，让回收器清走
		for (const pid of [p.id, qp.id]) await clearSnapshots(pid);
		const refs = sql<{ id: string }>(
			`select id from orbs where state not in ('deleting','deleted') and snapshot_of in (select id from project_snapshots where project_id in ('${p.id}','${qp.id}'))`,
		);
		for (const r of refs) await api("DELETE", `/api/v1/orbs/${r.id}`);
		for (const r of refs) await waitOrbDeleted(r.id);
		note(`删除引用 P / Q 旧快照的 orb ${refs.length} 台`);
		await waitFor(
			"配额占用回落到 1GB 以下",
			() => quotaSum() < GB,
			180_000,
			2000,
		).catch(() => undefined);
		note(`当前配额占用 ${mb(quotaSum())}`);
		check(quotaSum() < GB, "发布前配额占用 < 1GB");

		await newIdle({ projectId: p.id, tag: "P 发布" });
		const sp = await waitFor(
			"P 的 ready 快照",
			() => readyOf(p.id),
			120_000,
			1000,
		);
		note(`P 快照 ${fmtSnap(sp)}`);
		const b = await newIdle({ projectId: p.id, tag: "P 命中（引用者）" });
		check(
			(await orbView(b.orb)).snapshotOf === sp.id,
			`引用者 orb ${b.orb} 的 snapshotOf = ${sp.id}`,
		);
		await newIdle({ projectId: qp.id, tag: "Q 发布" });
		const sq = await waitFor(
			"Q 的 ready 快照",
			() => readyOf(qp.id),
			120_000,
			1000,
		);
		note(`Q 快照 ${fmtSnap(sq)}；配额占用 ${mb(quotaSum())}`);
		const spNow = await waitFor(
			"P 的快照退役",
			async () => {
				const s = await snapById(p.id, sp.id);
				return s?.state === "retired" ? s : undefined;
			},
			120_000,
			1000,
		);
		check(
			spNow.retireReason === "quota",
			`先发布的 P 快照 ${spNow.state}/${spNow.retireReason}`,
		);
		check(imageExists(sp.id), "仍被 orb 引用时镜像还在");
		const d = await api("DELETE", `/api/v1/orbs/${b.orb}`);
		check(d.status === 202, `DELETE orbs/${b.orb} → ${d.status}`);
		const delMs = await waitOrbDeleted(b.orb);
		const t0 = Date.now();
		await waitFor(
			"快照行与镜像消失",
			async () => !(await snapById(p.id, sp.id)) && !imageExists(sp.id),
			60_000,
			1000,
		);
		check(
			true,
			`orb ${sec(delMs)} 后 deleted，再过 ${sec(Date.now() - t0)} 镜像与行都消失（≤ 60s）`,
		);
	} finally {
		writeFileSync(ORYND_YAML, orig);
		rmSync(ORYND_BAK, { force: true });
		const r = sh(RESTART);
		note(
			`还原 dev/orynd.yaml 并重启 orynd（${r.code}）：${r.out.split("\n").at(-1)}`,
		);
		await waitProviderOnline().catch((e) =>
			note(`provider 未恢复在线：${String(e)}`),
		);
	}
}

async function X10() {
	const p = await ensureP();
	const s = await ensureReady(p.id);
	const ls = await cli(`project snapshots ls ${q(p.name)}`);
	console.log(ls.out.replace(/^/gm, "      "));
	check(ls.code === 0, "oryn project snapshots ls 退出码 0");
	const [head, ...rows] = ls.stdout.trim().split("\n");
	const cols = (head ?? "").trim().split(/\s+/);
	check(
		["TYPE", "STATE", "COMMIT", "AGE", "SIZE", "USES"].every((c) =>
			cols.includes(c),
		),
		`表头 ${cols.join(" ")}`,
	);
	const row = rows.find(
		(r) =>
			r.includes(" ready ") &&
			r.startsWith(s.type) &&
			(!s.baseCommit || r.includes(s.baseCommit.slice(0, 7))),
	);
	check(!!row, `有 ${s.type} / ready / ${s.baseCommit?.slice(0, 7)} 的行`);
	const rm = await cli(`project snapshots rm ${q(p.name)}`);
	check(rm.code === 0, `oryn project snapshots rm：${rm.out}`);
	const was = await snapById(p.id, s.id);
	check(
		was?.state === "retired" && was.retireReason === "manual",
		`该行 ${was?.state}/${was?.retireReason}`,
	);
}

async function X11() {
	const p = await ensureP();
	const ps = await ensureReady(p.id);
	note(`项目快照 ${fmtSnap(ps)}`);
	let t2 = state.t2 ?? "";
	if (!t2 || !threadRow(t2)?.orb_id || orbDbState(orbOf(t2)) === "deleted") {
		t2 = (await newIdle({ projectId: p.id, tag: "T2（重建）" })).id;
		state.t2 = t2;
		save();
	}
	focus = t2;
	await ensureAwake(t2);
	const r = await api("POST", `/api/v1/threads/${t2}/fork`, { env: "copy" });
	check(r.status === 201, `fork copy → ${r.status}`);
	const f = (r.json as { thread: { id: string } }).thread.id;
	focus = f;
	await waitFor(
		`${f} 就绪并静止`,
		() => {
			const row = threadRow(f);
			return row && row.last_seq > 0 && SETTLED[row.state];
		},
		600_000,
		1000,
	);
	const fo = await orbView(orbOf(f));
	note(`fork 线程 ${f} orb=${fo.id} snapshotOf=${fo.snapshotOf}`);
	check(fo.snapshotKind === "fork", "新 orb snapshotKind = fork");
	const forkSnap = fo.snapshotOf ?? "";
	check(!!forkSnap && imageExists(forkSnap), `fork 镜像 ${forkSnap} 存在`);
	const inTable = one<{ n: number }>(
		`select count(*) n from project_snapshots where id='${forkSnap}'`,
	)?.n;
	check(inTable === 0, "fork 快照不在 project_snapshots 里");
	const d = await api("DELETE", `/api/v1/orbs/${fo.id}`);
	check(d.status === 202, `DELETE orbs/${fo.id} → ${d.status}`);
	await waitOrbDeleted(fo.id);
	const t0 = Date.now();
	await waitFor("fork 镜像被删", () => !imageExists(forkSnap), 180_000, 2000);
	note(`fork orb 删除后 ${sec(Date.now() - t0)} fork 镜像消失`);
	const still = await snapById(p.id, ps.id);
	check(still?.state === "ready", `项目快照行仍在（${still?.state}）`);
	check(imageExists(ps.id), `项目快照镜像 ${ps.id} 仍在`);
}

async function X12() {
	const f = `${ROOT}/.dev/m11-x12.thread`;
	const tid =
		process.env.M11_X12_THREAD ??
		(existsSync(f) ? readFileSync(f, "utf8").trim() : "");
	if (!tid)
		throw new Skip("没有旧镜像线程（.dev/m11-x12.thread / M11_X12_THREAD）");
	focus = tid;
	const orb = orbOf(tid);
	const v0 = await orbView(orb);
	note(`旧线程 ${tid} orb=${orb} image=${v0.image} orbd=${v0.orbdVersion}`);
	const since = Math.floor(Date.now() / 1000);
	const t0 = Date.now();
	await ensureAwake(tid);
	note(`唤醒用时 ${sec(Date.now() - t0)}`);
	const before = threadRow(tid)?.last_seq ?? 0;
	const s = await cli(`thread send ${tid} ${q("只回复 ok")}`);
	check(s.code === 0, "oryn thread send 退出码 0");
	await waitIdle(tid, before);
	const text = await lastAssistant(tid);
	check(text.length > 0, `正常应答：${text.slice(0, 60)}`);
	const logs = sh(`docker logs --since ${since} ${orb} 2>&1`).stdout;
	const hits = logs.split("\n").filter((l) => /fetch/i.test(l));
	check(
		hits.length === 0,
		`唤醒后 orbd 日志（docker logs）没有 fetch（${hits.length} 行）`,
	);
	const setupLog = dexec(
		orb,
		"cat ~/.cache/oryn/logs/setup.log ~/.cache/oryn/logs/resume.log 2>/dev/null | grep -ci fetch || true",
	).stdout;
	check(
		Number(setupLog || 0) === 0,
		`setup / resume 日志里没有 fetch（${setupLog || 0}）`,
	);
}

let secretSet = false;

async function X13() {
	const p = await ensureP();
	const r = randomBytes(12).toString("hex");
	const put = await api("PUT", `/api/v1/secrets/${SECRET_NAME}`, {
		value: r,
		mode: "env",
	});
	check(put.status === 200, `PUT secrets/${SECRET_NAME}（env）→ ${put.status}`);
	secretSet = true;
	const key = modelKeyPrefix();
	note(
		key
			? "取到 ~/.omp/agent/models.yml 首个字面 apiKey 的前 12 位"
			: "没取到真实模型 key：只检查 M11_SECRET",
	);
	await clearSnapshots(p.id);
	const prof = await pickProfile("x13");
	const t = await newIdle({
		projectId: p.id,
		browserProfileId: prof.id,
		profileMode: prof.mode,
		tag: "带 Profile",
	});
	const o = await orbView(t.orb);
	note(`orb desktop.profile=${o.desktop?.profile?.id ?? "无"}`);
	const s = await waitFor(
		"该 orb 发布的 ready 快照",
		async () => {
			const x = await readyOf(p.id);
			if (!x) return undefined;
			const src = one<{ source_orb_id: string }>(
				`select source_orb_id from project_snapshots where id='${x.id}'`,
			)?.source_orb_id;
			return src === t.orb ? x : undefined;
		},
		120_000,
		1000,
	);
	note(`快照 ${fmtSnap(s)}（source_orb_id = ${t.orb}）`);
	const script = [
		'grep -rlF -- "$M11A_S" /home/user 2>/dev/null | wc -l',
		key ? 'grep -rlF -- "$M11A_K" /home/user 2>/dev/null | wc -l' : "echo 0",
		"ls -A /home/user/.oryn/desktop/profile 2>/dev/null | wc -l",
		"ls /home/user/.oryn/threads 2>/dev/null | wc -l",
	].join("; ");
	const out = must(
		sh(
			`docker run --rm -e M11A_S=${q(r)} -e M11A_K=${q(key)} --entrypoint sh ${q(s.id)} -c ${q(script)}`,
		),
		"docker run 快照",
	);
	const nums = out.split(/\s+/).map(Number);
	note(`四个数：${nums.join(" ")}${key ? "" : "（第二个未检查）"}`);
	check(
		nums.length === 4 && nums.every((n) => n === 0),
		"机密 / 模型 key / Profile / 线程目录都不在快照里",
	);
}

// ---------------------------------------------------------------- 场景：预热池

async function X14() {
	const p = await ensureP();
	if (p.warmOrbs !== 0 || (await poolOrbs(p.id)).length) {
		await patchProject(p.id, { warmOrbs: 0 });
		await waitFor(
			"P 的旧池 orb 回收",
			async () => (await poolOrbs(p.id)).length === 0,
			180_000,
			1000,
		);
		note("重跑：先把 P 的旧池 orb 清掉");
	}
	await patchProject(p.id, { warmOrbs: 1 });
	const t0 = Date.now();
	note("PATCH warmOrbs: 1");
	const seen = new Set<string>();
	const o = await waitFor(
		"60 秒内出现池 orb",
		async () => {
			const x = (await poolOrbs(p.id))[0];
			if (x?.pool) seen.add(x.pool);
			return x;
		},
		60_000,
		300,
	);
	note(`池 orb ${o.id} 在 ${sec(Date.now() - t0)} 出现，pool=${o.pool}`);
	check(seen.has("warming"), "先是 pool = warming");
	const idle = await waitFor(
		"池 orb 变 idle",
		async () => {
			const v = await orbView(o.id);
			if (v.pool) seen.add(v.pool);
			return v.pool === "idle" ? v : undefined;
		},
		1_200_000,
		1000,
	);
	note(`${sec(Date.now() - t0)} 时 pool = idle，state=${idle.state}`);
	check(idle.threadIds.length === 0, "threadIds = []");
}

async function X15() {
	const p = await ensureP();
	await ensureWarm(p, 1);
	let pool = await waitFor(
		"P 有 idle 的池 orb",
		async () => (await poolOrbs(p.id)).find((o) => o.pool === "idle"),
		1_200_000,
		1000,
	);
	if (pool.state !== "ready") {
		note(`池 orb ${pool.id} 是 ${pool.state}：先唤醒到 ready`);
		await api("POST", `/api/v1/orbs/${pool.id}/resume`);
		pool = await waitFor(
			"池 orb ready",
			async () => {
				const v = await orbView(pool.id);
				return v.state === "ready" && v.pool === "idle" ? v : undefined;
			},
			300_000,
			500,
		);
	}
	const since = Date.now();
	const s = await startThread({ projectId: p.id, tag: "T5" });
	check(s.orbId === pool.id, `T5.orbId = 池 orb ${pool.id}（${s.orbId}）`);
	const ms = await leaveProvisioning(s.id);
	await waitIdle(s.id);
	const ev = events(pool.id, since - 1000);
	note(`事件 ${pathStr(ev)}`);
	check(
		hasPath(ev, ["ready", "connecting", "resuming", "ready"]),
		"事件含 ready → connecting → resuming → ready",
	);
	const v = await orbView(pool.id);
	check(v.pool === undefined, `pool 字段消失（${v.pool ?? "无"}）`);
	check(ms < 8000, `耗时 ${sec(ms)}（预期 < 8s）`);
	const t0 = Date.now();
	const repl = await waitFor(
		"60 秒内补位",
		async () => (await poolOrbs(p.id)).find((o) => o.id !== pool.id),
		60_000,
		300,
	);
	note(
		`补位池 orb ${repl.id}（${repl.pool}）在认领后 ${sec(Date.now() - t0)} 出现`,
	);
}

async function X16() {
	const p = await ensureP();
	await ensureWarm(p, 1);
	let w = (await poolOrbs(p.id)).find((o) => o.pool === "warming");
	if (!w) {
		const idle = (await poolOrbs(p.id)).find((o) => o.pool === "idle");
		if (idle) {
			note(`没有 warming 的池 orb：先认领 idle 的 ${idle.id} 逼出补位`);
			await waitIdle(
				(await startThread({ projectId: p.id, tag: "逼补位" })).id,
			);
		}
		w = await waitFor(
			"warming 的池 orb",
			async () => (await poolOrbs(p.id)).find((o) => o.pool === "warming"),
			180_000,
			200,
		);
	}
	const s = await startThread({ projectId: p.id, tag: "T6" });
	check(s.orbId === w.id, `T6.orbId = warming 的池 orb ${w.id}（${s.orbId}）`);
	const ms = await leaveProvisioning(s.id);
	await waitIdle(s.id);
	note(`T6 耗时 ${sec(ms)}`);
	const claimed = events(w.id).filter((e) =>
		/"pool"\s*:\s*"claimed"/.test(e.detail),
	);
	check(claimed.length > 0, "有认领 warming 的事件（detail.pool = claimed）");
	const runs = (
		await getJson<{ runs: SetupRun[] }>(`/api/v1/orbs/${w.id}/setup-runs`)
	).runs.filter((r) => r.phase === "setup");
	check(runs.length === 1, `phase = setup 的运行记录 ${runs.length} 条`);
}

async function X17() {
	const p = await ensureP();
	await ensureWarm(p, 1);
	const t0 = Date.now();
	const fr = await waitFor(
		"P 的池 orb 冻结",
		async () =>
			(await poolOrbs(p.id)).find(
				(o) => o.pool === "idle" && o.state === "frozen",
			),
		900_000,
		2000,
	);
	note(`池 orb ${fr.id} 等待 ${sec(Date.now() - t0)} 后 frozen`);
	const since = Date.now();
	const s = await startThread({ projectId: p.id, tag: "认领 frozen" });
	check(s.orbId === fr.id, `线程 orbId = ${fr.id}（${s.orbId}）`);
	const ms = await leaveProvisioning(s.id);
	await waitIdle(s.id);
	const ev = events(fr.id, since - 1000);
	note(`事件 ${pathStr(ev)}；耗时 ${sec(ms)}`);
	check(hasPath(ev, ["frozen", "waking"]), "事件含 frozen → waking");
	const text = await lastAssistant(s.id);
	check(text.length > 0, `正常应答：${text.slice(0, 40)}`);
}

async function X18() {
	const p = await ensureP();
	await ensureWarm(p, 1);
	const old = await waitFor(
		"P 有池 orb",
		async () => {
			const os = await poolOrbs(p.id);
			return os.length ? os : undefined;
		},
		600_000,
		1000,
	);
	const hashOf = (orb: string) =>
		one<{ h: string | null }>(
			`select config_hash h from orbs where id='${orb}'`,
		)?.h ?? "";
	const oldHashes = old.map((o) => hashOf(o.id));
	note(`旧池 orb ${old.map((o) => `${o.id}(${o.pool})`).join(" ")}`);
	const fresh = await findProject(P_NAME);
	const patchAt = Date.now();
	await patchProject(p.id, {
		setupScript: `${fresh?.setupScript ?? SETUP_P}\n# x18 ${patchAt}`,
	});
	note("PATCH setupScript：追加一行注释");
	for (const o of old) {
		const ms = await waitOrbDeleted(o.id, 300_000);
		note(`旧池 orb ${o.id} 在 ${sec(ms)} 内 deleted`);
	}
	const nw = await waitFor(
		"新的池 orb",
		async () =>
			(await poolOrbs(p.id)).find((o) => !old.some((x) => x.id === o.id)),
		180_000,
		1000,
	);
	const nh = await waitFor(
		"新池 orb 的 config_hash",
		() => hashOf(nw.id),
		120_000,
	);
	check(
		!oldHashes.includes(nh),
		`新池 orb ${nw.id} config_hash ${nh.slice(0, 12)} ≠ 旧 ${oldHashes.map((h) => h.slice(0, 12)).join(",")}`,
	);
	const snap = await waitFor(
		"改配置后的新 ready 快照",
		async () => {
			const s = await readyOf(p.id);
			return s && s.createdAt >= patchAt ? s : undefined;
		},
		1_200_000,
		2000,
	);
	const src =
		one<{ s: string }>(
			`select source_orb_id s from project_snapshots where id='${snap.id}'`,
		)?.s ?? "";
	note(`新快照 ${fmtSnap(snap)}，source_orb_id=${src}`);
	check(wasPoolOrb(src), "source_orb_id 是池 orb");
}

async function X19() {
	const p = await ensureP();
	const qp = await ensureQ();
	await ensureQRecent(qp);
	await patchProject(p.id, { warmOrbs: 3 });
	await patchProject(qp.id, { warmOrbs: 3 });
	note("P、Q 各 warmOrbs: 3；采样 90 秒");
	let max = 0;
	let dist = "";
	for (let t0 = Date.now(); Date.now() - t0 < 90_000; ) {
		const os = await poolOrbs();
		if (os.length >= max) {
			max = os.length;
			dist = `P ${os.filter((o) => o.projectId === p.id).length} / Q ${os.filter((o) => o.projectId === qp.id).length} / 其它 ${os.filter((o) => o.projectId !== p.id && o.projectId !== qp.id).length}`;
		}
		await sleep(2000);
	}
	check(max <= 2, `池 orb 总数峰值 ${max} ≤ 2（${dist}）`);
	const poolP = await waitFor(
		"P 有 idle 的池 orb",
		async () => (await poolOrbs(p.id)).find((o) => o.pool === "idle"),
		1_200_000,
		1000,
	);
	const prof = await pickProfile("x19");
	const s = await startThread({
		projectId: p.id,
		browserProfileId: prof.id,
		profileMode: prof.mode,
		tag: "带 Profile",
	});
	const sOrb = s.orbId ?? orbOf(s.id);
	check(
		sOrb !== poolP.id && !wasPoolOrb(sOrb),
		`带 Profile 的线程用新 orb ${sOrb}（池 orb ${poolP.id}）`,
	);
	const stillPooled = await orbView(poolP.id);
	check(stillPooled.pool === "idle", `池 orb 仍是 ${stillPooled.pool}`);
	const r = await api(
		"POST",
		"/api/v1/threads",
		threadBody({ projectId: p.id, orbId: poolP.id, tag: "挂池 orb" }),
	);
	const code = (r.json as { code?: string } | null)?.code;
	check(
		r.status === 409 && code === "orb_is_pooled",
		`挂到池 orb → ${r.status} ${code}`,
	);
	const pIds = (await poolOrbs(p.id)).map((o) => o.id);
	await patchProject(p.id, { warmOrbs: 0 });
	const t0 = Date.now();
	for (const id of pIds) await waitOrbDeleted(id, 60_000 - (Date.now() - t0));
	check(
		true,
		`P 改回 0 后 ${pIds.length} 台池 orb 在 ${sec(Date.now() - t0)} 内 deleted`,
	);
	await waitIdle(s.id);
}

async function X20() {
	const p = await ensureP();
	for (const name of [Q_NAME, D_NAME]) {
		const x = await findProject(name);
		if (x && x.warmOrbs !== 0) await patchProject(x.id, { warmOrbs: 0 });
	}
	await ensureWarm(p, 1);
	const pool = await waitFor(
		"池里恰 1 台 idle、没有 warming",
		async () => {
			const os = await poolOrbs();
			const idle = os.filter((o) => o.pool === "idle");
			const only = idle[0];
			return os.length === 1 && only && only.projectId === p.id
				? only
				: undefined;
		},
		1_200_000,
		1000,
	);
	note(`唯一的池 orb ${pool.id}（${pool.state}）`);
	const [a, b] = await Promise.all([
		startThread({ projectId: p.id, tag: "并发认领 A" }),
		startThread({ projectId: p.id, tag: "并发认领 B" }),
	]);
	if (!a || !b) throw new Error("并发建线程失败");
	const ao = a.orbId ?? orbOf(a.id);
	const bo = b.orbId ?? orbOf(b.id);
	note(`A ${a.id} orb=${ao}；B ${b.id} orb=${bo}`);
	check(ao !== bo, "两个线程 orbId 不同");
	check(
		[ao, bo].filter((o) => o === pool.id).length === 1,
		"恰有一个等于池 orb",
	);
	await waitIdle(a.id);
	await waitIdle(b.id);
	await patchProject(p.id, { warmOrbs: 0 });
}

// ---------------------------------------------------------------- 场景：嵌套 Docker

async function X21() {
	const prov = await dindProvider();
	if (!prov)
		throw new Skip(
			`没有在线 provider 声明 ${DIND} 类型（父 agent 集成时加进 dev/provider.yaml）`,
		);
	note(
		`provider ${prov.name} 的 ${DIND} 特性：${prov.orbTypes.find((t) => t.name === DIND)?.features?.join(",") ?? "无"}`,
	);
	const t = await newIdle({
		prompt: "运行 docker run --rm hello-world，只回复含 Hello 的那一行",
		type: DIND,
		tag: "dind",
	});
	const text = await lastAssistant(t.id);
	check(text.includes("Hello from Docker"), `回复：${text.slice(0, 80)}`);
	const v = await orbView(t.orb);
	check(v.nestedDocker === true, "orb.nestedDocker = true");
	const insp = sh(
		`docker inspect ${t.orb} --format '{{.HostConfig.Runtime}} {{.HostConfig.Privileged}}'`,
	).stdout;
	check(insp === "sysbox-runc false", `Runtime / Privileged = ${insp}`);
	const um = sh(`docker exec ${t.orb} cat /proc/self/uid_map`).stdout;
	const host = um.split("\n")[0]?.trim().split(/\s+/)[1] ?? "";
	check(host !== "" && host !== "0", `uid_map 第二列 = ${host}`);
	const mk = sh(
		`docker exec ${t.orb} sh -c 'sudo mknod /tmp/d b 8 0 && sudo head -c1 /tmp/d'`,
	);
	check(
		mk.code !== 0,
		`mknod + 读块设备失败（${mk.code}：${mk.out.slice(0, 80)}）`,
	);
	const n = await newIdle({
		prompt: "用 bash 执行 docker ps，只回复其输出（含错误信息）",
		tag: "普通类型",
	});
	note(`普通类型回复：${(await lastAssistant(n.id)).slice(0, 80)}`);
	const dp = sh(`docker exec ${n.orb} docker ps`);
	check(
		dp.code !== 0 && /Cannot connect to the Docker daemon/i.test(dp.out),
		`普通 orb 里 docker ps 连不上守护进程（${dp.out.split("\n")[0]}）`,
	);
	const cd = JSON.parse(
		sh(`docker inspect ${n.orb} --format '{{json .HostConfig.CapDrop}}'`)
			.stdout || "[]",
	) as string[] | null;
	const caps = cd ?? [];
	check(
		["NET_RAW", "NET_ADMIN"].every((c) => caps.some((x) => x.endsWith(c))),
		`普通 orb CapDrop = ${caps.join(",")}`,
	);
}

async function X22() {
	if (!(await dindProvider()))
		throw new Skip(`没有在线 provider 声明 ${DIND} 类型`);
	const d = await ensureProject(D_NAME, {
		setupScript: SETUP_D,
		defaultOrbType: DIND,
		snapshots: true,
		warmOrbs: 0,
	});
	if ((await snaps(d.id)).some((s) => s.state !== "retired"))
		await clearSnapshots(d.id);
	const a = await newIdle({ projectId: d.id, type: DIND, tag: "dind #1" });
	const av = await orbView(a.orb);
	note(`#1 lastSetup：${fmtRun(av.lastSetup)}`);
	const s = await waitFor(
		"dind 类型的 ready 快照",
		() => readyOf(d.id, DIND),
		300_000,
		1000,
	);
	note(`快照 ${fmtSnap(s)}`);
	const b = await newIdle({ projectId: d.id, type: DIND, tag: "dind #2" });
	const bv = await orbView(b.orb);
	note(`#2 snapshotOf=${bv.snapshotOf} lastSetup：${fmtRun(bv.lastSetup)}`);
	check(bv.snapshotOf === s.id, "#2 来自该快照");
	check(bv.lastSetup?.setupRan === false, "#2 setupRan = false");
	const img = await waitFor(
		"#2 里 docker image ls alpine 有结果",
		() => {
			const r = dexec(
				b.orb,
				"docker image ls alpine --format '{{.Repository}}:{{.Tag}}'",
			);
			return r.code === 0 && r.stdout ? r.stdout : undefined;
		},
		90_000,
		2000,
	);
	check(true, `docker image ls alpine：${img.split("\n").join(" ")}`);
}

const TESTS: [string, () => Promise<void>][] = [
	["X2", X2],
	["X3", X3],
	["X4", X4],
	["X5", X5],
	["X6", X6],
	["X7", X7],
	["X8", X8],
	["X9", X9],
	["X10", X10],
	["X11", X11],
	["X12", X12],
	["X13", X13],
	["X14", X14],
	["X15", X15],
	["X16", X16],
	["X17", X17],
	["X18", X18],
	["X19", X19],
	["X20", X20],
	["X21", X21],
	["X22", X22],
];

// ---------------------------------------------------------------- 主流程

if (existsSync(ORYND_BAK)) {
	writeFileSync(ORYND_YAML, readFileSync(ORYND_BAK));
	rmSync(ORYND_BAK);
	const r = sh(RESTART);
	console.log(
		`发现上次中断遗留的 .dev/orynd.yaml.m11a-bak：已写回 dev/orynd.yaml 并重启 orynd（${r.code}）`,
	);
}
console.log(
	`M11a 验收 run=${RUN} base=${BASE} 项目 ${P_NAME} 分支 ${BRANCH} shots=${SHOTS}`,
);

for (const [id, fn] of TESTS) {
	if (ONLY && !ONLY.has(id)) continue;
	evidence = [];
	focus = "";
	console.log(`── ${id}`);
	const t0 = Date.now();
	const took = () => ((Date.now() - t0) / 1000).toFixed(1);
	try {
		await fn();
		console.log(`${id} PASS (${took()}s)`);
		results.push({ id, verdict: "PASS", evidence });
	} catch (e) {
		if (e instanceof Skip) {
			note(`跳过：${e.message}`);
			console.log(`${id} SKIP (${took()}s)`);
			results.push({ id, verdict: "SKIP", evidence });
			continue;
		}
		note(`错误：${e instanceof Error ? e.message : String(e)}`);
		await failArtifacts(id);
		console.log(`${id} FAIL (${took()}s)`);
		results.push({ id, verdict: "FAIL", evidence });
	}
}

// 收尾：停掉预热，删掉本进程写入的密钥
console.log("── 收尾");
evidence = [];
if (process.env.M11A_KEEP_POOL !== "1")
	for (const name of [P_NAME, Q_NAME, D_NAME]) {
		const x = await findProject(name).catch(() => undefined);
		if (x && x.warmOrbs !== 0)
			await patchProject(x.id, { warmOrbs: 0 }).then(
				() => note(`${name} warmOrbs → 0`),
				(e) => note(`${name} 预热数复位失败：${String(e)}`),
			);
	}
if (secretSet) {
	const r = await api("DELETE", `/api/v1/secrets/${SECRET_NAME}`);
	note(`删除密钥 ${SECRET_NAME} → ${r.status}`);
}
await browser?.close();
await Bun.write(
	`${SHOTS}/results-${RUN}.json`,
	JSON.stringify(results, null, 2),
);
console.log("\n汇总");
for (const r of results) console.log(`${r.id} ${r.verdict}`);
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
