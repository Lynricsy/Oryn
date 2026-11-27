/**
 * omp RPC 类型入口。权威类型在 `./rpc-types.d.ts`（由 oryn-core `scripts/oryn/emit-rpc-dts.ts` 生成，不手改）；
 * 本文件只做再导出，并派生出 rpc-types 未直接导出的常用类型。全前端一律从这里导入 omp 类型。
 */
import type {
	RpcAskDialogQuestion,
	RpcCommand,
	RpcGetQueueResult,
	RpcQueueClearResult,
	RpcQueueEditResult,
	RpcQueueMoveResult,
	RpcQueueRemoveResult,
	RpcExtensionUIRequest,
	RpcExtensionUIResponse,
	RpcFormSection,
	RpcMessageEventFrame,
	RpcResponse,
	RpcSessionState,
} from "./rpc-types";

export type * from "./rpc-types";

type DistributiveOmit<T, K extends PropertyKey> = T extends unknown ? Omit<T, K> : never;

/** 会话消息（user / assistant / toolResult / custom / bashExecution …）。 */
export type AgentMessage = RpcMessageEventFrame["message"];
export type AssistantMessage = Extract<AgentMessage, { role: "assistant" }>;
export type UserMessage = Extract<AgentMessage, { role: "user" }>;
export type ToolResultMessage = Extract<AgentMessage, { role: "toolResult" }>;
export type AssistantContentBlock = AssistantMessage["content"][number];
export type ToolCallBlock = Extract<AssistantContentBlock, { type: "toolCall" }>;
export type ImageContent = Extract<AssistantContentBlock, { type: "image" }>;

/** 客户端发出的命令（不带 `id`，由控制面注入）。 */
export type OmpCommand = DistributiveOmit<RpcCommand, "id">;
export type OmpCommandType = OmpCommand["type"];
export type CommandOf<T extends OmpCommandType> = Extract<OmpCommand, { type: T }>;

/**
 * Oryn 在 prompt / steer / follow_up / abort_and_prompt 帧上的扩展字段（控制面下发前删除，omp 看不到）：
 * 已上传附件的 id，控制面投递进 orb 后在 message 末尾追加 `<oryn-attachments>` 块；
 * 全局助手对话改用 `orynFiles`（助手文件库的 id，≤20），控制面绑定到本对话并追加沙箱路径块。
 */
export interface OrynPromptExtras {
	orynAttachments?: string[];
	orynFiles?: string[];
}

/** rpc-types 的 `RpcResponse` 联合里缺失的应答类型（以 `Rpc*Result` 为准补齐）。 */
interface MissingResponses {
	get_queue: RpcGetQueueResult;
	queue_edit: RpcQueueEditResult;
	queue_remove: RpcQueueRemoveResult;
	queue_move: RpcQueueMoveResult;
	queue_clear: RpcQueueClearResult;
}

/** 某命令成功应答的 `data`。 */
export type ResponseData<T extends string> = T extends keyof MissingResponses
	? MissingResponses[T]
	: Extract<RpcResponse, { command: T; success: true }> extends { data: infer D }
		? D
		: undefined;

export type ExtensionUiRequest = RpcExtensionUIRequest;
export type ExtensionUiMethod = RpcExtensionUIRequest["method"];
export type ExtensionUiRequestOf<M extends ExtensionUiMethod> = Extract<RpcExtensionUIRequest, { method: M }>;
export type ExtensionUiResponse = RpcExtensionUIResponse;
export type AskQuestion = RpcAskDialogQuestion;
export type FormSection = RpcFormSection;
export type FormBlock = FormSection["blocks"][number];
export type FormSpan = FormSection["title"][number];
export type FormFieldBlock = Extract<FormBlock, { kind: "field" }>;
export type FormField = FormFieldBlock extends { field: infer F } ? F : never;

export type SessionState = RpcSessionState;
export type TodoPhase = RpcSessionState["todoPhases"][number];
export type TodoItem = TodoPhase["tasks"][number];
export type GoalModeState = NonNullable<RpcSessionState["goal"]>;
export type ContextUsage = NonNullable<RpcSessionState["contextUsage"]>;
export type OmpModel = NonNullable<RpcSessionState["model"]>;

/**
 * 下一次请求在 prompt cache 里能找到什么（Core `PromptCacheStatus`，见 oryn-core `modes/rpc/wire/state.ts`）。
 * Core 19.2 起 `get_session_stats` 在运行时把它展开到应答上（rpc-mode 的 `{ ...stats, promptCache }`），
 * 但生成的 rpc-types 里 `SessionStats` 没有声明这个字段，这里照 wire schema 补齐。
 */
export type PromptCacheColdReason =
	| { kind: "expired"; at: number }
	/** omp 的续缓存请求发现缓存已经没了 */
	| { kind: "refresh-missed" }
	| { kind: "model"; from: string; to: string }
	| { kind: "thinking"; from: string; to: string }
	| { kind: "tools" }
	| { kind: "system-prompt" }
	| { kind: "retention" }
	| { kind: "compaction" };

export interface PromptCacheWarming {
	/** `scheduled`：已排好下一次续期；`refreshing`：续期请求在途；`inactive`：没有续期（原因见 `reason`）。 */
	state: "scheduled" | "refreshing" | "inactive";
	nextWarmAt?: number;
	/** 续期放弃的时刻（空闲 30 分钟窗口，或流式期间续期不再划算）。 */
	stopsAt?: number;
	reason?: string;
}

export interface PromptCacheStatus {
	/** `cold`：下一次请求要重写缓存前缀。 */
	state: "warm" | "cold";
	reason?: PromptCacheColdReason;
	/** 从重新打开的会话转录推断，不是本进程见到的应答（思考级别、工具未知，也没有续期信息）。 */
	inferred: boolean;
	tier: "short" | "long";
	/** 最后一次读或写这份缓存的请求发出时刻。 */
	touchedAt: number;
	/** 最后一次触达后的存活时长；提供方从不让它过期时缺省。 */
	ttlMs?: number;
	/** 热缓存能提供的提示词 token 数。 */
	cachedTokens: number;
	/** 下一次请求的提示词 token 估算。 */
	nextTokens: number;
	warming?: PromptCacheWarming;
}

export type SessionStatsData = ResponseData<"get_session_stats"> & {
	promptCache?: PromptCacheStatus;
};
