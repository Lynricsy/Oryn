/**
 * 线程的后台任务视图：把三路来源合成一份按任务的列表，供工具卡标记、转录事件、信息面板与任务对话框共用。
 *
 * - 转录（永久）：后台调用的工具结果（`details.async`）记下任务的启动、输入与所属工具调用；`async-result` 交付、
 *   `wait` / `job` 轮询结果记下终态与结果；`async-notify` 记下输出匹配。
 * - 实时帧：后台 bash 结束调用后仍以同一 toolCallId 推 `tool_execution_update`（Core `onDetachedToolUpdate`），
 *   正文是输出尾巴，`details.async.state` 在任务结束时变为终态。
 * - `get_jobs` 快照：Core 注册表里还在的任务（运行中与最近结束的），带可取消、停止中、预览与小组件。
 *
 * 合并规则：终态以转录 / 实时帧为准（快照可能比它们旧）；快照补齐转录没有的字段与转录里没有的任务（子 agent 的任务、
 * 未加载的早期条目）。同一个 id 在 Core 重启后可能复用，转录里再次启动时另起一条（key 加 `#n`）。
 */
import type { Entry } from "../api/client";
import {
	ASYNC_NOTIFY_TYPE,
	ASYNC_RESULT_TYPE,
	asyncFrameOf,
	type JobRunState,
	jobRunState,
	parseAsyncNotify,
	parseAsyncResult,
	polledJobsOf,
} from "../lib/async-jobs";
import type { ResponseData, RpcJobTreeRow } from "../omp/types";
import { isRecord, num, str } from "../tool-render/util";
import type { JsonRecord } from "./message-builder";
import type { LiveTool } from "./thread-reducer";
import { isMainFile } from "./transcript";

export type JobsSnapshot = ResponseData<"get_jobs">;
type SnapJob = NonNullable<RpcJobTreeRow["job"]>;
export type JobWidget = NonNullable<SnapJob["widget"]>;

/** `ended`：转录里启动过、快照里已没有、也没收到终态（结果被别处消费，或 Core 重启丢了它）。 */
export type JobStatus = JobRunState | "ended";

export type JobEventKind = "started" | "notify" | JobRunState;

export interface JobEvent {
	kind: JobEventKind;
	/** 毫秒时间戳；不知道时缺省。 */
	at?: number;
	/** notify：命中的正则与行。 */
	pattern?: string;
	lines?: string[];
	omitted?: number;
	/** 终态：结果或错误正文。 */
	text?: string;
	durationMs?: number;
}

export interface JobView {
	/** 列表与路由用的唯一键（同 id 复用时带 `#n`）。 */
	key: string;
	/** Core 的任务 id（`bg_3`）。 */
	id: string;
	/** `bash` / `eval` / `task`；不知道时为工具名。 */
	type: string;
	status: JobStatus;
	/** 一行名字：意图之外的标签（命令首行、子 agent 名）。 */
	label: string;
	/** 模型写的意图（`i`）。 */
	intent?: string;
	/** 完整命令 / 代码。 */
	command?: string;
	cwd?: string;
	timeoutSec?: number;
	/** bash `notify` 正则。 */
	notifyPattern?: string;
	/** 启动它的工具调用。 */
	toolCallId?: string;
	toolName?: string;
	startedAt?: number;
	endedAt?: number;
	durationMs?: number;
	/** 最近的输出（实时尾巴或最终结果）。 */
	output?: string;
	/** 输出的来处：实时帧、交付 / 轮询结果、快照预览。 */
	outputSource?: "live" | "result" | "preview";
	/** 失败时的错误正文（快照预览或交付结果）。 */
	error?: string;
	events: JobEvent[];
	/** Core 标了可取消（运行中、属于当前 agent）。 */
	cancellable: boolean;
	cancellationPending: boolean;
	/** 注册它的 agent（子 agent 的任务不是主 agent 的）。 */
	ownerId?: string;
	/** 跑子 agent 的任务：子 agent 的 registry id。 */
	agentId?: string;
	/** 快照树里的层级：0 = 主 agent 自己的任务。 */
	depth: number;
	caption?: string;
	widget?: JobWidget;
	/** 快照里有这条（Core 还记着它）。 */
	inSnapshot: boolean;
}

