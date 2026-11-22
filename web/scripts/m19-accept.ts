#!/usr/bin/env bun
import {
	copyFileSync,
	existsSync,
	mkdirSync,
	openSync,
	readdirSync,
	readFileSync,
	readlinkSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import type { Server, Subprocess } from "bun";

/**
 * M19 真机验收（docs/plan-m19.md 步骤 1.4 起步，步骤 5.1 补全）：钉住的 Core（build/core/x64/omp）跑在协调宿主的
 * 沙箱与 docker orb 里，真实模型驱动全局助手对话，逐项记录检查结果。宿主工具的细节、确认卡、主线等的确定性覆盖在
 * Go 端到端：`go test ./internal/server/ -run TestE2EM19`。
 *
 * 用法（web/ 目录）：bun run accept:m19 [-- --only <前缀>[,<前缀>…]] [-- --help]
 *   `--only L1` 只跑 id 以 L1 开头的检查；不给则全部跑。
 *
 * 自建一套隔离环境（全部在 /var/tmp/oryn-m19-<run>/ 下，结束时删除；M19_KEEP=1 保留，日志在 logs/）：
 *   - orynd（data_dir 全新）听 127.0.0.1:$M19_PORT（缺省 7690）与 docker0 网关地址的同一端口（orb 的 link 走它）。
 *   - 协调宿主：`omp_path` 指向钉住的 Core（校验 sha256），目录固定为 $M19_COORD_DIR（root 运行时宿主账号按目录派生，
 *     每次开跑与收尾时清空），助手对话的模型是 $M19_MODEL（缺省 claude-pro/claude-sonnet-5-5）。
 *   - provider `local`（driver docker）：镜像是在 $M19_BASE_IMAGE（缺省 oryn-orb:dev）上只替换 orbd / oryn / omp 的薄层
 *     `oryn-orb:m19accept-<run>`，结束时删除；工作线程、定时运行线程与助手的电脑都是这个 provider 上的 orb。
 *   - Agent 配置：把 $M19_MODELS（缺省 ~/.omp/agent/models.yml）原样 PUT 成全局配置的 models.yml（密钥由 orynd 抽取）；
 *     全局 config.yml 只写 modelRoles：default = $M19_MODEL，image 取本机 ~/.omp/agent/config.yml 的同名角色（没有则
 *     L1-image 跳过）。
 *
 * agent 的不确定性：提示词点名要调用的工具与参数；断言看转录里的工具结果与 REST 状态（不看措辞）。没按要求调用工具时，
 * 同一对话补发一次提示。
 *
 * 每条打印 `M19 <id> PASS|FAIL|SKIP（秒）` 与证据行，最后汇总。没有 FAIL 时退出码 0；SKIP 逐条写明原因（未验证，
 * 不等于通过）。
 */

const USAGE = `用法：bun scripts/m19-accept.ts [--only <前缀>[,<前缀>…]] [--help]
  环境变量：M19_BIN（缺省 <仓库>/bin）、M19_KEEP=1（保留临时目录）、M19_PORT（缺省 7690）、
  M19_MODELS（缺省 ~/.omp/agent/models.yml）、M19_MODEL（缺省 claude-pro/claude-sonnet-5-5）、
  M19_OMP_CONFIG（缺省 ~/.omp/agent/config.yml，只取 modelRoles.image）、M19_BASE_IMAGE（缺省 oryn-orb:dev）、
  M19_COORD_DIR（缺省 /var/tmp/oryn-m19-coord）、M19_COORD_EGRESS（缺省 7937）。`;
const argv = process.argv.slice(2);
if (argv.includes("--help") || argv.includes("-h")) {
	console.log(USAGE);
	process.exit(0);
}
const ONLY = argv
	.flatMap((a, i) =>
		a.startsWith("--only=")
			? [a.slice(7)]
			: argv[i - 1] === "--only"
				? [a]
				: [],
	)
	.flatMap((s) => s.split(","))
	.map((s) => s.trim())
	.filter(Boolean);

const ROOT = `${import.meta.dir}/../..`;
const BIN = process.env.M19_BIN ?? `${ROOT}/bin`;
const KEEP = process.env.M19_KEEP === "1";
const RUN = Math.random().toString(36).slice(2, 6);
const TMP = `/var/tmp/oryn-m19-${RUN}`;
const LOGS = `${TMP}/logs`;
const API_PORT = Number(process.env.M19_PORT ?? 7690);
const API = `http://127.0.0.1:${API_PORT}`;
const OMP = `${ROOT}/build/core/x64/omp`;
const MODELS_SRC =
	process.env.M19_MODELS ??
	`${process.env.HOME ?? "/root"}/.omp/agent/models.yml`;
const MODEL = process.env.M19_MODEL ?? "claude-pro/claude-sonnet-5-5";
const MODEL_PROVIDER = MODEL.slice(0, MODEL.indexOf("/"));
const MODEL_ID = MODEL.slice(MODEL.indexOf("/") + 1);
const OMP_CONFIG_SRC =
	process.env.M19_OMP_CONFIG ??
	`${process.env.HOME ?? "/root"}/.omp/agent/config.yml`;
const BASE_IMAGE = process.env.M19_BASE_IMAGE ?? "oryn-orb:dev";
const IMAGE = `oryn-orb:m19accept-${RUN}`;
const COORD_DIR = process.env.M19_COORD_DIR ?? "/var/tmp/oryn-m19-coord";
const COORD_EGRESS = Number(process.env.M19_COORD_EGRESS ?? 7937);
/** 回合等待上限（真实模型，含协调宿主冷启动）。 */
const TURN_TIMEOUT = 600_000;
/** 首回合要先建 docker orb 的线程的等待上限。 */
const ORB_TURN_TIMEOUT = 1_200_000;

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
/** 硬断言：不成立就结束本条。 */
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};
/** 软断言：记下失败继续走，本条结尾判 FAIL。 */
const expect = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) stepFails++;
};
/** 本条无法在这台机器上验证（如没有公网）：判 SKIP 并写明原因，不算通过。 */
class Skip extends Error {}
const skip = (why: string): never => {
	throw new Skip(why);
};

type Verdict = "PASS" | "FAIL" | "SKIP";
interface Check {
	id: string;
	title: string;
	/** 前置检查：任一条未 PASS 则本条判 FAIL，不执行。 */
	needs?: string[];
	run: () => Promise<void>;
}
const verdicts = new Map<string, { v: Verdict; why: string }>();
let envReady = false;

async function runCheck(c: Check) {
	console.log(`M19 ${c.id} ${c.title}`);
	const blocked = (c.needs ?? []).filter((n) => verdicts.get(n)?.v !== "PASS");
	if (!envReady || blocked.length) {
		const why = !envReady ? "环境未就绪" : `前置 ${blocked.join("、")} 未通过`;
		note(`✗ ${why}，不执行`);
		verdicts.set(c.id, { v: "FAIL", why });
		console.log(`M19 ${c.id} FAIL（${why}）`);
		return;
	}
	stepFails = 0;
	const t0 = Date.now();
	let why = "";
	let v: Verdict = "PASS";
	try {
		await c.run();
	} catch (e) {
		why = e instanceof Error ? e.message : String(e);
		if (e instanceof Skip) {
			note(`– 跳过：${why}`);
			v = "SKIP";
		} else {
			note(`✗ ${why}`);
			stepFails++;
		}
	}
	if (v !== "SKIP" && stepFails) {
		v = "FAIL";
		why ||= `${stepFails} 项软断言失败`;
	}
	verdicts.set(c.id, { v, why });
	console.log(
		`M19 ${c.id} ${v}（${sec(Date.now() - t0)}${v === "SKIP" ? `；${why}` : ""}）`,
	);
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
): Bg {
	mkdirSync(LOGS, { recursive: true });
	const fd = openSync(`${LOGS}/${name}.log`, "a");
	const proc = Bun.spawn(cmd, {
		cwd: ROOT,
		env: env ?? process.env,
		stdin: "ignore",
		stdout: fd,
		stderr: fd,
	});
	const b = { name, proc };
	bgs.push(b);
	return b;
}

