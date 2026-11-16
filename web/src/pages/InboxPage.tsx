/**
 * 收件箱 `/inbox`：跨线程的「需要你」分诊台，数据取实时线程索引与通知 store（随 index.patch / WS 通知变化，不另拉接口）。
 * 分类规则见 `lib/inbox.ts`：等你处理 → 出错 → 已完成未读 → 运行中（按紧急程度），节内按最后活动新的在前；空闲且已读的不列。
 * 页面自上而下：摘要（待处理数 + 状态筛选胶囊，筛选只在本页生效）→ 分节。页头的分段按钮切换「按状态 / 按项目」分组，选择存 localStorage。
 * 每行写明它在等你什么：等你处理 = 最近一条未作答的 `input` 通知正文（问题原文）；已完成未读 = 最近的 `done` 通知正文；
 * 出错 = `thread.error.reason`，没有时取最近的 `error` 通知；运行中 = 当前意图（`activity`）。通知 store 只有最近 50 条，取不到就不显示预览。
 * 按状态分组时「等你处理」是独立卡片（主角），其余各节是一块面板列表；按项目分组时每个项目一块面板。
 * 已完成未读与出错的行悬停时可标为已读 / 归档；无悬停的设备不显示（线程页照样能做）。
 * 页头与项目页、设置页同形：可拖动、按需放 ChromeLead / 侧栏开关 / 窗口控件。
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
	describeError,
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
	INBOX_ORDER,
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
import { patchThread } from "../state/actions";
import { type AppNotification, useNotifications } from "../state/notifications";
import { useIndex } from "../state/stores";
import { showToast } from "../state/toasts";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";

const GROUP_OPTIONS: readonly InboxGroupBy[] = ["state", "project"];

/** 分节标题与筛选胶囊的状态图标：已完成未读用空闲的勾。 */
const SECTION_GLYPH: Record<InboxState, ThreadState> = {
	you: "awaiting_input",
	ready: "idle",
	stopped: "error",
	running: "working",
};

/** 面板列表：圆角描边、行间分隔线。 */
const PANEL =
	"flex flex-col divide-y divide-border overflow-hidden rounded-lg border border-border bg-panel";

/** 每个线程最近的一条 input（未作答）/ done / error 通知正文，作行内预览。 */
type Previews = Map<string, Partial<Record<AppNotification["kind"], string>>>;

function previewsOf(items: readonly AppNotification[]): Previews {
	const out: Previews = new Map();
	// 通知按时间新的在前：同一线程同一类只取第一条
	for (const n of items) {
		if (!n.threadId || !n.body) continue;
		if (n.kind === "input" && n.resolvedAt) continue;
		const slot = out.get(n.threadId) ?? {};
		if (slot[n.kind] === undefined) slot[n.kind] = n.body;
		out.set(n.threadId, slot);
	}
	return out;
}

