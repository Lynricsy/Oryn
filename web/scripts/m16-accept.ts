#!/usr/bin/env bun
import {
	chmodSync,
	copyFileSync,
	existsSync,
	mkdirSync,
	openSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import type { Subprocess } from "bun";

/**
 * M16 真机验收（docs/plan-m16.md 步骤 5.2）：真实的 Hindsight（deploy/hindsight/compose.yaml）、真实的 docker orb
 * （钉住的 Core build/core/x64/omp）、本机 runner 与真实的计费模型，逐项记录 R1、R4、R5、R6、R8、R13、R20、R23。
 * 网关的伪造标签 / 伪造 bank、放行清单、开关与待重启、模型中转细节等由 Go 端到端覆盖：
 * `go test ./internal/server/ -run TestE2EM16`。
 *
 * 用法（web/ 目录）：bun run accept:m16 [-- --help]
 *
 * 自建一套隔离环境（全部在 /var/tmp/oryn-m16-<run>/ 下，结束时删除；M16_KEEP=1 保留，日志在 logs/）：
 *   - orynd（data_dir 全新）监听 127.0.0.1:7670 与 docker0 网关地址（`docker network inspect bridge` 取得，本机
 *     172.18.0.1）的 7670；`public_url` 为 `http://host.docker.internal:7670`。Hindsight 的 `.env` 里的中转地址以
 *     public_url 为前缀，compose 给 api 容器配了 `host.docker.internal:host-gateway`，host-gateway 就是 docker0 的
 *     网关地址，所以容器经它访问到 orynd 的 `/memory-relay/`；orb 的 link（`orb_url`）也走这个地址。
 *   - provider `local`（driver docker）：镜像是在 $M16_BASE_IMAGE（缺省 oryn-orb:dev）上只替换三个二进制的薄层
 *     `oryn-orb:m16accept-<run>`（$M16_BIN 的 orbd / oryn 与 build/core/x64/omp，构建时跑一遍 omp 冒烟），结束时删除。
 *     基础镜像只提供系统工具，M16 相关的代码全部来自这次换进去的二进制。
 *   - Agent 配置：把 $M16_MODELS（缺省 ~/.omp/agent/models.yml）原样 PUT 成 omp-home 的 models.yml（密钥由 orynd 抽取
 *     进密钥库），orb 线程用 $M16_AGENT_MODEL（缺省 claude-pro/claude-sonnet-5-5）。协调宿主的 omp_path 指向钉住的
 *     Core（中央模型目录靠它运行），目录固定为 $M16_COORD_DIR（root 运行时宿主账号按目录派生）。
 *   - 记忆配置全走 REST：Hindsight 地址 http://127.0.0.1:8888 并打开开关；嵌入接口 = 本机 TEI
 *     （$M16_EMBED_URL，缺省 http://127.0.0.1:18090；模型名取它的 `/info`，1024 维，无 key）；不配重排（计划 v5：rrf）；
 *     Hindsight 默认模型 claude-pro/claude-haiku-5-5（主人决定；$M16_HINDSIGHT_MODEL 可改）。生成的 `.env` 写到
 *     deploy/hindsight/.env（0600），compose 以项目名 oryn-hindsight-m16accept 启动；结束时（含失败与 Ctrl-C）
 *     `down -v` 删除容器与数据卷（主人决定），再删除 `.env`。已有 deploy/hindsight/.env 或 8888 被占用时直接退出。
 *   - runner：`POST /api/v1/runners` 后以 `orbd run --runner --dir <tmp>/runner-dir` 起在后台（HOME 在临时目录下，
 *     models.yml 同上拷一份，omp = 钉住的 Core，记忆通道端口随机：ORYN_EGRESS_ADDR=127.0.0.1:0）。
 * 前提：本机有 docker（含 compose 插件）且能构建镜像；`make build`（或 M16_BIN 指向含 orynd / orbd / oryn 的目录）；
 *   build/core/x64/omp 与 core/source.json 的 sha256 一致（scripts/fetch-core.sh）；TEI 在跑；models.yml 里有上面两个模型。
 *
 * agent 的不确定性：提示词点名要调用的工具（retain / recall）与参数；记忆状态尽量经 REST 断言（document、候选、
 * 召回搜索），召回再看转录（助手回复与 recall 工具结果）；没按要求调用工具或没召回到时，同一线程补发一次提示。
 *
 * 每条打印 `M16 R<n> PASS|FAIL（秒）` 与证据行，最后汇总；全部 PASS 时退出码 0。
 */

const RS = ["R1", "R4", "R5", "R6", "R8", "R13", "R20", "R23"] as const;
type Req = (typeof RS)[number];
const USAGE = `用法：bun scripts/m16-accept.ts [--help]
  需求：${RS.join(" ")}
  环境变量：M16_BIN（缺省 <仓库>/bin）、M16_KEEP=1（保留临时目录）、M16_BASE_IMAGE（缺省 oryn-orb:dev）、
  M16_MODELS（缺省 ~/.omp/agent/models.yml）、M16_AGENT_MODEL（缺省 claude-pro/claude-sonnet-5-5）、
  M16_HINDSIGHT_MODEL（缺省 claude-pro/claude-haiku-5-5）、M16_EMBED_URL（缺省 http://127.0.0.1:18090）、
  M16_COORD_DIR（缺省 /var/tmp/oryn-m16-coord）。`;
if (process.argv.includes("--help") || process.argv.includes("-h")) {
	console.log(USAGE);
	process.exit(0);
}

const ROOT = `${import.meta.dir}/../..`;
const BIN = process.env.M16_BIN ?? `${ROOT}/bin`;
const KEEP = process.env.M16_KEEP === "1";
const RUN = Math.random().toString(36).slice(2, 6);
const TMP = `/var/tmp/oryn-m16-${RUN}`;
const LOGS = `${TMP}/logs`;
const API_PORT = 7670;
const API = `http://127.0.0.1:${API_PORT}`;
/** Hindsight 容器经 host-gateway 访问 orynd：`.env` 的中转地址以它为前缀。 */
const PUBLIC = `http://host.docker.internal:${API_PORT}`;
const HS = "http://127.0.0.1:8888";
const COMPOSE_FILE = `${ROOT}/deploy/hindsight/compose.yaml`;
const ENV_FILE = `${ROOT}/deploy/hindsight/.env`;
const COMPOSE_PROJECT = "oryn-hindsight-m16accept";
const OMP = `${ROOT}/build/core/x64/omp`;
const BASE_IMAGE = process.env.M16_BASE_IMAGE ?? "oryn-orb:dev";
const IMAGE = `oryn-orb:m16accept-${RUN}`;
const MODELS_SRC =
	process.env.M16_MODELS ??
	`${process.env.HOME ?? "/root"}/.omp/agent/models.yml`;
const AGENT_MODEL =
	process.env.M16_AGENT_MODEL ?? "claude-pro/claude-sonnet-5-5";
const HS_MODEL =
	process.env.M16_HINDSIGHT_MODEL ?? "claude-pro/claude-haiku-5-5";
const EMBED = (process.env.M16_EMBED_URL ?? "http://127.0.0.1:18090").replace(
	/\/+$/,
	"",
);
const COORD_DIR = process.env.M16_COORD_DIR ?? "/var/tmp/oryn-m16-coord";
const IS_ROOT = process.getuid?.() === 0;

/** 项目 P 的约定（R5 / R13）与全局偏好（R8 / R20 / R6）：断言只看这两个罕见词。 */
const P_MARK = "翡翠蜗牛";
const P_FACT = `本项目约定：所有 Go 源文件的第一行注释必须写「${P_MARK}守夜」这六个字。`;
const P_ASK =
	"本项目对 Go 源文件的第一行注释有什么约定？请先调用 recall 工具查询记忆（query 用「Go 源文件 第一行注释 约定」），只根据记忆回答，并原样引用约定里的字词；查不到就回答「没有找到相关记忆」。不要读写任何文件，也不要运行命令。";
const P_NUDGE =
	"请现在调用 recall 工具（query 用「Go 源文件 第一行注释 必须写」），然后只根据召回结果、原样引用约定里的字词回答。";
const G_MARK = "琥珀松鼠";
const G_FACT = `我的个人偏好：所有代码评审意见都要以「${G_MARK}」四个字开头。`;
const G_ASK =
	"我对代码评审意见有什么个人偏好？请先调用 recall 工具查询记忆（query 用「代码评审意见 个人偏好 开头」），只根据记忆回答，并原样引用偏好里的字词；查不到就回答「没有找到相关记忆」。不要读写任何文件，也不要运行命令。";
const G_NUDGE =
	"请现在调用 recall 工具（query 用「代码评审意见 以什么开头」），然后只根据召回结果、原样引用偏好里的字词回答。";

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();
const clip = (s: string, n = 240) =>
	s.length > n ? `${s.slice(0, n)}…（共 ${s.length} 字）` : s;
const isRecord = (v: unknown): v is Record<string, unknown> =>
	typeof v === "object" && v !== null && !Array.isArray(v);
const sec = (ms: number) => `${(ms / 1000).toFixed(0)}s`;

function sh(
	cmd: string,
	env?: Record<string, string | undefined>,
): { code: number; out: string; stdout: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], {
		cwd: ROOT,
		env: env ?? process.env,
	});
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
		stdout: r.stdout.toString().trim(),
	};
}