const orynLog = () =>
	existsSync(`${LOGS}/orynd.log`)
		? readFileSync(`${LOGS}/orynd.log`, "utf8")
		: "";

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

/** 跑 oryn CLI（异步：等待期间事件循环照常，Ctrl-C 能收尾）。 */
async function cli(args: string[]): Promise<{ code: number; out: string }> {
	const p = Bun.spawn([`${BIN}/oryn`, ...args], {
		cwd: ROOT,
		env: { ...process.env, ORYN_API_URL: API, ORYN_API_TOKEN: token },
		stdout: "pipe",
		stderr: "pipe",
	});
	const [out, err, code] = await Promise.all([
		new Response(p.stdout).text(),
		new Response(p.stderr).text(),
		p.exited,
	]);
	return { code, out: (out + err).trim() };
}

// ---------------------------------------------------------------- 线程与转录

interface ThreadView {
	id: string;
	state: string;
	kind?: string;
	orbId?: string;
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
interface ToolResult {
	tool: string;
	/** `oryn` 工具调用的操作名（按 toolCallId 对回调用）；其它工具为空串。 */
	op: string;
	text: string;
	isError: boolean;
}
interface Transcript {
	/** 带文本的助手消息 */
	replies: string[];
	calls: ToolCall[];
	results: ToolResult[];
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
 * 工具调用与结果。可发现的工具（宿主工具、MCP 工具）在 omp 里可能是 xd:// 设备：agent 以 `write xd://<工具>` 调用，
 * 参数是 content 里的 JSON（`read xd://<工具>` 只是读文档，不算调用）；结果的 `details.xdev.tool` 是真实工具名。
 * `oryn{op, args}` 的结果按 toolCallId 记下调用的 op。
 */
function transcriptOf(es: Entry[]): Transcript {
	const t: Transcript = { replies: [], calls: [], results: [] };
	const opOf = new Map<string, string>();
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
				const name = dev ?? String(b.name ?? "");
				t.calls.push({ name, args });
				if (name === "oryn" && isRecord(args) && typeof b.id === "string")
					opOf.set(b.id, String(args.op ?? ""));
			}
		} else if (m.role === "toolResult") {
			const xdev =
				isRecord(m.details) && isRecord(m.details.xdev)
					? m.details.xdev
					: undefined;
			const tool =
				xdev && xdev.mode === "execute"
					? String(xdev.tool ?? "")
					: String(m.toolName ?? "");
			t.results.push({
				tool,
				op: tool === "oryn" ? (opOf.get(String(m.toolCallId ?? "")) ?? "") : "",
				text: textOf(m.content),
				isError: m.isError === true,
			});
		}
	}
	return t;
}
/** 结果是不是 `oryn{op}` 的调用（op 为 REST operationId 或技能原生操作名）。 */
const isOp = (r: ToolResult, op: string) => r.tool === "oryn" && r.op === op;
/** 审计行是不是助手经 `oryn` 执行的某个 REST 操作（action api_call，摘要以 operationId 开头）。 */
const isApiCall = (a: AuditRow, op: string) =>
	a.action === "api_call" && a.summary.startsWith(`${op} `);
const transcript = async (tid: string) => transcriptOf(await entries(tid));
const callNames = (t: Transcript) =>
	t.calls.map((c) => c.name).join(",") || "无";
const lastReply = (t: Transcript) => clip(oneLine(t.replies.at(-1) ?? ""), 200);

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
		2_000,
	);
	note(`线程 ${tid} 回合结束于 ${th.state}（${sec(Date.now() - t0)}）`);
	return th;
}

/** 新建全局助手对话（第一条消息即 prompt），等首回合结束。 */
async function newAssistant(tag: string, prompt: string): Promise<ThreadView> {
	const r = await rjson<{ thread: ThreadView }>(
		"POST",
		"/api/v1/assistant/conversations",
		{ prompt },
	);
	const tid = r.thread.id;
	note(`${tag}：助手对话 ${tid}`);
	await waitTurn(tid, 0, TURN_TIMEOUT);
	return getThread(tid);
}

/** 同一对话补发一条消息并等回合结束。 */
async function say(tid: string, msg: string) {
	const before = (await entries(tid)).at(-1)?.seq ?? 0;
	const r = await cli(["thread", "send", tid, msg]);
	if (r.code !== 0)
		throw new Error(`oryn thread send：${clip(oneLine(r.out))}`);
	await waitTurn(tid, before, TURN_TIMEOUT);
}

/** 新对话发 prompt；转录不满足 ok 时补发 nudge 一次。返回对话与最终转录。 */
async function askUntil(
	tag: string,
	prompt: string,
	nudge: string,
	ok: (t: Transcript) => boolean,
) {
	const th = await newAssistant(tag, prompt);
	let t = await transcript(th.id);
	if (!ok(t)) {
		note(
			`${tag} 首回合不满足（工具：${callNames(t)}；回复：${lastReply(t)}），补发一次`,
		);
		await say(th.id, nudge);
		t = await transcript(th.id);
	}
	note(`${tag} 工具调用：${callNames(t)}；最后回复：${lastReply(t)}`);
	return { th, t };
}

/** 协调宿主沙箱里该助手对话所在 orb 的 omp 目录（root 直接读）。 */
async function coordOmpHome(tid: string): Promise<string> {
	const th = await getThread(tid);
	if (!th.orbId) throw new Error(`助手对话 ${tid} 没有 orb`);
	return `${COORD_DIR}/homes/${th.orbId}/.omp/agent`;
}

// ---------------------------------------------------------------- 最小的 MCP 服务（Streamable HTTP）

const MCP_TOKEN = "probe";
/** 服务器名与地址里都不含 MCP_TOKEN，好断言沙箱里的 mcp.json 不含鉴权头。 */
const MCP_NAME = "m19echo";
interface McpProbe {
	server: Server;
	url: string;
	/** 鉴权头不对的请求数 */
	unauthorized: number;
	/** tools/call 收到的 echo 文本 */
	calls: string[];
}

/** 一个 `echo` 工具；每个请求都要 `Authorization: Bearer probe`，否则 401。只回 application/json，不开 SSE。 */
function startMcp(): McpProbe {
	const p: McpProbe = {
		server: undefined as unknown as Server,
		url: "",
		unauthorized: 0,
		calls: [],
	};
	const answer = (msg: Record<string, unknown>): unknown => {
		const params = isRecord(msg.params) ? msg.params : {};
		switch (msg.method) {
			case "initialize":
				return {
					protocolVersion:
						typeof params.protocolVersion === "string"
							? params.protocolVersion
							: "2025-03-26",
					capabilities: { tools: {} },
					serverInfo: { name: MCP_NAME, version: "1.0.0" },
				};
			case "ping":
				return {};
			case "tools/list":
				return {
					tools: [
						{
							name: "echo",
							description: "Echo the given text back, prefixed with 'echo: '.",
							inputSchema: {
								type: "object",
								properties: { text: { type: "string" } },
								required: ["text"],
							},
						},
					],
				};
			case "tools/call": {
				const args = isRecord(params.arguments) ? params.arguments : {};
				const text = String(args.text ?? "");
				p.calls.push(text);
				return { content: [{ type: "text", text: `echo: ${text}` }] };
			}
			default:
				return undefined;
		}
	};
	p.server = Bun.serve({
		hostname: "127.0.0.1",
		port: 0,
		async fetch(req) {
			if (req.headers.get("authorization") !== `Bearer ${MCP_TOKEN}`) {
				p.unauthorized++;
				return new Response("unauthorized", { status: 401 });
			}
			if (req.method === "DELETE") return new Response(null, { status: 200 });
			if (req.method !== "POST")
				return new Response("method not allowed", { status: 405 });
			let body: unknown;
			try {
				body = await req.json();
			} catch {
				return new Response("bad json", { status: 400 });
			}
			const msgs = (Array.isArray(body) ? body : [body]).filter(isRecord);
			const out = msgs
				.filter((m) => m.id !== undefined && typeof m.method === "string")
				.map((m) => {
					const result = answer(m);
					return result === undefined
						? {
								jsonrpc: "2.0",
								id: m.id,
								error: { code: -32601, message: "method not found" },
							}
						: { jsonrpc: "2.0", id: m.id, result };
				});
			if (!out.length) return new Response(null, { status: 202 });
			return Response.json(Array.isArray(body) ? out : out[0], {
				headers: { "Mcp-Session-Id": `m19-${RUN}` },
			});
		},
	});
	p.url = `http://127.0.0.1:${p.server.port}/rpc`;
	return p;
}
let mcp: McpProbe | undefined;

