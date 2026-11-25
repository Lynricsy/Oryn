import { JSDOM, VirtualConsole } from "jsdom";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	MAX_DESIGN_LINKS,
	type PageMessage,
	parseDesignInfo,
	parsePageMessage,
	parseParentMessage,
} from "./messages";
import { designFrameOf, install, navigateTarget, readConfig } from "./start";

// M29 C7：设计画布的协议扩展——选中带 loc、select-loc、design-reload、navigate、ready.design 与 navigated。

const CHANNEL = "0123456789abcdef";
const env = { v: 1, channel: CHANNEL } as const;
const DESIGN = { frame: "login", links: ["signup"] };
const DEV = {
	v: 1,
	mark: "",
	path: "dist/login.html",
	root: "",
	runtime: true,
	design: DESIGN,
};
const STATIC = { v: 1, mark: "", path: "dist/login.html", runtime: true };

const PAGE = `<!doctype html><html><body>
<main data-oryn-loc="src/frames/login.tsx:3:5">
	<h1 id="title" data-oryn-loc="src/frames/login.tsx:4:7">登录</h1>
	<button id="go"><span id="inner">进入</span></button>
	<p id="bad" data-oryn-loc="../etc/passwd:1:1">坏标记</p>
</main>
<footer id="plain">没有位置</footer>
<i data-oryn-loc="src/frames/login.tsx:9:3">一</i><i data-oryn-loc="src/frames/login.tsx:9:3">二</i>
</body></html>`;

const windows: { close(): void }[] = [];

function setup(config: unknown = DEV) {
	const parent = new JSDOM("", { url: "https://app.example/" }).window;
	const errors: string[] = [];
	const vc = new VirtualConsole();
	vc.on("jsdomError", (e: Error) => errors.push(e.message));
	const page = new JSDOM(PAGE, {
		url: "https://t-x-p5173.orb.example/frames/login",
		virtualConsole: vc,
	}).window;
	windows.push(parent, page);
	page.requestAnimationFrame = () => 0;
	const posted = vi.fn();
	vi.spyOn(parent, "postMessage").mockImplementation(posted);
	install(page, parent, config);
	const deliver = (body: Record<string, unknown>) =>
		page.dispatchEvent(
			new page.MessageEvent("message", {
				data: { ...env, ...body },
				source: parent,
			}),
		);
	const el = (id: string) => {
		const found = page.document.getElementById(id);
		if (!found) throw new Error(id);
		return found;
	};
	const sent = () => posted.mock.calls.map((c) => c[0] as PageMessage);
	return { page, posted, deliver, el, sent, errors };
}

afterEach(() => {
	vi.restoreAllMocks();
	for (const w of windows.splice(0)) w.close();
});

describe("设计画布的消息解析", () => {
	it("父 → 页：select-loc、design-reload、navigate 的正例", () => {
		expect(
			parseParentMessage({
				...env,
				kind: "select-loc",
				loc: "src/frames/login.tsx:12:5",
			}),
		).toEqual({ ...env, kind: "select-loc", loc: "src/frames/login.tsx:12:5" });
		expect(
			parseParentMessage({ ...env, kind: "design-reload", extra: 1 }),
		).toEqual({ ...env, kind: "design-reload" });
		expect(
			parseParentMessage({ ...env, kind: "navigate", frame: "signup" }),
		).toEqual({ ...env, kind: "navigate", frame: "signup" });
	});

	it("父 → 页：反例", () => {
		for (const loc of [
			"",
			"src/frames/login.tsx",
			"src/frames/login.tsx:0:1",
			"lib/x.tsx:1:1",
			"src/frames/x.css:1:1",
			`src/${"a".repeat(600)}.tsx:1:1`,
			12,
			null,
		])
			expect(parseParentMessage({ ...env, kind: "select-loc", loc })).toBe(
				null,
			);
		for (const frame of ["", "Login", "../x", "a/b", "-x", 1, undefined])
			expect(parseParentMessage({ ...env, kind: "navigate", frame })).toBe(
				null,
			);
	});

	it("页 → 父：ready 带合法 design，navigated", () => {
		const ready = { ...env, kind: "ready", params: {}, design: DESIGN };
		expect(parsePageMessage(ready)).toEqual(ready);
		// 旧运行时不带 design
		expect(parsePageMessage({ ...env, kind: "ready", params: {} })).toEqual({
			...env,
			kind: "ready",
			params: {},
		});
		expect(
			parsePageMessage({ ...env, kind: "navigated", frame: "signup" }),
		).toEqual({ ...env, kind: "navigated", frame: "signup" });
	});

	it("页 → 父：反例", () => {
		for (const design of [
			null,
			[],
			{ frame: "x" },
			{ frame: "X", links: [] },
			{ frame: "x", links: ["ok", "Bad"] },
			{ frame: "x", links: "y" },
		])
			expect(
				parsePageMessage({ ...env, kind: "ready", params: {}, design }),
			).toBe(null);
		expect(parsePageMessage({ ...env, kind: "navigated", frame: "" })).toBe(
			null,
		);
		expect(parsePageMessage({ ...env, kind: "navigated" })).toBe(null);
		expect(
			parseDesignInfo({
				frame: "x",
				links: Array.from({ length: MAX_DESIGN_LINKS + 1 }, () => "y"),
			}),
		).toBe(null);
	});

	it("选中里的 loc：缺省、null、合法都接受，不合规整条丢弃", () => {
		const selection = {
			path: "dist/login.html",
			src: null,
			tag: "h1",
			text: "登录",
			textOnly: true,
			style: {
				color: "",
				"background-color": "",
				"font-size": "",
				"font-weight": "",
				"text-align": "",
				padding: "",
				"border-radius": "",
			},
		};
		const msg = (extra: Record<string, unknown>) => ({
			...env,
			kind: "select",
			selection: { ...selection, ...extra },
		});
		expect(parsePageMessage(msg({}))).toEqual(msg({}));
		expect(parsePageMessage(msg({ loc: null }))).toEqual(msg({ loc: null }));
		const ok = msg({ loc: "src/frames/login.tsx:4:7" });
		expect(parsePageMessage(ok)).toEqual(ok);
		expect(parsePageMessage(msg({ loc: "../x.tsx:1:1" }))).toBe(null);
		expect(parsePageMessage(msg({ loc: 3 }))).toBe(null);
	});
});

