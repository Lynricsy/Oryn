/**
 * Tool renderer contract.
 *
 * Every tool gets a renderer:
 * - `Summary` — one-line inline header content (dense, truncated by the chrome).
 * - `Input` / `Result` — 展开体的两段：调用参数（输入）与执行结果（输出），由 ToolView 分段排版。
 *
 * Renderers are host-agnostic: they run inside the collab-web React app and
 * inside the `<omp-tool-view>` web component bundled into HTML session exports.
 * They must never import host-specific modules (wire types, coding-agent
 * runtime, node builtins) and must tolerate partial/malformed `args` and
 * `details` — these arrive as plain JSON over the wire.
 */
import type { ComponentType } from "react";
import type { AgentVisualState } from "../components/ui/AgentStatusAvatar";
import type { ViewAnswer } from "../lib/view/answer";

export interface ToolResultText {
	type: "text";
	text: string;
}

export interface ToolResultImage {
	type: "image";
	/** Base64-encoded image data. */
	data: string;
	/** e.g. "image/png". */
	mimeType: string;
}

export type ToolResultBlock =
	| ToolResultText
	| ToolResultImage
	| { type: string };

export interface ToolResultLike {
	content: readonly ToolResultBlock[];
	details?: unknown;
	isError?: boolean;
}

/** 后台任务的状态：Core 的四种，加上「已结束」（Core 不再记录、也没收到终态）。 */
export type ToolJobStatus =
	| "running"
	| "completed"
	| "failed"
	| "cancelled"
	| "ended";

/**
 * 这次调用转入了后台（结果带 `details.async`）：任务此刻的状态由宿主合成后传给卡片。
 * 卡片头部据此显示动态标记（收起时也在），展开体附任务的最近输出与入口。
 */
export interface ToolJobMark {
	/** Core 的任务 id（`bg_3`）。 */
	id: string;
	status: ToolJobStatus;
	/** 任务的最近输出（实时尾巴或最终结果）。 */
	output?: string;
	/** 打开任务详情；只读宿主不给。 */
	onOpen?: () => void;
}

/**
 * Capabilities the embedding host exposes to renderers. Functions are live
 * objects (passed via property assignment or the payload store) — they cannot
 * ride the JSON `payload` attribute.
 */
export interface ToolRenderHost {
	/** True when the host can show a transcript for this agent id. */
	hasAgent?(id: string): boolean;
	/** Open the sub-session/transcript view for an agent id. */
	openAgent?(id: string): void;
	/** 在当前线程的右面板里打开一个 artifact（转录里的 artifact 卡片）；只读的分享页不提供。 */
	openArtifact?(id: string): void;
	/**
	 * 当前线程 orb 上某个文件的地址（`desktop_record` 卡片内嵌播放录像）。orb 已删除时返回 null；
	 * 只读的分享页不提供，卡片只显示路径。
	 */
	orbFileUrl?(path: string): string | null;
	/** 在当前线程的文件面板里打开 orb 上的某个文件；只读的分享页不提供。 */
	openOrbFile?(path: string): void;
	/** 助手文件库里某个文件的内容地址（`computer_record` 卡片内嵌播放存进文件库的录像）；只读的分享页不提供。 */
	libraryFileUrl?(id: string): string;
	/**
	 * 渲染一个 agent 的状态头像（站内读 agent 头像设置，分享页读公开身份）。
	 * 不给时渲染器只用各 agent 的默认字形，不读任何设置。
	 */
	AgentAvatar?: ComponentType<AgentAvatarProps>;
	/** 视图（M33）的作答与帧；不给时视图只读显示、没有作答记录。 */
	view?: ViewHost;
}

