/**
 * `job` — 后台任务的轮询 / 取消 / 列表（别名 `await`、`poll`、`cancel_job`），以及 `wait` 共用的任务列表。
 *
 * `details.jobs[]` 是 oryn-core `async/job-control.ts` 的 `snapshotJobs` 快照：状态、类型、标签与意图、
 * 时长、子 agent 的进度（阶段、完成百分比、请求数、花费、上下文）、结果或错误预览。卡片把每个任务画成一行：
 * 状态点 + 类型图标 + 意图（没有则标签）+ 时长，下一行是 id、类型、退出码与进度读数，结果预览收在引用块里。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon, type IconName } from "../../components/ui/icons";
import { formatCost, formatDuration, formatTokens } from "../../lib/format";
import { AgentLink, type CellTone, Note, ProgressCells } from "../parts";
import type { ToolRenderer, ToolRenderHost, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
} from "../util";

export interface JobSnapshotLike {
	id: string;
	/** "bash" | "task" | "eval" upstream; untrusted, so plain string. */
	type: string;
	/** "running" | "completed" | "failed" | "cancelled" upstream. */
	status: string;
	queued: boolean;
	label: string;
	/** What the job is for, in the model's words; shown over a command `label`. */
	intent: string;
	/** 跑进程的任务的完整命令行（`label` 截到 120 字）。 */
	command: string;
	durationMs: number;
	exitCode: number | null;
	stage: string;
	completionPercent: number | null;
	requests: number | null;
	cost: number | null;
	contextTokens: number | null;
	contextWindow: number | null;
	/** 跑子 agent 的任务：它的 registry id（可打开转录）。 */
	agentId: string;
	resultText: string;
	errorText: string;
}

interface CancelOutcomeLike {
	id: string;
	/** "cancelled" | "not_found" | "already_completed" upstream. */
	status: string;
}

function idList(value: unknown): string[] {
	if (!Array.isArray(value)) return [];
	const out: string[] = [];
	for (const item of value) {
		const id = str(item);
		if (id) out.push(id);
	}
	return out;
}

/** `poll` (+ legacy `jobs`/`jobIds`) job ids. */
function pollIds(args: Record<string, unknown>): string[] {
	const poll = idList(args.poll);
	if (poll.length > 0) return poll;
	const jobs = idList(args.jobs);
	return jobs.length > 0 ? jobs : idList(args.jobIds);
}

/** `cancel` (+ legacy single `jobId`) job ids. */
function cancelIds(args: Record<string, unknown>): string[] {
	const cancel = idList(args.cancel);
	if (cancel.length > 0) return cancel;
	const single = str(args.jobId);
	return single ? [single] : [];
}

function jobOf(value: unknown): JobSnapshotLike | null {
	if (!isRecord(value)) return null;
	const id = str(value.id);
	if (!id) return null;
	return {
		id,
		type: str(value.type) ?? "",
		status: str(value.status) ?? "",
		queued: value.queued === true,
		label: str(value.label) ?? "",
		intent: str(value.intent) ?? "",
		command: str(value.command) ?? "",
		durationMs: num(value.durationMs) ?? 0,
		exitCode: num(value.exitCode),
		stage: str(value.stage) ?? "",
		completionPercent: num(value.completionPercent),
		requests: num(value.requests),
		cost: num(value.cost),
		contextTokens: num(value.contextTokens),
		contextWindow: num(value.contextWindow),
		agentId: str(value.agentUrlId) ?? "",
		resultText: str(value.resultText) ?? "",
		errorText: str(value.errorText) ?? "",
	};
}

export function jobsOf(
	details: Record<string, unknown> | null,
): JobSnapshotLike[] {
	const raw = details?.jobs;
	if (!Array.isArray(raw)) return [];
	const out: JobSnapshotLike[] = [];
	for (const item of raw) {
		const job = jobOf(item);
		if (job) out.push(job);
	}
	return out;
}

