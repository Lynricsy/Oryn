import { JSDOM } from "jsdom";
import { afterEach, describe, expect, it, type Mock, vi } from "vitest";
import {
	MAX_MESSAGE_BYTES,
	MAX_SELECT_PINS,
	parsePageMessage,
	parseParentMessage,
	utf8Length,
	type ViewerAnchor,
} from "./messages";
import "./main";
import { install, readConfig } from "./start";
import type { ViewerBridge } from "./viewer";

const ANCHOR: ViewerAnchor = {
	page: { number: 1, name: "" },
	x: 0.25,
	y: 0.5,
	range: null,
	block: null,
	text: "季度报告",
};

const CHANNEL = "0123456789abcdef";
const OTHER = "fedcba9876543210";
const CONFIG = { v: 1, mark: "ab12cd34", path: "index.html", runtime: true };

const PAGE = `<!doctype html><body>
<a id="ext" href="https://example.com/x?q=1">外链</a>
<a id="same" href="/other.html">同源</a>
<a id="mail" href="mailto:a@example.com">邮件</a>
<p id="text"><span>点这里</span></p>
</body>`;

function setup(config: unknown = CONFIG) {
	const parent = new JSDOM("", { url: "https://app.example/" }).window;
	const other = new JSDOM("", { url: "https://evil.example/" }).window;
	const page = new JSDOM(PAGE, { url: "https://a-token.example/" }).window;
	const posted = vi.fn();
	vi.spyOn(parent, "postMessage").mockImplementation(posted);
	const scrollTo = vi.fn();
	vi.spyOn(page, "scrollTo").mockImplementation(scrollTo);
	install(page, parent, config);
	const deliver = (data: unknown, source: Window = parent) =>
		page.dispatchEvent(new page.MessageEvent("message", { data, source }));
	const click = (id: string) => {
		const el = page.document.getElementById(id);
		if (!el) throw new Error(id);
		let prevented = false;
		// 冒泡阶段记录运行时（捕获阶段）有没有拦下，再自己拦下，免得 jsdom 去导航
		page.document.addEventListener(
			"click",
			(e) => {
				prevented = e.defaultPrevented;
				e.preventDefault();
			},
			{ once: true },
		);
		el.dispatchEvent(
			new page.MouseEvent("click", { bubbles: true, cancelable: true }),
		);
		return prevented;
	};
	return { parent, other, page, posted, scrollTo, deliver, click };
}

const sent = (posted: Mock) => posted.mock.calls.map((c) => c[0]);

afterEach(() => {
	vi.restoreAllMocks();
});

