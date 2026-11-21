/**
 * Boxes（M23）的查询与写入：box 列表 / 详情、活动、21 天用量、app 的 release 与密钥，以及启停、改名、时段、
 * 公开性、重启、回滚、下线、取消部署、密钥增删。
 * 变更成功后让 `boxKeys.all` 失效；别处（orb 里的 agent 部署、时段、助手）的变化经 WS `boxes.changed` 推送，
 * `useRealtimeBoxSync` 据此（以及每次 WS 重新连上时）让 Boxes 查询全部失效。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo } from "react";
import { type RealtimeClient, realtime } from "../realtime/socket";
import { api, type Schemas, unwrap } from "./client";

export type Box = Schemas["Box"];
export type BoxApp = Schemas["BoxApp"];
export type BoxRelease = Schemas["BoxRelease"];
export type BoxSecret = Schemas["BoxSecret"];
export type BoxActivity = Schemas["BoxActivity"];
export type BoxUsage = Schemas["BoxUsage"];
export type BoxSchedule = Schemas["BoxSchedule"];
export type UpdateBoxRequest = Schemas["UpdateBoxRequest"];

export const boxKeys = {
	all: ["boxes"] as const,
	list: ["boxes", "list"] as const,
	detail: (id: string) => ["boxes", "detail", id] as const,
	/** `id` 为 `"all"` 时是全部 box 的活动 */
	activity: (id: string) => ["boxes", "activity", id] as const,
	usage: (id: string) => ["boxes", "usage", id] as const,
	releases: (id: string, app: string) =>
		["boxes", "releases", id, app] as const,
	secrets: (id: string, app: string) => ["boxes", "secrets", id, app] as const,
};

/**
 * Boxes 查询与服务端对账：收到 `boxes.changed`、或 WS（重新）连上时，`["boxes"]` 前缀的查询全部失效。
 * 在调 `realtime.connect()` 的应用壳里调用一次。
 */
export function useRealtimeBoxSync(
	client: Pick<RealtimeClient, "onBoxesChanged"> = realtime,
): void {
	const qc = useQueryClient();
	useEffect(
		() =>
			client.onBoxesChanged(() => {
				void qc.invalidateQueries({ queryKey: boxKeys.all });
			}),
		[client, qc],
	);
}

/** 全部 box（按创建时间）。 */
export function useBoxes(enabled = true) {
	return useQuery({
		queryKey: boxKeys.list,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/boxes")),
		staleTime: 15_000,
	});
}

/** 单台 box；`id` 为 box 的 id 或名字。 */
export function useBox(id: string) {
	return useQuery({
		queryKey: boxKeys.detail(id),
		enabled: Boolean(id),
		queryFn: () =>
			unwrap(api.GET("/api/v1/boxes/{box}", { params: { path: { box: id } } })),
		staleTime: 15_000,
	});
}

/** 活动（新到旧）；`id` 为 `"all"` 时取全部 box 的。 */
export function useBoxActivity(id: string, limit = 50) {
	return useQuery({
		queryKey: boxKeys.activity(id),
		enabled: Boolean(id),
		queryFn: () =>
			unwrap(
				id === "all"
					? api.GET("/api/v1/box-activity", {
							params: { query: { limit } },
						})
					: api.GET("/api/v1/boxes/{box}/activity", {
							params: { path: { box: id }, query: { limit } },
						}),
			),
		staleTime: 15_000,
	});
}

/** 最近 21 个 UTC 自然日的线程数与在线率。 */
export function useBoxUsage(id: string) {
	return useQuery({
		queryKey: boxKeys.usage(id),
		enabled: Boolean(id),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/boxes/{box}/usage", { params: { path: { box: id } } }),
			),
		staleTime: 60_000,
	});
}

export function useBoxReleases(id: string, app: string, enabled = true) {
	return useQuery({
		queryKey: boxKeys.releases(id, app),
		enabled: enabled && Boolean(id && app),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/boxes/{box}/apps/{app}/releases", {
					params: { path: { box: id, app } },
				}),
			),
	});
}

