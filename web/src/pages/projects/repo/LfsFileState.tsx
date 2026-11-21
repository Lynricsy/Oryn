/**
 * 仓库文件页里不能预览的 Git LFS 文件（M24 阶段 ② 补充）：取对象失败（原因 + 重试）、或超过预览上限（只能下载）。
 * 两种情况都不渲染 `FileViewer`：媒体类型会拿下载地址内联播放，超大对象会被服务端以 413 拒绝。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	LFS_PREVIEW_MAX_BYTES,
	type RepoTreeEntry,
	repoBlobUrl,
} from "../../../api/repo-queries";
import { Card } from "../../../components/ui/data";
import { Button, EmptyState } from "../../../components/ui/primitives";
import { downloadUrl } from "../../../lib/artifacts/download";
import { formatBytes } from "../../../lib/format";

export function LfsFileState({
	kind,
	projectId,
	refName,
	path,
	entry,
	onRetry,
}: {
	kind: "error" | "large";
	projectId: string;
	refName: string;
	path: string;
	entry: RepoTreeEntry;
	onRetry: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const code = entry.lfs?.error;
	if (kind === "error")
		return (
			<Card>
				<div data-testid="repo-lfs-error">
					<EmptyState icon="alert" title={t("projectRepo.lfs.errorTitle")}>
						<div className="flex flex-col items-center gap-3">
							{code && <p>{t(`projectRepo.lfs.errors.${code}`)}</p>}
							<Button icon="refresh" size="sm" onClick={onRetry}>
								{t("projectRepo.lfs.retry")}
							</Button>
						</div>
					</EmptyState>
				</div>
			</Card>
		);
	return (
		<Card>
			<div data-testid="repo-lfs-large">
				<EmptyState icon="download" title={t("projectRepo.lfs.largeTitle")}>
					<div className="flex flex-col items-center gap-3">
						<p>
							{t("projectRepo.lfs.largeBody", {
								max: formatBytes(LFS_PREVIEW_MAX_BYTES),
							})}
						</p>
						<Button
							icon="download"
							size="sm"
							onClick={() =>
								downloadUrl(
									repoBlobUrl(projectId, refName, path, { download: true }),
									entry.name,
								)
							}
						>
							{t("projectRepo.lfs.download", { size: formatBytes(entry.size) })}
						</Button>
					</div>
				</EmptyState>
			</div>
		</Card>
	);
}
