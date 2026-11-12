/**
 * 线程视图 reducer（纯函数）：已复制条目 + 实时叠加层 + agent 侧状态（计划 C4「实时缓冲」与「客户端渲染规则」）。
 *
 * - 条目（entries）是持久真相，按 `seq` upsert。
 * - 叠加层只保存尚未被条目覆盖的在途消息与运行中工具：
 *   消息按 `(role, timestamp)` 匹配条目后清除；工具按 `toolCallId` 匹配 `toolResult` 条目后清除。
 * - `live.reset`：丢弃叠加层，直到下一个 `message_start` 之前只按条目渲染。
 * - 覆盖 omp 全部 33 种会话事件与非会话帧；不认识的帧进调试面板，不丢弃。
 */
import type {
	AgentStatus,
	Entry,
	Orb,
	Thread,
	WsEntries,
	WsThreadSnapshot,
} from "../api/client";
import type {
	ExtensionUiRequest,
	RpcAvailableSlashCommand,
	RpcBtwHistoryRecord,
	RpcGetQueueResult,
	RpcLoopModeState,
	RpcPlanProposalFrame,
	RpcPromptResultFrame,
	SessionState,
} from "../omp/types";
import { isRecord } from "../tool-render/util";
import {
	applyMessageUpdate,
	endLiveMessage,
	type JsonRecord,
	type LiveMessage,
	startLiveMessage,
} from "./message-builder";

/** omp `RpcAgentEvent` 联合里的全部 33 种会话事件。 */
export const SESSION_EVENT_TYPES = [
	"agent_start",
	"agent_end",
	"turn_start",
	"turn_end",
	"message_start",
	"message_update",
	"message_end",
	"tool_execution_start",
	"tool_execution_update",
	"tool_stream_update",
	"tool_execution_end",
	"auto_compaction_start",
	"auto_compaction_end",
	"manual_compaction_start",
	"manual_compaction_end",
	"auto_retry_start",
	"auto_retry_end",
	"cache_warming_start",
	"cache_warming_end",
	"retry_fallback_applied",
	"retry_fallback_succeeded",
	"model_changed",
	"config_warnings_changed",
	"advisor_cost_changed",
	"advisor_yielded",
	"ttsr_triggered",
	"todo_reminder",
	"todo_auto_clear",
	"irc_message",
	"notice",
	"thinking_level_changed",
	"goal_updated",
	"queue_update",
] as const;

/** 已知但界面不消费的非会话帧（语音、听写、宿主工具等）；记为已识别，不进调试面板。终端帧由 socket 直接交给 `terminalHub`，不经 reducer。 */
const IGNORED_FRAME_TYPES: Record<string, true> = {
	ready: true,
	response: true,
	rpc_chunk: true,
	live_phase: true,
	live_levels: true,
	live_transcript: true,
	live_end: true,
	dictation_state: true,
	dictation_transcript: true,
	dictation_end: true,
	host_dictation_state: true,
	host_dictation_text: true,
	host_dictation_notice: true,
	host_dictation_download: true,
	host_tool_call: true,
	host_tool_cancel: true,
	host_uri_request: true,
	host_uri_cancel: true,
	model_catalog_update: true,
	subagent_event: true,
};

export type OmpFrame = { type: string } & JsonRecord;

export interface LiveTool {
	toolCallId: string;
	toolName: string;
	args?: unknown;
	intent?: string;
	partialResult?: unknown;
	streamUpdate?: unknown;
	result?: unknown;
	isError?: boolean;
	ended: boolean;
	startedAt: number;
}

export interface SubagentLive {
	key: string;
	index: number;
	id?: string;
	agent?: string;
	description?: string;
	task?: string;
	status?: string;
	parentToolCallId?: string;
	sessionFile?: string;
	progress?: JsonRecord;
	updatedAt: number;
}

export type NoticeLevel = "info" | "warning" | "error";

