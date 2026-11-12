/**
 * 虚拟滚动转录（@tanstack/react-virtual，动态测量高度）。
 * 贴底跟随：用户停在底部时随新内容自动滚动；上翻后停止跟随并显示「回到底部」。
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
import { Icon } from "../ui/icons";
import {
	BranchSummaryCard,
	CompactionCard,
	MemoMessageView,
	ToolCard,
} from "./MessageView";

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
	return (
		<div className="py-0.5 text-center text-[11px] text-faint">{text}</div>
	);
}

const NOTICE_TONE: Record<Notice["level"], string> = {
	info: "border-border text-muted",
	warning: "border-warn/40 text-warn",
	error: "border-err/40 text-err",
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
	return (
		<div
			className={`flex items-start gap-2 rounded-md border px-2.5 py-1.5 text-xs ${NOTICE_TONE[notice.level]}`}
		>
			<Icon
				name={notice.level === "info" ? "info" : "alert"}
				size={13}
				className="mt-0.5 shrink-0"
			/>
			<div className="min-w-0 flex-1">
				<span className="font-medium">{label}</span>
				{notice.text && (
					<span
						className={
							notice.kind === "command_output"
								? "mt-1 block whitespace-pre-wrap font-mono"
								: "ml-1.5"
						}
					>
						{notice.text}
					</span>
				)}
				{url && (
					<a
						className="ml-1.5 text-accent underline"
						href={url}
						target="_blank"
						rel="noopener noreferrer"
					>
						{url}
					</a>
				)}
			</div>
		</div>
	);
}

function ItemView({ item }: { item: TranscriptItem }): ReactNode {
	const { t } = useTranslation();
	switch (item.kind) {
		case "session_divider":
			return (
				<div className="flex items-center gap-3 py-2 text-[11px] text-faint">
					<div className="h-px flex-1 bg-border" />
					{t("transcript.newSession")}
					<div className="h-px flex-1 bg-border" />
				</div>
			);
		case "message":
			return (
				<MemoMessageView
					message={item.message}
					entryId={item.entryId}
					live={item.live}
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

export function Transcript({
	items,
	footer,
}: {
	items: TranscriptItem[];
	footer?: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const scrollRef = useRef<HTMLDivElement>(null);
	const [atBottom, setAtBottom] = useState(true);
	const stick = useRef(true);
	const count = items.length + 1; // 末尾一项放 footer（工作中指示等）
	const virtualizer = useVirtualizer({
		count,
		getScrollElement: () => scrollRef.current,
		estimateSize: () => 96,
		overscan: 6,
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

	// 内容增长（新条目 / 流式增量 / 高度重测）时，贴底则跟随
	// biome-ignore lint/correctness/useExhaustiveDependencies: items 与 total 变化即需要重新贴底
	useLayoutEffect(() => {
		if (stick.current) scrollToEnd();
	}, [items, total, scrollToEnd]);

	useEffect(() => {
		stick.current = true;
		scrollToEnd();
	}, [scrollToEnd]);

	const virtualItems = virtualizer.getVirtualItems();
	const rendered = useMemo(
		() =>
			virtualItems.map((v) => {
				const item = v.index < items.length ? items[v.index] : undefined;
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
						// 行内菜单（消息操作）会溢出到后面的行：悬停 / 聚焦时把本行抬到兄弟行之上
						className="absolute left-0 w-full px-3 py-1.5 focus-within:z-10 hover:z-10 sm:px-5"
						style={{ transform: `translateY(${v.start}px)` }}
					>
						<div className="mx-auto max-w-3xl">
							{item ? <ItemView item={item} /> : footer}
						</div>
					</div>
				);
			}),
		[virtualItems, items, footer, virtualizer.measureElement],
	);

	return (
		<div className="relative min-h-0 flex-1">
			<div
				ref={scrollRef}
				onScroll={onScroll}
				className="h-full overflow-y-auto overscroll-contain"
				data-testid="transcript"
			>
				<div className="relative w-full" style={{ height: total + 24 }}>
					{rendered}
				</div>
			</div>
			{!atBottom && (
				<button
					type="button"
					className="absolute right-4 bottom-3 inline-flex items-center gap-1 rounded-full border border-border bg-panel px-3 py-1 text-muted text-xs shadow hover:text-fg"
					onClick={() => {
						stick.current = true;
						scrollToEnd();
					}}
				>
					<Icon name="chevronDown" size={12} />
					{t("transcript.jumpToEnd")}
				</button>
			)}
		</div>
	);
}
