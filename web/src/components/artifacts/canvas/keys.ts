/**
 * 画布编辑器的快捷键（计划 M14 6.3）。无修饰键、焦点不在输入框时生效；`Mod` 是 `Ctrl` 或 `⌘`。
 */

export type Tool =
	| "move"
	| "hand"
	| "frame"
	| "text"
	| "sticky"
	| "pen"
	| "shape"
	| "comment";

export type ZoomTarget = "fit" | "selection" | "actual" | "in" | "out";

export type KeyAction =
	| { type: "tool"; tool: Tool }
	| { type: "delete" }
	| { type: "nudge"; dx: number; dy: number }
	| { type: "undo" }
	| { type: "redo" }
	| { type: "duplicate" }
	| { type: "escape" }
	| { type: "zoom"; to: ZoomTarget }
	| { type: "focus" };

/** 工具的单键快捷键。 */
export const TOOL_KEYS: Record<Tool, string> = {
	move: "V",
	hand: "H",
	frame: "F",
	text: "T",
	sticky: "S",
	pen: "P",
	shape: "R",
	comment: "C",
};

const BY_KEY = new Map(
	Object.entries(TOOL_KEYS).map(([tool, key]) => [
		key.toLowerCase(),
		tool as Tool,
	]),
);

/** 键盘事件里用到的字段。 */
export interface KeyInput {
	key: string;
	code: string;
	shiftKey: boolean;
	altKey: boolean;
	ctrlKey: boolean;
	metaKey: boolean;
}

/** 焦点在输入框、可编辑区域里（快捷键不归画布）。 */
export function isTypingTarget(target: EventTarget | null): boolean {
	const el = target as HTMLElement | null;
	if (!el || typeof el.closest !== "function") return false;
	return !!el.closest("input, textarea, select, [contenteditable]");
}

/** 把按键翻译成画布动作；不是画布的快捷键时 null。 */
export function keyAction(e: KeyInput): KeyAction | null {
	const mod = e.ctrlKey || e.metaKey;
	if (e.altKey) return null;
	if (mod) {
		const k = e.key.toLowerCase();
		if (k === "z") return { type: e.shiftKey ? "redo" : "undo" };
		if (k === "y" && !e.shiftKey) return { type: "redo" };
		if (k === "d" && !e.shiftKey) return { type: "duplicate" };
		return null;
	}
	if (e.shiftKey) {
		if (e.code === "Digit1") return { type: "zoom", to: "fit" };
		if (e.code === "Digit2") return { type: "zoom", to: "selection" };
		if (e.code === "Digit0") return { type: "zoom", to: "actual" };
	}
	switch (e.key) {
		case "Delete":
		case "Backspace":
			return { type: "delete" };
		case "Escape":
			return { type: "escape" };
		case "Enter":
			return { type: "focus" };
		case "+":
		case "=":
			return { type: "zoom", to: "in" };
		case "-":
		case "_":
			return { type: "zoom", to: "out" };
		case "ArrowLeft":
		case "ArrowRight":
		case "ArrowUp":
		case "ArrowDown": {
			const step = e.shiftKey ? 10 : 1;
			const dx =
				e.key === "ArrowLeft" ? -step : e.key === "ArrowRight" ? step : 0;
			const dy = e.key === "ArrowUp" ? -step : e.key === "ArrowDown" ? step : 0;
			return { type: "nudge", dx, dy };
		}
	}
	if (e.shiftKey) return null;
	const tool = BY_KEY.get(e.key.toLowerCase());
	return tool ? { type: "tool", tool } : null;
}
