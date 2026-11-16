/**
 * 输入区上方的选中元素条（计划 M14 3.5）：「{标题} › {锚点标签}」+ 移除钮，位置与样式同引用回复条。
 * 选中来自右面板查看器的选择模式；发送普通消息时由输入区用 `selectionPrompt` 把它接在正文后面。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { ArtifactSelectionRef } from "../../lib/artifacts/prompt";
import { Icon } from "../ui/icons";
import { anchorLabel } from "./anchors";

export function SelectionChip({
	selection,
	onRemove,
}: {
	selection: ArtifactSelectionRef;
	onRemove: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const label = t("artifacts.selection.chip", {
		title: selection.title,
		target: anchorLabel(selection.anchor, t),
	});
	return (
		<div className="px-3 pt-3" data-testid="composer-artifact-selection">
			<div className="flex items-center gap-2 rounded-lg border border-border bg-panel py-1.5 pr-1.5 pl-3 shadow-xs">
				<Icon
					name="cursor"
					size={14}
					aria-hidden
					className="shrink-0 text-faint"
				/>
				<p
					className="min-w-0 flex-1 truncate text-muted text-xs leading-5"
					title={label}
				>
					<span className="sr-only">{t("artifacts.selection.selected")}</span>
					{label}
				</p>
				<button
					type="button"
					aria-label={t("artifacts.selection.remove")}
					title={t("artifacts.selection.remove")}
					className="inline-grid h-6 w-6 shrink-0 place-items-center rounded-md text-faint transition-colors hover:bg-hover hover:text-fg"
					onClick={onRemove}
				>
					<Icon name="close" size={12} />
				</button>
			</div>
		</div>
	);
}