/** 内联提示的种类（决定显示的标签文案）。 */
export type NoticeKind =
	| "notify"
	| "open_url"
	| "compaction"
	| "compaction_done"
	| "compaction_skipped"
	| "retry_failed"
	| "cache_warming_start"
	| "cache_warming_end"
	| "retry_fallback_applied"
	| "retry_fallback_succeeded"
	| "config_warnings_changed"
	| "ttsr_triggered"
	| "todo_reminder"
	| "irc_message"
	| "notice"
	| "prompt_error"
	| "extension_error"
	| "command_output"
	| "rpc_frame_error";

/** 转录内联提示（瞬态，不落库）。 */
export interface Notice {
	key: string;
	/** 到达时线程的 lastSeq：渲染时插在该序号之后。 */
	afterSeq: number;
	kind: NoticeKind;
	level: NoticeLevel;
	text: string;
	detail?: unknown;
	ts: number;
}

export interface QueueSnapshot {
	steering: string[];
	followUp: string[];
	liveSteered: number;
}

export interface CompactionStatus {
	kind: "auto" | "manual";
	reason?: string;
	action?: string;
	mode?: string;
}

export interface RetryStatus {
	attempt: number;
	maxAttempts: number;
	delayMs: number;
	errorMessage: string;
	startedAt: number;
}

export interface DebugFrame {
	key: string;
	ts: number;
	source: string;
	frame: unknown;
}

export interface ThreadViewState {
	threadId: string;
	thread?: Thread;
	orb?: Orb;
	agentStatus: AgentStatus;
	entries: Entry[];
	/** 可续传游标：已并入本地的最大条目 seq（不取服务端补发目标）。 */
	lastSeq: number;
	/** 服务端转录版本（`thread.snapshot.transcriptRev`）；未知为 null。重订阅时带上，不一致则服务端全量重放。 */
	transcriptRev: number | null;
	leafEntryId?: string;
	liveMessage: LiveMessage | null;
	liveTools: Record<string, LiveTool>;
	liveToolOrder: string[];
	/** `live.reset` 之后、下一个 `message_start` 之前为真。 */
	liveSuppressed: boolean;
	/** 已见 `agent_start` 且尚无终结的 `agent_end`。 */
	running: boolean;
	state: SessionState | null;
	queue: QueueSnapshot | null;
	/** 最近一次 `get_queue` 结果（带 id，可编辑 / 移动 / 删除）。 */
	queueItems: RpcGetQueueResult | null;
	pendingUi: ExtensionUiRequest[];
	statuses: Record<string, string>;
	widgets: Record<
		string,
		{ lines: string[]; placement: "aboveEditor" | "belowEditor" }
	>;
	planProposal: RpcPlanProposalFrame | null;
	subagents: Record<string, SubagentLive>;
	notices: Notice[];
	compaction: CompactionStatus | null;
	retry: RetryStatus | null;
	commands: RpcAvailableSlashCommand[] | null;
	btw: Record<string, RpcBtwHistoryRecord>;
	btwOrder: string[];
	loopMode: RpcLoopModeState | null;
	/** 最近一次 get/set_vibe_mode 的结果；未知为 null。 */
	vibeEnabled: boolean | null;
	lastPromptResult: RpcPromptResultFrame | null;
	/** omp 侧 `set_editor_text` 请求；nonce 变化时输入区应用之。 */
	editorText: { text: string; prepend: boolean; nonce: number } | null;
	snapshotReceived: boolean;
	debug: DebugFrame[];
	noticeSeq: number;
}

const MAX_NOTICES = 200;
const MAX_DEBUG = 200;

/** 需要弹出对话框并等待应答的 UI 请求方法。 */
export const DIALOG_UI_METHODS: Record<string, true> = {
	select: true,
	confirm: true,
	input: true,
	editor: true,
	ask: true,
	askDialog: true,
	form: true,
};

export function initialThreadState(threadId: string): ThreadViewState {
	return {
		threadId,
		agentStatus: "none",
		entries: [],
		lastSeq: 0,
		transcriptRev: null,
		liveMessage: null,
		liveTools: {},
		liveToolOrder: [],
		liveSuppressed: false,
		running: false,
		state: null,
		queue: null,
		queueItems: null,
		pendingUi: [],
		statuses: {},
		widgets: {},
		planProposal: null,
		subagents: {},
		notices: [],
		compaction: null,
		retry: null,
		commands: null,
		btw: {},
		btwOrder: [],
		loopMode: null,
		vibeEnabled: null,
		lastPromptResult: null,
		editorText: null,
		snapshotReceived: false,
		debug: [],
		noticeSeq: 0,
	};
}

