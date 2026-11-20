/**
 * 选区菜单组：由认领选区的登记方给出 `SelectionContext`，这里生成「引用回复 / 复制 …」等项。
 */

import type { MenuItem } from "../../components/ui/primitives";
import i18n from "../../i18n";
import { showToast } from "../../state/toasts";
import { copyToClipboard } from "../clipboard";
import type { MenuGroup, SelectionContext } from "./types";

/** 复制并在失败时提示（成功不提示）。 */
export async function copyWithToast(text: string): Promise<void> {
	if (!(await copyToClipboard(text)))
		showToast({ text: i18n.t("contextMenu.copyFailed"), tone: "err" });
}

export function selectionGroups(sel: SelectionContext): MenuGroup[] {
	const items: MenuItem[] = [];
	if (sel.quote)
		items.push({
			key: "quote",
			icon: "quote",
			label: i18n.t("transcript.quoteReply"),
			onSelect: sel.quote,
		});
	items.push({
		key: "copy",
		icon: "copy",
		label: i18n.t("common.copy"),
		onSelect: () => void copyWithToast(sel.text),
	});
	return [{ key: "selection", items }];
}
