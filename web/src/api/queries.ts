/** TanStack Query 封装的 REST 读写（键统一在 `qk` 下，便于失效）。 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetchHealth } from "../health";
import { api, encodePathParam, rawFetch, type Schemas, unwrap } from "./client";

export const qk = {
	health: ["healthz"] as const,
	me: ["me"] as const,
	devices: ["devices"] as const,
	projects: ["projects"] as const,
	providers: ["providers"] as const,
	secrets: ["secrets"] as const,
	gitIdentity: ["settings", "git-identity"] as const,
	agentFiles: ["agent-config", "files"] as const,
	agentFile: (path: string) => ["agent-config", "file", path] as const,
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

export function useAgentFiles() {
	return useQuery({
		queryKey: qk.agentFiles,
		queryFn: () => unwrap(api.GET("/api/v1/agent-config/files")),
	});
}

export function useAgentFile(path: string | null) {
	return useQuery({
		queryKey: qk.agentFile(path ?? ""),
		enabled: path !== null,
		queryFn: async () => {
			const res = await rawFetch(
				`/api/v1/agent-config/files/${encodePathParam(path ?? "")}`,
			);
			return res.text();
		},
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
