/**
 * 项目设置里展开的「定时任务」（M9）：该项目的「每次新建线程」定时——名称、表达式摘要、下次运行、最近一次状态、启停、
 * 编辑、删除；底部「新建定时任务」（对话框里项目锁定为本项目）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Project } from "../../api/client";
import { type Schedule, useSchedules } from "../../api/schedule-queries";
import { ScheduleDialog } from "../../components/schedule/ScheduleDialog";
import { ScheduleRow } from "../../components/schedule/ScheduleRow";
import { Button, ErrorText, Spinner } from "../../components/ui/primitives";

export function ProjectSchedules({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const schedules = useSchedules({ mode: "new_thread", project: project.id });
	const [editing, setEditing] = useState<Schedule | "new" | null>(null);
	const list = schedules.data?.schedules ?? [];
	return (
		<div
			data-testid={`project-schedules-${project.id}`}
			className="flex flex-col gap-2"
		>
			<div className="flex items-center gap-2">
				<h4 className="font-medium text-muted text-xs">
					{t("projects.schedules")}
				</h4>
				<span className="min-w-0 flex-1" />
				<Button size="sm" icon="plus" onClick={() => setEditing("new")}>
					{t("projects.newSchedule")}
				</Button>
			</div>
			{schedules.isPending && <Spinner size={12} />}
			<ErrorText error={schedules.error} />
			{!schedules.isPending && list.length === 0 && (
				<p className="text-faint text-xs">{t("projects.noSchedules")}</p>
			)}
			{list.length > 0 && (
				<ul className="-mx-2 flex flex-col">
					{list.map((s) => (
						<ScheduleRow key={s.id} schedule={s} onEdit={() => setEditing(s)} />
					))}
				</ul>
			)}
			{editing === "new" && (
				<ScheduleDialog
					target={{
						mode: "new_thread",
						projectId: project.id,
						lockProject: true,
					}}
					onClose={() => setEditing(null)}
				/>
			)}
			{editing && editing !== "new" && (
				<ScheduleDialog
					scheduleId={editing.id}
					target={{
						mode: "new_thread",
						projectId: project.id,
						lockProject: true,
					}}
					onClose={() => setEditing(null)}
				/>
			)}
		</div>
	);
}
