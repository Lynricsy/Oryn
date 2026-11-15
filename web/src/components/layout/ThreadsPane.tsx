/**
 * 「线程」分区侧栏（按最新 Amp 布局）：搜索、项目过滤签；分组 空间 → 已置顶 → 按项目分组的活跃线程 → 72 小时未活动（一键归档）→
 * 稍后提醒 → 已归档。协调对话（Space 聊天、助手对话）不进任何线程分组（助手对话在「助手」分区）。
 * 线程行 = 状态图标 + 标题卡图标 + 标题 + 相对时间 / 子线程计数 / 未读点（标题前最多两个标记）；线程色是行左缘的一根细竖条，
 * 有标题卡图标时图标底色也取线程色。fork 出的子线程挂在父线程下，带 L 形引导线；带线程内定时的行标一个时钟。
 * 「每次新建线程」定时建出的线程在项目分组里按定时折叠成一行（防刷屏），点开才是各次运行。
 * 链接的 textContent 必须只等于标题文字：图标字形、时间、计数、活动行一律走 data 属性 + 伪元素（或无文字的图标）。
 */

import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { NavLink } from "react-router";
import type { Project, Thread, ThreadState } from "../../api/client";
import { qk, useArchivedThreads, useProjects } from "../../api/queries";
import { useSchedules } from "../../api/schedule-queries";
import { type Space, useSpaces } from "../../api/space-queries";
import { formatRelative } from "../../lib/format";
import { isCoordinatorThread } from "../../lib/thread-kind";
import { parseTitleCard } from "../../lib/title";
import { patchThread } from "../../state/actions";
import { useIndex } from "../../state/stores";
import { uiStore, useUi } from "../../state/ui";
import { SpaceDialog } from "../space/SpaceDialog";
import { hueVar, threadHue } from "../thread/ThreadHeader";
import { nameHue, ProjectBadge } from "../ui/data";
import { Icon } from "../ui/icons";
import { IconButton, Kbd, ThreadStateIcon } from "../ui/primitives";
import {
	CountPill,
	closeOnCompact,
	Group,
	GroupIcon,
	NAV_ROW,
	PANE_SCROLL,
	UnreadDot,
	useNow,
} from "./pane-parts";

const INACTIVE_MS = 72 * 3600_000;

/** 一个「每次新建线程」定时在某项目下的折叠：运行线程新的在前。 */
export interface ScheduleStackGroup {
	scheduleId: string;
	threads: Thread[];
}

export interface SidebarGroups {
	pinned: Thread[];
	byProject: {
		projectId: string | null;
		threads: Thread[];
		stacks: ScheduleStackGroup[];
	}[];
	inactive: Thread[];
	snoozed: Thread[];
	children: Map<string, Thread[]>;
}

function activityOf(t: Thread): number {
	return t.lastMessageAt ?? t.updatedAt;
}

/** 侧栏的项目过滤：`undefined` 不过滤，`null` 只留无项目线程，否则只留该项目的线程。 */
export function filterByProject(
	threads: readonly Thread[],
	filter: string | null | undefined,
): Thread[] {
	if (filter === undefined) return [...threads];
	return threads.filter((t) => (t.projectId ?? null) === filter);
}

/** 折叠行的汇总状态：最需要你的那个（等待输入 > 出错 > 工作中 > 准备中 > 空闲）。 */
const STATE_RANK: Record<ThreadState, number> = {
	awaiting_input: 4,
	error: 3,
	working: 2,
	provisioning: 1,
	idle: 0,
};

export function stackState(threads: readonly Thread[]): ThreadState {
	let best: ThreadState = "idle";
	for (const t of threads)
		if (STATE_RANK[t.state] > STATE_RANK[best]) best = t.state;
	return best;
}

