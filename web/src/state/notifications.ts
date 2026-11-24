/**
 * 应用内通知（M5 计划 6.2）：最近通知、未读数与 toast。
 * - `load()`：`GET notifications?limit=50`（连接建立 / 重连后各一次，见 AppShell）。
 * - WS `notification` → `onNotification`：页面在前台（`host.isForeground()`）→ toast（6s 自动消失；正停在该线程页或
 *   该助手对话页 `/assistant/<id>` 时不弹）；
 *   浏览器里不在前台 → 有通知权限且本浏览器没有推送订阅时弹系统通知（有订阅时交给 service worker，页面不重复弹）；
 *   没有权限时什么都不弹，回到页面后在铃铛里看到。
 *   桌面壳（M6）里不在前台 → `host.notify` 交给壳弹系统通知（点击直达线程），不碰 `Notification` 与推送订阅。
 * - WS `notifications.read` → `onRead`；`markRead` 先乐观更新再 `POST notifications/read`。
 *   已读目标 `ReadTarget`：`{threadId}` = 该线程，`{id}` = 只这一条（没有线程的系统通知），省略 = 全部。
 *   按目标已读之后都要重新 `load()`：未读数是全库总数，本地只有最近 50 条，乐观扣减算不出其余部分。
 *   `load()` 期间经 WS 到达的通知合并回结果（应答可能早于它们生成）；期间又有已读则丢弃这次应答、再取一次。
 */
import type { TFunction } from "i18next";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { api, type Schemas, unwrap } from "../api/client";
import i18n from "../i18n";
import { host } from "../lib/host";
import {
	prepareNotificationAvatar,
	resolveNotificationAvatar,
} from "../lib/notification-avatar";
import { withoutNerdIcon } from "../lib/title";

export type AppNotification = Schemas["Notification"];

export const TOAST_MS = 6_000;
const LIST_LIMIT = 50;
const MAX_TOASTS = 3;

/** 已读目标：`{threadId}` 标该线程的全部通知，`{id}` 只标这一条；省略 = 全部。 */
export type ReadTarget = { threadId: string } | { id: string };

/** 点开一条通知时该标哪些已读：有线程标整个线程，否则只标这一条。 */
export function readTargetOf(
	n: Pick<AppNotification, "id" | "threadId">,
): ReadTarget {
	return n.threadId ? { threadId: n.threadId } : { id: n.id };
}

/** 与服务端推送载荷的 tag 同形：线程 id 末 12 位；没有线程时 budget → "oryn-budget"，其余 → "oryn-automations"。 */
export function notificationTag(
	n: Pick<AppNotification, "threadId" | "kind">,
): string {
	if (n.threadId) return `oryn-${n.threadId.slice(-12)}`;
	return n.kind === "budget" ? "oryn-budget" : "oryn-automations";
}

