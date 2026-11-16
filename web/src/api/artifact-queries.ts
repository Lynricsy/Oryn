/**
 * Artifacts（M14）的 REST 读写。键都在 `["artifacts", …]` 下：任一变更后整组失效，
 * 列表、详情、版本与右面板一起刷新。WS `artifacts.changed`（以及每次 WS 重新连上）由 `useRealtimeArtifactSync`
 * 让整组失效；某个 artifact 的新版本（`artifact.version`）只发给正在看它的连接，查看器自己处理。
 */
import {
	type QueryClient,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { useEffect } from "react";
import { type RealtimeClient, realtime } from "../realtime/socket";
import { api, type Schemas, unwrap } from "./client";

export type Artifact = Schemas["Artifact"];
export type ArtifactDetail = Schemas["ArtifactDetail"];
export type ArtifactVersion = Schemas["ArtifactVersion"];
export type ArtifactFrame = Schemas["ArtifactFrame"];
export type ArtifactCapabilities = Schemas["ArtifactCapabilities"];
export type ArtifactState = Schemas["ArtifactState"];
export type ArtifactThreadActivity = Schemas["ArtifactThreadActivity"];
export type ArtifactCommentThread = Schemas["ArtifactCommentThread"];
export type ArtifactCommentAnchor = Schemas["ArtifactCommentAnchor"];

/** 列表筛选（与 `GET /api/v1/artifacts` 的 query 同名）；缺省 = 不筛。 */
export interface ArtifactFilter {
	deleted?: boolean;
	threadId?: string;
	projectId?: string;
}

export const artifactQk = {
	all: ["artifacts"] as const,
	list: (f: ArtifactFilter) =>
		["artifacts", "list", f.deleted ?? false, f.threadId, f.projectId] as const,
	detail: (id: string, version?: number) =>
		["artifacts", "detail", id, version ?? 0] as const,
	versions: (id: string) => ["artifacts", "versions", id] as const,
	capabilities: ["artifacts", "capabilities"] as const,
	comments: (id: string) => ["artifacts", "comments", id] as const,
};

function invalidateArtifacts(qc: QueryClient) {
	void qc.invalidateQueries({ queryKey: artifactQk.all });
}

/**
 * artifact 查询与服务端对账：收到 `artifacts.changed`、或 WS（重新）连上时，`["artifacts"]` 前缀的查询全部失效。
 * 在调 `realtime.connect()` 的应用壳里调用一次。
 */
export function useRealtimeArtifactSync(
	client: Pick<RealtimeClient, "onArtifactsChanged"> = realtime,
): void {
	const qc = useQueryClient();
	useEffect(
		() => client.onArtifactsChanged(() => invalidateArtifacts(qc)),
		[client, qc],
	);
}

export function useArtifacts(filter: ArtifactFilter = {}, enabled = true) {
	return useQuery({
		queryKey: artifactQk.list(filter),
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/artifacts", {
					params: {
						query: {
							...(filter.deleted ? { deleted: true } : {}),
							...(filter.threadId ? { threadId: filter.threadId } : {}),
							...(filter.projectId ? { projectId: filter.projectId } : {}),
						},
					},
				}),
			),
		select: (r) => r.artifacts,
		staleTime: 10_000,
	});
}

/** 该线程创建或写过版本的 artifact（最近更新的在前）。 */
export function useThreadArtifacts(threadId: string) {
	return useArtifacts({ threadId });
}

/** 详情查询（`version` 缺省 = 最新版本）；钩子与命令式的 `qc.fetchQuery` 共用。 */
export function artifactDetailQuery(id: string, version?: number) {
	return {
		queryKey: artifactQk.detail(id, version),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/artifacts/{id}", {
					params: {
						path: { id },
						query: version ? { version } : {},
					},
				}),
			),
	};
}

/** 详情：`version` 缺省 = 最新版本。 */
export function useArtifact(id: string, version?: number) {
	return useQuery({
		...artifactDetailQuery(id, version),
		// 404（不在了）不重试：查看器直接显示空态
		retry: false,
	});
}

export function useArtifactVersions(id: string, enabled = true) {
	return useQuery({
		queryKey: artifactQk.versions(id),
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/artifacts/{id}/versions", {
					params: { path: { id } },
				}),
			),
		select: (r) => r.versions,
	});
}

export function useArtifactCapabilities() {
	return useQuery({
		queryKey: artifactQk.capabilities,
		queryFn: () => unwrap(api.GET("/api/v1/artifacts/capabilities")),
		staleTime: 60_000,
	});
}

export function useRenameArtifact() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({ id, title }: { id: string; title: string }) =>
			unwrap(
				api.PATCH("/api/v1/artifacts/{id}", {
					params: { path: { id } },
					body: { title },
				}),
			),
		onSuccess: () => invalidateArtifacts(qc),
	});
}