// ---------------------------------------------------------------- 环境

function clearCoordDir() {
	if (!existsSync(COORD_DIR)) return;
	for (const f of readdirSync(COORD_DIR))
		rmSync(`${COORD_DIR}/${f}`, { recursive: true, force: true });
}

let gateway = "";
let imageBuilt = false;
/** 本机 omp 配置里的生图模型角色（空 = 没有，L1-image 跳过）。 */
let imageRole = "";

async function setup() {
	for (const b of ["orynd", "orbd", "oryn"])
		if (!existsSync(`${BIN}/${b}`))
			throw new Error(`缺少 ${BIN}/${b}：先 make build（或设 M19_BIN）`);
	if (sh("command -v docker").code !== 0) throw new Error("本机缺少 docker");
	if (!existsSync(MODELS_SRC))
		throw new Error(
			`缺少 ${MODELS_SRC}（设 M19_MODELS 指向含真实模型的 models.yml）`,
		);
	const want = JSON.parse(readFileSync(`${ROOT}/core/source.json`, "utf8")) as {
		tag: string;
		sha256: Record<string, string>;
	};
	const got = sh(`sha256sum ${q(OMP)} | cut -d' ' -f1`).stdout;
	check(
		got === want.sha256["linux-x64"],
		`build/core/x64/omp 是钉住的 Core ${want.tag}（sha256 ${got.slice(0, 12)}…；不一致先跑 scripts/fetch-core.sh）`,
	);
	check(
		sh(`ss -Hltn 'sport = :${API_PORT}'`).stdout === "",
		`127.0.0.1:${API_PORT} 空闲（设 M19_PORT 换端口）`,
	);
	gateway = sh(
		"docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}'",
	).stdout;
	check(/^\d+\.\d+\.\d+\.\d+$/.test(gateway), `docker0 网关地址 ${gateway}`);

	clearCoordDir();
	mkdirSync(LOGS, { recursive: true });
	// orb 镜像：基础镜像 + 本次的三个二进制
	mkdirSync(`${TMP}/image`, { recursive: true });
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
public_url: "http://localhost:${API_PORT}"
portal_domain: ""
data_dir: "${TMP}/data"
log_level: "debug"
idle: { agent_idle: "30m", user_idle: "30m", stop_after_frozen: "24h" }
orb_defaults: { provider: "local", type: "" }
coordinator: { omp_path: "${OMP}", dir: "${COORD_DIR}", egress_port: ${COORD_EGRESS}, model: "${MODEL}" }
`,
	);
	bg("orynd", [`${BIN}/orynd`, "--config", `${TMP}/orynd.yaml`]);
	await waitFor(
		"orynd 就绪",
		async () => (await fetch(`${API}/healthz`)).ok,
		30_000,
	);
	const dev = sh(
		`${q(`${BIN}/orynd`)} --config ${q(`${TMP}/orynd.yaml`)} admin create-device --name m19-accept --kind cli`,
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
		`全局配置 models.yml ← ${MODELS_SRC}（${put.status}${put.status === 200 && isRecord(put.json) && Array.isArray(put.json.extractedSecrets) ? `，抽取 ${put.json.extractedSecrets.length} 个密钥` : `：${clip(put.text)}`}）`,
	);
	if (existsSync(OMP_CONFIG_SRC)) {
		const local = Bun.YAML.parse(readFileSync(OMP_CONFIG_SRC, "utf8"));
		const roles =
			isRecord(local) && isRecord(local.modelRoles) ? local.modelRoles : {};
		if (typeof roles.image === "string") imageRole = roles.image;
	}
	const rolesYml = `modelRoles:\n  default: "${MODEL}"\n${imageRole ? `  image: "${imageRole}"\n` : ""}`;
	const cfg = await api(
		"PUT",
		"/api/v1/agent-config/files/config.yml",
		rolesYml,
	);
	check(
		cfg.status === 200,
		`全局配置 config.yml：modelRoles default=${MODEL}${imageRole ? `、image=${imageRole}（取自 ${OMP_CONFIG_SRC}）` : "（本机没有 image 角色）"}（${cfg.status}${cfg.status === 200 ? "" : `：${clip(cfg.text)}`}）`,
	);
	await api("GET", "/api/v1/models/catalog?refresh=true");
	const cat = await waitFor(
		`中央模型目录含 ${MODEL}`,
		async () => {
			const c = await rjson<{ models: { provider: string; id: string }[] }>(
				"GET",
				"/api/v1/models/catalog",
			);
			return c.models.some((m) => `${m.provider}/${m.id}` === MODEL) ? c : null;
		},
		300_000,
		3_000,
	);
	note(`中央模型目录 ${cat.models.length} 个模型，含 ${MODEL}`);
	const coord = await waitFor(
		"协调宿主可用",
		async () => {
			const r = await rjson<{ available: boolean; reason?: string }>(
				"GET",
				"/api/v1/coordinator",
			);
			return r.available ? r : null;
		},
		120_000,
		1_000,
	);
	check(coord.available, `协调宿主可用（目录 ${COORD_DIR}）`);
	const st = await rjson<{ webAccess: boolean }>(
		"GET",
		"/api/v1/settings/assistant",
	);
	check(st.webAccess, "助手设置：联网缺省开启");
	envReady = true;
}

// ---------------------------------------------------------------- 检查

/** 主机自身能否访问公网（不能则 L1-web 只能记为未验证）。 */
async function hostOnline(): Promise<boolean> {
	try {
		const r = await fetch("https://example.com/", {
			signal: AbortSignal.timeout(15_000),
		});
		return (await r.text()).includes("Example Domain");
	} catch {
		return false;
	}
}

/** L1-web：助手用内置 read 读公网 https 地址（经 orynd 的 CONNECT 代理，验证假设 A1）。 */
async function l1Web() {
	if (!(await hostOnline()))
		skip("本机访问不了 https://example.com/（没有公网），假设 A1 未证实");
	const got = (t: Transcript) =>
		t.results.some(
			(r) => r.tool === "read" && r.text.includes("Example Domain"),
		);
	const { t } = await askUntil(
		"L1-web",
		"请用 read 工具读取 https://example.com/ 这个网址，然后复述这个网页的标题。只用 read 工具，不要用别的工具。",
		"请现在调用 read 工具，path 参数就是 https://example.com/ ，读到后复述网页标题。",
		got,
	);
	const reads = t.results.filter((r) => r.tool === "read");
	for (const r of reads)
		note(`read 结果（isError=${r.isError}）：${clip(oneLine(r.text), 200)}`);
	check(got(t), "read 的工具结果含 Example Domain");
}

/** L1-deny：解析到回环的域名经正向代理被拒（公网判定在连接时对实际 IP 做）。 */
async function l1Deny() {
	const url = `http://127.0.0.1.nip.io:${API_PORT}/`;
	const r = sh(`getent hosts 127.0.0.1.nip.io`);
	if (!r.stdout.startsWith("127.0.0.1"))
		skip(
			`本机解析不了 127.0.0.1.nip.io（getent：${clip(oneLine(r.out)) || "空"}）`,
		);
	const logBefore = orynLog().length;
	const tried = (t: Transcript) => t.results.some((x) => x.tool === "read");
	const { t } = await askUntil(
		"L1-deny",
		`请用 read 工具读取 ${url} 这个网址，把读到的内容或报错原样告诉我。只用 read 工具，不要用别的工具，失败也不要重试别的地址。`,
		`请现在调用 read 工具，path 参数就是 ${url} ，然后把结果或报错原样告诉我。`,
		tried,
	);
	const reads = t.results.filter((x) => x.tool === "read");
	for (const x of reads)
		note(`read 结果（isError=${x.isError}）：${clip(oneLine(x.text), 240)}`);
	check(reads.length > 0, "助手调用了 read");
	// omp 的 read 读 URL 失败时不置 isError，而是在结果里写 `Method: failed` 与 HTTP 状态码（Core 18.19 的行为）
	expect(
		reads.every(
			(x) =>
				x.isError || (/Method: failed/.test(x.text) && /HTTP 403/.test(x.text)),
		),
		"read 的工具结果是错误（isError，或 Method: failed 且 HTTP 403）",
	);
	expect(
		!reads.some((x) => x.text.includes("<title>") || /Oryn/i.test(x.text)),
		"没有读到 orynd 自己的页面",
	);
	const log = orynLog().slice(logBefore);
	const line = log
		.split("\n")
		.find((l) => l.includes("destination is not a public address"));
	check(
		!!line,
		`orynd 日志有 destination is not a public address${line ? `：${clip(oneLine(line), 240)}` : ""}`,
	);
}

