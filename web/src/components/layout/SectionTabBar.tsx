/**
 * 底栏（M9.6，全端同一个）：线程 / 助手 / 项目 / 更多。点一项 = 切分区（侧栏内容与主区路由一起换，导航到该分区记住的路径）；
 * 手机下沉侧栏里点后侧栏保持打开，再点一次已选中的分区收起侧栏。选中项是强调色文字 + 一块滑动底板（四等分，按序号定位，
 * 200ms `--ease`，与分区轨道同步；减少动效时由全局规则压掉过渡）。
 * 助手项显示全局助手的生效名字与圆形头像（都没设时「全局助手」+ sparkle）；任一助手对话未读时带未读点。
 * 「更多」项在连接不是 open 时带状态点。
 * 底栏悬浮在分区内容之上（绝对定位贴侧栏底边）：列表照常滚到它下面、从玻璃胶囊后面透出来，不再被一条实底截断；
 * 各分区滚动区底部按 `--tabbar-h`（`tabBarHeight`，由侧栏外壳设置）留白，最后一行能滚到胶囊上方。
 * 胶囊两种场合都是玻璃（`.glass`，自带模糊）：`tone="glass"` 是手机下沉侧栏层，底部让出安全区；`inset` 是列侧栏 / 双段抽屉，
 * `safeBottom` 时再让出底部安全区。外层不接收指针（胶囊两侧露出的列表仍可点）。底栏不带 `data-window-drag`（不能拖动窗口）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useAssistantIdentity } from "../../api/identity-queries";
import { SECTION_ORDER, type SectionId } from "../../lib/sections";
import { useConnection, useIndex } from "../../state/stores";
import { StatusDot } from "../ui/data";
import { AssistantAvatar } from "../ui/IdentityAvatar";
import { Icon, type IconName } from "../ui/icons";
import { useSection, useSwitchSection } from "./useSection";

const ICONS = {
	threads: "chats",
	assistant: "sparkle",
	projects: "folder",
	more: "more",
} as const satisfies Record<SectionId, IconName>;

/** 胶囊高度与上下外边距：与下面 `outer` 的类名一致，供侧栏外壳设 `--tabbar-h`。 */
const PILL = "52px";
export function tabBarHeight(
	tone: "glass" | "inset",
	safeBottom: boolean,
): string {
	const bottom =
		tone === "glass"
			? "max(env(safe-area-inset-bottom), 0.5rem)"
			: safeBottom
				? "calc(0.5rem + env(safe-area-inset-bottom))"
				: "0.5rem";
	return `calc(${PILL} + 0.375rem + ${bottom})`;
}

/** 快捷键提示：macOS / iOS 用 ⌘n，其余 Ctrl+n。 */
const MAC =
	typeof navigator !== "undefined" &&
	/^(Mac|iPhone|iPad|iPod)/.test(navigator.platform);

export function SectionTabBar({
	tone,
	safeBottom = false,
}: {
	tone: "glass" | "inset";
	safeBottom?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const section = useSection();
	const switchSection = useSwitchSection();
	const conn = useConnection((s) => s.status);
	const assistant = useAssistantIdentity();
	const assistantUnread = useIndex((s) =>
		Object.values(s.threads).some(
			(th) => th.kind === "assistant" && th.unread && !th.archivedAt,
		),
	);
	const index = SECTION_ORDER.indexOf(section);
	const hasAvatar = Boolean(
		assistant.avatar.avatarIcon || assistant.avatar.avatarImage,
	);
	const outer =
		tone === "glass"
			? "px-3 pt-1.5 pb-[max(env(safe-area-inset-bottom),0.5rem)]"
			: `px-2 pt-1.5 ${safeBottom ? "pb-[calc(0.5rem+env(safe-area-inset-bottom))]" : "pb-2"}`;
	return (
		<nav
			aria-label={t("tabBar.label")}
			data-testid="section-tabbar"
			data-tone={tone}
			className={`pointer-events-none absolute inset-x-0 bottom-0 z-10 ${outer}`}
		>
			<div className="glass pointer-events-auto h-[52px] rounded-full p-1">
				<div className="relative grid h-full grid-cols-4">
					<span
						aria-hidden
						data-testid="section-tabbar-plate"
						className="absolute inset-y-0 rounded-full bg-[var(--sb-sel)] transition-[left] duration-200 ease-[var(--ease)]"
						style={{ left: `${index * 25}%`, width: "25%" }}
					/>
					{SECTION_ORDER.map((key, i) => {
						const on = key === section;
						const label =
							key === "assistant"
								? (assistant.name ?? t("tabBar.assistant"))
								: t(`tabBar.${key}`);
						const keys = MAC ? `⌘${i + 1}` : `Ctrl+${i + 1}`;
						return (
							<button
								key={key}
								type="button"
								data-tab={key}
								aria-current={on ? "true" : undefined}
								title={t("sections.tabTitle", { label, keys })}
								onClick={() => switchSection(key, { toggleSink: true })}
								className={`relative z-10 flex min-h-11 min-w-0 flex-col items-center justify-center gap-0.5 rounded-full text-2xs leading-none transition-colors duration-150 ${on ? "font-semibold text-accent-ink" : "font-medium text-muted hover:text-fg"}`}
							>
								<span className="relative inline-flex">
									{key === "assistant" && hasAvatar ? (
										<AssistantAvatar size={20} shape="circle" />
									) : (
										<Icon
											name={ICONS[key]}
											size={20}
											weight={on ? "fill" : "regular"}
											aria-hidden
										/>
									)}
									{key === "assistant" && assistantUnread && (
										<span
											role="img"
											data-testid="tabbar-unread"
											aria-label={t("sidebar.unread")}
											className="absolute -top-0.5 -right-1 h-[7px] w-[7px] rounded-full bg-accent ring-2 ring-[var(--panel)]"
										/>
									)}
									{key === "more" && conn !== "open" && (
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
								<span className="max-w-full truncate px-1">{label}</span>
							</button>
						);
					})}
				</div>
			</div>
		</nav>
	);
}
