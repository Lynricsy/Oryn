/**
 * Provider 算力与资源总览页 `/providers`：
 * 展示全集群节点在线状态、运行中/总 Orb 分布、宿主机与 Orb 内存/存储/CPU 占用、快照缓存与任务队列健康度，
 * 并可逐节点展开查看下属 Orb 实时采样、钻取历史曲线（OrbDrawer）及执行快捷运维操作。
 */
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { useProvidersOverview } from "../api/queries";
import { useStatsMeta } from "../api/stats-queries";
import {
	ChromeLead,
	WindowControls,
} from "../components/layout/WindowControls";
import {
	OrbDistributionBar,
	ProviderCard,
	StackedResourceBar,
	type StackedResourceBarProps,
	VolumeUsageBar,
	type VolumeUsageBarProps,
} from "../components/providers/ProviderCard";
import { OrbDrawer } from "../components/stats/OrbDrawer";
import { Sheet } from "../components/ui/data";
import { Icon, type IconName } from "../components/ui/icons";
import {
	Button,
	EmptyState,
	ErrorText,
	IconButton,
	Spinner,
	TextInput,
} from "../components/ui/primitives";
import {
	formatBytes,
	formatCpus,
	formatMemoryMB,
	formatPercent,
	formatRelative,
	withUnlimitedCount,
} from "../lib/format";
import { useBreakpoint } from "../lib/viewport";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";

type NodeFilter = "all" | "online" | "offline" | "disabled";

function SummaryKpiCard({
	testId,
	icon,
	label,
	value,
	badgeText,
	sub,
	bar,
	volumeBar,
	extra,
}: {
	testId: string;
	icon: IconName;
	label: string;
	value: string;
	badgeText?: string;
	sub?: ReactNode;
	bar?: StackedResourceBarProps;
	volumeBar?: VolumeUsageBarProps;
	extra?: ReactNode;
}): ReactNode {
	return (
		<div
			data-testid={testId}
			className="flex min-w-0 flex-col gap-1.5 rounded-lg border border-border bg-panel px-3.5 py-3"
		>
			<div className="flex items-center justify-between gap-2">
				<span className="inline-flex min-w-0 items-center gap-1.5 truncate text-faint text-xs">
					<Icon name={icon} size={13} className="shrink-0" />
					<span className="truncate">{label}</span>
				</span>
				{badgeText && (
					<span className="shrink-0 font-mono text-muted text-xs tabular-nums">
						{badgeText}
					</span>
				)}
			</div>
			<div className="truncate font-semibold text-fg text-xl tabular-nums">
				{value}
			</div>
			{bar && <StackedResourceBar {...bar} />}
			{volumeBar && <VolumeUsageBar {...volumeBar} />}
			{extra}
			{sub && (
				<div className="truncate text-faint text-xs tabular-nums">{sub}</div>
			)}
		</div>
	);
}

