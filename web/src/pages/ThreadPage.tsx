/**
 * 线程页：订阅线程；对话纸面里转录铺满，玻璃页头与玻璃输入坞绝对定位盖在其上（内容从玻璃下方透出），
 * 输入坞高度驱动转录底部留白；右侧面板（Info / Changes / Files / Terminal）是另一张纸面。
 * 三种布局：expanded 三栏（面板列可关、可最大化、可拖宽）；窄屏（compact / medium）转录与面板各标签是同级标签，
 * medium 在页头下方一行标签条，compact（手机）不占这一行、改由页头右上角的视图菜单切换；切到面板标签时转录纸面
 * 隐藏但保持挂载（滚动位置不丢）；折叠屏双段视口左段转录、右段面板常驻。
 * 页面主体是 `ThreadScreen`：Space 页与助手页以 `variant="coordinator"` 复用它（没有右面板与面板开关、页头由调用方给、
 * 输入区是协调档）。协调对话（`kind` 为 space / assistant）经 `/t/<id>` 打开时重定向到对应页面（通知与深链接仍指向 `/t/<id>`）。
 */
import {
	type CSSProperties,
	type KeyboardEvent,
	type PointerEvent,
	type ReactNode,
	useCallback,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { Navigate, useParams } from "react-router";
import { useSpaces } from "../api/space-queries";
import { Composer } from "../components/composer/Composer";
import { DockPill, useDockCollapse } from "../components/composer/DockPill";
import {
	dockArrived,
	takeArrival,
	useDockMorph,
} from "../components/composer/DockTray";
import { CompactThreadHeader } from "../components/thread/CompactThreadHeader";
import {
	PlanProposalDialog,
	ThreadDialogs,
} from "../components/thread/Dialogs";
import { EnvBanner } from "../components/thread/EnvBanner";
import {
	AboveEditorWidgets,
	BelowEditorWidgets,
} from "../components/thread/ModeBar";
import { QueueBar } from "../components/thread/QueueBar";
import {
	RightPanel,
	ThreadTabStrip,
	ThreadViewMenu,
	useThreadTabs,
} from "../components/thread/RightPanel";
import { RunnerOfflineNotice } from "../components/thread/RunnerOfflineNotice";
import {
	COMPACT_HEADER_VEIL,
	HEADER_VEIL,
	ThreadHeader,
} from "../components/thread/ThreadHeader";
import { UiRequestCard } from "../components/thread/UiRequestCard";
import {
	TranscriptContext,
	type TranscriptContextValue,
} from "../components/transcript/context";
import { Transcript } from "../components/transcript/Transcript";
import { WorkingFooter } from "../components/transcript/WorkingFooter";
import { Sheet } from "../components/ui/data";
import { Spinner } from "../components/ui/primitives";
import { usePresence } from "../lib/presence";
import { useBreakpoint, useViewportSegments } from "../lib/viewport";
import { realtime } from "../realtime/socket";
import { useComposerDraft } from "../state/composer-drafts";
import { useConnection, useIndex, useThread } from "../state/stores";
import { terminalHub } from "../state/terminals";
import { buildTranscript } from "../state/transcript";
import { PANEL_MIN_WIDTH, uiStore, useSidebarShown, useUi } from "../state/ui";

/** 面板列收起 / 展开的时长，与左栏（AppShell）一致 */
const PANEL_ANIM_MS = 260;
/** 窄屏同级标签条那一行的高度（px，medium）：转录顶部留白与页头纱都加上它。 */
export const TAB_ROW = 40;
/** 贴屏幕边缘表面的同心圆角（`index.css` 的 `.dc-*`）：纸面 / 托盘都是 xl 圆角。 */
export const DC_SHEET_RIGHT =
	"dc-tr dc-br [--dc-r:var(--r-xl)] [--dc-gap:0.5rem]";
const DC_DOCK_BOTTOM = "dc-bl dc-br [--dc-r:var(--r-xl)] [--dc-gap:0.75rem]";

/** 面板左缘拖拽条：拖动改宽度（面板贴右缘，宽度 = 视口右缘 − 指针），方向键微调。 */
function PanelResizer() {
	const { t } = useTranslation();
	const width = useUi((s) => s.panelWidth);
	const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
		e.preventDefault();
		const el = e.currentTarget;
		el.setPointerCapture(e.pointerId);
		// 拖动期间关掉面板列的宽度过渡，宽度跟手
		const column = el.parentElement;
		column?.classList.add("is-resizing");
		const move = (ev: globalThis.PointerEvent) =>
			uiStore.getState().setPanelWidth(window.innerWidth - ev.clientX);
		const up = () => {
			column?.classList.remove("is-resizing");
			el.removeEventListener("pointermove", move);
			el.removeEventListener("pointerup", up);
			el.removeEventListener("pointercancel", up);
		};
		el.addEventListener("pointermove", move);
		el.addEventListener("pointerup", up);
		el.addEventListener("pointercancel", up);
	};
	const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
		const step = e.shiftKey ? 64 : 16;
		if (e.key === "ArrowLeft") uiStore.getState().setPanelWidth(width + step);
		else if (e.key === "ArrowRight")
			uiStore.getState().setPanelWidth(width - step);
		else return;
		e.preventDefault();
	};
	return (
		// biome-ignore lint/a11y/useSemanticElements: 可聚焦的分隔条（WAI-ARIA window splitter），<hr> 不能交互
		<div
			role="separator"
			aria-orientation="vertical"
			aria-label={t("rightPanel.resize")}
			aria-valuenow={width}
			aria-valuemin={PANEL_MIN_WIDTH}
			tabIndex={0}
			className="absolute inset-y-0 -left-2 z-10 w-2 cursor-col-resize touch-none rounded-full hover:bg-accent/30 focus-visible:bg-accent/40"
			onPointerDown={onPointerDown}
			onKeyDown={onKeyDown}
		/>
	);
}

