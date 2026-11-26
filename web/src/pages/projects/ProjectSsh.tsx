/**
 * 项目设置「SSH 主机」分组（M18）：只对这个项目的 orb 可见的主机；与全局主机同名时以这里为准。
 * 直接用设置页的 `HostsCard`（D 的 `Group` + `GroupList`，新建时固定为本项目），外面不包卡片。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Project } from "../../api/client";
import { HostsCard } from "../settings/agent/ssh/HostsCard";

export function ProjectSshCard({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	return (
		<HostsCard
			projectId={project.id}
			title={t("projectsPage.ssh.title")}
			intro={t("projectsPage.ssh.hint")}
		/>
	);
}
