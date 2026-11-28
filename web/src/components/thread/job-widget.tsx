/**
 * Core 的后台任务小组件（`JobWidgetView`）：标题读数（带涨跌）、阶段与各块。
 *
 * - `JobWidgetCard`：任务详情对话框里的完整视图，所有块、标记线标签、柱子的逐项标注、视图状态徽标。
 * - `JobWidgetCompact`：信息面板任务列表里的一行下方，只有读数与第一块（Core 约定第一块是主块，紧凑视图只显示它）。
 *   它放在列表行的按钮里，全部用 span 搭（按钮内只允许短语内容）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { JobWidget } from "../../state/jobs";
import { Badge, Meter, type Tone } from "../ui/data";
import { Sparkline } from "../ui/primitives";

type Block = JobWidget["blocks"][number];
type SparkBlock = Extract<Block, { kind: "spark" }>;
type BarsBlock = Extract<Block, { kind: "bars" }>;
type Headline = NonNullable<JobWidget["headline"]>;
type Trend = NonNullable<Headline["trend"]>;
type WidgetTone = NonNullable<SparkBlock["tone"]>;

/** 两种视图：对话框里的卡片、列表行里的紧凑版。 */
type Variant = "card" | "row";

const TEXT: Record<WidgetTone, string> = {
	neutral: "text-fg",
	accent: "text-accent",
	user: "text-accent",
	info: "text-frost",
	success: "text-ok",
	warning: "text-warn",
	error: "text-err",
	pending: "text-muted",
	muted: "text-faint",
};

const FILL: Record<WidgetTone, string> = {
	neutral: "bg-fg/70",
	accent: "bg-accent",
	user: "bg-accent",
	info: "bg-frost",
	success: "bg-ok",
	warning: "bg-warn",
	error: "bg-err",
	pending: "bg-ghost",
	muted: "bg-ghost",
};

const METER_TONE: Record<WidgetTone, Tone> = {
	neutral: "neutral",
	accent: "accent",
	user: "accent",
	info: "frost",
	success: "ok",
	warning: "warn",
	error: "err",
	pending: "neutral",
	muted: "neutral",
};

const TREND_TEXT: Record<Trend["tone"], string> = {
	success: "text-ok",
	error: "text-err",
	muted: "text-faint",
};

function TrendText({ trend }: { trend: Trend }): ReactNode {
	return (
		<span
			className={`shrink-0 font-medium text-xs tabular-nums ${TREND_TEXT[trend.tone]}`}
		>
			{trend.text}
		</span>
	);
}

/** `380 MB` 拆成大号数字 + 小号单位；不是「数字 + 单位」的形状时整段大号。 */
function splitUnit(text: string): [string, string] {
	const m = /^([-+−]?\d[\d.,]*)\s*([%A-Za-zµ°/][^\d]*)$/u.exec(text.trim());
	return m ? [m[1] ?? text, m[2] ?? ""] : [text, ""];
}

function HeadlineValue({
	headline,
	variant,
}: {
	headline: Headline;
	variant: Variant;
}): ReactNode {
	const [value, unit] = splitUnit(headline.text);
	return (
		<span className="flex min-w-0 items-baseline gap-1.5 tabular-nums">
			<span
				className={`shrink-0 font-semibold text-fg ${variant === "card" ? "text-3xl leading-9" : "text-lg leading-6"}`}
			>
				{value}
			</span>
			{unit && <span className="shrink-0 text-muted text-sm">{unit}</span>}
			{headline.trend && <TrendText trend={headline.trend} />}
			{headline.label && (
				<span className="min-w-0 truncate text-faint text-xs">
					{headline.label}
				</span>
			)}
		</span>
	);
}

/** 块标题行：左标签、右读数。 */
function BlockHead({
	label,
	value,
}: {
	label: string;
	value?: string;
}): ReactNode {
	return (
		<span className="flex min-w-0 justify-between gap-2 text-faint text-xs">
			<span className="min-w-0 truncate">{label}</span>
			{value && <span className="shrink-0 tabular-nums">{value}</span>}
		</span>
	);
}