/** 本次验收的 compose 项目（项目名隔离，不碰主人自己的 oryn-hindsight）。 */
const dc = (args: string) =>
	sh(`docker compose -p ${COMPOSE_PROJECT} -f ${q(COMPOSE_FILE)} ${args}`);

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
			if (v) return v as T;
		} catch (e) {
			last = e;
		}
		await sleep(intervalMs);
	}
	throw new Error(
		`等待超时（${timeoutMs}ms）：${what}${last ? `；最后错误：${last instanceof Error ? last.message : String(last)}` : ""}`,
	);
}

// ---------------------------------------------------------------- 结果

const note = (s: string) => console.log(`    · ${s}`);
let stepFails = 0;
/** 硬断言：不成立就结束本条（以及其后依赖它的条目）。 */
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};
/** 软断言：记下失败继续走，本条结尾判 FAIL。 */
const expect = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) stepFails++;
};

const verdicts = new Map<Req, { ok: boolean; why: string }>();
let envReady = false;

/** 跑一条需求；needs 里任一条未 PASS（或环境没起来）则直接判 FAIL，不执行。 */
async function req(
	r: Req,
	title: string,
	needs: Req[],
	fn: () => Promise<void>,
) {
	console.log(`M16 ${r} ${title}`);
	const blocked = needs.filter((n) => !verdicts.get(n)?.ok);
	if (!envReady || blocked.length) {
		const why = !envReady ? "环境未就绪" : `前置 ${blocked.join("、")} 未通过`;
		note(`✗ ${why}，不执行`);
		verdicts.set(r, { ok: false, why });
		console.log(`M16 ${r} FAIL（${why}）`);
		return;
	}
	stepFails = 0;
	const t0 = Date.now();
	let why = "";
	try {
		await fn();
	} catch (e) {
		why = e instanceof Error ? e.message : String(e);
		note(`✗ ${why}`);
		stepFails++;
	}
	const ok = stepFails === 0;
	verdicts.set(r, { ok, why: why || (ok ? "" : `${stepFails} 项软断言失败`) });
	console.log(`M16 ${r} ${ok ? "PASS" : "FAIL"}（${sec(Date.now() - t0)}）`);
}

// ---------------------------------------------------------------- 后台进程

interface Bg {
	name: string;
	proc: Subprocess;
}
const bgs: Bg[] = [];

function bg(
	name: string,
	cmd: string[],
	env?: Record<string, string | undefined>,
	cwd = ROOT,
): Bg {
	mkdirSync(LOGS, { recursive: true });
	const fd = openSync(`${LOGS}/${name}.log`, "a");
	const proc = Bun.spawn(cmd, {
		cwd,
		env: env ?? process.env,
		stdin: "ignore",
		stdout: fd,
		stderr: fd,
	});
	const b = { name, proc };
	bgs.push(b);
	return b;
}

// ---------------------------------------------------------------- HTTP

let token = "";

async function http(
	method: string,
	url: string,
	body?: unknown,
	headers: Record<string, string> = {},
) {
	const raw = typeof body === "string";
	const res = await fetch(url, {
		method,
		headers: {
			...headers,
			...(body === undefined
				? {}
				: {
						"Content-Type": raw
							? "application/octet-stream"
							: "application/json",
					}),
		},
		body: body === undefined ? undefined : raw ? body : JSON.stringify(body),
		redirect: "manual",
	});
	const text = await res.text();
	let json: unknown = null;
	try {
		json = JSON.parse(text);
	} catch {
		// 空体
	}
	return { status: res.status, text, json };
}
const api = (method: string, path: string, body?: unknown) =>
	http(method, `${API}${path}`, body, {
		Authorization: `Bearer ${token}`,
		"X-Oryn-CSRF": "1",
	});
async function rjson<T>(
	method: string,
	path: string,
	body?: unknown,
): Promise<T> {
	const r = await api(method, path, body);
	if (r.status >= 300)
		throw new Error(`${method} ${path} → ${r.status} ${clip(r.text)}`);
	return r.json as T;
}

/** 直接读 Hindsight（key 取自生成的 `.env`，只在内存里，不打印）。 */
let hsKey = "";
async function hsJson<T>(path: string): Promise<T> {
	const r = await http("GET", `${HS}${path}`, undefined, {
		Authorization: `Bearer ${hsKey}`,
	});
	if (r.status >= 300)
		throw new Error(`Hindsight GET ${path} → ${r.status} ${clip(r.text)}`);
	return r.json as T;
}

