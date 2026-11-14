/** 宿主适配：web / tauri 判定、前台判定、壳事件、invoke 的参数与容错（假 window）。 */
import { describe, expect, it, type Mock, vi } from "vitest";
import { createHost, HOST_EVENT, type Invoke } from "./host";

interface FakeWin {
	win: Window;
	invoke: Mock;
	setTab(v: DocumentVisibilityState): void;
	emit(detail: unknown): void;
}

function fakeWindow(
	opts: {
		tauri?: boolean;
		meta?: boolean;
		internals?: boolean;
		invoke?: (cmd: string, args?: Record<string, unknown>) => unknown;
	} = {},
): FakeWin {
	const target = new EventTarget();
	const doc = new EventTarget() as EventTarget & {
		visibilityState: DocumentVisibilityState;
	};
	doc.visibilityState = "visible";
	const invoke = vi.fn(async (cmd: string, args?: Record<string, unknown>) =>
		opts.invoke ? opts.invoke(cmd, args) : undefined,
	);
	const win = Object.assign(target, {
		document: doc,
		...(opts.meta !== false && opts.tauri
			? {
					__ORYN_HOST__: {
						kind: "tauri",
						platform: "linux",
						version: "0.1.0",
						bridge: 1,
					},
				}
			: {}),
		...(opts.tauri && !opts.internals
			? { __TAURI__: { core: { invoke: invoke as unknown as Invoke } } }
			: {}),
		...(opts.tauri && opts.internals
			? { __TAURI_INTERNALS__: { invoke: invoke as unknown as Invoke } }
			: {}),
	}) as unknown as Window;
	return {
		win,
		invoke,
		setTab(v) {
			doc.visibilityState = v;
			doc.dispatchEvent(new Event("visibilitychange"));
		},
		emit(detail) {
			target.dispatchEvent(new CustomEvent(HOST_EVENT, { detail }));
		},
	};
}

describe("判定", () => {
	it("没有 __ORYN_HOST__ 或取不到 invoke → web；两者都有 → tauri", () => {
		expect(createHost(fakeWindow().win).kind).toBe("web");
		expect(createHost(fakeWindow({ tauri: true, meta: false }).win).kind).toBe(
			"web",
		);
		const noInvoke = fakeWindow();
		(noInvoke.win as Window).__ORYN_HOST__ = { kind: "tauri" };
		expect(createHost(noInvoke.win).kind).toBe("web");
		const h = createHost(fakeWindow({ tauri: true }).win);
		expect(h.kind).toBe("tauri");
		expect(h.platform).toBe("linux");
		expect(h.desktop).not.toBeNull();
	});

	it("取不到 __TAURI__ 时退而用 __TAURI_INTERNALS__.invoke", () => {
		const f = fakeWindow({ tauri: true, internals: true });
		const h = createHost(f.win);
		expect(h.kind).toBe("tauri");
		h.setLanguage("en");
		expect(f.invoke).toHaveBeenCalledWith("host_set_language", { lang: "en" });
	});

	it("web：前台 = 标签页可见，其余空操作，ready 恒为 null", async () => {
		const f = fakeWindow();
		const h = createHost(f.win);
		const cb = vi.fn();
		const off = h.onForegroundChange(cb);
		expect(h.isForeground()).toBe(true);
		f.setTab("hidden");
		expect(h.isForeground()).toBe(false);
		expect(cb).toHaveBeenCalledTimes(1);
		off();
		f.setTab("visible");
		expect(cb).toHaveBeenCalledTimes(1);
		expect(await h.ready("zh-CN", "/")).toBeNull();
		expect(h.desktop).toBeNull();
	});
});

