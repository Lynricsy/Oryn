/**
 * 线程卡片（Space 页的线程列表、协调者工具卡）：状态图标 + 标题 + 项目徽标 + 活动一行。数据取线程索引里该 id 的线程
 * （实时变化），索引里没有时显示 id 与「已删除或无权查看」。`awaiting_input` 的卡片用强调色描边（需要你处理）。
 * 整张卡可点（标题链接的 `::after` 铺满），`actions` 叠在其上独立可点。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Thread } from "../../api/client";
import { useProjects } from "../../api/queries";
import { formatRelative } from "../../lib/format";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";
import { ProjectBadge } from "../ui/data";
import { Icon } from "../ui/icons";
import { ThreadStateIcon } from "../ui/primitives";

export function ThreadCard({
	threadId,
	actions,
}: {
	threadId: string;
	actions?: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const thread: Thread | undefined = useIndex((s) => s.threads[threadId]);
	const projects = useProjects();
	if (!thread)
		return (
			<div
				data-thread-card={threadId}
				className="flex items-center gap-2 rounded-lg border border-border border-dashed px-3 py-2 text-faint text-xs"
			>
				<Icon name="question" size={13} aria-hidden className="shrink-0" />
				<span className="min-w-0 truncate font-mono">{threadId}</span>
				<span className="shrink-0">{t("space.threadGone")}</span>
			</div>
		);
	const card = parseTitleCard(thread.title);
	const title = card.text || t("thread.untitled");
	const project = thread.projectId
		? projects.data?.projects?.find((p) => p.id === thread.projectId)
		: undefined;
	const awaiting = thread.state === "awaiting_input";
	const line =
		thread.state === "working" && thread.activity
			? thread.activity
			: thread.archivedAt
				? thread.shippedAt
					? t("space.shippedAt", {
							when: formatRelative(thread.shippedAt),
						})
					: t("space.archivedAt", {
							when: formatRelative(thread.archivedAt),
						})
				: t(`threadState.${thread.state}`);
	return (
		<div
			data-thread-card={threadId}
			data-state={thread.state}
			className={`group relative flex items-start gap-2.5 rounded-lg border px-3 py-2 transition-colors duration-150 hover:bg-hover ${awaiting ? "border-accent/45 bg-accent-soft/40" : "border-border bg-panel"}`}
		>
			<span className="mt-0.5 inline-flex">
				<ThreadStateIcon state={thread.state} size={14} />
			</span>
			<span className="flex min-w-0 flex-1 flex-col gap-0.5">
				<Link
					to={`/t/${thread.id}`}
					className={`min-w-0 truncate text-sm after:absolute after:inset-0 after:rounded-lg after:content-[''] ${thread.unread ? "font-semibold" : "font-medium"}`}
				>
					{card.icon ? `${card.icon} ${title}` : title}
				</Link>
				<span className="flex min-w-0 items-center gap-1.5 text-faint text-xs">
					{project && (
						<ProjectBadge name={project.name} icon={project.icon} size={14} />
					)}
					{thread.shippedAt && (
						<Icon
							name="rocket"
							size={11}
							aria-label={t("space.shipped")}
							className="shrink-0 text-ok"
						/>
					)}
					<span className="min-w-0 truncate">{line}</span>
				</span>
			</span>
			{thread.unread && (
				<span
					role="img"
					aria-label={t("sidebar.unread")}
					className="mt-1.5 h-[7px] w-[7px] shrink-0 rounded-full bg-accent ring-[3px] ring-accent/20"
				/>
			)}
			{actions && (
				<span className="relative z-10 flex shrink-0">{actions}</span>
			)}
		</div>
	);
}
