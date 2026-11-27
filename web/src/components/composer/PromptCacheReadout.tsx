/**
 * 输入坞页脚的提示缓存读数（与上下文圆环同款）：小圆环随存活倒计时消退，后跟剩余时间与当前活动上下文的命中率；
 * 缓存失效时换成虚线红圈与下一条消息要重写的 token 数。悬停、聚焦或点按打开详情卡。
 *
 * 状态取自 omp `get_session_stats` 的 `promptCache`（Core 按会话真实请求、续缓存结果与上下文变化推出），
 * 命中率取同一应答的 `tokens`：Core 只累计 `state.messages` 与活动分支上的模型用量（压缩前记录与旁支在 `history` 里，不用）。
 * 取数时机：转录前进、续缓存开始 / 结束、模型或思考级别切换（合并 1.5 秒内的变化），
 * 以及本地倒计时到期那一刻（确认续期是否接上）。Core 不报告时（模型没有缓存存活时长、旧版 Core）不显示。
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import {
	type FocusEvent,
	type ReactNode,
	useCallback,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import i18n from "../../i18n";
import { formatTokens, formatUptime } from "../../lib/format";
import {
	cacheHitRatio,
	formatCountdown,
	type PromptCacheView,
	promptCacheView,
} from "../../lib/prompt-cache";
import type {
	PromptCacheColdReason,
	PromptCacheStatus,
	SessionStatsData,
} from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { agentQueryable, useThread } from "../../state/stores";
import type { ThreadViewState } from "../../state/thread-reducer";
import { formatClock, useTicker } from "../thread/reports/parts";
import { Badge, Meter, Ring, type Tone } from "../ui/data";
import { Floating } from "../ui/Floating";
import { Icon } from "../ui/icons";

/** 转录等信号变化后合并多久再取数：流式回合里条目接连落盘，不必每条都问一次 */
const REFRESH_DEBOUNCE_MS = 1_500;
/** 本地倒计时到期后再等多久取数：给续缓存结果落盘留一点时间 */
const EXPIRY_GRACE_MS = 1_000;
const OPEN_DELAY = 200;
const CLOSE_DELAY = 150;

/** Core 续缓存停止原因（cache-warmer 的固定英文短语）→ 文案键；不认识的原样显示 */
type WarmingReasonKey =
	| "waitingFirst"
	| "disabled"
	| "contextChanged"
	| "bridging"
	| "economics"
	| "requestNoCache"
	| "noLifetime"
	| "settled"
	| "unsafeReplay"
	| "bridged"
	| "bridgeFailed"
	| "refreshFailed"
	| "refreshMissed";
const WARMING_REASON_KEYS: Record<string, WarmingReasonKey | undefined> = {
	"waiting for first request": "waitingFirst",
	"cache warming disabled": "disabled",
	"conversation context changed": "contextChanged",
	"bridge compaction in progress": "bridging",
	"cache economics unavailable": "economics",
	"request disabled prompt caching": "requestNoCache",
	"cache lifetime unavailable": "noLifetime",
	"agent run settled": "settled",
	"request cannot be replayed safely": "unsafeReplay",
	"bridged by compaction": "bridged",
	"bridge compaction failed": "bridgeFailed",
	"refresh failed": "refreshFailed",
	"refresh missed the cache": "refreshMissed",
};

/** 最近一条续缓存提示的 key：续期开始与结束都要重新取数。 */
function lastWarmingNotice(s: ThreadViewState): string {
	for (let i = s.notices.length - 1; i >= 0; i--) {
		const n = s.notices[i];
		if (n?.kind === "cache_warming_start" || n?.kind === "cache_warming_end")
			return n.key;
	}
	return "";
}

/** 需要重新取数的信号：活动会话的条目、续缓存进展、模型与思考级别（换了会让缓存失效）。 */
function refreshSignal(s: ThreadViewState): string {
	const model = s.state?.model;
	return `${s.lastSeq}|${lastWarmingNotice(s)}|${model?.provider}/${model?.id}|${s.state?.thinkingLevel}`;
}

