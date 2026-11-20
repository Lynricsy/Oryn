/**
 * orb 徽记：信息面板 orb 块里的大号状态标志。与状态珠（`OrbBead`）同一套状态语言，多两层信息：
 * - 外圈 36 根刻度是近一段时间的 CPU 采样绕成的一圈，最新一根在 12 点方向；
 * - `fault`（orb 本身就绪、但有服务或环境故障）时，表圈 5–7 点钟一段换成红色，像缺了一角。
 * 就绪时刻度统一淡色；工作中最新 8 根点亮成一段亮弧、核心外圈呼吸；不运行的状态刻度是一圈等长灰色短刻度。
 */
import type { ReactNode } from "react";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import { type OrbVisual, orbVisual } from "./OrbBead";

type OrbState = Schemas["Orb"]["state"];

const TICKS = 36;
/** 坐标系 56×56：圆心、刻度内缘半径、轨道半径、核心半径。 */
const C = 28;
const R_TICK = 21.5;
const R_TRACK = 18.5;
const R_CORE = 13.5;
/** 工作中点亮的最新刻度数。 */
const LIT = 8;
/** 故障缺角覆盖的角度（顺时针、从 12 点起）：5–7 点钟。 */
const NOTCH_FROM = 145;
const NOTCH_TO = 215;

/** 采样线性插值成 `TICKS` 个值（0..100）；没有采样时为空。 */
function resample(values: readonly number[]): number[] {
	if (values.length === 0) return [];
	if (values.length === 1) return Array(TICKS).fill(values[0] ?? 0);
	return Array.from({ length: TICKS }, (_, i) => {
		const t = (i / (TICKS - 1)) * (values.length - 1);
		const a = Math.floor(t);
		const b = Math.min(a + 1, values.length - 1);
		const va = values[a] ?? 0;
		return va + ((values[b] ?? va) - va) * (t - a);
	});
}

interface Tick {
	len: number;
	color: string;
	opacity: number;
	width: number;
}

function ticksOf(
	visual: OrbVisual,
	cpu: readonly number[],
	fault: boolean,
): Tick[] {
	const live =
		visual === "ready" || visual === "working" || visual === "failed";
	const color = visual === "failed" ? "var(--bad)" : "var(--good)";
	const vals = resample(cpu);
	return Array.from({ length: TICKS }, (_, i) => {
		const angle = ((i + 1) / TICKS) * 360;
		if (fault && angle >= NOTCH_FROM && angle <= NOTCH_TO)
			return { len: 6, color: "var(--bad)", opacity: 1, width: 2 };
		if (live) {
			// 没有采样时按 0 画：一圈最短刻度
			const v = Math.min(Math.max(vals[i] ?? 0, 0), 100);
			const lit = visual === "working" && i >= TICKS - LIT;
			return {
				len: 1.75 + 4.25 * Math.sqrt(v / 100),
				color,
				opacity: visual === "working" ? (lit ? 1 : 0.25) : 0.3,
				width: 1.6,
			};
		}
		if (visual === "deleted")
			return { len: 1.4, color: "var(--text-ghost)", opacity: 0.8, width: 1.6 };
		const frost = visual === "frozen" || visual === "freezing";
		return {
			len: 3.5,
			color: frost ? "var(--frost)" : "var(--text)",
			opacity: frost ? 0.4 : 0.2,
			width: 1.6,
		};
	});
}

