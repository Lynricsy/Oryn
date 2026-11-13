/**
 * 上下文分段的彩色堆叠量表、图例与分类表：信息面板的上下文卡与上下文报告对话框共用。
 * 分段数据见 `lib/context-segments.ts`；颜色是 index.css 的 `--ctx-*` 令牌，剩余空间不着色（轨道底色）。
 */
import { type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import {
	type ContextBreakdown,
	type ContextPart,
	type ContextPartId,
	contextParts,
	formatShare,
	partTitle,
} from "../../lib/context-segments";
import { formatCount, formatTokens } from "../../lib/format";
import { useThread } from "../../state/stores";
import { Badge, type Tone } from "../ui/data";

const PART_COLOR: Record<ContextPartId, string> = {
	systemPrompt: "var(--ctx-system-prompt)",
	systemContext: "var(--ctx-system-context)",
	instructions: "var(--ctx-instructions)",
	skills: "var(--ctx-skills)",
	systemTools: "var(--ctx-system-tools)",
	userMessages: "var(--ctx-user-messages)",
	assistantMessages: "var(--ctx-assistant-messages)",
	thinking: "var(--ctx-thinking)",
	toolCalls: "var(--ctx-tool-calls)",
	toolOutput: "var(--ctx-tool-output)",
	messages: "var(--ctx-messages)",
};

/** 占用偏高时用条外一圈描边表达阈值，分段照常着色。 */
const BAR_RING: Record<Tone, string> = {
	neutral: "inset-ring-1 inset-ring-border/70",
	accent: "inset-ring-1 inset-ring-border/70",
	ok: "inset-ring-1 inset-ring-border/70",
	frost: "inset-ring-1 inset-ring-border/70",
	warn: "ring-1 ring-warn",
	err: "ring-1 ring-err",
};

/** 当前线程的分段：报告还没取到时为 null。消息部分按当前转录估算拆分，拆不出时整段。 */
export function useContextParts(
	threadId: string,
	breakdown: ContextBreakdown | undefined,
): ContextPart[] | null {
	const entries = useThread(threadId, (s) => s.entries);
	const leaf = useThread(threadId, (s) => s.leafEntryId);
	return useMemo(
		() =>
			breakdown && breakdown.usedTokens > 0
				? contextParts(breakdown, entries.length > 0 ? entries : null, leaf)
				: null,
		[breakdown, entries, leaf],
	);
}

function usePartLabel() {
	const { t } = useTranslation();
	return (p: ContextPart, contextWindow: number) => {
		const name = t(`report.ctx.${p.id}`);
		const note = p.estimated ? t("report.ctx.estimated") : undefined;
		return { name, title: partTitle(name, p.tokens, contextWindow, note) };
	};
}

/** 非零项按占比从大到小（图例与分类表的顺序；量表本身保持固定的语义顺序）。 */
function bySize(parts: readonly ContextPart[]): ContextPart[] {
	return parts.filter((p) => p.tokens > 0).sort((a, b) => b.tokens - a.tokens);
}

function Swatch({ id }: { id: ContextPartId }) {
	return (
		<i
			aria-hidden
			className="inline-block h-[7px] w-[7px] shrink-0 rounded-[2px]"
			style={{ background: PART_COLOR[id] }}
		/>
	);
}

/** 彩色堆叠量表：每段宽度 = tokens / 上下文窗口，剩余部分露出轨道。`compact`（卡片收起时的迷你条）段间不留缝。 */
export function ContextBar({
	parts,
	contextWindow,
	tone,
	label,
	compact = false,
	className = "",
}: {
	parts: readonly ContextPart[];
	contextWindow: number;
	tone: Tone;
	label: string;
	compact?: boolean;
	className?: string;
}): ReactNode {
	const describe = usePartLabel();
	const win = Math.max(1, contextWindow);
	const used = parts.reduce((a, p) => a + p.tokens, 0);
	return (
		// biome-ignore lint/a11y/useSemanticElements: 原生 <meter> 无法画分段
		<div
			role="meter"
			aria-label={label}
			aria-valuenow={Math.round((used / win) * 100)}
			aria-valuemin={0}
			aria-valuemax={100}
			className={`flex h-1.5 overflow-hidden rounded-full bg-inset ${compact ? "" : "gap-px"} ${BAR_RING[tone]} ${className}`}
		>
			{parts
				.filter((p) => p.tokens > 0)
				.map((p) => (
					<span
						key={p.id}
						title={describe(p, win).title}
						className="h-full"
						style={{
							width: `${(p.tokens / win) * 100}%`,
							background: PART_COLOR[p.id],
						}}
					/>
				))}
		</div>
	);
}

/** 图例（信息面板）：色点 + 名称 + 数值，只列非零项、按占比从大到小；估算值带「≈」并在末尾注明。 */
export function ContextLegend({
	parts,
	contextWindow,
}: {
	parts: readonly ContextPart[];
	contextWindow: number;
}): ReactNode {
	const { t } = useTranslation();
	const describe = usePartLabel();
	const shown = bySize(parts);
	return (
		<ul className="mt-[9px] flex flex-wrap gap-x-3 gap-y-1 text-2xs text-faint">
			{shown.map((p) => {
				const { name, title } = describe(p, contextWindow);
				return (
					<li
						key={p.id}
						title={title}
						className="inline-flex items-center gap-1.5 whitespace-nowrap"
					>
						<Swatch id={p.id} />
						{name}
						<b className="font-medium text-muted tabular-nums">
							{p.estimated && "≈"}
							{formatTokens(p.tokens)}
						</b>
					</li>
				);
			})}
			{shown.some((p) => p.estimated) && (
				<li className="whitespace-nowrap">
					≈ {t("report.ctx.estimated")}
				</li>
			)}
		</ul>
	);
}

/** 分类表（上下文报告对话框）：色点、名称（估算项带徽标）、tokens、占窗口百分比；末行为剩余空间。 */
export function ContextTable({
	parts,
	contextWindow,
}: {
	parts: readonly ContextPart[];
	contextWindow: number;
}): ReactNode {
	const { t } = useTranslation();
	const describe = usePartLabel();
	const used = parts.reduce((a, p) => a + p.tokens, 0);
	const free = Math.max(0, contextWindow - used);
	const row = (
		key: string,
		swatch: ReactNode,
		name: ReactNode,
		tokens: number,
		title?: string,
	) => (
		<tr
			key={key}
			title={title}
			className="border-border border-t first:border-t-0"
		>
			<td className="py-1.5 pr-2 align-middle">{swatch}</td>
			<td className="w-full py-1.5 pr-3 text-muted">{name}</td>
			<td className="whitespace-nowrap py-1.5 pr-3 text-right tabular-nums">
				{formatCount(tokens)}
			</td>
			<td className="whitespace-nowrap py-1.5 text-right text-faint tabular-nums">
				{formatShare(tokens, contextWindow)}
			</td>
		</tr>
	);
	return (
		<table className="w-full text-xs">
			<tbody>
				{bySize(parts).map((p) => {
					const { name, title } = describe(p, contextWindow);
					return row(
						p.id,
						<Swatch id={p.id} />,
						<span className="inline-flex items-center gap-1.5">
							{name}
							{p.estimated && <Badge>{t("report.ctx.estimated")}</Badge>}
						</span>,
						p.tokens,
						title,
					);
				})}
				{free > 0 &&
					row(
						"free",
						<i
							aria-hidden
							className="block h-[7px] w-[7px] rounded-[2px] bg-inset inset-ring-1 inset-ring-border-strong"
						/>,
						t("report.ctx.free"),
						free,
					)}
			</tbody>
		</table>
	);
}
