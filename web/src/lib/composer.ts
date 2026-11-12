/**
 * 输入区按键与输入解析（计划 H5.4）：
 * - 空闲：Enter 发送（prompt）；Shift+Enter 换行。
 * - 忙碌：Enter = steer；Ctrl/Cmd+Enter = 排队（follow_up）；Alt+Enter = 中断并发送（abort_and_prompt）。
 * - 连按两次 Esc（间隔 ≤ 500ms）= 中断（abort）。
 * - `!cmd` = bash；`!!cmd` = bash 且不进上下文；`/` 开头 = 斜杠命令。
 */

export type ComposerAction =
	| "prompt"
	| "steer"
	| "follow_up"
	| "abort_and_prompt"
	| "abort"
	| "newline"
	| "none";

export interface KeyLike {
	key: string;
	shiftKey?: boolean;
	ctrlKey?: boolean;
	metaKey?: boolean;
	altKey?: boolean;
	isComposing?: boolean;
}

export const DOUBLE_ESC_MS = 500;

export interface KeyDecision {
	action: ComposerAction;
	/** 本次 Esc 的时间（用于判断下一次是否构成双击）；非 Esc 时为 null。 */
	escAt: number | null;
}

export function composerKeyAction(
	e: KeyLike,
	busy: boolean,
	lastEscAt: number | null,
	now: number,
): KeyDecision {
	if (e.isComposing) return { action: "none", escAt: null };
	if (e.key === "Escape") {
		if (busy && lastEscAt !== null && now - lastEscAt <= DOUBLE_ESC_MS)
			return { action: "abort", escAt: null };
		return { action: "none", escAt: now };
	}
	if (e.key !== "Enter") return { action: "none", escAt: null };
	if (e.shiftKey) return { action: "newline", escAt: null };
	if (!busy) return { action: "prompt", escAt: null };
	if (e.ctrlKey || e.metaKey) return { action: "follow_up", escAt: null };
	if (e.altKey) return { action: "abort_and_prompt", escAt: null };
	return { action: "steer", escAt: null };
}

export type ParsedInput =
	| { kind: "empty" }
	| { kind: "bash"; command: string; excludeFromContext: boolean }
	| { kind: "slash"; text: string }
	| { kind: "text"; text: string };

export function parseComposerInput(raw: string): ParsedInput {
	const text = raw.trim();
	if (!text) return { kind: "empty" };
	if (text.startsWith("!!")) {
		const command = text.slice(2).trim();
		return command
			? { kind: "bash", command, excludeFromContext: true }
			: { kind: "empty" };
	}
	if (text.startsWith("!")) {
		const command = text.slice(1).trim();
		return command
			? { kind: "bash", command, excludeFromContext: false }
			: { kind: "empty" };
	}
	if (/^\/[^\s/]/.test(text)) return { kind: "slash", text };
	return { kind: "text", text };
}
