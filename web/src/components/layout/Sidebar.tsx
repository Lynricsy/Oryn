/**
 * 侧边栏（按最新 Amp 布局）：搜索、新建；分区 Pinned → 按项目分组的活跃线程 → Inactive 72h（一键归档）→ Snoozed → Archived。
 * 线程行 = 颜色标记 + 状态图标 + 标题 + 未读点；fork 出的子线程缩进在父线程下，父线程显示 `n/m`。
 */
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { NavLink, useNavigate } from "react-router";
import type { Thread } from "../../api/client";
import { useArchivedThreads, useProjects } from "../../api/queries";
import { formatRelative } from "../../lib/format";
import { breakpointFor } from "../../lib/viewport";
import { patchThread } from "../../state/actions";
import { useConnection, useIndex } from "../../state/stores";
import { uiStore, useUi } from "../../state/ui";
import { COLOR_CLASS } from "../thread/ThreadHeader";
import { Icon } from "../ui/icons";
import { IconButton, Kbd, ThreadStateIcon } from "../ui/primitives";

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

function ThreadRow({
	thread,
	childThreads,
	depth = 0,
}: {
	thread: Thread;
	childThreads?: Thread[];
	depth?: number;
}) {
	const { t } = useTranslation();
	// 抽屉（compact）在跳转后收起；medium 的推入式侧边栏保持用户的开关选择
	const onNavigate = () => {
		if (breakpointFor(window.innerWidth) === "compact")
			uiStore.getState().setSidebar(false);
	};
	return (
		<>
			<NavLink
				to={`/t/${thread.id}`}
				onClick={onNavigate}
				className={({ isActive }) =>
					`group relative flex h-8 items-center gap-2 rounded-md pr-2 text-sm ${isActive ? "bg-hover text-fg" : "text-muted hover:bg-hover/60 hover:text-fg"}`
				}
				style={{ paddingLeft: `${0.5 + depth * 1}rem` }}
			>
				{thread.color && (
					<span
						className={`absolute top-1.5 bottom-1.5 left-0 w-0.5 rounded-full ${COLOR_CLASS[thread.color] ?? ""}`}
					/>
				)}
				<ThreadStateIcon state={thread.state} size={13} />
				<span
					className={`min-w-0 flex-1 truncate ${thread.unread ? "font-medium text-fg" : ""}`}
				>
					{thread.title || t("thread.untitled")}
				</span>
				{thread.childCount ? (
					<span className="font-mono text-[10px] text-faint">{`${thread.childDoneCount ?? 0}/${thread.childCount}`}</span>
				) : null}
				{thread.unread && (
					<span
						role="img"
						className="h-1.5 w-1.5 shrink-0 rounded-full bg-accent"
						aria-label={t("sidebar.unread")}
					/>
				)}
			</NavLink>
			{childThreads?.map((c) => (
				<ThreadRow key={c.id} thread={c} depth={depth + 1} />
			))}
		</>
	);
}

function Group({
	title,
	children,
	defaultOpen = true,
	action,
	count,
}: {
	title: string;
	children: ReactNode;
	defaultOpen?: boolean;
	action?: ReactNode;
	count?: number;
}) {
	const [open, setOpen] = useState(defaultOpen);
	return (
		<div className="mt-3">
			<div className="flex items-center gap-1 px-2">
				<button
					type="button"
					className="flex min-w-0 flex-1 items-center gap-1 text-left font-medium text-[11px] text-faint uppercase tracking-wide hover:text-muted"
					onClick={() => setOpen((v) => !v)}
					aria-expanded={open}
				>
					<Icon name={open ? "chevronDown" : "chevronRight"} size={11} />
					<span className="truncate">{title}</span>
					{count !== undefined && <span className="font-normal">{count}</span>}
				</button>
				{action}
			</div>
			{open && <div className="mt-1 flex flex-col">{children}</div>}
		</div>
	);
}

function ArchivedGroup() {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const archived = useArchivedThreads(open);
	return (
		<div className="mt-3">
			<button
				type="button"
				className="flex w-full items-center gap-1 px-2 text-left font-medium text-[11px] text-faint uppercase tracking-wide hover:text-muted"
				onClick={() => setOpen((v) => !v)}
				aria-expanded={open}
			>
				<Icon name={open ? "chevronDown" : "chevronRight"} size={11} />
				{t("sidebar.archived")}
			</button>
			{open && (
				<div className="mt-1 flex flex-col">
					{archived.data?.threads.map((th) => (
						<ThreadRow key={th.id} thread={th} />
					))}
					{archived.data?.threads.length === 0 && (
						<p className="px-3 py-1 text-faint text-xs">{t("sidebar.empty")}</p>
					)}
				</div>
			)}
		</div>
	);
}

