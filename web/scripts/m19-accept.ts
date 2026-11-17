#!/usr/bin/env bun
import {
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
 * 沙箱里，真实模型驱动全局助手对话，逐项记录检查结果。宿主工具的细节、确认卡、主线等由 Go 端到端覆盖：
 * `go test ./internal/server/ -run TestE2EM19`。
 *
 * 用法（web/ 目录）：bun run accept:m19 [-- --only <前缀>[,<前缀>…]] [-- --help]
 *   `--only L1` 只跑 id 以 L1 开头的检查；不给则全部跑。
 *
 * 自建一套隔离环境（全部在 /var/tmp/oryn-m19-<run>/ 下，结束时删除；M19_KEEP=1 保留，日志在 logs/）：
 *   - orynd（data_dir 全新）只听 127.0.0.1:$M19_PORT（缺省 7690）；不起 provider：本批检查只用协调宿主。
 *   - 协调宿主：`omp_path` 指向钉住的 Core（校验 sha256），目录固定为 $M19_COORD_DIR（root 运行时宿主账号按目录派生，
 *     每次开跑与收尾时清空），助手对话的模型是 $M19_MODEL（缺省 claude-pro/claude-sonnet-5-5）。
 *   - Agent 配置：把 $M19_MODELS（缺省 ~/.omp/agent/models.yml）原样 PUT 成全局配置的 models.yml（密钥由 orynd 抽取）。
 *
 * agent 的不确定性：提示词点名要调用的工具与参数；断言看转录里的工具结果（不看措辞）。没按要求调用工具时，同一对话补发
 * 一次提示。
 *
 * 每条打印 `M19 <id> PASS|FAIL|SKIP（秒）` 与证据行，最后汇总。没有 FAIL 时退出码 0；SKIP 逐条写明原因（未验证，
 * 不等于通过）。
 */

const USAGE = `用法：bun scripts/m19-accept.ts [--only <前缀>[,<前缀>…]] [--help]
  环境变量：M19_BIN（缺省 <仓库>/bin）、M19_KEEP=1（保留临时目录）、M19_PORT（缺省 7690）、
  M19_MODELS（缺省 ~/.omp/agent/models.yml）、M19_MODEL（缺省 claude-pro/claude-sonnet-5-5）、
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
const COORD_DIR = process.env.M19_COORD_DIR ?? "/var/tmp/oryn-m19-coord";
const COORD_EGRESS = Number(process.env.M19_COORD_EGRESS ?? 7937);
/** 回合等待上限（真实模型，含协调宿主冷启动）。 */
const TURN_TIMEOUT = 600_000;

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

const cli = (args: string) =>
	sh(`${q(`${BIN}/oryn`)} ${args}`, {
		...process.env,
		ORYN_API_URL: API,
		ORYN_API_TOKEN: token,
	});

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
	const r = cli(`thread send ${tid} ${q(msg)}`);
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

async function setup() {
	for (const b of ["orynd", "oryn"])
		if (!existsSync(`${BIN}/${b}`))
			throw new Error(`缺少 ${BIN}/${b}：先 make build（或设 M19_BIN）`);
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

	clearCoordDir();
	mkdirSync(LOGS, { recursive: true });
	writeFileSync(
		`${TMP}/orynd.yaml`,
		`listen: ["127.0.0.1:${API_PORT}"]
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

	const put = await api(
		"PUT",
		"/api/v1/agent-config/files/models.yml?mode=384",
		readFileSync(MODELS_SRC, "utf8"),
	);
	check(
		put.status === 200,
		`全局配置 models.yml ← ${MODELS_SRC}（${put.status}${put.status === 200 && isRecord(put.json) && Array.isArray(put.json.extractedSecrets) ? `，抽取 ${put.json.extractedSecrets.length} 个密钥` : `：${clip(put.text)}`}）`,
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
	check(!content.includes(MCP_TOKEN), `沙箱 mcp.json 不含 ${MCP_TOKEN}`);
	check(!/"headers"|authorization/i.test(content), "沙箱 mcp.json 不含任何头");
}

const CHECKS: Check[] = [
	{ id: "L1-web", title: "第一层：read 读公网 https 地址", run: l1Web },
	{ id: "L1-deny", title: "第一层：正向代理拒绝非公网目标", run: l1Deny },
	{
		id: "L1-mcp",
		title: "第一层：HTTP MCP 工具经 orynd 注入鉴权头",
		run: l1Mcp,
	},
];

// ---------------------------------------------------------------- 收尾

function killStragglers() {
	for (const pid of readdirSync("/proc")) {
		if (!/^\d+$/.test(pid) || Number(pid) === process.pid) continue;
		try {
			const cwd = readlinkSync(`/proc/${pid}/cwd`);
			if (cwd.startsWith(TMP) || cwd.startsWith(COORD_DIR))
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
	console.log("M19 收尾");
	mcp?.server.stop(true);
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
