/**
 * 消息附件（粘贴 / 拖放 / 选择任意文件）：加入即开始上传到控制面，发送时以 `orynAttachments` 携带 id。
 * 图片另按整条消息内联预算压缩出一份内联副本（模型可直接看到）；预算用完的图片只作为文件附件（「仅文件」）。
 * 新建线程框与线程输入区共用。
 * 每个文件在预处理（图片压缩）之前就登记取消句柄：composer 卸载（换线程）后，
 * 未完成的预处理不会再加入条目或开始上传。
 */
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type Attachment,
	deleteAttachment,
	MAX_ATTACHMENT_BYTES,
	MAX_ATTACHMENTS_PER_MESSAGE,
	uploadAttachment,
} from "../../lib/attachments";
import { formatBytes } from "../../lib/format";
import {
	checkBudget,
	type PreparedImage,
	prepareImage,
} from "../../lib/images";
import type { ImageContent } from "../../omp/types";
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
		remove,
		clear,
	};
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
		<ul className="flex flex-wrap gap-2 px-3 pt-2">
			{items.map((it) => (
				<li
					key={it.key}
					className={`relative flex max-w-60 items-center gap-2 rounded-md border px-1.5 py-1 text-xs ${
						it.status === "error" ? "border-err/60" : "border-border"
					}`}
					title={it.error ?? it.name}
				>
					{it.image ? (
						<img
							src={`data:${it.image.mimeType};base64,${it.image.data}`}
							alt={it.name}
							className="h-10 w-10 shrink-0 rounded object-cover"
						/>
					) : (
						<span className="flex h-10 w-10 shrink-0 items-center justify-center rounded bg-hover text-muted">
							<Icon name={it.fileOnly ? "image" : "file"} size={16} />
						</span>
					)}
					<span className="min-w-0">
						<span className="block truncate text-fg">{it.name}</span>
						<span className="block truncate text-faint">
							{it.status === "error" ? (
								<span className="text-err">{it.error}</span>
							) : it.status === "uploading" ? (
								t("attachments.uploading", {
									percent: Math.round(it.progress * 100),
								})
							) : (
								<>
									{formatBytes(it.size)}
									{it.fileOnly && ` · ${t("attachments.fileOnly")}`}
								</>
							)}
						</span>
					</span>
					{it.status === "uploading" && (
						<span
							aria-hidden
							className="absolute bottom-0 left-0 h-0.5 rounded bg-accent"
							style={{ width: `${Math.round(it.progress * 100)}%` }}
						/>
					)}
					<button
						type="button"
						aria-label={t("attachments.remove", { name: it.name })}
						className="absolute -top-1.5 -right-1.5 flex h-5 w-5 items-center justify-center rounded-full bg-fg text-bg"
						onClick={() => onRemove(it.key)}
					>
						<Icon name="close" size={11} />
					</button>
				</li>
			))}
		</ul>
	);
}
