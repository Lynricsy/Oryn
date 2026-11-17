/**
 * 记忆页「助手记忆 › 主题」卡（M17 R19）：注入「关于主人」的各主题（名称、范围、固定种子、状态、最近刷新时间、当前内容），
 * 遗忘或失效之后主题标为「等待刷新」，刷新完成前不进入注入。每个主题可以立即刷新（异步，提交后稍等再看），
 * 也可以立即运行一次主题整理（新建、归档主题），下方显示最近一次整理的结果。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type OwnerMemoryCuration,
	type OwnerMemoryTopic,
	useCurateOwnerMemoryTopics,
	useOwnerMemoryTopics,
	useRefreshOwnerMemoryTopic,
} from "../../../../api/owner-memory-queries";
import { Badge, Callout, Card } from "../../../../components/ui/data";
import { Button, Spinner } from "../../../../components/ui/primitives";
import { formatDateTime, formatRelative } from "../../../../lib/format";
import { MemoryError } from "./errors";

export function OwnerTopicsCard(): ReactNode {
	const { t } = useTranslation();
	const q = useOwnerMemoryTopics();
	const curate = useCurateOwnerMemoryTopics();
	const items = q.data?.items ?? [];
	return (
		<Card
			title={t("memoryPage.owner.topics.title")}
			icon="brain"
			action={
				<Button
					size="sm"
					icon="sparkle"
					disabled={curate.isPending}
					onClick={() => curate.mutate()}
				>
					{t("memoryPage.owner.topics.curate")}
				</Button>
			}
		>
			<div className="flex flex-col gap-2">
				<p className="text-faint text-xs">
					{t("memoryPage.owner.topics.hint")}
				</p>
				<MemoryError step="ownerCurate" error={curate.error} />
				{q.isPending ? (
					<Spinner />
				) : q.error ? (
					<MemoryError step="ownerLoadTopics" error={q.error} />
				) : (
					<>
						{items.length === 0 ? (
							<p className="text-faint text-sm">
								{t("memoryPage.owner.topics.empty")}
							</p>
						) : (
							<ul className="flex flex-col gap-2">
								{items.map((topic) => (
									<TopicRow key={topic.id} topic={topic} />
								))}
							</ul>
						)}
						{q.data?.lastCuration && (
							<Curation curation={q.data.lastCuration} />
						)}
					</>
				)}
			</div>
		</Card>
	);
}

function TopicRow({ topic: tp }: { topic: OwnerMemoryTopic }): ReactNode {
	const { t } = useTranslation();
	const refresh = useRefreshOwnerMemoryTopic();
	const [open, setOpen] = useState(false);
	return (
		<li
			className="flex flex-col gap-1.5 rounded-md border border-border px-3 py-2"
			data-topic={tp.id}
		>
			<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
				<span className="min-w-0 truncate font-medium text-sm">{tp.name}</span>
				{tp.seed && (
					<Badge icon="pin">{t("memoryPage.owner.topics.seed")}</Badge>
				)}
				<Badge tone={tp.status === "active" ? "ok" : "neutral"}>
					{t(`memoryPage.owner.topics.status.${tp.status}`)}
				</Badge>
				{tp.stale && (
					<Badge tone="warn" icon="hourglass">
						{t("memoryPage.owner.topics.stale")}
					</Badge>
				)}
				<Button
					size="sm"
					variant="ghost"
					icon="refresh"
					className="ml-auto"
					disabled={refresh.isPending}
					onClick={() => refresh.mutate(tp.id)}
				>
					{t("memoryPage.owner.topics.refresh")}
				</Button>
			</div>
			<p className="break-words text-muted text-xs">
				{t("memoryPage.owner.topics.scope", { scope: tp.scope })}
			</p>
			<span className="flex flex-wrap items-center gap-2 text-faint text-xs">
				<span>
					{tp.lastRefreshedAt === undefined
						? t("memoryPage.mental.neverRefreshed")
						: t("memoryPage.mental.refreshedAt", {
								when: formatRelative(tp.lastRefreshedAt),
							})}
					{refresh.isSuccess && ` · ${t("memoryPage.mental.submitted")}`}
				</span>
				{tp.content ? (
					<Button
						size="sm"
						variant="ghost"
						icon={open ? "chevronUp" : "chevronDown"}
						aria-expanded={open}
						onClick={() => setOpen(!open)}
					>
						{open
							? t("memoryPage.owner.topics.hideContent")
							: t("memoryPage.owner.topics.showContent")}
					</Button>
				) : (
					<span>{t("memoryPage.mental.noContent")}</span>
				)}
			</span>
			{open && tp.content && (
				<p className="whitespace-pre-wrap break-words rounded-md bg-inset px-3 py-2 text-sm">
					{tp.content}
				</p>
			)}
			<MemoryError step="ownerRefreshTopic" error={refresh.error} />
		</li>
	);
}

function Curation({
	curation: c,
}: {
	curation: OwnerMemoryCuration;
}): ReactNode {
	const { t } = useTranslation();
	const sep = t("memoryPage.listSep");
	const rows = [
		{ key: "created", names: c.created },
		{ key: "kept", names: c.kept },
		{ key: "archived", names: c.archived },
	] as const;
	return (
		<Callout
			tone={c.error ? "err" : "neutral"}
			title={t("memoryPage.owner.topics.lastCuration", {
				when: formatDateTime(c.at),
			})}
		>
			<div className="flex flex-col gap-0.5">
				{rows.map((r) => (
					<p key={r.key} className="break-words">
						{t(`memoryPage.owner.topics.curation.${r.key}`, {
							names: r.names.length > 0 ? r.names.join(sep) : "—",
						})}
					</p>
				))}
				{c.error && (
					<p className="break-words">
						{t("memoryPage.status.detail", { detail: c.error })}
					</p>
				)}
			</div>
		</Callout>
	);
}
