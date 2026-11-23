/**
 * 转录里的后台任务：
 * - `BackgroundToolView`：转入后台的工具卡（结果带 `details.async`），头部标记与展开体跟着任务的实时状态走；
 * - `JobEventMessage`：后台任务注入会话的消息解析成事件行——任务结束（`async-result`）、输出命中提醒
 *   （`async-notify`）、常驻进程退出（`launch-completion`）。认不出时渲染调用方给的通用卡片。
 *
 * 事件行与工具卡同一几何（38px 头部、22px 图标方块、左内边距 7px），点开是一层内嵌井放结果正文。
 */
import type { TFunction } from "i18next";
import { type ReactNode, useContext, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	ASYNC_NOTIFY_TYPE,
	ASYNC_RESULT_TYPE,
	type DaemonExit,
	type DeliveredJob,
	LAUNCH_COMPLETION_TYPE,
	type NotifyReport,
	parseAsyncNotify,
	parseAsyncResult,
	parseLaunchCompletion,
} from "../../lib/async-jobs";
import { formatDuration } from "../../lib/format";
import type { JsonRecord } from "../../state/message-builder";
import { ToolView, type ToolViewProps } from "../../tool-render/ToolView";
import { Markdown } from "../markdown/Markdown";
import { JOB_STATUS_LOOK, JOB_TONE_TEXT } from "../thread/job-parts";
import { openJob, useThreadJobs } from "../thread/jobs-hooks";
import { CopyButton } from "../ui/CopyButton";
import type { Tone } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { TranscriptContext } from "./context";

/** 转入后台的工具卡：把线程任务视图里这次调用的任务交给卡片。 */
export function BackgroundToolView({
	threadId,
	toolCallId,
	openable,
	view,
}: {
	threadId: string;
	toolCallId: string;
	/** 只读（分享页）不给任务入口。 */
	openable: boolean;
	view: ToolViewProps;
}): ReactNode {
	const jobs = useThreadJobs(threadId);
	const key = jobs.byToolCall.get(toolCallId);
	const job = key ? jobs.byKey.get(key) : undefined;
	return (
		<ToolView
			{...view}
			job={
				job && {
					id: job.id,
					status: job.status,
					output: job.output,
					onOpen: openable ? () => openJob(job.id, job.key) : undefined,
				}
			}
		/>
	);
}

const TONE_BOX: Record<Tone, string> = {
	neutral: "bg-inset text-muted",
	accent: "bg-accent-soft text-accent",
	ok: "bg-ok/12 text-ok",
	warn: "bg-warn/15 text-warn",
	err: "bg-err/10 text-err",
	frost: "bg-frost/12 text-frost",
};

/** 事件行外壳：一行头部（图标方块 + 标题 + 等宽细节 + 读数）；有正文时整行可点开。 */
function EventShell({
	icon,
	tone,
	title,
	detail,
	meta,
	children,
}: {
	icon: IconName;
	tone: Tone;
	title: string;
	detail?: string | null;
	meta?: ReactNode;
	children?: ReactNode;
}): ReactNode {
	const [open, setOpen] = useState(false);
	const head = (
		<>
			<span
				aria-hidden
				className={`grid h-[22px] w-[22px] shrink-0 place-items-center rounded-sm ${TONE_BOX[tone]}`}
			>
				<Icon name={icon} size={13} />
			</span>
			<span className="min-w-0 truncate font-semibold text-[12.5px] text-fg">
				{title}
			</span>
			<span className="min-w-0 flex-1 truncate font-mono text-faint text-xs">
				{detail}
			</span>
			{meta && (
				<span className="shrink-0 text-[11.5px] text-faint tabular-nums">
					{meta}
				</span>
			)}
		</>
	);
	const row =
		"flex min-h-[38px] w-full min-w-0 items-center gap-2.5 pr-2.5 pl-[7px]";
	return (
		<section
			data-testid="job-event"
			className="overflow-hidden rounded-lg border border-border"
		>
			{children ? (
				<button
					type="button"
					aria-expanded={open}
					onClick={() => setOpen((v) => !v)}
					className={`${row} text-left transition-colors duration-150 hover:bg-hover/60`}
				>
					{head}
					<Icon
						name="chevronRight"
						size={12}
						aria-hidden
						className={`shrink-0 text-ghost transition-transform duration-150 ${open ? "rotate-90" : ""}`}
					/>
				</button>
			) : (
				<div className={row}>{head}</div>
			)}
			{open && children && (
				<div className="flex flex-col gap-2.5 border-border border-t bg-inset px-3 py-2.5">
					{children}
				</div>
			)}
		</section>
	);
}