/** 把线程索引分到侧边栏各分区（纯函数，便于测试）。 */
export function groupThreads(
	threads: readonly Thread[],
	q: string,
	now: number,
): SidebarGroups {
	const query = q.trim().toLowerCase();
	const match = (t: Thread) =>
		!query ||
		t.title.toLowerCase().includes(query) ||
		t.labels.some((l) => l.toLowerCase().includes(query));
	const visible = threads.filter(
		(t) => !t.archivedAt && !isCoordinatorThread(t) && match(t),
	);
	const ids = new Set(visible.map((t) => t.id));
	const children = new Map<string, Thread[]>();
	const roots: Thread[] = [];
	for (const t of visible) {
		if (t.parentThreadId && ids.has(t.parentThreadId)) {
			const list = children.get(t.parentThreadId) ?? [];
			list.push(t);
			children.set(t.parentThreadId, list);
		} else roots.push(t);
	}
	for (const list of children.values())
		list.sort((a, b) => activityOf(b) - activityOf(a));
	roots.sort((a, b) => activityOf(b) - activityOf(a));
	const g: SidebarGroups = {
		pinned: [],
		byProject: [],
		inactive: [],
		snoozed: [],
		children,
	};
	const projects = new Map<string | null, Thread[]>();
	const stacks = new Map<string | null, Map<string, Thread[]>>();
	const bucket = (key: string | null) => {
		const list = projects.get(key) ?? [];
		projects.set(key, list);
		return list;
	};
	for (const t of roots) {
		if (t.pinned) g.pinned.push(t);
		else if (t.snoozedUntil && t.snoozedUntil > now) g.snoozed.push(t);
		else if (t.scheduleRun) {
			// 定时建出的线程：按定时折叠，不进「72 小时未活动」（数量由服务端的 keepLast 限制）
			const key = t.projectId ?? null;
			bucket(key);
			const byStack = stacks.get(key) ?? new Map<string, Thread[]>();
			const list = byStack.get(t.scheduleRun.scheduleId) ?? [];
			list.push(t);
			byStack.set(t.scheduleRun.scheduleId, list);
			stacks.set(key, byStack);
		} else if (
			now - activityOf(t) > INACTIVE_MS &&
			t.state !== "working" &&
			t.state !== "awaiting_input"
		)
			g.inactive.push(t);
		else bucket(t.projectId ?? null).push(t);
	}
	g.byProject = [...projects.entries()].map(([projectId, list]) => ({
		projectId,
		threads: list,
		stacks: [...(stacks.get(projectId)?.entries() ?? [])].map(
			([scheduleId, runs]) => ({
				scheduleId,
				threads: runs.sort(
					(a, b) => (b.scheduleRun?.run ?? 0) - (a.scheduleRun?.run ?? 0),
				),
			}),
		),
	}));
	return g;
}

/** 标题卡图标：外观同 `TitleGlyph`，但字形放在伪元素里，不进链接的 textContent。 */
function CardGlyph({ icon, hue }: { icon: string; hue: number }) {
	return (
		<span
			aria-hidden
			data-glyph={icon}
			className="avatar inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-xs text-[11px] leading-none before:content-[attr(data-glyph)]"
			style={hueVar(hue)}
		/>
	);
}

