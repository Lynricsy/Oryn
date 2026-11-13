/**
 * 侧边栏（按最新 Amp 布局）：品牌 + 新建、搜索；分区 已置顶 → 按项目分组的活跃线程 → 72 小时未活动（一键归档）→ 稍后提醒 → 已归档。
 * 线程行 = 状态图标 + 标题卡图标 + 标题 + 相对时间 / 子线程计数 / 未读点（标题前最多两个标记）；线程色是行左缘的一根细竖条，
 * 有标题卡图标时图标底色也取线程色。fork 出的子线程挂在父线程下，带 L 形引导线。
 * 链接的 textContent 必须只等于标题文字：图标字形、时间、计数、活动行一律走 data 属性 + 伪元素。
 */

import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { NavLink, useNavigate } from "react-router";
import type { Thread } from "../../api/client";
import { qk, useArchivedThreads, useProjects } from "../../api/queries";
import { formatRelative } from "../../lib/format";
import { parseTitleCard } from "../../lib/title";
import { breakpointFor } from "../../lib/viewport";
import { patchThread } from "../../state/actions";
import { useConnection, useIndex } from "../../state/stores";
import { type ThemeMode, themeStore, useTheme } from "../../state/theme";
import { uiStore, useUi } from "../../state/ui";
import { hueVar, threadHue } from "../thread/ThreadHeader";
import {
	BrandMark,
	nameHue,
	ProjectBadge,
	StatusDot,
	type Tone,
} from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { Button, IconButton, Kbd, ThreadStateIcon } from "../ui/primitives";

const INACTIVE_MS = 72 * 3600_000;

export interface SidebarGroups {
	pinned: Thread[];
	byProject: { projectId: string | null; threads: Thread[] }[];
	inactive: Thread[];
	snoozed: Thread[];
	children: Map<string, Thread[]>;
}

function activityOf(t: Thread): number {
	return t.lastMessageAt ?? t.updatedAt;
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
	const visible = threads.filter((t) => !t.archivedAt && match(t));
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
	for (const t of roots) {
		if (t.pinned) g.pinned.push(t);
		else if (t.snoozedUntil && t.snoozedUntil > now) g.snoozed.push(t);
		else if (
			now - activityOf(t) > INACTIVE_MS &&
			t.state !== "working" &&
			t.state !== "awaiting_input"
		)
			g.inactive.push(t);
		else {
			const key = t.projectId ?? null;
			const list = projects.get(key) ?? [];
			list.push(t);
			projects.set(key, list);
		}
	}
	g.byProject = [...projects.entries()].map(([projectId, list]) => ({
		projectId,
		threads: list,
	}));
	return g;
}