/** L1-mcp：全局 mcp.json 的 HTTP 服务器经 /mcp/<key> 路由到助手，鉴权头由 orynd 注入（验证假设 A2）。 */
async function l1Mcp() {
	mcp = startMcp();
	note(
		`MCP 服务 ${mcp.url}（工具 echo，要求 Authorization: Bearer ${MCP_TOKEN}）`,
	);
	const cfg = {
		mcpServers: {
			[MCP_NAME]: {
				type: "http",
				url: mcp.url,
				headers: { Authorization: `Bearer ${MCP_TOKEN}` },
			},
		},
	};
	const put = await api(
		"PUT",
		"/api/v1/agent-config/files/mcp.json",
		JSON.stringify(cfg, null, 2),
	);
	check(
		put.status === 200,
		`全局配置 mcp.json（${put.status}${put.status === 200 ? "" : `：${clip(put.text)}`}）`,
	);
	const servers = await rjson<{
		servers: {
			name: string;
			usable: boolean;
			enabled: boolean;
			reason?: string;
		}[];
	}>("GET", "/api/v1/assistant/mcp-servers");
	const s = servers.servers.find((x) => x.name === MCP_NAME);
	check(
		!!s && s.usable && s.enabled,
		`GET /assistant/mcp-servers：${MCP_NAME} 可用且开启（${JSON.stringify(s ?? null)}）`,
	);

	const nonce = `m19-${RUN}-${Math.random().toString(36).slice(2, 8)}`;
	const tool = `mcp__${MCP_NAME}_echo`;
	const echoed = (t: Transcript) =>
		t.results.some((r) => r.text.includes(`echo: ${nonce}`));
	const { th, t } = await askUntil(
		"L1-mcp",
		`请调用 MCP 工具 ${tool}（如果它是 xd:// 设备，就按设备的用法调用），参数 text 填 "${nonce}"，然后把工具返回的内容原样告诉我。不要用别的工具做别的事。`,
		`请现在调用 ${tool}，参数 {"text": "${nonce}"}，再把返回内容原样告诉我。`,
		echoed,
	);
	for (const r of t.results.filter(
		(r) => r.text.includes(nonce) || r.tool.startsWith("mcp__"),
	))
		note(
			`${r.tool} 结果（isError=${r.isError}）：${clip(oneLine(r.text), 200)}`,
		);
	check(echoed(t), `工具结果回显 echo: ${nonce}`);
	check(
		mcp.calls.includes(nonce),
		`MCP 服务收到带鉴权头的 tools/call（共 ${mcp.calls.length} 次）`,
	);
	expect(
		mcp.unauthorized === 0,
		`MCP 服务没有收到鉴权头不对的请求（${mcp.unauthorized} 次）`,
	);

	const home = await coordOmpHome(th.id);
	const file = `${home}/mcp.json`;
	check(existsSync(file), `沙箱里有 ${file}`);
	const content = readFileSync(file, "utf8");
	note(`沙箱 mcp.json：${clip(oneLine(content), 300)}`);
	check(
		/\/mcp\/m\d+/.test(content) && !content.includes(mcp.url),
		"沙箱 mcp.json 指向 orynd 的 /mcp/<key> 路由，不含真实地址",
	);
	check(
		!content.includes(MCP_TOKEN),
		`沙箱 mcp.json 不含主人配置的 ${MCP_TOKEN}`,
	);
	check(!/authorization/i.test(content), "沙箱 mcp.json 不含 Authorization 头");
	// 每个服务器只带一个模板头 X-Oryn-MCP-Token: ${ORYN_MCP_TOKEN}（凭据值由 orynd 只放进全局助手 omp 的环境）
	const parsed = JSON.parse(content) as {
		mcpServers: Record<string, { headers?: Record<string, string> }>;
	};
	const tmpl = `\${ORYN_MCP_TOKEN}`;
	check(
		Object.values(parsed.mcpServers).every(
			(s) =>
				JSON.stringify(s.headers ?? {}) ===
				JSON.stringify({ "X-Oryn-MCP-Token": tmpl }),
		),
		`每个服务器只带 X-Oryn-MCP-Token: ${tmpl} 这一个模板头`,
	);

	// 沙箱里这个对话的 omp 进程：凭据值不在 mcp.json 里；不带凭据直接请求 /mcp/m0 得 403
	const proc = sandboxProc(th.id);
	check(
		!!proc,
		`找到对话 ${th.id} 在沙箱里的 omp 进程（pid ${proc?.pid ?? "无"}）`,
	);
	const token = proc?.env.ORYN_MCP_TOKEN ?? "";
	const base = proc?.env.ORYN_MCP_BASE ?? "";
	check(
		token.length >= 16 && !content.includes(token),
		`omp 环境里有 ORYN_MCP_TOKEN（${token.length} 字符），mcp.json 不含凭据值本身`,
	);
	const key = /\/mcp\/(m\d+)/.exec(content)?.[1] ?? "m0";
	// 异步跑：带正确凭据的请求会经 orynd 回到本进程里的 MCP 服务，同步 spawn 会卡住事件循环
	const curl = async (headers: string[]) => {
		const p = Bun.spawn(
			[
				"nsenter",
				"-t",
				String(proc?.pid),
				"-n",
				"curl",
				"-s",
				"--max-time",
				"20",
				"-o",
				"/dev/null",
				"-w",
				"%{http_code}",
				"-X",
				"POST",
				"-H",
				"Content-Type: application/json",
				...headers.flatMap((h) => ["-H", h]),
				"--data",
				'{"jsonrpc":"2.0","id":1,"method":"ping"}',
				`${base}/mcp/${key}`,
			],
			{ stdout: "pipe", stderr: "ignore" },
		);
		const out = await new Response(p.stdout).text();
		await p.exited;
		return out.trim();
	};
	const bare = await curl([]);
	check(bare === "403", `沙箱网络里不带凭据 POST ${base}/mcp/${key} → ${bare}`);
	const forged = await curl(["X-Oryn-MCP-Token: forged-token-value"]);
	check(forged === "403", `带伪造凭据 → ${forged}`);
	const ok = await curl([`X-Oryn-MCP-Token: ${token}`]);
	note(`带正确凭据 → ${ok}`);
	expect(ok === "200", "带正确凭据经 orynd 到达 MCP 服务（200）");
}

