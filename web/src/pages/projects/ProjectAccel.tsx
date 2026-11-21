/**
 * 项目设置页的加速部分（M11a）：表单里的「项目快照」开关与「预热 orb」步进器，以及「快照与预热」卡——
 * 快照表（类型、状态、提交前 7 位、血统年龄、大小、使用次数、退役原因）、带确认的「清除快照」、本项目当前的预热 orb。
 * 卡片随后台进展更新：池 orb 取实时 orb 索引；快照表在本项目 orb 的推送后失效，在建 / 预热期间另有低频兜底刷新。
 */
import { useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ReactNode, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import {
	api,
	type Orb,
	type Project,
	type Schemas,
	unwrap,
} from "../../api/client";
import { qk, useLiveOrbs, useProjectSnapshots } from "../../api/queries";
import { useAsync } from "../../components/settings/useAsync";
import { Badge, Card, type Tone } from "../../components/ui/data";
import type { IconName } from "../../components/ui/icons";
import { DockerBadge, WarmBadge } from "../../components/ui/OrbBadges";
import { OrbBead } from "../../components/ui/OrbBead";
import {
	Button,
	ErrorText,
	IconButton,
	Spinner,
	Toggle,
} from "../../components/ui/primitives";
import { confirmAction } from "../../lib/confirm";
import { formatBytes, formatDuration } from "../../lib/format";
import { useBreakpoint } from "../../lib/viewport";
import { indexStore } from "../../state/stores";

type Snapshot = Schemas["ProjectSnapshot"];

/** 每个项目至多预热的 orb 数（服务端同样校验，越界 400）。 */
export const MAX_WARM_ORBS = 3;

/** 只有「可用」带图标；建立中与已退役用中性色文字，不借用别的图标。 */
const STATE_BADGE: Record<Snapshot["state"], { tone: Tone; icon?: IconName }> =
	{
		ready: { tone: "ok", icon: "checkCircle" },
		building: { tone: "neutral" },
		retired: { tone: "neutral" },
	};

/** 已知的退役原因（契约里列出的全部取值）；其余原样显示。 */
const RETIRE_REASONS = [
	"config_changed",
	"expired",
	"image_missing",
	"refresh_failed",
	"depth",
	"superseded",
	"quota",
	"manual",
	"project_deleted",
	"provider_removed",
] as const;
type RetireReason = (typeof RETIRE_REASONS)[number];

/** 「项目快照」开关 + 说明。 */
export function SnapshotsToggle({
	checked,
	onChange,
}: {
	checked: boolean;
	onChange: (v: boolean) => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div className="flex flex-col gap-1">
			<Toggle
				checked={checked}
				onChange={onChange}
				label={t("accel.snapshotsToggle")}
			/>
			<span className="text-faint text-xs">{t("accel.snapshotsHint")}</span>
		</div>
	);
}

/** 「预热 orb」步进器（0–3）+ 说明（全局上限、常驻占用内存）。 */
export function WarmOrbsStepper({
	value,
	onChange,
}: {
	value: number;
	onChange: (v: number) => void;
}): ReactNode {
	const { t } = useTranslation();
	const n = Math.min(MAX_WARM_ORBS, Math.max(0, value));
	return (
		<fieldset className="flex min-w-0 flex-col gap-1">
			<legend className="mb-1 font-medium text-muted text-xs">
				{t("accel.warmOrbs")}
			</legend>
			<div className="inline-flex h-8 w-fit items-center gap-1 rounded-md border border-border bg-inset px-1">
				<IconButton
					icon="minus"
					box="xs"
					size={13}
					label={t("accel.warmDec")}
					disabled={n <= 0}
					onClick={() => onChange(n - 1)}
				/>
				<output
					data-testid="warm-orbs-value"
					aria-live="polite"
					className="w-6 text-center font-medium text-sm tabular-nums"
				>
					{n}
				</output>
				<IconButton
					icon="plus"
					box="xs"
					size={13}
					label={t("accel.warmInc")}
					disabled={n >= MAX_WARM_ORBS}
					onClick={() => onChange(n + 1)}
				/>
			</div>
			<span className="text-faint text-xs">{t("accel.warmHint")}</span>
		</fieldset>
	);
}

const shortCommit = (c: string | undefined) => (c ? c.slice(0, 7) : "—");

/** 一次状态迁移常连着几条推送：窗口内只刷新一次快照表。 */
export const PUSH_REFRESH_WINDOW_MS = 300;

/** orb 在这些状态下跑 setup / 拍快照：同状态的旁注推送（快照 building / ready / failed）也可能改变快照表。 */
const SETUP_STATES: Partial<Record<Orb["state"], true>> = {
	connecting: true,
	setting_up: true,
	resuming: true,
};

/**
 * 本项目 orb 的 WS 推送 → 失效快照表。快照发布进展（building / ready / failed）在服务端会给来源 orb 写旁注并推送，
 * 但推送本身不带快照；所以在「状态或池变化」「orb 正在 setup / 预热」「表里有在建快照」时刷新，其余（就绪 orb 的
 * 桌面、服务等推送）不打扰。订阅随卡片卸载而解除。
 */
function useSnapshotRefreshOnPush(projectId: string, building: boolean): void {
	const qc = useQueryClient();
	const buildingRef = useRef(building);
	useEffect(() => {
		buildingRef.current = building;
	}, [building]);
	useEffect(() => {
		let timer: ReturnType<typeof setTimeout> | undefined;
		const unsub = indexStore.subscribe((s, prev) => {
			if (s.orbs === prev.orbs || timer !== undefined) return;
			const relevant = Object.values(s.orbs).some((o) => {
				const p = prev.orbs[o.id];
				if (o === p || o.projectId !== projectId) return false;
				return (
					!p ||
					p.state !== o.state ||
					p.pool !== o.pool ||
					buildingRef.current ||
					SETUP_STATES[o.state] ||
					o.pool === "warming"
				);
			});
			if (!relevant) return;
			timer = setTimeout(() => {
				timer = undefined;
				void qc.invalidateQueries({
					queryKey: qk.projectSnapshots(projectId),
				});
			}, PUSH_REFRESH_WINDOW_MS);
		});
		return () => {
			unsub();
			clearTimeout(timer);
		};
	}, [projectId, qc]);
}

/** 快照与预热卡：快照表 + 清除快照 + 本项目的预热 orb。 */
export function AccelCard({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const orbs = useLiveOrbs();
	const pool = orbs.filter(
		(o) =>
			o.projectId === project.id &&
			o.pool &&
			o.state !== "deleted" &&
			o.state !== "deleting",
	);
	const snaps = useProjectSnapshots(
		project.id,
		pool.some((o) => o.pool === "warming"),
	);
	const clear = useAsync();
	const list = snaps.data?.snapshots ?? [];
	const anyReady = list.some((s) => s.state === "ready");
	useSnapshotRefreshOnPush(
		project.id,
		list.some((s) => s.state === "building"),
	);
	return (
		<Card
			title={t("accel.cardTitle")}
			icon="snapshot"
			action={
				anyReady && (
					<Button
						size="sm"
						variant="danger"
						icon="trash"
						disabled={clear.busy}
						onClick={async () => {
							if (
								!(await confirmAction({
									title: t("accel.clearConfirm", { name: project.name }),
									body: t("accel.clearConfirmBody"),
									action: t("common.clear"),
									danger: true,
								}))
							)
								return;
							void clear.run(async () => {
								await unwrap(
									api.DELETE("/api/v1/projects/{id}/snapshots", {
										params: { path: { id: project.id } },
									}),
								);
								await qc.invalidateQueries({
									queryKey: qk.projectSnapshots(project.id),
								});
							});
						}}
					>
						{t("accel.clear")}
					</Button>
				)
			}
		>
			<div className="flex flex-col gap-3" data-testid="project-accel">
				<ErrorText error={clear.error ?? snaps.error} />
				<section className="flex flex-col gap-1.5">
					<h4 className="font-medium text-2xs text-faint">
						{t("accel.snapshots")}
					</h4>
					{!project.snapshots && (
						<p className="text-muted text-xs">{t("accel.snapshotsOff")}</p>
					)}
					{snaps.isPending ? (
						<Spinner />
					) : list.length === 0 ? (
						project.snapshots && (
							<p data-testid="snapshots-empty" className="text-faint text-xs">
								{t("accel.snapshotsEmpty")}
							</p>
						)
					) : (
						<SnapshotList list={list} />
					)}
				</section>
				<section className="flex flex-col gap-1.5">
					<h4 className="font-medium text-2xs text-faint">
						{t("accel.pool.title")}
					</h4>
					{pool.length === 0 ? (
						<p data-testid="pool-empty" className="text-faint text-xs">
							{t(
								project.warmOrbs > 0 ? "accel.pool.pending" : "accel.pool.none",
							)}
						</p>
					) : (
						<ul className="-mx-2 flex flex-col">
							{pool.map((o) => (
								<PoolOrbRow key={o.id} orb={o} />
							))}
						</ul>
					)}
				</section>
			</div>
		</Card>
	);
}

function StateBadge({ s }: { s: Snapshot }): ReactNode {
	const { t } = useTranslation();
	const b = STATE_BADGE[s.state];
	return (
		<Badge tone={b.tone} icon={b.icon}>
			{t(`accel.state.${s.state}`)}
		</Badge>
	);
}

function reasonText(t: TFunction, s: Snapshot): string {
	const r = s.retireReason;
	if (s.state !== "retired" || !r) return "";
	return RETIRE_REASONS.includes(r as RetireReason)
		? t(`accel.reason.${r as RetireReason}`)
		: r;
}

/** 宽屏为表格，手机为两行的列表。 */
function SnapshotList({ list }: { list: readonly Snapshot[] }): ReactNode {
	const { t } = useTranslation();
	const compact = useBreakpoint() === "compact";
	const now = Date.now();
	const age = (s: Snapshot) => formatDuration(Math.max(0, now - s.lineageAt));
	const size = (s: Snapshot) =>
		s.sizeBytes === undefined ? "—" : formatBytes(s.sizeBytes);
	if (compact)
		return (
			<ul data-testid="snapshot-list" className="flex flex-col">
				{list.map((s) => (
					<li
						key={s.id}
						data-snapshot={s.id}
						className="flex flex-col gap-0.5 border-border border-t py-1.5 first:border-t-0"
					>
						<span className="flex min-w-0 items-center gap-1.5 text-sm">
							<span
								className="min-w-0 truncate font-medium font-mono text-xs"
								title={s.providerName ?? s.providerId}
							>
								{s.type}
							</span>
							<StateBadge s={s} />
							<span className="min-w-0 truncate text-faint text-xs">
								{reasonText(t, s)}
							</span>
						</span>
						<span className="flex flex-wrap items-center gap-x-2.5 text-faint text-xs tabular-nums">
							<span className="font-mono">{shortCommit(s.baseCommit)}</span>
							<span>{age(s)}</span>
							<span>{size(s)}</span>
							<span>{t("accel.uses", { count: s.useCount })}</span>
						</span>
					</li>
				))}
			</ul>
		);
	return (
		<div className="-mx-1 overflow-x-auto">
			<table data-testid="snapshot-list" className="w-full text-xs">
				<thead>
					<tr className="text-left text-faint">
						<th className="px-1 py-1 font-medium">{t("accel.col.type")}</th>
						<th className="px-1 py-1 font-medium">{t("accel.col.state")}</th>
						<th className="px-1 py-1 font-medium">{t("accel.col.commit")}</th>
						<th className="px-1 py-1 text-right font-medium">
							{t("accel.col.age")}
						</th>
						<th className="px-1 py-1 text-right font-medium">
							{t("accel.col.size")}
						</th>
						<th className="px-1 py-1 text-right font-medium">
							{t("accel.col.uses")}
						</th>
						<th className="py-1 pr-1 pl-4 font-medium">
							{t("accel.col.reason")}
						</th>
					</tr>
				</thead>
				<tbody>
					{list.map((s) => (
						<tr
							key={s.id}
							data-snapshot={s.id}
							className="border-border border-t text-muted"
						>
							<td
								className="whitespace-nowrap px-1 py-1.5 font-medium font-mono text-fg"
								title={s.providerName ?? s.providerId}
							>
								{s.type}
							</td>
							<td className="px-1 py-1.5">
								<StateBadge s={s} />
							</td>
							<td className="whitespace-nowrap px-1 py-1.5 font-mono">
								{shortCommit(s.baseCommit)}
							</td>
							<td className="whitespace-nowrap px-1 py-1.5 text-right tabular-nums">
								{age(s)}
							</td>
							<td className="whitespace-nowrap px-1 py-1.5 text-right tabular-nums">
								{size(s)}
							</td>
							<td className="px-1 py-1.5 text-right tabular-nums">
								{s.useCount}
							</td>
							<td className="py-1.5 pr-1 pl-4 text-faint">
								{reasonText(t, s)}
							</td>
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}

function PoolOrbRow({ orb }: { orb: Orb }): ReactNode {
	const { t } = useTranslation();
	const pool = orb.pool;
	if (!pool) return null;
	return (
		<li
			data-pool-orb={orb.id}
			className="flex min-h-9 flex-wrap items-center gap-x-2 gap-y-0.5 rounded-md px-2 py-1 text-sm"
		>
			<OrbBead state={orb.state} />
			<span className="min-w-0 truncate font-mono text-xs">
				{orb.id.slice(0, 16)}
			</span>
			<Badge>{orb.type}</Badge>
			{orb.nestedDocker && <DockerBadge />}
			<WarmBadge pool={pool} />
			<span className="min-w-0 basis-full truncate pl-6 text-faint text-xs sm:basis-auto sm:pl-0">
				{t(`accel.pool.${pool}`)}
			</span>
		</li>
	);
}
