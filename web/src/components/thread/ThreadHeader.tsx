/** 线程页头（玻璃）：项目徽标与名称、状态、标题卡（图标 + 代码 + 可编辑标题）、orb 胶囊、会话树 / 面板 / More Actions。 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import type { Orb } from "../../api/client";
import { useProjects } from "../../api/queries";
import { parseTitleCard, withTitleCard } from "../../lib/title";
import {
	deleteOrb,
	deleteThread,
	patchThread,
	pauseOrb,
	renameThread,
	wakeThread,
} from "../../state/actions";
import { useIndex, useThread } from "../../state/stores";
import { uiStore, useUi } from "../../state/ui";
import { Badge, ProjectBadge, TitleGlyph } from "../ui/data";
import { Icon } from "../ui/icons";
import { OrbBead } from "../ui/OrbBead";
import {
	IconButton,
	Menu,
	type MenuItem,
	ThreadStateIcon,
} from "../ui/primitives";

export const THREAD_COLORS = [
	"red",
	"orange",
	"yellow",
	"green",
	"teal",
	"blue",
	"purple",
	"pink",
] as const;

export const COLOR_CLASS: Record<string, string> = {
	red: "bg-red-500",
	orange: "bg-orange-500",
	yellow: "bg-yellow-500",
	green: "bg-green-500",
	teal: "bg-teal-500",
	blue: "bg-blue-500",
	purple: "bg-purple-500",
	pink: "bg-pink-500",
};

function snoozeAt(kind: "1h" | "3h" | "tomorrow" | "week"): number {
	const now = new Date();
	if (kind === "1h") return now.getTime() + 3600_000;
	if (kind === "3h") return now.getTime() + 3 * 3600_000;
	const d = new Date(now);
	d.setHours(9, 0, 0, 0);
	d.setDate(d.getDate() + (kind === "tomorrow" ? 1 : 7));
	return d.getTime();
}

export function useOrbOf(threadId: string): Orb | undefined {
	const fromThread = useThread(threadId, (s) => s.orb);
	const orbId = useThread(threadId, (s) => s.thread?.orbId);
	const fromIndex = useIndex((s) => (orbId ? s.orbs[orbId] : undefined));
	if (fromIndex && fromThread)
		return fromIndex.stateChangedAt >= fromThread.stateChangedAt
			? fromIndex
			: fromThread;
	return fromIndex ?? fromThread;
}

/** orb 状态胶囊：状态珠 + 状态文字（compact 只留状态珠）；点击打开信息面板。 */
function OrbChip({ orb, working }: { orb: Orb; working: boolean }): ReactNode {
	const { t } = useTranslation();
	const state = t(`orbState.${orb.state}`);
	return (
		<button
			type="button"
			aria-label={t("thread.orbState", { state })}
			title={orb.id}
			className="inline-flex h-[26px] shrink-0 items-center gap-1.5 rounded-full border border-border bg-panel/60 px-2 font-medium text-fg text-xs transition-colors duration-150 hover:bg-hover sm:pr-2.5"
			onClick={() => {
				const ui = uiStore.getState();
				ui.setPanelTab("info");
				ui.setPanel(true);
			}}
		>
			<OrbBead state={orb.state} active={working} size={12} label={state} />
			<span aria-hidden className="hidden sm:inline">
				{state}
			</span>
		</button>
	);
}