function usePromptCache(threadId: string) {
	const qc = useQueryClient();
	const queryable = useThread(threadId, agentQueryable);
	const signal = useThread(threadId, refreshSignal);
	const query = useQuery({
		queryKey: ["rpc", threadId, "prompt-cache"],
		// orb 冻结 / 停机时不问（控制面会拒绝且不重放）；恢复后随 enabled 自然重取
		enabled: queryable,
		retry: false,
		refetchOnWindowFocus: false,
		queryFn: () =>
			realtime.cmd(threadId, {
				type: "get_session_stats",
			}) as Promise<SessionStatsData>,
	});
	const refetch = useCallback(
		() =>
			void qc.invalidateQueries({
				queryKey: ["rpc", threadId, "prompt-cache"],
				exact: true,
			}),
		[qc, threadId],
	);

	// 首次挂载由查询自己取；之后信号每变一次（合并 1.5 秒）取一次
	const lastSignal = useRef(signal);
	useEffect(() => {
		if (lastSignal.current === signal) return;
		lastSignal.current = signal;
		if (!queryable) return;
		const timer = setTimeout(refetch, REFRESH_DEBOUNCE_MS);
		return () => clearTimeout(timer);
	}, [signal, queryable, refetch]);

	const status = query.data?.promptCache;
	// 倒计时到期那一刻再问一次：续期接上了就继续倒计时，没接上 Core 会给出失效原因
	useEffect(() => {
		if (!queryable || status?.state !== "warm" || status.ttlMs === undefined)
			return;
		const delay =
			status.touchedAt + status.ttlMs - Date.now() + EXPIRY_GRACE_MS;
		// 取回时就已过期（续期在途被暂缓）：等续缓存结束的提示，不在这里反复取
		if (delay <= EXPIRY_GRACE_MS) return;
		const timer = setTimeout(refetch, delay);
		return () => clearTimeout(timer);
	}, [status, queryable, refetch]);

	const tokens = query.data?.tokens;
	return { status, hitRatio: tokens ? cacheHitRatio(tokens) : undefined };
}

function viewTone(view: PromptCacheView): Tone {
	switch (view.kind) {
		case "warm":
			return view.low ? "warn" : "ok";
		case "persistent":
			return "frost";
		case "refreshing":
			return "accent";
		case "cold":
			return "err";
	}
}

function badgeKey(view: PromptCacheView): "low" | PromptCacheView["kind"] {
	return view.kind === "warm" && view.low ? "low" : view.kind;
}

/** 失效时的虚线圈，与上下文圆环同尺寸。 */
function ColdRing(): ReactNode {
	return (
		<svg
			aria-hidden
			viewBox="0 0 20 20"
			width={12}
			height={12}
			className="shrink-0 text-err"
		>
			<circle
				cx={10}
				cy={10}
				r={7.5}
				fill="none"
				stroke="currentColor"
				strokeWidth={2.5}
				strokeDasharray="3 3"
			/>
		</svg>
	);
}

function resendText(
	t: TFunction,
	status: PromptCacheStatus,
	exact: boolean,
): string {
	return t(exact ? "promptCache.resend" : "promptCache.resendUpTo", {
		tokens: formatTokens(status.nextTokens),
	});
}

function reasonText(t: TFunction, reason: PromptCacheColdReason): string {
	switch (reason.kind) {
		case "expired":
			return t("promptCache.reason.expired", { time: formatClock(reason.at) });
		case "model":
		case "thinking":
			return t(`promptCache.reason.${reason.kind}`, {
				from: reason.from,
				to: reason.to,
			});
		default:
			return t(`promptCache.reason.${reason.kind}`);
	}
}

