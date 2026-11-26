/**
 * 消息附件（粘贴 / 拖放 / 选择任意文件）：加入即开始上传到控制面，发送时以 `orynAttachments` 携带 id。
 * 图片另按整条消息内联预算压缩出一份内联副本（模型可直接看到）；预算用完的图片只作为文件附件（「仅文件」）。
 * 新建线程框与线程输入区共用。
 * `library`（全局助手对话，M13）：文件传进助手文件库，发送时以 `orynFiles` 携带库文件 id；有内联副本的图片
 * 只随 `images` 发送、不上传。首屏切换对象时去向随之改变，已加入的文件按新去向重传（旧上传尽力删除）。
 * 系统分享进来的文件由移动壳先行上传（M7 计划 4.6），经 `addUploaded` 以已完成条目加入（只能随普通线程发送）。
 * 每个文件在预处理（图片压缩）之前就登记取消句柄：composer 卸载（换线程）后，
 * 未完成的预处理不会再加入条目或开始上传。
 */
import {
	type ChangeEvent,
	useCallback,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { assistantFileContentUrl } from "../../api/library-queries";
import {
	type AssistantFile,
	type Attachment,
	attachmentContentUrl,
	deleteAssistantFile,
	deleteAttachment,
	MAX_ATTACHMENT_BYTES,
	MAX_ATTACHMENTS_PER_MESSAGE,
	uploadAssistantFile,
	uploadAttachment,
} from "../../lib/attachments";
import { formatBytes } from "../../lib/format";
import { rasterMime } from "../../lib/image-src";
import {
	checkBudget,
	type PreparedImage,
	prepareImage,
} from "../../lib/images";
import { useCoarsePointer } from "../../lib/viewport";
import type { ImageContent } from "../../omp/types";
import type { ComposerRestoreOp } from "../../state/composer-drafts";
import { Badge, Meter } from "../ui/data";
import { FileTypeIcon } from "../ui/FileTypeIcon";
import { ImageViewer, type LightboxImage } from "../ui/ImageViewer";
import { Icon } from "../ui/icons";
import { describeError, Spinner } from "../ui/primitives";

export interface AttachmentItem {
	key: string;
	name: string;
	size: number;
	status: "preparing" | "uploading" | "done" | "error";
	/** 已上传字节 / 总字节（0–1）。 */
	progress: number;
	/** 单个文件的错误（超限、上传失败）；该文件不会随消息发送。 */
	error?: string;
	attachment?: Attachment;
	/** 已传进助手文件库的文件（`library` 模式）。 */
	libraryFile?: AssistantFile;
	/** 内联副本（仅图片且预算允许时）。 */
	image?: PreparedImage;
	/** 预处理期间的本机临时缩略图 URL（完成后或移除时 revoke，避免泄漏）。 */
	previewUrl?: string;
	/** 图片未能放进内联预算：只作为文件附件。 */
	fileOnly?: boolean;
	/** 本机选的原始文件：上传去向改变时据此重传；系统分享进来的已上传条目没有。 */
	source?: File;
	/** 回退带回的条目（`ComposerRestoreOp` 的批次标记）：附件属于旧消息，移除时不删除上传。 */
	restoredTag?: string;
}

const RASTER_IMAGE_EXT = /\.(png|jpe?g|gif|webp|avif)$/i;

/** 本地文件是否可视为安全栅格图（用于无 MIME 的拖拽/选图场景生成本地缩略图）。 */
function isLocalRasterImage(file: File): boolean {
	if (file.type) return rasterMime(file.type) !== null;
	return RASTER_IMAGE_EXT.test(file.name);
}

/** 解析附件条目的小预览图地址：内联副本 → 本地临时 URL → 已上传的栅格图附件内联地址。 */
function attachmentThumbSrc(it: AttachmentItem): string | undefined {
	if (it.image) return `data:${it.image.mimeType};base64,${it.image.data}`;
	if (it.previewUrl) return it.previewUrl;
	if (it.attachment && rasterMime(it.attachment.mime)) {
		return attachmentContentUrl(it.attachment.id, true);
	}
	if (it.libraryFile && rasterMime(it.libraryFile.mime)) {
		return assistantFileContentUrl(it.libraryFile.id, true);
	}
	return undefined;
}

let seq = 0;

/** 创建本机临时缩略图地址（兼容不含 createObjectURL 的测试环境）。 */
function makePreviewUrl(file: File): string | undefined {
	return typeof URL.createObjectURL === "function"
		? URL.createObjectURL(file)
		: undefined;
}

/** 释放本机临时缩略图地址。 */
function revokePreview(item: Pick<AttachmentItem, "previewUrl"> | undefined) {
	if (item?.previewUrl && typeof URL.revokeObjectURL === "function")
		URL.revokeObjectURL(item.previewUrl);
}

/** 已上传但不再发送：尽力删除（未随消息发出的上传服务端 24h 后也会清理）。回退带回的旧消息附件不删。 */
function discard(item: AttachmentItem) {
	if (item.restoredTag !== undefined) return;
	if (item.attachment)
		void deleteAttachment(item.attachment.id).catch(() => undefined);
	if (item.libraryFile)
		void deleteAssistantFile(item.libraryFile.id).catch(() => undefined);
}

export function useAttachments({
	threadId,
	library = false,
}: {
	threadId?: string;
	/** 传进全局助手文件库（`orynFiles`）而不是线程附件（`orynAttachments`） */
	library?: boolean;
} = {}) {
	const { t } = useTranslation();
	const [items, setItems] = useState<AttachmentItem[]>([]);
	const [error, setError] = useState<string | null>(null);
	// 同步可读的当前列表：所有修改都经 commit 基于它计算并同时写回 state，连续加入与并发上传回调不会互相覆盖
	const itemsRef = useRef<AttachmentItem[]>([]);
	const aborts = useRef(new Map<string, AbortController>());
	const disposed = useRef(false);

	useEffect(() => {
		disposed.current = false;
		const map = aborts.current;
		return () => {
			disposed.current = true;
			for (const c of map.values()) c.abort();
			map.clear();
			for (const it of itemsRef.current) revokePreview(it);
		};
	}, []);

	const commit = useCallback(
		(fn: (prev: AttachmentItem[]) => AttachmentItem[]) => {
			const next = fn(itemsRef.current);
			itemsRef.current = next;
			setItems(next);
		},
		[],
	);

	const update = useCallback(
		(key: string, patch: Partial<AttachmentItem>) =>
			commit((prev) =>
				prev.map((i) => (i.key === key ? { ...i, ...patch } : i)),
			),
		[commit],
	);

	const push = useCallback(
		(item: AttachmentItem) => commit((prev) => [...prev, item]),
		[commit],
	);

	// 当前的上传去向：上传开始时读取；换去向（下面的 effect）后按新去向重传
	const libraryRef = useRef(library);

	/** 按当前去向上传一个条目；助手文件库模式下有内联副本的图片只随 `images` 发送，不上传。 */
	const upload = useCallback(
		(key: string, file: File, inline: boolean) => {
			const lib = libraryRef.current;
			if (lib && inline) {
				update(key, { status: "done", progress: 1, error: undefined });
				return;
			}
			const ctl = new AbortController();
			aborts.current.set(key, ctl);
			update(key, { status: "uploading", progress: 0, error: undefined });
			const opts = {
				threadId,
				signal: ctl.signal,
				onProgress: (sent: number, total: number) =>
					update(key, { progress: total > 0 ? sent / total : 0 }),
			};
			const done: Promise<Partial<AttachmentItem>> = lib
				? uploadAssistantFile(file, opts).then((libraryFile) => ({
						libraryFile,
					}))
				: uploadAttachment(file, opts).then((attachment) => ({ attachment }));
			done
				.then((patch) => {
					if (ctl.signal.aborted) return;
					const cur = itemsRef.current.find((i) => i.key === key);
					const uploadedImg =
						(patch.attachment && rasterMime(patch.attachment.mime) !== null) ||
						(patch.libraryFile && rasterMime(patch.libraryFile.mime) !== null);
					if (cur?.previewUrl && uploadedImg) {
						revokePreview(cur);
						update(key, {
							status: "done",
							progress: 1,
							previewUrl: undefined,
							...patch,
						});
						return;
					}
					update(key, { status: "done", progress: 1, ...patch });
				})
				.catch((e: unknown) => {
					if (ctl.signal.aborted) return;
					update(key, { status: "error", error: describeError(e, t) });
				})
				.finally(() => {
					if (aborts.current.get(key) === ctl) aborts.current.delete(key);
				});
		},
		[threadId, t, update],
	);

	// 去向改变（首屏换对象）：本机选的文件取消在途上传、删掉旧上传、按新去向重传；
	// 系统分享进来的已上传附件（线程附件）不能随助手对话发送，切到助手时标为错误，切回来恢复
	useEffect(() => {
		if (libraryRef.current === library) return;
		libraryRef.current = library;
		for (const it of itemsRef.current) {
			if (it.status === "preparing") continue;
			if (!it.source) {
				if (library && it.attachment && it.status === "done")
					update(it.key, {
						status: "error",
						error: t("attachments.notForAssistant"),
					});
				else if (!library && it.attachment && it.status === "error")
					update(it.key, { status: "done", error: undefined });
				continue;
			}
			aborts.current.get(it.key)?.abort();
			aborts.current.delete(it.key);
			discard(it);
			update(it.key, { attachment: undefined, libraryFile: undefined });
			upload(it.key, it.source, !!it.image);
		}
	}, [library, t, update, upload]);

	const addFiles = useCallback(
		async (files: Iterable<File>) => {
			setError(null);
			const queued: Array<{
				key: string;
				file: File;
				isImage: boolean;
				ctl: AbortController;
			}> = [];
			// 先同步把本批合规条目全部推入列表：拖入/选图后 0ms 立即显现卡片与处理/上传动画
			for (const file of files) {
				if (disposed.current) return;
				const key = `att-${++seq}`;
				const live = itemsRef.current.filter((i) => i.status !== "error");
				if (live.length >= MAX_ATTACHMENTS_PER_MESSAGE) {
					setError(
						t("attachments.tooMany", { max: MAX_ATTACHMENTS_PER_MESSAGE }),
					);
					break;
				}
				if (file.size > MAX_ATTACHMENT_BYTES) {
					push({
						key,
						name: file.name,
						size: file.size,
						status: "error",
						progress: 0,
						error: t("attachments.tooLarge", {
							max: formatBytes(MAX_ATTACHMENT_BYTES),
						}),
					});
					continue;
				}
				const isImage =
					file.type.startsWith("image/") || RASTER_IMAGE_EXT.test(file.name);
				const canLocalThumb = isLocalRasterImage(file);
				const ctl = new AbortController();
				aborts.current.set(key, ctl);
				push({
					key,
					name: file.name,
					size: file.size,
					status: isImage ? "preparing" : "uploading",
					progress: 0,
					previewUrl: canLocalThumb ? makePreviewUrl(file) : undefined,
					source: file,
				});
				queued.push({ key, file, isImage, ctl });
			}
			for (const { key, file, isImage, ctl } of queued) {
				if (disposed.current) return;
				if (ctl.signal.aborted) {
					if (aborts.current.get(key) === ctl) aborts.current.delete(key);
					continue;
				}
				let hasInline = false;
				if (isImage) {
					const { remaining } = checkBudget(
						itemsRef.current.map((i) => i.image?.inlineBytes ?? 0),
						0,
					);
					let prepared: PreparedImage | undefined;
					try {
						prepared = await prepareImage(file, file.name, remaining);
					} catch {
						// 预算用完或解码失败：只作为文件附件
					}
					// 预处理期间 composer 已卸载或条目被移除：不再上传
					if (ctl.signal.aborted || disposed.current) {
						if (aborts.current.get(key) === ctl) aborts.current.delete(key);
						if (disposed.current) return;
						continue;
					}
					const current = itemsRef.current.find((i) => i.key === key);
					if (prepared) {
						revokePreview(current);
						hasInline = true;
						update(key, { image: prepared, previewUrl: undefined });
					} else {
						// 预算用完或解码失败：若本机已有缩略图则保留至上传完成，避免闪退回图标
						update(key, { fileOnly: true });
					}
				}
				upload(key, file, hasInline);
			}
		},
		[push, t, update, upload],
	);

	/**
	 * 已在别处上传好的附件（系统分享）：直接作为完成条目加入；图片不做内联副本，只作为文件附件。
	 * 它们是线程附件，助手文件库模式下不能发送（标为错误，切回普通去向时恢复）。
	 */
	const addUploaded = useCallback(
		(list: Attachment[]) => {
			setError(null);
			for (const attachment of list) {
				const live = itemsRef.current.filter((i) => i.status !== "error");
				if (live.length >= MAX_ATTACHMENTS_PER_MESSAGE) {
					setError(
						t("attachments.tooMany", { max: MAX_ATTACHMENTS_PER_MESSAGE }),
					);
					return;
				}
				push({
					key: `att-${++seq}`,
					name: attachment.name,
					size: attachment.size,
					status: libraryRef.current ? "error" : "done",
					progress: 1,
					attachment,
					fileOnly: attachment.mime.startsWith("image/"),
					...(libraryRef.current
						? { error: t("attachments.notForAssistant") }
						: {}),
				});
			}
		},
		[push, t],
	);

	/**
	 * 回退带回的附件：`add` 以完成条目加入（复用旧附件 id，不重传；有内联副本的图片照旧随 `images` 发送）；
	 * 线程附件在助手文件库模式下不能发送，同 `addUploaded` 标为错误。`drop`（撤销那次回退）去掉这一批，不删除上传。
	 */
	const applyRestore = useCallback(
		(op: ComposerRestoreOp) => {
			if (op.kind === "drop") {
				commit((prev) => prev.filter((i) => i.restoredTag !== op.tag));
				return;
			}
			setError(null);
			for (const r of op.items) {
				const live = itemsRef.current.filter((i) => i.status !== "error");
				if (live.length >= MAX_ATTACHMENTS_PER_MESSAGE) {
					setError(
						t("attachments.tooMany", { max: MAX_ATTACHMENTS_PER_MESSAGE }),
					);
					return;
				}
				const blocked = libraryRef.current && r.attachment !== undefined;
				push({
					key: `att-${++seq}`,
					name: r.name,
					size: r.size,
					status: blocked ? "error" : "done",
					progress: 1,
					restoredTag: op.tag,
					...(r.attachment ? { attachment: r.attachment } : {}),
					...(r.image
						? {
								image: {
									type: "image",
									data: r.image.data,
									mimeType: r.image.mimeType,
									// 尺寸只在本机压缩时得知；恢复的副本不用它
									width: 0,
									height: 0,
									inlineBytes: r.image.data.length,
									name: r.name,
								},
							}
						: {}),
					fileOnly:
						!r.image && r.attachment?.mime.startsWith("image/") === true,
					...(blocked ? { error: t("attachments.notForAssistant") } : {}),
				});
			}
		},
		[commit, push, t],
	);

	const remove = useCallback(
		(key: string) => {
			const item = itemsRef.current.find((i) => i.key === key);
			aborts.current.get(key)?.abort();
			aborts.current.delete(key);
			commit((prev) => prev.filter((i) => i.key !== key));
			revokePreview(item);
			if (item) discard(item);
		},
		[commit],
	);

	/** 发送成功后清空（附件已随消息绑定，不删除）。 */
	const clear = useCallback(() => {
		for (const it of itemsRef.current) revokePreview(it);
		commit(() => []);
		setError(null);
	}, [commit]);

	/** 发送失败时放回先前清掉的条目（清空时没有删除上传）；输入区已卸载则作罢，同 key 的不重复加入。 */
	const restore = useCallback(
		(list: readonly AttachmentItem[]) => {
			if (disposed.current || list.length === 0) return;
			commit((prev) => [
				...list.filter((i) => !prev.some((p) => p.key === i.key)),
				...prev,
			]);
		},
		[commit],
	);

	const sendable = items.filter((i) => i.status === "done");
	const contents: ImageContent[] = sendable.flatMap((i) =>
		i.image
			? [{ type: "image", data: i.image.data, mimeType: i.image.mimeType }]
			: [],
	);
	const attachmentIds = sendable.flatMap((i) =>
		i.attachment ? [i.attachment.id] : [],
	);
	const fileIds = sendable.flatMap((i) =>
		i.libraryFile ? [i.libraryFile.id] : [],
	);
	const uploading = items.some(
		(i) => i.status === "preparing" || i.status === "uploading",
	);
	return {
		items,
		contents,
		attachmentIds,
		fileIds,
		/** 至少有一个能随消息发出的条目（只有附件、没有文字时也能发送） */
		hasFiles: sendable.length > 0,
		uploading,
		error,
		addFiles,
		addUploaded,
		applyRestore,
		remove,
		clear,
		restore,
	};
}

const ATTACH_BTN =
	"inline-flex h-[30px] w-[30px] shrink-0 cursor-pointer items-center justify-center rounded-md text-faint transition-colors duration-150 hover:bg-hover hover:text-fg";

/** 输入区工具行的附件入口：回形针选任意文件；触屏上再给一个相机键直接拍照（M7 计划 4.5）。 */
export function AttachButton({ onFiles }: { onFiles(files: File[]): void }) {
	const { t } = useTranslation();
	const coarse = useCoarsePointer();
	const pick = (e: ChangeEvent<HTMLInputElement>) => {
		// 先拷出文件再清空 value（清空后同一文件可再次选择）
		const files = e.target.files ? [...e.target.files] : [];
		e.target.value = "";
		if (files.length > 0) onFiles(files);
	};
	return (
		<>
			<label className={ATTACH_BTN}>
				<Icon name="attach" size={15} />
				<span className="sr-only">{t("attachments.attach")}</span>
				<input
					type="file"
					multiple
					data-testid="attach-file-input"
					className="hidden"
					onChange={pick}
				/>
			</label>
			{coarse && (
				<label data-testid="attach-camera" className={ATTACH_BTN}>
					<Icon name="camera" size={15} />
					<span className="sr-only">{t("attachments.camera")}</span>
					<input
						type="file"
						accept="image/*"
						capture="environment"
						data-testid="attach-camera-input"
						className="hidden"
						onChange={pick}
					/>
				</label>
			)}
		</>
	);
}

export function AttachmentStrip({
	items,
	onRemove,
}: {
	items: AttachmentItem[];
	onRemove: (key: string) => void;
}) {
	const { t } = useTranslation();
	const [previewIndex, setPreviewIndex] = useState<number | null>(null);
	if (items.length === 0) return null;
	const gallery: LightboxImage[] = [];
	const galleryIndexByKey = new Map<string, number>();
	for (const it of items) {
		const thumb = attachmentThumbSrc(it);
		if (thumb) {
			galleryIndexByKey.set(it.key, gallery.length);
			gallery.push({
				src: thumb,
				alt: it.name,
				caption: it.name,
				filename: it.name,
			});
		}
	}
	return (
		<>
			<ul className="flex flex-wrap gap-2 px-3 pt-3">
				{items.map((it) => {
					const percent = Math.round(it.progress * 100);
					const busy = it.status === "preparing" || it.status === "uploading";
					const thumb = attachmentThumbSrc(it);
					const gIdx = galleryIndexByKey.get(it.key);
					return (
						<li
							key={it.key}
							data-status={it.status}
							className={`attachment-chip-in relative flex max-w-60 items-center gap-2 rounded-lg border bg-panel py-1 pr-2.5 pl-1 text-xs shadow-xs ${
								it.status === "error"
									? "border-err/50"
									: busy
										? "border-accent/40"
										: "border-border"
							}`}
							title={it.error ?? it.name}
						>
							{thumb ? (
								<button
									type="button"
									onClick={() => {
										if (gIdx !== undefined) setPreviewIndex(gIdx);
									}}
									aria-label={`${it.name} — ${t("image.lightbox.fullscreen")}`}
									title={
										it.error ?? `${it.name} · ${t("image.lightbox.fullscreen")}`
									}
									className="flex min-w-0 flex-1 cursor-zoom-in items-center gap-2 rounded-md text-left focus-visible:outline-2 focus-visible:outline-[var(--ring)] focus-visible:outline-offset-2"
								>
									<span className="relative h-10 w-10 shrink-0 overflow-hidden rounded-md bg-inset">
										<img
											src={thumb}
											alt={it.name}
											className={`h-full w-full object-cover transition-opacity duration-150 ${
												busy ? "opacity-60" : "opacity-100"
											}`}
										/>
										{busy && (
											<span
												aria-hidden
												className="absolute inset-0 flex items-center justify-center bg-bg/35 text-accent-ink backdrop-blur-[1px]"
											>
												<Spinner size={14} />
											</span>
										)}
									</span>
									<span className="flex min-w-0 flex-col gap-1">
										<span className="block truncate font-medium text-fg">
											{it.name}
										</span>
										{it.status === "error" ? (
											<span className="block truncate text-err">
												{it.error}
											</span>
										) : it.status === "preparing" ? (
											<span className="flex items-center gap-1.5 text-2xs text-faint">
												<span
													aria-hidden
													className="attachment-indeterminate inline-block h-1.5 w-14 shrink-0"
												/>
												{t("attachments.preparing")}
											</span>
										) : it.status === "uploading" ? (
											<span className="flex items-center gap-1.5 text-2xs text-faint tabular-nums">
												<Meter
													value={percent}
													tone="accent"
													label={t("attachments.uploading", { percent })}
													className="w-16"
												/>
												{t("attachments.uploading", { percent })}
											</span>
										) : (
											<span className="flex min-w-0 items-center gap-1.5 text-2xs text-faint tabular-nums">
												{formatBytes(it.size)}
												{it.fileOnly && (
													<Badge>{t("attachments.fileOnly")}</Badge>
												)}
											</span>
										)}
									</span>
								</button>
							) : (
								<>
									<span className="relative flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-inset text-faint">
										<FileTypeIcon name={it.name} size={22} />
										{busy && (
											<span
												aria-hidden
												className="absolute inset-0 flex items-center justify-center rounded-md bg-inset/75 text-accent-ink"
											>
												<Spinner size={14} />
											</span>
										)}
									</span>
									<span className="flex min-w-0 flex-col gap-1">
										<span className="block truncate font-medium text-fg">
											{it.name}
										</span>
										{it.status === "error" ? (
											<span className="block truncate text-err">
												{it.error}
											</span>
										) : it.status === "preparing" ? (
											<span className="flex items-center gap-1.5 text-2xs text-faint">
												<span
													aria-hidden
													className="attachment-indeterminate inline-block h-1.5 w-14 shrink-0"
												/>
												{t("attachments.preparing")}
											</span>
										) : it.status === "uploading" ? (
											<span className="flex items-center gap-1.5 text-2xs text-faint tabular-nums">
												<Meter
													value={percent}
													tone="accent"
													label={t("attachments.uploading", { percent })}
													className="w-16"
												/>
												{t("attachments.uploading", { percent })}
											</span>
										) : (
											<span className="flex min-w-0 items-center gap-1.5 text-2xs text-faint tabular-nums">
												{formatBytes(it.size)}
												{it.fileOnly && (
													<Badge>{t("attachments.fileOnly")}</Badge>
												)}
											</span>
										)}
									</span>
								</>
							)}
							<button
								type="button"
								aria-label={t("attachments.remove", { name: it.name })}
								className="absolute -top-1.5 -right-1.5 flex h-5 w-5 items-center justify-center rounded-full bg-ink text-ink-fg shadow-xs transition-[scale] duration-150 hover:scale-110"
								onClick={() => onRemove(it.key)}
							>
								<Icon name="close" size={11} weight="bold" />
							</button>
						</li>
					);
				})}
			</ul>
			{previewIndex !== null && gallery.length > 0 && (
				<ImageViewer
					images={gallery}
					initialIndex={previewIndex}
					onClose={() => setPreviewIndex(null)}
				/>
			)}
		</>
	);
}
