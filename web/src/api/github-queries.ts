/**
 * GitHub 集成（M14 / M15）的查询与写入：连接状态（OAuth App、两个身份槽位、自动化身份、最近投递；M14 的 App 与安装仍在）、
 * 仓库 hook、写操作审计、issue / 发布任务、触发规则预设。
 * 变更成功后让 `githubKeys.all` 失效；别处（回调页、另一台设备、webhook）的变化经 WS `github.changed` 推送，
 * `useRealtimeGitHubSync` 据此（以及每次 WS 重新连上时）让 GitHub 查询与项目列表失效（项目的绑定状态随之变化）。
 */
import {
	keepPreviousData,
	useInfiniteQuery,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { useEffect } from "react";
import { type RealtimeClient, realtime } from "../realtime/socket";
import { api, type Schemas, unwrap } from "./client";
import { qk } from "./queries";

export type GitHubStatus = Schemas["GitHubStatus"];
export type GitHubIdentity = Schemas["GitHubIdentity"];
export type GitHubIdentitySlot = Schemas["GitHubIdentitySlot"];
export type GitHubInstallation = Schemas["GitHubInstallation"];
export type GitHubManifestRequest = Schemas["GitHubManifestRequest"];
export type GitHubTrigger = Schemas["GitHubTrigger"];
export type GitHubTriggerInput = Schemas["GitHubTriggerInput"];
export type GitHubTriggerPatch = Schemas["GitHubTriggerPatch"];
export type GitHubOAuthAppInput = Schemas["GitHubOAuthAppInput"];
export type GitHubHook = Schemas["GitHubHook"];
export type GitHubHookStatus = Schemas["GitHubHookStatus"];
export type GitHubAuditEntry = Schemas["GitHubAuditEntry"];
export type GitHubTask = Schemas["GitHubTask"];
export type GitHubPreset = Schemas["GitHubPreset"];
export type GitHubGates = Schemas["GitHubGates"];

/** 审计列表的过滤条件（都可省略）。 */
export interface GitHubAuditFilter {
	thread?: string;
	/** `owner/repo` */
	repo?: string;
}

export const githubKeys = {
	all: ["github"] as const,
	status: ["github", "status"] as const,
	repos: (q: string) => ["github", "repos", q] as const,
	triggers: (projectId: string) => ["github", "triggers", projectId] as const,
	refs: (projectId: string, q: string) =>
		["github", "refs", projectId, q] as const,
	hooks: ["github", "hooks"] as const,
	audit: (f: GitHubAuditFilter) =>
		["github", "audit", f.thread ?? "", f.repo ?? ""] as const,
	tasks: ["github", "tasks"] as const,
	presets: ["github", "presets"] as const,
};

/**
 * GitHub 查询与服务端对账：收到 `github.changed`、或 WS（重新）连上时，`["github"]` 前缀的查询与项目列表全部失效。
 * 在调 `realtime.connect()` 的应用壳里调用一次。
 */
export function useRealtimeGitHubSync(
	client: Pick<RealtimeClient, "onGitHubChanged"> = realtime,
): void {
	const qc = useQueryClient();
	useEffect(
		() =>
			client.onGitHubChanged(() => {
				void qc.invalidateQueries({ queryKey: githubKeys.all });
				void qc.invalidateQueries({ queryKey: qk.projects });
			}),
		[client, qc],
	);
}

export function useGitHubStatus(enabled = true) {
	return useQuery({
		queryKey: githubKeys.status,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/github")),
		staleTime: 30_000,
		refetchOnWindowFocus: true,
	});
}

/** 我的账号经 App 可访问的仓库（按最近推送倒序，至多 50 个）；`q` 按 `owner/repo` 子串过滤。 */
export function useGitHubRepos(q: string, enabled = true) {
	return useQuery({
		queryKey: githubKeys.repos(q),
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/github/repos", {
					params: { query: q ? { q } : {} },
				}),
			),
		staleTime: 60_000,
		// 改搜索词时保留上一批结果，不闪成空列表
		placeholderData: keepPreviousData,
	});
}

function useInvalidateGitHub() {
	const qc = useQueryClient();
	return () => qc.invalidateQueries({ queryKey: githubKeys.all });
}

