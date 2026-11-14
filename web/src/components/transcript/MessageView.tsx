/**
 * 转录中的单条消息渲染：按 omp 的信息量全量展示（思考块、工具卡片、回合脚注、错误、bash 执行、自定义消息等）。
 * 所有字段都按「可能残缺」处理——条目与流式帧都是外部 JSON。
 */
import { memo, type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { ThreadState } from "../../api/client";
import { useAssetUrls } from "../../lib/asset-urls";
import {
	type MessageAttachment,
	parseAttachmentBlock,
} from "../../lib/attachments";
import { fileIcon } from "../../lib/file-icon";
import {
	formatBytes,
	formatCost,
	formatDuration,
	formatTokens,
} from "../../lib/format";
import { imageSrc } from "../../lib/image-src";
import {
	describeOrynHeader,
	type OrynHeader,
	parseOrynMessage,
} from "../../lib/oryn-message";
import { parseTitleCard } from "../../lib/title";
import type { JsonRecord } from "../../state/message-builder";
import { useIndex } from "../../state/stores";
import { ToolView } from "../../tool-render/ToolView";
import type { ToolResultLike } from "../../tool-render/types";
import { isRecord, resultTextOf } from "../../tool-render/util";
import { Markdown } from "../markdown/Markdown";
import { Badge, Callout, Stat } from "../ui/data";
import { ImageViewer } from "../ui/ImageViewer";
import { Icon, type IconName } from "../ui/icons";
import { Menu, ThreadStateIcon } from "../ui/primitives";
import { UserAvatar } from "../ui/UserAvatar";
import { useTranscriptContext } from "./context";

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

/** 用户气泡正文排版。 */
export const USER_BUBBLE_TEXT =
	"whitespace-pre-wrap break-words text-[0.875rem] leading-[1.375rem]";

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
	const [viewing, setViewing] = useState<string | null>(null);
	if (!Array.isArray(content)) return null;
	const imgs = content.filter(
		(b): b is { type: "image"; data: string; mimeType: string } =>
			isRecord(b) &&
			b.type === "image" &&
			typeof b.data === "string" &&
			typeof b.mimeType === "string",
	);
	if (imgs.length === 0) return null;
	return (
		<div className="mt-2 flex flex-wrap gap-2">
			{imgs.map((img, i) => {
				const src = imageSrc(img.data, img.mimeType, assets.blob);
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
				return (
					<button
						// biome-ignore lint/suspicious/noArrayIndexKey: 图片块无稳定 id
						key={i}
						type="button"
						className="rounded-md"
						onClick={() => setViewing(src)}
						aria-label={t("image.open", { n: i + 1 })}
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
			{viewing && (
				<ImageViewer src={viewing} alt="" onClose={() => setViewing(null)} />
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
						<Icon
							name={fileIcon(a.name, a.mime)}
							size={13}
							className="shrink-0 text-faint"
						/>
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
 * 消息操作菜单：行内渲染，悬停 / 聚焦时显现。打开时按可见空间选择向上或向下展开，
 * 展开后再把菜单滚进页头与输入坞之间（滚动容器带 scroll-padding），保证每一项都点得到。
 */
function MessageMenu({
	entryId,
	copyText,
	className = "",
	touchVisible = true,
}: {
	entryId?: string;
	/** 「复制」的文本：点击时才生成（回合脚注要拼接整回合正文，不在每次渲染时做）。 */
	copyText: () => string;
	/** 追加到外包元素上的定位类（例如回合中间消息右上角的浮动槽位）。 */
	className?: string;
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
	// 只读（分享页）：只有「复制」；fork / 标签都要改动线程
	const editable = entryId !== undefined && !ctx.readOnly;
	const itemCount = editable ? 3 : 1;
	return (
		<div
			ref={wrapRef}
			className={`opacity-0 transition-opacity duration-150 focus-within:opacity-100 group-hover/msg:opacity-100 ${touchVisible ? "pointer-coarse:opacity-100" : "group-focus-within/msg:opacity-100"} ${className}`}
		>
			<Menu
				side={side}
				trigger={(p) => (
					<button
						type="button"
						className="inline-grid h-6 w-6 place-items-center rounded-md text-ghost hover:bg-hover hover:text-fg"
						aria-label={t("transcript.messageActions")}
						{...p}
						onClick={() => {
							const opening = !p["aria-expanded"];
							if (opening) setSide(pickMenuSide(wrapRef.current, itemCount));
							p.onClick();
							if (!opening) return;
							requestAnimationFrame(() =>
								wrapRef.current
									?.querySelector('[role="menu"]')
									?.scrollIntoView({ block: "nearest" }),
							);
						}}
					>
						<Icon name="more" size={14} />
					</button>
				)}
				items={[
					{
						key: "copy",
						icon: "copy",
						label: t("common.copy"),
						onSelect: () => void navigator.clipboard?.writeText(copyText()),
					},
					...(editable && entryId
						? [
								{
									key: "fork",
									icon: "fork" as const,
									label: t("transcript.forkHere"),
									onSelect: () => ctx.forkFrom(entryId),
								},
								{
									key: "label",
									icon: "edit" as const,
									label: t("transcript.label"),
									onSelect: () => ctx.labelEntry(entryId),
								},
							]
						: []),
				]}
			/>
		</div>
	);
}

/** 首行小签的图标：定时用时钟，线程报告用列表，其余（agent 发来的消息）用机器人。 */
const ORYN_ICON: Record<OrynHeader["kind"], IconName> = {
	schedule: "clock",
	report: "list",
	message: "robot",
	unknown: "robot",
};

/**
 * 线程报告（M9）：一枚可点的线程胶囊（索引里的当前标题 + 状态，链到该线程）+ 引用块里的回复正文（Markdown）。
 * 不显示包裹标签与原始 id；索引里没有该线程时用报告首行里的标题快照与状态。
 */
function ThreadReport({
	threadId,
	title,
	state,
	content,
}: {
	threadId: string;
	title: string;
	state: string;
	content: string;
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
	const shown = oryn ? oryn.body : body;
	return (
		<div className="group/msg flex flex-col items-end gap-1">
			{(irc || synthetic || steer || oryn) && (
				<div className="flex max-w-full flex-wrap justify-end gap-1">
					{oryn && (
						<Badge icon={ORYN_ICON[oryn.header.kind]} title={label}>
							{label}
						</Badge>
					)}
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
				{oryn?.report && oryn.header.kind === "report" && (
					<ThreadReport
						threadId={oryn.report.threadId}
						title={oryn.header.title}
						state={oryn.header.state}
						content={oryn.report.content}
					/>
				)}
				{shown && <div className={USER_BUBBLE_TEXT}>{shown}</div>}
				<Images content={message.content} />
				<AttachmentChips attachments={attachments} />
			</UserBubble>
			<MessageMenu entryId={entryId} copyText={() => text} />
		</div>
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
	const [open, setOpen] = useState(false);
	return (
		<div>
			<button
				type="button"
				className="group/think flex h-7 w-full min-w-0 items-center gap-2 rounded-md pr-[11px] text-left text-[12.5px] text-muted hover:text-fg"
				onClick={() => setOpen((v) => !v)}
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
	const ctx = useTranscriptContext();
	const live = ctx.liveTools[toolCallId];
	const entryResult = ctx.toolResults.get(toolCallId);
	const result = entryResult
		? toolResultLike(entryResult)
		: live?.ended
			? toolResultLike(live.result)
			: undefined;
	const running =
		!result && (Boolean(live && !live.ended) || streamingArgs === true);
	const partial =
		live && !live.ended
			? resultTextOf(toolResultLike(live.partialResult))
			: undefined;
	return (
		<ToolView
			name={live?.toolName ?? name}
			args={args ?? live?.args}
			result={
				result
					? { ...result, isError: result.isError || live?.isError }
					: undefined
			}
			running={running}
			intent={intent ?? live?.intent}
			partial={partial}
			host={
				// 只读（分享页）不提供子 agent 转录查看
				ctx.readOnly
					? {}
					: {
							hasAgent: () => true,
							openAgent: (id) => ctx.openSubagent(id),
						}
			}
		/>
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
	// 停止提示与回合脚注都是正文层的块
	const tailProse = hasFooter || stop === "error" || stop === "aborted";
	const last = tailProse ? "prose" : (tail ?? "prose");
	return [head ?? "prose", live && head === null ? "prose" : last];
}

/**
 * 回合中间的助手消息自己的操作菜单：浮在消息右上角（压在上方间距与首块右上角），带底色描边，
 * 只在悬停 / 聚焦时显现——左侧不再留头像列，菜单不占正文宽度。
 */
const FLOAT_MENU =
	"absolute -top-3 right-0 z-10 rounded-md border border-border bg-panel shadow-xs";

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
	const content = Array.isArray(message.content) ? message.content : [];
	const lastIdx = content.length - 1;
	const stop =
		typeof message.stopReason === "string" ? message.stopReason : undefined;
	const error =
		typeof message.errorMessage === "string" ? message.errorMessage : undefined;
	// 回合末尾的消息由脚注承载整回合菜单；其余已结束的消息各自带一个菜单
	const ownMenu = !live && !turn;
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
				className={b.type === "text" && streaming ? "stream-caret" : undefined}
				data-tool-call-id={
					b.type === "toolCall" && typeof b.id === "string" ? b.id : undefined
				}
				style={marginTop ? { marginTop } : undefined}
			>
				{node}
			</div>,
		);
	});
	return (
		// 有浮动菜单时，外层可被点按聚焦（不进 Tab 顺序），触屏点正文即显出菜单，点别处收起
		<div
			className="group/msg relative outline-none"
			tabIndex={ownMenu ? -1 : undefined}
		>
			{ownMenu && (
				<MessageMenu
					entryId={entryId}
					copyText={() => textOfContent(content)}
					touchVisible={false}
					className={FLOAT_MENU}
				/>
			)}
			{blocks}
			{live && content.length === 0 && (
				<div className="stream-caret text-faint text-sm" />
			)}
			{(stop === "error" || stop === "aborted") && (
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

function CustomMessage({ message }: { message: JsonRecord }) {
	return (
		<div className="rounded-lg border border-border-strong border-dashed px-3 py-2">
			<div className="mb-1.5">
				<Badge icon="message">
					{String(message.customType ?? message.role ?? "")}
				</Badge>
			</div>
			<Markdown text={textOfContent(message.content)} />
			<Images content={message.content} />
		</div>
	);
}

function SummaryCard({
	icon,
	label,
	summary,
	extra,
}: {
	icon: IconName;
	label: string;
	summary: string;
	extra?: string;
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
				<span className="flex-1" />
				<Icon
					name="chevronRight"
					size={12}
					className={`shrink-0 text-ghost transition-transform duration-150 ${open ? "rotate-90" : ""}`}
				/>
			</button>
			{open && (
				<div className="border-border border-t px-3 py-2">
					<Markdown text={summary} />
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
	return (
		<SummaryCard
			icon="minimize"
			label={t("transcript.compaction")}
			extra={`${before} → ${after}`}
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
		case "hookMessage":
			return <CustomMessage message={message} />;
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
								<Icon
									name={fileIcon(path)}
									size={13}
									className="shrink-0 text-faint"
								/>
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
