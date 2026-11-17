/**
 * 助手的电脑 `/assistant/computer`（M19 步骤 4.4）：全局助手长期使用的一台 orb，主线与所有对话共用。
 *
 * 还没有电脑时：说明 + 「启动」（`POST assistant/computer/wake`，没有则创建并等到就绪）。
 * 有电脑时：状态卡（状态、provider、类型；唤醒 / 暂停 / 重置，重置先弹确认）+ 两个页签：
 * 「文件」= `FileBrowser` + `orbFileSource(orbId)`（电脑不属于任何线程，请求不带 `thread`；没在运行时不浏览，免得把它唤醒），
 * 「桌面」= 与线程右面板同一个桌面内核 `OrbDesktop`，遮罩上的「唤醒」同样走 `POST assistant/computer/wake`（经同一个 mutation，状态随之刷新）。
 * 状态取 `GET assistant/computer`；orb 本体（桌面、实时状态）取 REST 列表叠加 WS 推送的实时 orb。
 * 页头与收件箱、设置页同形。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	useAssistantComputer,
	useAssistantComputerAction,
} from "../api/assistant-queries";
import type { Orb, OrbState } from "../api/client";
import { useLiveOrbs } from "../api/queries";
import { OrbDesktop } from "../components/desktop/DesktopPanel";
import { FileBrowser } from "../components/files/FileBrowser";
import {
	ChromeLead,
	WindowControls,
} from "../components/layout/WindowControls";
import { Card, KeyValue, Sheet } from "../components/ui/data";
import { Icon } from "../components/ui/icons";
import { OrbBead } from "../components/ui/OrbBead";
import {
	Button,
	Dialog,
	EmptyState,
	ErrorText,
	IconButton,
	Spinner,
	tabClass,
} from "../components/ui/primitives";
import { orbFileSource } from "../lib/file-source";
import { useBreakpoint } from "../lib/viewport";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";

type Tab = "files" | "desktop";
const TABS: readonly Tab[] = ["files", "desktop"];

function ResetDialog({
	busy,
	onClose,
	onConfirm,
}: {
	busy: boolean;
	onClose: () => void;
	onConfirm: () => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<Dialog
			open
			title={t("assistantComputer.resetTitle")}
			onClose={busy ? () => {} : onClose}
			footer={
				<>
					<Button onClick={onClose} disabled={busy}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="danger"
						icon="trash"
						disabled={busy}
						data-testid="computer-reset-confirm"
						onClick={onConfirm}
					>
						{t("assistantComputer.reset")}
					</Button>
				</>
			}
		>
			<p className="text-muted text-sm">{t("assistantComputer.resetBody")}</p>
		</Dialog>
	);
}

function FilesTab({
	orbId,
	state,
	canWake,
	onWake,
	waking,
}: {
	orbId: string;
	state: OrbState | undefined;
	canWake: boolean;
	onWake: () => void;
	waking: boolean;
}): ReactNode {
	const { t } = useTranslation();
	if (state !== "ready")
		return (
			<EmptyState icon="folder" title={t("assistantComputer.filesAsleep")}>
				{canWake ? (
					<Button
						variant="accent"
						icon="play"
						disabled={waking}
						onClick={onWake}
					>
						{t("assistantComputer.wake")}
					</Button>
				) : (
					<Spinner />
				)}
			</EmptyState>
		);
	return (
		<FileBrowser
			sources={[orbFileSource(orbId)]}
			storageKey="oryn.assistantComputer.files"
		/>
	);
}

export function AssistantComputerPage(): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const sidebarToggle = useSidebarToggleShown();
	const chromeLeft = !useSidebarShown();
	const status = useAssistantComputer();
	const action = useAssistantComputerAction();
	const orbs = useLiveOrbs();
	const [tab, setTab] = useState<Tab>("files");
	const [confirmReset, setConfirmReset] = useState(false);
	const computer = status.data;
	const orbId = computer?.orbId;
	const orb: Orb | undefined = orbId
		? orbs.find((o) => o.id === orbId)
		: undefined;
	// 实时 orb 的状态更新（WS 推送）先于状态接口的轮询
	const state = orb?.state ?? computer?.state;
	const pending = action.isPending ? action.variables : undefined;
	// 可以唤醒：冻结、停机、失败；就绪时才能暂停
	const canWake =
		state === "frozen" || state === "stopped" || state === "failed";

	let body: ReactNode;
	if (status.isPending)
		body = (
			<div className="flex justify-center pt-[12vh]">
				<Spinner size={20} />
			</div>
		);
	else if (status.isError) body = <ErrorText error={status.error} />;
	else if (!orbId)
		body = (
			<div data-testid="computer-empty" className="pt-[12vh]">
				<EmptyState icon="laptop" title={t("assistantComputer.noneTitle")}>
					<p className="max-w-md">{t("assistantComputer.noneHint")}</p>
					<Button
						variant="accent"
						icon="play"
						className="mt-3"
						disabled={pending !== undefined}
						onClick={() => action.mutate("wake")}
					>
						{pending === "wake"
							? t("assistantComputer.starting")
							: t("assistantComputer.start")}
					</Button>
					<ErrorText error={action.error} />
				</EmptyState>
			</div>
		);
	else
		body = (
			<div className="mx-auto flex min-h-0 w-full max-w-5xl flex-1 flex-col gap-3">
				<Card
					title={t("assistantComputer.statusTitle")}
					icon="laptop"
					action={
						<div className="flex items-center gap-1.5">
							<Button
								size="sm"
								icon="play"
								disabled={pending !== undefined || !canWake}
								onClick={() => action.mutate("wake")}
							>
								{t("assistantComputer.wake")}
							</Button>
							<Button
								size="sm"
								icon="pause"
								disabled={pending !== undefined || state !== "ready"}
								onClick={() => action.mutate("pause")}
							>
								{t("assistantComputer.pause")}
							</Button>
							<Button
								size="sm"
								variant="ghost"
								icon="refresh"
								disabled={pending !== undefined}
								onClick={() => setConfirmReset(true)}
							>
								{t("assistantComputer.reset")}
							</Button>
						</div>
					}
				>
					<div data-testid="computer-status">
						<KeyValue
							rows={[
								{
									key: "state",
									label: t("assistantComputer.state"),
									value: (
										<span
											data-testid="computer-state"
											className="inline-flex items-center gap-1.5"
										>
											{state && <OrbBead state={state} size={12} />}
											{state ? t(`orbState.${state}`) : "—"}
											{pending && <Spinner size={11} />}
										</span>
									),
								},
								{
									key: "provider",
									label: t("assistantComputer.provider"),
									value: computer?.providerName ?? "—",
								},
								{
									key: "type",
									label: t("assistantComputer.type"),
									value: computer?.type ?? "—",
								},
							]}
						/>
						{!computer?.toolsReady && (
							<p className="mt-2 text-faint text-xs">
								{t("assistantComputer.toolsPending")}
							</p>
						)}
						<ErrorText error={action.error} />
					</div>
				</Card>
				<div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border border-border bg-panel">
					<div
						role="tablist"
						aria-label={t("assistantComputer.title")}
						className="flex h-10 shrink-0 items-center gap-0.5 border-border border-b px-2"
					>
						{TABS.map((k) => (
							<button
								key={k}
								type="button"
								role="tab"
								aria-selected={tab === k}
								data-tab={k}
								onClick={() => setTab(k)}
								className={`${tabClass(tab === k)} ${tab === k ? "bg-hover" : ""}`}
							>
								<Icon name={k === "files" ? "folder" : "monitor"} size={13} />
								{t(`assistantComputer.tabs.${k}`)}
							</button>
						))}
					</div>
					<div role="tabpanel" className="flex min-h-[420px] flex-1 flex-col">
						{tab === "files" ? (
							<FilesTab
								orbId={orbId}
								state={state}
								canWake={canWake}
								waking={pending !== undefined}
								onWake={() => action.mutate("wake")}
							/>
						) : orb ? (
							<OrbDesktop orb={orb} onWake={() => action.mutateAsync("wake")} />
						) : (
							<div className="flex flex-1 items-center justify-center">
								<Spinner />
							</div>
						)}
					</div>
				</div>
			</div>
		);

	return (
		<Sheet inset={bp === "expanded"} className="flex h-full min-h-0 flex-col">
			<header
				data-window-drag=""
				className="flex h-12 shrink-0 items-center gap-2 border-border border-b px-3"
			>
				{chromeLeft && <ChromeLead />}
				{sidebarToggle && (
					<IconButton
						icon={bp === "expanded" ? "sidebar" : "menu"}
						label={t("nav.sidebar")}
						onClick={() => uiStore.getState().toggleSidebar()}
					/>
				)}
				<Icon name="laptop" size={16} className="shrink-0 text-faint" />
				<h1 className="min-w-0 flex-1 truncate font-semibold text-sm">
					{t("assistantComputer.title")}
				</h1>
				<WindowControls side="right" className="-mr-1" />
			</header>
			<div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-3 pt-4 pb-safe sm:px-6 sm:pt-6">
				{body}
			</div>
			{confirmReset && (
				<ResetDialog
					busy={pending === "reset"}
					onClose={() => setConfirmReset(false)}
					onConfirm={() =>
						action.mutate("reset", {
							onSettled: () => setConfirmReset(false),
						})
					}
				/>
			)}
		</Sheet>
	);
}
