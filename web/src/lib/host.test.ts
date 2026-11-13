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
					? { bridge: 1, visible: false, pendingPath: "/t/T-9" }
					: undefined,
		});
		const h = createHost(f.win);
		expect(await h.ready("en", "/settings/devices")).toBe("/t/T-9");
		expect(f.invoke).toHaveBeenCalledWith("host_ready", {
			bridge: 1,
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
