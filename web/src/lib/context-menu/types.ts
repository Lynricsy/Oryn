/**
 * 统一上下文菜单（右键 / 菜单键 / 长按）的共用类型。登记见 `registry.ts`，分发见 `dispatch.ts`，
 * 渲染见 `components/ui/ContextMenu.tsx`。
 */
import type { MenuItem } from "../../components/ui/primitives";

export type ContextTrigger = "mouse" | "keyboard" | "touch";
export interface Anchor {
	left: number;
	top: number;
	right: number;
	bottom: number;
}
export interface MenuGroup {
	key: string;
	items: MenuItem[];
}

/** 触发那一刻的 DOM 选区快照（活的 Range 会被流式重渲染冲掉，不外传）。 */
export interface DomSelection {
	/** 选中的文字；KaTeX 公式换回 TeX 源码 */
	text: string;
	/** 包住整个选区的元素 */
	root: Element;
	startEl: Element;
	endEl: Element;
	/** 各行的视口矩形（宽度 > 0）；引擎没有 Range.getClientRects 时为空数组 */
	rects: DOMRect[];
	/** scope 内、选区之前 / 之后的文字，空白压成单个空格，取紧邻选区的 max 个码点 */
	before(scope: Element, max: number): string;
	after(scope: Element, max: number): string;
}

export type SelectionSource =
	| {
			kind: "message";
			role: "assistant" | "user";
			part: "text" | "thinking";
			turnsAgo: number | null;
			inContext: boolean;
			live: boolean;
			spans: boolean;
	  }
	| {
			kind: "tool";
			toolName: string;
			turnsAgo: number | null;
			inContext: boolean;
			spans: boolean;
	  }
	| { kind: "file"; path: string; startLine?: number; endLine?: number }
	| { kind: "diff"; path: string }
	| { kind: "terminal" }
	| { kind: "side" }
	| { kind: "other" };

export interface SelectionContext {
	/** 回答问题的线程；null = 这里不提供 AI 动作 */
	threadId: string | null;
	text: string;
	before: string;
	after: string;
	source: SelectionSource;
	/** 解释卡的锚点（视口坐标） */
	anchor: Anchor;
	/** 给出时菜单里有「引用回复」 */
	quote?: () => void;
	/** 给出时菜单里有「引用到输入框」，返回要插入草稿的文本 */
	insert?: () => string;
}

export interface ContextRequest {
	trigger: ContextTrigger;
	x: number;
	y: number;
	target: Element;
	selection: DomSelection | null;
	askThreadId: string | null;
}

export type ProviderResult = MenuGroup[] | "suppress" | null;

export interface ContextTargetSpec {
	/** 这个元素自己的菜单组；"suppress" = 不弹菜单也不放行原生菜单 */
	items?(req: ContextRequest): ProviderResult;
	/** 认领落在这个元素里的 DOM 选区；定义了它（即使返回 null）外层就不再认领 */
	selection?(sel: DomSelection, req: ContextRequest): SelectionContext | null;
	/** 这个子树里 AI 动作归哪个线程；null = 明确禁用 */
	askThreadId?: string | null;
}