export function ThreadRow({
	thread,
	childMap,
	depth = 0,
	ancestors,
	now,
	time,
}: {
	thread: Thread;
	/** 父线程 id → 子线程；每一层都从这里取，fork 再 fork 的后代逐级缩进 */
	childMap?: ReadonlyMap<string, Thread[]>;
	depth?: number;
	/** 已渲染的祖先（防止数据里出现环时无限递归） */
	ancestors?: ReadonlySet<string>;
	/** 相对时间的基准时刻（侧栏每分钟刷新一次）；缺省取当前时间 */
	now?: number;
	/** 行尾时间文字；缺省为最后活动的相对时间 */
	time?: string;
}) {
	const { t } = useTranslation();
	const card = parseTitleCard(thread.title);
	const title = card.text || t("thread.untitled");
	const stamp = time ?? formatRelative(activityOf(thread), now ?? Date.now());
	const hue = threadHue(thread.color);
	const activity = thread.state === "working" ? thread.activity : undefined;
	const seen = new Set(ancestors);
	seen.add(thread.id);
	const kids = (childMap?.get(thread.id) ?? []).filter((c) => !seen.has(c.id));
	// 子线程引导线的竖线落在本行状态图标的中线上
	const guideLeft = `calc(${0.5 + depth}rem + 6px)`;
	return (
		<>
			<NavLink
				to={`/t/${thread.id}`}
				onClick={closeOnCompact}
				className={({ isActive }) =>
					`relative flex flex-col rounded-lg pr-2 text-sm transition-colors duration-150 ${isActive ? "bg-[var(--sb-sel)] font-medium text-fg" : `${depth ? "text-muted" : "text-fg"} hover:bg-[var(--sb-hover)] hover:text-fg`}`
				}
				style={{ paddingLeft: `${0.5 + depth}rem` }}
			>
				{hue !== undefined && (
					<span
						aria-hidden
						className="thread-mark absolute top-2 left-0.5 h-4 w-[3px] rounded-full"
						style={hueVar(hue)}
					/>
				)}
				<span className="flex h-8 items-center gap-2">
					<ThreadStateIcon state={thread.state} size={14} />
					{card.icon && (
						<CardGlyph icon={card.icon} hue={hue ?? nameHue(title)} />
					)}
					<span
						className={`min-w-0 flex-1 truncate ${thread.unread ? "font-medium text-fg" : ""}`}
					>
						{title}
					</span>
					{thread.schedule?.enabled && (
						// 线程内定时：只是一个无文字的时钟图标（不进链接的 textContent）
						<span
							role="img"
							data-schedule=""
							aria-label={t("sidebar.scheduled")}
							title={t("sidebar.scheduled")}
							className="inline-flex shrink-0 text-faint"
						>
							<Icon name="clock" size={12} aria-hidden />
						</span>
					)}
					{thread.childCount ? (
						<span
							data-count={`${thread.childDoneCount ?? 0}/${thread.childCount}`}
							className="inline-flex h-[18px] shrink-0 items-center gap-0.5 rounded-sm border border-fg/12 pr-1.5 pl-1 font-semibold text-2xs text-muted tabular-nums leading-none after:content-[attr(data-count)]"
						>
							<Icon name="fork" size={11} aria-hidden />
						</span>
					) : thread.unread ? null : (
						<span
							aria-hidden
							data-time={stamp}
							className="shrink-0 font-normal text-2xs text-faint tabular-nums after:content-[attr(data-time)]"
						/>
					)}
					{thread.unread && (
						<span
							role="img"
							className="mr-0.5 h-[7px] w-[7px] shrink-0 rounded-full bg-accent ring-[3px] ring-accent/20"
							aria-label={t("sidebar.unread")}
						/>
					)}
				</span>
				{activity && (
					<span
						aria-hidden
						data-activity={activity}
						className="-mt-1.5 truncate pb-1.5 pl-[22px] font-normal text-2xs text-faint after:content-[attr(data-activity)]"
					/>
				)}
			</NavLink>
			{kids.map((c, i) => (
				<div key={c.id} className="relative">
					<span
						aria-hidden
						className="pointer-events-none absolute -top-2 z-[1] h-6 rounded-bl-[6px] border-border-strong border-b-[1.5px] border-l-[1.5px]"
						style={{ left: guideLeft, width: "calc(1rem - 9px)" }}
					/>
					{i < kids.length - 1 && (
						<span
							aria-hidden
							className="pointer-events-none absolute top-4 bottom-0 z-[1] border-border-strong border-l-[1.5px]"
							style={{ left: guideLeft }}
						/>
					)}
					<ThreadRow
						thread={c}
						childMap={childMap}
						depth={depth + 1}
						ancestors={seen}
						now={now}
					/>
				</div>
			))}
		</>
	);
}

function ArchivedGroup({ now }: { now: number }) {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const archived = useArchivedThreads(open);
	return (
		<Group
			fold
			title={t("sidebar.archived")}
			icon={<GroupIcon name="archive" />}
			open={open}
			onOpenChange={setOpen}
		>
			{archived.data?.threads.map((th) => (
				<ThreadRow key={th.id} thread={th} now={now} />
			))}
			{archived.data?.threads.length === 0 && (
				<p className="px-3 py-1 text-faint text-xs">{t("sidebar.empty")}</p>
			)}
		</Group>
	);
}

