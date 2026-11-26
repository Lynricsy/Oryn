/**
 * 「助手记忆状态」分组（M17 R19）：运行状态（不可用 / 已关闭 / 正常 / 出错及原因）、回填进度、写入失败的来源数、
 * 待确认的明确记忆数、bank `oryn-owner` 的记忆数与 document 数、最近整理时间、最近 24 小时 / 7 天的 LLM 用量，
 * 一项一行；「注入预览」打开对话框。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../../../api/client";
import {
	type OwnerMemoryState,
	type OwnerMemoryStatus,
	useOwnerMemoryStatus,
} from "../../../../api/owner-memory-queries";
import type { Tone } from "../../../../components/ui/data";
import {
	Button,
	IconButton,
	Spinner,
} from "../../../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupRow,
	StatusText,
} from "../../../../components/ui/workbench";
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
		<Group
			title={t("memoryPage.owner.status.title")}
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
			) : (
				<StatusBody status={q.data} />
			)}
			<OwnerBriefDialog open={briefOpen} onClose={() => setBriefOpen(false)} />
		</Group>
	);
}

/** 行尾的数值：次要灰、等宽数字。 */
function Value({ children }: { children: ReactNode }): ReactNode {
	return (
		<span className="text-[12.5px] text-muted tabular-nums">{children}</span>
	);
}

function StatusBody({ status: s }: { status: OwnerMemoryStatus }): ReactNode {
	const { t } = useTranslation();
	const count = (n: number | undefined) =>
		n === undefined ? "—" : formatCount(n);
	return (
		<div data-owner-memory-state={s.state} className="contents">
			<GroupList>
				<GroupRow
					icon="memory"
					title={t("memoryPage.owner.status.state")}
					meta={
						s.state !== "ok" && (
							<span className="break-words">
								{t(`memoryPage.owner.state.${s.state}.action`)}
								{s.error &&
									` ${t("memoryPage.status.detail", { detail: s.error })}`}
							</span>
						)
					}
					trailing={
						<StatusText tone={STATE_TONE[s.state]}>
							{t(`memoryPage.owner.state.${s.state}.label`)}
						</StatusText>
					}
				/>
				<GroupRow
					title={t("memoryPage.owner.status.backfill")}
					meta={
						s.backfillSince !== undefined &&
						t("memoryPage.owner.status.backfillSince", {
							when: formatDateTime(s.backfillSince),
						})
					}
					trailing={
						<Value>
							{t("memoryPage.owner.status.backfillProgress", {
								done: s.backfillDone,
								total: s.backfillTotal,
							})}
						</Value>
					}
				/>
				<GroupRow
					title={t("memoryPage.owner.status.failingSources")}
					trailing={<Value>{s.failingSources}</Value>}
				/>
				<GroupRow
					title={t("memoryPage.owner.status.pendingExplicit")}
					trailing={<Value>{s.pendingExplicit}</Value>}
				/>
				<GroupRow
					title={t("memoryPage.owner.status.bank")}
					trailing={
						<Value>
							{t("memoryPage.owner.status.bankCounts", {
								memories: count(s.memoryCount),
								documents: count(s.documentCount),
							})}
						</Value>
					}
				/>
				<GroupRow
					title={t("memoryPage.owner.status.lastCurated")}
					trailing={
						<Value>
							{s.lastCuratedAt === undefined
								? "—"
								: formatRelative(s.lastCuratedAt)}
						</Value>
					}
				/>
				<GroupRow
					title={t("memoryPage.owner.status.usageDay")}
					trailing={<Usage counts={s.usageDay} />}
				/>
				<GroupRow
					title={t("memoryPage.owner.status.usageWeek")}
					trailing={<Usage counts={s.usageWeek} />}
				/>
			</GroupList>
		</div>
	);
}

function Usage({
	counts,
}: {
	counts: Schemas["MemoryUsageCounts"] | undefined;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<Value>
			{counts
				? t("memoryPage.owner.status.usage", {
						calls: counts.calls,
						input: formatTokens(counts.inputTokens),
						output: formatTokens(counts.outputTokens),
					})
				: "—"}
		</Value>
	);
}
