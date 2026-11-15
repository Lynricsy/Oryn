/**
 * 用量报告（`/usage`）：每个供应商账号一张卡，限额用量表显示百分比与重置倒计时；下方是本会话的本地统计；
 * 底部是已存的限额重置（`list_reset_credits` → 确认 → `redeem_reset_credit`）。
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	formatBytes,
	formatCost,
	formatCount,
	formatDateTime,
	formatRelative,
	formatTokens,
} from "../../../lib/format";
import type {
	ResponseData,
	RpcResetCreditAccount,
	RpcResetCreditRedeemResult,
	RpcUsageLimit,
	RpcUsageReport,
} from "../../../omp/types";
import { realtime } from "../../../realtime/socket";
import { Badge, Callout, Meter, thresholdTone } from "../../ui/data";
import { ModelLogo } from "../../ui/ModelLogo";
import { Button, describeError, Spinner } from "../../ui/primitives";
import {
	ConfirmStrip,
	ReportCard,
	ReportError,
	ReportSection,
	StatGrid,
	useTicker,
} from "./parts";

type UsageData = ResponseData<"get_usage">;

/** 已用比例：优先 `usedFraction`，其次 `remainingFraction`，再其次 used / limit。 */
export function usedFractionOf(l: RpcUsageLimit): number | undefined {
	if (typeof l.usedFraction === "number") return l.usedFraction;
	if (typeof l.remainingFraction === "number") return 1 - l.remainingFraction;
	if (typeof l.used === "number" && typeof l.limit === "number" && l.limit > 0)
		return l.used / l.limit;
	return undefined;
}

/** 按单位格式化一个数（不带单位词）；`usd` / `bytes` / `tokens` 自带单位写法。 */
function formatAmount(n: number, unit: string | undefined): string {
	switch (unit) {
		case "tokens":
			return formatTokens(n);
		case "usd":
			return formatCost(n);
		case "bytes":
			return formatBytes(n);
		default:
			return formatCount(n);
	}
}

/** 「已用 / 上限」读数：两个数共用一个单位词（`910 / 1,000 次请求`）；百分比单位不显示（量表上方已有）。 */
function usedText(t: TFunction, l: RpcUsageLimit): string | null {
	if (typeof l.used !== "number" || l.unit === "percent") return null;
	const value =
		typeof l.limit === "number"
			? `${formatAmount(l.used, l.unit)} / ${formatAmount(l.limit, l.unit)}`
			: formatAmount(l.used, l.unit);
	const withUnit =
		l.unit === "minutes" || l.unit === "requests" || l.unit === "credits"
			? t(`report.usage.${l.unit}`, { n: value })
			: value;
	return typeof l.limit === "number"
		? withUnit
		: t("report.usage.usedOnly", { n: withUnit });
}

function LimitRow({
	limit,
	now,
}: {
	limit: RpcUsageLimit;
	now: number;
}): ReactNode {
	const { t } = useTranslation();
	const fraction = usedFractionOf(limit);
	const percent = fraction === undefined ? undefined : fraction * 100;
	const exhausted =
		limit.status === "exhausted" || (percent !== undefined && percent >= 100);
	const tone = exhausted
		? "err"
		: percent === undefined
			? "neutral"
			: thresholdTone(percent);
	const used = usedText(t, limit);
	const resets =
		typeof limit.resetsAt === "number" && limit.resetsAt > 0
			? limit.resetsAt > now
				? t("report.usage.resetsIn", {
						when: formatRelative(limit.resetsAt, now),
					})
				: t("report.usage.resetDue")
			: null;
	return (
		<li data-limit={limit.id} className="flex flex-col gap-1.5">
			<div className="flex min-w-0 items-baseline gap-2">
				<span className="min-w-0 truncate text-sm">{limit.label}</span>
				{limit.windowLabel && limit.windowLabel !== limit.label && (
					<span className="shrink-0 text-faint text-xs">
						{limit.windowLabel}
					</span>
				)}
				<span className="flex-1" />
				{exhausted && <Badge tone="err">{t("report.usage.exhausted")}</Badge>}
				<span
					className={`shrink-0 font-semibold text-sm tabular-nums ${tone === "err" ? "text-err" : tone === "warn" ? "text-warn" : ""}`}
				>
					{percent === undefined ? "—" : `${Math.round(percent)}%`}
				</span>
			</div>
			{percent !== undefined && (
				<Meter
					value={percent}
					max={100}
					tone={tone}
					label={t("report.usage.meter", {
						label: limit.label,
						percent: Math.round(percent),
					})}
				/>
			)}
			{(used || resets) && (
				<div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-faint text-xs tabular-nums">
					{used && <span>{used}</span>}
					<span className="flex-1" />
					{resets && (
						<span
							title={
								limit.resetsAt ? formatDateTime(limit.resetsAt) : undefined
							}
						>
							{resets}
						</span>
					)}
				</div>
			)}
			{limit.notes?.map((n) => (
				<p key={n} className="text-faint text-xs">
					{n}
				</p>
			))}
		</li>
	);
}

