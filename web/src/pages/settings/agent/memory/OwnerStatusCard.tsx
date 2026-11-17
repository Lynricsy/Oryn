/**
 * 记忆页「助手记忆 › 状态」卡（M17 R19）：运行状态（不可用 / 已关闭 / 正常 / 出错及原因）、回填进度、写入失败的来源数、
 * 待确认的明确记忆数、bank `oryn-owner` 的记忆数与 document 数、最近 24 小时 / 7 天的 LLM 用量；「注入预览」打开对话框。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../../../api/client";
import {
	type OwnerMemoryState,
	type OwnerMemoryStatus,
	useOwnerMemoryStatus,
} from "../../../../api/owner-memory-queries";
import { CountPill } from "../../../../components/layout/pane-parts";
import {
	Badge,
	Callout,
	Card,
	KeyValue,
	type Tone,
} from "../../../../components/ui/data";
import {
	Button,
	IconButton,
	Spinner,
} from "../../../../components/ui/primitives";
import {
	formatCount,
	formatDateTime,
	formatRelative,
	formatTokens,
} from "../../../../lib/format";
import { MemoryError } from "./errors";
import { OwnerBriefDialog } from "./OwnerBriefDialog";

const STATE_TONE: Record<OwnerMemoryState, Tone> = {
	ok: "ok",
	disabled: "neutral",
	unavailable: "neutral",
	error: "err",
};

export function OwnerStatusCard(): ReactNode {
	const { t } = useTranslation();
	const q = useOwnerMemoryStatus();
	const [briefOpen, setBriefOpen] = useState(false);
	return (
		<Card
			title={t("memoryPage.owner.status.title")}
			icon="database"
			action={
				<span className="flex items-center gap-1">
					<Button size="sm" icon="eye" onClick={() => setBriefOpen(true)}>
						{t("memoryPage.owner.brief.open")}
					</Button>
					<IconButton
						icon="refresh"
						box="sm"
						size={14}
						label={t("memoryPage.status.recheck")}
						disabled={q.isFetching}
						onClick={() => void q.refetch()}
					/>
				</span>
			}
		>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="ownerLoadStatus" error={q.error} />
			) : q.data ? (
				<StatusBody status={q.data} />
			) : null}
			<OwnerBriefDialog open={briefOpen} onClose={() => setBriefOpen(false)} />
		</Card>
	);
}

function StatusBody({ status: s }: { status: OwnerMemoryStatus }): ReactNode {
	const { t } = useTranslation();
	const tone = STATE_TONE[s.state];
	return (
		<div className="flex flex-col gap-3" data-owner-memory-state={s.state}>
			<div className="flex flex-wrap items-center gap-2">
				<Badge tone={tone}>
					{t(`memoryPage.owner.state.${s.state}.label`)}
				</Badge>
			</div>
			{s.state !== "ok" && (
				<Callout tone={tone === "ok" ? "neutral" : tone}>
					<p>{t(`memoryPage.owner.state.${s.state}.action`)}</p>
					{s.error && (
						<p className="mt-1 break-words">
							{t("memoryPage.status.detail", { detail: s.error })}
						</p>
					)}
				</Callout>
			)}
			<KeyValue
				rows={[
					{
						key: "backfill",
						label: t("memoryPage.owner.status.backfill"),
						value: (
							<span className="flex flex-wrap items-center gap-1.5">
								<span className="tabular-nums">
									{t("memoryPage.owner.status.backfillProgress", {
										done: s.backfillDone,
										total: s.backfillTotal,
									})}
								</span>
								{s.backfillSince !== undefined && (
									<span className="text-faint text-xs">
										{t("memoryPage.owner.status.backfillSince", {
											when: formatDateTime(s.backfillSince),
										})}
									</span>
								)}
							</span>
						),
					},
					{
						key: "failing",
						label: t("memoryPage.owner.status.failingSources"),
						value: <CountPill n={s.failingSources} />,
					},
					{
						key: "pending",
						label: t("memoryPage.owner.status.pendingExplicit"),
						value: <CountPill n={s.pendingExplicit} />,
					},
					{
						key: "memories",
						label: t("memoryPage.owner.status.bank"),
						value: (
							<span className="tabular-nums">
								{t("memoryPage.owner.status.bankCounts", {
									memories:
										s.memoryCount === undefined
											? "—"
											: formatCount(s.memoryCount),
									documents:
										s.documentCount === undefined
											? "—"
											: formatCount(s.documentCount),
								})}
							</span>
						),
					},
					{
						key: "curated",
						label: t("memoryPage.owner.status.lastCurated"),
						value:
							s.lastCuratedAt === undefined
								? "—"
								: formatRelative(s.lastCuratedAt),
					},
					{
						key: "day",
						label: t("memoryPage.owner.status.usageDay"),
						value: <Usage counts={s.usageDay} />,
					},
					{
						key: "week",
						label: t("memoryPage.owner.status.usageWeek"),
						value: <Usage counts={s.usageWeek} />,
					},
				]}
			/>
		</div>
	);
}

function Usage({
	counts,
}: {
	counts: Schemas["MemoryUsageCounts"] | undefined;
}): ReactNode {
	const { t } = useTranslation();
	if (!counts) return "—";
	return (
		<span className="tabular-nums">
			{t("memoryPage.owner.status.usage", {
				calls: counts.calls,
				input: formatTokens(counts.inputTokens),
				output: formatTokens(counts.outputTokens),
			})}
		</span>
	);
}
