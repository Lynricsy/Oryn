/**
 * 设置 → 分享与 Artifacts 的「公开的线程」「公开的文件」两组（M5 计划 5.3、M14 8.4）。
 * - 线程行：标题点进线程 · 模式、公开时间、确认过的疑似密钥 · 复制链接（悬停出现）· ⋯（撤销分享，先确认）。
 * - 文件行：名称打开公开链接 · 大小、所属线程、公开时间 · 复制链接（悬停出现）· ⋯（图片可预览；撤回，先确认）。
 * 两类都为空时只显示一组空态。Artifacts 的发布与页面能力授权在同页的后两组（`ArtifactPublishing`、`ArtifactsSection`）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	type PublishedFile,
	type ThreadShare,
	useRevokePublishedFile,
	useRevokeShare,
	useShareOverview,
} from "../../api/share-queries";
import { ShareModeBadge } from "../../components/share/share-url";
import { CopyButton } from "../../components/ui/CopyButton";
import { Badge } from "../../components/ui/data";
import { ImageViewer } from "../../components/ui/ImageViewer";
import {
	ErrorText,
	IconButton,
	Menu,
	type MenuItem,
	Spinner,
} from "../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupRow,
	Reveal,
} from "../../components/ui/workbench";
import { confirmAction } from "../../lib/confirm";
import { formatBytes, formatRelative } from "../../lib/format";
import { rasterMime } from "../../lib/image-src";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";

/** 行尾 ⋯ 菜单（分享与 Artifacts 各组共用）。 */
export function RowMenu({
	label,
	items,
}: {
	label: string;
	items: MenuItem[];
}): ReactNode {
	return (
		<Menu
			trigger={(p) => <IconButton icon="more" box="sm" label={label} {...p} />}
			items={items}
		/>
	);
}

function ShareRow({ share }: { share: ThreadShare }): ReactNode {
	const { t } = useTranslation();
	const revoke = useRevokeShare();
	const title = parseTitleCard(share.threadTitle).text || t("thread.untitled");
	return (
		<GroupRow
			icon={share.mode === "live" ? "broadcast" : "camera"}
			title={
				<Link
					to={`/t/${share.threadId}`}
					className="min-w-0 truncate hover:underline"
					title={share.threadTitle}
				>
					{title}
				</Link>
			}
			meta={
				<>
					<ShareModeBadge mode={share.mode} createdAt={share.createdAt} />
					<span>
						{t("share.settings.createdAt", {
							when: formatRelative(share.createdAt),
						})}
					</span>
					{share.findings > 0 && (
						<Badge tone="warn" icon="alert">
							{t("share.settings.findings", { n: share.findings })}
						</Badge>
					)}
				</>
			}
			trailing={
				<>
					<Reveal>
						<CopyButton text={share.url} ariaLabel={t("share.page.copyLink")} />
					</Reveal>
					<RowMenu
						label={t("share.settings.more", { name: title })}
						items={[
							{
								key: "revoke",
								icon: "trash",
								label: t("share.dialog.revoke"),
								danger: true,
								disabled: revoke.isPending,
								onSelect: async () => {
									if (
										await confirmAction({
											title: t("share.settings.revokeTitle", { title }),
											body: t("share.settings.revokeBody"),
											action: t("share.dialog.revoke"),
											danger: true,
										})
									)
										revoke.mutate(share.id);
								},
							},
						]}
					/>
				</>
			}
		>
			{revoke.error ? <ErrorText error={revoke.error} /> : undefined}
		</GroupRow>
	);
}

function FileRow({ file }: { file: PublishedFile }): ReactNode {
	const { t } = useTranslation();
	const [previewOpen, setPreviewOpen] = useState(false);
	const revoke = useRevokePublishedFile();
	const thread = useIndex((s) =>
		file.threadId ? s.threads[file.threadId] : undefined,
	);
	const isImage = rasterMime(file.mime) !== null && Boolean(file.url);
	const items: MenuItem[] = [
		...(isImage
			? [
					{
						key: "preview",
						icon: "image" as const,
						label: t("image.lightbox.fullscreen"),
						onSelect: () => setPreviewOpen(true),
					},
				]
			: []),
		{
			key: "unpublish",
			icon: "trash",
			label: t("publish.unpublish"),
			danger: true,
			disabled: revoke.isPending,
			onSelect: async () => {
				if (
					await confirmAction({
						title: t("share.settings.unpublishTitle", { name: file.name }),
						body: t("share.settings.unpublishBody"),
						action: t("publish.unpublish"),
						danger: true,
					})
				)
					revoke.mutate(file.id);
			},
		},
	];
	return (
		<GroupRow
			icon={isImage ? "image" : "file"}
			title={
				<a
					href={file.url}
					target="_blank"
					rel="noopener"
					className="min-w-0 truncate hover:underline"
					title={file.url}
				>
					{file.name}
				</a>
			}
			meta={
				<>
					<span className="tabular-nums">{formatBytes(file.size)}</span>
					{file.threadId && (
						<Link
							to={`/t/${file.threadId}`}
							className="min-w-0 truncate hover:text-fg hover:underline"
						>
							{thread
								? parseTitleCard(thread.title).text || t("thread.untitled")
								: t("share.settings.fileThread")}
						</Link>
					)}
					<span>
						{t("share.settings.createdAt", {
							when: formatRelative(file.createdAt),
						})}
					</span>
				</>
			}
			trailing={
				<>
					<Reveal>
						<CopyButton text={file.url} ariaLabel={t("publish.copyUrl")} />
					</Reveal>
					<RowMenu
						label={t("share.settings.more", { name: file.name })}
						items={items}
					/>
				</>
			}
		>
			{previewOpen && isImage && (
				<ImageViewer
					src={file.url}
					alt={file.name}
					caption={file.name}
					filename={file.name}
					onClose={() => setPreviewOpen(false)}
				/>
			)}
			{revoke.error ? <ErrorText error={revoke.error} /> : undefined}
		</GroupRow>
	);
}

/** 公开的线程与文件；两类都为空时只一组空态。 */
export default function SharesSettings(): ReactNode {
	const { t } = useTranslation();
	const q = useShareOverview();
	const shares = q.data?.shares ?? [];
	const files = q.data?.files ?? [];
	if (q.isPending || q.error)
		return (
			<Group title={t("share.settings.threads")}>
				{q.isPending && <Spinner />}
				<ErrorText error={q.error} />
			</Group>
		);
	if (shares.length === 0 && files.length === 0)
		return (
			<Group title={t("share.settings.public")}>
				<p className="px-1.5 text-faint text-sm">
					{t("share.settings.empty")}
					{" · "}
					{t("share.settings.emptyHint")}
				</p>
			</Group>
		);
	return (
		<>
			<Group title={t("share.settings.threads")}>
				{shares.length === 0 ? (
					<p className="px-1.5 text-faint text-sm">
						{t("share.settings.noThreads")}
					</p>
				) : (
					<GroupList>
						{shares.map((s) => (
							<ShareRow key={s.id} share={s} />
						))}
					</GroupList>
				)}
			</Group>
			<Group title={t("share.settings.files")}>
				{files.length === 0 ? (
					<p className="px-1.5 text-faint text-sm">
						{t("share.settings.noFiles")}
					</p>
				) : (
					<GroupList>
						{files.map((f) => (
							<FileRow key={f.id} file={f} />
						))}
					</GroupList>
				)}
			</Group>
		</>
	);
}
