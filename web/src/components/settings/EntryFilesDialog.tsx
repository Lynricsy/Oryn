/**
 * 目录式条目（技能目录、扩展 / 工具子目录）的文件管理：左侧列出目录下全部文件，右侧编辑选中的文本文件。
 * - 新建：填相对路径（可含子目录，如 `scripts/run.sh`），保存时创建；
 * - 上传：选择文件、选择文件夹或拖入（文件夹保留目录层级）；与已有文件同名时先确认覆盖，整批一次请求；
 * - 删除：单个文件，确认后删除；主文件（SKILL.md、index.ts…）随条目一起删，这里不能单独删；
 * - 切换文件不丢草稿：每个文件的草稿与编辑起点各自保留，保存只写当前文件，带冲突检查。
 * 二进制或超过编辑上限的文件只显示大小，不能在这里编辑。
 */
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type AgentFile,
	isModifiedError,
	useAgentFileActions,
	useAgentFiles,
	useAgentText,
} from "../../api/agent-config-queries";
import {
	type BundleFile,
	cleanRelPath,
	filesFromList,
	toBatchFiles,
} from "../../lib/agent-bundle";
import { MAX_EDIT_BYTES } from "../../lib/file-source";
import { formatBytes } from "../../lib/format";
import { Callout } from "../ui/data";
import { Icon } from "../ui/icons";
import {
	Button,
	Dialog,
	IconButton,
	Spinner,
	TextInput,
} from "../ui/primitives";
import { AgentErrorText, CodeField, EditConflict } from "./agent-config-ui";
import { DropArea, type PickedFile, usePickers } from "./DropArea";

/** 一个文件的草稿：编辑起点（null = 新建）与当前文本。 */
interface Draft {
	base: string | null;
	text: string;
}

