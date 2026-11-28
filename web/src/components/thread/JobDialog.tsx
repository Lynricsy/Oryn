/**
 * 后台任务详情对话框：一个任务的输入、输出与状态。
 *
 * - 状态带：任务字形 + 状态（运行中时长随时间走动）+ 类型与 id；Core 给了小组件时接着显示它的读数与进度。
 * - 输入：完整命令（或代码）、工作目录、超时、输出提醒正则。
 * - 输出：实时帧推来的最近输出（运行中自动跟随到底部，向上翻看时暂停跟随），结束后是交付或轮询到的结果，
 *   都没有时用快照里的预览。Core 只推输出尾巴、没有读全量输出的接口，标签照实说明来处。
 * - 事件：开始、输出匹配、结束，按时间排。
 * - 详细信息：id、类型、所属 agent、工具调用、开始与结束时刻、耗时。
 * - 底部：Core 标了可取消时可以停止（就地确认，与 `/jobs` 报告同一个入口）。
 */
import { type ReactNode, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatDateTime, formatDuration } from "../../lib/format";
import type { JobEvent, JobView } from "../../state/jobs";
import { stripAnsi } from "../../tool-render/util";
import { CopyButton } from "../ui/CopyButton";
import { Badge, KeyValue } from "../ui/data";
import { Icon } from "../ui/icons";
import { Button, Dialog, describeError, EmptyState } from "../ui/primitives";
import {
	JOB_STATUS_LOOK,
	JOB_TONE_TEXT,
	JobGlyph,
	jobElapsed,
	jobHeadline,
} from "./job-parts";
import { JobWidgetCard } from "./job-widget";
import { findJob, openJob, useThreadJobs } from "./jobs-hooks";
import { cancelJob } from "./reports/JobsReport";
import { ConfirmStrip, formatClock, useTicker } from "./reports/parts";

/** 小节标题：与报告里的小节同一种灰色小字，右侧可放操作。 */
function Section({
	title,
	right,
	children,
}: {
	title: string;
	right?: ReactNode;
	children: ReactNode;
}): ReactNode {
	return (
		<section className="flex min-w-0 flex-col gap-2">
			<header className="flex h-6 items-center gap-2">
				<h3 className="font-semibold text-muted text-xs">{title}</h3>
				{right && (
					<span className="ml-auto flex items-center gap-1.5">{right}</span>
				)}
			</header>
			{children}
		</section>
	);
}

/** 状态带：字形 + 状态 + 时长；右侧类型与 id。 */
function StatusBand({ job, now }: { job: JobView; now: number }): ReactNode {
	const { t } = useTranslation();
	const look = JOB_STATUS_LOOK[job.status];
	const elapsed = jobElapsed(job, now);
	const running = job.status === "running";
	return (
		<div className="flex min-w-0 items-center gap-3 rounded-lg bg-raised p-3.5">
			<JobGlyph job={job} size={36} />
			<div className="flex min-w-0 flex-1 flex-col gap-0.5">
				<div className="flex min-w-0 items-baseline gap-2">
					<span
						className={`font-semibold text-base ${running ? "text-fg" : JOB_TONE_TEXT[look.tone]}`}
					>
						{t(`jobs.status.${job.status}`)}
					</span>
					{job.cancellationPending && (
						<Badge tone="warn">{t("report.jobs.stopping")}</Badge>
					)}
					{elapsed !== undefined && (
						<span className="text-muted text-sm tabular-nums">
							{running
								? t("report.jobs.runningFor", { d: formatDuration(elapsed) })
								: t("report.jobs.ranFor", { d: formatDuration(elapsed) })}
						</span>
					)}
				</div>
				<div className="text-faint text-xs tabular-nums">
					{job.startedAt !== undefined &&
						t("report.jobs.started", { time: formatClock(job.startedAt) })}
					{job.status === "ended" && (
						<span className="ml-1.5">{t("jobs.dialog.endedHint")}</span>
					)}
				</div>
			</div>
			<div className="flex shrink-0 flex-col items-end gap-1">
				<Badge>{t(`jobs.type.${job.type}`, { defaultValue: job.type })}</Badge>
				<span className="font-mono text-faint text-xs">{job.id}</span>
			</div>
		</div>
	);
}

