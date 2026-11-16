/**
 * 设置 › 分享 里的 Artifacts 两块（M14 8.4）：
 * - 自动发布开关：把新建的 artifact（agent 创建与用户导入）以私有方式发布，并跟着最新版本更新。
 * - 「已发布的 Artifacts」：标题（点进查看器）、类型图标、访问级别、版本、访客数、复制链接（有访问码时第二行是访问码）、
 *   两步取消发布；加载中、失败、空三种占位。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	type ArtifactPublicationListItem,
	useArtifactPrefs,
	useArtifactPublications,
	useSetArtifactPrefs,
	useUnpublishArtifact,
} from "../../api/artifact-queries";
import { ConfirmButton } from "../../components/share/ConfirmButton";
import { CopyButton } from "../../components/ui/CopyButton";
import { Badge, Card, ListRow } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import { ErrorText, Spinner, Toggle } from "../../components/ui/primitives";
import { kindIcon } from "../../lib/artifacts/kinds";

const ACCESS_ICON = { private: "lock", code: "key", public: "globe" } as const;

function PublicationRow({
	item,
}: {
	item: ArtifactPublicationListItem;
}): ReactNode {
	const { t } = useTranslation();
	const unpublish = useUnpublishArtifact();
	const { artifact: a, publication: p } = item;
	return (
		<li data-testid="artifact-publication">
			<ListRow
				leading={
					<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
						<Icon name={kindIcon(a.kind)} size={15} />
					</span>
				}
				title={
					<Link
						to={`/artifacts/${a.id}`}
						className="min-w-0 truncate font-medium hover:underline"
					>
						{a.title}
					</Link>
				}
				meta={
					<>
						<Badge icon={ACCESS_ICON[p.access]}>
							{t(`artifacts.share.access.${p.access}`)}
						</Badge>
						<span className="font-mono tabular-nums">
							{t("artifacts.viewer.version", { n: p.version })}
						</span>
						{p.visitors > 0 && (
							<span title={t("artifacts.share.visitorsHint")}>
								{t("artifacts.share.visitors", { count: p.visitors })}
							</span>
						)}
					</>
				}
				trailing={
					<>
						<CopyButton
							text={
								p.code
									? `${p.url}\n${t("artifacts.share.codeLine", { code: p.code })}`
									: p.url
							}
							ariaLabel={t("artifacts.share.copyLink")}
						/>
						<ConfirmButton
							label={t("artifacts.share.unpublishConfirm")}
							confirmLabel={t("artifacts.share.settings.unpublishConfirm")}
							disabled={unpublish.isPending}
							onConfirm={() => unpublish.mutate(a.id)}
						/>
					</>
				}
			/>
			<ErrorText error={unpublish.error} />
		</li>
	);
}

export function ArtifactPublishing(): ReactNode {
	const { t } = useTranslation();
	const prefs = useArtifactPrefs();
	const setPrefs = useSetArtifactPrefs();
	const list = useArtifactPublications();
	return (
		<>
			<Card title={t("artifacts.share.settings.autoTitle")} icon="shapes">
				{prefs.isPending ? (
					<Spinner />
				) : prefs.isError ? (
					<ErrorText error={prefs.error} />
				) : (
					<Toggle
						disabled={setPrefs.isPending}
						// 请求进行中先显示要改成的值
						checked={
							setPrefs.isPending
								? setPrefs.variables.autoPublish
								: prefs.data.autoPublish
						}
						label={t("artifacts.share.settings.autoPublish")}
						onChange={(autoPublish) => setPrefs.mutate({ autoPublish })}
					/>
				)}
				<ErrorText error={setPrefs.error} />
			</Card>
			<Card title={t("artifacts.share.settings.title")} icon="globe">
				{list.isPending ? (
					<Spinner />
				) : list.isError ? (
					<ErrorText error={list.error} />
				) : list.data.length === 0 ? (
					<p className="text-faint text-sm">
						{t("artifacts.share.settings.empty")}
					</p>
				) : (
					<ul className="-mx-2 flex flex-col">
						{list.data.map((item) => (
							<PublicationRow key={item.publication.id} item={item} />
						))}
					</ul>
				)}
			</Card>
		</>
	);
}