export function InboxPage(): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const sidebarToggle = useSidebarToggleShown();
	const chromeLeft = !useSidebarShown();
	const threads = useIndex((s) => s.threads);
	const notifications = useNotifications((s) => s.items);
	const projects = useProjects();
	const now = useNow();
	const [groupBy, setGroupBy] = useState<InboxGroupBy>(loadInboxGroupBy);
	const [filter, setFilter] = useState<InboxState | null>(null);
	// 归档走与线程页同一个入口：带 worktree 的线程先问是否一并移除工作树
	const { archive, dialog: archiveDialog } = useArchiveThread();
	const onArchive = (thread: Thread) =>
		void archive(thread).catch((e: unknown) =>
			showToast({ text: describeError(e, t), tone: "err" }),
		);
	const items = useMemo(
		() => inboxItems(Object.values(threads), now),
		[threads, now],
	);
	const previews = useMemo(() => previewsOf(notifications), [notifications]);
	const counts = useMemo(() => {
		const c: Record<InboxState, number> = {
			you: 0,
			ready: 0,
			stopped: 0,
			running: 0,
		};
		for (const i of items) c[i.state]++;
		return c;
	}, [items]);
	// 筛选的那一类清空了（实时变化）就回到全部
	const only = filter && counts[filter] > 0 ? filter : null;
	const shown = only ? items.filter((i) => i.state === only) : items;
	const projectOf = (id: string | null | undefined): Project | undefined =>
		id ? projects.data?.projects.find((p) => p.id === id) : undefined;
	const pick = (v: InboxGroupBy) => {
		setGroupBy(v);
		saveInboxGroupBy(v);
	};
	const row = (item: InboxItem, card = false) => (
		<InboxRow
			key={item.thread.id}
			item={item}
			card={card}
			project={projectOf(item.thread.projectId)}
			parent={
				item.thread.parentThreadId
					? threads[item.thread.parentThreadId]
					: undefined
			}
			preview={previewOf(item, previews)}
			showState={groupBy === "project"}
			now={now}
			onArchive={onArchive}
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
			<div className="min-h-0 flex-1 overflow-y-auto px-3 pt-6 pb-safe sm:px-6 sm:pt-8">
				{items.length === 0 ? (
					<div data-testid="inbox-empty" className="pt-[12vh]">
						<EmptyState icon="tray" title={t("inbox.empty")}>
							{t("inbox.emptyHint")}
						</EmptyState>
					</div>
				) : (
					<div className="mx-auto flex max-w-3xl flex-col gap-6 pb-10">
						<InboxSummary counts={counts} only={only} onFilter={setFilter} />
						{groupBy === "state"
							? groupByState(shown).map((g) => (
									<InboxSection
										key={g.state}
										id={g.state}
										head={
											<>
												<ThreadStateIcon
													state={SECTION_GLYPH[g.state]}
													size={14}
												/>
												{t(`inbox.sections.${g.state}`)}
											</>
										}
										count={g.items.length}
										action={
											g.state === "ready" && g.items.length > 1 ? (
												<button
													type="button"
													data-inbox-read-all=""
													className="shrink-0 rounded-md px-2 py-1 font-medium text-2xs text-faint transition-colors duration-150 hover:bg-hover hover:text-fg"
													onClick={() => {
														for (const i of g.items)
															void patchThread(i.thread.id, { unread: false });
													}}
												>
													{t("inbox.markAllRead")}
												</button>
											) : undefined
										}
									>
										{g.state === "you" ? (
											<ul className="flex flex-col gap-2.5">
												{g.items.map((i) => row(i, true))}
											</ul>
										) : (
											<ul className={PANEL}>{g.items.map((i) => row(i))}</ul>
										)}
									</InboxSection>
								))
							: groupByProject(shown).map((g) => {
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
														<ProjectBadge
															name={name}
															icon={p?.icon}
															size={16}
														/>
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
											<ul className={PANEL}>{g.items.map((i) => row(i))}</ul>
										</InboxSection>
									);
								})}
					</div>
				)}
			</div>
			{archiveDialog}
		</Sheet>
	);
}

/** 行内预览：每种状态各取它最能说明「要你做什么」的一句。 */
function previewOf(item: InboxItem, previews: Previews): string | undefined {
	const { thread } = item;
	const n = previews.get(thread.id);
	switch (item.state) {
		case "you":
			return n?.input;
		case "ready":
			return n?.done;
		case "stopped":
			return thread.error?.reason || n?.error;
		case "running":
			return thread.state === "working" ? thread.activity : undefined;
	}
}

/**
 * 摘要：大号待处理数（等你处理 + 已完成未读 + 出错，与侧栏入口角标同口径）+ 状态筛选胶囊。
 * 再点一次已选的胶囊或点「全部」取消筛选。
 */
