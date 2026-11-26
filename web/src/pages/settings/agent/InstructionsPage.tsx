/**
 * 设置 → 技能与指令：技能、顶层提示词与上下文文件、命令 / 规则 / 提示片段 / instruction 合在一页。
 * 作用范围说明与下发状态只在页顶出现一次；之后每类一个 `Group`。
 */
import type { ReactNode } from "react";
import { AgentPageHead } from "../../../components/settings/agent-config-ui";
import CollectionsPage from "./CollectionsPage";
import PromptsPage from "./PromptsPage";
import SkillsPage from "./SkillsPage";

export default function InstructionsPage(): ReactNode {
	return (
		<>
			<AgentPageHead />
			<SkillsPage />
			<PromptsPage />
			<CollectionsPage />
		</>
	);
}
