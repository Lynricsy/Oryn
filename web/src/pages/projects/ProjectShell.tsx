/**
 * 项目分区各页的外框（M24）：纸面、可拖动的页头（ChromeLead / 侧栏开关 / 面包屑 / 动作 / 窗口控件）与正文容器；
 * 已归档的项目在正文最前面放归档提示与「取消归档」。`ProjectGate` 按路由里的 projectId 取项目（含已归档），
 * 加载中、不存在与读取失败各有一屏。
 */
import { useQueryClient } from "@tanstack/react-query";
import { Fragment, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link, useParams } from "react-router";
import { api, type Project, unwrap } from "../../api/client";
import { qk } from "../../api/queries";
import {
	ChromeLead,
	WindowControls,
} from "../../components/layout/WindowControls";
import { useAsync } from "../../components/settings/useAsync";
import { Callout, ProjectBadge, Sheet } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import {
	Button,
	ErrorText,
	IconButton,
	Spinner,
} from "../../components/ui/primitives";
import { useBreakpoint } from "../../lib/viewport";
import {
	uiStore,
	useSidebarShown,
	useSidebarToggleShown,
} from "../../state/ui";
import { useProject } from "./useProject";

/** 面包屑的一段；`to` 缺省时是纯文字（最后一段总是页面标题 h1）。 */
export interface Crumb {
	label: string;
	to?: string;
}

const H1 = "min-w-0 truncate font-semibold text-sm";
const CRUMB_LINK =
	"min-w-0 truncate text-muted text-sm transition-colors duration-150 hover:text-fg";

function Sep(): ReactNode {
	return (
		<span aria-hidden className="shrink-0 text-faint text-sm">
			/
		</span>
	);
}

/** 页头标题区：项目 / <项目> / 各段 crumb；没有项目也没有 crumb 时只有「项目」标题。 */
function Breadcrumb({
	project,
	crumbs,
}: {
	project?: Project;
	crumbs: readonly Crumb[];
}): ReactNode {
	const { t } = useTranslation();
	if (!project && crumbs.length === 0)
		return (
			<>
				<Icon name="folder" size={16} className="shrink-0 text-faint" />
				<h1 className={H1}>{t("projects.title")}</h1>
			</>
		);
	const last = crumbs.length - 1;
	return (
		<nav
			aria-label={t("projectShell.breadcrumb")}
			className="flex min-w-0 items-center gap-1.5"
		>
			{/* 窄屏省掉根节点，把位置留给项目名与页面标题 */}
			<span className="hidden shrink-0 items-center gap-1.5 sm:inline-flex">
				<Link to="/projects" className={CRUMB_LINK}>
					{t("projects.title")}
				</Link>
				<Sep />
			</span>
			{project &&
				(crumbs.length > 0 ? (
					<Link
						to={`/projects/${project.id}`}
						className={`flex min-w-0 items-center gap-1.5 ${CRUMB_LINK}`}
					>
						<ProjectBadge name={project.name} icon={project.icon} size={18} />
						<span className="truncate">{project.name}</span>
					</Link>
				) : (
					<>
						<ProjectBadge name={project.name} icon={project.icon} size={18} />
						<h1 className={H1}>{project.name}</h1>
					</>
				))}
			{crumbs.map((c, i) => (
				<Fragment key={c.label}>
					{(project || i > 0) && <Sep />}
					{i === last ? (
						<h1 className={H1}>{c.label}</h1>
					) : c.to ? (
						<Link to={c.to} className={CRUMB_LINK}>
							{c.label}
						</Link>
					) : (
						<span className="min-w-0 truncate text-muted text-sm">
							{c.label}
						</span>
					)}
				</Fragment>
			))}
		</nav>
	);
}

/** 归档提示：说明归档的效果，带「取消归档」。 */
function ArchivedNotice({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const unarchive = useAsync();
	return (
		<div data-testid="project-archived" className="flex flex-col gap-1">
			<Callout
				tone="neutral"
				icon="archive"
				role="status"
				title={t("projectShell.archivedTitle")}
				actions={
					<Button
						size="sm"
						disabled={unarchive.busy}
						onClick={() =>
							void unarchive.run(async () => {
								await unwrap(
									api.PATCH("/api/v1/projects/{id}", {
										params: { path: { id: project.id } },
										body: { archived: false },
									}),
								);
								await qc.invalidateQueries({ queryKey: qk.projects });
							})
						}
					>
						{t("projectShell.unarchive")}
					</Button>
				}
			>
				{t(
					project.hosted
						? "projectShell.archivedBodyHosted"
						: "projectShell.archivedBody",
				)}
			</Callout>
			<ErrorText error={unarchive.error} />
		</div>
	);
}

export function ProjectShell({
	project,
	crumbs = [],
	actions,
	wide = false,
	fill = false,
	children,
}: {
	project?: Project;
	crumbs?: readonly Crumb[];
	actions?: ReactNode;
	wide?: boolean;
	/** 正文占满可视高度、不整页滚动：页面自己安排各栏的内部滚动（提交子页的左右分栏）；子元素用 flex-1 min-h-0 撑开 */
	fill?: boolean;
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
				<ChromeLead show={chromeLeft} />
				{sidebarToggle && (
					<IconButton
						icon={bp === "expanded" ? "sidebar" : "menu"}
						label={t("nav.sidebar")}
						onClick={() => uiStore.getState().toggleSidebar()}
					/>
				)}
				<Breadcrumb project={project} crumbs={crumbs} />
				<span className="min-w-0 flex-1" />
				{actions}
				<WindowControls side="right" className="-mr-1" />
			</header>
			<div
				className={`min-h-0 flex-1 p-3 pb-safe sm:p-5 ${fill ? "overflow-hidden" : "overflow-y-auto"}`}
			>
				<div
					className={`mx-auto flex flex-col gap-3 ${wide ? "max-w-5xl" : "max-w-3xl"} ${fill ? "h-full" : ""}`}
				>
					{project?.archivedAt ? <ArchivedNotice project={project} /> : null}
					{children}
				</div>
			</div>
		</Sheet>
	);
}

/** 按路由里的 projectId 取项目后渲染 children；加载中、不存在或读取失败时给出对应的一屏。 */
export function ProjectGate({
	children,
}: {
	children: (p: Project) => ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const { projectId } = useParams();
	const found = useProject(projectId);
	if (found.status === "loading")
		return (
			<ProjectShell>
				<div className="flex justify-center p-6 text-muted">
					<Spinner size={18} />
				</div>
			</ProjectShell>
		);
	if (found.status === "missing")
		return (
			<ProjectShell>
				<div
					data-testid="project-not-found"
					className="flex flex-col items-center gap-2 p-8 text-center"
				>
					<ErrorText error={found.error} />
					<p className="text-muted text-sm">{t("projectsPage.notFound")}</p>
					<Link to="/projects" className="text-accent-ink text-sm underline">
						{t("projects.title")}
					</Link>
				</div>
			</ProjectShell>
		);
	return <Fragment key={found.project.id}>{children(found.project)}</Fragment>;
}
