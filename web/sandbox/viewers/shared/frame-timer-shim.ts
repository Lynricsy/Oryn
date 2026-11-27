// 渲染管线（控制面的 headless Chromium，`--virtual-time-budget` + `--run-all-compositor-stages-before-draw`）里的
// 两个坑，只在渲染视图（`?view=thumbnail|print`，只有渲染管线用）里处理。必须在任何渲染库求值之前装：各 Office
// 查看器入口第一行 import 本模块。
//
// 1. requestAnimationFrame 永远不触发（BeginFrame 由虚拟时间控制器驱动）：换成 16ms 的 setTimeout。
// 2. 虚拟时间只等主线程上的网络请求与任务：worker 里的模块下载、wasm 编译与解析都不算，主线程一空闲虚拟时间就直接跳到
//    预算末尾，截图时还停在「正在加载」。渲染完成（根元素 data-ready）之前，主线程每 4ms（虚拟时间）做一小段计算，
//    让虚拟时间按真实耗时一点点推进，worker 有真实时间干完活。预算耗尽或渲染完成即停。

import { viewModeOf } from "./config";

type FrameHost = Pick<
	Window,
	| "requestAnimationFrame"
	| "cancelAnimationFrame"
	| "setTimeout"
	| "clearTimeout"
> & { performance: Pick<Performance, "now"> };

/** 把 win 的 rAF / cAF 换成基于 setTimeout 的实现。 */
export function installFrameTimerShim(win: FrameHost): void {
	win.requestAnimationFrame = (cb) =>
		win.setTimeout(() => cb(win.performance.now()), 16);
	win.cancelAnimationFrame = (id) => win.clearTimeout(id);
}

/** 每一步的计算量：本机约几毫秒，足够让 worker 在虚拟时间预算内拿到数秒真实时间。 */
const SPIN_STEPS = 4_000_000;
/** 两步之间的虚拟时间间隔（ms）。 */
const SPIN_EVERY = 4;

/** 在 done() 为真之前，每隔 SPIN_EVERY 在主线程上做一小段计算；返回停止函数。 */
export function keepMainThreadBusy(
	win: Pick<Window, "setTimeout" | "clearTimeout">,
	done: () => boolean,
	steps = SPIN_STEPS,
): () => void {
	let timer = 0;
	let sink = 0;
	const tick = () => {
		if (done()) return;
		// 计算结果写回 sink，引擎不会把循环当作死代码删掉
		for (let i = 0; i < steps; i++) sink = (sink + i) % 1_000_003;
		timer = win.setTimeout(tick, SPIN_EVERY);
	};
	timer = win.setTimeout(tick, 0);
	return () => win.clearTimeout(timer);
}

if (typeof window !== "undefined" && viewModeOf(window.location.search)) {
	installFrameTimerShim(window);
	keepMainThreadBusy(
		window,
		() => document.documentElement.dataset.ready === "true",
	);
}
