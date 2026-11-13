/**
 * 思考级别圆圈（哈维球）：外圈 1.5px 描边，内部从 12 点方向顺时针填扇形，
 * off → xhigh 依次 0%、20%、40%、60%、80%、100%；max 画成火苗。
 * 14px 下扇形每级相差 72°，相邻两级的轮廓差别比「水位」明显（水位低档面积太小，60%/80% 只差 2px 高）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "./icons";

/** 能画成圆圈的级别（omp `ThinkingLevel` 去掉 inherit、max），按填充比例从小到大。 */
export const DIAL_LEVELS = [
	"off",
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
] as const;

/** 从 12 点顺时针、占 `f`（0–1）的扇形路径；圆心 (c, c)、半径 r。 */
function wedge(c: number, r: number, f: number): string {
	const a = f * 2 * Math.PI;
	const x = c + r * Math.sin(a);
	const y = c - r * Math.cos(a);
	const large = f > 0.5 ? 1 : 0;
	return `M${c} ${c}V${c - r}A${r} ${r} 0 ${large} 1 ${x.toFixed(3)} ${y.toFixed(3)}Z`;
}

/**
 * 思考级别圆圈；级别未知时不渲染。圆圈颜色跟随 currentColor，`className` 只作用于圆圈；
 * max 的火苗固定暖色（text-warn）。
 */
export function ThinkingDial({
	level,
	size = 14,
	className = "",
}: {
	level?: string;
	size?: number;
	className?: string;
}): ReactNode {
	const { t } = useTranslation();
	if (level === "max") {
		const label = t("model.levelLabel", { level: t("model.levels.max") });
		return (
			<span
				role="img"
				aria-label={label}
				title={label}
				data-level={level}
				className="inline-flex shrink-0 text-warn"
			>
				<Icon name="fire" size={size} weight="fill" />
			</span>
		);
	}
	const known = DIAL_LEVELS.find((l) => l === level);
	if (!known) return null;
	const f = DIAL_LEVELS.indexOf(known) / (DIAL_LEVELS.length - 1);
	const label = t("model.levelLabel", { level: t(`model.levels.${known}`) });
	const c = size / 2;
	const ring = c - 0.75;
	// 扇形与外圈之间留约 1px 缝，填满时仍看得出是「圈里装满」而不是实心点
	const r = ring - 0.75 - 1;
	return (
		<span
			role="img"
			aria-label={label}
			title={label}
			data-level={level}
			data-fill={f}
			className={`inline-flex shrink-0 ${className}`}
		>
			<svg
				width={size}
				height={size}
				viewBox={`0 0 ${size} ${size}`}
				aria-hidden
				focusable="false"
			>
				<circle
					cx={c}
					cy={c}
					r={ring}
					fill="none"
					stroke="currentColor"
					strokeWidth={1.5}
					opacity={0.55}
				/>
				{f >= 1 && <circle cx={c} cy={c} r={r} fill="currentColor" />}
				{f > 0 && f < 1 && <path d={wedge(c, r, f)} fill="currentColor" />}
			</svg>
		</span>
	);
}
