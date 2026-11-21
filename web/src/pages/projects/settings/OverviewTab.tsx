/**
 * 项目设置「概览」（M24）：基本信息（名称、描述、仓库、图标、基线分支、git 提交身份）、仓库卡（有仓库地址时是
 * 「仓库镜像」：大小与同步时间、立即同步；托管或没有仓库时是「托管仓库」：克隆地址与推送后 Webhook，或创建托管仓库）
 * 与危险操作（归档 / 取消归档、删除；托管项目的说明写明仓库只读 / 一并删除）。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { api, type Project, unwrap } from "../../../api/client";
import { qk } from "../../../api/queries";
import { useRepoStatus } from "../../../api/repo-queries";
import { useAsync } from "../../../components/settings/useAsync";
import { Card } from "../../../components/ui/data";
import { Button, ErrorText } from "../../../components/ui/primitives";
import { confirmAction } from "../../../lib/confirm";
import { formatBytes, formatRelative } from "../../../lib/format";
import { RepoSyncText, useRepoSyncAction } from "../home/RepoSyncStatus";
import { formOf, ProjectEditor } from "../ProjectEditor";
import { HostedRepoCard } from "./HostedRepoCard";

/** 危险操作的一行：标题与说明在左，按钮在右（窄屏换行）。 */
function DangerRow({
	title,
	hint,
	children,
}: {
	title: string;
	hint: string;
	children: ReactNode;
}): ReactNode {
	return (
		<div className="flex flex-wrap items-center gap-x-4 gap-y-2 py-2">
			<div className="flex min-w-0 flex-1 basis-60 flex-col gap-0.5">
				<span className="font-medium text-sm">{title}</span>
				<span className="text-faint text-xs">{hint}</span>
			</div>
			{children}
		</div>
	);
}

function DangerCard({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const navigate = useNavigate();
	const archive = useAsync();
	const del = useAsync();
	const deleteHint = project.hosted
		? "projectSettings.deleteHintHosted"
		: "projectSettings.deleteHint";
	const setArchived = (archived: boolean) =>
		void archive.run(async () => {
			const next = await unwrap(
				api.PATCH("/api/v1/projects/{id}", {
					params: { path: { id: project.id } },
					body: { archived },
				}),
			);
			// 归档后项目离开未归档列表：先把单取的缓存填上，页面不会在列表刷新期间闪回加载
			qc.setQueryData(qk.project(project.id), next);
			await qc.invalidateQueries({ queryKey: qk.projects });
		});
	return (
		<div data-testid="project-danger">
			<Card title={t("projectSettings.danger")} icon="alert">
				<div className="flex flex-col divide-y divide-border">
					{project.archivedAt ? (
						<DangerRow
							title={t("projectShell.unarchive")}
							hint={t("projectSettings.unarchiveHint")}
						>
							<Button
								size="sm"
								icon="archive"
								disabled={archive.busy}
								onClick={() => setArchived(false)}
							>
								{t("projectShell.unarchive")}
							</Button>
						</DangerRow>
					) : (
						<DangerRow
							title={t("projectSettings.archive")}
							hint={t(
								project.hosted
									? "projectSettings.archiveHintHosted"
									: "projectSettings.archiveHint",
							)}
						>
							<Button
								size="sm"
								icon="archive"
								disabled={archive.busy}
								onClick={async () => {
									if (
										await confirmAction({
											title: t("projectSettings.archiveConfirm", {
												name: project.name,
											}),
											body: t(
												project.hosted
													? "projectSettings.archiveConfirmBodyHosted"
													: "projectSettings.archiveConfirmBody",
											),
											action: t("common.archive"),
										})
									)
										setArchived(true);
								}}
							>
								{t("common.archive")}
							</Button>
						</DangerRow>
					)}
					<DangerRow title={t("projectSettings.delete")} hint={t(deleteHint)}>
						<Button
							size="sm"
							variant="danger"
							icon="trash"
							disabled={del.busy}
							onClick={async () => {
								if (
									!(await confirmAction({
										title: t("projects.deleteConfirm", { name: project.name }),
										body: t(deleteHint),
										action: t("common.delete"),
										danger: true,
									}))
								)
									return;
								const ok = await del.run(async () => {
									await unwrap(
										api.DELETE("/api/v1/projects/{id}", {
											params: { path: { id: project.id } },
										}),
									);
								});
								if (!ok) return;
								// 先离开本页再刷新列表：否则列表刷新后这里会短暂显示「项目不存在」
								navigate("/projects", { replace: true });
								void qc.invalidateQueries({ queryKey: qk.projects });
							}}
						>
							{t("common.delete")}
						</Button>
					</DangerRow>
				</div>
				<ErrorText error={archive.error ?? del.error} />
			</Card>
		</div>
	);
}

/** 「仓库镜像」卡：就绪时写镜像大小与同步时间，其余状态同主页仓库行的文字；右侧「立即同步」。 */
function RepoMirrorCard({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const { data: status, error } = useRepoStatus(project.id);
	const action = useRepoSyncAction(project.id, status);
	return (
		<div data-testid="project-repo-mirror">
			<Card title={t("projectRepo.settings.title")} icon="branch">
				<div className="flex flex-wrap items-center gap-x-4 gap-y-2 py-1">
					<span className="min-w-0 flex-1 basis-60 text-muted text-sm">
						{status?.state === "ready" &&
						!status.syncing &&
						status.syncedAt !== undefined ? (
							t("projectRepo.settings.mirror", {
								size: formatBytes(status.sizeBytes ?? 0),
								ago: formatRelative(status.syncedAt),
							})
						) : status ? (
							<RepoSyncText status={status} />
						) : null}
					</span>
					{action.syncable && (
						<Button
							size="sm"
							icon="refresh"
							disabled={action.busy}
							onClick={action.sync}
						>
							{t("projectRepo.sync")}
						</Button>
					)}
				</div>
				{!status && <ErrorText error={error} />}
			</Card>
		</div>
	);
}

export function OverviewTab({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	// 取消编辑 = 换 key 让表单还原为服务端值
	const [rev, setRev] = useState(0);
	return (
		<>
			<Card title={t("projectSettings.general")} icon="settings">
				<ProjectEditor
					key={`${project.updatedAt}:${rev}`}
					section="overview"
					initial={formOf(project)}
					id={project.id}
					onSaved={() => undefined}
					onCancel={() => setRev((r) => r + 1)}
				/>
			</Card>
			{project.repoUrl ? (
				<RepoMirrorCard project={project} />
			) : (
				<HostedRepoCard project={project} />
			)}
			<DangerCard project={project} />
		</>
	);
}
