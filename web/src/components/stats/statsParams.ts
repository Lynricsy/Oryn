/**
 * 统计页的 URL ↔ 状态（M10 计划 5.3）。查询串：`range`（默认 30d）、`from` / `to`（custom 的本地日期 YYYY-MM-DD，含首尾）、
 * `by`（默认 model）、`metric`（默认 cost）、`project`、`space`、`model`、`thread`。缺省值不写进 URL。
 * 窗口按浏览器本地时区换算：`from` = 本地零点，`to` = 本地次日零点；粒度按天数自动选。
 */
import type { Schemas } from "../../api/client";

export type StatsTab = "usage" | "orbs" | "output";
export const STATS_TABS: readonly StatsTab[] = ["usage", "orbs", "output"];

export type RangeKey =
	| "today"
	| "7d"
	| "30d"
	| "90d"
	| "month"
	| "all"
	| "custom";
export const RANGES: readonly RangeKey[] = [
	"today",
	"7d",
	"30d",
	"90d",
	"month",
	"all",
	"custom",
];

export type ByKey = "model" | "project" | "space" | "purpose";
export const BYS: readonly ByKey[] = ["model", "project", "space", "purpose"];

export type MetricKey = "cost" | "tokens" | "requests";
export const METRICS: readonly MetricKey[] = ["cost", "tokens", "requests"];

export type Grain = Schemas["StatsGrain"];

export const FILTER_KEYS = ["project", "space", "model", "thread"] as const;
export type FilterKey = (typeof FILTER_KEYS)[number];

export interface StatsParams {
	range: RangeKey;
	/** custom 的首日（本地日期 YYYY-MM-DD） */
	from?: string;
	/** custom 的末日（含） */
	to?: string;
	by: ByKey;
	metric: MetricKey;
	project?: string;
	space?: string;
	model?: string;
	thread?: string;
}

export const DEFAULT_PARAMS: StatsParams = {
	range: "30d",
	by: "model",
	metric: "cost",
};

const DATE_RE = /^(\d{4})-(\d{2})-(\d{2})$/;

function oneOf<T extends string>(
	v: string | null,
	all: readonly T[],
	fallback: T,
): T {
	return v !== null && (all as readonly string[]).includes(v)
		? (v as T)
		: fallback;
}

/** 本地日期串 → 当天本地零点；不合法返回 null。 */
function parseDate(s: string | undefined): Date | null {
	const m = s ? DATE_RE.exec(s) : null;
	if (!m) return null;
	const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
	return d.getFullYear() === Number(m[1]) &&
		d.getMonth() === Number(m[2]) - 1 &&
		d.getDate() === Number(m[3])
		? d
		: null;
}

