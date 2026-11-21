/** 项目主页的「最近线程」（M24）：本项目最近活动的 5 个线程（含已归档，安静的项目也不会空着），「查看全部」去线程子页。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Project } from "../../../api/client";
import { useNow } from "../../../components/layout/pane-parts";
import { Card } from "../../../components/ui/data";
import { useProjectThreads } from "../project-threads";
import { NewThreadButton } from "./HeaderActions";
import { ProjectThreadRow } from "./ProjectThreadRow";

const RECENT = 5;

export function RecentThreadsCard({
	project,
}: {
	project: Project;
}): ReactNode {
	const { t } = useTranslation();
	const now = useNow();
	const recent = useProjectThreads(project.id).threads.slice(0, RECENT);
	return (
		<div data-testid="project-recent" className="min-w-0">
			<Card
				title={t("projectHome.recentThreads")}
				icon="chats"
				action={
					<Link
						to={`/projects/${project.id}/threads`}
						className="font-normal text-accent-ink text-xs hover:underline"
					>
						{t("projectHome.viewAll")}
					</Link>
				}
			>
				{recent.length > 0 ? (
					<ul className="-mx-2">
						{recent.map((th) => (
							<ProjectThreadRow key={th.id} thread={th} now={now} />
						))}
					</ul>
				) : (
					<div className="flex flex-col items-start gap-2 py-1">
						<p className="text-faint text-sm">{t("projectHome.noThreads")}</p>
						<NewThreadButton project={project} />
					</div>
				)}
			</Card>
		</div>
	);
}
