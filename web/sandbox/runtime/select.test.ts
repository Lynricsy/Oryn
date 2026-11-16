import { JSDOM } from "jsdom";
import { afterEach, describe, expect, it, type Mock, vi } from "vitest";
import {
	MAX_SELECT_PINS,
	type PageMessage,
	parsePageMessage,
	parseParentMessage,
} from "./messages";
import { createPickLimiter, MAX_PICKS_PER_SECOND } from "./select";
import { install } from "./start";

const CHANNEL = "0123456789abcdef";
const MARK = "ab12cd34";
const CONFIG = { v: 1, mark: MARK, path: "index.html", runtime: true };

const PAGE = `<!doctype html><html><body data-oryn-src="${MARK}:50">
<h2 id="title" data-oryn-src="${MARK}:120" style="margin: 4px">标题
   一</h2>
<p id="para" data-oryn-src="${MARK}:200">段落 <b id="bold" data-oryn-src="${MARK}:210">粗体</b><script>var x = "不算文本";</script></p>
<span id="forged" data-oryn-src="zzzzzzzz:300">伪造</span>
<a id="ext" data-oryn-src="${MARK}:400" href="https://example.com/">外链</a>
<img id="pic" data-oryn-src="${MARK}:450" src="x.png">
</body></html>`;

const windows: { close(): void }[] = [];

function setup(
	config: unknown = CONFIG,
	fromPoint?: (x: number, y: number) => Element | null,
) {
	const parent = new JSDOM("", { url: "https://app.example/" }).window;
	const page = new JSDOM(PAGE, { url: "https://a-token.example/" }).window;
	windows.push(parent, page);
	if (fromPoint) {
		Object.defineProperty(page.document, "elementFromPoint", {
			value: fromPoint,
			configurable: true,
		});
	}
	// 帧回调排队、由测试手动推进：覆盖层的重画不依赖真实时钟
	const frames: (() => void)[] = [];
	page.requestAnimationFrame = (cb: FrameRequestCallback) => {
		frames.push(() => cb(0));
		return frames.length;
	};
	const flushFrame = () => {
		for (const cb of frames.splice(0)) cb();
	};
	const posted = vi.fn();
	vi.spyOn(parent, "postMessage").mockImplementation(posted);
	install(page, parent, config);
	const deliver = (body: Record<string, unknown>) =>
		page.dispatchEvent(
			new page.MessageEvent("message", {
				data: { v: 1, channel: CHANNEL, ...body },
				source: parent,
			}),
		);
	deliver({ kind: "hello" });
	posted.mockClear();
	const el = (id: string) => {
		const found = page.document.getElementById(id);
		if (!found) throw new Error(id);
		return found;
	};
	// 返回默认行为是否被阻止（页面里的点击都不是会导航的未拦链接）
	const click = (target: Element, x = 10, y = 20) =>
		!target.dispatchEvent(
			new page.MouseEvent("click", {
				bubbles: true,
				cancelable: true,
				clientX: x,
				clientY: y,
			}),
		);
	return {
		page,
		posted,
		deliver,
		el,
		click,
		flushFrame,
		pendingFrames: () => frames.length,
	};
}

type SelectMessage = Extract<PageMessage, { kind: "select" }>;

const sent = (posted: Mock) =>
	posted.mock.calls.map((c) => c[0] as Record<string, unknown>);
// 运行时发出的消息都符合页面 → 父的协议类型，这里按它读出 select
const selects = (posted: Mock) =>
	posted.mock.calls
		.map((c) => c[0] as PageMessage)
		.filter((m): m is SelectMessage => m.kind === "select");

afterEach(() => {
	vi.restoreAllMocks();
	for (const w of windows.splice(0)) w.close();
});

