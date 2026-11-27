// 视图工具（M33）的真跑检查：用 bun 直接加载 oryn-orb.ts，给它假的 pi（收集 registerTool 的定义与 on 注册的处理器，
// 维护一份激活工具列表）与假的 ctx（agent.kind、ui.input）。断言：没有 ORYN_VIEW_TOOLS 时不注册；注册的两个工具与契约文件
// 一致；C3 的六种结果与 details.view.spec；子 agent 里工具从激活集去掉且 execute 拒绝；指引只在主 agent 且 render_ui 激活时
// 追加（orb 与 Runner 两个分支，含角色白名单不含 render_ui 的情形）。
// 用法：bun testdata/view_tools.ts <oryn-orb.ts 的路径> <view-tools.json 的路径>（由 viewtools_test.go 调用）。
// 成功打印 OK，失败非零退出。
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

function fail(msg: string): never {
	console.error(`FAIL: ${msg}`);
	process.exit(1);
}

const extPath = process.argv[2];
const viewPath = process.argv[3];
if (!extPath || !viewPath) fail("缺少扩展路径或 view-tools.json 路径参数");
const contract = JSON.parse(readFileSync(viewPath, "utf8")) as {
	guide: string;
	tools: { name: string; label: string; description: string; parameters: unknown }[];
};

const dir = mkdtempSync(join(tmpdir(), "oryn-view-tools-"));
process.env.HOME = dir;
for (const k of ["ORYN_ARTIFACT_SOCK", "ORYN_RUNNER", "ORYN_SSH", "ORYN_ROLE_TOOLS", "ORYN_VIEW_TOOLS", "ORYN_LANGUAGE"]) {
	delete process.env[k];
}

type Result = { content: { type: string; text: string }[]; isError?: boolean; details?: Record<string, unknown> };
type InputOpts = { signal?: AbortSignal; timeout?: number; onTimeout?: () => void };
type Ctx = {
	agent: { kind: "main" | "sub" };
	ui: { input: (title: string, placeholder?: string, opts?: InputOpts) => Promise<string | undefined> };
};
type Tool = {
	name: string;
	label: string;
	description: string;
	parameters: unknown;
	loadMode?: string;
	approval?: string;
	execute: (id: string, params: unknown, signal: AbortSignal | undefined, onUpdate: undefined, ctx: Ctx) => Promise<Result>;
};
type Handler = (event: unknown, ctx: Ctx) => unknown;

// 假的 zod：任意属性访问与调用都返回自己，足够让其它工具定义构造出来。
const zod: unknown = new Proxy(() => {}, { get: () => zod, apply: () => zod });

function makePi() {
	const handlers = new Map<string, Handler[]>();
	const tools = new Map<string, Tool>();
	let active: string[] = [];
	const pi = {
		zod,
		on(event: string, handler: Handler) {
			handlers.set(event, [...(handlers.get(event) ?? []), handler]);
		},
		registerTool(t: Tool) {
			tools.set(t.name, t);
			active.push(t.name);
		},
		getAllTools: () => [...tools.keys(), "read", "bash"].map((name) => ({ name })),
		getActiveTools: () => [...active],
		setActiveTools: async (names: string[]) => {
			active = [...names];
		},
	};
	const emit = async (event: string, payload: unknown, ctx: Ctx) => {
		const out: unknown[] = [];
		for (const h of handlers.get(event) ?? []) out.push(await h(payload, ctx));
		return out;
	};
	const prompt = async (ctx: Ctx) => {
		const out = (await emit("before_agent_start", { type: "before_agent_start", systemPrompt: [] }, ctx)) as {
			systemPrompt: string[];
		}[];
		return out.map((r) => r.systemPrompt.join("\n")).join("\n");
	};
	return { pi, tools, handlers, emit, prompt, active: () => active };
}

// 扩展路径由命令行给出，只能动态加载；每次 load 用新的 pi，环境变量在调用时读取。
const mod = (await import(resolve(extPath))) as { default: (pi: unknown) => void };
function load() {
	const p = makePi();
	mod.default(p.pi);
	return p;
}