function str(v: unknown): string | undefined {
	return typeof v === "string" ? v : undefined;
}

function num(v: unknown): number | undefined {
	return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}

function pushNotice(
	s: ThreadViewState,
	kind: NoticeKind,
	level: NoticeLevel,
	text: string,
	detail?: unknown,
	now = Date.now(),
): ThreadViewState {
	const n = s.noticeSeq + 1;
	const notice: Notice = {
		key: `n${n}`,
		afterSeq: s.lastSeq,
		kind,
		level,
		text,
		detail,
		ts: now,
	};
	const notices = [...s.notices, notice];
	if (notices.length > MAX_NOTICES)
		notices.splice(0, notices.length - MAX_NOTICES);
	return { ...s, notices, noticeSeq: n };
}

function pushDebug(
	s: ThreadViewState,
	source: string,
	frame: unknown,
): ThreadViewState {
	const n = s.noticeSeq + 1;
	const debug = [...s.debug, { key: `d${n}`, ts: Date.now(), source, frame }];
	if (debug.length > MAX_DEBUG) debug.splice(0, debug.length - MAX_DEBUG);
	return { ...s, debug, noticeSeq: n };
}

function setTool(
	s: ThreadViewState,
	id: string,
	patch: Partial<LiveTool> & { toolName?: string },
): ThreadViewState {
	if (
		s.liveSuppressed &&
		!s.liveTools[id] &&
		patch.ended === undefined &&
		patch.startedAt === undefined
	)
		return s;
	const prev = s.liveTools[id];
	const tool: LiveTool = {
		toolCallId: id,
		toolName: patch.toolName ?? prev?.toolName ?? "tool",
		startedAt: prev?.startedAt ?? patch.startedAt ?? Date.now(),
		ended: false,
		...prev,
		...patch,
	};
	const liveToolOrder = prev ? s.liveToolOrder : [...s.liveToolOrder, id];
	return { ...s, liveTools: { ...s.liveTools, [id]: tool }, liveToolOrder };
}

function removeTools(s: ThreadViewState, ids: Set<string>): ThreadViewState {
	if (ids.size === 0) return s;
	let changed = false;
	const liveTools = { ...s.liveTools };
	for (const id of ids) {
		if (liveTools[id]) {
			delete liveTools[id];
			changed = true;
		}
	}
	// 父工具清除时一并清掉其子 agent
	let subagents = s.subagents;
	for (const [k, sub] of Object.entries(s.subagents)) {
		if (sub.parentToolCallId && ids.has(sub.parentToolCallId)) {
			if (subagents === s.subagents) subagents = { ...s.subagents };
			delete subagents[k];
			changed = true;
		}
	}
	if (!changed) return s;
	return {
		...s,
		liveTools,
		liveToolOrder: s.liveToolOrder.filter((id) => liveTools[id]),
		subagents,
	};
}

function messageOf(entry: Entry): JsonRecord | null {
	const p = entry.payload;
	if (entry.type !== "message" || !isRecord(p) || !isRecord(p.message))
		return null;
	return p.message;
}

/** 该条目是否覆盖了叠加层里的在途消息（`(role, timestamp)` 相同）。 */
export function entryCoversMessage(
	entry: Entry,
	live: Pick<LiveMessage, "role" | "timestamp">,
): boolean {
	const m = messageOf(entry);
	if (!m || live.timestamp === undefined) return false;
	return m.role === live.role && m.timestamp === live.timestamp;
}

/** 该条目若是 toolResult，返回其 toolCallId。 */
export function toolResultIdOf(entry: Entry): string | undefined {
	const m = messageOf(entry);
	if (m?.role !== "toolResult") return undefined;
	return str(m.toolCallId);
}

