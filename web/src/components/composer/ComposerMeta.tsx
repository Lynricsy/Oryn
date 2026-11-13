/**
 * 输入坞的会话读数：模型与思考级别（ModelChip，在工具行）、上下文圆环（ContextRing）与会话成本、输出速度（SessionReadout），
 * 后两者在页脚行右侧同一组。原线程底部状态栏的内容拆到这里。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useThreadUsage } from "../../api/queries";
import { formatCost, formatCount } from "../../lib/format";
import { modelBrand, shortModelName } from "../../lib/model-brand";
import { useThread } from "../../state/stores";
import { threadCost } from "../../state/transcript";
import { uiStore } from "../../state/ui";
import { Ring, Stat, thresholdTone } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import { ThinkingDial } from "../ui/ThinkingDial";

/** 快 / 慢模式的小图标；标签给读屏与悬停提示。 */
function ModeGlyph({
	icon,
	label,
	className,
}: {
	icon: IconName;
	label: string;
	className: string;
}): ReactNode {
	return (
		<span
			role="img"
			aria-label={label}
			title={label}
			className={`inline-flex shrink-0 ${className}`}
		>
			<Icon name={icon} size={12} weight="fill" />
		</span>
	);
}

/**
 * 当前模型：厂商 logo + 名字（认得出品牌时去掉系列名，logo 已说明）+ 思考级别圆圈 + 快/慢模式图标；
 * 点击打开模型对话框（顶部可切换角色）。悬停提示与读屏用全名。
 */
export function ModelChip({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const state = useThread(threadId, (s) => s.state);
	const model = state?.model;
	const name = model ? model.name || model.id : t("status.noModel");
	const short = model
		? shortModelName(name, modelBrand(model.id, model.provider))
		: name;
	// auto 模式下画实际生效的级别；模型不推理（可选级别为空）时不画
	const configured = state?.thinkingConfigured;
	const reasons =
		model?.reasoning !== false &&
		(state?.availableThinkingLevels?.length ?? 1) > 0;
	const level = reasons
		? configured && configured !== "auto"
			? configured
			: state?.thinkingLevel
		: undefined;
	return (
		<button
			type="button"
			title={name}
			className="inline-flex h-[30px] max-w-36 shrink-0 items-center gap-1.5 rounded-md px-2 text-fg text-xs transition-colors duration-150 hover:bg-hover sm:max-w-56"
			onClick={() => uiStore.getState().open({ kind: "model" })}
		>
			<ModelLogo
				provider={model?.provider}
				model={model?.id}
				size={14}
				className="shrink-0"
			/>
			{short === name ? (
				<span className="min-w-0 truncate font-medium">{name}</span>
			) : (
				<>
					<span aria-hidden className="min-w-0 truncate font-medium">
						{short}
					</span>
					<span className="sr-only">{name}</span>
				</>
			)}
			<ThinkingDial level={level} className="text-muted" />
			{state?.fastModeEnabled && (
				<ModeGlyph
					icon="fastForward"
					label={t("model.fastMode")}
					className="text-accent"
				/>
			)}
			{state?.slowModeEnabled && (
				<ModeGlyph
					icon="hourglass"
					label={t("model.slowMode")}
					className="text-muted"
				/>
			)}
		</button>
	);
}

/** 上下文占用圆环 + 百分比（页脚读数，与成本、速度同尺寸）；点击打开上下文报告。无数据时不渲染。 */
function ContextRing({ threadId }: { threadId: string }): ReactNode {
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
			className="-mx-1 inline-flex h-5 shrink-0 items-center gap-1 rounded-sm px-1 tabular-nums transition-colors duration-150 hover:bg-hover hover:text-fg"
			onClick={() =>
				uiStore
					.getState()
					.open({ kind: "report", report: "get_context_report" })
			}
		>
			<Ring value={pct} size={12} tone={thresholdTone(pct)} label={label} />
			{pct}%
		</button>
	);
}

/** 页脚右侧读数：上下文占用、会话累计成本、最近的输出速度。 */
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
			<ContextRing threadId={threadId} />
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