type InputCall = { title: string; placeholder?: string; opts?: InputOpts };
function ctxWith(kind: "main" | "sub", answer: (call: InputCall) => string | undefined) {
	const calls: InputCall[] = [];
	const ctx: Ctx = {
		agent: { kind },
		ui: {
			input: async (title, placeholder, opts) => {
				const call = { title, placeholder, opts };
				calls.push(call);
				return answer(call);
			},
		},
	};
	return { ctx, calls };
}
const neverAsked = () => fail("不该调用 ctx.ui.input");

// ---- 没有 ORYN_VIEW_TOOLS / 文件读不到：不注册、不追加 ----
{
	const p = load();
	if (p.tools.has("render_ui") || p.tools.has("render_html")) fail("没有 ORYN_VIEW_TOOLS 时不应注册视图工具");
	if ((await p.prompt(ctxWith("main", neverAsked).ctx)).includes(contract.guide)) fail("没有 ORYN_VIEW_TOOLS 时不应追加指引");
	process.env.ORYN_VIEW_TOOLS = join(dir, "missing.json");
	const q = load();
	if (q.tools.has("render_ui")) fail("文件读不到时不应注册视图工具");
}

process.env.ORYN_VIEW_TOOLS = viewPath;
const p = load();

// ---- 注册：名字、标签、描述、schema 与契约文件一致 ----
for (const want of contract.tools) {
	const t = p.tools.get(want.name) ?? fail(`没有注册 ${want.name}`);
	if (t.label !== want.label || t.description !== want.description) fail(`${want.name} 的 label / description 与契约不一致`);
	if (JSON.stringify(t.parameters) !== JSON.stringify(want.parameters)) fail(`${want.name} 的 parameters 与契约不一致`);
	if (t.loadMode !== "essential" || t.approval !== "read") fail(`${want.name} 的 loadMode / approval 是 ${t.loadMode} / ${t.approval}`);
}
const ui = p.tools.get("render_ui")!;
const html = p.tools.get("render_html")!;

const expectResult = (name: string, r: Result, text: string, view: Record<string, unknown>, isError?: boolean) => {
	if (r.content[0]?.text !== text) fail(`${name}：文本是 ${JSON.stringify(r.content[0]?.text)}，应为 ${JSON.stringify(text)}`);
	if (JSON.stringify(r.details?.view) !== JSON.stringify(view)) {
		fail(`${name}：details.view 是 ${JSON.stringify(r.details?.view)}，应为 ${JSON.stringify(view)}`);
	}
	if ((r.isError === true) !== (isError === true)) fail(`${name}：isError 是 ${r.isError}`);
};

const RENDERED = "Rendered.";
const RENDERED_INPUT = "Rendered. The user's input will arrive as their next message; end this turn now.";
const statsOnly = { title: "看板", blocks: [{ type: "stats", items: [{ label: "A", value: 1 }] }] };
const nestedForm = {
	blocks: [
		{
			type: "columns",
			columns: [
				{ blocks: [{ type: "text", text: "左" }] },
				{ blocks: [{ type: "card", blocks: [{ type: "form", fields: [{ name: "x", label: "X", kind: "text" }] }] }] },
			],
		},
	],
};
const tabsActions = {
	title: "  选一个\n方案  ",
	blocks: [{ type: "tabs", tabs: [{ label: "a", blocks: [{ type: "divider" }] }, { label: "b", blocks: [{ type: "actions", buttons: [{ label: "好", prompt: "就这个" }] }] }] }],
};