const cliEnv = () => ({
	...process.env,
	ORYN_API_URL: API,
	ORYN_API_TOKEN: token,
});
const cli = (args: string) => sh(`${q(`${BIN}/oryn`)} ${args}`, cliEnv());

// ---------------------------------------------------------------- 记忆 REST

interface MemoryStatus {
	state: string;
	version?: string;
	testedMinor: string;
	versionMismatch: boolean;
	memoryCount?: number;
	pendingDeletes: number;
	candidates: number;
	embedLocked: boolean;
	error?: string;
}
interface MemoryDocument {
	id: string;
	projectId?: string;
	threadId?: string;
	preview: string;
	content?: string;
}
interface MemoryCandidate {
	id: string;
	content: string;
	sourceThreadId?: string;
}
interface SearchHit {
	text: string;
	documentId?: string;
	global?: boolean;
}

const memStatus = () => rjson<MemoryStatus>("GET", "/api/v1/memory/status");
const projectDocs = async (pid: string) =>
	(
		await rjson<{ items: MemoryDocument[] }>(
			"GET",
			`/api/v1/memory/documents?projectId=${encodeURIComponent(pid)}&limit=100`,
		)
	).items;
const globalDocs = async () =>
	(
		await rjson<{ items: MemoryDocument[] }>(
			"GET",
			"/api/v1/memory/documents?global=true&limit=100",
		)
	).items;
const candidates = async () =>
	(
		await rjson<{ items: MemoryCandidate[] }>(
			"GET",
			"/api/v1/memory/candidates",
		)
	).items;
const search = async (text: string, pid?: string) =>
	(
		await rjson<{ items: SearchHit[] }>(
			"GET",
			`/api/v1/memory/search?q=${encodeURIComponent(text)}${pid ? `&projectId=${encodeURIComponent(pid)}` : ""}`,
		)
	).items;
const docText = (d: MemoryDocument) => `${d.preview}\n${d.content ?? ""}`;

// ---------------------------------------------------------------- 线程

interface ThreadView {
	id: string;
	state: string;
	orbId?: string;
	projectId?: string;
}
interface Entry {
	seq: number;
	type: string;
	payload: Record<string, unknown>;
}
interface ToolCall {
	name: string;
	args: unknown;
}
interface Transcript {
	/** 带文本的助手消息 */
	replies: string[];
	calls: ToolCall[];
	/** 工具结果：toolName → 文本 */
	results: { tool: string; text: string; isError: boolean }[];
}

const getThread = (id: string) =>
	rjson<ThreadView>("GET", `/api/v1/threads/${id}`);

async function entries(tid: string, after = 0): Promise<Entry[]> {
	const out: Entry[] = [];
	let cur = after;
	for (let i = 0; i < 100; i++) {
		const p = await rjson<{ entries: Entry[]; hasMore: boolean }>(
			"GET",
			`/api/v1/threads/${tid}/entries?after=${cur}&limit=500`,
		);
		out.push(...p.entries);
		const last = p.entries.at(-1);
		if (!p.hasMore || !last) break;
		cur = last.seq;
	}
	return out;
}

function textOf(c: unknown): string {
	if (typeof c === "string") return c;
	if (!Array.isArray(c)) return "";
	return c
		.map((x) => (isRecord(x) && x.type === "text" ? String(x.text ?? "") : ""))
		.join("\n");
}

/**
 * 工具调用与结果。记忆工具（retain / recall）在 orb 里是 xd:// 设备：agent 以 `write xd://<工具>` 调用，参数是
 * content 里的 JSON（`read xd://<工具>` 只是读文档，不算调用）；结果的 `details.xdev.tool` 是真实工具名。
 */
function transcriptOf(es: Entry[]): Transcript {
	const t: Transcript = { replies: [], calls: [], results: [] };
	for (const e of es) {
		const m = e.type === "message" ? e.payload.message : undefined;
		if (!isRecord(m)) continue;
		if (m.role === "assistant") {
			const text = textOf(m.content).trim();
			if (text) t.replies.push(text);
			if (!Array.isArray(m.content)) continue;
			for (const b of m.content) {
				if (!isRecord(b) || b.type !== "toolCall") continue;
				const a = isRecord(b.arguments) ? b.arguments : {};
				const dev =
					b.name === "write" && typeof a.path === "string"
						? /^xd:\/\/([\w-]+)$/.exec(a.path)?.[1]
						: undefined;
				let args: unknown = b.arguments;
				if (dev)
					try {
						args = JSON.parse(String(a.content ?? ""));
					} catch {
						args = a.content;
					}
				t.calls.push({ name: dev ?? String(b.name ?? ""), args });
			}
		} else if (m.role === "toolResult") {
			const xdev =
				isRecord(m.details) && isRecord(m.details.xdev)
					? m.details.xdev
					: undefined;
			t.results.push({
				tool:
					xdev && xdev.mode === "execute"
						? String(xdev.tool ?? "")
						: String(m.toolName ?? ""),
				text: textOf(m.content),
				isError: m.isError === true,
			});
		}
	}
	return t;
}
const transcript = async (tid: string) => transcriptOf(await entries(tid));
/** 召回证据：助手回复与 recall 工具的结果。 */
const recallEvidence = (t: Transcript) =>
	[
		...t.replies,
		...t.results.filter((r) => r.tool === "recall").map((r) => r.text),
	].join("\n");

const SETTLED: Record<string, true> = { idle: true, awaiting_input: true };

/** 等 afterSeq 之后出现用户消息、其后有带文本的助手回复，且线程静止。 */
async function waitTurn(tid: string, afterSeq: number, timeoutMs: number) {
	const t0 = Date.now();
	const th = await waitFor(
		`线程 ${tid} 回合结束`,
		async () => {
			const t = await getThread(tid);
			if (t.state === "error") throw new Error(`线程 ${tid} 停在 error`);
			if (!SETTLED[t.state]) return null;
			const es = await entries(tid, afterSeq);
			const userAt = es.findIndex((e) => {
				const m = e.payload.message;
				return e.type === "message" && isRecord(m) && m.role === "user";
			});
			if (userAt < 0) return null;
			return transcriptOf(es.slice(userAt + 1)).replies.length ? t : null;
		},
		timeoutMs,
		3_000,
	);
	note(`线程 ${tid} 回合结束于 ${th.state}（${sec(Date.now() - t0)}）`);
	return th;
}

/** 同一线程补发一条消息并等回合结束。 */
async function say(tid: string, msg: string) {
	const before = (await entries(tid)).at(-1)?.seq ?? 0;
	const r = cli(`thread send ${tid} ${q(msg)}`);
	if (r.code !== 0)
		throw new Error(`oryn thread send：${clip(oneLine(r.out))}`);
	await waitTurn(tid, before, 600_000);
}

const orbs = new Set<string>();
const [AGENT_PROVIDER = "", AGENT_ID = ""] = splitModel(AGENT_MODEL);
function splitModel(s: string): [string, string] {
	const i = s.indexOf("/");
	return i > 0 ? [s.slice(0, i), s.slice(i + 1)] : ["", s];
}

