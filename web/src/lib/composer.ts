/**
 * 输入区按键与输入解析（计划 H5.4）。发送键是本机偏好（`SendKey`，存 localStorage）：
 *
 * | 按键 | Enter 发送（默认） | ⌘/Ctrl+Enter 发送 |
 * |---|---|---|
 * | 空闲：发送（prompt） | Enter（带 ⌘/Ctrl/Alt 也发送） | ⌘/Ctrl+Enter（带 Shift/Alt 也发送） |
 * | 换行 | Shift+Enter | Enter、Shift+Enter、Alt+Enter |
 * | 忙碌：引导（steer） | Enter | ⌘/Ctrl+Enter |
 * | 忙碌：排队（follow_up） | ⌘/Ctrl+Enter | ⇧⌘/Ctrl+Shift+Enter |
 * | 忙碌：中断并发送（abort_and_prompt） | Alt+Enter | ⌥⌘/Ctrl+Alt+Enter |
 *
 * 即「发送手势」S = Enter 或 ⌘/Ctrl+Enter：空闲时 S 发送；忙碌时 S = 引导，S 加 Alt = 中断并发送，
 * 排队是 S 再加一个修饰键（Enter 模式加 ⌘/Ctrl，⌘Enter 模式加 Shift，因为 ⌘ 已被 S 占用）。
 * - 连按两次 Esc（间隔 ≤ 500ms）：忙碌时 = 中断（abort）；空闲且输入框为空时 = 回退（rewind，同 omp 的 Esc Esc）。
 *   空闲时输入框里有字，Esc 不计入连按（不让一次误触 Esc 毁掉草稿）。
 * - `!cmd` = bash；`!!cmd` = bash 且不进上下文；`$ code` / `$$ code` = Python eval；
 *   `? 问题` = `/btw`；`-> msg` / `=> msg` = follow_up 队列简写；`/` 开头 = 斜杠命令；单独 `.` = 继续（resume）。
 */
import { useSyncExternalStore } from "react";
import { isImeKeyEvent } from "./ime";

/** 发送键：`enter` = Enter 发送、Shift+Enter 换行；`mod` = ⌘/Ctrl+Enter 发送、Enter 换行。 */
export type SendKey = "enter" | "mod";

/** localStorage 键：发送键（本机偏好，不随账户同步）。 */
export const SEND_KEY_STORAGE = "oryn.composer.sendKey";

function readSendKey(): SendKey {
	try {
		return globalThis.localStorage?.getItem(SEND_KEY_STORAGE) === "mod"
			? "mod"
			: "enter";
	} catch {
		return "enter";
	}
}

/** 内存副本：每次 Enter 都要读，不反复访问 localStorage；别的标签页改了经 storage 事件跟上。 */
let sendKey: SendKey | null = null;
const listeners = new Set<() => void>();

export function getSendKey(): SendKey {
	sendKey ??= readSendKey();
	return sendKey;
}

export function setSendKey(next: SendKey): void {
	sendKey = next;
	try {
		globalThis.localStorage?.setItem(SEND_KEY_STORAGE, next);
	} catch {
		// 隐私模式下 localStorage 不可用：只在本次会话生效
	}
	for (const l of listeners) l();
}

function subscribeSendKey(l: () => void): () => void {
	const onStorage = (e: StorageEvent) => {
		if (e.key !== SEND_KEY_STORAGE) return;
		sendKey = readSendKey();
		l();
	};
	listeners.add(l);
	window.addEventListener("storage", onStorage);
	return () => {
		listeners.delete(l);
		window.removeEventListener("storage", onStorage);
	};
}

/** 当前发送键（设置页与快捷键一览用，随设置变化重渲染）。 */
export function useSendKey(): SendKey {
	return useSyncExternalStore(subscribeSendKey, getSendKey, () => "enter");
}

export type ComposerAction =
	| "prompt"
	| "steer"
	| "follow_up"
	| "abort_and_prompt"
	| "abort"
	| "rewind"
	| "newline"
	| "none";

export interface KeyLike {
	key: string;
	shiftKey?: boolean;
	ctrlKey?: boolean;
	metaKey?: boolean;
	altKey?: boolean;
	isComposing?: boolean;
	keyCode?: number;
	which?: number;
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
	/** 输入框没有文字（空闲时 Esc Esc 回退的前提） */
	empty = false,
	/** 发送键；缺省读本机偏好（`getSendKey`） */
	send?: SendKey,
): KeyDecision {
	if (isImeKeyEvent(e)) return { action: "none", escAt: null };
	if (e.key === "Escape") {
		if (!busy && !empty) return { action: "none", escAt: null };
		if (lastEscAt !== null && now - lastEscAt <= DOUBLE_ESC_MS)
			return { action: busy ? "abort" : "rewind", escAt: null };
		return { action: "none", escAt: now };
	}
	if (e.key !== "Enter") return { action: "none", escAt: null };
	const mod = Boolean(e.ctrlKey || e.metaKey);
	if ((send ?? getSendKey()) === "mod") {
		if (!mod) return { action: "newline", escAt: null };
		if (!busy) return { action: "prompt", escAt: null };
		if (e.shiftKey) return { action: "follow_up", escAt: null };
		if (e.altKey) return { action: "abort_and_prompt", escAt: null };
		return { action: "steer", escAt: null };
	}
	if (e.shiftKey) return { action: "newline", escAt: null };
	if (!busy) return { action: "prompt", escAt: null };
	if (mod) return { action: "follow_up", escAt: null };
	if (e.altKey) return { action: "abort_and_prompt", escAt: null };
	return { action: "steer", escAt: null };
}

export type ParsedInput =
	| { kind: "empty" }
	| { kind: "resume" }
	| { kind: "bash"; command: string; excludeFromContext: boolean }
	| { kind: "eval"; code: string; excludeFromContext: boolean }
	| { kind: "btw"; question: string }
	| { kind: "queue"; text: string }
	| { kind: "slash"; text: string }
	| { kind: "text"; text: string };

/**
 * 识别 `$` / `$$` Python 前缀：必须紧跟空白字符（同 Core `pythonCommandPrefixLength`），
 * 避免把 `$HOME`、`$100` 等自然文本误判为 eval。
 */
function pythonPrefixLength(text: string): 0 | 1 | 2 {
	if (text.charCodeAt(0) !== 36 /* $ */) return 0;
	const len = text.charCodeAt(1) === 36 /* $ */ ? 2 : 1;
	const next = text.charCodeAt(len);
	return next === 32 || next === 9 || next === 10 || next === 13 ? len : 0;
}

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
	const pyLen = pythonPrefixLength(text);
	if (pyLen > 0) {
		const code = text.slice(pyLen).trim();
		return code
			? { kind: "eval", code, excludeFromContext: pyLen === 2 }
			: { kind: "empty" };
	}
	if (text[0] === "?" && text[1] !== "?") {
		return { kind: "btw", question: text.slice(1).trim() };
	}
	if (text.startsWith("->") || text.startsWith("=>")) {
		const body = text.slice(2).trim();
		return body ? { kind: "queue", text: body } : { kind: "empty" };
	}
	if (text === ".") return { kind: "resume" };
	if (/^\/[^\s/]/.test(text)) return { kind: "slash", text };
	return { kind: "text", text };
}
