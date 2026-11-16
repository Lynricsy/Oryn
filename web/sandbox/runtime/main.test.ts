import { JSDOM } from "jsdom";
import { afterEach, describe, expect, it, type Mock, vi } from "vitest";
import {
	MAX_MESSAGE_BYTES,
	parsePageMessage,
	parseParentMessage,
	utf8Length,
} from "./messages";
import "./main";
import { install } from "./start";

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