describe("选择与评论模式", () => {
	it("评论模式下点带标记的元素：src 是属性里的偏移、tag 正确，默认行为被阻止", () => {
		const { posted, deliver, el, click } = setup();
		deliver({ kind: "select-mode", mode: "comment", accent: "#2563eb" });
		expect(click(el("title"), 12, 34)).toBe(true);
		const [msg] = selects(posted);
		expect(msg).toMatchObject({
			v: 1,
			channel: CHANNEL,
			kind: "select",
			at: { x: 12, y: 34 },
			selection: {
				path: "index.html",
				src: 120,
				tag: "h2",
				text: "标题 一",
				textOnly: true,
			},
		});
		const style = msg?.selection?.style ?? {};
		expect(Object.keys(style).sort()).toEqual(
			[
				"background-color",
				"border-radius",
				"color",
				"font-size",
				"font-weight",
				"padding",
				"text-align",
			].sort(),
		);
		// 父页面一侧的解析接受运行时发出的消息
		expect(parsePageMessage(msg)).toEqual(msg);
	});

	it("页面脚本生成的元素与标记不符的元素 src 为 null", () => {
		const { page, posted, deliver, el, click } = setup();
		deliver({ kind: "select-mode", mode: "select", accent: "" });
		const made = page.document.createElement("div");
		made.textContent = "脚本生成";
		el("para").appendChild(made);
		click(made);
		click(el("forged"));
		expect(selects(posted).map((m) => m.selection)).toMatchObject([
			{ src: null, tag: "div", text: "脚本生成" },
			{ src: null, tag: "span" },
		]);
	});

	it("文本摘录跳过 script，含子元素或空元素不算全是文本", () => {
		const { posted, deliver, el, click } = setup();
		deliver({ kind: "select-mode", mode: "select", accent: "" });
		click(el("para"));
		click(el("pic"));
		expect(selects(posted).map((m) => m.selection)).toMatchObject([
			{ src: 200, tag: "p", text: "段落 粗体", textOnly: false },
			{ src: 450, tag: "img", text: "", textOnly: false },
		]);
	});

	it("点空白处（body）回报整个页面：selection 为 null", () => {
		const { page, posted, deliver, click } = setup();
		deliver({ kind: "select-mode", mode: "comment", accent: "" });
		click(page.document.body, 5, 6);
		expect(selects(posted)).toMatchObject([
			{ selection: null, at: { x: 5, y: 6 } },
		]);
	});

	it("preview-style 只收合规的值，preview-reset 还原", () => {
		const { deliver, el, click } = setup();
		deliver({ kind: "select-mode", mode: "select", accent: "" });
		const title = el("title") as HTMLElement;
		click(title);
		deliver({ kind: "preview-style", style: { color: "red" } });
		expect(title.getAttribute("style")).toBe("margin: 4px");
		deliver({
			kind: "preview-style",
			style: { color: "#ff0000", "font-size": "0px", margin: "9px" },
		});
		expect(title.style.color).toBe("rgb(255, 0, 0)");
		expect(title.style.fontSize).toBe("");
		expect(title.style.margin).toBe("4px");
		deliver({ kind: "preview-reset" });
		expect(title.getAttribute("style")).toBe("margin: 4px");
	});

	it("preview-text 只对全是文本的元素生效，preview-reset 还原文字与样式", () => {
		const { deliver, el, click } = setup();
		deliver({ kind: "select-mode", mode: "select", accent: "" });
		const para = el("para");
		const before = para.innerHTML;
		click(para);
		deliver({ kind: "preview-text", text: "<i>不该出现</i>" });
		expect(para.innerHTML).toBe(before);

		const bold = el("bold");
		click(bold);
		deliver({ kind: "preview-style", style: { "font-weight": "300" } });
		deliver({ kind: "preview-text", text: "<i>换掉</i>" });
		expect(bold.textContent).toBe("<i>换掉</i>");
		expect(bold.children).toHaveLength(0);
		deliver({ kind: "preview-reset" });
		expect(bold.textContent).toBe("粗体");
		expect(bold.hasAttribute("style")).toBe(false);
	});

	it("每秒最多处理 20 次选取", () => {
		const { posted, deliver, el, click } = setup();
		deliver({ kind: "select-mode", mode: "select", accent: "" });
		for (let i = 0; i < 25; i++) {
			// 超限的点击仍被吞掉，不交给页面
			expect(click(el("title"))).toBe(true);
		}
		expect(selects(posted)).toHaveLength(MAX_PICKS_PER_SECOND);
	});

	it("限速是滑动窗口", () => {
		const allow = createPickLimiter(3, 1000);
		expect([0, 10, 20, 30].map(allow)).toEqual([true, true, true, false]);
		expect(allow(999)).toBe(false);
		expect(allow(1000)).toBe(true);
		expect(allow(1005)).toBe(false);
		expect(allow(1010)).toBe(true);
	});

	it("模式关闭时点击照常；选择模式下点外链只算选取，不报 blocked", () => {
		const { posted, deliver, el, click } = setup();
		expect(click(el("ext"))).toBe(true);
		expect(sent(posted).map((m) => m.kind)).toEqual(["blocked"]);
		posted.mockClear();
		expect(click(el("title"))).toBe(false);
		expect(posted).not.toHaveBeenCalled();

		deliver({ kind: "select-mode", mode: "select", accent: "" });
		expect(click(el("ext"))).toBe(true);
		expect(sent(posted)).toMatchObject([
			{ kind: "select", selection: { src: 400, tag: "a" } },
		]);
		posted.mockClear();
		deliver({ kind: "select-mode", mode: null, accent: "" });
		expect(click(el("title"))).toBe(false);
		expect(posted).not.toHaveBeenCalled();
	});

	it("页面自己的监听器在选择模式下收不到点击", () => {
		const { page, deliver, el, click } = setup();
		const seen = vi.fn();
		el("title").addEventListener("click", seen);
		page.addEventListener("mousedown", seen);
		deliver({ kind: "select-mode", mode: "select", accent: "" });
		click(el("title"));
		el("title").dispatchEvent(
			new page.MouseEvent("mousedown", { bubbles: true, cancelable: true }),
		);
		expect(seen).not.toHaveBeenCalled();
	});

	it("runtime 为假的帧忽略选择类消息", () => {
		const { posted, deliver, el, click } = setup({ ...CONFIG, runtime: false });
		deliver({ kind: "select-mode", mode: "select", accent: "" });
		deliver({ kind: "select-source", src: 120 });
		expect(click(el("title"))).toBe(false);
		expect(posted).not.toHaveBeenCalled();
	});

	it("select-source 重新选中同一偏移，select-locate 回包围盒且不改选中", () => {
		const { posted, deliver } = setup();
		deliver({ kind: "select-source", src: 210 });
		deliver({ kind: "select-source", src: 999 });
		deliver({ kind: "select-source", src: 120 });
		deliver({ kind: "select-locate", src: 210 });
		deliver({ kind: "select-locate", src: 998 });
		deliver({ kind: "select-source", src: null });
		expect(selects(posted)).toMatchObject([
			{ selection: { src: 210, tag: "b" } },
			{ selection: null },
			{ selection: { src: 120, tag: "h2" } },
			{
				selection: { src: 120 },
				located: { src: 210, box: { x: 0, y: 0, width: 0, height: 0 } },
			},
			{ selection: { src: 120 }, located: { src: 998, box: null } },
		]);
		expect(selects(posted)).toHaveLength(5);
	});

	it("select-pick 与 select-point 用坐标驱动，只在模式开启时生效", () => {
		let hit: Element | null = null;
		const { posted, deliver, el } = setup(CONFIG, () => hit);
		hit = el("bold");
		deliver({ kind: "select-pick", x: 1, y: 2 });
		expect(posted).not.toHaveBeenCalled();
		deliver({ kind: "select-mode", mode: "comment", accent: "" });
		deliver({ kind: "select-point", x: 1, y: 2 });
		deliver({ kind: "select-pick", x: 3, y: 4 });
		expect(selects(posted)).toMatchObject([
			{ selection: { src: 210, tag: "b" }, at: { x: 3, y: 4 } },
		]);
	});

	it("覆盖层在 closed shadow root 里，名字被运行时占用", () => {
		const { page, posted, deliver, el, click, flushFrame, pendingFrames } =
			setup();
		deliver({ kind: "select-mode", mode: "comment", accent: "#ff0000" });
		deliver({
			kind: "select-pins",
			pins: [{ n: 1, src: 120, answered: false }],
		});
		click(el("title"));
		flushFrame();
		const host = page.document.querySelector("oryn-overlay") as HTMLElement;
		expect(host).not.toBeNull();
		expect(host.parentElement).toBe(page.document.documentElement);
		expect(host.shadowRoot).toBeNull();
		expect(() => host.attachShadow({ mode: "open" })).toThrow();
		expect(() =>
			page.customElements.define(
				"oryn-overlay",
				class extends page.HTMLElement {},
			),
		).toThrow();
		// 选取时跳过覆盖层本身：算点在空白处
		click(host);
		expect(selects(posted).at(-1)?.selection).toBeNull();
		expect(page.document.querySelectorAll("oryn-overlay")).toHaveLength(1);
		// 关掉模式、清掉图钉后不再排下一帧
		deliver({ kind: "select-mode", mode: null, accent: "" });
		deliver({ kind: "select-pins", pins: [] });
		flushFrame();
		expect(pendingFrames()).toBe(0);
	});

	it("覆盖层用缓存的原生方法，页面改写原型拿不到 shadow root 里的节点", () => {
		const { page, deliver, el, click, flushFrame } = setup();
		const leaked = vi.fn();
		const proto = page.Node.prototype;
		const append = proto.appendChild;
		proto.appendChild = function <T extends Node>(this: Node, child: T): T {
			leaked(this);
			return append.call(this, child) as T;
		};
		deliver({ kind: "select-mode", mode: "select", accent: "" });
		click(el("title"));
		flushFrame();
		expect(page.document.querySelector("oryn-overlay")).not.toBeNull();
		expect(leaked).not.toHaveBeenCalled();
	});
});

