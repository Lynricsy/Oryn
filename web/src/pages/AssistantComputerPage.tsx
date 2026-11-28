/**
 * 助手的电脑 `/assistant/computer`（M19 步骤 4.4）：全局助手长期使用的一台 orb，主线与所有对话共用。
 *
 * 还没有电脑时：说明 + 「启动」（`POST assistant/computer/wake`，没有则创建并等到就绪）。
 * 有电脑时，页头一行装下全部控制：标题旁的状态（珠 + 文字）、居中的页签、右侧的电源钮（就绪时「暂停」，
 * 冻结 / 停机 / 失败时「唤醒」）与更多菜单（复制 orb ID、重置；重置先弹确认）；compact 下页签另起一行。
 * 页签内容铺满页头以下的全部空间，所选页签记在 localStorage：
 * 「概览」= orb 徽记与规格、CPU / 内存曲线（`orbs/{id}/metrics`）、最近 24 小时的状态时间线（`orbs/{id}/timeline`）
 *   与详情（工具是否就绪、最近使用、镜像、版本）；
 * 「桌面」= 与线程右面板同一个桌面内核 `OrbDesktop`，遮罩上的「唤醒」同样走 `POST assistant/computer/wake`
 *   （经同一个 mutation，状态随之刷新）；
 * 「文件」= `FileBrowser` + `orbFileSource(orbId)`（电脑不属于任何线程，请求不带 `thread`；没在运行时不浏览，免得把它唤醒）。
 * 状态取 `GET assistant/computer`；orb 本体（桌面、实时状态）取 REST 列表叠加 WS 推送的实时 orb。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type AssistantComputer,
	useAssistantComputer,
	useAssistantComputerAction,
} from "../api/assistant-queries";
import type { Orb, OrbState } from "../api/client";
import { useLiveOrbs, useOrbMetrics } from "../api/queries";
import { useOrbTimeline } from "../api/stats-queries";
import { TimelineStrip } from "../components/charts/TimelineStrip";
import { OrbDesktop } from "../components/desktop/DesktopPanel";
import { FileBrowser } from "../components/files/FileBrowser";
import {
	ChromeLead,
	WindowControls,
} from "../components/layout/WindowControls";
import {
	InfoBlock,
	InfoTag,
	OrbMetricTiles,
	Spec,
} from "../components/thread/info-parts";
import { KeyValue, Sheet } from "../components/ui/data";
import { Icon, type IconName } from "../components/ui/icons";
import { DockerBadge } from "../components/ui/OrbBadges";
import { OrbBead } from "../components/ui/OrbBead";
import { OrbEmblem } from "../components/ui/OrbEmblem";
import {
	Button,
	Dialog,
	EmptyState,
	ErrorText,
	IconButton,
	Menu,
	Spinner,
	tabClass,
} from "../components/ui/primitives";
import { copyToClipboard } from "../lib/clipboard";
import { orbFileSource } from "../lib/file-source";
import {
	formatDateTime,
	formatOrbCpus,
	formatOrbMemory,
	formatRelative,
	formatUptime,
} from "../lib/format";
import { useBreakpoint } from "../lib/viewport";
import { showToast } from "../state/toasts";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";

type Tab = "overview" | "desktop" | "files";
const TABS: readonly Tab[] = ["overview", "desktop", "files"];
const TAB_ICON: Record<Tab, IconName> = {
	overview: "info",
	desktop: "monitor",
	files: "folder",
};

/** localStorage 键：上次停留的页签。 */
const TAB_KEY = "oryn.assistantComputer.tab";

function readTab(): Tab {
	try {
		const v = globalThis.localStorage?.getItem(TAB_KEY);
		return TABS.find((k) => k === v) ?? "overview";
	} catch {
		return "overview";
	}
}

function saveTab(tab: Tab) {
	try {
		globalThis.localStorage?.setItem(TAB_KEY, tab);
	} catch {
		// 隐私模式下 localStorage 不可用：只在本次会话生效
	}
}

const DAY = 24 * 3600_000;

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

