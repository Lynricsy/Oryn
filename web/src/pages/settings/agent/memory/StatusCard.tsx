/**
 * 「状态」分组（R1、R23）：Hindsight 的状态与每种失败状态下一步怎么做、版本与已验证小版本不符的警告、
 * bank `oryn` 的记忆数、待批准候选与删除重试队列的计数，一项一行；重试队列非空时列出失败的条目。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemoryState,
	type MemoryStatus,
	useMemoryPendingDeletes,
	useMemoryStatus,
} from "../../../../api/memory-queries";
import { GroupNote } from "../../../../components/settings/agent-config-ui";
import { Callout, type Tone } from "../../../../components/ui/data";
import { IconButton, Spinner } from "../../../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupRow,
	StatusText,
} from "../../../../components/ui/workbench";
import { formatCount, formatRelative } from "../../../../lib/format";
import { MemoryError } from "./errors";
import { ItemMeta, ItemRow } from "./rows";
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
	return (
		<Group
			title={t("memoryPage.status.title")}
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
			) : (
				<StatusBody status={q.data} />
			)}
		</Group>
	);
}

/** 行尾的数值：次要灰、等宽数字。 */
function Value({ children }: { children: ReactNode }): ReactNode {
	return (
		<span className="text-[12.5px] text-muted tabular-nums">{children}</span>
	);
}

function StatusBody({ status: s }: { status: MemoryStatus }): ReactNode {
	const { t } = useTranslation();
	const tone = STATE_TONE[s.state];
	return (
		<div data-memory-state={s.state} className="contents">
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
			<GroupList>
				<GroupRow
					icon="database"
					title="Hindsight"
					meta={t("memoryPage.status.checkedAt", {
						when: formatRelative(secToMs(s.checkedAt)),
					})}
					trailing={
						<StatusText tone={tone}>
							{t(`memoryPage.state.${s.state}.label`)}
						</StatusText>
					}
				/>
				<GroupRow
					title={t("memoryPage.status.version")}
					meta={t("memoryPage.status.tested", { tested: s.testedMinor })}
					trailing={
						<span className="font-mono text-[12.5px] text-muted">
							{s.version ?? "—"}
						</span>
					}
				/>
				<GroupRow
					title={t("memoryPage.status.memoryCount")}
					trailing={
						<Value>
							{s.memoryCount === undefined ? "—" : formatCount(s.memoryCount)}
						</Value>
					}
				/>
				<GroupRow
					title={t("memoryPage.status.candidates")}
					trailing={<Value>{s.candidates}</Value>}
				/>
				<GroupRow
					title={t("memoryPage.status.pendingDeletes")}
					trailing={<Value>{s.pendingDeletes}</Value>}
				/>
			</GroupList>
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
		<>
			<GroupNote>{t("memoryPage.status.pendingHint")}</GroupNote>
			<GroupList>
				{items.map((d) => (
					<ItemRow key={d.documentId}>
						<code
							className="min-w-0 truncate font-mono text-fg text-xs"
							title={d.documentId}
						>
							{d.documentId}
						</code>
						<ItemMeta>
							<span>
								{t("memoryPage.status.pendingMeta", {
									n: d.attempts,
									when: formatRelative(secToMs(d.nextAttemptAt)),
								})}
							</span>
							{d.lastError && (
								<span className="break-words">
									{t("memoryPage.status.detail", { detail: d.lastError })}
								</span>
							)}
						</ItemMeta>
					</ItemRow>
				))}
			</GroupList>
		</>
	);
}
