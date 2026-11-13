/**
 * 输入坞的会话读数：模型与思考级别（ModelChip）、上下文圆环（ContextRing）、会话成本与输出速度（SessionReadout）。
 * 原线程底部状态栏的内容拆到这里，嵌在 Composer 的工具行与页脚行。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useThreadUsage } from "../../api/queries";
import { formatCost, formatCount } from "../../lib/format";
import { useThread } from "../../state/stores";
import { threadCost } from "../../state/transcript";
import { uiStore } from "../../state/ui";
import { Badge, Ring, Stat, thresholdTone } from "../ui/data";
import { Icon } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";

/** 当前模型（厂商 logo + 名字）+ 思考级别 + 快/慢模式；点击打开模型对话框（顶部可切换角色）。 */
export function ModelChip({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const state = useThread(threadId, (s) => s.state);
	const name = state?.model
		? state.model.name || state.model.id
		: t("status.noModel");
	const level = state?.thinkingConfigured ?? state?.thinkingLevel;
	return (
		<button
			type="button"
			title={name}
			className="inline-flex h-[30px] max-w-36 shrink-0 items-center gap-1.5 rounded-md px-2 text-fg text-xs transition-colors duration-150 hover:bg-hover sm:max-w-56"
			onClick={() => uiStore.getState().open({ kind: "model" })}
		>
			<ModelLogo
				provider={state?.model?.provider}
				model={state?.model?.id}
				size={14}
				className="shrink-0"
			/>
			<span className="min-w-0 truncate font-medium">{name}</span>
			{level && <Badge>{level}</Badge>}
			{state?.fastModeEnabled && <Badge tone="accent">{t("model.fast")}</Badge>}
			{state?.slowModeEnabled && <Badge>{t("model.slow")}</Badge>}
			<Icon name="chevronDown" size={11} className="shrink-0 text-ghost" />
		</button>
	);
}

/** 上下文占用圆环 + 百分比；点击打开上下文报告。无数据时不渲染。 */
export function ContextRing({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const ctx = useThread(threadId, (s) => s.state?.contextUsage);
	if (!ctx) return null;
	const pct = Math.round(ctx.percent);
	const label = t("status.context", { pct });
	return (
		<button
			type="button"
			aria-label={label}
			title={`${formatCount(ctx.tokens)} / ${formatCount(ctx.contextWindow)}`}
			className="inline-flex h-[30px] shrink-0 items-center gap-1.5 rounded-md px-1.5 font-medium text-muted text-xs tabular-nums transition-colors duration-150 hover:bg-hover"
			onClick={() =>
				uiStore
					.getState()
					.open({ kind: "report", report: "get_context_report" })
			}
		>
			<Ring value={pct} size={18} tone={thresholdTone(pct)} label={label} />
			{pct}%
		</button>
	);
}

/** 会话累计成本与最近的输出速度。 */
export function SessionReadout({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const state = useThread(threadId, (s) => s.state);
	const createdAt = useThread(threadId, (s) => s.thread?.createdAt);
	const entries = useThread(threadId, (s) => s.entries);
	const usage = useThreadUsage(threadId, createdAt);
	const cost = usage.data
		? usage.data.costUsd + (usage.data.subagentCostUsd ?? 0)
		: threadCost(entries);
	const tps = state?.tokensPerSecond ?? usage.data?.tokensPerSecond;
	return (
		<span className="inline-flex shrink-0 items-center gap-3 font-medium text-muted">
			<Stat icon="coin" label={t("stat.cost")}>
				{formatCost(cost)}
			</Stat>
			{typeof tps === "number" && tps > 0 && (
				<Stat icon="bolt" label={t("stat.speed")}>
					{t("status.tps", { n: tps.toFixed(0) })}
				</Stat>
			)}
		</span>
	);
}