/** 新建线程（缺省新 orb；runner 给出时跑在 runner 上），等首回合结束。 */
async function newThread(
	tag: string,
	prompt: string,
	opts: { projectId?: string; runner?: { id: string; dir: string } },
): Promise<ThreadView> {
	const created = await rjson<{ thread: ThreadView }>(
		"POST",
		"/api/v1/threads",
		{
			prompt,
			title: `m16 ${RUN} ${tag}`,
			...(opts.projectId ? { projectId: opts.projectId } : {}),
			executor: opts.runner
				? { kind: "runner", runnerId: opts.runner.id, dir: opts.runner.dir }
				: { kind: "new_orb", provider: "local" },
			model: { provider: AGENT_PROVIDER, id: AGENT_ID },
		},
	);
	const tid = created.thread.id;
	await waitTurn(tid, 0, 1_200_000);
	const th = await getThread(tid);
	if (th.orbId && !opts.runner) orbs.add(th.orbId);
	note(
		`${tag}：线程 ${tid}（${opts.projectId ?? "无项目"}，${opts.runner ? `runner ${opts.runner.id}` : `orb ${th.orbId ?? "?"}`}）`,
	);
	return th;
}

const retainArgs = (t: Transcript) =>
	t.calls.filter((c) => c.name === "retain").map((c) => JSON.stringify(c.args));

/** 新线程问一个问题，期望召回到 mark；没召回到就补发 nudge 一次。返回线程与最终转录。 */
async function askExpectRecall(
	tag: string,
	prompt: string,
	nudge: string,
	mark: string,
	opts: { projectId?: string; runner?: { id: string; dir: string } },
) {
	const th = await newThread(tag, prompt, opts);
	let t = await transcript(th.id);
	if (!recallEvidence(t).includes(mark)) {
		note(
			`${tag} 首回合没有召回到「${mark}」（工具：${t.calls.map((c) => c.name).join(",") || "无"}；回复：${clip(oneLine(t.replies.at(-1) ?? ""), 160)}），补发一次`,
		);
		await say(th.id, nudge);
		t = await transcript(th.id);
	}
	note(
		`${tag} 工具调用：${t.calls.map((c) => c.name).join(",") || "无"}；最后回复：${clip(oneLine(t.replies.at(-1) ?? ""), 200)}`,
	);
	return { th, t };
}

// ---------------------------------------------------------------- 环境

let gateway = "";
let embedModel = "";
let imageBuilt = false;
let composeStarted = false;
let envWritten = false;
let projectP = "";
let projectQ = "";
let envContent = "";

async function setup() {
	for (const b of ["orynd", "orbd", "oryn"])
		if (!existsSync(`${BIN}/${b}`))
			throw new Error(`缺少 ${BIN}/${b}：先 make build（或设 M16_BIN）`);
	for (const tool of ["docker", "git"])
		if (sh(`command -v ${tool}`).code !== 0)
			throw new Error(`本机缺少 ${tool}`);
	if (sh("docker compose version").code !== 0)
		throw new Error("本机 docker 缺少 compose 插件");
	if (existsSync(ENV_FILE))
		throw new Error(
			`${ENV_FILE} 已存在：验收要写入并在结束时删除它，先移走主人自己的 .env`,
		);
	if (sh("ss -Hltn 'sport = :8888'").stdout !== "")
		throw new Error("127.0.0.1:8888 已被占用（已有 Hindsight 在跑？）");
	if (!existsSync(MODELS_SRC))
		throw new Error(
			`缺少 ${MODELS_SRC}（设 M16_MODELS 指向含真实模型的 models.yml）`,
		);
	const want = JSON.parse(readFileSync(`${ROOT}/core/source.json`, "utf8")) as {
		tag: string;
		sha256: Record<string, string>;
	};
	const got = sh(`sha256sum ${q(OMP)} | cut -d' ' -f1`).stdout;
	check(
		got === want.sha256["linux-x64"],
		`build/core/x64/omp 是钉住的 Core ${want.tag}（sha256 ${got.slice(0, 12)}…）`,
	);
	const info = await http("GET", `${EMBED}/info`);
	check(
		info.status === 200 && isRecord(info.json) && !!info.json.model_id,
		`TEI ${EMBED}/info → ${info.status}`,
	);
	embedModel = String((info.json as Record<string, unknown>).model_id);
	note(`嵌入模型（TEI /info）：${embedModel}`);

	gateway = sh(
		"docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}'",
	).stdout;
	check(/^\d+\.\d+\.\d+\.\d+$/.test(gateway), `docker0 网关地址 ${gateway}`);

	// orb 镜像：基础镜像 + 本次的三个二进制
	mkdirSync(`${TMP}/image`, { recursive: true });
	mkdirSync(LOGS, { recursive: true });
	for (const [src, name] of [
		[OMP, "omp"],
		[`${BIN}/orbd`, "orbd"],
		[`${BIN}/oryn`, "oryn"],
	] as const)
		copyFileSync(src, `${TMP}/image/${name}`);
	writeFileSync(
		`${TMP}/image/Dockerfile`,
		`FROM ${BASE_IMAGE}
USER root
COPY --chmod=0755 omp orbd oryn /usr/local/bin/
USER user
RUN omp --version && omp --smoke-test && rm -rf /home/user/.omp/logs /home/user/.omp/run
`,
	);
	const t0 = Date.now();
	const built = sh(
		`docker build -q -t ${IMAGE} ${q(`${TMP}/image`)} > ${q(`${LOGS}/image.log`)} 2>&1`,
	);
	imageBuilt = built.code === 0;
	check(
		imageBuilt,
		`构建 orb 镜像 ${IMAGE}（基于 ${BASE_IMAGE}，${sec(Date.now() - t0)}）${built.code ? `：${clip(readFileSync(`${LOGS}/image.log`, "utf8").slice(-400))}` : ""}`,
	);
	rmSync(`${TMP}/image`, { recursive: true, force: true });

	writeFileSync(
		`${TMP}/orynd.yaml`,
		`listen: ["127.0.0.1:${API_PORT}", "${gateway}:${API_PORT}"]
public_url: "${PUBLIC}"
portal_domain: ""
data_dir: "${TMP}/data"
log_level: "debug"
idle: { agent_idle: "30m", user_idle: "30m", stop_after_frozen: "24h" }
orb_defaults: { provider: "local", type: "" }
coordinator: { omp_path: "${OMP}", dir: "${COORD_DIR}", egress_port: 7936 }
`,
	);
	bg("orynd", [`${BIN}/orynd`, "--config", `${TMP}/orynd.yaml`]);
	await waitFor(
		"orynd 就绪",
		async () => (await fetch(`${API}/healthz`)).ok,
		30_000,
	);
	const dev = sh(
		`${q(`${BIN}/orynd`)} --config ${q(`${TMP}/orynd.yaml`)} admin create-device --name m16-accept --kind cli`,
	);
	check(
		dev.code === 0,
		`orynd admin create-device${dev.code ? `：${clip(dev.out)}` : ""}`,
	);
	token = dev.stdout.split("\n").at(-1) ?? "";

	const prov = await rjson<{ token: string }>("POST", "/api/v1/providers", {
		name: "local",
	});
	writeFileSync(`${TMP}/provider.token`, `${prov.token}\n`, { mode: 0o600 });
	writeFileSync(
		`${TMP}/provider.yaml`,
		`url: "${API}"
token_file: "${TMP}/provider.token"
orb_url: "http://${gateway}:${API_PORT}"
driver: docker
docker:
  image: "${IMAGE}"
`,
	);
	bg("provider", [
		`${BIN}/oryn`,
		"provider",
		"run",
		"--config",
		`${TMP}/provider.yaml`,
	]);
	await waitFor(
		"provider 在线",
		async () =>
			(
				await rjson<{ providers: { name: string; online: boolean }[] }>(
					"GET",
					"/api/v1/providers",
				)
			).providers.some((p) => p.name === "local" && p.online),
		60_000,
		500,
	);
	note(
		`orynd ${API}（另听 ${gateway}:${API_PORT}），provider local（docker）在线`,
	);

	const put = await api(
		"PUT",
		"/api/v1/agent-config/files/models.yml?mode=384",
		readFileSync(MODELS_SRC, "utf8"),
	);
	check(
		put.status === 200,
		`omp-home models.yml ← ${MODELS_SRC}（${put.status}${put.status === 200 && isRecord(put.json) && Array.isArray(put.json.extractedSecrets) ? `，抽取 ${put.json.extractedSecrets.length} 个密钥` : `：${clip(put.text)}`}）`,
	);
	const wanted = [AGENT_MODEL, HS_MODEL];
	await api("GET", "/api/v1/models/catalog?refresh=true");
	const cat = await waitFor(
		`中央模型目录含 ${wanted.join("、")}`,
		async () => {
			const c = await rjson<{ models: { provider: string; id: string }[] }>(
				"GET",
				"/api/v1/models/catalog",
			);
			const have = new Set(c.models.map((m) => `${m.provider}/${m.id}`));
			return wanted.every((w) => have.has(w)) ? c : null;
		},
		300_000,
		3_000,
	);
	note(`中央模型目录 ${cat.models.length} 个模型，含 ${wanted.join("、")}`);

	const mkProject = async (name: string) =>
		(
			await rjson<{ id: string }>("POST", "/api/v1/projects", {
				name,
				shallowClone: false,
				snapshots: false,
				warmOrbs: 0,
			})
		).id;
	projectP = await mkProject(`m16-p-${RUN}`);
	projectQ = await mkProject(`m16-q-${RUN}`);
	note(`项目 P = ${projectP}，Q = ${projectQ}`);
	envReady = true;
}

