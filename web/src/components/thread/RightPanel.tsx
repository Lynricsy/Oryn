/**
 * 右侧面板（计划 M3 2.5）：标签 `Info | Changes | Files | Terminal`。Info 为原信息面板；其余三个按需加载。
 * Terminal 标签在有终端等待用户（agent 的 `pty:true` 命令、会话要密码）时亮注意点。
 */
import { lazy, type ReactNode, Suspense } from "react";
import { useTranslation } from "react-i18next";
import { useBreakpoint } from "../../lib/viewport";
import { useThread } from "../../state/stores";
import { useTerminals } from "../../state/terminals";
import { type PanelTab, uiStore, useUi } from "../../state/ui";
import { IconButton, Spinner } from "../ui/primitives";
import { InfoPanel } from "./InfoPanel";

const ChangesPanel = lazy(() => import("../changes/ChangesPanel"));
const FilesPanel = lazy(() => import("../files/FilesPanel"));
const TerminalPanel = lazy(() => import("../terminal/TerminalPanel"));

const TABS: readonly PanelTab[] = ["info", "changes", "files", "terminal"];

export function RightPanel({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose?: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const tab = useUi((s) => s.panelTab);
	const max = useUi((s) => s.panelMax);
	const bp = useBreakpoint();
	const orbId = useThread(threadId, (s) => s.thread?.orbId);
	const attention = useTerminals(threadId, (s) => s.attention);

	return (
		<aside
			className="flex h-full w-full min-w-0 flex-col bg-panel"
			aria-label={t("rightPanel.label")}
		>
			<div className="flex shrink-0 items-center gap-1 border-border border-b px-2 py-1.5">
				<div
					role="tablist"
					aria-label={t("rightPanel.label")}
					className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto"
				>
					{TABS.map((id) => (
						<button
							key={id}
							type="button"
							role="tab"
							aria-selected={tab === id}
							data-panel-tab={id}
							className={`relative h-7 shrink-0 rounded-md px-2.5 font-medium text-xs transition-colors ${tab === id ? "bg-hover text-fg" : "text-muted hover:text-fg"}`}
							onClick={() => uiStore.getState().setPanelTab(id)}
						>
							{t(`rightPanel.tabs.${id}`)}
							{id === "terminal" && attention && tab !== "terminal" && (
								<span
									data-testid="terminal-attention"
									title={t("rightPanel.attention")}
									className="absolute top-1 right-0.5 h-1.5 w-1.5 animate-pulse rounded-full bg-warn"
								/>
							)}
						</button>
					))}
				</div>
				{bp === "expanded" && tab !== "info" && (
					<IconButton
						icon={max ? "chevronRight" : "panel"}
						label={max ? t("rightPanel.restore") : t("rightPanel.maximize")}
						active={max}
						onClick={() => uiStore.getState().togglePanelMax()}
					/>
				)}
				{onClose && (
					<IconButton
						icon="close"
						label={t("common.close")}
						onClick={onClose}
					/>
				)}
			</div>
			<div className="min-h-0 flex-1">
				{tab === "info" ? (
					<InfoPanel threadId={threadId} />
				) : (
					<Suspense
						fallback={
							<div className="flex h-full items-center justify-center text-faint">
								<Spinner />
							</div>
						}
					>
						{tab === "changes" && (
							<ChangesPanel threadId={threadId} orbId={orbId} />
						)}
						{tab === "files" && (
							<FilesPanel threadId={threadId} orbId={orbId} />
						)}
						{tab === "terminal" && <TerminalPanel threadId={threadId} />}
					</Suspense>
				)}
			</div>
		</aside>
	);
}
