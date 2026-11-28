/** 宿主适配：web / tauri 判定、前台判定、壳事件、invoke 的参数与容错（假 window）。 */
import { describe, expect, it, type Mock, vi } from "vitest";
import {
	BRIDGE,
	createHost,
	HOST_EVENT,
	type Invoke,
	type NativeHeaderMenu,
	type NativeHeaderShow,
	type NativeHeaderSpec,
	type NativeTabBarSpec,
	parseHeaderEvent,
} from "./host";

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
		expect(await h.ready("en", "/settings/account")).toBe("/t/T-9");
		expect(f.invoke).toHaveBeenCalledWith("host_ready", {
			bridge: BRIDGE,
			lang: "en",
			path: "/settings/account",
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
		const click = (
			attrs: Record<string, string>,
			evInit: MouseEventInit & { type?: "click" | "auxclick" } = {},
		) => {
			const a = doc.createElement("a");
			for (const [k, v] of Object.entries(attrs)) a.setAttribute(k, v);
			const span = doc.createElement("span");
			a.append(span);
			doc.body.append(a);
			const { type = "click", ...init } = evInit;
			const ev = new MouseEvent(type, {
				bubbles: true,
				cancelable: true,
				...init,
			});
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

	it("_blank 与跨源链接改经 host_open_external；同源普通链接、download、锚点不拦；旧壳不拦 mailto", () => {
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

	it("桌面壳 bridge ≥ 11 统一接管 _blank / 跨源 / 修饰键 / 中键 / mailto / window.open", () => {
		const f = mobileWindow({ platform: "macos", bridge: 11 });
		const h = createHost(f.win);
		expect(h.desktop).not.toBeNull();
		expect(h.mobile).toBeNull();
		expect(f.click({ href: "https://example.com/x", target: "_blank" })).toBe(
			true,
		);
		expect(f.click({ href: "https://example.com/plain" })).toBe(true);
		expect(f.click({ href: "/t/T-9" }, { metaKey: true })).toBe(true);
		expect(f.click({ href: "/t/T-8" }, { type: "auxclick", button: 1 })).toBe(
			true,
		);
		expect(f.click({ href: "mailto:hi@example.com" })).toBe(true);
		expect(f.click({ href: "/t/T-2" })).toBe(false);
		expect(
			f.win.open("https://example.com/w", "_blank", "noopener"),
		).toBeNull();
		expect(f.invoke.mock.calls).toEqual([
			["host_open_external", { url: "https://example.com/x" }],
			["host_open_external", { url: "https://example.com/plain" }],
			["host_open_external", { url: "http://localhost:7600/t/T-9" }],
			["host_open_external", { url: "http://localhost:7600/t/T-8" }],
			["host_open_external", { url: "mailto:hi@example.com" }],
			["host_open_external", { url: "https://example.com/w" }],
		]);
	});

	it("旧桌面壳（bridge ≤ 10）将无 _blank 的跨源链接改由 window.open 交旧壳 on_new_window", () => {
		const f = mobileWindow({ platform: "linux", bridge: 10 });
		createHost(f.win);
		// 带 _blank 交由旧壳原生 on_new_window，不拦也不调未授权的 host_open_external
		expect(f.click({ href: "https://example.com/a", target: "_blank" })).toBe(
			false,
		);
		// 无 _blank 的跨源链接拦截并转为 window.open
		expect(f.click({ href: "https://example.com/plain" })).toBe(true);
		expect(f.win.open).toHaveBeenCalledWith(
			"https://example.com/plain",
			"_blank",
			"noopener",
		);
		expect(f.invoke).not.toHaveBeenCalled();
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

	it("rail 事件：竖排时给出边、矩形与安全区；不竖排或矩形为空时回调 null，桌面壳忽略", () => {
		const f = mobileWindow({ platform: "ios", bridge: 12 });
		const rail = vi.fn();
		createHost(f.win).onRail(rail);
		f.emit({
			type: "rail",
			side: "right",
			frame: { x: 402, y: 54, width: 64, height: 624 },
			safeArea: { top: 0, right: 64, bottom: 21 },
		});
		f.emit({
			type: "rail",
			safeArea: { top: 24, right: 0, bottom: 21, left: 0 },
		});
		f.emit({
			type: "rail",
			side: "left",
			frame: { x: 0, y: 0, width: 0, height: 600 },
		});
		expect(rail.mock.calls).toEqual([
			[
				{
					side: "right",
					frame: { x: 402, y: 54, width: 64, height: 624 },
					safeArea: { top: 0, right: 64, bottom: 21, left: 0 },
				},
			],
			[null],
			[null],
		]);

		const d = fakeWindow({ tauri: true });
		const r2 = vi.fn();
		createHost(d.win).onRail(r2);
		d.emit({ type: "rail", side: "right", frame: { width: 64, height: 9 } });
		expect(r2).not.toHaveBeenCalled();
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

	it("enablePush(transport)：只在壳 bridge ≥ 9 时带 transport；旧壳一律不带", async () => {
		const f = mobileWindow({ bridge: 9 });
		const m = createHost(f.win).mobile;
		expect(m?.pushTransport).toBe(true);
		await m?.enablePush("unifiedpush");
		await m?.enablePush("default");
		await m?.enablePush();
		const old = mobileWindow({ bridge: 8 });
		const o = createHost(old.win).mobile;
		expect(o?.pushTransport).toBe(false);
		await o?.enablePush("unifiedpush");
		expect(f.invoke.mock.calls).toEqual([
			["host_push_enable", { transport: "unifiedpush" }],
			["host_push_enable", { transport: "default" }],
			["host_push_enable"],
		]);
		expect(old.invoke.mock.calls).toEqual([["host_push_enable"]]);
	});

	describe("下沉侧栏与原生底栏（bridge 8）", () => {
		const spec: NativeTabBarSpec = {
			items: [{ id: "threads", label: "线程", icon: { symbol: "folder" } }],
			selected: "threads",
			frame: { x: 12, y: 758, width: 296, height: 52 },
			dark: false,
			accent: null,
		};

		it("ios + bridge 8 → sink / tabBar 非空；sink.set / sink.show / set / cover 的参数形状", async () => {
			const f = mobileWindow({ platform: "ios", bridge: 8 });
			const m = createHost(f.win).mobile;
			expect(m?.sink).toBeTruthy();
			expect(m?.tabBar).toBeTruthy();
			await m?.sink?.set(true);
			await m?.sink?.show(true, true);
			await m?.sink?.show(false, false);
			await m?.tabBar?.set(spec);
			await m?.tabBar?.set(null);
			await m?.tabBar?.cover(true);
			await m?.sink?.set(false);
			expect(f.invoke.mock.calls).toEqual([
				["host_sink_set", { on: true }],
				["host_sink_show", { open: true, animated: true }],
				["host_sink_show", { open: false, animated: false }],
				["host_tab_bar_set", { spec }],
				["host_tab_bar_set", { spec: null }],
				["host_tab_bar_cover", { covered: true }],
				["host_sink_set", { on: false }],
			]);
		});

		it("invoke 拒绝时 sink.set / set / cover 的 Promise 拒绝（调用方据此退回网页手势 / 底栏）", async () => {
			const f = mobileWindow({
				platform: "ios",
				bridge: 8,
				invoke: () => {
					throw "unsupported";
				},
			});
			const m = createHost(f.win).mobile;
			await expect(m?.sink?.set(true)).rejects.toBe("unsupported");
			await expect(m?.tabBar?.set(spec)).rejects.toBe("unsupported");
			await expect(m?.tabBar?.cover(false)).rejects.toBe("unsupported");
		});

		it("ios + bridge 7 及更早、android + bridge 8 → sink / tabBar / header 皆为 null", () => {
			for (const bridge of [3, 4, 5, 6, 7]) {
				const old = createHost(mobileWindow({ platform: "ios", bridge }).win);
				expect(old.mobile).not.toBeNull();
				expect(old.mobile?.sink).toBeNull();
				expect(old.mobile?.tabBar).toBeNull();
				expect(old.mobile?.header).toBeNull();
			}
			const android = createHost(mobileWindow({ bridge: 8 }).win);
			expect(android.mobile).not.toBeNull();
			expect(android.mobile?.sink).toBeNull();
			expect(android.mobile?.tabBar).toBeNull();
			expect(android.mobile?.header).toBeNull();
		});

		it("tabbar 事件到 onTabBar；id 不是字符串或桌面壳上忽略；可退订", () => {
			const f = mobileWindow({ platform: "ios", bridge: 8 });
			const h = createHost(f.win);
			const cb = vi.fn();
			const off = h.onTabBar(cb);
			f.emit({ type: "tabbar", id: "projects" });
			f.emit({ type: "tabbar", id: 3 });
			f.emit({ type: "tabbar" });
			expect(cb.mock.calls).toEqual([["projects"]]);
			off();
			f.emit({ type: "tabbar", id: "more" });
			expect(cb).toHaveBeenCalledTimes(1);

			const d = fakeWindow({ tauri: true });
			const desk = createHost(d.win);
			const c2 = vi.fn();
			desk.onTabBar(c2);
			d.emit({ type: "tabbar", id: "projects" });
			expect(c2).not.toHaveBeenCalled();
		});

		it("web 宿主：onTabBar 返回空函数", () => {
			const h = createHost(fakeWindow().win);
			const off = h.onTabBar(vi.fn());
			expect(typeof off).toBe("function");
			expect(() => off()).not.toThrow();
		});
	});

	describe("原生页头（bridge 8）", () => {
		const spec = { key: "T-1" } as NativeHeaderSpec;
		const show: NativeHeaderShow = { alpha: 1, ms: 120 };
		const menu: NativeHeaderMenu = { open: "title", page: "env" };

		it("本页宿主协议版本为 12（桌面端开机自启与启动时隐藏）", () => {
			expect(BRIDGE).toBe(12);
		});

		it("syncAssistantAvatar 在 bridge 9 旧壳上静默跳过，bridge 10 正常下发 host_sync_assistant_avatar", () => {
			const old = mobileWindow({ platform: "ios", bridge: 9 });
			createHost(old.win).mobile?.syncAssistantAvatar({
				image: "data:image/webp;base64,AAA",
			});
			expect(old.invoke).not.toHaveBeenCalled();

			const cur = mobileWindow({ platform: "ios", bridge: 10 });
			createHost(cur.win).mobile?.syncAssistantAvatar({
				icon: "🦊",
				image: "data:image/webp;base64,AAA",
			});
			createHost(cur.win).mobile?.syncAssistantAvatar({});
			expect(cur.invoke.mock.calls).toEqual([
				[
					"host_sync_assistant_avatar",
					{ icon: "🦊", image: "data:image/webp;base64,AAA" },
				],
				["host_sync_assistant_avatar", { icon: null, image: null }],
			]);
		});

		it("ios + bridge 8 → header 非空；set / show / menu 的参数形状（show 只有 alpha 与 ms）", async () => {
			const f = mobileWindow({ platform: "ios", bridge: 8 });
			const hd = createHost(f.win).mobile?.header;
			expect(hd).toBeTruthy();
			await hd?.set(spec);
			await hd?.set(null);
			await hd?.show(show);
			await hd?.show({ alpha: 0, ms: 0 });
			await hd?.menu(menu);
			await hd?.menu({ open: null });
			expect(f.invoke.mock.calls).toEqual([
				["host_header_set", { spec }],
				["host_header_set", { spec: null }],
				["host_header_show", { show: { alpha: 1, ms: 120 } }],
				["host_header_show", { show: { alpha: 0, ms: 0 } }],
				["host_header_menu", { menu }],
				["host_header_menu", { menu: { open: null } }],
			]);
		});

		it("invoke 拒绝时 set / show / menu 的 Promise 拒绝（调用方据此退回网页页头）", async () => {
			const f = mobileWindow({
				platform: "ios",
				bridge: 8,
				invoke: () => {
					throw "unsupported";
				},
			});
			const hd = createHost(f.win).mobile?.header;
			await expect(hd?.set(spec)).rejects.toBe("unsupported");
			await expect(hd?.show(show)).rejects.toBe("unsupported");
			await expect(hd?.menu(menu)).rejects.toBe("unsupported");
		});

		it("可替换图标只给 ios + bridge 6：get / set 的命令与参数；拒绝原样交给调用方", async () => {
			const f = mobileWindow({ platform: "ios", bridge: 6 });
			const icon = createHost(f.win).mobile?.appIcon;
			expect(icon).toBeTruthy();
			await icon?.get();
			await icon?.set("accent");
			expect(f.invoke.mock.calls).toEqual([
				["host_app_icon_get"],
				["host_app_icon_set", { id: "accent" }],
			]);

			expect(
				createHost(mobileWindow({ platform: "ios", bridge: 5 }).win).mobile
					?.appIcon,
			).toBeNull();
			expect(
				createHost(mobileWindow({ bridge: 6 }).win).mobile?.appIcon,
			).toBeNull();

			const denied = createHost(
				mobileWindow({
					platform: "ios",
					bridge: 6,
					invoke: () => {
						throw "unsupported";
					},
				}).win,
			).mobile?.appIcon;
			await expect(denied?.set("accent")).rejects.toBe("unsupported");
		});

		it("三种 header 事件到 onHeader；key 缺失、select 缺 id / 菜单种类、桌面壳上忽略；可退订", () => {
			const f = mobileWindow({ platform: "ios", bridge: 5 });
			const h = createHost(f.win);
			const cb = vi.fn();
			const off = h.onHeader(cb);
			f.emit({ type: "header", op: "lead", key: "T-1" });
			f.emit({ type: "header", op: "menu", key: "T-1", menu: "title" });
			f.emit({ type: "header", op: "menu", key: "T-1", menu: null });
			f.emit({
				type: "header",
				op: "select",
				key: "T-1",
				menu: "trail",
				id: "terminal",
			});
			f.emit({ type: "header", op: "lead" });
			f.emit({ type: "header", op: "select", key: "T-1", menu: "title" });
			f.emit({ type: "header", op: "select", key: "T-1", id: "x" });
			f.emit({ type: "header", op: "nope", key: "T-1" });
			expect(cb.mock.calls).toEqual([
				[{ op: "lead", key: "T-1" }],
				[{ op: "menu", key: "T-1", menu: "title" }],
				[{ op: "menu", key: "T-1", menu: null }],
				[{ op: "select", key: "T-1", menu: "trail", id: "terminal" }],
			]);
			off();
			f.emit({ type: "header", op: "lead", key: "T-1" });
			expect(cb).toHaveBeenCalledTimes(4);

			const d = fakeWindow({ tauri: true });
			const desk = createHost(d.win);
			const c2 = vi.fn();
			desk.onHeader(c2);
			d.emit({ type: "header", op: "lead", key: "T-1" });
			expect(c2).not.toHaveBeenCalled();
		});

		it("parseHeaderEvent：menu 不是 title / trail（含缺省）一律为 null；非对象为 null", () => {
			expect(parseHeaderEvent({ op: "menu", key: "K", menu: "x" })).toEqual({
				op: "menu",
				key: "K",
				menu: null,
			});
			expect(parseHeaderEvent({ op: "menu", key: "K" })).toEqual({
				op: "menu",
				key: "K",
				menu: null,
			});
			expect(parseHeaderEvent({ op: "lead", key: 3 })).toBeNull();
			expect(parseHeaderEvent(null)).toBeNull();
			expect(parseHeaderEvent("lead")).toBeNull();
		});

		it("web 宿主：onHeader 返回空函数", () => {
			const h = createHost(fakeWindow().win);
			const off = h.onHeader(vi.fn());
			expect(typeof off).toBe("function");
			expect(() => off()).not.toThrow();
		});
	});
});
