/**
 * `/btw` 旁路提问对话框，对齐 omp TUI 的 BTW 历史面板。一个话题是一段对话：标题是首轮问题，首轮回答之后
 * 追问以气泡、回答以正文依次排列；状态（回答中 / 已取消 / 被打断 / 出错）和「取消」落在所属的那一轮上；
 * 追问输入贴在话题底部；最新一轮起草的 `<inject>` 引导块单独成卡，可注入主会话（`btw_inject`）。
 * 话题按创建时间新的在前（与 Core `get_btw_history`、TUI 一致），最新与运行中的话题默认展开，其余折叠成一行。
 * Core 一次只跑一个 btw（否则报「cancel it first」），有运行中或正在发起时提问与追问都禁用。
 */
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatDateTime, formatRelative } from "../../lib/format";
import type { RpcBtwHistoryRecord, RpcBtwInjectState } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore, useThread } from "../../state/stores";
import { isRecord } from "../../tool-render/util";
import { Markdown } from "../markdown/Markdown";
import { CopyButton } from "../ui/CopyButton";
import { Badge, Callout, type Tone } from "../ui/data";
import { Icon } from "../ui/icons";
import {
	Button,
	Dialog,
	EmptyState,
	ErrorText,
	Spinner,
	TextInput,
} from "../ui/primitives";

type BtwTurn = Omit<
	RpcBtwHistoryRecord,
	"id" | "leafId" | "followUps" | "inject"
>;
type BtwStatus = BtwTurn["status"];

/** 折叠行上的状态徽标：绿黄红只表达成功 / 警告 / 错误，运行中是中性（强调色只给「需要你」的东西）。 */
const BTW_TONE: Record<BtwStatus, Tone> = {
	running: "neutral",
	complete: "ok",
	cancelled: "warn",
	error: "err",
	interrupted: "warn",
};

/** 追问气泡：与转录里的用户气泡同一形态（`MessageView` 的 USER_BUBBLE），字号按对话框正文收一档。 */
const QUESTION_BUBBLE =
	"w-fit min-w-0 max-w-[85%] whitespace-pre-wrap break-words rounded-lg rounded-br-xs border border-border bg-raised px-3 py-1.5 text-sm";

/** 最新一轮：追问末尾，无追问则是首轮（与 Core `getBtwLatestTurn` 一致）。 */
function latestTurn(record: RpcBtwHistoryRecord): BtwTurn {
	return record.followUps?.at(-1) ?? record;
}

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