function Core({
	visual,
	gradientId,
}: {
	visual: OrbVisual;
	gradientId: string;
}): ReactNode {
	// 实心盘只有中心到边缘约 8% 的同色压暗，不做高光
	const disc = (color: string) => (
		<>
			<defs>
				<radialGradient id={gradientId} cx="50%" cy="42%" r="62%">
					<stop offset="0" stopColor={color} />
					<stop
						offset="1"
						stopColor={`color-mix(in oklab, ${color}, black 8%)`}
					/>
				</radialGradient>
			</defs>
			<circle cx={C} cy={C} r={R_CORE} fill={`url(#${gradientId})`} />
		</>
	);
	switch (visual) {
		case "ready":
			return disc("var(--good)");
		case "working":
			return (
				<>
					<circle
						cx={C}
						cy={C}
						r={17}
						fill="var(--good)"
						className="orb-breathe"
						style={{ transformOrigin: `${C}px ${C}px` }}
					/>
					{disc("var(--good)")}
				</>
			);
		case "booting":
			return (
				<>
					<circle
						cx={C}
						cy={C}
						r={R_CORE}
						fill="color-mix(in oklab, var(--brand) 12%, transparent)"
					/>
					<g className="orb-spin" style={{ transformOrigin: `${C}px ${C}px` }}>
						<path
							d={`M${C} ${C - R_TRACK} A${R_TRACK} ${R_TRACK} 0 0 1 ${C + R_TRACK} ${C}`}
							fill="none"
							stroke="var(--brand)"
							strokeWidth={2.5}
							strokeLinecap="round"
						/>
					</g>
				</>
			);
		case "freezing":
		case "frozen":
			return (
				<g className={visual === "freezing" ? "orb-fade" : undefined}>
					<circle
						cx={C}
						cy={C}
						r={R_CORE}
						fill="color-mix(in oklab, var(--frost) 14%, transparent)"
					/>
					<g stroke="var(--frost)" strokeLinecap="round" fill="none">
						<circle cx={C} cy={C} r={12.5} strokeWidth={2} />
						{[0, 60, 120, 180, 240, 300].map((deg) => (
							<line
								key={deg}
								x1={C}
								y1={C - 6.5}
								x2={C}
								y2={C - 10}
								strokeWidth={1.8}
								transform={`rotate(${deg} ${C} ${C})`}
							/>
						))}
					</g>
				</g>
			);
		case "stopped":
			return (
				<circle
					cx={C}
					cy={C}
					r={12.5}
					fill="none"
					stroke="var(--text-faint)"
					strokeWidth={2.25}
				/>
			);
		case "deleted":
			return (
				<circle
					cx={C}
					cy={C}
					r={12.5}
					fill="none"
					stroke="var(--text-faint)"
					strokeWidth={2}
					strokeDasharray="3.2 3.2"
				/>
			);
		default:
			return (
				<>
					{disc("var(--bad)")}
					<path
						d="M23.2 23.2 L32.8 32.8 M32.8 23.2 L23.2 32.8"
						stroke="white"
						strokeWidth={2.4}
						strokeLinecap="round"
					/>
				</>
			);
	}
}

export function OrbEmblem({
	state,
	active = false,
	cpu = [],
	fault = false,
	size = 56,
	label,
	className = "",
}: {
	state: OrbState;
	active?: boolean;
	/** 近一段时间的 CPU 百分比采样（旧 → 新） */
	cpu?: readonly number[];
	fault?: boolean;
	size?: number;
	label?: string;
	className?: string;
}): ReactNode {
	const { t } = useTranslation();
	const gradientId = useId();
	const visual = orbVisual(state, active);
	const frost = visual === "frozen" || visual === "freezing";
	return (
		<span
			role="img"
			aria-label={label ?? t(`orbState.${state}`)}
			className={`inline-flex shrink-0 ${className}`}
		>
			<svg
				viewBox="0 0 56 56"
				width={size}
				height={size}
				aria-hidden
				className="overflow-visible"
			>
				{ticksOf(visual, cpu, fault).map((tick, i) => (
					<line
						// biome-ignore lint/suspicious/noArrayIndexKey: 固定 36 根，位置即身份
						key={i}
						x1={C}
						y1={C - R_TICK}
						x2={C}
						y2={C - R_TICK - tick.len}
						stroke={tick.color}
						strokeOpacity={tick.opacity}
						strokeWidth={tick.width}
						strokeLinecap="round"
						transform={`rotate(${((i + 1) / TICKS) * 360} ${C} ${C})`}
					/>
				))}
				<circle
					cx={C}
					cy={C}
					r={R_TRACK}
					fill="none"
					stroke={frost ? "var(--frost)" : "var(--text-faint)"}
					strokeOpacity={frost ? 1 : 0.3}
					strokeWidth={frost ? 1.25 : 1}
				/>
				<Core visual={visual} gradientId={gradientId} />
			</svg>
		</span>
	);
}
