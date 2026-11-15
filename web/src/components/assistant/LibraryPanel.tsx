/**
 * 全局助手文件库面板（M13 4.7，R24）：列出本对话的文件与库内共享的文件（可切到全部），每行显示名称、大小、
 * 来源（上传 / 助手保存 / orb）、可见范围标记、所属对话与来源线程、时间；可预览（复用 `FileViewer`，只读）、
 * 下载、切换共享 / 仅此对话、删除（确认后）。上传只走输入框的附件（R19），面板不另设上传。
 */
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { ApiError, rawFetch } from "../../api/client";
import {
	type AssistantFile,
	assistantFileContentUrl,
	useAssistantFiles,
	useDeleteAssistantFile,
	useSetAssistantFileScope,
} from "../../api/library-queries";
import { fileIcon } from "../../lib/file-icon";
import {
	type FileSource,
	MAX_EDIT_BYTES,
	MAX_IMAGE_PREVIEW_BYTES,
	readCapped,
} from "../../lib/file-source";
import { formatBytes, formatDateTime } from "../../lib/format";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";
import { FileViewer } from "../files/FileViewer";
import { Badge } from "../ui/data";
import { Icon } from "../ui/icons";
import {
	EmptyState,
	ErrorText,
	IconButton,
	Spinner,
	tabClass,
} from "../ui/primitives";

const TEXT_MIME =
	/^text\/|^application\/(json|xml|yaml|x-yaml|toml|javascript|x-sh|x-ndjson)$|\+json$|\+xml$/;

function readOnlyError(): Promise<never> {
	return Promise.reject(
		new ApiError(405, "read_only", "assistant library files are read-only"),
	);
}

/** 单个库文件的只读来源：`FileViewer` 以文件名为路径读取它（内容接口支持 Range）。 */
function libraryFileSource(file: AssistantFile): FileSource {
	const url = assistantFileContentUrl(file.id);
	// 服务端嗅探出的 MIME 带参数（`text/plain; charset=utf-8`）；预览按不带参数的类型分流
	const mime = file.mime.split(";")[0]?.trim().toLowerCase() ?? "";
	const entry = {
		path: file.name,
		name: file.name,
		type: "file" as const,
		size: file.size,
		mtime: file.createdAt,
		mode: 0o444,
		mime,
		text: TEXT_MIME.test(mime),
	};
	return {
		id: `library:${file.id}`,
		caps: { mkdir: false, rename: false, upload: false, download: true },
		roots: async () => [],
		list: async () => [],
		stat: async () => entry,
		async readText(_path, maxBytes = MAX_EDIT_BYTES) {
			let res: Response;
			try {
				res = await rawFetch(url, {
					headers: { Range: `bytes=0-${maxBytes - 1}` },
				});
			} catch (e) {
				// 空文件上的 Range 请求按 RFC 9110 回 416
				if (e instanceof ApiError && e.status === 416)
					return { text: "", truncated: false };
				throw e;
			}
			const { bytes, over } = await readCapped(res, maxBytes);
			const truncated = over || file.size > maxBytes;
			return {
				text: new TextDecoder("utf-8").decode(bytes, { stream: truncated }),
				truncated,
			};
		},
		async readBlob() {
			const { bytes, over } = await readCapped(
				await rawFetch(url),
				MAX_IMAGE_PREVIEW_BYTES,
			);
			if (over)
				throw new ApiError(413, "too_large", "file too large to preview");
			return new Blob([bytes]);
		},
		write: readOnlyError,
		mkdir: readOnlyError,
		rename: readOnlyError,
		remove: readOnlyError,
		downloadUrl: () => url,
	};
}

function download(file: AssistantFile) {
	const a = document.createElement("a");
	a.href = assistantFileContentUrl(file.id);
	a.download = file.name;
	a.click();
}

const SOURCE_ICON = {
	upload: "upload",
	assistant: "sparkle",
	orb: "box",
} as const;