/** 协调宿主沙箱里某个对话的 omp 进程（按环境变量 ORYN_THREAD_ID 找）及其环境。 */
function sandboxProc(
	tid: string,
): { pid: number; env: Record<string, string> } | undefined {
	for (const pid of readdirSync("/proc")) {
		if (!/^\d+$/.test(pid)) continue;
		try {
			const raw = readFileSync(`/proc/${pid}/environ`, "utf8");
			if (!raw.includes(`ORYN_THREAD_ID=${tid}\0`)) continue;
			const env = Object.fromEntries(
				raw
					.split("\0")
					.filter(Boolean)
					.map((kv) => [
						kv.slice(0, kv.indexOf("=")),
						kv.slice(kv.indexOf("=") + 1),
					]),
			);
			return { pid: Number(pid), env };
		} catch {
			// 已退出或无权限
		}
	}
	return undefined;
}

// ---------------------------------------------------------------- 步骤 5.1 的辅助

interface AuditRow {
	action: string;
	origin: string;
	ok: boolean;
	actorThreadId?: string;
	targetThreadId?: string;
	summary: string;
	error?: string;
}
const auditOf = async (tid: string) =>
	(
		await rjson<{ actions: AuditRow[] }>(
			"GET",
			`/api/v1/automation/actions?thread=${tid}&limit=200`,
		)
	).actions;

interface NotificationView {
	id: string;
	kind: string;
	threadId?: string;
	title: string;
	body: string;
	resolvedAt?: number;
	quickAnswer?: {
		question: string;
		options: { i: number; label: string; role?: string }[];
	};
}
const notifications = async () =>
	(
		await rjson<{ notifications: NotificationView[] }>(
			"GET",
			"/api/v1/notifications?limit=100",
		)
	).notifications;

/** 用户消息的文本（自动消息也是 user 消息）。 */
function userText(e: Entry): string | undefined {
	const m = e.type === "message" ? e.payload.message : undefined;
	return isRecord(m) && m.role === "user" ? textOf(m.content) : undefined;
}

/** 等 afterSeq 之后出现满足 pred 的用户消息，返回该条目。 */
async function waitUserMsg(
	tid: string,
	what: string,
	pred: (text: string) => boolean,
	afterSeq: number,
	timeoutMs: number,
	intervalMs = 1_000,
): Promise<Entry> {
	return waitFor(
		`线程 ${tid} 出现${what}`,
		async () =>
			(await entries(tid, afterSeq)).find((e) => {
				const t = userText(e);
				return t !== undefined && pred(t);
			}),
		timeoutMs,
		intervalMs,
	);
}

/** 等线程回到静止（idle / awaiting_input）且没有排队中的回合；返回线程。 */
const waitSettled = (tid: string, timeoutMs: number) =>
	waitFor(
		`线程 ${tid} 静止`,
		async () => {
			const t = await getThread(tid);
			if (t.state === "error") throw new Error(`线程 ${tid} 停在 error`);
			return SETTLED[t.state] ? t : null;
		},
		timeoutMs,
		2_000,
	);

const lastSeq = async (tid: string) => (await entries(tid)).at(-1)?.seq ?? 0;

let mainId = "";
async function ensureMain(): Promise<string> {
	if (mainId) return mainId;
	const r = await rjson<{ thread: ThreadView & { main?: boolean } }>(
		"GET",
		"/api/v1/assistant/main",
	);
	check(
		r.thread.main === true,
		`GET /assistant/main → ${r.thread.id}（main=true）`,
	);
	mainId = r.thread.id;
	return mainId;
}

// ---------------------------------------------------------------- 第一层（续）

/** L1-search：内置 web_search 经正向代理有结果。 */
async function l1Search() {
	if (!(await hostOnline())) skip("本机没有公网，web_search 无法验证");
	const got = (t: Transcript) =>
		t.results.some(
			(r) =>
				r.tool === "web_search" && !r.isError && /https?:\/\/\S+/.test(r.text),
		);
	const { t } = await askUntil(
		"L1-search",
		"请调用 web_search 工具搜索「Bun JavaScript runtime official site」，然后用一句话告诉我第一条结果的标题与网址。只用 web_search，不要用别的工具。",
		"请现在调用 web_search，query 填 Bun JavaScript runtime official site，然后告诉我第一条结果的标题与网址。",
		got,
	);
	for (const r of t.results.filter((r) => r.tool === "web_search"))
		note(
			`web_search 结果（isError=${r.isError}）：${clip(oneLine(r.text), 240)}`,
		);
	check(got(t), "web_search 的工具结果不是错误且含网址");
}

/** L1-image：generate_image 后 save_file，文件库里多一个图片。 */
async function l1Image() {
	if (!imageRole)
		skip(
			`本机 omp 配置（${OMP_CONFIG_SRC}）没有 modelRoles.image，没有生图模型`,
		);
	note(`生图角色 image=${imageRole}`);
	type FileRow = { id: string; name: string; mime: string; size: number };
	const files = async () =>
		(await rjson<{ files: FileRow[] }>("GET", "/api/v1/assistant/files")).files;
	const before = new Set((await files()).map((f) => f.id));
	const name = `m19-${RUN}.png`;
	const saved = (t: Transcript) =>
		t.results.some((r) => r.tool === "save_file" && !r.isError);
	const { t } = await askUntil(
		"L1-image",
		`请先调用 generate_image 生成一张很简单的小图：白色背景上一个红色实心圆。生成后调用 save_file，path 填 generate_image 结果里给出的图片文件路径，name 填 ${name}。最后告诉我文件库里的文件 id。`,
		`请调用 save_file，path 填刚才 generate_image 结果里的图片文件路径（若还没生成，先调用 generate_image），name 填 ${name}。`,
		saved,
	);
	for (const r of t.results.filter(
		(r) => r.tool === "generate_image" || r.tool === "save_file",
	))
		note(
			`${r.tool} 结果（isError=${r.isError}）：${clip(oneLine(r.text), 240)}`,
		);
	check(
		t.results.some((r) => r.tool === "generate_image" && !r.isError),
		"generate_image 成功",
	);
	check(saved(t), "save_file 成功");
	const added = (await files()).filter((f) => !before.has(f.id));
	note(
		`文件库新增：${added.map((f) => `${f.id} ${f.name} ${f.mime} ${f.size}B`).join("；") || "无"}`,
	);
	check(
		added.some((f) => f.mime.startsWith("image/") && f.size > 0),
		"文件库里多了一个图片",
	);
}

// ---------------------------------------------------------------- 第三层

/** L3-project 开出的工作线程（L3-confirm 删除它）。 */
let projectThread = "";