/** 本地日期（YYYY-MM-DD）。 */
export function localDate(ts: number): string {
	const d = new Date(ts);
	const p = (n: number) => String(n).padStart(2, "0");
	return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

export function parseStatsParams(sp: URLSearchParams): StatsParams {
	const p: StatsParams = {
		range: oneOf(sp.get("range"), RANGES, DEFAULT_PARAMS.range),
		by: oneOf(sp.get("by"), BYS, DEFAULT_PARAMS.by),
		metric: oneOf(sp.get("metric"), METRICS, DEFAULT_PARAMS.metric),
	};
	if (p.range === "custom") {
		const from = sp.get("from") ?? undefined;
		const to = sp.get("to") ?? undefined;
		const a = parseDate(from);
		const b = parseDate(to);
		// 日期不合法或首日晚于末日：退回缺省范围
		if (!a || !b || a > b) p.range = DEFAULT_PARAMS.range;
		else {
			p.from = from;
			p.to = to;
		}
	}
	for (const k of FILTER_KEYS) {
		const v = sp.get(k);
		if (v) p[k] = v;
	}
	return p;
}

/** 状态 → 查询串（不带 `?`，缺省值省略，键序固定）。 */
export function statsSearch(p: StatsParams): string {
	const sp = new URLSearchParams();
	if (p.range !== DEFAULT_PARAMS.range) sp.set("range", p.range);
	if (p.range === "custom" && p.from && p.to) {
		sp.set("from", p.from);
		sp.set("to", p.to);
	}
	if (p.by !== DEFAULT_PARAMS.by) sp.set("by", p.by);
	if (p.metric !== DEFAULT_PARAMS.metric) sp.set("metric", p.metric);
	for (const k of FILTER_KEYS) {
		const v = p[k];
		if (v) sp.set(k, v);
	}
	return sp.toString();
}

export interface StatsWindow {
	from: number;
	to: number;
	/** 浏览器的 IANA 时区 */
	tz: string;
	/** 本地日数 */
	days: number;
	grain: Grain;
}

/** 天数 → 粒度：≤2 → hour，≤92 → day，≤400 → week，其余 month。 */
export function grainFor(days: number): Grain {
	if (days <= 2) return "hour";
	if (days <= 92) return "day";
	if (days <= 400) return "week";
	return "month";
}

/** orb 与产出统计没有小时粒度：`hour` 退到 `day`。 */
export function coarseGrain(g: Grain): Exclude<Grain, "hour"> {
	return g === "hour" ? "day" : g;
}

export function browserTimeZone(): string {
	return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

/** 本地某天零点再加 n 天（按日历加，跨夏令时也落在零点）。 */
function addDays(d: Date, n: number): Date {
	return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);
}

/**
 * 范围 → 窗口。`earliestTs` 是 `StatsMeta.earliestTs`（`all` 的起点；没有则退回 30 天）。
 */
export function statsWindow(
	p: Pick<StatsParams, "range" | "from" | "to">,
	now: number,
	earliestTs?: number,
	tz = browserTimeZone(),
): StatsWindow {
	const today = addDays(new Date(now), 0);
	const tomorrow = addDays(today, 1);
	let from: Date;
	let to = tomorrow;
	switch (p.range) {
		case "today":
			from = today;
			break;
		case "7d":
			from = addDays(today, -6);
			break;
		case "90d":
			from = addDays(today, -89);
			break;
		case "month":
			from = new Date(today.getFullYear(), today.getMonth(), 1);
			break;
		case "all":
			from =
				earliestTs !== undefined && earliestTs > 0 && earliestTs < now
					? addDays(new Date(earliestTs), 0)
					: addDays(today, -29);
			break;
		case "custom": {
			const a = parseDate(p.from);
			const b = parseDate(p.to);
			if (a && b && a <= b) {
				from = a;
				to = addDays(b, 1);
			} else from = addDays(today, -29);
			break;
		}
		default:
			from = addDays(today, -29);
	}
	const days = Math.max(
		1,
		Math.round((to.getTime() - from.getTime()) / 86_400_000),
	);
	return {
		from: from.getTime(),
		to: to.getTime(),
		tz,
		days,
		grain: grainFor(days),
	};
}

/** 截至今天（含）的最近 `days` 个本地日：起点、终点都取本地零点，当天内查询键不变（项目总览的活动曲线用）。 */
export function daysWindow(
	days: number,
	now: number,
	tz = browserTimeZone(),
): StatsWindow {
	const today = addDays(new Date(now), 0);
	return {
		from: addDays(today, -(days - 1)).getTime(),
		to: addDays(today, 1).getTime(),
		tz,
		days,
		grain: grainFor(days),
	};
}

/** 等长的前一段（「较上一周期」）。 */
export function previousWindow(w: Pick<StatsWindow, "from" | "to">): {
	from: number;
	to: number;
} {
	return { from: w.from - (w.to - w.from), to: w.from };
}

/** 统计接口的筛选参数：`project` / `space` / `model` / `thread`（未设的省略）。 */
export function filterQuery(
	p: Pick<StatsParams, FilterKey>,
): Partial<Record<FilterKey, string>> {
	const q: Partial<Record<FilterKey, string>> = {};
	for (const k of FILTER_KEYS) if (p[k]) q[k] = p[k];
	return q;
}
