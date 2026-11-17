/**
 * 项目页的「SSH 主机」卡片（M18）：只对这个项目的 orb 可见的主机；与全局主机同名时以这里为准。
 * 列表、对话框与操作同设置页的主机卡片（新建时固定为本项目）。
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