/** 结果正文：子 agent 的结果是 Markdown，命令 / 代码的输出等宽原样排。 */
function ResultText({
	text,
	markdown,
}: {
	text: string;
	markdown: boolean;
}): ReactNode {
	const { t } = useTranslation();
	if (!text)
		return <p className="m-0 text-faint text-xs">{t("jobs.event.noOutput")}</p>;
	if (markdown)
		return (
			<div className="max-h-96 overflow-auto text-sm">
				<Markdown text={text} />
			</div>
		);
	return (
		<pre className="m-0 max-h-72 overflow-auto whitespace-pre-wrap break-words font-mono text-fg text-xs leading-relaxed">
			{text}
		</pre>
	);
}

/** 正文下的操作：打开任务详情（子 agent 打开它的对话框）与复制结果。 */
function JobActions({
	job,
	openable,
	onOpenAgent,
}: {
	job: DeliveredJob;
	openable: boolean;
	onOpenAgent?: (id: string) => void;
}): ReactNode {
	const { t } = useTranslation();
	const agent = job.type === "task" ? (job.agentId ?? job.jobId) : null;
	return (
		<div className="-mb-1 flex items-center gap-1">
			{openable &&
				(agent && onOpenAgent ? (
					<EventLink onClick={() => onOpenAgent(agent)}>
						{t("jobs.event.openAgent")}
					</EventLink>
				) : (
					!agent && (
						<EventLink onClick={() => openJob(job.jobId)}>
							{t("jobs.open")}
						</EventLink>
					)
				))}
			<span className="flex-1" />
			{job.result && (
				<CopyButton text={job.result} ariaLabel={t("jobs.copyOutput")} />
			)}
		</div>
	);
}

function EventLink({
	onClick,
	children,
}: {
	onClick: () => void;
	children: ReactNode;
}): ReactNode {
	return (
		<button
			type="button"
			onClick={onClick}
			className="-ml-1.5 inline-flex h-6 items-center gap-0.5 rounded-sm px-1.5 font-medium text-muted text-xs transition-colors duration-150 hover:bg-hover hover:text-fg"
		>
			{children}
			<Icon name="chevronRight" size={11} aria-hidden />
		</button>
	);
}

function jobTitle(t: TFunction, job: DeliveredJob): string {
	return job.type === "task"
		? t(`jobs.event.agent.${job.status}`, {
				name: job.label || job.agentId || job.jobId,
			})
		: t(`jobs.event.job.${job.status}`, { id: job.jobId });
}

/** `async-result`：一个任务一行；一次交付多个任务时汇成一行，点开逐个列出。 */
function JobResultEvent({
	jobs,
	openable,
	onOpenAgent,
}: {
	jobs: DeliveredJob[];
	openable: boolean;
	onOpenAgent?: (id: string) => void;
}): ReactNode {
	const { t } = useTranslation();
	const first = jobs[0];
	if (!first) return null;
	if (jobs.length === 1) {
		const look = JOB_STATUS_LOOK[first.status];
		return (
			<EventShell
				icon={look.icon}
				tone={look.tone}
				title={jobTitle(t, first)}
				detail={first.type === "task" ? null : first.label}
				meta={first.durationMs !== null && formatDuration(first.durationMs)}
			>
				<ResultText text={first.result} markdown={first.type === "task"} />
				<JobActions job={first} openable={openable} onOpenAgent={onOpenAgent} />
			</EventShell>
		);
	}
	const failed = jobs.filter((j) => j.status === "failed").length;
	return (
		<EventShell
			icon={failed > 0 ? "xCircle" : "checkCircle"}
			tone={failed > 0 ? "err" : "ok"}
			title={t("jobs.event.many", { count: jobs.length })}
			detail={jobs.map((j) => j.jobId).join(" · ")}
			meta={
				failed > 0 ? t("jobs.event.manyFailed", { count: failed }) : undefined
			}
		>
			<ul className="m-0 flex list-none flex-col divide-y divide-border p-0">
				{jobs.map((j) => {
					const look = JOB_STATUS_LOOK[j.status];
					return (
						<li
							key={j.jobId}
							className="flex flex-col gap-2 py-2.5 first:pt-0 last:pb-0"
						>
							<div className="flex min-w-0 items-center gap-2 text-xs">
								<Icon
									name={look.icon}
									size={14}
									aria-hidden
									className={`shrink-0 ${JOB_TONE_TEXT[look.tone]}`}
								/>
								<span className="shrink-0 font-medium text-fg">
									{jobTitle(t, j)}
								</span>
								<span className="min-w-0 flex-1 truncate font-mono text-faint">
									{j.type === "task" ? "" : j.label}
								</span>
								{j.durationMs !== null && (
									<span className="shrink-0 text-faint tabular-nums">
										{formatDuration(j.durationMs)}
									</span>
								)}
							</div>
							<ResultText text={j.result} markdown={j.type === "task"} />
							<JobActions
								job={j}
								openable={openable}
								onOpenAgent={onOpenAgent}
							/>
						</li>
					);
				})}
			</ul>
		</EventShell>
	);
}