export interface ThreadJobs {
	/** 启动顺序（早的在前）。 */
	jobs: JobView[];
	/** toolCallId → 任务 key。 */
	byToolCall: ReadonlyMap<string, string>;
	/** 任务 key → 任务。 */
	byKey: ReadonlyMap<string, JobView>;
}

export function isTerminal(status: JobStatus): boolean {
	return status !== "running";
}

function messageOfEntry(e: Entry): JsonRecord | null {
	const p = e.payload;
	if (!isRecord(p)) return null;
	if (e.type === "message") return isRecord(p.message) ? p.message : null;
	// 注入消息落盘为 custom_message 条目，字段与 role=custom 的消息相同
	if (e.type === "custom_message") return { ...p, role: "custom" };
	return null;
}

interface ToolCallInfo {
	name: string;
	args: Record<string, unknown>;
	intent?: string;
}

function firstLine(text: string): string {
	const line = text.trim().split("\n")[0] ?? "";
	return line.length > 120 ? `${line.slice(0, 119)}…` : line;
}

/** 转录这一路的中间结果（只依赖条目，按条目数组缓存）。 */
interface TranscriptJobs {
	jobs: JobView[];
	latest: Map<string, string>;
	byToolCall: Map<string, string>;
}

function blankJob(key: string, id: string, type: string): JobView {
	return {
		key,
		id,
		type,
		status: "running",
		label: id,
		events: [],
		cancellable: false,
		cancellationPending: false,
		depth: 0,
		inSnapshot: false,
	};
}

/** 记一个终态：已有终态时不重复记（Core 会重投交付），只补上缺的正文。 */
function settle(
	job: JobView,
	status: JobRunState,
	at: number | undefined,
	durationMs: number | null,
	text: string | null,
) {
	if (status === "running") return;
	const done = job.events.find(
		(ev) => ev.kind !== "started" && ev.kind !== "notify",
	);
	if (done) {
		if (!done.text && text) done.text = text;
		return;
	}
	job.status = status;
	if (durationMs !== null) job.durationMs = durationMs;
	if (at !== undefined) job.endedAt = at;
	job.events.push({
		kind: status,
		at,
		durationMs: durationMs ?? undefined,
		text: text ?? undefined,
	});
	if (text) {
		job.output = text;
		job.outputSource = "result";
		if (status === "failed") job.error = text;
	}
}

const transcriptCache = new WeakMap<readonly Entry[], TranscriptJobs>();

