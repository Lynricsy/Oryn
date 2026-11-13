/** 线程页头（渐进模糊纱）：项目徽标与名称、标题卡（图标 + 可编辑标题 + 代码）、orb 胶囊、会话树 / 面板 / More Actions。 */
import { type CSSProperties, type ReactNode, useState } from "react";
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
import { Badge, nameHue, ProjectBadge, StatusDot } from "../ui/data";
import { Icon } from "../ui/icons";
import { OrbBead } from "../ui/OrbBead";
import { IconButton, Menu, type MenuItem } from "../ui/primitives";

/** 页头背后渐进模糊纱的高度（px）：48px 页头 + 16px 渐隐，模糊在页头下沿之后 16px 内减到 0；转录顶部留白取同一个值。 */
export const HEADER_VEIL = 64;

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

/** 线程色 → 色相；配色在 index.css 的 `.thread-mark`（竖条）与 `.avatar`（标题图标底色）。 */
export const THREAD_HUE: Record<string, number> = {
	red: 25,
	orange: 55,
	yellow: 90,
	green: 150,
	teal: 190,
	blue: 255,
	purple: 300,
	pink: 350,
};

/** 线程色的色相；未设置或未知颜色时返回 undefined。 */
export function threadHue(color?: string | null): number | undefined {
	return color ? THREAD_HUE[color] : undefined;
}

/** 带 `--h` 的内联样式（`.thread-mark` / `.avatar` 读取）。 */
export function hueVar(hue: number): CSSProperties {
	return { "--h": hue } as CSSProperties;
}

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

/**
 * orb 状态胶囊：状态珠 + 状态文字（页头窄时只留状态珠）；点击打开信息面板。
 * orb 本身就绪、但最近一次 setup / resume 失败时，胶囊改报环境失败（错误色圆点 + 文案），与托盘里的提示条一致；
 * 其它状态（冻结、停机、唤醒、恢复、失败等）照实呈现生命周期，历史失败不覆盖。
 */
function OrbChip({ orb, working }: { orb: Orb; working: boolean }): ReactNode {
	const { t } = useTranslation();
	const failedRun =
		orb.state !== "ready"
			? undefined
			: orb.lastSetup && !orb.lastSetup.ok
				? orb.lastSetup
				: orb.lastResume && !orb.lastResume.ok
					? orb.lastResume
					: undefined;
	const state = failedRun
		? t(failedRun.phase === "resume" ? "env.resumeFailed" : "env.setupFailed")
		: t(`orbState.${orb.state}`);
	return (
		<button
			type="button"
			aria-label={t("thread.orbState", { state })}
			title={orb.id}
			className={`ml-1.5 inline-flex h-[26px] shrink-0 items-center gap-[7px] rounded-full border px-2 font-medium text-xs transition-colors duration-150 @lg/header:pr-2.5 @lg/header:pl-[9px] ${failedRun ? "border-err/30 bg-err/5 text-err hover:bg-err/10" : "border-border bg-panel/60 text-fg hover:bg-hover"}`}
			onClick={() => {
				const ui = uiStore.getState();
				ui.setPanelTab("info");
				ui.setPanel(true);
			}}
		>
			{failedRun ? (
				<span aria-hidden className="inline-grid h-3 w-3 place-items-center">
					<StatusDot tone="err" />
				</span>
			) : (
				<OrbBead state={orb.state} active={working} size={12} label={state} />
			)}
			<span aria-hidden className="hidden @lg/header:inline">
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
	/** 非 expanded 宽度时在标题栏左侧显示侧边栏开关；expanded 下侧栏收起时也显示 */
	sidebarToggle: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const thread = useThread(threadId, (s) => s.thread);
	const panelOpen = useUi((s) => s.panelOpen);
	const sidebarCollapsed = useUi((s) => s.sidebarCollapsed);
	const orb = useOrbOf(threadId);
	const projects = useProjects();
	// 只认线程自己的项目，和侧栏分组一致；「无项目」线程不显示项目段（即使 orb 属于某个项目）
	const project = thread?.projectId
		? projects.data?.projects.find((p) => p.id === thread.projectId)
		: undefined;
	const card = parseTitleCard(thread?.title ?? "");
	const hue = threadHue(thread?.color);
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
							<span
								className="thread-mark h-2.5 w-2.5 rounded-full"
								style={hueVar(THREAD_HUE[c] ?? 0)}
							/>
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
			key: "share",
			icon: "share",
			label: t("share.header.action"),
			onSelect: () => uiStore.getState().open({ kind: "share" }),
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
		<header className="veil-host relative isolate @container/header flex h-12 shrink-0 items-center gap-1.5 px-2 sm:px-3">
			<div aria-hidden className="veil" style={{ height: HEADER_VEIL }}>
				<i />
				<i />
				<i />
				<i />
				<i />
				<i />
			</div>
			{(sidebarToggle || sidebarCollapsed) && (
				<IconButton
					icon={sidebarToggle ? "menu" : "sidebar"}
					label={t("nav.sidebar")}
					onClick={() => uiStore.getState().toggleSidebar()}
				/>
			)}
			{project && (
				// 项目段整段显示或整段隐藏：页头够宽（D 稿）带项目名，中等宽度只留徽标，再窄连徽标一起隐藏
				<span
					className="hidden shrink-0 items-center gap-2 text-sm @md/header:inline-flex"
					title={project.name}
				>
					<ProjectBadge name={project.name} icon={project.icon} />
					<span className="hidden max-w-40 truncate text-muted @lg/header:inline">
						{project.name}
					</span>
					<span aria-hidden className="text-ghost">
						/
					</span>
				</span>
			)}
			{/* 面包屑只有 项目 / 标题卡 / 标题；没有标题图标时线程色是一根短竖条 */}
			{!card.icon && hue !== undefined && editing === null && (
				<span
					aria-hidden
					className="thread-mark h-3.5 w-[3px] shrink-0 rounded-full"
					style={hueVar(hue)}
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
					{/* 标题前的图标：有标题卡图标用它；没有时窄页头（项目段已隐藏）退到项目徽标，都没有就留空 */}
					{card.icon ? (
						<span
							aria-hidden
							data-title-glyph="card"
							className="avatar inline-flex h-[18px] w-[18px] shrink-0 items-center justify-center rounded-xs text-xs leading-none"
							style={hueVar(hue ?? nameHue(card.text))}
						>
							{card.icon}
						</span>
					) : (
						project && (
							<span
								aria-hidden
								data-title-glyph="project"
								className="inline-flex shrink-0 @md/header:hidden"
							>
								<ProjectBadge name={project.name} icon={project.icon} />
							</span>
						)
					)}
					<span className="min-w-0 truncate">
						{card.text || t("thread.untitled")}
					</span>
					{card.code && (
						<span
							aria-hidden
							className="hidden shrink-0 font-normal @2xl/header:inline-flex"
						>
							<Badge>{card.code}</Badge>
						</span>
					)}
				</button>
			)}
			{editing === null && (thread?.publicShares ?? 0) > 0 && (
				// 有未撤销的公开链接：中性徽标，点开分享对话框管理
				<button
					type="button"
					className="inline-flex shrink-0 rounded-sm"
					title={t("share.header.badgeTitle", { n: thread?.publicShares ?? 0 })}
					onClick={() => uiStore.getState().open({ kind: "share" })}
				>
					<Badge icon="globe">{t("share.header.badge")}</Badge>
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
