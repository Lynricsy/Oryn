/**
 * 排队条：停靠在输入坞输入框上方，显示 steering（引导）与 follow-up（排队）队列。
 * - 支持乐观项：用户在忙时发送引导/排队消息后立即可见，待权威 `queue_update` 到达无缝接管。
 * - 解析 `<oryn-attachments>` 附件块、前导引用、Oryn 自动消息头与 artifact 选区块，渲染为正文 + 文件芯片；
 *   编辑时只修改纯正文，保存时无损拼回附件块与引用前缀。
 * - 支持编辑、删除、拖动排序（跨队列）、提前发送、清空。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAssetUrls } from "../../lib/asset-urls";
import { formatBytes } from "../../lib/format";
import { imageSrc, rasterMime } from "../../lib/image-src";
import { describeOrynHeader } from "../../lib/oryn-message";
import {
	filterUnmatchedOptimistic,
	type OptimisticAttachmentMeta,
	parseQueueMessage,
} from "../../lib/queue-message";
import { parseTitleCard } from "../../lib/title";
import { userMarkdown } from "../../lib/user-markdown";
import type { OmpCommand, RpcQueuedMessage } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore, useIndex, useThread } from "../../state/stores";
import type {
	OptimisticQueuedMessage,
	QueueLane,
} from "../../state/thread-reducer";
import { Badge } from "../ui/data";
import { FileTypeIcon } from "../ui/FileTypeIcon";
import { ImageViewer, type LightboxImage } from "../ui/ImageViewer";
import { Icon } from "../ui/icons";
import { Button, IconButton, TextInput } from "../ui/primitives";

type DisplayItem = RpcQueuedMessage & {
	pending?: boolean;
	optimisticAttachments?: OptimisticAttachmentMeta[];
};

async function run(threadId: string, frame: OmpCommand) {
	await realtime.cmd(threadId, frame);
	const data = await realtime.cmd(threadId, { type: "get_queue" });
	getThreadStore(threadId).getState().patchLocal({ queueItems: data });
}

function mergeLane(
	lane: QueueLane,
	authoritative: RpcQueuedMessage[] | undefined,
	texts: readonly string[] | undefined,
	optimistic: readonly OptimisticQueuedMessage[],
): DisplayItem[] {
	const base: DisplayItem[] =
		authoritative ??
		(texts ?? []).map((text, i) => ({
			id: `${lane === "steering" ? "s" : "f"}${i}`,
			text,
			editable: false,
			timestamp: 0,
		}));
	const mine = optimistic.filter((o) => o.lane === lane);
	if (mine.length === 0) return base;
	const unmatched = filterUnmatchedOptimistic(
		mine,
		authoritative ?? null,
		texts ?? null,
	);
	if (unmatched.length === 0) return base;
	const extra: DisplayItem[] = unmatched.map((o) => ({
		id: o.id,
		text: o.text,
		images: o.images,
		optimisticAttachments: o.attachments,
		editable: false,
		timestamp: o.timestamp,
		pending: true,
	}));
	return [...base, ...extra];
}

/** 紧凑单行行内格式：复用 `userMarkdown` 给行内代码/粗体/斜体/删除线着色，换行折叠为空格。 */
function QueueInlineText({ text }: { text: string }): ReactNode {
	const singleLine = text.replace(/\s*\n+\s*/g, " ").trim();
	const segments = userMarkdown(singleLine);
	return (
		<>
			{segments.map((seg, i) => {
				const key = `${i}-${seg.text.slice(0, 6)}`;
				const s = seg.style;
				if (s.marker)
					return (
						<span key={key} className="text-faint">
							{seg.text}
						</span>
					);
				if (s.code)
					return (
						<code
							key={key}
							className="rounded bg-inset/90 px-1 py-0.5 font-mono text-[11px] text-fg"
						>
							{seg.text}
						</code>
					);
				const cls = [
					s.strong ? "font-semibold" : "",
					s.em ? "italic" : "",
					s.del ? "line-through text-muted" : "",
					s.link ? "text-accent-ink underline decoration-accent/40" : "",
				]
					.filter(Boolean)
					.join(" ");
				return cls ? (
					<span key={key} className={cls}>
						{seg.text}
					</span>
				) : (
					<span key={key}>{seg.text}</span>
				);
			})}
		</>
	);
}

