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

	it("markRead：乐观清零并 POST notifications/read", async () => {
		const fetch = vi.fn(async () => new Response(null, { status: 204 }));
		vi.stubGlobal("fetch", fetch);
		s().onNotification(note());
		await s().markRead(TID);
		expect(s().unread).toBe(0);
		const req = (fetch.mock.calls[0] as unknown as [Request])[0];
		expect(new URL(req.url).pathname).toBe("/api/v1/notifications/read");
		expect(req.headers.get("X-Oryn-CSRF")).toBe("1");
		expect(await req.json()).toEqual({ threadId: TID });
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
