/**
 * 自绘标题栏的窗口控件（无系统标题栏的桌面 App，布局与状态见 `lib/window-chrome.ts`）。
 * 放在哪：侧栏展开时，macOS 原生红绿灯与侧栏品牌行合为一行（`SidebarBrandRow`），Linux 左侧布局的自绘按钮在侧栏顶部
 * 单独一行（`SidebarChromeRow`）；侧栏没占左上角时左侧控件归最左侧页面表面的页头（`ChromeLead`）。右侧自绘按钮归最右侧
 * 表面的顶部（右面板展开时在面板标签条右侧、替掉面板的 ×，否则是线程页头 / 首页、设置页头的右端），没有页头的整页卡片
 * （登录、配对、连接页、加载中）用贴顶的 `ChromeBar`。
 * 按钮是低调的圆角 ghost：悬停出底色，关闭悬停变 err 色；窗口失焦时整组变淡；全屏时隐藏。
 * macOS 全屏时壳藏起原生红绿灯（`chrome.fullscreenLights`），页面在原位置自绘一组（`FullscreenLights`），指针移到左上角才出现；
 * 页头仍按红绿灯让位，侧栏品牌区平时左移占住那块、红绿灯出现时让回。
 * 浏览器与移动壳里 `useWindowChrome()` 为 null，这里全部渲染为空，留白不变。
 */
