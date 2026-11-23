/**
 * 信息面板的「后台任务」分组：与子 agent 分组并列，列出这个线程的后台命令 / 代码任务（子 agent 的任务在子 agent 分组里）。
 * 运行中的在前（新的在上），之后是最近结束的（默认只列几条，可展开全部）；每行是任务字形 + 意图 + 一行读数，
 * 运行中的读数随时间走动并带上最近一行输出。点开是任务详情对话框。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatDuration } from "../../lib/format";
import type { JobView } from "../../state/jobs";
import { uiStore, useUi } from "../../state/ui";
import { AgentStatusRing } from "../ui/AgentStatusAvatar";
import { ListRow } from "../ui/data";
import { InfoGroup } from "./info-parts";
import {
	JOB_STATUS_LOOK,
	JOB_TONE_TEXT,
	JobGlyph,
	jobElapsed,
	jobHeadline,
	jobStage,
} from "./job-parts";
import { openJob, useThreadJobs } from "./jobs-hooks";
import { useTicker } from "./reports/parts";

/** 收起时列出的已结束任务条数。 */
const RECENT_DONE = 4;

function JobRow({ job, now }: { job: JobView; now: number }): ReactNode {
	const { t } = useTranslation();
	const elapsed = jobElapsed(job, now);
	const running = job.status === "running";
	const stage = running ? jobStage(job) : undefined;
	const look = JOB_STATUS_LOOK[job.status];
	return (
		<li data-job={job.key}>
			<ListRow
				leading={<JobGlyph job={job} />}
				title={
					<span className="min-w-0 truncate" title={jobHeadline(job)}>
						{jobHeadline(job)}
					</span>
				}
				meta={
					<span className="flex min-w-0 items-center gap-1.5 tabular-nums">
						<span className="shrink-0 font-mono">{job.id}</span>
						<span aria-hidden className="shrink-0">
							·
						</span>
						<span
							className={`shrink-0 ${running ? "" : JOB_TONE_TEXT[look.tone]}`}
						>
							{t(`jobs.status.${job.status}`)}
						</span>
						{elapsed !== undefined && (
							<>
								<span aria-hidden className="shrink-0">
									·
								</span>
								<span className="shrink-0">{formatDuration(elapsed)}</span>
							</>
						)}
						{stage && (
							<span className="min-w-0 truncate font-mono text-ghost">
								{stage}
							</span>
						)}
					</span>
				}
				onClick={() => openJob(job.id, job.key)}
			/>
		</li>
	);
}

export function JobsSection({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const { jobs } = useThreadJobs(threadId);
	const collapsed = useUi((s) => !!s.collapsedCards.jobs);
	const [all, setAll] = useState(false);
	// 子 agent 的任务（task）已在子 agent 分组里，这里只列命令 / 代码任务
	const shown = jobs.filter((j) => j.type !== "task");
	const running = shown.filter((j) => j.status === "running").toReversed();
	const done = shown
		.filter((j) => j.status !== "running")
		.toSorted(
			(a, b) =>
				(b.endedAt ?? b.startedAt ?? 0) - (a.endedAt ?? a.startedAt ?? 0),
		);
	const now = useTicker(running.length > 0 && !collapsed);
	// 没有后台任务时不占位，等有了再出现
	if (shown.length === 0) return null;
	const visibleDone = all ? done : done.slice(0, RECENT_DONE);
	return (
		<InfoGroup
			title={t("jobs.panel.title")}
			icon="terminal"
			collapsed={collapsed}
			onToggle={() => uiStore.getState().toggleCard("jobs")}
			right={
				running.length > 0 ? (
					<span className="inline-flex items-center gap-1.5">
						<AgentStatusRing state="running" size={12} />
						{t("jobs.panel.running", { n: running.length })}
					</span>
				) : (
					t("jobs.panel.total", { n: shown.length })
				)
			}
		>
			{/* ListRow 自带 8px 内边距，外加 6px 与分组标题的 14px 对齐 */}
			<ul className="flex flex-col gap-px px-1.5">
				{running.map((j) => (
					<JobRow key={j.key} job={j} now={now} />
				))}
				{visibleDone.map((j) => (
					<JobRow key={j.key} job={j} now={now} />
				))}
			</ul>
			{done.length > RECENT_DONE && (
				<button
					type="button"
					onClick={() => setAll((v) => !v)}
					className="mx-1.5 mt-px flex h-8 w-[calc(100%-12px)] items-center rounded-md px-2 text-left text-faint text-xs transition-colors duration-150 hover:bg-hover hover:text-muted"
				>
					{all
						? t("jobs.panel.showLess")
						: t("jobs.panel.showAll", { n: done.length })}
				</button>
			)}
		</InfoGroup>
	);
}
