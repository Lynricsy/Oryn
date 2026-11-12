/**
 * 文件预览 / 编辑：`stat` 后分流——
 * - 文本且 ≤2MiB：CodeMirror 编辑器（懒加载）；`.md` 另有「预览 / 源码」切换；
 * - 文本但更大：只读显示前 256KiB；
 * - 常见图片：`readBlob` → object URL 的 `<img>`（不用 iframe，也不直接导航到接口地址）；
 * - 其它：类型、大小与下载按钮。
 * 保存带 `etag`；412 `modified` → 对话框「覆盖 / 重新加载」。
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
	PREVIEW_BYTES,
} from "../../lib/file-source";
import { formatDateTime } from "../../lib/format";
import { formatBytes } from "../../lib/images";
import { Markdown } from "../markdown/Markdown";
import { Icon } from "../ui/icons";
import {
	Button,
	Dialog,
	ErrorText,
	IconButton,
	Spinner,
} from "../ui/primitives";

const CodeEditor = lazy(() => import("../editor/CodeEditor"));

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
	const [draft, setDraft] = useState<string | null>(null);
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
		setDraft(null);
		setSaveError(null);
		(async () => {
			const entry = await source.stat(path);
			if (isPreviewImage(entry.mime)) {
				// 下载接口一律回 application/octet-stream；类型取自 stat，否则 SVG 无法在 <img> 中显示
				const blob = await source.readBlob(path);
				url = URL.createObjectURL(new Blob([blob], { type: entry.mime }));
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
	const dirty = text !== null && draft !== null && draft !== text.text;

	const save = useCallback(
		async (force: boolean) => {
			if (!text || text.readOnly || draft === null) return;
			setSaving(true);
			setSaveError(null);
			try {
				const entry = await source.write(path, draft, {
					etag: force ? undefined : text.etag,
				});
				setLoaded({ ...text, entry, text: draft, etag: entry.etag });
				setDraft(null);
				setConflict(false);
				onSaved?.(entry);
			} catch (e) {
				if (e instanceof ApiError && e.status === 412) setConflict(true);
				else setSaveError(e);
			} finally {
				setSaving(false);
			}
		},
		[source, path, text, draft, onSaved],
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
					{dirty && <span className="text-accent">● </span>}
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
								<Markdown text={draft ?? loaded.text} />
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
										value={draft ?? loaded.text}
										path={path}
										readOnly={loaded.readOnly}
										onChange={setDraft}
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
