/**
 * 锚定浮层（下拉菜单、选项弹层、小面板）的统一外壳。
 *
 * - 进顶层（Popover API `popover="manual"`）：不被祖先的 overflow 裁切，也不受任何层叠上下文的 z-index 影响；
 *   DOM 仍留在原处，外点判定（`contains`）、对话框的焦点圈、React 事件冒泡都不变。不支持 Popover API 时退化为 fixed + z-60。
 * - 按锚点（缺省 = DOM 父元素）定位：`side` 一侧放不下且另一侧更宽时翻面；横向夹在视口内；最大高度不超过该侧可用空间。
 * - 玻璃底（`.glass::before`）画在不滚动的外壳上，内容在内层滚动：滚动时模糊底不会跟着滚走。
 *
 * 开合、外点关闭、键盘由调用方负责；本组件只管「放在哪、画成什么」。
 */
import {
	type HTMLAttributes,
	type ReactNode,
	type Ref,
	type RefObject,
	useImperativeHandle,
	useLayoutEffect,
	useRef,
} from "react";

const EDGE = 8;
const SUPPORTS_POPOVER =
	typeof HTMLElement !== "undefined" && "showPopover" in HTMLElement.prototype;

export interface FloatingProps extends HTMLAttributes<HTMLDivElement> {
	/** 优先展开的一侧；放不下时自动翻到另一侧 */
	side?: "top" | "bottom";
	/** 与锚点左缘（start）或右缘（end）对齐，或横向撑满锚点宽度（stretch） */
	align?: "start" | "end" | "stretch";
	/** 与锚点的间距（px） */
	gap?: number;
	/** 锚点；缺省取 DOM 父元素 */
	anchor?: RefObject<Element | null>;
	/** 最大高度上限（px）；实际还会被该侧可用空间压低 */
	maxHeight?: number;
	/** 内层滚动区的类名（列表的内边距写在这里，滚动条才贴边） */
	bodyClassName?: string;
	/** 由调用方自己排版（如头部固定 + 列表滚动的 flex 列），不包内层滚动区 */
	bare?: boolean;
	ref?: Ref<HTMLDivElement | null>;
	children: ReactNode;
}
/** position:fixed 的实际包含块：最近一个让 fixed 后代相对它定位的祖先；没有则为 null（相对视口）。 */
function fixedContainingBlock(el: HTMLElement): HTMLElement | null {
	for (
		let e = el.parentElement;
		e && e !== document.documentElement;
		e = e.parentElement
	) {
		const s = getComputedStyle(e);
		if (
			s.transform !== "none" ||
			s.perspective !== "none" ||
			s.filter !== "none" ||
			(s.backdropFilter && s.backdropFilter !== "none") ||
			/paint|layout|strict|content/.test(s.contain) ||
			/transform|perspective|filter/.test(s.willChange) ||
			(s.containerType && s.containerType !== "normal")
		)
			return e;
	}
	return null;
}

function place(
	el: HTMLDivElement,
	anchorEl: Element,
	side: "top" | "bottom",
	align: "start" | "end" | "stretch",
	gap: number,
	cap: number,
) {
	const a = anchorEl.getBoundingClientRect();
	const vw = document.documentElement.clientWidth;
	const vh = window.innerHeight;
	if (align === "stretch")
		el.style.width = `${Math.min(a.width, vw - 2 * EDGE)}px`;
	el.style.maxHeight = `${cap}px`;
	const natural = el.offsetHeight;
	const below = vh - a.bottom - gap - EDGE;
	const above = a.top - gap - EDGE;
	let s = side;
	if (s === "bottom" && natural > below && above > below) s = "top";
	else if (s === "top" && natural > above && below > above) s = "bottom";
	el.style.maxHeight = `${Math.max(Math.min(cap, s === "bottom" ? below : above), 96)}px`;
	const h = el.offsetHeight;
	const w = el.offsetWidth;
	const left = align === "end" ? a.right - w : a.left;
	// 退化路径（无 Popover API，如 iOS 15/16、macOS 12 的 WebKit）：fixed 元素若有 transform / filter / contain 等祖先，
	// 定位基准就变成那个祖先而不是视口（如手机下沉卡片平移时），换算回它的坐标系
	const cb = SUPPORTS_POPOVER ? null : fixedContainingBlock(el);
	const r = cb?.getBoundingClientRect();
	const dx = cb && r ? r.left + cb.clientLeft - cb.scrollLeft : 0;
	const dy = cb && r ? r.top + cb.clientTop - cb.scrollTop : 0;
	el.style.left = `${Math.round(Math.min(Math.max(left, EDGE), vw - EDGE - w) - dx)}px`;
	el.style.top = `${Math.round((s === "bottom" ? a.bottom + gap : a.top - gap - h) - dy)}px`;
	el.dataset.side = s;
}

export function Floating({
	side = "bottom",
	align = "start",
	gap = 4,
	anchor,
	maxHeight = 320,
	bodyClassName = "",
	bare = false,
	className = "",
	ref,
	children,
	...rest
}: FloatingProps): ReactNode {
	const el = useRef<HTMLDivElement>(null);
	useImperativeHandle(ref, () => el.current as HTMLDivElement);
	useLayoutEffect(() => {
		const node = el.current;
		const anchorEl = anchor?.current ?? node?.parentElement;
		if (!node || !anchorEl) return;
		if (SUPPORTS_POPOVER && !node.matches(":popover-open")) node.showPopover();
		let frame = 0;
		const update = () => {
			cancelAnimationFrame(frame);
			frame = requestAnimationFrame(() =>
				place(node, anchorEl, side, align, gap, maxHeight),
			);
		};
		// 首帧同步定位：显示前就落在正确位置，进入动画不从视口中央飞过来
		place(node, anchorEl, side, align, gap, maxHeight);
		// 没有 ResizeObserver 的环境（jsdom）只跟滚动与窗口尺寸
		const ro =
			typeof ResizeObserver === "undefined" ? null : new ResizeObserver(update);
		ro?.observe(anchorEl);
		ro?.observe(node);
		window.addEventListener("scroll", update, true);
		window.addEventListener("resize", update);
		return () => {
			cancelAnimationFrame(frame);
			ro?.disconnect();
			window.removeEventListener("scroll", update, true);
			window.removeEventListener("resize", update);
			if (SUPPORTS_POPOVER && node.matches(":popover-open")) node.hidePopover();
		};
	}, [anchor, side, align, gap, maxHeight]);
	return (
		<div
			{...rest}
			ref={el}
			popover={SUPPORTS_POPOVER ? "manual" : undefined}
			data-floating=""
			// 顶层元素与 .glass 都自带 position；定位全由 place() 写内联样式
			style={{ position: "fixed", ...(SUPPORTS_POPOVER ? {} : { zIndex: 60 }) }}
			className={`glass pop-in flex flex-col rounded-lg ${className}`}
		>
			{bare ? (
				children
			) : (
				<div
					className={`min-h-0 flex-1 overflow-y-auto overscroll-contain ${bodyClassName}`}
				>
					{children}
				</div>
			)}
		</div>
	);
}
