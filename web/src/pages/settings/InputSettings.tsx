/** 设置 → 输入与快捷键：新对话默认对象、输入框预测、选区快捷动作。 */
import type { ReactNode } from "react";
import SelectionActionsSettings from "../../components/settings/SelectionActionsSettings";
import ComposerPage from "./agent/ComposerPage";
import { NewThreadTargetCard } from "./IdentitySettings";

export default function InputSettings(): ReactNode {
	return (
		<>
			<NewThreadTargetCard />
			<ComposerPage />
			<SelectionActionsSettings />
		</>
	);
}
