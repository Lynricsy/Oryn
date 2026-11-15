/** 各分区侧栏共用的小件：行样式、未读点、计数签、分组图标与可折叠分组、跳转后收起手机侧栏、分钟时钟。 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { breakpointFor } from "../../lib/viewport";
import { uiStore } from "../../state/ui";
import { Icon, type IconName } from "../ui/icons";

/** 侧栏里的导航行（Space、助手对话）：与线程行同高同底色。 */
export const NAV_ROW = ({ isActive }: { isActive: boolean }) =>
	`flex h-8 items-center gap-2 rounded-lg pr-2 pl-2 text-sm transition-colors duration-150 ${isActive ? "bg-[var(--sb-sel)] font-medium text-fg" : "text-fg hover:bg-[var(--sb-hover)]"}`;

/** 列表行（项目、「更多」条目、各分区首行的新建按钮）：比线程行高，图标在左。 */
export const LIST_ROW =
	"flex min-h-10 w-full items-center gap-2.5 rounded-lg px-2.5 text-left text-fg text-sm transition-colors duration-150 hover:bg-[var(--sb-hover)]";

/** 选中态的列表行（NavLink 的 className）。 */
export const LIST_LINK = ({ isActive }: { isActive: boolean }) =>
	`${LIST_ROW} ${isActive ? "bg-[var(--sb-sel)] font-medium" : ""}`;

/**
 * 分区滚动区：底部让出悬浮底栏的高度（`--tabbar-h`，侧栏外壳设置）再留 1rem，最后一行能滚到胶囊上方；
 * scroll-padding 同步让出这段，Tab 聚焦与 scrollIntoView 把行停在胶囊上方而不是压在胶囊底下。
 */
export const PANE_SCROLL =
	"min-h-0 flex-1 overflow-y-auto px-2 pt-1 pb-[calc(var(--tabbar-h,0px)+1rem)] scroll-pb-[calc(var(--tabbar-h,0px)+0.5rem)]";

/** 与线程行同款的未读点。 */
export function UnreadDot(): ReactNode {
	const { t } = useTranslation();
	return (
		<span
			role="img"
			className="mr-0.5 h-[7px] w-[7px] shrink-0 rounded-full bg-accent ring-[3px] ring-accent/20"
			aria-label={t("sidebar.unread")}
		/>
	);
}

export function CountPill({ n }: { n: number }): ReactNode {
	return (
		<span className="inline-grid h-[18px] min-w-5 shrink-0 place-items-center rounded-sm border border-fg/12 px-1.5 font-semibold text-2xs text-muted tabular-nums leading-none">
			{n}
		</span>
	);
}

export function GroupIcon({ name }: { name: IconName }): ReactNode {
	return (
		<Icon name={name} size={14} aria-hidden className="shrink-0 text-faint" />
	);
}

/** 侧栏里跳转之后：抽屉 / 下沉侧栏（compact）收起；medium 的推入式侧边栏保持用户的开关选择。 */
export function closeOnCompact(): void {
	if (breakpointFor(window.innerWidth) === "compact")
		uiStore.getState().setSidebar(false);
}

/** 相对时间的基准时刻：每分钟刷新一次。 */
export function useNow(): number {
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		const timer = setInterval(() => setNow(Date.now()), 60_000);
		return () => clearInterval(timer);
	}, []);
	return now;
}

/**
 * 分组：标题行可折叠。主分组（置顶、项目）的箭头悬停才显现；
 * `fold` 分组（未活动、稍后提醒、已归档）箭头常驻在最左、默认收起。
 */
export function Group({
	title,
	icon,
	children,
	defaultOpen = true,
	open: openProp,
	onOpenChange,
	action,
	count,
	fold,
}: {
	title: string;
	icon: ReactNode;
	children: ReactNode;
	defaultOpen?: boolean;
	/** 受控展开（已归档按展开与否决定是否拉取） */
	open?: boolean;
	onOpenChange?: (open: boolean) => void;
	action?: ReactNode;
	count?: number;
	fold?: boolean;
}): ReactNode {
	const [inner, setInner] = useState(defaultOpen);
	const open = openProp ?? inner;
	const toggle = () => {
		setInner(!open);
		onOpenChange?.(!open);
	};
	const caret = open ? "chevronDown" : "chevronRight";
	return (
		<div className={fold ? "" : "mb-2"}>
			<div
				className={`group/head flex items-center gap-1 rounded-lg pr-1 ${fold ? "transition-colors duration-150 hover:bg-[var(--sb-hover)]" : ""}`}
			>
				<button
					type="button"
					className={`flex min-w-0 flex-1 items-center gap-2 pl-2.5 text-left font-semibold text-muted text-xs hover:text-fg ${fold ? "h-8" : "h-[30px]"}`}
					onClick={toggle}
					aria-expanded={open}
				>
					{fold && (
						<Icon
							name={caret}
							size={11}
							aria-hidden
							className="-mx-0.5 shrink-0 text-ghost"
						/>
					)}
					{icon}
					<span className="min-w-0 flex-1 truncate">{title}</span>
					{count !== undefined && <CountPill n={count} />}
					{!fold && (
						<Icon
							name={caret}
							size={11}
							aria-hidden
							className={`shrink-0 text-ghost transition-opacity duration-150 ${open ? "opacity-0 group-focus-within/head:opacity-100 group-hover/head:opacity-100" : ""}`}
						/>
					)}
				</button>
				{action}
			</div>
			{open && <div className="flex flex-col pb-1">{children}</div>}
		</div>
	);
}
