/**
 * 记忆页「浏览」卡（R19）：选项目，按更新时间倒序分页列出 document；关键词召回搜索（含全局记忆）；
 * 删除单个 document，或按线程遗忘（删除该线程写入的全部 document）。破坏性操作两步确认。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	type MemoryDocument,
	type MemorySearchHit,
	useDeleteMemoryDocument,
	useForgetMemoryThread,
	useMemoryDocuments,
	useMemoryProjects,
	useMemorySearch,
} from "../../../../api/memory-queries";
import { ConfirmButton } from "../../../../components/share/ConfirmButton";
import { Badge, Callout, Card } from "../../../../components/ui/data";
import {
	Button,
	EmptyState,
	Select,
	Spinner,
	TextInput,
} from "../../../../components/ui/primitives";
import { formatRelative } from "../../../../lib/format";
import { parseTitleCard } from "../../../../lib/title";
import { MemoryError } from "./errors";
import { secToMs } from "./shared";

const PAGE = 20;

export function BrowseCard(): ReactNode {
	const { t } = useTranslation();
	const projects = useMemoryProjects();
	const items = projects.data?.items ?? [];
	const [projectId, setProjectId] = useState<string>("");
	const [offset, setOffset] = useState(0);
	const [draft, setDraft] = useState("");
	const [query, setQuery] = useState("");
	// 项目列表到了之后默认选第一个
	useEffect(() => {
		if (!projectId && items[0]) setProjectId(items[0].projectId);
	}, [projectId, items]);

	return (
		<Card title={t("memoryPage.browse.title")} icon="list">
			{projects.isPending ? (
				<Spinner />
			) : projects.error ? (
				<MemoryError step="loadProjects" error={projects.error} />
			) : items.length === 0 ? (
				<EmptyState icon="folder" title={t("memoryPage.projects.empty")} />
			) : (
				<div className="flex flex-col gap-3">
					<div className="flex flex-wrap gap-2">
						<Select
							aria-label={t("memoryPage.browse.project")}
							value={projectId}
							wrapperClassName="w-full sm:w-56"
							onChange={(e) => {
								setProjectId(e.target.value);
								setOffset(0);
							}}
						>
							{items.map((p) => (
								<option key={p.projectId} value={p.projectId}>
									{p.name}
								</option>
							))}
						</Select>
						<form
							className="flex min-w-0 flex-1 basis-60 gap-2"
							onSubmit={(e) => {
								e.preventDefault();
								setQuery(draft.trim());
							}}
						>
							<TextInput
								value={draft}
								onChange={(e) => setDraft(e.target.value)}
								maxLength={800}
								placeholder={t("memoryPage.browse.searchPlaceholder")}
								aria-label={t("memoryPage.browse.search")}
								className="min-w-0 flex-1"
							/>
							<Button type="submit" icon="search" disabled={!draft.trim()}>
								{t("memoryPage.browse.search")}
							</Button>
						</form>
					</div>
					{query && projectId ? (
						<SearchResults
							q={query}
							projectId={projectId}
							onClear={() => {
								setQuery("");
								setDraft("");
							}}
						/>
					) : projectId ? (
						<DocumentPage
							projectId={projectId}
							offset={offset}
							setOffset={setOffset}
						/>
					) : null}
				</div>
			)}
		</Card>
	);
}

function SearchResults({
	q,
	projectId,
	onClear,
}: {
	q: string;
	projectId: string;
	onClear: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const s = useMemorySearch(q, projectId);
	const hits = s.data?.items ?? [];
	return (
		<div className="flex flex-col gap-2">
			<div className="flex items-center justify-between gap-2">
				<span className="min-w-0 truncate text-muted text-xs">
					{t("memoryPage.browse.resultsFor", { q })}
				</span>
				<Button size="sm" variant="ghost" onClick={onClear}>
					{t("memoryPage.browse.clearSearch")}
				</Button>
			</div>
			{s.isPending ? (
				<Spinner />
			) : s.error ? (
				<MemoryError step="search" error={s.error} />
			) : hits.length === 0 ? (
				<p className="text-faint text-sm">{t("memoryPage.browse.noHits")}</p>
			) : (
				<ul className="flex flex-col gap-1.5">
					{hits.map((h, i) => (
						// biome-ignore lint/suspicious/noArrayIndexKey: 召回结果没有稳定标识（同一 document 可命中多条），只读列表顺序不变
						<HitRow key={`${h.documentId ?? ""}-${i}`} hit={h} />
					))}
				</ul>
			)}
		</div>
	);
}

function HitRow({ hit }: { hit: MemorySearchHit }): ReactNode {
	const { t } = useTranslation();
	return (
		<li className="flex flex-col gap-1 rounded-md bg-inset px-3 py-2">
			<p className="whitespace-pre-wrap break-words text-sm">{hit.text}</p>
			<span className="flex flex-wrap items-center gap-1.5 text-faint text-xs">
				{hit.global && (
					<Badge icon="globe">{t("memoryPage.browse.global")}</Badge>
				)}
				{hit.type && <Badge>{hit.type}</Badge>}
				{hit.mentionedAt && <span>{hit.mentionedAt}</span>}
			</span>
		</li>
	);
}

function DocumentPage({
	projectId,
	offset,
	setOffset,
}: {
	projectId: string;
	offset: number;
	setOffset: (n: number) => void;
}): ReactNode {
	const { t } = useTranslation();
	const q = useMemoryDocuments({ projectId }, offset, PAGE);
	const forget = useForgetMemoryThread();
	const docs = q.data?.items ?? [];
	if (q.isPending) return <Spinner />;
	if (q.error) return <MemoryError step="loadDocuments" error={q.error} />;
	return (
		<div className="flex flex-col gap-2">
			{forget.data && (
				<Callout tone={forget.data.pending > 0 ? "warn" : "ok"} role="status">
					{t("memoryPage.browse.forgotten", { n: forget.data.deleted })}
					{forget.data.pending > 0 &&
						` ${t("memoryPage.browse.forgetPending", { n: forget.data.pending })}`}
				</Callout>
			)}
			<MemoryError step="forget" error={forget.error} />
			{docs.length === 0 ? (
				<p className="text-faint text-sm">
					{offset === 0
						? t("memoryPage.browse.empty")
						: t("memoryPage.browse.pageEmpty")}
				</p>
			) : (
				<ul className="-mx-2 flex flex-col">
					{docs.map((d) => (
						<DocumentRow
							key={d.id}
							doc={d}
							forgetting={forget.isPending}
							onForget={(threadId) => forget.mutate(threadId)}
						/>
					))}
				</ul>
			)}
			{(offset > 0 || q.data?.hasMore) && (
				<div className="flex items-center justify-end gap-2">
					<span className="text-faint text-xs tabular-nums">
						{t("memoryPage.browse.range", {
							from: offset + 1,
							to: offset + docs.length,
						})}
					</span>
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
	);
}

function DocumentRow({
	doc,
	forgetting,
	onForget,
}: {
	doc: MemoryDocument;
	forgetting: boolean;
	onForget: (threadId: string) => void;
}): ReactNode {
	const { t } = useTranslation();
	const del = useDeleteMemoryDocument();
	const threadId = doc.threadId;
	return (
		<li
			className="flex flex-col gap-1 rounded-md px-2 py-2"
			data-document={doc.id}
		>
			<p className="line-clamp-3 whitespace-pre-wrap break-words text-sm">
				{doc.preview}
			</p>
			<div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-faint text-xs">
				{threadId && (
					<Link
						to={`/t/${threadId}`}
						className="min-w-0 max-w-full truncate hover:text-fg hover:underline"
						title={doc.threadTitle}
					>
						{doc.threadTitle
							? parseTitleCard(doc.threadTitle).text || t("thread.untitled")
							: t("memoryPage.browse.thread")}
					</Link>
				)}
				<span>
					{t("memoryPage.browse.updated", {
						when: formatRelative(secToMs(doc.updatedAt)),
					})}
				</span>
				<code className="min-w-0 max-w-full truncate font-mono" title={doc.id}>
					{doc.id}
				</code>
				<span className="ml-auto flex shrink-0 items-center gap-1">
					{threadId && (
						<ConfirmButton
							label={t("memoryPage.browse.forget")}
							confirmLabel={t("memoryPage.browse.forgetConfirm")}
							disabled={forgetting}
							onConfirm={() => onForget(threadId)}
						/>
					)}
					<ConfirmButton
						label={t("common.delete")}
						confirmLabel={t("memoryPage.browse.deleteConfirm")}
						disabled={del.isPending}
						onConfirm={() => del.mutate(doc.id)}
					/>
				</span>
			</div>
			<MemoryError step="deleteDocument" error={del.error} />
		</li>
	);
}
