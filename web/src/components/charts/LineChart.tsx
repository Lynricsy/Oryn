/**
 * 折线 / 面积图（纯 SVG）：orb 抽屉里的 CPU、内存曲线。`ys` 里的 null 是缺测，折线在那里断开。
 * 悬停（鼠标）或点按取最近的采样点，浮层列出各系列的值；点按固定显示，点图外空白处或 Esc 收起。
 */
import { type ReactNode, useRef, useState } from "react";
import { useBreakpoint } from "../../lib/viewport";
import { useElementWidth } from "../files/hooks";
import { ChartTooltip } from "./ChartTooltip";
import {
	type ChartSeries,
	PAD,
	type Selection,
	stepSelection,
	useOutsideDismiss,
} from "./StackedBars";
import { niceScale, thin } from "./scale";

const LABEL_PX = 72;
const TICKS = 4;

export interface LinePoint {
	x: number;
	ys: (number | null)[];
}

/** 一条系列按 null 断开成若干段，每段是 [x, y] 像素坐标。 */
function runs(
	points: LinePoint[],
	k: number,
	px: (x: number) => number,
	py: (y: number) => number,
): [number, number][][] {
	const out: [number, number][][] = [];
	let cur: [number, number][] = [];
	for (const p of points) {
		const v = p.ys[k];
		if (v === null || v === undefined || !Number.isFinite(v)) {
			if (cur.length) out.push(cur);
			cur = [];
			continue;
		}
		cur.push([px(p.x), py(v)]);
	}
	if (cur.length) out.push(cur);
	return out;
}

export function LineChart({
	points,
	series,
	yMax,
	formatX,
	formatY,
	height,
	ariaLabel,
}: {
	points: LinePoint[];
	series: ChartSeries[];
	/** 纵轴上限（如内存上限）；缺省按数据取整 */
	yMax?: number;
	formatX: (x: number) => string;
	formatY: (y: number) => string;
	height?: number;
	ariaLabel: string;
}): ReactNode {
	const bp = useBreakpoint();
	const h = height ?? (bp === "compact" ? 140 : 160);
	const ref = useRef<HTMLDivElement>(null);
	const width = useElementWidth(ref, 400);
	const [sel, setSel] = useState<Selection | null>(null);
	useOutsideDismiss(ref, sel, () => setSel(null));
	const n = points.length;
	const dataMax = Math.max(
		0,
		...points.flatMap((p) =>
			p.ys.filter((v): v is number => v !== null && Number.isFinite(v)),
		),
	);
	const scale =
		yMax !== undefined && yMax > 0
			? { max: yMax, step: yMax / TICKS }
			: niceScale(dataMax, TICKS);
	const plotW = Math.max(0, width - PAD.left - PAD.right);
	const plotH = Math.max(0, h - PAD.top - PAD.bottom);
	const x0 = points[0]?.x ?? 0;
	const x1 = points[n - 1]?.x ?? 1;
	const span = x1 > x0 ? x1 - x0 : 1;
	const px = (x: number) => PAD.left + ((x - x0) / span) * plotW;
	const py = (v: number) =>
		PAD.top + plotH - (Math.min(v, scale.max) / scale.max) * plotH;
	const show = thin(points, plotW / LABEL_PX);
	const ticks = Array.from({ length: TICKS + 1 }, (_, k) => k * scale.step);
	const nearest = (clientX: number): number => {
		const rect = ref.current?.getBoundingClientRect();
		const x = clientX - (rect?.left ?? 0);
		const ratio = plotW > 0 ? (x - PAD.left) / plotW : 0;
		const target = x0 + ratio * span;
		let best = 0;
		for (let i = 1; i < n; i++)
			if (
				Math.abs((points[i]?.x ?? 0) - target) <
				Math.abs((points[best]?.x ?? 0) - target)
			)
				best = i;
		return best;
	};
	const cur = sel && sel.i < n ? sel.i : null;
	const curPoint = cur !== null ? points[cur] : undefined;
	return (
		// biome-ignore lint/a11y/useSemanticElements: 不是 fieldset，是可聚焦的图表控件（方向键选采样点）
		<div
			ref={ref}
			role="group"
			aria-label={ariaLabel}
			// biome-ignore lint/a11y/noNoninteractiveTabindex: 图表要能聚焦才能用方向键选采样点
			tabIndex={0}
			data-testid="line-chart"
			className="relative w-full select-none rounded-md outline-none focus-visible:outline-2 focus-visible:outline-[var(--ring)]"
			onKeyDown={(e) => stepSelection(e, n, sel, setSel)}
			onPointerMove={(e) => {
				if (e.pointerType === "mouse" && n > 0)
					setSel((s) =>
						s?.pinned ? s : { i: nearest(e.clientX), pinned: false },
					);
			}}
			onPointerLeave={() => setSel((s) => (s?.pinned ? s : null))}
			onClick={(e) => {
				if (n === 0) return;
				const i = nearest(e.clientX);
				setSel((s) => (s?.pinned && s.i === i ? null : { i, pinned: true }));
			}}
		>
			<svg width={width} height={h} aria-hidden className="block">
				{ticks.map((v) => (
					<g key={v}>
						<line
							x1={PAD.left}
							x2={PAD.left + plotW}
							y1={py(v)}
							y2={py(v)}
							stroke="var(--line)"
							strokeWidth={1}
						/>
						<text
							x={PAD.left - 6}
							y={py(v)}
							dy="0.32em"
							textAnchor="end"
							fontSize={11}
							fill="var(--text-faint)"
						>
							{formatY(v)}
						</text>
					</g>
				))}
				{series.map((s, k) =>
					runs(points, k, px, py).map((run) => {
						const line = run.map(([x, y]) => `${x},${y}`).join(" ");
						const first = run[0];
						const last = run[run.length - 1];
						const base = PAD.top + plotH;
						return (
							<g key={`${s.key}-${first?.[0]}`} data-series={s.key}>
								{first && last && (
									<polygon
										points={`${first[0]},${base} ${line} ${last[0]},${base}`}
										fill={`var(${s.color})`}
										fillOpacity={0.12}
									/>
								)}
								<polyline
									points={line}
									fill="none"
									stroke={`var(${s.color})`}
									strokeWidth={1.5}
									strokeLinejoin="round"
									strokeLinecap="round"
								/>
							</g>
						);
					}),
				)}
				{points.map((p, i) =>
					show[i] ? (
						<text
							key={p.x}
							x={px(p.x)}
							y={h - 6}
							textAnchor={i === 0 ? "start" : "middle"}
							fontSize={11}
							fill="var(--text-faint)"
						>
							{formatX(p.x)}
						</text>
					) : null,
				)}
				{curPoint && (
					<line
						x1={px(curPoint.x)}
						x2={px(curPoint.x)}
						y1={PAD.top}
						y2={PAD.top + plotH}
						stroke="var(--line-strong)"
						strokeWidth={1}
					/>
				)}
			</svg>
			{curPoint && (
				<ChartTooltip
					title={formatX(curPoint.x)}
					x={px(curPoint.x)}
					width={width}
					rows={series.map((s, k) => {
						const v = curPoint.ys[k];
						return {
							key: s.key,
							label: s.label,
							color: s.color,
							value: v === null || v === undefined ? "—" : formatY(v),
						};
					})}
				/>
			)}
		</div>
	);
}
