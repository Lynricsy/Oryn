// 回合屏障（K5）的真跑检查：用 bun 直接加载 oryn-orb.ts，给它一个假的 pi（记录 on 注册的处理器，registerTool 空操作）
// 和一个假的 orbd.sock（design.turn 延迟 300 ms 回执）。先调 agent_start 处理器（不 await），立即调 tool_call 处理器并
// await，断言 tool_call 在 design.turn 回执之后才放行；回执之后的 tool_call 不再等待。
// 用法：bun testdata/turn_barrier.ts <oryn-orb.ts 的路径>（由 designtools_test.go 调用）。成功打印 OK，失败非零退出。
import { mkdtempSync, rmSync } from "node:fs";
import { createServer, type Socket } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const TURN_DELAY_MS = 300;

function fail(msg: string): never {
	console.error(`FAIL: ${msg}`);
	process.exit(1);
}

const extPath = process.argv[2];
if (!extPath) fail("缺少扩展路径参数");

const dir = mkdtempSync(join(tmpdir(), "oryn-barrier-"));
const sock = join(dir, "orbd.sock");
const calls: { method: string; at: number }[] = [];
let turnReplied = 0;

const server = createServer((conn: Socket) => {
	let buf = "";
	conn.setEncoding("utf8");
	conn.on("data", (chunk: string) => {
		buf += chunk;
		const i = buf.indexOf("\n");
		if (i < 0) return;
		const req = JSON.parse(buf.slice(0, i)) as { method: string };
		calls.push({ method: req.method, at: performance.now() });
		const reply = (result: unknown) => conn.end(`${JSON.stringify({ type: "result", result })}\n`);
		if (req.method === "design.turn") {
			setTimeout(() => {
				turnReplied = performance.now();
				reply({});
			}, TURN_DELAY_MS);
		} else if (req.method === "design.status") {
			reply({ designs: [] });
		} else {
			reply({});
		}
	});
});
const listening = Promise.withResolvers<void>();
server.listen(sock, listening.resolve);
await listening.promise;

process.env.ORYN_ARTIFACT_SOCK = sock;
process.env.ORYN_THREAD_ID = "T-barrier";
process.env.HOME = dir;
delete process.env.ORYN_RUNNER;

// 假的 zod：任意属性访问与调用都返回自己，足够让工具定义构造出来。
const zod: unknown = new Proxy(() => {}, { get: () => zod, apply: () => zod });
type Handler = (event: unknown, ctx?: unknown) => unknown;
const handlers = new Map<string, Handler[]>();
const pi = {
	zod,
	on(event: string, handler: Handler) {
		handlers.set(event, [...(handlers.get(event) ?? []), handler]);
	},
	registerTool() {},
};

// 扩展路径由命令行给出，只能动态加载；先设好上面的环境变量再加载。
const mod = (await import(resolve(extPath))) as { default: (pi: unknown) => void };
mod.default(pi);

const agentStart = handlers.get("agent_start") ?? fail("没有注册 agent_start 处理器");
const toolCall = handlers.get("tool_call") ?? fail("没有注册 tool_call 处理器");

// agent_start 不 await（omp 不等它就进入工具循环），随即来了第一个工具调用
for (const h of agentStart) void h({ type: "agent_start" });
const start = performance.now();
let result: unknown;
for (const h of toolCall) result = await h({ type: "tool_call", toolName: "bash", toolCallId: "tc-1", input: { command: "cat x" } });
const released = performance.now();

const turn = calls.find((c) => c.method === "design.turn") ?? fail("agent_start 没有发 design.turn");
if (turnReplied === 0) fail("tool_call 放行时 design.turn 还没有回执");
if (released < turnReplied) fail(`tool_call 在 design.turn 回执之前放行（放行 ${released.toFixed(1)}，回执 ${turnReplied.toFixed(1)}）`);
if (released - start < TURN_DELAY_MS - 50) fail(`tool_call 只等了 ${(released - start).toFixed(1)} ms`);
if (result !== undefined) fail(`回执及时时 tool_call 应放行且不带说明，收到 ${JSON.stringify(result)}`);
if (turn.at > start + 50) fail("design.turn 没有在 agent_start 时立即发出");

// 屏障已过：本回合后续的工具调用不再等
const again = performance.now();
for (const h of toolCall) await h({ type: "tool_call", toolName: "read", toolCallId: "tc-2", input: { path: "x" } });
if (performance.now() - again > 50) fail("屏障完成后 tool_call 仍在等待");

server.close();
rmSync(dir, { recursive: true, force: true });
console.log("OK");
process.exit(0);
