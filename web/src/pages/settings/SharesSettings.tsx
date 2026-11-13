/**
 * 设置页「公开分享」总览（M5 计划 5.3，default export，由设置页接入）：
 * 「公开的线程」（标题点进线程、模式、时间、复制、两步撤销）与「公开的文件」（名称、大小、所属线程、复制、两步撤回）。
 * 两类都为空时只显示一个空态。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	type PublishedFile,
	type ThreadShare,
	useRevokePublishedFile,
	useRevokeShare,
	useShareOverview,
} from "../../api/share-queries";
import { ConfirmButton } from "../../components/share/ConfirmButton";
import { ShareModeBadge } from "../../components/share/share-url";
import { CopyButton } from "../../components/ui/CopyButton";
import { Badge, Card, ListRow } from "../../components/ui/data";
import { Icon, type IconName } from "../../components/ui/icons";
import { EmptyState, ErrorText, Spinner } from "../../components/ui/primitives";
import { fileIcon } from "../../lib/file-icon";
import { formatBytes, formatRelative } from "../../lib/format";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";

function LeadIcon({ name }: { name: IconName }) {
	return (
		<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
			<Icon name={name} size={15} />
		</span>
	);
}

function ShareRow({ share }: { share: ThreadShare }): ReactNode {
	const { t } = useTranslation();
	const revoke = useRevokeShare();
	const title = parseTitleCard(share.threadTitle).text || t("thread.untitled");
	return (
		<li>
			<ListRow
				leading={
					<LeadIcon name={share.mode === "live" ? "broadcast" : "camera"} />
				}
				title={
					<Link
						to={`/t/${share.threadId}`}
						className="min-w-0 truncate font-medium hover:underline"
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
						<CopyButton text={share.url} ariaLabel={t("share.page.copyLink")} />
						<ConfirmButton
							label={t("share.dialog.revoke")}
							confirmLabel={t("share.dialog.revokeConfirm")}
							disabled={revoke.isPending}
							onConfirm={() => revoke.mutate(share.id)}
						/>
					</>
				}
			/>
			<ErrorText error={revoke.error} />
		</li>
	);
}

function FileRow({ file }: { file: PublishedFile }): ReactNode {
	const { t } = useTranslation();
	const revoke = useRevokePublishedFile();
	const thread = useIndex((s) =>
		file.threadId ? s.threads[file.threadId] : undefined,
	);
	return (
		<li>
			<ListRow
				leading={<LeadIcon name={fileIcon(file.name, file.mime)} />}
				title={
					<a
						href={file.url}
						target="_blank"
						rel="noopener"
						className="min-w-0 truncate font-medium hover:underline"
						title={file.url}
					>
						{file.name}
					</a>
				}
				meta={
					<>
						<Badge>{formatBytes(file.size)}</Badge>
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
						<CopyButton text={file.url} ariaLabel={t("publish.copyUrl")} />
						<ConfirmButton
							label={t("publish.unpublish")}
							confirmLabel={t("publish.unpublishConfirm")}
							disabled={revoke.isPending}
							onConfirm={() => revoke.mutate(file.id)}
						/>
					</>
				}
			/>
			<ErrorText error={revoke.error} />
		</li>
	);
}

export default function SharesSettings(): ReactNode {
	const { t } = useTranslation();
	const q = useShareOverview();
	const shares = q.data?.shares ?? [];
	const files = q.data?.files ?? [];
	if (q.isPending)
		return (
			<Card>
				<Spinner />
			</Card>
		);
	if (q.error)
		return (
			<Card>
				<ErrorText error={q.error} />
			</Card>
		);
	if (shares.length === 0 && files.length === 0)
		return (
			<Card>
				<EmptyState icon="globe" title={t("share.settings.empty")}>
					{t("share.settings.emptyHint")}
				</EmptyState>
			</Card>
		);
	return (
		<>
			<Card title={t("share.settings.threads")} icon="link">
				{shares.length === 0 ? (
					<p className="text-faint text-sm">{t("share.settings.noThreads")}</p>
				) : (
					<ul className="-mx-2 flex flex-col">
						{shares.map((s) => (
							<ShareRow key={s.id} share={s} />
						))}
					</ul>
				)}
			</Card>
			<Card title={t("share.settings.files")} icon="globe">
				{files.length === 0 ? (
					<p className="text-faint text-sm">{t("share.settings.noFiles")}</p>
				) : (
					<ul className="-mx-2 flex flex-col">
						{files.map((f) => (
							<FileRow key={f.id} file={f} />
						))}
					</ul>
				)}
			</Card>
		</>
	);
}
