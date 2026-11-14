/**
 * 右侧面板（计划 M3 2.5）：标签 `Info | Changes | Files | Terminal`，orb 有 portal 时在终端之后追加 `Portal`（M4），
 * orb 有桌面（`orb.desktop`）时再追加 `Desktop`（M8，Profile 被占用或装载失败时带提醒点）。
 * Info 为原信息面板；其余按需加载。标签条可横向滚动，选中项下有一块滑动底板；放不下全部文字时只有选中项保留文字。
 * Terminal 标签在有终端等待用户（agent 的 `pty:true` 命令、会话要密码）时亮注意点；
 * Changes 标签在有未查看的新改动时亮 accent 小点。
 * 窄屏（M7 4.2）：`peer` 面板与转录是同级标签 `Agent | Changes | Files | Terminal | (Portal) | (Desktop) | Info`，
 * 标签条（`ThreadTabStrip`）同时用在转录页头下方；Agent 标签在线程等你回答时亮注意点。
 * 桌面 App 无系统标题栏（`chrome`）：面板是窗口最右侧（或最大化时唯一）的纸面时承载窗口控件——三栏里在纸面顶部多一行
 * 48px 可拖动的标题行（与转录页头同高，按钮与页头按钮在同一条中线上），窄屏同级面板则直接放进标签条两端。
 */
import {
	lazy,
	type ReactNode,
	type RefObject,
	Suspense,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useBreakpoint, useViewportSegments } from "../../lib/viewport";
import { useThread } from "../../state/stores";
import { useTerminals } from "../../state/terminals";
import {
	hasUnseenChanges,
	type PanelTab,
	type ThreadTab,
	uiStore,
	useUi,
} from "../../state/ui";
import { type GitStatus, getGitStatus, readChangesRoot } from "../changes/git";
import { useWorkspaceActivity } from "../files/hooks";
import {
	ChromeLead,
	useChromeSides,
	WindowControls,
} from "../layout/WindowControls";
import { Icon, type IconName } from "../ui/icons";
import { IconButton, Spinner, tabClass } from "../ui/primitives";
import { InfoPanel } from "./InfoPanel";
import { useOrbOf } from "./ThreadHeader";

const ChangesPanel = lazy(() => import("../changes/ChangesPanel"));
const FilesPanel = lazy(() => import("../files/FilesPanel"));
const TerminalPanel = lazy(() => import("../terminal/TerminalPanel"));
const PortalPanel = lazy(() => import("../portal/PortalPanel"));
const DesktopPanel = lazy(() => import("../desktop/DesktopPanel"));

const CONTENT_TABS: readonly PanelTab[] = ["changes", "files", "terminal"];

const TAB_ICON: Record<ThreadTab, IconName> = {
	agent: "message",
	info: "info",
	changes: "diff",
	files: "folder",
	terminal: "terminal",
	portal: "globe",
	desktop: "monitor",
};

/** 改动标签后台检查的间隔（与 Changes 面板的后台轮询同频）。 */
export const CHANGES_CHECK_MS = 10_000;

/** 改动签名：文件、状态与增删行数；不含暂存状态（暂存是用户自己的操作）。无改动为空串。 */
export function changesSignature(s: GitStatus): string {
	if (!s.repo || s.files.length === 0) return "";
	return s.files
		.map(
			(f) =>
				`${f.status}:${f.oldPath ?? ""}>${f.path}:${f.additions ?? "-"}:${f.deletions ?? "-"}`,
		)
		.sort()
		.join("\n");
}

/**
 * 「改动」标签是否有未查看的新改动。Changes 面板只在自己挂载时轮询，所以这里在其它标签下
 * 以同样的后台请求（`bg=1`、默认范围 `all`）低频检查：orb 就绪、文档可见时每 10s 一次，
 * agent 活动后 1s 防抖再查一次。正在看改动标签时不查（面板自己在拉），并把最新签名记为已看；
 * 离开改动标签后的第一次结果也记为已看。
 * 「最近在看改动的线程」放在模块级：窄屏同级标签下看改动的是面板里的标签条，离开后接手检查的是转录页头里新挂载的那一条。
 */
