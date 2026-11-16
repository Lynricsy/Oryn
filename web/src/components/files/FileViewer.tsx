/**
 * 文件预览 / 编辑：`stat` 后按 `previewKind`（扩展名优先，其次 MIME 与 `text` 嗅探）分流——
 * - 文本且 ≤2MiB：CodeMirror 编辑器（懒加载）；更大：只读显示前 256KiB；
 *   Markdown、CSV/TSV、HTML、Notebook、mermaid、SVG 另有「预览 / 源码」切换，缺省显示预览（有草稿时显示源码），
 *   预览跟随未保存的草稿；源码被截断时预览改读整份文件（≤64MiB）；
 * - 整份读入的预览（≤64MiB）：图片（含 HEIC / TIFF / PSD 本地解码）、PDF、Word、电子表格、演示文稿、字体、压缩包目录、3D 模型；
 * - 音视频：下载地址直接作 `<video>` / `<audio>` 的 src，按 Range 流式播放；
 * - 其它或超过上限：类型、大小与下载按钮。
 * 不可信内容的边界：HTML 类内容只进惰性 iframe（不执行脚本、不发请求，见 InertFrame）；图片只经 `<img>`；
 * 表格、压缩包目录等一律作为文本交给 React 转义。
 * 保存带 `etag`；412 `modified` → 对话框「覆盖 / 重新加载」。只读（`readOnly`，如停机 orb 的预览）时不可编辑、保存或丢弃草稿。
 * 读取遇到唤醒超时或停机预览的索引构建中时按 `withWakeRetry` 等待重试。
 * 草稿存在 `drafts` store（按来源 + 路径），换文件、关面板、换线程回来后仍在；
 * 保存期间继续编辑时，保存完成只把基线前移，不覆盖较新的草稿。
 */
