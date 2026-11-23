/**
 * 信息面板的版式件：纸面上只有一层底色。
 * - `InfoBlock`：无描边的浅色块（orb、CPU / 内存、上下文），块里不再套第二层底色；
 * - `InfoGroup`：直接铺在纸面上的列表分组（待办、子 agent），标题可折叠，箭头在标题之后；
 * - `InfoRow`：纸面上的入口行（服务、环境、能力、SSH 主机），右侧状态标签，点开在行下展开详情；
 * - `InfoLink`：纸面上只做跳转的入口行（浏览器 Profile、报告与旁路提问）；
 * - `Spec` / `BigValue` / `OrbMetricTiles`：orb 规格项、大号读数与 CPU / 内存小块（线程信息面板与助手的电脑共用）。
 */
import { type ReactNode, useId, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import { formatMemoryMB, formatPercent } from "../../lib/format";
import { uiStore, useUi } from "../../state/ui";
import { type Tone, thresholdTone } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { Sparkline } from "../ui/primitives";

/** 入口行的展开状态：默认收起（展开才查询的小节就靠它），`collapsedCards` 里记「已展开」标记，键名以 `:expanded` 结尾。 */
export function useExpandedKey(key: `${string}:expanded`) {
	const expanded = useUi((s) => !!s.collapsedCards[key]);
	return {
		expanded,
		onToggle: () => uiStore.getState().toggleCard(key),
	};
}

export function InfoBlock({
	title,
	icon,
	action,
	children,
	className = "",
}: {
	title?: ReactNode;
	icon?: IconName;
	action?: ReactNode;
	children: ReactNode;
	className?: string;
}): ReactNode {
	return (
		<section
			className={`relative overflow-hidden rounded-lg bg-raised p-3.5 ${className}`}
		>
			{title && (
				<header className="relative z-[1] flex h-5 items-center gap-1.5 font-semibold text-muted text-sm">
					{icon && (
						<Icon name={icon} size={14} aria-hidden className="text-faint" />
					)}
					<h3 className="min-w-0 flex-1 truncate">{title}</h3>
					{action && <span className="-my-1 -mr-1.5 flex">{action}</span>}
				</header>
			)}
			{children}
		</section>
	);
}

export function InfoGroup({
	title,
	icon,
	sub,
	right,
	collapsed,
	onToggle,
	children,
}: {
	title: string;
	icon: IconName;
	/** 标题后的灰字（如待办只有一个阶段时的阶段名） */
	sub?: ReactNode;
	/** 标题行右侧（进度、运行计数），收起时照常显示 */
	right?: ReactNode;
	collapsed: boolean;
	onToggle: () => void;
	children: ReactNode;
}): ReactNode {
	const bodyId = useId();
	return (
		<section>
			<header className="group/head flex h-7 items-center gap-2 px-3.5 font-semibold text-sm">
				<Icon name={icon} size={16} aria-hidden className="text-faint" />
				{/* 截断放在按钮自己身上：标题若裁剪溢出，会把按钮的焦点环一起切掉 */}
				<h3 className="flex min-w-0 shrink">
					<button
						type="button"
						aria-expanded={!collapsed}
						aria-controls={bodyId}
						onClick={onToggle}
						className="min-w-0 cursor-pointer truncate"
					>
						{title}
					</button>
				</h3>
				{sub && (
					<span className="min-w-0 truncate font-medium text-muted">{sub}</span>
				)}
				{/* 展开时箭头只在悬停 / 聚焦时出现，收起时常驻 */}
				<Icon
					name={collapsed ? "chevronRight" : "chevronDown"}
					size={12}
					aria-hidden
					className={`-ml-1 shrink-0 text-ghost transition-opacity duration-150 ${collapsed ? "" : "opacity-0 group-focus-within/head:opacity-100 group-hover/head:opacity-100"}`}
				/>
				{right && (
					<span className="ml-auto flex shrink-0 items-center gap-2 font-medium text-faint text-xs tabular-nums">
						{right}
					</span>
				)}
			</header>
			<div id={bodyId} hidden={collapsed} className="mt-0.5">
				{children}
			</div>
		</section>
	);
}

const TAG_TONE: Record<Tone, string> = {
	neutral: "bg-faint/12 text-muted",
	accent: "bg-accent-soft text-accent-ink",
	ok: "bg-ok/12 text-ok-ink",
	warn: "bg-warn/12 text-warn-ink",
	err: "bg-err/12 text-err",
	frost: "bg-frost/12 text-frost",
};

/** 入口行右侧的状态标签：实色淡底、无描边。行窄时可收缩，内容在标签里从右截断（不让整枚标签从左边被裁掉）。 */
export function InfoTag({
	tone = "neutral",
	title,
	children,
}: {
	tone?: Tone;
	title?: string;
	children: ReactNode;
}): ReactNode {
	return (
		<span
			title={title}
			className={`inline-flex h-5 min-w-0 max-w-44 items-center gap-1 overflow-hidden whitespace-nowrap rounded-full px-2 font-semibold text-2xs ${TAG_TONE[tone]}`}
		>
			{children}
		</span>
	);
}

/** 一行最多显示的标签数，其余收成「+N」。 */
const MAX_TAGS = 2;

/** 入口行右侧：可选的灰字，加最多两个标签（多出的收成「+N」）。 */
export function InfoTags({
	meta,
	tags = [],
}: {
	meta?: ReactNode;
	tags?: readonly { key: string; node: ReactNode }[];
}): ReactNode {
	const hidden = tags.length - MAX_TAGS;
	return (
		<>
			{meta && (
				<span className="min-w-0 truncate text-faint text-sm">{meta}</span>
			)}
			{tags.slice(0, MAX_TAGS).map((tag) => (
				<span key={tag.key} className="flex min-w-0">
					{tag.node}
				</span>
			))}
			{hidden > 0 && <InfoTag>+{hidden}</InfoTag>}
		</>
	);
}

const ROW = "relative flex h-10 items-center gap-2.5 rounded-md px-3.5";
const ROW_HOVER = "transition-colors duration-150 hover:bg-raised";

export function InfoRow({
	icon,
	title,
	status,
	expanded,
	onToggle,
	action,
	children,
	testId,
}: {
	icon: IconName;
	title: string;
	/** 行右侧的灰字与标签（`InfoTags`） */
	status?: ReactNode;
	expanded: boolean;
	onToggle: () => void;
	/** 展开时显示在箭头前的动作（刷新、添加），叠在整行点击区之上 */
	action?: ReactNode;
	children: ReactNode;
	testId?: string;
}): ReactNode {
	const bodyId = useId();
	const ref = useRef<HTMLElement>(null);
	const toggle = () => {
		const opening = !expanded;
		onToggle();
		// 行常在纸面底部：展开后把明细滚进可视区，免得点了看不出变化
		if (opening)
			requestAnimationFrame(() =>
				ref.current?.scrollIntoView?.({ block: "nearest", behavior: "smooth" }),
			);
	};
	return (
		<section ref={ref} data-testid={testId}>
			<div
				className={`${ROW} ${ROW_HOVER} has-[>h3>button:focus-visible]:outline-2 has-[>h3>button:focus-visible]:outline-[var(--ring)] has-[>h3>button:focus-visible]:-outline-offset-2`}
			>
				<Icon
					name={icon}
					size={18}
					aria-hidden
					className="shrink-0 text-faint"
				/>
				<h3 className="shrink-0 text-sm">
					<button
						type="button"
						aria-expanded={expanded}
						aria-controls={bodyId}
						onClick={toggle}
						className="cursor-pointer outline-none after:absolute after:inset-0 after:content-['']"
					>
						{title}
					</button>
				</h3>
				<span className="ml-auto flex min-w-0 items-center justify-end gap-1.5 overflow-hidden">
					{status}
				</span>
				{expanded && action && (
					<span className="relative z-10 -my-1 flex shrink-0">{action}</span>
				)}
				<Icon
					name="chevronRight"
					size={14}
					aria-hidden
					className={`-ml-0.5 shrink-0 text-ghost transition-transform duration-150 ${expanded ? "rotate-90" : ""}`}
				/>
			</div>
			<div
				id={bodyId}
				hidden={!expanded}
				className="pt-0.5 pr-3.5 pb-2 pl-[46px]"
			>
				{expanded && children}
			</div>
		</section>
	);
}

/** 只做跳转的入口行；没有 `onClick` 时是静态的一行（不画箭头）。 */
export function InfoLink({
	icon,
	title,
	status,
	onClick,
	testId,
}: {
	icon: IconName;
	title: string;
	status?: ReactNode;
	onClick?: () => void;
	testId?: string;
}): ReactNode {
	const body = (
		<>
			<Icon name={icon} size={18} aria-hidden className="shrink-0 text-faint" />
			<span className="shrink-0 text-sm">{title}</span>
			<span className="ml-auto flex min-w-0 items-center justify-end gap-1.5 overflow-hidden">
				{status}
			</span>
			{onClick && (
				<Icon
					name="chevronRight"
					size={14}
					aria-hidden
					className="-ml-0.5 shrink-0 text-ghost"
				/>
			)}
		</>
	);
	return onClick ? (
		<button
			type="button"
			data-testid={testId}
			onClick={onClick}
			className={`${ROW} ${ROW_HOVER} w-full text-left`}
		>
			{body}
		</button>
	) : (
		<div data-testid={testId} className={ROW}>
			{body}
		</div>
	);
}

/** 规格的一项：图标 + 值，不加底。 */
export function Spec({
	icon,
	className = "",
	children,
}: {
	icon: IconName;
	className?: string;
	children: ReactNode;
}): ReactNode {
	return (
		<span
			className={`inline-flex min-w-0 items-center gap-1.5 whitespace-nowrap ${className}`}
		>
			<Icon name={icon} size={14} aria-hidden className="shrink-0 text-faint" />
			{children}
		</span>
	);
}

/** 大号读数：数字用大字，单位（%、GB）与 `suffix` 小一号灰字。 */
export function BigValue({
	value,
	suffix,
	className = "",
}: {
	value: string;
	suffix?: string;
	className?: string;
}): ReactNode {
	const m = /^([\d.,]+)\s*(.*)$/.exec(value);
	const unit = [m?.[2], suffix].filter(Boolean).join(" ");
	return (
		<div className={`font-semibold tabular-nums tracking-tight ${className}`}>
			{m ? m[1] : value}
			{unit && (
				<small className="ml-0.5 font-semibold text-faint text-sm tracking-normal">
					{unit}
				</small>
			)}
		</div>
	);
}

/** 迷你曲线：CPU 用强调色、内存用成功色，越过阈值改染警告 / 错误色。 */
function chartTone(tone: Tone, base: string): string {
	if (tone === "warn") return "text-warn";
	if (tone === "err") return "text-err";
	return base;
}

/** CPU / 内存小块：标题、大号读数，面积曲线铺满下半部、贴住块底边（颜色由 `className` 的文字色决定）。 */
function MetricTile({
	icon,
	label,
	value,
	suffix,
	chart,
	className,
}: {
	icon: IconName;
	label: string;
	value: string;
	suffix?: string;
	chart: ReactNode;
	className: string;
}): ReactNode {
	return (
		<InfoBlock icon={icon} title={label} className="h-28 pt-3 pb-0">
			<BigValue
				value={value}
				suffix={suffix}
				className="relative z-[1] mt-1 text-2xl leading-8"
			/>
			<div className={`absolute inset-x-0 bottom-0 ${className}`}>{chart}</div>
		</InfoBlock>
	);
}

/** orb 的 CPU / 内存两块（`GET orbs/{id}/metrics` 的采样，旧 → 新）：最新读数 + 面积曲线。 */
export function OrbMetricTiles({
	samples,
}: {
	samples: readonly Schemas["OrbMetricSample"][];
}): ReactNode {
	const { t } = useTranslation();
	const last = samples[samples.length - 1];
	const cpuTone = last ? thresholdTone(last.cpuPct) : "neutral";
	const memLimit = last?.memLimitMB ?? 0;
	const memTone =
		last && memLimit > 0
			? thresholdTone((last.memUsedMB / memLimit) * 100)
			: "neutral";
	return (
		<div className="grid grid-cols-2 gap-2">
			<MetricTile
				icon="cpu"
				label={t("panel.cpu")}
				value={last ? formatPercent(last.cpuPct) : "—"}
				className={chartTone(cpuTone, "text-accent")}
				chart={
					<Sparkline
						values={samples.map((s) => s.cpuPct)}
						max={100}
						label={t("panel.cpuChart")}
						className="h-12!"
					/>
				}
			/>
			<MetricTile
				icon="memory"
				label={t("panel.memory")}
				value={last ? formatMemoryMB(last.memUsedMB) : "—"}
				suffix={memLimit > 0 ? `/ ${formatMemoryMB(memLimit)}` : undefined}
				className={chartTone(memTone, "text-ok")}
				chart={
					<Sparkline
						values={samples.map((s) => s.memUsedMB)}
						max={memLimit || undefined}
						label={t("panel.memChart")}
						className="h-12!"
					/>
				}
			/>
		</div>
	);
}
