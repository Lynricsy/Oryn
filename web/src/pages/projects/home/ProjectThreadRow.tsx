/**
 * 项目页里的一行线程（M24）：状态图标、标题卡图标（同侧栏）、标题、PR 摘要（第一个打开的 PR，没有就取第一个）、
 * 已交付 / 已归档徽标、最后活动时间。不复用侧栏的 ThreadRow：那个绑着侧栏的样式变量、选中态与 fork 子树，这里是纸面上的行。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Thread } from "../../../api/client";
import {
	CHECK_TONE,
	prIcon,
	prStatusText,
} from "../../../components/github/PrChip";
import { Badge, StatusDot, TitleGlyph } from "../../../components/ui/data";
import { Icon } from "../../../components/ui/icons";
import { ThreadStateIcon } from "../../../components/ui/primitives";
import { formatRelative } from "../../../lib/format";
import { activityOf } from "../../../lib/inbox";
import { parseTitleCard } from "../../../lib/title";

export function ProjectThreadRow({
	thread,
	now,
}: {
	thread: Thread;
	now: number;
}): ReactNode {
	const { t } = useTranslation();
	const card = parseTitleCard(thread.title);
	const pull =
		thread.pulls?.find((p) => p.state === "open") ?? thread.pulls?.[0];
	return (
		<li>
			<Link
				to={`/t/${thread.id}`}
				data-testid="project-thread-row"
				data-thread={thread.id}
				className="flex min-h-10 items-center gap-3 rounded-md px-2 py-1.5 text-sm transition-colors duration-150 hover:bg-hover"
			>
				<ThreadStateIcon state={thread.state} size={14} />
				{card.icon && <TitleGlyph icon={card.icon} />}
				<span className="min-w-0 flex-1 truncate">
					{card.text || t("thread.untitled")}
				</span>
				{pull && (
					<span
						title={prStatusText(pull, t)}
						className="inline-flex shrink-0 items-center gap-1 text-muted text-xs tabular-nums"
					>
						<Icon name={prIcon(pull)} size={12} aria-hidden />#{pull.number}
						{pull.checks && (
							<StatusDot
								tone={CHECK_TONE[pull.checks]}
								label={t(`thread.pr.checks.${pull.checks}`)}
							/>
						)}
					</span>
				)}
				{thread.shippedAt !== undefined && (
					<Badge tone="ok" icon="checkCircle">
						{t("projectHome.shipped")}
					</Badge>
				)}
				{thread.archivedAt !== undefined && (
					<Badge icon="archive">{t("sidebar.archived")}</Badge>
				)}
				<span className="shrink-0 text-2xs text-faint tabular-nums">
					{formatRelative(activityOf(thread), now)}
				</span>
			</Link>
		</li>
	);
}
