/**
 * `public/sw.js`（手写、不经打包）在假的 `self` 上加载，逐个分支驱动 push / notificationclick。
 * 假窗口按是否装着应用接收器（`lib/push.ts`）应答 `oryn.probe` / `oryn.navigate`；
 * 没有接收器的窗口（匿名分享页、登录页）不应答。
 */
import { describe, expect, it, vi } from "vitest";
import SOURCE from "../../public/sw.js?raw";

interface FakeWindow {
	url: string;
	focused: boolean;
	focus: ReturnType<typeof vi.fn>;
	postMessage: ReturnType<typeof vi.fn>;
	/** 收到的消息类型 */
	got: string[];
}

type Handler = (event: Record<string, unknown>) => void;

function loadWorker({
	windows = [] as FakeWindow[],
	userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36",
} = {}) {
	const handlers = new Map<string, Handler>();
	const self = {
		navigator: { userAgent },
		addEventListener: (type: string, fn: Handler) => handlers.set(type, fn),
		skipWaiting: vi.fn(),
		clients: {
			claim: vi.fn(async () => {}),
			matchAll: vi.fn(async () => windows),
			openWindow: vi.fn(async () => null),
		},
		registration: { showNotification: vi.fn(async () => {}) },
	};
	new Function("self", SOURCE)(self);
	/** 派发事件并等 `waitUntil` 里的 promise 落地。 */
	const dispatch = async (type: string, event: Record<string, unknown>) => {
		let pending: Promise<unknown> = Promise.resolve();
		handlers.get(type)?.({
			...event,
			waitUntil: (p: Promise<unknown>) => {
				pending = p;
			},
		});
		await pending;
	};
	return { self, dispatch };
}

/**
 * `app`：装着应用接收器，探询时应答 `{app: true, toasts}`；`ackNavigate=false` 模拟页面收到跳转却没应答（已卸载）。
 * 不给 `app` = 匿名分享页 / 登录页：什么都不应答。
 */
function win(
	focused: boolean,
	{
		url = "https://oryn.test/",
		app,
		ackNavigate = true,
	}: { url?: string; app?: { toasts: boolean }; ackNavigate?: boolean } = {},
): FakeWindow {
	const got: string[] = [];
	return {
		url,
		focused,
		got,
		focus: vi.fn(async () => {}),
		postMessage: vi.fn((msg: { type: string }, ports: MessagePort[] = []) => {
			got.push(msg.type);
			if (!app) return;
			if (msg.type === "oryn.probe")
				ports[0]?.postMessage({ app: true, toasts: app.toasts });
			if (msg.type === "oryn.navigate" && ackNavigate)
				ports[0]?.postMessage({ ok: true });
		}),
	};
}

const appWin = (focused: boolean, toasts = true) =>
	win(focused, { url: "https://oryn.test/t/T-1", app: { toasts } });
const shareWin = (focused: boolean) =>
	win(focused, { url: "https://oryn.test/s/0123456789abcdef0123456789abcdef" });

const PAYLOAD = {
	v: 1,
	id: "ntf_1",
	kind: "done",
	title: "t",
	body: "b",
	url: "/t/x",
	tag: "oryn-x",
};
const push = { data: { json: () => PAYLOAD } };

