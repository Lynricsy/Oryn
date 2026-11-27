import { createContext, useContext } from "react";
import type { ViewAnswer } from "../../lib/view/answer";

/** 一个声明式视图里所有块共享的状态（由 `UiView` 提供）。 */
export interface ViewContextValue {
	toolCallId: string;
	/** 已作答（含在途）的块：块路径 → 作答。 */
	answers: ReadonlyMap<string, ViewAnswer>;
	/** 控件可用；false 时全部禁用。没有 `onAnswer` 时也视为不可用。 */
	interactive: boolean;
	/** 参数仍在流式：chart 与 svg 只显示骨架。 */
	streaming: boolean;
	onAnswer?: (answer: ViewAnswer, text: string) => Promise<void>;
}

export const ViewContext = createContext<ViewContextValue>({
	toolCallId: "",
	answers: new Map(),
	interactive: false,
	streaming: false,
});

export function useView(): ViewContextValue {
	return useContext(ViewContext);
}
