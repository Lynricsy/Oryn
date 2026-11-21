/** 项目主页的开头（M24）：一行描述，以及仓库行（来源图标、地址、基线分支；没有仓库时说明「无仓库」）。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Project } from "../../../api/client";
import { Icon } from "../../../components/ui/icons";
import { isGitHubRepo, repoLabel } from "../repo-label";

export function ProjectIntro({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	return (
		<div className="flex flex-col gap-1.5">
			{project.description && (
				<p className="text-muted text-sm">{project.description}</p>
			)}
			<p
				data-testid="project-repo"
				className="flex flex-wrap items-center gap-x-3 gap-y-1 text-faint text-xs"
			>
				{project.repoUrl ? (
					<>
						<span className="inline-flex min-w-0 items-center gap-1">
							<Icon
								name={isGitHubRepo(project.repoUrl) ? "github" : "code"}
								size={12}
								aria-hidden
								className="shrink-0"
							/>
							<span className="min-w-0 truncate font-mono">
								{repoLabel(project.repoUrl)}
							</span>
						</span>
						{project.baseBranch && (
							<span
								className="inline-flex min-w-0 items-center gap-1"
								title={t("projects.baseBranch")}
							>
								<Icon
									name="branch"
									size={12}
									aria-hidden
									className="shrink-0"
								/>
								<span className="min-w-0 truncate font-mono">
									{project.baseBranch}
								</span>
							</span>
						)}
					</>
				) : (
					<span className="inline-flex items-center gap-1">
						<Icon name="folder" size={12} aria-hidden />
						{t("projects.noRepo")}
					</span>
				)}
			</p>
		</div>
	);
}