describe("tauri", () => {
	it("前台 = 标签页可见 且 壳报告窗口可见（四种组合）", () => {
		const f = fakeWindow({ tauri: true });
		const h = createHost(f.win);
		const cb = vi.fn();
		h.onForegroundChange(cb);
		const cases: [DocumentVisibilityState, boolean, boolean][] = [
			["visible", true, true],
			["visible", false, false],
			["hidden", true, false],
			["hidden", false, false],
		];
		for (const [tab, win, want] of cases) {
			f.setTab(tab);
			f.emit({ type: "visibility", visible: win });
			expect(h.isForeground()).toBe(want);
		}
		// 窗口可见性每次变化都通知（标签页变化另计）
		expect(cb.mock.calls.length).toBeGreaterThanOrEqual(4);
	});

	it("navigate / update 事件交给回调；畸形事件被忽略", () => {
		const f = fakeWindow({ tauri: true });
		const h = createHost(f.win);
		const nav = vi.fn();
		const upd = vi.fn();
		const offNav = h.onNavigate(nav);
		h.onUpdate(upd);
		f.emit({ type: "navigate", path: "/t/T-1" });
		f.emit({ type: "update", state: "ready", version: "0.2.0" });
		f.emit(null);
		f.emit({ type: "navigate", path: 3 });
		f.emit({ type: "launcher", state: { step: "connect" } });
		expect(nav.mock.calls).toEqual([["/t/T-1"]]);
		expect(upd).toHaveBeenCalledTimes(1);
		offNav();
		f.emit({ type: "navigate", path: "/x" });
		expect(nav).toHaveBeenCalledTimes(1);
	});

	it("ready：带 bridge / lang / path 握手，返回待开路径并采用壳的可见性", async () => {
		const f = fakeWindow({
			tauri: true,
			invoke: (cmd) =>
				cmd === "host_ready"
					? { bridge: 2, visible: false, pendingPath: "/t/T-9" }
					: undefined,
		});
		const h = createHost(f.win);
		expect(await h.ready("en", "/settings/devices")).toBe("/t/T-9");
		expect(f.invoke).toHaveBeenCalledWith("host_ready", {
			bridge: 2,
			lang: "en",
			path: "/settings/devices",
		});
		expect(h.isForeground()).toBe(false);
	});

	it("命令参数：notify / setStatus / reauth / changeServer / setSettings", async () => {
		const f = fakeWindow({ tauri: true });
		const h = createHost(f.win);
		const n = { id: "ntf_1", title: "t", body: "b", path: "/t/T-1" };
		h.notify(n);
		const status = { awaiting: 1, unread: 0, items: [] };
		h.setStatus(status);
		h.reauth("/t/T-1?x=1");
		h.desktop?.changeServer();
		await h.desktop?.setSettings({ closeToTray: false });
		expect(f.invoke.mock.calls).toEqual([
			["host_notify", { notification: n }],
			["host_set_status", { status }],
			["host_reauth", { path: "/t/T-1?x=1" }],
			["host_change_server", undefined],
			["host_set_settings", { patch: { closeToTray: false } }],
		]);
	});

	it("invoke 拒绝不抛出：发出即忘的命令只 warn，ready 退回 null", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const f = fakeWindow({
			tauri: true,
			invoke: () => {
				throw new Error("ipc down");
			},
		});
		const h = createHost(f.win);
		expect(() =>
			h.notify({ id: "1", title: "t", body: "", path: "/" }),
		).not.toThrow();
		expect(await h.ready("zh-CN", "/")).toBeNull();
		h.reauth("/");
		await vi.waitFor(() => expect(warn).toHaveBeenCalledTimes(3));
		// 有返回值的桌面命令把错误交给调用方
		await expect(h.desktop?.info()).rejects.toThrow("ipc down");
		warn.mockRestore();
	});

	it("reauth 每个页面只发一次（壳随后会整页导航）", () => {
		const f = fakeWindow({ tauri: true });
		const h = createHost(f.win);
		h.reauth("/t/T-1");
		h.reauth("/t/T-1");
		h.reauth("/");
		expect(f.invoke.mock.calls).toEqual([["host_reauth", { path: "/t/T-1" }]]);
	});

	it("checkUpdate：壳返回 null 时为 null", async () => {
		const f = fakeWindow({ tauri: true, invoke: () => null });
		expect(await createHost(f.win).desktop?.checkUpdate()).toBeNull();
	});
});