/** L3-project：助手经 oryn 的 createProject 新建项目并在其中开线程，线程在 docker orb 里跑完首回合。 */
async function l3Project() {
	const pname = `m19-p-${RUN}`;
	const done = (t: Transcript) =>
		t.results.some((r) => isOp(r, "createProject") && !r.isError) &&
		t.results.some((r) => r.tool === "create_thread" && !r.isError);
	const { th, t } = await askUntil(
		"L3-project",
		`请调用 oryn 工具新建一个项目：op 填 createProject，args 填 {"body":{"name":"${pname}"}}（不要仓库，其它字段都不填）。成功后调用 create_thread 在这个项目里开一个工作线程：project 填 ${pname}，title 填 m19 项目线程，prompt 填「只回复 OK 两个字母，不要调用任何工具。」。开完线程就结束这一轮，不用等它回报。`,
		`请调用 create_thread：project 填 ${pname}，title 填 m19 项目线程，prompt 填「只回复 OK 两个字母，不要调用任何工具。」（项目若还没有，先调用 oryn 工具建它：op 填 createProject，args 填 {"body":{"name":"${pname}"}}）。`,
		done,
	);
	for (const r of t.results.filter(
		(r) => isOp(r, "createProject") || r.tool === "create_thread",
	))
		note(
			`${r.op || r.tool} 结果（isError=${r.isError}）：${clip(oneLine(r.text), 200)}`,
		);
	check(done(t), "oryn createProject 与 create_thread 都成功");
	const projects = await rjson<{ projects: { id: string; name: string }[] }>(
		"GET",
		"/api/v1/projects",
	);
	const p = projects.projects.find((x) => x.name === pname);
	check(!!p, `GET /projects 有 ${pname}（${p?.id ?? "无"}）`);
	const audit = await auditOf(th.id);
	const created = audit.find(
		(a) => a.action === "create_thread" && a.ok && a.targetThreadId,
	);
	check(
		!!created,
		`审计有助手的 create_thread（目标 ${created?.targetThreadId ?? "无"}）`,
	);
	expect(
		audit.some((a) => isApiCall(a, "createProject") && a.ok),
		"审计有 api_call createProject /api/v1/projects",
	);
	const wid = created?.targetThreadId ?? "";
	const w = await rjson<ThreadView & { projectId?: string; kind?: string }>(
		"GET",
		`/api/v1/threads/${wid}`,
	);
	check(
		w.projectId === p?.id,
		`线程 ${wid} 属于项目 ${pname}（projectId=${w.projectId ?? "无"}）`,
	);
	await waitTurn(wid, 0, ORB_TURN_TIMEOUT);
	const wt = await transcript(wid);
	note(
		`工作线程 ${wid}（orb ${(await getThread(wid)).orbId ?? "?"}）回复：${lastReply(wt)}`,
	);
	check(
		/OK/i.test(wt.replies.join("\n")),
		"工作线程在 orb 里跑完首回合并回复 OK",
	);
	projectThread = wid;
}

/** L3-confirm：经 oryn 的 deleteThread 删线程走确认卡：对话 awaiting_input、通知带是 / 否；作答“是”后线程被删、对话收到确认结果、审计 origin=owner。 */
async function l3Confirm() {
	const wid = projectThread;
	const asked = (t: Transcript) =>
		t.results.some((r) => isOp(r, "deleteThread"));
	const { th, t } = await askUntil(
		"L3-confirm",
		`请调用 oryn 工具删除线程 ${wid}：op 填 deleteThread，args 填 {"id":"${wid}"}。调用之后告诉我结果即可，不要做别的。`,
		`请现在调用 oryn 工具，op 填 deleteThread，args 填 {"id":"${wid}"}。`,
		asked,
	);
	for (const r of t.results.filter((r) => isOp(r, "deleteThread")))
		note(
			`oryn deleteThread 结果（isError=${r.isError}）：${clip(oneLine(r.text), 240)}`,
		);
	check(
		t.results.some(
			(r) => isOp(r, "deleteThread") && /awaiting_owner/.test(r.text),
		),
		"oryn deleteThread 返回 awaiting_owner（没有直接执行）",
	);
	check(
		(await api("GET", `/api/v1/threads/${wid}`)).status === 200,
		`确认之前线程 ${wid} 仍在`,
	);
	const st = await getThread(th.id);
	check(
		st.state === "awaiting_input",
		`助手对话处于 awaiting_input（${st.state}）`,
	);
	const n = await waitFor(
		"确认卡的 input 通知",
		async () =>
			(await notifications()).find(
				(x) => x.kind === "input" && x.threadId === th.id && !x.resolvedAt,
			),
		30_000,
		1_000,
	);
	const opts = n.quickAnswer?.options ?? [];
	note(
		`通知 ${n.id}：${clip(oneLine(`${n.title} / ${n.body}`), 200)}；选项 ${opts.map((o) => `${o.i}:${o.label}${o.role ? `(${o.role})` : ""}`).join(" ")}`,
	);
	const yes = opts.find((o) => o.role === "yes");
	check(
		!!yes && opts.some((o) => o.role === "no"),
		"通知可一触作答且带“是 / 否”",
	);
	const seq = await lastSeq(th.id);
	const ans = await api("POST", `/api/v1/notifications/${n.id}/answer`, {
		option: yes?.i,
	});
	check(
		ans.status === 200,
		`作答“是”→ ${ans.status}${ans.status === 200 ? "" : ` ${clip(ans.text)}`}`,
	);
	const msg = await waitUserMsg(
		th.id,
		"确认结果自动消息",
		(s) => s.startsWith("[Oryn confirmation · automatic]"),
		seq,
		120_000,
	);
	const text = userText(msg) ?? "";
	note(`自动消息：${clip(oneLine(text), 240)}`);
	check(
		text.startsWith(
			`[Oryn confirmation · automatic] api_call deleteThread /api/v1/threads/${wid}: approved and done.`,
		),
		`对话收到 [Oryn confirmation · automatic] api_call deleteThread /api/v1/threads/${wid}: approved and done.`,
	);
	const gone = await api("GET", `/api/v1/threads/${wid}`);
	check(gone.status === 404, `线程 ${wid} 已删除（GET → ${gone.status}）`);
	const row = (await auditOf(th.id)).find((a) => isApiCall(a, "deleteThread"));
	note(`审计：${JSON.stringify(row ?? null)}`);
	check(
		row?.origin === "owner" && row.ok,
		"审计里 api_call deleteThread 的 origin=owner 且成功",
	);
	await waitSettled(th.id, TURN_TIMEOUT);
}

/** L3-space：助手经 oryn 的 createSpace 新建 Space，向其协调对话发话，Space 那一轮结束后回报到达助手对话。 */
async function l3Space() {
	const sname = `m19-s-${RUN}`;
	const sent = (t: Transcript) =>
		t.results.some((r) => isOp(r, "createSpace") && !r.isError) &&
		t.results.some((r) => r.tool === "send_message" && !r.isError);
	const { th, t } = await askUntil(
		"L3-space",
		`请调用 oryn 工具新建一个 Space：op 填 createSpace，args 填 {"body":{"name":"${sname}","goal":"M19 验收"}}。成功后调用 send_message，space 参数填 ${sname}，message 填「请只回复 PONG 这个词，不要调用任何工具。」。发完就结束这一轮，等它的回报。`,
		`请调用 send_message：space 填 ${sname}，message 填「请只回复 PONG 这个词，不要调用任何工具。」（Space 若还没有，先调用 oryn 工具建它：op 填 createSpace，args 填 {"body":{"name":"${sname}","goal":"M19 验收"}}）。`,
		sent,
	);
	for (const r of t.results.filter(
		(r) => isOp(r, "createSpace") || r.tool === "send_message",
	))
		note(
			`${r.op || r.tool} 结果（isError=${r.isError}）：${clip(oneLine(r.text), 200)}`,
		);
	check(sent(t), "oryn createSpace 与 send_message 都成功");
	const spaces = await rjson<{
		spaces: { id: string; name: string; threadId: string }[];
	}>("GET", "/api/v1/spaces");
	const sp = spaces.spaces.find((s) => s.name === sname);
	check(!!sp, `GET /spaces 有 ${sname}（协调对话 ${sp?.threadId ?? "无"}）`);
	const sid = sp?.threadId ?? "";
	await waitUserMsg(
		sid,
		"助手发来的消息",
		(s) => s.includes("PONG"),
		0,
		60_000,
	);
	const report = await waitUserMsg(
		th.id,
		"Space 的回报",
		(s) => s.startsWith("[Oryn thread report") && s.includes(sid),
		0,
		TURN_TIMEOUT,
		2_000,
	);
	const st = await transcript(sid);
	note(`Space 协调对话回复：${lastReply(st)}`);
	note(`回报：${clip(oneLine(userText(report) ?? ""), 240)}`);
	// Space 协调者会把助手发来的话当作其它 agent 的消息（不可信），可能不照做；R8 只要求那一轮结束后回报到达
	check(st.replies.length > 0, "Space 协调对话那一轮有回复");
	expect(
		/"outcome":"completed"/.test(userText(report) ?? ""),
		"回报的 outcome 是 completed，带着 Space 的回复",
	);
	await waitSettled(th.id, TURN_TIMEOUT);
}

