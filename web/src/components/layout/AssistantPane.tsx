/**
 * 「助手」分区侧栏：全局助手的会话列表。不设分区标题行（底栏选中态已表明分区）。
 * 首行「新对话」（协调宿主不可用时禁用，下面一条警告）；会话行 = 状态图标 + 标题 + 未读点或相对时间，悬停 / 聚焦时
 * 行尾出现归档按钮；末尾折叠组「已归档」展开才拉取。链接的 textContent 只等于标题（时间走 `data-time` 伪元素）。
 */
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { NavLink, useLocation, useNavigate } from "react-router";
import { api, type Thread, unwrap } from "../../api/client";
import { useCoordinatorStatus } from "../../api/coordinator-queries";
import { createAssistantConversation } from "../../api/space-queries";
import { useAssistantConversations } from "../../lib/assistant-conversations";
import { formatRelative } from "../../lib/format";
import { parseTitleCard } from "../../lib/title";
import { patchThread } from "../../state/actions";
import { Callout } from "../ui/data";
import { Icon } from "../ui/icons";
import { describeError, IconButton, ThreadStateIcon } from "../ui/primitives";
import {
	closeOnCompact,
	Group,
	GroupIcon,
	LIST_ROW,
	NAV_ROW,
	UnreadDot,
	useNow,
} from "./pane-parts";

function ConversationRow({
	thread,
	now,
	archivable,
}: {
	thread: Thread;
	now: number;
	archivable: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const { pathname } = useLocation();
	const title = parseTitleCard(thread.title).text || t("assistant.untitled");
	const to = `/assistant/${thread.id}`;
	const stamp = formatRelative(thread.lastMessageAt ?? thread.updatedAt, now);
	return (
		<div className="group/row relative">
			<NavLink to={to} onClick={closeOnCompact} className={NAV_ROW}>
				<ThreadStateIcon state={thread.state} size={14} />
				<span
					className={`min-w-0 flex-1 truncate ${thread.unread ? "font-medium" : ""}`}
				>
					{title}
				</span>
				{thread.unread ? (
					<UnreadDot />
				) : (
					<span
						aria-hidden
						data-time={stamp}
						className={`shrink-0 font-normal text-2xs text-faint tabular-nums after:content-[attr(data-time)] ${archivable ? "group-focus-within/row:invisible group-hover/row:invisible" : ""}`}
					/>
				)}
			</NavLink>
			{archivable && (
				<IconButton
					icon="archive"
					box="xs"
					size={13}
					label={t("assistantPane.archiveOne", { title })}
					className="pointer-events-none absolute top-1 right-1 bg-[var(--sb-hover)] opacity-0 focus-visible:pointer-events-auto focus-visible:opacity-100 group-hover/row:pointer-events-auto group-hover/row:opacity-100"
					onClick={() => {
						void patchThread(thread.id, { archived: true }).then(() => {
							if (pathname === to) navigate("/assistant", { replace: true });
						});
					}}
				/>
			)}
		</div>
	);
}

/** 已归档的助手对话：`GET threads` 缺省只列工作线程，这里显式带 `kind=assistant`。 */
function ArchivedConversations({ now }: { now: number }): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const archived = useQuery({
		queryKey: ["threads", { archived: true, kind: "assistant" }],
		enabled: open,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/threads", {
					params: { query: { archived: true, kind: "assistant" } },
				}),
			),
	});
	const list = archived.data?.threads.filter((th) => th.kind === "assistant");
	return (
		<Group
			fold
			title={t("sidebar.archived")}
			icon={<GroupIcon name="archive" />}
			open={open}
			onOpenChange={setOpen}
		>
			{list?.map((th) => (
				<ConversationRow key={th.id} thread={th} now={now} archivable={false} />
			))}
			{list?.length === 0 && (
				<p className="px-3 py-1 text-faint text-xs">{t("sidebar.empty")}</p>
			)}
		</Group>
	);
}

export function AssistantPane(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const conversations = useAssistantConversations();
	const status = useCoordinatorStatus();
	const now = useNow();
	const [error, setError] = useState<unknown>(null);
	const [creating, setCreating] = useState(false);
	const unavailable = status.data?.available === false;
	const create = () => {
		setCreating(true);
		setError(null);
		createAssistantConversation().then(
			(th) => {
				setCreating(false);
				navigate(`/assistant/${th.id}`);
				closeOnCompact();
			},
			(err: unknown) => {
				setCreating(false);
				setError(err);
			},
		);
	};
	return (
		<div
			data-testid="sidebar-assistant"
			className="min-h-0 flex-1 overflow-y-auto px-2 pt-1 pb-4"
		>
			<button
				type="button"
				className={`${LIST_ROW} disabled:opacity-50 disabled:hover:bg-transparent`}
				disabled={unavailable || creating}
				onClick={create}
			>
				<span className="grid h-[22px] w-[22px] shrink-0 place-items-center">
					<Icon name="plus" size={16} aria-hidden className="text-muted" />
				</span>
				<span className="min-w-0 flex-1 truncate">{t("assistant.new")}</span>
			</button>
			{error !== null && (
				<p role="alert" className="px-2.5 pb-1 text-err text-xs">
					{t("assistant.createFailed")} · {describeError(error, t)}
				</p>
			)}
			{unavailable && (
				<div className="px-1 pt-1 pb-2">
					<Callout tone="warn" title={t("coordinator.unavailableTitle")}>
						{status.data?.reason}
					</Callout>
				</div>
			)}
			<div className="mt-1 mb-2 flex flex-col">
				{conversations.map((th) => (
					<ConversationRow key={th.id} thread={th} now={now} archivable />
				))}
				{conversations.length === 0 && (
					<p className="px-3 py-2 text-faint text-sm">
						{t("assistantPane.empty")}
					</p>
				)}
			</div>
			<ArchivedConversations now={now} />
		</div>
	);
}