export function EntryFilesDialog({
	title,
	root,
	main,
	locked,
	onClose,
}: {
	title: string;
	/** 条目目录（相对配置根，不带末尾 `/`），如 `skills/foo` */
	root: string;
	/** 默认选中的文件（完整路径） */
	main: string;
	/** 让 Core 认出这个条目的入口文件（SKILL.md、index.ts…）：随条目一起删，这里不能单独删 */
	locked?: string;
	onClose(): void;
}) {
	const { t } = useTranslation();
	const files = useAgentFiles();
	const actions = useAgentFileActions();
	const prefix = `${root}/`;
	const existing = useMemo(
		() =>
			new Map<string, AgentFile>(
				(files.data ?? [])
					.filter((f) => f.path.startsWith(prefix))
					.map((f) => [f.path, f]),
			),
		[files.data, prefix],
	);
	/** 新建、尚未保存的文件（完整路径） */
	const [pending, setPending] = useState<string[]>([]);
	const [selected, setSelected] = useState(main);
	const [drafts, setDrafts] = useState<Record<string, Draft>>({});
	const [newPath, setNewPath] = useState("");
	const [newError, setNewError] = useState<string | null>(null);
	const [deleting, setDeleting] = useState<string | null>(null);
	const [overwrite, setOverwrite] = useState<{
		files: BundleFile[];
		collisions: string[];
	} | null>(null);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);

	const listed = [...new Set([...existing.keys(), ...pending, main])].sort(
		(a, b) => a.localeCompare(b),
	);
	const draft = drafts[selected];
	const dirty = (p: string) => {
		const d = drafts[p];
		return d !== undefined && d.text !== d.base;
	};

	const dropDraft = (paths: readonly string[]) =>
		setDrafts((prev) => {
			const next = { ...prev };
			for (const p of paths) delete next[p];
			return next;
		});

	const run = async (job: () => Promise<void>) => {
		setBusy(true);
		setError(null);
		try {
			await job();
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};

	const save = () =>
		void run(async () => {
			if (!draft || busy) return;
			await actions.write(selected, draft.text, draft.base);
			dropDraft([selected]);
			setPending((prev) => prev.filter((p) => p !== selected));
		});

	const upload = (bundle: BundleFile[]) =>
		void run(async () => {
			setOverwrite(null);
			const batch = toBatchFiles(root, bundle);
			await actions.apply({ files: batch });
			const written = batch.map((f) => f.path);
			// 覆盖掉的文件：旧草稿的起点已过期，丢弃；新建未保存的同名项转为已存在
			dropDraft(written);
			setPending((prev) => prev.filter((p) => !written.includes(p)));
		});

	const ingest = async (picked: PickedFile[]) => {
		setError(null);
		const bundle = await filesFromList(picked);
		if (bundle.length === 0) return;
		const collisions = bundle
			.map((f) => `${prefix}${f.path}`)
			.filter((p) => existing.has(p));
		if (collisions.length > 0) setOverwrite({ files: bundle, collisions });
		else upload(bundle);
	};
	const pickers = usePickers((list) => void ingest(list));

	const remove = (path: string) =>
		void run(async () => {
			setDeleting(null);
			if (existing.has(path)) await actions.remove([path]);
			setPending((prev) => prev.filter((p) => p !== path));
			dropDraft([path]);
			if (selected === path) setSelected(main);
		});

	const addFile = () => {
		const rel = cleanRelPath(newPath.trim());
		if (!rel) {
			setNewError(t("agentPages.files.pathInvalid"));
			return;
		}
		const full = `${prefix}${rel}`;
		// 同一路径不能既是文件又是目录
		if (
			listed.some(
				(p) =>
					p === full || p.startsWith(`${full}/`) || full.startsWith(`${p}/`),
			)
		) {
			setNewError(t("agentPages.files.pathTaken"));
			return;
		}
		setNewError(null);
		setNewPath("");
		setPending((prev) => [...prev, full]);
		setDrafts((prev) => ({ ...prev, [full]: { base: null, text: "" } }));
		setSelected(full);
	};

	const selectedInfo = existing.get(selected);
	const isNew = !selectedInfo && pending.includes(selected);

	return (
		<Dialog
			open
			size="xl"
			onClose={onClose}
			title={t("agentPages.files.title", { name: title })}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.close")}
					</Button>
					<Button
						variant="primary"
						disabled={busy || !dirty(selected)}
						onClick={save}
					>
						{busy && <Spinner size={12} />}
						{t("common.save")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-3">
				{overwrite && (
					<Callout
						tone="warn"
						actions={
							<>
								<Button
									size="sm"
									variant="danger"
									onClick={() => upload(overwrite.files)}
								>
									{t("agentPages.files.overwriteAction")}
								</Button>
								<Button
									size="sm"
									variant="ghost"
									onClick={() => setOverwrite(null)}
								>
									{t("common.cancel")}
								</Button>
							</>
						}
					>
						{t("agentPages.files.overwrite", {
							count: overwrite.collisions.length,
							paths: overwrite.collisions
								.map((p) => p.slice(prefix.length))
								.join("、"),
						})}
					</Callout>
				)}
				{deleting && (
					<Callout
						tone="warn"
						actions={
							<>
								<Button
									size="sm"
									variant="danger"
									onClick={() => remove(deleting)}
								>
									{t("common.delete")}
								</Button>
								<Button
									size="sm"
									variant="ghost"
									onClick={() => setDeleting(null)}
								>
									{t("common.cancel")}
								</Button>
							</>
						}
					>
						{t("agentPages.files.confirmDelete", {
							path: deleting.slice(prefix.length),
						})}
					</Callout>
				)}
				<div className="flex flex-col gap-3 md:flex-row">
					<DropArea
						label={t("agentPages.files.listLabel")}
						onFiles={(list) => void ingest(list)}
						disabled={busy}
						className="flex flex-col gap-2 rounded-md border border-border border-dashed p-2 md:w-72 md:shrink-0"
						activeClassName="border-accent bg-hover"
					>
						<div className="flex flex-wrap gap-1.5">
							<Button
								size="sm"
								icon="upload"
								disabled={busy}
								onClick={pickers.pickFiles}
							>
								{t("agentPages.files.uploadFiles")}
							</Button>
							<Button
								size="sm"
								icon="folderPlus"
								disabled={busy}
								onClick={pickers.pickFolder}
							>
								{t("agentPages.files.uploadFolder")}
							</Button>
						</div>
						{pickers.inputs}
						<form
							className="flex gap-1.5"
							onSubmit={(e) => {
								e.preventDefault();
								addFile();
							}}
						>
							<TextInput
								value={newPath}
								aria-label={t("agentPages.files.newFile")}
								placeholder={t("agentPages.files.newPathPlaceholder")}
								aria-invalid={newError !== null}
								onChange={(e) => {
									setNewPath(e.target.value);
									setNewError(null);
								}}
							/>
							<Button size="sm" icon="filePlus" type="submit">
								{t("agentPages.files.add")}
							</Button>
						</form>
						{newError && <p className="text-err text-xs">{newError}</p>}
						<ul className="flex max-h-[22rem] flex-col overflow-y-auto">
							{listed.map((p) => (
								<FileRow
									key={p}
									rel={p.slice(prefix.length)}
									size={existing.get(p)?.size}
									selected={p === selected}
									isMain={p === locked}
									dirty={dirty(p)}
									onSelect={() => setSelected(p)}
									onDelete={() => setDeleting(p)}
								/>
							))}
						</ul>
						<p className="px-1 text-[12px] text-faint leading-normal">
							{t("agentPages.files.dropHint")}
						</p>
					</DropArea>
					<div className="flex min-w-0 flex-1 flex-col gap-2">
						<div className="flex items-center gap-1.5 font-[550] text-[13px] text-fg">
							<Icon name="fileCode" size={13} aria-hidden />
							<span className="truncate font-mono">{selected}</span>
						</div>
						<FilePane
							key={selected}
							path={selected}
							size={selectedInfo?.size}
							isNew={isNew}
							draft={draft}
							onDraft={(d) => setDrafts((prev) => ({ ...prev, [selected]: d }))}
							onSave={save}
						/>
					</div>
				</div>
				{isModifiedError(error) ? (
					<EditConflict onReload={() => dropDraft([selected])} />
				) : (
					<AgentErrorText error={error} />
				)}
			</div>
		</Dialog>
	);
}

function FileRow({
	rel,
	size,
	selected,
	isMain,
	dirty,
	onSelect,
	onDelete,
}: {
	rel: string;
	/** undefined = 新建未保存 */
	size: number | undefined;
	selected: boolean;
	isMain: boolean;
	dirty: boolean;
	onSelect(): void;
	onDelete(): void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<li
			className={`group/file flex items-center gap-1 rounded-[9px] pr-1 ${selected ? "bg-active" : "hover:bg-hover"}`}
		>
			<button
				type="button"
				aria-current={selected ? "true" : undefined}
				className="flex min-w-0 flex-1 items-center gap-1.5 px-2 py-1.5 text-left text-[12.5px]"
				onClick={onSelect}
			>
				<span className="min-w-0 truncate font-mono text-fg">{rel}</span>
				{dirty && (
					<span
						className="h-1.5 w-1.5 shrink-0 rounded-full bg-accent"
						title={t("agentPages.files.unsaved")}
					/>
				)}
				<span className="ml-auto shrink-0 text-faint text-xs">
					{size === undefined
						? t("agentPages.files.unsaved")
						: formatBytes(size)}
				</span>
			</button>
			<IconButton
				icon="trash"
				box="xs"
				size={13}
				label={
					isMain
						? t("agentPages.files.mainLocked")
						: t("agentPages.files.deleteFile", { path: rel })
				}
				disabled={isMain}
				onClick={onDelete}
			/>
		</li>
	);
}

/** 选中文件的编辑区：文本进编辑器；二进制、过大的只给说明。新建的文件从空白开始。 */
function FilePane({
	path,
	size,
	isNew,
	draft,
	onDraft,
	onSave,
}: {
	path: string;
	size: number | undefined;
	isNew: boolean;
	draft: Draft | undefined;
	onDraft(d: Draft): void;
	onSave(): void;
}): ReactNode {
	const { t } = useTranslation();
	const tooLarge = size !== undefined && size > MAX_EDIT_BYTES;
	const q = useAgentText(path, !isNew && !tooLarge && draft === undefined);
	if (tooLarge)
		return (
			<Callout tone="neutral" icon="info">
				{t("agentPages.files.tooLarge", { size: formatBytes(size) })}
			</Callout>
		);
	const base = draft ? draft.base : isNew ? null : q.data;
	if (base === undefined) {
		if (q.error) return <AgentErrorText error={q.error} />;
		return <Spinner />;
	}
	// 解码出 NUL 或替换字符：按二进制处理，不进编辑器（保存会损坏原文件）
	if (base !== null && (base.includes("\u0000") || base.includes("\uFFFD")))
		return (
			<Callout tone="neutral" icon="info">
				{t("agentPages.files.binary", { size: formatBytes(size ?? 0) })}
			</Callout>
		);
	return (
		<CodeField
			tall
			value={draft?.text ?? base ?? ""}
			path={path}
			onChange={(text) => onDraft({ base, text })}
			onSave={onSave}
		/>
	);
}