/** 页头的布局参数（`ThreadScreen` 交给调用方给的页头）。 */
export interface ScreenHeaderLayout {
	/** 非 expanded 宽度时在页头左侧显示侧边栏开关 */
	sidebarToggle: boolean;
	/** 渐进模糊纱的高度 */
	veil: number;
	chromeLeft: boolean;
	chromeRight: boolean;
}

export function ThreadPage(): ReactNode {
	const { threadId = "" } = useParams();
	const kind = useIndex((s) => s.threads[threadId]?.kind);
	if (kind === "assistant")
		return <Navigate to={`/assistant/${threadId}`} replace />;
	if (kind === "space") return <SpaceChatRedirect threadId={threadId} />;
	return <ThreadScreen threadId={threadId} variant="thread" />;
}

/** Space 聊天经 `/t/<id>` 打开：跳到所属 Space 页；找不到所属 Space 时照常显示线程。 */
function SpaceChatRedirect({ threadId }: { threadId: string }): ReactNode {
	const spaceOf = useIndex((s) => s.threads[threadId]?.spaceId);
	const spaces = useSpaces();
	const id =
		spaceOf ?? spaces.data?.spaces?.find((s) => s.threadId === threadId)?.id;
	if (id) return <Navigate to={`/space/${id}`} replace />;
	// Space 列表还在路上：先不渲染（不订阅一个马上要离开的线程）
	if (spaces.isPending) return null;
	return <ThreadScreen threadId={threadId} variant="thread" />;
}

/** 线程页最近一次「进入」的线程与当前挂着的线程页数：区分真正切换线程与布局跨断点引起的重新挂载。 */
let shownThread: string | null = null;
let shownCount = 0;

