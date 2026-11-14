/**
 * orb 状态时间线：一行色带（各段按时长占宽）+ 下方图例。颜色只表语义：运行 `--good`、setup `--caution`、
 * 冻结 `--frost`、失败 `--bad`、停机 `--line-strong`（中性，落在内嵌井底色上仍看得见）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { OrbState } from "../../api/client";
import { formatDateTime, formatDuration } from "../../lib/format";
import { Legend } from "./Legend";

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
	const span = to > from ? to - from : 1;
	const present = new Set(segments.map((s) => timelineGroup(s.state)));
	return (
		<div className="flex flex-col gap-1.5">
			<div
				role="img"
				aria-label={ariaLabel}
				data-testid="timeline-strip"
				className="relative h-3 w-full overflow-hidden rounded-xs bg-inset"
			>
				{segments.map((s) => {
					const a = Math.max(from, s.from);
					const b = Math.min(to, s.to);
					if (b <= a) return null;
					const g = timelineGroup(s.state);
					return (
						<span
							key={`${s.from}-${s.state}`}
							data-state={s.state}
							title={`${t(`stats.orbs.state.${g}`)} · ${formatDateTime(s.from)} · ${formatDuration(s.to - s.from)}`}
							className="absolute inset-y-0"
							style={{
								left: `${((a - from) / span) * 100}%`,
								width: `${((b - a) / span) * 100}%`,
								background: `var(${TIMELINE_COLOR[g]})`,
							}}
						/>
					);
				})}
			</div>
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