function InboxSummary({
	counts,
	only,
	onFilter,
}: {
	counts: Record<InboxState, number>;
	only: InboxState | null;
	onFilter: (s: InboxState | null) => void;
}): ReactNode {
	const { t } = useTranslation();
	const pending = counts.you + counts.ready + counts.stopped;
	const total = pending + counts.running;
	const chip = (on: boolean) =>
		`inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full border px-2.5 font-medium text-xs transition-colors duration-150 ${on ? "border-transparent bg-ink text-ink-fg" : "border-border bg-panel text-muted hover:bg-hover hover:text-fg"}`;
	return (
		<div data-inbox-summary="" className="flex flex-col gap-3 px-1">
			{pending > 0 ? (
				<p className="flex items-baseline gap-2.5">
					<span
						className={`font-semibold text-[2.5rem] leading-none tracking-tight tabular-nums ${counts.you > 0 ? "text-accent-ink" : "text-fg"}`}
					>
						{pending}
					</span>
					<span className="font-medium text-base text-muted">
						{t("inbox.pending", { count: pending })}
					</span>
				</p>
			) : (
				<p className="font-semibold text-2xl tracking-tight">
					{t("inbox.allClear")}
				</p>
			)}
			{/* biome-ignore lint/a11y/useSemanticElements: 筛选胶囊（aria-pressed），不是表单字段集 */}
			<div
				role="group"
				aria-label={t("inbox.filter")}
				className="flex flex-wrap items-center gap-1.5"
			>
				<button
					type="button"
					aria-pressed={only === null}
					onClick={() => onFilter(null)}
					className={chip(only === null)}
				>
					{t("inbox.all")}
					<span className="tabular-nums opacity-60">{total}</span>
				</button>
				{INBOX_ORDER.filter((s) => counts[s] > 0).map((s) => (
					<button
						key={s}
						type="button"
						data-inbox-filter={s}
						aria-pressed={only === s}
						onClick={() => onFilter(only === s ? null : s)}
						className={chip(only === s)}
					>
						<ThreadStateIcon state={SECTION_GLYPH[s]} size={12} />
						{t(`inbox.sections.${s}`)}
						<span className="tabular-nums opacity-60">{counts[s]}</span>
					</button>
				))}
			</div>
		</div>
	);
}

function InboxSection({
	id,
	head,
	count,
	action,
	children,
}: {
	id: string;
	head: ReactNode;
	count: number;
	action?: ReactNode;
	children: ReactNode;
}): ReactNode {
	return (
		<section data-inbox-section={id} className="flex flex-col gap-2.5">
			<div className="flex h-7 items-center gap-2 px-1">
				<h2 className="flex min-w-0 flex-1 items-center gap-2 font-semibold text-muted text-xs">
					{head}
					<CountPill n={count} />
				</h2>
				{action}
			</div>
			{children}
		</section>
	);
}