/** 合并一批条目（按 seq upsert，保持有序），并清除被覆盖的叠加层。 */
export function mergeEntries(
	s: ThreadViewState,
	incoming: Entry[],
): ThreadViewState {
	if (incoming.length === 0) return s;
	let entries = s.entries;
	const lastExisting =
		entries.length > 0 ? (entries[entries.length - 1]?.seq ?? 0) : 0;
	const sorted = [...incoming].sort((a, b) => a.seq - b.seq);
	if ((sorted[0]?.seq ?? 0) > lastExisting) {
		entries = [...entries, ...sorted];
	} else {
		const bySeq = new Map(entries.map((e) => [e.seq, e]));
		for (const e of sorted) bySeq.set(e.seq, e);
		entries = [...bySeq.values()].sort((a, b) => a.seq - b.seq);
	}
	let next: ThreadViewState = { ...s, entries };
	const toolIds = new Set<string>();
	for (const e of incoming) {
		if (next.liveMessage && entryCoversMessage(e, next.liveMessage))
			next = { ...next, liveMessage: null };
		const tid = toolResultIdOf(e);
		if (tid) toolIds.add(tid);
	}
	return removeTools(next, toolIds);
}

/**
 * 应用一批条目。游标只推进到实际并入的最大 seq：补发分批时若在中途断线，
 * 重连后的 `afterSeq` 不会越过尚未收到的批次。
 */
export function applyEntries(
	s: ThreadViewState,
	msg: Pick<WsEntries, "entries" | "leafEntryId">,
): ThreadViewState {
	const next = mergeEntries(s, msg.entries);
	let lastSeq = next.lastSeq;
	for (const e of msg.entries) if (e.seq > lastSeq) lastSeq = e.seq;
	return {
		...next,
		lastSeq,
		leafEntryId: msg.leafEntryId ?? next.leafEntryId,
	};
}

/** `transcript.reset`：服务端重写了转录，丢弃本地条目，随后以 `afterSeq=0` 重新订阅。 */
export function applyTranscriptReset(
	s: ThreadViewState,
	transcriptRev: number,
): ThreadViewState {
	return { ...s, entries: [], lastSeq: 0, transcriptRev };
}

/** `plan.resolved`：计划提案已被任一客户端处理。 */
export function applyPlanResolved(s: ThreadViewState): ThreadViewState {
	return s.planProposal ? { ...s, planProposal: null } : s;
}

function queueFrom(frame: JsonRecord): QueueSnapshot {
	return {
		steering: Array.isArray(frame.steering)
			? frame.steering.filter((x): x is string => typeof x === "string")
			: [],
		followUp: Array.isArray(frame.followUp)
			? frame.followUp.filter((x): x is string => typeof x === "string")
			: [],
		liveSteered: num(frame.liveSteered) ?? 0,
	};
}

function upsertSubagent(
	s: ThreadViewState,
	payload: unknown,
	kind: "lifecycle" | "progress",
): ThreadViewState {
	if (!isRecord(payload)) return s;
	const index = num(payload.index) ?? 0;
	const key = String(index);
	const prev = s.subagents[key];
	const sub: SubagentLive = {
		key,
		index,
		updatedAt: Date.now(),
		...prev,
		id: str(payload.id) ?? prev?.id,
		agent: str(payload.agent) ?? prev?.agent,
		description: str(payload.description) ?? prev?.description,
		task: str(payload.task) ?? prev?.task,
		parentToolCallId: str(payload.parentToolCallId) ?? prev?.parentToolCallId,
		sessionFile: str(payload.sessionFile) ?? prev?.sessionFile,
		status:
			kind === "lifecycle"
				? (str(payload.status) ?? prev?.status)
				: (prev?.status ?? "started"),
		progress:
			kind === "progress" && isRecord(payload.progress)
				? payload.progress
				: prev?.progress,
	};
	return { ...s, subagents: { ...s.subagents, [key]: sub } };
}

