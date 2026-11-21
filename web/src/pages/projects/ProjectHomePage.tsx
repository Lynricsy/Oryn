/**
 * 项目主页（M24，`/projects/<id>`）：描述与仓库行、状态条、最近线程（列容器，宽时与之后的「提交到基线」并排）、服务。
 * 页头是复制链接 / 复制 clone 命令 / 项目设置三个图标钮与「新建线程」。设置在 `…/settings/<分组>`。
 */
import type { ReactNode } from "react";
import type { Project } from "../../api/client";
import {
	CloneButton,
	CopyLinkButton,
	NewThreadButton,
	SettingsButton,
} from "./home/HeaderActions";
import { ProjectIntro } from "./home/ProjectIntro";
import { RecentThreadsCard } from "./home/RecentThreadsCard";
import { ServicesCard } from "./home/ServicesCard";
import { StatusStrip } from "./home/StatusStrip";
import { ProjectGate, ProjectShell } from "./ProjectShell";

export function ProjectHomePage(): ReactNode {
	return <ProjectGate>{(p) => <ProjectHome project={p} />}</ProjectGate>;
}

function ProjectHome({ project }: { project: Project }): ReactNode {
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
				</div>
				<ServicesCard project={project} />
			</div>
		</ProjectShell>
	);
}
