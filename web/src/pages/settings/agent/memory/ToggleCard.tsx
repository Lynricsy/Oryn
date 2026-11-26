/**
 * 「项目」分组（Q6、R11）：每个未归档项目一行开关；没单独设置过的跟随全局总开关。
 * 改动对新启动的 agent 生效，运行中需要重启的会话由页顶的生效状态条列出。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemoryProjectToggle,
	useMemoryProjects,
	usePutMemoryProject,
} from "../../../../api/memory-queries";
import {
	AvatarRow,
	EmptyPanel,
	GroupNote,
} from "../../../../components/settings/agent-config-ui";
import { Badge, ProjectBadge } from "../../../../components/ui/data";
import { Spinner, Toggle } from "../../../../components/ui/primitives";
import { Group, GroupList } from "../../../../components/ui/workbench";
import { MemoryError } from "./errors";

export function ToggleCard(): ReactNode {
	const { t } = useTranslation();
	const q = useMemoryProjects();
	const put = usePutMemoryProject();
	const items = q.data?.items ?? [];
	return (
		<Group title={t("memoryPage.projects.title")}>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadProjects" error={q.error} />
			) : items.length === 0 ? (
				<EmptyPanel>{t("memoryPage.projects.empty")}</EmptyPanel>
			) : (
				<>
					<GroupList>
						{items.map((p) => (
							<AvatarRow
								key={p.projectId}
								leading={<ProjectBadge name={p.name} />}
								title={
									<>
										<span className="min-w-0 truncate">{p.name}</span>
										{!p.overridden && (
											<Badge>{t("memoryPage.projects.followsGlobal")}</Badge>
										)}
									</>
								}
								trailing={
									<Toggle
										hideLabel
										checked={p.enabled}
										disabled={
											put.isPending && put.variables?.id === p.projectId
										}
										onChange={(enabled) =>
											put.mutate({ id: p.projectId, enabled })
										}
										label={t("memoryPage.projects.toggle", { name: p.name })}
									/>
								}
							/>
						))}
					</GroupList>
					<MemoryError step="saveProject" error={put.error} />
					<GroupNote>{t("memoryPage.projects.hint")}</GroupNote>
				</>
			)}
		</Group>
	);
}

/** 一个项目的记忆开关行（项目设置「Agent」分组也用它）。 */
export function MemoryProjectRow({
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
