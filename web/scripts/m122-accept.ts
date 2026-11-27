#!/usr/bin/env bun
import {
	existsSync,
	mkdirSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
/**
 * M12.2 真机验收（方案「真机验收」Z1–Z9）：对 dev 部署（镜像含 Core oryn-v18.19.0-8）逐项执行，每项打印
 * `Z<n> PASS|FAIL (秒)` 与证据行；失败时留 orynd.log 末尾与该场景最近线程的页面截图；汇总写 $M122_SHOTS/results.json。
 *
 * 用法（web/ 目录）：bun run accept:m122 [-- --only Z1,Z5] [-- --help]
 * 环境变量：ORYN_URL（缺省 http://localhost:7600）、ORYN_ROOT、ORYN_TOKEN、ORYN_CREDENTIALS（缺省 .dev/credentials.json）；
 *   M122_PROVIDER（orb provider，缺省 local）、M122_REPO（缺省 https://github.com/Lynricsy/oryn-setup-sandbox.git）、
 *   M122_BRANCH（缺省 pristine，无 setup 脚本）、M122_SHOTS（缺省 /tmp/oryn-shots/m122）、CHROMIUM（缺省 /usr/bin/chromium）。
 * 运行前提：dev 部署在跑且镜像已含 Core oryn-v18.19.0-8（make build && make web && make image）；本机有 sqlite3
 *   （读 .dev/data/oryn.db 的 threads / orbs / orb_events）；dev 能克隆上面的私有仓库（同现有 sandbox-noscript 项目）；
 *   dev 的全局 Agent 配置里有能用的真实模型（线程用缺省模型）。
 *
 * 固定布置：
 *   - 项目 `m122-a`、`m122-b`：按名复用，没有就用 REST 建（repoUrl = M122_REPO、baseBranch = M122_BRANCH、snapshots = false：
 *     每台新 orb 都走完整的 setup.run，不受项目快照构建干扰）。收尾**保留两个项目**以便下次复用，但删光它们的项目级配置文件。
 *   - 线程一律 `oryn thread new --project <项目>|--no-project --provider <M122_PROVIDER>`，每个线程一台新 orb，等首回合到 idle；
 *     收尾删除本次建的全部 orb。
 *   - 真实模型，没有假端点。「先跑一轮」= 线程首条提示词让模型原样回复 M122_MARKER；/omfg 的吐槽针对这个词。
 *     规则名由模型决定，不固定：取 omfg 应答的 `data.oryn.path`（契约 C5；浏览器抓 WS 下行的 `res` 帧），后续断言都用它。
 *   - /omfg 在浏览器里从输入坞提交（结果提示是前端本地插入的 notice，只有发命令的页面能看到）：生成规则可能要几十秒；
 *     先出「Validation / Save anyway?」或「Overwrite TTSR rule?」确认框时点「是」，再在保存位置选择卡上点要的项。
 *   - 全局配置（Z4 的同名技能 skills/z1/SKILL.md、Z6 由 /omfg 写进全局的规则）：改之前记下原状（原文与权限，或「不存在」），
 *     Z4 结束时、以及收尾时一律还原（原来没有的删掉，原来有的写回原文），不污染主人的全局配置。
 *   - 场景之间的前置惰性准备，`--only` 单跑时缺什么现建什么（Z3/Z4/Z2 缺 Z1 的文件就先推送；Z8 缺 Z3 的 orb 就先跑 Z3；
 *     Z5 复用 Z4 的「先跑一轮」线程，没有就新建）。
 *   - 截图：$M122_SHOTS/<场景>-<画面>-<1280|390>-<light|dark>.png（Z2 项目页卡片、Z4 能力区、Z5/Z6/Z7 选择卡与结果提示）。
 *
 * 与方案清单的出入：
 *   - 方案写的是假 Anthropic 端点 + 固定规则名 `m122-no-marker`；dev 上用真实模型，规则名从应答的 `data.oryn.path` 取。
 *   - Z2 写带 `Bearer ghp_…` 的 mcp.json 时保留 Z1 的 MCP server（只加 headers），免得 Z4 的 MCP 断言失去对象。
 *   - Z4 的能力以线程 rpc `get_skills` / `get_mcp_servers` 为准（硬断言）；信息面板「能力」卡显示项目层描述作软断言并截图。
 *     先放全局同名技能、再新建线程（全局配置只在 setup / resume 下发）。
 *   - Z6 在 `m122-b` 新建线程执行 /omfg（「另一线程」），再在 `m122-b` 另建一个线程核对 orb 里的全局规则文件。
 *   - Z7 只看选择卡的两项与 orb 里没有项目层目录，然后点「忽略」，不往全局写规则。
 *   - Z9 的 config.yml 写 `checkpoint: {enabled: false}`（无害的键）；「完成一轮对话」= 新线程首回合到 idle 且有助手回复。
 */
import {
	type Browser,
	type BrowserContext,
	chromium,
	type Locator,
	type Page,
	type WebSocket,
} from "playwright-core";

const IDS = ["Z1", "Z2", "Z3", "Z4", "Z5", "Z6", "Z7", "Z8", "Z9"] as const;
const USAGE = `用法：bun scripts/m122-accept.ts [--only Z1,Z5,…] [--help]
  场景：${IDS.join(" ")}
  环境变量见文件头注释（ORYN_URL、ORYN_CREDENTIALS、M122_PROVIDER、M122_REPO、M122_BRANCH、M122_SHOTS、CHROMIUM）。`;

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
const SHOTS = process.env.M122_SHOTS ?? "/tmp/oryn-shots/m122";
const PROVIDER = process.env.M122_PROVIDER ?? "local";
const REPO_URL =
	process.env.M122_REPO ?? "https://github.com/Lynricsy/oryn-setup-sandbox.git";
const BRANCH = process.env.M122_BRANCH ?? "pristine";
const DB = `${ROOT}/.dev/data/oryn.db`;
const ORYN = `${ROOT}/bin/oryn`;
const ORYND_LOG = `${ROOT}/.dev/orynd.log`;
const WEB_STATE = `${ROOT}/.dev/e2e/state.json`;
mkdirSync(SHOTS, { recursive: true });

const RUN = Math.random().toString(36).slice(2, 6);
const WORK = `/var/tmp/m122-${RUN}`;
const BOX = 'textarea[aria-label="消息"]';

const PROJ_A = "m122-a";
const PROJ_B = "m122-b";
/** orb 内项目层的位置（契约 C1）。 */
const PC_ROOT = "~/.oryn/project-config";
const PC_OMP = `${PC_ROOT}/.omp`;
const MARKER = "M122_MARKER";
const MARKER_PROMPT = `请原样只回复这一个词，不要加任何别的内容：${MARKER}`;
const OMFG_COMPLAINT = `你刚才的回复输出了 ${MARKER}。以后任何回复都不要再输出 ${MARKER} 这个词`;
/** 保存位置选择卡（`uiRequest.core.*` 的中文）。 */
const OMFG_SELECT_TITLE = "把这条规则保存到哪里？";
const L_PROJECT = "本项目（Oryn 项目配置）";
const L_GLOBAL = "全局（Oryn 中央配置）";
const L_AMEND = "修改反馈…";
const RULE_PATH_RE = /^rules\/[a-z0-9][a-z0-9_-]*\.md$/;

// ---------------------------------------------------------------- 固定内容

const Z1_SERVER = "m122-z1";
const Z1_RULE = `---\ndescription: M122 Z1 project-layer rule\n---\nM122 Z1 project-layer rule (run ${RUN}); it only verifies Oryn project config delivery.\n`;
const skillDesc = (layer: string) => `M122 z1 skill from the ${layer} layer`;
const skillDoc = (layer: string) =>
	`---\nname: z1\ndescription: ${skillDesc(layer)}\n---\nM122 acceptance skill (${layer} layer). Do nothing.\n`;
const Z1_SKILL_PROJECT = skillDoc("project");
const Z1_SKILL_GLOBAL = skillDoc("global");
const mcpDoc = (headers?: Record<string, string>) =>
	`${JSON.stringify(
		{
			mcpServers: {
				[Z1_SERVER]: {
					type: "http",
					url: "http://127.0.0.1:9/mcp",
					...(headers ? { headers } : {}),
				},
			},
		},
		null,
		2,
	)}\n`;
const Z1_MCP = mcpDoc();
const Z2_MCP = mcpDoc({
	Authorization: "Bearer ghp_M122fakeToken0123456789abcdef",
});
const Z2_RULE = "# z2\n\nM122 Z2 rule created from the project page.\n";
const Z9_CONFIG = "checkpoint:\n  enabled: false\n";
/** Z1 推送后项目层应恰有的文件。 */
const Z1_EXPECT = ["mcp.json", "rules/z1.md", "skills/z1/SKILL.md"];

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const isRecord = (v: unknown): v is Record<string, unknown> =>
	typeof v === "object" && v !== null && !Array.isArray(v);
const clip = (s: string, n = 200) =>
	s.length > n ? `${s.slice(0, n)}…（共 ${s.length} 字）` : s;
const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();

function sh(cmd: string): { code: number; out: string; stdout: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], { cwd: ROOT });
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
		stdout: r.stdout.toString().trim(),
	};
}

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

