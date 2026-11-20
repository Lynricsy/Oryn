/**
 * `/btw` 旁路提问对话框，对齐 omp TUI 的 BTW 历史面板，左右分栏：
 * - 左栏是话题列表（新的在前，与 Core `get_btw_history`、TUI 一致）：首轮问题（最多两行）、时间（今天写时刻，否则写日期）、
 *   追问轮数；运行中的话题带中性转圈。顶部「新话题」把右栏切成提问输入。列表是 listbox，上下键切换。
 * - 右栏是选中话题的详情（`BtwTopic.tsx`），追问输入贴在详情底部，由页脚主按钮「追问」展开。
 * - 页脚：左「复制回答」，右「关闭」与主按钮（追问 / 运行中改为取消 / 新话题时为提问）。
 * - 手机（compact）是两级页面：列表页 → 详情页，详情左上角返回列表。
 * 打开时默认选中最新话题（`/btw <问题>` 先发问再打开，最新的就是刚发的那个）；用户点过之后固定在所点话题。
 * Core 一次只跑一个 btw（否则报「cancel it first」），有运行中或正在发起时提问与追问都禁用。
 */
import { useQuery } from "@tanstack/react-query";
import {
	type FormEvent,
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { formatDateTime } from "../../lib/format";
import { useBreakpoint } from "../../lib/viewport";
import type { RpcBtwHistoryRecord } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore, useThread } from "../../state/stores";
import { isRecord } from "../../tool-render/util";
import { Callout } from "../ui/data";
import { Icon } from "../ui/icons";
import {
	Button,
	Dialog,
	ErrorText,
	Spinner,
	TextInput,
} from "../ui/primitives";
import {
	BtwTopicDetail,
	copyableAnswer,
	latestTurn,
	SideQuestionHead,
} from "./BtwTopic";

const NEW_FORM = "btw-new-topic";
const FOLLOW_FORM = "btw-follow-up";

/** 列表里的时间：今天写时刻，今年写月日，更早连年份。 */
function topicTime(ts: number, now: number, lang: string): string {
	const d = new Date(ts);
	const n = new Date(now);
	const sameYear = d.getFullYear() === n.getFullYear();
	if (
		sameYear &&
		d.getMonth() === n.getMonth() &&
		d.getDate() === n.getDate()
	) {
		return new Intl.DateTimeFormat(lang, {
			hour: "2-digit",
			minute: "2-digit",
		}).format(d);
	}
	return new Intl.DateTimeFormat(
		lang,
		sameYear
			? { month: "short", day: "numeric" }
			: { year: "numeric", month: "short", day: "numeric" },
	).format(d);
}