/** 移到「最近删除」。 */
export function useDeleteArtifact() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (id: string) => {
			await unwrap(
				api.DELETE("/api/v1/artifacts/{id}", { params: { path: { id } } }),
			);
		},
		onSuccess: () => invalidateArtifacts(qc),
	});
}

/** 从「最近删除」恢复。 */
export function useUndeleteArtifact() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.POST("/api/v1/artifacts/{id}/restore", {
					params: { path: { id } },
				}),
			),
		onSuccess: () => invalidateArtifacts(qc),
	});
}

/** 永久删除（只对回收站里的生效）。 */
export function usePurgeArtifact() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (id: string) => {
			await unwrap(
				api.DELETE("/api/v1/artifacts/{id}", {
					params: { path: { id }, query: { permanent: true } },
				}),
			);
		},
		onSuccess: () => invalidateArtifacts(qc),
	});
}

/** 恢复版本的结果：成功，或 `baseVersion` 之后来了新版本（409，带服务端状态）。 */
export type RestoreVersionResult =
	| { kind: "restored"; artifact: Artifact }
	| { kind: "conflict"; state: ArtifactState };

export function useRestoreArtifactVersion() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async ({
			id,
			version,
			baseVersion,
		}: {
			id: string;
			version: number;
			baseVersion: number;
		}): Promise<RestoreVersionResult> => {
			const res = await api.POST(
				"/api/v1/artifacts/{id}/versions/{n}/restore",
				{
					params: { path: { id, n: version } },
					body: { baseVersion },
				},
			);
			const err = res.error as Partial<Schemas["ArtifactConflict"]> | undefined;
			if (res.response.status === 409 && err?.state)
				return { kind: "conflict", state: err.state };
			return {
				kind: "restored",
				artifact: await unwrap(Promise.resolve(res)),
			};
		},
		onSuccess: () => invalidateArtifacts(qc),
	});
}

/** 记下「打开过」（总览页「最近」排序与首屏最近项用）。 */
export function useMarkArtifactOpened() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (id: string) => {
			await unwrap(
				api.POST("/api/v1/artifacts/{id}/opened", {
					params: { path: { id } },
				}),
			);
		},
		onSuccess: () =>
			void qc.invalidateQueries({ queryKey: ["artifacts", "list"] }),
	});
}

/**
 * 评论串（旧 → 新）与最新版本号。观看者收到 `artifact.comments`（评论变了）或 `artifact.version`
 * （服务端会在新版本上重新定位锚点）时失效重取；观看登记由查看器负责。
 */
export function useArtifactComments(
	id: string,
	enabled = true,
	client: Pick<RealtimeClient, "onArtifact"> = realtime,
) {
	const qc = useQueryClient();
	useEffect(() => {
		if (!enabled) return;
		return client.onArtifact(id, (msg) => {
			if (msg.type === "artifact.comments" || msg.type === "artifact.version")
				void qc.invalidateQueries({ queryKey: artifactQk.comments(id) });
		});
	}, [client, qc, id, enabled]);
	return useQuery({
		queryKey: artifactQk.comments(id),
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/artifacts/{id}/comments", {
					params: { path: { id } },
				}),
			),
	});
}

function useCommentMutation<V extends { id: string }, R>(
	fn: (v: V) => Promise<R>,
) {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: fn,
		onSuccess: (_r, v) =>
			void qc.invalidateQueries({ queryKey: artifactQk.comments(v.id) }),
	});
}

/** 新建一串评论（作者是用户）。 */
export function useCreateArtifactComment() {
	return useCommentMutation(
		({
			id,
			anchor,
			body,
		}: {
			id: string;
			anchor: ArtifactCommentAnchor;
			body: string;
		}) =>
			unwrap(
				api.POST("/api/v1/artifacts/{id}/comments", {
					params: { path: { id } },
					body: { anchor, body },
				}),
			),
	);
}

/** 用户回复一串评论（服务端同时清掉它的「已发送」）。 */
export function useReplyArtifactComment() {
	return useCommentMutation(
		({ id, threadId, body }: { id: string; threadId: string; body: string }) =>
			unwrap(
				api.POST("/api/v1/artifacts/{id}/comments/{threadId}/replies", {
					params: { path: { id, threadId } },
					body: { body },
				}),
			),
	);
}

/** 标为已解决 / 重新打开。 */
export function useResolveArtifactComment() {
	return useCommentMutation(
		({
			id,
			threadId,
			resolved,
		}: {
			id: string;
			threadId: string;
			resolved: boolean;
		}) =>
			unwrap(
				api.PATCH("/api/v1/artifacts/{id}/comments/{threadId}", {
					params: { path: { id, threadId } },
					body: { resolved },
				}),
			),
	);
}

/**
 * 导入：每个文件以相对路径为字段名放进 `FormData`。`name` 是上传时的文件名或文件夹名（页面没有 `<title>` 时作标题）。
 * 错误码（`too_large`、`too_many`、`no_index`、`bad_canvas`、`bad_name`、`not_utf8`）在抛出的 `ApiError.code` 上。
 */
