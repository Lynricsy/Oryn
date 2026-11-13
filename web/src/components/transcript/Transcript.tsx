/**
 * 虚拟滚动转录（@tanstack/react-virtual，动态测量高度）。
 * 贴底跟随：用户停在底部时随新内容自动滚动；上翻后停止跟随并显示「回到底部」。
 * 页头与输入坞以玻璃盖在转录上方：`insetTop` / `insetBottom` 走 virtualizer 的
 * `paddingStart` / `paddingEnd`（不给滚动容器加 CSS padding，否则会破坏贴底判断），
 * 同时写成滚动容器的 scroll-padding，让键盘聚焦与 scrollIntoView 落在两块覆盖层之间。
 * 左缘只有两条锚线：头像列（回合起点的 agent 头像）与正文列（其余一切左对齐的块）；用户气泡靠右。
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
import type { Notice } from "../../state/thread-reducer";
import type { TranscriptItem } from "../../state/transcript";
import { AgentAvatar } from "../ui/AgentAvatar";
import { Badge, Callout, type Tone } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { useTranscriptContext } from "./context";
import {
	BranchSummaryCard,
	CompactionCard,
	MemoMessageView,
	summarizeTurn,
	ToolCard,
	type TurnUsage,
	USER_BUBBLE,
	USER_BUBBLE_TEXT,
} from "./MessageView";
import { clearPendingPrompt, usePendingPrompt } from "./pending-prompt";

const META_ICON: Record<string, IconName> = {
	model_change: "sparkle",
	thinking_level_change: "brain",
	mode_change: "list",
	title_change: "edit",
	ttsr_injection: "bolt",
};

function MetaLine({ entry }: { entry: JsonRecord }) {
	const { t } = useTranslation();
	let text = "";
	switch (entry.type) {
		case "model_change":
			text = t("transcript.meta.model", { model: String(entry.model ?? "") });
			break;
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
				<Icon name={icon} size={11} />
			</span>
			<span className="min-w-0 truncate">{text}</span>
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

/**
 * 按回合（以用户消息 / 会话分隔为界）切分：每个回合的第一条 agent 行放头像；
 * 回合最后一行是已结束的助手消息时，在它下面挂整回合的累计用量脚注（回合仍在进行时不挂）。
 */
export function layoutTurns(items: readonly TranscriptItem[]): {
	starts: Set<number>;
	footers: Map<number, TurnUsage>;
} {
	const starts = new Set<number>();
	const footers = new Map<number, TurnUsage>();
	let messages: JsonRecord[] = [];
	let last = -1;
	const close = () => {
		const tail = items[last];
		if (tail?.kind === "message" && !tail.live && messages.length > 0)
			footers.set(last, summarizeTurn(messages));
		messages = [];
		last = -1;
	};
	items.forEach((it, i) => {
		if (it.kind === "session_divider" || isUserRow(it)) {
			close();
			return;
		}
		if (!isAgentRow(it)) return;
		if (last < 0) starts.add(i);
		last = i;
		if (it.kind === "message") messages.push(it.message);
	});
	close();
	return { starts, footers };
}

/** 环境准备阶段先显示的首条任务（客户端暂存，转录里出现 user 消息即清除）。 */
function PendingPrompt({ text }: { text: string }) {
	const { t } = useTranslation();
	return (
		<div className="flex flex-col items-end gap-1 pb-3">
			<span className="sr-only">{t("transcript.you")}</span>
			<div className={`${USER_BUBBLE} opacity-80`}>
				<div className={USER_BUBBLE_TEXT}>{text}</div>
			</div>
			<span className="inline-flex items-center gap-1 text-2xs text-faint">
				<Icon name="clock" size={11} aria-hidden />
				{t("transcript.sending")}
			</span>
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
	/** 底部被玻璃输入坞覆盖的高度（px）。 */
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
				// 用户气泡与会话分隔线铺满整行；其余块都从正文列开始
				const bodyCol =
					item !== undefined &&
					item.kind !== "session_divider" &&
					!isUserRow(item);
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
						className="absolute left-0 w-full px-3 py-1.5 focus-within:z-10 hover:z-10 sm:px-5"
						style={{ transform: `translateY(${v.start}px)` }}
					>
						<div
							className={`group/row relative mx-auto max-w-3xl ${bodyCol ? "pl-8" : ""}`}
						>
							{turns.starts.has(v.index) && (
								// 本行消息自带左侧菜单时，悬停 / 聚焦让出头像位置（细指针）
								<span className="absolute top-1.5 left-0 transition-opacity duration-150 pointer-fine:group-has-[.msg-gutter:focus-within]/row:opacity-0 pointer-fine:group-has-[.msg-gutter:hover]/row:opacity-0">
									<AgentAvatar size={20} />
								</span>
							)}
							{item ? (
								<ItemView
									item={item}
									turn={turns.footers.get(v.index)}
									turnStart={turns.starts.has(v.index)}
								/>
							) : (
								<>
									{pendingText !== undefined && (
										<PendingPrompt text={pendingText} />
									)}
									<div className="pl-8">{footer}</div>
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
			pendingText,
		],
	);

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
				<button
					type="button"
					className="glass pop-in absolute right-[max(1rem,calc(50%-24rem))] z-20 inline-flex h-7 items-center gap-1.5 rounded-full px-3 text-muted text-xs hover:text-fg"
					style={{ bottom: insetBottom + 8 }}
					onClick={() => {
						stick.current = true;
						scrollToEnd();
					}}
				>
					<Icon name="arrowDown" size={13} />
					{t("transcript.jumpToEnd")}
				</button>
			)}
		</div>
	);
}
