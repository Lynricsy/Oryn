/**
 * 自绘标题栏的窗口控件（无系统标题栏的桌面 App，布局与状态见 `lib/window-chrome.ts`）。
 * 放在哪：左侧控件（macOS 原生红绿灯 / Linux 左侧布局的自绘按钮）在侧栏展开时归侧栏顶部的标题行（`SidebarChromeRow`），
 * 否则归最左侧页面表面的页头（`ChromeLead`）；右侧自绘按钮归最右侧表面的顶部（右面板展开时是面板纸面顶部的标题行，
 * 否则是线程页头 / 首页、设置页头的右端），没有页头的整页卡片（登录、配对、连接页、加载中）用贴顶的 `ChromeBar`。
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
 * 页头最左端：原生红绿灯 → 让出到 `inset.left` 的空白（按自身在窗口里的横坐标算，窗口缩放与侧栏收起动画后重算）；
 * 左侧自绘 → 按钮组。页头本身是拖动区，这块空白也能拖。
 */
export function ChromeLead(): ReactNode {
	const v = useWindowChrome();
	const ref = useRef<HTMLSpanElement>(null);
	const [x, setX] = useState(0);
	const native =
		!!v && !v.flags.fullscreen && v.chrome.controls === "native-left";
	useLayoutEffect(() => {
		const el = ref.current;
		if (!native || !el) return;
		const update = () => setX(el.getBoundingClientRect().left);
		update();
		window.addEventListener("resize", update);
		// 侧栏 / 面板列的收起与展开是 260ms 的宽度过渡：结束后页头位置才定下来
		document.addEventListener("transitionend", update, true);
		return () => {
			window.removeEventListener("resize", update);
			document.removeEventListener("transitionend", update, true);
		};
	}, [native]);
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

/** 侧栏顶部的标题行：左侧控件归侧栏时出现（原生红绿灯由系统画在这一行里，自绘按钮排在左端），整行可拖动。 */
export function SidebarChromeRow(): ReactNode {
	const { left } = useChromeSides();
	if (!left) return null;
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
