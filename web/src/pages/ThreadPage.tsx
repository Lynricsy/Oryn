/**
 * 线程页：订阅线程；对话纸面里转录铺满，玻璃页头与玻璃输入坞绝对定位盖在其上（内容从玻璃下方透出），
 * 输入坞高度驱动转录底部留白；右侧面板（Info / Changes / Files / Terminal）是另一张纸面。
 * 三种布局：expanded 三栏（面板列可关、可最大化、可拖宽）；窄屏（compact / medium）转录与面板各标签是同级标签，
 * 页头下方一行标签条，切到面板标签时转录纸面隐藏但保持挂载（滚动位置不丢）；折叠屏双段视口左段转录、右段面板常驻。
 */
import {
	type CSSProperties,
	type KeyboardEvent,
	type PointerEvent,
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useParams } from "react-router";
import { Composer } from "../components/composer/Composer";
import {
	dockArrived,
	takeArrival,
	useDockMorph,
} from "../components/composer/DockTray";
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
	useThreadTabs,
} from "../components/thread/RightPanel";
import { HEADER_VEIL, ThreadHeader } from "../components/thread/ThreadHeader";
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
import { useConnection, useIndex, useThread } from "../state/stores";
import { terminalHub } from "../state/terminals";
import { buildTranscript } from "../state/transcript";
import { PANEL_MIN_WIDTH, uiStore, useUi } from "../state/ui";

/** 面板列收起 / 展开的时长，与左栏（AppShell）一致 */
const PANEL_ANIM_MS = 260;
/** 窄屏同级标签条那一行的高度（px）：转录顶部留白与页头纱都加上它。 */
const TAB_ROW = 40;
/** 贴屏幕边缘表面的同心圆角（`index.css` 的 `.dc-*`）：纸面 / 托盘都是 xl 圆角。 */
const DC_SHEET_RIGHT = "dc-tr dc-br [--dc-r:var(--r-xl)] [--dc-gap:0.5rem]";
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

export function ThreadPage(): ReactNode {
	const { threadId = "" } = useParams();
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const segments = useViewportSegments();
	const threadTab = useUi((s) => s.threadTab);
	const panelOpen = useUi((s) => s.panelOpen);
	const panelTab = useUi((s) => s.panelTab);
	const panelWidth = useUi((s) => s.panelWidth);
	const panelMax = useUi((s) => s.panelMax);
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

	// biome-ignore lint/correctness/useExhaustiveDependencies: 切换线程时关闭上一线程的对话框，窄屏回到对话标签
	useEffect(() => {
		const ui = uiStore.getState();
		ui.close();
		ui.setThreadTab("agent");
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

	const dual = segments !== null;
	const expanded = bp === "expanded" && !dual;
	// 窄屏：转录（agent）与面板各标签是同级标签
	const peer = !expanded && !dual;
	const onAgent = !peer || threadTab === "agent";
	const { tabs: peerTabs } = useThreadTabs(threadId, true);
	const topInset = peer ? HEADER_VEIL + TAB_ROW : HEADER_VEIL;
	const showPanelColumn = expanded && panelOpen;
	const panelFull = showPanelColumn && panelMax && panelTab !== "info";
	// 面板列的进出场：打开时挂载后下一帧再展开，收起时先走完 260ms 的收窄再卸载（与左栏同一套时长与缓动）
	const column = usePresence(showPanelColumn, PANEL_ANIM_MS);
	const firstUi = pendingUi[0];

	// 输入坞高度（含软键盘内边距）→ 转录底部留白，最后一条消息不被玻璃托盘遮住
	const dockRef = useRef<HTMLDivElement>(null);
	const [dockH, setDockH] = useState(0);
	useLayoutEffect(() => {
		const el = dockRef.current;
		if (!el) return;
		setDockH(el.offsetHeight);
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(() => setDockH(el.offsetHeight));
		ro.observe(el, { box: "border-box" });
		return () => ro.disconnect();
	}, []);

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
		<div className="flex h-full min-w-0 flex-1">
			{/*
			 * 对话纸面不做圆角裁剪（overflow-visible）：Chromium 里圆角 overflow 裁剪的后代 backdrop-filter 上的 mask-image
			 * 会被忽略，页头渐进模糊就成了整条硬切的色带。圆角裁剪只给转录（下面的 morph-late 层），页头纱与输入坞渐隐各自收角。
			 * 外层只管布局：最大化面板或窄屏切到面板标签时 hidden（保持挂载），双段视口里固定为左段宽。
			 */}
			<div
				data-testid="transcript-pane"
				data-pane={dual ? "primary" : undefined}
				className={`relative h-full min-w-0 ${dual ? "" : "flex-1"} ${panelFull || !onAgent ? "hidden" : "flex"}`}
				style={dual ? { width: segments.left, flex: "none" } : undefined}
			>
				<Sheet
					inset={expanded}
					// expanded 下面板列没开时转录是最右侧纸面，右上 / 右下角离屏幕边 8px（壳的 p-2）
					className={`relative flex h-full min-w-0 flex-1 flex-col overflow-visible ${expanded && !showPanelColumn ? DC_SHEET_RIGHT : ""}`}
				>
					<TranscriptContext.Provider value={ctx}>
						{/* morph-late：首条消息过渡时，气泡与「正在启动 agent…」在托盘走完约六成时淡入 */}
						<div
							className={`morph-late flex min-h-0 flex-1 flex-col overflow-hidden ${expanded ? "rounded-[calc(var(--r-xl)-1px)]" : ""} ${expanded && !showPanelColumn ? "dc-tr dc-br [--dc-r:calc(var(--r-xl)_-_1px)] [--dc-gap:calc(0.5rem_+_1px)]" : ""}`}
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
								/>
							)}
						</div>
					</TranscriptContext.Provider>
					<div className="absolute inset-x-0 top-0 z-30">
						<ThreadHeader
							threadId={threadId}
							sidebarToggle={!expanded}
							veil={topInset}
						/>
						{/* 同级标签条只在看转录时挂载：切到面板标签后由面板自己的标签条接手（改动的后台检查只跑一份） */}
						{peer && onAgent && (
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
							className={`pointer-events-none absolute inset-x-0 z-20 flex justify-center px-3 ${peer ? "top-24" : "top-14"}`}
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
						<div className="pointer-events-auto relative mx-auto w-full max-w-3xl pb-3">
							{/*
							 * 一整块玻璃托盘：各节透明无边框，节间只有发丝线；不裁剪，命令菜单要伸出去。
							 * compact 下托盘离屏幕左右与底边 12px（px-3 / pb-3），底部两角跟屏幕圆角同心
							 */}
							<div
								className={`glass glass-dock divide-y divide-[var(--glass-line)] rounded-xl ${bp === "compact" && !dual ? DC_DOCK_BOTTOM : ""} ${morph === "dock" ? "dock-morphing" : ""}`}
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
								<EnvBanner key={`env:${threadId}`} threadId={threadId} />
								<QueueBar threadId={threadId} />
								<AboveEditorWidgets threadId={threadId} />
								<Composer
									key={`composer:${threadId}`}
									threadId={threadId}
									disabled={orbGone}
								/>
								<BelowEditorWidgets threadId={threadId} />
							</div>
						</div>
					</div>
				</Sheet>
			</div>
			{peer && !onAgent && (
				<Sheet inset={false} className="h-full min-w-0 flex-1">
					<RightPanel threadId={threadId} peer />
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
							/>
						</Sheet>
					</div>
				</div>
			)}
			<ThreadDialogs threadId={threadId} />
		</div>
	);
}