export function useImportArtifact() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			files,
			name,
			projectId,
		}: {
			files: FormData;
			name?: string;
			projectId?: string;
		}) =>
			unwrap(
				api.POST("/api/v1/artifacts/import", {
					params: {
						query: {
							...(projectId ? { projectId } : {}),
							...(name ? { name } : {}),
						},
					},
					// FormData 原样发出（浏览器自己带 multipart 边界）
					body: files as unknown as Record<string, string>,
					bodySerializer: (b) => b as unknown as FormData,
				}),
			),
		onSuccess: () => invalidateArtifacts(qc),
	});
}

/** 申请一个查看帧（`version` 缺省 = 跟随最新版本）。 */
export function openArtifactFrame(
	id: string,
	version?: number,
	locale?: string,
): Promise<ArtifactFrame> {
	return unwrap(
		api.POST("/api/v1/artifacts/{id}/frames", {
			params: { path: { id } },
			body: {
				...(version ? { version } : {}),
				...(locale ? { locale } : {}),
			},
		}),
	);
}

/** 关闭查看帧；失败（帧已过期、orynd 重启过）无需处理。 */
export async function closeArtifactFrame(token: string): Promise<void> {
	await api
		.DELETE("/api/v1/artifact-frames/{token}", {
			params: { path: { token } },
		})
		.catch(() => undefined);
}

/** 「在浏览器中打开」的一次性地址（令牌在 URL 里，不需要登录态）。 */
export async function openArtifactLink(
	id: string,
	version?: number,
): Promise<string> {
	const r = await unwrap(
		api.POST("/api/v1/artifacts/{id}/open-link", {
			params: { path: { id } },
			body: version ? { version } : {},
		}),
	);
	return r.url;
}

export type ArtifactExportFormat = Schemas["ArtifactExportFormat"];

/** 下载得到的内容与服务端的 `Content-Disposition`（文件名在里面；没有时为 `null`）。 */
export interface ArtifactDownload {
	blob: Blob;
	disposition: string | null;
}

/** 导出某个版本（缺省 = 最新版本）。失败抛 `ApiError`。 */
export async function fetchArtifactExport(
	id: string,
	format: ArtifactExportFormat,
	version?: number,
): Promise<ArtifactDownload> {
	const res = await api.GET("/api/v1/artifacts/{id}/export", {
		params: {
			path: { id },
			query: { format, ...(version ? { version } : {}) },
		},
		parseAs: "blob",
	});
	const blob = await unwrap(Promise.resolve(res));
	return {
		blob,
		disposition: res.response.headers.get("Content-Disposition"),
	};
}

/** 在控制面渲染并取回 PNG（缺省入口文件、1280×800）。渲染不可用时抛 `render_unavailable`。 */
export async function fetchArtifactPng(
	id: string,
	opts: {
		version?: number;
		path?: string;
		width?: number;
		height?: number;
	} = {},
): Promise<ArtifactDownload> {
	const res = await api.GET("/api/v1/artifacts/{id}/render.png", {
		params: { path: { id }, query: opts },
		parseAs: "blob",
	});
	const blob = await unwrap(Promise.resolve(res));
	return {
		blob,
		disposition: res.response.headers.get("Content-Disposition"),
	};
}

/** 卡片缩略图的地址（`<img>` 直接用；没有缩略图时服务端回 404，卡片回落到类型示意图）。 */
export function artifactThumbnailUrl(id: string, version: number): string {
	return `/api/v1/artifacts/${encodeURIComponent(id)}/thumbnail?version=${version}`;
}

export type ArtifactElementEdit = Schemas["ArtifactElementEdit"];

/** 元素直接编辑的结果：落成的新版本，或元素所在文件在 `baseVersion` 之后被改过 / 偏移处已不是该标签（409，带服务端状态）。 */
export type ElementEditResult =
	| { kind: "saved"; version: number }
	| { kind: "conflict"; state: ArtifactState };

/** 直接编辑页面上的一个元素（文字与七个样式属性），落成一个用户版本。其它错误抛 `ApiError`。 */
export async function editArtifactElement(
	id: string,
	body: ArtifactElementEdit,
): Promise<ElementEditResult> {
	const res = await api.POST("/api/v1/artifacts/{id}/element", {
		params: { path: { id } },
		body,
	});
	const err = res.error as Partial<Schemas["ArtifactConflict"]> | undefined;
	if (res.response.status === 409 && err?.state)
		return { kind: "conflict", state: err.state };
	const r = await unwrap(Promise.resolve(res));
	return { kind: "saved", version: r.version };
}

/** 读某个版本里一个文件的原始字节。失败抛 `ApiError`。 */
export async function fetchArtifactFileBytes(
	id: string,
	version: number,
	path: string,
): Promise<Uint8Array> {
	const res = await api.GET("/api/v1/artifacts/{id}/file", {
		params: { path: { id }, query: { version, path } },
		parseAs: "arrayBuffer",
	});
	return new Uint8Array(await unwrap(Promise.resolve(res)));
}
