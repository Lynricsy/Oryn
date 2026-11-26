/**
 * Agent 沙箱（M30）的查询与销毁：线程信息页列出该线程名下在用的沙箱加最近 60 分钟内结束的，主人可销毁。
 * 沙箱由 orb 里的 agent 创建、变网络、续期、销毁，控制面经 WS `sandboxes.changed`（带线程 id）推送；
 * `useRealtimeSandboxSync` 据此让该线程的沙箱查询失效，WS 每次（重新）连上时让 `["sandboxes"]` 前缀全部失效。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { type RealtimeClient, realtime } from "../realtime/socket";
import { api, type Schemas, unwrap } from "./client";

export type Sandbox = Schemas["Sandbox"];

export const sandboxKeys = {
	all: ["sandboxes"] as const,
	thread: (threadId: string) => ["sandboxes", "thread", threadId] as const,
};

/** 沙箱查询与服务端对账：在调 `realtime.connect()` 的应用壳里调用一次。 */
export function useRealtimeSandboxSync(
	client: Pick<RealtimeClient, "onSandboxesChanged"> = realtime,
): void {
	const qc = useQueryClient();
	useEffect(
		() =>
			client.onSandboxesChanged((threadId) => {
				void qc.invalidateQueries({
					queryKey:
						threadId === null ? sandboxKeys.all : sandboxKeys.thread(threadId),
				});
			}),
		[client, qc],
	);
}

/** 线程名下的沙箱：在用的加最近 60 分钟内结束的（服务端按创建时间排序）。 */
export function useThreadSandboxes(threadId: string) {
	return useQuery({
		queryKey: sandboxKeys.thread(threadId),
		enabled: Boolean(threadId),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/sandboxes", {
					params: { query: { thread: threadId } },
				}),
			),
		staleTime: 15_000,
	});
}

/** 主人销毁一台沙箱（原因记为 `owner`）。 */
export function useDestroySandbox() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.DELETE("/api/v1/sandboxes/{sandbox}", {
					params: { path: { sandbox: id } },
				}),
			),
		onSuccess: () => qc.invalidateQueries({ queryKey: sandboxKeys.all }),
	});
}
