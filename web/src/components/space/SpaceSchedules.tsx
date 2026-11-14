/**
 * Space 页的「定时」小节：`listSchedules?space=<id>`（本 Space 的「每次新建线程」定时，加上挂在本 Space 线程上的线程内定时），
 * 每行名称、表达式、下次运行与启停；「新建」打开新建线程定时的对话框（项目预填 Space 的项目，`spaceId` 固定）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { type Schedule, useSchedules } from "../../api/schedule-queries";
import type { Space } from "../../api/space-queries";
import { ScheduleDialog } from "../schedule/ScheduleDialog";
import { editTarget, ScheduleRow } from "../schedule/ScheduleRow";
import { Button, Spinner } from "../ui/primitives";

export function SpaceSchedules({ space }: { space: Space }): ReactNode {
	const { t } = useTranslation();
	const schedules = useSchedules({ space: space.id });
	const [editing, setEditing] = useState<Schedule | "new" | null>(null);
	const list = schedules.data?.schedules ?? [];
	return (
		<section data-testid="space-schedules" className="flex flex-col gap-1.5">
			<h3 className="flex items-center gap-1.5 font-semibold text-muted text-xs">
				{t("space.schedules")}
				<span className="min-w-0 flex-1" />
				<Button size="sm" icon="plus" onClick={() => setEditing("new")}>
					{t("common.create")}
				</Button>
			</h3>
			{schedules.isPending && <Spinner size={12} />}
			{!schedules.isPending && list.length === 0 && (
				<p className="text-faint text-xs">{t("space.noSchedules")}</p>
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
						projectId: space.projectId ?? null,
						spaceId: space.id,
					}}
					onClose={() => setEditing(null)}
				/>
			)}
			{editing && editing !== "new" && (
				<ScheduleDialog
					scheduleId={editing.id}
					target={editTarget(editing)}
					onClose={() => setEditing(null)}
				/>
			)}
		</section>
	);
}
