/**
 * 「更多」分区侧栏：连接状态行（只在断线时出现，`warn` 脉动、`unauthorized` 为 `err`）、「切换明暗」、「设置」，
 * 其后是 `MORE_ENTRIES` 的「概览」组。设置各分区的导航在设置页里。不设分区标题行。
 * 路由落到某个条目时（直接打开设置分区、刷新）把高亮的那行滚进可视范围，不让它停在折叠线或底栏胶囊下面。
 */
import type { ParseKeys } from "i18next";
import { type ReactNode, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { NavLink, useLocation } from "react-router";
import { useConnection } from "../../state/stores";
import { type ThemeMode, themeStore, useTheme } from "../../state/theme";
import { StatusDot } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { MORE_ENTRIES, type MoreEntry } from "./more-entries";
import { closeOnCompact, LIST_LINK, LIST_ROW, PANE_SCROLL } from "./pane-parts";

const MODE_ICON = {
	system: "monitor",
	light: "sun",
	dark: "moon",
} as const satisfies Record<ThemeMode, IconName>;

/** 「设置」组只有一行，不加组标题 */
const GROUPS = [
	{ key: "settings", labelKey: null },
	{ key: "overview", labelKey: "more.overview" },
] as const satisfies readonly {
	key: MoreEntry["group"];
	labelKey: string | null;
}[];

export function MorePane(): ReactNode {
	const { t } = useTranslation();
	const conn = useConnection((s) => s.status);
	const mode = useTheme((s) => s.mode);
	const scrollRef = useRef<HTMLDivElement>(null);
	const { pathname } = useLocation();
	useEffect(() => {
		const box = scrollRef.current;
		const row = box?.querySelector("[aria-current=page]");
		if (!box || !row || !pathname) return;
		// 只滚本分区的滚动区（scrollIntoView 会带动外壳）；底部让出 scroll-padding（悬浮底栏的高度）
		const b = box.getBoundingClientRect();
		const r = row.getBoundingClientRect();
		const s = getComputedStyle(box);
		const top = b.top + (Number.parseFloat(s.scrollPaddingTop) || 0);
		const bottom = b.bottom - (Number.parseFloat(s.scrollPaddingBottom) || 0);
		if (r.top < top) box.scrollTop -= top - r.top;
		else if (r.bottom > bottom) box.scrollTop += r.bottom - bottom;
	}, [pathname]);
	return (
		<div ref={scrollRef} className={PANE_SCROLL}>
			<div className="flex flex-col gap-0.5" data-testid="sidebar-more">
				{conn !== "open" && (
					<div
						data-testid="sidebar-connection"
						className="flex min-h-10 items-center gap-2.5 px-2.5 text-muted text-sm"
					>
						<span className="grid w-4 shrink-0 place-items-center">
							<StatusDot
								tone={conn === "unauthorized" ? "err" : "warn"}
								pulse={conn !== "unauthorized"}
							/>
						</span>
						<span className="truncate">{t(`connection.${conn}`)}</span>
					</div>
				)}
				<button
					type="button"
					className={LIST_ROW}
					onClick={() => themeStore.getState().cycleMode()}
				>
					<Icon
						name={MODE_ICON[mode]}
						size={16}
						className="shrink-0 text-muted"
					/>
					<span className="min-w-0 flex-1 truncate">{t("sidebar.theme")}</span>
					<span className="shrink-0 text-faint text-xs">
						{t(`appearance.modes.${mode}`)}
					</span>
				</button>
				{GROUPS.map((g) => (
					<div key={g.key} data-more-group={g.key} className="flex flex-col">
						{g.labelKey && (
							<div className="px-2.5 pt-3 pb-1 font-semibold text-muted text-xs">
								{t(g.labelKey)}
							</div>
						)}
						{MORE_ENTRIES.filter((e) => e.group === g.key).map((e) => (
							<NavLink
								key={e.key}
								to={e.to}
								data-more-entry={e.key}
								onClick={closeOnCompact}
								className={LIST_LINK}
							>
								<Icon name={e.icon} size={16} className="shrink-0 text-muted" />
								<span className="min-w-0 flex-1 truncate">
									{/* 注册表的 labelKey 是普通字符串（键集合由 MorePane.test 对照语言文件校验） */}
									{t(e.labelKey as ParseKeys)}
								</span>
							</NavLink>
						))}
					</div>
				))}
			</div>
		</div>
	);
}
