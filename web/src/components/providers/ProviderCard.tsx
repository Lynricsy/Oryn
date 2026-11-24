/**
 * 单个 Provider 节点的综合监控卡片：
 * 顶栏（在线状态、驱动、版本、主机信息、能力与租约）+ 三列资源仪表（CPU / 内存 / 存储）
 * + Orb 状态分布与队列/网络摘要 + 可用规格胶囊 + 可展开的下属 Orb 列表与快捷运维操作。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, type Schemas, unwrap } from "../../api/client";
import { qk } from "../../api/queries";
import { confirmAction } from "../../lib/confirm";
import {
	formatBytes,
	formatMemoryMB,
	formatPercent,
	formatRelative,
	formatUptime,
} from "../../lib/format";
import { Badge, Meter, StatusDot, type Tone, thresholdTone } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { DockerBadge, typeHasDocker, WarmBadge } from "../ui/OrbBadges";
import { ErrorText, IconButton } from "../ui/primitives";

type OrbFilter = "all" | "running" | "frozen" | "stopped" | "failed";

/** Orb 状态映射到语义色。 */
export function orbStateTone(state: Schemas["OrbState"]): Tone {
	if (state === "ready") return "ok";
	if (state === "frozen" || state === "freezing") return "frost";
	if (state === "failed") return "err";
	if (
		state === "requested" ||
		state === "spawning" ||
		state === "connecting" ||
		state === "setting_up" ||
		state === "resuming" ||
		state === "waking"
	)
		return "accent";
	return "neutral";
}

function isOrbStarting(state: Schemas["OrbState"]): boolean {
	return (
		state === "requested" ||
		state === "spawning" ||
		state === "connecting" ||
		state === "setting_up" ||
		state === "resuming" ||
		state === "waking"
	);
}

function orbMatchesFilter(
	o: Schemas["ProviderOrbSummary"],
	filter: OrbFilter,
): boolean {
	switch (filter) {
		case "all":
			return true;
		case "running":
			return o.state === "ready" || isOrbStarting(o.state);
		case "frozen":
			return o.state === "frozen" || o.state === "freezing";
		case "stopped":
			return o.state === "stopped" || o.state === "stopping";
		case "failed":
			return o.state === "failed";
	}
}

function shortId(id: string): string {
	return id.length > 16 ? `${id.slice(0, 12)}…` : id;
}

export function formatCpus(n: number): string {
	return Number.isInteger(n) ? String(n) : Number(n.toFixed(1)).toString();
}

/** 运行 / 启动 / 冻结 / 停机 / 异常 五段状态分布条。 */
export function OrbDistributionBar({
	counts,
	label,
}: {
	counts: Schemas["ProviderOrbCounts"];
	label: string;
}): ReactNode {
	const total = counts.total;
	const segments = [
		{ key: "running", n: counts.running, color: "var(--good)" },
		{ key: "starting", n: counts.starting, color: "var(--brand)" },
		{ key: "frozen", n: counts.frozen, color: "var(--frost)" },
		{ key: "stopped", n: counts.stopped, color: "var(--text-ghost)" },
		{ key: "failed", n: counts.failed, color: "var(--bad)" },
	];
	return (
		<div
			role="img"
			aria-label={label}
			className="flex h-2 w-full overflow-hidden rounded-full bg-inset"
		>
			{total > 0 &&
				segments.map((seg) =>
					seg.n > 0 ? (
						<span
							key={seg.key}
							className="h-full transition-[width] duration-300 ease-out"
							style={{
								width: `${(seg.n / total) * 100}%`,
								background: seg.color,
							}}
						/>
					) : null,
				)}
		</div>
	);
}