let viewedChanges: string | null = null;

function useUnseenChanges(
	threadId: string,
	orbId: string | undefined,
	viewing: boolean,
): boolean {
	const ready = useOrbOf(threadId)?.state === "ready";
	const sig = useUi((s) => s.changesSig[threadId]);
	const check = useRef<(() => void) | null>(null);

	useEffect(() => {
		if (!viewing) return;
		viewedChanges = threadId;
		uiStore.getState().markChangesSeen(threadId);
	}, [viewing, threadId]);

	useEffect(() => {
		if (!orbId || !ready || viewing) return;
		const ctl = new AbortController();
		let seen = viewedChanges === threadId;
		viewedChanges = null;
		let busy = false;
		const run = async (force: boolean) => {
			if (busy || (!force && document.visibilityState !== "visible")) return;
			busy = true;
			try {
				const s = await getGitStatus(
					{ orbId, root: readChangesRoot(threadId), scope: { scope: "all" } },
					true,
					ctl.signal,
				);
				if (ctl.signal.aborted) return;
				uiStore.getState().noteChanges(threadId, changesSignature(s), seen);
				seen = false;
			} catch {
				// 后台检查：orb 不可用、取消、超时都等下一次
			} finally {
				busy = false;
			}
		};
		void run(true);
		check.current = () => void run(false);
		const timer = setInterval(() => void run(false), CHANGES_CHECK_MS);
		return () => {
			ctl.abort();
			clearInterval(timer);
			check.current = null;
		};
	}, [threadId, orbId, ready, viewing]);

	useWorkspaceActivity(orbId && ready && !viewing ? threadId : undefined, () =>
		check.current?.(),
	);

	return !viewing && hasUnseenChanges(sig);
}

/** 带文字的标签内容实际占的宽度：最后一个标签的右边缘（相对 tablist，它是 offsetParent）。 */
function tabsWidth(list: HTMLElement): number {
	let right = 0;
	for (const tab of list.querySelectorAll<HTMLElement>('[role="tab"]'))
		right = Math.max(right, tab.offsetLeft + tab.offsetWidth);
	return right;
}

/**
 * 标签条放不下全部文字时只有选中的标签保留文字，其余只显示图标（保留 `aria-label` 与悬停提示）。
 * 带文字时量出标签内容本身的宽度（最后一个标签的右边缘，不是会被 flex 撑满的 tablist）并记住，之后按可用宽度
 * 与它比较；标签集合或语言变化时先回到带文字状态重新量。
 * `reserve`：此刻没显示、但切到别的标签就会出现的页头按钮宽度（信息标签下隐藏的「最大化」），
 * 按它出现时的宽度判断，免得切标签时文字忽隐忽现。
 */
function useCompactTabs(
	ref: RefObject<HTMLDivElement | null>,
	tabs: readonly ThreadTab[],
	lang: string,
	reserve: number,
): boolean {
	const sig = `${tabs.join(",")}|${lang}`;
	const [state, setState] = useState({ sig, compact: false });
	const compact = state.sig === sig && state.compact;
	const full = useRef(0);
	const current = useRef(compact);
	current.current = compact;
	useLayoutEffect(() => {
		const list = ref.current;
		if (!list) return;
		const measure = () => {
			// tablist 是 flex-1，scrollWidth 不小于 clientWidth；要量的是标签内容（offset* 不受横向滚动影响）
			if (!current.current) full.current = tabsWidth(list);
			const next = list.clientWidth - reserve + 1 < full.current;
			if (next !== current.current) {
				current.current = next;
				setState({ sig, compact: next });
			}
		};
		measure();
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(measure);
		ro.observe(list);
		return () => ro.disconnect();
	}, [sig, ref, reserve]);
	return compact;
}