/** 标题与正文；正文为空时用该 kind 的缺省文案（按界面语言；不认识的 kind 为空串）。 */
export function notificationText(
	n: Pick<AppNotification, "kind" | "title" | "body">,
	t: TFunction,
): { title: string; body: string } {
	return {
		title: n.title || t("notify.untitled"),
		body: n.body || t(`notify.kindBody.${n.kind}`, { defaultValue: "" }),
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
	onRead(target?: ReadTarget): void;
	/**
	 * input 通知对应的请求已解决（WS `notifications.resolved`，或本页作答成功 / 得到 409）：记 `resolvedAt`、去掉 `quickAnswer`。
	 * 同样递增 readGeneration：在途的 load() 应答可能早于解决，丢弃后重取。
	 */
	onResolved(ids: readonly string[], at: number): void;
	markRead(target?: ReadTarget): Promise<void>;
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
/** 各个进行中的 `load()` 期间经 WS 到达的通知。 */
const arrivedDuringLoad = new Set<AppNotification[]>();
/** 每次已读（本地或其它设备）加一；`load()` 据此判断应答是否可能已过期。 */
let readGeneration = 0;

/** 系统通知（浏览器、桌面壳）的文字：系统字体画不了 Nerd 字形，标题卡片的 Nerd 图标去掉（服务端推送同样处理）。 */
function systemNotificationText(n: AppNotification): {
	title: string;
	body: string;
} {
	const { title, body } = notificationText(n, i18n.t);
	return { title: withoutNerdIcon(title), body };
}

function showSystemNotification(n: AppNotification) {
	const { title, body } = systemNotificationText(n);
	const sys = new Notification(title, {
		body,
		tag: notificationTag(n),
	});
	sys.onclick = () => {
		window.focus();
		navigateTo(n.url);
		sys.close();
	};
}

export const notificationsStore = createStore<NotificationsState>(
	(set, get) => {
		/** 乐观已读：本地列表里命中目标的条目标已读并扣减；有目标时只是近似值，随后由 load() 校正。 */
		const applyRead = (target: ReadTarget | undefined) => {
			readGeneration++;
			const now = Date.now();
			const s = get();
			const hit = (n: AppNotification) =>
				!target ||
				("id" in target ? n.id === target.id : n.threadId === target.threadId);
			let cleared = 0;
			const items = s.items.map((n) => {
				if (n.readAt || !hit(n)) return n;
				cleared++;
				return { ...n, readAt: now };
			});
			set({
				items,
				unread: target ? Math.max(0, s.unread - cleared) : 0,
				toasts: target ? s.toasts.filter((n) => !hit(n)) : [],
			});
		};
		return {
			items: [],
			unread: 0,
			toasts: [],
			loaded: false,
			pushActive: false,
			load: async () => {
				const arrived: AppNotification[] = [];
				const gen = readGeneration;
				arrivedDuringLoad.add(arrived);
				let res: Schemas["NotificationList"];
				try {
					res = await unwrap(
						api.GET("/api/v1/notifications", {
							params: { query: { limit: LIST_LIMIT } },
						}),
					);
				} catch {
					// 加载失败只影响铃铛里的历史；实时到达的通知照常显示
					return;
				} finally {
					arrivedDuringLoad.delete(arrived);
				}
				// 请求期间有过已读：应答未必反映它，再取一次
				if (gen !== readGeneration) return get().load();
				const ids = new Set(res.notifications.map((n) => n.id));
				const extra = arrived.filter((n) => !ids.has(n.id));
				set({
					items: [...extra, ...res.notifications].slice(0, LIST_LIMIT),
					unread: res.unread + extra.filter((n) => !n.readAt).length,
					loaded: true,
				});
			},
			onNotification: (n) => {
				const s = get();
				if (s.items.some((x) => x.id === n.id)) return;
				for (const list of arrivedDuringLoad) list.push(n);
				set({
					items: [n, ...s.items].slice(0, LIST_LIMIT),
					unread: n.readAt ? s.unread : s.unread + 1,
				});
				if (host.isForeground()) {
					if (
						n.threadId &&
						(location.pathname === `/t/${n.threadId}` ||
							location.pathname === `/assistant/${n.threadId}`)
					)
						return;
					set({ toasts: [...get().toasts, n].slice(-MAX_TOASTS) });
					toastTimers.set(
						n.id,
						setTimeout(() => get().dismissToast(n.id), TOAST_MS),
					);
					return;
				}
				if (host.kind === "tauri") {
					const base = {
						id: n.id,
						...systemNotificationText(n),
						path: n.url,
						timestamp: n.createdAt,
					};
					const avatar = prepareNotificationAvatar(
						resolveNotificationAvatar(n),
						host.platform,
					);
					if (avatar instanceof Promise) {
						void avatar.then(
							(av) => host.notify({ ...base, avatar: av }),
							() =>
								host.notify({
									...base,
									avatar: resolveNotificationAvatar(n),
								}),
						);
					} else {
						host.notify({ ...base, avatar });
					}
					return;
				}
				if (
					typeof Notification !== "undefined" &&
					Notification.permission === "granted" &&
					!s.pushActive
				)
					showSystemNotification(n);
			},
			onResolved: (ids, at) => {
				readGeneration++;
				const hit = new Set(ids);
				set({
					items: get().items.map((n) =>
						hit.has(n.id) && !n.resolvedAt
							? { ...n, resolvedAt: at, quickAnswer: undefined }
							: n,
					),
				});
			},
			onRead: (target) => {
				applyRead(target);
				if (target) void get().load();
			},
			markRead: async (target) => {
				applyRead(target);
				await unwrap(
					api.POST("/api/v1/notifications/read", { body: target ?? {} }),
				);
				if (target) await get().load();
			},
			dismissToast: (id) => {
				clearTimeout(toastTimers.get(id));
				toastTimers.delete(id);
				set({ toasts: get().toasts.filter((n) => n.id !== id) });
			},
			setPushActive: (on) => set({ pushActive: on }),
		};
	},
);

export function useNotifications<T>(selector: (s: NotificationsState) => T): T {
	return useStore(notificationsStore, selector);
}
