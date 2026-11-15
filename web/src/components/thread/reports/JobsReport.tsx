/**
 * 后台任务报告（`/jobs`）：按 Core 给的树（`tree[].depth`）缩进成一份列表，没有树时退回平铺的 `jobs`；
 * 运行中的任务时长随时间走动，报告每 2 秒自动刷新；Core 标了 `cancellable` 的行可以停止（确认后发 `oryn_cancel_job`）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatDateTime, formatDuration } from "../../../lib/format";
import type { ResponseData, RpcJobTreeRow } from "../../../omp/types";
import { realtime } from "../../../realtime/socket";
import { Badge, type Tone } from "../../ui/data";
import { Icon, type IconName } from "../../ui/icons";
import { Button, describeError, EmptyState } from "../../ui/primitives";
import { ConfirmStrip, formatClock, useTicker } from "./parts";

type JobsData = ResponseData<"get_jobs">;
type Job = NonNullable<RpcJobTreeRow["job"]>;

/** 运行中时报告的自动刷新间隔。 */
export const JOBS_POLL_MS = 2000;

/** 停止一个后台任务（与 TUI `/jobs kill` 同一个按归属限定的入口）；返回 Core 是否真的停了它。 */
export async function cancelJob(
	threadId: string,
	jobId: string,
): Promise<boolean> {
	const r = await realtime.cmd(threadId, { type: "oryn_cancel_job", jobId });
	return r.cancelled;
}
/**
 * 报告展示的任务集合：有树时取树里的任务（含子 agent 后代），没有树才退回 Core 按归属限定的 `jobs`。
 * 轮询、计数与时长走动都按这一份算，与界面上的行一致。
 */
function shownJobs(data: JobsData | undefined): Job[] {
	if (!data) return [];
	return data.tree?.length
		? data.tree.flatMap((r) => (r.job ? [r.job] : []))
		: data.jobs;
}

/** 有仍在变化的任务时才轮询：运行中（含树中的后代），或已请求停止但还没真正退出（`cancellationPending`）。 */
export function jobsRunning(data: JobsData | undefined): boolean {
	return shownJobs(data).some(
		(j) => j.status === "running" || j.cancellationPending === true,
	);
}

/** 运行中不用强调色（强调色只给「需要你」的东西），靠走动的时长与「停止」按钮区分。 */
const STATUS_TONE: Record<Job["status"], Tone> = {
	running: "neutral",
	completed: "ok",
	failed: "err",
	cancelled: "neutral",
};

const TYPE_ICON: Record<Job["type"], IconName> = {
	bash: "terminal",
	task: "robot",
	eval: "code",
};