/**
 * 当前线程的标签集合：`peer`（窄屏同级标签）为 `agent … info`，否则为 `info …` 面板标签；
 * orb 有 portal 时在终端之后加 Portal，orb 有桌面时再加 Desktop。
 * `portalGone` / `desktopGone`：对应标签消失（服务停掉、orb 删除、换到没有它的线程）；orb 还没到时为假，先不动。
 */
export function useThreadTabs(
	threadId: string,
	peer: boolean,
): {
	tabs: readonly ThreadTab[];
	portalGone: boolean;
	desktopGone: boolean;
} {
	const orbId = useThread(threadId, (s) => s.thread?.orbId);
	const loaded = useThread(threadId, (s) => s.thread !== undefined);
	const orb = useOrbOf(threadId);
	const live = orb?.state !== "deleted";
	const hasPortal = live && (orb?.portals?.length ?? 0) > 0;
	const hasDesktop = live && orb?.desktop !== undefined;
	const tabs = useMemo(() => {
		const extra: PanelTab[] = [
			...(hasPortal ? (["portal"] as const) : []),
			...(hasDesktop ? (["desktop"] as const) : []),
		];
		return peer
			? (["agent", ...CONTENT_TABS, ...extra, "info"] as const)
			: (["info", ...CONTENT_TABS, ...extra] as const);
	}, [peer, hasPortal, hasDesktop]);
	const settled = orb !== undefined || (loaded && !orbId);
	return {
		tabs,
		portalGone: !hasPortal && settled,
		desktopGone: !hasDesktop && settled,
	};
}

/**
 * 线程标签条（tablist）：滑动底板、放不下时只留选中项文字、终端注意点、改动未看点、Agent 注意点（线程在等你回答）。
 * 只是标签本身（`flex-1`），所在行与两侧按钮由调用方放。
 * `reserve`：见 `useCompactTabs`。
 */