/** 单个资源仪表小块（CPU / 内存 / 存储）。 */
function ResourceTile({
	icon,
	title,
	primary,
	percent,
	meterLabel,
	rows,
}: {
	icon: IconName;
	title: string;
	primary: string;
	percent?: number;
	meterLabel: string;
	rows: { label: string; value: ReactNode }[];
}): ReactNode {
	return (
		<div className="flex min-w-0 flex-col gap-2 rounded-md border border-border/80 bg-inset/50 p-3">
			<div className="flex items-center justify-between gap-2">
				<span className="inline-flex items-center gap-1.5 font-medium text-muted text-xs">
					<Icon name={icon} size={13} className="shrink-0 text-faint" />
					{title}
				</span>
				{percent !== undefined && (
					<span className="font-mono text-xs text-muted tabular-nums">
						{formatPercent(percent)}
					</span>
				)}
			</div>
			<div className="truncate font-semibold text-base text-fg tabular-nums">
				{primary}
			</div>
			{percent !== undefined && (
				<Meter
					value={percent}
					tone={thresholdTone(percent)}
					label={meterLabel}
				/>
			)}
			<dl className="mt-0.5 flex flex-col gap-1 text-xs">
				{rows.map((r) => (
					<div
						key={r.label}
						className="flex items-baseline justify-between gap-2"
					>
						<dt className="shrink-0 text-faint">{r.label}</dt>
						<dd className="m-0 min-w-0 truncate text-right text-muted tabular-nums">
							{r.value}
						</dd>
					</div>
				))}
			</dl>
		</div>
	);
}

