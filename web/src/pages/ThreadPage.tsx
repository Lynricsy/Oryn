/** 线程页：订阅线程、渲染转录 / 输入区 / 停靠对话框 / 右侧面板（Info / Changes / Files / Terminal）。 */
import {
	type KeyboardEvent,
	type PointerEvent,
	type ReactNode,
	useEffect,
	useMemo,
} from "react";
import { useTranslation } from "react-i18next";
import { useParams } from "react-router";
import { Composer } from "../components/composer/Composer";
import {
	PlanProposalDialog,
	ThreadDialogs,
} from "../components/thread/Dialogs";
import { EnvBanner } from "../components/thread/EnvBanner";
import { BelowEditorWidgets, ModeBar } from "../components/thread/ModeBar";
import { QueueBar } from "../components/thread/QueueBar";
import { RightPanel } from "../components/thread/RightPanel";
import {
	StatusBar,
	ThreadHeader,
	useOrbOf,
} from "../components/thread/ThreadHeader";
import { UiRequestCard } from "../components/thread/UiRequestCard";
import {
	TranscriptContext,
	type TranscriptContextValue,
} from "../components/transcript/context";
import { Transcript } from "../components/transcript/Transcript";
import { Spinner } from "../components/ui/primitives";
import { useBreakpoint } from "../lib/viewport";
import { realtime } from "../realtime/socket";
import { useConnection, useIndex, useThread } from "../state/stores";
import { terminalHub } from "../state/terminals";
import { buildTranscript } from "../state/transcript";
import { PANEL_MIN_WIDTH, uiStore, useUi } from "../state/ui";

const INFO_PANEL_WIDTH = 320;

/** 面板左缘拖拽条：拖动改宽度（面板贴右缘，宽度 = 视口右缘 − 指针），方向键微调。 */
function PanelResizer() {
	const { t } = useTranslation();
	const width = useUi((s) => s.panelWidth);
	const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
		e.preventDefault();
		const el = e.currentTarget;
		el.setPointerCapture(e.pointerId);
		const move = (ev: globalThis.PointerEvent) =>
			uiStore.getState().setPanelWidth(window.innerWidth - ev.clientX);
		const up = () => {
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
			className="absolute inset-y-0 -left-1 z-10 w-2 cursor-col-resize touch-none outline-none hover:bg-accent/30 focus-visible:bg-accent/40"
			onPointerDown={onPointerDown}
			onKeyDown={onKeyDown}
		/>
	);
}

function WorkingFooter({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const running = useThread(threadId, (s) => s.running);
	const live = useThread(
		threadId,
		(s) => s.liveMessage && !s.liveMessage.ended,
	);
	const state = useThread(threadId, (s) => s.thread?.state);
	const agentStatus = useThread(threadId, (s) => s.agentStatus);
	const orb = useOrbOf(threadId);
	if (state === "provisioning") {
		return (
			<div className="flex items-center gap-2 py-3 text-muted text-sm">
				<Spinner />
				{t("thread.provisioning", {
					state: orb ? t(`orbState.${orb.state}`) : "…",
				})}
			</div>
		);
	}
	if (agentStatus === "exited") {
		return (
			<div className="py-3 text-sm text-warn">{t("thread.agentExited")}</div>
		);
	}
	if (running && !live) {
		return (
			<div className="flex items-center gap-2 py-3 text-muted text-sm">
				<Spinner />
				{t("thread.working")}
			</div>
		);
	}
	return <div className="h-4" />;
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

	const showPanelColumn = bp === "expanded" && panelOpen;
	const showPanelDrawer = bp !== "expanded" && panelOpen;
	const panelFull = showPanelColumn && panelMax && panelTab !== "info";
	const firstUi = pendingUi[0];

	return (
		<div className="flex h-full min-w-0 flex-1">
			<div
				className={`h-full min-w-0 flex-1 flex-col ${panelFull ? "hidden" : "flex"}`}
			>
				<ThreadHeader threadId={threadId} sidebarToggle={bp !== "expanded"} />
				{conn !== "open" && (
					<div className="border-border border-b bg-raised px-3 py-1 text-center text-muted text-xs">
						{t(`connection.${conn}`)}
					</div>
				)}
				<TranscriptContext.Provider value={ctx}>
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
						/>
					)}
				</TranscriptContext.Provider>
				<div className="shrink-0 px-3 pt-1 pb-keyboard sm:px-5">
					<div className="pb-2">
						{plan && (
							<PlanProposalDialog
								key={threadId}
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
						<EnvBanner key={threadId} threadId={threadId} />
						<QueueBar threadId={threadId} />
						<ModeBar threadId={threadId} />
						<Composer key={threadId} threadId={threadId} disabled={orbGone} />
						<BelowEditorWidgets threadId={threadId} />
						<StatusBar threadId={threadId} />
					</div>
				</div>
			</div>
			{showPanelColumn && (
				<div
					className={`relative h-full min-w-0 border-border border-l ${panelFull ? "flex-1" : "shrink-0"}`}
					style={
						panelFull
							? undefined
							: {
									width: panelTab === "info" ? INFO_PANEL_WIDTH : panelWidth,
								}
					}
				>
					{panelTab !== "info" && !panelFull && <PanelResizer />}
					<RightPanel
						threadId={threadId}
						onClose={() => uiStore.getState().setPanel(false)}
					/>
				</div>
			)}
			{showPanelDrawer && (
				<div className="fixed inset-0 z-40 flex justify-end">
					<button
						type="button"
						aria-label={t("common.close")}
						className="absolute inset-0 bg-black/40"
						onClick={() => uiStore.getState().setPanel(false)}
					/>
					<div
						className={
							panelTab === "info"
								? "relative h-full w-full max-w-sm pt-safe shadow-xl"
								: "fixed inset-0 bg-panel pt-safe pr-safe pl-safe"
						}
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