/** Sparkline 的纵向换算（视图高 24、上下各留 1）：值在区间里的比例 → 距顶部的百分比。 */
function topPercent(ratio: number): number {
	const r = Math.min(1, Math.max(0, ratio));
	return ((24 - r * 22 - 1) / 24) * 100;
}

/** 曲线 + 参考线 + 逐点悬停提示。 */
function SparkChart({
	block,
	variant,
}: {
	block: SparkBlock;
	variant: Variant;
}): ReactNode {
	const span = block.max - block.min;
	const range = span > 0 ? span : 1;
	const n = block.values.length;
	const tips = block.tips?.length === n && n > 1 ? block.tips : undefined;
	return (
		<span className="flex flex-col gap-1">
			<BlockHead label={block.label} value={block.last} />
			<span className={`relative block ${TEXT[block.tone ?? "accent"]}`}>
				<Sparkline
					values={block.values.map((v) => v - block.min)}
					max={range}
					label={block.label}
					className={variant === "card" ? "h-16!" : "h-8!"}
				/>
				{block.marks?.map((m) => (
					<span
						key={`${m.label}:${m.value}`}
						title={m.label}
						className={`pointer-events-none absolute inset-x-0 border-current border-t border-dashed opacity-60 ${TEXT[m.tone ?? "muted"]}`}
						style={{ top: `${topPercent((m.value - block.min) / range)}%` }}
					>
						{variant === "card" && (
							<span className="absolute right-0 bottom-0.5 text-2xs leading-none">
								{m.label}
							</span>
						)}
					</span>
				))}
				{tips?.map((tip, i) => {
					// 点没有稳定 id，位置即身份
					const key = i;
					const left = Math.max(0, (i - 0.5) / (n - 1));
					const right = Math.min(1, (i + 0.5) / (n - 1));
					return (
						<span
							key={key}
							title={tip}
							className="absolute inset-y-0 hover:bg-fg/8"
							style={{
								left: `${left * 100}%`,
								width: `${(right - left) * 100}%`,
							}}
						/>
					);
				})}
			</span>
		</span>
	);
}

/** 竖向柱状图：柱高 = 值 / 满格值；卡片里每根柱子下面标名字与读数。 */
function BarsChart({
	block,
	variant,
}: {
	block: BarsBlock;
	variant: Variant;
}): ReactNode {
	return (
		<span className="flex flex-col gap-1">
			<BlockHead label={block.label} />
			<span
				role="img"
				aria-label={`${block.label}: ${block.items.map((it) => `${it.label} ${it.text}`).join(", ")}`}
				className={`flex items-end gap-1 ${variant === "card" ? "h-20" : "h-10"}`}
			>
				{block.items.map((it) => (
					<span
						key={it.label}
						title={`${it.label} · ${it.text}`}
						className={`min-h-px min-w-1 max-w-12 flex-1 rounded-t-sm ${FILL[it.tone ?? "accent"]}`}
						style={{
							height: `${block.max > 0 ? Math.min(1, Math.max(0, it.value / block.max)) * 100 : 0}%`,
						}}
					/>
				))}
			</span>
			{variant === "card" && (
				<span className="flex gap-1 text-2xs leading-tight">
					{block.items.map((it) => (
						<span
							key={it.label}
							className="flex min-w-1 max-w-12 flex-1 flex-col overflow-hidden"
						>
							<span className="truncate text-faint">{it.label}</span>
							<span className="truncate text-muted tabular-nums">
								{it.text}
							</span>
						</span>
					))}
				</span>
			)}
		</span>
	);
}

