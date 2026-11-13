/**
 * 进出场动画用的挂载状态：打开时先挂载（`shown=false` 的初始样式），强制一次样式计算后再置 `shown=true` 触发过渡；
 * 关闭时先置 `shown=false` 播完退出过渡，`ms` 后再卸载。
 *
 * 不用 CSS `@starting-style`：老一些的 Safari 不认它，进场直接跳到终态；它也管不了退出（卸载就没了）。
 * 也不等 requestAnimationFrame：后台或节流中的页面（无头 WebKit 实测 1.6s）rAF 会被推迟，抽屉迟迟不出来。
 * 读一次布局就能让浏览器先按 `shown=false` 算好样式，过渡就有了起点，同一次提交里切到 `shown=true` 也会播放。
 */
import { useEffect, useLayoutEffect, useState } from "react";

export function usePresence(
	open: boolean,
	ms: number,
): { mounted: boolean; shown: boolean } {
	// 首次渲染时已经打开（例如刷新时右栏本来开着）就直接是终态，不播进场
	const [mounted, setMounted] = useState(open);
	const [shown, setShown] = useState(open);
	useLayoutEffect(() => {
		if (open) setMounted(true);
	}, [open]);
	useLayoutEffect(() => {
		if (!open || !mounted || shown) return;
		// 强制样式计算：让 shown=false 的样式先生效，作为过渡起点（退出途中重新打开时从当前位置折返）
		document.body.getBoundingClientRect();
		setShown(true);
	}, [open, mounted, shown]);
	useEffect(() => {
		if (open) return;
		setShown(false);
		const id = setTimeout(() => setMounted(false), ms);
		return () => clearTimeout(id);
	}, [open, ms]);
	return { mounted, shown };
}
