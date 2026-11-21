/**
 * 「项目」分区侧栏（M24）：首行「总览」（`/projects`，行尾 `+` 新建项目），其后每个未归档项目一行
 * （名称 + 线程数 · 多久前活跃，链到项目主页 `/projects/<id>`，子页里同样选中）。不设分区标题行。
 * 行顺序沿用服务端的名称序：活动变化时行不跳。选中只换底色、不加粗（中文「总览」加粗明显而拉丁项目名几乎看不出，
 * 两种行会显得不一致）；`+` 叠在「总览」行的右端，选中底色与项目行等宽。
 */
import { type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { NavLink, useNavigate } from "react-router";
import { useProjects } from "../../api/queries";
import { formatRelative } from "../../lib/format";
import { lastActive, projectActivity } from "../../lib/project-activity";
import { useIndex } from "../../state/stores";
import { ProjectBadge } from "../ui/data";
import { IconButton } from "../ui/primitives";
import {
	closeOnCompact,
	GroupIcon,
	LIST_ROW,
	PANE_SCROLL,
	useNow,
} from "./pane-parts";

/** 本分区的行：选中只换底色。 */
const ROW = ({ isActive }: { isActive: boolean }) =>
	`${LIST_ROW} ${isActive ? "bg-[var(--sb-sel)]" : ""}`;

export function ProjectsPane(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const now = useNow();
	const threads = useIndex((s) => s.threads);
	const projects = useProjects();
	const list = (projects.data?.projects ?? []).filter((p) => !p.archivedAt);
	const activity = useMemo(
		() => projectActivity(Object.values(threads)),
		[threads],
	);
	return (
		<div className={PANE_SCROLL}>
			<div className="flex flex-col gap-0.5" data-testid="sidebar-projects">
				<div className="relative">
					<NavLink
						to="/projects"
						end
						onClick={closeOnCompact}
						className={(s) => `${ROW(s)} pr-10`}
					>
						<span className="grid h-[22px] w-[22px] shrink-0 place-items-center">
							<GroupIcon name="folder" />
						</span>
						<span className="min-w-0 flex-1 truncate">
							{t("projectsPane.overview")}
						</span>
					</NavLink>
					<IconButton
						icon="plus"
						size={15}
						label={t("projects.new")}
						className="absolute top-1/2 right-1 -translate-y-1/2"
						onClick={() => {
							navigate("/projects/new");
							closeOnCompact();
						}}
					/>
				</div>
				{list.map((p) => {
					const a = activity.get(p.id);
					return (
						<NavLink
							key={p.id}
							to={`/projects/${p.id}`}
							onClick={closeOnCompact}
							className={ROW}
						>
							<ProjectBadge name={p.name} icon={p.icon} size={22} />
							<span className="flex min-w-0 flex-1 flex-col">
								<span className="truncate">{p.name}</span>
								<span className="truncate font-normal text-2xs text-faint">
									{t("projectsPane.meta", {
										count: a?.threads ?? 0,
										ago: formatRelative(lastActive(p, a), now),
									})}
								</span>
							</span>
						</NavLink>
					);
				})}
			</div>
		</div>
	);
}