import {
	type ReactNode,
	type RefObject,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import { useTranslation } from "react-i18next";
import {
	useWindowChrome,
	type WindowAction,
	type WindowButton,
	type WindowChromeView,
} from "../../lib/window-chrome";
import { useUi } from "../../state/ui";
import { BrandMark } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";

export type ChromeSide = "left" | "right";

/**
 * 两侧此刻各由谁占着：`native` = 红绿灯（原生，或 macOS 全屏时页面自绘的那组），`custom` = 页面自绘按钮；
 * 不在桌面壳里时都为 null，全屏时除自绘红绿灯外也为 null。
 */
export function useChromeSides(): {
	left: "native" | "custom" | null;
	right: "custom" | null;
} {
	const v = useWindowChrome();
	if (!v) return { left: null, right: null };
	const c = v.chrome.controls;
	if (v.flags.fullscreen)
		return {
			left: c === "native-left" && v.chrome.fullscreenLights ? "native" : null,
			right: null,
		};
	return {
		left:
			c === "native-left" ? "native" : c === "custom-left" ? "custom" : null,
		right: c === "custom-right" ? "custom" : null,
	};
}

const ACTION = {
	minimize: "minimize",
	maximize: "toggleMaximize",
	close: "close",
} as const satisfies Record<WindowButton, WindowAction>;

/** 一组自绘按钮；不归这一侧（或全屏、不在桌面壳里）时不渲染。 */
export function WindowControls({
	side,
	className = "",
}: {
	side: ChromeSide;
	className?: string;
}): ReactNode {
	const { t } = useTranslation();
	const v = useWindowChrome();
	if (!v || v.flags.fullscreen || v.chrome.controls !== `custom-${side}`)
		return null;
	const { maximized, focused } = v.flags;
	return (
		<div
			data-window-controls={side}
			data-focused={focused ? "" : undefined}
			className={`flex shrink-0 items-center gap-0.5 transition-opacity duration-150 ${focused ? "" : "opacity-50 hover:opacity-100"} ${className}`}
		>
			{v.chrome.buttons.map((b) => {
				const label =
					b === "minimize"
						? t("window.minimize")
						: b === "close"
							? t("window.close")
							: maximized
								? t("window.restore")
								: t("window.maximize");
				const icon: IconName =
					b === "minimize"
						? "windowMinimize"
						: b === "close"
							? "close"
							: maximized
								? "windowRestore"
								: "windowMaximize";
				return (
					<button
						key={b}
						type="button"
						aria-label={label}
						title={label}
						data-window-control={b}
						className={`inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted transition-[background-color,color,scale] duration-150 ease-out active:scale-95 ${b === "close" ? "hover:bg-err/12 hover:text-err" : "hover:bg-hover hover:text-fg"}`}
						onClick={() => v.act(ACTION[b])}
					>
						<Icon name={icon} size={b === "close" ? 15 : 14} />
					</button>
				);
			})}
		</div>
	);
}

/**
 * 元素在窗口里的布局位置（`[left, top]`，CSS px）：沿 offsetParent 链累加 offsetLeft / offsetTop 与各级的边框宽，
 * 不含 transform / translate。侧栏收起时只是整块平移出屏，按 getBoundingClientRect 量会得到负的横坐标，
 * 让位宽度随之变成三百多像素、把品牌行推回屏幕里（收起后左上角漏出 logo 与第二个侧栏按钮）；按布局位置量，
 * 开合途中与收起后都等于侧栏展开时的位置。`active` 时测量，窗口缩放、侧栏 / 面板列 260ms 的宽度过渡结束后重测。
 */
function layoutPos(el: HTMLElement): [number, number] {
	let x = 0;
	let y = 0;
	for (let n: HTMLElement | null = el; n; ) {
		x += n.offsetLeft;
		y += n.offsetTop;
		const parent: Element | null = n.offsetParent;
		if (!(parent instanceof HTMLElement)) break;
		x += parent.clientLeft;
		y += parent.clientTop;
		n = parent;
	}
	return [x, y];
}

function useWindowPos(
	ref: { current: HTMLElement | null },
	active: boolean,
): [number, number] {
	const [pos, setPos] = useState<[number, number]>([0, 0]);
	useLayoutEffect(() => {
		const el = ref.current;
		if (!active || !el) return;
		const update = () => {
			const [x, y] = layoutPos(el);
			setPos((p) => (p[0] === x && p[1] === y ? p : [x, y]));
		};
		update();
		window.addEventListener("resize", update);
		document.addEventListener("transitionend", update, true);
		return () => {
			window.removeEventListener("resize", update);
			document.removeEventListener("transitionend", update, true);
		};
	}, [ref, active]);
	return pos;
}

/** 会挪动页头位置的过渡属性：侧栏 / 面板列的宽度与外边距、玻璃栏的平移。颜色、透明度、缩放不影响位置，不跟。 */
const LAYOUT_TRANSITION =
	/^(width|margin|translate|transform|left|right|inset|flex)/;
/** 一次布局过渡开始后逐帧跟多久（ms）：侧栏 / 面板开合是 260ms，留足余量；transitionend 时会再算一次。 */
const FOLLOW_MS = 600;

/** 按页头（`el` 的父元素）此刻的布局位置写让位宽度；页头不在顶部标题行里时不让位。 */
function placeLead(el: HTMLElement, inset: { left: number; top: number }) {
	const host = el.parentElement;
	if (!host) return;
	const pad = Number.parseFloat(getComputedStyle(host).paddingLeft) || 0;
	const [x, y] = layoutPos(host);
	const width =
		y < inset.top
			? Math.max(0, Math.round(inset.left - (x + host.clientLeft + pad)))
			: 0;
	el.style.width = `${width}px`;
	el.style.display = width > 0 ? "" : "none";
}

/**
 * 红绿灯让位：宽度 = inset.left − 所在页头内容区左缘在窗口里的横坐标；不需要让位时（页头在红绿灯右边，或不在顶部标题行里）
 * 不显示，也不占页头的 gap。侧栏 / 面板开合时页头跟着卡片滑动，过渡期间逐帧重算并直接写到元素上（不经 React 重渲染）：
 * 页头内容始终停在红绿灯右侧，不会先滑到红绿灯底下、过渡结束时再跳过去。量的是页头（父元素），不是让位块自己——它隐藏时量不到位置。
 * 过渡事件不是唯一触发：减少动效时过渡时长为 0，一个过渡事件都没有，所以 `layout`（侧栏 / 面板状态与 `show`）每次提交后
 * 也在 layout effect 里同步重算一次，绘制前就落位。
 */
function useLightsLead(
	ref: { current: HTMLElement | null },
	inset: { left: number; top: number },
	active: boolean,
	layout: string,
): void {
	const { left, top } = inset;
	// 过渡跟随：transitionrun 起逐帧重算，resize / transitionend / transitioncancel 各算一次
	useLayoutEffect(() => {
		const el = ref.current;
		if (!active || !el) return;
		const apply = () => placeLead(el, { left, top });
		let raf = 0;
		let until = 0;
		const tick = () => {
			apply();
			raf = performance.now() < until ? requestAnimationFrame(tick) : 0;
		};
		const follow = (e: Event) => {
			if (
				e instanceof TransitionEvent &&
				!LAYOUT_TRANSITION.test(e.propertyName)
			)
				return;
			if (e.type !== "transitionrun") {
				apply();
				return;
			}
			until = performance.now() + FOLLOW_MS;
			if (!raf) raf = requestAnimationFrame(tick);
		};
		const events = ["transitionrun", "transitionend", "transitioncancel"];
		window.addEventListener("resize", apply);
		for (const type of events) document.addEventListener(type, follow, true);
		return () => {
			cancelAnimationFrame(raf);
			window.removeEventListener("resize", apply);
			for (const type of events)
				document.removeEventListener(type, follow, true);
		};
	}, [ref, left, top, active]);
	// 每次布局状态提交后同步落位（含挂载）：没有过渡（减少动效）时这是唯一的触发
	// biome-ignore lint/correctness/useExhaustiveDependencies: layout 变化 = 页头可能已挪位，需要重新量
	useLayoutEffect(() => {
		const el = ref.current;
		if (active && el) placeLead(el, { left, top });
	}, [ref, left, top, active, layout]);
}

/**
 * 页头最左端：原生红绿灯 → 让出到 `inset.left` 的空白（按页头在窗口里的横坐标算，见 `useLightsLead`），页头总是挂着它，
 * 用不着时自己隐藏——侧栏开合途中页头从红绿灯底下滑过时也能一路让开；左侧自绘 → `show`（页头此刻是最左侧表面）时放按钮组。
 * 页头本身是拖动区，这块空白也能拖。
 */
export function ChromeLead({ show }: { show: boolean }): ReactNode {
	const v = useWindowChrome();
	const ref = useRef<HTMLSpanElement>(null);
	// 全屏时壳藏起原生红绿灯、页面在原位置自绘（指针移上去才出现），页头照样让位，红绿灯不会盖住页头的按钮
	const native =
		!!v &&
		v.chrome.controls === "native-left" &&
		(!v.flags.fullscreen || v.chrome.fullscreenLights);
	// 会挪动页头横坐标的状态：侧栏展开 / 收起 / 抽屉、面板开合 / 最大化 / 宽度，以及页头自己是不是最左侧表面
	const layout = useUi(
		(s) =>
			`${s.sidebarCollapsed}|${s.sidebarOpen}|${s.panelOpen}|${s.panelMax}|${s.panelWidth}|${show}`,
	);
	useLightsLead(ref, v?.chrome.inset ?? { left: 0, top: 0 }, native, layout);
	if (!v) return null;
	if (native)
		return (
			<span
				ref={ref}
				aria-hidden
				data-chrome-lead="native"
				className="shrink-0"
			/>
		);
	return show ? <WindowControls side="left" className="mr-1" /> : null;
}

/** 侧栏顶部的标题行：只给左侧自绘按钮（Linux 左侧布局，三个 32px 按钮与品牌行排不进 256px 宽的一行），整行可拖动。 */
export function SidebarChromeRow(): ReactNode {
	const { left } = useChromeSides();
	if (left !== "custom") return null;
	return (
		<div
			data-window-drag=""
			data-chrome-row="sidebar"
			className="flex h-12 shrink-0 items-center pl-2.5"
		>
			<WindowControls side="left" />
		</div>
	);
}

/** macOS 红绿灯的竖直中线（窗口坐标）：chrome.rs 的 TRAFFIC_LIGHT_Y 24 + 按钮框高 16 的一半。 */
const LIGHTS_MID = 32;
/** 红绿灯模式下品牌行高：与页头同为 48px，侧栏玻璃离窗口上缘 8px 时中线正好落在红绿灯中线上。 */
const LIGHTS_ROW = 48;
/**
 * 红绿灯与品牌标之间分隔线区的宽度：红绿灯右缘在 76，inset.left = 84；
 * 分隔线放在 86（离红绿灯 10px），品牌标从 97 开始（离分隔线 10px），两侧等距。
 */
const LIGHTS_DIVIDER = 13;

/**
 * 侧栏顶部的品牌行（品牌标 + 字标 + `children` 里的小按钮），整行可拖动。
 * macOS 原生红绿灯（`native-left`）时与红绿灯合为一行：行高 48、竖直中线对齐红绿灯（侧栏贴着窗口上缘时补上内边距），
 * 左端让出到红绿灯右侧（`inset.left`），按钮间距收紧；字标只在品牌区放得下它的实际宽度时显示（`useWordmarkFits`），
 * 放不下（红绿灯更宽的 macOS 26、窄侧栏）只留品牌标。
 * 全屏时红绿灯收起：品牌区用 transform 平移到红绿灯原来的位置（260ms、`--ease`），退出全屏时平移回来；
 * 进入全屏延后 180ms 再走（壳报告全屏时系统的全屏过渡刚开始，红绿灯随标题栏淡出，先让它们走），退出不延后
 * （退出过渡结束时红绿灯才重新出现，260ms 早已让出空间）。全屏时指针移到左上角唤出页面自绘的红绿灯（`FullscreenLights`）
 * 期间，品牌区同样平移回来让出位置。减少动效时直接切换。
 * 其它环境（浏览器、移动壳、Windows / Linux）与以前完全相同：56px 行高，字标同样按实际宽度决定显隐。
 */
export function SidebarBrandRow({
	children,
}: {
	children: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const v = useWindowChrome();
	const native = v?.chrome.controls === "native-left";
	const lightsShown = useLightsShown();
	// 品牌区此刻占着红绿灯的位置：全屏、且没有唤出自绘红绿灯
	const shifted =
		native &&
		!!v?.flags.fullscreen &&
		!(v.chrome.fullscreenLights && lightsShown);
	const row = useRef<HTMLDivElement>(null);
	const spacer = useRef<HTMLSpanElement>(null);
	const brand = useRef<HTMLSpanElement>(null);
	const word = useRef<HTMLSpanElement>(null);
	const wordFits = useWordmarkFits(brand, word);
	const [, rowTop] = useWindowPos(row, native);
	const [spacerX] = useWindowPos(spacer, native);
	const reserve = native
		? Math.max(0, (v?.chrome.inset.left ?? 0) - spacerX)
		: 0;
	// 品牌区整体左移的距离：红绿灯让位 + 分隔线区
	const shiftBy = reserve + LIGHTS_DIVIDER;
	const padTop = native ? Math.max(0, LIGHTS_MID - LIGHTS_ROW / 2 - rowTop) : 0;
	return (
		<div
			ref={row}
			data-window-drag=""
			data-brand-row={native ? "lights" : undefined}
			className={`flex shrink-0 items-center pr-2.5 pl-4 ${native ? "gap-1" : "h-14 gap-2"}`}
			style={
				native ? { height: LIGHTS_ROW + padTop, paddingTop: padTop } : undefined
			}
		>
			{native && (
				// 抵掉与品牌区之间的 gap-1：红绿灯让位到 inset.left，之后是分隔线区，品牌标紧随其后
				<span
					ref={spacer}
					aria-hidden
					data-lights-reserve=""
					className="relative -mr-1 shrink-0 self-stretch"
					style={{ width: shiftBy }}
				>
					{/* 红绿灯与品牌之间的发丝分隔线（macOS 常见做法）；全屏红绿灯消失时随之淡出，退出全屏等品牌区回位后再淡入 */}
					<span
						data-lights-divider=""
						className={`absolute top-1/2 h-4 w-px -translate-y-1/2 bg-border transition-opacity duration-150 motion-reduce:transition-none ${shifted ? "opacity-0 delay-0" : "opacity-100 delay-[260ms]"}`}
						style={{ left: reserve + 2 }}
					/>
				</span>
			)}
			<span
				ref={brand}
				data-brand-shift={
					native ? (shifted ? "fullscreen" : "lights") : undefined
				}
				className={`flex min-w-0 flex-1 items-center ${native ? "gap-1.5" : "gap-2"} ${native ? `transition-transform duration-[260ms] ease-[var(--ease)] motion-reduce:transition-none ${shifted ? "delay-[180ms]" : "delay-0"}` : ""}`}
				style={shifted ? { transform: `translateX(${-shiftBy}px)` } : undefined}
			>
				<BrandMark size={24} />
				{/* 不加 truncate：overflow 裁切会切掉 leading-none 下 y 的降部。shrink-0：量到的总是完整宽度。
				    放不下时脱离文档流并隐藏（不占位、不进无障碍树），仍留在 DOM 里供下次量宽 */}
				<span
					ref={word}
					data-wordmark={wordFits ? "shown" : "hidden"}
					className={`wordmark shrink-0 whitespace-nowrap text-[24px] leading-none ${wordFits ? "" : "invisible absolute"}`}
				>
					{t("app.name")}
				</span>
			</span>
			{children}
		</div>
	);
}

/**
 * 字标放得下吗：品牌区（flex-1，宽度不随内容变）≥ 品牌标 + gap + 字标实际宽度。
 * 不写死阈值：红绿灯让位随系统版本变（macOS 26 的工具栏红绿灯更宽），字体加载、语言切换也会改字标宽度。
 * 同时观察品牌区与字标：侧栏宽度、红绿灯让位变化改前者，字体换完改后者。用 getBoundingClientRect 取小数宽度，
 * 不让 offsetWidth 的取整在临界宽度上误判；transform 平移不改宽度。
 */
function useWordmarkFits(
	box: RefObject<HTMLElement | null>,
	word: RefObject<HTMLElement | null>,
): boolean {
	const [fits, setFits] = useState(true);
	useLayoutEffect(() => {
		const b = box.current;
		const w = word.current;
		const mark = b?.firstElementChild;
		if (!b || !w || !mark) return;
		const check = () => {
			const gap = Number.parseFloat(getComputedStyle(b).columnGap) || 0;
			const need =
				mark.getBoundingClientRect().width +
				gap +
				w.getBoundingClientRect().width;
			setFits(need <= b.getBoundingClientRect().width);
		};
		check();
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(check);
		ro.observe(b);
		ro.observe(w);
		return () => ro.disconnect();
	}, [box, word]);
	return fits;
}

/** 没有页头的整页卡片（登录 / 配对 / 连接页、加载中）：贴顶一条透明标题行，整条可拖动，两端放自绘按钮；全屏时只剩自绘红绿灯。 */
export function ChromeBar(): ReactNode {
	const v = useWindowChrome();
	if (!v || v.chrome.controls === "none") return null;
	if (v.flags.fullscreen) return <FullscreenLights />;
	return (
		<div
			data-window-drag=""
			data-chrome-row="bar"
			className="absolute inset-x-0 top-0 z-10 flex h-12 items-center px-2"
		>
			<WindowControls side="left" />
			<span className="min-w-0 flex-1" />
			<WindowControls side="right" />
		</div>
	);
}

/** 自绘红绿灯的关闭按钮左缘：与原生一致（chrome.rs 的 TRAFFIC_LIGHT_X）。 */
const LIGHTS_X = 22;
/** 按钮框边长：14px 一个、间距 6（中心距 20，与原生相同）。 */
const LIGHT_BOX = 14;
/** 指针离开后多久收起（ms）：在按钮之间滑过、稍微出界时不闪。 */
const LIGHTS_LINGER = 300;

// 自绘红绿灯此刻是否唤出：侧栏品牌行据此让位（各处只有一组红绿灯，模块级状态即可）
let lightsShown = false;
const lightsSubs = new Set<() => void>();
function setLightsShown(on: boolean) {
	if (on === lightsShown) return;
	lightsShown = on;
	for (const cb of [...lightsSubs]) cb();
}
function subscribeLights(cb: () => void): () => void {
	lightsSubs.add(cb);
	return () => lightsSubs.delete(cb);
}
function useLightsShown(): boolean {
	return useSyncExternalStore(
		subscribeLights,
		() => lightsShown,
		() => false,
	);
}

/** 全屏、且壳把红绿灯交给页面画（`chrome.fullscreenLights`）时返回视图，否则 null。 */
function useFullscreenLights(): WindowChromeView | null {
	const v = useWindowChrome();
	return v?.flags.fullscreen &&
		v.chrome.controls === "native-left" &&
		v.chrome.fullscreenLights
		? v
		: null;
}

/**
 * macOS 全屏时页面自绘的红绿灯：位置与原生红绿灯相同，平时隐藏；指针进入左上角（红绿灯让位宽 × 标题行）或键盘聚焦时淡入，
 * 离开 300ms 后淡出。关闭 = 原生关闭；最小化在全屏里不可用（与系统一致，置灰）；绿灯退出全屏。
 * 热区下面只有页头的让位空白或侧栏品牌区（都不可交互），不会挡住按钮。
 */
export function FullscreenLights(): ReactNode {
	const { t } = useTranslation();
	const v = useFullscreenLights();
	const shown = useLightsShown();
	const timer = useRef(0);
	const active = !!v;
	// 退出全屏或卸载时收起：下次进全屏从隐藏开始
	// biome-ignore lint/correctness/useExhaustiveDependencies: active 变化 = 进出全屏，需要复位
	useEffect(
		() => () => {
			window.clearTimeout(timer.current);
			setLightsShown(false);
		},
		[active],
	);
	if (!v) return null;
	const show = () => {
		window.clearTimeout(timer.current);
		setLightsShown(true);
	};
	const hide = () => {
		window.clearTimeout(timer.current);
		timer.current = window.setTimeout(
			() => setLightsShown(false),
			LIGHTS_LINGER,
		);
	};
	return (
		// biome-ignore lint/a11y/noStaticElementInteractions: 悬停热区只负责唤出；按钮本身可聚焦，聚焦同样唤出
		<div
			data-fullscreen-lights={shown ? "shown" : "hidden"}
			className="fixed top-0 left-0 z-50"
			style={{
				width: v.chrome.inset.left,
				height: LIGHTS_MID + LIGHTS_ROW / 2,
			}}
			onPointerEnter={show}
			onPointerLeave={hide}
			onFocus={show}
			onBlur={hide}
		>
			<div
				className={`group/lights absolute flex gap-1.5 transition-opacity duration-150 motion-reduce:transition-none ${shown ? "opacity-100" : "pointer-events-none opacity-0"}`}
				style={{ left: LIGHTS_X, top: LIGHTS_MID - LIGHT_BOX / 2 }}
			>
				<Light
					kind="close"
					label={t("window.close")}
					onClick={() => v.act("close")}
				/>
				<Light kind="minimize" label={t("window.minimize")} />
				<Light
					kind="fullscreen"
					label={t("window.exitFullscreen")}
					onClick={() => v.act("toggleFullscreen")}
				/>
			</div>
		</div>
	);
}

/** 一颗红绿灯：12px 圆点，整组悬停时显示符号；没有 `onClick` 即不可用（灰）。 */
function Light({
	kind,
	label,
	onClick,
}: {
	kind: "close" | "minimize" | "fullscreen";
	label: string;
	onClick?: () => void;
}): ReactNode {
	const color = !onClick
		? "bg-fg/20"
		: kind === "close"
			? "bg-[#ff5f57]"
			: "bg-[#28c840]";
	return (
		<button
			type="button"
			aria-label={label}
			title={label}
			disabled={!onClick}
			data-fullscreen-light={kind}
			className="inline-flex shrink-0 items-center justify-center rounded-full focus-visible:outline-2 focus-visible:outline-accent"
			style={{ width: LIGHT_BOX, height: LIGHT_BOX }}
			onClick={onClick}
		>
			<span
				className={`flex h-3 w-3 items-center justify-center rounded-full text-black/55 ring-1 ring-black/10 ring-inset ${color}`}
			>
				{onClick && (
					<svg
						viewBox="0 0 12 12"
						aria-hidden
						className="h-3 w-3 opacity-0 transition-opacity duration-100 group-hover/lights:opacity-100"
					>
						{kind === "close" ? (
							<path
								d="M4 4l4 4M8 4l-4 4"
								stroke="currentColor"
								strokeWidth="1.2"
								strokeLinecap="round"
							/>
						) : (
							<path
								d="M5.6 5.6V2.6L2.6 5.6ZM6.4 6.4V9.4L9.4 6.4Z"
								fill="currentColor"
							/>
						)}
					</svg>
				)}
			</span>
		</button>
	);
}