/** 开始按 manifest 创建 App；返回中转页地址，调用方整页跳转过去。 */
export function useBeginGitHubApp() {
	const invalidate = useInvalidateGitHub();
	return useMutation({
		mutationFn: (body: GitHubManifestRequest) =>
			unwrap(api.POST("/api/v1/github/app/manifest", { body })),
		onSuccess: () => invalidate(),
	});
}

export function useDeleteGitHubApp() {
	const invalidate = useInvalidateGitHub();
	return useMutation({
		mutationFn: () => unwrap(api.DELETE("/api/v1/github/app")),
		onSuccess: () => invalidate(),
	});
}

/** 开始连接某个身份槽位；返回 GitHub 授权地址（10 分钟内有效）。 */
export function useAuthorizeGitHubIdentity() {
	const invalidate = useInvalidateGitHub();
	return useMutation({
		mutationFn: (slot: GitHubIdentitySlot) =>
			unwrap(
				api.POST("/api/v1/github/identities/{slot}/authorize", {
					params: { path: { slot } },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useDisconnectGitHubIdentity() {
	const invalidate = useInvalidateGitHub();
	return useMutation({
		mutationFn: (slot: GitHubIdentitySlot) =>
			unwrap(
				api.DELETE("/api/v1/github/identities/{slot}", {
					params: { path: { slot } },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useSetGitHubAutomation() {
	const invalidate = useInvalidateGitHub();
	return useMutation({
		mutationFn: (automationIdentity: GitHubIdentitySlot) =>
			unwrap(
				api.PUT("/api/v1/github/settings", { body: { automationIdentity } }),
			),
		onSuccess: () => invalidate(),
	});
}

export function useSyncGitHubInstallations() {
	const invalidate = useInvalidateGitHub();
	return useMutation({
		mutationFn: () => unwrap(api.POST("/api/v1/github/installations/sync")),
		onSuccess: () => invalidate(),
	});
}

/** 保存 OAuth App 凭据与 webhook 基址（M15）；`clientSecret` 省略 = 沿用已存的值。 */
export function useSetGitHubOAuthApp() {
	const invalidate = useInvalidateGitHub();
	return useMutation({
		mutationFn: (body: GitHubOAuthAppInput) =>
			unwrap(api.PUT("/api/v1/github/oauth-app", { body })),
		onSuccess: () => invalidate(),
	});
}

/** 删除 OAuth App 配置（M15）：服务端先删掉 Oryn 建的全部仓库 hook，再断开两个身份。 */
export function useDeleteGitHubOAuthApp() {
	const invalidate = useInvalidateGitHub();
	return useMutation({
		mutationFn: () => unwrap(api.DELETE("/api/v1/github/oauth-app")),
		onSuccess: () => invalidate(),
	});
}

/** 已绑定仓库上的 webhook 状态（M15），按仓库一行。 */
export function useGitHubHooks(enabled = true) {
	return useQuery({
		queryKey: githubKeys.hooks,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/github/hooks")),
		staleTime: 30_000,
	});
}

/** 每页的审计条数（服务端上限 200）。 */
export const AUDIT_PAGE = 50;

/** GitHub 写操作审计（M15，按时间倒序）；下一页用上一页的 `nextBefore` 作 `before`。 */
export function useGitHubAudit(filter: GitHubAuditFilter, enabled = true) {
	return useInfiniteQuery({
		queryKey: githubKeys.audit(filter),
		enabled,
		initialPageParam: undefined as number | undefined,
		queryFn: ({ pageParam }) =>
			unwrap(
				api.GET("/api/v1/github/audit", {
					params: {
						query: {
							limit: AUDIT_PAGE,
							...(filter.thread ? { thread: filter.thread } : {}),
							...(filter.repo ? { repo: filter.repo } : {}),
							...(pageParam !== undefined ? { before: pageParam } : {}),
						},
					},
				}),
			),
		getNextPageParam: (last) => last.nextBefore,
		staleTime: 30_000,
	});
}

/** issue 线程与发布哨兵的任务状态（M15）。 */
export function useGitHubTasks(enabled = true) {
	return useQuery({
		queryKey: githubKeys.tasks,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/github/tasks")),
		staleTime: 30_000,
	});
}

/** 触发规则预设（M15，robomp 分诊 / PR 评审 / 发布哨兵）；内容随版本固定，不需要常刷新。 */
export function useGitHubPresets(enabled = true) {
	return useQuery({
		queryKey: githubKeys.presets,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/github/presets")),
		staleTime: 5 * 60_000,
	});
}

/** 保存项目的推送 / 开 PR 门禁（M15，`PATCH projects/{id}` 的 `githubGates`）；项目列表随之失效。 */
export function useSetProjectGates(projectId: string) {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (githubGates: GitHubGates) =>
			unwrap(
				api.PATCH("/api/v1/projects/{id}", {
					params: { path: { id: projectId } },
					body: { githubGates },
				}),
			),
		onSuccess: () => qc.invalidateQueries({ queryKey: qk.projects }),
	});
}

/** 组织批准 OAuth App 的申请页（R4）：`<web_url>/settings/connections/applications/<client_id>`。 */
export function githubOrgApprovalUrl(clientId: string): string {
	return `https://github.com/settings/connections/applications/${encodeURIComponent(clientId)}`;
}

/** `sso_required` 的错误说明里带的 SSO 授权地址（取自 `X-GitHub-SSO: required; url=…`）；没有时为 undefined。 */
export function githubSsoUrl(error: string | undefined): string | undefined {
	return error?.match(/https:\/\/\S+/)?.[0];
}

/** 项目的 GitHub 触发规则。 */
export function useGitHubTriggers(projectId: string, enabled = true) {
	return useQuery({
		queryKey: githubKeys.triggers(projectId),
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/projects/{id}/github/triggers", {
					params: { path: { id: projectId } },
				}),
			),
		staleTime: 30_000,
	});
}

function useInvalidateTriggers(projectId: string) {
	const qc = useQueryClient();
	return () =>
		qc.invalidateQueries({ queryKey: githubKeys.triggers(projectId) });
}

export function useCreateGitHubTrigger(projectId: string) {
	const invalidate = useInvalidateTriggers(projectId);
	return useMutation({
		mutationFn: (body: GitHubTriggerInput) =>
			unwrap(
				api.POST("/api/v1/projects/{id}/github/triggers", {
					params: { path: { id: projectId } },
					body,
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useUpdateGitHubTrigger(projectId: string) {
	const invalidate = useInvalidateTriggers(projectId);
	return useMutation({
		mutationFn: ({ id, patch }: { id: string; patch: GitHubTriggerPatch }) =>
			unwrap(
				api.PATCH("/api/v1/github/triggers/{id}", {
					params: { path: { id } },
					body: patch,
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useDeleteGitHubTrigger(projectId: string) {
	const invalidate = useInvalidateTriggers(projectId);
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.DELETE("/api/v1/github/triggers/{id}", {
					params: { path: { id } },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

/** 暂停 / 恢复某个关联 PR 的事件回流（M14）；线程随 `index.patch` 更新，不需要失效查询。 */
export function setThreadPullListening(
	threadId: string,
	number: number,
	listening: boolean,
) {
	return unwrap(
		api.PUT("/api/v1/threads/{id}/github/pulls/{number}", {
			params: { path: { id: threadId, number } },
			body: { listening },
		}),
	);
}

/** 取消线程与 PR 的关联（服务端留墓碑，不会自动重连）。 */
export function unlinkThreadPull(threadId: string, number: number) {
	return unwrap(
		api.DELETE("/api/v1/threads/{id}/github/pulls/{number}", {
			params: { path: { id: threadId, number } },
		}),
	);
}

/** 输入框 `#` 引用的候选（M14）：已绑定项目仓库的 issue / PR；`q` 为空时是最近更新的打开项，纯数字时只查该编号。 */
export function useGitHubRefs(projectId: string, q: string, enabled: boolean) {
	return useQuery({
		queryKey: githubKeys.refs(projectId, q),
		enabled: enabled && projectId !== "",
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/projects/{id}/github/refs", {
					params: { path: { id: projectId }, query: q ? { q } : {} },
				}),
			),
		staleTime: 15_000,
	});
}

/** 把一个 issue / PR 渲染成 Markdown 附件（未绑定线程），供本条消息带上。 */
export function attachGitHubRef(projectId: string, number: number) {
	return unwrap(
		api.POST("/api/v1/projects/{id}/github/refs/{number}/attachment", {
			params: { path: { id: projectId, number } },
		}),
	);
}
