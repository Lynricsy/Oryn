/**
 * 定时列表的一行（设置 → 自动化、项目设置里的「定时任务」、Space 页的「定时」小节共用）：
 * 名称或线程标题、可选的目标（项目徽标 / 线程链接）、表达式摘要、下次运行、最近一次状态、`由 agent 创建` 徽标、
 * 启停开关与菜单（编辑 / 立即运行 / 运行记录 / 删除）。点名称 = 编辑（对话框由调用方渲染）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Schemas } from "../../api/client";
import { useProjects } from "../../api/queries";
import {
	type Schedule,
	useDeleteSchedule,
	useRunSchedule,
	useSetScheduleEnabled,
} from "../../api/schedule-queries";
import { formatRelative } from "../../lib/format";
import { describeSchedule } from "../../lib/schedule";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";
import { Badge, ProjectBadge, type Tone } from "../ui/data";
import { Icon } from "../ui/icons";
import { describeError, Menu, Toggle } from "../ui/primitives";

const LAST_TONE: Record<Schemas["ScheduleRunStatus"], Tone> = {
	pending: "neutral",
	running: "accent",
	needs_input: "accent",
	completed: "ok",
	failed: "err",
	skipped: "neutral",
};

/** 定时的显示名：有名字用名字；线程内定时无名时用目标线程的标题。 */
export function useScheduleTitle(s: Schedule): string {
	const { t } = useTranslation();
	const thread = useIndex((st) =>
		s.threadId ? st.threads[s.threadId] : undefined,
	);
	if (s.name.trim()) return s.name.trim();
	if (thread) return parseTitleCard(thread.title).text || t("thread.untitled");
	return s.threadId ?? t("sidebar.scheduledRuns");
}

export function ScheduleRow({
	schedule: s,
	onEdit,
	showTarget = false,
}: {
	schedule: Schedule;
	onEdit: () => void;
	/** 显示目标（项目徽标或线程链接）：全局列表用 */
	showTarget?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const title = useScheduleTitle(s);
	const projects = useProjects();
	const setEnabled = useSetScheduleEnabled();
	const run = useRunSchedule();
	const del = useDeleteSchedule();
	const project = s.projectId
		? projects.data?.projects?.find((p) => p.id === s.projectId)
		: undefined;
	const error = setEnabled.error ?? run.error ?? del.error;
	return (
		<li
			data-schedule={s.id}
			className="flex min-h-11 items-center gap-3 rounded-md px-2 py-1.5"
		>
			<span className="flex min-w-0 flex-1 flex-col gap-0.5">
				<span className="flex min-w-0 items-center gap-1.5 text-sm">
					<Icon
						name={s.mode === "new_thread" ? "repeat" : "clock"}
						size={14}
						aria-hidden
						className="shrink-0 text-faint"
					/>
					<button
						type="button"
						onClick={onEdit}
						className="min-w-0 truncate text-left font-medium hover:underline"
					>
						{title}
					</button>
					{s.createdBy === "agent" && (
						<Badge icon="robot">{t("automation.byAgent")}</Badge>
					)}
				</span>
				<span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-faint text-xs">
					{showTarget &&
						(s.mode === "new_thread" ? (
							<span className="inline-flex min-w-0 items-center gap-1">
								{project ? (
									<ProjectBadge
										name={project.name}
										icon={project.icon}
										size={14}
									/>
								) : (
									<Icon name="folder" size={12} aria-hidden />
								)}
								<span className="truncate">
									{project?.name ?? t("sidebar.noProject")}
								</span>
							</span>
						) : (
							s.threadId && (
								<Link
									to={`/t/${s.threadId}`}
									className="inline-flex min-w-0 items-center gap-1 text-accent-ink hover:underline"
								>
									<Icon name="message" size={12} aria-hidden />
									<span className="truncate">{t("automation.openThread")}</span>
								</Link>
							)
						))}
					<span>{describeSchedule(s, t)}</span>
					<span>
						{s.nextRunAt !== undefined
							? t("automation.nextRun", { when: formatRelative(s.nextRunAt) })
							: s.pausedReason
								? t(`schedule.pausedReason.${s.pausedReason}`)
								: t("schedule.paused")}
					</span>
					{s.lastRun && (
						<Badge tone={LAST_TONE[s.lastRun.status]}>
							{t(`schedule.runStatus.${s.lastRun.status}`)}
						</Badge>
					)}
					{error ? (
						<span className="text-err">{describeError(error, t)}</span>
					) : null}
				</span>
			</span>
			<Toggle
				checked={s.enabled}
				disabled={setEnabled.isPending}
				onChange={(v) => setEnabled.mutate({ id: s.id, enabled: v })}
				label={
					<span className="sr-only">
						{t("automation.toggle", { name: title })}
					</span>
				}
			/>
			<Menu
				items={[
					{
						key: "edit",
						icon: "edit",
						label: t("common.edit"),
						onSelect: onEdit,
					},
					{
						key: "run",
						icon: "play",
						label: t("schedule.runNow"),
						onSelect: () => run.mutate(s.id),
					},
					{
						key: "runs",
						icon: "list",
						label: t("automation.runs"),
						onSelect: onEdit,
					},
					{
						key: "delete",
						icon: "trash",
						danger: true,
						label: t("common.delete"),
						onSelect: () => {
							if (window.confirm(t("schedule.deleteConfirm"))) del.mutate(s.id);
						},
					},
				]}
				trigger={(p) => (
					<button
						type="button"
						aria-label={t("automation.menu", { name: title })}
						className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted transition-colors duration-150 hover:bg-hover hover:text-fg"
						{...p}
					>
						<Icon name="more" size={16} />
					</button>
				)}
			/>
		</li>
	);
}

/** 按定时的模式打开对应的对话框目标。 */
export function editTarget(
	s: Schedule,
):
	| { mode: "thread"; threadId: string }
	| { mode: "new_thread"; projectId?: string | null } {
	return s.mode === "thread" && s.threadId
		? { mode: "thread", threadId: s.threadId }
		: { mode: "new_thread", projectId: s.projectId ?? null };
}