describe("移动壳（M7）", () => {
	/** android / bridge 2 的假 window：带真实 DOM 文档（点击能冒泡到 document）与 location。 */
	function mobileWindow(
		opts: { platform?: string; bridge?: number; invoke?: () => unknown } = {},
	) {
		const target = new EventTarget();
		const doc = document.implementation.createHTMLDocument("t");
		const invoke = vi.fn(
			async (_cmd: string, _args?: Record<string, unknown>) =>
				opts.invoke ? opts.invoke() : undefined,
		);
		const win = Object.assign(target, {
			document: doc,
			location: {
				href: "http://localhost:7600/t/T-1",
				origin: "http://localhost:7600",
			},
			open: vi.fn(),
			__ORYN_HOST__: {
				kind: "tauri",
				platform: opts.platform ?? "android",
				version: "0.1.0",
				bridge: opts.bridge ?? 2,
			},
			__TAURI__: { core: { invoke: invoke as unknown as Invoke } },
		}) as unknown as Window;
		const emit = (detail: unknown) =>
			target.dispatchEvent(new CustomEvent(HOST_EVENT, { detail }));
		/** 在文档里放一个链接并点击；返回事件是否被阻止。 */
		const click = (attrs: Record<string, string>) => {
			const a = doc.createElement("a");
			for (const [k, v] of Object.entries(attrs)) a.setAttribute(k, v);
			const span = doc.createElement("span");
			a.append(span);
			doc.body.append(a);
			const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
			span.dispatchEvent(ev);
			a.remove();
			return ev.defaultPrevented;
		};
		return { win, invoke, emit, click };
	}

	it("android + bridge 2 → mobile 非空、desktop 为 null；bridge 1 或桌面平台 → 反之", () => {
		const m = createHost(mobileWindow().win);
		expect(m.mobile).not.toBeNull();
		expect(m.desktop).toBeNull();
		expect(m.platform).toBe("android");
		const ios = createHost(mobileWindow({ platform: "ios" }).win);
		expect(ios.mobile).not.toBeNull();
		const old = createHost(mobileWindow({ bridge: 1 }).win);
		expect(old.mobile).toBeNull();
		expect(old.desktop).not.toBeNull();
		const desk = createHost(fakeWindow({ tauri: true }).win);
		expect(desk.mobile).toBeNull();
		expect(desk.desktop).not.toBeNull();
		expect(createHost(fakeWindow().win).mobile).toBeNull();
	});

	it("_blank 与跨源链接改经 host_open_external；同源普通链接、download、锚点不拦", () => {
		const f = mobileWindow();
		createHost(f.win);
		expect(f.click({ href: "/docs", target: "_blank" })).toBe(true);
		expect(f.click({ href: "https://example.com/a?b=1" })).toBe(true);
		expect(f.click({ href: "/t/T-2" })).toBe(false);
		expect(f.click({ href: "/api/v1/attachments/x", download: "" })).toBe(
			false,
		);
		expect(f.click({ href: "#top" })).toBe(false);
		expect(f.click({ href: "mailto:a@b.c" })).toBe(false);
		expect(f.invoke.mock.calls).toEqual([
			["host_open_external", { url: "http://localhost:7600/docs" }],
			["host_open_external", { url: "https://example.com/a?b=1" }],
		]);
	});

	it("window.open 改道到系统浏览器并返回 null", () => {
		const f = mobileWindow();
		createHost(f.win);
		expect(f.win.open("https://example.com/x", "_blank")).toBeNull();
		expect(f.win.open("/rel")).toBeNull();
		expect(f.invoke.mock.calls).toEqual([
			["host_open_external", { url: "https://example.com/x" }],
			["host_open_external", { url: "http://localhost:7600/rel" }],
		]);
	});

	it("share / mobile / insets 事件到回调；畸形与桌面壳上的同名事件被忽略", () => {
		const f = mobileWindow();
		const h = createHost(f.win);
		const share = vi.fn();
		const mobile = vi.fn();
		const insets = vi.fn();
		h.onShare(share);
		h.onMobile(mobile);
		h.onInsets(insets);
		const att = { id: "att_1", name: "a.png", size: 3, mime: "image/png" };
		f.emit({
			type: "share",
			share: {
				id: "s1",
				text: "hi",
				attachments: [att, { bad: 1 }],
				failed: [{ name: "b.bin", reason: "too_large" }, 3],
			},
		});
		f.emit({ type: "share", share: { text: "no id" } });
		f.emit({ type: "mobile" });
		f.emit({
			type: "insets",
			cornerRadii: { tl: 40, tr: 40, bl: 40, br: 40 },
			insets: { top: 24, right: 0, bottom: 16, left: 0 },
		});
		f.emit({ type: "insets" });
		expect(share.mock.calls).toEqual([
			[
				{
					id: "s1",
					text: "hi",
					attachments: [att],
					failed: [{ name: "b.bin", reason: "too_large" }],
				},
			],
		]);
		expect(mobile).toHaveBeenCalledTimes(1);
		expect(insets.mock.calls).toEqual([
			[
				{
					cornerRadii: { tl: 40, tr: 40, bl: 40, br: 40 },
					insets: { top: 24, right: 0, bottom: 16, left: 0 },
				},
			],
		]);

		const d = fakeWindow({ tauri: true });
		const desk = createHost(d.win);
		const s2 = vi.fn();
		desk.onShare(s2);
		d.emit({ type: "share", share: { id: "x", attachments: [], failed: [] } });
		expect(s2).not.toHaveBeenCalled();
	});

	it("命令参数：info / enablePush / disablePush / shareAck / setChrome / openNotificationSettings / changeServer", async () => {
		const f = mobileWindow();
		const m = createHost(f.win).mobile;
		await m?.info();
		await m?.enablePush();
		await m?.disablePush();
		m?.shareAck("s1");
		m?.setChrome({ dark: true, color: "#101010" });
		m?.openNotificationSettings();
		m?.changeServer();
		expect(f.invoke.mock.calls).toEqual([
			["host_mobile_info"],
			["host_push_enable"],
			["host_push_disable"],
			["host_share_ack", { id: "s1" }],
			["host_set_chrome", { dark: true, color: "#101010" }],
			["host_open_notification_settings", undefined],
			["host_change_server", undefined],
		]);
	});
});
