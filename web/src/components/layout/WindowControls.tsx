/**
 * 自绘标题栏的窗口控件（无系统标题栏的桌面 App，布局与状态见 `lib/window-chrome.ts`）。
 * 放在哪：侧栏展开时，macOS 原生红绿灯与侧栏品牌行合为一行（`SidebarBrandRow`），Linux 左侧布局的自绘按钮在侧栏顶部
 * 单独一行（`SidebarChromeRow`）；侧栏没占左上角时左侧控件归最左侧页面表面的页头（`ChromeLead`）。右侧自绘按钮归最右侧
 * 表面的顶部（右面板展开时在面板标签条右侧、替掉面板的 ×，否则是线程页头 / 首页、设置页头的右端），没有页头的整页卡片
 * （登录、配对、连接页、加载中）用贴顶的 `ChromeBar`。
 * 按钮是低调的圆角 ghost：悬停出底色，关闭悬停变 err 色；窗口失焦时整组变淡；全屏时隐藏（macOS 全屏时红绿灯也收起，留白归 0）。
 * 浏览器与移动壳里 `useWindowChrome()` 为 null，这里全部渲染为空，留白不变。
 */
import { type ReactNode, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	useWindowChrome,
	type WindowAction,
	type WindowButton,
} from "../../lib/window-chrome";
import { BrandMark } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";

export type ChromeSide = "left" | "right";

/** 两侧此刻各由谁占着：`native` = 原生红绿灯，`custom` = 页面自绘；全屏或不在桌面壳里时都为 null。 */
export function useChromeSides(): {
	left: "native" | "custom" | null;
	right: "custom" | null;
} {
	const v = useWindowChrome();
	if (!v || v.flags.fullscreen) return { left: null, right: null };
	const c = v.chrome.controls;
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
 * 元素在窗口里的位置（`[left, top]`，CSS px）：`active` 时测量，窗口缩放、侧栏 / 面板列 260ms 的宽度过渡结束后重测
 * （过渡结束后位置才定下来）。
 */
function useWindowPos(
	ref: { current: HTMLElement | null },
	active: boolean,
): [number, number] {
	const [pos, setPos] = useState<[number, number]>([0, 0]);
	useLayoutEffect(() => {
		const el = ref.current;
		if (!active || !el) return;
		const update = () => {
			const r = el.getBoundingClientRect();
			setPos((p) => (p[0] === r.left && p[1] === r.top ? p : [r.left, r.top]));
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

/**
 * 页头最左端：原生红绿灯 → 让出到 `inset.left` 的空白（按自身在窗口里的横坐标算）；左侧自绘 → 按钮组。
 * 页头本身是拖动区，这块空白也能拖。
 */
export function ChromeLead(): ReactNode {
	const v = useWindowChrome();
	const ref = useRef<HTMLSpanElement>(null);
	const native =
		!!v && !v.flags.fullscreen && v.chrome.controls === "native-left";
	const [x] = useWindowPos(ref, native);
	if (!v) return null;
	if (native)
		return (
			<span
				ref={ref}
				aria-hidden
				data-chrome-lead="native"
				className="shrink-0"
				style={{ width: Math.max(0, v.chrome.inset.left - x) }}
			/>
		);
	return <WindowControls side="left" className="mr-1" />;
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
 * 左端让出到红绿灯右侧（`inset.left`），按钮间距收紧；字标只在品牌区够宽（≥ 6rem）时显示，256px 宽的侧栏只留品牌标。
 * 全屏时红绿灯收起：品牌区用 transform 平移到红绿灯原来的位置（260ms、`--ease`），退出全屏时平移回来；
 * 进入全屏延后 180ms 再走（壳报告全屏时系统的全屏过渡刚开始，红绿灯随标题栏淡出，先让它们走），退出不延后
 * （退出过渡结束时红绿灯才重新出现，260ms 早已让出空间）。减少动效时直接切换。
 * 其它环境（浏览器、移动壳、Windows / Linux）与以前完全相同：56px 行高、字标常在。
 */
export function SidebarBrandRow({
	children,
}: {
	children: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const v = useWindowChrome();
	const native = v?.chrome.controls === "native-left";
	const fullscreen = native && !!v?.flags.fullscreen;
	const row = useRef<HTMLDivElement>(null);
	const spacer = useRef<HTMLSpanElement>(null);
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
						className={`absolute top-1/2 h-4 w-px -translate-y-1/2 bg-border transition-opacity duration-150 motion-reduce:transition-none ${fullscreen ? "opacity-0 delay-0" : "opacity-100 delay-[260ms]"}`}
						style={{ left: reserve + 2 }}
					/>
				</span>
			)}
			<span
				data-brand-shift={
					native ? (fullscreen ? "fullscreen" : "lights") : undefined
				}
				className={`@container flex min-w-0 flex-1 items-center ${native ? "gap-1.5" : "gap-2"} ${native ? `transition-transform duration-[260ms] ease-[var(--ease)] motion-reduce:transition-none ${fullscreen ? "delay-[180ms]" : "delay-0"}` : ""}`}
				style={
					fullscreen ? { transform: `translateX(${-shiftBy}px)` } : undefined
				}
			>
				<BrandMark size={24} />
				{/* 不加 truncate：overflow 裁切会切掉 leading-none 下 y 的降部 */}
				<span className="wordmark hidden min-w-0 text-[24px] leading-none @[5.5rem]:inline">
					{t("app.name")}
				</span>
			</span>
			{children}
		</div>
	);
}

/** 没有页头的整页卡片（登录 / 配对 / 连接页、加载中）：贴顶一条透明标题行，整条可拖动，两端放自绘按钮。 */
export function ChromeBar(): ReactNode {
	const v = useWindowChrome();
	if (!v || v.flags.fullscreen || v.chrome.controls === "none") return null;
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
