/**
 * iPhone Duo 竖栏（`lib/rail.ts`，壳报告系统把工具栏竖排在一侧时由 AppShell 渲染）：固定在壳给的竖栏矩形里，不随侧栏开合、
 * 卡片移动。上面一根玻璃胶囊是页头的按钮：开关侧栏（取代各页页头里带 `data-sidebar-toggle` 的那个，竖栏在时它们被隐藏）
 * 与操作槽（当前页头经 `InRail` 送进来的操作，如线程页的树、信息面板、更多）；下面是竖排的分区底栏。
 * 页头里的侧栏开关一隐藏，原生页头量不到侧栏钮就不会接管（`useSlotFrame` 为 null），页头整条退回网页画。
 */
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { setRailSlot, useRail, useRailSlot } from "../../lib/rail";
import { uiStore } from "../../state/ui";
import { Icon } from "../ui/icons";
import { SectionTabBar } from "./SectionTabBar";

export function Rail(): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			data-testid="rail"
			className="pointer-events-none fixed top-[var(--rail-y)] left-[var(--rail-x)] z-40 flex h-[var(--rail-h)] w-[var(--rail-w)] flex-col items-center gap-2 overflow-y-auto pt-1"
		>
			<div className="glass pointer-events-auto flex w-[52px] shrink-0 flex-col items-center gap-1 rounded-full p-1">
				<button
					type="button"
					data-testid="rail-sidebar"
					aria-label={t("nav.sidebar")}
					title={t("nav.sidebar")}
					className="inline-flex h-11 w-11 items-center justify-center rounded-full text-muted transition-colors duration-150 hover:bg-hover hover:text-fg"
					onClick={() => uiStore.getState().toggleSidebar()}
				>
					<Icon name="sidebar" size={19} />
				</button>
				<div
					ref={setRailSlot}
					data-testid="rail-actions"
					className="flex flex-col items-center gap-1 empty:hidden"
				/>
			</div>
			<SectionTabBar tone="rail" />
		</div>
	);
}

/** 竖栏在时把页头操作送进竖栏的操作槽，否则原地渲染。进出竖栏（折叠 / 展开）时这些按钮重挂载，开着的菜单随之关闭。 */
export function InRail({ children }: { children: ReactNode }): ReactNode {
	const rail = useRail();
	const slot = useRailSlot();
	return rail && slot ? createPortal(children, slot) : children;
}