// ---------------------------------------------------------------- 需求

let statusBeforeUp = "";

/** R4：管理接口配好连接、嵌入、模型，生成 .env，compose 起来后状态变成 ok。 */
async function r4() {
	const st = await rjson<{ enabled: boolean; hindsightUrl?: string }>(
		"PUT",
		"/api/v1/memory/settings",
		{ enabled: true, hindsightUrl: HS },
	);
	check(
		st.enabled && st.hindsightUrl === HS,
		`设置：地址 ${st.hindsightUrl}，开关 ${st.enabled ? "开" : "关"}`,
	);
	const ep = await rjson<{
		embed?: { baseUrl: string; model: string; dimensions?: number };
		rerankMode: string;
		needsRestart: boolean;
	}>("PUT", "/api/v1/memory/endpoints/embed", {
		baseUrl: `${EMBED}/v1`,
		model: embedModel,
		dimensions: 1024,
	});
	check(
		ep.embed?.model === embedModel && ep.embed.dimensions === 1024,
		`嵌入接口 ${ep.embed?.baseUrl} ${ep.embed?.model}（${ep.embed?.dimensions} 维）`,
	);
	expect(
		ep.rerankMode === "rrf",
		`未配重排接口：rerankMode = ${ep.rerankMode}`,
	);
	const [hp, hm] = splitModel(HS_MODEL);
	const models = await rjson<{
		items: {
			purpose: string;
			providerKey?: string;
			modelId?: string;
			family?: string;
		}[];
	}>("PUT", "/api/v1/memory/models", {
		items: [{ purpose: "default", providerKey: hp, modelId: hm }],
	});
	const def = models.items.find((i) => i.purpose === "default");
	check(
		def?.providerKey === hp && def.modelId === hm,
		`Hindsight 默认模型 ${def?.providerKey}/${def?.modelId}（${def?.family}），其余用途跟随默认`,
	);

	const env = await rjson<{ content: string }>(
		"POST",
		"/api/v1/memory/env-file",
	);
	envContent = env.content;
	hsKey = /^HINDSIGHT_API_TENANT_API_KEY=(.+)$/m.exec(envContent)?.[1] ?? "";
	check(hsKey !== "", ".env 含 Hindsight API key");
	expect(
		/^HINDSIGHT_API_RERANKER_PROVIDER=rrf$/m.test(envContent),
		".env 写 HINDSIGHT_API_RERANKER_PROVIDER=rrf",
	);
	expect(
		envContent.includes(
			`HINDSIGHT_API_LLM_BASE_URL=${PUBLIC}/memory-relay/v1/default`,
		),
		`.env 的中转地址以 public_url（${PUBLIC}）为前缀`,
	);
	writeFileSync(ENV_FILE, envContent, { mode: 0o600 });
	chmodSync(ENV_FILE, 0o600);
	envWritten = true;
	note(`.env → ${ENV_FILE}（0600，${envContent.split("\n").length} 行）`);
	statusBeforeUp = (await memStatus()).state;
	note(`compose 启动前状态：${statusBeforeUp}`);

	const t0 = Date.now();
	composeStarted = true;
	const up = dc("up -d");
	check(
		up.code === 0,
		`docker compose -p ${COMPOSE_PROJECT} up -d（${sec(Date.now() - t0)}）${up.code ? `：${clip(up.out, 600)}` : ""}`,
	);
	await waitFor(
		"记忆状态 ok",
		async () => {
			const s = await memStatus();
			return s.state === "ok" ? s : null;
		},
		300_000,
		3_000,
	).catch(async (e: unknown) => {
		const s = await memStatus().catch(() => undefined);
		note(`最后状态：${s?.state}（${clip(s?.error ?? "")}）`);
		note(`api 日志末尾：${clip(dc("logs --tail 30 api").out, 800)}`);
		throw e;
	});
	check(true, `GET /memory/status → ok（up 后 ${sec(Date.now() - t0)}）`);
	const port = dc("port api 8888").stdout;
	expect(port === "127.0.0.1:8888", `API 只发布在 127.0.0.1（${port}）`);
	const reach = dc(
		`exec -T api python3 -c ${q(`import urllib.request as u; print(u.urlopen("${PUBLIC}/healthz", timeout=5).status)`)}`,
	);
	expect(
		reach.stdout.endsWith("200"),
		`api 容器访问 ${PUBLIC}/healthz → ${clip(oneLine(reach.out), 120)}`,
	);
	const probe = await api(
		"GET",
		`/api/v1/memory/search?q=${encodeURIComponent("连通性检查")}`,
	);
	check(
		probe.status === 200,
		`召回经嵌入中转（容器 → orynd → TEI）可用：GET /memory/search → ${probe.status}${probe.status === 200 ? "" : ` ${clip(probe.text)}`}`,
	);
	const after = await rjson<{ items: { needsRestart: boolean }[] }>(
		"GET",
		"/api/v1/memory/models",
	);
	const eps = await rjson<{ needsRestart: boolean; rerankMode: string }>(
		"GET",
		"/api/v1/memory/endpoints",
	);
	expect(
		after.items.every((i) => !i.needsRestart) && !eps.needsRestart,
		`生成 .env 后无需重启（模型 ${after.items.filter((i) => i.needsRestart).length} 项待重启，重排 ${eps.rerankMode}${eps.needsRestart ? " 待重启" : ""}）`,
	);
}

