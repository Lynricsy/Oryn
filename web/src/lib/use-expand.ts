/**
 * 展开体的开合过渡（工具卡、思考块）。动画期间只动合成层属性：主线程不重排、不重渲染、不重绘下方的行。
 * - 排版一次到位：打开时展开体立即按完整高度排版；收起时排版保持到动画结束，再卸载展开体。
 * - 视觉用 FLIP 补回差值：记尚未露出的高度为 s（打开 Δ→0，收起 0→Δ，Δ 是展开体的排版高度），
 *   外框（frame，默认是展开体本身）用 clip-path 从底边裁掉 s，外框之后的内容（滚动容器以内逐层的后续兄弟，
 *   包括虚拟列表的后续行）平移 -s，展开体透明度随之渐变。
 * - 收起时滚动容器若已接近底部，卸载后浏览器会把 scrollTop 夹回 k 像素：动画里改由外框及其之前的内容下移 k·s/Δ、
 *   之后的内容只上移其余部分，终点与夹回后的位置重合（贴底时读起来是底边不动、上方内容落下来）。
 * 以前直接对 height 做动画：每帧都要重排、触发虚拟列表测高与整段转录重渲染、重绘下方所有行，长转录里明显掉帧。
 * 首次挂载就是打开的不做动画（由外层入场负责）；外框不在可视区内（屏幕外的卡片自动收起）、减少动效或没有
 * matchMedia 时直接切换。中途反向从当前的 s 接着走。
 */
import { type RefObject, useLayoutEffect, useRef, useState } from "react";
import { motionAllowed } from "./motion";

const OPEN_MS = 260;
const CLOSE_MS = 220;
const EASE = "cubic-bezier(0.2, 0.8, 0.2, 1)";

interface Run {
	anims: Animation[];
	/** 读当前进度用的动画（外框的裁切）。 */
	driver: Animation;
	from: number;
	to: number;
}

function cancelRun(run: { current: Run | null }): void {
	for (const a of run.current?.anims ?? []) a.cancel();
	run.current = null;
}

/** 当前尚未露出的高度：进度已含缓动（getComputedTiming 的 progress 是缓动后的值）。 */
function hiddenNow(run: Run): number {
	const p = run.driver.effect?.getComputedTiming().progress;
	return p == null ? run.to : run.from + (run.to - run.from) * p;
}

function scrollerOf(el: HTMLElement): HTMLElement | null {
	for (let p = el.parentElement; p; p = p.parentElement) {
		const y = getComputedStyle(p).overflowY;
		if (y === "auto" || y === "scroll") return p;
	}
	return null;
}

/**
 * 外框之前 / 之后的内容：从外框逐层向上直到滚动容器（没有则到 body），每层的前后兄弟。
 * 只取可视区附近（上下各留 margin）的：屏幕外的跟着排版直接到位，不必提升成合成层。
 */
function neighbours(
	frame: HTMLElement,
	scroller: HTMLElement | null,
	view: { top: number; bottom: number },
	margin: number,
): { before: HTMLElement[]; after: HTMLElement[] } {
	const before: HTMLElement[] = [];
	const after: HTMLElement[] = [];
	const near = (el: Element): el is HTMLElement => {
		if (!(el instanceof HTMLElement)) return false;
		const r = el.getBoundingClientRect();
		return r.bottom > view.top - margin && r.top < view.bottom + margin;
	};
	for (
		let node: HTMLElement = frame;
		node.parentElement &&
		node.parentElement !== scroller &&
		node !== document.body;
		node = node.parentElement
	) {
		for (let s = node.previousElementSibling; s; s = s.previousElementSibling)
			if (near(s)) before.push(s);
		for (let s = node.nextElementSibling; s; s = s.nextElementSibling)
			if (near(s)) after.push(s);
	}
	return { before, after };
}