/**
 * 「每次新建线程」定时的折叠行：循环时钟 + 定时名 + 汇总状态 + 未读点 + 运行数；点开是缩进一级的运行线程，
 * 末尾「全部运行」跳到设置 → 自动化里该定时的对话框。
 */
function ScheduleStack({
	stack,
	name,
	childMap,
	now,
}: {
	stack: ScheduleStackGroup;
	name: string | undefined;
	childMap: ReadonlyMap<string, Thread[]>;
	now: number;
}) {
	const { t } = useTranslation();
	const open = useUi((s) => Boolean(s.expandedStacks[stack.scheduleId]));
	const unread = stack.threads.some((th) => th.unread);
	const label = name || t("sidebar.scheduledRuns");
	return (
		<div data-schedule-stack={stack.scheduleId}>
			<button
				type="button"
				aria-expanded={open}
				title={label}
				onClick={() => uiStore.getState().toggleStack(stack.scheduleId)}
				className="flex h-8 w-full items-center gap-2 rounded-lg pr-2 pl-2 text-left text-fg text-sm transition-colors duration-150 hover:bg-[var(--sb-hover)]"
			>
				<Icon
					name="repeat"
					size={14}
					aria-hidden
					className="shrink-0 text-muted"
				/>
				<span
					className={`min-w-0 flex-1 truncate ${unread ? "font-medium" : ""}`}
				>
					{label}
				</span>
				<ThreadStateIcon state={stackState(stack.threads)} size={13} />
				{unread && <UnreadDot />}
				<CountPill n={stack.threads.length} />
			</button>
			{open && (
				<div className="flex flex-col">
					{stack.threads.map((th) => (
						<ThreadRow
							key={th.id}
							thread={th}
							childMap={childMap}
							depth={1}
							now={now}
						/>
					))}
					<NavLink
						to={`/settings/automations?schedule=${stack.scheduleId}`}
						onClick={closeOnCompact}
						className="flex h-7 items-center gap-1.5 rounded-lg pr-2 pl-[calc(1.5rem+22px)] text-muted text-xs transition-colors duration-150 hover:bg-[var(--sb-hover)] hover:text-fg"
					>
						{t("sidebar.allRuns")}
						<Icon name="chevronRight" size={11} aria-hidden />
					</NavLink>
				</div>
			)}
		</div>
	);
}

/** 「空间」分组：每行 = 聊天的状态 + Space 名 + 聊天未读点 + 未归档线程数；标题行带「新建」。 */
function SpacesGroup({ threads }: { threads: Record<string, Thread> }) {
	const { t } = useTranslation();
	const spaces = useSpaces();
	const [creating, setCreating] = useState(false);
	const list = spaces.data?.spaces ?? [];
	const counts = useMemo(() => {
		const m = new Map<string, number>();
		for (const th of Object.values(threads))
			if (th.spaceId && !th.archivedAt && !isCoordinatorThread(th))
				m.set(th.spaceId, (m.get(th.spaceId) ?? 0) + 1);
		return m;
	}, [threads]);
	return (
		<>
			<Group
				title={t("sidebar.spaces")}
				icon={<GroupIcon name="target" />}
				action={
					<IconButton
						icon="plus"
						box="xs"
						size={13}
						label={t("space.new")}
						onClick={() => setCreating(true)}
					/>
				}
			>
				{list.map((s) => (
					<SpaceRow
						key={s.id}
						space={s}
						chat={threads[s.threadId]}
						count={counts.get(s.id) ?? 0}
					/>
				))}
			</Group>
			{creating && <SpaceDialog onClose={() => setCreating(false)} />}
		</>
	);
}

function SpaceRow({
	space,
	chat,
	count,
}: {
	space: Space;
	chat: Thread | undefined;
	count: number;
}) {
	return (
		<NavLink
			to={`/space/${space.id}`}
			onClick={closeOnCompact}
			className={NAV_ROW}
		>
			<ThreadStateIcon state={chat?.state ?? "idle"} size={14} />
			<span
				className={`min-w-0 flex-1 truncate ${chat?.unread ? "font-medium" : ""}`}
			>
				{space.name}
			</span>
			{chat?.unread && <UnreadDot />}
			<CountPill n={count} />
		</NavLink>
	);
}