/** 输入：命令 / 代码与运行参数。 */
function InputView({ job }: { job: JobView }): ReactNode {
	const { t } = useTranslation();
	const params: { key: string; label: string; value: ReactNode }[] = [];
	if (job.cwd)
		params.push({
			key: "cwd",
			label: t("jobs.dialog.cwd"),
			value: <span className="font-mono text-xs">{job.cwd}</span>,
		});
	if (job.timeoutSec !== undefined)
		params.push({
			key: "timeout",
			label: t("jobs.dialog.timeout"),
			value: formatDuration(job.timeoutSec * 1000),
		});
	if (job.notifyPattern)
		params.push({
			key: "notify",
			label: t("jobs.dialog.notify"),
			value: <span className="font-mono text-xs">/{job.notifyPattern}/</span>,
		});
	return (
		<div className="flex flex-col gap-2.5">
			{job.command ? (
				<div className="relative">
					<pre className="m-0 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-md bg-inset py-2.5 pr-10 pl-3 font-mono text-fg text-xs leading-relaxed">
						{job.type === "bash" && (
							<span aria-hidden className="select-none text-faint">
								${" "}
							</span>
						)}
						{job.command}
					</pre>
					<span className="absolute top-1.5 right-1.5">
						<CopyButton
							text={job.command}
							ariaLabel={t("jobs.dialog.copyCommand")}
						/>
					</span>
				</div>
			) : (
				<p className="m-0 font-mono text-muted text-xs">{job.label}</p>
			)}
			{params.length > 0 && <KeyValue rows={params} />}
		</div>
	);
}

/** 输出井：运行中跟随到底部，用户向上翻看时暂停跟随，翻回底部再继续。 */
function OutputView({ job }: { job: JobView }): ReactNode {
	const { t } = useTranslation();
	const ref = useRef<HTMLPreElement>(null);
	const follow = useRef(true);
	const text = job.output ? stripAnsi(job.output) : "";
	const running = job.status === "running";
	useLayoutEffect(() => {
		const el = ref.current;
		if (el && follow.current && text) el.scrollTop = el.scrollHeight;
	}, [text]);
	if (!text)
		return (
			<div className="rounded-md bg-inset px-3 py-6 text-center text-faint text-xs">
				{running ? t("jobs.dialog.outputWaiting") : t("jobs.dialog.outputNone")}
			</div>
		);
	return (
		<pre
			ref={ref}
			data-testid="job-output"
			onScroll={(e) => {
				const el = e.currentTarget;
				follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
			}}
			className="m-0 max-h-[360px] min-h-24 overflow-auto whitespace-pre-wrap break-words rounded-md bg-inset px-3 py-2.5 font-mono text-fg text-xs leading-relaxed"
		>
			{text}
		</pre>
	);
}

/** 输出来处的说明标签：实时（带呼吸点）/ 最终结果 / 结果预览。 */
function OutputSourceTag({ job }: { job: JobView }): ReactNode {
	const { t } = useTranslation();
	if (!job.output) return null;
	if (job.outputSource === "live" && job.status === "running")
		return (
			<span className="inline-flex items-center gap-1.5 text-faint text-xs">
				<span className="relative inline-flex h-1.5 w-1.5">
					<span className="absolute inset-0 animate-ping rounded-full bg-accent opacity-60 motion-reduce:hidden" />
					<span className="relative h-1.5 w-1.5 rounded-full bg-accent" />
				</span>
				{t("jobs.dialog.outputLive")}
			</span>
		);
	return (
		<span className="text-faint text-xs">
			{t(
				job.outputSource === "result"
					? "jobs.dialog.outputResult"
					: job.outputSource === "preview"
						? "jobs.dialog.outputPreview"
						: "jobs.dialog.outputRecent",
			)}
		</span>
	);
}

