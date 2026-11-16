/**
 * 收件箱 `/inbox`：跨线程的「需要你」总览，数据全部取实时线程索引（随 index.patch 变化，不另拉接口）。
 * 分类规则见 `lib/inbox.ts`：等你处理 → 已完成未读 → 出错 → 运行中，节内按最后活动新的在前；空闲且已读的不列。
 * 页头的分段按钮切换「按状态 / 按项目」分组，选择存 localStorage。每行 = 状态图标 + 标题卡图标 + 标题 + 未读点 + 相对时间，
 * 第二行是项目（按项目分组时换成收件箱分节名）与派生来源，工作中的线程第三行是当前意图（`activity`）。
 * 索引里没有待办数据，所以行上不显示待办进度。页头与项目页、设置页同形：可拖动、按需放 ChromeLead / 侧栏开关 / 窗口控件。
 */
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Project, Thread, ThreadState } from "../api/client";
import { useProjects } from "../api/queries";
import { CountPill, useNow } from "../components/layout/pane-parts";
import {
	ChromeLead,
	WindowControls,
} from "../components/layout/WindowControls";
import { ProjectBadge, Sheet, TitleGlyph } from "../components/ui/data";
import { Icon } from "../components/ui/icons";
import {
	EmptyState,
	IconButton,
	ThreadStateIcon,
	tabClass,
} from "../components/ui/primitives";
import { formatRelative } from "../lib/format";
import {
	activityOf,
	groupByProject,
	groupByState,
	type InboxGroupBy,
	type InboxItem,
	type InboxState,
	inboxItems,
	loadInboxGroupBy,
	saveInboxGroupBy,
} from "../lib/inbox";
import { isCoordinatorThread } from "../lib/thread-kind";
import { parseTitleCard } from "../lib/title";
import { useBreakpoint } from "../lib/viewport";
import { useIndex } from "../state/stores";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";

const GROUP_OPTIONS: readonly InboxGroupBy[] = ["state", "project"];

/** 分节标题旁的状态图标：已完成未读用空闲的勾。 */
const SECTION_GLYPH: Record<InboxState, ThreadState> = {
	you: "awaiting_input",
	ready: "idle",
	stopped: "error",
	running: "working",
};

export function InboxPage(): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const sidebarToggle = useSidebarToggleShown();
	const chromeLeft = !useSidebarShown();
	const threads = useIndex((s) => s.threads);
	const projects = useProjects();
	const now = useNow();
	const [groupBy, setGroupBy] = useState<InboxGroupBy>(loadInboxGroupBy);
	const items = useMemo(
		() => inboxItems(Object.values(threads), now),
		[threads, now],
	);
	const projectOf = (id: string | null | undefined): Project | undefined =>
		id ? projects.data?.projects.find((p) => p.id === id) : undefined;
	const pick = (v: InboxGroupBy) => {
		setGroupBy(v);
		saveInboxGroupBy(v);
	};
	const row = (item: InboxItem) => (
		<InboxRow
			key={item.thread.id}
			item={item}
			project={projectOf(item.thread.projectId)}
			parent={
				item.thread.parentThreadId
					? threads[item.thread.parentThreadId]
					: undefined
			}
			showState={groupBy === "project"}
			now={now}
		/>
	);
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
				<Icon name="tray" size={16} className="shrink-0 text-faint" />
				<h1 className="min-w-0 truncate font-semibold text-sm">
					{t("inbox.title")}
				</h1>
				<span className="min-w-0 flex-1" />
				{/* biome-ignore lint/a11y/useSemanticElements: 分段按钮（aria-pressed），不是表单字段集 */}
				<div
					role="group"
					aria-label={t("inbox.groupBy")}
					className="flex shrink-0 items-center gap-0.5 rounded-md bg-inset p-0.5"
				>
					{GROUP_OPTIONS.map((o) => (
						<button
							key={o}
							type="button"
							data-group-by={o}
							aria-pressed={groupBy === o}
							onClick={() => pick(o)}
							className={`${tabClass(groupBy === o)} h-6 ${groupBy === o ? "bg-panel shadow-xs" : ""}`}
						>
							{t(o === "state" ? "inbox.byState" : "inbox.byProject")}
						</button>
					))}
				</div>
				<WindowControls side="right" className="-mr-1" />
			</header>
			<div className="min-h-0 flex-1 overflow-y-auto p-3 pb-safe sm:p-5">
				<div className="mx-auto flex max-w-3xl flex-col gap-5">
					{items.length === 0 ? (
						<div data-testid="inbox-empty">
							<EmptyState icon="tray" title={t("inbox.empty")}>
								{t("inbox.emptyHint")}
							</EmptyState>
						</div>
					) : groupBy === "state" ? (
						groupByState(items).map((g) => (
							<InboxSection
								key={g.state}
								id={g.state}
								head={
									<>
										<ThreadStateIcon state={SECTION_GLYPH[g.state]} size={14} />
										{t(`inbox.sections.${g.state}`)}
									</>
								}
								count={g.items.length}
							>
								{g.items.map(row)}
							</InboxSection>
						))
					) : (
						groupByProject(items).map((g) => {
							const p = projectOf(g.projectId);
							const name = g.projectId
								? (p?.name ?? g.projectId)
								: t("sidebar.noProject");
							return (
								<InboxSection
									key={g.projectId ?? "_none"}
									id={g.projectId ?? "_none"}
									head={
										<>
											{g.projectId ? (
												<ProjectBadge name={name} icon={p?.icon} size={16} />
											) : (
												<Icon
													name="folder"
													size={14}
													aria-hidden
													className="shrink-0 text-faint"
												/>
											)}
											<span className="min-w-0 truncate">{name}</span>
										</>
									}
									count={g.items.length}
								>
									{g.items.map(row)}
								</InboxSection>
							);
						})
					)}
				</div>
			</div>
		</Sheet>
	);
}

