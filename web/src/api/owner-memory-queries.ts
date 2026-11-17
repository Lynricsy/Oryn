/**
 * 全局助手主人记忆的 REST 读写（M17 步骤 5.1）。键都在 `["owner-memory", …]` 下；数据变了（写入、遗忘、失效、批准、
 * 排除）之后整组失效：遗忘与失效会让主题进入「等待刷新」，状态卡的计数也随之变化。时间戳都是 Unix 毫秒。
 */
import {
	type QueryClient,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type OwnerMemorySettings = Schemas["OwnerMemorySettings"];
export type OwnerMemoryDecideEndpoint = Schemas["OwnerMemoryDecideEndpoint"];
export type OwnerMemoryDecideUpdate = Schemas["OwnerMemoryDecideUpdate"];
export type OwnerMemoryStatus = Schemas["OwnerMemoryStatus"];
export type OwnerMemoryState = OwnerMemoryStatus["state"];
export type OwnerMemoryTopic = Schemas["OwnerMemoryTopic"];
export type OwnerMemoryCuration = Schemas["OwnerMemoryCuration"];
export type OwnerMemoryBrief = Schemas["OwnerMemoryBrief"];
export type OwnerMemoryFact = Schemas["OwnerMemoryFact"];
export type OwnerMemoryProvenance = Schemas["OwnerMemoryProvenance"];
export type OwnerMemorySource = Schemas["OwnerMemorySource"];
export type OwnerMemoryExclusion = Schemas["OwnerMemoryExclusion"];
export type OwnerMemoryExclusionKind = Schemas["OwnerMemoryExclusionKind"];
export type OwnerMemoryExplicit = Schemas["OwnerMemoryExplicit"];

/** 事实的类型筛选；省略 = 两种都要。 */
export type OwnerMemoryFactType = "world" | "observation";

export const ownerMemoryQk = {
	all: ["owner-memory"] as const,
	settings: ["owner-memory", "settings"] as const,
	status: ["owner-memory", "status"] as const,
	topics: ["owner-memory", "topics"] as const,
	brief: (threadId: string | undefined) =>
		["owner-memory", "brief", threadId ?? ""] as const,
	facts: (
		q: string,
		type: OwnerMemoryFactType | undefined,
		offset: number,
		limit: number,
	) => ["owner-memory", "facts", q, type ?? "", offset, limit] as const,
	sources: (offset: number, limit: number) =>
		["owner-memory", "sources", offset, limit] as const,
	exclusions: ["owner-memory", "exclusions"] as const,
	explicit: ["owner-memory", "explicit"] as const,
};

/** 状态卡要探测 Hindsight，结果 30 秒内视为新鲜；数据变了之后整组失效。 */
const STATUS_STALE_MS = 30_000;

function invalidateOwnerMemory(qc: QueryClient): void {
	void qc.invalidateQueries({ queryKey: ownerMemoryQk.all });
}

// ---------------------------------------------------------------- 设置与判断模型接口

export function useOwnerMemorySettings() {
	return useQuery({
		queryKey: ownerMemoryQk.settings,
		queryFn: () => unwrap(api.GET("/api/v1/owner-memory/settings")),
	});
}

export function usePutOwnerMemorySettings() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (body: Schemas["OwnerMemorySettingsUpdate"]) =>
			unwrap(api.PUT("/api/v1/owner-memory/settings", { body })),
		onSuccess: (data) => {
			qc.setQueryData(ownerMemoryQk.settings, data);
			invalidateOwnerMemory(qc);
		},
	});
}

/** `apiKey` 省略 = 保留原 key，空串 = 清除 key。应答里只有 `hasKey`。 */
export function usePutOwnerMemoryDecideEndpoint() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (body: OwnerMemoryDecideUpdate) =>
			unwrap(api.PUT("/api/v1/owner-memory/decide-endpoint", { body })),
		onSuccess: (data) => qc.setQueryData(ownerMemoryQk.settings, data),
	});
}

/** 删除判断模型接口：召回改按 RRF 顺序给出并标注未经重排。 */
export function useDeleteOwnerMemoryDecideEndpoint() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: () =>
			unwrap(api.DELETE("/api/v1/owner-memory/decide-endpoint")),
		onSuccess: (data) => qc.setQueryData(ownerMemoryQk.settings, data),
	});
}

// ---------------------------------------------------------------- 状态、主题、注入预览

export function useOwnerMemoryStatus() {
	return useQuery({
		queryKey: ownerMemoryQk.status,
		queryFn: () => unwrap(api.GET("/api/v1/owner-memory/status")),
		staleTime: STATUS_STALE_MS,
	});
}

export function useOwnerMemoryTopics() {
	return useQuery({
		queryKey: ownerMemoryQk.topics,
		queryFn: () => unwrap(api.GET("/api/v1/owner-memory/topics")),
	});
}

/** 立即刷新一个主题（202，服务端异步完成；之后重取主题列表）。 */
export function useRefreshOwnerMemoryTopic() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (topicId: string) => {
			await unwrap(
				api.POST("/api/v1/owner-memory/topics/{topicId}/refresh", {
					params: { path: { topicId } },
				}),
			);
		},
		onSuccess: () => {
			void qc.invalidateQueries({ queryKey: ownerMemoryQk.topics });
			void qc.invalidateQueries({ queryKey: ownerMemoryQk.status });
		},
	});
}

export function useCurateOwnerMemoryTopics() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: () => unwrap(api.POST("/api/v1/owner-memory/curate")),
		onSuccess: () => {
			void qc.invalidateQueries({ queryKey: ownerMemoryQk.topics });
			void qc.invalidateQueries({ queryKey: ownerMemoryQk.status });
		},
	});
}

