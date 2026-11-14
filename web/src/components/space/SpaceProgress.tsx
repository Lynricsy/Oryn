/**
 * Space 近 30 天进度：纯 SVG，每天一根堆叠柱（已交付 = 成功色、普通归档 = 弱色）+ 一条「未结束」折线；
 * 悬停 / 点按某天在下方读数行显示当天三个数，缺省显示最近一天。自然日按浏览器时区（服务端按 `tz` 分日）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { SpaceProgressDay } from "../../api/space-queries";

const W = 300;
const H = 72;

/** 纵轴上限：当天交付 + 归档与未结束数里的最大值（至少 1）。 */
export function progressMax(days: readonly SpaceProgressDay[]): number {
	return Math.max(
		1,
		...days.map((d) => Math.max(d.shipped + d.archived, d.open)),
	);
}

export function SpaceProgress({
	days,
}: {
	days: readonly SpaceProgressDay[];
}): ReactNode {
	const { t } = useTranslation();
	const [pick, setPick] = useState<number | null>(null);
	const n = days.length;
	const sel = pick ?? n - 1;
	const day = days[sel];
	const max = progressMax(days);
	const slot = n > 0 ? W / n : W;
	const bar = Math.max(2, slot * 0.62);
	const y = (v: number) => H - (v / max) * (H - 4);
	const line = days
		.map(
			(d, i) => `${(i * slot + slot / 2).toFixed(1)},${y(d.open).toFixed(1)}`,
		)
		.join(" ");
	const shipped = days.reduce((s, d) => s + d.shipped, 0);
	const archived = days.reduce((s, d) => s + d.archived, 0);
	return (
		<div data-testid="space-progress" className="flex flex-col gap-2">
			<svg
				viewBox={`0 0 ${W} ${H}`}
				preserveAspectRatio="none"
				className="block h-[72px] w-full"
				role="img"
				aria-label={t("space.progress.summary", {
					days: n,
					shipped,
					archived,
					open: days[n - 1]?.open ?? 0,
				})}
				onMouseLeave={() => setPick(null)}
			>
				<line
					x1={0}
					x2={W}
					y1={H - 0.5}
					y2={H - 0.5}
					className="stroke-border"
					strokeWidth={1}
					vectorEffect="non-scaling-stroke"
				/>
				{days.map((d, i) => {
					const x = i * slot + (slot - bar) / 2;
					const ys = y(d.shipped);
					const ya = y(d.shipped + d.archived);
					return (
						// biome-ignore lint/a11y/noStaticElementInteractions: 读数行同时给出文字，柱子只是悬停 / 点按的命中区
						<g
							key={d.date}
							data-day={d.date}
							onMouseEnter={() => setPick(i)}
							onClick={() => setPick(i)}
							className={i === sel ? "" : "opacity-80"}
						>
							<rect
								x={i * slot}
								y={0}
								width={slot}
								height={H}
								className={i === sel ? "fill-hover" : "fill-transparent"}
							/>
							{d.shipped > 0 && (
								<rect
									data-kind="shipped"
									x={x}
									y={ys}
									width={bar}
									height={H - ys}
									className="fill-ok"
								/>
							)}
							{d.archived > 0 && (
								<rect
									data-kind="archived"
									x={x}
									y={ya}
									width={bar}
									height={ys - ya}
									className="fill-border-strong"
								/>
							)}
						</g>
					);
				})}
				{n > 1 && (
					<polyline
						data-kind="open"
						points={line}
						fill="none"
						className="stroke-muted"
						strokeWidth={1.5}
						strokeLinejoin="round"
						strokeLinecap="round"
						vectorEffect="non-scaling-stroke"
						pointerEvents="none"
					/>
				)}
			</svg>
			<div
				data-testid="space-progress-readout"
				className="flex flex-wrap items-center gap-x-3 gap-y-1 text-faint text-xs tabular-nums"
			>
				<span className="text-muted">{day?.date ?? "—"}</span>
				<span className="inline-flex items-center gap-1">
					<span aria-hidden className="h-2 w-2 rounded-xs bg-ok" />
					{t("space.progress.shipped", { n: day?.shipped ?? 0 })}
				</span>
				<span className="inline-flex items-center gap-1">
					<span aria-hidden className="h-2 w-2 rounded-xs bg-border-strong" />
					{t("space.progress.archived", { n: day?.archived ?? 0 })}
				</span>
				<span className="inline-flex items-center gap-1">
					<span aria-hidden className="h-0.5 w-2.5 rounded-full bg-muted" />
					{t("space.progress.open", { n: day?.open ?? 0 })}
				</span>
			</div>
		</div>
	);
}