/** 预览文字里的 `行内代码` 渲染成等宽小块；其余原样。 */
function withCode(text: string): ReactNode {
	const parts = text.split(/(`[^`\n]+`)/);
	return parts.map((p, i) =>
		p.length > 2 && p.startsWith("`") && p.endsWith("`") ? (
			<code
				// biome-ignore lint/suspicious/noArrayIndexKey: 切分结果顺序固定
				key={i}
				className="rounded-xs bg-inset px-1 py-px font-mono text-[0.92em]"
			>
				{p.slice(1, -1)}
			</code>
		) : (
			p
		),
	);
}

function InboxRow({
	item,
	card,
	project,
	parent,
	preview,
	showState,
	now,
	onArchive,
}: {
	item: InboxItem;
	/** 「等你处理」的独立卡片（按状态分组时）；否则是面板列表里的一行 */
	card: boolean;
	project: Project | undefined;
	/** 父线程（fork / 协调者派生）；协调对话不显示来源 */
	parent: Thread | undefined;
	preview: string | undefined;
	/** 按项目分组时第二行写收件箱分节名（项目已是组标题） */
	showState: boolean;
	now: number;
	onArchive: (thread: Thread) => void;
}): ReactNode {
	const { t } = useTranslation();
	const { thread, state } = item;
	const tcard = parseTitleCard(thread.title);
	const title = tcard.text || t("thread.untitled");
	const from =
		parent && !isCoordinatorThread(parent)
			? parseTitleCard(parent.title).text || t("thread.untitled")
			: undefined;
	// 已完成未读与出错的行可以就地清掉；等你处理要进线程作答，运行中不该打断
	const triage = state === "ready" || state === "stopped";
	// 等你处理在两种分组下都带强调底色与「去回复」：它是这页的主动作
	const reply = state === "you";
	return (
		<li
			className={`group/row relative ${card ? "rounded-lg border border-accent/35 bg-accent-soft/55 shadow-xs transition-[border-color,box-shadow] duration-150 hover:border-accent/60 hover:shadow-sm" : ""}`}
		>
			<Link
				to={`/t/${thread.id}`}
				data-inbox-row={thread.id}
				data-inbox-state={state}
				className={`flex items-start gap-3 transition-colors duration-150 ${card ? "rounded-lg px-3.5 py-3" : reply ? "bg-accent-soft/45 px-3.5 py-2.5 hover:bg-accent-soft/75" : "px-3.5 py-2.5 hover:bg-hover"}`}
			>
				<span className="mt-[3px] inline-flex">
					<ThreadStateIcon state={thread.state} size={14} />
				</span>
				<span className="flex min-w-0 flex-1 flex-col gap-1">
					<span className="flex min-w-0 items-center gap-2">
						{tcard.icon && <TitleGlyph icon={tcard.icon} seed={title} />}
						<span
							className={`min-w-0 flex-1 truncate text-sm ${thread.unread ? "font-semibold text-fg" : "font-medium text-fg"}`}
						>
							{title}
						</span>
						<span
							className={`shrink-0 text-2xs text-faint tabular-nums ${triage ? "[@media(hover:hover)]:group-focus-within/row:invisible [@media(hover:hover)]:group-hover/row:invisible" : ""}`}
						>
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
							<span className="shrink-0">{t(`inbox.sections.${state}`)}</span>
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
						{reply && (
							<span className="ml-auto flex shrink-0 items-center gap-0.5 pl-2 font-medium text-accent-ink">
								{t("inbox.reply")}
								<Icon
									name="chevronRight"
									size={12}
									aria-hidden
									className="transition-transform duration-150 group-hover/row:translate-x-0.5"
								/>
							</span>
						)}
					</span>
					{preview && <Preview state={state} text={preview} />}
				</span>
			</Link>
			{triage && (
				<span className="absolute top-2 right-2.5 hidden items-center gap-0.5 rounded-md border border-border bg-panel p-0.5 opacity-0 shadow-xs transition-opacity duration-150 group-focus-within/row:opacity-100 group-hover/row:opacity-100 [@media(hover:hover)]:flex">
					{thread.unread && (
						<IconButton
							icon="check"
							size={14}
							box="xs"
							label={t("inbox.markRead")}
							onClick={() => void patchThread(thread.id, { unread: false })}
						/>
					)}
					<IconButton
						icon="archive"
						size={14}
						box="xs"
						label={t("thread.archive")}
						onClick={() => onArchive(thread)}
					/>
				</span>
			)}
		</li>
	);
}

/** 行内预览块：等你处理是问题原文（引用块），出错是失败原因（等宽），其余是一两行说明。 */
function Preview({
	state,
	text,
}: {
	state: InboxState;
	text: string;
}): ReactNode {
	if (state === "you")
		return (
			<span
				data-inbox-preview=""
				className="mt-1 flex items-start gap-2 rounded-md bg-panel px-3 py-2 text-fg text-sm"
			>
				<Icon
					name="question"
					size={13}
					aria-hidden
					className="mt-[3px] shrink-0 text-accent-ink"
				/>
				<span className="line-clamp-3 min-w-0 break-words">
					{withCode(text)}
				</span>
			</span>
		);
	if (state === "stopped")
		return (
			<span
				data-inbox-preview=""
				className="mt-1 line-clamp-2 break-words rounded-md bg-err/8 px-2 py-1 font-mono text-[11.5px] text-err leading-5"
			>
				{text}
			</span>
		);
	return (
		<span
			data-inbox-preview=""
			{...(state === "running" ? { "data-inbox-activity": "" } : {})}
			className={`min-w-0 break-words text-xs leading-5 ${state === "running" ? "truncate text-faint" : "line-clamp-2 text-muted"}`}
		>
			{withCode(text)}
		</span>
	);
}
