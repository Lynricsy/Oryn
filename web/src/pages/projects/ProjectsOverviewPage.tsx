/**
 * 项目总览（M24，`/projects`）：一行一个项目（徽标、名称、描述与仓库、近 14 天每天回合数的曲线、线程数与最后活跃），
 * 按最近活动排序。页头 `⋯` 菜单里「显示已归档项目」（状态在 URL 的 `?archived=1`，刷新与切分区回来都保持）。
 */
import { type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useSearchParams } from "react-router";
import type { Project } from "../../api/client";
import { useArchivedProjects, useProjects } from "../../api/queries";
import { type UsageStats, useUsageStats } from "../../api/stats-queries";
import { useNow } from "../../components/layout/pane-parts";
import { daysWindow } from "../../components/stats/statsParams";
import { Badge, Card, ProjectBadge } from "../../components/ui/data";
import {
	Button,
	EmptyState,
	ErrorText,
	IconButton,
	Menu,
	Sparkline,
	Spinner,
} from "../../components/ui/primitives";
import { formatRelative } from "../../lib/format";
import {
	byActivity,
	lastActive,
	type ProjectActivity,
	projectActivity,
} from "../../lib/project-activity";
import { useIndex } from "../../state/stores";
import { ProjectShell } from "./ProjectShell";
import { repoLabel } from "./repo-label";

/** 活动曲线覆盖的天数。 */
const CURVE_DAYS = 14;

/** 用量接口把超出 limit 的分组折叠成这一项。 */
const OTHER = "__other__";

/** 项目每天的回合数；拿不到可信数据（请求未完成或出错、该项目被折叠进 `__other__`）时为 undefined。 */
function curveOf(
	usage: UsageStats | undefined,
	projectId: string,
): number[] | undefined {
	if (!usage?.series) return undefined;
	const listed = usage.groups.some((g) => g.key === projectId);
	if (!listed && usage.groups.some((g) => g.key === OTHER)) return undefined;
	return usage.series.map(
		(pt) => pt.groups.find((g) => g.key === projectId)?.turns ?? 0,
	);
}

function ProjectRow({
	project: p,
	activity,
	curve,
	now,
	archived = false,
}: {
	project: Project;
	activity: ProjectActivity | undefined;
	curve: number[] | undefined;
	now: number;
	archived?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const sub =
		[p.description, p.repoUrl && repoLabel(p.repoUrl)]
			.filter(Boolean)
			.join(" · ") || t("projects.noRepo");
	return (
		<li>
			<Link
				to={`/projects/${p.id}`}
				data-testid="project-row"
				data-project={p.id}
				className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 rounded-md px-2 py-2.5 transition-colors duration-150 hover:bg-hover sm:grid-cols-[auto_minmax(0,1fr)_6rem_auto]"
			>
				<ProjectBadge name={p.name} icon={p.icon} size={28} />
				<span className="flex min-w-0 flex-col">
					<span className="flex min-w-0 items-center gap-2">
						<span className="truncate font-medium text-sm">{p.name}</span>
						{archived && <Badge icon="archive">{t("sidebar.archived")}</Badge>}
					</span>
					<span className="truncate text-faint text-xs">{sub}</span>
				</span>
				{/* 曲线拿不到可信数据时留空位，右列不跟着左移 */}
				<span className="hidden sm:block">
					{curve && (
						<Sparkline
							className="text-muted"
							values={curve}
							label={t("projectsOverview.activity", { name: p.name })}
						/>
					)}
				</span>
				<span className="flex flex-col items-end text-right text-faint text-xs tabular-nums">
					<span className="text-muted">
						{t("sidebar.projectThreads", { count: activity?.threads ?? 0 })}
					</span>
					<span>
						{t("projectsOverview.activeAgo", {
							ago: formatRelative(lastActive(p, activity), now),
						})}
					</span>
				</span>
			</Link>
		</li>
	);
}

export function ProjectsOverviewPage(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const now = useNow();
	const [sp, setSp] = useSearchParams();
	const showArchived = sp.get("archived") === "1";
	const toggleArchived = () =>
		setSp(
			(prev) => {
				const next = new URLSearchParams(prev);
				if (showArchived) next.delete("archived");
				else next.set("archived", "1");
				return next;
			},
			{ replace: true },
		);
	const projects = useProjects();
	const archived = useArchivedProjects(showArchived);
	const threads = useIndex((s) => s.threads);
	const activity = useMemo(
		() => projectActivity(Object.values(threads)),
		[threads],
	);
	// 按天取整：同一天内查询 key 不变
	const w = daysWindow(CURVE_DAYS, now);
	const usage = useUsageStats({
		from: w.from,
		to: w.to,
		tz: w.tz,
		groupBy: "project",
		series: "day",
		limit: 500,
	});
	const active = (projects.data?.projects ?? [])
		.filter((p) => !p.archivedAt)
		.sort(byActivity(activity));
	const archivedList = (archived.data?.projects ?? []).toSorted(
		byActivity(activity),
	);
	return (
		<ProjectShell
			wide
			actions={
				<>
					<Menu
						trigger={(p) => (
							<IconButton
								icon="more"
								label={t("projectsOverview.menu")}
								{...p}
							/>
						)}
						items={[
							{
								key: "archived",
								label: t("projectsOverview.showArchived"),
								checked: showArchived,
								onSelect: toggleArchived,
							},
						]}
					/>
					<Button
						variant="primary"
						size="sm"
						icon="plus"
						aria-label={t("projects.new")}
						onClick={() => navigate("/projects/new")}
					>
						<span className="hidden sm:inline">{t("projects.new")}</span>
					</Button>
				</>
			}
		>
			<div data-testid="projects-overview" className="flex flex-col gap-3">
				<ErrorText error={projects.error} />
				{projects.isPending ? (
					<div className="flex justify-center p-6 text-muted">
						<Spinner size={18} />
					</div>
				) : active.length === 0 ? (
					<div data-testid="projects-empty">
						<EmptyState icon="folder" title={t("projectsPage.empty")}>
							<Button
								variant="primary"
								icon="plus"
								className="mt-2"
								onClick={() => navigate("/projects/new")}
							>
								{t("projects.new")}
							</Button>
						</EmptyState>
					</div>
				) : (
					<Card>
						<ul className="-mx-2 flex flex-col">
							{active.map((p) => (
								<ProjectRow
									key={p.id}
									project={p}
									activity={activity.get(p.id)}
									curve={curveOf(usage.data, p.id)}
									now={now}
								/>
							))}
						</ul>
					</Card>
				)}
				{showArchived && (
					<Card title={t("sidebar.archived")} icon="archive">
						{archived.isPending ? (
							<div className="flex justify-center p-3 text-muted">
								<Spinner size={16} />
							</div>
						) : archivedList.length === 0 ? (
							<>
								<ErrorText error={archived.error} />
								<p className="py-2 text-muted text-sm">
									{t("projectsOverview.noArchived")}
								</p>
							</>
						) : (
							<ul className="-mx-2 flex flex-col">
								{archivedList.map((p) => (
									<ProjectRow
										key={p.id}
										project={p}
										activity={activity.get(p.id)}
										curve={curveOf(usage.data, p.id)}
										now={now}
										archived
									/>
								))}
							</ul>
						)}
					</Card>
				)}
			</div>
		</ProjectShell>
	);
}
