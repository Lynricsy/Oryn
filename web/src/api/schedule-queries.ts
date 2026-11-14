/**
 * 定时的 REST（M9）：两种模式（线程内 / 每次新建线程）共用一张表。增删改成功后让 `scheduleKeys.all` 失效；
 * 别处（另一台设备、agent、按时触发、自动暂停）的变化经 WS `schedules.changed` 推送，`useRealtimeScheduleSync`
 * 据此（以及每次 WS 重新连上时）让同一前缀的查询全部失效。
 * 线程内定时的摘要（`thread.schedule`）随线程索引推送，不必另行刷新。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { type RealtimeClient, realtime } from "../realtime/socket";
import { api, type Schemas, unwrap } from "./client";

export type Schedule = Schemas["Schedule"];
export type ScheduleInput = Schemas["ScheduleInput"];
export type ScheduleRun = Schemas["ScheduleRun"];

export interface ScheduleFilter {
	mode?: Schemas["ScheduleMode"];
	/** 项目 id；`none` = 没有项目的 `new_thread` 定时 */
	project?: string;
	space?: string;
	thread?: string;
}

export const scheduleKeys = {
	all: ["schedules"] as const,
	list: (f: ScheduleFilter) => ["schedules", "list", f] as const,
	one: (id: string) => ["schedules", "one", id] as const,
	thread: (threadId: string) => ["schedules", "thread", threadId] as const,
	runs: (id: string) => ["schedules", "runs", id] as const,
};

/**
 * 定时查询与服务端对账：收到 `schedules.changed`、或 WS（重新）连上时，`["schedules"]` 前缀的查询全部失效。
 * 在调 `realtime.connect()` 的应用壳里调用一次。
 */
export function useRealtimeScheduleSync(
	client: Pick<RealtimeClient, "onSchedulesChanged"> = realtime,
): void {
	const qc = useQueryClient();
	useEffect(
		() =>
			client.onSchedulesChanged(() => {
				void qc.invalidateQueries({ queryKey: scheduleKeys.all });
			}),
		[client, qc],
	);
}

export function useSchedules(filter: ScheduleFilter = {}, enabled = true) {
	return useQuery({
		queryKey: scheduleKeys.list(filter),
		enabled,
		queryFn: () =>
			unwrap(api.GET("/api/v1/schedules", { params: { query: filter } })),
		staleTime: 10_000,
	});
}

export function useSchedule(id: string | undefined) {
	return useQuery({
		queryKey: scheduleKeys.one(id ?? ""),
		enabled: Boolean(id),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/schedules/{id}", {
					params: { path: { id: id ?? "" } },
				}),
			),
	});
}

/** 线程内定时；没有时为 `null`（服务端 404 `schedule_not_found`）。 */
export function useThreadSchedule(threadId: string, enabled = true) {
	return useQuery({
		queryKey: scheduleKeys.thread(threadId),
		enabled: enabled && Boolean(threadId),
		queryFn: async () => {
			const res = await api.GET("/api/v1/threads/{id}/schedule", {
				params: { path: { id: threadId } },
			});
			if (res.response.status === 404) return null;
			return unwrap(Promise.resolve(res));
		},
	});
}

/** 最近 20 次运行；有未结束的运行时 5 秒轮询一次。 */
export function useScheduleRuns(id: string | undefined, poll = false) {
	return useQuery({
		queryKey: scheduleKeys.runs(id ?? ""),
		enabled: Boolean(id),
		refetchInterval: poll ? 5_000 : false,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/schedules/{id}/runs", {
					params: { path: { id: id ?? "" }, query: { limit: 20 } },
				}),
			),
	});
}

function useInvalidateSchedules() {
	const qc = useQueryClient();
	return () => qc.invalidateQueries({ queryKey: scheduleKeys.all });
}

export function useCreateSchedule() {
	const invalidate = useInvalidateSchedules();
	return useMutation({
		mutationFn: (body: ScheduleInput) =>
			unwrap(api.POST("/api/v1/schedules", { body })),
		onSuccess: () => invalidate(),
	});
}

export function usePutSchedule(id: string) {
	const invalidate = useInvalidateSchedules();
	return useMutation({
		mutationFn: (body: ScheduleInput) =>
			unwrap(
				api.PUT("/api/v1/schedules/{id}", { params: { path: { id } }, body }),
			),
		onSuccess: () => invalidate(),
	});
}

export function usePutThreadSchedule(threadId: string) {
	const invalidate = useInvalidateSchedules();
	return useMutation({
		mutationFn: (body: ScheduleInput) =>
			unwrap(
				api.PUT("/api/v1/threads/{id}/schedule", {
					params: { path: { id: threadId } },
					body,
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useDeleteSchedule() {
	const invalidate = useInvalidateSchedules();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.DELETE("/api/v1/schedules/{id}", { params: { path: { id } } }),
			),
		onSuccess: () => invalidate(),
	});
}

/** 启停：`true` 恢复（`resume`），`false` 暂停（`pause`）。 */
export function useSetScheduleEnabled() {
	const invalidate = useInvalidateSchedules();
	return useMutation({
		mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
			unwrap(
				enabled
					? api.POST("/api/v1/schedules/{id}/resume", {
							params: { path: { id } },
						})
					: api.POST("/api/v1/schedules/{id}/pause", {
							params: { path: { id } },
						}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useRunSchedule() {
	const invalidate = useInvalidateSchedules();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.POST("/api/v1/schedules/{id}/run", { params: { path: { id } } }),
			),
		onSuccess: () => invalidate(),
	});
}

/** 预览接下来至多 5 次触发时间（只看时间字段）。 */
export function previewSchedule(
	body: ScheduleInput,
	signal?: AbortSignal,
): Promise<Schemas["SchedulePreview"]> {
	return unwrap(api.POST("/api/v1/schedules/preview", { body, signal }));
}