interface ApiRes {
	status: number;
	json: unknown;
	text: string;
}

/** REST 请求：`body` 为字符串时按 octet-stream 原样发送，否则按 JSON。变更请求一律带 CSRF 头。 */
async function api(
	method: string,
	path: string,
	body?: unknown,
): Promise<ApiRes> {
	const raw = typeof body === "string";
	const res = await fetch(`${BASE}${path}`, {
		method,
		headers: {
			Authorization: `Bearer ${TOKEN}`,
			"X-Oryn-CSRF": "1",
			...(body === undefined
				? {}
				: {
						"Content-Type": raw
							? "application/octet-stream"
							: "application/json",
					}),
		},
		body:
			body === undefined
				? undefined
				: raw
					? (body as string)
					: JSON.stringify(body),
		redirect: "manual",
	});
	const text = await res.text();
	let json: unknown = null;
	try {
		json = JSON.parse(text);
	} catch {
		// 空体（204）或文件原文
	}
	return { status: res.status, json, text };
}

async function getJson<T>(path: string): Promise<T> {
	const r = await api("GET", path);
	if (r.status !== 200) throw new Error(`GET ${path} → ${r.status} ${r.text}`);
	return r.json as T;
}

const errCode = (r: ApiRes) =>
	isRecord(r.json) && typeof r.json.code === "string" ? r.json.code : "";

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
const setupNotes: string[] = [];
const cleanupNotes: string[] = [];
const shotFiles: string[] = [];
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
/** 软断言：记下失败继续走（场景结尾判 FAIL）。 */
const expect = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) softFails++;
	return ok;
};
const snote = (s: string) => {
	setupNotes.push(s);
	console.log(`    [准备] ${s}`);
};

// ---------------------------------------------------------------- 项目与 Agent 配置

interface ProjectView {
	id: string;
	name: string;
	repoUrl?: string;
	baseBranch?: string;
}
interface CfgFile {
	path: string;
	size: number;
	mode: number;
	sha256: string;
}
interface WriteResult {
	warnings?: { path: string; kind: string; message: string; line?: number }[];
}
/** 配置层：某项目的项目级配置，或全局 omp-home。 */
type Scope = ProjectView | "global";

const projects = new Map<string, ProjectView>();

async function ensureProject(name: string): Promise<ProjectView> {
	const hit = projects.get(name);
	if (hit) return hit;
	const list = (await getJson<{ projects: ProjectView[] }>("/api/v1/projects"))
		.projects;
	let p = list.find((x) => x.name === name);
	if (p) {
		snote(
			`复用项目 ${name} = ${p.id}（repo ${p.repoUrl ?? "-"}，分支 ${p.baseBranch ?? "-"}）`,
		);
		if (p.repoUrl !== REPO_URL || p.baseBranch !== BRANCH)
			snote(
				`⚠ 项目 ${name} 的仓库 / 分支与预期（${REPO_URL} @ ${BRANCH}）不同，照用`,
			);
	} else {
		const r = await api("POST", "/api/v1/projects", {
			name,
			repoUrl: REPO_URL,
			baseBranch: BRANCH,
			snapshots: false,
		});
		if (r.status !== 201 || !isRecord(r.json))
			throw new Error(`POST projects ${name} → ${r.status} ${r.text}`);
		p = r.json as unknown as ProjectView;
		snote(
			`新建项目 ${name} = ${p.id}（${REPO_URL} @ ${BRANCH}，snapshots=false）`,
		);
	}
	projects.set(name, p);
	return p;
}

const scopeName = (s: Scope) => (s === "global" ? "全局" : s.name);
const cfgBase = (s: Scope) =>
	s === "global"
		? "/api/v1/agent-config/files"
		: `/api/v1/projects/${s.id}/agent-config/files`;
const cfgUrl = (s: Scope, path: string) =>
	`${cfgBase(s)}/${encodeURIComponent(path)}`;

async function cfgList(s: Scope): Promise<CfgFile[]> {
	return (await getJson<{ files: CfgFile[] }>(cfgBase(s))).files;
}
const cfgPaths = async (s: Scope) =>
	(await cfgList(s)).map((f) => f.path).sort();
const cfgGet = (s: Scope, path: string) => api("GET", cfgUrl(s, path));
async function cfgText(s: Scope, path: string): Promise<string> {
	const r = await cfgGet(s, path);
	if (r.status !== 200)
		throw new Error(`读 ${scopeName(s)} 的 ${path} → ${r.status} ${r.text}`);
	return r.text;
}
async function cfgPut(s: Scope, path: string, body: string, mode?: number) {
	return api("PUT", `${cfgUrl(s, path)}${mode ? `?mode=${mode}` : ""}`, body);
}
async function cfgPutOk(s: Scope, path: string, body: string) {
	const r = await cfgPut(s, path, body);
	if (r.status !== 200)
		throw new Error(`写 ${scopeName(s)} 的 ${path} → ${r.status} ${r.text}`);
	return r.json as WriteResult;
}
const cfgDelete = (s: Scope, path: string) => api("DELETE", cfgUrl(s, path));
/** 路径 → sha256（判断「全局列表没变」）。 */
const cfgShas = async (s: Scope) =>
	new Map((await cfgList(s)).map((f) => [f.path, f.sha256]));

/** 全局配置被本脚本改过的文件：原文与权限（orig = null 表示原来不存在）。收尾按它还原。 */
const globalTouched = new Map<string, { orig: string | null; mode: number }>();

/** 改全局 path 之前记下原状；known = 调用方事先读好的原状（规则名事后才知道的情形）。 */
async function rememberGlobal(
	path: string,
	known?: { orig: string | null; mode: number },
) {
	if (globalTouched.has(path)) return;
	if (known) {
		globalTouched.set(path, known);
		return;
	}
	const r = await cfgGet("global", path);
	if (r.status === 404) {
		globalTouched.set(path, { orig: null, mode: 0o644 });
		return;
	}
	if (r.status !== 200)
		throw new Error(`读全局 ${path} → ${r.status} ${r.text}`);
	const mode = (await cfgList("global")).find((f) => f.path === path)?.mode;
	globalTouched.set(path, { orig: r.text, mode: mode ?? 0o644 });
}

/** 把全局里的 path 还原成改动前的样子；返回一行说明。 */
async function restoreGlobal(path: string): Promise<string> {
	const g = globalTouched.get(path);
	if (!g) return `全局 ${path} 未改动`;
	globalTouched.delete(path);
	if (g.orig === null) {
		const r = await cfgDelete("global", path);
		return `删除全局 ${path} → ${r.status}`;
	}
	const r = await cfgPut("global", path, g.orig, g.mode);
	return `还原全局 ${path}（原文 ${g.orig.length} 字节）→ ${r.status}`;
}

