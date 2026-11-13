/**
 * 输入坞的会话读数：模型与思考级别（ModelChip，在工具行）、上下文圆环（ContextRing）与会话成本、速度档位与输出速度（SessionReadout），
 * 后两者在页脚行右侧同一组。原线程底部状态栏的内容拆到这里。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useThreadUsage } from "../../api/queries";
import { formatCost, formatCount } from "../../lib/format";
import { modelBrand, shortModelName } from "../../lib/model-brand";
import {
	currentSpeed,
	SPEED_ICON,
	speedCommands,
	speedTiers,
} from "../../lib/speed-tier";
import { useThread } from "../../state/stores";
import { threadCost } from "../../state/transcript";
import { uiStore } from "../../state/ui";
import { runTrayCommand } from "../thread/ModeBar";
import { Ring, Stat, thresholdTone } from "../ui/data";
import { Icon } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import { Menu, type MenuItem } from "../ui/primitives";
import { ThinkingMenu } from "../ui/ThinkingDial";

/**
 * 当前模型：厂商 logo + 名字（认得出品牌时去掉系列名，logo 已说明），点击打开模型对话框（顶部可切换角色）；
 * 右侧思考级别圆圈是单独的按钮，点开选级别（选项按模型能力，模型不推理时不显示圆圈）。悬停提示与读屏用全名。
 */
export function ModelChip({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const state = useThread(threadId, (s) => s.state);
	const model = state?.model;
	const name = model ? model.name || model.id : t("status.noModel");
	const short = model
		? shortModelName(name, modelBrand(model.id, model.provider))
		: name;
	const levels = state?.availableThinkingLevels;
	const reasons = model?.reasoning !== false && (levels?.length ?? 1) > 0;
	// 菜单里勾选的是配置值（auto 或具体级别）；圆圈画实际生效的级别
	const configured = state?.thinkingConfigured ?? state?.thinkingLevel ?? "";
	const effective =
		configured && configured !== "auto" ? configured : state?.thinkingLevel;
	return (
		<span className="inline-flex min-w-0 shrink-0 items-center">
			<button
				type="button"
				title={name}
				className={`inline-flex h-[30px] max-w-36 shrink-0 items-center gap-1.5 rounded-md text-fg text-xs transition-colors duration-150 hover:bg-hover sm:max-w-56 ${reasons ? "pr-1 pl-2" : "px-2"}`}
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
			</button>
			{reasons && (
				<ThinkingMenu
					value={configured}
					shown={effective}
					options={["auto", ...(levels ?? [])]}
					onSelect={(level) =>
						void runTrayCommand(threadId, {
							type: "set_thinking_level",
							level: level as "auto",
						})
					}
					className="text-muted"
				/>
			)}
		</span>
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

/**
 * 速度档位 + 最近的输出速度：图标表示当前档位（标准 / 快速 / 极速 / 慢速）。
 * 模型有可选档位时是按钮，点开只列当前模型支持的档位（omp 会话状态里的 *Supported）；只有标准一档时是静态读数。
 */
function SpeedControl({
	threadId,
	tps,
}: {
	threadId: string;
	tps: number | null | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const state = useThread(threadId, (s) => s.state);
	const rate =
		typeof tps === "number" && tps > 0
			? t("status.tps", { n: tps.toFixed(0) })
			: null;
	const tiers = state ? speedTiers(state) : ["standard" as const];
	const current = state ? currentSpeed(state) : "standard";
	if (tiers.length === 1) {
		return rate ? (
			<Stat icon={SPEED_ICON.standard} label={t("stat.speed")}>
				{rate}
			</Stat>
		) : null;
	}
	const name = t("speed.label", { tier: t(`speed.${current}`) });
	const items: MenuItem[] = tiers.map((tier) => ({
		key: tier,
		icon: SPEED_ICON[tier],
		label: t(`speed.${tier}`),
		// Anthropic 订阅的慢速是全局配置，会影响所有会话
		hint:
			tier === "slow" && state?.slowModeScope === "global"
				? t("speed.global")
				: undefined,
		checked: tier === current,
		onSelect: async () => {
			if (!state) return;
			for (const frame of speedCommands(state, tier))
				if (!(await runTrayCommand(threadId, frame))) return;
		},
	}));
	return (
		<Menu
			side="top"
			align="right"
			items={items}
			trigger={(p) => (
				<button
					type="button"
					aria-label={
						rate
							? t("speed.buttonRate", { tier: t(`speed.${current}`), rate })
							: t("speed.button", { tier: t(`speed.${current}`) })
					}
					title={name}
					className="-mx-1 inline-flex h-5 shrink-0 items-center gap-1 rounded-sm px-1 tabular-nums transition-colors duration-150 hover:bg-hover hover:text-fg"
					{...p}
				>
					<Icon name={SPEED_ICON[current]} size={12} />
					{rate}
				</button>
			)}
		/>
	);
}

/** 页脚右侧读数：上下文占用、会话累计成本、速度档位与最近的输出速度。 */
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
			{/* 成本不配图标：formatCost 自带 $，本身就是读数的记号，再加货币图标会读成「$ $0.24」 */}
			<span title={t("stat.cost")} className="tabular-nums">
				<span className="sr-only">{t("stat.cost")}</span>
				{formatCost(cost)}
			</span>
			<SpeedControl threadId={threadId} tps={tps} />
		</span>
	);
}