export function BtwDialog({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const btw = useThread(threadId, (s) => s.btw);
	const order = useThread(threadId, (s) => s.btwOrder);
	const notices = useThread(threadId, (s) => s.notices);
	const [openedAt] = useState(Date.now);
	const [q, setQ] = useState("");
	const [error, setError] = useState<unknown>(null);
	// 一个 btw 命令在途（Core 还没写入运行中的记录）：同样不能再发起
	const [asking, setAsking] = useState(false);
	const history = useQuery({
		queryKey: ["rpc", threadId, "btw-history"],
		retry: false,
		queryFn: async () => {
			const data = await realtime.cmd(threadId, { type: "get_btw_history" });
			getThreadStore(threadId)
				.getState()
				.apply(
					data.records.map((record) => ({
						type: "frame" as const,
						frame: { type: "btw_record", record },
					})),
				);
			return data.records.length;
		},
	});
	// btw 历史存盘失败等 `notice`（source btw-history）也进转录；对话框遮住转录，打开期间到达的在这里再显示一次
	const historyNotices = useMemo(
		() =>
			notices.filter(
				(n) =>
					n.kind === "notice" &&
					n.ts >= openedAt &&
					isRecord(n.detail) &&
					n.detail.source === "btw-history",
			),
		[notices, openedAt],
	);
	const running = order.some((id) => {
		const r = btw[id];
		return r !== undefined && latestTurn(r).status === "running";
	});
	const busy = running || asking;

	const ask = async (question: string, recordId?: string) => {
		setAsking(true);
		try {
			await realtime.cmd(
				threadId,
				recordId === undefined
					? { type: "btw", question }
					: { type: "btw", question, recordId },
			);
		} finally {
			setAsking(false);
		}
	};

	return (
		<Dialog open wide onClose={onClose} title={t("btw.title")}>
			<p className="mb-3 text-muted text-xs">{t("btw.hint")}</p>
			<form
				className="flex gap-2"
				onSubmit={(e) => {
					e.preventDefault();
					const question = q.trim();
					if (!question || busy) return;
					setError(null);
					ask(question).then(() => setQ(""), setError);
				}}
			>
				<TextInput
					autoFocus
					aria-label={t("btw.placeholder")}
					placeholder={t("btw.placeholder")}
					value={q}
					disabled={busy}
					onChange={(e) => setQ(e.target.value)}
				/>
				<Button
					type="submit"
					variant="primary"
					className="shrink-0 whitespace-nowrap"
					disabled={busy || !q.trim()}
				>
					{t("btw.ask")}
				</Button>
			</form>
			{busy && <p className="mt-1.5 text-muted text-xs">{t("btw.busy")}</p>}
			<div className="mt-2">
				<ErrorText error={error ?? history.error} />
			</div>
			{historyNotices.map((n) => (
				<div key={n.key} className="mt-2">
					<Callout tone={n.level === "error" ? "err" : "warn"} role="alert">
						{n.text}
					</Callout>
				</div>
			))}
			{order.length === 0 && history.isSuccess ? (
				<EmptyState icon="chats" title={t("btw.empty")}>
					{t("btw.emptyHint")}
				</EmptyState>
			) : (
				<div className="mt-3 flex flex-col gap-2.5">
					{order.map((id, i) => {
						const r = btw[id];
						return r ? (
							<BtwTopic
								key={id}
								threadId={threadId}
								record={r}
								busy={busy}
								defaultOpen={i === 0 || latestTurn(r).status === "running"}
								onAsk={ask}
							/>
						) : null;
					})}
				</div>
			)}
		</Dialog>
	);
}

/**
 * 一个话题。折叠时是一行：首轮问题（单行截断）、非完成状态的徽标、轮数与时间；
 * 展开后标题完整换行，下面依次是各轮回答与追问，底部是追问输入。
 */
function BtwTopic({
	threadId,
	record,
	busy,
	defaultOpen,
	onAsk,
}: {
	threadId: string;
	record: RpcBtwHistoryRecord;
	busy: boolean;
	defaultOpen: boolean;
	onAsk: (question: string, recordId: string) => Promise<void>;
}) {
	const { t } = useTranslation();
	const turns: readonly BtwTurn[] = [record, ...(record.followUps ?? [])];
	const latest = latestTurn(record);
	const latestIndex = turns.length - 1;
	const [open, setOpen] = useState(defaultOpen);
	const [followUp, setFollowUp] = useState("");
	const [error, setError] = useState<unknown>(null);
	const [injectNote, setInjectNote] = useState<string | null>(null);
	// 最近一个非空回答，原样保留空白（与 TUI 的复制一致）
	const copy = turns
		.map((turn) => turn.answer)
		.findLast((answer) => answer.trim() !== "");

	const cancel = () => {
		setError(null);
		realtime
			.cmd(threadId, { type: "btw_cancel", recordId: record.id })
			.catch(setError);
	};
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
			className="rounded-lg border border-border"
		>
			<div className="flex items-start gap-2 px-3 pt-2.5 pb-2.5">
				<button
					type="button"
					aria-expanded={open}
					onClick={() => setOpen((v) => !v)}
					className="-mx-1 -my-0.5 flex min-w-0 flex-1 items-start gap-1.5 rounded-sm px-1 py-0.5 text-left hover:bg-hover"
				>
					<Icon
						name={open ? "chevronDown" : "chevronRight"}
						size={12}
						aria-hidden
						className="mt-[3px] shrink-0 text-faint"
					/>
					<span
						className={`min-w-0 flex-1 font-medium text-sm ${open ? "whitespace-pre-wrap break-words" : "truncate"}`}
					>
						{record.question}
					</span>
				</button>
				{!open && latest.status !== "complete" && (
					<Badge tone={BTW_TONE[latest.status]}>
						{t(`btw.status.${latest.status}`)}
					</Badge>
				)}
				<span
					title={formatDateTime(record.createdAt)}
					className="mt-0.5 shrink-0 whitespace-nowrap text-2xs text-faint"
				>
					{turns.length > 1 && `${t("btw.turns", { count: turns.length })} · `}
					{formatRelative(record.createdAt)}
				</span>
				{open && copy && (
					<span className="-my-0.5 shrink-0">
						<CopyButton text={copy} ariaLabel={t("common.copy")} />
					</span>
				)}
			</div>
			{open && (
				<div className="flex flex-col gap-3 px-3 pb-3">
					{turns.map((turn, i) => (
						<BtwTurnView
							// 轮次只追加不重排，下标即稳定身份
							// biome-ignore lint/suspicious/noArrayIndexKey: 同上
							key={i}
							turn={turn}
							followUp={i > 0}
							inject={i === latestIndex ? record.inject : undefined}
							onInject={inject}
							onCancel={i === latestIndex ? cancel : undefined}
						/>
					))}
					{injectNote && (
						<p role="status" className="text-warn-ink text-xs">
							{injectNote}
						</p>
					)}
					<ErrorText error={error} />
					<form
						className="flex gap-2"
						onSubmit={(e) => {
							e.preventDefault();
							const question = followUp.trim();
							if (!question || busy) return;
							setError(null);
							onAsk(question, record.id).then(() => setFollowUp(""), setError);
						}}
					>
						<TextInput
							aria-label={t("btw.followUpPlaceholder")}
							placeholder={t("btw.followUpPlaceholder")}
							title={busy ? t("btw.busy") : undefined}
							value={followUp}
							disabled={busy}
							onChange={(e) => setFollowUp(e.target.value)}
						/>
						<Button
							type="submit"
							className="shrink-0 whitespace-nowrap"
							disabled={busy || !followUp.trim()}
						>
							{t("btw.followUp")}
						</Button>
					</form>
				</div>
			)}
		</section>
	);
}

