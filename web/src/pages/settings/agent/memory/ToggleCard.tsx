/**
 * 记忆页「项目」卡（Q6、R11）：每个未归档项目一个开关；没单独设置过的跟随全局总开关。
 * 改动对新启动的 agent 生效，运行中需要重启的会话由页顶的生效状态条列出。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemoryProjectToggle,
	useMemoryProjects,
	usePutMemoryProject,
} from "../../../../api/memory-queries";
import { Badge, Card, ProjectBadge } from "../../../../components/ui/data";
import {
	EmptyState,
	Spinner,
	Toggle,
} from "../../../../components/ui/primitives";
import { MemoryError } from "./errors";

export function ToggleCard(): ReactNode {
	const { t } = useTranslation();
	const q = useMemoryProjects();
	const put = usePutMemoryProject();
	const items = q.data?.items ?? [];
	return (
		<Card title={t("memoryPage.projects.title")} icon="folder">
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadProjects" error={q.error} />
			) : items.length === 0 ? (
				<EmptyState icon="folder" title={t("memoryPage.projects.empty")} />
			) : (
				<div className="flex flex-col gap-2">
					<p className="text-faint text-xs">{t("memoryPage.projects.hint")}</p>
					<ul className="-mx-2 flex flex-col">
						{items.map((p) => (
							<ProjectRow
								key={p.projectId}
								project={p}
								busy={put.isPending && put.variables?.id === p.projectId}
								onChange={(enabled) => put.mutate({ id: p.projectId, enabled })}
							/>
						))}
					</ul>
					<MemoryError step="saveProject" error={put.error} />
				</div>
			)}
		</Card>
	);
}

function ProjectRow({
	project: p,
	busy,
	onChange,
}: {
	project: MemoryProjectToggle;
	busy: boolean;
	onChange: (enabled: boolean) => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<li className="flex min-h-10 items-center gap-3 rounded-md px-2 py-1.5">
			<ProjectBadge name={p.name} />
			<span className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5 text-sm">
				<span className="min-w-0 truncate font-medium">{p.name}</span>
				{!p.overridden && (
					<Badge>{t("memoryPage.projects.followsGlobal")}</Badge>
				)}
			</span>
			<Toggle
				checked={p.enabled}
				disabled={busy}
				onChange={onChange}
				label={
					<span className="sr-only">
						{t("memoryPage.projects.toggle", { name: p.name })}
					</span>
				}
			/>
		</li>
	);
}