/** 项目过滤签（搜索框下）：项目徽标 + 名称 + 关闭。 */
function ProjectFilterChip({
	filter,
	projects,
}: {
	filter: string | null;
	projects: readonly Project[];
}) {
	const { t } = useTranslation();
	const p = filter ? projects.find((x) => x.id === filter) : undefined;
	const name = filter === null ? t("sidebar.noProject") : (p?.name ?? filter);
	return (
		<div className="shrink-0 px-2.5 pb-2">
			<span
				data-testid="project-filter"
				className="inline-flex h-7 max-w-full items-center gap-1.5 rounded-full border border-border bg-[var(--sb-field)] pr-1 pl-2 text-fg text-xs"
			>
				{filter === null ? (
					<GroupIcon name="folder" />
				) : (
					<ProjectBadge name={name} icon={p?.icon} size={16} />
				)}
				<span className="min-w-0 truncate">
					{t("sidebar.projectFilter", { name })}
				</span>
				<button
					type="button"
					aria-label={t("sidebar.clearProjectFilter")}
					title={t("sidebar.clearProjectFilter")}
					onClick={() => uiStore.getState().setProjectFilter(undefined)}
					className="grid h-5 w-5 shrink-0 place-items-center rounded-full text-faint hover:bg-[var(--sb-hover)] hover:text-fg"
				>
					<Icon name="close" size={11} />
				</button>
			</span>
		</div>
	);
}

/**
 * `active`：本分区此刻显示着。搜索框的聚焦请求（`searchNonce`）只在 active 时处理、每个 nonce 只处理一次：
 * 分区刚切过来时 pane 还是 inert，此时聚焦会落空，等它变成 active 再聚焦。
 */
