/** 转录渲染上下文：工具结果 / 运行中工具查找、子 agent 查看、消息操作回调；`readOnly` 用于匿名分享页。 */
import { createContext, useContext } from "react";
import type { JsonRecord } from "../../state/message-builder";
import type { LiveTool, SubagentLive } from "../../state/thread-reducer";

export interface TranscriptContextValue {
	threadId: string;
	toolResults: Map<string, JsonRecord>;
	liveTools: Record<string, LiveTool>;
	subagents: Record<string, SubagentLive>;
	openSubagent(id: string): void;
	forkFrom(entryId: string): void;
	labelEntry(entryId: string): void;
	/** 只读（分享页）：消息菜单只留「复制」，task 卡片不提供「查看转录」。 */
	readOnly?: boolean;
}

export const TranscriptContext = createContext<TranscriptContextValue | null>(
	null,
);

export function useTranscriptContext(): TranscriptContextValue {
	const ctx = useContext(TranscriptContext);
	if (!ctx) throw new Error("TranscriptContext 缺失");
	return ctx;
}
