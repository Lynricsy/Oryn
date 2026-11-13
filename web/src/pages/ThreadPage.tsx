/**
 * 线程页：订阅线程；对话纸面里转录铺满，玻璃页头与玻璃输入坞绝对定位盖在其上（内容从玻璃下方透出），
 * 输入坞高度驱动转录底部留白；右侧面板（Info / Changes / Files / Terminal）是另一张纸面。
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
import { RightPanel } from "../components/thread/RightPanel";
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
import { useBreakpoint } from "../lib/viewport";
import { realtime } from "../realtime/socket";
import { useConnection, useIndex, useThread } from "../state/stores";
import { terminalHub } from "../state/terminals";
import { buildTranscript } from "../state/transcript";
import { PANEL_MIN_WIDTH, uiStore, useUi } from "../state/ui";

/** 面板列收起 / 展开的时长，与左栏（AppShell）一致 */
const PANEL_ANIM_MS = 260;

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

	// biome-ignore lint/correctness/useExhaustiveDependencies: 切换线程时关闭上一线程的对话框
	useEffect(() => {
		uiStore.getState().close();
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

	const expanded = bp === "expanded";
	const showPanelColumn = expanded && panelOpen;
	const showPanelDrawer = !expanded && panelOpen;
	const panelFull = showPanelColumn && panelMax && panelTab !== "info";
	// 面板列与抽屉的进出场：打开时挂载后下一帧再展开，收起时先走完 260ms 的收窄 / 滑出再卸载（与左栏同一套时长与缓动）
	const column = usePresence(showPanelColumn, PANEL_ANIM_MS);
	const drawer = usePresence(showPanelDrawer, PANEL_ANIM_MS);
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
			 */}
			<Sheet
				inset={expanded}
				className={`relative h-full min-w-0 flex-1 flex-col overflow-visible ${panelFull ? "hidden" : "flex"}`}
			>
				<TranscriptContext.Provider value={ctx}>
					{/* morph-late：首条消息过渡时，气泡与「正在启动 agent…」在托盘走完约六成时淡入 */}
					<div
						className={`morph-late flex min-h-0 flex-1 flex-col overflow-hidden ${expanded ? "rounded-[calc(var(--r-xl)-1px)]" : ""}`}
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
								insetTop={HEADER_VEIL}
								insetBottom={dockH + 16}
							/>
						)}
					</div>
				</TranscriptContext.Provider>
				<div className="absolute inset-x-0 top-0 z-30">
					<ThreadHeader threadId={threadId} sidebarToggle={!expanded} />
				</div>
				{conn !== "open" && (
					<div className="pointer-events-none absolute inset-x-0 top-14 z-20 flex justify-center px-3">
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
						{/* 一整块玻璃托盘：各节透明无边框，节间只有发丝线；不裁剪，命令菜单要伸出去 */}
						<div
							className={`glass glass-dock divide-y divide-[var(--glass-line)] rounded-xl ${morph === "dock" ? "dock-morphing" : ""}`}
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
						<Sheet inset className="h-full">
							<RightPanel
								threadId={threadId}
								onClose={() => uiStore.getState().setPanel(false)}
							/>
						</Sheet>
					</div>
				</div>
			)}
			{drawer.mounted && (
				<div className="fixed inset-0 z-40">
					<button
						type="button"
						aria-label={t("common.close")}
						className={`absolute inset-0 bg-scrim/70 backdrop-blur-sm transition-opacity duration-[260ms] ease-out ${drawer.shown ? "opacity-100" : "opacity-0"}`}
						onClick={() => uiStore.getState().setPanel(false)}
					/>
					{/*
					 * 与左抽屉同一形态：悬浮圆角纸面，四周留 8px 与安全区，从右侧滑入 / 滑出。
					 * 宽度与标签无关：手机上铺满（减去边距），平板上停在 36rem，切标签不跳。
					 */}
					<div
						inert={!showPanelDrawer}
						className={`absolute top-[max(0.5rem,env(safe-area-inset-top))] right-[max(0.5rem,env(safe-area-inset-right))] bottom-[max(0.5rem,env(safe-area-inset-bottom))] left-2 ml-auto max-w-xl overflow-hidden rounded-2xl border border-border bg-panel shadow-xl transition-[translate,opacity] duration-[260ms] ease-out ${drawer.shown ? "translate-x-0 opacity-100" : "translate-x-[calc(100%+1rem)] opacity-0"}`}
					>
						<RightPanel
							threadId={threadId}
							onClose={() => uiStore.getState().setPanel(false)}
						/>
					</div>
				</div>
			)}
			<ThreadDialogs threadId={threadId} />
		</div>
	);
}