export function ThreadTabStrip({
	threadId,
	tabs,
	shown,
	onSelect,
	reserve = 0,
}: {
	threadId: string;
	tabs: readonly ThreadTab[];
	shown: ThreadTab;
	onSelect: (tab: ThreadTab) => void;
	reserve?: number;
}): ReactNode {
	const { t, i18n } = useTranslation();
	const orbId = useThread(threadId, (s) => s.thread?.orbId);
	const attention = useTerminals(threadId, (s) => s.attention);
	// Desktop：Profile 被别的 orb 占用或装载失败，需要你选择或重试
	const profileStatus = useOrbOf(threadId)?.desktop?.profile?.status;
	const deskAttention = profileStatus === "in_use" || profileStatus === "error";
	const unseen = useUnseenChanges(threadId, orbId, shown === "changes");
	// 线程进入 awaiting_input：有待回答的扩展 UI 请求或待批准的计划
	const awaiting = useThread(
		threadId,
		(s) => s.pendingUi.length > 0 || s.planProposal !== null,
	);

	// 滑动底板：读选中标签的 offsetLeft/offsetWidth（相对可滚动的 tablist，随内容一起滚动）。
	// 首次定位与尺寸变化（窗口缩放、字体加载）直接到位，只有切换标签时滑动。
	const listRef = useRef<HTMLDivElement>(null);
	const compact = useCompactTabs(listRef, tabs, i18n.language, reserve);
	const plateRef = useRef<HTMLSpanElement>(null);
	const placed = useRef(false);
	// biome-ignore lint/correctness/useExhaustiveDependencies: 标签（含增减与只显示图标）与语言变化后按钮位置 / 宽度改变，需要重新定位
	useLayoutEffect(() => {
		const list = listRef.current;
		const plate = plateRef.current;
		if (!list || !plate) return;
		const place = (animate: boolean) => {
			const el = list.querySelector<HTMLElement>(
				'[role="tab"][aria-selected="true"]',
			);
			if (!el) return;
			plate.style.transitionDuration = animate ? "" : "0ms";
			plate.style.left = `${el.offsetLeft}px`;
			plate.style.width = `${el.offsetWidth}px`;
			plate.style.opacity = "1";
			if (animate) el.scrollIntoView?.({ block: "nearest", inline: "nearest" });
		};
		place(placed.current);
		placed.current = true;
		const settle = () => place(false);
		window.addEventListener("resize", settle);
		let ro: ResizeObserver | undefined;
		if (typeof ResizeObserver !== "undefined") {
			ro = new ResizeObserver(settle);
			for (const b of list.querySelectorAll('[role="tab"]')) ro.observe(b);
		}
		return () => {
			window.removeEventListener("resize", settle);
			ro?.disconnect();
		};
	}, [shown, tabs, compact, i18n.language]);

	return (
		<div
			ref={listRef}
			role="tablist"
			aria-label={t("rightPanel.label")}
			data-testid="thread-tabs"
			className="focus-inset relative flex min-w-0 flex-1 items-center gap-px overflow-x-auto [scrollbar-width:none]"
		>
			<span
				ref={plateRef}
				aria-hidden
				className="absolute top-0 left-0 h-7 rounded-md bg-hover opacity-0 shadow-xs ring-1 ring-border transition-[left,width] duration-200 ease-out"
			/>
			{tabs.map((id) => (
				<button
					key={id}
					type="button"
					role="tab"
					aria-selected={shown === id}
					aria-label={t(`rightPanel.tabs.${id}`)}
					title={
						compact && shown !== id ? t(`rightPanel.tabs.${id}`) : undefined
					}
					data-panel-tab={id}
					className={tabClass(shown === id)}
					onClick={() => onSelect(id)}
				>
					<Icon name={TAB_ICON[id]} size={14} aria-hidden />
					{(!compact || shown === id) && t(`rightPanel.tabs.${id}`)}
					{id === "agent" && awaiting && shown !== "agent" && (
						<span
							data-testid="agent-attention"
							title={t("rightPanel.agentAttention")}
							className="absolute top-1 right-0.5 h-1.5 w-1.5 animate-pulse rounded-full bg-accent"
						/>
					)}
					{id === "terminal" && attention && shown !== "terminal" && (
						<span
							data-testid="terminal-attention"
							title={t("rightPanel.attention")}
							className="absolute top-1 right-0.5 h-1.5 w-1.5 animate-pulse rounded-full bg-warn"
						/>
					)}
					{id === "desktop" && deskAttention && (
						<span
							data-testid="desktop-attention"
							title={t("rightPanel.desktopAttention")}
							className="absolute top-1 right-0.5 h-1.5 w-1.5 rounded-full bg-warn"
						/>
					)}
					{id === "changes" && unseen && (
						<span
							data-testid="changes-unseen"
							title={t("rightPanel.unseenChanges")}
							className="absolute top-1 right-0.5 h-1.5 w-1.5 rounded-full bg-accent"
						/>
					)}
				</button>
			))}
		</div>
	);
}

/**
 * `peer`：窄屏线程页里与转录并列的面板（同级标签带 Agent，选 Agent 回到转录）；不能关闭、不能最大化，
 * 标签条左侧放侧栏开关（转录页头此时隐藏）。
 * 不传 `onClose` 就没有关闭按钮（双段视口的右段常驻）；最大化只在 expanded 三栏里有。
 */
