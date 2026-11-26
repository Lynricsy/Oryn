/**
 * 设置 → 分享与 Artifacts 的「已发布的 Artifacts」分组（M14 8.4）：
 * - 首行是自动发布开关：把新建的 artifact（agent 创建与用户导入）以私有方式发布，并跟着最新版本更新。
 * - 其后每个已发布的 artifact 一行：标题（点进查看器）、类型图标 · 访问级别、版本、访客数 · 复制链接（悬停出现；
 *   有访问码时第二行是访问码）· ⋯（取消发布，先确认）。加载中、失败、空三种占位。
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
import { CopyButton } from "../../components/ui/CopyButton";
import { Badge } from "../../components/ui/data";
import { ErrorText, Spinner, Toggle } from "../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupRow,
	Reveal,
} from "../../components/ui/workbench";
import { kindIcon } from "../../lib/artifacts/kinds";
import { confirmAction } from "../../lib/confirm";
import { RowMenu } from "./SharesSettings";

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
		<GroupRow
			icon={kindIcon(a.kind)}
			title={
				<Link
					to={`/artifacts/${a.id}`}
					className="min-w-0 truncate hover:underline"
					data-testid="artifact-publication"
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
					<Reveal>
						<CopyButton
							text={
								p.code
									? `${p.url}\n${t("artifacts.share.codeLine", { code: p.code })}`
									: p.url
							}
							ariaLabel={t("artifacts.share.copyLink")}
						/>
					</Reveal>
					<RowMenu
						label={t("share.settings.more", { name: a.title })}
						items={[
							{
								key: "unpublish",
								icon: "trash",
								label: t("artifacts.share.unpublishConfirm"),
								danger: true,
								disabled: unpublish.isPending,
								onSelect: async () => {
									if (
										await confirmAction({
											title: t("artifacts.share.settings.unpublishTitle", {
												title: a.title,
											}),
											body: t("artifacts.share.settings.unpublishBody"),
											action: t("artifacts.share.unpublishConfirm"),
											danger: true,
										})
									)
										unpublish.mutate(a.id);
								},
							},
						]}
					/>
				</>
			}
		>
			{unpublish.error ? <ErrorText error={unpublish.error} /> : undefined}
		</GroupRow>
	);
}

export function ArtifactPublishing(): ReactNode {
	const { t } = useTranslation();
	const prefs = useArtifactPrefs();
	const setPrefs = useSetArtifactPrefs();
	const list = useArtifactPublications();
	return (
		<Group title={t("artifacts.share.settings.title")}>
			<GroupList>
				{/* 自动发布开关只有一行，作为本组首行，不单独占一组 */}
				<GroupRow
					icon="sparkle"
					title={t("artifacts.share.settings.autoTitle")}
					meta={t("artifacts.share.settings.autoPublish")}
					trailing={
						prefs.isPending ? (
							<Spinner />
						) : prefs.isError ? undefined : (
							<Toggle
								hideLabel
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
						)
					}
				>
					{prefs.error || setPrefs.error ? (
						<ErrorText error={prefs.error ?? setPrefs.error} />
					) : undefined}
				</GroupRow>
				{list.data?.map((item) => (
					<PublicationRow key={item.publication.id} item={item} />
				))}
			</GroupList>
			{list.isPending && <Spinner />}
			<ErrorText error={list.error} />
			{list.data?.length === 0 && (
				<p className="px-1.5 text-faint text-sm">
					{t("artifacts.share.settings.empty")}
				</p>
			)}
		</Group>
	);
}