// ---------------------------------------------------------------- 主线

/** MAIN-handoff：每分钟、开着“交给助手”的定时运行结束 → 约 15 秒内主线收到合并结果，助手说话或沉默；该运行没有 done 通知。 */
async function mainHandoff() {
	const main = await ensureMain();
	const seq0 = await lastSeq(main);
	const sch = await rjson<{ id: string; handToAssistant: boolean }>(
		"POST",
		"/api/v1/schedules",
		{
			mode: "new_thread",
			name: `m19-h-${RUN}`,
			kind: "every",
			every: "1m",
			prompt: "只回复一句：M19 定时检查一切正常。不要调用任何工具。",
			provider: "local",
			model: { provider: MODEL_PROVIDER, id: MODEL_ID },
			handToAssistant: true,
		},
	);
	check(
		sch.handToAssistant,
		`定时 ${sch.id}：every 1m、new_thread、handToAssistant=true`,
	);
	try {
		const run = await waitFor(
			"第一次运行建出线程",
			async () =>
				(
					await rjson<{ runs: { threadId?: string; status: string }[] }>(
						"GET",
						`/api/v1/schedules/${sch.id}/runs`,
					)
				).runs.find((r) => r.threadId),
			150_000,
			2_000,
		);
		// 只要一次运行：拿到第一次就暂停
		await rjson("POST", `/api/v1/schedules/${sch.id}/pause`);
		const rid = run.threadId ?? "";
		note(`第一次运行线程 ${rid}；定时已暂停`);
		await waitTurn(rid, 0, ORB_TURN_TIMEOUT);
		const ended = Date.now();
		const msg = await waitUserMsg(
			main,
			"[Oryn automation results",
			(s) => s.startsWith("[Oryn automation results") && s.includes(rid),
			seq0,
			180_000,
		);
		const delay = Date.now() - ended;
		note(
			`主线收到自动消息（运行结束后约 ${sec(delay)}）：${clip(oneLine(userText(msg) ?? ""), 240)}`,
		);
		expect(
			delay <= 45_000,
			"运行结束后 45 秒内到达（合并窗口 15 秒，含轮询间隔）",
		);
		await waitSettled(main, TURN_TIMEOUT);
		const after = transcriptOf(await entries(main, msg.seq));
		const silent = after.calls.some((c) => c.name === "stay_silent");
		note(
			`主线这一回合：${silent ? `stay_silent ${JSON.stringify(after.calls.find((c) => c.name === "stay_silent")?.args ?? {})}` : `助手说：${lastReply(after)}`}`,
		);
		check(
			silent || after.replies.length > 0,
			"主线出现一条助手消息或一条沉默记录",
		);
		const done = (await notifications()).filter(
			(n) => n.kind === "done" && n.threadId === rid,
		);
		check(
			done.length === 0,
			`运行线程 ${rid} 没有 done 通知（${done.length} 条）`,
		);
	} finally {
		await api("DELETE", `/api/v1/schedules/${sch.id}`);
	}
}

/** MAIN-episode：主线一段满 8 个回合后停 agent，下一段的系统提示词带上一段的消息，转录里有 reset_boundary。 */
async function mainEpisode() {
	const main = await ensureMain();
	const marker = `琥珀-${RUN}-${Math.random().toString(36).slice(2, 6)}`;
	const logFrom = orynLog().length;
	const seq0 = await lastSeq(main);
	// 近况只带最近 12 条：口令放进这一段的每条消息，换段时无论落在哪个回合都在近况里
	const ended = () => /主线一段结束/.test(orynLog().slice(logFrom));
	for (let i = 1; i <= 9 && !ended(); i++) {
		await say(
			main,
			`第 ${i} 条测试消息，口令是 ${marker}。只回复 OK，不要调用任何工具。`,
		);
		await sleep(2_000);
	}
	const line = orynLog()
		.slice(logFrom)
		.split("\n")
		.find((l) => l.includes("主线一段结束"));
	check(
		!!line,
		`orynd 日志：${clip(oneLine(line ?? "没有「主线一段结束」"), 200)}`,
	);
	await say(
		main,
		"我之前让你记住的口令是什么？只根据系统提示词里的「主线近况」回答，原样写出口令，不要调用任何工具。",
	);
	const prompt = sh(
		`find ${q(COORD_DIR)} -path '*/prompts/*' -name ${q(`${main}.md`)} | head -1`,
	).stdout;
	check(!!prompt, `主线的系统提示词文件 ${prompt || "（没找到）"}`);
	const body = readFileSync(prompt, "utf8");
	const brief = body.slice(body.indexOf("## 主线近况"));
	note(`主线近况：${clip(oneLine(brief), 300)}`);
	check(body.includes("## 主线近况"), "系统提示词带「主线近况」");
	check(
		brief.includes(marker),
		`第二段的系统提示词含上一段的消息（口令 ${marker}）`,
	);
	const es = await entries(main, seq0);
	const resets = es.filter((e) => e.type === "reset_boundary");
	check(
		resets.length > 0,
		`转录里有 reset_boundary（本检查期间 ${resets.length} 个，seq ${resets.map((e) => e.seq).join(",")}）`,
	);
	const t = transcriptOf(es.slice(es.indexOf(resets.at(-1) as Entry)));
	note(`第二段的回答：${lastReply(t)}`);
	expect(t.replies.join("\n").includes(marker), "第二段的回答原样写出了口令");
}

// ---------------------------------------------------------------- 第二层

interface ComputerView {
	orbId?: string;
	state?: string;
	toolsReady: boolean;
}
const computer = () => rjson<ComputerView>("GET", "/api/v1/assistant/computer");

/** 唤醒（必要时创建）助手的电脑，直到 ready 且工具定义已缓存。 */
async function wakeComputer(): Promise<ComputerView> {
	return waitFor(
		"助手的电脑就绪",
		async () => {
			const r = await api("POST", "/api/v1/assistant/computer/wake");
			if (r.status >= 300) {
				note(
					`POST /assistant/computer/wake → ${r.status} ${clip(oneLine(r.text), 160)}`,
				);
				return null;
			}
			const c = await computer();
			return c.state === "ready" && c.toolsReady ? c : null;
		},
		900_000,
		10_000,
	);
}

/** 在新的助手对话里要求调用一个电脑工具，返回该工具的结果。 */
async function onComputer(
	tag: string,
	tool: string,
	prompt: string,
	nudge: string,
	ok: (text: string) => boolean,
) {
	const got = (t: Transcript) =>
		t.results.some((r) => r.tool === tool && !r.isError && ok(r.text));
	const { t } = await askUntil(tag, prompt, nudge, got);
	for (const r of t.results.filter((r) => r.tool.startsWith("computer_")))
		note(
			`${r.tool} 结果（isError=${r.isError}）：${clip(oneLine(r.text), 240)}`,
		);
	check(got(t), `${tool} 的结果符合预期`);
}

/** L2-bash：computer_bash 执行 uname -a，结果含内核版本（docker orb 与宿主同一内核）。 */
async function l2Bash() {
	const t0 = Date.now();
	const c = await wakeComputer();
	note(
		`助手的电脑 ${c.orbId}：${c.state}，工具已缓存（${sec(Date.now() - t0)}）`,
	);
	const orbs = await rjson<{ orbs: { id: string; computer?: boolean }[] }>(
		"GET",
		"/api/v1/orbs",
	);
	check(
		orbs.orbs
			.filter((o) => o.computer)
			.map((o) => o.id)
			.join(",") === c.orbId,
		"全库只有这一台 computer=true 的 orb",
	);
	const kernel = sh("uname -r").stdout;
	await onComputer(
		"L2-bash",
		"computer_bash",
		"请调用 computer_bash 执行命令 uname -a（在你的电脑上），然后把输出原样告诉我。",
		"请现在调用 computer_bash，command 填 uname -a。",
		(s) => s.includes("Linux") && s.includes(kernel),
	);
}

