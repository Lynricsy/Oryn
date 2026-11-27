// pull_artifact 扩展工具（M31）的真跑检查：用 bun 直接加载 oryn-orb.ts，给它一个假的 pi（记录注册的工具）和一个假的
// orbd.sock（artifact.call 的 pull_artifact 按 7 字节一页回内容）。调用工具，断言文件按页拼回且 sha256 正确、第二页起钉住
// 第一页的版本与路径、to 已存在且没给 overwrite 时报错、校验不过时不留文件。
// <to>.oryn-part 已存在时报错且不动它。
// 用法：bun testdata/pull_artifact.ts <oryn-orb.ts 的路径>（由 office_test.go 调用）。成功打印 OK，失败非零退出。
import { createHash } from "node:crypto";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type Socket } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

function fail(msg: string): never {
	console.error(`FAIL: ${msg}`);
	process.exit(1);
}

const extPath = process.argv[2];
if (!extPath) fail("缺少扩展路径参数");

const content = Buffer.from("PK\x03\x04 分页取回的 Office 文件");
const sha = createHash("sha256").update(content).digest("hex");
const PAGE = 7;
let corrupt = false;

const dir = mkdtempSync(join(tmpdir(), "oryn-pull-"));
const sock = join(dir, "orbd.sock");
const requests: Record<string, unknown>[] = [];

const server = createServer((conn: Socket) => {
	let buf = "";
	conn.setEncoding("utf8");
	conn.on("data", (chunk: string) => {
		buf += chunk;
		const i = buf.indexOf("\n");
		if (i < 0) return;
		const req = JSON.parse(buf.slice(0, i)) as { method: string; params: { op: string; params: Record<string, unknown> } };
		if (req.method !== "artifact.call" || req.params.op !== "pull_artifact") {
			conn.end(`${JSON.stringify({ type: "error", code: "invalid", message: "unexpected" })}\n`);
			return;
		}
		const p = req.params.params;
		requests.push(p);
		const off = typeof p.cursor === "string" ? Number(p.cursor) : 0;
		const end = Math.min(off + PAGE, content.length);
		const data = Buffer.from(content.subarray(off, end));
		if (corrupt && off > 0) data[0] ^= 0xff;
		const page = { id: "art_X", version: 4, path: "workbook.xlsx", size: content.length, sha256: sha, offset: off, data: data.toString("base64"), next: end < content.length ? String(end) : "" };
		const result = { text: JSON.stringify(page), details: { op: "pull_artifact", id: "art_X", title: "预算", kind: "sheet", version: 4 } };
		conn.end(`${JSON.stringify({ type: "result", result })}\n`);
	});
});
const listening = Promise.withResolvers<void>();
server.listen(sock, listening.resolve);
await listening.promise;

process.env.ORYN_ARTIFACT_SOCK = sock;
process.env.ORYN_THREAD_ID = "T-pull";
process.env.HOME = dir;
delete process.env.ORYN_RUNNER;

// 假的 zod：任意属性访问与调用都返回自己，足够让工具定义构造出来。
const zod: unknown = new Proxy(() => {}, { get: () => zod, apply: () => zod });
type Tool = {
	name: string;
	loadMode?: string;
	execute: (id: string, params: unknown, signal: AbortSignal | undefined, onUpdate: unknown, ctx: unknown) => Promise<{
		content: { type: string; text?: string }[];
		isError?: boolean;
		details?: Record<string, unknown>;
	}>;
};
const tools = new Map<string, Tool>();
const pi = {
	zod,
	on() {},
	registerTool(tool: Tool) {
		tools.set(tool.name, tool);
	},
};

// 扩展路径由命令行给出，只能动态加载；先设好上面的环境变量再加载。
const mod = (await import(resolve(extPath))) as { default: (pi: unknown) => void };
mod.default(pi);
const pull = tools.get("pull_artifact") ?? fail("没有注册 pull_artifact");
if (pull.loadMode !== "discoverable") fail(`pull_artifact 的 loadMode 是 ${pull.loadMode}`);

const ctx = { cwd: dir };
const res = await pull.execute("tc-1", { id: "art_X", to: "out/../budget.xlsx" }, undefined, undefined, ctx);
if (res.isError) fail(`取回失败：${res.content[0]?.text}`);
const to = join(dir, "budget.xlsx");
if (!readFileSync(to).equals(content)) fail("拼回的文件与原文件不同");
if (existsSync(`${to}.oryn-part`)) fail("留下了 .oryn-part");
const out = JSON.parse(res.content[0]?.text ?? "{}") as Record<string, unknown>;
const want = { id: "art_X", version: 4, path: "workbook.xlsx", to, size: content.length, sha256: sha };
if (JSON.stringify(out) !== JSON.stringify(want)) fail(`结果不对：${JSON.stringify(out)}`);
if (res.details?.title !== "预算" || res.details?.to !== to) fail(`details 不对：${JSON.stringify(res.details)}`);
const pages = Math.ceil(content.length / PAGE);
if (requests.length !== pages) fail(`请求了 ${requests.length} 页，应为 ${pages}`);
if (JSON.stringify(requests[0]) !== JSON.stringify({ id: "art_X" })) fail(`第一页参数：${JSON.stringify(requests[0])}`);
if (JSON.stringify(requests[1]) !== JSON.stringify({ id: "art_X", version: 4, path: "workbook.xlsx", cursor: "7" })) {
	fail(`第二页没有钉住版本与路径：${JSON.stringify(requests[1])}`);
}

// 已存在且没给 overwrite：报错、不发请求；给了就覆盖
const n = requests.length;
const again = await pull.execute("tc-2", { id: "art_X", to }, undefined, undefined, ctx);
if (!again.isError || !again.content[0]?.text?.includes("overwrite")) fail(`已存在时应报错：${JSON.stringify(again)}`);
if (requests.length !== n) fail("已存在时不应发请求");
const over = await pull.execute("tc-3", { id: "art_X", to, overwrite: true }, undefined, undefined, ctx);
if (over.isError) fail(`overwrite 失败：${over.content[0]?.text}`);

// <to>.oryn-part 已存在（另一次取回或残片）：报错、不发请求，不覆盖也不删除它，目标文件不变
writeFileSync(`${to}.oryn-part`, "别人的临时文件");
const before = requests.length;
const busy = await pull.execute("tc-5", { id: "art_X", to, overwrite: true }, undefined, undefined, ctx);
if (!busy.isError || !busy.content[0]?.text?.includes(".oryn-part 已存在")) fail(`临时文件已存在时应报错：${JSON.stringify(busy)}`);
if (requests.length !== before) fail("临时文件已存在时不应发请求");
if (readFileSync(`${to}.oryn-part`, "utf8") !== "别人的临时文件") fail("覆盖或删除了别人的临时文件");
if (!readFileSync(to).equals(content)) fail("临时文件已存在时改动了目标文件");
rmSync(`${to}.oryn-part`);

// 校验不过：报错，不留目标文件与 .oryn-part
corrupt = true;
const bad = join(dir, "bad.xlsx");
const broken = await pull.execute("tc-4", { id: "art_X", to: bad }, undefined, undefined, ctx);
if (!broken.isError) fail("内容被改过时应报错");
if (existsSync(bad) || existsSync(`${bad}.oryn-part`)) fail("校验不过时留下了文件");

server.close();
rmSync(dir, { recursive: true, force: true });
console.log("OK");
process.exit(0);