export function ThreadHeader({
	threadId,
	sidebarToggle,
}: {
	threadId: string;
	/** 非 expanded 宽度时在标题栏左侧显示侧边栏开关 */
	sidebarToggle: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const thread = useThread(threadId, (s) => s.thread);
	const panelOpen = useUi((s) => s.panelOpen);
	const orb = useOrbOf(threadId);
	const projects = useProjects();
	const projectId = thread?.projectId ?? orb?.projectId;
	const project = projectId
		? projects.data?.projects.find((p) => p.id === projectId)
		: undefined;
	const card = parseTitleCard(thread?.title ?? "");
	const [editing, setEditing] = useState<string | null>(null);
	const [error, setError] = useState<string | null>(null);
	const act = (fn: () => Promise<unknown>) => () => {
		setError(null);
		fn().catch((e: unknown) =>
			setError(e instanceof Error ? e.message : String(e)),
		);
	};
	const items: MenuItem[] = [
		{
			key: "pin",
			icon: "pin",
			label: thread?.pinned ? t("thread.unpin") : t("thread.pin"),
			onSelect: act(() => patchThread(threadId, { pinned: !thread?.pinned })),
		},
		{
			key: "snooze",
			icon: "clock",
			label: t("thread.snooze"),
			children: [
				...(["1h", "3h", "tomorrow", "week"] as const).map((k) => ({
					key: k,
					label: t(`thread.snoozeFor.${k}`),
					onSelect: act(() =>
						patchThread(threadId, { snoozedUntil: snoozeAt(k) }),
					),
				})),
				{
					key: "clear",
					label: t("thread.snoozeClear"),
					onSelect: act(() => patchThread(threadId, { snoozedUntil: 0 })),
				},
			],
		},
		{
			key: "archive",
			icon: "archive",
			label: thread?.archivedAt ? t("thread.unarchive") : t("thread.archive"),
			onSelect: act(() =>
				patchThread(threadId, { archived: !thread?.archivedAt }),
			),
		},
		{
			key: "rename",
			icon: "edit",
			label: t("thread.rename"),
			onSelect: () => uiStore.getState().open({ kind: "rename" }),
		},
		{
			key: "labels",
			icon: "list",
			label: t("thread.labels"),
			onSelect: () => uiStore.getState().open({ kind: "labels" }),
		},
		{
			key: "color",
			icon: "target",
			label: t("thread.color"),
			children: [
				...THREAD_COLORS.map((c) => ({
					key: c,
					label: (
						<span className="inline-flex items-center gap-2">
							<span className={`h-2.5 w-2.5 rounded-full ${COLOR_CLASS[c]}`} />
							{t(`color.${c}`)}
						</span>
					),
					onSelect: act(() => patchThread(threadId, { color: c })),
				})),
				{
					key: "none",
					label: t("color.none"),
					onSelect: act(() => patchThread(threadId, { color: "" })),
				},
			],
		},
		{
			key: "link",
			icon: "copy",
			label: t("thread.copyLink"),
			onSelect: () =>
				void navigator.clipboard?.writeText(`${location.origin}/t/${threadId}`),
		},
		{
			key: "fork",
			icon: "fork",
			label: t("thread.fork"),
			onSelect: () => uiStore.getState().open({ kind: "fork" }),
		},
		{
			key: "attachments",
			icon: "file",
			label: t("attachments.title"),
			onSelect: () => uiStore.getState().open({ kind: "attachments" }),
		},
		{
			key: "orb",
			icon: "box",
			label: t("thread.orb"),
			disabled: !thread?.orbId,
			children: [
				{
					key: "state",
					label: t("thread.orbState", {
						state: orb ? t(`orbState.${orb.state}`) : "—",
					}),
					disabled: true,
				},
				{
					key: "freeze",
					icon: "pause",
					label: t("thread.orbFreeze"),
					disabled: orb?.state !== "ready",
					onSelect: act(() => pauseOrb(thread?.orbId ?? "", "freeze")),
				},
				{
					key: "wake",
					icon: "play",
					label: t("thread.orbWake"),
					onSelect: act(() => wakeThread(threadId)),
				},
				{
					key: "delete",
					icon: "trash",
					danger: true,
					label: t("thread.orbDelete"),
					onSelect: () => {
						if (window.confirm(t("thread.orbDeleteConfirm")))
							act(() => deleteOrb(thread?.orbId ?? ""))();
					},
				},
			],
		},
		{
			key: "debug",
			icon: "bug",
			label: t("thread.debug"),
			onSelect: () => uiStore.getState().open({ kind: "debug" }),
		},
		{
			key: "delete",
			icon: "trash",
			danger: true,
			label: t("thread.delete"),
			onSelect: () => {
				if (!window.confirm(t("thread.deleteConfirm"))) return;
				act(async () => {
					await deleteThread(threadId);
					navigate("/");
				})();
			},
		},
	];

	return (
		<header className="glass flex h-12 shrink-0 items-center gap-1.5 rounded-none border-0 border-b px-2 shadow-none sm:px-3">
			{sidebarToggle && (
				<IconButton
					icon="menu"
					label={t("nav.sidebar")}
					onClick={() =>
						uiStore.getState().setSidebar(!uiStore.getState().sidebarOpen)
					}
				/>
			)}
			{project && (
				<span className="hidden min-w-0 shrink items-center gap-2 text-sm sm:inline-flex">
					<ProjectBadge name={project.name} icon={project.icon} />
					<span className="max-w-40 truncate text-muted">{project.name}</span>
					<span aria-hidden className="text-ghost">
						/
					</span>
				</span>
			)}
			{thread && <ThreadStateIcon state={thread.state} />}
			{thread?.color && (
				<span
					className={`h-1.5 w-1.5 shrink-0 rounded-full ${COLOR_CLASS[thread.color] ?? ""}`}
				/>
			)}
			{editing !== null ? (
				<form
					className="min-w-0 flex-1"
					onSubmit={(e) => {
						e.preventDefault();
						const text = editing.trim();
						setEditing(null);
						if (!text || !thread) return;
						// 原标题带 omp 标题卡时保留 `<图标> <代码>: ` 前缀
						const title = withTitleCard(thread.title, text);
						if (title !== thread.title)
							act(() => renameThread(threadId, title))();
					}}
				>
					<input
						ref={(el) => el?.focus()}
						aria-label={t("thread.rename")}
						className="w-full rounded-md bg-inset px-2 py-1 font-medium text-sm"
						value={editing}
						onChange={(e) => setEditing(e.target.value)}
						onBlur={(e) => e.currentTarget.form?.requestSubmit()}
					/>
				</form>
			) : (
				<button
					type="button"
					className="flex min-w-0 shrink items-center gap-1.5 rounded-md px-1 py-1 text-left font-semibold text-sm tracking-tight transition-colors duration-150 hover:bg-hover"
					title={t("thread.clickToRename")}
					onClick={() => setEditing(card.text)}
				>
					{card.icon && (
						<TitleGlyph icon={card.icon} seed={card.text} size={18} />
					)}
					{card.code && (
						<span aria-hidden className="hidden sm:inline-flex">
							<Badge>{card.code}</Badge>
						</span>
					)}
					<span className="min-w-0 truncate">
						{card.text || t("thread.untitled")}
					</span>
				</button>
			)}
			{editing === null && orb && (
				<OrbChip orb={orb} working={thread?.state === "working"} />
			)}
			<span className="min-w-0 flex-1" />
			{error && (
				<span className="max-w-40 truncate text-err text-xs" title={error}>
					{error}
				</span>
			)}
			<IconButton
				icon="tree"
				label={t("thread.tree")}
				onClick={() => uiStore.getState().open({ kind: "tree" })}
			/>
			<IconButton
				icon="panel"
				label={t("nav.infoPanel")}
				active={panelOpen}
				onClick={() => uiStore.getState().setPanel(!panelOpen)}
			/>
			<Menu
				items={items}
				trigger={(p) => (
					<button
						type="button"
						aria-label={t("thread.moreActions")}
						className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted transition-colors duration-150 hover:bg-hover hover:text-fg"
						{...p}
					>
						<Icon name="more" size={16} />
					</button>
				)}
			/>
		</header>
	);
}
