/**
 * 仓库区块不可用时的一条说明（M24 阶段 ②）：没有仓库地址、控制面没有 git、克隆失败、尚未克隆（归档项目不自动克隆）、
 * 克隆中、仓库是空的。主页与仓库各子页在镜像不能读时只显示它，其余功能照常。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Project } from "../../../api/client";
import type { RepoStatus } from "../../../api/repo-queries";
import { Card } from "../../../components/ui/data";
import { Button, EmptyState, Spinner } from "../../../components/ui/primitives";
import { repoLabel } from "../repo-label";
import { useRepoSyncAction } from "./RepoSyncStatus";

function SyncButton({
	projectId,
	status,
}: {
	projectId: string;
	status: RepoStatus;
}): ReactNode {
	const { t } = useTranslation();
	const action = useRepoSyncAction(projectId, status);
	return (
		<Button
			variant="primary"
			size="sm"
			icon="refresh"
			className="mt-2"
			disabled={action.busy}
			onClick={action.sync}
		>
			{t("projectRepo.sync")}
		</Button>
	);
}

export function RepoUnavailable({
	project,
	status,
}: {
	project: Project;
	status: RepoStatus;
}): ReactNode {
	const { t } = useTranslation();
	let body: ReactNode;
	if (status.kind === "none")
		body = (
			<EmptyState
				icon="branch"
				title={t("projectRepo.unavailable.noRepoTitle")}
			>
				<p>{t("projectRepo.unavailable.noRepoBody")}</p>
				<Link
					to={`/projects/${project.id}/settings/overview`}
					className="mt-2 inline-block text-accent-ink hover:underline"
				>
					{t("projectRepo.unavailable.openSettings")}
				</Link>
			</EmptyState>
		);
	else if (!status.available)
		body = (
			<EmptyState
				icon="alert"
				title={t(
					status.unavailableReason === "git_too_old"
						? "projectRepo.status.gitTooOld"
						: "projectRepo.status.gitMissing",
				)}
			>
				{t("projectRepo.unavailable.gitBody")}
			</EmptyState>
		);
	else if (status.state === "error") {
		const code = status.lastError?.code ?? "fetch_failed";
		const auth = code === "auth_required" || code === "auth_failed";
		body = (
			<EmptyState icon="alert" title={t("projectRepo.unavailable.errorTitle")}>
				{/* auth_required 的原因文案本身就是「需要凭据：…」，与下一步提示重复，只留提示 */}
				{code !== "auth_required" && <p>{t(`projectRepo.errors.${code}`)}</p>}
				{auth && (
					<p>
						{t("projectRepo.unavailable.authHint", {
							host: repoLabel(project.repoUrl ?? "").split("/")[0] || "<host>",
						})}
					</p>
				)}
				<SyncButton projectId={project.id} status={status} />
			</EmptyState>
		);
	} else if (status.state === "cloning")
		body = (
			<EmptyState
				icon="download"
				title={
					<span className="inline-flex items-center gap-2">
						<Spinner size={12} />
						{t("projectRepo.status.cloning")}
					</span>
				}
			>
				{t("projectRepo.unavailable.cloningBody")}
			</EmptyState>
		);
	else if (status.state === "absent")
		// 未归档项目打开页面时服务端会自动开始克隆，这里只是短暂状态；归档项目要手动同步
		body = (
			<EmptyState
				icon="download"
				title={t("projectRepo.unavailable.absentTitle")}
			>
				{project.archivedAt && <p>{t("projectRepo.unavailable.absentBody")}</p>}
				<SyncButton projectId={project.id} status={status} />
			</EmptyState>
		);
	else
		body = (
			<EmptyState icon="commit" title={t("projectRepo.status.empty")}>
				{t("projectRepo.unavailable.emptyBody")}
			</EmptyState>
		);
	return (
		<div data-testid="repo-unavailable">
			<Card>{body}</Card>
		</div>
	);
}