function warmingText(t: TFunction, status: PromptCacheStatus): string {
	const w = status.warming;
	if (!w) return t("promptCache.warming.unknown");
	if (w.state === "refreshing") return t("promptCache.warming.refreshing");
	if (w.state === "inactive") {
		if (!w.reason) return t("promptCache.warming.inactive");
		const key = WARMING_REASON_KEYS[w.reason];
		return t("promptCache.warming.inactiveReason", {
			reason: key ? t(`promptCache.warmingReason.${key}`) : w.reason,
		});
	}
	const next =
		w.nextWarmAt === undefined
			? t("promptCache.warming.scheduledNoTime")
			: t("promptCache.warming.scheduled", { time: formatClock(w.nextWarmAt) });
	return w.stopsAt === undefined
		? next
		: `${next} · ${t("promptCache.warming.until", { time: formatClock(w.stopsAt) })}`;
}

function DetailCard({
	status,
	view,
	hitRatio,
}: {
	status: PromptCacheStatus;
	view: PromptCacheView;
	hitRatio: number | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const tone = viewTone(view);
	const reads = Math.min(status.cachedTokens, status.nextTokens);
	const rows: [string, ReactNode][] = [];
	if (view.kind === "cold") {
		if (view.reason)
			rows.push([t("promptCache.rows.reason"), reasonText(t, view.reason)]);
		rows.push([
			t("promptCache.rows.next"),
			<span key="next" className="text-err">
				{resendText(t, status, view.exact)}
			</span>,
		]);
	} else {
		rows.push([
			t("promptCache.rows.next"),
			t("promptCache.reads", {
				read: formatTokens(reads),
				fresh: formatTokens(Math.max(0, status.nextTokens - reads)),
			}),
		]);
	}
	if (hitRatio !== undefined)
		rows.push([t("promptCache.rows.hit"), `${Math.round(hitRatio * 100)}%`]);
	rows.push([
		t("promptCache.rows.cached"),
		t("promptCache.tokens", { tokens: formatTokens(status.cachedTokens) }),
	]);
	rows.push([t("promptCache.rows.warming"), warmingText(t, status)]);

	return (
		<div className="text-muted text-xs">
			<div className="mb-2 flex items-center gap-2">
				<Icon name="database" size={14} aria-hidden className="shrink-0" />
				<span className="font-medium text-fg">{t("promptCache.title")}</span>
				<span className="ml-auto">
					<Badge tone={tone}>{t(`promptCache.badge.${badgeKey(view)}`)}</Badge>
				</span>
			</div>
			{view.kind === "warm" ? (
				<>
					<div className="mb-1.5 flex items-baseline gap-1.5">
						<span
							className={`font-semibold text-xl tabular-nums ${view.low ? "text-warn" : "text-ok"}`}
						>
							{status.inferred ? "~" : ""}
							{formatCountdown(view.remainingMs)}
						</span>
						<span className="text-faint">
							{t("promptCache.remaining", {
								ttl: formatUptime(view.ttlMs, i18n.language),
								time: formatClock(view.expiresAt),
							})}
						</span>
					</div>
					<Meter
						value={view.remainingMs}
						max={view.ttlMs}
						tone={tone}
						label={t("promptCache.title")}
						className="mb-2.5"
					/>
				</>
			) : (
				view.kind !== "cold" && (
					<p className="mb-2.5 leading-5">
						{t(
							view.kind === "persistent"
								? "promptCache.persistentNote"
								: "promptCache.refreshingNote",
						)}
					</p>
				)
			)}
			<dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
				{rows.map(([k, v]) => (
					<div key={k} className="contents">
						<dt className="text-faint">{k}</dt>
						<dd className="text-right text-fg tabular-nums">{v}</dd>
					</div>
				))}
			</dl>
			{status.inferred && (
				<p className="mt-2 text-2xs text-faint leading-4">
					{t("promptCache.inferred")}
				</p>
			)}
		</div>
	);
}

export function PromptCacheReadout({
	threadId,
}: {
	threadId: string;
}): ReactNode {
	const { t } = useTranslation();
	const { status, hitRatio } = usePromptCache(threadId);
	const ticking = status?.state === "warm" && status.ttlMs !== undefined;
	const now = useTicker(ticking);
	const anchor = useRef<HTMLButtonElement>(null);
	const card = useRef<HTMLDivElement>(null);
	const [open, setOpen] = useState(false);
	const timer = useRef<number | undefined>(undefined);
	const schedule = (next: boolean, ms: number) => {
		window.clearTimeout(timer.current);
		timer.current = window.setTimeout(() => setOpen(next), ms);
	};
	const hold = () => window.clearTimeout(timer.current);
	useEffect(() => () => window.clearTimeout(timer.current), []);
	useEffect(() => {
		if (!open) return;
		const onKey = (e: KeyboardEvent) => {
			if (e.key === "Escape") setOpen(false);
		};
		// 触屏点开后点别处关闭
		const onDown = (e: PointerEvent) => {
			const target = e.target as Node;
			if (anchor.current?.contains(target) || card.current?.contains(target))
				return;
			setOpen(false);
		};
		window.addEventListener("keydown", onKey);
		document.addEventListener("pointerdown", onDown);
		return () => {
			window.removeEventListener("keydown", onKey);
			document.removeEventListener("pointerdown", onDown);
		};
	}, [open]);
	if (!status) return null;

	const view = promptCacheView(status, ticking ? now : Date.now());
	const tone = viewTone(view);
	const hit =
		hitRatio !== undefined && view.kind !== "cold" ? (
			<span className="text-faint max-sm:hidden">
				· {t("promptCache.hit", { pct: Math.round(hitRatio * 100) })}
			</span>
		) : null;
	/** 焦点去了按钮与详情卡之外才关 */
	const onBlur = (e: FocusEvent) => {
		const next = e.relatedTarget;
		if (
			next instanceof Node &&
			(anchor.current?.contains(next) || card.current?.contains(next))
		)
			return;
		schedule(false, 0);
	};
	const mouse = (e: { pointerType: string }) => e.pointerType === "mouse";

	return (
		<>
			<button
				ref={anchor}
				type="button"
				data-testid="prompt-cache"
				aria-expanded={open}
				className="-mx-1 inline-flex h-5 shrink-0 items-center gap-1 rounded-sm px-1 tabular-nums transition-colors duration-150 hover:bg-hover hover:text-fg"
				onPointerEnter={(e) => mouse(e) && schedule(true, OPEN_DELAY)}
				onPointerLeave={(e) => mouse(e) && schedule(false, CLOSE_DELAY)}
				onFocus={() => {
					hold();
					setOpen(true);
				}}
				onBlur={onBlur}
				onClick={() => {
					hold();
					setOpen(true);
				}}
			>
				{view.kind === "cold" ? (
					<>
						<ColdRing />
						<span className="text-err">{t("promptCache.cold")}</span>
						<span className="text-faint max-sm:hidden">
							· {resendText(t, status, view.exact)}
						</span>
					</>
				) : (
					<>
						<Ring
							value={
								view.kind === "warm"
									? (view.remainingMs / view.ttlMs) * 100
									: 100
							}
							size={12}
							tone={tone}
							label={t("promptCache.title")}
						/>
						<span
							className={view.kind === "warm" && view.low ? "text-warn" : ""}
						>
							{view.kind === "warm"
								? `${status.inferred ? "~" : ""}${formatCountdown(view.remainingMs)}`
								: t(
										view.kind === "persistent"
											? "promptCache.cached"
											: "promptCache.refreshing",
									)}
						</span>
						{hit}
					</>
				)}
			</button>
			{open && (
				<Floating
					ref={card}
					anchor={anchor}
					side="top"
					align="end"
					gap={8}
					bare
					motion={false}
					className="w-72 max-w-[calc(100vw-1rem)] p-3"
					onPointerEnter={hold}
					onPointerLeave={(e) => mouse(e) && schedule(false, CLOSE_DELAY)}
					onBlur={onBlur}
				>
					<DetailCard status={status} view={view} hitRatio={hitRatio} />
				</Floating>
			)}
		</>
	);
}