function ProviderCard({
	report,
	now,
}: {
	report: RpcUsageReport;
	now: number;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<ReportCard>
			<div
				data-provider={report.provider}
				className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1"
			>
				<ModelLogo provider={report.provider} className="text-muted" />
				<span className="font-medium text-sm">{report.provider}</span>
				{report.account && (
					<span className="min-w-0 truncate text-faint text-xs">
						{report.account}
					</span>
				)}
				<span className="flex-1" />
				{(report.resetCreditsAvailable ?? 0) > 0 && (
					<Badge icon="restart">
						{t("report.usage.creditsBadge", {
							count: report.resetCreditsAvailable,
						})}
					</Badge>
				)}
				{report.fetchedAt > 0 && (
					<span
						className="text-2xs text-faint"
						title={formatDateTime(report.fetchedAt)}
					>
						{t("report.usage.fetched", {
							when: formatRelative(report.fetchedAt, now),
						})}
					</span>
				)}
			</div>
			{report.limits.length > 0 ? (
				<ul className="mt-3 flex flex-col gap-3.5">
					{report.limits.map((l) => (
						<LimitRow key={l.id} limit={l} now={now} />
					))}
				</ul>
			) : (
				<p className="mt-2 text-muted text-sm">{t("report.usage.noLimits")}</p>
			)}
			{report.notes?.map((n) => (
				<p key={n} className="mt-2 text-faint text-xs">
					{n}
				</p>
			))}
		</ReportCard>
	);
}

type RedeemCode =
	| "reset"
	| "already_redeemed"
	| "no_credit"
	| "credit_list_failed"
	| "nothing_to_reset"
	| "no_account"
	| "account_unavailable";

/** Core `describeRedeemOutcome` 的结果码；有对应译文，其余结果码用 Core 原文。 */
const REDEEM_CODES: Record<string, RedeemCode> = {
	reset: "reset",
	already_redeemed: "already_redeemed",
	no_credit: "no_credit",
	credit_list_failed: "credit_list_failed",
	nothing_to_reset: "nothing_to_reset",
	no_account: "no_account",
	account_unavailable: "account_unavailable",
};

function redeemText(
	t: TFunction,
	r: RpcResetCreditRedeemResult,
	label: string,
): string {
	const code = REDEEM_CODES[r.code];
	return code ? t(`report.usage.redeem.${code}`, { label }) : r.message;
}

