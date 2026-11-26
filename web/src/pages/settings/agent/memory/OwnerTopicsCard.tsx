/**
 * 「主题」分组（M17 R19）：注入「关于主人」的各主题（名称、范围、固定种子、状态、最近刷新时间、当前内容），
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
import {
	EmptyPanel,
	GroupNote,
} from "../../../../components/settings/agent-config-ui";
import { Badge } from "../../../../components/ui/data";
import { Button, Spinner } from "../../../../components/ui/primitives";
import { Group, GroupList } from "../../../../components/ui/workbench";
import { formatDateTime, formatRelative } from "../../../../lib/format";
import { MemoryError } from "./errors";
import { ItemMeta, ItemRow } from "./rows";

export function OwnerTopicsCard(): ReactNode {
	const { t } = useTranslation();
	const q = useOwnerMemoryTopics();
	const curate = useCurateOwnerMemoryTopics();
	const items = q.data?.items ?? [];
	return (
		<Group
			title={t("memoryPage.owner.topics.title")}
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
			<MemoryError step="ownerCurate" error={curate.error} />
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="ownerLoadTopics" error={q.error} />
			) : (
				<>
					{items.length === 0 ? (
						<EmptyPanel>{t("memoryPage.owner.topics.empty")}</EmptyPanel>
					) : (
						<GroupList>
							{items.map((topic) => (
								<TopicRow key={topic.id} topic={topic} />
							))}
						</GroupList>
					)}
					{q.data?.lastCuration && <Curation curation={q.data.lastCuration} />}
				</>
			)}
			<GroupNote>{t("memoryPage.owner.topics.hint")}</GroupNote>
		</Group>
	);
}

function TopicRow({ topic: tp }: { topic: OwnerMemoryTopic }): ReactNode {
	const { t } = useTranslation();
	const refresh = useRefreshOwnerMemoryTopic();
	const [open, setOpen] = useState(false);
	return (
		<ItemRow data-topic={tp.id}>
			<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
				<span className="min-w-0 truncate font-[550] text-fg text-sm">
					{tp.name}
				</span>
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
			</div>
			<p className="break-words text-[12.5px] text-muted">
				{t("memoryPage.owner.topics.scope", { scope: tp.scope })}
			</p>
			<ItemMeta
				actions={
					<>
						{tp.content && (
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
						)}
						<Button
							size="sm"
							variant="ghost"
							icon="refresh"
							disabled={refresh.isPending}
							onClick={() => refresh.mutate(tp.id)}
						>
							{t("memoryPage.owner.topics.refresh")}
						</Button>
					</>
				}
			>
				<span>
					{tp.lastRefreshedAt === undefined
						? t("memoryPage.mental.neverRefreshed")
						: t("memoryPage.mental.refreshedAt", {
								when: formatRelative(tp.lastRefreshedAt),
							})}
					{refresh.isSuccess && ` · ${t("memoryPage.mental.submitted")}`}
				</span>
				{!tp.content && <span>{t("memoryPage.mental.noContent")}</span>}
			</ItemMeta>
			{open && tp.content && (
				<p className="whitespace-pre-wrap break-words rounded-[11px] bg-inset px-3 py-2 text-sm">
					{tp.content}
				</p>
			)}
			<MemoryError step="ownerRefreshTopic" error={refresh.error} />
		</ItemRow>
	);
}

/** 最近一次整理：新建 / 保留 / 归档的主题；出错时附原因。 */
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
		<div
			role={c.error ? "alert" : undefined}
			className="flex flex-col gap-0.5 px-1.5 text-[12.5px] text-muted"
		>
			<p className={`font-[550] ${c.error ? "text-err" : "text-fg"}`}>
				{t("memoryPage.owner.topics.lastCuration", {
					when: formatDateTime(c.at),
				})}
			</p>
			{rows.map((r) => (
				<p key={r.key} className="break-words">
					{t(`memoryPage.owner.topics.curation.${r.key}`, {
						names: r.names.length > 0 ? r.names.join(sep) : "—",
					})}
				</p>
			))}
			{c.error && (
				<p className="break-words text-err">
					{t("memoryPage.status.detail", { detail: c.error })}
				</p>
			)}
		</div>
	);
}
