/**
 * omp RPC 类型入口。权威类型在 `./rpc-types.d.ts`（由 oryn-agent `scripts/oryn/emit-rpc-dts.ts` 生成，不手改）；
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
