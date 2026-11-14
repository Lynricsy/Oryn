/**
 * Space 近 30 天进度：纯 SVG，每天一根堆叠柱（已交付 = 成功色、普通归档 = 弱色）+ 一条「未结束」折线；
 * 悬停 / 点按某天在下方读数行显示当天三个数，缺省显示最近一天。自然日按浏览器时区（服务端按 `tz` 分日）。
 * 键盘与读屏（M9 审查 W8）：图表上方有一个视觉隐藏的 `input[type=range]` 选日期（左右键、Home、End、PageUp/Down 一周），
 * 聚焦时图表外框显示焦点环；`aria-valuetext` 读出当天三个数，读数行 `aria-live` 随之播报。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { SpaceProgressDay } from "../../api/space-queries";

const W = 300;
const H = 72;

/** 键盘选日期：键 → 相对当前选中日的偏移（Home / End 另算）。 */
const KEY_STEP: Record<string, number> = {
	ArrowLeft: -1,
	ArrowDown: -1,
	ArrowRight: 1,
	ArrowUp: 1,
	PageUp: -7,
	PageDown: 7,
};

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
	// 点按 / 键盘选定的日子；悬停只是临时查看，移开就回到选定的那天
	const [pick, setPick] = useState<number | null>(null);
	const [hover, setHover] = useState<number | null>(null);
	const n = days.length;
	const sel = hover ?? pick ?? n - 1;
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
	const choose = (i: number) => {
		setHover(null);
		setPick(Math.min(n - 1, Math.max(0, i)));
	};
	return (
		<div data-testid="space-progress" className="flex flex-col gap-2">
			{n > 0 && (
				<input
					type="range"
					data-testid="space-progress-day"
					className="peer sr-only"
					min={0}
					max={n - 1}
					step={1}
					value={sel}
					aria-label={t("space.progress.pickDay")}
					aria-valuetext={t("space.progress.dayValue", {
						date: day?.date ?? "",
						shipped: day?.shipped ?? 0,
						archived: day?.archived ?? 0,
						open: day?.open ?? 0,
					})}
					onChange={(e) => choose(Number(e.target.value))}
					onKeyDown={(e) => {
						// 自己处理按键：不依赖各引擎对 range 的默认步进，选中的总是当前读数那天的相邻日
						const delta = KEY_STEP[e.key];
						const next =
							e.key === "Home"
								? 0
								: e.key === "End"
									? n - 1
									: delta !== undefined
										? sel + delta
										: null;
						if (next === null) return;
						e.preventDefault();
						choose(next);
					}}
				/>
			)}
			<div className="rounded-xs peer-focus-visible:outline-2 peer-focus-visible:outline-[var(--ring)] peer-focus-visible:outline-offset-2">
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
					onMouseLeave={() => setHover(null)}
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
							// biome-ignore lint/a11y/noStaticElementInteractions: 键盘与读屏走上面的日期滑块，柱子只是悬停 / 点按的命中区
							<g
								key={d.date}
								data-day={d.date}
								onMouseEnter={() => setHover(i)}
								onClick={() => choose(i)}
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
			</div>
			<div
				data-testid="space-progress-readout"
				aria-live="polite"
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