/** 视图（M33，docs/plan-m33.md 的 C10）的宿主能力。 */
export interface ViewHost {
	/** 这次调用在转录里已有的作答（含在途提交）：块路径 → 作答。 */
	answers(toolCallId: string): ReadonlyMap<string, ViewAnswer>;
	/** 这次调用是否正挂起等待作答（`await`）。 */
	awaiting(toolCallId: string): boolean;
	/** 提交作答：挂起时作为工具结果返回，否则作为下一条消息。只读页不提供。 */
	submit?(toolCallId: string, answer: ViewAnswer, text: string): Promise<void>;
	/** 申请 HTML 视图帧，返回帧根地址；`fresh` = 旧帧失效，重新申请。 */
	openFrame?(toolCallId: string, fresh?: boolean): Promise<string>;
}

/** `ToolRenderHost.AgentAvatar` 的参数。 */
export interface AgentAvatarProps {
	/** agent 名（类型，如 `scout`），不是子 agent 的 id。 */
	agent: string;
	state: AgentVisualState;
	/** tooltip 与 aria-label。 */
	title: string;
	size?: number;
}

export interface ToolRenderProps {
	/** Wire tool name (may be an alias of the registry key, e.g. `grep` → search). */
	name: string;
	/** Parsed tool-call arguments with the internal `i` intent already stripped. */
	args: Record<string, unknown>;
	result?: ToolResultLike;
	/** Tool is still executing (live collab view). */
	running?: boolean;
	/** 运行中的结构化部分结果（tool_execution_update.partialResult）；仅在 running 且尚无 result 时有值。 */
	partial?: ToolResultLike;
	/** 调用开始执行的时刻（epoch ms）：只在实时视图里有值，运行中的计时、等待时长据此走动。 */
	startedAt?: number;
	/** Host capabilities (sub-session drill-down, …). */
	host?: ToolRenderHost;
}

/**
 * 直出渲染器的参数（C10）：不套卡片外壳，三种规格来源都传下来——结果的 `details.view.spec`（在 `result` 里）、
 * `tool_execution_start` 的已修复参数（`liveArgs`）、toolCall 块里的原始参数（`args`，流式中是残缺对象）。
 */
export type ToolInlineProps = ToolRenderProps & {
	toolCallId: string;
	/** 参数仍在流式。 */
	streaming?: boolean;
	/** 已收到 `tool_execution_start`（参数已通过校验、调用已开始执行）。 */
	started?: boolean;
	/** `tool_execution_start` 带来的已修复参数（去掉了意图字段）。 */
	liveArgs?: Record<string, unknown>;
	/** 根元素的 ref（宿主在上面登记右键菜单）。 */
	rootRef?: (el: HTMLDivElement | null) => void;
};

export interface ToolRenderer {
	/**
	 * 头部摘要：一行内联内容（不得渲染块级元素）。有意图（`i`）时头部一直显示意图（收起、展开都一样），
	 * 没有意图才显示 Summary；完整参数由 Input 段给出。
	 */
	Summary: ComponentType<ToolRenderProps>;
	/**
	 * 头部右侧的结果读数（行数、+N −M、匹配数、通过数与耗时……），用 `tv-meta-*` 类按语义着色。
	 * 读数只在这里出现一次：Summary、Input、Result 不再重复。Omit when there is nothing to read out.
	 */
	Meta?: ComponentType<ToolRenderProps>;
	/**
	 * 展开体「输入」段：调用参数的完整视图（命令、代码、路径、模式、选项）。
	 * 只展示模型给出的参数；结果派生的信息放进 `Result`。渲染 null 时整段不出现。
	 */
	Input?: ComponentType<ToolRenderProps>;
	/**
	 * 展开体「输出」段：执行结果（输出文本、diff、图片、错误、运行中的结构化进度）。
	 * 渲染 null 时整段不出现。
	 */
	Result?: ComponentType<ToolRenderProps>;
	/** 为真时渲染器自己展示 partial，ToolView 不再输出文本尾巴。 */
	livePartial?: boolean;
	/** 直出：有它时不出卡片外壳（头部 / 展开体）、不随批次开合，直接渲染它（视图）。 */
	Inline?: ComponentType<ToolInlineProps>;
}
