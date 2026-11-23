/**
 * 启动画面：`index.html` 与 `launcher.html` 里的静态 `#boot-splash`（样式在 index.css 的「启动画面」一节）。
 * 首帧就画出居中的大品牌标（插入符闪烁）与底部点阵扫光，不等脚本、不显示任何「连接中」字样。
 *
 * 交接（`releaseSplash`）：
 * - 有落点（首页输入框上方的品牌标、认证卡片的品牌标）：底色与点阵淡出，大品牌标逐帧追着落点的**实时**位置平移缩放过去，
 *   落点自己的入场位移、数据到达引起的布局移动都跟得上；落地那一帧换成真正的品牌标（飞行期间落点隐藏）。
 * - 没有落点（其它页面、加载失败）或减少动态效果：整体淡出。
 * - 只交接一次：之后的调用都是空操作，所以首页、认证卡片与应用壳可以各自无条件调用，先到者生效。
 *
 * 桌面 / 移动壳的连接页在连接中用 `holdSplash` 重新举起它；壳随后把主窗口导航到远程页面，远程页面首帧就是同一张启动画面，
 * 扫光相位按墙钟对齐（theme-init.js 写 `--boot-t`），换页时接得上。没有 `#boot-splash` 的页面（测试、沙箱页）全部空操作。
 */

/** 品牌标飞向落点的时长。 */
const FLY_MS = 800;
/** 淡出时长，与 CSS 里 `.boot-splash` 各层的 opacity 过渡一致。 */
const FADE_MS = 280;

/** 阶段写在 `#boot-splash[data-phase]`：shown（静态标记的初值）→ flying / fading → gone。 */

let raf = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
/** 正在飞向的落点：被打断时要恢复它的可见性。 */
let landing: HTMLElement | null = null;

function parts(): { root: HTMLElement; mark: HTMLElement } | null {
	const root = document.getElementById("boot-splash");
	const mark = root?.querySelector<HTMLElement>(".boot-splash-mark");
	return root && mark ? { root, mark } : null;
}

function stop(mark: HTMLElement) {
	cancelAnimationFrame(raf);
	clearTimeout(timer);
	if (landing) landing.style.visibility = "";
	landing = null;
	mark.style.transform = "";
}

function fadeOut(root: HTMLElement, mark: HTMLElement) {
	stop(mark);
	root.dataset.phase = "fading";
	timer = setTimeout(() => {
		root.dataset.phase = "gone";
	}, FADE_MS);
}

/** easeOutCubic 的加强版：起步干脆、落地很软。 */
function ease(p: number): number {
	return 1 - (1 - p) ** 4;
}

function fly(root: HTMLElement, mark: HTMLElement, target: HTMLElement) {
	stop(mark);
	const from = mark.getBoundingClientRect();
	const fx = from.left + from.width / 2;
	const fy = from.top + from.height / 2;
	root.dataset.phase = "flying";
	landing = target;
	target.style.visibility = "hidden";
	const t0 = performance.now();
	const step = (now: number) => {
		const to = target.getBoundingClientRect();
		// 落点被卸载或不再占位（换了路由、布局把它藏了）：不再追，直接淡出
		if (!target.isConnected || to.width === 0) {
			fadeOut(root, mark);
			return;
		}
		const p = Math.min(1, (now - t0) / FLY_MS);
		const e = ease(p);
		const dx = (to.left + to.width / 2 - fx) * e;
		const dy = (to.top + to.height / 2 - fy) * e;
		const s = 1 + (to.width / from.width - 1) * e;
		mark.style.transform = `translate(${dx}px, ${dy}px) scale(${s})`;
		if (p < 1) {
			raf = requestAnimationFrame(step);
			return;
		}
		// 落地：真正的品牌标与飞行中的那个此刻重合，同一帧互换
		target.style.visibility = "";
		landing = null;
		root.dataset.phase = "gone";
		mark.style.transform = "";
	};
	raf = requestAnimationFrame(step);
}

/** 页面就绪：交出启动画面。`target` 是品牌标的落点（与启动画面同一个 32×32 viewBox 的 BrandMark）。 */
export function releaseSplash(target?: HTMLElement | null): void {
	const p = parts();
	if (p?.root.dataset.phase !== "shown") return;
	const reduced = window.matchMedia?.(
		"(prefers-reduced-motion: reduce)",
	).matches;
	if (target && !reduced && target.getBoundingClientRect().width > 0)
		fly(p.root, p.mark, target);
	else fadeOut(p.root, p.mark);
}

/** 重新举起启动画面（连接页进入连接中）。已经举着时不动，正在交接时打断并淡回。 */
export function holdSplash(): void {
	const p = parts();
	if (!p || p.root.dataset.phase === "shown") return;
	stop(p.mark);
	// gone 时整层 display: none，动画已停；重新显示时动画从头开始，按此刻的墙钟重写延迟，扫光相位仍与远程页面对齐。
	// 交接途中（flying / fading）动画还在跑，改延迟反而会让它跳相位，不动
	if (p.root.dataset.phase === "gone")
		document.documentElement.style.setProperty(
			"--boot-t",
			`${-(Date.now() % 12000)}ms`,
		);
	p.root.dataset.phase = "shown";
}
