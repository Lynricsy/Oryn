/**
 * Oryn Core 在 extension UI 对话框里发来的固定英文文案 → i18n 键（M12.2）。
 * 目前只有 `/omfg` 的「保存到哪里」对话框：新 Core（`ORYN_CENTRAL_CONFIG=1`，≥ oryn-v18.19.0-8）的 Oryn 文案，
 * 与旧 Core / runner 仍发的上游原文（落在仓库 `.omp/rules` 或本机 `~/.omp/agent/rules`）。
 * 字面量与 Core 的 `rpc-omfg.ts`、控制面的 `internal/threads/cfgsync.go` 一致，改动须三处同步。
 */
import type { ParseKeys } from "i18next";

/** `/omfg` 保存位置对话框的标题（新旧 Core 相同）。 */
export const OMFG_SAVE_TITLE = "Save TTSR rule where?";

const CORE_LABELS: Record<string, ParseKeys> = {
	[OMFG_SAVE_TITLE]: "uiRequest.core.omfgTitle",
	"This project (Oryn project config)": "uiRequest.core.omfgProject",
	"Global — all projects (Oryn central config)": "uiRequest.core.omfgGlobal",
	"Amend with feedback…": "uiRequest.core.omfgAmend",
	// 旧 Core（未重建的 orb）与 runner：上游原文
	"This project (.omp/rules)": "uiRequest.core.omfgProjectRepo",
	"Global — all projects (~/.omp/agent/rules)":
		"uiRequest.core.omfgGlobalLocal",
};

/** Oryn Core 发来的固定英文文案 → i18n 键；不在表里的原样显示。 */
export function coreLabelKey(text: string): ParseKeys | undefined {
	return Object.hasOwn(CORE_LABELS, text) ? CORE_LABELS[text] : undefined;
}
