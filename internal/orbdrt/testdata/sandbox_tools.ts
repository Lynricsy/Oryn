// sandbox_* 工具（M30）的真跑检查：用 bun 直接加载 oryn-orb.ts，给它一个假的 pi（收集 registerTool 的定义与 on 注册的处理器）
// 和一个说 orbd.sock 协议的假服务端。断言 8 个工具名与 loadMode；sandbox_exec 把两路输出流式交给 onUpdate、
// 最终文本带输出与退出码且非零退出为 isError；status 回 available: false 时系统提示里没有沙箱指引，true 时有。
// 用法：bun testdata/sandbox_tools.ts <oryn-orb.ts 的路径>（由 sandboxtools_test.go 调用）。成功打印 OK，失败非零退出。
import { mkdtempSync, rmSync } from "node:fs";
import { createServer, type Socket } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

function fail(msg: string): never {
	console.error(`FAIL: ${msg}`);
	process.exit(1);
}

const extPath = process.argv[2];
if (!extPath) fail("缺少扩展路径参数");

const dir = mkdtempSync(join(tmpdir(), "oryn-sbx-tools-"));
const sock = join(dir, "orbd.sock");
let available = false;
const requests: { method: string; params: { threadId: string; op: string; params: Record<string, unknown> } }[] = [];

const server = createServer((conn: Socket) => {
	let buf = "";
	conn.setEncoding("utf8");
	conn.on("data", (chunk: string) => {
		buf += chunk;
		const i = buf.indexOf("\n");
		if (i < 0) return;
		const req = JSON.parse(buf.slice(0, i));
		requests.push(req);
		const line = (v: unknown) => conn.write(`${JSON.stringify(v)}\n`);
		if (req.method !== "sandbox.call") {
			conn.end(`${JSON.stringify({ type: "result", result: {} })}\n`);
			return;
		}
		switch (req.params.op) {
			case "status":
				conn.end(`${JSON.stringify({ type: "result", result: { available, backends: available ? ["kvm", "docker"] : [] } })}\n`);
				return;
			case "exec":
				line({ type: "log", stream: "stdout", data: "a\n" });
				setTimeout(() => {
					line({ type: "log", stream: "stderr", data: "b\n" });
					conn.end(`${JSON.stringify({ type: "result", result: { exit: 2, durationMs: 5 } })}\n`);
				}, 20);
				return;
			default:
				conn.end(`${JSON.stringify({ type: "error", code: "sandbox_not_found", message: "no such sandbox" })}\n`);
		}
	});
});
const listening = Promise.withResolvers<void>();
server.listen(sock, listening.resolve);
await listening.promise;

process.env.ORYN_ARTIFACT_SOCK = sock;
process.env.ORYN_THREAD_ID = "T-sbx";
process.env.HOME = dir;
delete process.env.ORYN_RUNNER;
delete process.env.ORYN_SSH;

// 假的 zod：任意属性访问与调用都返回自己，足够让工具定义构造出来。
const zod: unknown = new Proxy(() => {}, { get: () => zod, apply: () => zod });
type Handler = (event: unknown, ctx?: unknown) => unknown;
type Tool = {
	name: string;
	loadMode?: string;
	approval?: string;
	execute: (
		id: string,
		params: unknown,
		signal: AbortSignal | undefined,
		onUpdate?: (u: { content: { text: string }[]; details?: Record<string, unknown> }) => void,
		ctx?: unknown,
	) => Promise<{
		content: { text: string }[];
		isError?: boolean;
		details?: Record<string, unknown>;
	}>;
};
const handlers = new Map<string, Handler[]>();
const tools = new Map<string, Tool>();
const pi = {
	zod,
	on(event: string, handler: Handler) {
		handlers.set(event, [...(handlers.get(event) ?? []), handler]);
	},
	registerTool(t: Tool) {
		tools.set(t.name, t);
	},
	getAllTools: () => [],
	setActiveTools: async () => {},
};

