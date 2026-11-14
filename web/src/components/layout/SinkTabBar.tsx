/**
 * 手机底部胶囊（M9，参照 Amp）：线程 / 全局助手 / 项目 / 更多。挂在 `SinkLayout` 的 `footer` 槽——属于下沉侧栏层，
 * 平时被对话卡片盖住，只在打开侧栏时露出；不放在卡片上，也不常驻屏幕底部。
 * 线程 / 项目 / 更多切换侧栏主体视图（`uiStore.sidebarView`）；全局助手跳到 `/assistant` 并收起侧栏。
 * 选中项是主题色文字 + 一块滑动的底板（四等分，按序号定位，180ms；减少动效时由全局规则压到 1ms）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useLocation, useNavigate } from "react-router";
import { useConnection, useIndex } from "../../state/stores";
import { type SidebarView, uiStore, useUi } from "../../state/ui";
import { StatusDot } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";

type TabKey = SidebarView | "assistant";

const TABS: { key: TabKey; icon: IconName }[] = [
	{ key: "threads", icon: "chats" },
	{ key: "assistant", icon: "sparkle" },
	{ key: "projects", icon: "folder" },
	{ key: "more", icon: "more" },
];

/** 当前选中项：项目 / 更多视图优先，其次是助手路由，否则线程。 */
export function selectedTab(view: SidebarView, pathname: string): TabKey {
	if (view !== "threads") return view;
	return pathname.startsWith("/assistant") ? "assistant" : "threads";
}

export function SinkTabBar(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const { pathname } = useLocation();
	const view = useUi((s) => s.sidebarView);
	const conn = useConnection((s) => s.status);
	const assistantUnread = useIndex((s) =>
		Object.values(s.threads).some(
			(th) => th.kind === "assistant" && th.unread && !th.archivedAt,
		),
	);
	const selected = selectedTab(view, pathname);
	const index = TABS.findIndex((tab) => tab.key === selected);
	const press = (key: TabKey) => {
		const ui = uiStore.getState();
		if (key === "assistant") {
			ui.setSidebar(false);
			navigate("/assistant");
		} else ui.setSidebarView(key);
	};
	return (
		<nav
			aria-label={t("tabBar.label")}
			data-testid="sink-tabbar"
			className="shrink-0 px-3 pt-1.5 pb-[max(env(safe-area-inset-bottom),0.5rem)] pl-[max(env(safe-area-inset-left),0.75rem)]"
		>
			<div className="glass h-[52px] rounded-full p-1">
				<div className="relative grid h-full grid-cols-4">
					<span
						aria-hidden
						data-testid="sink-tabbar-plate"
						className="absolute inset-y-0 rounded-full bg-[var(--sb-sel)] transition-[left] duration-[180ms] ease-out"
						style={{ left: `${index * 25}%`, width: "25%" }}
					/>
					{TABS.map((tab) => {
						const on = tab.key === selected;
						return (
							<button
								key={tab.key}
								type="button"
								data-tab={tab.key}
								aria-current={on ? "true" : undefined}
								onClick={() => press(tab.key)}
								className={`relative z-10 flex min-h-11 flex-col items-center justify-center gap-0.5 rounded-full text-2xs leading-none transition-colors duration-150 ${on ? "font-semibold text-accent-ink" : "font-medium text-muted hover:text-fg"}`}
							>
								<span className="relative inline-flex">
									<Icon
										name={tab.icon}
										size={20}
										weight={on ? "fill" : "regular"}
										aria-hidden
									/>
									{tab.key === "assistant" && assistantUnread && (
										<span
											role="img"
											data-testid="tabbar-unread"
											aria-label={t("sidebar.unread")}
											className="absolute -top-0.5 -right-1 h-[7px] w-[7px] rounded-full bg-accent ring-2 ring-[var(--panel)]"
										/>
									)}
									{tab.key === "more" && conn !== "open" && (
										<span
											data-testid="tabbar-conn"
											className="absolute -top-0.5 -right-1 inline-flex"
										>
											<StatusDot
												tone={conn === "unauthorized" ? "err" : "warn"}
												label={t(`connection.${conn}`)}
											/>
										</span>
									)}
								</span>
								{t(`tabBar.${tab.key}`)}
							</button>
						);
					})}
				</div>
			</div>
		</nav>
	);
}