function OrbRow({
	orb,
	canFreeze,
	busy,
	onInspect,
	onAction,
}: {
	orb: Schemas["ProviderOrbSummary"];
	canFreeze: boolean;
	busy: boolean;
	onInspect: (id: string, title: string) => void;
	onAction: (
		orb: Schemas["ProviderOrbSummary"],
		op: "freeze" | "stop" | "resume" | "delete",
	) => void;
}): ReactNode {
	const { t } = useTranslation();
	const primaryTitle =
		orb.threadTitles[0] ||
		orb.projectName ||
		t("providers.overview.orbs.untitled");
	const threadId = orb.threadIds[0];
	const sample = orb.latestSample;
	const stateLabel = t(`orbState.${orb.state}`);
	const isBox = Boolean(orb.box);
	const isDeleting = orb.state === "deleting";
	const isRunning = orb.state === "ready";
	const canResume =
		orb.state === "frozen" || orb.state === "stopped" || orb.state === "failed";

	return (
		<li
			data-testid="provider-orb-row"
			data-orb-id={orb.id}
			className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md px-2.5 py-2 transition-colors duration-150 hover:bg-hover/70"
		>
			{/* 状态与身份：点击打开 OrbDrawer 曲线与时间线 */}
			<button
				type="button"
				onClick={() =>
					onInspect(orb.id, `${primaryTitle} · ${shortId(orb.id)}`)
				}
				className="flex min-w-0 flex-1 items-center gap-2.5 text-left"
				title={t("providers.overview.orbs.inspect")}
			>
				<StatusDot
					tone={orbStateTone(orb.state)}
					pulse={isOrbStarting(orb.state) || isDeleting}
					label={stateLabel}
				/>
				<span className="flex min-w-0 flex-1 flex-col gap-0.5">
					<span className="flex min-w-0 flex-wrap items-center gap-1.5">
						<span className="truncate font-medium text-fg text-sm">
							{primaryTitle}
						</span>
						<Badge tone={orbStateTone(orb.state)}>{stateLabel}</Badge>
						{orb.pool && <WarmBadge pool={orb.pool} />}
						{orb.box && (
							<Badge icon="hardDrives">
								{t("providers.overview.orbs.boxBadge")}
							</Badge>
						)}
						{orb.computer && (
							<Badge icon="monitor">
								{t("providers.overview.orbs.computerBadge")}
							</Badge>
						)}
						{orb.nestedDocker && <DockerBadge />}
						{orb.snapshotKind && (
							<Badge icon="snapshot">
								{orb.snapshotKind === "project"
									? t("providers.overview.orbs.snapshotProject")
									: t("providers.overview.orbs.snapshotFork")}
							</Badge>
						)}
					</span>
					<span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-faint text-xs tabular-nums">
						<span className="font-mono" title={orb.id}>
							{shortId(orb.id)}
						</span>
						<span aria-hidden>·</span>
						<span>
							{orb.type} ({formatCpus(orb.cpus)}C /{" "}
							{formatMemoryMB(orb.memoryMB)})
						</span>
						{orb.projectName && orb.threadTitles[0] && (
							<>
								<span aria-hidden>·</span>
								<span className="truncate">{orb.projectName}</span>
							</>
						)}
						{orb.failureReason && (
							<>
								<span aria-hidden>·</span>
								<span className="truncate text-err">{orb.failureReason}</span>
							</>
						)}
					</span>
				</span>
			</button>

			{/* 实时资源三联读数 */}
			<div className="flex shrink-0 items-center gap-3 font-mono text-muted text-xs tabular-nums">
				{sample ? (
					<>
						<span
							className="inline-flex items-center gap-1"
							title={t("providers.overview.node.cpu")}
						>
							<Icon name="cpu" size={12} className="text-faint" />
							{orb.state === "ready" ? formatPercent(sample.cpuPct) : "—"}
						</span>
						<span
							className="inline-flex items-center gap-1"
							title={t("providers.overview.node.memory")}
						>
							<Icon name="memory" size={12} className="text-faint" />
							{orb.state === "ready" || orb.state === "frozen"
								? `${formatMemoryMB(sample.memUsedMB)} / ${formatMemoryMB(sample.memLimitMB || orb.memoryMB)}`
								: "—"}
						</span>
						<span
							className="hidden items-center gap-1 sm:inline-flex"
							title={t("providers.overview.orbs.fsUsed")}
						>
							<Icon name="database" size={12} className="text-faint" />
							{formatMemoryMB(sample.diskUsedMB)}
						</span>
					</>
				) : (
					<span className="text-faint">
						{t("providers.overview.orbs.noSample")}
					</span>
				)}
			</div>

			{/* 快捷跳转与生命周期操作 */}
			<div className="flex shrink-0 items-center gap-0.5">
				<IconButton
					icon="chart"
					box="xs"
					size={13}
					label={t("providers.overview.orbs.inspect")}
					onClick={() =>
						onInspect(orb.id, `${primaryTitle} · ${shortId(orb.id)}`)
					}
				/>
				{threadId && (
					<Link
						to={`/t/${threadId}`}
						title={t("providers.overview.orbs.openThread")}
						aria-label={t("providers.overview.orbs.openThread")}
						className="inline-flex h-6 w-6 items-center justify-center rounded-md text-muted transition-colors hover:bg-hover hover:text-fg"
					>
						<Icon name="arrowUpRight" size={13} />
					</Link>
				)}
				{isBox && (
					<Link
						to="/boxes"
						title={t("providers.overview.orbs.openBox")}
						aria-label={t("providers.overview.orbs.openBox")}
						className="inline-flex h-6 w-6 items-center justify-center rounded-md text-muted transition-colors hover:bg-hover hover:text-fg"
					>
						<Icon name="hardDrives" size={13} />
					</Link>
				)}
				{!isBox && !isDeleting && isRunning && canFreeze && (
					<IconButton
						icon="pause"
						box="xs"
						size={13}
						disabled={busy}
						label={t("providers.overview.orbs.freeze")}
						onClick={() => onAction(orb, "freeze")}
					/>
				)}
				{!isBox && !isDeleting && isRunning && (
					<IconButton
						icon="stop"
						box="xs"
						size={13}
						disabled={busy}
						label={t("providers.overview.orbs.stop")}
						onClick={() => onAction(orb, "stop")}
					/>
				)}
				{!isBox && !isDeleting && canResume && (
					<IconButton
						icon="play"
						box="xs"
						size={13}
						disabled={busy}
						label={t("providers.overview.orbs.resume")}
						onClick={() => onAction(orb, "resume")}
					/>
				)}
				{!isBox && !isDeleting && (
					<IconButton
						icon="trash"
						box="xs"
						size={13}
						disabled={busy}
						label={t("providers.overview.orbs.delete")}
						onClick={() => onAction(orb, "delete")}
					/>
				)}
			</div>
		</li>
	);
}

