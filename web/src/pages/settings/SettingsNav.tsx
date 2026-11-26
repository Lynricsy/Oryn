/**
 * 设置导航：搜索 + 四组分区。宽屏是设置页左侧的导航栏（`rail`），手机是 `/settings` 的分组首页（`index`，D 分组列表）。
 * 搜索按分区名与该分区的关键词（`settings.keywords.<id>`，列出页内各项设置的叫法）匹配，不区分大小写。
 */
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { NavLink } from "react-router";
import { Icon } from "../../components/ui/icons";
import { TextInput } from "../../components/ui/primitives";
import { Group, GroupList, Tile } from "../../components/ui/workbench";
import { host } from "../../lib/host";
import {
	SETTINGS_GROUPS,
	type SettingsEntry,
	visibleEntries,
} from "./registry";

/** 按搜索词过滤可见分区；空词返回全部。 */
function useFiltered(query: string): readonly SettingsEntry[] {
	const { t } = useTranslation();
	return useMemo(() => {
		const all = visibleEntries(host.desktop !== null);
		const q = query.trim().toLocaleLowerCase();
		if (!q) return all;
		return all.filter((e) =>
			`${t(`settings.${e.id}`)} ${t(`settings.keywords.${e.id}`)} ${e.id}`
				.toLocaleLowerCase()
				.includes(q),
		);
	}, [query, t]);
}

export function SettingsNav({
	variant,
}: {
	variant: "rail" | "index";
}): ReactNode {
	const { t } = useTranslation();
	const [query, setQuery] = useState("");
	const entries = useFiltered(query);
	const groups = SETTINGS_GROUPS.map((g) => ({
		g,
		items: entries.filter((e) => e.group === g),
	})).filter((x) => x.items.length > 0);
	return (
		<nav
			aria-label={t("nav.settings")}
			className={`flex flex-col ${variant === "rail" ? "gap-4" : "gap-[30px]"}`}
		>
			<TextInput
				type="search"
				value={query}
				aria-label={t("settings.search")}
				placeholder={t("settings.search")}
				onChange={(e) => setQuery(e.target.value)}
				onKeyDown={(e) => {
					if (e.key === "Escape" && query) {
						e.stopPropagation();
						setQuery("");
					}
				}}
			/>
			{groups.length === 0 && (
				<p className="px-1.5 text-faint text-sm">{t("settings.noMatch")}</p>
			)}
			{groups.map(({ g, items }) =>
				variant === "rail" ? (
					<section key={g} className="flex flex-col gap-0.5">
						<h2 className="px-2 pb-1 font-semibold text-[12.5px] text-muted">
							{t(`settings.groups.${g}`)}
						</h2>
						{items.map((e) => (
							<NavLink
								key={e.id}
								to={`/settings/${e.id}`}
								data-settings-entry={e.id}
								className={({ isActive }) =>
									`flex h-8 items-center gap-2.5 rounded-[9px] px-2 text-[13.5px] transition-colors duration-150 ${isActive ? "bg-active font-medium text-fg" : "text-fg hover:bg-hover"}`
								}
							>
								<Icon name={e.icon} size={15} className="shrink-0 text-muted" />
								<span className="min-w-0 flex-1 truncate">
									{t(`settings.${e.id}`)}
								</span>
							</NavLink>
						))}
					</section>
				) : (
					<Group key={g} title={t(`settings.groups.${g}`)}>
						<GroupList>
							{items.map((e) => (
								<li key={e.id}>
									<NavLink
										to={`/settings/${e.id}`}
										data-settings-entry={e.id}
										className="flex min-h-12 items-center gap-3 py-2 pr-3 pl-3.5 transition-colors duration-150 hover:bg-raised"
									>
										<Tile icon={e.icon} />
										<span className="min-w-0 flex-1 truncate font-[550] text-fg text-sm">
											{t(`settings.${e.id}`)}
										</span>
										<Icon
											name="chevronRight"
											size={14}
											className="shrink-0 text-faint"
										/>
									</NavLink>
								</li>
							))}
						</GroupList>
					</Group>
				),
			)}
		</nav>
	);
}