function CreditRow({
	threadId,
	account,
	onRedeemed,
}: {
	threadId: string;
	account: RpcResetCreditAccount;
	onRedeemed: (r: RpcResetCreditRedeemResult) => void;
}): ReactNode {
	const { t } = useTranslation();
	const [confirming, setConfirming] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const usable = !account.error && account.redeemableCount > 0;
	const redeem = async () => {
		setBusy(true);
		setError(null);
		try {
			const r = (await realtime.cmd(threadId, {
				type: "redeem_reset_credit",
				target: account.target,
				label: account.label,
			})) as RpcResetCreditRedeemResult;
			setConfirming(false);
			onRedeemed(r);
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};
	const id = `${account.provider}/${account.target.credentialId}`;
	return (
		<li
			data-credit={id}
			className="flex flex-col gap-2 border-border border-t py-2.5 first:border-t-0 first:pt-0 last:pb-0"
		>
			<div className="flex min-w-0 items-start gap-3">
				<div className="flex min-w-0 flex-1 flex-col gap-0.5">
					<div className="flex min-w-0 flex-wrap items-center gap-1.5 text-sm">
						<span className="min-w-0 truncate">{account.label}</span>
						<Badge>{account.providerLabel}</Badge>
						{account.active && <Badge>{t("report.usage.activeAccount")}</Badge>}
					</div>
					<div className="text-faint text-xs tabular-nums">
						{account.error
							? t("report.usage.creditUnavailable", { reason: account.error })
							: t("report.usage.creditCounts", {
									saved: account.availableCount,
									usable: account.redeemableCount,
								})}
						{account.expiresAt &&
							` · ${t("report.usage.creditExpires", {
								when: Number.isNaN(Date.parse(account.expiresAt))
									? account.expiresAt
									: formatDateTime(Date.parse(account.expiresAt)),
							})}`}
					</div>
					{!account.error &&
						account.redeemableCount === 0 &&
						account.unavailableReason && (
							<div className="text-faint text-xs">
								{account.unavailableReason}
							</div>
						)}
				</div>
				{!confirming && (
					<Button
						size="sm"
						disabled={!usable}
						onClick={() => setConfirming(true)}
					>
						{t("report.usage.redeemOne")}
					</Button>
				)}
			</div>
			{confirming && (
				<ConfirmStrip
					label={t("report.usage.confirmTitle")}
					body={t("report.usage.confirmBody", { label: account.label })}
					cancelLabel={t("common.cancel")}
					confirmLabel={t("report.usage.confirmRedeem")}
					busy={busy}
					onCancel={() => setConfirming(false)}
					onConfirm={() => void redeem()}
				/>
			)}
			{error ? (
				<p role="alert" className="text-err text-xs">
					{t("report.usage.redeemFailed", { reason: describeError(error, t) })}
				</p>
			) : null}
		</li>
	);
}

function ResetCredits({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [open, setOpen] = useState(false);
	const [result, setResult] = useState<{
		ok: boolean;
		text: string;
	} | null>(null);
	const credits = useQuery({
		queryKey: ["rpc", threadId, { type: "list_reset_credits" }],
		enabled: open,
		retry: false,
		queryFn: () =>
			realtime.cmd(threadId, { type: "list_reset_credits" }) as Promise<
				ResponseData<"list_reset_credits">
			>,
	});
	const onRedeemed = (
		r: RpcResetCreditRedeemResult,
		account: RpcResetCreditAccount,
	) => {
		setResult({ ok: r.ok, text: redeemText(t, r, account.label) });
		void qc.invalidateQueries({
			queryKey: ["rpc", threadId, { type: "list_reset_credits" }],
		});
		// 与 `Dialogs.tsx` 的 `useRpc` 同一种 key：用量报告随之重读
		void qc.invalidateQueries({
			queryKey: ["rpc", threadId, { type: "get_usage" }],
		});
	};
	return (
		<ReportSection title={t("report.usage.creditsTitle")}>
			<ReportCard>
				{!open ? (
					<div className="flex flex-wrap items-center gap-3">
						<p className="min-w-0 flex-1 text-muted text-sm">
							{t("report.usage.creditsIntro")}
						</p>
						<Button size="sm" icon="restart" onClick={() => setOpen(true)}>
							{t("report.usage.creditsOpen")}
						</Button>
					</div>
				) : credits.isPending ? (
					<div className="flex items-center gap-2 text-muted text-sm">
						<Spinner />
						{t("report.usage.creditsLoading")}
					</div>
				) : credits.isError ? (
					<ReportError
						title={t("report.usage.creditsFailed")}
						error={credits.error}
						retrying={credits.isFetching}
						onRetry={() => void credits.refetch()}
					/>
				) : credits.data.accounts.length === 0 ? (
					<p className="text-muted text-sm">{t("report.usage.creditsNone")}</p>
				) : (
					<ul className="flex flex-col">
						{credits.data.accounts.map((a) => (
							<CreditRow
								key={`${a.provider}/${a.target.credentialId}`}
								threadId={threadId}
								account={a}
								onRedeemed={(r) => onRedeemed(r, a)}
							/>
						))}
					</ul>
				)}
			</ReportCard>
			{result && (
				<Callout tone={result.ok ? "ok" : "warn"} role="status">
					{result.text}
				</Callout>
			)}
		</ReportSection>
	);
}

export function UsageReport({
	threadId,
	data,
}: {
	threadId: string;
	data: UsageData;
}): ReactNode {
	const { t } = useTranslation();
	// 相对时间（重置倒计时、更新于）每分钟走一次
	const now = useTicker(true, 60_000);
	const s = data.session;
	return (
		<div className="flex flex-col gap-4">
			<ReportSection title={t("report.usage.providers")}>
				{data.reports.length > 0 ? (
					data.reports.map((r) => (
						<ProviderCard
							key={`${r.provider}/${r.account ?? ""}`}
							report={r}
							now={now}
						/>
					))
				) : (
					<Callout tone="neutral" role="status">
						{t("report.usage.noProviders")}
					</Callout>
				)}
			</ReportSection>
			<ReportSection title={t("report.usage.session")}>
				<ReportCard>
					<StatGrid
						items={[
							{
								key: "cost",
								label: t("report.usage.cost"),
								value: formatCost(s.cost),
							},
							{
								key: "total",
								label: t("report.usage.totalTokens"),
								value: formatCount(s.totalTokens),
							},
							{
								key: "input",
								label: t("report.usage.input"),
								value: formatCount(s.input),
							},
							{
								key: "output",
								label: t("report.usage.output"),
								value: formatCount(s.output),
							},
							{
								key: "cacheRead",
								label: t("report.usage.cacheRead"),
								value: formatCount(s.cacheRead),
							},
							{
								key: "cacheWrite",
								label: t("report.usage.cacheWrite"),
								value: formatCount(s.cacheWrite),
							},
							{
								key: "orchestration",
								label: t("report.usage.orchestration"),
								value: formatCount(s.orchestrationTokens),
							},
							{
								key: "premium",
								label: t("report.usage.premium"),
								value: formatCount(s.premiumRequests),
							},
						]}
					/>
				</ReportCard>
			</ReportSection>
			<ResetCredits threadId={threadId} />
		</div>
	);
}
