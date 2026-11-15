/**
 * btw 对话框右栏（窄屏是详情页）：一个话题的完整内容。顶部是大号首轮问题与完整日期时间，下面依次是各轮问答
 * （首轮回答、追问气泡、追问回答）、最新一轮的 `<inject>` 引导卡与这一轮的状态。详情区自己滚动：
 * 切换话题时回到顶部（父组件按话题 id 重新挂载），追问新增一轮或贴底时随流式回答跟到底部。
 */
import { type ReactNode, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatDateTime } from "../../lib/format";
import type { RpcBtwHistoryRecord, RpcBtwInjectState } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { Markdown } from "../markdown/Markdown";
import { Callout } from "../ui/data";
import { Icon } from "../ui/icons";
import { Button, ErrorText, Spinner } from "../ui/primitives";

export type BtwTurn = Omit<
	RpcBtwHistoryRecord,
	"id" | "leafId" | "followUps" | "inject"
>;

/** 最新一轮：追问末尾，无追问则是首轮（与 Core `getBtwLatestTurn` 一致）。 */
export function latestTurn(record: RpcBtwHistoryRecord): BtwTurn {
	return record.followUps?.at(-1) ?? record;
}

/** 「复制回答」的内容：最近一个非空回答，原样保留空白（与 TUI 的复制一致）。 */
export function copyableAnswer(
	record: RpcBtwHistoryRecord,
): string | undefined {
	return [record, ...(record.followUps ?? [])]
		.map((turn) => turn.answer)
		.findLast((answer) => answer.trim() !== "");
}

/** 追问气泡：与转录里的用户气泡同一形态（`MessageView` 的 USER_BUBBLE），字号按对话框正文收一档。 */
const QUESTION_BUBBLE =
	"w-fit min-w-0 max-w-[85%] whitespace-pre-wrap break-words rounded-lg rounded-br-xs border border-border bg-raised px-3 py-1.5 text-sm";

/** 离底部不超过这么多像素算「贴底」，流式回答增长时跟着滚到底。 */
const STICK_SLACK = 40;

const INJECT_OPEN = "<inject>";
const INJECT_CLOSE = "</inject>";

interface InjectParts {
	before: string;
	inject: string;
	after: string;
	/** 没有闭合块：最后一个起始标签一直延伸到结尾，只在仍在流式时单独显示。 */
	closed: boolean;
}

/** 在最后一个闭合的 `<inject>` 块处切开回答（移植自 Core `splitBtwInject`）。 */
function splitInject(answer: string): InjectParts | undefined {
	const lastClose = answer.lastIndexOf(INJECT_CLOSE);
	const open =
		lastClose === -1
			? -1
			: answer.lastIndexOf(INJECT_OPEN, lastClose - INJECT_OPEN.length);
	if (open !== -1) {
		const close = answer.indexOf(INJECT_CLOSE, open + INJECT_OPEN.length);
		return {
			before: answer.slice(0, open),
			inject: answer.slice(open + INJECT_OPEN.length, close),
			after: answer.slice(close + INJECT_CLOSE.length),
			closed: true,
		};
	}
	const streaming = answer.lastIndexOf(INJECT_OPEN);
	if (streaming === -1) return undefined;
	return {
		before: answer.slice(0, streaming),
		inject: answer.slice(streaming + INJECT_OPEN.length),
		after: "",
		closed: false,
	};
}

/** 一个话题的详情；`section` 以首轮问题为名，读屏与测试按它定位。 */
export function BtwTopicDetail({
	threadId,
	record,
}: {
	threadId: string;
	record: RpcBtwHistoryRecord;
}) {
	const { t } = useTranslation();
	const turns: readonly BtwTurn[] = [record, ...(record.followUps ?? [])];
	const latestIndex = turns.length - 1;
	const [error, setError] = useState<unknown>(null);
	const [injectNote, setInjectNote] = useState<string | null>(null);
	const scrollRef = useRef<HTMLDivElement>(null);
	const stick = useRef(false);
	const seenTurns = useRef(turns.length);
	const latestLength = turns[latestIndex]?.answer.length ?? 0;

	// 新增一轮（刚发出的追问）时滚到底并开始贴底；之后只要还贴着底，流式增长就跟着滚
	// biome-ignore lint/correctness/useExhaustiveDependencies: 最新回答变长（流式）时也要重新贴底
	useLayoutEffect(() => {
		const el = scrollRef.current;
		if (!el) return;
		if (turns.length > seenTurns.current) stick.current = true;
		seenTurns.current = turns.length;
		if (stick.current) el.scrollTop = el.scrollHeight;
	}, [turns.length, latestLength]);

	const inject = () => {
		setError(null);
		setInjectNote(null);
		realtime
			.cmd(threadId, { type: "btw_inject", recordId: record.id })
			.then((r) => {
				if (!r.sent)
					setInjectNote(
						r.reason
							? t("btw.inject.refused", { reason: r.reason })
							: t("btw.inject.notSent"),
					);
			}, setError);
	};

	return (
		<section
			aria-label={record.question}
			ref={scrollRef}
			onScroll={(e) => {
				const el = e.currentTarget;
				stick.current =
					el.scrollHeight - el.scrollTop - el.clientHeight <= STICK_SLACK;
			}}
			className="min-h-0 flex-1 overflow-y-auto px-5 pt-5 pb-6 sm:px-7"
		>
			<h3 className="whitespace-pre-wrap break-words font-semibold text-fg text-lg leading-snug tracking-tight">
				{record.question}
			</h3>
			<p className="mt-1 text-faint text-xs tabular-nums">
				{formatDateTime(record.createdAt)}
			</p>
			<div className="mt-4 flex flex-col gap-4">
				{turns.map((turn, i) => (
					<BtwTurnView
						// 轮次只追加不重排，下标即稳定身份
						// biome-ignore lint/suspicious/noArrayIndexKey: 同上
						key={i}
						turn={turn}
						followUp={i > 0}
						inject={i === latestIndex ? record.inject : undefined}
						onInject={inject}
					/>
				))}
				{injectNote && (
					<p role="status" className="text-warn-ink text-xs">
						{injectNote}
					</p>
				)}
				<ErrorText error={error} />
			</div>
		</section>
	);
}

