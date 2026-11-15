/**
 * 会话统计报告（`/session`、`/stats`）：会话、token、成本、整段历史、路由模型分组显示，数字按单位格式化。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	formatCost,
	formatCount,
	formatDateTime,
	formatRelative,
} from "../../../lib/format";
import { modelBrand, shortModelName } from "../../../lib/model-brand";
import type { ResponseData } from "../../../omp/types";
import { CopyButton } from "../../ui/CopyButton";
import { KeyValue, Meter, thresholdTone } from "../../ui/data";
import { ModelLogo } from "../../ui/ModelLogo";
import { ReportCard, ReportSection, StatGrid } from "./parts";

type SessionData = ResponseData<"get_session_stats">;

export function SessionReport({ data }: { data: SessionData }): ReactNode {
	const { t } = useTranslation();
	const tk = data.tokens;
	const ctx = data.contextUsage;
	const ctxPercent =
		ctx && ctx.contextWindow > 0
			? (ctx.tokens / ctx.contextWindow) * 100
			: null;
	const routed = Object.entries(data.routedModels ?? {}).sort(
		(a, b) => b[1] - a[1],
	);
	const rejections = Object.entries(data.editRejections ?? {}).filter(
		([, n]) => n > 0,
	);
	const h = data.history;
	return (
		<div className="flex flex-col gap-4">
			<ReportSection title={t("report.session.info")}>
				<ReportCard>
					<KeyValue
						rows={[
							{
								key: "id",
								label: t("report.session.id"),
								value: (
									<span className="inline-flex min-w-0 items-center gap-1">
										<span className="min-w-0 break-all font-mono text-xs">
											{data.sessionId}
										</span>
										<CopyButton
											text={data.sessionId}
											ariaLabel={t("report.session.copyId")}
										/>
									</span>
								),
							},
							...(data.sessionFile
								? [
										{
											key: "file",
											label: t("report.session.file"),
											value: (
												<span className="break-all font-mono text-xs">
													{data.sessionFile}
												</span>
											),
										},
									]
								: []),
							{
								key: "messages",
								label: t("report.session.messages"),
								value: (
									<span className="tabular-nums">
										{t("report.session.messagesValue", {
											total: formatCount(data.totalMessages),
											user: formatCount(data.userMessages),
											assistant: formatCount(data.assistantMessages),
										})}
									</span>
								),
							},
							{
								key: "tools",
								label: t("report.session.toolCalls"),
								value: (
									<span className="tabular-nums">
										{t("report.session.toolCallsValue", {
											calls: formatCount(data.toolCalls),
											results: formatCount(data.toolResults),
										})}
									</span>
								),
							},
						]}
					/>
				</ReportCard>
			</ReportSection>

			<ReportSection title={t("report.session.tokens")}>
				<ReportCard>
					<StatGrid
						cols={3}
						items={[
							{
								key: "total",
								label: t("report.session.total"),
								value: (
									<span className="font-semibold">{formatCount(tk.total)}</span>
								),
							},
							{
								key: "input",
								label: t("report.usage.input"),
								value: formatCount(tk.input),
							},
							{
								key: "output",
								label: t("report.usage.output"),
								value: formatCount(tk.output),
							},
							{
								key: "reasoning",
								label: t("report.session.reasoning"),
								value: formatCount(tk.reasoning),
							},
							{
								key: "cacheRead",
								label: t("report.usage.cacheRead"),
								value: formatCount(tk.cacheRead),
							},
							{
								key: "cacheWrite",
								label: t("report.usage.cacheWrite"),
								value: formatCount(tk.cacheWrite),
							},
						]}
					/>
					{ctx && ctxPercent !== null && (
						<div className="mt-3 border-border border-t pt-3">
							<div className="mb-1.5 flex items-baseline gap-2 text-xs">
								<span className="text-faint">
									{t("report.session.context")}
								</span>
								<span className="flex-1" />
								<span className="font-medium tabular-nums">
									{Math.round(ctxPercent)}%
								</span>
								<span className="text-faint tabular-nums">
									{formatCount(ctx.tokens)} / {formatCount(ctx.contextWindow)}
								</span>
							</div>
							<Meter
								value={ctxPercent}
								tone={thresholdTone(ctxPercent)}
								label={t("report.session.context")}
							/>
						</div>
					)}
				</ReportCard>
			</ReportSection>

			<ReportSection title={t("report.session.cost")}>
				<ReportCard>
					<StatGrid
						cols={3}
						items={[
							{
								key: "cost",
								label: t("report.usage.cost"),
								value: (
									<span className="font-semibold">{formatCost(data.cost)}</span>
								),
							},
							{
								key: "premium",
								label: t("report.usage.premium"),
								value: formatCount(data.premiumRequests),
							},
							...(data.credits
								? [
										{
											key: "credits",
											label: t("report.session.credits"),
											value: formatCost(data.credits.cost),
											hint: t("report.session.creditsHint", {
												committed: formatCost(data.credits.committedCost),
												acu: formatCount(data.credits.acuCost),
											}),
										},
									]
								: []),
						]}
					/>
				</ReportCard>
			</ReportSection>

			{h && (
				<ReportSection
					title={t("report.session.history")}
					aside={
						<span
							className="text-2xs text-faint"
							title={formatDateTime(h.sampledAt)}
						>
							{t("report.usage.fetched", {
								when: formatRelative(h.sampledAt),
							})}
						</span>
					}
				>
					<ReportCard>
						<p className="mb-3 text-faint text-xs">
							{t("report.session.historyHint")}
						</p>
						<StatGrid
							items={[
								{
									key: "cost",
									label: t("report.usage.cost"),
									value: formatCost(h.cost),
									hint:
										h.subagentCost > 0
											? t("report.session.subagentCost", {
													cost: formatCost(h.subagentCost),
												})
											: undefined,
								},
								{
									key: "total",
									label: t("report.session.total"),
									value: formatCount(h.totalTokens),
								},
								{
									key: "input",
									label: t("report.usage.input"),
									value: formatCount(h.input),
								},
								{
									key: "output",
									label: t("report.usage.output"),
									value: formatCount(h.output),
								},
							]}
						/>
					</ReportCard>
				</ReportSection>
			)}

			{routed.length > 0 && (
				<ReportSection title={t("report.session.models")}>
					<ReportCard className="py-1.5">
						<ul className="flex flex-col">
							{routed.map(([model, turns]) => (
								<li
									key={model}
									title={model}
									className="flex items-center gap-2 border-border border-t py-1.5 text-sm first:border-t-0"
								>
									<ModelLogo model={model} className="text-muted" />
									<span className="min-w-0 flex-1 truncate">
										{shortModelName(model, modelBrand(model))}
									</span>
									<span className="shrink-0 text-faint text-xs tabular-nums">
										{t("report.session.turns", { count: turns })}
									</span>
								</li>
							))}
						</ul>
					</ReportCard>
				</ReportSection>
			)}

			{rejections.length > 0 && (
				<ReportSection title={t("report.session.editRejections")}>
					<ReportCard>
						<KeyValue
							rows={rejections.map(([kind, n]) => ({
								key: kind,
								label: <span className="font-mono text-xs">{kind}</span>,
								value: <span className="tabular-nums">{formatCount(n)}</span>,
							}))}
						/>
					</ReportCard>
				</ReportSection>
			)}
		</div>
	);
}