describe("设计画布的运行时配置", () => {
	it("readConfig 接受合法 design，不合法时丢弃 design 但不连累其余", () => {
		expect(readConfig(DEV)).toEqual(DEV);
		const bad = readConfig({ ...DEV, design: { frame: "../x", links: [] } });
		expect(bad.design).toBeUndefined();
		expect(bad.runtime).toBe(true);
		expect(readConfig({ ...DEV, mark: "BAD" }).design).toBeUndefined();
	});

	it("帧与跳转目标：dev 帧 ./<id>，静态产物 ./<id>.html，非设计页不跳", () => {
		expect(designFrameOf(readConfig(DEV))).toBe("login");
		expect(designFrameOf(readConfig(STATIC))).toBe("login");
		expect(
			designFrameOf(readConfig({ ...STATIC, path: "index.html" })),
		).toBeNull();
		expect(navigateTarget(readConfig(DEV), "signup")).toBe("./signup");
		expect(navigateTarget(readConfig(STATIC), "signup")).toBe("./signup.html");
		expect(
			navigateTarget(readConfig({ ...STATIC, path: "index.html" }), "signup"),
		).toBeNull();
	});
});

describe("设计画布的页面运行时", () => {
	it("dev 帧的 ready 带 design，随后报 navigated", () => {
		const { deliver, sent } = setup();
		deliver({ kind: "hello" });
		expect(sent()).toEqual([
			{ ...env, kind: "ready", params: {}, design: DESIGN },
			{ ...env, kind: "navigated", frame: "login" },
		]);
		for (const m of sent()) expect(parsePageMessage(m)).toEqual(m);
	});

	it("静态产物的 ready 不带 design，navigated 取自路径；普通页面两者都没有", () => {
		const a = setup(STATIC);
		a.deliver({ kind: "hello" });
		expect(a.sent()).toEqual([
			{ ...env, kind: "ready", params: {} },
			{ ...env, kind: "navigated", frame: "login" },
		]);
		const b = setup({ ...STATIC, path: "index.html" });
		b.deliver({ kind: "hello" });
		expect(b.sent()).toEqual([{ ...env, kind: "ready", params: {} }]);
	});

	it("选中带最近祖先的 loc；没有或不合规的祖先链为 null", () => {
		const { page, deliver, el, sent, posted } = setup();
		deliver({ kind: "hello" });
		posted.mockClear();
		deliver({ kind: "select-mode", mode: "select", accent: "" });
		for (const id of ["title", "inner", "bad", "plain"])
			el(id).dispatchEvent(
				new page.MouseEvent("click", { bubbles: true, cancelable: true }),
			);
		const locs = sent()
			.filter((m) => m.kind === "select")
			.map((m) => (m.kind === "select" ? m.selection?.loc : undefined));
		expect(locs).toEqual([
			"src/frames/login.tsx:4:7",
			// span → button → main：取到 main 的
			"src/frames/login.tsx:3:5",
			// 自己的不合规，取 main 的
			"src/frames/login.tsx:3:5",
			null,
		]);
	});

	it("select-loc 选中带该位置的第一个元素，找不到回 null", () => {
		const { deliver, sent, posted } = setup();
		deliver({ kind: "hello" });
		posted.mockClear();
		deliver({ kind: "select-loc", loc: "src/frames/login.tsx:9:3" });
		deliver({ kind: "select-loc", loc: "src/frames/login.tsx:99:1" });
		expect(sent()).toMatchObject([
			{
				kind: "select",
				selection: { tag: "i", text: "一", loc: "src/frames/login.tsx:9:3" },
			},
			{ kind: "select", selection: null },
		]);
	});

	it("navigate 跳到别的帧、同帧不跳；design-reload 重新加载；非设计页都忽略", () => {
		const a = setup();
		a.deliver({ kind: "hello" });
		a.deliver({ kind: "navigate", frame: "login" });
		expect(a.errors).toEqual([]);
		a.deliver({ kind: "navigate", frame: "signup" });
		expect(a.errors).toHaveLength(1);
		a.deliver({ kind: "design-reload" });
		// jsdom 不实现导航（含 reload），调用即报 not implemented：两次调用都到了
		expect(a.errors).toHaveLength(2);
		for (const e of a.errors) expect(e).toMatch(/navigation/);

		const b = setup({ ...STATIC, path: "index.html" });
		b.deliver({ kind: "hello" });
		b.deliver({ kind: "navigate", frame: "signup" });
		b.deliver({ kind: "design-reload" });
		expect(b.errors).toEqual([]);
	});
});
