/**
 * 「更多」分区侧栏：连接状态行（只在断线时出现，`warn` 脉动、`unauthorized` 为 `err`）、「切换明暗」，
 * 其后是 `MORE_ENTRIES` 的三组条目（概览 / Agent / 设置）；浏览器里不出现桌面 App 专属的条目。不设分区标题行。
 */
import type { ParseKeys } from "i18next";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { NavLink } from "react-router";
import { host } from "../../lib/host";
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

const GROUPS = [
	{ key: "overview", labelKey: "more.overview" },
	{ key: "agent", labelKey: "more.agent" },
	{ key: "settings", labelKey: "nav.settings" },
] as const satisfies readonly {
	key: MoreEntry["group"];
	labelKey: string;
}[];

export function MorePane(): ReactNode {
	const { t } = useTranslation();
	const conn = useConnection((s) => s.status);
	const mode = useTheme((s) => s.mode);
	const entries = MORE_ENTRIES.filter(
		(e) => !(e.desktopOnly && host.desktop === null),
	);
	return (
		<div className={PANE_SCROLL}>
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
						<div className="px-2.5 pt-3 pb-1 font-semibold text-muted text-xs">
							{t(g.labelKey)}
						</div>
						{entries
							.filter((e) => e.group === g.key)
							.map((e) => (
								<NavLink
									key={e.key}
									to={e.to}
									data-more-entry={e.key}
									onClick={closeOnCompact}
									className={LIST_LINK}
								>
									<Icon
										name={e.icon}
										size={16}
										className="shrink-0 text-muted"
									/>
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
