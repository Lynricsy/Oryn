/** 线程页：订阅线程、渲染转录 / 输入区 / 停靠对话框 / 信息面板。 */
import { type ReactNode, useEffect, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useParams } from "react-router";
import { Composer } from "../components/composer/Composer";
import {
	PlanProposalDialog,
	ThreadDialogs,
} from "../components/thread/Dialogs";
import { InfoPanel } from "../components/thread/InfoPanel";
import { BelowEditorWidgets, ModeBar } from "../components/thread/ModeBar";
import { QueueBar } from "../components/thread/QueueBar";
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
import { buildTranscript } from "../state/transcript";
import { uiStore, useUi } from "../state/ui";

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
		const unsub = realtime.subscribe(threadId);
		realtime.focus(threadId);
		return () => {
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
	const firstUi = pendingUi[0];

	return (
		<div className="flex h-full min-w-0 flex-1">
			<div className="flex h-full min-w-0 flex-1 flex-col">
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
						{plan && <PlanProposalDialog threadId={threadId} proposal={plan} />}
						{firstUi && (
							<UiRequestCard
								key={firstUi.id}
								threadId={threadId}
								req={firstUi}
								queued={pendingUi.length - 1}
							/>
						)}
						<QueueBar threadId={threadId} />
						<ModeBar threadId={threadId} />
						<Composer threadId={threadId} disabled={orbGone} />
						<BelowEditorWidgets threadId={threadId} />
						<StatusBar threadId={threadId} />
					</div>
				</div>
			</div>
			{showPanelColumn && (
				<div className="h-full w-80 shrink-0 border-border border-l">
					<InfoPanel threadId={threadId} />
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
					<div className="relative h-full w-full max-w-sm pt-safe shadow-xl">
						<InfoPanel
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
