/**
 * orb 状态时间线：一行色带（各段按时长占宽）+ 下方图例。颜色只表语义：运行 `--good`、setup `--caution`、
 * 冻结 `--frost`、失败 `--bad`、停机 `--line-strong`（中性，落在内嵌井底色上仍看得见）。
 * 交互与 StackedBars 一致：外层单焦点，方向键 / Home / End 选段，悬停（鼠标）或点按查看，点按固定，点图外或 Esc 收起；
 * 浮层列出状态、起止与时长。另有一份与各段一一对应的视觉隐藏列表，读屏不必交互也能逐段读到同样的信息。
 */
import { type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { OrbState } from "../../api/client";
import { formatDateTime, formatDuration } from "../../lib/format";
import { useElementWidth } from "../files/hooks";
import { ChartTooltip } from "./ChartTooltip";
import { Legend } from "./Legend";
import {
	type Selection,
	stepSelection,
	useOutsideDismiss,
} from "./StackedBars";

export type TimelineGroup =
	| "running"
	| "setup"
	| "frozen"
	| "failed"
	| "stopped";

export const TIMELINE_COLOR: Record<TimelineGroup, string> = {
	running: "--good",
	setup: "--caution",
	frozen: "--frost",
	failed: "--bad",
	stopped: "--line-strong",
};

const ORDER: readonly TimelineGroup[] = [
	"running",
	"setup",
	"frozen",
	"stopped",
	"failed",
];

/** orb 状态归到时间线的五类（与统计口径一致：setup 也算运行时长，只是单独着色）。 */
export function timelineGroup(state: OrbState | string): TimelineGroup {
	switch (state) {
		case "setting_up":
		case "resuming":
			return "setup";
		case "frozen":
			return "frozen";
		case "failed":
			return "failed";
		case "stopped":
		case "deleting":
		case "deleted":
			return "stopped";
		default:
			return "running";
	}
}

export function TimelineStrip({
	from,
	to,
	segments,
	ariaLabel,
}: {
	from: number;
	to: number;
	segments: { from: number; to: number; state: string }[];
	ariaLabel: string;
}): ReactNode {
	const { t } = useTranslation();
	const ref = useRef<HTMLDivElement>(null);
	const width = useElementWidth(ref, 400);
	const [sel, setSel] = useState<Selection | null>(null);
	useOutsideDismiss(ref, sel, () => setSel(null));
	const span = to > from ? to - from : 1;
	// 落在窗口内的段：a / b 是截到窗口后的绘制范围，起止与时长的文字用段本身的 from / to
	const shown = segments.flatMap((s) => {
		const a = Math.max(from, s.from);
		const b = Math.min(to, s.to);
		if (b <= a) return [];
		const g = timelineGroup(s.state);
		return [
			{
				...s,
				a,
				b,
				g,
				label: t(`stats.orbs.state.${g}`),
				start: formatDateTime(s.from),
				end: formatDateTime(s.to),
				duration: formatDuration(s.to - s.from),
			},
		];
	});
	const n = shown.length;
	const present = new Set(shown.map((s) => s.g));
	const cur = sel && sel.i < n ? shown[sel.i] : undefined;
	/** 指针所在的段；窄到点不中的段取离指针最近的一段。 */
	const at = (clientX: number): number => {
		const rect = ref.current?.getBoundingClientRect();
		const ts =
			from + ((clientX - (rect?.left ?? 0)) / (rect?.width || width)) * span;
		let best = 0;
		let gap = Number.POSITIVE_INFINITY;
		shown.forEach((s, i) => {
			const d = ts < s.a ? s.a - ts : ts > s.b ? ts - s.b : 0;
			if (d < gap) {
				gap = d;
				best = i;
			}
		});
		return best;
	};
	return (
		<div className="flex flex-col gap-1.5">
			{/* biome-ignore lint/a11y/useSemanticElements: 不是 fieldset，是可聚焦的图表控件（方向键选段） */}
			<div
				ref={ref}
				role="group"
				aria-label={ariaLabel}
				// biome-ignore lint/a11y/noNoninteractiveTabindex: 时间线要能聚焦才能用方向键选段
				tabIndex={0}
				data-testid="timeline-strip"
				// 上下各 8px 透明命中区，触屏点按 12px 高的色带也不费劲；负外边距抵掉，不改版面
				className="relative -my-2 select-none rounded-xs py-2 outline-none focus-visible:outline-2 focus-visible:outline-[var(--ring)]"
				onKeyDown={(e) => stepSelection(e, n, sel, setSel)}
				onPointerMove={(e) => {
					if (e.pointerType === "mouse" && n > 0)
						setSel((s) =>
							s?.pinned ? s : { i: at(e.clientX), pinned: false },
						);
				}}
				onPointerLeave={() => setSel((s) => (s?.pinned ? s : null))}
				onClick={(e) => {
					if (n === 0) return;
					const i = at(e.clientX);
					setSel((s) => (s?.pinned && s.i === i ? null : { i, pinned: true }));
				}}
			>
				<div
					aria-hidden
					className="relative h-3 w-full overflow-hidden rounded-xs bg-inset"
				>
					{shown.map((s, i) => (
						<span
							key={`${s.from}-${s.state}`}
							data-state={s.state}
							data-selected={sel?.i === i ? "" : undefined}
							className="absolute inset-y-0 data-selected:outline-2 data-selected:outline-fg data-selected:-outline-offset-2"
							style={{
								left: `${((s.a - from) / span) * 100}%`,
								width: `${((s.b - s.a) / span) * 100}%`,
								background: `var(${TIMELINE_COLOR[s.g]})`,
							}}
						/>
					))}
				</div>
				{cur && (
					// 浮层挂在色带下方，不挡住选中的段
					<div className="pointer-events-none absolute inset-x-0 top-full">
						<ChartTooltip
							title={
								<span className="flex items-center gap-2">
									<span
										aria-hidden
										className="h-2 w-2 shrink-0 rounded-full"
										style={{ background: `var(${TIMELINE_COLOR[cur.g]})` }}
									/>
									{cur.label}
								</span>
							}
							x={(((cur.a + cur.b) / 2 - from) / span) * width}
							width={width}
							rows={[
								{
									key: "start",
									label: t("stats.chart.start"),
									value: cur.start,
								},
								{ key: "end", label: t("stats.chart.end"), value: cur.end },
							]}
							total={{ label: t("stats.chart.duration"), value: cur.duration }}
						/>
					</div>
				)}
			</div>
			<ul
				className="sr-only"
				aria-label={t("stats.chart.segments", { name: ariaLabel })}
				data-testid="timeline-segments"
			>
				{shown.map((s) => (
					<li key={`${s.from}-${s.state}`}>
						{t("stats.chart.segment", {
							state: s.label,
							start: s.start,
							end: s.end,
							duration: s.duration,
						})}
					</li>
				))}
			</ul>
			<Legend
				items={ORDER.filter((g) => present.has(g)).map((g) => ({
					key: g,
					label: t(`stats.orbs.state.${g}`),
					color: TIMELINE_COLOR[g],
				}))}
			/>
		</div>
	);
}
