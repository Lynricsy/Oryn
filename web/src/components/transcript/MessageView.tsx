/**
 * 转录中的单条消息渲染：按 omp 的信息量全量展示（思考块、工具卡片、回合脚注、错误、bash 执行、自定义消息等）。
 * 所有字段都按「可能残缺」处理——条目与流式帧都是外部 JSON。
 */
import type { TFunction } from "i18next";
import { memo, type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Orb, ThreadState } from "../../api/client";
import { assistantFileContentUrl } from "../../api/library-queries";
import { useAssetUrls } from "../../lib/asset-urls";
import { asyncFrameOf } from "../../lib/async-jobs";
import {
	type MessageAttachment,
	parseAttachmentBlock,
} from "../../lib/attachments";
import { imageGroup } from "../../lib/context-menu/image-actions";
import { useContextTarget } from "../../lib/context-menu/registry";
import { copyWithToast } from "../../lib/context-menu/selection-groups";
import {
	firstCustomMessagePreview,
	isSilentAbortMessage,
	parseHandoffMessage,
} from "../../lib/custom-messages";
import { joinPath, orbFileSource } from "../../lib/file-source";
import {
	formatBytes,
	formatCost,
	formatDuration,
	formatTokens,
} from "../../lib/format";
import { imageSrc } from "../../lib/image-src";
import {
	describeOrynHeader,
	type OrynAutomationItem,
	type OrynHeader,
	type OrynResearchFinding,
	type OrynTone,
	orynTone,
	parseOrynMessage,
} from "../../lib/oryn-message";
import { normalizeQuote, splitLeadingQuote } from "../../lib/quote";
import { parseTitleCard } from "../../lib/title";
import { patchComposerDraft } from "../../state/composer-drafts";
import type { JsonRecord } from "../../state/message-builder";
import { locateEntry, runSideAsk, sideAskBusy } from "../../state/side-ask";
import {
	indexStore,
	peekThreadStore,
	useIndex,
	useThread,
} from "../../state/stores";
import { uiStore } from "../../state/ui";
import { ToolView, type ToolViewProps } from "../../tool-render/ToolView";
import type { ToolResultLike } from "../../tool-render/types";
import { isRecord, resultTextOf, toolSubject } from "../../tool-render/util";
import { Markdown } from "../markdown/Markdown";
import { Badge, Callout, Stat } from "../ui/data";
import { FileTypeIcon } from "../ui/FileTypeIcon";
import { ImageViewer, type LightboxImage } from "../ui/ImageViewer";
import { Icon, type IconName } from "../ui/icons";
import { Menu, type MenuItem, ThreadStateIcon } from "../ui/primitives";
import { UserAvatar } from "../ui/UserAvatar";
import { AgentAvatar } from "./AgentAvatar";
import { useToolCallActive, useToolsExpand } from "./active-tools";
import { renderKnownCustomMessage } from "./CustomMessages";
import { type TranscriptContextValue, useTranscriptContext } from "./context";
import { Foldable } from "./Foldable";
import { BackgroundToolView, JobEventMessage } from "./JobEvents";
import { escapeAngles, MD_SIZE } from "./NoticeLine";
import { UserText } from "./UserText";

const CHIP =
	"inline-flex items-center gap-1.5 rounded-md border border-border bg-panel px-2 py-1 text-xs";

/** 用户气泡外观（真实消息与「发送中」的待发任务共用）。 */
const USER_BUBBLE =
	"w-fit min-w-0 max-w-[85%] rounded-lg rounded-br-xs border border-border bg-raised px-3.5 py-2";

/**
 * 用户气泡一行：气泡靠右；`avatar` 为真（用户回合起点）时在右侧头像列放用户头像，
 * 与助手回合顶部的头像同尺寸（28px）——外层须用 `pr-10` 让出这一列（头像与气泡间 12px）。头像只作装饰（aria-hidden）。
 * 这一行只含气泡本身（徽标、消息菜单都在行外），头像贴底即与气泡底边对齐，正对右下角的小圆角。
 */
export function UserBubble({
	avatar,
	dim,
	children,
}: {
	avatar: boolean;
	dim: boolean;
	children: ReactNode;
}): ReactNode {
	return (
		<div className="relative flex w-full justify-end">
			{avatar && (
				<span aria-hidden className="absolute -right-10 bottom-0 flex">
					<UserAvatar size={28} />
				</span>
			)}
			<div className={`${USER_BUBBLE} ${dim ? "opacity-80" : ""}`}>
				{children}
			</div>
		</div>
	);
}

/** 用户消息开头的引用块（引用回复）：左侧竖线、弱色，与下面的回复正文分开。 */
export const USER_QUOTE =
	"whitespace-pre-wrap break-words border-border-strong border-l-2 pl-2.5 text-muted text-sm leading-5";

export function textOfContent(content: unknown): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	return content
		.map((b) =>
			isRecord(b) && b.type === "text" && typeof b.text === "string"
				? b.text
				: "",
		)
		.filter(Boolean)
		.join("\n\n");
}

function Images({ content }: { content: unknown }): ReactNode {
	const { t } = useTranslation();
	const assets = useAssetUrls();
	const [viewingIndex, setViewingIndex] = useState<number | null>(null);
	const ctxRef = useContextTarget<HTMLDivElement>({
		items: (req) => {
			const img = req.target.closest("img");
			return img
				? [imageGroup(img.currentSrc || img.src, req.askThreadId)]
				: null;
		},
	});
	if (!Array.isArray(content)) return null;
	const imgs = content.filter(
		(b): b is { type: "image"; data: string; mimeType: string } =>
			isRecord(b) &&
			b.type === "image" &&
			typeof b.data === "string" &&
			typeof b.mimeType === "string",
	);
	if (imgs.length === 0) return null;
	const resolved = imgs.map((img, i) => ({
		img,
		src: imageSrc(img.data, img.mimeType, assets.blob),
		n: i + 1,
	}));
	const gallery: LightboxImage[] = [];
	const galleryIndexByRaw = new Map<number, number>();
	resolved.forEach((item, i) => {
		if (!item.src) return;
		galleryIndexByRaw.set(i, gallery.length);
		gallery.push({
			src: item.src,
			alt: t("image.alt", { n: item.n }),
		});
	});
	return (
		<div ref={ctxRef} className="mt-2 flex flex-wrap gap-2">
			{resolved.map(({ img, src, n }, i) => {
				if (!src)
					return (
						<span
							// biome-ignore lint/suspicious/noArrayIndexKey: 图片块无稳定 id
							key={i}
							className="rounded-md border border-border px-2 py-1 text-faint text-xs"
						>
							{t("image.unsupported", { type: img.mimeType })}
						</span>
					);
				const gIdx = galleryIndexByRaw.get(i) ?? 0;
				return (
					<button
						// biome-ignore lint/suspicious/noArrayIndexKey: 图片块无稳定 id
						key={i}
						type="button"
						className="cursor-zoom-in rounded-md focus-visible:outline-2 focus-visible:outline-[var(--ring)] focus-visible:outline-offset-2"
						data-ctx-touch=""
						onClick={() => setViewingIndex(gIdx)}
						aria-label={t("image.open", { n })}
					>
						<img
							src={src}
							alt=""
							className="max-h-64 max-w-full rounded-md border border-border"
							loading="lazy"
						/>
					</button>
				);
			})}
			{viewingIndex !== null && gallery.length > 0 && (
				<ImageViewer
					images={gallery}
					initialIndex={viewingIndex}
					onClose={() => setViewingIndex(null)}
				/>
			)}
		</div>
	);
}