function cancelOutcomesOf(
	details: Record<string, unknown> | null,
): CancelOutcomeLike[] {
	const raw = details?.cancelled;
	if (!Array.isArray(raw)) return [];
	const out: CancelOutcomeLike[] = [];
	for (const item of raw) {
		if (!isRecord(item)) continue;
		const id = str(item.id);
		if (id) out.push({ id, status: str(item.status) ?? "" });
	}
	return out;
}

type JobStatus = "running" | "completed" | "failed" | "cancelled" | "unknown";

function jobStatus(status: string): JobStatus {
	return status === "running" ||
		status === "completed" ||
		status === "failed" ||
		status === "cancelled"
		? status
		: "unknown";
}

const STATUS_TONE: Record<JobStatus, CellTone> = {
	running: "accent",
	completed: "ok",
	failed: "err",
	cancelled: "warn",
	unknown: "idle",
};

// Running first (what the user is waiting on), then failed, then the rest.
const STATUS_ORDER: Record<JobStatus, number> = {
	running: 0,
	failed: 1,
	cancelled: 2,
	completed: 3,
	unknown: 4,
};

const TYPE_ICON: Record<string, IconName> = {
	bash: "terminal",
	task: "robot",
	eval: "code",
};

/**
 * Task job results arrive in the model-facing `<task-result>` envelope; the
 * wrapper markup is noise to a human — preview the inner body instead.
 */
function stripTaskResultEnvelope(text: string): string {
	if (!text.startsWith("<task-result")) return text;
	const body = /<(output|preview)(?:\s[^>]*)?>\n?([\s\S]*?)\n?<\/\1>/.exec(
		text,
	)?.[2];
	return body?.trim() || text;
}

/** 各状态的任务数。 */
export function tallyJobs(
	jobs: readonly JobSnapshotLike[],
): Record<JobStatus, number> {
	const out: Record<JobStatus, number> = {
		running: 0,
		completed: 0,
		failed: 0,
		cancelled: 0,
		unknown: 0,
	};
	for (const job of jobs) out[jobStatus(job.status)]++;
	return out;
}

/** 头部读数：多个任务时一格一个任务的进度格 + 已结束数 / 总数；失败数单独标红。 */
export function JobsMeta({
	jobs,
}: {
	jobs: readonly JobSnapshotLike[];
}): ReactNode {
	const { t } = useTranslation();
	if (jobs.length === 0) return null;
	const counts = tallyJobs(jobs);
	const settled = jobs.length - counts.running;
	return (
		<>
			{jobs.length > 1 && (
				<>
					<ProgressCells
						tones={sortJobs(jobs).map((j) => STATUS_TONE[jobStatus(j.status)])}
						label={t("toolRender.jobs.settled", {
							settled,
							total: jobs.length,
						})}
					/>
					<span>
						{settled}/{jobs.length}
					</span>
				</>
			)}
			{counts.failed > 0 && (
				<span className="tv-meta-err">
					{t("toolRender.jobs.count.failed", { count: counts.failed })}
				</span>
			)}
			{jobs.length === 1 && counts.failed === 0 && (
				<span
					className={
						counts.completed === 1
							? "tv-meta-ok"
							: counts.cancelled === 1
								? "tv-meta-warn"
								: undefined
					}
				>
					{t(`toolRender.jobs.status.${jobStatus(jobs[0]?.status ?? "")}`)}
				</span>
			)}
		</>
	);
}

function sortJobs(jobs: readonly JobSnapshotLike[]): JobSnapshotLike[] {
	return [...jobs].sort((a, b) => {
		const diff =
			STATUS_ORDER[jobStatus(a.status)] - STATUS_ORDER[jobStatus(b.status)];
		return diff !== 0 ? diff : b.durationMs - a.durationMs;
	});
}

