/**
 * 项目主页的开头（M24）：一行描述，以及仓库行（来源图标、地址或「Oryn 托管」、基线分支、镜像同步状态；
 * 没有仓库时说明「无仓库」）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Project } from "../../../api/client";
import { Icon } from "../../../components/ui/icons";
import { isGitHubRepo, repoLabel } from "../repo-label";
import { RepoSyncStatus } from "./RepoSyncStatus";

function BaseBranch({ branch }: { branch: string }): ReactNode {
	const { t } = useTranslation();
	return (
		<span
			className="inline-flex min-w-0 items-center gap-1"
			title={t("projects.baseBranch")}
		>
			<Icon name="branch" size={12} aria-hidden className="shrink-0" />
			<span className="min-w-0 truncate font-mono">{branch}</span>
		</span>
	);
}

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
				{project.hosted ? (
					<>
						<span className="inline-flex min-w-0 items-center gap-1">
							<Icon
								name="hostedRepo"
								size={12}
								aria-hidden
								className="shrink-0"
							/>
							<span className="min-w-0 truncate">{t("hostedRepo.label")}</span>
						</span>
						<BaseBranch branch={project.baseBranch || "main"} />
					</>
				) : project.repoUrl ? (
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
						{project.baseBranch && <BaseBranch branch={project.baseBranch} />}
					</>
				) : (
					<span className="inline-flex items-center gap-1">
						<Icon name="folder" size={12} aria-hidden />
						{t("projects.noRepo")}
					</span>
				)}
				<RepoSyncStatus project={project} />
			</p>
		</div>
	);
}