function InboxSection({
	id,
	head,
	count,
	children,
}: {
	id: string;
	head: ReactNode;
	count: number;
	children: ReactNode;
}): ReactNode {
	return (
		<section data-inbox-section={id} className="flex flex-col gap-1">
			<h2 className="flex h-7 items-center gap-2 px-2 font-semibold text-muted text-xs">
				{head}
				<CountPill n={count} />
			</h2>
			<ul className="flex flex-col">{children}</ul>
		</section>
	);
}

function InboxRow({
	item,
	project,
	parent,
	showState,
	now,
}: {
	item: InboxItem;
	project: Project | undefined;
	/** 父线程（fork / 协调者派生）；协调对话不显示来源 */
	parent: Thread | undefined;
	/** 按项目分组时第二行写收件箱分节名（项目已是组标题） */
	showState: boolean;
	now: number;
}): ReactNode {
	const { t } = useTranslation();
	const { thread } = item;
	const card = parseTitleCard(thread.title);
	const title = card.text || t("thread.untitled");
	const activity = thread.state === "working" ? thread.activity : undefined;
	const from =
		parent && !isCoordinatorThread(parent)
			? parseTitleCard(parent.title).text || t("thread.untitled")
			: undefined;
	return (
		<li>
			<Link
				to={`/t/${thread.id}`}
				data-inbox-row={thread.id}
				data-inbox-state={item.state}
				className="flex items-start gap-2.5 rounded-lg px-2 py-2 transition-colors duration-150 hover:bg-hover"
			>
				<span className="mt-[3px] inline-flex">
					<ThreadStateIcon state={thread.state} size={14} />
				</span>
				<span className="flex min-w-0 flex-1 flex-col gap-0.5">
					<span className="flex min-w-0 items-center gap-2">
						{card.icon && <TitleGlyph icon={card.icon} seed={title} />}
						<span
							className={`min-w-0 flex-1 truncate text-sm ${thread.unread ? "font-semibold text-fg" : "font-medium text-fg"}`}
						>
							{title}
						</span>
						<span className="shrink-0 text-2xs text-faint tabular-nums">
							{formatRelative(activityOf(thread), now)}
						</span>
						{thread.unread && (
							<span
								role="img"
								className="h-[7px] w-[7px] shrink-0 rounded-full bg-accent ring-[3px] ring-accent/20"
								aria-label={t("sidebar.unread")}
							/>
						)}
					</span>
					<span className="flex min-w-0 items-center gap-1.5 text-faint text-xs">
						{showState ? (
							<span className="shrink-0">
								{t(`inbox.sections.${item.state}`)}
							</span>
						) : project ? (
							<>
								<ProjectBadge
									name={project.name}
									icon={project.icon}
									size={14}
								/>
								<span className="min-w-0 max-w-[50%] shrink-0 truncate">
									{project.name}
								</span>
							</>
						) : (
							<span className="shrink-0">{t("sidebar.noProject")}</span>
						)}
						{from && (
							<>
								<span aria-hidden>·</span>
								<Icon name="fork" size={11} aria-hidden className="shrink-0" />
								<span className="min-w-0 truncate">
									{t("inbox.forkOf", { title: from })}
								</span>
							</>
						)}
					</span>
					{activity && (
						<span
							data-inbox-activity=""
							className="min-w-0 truncate text-faint text-xs"
						>
							{activity}
						</span>
					)}
				</span>
			</Link>
		</li>
	);
}