describe("选择类消息的协议", () => {
	const env = { v: 1, channel: CHANNEL };

	it("preview-style 解析时丢掉不合规的项", () => {
		expect(
			parseParentMessage({
				...env,
				kind: "preview-style",
				style: {
					color: "red",
					"background-color": "#00ff0080",
					padding: "1px 2.5px",
					"text-align": "middle",
					margin: "1px",
				},
			}),
		).toEqual({
			...env,
			kind: "preview-style",
			style: { "background-color": "#00ff0080", padding: "1px 2.5px" },
		});
	});

	it("select-pins 最多 200 个，字段逐个校验", () => {
		const pin = { n: 1, src: 5, answered: true };
		expect(
			parseParentMessage({ ...env, kind: "select-pins", pins: [pin] }),
		).toEqual({ ...env, kind: "select-pins", pins: [pin] });
		expect(
			parseParentMessage({
				...env,
				kind: "select-pins",
				pins: Array.from({ length: MAX_SELECT_PINS + 1 }, () => pin),
			}),
		).toBeNull();
		for (const bad of [
			{ ...pin, n: 0 },
			{ ...pin, src: -1 },
			{ ...pin, answered: "yes" },
		]) {
			expect(
				parseParentMessage({ ...env, kind: "select-pins", pins: [bad] }),
			).toBeNull();
		}
		expect(
			parseParentMessage({
				...env,
				kind: "select-pins",
				pins: [{ n: 2, src: null, answered: false }],
			}),
		).not.toBeNull();
	});

	it("select-mode 只认三种模式", () => {
		expect(
			parseParentMessage({
				...env,
				kind: "select-mode",
				mode: "edit",
				accent: "",
			}),
		).toBeNull();
		expect(
			parseParentMessage({
				...env,
				kind: "select-mode",
				mode: null,
				accent: "",
			}),
		).toEqual({ ...env, kind: "select-mode", mode: null, accent: "" });
	});

	it("页面发来的 select 逐个字段校验", () => {
		const selection = {
			path: "index.html",
			src: 3,
			tag: "h1",
			text: "t",
			textOnly: true,
			style: {
				color: "rgb(0, 0, 0)",
				"background-color": "",
				"font-size": "16px",
				"font-weight": "400",
				"text-align": "start",
				padding: "0px",
				"border-radius": "0px",
			},
		};
		const ok = { ...env, kind: "select", selection };
		expect(parsePageMessage(ok)).toEqual(ok);
		expect(
			parsePageMessage({ ...ok, selection: { ...selection, tag: "H1" } }),
		).toBeNull();
		expect(
			parsePageMessage({
				...ok,
				selection: { ...selection, style: { color: "x" } },
			}),
		).toBeNull();
		expect(
			parsePageMessage({ ...ok, selection: { ...selection, src: 1.5 } }),
		).toBeNull();
		expect(
			parsePageMessage({ ...ok, located: { src: 3, box: { x: 0, y: 0 } } }),
		).toBeNull();
		expect(
			parsePageMessage({ ...env, kind: "select", selection: null, extra: 1 }),
		).toEqual({ ...env, kind: "select", selection: null });
	});
});
