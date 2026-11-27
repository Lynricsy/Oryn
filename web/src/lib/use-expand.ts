/**
 * 展开体的高度过渡（工具卡、思考块）：打开时从当前高度长到内容高度，收起时缩回 0 后再卸载。
 * 用 WAAPI 驱动，结束后清掉内联样式，高度回到 auto（内容之后再变高不受影响）；中途反向从当前高度接着走。
 * 首次挂载就是打开的（历史卡片、虚拟列表重新挂载、当前一批调用默认展开）不做动画，由外层的入场动画负责。
 * 减少动效或没有 matchMedia 时直接切换。
 */
import { type RefObject, useLayoutEffect, useRef, useState } from "react";
import { motionAllowed } from "./motion";

const OPEN_MS = 260;
const CLOSE_MS = 200;
const EASE = "cubic-bezier(0.2, 0.8, 0.2, 1)";

export function useExpand<T extends HTMLElement>(
	open: boolean,
): { shown: boolean; ref: RefObject<T | null> } {
	const ref = useRef<T | null>(null);
	const anim = useRef<Animation | null>(null);
	const [prevOpen, setPrevOpen] = useState(open);
	// 收起动画进行中：展开体仍挂着，动画结束才卸载
	const [closing, setClosing] = useState(false);
	if (open !== prevOpen) {
		setPrevOpen(open);
		setClosing(!open && motionAllowed());
	}

	// 上一次生效的开合态：只在真正切换时动画（首次挂载与 StrictMode 的重复执行都跳过）
	const applied = useRef(open);
	useLayoutEffect(() => {
		if (applied.current === open) return;
		applied.current = open;
		const el = ref.current;
		// 没有进行中的动画：打开从 0 起（展开体刚挂上），收起从完整高度起；
		// 动画中途反向：从此刻的高度与透明度接着走
		const running = anim.current;
		let from = open || !el ? 0 : el.getBoundingClientRect().height;
		let fromOpacity = open ? 0 : 1;
		if (running && el) {
			from = el.getBoundingClientRect().height;
			fromOpacity = Number(getComputedStyle(el).opacity);
		}
		running?.cancel();
		anim.current = null;
		if (el) el.style.overflow = "";
		if (!el || !motionAllowed()) {
			setClosing(false);
			return;
		}
		const to = open ? el.scrollHeight : 0;
		if (Math.abs(to - from) < 1) {
			setClosing(false);
			return;
		}
		el.style.overflow = "hidden";
		const a = el.animate(
			[
				{ height: `${from}px`, opacity: fromOpacity },
				{ height: `${to}px`, opacity: open ? 1 : 0 },
			],
			{ duration: open ? OPEN_MS : CLOSE_MS, easing: EASE },
		);
		anim.current = a;
		a.onfinish = () => {
			if (anim.current !== a) return;
			anim.current = null;
			el.style.overflow = "";
			if (!open) setClosing(false);
		};
	}, [open]);

	return { shown: open || closing, ref };
}