/**
 * 一轮问答：追问轮先是右对齐的问题气泡（首轮问题就是话题标题），然后是回答与这一轮的状态。
 * 回答里的 `<inject>` 块只在最新一轮单独成卡：流式中显示起草中，完成后按 Core 给的 inject 状态显示可注入 / 注入中 / 已注入。
 */
function BtwTurnView({
	turn,
	followUp,
	inject,
	onInject,
	onCancel,
}: {
	turn: BtwTurn;
	followUp: boolean;
	inject: RpcBtwInjectState | undefined;
	onInject: () => void;
	/** 只给最新一轮：运行中可取消。 */
	onCancel: (() => void) | undefined;
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
			<TurnStatus turn={turn} onCancel={onCancel} />
		</div>
	);
}

/** 一轮的状态行：完成不写；回答中带转圈与取消；已取消 / 被打断为警示小字；出错为错误提示块。 */
function TurnStatus({
	turn,
	onCancel,
}: {
	turn: BtwTurn;
	onCancel: (() => void) | undefined;
}): ReactNode {
	const { t } = useTranslation();
	switch (turn.status) {
		case "complete":
			return null;
		case "running":
			return (
				<div className="flex items-center gap-2 text-muted text-xs">
					<Spinner size={12} />
					<span>{t("btw.status.running")}</span>
					{onCancel && (
						<Button size="sm" variant="ghost" onClick={onCancel}>
							{t("common.cancel")}
						</Button>
					)}
				</div>
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
