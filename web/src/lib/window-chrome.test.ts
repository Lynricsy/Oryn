/** 自绘标题栏适配：判定（浏览器 / 移动 / 旧壳无变化）、布局解析、窗口状态、动作与拖动区（假 window）。 */
import { describe, expect, it, vi } from "vitest";
import { HOST_EVENT, type Invoke } from "./host";
import { createWindowChrome, inDragRegion, parseChrome } from "./window-chrome";

const CHROME = {
	platform: "windows",
	controls: "custom-right",
	buttons: ["minimize", "maximize", "close"],
	inset: { top: 48, left: 0, right: 0 },
};

/** 带真实 DOM 文档的假 window：`meta` 写进 `__ORYN_HOST__`，`invoke` 应答 `host_window`。 */
function fakeWindow(
	meta: Record<string, unknown> | null,
	reply: (args?: Record<string, unknown>) => unknown = () => ({
		maximized: false,
		fullscreen: false,
		focused: true,
	}),
) {
	const doc = document.implementation.createHTMLDocument("t");
	const target = new EventTarget();
	const invoke = vi.fn(async (_cmd: string, args?: Record<string, unknown>) =>
		reply(args),
	);
	const win = Object.assign(target, {
		document: doc,
		...(meta ? { __ORYN_HOST__: meta } : {}),
		__TAURI__: { core: { invoke: invoke as unknown as Invoke } },
	}) as unknown as Window;
	return {
		win,
		doc,
		invoke,
		emit: (detail: unknown) =>
			target.dispatchEvent(new CustomEvent(HOST_EVENT, { detail })),
	};
}

const desktop = (chrome: unknown, extra: Record<string, unknown> = {}) => ({
	kind: "tauri",
	platform: "windows",
	version: "0.1.0",
	bridge: 3,
	chrome,
	...extra,
});

/** 等 invoke 的应答链（async 函数 → then）走完：只排空微任务，不等真实时间。 */
async function flush() {
	for (let i = 0; i < 5; i++) await Promise.resolve();
}

describe("判定", () => {
	it("浏览器、移动壳、旧壳（bridge < 3）、没有 chrome、controls=none → null，且不调用壳", () => {
		for (const meta of [
			null,
			{ kind: "tauri", platform: "android", bridge: 3 },
			desktop(CHROME, { bridge: 2 }),
			desktop(undefined),
			desktop({ ...CHROME, controls: "none" }),
			desktop({ ...CHROME, platform: "android" }),
		]) {
			const f = fakeWindow(meta);
			expect(createWindowChrome(f.win)).toBeNull();
			expect(f.invoke).not.toHaveBeenCalled();
		}
	});

	it("桌面壳 bridge 3：解析布局，挂载即取一次窗口状态", async () => {
		const f = fakeWindow(desktop(CHROME), () => ({
			maximized: true,
			fullscreen: false,
			focused: false,
		}));
		const wc = createWindowChrome(f.win);
		expect(wc?.chrome).toEqual(CHROME);
		expect(f.invoke).toHaveBeenCalledWith("host_window", { action: "state" });
		await flush();
		expect(wc?.getFlags()).toEqual({
			maximized: true,
			fullscreen: false,
			focused: false,
		});
	});
});

describe("parseChrome", () => {
	it("未知按钮丢掉、重复去掉、缺省 inset 为 0；平台或控件取值不认识 → null", () => {
		expect(
			parseChrome({
				platform: "linux",
				controls: "custom-left",
				buttons: ["close", "spacer", "close", "minimize"],
			}),
		).toEqual({
			platform: "linux",
			controls: "custom-left",
			buttons: ["close", "minimize"],
			inset: { top: 0, left: 0, right: 0 },
		});
		expect(
			parseChrome({
				platform: "macos",
				controls: "native-left",
				inset: { top: 48, left: 84 },
			})?.inset,
		).toEqual({ top: 48, left: 84, right: 0 });
		expect(parseChrome({ platform: "linux", controls: "center" })).toBeNull();
		expect(parseChrome({ platform: "ios", controls: "none" })).toBeNull();
		expect(parseChrome("custom-right")).toBeNull();
	});
});

