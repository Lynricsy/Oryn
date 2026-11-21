/**
 * box 页侧栏的「最近 21 天」（M23 步骤 7.4）：21 根竖条 = 每天在这台 box 上开的线程数（按最大值归一，至少 2px），
 * 竖条下一条分 21 段的细线 = 在线率（段的不透明度 = 当天的 `upRatio`）。图例给线程总数与 21 天平均在线率。
 * 不引入图表库；颜色只用令牌（在线率用成功色，线程数用中性墨色）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { BoxUsage } from "../../api/box-queries";

const BAR_H = 56;

export function BoxUsageChart({ usage }: { usage: BoxUsage }): ReactNode {
	const { t } = useTranslation();
	const days = usage.days;
	const max = Math.max(1, ...days.map((d) => d.threads));
	const total = days.reduce((n, d) => n + d.threads, 0);
	const up =
		days.length > 0
			? Math.round(
					(days.reduce((n, d) => n + d.upRatio, 0) / days.length) * 100,
				)
			: 0;
	return (
		<section data-testid="box-usage" aria-label={t("boxes.usage.title")}>
			<h2 className="mb-2 font-medium text-faint text-xs">
				{t("boxes.usage.title")}
			</h2>
			<div className="flex items-end gap-[3px]" style={{ height: BAR_H }}>
				{days.map((d) => (
					<span
						key={d.date}
						title={t("boxes.usage.day", {
							date: d.date,
							threads: d.threads,
							percent: Math.round(d.upRatio * 100),
						})}
						className="min-w-0 flex-1 rounded-t-xs bg-fg/35"
						style={{
							height: Math.max(2, Math.round((d.threads / max) * BAR_H)),
						}}
					/>
				))}
			</div>
			<div aria-hidden className="mt-1.5 flex gap-[3px]">
				{days.map((d) => (
					<span
						key={d.date}
						className="h-1 min-w-0 flex-1 rounded-full bg-ok"
						style={{ opacity: Math.max(0.08, d.upRatio) }}
					/>
				))}
			</div>
			<div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-faint text-xs">
				<span className="inline-flex items-center gap-1.5">
					<span aria-hidden className="h-2 w-2 rounded-xs bg-fg/35" />
					{t("boxes.usage.threads", { count: total })}
				</span>
				<span className="inline-flex items-center gap-1.5">
					<span aria-hidden className="h-1 w-2.5 rounded-full bg-ok" />
					{t("boxes.usage.up", { percent: up })}
				</span>
			</div>
		</section>
	);
}
