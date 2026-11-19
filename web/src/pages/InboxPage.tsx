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
import {
	type CSSProperties,
	type ReactNode,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	ApiError,
	api,
	type Project,
	type Thread,
	type ThreadState,
	unwrap,
} from "../api/client";
import { useProjects } from "../api/queries";
import { CountPill, useNow } from "../components/layout/pane-parts";
import {
	ChromeLead,
	WindowControls,
} from "../components/layout/WindowControls";
import { useArchiveThread } from "../components/thread/ArchiveDialog";
import { ProjectBadge, Sheet, TitleGlyph } from "../components/ui/data";
import { Icon } from "../components/ui/icons";
import {
	Button,
	describeError,
	EmptyState,
	ErrorText,
	IconButton,
	Spinner,
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
import {
	type AppNotification,
	notificationsStore,
	useNotifications,
} from "../state/notifications";
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

/** 首次进入的依次出场在这之后不再播（最多 12 档 × 40ms + 260ms，见 index.css 的 `.inbox-intro`）。 */
const INTRO_MS = 800;
/** 离开的行收起的时长，与 index.css 的 `.inbox-leave` 一致。 */
const LEAVE_MS = 180;
/** 新到的行保留 `.inbox-arrive` 的时长（≥ 动画 220ms）：期间重渲染不会摘掉类名把动画截断。 */
const ARRIVE_MS = 300;

/** 行的键含状态：同一线程换了分节算旧的离开、新的加入，两处各播各的。 */
function itemKey(i: InboxItem): string {
	return `${i.thread.id}:${i.state}`;
}

type LiveItem = InboxItem & { leaving?: boolean };

/** 一行的出场 / 离场：动效类名与出场序号（`--i`）。 */
type Motion = { className: string; style?: CSSProperties };

/**
 * 收件箱条目的进出：离开的条目（标为已读、归档、作答后换了状态）再留 LEAVE_MS，带 `leaving` 播收起动画、留在原位；
 * 新到的条目（上一次提交里没有这个键）记入 `arrived`，ARRIVE_MS 内带 `.inbox-arrive`。
 * 离场记录按键唯一：同键回来就撤销离场并取消计时器，再次离开换成新记录与新计时器（旧回调已被清掉，删不到新一轮）。
 * 计时器只在键回来或卸载时清：`items` 每分钟随 `now` 重算，不能因此取消收起。
 * 首次挂载不算新到（首次出场由页面的 intro 负责）；切分组 / 筛选不改 `items`，所以不会重播。
 */
function useInboxPresence(items: readonly InboxItem[]): {
	live: LiveItem[];
	arrived: ReadonlySet<string>;
} {
	const prev = useRef<readonly InboxItem[] | null>(null);
	const timers = useRef(new Map<string, number>());
	const arrivals = useRef(new Map<string, number>());
	const [gone, setGone] = useState<ReadonlyMap<string, InboxItem>>(
		() => new Map(),
	);
	// 渲染时就要知道谁是新到的（它的第一帧就得带动效类）；对照的是上一次提交的条目
	const keys = items.map(itemKey);
	const t0 = Date.now();
	if (prev.current) {
		const before = new Set(prev.current.map(itemKey));
		for (const k of keys)
			if (!before.has(k) && !arrivals.current.has(k))
				arrivals.current.set(k, t0);
	}
	const arrived = new Set<string>();
	for (const [k, at] of arrivals.current)
		if (t0 - at < ARRIVE_MS) arrived.add(k);
	useEffect(() => {
		const keep = new Set(items.map(itemKey));
		for (const [k, at] of arrivals.current)
			if (!keep.has(k) || Date.now() - at >= ARRIVE_MS)
				arrivals.current.delete(k);
		for (const [k, t] of timers.current)
			if (keep.has(k)) {
				clearTimeout(t);
				timers.current.delete(k);
			}
		const left = (prev.current ?? []).filter((i) => !keep.has(itemKey(i)));
		prev.current = items;
		setGone((g) => {
			let next: Map<string, InboxItem> | null = null;
			for (const k of g.keys())
				if (keep.has(k)) {
					next ??= new Map(g);
					next.delete(k);
				}
			for (const i of left) {
				next ??= new Map(g);
				next.set(itemKey(i), i);
			}
			return next ?? g;
		});
		for (const i of left) {
			const k = itemKey(i);
			clearTimeout(timers.current.get(k));
			timers.current.set(
				k,
				window.setTimeout(() => {
					timers.current.delete(k);
					setGone((g) => {
						if (!g.has(k)) return g;
						const next = new Map(g);
						next.delete(k);
						return next;
					});
				}, LEAVE_MS),
			);
		}
	}, [items]);
	useEffect(() => {
		const pending = timers.current;
		return () => {
			for (const t of pending.values()) clearTimeout(t);
		};
	}, []);
	const live = useMemo(() => {
		const keep = new Set(items.map(itemKey));
		const leaving = [...gone.entries()]
			.filter(([k]) => !keep.has(k))
			.map(([, i]) => ({ ...i, leaving: true }));
		if (leaving.length === 0) return [...items];
		return [...items, ...leaving].sort(
			(a, b) => activityOf(b.thread) - activityOf(a.thread),
		);
	}, [items, gone]);
	return { live, arrived };
}

/** 每个线程最近的一条未作答 input 通知，以及最近的 done / error 通知正文，作行内预览。 */
type Previews = Map<
	string,
	{ input?: AppNotification; done?: string; error?: string }
>;

function previewsOf(items: readonly AppNotification[]): Previews {
	const out: Previews = new Map();
	// 通知按时间新的在前：同一线程同一类只取第一条
	for (const n of items) {
		if (!n.threadId) continue;
		const slot = out.get(n.threadId) ?? {};
		if (n.kind === "input" && !n.resolvedAt && !slot.input) slot.input = n;
		if (n.kind === "done" && n.body && slot.done === undefined)
			slot.done = n.body;
		if (n.kind === "error" && n.body && slot.error === undefined)
			slot.error = n.body;
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
	// 进出：离开的条目再留一会儿播收起动画，新到的条目淡入（见 useInboxPresence）；计数与筛选仍按真实条目
	const { live, arrived } = useInboxPresence(items);
	// 首次进入：摘要、分节、行按阅读顺序依次出场，只播一次
	const [intro, setIntro] = useState(true);
	useEffect(() => {
		const t = setTimeout(() => setIntro(false), INTRO_MS);
		return () => clearTimeout(t);
	}, []);
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
	const shown = only ? live.filter((i) => i.state === only) : live;
	const projectOf = (id: string | null | undefined): Project | undefined =>
		id ? projects.data?.projects.find((p) => p.id === id) : undefined;
	const pick = (v: InboxGroupBy) => {
		setGroupBy(v);
		saveInboxGroupBy(v);
	};
	// 出场序号：摘要是 0，之后分节标题与行按渲染顺序递增
	let order = 1;
	const enter = (): { className: string; style?: CSSProperties } =>
		intro
			? {
					className: "inbox-intro",
					style: { "--i": order++ } as CSSProperties,
				}
			: { className: "" };
	const row = (item: LiveItem, card = false) => {
		const p = previewOf(item, previews);
		const key = itemKey(item);
		const motion = item.leaving
			? { className: "inbox-leave" }
			: intro
				? enter()
				: { className: arrived.has(key) ? "inbox-arrive" : "" };
		return (
			<InboxRow
				key={key}
				item={item}
				motion={motion}
				card={card}
				project={projectOf(item.thread.projectId)}
				parent={
					item.thread.parentThreadId
						? threads[item.thread.parentThreadId]
						: undefined
				}
				preview={p.text}
				ask={p.ask}
				showState={groupBy === "project"}
				now={now}
				onArchive={onArchive}
			/>
		);
	};
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
				{live.length === 0 ? (
					<div data-testid="inbox-empty" className="pt-[12vh]">
						<EmptyState icon="tray" title={t("inbox.empty")}>
							{t("inbox.emptyHint")}
						</EmptyState>
					</div>
				) : (
					<div className="mx-auto flex max-w-3xl flex-col gap-6 pb-10">
						<InboxSummary
							counts={counts}
							only={only}
							onFilter={setFilter}
							motion={intro ? { className: "inbox-intro" } : { className: "" }}
							intro={intro}
						/>
						{groupBy === "state"
							? groupByState(shown).map((g) => (
									<InboxSection
										key={g.state}
										id={g.state}
										motion={enter()}
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
															if (!i.leaving)
																void patchThread(i.thread.id, {
																	unread: false,
																});
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
											motion={enter()}
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

/**
 * 行内预览：每种状态各取它最能说明「要你做什么」的一句。等你处理另给出可一触作答的 input 通知（`ask`）：
 * 取该线程最新一条未解决的 input 通知。解决状态靠 WS `notifications.resolved` 与本页作答结果维护（见 notificationsStore.onResolved），
 * 不拿线程的 stateChangedAt 比：agent 断线重放同一请求时状态时间会变，而通知按请求去重、发出时间不变。
 */
function previewOf(
	item: InboxItem,
	previews: Previews,
): { text?: string; ask?: AppNotification } {
	const { thread } = item;
	const n = previews.get(thread.id);
	switch (item.state) {
		case "you":
			return {
				text: n?.input?.body || undefined,
				ask: n?.input?.quickAnswer ? n.input : undefined,
			};
		case "ready":
			return { text: n?.done };
		case "stopped":
			return { text: thread.error?.reason || n?.error };
		case "running":
			return {
				text: thread.state === "working" ? thread.activity : undefined,
			};
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
	motion,
	intro,
}: {
	counts: Record<InboxState, number>;
	only: InboxState | null;
	onFilter: (s: InboxState | null) => void;
	motion: Motion;
	/** 首次进入：数字不弹（之后换值时轻弹一下，交代「少了一项」） */
	intro: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const pending = counts.you + counts.ready + counts.stopped;
	const total = pending + counts.running;
	const chip = (on: boolean) =>
		`inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full border px-2.5 font-medium text-xs transition-colors duration-150 ${on ? "border-transparent bg-ink text-ink-fg" : "border-border bg-panel text-muted hover:bg-hover hover:text-fg"}`;
	return (
		<div
			data-inbox-summary=""
			className={`flex flex-col gap-3 px-1 ${motion.className}`}
			style={motion.style}
		>
			{pending > 0 ? (
				<p className="flex items-baseline gap-2.5">
					<span
						key={pending}
						className={`font-semibold text-[2.5rem] leading-none tracking-tight tabular-nums ${counts.you > 0 ? "text-accent-ink" : "text-fg"} ${intro ? "" : "inbox-pop"}`}
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
	motion,
	children,
}: {
	id: string;
	head: ReactNode;
	count: number;
	action?: ReactNode;
	motion: Motion;
	children: ReactNode;
}): ReactNode {
	return (
		<section data-inbox-section={id} className="flex flex-col gap-2.5">
			<div
				className={`flex h-7 items-center gap-2 px-1 ${motion.className}`}
				style={motion.style}
			>
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
	motion,
	card,
	project,
	parent,
	preview,
	ask,
	showState,
	now,
	onArchive,
}: {
	item: LiveItem;
	/** 出场 / 离场动效：首次进入依次出场、新出现淡入、离开收起 */
	motion: Motion;
	/** 「等你处理」的独立卡片（按状态分组时）；否则是面板列表里的一行 */
	card: boolean;
	project: Project | undefined;
	/** 父线程（fork / 协调者派生）；协调对话不显示来源 */
	parent: Thread | undefined;
	preview: string | undefined;
	/** 可一触作答的 input 通知：问题与选项在行内（链接外）作答 */
	ask: AppNotification | undefined;
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
		// 动效类在外层 li（离开时它是单行 grid，0fr 收起高度）；卡片外观在里层
		<li
			className={motion.className}
			style={motion.style}
			inert={item.leaving}
			aria-hidden={item.leaving}
		>
			<div
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
									<Icon
										name="fork"
										size={11}
										aria-hidden
										className="shrink-0"
									/>
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
						{preview && !ask && (
							<Preview state={state} text={preview} failure={thread.error} />
						)}
					</span>
				</Link>
				{ask?.quickAnswer && (
					// 换成同一线程的另一条提问时重新挂载：按钮的忙碌 / 错误状态属于那一条
					<QuickAnswer
						key={ask.id}
						id={ask.id}
						quick={ask.quickAnswer}
						tinted={!card}
					/>
				)}
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
			</div>
		</li>
	);
}

/**
 * 行内一触作答：完整问题按原文纯文本显示（不解析反引号等标记：审批要看清真正会执行的命令）+ 选项按钮，
 * 走 `POST notifications/{id}/answer`。作答成功或得到 409 `input_resolved`（已在别处作答 / 取消）都把这条通知记为已解决，
 * 这一块随之消失；同一线程若还有更早的未决提问，行内换成它。409 另用 toast 说明。
 */
function QuickAnswer({
	id,
	quick,
	tinted,
}: {
	id: string;
	quick: NonNullable<AppNotification["quickAnswer"]>;
	/** 面板列表里的行（按项目分组）：底色与行一致 */
	tinted: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const [busy, setBusy] = useState<number | null>(null);
	const [error, setError] = useState<unknown>(null);
	const pick = async (option: number) => {
		setBusy(option);
		setError(null);
		try {
			await unwrap(
				api.POST("/api/v1/notifications/{id}/answer", {
					params: { path: { id } },
					body: { option },
				}),
			);
			notificationsStore.getState().onResolved([id], Date.now());
		} catch (e) {
			setBusy(null);
			if (e instanceof ApiError && e.code === "input_resolved") {
				showToast({ text: t("inbox.answeredElsewhere") });
				notificationsStore.getState().onResolved([id], Date.now());
				return;
			}
			setError(e);
		}
	};
	return (
		<div
			data-inbox-quick=""
			className={`flex flex-col gap-2.5 pr-3.5 pb-3 pl-10 ${tinted ? "bg-accent-soft/45" : ""}`}
		>
			<p className="flex items-start gap-2 rounded-md bg-panel px-3 py-2 text-fg text-sm">
				<Icon
					name="question"
					size={13}
					aria-hidden
					className="mt-[3px] shrink-0 text-accent-ink"
				/>
				<span
					data-inbox-question=""
					className="min-w-0 whitespace-pre-wrap break-words"
				>
					{quick.question}
				</span>
			</p>
			{/* biome-ignore lint/a11y/useSemanticElements: 一组作答按钮，不是表单字段集 */}
			<div
				role="group"
				aria-label={t("inbox.answer")}
				className="flex flex-wrap items-center gap-1.5"
			>
				{quick.options.map((o) => (
					<Button
						key={o.i}
						variant={o.role === "yes" ? "accent" : "secondary"}
						disabled={busy !== null}
						onClick={() => void pick(o.i)}
					>
						{busy === o.i && <Spinner size={13} />}
						{o.role
							? t(o.role === "yes" ? "common.yes" : "common.no")
							: o.label}
					</Button>
				))}
			</div>
			<ErrorText error={error} />
		</div>
	);
}

/**
 * 行内预览块：等你处理是问题原文（引用块），其余是一两行说明。出错时后端认得出原因（`error.cause`）就先写一句人话
 * （带抽出的镜像名 / 退出码 / 目录），原文降为一行等宽小字（悬停看全文）；认不出时原文照旧放在错误色块里。
 */
function Preview({
	state,
	text,
	failure,
}: {
	state: InboxState;
	text: string;
	failure?: Thread["error"];
}): ReactNode {
	const { t } = useTranslation();
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
				{/* 提问原文（审批可能含命令）：纯文本，不解析反引号 */}
				<span className="line-clamp-3 min-w-0 whitespace-pre-wrap break-words">
					{text}
				</span>
			</span>
		);
	if (state === "stopped" && failure?.cause && failure.cause !== "unknown")
		return (
			<span
				data-inbox-preview=""
				data-inbox-cause={failure.cause}
				className="mt-1 flex min-w-0 flex-col gap-1 rounded-md bg-err/8 px-2.5 py-1.5"
			>
				<span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-err text-sm">
					<Icon name="alert" size={13} aria-hidden className="shrink-0" />
					<span className="font-medium">
						{t(`inbox.cause.${failure.cause}`)}
					</span>
					{failure.detail && (
						<code className="min-w-0 truncate rounded-xs bg-err/10 px-1.5 font-mono text-xs">
							{failure.cause === "provider_command"
								? t("inbox.exitCode", { code: failure.detail })
								: failure.detail}
						</code>
					)}
				</span>
				<span
					title={text}
					className="min-w-0 truncate font-mono text-[11px] text-err/70 leading-4"
				>
					{text}
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