function Item({
	threadId,
	item,
	lane,
	index,
}: {
	threadId: string;
	item: DisplayItem;
	lane: QueueLane;
	index: number;
}) {
	const { t, i18n } = useTranslation();
	const assets = useAssetUrls();
	const [editing, setEditing] = useState<string | null>(null);
	const [previewIndex, setPreviewIndex] = useState<number | null>(null);
	const isSteer = lane === "steering";
	const parsed = parseQueueMessage(item.text);
	const senderId =
		parsed.oryn?.header.kind === "message" ? parsed.oryn.header.threadId : "";
	const senderTitle = useIndex((s) =>
		senderId ? s.threads[senderId]?.title : undefined,
	);
	const orynLabel = parsed.oryn
		? describeOrynHeader(parsed.oryn, t, i18n.language, () =>
				senderTitle ? parseTitleCard(senderTitle).text : undefined,
			)
		: null;

	const inlineImages = (item.images ?? [])
		.map((img, i) => ({
			src: imageSrc(img.data, img.mimeType, assets.blob),
			alt: t("image.alt", { n: i + 1 }),
		}))
		.filter((x): x is { src: string; alt: string } => x.src !== null);

	let nextInlineImg = 0;
	const gallery: LightboxImage[] = [];
	const attachments: Array<{
		key: string;
		name: string;
		mime?: string;
		size?: number;
		title?: string;
		thumb?: string;
		galleryIndex?: number;
	}> =
		parsed.attachments.length > 0
			? parsed.attachments.map((a, i) => {
					const isRaster = rasterMime(a.mime) !== null;
					let thumb: string | undefined;
					if (isRaster) {
						thumb = (a.id ? assets.attachment(a.id, true) : null) ?? undefined;
						if (!thumb && nextInlineImg < inlineImages.length) {
							thumb = inlineImages[nextInlineImg]?.src;
						}
						if (nextInlineImg < inlineImages.length) nextInlineImg++;
					}
					let galleryIndex: number | undefined;
					if (thumb) {
						galleryIndex = gallery.length;
						gallery.push({
							src: thumb,
							alt: a.name,
							caption: a.name,
							filename: a.name,
						});
					}
					return {
						key: `${a.path}-${i}`,
						name: a.name,
						mime: a.mime,
						size: a.size,
						title: `${a.name} (${formatBytes(a.size)})`,
						thumb,
						galleryIndex,
					};
				})
			: (item.optimisticAttachments ?? []).map((a, i) => {
					const isRaster = a.mime ? rasterMime(a.mime) !== null : false;
					let thumb: string | undefined;
					if (isRaster && nextInlineImg < inlineImages.length) {
						thumb = inlineImages[nextInlineImg]?.src;
						nextInlineImg++;
					}
					let galleryIndex: number | undefined;
					if (thumb) {
						galleryIndex = gallery.length;
						gallery.push({
							src: thumb,
							alt: a.name,
							caption: a.name,
							filename: a.name,
						});
					}
					return {
						key: `${a.name}-${i}`,
						name: a.name,
						mime: a.mime,
						size: a.size,
						title:
							a.size !== undefined
								? `${a.name} (${formatBytes(a.size)})`
								: a.name,
						thumb,
						galleryIndex,
					};
				});

	// 剩余未配对到附件芯片的内联图片也放进画廊并渲染小预览
	const extraGalleryItems: Array<{ src: string; alt: string; index: number }> =
		[];
	for (let i = nextInlineImg; i < inlineImages.length; i++) {
		const img = inlineImages[i];
		if (!img) continue;
		const idx = gallery.length;
		gallery.push({ src: img.src, alt: img.alt });
		extraGalleryItems.push({ src: img.src, alt: img.alt, index: idx });
	}

	// 图片文件已出现在附件芯片里时不重复显示「N 张图」
	const imageAttachments = attachments.filter((a) =>
		a.mime?.startsWith("image/"),
	).length;
	const extraImages = Math.max(
		0,
		(item.images?.length ?? 0) - imageAttachments - extraGalleryItems.length,
	);
	const hasChips =
		attachments.length > 0 ||
		parsed.artifactTitle !== null ||
		extraGalleryItems.length > 0 ||
		extraImages > 0;
	const displayBody = parsed.body.trim();
	return (
		<li
			draggable={item.editable}
			data-queue-lane={lane}
			data-queue-pending={item.pending ? "true" : undefined}
			onDragStart={(e) =>
				e.dataTransfer.setData(
					"text/x-oryn-queue",
					JSON.stringify({ id: item.id }),
				)
			}
			onDragOver={(e) => e.preventDefault()}
			onDrop={(e) => {
				e.preventDefault();
				if (item.pending) return;
				const raw = e.dataTransfer.getData("text/x-oryn-queue");
				if (!raw) return;
				const { id } = JSON.parse(raw) as { id: string };
				if (id !== item.id)
					void run(threadId, {
						type: "queue_move",
						queueId: id,
						toIndex: index,
						toLane: lane,
					});
			}}
			className={`flex items-start gap-1.5 rounded-lg border py-1.5 pr-1.5 pl-2.5 text-xs transition-colors duration-150 ${
				isSteer
					? "border-accent/25 bg-accent-soft/55"
					: "border-border bg-panel/70"
			} ${item.pending ? "opacity-80" : ""}`}
		>
			{item.editable && (
				<span
					className="mt-0.5 cursor-grab select-none text-ghost active:cursor-grabbing"
					aria-hidden="true"
				>
					⋮⋮
				</span>
			)}
			<span
				className={`mt-0.5 inline-flex shrink-0 items-center ${isSteer ? "text-accent-ink" : "text-faint"}`}
				aria-hidden="true"
			>
				<Icon
					name={isSteer ? "bolt" : "clock"}
					size={12}
					weight={isSteer ? "fill" : "regular"}
				/>
			</span>
			{editing !== null ? (
				<form
					className="flex min-w-0 flex-1 flex-col gap-1"
					onSubmit={(e) => {
						e.preventDefault();
						void run(threadId, {
							type: "queue_edit",
							queueId: item.id,
							text: parsed.rebuild(editing),
						}).then(() => setEditing(null));
					}}
				>
					<div className="flex min-w-0 items-center gap-1">
						<TextInput
							autoFocus
							className="h-6 py-0 text-xs"
							value={editing}
							onKeyDown={(e) => {
								if (e.key === "Escape") {
									e.preventDefault();
									setEditing(null);
								}
							}}
							onChange={(e) => setEditing(e.target.value)}
						/>
						<Button size="sm" type="submit">
							{t("common.save")}
						</Button>
					</div>
					{attachments.length > 0 && (
						<div className="flex flex-wrap items-center gap-1 opacity-80">
							{attachments.map((a) => (
								<span
									key={a.key}
									className="inline-flex max-w-44 items-center gap-1 rounded border border-border/70 bg-panel/70 px-1.5 py-0.5 text-2xs text-muted"
								>
									{a.thumb ? (
										<img
											src={a.thumb}
											alt={a.name}
											className="h-4 w-4 shrink-0 rounded-[3px] object-cover"
										/>
									) : (
										<FileTypeIcon name={a.name} mime={a.mime} size={12} />
									)}
									<span className="truncate">{a.name}</span>
								</span>
							))}
						</div>
					)}
				</form>
			) : (
				<div className="flex min-w-0 flex-1 flex-col gap-1">
					{orynLabel && (
						<span className="inline-flex w-fit max-w-full items-center gap-1 truncate rounded bg-inset px-1.5 py-0.5 font-medium text-2xs text-muted">
							{orynLabel}
						</span>
					)}
					{parsed.quote && (
						<div
							className="flex min-w-0 items-center gap-1 border-border-strong border-l-2 pl-1.5 text-2xs text-muted"
							title={parsed.quote}
						>
							<Icon name="quote" size={10} className="shrink-0 opacity-70" />
							<span className="min-w-0 truncate">
								{parsed.quote.replace(/\s*\n+\s*/g, " ")}
							</span>
						</div>
					)}
					{displayBody && (
						<span
							className="min-w-0 truncate text-fg leading-4"
							title={displayBody}
						>
							<QueueInlineText text={displayBody} />
						</span>
					)}
					{hasChips && (
						<div
							data-testid="queue-attachments"
							className="flex flex-wrap items-center gap-1"
						>
							{attachments.map((a) =>
								a.thumb && a.galleryIndex !== undefined ? (
									<button
										key={a.key}
										type="button"
										onClick={() => setPreviewIndex(a.galleryIndex ?? 0)}
										title={`${a.title ?? a.name} · ${t("image.lightbox.fullscreen")}`}
										className="inline-flex max-w-48 cursor-zoom-in items-center gap-1.5 rounded-md border border-border/80 bg-panel/85 py-0.5 pr-1.5 pl-0.5 text-2xs text-fg hover:bg-hover focus-visible:outline-2 focus-visible:outline-[var(--ring)]"
									>
										<img
											src={a.thumb}
											alt={a.name}
											loading="lazy"
											className="h-5 w-5 shrink-0 rounded-[4px] border border-border/60 bg-inset object-cover"
										/>
										<span className="min-w-0 truncate">{a.name}</span>
										{a.size !== undefined && (
											<span className="shrink-0 text-faint tabular-nums">
												{formatBytes(a.size)}
											</span>
										)}
									</button>
								) : (
									<span
										key={a.key}
										title={a.title}
										className="inline-flex max-w-48 items-center gap-1 rounded-md border border-border/80 bg-panel/85 px-1.5 py-0.5 text-2xs text-fg"
									>
										<FileTypeIcon name={a.name} mime={a.mime} size={13} />
										<span className="min-w-0 truncate">{a.name}</span>
										{a.size !== undefined && (
											<span className="shrink-0 text-faint tabular-nums">
												{formatBytes(a.size)}
											</span>
										)}
									</span>
								),
							)}
							{parsed.artifactTitle && (
								<span className="inline-flex max-w-44 items-center gap-1 rounded-md border border-border/80 bg-panel/85 px-1.5 py-0.5 text-2xs text-muted">
									<Icon name="cursor" size={11} className="shrink-0" />
									<span className="min-w-0 truncate">
										{parsed.artifactTitle}
									</span>
								</span>
							)}
							{extraGalleryItems.map((img) => (
								<button
									key={img.index}
									type="button"
									onClick={() => setPreviewIndex(img.index)}
									aria-label={t("image.open", { n: img.index + 1 })}
									title={t("image.lightbox.fullscreen")}
									className="h-5 w-5 shrink-0 cursor-zoom-in overflow-hidden rounded border border-border/80 bg-inset focus-visible:outline-2 focus-visible:outline-[var(--ring)]"
								>
									<img
										src={img.src}
										alt={img.alt}
										className="h-full w-full object-cover"
									/>
								</button>
							))}
							{extraImages > 0 && (
								<span className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border/80 bg-panel/85 px-1.5 py-0.5 text-2xs text-muted">
									<Icon name="image" size={11} />
									{t("queue.imagesCount", { count: extraImages })}
								</span>
							)}
						</div>
					)}
				</div>
			)}
			{item.pending && (
				<span className="mt-0.5 shrink-0 pr-1 text-2xs text-faint">
					{t("queue.sending")}
				</span>
			)}
			{item.editable && editing === null && (
				<div className="flex shrink-0 items-center gap-0.5">
					{lane === "followUp" && (
						<IconButton
							icon="bolt"
							size={13}
							box="xs"
							label={t("queue.promote")}
							onClick={() =>
								void run(threadId, {
									type: "promote_queued_message",
									message: item.text,
								})
							}
						/>
					)}
					<IconButton
						icon="edit"
						size={13}
						box="xs"
						label={t("queue.edit")}
						onClick={() => setEditing(parsed.body)}
					/>
					<IconButton
						icon="close"
						size={13}
						box="xs"
						label={t("queue.remove")}
						onClick={() =>
							void run(threadId, { type: "queue_remove", queueId: item.id })
						}
					/>
				</div>
			)}
			{previewIndex !== null && gallery.length > 0 && (
				<ImageViewer
					images={gallery}
					initialIndex={previewIndex}
					onClose={() => setPreviewIndex(null)}
				/>
			)}
		</li>
	);
}
export function QueueBar({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const queue = useThread(threadId, (s) => s.queue);
	const items = useThread(threadId, (s) => s.queueItems);
	const optimistic = useThread(threadId, (s) => s.optimisticQueue);
	const [open, setOpen] = useState(true);
	const steering = mergeLane(
		"steering",
		items?.steering,
		queue?.steering,
		optimistic,
	);
	const followUp = mergeLane(
		"followUp",
		items?.followUp,
		queue?.followUp,
		optimistic,
	);
	const total = steering.length + followUp.length;
	if (total === 0) return null;
	const hasAuthoritative =
		(queue?.steering.length ?? 0) + (queue?.followUp.length ?? 0) > 0 ||
		(items?.steering.length ?? 0) + (items?.followUp.length ?? 0) > 0;
	const lanes: { lane: QueueLane; list: DisplayItem[] }[] = [
		{ lane: "steering", list: steering },
		{ lane: "followUp", list: followUp },
	];
	return (
		<div data-testid="queue-bar" className="px-3.5 py-2.5">
			<div className="flex items-center gap-2 text-muted text-xs">
				<button
					type="button"
					className="-ml-1 flex items-center gap-1.5 rounded-md px-1 py-0.5 font-medium transition-colors duration-150 hover:bg-hover hover:text-fg"
					onClick={() => setOpen((v) => !v)}
					aria-expanded={open}
				>
					<Icon name={open ? "chevronDown" : "chevronRight"} size={12} />
					{t("queue.heading")}
					<Badge tone={steering.length > 0 ? "accent" : "neutral"}>
						{total}
					</Badge>
				</button>
				<div className="flex-1" />
				{hasAuthoritative && (
					<Button
						size="sm"
						variant="ghost"
						onClick={() => void run(threadId, { type: "queue_clear" })}
					>
						{t("queue.clear")}
					</Button>
				)}
			</div>
			{open && (
				<div className="mt-2 flex flex-col gap-2">
					{lanes.map(({ lane, list }) =>
						list.length === 0 ? null : (
							<div key={lane}>
								<div className="mb-1 flex items-center gap-1.5 font-medium text-2xs text-faint">
									<span>{t(`queue.${lane}`)}</span>
									<span className="tabular-nums opacity-75">{list.length}</span>
								</div>
								<ul className="flex flex-col gap-1">
									{list.map((item, i) => (
										<Item
											key={item.id}
											threadId={threadId}
											item={item}
											lane={lane}
											index={i}
										/>
									))}
								</ul>
							</div>
						),
					)}
				</div>
			)}
		</div>
	);
}
