/**
 * 堆叠柱图（纯 SVG，宽度由 ResizeObserver 量出）：每个桶一个 `<g data-bucket>`，系列自下而上堆叠。
 * 悬停（鼠标）、点按或方向键选中一个桶，浮层列出该桶各系列的值与合计；点按选中的桶固定显示，点图外空白处或 Esc 收起。
 * 颜色只用 CSS 变量（`ChartSeries.color` 是变量名），明暗与主题色切换不需要重画。
 */
import {
	type KeyboardEvent,
	type ReactNode,
	type RefObject,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useBreakpoint } from "../../lib/viewport";
import { useElementWidth } from "../files/hooks";
import { ChartTooltip } from "./ChartTooltip";
import { niceScale, thin } from "./scale";

export interface ChartSeries {
	key: string;
	label: string;
	/** CSS 变量名，如 `--viz-1` */
	color: string;
	/** 系列是模型时的 `<provider>/<model>`：图例与浮层在名字前放品牌 logo */
	model?: string;
}

/** 绘图区留白：左侧纵轴标签、底部横轴标签。 */
export const PAD = { left: 48, right: 8, top: 10, bottom: 22 } as const;
/** 横轴每个标签至少占的宽度（px），据此抽稀。 */
const LABEL_PX = 64;
const TICKS = 4;

/** 选中的桶：`pinned` = 点按 / 键盘选中（固定显示），否则是鼠标悬停（移出即收起）。 */
export interface Selection {
	i: number;
	pinned: boolean;
}

/** 选中态的共用逻辑：点图外空白处收起固定的选中。 */
export function useOutsideDismiss(
	ref: RefObject<HTMLElement | null>,
	sel: Selection | null,
	clear: () => void,
): void {
	const pinned = sel?.pinned ?? false;
	const clearRef = useRef(clear);
	clearRef.current = clear;
	useEffect(() => {
		if (!pinned) return;
		const onDown = (e: PointerEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node))
				clearRef.current();
		};
		document.addEventListener("pointerdown", onDown);
		return () => document.removeEventListener("pointerdown", onDown);
	}, [pinned, ref]);
}

/** 方向键在 n 个桶之间移动选中（固定显示）；Esc 收起。 */
export function stepSelection(
	e: KeyboardEvent,
	n: number,
	sel: Selection | null,
	set: (s: Selection | null) => void,
): void {
	if (n === 0) return;
	const cur = sel?.i ?? -1;
	let next: number | null = null;
	if (e.key === "ArrowRight") next = Math.min(n - 1, cur + 1);
	else if (e.key === "ArrowLeft") next = cur < 0 ? n - 1 : Math.max(0, cur - 1);
	else if (e.key === "Home") next = 0;
	else if (e.key === "End") next = n - 1;
	else if (e.key === "Escape" && sel) {
		e.preventDefault();
		set(null);
		return;
	}
	if (next === null) return;
	e.preventDefault();
	set({ i: next, pinned: true });
}

