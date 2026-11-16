/**
 * 设置 › Artifacts（M14 计划步骤 7.3）：列出已授权页面能力（读取项目文件、向模型提问）的 artifact，每行可以收回
 * （整个 artifact 的授权一起撤）。回收站里的 artifact 不列出。加载中、失败（可重试）、空三种占位。
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
import { formatRelative } from "../../lib/format";
import { ConfirmButton } from "../share/ConfirmButton";
import { Badge, Card, ListRow } from "../ui/data";
import { Icon } from "../ui/icons";
import { Button, EmptyState, ErrorText, Spinner } from "../ui/primitives";

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
		<li>
			<ListRow
				leading={
					<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
						<Icon name="shapes" size={15} />
					</span>
				}
				title={
					<Link
						to={`/artifacts/${consent.artifactId}`}
						className="min-w-0 truncate font-medium hover:underline"
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
					<ConfirmButton
						label={t("artifacts.settings.revoke")}
						confirmLabel={t("artifacts.settings.revokeConfirm")}
						disabled={revoke.isPending}
						onConfirm={() => revoke.mutate(consent.artifactId)}
					/>
				}
			/>
			<ErrorText error={revoke.error} />
		</li>
	);
}

export function ArtifactsSection(): ReactNode {
	const { t } = useTranslation();
	const q = useArtifactConsents();
	const projects = useProjects();
	if (q.isPending)
		return (
			<Card>
				<Spinner />
			</Card>
		);
	if (q.error)
		return (
			<Card>
				<EmptyState icon="alert" title={t("artifacts.settings.failed")}>
					<div className="flex flex-col items-center gap-3">
						<ErrorText error={q.error} />
						<Button size="sm" icon="refresh" onClick={() => void q.refetch()}>
							{t("artifacts.settings.retry")}
						</Button>
					</div>
				</EmptyState>
			</Card>
		);
	if (q.data.length === 0)
		return (
			<Card>
				<EmptyState icon="shapes" title={t("artifacts.settings.empty")}>
					{t("artifacts.settings.emptyHint")}
				</EmptyState>
			</Card>
		);
	const names = projects.data
		? new Map(projects.data.projects.map((p) => [p.id, p.name]))
		: null;
	return (
		<Card title={t("artifacts.settings.granted")} icon="shapes">
			<p className="mb-2 text-muted text-xs">{t("artifacts.settings.hint")}</p>
			<ul className="-mx-2 flex flex-col">
				{q.data.map((c) => (
					<ConsentRow
						key={c.artifactId}
						consent={c}
						project={
							!c.projectId ? null : (names?.get(c.projectId) ?? undefined)
						}
					/>
				))}
			</ul>
		</Card>
	);
}
