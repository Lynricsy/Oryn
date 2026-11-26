/**
 * 「待批准」分组（Q4、R6、R8、R20、T7）：agent 提议的全局记忆和无项目线程写入的记忆先进这里，不进 Hindsight；
 * 可以直接批准、改写后批准，或拒绝（直接删除）。批准之后立即成为全局记忆。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	type MemoryCandidate,
	useApproveMemoryCandidate,
	useMemoryCandidates,
	useRejectMemoryCandidate,
} from "../../../../api/memory-queries";
import {
	EmptyPanel,
	GroupNote,
} from "../../../../components/settings/agent-config-ui";
import { ConfirmButton } from "../../../../components/share/ConfirmButton";
import { Button, Spinner } from "../../../../components/ui/primitives";
import { Group, GroupList } from "../../../../components/ui/workbench";
import { formatRelative } from "../../../../lib/format";
import { parseTitleCard } from "../../../../lib/title";
import { MemoryError } from "./errors";
import { ContentEditor } from "./GlobalCard";
import { ItemMeta, ItemRow } from "./rows";
import { secToMs } from "./shared";

export function CandidatesCard(): ReactNode {
	const { t } = useTranslation();
	const q = useMemoryCandidates();
	const items = q.data?.items ?? [];
	return (
		<Group
			title={t("memoryPage.candidates.title")}
			meta={q.data ? items.length : undefined}
		>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadCandidates" error={q.error} />
			) : items.length === 0 ? (
				<EmptyPanel>{t("memoryPage.candidates.empty")}</EmptyPanel>
			) : (
				<>
					<GroupList>
						{items.map((c) => (
							<CandidateRow key={c.id} candidate={c} />
						))}
					</GroupList>
					<GroupNote>{t("memoryPage.candidates.hint")}</GroupNote>
				</>
			)}
		</Group>
	);
}

function CandidateRow({
	candidate: c,
}: {
	candidate: MemoryCandidate;
}): ReactNode {
	const { t } = useTranslation();
	const approve = useApproveMemoryCandidate();
	const reject = useRejectMemoryCandidate();
	const [editing, setEditing] = useState(false);
	const busy = approve.isPending || reject.isPending;
	return (
		<ItemRow data-candidate={c.id}>
			{editing ? (
				<ContentEditor
					initial={c.content}
					label={t("memoryPage.candidates.editLabel")}
					saveLabel={t("memoryPage.candidates.approve")}
					busy={busy}
					onSave={(content) =>
						approve.mutate(
							{
								candidateId: c.id,
								...(content === c.content ? {} : { content }),
							},
							{ onSuccess: () => setEditing(false) },
						)
					}
					onCancel={() => setEditing(false)}
				/>
			) : (
				<>
					<p className="whitespace-pre-wrap break-words text-fg text-sm">
						{c.content}
					</p>
					{c.context && (
						<p className="whitespace-pre-wrap break-words text-muted text-xs">
							{t("memoryPage.candidates.context", { text: c.context })}
						</p>
					)}
					<ItemMeta
						actions={
							<>
								<Button
									size="sm"
									variant="ghost"
									disabled={busy}
									onClick={() => setEditing(true)}
								>
									{t("memoryPage.candidates.edit")}
								</Button>
								<ConfirmButton
									label={t("memoryPage.candidates.reject")}
									confirmLabel={t("memoryPage.candidates.rejectConfirm")}
									disabled={busy}
									onConfirm={() => reject.mutate(c.id)}
								/>
								<Button
									size="sm"
									variant="primary"
									disabled={busy}
									onClick={() => approve.mutate({ candidateId: c.id })}
								>
									{t("memoryPage.candidates.approve")}
								</Button>
							</>
						}
					>
						{c.sourceThreadId && (
							<Link
								to={`/t/${c.sourceThreadId}`}
								className="min-w-0 max-w-full truncate hover:text-fg hover:underline"
								title={c.sourceThreadTitle}
							>
								{c.sourceThreadTitle
									? parseTitleCard(c.sourceThreadTitle).text ||
										t("thread.untitled")
									: t("memoryPage.browse.thread")}
							</Link>
						)}
						<span>
							{t("memoryPage.candidates.proposed", {
								when: formatRelative(secToMs(c.createdAt)),
							})}
						</span>
					</ItemMeta>
				</>
			)}
			<MemoryError step="approve" error={approve.error} />
			<MemoryError step="reject" error={reject.error} />
		</ItemRow>
	);
}
