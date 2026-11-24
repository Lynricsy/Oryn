/**
 * M27 客户端浏览器中继（browser-relay）TanStack Query 封装与 WS 实时对账。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { realtime } from "../realtime/socket";
import { api, type Schemas, unwrap } from "./client";

export const browserRelayKeys = {
	all: ["browser-relay"] as const,
	clients: ["browser-relay", "clients"] as const,
	thread: (threadId: string) => ["browser-relay", "thread", threadId] as const,
};

/** 列出当前在线的本机浏览器扩展客户端，并订阅 WS 实时更新。 */
export function useBrowserRelayClients(enabled = true) {
	const qc = useQueryClient();
	useEffect(() => {
		if (!enabled) return;
		return realtime.onBrowserRelayClients((clients) => {
			if (clients === null) {
				void qc.invalidateQueries({ queryKey: browserRelayKeys.clients });
				return;
			}
			qc.setQueryData<Schemas["BrowserRelayClientList"]>(
				browserRelayKeys.clients,
				{ clients },
			);
		});
	}, [enabled, qc]);

	return useQuery({
		queryKey: browserRelayKeys.clients,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/browser-relay/clients")),
		staleTime: 15_000,
	});
}

/** 查询并实时跟踪指定工作线程的本机浏览器绑定与待审批状态。 */
export function useThreadBrowserRelay(threadId: string, enabled = true) {
	const qc = useQueryClient();
	useEffect(() => {
		if (!enabled || !threadId) return;
		return realtime.onBrowserRelayState((state) => {
			if (state === null) {
				void qc.invalidateQueries({
					queryKey: browserRelayKeys.thread(threadId),
				});
				return;
			}
			if (state.threadId === threadId) {
				qc.setQueryData<Schemas["ThreadBrowserRelay"]>(
					browserRelayKeys.thread(threadId),
					state,
				);
			}
		});
	}, [enabled, qc, threadId]);

	return useQuery({
		queryKey: browserRelayKeys.thread(threadId),
		enabled: enabled && Boolean(threadId),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/threads/{id}/browser-relay", {
					params: { path: { id: threadId } },
				}),
			),
		staleTime: 15_000,
	});
}

/** 绑定线程到指定的 browser_extension 设备。 */
export function useBindThreadBrowserRelay(threadId: string) {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (deviceId: string) =>
			unwrap(
				api.PUT("/api/v1/threads/{id}/browser-relay", {
					params: { path: { id: threadId } },
					body: { deviceId },
				}),
			),
		onSuccess: (state) => {
			qc.setQueryData<Schemas["ThreadBrowserRelay"]>(
				browserRelayKeys.thread(threadId),
				state,
			);
			void qc.invalidateQueries({ queryKey: browserRelayKeys.clients });
		},
	});
}

/** 解除线程与本机浏览器扩展的绑定。 */
export function useUnbindThreadBrowserRelay(threadId: string) {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: () =>
			unwrap(
				api.DELETE("/api/v1/threads/{id}/browser-relay", {
					params: { path: { id: threadId } },
				}),
			),
		onSuccess: (state) => {
			qc.setQueryData<Schemas["ThreadBrowserRelay"]>(
				browserRelayKeys.thread(threadId),
				state,
			);
			void qc.invalidateQueries({ queryKey: browserRelayKeys.clients });
		},
	});
}
