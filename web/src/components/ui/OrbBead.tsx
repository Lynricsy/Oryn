/** orb 状态珠：启动中旋转弧、就绪实心（工作时呼吸）、冻结霜环、停机空心、删除虚线、失败红叉。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";

type OrbState = Schemas["Orb"]["state"];

const BOOTING: Partial<Record<OrbState, true>> = {
	requested: true,
	spawning: true,
	connecting: true,
	setting_up: true,
	resuming: true,
	waking: true,
};

function Glyph({
	state,
	active,
}: {
	state: OrbState;
	active: boolean;
}): ReactNode {
	if (BOOTING[state])
		return (
			<g className="orb-spin">
				<circle
					cx={8}
					cy={8}
					r={6}
					fill="none"
					stroke="var(--line-strong)"
					strokeWidth={1.75}
				/>
				<path
					d="M8 2 A6 6 0 0 1 14 8"
					fill="none"
					stroke="var(--brand)"
					strokeWidth={1.75}
					strokeLinecap="round"
				/>
			</g>
		);
	switch (state) {
		case "ready":
			return (
				<>
					<circle
						cx={8}
						cy={8}
						r={7.36}
						fill="var(--good)"
						opacity={0.3}
						className={active ? "orb-breathe" : undefined}
					/>
					<circle cx={8} cy={8} r={5.12} fill="var(--good)" />
				</>
			);
		case "freezing":
		case "frozen":
			return (
				<g
					stroke="var(--frost)"
					strokeLinecap="round"
					className={state === "freezing" ? "orb-fade" : undefined}
				>
					<circle cx={8} cy={8} r={6} fill="none" strokeWidth={1.5} />
					{[0, 60, 120, 180, 240, 300].map((deg) => (
						<line
							key={deg}
							x1={8}
							y1={4.6}
							x2={8}
							y2={6.4}
							strokeWidth={1.2}
							transform={`rotate(${deg} 8 8)`}
						/>
					))}
				</g>
			);
		case "stopping":
		case "stopped":
			return (
				<circle
					cx={8}
					cy={8}
					r={6}
					fill="none"
					stroke="var(--text-ghost)"
					strokeWidth={1.75}
				/>
			);
		case "deleting":
		case "deleted":
			return (
				<circle
					cx={8}
					cy={8}
					r={6}
					fill="none"
					stroke="var(--text-ghost)"
					strokeWidth={1.5}
					strokeDasharray="2 2"
				/>
			);
		default:
			return (
				<>
					<circle cx={8} cy={8} r={7} fill="var(--bad)" />
					<path
						d="M5.6 5.6 L10.4 10.4 M10.4 5.6 L5.6 10.4"
						stroke="white"
						strokeWidth={1.6}
						strokeLinecap="round"
					/>
				</>
			);
	}
}

export function OrbBead({
	state,
	active = false,
	size = 14,
	label,
}: {
	state: OrbState;
	active?: boolean;
	size?: number;
	label?: string;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<span
			role="img"
			aria-label={label ?? t(`orbState.${state}`)}
			className="inline-flex shrink-0"
		>
			<svg
				viewBox="0 0 16 16"
				width={size}
				height={size}
				aria-hidden
				className="overflow-visible"
			>
				<Glyph state={state} active={active} />
			</svg>
		</span>
	);
}