/**
 * 用户消息附件块解析出的附件芯片：有下载地址（站内 `attachments/{id}/content`、分享页里已发布的公开 URL）
 * 时点击以附件形式下载，否则是不可点的芯片。
 */
export function AttachmentChips({
	attachments,
}: {
	attachments: MessageAttachment[];
}): ReactNode {
	const { t } = useTranslation();
	const assets = useAssetUrls();
	if (attachments.length === 0) return null;
	return (
		<ul
			className="mt-2 flex flex-wrap gap-1.5"
			aria-label={t("attachments.inMessage")}
		>
			{attachments.map((a) => {
				const body = (
					<>
						<FileTypeIcon name={a.name} mime={a.mime} size={14} />
						<span className="max-w-56 truncate">{a.name}</span>
						<span className="text-faint tabular-nums">
							{formatBytes(a.size)}
						</span>
					</>
				);
				const cls = CHIP;
				const url = a.id ? assets.attachment(a.id) : null;
				return (
					<li key={a.path}>
						{url ? (
							<a
								href={url}
								download={a.name}
								className={`${cls} hover:bg-hover`}
								title={a.path}
							>
								{body}
							</a>
						) : (
							<span className={cls} title={a.path}>
								{body}
							</span>
						)}
					</li>
				);
			})}
		</ul>
	);
}

/** 菜单项高度（`py-1.5` + 20px 行高）与容器内边距 / 间距，用于估算展开所需空间。 */
const MENU_ITEM_H = 32;
const MENU_CHROME_H = 12;

/**
 * 选择菜单展开方向：可见区取转录滚动容器去掉 scroll-padding（页头 / 输入坞覆盖的高度）后的部分；
 * 上方放得下就向上（贴近输入坞的常见情形），否则取上下两侧空间更大的一侧。
 */
function pickMenuSide(el: HTMLElement | null, items: number): "top" | "bottom" {
	if (!el) return "top";
	const need = items * MENU_ITEM_H + MENU_CHROME_H;
	const r = el.getBoundingClientRect();
	let top = 0;
	let bottom = window.innerHeight;
	const scroller = el.closest<HTMLElement>('[data-testid="transcript"]');
	if (scroller) {
		const s = scroller.getBoundingClientRect();
		const cs = getComputedStyle(scroller);
		top = s.top + (Number.parseFloat(cs.scrollPaddingTop) || 0);
		bottom = s.bottom - (Number.parseFloat(cs.scrollPaddingBottom) || 0);
	}
	const above = r.top - top;
	const below = bottom - r.bottom;
	return above >= need || above >= below ? "top" : "bottom";
}

/**
 * 消息菜单项：行内「⋯」与右键菜单共用。只读（分享页）只有「复制」；回退 / fork / 标签 / 引用整条都要改动线程。
 */
function messageMenuItems(o: {
	entryId?: string;
	copyText: () => string;
	copyLabel: string;
	/** 给出时多一项「引用整条」（右键菜单的助手消息） */
	quoteText?: () => string;
	/** 用户自己发的消息：多一项「回到这里编辑」（回退到它之前，原文放回输入区） */
	rewindable?: boolean;
	ctx: TranscriptContextValue;
	t: TFunction;
}): MenuItem[] {
	const { entryId, ctx, t } = o;
	const editable = entryId !== undefined && !ctx.readOnly;
	const items: MenuItem[] = [
		{
			key: "copy",
			icon: "copy",
			label: o.copyLabel,
			onSelect: () => void copyWithToast(o.copyText()),
		},
	];
	const { quoteText } = o;
	if (quoteText && editable)
		items.push({
			key: "quote",
			icon: "quote",
			label: t("contextMenu.quoteMessage"),
			onSelect: () =>
				patchComposerDraft(ctx.threadId, {
					quote: { text: normalizeQuote(quoteText()) },
				}),
		});
	if (editable && entryId && o.rewindable)
		items.push({
			key: "rewind",
			icon: "undo",
			label: t("transcript.editFromHere"),
			onSelect: () => ctx.rewindTo(entryId),
		});
	else if (editable && entryId && ctx.openRewind)
		items.push({
			key: "rewindHere",
			icon: "undo",
			label: t("transcript.rewindFromHere"),
			onSelect: () => ctx.openRewind?.(entryId),
		});
	if (editable && entryId)
		items.push(
			{
				key: "fork",
				icon: "fork",
				label: t("transcript.forkHere"),
				onSelect: () => ctx.forkFrom(entryId),
			},
			{
				key: "label",
				icon: "edit",
				label: t("transcript.label"),
				onSelect: () => ctx.labelEntry(entryId),
			},
		);
	return items;
}

/**
 * 消息操作菜单：行内渲染，悬停 / 聚焦时显现。打开时按可见空间（去掉页头与输入坞盖住的部分）选择向上或向下展开；
 * 菜单在顶层按视口夹位、限高滚动，不再滚动转录去迁就它（滚动会把锚点连同菜单一起带走）。
 * 显隐的透明度只加在「⋯」按钮上：菜单是外包元素的 DOM 后代，外包元素做 opacity 过渡时菜单的玻璃模糊会被截断。
 */
