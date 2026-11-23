/**
 * 项目仓库镜像（M24 阶段 ②）的查询：状态与同步、分支 / 标签、提交与提交详情、diff、目录树，以及文件内容地址。
 * 控制面在后台克隆或拉取，状态变化经 WS `project_repo.changed`（带项目 id）推送；`useRealtimeRepoSync` 据此让该项目的
 * 仓库查询失效，WS 每次重新连上时让全部项目的仓库查询失效。
 */
import {
	useInfiniteQuery,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { useEffect } from "react";
import { type RealtimeClient, realtime } from "../realtime/socket";
import {
	ApiError,
	api,
	encodePathParam,
	type Project,
	type Schemas,
	unwrap,
} from "./client";
import { qk } from "./queries";

export type RepoStatus = Schemas["ProjectRepoStatus"];
export type RepoRef = Schemas["ProjectRepoRef"];
export type RepoRefKind = Schemas["ProjectRepoRefKind"];
export type RepoCommit = Schemas["ProjectRepoCommit"];
export type RepoCommitDetail = Schemas["ProjectRepoCommitDetail"];
export type RepoTree = Schemas["ProjectRepoTree"];
export type RepoTreeEntry = Schemas["ProjectRepoTreeEntry"];

export const repoKeys = {
	all: (projectId: string) => ["projectRepo", projectId] as const,
	status: (projectId: string) =>
		[...repoKeys.all(projectId), "status"] as const,
	refs: (projectId: string, kind: RepoRefKind) =>
		[...repoKeys.all(projectId), "refs", kind] as const,
	commits: (projectId: string, ref?: string, path?: string) =>
		[...repoKeys.all(projectId), "commits", ref ?? "", path ?? ""] as const,
	commit: (projectId: string, sha: string) =>
		[...repoKeys.all(projectId), "commit", sha] as const,
	diff: (projectId: string, sha: string, path: string, oldPath?: string) =>
		[...repoKeys.all(projectId), "diff", sha, path, oldPath ?? ""] as const,
	tree: (
		projectId: string,
		ref: string | undefined,
		path: string,
		lastCommits: boolean,
	) =>
		[...repoKeys.all(projectId), "tree", ref ?? "", path, lastCommits] as const,
};

/**
 * 仓库查询与服务端对账：收到 `project_repo.changed` 时让该项目的仓库查询失效；WS（重新）连上时（null）让全部项目的
 * 仓库查询失效。托管项目（M24 阶段 ③）的推送后 Webhook 最近投递在项目对象里，所以托管项目另让项目列表失效
 * （只对托管项目多一次列表请求，镜像项目每次同步不拉列表）。在调 `realtime.connect()` 的应用壳里调用一次。
 */
export function useRealtimeRepoSync(
	client: Pick<RealtimeClient, "onProjectRepoChanged"> = realtime,
): void {
	const qc = useQueryClient();
	useEffect(
		() =>
			client.onProjectRepoChanged((projectId) => {
				void qc.invalidateQueries({
					queryKey: projectId ? repoKeys.all(projectId) : ["projectRepo"],
				});
				if (!projectId) return;
				const list = qc.getQueryData<{ projects: Project[] }>(qk.projects);
				if (list?.projects.some((p) => p.id === projectId && p.hosted))
					void qc.invalidateQueries({ queryKey: qk.projects });
			}),
		[client, qc],
	);
}

/** 确定性的错误（参数不对、不存在、镜像不可用、过大、没有 git）不重试；其余至多重试 2 次。 */
export function repoRetry(failures: number, error: unknown): boolean {
	return (
		!(
			error instanceof ApiError &&
			[400, 404, 409, 413, 503].includes(error.status)
		) && failures < 2
	);
}

/** 镜像状态；克隆或拉取进行中每 5 秒轮询一次（WS 推送丢失时的兜底）。 */
export function useRepoStatus(projectId: string, enabled = true) {
	return useQuery({
		queryKey: repoKeys.status(projectId),
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/projects/{id}/repo", {
					params: { path: { id: projectId } },
				}),
			),
		staleTime: 10_000,
		retry: repoRetry,
		refetchInterval: (q) => {
			const s = q.state.data;
			return s && (s.state === "cloning" || s.syncing) ? 5_000 : false;
		},
	});
}

/** 立即克隆或拉取；成功后直接用返回的状态替换缓存。 */
export function useSyncRepo(projectId: string) {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: () =>
			unwrap(
				api.POST("/api/v1/projects/{id}/repo/sync", {
					params: { path: { id: projectId } },
				}),
			),
		onSuccess: (status) => {
			qc.setQueryData(repoKeys.status(projectId), status);
		},
	});
}

/** 每页的分支 / 标签数（服务端上限 200）。 */
export const REPO_REFS_PAGE = 50;

