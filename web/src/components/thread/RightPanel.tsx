/**
 * 右侧面板（计划 M3 2.5）：标签 `Info | Changes | Files | Terminal`，orb 有 portal 时在终端之后追加 `Portal`（M4）。
 * Info 为原信息面板；其余按需加载。标签条可横向滚动，选中项下有一块滑动底板；放不下全部文字时只有选中项保留文字。
 * Terminal 标签在有终端等待用户（agent 的 `pty:true` 命令、会话要密码）时亮注意点；
 * Changes 标签在有未查看的新改动时亮 accent 小点。
 */
import {
	lazy,
	type ReactNode,
	type RefObject,
	Suspense,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useBreakpoint } from "../../lib/viewport";
import { useThread } from "../../state/stores";
import { useTerminals } from "../../state/terminals";
import {
	hasUnseenChanges,
	type PanelTab,
	uiStore,
	useUi,
} from "../../state/ui";
import { type GitStatus, getGitStatus, readChangesRoot } from "../changes/git";
import { useWorkspaceActivity } from "../files/hooks";
import { Icon, type IconName } from "../ui/icons";
import { IconButton, Spinner, tabClass } from "../ui/primitives";
import { InfoPanel } from "./InfoPanel";
import { useOrbOf } from "./ThreadHeader";

const ChangesPanel = lazy(() => import("../changes/ChangesPanel"));
const FilesPanel = lazy(() => import("../files/FilesPanel"));
const TerminalPanel = lazy(() => import("../terminal/TerminalPanel"));
const PortalPanel = lazy(() => import("../portal/PortalPanel"));

const BASE_TABS: readonly PanelTab[] = ["info", "changes", "files", "terminal"];
const PORTAL_TABS: readonly PanelTab[] = [...BASE_TABS, "portal"];

const TAB_ICON: Record<PanelTab, IconName> = {
	info: "info",
	changes: "diff",
	files: "folder",
	terminal: "terminal",
	portal: "globe",
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
 */
function useUnseenChanges(
	threadId: string,
	orbId: string | undefined,
	viewing: boolean,
): boolean {
	const ready = useOrbOf(threadId)?.state === "ready";
	const sig = useUi((s) => s.changesSig[threadId]);
	// 最近一次正在看改动标签的线程
	const viewed = useRef<string | null>(null);
	const check = useRef<(() => void) | null>(null);

	useEffect(() => {
		if (!viewing) return;
		viewed.current = threadId;
		uiStore.getState().markChangesSeen(threadId);
	}, [viewing, threadId]);

	useEffect(() => {
		if (!orbId || !ready || viewing) return;
		const ctl = new AbortController();
		let seen = viewed.current === threadId;
		viewed.current = null;
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
	tabs: readonly PanelTab[],
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

export function RightPanel({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose?: () => void;
}): ReactNode {
	const { t, i18n } = useTranslation();
	const tab = useUi((s) => s.panelTab);
	const max = useUi((s) => s.panelMax);
	const bp = useBreakpoint();
	const orbId = useThread(threadId, (s) => s.thread?.orbId);
	const loaded = useThread(threadId, (s) => s.thread !== undefined);
	const orb = useOrbOf(threadId);
	const hasPortal = orb?.state !== "deleted" && (orb?.portals?.length ?? 0) > 0;
	const tabs = hasPortal ? PORTAL_TABS : BASE_TABS;
	// portal 消失（服务停掉、orb 删除、换到没有 portal 的线程）时退回信息标签；orb 还没到时先不动
	const portalGone = !hasPortal && (orb !== undefined || (loaded && !orbId));
	const shown: PanelTab = tab === "portal" && !hasPortal ? "info" : tab;
	useEffect(() => {
		if (tab === "portal" && portalGone) uiStore.getState().setPanelTab("info");
	}, [tab, portalGone]);
	const attention = useTerminals(threadId, (s) => s.attention);
	const unseen = useUnseenChanges(threadId, orbId, shown === "changes");

	// 滑动底板：读选中标签的 offsetLeft/offsetWidth（相对可滚动的 tablist，随内容一起滚动）。
	// 首次定位与尺寸变化（窗口缩放、字体加载）直接到位，只有切换标签时滑动。
	const listRef = useRef<HTMLDivElement>(null);
	// 信息标签下不显示「最大化」（IconButton 32px + 间距 4px），按它在时的宽度判断
	const compact = useCompactTabs(
		listRef,
		tabs,
		i18n.language,
		bp === "expanded" && shown === "info" ? 36 : 0,
	);
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
		<aside
			className="flex h-full w-full min-w-0 flex-col bg-panel"
			aria-label={t("rightPanel.label")}
		>
			<div className="flex h-12 shrink-0 items-center gap-1 border-border border-b pr-2 pl-2.5">
				<div
					ref={listRef}
					role="tablist"
					aria-label={t("rightPanel.label")}
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
							onClick={() => uiStore.getState().setPanelTab(id)}
						>
							<Icon name={TAB_ICON[id]} size={14} aria-hidden />
							{(!compact || shown === id) && t(`rightPanel.tabs.${id}`)}
							{id === "terminal" && attention && shown !== "terminal" && (
								<span
									data-testid="terminal-attention"
									title={t("rightPanel.attention")}
									className="absolute top-1 right-0.5 h-1.5 w-1.5 animate-pulse rounded-full bg-warn"
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
				{bp === "expanded" && shown !== "info" && (
					<IconButton
						icon={max ? "minimize" : "maximize"}
						size={14}
						label={max ? t("rightPanel.restore") : t("rightPanel.maximize")}
						active={max}
						onClick={() => uiStore.getState().togglePanelMax()}
					/>
				)}
				{onClose && (
					<IconButton
						icon="close"
						size={14}
						label={t("common.close")}
						onClick={onClose}
					/>
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
					</Suspense>
				)}
			</div>
		</aside>
	);
}
