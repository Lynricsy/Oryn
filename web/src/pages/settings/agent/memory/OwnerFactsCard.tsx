/**
 * 「事实」分组（M17 R20）：浏览或搜索主人记忆里的 world 事实与 observation，每条带出处
 * （主人陈述 / 含助手消息 / 明确记忆 / 来源未确认，能回溯时给出对话与消息范围并可跳过去）。搜索按召回相关度排序、不分页；
 * 不搜索时按时间倒序分页。world 事实可以设为失效（可填原因）或恢复；observation 不能直接失效，只能失效它的来源事实。
 * 失效后全部活跃主题在刷新完成前不进入注入。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	type OwnerMemoryFact,
	type OwnerMemoryFactType,
	type OwnerMemoryProvenance,
	useOwnerMemoryFacts,
	useSetOwnerMemoryFactState,
} from "../../../../api/owner-memory-queries";
import {
	EmptyPanel,
	GroupNote,
} from "../../../../components/settings/agent-config-ui";
import { Badge } from "../../../../components/ui/data";
import {
	Button,
	Select,
	Spinner,
	TextInput,
} from "../../../../components/ui/primitives";
import { Group, GroupList } from "../../../../components/ui/workbench";
import { parseTitleCard } from "../../../../lib/title";
import { MemoryError } from "./errors";
import { ItemMeta, ItemRow, Pager } from "./rows";
import { threadHref } from "./shared";

const PAGE = 20;
/** 失效原因的长度上限（只是给输入框一个边界）。 */
const REASON_MAX = 200;

/** `enabled`：助手记忆开着（关着时事实读不出来，只显示提示）。 */
export function OwnerFactsCard({ enabled }: { enabled: boolean }): ReactNode {
	const { t } = useTranslation();
	const [draft, setDraft] = useState("");
	const [query, setQuery] = useState("");
	const [type, setType] = useState<OwnerMemoryFactType | "">("");
	const [offset, setOffset] = useState(0);
	const q = useOwnerMemoryFacts(
		query,
		type || undefined,
		offset,
		PAGE,
		enabled,
	);
	const items = q.data?.items ?? [];
	return (
		<Group title={t("memoryPage.owner.facts.title")}>
			<div className="flex flex-wrap gap-2">
				<Select<OwnerMemoryFactType | "all">
					aria-label={t("memoryPage.owner.facts.type")}
					value={type || "all"}
					className="w-full sm:w-44"
					options={[
						{ value: "all", label: t("memoryPage.owner.facts.allTypes") },
						{
							value: "world",
							label: t("memoryPage.owner.facts.types.world"),
						},
						{
							value: "observation",
							label: t("memoryPage.owner.facts.types.observation"),
						},
					]}
					onChange={(v) => {
						setType(v === "all" ? "" : v);
						setOffset(0);
					}}
				/>
				<form
					className="flex min-w-0 flex-1 basis-60 gap-2"
					onSubmit={(e) => {
						e.preventDefault();
						setQuery(draft.trim());
						setOffset(0);
					}}
				>
					<TextInput
						value={draft}
						onChange={(e) => setDraft(e.target.value)}
						maxLength={800}
						placeholder={t("memoryPage.owner.facts.searchPlaceholder")}
						aria-label={t("memoryPage.owner.facts.search")}
						className="min-w-0 flex-1"
					/>
					<Button type="submit" icon="search" disabled={!draft.trim()}>
						{t("memoryPage.owner.facts.search")}
					</Button>
				</form>
			</div>
			{query && (
				<div className="flex items-center justify-between gap-2 px-1.5">
					<span className="min-w-0 truncate text-muted text-xs">
						{t("memoryPage.browse.resultsFor", { q: query })}
					</span>
					<Button
						size="sm"
						variant="ghost"
						onClick={() => {
							setQuery("");
							setDraft("");
						}}
					>
						{t("memoryPage.browse.clearSearch")}
					</Button>
				</div>
			)}
			{!enabled ? (
				<EmptyPanel>{t("memoryPage.owner.facts.disabled")}</EmptyPanel>
			) : q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="ownerLoadFacts" error={q.error} />
			) : items.length === 0 ? (
				<EmptyPanel>
					{query
						? t("memoryPage.browse.noHits")
						: offset === 0
							? t("memoryPage.owner.facts.empty")
							: t("memoryPage.browse.pageEmpty")}
				</EmptyPanel>
			) : (
				<GroupList>
					{items.map((f) => (
						<FactRow key={f.id} fact={f} />
					))}
				</GroupList>
			)}
			{!query && (
				<Pager
					offset={offset}
					page={PAGE}
					hasMore={q.data?.hasMore}
					shown={items.length}
					onOffset={setOffset}
				/>
			)}
			<GroupNote>{t("memoryPage.owner.facts.hint")}</GroupNote>
		</Group>
	);
}