export function ProvidersOverviewPage(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const bp = useBreakpoint();
	const sidebarToggle = useSidebarToggleShown();
	const chromeLeft = !useSidebarShown();

	const overview = useProvidersOverview();
	const statsMeta = useStatsMeta();
	const [nodeFilter, setNodeFilter] = useState<NodeFilter>("all");
	const [search, setSearch] = useState("");
	const [inspecting, setInspecting] = useState<{
		id: string;
		title: string;
	} | null>(null);

	const data = overview.data;
	const providers = data?.providers ?? [];

	const filteredProviders = useMemo(() => {
		const q = search.trim().toLowerCase();
		return providers.filter((item) => {
			const p = item.provider;
			if (nodeFilter === "online" && (!p.online || p.disabledAt)) return false;
			if (nodeFilter === "offline" && (p.online || p.disabledAt)) return false;
			if (nodeFilter === "disabled" && !p.disabledAt) return false;
			if (!q) return true;
			if (
				p.name.toLowerCase().includes(q) ||
				(p.driver ?? "").toLowerCase().includes(q) ||
				(item.hostMetrics?.hostname ?? "").toLowerCase().includes(q)
			)
				return true;
			return item.orbItems.some(
				(o) =>
					o.id.toLowerCase().includes(q) ||
					o.type.toLowerCase().includes(q) ||
					(o.projectName ?? "").toLowerCase().includes(q) ||
					o.threadTitles.some((tt) => tt.toLowerCase().includes(q)),
			);
		});
	}, [providers, nodeFilter, search]);

	const summary = data?.summary;
	const hasHostMem = Boolean(summary && summary.host.memTotalMB > 0);
	const hasHostDisk = Boolean(summary && summary.host.diskTotalBytes > 0);
	const hasHostCpu = Boolean(summary && summary.host.cpuCores > 0);
	const hasOrbDisk = Boolean(summary && summary.host.orbDiskReportedCount > 0);

	// 全局内存：Orb 实时内存 vs 宿主机其他内存；没有宿主机数据时退回按运行配额算占比，
	// 有不限内存的 orb 在跑时配额不能当分母（它们的用量不在配额里）
	const orbMemPct =
		summary && hasHostMem
			? (summary.live.memUsedMB / summary.host.memTotalMB) * 100
			: summary &&
					summary.allocated.runningMemoryMB > 0 &&
					summary.allocated.runningUnlimitedMemory === 0 &&
					summary.live.sampledOrbs > 0
				? (summary.live.memUsedMB / summary.allocated.runningMemoryMB) * 100
				: undefined;
	const hostMemPct =
		summary && hasHostMem
			? (summary.host.memUsedMB / summary.host.memTotalMB) * 100
			: undefined;
	const otherMemMB =
		summary && hasHostMem
			? Math.max(0, summary.host.memUsedMB - summary.live.memUsedMB)
			: undefined;

	// 全局存储：Orb 可写层/盘（逻辑或含 CoW 块）与快照缓存独立标注，宿主机物理卷进度条仅反映 statfs，不做扣减
	const hostDiskPct =
		summary && hasHostDisk
			? (summary.host.diskUsedBytes / summary.host.diskTotalBytes) * 100
			: undefined;
	const storagePrimaryBytes = summary
		? hasOrbDisk
			? summary.host.orbDiskBytes
			: summary.snapshots.totalBytes
		: 0;
	// 全局 CPU：Orb 实时 CPU 核数 vs 宿主机其他 CPU 核数（退回配额分母的条件同内存）
	const orbCpuCores = summary ? summary.live.cpuPct / 100 : 0;
	const orbCpuPct =
		summary && hasHostCpu
			? (orbCpuCores / summary.host.cpuCores) * 100
			: summary &&
					summary.allocated.runningCpus > 0 &&
					summary.allocated.runningUnlimitedCpu === 0 &&
					summary.live.sampledOrbs > 0
				? (orbCpuCores / summary.allocated.runningCpus) * 100
				: undefined;
	const hostCpuPct =
		summary && hasHostCpu && summary.host.cpuUsedCores > 0
			? (summary.host.cpuUsedCores / summary.host.cpuCores) * 100
			: undefined;
	const otherCpuCores =
		summary && hasHostCpu && summary.host.cpuUsedCores > 0
			? Math.max(0, summary.host.cpuUsedCores - orbCpuCores)
			: undefined;

	const activeJobs = (summary?.jobs.queued ?? 0) + (summary?.jobs.claimed ?? 0);
	const kpiCols =
		bp === "compact"
			? "grid-cols-1 sm:grid-cols-2"
			: bp === "medium"
				? "grid-cols-2 lg:grid-cols-3"
				: "grid-cols-3";

	const filterButtons: { key: NodeFilter; label: string; count: number }[] = [
		{
			key: "all",
			label: t("providers.overview.filterAll"),
			count: summary?.providersTotal ?? 0,
		},
		{
			key: "online",
			label: t("providers.overview.filterOnline"),
			count: summary?.providersOnline ?? 0,
		},
		{
			key: "offline",
			label: t("providers.overview.filterOffline"),
			count: summary?.providersOffline ?? 0,
		},
		{
			key: "disabled",
			label: t("providers.overview.filterDisabled"),
			count: summary?.providersDisabled ?? 0,
		},
	];

	const drawerTo = Date.now();
	const drawerFrom = drawerTo - 24 * 3600_000;

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
				<Icon name="gauge" size={16} className="shrink-0 text-faint" />
				<h1 className="min-w-0 flex-1 truncate font-semibold text-sm">
					{t("providers.overview.title")}
				</h1>
				{data?.generatedAt && (
					<span className="hidden text-faint text-xs tabular-nums sm:inline">
						{t("providers.overview.updatedAt", {
							time: formatRelative(data.generatedAt),
						})}
					</span>
				)}
				<IconButton
					icon="refresh"
					box="sm"
					size={14}
					disabled={overview.isFetching}
					label={t("providers.overview.refresh")}
					onClick={() => void overview.refetch()}
				/>
				{bp === "compact" ? (
					<IconButton
						icon="settings"
						box="sm"
						size={14}
						label={t("providers.overview.manage")}
						onClick={() => navigate("/settings/compute")}
					/>
				) : (
					<Button
						size="sm"
						icon="settings"
						onClick={() => navigate("/settings/compute")}
					>
						{t("providers.overview.manage")}
					</Button>
				)}
				<WindowControls side="right" className="-mr-1" />
			</header>

			<div
				data-testid="providers-scroll"
				className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden p-3 pb-safe sm:p-5"
			>
				<div className="mx-auto flex max-w-7xl min-w-0 flex-col gap-4">
					{overview.isPending ? (
						<div className="flex justify-center pt-[14vh] text-muted">
							<Spinner size={20} />
						</div>
					) : overview.isError ? (
						<ErrorText error={overview.error} />
					) : !summary || providers.length === 0 ? (
						<EmptyState icon="gauge" title={t("providers.overview.emptyTitle")}>
							<p className="max-w-md text-muted text-sm">
								{t("providers.overview.emptyBody")}
							</p>
							<Button
								variant="primary"
								icon="plus"
								onClick={() => navigate("/settings/compute")}
							>
								{t("providers.overview.emptyAction")}
							</Button>
						</EmptyState>
					) : (
						<>
							{/* 全局算力 KPI 面板 */}
							<div className={`grid gap-2.5 ${kpiCols}`}>
								<SummaryKpiCard
									testId="kpi-providers-nodes"
									icon="box"
									label={t("providers.overview.kpi.nodes")}
									value={`${summary.providersOnline} / ${summary.providersTotal}`}
									sub={t("providers.overview.kpi.nodesSub", {
										offline: summary.providersOffline,
										disabled: summary.providersDisabled,
									})}
								/>
								<SummaryKpiCard
									testId="kpi-providers-orbs"
									icon="bolt"
									label={t("providers.overview.kpi.orbs")}
									value={`${summary.orbs.running} / ${summary.orbs.total}`}
									extra={
										<OrbDistributionBar
											counts={summary.orbs}
											label={t("providers.overview.kpi.orbs")}
										/>
									}
									sub={t("providers.overview.kpi.orbsSub", {
										frozen: summary.orbs.frozen,
										stopped: summary.orbs.stopped,
										failed: summary.orbs.failed,
									})}
								/>
								<SummaryKpiCard
									testId="kpi-providers-memory"
									icon="memory"
									label={t("providers.overview.kpi.memory")}
									value={
										summary.live.sampledOrbs > 0
											? formatMemoryMB(summary.live.memUsedMB)
											: formatMemoryMB(summary.allocated.runningMemoryMB)
									}
									badgeText={
										orbMemPct !== undefined
											? t("providers.overview.node.orbShare", {
													pct: formatPercent(orbMemPct),
												})
											: undefined
									}
									bar={
										orbMemPct !== undefined
											? {
													orbPct: orbMemPct,
													hostTotalPct: hostMemPct,
													orbLegend: t("providers.overview.node.barOrb", {
														value: formatMemoryMB(summary.live.memUsedMB),
													}),
													otherLegend:
														otherMemMB !== undefined
															? t("providers.overview.node.barOther", {
																	value: formatMemoryMB(otherMemMB),
																})
															: undefined,
													label: t("providers.overview.kpi.memory"),
													testId: "kpi-bar-memory",
												}
											: undefined
									}
									sub={withUnlimitedCount(
										hasHostMem
											? t("providers.overview.kpi.memoryHostSub", {
													used: formatMemoryMB(summary.host.memUsedMB),
													total: formatMemoryMB(summary.host.memTotalMB),
													alloc: formatMemoryMB(
														summary.allocated.runningMemoryMB,
													),
												})
											: t("providers.overview.kpi.memoryOrbSub", {
													run: formatMemoryMB(
														summary.allocated.runningMemoryMB,
													),
													total: formatMemoryMB(
														summary.allocated.totalMemoryMB,
													),
												}),
										summary.allocated.runningUnlimitedMemory,
									)}
								/>
								<SummaryKpiCard
									testId="kpi-providers-storage"
									icon="database"
									label={t("providers.overview.kpi.storage")}
									value={formatBytes(storagePrimaryBytes)}
									badgeText={
										hostDiskPct !== undefined
											? t("providers.overview.node.hostVolShare", {
													pct: formatPercent(hostDiskPct),
												})
											: undefined
									}
									volumeBar={
										hostDiskPct !== undefined
											? {
													percent: hostDiskPct,
													legendLeft: t(
														"providers.overview.node.hostVolLegend",
														{
															used: formatBytes(summary.host.diskUsedBytes),
															total: formatBytes(summary.host.diskTotalBytes),
														},
													),
													label: t("providers.overview.kpi.storage"),
													testId: "kpi-bar-storage",
												}
											: undefined
									}
									sub={
										hasHostDisk
											? hasOrbDisk
												? t("providers.overview.kpi.storageDetailSub", {
														snap: formatBytes(summary.snapshots.totalBytes),
														count: summary.snapshots.readyCount,
														used: formatBytes(summary.host.diskUsedBytes),
														total: formatBytes(summary.host.diskTotalBytes),
													})
												: t("providers.overview.kpi.storageSnapOnlySub", {
														snap: formatBytes(summary.snapshots.totalBytes),
														count: summary.snapshots.readyCount,
														used: formatBytes(summary.host.diskUsedBytes),
														total: formatBytes(summary.host.diskTotalBytes),
													})
											: t("providers.overview.kpi.storageSnapNoHostSub", {
													count: summary.snapshots.readyCount,
												})
									}
								/>
								<SummaryKpiCard
									testId="kpi-providers-cpu"
									icon="cpu"
									label={t("providers.overview.kpi.cpu")}
									value={
										summary.live.sampledOrbs > 0
											? formatPercent(summary.live.cpuPct)
											: t("panel.cores", {
													n: formatCpus(summary.allocated.runningCpus),
												})
									}
									badgeText={
										orbCpuPct !== undefined
											? t("providers.overview.node.orbShare", {
													pct: formatPercent(orbCpuPct),
												})
											: undefined
									}
									bar={
										orbCpuPct !== undefined
											? {
													orbPct: orbCpuPct,
													hostTotalPct: hostCpuPct,
													orbLegend: t("providers.overview.node.barOrb", {
														value: t("panel.cores", {
															n: formatCpus(orbCpuCores),
														}),
													}),
													otherLegend:
														otherCpuCores !== undefined
															? t("providers.overview.node.barOther", {
																	value: t("panel.cores", {
																		n: formatCpus(otherCpuCores),
																	}),
																})
															: undefined,
													label: t("providers.overview.kpi.cpu"),
													testId: "kpi-bar-cpu",
												}
											: undefined
									}
									sub={withUnlimitedCount(
										summary.host.cpuCores > 0
											? t("providers.overview.kpi.cpuSub", {
													run: formatCpus(summary.allocated.runningCpus),
													total: formatCpus(summary.allocated.totalCpus),
													host: summary.host.cpuCores,
												})
											: t("providers.overview.kpi.cpuSubNoHost", {
													run: formatCpus(summary.allocated.runningCpus),
													total: formatCpus(summary.allocated.totalCpus),
												}),
										summary.allocated.runningUnlimitedCpu,
									)}
								/>
								<SummaryKpiCard
									testId="kpi-providers-jobs"
									icon="snapshot"
									label={t("providers.overview.kpi.jobs")}
									value={
										activeJobs > 0
											? t("providers.overview.kpi.jobsActive", {
													count: activeJobs,
												})
											: t("providers.overview.kpi.jobsIdle")
									}
									sub={t("providers.overview.kpi.jobsSub", {
										ok: summary.jobs.succeeded24h,
										fail: summary.jobs.failed24h,
										snaps: summary.snapshots.readyCount,
									})}
								/>
							</div>

							{/* 筛选与搜索条 */}
							<div className="flex flex-wrap items-center justify-between gap-2">
								<div
									role="tablist"
									aria-label={t("providers.overview.title")}
									className="inline-flex flex-wrap items-center gap-1 rounded-lg border border-border bg-panel p-0.5"
								>
									{filterButtons.map((btn) => (
										<button
											key={btn.key}
											type="button"
											role="tab"
											aria-selected={nodeFilter === btn.key}
											onClick={() => setNodeFilter(btn.key)}
											className={`inline-flex h-7 items-center gap-1.5 rounded-md px-2.5 text-xs tabular-nums transition-colors ${
												nodeFilter === btn.key
													? "bg-active font-medium text-fg"
													: "text-muted hover:bg-hover hover:text-fg"
											}`}
										>
											<span>{btn.label}</span>
											<span className="text-faint">{btn.count}</span>
										</button>
									))}
								</div>
								<div className="relative w-full sm:w-80">
									<Icon
										name="search"
										size={14}
										className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-faint"
									/>
									<TextInput
										value={search}
										onChange={(e) => setSearch(e.target.value)}
										placeholder={t("providers.overview.searchPlaceholder")}
										className="pl-8"
									/>
								</div>
							</div>

							{/* 节点详情卡片列表 */}
							{filteredProviders.length === 0 ? (
								<div className="rounded-lg border border-border bg-panel px-4 py-8 text-center text-faint text-sm">
									{t("providers.overview.noMatch")}
								</div>
							) : (
								<div className="flex flex-col gap-3">
									{filteredProviders.map((item, idx) => (
										<ProviderCard
											key={item.provider.id}
											item={item}
											isDefault={
												data?.orbDefaults?.provider === item.provider.name
											}
											defaultExpanded={idx === 0 && item.orbs.total > 0}
											searchQuery={search}
											onInspectOrb={setInspecting}
										/>
									))}
								</div>
							)}
						</>
					)}
				</div>
			</div>

			{inspecting && (
				<OrbDrawer
					orbId={inspecting.id}
					title={inspecting.title}
					from={drawerFrom}
					to={drawerTo}
					samplesRetentionMs={statsMeta.data?.retention.samplesMs ?? 0}
					onClose={() => setInspecting(null)}
				/>
			)}
		</Sheet>
	);
}
