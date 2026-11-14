/** TanStack Query 封装的 REST 读写（键统一在 `qk` 下，便于失效）。 */
import {
	type QueryClient,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { fetchHealth } from "../health";
import { acceptOrbReply, orbPushMark } from "../state/stores";
import { api, type Orb, rawFetch, type Schemas, unwrap } from "./client";

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
	serviceLog: (orbId: string, name: string) =>
		["orbs", orbId, "services", name, "log"] as const,
	threads: (archived: boolean) => ["threads", { archived }] as const,
	threadUsage: (id: string) => ["stats", "usage", "thread", id] as const,
	browserProfiles: ["browser-profiles"] as const,
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

/**
 * 单用户设置（git 身份、头像）的新鲜期：转录里每条消息都挂头像，虚拟列表滚动时反复挂载，
 * 默认的 0 会让每次挂载都重新请求。写入方（设置页）成功后直接写回缓存，不靠重新拉取。
 * queryFn 故意不接 `signal`：接了之后最后一个观察者卸载（滚出视口）就会中止在途请求，下次挂载又重发。
 */
const SETTINGS_STALE_MS = 5 * 60_000;

export function useGitIdentity() {
	return useQuery({
		queryKey: qk.gitIdentity,
		queryFn: () => unwrap(api.GET("/api/v1/settings/git-identity")),
		staleTime: SETTINGS_STALE_MS,
	});
}

/** 头像归属：agent 头像（助手回合）与用户头像（用户回合、侧栏页脚）同一套契约。 */
export type AvatarOwner = "agent" | "user";

const avatarIdentityKey = (who: AvatarOwner) =>
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
		staleTime: SETTINGS_STALE_MS,
	});
}

/**
 * 整体替换某个身份的头像（`{}` = 恢复默认）并写回缓存。所有头像写入都走这里：
 * 写入前后都取消在途的读取，否则 PUT 之前发出的旧 GET 后到会把缓存还原成旧头像。
 */
export async function saveAvatarIdentity(
	qc: QueryClient,
	who: AvatarOwner,
	body: Schemas["AvatarIdentity"],
): Promise<void> {
	const queryKey = avatarIdentityKey(who);
	await qc.cancelQueries({ queryKey });
	const saved = await unwrap(
		who === "agent"
			? api.PUT("/api/v1/settings/agent-identity", { body })
			: api.PUT("/api/v1/settings/user-identity", { body }),
	);
	await qc.cancelQueries({ queryKey });
	qc.setQueryData(queryKey, saved);
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

// ---------------------------------------------------------------- 受管服务与 portal（M4）
// 列表不另行请求：`orb.services` / `orb.portals` 随 WS `orb` 消息到达；以下变更成功后控制面同样经 `orb` 消息推送新状态。

export type ServiceAction = "start" | "stop" | "restart";

/** 启动 / 停止 / 重启受管服务（orb 不可用时控制面会唤醒并等待）。 */
export function useServiceAction(orbId: string) {
	return useMutation({
		mutationFn: ({ name, action }: { name: string; action: ServiceAction }) => {
			const params = { params: { path: { id: orbId, name } } };
			return unwrap(
				action === "start"
					? api.POST("/api/v1/orbs/{id}/services/{name}/start", params)
					: action === "stop"
						? api.POST("/api/v1/orbs/{id}/services/{name}/stop", params)
						: api.POST("/api/v1/orbs/{id}/services/{name}/restart", params),
			);
		},
	});
}

/** 限时公开某个端口的 portal；已公开时即续期（到期时间从现在重新算）。 */
export function useSetPortalShare(orbId: string) {
	return useMutation({
		mutationFn: ({
			port,
			duration,
		}: {
			port: number;
			duration: Schemas["PortalShareDuration"];
		}) =>
			unwrap(
				api.PUT("/api/v1/orbs/{id}/portals/{port}/share", {
					params: { path: { id: orbId, port } },
					body: { duration },
				}),
			),
	});
}

/** 终止公开（立即断开匿名访客）。 */
export function useEndPortalShare(orbId: string) {
	return useMutation({
		mutationFn: async ({ port }: { port: number }) => {
			await unwrap(
				api.DELETE("/api/v1/orbs/{id}/portals/{port}/share", {
					params: { path: { id: orbId, port } },
				}),
			);
		},
	});
}

/** 服务日志末尾（纯文本）；`bg` = 后台轮询：orb 不可用时直接失败，不唤醒、不计用户交互。 */
export async function fetchServiceLog(
	orbId: string,
	name: string,
	{ bg = false, signal }: { bg?: boolean; signal?: AbortSignal } = {},
): Promise<string> {
	const res = await rawFetch(
		`/api/v1/orbs/${encodeURIComponent(orbId)}/services/${encodeURIComponent(name)}/log${bg ? "?bg=1" : ""}`,
		{ signal },
	);
	return res.text();
}

// ---------------------------------------------------------------- 浏览器 Profile 与 Desktop（M8）
// 桌面状态不另行请求：`orb.desktop` 随 WS `orb` 消息到达；下面的 orb 变更成功后把返回的 orb 写进索引（不等推送），
// 并使 Profile 列表失效（持有者 / 修订可能变了）。

export function useBrowserProfiles() {
	return useQuery({
		queryKey: qk.browserProfiles,
		queryFn: () => unwrap(api.GET("/api/v1/browser-profiles")),
		staleTime: 10_000,
	});
}

function useProfilesInvalidate() {
	const qc = useQueryClient();
	return () => qc.invalidateQueries({ queryKey: qk.browserProfiles });
}

export function useCreateBrowserProfile() {
	const invalidate = useProfilesInvalidate();
	return useMutation({
		mutationFn: (name: string) =>
			unwrap(api.POST("/api/v1/browser-profiles", { body: { name } })),
		onSuccess: invalidate,
	});
}

export function useRenameBrowserProfile() {
	const invalidate = useProfilesInvalidate();
	return useMutation({
		mutationFn: ({ id, name }: { id: string; name: string }) =>
			unwrap(
				api.PATCH("/api/v1/browser-profiles/{id}", {
					params: { path: { id } },
					body: { name },
				}),
			),
		onSuccess: invalidate,
	});
}

export function useDeleteBrowserProfile() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (id: string) => {
			await unwrap(
				api.DELETE("/api/v1/browser-profiles/{id}", {
					params: { path: { id } },
				}),
			);
		},
		// 删除会清掉引用它的项目默认值
		onSuccess: () =>
			Promise.all([
				qc.invalidateQueries({ queryKey: qk.browserProfiles }),
				qc.invalidateQueries({ queryKey: qk.projects }),
			]),
	});
}