function handleUiRequest(
	s: ThreadViewState,
	frame: JsonRecord,
): ThreadViewState {
	const req = frame as unknown as ExtensionUiRequest;
	switch (req.method) {
		case "cancel":
			return {
				...s,
				pendingUi: s.pendingUi.filter((r) => r.id !== req.targetId),
			};
		case "notify":
			return pushNotice(s, "notify", req.notifyType ?? "info", req.message);
		case "setStatus": {
			const statuses = { ...s.statuses };
			if (req.statusText) statuses[req.statusKey] = req.statusText;
			else delete statuses[req.statusKey];
			return { ...s, statuses };
		}
		case "setWidget": {
			const widgets = { ...s.widgets };
			if (req.widgetLines && req.widgetLines.length > 0) {
				widgets[req.widgetKey] = {
					lines: req.widgetLines,
					placement: req.widgetPlacement ?? "aboveEditor",
				};
			} else delete widgets[req.widgetKey];
			return { ...s, widgets };
		}
		case "setTitle":
			// 标题由控制面随 session_info_update 同步到线程，这里不处理
			return s;
		case "set_editor_text":
			return {
				...s,
				editorText: {
					text: req.text,
					prepend: req.prepend === true,
					nonce: (s.editorText?.nonce ?? 0) + 1,
				},
			};
		case "open_url":
			return pushNotice(s, "open_url", "info", req.instructions ?? req.url, {
				url: req.launchUrl ?? req.url,
			});
		default:
			if (DIALOG_UI_METHODS[req.method]) {
				if (s.pendingUi.some((r) => r.id === req.id)) return s;
				return { ...s, pendingUi: [...s.pendingUi, req] };
			}
			return pushDebug(s, "frame", frame);
	}
}