function MessageMenu({
	entryId,
	copyText,
	rewindable,
	className = "",
	chrome = "",
	touchVisible = true,
}: {
	entryId?: string;
	/** 「复制」的文本：点击时才生成（回合脚注要拼接整回合正文，不在每次渲染时做）。 */
	copyText: () => string;
	/** 见 `messageMenuItems` */
	rewindable?: boolean;
	/** 追加到外包元素上的定位类（例如回合中间消息右上角的浮动槽位）。 */
	className?: string;
	/** 追加到按钮上的外观类（底色、描边、阴影）：随按钮一起显隐，按钮透明时不留空框 */
	chrome?: string;
	/**
	 * 粗指针下常显（弱化按钮）。浮在内容上的槽位关掉：改为点按消息后显现——
	 * 外层 `group/msg` 可被点按聚焦（tabIndex=-1），靠 `group-focus-within` 显示；
	 * 不能依赖 `group-hover`，Tailwind 4 把它包在 `@media (hover: hover)` 里，触屏不触发。
	 */
	touchVisible?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const ctx = useTranscriptContext();
	const wrapRef = useRef<HTMLDivElement>(null);
	const [side, setSide] = useState<"top" | "bottom">("top");
	const items = messageMenuItems({
		entryId,
		copyText,
		copyLabel: t("common.copy"),
		rewindable,
		ctx,
		t,
	});
	const itemCount = items.length;
	return (
		<div ref={wrapRef} className={`group/mm ${className}`}>
			<Menu
				side={side}
				trigger={(p) => (
					<button
						type="button"
						className={`inline-grid h-6 w-6 place-items-center rounded-md text-ghost opacity-0 transition-opacity duration-150 hover:bg-hover hover:text-fg group-focus-within/mm:opacity-100 group-hover/msg:opacity-100 ${touchVisible ? "pointer-coarse:opacity-100" : "group-focus-within/msg:opacity-100"} ${chrome}`}
						aria-label={t("transcript.messageActions")}
						{...p}
						onClick={() => {
							if (!p["aria-expanded"])
								setSide(pickMenuSide(wrapRef.current, itemCount));
							p.onClick();
						}}
					>
						<Icon name="more" size={14} />
					</button>
				)}
				items={items}
			/>
		</div>
	);
}

/** 首行小签的图标：定时用时钟，线程报告用列表，GitHub 回流 / 触发用 GitHub 标志，确认卡结果用对勾圈，交给助手的运行结果用铃铛，后台研究的发现用放大镜，其余（agent 发来的消息）用机器人。 */
const ORYN_ICON: Record<OrynHeader["kind"], IconName> = {
	schedule: "clock",
	report: "list",
	message: "robot",
	github: "github",
	confirmation: "checkCircle",
	automation: "bell",
	research: "search",
	unknown: "robot",
};

/** 运行结果的结局 → 徽标色调；认不出的按中性。 */
const OUTCOME_TONE: Record<string, "ok" | "err" | "neutral" | "accent"> = {
	completed: "ok",
	failed: "err",
	error: "err",
	aborted: "neutral",
	needs_input: "accent",
};

/**
 * 交给助手的运行结果（M19）：逐项一张卡片——来源（定时 / 触发规则的名字）、线程胶囊（索引里的当前标题，
 * 否则用结果里的标题快照，链到该线程）、结局徽标、回复引用块（Markdown）与出错原因。
 */
function AutomationResults({ items }: { items: OrynAutomationItem[] }) {
	const { t } = useTranslation();
	const titles = useIndex((s) => s.threads);
	return (
		<ul
			data-testid="automation-results"
			className="flex min-w-0 flex-col gap-2"
		>
			{items.map((it, i) => {
				const thread = titles[it.threadId];
				const name =
					(thread && parseTitleCard(thread.title).text) ||
					it.title ||
					t("thread.untitled");
				return (
					<li
						// biome-ignore lint/suspicious/noArrayIndexKey: 同一线程可能出现多次，按位置区分
						key={i}
						data-testid="automation-item"
						className="flex min-w-0 flex-col gap-1.5 rounded-md border border-border bg-panel/60 px-2.5 py-2"
					>
						<span className="flex min-w-0 flex-wrap items-center gap-1.5 text-xs">
							{it.name && (
								<span className="inline-flex min-w-0 items-center gap-1 text-muted">
									<Icon
										name={it.kind === "trigger" ? "github" : "clock"}
										size={12}
										className="shrink-0 text-faint"
									/>
									<span className="truncate">{it.name}</span>
								</span>
							)}
							<Badge tone={OUTCOME_TONE[it.outcome] ?? "neutral"}>
								{t(`oryn.outcome.${it.outcome}`, { defaultValue: it.outcome })}
							</Badge>
						</span>
						<Link
							to={`/t/${it.threadId}`}
							className="inline-flex h-7 max-w-full items-center gap-1.5 self-start rounded-full border border-border bg-panel/70 pr-2.5 pl-2 font-medium text-fg text-xs transition-colors duration-150 hover:bg-hover"
						>
							{thread && <ThreadStateIcon state={thread.state} size={13} />}
							<span className="min-w-0 truncate">{name}</span>
						</Link>
						{it.reply?.trim() && (
							<blockquote className="min-w-0 border-border-strong border-l-2 pl-3 text-muted">
								<Markdown text={it.reply} />
							</blockquote>
						)}
						{it.error && (
							<p className="flex min-w-0 items-start gap-1.5 break-words text-err text-xs">
								<Icon name="alert" size={13} className="mt-px shrink-0" />
								<span className="min-w-0">{it.error}</span>
							</p>
						)}
					</li>
				);
			})}
		</ul>
	);
}

/** 来源链接只接受 http(s)；其余（`javascript:` 等）不显示。 */
function isWebUrl(url: string): boolean {
	try {
		const { protocol } = new URL(url);
		return protocol === "http:" || protocol === "https:";
	} catch {
		return false;
	}
}

/**
 * 后台研究的发现（M26）：逐项一张卡片——管线徽标、标题、纯文本摘要（来自网页 / 邮件，不按 Markdown 渲染）、
 * 来源链接（只有 http(s)，新标签页打开）与相关对话或线程的站内链接。
 */
function ResearchFindings({ items }: { items: OrynResearchFinding[] }) {
	const { t } = useTranslation();
	return (
		<ul data-testid="research-findings" className="flex min-w-0 flex-col gap-2">
			{items.map((it, i) => {
				const sources = it.sources.filter((s) => isWebUrl(s.url));
				return (
					<li
						// biome-ignore lint/suspicious/noArrayIndexKey: 发现没有稳定 id，按位置区分
						key={i}
						data-testid="research-finding"
						className="flex min-w-0 flex-col gap-1.5 rounded-md border border-border bg-panel/60 px-2.5 py-2"
					>
						<span className="flex min-w-0 flex-wrap items-center gap-1.5 text-xs">
							{it.pipeline && (
								<Badge>
									{t(`oryn.pipeline.${it.pipeline}`, {
										defaultValue: it.pipeline,
									})}
								</Badge>
							)}
							<span className="min-w-0 font-medium text-fg">{it.title}</span>
						</span>
						<p className="min-w-0 whitespace-pre-wrap break-words text-muted text-sm">
							{it.summary}
						</p>
						{sources.length > 0 && (
							<ul
								aria-label={t("oryn.researchSources")}
								className="flex min-w-0 flex-col gap-0.5 text-xs"
							>
								{sources.map((s, k) => (
									// biome-ignore lint/suspicious/noArrayIndexKey: 同一网址可能出现多次
									<li key={k} className="flex min-w-0 items-center gap-1.5">
										<Icon
											name="link"
											size={12}
											aria-hidden
											className="shrink-0 text-faint"
										/>
										<a
											href={s.url}
											target="_blank"
											rel="noreferrer noopener"
											className="min-w-0 truncate text-accent-ink hover:underline"
										>
											{s.title}
										</a>
										{s.date && (
											<span className="shrink-0 text-faint tabular-nums">
												{s.date}
											</span>
										)}
									</li>
								))}
							</ul>
						)}
						{it.threadId && (
							<Link
								to={`/t/${it.threadId}`}
								className="inline-flex min-w-0 items-center gap-1 self-start text-accent-ink text-xs hover:underline"
							>
								<Icon name="message" size={12} aria-hidden />
								<span className="truncate">{t("oryn.researchThread")}</span>
							</Link>
						)}
					</li>
				);
			})}
		</ul>
	);
}

/**
 * 线程报告（M9）：一枚可点的线程胶囊（索引里的当前标题 + 状态，链到该线程）+ 引用块里的回复正文（Markdown）
 * + 出错原因（有时）。不显示包裹标签与原始 id；索引里没有该线程时用报告里的标题快照（新格式在包裹的 JSON 里，
 * 旧格式在首行）与状态。
 */
function ThreadReport({
	threadId,
	title,
	state,
	content,
	error,
}: {
	threadId: string;
	title: string;
	state: string;
	content: string;
	error?: string;
}) {
	const { t } = useTranslation();
	const thread = useIndex((s) => s.threads[threadId]);
	const name =
		(thread && parseTitleCard(thread.title).text) ||
		title ||
		t("thread.untitled");
	const now = (thread?.state ?? state) as ThreadState;
	const stateText = t(`threadState.${now}`, { defaultValue: now });
	return (
		<div data-testid="thread-report" className="flex min-w-0 flex-col gap-2">
			<Link
				to={`/t/${threadId}`}
				aria-label={t("oryn.reportThread", { name, state: stateText })}
				className="inline-flex h-7 max-w-full items-center gap-1.5 self-start rounded-full border border-border bg-panel/70 pr-2.5 pl-2 font-medium text-fg text-xs transition-colors duration-150 hover:bg-hover"
			>
				{thread || now in STATE_KNOWN ? (
					<ThreadStateIcon state={now} size={13} />
				) : null}
				<span className="min-w-0 truncate">{name}</span>
				<span aria-hidden className="shrink-0 font-normal text-faint">
					{stateText}
				</span>
			</Link>
			{content.trim() && (
				<blockquote className="min-w-0 border-border-strong border-l-2 pl-3 text-muted">
					<Markdown text={content} />
				</blockquote>
			)}
			{error && (
				<p
					data-testid="thread-report-error"
					className="flex min-w-0 items-start gap-1.5 break-words text-err text-xs"
				>
					<Icon name="alert" size={13} className="mt-px shrink-0" />
					<span className="min-w-0">{error}</span>
				</p>
			)}
		</div>
	);
}

const STATE_KNOWN: Record<ThreadState, true> = {
	provisioning: true,
	awaiting_input: true,
	working: true,
	error: true,
	idle: true,
};

function UserMessage({
	message,
	entryId,
	live,
	turnStart,
}: {
	message: JsonRecord;
	entryId?: string;
	live: boolean;
	/** 连续用户消息中的第一条：右侧放用户头像。 */
	turnStart: boolean;
}) {
	const { t, i18n } = useTranslation();
	const ctx = useTranscriptContext();
	// 控制面追加的附件块不按原文显示，渲染成附件芯片
	const { text, attachments } = parseAttachmentBlock(
		textOfContent(message.content),
	);
	const irc = isRecord(message.parentIrc) ? message.parentIrc : null;
	const synthetic = message.synthetic === true;
	const steer = message.steering === true || message.liveSteered === true;
	const body = irc ? String(irc.body ?? text) : text;
	const oryn = irc ? null : parseOrynMessage(body);
	// agent 发来的消息：发起方的名字取索引里的当前标题（比首行里的快照新）
	const senderId = oryn?.header.kind === "message" ? oryn.header.threadId : "";
	const senderTitle = useIndex((s) =>
		senderId ? s.threads[senderId]?.title : undefined,
	);
	const label = oryn
		? describeOrynHeader(oryn, t, i18n.language, () =>
				senderTitle ? parseTitleCard(senderTitle).text : undefined,
			)
		: "";
	// 用户自己写的消息才能「回到这里编辑」：agent / 定时等发来的消息、IRC 转述、合成消息不算
	const rewindable = !oryn && !irc && !synthetic;
	const ctxRef = useContextTarget<HTMLDivElement>({
		items: () => [
			{
				key: "message",
				items: messageMenuItems({
					entryId,
					copyText: () => text,
					copyLabel: t("contextMenu.copyMessage"),
					rewindable,
					ctx,
					t,
				}),
			},
		],
	});
	const menu = (
		<MessageMenu
			entryId={entryId}
			copyText={() => text}
			rewindable={rewindable}
		/>
	);
	if (oryn)
		return (
			<div
				ref={ctxRef}
				className="group/msg flex flex-col items-start gap-1"
				data-ask-scope="user"
			>
				<AutoMessageCard
					icon={ORYN_ICON[oryn.header.kind]}
					label={label}
					tone={orynTone(oryn)}
					dim={live}
					flags={
						<>
							{synthetic && (
								<Badge icon="bolt">{t("transcript.synthetic")}</Badge>
							)}
							{steer && <Badge icon="send">{t("transcript.steer")}</Badge>}
						</>
					}
				>
					{oryn.report && oryn.header.kind === "report" && (
						<ThreadReport
							threadId={oryn.report.threadId}
							title={oryn.report.title ?? oryn.header.title}
							state={oryn.header.state}
							content={oryn.report.content}
							error={oryn.report.error}
						/>
					)}
					{oryn.automation && <AutomationResults items={oryn.automation} />}
					{oryn.research && <ResearchFindings items={oryn.research} />}
					{oryn.body && (
						<Foldable
							lines={oryn.body.split("\n").length}
							testId="oryn-body"
							toggleClassName="-ml-1.5 pt-1"
						>
							<div className={`text-fg ${MD_SIZE}`}>
								{/* 正文可能含第三方原文（GitHub 评审 / 评论）：不加载图片，免得打开线程就向外站发请求 */}
								<Markdown text={escapeAngles(oryn.body)} images={false} />
							</div>
						</Foldable>
					)}
					<Images content={message.content} />
					<AttachmentChips attachments={attachments} />
				</AutoMessageCard>
				{menu}
			</div>
		);
	const quoted = splitLeadingQuote(body);
	return (
		<div
			ref={ctxRef}
			className="group/msg flex flex-col items-end gap-1"
			data-ask-scope="user"
		>
			{(irc || synthetic || steer) && (
				<div className="flex max-w-full flex-wrap justify-end gap-1">
					{irc && (
						<Badge icon="chats">
							{t("transcript.ircFrom", { from: String(irc.from ?? "") })}
						</Badge>
					)}
					{synthetic && <Badge icon="bolt">{t("transcript.synthetic")}</Badge>}
					{steer && <Badge icon="send">{t("transcript.steer")}</Badge>}
				</div>
			)}
			{!irc && <span className="sr-only">{t("transcript.you")}</span>}
			{/* 别的 agent 经 irc 发来的消息不是「你」说的，不放用户头像 */}
			<UserBubble avatar={turnStart && !irc} dim={live}>
				{quoted ? (
					<>
						<blockquote
							data-testid="user-quote"
							className={`${USER_QUOTE} ${quoted.rest ? "mb-1.5" : ""}`}
						>
							{quoted.quote}
						</blockquote>
						{quoted.rest && <UserText text={quoted.rest} />}
					</>
				) : (
					body && <UserText text={body} />
				)}
				<Images content={message.content} />
				<AttachmentChips attachments={attachments} />
			</UserBubble>
			{menu}
		</div>
	);
}

/** 事件卡的语气 → 图标方块、外框与页眉分隔线的颜色；好消息只给图标方块上色，不让描边喧宾夺主。 */
const AUTO_TONE: Record<
	OrynTone,
	{ chip: string; card: string; rule: string }
> = {
	neutral: {
		chip: "border-border bg-panel text-muted",
		card: "border-border",
		rule: "border-border",
	},
	ok: {
		chip: "border-ok/25 bg-ok/12 text-ok",
		card: "border-border",
		rule: "border-border",
	},
	warn: {
		chip: "border-warn/30 bg-warn/15 text-warn-ink",
		card: "border-warn/40",
		rule: "border-warn/25",
	},
	err: {
		chip: "border-err/25 bg-err/10 text-err",
		card: "border-err/35",
		rule: "border-err/20",
	},
};

/**
 * 控制面代发的自动消息（GitHub 事件、定时运行、线程报告等）：不是主人说的话，不用靠右的用户气泡与头像，
 * 而是铺满正文列的事件卡。页眉是来源图标方块 + 来源标签，右侧是引导 / 系统注入徽标与「自动投递」；
 * 需要留意的结果（CI 失败、线程出错……，见 `orynTone`）给图标方块与描边上色。
 * 正文按 Markdown 排（GitHub 评审、定时任务的提示词本来就是 Markdown，链接可点），字号与内联提示同档，过长折叠。
 */
function AutoMessageCard({
	icon,
	label,
	tone,
	dim,
	flags,
	children,
}: {
	icon: IconName;
	label: string;
	tone: OrynTone;
	dim: boolean;
	flags: ReactNode;
	children: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const style = AUTO_TONE[tone];
	return (
		<section
			data-testid="oryn-message"
			data-tone={tone}
			aria-label={label}
			className={`@container w-full min-w-0 overflow-hidden rounded-xl border bg-panel/60 ${style.card} ${dim ? "opacity-80" : ""}`}
		>
			<header
				className={`flex h-10 min-w-0 items-center gap-2.5 border-b bg-inset/40 pr-3 pl-2.5 ${style.rule}`}
			>
				<span
					aria-hidden
					className={`inline-grid size-6 shrink-0 place-items-center rounded-md border ${style.chip}`}
				>
					<Icon name={icon} size={13} />
				</span>
				<span
					title={label}
					className="min-w-0 flex-1 truncate font-medium text-[13px] text-fg"
				>
					{label}
				</span>
				{flags}
				{/* 窄卡片让位给来源标签：标签被截断比少一句「自动投递」更糟 */}
				<span className="shrink-0 text-2xs text-faint @max-sm:hidden">
					{t("transcript.automated")}
				</span>
			</header>
			<div className="flex min-w-0 flex-col gap-2 px-3.5 py-3">{children}</div>
		</section>
	);
}

/** 思考块文本压成一行预览：完成后取开头，流式中取末尾。 */
function thinkingPreview(text: string, streaming: boolean): string {
	const flat = text.replace(/\s+/g, " ").trim();
	if (flat.length <= 240) return flat;
	return streaming ? `…${flat.slice(-240)}` : flat.slice(0, 240);
}

function ThinkingBlock({
	text,
	streaming,
}: {
	text: string;
	streaming: boolean;
}) {
	const { t } = useTranslation();
	const expand = useToolsExpand();
	const [userOpen, setUserOpen] = useState<boolean | null>(null);
	const [prevEpoch, setPrevEpoch] = useState(expand.epoch);
	if (expand.epoch !== prevEpoch) {
		setPrevEpoch(expand.epoch);
		setUserOpen(null);
	}
	const open = userOpen ?? expand.expanded;
	return (
		<div>
			<button
				type="button"
				className="group/think flex h-7 w-full min-w-0 items-center gap-2 rounded-md pr-[11px] text-left text-[12.5px] text-muted hover:text-fg"
				onClick={() => setUserOpen(!open)}
				aria-expanded={open}
			>
				<Icon name="brain" size={14} className="shrink-0 text-faint" />
				<span className="shrink-0 whitespace-nowrap">
					{streaming ? t("transcript.thinkingLive") : t("transcript.thinking")}
				</span>
				<span className="min-w-0 flex-1 truncate text-faint italic">
					{open ? "" : thinkingPreview(text, streaming)}
				</span>
				<Icon
					name="chevronRight"
					size={12}
					className={`shrink-0 text-ghost transition-transform duration-150 group-hover/think:text-faint ${open ? "rotate-90" : ""}`}
				/>
			</button>
			{open && (
				<div
					data-ask-scope="thinking"
					className={`mt-0.5 mb-1.5 whitespace-pre-wrap break-words pl-[22px] text-muted text-xs leading-relaxed ${streaming ? "stream-caret" : ""}`}
				>
					{text}
				</div>
			)}
		</div>
	);
}

function toolResultLike(raw: unknown): ToolResultLike | undefined {
	if (!isRecord(raw)) return undefined;
	return {
		content: Array.isArray(raw.content)
			? (raw.content as ToolResultLike["content"])
			: [],
		details: raw.details,
		isError: raw.isError === true,
	};
}

/**
 * 线程 orb 上文件的下载地址（`desktop_record` 卡片内嵌播放的录像）。调用时读 store 快照，不给每张工具卡多加一个订阅：
 * 卡片随转录上下文重渲染时会重新取。orb 的选取与 `useOrbOf` 相同（线程快照与索引里较新的一份）。
 * 线程没有 orb 或 orb 已删除时为 null；orb 停机时走停机预览（与文件面板一致），打开转录不会唤醒它。
 */
function threadOrbFileUrl(threadId: string, path: string): string | null {
	const s = peekThreadStore(threadId)?.getState();
	const orbId = s?.thread?.orbId ?? s?.orb?.id;
	if (!orbId) return null;
	const fromIndex: Orb | undefined = indexStore.getState().orbs[orbId];
	const fromThread = s?.orb?.id === orbId ? s.orb : undefined;
	const orb =
		fromIndex && fromThread
			? fromIndex.stateChangedAt >= fromThread.stateChangedAt
				? fromIndex
				: fromThread
			: (fromIndex ?? fromThread);
	if (!orb || orb.state === "deleted") return null;
	return orbFileSource(orbId, threadId, {
		offline: orb.state === "stopped",
	}).downloadUrl(path);
}

/** 工具卡片：调用参数来自 toolCall 块（或实时帧），结果来自 toolResult 条目（或 tool_execution_end）。 */
export function ToolCard({
	toolCallId,
	name,
	args,
	intent,
	streamingArgs,
}: {
	toolCallId: string;
	name: string;
	args?: unknown;
	intent?: string;
	streamingArgs?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const ctx = useTranscriptContext();
	const cwd = useThread(ctx.threadId, (s) => s.thread?.cwd);
	// 当前一批调用默认展开，模型开始下一批或回合结束时收起（见 active-tools.ts）；Rewind 模式 Ctrl+O 可批量展开
	const active = useToolCallActive(toolCallId);
	const expand = useToolsExpand();
	const live = ctx.liveTools[toolCallId];
	const entryResult = ctx.toolResults.get(toolCallId);
	const result = entryResult
		? toolResultLike(entryResult)
		: live?.ended
			? toolResultLike(live.result)
			: undefined;
	const running =
		!result && (Boolean(live && !live.ended) || streamingArgs === true);
	const partialResult =
		live && !live.ended ? toolResultLike(live.partialResult) : undefined;
	const partial = resultTextOf(partialResult);
	const toolName = live?.toolName ?? name;
	const toolArgs = args ?? live?.args;
	const cardRef = useRef<HTMLDivElement | null>(null);
	const ctxRef = useContextTarget<HTMLDivElement>({
		items: (req) => {
			const subject = toolSubject(toolName, toolArgs);
			const output = resultTextOf(result);
			const items: MenuItem[] = [];
			if (subject)
				items.push({
					key: "copySubject",
					icon: "copy",
					label:
						subject.kind === "command"
							? t("contextMenu.copyCommand")
							: t("contextMenu.copyPath"),
					onSelect: () => void copyWithToast(subject.text),
				});
			if (output)
				items.push({
					key: "copyOutput",
					icon: "copy",
					label: t("contextMenu.copyOutput"),
					onSelect: () => void copyWithToast(output),
				});
			const abs =
				subject?.kind !== "path"
					? null
					: subject.text.startsWith("/")
						? subject.text
						: cwd
							? joinPath(cwd, subject.text)
							: null;
			if (abs && req.askThreadId)
				items.push({
					key: "openFile",
					icon: "folderOpen",
					label: t("contextMenu.openInFiles"),
					onSelect: () => uiStore.getState().openFile(ctx.threadId, abs),
				});
			const threadId = req.askThreadId;
			const el = cardRef.current;
			if (threadId && el) {
				const busy = sideAskBusy(threadId);
				const r = el.getBoundingClientRect();
				const entryId =
					el.closest("[data-index]")?.getAttribute("data-entry-id") ??
					undefined;
				items.push({
					key: "explain",
					icon: "sparkle",
					label: t("contextMenu.explainTool"),
					disabled: busy,
					title: busy ? t("sideAsk.busy") : undefined,
					onSelect: () =>
						void runSideAsk(
							{ kind: "tool" },
							{
								threadId,
								text: subject
									? `${toolName}: ${subject.text}`
									: `${toolName}: ${JSON.stringify(toolArgs ?? {}).slice(0, 300)}`,
								before: "",
								after: "",
								source: {
									kind: "tool",
									toolName,
									...locateEntry(ctx.threadId, entryId),
									spans: false,
								},
								anchor: {
									left: r.left,
									top: r.top,
									right: r.right,
									bottom: r.bottom,
								},
							},
						),
				});
			}
			return items.length ? [{ key: "tool", items }] : null;
		},
	});
	const view: ToolViewProps = {
		rootRef: (el) => {
			cardRef.current = el;
			ctxRef(el);
		},
		name: toolName,
		args: toolArgs,
		result: result
			? { ...result, isError: result.isError || live?.isError }
			: undefined,
		running,
		defaultOpen: active || expand.expanded,
		expandEpoch: expand.epoch,
		intent: intent ?? live?.intent,
		partial,
		partialResult,
		startedAt: live?.startedAt,
		host:
			// 只读（分享页）不提供子 agent 转录查看；头像在分享页只用公开身份（见 AgentAvatar）
			ctx.readOnly
				? { AgentAvatar }
				: {
						hasAgent: () => true,
						openAgent: (id) => ctx.openSubagent(id),
						// artifact 卡片（线程页开右面板，协调对话开整页）；只读的分享页不给，卡片不可点
						openArtifact: ctx.openArtifact,
						// desktop_record 的录像：线程 orb 上的文件；只读的分享页不给，卡片只显示路径
						orbFileUrl: (path) => threadOrbFileUrl(ctx.threadId, path),
						openOrbFile: (path) =>
							uiStore.getState().openFile(ctx.threadId, path),
						// computer_record 的录像：存进助手文件库的那份；只读的分享页不给，卡片只显示电脑上的路径
						libraryFileUrl: (id) => assistantFileContentUrl(id),
						AgentAvatar,
					},
	};
	// 转入后台的调用：卡片跟着任务的实时状态走（头部标记、最近输出、任务入口）
	return asyncFrameOf(result?.details) ? (
		<BackgroundToolView
			threadId={ctx.threadId}
			toolCallId={toolCallId}
			openable={!ctx.readOnly}
			view={view}
		/>
	) : (
		<ToolView {...view} />
	);
}

/** 一个回合（两条用户消息之间）的累计用量：各条助手消息的读数相加。模型名在回合顶部的头像行里。 */
export interface TurnUsage {
	hasUsage: boolean;
	input: number;
	output: number;
	cacheRead: number;
	cost: number;
	/** 各条消息 `duration` 之和；没有任何一条带耗时时为 null。 */
	duration: number | null;
	/** 回合内的助手消息；「复制」时才从中拼出正文。 */
	messages: readonly JsonRecord[];
}

export function summarizeTurn(messages: readonly JsonRecord[]): TurnUsage {
	const n = (v: unknown) => (typeof v === "number" ? v : 0);
	const sum: TurnUsage = {
		hasUsage: false,
		input: 0,
		output: 0,
		cacheRead: 0,
		cost: 0,
		duration: null,
		messages,
	};
	for (const m of messages) {
		if (isRecord(m.usage)) {
			sum.hasUsage = true;
			sum.input += n(m.usage.input);
			sum.output += n(m.usage.output);
			sum.cacheRead += n(m.usage.cacheRead);
			if (isRecord(m.usage.cost)) sum.cost += n(m.usage.cost.total);
		}
		if (typeof m.duration === "number")
			sum.duration = (sum.duration ?? 0) + m.duration;
	}
	return sum;
}

/** 回合内全部助手正文（段间空一行），供回合脚注的「复制」。 */
function turnText(turn: TurnUsage): string {
	const texts: string[] = [];
	for (const m of turn.messages) {
		const text = textOfContent(m.content);
		if (text) texts.push(text);
	}
	return texts.join("\n\n");
}

/** 回合脚注里的单个读数胶囊。 */
const META_CHIP =
	"inline-flex h-6 shrink-0 items-center gap-1 whitespace-nowrap rounded-md border border-border px-1.5 tabular-nums [&_svg]:text-faint";

/**
 * 回合脚注：整回合的累计用量（每项一个细描边胶囊）+ 消息操作；只挂在回合最后一条助手消息下。
 * 始终单行：按自身宽度（容器查询）先藏缓存、再藏耗时。模型名不在这里重复（见回合顶部的头像行）。
 */
function TurnMeta({ turn, entryId }: { turn: TurnUsage; entryId?: string }) {
	const { t } = useTranslation();
	return (
		<div className="@container mt-2">
			<div className="flex min-w-0 flex-nowrap items-center gap-1.5 text-2xs text-muted">
				{turn.hasUsage && (
					<>
						<span className={META_CHIP}>
							<Stat icon="send" label={t("stat.input")}>
								{formatTokens(turn.input)}
							</Stat>
						</span>
						<span className={META_CHIP}>
							<Stat icon="arrowDown" label={t("stat.output")}>
								{formatTokens(turn.output)}
							</Stat>
						</span>
						{turn.cacheRead > 0 && (
							<span className={`${META_CHIP} @max-md:hidden`}>
								<Stat icon="database" label={t("stat.cache")}>
									{formatTokens(turn.cacheRead)}
								</Stat>
							</span>
						)}
						{turn.cost > 0 && (
							// 成本不配图标：formatCost 自带 $（与输入坞页脚同一写法），再加货币图标会读成「$ $0.18」
							<span className={META_CHIP} title={t("stat.cost")}>
								<span className="sr-only">{t("stat.cost")}</span>
								{formatCost(turn.cost)}
							</span>
						)}
					</>
				)}
				{turn.duration !== null && (
					<span className={`${META_CHIP} @max-sm:hidden`}>
						<Stat icon="timer" label={t("stat.duration")}>
							{formatDuration(turn.duration)}
						</Stat>
					</span>
				)}
				<span className="flex-1" />
				<MessageMenu entryId={entryId} copyText={() => turnText(turn)} />
			</div>
		</div>
	);
}

/**
 * 转录的纵向节奏只有两档（数值取自 D 稿）：连续的过程块（思考行、工具卡）之间 `GAP_PROC`（D 稿
 * `.tools` 的 gap 6px），正文与过程块之间、以及正文段与正文段之间 `GAP_FLOW`（D 稿工具组下方 12px）。
 * 消息内的块与虚拟列表的相邻行（Transcript 按行首行尾的块类型给行加上边距）用同一套规则。
 */
export const GAP_PROC = 6;
export const GAP_FLOW = 12;

/** 块在节奏里的类别：过程行或正文。 */
export type FlowKind = "proc" | "prose";

export function flowGap(prev: FlowKind, next: FlowKind): number {
	return prev === "proc" && next === "proc" ? GAP_PROC : GAP_FLOW;
}

/** 助手消息内容块的节奏类别；不渲染的块返回 null（与 AssistantMessage 的渲染条件一致）。 */
function blockKind(b: unknown): FlowKind | null {
	if (!isRecord(b)) return null;
	switch (b.type) {
		case "text":
			return typeof b.text === "string" && b.text ? "prose" : null;
		case "thinking":
			return typeof b.thinking === "string" && b.thinking ? "proc" : null;
		case "redactedThinking":
		case "toolCall":
			return "proc";
		case "image":
			return "prose";
		default:
			return null;
	}
}

/** 助手消息首尾两块的节奏类别（Transcript 用来定相邻行的间距）。 */
export function assistantEdges(
	message: JsonRecord,
	live: boolean,
	hasFooter: boolean,
): [FlowKind, FlowKind] {
	const content = Array.isArray(message.content) ? message.content : [];
	// 只从两端找第一个会渲染的块：流式时每个增量都要重算全部行的间距，不能逐块遍历
	let head: FlowKind | null = null;
	for (let i = 0; i < content.length && head === null; i++)
		head = blockKind(content[i]);
	let tail: FlowKind | null = null;
	for (let i = content.length - 1; i >= 0 && tail === null; i--)
		tail = blockKind(content[i]);
	const stop = message.stopReason;
	const silentAbort = stop === "aborted" && isSilentAbortMessage(message);
	// 停止提示与回合脚注都是正文层的块；Core 静默中止不渲染提示
	const tailProse =
		hasFooter || stop === "error" || (stop === "aborted" && !silentAbort);
	const last = tailProse ? "prose" : (tail ?? "prose");
	return [head ?? "prose", live && head === null ? "prose" : last];
}

/** 等待 agent 首个可见输出时的占位：三个依次跃动的点（待回复的回合头下、空的流式助手消息里）。 */
export function TypingDots(): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			role="status"
			aria-label={t("thread.working")}
			data-testid="typing-dots"
			className="flex h-6 items-center gap-1 text-faint"
		>
			{[0, 1, 2].map((i) => (
				<span
					key={i}
					aria-hidden
					className="typing-dot h-1.5 w-1.5 rounded-full bg-current"
					style={{ animationDelay: `${i * 160}ms` }}
				/>
			))}
		</div>
	);
}

/**
 * 回合中间的助手消息自己的操作菜单：浮在消息右上角（压在上方间距与首块右上角），带底色描边，
 * 只在悬停 / 聚焦时显现——左侧不再留头像列，菜单不占正文宽度。槽位只管定位，底色描边画在按钮上随它显隐。
 */
const FLOAT_SLOT = "absolute -top-3 right-0 z-10";
const FLOAT_CHROME = "border border-border bg-panel shadow-xs";

function AssistantMessage({
	message,
	entryId,
	live,
	turn,
}: {
	message: JsonRecord;
	entryId?: string;
	live: boolean;
	turn?: TurnUsage;
}) {
	const { t } = useTranslation();
	const ctx = useTranscriptContext();
	const content = Array.isArray(message.content) ? message.content : [];
	const lastIdx = content.length - 1;
	const stop =
		typeof message.stopReason === "string" ? message.stopReason : undefined;
	const error =
		typeof message.errorMessage === "string" ? message.errorMessage : undefined;
	// 回合末尾的消息由脚注承载整回合菜单；其余已结束的消息各自带一个菜单
	const ownMenu = !live && !turn;
	const ctxRef = useContextTarget<HTMLDivElement>({
		items: () => [
			{
				key: "message",
				items: messageMenuItems({
					entryId,
					copyText: () => textOfContent(content),
					copyLabel: t("contextMenu.copyMessage"),
					quoteText: () => textOfContent(content),
					ctx,
					t,
				}),
			},
		],
	});
	let prev: FlowKind | null = null;
	const blocks: ReactNode[] = [];
	content.forEach((b, i) => {
		const kind = blockKind(b);
		if (kind === null || !isRecord(b)) return;
		const key = `${i}-${String(b.type)}`;
		const streaming = live && i === lastIdx;
		const marginTop = prev === null ? 0 : flowGap(prev, kind);
		prev = kind;
		let node: ReactNode;
		switch (b.type) {
			case "text":
				node = <Markdown text={String(b.text)} streaming={streaming} />;
				break;
			case "thinking":
				node = (
					<ThinkingBlock text={String(b.thinking)} streaming={streaming} />
				);
				break;
			case "redactedThinking":
				node = (
					<div className="flex h-7 items-center gap-2 text-faint text-xs italic">
						<Icon name="brain" size={14} className="shrink-0" />
						{t("transcript.redactedThinking")}
					</div>
				);
				break;
			case "toolCall":
				node = (
					<ToolCard
						toolCallId={typeof b.id === "string" ? b.id : ""}
						name={typeof b.name === "string" ? b.name : "tool"}
						args={b.arguments}
						intent={typeof b.intent === "string" ? b.intent : undefined}
						streamingArgs={streaming}
					/>
				);
				break;
			default:
				node = <Images content={[b]} />;
		}
		blocks.push(
			<div
				key={
					b.type === "toolCall" && typeof b.id === "string" && b.id ? b.id : key
				}
				// 助手正文里的表格可伸出正文列（宽度按转录行，见 .md-wide）
				className={
					b.type === "text"
						? `md-wide${streaming ? " stream-caret" : ""}`
						: undefined
				}
				data-tool-call-id={
					b.type === "toolCall" && typeof b.id === "string" ? b.id : undefined
				}
				data-tool-name={
					b.type === "toolCall" && typeof b.name === "string"
						? b.name
						: undefined
				}
				style={marginTop ? { marginTop } : undefined}
			>
				{node}
			</div>,
		);
	});
	return (
		// 有浮动菜单时，外层可被点按聚焦（不进 Tab 顺序），触屏点正文即显出菜单，点别处收起。
		// data-quote-scope：选区完整落在这一条消息里时可以「引用回复」（见 quote-selection）
		<div
			ref={ctxRef}
			className="group/msg relative outline-none"
			tabIndex={ownMenu ? -1 : undefined}
			data-quote-scope=""
		>
			{ownMenu && (
				<MessageMenu
					entryId={entryId}
					copyText={() => textOfContent(content)}
					touchVisible={false}
					className={FLOAT_SLOT}
					chrome={FLOAT_CHROME}
				/>
			)}
			{blocks}
			{/* 流式中还没有可见块（空 content，或只有空的 text / thinking 块）：接力回合开始时的三点，直到首个可见块 */}
			{live && blocks.length === 0 && <TypingDots />}
			{(stop === "error" ||
				(stop === "aborted" && !isSilentAbortMessage(message))) && (
				<div style={blocks.length > 0 ? { marginTop: GAP_FLOW } : undefined}>
					<Callout
						tone={stop === "error" ? "err" : "neutral"}
						icon={stop === "error" ? "alert" : "stop"}
					>
						{stop === "aborted"
							? t("transcript.aborted")
							: t("transcript.error", { message: error ?? "" })}
					</Callout>
				</div>
			)}
			{!live && turn && <TurnMeta turn={turn} entryId={entryId} />}
		</div>
	);
}

function ExecutionMessage({ message }: { message: JsonRecord }) {
	const { t } = useTranslation();
	const isBash = message.role === "bashExecution";
	const code = String((isBash ? message.command : message.code) ?? "");
	const exit =
		typeof message.exitCode === "number" ? message.exitCode : undefined;
	return (
		<div className="overflow-hidden rounded-lg border border-border bg-inset text-xs">
			<div className="flex min-w-0 items-center gap-2 border-border border-b px-3 py-1.5">
				<Icon
					name={isBash ? "terminal" : "code"}
					size={13}
					className="shrink-0 text-faint"
				/>
				<span className="min-w-0 flex-1 truncate font-mono text-fg">
					{isBash ? code : code.split("\n")[0]}
				</span>
				{message.excludeFromContext === true && (
					<Badge>{t("transcript.excluded")}</Badge>
				)}
				{isBash && message.movedCwd === true && (
					<Badge tone="warn" title={t("transcript.movedCwdHint")}>
						{t("transcript.movedCwd")}
					</Badge>
				)}
				{isBash && message.errexit === true && (
					<Badge tone="warn" title={t("transcript.errexitHint")}>
						set -e
					</Badge>
				)}
				{message.cancelled === true && (
					<Badge tone="warn">{t("transcript.cancelled")}</Badge>
				)}
				{exit !== undefined && (
					<Badge tone={exit === 0 ? "ok" : "err"}>{`exit ${exit}`}</Badge>
				)}
			</div>
			<pre className="max-h-72 overflow-auto whitespace-pre-wrap px-3 py-2 font-mono text-fg">
				{String(message.output ?? "")}
			</pre>
		</div>
	);
}

/** 超过这个行数或字数的扩展注入消息默认收成一行摘要（如把整份清单列给模型的启动提示），点开看全文。 */
const CUSTOM_FOLD_LINES = 6;
const CUSTOM_FOLD_CHARS = 480;

function CustomMessage({ message }: { message: JsonRecord }) {
	const text = textOfContent(message.content);
	const lines = text.split("\n");
	const long =
		lines.length > CUSTOM_FOLD_LINES || text.length > CUSTOM_FOLD_CHARS;
	const [open, setOpen] = useState(!long);
	const label = String(message.customType ?? message.role ?? "");
	if (!long)
		return (
			<div className="rounded-lg border border-border-strong border-dashed px-3 py-2">
				<div className="mb-1.5">
					<Badge icon="message">{label}</Badge>
				</div>
				<Markdown text={escapeAngles(text)} />
				<Images content={message.content} />
			</div>
		);
	// 摘要取第一行正文：跳过 `<system-notice>` / `<advisory ...>` 这类包裹标签行
	const first = firstCustomMessagePreview(text);
	return (
		<div className="overflow-hidden rounded-lg border border-border-strong border-dashed">
			<button
				type="button"
				aria-expanded={open}
				onClick={() => setOpen((v) => !v)}
				className="flex h-9 w-full min-w-0 items-center gap-2 px-3 text-left text-muted text-xs hover:bg-hover/60"
			>
				<Badge icon="message">{label}</Badge>
				<span className="min-w-0 flex-1 truncate">{open ? "" : first}</span>
				<Icon
					name="chevronRight"
					size={12}
					className={`shrink-0 text-ghost transition-transform duration-150 ${open ? "rotate-90" : ""}`}
				/>
			</button>
			{open && (
				<div className="border-border border-t border-dashed px-3 py-2">
					<Markdown text={escapeAngles(text)} />
					<Images content={message.content} />
				</div>
			)}
		</div>
	);
}

function SummaryCard({
	icon,
	label,
	summary,
	extra,
	warning,
}: {
	icon: IconName;
	label: string;
	summary: string;
	extra?: string;
	warning?: string;
}) {
	const [open, setOpen] = useState(false);
	return (
		<section className="my-1 overflow-hidden rounded-lg border border-border bg-panel">
			<button
				type="button"
				className="flex h-9 w-full min-w-0 items-center gap-2 px-3 text-left text-muted text-xs hover:bg-hover/60"
				onClick={() => setOpen((v) => !v)}
				aria-expanded={open}
			>
				<Icon name={icon} size={13} className="shrink-0 text-faint" />
				<span className="truncate font-medium">{label}</span>
				{extra && <Badge>{extra}</Badge>}
				{warning && (
					<Icon name="alert" size={13} className="shrink-0 text-warn-ink" />
				)}
				<span className="flex-1" />
				<Icon
					name="chevronRight"
					size={12}
					className={`shrink-0 text-ghost transition-transform duration-150 ${open ? "rotate-90" : ""}`}
				/>
			</button>
			{open && (
				<div className="flex flex-col gap-2 border-border border-t px-3 py-2">
					{warning && (
						<Callout tone="warn" icon="alert">
							{warning}
						</Callout>
					)}
					<Markdown text={escapeAngles(summary)} />
				</div>
			)}
		</section>
	);
}

export function CompactionCard({ entry }: { entry: JsonRecord }) {
	const { t } = useTranslation();
	const before =
		typeof entry.tokensBefore === "number"
			? formatTokens(entry.tokensBefore)
			: "?";
	const after =
		typeof entry.tokensAfter === "number"
			? formatTokens(entry.tokensAfter)
			: "?";
	const warning =
		typeof entry.warning === "string" && entry.warning.trim()
			? entry.warning.trim()
			: undefined;
	return (
		<SummaryCard
			icon="minimize"
			label={t("transcript.compaction")}
			extra={`${before} → ${after}`}
			warning={warning}
			summary={String(entry.shortSummary ?? entry.summary ?? "")}
		/>
	);
}

export function BranchSummaryCard({ entry }: { entry: JsonRecord }) {
	const { t } = useTranslation();
	return (
		<SummaryCard
			icon="fork"
			label={t("transcript.branchSummary")}
			summary={String(entry.summary ?? "")}
		/>
	);
}

export function MessageView({
	message,
	entryId,
	live,
	turn,
	turnStart,
}: {
	message: JsonRecord;
	entryId?: string;
	live: boolean;
	/** 只在回合最后一条助手消息上传入：整回合的累计用量脚注。 */
	turn?: TurnUsage;
	/** 这条消息是回合起点（用户：右侧放用户头像；助手的头像行由 Transcript 放在行首）。 */
	turnStart?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	switch (message.role) {
		case "user":
			return (
				<UserMessage
					message={message}
					entryId={entryId}
					live={live}
					turnStart={turnStart === true}
				/>
			);
		case "assistant":
			return (
				<AssistantMessage
					message={message}
					entryId={entryId}
					live={live}
					turn={turn}
				/>
			);
		case "bashExecution":
		case "pythonExecution":
			return <ExecutionMessage message={message} />;
		case "custom":
		case "hookMessage": {
			const handoff = parseHandoffMessage(message);
			if (handoff !== null) {
				return (
					<SummaryCard
						icon="handoff"
						label={t("transcript.handoff")}
						summary={handoff || t("transcript.handoffEmpty")}
					/>
				);
			}
			const known = renderKnownCustomMessage(
				message,
				<Images content={message.content} />,
			);
			if (known) return known;
			// 后台任务的交付 / 提醒 / 常驻进程退出：解析成事件行；认不出时退回通用卡片
			return (
				<JobEventMessage
					message={message}
					fallback={<CustomMessage message={message} />}
				/>
			);
		}
		case "branchSummary":
			return <BranchSummaryCard entry={message} />;
		case "compactionSummary":
			return <CompactionCard entry={message} />;
		case "fileMention": {
			const files = Array.isArray(message.files) ? message.files : [];
			return (
				<div className="flex flex-wrap gap-1.5">
					{files.map((f, i) => {
						const path = isRecord(f) ? String(f.path ?? "") : "";
						return (
							<span
								// biome-ignore lint/suspicious/noArrayIndexKey: 文件列表无稳定 id
								key={i}
								className={`${CHIP} text-muted`}
								title={path}
							>
								<FileTypeIcon name={path} size={14} />
								<span className="max-w-72 truncate">{path}</span>
							</span>
						);
					})}
				</div>
			);
		}
		case "developer":
			return (
				<SummaryCard
					icon="info"
					label={t("transcript.developer")}
					summary={textOfContent(message.content)}
				/>
			);
		default:
			return null;
	}
}

export const MemoMessageView = memo(MessageView);
