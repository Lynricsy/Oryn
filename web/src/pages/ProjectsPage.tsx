/**
 * 项目分区的主区（M9.6）：`/projects` 跳到第一个未归档项目（没有项目时是空状态），`/projects/new` 新建，
 * `/projects/<id>` 是该项目的设置表单、Agent 配置（M12.2 项目级中央 omp 配置）、环境配置（最近一次 setup）、
 * 服务（只读汇总）与定时任务。
 * 页头与设置页同形：可拖动、按需放 ChromeLead / 侧栏开关 / 窗口控件；「查看线程」设侧栏过滤并切回线程分区。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, Navigate, useNavigate, useParams } from "react-router";
import {
	api,
	type Orb,
	type Project,
	type Schemas,
	unwrap,
} from "../api/client";
import { qk, useOrbs, useProjects } from "../api/queries";
import { useSwitchSection } from "../components/layout/useSection";
import {
	ChromeLead,
	WindowControls,
} from "../components/layout/WindowControls";
import { useAsync } from "../components/settings/useAsync";
import {
	Badge,
	Card,
	ProjectBadge,
	Sheet,
	Stat,
	type Tone,
} from "../components/ui/data";
import { Icon } from "../components/ui/icons";
import {
	Button,
	ErrorText,
	IconButton,
	Spinner,
} from "../components/ui/primitives";
import type { SetupRun } from "../lib/env-banner";
import { formatDuration, formatRelative } from "../lib/format";
import { parseTitleCard } from "../lib/title";
import { useBreakpoint } from "../lib/viewport";
import { useIndex } from "../state/stores";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";
import { AccelCard } from "./projects/ProjectAccel";
import { ProjectAgentConfigCard } from "./projects/ProjectAgentConfig";
import {
	EMPTY_PROJECT,
	formOf,
	ProjectEditor,
	RepoScriptText,
} from "./projects/ProjectEditor";
import { ProjectGitHubCard } from "./projects/ProjectGitHub";
import { ProjectSchedules } from "./settings/ProjectSchedules";

/** 服务卡最多汇总的 orb 数（按最近活动）。 */
const MAX_SERVICE_ORBS = 12;

const SERVICE_TONE: Record<Schemas["OrbServiceStatus"], Tone> = {
	running: "ok",
	starting: "warn",
	backoff: "warn",
	unhealthy: "err",
	failed: "err",
	stopped: "neutral",
};