/** 应用一条实时 omp 帧。 */
export function applyFrame(
	s: ThreadViewState,
	frame: OmpFrame,
): ThreadViewState {
	const f = frame as JsonRecord;
	switch (frame.type) {
		// ---- 会话事件（33 种） ----
		case "agent_start":
			return { ...s, running: true, lastPromptResult: null };
		case "agent_end":
			// isTerminal === false：仍有续跑或异步投递
			return f.isTerminal !== false ? { ...s, running: false, retry: null } : s;
		case "turn_start":
		case "turn_end":
			return s;
		case "message_start":
			return { ...s, liveMessage: startLiveMessage(f), liveSuppressed: false };
		case "message_update":
			if (s.liveSuppressed) return s;
			return {
				...s,
				liveMessage: applyMessageUpdate(
					s.liveMessage ?? startLiveMessage(f),
					f,
				),
			};
		case "message_end":
			if (s.liveSuppressed) return s;
			return { ...s, liveMessage: endLiveMessage(s.liveMessage, f) };
		case "tool_execution_start": {
			const id = str(f.toolCallId);
			if (!id) return s;
			return setTool(s, id, {
				toolName: str(f.toolName),
				args: f.args,
				intent: str(f.intent),
				startedAt: Date.now(),
				ended: false,
			});
		}
		case "tool_execution_update": {
			const id = str(f.toolCallId);
			if (!id) return s;
			const patch: Partial<LiveTool> = {
				toolName: str(f.toolName),
				partialResult: f.partialResult,
			};
			if (f.args !== undefined) patch.args = f.args;
			return setTool(s, id, patch);
		}
		case "tool_stream_update": {
			const id = str(f.toolCallId);
			if (!id) return s;
			return setTool(s, id, {
				toolName: str(f.toolName),
				streamUpdate: f.update,
			});
		}
		case "tool_execution_end": {
			const id = str(f.toolCallId);
			if (!id) return s;
			return setTool(s, id, {
				toolName: str(f.toolName),
				result: f.result,
				isError: f.isError === true,
				ended: true,
			});
		}
		case "auto_compaction_start":
			return {
				...s,
				compaction: {
					kind: "auto",
					reason: str(f.reason),
					action: str(f.action),
				},
			};
		case "auto_compaction_end": {
			const next = { ...s, compaction: null };
			if (f.aborted === true)
				return pushNotice(
					next,
					"compaction",
					"warning",
					str(f.errorMessage) ?? "",
					f,
				);
			if (str(f.errorMessage))
				return pushNotice(
					next,
					"compaction",
					"error",
					str(f.errorMessage) ?? "",
					f,
				);
			return pushNotice(next, "compaction_done", "info", "", f);
		}
		case "manual_compaction_start":
			return { ...s, compaction: { kind: "manual", mode: str(f.mode) } };
		case "manual_compaction_end": {
			const next = { ...s, compaction: null };
			if (f.skipped === true)
				return pushNotice(
					next,
					"compaction_skipped",
					"info",
					str(f.errorMessage) ?? "",
					f,
				);
			if (f.aborted === true)
				return pushNotice(
					next,
					"compaction",
					"warning",
					str(f.errorMessage) ?? "",
					f,
				);
			if (str(f.errorMessage))
				return pushNotice(
					next,
					"compaction",
					"error",
					str(f.errorMessage) ?? "",
					f,
				);
			return pushNotice(next, "compaction_done", "info", "", f);
		}
		case "auto_retry_start":
			return {
				...s,
				retry: {
					attempt: num(f.attempt) ?? 1,
					maxAttempts: num(f.maxAttempts) ?? 1,
					delayMs: num(f.delayMs) ?? 0,
					errorMessage: str(f.errorMessage) ?? "",
					startedAt: Date.now(),
				},
			};
		case "auto_retry_end": {
			const next = { ...s, retry: null };
			return f.success === true
				? next
				: pushNotice(next, "retry_failed", "error", str(f.finalError) ?? "", f);
		}
		case "cache_warming_start":
			return pushNotice(
				s,
				"cache_warming_start",
				"info",
				`${str(f.provider) ?? ""}/${str(f.model) ?? ""}`,
				f,
			);
		case "cache_warming_end":
			return pushNotice(
				s,
				"cache_warming_end",
				f.outcome === "error" ? "warning" : "info",
				str(f.outcome) ?? "",
				f,
			);
		case "retry_fallback_applied":
			return pushNotice(
				s,
				"retry_fallback_applied",
				"warning",
				`${str(f.from) ?? ""} → ${str(f.to) ?? ""}`,
				f,
			);
		case "retry_fallback_succeeded":
			return pushNotice(
				s,
				"retry_fallback_succeeded",
				"info",
				str(f.model) ?? "",
				f,
			);
		case "model_changed":
		case "advisor_cost_changed":
		case "advisor_yielded":
			return s;
		case "config_warnings_changed":
			return pushNotice(s, "config_warnings_changed", "warning", "", f);
		case "ttsr_triggered": {
			const rules = Array.isArray(f.rules) ? f.rules : [];
			const names = rules
				.map((r) => (isRecord(r) ? (str(r.name) ?? "") : ""))
				.filter(Boolean);
			return pushNotice(s, "ttsr_triggered", "info", names.join(", "), f);
		}
		case "todo_reminder":
			return pushNotice(
				s,
				"todo_reminder",
				"info",
				`${num(f.attempt) ?? 0}/${num(f.maxAttempts) ?? 0}`,
				f,
			);
		case "todo_auto_clear":
			return s.state ? { ...s, state: { ...s.state, todoPhases: [] } } : s;
		case "irc_message": {
			const m = isRecord(f.message) ? f.message : {};
			const content = m.content;
			const text =
				typeof content === "string"
					? content
					: Array.isArray(content)
						? content
								.map((b) =>
									isRecord(b) && typeof b.text === "string" ? b.text : "",
								)
								.filter(Boolean)
								.join("\n")
						: "";
			return pushNotice(s, "irc_message", "info", text, f);
		}
		case "notice": {
			const level =
				f.level === "warning" || f.level === "error" ? f.level : "info";
			return pushNotice(s, "notice", level, str(f.message) ?? "", f);
		}
		case "thinking_level_changed":
			return s.state
				? {
						...s,
						state: {
							...s.state,
							thinkingLevel:
								(f.thinkingLevel as SessionState["thinkingLevel"]) ??
								s.state.thinkingLevel,
							thinkingConfigured:
								(f.configured as SessionState["thinkingConfigured"]) ??
								s.state.thinkingConfigured,
						},
					}
				: s;
		case "goal_updated":
			return s.state
				? {
						...s,
						state: {
							...s.state,
							goal: (f.state as SessionState["goal"]) ?? null,
						},
					}
				: s;
		case "queue_update":
			return { ...s, queue: queueFrom(f) };

		// ---- 非会话帧 ----
		case "prompt_result": {
			const r = f as unknown as RpcPromptResultFrame;
			const next = { ...s, lastPromptResult: r };
			if (r.status === "error")
				return pushNotice(
					next,
					"prompt_error",
					"error",
					r.error?.message ?? "",
					r.error,
				);
			return next;
		}
		case "session_settled":
			return {
				...s,
				running: false,
				state: s.state ? { ...s.state, isSettled: true } : s.state,
			};
		case "extension_error":
			return pushNotice(
				s,
				"extension_error",
				"error",
				`${str(f.extensionPath) ?? ""}: ${str(f.error) ?? ""}`,
				f,
			);
		case "available_commands_update":
			return {
				...s,
				commands: Array.isArray(f.commands)
					? (f.commands as RpcAvailableSlashCommand[])
					: [],
			};
		case "subagent_lifecycle":
			return upsertSubagent(s, f.payload, "lifecycle");
		case "subagent_progress":
			return upsertSubagent(s, f.payload, "progress");
		case "btw_delta": {
			const id = str(f.recordId);
			const prev = id ? s.btw[id] : undefined;
			if (!id || !prev) return s;
			return {
				...s,
				btw: {
					...s.btw,
					[id]: { ...prev, answer: `${prev.answer}${str(f.delta) ?? ""}` },
				},
			};
		}
		case "btw_record": {
			const rec = isRecord(f.record)
				? (f.record as unknown as RpcBtwHistoryRecord)
				: null;
			if (!rec?.id) return s;
			const btwOrder = s.btw[rec.id] ? s.btwOrder : [...s.btwOrder, rec.id];
			return { ...s, btw: { ...s.btw, [rec.id]: rec }, btwOrder };
		}
		case "command_output":
			return pushNotice(s, "command_output", "info", str(f.text) ?? "", f);
		case "session_info_update":
			return s.thread && str(f.title) && s.thread.titleSource !== "user"
				? {
						...s,
						thread: { ...s.thread, title: str(f.title) ?? s.thread.title },
					}
				: s;
		case "config_update":
			return s.state
				? {
						...s,
						state: {
							...s.state,
							model: (f.model as SessionState["model"]) ?? s.state.model,
							thinkingLevel:
								(f.thinkingLevel as SessionState["thinkingLevel"]) ??
								s.state.thinkingLevel,
						},
					}
				: s;
		case "rpc_frame_error":
			return pushNotice(
				s,
				"rpc_frame_error",
				"warning",
				`${str(f.originalType) ?? ""}: ${str(f.error) ?? ""}`,
				f,
			);
		case "extension_ui_request":
			return handleUiRequest(s, f);
		case "plan_proposal":
			return { ...s, planProposal: f as unknown as RpcPlanProposalFrame };
		case "loop_mode_update":
			return {
				...s,
				loopMode: isRecord(f.state)
					? (f.state as unknown as RpcLoopModeState)
					: null,
			};
		default:
			if (IGNORED_FRAME_TYPES[frame.type]) return s;
			return pushDebug(s, "frame", frame);
	}
}

