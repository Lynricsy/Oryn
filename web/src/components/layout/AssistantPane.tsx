/**
 * 「助手」分区侧栏：全局助手的会话列表。不设分区标题行（底栏选中态已表明分区）。
 * 最上面是主线（M19）的置顶行：助手的头像与名字（没设名字用「全局助手」）+ 未读点或相对时间；索引加载完、协调宿主可用
 * 而索引里还没有主线时，调一次 `GET assistant/main` 让服务端建出来。其下是「新对话」（协调宿主不可用时禁用，下面一条警告）；
 * 会话行 = 状态图标 + 标题卡图标（有则显示）+ 标题 + 未读点或相对时间，悬停 / 聚焦时行尾出现归档按钮；末尾折叠组「已归档」展开才拉取。
 * 链接的 textContent 只等于标题（时间走 `data-time` 伪元素）。主线不在对话列表里。主线行下面是「助手的电脑」（M19，`/assistant/computer`）。
 * 主线行与各对话行（含已归档）都有右键菜单（`assistantRowGroups`）；归档一律走侧栏宿主的 `archive`，行归档后即卸载，不能用行自己的。
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { NavLink, useNavigate } from "react-router";
import { api, type Thread, unwrap } from "../../api/client";
import { useCoordinatorStatus } from "../../api/coordinator-queries";
import { useAssistantIdentity } from "../../api/identity-queries";
import { createAssistantConversation } from "../../api/space-queries";
import {
	archivedAssistantKey,
	ensureAssistantMain,
	useArchiveAssistantConversation,
	useAssistantConversations,
	useAssistantMain,
} from "../../lib/assistant-conversations";
import { useContextTarget } from "../../lib/context-menu/registry";
import { formatRelative } from "../../lib/format";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";
import { AssistantMark } from "../assistant/AssistantMark";
import { assistantRowGroups } from "../thread/thread-row-menu";
import { Callout } from "../ui/data";
import { Icon } from "../ui/icons";
import { describeError, IconButton, ThreadStateIcon } from "../ui/primitives";
import {
	CardGlyph,
	closeOnCompact,
	Group,
	GroupIcon,
	LIST_ROW,
	NAV_ROW,
	PANE_SCROLL,
	UnreadDot,
	useNow,
} from "./pane-parts";

type Archive = (threadId: string) => Promise<void>;

/** 对话行（含主线）的右键菜单登记，挂在行链接上。 */
function useRowMenu(thread: Thread, archive: Archive) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const qc = useQueryClient();
	return useContextTarget<HTMLAnchorElement>({
		items: () =>
			assistantRowGroups(thread, {
				navigate,
				t,
				archive,
				refreshArchived: () =>
					void qc.invalidateQueries({ queryKey: archivedAssistantKey }),
			}),
	});
}

function ConversationRow({
	thread,
	now,
	archive,
}: {
	thread: Thread;
	now: number;
	archive: Archive;
}): ReactNode {
	const { t } = useTranslation();
	const ctxRef = useRowMenu(thread, archive);
	// 已归档的行不出归档按钮（取消归档在右键菜单里）
	const archivable = !thread.archivedAt;
	const card = parseTitleCard(thread.title);
	const title = card.text || t("assistant.untitled");
	const to = `/assistant/${thread.id}`;
	const stamp = formatRelative(thread.lastMessageAt ?? thread.updatedAt, now);
	return (
		<div className="group/row relative">
			<NavLink
				ref={ctxRef}
				data-ctx-touch=""
				to={to}
				onClick={closeOnCompact}
				className={NAV_ROW}
			>
				<ThreadStateIcon state={thread.state} size={14} />
				{card.icon && <CardGlyph icon={card.icon} />}
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
					onClick={() => void archive(thread.id).catch(() => undefined)}
				/>
			)}
		</div>
	);
}

/** 主线的置顶行：助手的头像与名字，未读时是未读点，否则是最近活动的相对时间。 */
function MainRow({
	thread,
	now,
	archive,
}: {
	thread: Thread;
	now: number;
	archive: Archive;
}): ReactNode {
	const { t } = useTranslation();
	const { name } = useAssistantIdentity();
	const ctxRef = useRowMenu(thread, archive);
	const stamp = formatRelative(thread.lastMessageAt ?? thread.updatedAt, now);
	return (
		<NavLink
			ref={ctxRef}
			data-ctx-touch=""
			to={`/assistant/${thread.id}`}
			onClick={closeOnCompact}
			data-testid="assistant-main-row"
			className={NAV_ROW}
		>
			<span className="grid h-[22px] w-[22px] shrink-0 place-items-center">
				<AssistantMark size={20} />
			</span>
			<span
				className={`min-w-0 flex-1 truncate ${thread.unread ? "font-medium" : ""}`}
			>
				{name || t("tabBar.assistant")}
			</span>
			{thread.unread ? (
				<UnreadDot />
			) : (
				<span
					aria-hidden
					data-time={stamp}
					className="shrink-0 font-normal text-2xs text-faint tabular-nums after:content-[attr(data-time)]"
				/>
			)}
		</NavLink>
	);
}

/** 已归档的助手对话：`GET threads` 缺省只列工作线程，这里显式带 `kind=assistant`。 */
function ArchivedConversations({
	now,
	archive,
}: {
	now: number;
	archive: Archive;
}): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const archived = useQuery({
		queryKey: archivedAssistantKey,
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
				<ConversationRow key={th.id} thread={th} now={now} archive={archive} />
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
	const main = useAssistantMain();
	const loaded = useIndex((s) => s.loaded);
	// 宿主是常驻的侧栏 pane（行归档后即卸载）：完成时按最新路由判断是否回 landing
	const archive = useArchiveAssistantConversation();
	const status = useCoordinatorStatus();
	const now = useNow();
	const [error, setError] = useState<unknown>(null);
	const [creating, setCreating] = useState(false);
	const unavailable = status.data?.available === false;
	const available = status.data?.available === true;
	// 还没有主线时让服务端建一条（只试一次；失败时侧栏照常，只是没有主线行）
	const ensured = useRef(false);
	useEffect(() => {
		if (!loaded || main || !available || ensured.current) return;
		ensured.current = true;
		ensureAssistantMain().catch(() => undefined);
	}, [loaded, main, available]);
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
		<div data-testid="sidebar-assistant" className={PANE_SCROLL}>
			{main && <MainRow thread={main} now={now} archive={archive} />}
			<NavLink
				to="/assistant/computer"
				onClick={closeOnCompact}
				data-testid="assistant-computer-row"
				className={NAV_ROW}
			>
				<span className="grid h-[22px] w-[22px] shrink-0 place-items-center">
					<Icon name="laptop" size={16} aria-hidden className="text-muted" />
				</span>
				<span className="min-w-0 flex-1 truncate">
					{t("assistantPane.computer")}
				</span>
			</NavLink>
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
					<ConversationRow
						key={th.id}
						thread={th}
						now={now}
						archive={archive}
					/>
				))}
				{conversations.length === 0 && (
					<p className="px-3 py-2 text-faint text-sm">
						{t("assistantPane.empty")}
					</p>
				)}
			</div>
			<ArchivedConversations now={now} archive={archive} />
		</div>
	);
}
