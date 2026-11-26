/**
 * 设置 → 自动化（M9），D 分组列表：
 * - 「项目定时（每次新建线程）」「线程内定时」两组，每行是 `ScheduleRow`（名称 / 线程、目标、表达式、下次运行、
 *   最近一次状态、`由 agent 创建`、⋯ 菜单与启停）。项目定时组右上「新建」（项目可选）。`?schedule=<id>` 直接打开该定时的
 *   对话框（侧栏折叠行的「全部运行」、失败通知的链接），`?project=<id>` 只列该项目的项目定时（不显示线程内定时）。
 * - 「操作记录」：协调者与 orb 内 agent 的操作（审计，新的在前）——动作、`自动` 徽标、失败的错误码 · 时间、发起方、目标线程，
 *   摘要在行下方；「加载更多」每次再取 50 条。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useSearchParams } from "react-router";
import {
	type AutomationAction,
	useAutomationActions,
} from "../../api/automation-queries";
import { useProjects } from "../../api/queries";
import {
	type Schedule,
	useSchedule,
	useSchedules,
} from "../../api/schedule-queries";
import { useSpaces } from "../../api/space-queries";
import { ScheduleDialog } from "../../components/schedule/ScheduleDialog";
import { editTarget, ScheduleRow } from "../../components/schedule/ScheduleRow";
import { Badge } from "../../components/ui/data";
import type { IconName } from "../../components/ui/icons";
import { Button, ErrorText, Spinner } from "../../components/ui/primitives";
import { Group, GroupList, GroupRow } from "../../components/ui/workbench";
import { formatDateTime } from "../../lib/format";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";

const PAGE = 50;

type Editing =
	| { kind: "new" }
	| { kind: "edit"; schedule: Schedule }
	| { kind: "byId"; id: string };

/** `?schedule=<id>`：先取到定义再按模式打开对话框。 */
function DialogById({ id, onClose }: { id: string; onClose: () => void }) {
	const one = useSchedule(id);
	if (one.isPending) return null;
	if (!one.data) return <ErrorText error={one.error} />;
	return (
		<ScheduleDialog
			scheduleId={id}
			target={editTarget(one.data)}
			onClose={onClose}
		/>
	);
}

/** 一组定时：空时一句说明。外层 `data-testid` 给测试与验收脚本定位。 */
function ScheduleGroup({
	testId,
	title,
	meta,
	action,
	schedules,
	onEdit,
	children,
}: {
	testId: string;
	title: string;
	meta?: ReactNode;
	action?: ReactNode;
	schedules: readonly Schedule[];
	onEdit: (s: Schedule) => void;
	children?: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div data-testid={testId} className="contents">
			<Group title={title} meta={meta} action={action}>
				{children}
				{schedules.length === 0 ? (
					<p className="px-1.5 text-faint text-sm">{t("automation.none")}</p>
				) : (
					<GroupList>
						{schedules.map((s) => (
							<ScheduleRow
								key={s.id}
								schedule={s}
								showTarget
								onEdit={() => onEdit(s)}
							/>
						))}
					</GroupList>
				)}
			</Group>
		</div>
	);
}

function Schedules(): ReactNode {
	const { t } = useTranslation();
	const [params, setParams] = useSearchParams();
	const projectFilter = params.get("project") ?? undefined;
	const deepLink = params.get("schedule");
	const projects = useProjects();
	const all = useSchedules({});
	const [editing, setEditing] = useState<Editing | null>(null);
	useEffect(() => {
		if (deepLink) setEditing({ kind: "byId", id: deepLink });
	}, [deepLink]);
	const close = () => {
		setEditing(null);
		if (deepLink) {
			const next = new URLSearchParams(params);
			next.delete("schedule");
			setParams(next, { replace: true });
		}
	};
	const edit = (s: Schedule) => setEditing({ kind: "edit", schedule: s });
	const list = all.data?.schedules ?? [];
	const projectRuns = list.filter(
		(s) =>
			s.mode === "new_thread" &&
			(projectFilter === undefined ||
				(projectFilter === "none"
					? !s.projectId
					: s.projectId === projectFilter)),
	);
	const threadRuns = list.filter((s) => s.mode === "thread");
	const filterName =
		projectFilter === "none"
			? t("sidebar.noProject")
			: projects.data?.projects?.find((p) => p.id === projectFilter)?.name;
	return (
		<>
			<ScheduleGroup
				testId="automation-project-schedules"
				title={t("automation.projectSchedules")}
				meta={
					projectFilter !== undefined && (
						<span className="inline-flex items-center gap-2">
							{t("automation.filteredBy", {
								name: filterName ?? projectFilter,
							})}
							<Link
								to="/settings/automations"
								className="text-accent-ink underline-offset-2 hover:underline"
							>
								{t("automation.showAll")}
							</Link>
						</span>
					)
				}
				action={
					<Button
						size="sm"
						icon="plus"
						onClick={() => setEditing({ kind: "new" })}
					>
						{t("common.create")}
					</Button>
				}
				schedules={all.isSuccess ? projectRuns : []}
				onEdit={edit}
			>
				{all.isPending && <Spinner />}
				<ErrorText error={all.error} />
			</ScheduleGroup>
			{projectFilter === undefined && all.isSuccess && (
				<ScheduleGroup
					testId="automation-thread-schedules"
					title={t("automation.threadSchedules")}
					schedules={threadRuns}
					onEdit={edit}
				/>
			)}
			{editing?.kind === "new" && (
				<ScheduleDialog
					target={{
						mode: "new_thread",
						projectId:
							projectFilter && projectFilter !== "none" ? projectFilter : null,
					}}
					onClose={close}
				/>
			)}
			{editing?.kind === "edit" && (
				<ScheduleDialog
					scheduleId={editing.schedule.id}
					target={editTarget(editing.schedule)}
					onClose={close}
				/>
			)}
			{editing?.kind === "byId" && (
				<DialogById id={editing.id} onClose={close} />
			)}
		</>
	);
}

