/**
 * 「导出」分组（R7、R22）：触发 bank `oryn` 的异步导出，轮询进度，完成后给下载链接。
 * 整库定时备份见部署文档（`hindsight-admin backup` 或 `pg_dump`），这里只做按需导出。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	memoryExportDownloadUrl,
	useCreateMemoryExport,
	useMemoryExport,
} from "../../../../api/memory-queries";
import { Callout } from "../../../../components/ui/data";
import { Button, Spinner } from "../../../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupRow,
} from "../../../../components/ui/workbench";
import { formatRelative } from "../../../../lib/format";
import { MemoryError } from "./errors";
import { MEMORY_DEPLOY_DOC_URL, secToMs } from "./shared";

export function ExportCard(): ReactNode {
	const { t } = useTranslation();
	const create = useCreateMemoryExport();
	const [id, setId] = useState<string>();
	const q = useMemoryExport(id);
	const ex = q.data;
	const running = create.isPending || ex?.state === "running";
	return (
		<Group title={t("memoryPage.export.title")}>
			<GroupList>
				<GroupRow
					icon="download"
					title={t("memoryPage.export.rowTitle")}
					meta={
						<span>
							{t("memoryPage.export.hint")}{" "}
							<a
								href={MEMORY_DEPLOY_DOC_URL}
								target="_blank"
								rel="noopener"
								className="text-accent-ink underline"
							>
								{t("memoryPage.export.backupDoc")}
							</a>
						</span>
					}
					trailing={
						<>
							{running && (
								<span className="inline-flex items-center gap-1.5 text-muted text-xs">
									<Spinner size={12} />
									{t("memoryPage.export.running")}
								</span>
							)}
							{ex?.state === "done" && (
								<a
									href={memoryExportDownloadUrl(ex.id)}
									download
									data-testid="memory-export-download"
									className="font-medium text-accent-ink text-sm underline"
								>
									{t("memoryPage.export.download", {
										when: formatRelative(secToMs(ex.createdAt)),
									})}
								</a>
							)}
							<Button
								size="sm"
								icon="export"
								disabled={running}
								onClick={() =>
									create.mutate(undefined, { onSuccess: (d) => setId(d.id) })
								}
							>
								{t("memoryPage.export.start")}
							</Button>
						</>
					}
				/>
			</GroupList>
			{ex?.state === "failed" && (
				<Callout tone="err" role="alert" title={t("memoryPage.export.failed")}>
					{ex.error && (
						<p className="break-words">
							{t("memoryPage.status.detail", { detail: ex.error })}
						</p>
					)}
					<p>{t("memoryPage.export.failedNext")}</p>
				</Callout>
			)}
			<MemoryError step="createExport" error={create.error} />
			<MemoryError step="loadExport" error={q.error} />
		</Group>
	);
}
