/**
 * `wait` — 阻塞等待自己起的后台任务、服务或回复。运行中 Core 每 0.5 秒推一次部分结果
 * （`details.jobs` 快照，见 oryn-core `tools/wait.ts`），卡片据此实时列出正在等的任务：意图、命令、
 * 已运行时长、子 agent 的阶段与进度；头部的已用时长由外壳按调用开始时刻走表。
 * 结束后头部读数说清这次等待的结局（几个完成 / 失败、到时未结束、被消息打断、没有可等的）。
 * 旧转录里消费了一条 peer 消息的等待（`details.waited`）按 irc 渲染。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Note } from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import { detailsRecord, normalizeWs, resultTextOf, truncate } from "../util";
import { ircRenderer } from "./irc";
import {
	JobList,
	type JobSnapshotLike,
	JobsMeta,
	jobsOf,
	RawReport,
} from "./job";

type Outcome =
	| "jobs"
	| "timeout"
	| "interrupted"
	| "nothing"
	| "service"
	| "other";

/** 结局：从 details 与报告文本判断（文本是 Core 固定的英文模板）。 */
function outcomeOf(
	result: ToolResultLike,
	jobs: readonly JobSnapshotLike[],
): Outcome {
	const details = detailsRecord(result);
	if (details?.interrupted === true) return "interrupted";
	const text = resultTextOf(result);
	if (
		/\bwithin \d[\w.]*\. /.test(text) &&
		/\bNo (job|reply|service|message)\b/.test(text)
	)
		return "timeout";
	if (jobs.length > 0) return "jobs";
	if (text.startsWith("Nothing to wait for")) return "nothing";
	if (text.startsWith("A service finished")) return "service";
	return "other";
}

/** 一个任务的称呼：意图，没有则标签，再没有则 id。 */
function jobName(job: JobSnapshotLike): string {
	return normalizeWs(job.intent) || normalizeWs(job.label) || job.id;
}

function Summary(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const current = props.result ?? props.partial;
	if (detailsRecord(props.result)?.waited)
		return <ircRenderer.Summary {...props} args={{ op: "wait" }} />;
	const jobs = jobsOf(detailsRecord(current));
	if (jobs.length === 0) {
		// 运行中没有任务快照：在等回复或服务
		if (props.running)
			return <span className="tv-muted">{t("toolRender.wait.reply")}</span>;
		return <span className="tv-muted">{t("toolRender.wait.idle")}</span>;
	}
	// 先说还在跑的那个：等待就是为了它
	const first = jobs.find((j) => j.status === "running") ?? jobs[0];
	return (
		<span className="tv-sans">
			{first && truncate(jobName(first), 60)}
			{jobs.length > 1 && <span className="tv-faint"> +{jobs.length - 1}</span>}
		</span>
	);
}

/** 头部读数：运行中是各任务的进度格；结束后是结局。 */
function Meta({ result, partial }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (detailsRecord(result)?.waited) return null;
	if (!result) return <JobsMeta jobs={jobsOf(detailsRecord(partial))} />;
	const jobs = jobsOf(detailsRecord(result));
	const outcome = outcomeOf(result, jobs);
	if (outcome === "jobs") return <JobsMeta jobs={jobs} />;
	if (outcome === "timeout")
		return <span className="tv-meta-warn">{t("toolRender.wait.timeout")}</span>;
	if (outcome === "other") return null;
	return <span>{t(`toolRender.wait.${outcome}`)}</span>;
}

/**
 * 只有输出段：wait 没有参数。运行中列出正在等的任务（部分结果每 0.5 秒刷新一次时长与进度）；
 * 结束后列出任务的结局与结果预览，模型收到的原始报告收在末尾。
 */
function Result(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { result, partial, host } = props;
	if (detailsRecord(result)?.waited) {
		return ircRenderer.Result ? (
			<ircRenderer.Result {...props} args={{ op: "wait" }} />
		) : null;
	}
	if (!result) {
		const jobs = jobsOf(detailsRecord(partial));
		return jobs.length > 0 ? (
			<JobList jobs={jobs} host={host} />
		) : (
			<div className="tv-wait-idle">
				<span className="tv-wait-pulse" aria-hidden="true" />
				{t("toolRender.wait.replyLong")}
			</div>
		);
	}
	const jobs = jobsOf(detailsRecord(result));
	const text = resultTextOf(result).trim();
	if (result.isError) return <Note tone="err">{text}</Note>;
	if (jobs.length > 0)
		return (
			<>
				<JobList jobs={jobs} host={host} />
				<RawReport text={text} />
			</>
		);
	return text ? <div className="tv-wait-text">{text}</div> : null;
}

export const waitRenderer: ToolRenderer = {
	Summary,
	Meta,
	Result,
	livePartial: true,
};
