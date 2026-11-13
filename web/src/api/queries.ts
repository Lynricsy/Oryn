/** TanStack Query 封装的 REST 读写（键统一在 `qk` 下，便于失效）。 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetchHealth } from "../health";
import { api, type Schemas, unwrap } from "./client";

export const qk = {
	health: ["healthz"] as const,
	me: ["me"] as const,
	devices: ["devices"] as const,
	projects: ["projects"] as const,
	providers: ["providers"] as const,
	secrets: ["secrets"] as const,
	gitIdentity: ["settings", "git-identity"] as const,
	agentIdentity: ["settings", "agent-identity"] as const,
	userIdentity: ["settings", "user-identity"] as const,
	orbs: ["orbs"] as const,
	orb: (id: string) => ["orbs", id] as const,
	orbMetrics: (id: string) => ["orbs", id, "metrics"] as const,
	setupRunLog: (orbId: string, runId: string) =>
		["orbs", orbId, "setup-runs", runId, "log"] as const,
	threads: (archived: boolean) => ["threads", { archived }] as const,
	threadUsage: (id: string) => ["stats", "usage", "thread", id] as const,
};

export function useHealth() {
	return useQuery({
		queryKey: qk.health,
		queryFn: ({ signal }) => fetchHealth(signal),
		staleTime: 60_000,
	});
}

export function useMe() {
	return useQuery({
		queryKey: qk.me,
		queryFn: () => unwrap(api.GET("/api/v1/me")),
		retry: false,
	});
}

export function useDevices() {
	return useQuery({
		queryKey: qk.devices,
		queryFn: () => unwrap(api.GET("/api/v1/devices")),
	});
}

export function useProjects() {
	return useQuery({
		queryKey: qk.projects,
		queryFn: () => unwrap(api.GET("/api/v1/projects")),
		staleTime: 30_000,
	});
}

export function useGitIdentity() {
	return useQuery({
		queryKey: qk.gitIdentity,
		queryFn: () => unwrap(api.GET("/api/v1/settings/git-identity")),
	});
}

/** 头像归属：agent 头像（助手回合）与用户头像（用户回合、侧栏页脚）同一套契约。 */
export type AvatarOwner = "agent" | "user";

export const avatarIdentityKey = (who: AvatarOwner) =>
	who === "agent" ? qk.agentIdentity : qk.userIdentity;

/** 读取某个身份的头像（单用户设置）。 */
export function useAvatarIdentity(who: AvatarOwner) {
	return useQuery({
		queryKey: avatarIdentityKey(who),
		queryFn: () =>
			unwrap(
				who === "agent"
					? api.GET("/api/v1/settings/agent-identity")
					: api.GET("/api/v1/settings/user-identity"),
			),
		staleTime: 5 * 60_000,
	});
}

/** 整体替换某个身份的头像：`{}` = 恢复默认。 */
export function putAvatarIdentity(
	who: AvatarOwner,
	body: Schemas["AvatarIdentity"],
): Promise<Schemas["AvatarIdentity"]> {
	return unwrap(
		who === "agent"
			? api.PUT("/api/v1/settings/agent-identity", { body })
			: api.PUT("/api/v1/settings/user-identity", { body }),
	);
}

/** agent 头像；助手回合头像与外观设置用。 */
export const useAgentIdentity = () => useAvatarIdentity("agent");

/** 用户头像；用户回合头像、侧栏页脚与外观设置用。 */
export const useUserIdentity = () => useAvatarIdentity("user");

export function useProviders() {
	return useQuery({
		queryKey: qk.providers,
		queryFn: () => unwrap(api.GET("/api/v1/providers")),
		staleTime: 15_000,
	});
}

export function useSecrets() {
	return useQuery({
		queryKey: qk.secrets,
		queryFn: () => unwrap(api.GET("/api/v1/secrets")),
	});
}

export function useOrbs() {
	return useQuery({
		queryKey: qk.orbs,
		queryFn: () => unwrap(api.GET("/api/v1/orbs")),
		staleTime: 10_000,
	});
}

/** 最近 30 分钟的 orb 采样（30s 步长），信息面板迷你曲线用。 */
export function useOrbMetrics(orbId: string | undefined) {
	return useQuery({
		queryKey: qk.orbMetrics(orbId ?? ""),
		enabled: Boolean(orbId),
		refetchInterval: 30_000,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/orbs/{id}/metrics", {
					params: {
						path: { id: orbId ?? "" },
						query: { from: Date.now() - 30 * 60_000, step: 30 },
					},
				}),
			),
	});
}

export function useArchivedThreads(enabled: boolean) {
	return useQuery({
		queryKey: qk.threads(true),
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/threads", { params: { query: { archived: true } } }),
			),
	});
}

/** 本线程的用量聚合（含子 agent 成本），状态栏用。 */
export function useThreadUsage(threadId: string, since: number | undefined) {
	return useQuery({
		queryKey: qk.threadUsage(threadId),
		enabled: since !== undefined,
		refetchInterval: 60_000,
		queryFn: async () => {
			const stats = await unwrap(
				api.GET("/api/v1/stats/usage", {
					params: { query: { groupBy: "thread", from: since } },
				}),
			);
			return stats.groups.find((g) => g.key === threadId) ?? null;
		},
	});
}

export function usePatchThread() {
	return useMutation({
		mutationFn: ({
			id,
			patch,
		}: {
			id: string;
			patch: Schemas["ThreadPatch"];
		}) =>
			unwrap(
				api.PATCH("/api/v1/threads/{id}", {
					params: { path: { id } },
					body: patch,
				}),
			),
	});
}

export function useInvalidate() {
	const qc = useQueryClient();
	return (key: readonly unknown[]) => qc.invalidateQueries({ queryKey: key });
}
