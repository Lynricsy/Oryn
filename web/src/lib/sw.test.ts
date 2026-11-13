/**
 * `public/sw.js`（手写、不经打包）在假的 `self` 上加载，逐个分支驱动 push / notificationclick。
 */
import { describe, expect, it, vi } from "vitest";
import SOURCE from "../../public/sw.js?raw";

interface FakeWindow {
	focused: boolean;
	focus: ReturnType<typeof vi.fn>;
	postMessage: ReturnType<typeof vi.fn>;
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

function win(focused: boolean): FakeWindow {
	return { focused, focus: vi.fn(async () => {}), postMessage: vi.fn() };
}

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

	it("有聚焦的窗口时不弹（页面内已有 toast）", async () => {
		const { self, dispatch } = loadWorker({ windows: [win(true)] });
		await dispatch("push", push);
		expect(self.registration.showNotification).not.toHaveBeenCalled();
	});

	it("没有聚焦的窗口：按载荷显示通知", async () => {
		const { self, dispatch } = loadWorker({ windows: [win(false)] });
		await dispatch("push", push);
		expect(self.registration.showNotification).toHaveBeenCalledWith("t", {
			body: "b",
			tag: "oryn-x",
			data: { url: "/t/x" },
			icon: "/icon-192.png",
			badge: "/icon-192.png",
		});
	});

	it("Safari 即使有聚焦窗口也必须显示（否则会吊销订阅）；载荷缺字段时用缺省值", async () => {
		const { self, dispatch } = loadWorker({
			windows: [win(true)],
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

	it("点击通知：没有窗口时打开新窗口", async () => {
		const { self, dispatch } = loadWorker();
		const notification = { close: vi.fn(), data: { url: "/t/x" } };
		await dispatch("notificationclick", { notification });
		expect(notification.close).toHaveBeenCalled();
		expect(self.clients.openWindow).toHaveBeenCalledWith("/t/x");
	});

	it("点击通知：有窗口时聚焦并让页面站内跳转", async () => {
		const w = win(false);
		const { self, dispatch } = loadWorker({ windows: [w] });
		await dispatch("notificationclick", {
			notification: { close: vi.fn(), data: { url: "/t/x" } },
		});
		expect(w.focus).toHaveBeenCalled();
		expect(w.postMessage).toHaveBeenCalledWith({
			type: "oryn.navigate",
			url: "/t/x",
		});
		expect(self.clients.openWindow).not.toHaveBeenCalled();
	});
});