export function useExpand<T extends HTMLElement>(
	open: boolean,
	frameRef?: RefObject<HTMLElement | null>,
): { shown: boolean; ref: RefObject<T | null> } {
	const ref = useRef<T | null>(null);
	const run = useRef<Run | null>(null);
	const [prevOpen, setPrevOpen] = useState(open);
	// 收起动画进行中：展开体仍挂着，动画结束才卸载
	const [closing, setClosing] = useState(false);
	if (open !== prevOpen) {
		setPrevOpen(open);
		setClosing(!open && motionAllowed());
	}
	const shown = open || closing;

	// 收起动画停在终点（fill: forwards）；展开体卸载的这次提交里撤掉，同一帧排版已经到位
	useLayoutEffect(() => {
		if (!shown) cancelRun(run);
	}, [shown]);
	useLayoutEffect(() => () => cancelRun(run), []);

	// 上一次生效的开合态：只在真正切换时动画（首次挂载与 StrictMode 的重复执行都跳过）
	const applied = useRef(open);
	useLayoutEffect(() => {
		if (applied.current === open) return;
		applied.current = open;
		const prev = run.current;
		const resumeFrom = prev ? hiddenNow(prev) : undefined;
		cancelRun(run);
		const body = ref.current;
		const frame = frameRef?.current ?? body;
		const delta = body?.offsetHeight ?? 0;
		const scroller = frame ? scrollerOf(frame) : null;
		const view = scroller?.getBoundingClientRect() ?? {
			top: 0,
			bottom: window.innerHeight,
		};
		const rect = frame?.getBoundingClientRect();
		if (
			!body ||
			!frame ||
			!rect ||
			!motionAllowed() ||
			delta < 1 ||
			rect.bottom <= view.top ||
			rect.top >= view.bottom
		) {
			setClosing(false);
			return;
		}
		const from = Math.min(delta, resumeFrom ?? (open ? delta : 0));
		const to = open ? 0 : delta;
		// 收起后浏览器夹回的滚动量（只有收起会让内容变矮；内容本来就不满一屏时不夹）
		let k = 0;
		if (!open && scroller) {
			const max = scroller.scrollHeight - scroller.clientHeight;
			k = Math.min(
				delta,
				Math.max(0, scroller.scrollTop - Math.max(0, max - delta)),
			);
		}
		const lift = (s: number) => (k * s) / delta;
		const sink = (s: number) => s - lift(s);
		const timing: KeyframeAnimationOptions = {
			duration: open ? OPEN_MS : CLOSE_MS,
			easing: EASE,
			fill: open ? "none" : "forwards",
		};
		// 平移叠加在元素原有的 translate 上（入场动画、虚拟行定位都不受影响）
		const shift = (el: HTMLElement, a: number, b: number) =>
			el.animate([{ translate: `0 ${a}px` }, { translate: `0 ${b}px` }], {
				...timing,
				composite: "add",
			});
		const radius = getComputedStyle(frame).borderRadius || "0px";
		const clip = (s: number) => `inset(0 0 ${s}px 0 round ${radius})`;
		const driver = frame.animate(
			[{ clipPath: clip(from) }, { clipPath: clip(to) }],
			timing,
		);
		const anims = [
			driver,
			body.animate(
				[{ opacity: 1 - from / delta }, { opacity: 1 - to / delta }],
				timing,
			),
		];
		const { before, after } = neighbours(frame, scroller, view, delta);
		if (k > 0) {
			anims.push(shift(frame, lift(from), lift(to)));
			for (const el of before) anims.push(shift(el, lift(from), lift(to)));
		}
		if (k < delta)
			for (const el of after) anims.push(shift(el, -sink(from), -sink(to)));
		const current: Run = { anims, driver, from, to };
		run.current = current;
		driver.onfinish = () => {
			if (run.current !== current) return;
			if (open) cancelRun(run);
			else setClosing(false);
		};
	}, [open, frameRef]);

	return { shown, ref };
}