/**
 * `live.reset`（agent 断档 / 重启）：服务端已清空在途帧、工作标志、待答 UI、计划提案与子 agent，
 * 客户端同步作废这些状态（随后由 `refresh_state` 拉回权威 state）；叠加层抑制到下一个 message_start。
 */
export function applyLiveReset(s: ThreadViewState): ThreadViewState {
	return {
		...s,
		liveMessage: null,
		liveTools: {},
		liveToolOrder: [],
		liveSuppressed: true,
		running: false,
		pendingUi: [],
		planProposal: null,
		subagents: {},
		compaction: null,
		retry: null,
	};
}

/** `ui.resolved`：某个 UI 请求已被任一客户端应答（先到先得），关闭本地对话框。 */
export function applyUiResolved(
	s: ThreadViewState,
	requestId: string,
): ThreadViewState {
	if (!s.pendingUi.some((r) => r.id === requestId)) return s;
	return { ...s, pendingUi: s.pendingUi.filter((r) => r.id !== requestId) };
}

/** `thread.snapshot`：以服务端缓冲为准重建叠加层与 agent 状态；已有条目保留（随后按 afterSeq 补发）。 */
export function applySnapshot(
	s: ThreadViewState,
	snap: WsThreadSnapshot,
): ThreadViewState {
	// fullReplay：服务端因 transcriptRev 不一致改为全量补发，本地条目作废
	const keepEntries = !snap.fullReplay && snap.lastSeq >= s.lastSeq;
	let next: ThreadViewState = {
		...initialThreadState(s.threadId),
		entries: keepEntries ? s.entries : [],
		lastSeq: keepEntries ? s.lastSeq : 0,
		transcriptRev: snap.transcriptRev,
		notices: s.notices,
		noticeSeq: s.noticeSeq,
		debug: s.debug,
		commands: s.commands,
		btw: s.btw,
		btwOrder: s.btwOrder,
		loopMode: s.loopMode,
		vibeEnabled: s.vibeEnabled,
		editorText: s.editorText,
		thread: snap.thread,
		orb: snap.orb,
		agentStatus: snap.agent.status,
		leafEntryId: snap.leafEntryId ?? s.leafEntryId,
		state: isRecord(snap.agent.state)
			? (snap.agent.state as unknown as SessionState)
			: s.state,
		snapshotReceived: true,
	};
	next = {
		...next,
		running: next.state
			? next.state.isStreaming || !next.state.isSettled
			: false,
	};
	if (isRecord(snap.agent.queue))
		next = { ...next, queue: queueFrom(snap.agent.queue) };
	for (const ui of snap.agent.pendingUi) {
		if (isRecord(ui) && typeof ui.type === "string")
			next = applyFrame(next, ui as OmpFrame);
	}
	if (isRecord(snap.agent.planProposal)) {
		next = {
			...next,
			planProposal: snap.agent.planProposal as unknown as RpcPlanProposalFrame,
		};
	}
	for (const sub of snap.agent.subagents) {
		if (isRecord(sub) && typeof sub.type === "string")
			next = applyFrame(next, sub as OmpFrame);
	}
	for (const fr of snap.agent.live) {
		if (isRecord(fr) && typeof fr.type === "string")
			next = applyFrame(next, fr as OmpFrame);
	}
	// 叠加层里可能有已被已有条目覆盖的部分
	const covered = new Set<string>();
	for (const e of next.entries) {
		const tid = toolResultIdOf(e);
		if (tid && next.liveTools[tid]) covered.add(tid);
		if (next.liveMessage && entryCoversMessage(e, next.liveMessage))
			next = { ...next, liveMessage: null };
	}
	return removeTools(next, covered);
}