export function ThreadScreen({
	threadId,
	variant,
	header,
	edgeRight = true,
	subheader,
	disabled = false,
	dockNotice,
}: {
	threadId: string;
	/** `coordinator`：协调对话（Space 聊天、助手对话）——没有右面板与面板开关，页头由 `header` 给，输入区为协调档 */
	variant: "thread" | "coordinator";
	header?: (layout: ScreenHeaderLayout) => ReactNode;
	/** 纸面是不是屏幕最右侧的表面（Space 页右侧还有一列时为 false） */
	edgeRight?: boolean;
	/** 页头下方一行（40px，同级标签条的位置），如 Space 页窄屏的「聊天 | 线程」标签 */
	subheader?: ReactNode;
	/** 输入区不可用（协调宿主不可用时只读浏览） */
	disabled?: boolean;
	/** 输入坞托盘里、输入区之上的一节提示 */
	dockNotice?: ReactNode;
}): ReactNode {
	const coordinator = variant === "coordinator";
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const segments = useViewportSegments();
	const threadTab = useUi((s) => s.threadTab);
	const panelOpen = useUi((s) => s.panelOpen);
	const panelTab = useUi((s) => s.panelTab);
	const panelWidth = useUi((s) => s.panelWidth);
	const panelMax = useUi((s) => s.panelMax);
	const sidebarShown = useSidebarShown();
	const conn = useConnection((s) => s.status);
	const known = useIndex((s) => Boolean(s.threads[threadId]));
	const entries = useThread(threadId, (s) => s.entries);
	const leaf = useThread(threadId, (s) => s.leafEntryId);
	const liveMessage = useThread(threadId, (s) => s.liveMessage);
	const liveTools = useThread(threadId, (s) => s.liveTools);
	const liveToolOrder = useThread(threadId, (s) => s.liveToolOrder);
	const notices = useThread(threadId, (s) => s.notices);
	const subagents = useThread(threadId, (s) => s.subagents);
	const pendingUi = useThread(threadId, (s) => s.pendingUi);
	const plan = useThread(threadId, (s) => s.planProposal);
	const snapshotReceived = useThread(threadId, (s) => s.snapshotReceived);
	const orbGone = useThread(
		threadId,
		(s) => s.snapshotReceived && !s.thread?.orbId,
	);

	useEffect(() => {
		if (!threadId) return;
		// 先挂终端跟踪再订阅：快照到达时 terminalHub 已就位
		const detach = terminalHub.attach(threadId);
		const unsub = realtime.subscribe(threadId);
		realtime.focus(threadId);
		return () => {
			detach();
			unsub();
			realtime.focus(null);
		};
	}, [threadId]);

	// 切换线程（或从别处进入线程）时关闭上一线程的对话框，窄屏回到对话标签。布局跨断点（如手机 ↔ 平板）时外壳换了
	// 一套结构，线程页会在同一次提交里卸载再挂载：同一线程不算进入，打开的对话框与当前标签保持不变
	useEffect(() => {
		shownCount++;
		if (shownThread !== threadId) {
			const ui = uiStore.getState();
			ui.close();
			ui.setThreadTab("agent");
			shownThread = threadId;
		}
		return () => {
			shownCount--;
			// 卸载与重新挂载的 effect 在同一轮里先后执行；之后仍没有线程页挂着才算离开
			queueMicrotask(() => {
				if (shownCount === 0) shownThread = null;
			});
		};
	}, [threadId]);

	const model = useMemo(
		() =>
			buildTranscript({
				entries,
				leafEntryId: leaf,
				liveMessage,
				liveTools,
				liveToolOrder,
				notices,
			}),
		[entries, leaf, liveMessage, liveTools, liveToolOrder, notices],
	);
	const ctx: TranscriptContextValue = useMemo(
		() => ({
			threadId,
			toolResults: model.toolResults,
			liveTools: model.liveTools,
			subagents,
			openSubagent: (id) => uiStore.getState().open({ kind: "subagent", id }),
			forkFrom: (entryId) => uiStore.getState().open({ kind: "fork", entryId }),
			labelEntry: (entryId) =>
				uiStore.getState().open({ kind: "label", entryId }),
		}),
		[threadId, model.toolResults, model.liveTools, subagents],
	);

	const dual = segments !== null && !coordinator;
	const expanded = bp === "expanded" && !dual;
	// 窄屏：转录（agent）与面板各标签是同级标签（协调对话没有面板）
	const peer = !expanded && !dual && !coordinator;
	const onAgent = !peer || threadTab === "agent";
	const { tabs: peerTabs } = useThreadTabs(threadId, true);
	// 手机不常驻标签条（视图菜单在页头右上角）；medium 仍是页头下方一行标签条
	const tabRow = peer && bp !== "compact";
	// 手机线程页头是悬浮的三件式（侧栏钮 / 标题胶囊 / 视图钮，比宽页头高）；协调对话的页头由调用方给，仍是 48px
	const compactHeader = bp === "compact" && !dual && !header;
	const headerVeil = compactHeader ? COMPACT_HEADER_VEIL : HEADER_VEIL;
	const topInset = tabRow || subheader ? headerVeil + TAB_ROW : headerVeil;
	const showPanelColumn = expanded && panelOpen && !coordinator;
	const panelFull = showPanelColumn && panelMax && panelTab !== "info";
	/** 转录被面板标签或最大化面板挡住（保持挂载、仍参与布局，见下面纸面的注释）。 */
	const away = panelFull || !onAgent;
	// 面板列的进出场：打开时挂载后下一帧再展开，收起时先走完 260ms 的收窄再卸载（与左栏同一套时长与缓动）
	const column = usePresence(showPanelColumn, PANEL_ANIM_MS);
	const firstUi = pendingUi[0];
	// 桌面 App 无系统标题栏：左侧窗口控件归最左侧表面（侧栏没占着左上角时），右侧自绘按钮归最右侧表面。
	// 转录可见时它在最左；面板列展开时面板在最右，最大化时面板也在最左；窄屏切到面板标签时同级面板两端都占
	const chromeLeft = !sidebarShown && !dual;
	const transcriptShown = !panelFull && onAgent;

	// 输入坞高度（含软键盘内边距）→ 转录底部留白，最后一条消息不被玻璃托盘遮住。
	// 收成胶囊时不改留白（改了会让内容高度跳动、把阅读位置推到底部又触发展开），只另记胶囊那一档的高度给「回到底部」定位
	const dockRef = useRef<HTMLDivElement>(null);
	const trayRef = useRef<HTMLDivElement>(null);
	const bodyRef = useRef<HTMLDivElement>(null);
	const pillRowRef = useRef<HTMLSpanElement>(null);
	const [dockH, setDockH] = useState(0);
	const [miniH, setMiniH] = useState(0);
	const measureDock = useCallback(() => {
		const el = dockRef.current;
		const tray = trayRef.current;
		if (!el || tray?.hasAttribute("data-morphing")) return;
		if (tray?.dataset.dock === "mini") setMiniH(el.offsetHeight);
		else setDockH(el.offsetHeight);
	}, []);
	useLayoutEffect(() => {
		const el = dockRef.current;
		if (!el) return;
		measureDock();
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(measureDock);
		ro.observe(el, { box: "border-box" });
		return () => ro.disconnect();
	}, [measureDock]);

	// 手机：往上翻历史时输入坞收成一条胶囊（DockPill），往下翻回来、回到底部或点胶囊时展开
	const collapsible = bp === "compact" && !dual;
	const [miniWanted, setMini] = useState(false);
	const mini = collapsible && miniWanted;
	const [draft, setDraft] = useState(false);
	// biome-ignore lint/correctness/useExhaustiveDependencies: 换线程或离开手机宽度时回到展开
	useEffect(() => {
		setMini(false);
	}, [threadId, collapsible]);
	const onReadDirection = useCallback((dir: "back" | "forward") => {
		if (dir === "forward") {
			setMini(false);
			return;
		}
		const dock = dockRef.current;
		setDraft(Boolean(dock?.querySelector("textarea")?.value.trim()));
		// 收起前交出焦点：软键盘随之收起，收进胶囊的输入框不留着焦点
		const focused = document.activeElement;
		if (focused instanceof HTMLElement && dock?.contains(focused))
			focused.blur();
		setMini(true);
	}, []);
	// omp 带回草稿（会话树跳转、set_editor_text）时展开，免得字填进了看不见的输入框
	const editorText = useThread(threadId, (s) => s.editorText);
	useEffect(() => {
		if (editorText) setMini(false);
	}, [editorText]);
	// 引用回复带进输入区时同样展开：引用条与光标都在托盘里
	const quote = useComposerDraft(threadId, (d) => d.quote);
	useEffect(() => {
		if (quote) setMini(false);
	}, [quote]);
	useDockCollapse({
		mini,
		trayRef,
		bodyRef,
		measureRef: pillRowRef,
		onSettle: measureDock,
	});

	// 首条消息过渡（DockTray）：挂载即告诉过渡新状态已就绪；变形期间托盘挂 view-transition-name，
	// 结束后在有键盘鼠标的设备上把焦点交给输入框（触屏不抢焦点，免得软键盘收起又弹出）
	const morph = useDockMorph();
	const arrival = useRef(false);
	useLayoutEffect(() => {
		if (takeArrival()) arrival.current = true;
		dockArrived();
	}, []);
	useEffect(() => {
		if (!arrival.current || morph !== "idle") return;
		arrival.current = false;
		if (!window.matchMedia?.("(pointer: fine)").matches) return;
		dockRef.current?.querySelector("textarea")?.focus({ preventScroll: true });
	}, [morph]);

	return (
		<div className="relative flex h-full min-w-0 flex-1">
			{/*
			 * 对话纸面不做圆角裁剪（overflow-visible）：Chromium 里圆角 overflow 裁剪的后代 backdrop-filter 上的 mask-image
			 * 会被忽略，页头渐进模糊就成了整条硬切的色带。圆角裁剪只给转录（下面的 morph-late 层），页头纱与输入坞渐隐各自收角。
			 * 外层只管布局：最大化面板或窄屏切到面板标签时「切走」（保持挂载），双段视口里固定为左段宽。
			 * 切走不用 display:none：那会让 react-virtual 把在屏行测成 0、总高缩水，恢复显示时滚动偏移被钳住、
			 * 再被测高补偿推走（M7 A9：阅读位置漂移几百到上千像素）。改为叠在原处、仍参与布局但透明且 inert，
			 * 行高与滚动偏移原样保留；贴底时新消息照常跟随。
			 */}
			<div
				data-testid="transcript-pane"
				data-pane={dual ? "primary" : undefined}
				inert={away}
				className={`h-full min-w-0 ${dual ? "" : "flex-1"} flex ${away ? "pointer-events-none absolute inset-0 opacity-0" : "relative"}`}
				style={dual ? { width: segments.left, flex: "none" } : undefined}
			>
				<Sheet
					inset={expanded}
					// expanded 下面板列没开时转录是最右侧纸面，右上 / 右下角离屏幕边 8px（壳的 p-2）
					className={`relative flex h-full min-w-0 flex-1 flex-col overflow-visible ${expanded && !showPanelColumn && edgeRight ? DC_SHEET_RIGHT : ""}`}
				>
					<TranscriptContext.Provider value={ctx}>
						{/* morph-late：首条消息过渡时，气泡与「正在启动 agent…」在托盘走完约六成时淡入 */}
						<div
							className={`morph-late flex min-h-0 flex-1 flex-col overflow-hidden ${expanded ? "rounded-[calc(var(--r-xl)-1px)]" : ""} ${expanded && !showPanelColumn && edgeRight ? "dc-tr dc-br [--dc-r:calc(var(--r-xl)_-_1px)] [--dc-gap:calc(0.5rem_+_1px)]" : ""}`}
						>
							{!snapshotReceived && entries.length === 0 ? (
								<div className="flex flex-1 items-center justify-center text-muted">
									{known || conn !== "open" ? (
										<Spinner size={18} />
									) : (
										t("thread.notFound")
									)}
								</div>
							) : (
								<Transcript
									items={model.items}
									footer={<WorkingFooter threadId={threadId} />}
									insetTop={topInset}
									insetBottom={dockH + 16}
									onReadDirection={collapsible ? onReadDirection : undefined}
									trayTop={mini && miniH > 0 ? miniH : undefined}
								/>
							)}
						</div>
					</TranscriptContext.Provider>
					<div className="absolute inset-x-0 top-0 z-30">
						{header ? (
							header({
								sidebarToggle: !expanded,
								veil: topInset,
								chromeLeft: chromeLeft && transcriptShown,
								chromeRight: transcriptShown && edgeRight && !dual,
							})
						) : compactHeader ? (
							<CompactThreadHeader
								threadId={threadId}
								veil={topInset}
								chromeLeft={chromeLeft && transcriptShown}
								chromeRight={transcriptShown}
								viewMenu={
									peer &&
									onAgent && (
										<ThreadViewMenu
											threadId={threadId}
											tabs={peerTabs}
											shown="agent"
											round
										/>
									)
								}
							/>
						) : (
							<ThreadHeader
								threadId={threadId}
								sidebarToggle={!expanded}
								veil={topInset}
								chromeLeft={chromeLeft && transcriptShown}
								chromeRight={transcriptShown && !showPanelColumn && !dual}
							/>
						)}
						{subheader && (
							<div className="veil-host relative flex h-10 items-center px-2 sm:px-3">
								{subheader}
							</div>
						)}
						{/* 同级标签条只在看转录时挂载：切到面板标签后由面板自己的标签条接手（改动的后台检查只跑一份）；
						    手机的视图菜单同理 */}
						{tabRow && onAgent && (
							<div className="veil-host relative flex h-10 items-center px-2 sm:px-3">
								<ThreadTabStrip
									threadId={threadId}
									tabs={peerTabs}
									shown="agent"
									onSelect={(id) => uiStore.getState().setThreadTab(id)}
								/>
							</div>
						)}
					</div>
					{conn !== "open" && (
						<div
							className="pointer-events-none absolute inset-x-0 z-20 flex justify-center px-3"
							style={{ top: topInset - 8 }}
						>
							<div className="glass flex items-center gap-2 rounded-full px-3 py-1 text-muted text-xs">
								<Spinner size={11} />
								{t(`connection.${conn}`)}
							</div>
						</div>
					)}
					<div
						ref={dockRef}
						// 输入坞整体独立悬浮：外层透明、不遮罩、不晕染，只有托盘本身是玻璃
						className="pointer-events-none absolute inset-x-0 bottom-0 z-20 px-3 pb-keyboard sm:px-5"
					>
						<div className="relative mx-auto w-full max-w-3xl pb-3">
							{/*
							 * 一整块玻璃托盘：各节透明无边框，节间只有发丝线；不裁剪，命令菜单要伸出去。
							 * compact 下托盘离屏幕左右与底边 12px（px-3 / pb-3），底部两角跟屏幕圆角同心；
							 * 往上翻历史时收成胶囊（data-dock="mini"，见 DockPill），托盘两侧空出来的地方不拦触摸
							 */}
							<div
								ref={trayRef}
								data-testid="dock-tray"
								data-dock={collapsible ? (mini ? "mini" : "open") : undefined}
								className={`glass glass-dock pointer-events-auto mx-auto rounded-xl ${collapsible ? DC_DOCK_BOTTOM : ""} ${morph === "dock" ? "dock-morphing" : ""}`}
							>
								<div
									ref={bodyRef}
									inert={mini}
									className="dock-body divide-y divide-[var(--glass-line)]"
								>
									{plan && (
										<PlanProposalDialog
											key={`plan:${threadId}`}
											threadId={threadId}
											proposal={plan}
										/>
									)}
									{firstUi && (
										<UiRequestCard
											key={firstUi.id}
											threadId={threadId}
											req={firstUi}
											queued={pendingUi.length - 1}
										/>
									)}
									{!coordinator && (
										<EnvBanner key={`env:${threadId}`} threadId={threadId} />
									)}
									{!coordinator && <RunnerOfflineNotice threadId={threadId} />}
									{dockNotice}
									<QueueBar threadId={threadId} />
									<AboveEditorWidgets threadId={threadId} />
									<Composer
										key={`composer:${threadId}`}
										threadId={threadId}
										disabled={disabled || (orbGone && !coordinator)}
										coordinator={coordinator}
									/>
									<BelowEditorWidgets threadId={threadId} />
								</div>
								{collapsible && (
									<DockPill
										threadId={threadId}
										bodyRef={bodyRef}
										measureRef={pillRowRef}
										draft={draft}
										shown={mini}
										onExpand={(e) => {
											setMini(false);
											// 键盘激活（detail 为 0）：焦点交给输入框，不留在要变成 inert 的胶囊上（等托盘内容解除 inert 后）；触屏不弹软键盘
											if (e.detail === 0)
												requestAnimationFrame(() =>
													dockRef.current
														?.querySelector("textarea")
														?.focus({ preventScroll: true }),
												);
										}}
									/>
								)}
							</div>
						</div>
					</div>
				</Sheet>
			</div>
			{peer && !onAgent && (
				<Sheet inset={false} className="h-full min-w-0 flex-1">
					<RightPanel
						threadId={threadId}
						peer
						chrome={{ left: chromeLeft, right: true }}
					/>
				</Sheet>
			)}
			{dual && (
				// 双段视口右段：隔着铰链常驻，不能关闭、不能最大化、不能拖宽
				<div
					data-pane="secondary"
					className="h-full min-w-0"
					style={{ marginLeft: segments.gap, flex: 1 }}
				>
					<Sheet inset={false} className="h-full">
						<RightPanel threadId={threadId} />
					</Sheet>
				</div>
			)}
			{column.mounted && (
				// 与左栏同一做法：外层宽度与左侧 8px 缝一起过渡，内层固定宽度，左缘贴着外层左缘随之滑入 / 滑出并淡入淡出；
				// 外层不裁剪，免得裁掉纸面描边与投影
				<div
					inert={!showPanelColumn}
					data-panel={column.shown ? "shown" : "collapsed"}
					className={`relative h-full min-w-0 transition-[width,margin-left] duration-[260ms] ease-out [&.is-resizing]:transition-none ${
						!column.shown
							? "ml-0 w-0 shrink-0"
							: panelFull
								? "ml-2 flex-1"
								: "ml-2 w-[var(--pw)] shrink-0"
					}`}
					style={{ "--pw": `${panelWidth}px` } as CSSProperties}
				>
					{column.shown && !panelFull && <PanelResizer />}
					<div
						className={`h-full transition-opacity duration-[260ms] ease-out ${panelFull ? "w-full" : "w-[var(--pw)]"} ${column.shown ? "opacity-100" : "opacity-0"}`}
					>
						{/* 面板列是最右侧纸面：右上 / 右下角离屏幕边 8px */}
						<Sheet inset className={`h-full ${DC_SHEET_RIGHT}`}>
							<RightPanel
								threadId={threadId}
								onClose={() => uiStore.getState().setPanel(false)}
								chrome={{
									left: chromeLeft && panelFull,
									right: showPanelColumn,
								}}
							/>
						</Sheet>
					</div>
				</div>
			)}
			<ThreadDialogs threadId={threadId} />
		</div>
	);
}
