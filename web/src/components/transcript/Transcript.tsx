/**
 * 虚拟滚动转录（@tanstack/react-virtual，动态测量高度）。
 * 贴底跟随：用户停在底部时随新内容自动滚动；上翻后停止跟随并显示「回到底部」。
 * 页头与输入坞以玻璃盖在转录上方：`insetTop` / `insetBottom` 走 virtualizer 的
 * `paddingStart` / `paddingEnd`（不给滚动容器加 CSS padding，否则会破坏贴底判断），
 * 同时写成滚动容器的 scroll-padding，让键盘聚焦与 scrollIntoView 落在两块覆盖层之间。
 * 左缘只有一条锚线：agent 头像（回合起点内容上方单独一行）与其余一切左对齐的块共用；用户气泡靠右。
 */
import { useVirtualizer } from "@tanstack/react-virtual";
import {
	type ReactNode,
	useCallback,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { JsonRecord } from "../../state/message-builder";
import { useIndex, useThread } from "../../state/stores";
import type { Notice } from "../../state/thread-reducer";
import type { TranscriptItem } from "../../state/transcript";
import { AgentAvatar } from "../ui/AgentAvatar";
import { Badge, Callout, type Tone } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import { useTranscriptContext } from "./context";
import {
	assistantEdges,
	BranchSummaryCard,
	CompactionCard,
	type FlowKind,
	flowGap,
	GAP_FLOW,
	GAP_PROC,
	MemoMessageView,
	summarizeTurn,
	ToolCard,
	type TurnUsage,
	USER_BUBBLE_TEXT,
	UserBubble,
} from "./MessageView";
import { type KnownModel, modelLabel } from "./model-label";
import { clearPendingPrompt, usePendingPrompt } from "./pending-prompt";

const META_ICON: Record<string, IconName> = {
	thinking_level_change: "brain",
	mode_change: "list",
	title_change: "edit",
	ttsr_injection: "bolt",
};

function MetaLine({ entry }: { entry: JsonRecord }) {
	const { t } = useTranslation();
	const { threadId } = useTranscriptContext();
	const known = useThread(threadId, (s) => s.state?.model);
	let text = "";
	let title: string | undefined;
	switch (entry.type) {
		case "model_change": {
			// 条目里是 "provider/modelId"：界面写短名（与头像行、输入坞一致），完整 id 进悬停提示
			const label = modelLabel(String(entry.model ?? ""), undefined, known);
			text = t("transcript.meta.model", { model: label?.short ?? "" });
			title = label?.full;
			break;
		}
		case "thinking_level_change":
			text = t("transcript.meta.thinking", {
				level: String(entry.thinkingLevel ?? entry.configured ?? "off"),
			});
			break;
		case "mode_change":
			text = t("transcript.meta.mode", { mode: String(entry.mode ?? "") });
			break;
		case "ttsr_injection":
			text = t("transcript.meta.ttsr", {
				rules: Array.isArray(entry.injectedRules)
					? entry.injectedRules.join(", ")
					: "",
			});
			break;
		case "title_change":
			text = t("transcript.meta.title", { title: String(entry.title ?? "") });
			break;
		default:
			text = String(entry.type ?? "");
	}
	const icon = META_ICON[String(entry.type)] ?? "info";
	return (
		<div className="flex min-w-0 items-center gap-2 py-0.5 text-2xs text-faint">
			<span
				aria-hidden
				className="inline-grid h-[18px] w-[18px] shrink-0 place-items-center rounded bg-inset text-muted"
			>
				{entry.type === "model_change" ? (
					// model 是 "provider/modelId"，ModelLogo 会按路径识别
					<ModelLogo model={String(entry.model ?? "")} size={11} />
				) : (
					<Icon name={icon} size={11} />
				)}
			</span>
			<span className="min-w-0 truncate" title={title}>
				{text}
			</span>
		</div>
	);
}

const NOTICE_TONE: Record<Notice["level"], Tone> = {
	info: "neutral",
	warning: "warn",
	error: "err",
};

function NoticeLine({ notice }: { notice: Notice }) {
	const { t } = useTranslation();
	const label = t(`notice.${notice.kind}`);
	const url =
		notice.kind === "open_url" &&
		notice.detail &&
		typeof notice.detail === "object" &&
		"url" in notice.detail
			? String(notice.detail.url)
			: null;
	const body =
		notice.text || url ? (
			<>
				{notice.text && (
					<span
						className={
							notice.kind === "command_output"
								? "block whitespace-pre-wrap break-words font-mono"
								: "break-words"
						}
					>
						{notice.text}
					</span>
				)}
				{url && (
					<a
						className="break-all text-accent underline underline-offset-2"
						href={url}
						target="_blank"
						rel="noopener noreferrer"
					>
						{url}
					</a>
				)}
			</>
		) : undefined;
	return (
		<Callout
			tone={NOTICE_TONE[notice.level]}
			icon={notice.level === "info" ? "info" : "alert"}
			title={label}
		>
			{body}
		</Callout>
	);
}

function ItemView({
	item,
	turn,
	turnStart,
}: {
	item: TranscriptItem;
	turn?: TurnUsage;
	turnStart: boolean;
}): ReactNode {
	const { t } = useTranslation();
	switch (item.kind) {
		case "session_divider":
			return (
				<div className="flex items-center gap-3 py-2">
					<div className="h-px flex-1 bg-border" />
					<Badge icon="chats">{t("transcript.newSession")}</Badge>
					<div className="h-px flex-1 bg-border" />
				</div>
			);
		case "message":
			return (
				<MemoMessageView
					message={item.message}
					entryId={item.entryId}
					live={item.live}
					turn={turn}
					turnStart={turnStart}
				/>
			);
		case "tool":
			return <ToolCard toolCallId={item.toolCallId} name={item.toolName} />;
		case "compaction":
			return <CompactionCard entry={item.entry} />;
		case "branch_summary":
			return <BranchSummaryCard entry={item.entry} />;
		case "custom_message":
			return (
				<MemoMessageView
					message={{ ...item.entry, role: "custom" }}
					live={false}
				/>
			);
		case "meta":
			return <MetaLine entry={item.entry} />;
		case "notice":
			return <NoticeLine notice={item.notice} />;
	}
}

/** 属于 agent 回合的行：助手消息与独立的运行中工具卡。 */
function isAgentRow(item: TranscriptItem | undefined): boolean {
	if (!item) return false;
	if (item.kind === "tool") return true;
	return item.kind === "message" && item.message.role === "assistant";
}

function isUserRow(item: TranscriptItem): boolean {
	return item.kind === "message" && item.message.role === "user";
}

/** 回合的模型（第一条带模型字段的助手消息），用于头像行的身份文字。 */
export interface TurnModel {
	model: string;
	provider: string;
}

/**
 * 按回合（以用户消息 / 会话分隔为界）切分：每个回合的第一条 agent 行在内容上方单独放一行头像
 * （`models` 给出该回合的模型，作为头像旁的身份文字）；
 * 回合最后一行是已结束的助手消息时，在它下面挂整回合的累计用量脚注（回合仍在进行时不挂）。
 * 连续的用户消息只有第一条放用户头像（`userStarts`）。
 * `openStart` 是最后一个助手回合的起点，且其后再没有用户消息 / 会话分隔（线程工作中时就是正在进行的回合）。
 */
export function layoutTurns(items: readonly TranscriptItem[]): {
	starts: Set<number>;
	userStarts: Set<number>;
	footers: Map<number, TurnUsage>;
	models: Map<number, TurnModel>;
	openStart: number | undefined;
} {
	const starts = new Set<number>();
	const userStarts = new Set<number>();
	const footers = new Map<number, TurnUsage>();
	const models = new Map<number, TurnModel>();
	let messages: JsonRecord[] = [];
	let last = -1;
	let start = -1;
	let openStart: number | undefined;
	const close = () => {
		const tail = items[last];
		if (tail?.kind === "message" && !tail.live && messages.length > 0)
			footers.set(last, summarizeTurn(messages));
		messages = [];
		last = -1;
	};
	items.forEach((it, i) => {
		if (isUserRow(it)) {
			const prev = items[i - 1];
			if (prev === undefined || !isUserRow(prev)) userStarts.add(i);
			close();
			openStart = undefined;
			return;
		}
		if (it.kind === "session_divider") {
			close();
			openStart = undefined;
			return;
		}
		if (!isAgentRow(it)) return;
		if (last < 0) {
			starts.add(i);
			openStart = i;
			start = i;
		}
		last = i;
		if (it.kind !== "message") return;
		messages.push(it.message);
		const { model, provider } = it.message;
		if (!models.has(start) && typeof model === "string" && model)
			models.set(start, {
				model,
				provider: typeof provider === "string" ? provider : "",
			});
	});
	close();
	return { starts, userStarts, footers, models, openStart };
}

/** 回合边界（用户气泡、会话分隔、提示行等非 agent 行）上下的间距。 */
const GAP_TURN = 16;

/** 回合头像行与回合第一块内容之间的距离：头像是这一回合的题头，贴着内容用近档。 */
const AVATAR_GAP = GAP_PROC;

/** `insetBottom` 里输入坞上方的留白（ThreadPage 传 `dockH + 16`）；据此找到托盘顶边。 */
const DOCK_GAP = 16;

/** 行首 / 行尾的节奏类别；`turn` 是不属于 agent 过程流的行。 */
type Edge = FlowKind | "turn";

function rowEdges(item: TranscriptItem, hasFooter: boolean): [Edge, Edge] {
	if (item.kind === "tool") return ["proc", "proc"];
	if (item.kind === "message" && item.message.role === "assistant")
		return assistantEdges(item.message, item.live, hasFooter);
	return ["turn", "turn"];
}

/**
 * 每行的上边距：由上一行行尾与本行行首的类别决定（见 MessageView 的 `flowGap`），
 * 与消息内块之间的间距同一规则，虚拟行之间不再叠加各自的内边距。
 * 回合起点行以头像行开头，它的上一行必然是回合边界（用户气泡、分隔线、提示行）或列表开头，
 * 所以按 `GAP_TURN` / `GAP_FLOW` 取值，与行首块的类别无关。
 */
export function rowGaps(
	items: readonly TranscriptItem[],
	footers: ReadonlyMap<number, TurnUsage>,
): number[] {
	let prev: Edge | null = null;
	return items.map((it, i) => {
		const [first, last] = rowEdges(it, footers.has(i));
		const gap =
			prev === null
				? GAP_FLOW
				: prev === "turn" || first === "turn"
					? GAP_TURN
					: flowGap(prev, first);
		prev = last;
		return gap;
	});
}

/** 环境准备阶段先显示的首条任务（客户端暂存，转录里出现 user 消息即清除）。 */
function PendingPrompt({ text }: { text: string }) {
	const { t } = useTranslation();
	return (
		<div className="flex flex-col items-end gap-1 pr-10 pb-3">
			<span className="sr-only">{t("transcript.you")}</span>
			<UserBubble avatar dim>
				<div className={USER_BUBBLE_TEXT}>{text}</div>
			</UserBubble>
			<span className="inline-flex items-center gap-1 text-2xs text-faint">
				<Icon name="clock" size={11} aria-hidden />
				{t("transcript.sending")}
			</span>
		</div>
	);
}

/**
 * 回合头像行：agent 头像 → 模型品牌 logo（与输入坞模型胶囊同款）→ 该回合模型的短名（淡色，悬停给完整 id）；
 * 消息里没有模型信息时只放头像。
 */
function TurnHead({
	working,
	model,
	known,
}: {
	working: boolean;
	model?: TurnModel;
	known?: KnownModel;
}): ReactNode {
	const label = model ? modelLabel(model.model, model.provider, known) : null;
	return (
		<div
			className="flex h-7 min-w-0 items-center gap-2"
			style={{ marginBottom: AVATAR_GAP }}
		>
			<AgentAvatar size={28} working={working} />
			{model && label && (
				<span
					className="flex min-w-0 items-center gap-1.5 font-medium text-faint text-sm"
					title={label.full}
				>
					<ModelLogo
						model={model.model}
						provider={model.provider || undefined}
						size={16}
						className="shrink-0"
					/>
					<span className="min-w-0 truncate">{label.short}</span>
				</span>
			)}
		</div>
	);
}

export function Transcript({
	items,
	footer,
	insetTop = 0,
	insetBottom = 0,
}: {
	items: TranscriptItem[];
	footer?: ReactNode;
	/** 顶部被玻璃页头覆盖的高度（px）。 */
	insetTop?: number;
	/** 底部被玻璃输入坞覆盖的高度（px）：输入坞高度 + `DOCK_GAP` 留白（ThreadPage 传入 `dockH + 16`）。 */
	insetBottom?: number;
}): ReactNode {
	const { t } = useTranslation();
	const { threadId } = useTranscriptContext();
	const pending = usePendingPrompt(threadId);
	const hasUser = useMemo(() => items.some(isUserRow), [items]);
	// 第一条 user 消息到了就丢掉暂存，避免同一句话出现两次
	useEffect(() => {
		if (hasUser && pending !== undefined) clearPendingPrompt(threadId);
	}, [hasUser, pending, threadId]);
	const pendingText = hasUser ? undefined : pending;
	const turns = useMemo(() => layoutTurns(items), [items]);
	// 线程工作中时，只有正在进行的那个助手回合的头像闪（历史回合一律静止）
	const working = useIndex((s) => s.threads[threadId]?.state === "working");
	const liveStart = working ? turns.openStart : undefined;
	const sessionModel = useThread(threadId, (s) => s.state?.model);
	const gaps = useMemo(() => rowGaps(items, turns.footers), [items, turns]);
	const scrollRef = useRef<HTMLDivElement>(null);
	const [atBottom, setAtBottom] = useState(true);
	const stick = useRef(true);
	const count = items.length + 1; // 末尾一项放 footer（工作中指示等）
	const virtualizer = useVirtualizer({
		count,
		getScrollElement: () => scrollRef.current,
		estimateSize: () => 96,
		overscan: 6,
		paddingStart: insetTop,
		paddingEnd: insetBottom,
		getItemKey: (i) => (i < items.length ? (items[i]?.key ?? i) : "__footer__"),
	});
	const total = virtualizer.getTotalSize();

	const onScroll = useCallback(() => {
		const el = scrollRef.current;
		if (!el) return;
		const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
		stick.current = bottom;
		setAtBottom(bottom);
	}, []);

	const scrollToEnd = useCallback(() => {
		const el = scrollRef.current;
		if (el) el.scrollTop = el.scrollHeight;
	}, []);

	// 内容增长（新条目 / 流式增量 / 高度重测 / 输入坞变高）时，贴底则跟随
	// biome-ignore lint/correctness/useExhaustiveDependencies: items、total 与 insetBottom 变化即需要重新贴底
	useLayoutEffect(() => {
		if (stick.current) scrollToEnd();
	}, [items, total, insetBottom, scrollToEnd]);

	useEffect(() => {
		stick.current = true;
		scrollToEnd();
	}, [scrollToEnd]);

	const virtualItems = virtualizer.getVirtualItems();
	const rendered = useMemo(
		() =>
			virtualItems.map((v) => {
				const item = v.index < items.length ? items[v.index] : undefined;
				// 会话分隔线与 agent 各块都铺满正文列；用户气泡右侧让出头像列（pr-10）
				const userRow = item !== undefined && isUserRow(item);
				return (
					<div
						key={v.key}
						data-index={v.index}
						data-kind={item?.kind}
						data-role={
							item?.kind === "message" ? String(item.message.role) : undefined
						}
						data-entry-id={item?.kind === "message" ? item.entryId : undefined}
						data-live={item?.kind === "message" && item.live ? "1" : undefined}
						ref={virtualizer.measureElement}
						// 行内菜单（消息操作）会溢出到相邻行：悬停 / 聚焦时把本行抬到兄弟行之上
						className="absolute left-0 w-full px-3 focus-within:z-10 hover:z-10 sm:px-5"
						style={{
							transform: `translateY(${v.start}px)`,
							// 行间距只来自本行上边距（见 rowGaps）；末尾的 footer 行再留一点底
							paddingTop: item ? gaps[v.index] : GAP_FLOW,
							paddingBottom: item ? 0 : 6,
						}}
					>
						<div
							className={`group/row relative mx-auto max-w-3xl ${userRow ? "pr-10" : ""}`}
						>
							{turns.starts.has(v.index) && (
								// 回合起点：头像单独占一行，放在本行（回合首行）内部，不另占虚拟行
								<TurnHead
									working={liveStart === v.index}
									model={turns.models.get(v.index)}
									known={sessionModel}
								/>
							)}
							{item ? (
								<ItemView
									item={item}
									turn={turns.footers.get(v.index)}
									turnStart={
										turns.starts.has(v.index) || turns.userStarts.has(v.index)
									}
								/>
							) : (
								<>
									{pendingText !== undefined && (
										<PendingPrompt text={pendingText} />
									)}
									{footer}
								</>
							)}
						</div>
					</div>
				);
			}),
		[
			virtualItems,
			items,
			footer,
			virtualizer.measureElement,
			turns,
			gaps,
			pendingText,
			liveStart,
			sessionModel,
		],
	);

	// 「回到底部」是悬在托盘顶边上方 8px 的独立小圆钮；输入坞整体独立悬浮，不遮罩、不淡出周围正文
	const trayTop = Math.max(0, insetBottom - DOCK_GAP);

	return (
		<div className="relative min-h-0 flex-1">
			<div
				ref={scrollRef}
				onScroll={onScroll}
				className="h-full overflow-y-auto overscroll-contain"
				style={{
					scrollPaddingTop: insetTop,
					scrollPaddingBottom: insetBottom,
				}}
				data-testid="transcript"
			>
				<div className="relative w-full" style={{ height: total }}>
					{rendered}
				</div>
			</div>
			{!atBottom && (
				// 水平居中、悬在托盘顶边上方留白里的小圆钮
				<button
					type="button"
					aria-label={t("transcript.jumpToEnd")}
					title={t("transcript.jumpToEnd")}
					className="glass pop-in absolute left-1/2 z-20 inline-grid h-7 w-7 -translate-x-1/2 place-items-center rounded-full text-muted hover:text-fg"
					style={{ bottom: trayTop + 8 }}
					onClick={() => {
						stick.current = true;
						scrollToEnd();
					}}
				>
					<Icon name="arrowDown" size={14} />
				</button>
			)}
		</div>
	);
}