/** 发 orb 变更：应答里的 orb 只在请求在途期间没有新的 WS 推送时写入索引（见 `acceptOrbReply`）。 */
async function mutateOrb(
	orbId: string,
	send: () => Promise<Orb>,
): Promise<Orb> {
	const mark = orbPushMark(orbId);
	const orb = await send();
	acceptOrbReply(orb, mark);
	return orb;
}

/** 为 orb 选用 / 切换 / 解除 Profile（`profileId` 空串 = 解除）；被占用时抛 409 `profile_in_use`（见 `profileConflictOf`）。 */
export function useSetOrbDesktopProfile(orbId: string) {
	const invalidate = useProfilesInvalidate();
	return useMutation({
		mutationFn: (body: Schemas["OrbDesktopProfilePut"]) =>
			mutateOrb(orbId, () =>
				unwrap(
					api.PUT("/api/v1/orbs/{id}/desktop/profile", {
						params: { path: { id: orbId } },
						body,
					}),
				),
			),
		onSettled: invalidate,
	});
}

/** 立即保存（活体保存，浏览器不关闭）。 */
export function useSaveOrbDesktopProfile(orbId: string) {
	const invalidate = useProfilesInvalidate();
	return useMutation({
		mutationFn: () =>
			unwrap(
				api.POST("/api/v1/orbs/{id}/desktop/profile/save", {
					params: { path: { id: orbId } },
				}),
			),
		onSettled: invalidate,
	});
}

/** 启动（`true`）/ 关闭（`false`）桌面上的浏览器。 */
export function useOrbBrowser(orbId: string) {
	return useMutation({
		mutationFn: (run: boolean) => {
			const params = { params: { path: { id: orbId } } };
			return mutateOrb(orbId, () =>
				unwrap(
					run
						? api.POST("/api/v1/orbs/{id}/desktop/browser/start", params)
						: api.POST("/api/v1/orbs/{id}/desktop/browser/stop", params),
				),
			);
		},
	});
}
