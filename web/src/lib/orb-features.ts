/**
 * runner（M11b）的线程右面板按 orbd 声明的特性（`orb.features`）决定内容标签：改动要 `git`、文件要 `fs`、终端要 `terminal`。
 * 普通 orb（`kind` 缺省或为 `orb`）不看特性、三个都在：旧 orbd 的 hello 也带特性列表，但不含 `terminal`，
 * 按特性过滤会让所有旧 orb 的终端标签消失。
 */
import type { Orb } from "../api/client";

export const CONTENT_TABS = ["changes", "files", "terminal"] as const;

export type ContentTab = (typeof CONTENT_TABS)[number];

const TAB_FEATURE: Record<ContentTab, string> = {
	changes: "git",
	files: "fs",
	terminal: "terminal",
};

export function contentTabs(
	orb: Pick<Orb, "features" | "kind"> | undefined,
): ContentTab[] {
	if (orb?.kind !== "runner") return [...CONTENT_TABS];
	const features = orb.features ?? [];
	return CONTENT_TABS.filter((tab) => features.includes(TAB_FEATURE[tab]));
}