/** R1：状态里有可达性、版本、ready 与记忆数；版本在已验证的小版本内。 */
async function r1() {
	expect(
		statusBeforeUp === "unreachable",
		`Hindsight 未启动时状态为 unreachable（实际 ${statusBeforeUp}）`,
	);
	const s = await memStatus();
	check(s.state === "ok", `状态 ${s.state}（可达且 /health/ready 正常）`);
	expect(
		!!s.version && s.version.startsWith(`${s.testedMinor}.`),
		`版本 ${s.version}（已验证 ${s.testedMinor}）`,
	);
	expect(!s.versionMismatch, `versionMismatch = ${s.versionMismatch}`);
	expect(
		typeof s.memoryCount === "number",
		`bank oryn 的记忆数 ${s.memoryCount}`,
	);
	const v = await http("GET", `${HS}/version`);
	expect(
		isRecord(v.json) && v.json.api_version === s.version,
		`与 Hindsight /version 一致（${isRecord(v.json) ? String(v.json.api_version) : v.status}）`,
	);
}

let pDocIds: string[] = [];

/** R5：P 线程 1 写入的约定，P 线程 2（新 orb）能召回，Q 召回不到。 */
async function r5() {
	const t1 = await newThread(
		"P1 写入",
		`请调用记忆工具 retain 记下本项目的一条约定（scope 用 project）：「${P_FACT}」只调用一次 retain，不要读写任何文件，也不要运行命令；写完只回复「已记下」。`,
		{ projectId: projectP },
	);
	let tr = await transcript(t1.id);
	if (!retainArgs(tr).some((a) => a.includes(P_MARK))) {
		note(
			`P1 没有调用 retain（工具：${tr.calls.map((c) => c.name).join(",") || "无"}），补发一次`,
		);
		await say(
			t1.id,
			`请现在调用 retain 工具，items 只有一条，content 为「${P_FACT}」，scope 为 project。`,
		);
		tr = await transcript(t1.id);
	}
	const args = retainArgs(tr);
	expect(
		args.some((a) => a.includes(P_MARK)),
		`P1 调用 retain：${clip(args.join(" | "), 200)}`,
	);
	const t0 = Date.now();
	const docs = await waitFor(
		"P 的 document 出现（Hindsight 异步 retain）",
		async () => {
			const d = await projectDocs(projectP);
			return d.some((x) => docText(x).includes(P_MARK)) ? d : null;
		},
		600_000,
		5_000,
	);
	pDocIds = docs.map((d) => d.id);
	const doc = docs.find((x) => docText(x).includes(P_MARK));
	check(
		!!doc,
		`GET /memory/documents?projectId=P 出现约定（${sec(Date.now() - t0)}，${docs.length} 个 document：${clip(oneLine(doc?.preview ?? ""), 120)}）`,
	);
	expect(
		doc?.threadId === t1.id,
		`document 归属线程 P1（${doc?.threadId ?? "无"}）`,
	);
	const qDocs = await projectDocs(projectQ);
	expect(
		!qDocs.some((d) => docText(d).includes(P_MARK)),
		`Q 的 document 里没有约定（${qDocs.length} 个）`,
	);

	const p2 = await askExpectRecall("P2 召回", P_ASK, P_NUDGE, P_MARK, {
		projectId: projectP,
	});
	check(
		recallEvidence(p2.t).includes(P_MARK),
		`P2（新 orb ${p2.th.orbId}，与 P1 的 ${t1.orbId} 不同：${p2.th.orbId !== t1.orbId}）召回到「${P_MARK}」`,
	);
	expect(p2.th.orbId !== t1.orbId, "P2 跑在新 orb 上");

	const q1 = await newThread("Q1 召回", P_ASK, { projectId: projectQ });
	let qt = await transcript(q1.id);
	if (!qt.calls.some((c) => c.name === "recall")) {
		note("Q1 没有调用 recall，补发一次（否则「召回不到」不成立）");
		await say(q1.id, P_NUDGE);
		qt = await transcript(q1.id);
	}
	expect(
		qt.calls.some((c) => c.name === "recall"),
		"Q1 调用了 recall",
	);
	check(
		!recallEvidence(qt).includes(P_MARK),
		`Q1 召回不到约定（最后回复：${clip(oneLine(qt.replies.at(-1) ?? ""), 160)}）`,
	);
	const hitsQ = await search("Go 源文件 第一行注释 约定", projectQ);
	expect(
		!hitsQ.some(
			(h) =>
				h.text.includes(P_MARK) ||
				(!!h.documentId && pDocIds.includes(h.documentId)),
		),
		`REST 召回（projectId=Q）不含 P 的记忆（${hitsQ.length} 条）`,
	);
	const hitsP = await search("Go 源文件 第一行注释 约定", projectP);
	expect(
		hitsP.some(
			(h) =>
				h.text.includes(P_MARK) ||
				(!!h.documentId && pDocIds.includes(h.documentId)),
		),
		`REST 召回（projectId=P）含约定（${hitsP.length} 条）`,
	);
}

