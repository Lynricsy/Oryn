/**
 * 「来源」与「排除项」两组（M17 R21）。
 * 来源：已写入（或写入失败）的对话与线程，按最近更新倒序分页；每条可以按来源遗忘（删除它写入的全部 document，
 * 受影响的主题在刷新前不进入注入；之后有新内容仍会再写入，除非设了排除）。
 * 排除项：不进助手记忆的对话与项目，可取消排除；可以从项目列表里选一个项目排除（已写入的随之遗忘）。
 * 单个对话的排除在助手对话的菜单里切换。
 */
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Schemas } from "../../../../api/client";
import {
	type OwnerMemoryExclusion,
	type OwnerMemorySource,
	useDeleteOwnerMemoryExclusion,
	useForgetOwnerMemorySource,
	useOwnerMemoryExclusions,
	useOwnerMemorySources,
	usePutOwnerMemoryExclusion,
} from "../../../../api/owner-memory-queries";
import { useProjects } from "../../../../api/queries";
import {
	EmptyPanel,
	GroupNote,
} from "../../../../components/settings/agent-config-ui";
import { Badge, Callout } from "../../../../components/ui/data";
import {
	Button,
	Dialog,
	Select,
	Spinner,
} from "../../../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupRow,
} from "../../../../components/ui/workbench";
import { formatRelative } from "../../../../lib/format";
import { parseTitleCard } from "../../../../lib/title";
import { MemoryError } from "./errors";
import { ItemMeta, ItemRow, Pager } from "./rows";
import { threadHref } from "./shared";

const PAGE = 20;

export function OwnerSourcesCard(): ReactNode {
	const { t } = useTranslation();
	const [offset, setOffset] = useState(0);
	const q = useOwnerMemorySources(offset, PAGE);
	const forget = useForgetOwnerMemorySource();
	const [target, setTarget] = useState<OwnerMemorySource | null>(null);
	const [result, setResult] = useState<Schemas["MemoryForgetResult"] | null>(
		null,
	);
	const items = q.data?.items ?? [];
	return (
		<Group title={t("memoryPage.owner.sources.title")}>
			{result && (
				<Callout tone={result.pending > 0 ? "warn" : "ok"} role="status">
					{t("memoryPage.owner.sources.forgotten", { n: result.deleted })}
					{result.pending > 0 &&
						` ${t("memoryPage.browse.forgetPending", { n: result.pending })}`}
				</Callout>
			)}
			<MemoryError step="ownerForgetSource" error={forget.error} />
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="ownerLoadSources" error={q.error} />
			) : items.length === 0 ? (
				<EmptyPanel>
					{offset === 0
						? t("memoryPage.owner.sources.empty")
						: t("memoryPage.browse.pageEmpty")}
				</EmptyPanel>
			) : (
				<GroupList>
					{items.map((s) => (
						<SourceRow
							key={s.threadId}
							source={s}
							busy={forget.isPending}
							onForget={() => {
								forget.reset();
								setResult(null);
								setTarget(s);
							}}
						/>
					))}
				</GroupList>
			)}
			<Pager
				offset={offset}
				page={PAGE}
				hasMore={q.data?.hasMore}
				onOffset={setOffset}
			/>
			<GroupNote>{t("memoryPage.owner.sources.hint")}</GroupNote>
			<Dialog
				open={target !== null}
				onClose={() => setTarget(null)}
				title={t("memoryPage.owner.sources.forgetTitle")}
				footer={
					<>
						<Button variant="ghost" onClick={() => setTarget(null)}>
							{t("common.cancel")}
						</Button>
						<Button
							variant="danger"
							disabled={forget.isPending}
							onClick={() => {
								if (!target) return;
								forget.mutate(target.threadId, {
									onSuccess: (r) => setResult(r),
									onSettled: () => setTarget(null),
								});
							}}
						>
							{t("memoryPage.owner.sources.forgetConfirm")}
						</Button>
					</>
				}
			>
				<p className="text-sm">
					{t("memoryPage.owner.sources.forgetBody", {
						name: target ? sourceName(target, t) : "",
						n: target?.chunks ?? 0,
					})}
				</p>
			</Dialog>
		</Group>
	);
}

/** 来源的显示名：线程已删除、无标题与有标题三种。 */
function sourceName(s: OwnerMemorySource, t: TFunction): string {
	if (s.missing) return t("memoryPage.owner.sources.missing");
	return s.threadTitle
		? parseTitleCard(s.threadTitle).text || t("thread.untitled")
		: t("memoryPage.browse.thread");
}