// 扩展路径由命令行给出，只能动态加载；先设好上面的环境变量再加载。
const mod = (await import(resolve(extPath))) as { default: (pi: unknown) => void };
mod.default(pi);

const want = [
	"sandbox_create",
	"sandbox_exec",
	"sandbox_cp",
	"sandbox_network",
	"sandbox_jobs",
	"sandbox_extend",
	"sandbox_list",
	"sandbox_destroy",
];
for (const name of want) {
	const t = tools.get(name) ?? fail(`没有注册 ${name}`);
	if (t.loadMode !== "discoverable") fail(`${name} 的 loadMode 是 ${t.loadMode}`);
	if (t.approval !== (name === "sandbox_list" ? "read" : "exec")) fail(`${name} 的 approval 是 ${t.approval}`);
}
const extra = [...tools.keys()].filter((n) => n.startsWith("sandbox_") && !want.includes(n));
if (extra.length > 0) fail(`多注册了 ${extra.join(", ")}`);

// sandbox_exec：流式输出与最终结果
const updates: string[] = [];
const liveDetails: unknown[] = [];
const res = await tools.get("sandbox_exec")!.execute("tc-1", { sandbox: "sbx_1", command: "x" }, undefined, (u) => {
	updates.push(u.content[0].text);
	liveDetails.push(u.details?.output);
});
if (updates.length === 0) fail("sandbox_exec 没有调用 onUpdate");
// 经 bash 工具命令调用时 Core 只转发部分结果的 details，输出尾巴必须也在 details.output 里
if (liveDetails.at(-1) !== updates.at(-1) || !String(liveDetails.at(-1)).includes("a\n")) {
	fail(`onUpdate 的 details.output 不对：${JSON.stringify(liveDetails)}`);
}
const text = res.content[0].text;
for (const s of ["a\n", "b\n", "exit 2"]) {
	if (!text.includes(s)) fail(`sandbox_exec 的文本缺少 ${JSON.stringify(s)}：${JSON.stringify(text)}`);
}
if (res.isError !== true) fail("非零退出应为 isError");
if (res.details?.exit !== 2 || res.details?.durationMs !== 5) fail(`details 不对：${JSON.stringify(res.details)}`);
const execReq = requests.find((r) => r.params?.op === "exec") ?? fail("没有发 exec");
if (execReq.params.threadId !== "T-sbx" || execReq.params.params.sandbox !== "sbx_1") fail(`exec 请求不对：${JSON.stringify(execReq)}`);

// 错误回成 code: message
const err = await tools.get("sandbox_destroy")!.execute("tc-2", { sandbox: "sbx_x" }, undefined);
if (err.isError !== true || err.content[0].text !== "sandbox_not_found: no such sandbox") fail(`错误结果不对：${JSON.stringify(err)}`);

// sandbox_cp：orb 一侧的相对路径按 cwd 解析
await tools.get("sandbox_cp")!.execute("tc-3", { sandbox: "sbx_1", direction: "to_sandbox", source: "src", dest: "proj" }, undefined, undefined, {
	cwd: "/home/user/work",
});
const cpReq = requests.find((r) => r.params?.op === "cp") ?? fail("没有发 cp");
if (cpReq.params.params.source !== "/home/user/work/src" || cpReq.params.params.dest !== "proj") fail(`cp 路径不对：${JSON.stringify(cpReq.params.params)}`);

// 系统提示：不可用时没有沙箱指引，可用时有且带后端
const before = handlers.get("before_agent_start") ?? fail("没有注册 before_agent_start");
const prompt = async () => {
	let out = "";
	for (const h of before) {
		const r = (await h({ systemPrompt: [] })) as { systemPrompt: string[] };
		out += r.systemPrompt.join("\n");
	}
	return out;
};
if ((await prompt()).includes("沙箱：")) fail("status 不可用时不应追加沙箱指引");
available = true;
const on = await prompt();
if (!on.includes("沙箱：") || !on.includes("kvm、docker")) fail("status 可用时应追加带后端的沙箱指引");

server.close();
rmSync(dir, { recursive: true, force: true });
console.log("OK");
process.exit(0);
