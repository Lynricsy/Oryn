/**
 * 全局助手后台研究（M26）的 REST：设置（整体 PUT，信封里带今天的花费）、跟进话题（列出、新建、修改、删除即置
 * `dismissed`、立即检查）与运行记录。增删改成功后让 `researchKeys.all` 失效；没有 WS 推送，跟进对话框开着时
 * 话题列表 30 秒轮询一次。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";
import type { paths } from "./schema";

export type ResearchSettings = Schemas["ResearchSettings"];
export type ResearchSettingsEnvelope = Schemas["ResearchSettingsEnvelope"];
export type ResearchTopic = Schemas["ResearchTopic"];
export type ResearchTopicInput = Schemas["ResearchTopicInput"];
export type ResearchTopicPatch = Schemas["ResearchTopicPatch"];
/** 列表的状态过滤：缺省 = 除 `dismissed` 之外的全部；`all` = 全部。 */
export type ResearchTopicState = NonNullable<
	NonNullable<
		paths["/api/v1/assistant/research/topics"]["get"]["parameters"]["query"]
	>["state"]
>;

/** 研究偏好的长度上限（Unicode 字符，同服务端）。 */
export const PREFERENCES_LIMIT = 1500;
/** 每日花费上限的取值范围（美元，同服务端）。 */
export const DAILY_USD_MIN = 0.1;
export const DAILY_USD_MAX = 1000;

export const researchKeys = {
	all: ["research"] as const,
	settings: ["research", "settings"] as const,
	topics: (state?: ResearchTopicState) =>
		["research", "topics", state ?? "default"] as const,
	runs: ["research", "runs"] as const,
};

function useInvalidateResearch() {
	const qc = useQueryClient();
	return () => qc.invalidateQueries({ queryKey: researchKeys.all });
}

export function useResearchSettings(enabled = true) {
	return useQuery({
		queryKey: researchKeys.settings,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/assistant/research/settings")),
	});
}

/** 整体写入设置（PUT 是全量替换，调用方在当前值上改一处再提交）；应答的信封直接写回缓存。 */
export function usePutResearchSettings() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (body: ResearchSettings) =>
			unwrap(api.PUT("/api/v1/assistant/research/settings", { body })),
		onSuccess: (saved) => qc.setQueryData(researchKeys.settings, saved),
	});
}

/** 话题列表；`poll` 时 30 秒轮询一次（跟进对话框开着时）。 */
export function useResearchTopics(state?: ResearchTopicState, poll = false) {
	return useQuery({
		queryKey: researchKeys.topics(state),
		refetchInterval: poll ? 30_000 : false,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/assistant/research/topics", {
					params: { query: state ? { state } : {} },
				}),
			),
	});
}

export function useCreateResearchTopic() {
	const invalidate = useInvalidateResearch();
	return useMutation({
		mutationFn: (body: ResearchTopicInput) =>
			unwrap(api.POST("/api/v1/assistant/research/topics", { body })),
		onSuccess: () => invalidate(),
	});
}

export function useUpdateResearchTopic() {
	const invalidate = useInvalidateResearch();
	return useMutation({
		mutationFn: ({ id, patch }: { id: string; patch: ResearchTopicPatch }) =>
			unwrap(
				api.PATCH("/api/v1/assistant/research/topics/{id}", {
					params: { path: { id } },
					body: patch,
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useDeleteResearchTopic() {
	const invalidate = useInvalidateResearch();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.DELETE("/api/v1/assistant/research/topics/{id}", {
					params: { path: { id } },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useRunResearchTopic() {
	const invalidate = useInvalidateResearch();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.POST("/api/v1/assistant/research/topics/{id}/run", {
					params: { path: { id } },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

/** 最近的研究运行（缺省 50 条）。 */
export function useResearchRuns(limit = 50, enabled = true) {
	return useQuery({
		queryKey: [...researchKeys.runs, limit] as const,
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/assistant/research/runs", {
					params: { query: { limit } },
				}),
			),
	});
}