/** 页面外框与页头：标题区由调用方给出，右侧是动作与窗口控件。 */
function ProjectSheet({
	title,
	actions,
	children,
}: {
	title: ReactNode;
	actions?: ReactNode;
	children: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const sidebarToggle = useSidebarToggleShown();
	// 桌面 App 无系统标题栏：页头可拖动，两端按需放窗口控件
	const chromeLeft = !useSidebarShown();
	return (
		<Sheet inset={bp === "expanded"} className="flex h-full min-h-0 flex-col">
			<header
				data-window-drag=""
				className="flex h-12 shrink-0 items-center gap-2 border-border border-b px-3"
			>
				{chromeLeft && <ChromeLead />}
				{sidebarToggle && (
					<IconButton
						icon={bp === "expanded" ? "sidebar" : "menu"}
						label={t("nav.sidebar")}
						onClick={() => uiStore.getState().toggleSidebar()}
					/>
				)}
				{title}
				<span className="min-w-0 flex-1" />
				{actions}
				<WindowControls side="right" className="-mr-1" />
			</header>
			<div className="min-h-0 flex-1 overflow-y-auto p-3 pb-safe sm:p-5">
				<div className="mx-auto flex max-w-3xl flex-col gap-3">{children}</div>
			</div>
		</Sheet>
	);
}

function PlainTitle({ children }: { children: ReactNode }): ReactNode {
	return (
		<>
			<Icon name="folder" size={16} className="shrink-0 text-faint" />
			<h1 className="min-w-0 truncate font-semibold text-sm">{children}</h1>
		</>
	);
}

/** `/projects`：有未归档项目就进第一个，否则空状态。 */
export function ProjectsIndexPage(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const projects = useProjects();
	const first = projects.data?.projects.find((p) => !p.archivedAt);
	if (first) return <Navigate to={`/projects/${first.id}`} replace />;
	return (
		<ProjectSheet title={<PlainTitle>{t("projects.title")}</PlainTitle>}>
			{projects.isPending ? (
				<div className="flex justify-center p-6 text-muted">
					<Spinner size={18} />
				</div>
			) : (
				<div
					data-testid="projects-empty"
					className="flex flex-col items-center gap-3 p-8 text-center"
				>
					<ErrorText error={projects.error} />
					<p className="max-w-sm text-muted text-sm">
						{t("projectsPage.empty")}
					</p>
					<Button
						variant="primary"
						icon="plus"
						onClick={() => navigate("/projects/new")}
					>
						{t("projects.new")}
					</Button>
				</div>
			)}
		</ProjectSheet>
	);
}

export function ProjectPage({
	create = false,
}: {
	create?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const { projectId } = useParams();
	const projects = useProjects();
	if (create)
		return (
			<ProjectSheet title={<PlainTitle>{t("projects.new")}</PlainTitle>}>
				<Card title={t("projectsPage.settings")} icon="settings">
					<ProjectEditor
						initial={EMPTY_PROJECT}
						onSaved={(p) => navigate(`/projects/${p.id}`, { replace: true })}
						onCancel={() => navigate("/projects")}
					/>
				</Card>
			</ProjectSheet>
		);
	const project = projects.data?.projects.find((p) => p.id === projectId);
	if (!project)
		return (
			<ProjectSheet title={<PlainTitle>{t("projects.title")}</PlainTitle>}>
				{projects.isPending ? (
					<div className="flex justify-center p-6 text-muted">
						<Spinner size={18} />
					</div>
				) : (
					<div
						data-testid="project-not-found"
						className="flex flex-col items-center gap-2 p-8 text-center"
					>
						<ErrorText error={projects.error} />
						<p className="text-muted text-sm">{t("projectsPage.notFound")}</p>
						<Link to="/projects" className="text-accent-ink text-sm underline">
							{t("projects.title")}
						</Link>
					</div>
				)}
			</ProjectSheet>
		);
	return <ProjectDetail key={project.id} project={project} />;
}

function ProjectDetail({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const navigate = useNavigate();
	const switchSection = useSwitchSection();
	const orbs = useOrbs().data?.orbs ?? [];
	const del = useAsync();
	// 取消编辑 = 换 key 让表单还原为服务端值
	const [rev, setRev] = useState(0);
	const mine = orbs.filter((o) => o.projectId === project.id);
	const setupOrb = latestSetupOrb(mine);
	return (
		<ProjectSheet
			title={
				<>
					<ProjectBadge name={project.name} icon={project.icon} size={18} />
					<h1 className="min-w-0 truncate font-semibold text-sm">
						{project.name}
					</h1>
				</>
			}
			actions={
				<>
					<Button
						size="sm"
						icon="chats"
						// 窄屏只留图标：名字由 aria-label 给出
						aria-label={t("projectsPage.viewThreads")}
						title={t("projectsPage.viewThreads")}
						onClick={() => {
							uiStore.getState().setProjectFilter(project.id);
							switchSection("threads");
						}}
					>
						<span className="hidden sm:inline">
							{t("projectsPage.viewThreads")}
						</span>
					</Button>
					<IconButton
						icon="trash"
						label={t("common.delete")}
						disabled={del.busy}
						onClick={() => {
							if (
								!window.confirm(
									t("projects.deleteConfirm", { name: project.name }),
								)
							)
								return;
							void del
								.run(async () => {
									await unwrap(
										api.DELETE("/api/v1/projects/{id}", {
											params: { path: { id: project.id } },
										}),
									);
									await qc.invalidateQueries({ queryKey: qk.projects });
								})
								.then((ok) => {
									if (ok) navigate("/projects", { replace: true });
								});
						}}
					/>
				</>
			}
		>
			<ErrorText error={del.error} />
			<Card title={t("projectsPage.settings")} icon="settings">
				<ProjectEditor
					key={`${project.updatedAt}:${rev}`}
					initial={formOf(project)}
					id={project.id}
					lastSetup={setupOrb?.lastSetup}
					onSaved={() => undefined}
					onCancel={() => setRev((r) => r + 1)}
				/>
			</Card>
			<ProjectAgentConfigCard project={project} />
			<ProjectGitHubCard project={project} />
			<EnvironmentCard orb={setupOrb} />
			<AccelCard project={project} />
			<ServicesCard orbs={mine} />
			{/* 定时列表自带「定时任务」小标题与新建按钮，卡片不再重复标题 */}
			<Card>
				<ProjectSchedules project={project} />
			</Card>
		</ProjectSheet>
	);
}

/** 该项目最近一次 setup 所在的 orb。 */
function latestSetupOrb(
	orbs: readonly Orb[],
): (Orb & { lastSetup: SetupRun }) | undefined {
	let best: (Orb & { lastSetup: SetupRun }) | undefined;
	for (const o of orbs) {
		const run = o.lastSetup;
		if (run && (!best || run.finishedAt > best.lastSetup.finishedAt))
			best = { ...o, lastSetup: run };
	}
	return best;
}

function EnvironmentCard({
	orb,
}: {
	orb: (Orb & { lastSetup: SetupRun }) | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const run = orb?.lastSetup;
	const thread = orb?.threadIds[0];
	return (
		<Card title={t("projectsPage.environment")} icon="wrench">
			{!run ? (
				<p data-testid="project-env-empty" className="text-faint text-sm">
					{t("projectsPage.noSetupRun")}
				</p>
			) : (
				<div
					data-testid="project-env"
					className="flex flex-col gap-2 text-muted text-xs"
				>
					<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
						{run.ok ? (
							<Badge tone={run.setupRan ? "ok" : "neutral"} icon="checkCircle">
								{t(run.setupRan ? "env.ok" : "env.skipped")}
							</Badge>
						) : (
							<Badge tone="err" icon="xCircle">
								{t("env.failed")}
							</Badge>
						)}
						<Stat icon="timer" label={t("stat.duration")}>
							{formatDuration(run.durationMs)}
						</Stat>
						<span className="inline-flex items-center gap-1">
							<Icon
								name={run.trigger === "manual" ? "user" : "bolt"}
								size={12}
								aria-hidden
							/>
							{t(`env.trigger.${run.trigger}`)}
						</span>
						<span className="inline-flex items-center gap-1 tabular-nums">
							<Icon name="clock" size={12} aria-hidden />
							{formatRelative(run.finishedAt)}
						</span>
					</div>
					{run.error && <p className="break-words text-err">{run.error}</p>}
					<div className="flex flex-wrap items-center justify-between gap-2">
						<span>
							<RepoScriptText run={run} />
						</span>
						{thread && (
							<Link
								to={`/t/${thread}`}
								className="inline-flex items-center gap-1 text-accent-ink underline"
							>
								{t("projectsPage.openThread")}
							</Link>
						)}
					</div>
				</div>
			)}
		</Card>
	);
}

/**
 * 服务汇总（只读）：本项目、未删除、非协调的 orb，按最近活动取前 12 台，列出各自上报的受管服务。
 * 服务列表就是 `GET orbs` 里各 orb 的 `services` 镜像（随 WS `orb` 消息更新），不另行请求、不唤醒 orb。
 */
function ServicesCard({ orbs }: { orbs: readonly Orb[] }): ReactNode {
	const { t } = useTranslation();
	const threads = useIndex((s) => s.threads);
	const rows = orbs
		.filter((o) => !o.deletedAt && !o.coordinator)
		.sort((a, b) => activityOf(b) - activityOf(a))
		.slice(0, MAX_SERVICE_ORBS)
		.flatMap((o) =>
			(o.services ?? []).map((s) => ({
				key: `${o.id}/${s.name}`,
				service: s,
				threadId: s.threadId ?? o.threadIds[0],
			})),
		);
	return (
		<Card title={t("projectsPage.services")} icon="globe">
			{rows.length === 0 ? (
				<p data-testid="project-services-empty" className="text-faint text-sm">
					{t("projectsPage.noServices")}
				</p>
			) : (
				<ul className="-mx-2 flex flex-col">
					{rows.map(({ key, service, threadId }) => {
						const thread = threadId ? threads[threadId] : undefined;
						return (
							<li
								key={key}
								data-testid="project-service"
								className="flex min-h-9 items-center gap-2 rounded-md px-2 py-1 text-sm"
							>
								<span className="min-w-0 truncate font-medium font-mono text-xs">
									{service.name}
								</span>
								<Badge tone={SERVICE_TONE[service.status]}>
									{t(`services.status.${service.status}`)}
								</Badge>
								<span className="min-w-0 flex-1" />
								{threadId && (
									<Link
										to={`/t/${threadId}`}
										className="min-w-0 max-w-[50%] truncate text-muted text-xs hover:text-fg hover:underline"
									>
										{(thread && parseTitleCard(thread.title).text) || threadId}
									</Link>
								)}
							</li>
						);
					})}
				</ul>
			)}
		</Card>
	);
}

/** 最近活动：用户交互、agent 活动、状态变化里最晚的一个。 */
const activityOf = (o: Orb) =>
	Math.max(
		o.lastUserInteractionAt ?? 0,
		o.lastAgentActivityAt ?? 0,
		o.stateChangedAt,
	);