const EVENT_DOT: Record<JobEvent["kind"], string> = {
	started: "bg-ghost",
	notify: "bg-frost",
	running: "bg-ghost",
	completed: "bg-ok",
	failed: "bg-err",
	cancelled: "bg-warn",
};

/** 事件时间线：左侧一条竖线串起各事件的圆点。 */
function EventsView({ events }: { events: JobEvent[] }): ReactNode {
	const { t } = useTranslation();
	return (
		<ol className="relative m-0 flex list-none flex-col gap-3 p-0 pl-5">
			<span
				aria-hidden
				className="absolute top-1.5 bottom-1.5 left-[5px] w-px bg-border-strong"
			/>
			{events.map((ev, i) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: 事件按发生顺序追加，不重排
				<li key={i} className="relative flex min-w-0 flex-col gap-1">
					<span
						aria-hidden
						className={`absolute top-[5px] -left-5 h-[11px] w-[11px] rounded-full ring-2 ring-panel ${EVENT_DOT[ev.kind]}`}
					/>
					<div className="flex min-w-0 items-baseline gap-2 text-sm">
						<span className="font-medium">
							{ev.kind === "notify"
								? t("jobs.dialog.event.notify", { pattern: ev.pattern ?? "" })
								: t(`jobs.dialog.event.${ev.kind}`)}
						</span>
						{ev.durationMs !== undefined && (
							<span className="text-faint text-xs tabular-nums">
								{formatDuration(ev.durationMs)}
							</span>
						)}
						{ev.at !== undefined && (
							<span
								className="ml-auto shrink-0 text-faint text-xs tabular-nums"
								title={formatDateTime(ev.at)}
							>
								{formatClock(ev.at)}
							</span>
						)}
					</div>
					{ev.kind === "notify" && ev.lines && ev.lines.length > 0 && (
						<pre className="m-0 max-h-32 overflow-auto whitespace-pre-wrap break-words rounded-md bg-inset px-2.5 py-1.5 font-mono text-muted text-xs">
							{ev.lines.join("\n")}
							{ev.omitted
								? `\n${t("jobs.event.notifyOmitted", { count: ev.omitted })}`
								: ""}
						</pre>
					)}
				</li>
			))}
		</ol>
	);
}