// 1. 无交互块：Rendered.；await 被忽略（不问）；spec 去掉 await
{
	const { ctx } = ctxWith("main", neverAsked);
	expectResult("无交互块", await ui.execute("tc-1", { ...statsOnly, await: true }, undefined, undefined, ctx), RENDERED, {
		outcome: "rendered",
		spec: statsOnly,
	});
}
// 2. 有交互块（嵌套在 columns / card 里）、非 await
{
	const { ctx } = ctxWith("main", neverAsked);
	expectResult("有交互块非 await", await ui.execute("tc-2", { ...nestedForm, await: false }, undefined, undefined, ctx), RENDERED_INPUT, {
		outcome: "rendered",
		spec: nestedForm,
	});
}
// 3. HTML 非 await：Rendered.；spec 只有 { libs, height }，libs 兼容单个字符串
{
	const { ctx } = ctxWith("main", neverAsked);
	expectResult(
		"HTML 非 await",
		await html.execute("tc-3", { title: "T", html: "<p>x</p>", libs: "chartjs", height: 300 }, undefined, undefined, ctx),
		RENDERED,
		{ outcome: "rendered", spec: { libs: ["chartjs"], height: 300 } },
	);
	expectResult("HTML 无 libs", await html.execute("tc-3b", { html: "<p>x</p>" }, undefined, undefined, ctx), RENDERED, {
		outcome: "rendered",
		spec: { libs: [] },
	});
}
// 4. await 已作答：{v:1, answer, text} → text 进结果、answer 进 details；请求的标题与标记
{
	const answer = { block: "0.t1.0", kind: "action", choice: 0 };
	const { ctx, calls } = ctxWith("main", () => JSON.stringify({ v: 1, answer, text: "就这个" }));
	expectResult(
		"await 作答",
		await ui.execute("tc-4", { ...tabsActions, await: true }, undefined, undefined, ctx),
		"The user answered in the view:\n就这个",
		{ outcome: "answered", spec: tabsActions, answer },
	);
	const c = calls[0] ?? fail("await 没有调用 ctx.ui.input");
	if (c.title !== "选一个 方案") fail(`标题是 ${JSON.stringify(c.title)}`);
	if (c.placeholder !== "oryn-view:tc-4") fail(`placeholder 是 ${JSON.stringify(c.placeholder)}`);
	if (c.opts?.timeout !== 600_000 || typeof c.opts?.onTimeout !== "function") fail(`input 选项不对：${JSON.stringify(c.opts)}`);
}
// 4b. 不是那种 JSON：整段当 text，不带 answer；HTML 的 await 同样走阻塞
{
	const { ctx } = ctxWith("main", () => "手打的回答");
	expectResult(
		"await 纯文本作答",
		await html.execute("tc-4b", { html: "<p>x</p>", libs: ["tailwind"], await: true }, undefined, undefined, ctx),
		"The user answered in the view:\n手打的回答",
		{ outcome: "answered", spec: { libs: ["tailwind"] } },
	);
}
// 5. 超时
{
	const { ctx } = ctxWith("main", (c) => {
		c.opts?.onTimeout?.();
		return undefined;
	});
	expectResult(
		"await 超时",
		await ui.execute("tc-5", { ...nestedForm, await: true }, undefined, undefined, ctx),
		"No response within 10 minutes. The view stays usable: a later answer arrives as a user message. Continue without it or end the turn.",
		{ outcome: "timeout", spec: nestedForm },
	);
}
// 6. 中止
{
	const ac = new AbortController();
	const { ctx, calls } = ctxWith("main", () => {
		ac.abort();
		return undefined;
	});
	expectResult("await 中止", await ui.execute("tc-6", { ...nestedForm, await: true }, ac.signal, undefined, ctx), "Aborted.", {
		outcome: "aborted",
		spec: nestedForm,
	}, true);
	if (calls[0]?.opts?.signal !== ac.signal) fail("input 没有带上 signal");
}
// 7. 主人改为打字（cancelled，无超时、无中止）
{
	const { ctx } = ctxWith("main", () => undefined);
	expectResult(
		"await 改为打字",
		await ui.execute("tc-7", { ...nestedForm, await: true }, undefined, undefined, ctx),
		"The user chose to reply in text instead; their message follows.",
		{ outcome: "dismissed", spec: nestedForm },
	);
}
// 8. 没有 title 时按 ORYN_LANGUAGE 取缺省标题
{
	const { ctx, calls } = ctxWith("main", () => undefined);
	await ui.execute("tc-8", { ...nestedForm, await: true }, undefined, undefined, ctx);
	process.env.ORYN_LANGUAGE = "en";
	await ui.execute("tc-8b", { ...nestedForm, await: true }, undefined, undefined, ctx);
	delete process.env.ORYN_LANGUAGE;
	if (calls[0]?.title !== "有一个视图等你操作" || calls[1]?.title !== "A view is waiting for your input") {
		fail(`缺省标题不对：${JSON.stringify(calls.map((c) => c.title))}`);
	}
}
// 9. 子 agent：execute 硬拒绝（不问、不带 details.view）
{
	const { ctx } = ctxWith("sub", neverAsked);
	for (const t of [ui, html]) {
		const r = await t.execute("tc-9", { ...nestedForm, html: "<p>x</p>", await: true }, undefined, undefined, ctx);
		if (r.isError !== true || r.details?.view !== undefined) fail(`子 agent 调用 ${t.name} 应被拒绝：${JSON.stringify(r)}`);
	}
}

