/**
 * 「转为 artifact」（计划 M31 6.4）：把文件面板或文件库里的一个 Office 文件下载成 Blob，以原文件名为字段名交给导入接口，
 * 得到一次性快照的新 artifact。超过 256 MiB 的不下载、直接报错；旧版与 OpenDocument 格式由服务端回 `unsupported_format` 的说明。
 */
import { useTranslation } from "react-i18next";
import { type Artifact, useImportArtifact } from "../../api/artifact-queries";
import { rawFetch } from "../../api/client";
import { MAX_OFFICE_FILE_BYTES } from "../../lib/artifacts/office/formats";
import { formatBytes } from "../../lib/format";

export interface FileToArtifact {
	/** 文件的下载地址（来源的 `downloadUrl`） */
	url: string | null;
	name: string;
	size: number;
	threadId?: string;
	projectId?: string;
}

export function useFileToArtifact(): (f: FileToArtifact) => Promise<Artifact> {
	const { t } = useTranslation();
	const importArtifact = useImportArtifact();
	return async ({ url, name, size, threadId, projectId }) => {
		if (size > MAX_OFFICE_FILE_BYTES)
			throw new Error(
				t("files.toArtifactTooLarge", {
					max: formatBytes(MAX_OFFICE_FILE_BYTES),
				}),
			);
		if (!url) throw new Error(t("files.toArtifactUnavailable"));
		const blob = await (await rawFetch(url)).blob();
		const form = new FormData();
		form.append(name, blob, name);
		return importArtifact.mutateAsync({
			files: form,
			...(threadId ? { threadId } : {}),
			...(projectId ? { projectId } : {}),
		});
	};
}
