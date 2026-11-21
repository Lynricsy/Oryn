/** 项目设置「定时任务」（M24）：本项目「每次新建线程」的定时列表（自带小标题与新建按钮）。 */
import type { ReactNode } from "react";
import type { Project } from "../../../api/client";
import { Card } from "../../../components/ui/data";
import { ProjectSchedules } from "../../settings/ProjectSchedules";

export function SchedulesTab({ project }: { project: Project }): ReactNode {
	return (
		<Card>
			<ProjectSchedules project={project} />
		</Card>
	);
}
