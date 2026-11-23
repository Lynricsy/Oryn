/**
 * omp 后台任务相关消息的解析（纯函数，宿主无关：线程页、分享页、信息面板共用）。
 *
 * - `async-result`：后台任务结束后注入会话的交付消息（`details.jobs[]` + `<system-notice>` 正文，
 *   oryn-core `session/async-job-delivery.ts` 与 `prompts/tools/async-result.md`）。
 * - `async-notify`：后台 bash（或常驻进程）输出命中 `notify` 正则时的提醒（`details.reports[]`）。
 * - `launch-completion`：常驻进程（`launch`）退出（`details.daemons[]`）。
 * - 工具结果的 `details.async`（`{ state, jobId, type }`）：这次调用转入了后台。
 * - `wait` / `job` 工具结果的 `details.jobs[]`：等待或轮询到的任务状态。
 */
import { isRecord, num, str } from "../tool-render/util";

export const ASYNC_RESULT_TYPE = "async-result";
export const ASYNC_NOTIFY_TYPE = "async-notify";
export const LAUNCH_COMPLETION_TYPE = "launch-completion";

/** 后台任务事件消息的 customType。 */
export const JOB_EVENT_TYPES: readonly string[] = [
	ASYNC_RESULT_TYPE,
	ASYNC_NOTIFY_TYPE,
	LAUNCH_COMPLETION_TYPE,
];

/** Core 的任务状态；交付消息里只会出现终态。 */
export type JobRunState = "running" | "completed" | "failed" | "cancelled";

const RUN_STATES: Record<string, JobRunState> = {
	running: "running",
	completed: "completed",
	failed: "failed",
	cancelled: "cancelled",
};

export function jobRunState(v: unknown): JobRunState | null {
	const s = str(v);
	return s && Object.hasOwn(RUN_STATES, s) ? (RUN_STATES[s] ?? null) : null;
}

/** 一次交付里的一个任务。 */
export interface DeliveredJob {
	jobId: string;
	/** `bash` / `eval` / `task`；旧消息缺省。 */
	type: string | null;
	/** 交付时的状态；缺省按「已完成」（模板只写 has completed）。 */
	status: JobRunState;
	label: string | null;
	/** 跑子 agent 的任务：它的 registry id。 */
	agentId: string | null;
	durationMs: number | null;
	/** 正文里这个任务的结果段（去掉通知包裹与标题行）。 */
	result: string;
}

const NOTICE_OPEN = /^\s*<system-notice>\s*\n?/;
const NOTICE_CLOSE = /\n?\s*<\/system-notice>\s*$/;
/** 多任务交付的分段行：`── Job bg_1 (label) ──`。 */
const MULTI_HEADER = /^── Job (\S+)(?: \((.*)\))? ──$/;
/** 单任务交付的标题行：`Background job bg_3 (cmd) has completed.` */
const SINGLE_HEADER = /^Background job (\S+)(?: \((.*)\))? has completed\.$/;
const RESUME_LINE = /^Resume your work using the results? below\.$/;

function textOf(content: unknown): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	return content
		.map((b) =>
			isRecord(b) && b.type === "text" && typeof b.text === "string"
				? b.text
				: "",
		)
		.join("\n");
}

/** 把正文按任务切段：单任务取标题行之后的全部，多任务按分段行切。键为 jobId。 */
function resultSections(text: string): Map<string, string> {
	const lines = text
		.replace(NOTICE_OPEN, "")
		.replace(NOTICE_CLOSE, "")
		.split("\n");
	const out = new Map<string, string>();
	const single = SINGLE_HEADER.exec(lines[0]?.trim() ?? "");
	if (single?.[1]) {
		let body = lines.slice(1);
		if (RESUME_LINE.test(body[0]?.trim() ?? "")) body = body.slice(1);
		out.set(single[1], body.join("\n").trim());
		return out;
	}
	let current: string | null = null;
	let buf: string[] = [];
	const flush = () => {
		if (current !== null) out.set(current, buf.join("\n").trim());
	};
	for (const line of lines) {
		const m = MULTI_HEADER.exec(line);
		if (m?.[1]) {
			flush();
			current = m[1];
			buf = [];
		} else if (current !== null) buf.push(line);
	}
	flush();
	return out;
}

/**
 * 解析一条 `async-result` 交付：优先 `details.jobs[]`；旧消息没有 details 时退回正文里的标题行。
 * 认不出任何任务时返回空数组（调用方退回通用卡片）。
 */
