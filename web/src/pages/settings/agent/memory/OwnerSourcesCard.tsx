/**
 * 记忆页「助手记忆 › 来源」卡（M17 R21）：已写入（或写入失败）的对话与线程，按最近更新倒序分页；每条可以按来源遗忘
 * （删除它写入的全部 document，受影响的主题在刷新前不进入注入；之后有新内容仍会再写入，除非设了排除）。
 * 下半部分是排除项：不进助手记忆的对话与项目，可取消排除；可以从项目列表里选一个项目排除（已写入的随之遗忘）。
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
import { Badge, Callout, Card } from "../../../../components/ui/data";
import {
	Button,
	Dialog,
	Select,
	Spinner,
} from "../../../../components/ui/primitives";
import { formatRelative } from "../../../../lib/format";
import { parseTitleCard } from "../../../../lib/title";
import { MemoryError } from "./errors";
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
		<Card title={t("memoryPage.owner.sources.title")} icon="chats">
			<div className="flex flex-col gap-3">
				<p className="text-faint text-xs">
					{t("memoryPage.owner.sources.hint")}
				</p>
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
					<p className="text-faint text-sm">
						{offset === 0
							? t("memoryPage.owner.sources.empty")
							: t("memoryPage.browse.pageEmpty")}
					</p>
				) : (
					<ul className="-mx-2 flex flex-col">
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
					</ul>
				)}
				{(offset > 0 || q.data?.hasMore) && (
					<div className="flex items-center justify-end gap-2">
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
				<div className="border-border border-t" />
				<Exclusions />
			</div>
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
		</Card>
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
		<li
			className="flex flex-col gap-1 rounded-md px-2 py-2"
			data-source={s.threadId}
		>
			<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
				{s.missing ? (
					<span className="min-w-0 truncate text-muted text-sm">{name}</span>
				) : (
					<Link
						to={threadHref(s.threadId, s.threadKind)}
						className="min-w-0 max-w-full truncate font-medium text-sm hover:underline"
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
				<Button
					size="sm"
					variant="ghost"
					className="ml-auto"
					disabled={busy || s.chunks === 0}
					onClick={onForget}
				>
					{t("memoryPage.owner.sources.forget")}
				</Button>
			</div>
			<div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-faint text-xs">
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
			</div>
			{s.lastError && (
				<p className="break-words text-muted text-xs">
					{t("memoryPage.status.detail", { detail: s.lastError })}
				</p>
			)}
		</li>
	);
}

function Exclusions(): ReactNode {
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
		<div className="flex flex-col gap-2">
			<h4 className="font-medium text-sm">
				{t("memoryPage.owner.exclusions.title")}
			</h4>
			<p className="text-faint text-xs">
				{t("memoryPage.owner.exclusions.hint")}
			</p>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="ownerLoadExclusions" error={q.error} />
			) : items.length === 0 ? (
				<p className="text-faint text-sm">
					{t("memoryPage.owner.exclusions.empty")}
				</p>
			) : (
				<ul className="-mx-2 flex flex-col">
					{items.map((e) => (
						<ExclusionRow key={`${e.kind}:${e.targetId}`} exclusion={e} />
					))}
				</ul>
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
					wrapperClassName="w-full sm:w-56"
					disabled={projects.isPending}
					onChange={(e) => setProjectId(e.target.value)}
				>
					<option value="">
						{t("memoryPage.owner.exclusions.pickProject")}
					</option>
					{choices.map((p) => (
						<option key={p.id} value={p.id}>
							{p.name}
						</option>
					))}
				</Select>
				<Button
					type="submit"
					icon="minusCircle"
					disabled={!projectId || put.isPending}
				>
					{t("memoryPage.owner.exclusions.excludeProject")}
				</Button>
			</form>
			<MemoryError step="ownerSaveExclusion" error={put.error} />
		</div>
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
		<li
			className="flex min-h-9 flex-col gap-1 rounded-md px-2 py-1.5"
			data-exclusion={`${e.kind}:${e.targetId}`}
		>
			<div className="flex flex-wrap items-center gap-2">
				<Badge>{t(`memoryPage.owner.exclusions.kind.${e.kind}`)}</Badge>
				<span className="min-w-0 flex-1 truncate text-sm" title={e.targetId}>
					{name}
				</span>
				<span className="text-faint text-xs">
					{formatRelative(e.createdAt)}
				</span>
				<Button
					size="sm"
					variant="ghost"
					disabled={del.isPending}
					onClick={() => del.mutate({ kind: e.kind, targetId: e.targetId })}
				>
					{t("memoryPage.owner.exclusions.remove")}
				</Button>
			</div>
			<MemoryError step="ownerDeleteExclusion" error={del.error} />
		</li>
	);
}