function WidgetBlock({
	block,
	variant,
}: {
	block: Block;
	variant: Variant;
}): ReactNode {
	switch (block.kind) {
		case "meter":
			return (
				<span className="flex flex-col gap-1">
					<BlockHead label={block.label} value={block.text} />
					<Meter
						value={block.value * 100}
						label={block.label}
						tone={METER_TONE[block.tone ?? "neutral"]}
					/>
				</span>
			);
		case "spark":
			return <SparkChart block={block} variant={variant} />;
		case "bars":
			return <BarsChart block={block} variant={variant} />;
		case "stat":
			return (
				<span className="flex flex-wrap gap-x-4 gap-y-1.5">
					{block.items.map((it) => (
						<span key={it.label} className="flex min-w-0 flex-col">
							<span className="truncate text-faint text-xs">{it.label}</span>
							<span className="flex items-baseline gap-1.5">
								<span
									className={`font-medium text-sm tabular-nums ${TEXT[it.tone ?? "neutral"]}`}
								>
									{it.text}
								</span>
								{it.trend && <TrendText trend={it.trend} />}
							</span>
						</span>
					))}
				</span>
			);
		case "grid":
			return (
				<span className="flex flex-col gap-1">
					<BlockHead label={block.label} />
					<span className="flex flex-wrap gap-x-3 gap-y-1 text-xs">
						{block.items.map((it) => (
							<span
								key={it.label}
								className="inline-flex min-w-0 items-center gap-1.5"
							>
								<span
									aria-hidden
									className={`size-1.5 shrink-0 rounded-full ${FILL[it.tone]}`}
								/>
								<span className="truncate text-muted">{it.label}</span>
								<span className={`shrink-0 tabular-nums ${TEXT[it.tone]}`}>
									{it.text}
								</span>
							</span>
						))}
					</span>
				</span>
			);
		case "text":
			return (
				<span
					className={`block whitespace-pre-wrap text-muted text-xs ${variant === "row" ? "line-clamp-2" : ""}`}
				>
					{block.text}
				</span>
			);
		default:
			return null;
	}
}

/** 视图状态：模型还在写规格时、或没有规格只能显示最后一行与速率时，标出来。 */
function StateBadge({ state }: { state: JobWidget["state"] }): ReactNode {
	const { t } = useTranslation();
	if (state === "live") return null;
	return (
		<Badge
			tone={state === "authoring" ? "accent" : "neutral"}
			title={t(`jobs.widget.${state}Hint`)}
		>
			{t(`jobs.widget.${state}`)}
		</Badge>
	);
}

/** 对话框里的完整视图。 */
export function JobWidgetCard({ widget }: { widget: JobWidget }): ReactNode {
	return (
		<div className="flex flex-col gap-3 rounded-lg border border-border p-3">
			<div className="flex min-w-0 items-center gap-2">
				<span className="min-w-0 truncate font-medium text-sm">
					{widget.title}
				</span>
				<span className="ml-auto shrink-0">
					<StateBadge state={widget.state} />
				</span>
			</div>
			{widget.headline && (
				<HeadlineValue headline={widget.headline} variant="card" />
			)}
			{widget.stage && (
				<p className="m-0 truncate font-mono text-muted text-xs">
					{widget.stage}
				</p>
			)}
			{widget.blocks.map((b, i) => {
				// 块没有稳定 id，顺序即身份
				const key = i;
				return <WidgetBlock key={key} block={b} variant="card" />;
			})}
		</div>
	);
}

/** 列表行里的紧凑版：读数 + 第一块；两者都没有时不占位。 */
export function JobWidgetCompact({ widget }: { widget: JobWidget }): ReactNode {
	const first = widget.blocks[0];
	if (!widget.headline && !first) return null;
	return (
		<span className="flex min-w-0 flex-col gap-1.5 pt-1 pb-0.5">
			{widget.headline && (
				<HeadlineValue headline={widget.headline} variant="row" />
			)}
			{first && <WidgetBlock block={first} variant="row" />}
		</span>
	);
}