/** 结果 / 错误预览：引用块里显示开头几行，可展开。 */
function JobPreview({
	text,
	error,
}: {
	text: string;
	error: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const lines = text.split("\n");
	const max = 4;
	const long = lines.length > max + 1;
	const shown = long && !open ? lines.slice(0, max).join("\n") : text;
	return (
		<div
			className={`tv-jobrow-preview${error ? " tv-jobrow-preview--err" : ""}`}
		>
			<pre>{shown}</pre>
			{long && (
				<button
					type="button"
					className="tv-expand"
					onClick={() => setOpen((v) => !v)}
				>
					{open
						? t("toolRender.collapse")
						: t("toolRender.moreLines", { count: lines.length - max })}
				</button>
			)}
		</div>
	);
}

function JobRow({
	job,
	host,
}: {
	job: JobSnapshotLike;
	host?: ToolRenderHost;
}): ReactNode {
	const { t } = useTranslation();
	const status = jobStatus(job.status);
	const running = status === "running";
	const title =
		normalizeWs(job.intent) ||
		normalizeWs(job.label) ||
		t("toolRender.jobs.noLabel");
	// 意图做标题时，命令行另起一行；标签本身就是命令时不再重复
	const command = normalizeWs(job.command || job.label);
	const showCommand =
		command !== "" && command !== title && job.type !== "task";
	const preview = (
		job.errorText.trim() || stripTaskResultEnvelope(job.resultText.trim())
	).replace(/\n+$/, "");
	const facts: ReactNode[] = [];
	if (running && job.queued) facts.push(t("toolRender.jobs.status.queued"));
	if (job.exitCode !== null && job.exitCode !== 0)
		facts.push(
			<span key="exit" className="tv-err-text">
				{t("toolRender.jobs.exit", { code: job.exitCode })}
			</span>,
		);
	if (job.requests !== null && job.requests > 0)
		facts.push(t("toolRender.jobs.requests", { count: job.requests }));
	if (job.cost !== null && job.cost > 0) facts.push(formatCost(job.cost));
	if (job.contextTokens !== null && job.contextTokens > 0)
		facts.push(
			job.contextWindow
				? t("toolRender.jobs.context", {
						used: formatTokens(job.contextTokens),
						window: formatTokens(job.contextWindow),
					})
				: t("toolRender.jobs.contextUsed", {
						used: formatTokens(job.contextTokens),
					}),
		);
	const pct =
		running && job.completionPercent !== null
			? Math.max(0, Math.min(100, job.completionPercent))
			: null;
	return (
		<div className={`tv-jobrow tv-jobrow--${status}`}>
			<span className="tv-jobrow-dot" aria-hidden="true" />
			<div className="tv-jobrow-main">
				<div className="tv-jobrow-line">
					<Icon
						name={TYPE_ICON[job.type] ?? "hourglass"}
						size={13}
						className="tv-jobrow-type"
					/>
					<span className="tv-jobrow-title">{title}</span>
					{job.durationMs > 0 && (
						<span className="tv-jobrow-time">
							{formatDuration(job.durationMs)}
						</span>
					)}
				</div>
				{showCommand && <div className="tv-jobrow-cmd">{command}</div>}
				<div className="tv-jobrow-sub">
					{job.agentId && job.type === "task" ? (
						<AgentLink id={job.agentId} host={host} />
					) : (
						<span className="tv-jobrow-id">{job.id}</span>
					)}
					<span>{t(`toolRender.jobs.status.${status}`)}</span>
					{facts.map((fact, i) => (
						<span key={i}>{fact}</span>
					))}
				</div>
				{(pct !== null || (running && job.stage)) && (
					<div className="tv-jobrow-progress">
						{pct !== null && (
							<span className="tv-jobrow-bar" aria-hidden="true">
								<i style={{ width: `${pct}%` }} />
							</span>
						)}
						{pct !== null && <span>~{Math.round(pct)}%</span>}
						{running && job.stage && (
							<span className="tv-jobrow-stage">{job.stage}</span>
						)}
					</div>
				)}
				{preview && !running && (
					<JobPreview text={preview} error={job.errorText.trim() !== ""} />
				)}
			</div>
		</div>
	);
}

/** 任务列表：运行中的排最前，然后失败、取消、完成。`wait` 与 `job` 共用。 */
export function JobList({
	jobs,
	host,
}: {
	jobs: readonly JobSnapshotLike[];
	host?: ToolRenderHost;
}): ReactNode {
	if (jobs.length === 0) return null;
	return (
		<div className="tv-jobrows">
			{sortJobs(jobs).map((job) => (
				<JobRow key={job.id} job={job} host={host} />
			))}
		</div>
	);
}

/** 模型收到的原始报告：任务行已给出同样的信息，默认收起。 */
export function RawReport({ text }: { text: string }): ReactNode {
	const { t } = useTranslation();
	if (!text.trim()) return null;
	return (
		<details className="tv-rawreport">
			<summary>{t("toolRender.jobs.report")}</summary>
			<pre>{text.trim()}</pre>
		</details>
	);
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const poll = pollIds(args);
	const cancel = cancelIds(args);
	const parts: ReactNode[] = [];
	if (cancel.length > 0)
		parts.push(
			<span key="cancel" className="tv-todo-sumop">
				<Icon name="stop" size={12} />
				<span className="tv-trunc">{cancel.join(", ")}</span>
			</span>,
		);
	if (poll.length > 0)
		parts.push(
			<span key="poll" className="tv-todo-sumop">
				<Icon name="refresh" size={12} />
				<span className="tv-trunc">{poll.join(", ")}</span>
			</span>,
		);
	if (args.list === true || parts.length === 0)
		parts.unshift(
			<span key="list" className="tv-muted">
				{t("toolRender.jobs.all")}
			</span>,
		);
	return <>{parts}</>;
}

function Meta({ result }: ToolRenderProps): ReactNode {
	return <JobsMeta jobs={jobsOf(detailsRecord(result))} />;
}

function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const poll = pollIds(args);
	const cancel = cancelIds(args);
	if (poll.length === 0 && cancel.length === 0) return null;
	return (
		<div className="tv-todo-ops">
			{cancel.length > 0 && (
				<div className="tv-todo-op tv-todo-op--warn">
					<span className="tv-todo-op-chip">
						<Icon name="stop" size={11} />
						{t("toolRender.jobs.cancel")}
					</span>
					<span className="tv-todo-op-body tv-mono">{cancel.join(", ")}</span>
				</div>
			)}
			{poll.length > 0 && (
				<div className="tv-todo-op">
					<span className="tv-todo-op-chip">
						<Icon name="refresh" size={11} />
						{t("toolRender.jobs.poll")}
					</span>
					<span className="tv-todo-op-body tv-mono">{poll.join(", ")}</span>
				</div>
			)}
		</div>
	);
}

function Result({ result, host }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const details = detailsRecord(result);
	const jobs = jobsOf(details);
	const badOutcomes = cancelOutcomesOf(details).filter(
		(o) => o.status !== "cancelled" && o.status !== "",
	);
	const text = resultTextOf(result);
	return (
		<>
			{badOutcomes.length > 0 && (
				<Note tone="warn">
					{badOutcomes
						.map(
							(o) =>
								`${o.id}: ${
									o.status === "not_found" || o.status === "already_completed"
										? t(`toolRender.jobs.cancelOutcome.${o.status}`)
										: o.status.replace(/_/g, " ")
								}`,
						)
						.join(" · ")}
				</Note>
			)}
			<JobList jobs={jobs} host={host} />
			{jobs.length > 0 ? (
				<RawReport text={text} />
			) : result?.isError ? (
				<Note tone="err">{text.trim()}</Note>
			) : (
				text.trim() && <span className="tv-faint tv-sans">{text.trim()}</span>
			)}
		</>
	);
}

export const jobRenderer: ToolRenderer = { Summary, Meta, Input, Result };
