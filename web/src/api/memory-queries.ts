/**
 * 内置 Hindsight 记忆的 REST 读写（M16 阶段 3）。键都在 `["memory", …]` 下；改了开关的变更另外让 apply-status 重取，
 * 页顶的 `ApplyStatusBanner` 随即列出待重启的会话。时间戳都是 Unix 秒。
 */
import {
	type QueryClient,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { applyStatusKeys } from "./agent-config-queries";
import { api, type Schemas, unwrap } from "./client";

export type MemorySettings = Schemas["MemorySettings"];
export type MemoryStatus = Schemas["MemoryStatus"];
export type MemoryState = MemoryStatus["state"];
export type MemoryEnvFile = Schemas["MemoryEnvFile"];
export type MemoryProjectToggle = Schemas["MemoryProjectToggle"];
export type MemoryPurpose = Schemas["MemoryPurpose"];
export type MemoryFamily = Schemas["MemoryFamily"];
export type MemoryModels = Schemas["MemoryModels"];
export type MemoryModelChoice = Schemas["MemoryModelChoice"];
export type MemoryModelOption = Schemas["MemoryModelOption"];
export type MemoryModelsUpdate = Schemas["MemoryModelsUpdate"];
export type MemoryEndpointKind = Schemas["MemoryEndpointKind"];
export type MemoryEndpoint = Schemas["MemoryEndpoint"];
export type MemoryEndpoints = Schemas["MemoryEndpoints"];
export type MemoryEndpointUpdate = Schemas["MemoryEndpointUpdate"];
export type MemoryUsage = Schemas["MemoryUsage"];
export type MemoryDocument = Schemas["MemoryDocument"];
export type MemoryDocumentList = Schemas["MemoryDocumentList"];
export type MemorySearchHit = Schemas["MemorySearchHit"];
export type MemoryCandidate = Schemas["MemoryCandidate"];
export type MemoryMentalModel = Schemas["MemoryMentalModel"];
export type MemoryPendingDelete = Schemas["MemoryPendingDelete"];
export type MemoryExport = Schemas["MemoryExport"];

/** 五种用途的固定顺序（与 `getMemoryModels` 的 items 一致）。 */
export const MEMORY_PURPOSES: readonly MemoryPurpose[] = [
	"default",
	"retain",
	"reflect",
	"consolidation",
	"mental_model",
];

/** 文档列表的范围：某个项目，或全局记忆。 */
export type MemoryScope = { projectId: string } | { global: true };

export const memoryQk = {
	all: ["memory"] as const,
	settings: ["memory", "settings"] as const,
	status: ["memory", "status"] as const,
	projects: ["memory", "projects"] as const,
	models: ["memory", "models"] as const,
	endpoints: ["memory", "endpoints"] as const,
	usage: ["memory", "usage"] as const,
	documents: ["memory", "documents"] as const,
	documentPage: (scope: MemoryScope, offset: number, limit: number) =>
		[
			"memory",
			"documents",
			"global" in scope ? "global" : scope.projectId,
			offset,
			limit,
		] as const,
	search: (q: string, projectId: string | undefined) =>
		["memory", "search", projectId ?? "global", q] as const,
	candidates: ["memory", "candidates"] as const,
	mentalModels: (projectId: string | undefined) =>
		["memory", "mental-models", projectId ?? "global"] as const,
	pendingDeletes: ["memory", "pending-deletes"] as const,
	export: (id: string) => ["memory", "export", id] as const,
};

/** 状态卡的探测最多 10 秒，结果 30 秒内视为新鲜；数据变了（写入、批准、删除）之后整组失效。 */
const STATUS_STALE_MS = 30_000;
/** 导出进行中的轮询间隔。 */
const EXPORT_POLL_MS = 1_500;

function invalidateMemory(qc: QueryClient): void {
	void qc.invalidateQueries({ queryKey: memoryQk.all });
}

/** 开关变了：运行中且需重启的线程进入 apply-status 的待重启列表。 */
function invalidateApplyStatus(qc: QueryClient): void {
	void qc.invalidateQueries({ queryKey: applyStatusKeys.status });
}

// ---------------------------------------------------------------- 连接、状态、.env、开关

export function useMemorySettings() {
	return useQuery({
		queryKey: memoryQk.settings,
		queryFn: () => unwrap(api.GET("/api/v1/memory/settings")),
	});
}

export function usePutMemorySettings() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (body: Schemas["MemorySettingsUpdate"]) =>
			unwrap(api.PUT("/api/v1/memory/settings", { body })),
		onSuccess: (data) => {
			qc.setQueryData(memoryQk.settings, data);
			invalidateMemory(qc);
			invalidateApplyStatus(qc);
		},
	});
}