describe("service worker", () => {
	it("install / activate：立即接管", async () => {
		const { self, dispatch } = loadWorker();
		await dispatch("install", {});
		await dispatch("activate", {});
		expect(self.skipWaiting).toHaveBeenCalled();
		expect(self.clients.claim).toHaveBeenCalled();
	});

	it("聚焦的应用窗口确认会自己弹站内提示：不弹系统通知", async () => {
		const { self, dispatch } = loadWorker({ windows: [appWin(true)] });
		await dispatch("push", push);
		expect(self.registration.showNotification).not.toHaveBeenCalled();
	});

	it("只聚焦着匿名分享页（没有通知接收器）：照样弹系统通知", async () => {
		const share = shareWin(true);
		const { self, dispatch } = loadWorker({ windows: [share, appWin(false)] });
		await dispatch("push", push);
		expect(share.got).toEqual(["oryn.probe"]);
		expect(self.registration.showNotification).toHaveBeenCalledTimes(1);
	});

	it("聚焦的应用窗口此刻弹不了站内提示（实时连接断开）：弹系统通知", async () => {
		const { self, dispatch } = loadWorker({ windows: [appWin(true, false)] });
		await dispatch("push", push);
		expect(self.registration.showNotification).toHaveBeenCalledTimes(1);
	});

	it("没有聚焦的窗口：按载荷显示通知", async () => {
		const { self, dispatch } = loadWorker({ windows: [appWin(false)] });
		await dispatch("push", push);
		expect(self.registration.showNotification).toHaveBeenCalledWith("t", {
			body: "b",
			tag: "oryn-x",
			data: { url: "/t/x" },
			icon: "/icon-192.png",
			badge: "/icon-192.png",
		});
	});

	it("Safari 即使有聚焦的应用窗口也必须显示（否则会吊销订阅）；载荷缺字段时用缺省值", async () => {
		const { self, dispatch } = loadWorker({
			windows: [appWin(true)],
			userAgent:
				"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15",
		});
		await dispatch("push", { data: null });
		expect(self.registration.showNotification).toHaveBeenCalledWith("Oryn", {
			body: "",
			tag: undefined,
			data: { url: "/" },
			icon: "/icon-192.png",
			badge: "/icon-192.png",
		});
	});

	const click = { notification: { close: vi.fn(), data: { url: "/t/x" } } };

	it("点击通知：没有窗口时打开新窗口", async () => {
		const { self, dispatch } = loadWorker();
		const notification = { close: vi.fn(), data: { url: "/t/x" } };
		await dispatch("notificationclick", { notification });
		expect(notification.close).toHaveBeenCalled();
		expect(self.clients.openWindow).toHaveBeenCalledWith("/t/x");
	});

	it("点击通知：跳过排在前面的分享页，让确认过的应用窗口站内跳转并聚焦", async () => {
		const share = shareWin(true);
		const app = appWin(false);
		const { self, dispatch } = loadWorker({ windows: [share, app] });
		await dispatch("notificationclick", click);
		expect(share.got).toEqual(["oryn.probe"]);
		expect(share.focus).not.toHaveBeenCalled();
		expect(app.got).toEqual(["oryn.probe", "oryn.navigate"]);
		expect(app.postMessage).toHaveBeenLastCalledWith(
			{ type: "oryn.navigate", url: "/t/x" },
			expect.any(Array),
		);
		expect(app.focus).toHaveBeenCalled();
		expect(self.clients.openWindow).not.toHaveBeenCalled();
	});

	it("点击通知：多个应用窗口时优先聚焦的那个", async () => {
		const a = appWin(false);
		const b = appWin(true);
		const { dispatch } = loadWorker({ windows: [a, b] });
		await dispatch("notificationclick", click);
		expect(b.got).toContain("oryn.navigate");
		expect(a.got).not.toContain("oryn.navigate");
	});

	it("点击通知：只有分享页 / 登录页时新开窗口，不把分享页当成跳转成功", async () => {
		const share = shareWin(true);
		const { self, dispatch } = loadWorker({ windows: [share] });
		await dispatch("notificationclick", click);
		expect(share.focus).not.toHaveBeenCalled();
		expect(self.clients.openWindow).toHaveBeenCalledWith("/t/x");
	});

	it("点击通知：应用窗口收到跳转却没有应答：退回新开窗口", async () => {
		const app = win(true, { app: { toasts: true }, ackNavigate: false });
		const { self, dispatch } = loadWorker({ windows: [app] });
		await dispatch("notificationclick", click);
		expect(app.focus).not.toHaveBeenCalled();
		expect(self.clients.openWindow).toHaveBeenCalledWith("/t/x");
	});
});
