/**
 * Space 与全局助手的 REST（M9）。Space 的线程不另行请求：从线程索引里按 `spaceId` 过滤（实时）。
 * Space 本身没有实时推送：增删改成功后让 `spaceKeys.all` 失效。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { indexStore } from "../state/stores";
import { api, type Schemas, type Thread, unwrap } from "./client";

export type Space = Schemas["Space"];
export type SpaceProgressDay = Schemas["SpaceProgressDay"];

export const spaceKeys = {
	all: ["spaces"] as const,
	list: (archived: boolean) => ["spaces", "list", { archived }] as const,
	one: (id: string) => ["spaces", "one", id] as const,
	progress: (id: string, days: number, tz: string) =>
		["spaces", "progress", id, { days, tz }] as const,
};

/** 浏览器时区（进度图的自然日边界）。 */
export function browserTimeZone(): string {
	try {
		return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
	} catch {
		return "UTC";
	}
}

export function useSpaces(archived = false) {
	return useQuery({
		queryKey: spaceKeys.list(archived),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/spaces", {
					params: { query: archived ? { archived: true } : {} },
				}),
			),
		staleTime: 15_000,
	});
}

export function useSpace(id: string) {
	return useQuery({
		queryKey: spaceKeys.one(id),
		enabled: Boolean(id),
		queryFn: () =>
			unwrap(api.GET("/api/v1/spaces/{id}", { params: { path: { id } } })),
	});
}

/** 近 `days` 天的进度；索引里本 Space 的线程变化（归档、交付、新建）时由页面失效重取。 */
export function useSpaceProgress(id: string, days = 30) {
	const tz = browserTimeZone();
	return useQuery({
		queryKey: spaceKeys.progress(id, days, tz),
		enabled: Boolean(id),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/spaces/{id}/progress", {
					params: { path: { id }, query: { days, tz } },
				}),
			),
	});
}

function useInvalidateSpaces() {
	const qc = useQueryClient();
	return () => qc.invalidateQueries({ queryKey: spaceKeys.all });
}

export function useCreateSpace() {
	const invalidate = useInvalidateSpaces();
	return useMutation({
		mutationFn: (body: Schemas["SpaceCreate"]) =>
			unwrap(api.POST("/api/v1/spaces", { body })),
		onSuccess: () => invalidate(),
	});
}

export function useUpdateSpace(id: string) {
	const invalidate = useInvalidateSpaces();
	return useMutation({
		mutationFn: (body: Schemas["SpacePatch"]) =>
			unwrap(
				api.PATCH("/api/v1/spaces/{id}", { params: { path: { id } }, body }),
			),
		onSuccess: () => invalidate(),
	});
}

export function useDeleteSpace() {
	const invalidate = useInvalidateSpaces();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(api.DELETE("/api/v1/spaces/{id}", { params: { path: { id } } })),
		onSuccess: () => invalidate(),
	});
}

/** 新建一个全局助手对话并写入索引（不等推送，页面随即跳过去）。 */
export async function createAssistantConversation(): Promise<Thread> {
	const res = await unwrap(api.POST("/api/v1/assistant/conversations"));
	indexStore.getState().upsertThread(res.thread);
	return res.thread;
}
