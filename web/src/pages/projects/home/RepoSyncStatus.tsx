/**
 * 项目仓库镜像的同步状态（M24 阶段 ②）：主页仓库行末尾的一段文字（未克隆 / 克隆中 / 同步中 / 已同步多久前 / 同步失败，
 * git 不可用时说明原因）加「立即同步」图标钮。项目没有仓库地址时不渲染（仓库行本身已写「无仓库」）。
 * `RepoSyncText` 与 `useRepoSyncAction` 也给设置「仓库镜像」卡用。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Project } from "../../../api/client";
import {
	type RepoStatus,
	useRepoStatus,
	useSyncRepo,
} from "../../../api/repo-queries";
import { useNow } from "../../../components/layout/pane-parts";
import { Icon } from "../../../components/ui/icons";
import {
	describeError,
	IconButton,
	Spinner,
} from "../../../components/ui/primitives";
import { formatRelative } from "../../../lib/format";
import { showToast } from "../../../state/toasts";

/** 立即克隆或拉取；失败弹提示。`syncable` = 有仓库地址且控制面有 git；`busy` 含服务端正在克隆或拉取。 */
export function useRepoSyncAction(projectId: string, status?: RepoStatus) {
	const { t } = useTranslation();
	const m = useSyncRepo(projectId);
	return {
		syncable: !!status && status.kind !== "none" && status.available,
		busy:
			m.isPending ||
			status?.state === "cloning" ||
			(status?.state === "ready" && status.syncing),
		sync: () =>
			m.mutate(undefined, {
				onError: (e) => showToast({ text: describeError(e, t), tone: "err" }),
			}),
	};
}

/**
 * 状态的一行文字（行内元素，可放进 `<p>`），前置图标：失败是警示、进行中是转圈、其余是同步记录；没有仓库时为 null。
 * 不用分支图标：仓库行里紧挨着的基线分支已经用了它。
 */
export function RepoSyncText({ status }: { status: RepoStatus }): ReactNode {
	const { t } = useTranslation();
	const now = useNow();
	if (status.kind === "none") return null;
	const row = (lead: ReactNode, text: ReactNode, tone = "") => (
		<span className={`inline-flex min-w-0 items-center gap-1 ${tone}`}>
			{lead}
			<span className="min-w-0">{text}</span>
		</span>
	);
	const repeat = (
		<Icon name="repeat" size={12} aria-hidden className="shrink-0" />
	);
	if (!status.available)
		return row(
			repeat,
			t(
				status.unavailableReason === "git_too_old"
					? "projectRepo.status.gitTooOld"
					: "projectRepo.status.gitMissing",
			),
		);
	const reason = status.lastError
		? t(`projectRepo.errors.${status.lastError.code}`)
		: "";
	if (status.state === "error")
		return row(
			<Icon name="alert" size={12} aria-hidden className="shrink-0" />,
			t("projectRepo.status.failed", { reason }),
			"text-err",
		);
	if (status.state === "cloning")
		return row(<Spinner size={10} />, t("projectRepo.status.cloning"));
	if (status.state === "ready" && status.syncing)
		return row(<Spinner size={10} />, t("projectRepo.status.syncing"));
	if (status.state === "ready" && status.syncedAt !== undefined)
		return (
			<span className="inline-flex min-w-0 items-center gap-1">
				{repeat}
				<span className="min-w-0">
					{t("projectRepo.status.synced", {
						ago: formatRelative(status.syncedAt, now),
					})}
				</span>
				{status.lastError && (
					<span
						className="inline-flex shrink-0 text-warn"
						title={t("projectRepo.status.staleFailed", { reason })}
					>
						<Icon
							name="alert"
							size={12}
							aria-label={t("projectRepo.status.staleFailed", { reason })}
						/>
					</span>
				)}
			</span>
		);
	return row(repeat, t("projectRepo.status.absent"));
}

export function RepoSyncStatus({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const { data: status } = useRepoStatus(project.id, !!project.repoUrl);
	const action = useRepoSyncAction(project.id, status);
	if (!project.repoUrl || !status || status.kind === "none") return null;
	return (
		<span
			data-testid="repo-sync-status"
			className="inline-flex min-w-0 items-center gap-0.5"
		>
			<RepoSyncText status={status} />
			{action.syncable && (
				<IconButton
					icon="refresh"
					size={12}
					box="xs"
					label={t("projectRepo.sync")}
					disabled={action.busy}
					onClick={action.sync}
				/>
			)}
		</span>
	);
}