export function Sidebar(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const threads = useIndex((s) => s.threads);
	const projects = useProjects();
	const conn = useConnection((s) => s.status);
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
	const groups = useMemo(
		() => groupThreads(Object.values(threads), q, now),
		[threads, q, now],
	);
	const projectName = (id: string | null) =>
		id
			? (projects.data?.projects.find((p) => p.id === id)?.name ?? id)
			: t("sidebar.noProject");
	const rows = (list: Thread[]) =>
		list.map((th) => (
			<ThreadRow
				key={th.id}
				thread={th}
				childThreads={groups.children.get(th.id)}
			/>
		));
	const empty = Object.keys(threads).length === 0;

	return (
		<nav
			className="flex h-full w-full flex-col bg-panel pt-safe pl-safe"
			aria-label={t("nav.sidebar")}
		>
			<div className="flex h-12 shrink-0 items-center gap-1 px-3">
				<span className="flex-1 font-semibold text-sm tracking-tight">
					{t("app.name")}
				</span>
				<IconButton
					icon="plus"
					label={t("sidebar.newThread")}
					onClick={() => {
						uiStore.getState().setSidebar(false);
						navigate("/");
					}}
				/>
			</div>
			<div className="px-3">
				<div className="flex items-center gap-2 rounded-md border border-border bg-inset px-2">
					<Icon name="search" size={13} className="text-faint" />
					<input
						ref={searchRef}
						value={q}
						onChange={(e) => setQ(e.target.value)}
						placeholder={t("sidebar.search")}
						aria-label={t("sidebar.search")}
						className="h-7 min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-faint"
					/>
					{q && (
						<button
							type="button"
							aria-label={t("common.clear")}
							onClick={() => setQ("")}
							className="text-faint"
						>
							<Icon name="close" size={12} />
						</button>
					)}
				</div>
			</div>
			<div className="min-h-0 flex-1 overflow-y-auto px-2 pb-4">
				{empty && (
					<p className="px-3 py-4 text-faint text-sm">
						{t("sidebar.noThreads")}
					</p>
				)}
				{groups.pinned.length > 0 && (
					<Group title={t("sidebar.pinned")}>{rows(groups.pinned)}</Group>
				)}
				{groups.byProject.map((g) => (
					<Group
						key={g.projectId ?? "_none"}
						title={projectName(g.projectId)}
						count={g.threads.length}
					>
						{rows(g.threads)}
					</Group>
				))}
				{groups.inactive.length > 0 && (
					<Group
						title={t("sidebar.inactive")}
						defaultOpen={false}
						count={groups.inactive.length}
						action={
							<button
								type="button"
								className="text-[11px] text-faint hover:text-fg"
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
						title={t("sidebar.snoozed")}
						defaultOpen={false}
						count={groups.snoozed.length}
					>
						{groups.snoozed.map((th) => (
							<div key={th.id} className="relative">
								<ThreadRow thread={th} />
								<span className="pointer-events-none absolute top-2 right-2 text-[10px] text-faint">
									{formatRelative(th.snoozedUntil ?? now, now)}
								</span>
							</div>
						))}
					</Group>
				)}
				<ArchivedGroup />
			</div>
			<div className="flex shrink-0 items-center gap-2 border-border border-t px-3 py-2 pb-safe">
				<span
					className={`h-2 w-2 rounded-full ${conn === "open" ? "bg-ok" : conn === "unauthorized" ? "bg-err" : "bg-warn"}`}
					title={t(`connection.${conn}`)}
				/>
				<span className="min-w-0 flex-1 truncate text-faint text-xs">
					{t(`connection.${conn}`)}
				</span>
				<span className="hidden text-faint text-xs lg:inline">
					<Kbd>/</Kbd>
				</span>
				<NavLink
					to="/settings/devices"
					onClick={() => uiStore.getState().setSidebar(false)}
					className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted hover:bg-hover hover:text-fg"
					aria-label={t("nav.settings")}
				>
					<Icon name="settings" size={16} />
				</NavLink>
			</div>
		</nav>
	);
}
