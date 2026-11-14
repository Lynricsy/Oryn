/**
 * 统计接口（M10）：`stats/*` 与 `orbs/{id}/timeline` 的查询封装。窗口与筛选都进 `queryKey`；页面打开期间每 60 秒轮询一次。
 * 这些接口同时声明了 `text/csv`（`format=csv`），openapi-fetch 给出的 data 是「JSON 或 string」：不传 format 时总是 JSON，
 * 拿到 string 视为协议错误。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

const POLL_MS = 60_000;

export type UsageStats = Schemas["UsageStats"];
export type UsageStat = Schemas["UsageStat"];
export type ToolStats = Schemas["ToolStats"];
export type OrbStats = Schemas["OrbStats"];
export type OrbStat = Schemas["OrbStat"];
export type OutputStats = Schemas["OutputStats"];
export type OutputStat = Schemas["OutputStat"];
export type BudgetStatus = Schemas["BudgetStatus"];
export type Budget = Schemas["Budget"];

/** 公共窗口与筛选（未设的筛选省略）。 */
export interface StatsScope {
	from: number;
	to: number;
	tz: string;
	project?: string;
	space?: string;
	thread?: string;
	model?: string;
}

export const statsKeys = {
	all: ["stats"] as const,
	meta: ["stats", "meta"] as const,
	usage: (q: object) => ["stats", "usage", q] as const,
	tools: (q: object) => ["stats", "tools", q] as const,
	orbs: (q: object) => ["stats", "orbs", q] as const,
	output: (q: object) => ["stats", "output", q] as const,
	timeline: (id: string, from: number, to: number) =>
		["orbs", id, "timeline", from, to] as const,
	orbMetrics: (id: string, from: number, to: number) =>
		["orbs", id, "metrics", from, to] as const,
	setupRuns: (id: string) => ["orbs", id, "setup-runs"] as const,
	budget: ["stats", "budget"] as const,
};

function json<T>(data: T | string): T {
	if (typeof data === "string") throw new Error("unexpected text/csv response");
	return data;
}

export function useStatsMeta() {
	return useQuery({
		queryKey: statsKeys.meta,
		refetchInterval: POLL_MS,
		queryFn: () => unwrap(api.GET("/api/v1/stats/meta")),
	});
}

export type UsageQuery = StatsScope & {
	groupBy: Schemas["UsageGroupBy"];
	series?: Schemas["StatsGrain"];
	limit?: number;
};

export function useUsageStats(q: UsageQuery, enabled = true) {
	return useQuery({
		queryKey: statsKeys.usage(q),
		enabled,
		refetchInterval: POLL_MS,
		queryFn: async () =>
			json(
				await unwrap(api.GET("/api/v1/stats/usage", { params: { query: q } })),
			),
	});
}

export type ToolQuery = Omit<StatsScope, "model"> & {
	series?: Schemas["StatsGrain"];
	limit?: number;
};

export function useToolStats(q: ToolQuery, enabled = true) {
	return useQuery({
		queryKey: statsKeys.tools(q),
		enabled,
		refetchInterval: POLL_MS,
		queryFn: async () =>
			json(
				await unwrap(api.GET("/api/v1/stats/tools", { params: { query: q } })),
			),
	});
}

export type OrbQuery = Pick<StatsScope, "from" | "to" | "tz" | "project"> & {
	groupBy: Schemas["OrbGroupBy"];
	orb?: string;
};

export function useOrbStats(q: OrbQuery, enabled = true) {
	return useQuery({
		queryKey: statsKeys.orbs(q),
		enabled,
		refetchInterval: POLL_MS,
		queryFn: async () =>
			json(
				await unwrap(api.GET("/api/v1/stats/orbs", { params: { query: q } })),
			),
	});
}

/**
 * orb 抽屉的查询窗口：键里是所选窗口 `[from, to)`（稳定，不随渲染变化）；实际请求的终点在每次取数（含每 60 秒的
 * 轮询）时才算成 `min(to, 现在)`。今天 / 近 N 天的 `to` 是本地次日零点，轮询因此逐轮推进；历史窗口的终点不越过 `to`。
 */
export function useOrbTimeline(orbId: string, from: number, to: number) {
	return useQuery({
		queryKey: statsKeys.timeline(orbId, from, to),
		refetchInterval: POLL_MS,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/orbs/{id}/timeline", {
					params: {
						path: { id: orbId },
						query: { from, to: Math.min(to, Date.now()) },
					},
				}),
			),
	});
}

/** 抽屉里的 CPU / 内存曲线步长：`max(15, 15 × ceil(窗口秒数 / 600 / 15))`，约 600 个点。 */
export function metricsStep(from: number, to: number): number {
	return Math.max(15, 15 * Math.ceil((to - from) / 1000 / 600 / 15));
}

export function useOrbMetricsRange(orbId: string, from: number, to: number) {
	return useQuery({
		queryKey: statsKeys.orbMetrics(orbId, from, to),
		refetchInterval: POLL_MS,
		queryFn: () => {
			const end = Math.min(to, Date.now());
			return unwrap(
				api.GET("/api/v1/orbs/{id}/metrics", {
					params: {
						path: { id: orbId },
						query: { from, to: end, step: metricsStep(from, end) },
					},
				}),
			);
		},
	});
}

export function useOrbSetupRuns(orbId: string) {
	return useQuery({
		queryKey: statsKeys.setupRuns(orbId),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/orbs/{id}/setup-runs", {
					params: { path: { id: orbId } },
				}),
			),
	});
}

export type OutputQuery = Pick<
	StatsScope,
	"from" | "to" | "tz" | "project" | "space"
> & {
	groupBy: Schemas["OutputGroupBy"];
};

export function useOutputStats(q: OutputQuery, enabled = true) {
	return useQuery({
		queryKey: statsKeys.output(q),
		enabled,
		refetchInterval: POLL_MS,
		queryFn: async () =>
			json(
				await unwrap(api.GET("/api/v1/stats/output", { params: { query: q } })),
			),
	});
}

export function useBudget() {
	return useQuery({
		queryKey: statsKeys.budget,
		refetchInterval: POLL_MS,
		queryFn: () => unwrap(api.GET("/api/v1/stats/budget")),
	});
}

export function useSetBudget() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (body: Budget) =>
			unwrap(api.PUT("/api/v1/stats/budget", { body })),
		onSuccess: (status) => qc.setQueryData(statsKeys.budget, status),
	});
}

export type ExportKind = "usage" | "tools" | "orbs" | "output" | "records";

/**
 * 导出链接（同源、相对路径，给 `<a download>` 用）：`records` = 用量明细 CSV（`stats/usage/records`）；
 * 其余是对应接口带 `format=csv`，或 JSON 带 `download=true`（服务端加附件头）。未设的参数省略。
 */
export function statsExportUrl(
	kind: ExportKind,
	params: Record<string, string | number | undefined>,
	format: "csv" | "json",
): string {
	const sp = new URLSearchParams();
	for (const [k, v] of Object.entries(params))
		if (v !== undefined && v !== "") sp.set(k, String(v));
	if (kind !== "records") {
		if (format === "csv") sp.set("format", "csv");
		else sp.set("download", "true");
	}
	const path = kind === "records" ? "usage/records" : kind;
	return `/api/v1/stats/${path}?${sp.toString()}`;
}