export function useBoxSecrets(id: string, app: string, enabled = true) {
	return useQuery({
		queryKey: boxKeys.secrets(id, app),
		enabled: enabled && Boolean(id && app),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/boxes/{box}/apps/{app}/secrets", {
					params: { path: { box: id, app } },
				}),
			),
	});
}

/** 线程页用：`orbId` 属于哪台 box（不是 box 的 orb、或列表还没到时为 undefined）。 */
export function useBoxOfOrb(orbId: string | undefined, enabled = true) {
	const { data } = useBoxes(enabled && Boolean(orbId));
	return useMemo(
		() => (orbId ? data?.boxes.find((b) => b.orbId === orbId) : undefined),
		[data, orbId],
	);
}

function useInvalidateBoxes() {
	const qc = useQueryClient();
	return () => qc.invalidateQueries({ queryKey: boxKeys.all });
}

export function useCreateBox() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: (body: Schemas["CreateBoxRequest"]) =>
			unwrap(api.POST("/api/v1/boxes", { body })),
		onSuccess: () => invalidate(),
	});
}

export function useUpdateBox() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: ({ id, patch }: { id: string; patch: UpdateBoxRequest }) =>
			unwrap(
				api.PATCH("/api/v1/boxes/{box}", {
					params: { path: { box: id } },
					body: patch,
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useDeleteBox() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.DELETE("/api/v1/boxes/{box}", { params: { path: { box: id } } }),
			),
		onSuccess: () => invalidate(),
	});
}

export function useStartBox() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.POST("/api/v1/boxes/{box}/start", {
					params: { path: { box: id } },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useStopBox() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.POST("/api/v1/boxes/{box}/stop", { params: { path: { box: id } } }),
			),
		onSuccess: () => invalidate(),
	});
}

interface AppRef {
	box: string;
	app: string;
}

export function useUpdateBoxApp() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: ({
			box,
			app,
			public: isPublic,
		}: AppRef & { public: boolean }) =>
			unwrap(
				api.PATCH("/api/v1/boxes/{box}/apps/{app}", {
					params: { path: { box, app } },
					body: { public: isPublic },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useUndeployBoxApp() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: ({ box, app, purgeData }: AppRef & { purgeData?: boolean }) =>
			unwrap(
				api.DELETE("/api/v1/boxes/{box}/apps/{app}", {
					params: {
						path: { box, app },
						query: purgeData ? { purgeData } : {},
					},
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useRestartBoxApp() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: ({ box, app }: AppRef) =>
			unwrap(
				api.POST("/api/v1/boxes/{box}/apps/{app}/restart", {
					params: { path: { box, app } },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useRollbackBoxApp() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: ({ box, app, releaseId }: AppRef & { releaseId?: string }) =>
			unwrap(
				api.POST("/api/v1/boxes/{box}/apps/{app}/rollback", {
					params: { path: { box, app } },
					body: releaseId ? { releaseId } : {},
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useCancelBoxDeploy() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: ({ box, app }: AppRef) =>
			unwrap(
				api.POST("/api/v1/boxes/{box}/apps/{app}/cancel-deploy", {
					params: { path: { box, app } },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function usePutBoxSecret() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: ({
			box,
			app,
			name,
			value,
			secret,
		}: AppRef & { name: string; value: string; secret: boolean }) =>
			unwrap(
				api.PUT("/api/v1/boxes/{box}/apps/{app}/secrets/{name}", {
					params: { path: { box, app, name } },
					body: { value, secret },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useDeleteBoxSecret() {
	const invalidate = useInvalidateBoxes();
	return useMutation({
		mutationFn: ({ box, app, name }: AppRef & { name: string }) =>
			unwrap(
				api.DELETE("/api/v1/boxes/{box}/apps/{app}/secrets/{name}", {
					params: { path: { box, app, name } },
				}),
			),
		onSuccess: () => invalidate(),
	});
}