/** `async-notify`：每个任务 / 常驻进程一行，收起时露出第一条匹配行。 */
function NotifyEvent({
	report,
	openable,
}: {
	report: NotifyReport;
	openable: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const total = report.lines.length + report.omitted;
	return (
		<EventShell
			icon="bell"
			tone="neutral"
			title={t(
				report.kind === "service"
					? "jobs.event.notifyService"
					: "jobs.event.notify",
				{ id: report.id, pattern: report.pattern },
			)}
			detail={report.lines[0]}
			meta={t("jobs.event.lines", { count: total })}
		>
			<pre className="m-0 max-h-72 overflow-auto whitespace-pre-wrap break-words font-mono text-fg text-xs leading-relaxed">
				{report.lines.join("\n")}
			</pre>
			{(report.omitted > 0 || report.stopped || !report.running) && (
				<ul className="m-0 flex list-none flex-col gap-0.5 p-0 text-faint text-xs">
					{report.omitted > 0 && (
						<li>{t("jobs.event.notifyOmitted", { count: report.omitted })}</li>
					)}
					{report.stopped && <li>{t("jobs.event.notifyStopped")}</li>}
					{!report.running && <li>{t("jobs.event.notifyEnded")}</li>}
				</ul>
			)}
			{openable && report.kind === "bash" && (
				<div className="-mb-1 flex">
					<EventLink onClick={() => openJob(report.id)}>
						{t("jobs.open")}
					</EventLink>
				</div>
			)}
		</EventShell>
	);
}

/** `launch-completion`：常驻进程退出，没有正文。 */
function DaemonExitEvent({ exit }: { exit: DaemonExit }): ReactNode {
	const { t } = useTranslation();
	const failed = exit.exitCode !== null && exit.exitCode !== 0;
	return (
		<EventShell
			icon="terminal"
			tone={failed ? "err" : "neutral"}
			title={t("jobs.event.daemon", { name: exit.name })}
			detail={exit.state}
			meta={
				exit.exitCode !== null
					? t("jobs.event.exitCode", { code: exit.exitCode })
					: undefined
			}
		/>
	);
}

/** 后台任务注入的消息：认得出就渲染成事件行，否则渲染 `fallback`。 */
export function JobEventMessage({
	message,
	fallback,
}: {
	message: JsonRecord;
	fallback: ReactNode;
}): ReactNode {
	const ctx = useContext(TranscriptContext);
	const openable = ctx !== null && ctx.readOnly !== true;
	switch (message.customType) {
		case ASYNC_RESULT_TYPE: {
			const jobs = parseAsyncResult(message);
			if (jobs.length === 0) return fallback;
			return (
				<JobResultEvent
					jobs={jobs}
					openable={openable}
					onOpenAgent={ctx?.openSubagent}
				/>
			);
		}
		case ASYNC_NOTIFY_TYPE: {
			const reports = parseAsyncNotify(message);
			if (reports.length === 0) return fallback;
			return (
				<div className="flex flex-col gap-1.5">
					{reports.map((r) => (
						<NotifyEvent
							key={`${r.kind}:${r.id}`}
							report={r}
							openable={openable}
						/>
					))}
				</div>
			);
		}
		case LAUNCH_COMPLETION_TYPE: {
			const exits = parseLaunchCompletion(message);
			if (exits.length === 0) return fallback;
			return (
				<div className="flex flex-col gap-1.5">
					{exits.map((d) => (
						<DaemonExitEvent key={d.name} exit={d} />
					))}
				</div>
			);
		}
		default:
			return fallback;
	}
}
