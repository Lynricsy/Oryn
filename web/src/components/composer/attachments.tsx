/**
 * 消息附件（粘贴 / 拖放 / 选择任意文件）：加入即开始上传到控制面，发送时以 `orynAttachments` 携带 id。
 * 图片另按整条消息内联预算压缩出一份内联副本（模型可直接看到）；预算用完的图片只作为文件附件（「仅文件」）。
 * 新建线程框与线程输入区共用。
 * 系统分享进来的文件由移动壳先行上传（M7 计划 4.6），经 `addUploaded` 以已完成条目加入。
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
import {
	type Attachment,
	deleteAttachment,
	MAX_ATTACHMENT_BYTES,
	MAX_ATTACHMENTS_PER_MESSAGE,
	uploadAttachment,
} from "../../lib/attachments";
import { fileIcon } from "../../lib/file-icon";
import { formatBytes } from "../../lib/format";
import {
	checkBudget,
	type PreparedImage,
	prepareImage,
} from "../../lib/images";
import { useCoarsePointer } from "../../lib/viewport";
import type { ImageContent } from "../../omp/types";
import { Badge, Meter } from "../ui/data";
import { Icon } from "../ui/icons";
import { describeError } from "../ui/primitives";

export interface AttachmentItem {
	key: string;
	name: string;
	size: number;
	status: "uploading" | "done" | "error";
	/** 已上传字节 / 总字节（0–1）。 */
	progress: number;
	/** 单个文件的错误（超限、上传失败）；该文件不会随消息发送。 */
	error?: string;
	attachment?: Attachment;
	/** 内联副本（仅图片且预算允许时）。 */
	image?: PreparedImage;
	/** 图片未能放进内联预算：只作为文件附件。 */
	fileOnly?: boolean;
}

let seq = 0;

export function useAttachments({ threadId }: { threadId?: string } = {}) {
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

	const addFiles = useCallback(
		async (files: Iterable<File>) => {
			setError(null);
			for (const file of files) {
				if (disposed.current) return;
				const key = `att-${++seq}`;
				const live = itemsRef.current.filter((i) => i.status !== "error");
				if (live.length >= MAX_ATTACHMENTS_PER_MESSAGE) {
					setError(
						t("attachments.tooMany", { max: MAX_ATTACHMENTS_PER_MESSAGE }),
					);
					return;
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
				const item: AttachmentItem = {
					key,
					name: file.name,
					size: file.size,
					status: "uploading",
					progress: 0,
				};
				const ctl = new AbortController();
				aborts.current.set(key, ctl);
				if (file.type.startsWith("image/")) {
					const { remaining } = checkBudget(
						itemsRef.current.map((i) => i.image?.inlineBytes ?? 0),
						0,
					);
					try {
						item.image = await prepareImage(file, file.name, remaining);
					} catch {
						// 预算用完或解码失败：只作为文件附件
						item.fileOnly = true;
					}
					// 预处理期间 composer 已卸载：不再加入、不再上传
					if (ctl.signal.aborted || disposed.current) {
						aborts.current.delete(key);
						return;
					}
				}
				push(item);
				uploadAttachment(file, {
					threadId,
					signal: ctl.signal,
					onProgress: (sent, total) =>
						update(key, { progress: total > 0 ? sent / total : 0 }),
				})
					.then((attachment) => {
						if (ctl.signal.aborted) return;
						update(key, { status: "done", progress: 1, attachment });
					})
					.catch((e: unknown) => {
						if (ctl.signal.aborted) return;
						update(key, { status: "error", error: describeError(e, t) });
					})
					.finally(() => aborts.current.delete(key));
			}
		},
		[push, threadId, t, update],
	);

	/** 已在别处上传好的附件（系统分享）：直接作为完成条目加入；图片不做内联副本，只作为文件附件。 */
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
					status: "done",
					progress: 1,
					attachment,
					fileOnly: attachment.mime.startsWith("image/"),
				});
			}
		},
		[push, t],
	);

	const remove = useCallback(
		(key: string) => {
			const item = itemsRef.current.find((i) => i.key === key);
			aborts.current.get(key)?.abort();
			aborts.current.delete(key);
			commit((prev) => prev.filter((i) => i.key !== key));
			// 已上传但不再发送：尽力删除（未绑定的附件服务端 24h 后也会清理）
			if (item?.attachment)
				void deleteAttachment(item.attachment.id).catch(() => undefined);
		},
		[commit],
	);

	/** 发送成功后清空（附件已随消息绑定，不删除）。 */
	const clear = useCallback(() => {
		commit(() => []);
		setError(null);
	}, [commit]);

	const sendable = items.filter((i) => i.status === "done");
	const contents: ImageContent[] = sendable.flatMap((i) =>
		i.image
			? [{ type: "image", data: i.image.data, mimeType: i.image.mimeType }]
			: [],
	);
	const attachmentIds = sendable.flatMap((i) =>
		i.attachment ? [i.attachment.id] : [],
	);
	const uploading = items.some((i) => i.status === "uploading");
	return {
		items,
		contents,
		attachmentIds,
		uploading,
		error,
		addFiles,
		addUploaded,
		remove,
		clear,
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
	if (items.length === 0) return null;
	return (
		<ul className="flex flex-wrap gap-2 px-3 pt-3">
			{items.map((it) => {
				const percent = Math.round(it.progress * 100);
				return (
					<li
						key={it.key}
						className={`relative flex max-w-60 items-center gap-2 rounded-lg border bg-panel py-1 pr-2.5 pl-1 text-xs shadow-xs ${
							it.status === "error" ? "border-err/50" : "border-border"
						}`}
						title={it.error ?? it.name}
					>
						{it.image ? (
							<img
								src={`data:${it.image.mimeType};base64,${it.image.data}`}
								alt={it.name}
								className="h-10 w-10 shrink-0 rounded-md object-cover"
							/>
						) : (
							<span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-inset text-faint">
								<Icon name={fileIcon(it.name)} size={18} />
							</span>
						)}
						<span className="flex min-w-0 flex-col gap-1">
							<span className="block truncate font-medium text-fg">
								{it.name}
							</span>
							{it.status === "error" ? (
								<span className="block truncate text-err">{it.error}</span>
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
									{it.fileOnly && <Badge>{t("attachments.fileOnly")}</Badge>}
								</span>
							)}
						</span>
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
	);
}
