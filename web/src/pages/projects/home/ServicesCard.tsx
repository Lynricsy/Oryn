/**
 * 项目主页的「服务」（只读，M9.6 起）：本项目、未删除、非协调的 orb，按最近活动取前 12 台，列出各自上报的受管服务
 * （失败和已停止的也列）。服务列表就是 `GET orbs` 里各 orb 的 `services` 镜像（随 WS `orb` 消息更新），
 * 不另行请求、不唤醒 orb。没有服务时整张卡不渲染。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Project, Schemas } from "../../../api/client";
import { useOrbs } from "../../../api/queries";
import { Badge, Card, type Tone } from "../../../components/ui/data";
import { Icon } from "../../../components/ui/icons";
import { parseTitleCard } from "../../../lib/title";
import { useIndex } from "../../../state/stores";
import { orbActivity } from "../project-orbs";

/** 服务卡最多汇总的 orb 数（按最近活动）。 */
const MAX_SERVICE_ORBS = 12;

const SERVICE_TONE: Record<Schemas["OrbServiceStatus"], Tone> = {
	running: "ok",
	starting: "warn",
	backoff: "warn",
	unhealthy: "err",
	failed: "err",
	stopped: "neutral",
};

export function ServicesCard({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const threads = useIndex((s) => s.threads);
	const orbs = useOrbs().data?.orbs ?? [];
	const rows = orbs
		.filter((o) => o.projectId === project.id && !o.deletedAt && !o.coordinator)
		.sort((a, b) => orbActivity(b) - orbActivity(a))
		.slice(0, MAX_SERVICE_ORBS)
		.flatMap((o) =>
			(o.services ?? []).map((s) => ({
				key: `${o.id}/${s.name}`,
				service: s,
				threadId: s.threadId ?? o.threadIds[0],
			})),
		);
	if (rows.length === 0) return null;
	return (
		<div data-testid="project-services">
			<Card title={t("projectsPage.services")} icon="globe">
				<ul className="-mx-2 flex flex-col">
					{rows.map(({ key, service, threadId }) => {
						const thread = threadId ? threads[threadId] : undefined;
						const threadTitle =
							(thread && parseTitleCard(thread.title).text) || threadId;
						return (
							<li
								key={key}
								data-testid="project-service"
								className="flex min-h-9 items-center gap-2 rounded-md px-2 py-1 text-sm"
							>
								<span className="min-w-0 truncate font-medium font-mono text-xs">
									{service.name}
								</span>
								<Badge tone={SERVICE_TONE[service.status]}>
									{t(`services.status.${service.status}`)}
								</Badge>
								<span className="min-w-0 flex-1" />
								{threadId && (
									<Link
										to={`/t/${threadId}`}
										title={threadTitle}
										className="flex min-w-0 max-w-[50%] items-center gap-1.5 text-faint text-xs transition-colors duration-150 hover:text-fg"
									>
										<Icon name="chats" size={12} className="shrink-0" />
										<span className="truncate">{threadTitle}</span>
									</Link>
								)}
							</li>
						);
					})}
				</ul>
			</Card>
		</div>
	);
}
