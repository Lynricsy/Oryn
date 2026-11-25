/**
 * 输入法（IME）组字态与回车确认保护。
 *
 * 仅靠 `KeyboardEvent.isComposing` 在中文输入法按 Enter 直接确认输入英文时并不可靠：
 * 1. Safari / WKWebView（macOS / iOS Tauri）会先触发 `compositionend`，再补发一次
 *    `keydown(key: "Enter", isComposing: false, keyCode: 229)`；
 * 2. 部分引擎或输入法以 `key === "Process"` 或 `keyCode/which === 229` 报告 IME 按键；
 * 3. 部分引擎在 `compositionstart` 与 `compositionend` 之间偶发 `isComposing === false`。
 *
 * 本模块提供单事件判断 `isImeKeyEvent` 与组件级 Hook `useImeComposition`（同步追踪
 * `compositionstart` / `compositionend` / `blur` 状态，并复用 `isImeKeyEvent`）。
 * 故意不使用 `compositionend` 后的盲时间窗，避免误吞数字键/鼠标选词后紧跟的合法 Enter。
 */
import { useCallback, useRef, useState } from "react";

export interface ImeKeyLike {
	key?: string;
	isComposing?: boolean;
	keyCode?: number;
	which?: number;
}

/**
 * 判断单个键盘事件是否正在由输入法（IME）处理。
 * 覆盖标准 `isComposing`、`key === "Process"` 及 WebKit/旧引擎的 `keyCode/which === 229`。
 */
export function isImeKeyEvent(e: ImeKeyLike): boolean {
	return (
		e.isComposing === true ||
		e.key === "Process" ||
		e.keyCode === 229 ||
		e.which === 229
	);
}

export interface ImeKeyDownEventLike {
	nativeEvent: ImeKeyLike;
}

export interface ImeCompositionGuard {
	/** React 渲染态：当前是否处于输入法组字中（用于隐藏幽灵字等 UI）。 */
	composing: boolean;
	onCompositionStart: () => void;
	onCompositionEnd: () => void;
	onBlur: () => void;
	/**
	 * 在 `onKeyDown` 入口调用：返回 `true` 时调用方应立即 `return`（且不调用 `preventDefault`，
	 * 以免阻断输入法自身上屏）。
	 */
	shouldIgnoreKeyDown: (e: ImeKeyDownEventLike) => boolean;
}

export function useImeComposition(): ImeCompositionGuard {
	const [composing, setComposing] = useState(false);
	const composingRef = useRef(false);

	const onCompositionStart = useCallback(() => {
		composingRef.current = true;
		setComposing(true);
	}, []);

	const onCompositionEnd = useCallback(() => {
		composingRef.current = false;
		setComposing(false);
	}, []);

	const onBlur = useCallback(() => {
		composingRef.current = false;
		setComposing(false);
	}, []);

	const shouldIgnoreKeyDown = useCallback((e: ImeKeyDownEventLike): boolean => {
		return composingRef.current || isImeKeyEvent(e.nativeEvent);
	}, []);

	return {
		composing,
		onCompositionStart,
		onCompositionEnd,
		onBlur,
		shouldIgnoreKeyDown,
	};
}
