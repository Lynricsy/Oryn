/**
 * 子 agent 的状态头像（M13 R10 / R12）：总览、详情弹窗与 task 工具卡片共用。头像按 agent 定义取（同一个 scout 不随
 * 所用模型变脸）：站内读 agent 头像设置；分享页（只读转录上下文）只用匿名身份接口给的 `agentFaces`，不发请求。
 */
import type { TFunction } from "i18next";
import { type ReactNode, useContext } from "react";
import { useAgentFace } from "../../api/identity-queries";
import { shareAgentFace } from "../../share/identity";
import type { SubagentArchiveReason } from "../../state/subagent-tree";
import {
	AgentStatusAvatar,
	type AgentVisualState,
} from "../ui/AgentStatusAvatar";
import { TranscriptContext } from "./context";

/**
 * 状态的说明文字（头像的 tooltip 与 aria-label）。只剩保存的转录时按原因说明（agent 未运行 / 较早会话的同名存档）；
 * 状态未知时没有说明，由调用方用 agent 名。
 */
export function agentStateLabel(
	t: TFunction,
	state: AgentVisualState,
	reason?: SubagentArchiveReason,
): string | undefined {
	switch (state) {
		case "archived":
			return t(
				reason === "superseded" ? "subagent.archived" : "subagent.offline",
			);
		case "unknown":
			return undefined;
		default:
			return t(`subagent.state.${state}`);
	}
}

export function AgentAvatar({
	agent,
	state,
	title,
	size,
}: {
	/** agent 名（类型，如 `scout`），不是子 agent 的 id。 */
	agent: string;
	state: AgentVisualState;
	title: string;
	size?: number;
}): ReactNode {
	const ctx = useContext(TranscriptContext);
	const share = ctx?.readOnly === true;
	// 分享页不读站内设置：查询照常挂上（hook 顺序固定）但不发请求
	const site = useAgentFace(agent, !share);
	const { face, glyph } = share ? shareAgentFace(ctx?.agentFaces, agent) : site;
	return (
		<AgentStatusAvatar
			face={face}
			fallbackGlyph={glyph}
			state={state}
			size={size}
			title={title}
		/>
	);
}
