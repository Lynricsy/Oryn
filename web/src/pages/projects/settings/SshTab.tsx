/** 项目设置「SSH 主机」（M24）：只对本项目 orb 可见的主机（M18）。 */
import type { ReactNode } from "react";
import type { Project } from "../../../api/client";
import { ProjectSshCard } from "../ProjectSsh";

export function SshTab({ project }: { project: Project }): ReactNode {
	return <ProjectSshCard project={project} />;
}
