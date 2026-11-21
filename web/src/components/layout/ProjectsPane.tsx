/**
 * 「项目」分区侧栏：首行新建项目，其后每个未归档项目一行（链到项目页 `/projects/<id>`）。不设分区标题行。
 */
import { type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { NavLink, useNavigate } from "react-router";
import { useProjects } from "../../api/queries";
import { useSchedules } from "../../api/schedule-queries";
import { isCoordinatorThread } from "../../lib/thread-kind";
import { useIndex } from "../../state/stores";
import { ProjectBadge } from "../ui/data";
import {
	closeOnCompact,
	GroupIcon,
	LIST_LINK,
	LIST_ROW,
	PANE_SCROLL,
} from "./pane-parts";

export function ProjectsPane(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const threads = useIndex((s) => s.threads);
	const projects = useProjects();
	const schedules = useSchedules({ mode: "new_thread" });
	const list = (projects.data?.projects ?? []).filter((p) => !p.archivedAt);
	const counts = useMemo(() => {
		const m = new Map<string | null, number>();
		for (const th of Object.values(threads))
			if (!th.archivedAt && !isCoordinatorThread(th)) {
				const key = th.projectId ?? null;
				m.set(key, (m.get(key) ?? 0) + 1);
			}
		return m;
	}, [threads]);
	const scheduleCounts = useMemo(() => {
		const m = new Map<string, number>();
		for (const s of schedules.data?.schedules ?? [])
			if (s.projectId) m.set(s.projectId, (m.get(s.projectId) ?? 0) + 1);
		return m;
	}, [schedules.data]);
	return (
		<div className={PANE_SCROLL}>
			<div className="flex flex-col gap-0.5" data-testid="sidebar-projects">
				<button
					type="button"
					className={LIST_ROW}
					onClick={() => {
						navigate("/projects/new");
						closeOnCompact();
					}}
				>
					<span className="grid h-[22px] w-[22px] shrink-0 place-items-center">
						<GroupIcon name="plus" />
					</span>
					<span className="min-w-0 flex-1 truncate">{t("projects.new")}</span>
				</button>
				{list.map((p) => (
					<NavLink
						key={p.id}
						to={`/projects/${p.id}`}
						onClick={closeOnCompact}
						className={LIST_LINK}
					>
						<ProjectBadge name={p.name} icon={p.icon} size={22} />
						<span className="flex min-w-0 flex-1 flex-col">
							<span className="truncate">{p.name}</span>
							<span className="truncate font-normal text-2xs text-faint">
								{t("sidebar.projectMeta", {
									threads: counts.get(p.id) ?? 0,
									schedules: scheduleCounts.get(p.id) ?? 0,
								})}
							</span>
						</span>
					</NavLink>
				))}
			</div>
		</div>
	);
}