/** 分段页签（`bg-inset` 槽 + `bg-panel` 滑块，同 `tabClass` 的约定）。 */
function ComputerTabs({
	tab,
	onSelect,
	className = "",
}: {
	tab: Tab;
	onSelect: (tab: Tab) => void;
	className?: string;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			role="tablist"
			aria-label={t("assistantComputer.title")}
			className={`flex shrink-0 gap-0.5 rounded-full bg-inset p-0.5 ${className}`}
		>
			{TABS.map((k) => (
				<button
					key={k}
					type="button"
					role="tab"
					aria-selected={tab === k}
					data-tab={k}
					onClick={() => onSelect(k)}
					className={`${tabClass(tab === k)} flex-1 justify-center ${tab === k ? "bg-panel shadow-xs" : ""}`}
				>
					<Icon name={TAB_ICON[k]} size={13} aria-hidden />
					{t(`assistantComputer.tabs.${k}`)}
				</button>
			))}
		</div>
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

/** 时间戳的相对写法，悬停给出绝对时间；没有时写「从未」。 */
function When({ ts }: { ts: number | undefined }): ReactNode {
	const { t } = useTranslation();
	if (!ts)
		return <span className="text-faint">{t("assistantComputer.never")}</span>;
	return <span title={formatDateTime(ts)}>{formatRelative(ts)}</span>;
}

function ComputerOverview({
	orbId,
	computer,
	orb,
	state,
}: {
	orbId: string;
	computer: AssistantComputer;
	orb: Orb | undefined;
	state: OrbState | undefined;
}): ReactNode {
	const { t, i18n } = useTranslation();
	const metrics = useOrbMetrics(state === "deleted" ? undefined : orbId);
	// 窗口在挂载时定下：终点在每次轮询时取 `min(to, 现在)`，页面开着时间线也会前进
	const [win] = useState(() => {
		const now = Date.now();
		return { from: now - DAY, to: now + DAY };
	});
	const timeline = useOrbTimeline(orbId, win.from, win.to);
	const samples = metrics.data?.samples ?? [];
	const stateLabel = state ? t(`orbState.${state}`) : "—";
	const changedAt = orb?.stateChangedAt;
	const subtitle =
		state === "ready" && changedAt
			? t("panel.uptime", {
					d: formatUptime(Date.now() - changedAt, i18n.language),
				})
			: changedAt
				? t("assistantComputer.since", { when: formatRelative(changedAt) })
				: undefined;
	const versions = [
		orb?.orbdVersion && `orbd ${orb.orbdVersion}`,
		orb?.agentVersion && `agent ${orb.agentVersion}`,
	].filter(Boolean);
	const rows = [
		{
			key: "tools",
			label: t("assistantComputer.tools"),
			value: computer.toolsReady ? (
				<InfoTag tone="ok">{t("assistantComputer.toolsReady")}</InfoTag>
			) : (
				<InfoTag tone="warn">{t("assistantComputer.toolsWaiting")}</InfoTag>
			),
		},
		{
			key: "agent",
			label: t("assistantComputer.lastAgent"),
			value: <When ts={orb?.lastAgentActivityAt} />,
		},
		{
			key: "user",
			label: t("assistantComputer.lastUser"),
			value: <When ts={orb?.lastUserInteractionAt} />,
		},
		...(orb
			? [
					{
						key: "created",
						label: t("assistantComputer.created"),
						value: formatDateTime(orb.createdAt),
					},
				]
			: []),
		// docker provider 不回填镜像名（生产上全是空串）：没有就不占一行
		...(orb?.image
			? [
					{
						key: "image",
						label: t("assistantComputer.image"),
						value: (
							<span className="break-all font-mono text-xs">{orb.image}</span>
						),
					},
				]
			: []),
		...(versions.length > 0
			? [
					{
						key: "versions",
						label: t("assistantComputer.versions"),
						value: (
							<span className="font-mono text-xs">{versions.join(" · ")}</span>
						),
					},
				]
			: []),
		{
			key: "id",
			label: t("assistantComputer.orbId"),
			value: <span className="break-all font-mono text-xs">{orbId}</span>,
		},
	];
	return (
		<div
			role="tabpanel"
			data-testid="computer-overview"
			className="min-h-0 flex-1 overflow-y-auto px-3 py-4 pb-safe sm:px-6 sm:py-6"
		>
			<div className="mx-auto grid w-full max-w-5xl items-start gap-3 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
				<div className="flex min-w-0 flex-col gap-3">
					<InfoBlock className="pb-4">
						<div className="flex items-center gap-3">
							<OrbEmblem
								state={state ?? "requested"}
								cpu={samples.map((s) => s.cpuPct)}
								fault={state === "failed"}
								className="-my-0.5 -ml-0.5"
							/>
							<div className="min-w-0 flex-1">
								<div className="truncate font-semibold text-2xl leading-[30px] tracking-tight">
									{stateLabel}
								</div>
								{subtitle && (
									<div className="truncate text-faint text-sm leading-[18px]">
										{subtitle}
									</div>
								)}
							</div>
						</div>
						{state === "failed" && orb?.failureReason && (
							<p className="mt-3 text-err text-xs">{orb.failureReason}</p>
						)}
						<div className="mt-3.5 flex flex-wrap items-center gap-x-3.5 gap-y-1 text-muted text-xs leading-[18px]">
							{computer.providerName && (
								<Spec icon="hardDrives" className="font-medium text-fg">
									{computer.providerName}
								</Spec>
							)}
							{computer.type && <Spec icon="box">{computer.type}</Spec>}
							{orb && (
								<>
									<Spec icon="cpu">{formatOrbCpus(orb.cpus)}</Spec>
									<Spec icon="memory">{formatOrbMemory(orb.memoryMB)}</Spec>
									{orb.nestedDocker && <DockerBadge />}
								</>
							)}
						</div>
					</InfoBlock>
					{state !== "deleted" && <OrbMetricTiles samples={samples} />}
					<InfoBlock icon="clock" title={t("assistantComputer.timeline")}>
						<div className="mt-3">
							{timeline.isPending ? (
								<Spinner />
							) : timeline.isError ? (
								<ErrorText error={timeline.error} />
							) : (
								<TimelineStrip
									ariaLabel={t("assistantComputer.timeline")}
									from={timeline.data.from}
									to={timeline.data.to}
									segments={timeline.data.segments}
								/>
							)}
						</div>
					</InfoBlock>
				</div>
				<InfoBlock icon="info" title={t("assistantComputer.details")}>
					<div className="mt-3">
						<KeyValue rows={rows} />
					</div>
					{!computer.toolsReady && (
						<p className="mt-3 text-faint text-xs">
							{t("assistantComputer.toolsPending")}
						</p>
					)}
				</InfoBlock>
			</div>
		</div>
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
	const [tab, setTab] = useState<Tab>(readTab);
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
	const selectTab = (k: Tab) => {
		setTab(k);
		saveTab(k);
	};
	const tabs = orbId ? (
		<ComputerTabs
			tab={tab}
			onSelect={selectTab}
			className={bp === "compact" ? "w-full" : ""}
		/>
	) : null;

	let body: ReactNode;
	if (status.isPending)
		body = (
			<div className="flex justify-center pt-[12vh]">
				<Spinner size={20} />
			</div>
		);
	else if (status.isError)
		body = (
			<div className="px-3 pt-4 sm:px-6 sm:pt-6">
				<ErrorText error={status.error} />
			</div>
		);
	else if (!orbId)
		body = (
			<div
				data-testid="computer-empty"
				className="min-h-0 flex-1 overflow-y-auto px-3 pt-[12vh] pb-safe"
			>
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
	else if (tab === "overview")
		body = (
			<ComputerOverview
				orbId={orbId}
				computer={computer}
				orb={orb}
				state={state}
			/>
		);
	else
		body = (
			<div role="tabpanel" className="flex min-h-0 flex-1 flex-col">
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
		);

	return (
		<Sheet inset={bp === "expanded"} className="flex h-full min-h-0 flex-col">
			<header
				data-window-drag=""
				className="flex h-12 shrink-0 items-center gap-2 border-border border-b px-3"
			>
				<ChromeLead show={chromeLeft} />
				{sidebarToggle && (
					<IconButton
						icon={bp === "expanded" ? "sidebar" : "menu"}
						label={t("nav.sidebar")}
						data-sidebar-toggle=""
						onClick={() => uiStore.getState().toggleSidebar()}
					/>
				)}
				<Icon name="laptop" size={16} className="shrink-0 text-faint" />
				<h1 className="min-w-0 shrink truncate font-semibold text-sm">
					{t("assistantComputer.title")}
				</h1>
				{orbId && (
					<span
						data-testid="computer-state"
						className="inline-flex shrink-0 items-center gap-1.5 text-muted text-xs"
					>
						{state && <OrbBead state={state} size={10} />}
						{state ? t(`orbState.${state}`) : "—"}
						{pending && <Spinner size={11} />}
					</span>
				)}
				<div className="min-w-0 flex-1" />
				{bp !== "compact" && tabs}
				<div className="min-w-0 flex-1" />
				{orbId && (
					<div
						data-testid="computer-status"
						className="flex shrink-0 items-center gap-1"
					>
						{state === "ready" ? (
							<Button
								size="sm"
								icon="pause"
								disabled={pending !== undefined}
								onClick={() => action.mutate("pause")}
							>
								{t("assistantComputer.pause")}
							</Button>
						) : (
							<Button
								size="sm"
								variant="accent"
								icon="play"
								disabled={pending !== undefined || !canWake}
								onClick={() => action.mutate("wake")}
							>
								{t("assistantComputer.wake")}
							</Button>
						)}
						<Menu
							items={[
								{
									key: "copy",
									icon: "copy",
									label: t("assistantComputer.copyId"),
									onSelect: async () => {
										const ok = await copyToClipboard(orbId);
										showToast(
											ok
												? { text: t("common.copied") }
												: { text: t("contextMenu.copyFailed"), tone: "err" },
										);
									},
								},
								{
									key: "reset",
									icon: "refresh",
									danger: true,
									disabled: pending !== undefined,
									label: t("assistantComputer.reset"),
									onSelect: () => setConfirmReset(true),
								},
							]}
							trigger={(p) => (
								<IconButton
									{...p}
									icon="more"
									label={t("assistantComputer.more")}
								/>
							)}
						/>
					</div>
				)}
				<WindowControls side="right" className="-mr-1" />
			</header>
			{bp === "compact" && tabs && (
				<div className="flex shrink-0 border-border border-b px-3 py-1.5">
					{tabs}
				</div>
			)}
			{orbId && action.error ? (
				<div className="shrink-0 border-border border-b px-4 py-2">
					<ErrorText error={action.error} />
				</div>
			) : null}
			<div className="flex min-h-0 flex-1 flex-col">{body}</div>
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