function SourceRow({
	source: s,
	busy,
	onForget,
}: {
	source: OwnerMemorySource;
	busy: boolean;
	onForget: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const name = sourceName(s, t);
	return (
		<ItemRow data-source={s.threadId}>
			<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
				{s.missing ? (
					<span className="min-w-0 truncate text-muted text-sm">{name}</span>
				) : (
					<Link
						to={threadHref(s.threadId, s.threadKind)}
						className="min-w-0 max-w-full truncate font-[550] text-fg text-sm hover:underline"
						title={s.threadTitle}
					>
						{name}
					</Link>
				)}
				<Badge>{t(`memoryPage.owner.sources.kind.${s.kind}`)}</Badge>
				{s.attempts > 0 && s.lastError && (
					<Badge tone="err">
						{t("memoryPage.owner.sources.failed", { n: s.attempts })}
					</Badge>
				)}
			</div>
			<ItemMeta
				actions={
					<Button
						size="sm"
						variant="ghost"
						disabled={busy || s.chunks === 0}
						onClick={onForget}
					>
						{t("memoryPage.owner.sources.forget")}
					</Button>
				}
			>
				<span className="tabular-nums">
					{t("memoryPage.owner.sources.chunks", { n: s.chunks })}
				</span>
				<span>
					{s.retainedAt === undefined
						? t("memoryPage.owner.sources.notRetained")
						: t("memoryPage.owner.sources.retainedAt", {
								when: formatRelative(s.retainedAt),
							})}
				</span>
				{s.nextAttemptAt !== undefined && s.lastError && (
					<span>
						{t("memoryPage.owner.sources.nextAttempt", {
							when: formatRelative(s.nextAttemptAt),
						})}
					</span>
				)}
			</ItemMeta>
			{s.lastError && (
				<p className="break-words text-muted text-xs">
					{t("memoryPage.status.detail", { detail: s.lastError })}
				</p>
			)}
		</ItemRow>
	);
}

export function OwnerExclusionsCard(): ReactNode {
	const { t } = useTranslation();
	const q = useOwnerMemoryExclusions();
	const projects = useProjects();
	const put = usePutOwnerMemoryExclusion();
	const [projectId, setProjectId] = useState("");
	const items = q.data?.items ?? [];
	const excluded = new Set(
		items.filter((e) => e.kind === "project").map((e) => e.targetId),
	);
	const choices = (projects.data?.projects ?? []).filter(
		(p) => !p.archivedAt && !excluded.has(p.id),
	);
	return (
		<Group title={t("memoryPage.owner.exclusions.title")}>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="ownerLoadExclusions" error={q.error} />
			) : items.length === 0 ? (
				<EmptyPanel>{t("memoryPage.owner.exclusions.empty")}</EmptyPanel>
			) : (
				<GroupList>
					{items.map((e) => (
						<ExclusionRow key={`${e.kind}:${e.targetId}`} exclusion={e} />
					))}
				</GroupList>
			)}
			<form
				className="flex flex-wrap gap-2"
				onSubmit={(e) => {
					e.preventDefault();
					if (!projectId) return;
					put.mutate(
						{ kind: "project", targetId: projectId },
						{ onSuccess: () => setProjectId("") },
					);
				}}
			>
				<Select
					aria-label={t("memoryPage.owner.exclusions.project")}
					value={projectId}
					placeholder={t("memoryPage.owner.exclusions.pickProject")}
					className="w-full sm:w-56"
					disabled={projects.isPending}
					options={choices.map((p) => ({ value: p.id, label: p.name }))}
					onChange={setProjectId}
				/>
				<Button
					type="submit"
					icon="minusCircle"
					disabled={!projectId || put.isPending}
				>
					{t("memoryPage.owner.exclusions.excludeProject")}
				</Button>
			</form>
			<MemoryError step="ownerSaveExclusion" error={put.error} />
			<GroupNote>{t("memoryPage.owner.exclusions.hint")}</GroupNote>
		</Group>
	);
}

function ExclusionRow({
	exclusion: e,
}: {
	exclusion: OwnerMemoryExclusion;
}): ReactNode {
	const { t } = useTranslation();
	const del = useDeleteOwnerMemoryExclusion();
	const name =
		e.kind === "thread" && e.name
			? parseTitleCard(e.name).text || t("thread.untitled")
			: (e.name ?? e.targetId);
	return (
		<GroupRow
			title={
				<span
					className="truncate"
					title={e.targetId}
					data-exclusion={`${e.kind}:${e.targetId}`}
				>
					{name}
				</span>
			}
			meta={
				<>
					<span>{t(`memoryPage.owner.exclusions.kind.${e.kind}`)}</span>
					<span aria-hidden className="text-faint">
						·
					</span>
					<span>{formatRelative(e.createdAt)}</span>
				</>
			}
			trailing={
				<Button
					size="sm"
					variant="ghost"
					disabled={del.isPending}
					onClick={() => del.mutate({ kind: e.kind, targetId: e.targetId })}
				>
					{t("memoryPage.owner.exclusions.remove")}
				</Button>
			}
		>
			{del.error ? (
				<MemoryError step="ownerDeleteExclusion" error={del.error} />
			) : undefined}
		</GroupRow>
	);
}
