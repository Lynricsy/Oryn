/**
 * 页头的定时胶囊（M9）：
 * - `ScheduleChip`：线程内定时——时钟 + 下次运行的相对时间；暂停或已结束时灰色。
 * - `ScheduleRunChip`：线程由「每次新建线程」定时建出——`⟳ <定时名> #<次序>`。
 * 两者点开都是对应的定时对话框（由调用方渲染）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import { useSchedule } from "../../api/schedule-queries";
import { formatRelative } from "../../lib/format";
import { Icon } from "../ui/icons";

const CHIP =
	"inline-flex h-[26px] shrink-0 items-center gap-1.5 rounded-full border px-2 font-medium text-xs transition-colors duration-150";

export function ScheduleChip({
	schedule,
	onOpen,
}: {
	schedule: Schemas["ThreadScheduleSummary"];
	onOpen: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const live = schedule.enabled && schedule.nextRunAt !== undefined;
	const label = live
		? t("thread.scheduleNext", {
				when: formatRelative(schedule.nextRunAt ?? Date.now()),
			})
		: t("thread.schedulePaused");
	return (
		<button
			type="button"
			data-testid="schedule-chip"
			data-enabled={live ? "" : undefined}
			aria-label={label}
			title={label}
			onClick={onOpen}
			className={`${CHIP} ${live ? "border-border bg-panel/60 text-fg hover:bg-hover" : "border-border/70 bg-transparent text-faint hover:bg-hover"}`}
		>
			<Icon name="clock" size={12} aria-hidden />
			<span aria-hidden className="hidden @lg/header:inline">
				{live
					? formatRelative(schedule.nextRunAt ?? Date.now())
					: t("schedule.paused")}
			</span>
		</button>
	);
}

export function ScheduleRunChip({
	run,
	onOpen,
}: {
	run: Schemas["ThreadScheduleRun"];
	onOpen: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const schedule = useSchedule(run.scheduleId);
	const name = schedule.data?.name || t("sidebar.scheduledRuns");
	const label = t("thread.scheduleRun", { name, run: run.run });
	return (
		<button
			type="button"
			data-testid="schedule-run-chip"
			aria-label={label}
			title={label}
			onClick={onOpen}
			className={`${CHIP} border-border bg-panel/60 text-muted hover:bg-hover hover:text-fg`}
		>
			<Icon name="repeat" size={12} aria-hidden />
			<span aria-hidden className="hidden max-w-32 truncate @md/header:inline">
				{`${name} #${run.run}`}
			</span>
		</button>
	);
}
