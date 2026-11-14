/**
 * 侧边栏外壳（M9.6）：顶部是窗口控件行与品牌行（通知铃、收起、新建线程），中间是四个分区的滑动轨道（`SectionPanes`），
 * 底部是底栏（`SectionTabBar`，全端同一个）。显示哪个分区完全由路由决定（`sectionOf`），没有独立的「侧栏视图」状态。
 * `floating`：手机上的悬浮抽屉（双段视口）自己按安全区留了外边距，栏内就不再加 safe-area 内边距（否则刘海下空两次）。
 * `sink`：手机下沉侧栏，底栏用玻璃胶囊（`tone="glass"`）；列侧栏与抽屉用内嵌底色（`inset`）。
 * 桌面 App 无系统标题栏：macOS 红绿灯与品牌行合为一行（品牌在红绿灯右侧，见 `SidebarBrandRow`）；Linux 左侧布局的
 * 三个自绘按钮在顶部单独一行（与品牌行、三个小按钮排不进 256px）。标题行与品牌行的空白处都可拖动窗口，底栏不可。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { uiStore } from "../../state/ui";
import { NotificationBell } from "../notify/NotificationBell";
import { Icon } from "../ui/icons";
import { Button, IconButton } from "../ui/primitives";
import { SectionPanes } from "./SectionPanes";
import { SectionTabBar } from "./SectionTabBar";
import { SidebarBrandRow, SidebarChromeRow } from "./WindowControls";

export function Sidebar({
	floating = false,
	sink = false,
}: {
	floating?: boolean;
	sink?: boolean;
} = {}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	return (
		<nav
			className={`flex h-full w-full flex-col ${floating ? "" : "pt-safe pl-safe"}`}
			aria-label={t("nav.sidebar")}
		>
			{!floating && <SidebarChromeRow />}
			<SidebarBrandRow>
				<NotificationBell />
				<IconButton
					icon="sidebar"
					box="sm"
					label={t("sidebar.collapse")}
					onClick={() => uiStore.getState().toggleSidebar()}
				/>
				<Button
					variant="primary"
					size="sm"
					className="w-7 px-0"
					aria-label={t("sidebar.newThread")}
					title={t("sidebar.newThread")}
					onClick={() => {
						uiStore.getState().setSidebar(false);
						navigate("/");
					}}
				>
					<Icon name="plus" size={15} weight="bold" />
				</Button>
			</SidebarBrandRow>
			<SectionPanes />
			<SectionTabBar tone={sink ? "glass" : "inset"} safeBottom={!floating} />
		</nav>
	);
}