/** 推送 Z1 的目录（含应被拒收的 models.yml 与 plans/x.md），返回 CLI 结果。 */
async function pushZ1(): Promise<CliRes> {
	const dir = `${WORK}/z1-omp`;
	rmSync(dir, { recursive: true, force: true });
	const files: Record<string, string> = {
		"rules/z1.md": Z1_RULE,
		"skills/z1/SKILL.md": Z1_SKILL_PROJECT,
		"mcp.json": Z1_MCP,
		"models.yml": "providers: {}\n",
		"plans/x.md": "# plan x\n",
	};
	for (const [rel, body] of Object.entries(files)) {
		const f = `${dir}/${rel}`;
		mkdirSync(f.slice(0, f.lastIndexOf("/")), { recursive: true });
		writeFileSync(f, body);
	}
	return cli(`config push --project ${q(PROJ_A)} ${q(dir)}`);
}

/** 后续场景要用的 Z1 文件不在（或被改过）就重新推送。 */
async function ensureZ1Files(): Promise<ProjectView> {
	const a = await ensureProject(PROJ_A);
	const paths = await cfgPaths(a);
	const fine =
		paths.includes("mcp.json") &&
		(await cfgGet(a, "rules/z1.md")).text === Z1_RULE &&
		(await cfgGet(a, "skills/z1/SKILL.md")).text === Z1_SKILL_PROJECT &&
		(await cfgText(a, "mcp.json")).includes(Z1_SERVER);
	if (!fine) {
		const r = await pushZ1();
		if (r.code !== 0)
			throw new Error(`oryn config push --project 失败：${r.out}`);
		note(`前置：已把 Z1 的文件推送到 ${PROJ_A}`);
	}
	return a;
}

// ---------------------------------------------------------------- 线程与 orb

interface Th {
	tid: string;
	orb: string;
}
const orbs = new Set<string>();
/** 当前场景最近操作的线程：失败时截它的线程页。 */
let focus = "";

interface ThreadRow {
	state: string;
	last_seq: number;
	orb_id: string | null;
}
const threadRow = (tid: string) =>
	one<ThreadRow>(
		`select state, last_seq, orb_id from threads where id='${tid}'`,
	);
const orbState = (orb: string) =>
	one<{ state: string }>(`select state from orbs where id='${orb}'`)?.state ??
	"?";
const assistantsAfter = (tid: string, beforeSeq: number) =>
	one<{ n: number }>(
		`select count(*) n from entries where thread_id='${tid}' and seq>${beforeSeq} and type='message' and instr(cast(payload as text), '"role":"assistant"')>0`,
	)?.n ?? 0;
const SETTLED: Record<string, true> = {
	idle: true,
	awaiting_input: true,
	error: true,
};
const alive = (orb: string) =>
	!["deleting", "deleted", "failed", "?"].includes(orbState(orb));

/** 等 beforeSeq 之后出现助手消息且线程静止，返回状态。 */
async function waitTurn(tid: string, beforeSeq: number, timeoutMs: number) {
	return waitFor(
		`线程 ${tid} 回合结束`,
		() => {
			const r = threadRow(tid);
			if (!r || !SETTLED[r.state] || r.last_seq <= beforeSeq) return undefined;
			return assistantsAfter(tid, beforeSeq) ? r.state : undefined;
		},
		timeoutMs,
		1000,
	);
}

async function ensureOrbReady(orb: string): Promise<void> {
	const st = orbState(orb);
	if (st === "ready") return;
	if (st === "frozen" || st === "stopped") {
		const r = await api("POST", `/api/v1/orbs/${orb}/resume`);
		note(`orb ${orb} 为 ${st}，POST resume → ${r.status}`);
	}
	await waitFor(
		`orb ${orb} ready`,
		() => orbState(orb) === "ready",
		300_000,
		1000,
	);
}

/** orb 醒着且 agent 能答 get_state。 */
async function ensureAgent(th: Th): Promise<void> {
	await ensureOrbReady(th.orb);
	await waitFor(
		"agent 可答 get_state",
		async () => (await rpc(th.tid, { type: "get_state" })).code === 0,
		180_000,
		2000,
	);
}

/** 新建线程（新 orb），等首回合到 idle；project = null 为无项目线程。 */
async function newThread(
	tag: string,
	project: string | null,
	prompt: string,
): Promise<Th> {
	const scope = project ? `--project ${q(project)}` : "--no-project";
	const out = await cli(
		`thread new ${scope} --provider ${q(PROVIDER)} --title ${q(`m122 ${RUN} ${tag}`)} ${q(prompt)}`,
	);
	if (out.code !== 0) throw new Error(`oryn thread new 失败：${out.out}`);
	const tid = out.stdout.trim().split("\n").at(-1) ?? "";
	focus = tid;
	const orb = await waitFor(
		`线程 ${tid} 分到 orb`,
		() => threadRow(tid)?.orb_id ?? undefined,
		120_000,
	);
	orbs.add(orb);
	const t0 = Date.now();
	const st = await waitTurn(tid, 0, 1_200_000);
	check(
		st === "idle",
		`${tag} 线程 ${tid}（${project ?? "无项目"}，orb ${orb}）首回合到 idle（${st}，${((Date.now() - t0) / 1000).toFixed(0)}s）`,
	);
	return { tid, orb };
}

/** 最后一条 assistant 消息的文本。 */
async function lastAssistantText(tid: string): Promise<string> {
	const last = threadRow(tid)?.last_seq ?? 0;
	const p = await getJson<{
		entries: { type: string; payload: Record<string, unknown> }[];
	}>(
		`/api/v1/threads/${tid}/entries?after=${Math.max(0, last - 40)}&limit=500`,
	);
	for (const e of [...p.entries].reverse()) {
		const m = e.type === "message" ? e.payload.message : undefined;
		if (!isRecord(m) || m.role !== "assistant") continue;
		const c = m.content;
		if (typeof c === "string") return c.trim();
		if (Array.isArray(c))
			return c
				.map((x) => (isRecord(x) && x.type === "text" ? String(x.text) : ""))
				.join("")
				.trim();
	}
	return "";
}

const orbBusy = (r: ApiRes) =>
	r.status === 502 || r.status === 503 || r.status === 504;

/** orb 文件接口（GET 读 / PUT 写，`~/…` 路径）；orb 唤醒中的 502/503/504 重试。 */
async function orbFile(th: Th, path: string, put?: string): Promise<ApiRes> {
	await ensureOrbReady(th.orb);
	const url = `/api/v1/orbs/${th.orb}/fs/file?path=${encodeURIComponent(path)}&thread=${th.tid}${put !== undefined ? "&overwrite=true" : ""}`;
	return waitFor(
		`orb ${th.orb} fs ${put !== undefined ? "PUT" : "GET"} ${path}`,
		async () => {
			const r = await api(put !== undefined ? "PUT" : "GET", url, put);
			return orbBusy(r) ? undefined : r;
		},
		120_000,
		2000,
	);
}

async function orbList(th: Th, path: string): Promise<ApiRes> {
	await ensureOrbReady(th.orb);
	return waitFor(
		`orb ${th.orb} fs list ${path}`,
		async () => {
			const r = await api(
				"GET",
				`/api/v1/orbs/${th.orb}/fs/list?path=${encodeURIComponent(path)}&thread=${th.tid}`,
			);
			return orbBusy(r) ? undefined : r;
		},
		120_000,
		2000,
	);
}

/**
 * 仓库工作区相对基线没有被项目配置层污染（含未跟踪文件）。唯一放过的改动：中央全局 mcp.json 里主人自己的
 * agent-logs MCP（mcp-agentlogs）启动时会往工作目录的 .gitignore 追加 `.agent-logs/` 一行，与 M12.2 无关。
 */
async function checkGitClean(th: Th) {
	await ensureOrbReady(th.orb);
	const r = await waitFor(
		`orb ${th.orb} git status`,
		async () => {
			const x = await api(
				"GET",
				`/api/v1/orbs/${th.orb}/git/status?thread=${th.tid}`,
			);
			return orbBusy(x) ? undefined : x;
		},
		120_000,
		2000,
	);
	const s = isRecord(r.json) ? r.json : {};
	const all = Array.isArray(s.files) ? s.files : [];
	let tolerated = "";
	const files = [];
	for (const f of all) {
		const agentLogsIgnore =
			isRecord(f) &&
			f.path === ".gitignore" &&
			f.status === "modified" &&
			f.additions === 1 &&
			f.deletions === 0 &&
			/^\.agent-logs\/?$/m.test(
				(await orbFile(th, "~/workspace/repo/.gitignore")).text,
			);
		if (agentLogsIgnore)
			tolerated = "（放过 agent-logs MCP 追加的 .gitignore 一行）";
		else files.push(f);
	}
	check(
		r.status === 200 && s.repo === true && files.length === 0,
		`orb ${th.orb} 的 git status：${r.status} repo=${String(s.repo)} root=${String(s.root ?? "-")} 改动 ${files.length} 个${files.length ? `（${clip(JSON.stringify(files), 300)}）` : ""}${tolerated}`,
	);
}