/** L2-eval：computer_eval 跑一段 Python。 */
async function l2Eval() {
	await onComputer(
		"L2-eval",
		"computer_eval",
		"请调用 computer_eval 在你的电脑上跑一段 Python（language 用 py）：print(sum(range(1, 101)) * 3)，然后告诉我输出。",
		"请现在调用 computer_eval，language 填 py，code 填 print(sum(range(1, 101)) * 3)。",
		(s) => s.includes("15150"),
	);
}

/** L2-freeze：暂停电脑后再调用，调用先唤醒再返回。 */
async function l2Freeze() {
	const before = await computer();
	const p = await api("POST", "/api/v1/assistant/computer/pause");
	check(p.status < 300, `POST /assistant/computer/pause → ${p.status}`);
	const frozen = await waitFor(
		"电脑冻结",
		async () => {
			const c = await computer();
			return c.state === "frozen" ? c : null;
		},
		120_000,
		2_000,
	);
	note(`电脑 ${frozen.orbId}：${frozen.state}`);
	const nonce = `m19-freeze-${RUN}`;
	await onComputer(
		"L2-freeze",
		"computer_bash",
		`请调用 computer_bash 在你的电脑上执行 echo ${nonce}，然后把输出告诉我。`,
		`请现在调用 computer_bash，command 填 echo ${nonce}。`,
		(s) => s.includes(nonce),
	);
	const after = await computer();
	note(`调用后电脑 ${after.orbId}：${after.state}`);
	check(
		after.orbId === before.orbId && after.state === "ready",
		"同一台电脑被唤醒回 ready",
	);
	const woke = orynLog()
		.split("\n")
		.filter((l) => l.includes(before.orbId ?? "?") && /wake|唤醒/i.test(l));
	note(
		`orynd 日志里该 orb 的唤醒记录 ${woke.length} 条${woke.length ? `：${clip(oneLine(woke.at(-1) ?? ""), 200)}` : ""}`,
	);
}

const CHECKS: Check[] = [
	{ id: "L1-web", title: "第一层：read 读公网 https 地址", run: l1Web },
	{ id: "L1-deny", title: "第一层：正向代理拒绝非公网目标", run: l1Deny },
	{
		id: "L1-mcp",
		title: "第一层：HTTP MCP 工具经 orynd 注入鉴权头",
		run: l1Mcp,
	},
	{ id: "L1-search", title: "第一层：web_search 有结果", run: l1Search },
	{
		id: "L1-image",
		title: "第一层：generate_image 后存进文件库",
		run: l1Image,
	},
	{ id: "L3-project", title: "第三层：新建项目并在其中开线程", run: l3Project },
	{
		id: "L3-confirm",
		title: "第三层：删除线程走确认卡，主人确认后执行",
		needs: ["L3-project"],
		run: l3Confirm,
	},
	{
		id: "L3-space",
		title: "第三层：新建 Space、向其协调对话发话并收到回报",
		run: l3Space,
	},
	{
		id: "MAIN-handoff",
		title: "主线：交给助手的定时结果进主线",
		run: mainHandoff,
	},
	{
		id: "MAIN-episode",
		title: "主线：分段、近况与 reset_boundary",
		run: mainEpisode,
	},
	{ id: "L2-bash", title: "第二层：computer_bash 执行 uname -a", run: l2Bash },
	{
		id: "L2-eval",
		title: "第二层：computer_eval 跑 Python",
		needs: ["L2-bash"],
		run: l2Eval,
	},
	{
		id: "L2-freeze",
		title: "第二层：暂停电脑后再调用先唤醒",
		needs: ["L2-bash"],
		run: l2Freeze,
	},
];

// ---------------------------------------------------------------- 收尾

function killStragglers() {
	for (const pid of readdirSync("/proc")) {
		if (!/^\d+$/.test(pid) || Number(pid) === process.pid) continue;
		try {
			const cwd = readlinkSync(`/proc/${pid}/cwd`);
			const cmd = readFileSync(`/proc/${pid}/cmdline`, "utf8");
			if ([cwd, cmd].some((s) => s.includes(TMP) || s.includes(COORD_DIR)))
				process.kill(Number(pid), "SIGKILL");
		} catch {
			// 已退出或无权限
		}
	}
}

let cleaning: Promise<void> | undefined;
/** 收尾只做一次；再次 Ctrl-C 时等同一次收尾做完。 */
const cleanup = () => {
	cleaning ??= doCleanup();
	return cleaning;
};
async function doCleanup() {
	console.log("M19 收尾");
	mcp?.server.stop(true);
	// orb：经 REST 删除（provider 还在）；删不掉的按镜像 docker rm -f
	if (token) {
		const list = await api("GET", "/api/v1/orbs").catch(() => undefined);
		const ids =
			isRecord(list?.json) && Array.isArray(list.json.orbs)
				? list.json.orbs
						.filter((o) => isRecord(o) && o.coordinator !== true)
						.map((o) => String((o as Record<string, unknown>).id))
				: [];
		for (const id of ids) {
			const r = await api("DELETE", `/api/v1/orbs/${id}`).catch(
				() => undefined,
			);
			note(`DELETE orb ${id} → ${r?.status ?? "失败"}`);
		}
		if (ids.length)
			await waitFor(
				"orb 容器删除",
				() => sh(`docker ps -aq --filter ancestor=${IMAGE}`).stdout === "",
				120_000,
				2_000,
			).catch(() => note("部分 orb 容器未按时删除，改为 docker rm -f"));
	}
	if (imageBuilt)
		sh(
			`docker ps -aq --filter ancestor=${IMAGE} | xargs -r docker rm -f >/dev/null 2>&1`,
		);
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
	killStragglers();
	if (imageBuilt) sh(`docker rmi ${IMAGE} >/dev/null 2>&1`);
	if (!existsSync(TMP)) return;
	if (KEEP) console.log(`保留 ${TMP}（日志在 logs/）与 ${COORD_DIR}`);
	else {
		rmSync(TMP, { recursive: true, force: true });
		clearCoordDir();
	}
}

process.on("SIGINT", () => {
	console.log("\n收到 Ctrl-C，收尾后退出");
	void cleanup().finally(() => process.exit(130));
});

// ---------------------------------------------------------------- 主流程

const selected = CHECKS.filter(
	(c) => !ONLY.length || ONLY.some((p) => c.id.startsWith(p)),
);
if (!selected.length) {
	console.log(
		`--only ${ONLY.join(",")} 没有匹配的检查（可选：${CHECKS.map((c) => c.id).join(" ")}）`,
	);
	process.exit(2);
}
console.log(
	`M19 真机验收（run ${RUN}，临时目录 ${TMP}；检查 ${selected.map((c) => c.id).join(" ")}）`,
);
try {
	console.log("M19 环境");
	const t0 = Date.now();
	try {
		await setup();
		console.log(`M19 环境 PASS（${sec(Date.now() - t0)}）`);
	} catch (e) {
		note(`✗ ${e instanceof Error ? e.message : String(e)}`);
		console.log("M19 环境 FAIL");
	}
	for (const c of selected) await runCheck(c);
} finally {
	await cleanup();
}

console.log("M19 汇总");
for (const c of selected) {
	const v = verdicts.get(c.id);
	console.log(
		`  ${c.id.padEnd(8)} ${v?.v ?? "FAIL"}${v && v.v !== "PASS" && v.why ? `  ${clip(oneLine(v.why), 160)}` : ""}`,
	);
}
const fails = selected.filter(
	(c) => verdicts.get(c.id)?.v !== "PASS" && verdicts.get(c.id)?.v !== "SKIP",
);
const skips = selected.filter((c) => verdicts.get(c.id)?.v === "SKIP");
console.log(
	fails.length
		? "M19 真机验收 FAIL"
		: `M19 真机验收 PASS${skips.length ? `（${skips.length} 项跳过，未验证）` : ""}`,
);
process.exit(fails.length ? 1 : 0);