export function RightPanel({
	threadId,
	onClose,
	peer = false,
	chrome,
}: {
	threadId: string;
	onClose?: () => void;
	peer?: boolean;
	/** 由面板承载的窗口控件：`left` = 面板是最左侧表面（同级面板、或最大化且侧栏没占左上角），`right` = 最右侧表面 */
	chrome?: { left: boolean; right: boolean };
}): ReactNode {
	const { t } = useTranslation();
	const tab = useUi((s) => s.panelTab);
	const max = useUi((s) => s.panelMax);
	const bp = useBreakpoint();
	const segments = useViewportSegments();
	const orbId = useThread(threadId, (s) => s.thread?.orbId);
	const { tabs, portalGone, desktopGone } = useThreadTabs(threadId, peer);
	// peer 模式下 threadTab 非 agent 时与 panelTab 同步，这里统一按 panelTab 显示；可选标签不在时先按信息标签显示
	const shown: PanelTab =
		(tab === "portal" || tab === "desktop") && !tabs.includes(tab)
			? "info"
			: tab;
	useEffect(() => {
		if ((tab === "portal" && portalGone) || (tab === "desktop" && desktopGone))
			uiStore.getState().setPanelTab("info");
	}, [tab, portalGone, desktopGone]);
	const canMax = !peer && bp === "expanded" && !segments;
	const sides = useChromeSides();
	const chromeLeft = !!chrome?.left && sides.left !== null;
	const chromeRight = !!chrome?.right && sides.right !== null;

	return (
		<aside
			className="flex h-full w-full min-w-0 flex-col bg-panel"
			aria-label={t("rightPanel.label")}
		>
			{!peer && (chromeLeft || chromeRight) && (
				<div
					data-window-drag=""
					data-chrome-row="panel"
					className="flex h-12 shrink-0 items-center px-2"
				>
					{chromeLeft && <ChromeLead />}
					<span className="min-w-0 flex-1" />
					{chromeRight && <WindowControls side="right" />}
				</div>
			)}
			<div
				// 同级面板的标签条就是窗口最上面一行：空白处可拖动窗口
				data-window-drag={peer ? "" : undefined}
				className={`flex h-12 shrink-0 items-center gap-1 border-border border-b pr-2 ${peer ? "pl-2 sm:pl-3" : "pl-2.5"}`}
			>
				{peer && chromeLeft && <ChromeLead />}
				{peer && (
					<IconButton
						icon="menu"
						label={t("nav.sidebar")}
						onClick={() => uiStore.getState().toggleSidebar()}
					/>
				)}
				<ThreadTabStrip
					threadId={threadId}
					tabs={tabs}
					shown={shown}
					onSelect={(id) =>
						peer
							? uiStore.getState().setThreadTab(id)
							: id !== "agent" && uiStore.getState().setPanelTab(id)
					}
					// 信息标签下不显示「最大化」（IconButton 32px + 间距 4px），按它在时的宽度判断
					reserve={canMax && shown === "info" ? 36 : 0}
				/>
				{canMax && shown !== "info" && (
					<IconButton
						icon={max ? "minimize" : "maximize"}
						size={14}
						label={max ? t("rightPanel.restore") : t("rightPanel.maximize")}
						active={max}
						onClick={() => uiStore.getState().togglePanelMax()}
					/>
				)}
				{onClose && !peer && (
					<IconButton
						icon="close"
						size={14}
						label={t("common.close")}
						onClick={onClose}
					/>
				)}
				{peer && chromeRight && (
					<WindowControls side="right" className="ml-1" />
				)}
			</div>
			<div className="min-h-0 flex-1">
				{shown === "info" ? (
					<InfoPanel threadId={threadId} />
				) : (
					<Suspense
						fallback={
							<div className="flex h-full items-center justify-center text-faint">
								<Spinner />
							</div>
						}
					>
						{shown === "changes" && (
							// 换线程即重建：根目录、范围（含所选 commit）与所选文件都属于该线程
							<ChangesPanel key={threadId} threadId={threadId} orbId={orbId} />
						)}
						{shown === "files" && (
							<FilesPanel threadId={threadId} orbId={orbId} />
						)}
						{shown === "terminal" && <TerminalPanel threadId={threadId} />}
						{shown === "portal" && (
							<PortalPanel threadId={threadId} orbId={orbId} />
						)}
						{shown === "desktop" && (
							<DesktopPanel threadId={threadId} orbId={orbId} />
						)}
					</Suspense>
				)}
			</div>
		</aside>
	);
}
