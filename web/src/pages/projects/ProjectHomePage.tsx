/**
 * 项目主页（M24，`/projects/<id>`）：描述与仓库行、状态条、最近线程与「提交到 <基线>」（列容器，宽时并排）、
 * 文件、README、服务。镜像不能读（没有仓库、控制面没有 git、克隆失败或进行中、空仓库）时，提交 / 文件 / README
 * 三块换成一条说明，其余照常。页头是复制链接 / 复制 clone 命令 / 项目设置三个图标钮与「新建线程」。
 * 设置在 `…/settings/<分组>`。
 */
import type { ReactNode } from "react";
import type { Project } from "../../api/client";
import { useRepoStatus } from "../../api/repo-queries";
import { ErrorText } from "../../components/ui/primitives";
import { FilesSection } from "./home/FilesSection";
import {
	CloneButton,
	CopyLinkButton,
	NewThreadButton,
	SettingsButton,
} from "./home/HeaderActions";
import { ProjectIntro } from "./home/ProjectIntro";
import { ReadmeCard } from "./home/ReadmeCard";
import { RecentCommitsCard } from "./home/RecentCommitsCard";
import { RecentThreadsCard } from "./home/RecentThreadsCard";
import { RepoUnavailable } from "./home/RepoUnavailable";
import { ServicesCard } from "./home/ServicesCard";
import { StatusStrip } from "./home/StatusStrip";
import { ProjectGate, ProjectShell } from "./ProjectShell";

export function ProjectHomePage(): ReactNode {
	return <ProjectGate>{(p) => <ProjectHome project={p} />}</ProjectGate>;
}

function ProjectHome({ project }: { project: Project }): ReactNode {
	const repo = useRepoStatus(project.id);
	const status = repo.data;
	const readable =
		status &&
		status.kind !== "none" &&
		status.available &&
		status.state === "ready" &&
		!status.empty;
	const branch = status?.baseBranch ?? "";
	return (
		<ProjectShell
			project={project}
			wide
			actions={
				<>
					<CopyLinkButton project={project} />
					<CloneButton project={project} />
					<SettingsButton project={project} />
					<NewThreadButton project={project} />
				</>
			}
		>
			<div data-testid="project-home" className="flex flex-col gap-3">
				<ProjectIntro project={project} />
				<StatusStrip project={project} />
				{/* auto-fit 折叠空轨道：只有一个子元素时占满整行，够宽时并排两列 */}
				<div
					data-testid="project-home-columns"
					className="grid grid-cols-[repeat(auto-fit,minmax(min(100%,24rem),1fr))] items-start gap-3"
				>
					<RecentThreadsCard project={project} />
					{readable && (
						<RecentCommitsCard projectId={project.id} branch={branch} />
					)}
				</div>
				{readable && (
					<>
						<FilesSection projectId={project.id} refName={branch} />
						<ReadmeCard projectId={project.id} refName={branch} />
					</>
				)}
				{status && !readable && (
					<RepoUnavailable project={project} status={status} />
				)}
				{!status && <ErrorText error={repo.error} />}
				<ServicesCard project={project} />
			</div>
		</ProjectShell>
	);
}
