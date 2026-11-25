/**
 * orb 抽屉（M10 计划 5.3）：状态时间线（`orbs/{id}/timeline`）+ CPU、内存两张曲线（`orbs/{id}/metrics`，步长随窗口）
 * + 最近 setup 记录。compact 全屏，其余是右侧整高面板；Esc / 关闭按钮 / 点遮罩关闭，打开时焦点进抽屉，关闭后还原。
 * 采样为空且窗口起点早于采样保留期时说明「采样已过保留期」。
 */
import { type ReactNode, useLayoutEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import {
	useOrbMetricsRange,
	useOrbSetupRuns,
	useOrbTimeline,
} from "../../api/stats-queries";
import {
	formatDateTime,
	formatDuration,
	formatMemoryMB,
	formatPercent,
} from "../../lib/format";
import { focusablesIn, isTopModal, pushModal, trapTab } from "../../lib/modal";
import { useBreakpoint } from "../../lib/viewport";
import { LineChart } from "../charts/LineChart";
import { TimelineStrip } from "../charts/TimelineStrip";
import { Badge, Card } from "../ui/data";
import { ErrorText, IconButton, Spinner } from "../ui/primitives";

export function OrbDrawer({
	orbId,
	title,
	from,
	to,
	samplesRetentionMs,
	onClose,
}: {
	orbId: string;
	title: string;
	from: number;
	to: number;
	/** `StatsMeta.retention.samplesMs`（0 = 永久） */
	samplesRetentionMs: number;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const compact = useBreakpoint() === "compact";
	const hostRef = useRef<HTMLDivElement>(null);
	const panelRef = useRef<HTMLDivElement>(null);
	const onCloseRef = useRef(onClose);
	onCloseRef.current = onClose;
	// 查询键用所选窗口；实际终点在每次轮询时取 `min(to, 现在)`（见 `useOrbTimeline`），抽屉开着也会跟着前进
	const timeline = useOrbTimeline(orbId, from, to);
	const metrics = useOrbMetricsRange(orbId, from, to);
	const runs = useOrbSetupRuns(orbId);
	useLayoutEffect(() => {
		const host = hostRef.current;
		const panel = panelRef.current;
		if (!host || !panel) return;
		const previous =
			document.activeElement instanceof HTMLElement
				? document.activeElement
				: null;
		const release = pushModal(host);
		(focusablesIn(panel)[0] ?? panel).focus();
		const onKey = (e: KeyboardEvent) => {
			if (!isTopModal(host)) return;
			if (e.key === "Escape") onCloseRef.current();
			else trapTab(e, panel);
		};
		window.addEventListener("keydown", onKey);
		return () => {
			window.removeEventListener("keydown", onKey);
			release();
			if (previous?.isConnected) previous.focus();
		};
	}, []);
	const samples = metrics.data?.samples ?? [];
	const expired =
		samples.length === 0 &&
		samplesRetentionMs > 0 &&
		from < Date.now() - samplesRetentionMs;
	const span = (timeline.data?.to ?? Math.min(to, Date.now())) - from;
	const fmtX = (x: number) =>
		new Intl.DateTimeFormat(undefined, {
			...(span > 2 * 86_400_000 ? { month: "numeric", day: "numeric" } : {}),
			hour: "2-digit",
			minute: "2-digit",
			hourCycle: "h23",
		}).format(x);
	const memLimit = Math.max(0, ...samples.map((s) => s.memLimitMB));
	return createPortal(
		<div ref={hostRef} className="fixed inset-0 z-50 flex justify-end">
			{!compact && (
				<button
					type="button"
					tabIndex={-1}
					aria-label={t("common.close")}
					className="absolute inset-0 bg-scrim"
					onClick={onClose}
				/>
			)}
			<div
				ref={panelRef}
				role="dialog"
				aria-modal="true"
				aria-label={title}
				tabIndex={-1}
				data-testid="orb-drawer"
				className={`relative flex flex-col bg-panel pt-safe pr-safe outline-none ${compact ? "h-full w-full pl-safe" : "h-full w-[min(34rem,100vw)] border-border border-l shadow-xl"}`}
			>
				<div className="flex h-12 shrink-0 items-center gap-2 border-border border-b px-4">
					<h2
						className="min-w-0 flex-1 truncate font-semibold text-sm"
						title={orbId}
					>
						{title}
					</h2>
					<IconButton
						icon="close"
						label={t("common.close")}
						onClick={onClose}
					/>
				</div>
				<div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-4 pb-safe">
					<Card title={t("stats.orbs.drawer.timeline")}>
						<ErrorText error={timeline.error} />
						{timeline.isPending ? (
							<Spinner />
						) : (
							<TimelineStrip
								ariaLabel={t("stats.orbs.drawer.timeline")}
								from={timeline.data?.from ?? from}
								to={timeline.data?.to ?? Math.min(to, Date.now())}
								segments={timeline.data?.segments ?? []}
							/>
						)}
					</Card>
					{expired ? (
						<p className="text-faint text-xs">
							{t("stats.orbs.drawer.expired")}
						</p>
					) : (
						<>
							<Card title={t("stats.orbs.drawer.cpu")}>
								<LineChart
									ariaLabel={t("stats.orbs.drawer.cpu")}
									points={samples.map((s) => ({ x: s.ts, ys: [s.cpuPct] }))}
									series={[
										{
											key: "cpu",
											label: t("stats.orbs.drawer.cpu"),
											color: "--viz-1",
										},
									]}
									yFloor={100}
									formatX={fmtX}
									formatY={formatPercent}
								/>
							</Card>
							<Card title={t("stats.orbs.drawer.memory")}>
								<LineChart
									ariaLabel={t("stats.orbs.drawer.memory")}
									points={samples.map((s) => ({ x: s.ts, ys: [s.memUsedMB] }))}
									series={[
										{
											key: "mem",
											label: t("stats.orbs.drawer.memUsed"),
											color: "--viz-3",
										},
									]}
									yFloor={memLimit > 0 ? memLimit : undefined}
									formatX={fmtX}
									formatY={formatMemoryMB}
								/>
							</Card>
							{!metrics.isPending && samples.length === 0 && (
								<p className="text-faint text-xs">
									{t("stats.orbs.drawer.noSamples")}
								</p>
							)}
						</>
					)}
					<Card title={t("stats.orbs.drawer.setupRuns")}>
						<ErrorText error={runs.error} />
						{runs.data && runs.data.runs.length === 0 && (
							<p className="text-faint text-xs">
								{t("stats.orbs.drawer.noRuns")}
							</p>
						)}
						<ul className="flex flex-col divide-y divide-border">
							{(runs.data?.runs ?? []).slice(0, 10).map((r) => (
								<li
									key={r.id}
									className="flex items-center gap-2 py-1.5 text-xs"
								>
									<Badge tone={r.ok ? "ok" : "err"}>
										{t(`stats.orbs.drawer.phase.${r.phase}`)}
									</Badge>
									<span className="min-w-0 flex-1 truncate text-muted">
										{formatDateTime(r.startedAt)}
									</span>
									<span className="shrink-0 text-fg tabular-nums">
										{formatDuration(r.durationMs)}
									</span>
								</li>
							))}
						</ul>
					</Card>
				</div>
			</div>
		</div>,
		document.body,
	);
}