// ---- 激活集与指引 ----
const main = ctxWith("main", neverAsked).ctx;
const sub = ctxWith("sub", neverAsked).ctx;
const hasViews = (names: string[]) => names.includes("render_ui") || names.includes("render_html");

// orb 分支：主 agent 保留、追加指引；子 agent 去掉、不追加
await p.emit("session_start", { type: "session_start" }, main);
if (!hasViews(p.active())) fail("主 agent 的激活集里应有视图工具");
if (!(await p.prompt(main)).includes(contract.guide)) fail("主 agent 且 render_ui 激活时应追加指引");
{
	const s = load();
	await s.emit("session_start", { type: "session_start" }, sub);
	if (hasViews(s.active())) fail(`子 agent 的激活集里不应有视图工具：${s.active().join(",")}`);
	if ((await s.prompt(sub)).includes(contract.guide)) fail("子 agent 不应追加指引");
	const w = load();
	await w.emit("session_switch", { type: "session_switch" }, sub);
	if (hasViews(w.active())) fail("session_switch 后子 agent 的激活集里不应有视图工具");
}

// 角色白名单：含 render_ui 时主 agent 有工具与指引、子 agent 两者都没有；不含时都没有
process.env.ORYN_ROLE_TOOLS = JSON.stringify(["read", "render_ui"]);
{
	const m = load();
	await m.emit("session_start", { type: "session_start" }, main);
	if (m.active().toSorted().join(",") !== "read,render_ui") fail(`白名单含 render_ui 时主 agent 激活集是 ${m.active().join(",")}`);
	if (!(await m.prompt(main)).includes(contract.guide)) fail("白名单含 render_ui 时应追加指引");
	const s = load();
	await s.emit("session_start", { type: "session_start" }, sub);
	if (s.active().join(",") !== "read") fail(`白名单含 render_ui 时子 agent 激活集是 ${s.active().join(",")}`);
	if ((await s.prompt(sub)).includes(contract.guide)) fail("子 agent 不应追加指引");
}
process.env.ORYN_ROLE_TOOLS = JSON.stringify(["read"]);
{
	const m = load();
	await m.emit("session_start", { type: "session_start" }, main);
	if (hasViews(m.active())) fail("白名单不含视图工具时不应激活");
	if ((await m.prompt(main)).includes(contract.guide)) fail("角色白名单不含 render_ui 时不应追加指引");
}
delete process.env.ORYN_ROLE_TOOLS;

// Runner 分支同样注册并按同样条件追加
process.env.ORYN_RUNNER = "1";
{
	const r = load();
	if (!r.tools.has("render_ui") || !r.tools.has("render_html")) fail("Runner 上应注册视图工具");
	await r.emit("session_start", { type: "session_start" }, main);
	if (!(await r.prompt(main)).includes(contract.guide)) fail("Runner 主 agent 应追加指引");
	const s = load();
	await s.emit("session_start", { type: "session_start" }, sub);
	if ((await s.prompt(sub)).includes(contract.guide)) fail("Runner 子 agent 不应追加指引");
}
delete process.env.ORYN_RUNNER;

rmSync(dir, { recursive: true, force: true });
console.log("OK");
process.exit(0);