import {
	lazy,
	type ReactNode,
	Suspense,
	useCallback,
	useEffect,
	useMemo,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "../../api/client";
import { fileIcon } from "../../lib/file-icon";
import {
	type FileEntry,
	type FileSource,
	MAX_EDIT_BYTES,
	MAX_PREVIEW_BLOB_BYTES,
	PREVIEW_BYTES,
} from "../../lib/file-source";
import { formatBytes, formatDateTime } from "../../lib/format";
import {
	type BlobViewer,
	previewKind,
	type TextRender,
} from "../../lib/preview-kind";
import { Markdown } from "../markdown/Markdown";
import { Badge } from "../ui/data";
import { Icon } from "../ui/icons";
import {
	Button,
	Dialog,
	ErrorText,
	IconButton,
	Spinner,
	tabClass,
} from "../ui/primitives";
import {
	discardDraft,
	draftKey,
	editDraft,
	markSaved,
	useDraft,
} from "./drafts";
import { type RetryWait, withWakeRetry } from "./hooks";
import { InertFrame } from "./preview/InertFrame";
import MediaPreview from "./preview/MediaPreview";

const CodeEditor = lazy(() => import("../editor/CodeEditor"));
const ImagePreview = lazy(() => import("./preview/ImagePreview"));
const PdfPreview = lazy(() => import("./preview/PdfPreview"));
const DocxPreview = lazy(() => import("./preview/DocxPreview"));
const SheetPreview = lazy(() => import("./preview/SheetPreview"));
const SlidesPreview = lazy(() => import("./preview/SlidesPreview"));
const FontPreview = lazy(() => import("./preview/FontPreview"));
const ArchivePreview = lazy(() => import("./preview/ArchivePreview"));
const ModelPreview = lazy(() => import("./preview/ModelPreview"));
const CsvPreview = lazy(() => import("./preview/CsvPreview"));
const NotebookPreview = lazy(() => import("./preview/NotebookPreview"));

type Loaded =
	| {
			kind: "text";
			entry: FileEntry;
			text: string;
			etag?: string;
			/** 只显示了前 `PREVIEW_BYTES`（太大或读取被截断） */
			truncated: boolean;
			readOnly: boolean;
			render?: TextRender;
	  }
	| { kind: "blob"; entry: FileEntry; viewer: BlobViewer; blob: Blob }
	| { kind: "media"; entry: FileEntry; media: "video" | "audio"; src: string }
	| {
			kind: "binary";
			entry: FileEntry;
			/** 有预览方式但超过 `MAX_PREVIEW_BLOB_BYTES` */
			tooLarge: boolean;
	  };

function BlobView({
	viewer,
	blob,
	name,
}: {
	viewer: BlobViewer;
	blob: Blob;
	name: string;
}): ReactNode {
	switch (viewer) {
		case "image":
		case "heic":
		case "tiff":
		case "psd":
			return <ImagePreview blob={blob} name={name} decoder={viewer} />;
		case "pdf":
			return <PdfPreview blob={blob} name={name} />;
		case "docx":
			return <DocxPreview blob={blob} name={name} />;
		case "sheet":
			return <SheetPreview blob={blob} name={name} />;
		case "slides":
			return <SlidesPreview blob={blob} name={name} />;
		case "font":
			return <FontPreview blob={blob} name={name} />;
		case "archive":
			return <ArchivePreview blob={blob} name={name} />;
		case "model":
			return <ModelPreview blob={blob} name={name} />;
	}
}

/** 文本的渲染视图；`text` 为当前内容（含未保存的草稿）。 */
function TextView({
	render,
	text,
	name,
}: {
	render: TextRender;
	text: string;
	name: string;
}): ReactNode {
	// SVG 以 Blob 交给图片查看器（`data:` URL 显示）；只随内容变化重建
	const svg = useMemo(
		() => (render === "svg" ? new Blob([text]) : null),
		[render, text],
	);
	switch (render) {
		case "markdown":
			return (
				<div className="h-full overflow-y-auto px-4 py-3">
					<Markdown text={text} />
				</div>
			);
		case "mermaid":
			return (
				<div className="h-full overflow-auto px-4 py-3">
					<Markdown text={`\`\`\`mermaid\n${text}\n\`\`\``} />
				</div>
			);
		case "table":
			return <CsvPreview text={text} name={name} />;
		case "html":
			return <InertFrame html={text} title={name} />;
		case "notebook":
			return <NotebookPreview text={text} />;
		case "svg":
			return svg && <ImagePreview blob={svg} name={name} decoder="image" />;
	}
}

export function FileViewer({
	source,
	path,
	onSaved,
	onBack,
	onDownload,
	readOnly = false,
}: {
	source: FileSource;
	path: string;
	onSaved?(entry: FileEntry): void;
	/** 窄布局下返回列表 */
	onBack?(): void;
	onDownload?(path: string): void;
	/** 只预览不编辑（来源不支持写入，如助手文件库、停机的 orb） */
	readOnly?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const [loaded, setLoaded] = useState<Loaded | null>(null);
	const [loadError, setLoadError] = useState<unknown>(null);
	const key = draftKey(source.id, path);
	const draft = useDraft(key);
	const [saving, setSaving] = useState(false);
	const [saveError, setSaveError] = useState<unknown>(null);
	const [conflict, setConflict] = useState(false);
	/** 用户为某个文件选的视图；没选过时缺省显示预览，有草稿或文件为空（如刚新建）时显示源码 */
	const [viewChoice, setViewChoice] = useState<{
		key: string;
		source: boolean;
	} | null>(null);
	/** 源码被截断时预览用的整份文本 */
	const [fullText, setFullText] = useState<{
		key: string;
		text?: string;
		error?: unknown;
	} | null>(null);
	const [reloadKey, setReloadKey] = useState(0);
	const [wait, setWait] = useState<RetryWait | null>(null);

	// biome-ignore lint/correctness/useExhaustiveDependencies: reloadKey 触发重新加载
	useEffect(() => {
		let cancelled = false;
		setLoaded(null);
		setLoadError(null);
		setSaveError(null);
		setFullText(null);
		setWait(null);
		void withWakeRetry(
			async (): Promise<Loaded> => {
				const entry = await source.stat(path);
				const kind = previewKind(path, entry.mime, entry.text);
				// 超出编辑上限的 SVG 截断后无法显示，整份当图片读
				const big = entry.size > MAX_EDIT_BYTES;
				if (kind.kind === "text" && kind.render === "svg" && big)
					return entry.size > MAX_PREVIEW_BLOB_BYTES
						? { kind: "binary", entry, tooLarge: true }
						: {
								kind: "blob",
								entry,
								viewer: "image",
								blob: await source.readBlob(path),
							};
				if (kind.kind === "blob") {
					if (entry.size > MAX_PREVIEW_BLOB_BYTES)
						return { kind: "binary", entry, tooLarge: true };
					return {
						kind: "blob",
						entry,
						viewer: kind.viewer,
						blob: await source.readBlob(path),
					};
				}
				if (kind.kind === "media") {
					const src = source.downloadUrl(path);
					if (src) return { kind: "media", entry, media: kind.media, src };
					return { kind: "binary", entry, tooLarge: false };
				}
				if (kind.kind === "text") {
					const r = await source.readText(
						path,
						big ? PREVIEW_BYTES : MAX_EDIT_BYTES,
					);
					const truncated = big || r.truncated;
					return {
						kind: "text",
						entry,
						text: r.text,
						etag: r.etag ?? entry.etag,
						truncated,
						readOnly: readOnly || truncated,
						render: kind.render,
					};
				}
				return { kind: "binary", entry, tooLarge: false };
			},
			(w) => !cancelled && setWait(w),
		).then(
			(l) => !cancelled && setLoaded(l),
			(e) => !cancelled && setLoadError(e),
		);
		return () => {
			cancelled = true;
		};
	}, [source, path, readOnly, reloadKey]);

	const text = loaded?.kind === "text" ? loaded : null;
	const dirty = text !== null && !text.readOnly && draft !== undefined;
	// 草稿开始之后磁盘上的文件又变了：保存会触发 412 再让用户选择
	const draftStale =
		dirty && text.etag !== undefined && draft.etag !== text.etag;
	const showSource =
		!text?.render ||
		(viewChoice?.key === key
			? viewChoice.source
			: draft !== undefined || text.entry.size === 0);
	const needFull =
		text !== null &&
		!showSource &&
		text.truncated &&
		text.entry.size <= MAX_PREVIEW_BLOB_BYTES;

	useEffect(() => {
		if (!needFull || fullText?.key === key) return;
		let cancelled = false;
		setFullText({ key });
		void withWakeRetry(
			() => source.readBlob(path),
			(w) => !cancelled && setWait(w),
		)
			.then((b) => b.text())
			.then(
				(s) => !cancelled && setFullText({ key, text: s }),
				(e) => !cancelled && setFullText({ key, error: e }),
			);
		return () => {
			cancelled = true;
		};
	}, [needFull, fullText?.key, key, source, path]);

	const onChange = useCallback(
		(value: string) => {
			if (!text || text.readOnly) return;
			editDraft(key, value, { text: text.text, etag: text.etag });
		},
		[key, text],
	);

	const save = useCallback(
		async (force: boolean) => {
			if (!text || text.readOnly || !draft) return;
			const submitted = draft;
			setSaving(true);
			setSaveError(null);
			try {
				const entry = await source.write(path, submitted.text, {
					etag: force ? undefined : submitted.etag,
				});
				setLoaded({ ...text, entry, text: submitted.text, etag: entry.etag });
				markSaved(key, submitted.rev, submitted.text, entry.etag);
				setConflict(false);
				onSaved?.(entry);
			} catch (e) {
				if (e instanceof ApiError && e.status === 412) setConflict(true);
				else setSaveError(e);
			} finally {
				setSaving(false);
			}
		},
		[source, path, key, text, draft, onSaved],
	);

	const entry = loaded?.entry;
	const cut = path.lastIndexOf("/") + 1;
	const dirPart = path.slice(0, cut);
	const namePart = path.slice(cut);
	const loading = (
		<div className="flex items-center gap-2 p-3 text-muted text-sm">
			<Spinner />
			{wait &&
				t(wait === "indexing" ? "files.offline.indexing" : "files.waking")}
		</div>
	);

	let previewText: string | null = null;
	let previewError: unknown = null;
	if (text && !showSource) {
		if (dirty) previewText = draft.text;
		else if (!needFull) previewText = text.text;
		else if (fullText?.key === key) {
			previewText = fullText.text ?? null;
			previewError = fullText.error ?? null;
		}
	}

	return (
		<div className="flex h-full min-h-0 flex-col">
			<div className="flex h-10 shrink-0 items-center gap-1.5 border-border border-b px-2">
				{onBack && (
					<IconButton
						icon="back"
						label={t("files.viewer.back")}
						onClick={onBack}
					/>
				)}
				<Icon
					name={fileIcon(namePart, entry?.mime)}
					size={14}
					className="shrink-0 text-faint"
				/>
				{/* 路径拆段：目录先截断，文件名尽量完整 */}
				<span
					className="flex min-w-0 flex-1 items-center font-mono text-xs"
					title={path}
				>
					{dirPart && (
						<span className="min-w-0 shrink-[1000] truncate text-faint">
							{dirPart}
						</span>
					)}
					<span className="min-w-0 truncate font-medium text-fg">
						{namePart}
					</span>
					{dirty && (
						<span
							className="ml-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-accent"
							role="img"
							aria-label={t("files.unsaved")}
							title={t("files.unsaved")}
						/>
					)}
				</span>
				{entry && (
					<span className="flex min-w-0 shrink items-center gap-1">
						{entry.mime && !onBack && (
							<Badge title={entry.mime}>{entry.mime}</Badge>
						)}
						<Badge>{formatBytes(entry.size)}</Badge>
					</span>
				)}
				{text?.render && (
					<div className="flex shrink-0 items-center gap-0.5">
						<button
							type="button"
							aria-pressed={!showSource}
							className={`${tabClass(!showSource)} ${!showSource ? "bg-hover" : ""}`}
							onClick={() => setViewChoice({ key, source: false })}
						>
							{t("files.viewer.preview")}
						</button>
						<button
							type="button"
							aria-pressed={showSource}
							className={`${tabClass(showSource)} ${showSource ? "bg-hover" : ""}`}
							onClick={() => setViewChoice({ key, source: true })}
						>
							{t("files.viewer.source")}
						</button>
					</div>
				)}
				{draft && !readOnly && (
					<Button
						size="sm"
						variant="ghost"
						disabled={saving}
						onClick={() => discardDraft(key)}
					>
						{t("files.viewer.discard")}
					</Button>
				)}
				{text && !text.readOnly && (
					<Button
						size="sm"
						variant="primary"
						disabled={!dirty || saving}
						onClick={() => void save(false)}
						title={t("editor.saveHint")}
					>
						{t("common.save")}
					</Button>
				)}
				{entry && onDownload && source.caps.download && (
					<IconButton
						icon="download"
						label={t("files.actions.download")}
						onClick={() => onDownload(path)}
					/>
				)}
			</div>
			{saveError ? (
				<div className="shrink-0 px-3 py-1.5">
					<ErrorText error={saveError} />
				</div>
			) : null}
			<div className="min-h-0 flex-1 overflow-hidden">
				{loadError ? (
					<div className="p-3">
						<ErrorText error={loadError} />
					</div>
				) : !loaded ? (
					loading
				) : loaded.kind === "text" ? (
					<div className="flex h-full min-h-0 flex-col">
						{draftStale && (
							<p className="flex shrink-0 items-center gap-1.5 bg-warn/8 px-3 py-1.5 text-warn text-xs">
								<Icon name="alert" size={13} className="shrink-0" />
								{t("files.viewer.draftStale")}
							</p>
						)}
						{loaded.truncated && (showSource || !needFull) && (
							<p className="flex shrink-0 items-center gap-1.5 bg-warn/8 px-3 py-1.5 text-warn text-xs">
								<Icon name="info" size={13} className="shrink-0" />
								{t("files.viewer.previewTruncated", {
									shown: formatBytes(PREVIEW_BYTES),
									size: formatBytes(loaded.entry.size),
								})}
							</p>
						)}
						<Suspense
							fallback={
								<div className="p-3 text-faint text-xs">
									{t("editor.loading")}
								</div>
							}
						>
							<div className="min-h-0 flex-1">
								{!showSource && loaded.render ? (
									previewError ? (
										<div className="p-3">
											<ErrorText error={previewError} />
										</div>
									) : previewText === null ? (
										loading
									) : (
										<TextView
											render={loaded.render}
											text={previewText}
											name={namePart}
										/>
									)
								) : (
									<CodeEditor
										value={dirty ? draft.text : loaded.text}
										path={path}
										readOnly={loaded.readOnly}
										onChange={onChange}
										onSave={() => void save(false)}
									/>
								)}
							</div>
						</Suspense>
					</div>
				) : loaded.kind === "blob" ? (
					<Suspense fallback={loading}>
						<BlobView
							viewer={loaded.viewer}
							blob={loaded.blob}
							name={namePart}
						/>
					</Suspense>
				) : loaded.kind === "media" ? (
					<MediaPreview src={loaded.src} media={loaded.media} name={namePart} />
				) : (
					<div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm">
						<span className="grid h-11 w-11 place-items-center rounded-full bg-inset">
							<Icon
								name={fileIcon(loaded.entry.name, loaded.entry.mime)}
								size={20}
								className="text-faint"
							/>
						</span>
						<div className="max-w-full truncate font-medium">
							{loaded.entry.name}
						</div>
						<div className="text-muted text-xs">
							{t("files.viewer.binaryInfo", {
								mime: loaded.entry.mime || "application/octet-stream",
								size: formatBytes(loaded.entry.size),
							})}
						</div>
						{loaded.tooLarge && (
							<div className="text-muted text-xs">
								{t("files.viewer.tooLargeToPreview", {
									max: formatBytes(MAX_PREVIEW_BLOB_BYTES),
								})}
							</div>
						)}
						<div className="text-faint text-xs tabular-nums">
							{formatDateTime(loaded.entry.mtime)}
						</div>
						{onDownload && source.caps.download && (
							<Button
								size="sm"
								icon="download"
								className="mt-1"
								onClick={() => onDownload(path)}
							>
								{t("files.actions.download")}
							</Button>
						)}
					</div>
				)}
			</div>
			<Dialog
				open={conflict}
				onClose={() => setConflict(false)}
				title={t("files.conflict.title")}
				footer={
					<>
						<Button
							onClick={() => {
								setConflict(false);
								discardDraft(key);
								setReloadKey((k) => k + 1);
							}}
						>
							{t("files.conflict.reload")}
						</Button>
						<Button variant="danger" onClick={() => void save(true)}>
							{t("files.conflict.overwrite")}
						</Button>
					</>
				}
			>
				<p className="text-sm">{t("files.conflict.body", { path })}</p>
			</Dialog>
		</div>
	);
}
