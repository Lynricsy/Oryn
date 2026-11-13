/**
 * 文件预览 / 编辑：`stat` 后分流——
 * - 文本且 ≤2MiB：CodeMirror 编辑器（懒加载）；`.md` 另有「预览 / 源码」切换；
 * - 文本但更大：只读显示前 256KiB；
 * - 常见图片（≤32MiB）：`readBlob` → `<img>`（不用 iframe，也不直接导航到接口地址）；
 *   位图用 object URL，SVG 用 `data:` URL（不透明源，SVG 里的脚本与外链拿不到本站的源）；
 * - 其它：类型、大小与下载按钮。
 * 保存带 `etag`；412 `modified` → 对话框「覆盖 / 重新加载」。
 * 草稿存在 `drafts` store（按来源 + 路径），换文件、关面板、换线程回来后仍在；
 * 保存期间继续编辑时，保存完成只把基线前移，不覆盖较新的草稿。
 */
import {
	lazy,
	type ReactNode,
	Suspense,
	useCallback,
	useEffect,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "../../api/client";
import {
	type FileEntry,
	type FileSource,
	isPreviewImage,
	MAX_EDIT_BYTES,
	MAX_IMAGE_PREVIEW_BYTES,
	PREVIEW_BYTES,
} from "../../lib/file-source";
import { formatBytes, formatDateTime } from "../../lib/format";
import { Markdown } from "../markdown/Markdown";
import { Icon } from "../ui/icons";
import {
	Button,
	Dialog,
	ErrorText,
	IconButton,
	Spinner,
} from "../ui/primitives";
import {
	discardDraft,
	draftKey,
	editDraft,
	markSaved,
	useDraft,
} from "./drafts";

const CodeEditor = lazy(() => import("../editor/CodeEditor"));

/** Blob → `data:<type>;base64,…`。 */
function dataUrl(blob: Blob): Promise<string> {
	return new Promise((resolve, reject) => {
		const r = new FileReader();
		r.onload = () => resolve(String(r.result));
		r.onerror = () => reject(r.error);
		r.readAsDataURL(blob);
	});
}

type Loaded =
	| {
			kind: "text";
			entry: FileEntry;
			text: string;
			etag?: string;
			readOnly: boolean;
	  }
	| { kind: "image"; entry: FileEntry; url: string }
	| { kind: "binary"; entry: FileEntry };

export function FileViewer({
	source,
	path,
	onSaved,
	onBack,
	onDownload,
}: {
	source: FileSource;
	path: string;
	onSaved?(entry: FileEntry): void;
	/** 窄布局下返回列表 */
	onBack?(): void;
	onDownload?(path: string): void;
}): ReactNode {
	const { t } = useTranslation();
	const [loaded, setLoaded] = useState<Loaded | null>(null);
	const [loadError, setLoadError] = useState<unknown>(null);
	const key = draftKey(source.id, path);
	const draft = useDraft(key);
	const [saving, setSaving] = useState(false);
	const [saveError, setSaveError] = useState<unknown>(null);
	const [conflict, setConflict] = useState(false);
	const [mdPreview, setMdPreview] = useState(false);
	const [reloadKey, setReloadKey] = useState(0);

	// biome-ignore lint/correctness/useExhaustiveDependencies: reloadKey 触发重新加载
	useEffect(() => {
		let cancelled = false;
		let url: string | null = null;
		setLoaded(null);
		setLoadError(null);
		setSaveError(null);
		(async () => {
			const entry = await source.stat(path);
			if (isPreviewImage(entry.mime) && entry.size <= MAX_IMAGE_PREVIEW_BYTES) {
				// 下载接口一律回 application/octet-stream；类型取自 stat，否则 SVG 无法在 <img> 中显示
				const blob = new Blob([await source.readBlob(path)], {
					type: entry.mime,
				});
				if (entry.mime === "image/svg+xml")
					return { kind: "image", entry, url: await dataUrl(blob) } as const;
				url = URL.createObjectURL(blob);
				return { kind: "image", entry, url } as const;
			}
			if (entry.text) {
				const editable = entry.size <= MAX_EDIT_BYTES;
				const r = await source.readText(
					path,
					editable ? MAX_EDIT_BYTES : PREVIEW_BYTES,
				);
				return {
					kind: "text",
					entry,
					text: r.text,
					etag: r.etag ?? entry.etag,
					readOnly: !editable || r.truncated,
				} as const;
			}
			return { kind: "binary", entry } as const;
		})().then(
			(l) => {
				if (cancelled) {
					if (url) URL.revokeObjectURL(url);
					return;
				}
				setLoaded(l);
			},
			(e) => {
				if (!cancelled) setLoadError(e);
			},
		);
		return () => {
			cancelled = true;
			if (url) URL.revokeObjectURL(url);
		};
	}, [source, path, reloadKey]);

	const text = loaded?.kind === "text" ? loaded : null;
	const dirty = text !== null && !text.readOnly && draft !== undefined;
	// 草稿开始之后磁盘上的文件又变了：保存会触发 412 再让用户选择
	const draftStale =
		dirty && text.etag !== undefined && draft.etag !== text.etag;

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

	const isMarkdown = /\.(md|markdown)$/i.test(path);
	const entry = loaded?.entry;

	return (
		<div className="flex h-full min-h-0 flex-col">
			<div className="flex h-9 shrink-0 items-center gap-1 border-border border-b px-2">
				{onBack && (
					<IconButton
						icon="back"
						label={t("files.viewer.back")}
						onClick={onBack}
					/>
				)}
				<span
					className="min-w-0 flex-1 truncate font-mono text-xs"
					title={path}
				>
					{dirty && (
						<span
							className="text-accent"
							role="img"
							aria-label={t("files.unsaved")}
							title={t("files.unsaved")}
						>
							{"● "}
						</span>
					)}
					{path}
				</span>
				{text && isMarkdown && (
					<div className="flex rounded-md border border-border text-xs">
						<button
							type="button"
							aria-pressed={!mdPreview}
							className={`px-2 py-0.5 ${!mdPreview ? "bg-hover" : "text-muted"}`}
							onClick={() => setMdPreview(false)}
						>
							{t("files.viewer.source")}
						</button>
						<button
							type="button"
							aria-pressed={mdPreview}
							className={`px-2 py-0.5 ${mdPreview ? "bg-hover" : "text-muted"}`}
							onClick={() => setMdPreview(true)}
						>
							{t("files.viewer.preview")}
						</button>
					</div>
				)}
				{draft && (
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
						icon="back"
						className="-rotate-90"
						label={t("files.actions.download")}
						onClick={() => onDownload(path)}
					/>
				)}
			</div>
			<ErrorText error={saveError} />
			<div className="min-h-0 flex-1 overflow-hidden">
				{loadError ? (
					<div className="p-3">
						<ErrorText error={loadError} />
					</div>
				) : !loaded ? (
					<div className="p-3">
						<Spinner />
					</div>
				) : loaded.kind === "text" ? (
					<div className="flex h-full min-h-0 flex-col">
						{draftStale && (
							<p className="shrink-0 bg-warn/10 px-3 py-1 text-warn text-xs">
								{t("files.viewer.draftStale")}
							</p>
						)}
						{loaded.readOnly && (
							<p className="shrink-0 bg-warn/10 px-3 py-1 text-warn text-xs">
								{t("files.viewer.previewTruncated", {
									shown: formatBytes(PREVIEW_BYTES),
									size: formatBytes(loaded.entry.size),
								})}
							</p>
						)}
						{isMarkdown && mdPreview ? (
							<div className="min-h-0 flex-1 overflow-y-auto p-3 text-sm">
								<Markdown text={dirty ? draft.text : loaded.text} />
							</div>
						) : (
							<Suspense
								fallback={
									<div className="p-3 text-faint text-xs">
										{t("editor.loading")}
									</div>
								}
							>
								<div className="min-h-0 flex-1">
									<CodeEditor
										value={dirty ? draft.text : loaded.text}
										path={path}
										readOnly={loaded.readOnly}
										onChange={onChange}
										onSave={() => void save(false)}
									/>
								</div>
							</Suspense>
						)}
					</div>
				) : loaded.kind === "image" ? (
					<div className="flex h-full items-center justify-center overflow-auto bg-inset p-3">
						<img
							src={loaded.url}
							alt={loaded.entry.name}
							className="max-h-full max-w-full object-contain"
						/>
					</div>
				) : (
					<div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm">
						<Icon name="file" size={28} className="text-faint" />
						<div className="font-medium">{loaded.entry.name}</div>
						<div className="text-muted text-xs">
							{t("files.viewer.binaryInfo", {
								mime: loaded.entry.mime || "application/octet-stream",
								size: formatBytes(loaded.entry.size),
							})}
						</div>
						<div className="text-faint text-xs">
							{formatDateTime(loaded.entry.mtime)}
						</div>
						{onDownload && source.caps.download && (
							<Button size="sm" onClick={() => onDownload(path)}>
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
