/**
 * 协调对话页（Space 页、助手页）的页头：与线程页头同一块渐进模糊纱与同样的两端（侧栏开关、窗口控件），
 * 中间放调用方给的标题段，右端放调用方的动作。由 `ThreadScreen` 的 `header` 回调按布局参数渲染。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { ScreenHeaderLayout } from "../../pages/ThreadPage";
import { uiStore, useUi } from "../../state/ui";
import { ChromeLead, WindowControls } from "../layout/WindowControls";
import { IconButton } from "../ui/primitives";

export function CoordinatorHeader({
	layout,
	title,
	actions,
}: {
	layout: ScreenHeaderLayout;
	title: ReactNode;
	actions?: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const sidebarCollapsed = useUi((s) => s.sidebarCollapsed);
	const safeTop = layout.safeTop ?? 0;
	return (
		<header
			data-window-drag=""
			className="veil-host relative isolate @container/header flex shrink-0 items-center gap-1.5 px-2 sm:px-3"
			// 铺到状态栏下面时整体往下推一个安全区（48px 一行仍在安全区下方），纱从屏幕顶部盖起
			style={{ height: 48 + safeTop, paddingTop: safeTop }}
		>
			{/*
			 * 铺到状态栏下面时纱从屏幕顶部盖起、换成轻模糊（data-band）。这里的标题与按钮不是玻璃胶囊，
			 * 纱仍要盖住页头这一行，否则标题直接压在清楚的对话文字上；手机线程页的玻璃胶囊才让纱只盖状态栏一带。
			 */}
			<div
				aria-hidden
				className="veil"
				data-band={safeTop > 0 ? "" : undefined}
				style={{ height: layout.veil + safeTop }}
			>
				<i />
				<i />
				<i />
				<i />
				<i />
				<i />
			</div>
			{layout.chromeLeft && <ChromeLead />}
			{(layout.sidebarToggle || sidebarCollapsed) && (
				<IconButton
					icon={layout.sidebarToggle ? "menu" : "sidebar"}
					label={t("nav.sidebar")}
					onClick={() => uiStore.getState().toggleSidebar()}
				/>
			)}
			<div className="flex min-w-0 flex-1 items-center gap-1.5">{title}</div>
			{actions}
			{layout.chromeRight && <WindowControls side="right" className="ml-1" />}
		</header>
	);
}
