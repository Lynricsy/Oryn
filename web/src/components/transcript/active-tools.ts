/**
 * 当前这一批工具调用：线程工作中时，最后一条带工具调用的助手消息里的全部调用（并行调用同属一批）。
 * 这一批的工具卡默认展开；模型开始下一批调用，或回合结束（线程不再 working、等待用户输入）时自动收起。
 */
import { createContext, useContext } from "react";
import { isRecord } from "../../tool-render/util";
import type { MainlineRow } from "./mainline";

export const NO_ACTIVE_TOOL_CALLS: ReadonlySet<string> = new Set();

/**
 * 从转录末尾往前找本回合最近一批工具调用的 id；碰到用户消息（回合边界）就停，
 * 上一回合的调用不会在新回合开始、模型还没调用工具时重新展开。运行中插入的 steer
 * （`steering` / `liveSteered`）属于同一回合，不算边界。没有时为空集。
 */
export function activeToolCallIds(
	rows: readonly MainlineRow[],
): ReadonlySet<string> {
	for (let i = rows.length - 1; i >= 0; i--) {
		const row = rows[i];
		if (!row) continue;
		// 还没并入助手消息的在途工具行：连同紧挨着的同类行算一批
		if (row.kind === "tool") {
			const ids = new Set<string>();
			for (let j = i; j >= 0; j--) {
				const r = rows[j];
				if (r?.kind !== "tool") break;
				ids.add(r.toolCallId);
			}
			return ids;
		}
		if (
			row.kind === "message" &&
			row.message.role === "user" &&
			row.message.steering !== true &&
			row.message.liveSteered !== true
		)
			return NO_ACTIVE_TOOL_CALLS;
		if (row.kind !== "message" || row.message.role !== "assistant") continue;
		const content = row.message.content;
		if (!Array.isArray(content)) continue;
		const ids = new Set<string>();
		for (const b of content) {
			if (isRecord(b) && b.type === "toolCall" && typeof b.id === "string")
				ids.add(b.id);
		}
		if (ids.size > 0) return ids;
	}
	return NO_ACTIVE_TOOL_CALLS;
}

export const ActiveToolCallsContext =
	createContext<ReadonlySet<string>>(NO_ACTIVE_TOOL_CALLS);

/** 这次工具调用是否属于当前一批（卡片据此默认展开）。 */
export function useToolCallActive(toolCallId: string): boolean {
	return useContext(ActiveToolCallsContext).has(toolCallId);
}

/** 批量展开/收起工具卡与思考块（Rewind 模式 Ctrl+O）：epoch 递增时重置各卡片的手动展开态。 */
export interface ToolsExpandState {
	expanded: boolean;
	epoch: number;
}

export const DEFAULT_TOOLS_EXPAND: ToolsExpandState = {
	expanded: false,
	epoch: 0,
};

export const ToolsExpandContext =
	createContext<ToolsExpandState>(DEFAULT_TOOLS_EXPAND);

export function useToolsExpand(): ToolsExpandState {
	return useContext(ToolsExpandContext);
}
