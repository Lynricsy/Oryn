/**
 * 项目设置「环境」（M24）：默认环境与脚本表单、最近一次 setup 的结果（环境配置卡）、快照与预热。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Project } from "../../../api/client";
import { useOrbs } from "../../../api/queries";
import { Badge, Card, Stat } from "../../../components/ui/data";
import { Icon } from "../../../components/ui/icons";
import { formatDuration, formatRelative } from "../../../lib/format";
import { AccelCard } from "../ProjectAccel";
import { formOf, ProjectEditor, RepoScriptText } from "../ProjectEditor";
import { latestSetupOrb, type SetupOrb } from "../project-orbs";

/** 最近一次 setup：结果、耗时、触发方式、完成时间、错误、仓库内脚本与所在线程。 */
function EnvironmentCard({ orb }: { orb: SetupOrb | undefined }): ReactNode {
	const { t } = useTranslation();
	const run = orb?.lastSetup;
	const thread = orb?.threadIds[0];
	return (
		<Card title={t("projectsPage.environment")} icon="wrench">
			{!run ? (
				<p data-testid="project-env-empty" className="text-faint text-sm">
					{t("projectsPage.noSetupRun")}
				</p>
			) : (
				<div
					data-testid="project-env"
					className="flex flex-col gap-2 text-muted text-xs"
				>
					<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
						{run.ok ? (
							<Badge tone={run.setupRan ? "ok" : "neutral"} icon="checkCircle">
								{t(run.setupRan ? "env.ok" : "env.skipped")}
							</Badge>
						) : (
							<Badge tone="err" icon="xCircle">
								{t("env.failed")}
							</Badge>
						)}
						<Stat icon="timer" label={t("stat.duration")}>
							{formatDuration(run.durationMs)}
						</Stat>
						<span className="inline-flex items-center gap-1">
							<Icon
								name={run.trigger === "manual" ? "user" : "bolt"}
								size={12}
								aria-hidden
							/>
							{t(`env.trigger.${run.trigger}`)}
						</span>
						<span className="inline-flex items-center gap-1 tabular-nums">
							<Icon name="clock" size={12} aria-hidden />
							{formatRelative(run.finishedAt)}
						</span>
					</div>
					{run.error && <p className="break-words text-err">{run.error}</p>}
					<div className="flex flex-wrap items-center justify-between gap-2">
						<span>
							<RepoScriptText run={run} />
						</span>
						{thread && (
							<Link
								to={`/t/${thread}`}
								className="inline-flex items-center gap-1 text-accent-ink underline"
							>
								{t("projectsPage.openThread")}
							</Link>
						)}
					</div>
				</div>
			)}
		</Card>
	);
}

export function EnvironmentTab({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const orbs = useOrbs().data?.orbs ?? [];
	const setupOrb = latestSetupOrb(
		orbs.filter((o) => o.projectId === project.id),
	);
	// 取消编辑 = 换 key 让表单还原为服务端值
	const [rev, setRev] = useState(0);
	return (
		<>
			<Card title={t("projectSettings.envForm")} icon="settings">
				<ProjectEditor
					key={`${project.updatedAt}:${rev}`}
					section="environment"
					initial={formOf(project)}
					id={project.id}
					lastSetup={setupOrb?.lastSetup}
					onSaved={() => undefined}
					onCancel={() => setRev((r) => r + 1)}
				/>
			</Card>
			<EnvironmentCard orb={setupOrb} />
			<AccelCard project={project} />
		</>
	);
}