/** 线程标题链接：助手对话进 `/assistant/<id>`，其它线程进 `/t/<id>`；索引里没有（已删除）时只显示 id 前缀。 */
function ThreadRef({
	threadId,
	assistant,
}: {
	threadId: string;
	assistant: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const th = useIndex((s) => s.threads[threadId]);
	const title = th
		? parseTitleCard(th.title).text || t("assistant.untitled")
		: threadId.slice(0, 12);
	return (
		<Link
			to={assistant ? `/assistant/${threadId}` : `/t/${threadId}`}
			className="min-w-0 max-w-48 truncate text-accent-ink hover:underline"
			title={title}
		>
			{title}
		</Link>
	);
}

function FileRow({
	file,
	threadId,
	busy,
	onPreview,
	onToggleShared,
	onDelete,
}: {
	file: AssistantFile;
	threadId: string;
	busy: boolean;
	onPreview(): void;
	onToggleShared(): void;
	onDelete(): void;
}): ReactNode {
	const { t } = useTranslation();
	const shared = file.scope === "shared";
	return (
		<li
			aria-label={file.name}
			className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 text-sm"
		>
			<span className="grid h-8 w-8 shrink-0 place-items-center rounded-md border border-border bg-inset text-faint">
				<Icon name={fileIcon(file.name, file.mime)} size={15} aria-hidden />
			</span>
			<span className="min-w-0 flex-1">
				<button
					type="button"
					onClick={onPreview}
					className="block max-w-full truncate text-left font-medium hover:underline"
					title={file.name}
				>
					{file.name}
				</button>
				<span className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-faint text-xs tabular-nums">
					<Badge>{formatBytes(file.size)}</Badge>
					<Badge icon={SOURCE_ICON[file.source]}>
						{t(`library.source.${file.source}`)}
					</Badge>
					<span data-testid="scope">
						<Badge icon={shared ? "share" : "lock"}>
							{t(
								shared ? "library.scope.shared" : "library.scope.conversation",
							)}
						</Badge>
					</span>
					<span>{formatDateTime(file.createdAt)}</span>
				</span>
				{(file.threadId || file.sourceThreadId) && (
					<span className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-faint text-xs">
						{file.threadId && (
							<span className="flex min-w-0 items-center gap-1">
								{t("library.conversation")}
								{file.threadId === threadId ? (
									<span className="text-muted">
										{t("library.thisConversation")}
									</span>
								) : (
									<ThreadRef threadId={file.threadId} assistant />
								)}
							</span>
						)}
						{file.sourceThreadId && (
							<span className="flex min-w-0 items-center gap-1">
								{t("library.sourceThread")}
								<ThreadRef threadId={file.sourceThreadId} assistant={false} />
							</span>
						)}
					</span>
				)}
			</span>
			<span className="ml-auto flex shrink-0 items-center gap-0.5">
				<IconButton
					icon="eye"
					label={t("library.preview")}
					onClick={onPreview}
				/>
				<IconButton
					icon="download"
					label={t("library.download")}
					onClick={() => download(file)}
				/>
				<IconButton
					icon="share"
					active={shared}
					disabled={busy}
					label={t(shared ? "library.makeConversation" : "library.makeShared")}
					onClick={onToggleShared}
				/>
				<IconButton
					icon="trash"
					disabled={busy}
					label={t("library.delete")}
					className="hover:text-err"
					onClick={onDelete}
				/>
			</span>
		</li>
	);
}

function Preview({
	file,
	onBack,
}: {
	file: AssistantFile;
	onBack(): void;
}): ReactNode {
	const source = useMemo(() => libraryFileSource(file), [file]);
	return (
		<div className="-mx-5 h-[60dvh] min-h-72 border-border border-y">
			<FileViewer
				source={source}
				path={file.name}
				readOnly
				onBack={onBack}
				onDownload={() => download(file)}
			/>
		</div>
	);
}

export function LibraryPanel({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const [showAll, setShowAll] = useState(false);
	const [previewing, setPreviewing] = useState<AssistantFile | null>(null);
	const q = useAssistantFiles(showAll ? undefined : threadId);
	const setScope = useSetAssistantFileScope();
	const remove = useDeleteAssistantFile();
	const pendingId = setScope.isPending
		? setScope.variables?.id
		: remove.isPending
			? remove.variables
			: undefined;
	if (previewing)
		return <Preview file={previewing} onBack={() => setPreviewing(null)} />;
	const list = q.data ?? [];
	return (
		<div className="flex flex-col gap-2">
			<div className="flex items-center gap-0.5">
				<button
					type="button"
					aria-pressed={!showAll}
					className={`${tabClass(!showAll)} ${!showAll ? "bg-hover" : ""}`}
					onClick={() => setShowAll(false)}
				>
					{t("library.filter.visible")}
				</button>
				<button
					type="button"
					aria-pressed={showAll}
					className={`${tabClass(showAll)} ${showAll ? "bg-hover" : ""}`}
					onClick={() => setShowAll(true)}
				>
					{t("library.filter.all")}
				</button>
			</div>
			{q.isPending && <Spinner />}
			<ErrorText error={q.error ?? setScope.error ?? remove.error} />
			{q.isSuccess && list.length === 0 && (
				<EmptyState icon="folder" title={t("library.empty")}>
					{t("library.hint")}
				</EmptyState>
			)}
			<ul className="flex flex-col divide-y divide-border">
				{list.map((f) => (
					<FileRow
						key={f.id}
						file={f}
						threadId={threadId}
						busy={pendingId === f.id}
						onPreview={() => setPreviewing(f)}
						onToggleShared={() =>
							setScope.mutate({
								id: f.id,
								scope: f.scope === "shared" ? "conversation" : "shared",
							})
						}
						onDelete={() => {
							if (window.confirm(t("library.deleteConfirm", { name: f.name })))
								remove.mutate(f.id);
						}}
					/>
				))}
			</ul>
			{list.length > 0 && (
				<p className="text-faint text-xs">{t("library.hint")}</p>
			)}
		</div>
	);
}
