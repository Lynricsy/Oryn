/**
 * 选区菜单组：由认领选区的登记方给出 `SelectionContext`，这里生成两组项——
 * AI 动作（解释 / 就此提问… / 翻译 / 自定义快捷动作，走旁路提问）与通用项（引用回复 / 引用到输入框 / 复制 / 搜索）。
 */

import {
	openPalette,
	paletteAvailable,
} from "../../components/palette/palette-state";
import type { MenuItem } from "../../components/ui/primitives";
import i18n from "../../i18n";
import { insertIntoComposer } from "../../state/composer-drafts";
import { selectionActionsStore } from "../../state/selection-actions";
import {
	openSideAskCompose,
	runSideAsk,
	sideAskBusy,
} from "../../state/side-ask";
import { showToast } from "../../state/toasts";
import { copyToClipboard } from "../clipboard";
import type { MenuGroup, SelectionContext } from "./types";

/** 菜单里直接列出的自定义动作个数，其余收进「更多动作」 */
const INLINE_CUSTOM = 3;
const SEARCH_MAX = 200;

/** 复制并在失败时提示（成功不提示）。 */
export async function copyWithToast(text: string): Promise<void> {
	if (!(await copyToClipboard(text)))
		showToast({ text: i18n.t("contextMenu.copyFailed"), tone: "err" });
}

/** 搜索词：选区第一行，压空白，截 200 字符。 */
function searchQueryOf(text: string): string {
	const line = text.split("\n").find((l) => l.trim()) ?? "";
	return line.replace(/\s+/g, " ").trim().slice(0, SEARCH_MAX);
}

function aiGroup(sel: SelectionContext, threadId: string): MenuGroup {
	const busy = sideAskBusy(threadId);
	const custom = selectionActionsStore
		.getState()
		.actions.map<MenuItem>((a) => ({
			key: `custom:${a.id}`,
			icon: "bolt",
			label: a.name,
			onSelect: () =>
				void runSideAsk(
					{ kind: "custom", name: a.name, prompt: a.prompt },
					sel,
				),
		}));
	const items: MenuItem[] = [
		{
			key: "explain",
			icon: "sparkle",
			label: i18n.t("contextMenu.explain"),
			onSelect: () => void runSideAsk({ kind: "explain" }, sel),
		},
		{
			key: "ask",
			icon: "question",
			label: i18n.t("contextMenu.ask"),
			onSelect: () => openSideAskCompose(sel),
		},
		{
			key: "translate",
			icon: "globe",
			label: i18n.t("contextMenu.translate"),
			onSelect: () => void runSideAsk({ kind: "translate" }, sel),
		},
		...custom.slice(0, INLINE_CUSTOM),
	];
	if (custom.length > INLINE_CUSTOM)
		items.push({
			key: "more",
			icon: "bolt",
			label: i18n.t("contextMenu.moreActions"),
			children: custom.slice(INLINE_CUSTOM),
		});
	return {
		key: "selection.ai",
		items: busy
			? items.map((i) => ({
					...i,
					disabled: true,
					title: i18n.t("sideAsk.busy"),
				}))
			: items,
	};
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
	const { insert, threadId } = sel;
	if (insert && threadId)
		items.push({
			key: "insert",
			icon: "message",
			label: i18n.t("contextMenu.insert"),
			onSelect: () => insertIntoComposer(threadId, insert()),
		});
	items.push({
		key: "copy",
		icon: "copy",
		label: i18n.t("common.copy"),
		onSelect: () => void copyWithToast(sel.text),
	});
	if (paletteAvailable())
		items.push({
			key: "search",
			icon: "search",
			label: i18n.t("contextMenu.search"),
			onSelect: () => openPalette(searchQueryOf(sel.text)),
		});
	const groups: MenuGroup[] = [];
	if (threadId) groups.push(aiGroup(sel, threadId));
	groups.push({ key: "selection", items });
	return groups;
}
