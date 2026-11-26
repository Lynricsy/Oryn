/**
 * 项目设置里的「定时任务」（M9）：该项目的「每次新建线程」定时，D 的分组列表（`ScheduleRow`，与设置 → 自动化同一种行）——
 * 名称、表达式摘要、下次运行、最近一次状态、启停、编辑、删除；分组右上「新建定时任务」（对话框里项目锁定为本项目）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Project } from "../../api/client";
import { type Schedule, useSchedules } from "../../api/schedule-queries";
import { ScheduleDialog } from "../../components/schedule/ScheduleDialog";
import { ScheduleRow } from "../../components/schedule/ScheduleRow";
import { Button, ErrorText, Spinner } from "../../components/ui/primitives";
import { Group, GroupList } from "../../components/ui/workbench";

export function ProjectSchedules({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const schedules = useSchedules({ mode: "new_thread", project: project.id });
	const [editing, setEditing] = useState<Schedule | "new" | null>(null);
	const list = schedules.data?.schedules ?? [];
	return (
		<div data-testid={`project-schedules-${project.id}`} className="contents">
			<Group
				title={t("projects.schedules")}
				action={
					<Button size="sm" icon="plus" onClick={() => setEditing("new")}>
						{t("projects.newSchedule")}
					</Button>
				}
			>
				{schedules.isPending && <Spinner size={12} />}
				<ErrorText error={schedules.error} />
				{!schedules.isPending && list.length === 0 && (
					<p className="px-1.5 text-faint text-sm">
						{t("projects.noSchedules")}
					</p>
				)}
				{list.length > 0 && (
					<GroupList>
						{list.map((s) => (
							<ScheduleRow
								key={s.id}
								schedule={s}
								onEdit={() => setEditing(s)}
							/>
						))}
					</GroupList>
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
			</Group>
		</div>
	);
}
