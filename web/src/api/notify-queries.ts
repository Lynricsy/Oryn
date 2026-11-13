/** 通知相关 REST（M5）：渠道、本浏览器之外的推送订阅、带投递状态的最近通知。键在 `notifyKeys` 下。 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type NotificationChannel = Schemas["NotificationChannel"];
export type NotificationChannelInput = Schemas["NotificationChannelInput"];
export type NotificationChannelPatch = Schemas["NotificationChannelPatch"];

export const notifyKeys = {
	channels: ["notification-channels"] as const,
	subscriptions: ["push", "subscriptions"] as const,
	recent: (limit: number) => ["notifications", { limit }] as const,
};

/** 渠道列表；`pollMs` 给出时按该间隔轮询（Telegram 向导等待绑定用）。 */
export function useNotificationChannels(pollMs?: number) {
	return useQuery({
		queryKey: notifyKeys.channels,
		queryFn: () => unwrap(api.GET("/api/v1/notification-channels")),
		refetchInterval: pollMs,
	});
}

export function usePushSubscriptions() {
	return useQuery({
		queryKey: notifyKeys.subscriptions,
		queryFn: () => unwrap(api.GET("/api/v1/push/subscriptions")),
	});
}

/** 设置页「最近通知」：带各投递的状态，10s 轮询（待重试的投递会变）。 */
export function useRecentNotifications(limit: number) {
	return useQuery({
		queryKey: notifyKeys.recent(limit),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/notifications", { params: { query: { limit } } }),
			),
		refetchInterval: 10_000,
	});
}

function useInvalidateChannels() {
	const qc = useQueryClient();
	return () => qc.invalidateQueries({ queryKey: notifyKeys.channels });
}

export function useCreateChannel() {
	const invalidate = useInvalidateChannels();
	return useMutation({
		mutationFn: (body: NotificationChannelInput) =>
			unwrap(api.POST("/api/v1/notification-channels", { body })),
		onSuccess: invalidate,
	});
}

export function useUpdateChannel() {
	const invalidate = useInvalidateChannels();
	return useMutation({
		mutationFn: ({
			id,
			patch,
		}: {
			id: string;
			patch: NotificationChannelPatch;
		}) =>
			unwrap(
				api.PATCH("/api/v1/notification-channels/{id}", {
					params: { path: { id } },
					body: patch,
				}),
			),
		onSuccess: invalidate,
	});
}

export function useDeleteChannel() {
	const invalidate = useInvalidateChannels();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.DELETE("/api/v1/notification-channels/{id}", {
					params: { path: { id } },
				}),
			),
		onSuccess: invalidate,
	});
}

/** 发一条测试消息；结果 `{ok, error?}` 就地显示，同时渠道的 `lastError` 会变。 */
export function useTestChannel() {
	const invalidate = useInvalidateChannels();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.POST("/api/v1/notification-channels/{id}/test", {
					params: { path: { id } },
				}),
			),
		onSettled: invalidate,
	});
}

/** 取一个 Telegram 绑定码（10 分钟有效）。 */
export function useLinkTelegram() {
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.POST("/api/v1/notification-channels/{id}/telegram/link", {
					params: { path: { id } },
				}),
			),
	});
}

export function useDeletePushSubscription() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.DELETE("/api/v1/push/subscriptions/{id}", {
					params: { path: { id } },
				}),
			),
		onSuccess: () =>
			qc.invalidateQueries({ queryKey: notifyKeys.subscriptions }),
	});
}