function FactRow({ fact: f }: { fact: OwnerMemoryFact }): ReactNode {
	const { t } = useTranslation();
	const set = useSetOwnerMemoryFactState();
	const [invalidating, setInvalidating] = useState(false);
	const [reason, setReason] = useState("");
	const invalid = f.state === "invalidated";
	return (
		<ItemRow data-fact={f.id}>
			<p
				className={`whitespace-pre-wrap break-words text-sm ${invalid ? "text-muted line-through" : "text-fg"}`}
			>
				{f.text}
			</p>
			<ItemMeta
				actions={
					f.type === "world" &&
					!invalidating &&
					(invalid ? (
						<Button
							size="sm"
							variant="ghost"
							disabled={set.isPending}
							onClick={() =>
								set.mutate({ factId: f.id, body: { state: "valid" } })
							}
						>
							{t("memoryPage.owner.facts.restore")}
						</Button>
					) : (
						<Button
							size="sm"
							variant="ghost"
							onClick={() => setInvalidating(true)}
						>
							{t("memoryPage.owner.facts.invalidate")}
						</Button>
					))
				}
			>
				<Badge>
					{f.type === "world" || f.type === "observation"
						? t(`memoryPage.owner.facts.types.${f.type}`)
						: f.type}
				</Badge>
				{invalid && (
					<Badge tone="warn">{t("memoryPage.owner.facts.invalidated")}</Badge>
				)}
				{f.mentionedAt && <span>{f.mentionedAt}</span>}
				<Provenance p={f.provenance} />
			</ItemMeta>
			{invalidating && (
				<form
					className="flex flex-col gap-2 rounded-[14px] bg-inset p-2.5"
					onSubmit={(e) => {
						e.preventDefault();
						const why = reason.trim();
						set.mutate(
							{
								factId: f.id,
								body: why
									? { state: "invalidated", reason: why }
									: { state: "invalidated" },
							},
							{
								onSuccess: () => {
									setInvalidating(false);
									setReason("");
								},
							},
						);
					}}
				>
					<p className="text-muted text-xs">
						{t("memoryPage.owner.facts.invalidateHint")}
					</p>
					<TextInput
						value={reason}
						onChange={(e) => setReason(e.target.value)}
						maxLength={REASON_MAX}
						placeholder={t("memoryPage.owner.facts.reasonPlaceholder")}
						aria-label={t("memoryPage.owner.facts.reason")}
					/>
					<div className="flex justify-end gap-2">
						<Button
							size="sm"
							variant="ghost"
							onClick={() => {
								setInvalidating(false);
								setReason("");
							}}
						>
							{t("common.cancel")}
						</Button>
						<Button
							type="submit"
							size="sm"
							variant="danger"
							disabled={set.isPending}
						>
							{t("memoryPage.owner.facts.invalidateConfirm")}
						</Button>
					</div>
				</form>
			)}
			<MemoryError step="ownerSetFactState" error={set.error} />
		</ItemRow>
	);
}

/** 出处：类型徽标；能回溯时加上对话（可跳转）与消息范围。 */
function Provenance({ p }: { p: OwnerMemoryProvenance }): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			<Badge
				tone={p.kind === "owner" || p.kind === "explicit" ? "ok" : "warn"}
				title={t(`memoryPage.owner.facts.provenance.${p.kind}.hint`)}
			>
				{t(`memoryPage.owner.facts.provenance.${p.kind}.label`)}
			</Badge>
			{p.threadId && (
				<Link
					to={threadHref(p.threadId, p.threadKind)}
					className="min-w-0 max-w-full truncate hover:text-fg hover:underline"
					title={p.threadTitle}
				>
					{p.threadTitle
						? parseTitleCard(p.threadTitle).text || t("thread.untitled")
						: t("memoryPage.browse.thread")}
				</Link>
			)}
			{p.seqFrom !== undefined && (
				<span className="tabular-nums">
					{p.seqTo !== undefined && p.seqTo !== p.seqFrom
						? t("memoryPage.owner.facts.seqRange", {
								from: p.seqFrom,
								to: p.seqTo,
							})
						: t("memoryPage.owner.facts.seq", { seq: p.seqFrom })}
				</span>
			)}
		</>
	);
}