export function useMemoryStatus() {
	return useQuery({
		queryKey: memoryQk.status,
		queryFn: () => unwrap(api.GET("/api/v1/memory/status")),
		staleTime: STATUS_STALE_MS,
	});
}

/** 生成 .env：同时轮换两个密钥，返回值只在这一次里有，不进查询缓存。 */
export function useCreateMemoryEnvFile() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: () => unwrap(api.POST("/api/v1/memory/env-file")),
		onSuccess: () => {
			void qc.invalidateQueries({ queryKey: memoryQk.settings });
			void qc.invalidateQueries({ queryKey: memoryQk.status });
		},
	});
}

export function useMemoryProjects() {
	return useQuery({
		queryKey: memoryQk.projects,
		queryFn: () => unwrap(api.GET("/api/v1/memory/projects")),
	});
}

export function usePutMemoryProject() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
			unwrap(
				api.PUT("/api/v1/memory/projects/{id}", {
					params: { path: { id } },
					body: { enabled },
				}),
			),
		onSuccess: () => {
			void qc.invalidateQueries({ queryKey: memoryQk.projects });
			invalidateApplyStatus(qc);
		},
	});
}

// ---------------------------------------------------------------- 模型与接口

export function useMemoryModels() {
	return useQuery({
		queryKey: memoryQk.models,
		queryFn: () => unwrap(api.GET("/api/v1/memory/models")),
	});
}

export function usePutMemoryModels() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (body: MemoryModelsUpdate) =>
			unwrap(api.PUT("/api/v1/memory/models", { body })),
		onSuccess: (data) => qc.setQueryData(memoryQk.models, data),
	});
}

export function useConfirmMemoryModelsDeployed() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: () => unwrap(api.POST("/api/v1/memory/models/deployed")),
		onSuccess: (data) => qc.setQueryData(memoryQk.models, data),
	});
}

export function useMemoryEndpoints() {
	return useQuery({
		queryKey: memoryQk.endpoints,
		queryFn: () => unwrap(api.GET("/api/v1/memory/endpoints")),
	});
}

export function usePutMemoryEndpoint() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			kind,
			body,
		}: {
			kind: MemoryEndpointKind;
			body: MemoryEndpointUpdate;
		}) =>
			unwrap(
				api.PUT("/api/v1/memory/endpoints/{kind}", {
					params: { path: { kind } },
					body,
				}),
			),
		onSuccess: (data) => qc.setQueryData(memoryQk.endpoints, data),
	});
}

export function useMemoryUsage() {
	return useQuery({
		queryKey: memoryQk.usage,
		queryFn: () => unwrap(api.GET("/api/v1/memory/usage")),
	});
}

// ---------------------------------------------------------------- 浏览、搜索、遗忘

export function useMemoryDocuments(
	scope: MemoryScope | null,
	offset: number,
	limit: number,
) {
	return useQuery({
		queryKey: scope
			? memoryQk.documentPage(scope, offset, limit)
			: memoryQk.documents,
		enabled: scope !== null,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/memory/documents", {
					params: {
						query:
							scope && "global" in scope
								? { global: true, offset, limit }
								: { projectId: scope?.projectId, offset, limit },
					},
				}),
			),
	});
}

export function useDeleteMemoryDocument() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (documentId: string) => {
			await unwrap(
				api.DELETE("/api/v1/memory/documents/{documentId}", {
					params: { path: { documentId } },
				}),
			);
		},
		onSuccess: () => invalidateMemory(qc),
	});
}

