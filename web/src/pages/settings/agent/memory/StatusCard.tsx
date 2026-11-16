/**
 * 记忆页「状态」卡（R1、R23）：Hindsight 的状态徽标与每种失败状态下一步怎么做、版本与已验证小版本不符的警告、
 * bank `oryn` 的记忆数、待批准候选与删除重试队列的计数；重试队列非空时列出失败的条目。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemoryState,
	type MemoryStatus,
	useMemoryPendingDeletes,
	useMemoryStatus,
} from "../../../../api/memory-queries";
import { CountPill } from "../../../../components/layout/pane-parts";
import {
	Badge,
	Callout,
	Card,
	KeyValue,
	type Tone,
} from "../../../../components/ui/data";
import { IconButton, Spinner } from "../../../../components/ui/primitives";
import { formatCount, formatRelative } from "../../../../lib/format";
import { MemoryError } from "./errors";
import { MEMORY_DEPLOY_DOC_URL, secToMs } from "./shared";

const STATE_TONE: Record<MemoryState, Tone> = {
	ok: "ok",
	disabled: "neutral",
	unconfigured: "neutral",
	no_keys: "warn",
	not_ready: "warn",
	unreachable: "err",
	unauthorized: "err",
	suspect_empty: "err",
};

export function StatusCard(): ReactNode {
	const { t } = useTranslation();
	const q = useMemoryStatus();
	const s = q.data;
	return (
		<Card
			title={t("memoryPage.status.title")}
			icon="database"
			action={
				<IconButton
					icon="refresh"
					box="sm"
					size={14}
					label={t("memoryPage.status.recheck")}
					disabled={q.isFetching}
					onClick={() => void q.refetch()}
				/>
			}
		>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadStatus" error={q.error} />
			) : s ? (
				<StatusBody status={s} />
			) : null}
		</Card>
	);
}

function StatusBody({ status: s }: { status: MemoryStatus }): ReactNode {
	const { t } = useTranslation();
	const tone = STATE_TONE[s.state];
	return (
		<div className="flex flex-col gap-3" data-memory-state={s.state}>
			<div className="flex flex-wrap items-center gap-2">
				<Badge tone={tone}>{t(`memoryPage.state.${s.state}.label`)}</Badge>
				<span className="text-faint text-xs">
					{t("memoryPage.status.checkedAt", {
						when: formatRelative(secToMs(s.checkedAt)),
					})}
				</span>
			</div>
			{s.state !== "ok" && (
				<Callout
					tone={tone === "ok" ? "neutral" : tone}
					title={t(`memoryPage.state.${s.state}.title`)}
				>
					<p>{t(`memoryPage.state.${s.state}.action`)}</p>
					{s.error && (
						<p className="mt-1 break-words">
							{t("memoryPage.status.detail", { detail: s.error })}
						</p>
					)}
					{s.state === "suspect_empty" && (
						<a
							href={MEMORY_DEPLOY_DOC_URL}
							target="_blank"
							rel="noopener"
							className="mt-1 inline-block text-accent-ink underline"
						>
							{t("memoryPage.status.deployDoc")}
						</a>
					)}
				</Callout>
			)}
			{s.versionMismatch && (
				<Callout tone="warn" title={t("memoryPage.status.versionMismatch")}>
					{t("memoryPage.status.versionMismatchHint", {
						version: s.version ?? "?",
						tested: s.testedMinor,
					})}
				</Callout>
			)}
			<KeyValue
				rows={[
					{
						key: "version",
						label: t("memoryPage.status.version"),
						value: (
							<span className="flex flex-wrap items-center gap-1.5">
								<span className="font-mono">{s.version ?? "—"}</span>
								<span className="text-faint text-xs">
									{t("memoryPage.status.tested", { tested: s.testedMinor })}
								</span>
							</span>
						),
					},
					{
						key: "count",
						label: t("memoryPage.status.memoryCount"),
						value: (
							<span className="tabular-nums">
								{s.memoryCount === undefined ? "—" : formatCount(s.memoryCount)}
							</span>
						),
					},
					{
						key: "candidates",
						label: t("memoryPage.status.candidates"),
						value: <CountPill n={s.candidates} />,
					},
					{
						key: "pending",
						label: t("memoryPage.status.pendingDeletes"),
						value: <CountPill n={s.pendingDeletes} />,
					},
				]}
			/>
			{s.pendingDeletes > 0 && <PendingDeletes />}
		</div>
	);
}

/** 删除失败、等待重试的 document（项目删除、按线程遗忘、单个删除失败都会进这里）。 */
function PendingDeletes(): ReactNode {
	const { t } = useTranslation();
	const q = useMemoryPendingDeletes(true);
	if (q.isPending) return <Spinner />;
	if (q.error) return <MemoryError step="loadPendingDeletes" error={q.error} />;
	const items = q.data?.items ?? [];
	if (items.length === 0) return null;
	return (
		<div className="flex flex-col gap-1.5">
			<p className="text-muted text-xs">{t("memoryPage.status.pendingHint")}</p>
			<ul className="flex flex-col gap-1">
				{items.map((d) => (
					<li
						key={d.documentId}
						className="flex min-w-0 flex-col gap-0.5 rounded-md bg-inset px-2.5 py-1.5 text-xs"
					>
						<code className="min-w-0 truncate font-mono" title={d.documentId}>
							{d.documentId}
						</code>
						<span className="text-faint">
							{t("memoryPage.status.pendingMeta", {
								n: d.attempts,
								when: formatRelative(secToMs(d.nextAttemptAt)),
							})}
						</span>
						{d.lastError && (
							<span className="break-words text-muted">
								{t("memoryPage.status.detail", { detail: d.lastError })}
							</span>
						)}
					</li>
				))}
			</ul>
		</div>
	);
}
