/**
 * 记忆页「mental models」卡（R21）：按项目或全局列出，手动刷新（异步，提交后稍等再看内容）。
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
import { Badge, Card } from "../../../../components/ui/data";
import { Button, Select, Spinner } from "../../../../components/ui/primitives";
import { formatRelative } from "../../../../lib/format";
import { MemoryError } from "./errors";

/** 选择器里「全局」的取值（项目 id 都以 `prj_` 开头，不会撞上）。 */
const GLOBAL = "";

export function MentalModelsCard(): ReactNode {
	const { t } = useTranslation();
	const projects = useMemoryProjects();
	const [scope, setScope] = useState(GLOBAL);
	const q = useMemoryMentalModels(scope === GLOBAL ? undefined : scope);
	const items = q.data?.items ?? [];
	return (
		<Card title={t("memoryPage.mental.title")} icon="brain">
			<div className="flex flex-col gap-2">
				<Select
					aria-label={t("memoryPage.mental.scope")}
					value={scope}
					wrapperClassName="w-full sm:w-56"
					onChange={(e) => setScope(e.target.value)}
				>
					<option value={GLOBAL}>{t("memoryPage.mental.global")}</option>
					{(projects.data?.items ?? []).map((p) => (
						<option key={p.projectId} value={p.projectId}>
							{p.name}
						</option>
					))}
				</Select>
				{q.isPending ? (
					<Spinner />
				) : q.error ? (
					<MemoryError step="loadMentalModels" error={q.error} />
				) : items.length === 0 ? (
					<p className="text-faint text-sm">{t("memoryPage.mental.empty")}</p>
				) : (
					<ul className="flex flex-col gap-2">
						{items.map((m) => (
							<ModelRow key={m.id} model={m} />
						))}
					</ul>
				)}
			</div>
		</Card>
	);
}

function ModelRow({ model: m }: { model: MemoryMentalModel }): ReactNode {
	const { t } = useTranslation();
	const refresh = useRefreshMemoryMentalModel();
	const refreshed = m.lastRefreshedAt ? Date.parse(m.lastRefreshedAt) : NaN;
	return (
		<li className="flex flex-col gap-1.5 rounded-md border border-border px-3 py-2">
			<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
				<span className="min-w-0 truncate font-medium text-sm">{m.name}</span>
				{m.global && (
					<Badge icon="globe">{t("memoryPage.browse.global")}</Badge>
				)}
				<code
					className="min-w-0 truncate font-mono text-faint text-xs"
					title={m.id}
				>
					{m.id}
				</code>
				<Button
					size="sm"
					variant="ghost"
					icon="refresh"
					className="ml-auto"
					disabled={refresh.isPending}
					onClick={() => refresh.mutate(m.id)}
				>
					{t("memoryPage.mental.refresh")}
				</Button>
			</div>
			{m.sourceQuery && (
				<p className="break-words text-muted text-xs">
					{t("memoryPage.mental.source", { query: m.sourceQuery })}
				</p>
			)}
			{m.content ? (
				<p className="line-clamp-6 whitespace-pre-wrap break-words text-sm">
					{m.content}
				</p>
			) : (
				<p className="text-faint text-sm">{t("memoryPage.mental.noContent")}</p>
			)}
			<span className="text-faint text-xs">
				{Number.isFinite(refreshed)
					? t("memoryPage.mental.refreshedAt", {
							when: formatRelative(refreshed),
						})
					: t("memoryPage.mental.neverRefreshed")}
				{refresh.isSuccess && ` · ${t("memoryPage.mental.submitted")}`}
			</span>
			<MemoryError step="refreshMentalModel" error={refresh.error} />
		</li>
	);
}