/** 召回搜索；`q` 为空时不发请求。`projectId` 省略 = 只搜全局。 */
export function useMemorySearch(q: string, projectId: string | undefined) {
	return useQuery({
		queryKey: memoryQk.search(q, projectId),
		enabled: q.length > 0,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/memory/search", {
					params: { query: projectId ? { q, projectId } : { q } },
				}),
			),
	});
}

export function useForgetMemoryThread() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (threadId: string) =>
			unwrap(
				api.POST("/api/v1/memory/threads/{id}/forget", {
					params: { path: { id: threadId } },
				}),
			),
		onSuccess: () => invalidateMemory(qc),
	});
}

export function useMemoryPendingDeletes(enabled: boolean) {
	return useQuery({
		queryKey: memoryQk.pendingDeletes,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/memory/pending-deletes")),
	});
}

// ---------------------------------------------------------------- 全局记忆与候选

export function useCreateGlobalMemory() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (content: string) =>
			unwrap(api.POST("/api/v1/memory/global", { body: { content } })),
		onSuccess: () => invalidateMemory(qc),
	});
}

export function useUpdateGlobalMemory() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			documentId,
			content,
		}: {
			documentId: string;
			content: string;
		}) =>
			unwrap(
				api.PUT("/api/v1/memory/global/{documentId}", {
					params: { path: { documentId } },
					body: { content },
				}),
			),
		onSuccess: () => invalidateMemory(qc),
	});
}

export function useMemoryCandidates() {
	return useQuery({
		queryKey: memoryQk.candidates,
		queryFn: () => unwrap(api.GET("/api/v1/memory/candidates")),
	});
}

/** 批准候选；`content` 给出时按改写后的内容批准。 */
export function useApproveMemoryCandidate() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			candidateId,
			content,
		}: {
			candidateId: string;
			content?: string;
		}) =>
			unwrap(
				api.POST("/api/v1/memory/candidates/{candidateId}/approve", {
					params: { path: { candidateId } },
					body: content === undefined ? {} : { content },
				}),
			),
		onSuccess: () => invalidateMemory(qc),
	});
}

export function useRejectMemoryCandidate() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (candidateId: string) => {
			await unwrap(
				api.DELETE("/api/v1/memory/candidates/{candidateId}", {
					params: { path: { candidateId } },
				}),
			);
		},
		onSuccess: () => invalidateMemory(qc),
	});
}

// ---------------------------------------------------------------- mental models 与导出

/** `projectId` 省略 = 全局（user-preferences）。 */
export function useMemoryMentalModels(projectId: string | undefined) {
	return useQuery({
		queryKey: memoryQk.mentalModels(projectId),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/memory/mental-models", {
					params: { query: projectId ? { projectId } : {} },
				}),
			),
	});
}

export function useRefreshMemoryMentalModel() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (modelId: string) => {
			await unwrap(
				api.POST("/api/v1/memory/mental-models/{modelId}/refresh", {
					params: { path: { modelId } },
				}),
			);
		},
		onSuccess: () =>
			void qc.invalidateQueries({ queryKey: ["memory", "mental-models"] }),
	});
}

export function useCreateMemoryExport() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: () => unwrap(api.POST("/api/v1/memory/exports")),
		onSuccess: (data) => qc.setQueryData(memoryQk.export(data.id), data),
	});
}

/** 导出进度：`running` 时轮询，完成或失败后停。 */
export function useMemoryExport(id: string | undefined) {
	return useQuery({
		queryKey: memoryQk.export(id ?? ""),
		enabled: id !== undefined,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/memory/exports/{exportId}", {
					params: { path: { exportId: id ?? "" } },
				}),
			),
		refetchInterval: (q) =>
			q.state.data?.state === "running" ? EXPORT_POLL_MS : false,
	});
}

/** 导出归档的下载地址（同源 Cookie 会话，浏览器直接下载）。 */
export function memoryExportDownloadUrl(id: string): string {
	return `/api/v1/memory/exports/${encodeURIComponent(id)}/download`;
}