export function parseAsyncResult(message: {
	content?: unknown;
	details?: unknown;
}): DeliveredJob[] {
	const text = textOf(message.content);
	const sections = resultSections(text);
	const raw =
		isRecord(message.details) && Array.isArray(message.details.jobs)
			? message.details.jobs
			: [];
	const jobs: DeliveredJob[] = [];
	for (const j of raw) {
		if (!isRecord(j)) continue;
		const jobId = str(j.jobId);
		if (!jobId) continue;
		jobs.push({
			jobId,
			type: str(j.type),
			status: jobRunState(j.status) ?? "completed",
			label: str(j.label),
			agentId: str(j.agentId),
			durationMs: num(j.durationMs),
			result:
				sections.get(jobId) ??
				(raw.length === 1 ? (sections.values().next().value ?? "") : ""),
		});
	}
	if (jobs.length > 0) return jobs;
	for (const [jobId, result] of sections)
		jobs.push({
			jobId,
			type: null,
			status: "completed",
			label: null,
			agentId: null,
			durationMs: null,
			result,
		});
	return jobs;
}

/** `async-notify` 里一个任务 / 常驻进程的匹配。 */
export interface NotifyReport {
	/** 任务 id 或常驻进程名。 */
	id: string;
	kind: "bash" | "service";
	pattern: string;
	lines: string[];
	omitted: number;
	/** 生成提醒时是否仍在运行。 */
	running: boolean;
	/** 匹配太频繁，之后不再提醒。 */
	stopped: boolean;
}

export function parseAsyncNotify(message: {
	details?: unknown;
}): NotifyReport[] {
	const raw =
		isRecord(message.details) && Array.isArray(message.details.reports)
			? message.details.reports
			: [];
	const out: NotifyReport[] = [];
	for (const r of raw) {
		if (!isRecord(r)) continue;
		const id = str(r.id);
		if (!id) continue;
		out.push({
			id,
			kind: r.type === "service" ? "service" : "bash",
			pattern: str(r.pattern) ?? "",
			lines: Array.isArray(r.lines)
				? r.lines.filter((l): l is string => typeof l === "string")
				: [],
			omitted: num(r.omitted) ?? 0,
			running: r.running === true,
			stopped: r.stopped === true,
		});
	}
	return out;
}

/** `launch-completion` 里一个退出的常驻进程。 */
export interface DaemonExit {
	name: string;
	/** Core 的 unit 状态（`exited` / `failed` / `stopped` …），原样显示。 */
	state: string;
	exitCode: number | null;
}

export function parseLaunchCompletion(message: {
	details?: unknown;
}): DaemonExit[] {
	const raw =
		isRecord(message.details) && Array.isArray(message.details.daemons)
			? message.details.daemons
			: [];
	const out: DaemonExit[] = [];
	for (const d of raw) {
		if (!isRecord(d)) continue;
		const name = str(d.key) ?? str(d.name);
		if (!name) continue;
		out.push({
			name,
			state: str(d.state) ?? "",
			exitCode: num(d.exitCode),
		});
	}
	return out;
}

/** 工具结果（或实时帧）的 `details.async`：这次调用转入了后台。 */
export interface AsyncCallFrame {
	jobId: string;
	state: JobRunState;
	type: string | null;
}

export function asyncFrameOf(details: unknown): AsyncCallFrame | null {
	if (!isRecord(details) || !isRecord(details.async)) return null;
	const jobId = str(details.async.jobId);
	const state = jobRunState(details.async.state);
	if (!jobId || !state) return null;
	return { jobId, state, type: str(details.async.type) };
}

/** `wait` / `job`（poll）工具结果里的一个任务快照。 */
export interface PolledJob {
	id: string;
	type: string | null;
	status: JobRunState;
	label: string | null;
	durationMs: number | null;
	resultText: string | null;
	errorText: string | null;
}

/** 会在 `details.jobs[]` 里报告任务状态的工具。 */
const POLL_TOOLS: Record<string, true> = {
	wait: true,
	job: true,
	await: true,
	poll: true,
};

export function polledJobsOf(toolName: string, details: unknown): PolledJob[] {
	if (!Object.hasOwn(POLL_TOOLS, toolName)) return [];
	if (!isRecord(details) || !Array.isArray(details.jobs)) return [];
	const out: PolledJob[] = [];
	for (const j of details.jobs) {
		if (!isRecord(j)) continue;
		const id = str(j.id);
		const status = jobRunState(j.status);
		if (!id || !status) continue;
		out.push({
			id,
			type: str(j.type),
			status,
			label: str(j.label),
			durationMs: num(j.durationMs),
			resultText: str(j.resultText),
			errorText: str(j.errorText),
		});
	}
	return out;
}
