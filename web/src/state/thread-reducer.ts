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
import {
	filterUnmatchedOptimistic,
	type OptimisticAttachmentMeta,
} from "../lib/queue-message";
import { viewAwaitCallId } from "../lib/view/answer";
import type {
	ExtensionUiRequest,
	ImageContent,
	RpcAvailableSlashCommand,
	RpcBtwHistoryRecord,
	RpcGetQueueResult,
	RpcLoopModeState,
	RpcPlanProposalFrame,
	RpcPromptResultFrame,
	RpcQueuedMessage,
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
	/** 嵌套派发时派出它的子 agent 的 registry id；根上缺失。 */
	parentSubagentId?: string;
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

export type QueueLane = "steering" | "followUp";

/** 本地乐观排队项：用户在忙时发出 steer / follow_up 后立刻在输入坞上方展示，待权威 `queue_update` 接管。 */
export interface OptimisticQueuedMessage {
	id: string;
	lane: QueueLane;
	text: string;
	images?: ImageContent[];
	attachments?: OptimisticAttachmentMeta[];
	/** 提交时刻该 lane 已存在的权威条目 id，防止旧权威项吞掉同正文的新乐观项。 */
	baselineIds?: string[];
	/** 提交时刻该 lane 已存在的权威条目数（旧版纯字符串帧回退用）。 */
	baselineCount?: number;
	timestamp: number;
	/** 命令已应答但尚未被权威帧消解时为真；后续权威帧、用户消息落盘或回合结束会清理它。 */
	settled?: boolean;
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
	/**
	 * 带 `tail` 订阅的补发窗口起点（`thread.snapshot.windowStart`，M19）：seq ≤ windowStart 的条目尚未加载，
	 * 经 REST `before` 向前翻页；0 = 没有开窗或已加载到最早一条。
	 */
	windowStart: number;
	leafEntryId?: string;
	liveMessage: LiveMessage | null;
	/**
	 * 被下一条 `message_start` 顶替、落盘条目还没到的已结束消息（按到达顺序）。帧与条目经两条路送达
	 * （stdout 与会话文件 tail），助手刚说完就紧跟注入消息（顾问建议、迟到诊断等）时，条目往往晚于新消息的帧；
	 * 留在这里继续渲染，条目到了再接替，转录不会先塌掉一整条再长回来。
	 */
	landing: LiveMessage[];
	/**
	 * 条目 seq → 它接替的在途消息行键（`LiveMessage.rowKey`）。虚拟列表按行键缓存测得的高度：换成 `e<seq>` 会按估高重挂载，
	 * 读到上方时视口被测高补偿整段推走；沿用在途行键则原地交接。
	 */
	rowKeys: Record<number, string>;
	/** 下一条在途消息的行键序号（`live-<n>`）：只增不减，快照与转录重置也不回绕，保证与 `rowKeys` 里沿用的旧键不撞。 */
	liveSerial: number;
	liveTools: Record<string, LiveTool>;
	liveToolOrder: string[];
	/** `live.reset` 之后、下一个 `message_start` 之前为真。 */
	liveSuppressed: boolean;
	/** 已见 `agent_start` 且尚无终结的 `agent_end`。 */
	running: boolean;
	state: SessionState | null;
	queue: QueueSnapshot | null;
	/** 最近一次 `queue_update`（新版对象格式）或 `get_queue` 结果（带 id，可编辑 / 移动 / 删除）。 */
	queueItems: RpcGetQueueResult | null;
	/** 已发出、等待服务端 `queue_update` 接管的本地乐观排队项。 */
	optimisticQueue: OptimisticQueuedMessage[];
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
	/** btw topic 的展示顺序：与 Core `get_btw_history` 和 TUI 一致，按 createdAt 新的在前（同时刻按 id 升序）。 */
	btwOrder: string[];
	loopMode: RpcLoopModeState | null;
	/** 最近一次 get/set_vibe_mode 的结果；未知为 null。 */
	vibeEnabled: boolean | null;
	lastPromptResult: RpcPromptResultFrame | null;
	/** omp 侧 `set_editor_text` 请求；nonce 变化时输入区应用之。 */
	editorText: { text: string; prepend: boolean; nonce: number } | null;
	/** 下一条用户消息的预测（`oryn_composer_prediction`），按可能性从高到低；空数组表示无预测。是否显示由输入区按草稿决定。 */
	composerPrediction: string[];
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
		windowStart: 0,
		liveMessage: null,
		landing: [],
		rowKeys: {},
		liveSerial: 0,
		liveTools: {},
		liveToolOrder: [],
		liveSuppressed: false,
		running: false,
		state: null,
		queue: null,
		queueItems: null,
		optimisticQueue: [],
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
		composerPrediction: [],
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

/** btw topic 按 createdAt 新的在前排序（同时刻按 id 升序），与 Core 的历史顺序一致。 */
function btwOrderOf(btw: Record<string, RpcBtwHistoryRecord>): string[] {
	return Object.values(btw)
		.sort((a, b) => b.createdAt - a.createdAt || a.id.localeCompare(b.id))
		.map((r) => r.id);
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

/** 落盘成 `custom_message` 条目的在途消息角色（omp `#persistMessageEnd`）。 */
const CUSTOM_ROLES: Record<string, true> = { custom: true, hookMessage: true };

/**
 * 该条目是否覆盖了叠加层里的在途消息：`message` 条目按 `(role, timestamp)`；注入消息（custom / hookMessage）
 * 落盘为 `custom_message` 条目，没有 `message` 包裹，按 `(customType, 条目时间戳)` 认——Core 用消息自己的
 * 时间戳写条目，毫秒经 ISO 往返不变。
 */
export function entryCoversMessage(
	entry: Entry,
	live: Pick<LiveMessage, "role" | "timestamp" | "message">,
): boolean {
	if (live.timestamp === undefined) return false;
	if (entry.type === "custom_message") {
		const p = entry.payload;
		return (
			CUSTOM_ROLES[live.role] === true &&
			isRecord(p) &&
			p.customType === live.message.customType &&
			entry.ts === live.timestamp
		);
	}
	const m = messageOf(entry);
	if (!m) return false;
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
	// 条目按落盘顺序到达：盖住第 i 条在途消息时，它之前的已结束消息要么已被盖住，要么不会再落盘，一并清掉
	const inflight = s.liveMessage ? [...s.landing, s.liveMessage] : s.landing;
	let covered = -1;
	let rowKeys = s.rowKeys;
	for (const e of incoming) {
		for (let i = inflight.length - 1; i > covered; i--) {
			const m = inflight[i];
			if (m && entryCoversMessage(e, m)) {
				covered = i;
				rowKeys = { ...rowKeys, [e.seq]: m.rowKey };
				break;
			}
		}
		const tid = toolResultIdOf(e);
		if (tid) toolIds.add(tid);
	}
	if (covered >= 0)
		next = {
			...next,
			landing: s.landing.slice(covered + 1),
			liveMessage: covered >= s.landing.length ? null : s.liveMessage,
			rowKeys,
		};
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
	let hasUserEntry = false;
	for (const e of msg.entries) {
		if (e.seq > lastSeq) lastSeq = e.seq;
		if (
			e.type === "message" &&
			isRecord(e.payload) &&
			isRecord(e.payload.message) &&
			e.payload.message.role === "user"
		)
			hasUserEntry = true;
	}
	const optimisticQueue =
		hasUserEntry && next.optimisticQueue.some((o) => o.settled)
			? next.optimisticQueue.filter((o) => !o.settled)
			: next.optimisticQueue;
	return {
		...next,
		lastSeq,
		leafEntryId: msg.leafEntryId ?? next.leafEntryId,
		optimisticQueue,
	};
}

/**
 * 并入更早的一页条目（REST `entries?before=`，M19）。`lastSeq` 不变（续传游标只看补发进度）；
 * `windowStart` 更新为页内最小 seq 减一，翻到头（`hasMore` 为假）时置 0。
 */
export function prependEntries(
	s: ThreadViewState,
	page: { entries: Entry[]; hasMore: boolean },
): ThreadViewState {
	const next = mergeEntries(s, page.entries);
	let windowStart = 0;
	if (page.hasMore && page.entries.length > 0)
		windowStart = Math.min(...page.entries.map((e) => e.seq)) - 1;
	return { ...next, windowStart };
}

/** `transcript.reset`：服务端重写了转录，丢弃本地条目，随后以 `afterSeq=0` 重新订阅。 */
export function applyTranscriptReset(
	s: ThreadViewState,
	transcriptRev: number,
): ThreadViewState {
	return {
		...s,
		entries: [],
		lastSeq: 0,
		transcriptRev,
		windowStart: 0,
		rowKeys: {},
	};
}

/** `plan.resolved`：计划提案已被任一客户端处理。 */
export function applyPlanResolved(s: ThreadViewState): ThreadViewState {
	return s.planProposal ? { ...s, planProposal: null } : s;
}

interface ParsedQueueLane {
	texts: string[];
	/** 当且仅当该路所有元素均为带 id 的对象（或为空数组）时非空；旧版纯字符串数组时为 null。 */
	items: RpcQueuedMessage[] | null;
}

function parseQueueLane(raw: unknown): ParsedQueueLane {
	if (!Array.isArray(raw)) return { texts: [], items: null };
	if (raw.length === 0) return { texts: [], items: [] };
	const texts: string[] = [];
	const items: RpcQueuedMessage[] = [];
	let allObjects = true;
	for (const x of raw) {
		if (typeof x === "string") {
			texts.push(x);
			allObjects = false;
			continue;
		}
		if (isRecord(x) && typeof x.text === "string") {
			texts.push(x.text);
			if (typeof x.id === "string" && x.id) {
				const images = Array.isArray(x.images)
					? (x.images as ImageContent[])
					: undefined;
				items.push({
					id: x.id,
					text: x.text,
					...(images && images.length > 0 ? { images } : {}),
					editable: x.editable === true,
					timestamp: num(x.timestamp) ?? 0,
				});
			} else {
				allObjects = false;
			}
		}
	}
	return { texts, items: allObjects ? items : null };
}

function queueFrom(frame: JsonRecord): QueueSnapshot {
	const steering = parseQueueLane(frame.steering);
	const followUp = parseQueueLane(frame.followUp);
	return {
		steering: steering.texts,
		followUp: followUp.texts,
		liveSteered: num(frame.liveSteered) ?? 0,
	};
}

/** 新版 omp `queue_update` 直接携带 `SessionQueuedMessage[]`（带 id / editable），无需再等 `get_queue`。 */
function queueItemsFrom(frame: JsonRecord): RpcGetQueueResult | null {
	const steering = parseQueueLane(frame.steering);
	const followUp = parseQueueLane(frame.followUp);
	if (steering.items === null || followUp.items === null) return null;
	return { steering: steering.items, followUp: followUp.items };
}

/** 权威 `queue_update` 到达后，仅用新出现的权威项（按正文 + 附件身份）消解乐观项，避免旧项误删在途消息。 */
function reconcileOptimisticQueue(
	optimistic: readonly OptimisticQueuedMessage[],
	authoritative: QueueSnapshot,
	authoritativeItems: RpcGetQueueResult | null,
): OptimisticQueuedMessage[] {
	if (optimistic.length === 0) return [];
	const steering = filterUnmatchedOptimistic(
		optimistic.filter((o) => o.lane === "steering"),
		authoritativeItems?.steering ?? null,
		authoritative.steering,
		{ dropSettled: true },
	);
	const followUp = filterUnmatchedOptimistic(
		optimistic.filter((o) => o.lane === "followUp"),
		authoritativeItems?.followUp ?? null,
		authoritative.followUp,
		{ dropSettled: true },
	);
	if (steering.length === 0 && followUp.length === 0) return [];
	const keepIds = new Set([
		...steering.map((o) => o.id),
		...followUp.map((o) => o.id),
	]);
	return optimistic.filter((o) => keepIds.has(o.id));
}

function upsertSubagent(
	s: ThreadViewState,
	payload: unknown,
	kind: "lifecycle" | "progress",
): ThreadViewState {
	if (!isRecord(payload)) return s;
	const index = num(payload.index) ?? 0;
	// 按 id 区分：`index` 只是在所属 task 调用里的序号，嵌套派发的子 agent 会与根上的同号（progress 帧的 id 在 `progress.id`）
	const id =
		str(payload.id) ??
		(isRecord(payload.progress) ? str(payload.progress.id) : undefined);
	const key = id ?? String(index);
	const prev = s.subagents[key];
	const sub: SubagentLive = {
		key,
		index,
		updatedAt: Date.now(),
		...prev,
		id: id ?? prev?.id,
		agent: str(payload.agent) ?? prev?.agent,
		description: str(payload.description) ?? prev?.description,
		task: str(payload.task) ?? prev?.task,
		parentToolCallId: str(payload.parentToolCallId) ?? prev?.parentToolCallId,
		parentSubagentId: str(payload.parentSubagentId) ?? prev?.parentSubagentId,
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
			// 新一轮开始：上一轮的输入预测作废
			return {
				...s,
				running: true,
				lastPromptResult: null,
				composerPrediction: [],
			};
		case "agent_end":
			// isTerminal === false：仍有续跑或异步投递
			return f.isTerminal !== false
				? {
						...s,
						running: false,
						retry: null,
						optimisticQueue: s.optimisticQueue.filter((o) => !o.settled),
					}
				: s;
		case "turn_start":
		case "turn_end":
			return s;
		case "message_start": {
			// 被顶替的上一条：已结束、可能进转录（toolResult 不渲染；没有时间戳认不出接替它的条目）、条目还没到的留到 landing
			const prev = s.liveMessage;
			const landing =
				prev?.ended &&
				prev.role !== "toolResult" &&
				prev.timestamp !== undefined &&
				!s.entries.some((e) => entryCoversMessage(e, prev))
					? [...s.landing, prev]
					: s.landing;
			return {
				...s,
				liveMessage: startLiveMessage(f, `live-${s.liveSerial}`),
				liveSerial: s.liveSerial + 1,
				landing,
				liveSuppressed: false,
			};
		}
		case "message_update":
		case "message_end": {
			if (s.liveSuppressed) return s;
			// 没有 message_start 就到的增量 / 结束帧：按本帧开一条，同样占一个行键序号
			const live = s.liveMessage ?? startLiveMessage(f, `live-${s.liveSerial}`);
			return {
				...s,
				liveMessage:
					frame.type === "message_end"
						? endLiveMessage(live, f)
						: applyMessageUpdate(live, f),
				liveSerial: s.liveMessage ? s.liveSerial : s.liveSerial + 1,
			};
		}
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
			// omp 的工具挂载变动提示（旧版 `xdev` / 新版 `tool-commands`）只是罗列工具名，对人没有信息量；
			// 模型照样会随下一条 prompt 得知变动，这里不进转录
			if (f.source === "xdev" || f.source === "tool-commands") return s;
			const level =
				f.level === "warning" || f.level === "error" ? f.level : "info";
			return pushNotice(s, "notice", level, str(f.message) ?? "", f);
		}
		case "thinking_level_changed": {
			if (!s.state) return s;
			const nextEffective =
				(f.resolved as SessionState["thinkingLevel"]) ??
				(f.thinkingLevel as SessionState["thinkingLevel"]) ??
				s.state.thinkingLevel;
			// 上游事件只在配置值与生效值不同（如 auto）时附带 configured；切回具体档位时省略它。
			const nextConfigured =
				(f.configured as SessionState["thinkingConfigured"]) ??
				(f.thinkingLevel as SessionState["thinkingConfigured"]) ??
				s.state.thinkingConfigured;
			return {
				...s,
				state: {
					...s.state,
					thinkingLevel: nextEffective,
					thinkingConfigured: nextConfigured,
					configuredThinkingLevel: nextConfigured,
				},
			};
		}
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
		case "queue_update": {
			const queue = queueFrom(f);
			const queueItems = queueItemsFrom(f);
			return {
				...s,
				queue,
				queueItems,
				optimisticQueue: reconcileOptimisticQueue(
					s.optimisticQueue,
					queue,
					queueItems,
				),
			};
		}

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
				optimisticQueue: s.optimisticQueue.filter((o) => !o.settled),
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
			// 增量属于最新一轮：有追问时是 followUps 末尾，否则是顶层首轮
			const id = str(f.recordId);
			const prev = id ? s.btw[id] : undefined;
			if (!id || !prev) return s;
			const delta = str(f.delta) ?? "";
			const followUps = prev.followUps;
			const last = followUps?.at(-1);
			const next: RpcBtwHistoryRecord =
				followUps && last
					? {
							...prev,
							followUps: [
								...followUps.slice(0, -1),
								{ ...last, answer: `${last.answer}${delta}` },
							],
						}
					: { ...prev, answer: `${prev.answer}${delta}` };
			return { ...s, btw: { ...s.btw, [id]: next } };
		}
		case "btw_record": {
			const rec = isRecord(f.record)
				? (f.record as unknown as RpcBtwHistoryRecord)
				: null;
			if (!rec?.id) return s;
			const btw = { ...s.btw, [rec.id]: rec };
			return { ...s, btw, btwOrder: btwOrderOf(btw) };
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
		case "oryn_composer_prediction":
			return { ...s, composerPrediction: predictionChoices(f.choices) };
		default:
			if (IGNORED_FRAME_TYPES[frame.type]) return s;
			return pushDebug(s, "frame", frame);
	}
}

/**
 * `live.reset`（agent 断档 / 重启）：服务端已清空在途帧、工作标志、待答 UI、计划提案、子 agent 与展示类 UI
 * （setStatus / setWidget），客户端同步作废这些状态（随后由 `refresh_state` 拉回权威 state；断档后同一进程的
 * 展示类 UI 由服务端重放的帧重建）；叠加层抑制到下一个 message_start。
 */
export function applyLiveReset(s: ThreadViewState): ThreadViewState {
	return {
		...s,
		liveMessage: null,
		landing: [],
		liveTools: {},
		liveToolOrder: [],
		liveSuppressed: true,
		running: false,
		pendingUi: [],
		statuses: {},
		widgets: {},
		planProposal: null,
		subagents: {},
		compaction: null,
		retry: null,
		composerPrediction: [],
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

/** 这个 UI 请求是视图的阻塞请求时，对应的 toolCallId（C5：`input` 且 placeholder 带 `oryn-view:` 标记）。 */
export function viewAwaitCallIdOf(req: ExtensionUiRequest): string | null {
	return viewAwaitCallId(
		req.method,
		"placeholder" in req ? req.placeholder : undefined,
	);
}

const dockPendingCache = new WeakMap<
	readonly ExtensionUiRequest[],
	ExtensionUiRequest[]
>();

/**
 * 输入坞要处理的 UI 请求：去掉视图的阻塞请求（它们由转录里对应的视图作答）。没有视图请求时原样返回同一个数组，
 * 有时按 `pendingUi` 的引用缓存——可直接作 store 选择器。
 */
export function dockPendingUi(
	s: Pick<ThreadViewState, "pendingUi">,
): ExtensionUiRequest[] {
	if (!s.pendingUi.some((r) => viewAwaitCallIdOf(r) !== null))
		return s.pendingUi;
	let out = dockPendingCache.get(s.pendingUi);
	if (!out) {
		out = s.pendingUi.filter((r) => viewAwaitCallIdOf(r) === null);
		dockPendingCache.set(s.pendingUi, out);
	}
	return out;
}

/** 这次视图调用挂起等待作答的请求（`await`）；没有时为 undefined。 */
export function viewAwaitOf(
	s: Pick<ThreadViewState, "pendingUi">,
	toolCallId: string,
): ExtensionUiRequest | undefined {
	return s.pendingUi.find((r) => viewAwaitCallIdOf(r) === toolCallId);
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
		rowKeys: keepEntries ? s.rowKeys : {},
		liveSerial: s.liveSerial,
		lastSeq: keepEntries ? s.lastSeq : 0,
		transcriptRev: snap.transcriptRev,
		// 服务端开窗时（只在从 seq 0 补发时）以它为准；续传沿用本地的窗口
		windowStart: snap.windowStart || (keepEntries ? s.windowStart : 0),
		notices: s.notices,
		noticeSeq: s.noticeSeq,
		debug: s.debug,
		commands: s.commands,
		btw: s.btw,
		btwOrder: s.btwOrder,
		loopMode: s.loopMode,
		vibeEnabled: s.vibeEnabled,
		editorText: s.editorText,
		composerPrediction: predictionChoices(snap.agent.composerPrediction),
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
		running:
			typeof snap.agent.working === "boolean"
				? snap.agent.working
				: next.state
					? next.state.isStreaming || !next.state.isSettled
					: false,
	};
	if (isRecord(snap.agent.queue)) {
		const queue = queueFrom(snap.agent.queue);
		const queueItems = queueItemsFrom(snap.agent.queue);
		next = {
			...next,
			queue,
			queueItems,
			optimisticQueue: reconcileOptimisticQueue(
				s.optimisticQueue,
				queue,
				queueItems,
			),
		};
	} else if (s.optimisticQueue.length > 0) {
		next = {
			...next,
			optimisticQueue: s.optimisticQueue.filter((o) => !o.settled),
		};
	}
	for (const ui of snap.agent.pendingUi) {
		if (isRecord(ui) && typeof ui.type === "string")
			next = applyFrame(next, ui as OmpFrame);
	}
	// 展示类 UI（setStatus / setWidget）：服务端按 key 保留当前值，重新订阅后据此恢复
	for (const ui of snap.agent.displayUi ?? []) {
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

/** 预测候选：只保留非空字符串（帧与快照共用）。 */
function predictionChoices(v: unknown): string[] {
	return Array.isArray(v)
		? v.filter((c): c is string => typeof c === "string" && c.length > 0)
		: [];
}

/** 收到 `thread.snapshot` 后需要主动拉取的数据：agent 就绪时补 state、命令表，以及恢复的队列的可编辑条目。 */
export function snapshotEffects(snap: WsThreadSnapshot): FrameEffect[] {
	if (snap.agent.status !== "ready") return [];
	const effects: FrameEffect[] = ["refresh_commands"];
	if (!snap.agent.state) effects.push("refresh_state");
	if (isRecord(snap.agent.queue)) {
		const q = queueFrom(snap.agent.queue);
		if (
			(q.steering.length > 0 || q.followUp.length > 0) &&
			queueItemsFrom(snap.agent.queue) === null
		)
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
		case "queue_update": {
			const f = frame as JsonRecord;
			const q = queueFrom(f);
			return (q.steering.length > 0 || q.followUp.length > 0) &&
				queueItemsFrom(f) === null
				? ["refresh_queue"]
				: [];
		}
		default:
			return [];
	}
}
