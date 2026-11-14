/** 统计页的显示规则：分组名、时间桶标签、系列色、指标取值与格式（M10 计划 5.3）。 */
import type { TFunction } from "i18next";
import type { UsageStat } from "../../api/stats-queries";
import i18n from "../../i18n";
import { formatCost, formatTokens } from "../../lib/format";
import { modelBrand, shortModelName } from "../../lib/model-brand";
import type { Grain, MetricKey } from "./statsParams";

/** 服务端把排名之外的组折叠成的键。 */
export const OTHER_KEY = "__other__";

/** 系列色：按排名取 `--viz-1…8`，折叠项与第 9 项起用 `--viz-other`。 */
export function vizColor(rank: number, key: string): string {
	return key === OTHER_KEY || rank >= 8 ? "--viz-other" : `--viz-${rank + 1}`;
}

export type GroupDim = "model" | "project" | "space" | "thread" | "purpose";

/**
 * 分组的显示名与全名（`title`，悬停提示）：折叠项为「其他」，空键按维度给（无项目 / 无空间 / 已删除的线程 / 对话回合）；
 * 模型键 `<provider>/<model>` 显示为短名（与全站模型名同一写法），全名进 title。
 */
export function groupName(
	dim: GroupDim,
	g: { key: string; label?: string },
	t: TFunction,
): { name: string; title: string } {
	if (g.key === OTHER_KEY) return { name: t("stats.other"), title: "" };
	if (g.key === "") {
		const name = t(`stats.emptyKey.${dim}`);
		return { name, title: name };
	}
	if (dim === "model") {
		const cut = g.key.indexOf("/");
		const provider = cut > 0 ? g.key.slice(0, cut) : undefined;
		const model = cut > 0 ? g.key.slice(cut + 1) : g.key;
		return {
			name: shortModelName(model, modelBrand(model, provider)),
			title: g.key,
		};
	}
	const name = g.label || g.key;
	return { name, title: g.label ? `${g.label} (${g.key})` : g.key };
}

/** 四类 token 之和。 */
export function tokensOf(s: UsageStat): number {
	return (
		s.inputTokens + s.outputTokens + s.cacheReadTokens + s.cacheWriteTokens
	);
}

/** 主图纵轴的取值。 */
export function metricOf(metric: MetricKey, s: UsageStat): number {
	if (metric === "cost") return s.costUsd;
	if (metric === "tokens") return tokensOf(s);
	return s.requests;
}

export function formatMetric(metric: MetricKey, v: number): string {
	return metric === "cost" ? formatCost(v) : formatTokens(v);
}

/** 比例 → 百分比文本（一位小数以内）；分母为 0 时「—」。 */
export function formatRatio(num: number, den: number): string {
	if (!(den > 0)) return "—";
	const pct = (num / den) * 100;
	return `${pct < 10 && pct > 0 ? pct.toFixed(1) : Math.round(pct)}%`;
}

/** 小时数（图表纵轴、orb 时长）：<10 一位小数。 */
export function formatHours(ms: number): string {
	const h = ms / 3_600_000;
	if (h === 0) return "0h";
	return `${h < 10 ? Number(h.toFixed(1)) : Math.round(h)}h`;
}

/**
 * 时间桶标签：小时 `13:00`、日 `11/14`、周 `W46`、月 `11月` / `Nov`；`long` 时给完整日期（浮层标题）。
 * `start` 是桶起点（毫秒），按浏览器本地时区格式化（窗口本来就按本地时区切）。
 */
export function bucketLabel(
	key: string,
	start: number | undefined,
	grain: Grain,
	long = false,
): string {
	const lng = i18n.language;
	if (grain === "week") {
		const w = /W(\d+)$/.exec(key)?.[1];
		if (!long || start === undefined) return w ? `W${Number(w)}` : key;
		const d = new Intl.DateTimeFormat(lng, { month: "short", day: "numeric" });
		return `${key} · ${d.format(start)}`;
	}
	if (start === undefined) return key;
	if (grain === "hour")
		return new Intl.DateTimeFormat(lng, {
			...(long ? { month: "short", day: "numeric" } : {}),
			hour: "2-digit",
			minute: "2-digit",
			hourCycle: "h23",
		}).format(start);
	if (grain === "month")
		return new Intl.DateTimeFormat(lng, {
			...(long ? { year: "numeric" } : {}),
			month: "short",
		}).format(start);
	return new Intl.DateTimeFormat(
		lng,
		long
			? { year: "numeric", month: "short", day: "numeric", weekday: "short" }
			: { month: "numeric", day: "numeric" },
	).format(start);
}
