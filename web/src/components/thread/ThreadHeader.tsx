/**
 * 线程页头（渐进模糊纱）：宽页头 `ThreadHeader` 是面包屑 Space / 项目 / 标题卡（图标 + 可编辑标题 + 代码）、定时胶囊、orb 胶囊、会话树 / 面板 / More Actions；
 * 手机是 `CompactThreadHeader`（另一个文件），两者共用这里的 `useThreadActions`（全部线程操作与对话框）。
 * 桌面 App 无系统标题栏：整条页头是拖动区（按钮、标题等可交互元素除外）；`chromeLeft` / `chromeRight` 时两端放窗口控件。
 * 定时（M9）：线程内定时显示 `ScheduleChip`，「每次新建线程」定时建出的线程显示 `ScheduleRunChip`，More Actions 有「定时…」；
 * GitHub（M14）：线程关联的 PR 显示 `PrChip`（最多两枚），在定时胶囊之后、orb 胶囊之前；
 * M15：触发规则建立的 issue 任务或发布哨兵的状态显示 `GitHubTaskChip`（`thread.githubTask`），紧跟在 PR 胶囊之后；
 * 归档旁有「归档为已交付」（Space 进度图的 Shipped）。
 */
import type { TFunction } from "i18next";
import { type CSSProperties, type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router";
import type { Orb, Schemas } from "../../api/client";
import { useProjects } from "../../api/queries";
import { useSpaces } from "../../api/space-queries";
import { isDark, markHex } from "../../lib/native-header";
import { parseTitleCard, withTitleCard } from "../../lib/title";
import { useBreakpoint, useViewportSegments } from "../../lib/viewport";
import { deleteThread, patchThread, renameThread } from "../../state/actions";
import { useIndex, useThread } from "../../state/stores";
import { uiStore, useUi } from "../../state/ui";
import { PrChip } from "../github/PrChip";
import { ChromeLead, WindowControls } from "../layout/WindowControls";
import { ScheduleChip, ScheduleRunChip } from "../schedule/ScheduleChip";
import { ScheduleDialog } from "../schedule/ScheduleDialog";
import { Badge, nameHue, ProjectBadge, StatusDot } from "../ui/data";
import { Icon } from "../ui/icons";
import { OrbBead } from "../ui/OrbBead";
import { IconButton, Menu, type MenuItem } from "../ui/primitives";
import { useArchiveThread } from "./ArchiveDialog";

/** 页头背后渐进模糊纱的高度（px）：48px 页头 + 16px 渐隐，模糊在页头下沿之后 16px 内减到 0；转录顶部留白取同一个值。 */
export const HEADER_VEIL = 64;
/** 手机悬浮页头的纱高（px）：8px 上边距 + 46px 标题胶囊 + 8px，再 16px 渐隐；转录顶部留白取同一个值。 */
export const COMPACT_HEADER_VEIL = 78;

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

export function snoozeAt(kind: "1h" | "3h" | "tomorrow" | "week"): number {
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
 * orb 的状态文案：orb 本身就绪、但最近一次 setup / resume 失败时报环境失败（`failed`），与托盘里的提示条一致；
 * 其它状态（冻结、停机、唤醒、恢复、失败等）照实呈现生命周期，历史失败不覆盖。
 */
export function orbStatus(
	orb: Orb,
	t: TFunction,
): { failed: boolean; label: string } {
	const failedRun =
		orb.state !== "ready"
			? undefined
			: orb.lastSetup && !orb.lastSetup.ok
				? orb.lastSetup
				: orb.lastResume && !orb.lastResume.ok
					? orb.lastResume
					: undefined;
	return failedRun
		? {
				failed: true,
				label: t(
					failedRun.phase === "resume" ? "env.resumeFailed" : "env.setupFailed",
				),
			}
		: { failed: false, label: t(`orbState.${orb.state}`) };
}

/** orb 状态胶囊：状态珠 + 状态文字（页头窄时只留状态珠）；点击打开信息面板。环境失败时是错误色圆点 + 文案。 */
function OrbChip({ orb, working }: { orb: Orb; working: boolean }): ReactNode {
	const { t } = useTranslation();
	const { failed: failedRun, label: state } = orbStatus(orb, t);
	return (
		<button
			type="button"
			aria-label={t("thread.orbState", { state })}
			title={t("thread.orbState", { state })}
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

/** 长路径只留最后两段（`…/code/oryn`），完整路径放悬停提示。 */
export function shortPath(p: string): string {
	const parts = p.split("/").filter(Boolean);
	return parts.length > 2 ? `…/${parts.slice(-2).join("/")}` : p;
}

type GitHubThreadTask = Schemas["GitHubThreadTask"];

const ISSUE_STATES = [
	"new",
	"reproducing",
	"fixing",
	"reviewing",
	"opened",
	"needs_info",
	"merged",
	"closed",
	"abandoned",
] as const satisfies readonly Schemas["GitHubTaskState"][];

const RELEASE_STATES = [
	"awaiting_ci",
	"fixing",
	"green",
	"failed",
	"superseded",
] as const satisfies readonly Schemas["GitHubReleaseState"][];

/** 任务状态的语义色：只有成功、等待处理与失败上色，其余中性（不显示状态点）。 */
const TASK_TONE: Record<string, "ok" | "warn" | "err"> = {
	merged: "ok",
	green: "ok",
	needs_info: "warn",
	awaiting_ci: "warn",
	failed: "err",
};

/** GitHub 任务的对象（`#12` 或 `v1.2.0`）与状态文案；服务端出现新状态值时原样显示。 */
export function githubTaskText(
	task: GitHubThreadTask,
	t: TFunction,
): { subject: string; state: string } {
	if (task.kind === "release") {
		const s = RELEASE_STATES.find((x) => x === task.state);
		return {
			subject: `v${task.version ?? ""}`,
			state: s ? t(`github.task.release.${s}`) : task.state,
		};
	}
	const s = ISSUE_STATES.find((x) => x === task.state);
	return {
		subject: `#${task.number ?? ""}`,
		state: s ? t(`github.task.issue.${s}`) : task.state,
	};
}

/** 任务在 GitHub 上的页面：issue 页或 `v<版本>` 的 Release 页。 */
export function githubTaskUrl(task: GitHubThreadTask): string {
	return task.kind === "release"
		? `https://github.com/${task.repo}/releases/tag/v${task.version ?? ""}`
		: `https://github.com/${task.repo}/issues/${task.number ?? ""}`;
}

export function githubTaskIcon(task: GitHubThreadTask): "rocket" | "github" {
	return task.kind === "release" ? "rocket" : "github";
}

/** 线程页头的 GitHub 任务胶囊（M15）：图标 +（页头够宽时）`#12 · 修复中`，有语义色时带状态点；点开 GitHub 页面。 */
export function GitHubTaskChip({
	task,
}: {
	task: GitHubThreadTask;
}): ReactNode {
	const { t } = useTranslation();
	const { subject, state } = githubTaskText(task, t);
	const tone = TASK_TONE[task.state];
	const label = t(
		task.kind === "release"
			? "github.task.releaseLabel"
			: "github.task.issueLabel",
		{ repo: task.repo, subject, state },
	);
	return (
		<a
			href={githubTaskUrl(task)}
			target="_blank"
			rel="noopener noreferrer"
			data-testid="github-task-chip"
			data-state={task.state}
			aria-label={label}
			title={label}
			className="inline-flex h-[26px] shrink-0 items-center gap-1.5 rounded-full border border-border bg-panel/60 px-2 font-medium text-fg text-xs transition-colors duration-150 hover:bg-hover"
		>
			<Icon name={githubTaskIcon(task)} size={12} aria-hidden />
			<span aria-hidden className="hidden tabular-nums @md/header:inline">
				{`${subject} · ${state}`}
			</span>
			{tone && (
				<span className="inline-flex">
					<StatusDot tone={tone} />
				</span>
			)}
		</a>
	);
}

/**
 * runner 线程（M11b）的位置胶囊：状态珠 + `runner 名 · cwd`（页头窄时只留状态珠与名字）；点击打开信息面板。
 * runner 不在线时状态珠是 `stopped` 的静止灰点，与离线提示条一致。
 */
function RunnerChip({
	orb,
	cwd,
	working,
}: {
	orb: Orb;
	cwd?: string;
	working: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const name = orb.providerName ?? t("runner.label");
	const state =
		orb.state === "ready" ? t("runner.online") : t("runner.offline");
	return (
		<button
			type="button"
			data-testid="runner-chip"
			aria-label={t("runner.chip", { name, cwd: cwd ?? "", state })}
			title={cwd ? `${name} · ${cwd}` : name}
			className="ml-1.5 inline-flex h-[26px] min-w-0 shrink items-center gap-[7px] rounded-full border border-border bg-panel/60 px-2 font-medium text-fg text-xs transition-colors duration-150 hover:bg-hover @lg/header:pr-2.5 @lg/header:pl-[9px]"
			onClick={() => {
				const ui = uiStore.getState();
				ui.setPanelTab("info");
				ui.setPanel(true);
			}}
		>
			<OrbBead state={orb.state} active={working} size={12} label={state} />
			<span aria-hidden className="max-w-28 shrink-0 truncate">
				{name}
			</span>
			{cwd && (
				<span
					aria-hidden
					className="hidden min-w-0 truncate font-mono font-normal text-muted @lg/header:inline"
				>
					<span className="text-ghost">· </span>
					{shortPath(cwd)}
				</span>
			)}
		</button>
	);
}

/** 线程自己的项目：只认线程的 `projectId`，和侧栏分组一致；「无项目」线程没有项目段（即使 orb 属于某个项目）。 */
export function useThreadProject(projectId: string | null | undefined) {
	const projects = useProjects();
	return projectId
		? projects.data?.projects.find((p) => p.id === projectId)
		: undefined;
}

/** 线程所属的 Space（Space 聊天里派出的线程）。 */
export function useThreadSpace(spaceId: string | null | undefined) {
	const spaces = useSpaces();
	return spaceId
		? spaces.data?.spaces?.find((s) => s.id === spaceId)
		: undefined;
}

/** 定时对话框的目标：线程内定时，或建出本线程的「每次新建线程」定时。 */
export type ScheduleTarget =
	| { mode: "thread" }
	| { mode: "new_thread"; id: string };

/**
 * 线程的全部操作（宽页头的 More Actions 与手机标题菜单共用）：菜单项按宽页头菜单的顺序排，`key` 稳定（手机菜单按 key 取用、重排）。
 * `act` 包一层错误收集（失败文案进 `error`）；`dialogs` 是操作要用的定时 / 归档对话框，调用方渲染。
 */
export function useThreadActions(threadId: string): {
	items: MenuItem[];
	act: (fn: () => Promise<unknown>) => () => void;
	error: string | null;
	openSchedule: (target: ScheduleTarget) => void;
	dialogs: ReactNode;
} {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const thread = useThread(threadId, (s) => s.thread);
	const [error, setError] = useState<string | null>(null);
	const [scheduleOpen, setScheduleOpen] = useState<ScheduleTarget | null>(null);
	const act = (fn: () => Promise<unknown>) => () => {
		setError(null);
		fn().catch((e: unknown) =>
			setError(e instanceof Error ? e.message : String(e)),
		);
	};
	const { archive, dialog: archiveDialog } = useArchiveThread();
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
			key: "schedule",
			icon: "clock",
			label: t("thread.schedule"),
			disabled: Boolean(thread?.archivedAt),
			onSelect: () => setScheduleOpen({ mode: "thread" }),
		},
		{
			key: "archive",
			icon: "archive",
			label: thread?.archivedAt ? t("thread.unarchive") : t("thread.archive"),
			onSelect: act(() =>
				thread?.archivedAt
					? patchThread(threadId, { archived: false })
					: archive(thread ?? { id: threadId }),
			),
		},
		...(thread?.archivedAt
			? []
			: [
					{
						key: "archiveShipped",
						icon: "rocket" as const,
						label: t("thread.archiveShipped"),
						onSelect: act(() => archive(thread ?? { id: threadId }, true)),
					},
				]),
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
					native: {
						label: t(`color.${c}`),
						icon: { dot: markHex(THREAD_HUE[c] ?? 0, isDark()) },
					},
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
			// 原生菜单选中时由原生直接写剪贴板（经事件回到网页时已没有用户手势）
			native: { copy: `${location.origin}/t/${threadId}` },
		},
		{
			key: "costUsage",
			icon: "chart",
			label: t("thread.costUsage"),
			onSelect: () =>
				navigate(
					`/stats/usage?thread=${encodeURIComponent(threadId)}&range=all`,
				),
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
	const dialogs = (
		<>
			{scheduleOpen?.mode === "thread" && (
				<ScheduleDialog
					target={{ mode: "thread", threadId }}
					onClose={() => setScheduleOpen(null)}
				/>
			)}
			{scheduleOpen?.mode === "new_thread" && (
				<ScheduleDialog
					scheduleId={scheduleOpen.id}
					target={{ mode: "new_thread" }}
					onClose={() => setScheduleOpen(null)}
				/>
			)}
			{archiveDialog}
		</>
	);
	return { items, act, error, openSchedule: setScheduleOpen, dialogs };
}

/**
 * 宽页头（medium / expanded；手机是 `CompactThreadHeader`）：Space / 项目 / 标题卡 / 标题（点击就地改名）、
 * 公开、定时与 orb 胶囊、会话树 / 面板开关 / More Actions。
 */
export function ThreadHeader({
	threadId,
	sidebarToggle,
	veil = HEADER_VEIL,
	chromeLeft = false,
	chromeRight = false,
	infoBeside = false,
}: {
	threadId: string;
	/** 非 expanded 宽度时在标题栏左侧显示侧边栏开关；expanded 下侧栏收起时也显示 */
	sidebarToggle: boolean;
	/** 渐进模糊纱的高度（px）；页头下方还叠着一行（medium 的同级标签条）时加上那一行，纱一起盖住 */
	veil?: number;
	/** 页头是窗口最左侧的标题行（侧栏没占着左上角）：左侧窗口控件放这里 */
	chromeLeft?: boolean;
	/** 页头是窗口最右侧的标题行（右面板没展开）：右侧自绘按钮放这里 */
	chromeRight?: boolean;
	/** 旁边正显示着信息面板：orb / runner 的状态、规格都在那里，页头不再重复这枚胶囊 */
	infoBeside?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const thread = useThread(threadId, (s) => s.thread);
	const panelOpen = useUi((s) => s.panelOpen);
	const sidebarCollapsed = useUi((s) => s.sidebarCollapsed);
	const bp = useBreakpoint();
	const segments = useViewportSegments();
	// 面板开关只属于 expanded 三栏：窄屏面板是同级标签，双段视口右段常驻
	const panelToggle = bp === "expanded" && !segments;
	const orb = useOrbOf(threadId);
	const project = useThreadProject(thread?.projectId);
	const card = parseTitleCard(thread?.title ?? "");
	const hue = threadHue(thread?.color);
	const [editing, setEditing] = useState<string | null>(null);
	const space = useThreadSpace(thread?.spaceId);
	const { items, act, error, openSchedule, dialogs } =
		useThreadActions(threadId);
	const isRunner = orb?.kind === "runner";

	return (
		<header
			data-window-drag=""
			className="veil-host relative isolate @container/header flex h-12 shrink-0 items-center gap-1.5 px-2 sm:px-3"
		>
			<div aria-hidden className="veil" style={{ height: veil }}>
				<i />
				<i />
				<i />
				<i />
				<i />
				<i />
			</div>
			<ChromeLead show={chromeLeft} />
			{(sidebarToggle || sidebarCollapsed) && (
				<IconButton
					icon={sidebarToggle ? "menu" : "sidebar"}
					label={t("nav.sidebar")}
					onClick={() => uiStore.getState().toggleSidebar()}
				/>
			)}
			{space && (
				// Space 段只在宽页头出现：Space / 项目 / 标题
				<Link
					to={`/space/${space.id}`}
					data-testid="header-space"
					className="hidden shrink-0 items-center gap-2 text-sm @xl/header:inline-flex"
					title={space.name}
				>
					<Icon name="target" size={14} aria-hidden className="text-faint" />
					<span className="max-w-32 truncate text-muted hover:text-fg">
						{space.name}
					</span>
					<span aria-hidden className="text-ghost">
						/
					</span>
				</Link>
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
				// 有未撤销的公开链接：中性徽标，点开分享对话框管理；窄页头只留地球图标，把宽度让给标题。
				// 读屏名固定为「已公开」（与宽页头上看得见的字一致），链接数量在悬停提示里
				<button
					type="button"
					className="inline-flex shrink-0 rounded-sm"
					aria-label={t("share.header.badge")}
					title={t("share.header.badgeTitle", { n: thread?.publicShares ?? 0 })}
					onClick={() => uiStore.getState().open({ kind: "share" })}
				>
					<span className="hidden @md/header:inline-flex">
						<Badge icon="globe">{t("share.header.badge")}</Badge>
					</span>
					<span className="inline-flex h-6 w-6 items-center justify-center rounded-sm text-muted @md/header:hidden">
						<Icon name="globe" size={14} />
					</span>
				</button>
			)}
			{editing === null && thread?.schedule && (
				<ScheduleChip
					schedule={thread.schedule}
					onOpen={() => openSchedule({ mode: "thread" })}
				/>
			)}
			{editing === null && thread?.scheduleRun && (
				<ScheduleRunChip
					run={thread.scheduleRun}
					onOpen={() =>
						openSchedule({
							mode: "new_thread",
							id: thread.scheduleRun?.scheduleId ?? "",
						})
					}
				/>
			)}
			{editing === null &&
				thread?.pulls
					?.slice(0, 2)
					.map((p) => <PrChip key={p.number} threadId={threadId} pull={p} />)}
			{editing === null && thread?.githubTask && (
				<GitHubTaskChip task={thread.githubTask} />
			)}
			{editing === null &&
				orb &&
				!infoBeside &&
				(isRunner ? (
					<RunnerChip
						orb={orb}
						cwd={thread?.cwd}
						working={thread?.state === "working"}
					/>
				) : (
					<OrbChip orb={orb} working={thread?.state === "working"} />
				))}
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
			{panelToggle && (
				<IconButton
					icon="panel"
					label={t("nav.infoPanel")}
					active={panelOpen}
					onClick={() => uiStore.getState().setPanel(!panelOpen)}
				/>
			)}
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
			{chromeRight && <WindowControls side="right" className="ml-1" />}
			{dialogs}
		</header>
	);
}
