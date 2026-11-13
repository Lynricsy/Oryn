/**
 * 转录中的单条消息渲染：按 omp 的信息量全量展示（思考块、工具卡片、回合脚注、错误、bash 执行、自定义消息等）。
 * 所有字段都按「可能残缺」处理——条目与流式帧都是外部 JSON。
 */
import { memo, type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	attachmentContentUrl,
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
import type { JsonRecord } from "../../state/message-builder";
import { ToolView } from "../../tool-render/ToolView";
import type { ToolResultLike } from "../../tool-render/types";
import { isRecord, resultTextOf } from "../../tool-render/util";
import { Markdown } from "../markdown/Markdown";
import { Badge, Callout, Stat } from "../ui/data";
import { ImageViewer } from "../ui/ImageViewer";
import { Icon, type IconName } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import { Menu } from "../ui/primitives";
import { UserAvatar } from "../ui/UserAvatar";
import { useTranscriptContext } from "./context";

const CHIP =
	"inline-flex items-center gap-1.5 rounded-md border border-border bg-panel px-2 py-1 text-xs";

/** 用户气泡外观（真实消息与「发送中」的待发任务共用）。 */
const USER_BUBBLE =
	"w-fit min-w-0 max-w-[85%] rounded-lg rounded-br-xs border border-border bg-raised px-3.5 py-2";

/**
 * 用户气泡一行：气泡靠右；`avatar` 为真（用户回合起点）时在右侧头像列放用户头像，
 * 与助手回合左侧的头像对称——外层须用 `pr-8` 让出这一列。头像只作装饰（aria-hidden）。
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
				<span aria-hidden className="absolute -right-8 bottom-0 flex">
					<UserAvatar size={20} />
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

function textOfContent(content: unknown): string {
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
				const src = imageSrc(img.data, img.mimeType);
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

/** 用户消息附件块解析出的附件芯片：点击以附件形式下载（`attachments/{id}/content`）。 */
export function AttachmentChips({
	attachments,
}: {
	attachments: MessageAttachment[];
}): ReactNode {
	const { t } = useTranslation();
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
				return (
					<li key={a.path}>
						{a.id ? (
							<a
								href={attachmentContentUrl(a.id)}
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
	text,
	className = "",
	align = "right",
}: {
	entryId?: string;
	text: string;
	/** 追加到外包元素上的定位类（例如回合中间消息的左侧槽位）。 */
	className?: string;
	/** 弹层对齐边：左侧槽位里的按钮要向右展开，否则会伸出转录左缘。 */
	align?: "left" | "right";
}): ReactNode {
	const { t } = useTranslation();
	const ctx = useTranscriptContext();
	const wrapRef = useRef<HTMLDivElement>(null);
	const [side, setSide] = useState<"top" | "bottom">("top");
	const itemCount = entryId ? 3 : 1;
	return (
		<div
			ref={wrapRef}
			className={`opacity-0 transition-opacity duration-150 focus-within:opacity-100 group-hover/msg:opacity-100 pointer-coarse:opacity-100 ${className}`}
		>
			<Menu
				side={side}
				align={align}
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
						onSelect: () => void navigator.clipboard?.writeText(text),
					},
					...(entryId
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
	const { t } = useTranslation();
	// 控制面追加的附件块不按原文显示，渲染成附件芯片
	const { text, attachments } = parseAttachmentBlock(
		textOfContent(message.content),
	);
	const irc = isRecord(message.parentIrc) ? message.parentIrc : null;
	const synthetic = message.synthetic === true;
	const steer = message.steering === true || message.liveSteered === true;
	const body = irc ? String(irc.body ?? text) : text;
	return (
		<div className="group/msg flex flex-col items-end gap-1">
			{(irc || synthetic || steer) && (
				<div className="flex flex-wrap justify-end gap-1">
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
				{body && <div className={USER_BUBBLE_TEXT}>{body}</div>}
				<Images content={message.content} />
				<AttachmentChips attachments={attachments} />
			</UserBubble>
			<MessageMenu entryId={entryId} text={text} />
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
			host={{
				hasAgent: () => true,
				openAgent: (id) => ctx.openSubagent(id),
			}}
		/>
	);
}

/** 一个回合（两条用户消息之间）的累计用量：各条助手消息的读数相加，模型取最后一条。 */
export interface TurnUsage {
	model: string;
	/** 最后一条助手消息的 provider，帮 ModelLogo 识别认不出的模型 id。 */
	provider: string;
	hasUsage: boolean;
	input: number;
	output: number;
	cacheRead: number;
	cost: number;
	/** 各条消息 `duration` 之和；没有任何一条带耗时时为 null。 */
	duration: number | null;
	/** 回合内全部助手正文，供「复制」。 */
	text: string;
}

export function summarizeTurn(messages: readonly JsonRecord[]): TurnUsage {
	const n = (v: unknown) => (typeof v === "number" ? v : 0);
	const sum: TurnUsage = {
		model: "",
		provider: "",
		hasUsage: false,
		input: 0,
		output: 0,
		cacheRead: 0,
		cost: 0,
		duration: null,
		text: "",
	};
	const texts: string[] = [];
	for (const m of messages) {
		if (typeof m.model === "string" && m.model) {
			sum.model = m.model;
			sum.provider = typeof m.provider === "string" ? m.provider : "";
		}
		if (isRecord(m.usage)) {
			sum.hasUsage = true;
			sum.input += n(m.usage.input);
			sum.output += n(m.usage.output);
			sum.cacheRead += n(m.usage.cacheRead);
			if (isRecord(m.usage.cost)) sum.cost += n(m.usage.cost.total);
		}
		if (typeof m.duration === "number")
			sum.duration = (sum.duration ?? 0) + m.duration;
		const text = textOfContent(m.content);
		if (text) texts.push(text);
	}
	sum.text = texts.join("\n\n");
	return sum;
}

/** 回合脚注里的单个读数胶囊。 */
const META_CHIP =
	"inline-flex h-6 shrink-0 items-center gap-1 whitespace-nowrap rounded-md border border-border px-1.5 tabular-nums [&_svg]:text-faint";

/**
 * 回合脚注：整回合的模型与累计用量（每项一个细描边胶囊）+ 消息操作；只挂在回合最后一条助手消息下。
 * 始终单行：按自身宽度（容器查询）先藏缓存、再藏耗时，模型名最后截断。
 */
function TurnMeta({ turn, entryId }: { turn: TurnUsage; entryId?: string }) {
	const { t } = useTranslation();
	return (
		<div className="@container mt-2">
			<div className="flex min-w-0 flex-nowrap items-center gap-1.5 text-2xs text-muted">
				{turn.model && (
					<span
						className={`${META_CHIP} min-w-0 shrink font-medium text-fg`}
						title={turn.model}
					>
						<ModelLogo model={turn.model} provider={turn.provider} size={12} />
						<span className="min-w-0 truncate">{turn.model}</span>
					</span>
				)}
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
							<span className={META_CHIP}>
								<Stat icon="coin" label={t("stat.cost")}>
									{formatCost(turn.cost)}
								</Stat>
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
				<MessageMenu entryId={entryId} text={turn.text} />
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
	const kinds = content.map(blockKind).filter((k) => k !== null);
	const first = kinds[0] ?? "prose";
	const stop = message.stopReason;
	// 停止提示与回合脚注都是正文层的块
	const tailProse = hasFooter || stop === "error" || stop === "aborted";
	const last = tailProse ? "prose" : (kinds[kinds.length - 1] ?? "prose");
	return [first, live && kinds.length === 0 ? "prose" : last];
}

/**
 * 回合中间的助手消息自己的操作菜单：放在头像列里（与头像同一位置），悬停 / 聚焦时显现，
 * 粗指针常显弱化按钮。回合起点那条消息的头像在悬停时让位；粗指针下按钮挪到头像下方。
 */
const GUTTER_MENU = "absolute top-0.5 -left-[34px]";
const GUTTER_MENU_UNDER_AVATAR = "pointer-coarse:top-[28px]";

function AssistantMessage({
	message,
	entryId,
	live,
	turn,
	turnStart,
}: {
	message: JsonRecord;
	entryId?: string;
	live: boolean;
	turn?: TurnUsage;
	turnStart?: boolean;
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
				style={marginTop ? { marginTop } : undefined}
			>
				{node}
			</div>,
		);
	});
	return (
		<div className={`group/msg relative ${ownMenu ? "msg-gutter" : ""}`}>
			{ownMenu && (
				<MessageMenu
					entryId={entryId}
					text={textOfContent(content)}
					align="left"
					className={`${GUTTER_MENU} ${turnStart ? GUTTER_MENU_UNDER_AVATAR : ""}`}
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
	/** 这条消息是回合起点（助手：头像列里已有头像；用户：右侧放用户头像）。 */
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
					turnStart={turnStart}
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