/** 从转录条目（活动分支、按顺序）推出任务。结果按条目数组缓存。 */
export function transcriptJobs(entries: readonly Entry[]): TranscriptJobs {
	const hit = transcriptCache.get(entries);
	if (hit) return hit;
	const jobs: JobView[] = [];
	const byKey = new Map<string, JobView>();
	const latest = new Map<string, string>();
	const byToolCall = new Map<string, string>();
	const calls = new Map<string, ToolCallInfo>();
	const ensure = (id: string, type: string): JobView => {
		const key = latest.get(id);
		const known = key ? byKey.get(key) : undefined;
		if (known) return known;
		const job = blankJob(id, id, type);
		jobs.push(job);
		byKey.set(id, job);
		latest.set(id, id);
		return job;
	};
	for (const e of entries) {
		// 子 agent 的转录在子目录里：它们的任务经快照树进来
		if (!isMainFile(e.file)) continue;
		const m = messageOfEntry(e);
		if (!m) continue;
		const at = e.ts ?? num(m.timestamp) ?? undefined;
		if (m.role === "assistant" && Array.isArray(m.content)) {
			for (const b of m.content) {
				if (!isRecord(b) || b.type !== "toolCall") continue;
				const id = str(b.id);
				if (!id) continue;
				const args = isRecord(b.arguments) ? b.arguments : {};
				calls.set(id, {
					name: str(b.name) ?? "tool",
					args,
					intent: str(b.intent) ?? str(args.i) ?? undefined,
				});
			}
			continue;
		}
		if (m.role === "toolResult") {
			const callId = str(m.toolCallId);
			const toolName =
				str(m.toolName) ??
				(callId ? calls.get(callId)?.name : undefined) ??
				"tool";
			const frame = asyncFrameOf(m.details);
			if (frame && callId) {
				const seen = jobs.filter((j) => j.id === frame.jobId).length;
				const key = seen === 0 ? frame.jobId : `${frame.jobId}#${seen + 1}`;
				const call = calls.get(callId);
				const args = call?.args ?? {};
				const command = str(args.command) ?? str(args.code) ?? undefined;
				const details = isRecord(m.details) ? m.details : {};
				const job = blankJob(key, frame.jobId, frame.type ?? toolName);
				job.toolCallId = callId;
				job.toolName = toolName;
				job.command = command;
				job.label = command ? firstLine(command) : toolName;
				job.intent = call?.intent;
				job.cwd = str(args.cwd) ?? undefined;
				job.timeoutSec =
					num(args.timeout) ?? num(details.timeoutSeconds) ?? undefined;
				job.notifyPattern = str(args.notify) ?? undefined;
				job.startedAt = at;
				job.events.push({ kind: "started", at });
				jobs.push(job);
				byKey.set(key, job);
				latest.set(frame.jobId, key);
				byToolCall.set(callId, key);
				// 调用返回前任务已经结束：启动结果本身就是终态
				settle(job, frame.state, at, null, null);
			}
			for (const p of polledJobsOf(toolName, m.details)) {
				if (p.status === "running") continue;
				const job = ensure(p.id, p.type ?? "bash");
				if (job.label === job.id && p.label) job.label = p.label;
				settle(job, p.status, at, p.durationMs, p.errorText || p.resultText);
			}
			continue;
		}
		if (m.role !== "custom") continue;
		if (m.customType === ASYNC_RESULT_TYPE) {
			for (const d of parseAsyncResult(m)) {
				const job = ensure(d.jobId, d.type ?? "bash");
				if (d.type && job.type !== d.type && !job.toolCallId) job.type = d.type;
				if (job.label === job.id && d.label) job.label = d.label;
				if (d.agentId) job.agentId = d.agentId;
				settle(job, d.status, at, d.durationMs, d.result || null);
			}
		} else if (m.customType === ASYNC_NOTIFY_TYPE) {
			for (const r of parseAsyncNotify(m)) {
				if (r.kind !== "bash") continue;
				ensure(r.id, "bash").events.push({
					kind: "notify",
					at,
					pattern: r.pattern,
					lines: r.lines,
					omitted: r.omitted,
				});
			}
		}
	}
	const out = { jobs, latest, byToolCall };
	transcriptCache.set(entries, out);
	return out;
}

function liveText(raw: unknown): string | undefined {
	if (!isRecord(raw) || !Array.isArray(raw.content)) return undefined;
	const parts: string[] = [];
	for (const b of raw.content)
		if (isRecord(b) && b.type === "text" && typeof b.text === "string")
			parts.push(b.text);
	return parts.length > 0 ? parts.join("\n") : undefined;
}

function applySnapshot(job: JobView, snap: SnapJob, row: RpcJobTreeRow) {
	job.inSnapshot = true;
	// 快照可能比转录 / 实时帧旧：已知终态不被快照里的 running 盖回去
	const status = jobRunState(snap.status);
	if (status && (status !== "running" || !isTerminal(job.status)))
		job.status = status;
	if (snap.label && (job.label === job.id || !job.label))
		job.label = snap.label;
	if (snap.intent) job.intent = snap.intent;
	if (snap.command) job.command = snap.command;
	if (typeof snap.startTime === "number") job.startedAt = snap.startTime;
	const end = snap.endTime ?? snap.endedAt;
	if (typeof end === "number") job.endedAt = end;
	if (
		typeof end === "number" &&
		typeof snap.startTime === "number" &&
		job.durationMs === undefined
	)
		job.durationMs = end - snap.startTime;
	job.cancellable = row.cancellable === true && job.status === "running";
	job.cancellationPending = snap.cancellationPending === true;
	if (snap.ownerId) job.ownerId = snap.ownerId;
	if (snap.agentId) job.agentId = snap.agentId;
	job.depth = row.depth;
	if (snap.caption) job.caption = snap.caption;
	if (snap.widget) job.widget = snap.widget;
	if (snap.errorPreview && !job.error) job.error = snap.errorPreview;
	const preview = snap.errorPreview ?? snap.resultPreview;
	if (preview && job.outputSource !== "live" && job.outputSource !== "result") {
		job.output = preview;
		job.outputSource = "preview";
	}
}

