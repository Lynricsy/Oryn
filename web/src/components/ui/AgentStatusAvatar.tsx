/**
 * 子 agent 状态头像（M13 R10）：agent 头像外加状态外圈与右下角标，取代状态点。
 *
 * - 运行中：外圈光弧旋转；
 * - 排队：虚线外圈呼吸；
 * - 完成：右下角小勾；
 * - 失败 / 中止：头像去饱和，右下角叉（失败用错误色，中止用中性灰）；
 * - 只剩保存的转录（archived）：头像保持原色，静止虚线环加存档角标；
 * - 未知：只有头像。
 *
 * 外圈与角标只用中性、成功、错误色，不用强调色（docs/design/web-guidelines.md §2）。减少动效时全局规则让动画停下，
 * 光弧补成整圈实线、呼吸停在全亮（index.css 的 `.agent-ring-*`）。各状态的含义由 `title`（tooltip 与 aria-label）说明，
 * 外圈与角标本身只作装饰。根元素外框含外圈留白，各状态尺寸一致，切换状态不挪动布局。
 */
import type { ReactNode } from "react";
import type { Schemas } from "../../api/client";
import { AvatarTile } from "./AvatarTile";
import { Icon, type IconName } from "./icons";

export type AgentVisualState =
	| "running"
	| "pending"
	| "done"
	| "failed"
	| "aborted"
	| "archived"
	| "unknown";

/** 外圈与头像之间的留白（含描边），四边各一份。 */
const RING_PAD = 3;
const RING_STROKE = 1.5;

/** 角标：底色 + 图标；未列出的状态没有角标。 */
const BADGE: Partial<
	Record<AgentVisualState, { icon: IconName; className: string }>
> = {
	done: { icon: "check", className: "bg-ok text-raised" },
	failed: { icon: "close", className: "bg-err text-raised" },
	aborted: { icon: "close", className: "bg-faint text-raised" },
	archived: {
		icon: "archive",
		className: "bg-raised text-muted ring-1 ring-border-strong ring-inset",
	},
};

/** 头像去饱和的状态。存档不在此列：只剩转录不代表出错，保持原色。 */
const MUTED: Partial<Record<AgentVisualState, true>> = {
	failed: true,
	aborted: true,
};

function Ring({
	state,
	box,
}: {
	state: AgentVisualState;
	box: number;
}): ReactNode {
	const c = box / 2;
	const r = c - RING_STROKE / 2;
	const circle = (props: {
		stroke: string;
		dash?: string;
		className?: string;
	}) => (
		<circle
			cx={c}
			cy={c}
			r={r}
			fill="none"
			stroke={props.stroke}
			strokeWidth={RING_STROKE}
			strokeLinecap="round"
			strokeDasharray={props.dash}
			className={props.className}
		/>
	);
	const svg = (children: ReactNode, className?: string) => (
		<svg
			aria-hidden
			data-ring={state}
			viewBox={`0 0 ${box} ${box}`}
			width={box}
			height={box}
			className={`pointer-events-none absolute inset-0 ${className ?? ""}`}
		>
			{children}
		</svg>
	);
	switch (state) {
		case "running": {
			// 光弧占周长约 3/10；减少动效时 `.agent-ring-arc` 取消虚线，补成整圈实线高亮
			const arc = 2 * Math.PI * r * 0.3;
			return svg(
				<>
					{circle({ stroke: "var(--line)" })}
					{circle({
						stroke: "var(--text-muted)",
						dash: `${arc} ${2 * Math.PI * r}`,
						className: "agent-ring-arc",
					})}
				</>,
				"agent-ring-spin",
			);
		}
		case "pending":
			return svg(
				circle({ stroke: "var(--text-faint)", dash: "2 3" }),
				"agent-ring-breathe",
			);
		case "archived":
			return svg(circle({ stroke: "var(--text-ghost)", dash: "2 3" }));
		default:
			return null;
	}
}

/** 只有外圈、没有头像的状态标记（与头像外圈同一套画法）：用在汇总处，例如总览标题栏的「N 个运行中」。 */
export function AgentStatusRing({
	state,
	size = 12,
}: {
	state: AgentVisualState;
	/** 外框边长（外圈贴着外框画）。 */
	size?: number;
}): ReactNode {
	return (
		<span
			aria-hidden
			className="relative inline-block shrink-0"
			style={{ width: size, height: size }}
		>
			<Ring state={state} box={size} />
		</span>
	);
}

export function AgentStatusAvatar({
	face,
	fallbackGlyph,
	state,
	size = 20,
	title,
}: {
	/** 已解析的 agent 头像（图片 / 字形）；未设置时显示 `fallbackGlyph`。 */
	face: Schemas["AvatarIdentity"] | undefined;
	/** 未设置头像时的默认字形。 */
	fallbackGlyph: string;
	state: AgentVisualState;
	/** 头像本身的边长；外框另加外圈留白。 */
	size?: number;
	/** 状态说明：tooltip 与 aria-label。 */
	title: string;
}): ReactNode {
	const box = size + RING_PAD * 2;
	const badge = BADGE[state];
	const badgeSize = Math.max(8, Math.round(size * 0.45));
	return (
		<span
			role="img"
			aria-label={title}
			title={title}
			data-state={state}
			className="relative inline-grid shrink-0 place-items-center"
			style={{ width: box, height: box }}
		>
			<Ring state={state} box={box} />
			<span
				data-agent-face
				className={`inline-flex ${MUTED[state] ? "opacity-70 grayscale" : ""}`}
			>
				<AvatarTile
					of="agent"
					identity={face}
					size={size}
					tone="accent"
					shape="circle"
					fallback={fallbackGlyph}
				/>
			</span>
			{badge && (
				<span
					aria-hidden
					data-badge={state}
					className={`absolute right-0 bottom-0 inline-grid place-items-center rounded-full ring-2 ring-raised ${badge.className}`}
					style={{ width: badgeSize, height: badgeSize }}
				>
					<Icon
						name={badge.icon}
						size={Math.round(badgeSize * 0.7)}
						weight="bold"
					/>
				</span>
			)}
		</span>
	);
}
