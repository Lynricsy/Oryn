/**
 * 记忆页「全局记忆」卡（R6、R20）：所有项目与无项目线程都能召回的记忆；新建、改写、删除。
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
import { ConfirmButton } from "../../../../components/share/ConfirmButton";
import { Card } from "../../../../components/ui/data";
import {
	Button,
	Spinner,
	TextArea,
} from "../../../../components/ui/primitives";
import { formatRelative } from "../../../../lib/format";
import { MemoryError } from "./errors";
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
		<Card
			title={t("memoryPage.global.title")}
			icon="globe"
			action={
				!adding && (
					<Button size="sm" icon="plus" onClick={() => setAdding(true)}>
						{t("memoryPage.global.add")}
					</Button>
				)
			}
		>
			<div className="flex flex-col gap-2">
				<p className="text-faint text-xs">{t("memoryPage.global.hint")}</p>
				{adding && <NewGlobal onDone={() => setAdding(false)} />}
				{q.isPending ? (
					<Spinner />
				) : q.error ? (
					<MemoryError step="loadDocuments" error={q.error} />
				) : docs.length === 0 ? (
					<p className="text-faint text-sm">{t("memoryPage.global.empty")}</p>
				) : (
					<ul className="-mx-2 flex flex-col">
						{docs.map((d) => (
							<GlobalRow key={d.id} doc={d} />
						))}
					</ul>
				)}
				{(offset > 0 || q.data?.hasMore) && (
					<div className="flex justify-end gap-2">
						<Button
							size="sm"
							icon="chevronLeft"
							disabled={offset === 0}
							onClick={() => setOffset(Math.max(0, offset - PAGE))}
						>
							{t("memoryPage.browse.prev")}
						</Button>
						<Button
							size="sm"
							disabled={!q.data?.hasMore}
							onClick={() => setOffset(offset + PAGE)}
						>
							{t("memoryPage.browse.next")}
						</Button>
					</div>
				)}
			</div>
		</Card>
	);
}

/** 内容编辑区：新建、改写全局记忆与改写候选共用。 */
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
		<div className="flex flex-col gap-2 rounded-md border border-border p-2.5">
			<ContentEditor
				initial=""
				label={t("memoryPage.global.newLabel")}
				saveLabel={t("common.create")}
				busy={create.isPending}
				onSave={(content) => create.mutate(content, { onSuccess: onDone })}
				onCancel={onDone}
			/>
			<MemoryError step="createGlobal" error={create.error} />
		</div>
	);
}

function GlobalRow({ doc }: { doc: MemoryDocument }): ReactNode {
	const { t } = useTranslation();
	const update = useUpdateGlobalMemory();
	const del = useDeleteMemoryDocument();
	const [editing, setEditing] = useState(false);
	const content = doc.content ?? doc.preview;
	return (
		<li
			className="flex flex-col gap-1.5 rounded-md px-2 py-2"
			data-document={doc.id}
		>
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
					<p className="whitespace-pre-wrap break-words text-sm">{content}</p>
					<div className="flex flex-wrap items-center gap-2 text-faint text-xs">
						<span>
							{t("memoryPage.browse.updated", {
								when: formatRelative(secToMs(doc.updatedAt)),
							})}
						</span>
						<span className="ml-auto flex shrink-0 items-center gap-1">
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
						</span>
					</div>
				</>
			)}
			<MemoryError step="updateGlobal" error={update.error} />
			<MemoryError step="deleteDocument" error={del.error} />
		</li>
	);
}
