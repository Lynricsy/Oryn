/**
 * 项目设置「Agent」（M24）：项目级中央 omp 配置（M12.2）与项目记忆开关（记忆页「项目」卡的同一行）。
 * 记忆的项目列表只含未归档项目，归档项目在这里只给说明。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Project } from "../../../api/client";
import {
	useMemoryProjects,
	usePutMemoryProject,
} from "../../../api/memory-queries";
import { Card } from "../../../components/ui/data";
import { Spinner } from "../../../components/ui/primitives";
import { MemoryError } from "../../settings/agent/memory/errors";
import { MemoryProjectRow } from "../../settings/agent/memory/ToggleCard";
import { ProjectAgentConfigCard } from "../ProjectAgentConfig";

function MemoryCard({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const q = useMemoryProjects();
	const put = usePutMemoryProject();
	const mine = q.data?.items.find((m) => m.projectId === project.id);
	return (
		<Card title={t("projectSettings.memory.title")} icon="brain">
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadProjects" error={q.error} />
			) : !mine ? (
				<p className="text-faint text-sm">
					{t("projectSettings.memory.archived")}
				</p>
			) : (
				<div className="flex flex-col gap-2">
					<p className="text-faint text-xs">{t("memoryPage.projects.hint")}</p>
					<ul className="-mx-2">
						<MemoryProjectRow
							project={mine}
							busy={put.isPending}
							onChange={(enabled) => put.mutate({ id: project.id, enabled })}
						/>
					</ul>
					<MemoryError step="saveProject" error={put.error} />
				</div>
			)}
		</Card>
	);
}

export function AgentTab({ project }: { project: Project }): ReactNode {
	return (
		<>
			<ProjectAgentConfigCard project={project} />
			<MemoryCard project={project} />
		</>
	);
}