export function StackedBars({
	buckets,
	series,
	values,
	format,
	height,
	ariaLabel,
}: {
	buckets: { key: string; label: string }[];
	series: ChartSeries[];
	/** values[桶][系列] */
	values: number[][];
	format: (v: number) => string;
	/** 默认 220，compact 180 */
	height?: number;
	ariaLabel: string;
}): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const h = height ?? (bp === "compact" ? 180 : 220);
	const ref = useRef<HTMLDivElement>(null);
	const width = useElementWidth(ref, 600);
	const [sel, setSel] = useState<Selection | null>(null);
	useOutsideDismiss(ref, sel, () => setSel(null));
	const n = buckets.length;
	const totals = buckets.map((_, i) =>
		(values[i] ?? []).reduce((a, v) => a + Math.max(v, 0), 0),
	);
	const scale = niceScale(Math.max(0, ...totals), TICKS);
	const plotW = Math.max(0, width - PAD.left - PAD.right);
	const plotH = Math.max(0, h - PAD.top - PAD.bottom);
	const slot = n > 0 ? plotW / n : 0;
	const barW = Math.max(1, Math.min(48, slot * 0.68));
	const y = (v: number) => PAD.top + plotH - (v / scale.max) * plotH;
	const show = thin(buckets, plotW / LABEL_PX);
	const cur = sel && sel.i < n ? sel.i : null;
	const ticks = Array.from({ length: TICKS + 1 }, (_, k) => k * scale.step);
	return (
		// biome-ignore lint/a11y/useSemanticElements: 不是 fieldset，是可聚焦的图表控件（方向键选桶）
		<div
			ref={ref}
			role="group"
			aria-label={ariaLabel}
			// biome-ignore lint/a11y/noNoninteractiveTabindex: 图表要能聚焦才能用方向键选桶
			tabIndex={0}
			data-testid="stats-chart"
			className="relative w-full select-none rounded-md outline-none focus-visible:outline-2 focus-visible:outline-[var(--ring)]"
			onKeyDown={(e) => stepSelection(e, n, sel, setSel)}
			onPointerLeave={() => setSel((s) => (s?.pinned ? s : null))}
		>
			<svg width={width} height={h} aria-hidden className="block">
				{ticks.map((v) => (
					<g key={v}>
						<line
							x1={PAD.left}
							x2={PAD.left + plotW}
							y1={y(v)}
							y2={y(v)}
							stroke="var(--line)"
							strokeWidth={1}
						/>
						<text
							x={PAD.left - 6}
							y={y(v)}
							dy="0.32em"
							textAnchor="end"
							fontSize={11}
							fill="var(--text-faint)"
							className="tabular-nums"
						>
							{format(v)}
						</text>
					</g>
				))}
				{buckets.map((b, i) => {
					const cx = PAD.left + slot * (i + 0.5);
					let acc = 0;
					return (
						// biome-ignore lint/a11y/noStaticElementInteractions: 柱子的点按 / 悬停是鼠标与触屏的捷径，键盘在外层用方向键选桶
						<g
							key={b.key}
							data-bucket={b.key}
							data-selected={cur === i ? "" : undefined}
							onPointerEnter={(e) => {
								if (e.pointerType === "mouse")
									setSel((s) => (s?.pinned ? s : { i, pinned: false }));
							}}
							onClick={() =>
								setSel((s) =>
									s?.pinned && s.i === i ? null : { i, pinned: true },
								)
							}
						>
							<rect
								x={PAD.left + slot * i}
								y={PAD.top}
								width={slot}
								height={plotH}
								fill={cur === i ? "var(--hover)" : "none"}
								pointerEvents="all"
							/>
							{series.map((s, k) => {
								const v = values[i]?.[k] ?? 0;
								if (!(v > 0)) return null;
								const top = y(acc + v);
								const bottom = y(acc);
								acc += v;
								return (
									<rect
										key={s.key}
										data-series={s.key}
										x={cx - barW / 2}
										y={top}
										width={barW}
										height={Math.max(0, bottom - top)}
										fill={`var(${s.color})`}
										pointerEvents="none"
									/>
								);
							})}
							{show[i] && (
								<text
									x={cx}
									y={h - 6}
									textAnchor="middle"
									fontSize={11}
									fill="var(--text-faint)"
									pointerEvents="none"
								>
									{b.label}
								</text>
							)}
						</g>
					);
				})}
			</svg>
			{cur !== null && (
				<ChartTooltip
					title={buckets[cur]?.label}
					x={PAD.left + slot * (cur + 0.5)}
					width={width}
					rows={series
						.map((s, k) => ({ s, v: values[cur]?.[k] ?? 0 }))
						.filter(({ v }) => v > 0)
						.reverse()
						.map(({ s, v }) => ({
							key: s.key,
							label: s.label,
							color: s.color,
							model: s.model,
							value: format(v),
						}))}
					total={{
						label: t("stats.chart.total"),
						value: format(totals[cur] ?? 0),
					}}
				/>
			)}
		</div>
	);
}