export function ThreadsPane({ active }: { active: boolean }): ReactNode {
	const { t } = useTranslation();
	const threads = useIndex((s) => s.threads);
	const projects = useProjects();
	const searchNonce = useUi((s) => s.searchNonce);
	const projectFilter = useUi((s) => s.projectFilter);
	const [q, setQ] = useState("");
	const searchRef = useRef<HTMLInputElement>(null);
	const focusedNonce = useRef(0);
	const now = useNow();
	useEffect(() => {
		if (!active || searchNonce === focusedNonce.current) return;
		focusedNonce.current = searchNonce;
		searchRef.current?.focus();
	}, [searchNonce, active]);
	// 项目变更没有实时推送：服务端会把带卡片的自动标题图标写进尚无图标的项目，
	// 侧栏发现这样的线程（签名变化）时让项目列表失效一次，拉回新图标
	const qc = useQueryClient();
	const iconlessSig = useMemo(() => {
		const iconless = new Set(
			projects.data?.projects.filter((p) => !p.icon).map((p) => p.id),
		);
		return Object.values(threads)
			.filter(
				(th) =>
					th.projectId &&
					iconless.has(th.projectId) &&
					parseTitleCard(th.title).icon,
			)
			.map((th) => `${th.id}\u0000${th.title}`)
			.sort()
			.join("\u0001");
	}, [threads, projects.data]);
	useEffect(() => {
		if (iconlessSig) void qc.invalidateQueries({ queryKey: qk.projects });
	}, [iconlessSig, qc]);
	const groups = useMemo(
		() =>
			groupThreads(
				filterByProject(Object.values(threads), projectFilter),
				q,
				now,
			),
		[threads, projectFilter, q, now],
	);
	const hasStacks = groups.byProject.some((g) => g.stacks.length > 0);
	const stackSchedules = useSchedules({ mode: "new_thread" }, hasStacks);
	const stackNames = useMemo(
		() =>
			new Map(
				(stackSchedules.data?.schedules ?? []).map((s) => [s.id, s.name]),
			),
		[stackSchedules.data],
	);
	const projectHead = (id: string | null) => {
		if (!id)
			return {
				name: t("sidebar.noProject"),
				icon: <GroupIcon name="folder" />,
			};
		const p = projects.data?.projects.find((x) => x.id === id);
		const name = p?.name ?? id;
		return { name, icon: <ProjectBadge name={name} icon={p?.icon} /> };
	};
	const rows = (list: Thread[]) =>
		list.map((th) => (
			<ThreadRow key={th.id} thread={th} childMap={groups.children} now={now} />
		));
	const empty = !Object.values(threads).some(
		(th) => !isCoordinatorThread(th) && !th.archivedAt,
	);

	return (
		<>
			<div className="shrink-0 px-2.5 pb-2">
				<div className="flex h-8 items-center gap-2 rounded-lg border border-transparent bg-[var(--sb-field)] pr-1 pl-2.5 text-faint transition-colors duration-150 focus-within:border-accent/60">
					<Icon name="search" size={14} aria-hidden className="shrink-0" />
					<input
						ref={searchRef}
						value={q}
						onChange={(e) => setQ(e.target.value)}
						placeholder={t("sidebar.search")}
						aria-label={t("sidebar.search")}
						className="h-full min-w-0 flex-1 bg-transparent text-fg text-sm outline-none placeholder:text-faint focus-visible:outline-none"
					/>
					{q ? (
						<button
							type="button"
							aria-label={t("common.clear")}
							onClick={() => setQ("")}
							className="grid h-6 w-6 shrink-0 place-items-center rounded-sm hover:bg-[var(--sb-hover)] hover:text-fg"
						>
							<Icon name="close" size={12} />
						</button>
					) : (
						<span className="hidden pr-0.5 lg:inline-flex">
							<Kbd>Ctrl K</Kbd>
						</span>
					)}
				</div>
			</div>
			{projectFilter !== undefined && (
				<ProjectFilterChip
					filter={projectFilter}
					projects={projects.data?.projects ?? []}
				/>
			)}
			<div data-testid="threads-scroll" className={PANE_SCROLL}>
				<SpacesGroup threads={threads} />
				{empty && (
					<p className="px-3 py-4 text-faint text-sm">
						{t("sidebar.noThreads")}
					</p>
				)}
				{groups.pinned.length > 0 && (
					<Group title={t("sidebar.pinned")} icon={<GroupIcon name="pin" />}>
						{rows(groups.pinned)}
					</Group>
				)}
				{groups.byProject.map((g) => {
					const head = projectHead(g.projectId);
					return (
						<Group
							key={g.projectId ?? "_none"}
							title={head.name}
							icon={head.icon}
							count={g.threads.length + g.stacks.length}
						>
							{g.stacks.map((s) => (
								<ScheduleStack
									key={s.scheduleId}
									stack={s}
									name={stackNames.get(s.scheduleId)}
									childMap={groups.children}
									now={now}
								/>
							))}
							{rows(g.threads)}
						</Group>
					);
				})}
				<div className="mt-3">
					{groups.inactive.length > 0 && (
						<Group
							fold
							title={t("sidebar.inactive")}
							icon={<GroupIcon name="hourglass" />}
							defaultOpen={false}
							count={groups.inactive.length}
							action={
								<button
									type="button"
									className="shrink-0 rounded-sm px-1.5 py-0.5 text-2xs text-faint hover:bg-[var(--sb-hover)] hover:text-fg"
									onClick={() => {
										if (
											!window.confirm(
												t("sidebar.archiveAllConfirm", {
													count: groups.inactive.length,
												}),
											)
										)
											return;
										for (const th of groups.inactive)
											void patchThread(th.id, { archived: true });
									}}
								>
									{t("sidebar.archiveAll")}
								</button>
							}
						>
							{rows(groups.inactive)}
						</Group>
					)}
					{groups.snoozed.length > 0 && (
						<Group
							fold
							title={t("sidebar.snoozed")}
							icon={<GroupIcon name="bell" />}
							defaultOpen={false}
							count={groups.snoozed.length}
						>
							{groups.snoozed.map((th) => (
								<ThreadRow
									key={th.id}
									thread={th}
									now={now}
									time={formatRelative(th.snoozedUntil ?? now, now)}
								/>
							))}
						</Group>
					)}
					<ArchivedGroup now={now} />
				</div>
			</div>
		</>
	);
}