/**
 * 合成线程的任务列表。
 * `snapshotCurrent`：快照是在转录里最后一个任务出现之后取的——只有这时，快照里没有的运行中任务才能判为「已结束」。
 */
export function mergeJobs(
	entries: readonly Entry[],
	liveTools: Readonly<Record<string, LiveTool>>,
	snapshot: JobsSnapshot | undefined,
	snapshotCurrent: boolean,
): ThreadJobs {
	const base = transcriptJobs(entries);
	const jobs = base.jobs.map((j) => ({ ...j, events: [...j.events] }));
	const byKey = new Map(jobs.map((j) => [j.key, j]));
	const byToolCall = new Map(base.byToolCall);
	const latest = new Map(base.latest);
	// 实时帧：后台调用结束后继续推的进度（输出尾巴 + 状态）
	for (const tool of Object.values(liveTools)) {
		const frame =
			asyncFrameOf(
				isRecord(tool.partialResult) ? tool.partialResult.details : undefined,
			) ??
			asyncFrameOf(isRecord(tool.result) ? tool.result.details : undefined);
		if (!frame) continue;
		let key = byToolCall.get(tool.toolCallId);
		if (!key) {
			// 工具结果条目还没落盘：先用实时帧建一条
			key = frame.jobId;
			if (byKey.has(key)) continue;
			const args = isRecord(tool.args) ? tool.args : {};
			const command = str(args.command) ?? str(args.code) ?? undefined;
			const job = blankJob(key, frame.jobId, frame.type ?? tool.toolName);
			job.toolCallId = tool.toolCallId;
			job.toolName = tool.toolName;
			job.command = command;
			job.label = command ? firstLine(command) : tool.toolName;
			job.intent = tool.intent;
			job.cwd = str(args.cwd) ?? undefined;
			job.startedAt = tool.startedAt;
			job.events.push({ kind: "started", at: tool.startedAt });
			jobs.push(job);
			byKey.set(key, job);
			byToolCall.set(tool.toolCallId, key);
			latest.set(frame.jobId, key);
		}
		const job = byKey.get(key);
		if (!job) continue;
		const text = liveText(tool.partialResult);
		// 交付 / 轮询到的最终结果比实时尾巴完整，不被后者盖掉
		if (text !== undefined && job.outputSource !== "result") {
			job.output = text;
			job.outputSource = "live";
		}
		if (isTerminal(frame.state) && job.status === "running")
			settle(job, frame.state, undefined, null, null);
	}
	if (snapshot) {
		// 线路上的普通 JSON：旧 Core 或出错的应答可能缺字段
		const rows: RpcJobTreeRow[] = snapshot.tree?.length
			? snapshot.tree
			: (Array.isArray(snapshot.jobs) ? snapshot.jobs : []).map((job) => ({
					depth: 0,
					job,
				}));
		for (const row of rows) {
			const snap = row.job;
			if (!snap) continue;
			const key = latest.get(snap.id);
			let job = key ? byKey.get(key) : undefined;
			if (!job) {
				job = blankJob(snap.id, snap.id, snap.type);
				jobs.push(job);
				byKey.set(job.key, job);
				latest.set(snap.id, job.key);
			}
			applySnapshot(job, snap, row);
		}
		if (snapshotCurrent)
			for (const job of jobs)
				if (job.status === "running" && !job.inSnapshot) job.status = "ended";
	}
	return { jobs, byToolCall, byKey };
}

/** 转录里任务集合的指纹：有新任务启动或结束时变化（据此重取快照）。 */
export function jobsSignature(entries: readonly Entry[]): string {
	const { jobs } = transcriptJobs(entries);
	let running = 0;
	for (const j of jobs) if (j.status === "running") running++;
	return `${jobs.length}:${running}`;
}
