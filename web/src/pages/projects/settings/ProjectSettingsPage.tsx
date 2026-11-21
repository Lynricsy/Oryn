/**
 * 项目设置（M24，`/projects/<id>/settings/<分组>`）：纸面内左导航 + 右内容；窄屏是一页分组列表，点进去看该组内容，
 * 靠面包屑里的「设置」回到列表。宽屏没有分组时落到「概览」，不认识的分组也落到「概览」。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link, Navigate, NavLink, useParams } from "react-router";
import type { Project } from "../../../api/client";
import { Card } from "../../../components/ui/data";
import { Icon, type IconName } from "../../../components/ui/icons";
import { useBreakpoint } from "../../../lib/viewport";
import { ProjectGate, ProjectShell } from "../ProjectShell";
import { AgentTab } from "./AgentTab";
import { EnvironmentTab } from "./EnvironmentTab";
import { GitHubTab } from "./GitHubTab";
import { OverviewTab } from "./OverviewTab";
import { SchedulesTab } from "./SchedulesTab";
import { SecretsTab } from "./SecretsTab";
import { SshTab } from "./SshTab";

export const SETTINGS_TABS = [
	"overview",
	"environment",
	"agent",
	"github",
	"schedules",
	"ssh",
	"secrets",
] as const;
export type ProjectSettingsTab = (typeof SETTINGS_TABS)[number];

const TAB_ICON: Record<ProjectSettingsTab, IconName> = {
	overview: "info",
	environment: "wrench",
	agent: "robot",
	github: "github",
	schedules: "clock",
	ssh: "hardDrives",
	secrets: "key",
};

const TAB_BODY: Record<
	ProjectSettingsTab,
	(props: { project: Project }) => ReactNode
> = {
	overview: OverviewTab,
	environment: EnvironmentTab,
	agent: AgentTab,
	github: GitHubTab,
	schedules: SchedulesTab,
	ssh: SshTab,
	secrets: SecretsTab,
};

const isTab = (v: string | undefined): v is ProjectSettingsTab =>
	(SETTINGS_TABS as readonly string[]).includes(v ?? "");

export function ProjectSettingsPage(): ReactNode {
	return <ProjectGate>{(p) => <ProjectSettings project={p} />}</ProjectGate>;
}

function ProjectSettings({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const { tab: raw } = useParams();
	const compact = useBreakpoint() === "compact";
	const base = `/projects/${project.id}/settings`;
	const tab = isTab(raw) ? raw : undefined;
	if ((raw !== undefined && !tab) || (!tab && !compact))
		return <Navigate to={`${base}/overview`} replace />;
	const Body = tab ? TAB_BODY[tab] : undefined;
	return (
		<ProjectShell
			project={project}
			wide
			crumbs={[
				{ label: t("projectSettings.title"), to: base },
				...(tab ? [{ label: t(`projectSettings.tabs.${tab}`) }] : []),
			]}
		>
			{compact ? (
				Body ? (
					<div className="flex min-w-0 flex-col gap-3">
						<Body project={project} />
					</div>
				) : (
					<div data-testid="project-settings-list">
						<Card>
							<ul className="-mx-2">
								{SETTINGS_TABS.map((k) => (
									<li key={k}>
										<Link
											to={`${base}/${k}`}
											className="flex min-h-11 items-center gap-3 rounded-md px-2 text-sm hover:bg-hover"
										>
											<Icon
												name={TAB_ICON[k]}
												size={16}
												aria-hidden
												className="shrink-0 text-faint"
											/>
											<span className="min-w-0 flex-1 truncate">
												{t(`projectSettings.tabs.${k}`)}
											</span>
											<Icon
												name="chevronRight"
												size={14}
												aria-hidden
												className="shrink-0 text-faint"
											/>
										</Link>
									</li>
								))}
							</ul>
						</Card>
					</div>
				)
			) : (
				<div
					data-testid="project-settings"
					className="grid grid-cols-[11rem_minmax(0,1fr)] items-start gap-5"
				>
					<nav
						data-testid="project-settings-nav"
						aria-label={t("projectSettings.nav")}
						className="sticky top-0 flex flex-col gap-0.5"
					>
						{SETTINGS_TABS.map((k) => (
							<NavLink
								key={k}
								to={`${base}/${k}`}
								className={({ isActive }) =>
									`flex h-8 items-center gap-2 rounded-md px-2 text-sm transition-colors duration-150 ${isActive ? "bg-active font-medium text-fg" : "text-muted hover:bg-hover hover:text-fg"}`
								}
							>
								<Icon
									name={TAB_ICON[k]}
									size={15}
									aria-hidden
									className="shrink-0"
								/>
								<span className="min-w-0 truncate">
									{t(`projectSettings.tabs.${k}`)}
								</span>
							</NavLink>
						))}
					</nav>
					<div className="flex min-w-0 flex-col gap-3">
						{Body && <Body project={project} />}
					</div>
				</div>
			)}
		</ProjectShell>
	);
}