/** 中央 scope 的全部文件都以相同内容出现在 orb 的 dir 下。 */
async function checkMirror(th: Th, scope: Scope, dir: string) {
	const files = await cfgPaths(scope);
	for (const p of files) {
		const central = await cfgText(scope, p);
		const r = await orbFile(th, `${dir}/${p}`);
		check(
			r.status === 200 && r.text === central,
			`orb ${th.orb} 有 ${dir}/${p} 且与中央一致（${r.status}，${r.text.length}/${central.length} 字节）`,
		);
	}
}

/** orb_events 里 since 之后、path 对应的 agent_config_saved 审计事件。 */
async function savedEvent(orb: string, since: number, path: string) {
	return waitFor(
		`orb ${orb} 的 agent_config_saved（${path}）`,
		() =>
			sql<{ detail: string }>(
				`select detail from orb_events where orb_id='${orb}' and ts>=${since} and instr(detail, 'agent_config_saved')>0 order by id desc`,
			)
				.map((r) => JSON.parse(r.detail) as Record<string, unknown>)
				.find((d) => d.path === path),
		30_000,
		1000,
	);
}

// ---------------------------------------------------------------- 浏览器

let browser: Browser | undefined;
let bctx: BrowserContext | undefined;
const pageErrors: string[] = [];
let pageErrorsSeen = 0;

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
	if (existsSync(WEB_STATE))
		await c.addCookies(
			(
				JSON.parse(readFileSync(WEB_STATE, "utf8")) as {
					cookies: Parameters<BrowserContext["addCookies"]>[0];
				}
			).cookies,
		);
	await c.addInitScript(() => {
		localStorage.setItem("oryn.panel.width", "760");
	});
	return c;
}

async function newPage(): Promise<Page> {
	bctx ??= await makeContext();
	const p = await bctx.newPage();
	p.on("pageerror", (e) =>
		pageErrors.push(`pageerror：${oneLine(e.message).slice(0, 400)}`),
	);
	p.on("console", (m) => {
		if (m.type() === "error")
			pageErrors.push(`console.error：${oneLine(m.text()).slice(0, 400)}`);
	});
	return p;
}

/** target 已挂载且与视口相交（Playwright 的 isVisible 对视口外的已挂载节点也为真，不能用来判断）。 */
async function inViewport(p: Page, target: Locator): Promise<boolean> {
	if ((await target.count()) === 0) return false;
	const box = await target.boundingBox().catch(() => null);
	const vp = p.viewportSize();
	if (!box || !vp) return false;
	return (
		box.y < vp.height &&
		box.y + box.height > 0 &&
		box.x < vp.width &&
		box.x + box.width > 0
	);
}

/**
 * 把 target 弄进视口，返回是否做到。转录是虚拟列表：目标被后来的长条目挤出视口后会被卸载。
 * 这时把转录的滚动容器往上滚：取最后一条提示最近的、高于 300px 的可滚动祖先（长提示自身的 max-h-48 内容框也能滚）。
 * Transcript 只有在 wheel / 键盘事件里记下「用户在往上翻」后才不会在 onScroll 里弹回底部，所以先派发一次 WheelEvent 再 scrollBy。
 */
async function reveal(p: Page, target: Locator): Promise<boolean> {
	for (let i = 0; i < 20; i++) {
		const mounted = (await target.count()) > 0;
		// 每次程序化滚动前都派发一次向上的 wheel，让 Transcript 记下「用户在往上翻」；没挂载时再往上滚 400px
		const hasNotices = (await notices(p).count()) > 0;
		const moved =
			hasNotices &&
			(await notices(p)
				.last()
				.evaluate((el, scrollUp) => {
					for (let n = el.parentElement; n; n = n.parentElement) {
						const s = getComputedStyle(n).overflowY;
						if (
							(s === "auto" || s === "scroll") &&
							n.clientHeight > 300 &&
							n.scrollHeight > n.clientHeight
						) {
							n.dispatchEvent(
								new WheelEvent("wheel", { deltaY: -400, bubbles: true }),
							);
							if (!scrollUp) return true;
							const before = n.scrollTop;
							n.scrollBy(0, -400);
							return n.scrollTop !== before;
						}
					}
					return false;
				}, !mounted)
				.catch(() => false));
		if (mounted) {
			await target.scrollIntoViewIfNeeded({ timeout: 3_000 }).catch(() => {});
			await sleep(150);
			if (await inViewport(p, target)) return true;
		} else if (!moved) break;
		// 已挂载却滚不进视口：试三次就放弃（调用方抛错）
		if (mounted && i >= 2) break;
		await sleep(250);
	}
	return inViewport(p, target);
}

/**
 * 截 1280×900 与 390×844 各亮 / 暗一张，最后回到 1280 亮色。给了 target 时它必须在画面里，弄不进视口就抛错
 * （不静默拍错图）。prepare 在每次换宽度后执行，用来打开目标所在的面板（如手机布局下的信息面板）。
 */
async function shots(
	p: Page,
	name: string,
	target?: Locator,
	prepare?: (width: number) => Promise<void>,
) {
	for (const [w, h] of [
		[1280, 900],
		[390, 844],
	] as const) {
		await p.setViewportSize({ width: w, height: h });
		await sleep(700);
		if (prepare) await prepare(w);
		for (const scheme of ["light", "dark"] as const) {
			await p.emulateMedia({ colorScheme: scheme });
			await sleep(350);
			if (target && !(await reveal(p, target)))
				throw new Error(`截图 ${name}-${w}-${scheme}：目标没能滚进视口`);
			const f = `${SHOTS}/${name}-${w}-${scheme}.png`;
			await p.screenshot({ path: f });
			shotFiles.push(f);
		}
	}
	await p.setViewportSize({ width: 1280, height: 900 });
	await p.emulateMedia({ colorScheme: "light" });
	await sleep(500);
	note(`截图 ${name}-{1280,390}-{light,dark}.png`);
}

/** 信息面板 / 项目页里标题恰为 title 的卡片（`components/ui/data.tsx` 的 Card：section > header > h3）。 */
const cardSection = (page: Page, title: string) =>
	page.locator("section").filter({
		has: page.locator("header h3", { hasText: new RegExp(`^${title}$`) }),
	});

