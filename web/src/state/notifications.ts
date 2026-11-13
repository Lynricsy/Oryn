/**
 * 应用内通知（M5 计划 6.2）：最近通知、未读数与 toast。
 * - `load()`：`GET notifications?limit=50`（连接建立 / 重连后各一次，见 AppShell）。
 * - WS `notification` → `onNotification`：页面可见 → toast（6s 自动消失；正停在该线程页时不弹）；
 *   页面隐藏 → 有通知权限且本浏览器没有推送订阅时弹系统通知（有订阅时交给 service worker，页面不重复弹）；
 *   没有权限时什么都不弹，回到页面后在铃铛里看到。
 * - WS `notifications.read` → `onRead`；`markRead` 先乐观更新再 `POST notifications/read`。
 */
import type { TFunction } from "i18next";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { api, type Schemas, unwrap } from "../api/client";
import i18n from "../i18n";

export type AppNotification = Schemas["Notification"];

export const TOAST_MS = 6_000;
const LIST_LIMIT = 50;
const MAX_TOASTS = 3;

/** 系统通知 / 推送的去重标签：与服务端 Web Push 载荷的 `tag` 同形（线程 id 末 12 位）。 */
export function notificationTag(threadId: string | undefined): string {
	return `oryn-${(threadId ?? "").slice(-12)}`;
}

/** 标题与正文；正文为空时用该 kind 的缺省文案（按界面语言）。 */
export function notificationText(
	n: Pick<AppNotification, "kind" | "title" | "body">,
	t: TFunction,
): { title: string; body: string } {
	return {
		title: n.title || t("notify.untitled"),
		body: n.body || t(`notify.kindBody.${n.kind}`),
	};
}

export interface NotificationsState {
	items: AppNotification[];
	unread: number;
	toasts: AppNotification[];
	loaded: boolean;
	/** 本浏览器已有有效的推送订阅（由设置页与启动检查写入）。 */
	pushActive: boolean;
	load(): Promise<void>;
	onNotification(n: AppNotification): void;
	onRead(threadId?: string): void;
	markRead(threadId?: string): Promise<void>;
	dismissToast(id: string): void;
	setPushActive(on: boolean): void;
}

/** 站内跳转（AppShell 注入 react-router 的 navigate）；未注入时退回整页跳转。 */
let navigateTo: (url: string) => void = (url) => {
	location.assign(url);
};

export function setNotificationNavigator(fn: (url: string) => void) {
	navigateTo = fn;
}

const toastTimers = new Map<string, ReturnType<typeof setTimeout>>();

function pageVisible(): boolean {
	return (
		typeof document === "undefined" || document.visibilityState === "visible"
	);
}

function showSystemNotification(n: AppNotification) {
	const { title, body } = notificationText(n, i18n.t);
	const sys = new Notification(title, {
		body,
		tag: notificationTag(n.threadId),
	});
	sys.onclick = () => {
		window.focus();
		navigateTo(n.url);
		sys.close();
	};
}

export const notificationsStore = createStore<NotificationsState>(
	(set, get) => ({
		items: [],
		unread: 0,
		toasts: [],
		loaded: false,
		pushActive: false,
		load: async () => {
			try {
				const res = await unwrap(
					api.GET("/api/v1/notifications", {
						params: { query: { limit: LIST_LIMIT } },
					}),
				);
				set({ items: res.notifications, unread: res.unread, loaded: true });
			} catch {
				// 加载失败只影响铃铛里的历史；实时到达的通知照常显示
			}
		},
		onNotification: (n) => {
			const s = get();
			if (s.items.some((x) => x.id === n.id)) return;
			set({
				items: [n, ...s.items].slice(0, LIST_LIMIT),
				unread: n.readAt ? s.unread : s.unread + 1,
			});
			if (pageVisible()) {
				if (n.threadId && location.pathname === `/t/${n.threadId}`) return;
				set({ toasts: [...get().toasts, n].slice(-MAX_TOASTS) });
				toastTimers.set(
					n.id,
					setTimeout(() => get().dismissToast(n.id), TOAST_MS),
				);
				return;
			}
			if (
				typeof Notification !== "undefined" &&
				Notification.permission === "granted" &&
				!s.pushActive
			)
				showSystemNotification(n);
		},
		onRead: (threadId) => {
			const now = Date.now();
			const s = get();
			let cleared = 0;
			const items = s.items.map((n) => {
				if (n.readAt || (threadId && n.threadId !== threadId)) return n;
				cleared++;
				return { ...n, readAt: now };
			});
			set({
				items,
				unread: threadId ? Math.max(0, s.unread - cleared) : 0,
				toasts: threadId ? s.toasts.filter((n) => n.threadId !== threadId) : [],
			});
		},
		markRead: async (threadId) => {
			get().onRead(threadId);
			await unwrap(
				api.POST("/api/v1/notifications/read", {
					body: threadId ? { threadId } : {},
				}),
			);
		},
		dismissToast: (id) => {
			clearTimeout(toastTimers.get(id));
			toastTimers.delete(id);
			set({ toasts: get().toasts.filter((n) => n.id !== id) });
		},
		setPushActive: (on) => set({ pushActive: on }),
	}),
);

export function useNotifications<T>(selector: (s: NotificationsState) => T): T {
	return useStore(notificationsStore, selector);
}