/** R13：runner 上属于 P 的线程能召回 P 的记忆。 */
async function r13() {
	const created = await rjson<{
		runner: { id: string; orbId: string };
		token: string;
	}>("POST", "/api/v1/runners", { name: `m16-runner-${RUN}` });
	const rhome = `${TMP}/runner-home`;
	const rdir = `${TMP}/runner-dir`;
	mkdirSync(`${rhome}/.omp/agent`, { recursive: true });
	mkdirSync(rdir, { recursive: true });
	copyFileSync(MODELS_SRC, `${rhome}/.omp/agent/models.yml`);
	chmodSync(`${rhome}/.omp/agent/models.yml`, 0o600);
	sh(`git -C ${q(rdir)} init -q`);
	bg(
		"runner",
		[
			`${BIN}/orbd`,
			"run",
			"--runner",
			...(IS_ROOT ? ["--allow-root"] : []),
			"--dir",
			rdir,
		],
		{
			HOME: rhome,
			PATH: process.env.PATH ?? "/usr/local/bin:/usr/bin:/bin",
			ORYN_URL: API,
			ORYN_RUNNER_TOKEN: created.token,
			ORYN_AGENT_BIN: OMP,
			ORYN_EGRESS_ADDR: "127.0.0.1:0",
		},
		rhome,
	);
	const runner = await waitFor(
		"runner 在线",
		async () =>
			(
				await rjson<{
					runners: {
						id: string;
						online: boolean;
						features: string[];
						agentVersion?: string;
					}[];
				}>("GET", "/api/v1/runners")
			).runners.find((r) => r.id === created.runner.id && r.online),
		60_000,
		1_000,
	);
	note(
		`runner ${runner.id} 在线（features：${runner.features.join(",")}；agent ${runner.agentVersion ?? "?"}）`,
	);
	const r = await askExpectRecall("runner 召回", P_ASK, P_NUDGE, P_MARK, {
		projectId: projectP,
		runner: { id: runner.id, dir: rdir },
	});
	const th = await getThread(r.th.id);
	expect(th.orbId === created.runner.orbId, "线程跑在 runner 的 orb 上");
	expect(
		th.projectId === projectP,
		`线程属于 P（${th.projectId ?? "无项目"}）`,
	);
	check(recallEvidence(r.t).includes(P_MARK), `runner 线程召回到「${P_MARK}」`);
}

let candidate: MemoryCandidate | undefined;

/** R8：无项目线程写入的全局偏好进候选队列，不进 Hindsight。 */
async function r8() {
	const before = (await memStatus()).memoryCount ?? 0;
	const th = await newThread(
		"无项目 写入全局",
		`请调用记忆工具 retain 记下我的一条个人偏好，scope 必须设为 global：「${G_FACT}」只调用一次 retain，不要读写任何文件，也不要运行命令；写完只回复「已记下」。`,
		{},
	);
	let tr = await transcript(th.id);
	if (!retainArgs(tr).some((a) => a.includes(G_MARK))) {
		note(
			`没有调用 retain（工具：${tr.calls.map((c) => c.name).join(",") || "无"}），补发一次`,
		);
		await say(
			th.id,
			`请现在调用 retain 工具，items 只有一条，content 为「${G_FACT}」，scope 为 global。`,
		);
		tr = await transcript(th.id);
	}
	const args = retainArgs(tr);
	expect(
		args.some((a) => a.includes(G_MARK)),
		`调用 retain：${clip(args.join(" | "), 200)}`,
	);
	// agent 偶尔先给错参数（工具当场校验失败）再自行改正；要的是至少一次成功写入，网关回 2xx（Core 收到成功应答）
	const rets = tr.results.filter((x) => x.tool === "retain");
	const okRet = rets.filter((x) => !x.isError);
	expect(
		okRet.length > 0,
		`retain 成功应答 ${okRet.length} 次：${clip(oneLine(okRet.map((x) => x.text).join(" | ")), 120)}${rets.length > okRet.length ? `（另有 ${rets.length - okRet.length} 次参数校验失败后由 agent 自行改正）` : ""}`,
	);
	const t0 = Date.now();
	candidate = await waitFor(
		"候选出现",
		async () => (await candidates()).find((c) => c.content.includes(G_MARK)),
		300_000,
		3_000,
	);
	check(
		!!candidate,
		`GET /memory/candidates 出现候选（${sec(Date.now() - t0)}）：${clip(oneLine(candidate.content), 120)}`,
	);
	expect(
		candidate.sourceThreadId === th.id,
		`候选来源线程 ${candidate.sourceThreadId ?? "无"}`,
	);
	const g = await globalDocs();
	expect(
		!g.some((d) => docText(d).includes(G_MARK)),
		`全局 document 里没有它（${g.length} 个）`,
	);
	const hits = await search("代码评审意见 个人偏好 开头");
	expect(
		!hits.some((h) => h.text.includes(G_MARK)),
		`REST 全局召回召不到它（${hits.length} 条）`,
	);
	const after = (await memStatus()).memoryCount ?? 0;
	note(`bank 记忆数 ${before} → ${after}（候选不写 Hindsight）`);
	const st = await memStatus();
	expect(st.candidates >= 1, `状态里的候选数 ${st.candidates}`);
}

let approved: MemoryDocument | undefined;

/** R20：批准候选后成为全局记忆，Q 的新线程能召回。 */
async function r20() {
	const c = candidate;
	if (!c) throw new Error("没有 R8 的候选");
	approved = await rjson<MemoryDocument>(
		"POST",
		`/api/v1/memory/candidates/${c.id}/approve`,
		{},
	);
	check(
		!!approved.id,
		`批准候选 → 全局记忆 ${approved.id}：${clip(oneLine(approved.content ?? approved.preview), 120)}`,
	);
	expect(!(await candidates()).some((x) => x.id === c.id), "候选已离开队列");
	const g = await waitFor(
		"全局 document 出现",
		async () => {
			const d = await globalDocs();
			return d.some((x) => x.id === approved?.id) ? d : null;
		},
		60_000,
		2_000,
	);
	expect(
		g.some((x) => x.id === approved?.id && docText(x).includes(G_MARK)),
		`GET /memory/documents?global=true 含它（${g.length} 个）`,
	);
	const r = await askExpectRecall("Q2 召回全局", G_ASK, G_NUDGE, G_MARK, {
		projectId: projectQ,
	});
	check(
		recallEvidence(r.t).includes(G_MARK),
		`Q 的新线程（orb ${r.th.orbId}）召回到「${G_MARK}」`,
	);
}

/** R6：已批准的全局记忆在各项目与无项目范围都能召回（R20 已在 Q 的线程里验过 agent 侧）。 */
async function r6() {
	const id = approved?.id ?? "";
	const hit = (hs: SearchHit[]) =>
		hs.some((h) => h.documentId === id || h.text.includes(G_MARK));
	const query = "代码评审意见 个人偏好 开头";
	for (const [label, pid] of [
		["项目 P", projectP],
		["项目 Q", projectQ],
		["无项目（仅全局）", undefined],
	] as const) {
		const hs = await search(query, pid);
		const g = hs.filter((h) => h.documentId === id || h.text.includes(G_MARK));
		expect(
			hit(hs) && g.every((h) => h.global !== false),
			`${label} 的 REST 召回含全局偏好（${hs.length} 条，命中 ${g.length} 条${g.some((h) => h.global) ? "，标为全局" : ""}）`,
		);
	}
	expect(
		verdicts.get("R20")?.ok === true,
		"agent 侧：Q 的新线程召回到全局偏好（R20）",
	);
}