async function openTab(page: Page, tab: string) {
	const btn = page.locator(`[data-panel-tab=${tab}]`);
	await btn.waitFor({ timeout: 60_000 });
	if ((await btn.getAttribute("aria-selected")) !== "true") await btn.click();
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
async function composerAlert(p: Page): Promise<string> {
	const alert = p.locator("p[role=alert]").last();
	if ((await alert.count()) === 0) return "";
	return oneLine((await alert.textContent().catch(() => "")) ?? "");
}

/** 前端本地插入的斜杠命令结果提示（NoticeLine 的 data-notice="notice"）。 */
const notices = (p: Page) => p.locator('[data-notice="notice"]');

async function failArtifacts(id: string) {
	const log = `${SHOTS}/${id}-FAIL-${RUN}.orynd.log`;
	writeFileSync(log, sh(`tail -n 200 ${q(ORYND_LOG)}`).stdout);
	note(`orynd.log 末尾 ${log}`);
	if (!focus) return;
	try {
		const page = await newPage();
		await page
			.goto(`${BASE}/t/${focus}`, { waitUntil: "networkidle", timeout: 30_000 })
			.catch(() => {});
		await sleep(1500);
		const p = `${SHOTS}/${id}-FAIL-${RUN}.png`;
		await page.screenshot({ path: p, fullPage: true });
		shotFiles.push(p);
		note(`截图 ${p}`);
		await page.close();
	} catch (e) {
		note(`截图失败：${e instanceof Error ? e.message : String(e)}`);
	}
}

// ---------------------------------------------------------------- /omfg（浏览器）

type OmfgChoice = "project" | "global" | "dismiss";
interface OmfgRun {
	/** 保存位置选择卡上显示的选项文案。 */
	options: string[];
	/** 途中点过「是」的确认框标题。 */
	confirms: string[];
	/** omfg 应答（`response` 帧）。 */
	response: Record<string, unknown>;
	data: Record<string, unknown>;
	/** 结果提示全文。 */
	notice: string;
}

/**
 * 在线程页输入坞提交 `/omfg <吐槽>`，处理途中的确认框（点「是」），在保存位置选择卡上点 choice 对应项（dismiss = 点「忽略」），
 * 抓 WS 下行 `res` 帧里的 omfg 应答，等结果提示出现。选择卡与结果提示各截 1280/390 亮暗。
 */
async function runOmfg(
	id: string,
	th: Th,
	choice: OmfgChoice,
): Promise<OmfgRun> {
	await ensureAgent(th);
	focus = th.tid;
	const page = await newPage();
	try {
		let response: Record<string, unknown> | undefined;
		const onWs = (ws: WebSocket) =>
			ws.on("framereceived", (f) => {
				const s =
					typeof f.payload === "string" ? f.payload : f.payload.toString();
				if (!s.includes('"omfg"')) return;
				try {
					const m = JSON.parse(s) as unknown;
					if (
						isRecord(m) &&
						m.type === "res" &&
						isRecord(m.response) &&
						m.response.command === "omfg"
					)
						response = m.response;
				} catch {
					// 不是单个 JSON 帧
				}
			});
		page.on("websocket", onWs);
		await page.goto(`${BASE}/t/${th.tid}`, { waitUntil: "domcontentloaded" });
		await page.locator(BOX).waitFor({ state: "visible", timeout: 60_000 });
		await sleep(1500);
		const n0 = await notices(page).count();
		const t0 = Date.now();
		await submit(page, `/omfg ${OMFG_COMPLAINT}`);
		note(`线程 ${th.tid} 提交 /omfg ${OMFG_COMPLAINT}`);

		const select = page.getByRole("dialog", {
			name: OMFG_SELECT_TITLE,
			exact: true,
		});
		const confirm = page.locator('[role="dialog"]').filter({
			has: page.getByRole("button", { name: "是", exact: true }),
		});
		let options: string[] | undefined;
		const confirms: string[] = [];
		const deadline = Date.now() + 600_000;
		while (!response) {
			if (Date.now() > deadline)
				throw new Error(
					`10 分钟内没有收到 omfg 应答；输入坞错误行「${await composerAlert(page)}」`,
				);
			if ((await confirm.count()) > 0) {
				const d = confirm.first();
				const title = (await d.getAttribute("aria-label")) ?? "?";
				confirms.push(title);
				note(
					`确认框「${title}」（${clip(oneLine((await d.textContent()) ?? ""), 160)}）：点「是」`,
				);
				await d.getByRole("button", { name: "是", exact: true }).click();
				await sleep(1000);
				continue;
			}
			if ((await select.count()) > 0) {
				if (!options) {
					options = (await select.locator("button.text-left").allInnerTexts())
						.map((s) => s.trim().split("\n")[0]?.trim() ?? "")
						.filter(Boolean);
					note(
						`选择卡（${((Date.now() - t0) / 1000).toFixed(0)}s 后出现）：${options.map((o) => `「${o}」`).join(" ")}`,
					);
					await shots(page, `${id}-omfg-select`, select);
				}
				if (choice === "dismiss") {
					note("点「忽略」");
					await select
						.getByRole("button", { name: "忽略", exact: true })
						.click();
				} else {
					const label = choice === "project" ? L_PROJECT : L_GLOBAL;
					note(`点「${label}」`);
					await select
						.getByRole("button", { name: label, exact: true })
						.click();
				}
				await sleep(1000);
				continue;
			}
			await sleep(1000);
		}
		note(
			`omfg 应答（${((Date.now() - t0) / 1000).toFixed(0)}s）：${clip(JSON.stringify(response), 400)}`,
		);
		if (response.success === false)
			throw new Error(`omfg 失败：${String(response.error ?? "")}`);
		const data = isRecord(response.data) ? response.data : {};
		// 只看本次提交后新出现的提示里与规则有关的那条：Core 可能在同一时段发别的提示（如 xd:// 设备卸载），
		// 取「最后一条」会拿错。omfg 的各种结果文案都含「规则」。
		const pick = async () => {
			const all = notices(page);
			const count = await all.count();
			for (let i = count - 1; i >= n0; i--) {
				const text = oneLine((await all.nth(i).textContent()) ?? "");
				if (text.includes("规则")) return text;
			}
			return undefined;
		};
		const notice = await waitFor("omfg 结果提示", pick, 30_000);
		note(`结果提示：${notice}`);
		// 截图按内容定位：截图前可能又插入新提示，按下标会拍偏
		await shots(
			page,
			`${id}-omfg-result`,
			notices(page).filter({ hasText: "规则" }).last(),
		);
		return { options: options ?? [], confirms, response, data, notice };
	} finally {
		await page.close().catch(() => {});
	}
}

/** 保存成功（写回中央）的 omfg 结果：返回规则相对路径 rules/<name>.md。 */
function checkSynced(run: OmfgRun, scope: "project" | "global"): string {
	const d = run.data;
	const o = isRecord(d.oryn) ? d.oryn : {};
	check(d.state === "saved", `omfg state = saved（${String(d.state)}）`);
	const path = typeof o.path === "string" ? o.path : "";
	check(
		o.synced === true && o.scope === scope && RULE_PATH_RE.test(path),
		`data.oryn = ${JSON.stringify(o)}：synced、scope=${scope}、path 形如 rules/<name>.md`,
	);
	const saved = typeof d.savedPath === "string" ? d.savedPath : "";
	const name = path.slice("rules/".length);
	check(
		scope === "project"
			? saved === `~/.oryn/project-config/.omp/rules/${name}`
			: saved === `~/.omp/agent/rules/${name}`,
		`savedPath = ${saved}`,
	);
	return path;
}

const OMFG_FULL = [L_PROJECT, L_GLOBAL, L_AMEND];

// ---------------------------------------------------------------- 场景

/** Z1 项目配置 CRUD（CLI 推送 + REST 拒收）。 */
async function Z1() {
	const a = await ensureProject(PROJ_A);
	const b = await ensureProject(PROJ_B);
	const globalBefore = await cfgShas("global");
	const bBefore = await cfgPaths(b);
	const r = await pushZ1();
	note(
		`oryn config push --project ${PROJ_A} → ${r.code}：${clip(oneLine(r.out), 300)}`,
	);
	check(r.code === 0, "config push --project 成功");
	const aPaths = await cfgPaths(a);
	check(
		JSON.stringify(aPaths) === JSON.stringify(Z1_EXPECT),
		`${PROJ_A} 列表恰为 ${Z1_EXPECT.join(", ")}（实际 ${aPaths.join(", ")}）`,
	);
	check(
		(await cfgText(a, "rules/z1.md")) === Z1_RULE,
		"rules/z1.md 内容与推送的一致",
	);
	const bad = await cfgPut(a, "../x", "x\n");
	check(bad.status === 400, `PUT files/..%2Fx → ${bad.status} ${errCode(bad)}`);
	const cfg = await cfgPut(a, "config.yml", "- a\n");
	check(
		cfg.status === 400,
		`PUT files/config.yml（- a）→ ${cfg.status} ${errCode(cfg)}：${clip(cfg.text, 160)}`,
	);
	const aAfter = await cfgPaths(a);
	check(
		JSON.stringify(aAfter) === JSON.stringify(Z1_EXPECT),
		`被拒的写入没有落盘（${aAfter.join(", ")}）`,
	);
	const bAfter = await cfgPaths(b);
	check(
		JSON.stringify(bAfter) === JSON.stringify(bBefore) &&
			!Z1_EXPECT.some((p) => bAfter.includes(p)),
		`${PROJ_B} 列表不变且不含这些文件（${bAfter.join(", ") || "空"}）`,
	);
	const globalAfter = await cfgShas("global");
	const same =
		globalAfter.size === globalBefore.size &&
		[...globalBefore].every(([p, sha]) => globalAfter.get(p) === sha);
	check(
		same &&
			globalAfter.get("rules/z1.md") === undefined &&
			globalAfter.get("skills/z1/SKILL.md") === undefined,
		`全局列表不变（${globalAfter.size} 个文件）且没有 rules/z1.md、skills/z1/SKILL.md`,
	);
}

/** Z2 项目页「Agent 配置」卡片：新建 / 保存 / 删除 rules/z2.md；mcp.json 写入 Bearer ghp_… 出 possible_secret 警告。 */
async function Z2() {
	const a = await ensureZ1Files();
	if ((await cfgPaths(a)).includes("rules/z2.md"))
		note(
			`清掉上次残留的 rules/z2.md → ${(await cfgDelete(a, "rules/z2.md")).status}`,
		);
	const page = await newPage();
	try {
		await page.goto(`${BASE}/projects/${a.id}/settings/agent`, {
			waitUntil: "domcontentloaded",
		});
		const card = cardSection(page, "Agent 配置");
		await card.waitFor({ timeout: 60_000 });
		await card
			.locator('[role=treeitem][data-path="mcp.json"]')
			.waitFor({ timeout: 30_000 });
		note("项目页出现「Agent 配置」卡片，树里有 mcp.json");

		// 新建 rules/z2.md 并保存
		await card.getByRole("button", { name: "新建文件", exact: true }).click();
		await page
			.getByRole("dialog")
			.getByLabel("名称", { exact: true })
			.fill("rules/z2.md");
		await page
			.getByRole("dialog")
			.getByRole("button", { name: "创建", exact: true })
			.click();
		await waitFor(
			"中央列表出现 rules/z2.md",
			async () => (await cfgPaths(a)).includes("rules/z2.md"),
			20_000,
		);
		check(true, "新建后 REST 列表出现 rules/z2.md");
		const editor = card.locator(".cm-editor .cm-content");
		await editor.waitFor({ timeout: 30_000 });
		await editor.click();
		await page.keyboard.press("Control+A");
		await page.keyboard.insertText(Z2_RULE);
		await card.getByRole("button", { name: "保存", exact: true }).click();
		const z2 = await waitFor(
			"rules/z2.md 保存",
			async () => {
				const t = await cfgText(a, "rules/z2.md");
				return t.trim() === Z2_RULE.trim() ? t : undefined;
			},
			20_000,
		);
		check(true, `保存后 REST 读到 rules/z2.md（${z2.length} 字节）`);

		// mcp.json 写入带令牌的 headers。警告只存在于保存响应的前端状态里，换布局后卡片重新挂载就没了，
		// 所以 390 宽截图前在手机布局下再保存一次（内容加一个换行，保证有改动可存）。
		const saveMcp = async (text: string) => {
			// 手机布局的文件浏览器是单栏：已停在 mcp.json 的编辑视图时树不可见，直接编辑
			const row = card.locator('[role=treeitem][data-path="mcp.json"]');
			if (await row.isVisible().catch(() => false)) await row.click();
			await waitFor(
				"编辑器载入 mcp.json",
				async () => ((await editor.textContent()) ?? "").includes("mcpServers"),
				30_000,
			);
			await editor.click();
			await page.keyboard.press("Control+A");
			await page.keyboard.insertText(text);
			const putRes = page.waitForResponse(
				(r) =>
					r.request().method() === "PUT" &&
					r.url().includes("/agent-config/files/mcp.json"),
				{ timeout: 30_000 },
			);
			await card.getByRole("button", { name: "保存", exact: true }).click();
			const res = await putRes;
			const wr = (await res.json().catch(() => ({}))) as WriteResult;
			return { status: res.status(), warnings: wr.warnings ?? [] };
		};
		const first = await saveMcp(Z2_MCP);
		check(
			first.status === 200 &&
				first.warnings.some((w) => w.kind === "possible_secret"),
			`PUT mcp.json → ${first.status}，warnings ${JSON.stringify(first.warnings)}`,
		);
		const saved = await cfgText(a, "mcp.json");
		check(
			saved.includes("ghp_M122fakeToken") && saved.includes(Z1_SERVER),
			"REST 读到的 mcp.json 带 Bearer ghp_… 且保留 Z1 的 server",
		);
		const callout = card.getByText(/^mcp\.json(:\d+)? — /).first();
		await callout.waitFor({ timeout: 15_000 });
		check(
			true,
			`卡片下出现警告：${oneLine((await callout.textContent()) ?? "")}`,
		);
		await shots(page, "Z2-agent-config-card", callout, async (w) => {
			if (w >= 640 || (await callout.count()) > 0) return;
			const again = await saveMcp(`${Z2_MCP}\n`);
			check(
				again.status === 200,
				`手机布局下重新保存 mcp.json → ${again.status}`,
			);
			await callout.waitFor({ timeout: 15_000 });
		});

		// 删除 rules/z2.md：截图换过宽度后文件浏览器可能停在单栏编辑视图或目录已折叠，重新加载后从树里点
		await page.reload({ waitUntil: "domcontentloaded" });
		await card.waitFor({ timeout: 60_000 });
		const z2Row = card.locator('[role=treeitem][data-path="rules/z2.md"]');
		if (!(await z2Row.isVisible().catch(() => false)))
			await card.locator('[role=treeitem][data-path="rules"]').click();
		await z2Row.waitFor({ timeout: 30_000 });
		await z2Row.hover();
		await card
			.getByRole("button", { name: "z2.md 的操作", exact: true })
			.click();
		await page.getByRole("menuitem", { name: "删除", exact: true }).click();
		await page
			.getByRole("dialog")
			.getByRole("button", { name: "删除", exact: true })
			.click();
		await waitFor(
			"中央列表去掉 rules/z2.md",
			async () => !(await cfgPaths(a)).includes("rules/z2.md"),
			20_000,
		);
		check(true, `删除后 REST 列表：${(await cfgPaths(a)).join(", ")}`);
	} finally {
		await page.close().catch(() => {});
	}
}

let z3: Th | undefined;

/** Z3 下发：新线程的 orb 里有项目层文件、git 干净、经 orb 文件接口写项目层 → 403 protected_path。 */
async function Z3() {
	const a = await ensureZ1Files();
	const th = await newThread("z3", PROJ_A, "只回复 ok");
	z3 = th;
	const path = `${PC_OMP}/rules/z1.md`;
	const central = await cfgText(a, "rules/z1.md");
	const r = await orbFile(th, path);
	check(
		r.status === 200 && r.text === central,
		`orb fs 读 ${path} → ${r.status}，内容与中央一致（${r.text.length} 字节）`,
	);
	await checkMirror(th, a, PC_OMP);
	await checkGitClean(th);
	const w = await orbFile(th, path, "tampered\n");
	check(
		w.status === 403 && errCode(w) === "protected_path",
		`orb fs PUT ${path} → ${w.status} ${errCode(w)}`,
	);
	const again = await orbFile(th, path);
	check(again.text === central, "被拒后文件内容未变");
}

/** Z4 建的「先跑一轮」线程（首回合已回复 M122_MARKER），Z5 复用。 */
let markerA: Th | undefined;

/** Z4 生效与优先级：全局放同名技能 z1，新线程里 z1 用项目层那份；mcp.json 的 server 出现在 MCP 列表。 */
async function Z4() {
	const a = await ensureZ1Files();
	note(`${PROJ_A} 的项目层：${(await cfgPaths(a)).join(", ")}`);
	const skillPath = "skills/z1/SKILL.md";
	await rememberGlobal(skillPath);
	const before = globalTouched.get(skillPath);
	note(
		`全局 ${skillPath} 原状：${before?.orig === null ? "不存在" : `已存在（${before?.orig.length} 字节，收尾写回）`}`,
	);
	try {
		await cfgPutOk("global", skillPath, Z1_SKILL_GLOBAL);
		note(`全局写入 ${skillPath}（description「${skillDesc("global")}」）`);
		const th = await newThread("z4-marker", PROJ_A, MARKER_PROMPT);
		markerA = th;
		note(`首回合回复：${clip(await lastAssistantText(th.tid), 120)}`);

		const sk = await rpc(th.tid, { type: "get_skills" });
		check(sk.code === 0, `get_skills → ${sk.code}`);
		const skills = (
			isRecord(sk.data) && Array.isArray(sk.data.skills) ? sk.data.skills : []
		).filter(isRecord);
		const z1s = skills.filter((s) => s.name === "z1");
		note(
			`get_skills 里的 z1：${JSON.stringify(z1s.map((s) => ({ description: s.description, source: s.source, enabled: s.enabled, location: s.location })))}`,
		);
		const winner = z1s.find((s) => s.enabled === true) ?? z1s[0];
		check(
			winner?.description === skillDesc("project") &&
				String(winner.location ?? "").includes("/.oryn/project-config/.omp/"),
			`生效的 z1 是项目层那份（description「${String(winner?.description)}」，${String(winner?.location)}）`,
		);
		check(
			!z1s.some(
				(s) => s.enabled === true && s.description === skillDesc("global"),
			),
			"全局那份 z1 没有生效",
		);

		const mc = await rpc(th.tid, { type: "get_mcp_servers" });
		check(mc.code === 0, `get_mcp_servers → ${mc.code}`);
		const servers = (
			isRecord(mc.data) && Array.isArray(mc.data.servers) ? mc.data.servers : []
		).filter(isRecord);
		const srv = servers.find((s) => s.name === Z1_SERVER);
		check(
			srv !== undefined,
			`MCP 列表有 ${Z1_SERVER}（${JSON.stringify(srv ?? null)}；共 ${servers.length} 个：${servers.map((s) => String(s.name)).join(", ")}）`,
		);

		// 界面：信息面板「能力」卡（软断言 + 截图）
		const page = await newPage();
		try {
			await page.goto(`${BASE}/t/${th.tid}`, { waitUntil: "domcontentloaded" });
			await openTab(page, "info");
			const sec = cardSection(page, "能力");
			await sec.waitFor({ timeout: 60_000 });
			const btn = sec.locator("header h3 button");
			if ((await btn.getAttribute("aria-expanded")) !== "true")
				await btn.click();
			const shown = await sec
				.getByText(skillDesc("project"))
				.first()
				.waitFor({ timeout: 60_000 })
				.then(() => true)
				.catch(() => false);
			expect(
				shown,
				`「能力」卡显示 z1 的项目层描述「${skillDesc("project")}」`,
			);
			expect(
				(await sec.getByText(Z1_SERVER).count()) > 0,
				`「能力」卡列出 MCP server ${Z1_SERVER}`,
			);
			// 手机布局里信息面板默认不展开：从标题菜单的 orb 行打开（无 box 时点行直达信息页）
			await shots(page, "Z4-capabilities", sec, async (w) => {
				if (w >= 640 || (await sec.isVisible().catch(() => false))) return;
				await page.getByTestId("thread-title").click();
				await page
					.getByRole("menuitem")
					.filter({ hasText: /^orb/ })
					.first()
					.click();
				await sec.waitFor({ timeout: 15_000 });
			});
		} finally {
			await page.close().catch(() => {});
		}
	} finally {
		note(await restoreGlobal(skillPath));
	}
}

/** Z5 /omfg → 本项目。 */
async function Z5() {
	const a = await ensureProject(PROJ_A);
	if (!markerA || !alive(markerA.orb))
		markerA = await newThread("z5-marker", PROJ_A, MARKER_PROMPT);
	const th = markerA;
	focus = th.tid;
	const globalBefore = await cfgShas("global");
	const since = Date.now();
	const run = await runOmfg("Z5", th, "project");
	check(
		JSON.stringify(run.options) === JSON.stringify(OMFG_FULL),
		`选择卡三项恰为 ${OMFG_FULL.map((o) => `「${o}」`).join("")}`,
	);
	if (run.confirms.length) note(`途中确认：${run.confirms.join("、")}`);
	const path = checkSynced(run, "project");
	check(
		run.notice.includes(`规则已保存到本项目的 Agent 配置（${path}）`),
		`结果提示含「规则已保存到本项目的 Agent 配置（${path}）」`,
	);
	const aPaths = await cfgPaths(a);
	check(aPaths.includes(path), `${PROJ_A} 列表含 ${path}`);
	const globalAfter = await cfgShas("global");
	check(
		globalAfter.get(path) === globalBefore.get(path),
		globalBefore.has(path)
			? `全局原有同名的 ${path}，内容没有被改动`
			: `全局列表不含 ${path}`,
	);
	const rule = await cfgText(a, path);
	note(`规则内容：${clip(oneLine(rule), 300)}`);
	const local = await orbFile(th, `${PC_OMP}/${path}`);
	check(
		local.status === 200 && local.text === rule,
		"当前 orb 的项目层里也是这份规则",
	);
	await checkGitClean(th);
	const ev = await savedEvent(th.orb, since, path);
	check(
		ev.scope === "project" && ev.projectId === a.id && ev.threadId === th.tid,
		`orb_events agent_config_saved：${JSON.stringify(ev)}`,
	);
	const th2 = await newThread("z5-second", PROJ_A, "只回复 ok");
	const r2 = await orbFile(th2, `${PC_OMP}/${path}`);
	check(
		r2.status === 200 && r2.text === rule,
		`第二个线程的 orb ${th2.orb} 有 ${PC_OMP}/${path}（${r2.status}）`,
	);
}

/** Z6 /omfg → 全局（在 m122-b 的线程里）。 */
async function Z6() {
	const b = await ensureProject(PROJ_B);
	// 改动前的全局 rules/*（规则名事先不知道，原文与权限全部记下，存盘后按实际路径登记还原）
	const before = new Map<string, { orig: string; mode: number }>();
	for (const f of await cfgList("global"))
		if (f.path.startsWith("rules/"))
			before.set(f.path, {
				orig: await cfgText("global", f.path),
				mode: f.mode,
			});
	note(`全局原有规则 ${before.size} 条`);
	const th = await newThread("z6-marker", PROJ_B, MARKER_PROMPT);
	const since = Date.now();
	const run = await runOmfg("Z6", th, "global");
	check(
		JSON.stringify(run.options) === JSON.stringify(OMFG_FULL),
		`选择卡三项恰为 ${OMFG_FULL.map((o) => `「${o}」`).join("")}`,
	);
	if (run.confirms.length) note(`途中确认：${run.confirms.join("、")}`);
	const d = run.data;
	const o = isRecord(d.oryn) ? d.oryn : {};
	if (typeof o.path === "string" && RULE_PATH_RE.test(o.path))
		await rememberGlobal(
			o.path,
			before.get(o.path) ?? { orig: null, mode: 0o644 },
		);
	const path = checkSynced(run, "global");
	check(
		run.notice.includes(`规则已保存到全局 Agent 配置（${path}）`),
		`结果提示含「规则已保存到全局 Agent 配置（${path}）」`,
	);
	check(
		(await cfgPaths("global")).includes(path),
		`全局列表含 ${path}（收尾${before.has(path) ? "写回原文" : "删除"}）`,
	);
	const rule = await cfgText("global", path);
	note(`规则内容：${clip(oneLine(rule), 300)}`);
	check(
		!(await cfgPaths(b)).includes(path),
		`${PROJ_B} 的项目层没有写入 ${path}`,
	);
	const ev = await savedEvent(th.orb, since, path);
	check(
		ev.scope === "global" && ev.projectId === undefined,
		`orb_events agent_config_saved：${JSON.stringify(ev)}`,
	);
	const th2 = await newThread("z6-check", PROJ_B, "只回复 ok");
	const r2 = await orbFile(th2, `~/.omp/agent/${path}`);
	check(
		r2.status === 200 && r2.text === rule,
		`${PROJ_B} 新线程的 orb ${th2.orb} 有 ~/.omp/agent/${path}（${r2.status}）`,
	);
}

/** Z7 无项目线程：选择卡只有全局与修改两项，orb 里没有项目层目录。 */
async function Z7() {
	const th = await newThread("z7-noproject", null, MARKER_PROMPT);
	const ls = await orbList(th, "~/.oryn");
	const names =
		isRecord(ls.json) && Array.isArray(ls.json.entries)
			? ls.json.entries.filter(isRecord).map((e) => String(e.name))
			: [];
	check(
		ls.status === 200 && !names.includes("project-config"),
		`orb ${th.orb} 的 ~/.oryn 没有 project-config（${ls.status}：${names.join(", ")}）`,
	);
	const st = await api(
		"GET",
		`/api/v1/orbs/${th.orb}/fs/stat?path=${encodeURIComponent(PC_ROOT)}&thread=${th.tid}`,
	);
	check(st.status === 404, `fs/stat ${PC_ROOT} → ${st.status} ${errCode(st)}`);
	const run = await runOmfg("Z7", th, "dismiss");
	check(
		JSON.stringify(run.options) === JSON.stringify([L_GLOBAL, L_AMEND]),
		`选择卡恰为「${L_GLOBAL}」「${L_AMEND}」（实际 ${run.options.map((x) => `「${x}」`).join("")}）`,
	);
	note(`忽略后 state=${String(run.data.state)}，提示「${run.notice}」`);
}

/** Z8 删除传播：删中央 rules/z1.md；Z3 的 orb 冻结后唤醒，新线程的 orb 也没有它，其余文件还在。 */
async function Z8() {
	if (!z3 || !alive(z3.orb)) await Z3();
	const th = z3;
	if (!th) throw new Error("没有 Z3 的 orb");
	focus = th.tid;
	const a = await ensureProject(PROJ_A);
	const z1Path = `${PC_OMP}/rules/z1.md`;
	const pre = await orbFile(th, z1Path);
	check(pre.status === 200, `删除前 Z3 的 orb 有 ${z1Path}`);
	const del = await cfgDelete(a, "rules/z1.md");
	check(del.status === 204, `DELETE ${PROJ_A} 的 rules/z1.md → ${del.status}`);
	note(`中央剩余：${(await cfgPaths(a)).join(", ")}`);

	await ensureOrbReady(th.orb);
	const p = await api("POST", `/api/v1/orbs/${th.orb}/pause`, {
		mode: "freeze",
	});
	check(
		p.status === 202,
		`POST orbs/${th.orb}/pause {mode: freeze} → ${p.status}`,
	);
	await waitFor(
		`orb ${th.orb} frozen`,
		() => orbState(th.orb) === "frozen",
		180_000,
		1000,
	);
	note(`orb ${th.orb} 已冻结`);
	const r = await api("POST", `/api/v1/orbs/${th.orb}/resume`);
	check(r.status === 202, `POST orbs/${th.orb}/resume → ${r.status}`);
	await waitFor(
		`orb ${th.orb} ready`,
		() => orbState(th.orb) === "ready",
		300_000,
		1000,
	);
	note(`orb ${th.orb} 已唤醒`);
	const gone = await orbFile(th, z1Path);
	check(
		gone.status === 404,
		`唤醒后 ${z1Path} → ${gone.status} ${errCode(gone)}`,
	);
	await checkMirror(th, a, PC_OMP);

	const th2 = await newThread("z8-new", PROJ_A, "只回复 ok");
	const gone2 = await orbFile(th2, z1Path);
	check(
		gone2.status === 404,
		`新线程的 orb ${th2.orb} 上 ${z1Path} → ${gone2.status} ${errCode(gone2)}`,
	);
	await checkMirror(th2, a, PC_OMP);
}

/** Z9 合法 config.yml 不会拖垮 agent。 */
async function Z9() {
	const a = await ensureProject(PROJ_A);
	const w = await cfgPutOk(a, "config.yml", Z9_CONFIG);
	note(
		`PUT ${PROJ_A} 的 config.yml（${oneLine(Z9_CONFIG)}）→ warnings ${JSON.stringify(w.warnings ?? [])}`,
	);
	const th = await newThread("z9-config", PROJ_A, "只回复 ok");
	const text = await lastAssistantText(th.tid);
	check(text.length > 0, `首回合有助手回复：「${clip(text, 120)}」`);
	const r = await orbFile(th, `${PC_OMP}/config.yml`);
	check(
		r.status === 200 && r.text === Z9_CONFIG,
		`orb 里有 ${PC_OMP}/config.yml（${r.status}）`,
	);
	const st = await rpc(th.tid, { type: "get_state" });
	check(st.code === 0, `agent 仍可答 get_state（${st.code}）`);
}

const TESTS: [string, () => Promise<void>][] = [
	["Z1", Z1],
	["Z2", Z2],
	["Z3", Z3],
	["Z4", Z4],
	["Z5", Z5],
	["Z6", Z6],
	["Z7", Z7],
	["Z8", Z8],
	["Z9", Z9],
];

// ---------------------------------------------------------------- 收尾

let cleaned = false;
async function cleanup() {
	if (cleaned) return;
	cleaned = true;
	const cn = (s: string) => {
		cleanupNotes.push(s);
		console.log(`    · ${s}`);
	};
	for (const path of [...globalTouched.keys()])
		cn(
			await restoreGlobal(path).catch(
				(e) => `还原全局 ${path} 失败：${String(e)}`,
			),
		);
	for (const p of projects.values()) {
		try {
			const files = await cfgPaths(p);
			for (const f of files) {
				const r = await cfgDelete(p, f);
				if (r.status !== 204) cn(`删除 ${p.name} 的 ${f} → ${r.status}`);
			}
			cn(
				`清空 ${p.name} 的项目级配置（${files.length} 个文件；项目本身保留以便复用）→ 剩 ${(await cfgPaths(p)).length} 个`,
			);
		} catch (e) {
			cn(`清空 ${p.name} 的项目级配置失败：${String(e)}`);
		}
	}
	const left = [...orbs].filter(
		(o) => !["deleting", "deleted", "?"].includes(orbState(o)),
	);
	for (const o of left) {
		const r = await api("DELETE", `/api/v1/orbs/${o}`).catch(
			(e) => ({ status: 0, json: null, text: String(e) }) as ApiRes,
		);
		cn(`删除 orb ${o} → ${r.status}`);
	}
	await Promise.all(
		left.map((o) =>
			waitFor(
				`${o} deleted`,
				() => orbState(o) === "deleted",
				180_000,
				1000,
			).catch((e) => cn(`${o} 未删干净：${String(e)}`)),
		),
	);
	rmSync(WORK, { recursive: true, force: true });
	await browser?.close().catch(() => {});
}

for (const s of ["SIGINT", "SIGTERM"] as const)
	process.on(s, () => {
		console.log(`\n收到 ${s}，清理后退出`);
		void cleanup().finally(() => process.exit(130));
	});

// ---------------------------------------------------------------- 主流程

console.log(
	`M12.2 验收 run=${RUN} base=${BASE} provider=${PROVIDER} 仓库 ${REPO_URL}@${BRANCH} shots=${SHOTS}`,
);
mkdirSync(WORK, { recursive: true });

try {
	for (const [id, fn] of TESTS) {
		if (ONLY && !ONLY.has(id)) continue;
		evidence = [];
		softFails = 0;
		focus = "";
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
		// 页面未捕获的异常算失败；console.error 里有预期的 HTTP 错误日志（如 400 / 403 断言），只记录不判负
		const uncaught = errs.filter((e) => e.startsWith("pageerror："));
		if (uncaught.length > 0) {
			note(`✗ 页面有 ${uncaught.length} 个未捕获异常`);
			verdict = "FAIL";
		}
		if (verdict === "FAIL") await failArtifacts(id);
		console.log(`${id} ${verdict} (${took()}s)`);
		results.push({ id, verdict, seconds: took(), evidence });
	}
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
			projects: [...projects.values()],
			orbs: [...orbs],
			setup: setupNotes,
			cleanup: cleanupNotes,
			results,
			shots: shotFiles,
		},
		null,
		2,
	),
);
console.log(`\n汇总（${out}）`);
for (const r of results) console.log(`${r.id} ${r.verdict} ${r.seconds}s`);
process.exit(results.some((r) => r.verdict === "FAIL") ? 1 : 0);