describe("页面运行时", () => {
	it("非父窗口来源的消息被忽略", () => {
		const { other, posted, deliver } = setup();
		deliver({ v: 1, channel: CHANNEL, kind: "hello" }, other);
		deliver({ v: 1, channel: CHANNEL, kind: "keep-scroll" });
		expect(posted).not.toHaveBeenCalled();

		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		expect(sent(posted)).toEqual([
			{ v: 1, channel: CHANNEL, kind: "ready", params: {} },
		]);
		expect(posted.mock.calls[0]?.[1]).toBe("*");
	});

	it("hello 之后带错 channel 的消息被忽略", () => {
		const { posted, scrollTo, deliver } = setup();
		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		posted.mockClear();
		deliver({ v: 1, channel: OTHER, kind: "hello" });
		deliver({ v: 1, channel: OTHER, kind: "keep-scroll" });
		deliver({ v: 1, channel: OTHER, kind: "restore-scroll", x: 1, y: 2 });
		deliver({ v: 2, channel: CHANNEL, kind: "keep-scroll" });
		expect(posted).not.toHaveBeenCalled();
		expect(scrollTo).not.toHaveBeenCalled();
	});

	it("keep-scroll 回 scroll，restore-scroll 滚到指定位置", () => {
		const { posted, scrollTo, deliver } = setup();
		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		posted.mockClear();
		deliver({ v: 1, channel: CHANNEL, kind: "keep-scroll" });
		expect(sent(posted)).toEqual([
			{ v: 1, channel: CHANNEL, kind: "scroll", x: 0, y: 0 },
		]);
		deliver({ v: 1, channel: CHANNEL, kind: "restore-scroll", x: 10, y: 240 });
		expect(scrollTo).toHaveBeenCalledWith(10, 240);
	});

	it("协议消息不交给页面自己的监听器", () => {
		const { page, deliver } = setup();
		const seen = vi.fn();
		page.addEventListener("message", seen);
		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		expect(seen).not.toHaveBeenCalled();
		deliver({ hello: "页面自己的消息" });
		expect(seen).toHaveBeenCalledTimes(1);
	});

	it("点击外链发出 blocked 且默认行为被阻止", () => {
		const { posted, deliver, click } = setup();
		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		posted.mockClear();
		expect(click("ext")).toBe(true);
		expect(sent(posted)).toEqual([
			{
				v: 1,
				channel: CHANNEL,
				kind: "blocked",
				url: "https://example.com/x?q=1",
			},
		]);
	});

	it("同源链接与非 http(s) 链接不拦", () => {
		const { posted, deliver, click } = setup();
		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		posted.mockClear();
		expect(click("same")).toBe(false);
		expect(click("mail")).toBe(false);
		expect(click("text")).toBe(false);
		expect(posted).not.toHaveBeenCalled();
	});

	it("hello 之前点外链也会拦下，只是没有通道可报", () => {
		const { posted, click } = setup();
		expect(click("ext")).toBe(true);
		expect(posted).not.toHaveBeenCalled();
	});

	it("window.oryn 冻结且不可重新赋值，use 恒返回 null", async () => {
		const { page } = setup();
		const w = page as unknown as {
			oryn: { version: number; use: (name: string) => Promise<unknown> };
		};
		const oryn = w.oryn;
		expect(oryn.version).toBe(1);
		expect(Object.isFrozen(oryn)).toBe(true);
		expect(() => {
			w.oryn = { version: 2, use: () => Promise.resolve(1) };
		}).toThrow(TypeError);
		expect(() => Object.defineProperty(page, "oryn", { value: {} })).toThrow(
			TypeError,
		);
		expect(Reflect.set(oryn, "version", 2)).toBe(false);
		expect(w.oryn).toBe(oryn);
		await expect(oryn.use("db")).resolves.toBeNull();
	});

	it("runtime 为假时不发 ready，滚动与链接拦截照常", () => {
		const { posted, scrollTo, deliver, click } = setup({
			...CONFIG,
			runtime: false,
		});
		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		expect(posted).not.toHaveBeenCalled();
		deliver({ v: 1, channel: CHANNEL, kind: "restore-scroll", x: 0, y: 5 });
		expect(scrollTo).toHaveBeenCalledWith(0, 5);
		expect(click("ext")).toBe(true);
		expect(sent(posted).map((m) => m.kind)).toEqual(["blocked"]);
	});

	it("配置不合法时按旧版本处理", () => {
		const { posted, deliver } = setup({ v: 1, mark: "BAD", runtime: true });
		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		expect(posted).not.toHaveBeenCalled();
	});

	it("DOM 还在加载时 ready 等到 DOMContentLoaded", () => {
		const parent = new JSDOM("", { url: "https://app.example/" }).window;
		const page = new JSDOM("", { url: "https://a-token.example/" }).window;
		const posted = vi.fn();
		vi.spyOn(parent, "postMessage").mockImplementation(posted);
		Object.defineProperty(page.document, "readyState", {
			value: "loading",
			configurable: true,
		});
		install(page, parent, CONFIG);
		page.dispatchEvent(
			new page.MessageEvent("message", {
				data: { v: 1, channel: CHANNEL, kind: "hello" },
				source: parent,
			}),
		);
		expect(posted).not.toHaveBeenCalled();
		page.document.dispatchEvent(new page.Event("DOMContentLoaded"));
		expect(sent(posted).map((m) => m.kind)).toEqual(["ready"]);
	});

	it("入口只认第一次 __orynStart 并随即移除它", () => {
		const w = window as unknown as {
			__orynStart?: (c: unknown) => void;
			oryn?: unknown;
		};
		const start = w.__orynStart;
		expect(typeof start).toBe("function");
		start?.(CONFIG);
		expect("__orynStart" in window).toBe(false);
		expect(w.oryn).toBeDefined();
	});

	it("查看器页（无源码标记）请父页面进入演示", () => {
		const { page, posted, deliver } = setup({
			v: 1,
			mark: "",
			path: "slides.json",
			root: "/",
			runtime: true,
		});
		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		posted.mockClear();
		const w = page as unknown as { __orynPresent?: (on: boolean) => void };
		expect(Object.keys(page)).not.toContain("__orynPresent");
		w.__orynPresent?.(true);
		w.__orynPresent?.(false);
		expect(sent(posted)).toEqual([
			{ v: 1, channel: CHANNEL, kind: "present", on: true },
			{ v: 1, channel: CHANNEL, kind: "present", on: false },
		]);
	});

	it("查看器页装 __orynViewer：重放模式与图钉，评论模式下报点选", () => {
		const { page, posted, deliver } = setup({
			v: 1,
			mark: "",
			path: "document.docx",
			root: "/",
			runtime: true,
			viewer: true,
		});
		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		const w = page as unknown as { __orynViewer?: ViewerBridge };
		const bridge = w.__orynViewer;
		expect(bridge).toBeDefined();
		if (!bridge) return;
		expect(Object.keys(page)).not.toContain("__orynViewer");
		expect(Object.isFrozen(bridge)).toBe(true);
		expect(() => {
			Reflect.set(page, "__orynViewer", null) ||
				Reflect.deleteProperty(page, "__orynViewer");
		}).not.toThrow();
		expect(w.__orynViewer).toBe(bridge);

		const pin = { n: 1, answered: false, anchor: ANCHOR };
		deliver({
			v: 1,
			channel: CHANNEL,
			kind: "select-mode",
			mode: "comment",
			accent: "",
		});
		deliver({ v: 1, channel: CHANNEL, kind: "anchor-pins", pins: [pin] });
		const modes: unknown[] = [];
		const pins: unknown[] = [];
		const located: unknown[] = [];
		bridge.onMode((m) => modes.push(m));
		bridge.onPins((p) => pins.push(p));
		const off = bridge.onLocate((a) => located.push(a));
		expect(modes).toEqual(["comment"]);
		expect(pins).toEqual([[pin]]);
		deliver({ v: 1, channel: CHANNEL, kind: "anchor-locate", anchor: ANCHOR });
		off();
		deliver({ v: 1, channel: CHANNEL, kind: "anchor-locate", anchor: ANCHOR });
		expect(located).toEqual([ANCHOR]);

		posted.mockClear();
		bridge.pick(ANCHOR, { x: 10, y: 20 });
		bridge.pick({ ...ANCHOR, x: 2 }, { x: 10, y: 20 });
		bridge.pick(null, { x: 1, y: 2 });
		expect(sent(posted)).toEqual([
			{
				v: 1,
				channel: CHANNEL,
				kind: "anchor-pick",
				anchor: ANCHOR,
				at: { x: 10, y: 20 },
			},
			{
				v: 1,
				channel: CHANNEL,
				kind: "anchor-pick",
				anchor: null,
				at: { x: 1, y: 2 },
			},
		]);
		deliver({
			v: 1,
			channel: CHANNEL,
			kind: "select-mode",
			mode: null,
			accent: "",
		});
		expect(modes).toEqual(["comment", null]);
		posted.mockClear();
		bridge.pick(ANCHOR, { x: 10, y: 20 });
		expect(posted).not.toHaveBeenCalled();
	});

	it("非查看器页没有 __orynViewer", () => {
		const { page } = setup();
		expect(
			(page as unknown as { __orynViewer?: unknown }).__orynViewer,
		).toBeUndefined();
	});

	it("readConfig 接受空标记与帧根，帧根不以 / 结尾时丢弃", () => {
		expect(
			readConfig({
				v: 1,
				mark: "",
				path: "document.md",
				root: "/_af/x/",
				runtime: true,
			}),
		).toEqual({
			v: 1,
			mark: "",
			path: "document.md",
			root: "/_af/x/",
			runtime: true,
		});
		expect(readConfig({ ...CONFIG, root: "/_af/x" }).root).toBe("");
		expect(readConfig({ ...CONFIG, mark: "BAD" }).runtime).toBe(false);
	});
});

