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
	return (
		<header
			data-window-drag=""
			className="veil-host relative isolate @container/header flex h-12 shrink-0 items-center gap-1.5 px-2 sm:px-3"
		>
			<div aria-hidden className="veil" style={{ height: layout.veil }}>
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