/** 停止任务：就地确认后发 `oryn_cancel_job`（与 `/jobs` 报告同一入口）。 */
function StopControl({
	threadId,
	job,
}: {
	threadId: string;
	job: JobView;
}): ReactNode {
	const { t } = useTranslation();
	const [confirming, setConfirming] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const [notStopped, setNotStopped] = useState(false);
	const stop = async () => {
		setBusy(true);
		setError(null);
		setNotStopped(false);
		try {
			const cancelled = await cancelJob(threadId, job.id);
			setConfirming(false);
			setNotStopped(!cancelled);
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};
	return (
		<div className="flex w-full flex-col gap-2">
			{confirming ? (
				<ConfirmStrip
					label={t("report.jobs.confirmTitle")}
					body={t("report.jobs.confirmBody")}
					cancelLabel={t("report.jobs.keep")}
					confirmLabel={t("report.jobs.confirmStop")}
					danger
					busy={busy}
					onCancel={() => setConfirming(false)}
					onConfirm={() => void stop()}
				/>
			) : (
				<div className="flex justify-end">
					<Button
						size="sm"
						icon="stop"
						disabled={busy || job.cancellationPending}
						onClick={() => setConfirming(true)}
					>
						{t("report.jobs.stop")}
					</Button>
				</div>
			)}
			{error ? (
				<p role="alert" className="m-0 text-err text-xs">
					{t("report.jobs.stopFailed", { reason: describeError(error, t) })}
				</p>
			) : notStopped ? (
				<p role="status" className="m-0 text-warn-ink text-xs">
					{t("report.jobs.notStopped")}
				</p>
			) : null}
		</div>
	);
}

export function JobDialog({
	threadId,
	id,
	jobKey,
	onClose,
}: {
	threadId: string;
	id: string;
	jobKey?: string;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const jobs = useThreadJobs(threadId);
	const job = findJob(jobs, id, jobKey);
	const now = useTicker(job?.status === "running");
	// 同一线程里的其他任务：对话框底部的上一个 / 下一个
	const siblings = jobs.jobs.filter((j) => j.type !== "task");
	const at = job ? siblings.indexOf(job) : -1;
	const prev = at > 0 ? siblings[at - 1] : undefined;
	const next =
		at >= 0 && at < siblings.length - 1 ? siblings[at + 1] : undefined;
	if (!job)
		return (
			<Dialog
				open
				wide
				onClose={onClose}
				title={t("jobs.dialog.title", { id })}
			>
				<EmptyState icon="hourglass" title={t("jobs.dialog.notFoundTitle")}>
					{t("jobs.dialog.notFoundBody")}
				</EmptyState>
			</Dialog>
		);
	const details: { key: string; label: string; value: ReactNode }[] = [
		{
			key: "id",
			label: t("jobs.dialog.id"),
			value: <span className="font-mono text-xs">{job.id}</span>,
		},
	];
	if (job.ownerId)
		details.push({
			key: "owner",
			label: t("jobs.dialog.owner"),
			value: job.ownerId,
		});
	if (job.toolCallId)
		details.push({
			key: "call",
			label: t("jobs.dialog.toolCall"),
			value: (
				<span className="font-mono text-xs">
					{job.toolName} · {job.toolCallId}
				</span>
			),
		});
	if (job.startedAt !== undefined)
		details.push({
			key: "start",
			label: t("jobs.dialog.startedAt"),
			value: formatDateTime(job.startedAt),
		});
	if (job.endedAt !== undefined && job.status !== "running")
		details.push({
			key: "end",
			label: t("jobs.dialog.endedAt"),
			value: formatDateTime(job.endedAt),
		});
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={jobHeadline(job)}
			bodyTestId="job-dialog-body"
			footer={
				job.cancellable || job.cancellationPending ? (
					<StopControl key={job.key} threadId={threadId} job={job} />
				) : prev || next ? (
					<div className="flex w-full items-center justify-between gap-2">
						<Button
							size="sm"
							variant="ghost"
							disabled={!prev}
							onClick={() => prev && openJob(prev.id, prev.key)}
						>
							<Icon name="chevronLeft" size={13} />
							{t("jobs.dialog.prev")}
						</Button>
						<Button
							size="sm"
							variant="ghost"
							disabled={!next}
							onClick={() => next && openJob(next.id, next.key)}
						>
							{t("jobs.dialog.next")}
							<Icon name="chevronRight" size={13} />
						</Button>
					</div>
				) : undefined
			}
		>
			<div className="flex flex-col gap-5">
				<StatusBand job={job} now={now} />
				{job.widget && <JobWidgetCard widget={job.widget} />}
				<Section title={t("jobs.dialog.input")}>
					<InputView job={job} />
				</Section>
				<Section
					title={t("jobs.dialog.output")}
					right={
						<>
							<OutputSourceTag job={job} />
							{job.output && (
								<CopyButton
									text={stripAnsi(job.output)}
									ariaLabel={t("jobs.copyOutput")}
								/>
							)}
						</>
					}
				>
					<OutputView job={job} />
				</Section>
				{job.events.length > 0 && (
					<Section title={t("jobs.dialog.events")}>
						<EventsView events={job.events} />
					</Section>
				)}
				<Section title={t("jobs.dialog.details")}>
					<KeyValue rows={details} />
				</Section>
			</div>
		</Dialog>
	);
}