describe("消息协议", () => {
	it("规整并拒绝不合格的消息", () => {
		expect(
			parseParentMessage({ v: 1, channel: CHANNEL, kind: "hello", x: 1 }),
		).toEqual({ v: 1, channel: CHANNEL, kind: "hello" });
		expect(parseParentMessage({ v: 1, channel: "ABC", kind: "hello" })).toBe(
			null,
		);
		expect(
			parseParentMessage({
				v: 1,
				channel: CHANNEL,
				kind: "restore-scroll",
				x: Number.NaN,
				y: 0,
			}),
		).toBe(null);
		expect(parseParentMessage([1])).toBe(null);
		expect(
			parsePageMessage({ v: 1, channel: CHANNEL, kind: "ready", params: [] }),
		).toBe(null);
		expect(
			parsePageMessage({ v: 1, channel: CHANNEL, kind: "blocked", url: "x" }),
		).toEqual({ v: 1, channel: CHANNEL, kind: "blocked", url: "x" });
		expect(
			parsePageMessage({ v: 1, channel: CHANNEL, kind: "present", on: "yes" }),
		).toBe(null);
	});

	it("查看器锚点逐字段校验", () => {
		const env = { v: 1, channel: CHANNEL };
		const pick = (anchor: unknown) =>
			parsePageMessage({
				...env,
				kind: "anchor-pick",
				anchor,
				at: { x: 1, y: 2 },
				extra: 1,
			});
		expect(pick({ ...ANCHOR, extra: true })).toEqual({
			...env,
			kind: "anchor-pick",
			anchor: ANCHOR,
			at: { x: 1, y: 2 },
		});
		expect(pick(null)).toEqual({
			...env,
			kind: "anchor-pick",
			anchor: null,
			at: { x: 1, y: 2 },
		});
		const sheet = {
			page: { number: 2, name: "明细" },
			x: null,
			y: null,
			range: "B2:C3",
			block: null,
			text: "",
		};
		expect(pick(sheet)).not.toBe(null);
		const doc = {
			page: null,
			x: null,
			y: null,
			range: null,
			block: 3,
			text: "第一段",
		};
		expect(pick(doc)).not.toBe(null);
		for (const bad of [
			{ ...ANCHOR, x: 0.5, y: null },
			{ ...ANCHOR, x: 1.2 },
			{ ...ANCHOR, x: Number.NaN },
			{ ...ANCHOR, y: undefined },
			{ ...ANCHOR, range: "b2" },
			{ ...ANCHOR, range: "A0" },
			{ ...ANCHOR, block: -1 },
			{ ...ANCHOR, block: 1.5 },
			{ ...ANCHOR, text: "字".repeat(121) },
			{ ...ANCHOR, page: { number: 0, name: "" } },
			{ ...ANCHOR, page: { number: 1, name: "名".repeat(121) } },
			{ ...ANCHOR, page: { number: 1 } },
			"anchor",
		]) {
			expect(pick(bad)).toBe(null);
		}
		expect(
			parsePageMessage({
				...env,
				kind: "anchor-pick",
				anchor: ANCHOR,
				at: { x: "1", y: 2 },
			}),
		).toBe(null);
		expect(
			parseParentMessage({ ...env, kind: "anchor-locate", anchor: ANCHOR }),
		).toEqual({ ...env, kind: "anchor-locate", anchor: ANCHOR });
		expect(
			parseParentMessage({
				...env,
				kind: "anchor-locate",
				anchor: { ...ANCHOR, x: -1 },
			}),
		).toBe(null);
		const pins = [{ n: 1, answered: true, anchor: ANCHOR }];
		expect(parseParentMessage({ ...env, kind: "anchor-pins", pins })).toEqual({
			...env,
			kind: "anchor-pins",
			pins,
		});
		expect(
			parseParentMessage({
				...env,
				kind: "anchor-pins",
				pins: [{ n: 0, answered: true, anchor: ANCHOR }],
			}),
		).toBe(null);
		expect(
			parseParentMessage({
				...env,
				kind: "anchor-pins",
				pins: Array.from({ length: MAX_SELECT_PINS + 1 }, (_, i) => ({
					n: i + 1,
					answered: false,
					anchor: ANCHOR,
				})),
			}),
		).toBe(null);
	});

	it("超过 64 KiB 的消息被丢弃", () => {
		const url = "a".repeat(MAX_MESSAGE_BYTES);
		expect(
			parsePageMessage({ v: 1, channel: CHANNEL, kind: "blocked", url }),
		).toBe(null);
	});

	it("utf8Length 按 UTF-8 计字节", () => {
		expect(utf8Length("a")).toBe(1);
		expect(utf8Length("é")).toBe(2);
		expect(utf8Length("中")).toBe(3);
		expect(utf8Length("😀")).toBe(4);
		expect(utf8Length("\ud800")).toBe(3);
	});
});
