/** 线程种类（M9）：协调对话（Space 聊天、助手对话）不进侧栏分组、不算工作线程。 */
import type { Thread } from "../api/client";

export function isCoordinatorThread(t: Pick<Thread, "kind">): boolean {
	return t.kind === "space" || t.kind === "assistant";
}
