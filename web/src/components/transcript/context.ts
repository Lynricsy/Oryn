/** 转录渲染上下文：工具结果 / 运行中工具查找、子 agent 查看、消息操作回调；`readOnly` 用于匿名分享页。 */
import { createContext, useContext } from "react";
import type { Schemas } from "../../api/client";
import type { RoleFace } from "../../lib/turn-role";
import type { JsonRecord } from "../../state/message-builder";
import type { LiveTool, SubagentLive } from "../../state/thread-reducer";

/** 回合头的身份来源（M9.6 §7.3）。 */
export interface TurnIdentities {
	/** 能显示的 role：设了头像的，以及站内没设头像的内置 role（默认字形）（站内 key = role id；分享页 key = 不透明键）。 */
	faces: readonly RoleFace[];
	/** 回落身份；undefined = 默认品牌标 */
	fallback?: { name?: string; avatar: Schemas["AvatarIdentity"] };
	fallbackKind: "agent" | "assistant" | "default";
}

export interface TranscriptContextValue {
	threadId: string;
	toolResults: Map<string, JsonRecord>;
	liveTools: Record<string, LiveTool>;
	subagents: Record<string, SubagentLive>;
	openSubagent(id: string): void;
	forkFrom(entryId: string): void;
	/** 回退到这条用户消息之前，原文放回输入区（消息菜单的「回到这里编辑」）。 */
	rewindTo(entryId: string): void;
	labelEntry(entryId: string): void;
	/** 打开转录里 artifact 卡片指向的 artifact（线程页：右面板；协调对话没有面板：整页查看器）。不给时卡片不可点。 */
	openArtifact?(id: string): void;
	/** 只读（分享页）：消息菜单只留「复制」，task 卡片不提供「查看转录」。 */
	readOnly?: boolean;
	/**
	 * 回合头身份（分享页从匿名身份接口组出）。不给时：只读 → 全部默认品牌标；
	 * 否则由转录自己读站内设置（role 头像、助手 / agent 身份）。
	 */
	identities?: TurnIdentities;
	/**
	 * 子 agent 头像（M13 R9；分享页取自匿名身份接口的 `agents`，只含设了自定义头像的）。只读时只用它，没有的
	 * agent 显示默认字形；非只读时不看它，由头像自己读站内的 agent 头像设置。
	 */
	agentFaces?: readonly Schemas["PublicAgentIdentity"][];
}

export const TranscriptContext = createContext<TranscriptContextValue | null>(
	null,
);

export function useTranscriptContext(): TranscriptContextValue {
	const ctx = useContext(TranscriptContext);
	if (!ctx) throw new Error("TranscriptContext 缺失");
	return ctx;
}

/**
 * 转录末尾正在画「待回复的回合头」（回合已开始、agent 还没有任何输出）：头像行 + 跃动的三点。
 * 此时 footer 里的 WorkingFooter 不再画「工作中」行，免得两处同时表示同一件事。
 */
export const AwaitingReplyContext = createContext(false);
