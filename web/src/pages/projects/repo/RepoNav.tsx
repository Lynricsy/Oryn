/**
 * 仓库子页的子导航（M24 阶段 ②）：提交 / 分支 / 标签三个分段链接，选中项是纸面色的小块（同线程子页的状态筛选）。
 * 放在各仓库子页正文的最上面；文件页里三项都不选中。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { NavLink } from "react-router";
import { Icon, type IconName } from "../../../components/ui/icons";
import { tabClass } from "../../../components/ui/primitives";

const TABS = [
	["commits", "commit"],
	["branches", "branch"],
	["tags", "tag"],
] as const satisfies readonly (readonly [string, IconName])[];

export function RepoNav({ projectId }: { projectId: string }): ReactNode {
	const { t } = useTranslation();
	return (
		<nav
			aria-label={t("projectRepo.nav.label")}
			data-testid="repo-nav"
			className="flex w-fit shrink-0 items-center gap-0.5 rounded-md bg-inset p-0.5"
		>
			{TABS.map(([sub, icon]) => (
				<NavLink
					key={sub}
					to={`/projects/${projectId}/${sub}`}
					className={({ isActive }) =>
						`${tabClass(isActive)} h-6 ${isActive ? "bg-panel shadow-xs" : ""}`
					}
				>
					<Icon name={icon} size={13} aria-hidden />
					{t(`projectRepo.nav.${sub}`)}
				</NavLink>
			))}
		</nav>
	);
}
