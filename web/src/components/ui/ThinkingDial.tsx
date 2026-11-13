/**
 * 思考级别圆圈（哈维球）：外圈 1.5px 描边，内部从 12 点方向顺时针填扇形，
 * off → xhigh 依次 0%、20%、40%、60%、80%、100%；max 画成火苗。
 * 14px 下扇形每级相差 72°，相邻两级的轮廓差别比「水位」明显（水位低档面积太小，60%/80% 只差 2px 高）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "./icons";
import { Menu, type MenuItem } from "./primitives";

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

/** 未指定级别（跟随 role / 默认）的图形：虚线空圈，与「关闭」的实线空圈区分开。 */
function DefaultDial({ size }: { size: number }): ReactNode {
	const c = size / 2;
	return (
		<svg
			width={size}
			height={size}
			viewBox={`0 0 ${size} ${size}`}
			aria-hidden
			focusable="false"
			className="shrink-0"
		>
			<circle
				cx={c}
				cy={c}
				r={c - 0.75}
				fill="none"
				stroke="currentColor"
				strokeWidth={1.5}
				strokeDasharray="2 2"
				opacity={0.55}
			/>
		</svg>
	);
}

/** 菜单项前的级别图形：auto 是闪光、空串是虚线空圈，其余同输入坞里的圆圈。 */
function LevelGlyph({ level, size }: { level: string; size: number }) {
	if (level === "auto") return <Icon name="sparkle" size={size} />;
	if (level === "") return <DefaultDial size={size} />;
	return <ThinkingDial level={level} size={size} />;
}

/**
 * 思考级别菜单：触发器就是圆圈本身，点开列出可选级别，每项左侧画该级别的圆圈。
 * `options` 由调用方按模型能力给出（线程里是 omp 的 `availableThinkingLevels`），空串表示「跟随默认」；
 * 能力未知、只能给出「跟随默认」时，用 `defaultHint` 在该项右侧说明原因。
 * `shown` 是触发器上画的级别（auto 时传实际生效的级别）；缺省时画 `value`。
 */
export function ThinkingMenu({
	value,
	shown,
	options,
	defaultLabel,
	onSelect,
	size = 14,
	className = "",
	side = "top",
	align = "right",
	badge = false,
	defaultHint,
}: {
	value: string;
	shown?: string;
	options: readonly string[];
	/** 空串选项的文案 */
	defaultLabel?: string;
	onSelect: (level: string) => void;
	size?: number;
	className?: string;
	side?: "top" | "bottom";
	align?: "left" | "right";
	/** 角标形态（窄屏首页模型图标右下角）：圆形小触发器，点击区向外扩到约 28px */
	badge?: boolean;
	/** 「跟随默认」项右侧的说明 */
	defaultHint?: string;
}): ReactNode {
	const { t } = useTranslation();
	// 级别来自 omp（`ThinkingLevel` 加上 auto），文案键按同名取
	const label = (l: string): string =>
		l === ""
			? (defaultLabel ?? "")
			: t(`model.levels.${l as (typeof DIAL_LEVELS)[number] | "auto" | "max"}`);
	const items: MenuItem[] = options.map((l) => ({
		key: l || "default",
		label: label(l),
		leading: <LevelGlyph level={l} size={14} />,
		checked: l === value,
		hint: l === "" ? defaultHint : undefined,
		onSelect: () => onSelect(l),
	}));
	const drawn = shown ?? value;
	const known = drawn === "max" || DIAL_LEVELS.some((l) => l === drawn);
	const level = label(value);
	return (
		<Menu
			side={side}
			align={align}
			items={items}
			// 根元素默认是块级 div，行高会把角标撑成竖向椭圆；inline-flex 让它只有按钮那么大
			className="inline-flex"
			trigger={(p) => (
				<button
					type="button"
					aria-label={t("model.thinkingButton", { level })}
					title={t("model.levelLabel", { level })}
					// 触发器固定为正方形 + rounded-full：任何浏览器、任何行高下都是正圆
					style={badge ? { width: size + 4, height: size + 4 } : undefined}
					className={
						badge
							? `relative grid shrink-0 place-items-center rounded-full bg-panel leading-none transition-colors duration-150 after:absolute after:-inset-2 after:content-[''] hover:text-fg ${className}`
							: `grid h-7 w-7 shrink-0 place-items-center rounded-full leading-none transition-colors duration-150 hover:bg-hover hover:text-fg ${className}`
					}
					{...p}
				>
					{/* 圆圈自身的读屏名由按钮给出，这里只画 */}
					<span aria-hidden className="flex">
						{known ? (
							<ThinkingDial level={drawn} size={size} />
						) : (
							<LevelGlyph level={drawn === "auto" ? "auto" : ""} size={size} />
						)}
					</span>
				</button>
			)}
		/>
	);
}
