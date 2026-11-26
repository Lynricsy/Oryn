/** 项目设置「定时任务」（M24）：本项目「每次新建线程」的定时列表，D 的分组（自带分组标题与新建按钮），不再包卡片。 */
import type { ReactNode } from "react";
import type { Project } from "../../../api/client";
import { ProjectSchedules } from "../../settings/ProjectSchedules";

export function SchedulesTab({ project }: { project: Project }): ReactNode {
	return <ProjectSchedules project={project} />;
}
