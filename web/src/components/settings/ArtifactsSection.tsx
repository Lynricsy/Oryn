/**
 * 设置 → 分享与 Artifacts 的「Artifacts 页面能力」分组（M14 计划步骤 7.3）：列出已授权页面能力（读取项目文件、
 * 向模型提问）的 artifact，每行 ⋯ → 收回（整个 artifact 的授权一起撤，先确认）。回收站里的 artifact 不列出。
 * 加载中、失败（可重试）、空三种占位。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	type ArtifactConsent,
	useArtifactConsents,
	useRevokeArtifactConsents,
} from "../../api/artifact-queries";
import { useProjects } from "../../api/queries";
import { confirmAction } from "../../lib/confirm";
import { formatRelative } from "../../lib/format";
import { Badge } from "../ui/data";
import { Button, ErrorText, IconButton, Menu, Spinner } from "../ui/primitives";
import { Group, GroupList, GroupRow } from "../ui/workbench";

/** `project`：项目名；artifact 不属于任何项目时为 `null`，项目名还没取到时为 `undefined`（不显示）。 */
function ConsentRow({
	consent,
	project,
}: {
	consent: ArtifactConsent;
	project: string | null | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const revoke = useRevokeArtifactConsents();
	return (
		<GroupRow
			icon="shapes"
			title={
				<Link
					to={`/artifacts/${consent.artifactId}`}
					className="min-w-0 truncate hover:underline"
					title={consent.title}
				>
					{consent.title}
				</Link>
			}
			meta={
				<>
					{project !== undefined && (
						<span className="min-w-0 truncate">
							{project ?? t("artifacts.settings.noProject")}
						</span>
					)}
					{consent.capabilities.map((c) => (
						<Badge key={c}>{t(`artifacts.settings.caps.${c}`)}</Badge>
					))}
					<span>
						{t("artifacts.settings.grantedAt", {
							when: formatRelative(consent.grantedAt),
						})}
					</span>
				</>
			}
			trailing={
				<Menu
					trigger={(p) => (
						<IconButton
							icon="more"
							box="sm"
							label={t("artifacts.settings.more", { title: consent.title })}
							{...p}
						/>
					)}
					items={[
						{
							key: "revoke",
							icon: "trash",
							label: t("artifacts.settings.revoke"),
							danger: true,
							disabled: revoke.isPending,
							onSelect: async () => {
								if (
									await confirmAction({
										title: t("artifacts.settings.revokeTitle", {
											title: consent.title,
										}),
										body: t("artifacts.settings.revokeBody"),
										action: t("artifacts.settings.revoke"),
										danger: true,
									})
								)
									revoke.mutate(consent.artifactId);
							},
						},
					]}
				/>
			}
		>
			{revoke.error ? <ErrorText error={revoke.error} /> : undefined}
		</GroupRow>
	);
}

export function ArtifactsSection(): ReactNode {
	const { t } = useTranslation();
	const q = useArtifactConsents();
	const projects = useProjects();
	const names = projects.data
		? new Map(projects.data.projects.map((p) => [p.id, p.name]))
		: null;
	return (
		<Group title={t("artifacts.settings.granted")}>
			{q.isPending && <Spinner />}
			{q.error && (
				<div className="flex flex-wrap items-center gap-2 px-1.5">
					<span className="text-err text-sm">
						{t("artifacts.settings.failed")}
					</span>
					<ErrorText error={q.error} />
					<Button size="sm" icon="refresh" onClick={() => void q.refetch()}>
						{t("artifacts.settings.retry")}
					</Button>
				</div>
			)}
			{q.data && q.data.length > 0 && (
				<GroupList>
					{q.data.map((c) => (
						<ConsentRow
							key={c.artifactId}
							consent={c}
							project={
								!c.projectId ? null : (names?.get(c.projectId) ?? undefined)
							}
						/>
					))}
				</GroupList>
			)}
			{q.data && (
				<p className="px-1.5 text-faint text-sm">
					{q.data.length === 0
						? `${t("artifacts.settings.empty")} · ${t("artifacts.settings.emptyHint")}`
						: t("artifacts.settings.hint")}
				</p>
			)}
		</Group>
	);
}
