import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	type AppNotification,
	notificationsStore,
	notificationTag,
	setNotificationNavigator,
	TOAST_MS,
} from "./notifications";

const TID = "T-0193a1b2-0000-7000-8000-0000000000n1";

function note(p: Partial<AppNotification> = {}): AppNotification {
	return {
		id: `ntf_${Math.random()}`,
		kind: "done",
		threadId: TID,
		title: "线程",
		body: "",
		url: `/t/${TID}`,
		createdAt: 1,
		deliveries: [],
		...p,
	};
}

class FakeNotification {
	static permission: NotificationPermission = "granted";
	static created: FakeNotification[] = [];
	onclick: (() => void) | null = null;
	closed = false;
	readonly title: string;
	readonly options: NotificationOptions;
	constructor(title: string, options: NotificationOptions) {
		this.title = title;
		this.options = options;
		FakeNotification.created.push(this);
	}
	close() {
		this.closed = true;
	}
}

let visibility: DocumentVisibilityState;

/** 推进微任务直到条件成立（请求经 openapi-fetch 的中间件后才真正调用 fetch）。 */
async function until(cond: () => boolean) {
	for (let i = 0; i < 100 && !cond(); i++) await Promise.resolve();
}

beforeEach(() => {
	vi.useFakeTimers();
	visibility = "visible";
	vi.spyOn(document, "visibilityState", "get").mockImplementation(
		() => visibility,
	);
	FakeNotification.permission = "granted";
	FakeNotification.created = [];
	vi.stubGlobal("Notification", FakeNotification);
	history.replaceState(null, "", "/");
	notificationsStore.setState({
		items: [],
		unread: 0,
		toasts: [],
		loaded: false,
		pushActive: false,
	});
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

const s = () => notificationsStore.getState();

describe("通知 store", () => {
	it("页面可见：进列表、未读 +1、弹 toast，6s 后自动消失", () => {
		const n = note();
		s().onNotification(n);
		expect(s().items).toEqual([n]);
		expect(s().unread).toBe(1);
		expect(s().toasts).toEqual([n]);
		vi.advanceTimersByTime(TOAST_MS);
		expect(s().toasts).toEqual([]);
		expect(FakeNotification.created).toHaveLength(0);
	});

	it("正停在该线程页时不弹 toast（仍计入未读）", () => {
		history.replaceState(null, "", `/t/${TID}`);
		s().onNotification(note());
		expect(s().toasts).toEqual([]);
		expect(s().unread).toBe(1);
	});

	it("同一条通知重复到达只算一次", () => {
		const n = note();
		s().onNotification(n);
		s().onNotification(n);
		expect(s().items).toHaveLength(1);
		expect(s().unread).toBe(1);
	});

	it("页面隐藏、已授权、本浏览器未订阅推送：弹系统通知，点击跳转", () => {
		visibility = "hidden";
		const navigate = vi.fn();
		setNotificationNavigator(navigate);
		const focus = vi.spyOn(window, "focus").mockImplementation(() => {});
		s().onNotification(note({ kind: "input", body: "" }));
		expect(s().toasts).toEqual([]);
		const [sys] = FakeNotification.created;
		expect(sys?.title).toBe("线程");
		// 空正文用该 kind 的缺省文案
		expect(sys?.options).toEqual({
			body: "等待你的回答",
			tag: notificationTag(TID),
		});
		expect(notificationTag(TID)).toBe(`oryn-${TID.slice(-12)}`);
		sys?.onclick?.();
		expect(focus).toHaveBeenCalled();
		expect(navigate).toHaveBeenCalledWith(`/t/${TID}`);
		expect(sys?.closed).toBe(true);
	});

	it("页面隐藏但已有推送订阅：交给 service worker，页面不弹", () => {
		visibility = "hidden";
		s().setPushActive(true);
		s().onNotification(note());
		expect(FakeNotification.created).toHaveLength(0);
		expect(s().unread).toBe(1);
	});

	it("页面隐藏且没有通知权限：什么都不弹", () => {
		visibility = "hidden";
		FakeNotification.permission = "default";
		s().onNotification(note());
		expect(FakeNotification.created).toHaveLength(0);
		expect(s().toasts).toEqual([]);
	});

	it("notifications.read：按线程或全部清未读与 toast", () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(() => new Promise<Response>(() => {})),
		);
		const other = "T-0193a1b2-0000-7000-8000-0000000000n2";
		s().onNotification(note({ id: "a" }));
		s().onNotification(note({ id: "b", threadId: other, url: `/t/${other}` }));
		expect(s().unread).toBe(2);
		s().onRead(TID);
		expect(s().unread).toBe(1);
		expect(s().items.find((n) => n.id === "a")?.readAt).toBeDefined();
		expect(s().items.find((n) => n.id === "b")?.readAt).toBeUndefined();
		expect(s().toasts.map((n) => n.id)).toEqual(["b"]);
		s().onRead();
		expect(s().unread).toBe(0);
		expect(s().toasts).toEqual([]);
	});

	it("markRead：乐观清零并 POST notifications/read，之后重新取权威未读数", async () => {
		const fetch = vi.fn(async (req: Request) =>
			new URL(req.url).pathname === "/api/v1/notifications/read"
				? new Response(null, { status: 204 })
				: Response.json({ notifications: [], unread: 0 }),
		);
		vi.stubGlobal("fetch", fetch);
		s().onNotification(note());
		await s().markRead(TID);
		expect(s().unread).toBe(0);
		const reqs = fetch.mock.calls.map((c) => (c as unknown as [Request])[0]);
		const req = reqs[0] as Request;
		expect(new URL(req.url).pathname).toBe("/api/v1/notifications/read");
		expect(req.headers.get("X-Oryn-CSRF")).toBe("1");
		expect(await req.json()).toEqual({ threadId: TID });
		expect(reqs.map((r) => `${r.method} ${new URL(r.url).pathname}`)).toEqual([
			"POST /api/v1/notifications/read",
			"GET /api/v1/notifications",
		]);
	});

	it("按线程已读：本地最近 50 条之外还有该线程的未读时，以服务端的总数为准", async () => {
		// 同一线程 60 条未读，本地只持有最近 50 条
		const local = Array.from({ length: 50 }, (_, i) => note({ id: `n${i}` }));
		notificationsStore.setState({ items: local, unread: 60 });
		vi.stubGlobal(
			"fetch",
			vi.fn(async () =>
				Response.json({
					notifications: local.map((n) => ({ ...n, readAt: 5 })),
					unread: 0,
				}),
			),
		);
		s().onRead(TID);
		await vi.waitFor(() => expect(s().unread).toBe(0));
	});

	it("load 期间经 WS 到达的通知并入结果，不被较早的应答覆盖", async () => {
		let respond: (r: Response) => void = () => {};
		const old = note({ id: "old" });
		let asked = false;
		const fetch = vi.fn(
			() =>
				new Promise<Response>((r) => {
					asked = true;
					respond = r;
				}),
		);
		vi.stubGlobal("fetch", fetch);
		const pending = s().load();
		await until(() => asked);
		s().onNotification(note({ id: "live" }));
		respond(Response.json({ notifications: [old], unread: 3 }));
		await pending;
		expect(s().items.map((n) => n.id)).toEqual(["live", "old"]);
		expect(s().unread).toBe(4);
	});

	it("load 期间又有已读：丢弃可能过期的应答，再取一次", async () => {
		const answers = [
			{ notifications: [note({ id: "a" })], unread: 9 },
			{ notifications: [note({ id: "a", readAt: 1 })], unread: 0 },
		];
		const resolvers: ((r: Response) => void)[] = [];
		vi.stubGlobal(
			"fetch",
			vi.fn(
				() =>
					new Promise<Response>((r) => {
						resolvers.push(r);
					}),
			),
		);
		const pending = s().load();
		await until(() => resolvers.length === 1);
		s().onRead();
		resolvers[0]?.(Response.json(answers[0]));
		await until(() => resolvers.length === 2);
		expect(resolvers).toHaveLength(2);
		resolvers[1]?.(Response.json(answers[1]));
		await pending;
		expect(s().unread).toBe(0);
	});

	it("load：取最近 50 条与未读数", async () => {
		const n = note();
		const fetch = vi.fn(async () =>
			Response.json({ notifications: [n], unread: 7 }),
		);
		vi.stubGlobal("fetch", fetch);
		await s().load();
		const req = (fetch.mock.calls[0] as unknown as [Request])[0];
		expect(new URL(req.url).search).toBe("?limit=50");
		expect(s()).toMatchObject({ items: [n], unread: 7, loaded: true });
	});
});
