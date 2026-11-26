/**
 * 「mental models」分组（R21）：按项目或全局列出，手动刷新（异步，提交后稍等再看内容）。
 * 全局的 user-preferences 由控制面维护，只从已批准的全局记忆中提炼。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemoryMentalModel,
	useMemoryMentalModels,
	useMemoryProjects,
	useRefreshMemoryMentalModel,
} from "../../../../api/memory-queries";
import { EmptyPanel } from "../../../../components/settings/agent-config-ui";
import { Badge } from "../../../../components/ui/data";
import { Button, Select, Spinner } from "../../../../components/ui/primitives";
import { Group, GroupList } from "../../../../components/ui/workbench";
import { formatRelative } from "../../../../lib/format";
import { MemoryError } from "./errors";
import { ItemMeta, ItemRow } from "./rows";

/** 选择器里「全局」的取值（项目 id 都以 `prj_` 开头，不会撞上）。 */
const GLOBAL = "global";

export function MentalModelsCard(): ReactNode {
	const { t } = useTranslation();
	const projects = useMemoryProjects();
	const [scope, setScope] = useState(GLOBAL);
	const q = useMemoryMentalModels(scope === GLOBAL ? undefined : scope);
	const items = q.data?.items ?? [];
	return (
		<Group title={t("memoryPage.mental.title")}>
			<Select
				aria-label={t("memoryPage.mental.scope")}
				value={scope}
				className="w-full sm:w-56"
				options={[
					{ value: GLOBAL, label: t("memoryPage.mental.global") },
					...(projects.data?.items ?? []).map((p) => ({
						value: p.projectId,
						label: p.name,
					})),
				]}
				onChange={setScope}
			/>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadMentalModels" error={q.error} />
			) : items.length === 0 ? (
				<EmptyPanel>{t("memoryPage.mental.empty")}</EmptyPanel>
			) : (
				<GroupList>
					{items.map((m) => (
						<ModelRow key={m.id} model={m} />
					))}
				</GroupList>
			)}
		</Group>
	);
}

function ModelRow({ model: m }: { model: MemoryMentalModel }): ReactNode {
	const { t } = useTranslation();
	const refresh = useRefreshMemoryMentalModel();
	const refreshed = m.lastRefreshedAt ? Date.parse(m.lastRefreshedAt) : NaN;
	return (
		<ItemRow>
			<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
				<span className="min-w-0 truncate font-[550] text-fg text-sm">
					{m.name}
				</span>
				{m.global && (
					<Badge icon="globe">{t("memoryPage.browse.global")}</Badge>
				)}
				<code
					className="min-w-0 truncate font-mono text-faint text-xs"
					title={m.id}
				>
					{m.id}
				</code>
			</div>
			{m.sourceQuery && (
				<p className="break-words text-[12.5px] text-muted">
					{t("memoryPage.mental.source", { query: m.sourceQuery })}
				</p>
			)}
			{m.content ? (
				<p className="line-clamp-6 whitespace-pre-wrap break-words text-fg text-sm">
					{m.content}
				</p>
			) : (
				<p className="text-muted text-sm">{t("memoryPage.mental.noContent")}</p>
			)}
			<ItemMeta
				actions={
					<Button
						size="sm"
						variant="ghost"
						icon="refresh"
						disabled={refresh.isPending}
						onClick={() => refresh.mutate(m.id)}
					>
						{t("memoryPage.mental.refresh")}
					</Button>
				}
			>
				<span>
					{Number.isFinite(refreshed)
						? t("memoryPage.mental.refreshedAt", {
								when: formatRelative(refreshed),
							})
						: t("memoryPage.mental.neverRefreshed")}
					{refresh.isSuccess && ` · ${t("memoryPage.mental.submitted")}`}
				</span>
			</ItemMeta>
			<MemoryError step="refreshMentalModel" error={refresh.error} />
		</ItemRow>
	);
}