/** R23：删除 P 后它在 Hindsight 的 document 全部删除。 */
async function r23() {
	const before = await projectDocs(projectP);
	check(before.length > 0, `删除前 P 有 ${before.length} 个 document`);
	const del = await api("DELETE", `/api/v1/projects/${projectP}`);
	check(del.status === 204, `DELETE /api/v1/projects/P → ${del.status}`);
	const t0 = Date.now();
	await waitFor(
		"P 的 document 清空",
		async () => (await projectDocs(projectP)).length === 0,
		180_000,
		3_000,
	);
	check(
		true,
		`GET /memory/documents?projectId=P 为空（${sec(Date.now() - t0)}）`,
	);
	const direct = await hsJson<{ total: number }>(
		`/v1/default/banks/oryn/documents?tags=${encodeURIComponent(`project:${projectP.toLowerCase()}`)}&tags_match=any_strict&limit=10`,
	);
	expect(
		direct.total === 0,
		`Hindsight 里 project:P 的 document 数 ${direct.total}`,
	);
	for (const id of pDocIds) {
		const r = await http(
			"GET",
			`${HS}/v1/default/banks/oryn/documents/${encodeURIComponent(id)}`,
			undefined,
			{ Authorization: `Bearer ${hsKey}` },
		);
		expect(r.status === 404, `Hindsight GET document ${id} → ${r.status}`);
	}
	// 遗忘后受影响的 mental model 刷新完成前，重试队列里会有 `mental-model-refresh:<id>`（v6），不算删除失败
	const pend = await rjson<{
		items: { documentId: string; projectId?: string }[];
	}>("GET", "/api/v1/memory/pending-deletes");
	const failedP = pend.items.filter(
		(p) =>
			p.projectId === projectP &&
			!p.documentId.startsWith("mental-model-refresh:"),
	);
	expect(
		failedP.length === 0,
		`重试队列里没有 P 的删除失败条目（共 ${pend.items.length} 条，P 的 ${failedP.length} 条）`,
	);
	const mm = await rjson<{ items: unknown[] }>(
		"GET",
		`/api/v1/memory/mental-models?projectId=${projectP}`,
	);
	expect(
		mm.items.length === 0,
		`P 的 mental models 已删除（剩 ${mm.items.length} 个）`,
	);
	// 全局记忆只在 R20 批准过时存在
	if (approved) {
		const g = await globalDocs();
		expect(
			g.some((d) => d.id === approved?.id),
			"全局记忆不受影响",
		);
	}
}

// ---------------------------------------------------------------- 收尾

let cleaned = false;
async function cleanup() {
	if (cleaned) return;
	cleaned = true;
	console.log("M16 收尾");
	// orb：经 REST 删除（provider 还在），删不掉的直接 docker rm -f
	for (const id of orbs) {
		const r = await api("DELETE", `/api/v1/orbs/${id}`).catch(() => undefined);
		note(`DELETE orb ${id} → ${r?.status ?? "失败"}`);
	}
	if (orbs.size)
		await waitFor(
			"orb 容器删除",
			() =>
				[...orbs].every(
					(id) => sh(`docker container inspect ${id}`).code !== 0,
				),
			120_000,
			2_000,
		).catch(() => note("部分 orb 容器未按时删除，改为 docker rm -f"));
	for (const id of orbs) sh(`docker rm -f ${id} >/dev/null 2>&1`);
	for (const b of [...bgs].reverse()) {
		if (b.proc.exitCode === null && b.proc.signalCode === null) {
			b.proc.kill("SIGTERM");
			const r = await Promise.race([
				b.proc.exited,
				sleep(10_000).then(() => "timeout"),
			]);
			if (r === "timeout") b.proc.kill("SIGKILL");
		}
	}
	if (composeStarted) {
		if (!existsSync(ENV_FILE) && envContent)
			writeFileSync(ENV_FILE, envContent, { mode: 0o600 });
		const down = dc("down -v --remove-orphans --timeout 75");
		note(
			`docker compose -p ${COMPOSE_PROJECT} down -v → ${down.code}${down.code ? `：${clip(down.out, 300)}` : ""}`,
		);
		// 兜底：按 compose 标签清掉残留的容器与卷
		const label = `label=com.docker.compose.project=${COMPOSE_PROJECT}`;
		sh(
			`docker ps -aq --filter ${label} | xargs -r docker rm -f >/dev/null 2>&1; docker volume ls -q --filter ${label} | xargs -r docker volume rm >/dev/null 2>&1; docker network ls -q --filter ${label} | xargs -r docker network rm >/dev/null 2>&1`,
		);
		const left = sh(`docker volume ls -q --filter ${label}`).stdout;
		note(left ? `✗ 数据卷残留：${left}` : "数据卷已删除");
	}
	if (envWritten || existsSync(ENV_FILE)) {
		if (envWritten) rmSync(ENV_FILE, { force: true });
		note(`${ENV_FILE} ${existsSync(ENV_FILE) ? "仍在" : "已删除"}`);
	}
	if (imageBuilt) sh(`docker rmi ${IMAGE} >/dev/null 2>&1`);
	if (!existsSync(TMP)) return;
	if (KEEP) console.log(`保留 ${TMP}（日志在 logs/）`);
	else rmSync(TMP, { recursive: true, force: true });
}

process.on("SIGINT", () => {
	console.log("\n收到 Ctrl-C，收尾后退出");
	void cleanup().finally(() => process.exit(130));
});

// ---------------------------------------------------------------- 主流程

console.log(`M16 真机验收（run ${RUN}，临时目录 ${TMP}）`);
try {
	console.log("M16 环境");
	const t0 = Date.now();
	await setup();
	console.log(`M16 环境 PASS（${sec(Date.now() - t0)}）`);
} catch (e) {
	note(`✗ ${e instanceof Error ? e.message : String(e)}`);
	console.log("M16 环境 FAIL");
}
try {
	await req("R4", "compose + 生成的 .env 起 Hindsight，状态变为正常", [], r4);
	await req("R1", "状态：可达、版本、ready、记忆数", ["R4"], r1);
	await req("R5", "同项目共享、项目之间隔离", ["R4"], r5);
	await req("R13", "runner 上属于 P 的线程召回 P 的记忆", ["R5"], r13);
	await req("R8", "全局写入进候选队列", ["R4"], r8);
	await req("R20", "批准候选后 Q 能召回", ["R8"], r20);
	await req("R6", "已批准的全局记忆处处可召回", ["R20"], r6);
	await req("R23", "删除项目级联删除记忆", ["R5"], r23);
} finally {
	await cleanup();
}

console.log("M16 汇总");
for (const r of RS) {
	const v = verdicts.get(r);
	console.log(
		`  ${r.padEnd(4)} ${v?.ok ? "PASS" : "FAIL"}${v && !v.ok && v.why ? `  ${clip(oneLine(v.why), 160)}` : ""}`,
	);
}
const allOk = RS.every((r) => verdicts.get(r)?.ok);
console.log(allOk ? "M16 真机验收 PASS" : "M16 真机验收 FAIL");
process.exit(allOk ? 0 : 1);
