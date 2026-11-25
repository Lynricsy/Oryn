/**
 * 输入法（IME）组字态与回车确认保护。
 *
 * 仅靠 `KeyboardEvent.isComposing` 在中文输入法按 Enter 直接确认输入英文时并不可靠：
 * 1. Safari / WKWebView（macOS / iOS Tauri）会先触发 `compositionend`，再补发一次
 *    `keydown(key: "Enter", isComposing: false, keyCode: 229)`；
 * 2. WebKitGTK（Linux Tauri）与部分 Linux Fcitx5 / IBus 或第三方中文输入法在 `compositionend`
 *    后同一帧或几十毫秒内补发的 `keydown(key: "Enter")` 甚至连 `keyCode` 都是 `13`、`isComposing` 为 `false`；
 * 3. 部分引擎在 `compositionstart` 与 `compositionend` 之间偶发 `isComposing === false`。
 *
 * 本模块提供单事件判断 `isImeKeyEvent` 与组件级 Hook `useImeComposition` 组合兜底。
 */
import { useCallback, useRef, useState } from "react";

/**
 * `compositionend` 之后拦截同次按键补发的幽灵裸 `Enter` 的窗口时长（毫秒）。
 * 足以覆盖同帧（0~16ms）与跨 IPC（~20~50ms）补发；配合 `onKeyUp` 与单次消费，不误伤正常连按发送。
 */
export const POST_COMPOSITION_ENTER_MS = 80;

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
	key: string;
	shiftKey?: boolean;
	ctrlKey?: boolean;
	metaKey?: boolean;
	altKey?: boolean;
	nativeEvent: ImeKeyLike;
	preventDefault: () => void;
}

export interface ImeKeyUpEventLike {
	key: string;
	nativeEvent: ImeKeyLike;
}

export interface ImeCompositionGuard {
	/** React 渲染态：当前是否处于输入法组字中（用于隐藏幽灵字等 UI）。 */
	composing: boolean;
	onCompositionStart: () => void;
	onCompositionEnd: () => void;
	onBlur: () => void;
	onKeyUp: (e: ImeKeyUpEventLike) => void;
	/**
	 * 在 `onKeyDown` 入口调用：返回 `true` 时调用方应立即 `return`，不触发发送/补全/快捷键。
	 */
	shouldIgnoreKeyDown: (e: ImeKeyDownEventLike) => boolean;
}

export function useImeComposition(): ImeCompositionGuard {
	const [composing, setComposing] = useState(false);
	const composingRef = useRef(false);
	const endedAtRef = useRef(0);

	const onCompositionStart = useCallback(() => {
		composingRef.current = true;
		endedAtRef.current = 0;
		setComposing(true);
	}, []);

	const onCompositionEnd = useCallback(() => {
		composingRef.current = false;
		endedAtRef.current = Date.now();
		setComposing(false);
	}, []);

	const onBlur = useCallback(() => {
		composingRef.current = false;
		endedAtRef.current = 0;
		setComposing(false);
	}, []);

	const onKeyUp = useCallback((e: ImeKeyUpEventLike) => {
		if (composingRef.current || isImeKeyEvent(e.nativeEvent)) return;
		// 仅在确认类按键（Enter / 空格）抬起时提前结束保护窗口：
		// 既保证「空格选词松开 → 立刻回车发送」和「Chrome 回车确认松开 → 立刻回车发送」零延迟，
		// 又避免高速输入最后一个字母未松手（rollover）就在 compositionend 后清掉窗口。
		if (e.key === "Enter" || e.key === " ") {
			endedAtRef.current = 0;
		}
	}, []);

	const shouldIgnoreKeyDown = useCallback((e: ImeKeyDownEventLike): boolean => {
		if (composingRef.current || isImeKeyEvent(e.nativeEvent)) {
			// Safari / WKWebView 在 compositionend 后补发的 229 回车：消费窗口但不 preventDefault，让 IME 正常结束
			if (!composingRef.current) endedAtRef.current = 0;
			return true;
		}
		const endedAt = endedAtRef.current;
		if (endedAt !== 0) {
			endedAtRef.current = 0;
			const elapsed = Date.now() - endedAt;
			const bareEnter =
				e.key === "Enter" &&
				!e.shiftKey &&
				!e.ctrlKey &&
				!e.metaKey &&
				!e.altKey;
			if (bareEnter && elapsed >= 0 && elapsed <= POST_COMPOSITION_ENTER_MS) {
				// compositionend 已将确认文本上屏，此处补发的裸 Enter 是引擎残留：同时阻止发送与多余换行
				e.preventDefault();
				return true;
			}
		}
		return false;
	}, []);

	return {
		composing,
		onCompositionStart,
		onCompositionEnd,
		onBlur,
		onKeyUp,
		shouldIgnoreKeyDown,
	};
}