/** 收到 `thread.snapshot` 后需要主动拉取的数据：agent 就绪时补 state、命令表，以及恢复的队列的可编辑条目。 */
export function snapshotEffects(snap: WsThreadSnapshot): FrameEffect[] {
	if (snap.agent.status !== "ready") return [];
	const effects: FrameEffect[] = ["refresh_commands"];
	if (!snap.agent.state) effects.push("refresh_state");
	if (isRecord(snap.agent.queue)) {
		const q = queueFrom(snap.agent.queue);
		if (q.steering.length > 0 || q.followUp.length > 0)
			effects.push("refresh_queue");
	}
	return effects;
}

/** 某帧之后需要主动向 agent 拉取的数据（副作用由 store 执行）。 */
export type FrameEffect =
	| "refresh_state"
	| "refresh_queue"
	| "refresh_commands";

export function effectsOf(frame: OmpFrame): FrameEffect[] {
	switch (frame.type) {
		case "agent_end":
		case "model_changed":
		case "thinking_level_changed":
		case "goal_updated":
		case "config_update":
		case "todo_auto_clear":
		case "session_settled":
			return ["refresh_state"];
		case "tool_execution_end":
			return frame.toolName === "todo" ? ["refresh_state"] : [];
		case "queue_update":
			return ["refresh_queue"];
		default:
			return [];
	}
}
