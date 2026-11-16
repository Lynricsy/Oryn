/**
 * artifact 的对话框（计划 M14 2.5）：重命名（标题按码点计不超过 120）、永久删除确认、导入（选项目）。
 * 普通删除不弹框：直接移到「最近删除」，toast 带「撤销」（见 `ArtifactMenu`）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type Artifact,
	useImportArtifact,
	usePurgeArtifact,
	useRenameArtifact,
} from "../../api/artifact-queries";
import { ApiError } from "../../api/client";
import { useProjects } from "../../api/queries";
import { showToast } from "../../state/toasts";
import {
	Button,
	Dialog,
	describeError,
	ErrorText,
	Field,
	Select,
	TextInput,
} from "../ui/primitives";
import { importEntries, isImportErrorCode } from "./artifacts-model";

/** 标题上限（码点，与服务端 `MaxTitleRunes` 一致）。 */
export const MAX_TITLE_RUNES = 120;

export function RenameDialog({
	artifact,
	onClose,
}: {
	artifact: Pick<Artifact, "id" | "title">;
	onClose(): void;
}): ReactNode {
	const { t } = useTranslation();
	const rename = useRenameArtifact();
	const [title, setTitle] = useState(artifact.title);
	const trimmed = title.trim();
	const runes = [...trimmed].length;
	const tooLong = runes > MAX_TITLE_RUNES;
	const canSave =
		trimmed !== "" &&
		!tooLong &&
		trimmed !== artifact.title &&
		!rename.isPending;
	const submit = async () => {
		if (!canSave) return;
		try {
			await rename.mutateAsync({ id: artifact.id, title: trimmed });
			onClose();
		} catch {
			// 错误显示在对话框里（`rename.error`）
		}
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("artifacts.dialogs.renameTitle")}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						disabled={!canSave}
						onClick={() => void submit()}
					>
						{t("artifacts.dialogs.renameSave")}
					</Button>
				</>
			}
		>
			<form
				className="flex flex-col gap-3"
				onSubmit={(e) => {
					e.preventDefault();
					void submit();
				}}
			>
				<Field
					label={t("artifacts.dialogs.renameLabel")}
					hint={
						tooLong ? (
							<span className="text-err">
								{t("artifacts.dialogs.renameTooLong", {
									max: MAX_TITLE_RUNES,
									n: runes,
								})}
							</span>
						) : undefined
					}
				>
					<TextInput
						autoFocus
						value={title}
						aria-invalid={tooLong}
						onChange={(e) => setTitle(e.target.value)}
						onFocus={(e) => e.currentTarget.select()}
					/>
				</Field>
				<ErrorText error={rename.error} />
			</form>
		</Dialog>
	);
}

export function PurgeDialog({
	artifact,
	onClose,
}: {
	artifact: Pick<Artifact, "id" | "title" | "version">;
	onClose(): void;
}): ReactNode {
	const { t } = useTranslation();
	const purge = usePurgeArtifact();
	const submit = async () => {
		try {
			await purge.mutateAsync(artifact.id);
			onClose();
			showToast({
				text: t("artifacts.toast.purged", { title: artifact.title }),
			});
		} catch {
			// 错误显示在对话框里
		}
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("artifacts.dialogs.purgeTitle", { title: artifact.title })}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="danger"
						disabled={purge.isPending}
						onClick={() => void submit()}
					>
						{t("artifacts.dialogs.purgeConfirm")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-3 text-muted text-sm">
				<p>{t("artifacts.dialogs.purgeBody", { count: artifact.version })}</p>
				<ErrorText error={purge.error} />
			</div>
		</Dialog>
	);
}

/**
 * 导入：选好文件（单个 HTML 或一个文件夹）之后选导入到哪个项目或「无项目」。每个文件以「去掉顶层文件夹名的相对路径」
 * 为字段名放进 `FormData`；失败时按错误码给出对应文案，对话框留着可以换个项目或取消。
 */
export function ImportDialog({
	files,
	defaultProjectId,
	onClose,
	onImported,
}: {
	files: readonly File[];
	defaultProjectId?: string;
	onClose(): void;
	onImported(a: Artifact): void;
}): ReactNode {
	const { t } = useTranslation();
	const projects = useProjects();
	const run = useImportArtifact();
	const [projectId, setProjectId] = useState(defaultProjectId ?? "");
	const { entries, name } = importEntries(files);
	const submit = async () => {
		const form = new FormData();
		for (const e of entries) form.append(e.path, e.file, e.file.name);
		try {
			const a = await run.mutateAsync({
				files: form,
				name,
				...(projectId ? { projectId } : {}),
			});
			onClose();
			onImported(a);
		} catch {
			// 错误显示在对话框里
		}
	};
	const err = run.error;
	const reason =
		err instanceof ApiError && isImportErrorCode(err.code)
			? t(`artifacts.import.errors.${err.code}`)
			: err
				? describeError(err, t)
				: null;
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("artifacts.import.title")}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						disabled={run.isPending || entries.length === 0}
						onClick={() => void submit()}
					>
						{t("artifacts.import.submit")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-3">
				<p className="text-muted text-sm">
					{t("artifacts.import.summary", { name, n: entries.length })}
				</p>
				<Field label={t("artifacts.import.project")}>
					<Select
						value={projectId}
						data-testid="import-project"
						onChange={(e) => setProjectId(e.target.value)}
					>
						<option value="">{t("artifacts.page.noProject")}</option>
						{(projects.data?.projects ?? []).map((p) => (
							<option key={p.id} value={p.id}>
								{p.name}
							</option>
						))}
					</Select>
				</Field>
				{reason && (
					<p
						role="alert"
						data-testid="import-error"
						className="text-err text-sm"
					>
						{t("artifacts.import.failed", { reason })}
					</p>
				)}
			</div>
		</Dialog>
	);
}