export function BtwDialog({
	threadId,
	initialRecordId,
	onClose,
}: {
	threadId: string;
	/** 打开时（或对话框开着时换成新值）直接选中这个话题（解释卡的「在旁路提问里打开」） */
	initialRecordId?: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const compact = useBreakpoint() === "compact";
	const btw = useThread(threadId, (s) => s.btw);
	const order = useThread(threadId, (s) => s.btwOrder);
	const notices = useThread(threadId, (s) => s.notices);
	const [openedAt] = useState(Date.now);
	// 用户点选的话题；null 表示跟随最新的那个
	const [picked, setPicked] = useState<string | null>(initialRecordId ?? null);
	// 右栏（窄屏为整页）是新话题输入
	const [composing, setComposing] = useState(false);
	// 窄屏的两级页面；null 表示还没定：话题第一次出现时（可能在历史读回之后）定下来，
	// 最新话题正在回答（刚用 /btw 发问）就直接进详情，否则停在列表
	const [pageState, setPage] = useState<"list" | "detail" | null>(
		initialRecordId ? "detail" : null,
	);
	useEffect(() => {
		if (!initialRecordId) return;
		setPicked(initialRecordId);
		setComposing(false);
		setPage("detail");
	}, [initialRecordId]);
	const [question, setQuestion] = useState("");
	const [followOpen, setFollowOpen] = useState(false);
	const [followUp, setFollowUp] = useState("");
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
	const records = useMemo(
		() =>
			order.flatMap((id) => {
				const r = btw[id];
				return r ? [r] : [];
			}),
		[order, btw],
	);
	const running = records.some((r) => latestTurn(r).status === "running");
	const busy = running || asking;
	const selected = (picked === null ? undefined : btw[picked]) ?? records.at(0);
	// 还没有任何话题且历史在读：先转圈，读完再决定是不是空态（空态直接进新话题输入）
	const loading = selected === undefined && history.isPending;
	const newMode = !loading && (composing || selected === undefined);
	const topic = newMode ? undefined : selected;
	const topicRunning =
		topic !== undefined && latestTurn(topic).status === "running";
	const newest = records.at(0);
	const page =
		pageState ??
		(newest && latestTurn(newest).status === "running" ? "detail" : "list");
	const pageDecided = pageState !== null || newest === undefined;
	useEffect(() => {
		if (!pageDecided) setPage(page);
	}, [pageDecided, page]);

	const select = (id: string) => {
		setPicked(id);
		setComposing(false);
		setFollowOpen(false);
		setFollowUp("");
		setError(null);
	};
	const startNew = () => {
		setComposing(true);
		setFollowOpen(false);
		setError(null);
	};
	const backToList = () => {
		setComposing(false);
		setFollowOpen(false);
		setError(null);
		setPage("list");
	};

	const ask = async (q: string, recordId?: string) => {
		setAsking(true);
		try {
			const res = await realtime.cmd(
				threadId,
				recordId === undefined
					? { type: "btw", question: q }
					: { type: "btw", question: q, recordId },
			);
			return res.record.id;
		} finally {
			setAsking(false);
		}
	};
	const submitNew = (e: FormEvent) => {
		e.preventDefault();
		const q = question.trim();
		if (!q || busy) return;
		setError(null);
		ask(q).then((id) => {
			setQuestion("");
			setPicked(id);
			setComposing(false);
			setPage("detail");
		}, setError);
	};
	const submitFollowUp = (e: FormEvent) => {
		e.preventDefault();
		const q = followUp.trim();
		if (!q || busy || !topic) return;
		setError(null);
		ask(q, topic.id).then(() => {
			setFollowUp("");
			setFollowOpen(false);
		}, setError);
	};
	const cancel = () => {
		if (!topic) return;
		setError(null);
		realtime
			.cmd(threadId, { type: "btw_cancel", recordId: topic.id })
			.catch(setError);
	};

	const errorLine = error ? (
		<div className="px-5 pb-3 sm:px-7">
			<ErrorText error={error} />
		</div>
	) : null;
	const busyHint = <p className="mt-1.5 text-muted text-xs">{t("btw.busy")}</p>;

	const list = (
		<TopicList
			records={records}
			selectedId={newMode ? undefined : selected?.id}
			newActive={newMode}
			onNew={startNew}
			onPick={(id) => {
				select(id);
				if (compact) setPage("detail");
			}}
			onMove={compact ? setPicked : select}
		/>
	);

	const newPane = (
		<form
			id={NEW_FORM}
			onSubmit={submitNew}
			className="min-h-0 flex-1 overflow-y-auto px-5 pt-5 pb-6 sm:px-7"
		>
			<h3 className="font-semibold text-fg text-lg leading-snug tracking-tight">
				{t("btw.newTopic")}
			</h3>
			<p className="mt-1 text-muted text-xs">{t("btw.hint")}</p>
			<div className="mt-4">
				<TextInput
					autoFocus
					aria-label={t("btw.placeholder")}
					placeholder={t("btw.placeholder")}
					value={question}
					disabled={busy}
					onChange={(e) => setQuestion(e.target.value)}
				/>
				{busy && busyHint}
			</div>
			{records.length === 0 && (
				<p className="mt-3 text-faint text-xs">{t("btw.emptyHint")}</p>
			)}
			<div className="mt-3">
				<ErrorText error={error} />
			</div>
		</form>
	);

	const detailPane = topic && (
		<>
			<BtwTopicDetail key={topic.id} threadId={threadId} record={topic} />
			{followOpen && !topicRunning && (
				<form
					id={FOLLOW_FORM}
					onSubmit={submitFollowUp}
					className="border-border border-t px-5 pt-3 pb-3 sm:px-7"
				>
					<TextInput
						autoFocus
						aria-label={t("btw.followUpPlaceholder")}
						placeholder={t("btw.followUpPlaceholder")}
						value={followUp}
						disabled={busy}
						onChange={(e) => setFollowUp(e.target.value)}
					/>
				</form>
			)}
			{busy && !topicRunning && (
				<div className="px-5 pb-3 sm:px-7">{busyHint}</div>
			)}
			{errorLine}
		</>
	);

	const spinner = (
		<div className="flex flex-1 items-center justify-center text-faint">
			<Spinner size={16} />
		</div>
	);

	let body: ReactNode;
	if (!compact) {
		body = (
			<>
				<div className="flex w-64 shrink-0 flex-col border-border border-r lg:w-72">
					{list}
				</div>
				<div className="flex min-w-0 flex-1 flex-col">
					{loading ? spinner : newMode ? newPane : detailPane}
				</div>
			</>
		);
	} else if (loading) {
		body = spinner;
	} else if (newMode || page === "detail") {
		body = (
			<div className="flex min-w-0 flex-1 flex-col">
				{(!newMode || records.length > 0) && (
					<div className="px-3 pt-2">
						<Button size="sm" variant="ghost" icon="back" onClick={backToList}>
							{t("btw.topics")}
						</Button>
					</div>
				)}
				{newMode ? newPane : detailPane}
			</div>
		);
	} else {
		body = <div className="flex min-w-0 flex-1 flex-col">{list}</div>;
	}

	const close = <Button onClick={onClose}>{t("common.close")}</Button>;
	let footer: ReactNode = close;
	if (newMode) {
		footer = (
			<>
				{close}
				<Button
					type="submit"
					form={NEW_FORM}
					variant="primary"
					disabled={busy || !question.trim()}
				>
					{t("btw.ask")}
				</Button>
			</>
		);
	} else if (topic && (!compact || page === "detail")) {
		footer = (
			<>
				<CopyAnswer key={topic.id} text={copyableAnswer(topic)} />
				{close}
				{topicRunning ? (
					<Button variant="primary" onClick={cancel}>
						{t("common.cancel")}
					</Button>
				) : followOpen ? (
					<Button
						type="submit"
						form={FOLLOW_FORM}
						variant="primary"
						disabled={busy || !followUp.trim()}
					>
						{t("btw.followUp")}
					</Button>
				) : (
					<Button
						variant="primary"
						disabled={busy}
						title={busy ? t("btw.busy") : undefined}
						onClick={() => setFollowOpen(true)}
					>
						{t("btw.followUp")}
					</Button>
				)}
			</>
		);
	}

	return (
		<Dialog
			open
			size="xl"
			onClose={onClose}
			title={t("btw.title")}
			footer={footer}
		>
			{(history.error || historyNotices.length > 0) && (
				<div className="mb-3 flex flex-col gap-2">
					<ErrorText error={history.error} />
					{historyNotices.map((n) => (
						<Callout
							key={n.key}
							tone={n.level === "error" ? "err" : "warn"}
							role="alert"
						>
							{n.text}
						</Callout>
					))}
				</div>
			)}
			<div className="-mx-5 -mb-2 flex h-[min(68dvh,640px)] border-border border-y">
				{body}
			</div>
		</Dialog>
	);
}

/**
 * 话题列表：顶部「新话题」，下面是 listbox。选中项中性高亮（强调色只给「需要你」的东西），
 * 漫游 tabindex：只有选中项（无选中时第一项）在 Tab 序里，上下键 / Home / End 在项间移动并选中。
 */
function TopicList({
	records,
	selectedId,
	newActive,
	onNew,
	onPick,
	onMove,
}: {
	records: readonly RpcBtwHistoryRecord[];
	selectedId: string | undefined;
	newActive: boolean;
	onNew: () => void;
	onPick: (id: string) => void;
	onMove: (id: string) => void;
}) {
	const { t, i18n } = useTranslation();
	const listRef = useRef<HTMLDivElement>(null);
	const now = Date.now();
	const tabbable = selectedId ?? records[0]?.id;
	// 打开（窄屏是回到列表页）时焦点落在选中的话题上，上下键即可切换。话题可能在历史读回后才出现，
	// 所以等第一次有选中项时再聚焦（只做一次）；用户已经在输入框里打字就不抢焦点
	const focused = useRef(false);
	const hasSelection = selectedId !== undefined && records.length > 0;
	useLayoutEffect(() => {
		if (focused.current || !hasSelection) return;
		focused.current = true;
		const active = document.activeElement;
		if (
			active instanceof HTMLInputElement ||
			active instanceof HTMLTextAreaElement
		)
			return;
		listRef.current
			?.querySelector<HTMLElement>("[aria-selected=true]")
			?.focus();
	}, [hasSelection]);

	const move = (from: string, key: string) => {
		const i = records.findIndex((r) => r.id === from);
		const next =
			key === "ArrowDown"
				? Math.min(i + 1, records.length - 1)
				: key === "ArrowUp"
					? Math.max(i - 1, 0)
					: key === "Home"
						? 0
						: records.length - 1;
		const id = records[next]?.id;
		if (id === undefined) return;
		onMove(id);
		listRef.current
			?.querySelector<HTMLElement>(`[data-topic="${CSS.escape(id)}"]`)
			?.focus();
	};

	return (
		<>
			{/* 与下方滚动的话题列表之间一条分隔线：列表滚动时顶上被截的话题不会贴着「新话题」 */}
			<div className="border-border border-b px-2 pt-2 pb-2">
				<button
					type="button"
					aria-pressed={newActive}
					onClick={onNew}
					className={`flex h-9 w-full items-center gap-2 rounded-md px-3 text-left text-sm transition-colors duration-150 ${newActive ? "bg-active text-fg" : "text-muted hover:bg-hover hover:text-fg"}`}
				>
					<Icon name="plus" size={14} aria-hidden />
					{t("btw.newTopic")}
				</button>
			</div>
			{records.length === 0 ? (
				// 没有话题时只剩新话题输入（历史还在读时 newActive 为假，不先写空态）
				newActive && (
					<p className="px-5 py-3 text-faint text-xs">{t("btw.empty")}</p>
				)
			) : (
				<div
					ref={listRef}
					role="listbox"
					aria-label={t("btw.topics")}
					className="min-h-0 flex-1 overflow-y-auto scroll-py-2 px-2 pt-2 pb-2"
					onKeyDown={(e) => {
						if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key))
							return;
						const from = (e.target as HTMLElement).dataset.topic;
						if (from === undefined) return;
						e.preventDefault();
						move(from, e.key);
					}}
				>
					{records.map((r) => {
						const sel = r.id === selectedId;
						const status = latestTurn(r).status;
						const followUps = r.followUps?.length ?? 0;
						return (
							<button
								key={r.id}
								type="button"
								role="option"
								aria-selected={sel}
								data-topic={r.id}
								tabIndex={r.id === tabbable ? 0 : -1}
								onClick={() => onPick(r.id)}
								className={`mt-0.5 flex w-full flex-col gap-1 rounded-md px-3 py-2 text-left transition-colors duration-150 ${sel ? "bg-active" : "hover:bg-hover"}`}
							>
								<span className="line-clamp-2 break-words text-fg text-sm leading-snug">
									<SideQuestionHead question={r.question} variant="list" />
								</span>
								<span className="flex min-w-0 items-center gap-1.5 text-2xs text-faint tabular-nums">
									{status === "running" && <Spinner size={10} />}
									{status !== "complete" && (
										<span
											className={
												status === "error"
													? "text-err"
													: status === "running"
														? ""
														: "text-warn-ink"
											}
										>
											{t(`btw.status.${status}`)}
										</span>
									)}
									{status !== "complete" && <span aria-hidden>·</span>}
									<time
										dateTime={new Date(r.createdAt).toISOString()}
										title={formatDateTime(r.createdAt)}
									>
										{topicTime(r.createdAt, now, i18n.language)}
									</time>
									{followUps > 0 && (
										<>
											<span aria-hidden>·</span>
											<span>{t("btw.followUps", { count: followUps })}</span>
										</>
									)}
								</span>
							</button>
						);
					})}
				</div>
			)}
		</>
	);
}

/** 页脚左侧的「复制回答」：复制选中话题最近一个非空回答，成功后短暂显示「已复制」。 */
function CopyAnswer({ text }: { text: string | undefined }) {
	const { t } = useTranslation();
	const [done, setDone] = useState(false);
	return (
		<Button
			icon={done ? "check" : "copy"}
			className="mr-auto"
			disabled={text === undefined}
			onClick={() => {
				if (text === undefined) return;
				void navigator.clipboard?.writeText(text).then(() => {
					setDone(true);
					setTimeout(() => setDone(false), 1200);
				});
			}}
		>
			{done ? t("common.copied") : t("btw.copyAnswer")}
		</Button>
	);
}
