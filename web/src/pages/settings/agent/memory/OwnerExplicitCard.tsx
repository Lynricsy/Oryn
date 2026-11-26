/**
 * 「明确记忆」分组（M17 R22）：主人明确要求记住的内容。待确认的在前（助手调用 remember 时给出的
 * 主人原话没能在对话里核实），可以直接批准、改写后批准，或拒绝（= 删除）；已生效的可以编辑（随之重写 document）与删除。
 * 在这里新建的直接生效。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	type OwnerMemoryExplicit,
	useApproveOwnerMemoryExplicit,
	useCreateOwnerMemoryExplicit,
	useDeleteOwnerMemoryExplicit,
	useOwnerMemoryExplicit,
	useUpdateOwnerMemoryExplicit,
} from "../../../../api/owner-memory-queries";
import {
	EmptyPanel,
	GroupNote,
} from "../../../../components/settings/agent-config-ui";
import { ConfirmButton } from "../../../../components/share/ConfirmButton";
import { Badge } from "../../../../components/ui/data";
import { Button, Spinner } from "../../../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupPanel,
} from "../../../../components/ui/workbench";
import { formatRelative } from "../../../../lib/format";
import { parseTitleCard } from "../../../../lib/title";
import { MemoryError } from "./errors";
import { ContentEditor } from "./GlobalCard";
import { ItemMeta, ItemRow } from "./rows";
import { threadHref } from "./shared";

export function OwnerExplicitCard(): ReactNode {
	const { t } = useTranslation();
	const q = useOwnerMemoryExplicit();
	const [adding, setAdding] = useState(false);
	const items = q.data?.items ?? [];
	const pending = items.filter((e) => e.status === "pending").length;
	return (
		<Group
			title={t("memoryPage.owner.explicit.title")}
			meta={
				pending > 0
					? t("memoryPage.owner.explicit.pendingCount", { n: pending })
					: undefined
			}
			action={
				!adding && (
					<Button size="sm" icon="plus" onClick={() => setAdding(true)}>
						{t("memoryPage.owner.explicit.add")}
					</Button>
				)
			}
		>
			{adding && <NewExplicit onDone={() => setAdding(false)} />}
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="ownerLoadExplicit" error={q.error} />
			) : items.length === 0 ? (
				<EmptyPanel>{t("memoryPage.owner.explicit.empty")}</EmptyPanel>
			) : (
				<GroupList>
					{items.map((e) => (
						<ExplicitRow key={e.id} item={e} />
					))}
				</GroupList>
			)}
			<GroupNote>{t("memoryPage.owner.explicit.hint")}</GroupNote>
		</Group>
	);
}

function NewExplicit({ onDone }: { onDone: () => void }): ReactNode {
	const { t } = useTranslation();
	const create = useCreateOwnerMemoryExplicit();
	return (
		<GroupPanel>
			<ContentEditor
				initial=""
				label={t("memoryPage.owner.explicit.newLabel")}
				saveLabel={t("common.create")}
				busy={create.isPending}
				onSave={(content) => create.mutate(content, { onSuccess: onDone })}
				onCancel={onDone}
			/>
			<MemoryError step="ownerCreateExplicit" error={create.error} />
		</GroupPanel>
	);
}

function ExplicitRow({ item: e }: { item: OwnerMemoryExplicit }): ReactNode {
	const { t } = useTranslation();
	const approve = useApproveOwnerMemoryExplicit();
	const update = useUpdateOwnerMemoryExplicit();
	const del = useDeleteOwnerMemoryExplicit();
	const [editing, setEditing] = useState(false);
	const pending = e.status === "pending";
	const busy = approve.isPending || update.isPending || del.isPending;
	return (
		<ItemRow data-explicit={e.id} data-status={e.status}>
			{editing ? (
				<ContentEditor
					initial={e.content}
					label={t("memoryPage.owner.explicit.editLabel")}
					saveLabel={
						pending ? t("memoryPage.owner.explicit.approve") : t("common.save")
					}
					busy={busy}
					onSave={(content) =>
						pending
							? approve.mutate(
									{
										explicitId: e.id,
										...(content === e.content ? {} : { content }),
									},
									{ onSuccess: () => setEditing(false) },
								)
							: update.mutate(
									{ explicitId: e.id, content },
									{ onSuccess: () => setEditing(false) },
								)
					}
					onCancel={() => setEditing(false)}
				/>
			) : (
				<>
					<div className="flex items-start gap-2">
						{pending && (
							<Badge tone="warn">
								{t("memoryPage.owner.explicit.pending")}
							</Badge>
						)}
						<p className="min-w-0 flex-1 whitespace-pre-wrap break-words text-fg text-sm">
							{e.content}
						</p>
					</div>
					{e.ownerQuote && (
						<p className="whitespace-pre-wrap break-words text-muted text-xs">
							{t("memoryPage.owner.explicit.quote", { text: e.ownerQuote })}
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
									{t("common.edit")}
								</Button>
								{pending ? (
									<>
										<ConfirmButton
											label={t("memoryPage.owner.explicit.reject")}
											confirmLabel={t(
												"memoryPage.owner.explicit.rejectConfirm",
											)}
											disabled={busy}
											onConfirm={() => del.mutate(e.id)}
										/>
										<Button
											size="sm"
											variant="primary"
											disabled={busy}
											onClick={() => approve.mutate({ explicitId: e.id })}
										>
											{t("memoryPage.owner.explicit.approve")}
										</Button>
									</>
								) : (
									<ConfirmButton
										label={t("common.delete")}
										confirmLabel={t("memoryPage.browse.deleteConfirm")}
										disabled={busy}
										onConfirm={() => del.mutate(e.id)}
									/>
								)}
							</>
						}
					>
						{e.sourceThreadId && (
							<Link
								to={threadHref(e.sourceThreadId, "assistant")}
								className="min-w-0 max-w-full truncate hover:text-fg hover:underline"
								title={e.sourceThreadTitle}
							>
								{e.sourceThreadTitle
									? parseTitleCard(e.sourceThreadTitle).text ||
										t("thread.untitled")
									: t("memoryPage.browse.thread")}
							</Link>
						)}
						<span>
							{t("memoryPage.browse.updated", {
								when: formatRelative(e.updatedAt),
							})}
						</span>
					</ItemMeta>
				</>
			)}
			<MemoryError step="ownerApproveExplicit" error={approve.error} />
			<MemoryError step="ownerUpdateExplicit" error={update.error} />
			<MemoryError step="ownerDeleteExplicit" error={del.error} />
		</ItemRow>
	);
}
