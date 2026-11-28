/**
 * 侧边栏外壳（M9.6）：顶部是窗口控件行与品牌行（通知铃、收起、新建线程），其下是搜索入口，中间是四个分区的滑动轨道
 * （`SectionPanes`），底部是底栏（`SectionTabBar`，全端同一个）。显示哪个分区完全由路由决定（`sectionOf`），没有独立的「侧栏视图」状态。
 * 搜索入口就是命令面板（线程、Artifacts、对话内容、设置与操作都在里面搜）：放在外壳里、四个分区共用，手机上没有 ⌘K 时也有入口；
 * 以前品牌行的放大镜与线程分区的「搜索线程」过滤框功能重复，且放大镜挤掉了字标，两者合并成这一个。
 * `floating`：手机上的悬浮抽屉（双段视口）自己按安全区留了外边距，栏内就不再加 safe-area 内边距（否则刘海下空两次）。
 * `sink`：手机下沉侧栏，底栏用玻璃胶囊（`tone="glass"`）；列侧栏与抽屉用 `inset`。底栏悬浮在分区内容之上，
 * 外壳按它的高度设 `--tabbar-h`，各分区滚动区据此在底部留白。iPhone Duo 竖栏在时（`useRail`）底栏挪进竖栏（AppShell），侧栏里不放。
 * 桌面 App 无系统标题栏：macOS 红绿灯与品牌行合为一行（品牌在红绿灯右侧，见 `SidebarBrandRow`）；Linux 左侧布局的
 * 三个自绘按钮在顶部单独一行（与品牌行、三个小按钮排不进 256px）。标题行与品牌行的空白处都可拖动窗口，底栏不可。
 */
import { type CSSProperties, memo, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { useRail } from "../../lib/rail";
import { uiStore } from "../../state/ui";
import { NotificationBell } from "../notify/NotificationBell";
import { openPalette } from "../palette/palette-state";
import { Icon } from "../ui/icons";
import { Button, IconButton } from "../ui/primitives";
import { SectionPanes } from "./SectionPanes";
import { MAC, SectionTabBar, tabBarHeight } from "./SectionTabBar";
import { SidebarBrandRow, SidebarChromeRow } from "./WindowControls";

/**
 * memo：壳在侧栏开合、断点变化时重渲染，侧栏的 props 不变，不必把整棵线程列表跟着重算一遍
 * （开合那一帧的重渲染会让 260ms 的过渡头几帧卡住）。
 */
export const Sidebar = memo(function Sidebar({
	floating = false,
	sink = false,
}: {
	floating?: boolean;
	sink?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const tone = sink ? "glass" : "inset";
	const safeBottom = !floating;
	const rail = useRail() !== null;
	return (
		<nav
			className={`relative flex h-full w-full flex-col ${floating ? "" : "pt-safe pl-safe"}`}
			style={
				{
					"--tabbar-h": rail ? "0px" : tabBarHeight(tone, safeBottom),
				} as CSSProperties
			}
			aria-label={t("nav.sidebar")}
		>
			{!floating && <SidebarChromeRow />}
			<SidebarBrandRow compact={sink}>
				<NotificationBell />
				<IconButton
					icon="sidebar"
					box="sm"
					label={t("sidebar.collapse")}
					data-sidebar-toggle=""
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
			<SidebarSearch />
			<SectionPanes />
			{!rail && <SectionTabBar tone={tone} safeBottom={safeBottom} />}
		</nav>
	);
});

/** 搜索入口：外观沿用原来的过滤框（填充底、放大镜、占位文字），点开命令面板；有键盘的设备在右端提示快捷键。 */
function SidebarSearch(): ReactNode {
	const { t } = useTranslation();
	return (
		<div className="shrink-0 px-2.5 pb-2">
			<button
				type="button"
				data-testid="sidebar-search"
				aria-label={t("palette.open")}
				aria-keyshortcuts="Control+K Meta+K"
				onClick={() => openPalette()}
				className="flex h-8 w-full items-center gap-2 rounded-lg bg-[var(--sb-field)] pr-1.5 pl-2.5 text-faint text-sm transition-colors duration-150 hover:text-muted"
			>
				<Icon name="search" size={14} aria-hidden className="shrink-0" />
				<span className="min-w-0 flex-1 truncate text-left">
					{t("sidebar.search")}
				</span>
				<kbd className="shrink-0 rounded-sm px-1 font-sans text-[11px] text-faint pointer-coarse:hidden">
					{MAC ? "⌘K" : "Ctrl K"}
				</kbd>
			</button>
		</div>
	);
}