export function ProviderCard({
	item,
	isDefault,
	defaultExpanded,
	searchQuery,
	onInspectOrb,
}: {
	item: Schemas["ProviderOverviewItem"];
	isDefault: boolean;
	defaultExpanded: boolean;
	searchQuery: string;
	onInspectOrb: (orb: { id: string; title: string }) => void;
}): ReactNode {
	const { t, i18n } = useTranslation();
	const qc = useQueryClient();
	const [expanded, setExpanded] = useState(defaultExpanded);
	const [orbFilter, setOrbFilter] = useState<OrbFilter>("all");
	const [busyOrbId, setBusyOrbId] = useState<string | null>(null);
	const [actionError, setActionError] = useState<unknown>(null);

	const {
		provider: p,
		hostMetrics: hm,
		orbs,
		allocated,
		live,
		snapshots,
		jobs,
	} = item;
	const disabled = Boolean(p.disabledAt);
	const statusTone: Tone = disabled ? "err" : p.online ? "ok" : "neutral";
	const statusText = disabled
		? t("providers.disabled")
		: p.online
			? t("providers.online")
			: t("providers.offline");

	// CPU 仪表计算
	const cpuPct =
		hm?.cpuPct !== undefined
			? hm.cpuPct
			: hm && hm.cpuCores > 0 && live.sampledOrbs > 0
				? Math.min(100, live.cpuPct / hm.cpuCores)
				: allocated.runningCpus > 0 && live.sampledOrbs > 0
					? Math.min(100, live.cpuPct / allocated.runningCpus)
					: undefined;
	const cpuPrimary =
		hm?.cpuPct !== undefined
			? `${formatPercent(hm.cpuPct)} · ${t("panel.cores", { n: hm.cpuCores })}`
			: hm
				? t("panel.cores", { n: hm.cpuCores })
				: live.sampledOrbs > 0
					? formatPercent(live.cpuPct)
					: t("panel.cores", { n: formatCpus(allocated.runningCpus) });
	const cpuRows: { label: string; value: ReactNode }[] = [
		...(hm?.loadAvg1 !== undefined
			? [
					{
						label: t("providers.overview.node.hostUsage"),
						value: t("providers.overview.node.loadAvg", {
							l1: hm.loadAvg1.toFixed(2),
							l5: (hm.loadAvg5 ?? 0).toFixed(2),
							l15: (hm.loadAvg15 ?? 0).toFixed(2),
						}),
					},
				]
			: []),
		{
			label: t("providers.overview.node.orbLive"),
			value:
				live.sampledOrbs > 0
					? `${formatPercent(live.cpuPct)} (${live.sampledOrbs})`
					: "—",
		},
		{
			label: t("providers.overview.node.allocated"),
			value: t("providers.overview.node.runTotal", {
				run: `${formatCpus(allocated.runningCpus)}C`,
				total: `${formatCpus(allocated.totalCpus)}C`,
			}),
		},
	];

	// 内存仪表计算
	const memPct =
		hm && hm.memTotalMB > 0
			? (hm.memUsedMB / hm.memTotalMB) * 100
			: allocated.runningMemoryMB > 0 && live.sampledOrbs > 0
				? (live.memUsedMB / allocated.runningMemoryMB) * 100
				: undefined;
	const memPrimary =
		hm && hm.memTotalMB > 0
			? `${formatMemoryMB(hm.memUsedMB)} / ${formatMemoryMB(hm.memTotalMB)}`
			: live.sampledOrbs > 0
				? `${formatMemoryMB(live.memUsedMB)} / ${formatMemoryMB(allocated.runningMemoryMB)}`
				: formatMemoryMB(allocated.runningMemoryMB);
	const memRows: { label: string; value: ReactNode }[] = [
		{
			label: t("providers.overview.node.orbLive"),
			value: live.sampledOrbs > 0 ? formatMemoryMB(live.memUsedMB) : "—",
		},
		{
			label: t("providers.overview.node.allocated"),
			value: t("providers.overview.node.runTotal", {
				run: formatMemoryMB(allocated.runningMemoryMB),
				total: formatMemoryMB(allocated.totalMemoryMB),
			}),
		},
		...(hm?.limits?.maxMemoryMB
			? [
					{
						label: t("providers.overview.node.hostUsage"),
						value: t("providers.overview.node.limitMaxMem", {
							mem: formatMemoryMB(hm.limits.maxMemoryMB),
						}),
					},
				]
			: []),
	];

	// 存储仪表计算：仅采信宿主机物理卷与项目快照缓存，不累加各 orb 的 statfs（共享底盘会重复计数）
	const diskPct =
		hm && hm.diskTotalBytes > 0
			? (hm.diskUsedBytes / hm.diskTotalBytes) * 100
			: undefined;
	const diskPrimary =
		hm && hm.diskTotalBytes > 0
			? `${formatBytes(hm.diskUsedBytes)} / ${formatBytes(hm.diskTotalBytes)}`
			: snapshots.totalBytes > 0
				? formatBytes(snapshots.totalBytes)
				: "—";
	const diskRows: { label: string; value: ReactNode }[] = [
		...(hm?.diskAvailableBytes !== undefined
			? [
					{
						label: t("providers.overview.node.diskAvailable"),
						value: formatBytes(hm.diskAvailableBytes),
					},
				]
			: []),
		{
			label: t("providers.overview.node.snapshots"),
			value:
				snapshots.readyCount > 0 || snapshots.buildingCount > 0
					? `${t("providers.overview.node.snapshotsDetail", {
							ready: snapshots.readyCount,
							size: formatBytes(snapshots.totalBytes),
						})}${
							snapshots.buildingCount > 0
								? ` · ${t("providers.overview.node.snapshotsBuilding", { count: snapshots.buildingCount })}`
								: ""
						}`
					: "—",
		},
		hm?.diskPath
			? { label: t("providers.overview.node.hostUsage"), value: hm.diskPath }
			: {
					label: t("providers.overview.node.hostUsage"),
					value: t("providers.overview.node.hostDiskPending"),
				},
	];

	// 搜索词命中某个 orb 时自动展开列表
	const q = searchQuery.trim().toLowerCase();
	const filteredOrbs = useMemo(() => {
		return item.orbItems.filter((o) => {
			if (!orbMatchesFilter(o, orbFilter)) return false;
			if (!q) return true;
			if (
				p.name.toLowerCase().includes(q) ||
				(p.driver ?? "").toLowerCase().includes(q) ||
				(hm?.hostname ?? "").toLowerCase().includes(q)
			)
				return true;
			return (
				o.id.toLowerCase().includes(q) ||
				o.type.toLowerCase().includes(q) ||
				(o.projectName ?? "").toLowerCase().includes(q) ||
				o.threadTitles.some((tt) => tt.toLowerCase().includes(q))
			);
		});
	}, [item.orbItems, orbFilter, q, p.name, p.driver, hm?.hostname]);

	const orbSectionOpen =
		expanded ||
		(Boolean(q) &&
			!p.name.toLowerCase().includes(q) &&
			filteredOrbs.length > 0);

	const handleOrbAction = async (
		orb: Schemas["ProviderOrbSummary"],
		op: "freeze" | "stop" | "resume" | "delete",
	) => {
		if (op === "delete") {
			const ok = await confirmAction({
				title: t("providers.overview.orbs.deleteConfirm", {
					id: shortId(orb.id),
				}),
				action: t("common.delete"),
				danger: true,
			});
			if (!ok) return;
		}
		setBusyOrbId(orb.id);
		setActionError(null);
		try {
			if (op === "freeze" || op === "stop") {
				await unwrap(
					api.POST("/api/v1/orbs/{id}/pause", {
						params: { path: { id: orb.id } },
						body: { mode: op },
					}),
				);
			} else if (op === "resume") {
				await unwrap(
					api.POST("/api/v1/orbs/{id}/resume", {
						params: { path: { id: orb.id } },
					}),
				);
			} else {
				await unwrap(
					api.DELETE("/api/v1/orbs/{id}", {
						params: { path: { id: orb.id } },
					}),
				);
			}
			await Promise.all([
				qc.invalidateQueries({ queryKey: qk.providersOverview }),
				qc.invalidateQueries({ queryKey: qk.orbs }),
			]);
		} catch (err) {
			setActionError(err);
		} finally {
			setBusyOrbId(null);
		}
	};

	const filterTabs: { key: OrbFilter; label: string; count: number }[] = [
		{ key: "all", label: t("providers.overview.orbs.all"), count: orbs.total },
		{
			key: "running",
			label: t("providers.overview.state.running"),
			count: orbs.running + orbs.starting,
		},
		{
			key: "frozen",
			label: t("providers.overview.state.frozen"),
			count: orbs.frozen,
		},
		{
			key: "stopped",
			label: t("providers.overview.state.stopped"),
			count: orbs.stopped,
		},
		{
			key: "failed",
			label: t("providers.overview.state.failed"),
			count: orbs.failed,
		},
	];

	return (
		<section
			data-testid="provider-node-card"
			data-provider-id={p.id}
			className="flex min-w-0 flex-col overflow-hidden rounded-lg border border-border bg-panel"
		>
			{/* 节点身份头 */}
			<div className="flex flex-wrap items-start justify-between gap-2 border-border border-b px-4 py-3">
				<div className="flex min-w-0 flex-col gap-1">
					<div className="flex min-w-0 flex-wrap items-center gap-2">
						<StatusDot tone={statusTone} label={statusText} />
						<h2 className="truncate font-semibold text-base text-fg">
							{p.name}
						</h2>
						<Badge tone={statusTone}>{statusText}</Badge>
						{isDefault && (
							<Badge tone="accent">
								{t("providers.overview.node.defaultProvider")}
							</Badge>
						)}
						{p.driver && <Badge icon="box">{p.driver}</Badge>}
						{item.version && <Badge>{item.version}</Badge>}
						{item.allowMultiple && (
							<Badge>{t("providers.overview.node.multiInstance")}</Badge>
						)}
					</div>
					<div className="flex flex-wrap items-center gap-x-2.5 gap-y-0.5 text-faint text-xs tabular-nums">
						{hm?.hostname && (
							<span className="font-mono text-muted">{hm.hostname}</span>
						)}
						{hm?.os && hm?.arch && (
							<span>
								{hm.os}/{hm.arch}
							</span>
						)}
						{hm?.uptimeSeconds !== undefined && hm.uptimeSeconds > 0 && (
							<span>
								{t("providers.overview.node.uptime", {
									duration: formatUptime(
										hm.uptimeSeconds * 1000,
										i18n.language,
									),
								})}
							</span>
						)}
						{p.online && p.leaseUntil && (
							<span>
								{t("providers.overview.node.leaseValid", {
									when: formatRelative(p.leaseUntil),
								})}
							</span>
						)}
						{p.instanceId && (
							<span className="font-mono">
								{t("providers.overview.node.instanceId", {
									id: p.instanceId.slice(0, 10),
								})}
							</span>
						)}
						{!hm && p.online && (
							<span className="text-faint">
								{t("providers.overview.node.hostMetricsMissing")}
							</span>
						)}
					</div>
				</div>

				{/* 右侧能力徽标 */}
				{p.capabilities && (
					<div className="flex flex-wrap items-center gap-1.5">
						<Badge
							tone={p.capabilities.freeze ? "ok" : "neutral"}
							icon={p.capabilities.freeze ? "check" : "close"}
						>
							{t("providers.freeze")}
						</Badge>
						<Badge
							tone={p.capabilities.snapshot ? "ok" : "neutral"}
							icon={p.capabilities.snapshot ? "check" : "close"}
						>
							{t("providers.snapshot")}
						</Badge>
						{p.capabilities.offlineFs && (
							<Badge tone="ok" icon="check">
								{t("providers.offlineFs")}
							</Badge>
						)}
					</div>
				)}
			</div>

			{/* 主体：三列资源仪表盘 + 状态分布 + 规格 */}
			<div className="flex flex-col gap-3 p-4">
				<div className="grid grid-cols-1 gap-2.5 md:grid-cols-3">
					<ResourceTile
						icon="cpu"
						title={t("providers.overview.node.cpu")}
						primary={cpuPrimary}
						percent={cpuPct}
						meterLabel={t("providers.overview.node.cpu")}
						rows={cpuRows}
					/>
					<ResourceTile
						icon="memory"
						title={t("providers.overview.node.memory")}
						primary={memPrimary}
						percent={memPct}
						meterLabel={t("providers.overview.node.memory")}
						rows={memRows}
					/>
					<ResourceTile
						icon="database"
						title={t("providers.overview.node.storage")}
						primary={diskPrimary}
						percent={diskPct}
						meterLabel={t("providers.overview.node.storage")}
						rows={diskRows}
					/>
				</div>

				{/* Orb 状态分布 & 任务队列 / 网络摘要 */}
				<div className="flex flex-col gap-2 rounded-md border border-border/80 bg-inset/35 px-3 py-2.5">
					<div className="flex flex-wrap items-center justify-between gap-2 text-xs">
						<span className="font-medium text-muted">
							{t("providers.overview.node.orbBreakdown")}
						</span>
						<div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-muted tabular-nums">
							<span className="inline-flex items-center gap-1">
								<StatusDot tone="ok" />
								{t("providers.overview.state.running")} {orbs.running}
							</span>
							{orbs.starting > 0 && (
								<span className="inline-flex items-center gap-1">
									<StatusDot tone="accent" />
									{t("providers.overview.state.starting")} {orbs.starting}
								</span>
							)}
							<span className="inline-flex items-center gap-1">
								<StatusDot tone="frost" />
								{t("providers.overview.state.frozen")} {orbs.frozen}
							</span>
							<span className="inline-flex items-center gap-1">
								<StatusDot tone="neutral" />
								{t("providers.overview.state.stopped")} {orbs.stopped}
							</span>
							{orbs.failed > 0 && (
								<span className="inline-flex items-center gap-1 text-err">
									<StatusDot tone="err" />
									{t("providers.overview.state.failed")} {orbs.failed}
								</span>
							)}
							{orbs.deleted > 0 && (
								<span className="text-faint">
									{t("providers.overview.node.deletedHistory", {
										count: orbs.deleted,
									})}
								</span>
							)}
						</div>
					</div>
					<OrbDistributionBar
						counts={orbs}
						label={t("providers.overview.node.orbBreakdown")}
					/>
					<div className="flex flex-wrap items-center justify-between gap-2 pt-0.5 text-faint text-xs tabular-nums">
						<div className="flex flex-wrap items-center gap-1.5">
							{orbs.warmPool > 0 && (
								<Badge icon="warm">
									{t("providers.overview.node.warmPool", {
										count: orbs.warmPool,
									})}
								</Badge>
							)}
							{orbs.boxes > 0 && (
								<Badge icon="hardDrives">
									{t("providers.overview.node.boxes", { count: orbs.boxes })}
								</Badge>
							)}
							{orbs.computers > 0 && (
								<Badge icon="monitor">
									{t("providers.overview.node.computers", {
										count: orbs.computers,
									})}
								</Badge>
							)}
							{hm?.limits?.maxVMs && (
								<Badge>
									{t("providers.overview.node.limitMaxVms", {
										used: orbs.running + orbs.starting,
										max: hm.limits.maxVMs,
									})}
								</Badge>
							)}
						</div>
						<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
							{(live.netRxBytes > 0 || live.netTxBytes > 0) && (
								<span>
									{t("providers.overview.node.netTraffic")}: ↓
									{formatBytes(live.netRxBytes)} / ↑
									{formatBytes(live.netTxBytes)}
								</span>
							)}
							<span>
								{t("providers.overview.node.jobs")}:{" "}
								{t("providers.overview.node.jobsQueue", {
									queued: jobs.queued,
									claimed: jobs.claimed,
								})}{" "}
								·{" "}
								<span className={jobs.failed24h > 0 ? "text-err" : undefined}>
									{t("providers.overview.node.jobs24h", {
										ok: jobs.succeeded24h,
										fail: jobs.failed24h,
									})}
								</span>
							</span>
						</div>
					</div>
				</div>

				{/* 支持规格胶囊行 */}
				{p.orbTypes.length > 0 && (
					<div className="flex flex-wrap items-center gap-2 text-xs">
						<span className="text-faint">
							{t("providers.overview.node.orbTypes")}:
						</span>
						{p.orbTypes.map((ot) => (
							<span
								key={ot.name}
								className="inline-flex h-6 items-center gap-1.5 rounded-md bg-inset px-2 text-muted tabular-nums"
							>
								<span className="font-medium text-fg">{ot.name}</span>
								<span>
									{formatCpus(ot.cpus)}C / {formatMemoryMB(ot.memoryMB)}
								</span>
								{p.defaultType === ot.name && (
									<Badge>{t("providers.default")}</Badge>
								)}
								{typeHasDocker(ot) && <DockerBadge />}
							</span>
						))}
					</div>
				)}
			</div>

			{/* 下属 Orb 折叠明细 */}
			<div className="border-border border-t">
				<div className="flex flex-wrap items-center justify-between gap-2 px-4 py-2">
					<button
						type="button"
						data-testid="provider-orbs-toggle"
						aria-expanded={orbSectionOpen}
						onClick={() => setExpanded((v) => !v)}
						className="inline-flex items-center gap-1.5 font-medium text-muted text-xs transition-colors hover:text-fg"
					>
						<Icon
							name={orbSectionOpen ? "chevronDown" : "chevronRight"}
							size={13}
						/>
						{t("providers.overview.orbs.section", { count: orbs.total })}
					</button>
					{orbSectionOpen && orbs.total > 0 && (
						<div className="flex flex-wrap items-center gap-1">
							{filterTabs.map((tab) => (
								<button
									key={tab.key}
									type="button"
									onClick={() => setOrbFilter(tab.key)}
									className={`inline-flex h-6 items-center gap-1 rounded-md px-2 text-xs tabular-nums transition-colors ${
										orbFilter === tab.key
											? "bg-active font-medium text-fg"
											: "text-muted hover:bg-hover hover:text-fg"
									}`}
								>
									<span>{tab.label}</span>
									<span className="text-faint">{tab.count}</span>
								</button>
							))}
						</div>
					)}
				</div>

				{orbSectionOpen && (
					<div className="border-border/60 border-t px-2 py-1.5">
						<ErrorText error={actionError} />
						{orbs.total === 0 ? (
							<p className="px-2 py-3 text-faint text-xs">
								{t("providers.overview.orbs.empty")}
							</p>
						) : filteredOrbs.length === 0 ? (
							<p className="px-2 py-3 text-faint text-xs">
								{t("providers.overview.orbs.emptyFilter")}
							</p>
						) : (
							<ul className="flex flex-col divide-y divide-border/50">
								{filteredOrbs.map((orb) => (
									<OrbRow
										key={orb.id}
										orb={orb}
										canFreeze={Boolean(p.capabilities?.freeze)}
										busy={busyOrbId === orb.id}
										onInspect={(id, title) => onInspectOrb({ id, title })}
										onAction={handleOrbAction}
									/>
								))}
							</ul>
						)}
					</div>
				)}
			</div>
		</section>
	);
}