/**
 * 一轮问答：追问轮先是右对齐的问题气泡（首轮问题就是详情标题），然后是回答与这一轮的状态。
 * 回答里的 `<inject>` 块只在最新一轮单独成卡：流式中显示起草中，完成后按 Core 给的 inject 状态显示可注入 / 注入中 / 已注入。
 */
function BtwTurnView({
	turn,
	followUp,
	inject,
	onInject,
}: {
	turn: BtwTurn;
	followUp: boolean;
	inject: RpcBtwInjectState | undefined;
	onInject: () => void;
}) {
	const streaming = turn.status === "running";
	const parts = splitInject(turn.answer);
	const block =
		parts && (streaming || (parts.closed && inject !== undefined))
			? parts
			: undefined;
	return (
		<div className="flex flex-col gap-2">
			{followUp && (
				<div className="flex justify-end">
					<p className={QUESTION_BUBBLE}>{turn.question}</p>
				</div>
			)}
			{block ? (
				<>
					<Prose text={block.before} streaming={streaming} />
					<InjectCard
						text={block.inject.trim()}
						state={streaming ? undefined : inject}
						onInject={onInject}
					/>
					<Prose text={block.after} streaming={streaming} />
				</>
			) : (
				<Prose text={turn.answer} streaming={streaming} />
			)}
			<TurnStatus turn={turn} />
		</div>
	);
}

/** 一轮的状态行：完成不写；回答中是中性转圈（取消在页脚主按钮）；已取消 / 被打断为警示小字；出错为错误提示块。 */
function TurnStatus({ turn }: { turn: BtwTurn }): ReactNode {
	const { t } = useTranslation();
	switch (turn.status) {
		case "complete":
			return null;
		case "running":
			return (
				<p className="flex items-center gap-2 text-muted text-xs">
					<Spinner size={12} />
					{t("btw.status.running")}
				</p>
			);
		case "cancelled":
			return (
				<p className="flex items-center gap-1.5 text-warn-ink text-xs">
					<Icon name="minusCircle" size={13} aria-hidden />
					{t("btw.status.cancelled")}
				</p>
			);
		case "interrupted":
			return (
				<p className="flex items-center gap-1.5 text-warn-ink text-xs">
					<Icon name="circleHalf" size={13} aria-hidden />
					{t("btw.note.interrupted")}
				</p>
			);
		case "error":
			return (
				<Callout tone="err" title={t("btw.note.error")}>
					{turn.error}
				</Callout>
			);
	}
}

function Prose({
	text,
	streaming,
}: {
	text: string;
	streaming: boolean;
}): ReactNode {
	const trimmed = text.trim();
	return trimmed ? <Markdown text={trimmed} streaming={streaming} /> : null;
}

/** `<inject>` 引导块：内嵌井底色，标签说明用途；可注入时给墨色主按钮，注入中转圈，已注入为成功色勾；`state` 缺省表示仍在起草。 */
function InjectCard({
	text,
	state,
	onInject,
}: {
	text: string;
	state: RpcBtwInjectState | undefined;
	onInject: () => void;
}) {
	const { t } = useTranslation();
	return (
		<div className="rounded-md bg-inset px-3 py-2.5">
			<p className="mb-1 font-medium text-2xs text-faint">
				{t("btw.inject.caption")}
			</p>
			<p className="whitespace-pre-wrap break-words text-sm">{text}</p>
			<div className="mt-2 flex items-center gap-1.5 text-xs">
				{state === undefined && (
					<span className="text-muted">{t("btw.inject.drafting")}</span>
				)}
				{state === "ready" && (
					<Button size="sm" variant="primary" icon="send" onClick={onInject}>
						{t("btw.inject.send")}
					</Button>
				)}
				{state === "sending" && (
					<span className="flex items-center gap-1.5 text-muted">
						<Spinner size={12} />
						{t("btw.inject.sending")}
					</span>
				)}
				{state === "sent" && (
					<span className="flex items-center gap-1 text-ok-ink">
						<Icon name="check" size={12} aria-hidden />
						{t("btw.inject.sent")}
					</span>
				)}
			</div>
		</div>
	);
}
