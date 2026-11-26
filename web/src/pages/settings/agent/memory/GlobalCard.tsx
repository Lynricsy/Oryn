/**
 * 「全局记忆」分组（R6、R20）：所有项目与无项目线程都能召回的记忆；新建、改写、删除。
 * 每次新建或改写后，控制面自动刷新全局的 user-preferences mental model。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemoryDocument,
	useCreateGlobalMemory,
	useDeleteMemoryDocument,
	useMemoryDocuments,
	useUpdateGlobalMemory,
} from "../../../../api/memory-queries";
import {
	EmptyPanel,
	GroupNote,
} from "../../../../components/settings/agent-config-ui";
import { ConfirmButton } from "../../../../components/share/ConfirmButton";
import {
	Button,
	Spinner,
	TextArea,
} from "../../../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupPanel,
} from "../../../../components/ui/workbench";
import { formatRelative } from "../../../../lib/format";
import { MemoryError } from "./errors";
import { ItemMeta, ItemRow, Pager } from "./rows";
import { secToMs } from "./shared";

/** 与 `MemoryContent.content` 的上限一致。 */
export const MEMORY_CONTENT_MAX = 4000;
const PAGE = 30;

export function GlobalCard(): ReactNode {
	const { t } = useTranslation();
	const [offset, setOffset] = useState(0);
	const q = useMemoryDocuments({ global: true }, offset, PAGE);
	const [adding, setAdding] = useState(false);
	const docs = q.data?.items ?? [];
	return (
		<Group
			title={t("memoryPage.global.title")}
			action={
				!adding && (
					<Button size="sm" icon="plus" onClick={() => setAdding(true)}>
						{t("memoryPage.global.add")}
					</Button>
				)
			}
		>
			{adding && <NewGlobal onDone={() => setAdding(false)} />}
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadDocuments" error={q.error} />
			) : docs.length === 0 ? (
				<EmptyPanel>{t("memoryPage.global.empty")}</EmptyPanel>
			) : (
				<GroupList>
					{docs.map((d) => (
						<GlobalRow key={d.id} doc={d} />
					))}
				</GroupList>
			)}
			<Pager
				offset={offset}
				page={PAGE}
				hasMore={q.data?.hasMore}
				onOffset={setOffset}
			/>
			<GroupNote>{t("memoryPage.global.hint")}</GroupNote>
		</Group>
	);
}

/** 内容编辑区：新建、改写全局记忆与改写候选、明确记忆共用。 */
export function ContentEditor({
	initial,
	label,
	saveLabel,
	busy,
	onSave,
	onCancel,
}: {
	initial: string;
	label: string;
	saveLabel: string;
	busy: boolean;
	onSave: (content: string) => void;
	onCancel: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const [text, setText] = useState(initial);
	const content = text.trim();
	return (
		<div className="flex flex-col gap-2">
			<TextArea
				mono={false}
				aria-label={label}
				value={text}
				maxLength={MEMORY_CONTENT_MAX}
				rows={4}
				onChange={(e) => setText(e.target.value)}
			/>
			<div className="flex flex-wrap items-center justify-end gap-2">
				<span className="mr-auto text-faint text-xs tabular-nums">
					{text.length} / {MEMORY_CONTENT_MAX}
				</span>
				<Button variant="ghost" size="sm" onClick={onCancel}>
					{t("common.cancel")}
				</Button>
				<Button
					variant="primary"
					size="sm"
					disabled={!content || busy}
					onClick={() => onSave(content)}
				>
					{saveLabel}
				</Button>
			</div>
		</div>
	);
}

function NewGlobal({ onDone }: { onDone: () => void }): ReactNode {
	const { t } = useTranslation();
	const create = useCreateGlobalMemory();
	return (
		<GroupPanel>
			<ContentEditor
				initial=""
				label={t("memoryPage.global.newLabel")}
				saveLabel={t("common.create")}
				busy={create.isPending}
				onSave={(content) => create.mutate(content, { onSuccess: onDone })}
				onCancel={onDone}
			/>
			<MemoryError step="createGlobal" error={create.error} />
		</GroupPanel>
	);
}

function GlobalRow({ doc }: { doc: MemoryDocument }): ReactNode {
	const { t } = useTranslation();
	const update = useUpdateGlobalMemory();
	const del = useDeleteMemoryDocument();
	const [editing, setEditing] = useState(false);
	const content = doc.content ?? doc.preview;
	return (
		<ItemRow data-document={doc.id}>
			{editing ? (
				<ContentEditor
					initial={content}
					label={t("memoryPage.global.editLabel")}
					saveLabel={t("common.save")}
					busy={update.isPending}
					onSave={(next) =>
						update.mutate(
							{ documentId: doc.id, content: next },
							{ onSuccess: () => setEditing(false) },
						)
					}
					onCancel={() => setEditing(false)}
				/>
			) : (
				<>
					<p className="whitespace-pre-wrap break-words text-fg text-sm">
						{content}
					</p>
					<ItemMeta
						actions={
							<>
								<Button
									size="sm"
									variant="ghost"
									onClick={() => setEditing(true)}
								>
									{t("common.edit")}
								</Button>
								<ConfirmButton
									label={t("common.delete")}
									confirmLabel={t("memoryPage.browse.deleteConfirm")}
									disabled={del.isPending}
									onConfirm={() => del.mutate(doc.id)}
								/>
							</>
						}
					>
						<span>
							{t("memoryPage.browse.updated", {
								when: formatRelative(secToMs(doc.updatedAt)),
							})}
						</span>
					</ItemMeta>
				</>
			)}
			<MemoryError step="updateGlobal" error={update.error} />
			<MemoryError step="deleteDocument" error={del.error} />
		</ItemRow>
	);
}
