/**
 * 进出场动画用的挂载状态：打开时先挂载（`shown=false` 的初始样式），下一帧再置 `shown=true` 触发过渡；
 * 关闭时先置 `shown=false` 播完退出过渡，`ms` 后再卸载。
 *
 * 不用 CSS `@starting-style`：老一些的 Safari 不认它，进场直接跳到终态；它也管不了退出（卸载就没了）。
 */
import { useEffect, useState } from "react";

export function usePresence(
	open: boolean,
	ms: number,
): { mounted: boolean; shown: boolean } {
	// 首次渲染时已经打开（例如刷新时右栏本来开着）就直接是终态，不播进场
	const [mounted, setMounted] = useState(open);
	const [shown, setShown] = useState(open);
	useEffect(() => {
		if (open) {
			setMounted(true);
			// 两帧：第一帧让浏览器按 shown=false 排版，第二帧再切到 shown=true，过渡才有起点
			let second = 0;
			const first = requestAnimationFrame(() => {
				second = requestAnimationFrame(() => setShown(true));
			});
			return () => {
				cancelAnimationFrame(first);
				cancelAnimationFrame(second);
			};
		}
		setShown(false);
		const id = setTimeout(() => setMounted(false), ms);
		return () => clearTimeout(id);
	}, [open, ms]);
	return { mounted, shown };
}
