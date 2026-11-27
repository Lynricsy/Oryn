/**
 * 流式正文的平滑显现：增量按帧成批到达（一帧几十字、偶尔停顿后一大段），直接渲染读起来是一顿一顿地跳。
 * 这里让显示长度按指数逼近到达的长度：积压越多走得越快，约 3τ 追平，任何时刻都不会落后太多；
 * 积压很少时按最低速度匀速走完。流式结束、减少动效或文本不是在原文末尾续写时直接显示全文。
 */
import { useEffect, useRef, useState } from "react";
import { motionAllowed } from "../../lib/motion";

/** 追赶的时间常数（ms）。 */
const TAU = 110;
/** 最低显现速度（字 / 秒）：积压只剩几个字时不至于一个字一个字地挪。 */
const MIN_CPS = 90;

/** 显现长度的下一步：不越过目标，不切开 UTF-16 代理对。 */
export function nextShown(text: string, shown: number, dt: number): number {
	const backlog = text.length - shown;
	if (backlog <= 0) return text.length;
	const step = Math.max(
		backlog * (1 - Math.exp(-dt / TAU)),
		(MIN_CPS * dt) / 1000,
	);
	let next = Math.min(text.length, shown + Math.ceil(step));
	const code = text.charCodeAt(next - 1);
	if (code >= 0xd800 && code <= 0xdbff && next < text.length) next++;
	return next;
}

export function useSmoothText(text: string, streaming: boolean): string {
	// 挂载时已有的文字直接显示（虚拟列表重新挂载、打开线程时正在流式）
	const [shown, setShown] = useState(text.length);
	const shownRef = useRef(shown);
	shownRef.current = shown;
	const textRef = useRef(text);
	// 文本被替换（不是在原文末尾续写）：直接显示新文本，不回放
	const prefixed = text.startsWith(textRef.current.slice(0, shown));
	textRef.current = text;
	const smooth = streaming && prefixed && motionAllowed();
	// 不平滑时显示长度跟着全文走，之后续写从这里接着显现
	if (!smooth && shown !== text.length) setShown(text.length);
	const behind = smooth && shown < text.length;

	// 每次续写都重新接上逐帧循环（循环追平即停，不空转）
	// biome-ignore lint/correctness/useExhaustiveDependencies: text 变化即需要重新接上循环（目标经 textRef 读取）
	useEffect(() => {
		if (!behind) return;
		let raf = 0;
		let last = performance.now();
		const tick = (now: number) => {
			const dt = Math.min(64, Math.max(0, now - last));
			last = now;
			const target = textRef.current;
			const next = nextShown(target, shownRef.current, dt);
			shownRef.current = next;
			setShown(next);
			if (next < target.length) raf = requestAnimationFrame(tick);
		};
		raf = requestAnimationFrame(tick);
		return () => cancelAnimationFrame(raf);
	}, [behind, text]);

	return behind ? text.slice(0, shown) : text;
}