/** 分支或标签（按最近提交倒序，默认分支第一）；下一页用 `nextCursor`。 */
export function useRepoRefs(projectId: string, kind: RepoRefKind) {
	return useInfiniteQuery({
		queryKey: repoKeys.refs(projectId, kind),
		initialPageParam: undefined as string | undefined,
		queryFn: ({ pageParam }) =>
			unwrap(
				api.GET("/api/v1/projects/{id}/repo/refs", {
					params: {
						path: { id: projectId },
						query: {
							kind,
							limit: REPO_REFS_PAGE,
							...(pageParam !== undefined ? { cursor: pageParam } : {}),
						},
					},
				}),
			),
		getNextPageParam: (last) => last.nextCursor,
		staleTime: 30_000,
		retry: repoRetry,
	});
}

/** 每页的提交数（服务端上限 100）。 */
export const REPO_COMMITS_PAGE = 30;

/** 提交（新到旧；ref 缺省 = 基线分支，path 过滤到某个文件或目录）；下一页用 `nextCursor`。 */
export function useRepoCommits(
	projectId: string,
	{ ref, path }: { ref?: string; path?: string } = {},
) {
	return useInfiniteQuery({
		queryKey: repoKeys.commits(projectId, ref, path),
		initialPageParam: undefined as string | undefined,
		queryFn: ({ pageParam }) =>
			unwrap(
				api.GET("/api/v1/projects/{id}/repo/commits", {
					params: {
						path: { id: projectId },
						query: {
							limit: REPO_COMMITS_PAGE,
							...(ref ? { ref } : {}),
							...(path ? { path } : {}),
							...(pageParam !== undefined ? { cursor: pageParam } : {}),
						},
					},
				}),
			),
		getNextPageParam: (last) => last.nextCursor,
		staleTime: 30_000,
		retry: repoRetry,
	});
}

/** 提交详情（改动文件与关联线程）。 */
export function useRepoCommit(projectId: string, sha: string) {
	return useQuery({
		queryKey: repoKeys.commit(projectId, sha),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/projects/{id}/repo/commits/{sha}", {
					params: { path: { id: projectId, sha } },
				}),
			),
		staleTime: 60_000,
		retry: repoRetry,
	});
}

/** 空头像列表的新鲜期：与服务端失败缓存（10 分钟）对齐 */
const AVATARS_EMPTY_STALE_MS = 600_000;

/**
 * 提交作者与共同作者在 GitHub 上的头像（服务端按邮箱对账号，查不到为空列表）；`enabled` 为假时不请求
 * （例如人人都是 noreply 邮箱、前端已能推出头像）。查到的缓存一小时；空列表可能是身份未连接或限流（服务端只把失败
 * 记 10 分钟），所以空结果至多 10 分钟就重新问。
 */
export function useRepoCommitAvatars(
	projectId: string,
	sha: string,
	enabled = true,
) {
	return useQuery({
		queryKey: [...repoKeys.commit(projectId, sha), "avatars"],
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/projects/{id}/repo/commits/{sha}/avatars", {
					params: { path: { id: projectId, sha } },
				}),
			),
		staleTime: (q) =>
			q.state.data?.avatars.length ? 3_600_000 : AVATARS_EMPTY_STALE_MS,
		retry: false,
	});
}

/** 提交里一个文件的 diff；提交不可变，缓存永不过期。 */
export function useRepoCommitDiff(
	projectId: string,
	sha: string,
	file: Pick<Schemas["GitFileChange"], "path" | "oldPath">,
	enabled = true,
) {
	return useQuery({
		queryKey: repoKeys.diff(projectId, sha, file.path, file.oldPath),
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/projects/{id}/repo/commits/{sha}/diff", {
					params: {
						path: { id: projectId, sha },
						query: {
							path: file.path,
							...(file.oldPath ? { oldPath: file.oldPath } : {}),
						},
					},
				}),
			),
		staleTime: Number.POSITIVE_INFINITY,
		retry: repoRetry,
	});
}

/** 某个 ref 下的目录或文件（ref 缺省 = 基线分支；path "" = 根）；lastCommits 给目录子项找最后提交。 */
export function useRepoTree(
	projectId: string,
	ref: string | undefined,
	path: string,
	lastCommits = true,
) {
	return useQuery({
		queryKey: repoKeys.tree(projectId, ref, path, lastCommits),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/projects/{id}/repo/tree", {
					params: {
						path: { id: projectId },
						query: { ...(ref ? { ref } : {}), path, lastCommits },
					},
				}),
			),
		staleTime: 30_000,
		retry: repoRetry,
	});
}

/** 能在浏览器里预览的 Git LFS 对象上限：与服务端 lfsInlineMax 一致，超过它只能 `download=1` 下载。 */
export const LFS_PREVIEW_MAX_BYTES = 100 * 1024 * 1024;

/**
 * 文件内容地址（`<img src>`、下载、`FileSource` 读取）；ref 为空时由服务端取基线分支。
 * `download` 时一律作附件下载，超过 {@link LFS_PREVIEW_MAX_BYTES} 的 Git LFS 对象也只能这样取。
 */
export function repoBlobUrl(
	projectId: string,
	ref: string | undefined,
	path: string,
	opts?: { download?: boolean },
): string {
	const q = new URLSearchParams();
	if (ref) q.set("ref", ref);
	q.set("path", path);
	if (opts?.download) q.set("download", "1");
	return `/api/v1/projects/${encodePathParam(projectId)}/repo/blob?${q}`;
}