/** 标题卡图标：外观同 `TitleGlyph`，但字形放在伪元素里，不进链接的 textContent。 */
function CardGlyph({ icon, hue }: { icon: string; hue: number }) {
	return (
		<span
			aria-hidden
			data-glyph={icon}
			className="avatar inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-[4px] text-[11px] leading-none before:content-[attr(data-glyph)]"
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
	// 抽屉（compact）在跳转后收起；medium 的推入式侧边栏保持用户的开关选择
	const onNavigate = () => {
		if (breakpointFor(window.innerWidth) === "compact")
			uiStore.getState().setSidebar(false);
	};
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
				onClick={onNavigate}
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

function CountPill({ n }: { n: number }) {
	return (
		<span className="inline-grid h-[18px] min-w-5 shrink-0 place-items-center rounded-sm border border-fg/12 px-1.5 font-semibold text-2xs text-muted tabular-nums leading-none">
			{n}
		</span>
	);
}

function GroupIcon({ name }: { name: IconName }) {
	return (
		<Icon name={name} size={14} aria-hidden className="shrink-0 text-faint" />
	);
}

/**
 * 分组：标题行可折叠。主分组（置顶、项目）的箭头悬停才显现；
 * `fold` 分组（未活动、稍后提醒、已归档）箭头常驻在最左、默认收起。
 */
function Group({
	title,
	icon,
	children,
	defaultOpen = true,
	open: openProp,
	onOpenChange,
	action,
	count,
	fold,
}: {
	title: string;
	icon: ReactNode;
	children: ReactNode;
	defaultOpen?: boolean;
	/** 受控展开（已归档按展开与否决定是否拉取） */
	open?: boolean;
	onOpenChange?: (open: boolean) => void;
	action?: ReactNode;
	count?: number;
	fold?: boolean;
}) {
	const [inner, setInner] = useState(defaultOpen);
	const open = openProp ?? inner;
	const toggle = () => {
		setInner(!open);
		onOpenChange?.(!open);
	};
	const caret = open ? "chevronDown" : "chevronRight";
	return (
		<div className={fold ? "" : "mb-2"}>
			<div
				className={`group/head flex items-center gap-1 rounded-lg pr-1 ${fold ? "transition-colors duration-150 hover:bg-[var(--sb-hover)]" : ""}`}
			>
				<button
					type="button"
					className={`flex min-w-0 flex-1 items-center gap-2 pl-2.5 text-left font-semibold text-muted text-xs hover:text-fg ${fold ? "h-8" : "h-[30px]"}`}
					onClick={toggle}
					aria-expanded={open}
				>
					{fold && (
						<Icon
							name={caret}
							size={11}
							aria-hidden
							className="-mx-0.5 shrink-0 text-ghost"
						/>
					)}
					{icon}
					<span className="min-w-0 flex-1 truncate">{title}</span>
					{count !== undefined && <CountPill n={count} />}
					{!fold && (
						<Icon
							name={caret}
							size={11}
							aria-hidden
							className={`shrink-0 text-ghost transition-opacity duration-150 ${open ? "opacity-0 group-focus-within/head:opacity-100 group-hover/head:opacity-100" : ""}`}
						/>
					)}
				</button>
				{action}
			</div>
			{open && <div className="flex flex-col pb-1">{children}</div>}
		</div>
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

const MODE_ICON = {
	system: "monitor",
	light: "sun",
	dark: "moon",
} as const satisfies Record<ThemeMode, IconName>;

export function Sidebar(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const threads = useIndex((s) => s.threads);
	const projects = useProjects();
	const conn = useConnection((s) => s.status);
	const mode = useTheme((s) => s.mode);
	const searchNonce = useUi((s) => s.searchNonce);
	const [q, setQ] = useState("");
	const searchRef = useRef<HTMLInputElement>(null);
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		const timer = setInterval(() => setNow(Date.now()), 60_000);
		return () => clearInterval(timer);
	}, []);
	useEffect(() => {
		if (searchNonce > 0) searchRef.current?.focus();
	}, [searchNonce]);
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
		() => groupThreads(Object.values(threads), q, now),
		[threads, q, now],
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
	const empty = Object.keys(threads).length === 0;
	const tone: Tone =
		conn === "open" ? "ok" : conn === "unauthorized" ? "err" : "warn";

	return (
		<nav
			className="flex h-full w-full flex-col pt-safe pl-safe"
			aria-label={t("nav.sidebar")}
		>
			<div className="flex h-[52px] shrink-0 items-center gap-2 pr-2.5 pl-4">
				<BrandMark size={18} />
				<span className="min-w-0 flex-1 truncate font-semibold text-[15px] tracking-tight">
					{t("app.name")}
				</span>
				<Button
					variant="primary"
					size="sm"
					className="w-7 px-0"
					aria-label={t("sidebar.newThread")}
					title={t("sidebar.newThread")}
					onClick={() => {
						uiStore.getState().setSidebar(false);
						navigate("/");
					}}
				>
					<Icon name="plus" size={15} weight="bold" />
				</Button>
			</div>
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
			<div className="min-h-0 flex-1 overflow-y-auto px-2 pt-1 pb-4">
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
							count={g.threads.length}
						>
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
			<div className="flex min-h-[46px] shrink-0 items-center gap-1 pr-2 pb-safe pl-4">
				<span className="flex min-w-0 flex-1 items-center gap-2 text-muted text-xs">
					<StatusDot tone={tone} pulse={tone === "warn"} />
					<span className="truncate">{t(`connection.${conn}`)}</span>
				</span>
				<span className="hidden lg:inline-flex">
					<Kbd>/</Kbd>
				</span>
				<IconButton
					icon={MODE_ICON[mode]}
					size={15}
					label={t("sidebar.theme")}
					title={t(`appearance.modes.${mode}`)}
					onClick={() => themeStore.getState().cycleMode()}
				/>
				<NavLink
					to="/settings/devices"
					onClick={() => uiStore.getState().setSidebar(false)}
					className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted transition-colors duration-150 hover:bg-[var(--sb-hover)] hover:text-fg"
					aria-label={t("nav.settings")}
					title={t("nav.settings")}
				>
					<Icon name="settings" size={15} />
				</NavLink>
			</div>
		</nav>
	);
}