describe("窗口状态与动作", () => {
	it("`window` 事件更新状态并通知订阅者；值没变不通知", async () => {
		const f = fakeWindow(desktop(CHROME));
		const wc = createWindowChrome(f.win);
		await flush();
		const cb = vi.fn();
		wc?.subscribe(cb);
		f.emit({
			type: "window",
			maximized: true,
			fullscreen: false,
			focused: true,
		});
		expect(wc?.getFlags().maximized).toBe(true);
		expect(cb).toHaveBeenCalledTimes(1);
		f.emit({
			type: "window",
			maximized: true,
			fullscreen: false,
			focused: true,
		});
		f.emit({ type: "visibility", visible: false });
		expect(cb).toHaveBeenCalledTimes(1);
		f.emit({ type: "window", fullscreen: true });
		expect(wc?.getFlags()).toEqual({
			maximized: true,
			fullscreen: true,
			focused: true,
		});
	});

	it("动作经 host_window 发给壳，应答的状态立即生效；拒绝只记日志", async () => {
		let maximized = false;
		const f = fakeWindow(desktop(CHROME), (args) => {
			if (args?.action === "close") throw new Error("denied");
			if (args?.action === "toggleMaximize") maximized = !maximized;
			return { maximized, fullscreen: false, focused: true };
		});
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const wc = createWindowChrome(f.win);
		wc?.act("toggleMaximize");
		await flush();
		expect(f.invoke).toHaveBeenLastCalledWith("host_window", {
			action: "toggleMaximize",
		});
		expect(wc?.getFlags().maximized).toBe(true);
		wc?.act("minimize");
		wc?.act("close");
		await flush();
		expect(f.invoke).toHaveBeenCalledWith("host_window", {
			action: "minimize",
		});
		expect(warn).toHaveBeenCalledWith(
			"[window] close failed",
			expect.any(Error),
		);
		warn.mockRestore();
	});
});

describe("拖动区", () => {
	function dom(doc: Document) {
		doc.body.innerHTML = `
			<header data-window-drag="">
				<span id="blank">　</span>
				<button id="btn"><svg id="icon"></svg></button>
				<div role="tab" id="tab">标签</div>
				<div data-window-drag="false"><span id="off">不拖</span></div>
				<input id="field" />
			</header>
			<p id="outside">正文</p>`;
		return (id: string) => doc.getElementById(id) as HTMLElement;
	}

	const fire = (
		el: HTMLElement,
		type: "mousedown" | "mousemove" | "mouseup" | "dblclick",
		init: MouseEventInit = {},
	) => {
		const e = new MouseEvent(type, {
			bubbles: true,
			cancelable: true,
			button: 0,
			...init,
		});
		el.dispatchEvent(e);
		return e;
	};

	it("按住左键移动超过 3px 才开始拖动，单纯点击不拖；双击切换最大化", () => {
		const f = fakeWindow(desktop({ ...CHROME, platform: "linux" }));
		createWindowChrome(f.win);
		const el = dom(f.doc);
		f.invoke.mockClear();
		const down = fire(el("blank"), "mousedown", { clientX: 10, clientY: 10 });
		expect(down.defaultPrevented).toBe(true);
		fire(el("blank"), "mousemove", { clientX: 11, clientY: 12, buttons: 1 });
		expect(f.invoke).not.toHaveBeenCalled();
		fire(el("blank"), "mousemove", { clientX: 14, clientY: 12, buttons: 1 });
		expect(f.invoke).toHaveBeenCalledTimes(1);
		expect(f.invoke).toHaveBeenLastCalledWith("host_window", {
			action: "startDrag",
		});
		// 一次按下只拖一次
		fire(el("blank"), "mousemove", { clientX: 40, clientY: 40, buttons: 1 });
		expect(f.invoke).toHaveBeenCalledTimes(1);
		// 点击（按下、松开、没移动）不拖；松开后再移动也不拖
		f.invoke.mockClear();
		fire(el("blank"), "mousedown", { clientX: 10, clientY: 10 });
		fire(el("blank"), "mouseup", { clientX: 10, clientY: 10 });
		fire(el("blank"), "mousemove", { clientX: 30, clientY: 30, buttons: 1 });
		fire(el("blank"), "dblclick", { detail: 2 });
		expect(f.invoke.mock.calls).toEqual([
			["host_window", { action: "toggleMaximize" }],
		]);
	});

	it("按钮、交互 role、输入框、显式关闭与区外都不拖也不切换最大化；右键不拖", () => {
		const f = fakeWindow(desktop({ ...CHROME, platform: "macos" }));
		createWindowChrome(f.win);
		const el = dom(f.doc);
		f.invoke.mockClear();
		for (const id of ["btn", "icon", "tab", "off", "field", "outside"]) {
			expect(fire(el(id), "mousedown").defaultPrevented).toBe(false);
			fire(el(id), "mousemove", { clientX: 50, clientY: 50, buttons: 1 });
			fire(el(id), "dblclick", { detail: 2 });
		}
		fire(el("blank"), "mousedown", { button: 2 });
		fire(el("blank"), "mousemove", { clientX: 50, clientY: 50, buttons: 2 });
		expect(f.invoke).not.toHaveBeenCalled();
	});

	it("inDragRegion：路径上先遇到可交互元素就不拖", () => {
		const doc = document.implementation.createHTMLDocument("t");
		doc.body.innerHTML = `<div data-window-drag=""><a href="#" id="a"><b id="b">x</b></a><i id="i" tabindex="0"></i><i id="j" tabindex="-1"></i></div>`;
		const path = (id: string) => {
			const out: Element[] = [];
			for (
				let n: Element | null = doc.getElementById(id);
				n;
				n = n.parentElement
			)
				out.push(n);
			return out;
		};
		expect(inDragRegion(path("b"))).toBe(false);
		expect(inDragRegion(path("i"))).toBe(false);
		expect(inDragRegion(path("j"))).toBe(true);
		expect(inDragRegion([])).toBe(false);
	});
});
