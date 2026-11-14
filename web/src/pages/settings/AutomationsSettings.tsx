/**
 * 设置 → 自动化（M9）：
 * - 「定时任务」：全部定时，分「项目定时」（每次新建线程）与「线程内定时」两段；每行名称 / 线程、目标、表达式、下次运行、
 *   最近一次状态、`由 agent 创建`、启停与菜单。右上「新建」是项目定时（项目可选）。`?schedule=<id>` 直接打开该定时的
 *   对话框（侧栏折叠行的「全部运行」、失败通知的链接），`?project=<id>` 只列该项目的项目定时。
 * - 「操作记录」：协调者与 orb 内 agent 的操作（审计，新的在前）——时间、发起方、动作、目标线程、`自动` 徽标、失败的错误码；
 *   「加载更多」每次再取 50 条。
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
import { Badge, Card } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import { Button, ErrorText, Spinner } from "../../components/ui/primitives";
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

function SchedulesCard(): ReactNode {
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
	const list = all.data?.schedules ?? [];
	const projectRuns = list.filter(
		(s) =>
			s.mode === "new_thread" &&
			(projectFilter === undefined ||
				(projectFilter === "none"
					? !s.projectId
					: s.projectId === projectFilter)),
	);
	const threadRuns =
		projectFilter === undefined ? list.filter((s) => s.mode === "thread") : [];
	const filterName =
		projectFilter === "none"
			? t("sidebar.noProject")
			: projects.data?.projects?.find((p) => p.id === projectFilter)?.name;
	return (
		<Card
			title={t("automation.schedules")}
			icon="clock"
			action={
				<Button
					size="sm"
					icon="plus"
					onClick={() => setEditing({ kind: "new" })}
				>
					{t("common.create")}
				</Button>
			}
		>
			{projectFilter !== undefined && (
				<p className="mb-2 flex items-center gap-2 text-muted text-xs">
					{t("automation.filteredBy", { name: filterName ?? projectFilter })}
					<Link
						to="/settings/automations"
						className="text-accent-ink underline"
					>
						{t("automation.showAll")}
					</Link>
				</p>
			)}
			{all.isPending && <Spinner />}
			<ErrorText error={all.error} />
			<section data-testid="automation-project-schedules" className="mb-3">
				<h4 className="mb-1 font-medium text-faint text-xs">
					{t("automation.projectSchedules")}
				</h4>
				{projectRuns.length === 0 ? (
					<p className="text-faint text-xs">{t("automation.none")}</p>
				) : (
					<ul className="-mx-2 flex flex-col">
						{projectRuns.map((s) => (
							<ScheduleRow
								key={s.id}
								schedule={s}
								showTarget
								onEdit={() => setEditing({ kind: "edit", schedule: s })}
							/>
						))}
					</ul>
				)}
			</section>
			{projectFilter === undefined && (
				<section data-testid="automation-thread-schedules">
					<h4 className="mb-1 font-medium text-faint text-xs">
						{t("automation.threadSchedules")}
					</h4>
					{threadRuns.length === 0 ? (
						<p className="text-faint text-xs">{t("automation.none")}</p>
					) : (
						<ul className="-mx-2 flex flex-col">
							{threadRuns.map((s) => (
								<ScheduleRow
									key={s.id}
									schedule={s}
									showTarget
									onEdit={() => setEditing({ kind: "edit", schedule: s })}
								/>
							))}
						</ul>
					)}
				</section>
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
		</Card>
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

function ActionsCard(): ReactNode {
	const { t } = useTranslation();
	const [limit, setLimit] = useState(PAGE);
	const actions = useAutomationActions({ limit });
	const list = actions.data?.actions ?? [];
	return (
		<Card title={t("automation.log")} icon="list">
			<p className="mb-2 text-faint text-xs">{t("automation.logHint")}</p>
			{actions.isPending && <Spinner />}
			<ErrorText error={actions.error} />
			{!actions.isPending && list.length === 0 && (
				<p className="text-faint text-xs">{t("automation.logEmpty")}</p>
			)}
			<ul data-testid="automation-actions" className="flex flex-col">
				{list.map((a) => (
					<li
						key={a.id}
						data-action={a.id}
						className="flex flex-col gap-0.5 border-border border-b py-2 text-xs last:border-b-0"
					>
						<span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
							<span className="text-faint tabular-nums">
								{formatDateTime(a.ts)}
							</span>
							<Actor a={a} />
							<span className="font-medium text-fg">
								{t(`automation.action.${a.action}`)}
							</span>
							{a.targetThreadId && (
								<>
									<Icon
										name="forward"
										size={11}
										aria-hidden
										className="text-faint"
									/>
									<Target id={a.targetThreadId} />
								</>
							)}
							{a.origin === "auto" && (
								<Badge icon="bolt" title={t("automation.autoHint")}>
									{t("automation.auto")}
								</Badge>
							)}
							{!a.ok && (
								<Badge tone="err">{a.error ?? t("automation.failed")}</Badge>
							)}
						</span>
						{a.summary && (
							<span className="min-w-0 break-words text-muted">
								{a.summary}
							</span>
						)}
					</li>
				))}
			</ul>
			{list.length >= limit && (
				<div className="mt-2">
					<Button
						size="sm"
						disabled={actions.isFetching}
						onClick={() => setLimit((n) => n + PAGE)}
					>
						{t("automation.loadMore")}
					</Button>
				</div>
			)}
		</Card>
	);
}

export default function AutomationsSettings(): ReactNode {
	return (
		<>
			<SchedulesCard />
			<ActionsCard />
		</>
	);
}
