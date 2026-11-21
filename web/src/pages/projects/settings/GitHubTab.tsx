/**
 * 项目设置「GitHub」（M24）：绑定状态、事件回流与触发规则（`ProjectGitHubCard`），以及推送 / 开 PR 门禁。
 * 仓库不在 GitHub 上时只给说明和去「概览」改仓库地址的入口（条件与 `ProjectGitHubCard` 返回 null 时相同）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Project } from "../../../api/client";
import { EmptyState } from "../../../components/ui/primitives";
import { ProjectGatesCard } from "../ProjectGates";
import { ProjectGitHubCard } from "../ProjectGitHub";

export function GitHubTab({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	// 门禁取消 = 换 key 还原为服务端值
	const [rev, setRev] = useState(0);
	if (!project.github && !project.repoUrl?.startsWith("https://github.com/"))
		return (
			<EmptyState icon="github" title={t("projectSettings.github.noneTitle")}>
				<div className="flex flex-col items-center gap-3">
					<p>{t("projectSettings.github.noneBody")}</p>
					<Link
						to={`/projects/${project.id}/settings/overview`}
						className="text-accent-ink text-sm underline"
					>
						{t("projectSettings.github.editRepo")}
					</Link>
				</div>
			</EmptyState>
		);
	return (
		<>
			<ProjectGitHubCard project={project} />
			<ProjectGatesCard
				key={`${project.updatedAt}:${rev}`}
				project={project}
				onCancel={() => setRev((r) => r + 1)}
			/>
		</>
	);
}