function JobItem({
	threadId,
	row,
	job,
	cancellable,
	now,
	onChanged,
}: {
	threadId: string;
	row: RpcJobTreeRow;
	job: Job;
	cancellable: boolean;
	now: number;
	onChanged: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const [confirming, setConfirming] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	// Core 回 cancelled=false：任务在确认期间已经结束，或不归当前 agent
	const [notStopped, setNotStopped] = useState(false);
	const running = job.status === "running";
	const end = job.endTime ?? job.endedAt;
	const duration = formatDuration(
		(running ? now : (end ?? now)) - job.startTime,
	);
	const title = job.intent || job.label;
	// 子 agent 的任务以 agent id 作 id 和 label：同名时只写一次
	const agent = row.agentId && row.agentId !== job.id ? row.agentId : null;
	const command = job.command && job.command !== title ? job.command : null;
	const stop = async () => {
		setBusy(true);
		setError(null);
		setNotStopped(false);
		try {
			const cancelled = await cancelJob(threadId, job.id);
			setConfirming(false);
			setNotStopped(!cancelled);
			onChanged();
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};
	const stopping = job.cancellationPending || busy;
	return (
		<div data-job={job.id} className="flex min-w-0 flex-col gap-1.5 py-2.5">
			<div className="flex min-w-0 items-start gap-2.5">
				<span className="mt-0.5 grid h-6 w-6 shrink-0 place-items-center rounded-xs bg-inset text-muted">
					<Icon name={TYPE_ICON[job.type] ?? "timer"} size={13} aria-hidden />
				</span>
				<div className="flex min-w-0 flex-1 flex-col gap-1">
					<p className="break-words text-sm">{title}</p>
					{command && (
						<code
							title={command}
							className="line-clamp-2 break-all font-mono text-2xs text-faint"
						>
							{command}
						</code>
					)}
					<div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-faint text-xs tabular-nums">
						<Badge tone={STATUS_TONE[job.status] ?? "neutral"}>
							{t(`report.jobs.status.${job.status}`)}
						</Badge>
						{job.cancellationPending && (
							<Badge tone="warn">{t("report.jobs.stopping")}</Badge>
						)}
						<span title={formatDateTime(job.startTime)}>
							{t("report.jobs.started", { time: formatClock(job.startTime) })}
						</span>
						<span>·</span>
						<span>
							{running
								? t("report.jobs.runningFor", { d: duration })
								: t("report.jobs.ranFor", { d: duration })}
						</span>
						<span className="font-mono text-2xs">{job.id}</span>
						{agent && (
							<span className="inline-flex items-center gap-1">
								<Icon name="robot" size={11} aria-hidden />
								{agent}
							</span>
						)}
					</div>
					{job.status === "failed" && job.errorPreview && (
						<p className="line-clamp-3 whitespace-pre-wrap break-words text-err text-xs">
							{job.errorPreview}
						</p>
					)}
					{job.status === "completed" && job.resultPreview && (
						<p className="line-clamp-2 whitespace-pre-wrap break-words text-muted text-xs">
							{job.resultPreview}
						</p>
					)}
				</div>
				{cancellable && !confirming && (
					<Button
						size="sm"
						variant="ghost"
						icon="stop"
						disabled={stopping}
						onClick={() => setConfirming(true)}
					>
						{t("report.jobs.stop")}
					</Button>
				)}
			</div>
			{confirming && (
				<ConfirmStrip
					className="ml-8.5"
					label={t("report.jobs.confirmTitle")}
					body={t("report.jobs.confirmBody")}
					cancelLabel={t("report.jobs.keep")}
					confirmLabel={t("report.jobs.confirmStop")}
					danger
					busy={busy}
					onCancel={() => setConfirming(false)}
					onConfirm={() => void stop()}
				/>
			)}
			{error ? (
				<p role="alert" className="ml-8.5 text-err text-xs">
					{t("report.jobs.stopFailed", { reason: describeError(error, t) })}
				</p>
			) : notStopped ? (
				<p role="status" className="ml-8.5 text-warn-ink text-xs">
					{t("report.jobs.notStopped")}
				</p>
			) : null}
		</div>
	);
}

export function JobsReport({
	threadId,
	data,
	onChanged,
}: {
	threadId: string;
	data: JobsData;
	onChanged: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const jobs = shownJobs(data);
	const running = jobs.filter((j) => j.status === "running").length;
	// 与 Dialogs 的轮询同一判定：正在停止的任务时长也还在走
	const live = jobsRunning(data);
	const now = useTicker(live);
	const rows: RpcJobTreeRow[] = data.tree?.length
		? data.tree
		: data.jobs.map((job) => ({ depth: 0, job }));
	if (rows.length === 0)
		return (
			<EmptyState icon="timer" title={t("report.jobs.emptyTitle")}>
				{t("report.jobs.emptyBody")}
			</EmptyState>
		);
	return (
		<div className="flex flex-col gap-2">
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-0.5 text-faint text-xs">
				<span>
					{t("report.jobs.summary", {
						running,
						recent: jobs.length - running,
					})}
				</span>
				<span className="flex-1" />
				{live && (
					<span className="inline-flex items-center gap-1">
						<Icon name="refresh" size={11} aria-hidden />
						{t("report.jobs.autoRefresh")}
					</span>
				)}
			</div>
			<ul className="flex flex-col rounded-md border border-border bg-raised px-3">
				{rows.map((row, i) => {
					const key = row.job?.id ?? `agent:${row.agentId ?? i}`;
					return (
						<li
							key={key}
							className="border-border border-t first:border-t-0"
							style={{ paddingLeft: row.depth * 20 }}
						>
							<div
								className={
									row.depth > 0 ? "border-border-strong border-l pl-3" : ""
								}
							>
								{row.job ? (
									<JobItem
										threadId={threadId}
										row={row}
										job={row.job}
										// 取消按钮只给 Core 标了 `cancellable` 的行（运行中且属于当前 agent）
										cancellable={row.cancellable === true}
										now={now}
										onChanged={onChanged}
									/>
								) : (
									<div
										data-agent-row={row.agentId}
										className="flex items-center gap-2 py-2 text-muted text-xs"
									>
										<Icon name="robot" size={13} aria-hidden />
										<span className="font-medium">{row.agentId}</span>
									</div>
								)}
							</div>
						</li>
					);
				})}
			</ul>
		</div>
	);
}
