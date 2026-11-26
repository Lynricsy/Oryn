/** 设置 → 技能与指令：技能、顶层提示词与上下文文件、命令 / 规则 / 提示词 / 指令集合。 */
import type { ReactNode } from "react";
import CollectionsPage from "./CollectionsPage";
import PromptsPage from "./PromptsPage";
import SkillsPage from "./SkillsPage";

export default function InstructionsPage(): ReactNode {
	return (
		<>
			<SkillsPage />
			<PromptsPage />
			<CollectionsPage />
		</>
	);
}
