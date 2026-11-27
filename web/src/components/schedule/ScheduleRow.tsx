/**
 * 定时列表的一行（设置 → 自动化、项目设置里的「定时任务」、Space 页的「定时」小节共用），D 的 `GroupRow`，放在 `GroupList` 里：
 * 标题 = 名称或线程标题（点击 = 编辑，对话框由调用方渲染）与 `由 agent 创建` 徽标；
 * 说明 = 可选的目标（项目徽标 / 线程链接）、表达式摘要、下次运行（「手动 / Webhook」启用时不显示）、入站 Webhook 标记、
 * 运行前闸门摘要（主机 + 命令首行）；
 * 行尾 = 最近一次状态（定宽状态列）· ⋯（编辑 / 立即运行 / 运行记录 / 删除）· 启停开关。出错原文在行下方。
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
import { confirmAction } from "../../lib/confirm";
import { formatRelative } from "../../lib/format";
import { describeSchedule, gateCommandSummary } from "../../lib/schedule";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";
import { Badge, ProjectBadge, type Tone } from "../ui/data";
import { Icon } from "../ui/icons";
import { ErrorText, IconButton, Menu, Toggle } from "../ui/primitives";
import { GroupRow, StatusCell, StatusText } from "../ui/workbench";

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
		<GroupRow
			icon={s.mode === "new_thread" ? "repeat" : "clock"}
			title={
				<span
					data-schedule={s.id}
					className="flex min-w-0 items-center gap-1.5"
				>
					<button
						type="button"
						onClick={onEdit}
						className="min-w-0 truncate text-left hover:underline"
					>
						{title}
					</button>
					{s.createdBy === "agent" && (
						<Badge icon="robot">{t("automation.byAgent")}</Badge>
					)}
				</span>
			}
			meta={
				<>
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
					{/* 「手动 / Webhook」没有下次运行：启用时摘要已说明，不再显示「已暂停」 */}
					{!(s.kind === "manual" && s.enabled) && (
						<span>
							{s.nextRunAt !== undefined
								? t("automation.nextRun", { when: formatRelative(s.nextRunAt) })
								: s.pausedReason
									? t(`schedule.pausedReason.${s.pausedReason}`)
									: t("schedule.paused")}
						</span>
					)}
					{s.webhook && (
						<span
							data-testid="schedule-webhook-summary"
							className="inline-flex min-w-0 items-center gap-1"
						>
							<Icon name="webhook" size={12} aria-hidden />
							{t("schedule.webhook.badge")}
						</span>
					)}
					{s.gate && (
						<span
							data-testid="schedule-gate-summary"
							title={s.gate.command}
							className="inline-flex min-w-0 items-center gap-1"
						>
							<Icon name="funnel" size={12} aria-hidden />
							<span className="truncate">
								{t("schedule.gate.summary", {
									// 没有 host = 在控制面本机的沙箱里执行
									host: s.gate.host ?? t("schedule.gate.localShort"),
									command: gateCommandSummary(s.gate.command),
								})}
							</span>
						</span>
					)}
				</>
			}
			trailing={
				<>
					<StatusCell>
						{s.lastRun && (
							<StatusText tone={LAST_TONE[s.lastRun.status]}>
								{t(`schedule.runStatus.${s.lastRun.status}`)}
							</StatusText>
						)}
					</StatusCell>
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
								onSelect: async () => {
									if (
										await confirmAction({
											title: t("schedule.deleteConfirm"),
											body: t("schedule.deleteConfirmBody"),
											action: t("common.delete"),
											danger: true,
										})
									)
										del.mutate(s.id);
								},
							},
						]}
						trigger={(p) => (
							<IconButton
								icon="more"
								box="sm"
								label={t("automation.menu", { name: title })}
								{...p}
							/>
						)}
					/>
					<Toggle
						hideLabel
						checked={s.enabled}
						disabled={setEnabled.isPending}
						onChange={(v) => setEnabled.mutate({ id: s.id, enabled: v })}
						label={t("automation.toggle", { name: title })}
					/>
				</>
			}
		>
			{error ? <ErrorText error={error} /> : undefined}
		</GroupRow>
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