/** 注入预览：`threadId` 给出时取该对话的快照（没有或过期时现生成）；`enabled` 为假时不发请求。 */
export function useOwnerMemoryBrief(
	threadId: string | undefined,
	enabled: boolean,
) {
	return useQuery({
		queryKey: ownerMemoryQk.brief(threadId),
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/owner-memory/brief", {
					params: { query: threadId ? { threadId } : {} },
				}),
			),
	});
}

// ---------------------------------------------------------------- 事实

/**
 * `q` 非空时按召回相关度排序、不分页（`offset` 不生效）。事实读自 bank，助手记忆关闭时服务端拒绝，
 * 所以 `enabled` 为假时不发请求。
 */
export function useOwnerMemoryFacts(
	q: string,
	type: OwnerMemoryFactType | undefined,
	offset: number,
	limit: number,
	enabled: boolean,
) {
	return useQuery({
		queryKey: ownerMemoryQk.facts(q, type, offset, limit),
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/owner-memory/facts", {
					params: {
						query: {
							...(q ? { q } : { offset }),
							...(type ? { type } : {}),
							limit,
						},
					},
				}),
			),
	});
}

/** 让一条 world 事实失效或恢复；失效后全部活跃主题在刷新完成前不进入注入。 */
export function useSetOwnerMemoryFactState() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async ({
			factId,
			body,
		}: {
			factId: string;
			body: Schemas["OwnerMemoryFactStateUpdate"];
		}) => {
			await unwrap(
				api.PUT("/api/v1/owner-memory/facts/{factId}/state", {
					params: { path: { factId } },
					body,
				}),
			);
		},
		onSuccess: () => invalidateOwnerMemory(qc),
	});
}

// ---------------------------------------------------------------- 来源与排除

export function useOwnerMemorySources(offset: number, limit: number) {
	return useQuery({
		queryKey: ownerMemoryQk.sources(offset, limit),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/owner-memory/sources", {
					params: { query: { offset, limit } },
				}),
			),
	});
}

/** 按来源遗忘：删除该对话或线程写入的全部 document；应答是删除数与进入重试队列的数。 */
export function useForgetOwnerMemorySource() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (threadId: string) =>
			unwrap(
				api.DELETE("/api/v1/owner-memory/sources/{id}", {
					params: { path: { id: threadId } },
				}),
			),
		onSuccess: () => invalidateOwnerMemory(qc),
	});
}

export function useOwnerMemoryExclusions(enabled = true) {
	return useQuery({
		queryKey: ownerMemoryQk.exclusions,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/owner-memory/exclusions")),
	});
}

/** 设为不进助手记忆，已写入的随之遗忘。 */
export function usePutOwnerMemoryExclusion() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			kind,
			targetId,
		}: {
			kind: OwnerMemoryExclusionKind;
			targetId: string;
		}) =>
			unwrap(
				api.PUT("/api/v1/owner-memory/exclusions/{kind}/{targetId}", {
					params: { path: { kind, targetId } },
				}),
			),
		onSuccess: () => invalidateOwnerMemory(qc),
	});
}

/** 取消排除：之后有新内容时重新写入。 */
export function useDeleteOwnerMemoryExclusion() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async ({
			kind,
			targetId,
		}: {
			kind: OwnerMemoryExclusionKind;
			targetId: string;
		}) => {
			await unwrap(
				api.DELETE("/api/v1/owner-memory/exclusions/{kind}/{targetId}", {
					params: { path: { kind, targetId } },
				}),
			);
		},
		onSuccess: () => invalidateOwnerMemory(qc),
	});
}

// ---------------------------------------------------------------- 明确记忆

export function useOwnerMemoryExplicit() {
	return useQuery({
		queryKey: ownerMemoryQk.explicit,
		queryFn: () => unwrap(api.GET("/api/v1/owner-memory/explicit")),
	});
}

/** 新建（直接生效）。 */
export function useCreateOwnerMemoryExplicit() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (content: string) =>
			unwrap(api.POST("/api/v1/owner-memory/explicit", { body: { content } })),
		onSuccess: () => invalidateOwnerMemory(qc),
	});
}

/** 修改内容；已生效的随之重写 document，待确认的仍待确认。 */
export function useUpdateOwnerMemoryExplicit() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			explicitId,
			content,
		}: {
			explicitId: string;
			content: string;
		}) =>
			unwrap(
				api.PATCH("/api/v1/owner-memory/explicit/{explicitId}", {
					params: { path: { explicitId } },
					body: { content },
				}),
			),
		onSuccess: () => invalidateOwnerMemory(qc),
	});
}

/** 删除：已生效的连同 document 一起删除；待确认的即拒绝。 */
export function useDeleteOwnerMemoryExplicit() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (explicitId: string) => {
			await unwrap(
				api.DELETE("/api/v1/owner-memory/explicit/{explicitId}", {
					params: { path: { explicitId } },
				}),
			);
		},
		onSuccess: () => invalidateOwnerMemory(qc),
	});
}

/** 批准待确认的明确记忆；`content` 给出时按改写后的内容批准。 */
export function useApproveOwnerMemoryExplicit() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			explicitId,
			content,
		}: {
			explicitId: string;
			content?: string;
		}) =>
			unwrap(
				api.POST("/api/v1/owner-memory/explicit/{explicitId}/approve", {
					params: { path: { explicitId } },
					body: content === undefined ? {} : { content },
				}),
			),
		onSuccess: () => invalidateOwnerMemory(qc),
	});
}