/** 一条操作记录的发起方：Space 名 / 助手对话标题 / 线程标题，带链接。 */
function Actor({ a }: { a: AutomationAction }): ReactNode {
	const { t } = useTranslation();
	const spaces = useSpaces();
	const thread = useIndex((s) =>
		a.actorThreadId ? s.threads[a.actorThreadId] : undefined,
	);
	const title = thread
		? parseTitleCard(thread.title).text || t("thread.untitled")
		: a.actorThreadId;
	if (a.actorKind === "space") {
		const space = spaces.data?.spaces?.find(
			(s) => s.threadId === a.actorThreadId,
		);
		return space ? (
			<Link
				to={`/space/${space.id}`}
				className="text-accent-ink hover:underline"
			>
				{t("automation.actor.space", { name: space.name })}
			</Link>
		) : (
			<span>{t("automation.actor.space", { name: title ?? "?" })}</span>
		);
	}
	if (!a.actorThreadId)
		return <span>{t(`automation.actorKind.${a.actorKind}`)}</span>;
	const to =
		a.actorKind === "assistant"
			? `/assistant/${a.actorThreadId}`
			: `/t/${a.actorThreadId}`;
	return (
		<Link to={to} className="text-accent-ink hover:underline">
			{a.actorKind === "assistant"
				? t("automation.actor.assistant", { name: title })
				: t("automation.actor.agent", { name: title })}
		</Link>
	);
}

function Target({ id }: { id: string }): ReactNode {
	const { t } = useTranslation();
	const thread = useIndex((s) => s.threads[id]);
	return (
		<Link
			to={`/t/${id}`}
			className="min-w-0 truncate text-accent-ink hover:underline"
		>
			{thread ? parseTitleCard(thread.title).text || t("thread.untitled") : id}
		</Link>
	);
}

const ACTOR_ICON: Record<AutomationAction["actorKind"], IconName> = {
	space: "chats",
	assistant: "globe",
	agent: "robot",
};

function ActionRow({ a }: { a: AutomationAction }): ReactNode {
	const { t } = useTranslation();
	return (
		<GroupRow
			icon={ACTOR_ICON[a.actorKind]}
			title={
				<span data-action={a.id} className="flex min-w-0 items-center gap-1.5">
					<span className="truncate">{t(`automation.action.${a.action}`)}</span>
					{a.origin === "auto" && (
						<Badge icon="bolt" title={t("automation.autoHint")}>
							{t("automation.auto")}
						</Badge>
					)}
					{!a.ok && (
						<Badge tone="err">{a.error ?? t("automation.failed")}</Badge>
					)}
				</span>
			}
			meta={
				<>
					<span className="tabular-nums">{formatDateTime(a.ts)}</span>
					<span aria-hidden>·</span>
					<Actor a={a} />
					{a.targetThreadId && (
						<>
							<span aria-hidden>→</span>
							<Target id={a.targetThreadId} />
						</>
					)}
				</>
			}
		>
			{a.summary && (
				<p className="min-w-0 break-words text-[12.5px] text-muted">
					{a.summary}
				</p>
			)}
		</GroupRow>
	);
}

function ActionsLog(): ReactNode {
	const { t } = useTranslation();
	const [limit, setLimit] = useState(PAGE);
	const actions = useAutomationActions({ limit });
	const list = actions.data?.actions ?? [];
	return (
		<div data-testid="automation-actions" className="contents">
			<Group title={t("automation.log")}>
				{actions.isPending && <Spinner />}
				<ErrorText error={actions.error} />
				{list.length > 0 && (
					<GroupList>
						{list.map((a) => (
							<ActionRow key={a.id} a={a} />
						))}
					</GroupList>
				)}
				{list.length >= limit && (
					<div>
						<Button
							size="sm"
							disabled={actions.isFetching}
							onClick={() => setLimit((n) => n + PAGE)}
						>
							{t("automation.loadMore")}
						</Button>
					</div>
				)}
				<p className="px-1.5 text-faint text-xs">
					{!actions.isPending && list.length === 0
						? `${t("automation.logEmpty")} `
						: ""}
					{t("automation.logHint")}
				</p>
			</Group>
		</div>
	);
}

export default function AutomationsSettings(): ReactNode {
	return (
		<>
			<Schedules />
			<ActionsLog />
		</>
	);
}
